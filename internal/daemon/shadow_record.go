package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/osauer/canary/v2/internal/core"
	"github.com/osauer/canary/v2/internal/daemon/corestore"
	"github.com/osauer/canary/v2/internal/mandate"
	"github.com/osauer/canary/v2/internal/marketcal"
	"github.com/osauer/canary/v2/internal/register"
	"github.com/osauer/canary/v2/internal/risk"
	"github.com/osauer/canary/v2/internal/rpc"
)

// The shadow record (internal-docs/design/strategy-register.md, section 3;
// owner decision 2026-10-18 09:01 CEST: "shadow on the production path"). A
// strategy in shadow runs its own verdict code on a paper position, the
// shadow holding, which starts at zero and moves only by the record's own
// simulated fills. Its decisions, intents and fills are append-only events
// under stable keys, so a retry or a second evaluation never writes one
// twice. The only writer is the background refresh after the scheduled
// verdict; no order path reads the record, and nothing in it is a proposal.

const (
	shadowRecordType    = "shadow.record.v1"
	shadowRecordVersion = 1
	// shadowRecordLimit bounds one strategy's read; a monthly strategy
	// writes about three records a month.
	shadowRecordLimit = 10000
	// shadowServeRecords is how many records a read shows, newest first.
	shadowServeRecords = 12
)

// shadowScope is a strategy's event scope; shadowKey a record's stable key.
// Both carry the shadow prefix, so they never collide with an execution
// journal key.
func shadowScope(id string) string { return "shadow/" + id }

func shadowKey(id, rule, session, kind string) string {
	return "shadow:" + id + ":" + rule + ":" + session + ":" + kind
}

// shadowRecords reads one strategy's records in the order they were written.
func (s *Server) shadowRecords(ctx context.Context, id string) ([]rpc.ShadowRecord, error) {
	if s.coreStore == nil {
		return nil, errors.New("the shadow record is unavailable: no daemon store")
	}
	events, err := s.coreStore.LoadEvents(ctx, corestore.EventQuery{ScopeKey: shadowScope(id), Type: shadowRecordType, Limit: shadowRecordLimit})
	if err != nil {
		return nil, err
	}
	out := make([]rpc.ShadowRecord, 0, len(events))
	for _, ev := range events {
		var r rpc.ShadowRecord
		if err := json.Unmarshal(ev.PayloadJSON, &r); err != nil {
			return nil, fmt.Errorf("shadow record %s cannot be read: %w", ev.EventKey, err)
		}
		out = append(out, r)
	}
	return out, nil
}

// shadowHolding is the paper position: the sum of the record's fills.
func shadowHolding(records []rpc.ShadowRecord) int {
	held := 0
	for _, r := range records {
		if r.Kind == rpc.ShadowKindFill && r.Fill != nil {
			held += r.Fill.Quantity
		}
	}
	return held
}

// shadowRecorded reports whether a record of that kind for that session is
// in the record.
func shadowRecorded(records []rpc.ShadowRecord, kind, session string) bool {
	return slices.ContainsFunc(records, func(r rpc.ShadowRecord) bool { return r.Kind == kind && r.Session == session })
}

// appendShadowRecord appends one record once under its stable key; a record
// already there is left as it is.
func (s *Server) appendShadowRecord(ctx context.Context, r rpc.ShadowRecord) error {
	key := shadowKey(r.Strategy, r.Rule, r.Session, r.Kind)
	if _, ok, err := s.coreStore.GetEvent(ctx, shadowScope(r.Strategy), key); err != nil || ok {
		return err
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = s.coreStore.AppendEvents(ctx, []corestore.EventInput{{ScopeKey: shadowScope(r.Strategy), EventKey: key, Type: shadowRecordType, Action: r.Kind,
		Origin: coreEventOriginDaemon, OccurredAt: r.RecordedAt, PayloadJSON: raw}})
	return err
}

// recordCoreShadow writes the core's shadow records that are due while the
// core runs in shadow: the month's decision and intent once the month is
// frozen, a hold when the decision was measured but not frozen, and the
// intent's simulated fill once its session's close is in the core's series.
// res is the core's verdict of this refresh.
func (s *Server) recordCoreShadow(ctx context.Context, res *rpc.CoreTargetResult, now time.Time) {
	if s.coreStore == nil || res == nil || !res.Active {
		return
	}
	c := s.riskPolicies.snapshot().policy
	reg, ok := resolvedCore(s.resolvedRegister(c))
	if !ok || reg.State != register.StateShadow {
		return
	}
	records, err := s.shadowRecords(ctx, risk.CoreStrategyID)
	if err != nil {
		s.logShadowFailure(err)
		return
	}
	base := rpc.ShadowRecord{Version: shadowRecordVersion, Strategy: risk.CoreStrategyID, Rule: core.RuleID, SpecHash: core.SpecHash, SpecCited: c.Mandate.Core.SpecHash != "",
		Stage: string(reg.State), RecordedAt: now}
	if res.PolicyFingerprint != nil {
		base.PolicyFingerprint = res.PolicyFingerprint.Key
	}
	contract, _, _, _ := s.corePinState(c.Mandate.Core)
	var series *mandateHistorySeries
	if contract != nil {
		series = s.mandateHistorySeries(ctx, coreSeriesKey(contract.ConID))
	}

	records = s.recordCoreShadowDecision(ctx, res, base, records, contract, reg.ScalePct, now)
	// The fills due: an intent whose session's close is in.
	for _, intent := range slices.Clone(records) {
		if intent.Kind != rpc.ShadowKindIntent || intent.Intent == nil || intent.Intent.Quantity == 0 || shadowRecorded(records, rpc.ShadowKindFill, intent.Intent.ForSession) || series == nil {
			continue
		}
		fill, due := coreShadowFill(intent, series, shadowHolding(records))
		if !due {
			continue
		}
		r := base
		r.Kind, r.Session, r.Fill = rpc.ShadowKindFill, intent.Intent.ForSession, fill
		if err := s.appendShadowRecord(ctx, r); err != nil {
			s.logShadowFailure(err)
			return
		}
		records = append(records, r)
	}
}

// recordCoreShadowDecision writes the month's decision and intent once the
// month is frozen, or its hold when the decision was measured and not
// frozen, and returns the records with what it wrote.
func (s *Server) recordCoreShadowDecision(ctx context.Context, res *rpc.CoreTargetResult, base rpc.ShadowRecord, records []rpc.ShadowRecord, contract *coreContract, scalePct float64, now time.Time) []rpc.ShadowRecord {
	d := res.Decision
	if d == nil || d.Decision == "" || shadowRecorded(records, rpc.ShadowKindDecision, d.Decision) {
		return records
	}
	if d.FrozenAt.IsZero() || d.Drift || res.Sizing == nil || len(res.Reasons) > 0 {
		if !shadowRecorded(records, rpc.ShadowKindHold, d.Decision) {
			r := base
			r.Kind, r.Session, r.Status, r.Reasons = rpc.ShadowKindHold, d.Decision, rpc.CoreStatusHold, slices.Clone(res.Reasons)
			if len(r.Reasons) == 0 {
				r.Reasons = []string{"the month's decision is not frozen yet"}
			}
			if err := s.appendShadowRecord(ctx, r); err != nil {
				s.logShadowFailure(err)
				return records
			}
			records = append(records, r)
		}
		return records
	}
	if contract == nil {
		return records
	}
	held := shadowHolding(records)
	decision := base
	decision.Kind, decision.Session, decision.Status = rpc.ShadowKindDecision, d.Decision, rpc.CoreStatusReady
	decision.Decision = &rpc.ShadowDecision{TrendOn: d.TrendOn, Sigma: d.Sigma, Binding: d.Binding, TargetShares: res.Sizing.Target, ScalePct: scalePct, Digest: d.Digest}
	intent := base
	intent.Kind, intent.Session = rpc.ShadowKindIntent, d.Decision
	action := s.coreShadowDecide(res, contract, held, scalePct)
	// The session and the order terms come from the live row's builder, so
	// the shadow intent carries what its row would (core_rebalance.go).
	terms := coreRowTerms(contract, d.Decision, s.currentBrokerStateScope(), now)
	forSession := coreNextSession(d.Decision, contract.Market, now)
	if len(terms.Window) > 0 {
		forSession = terms.Window[0]
	}
	intent.Intent = &rpc.ShadowIntent{ForSession: forSession, Kind: action.Kind, Why: action.Why, Diff: action.Diff, BandShares: action.BandShares, HoldingBefore: held}
	switch action.Kind {
	case core.ActionBuy:
		intent.Intent.Quantity, intent.Intent.Terms = action.Quantity, &terms
	case core.ActionSell, core.ActionSellAll:
		intent.Intent.Quantity, intent.Intent.Terms = -action.Quantity, &terms
	}
	for _, r := range []rpc.ShadowRecord{decision, intent} {
		if err := s.appendShadowRecord(ctx, r); err != nil {
			s.logShadowFailure(err)
			return records
		}
		records = append(records, r)
	}
	return records
}

// coreShadowDecide is core.Decide on the shadow holding, with the buy cut to
// the headroom a live core would have: the book as held with the core's
// contracts holding the shadow position, and the core's allocation counted
// as if reserved at its written scale. The kept verdict is read, never
// changed; the hypothetical book is a copy that is never kept.
func (s *Server) coreShadowDecide(res *rpc.CoreTargetResult, k *coreContract, held int, scalePct float64) core.Decision {
	p := s.riskPolicies.snapshot().policy.Mandate.Core
	price := 0.0
	if res.Holding != nil {
		price = res.Holding.Price
	}
	in := core.DecideInput{Target: res.Sizing.Target, TrendOn: res.Decision.TrendOn, Held: held, Price: price, BandPct: policyCheckDeref(p.BandPct), MinOrderBase: policyCheckDeref(p.MinOrderBase), Holdable: held}
	_, kept, _, _ := s.mandateVerdicts.current()
	switch {
	case res.Mandate == nil || res.Mandate.Status != mandate.StatusWithin:
		status := "unknown"
		if res.Mandate != nil {
			status = res.Mandate.Status
		}
		in.BuyBlocked = "the mandate verdict reads " + status
	case kept == nil:
		in.BuyBlocked = "no mandate verdict is kept"
	default:
		if want := res.Sizing.Target - held; want > 0 && price > 0 {
			book := coreShadowBook(kept, k, s.mandateCoreContracts(context.Background(), s.riskPolicies.snapshot().policy), held, price, scalePct)
			in.Holdable = held + core.Headroom(want, func(q int) bool { return coreHeadroomFits(book, k, q, price) })
		}
	}
	return core.Decide(in)
}

// coreShadowBook is the book a live core would be judged on: a copy of the
// kept book with the core's contracts holding the shadow position instead of
// the account's units (every unit of the core's fund is the core's, so the
// real units would otherwise count twice), and the core's allocation counted
// as if reserved at scalePct.
func coreShadowBook(in *mandate.Input, k *coreContract, coreContracts []int, held int, price, scalePct float64) *mandate.Input {
	book := *in
	book.Legs = nil
	template := mandate.Leg{ID: mandateConLegID(k.ConID), Symbol: k.Symbol, Underlying: k.Symbol, Kind: mandate.KindStock, Multiplier: 1, Currency: k.Currency, Spot: price, ISIN: k.ISIN}
	ids := map[string]bool{}
	for _, id := range coreContracts {
		ids[mandateConLegID(id)] = true
	}
	for _, l := range in.Legs {
		if ids[l.ID] || l.ID == template.ID {
			if l.ID == template.ID {
				template.Symbol, template.Underlying = l.Symbol, l.Underlying
				if l.Spot > 0 {
					template.Spot = l.Spot
				}
			}
			continue
		}
		book.Legs = append(book.Legs, l)
	}
	if held > 0 {
		template.Quantity = float64(held)
		book.Legs = append(book.Legs, template)
	}
	if plan := in.Allocations; plan != nil {
		copied := &mandate.AllocationPlan{Placed: plan.Placed, Machine: slices.Clone(plan.Machine)}
		for i := range copied.Machine {
			if copied.Machine[i].Sleeve == mandate.SleeveCore {
				copied.Machine[i].StagePct = scalePct
			}
		}
		book.Allocations = copied
	}
	return &book
}

// coreShadowFill simulates an intent's fill by the lab's R2 convention: the
// whole quantity at its session's close. Due once the series reaches the
// session; a series past it without its bar records the fill unobserved.
func coreShadowFill(intent rpc.ShadowRecord, series *mandateHistorySeries, held int) (*rpc.ShadowFill, bool) {
	session := intent.Intent.ForSession
	if session == "" || len(series.Dates) == 0 || series.Dates[len(series.Dates)-1] < session {
		return nil, false
	}
	fill := &rpc.ShadowFill{Convention: rpc.ShadowFillConvention, HoldingAfter: held}
	i := slices.Index(series.Dates, session)
	if i < 0 {
		fill.Unobserved = "no close of " + session + " in the series; nothing is guessed"
		return fill, true
	}
	fill.Quantity, fill.Price = intent.Intent.Quantity, series.Closes[i]
	fill.HoldingAfter = held + fill.Quantity
	return fill, true
}

// coreNextSession is the first session after a date on the core's exchange
// calendar, or empty past its coverage.
func coreNextSession(date string, market marketcal.Market, now time.Time) string {
	day, err := time.Parse(time.DateOnly, date)
	if err != nil {
		return ""
	}
	cal := marketcal.NewWithClock(func() time.Time { return now })
	for range 14 {
		day = day.AddDate(0, 0, 1)
		res, err := cal.Query(marketcal.Query{Market: market, Date: day.Format(time.DateOnly), Days: 1})
		if err != nil {
			return ""
		}
		if res.Session.State == marketcal.StateRegular || res.Session.State == marketcal.StateEarlyClose {
			return day.Format(time.DateOnly)
		}
	}
	return ""
}

// coreShadowView is the core's shadow record as `canary core` serves it:
// the paper position and the newest records. Nil while the core is not in
// shadow and has no record.
func (s *Server) coreShadowView(ctx context.Context, state register.State) *rpc.ShadowView {
	if s.coreStore == nil {
		return nil
	}
	records, err := s.shadowRecords(ctx, risk.CoreStrategyID)
	if err != nil {
		return &rpc.ShadowView{Strategy: risk.CoreStrategyID, Stage: string(state), Unavailable: err.Error()}
	}
	if state != register.StateShadow && len(records) == 0 {
		return nil
	}
	view := &rpc.ShadowView{Strategy: risk.CoreStrategyID, Stage: string(state), Holding: shadowHolding(records), Records: len(records)}
	for i := len(records) - 1; i >= 0 && len(view.Recent) < shadowServeRecords; i-- {
		view.Recent = append(view.Recent, records[i])
	}
	return view
}

func (s *Server) logShadowFailure(err error) {
	if s.logger != nil {
		s.logger.Warnf("shadow record: %v; the next refresh tries again under the same keys", err)
	}
}

// coreShadowDue reports whether the core in shadow has a record to write
// that a frozen month would not prompt: the latest month's decision not yet
// recorded, or an intent whose fill is not in.
func (s *Server) coreShadowDue(ctx context.Context, latest string) bool {
	if s.coreStore == nil {
		return false
	}
	reg, ok := resolvedCore(s.resolvedRegister(s.riskPolicies.snapshot().policy))
	if !ok || reg.State != register.StateShadow {
		return false
	}
	records, err := s.shadowRecords(ctx, risk.CoreStrategyID)
	if err != nil {
		return false
	}
	if !shadowRecorded(records, rpc.ShadowKindDecision, latest) {
		return true
	}
	for _, r := range records {
		if r.Kind == rpc.ShadowKindIntent && r.Intent != nil && r.Intent.Quantity != 0 && !shadowRecorded(records, rpc.ShadowKindFill, r.Intent.ForSession) {
			return true
		}
	}
	return false
}
