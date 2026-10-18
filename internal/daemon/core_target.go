package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/osauer/canary/v2/internal/core"
	"github.com/osauer/canary/v2/internal/daemon/corestore"
	"github.com/osauer/canary/v2/internal/mandate"
	"github.com/osauer/canary/v2/internal/marketcal"
	"github.com/osauer/canary/v2/internal/risk"
	"github.com/osauer/canary/v2/internal/rpc"
)

// The core sleeve's target (internal-docs/design/core-sleeve.md), phase 1:
// read-only. The daemon keeps the core contract's own closes (the line IBKR
// lists under the policy's ISIN and currency, core_resolve.go, read by
// contract id on its primary exchange, judged fresh on that exchange's
// calendar, and never read by the book's verdict), decides each month at
// the exchange's last session's close with internal/core, freezes the month's target with the
// digest of its inputs, sizes it on the mandate's capital, cuts a buy to
// the mandate's headroom (the book at most at 0.95 of the budget, the
// core's gap at most at 0.95 of its cap, whole book: owner decision D1)
// and serves what the rule would do. It creates no proposal row and sends
// no order.

const (
	coreSeriesKeyPrefix = "core:con:"
	stateKindCoreTarget = "core_target"
	coreTargetDocVer    = 1
	// coreDecisionAfter is how long after a decision day's scheduled close
	// its close counts as in: the closing auction's end with margin (18:00
	// Berlin on a regular Xetra or London day).
	coreDecisionAfter = 30 * time.Minute
)

// coreSeriesKey keys the core's series by its contract id.
func coreSeriesKey(conID int) string { return fmt.Sprintf("%s%d", coreSeriesKeyPrefix, conID) }

// coreSeriesNeed is the core's series need: the contract IBKR lists under
// the ISIN, asked by its id with IBKR's symbol on its primary exchange,
// never SMART, and judged fresh on that exchange's calendar.
func coreSeriesNeed(k *coreContract) mandateHistoryNeed {
	return mandateHistoryNeed{Key: coreSeriesKey(k.ConID), Market: k.Market, Contract: rpc.ContractParams{ConID: k.ConID, Symbol: k.Symbol, SecType: "STK",
		Currency: k.Currency, Exchange: k.PrimaryExch, PrimaryExch: k.PrimaryExch}}
}

// coreFrozenTarget is one month's decision as first made: the signal, the
// target and the capital it was sized on. Persisted per account and
// contract; a later reading of the same window that differs holds.
type coreFrozenTarget struct {
	Version  int         `json:"version"`
	Scope    string      `json:"scope"`
	Decision string      `json:"decision"`
	Digest   string      `json:"digest"`
	FrozenAt time.Time   `json:"frozen_at"`
	Signal   core.Signal `json:"signal"`
	On       core.Sizing `json:"on"`
	Size     core.Size   `json:"size"`
}

type coreTargetStore struct {
	mu        sync.Mutex
	frozen    map[string]*coreFrozenTarget
	revisions map[string]int64
	loaded    map[string]bool
	// pin is the last resolution of the core's contract, and used the
	// contract the core last used per ISIN and currency (core_resolve.go).
	pin           corePin
	used          map[string]*coreContract
	usedRevisions map[string]int64
	usedLoaded    map[string]bool
}

func coreTargetScope(scope string) string {
	sum := sha256.Sum256([]byte(scope))
	return "core/target/" + hex.EncodeToString(sum[:])
}

func (s *Server) coreFrozen(ctx context.Context, scope string) *coreFrozenTarget {
	st := &s.coreTargets
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.frozen == nil {
		st.frozen, st.revisions, st.loaded = map[string]*coreFrozenTarget{}, map[string]int64{}, map[string]bool{}
	}
	if !st.loaded[scope] {
		st.loaded[scope] = true
		if s.coreStore != nil {
			doc, ok, err := s.coreStore.GetStateDocument(ctx, coreTargetScope(scope), stateKindCoreTarget)
			if err == nil && ok {
				var saved coreFrozenTarget
				if json.Unmarshal(doc.JSON, &saved) == nil && saved.Version == coreTargetDocVer && saved.Scope == scope {
					st.frozen[scope] = &saved
					st.revisions[scope] = doc.Revision
				}
			}
		}
	}
	return st.frozen[scope]
}

// freezeCore keeps a month's decision; a failed write keeps it in memory
// for this process and says so.
func (s *Server) freezeCore(ctx context.Context, f *coreFrozenTarget) error {
	st := &s.coreTargets
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.frozen == nil {
		st.frozen, st.revisions, st.loaded = map[string]*coreFrozenTarget{}, map[string]int64{}, map[string]bool{}
	}
	st.frozen[f.Scope], st.loaded[f.Scope] = f, true
	if s.coreStore == nil {
		return nil
	}
	raw, err := json.Marshal(f)
	if err != nil {
		return err
	}
	saved, err := s.coreStore.CompareAndSwapStateDocument(ctx, corestore.StateDocumentCAS{ScopeKey: coreTargetScope(f.Scope), Kind: stateKindCoreTarget, ExpectedRevision: st.revisions[f.Scope], JSON: raw})
	if err != nil {
		var conflict *corestore.RevisionConflictError
		if errors.As(err, &conflict) && conflict.Exists {
			st.revisions[f.Scope] = conflict.Actual
		}
		return err
	}
	st.revisions[f.Scope] = saved.Revision
	return nil
}

// coreDecisionDays returns the latest decided month-end (the last session
// of a month on the core's exchange calendar whose close is in: an earlier
// day, or the same day from coreDecisionAfter past its scheduled close) and
// the next one. Empty with a reason when the calendar does not cover them.
func coreDecisionDays(now time.Time, market marketcal.Market) (latest, next string, why string) {
	cal := marketcal.NewWithClock(func() time.Time { return now })
	probe, err := cal.Query(marketcal.Query{Market: market, At: now, Days: 1})
	if err != nil {
		return "", "", "no exchange calendar for " + string(market)
	}
	loc, err := time.LoadLocation(probe.Session.Timezone)
	if err != nil {
		return "", "", "the " + probe.Session.Timezone + " time zone is not available"
	}
	label := nonEmptyString(marketcal.Label(market), string(market))
	local := now.In(loc)
	type monthEndSession struct {
		date  string
		close time.Time
	}
	monthEnd := func(year int, month time.Month) (monthEndSession, bool) {
		first := time.Date(year, month, 1, 12, 0, 0, 0, loc)
		for day := first.AddDate(0, 1, -1); !day.Before(first); day = day.AddDate(0, 0, -1) {
			res, err := cal.Query(marketcal.Query{Market: market, Date: day.Format(time.DateOnly), Days: 1})
			if err != nil || res.Session.State == marketcal.StateUnknown {
				return monthEndSession{}, false
			}
			if res.Session.State == marketcal.StateRegular || res.Session.State == marketcal.StateEarlyClose {
				return monthEndSession{date: res.Session.Date, close: res.Session.Close}, true
			}
		}
		return monthEndSession{}, false
	}
	this, ok := monthEnd(local.Year(), local.Month())
	if !ok {
		return "", "", "the " + label + " calendar does not cover " + local.Format("January 2006")
	}
	if !now.Before(this.close.Add(coreDecisionAfter)) {
		nextMonth := time.Date(local.Year(), local.Month()+1, 1, 12, 0, 0, 0, loc)
		after, _ := monthEnd(nextMonth.Year(), nextMonth.Month())
		return this.date, after.date, ""
	}
	prevMonth := time.Date(local.Year(), local.Month()-1, 1, 12, 0, 0, 0, loc)
	before, ok := monthEnd(prevMonth.Year(), prevMonth.Month())
	if !ok {
		return "", this.date, "the " + label + " calendar does not cover " + prevMonth.Format("January 2006")
	}
	return before.date, this.date, ""
}

// coreCalendar is the session list Measure reads: the core exchange's
// official sessions where the embedded calendar covers them, the series'
// own dates before its coverage starts. It also names any bar dated on a
// day the exchange was closed.
func coreCalendar(dates []string, to string, now time.Time, market marketcal.Market) (sessions []string, offCalendar []string) {
	if len(dates) == 0 {
		return nil, nil
	}
	cal := marketcal.NewWithClock(func() time.Time { return now })
	have := make(map[string]bool, len(dates))
	for _, d := range dates {
		have[d] = true
	}
	start, err := time.Parse(time.DateOnly, dates[0])
	if err != nil {
		return nil, nil
	}
	end, err := time.Parse(time.DateOnly, to)
	if err != nil {
		return nil, nil
	}
	for day := start; !day.After(end); day = day.AddDate(0, 0, 1) {
		d := day.Format(time.DateOnly)
		res, err := cal.Query(marketcal.Query{Market: market, Date: d, Days: 1})
		state := marketcal.StateUnknown
		if err == nil {
			state = res.Session.State
		}
		switch state {
		case marketcal.StateRegular, marketcal.StateEarlyClose:
			sessions = append(sessions, d)
		case marketcal.StateUnknown:
			if have[d] {
				sessions = append(sessions, d)
			}
		default:
			if have[d] {
				offCalendar = append(offCalendar, d)
			}
		}
	}
	return sessions, offCalendar
}

// coreInactiveResult is the payload while the constitution carries no
// [mandate.core]: nothing is decided and nothing is read.
func coreInactiveResult(now time.Time, c *risk.Constitution) *rpc.CoreTargetResult {
	msg := "the risk constitution carries no [mandate.core] table; the core sleeve is inactive"
	switch {
	case c == nil:
		msg = "no risk constitution is in force; the core sleeve is inactive"
	case c.Mandate == nil:
		msg = "the risk constitution carries no [mandate] table; the core sleeve is inactive"
	}
	return &rpc.CoreTargetResult{AsOf: now, Active: false, Status: rpc.CoreStatusInactive, Message: msg, ReadOnly: true, Rule: core.RuleID, SpecID: core.SpecID, SpecHash: core.SpecHash}
}

// handleCoreTarget serves the core's target on the mandate verdict: the
// kept one while it is younger than mandateVerdictServeWithin, else a
// fresh evaluation after an account read, as mandate.risk serves it.
func (s *Server) handleCoreTarget(ctx context.Context, req *rpc.Request) (*rpc.CoreTargetResult, error) {
	var p rpc.CoreTargetParams
	if len(req.Params) > 0 && string(req.Params) != "null" {
		if err := decodeParams(req.Params, &p); err != nil {
			return nil, err
		}
	}
	now := s.nowUTC()
	c := s.riskPolicies.snapshot().policy
	if c == nil || c.Mandate == nil || c.Mandate.Core == nil {
		return coreInactiveResult(now, c), nil
	}
	if result, _, at, fp := s.mandateVerdicts.current(); p.Refresh || result == nil || fp != c.FingerprintKey() || now.Sub(at) >= mandateVerdictServeWithin {
		s.evaluateMandate(ctx, true)
	}
	return s.evaluateCoreTarget(ctx, now), nil
}

// refreshCoreTarget runs on the background cadence after the mandate
// verdict, so a month is frozen at its first evaluation after the decision
// close whether or not anyone reads it. It reads nothing from the broker.
func (s *Server) refreshCoreTarget(ctx context.Context) {
	if s.riskPolicies == nil {
		return
	}
	c := s.riskPolicies.snapshot().policy
	if c == nil || c.Mandate == nil || c.Mandate.Core == nil {
		return
	}
	contract, _, _, _ := s.corePinState(c.Mandate.Core)
	if contract == nil {
		return // nothing to decide on; a read names the hold
	}
	now := s.nowUTC()
	latest, _, why := coreDecisionDays(now, contract.Market)
	if why != "" {
		return
	}
	if f := s.coreFrozen(ctx, coreScope(s.currentBrokerStateScope(), contract.ConID)); f != nil && f.Decision == latest {
		return
	}
	s.evaluateCoreTarget(ctx, now)
}

// coreScope keys a frozen month by account, mode and the core's contract.
func coreScope(scope brokerStateScope, conID int) string {
	return fmt.Sprintf("%s|%s|%d", strings.TrimSpace(scope.Account), scope.Mode, conID)
}

// evaluateCoreTarget decides on the kept mandate verdict, the core's
// contract as IBKR lists it and its series. Anything the decision lacks
// holds, and every hold is named.
func (s *Server) evaluateCoreTarget(ctx context.Context, now time.Time) *rpc.CoreTargetResult {
	c := s.riskPolicies.snapshot().policy
	if c == nil || c.Mandate == nil || c.Mandate.Core == nil {
		return coreInactiveResult(now, c)
	}
	p := c.Mandate.Core
	isin, ccy, pinned := coreRequest(p)
	res := &rpc.CoreTargetResult{AsOf: now, Active: true, Status: rpc.CoreStatusHold, ReadOnly: true, Rule: core.RuleID, SpecID: core.SpecID, SpecHash: core.SpecHash, PolicyRule: p.Rule,
		PolicyFingerprint: &rpc.Fingerprint{Version: rpc.RiskConstitutionFingerprintVersion, Key: c.FingerprintKey()},
		Policy:            corePolicyView(c.Mandate),
		Instrument:        &rpc.CoreInstrument{Symbol: core.Symbol, ISIN: isin, ConID: pinned, Currency: ccy}}
	hold := func(reason string) { res.Reasons = append(res.Reasons, reason) }
	if keys := c.Mandate.CoreUnapprovedKeys(); len(keys) > 0 {
		hold("core keys are unwritten: " + strings.Join(keys, ", "))
	}
	if p.Rule != "" && p.Rule != core.RuleID {
		hold(fmt.Sprintf("the policy names rule %q; this daemon carries %s", p.Rule, core.RuleID))
	}
	if p.ISIN != "" && p.ISIN != core.ISIN {
		hold(fmt.Sprintf("the policy pins ISIN %s; %s is compiled for %s (%s)", p.ISIN, core.RuleID, core.ISIN, core.Symbol))
	}
	if p.Currency != "" && p.Currency != core.Currency {
		hold(fmt.Sprintf("the policy names currency %s; %s is %s", p.Currency, core.Symbol, core.Currency))
	}
	if base := normCcy(c.Capital.BaseCurrency); base != core.Currency {
		hold(fmt.Sprintf("the account base currency %s is not %s: the core is funded and measured in %s only", nonEmptyString(base, "(unwritten)"), core.Currency, core.Currency))
	}

	// The contract IBKR lists under the ISIN (core_resolve.go).
	contract, pinHold, lookupErr, asked := s.corePinState(p)
	switch {
	case contract != nil:
		res.Instrument = &rpc.CoreInstrument{Symbol: contract.Symbol, ISIN: contract.ISIN, ConID: contract.ConID, Exchange: contract.PrimaryExch, Currency: contract.Currency, Resolved: true,
			LocalSymbol: contract.LocalSymbol, TradingClass: contract.TradingClass, Calendar: string(contract.Market), CalendarLabel: marketcal.Label(contract.Market), ResolvedAt: contract.At}
	case pinHold != "":
		hold(pinHold)
	case lookupErr != "":
		hold(fmt.Sprintf("the core's contract is not resolved: IBKR gave no answer for %s in %s (%s); the desk asks again on the background lane", isin, ccy, lookupErr))
	case !asked:
		hold(fmt.Sprintf("the core's contract is not resolved yet: the desk asks IBKR for %s in %s on the background lane", isin, ccy))
	}
	var latest string
	sym, venue := res.Instrument.Symbol, ""
	if contract != nil {
		venue = nonEmptyString(marketcal.Label(contract.Market), contract.PrimaryExch)
		var next, why string
		latest, next, why = coreDecisionDays(now, contract.Market)
		res.NextDecision = next
		if why != "" {
			hold(why)
		}
	}

	// The core's own series.
	var series *mandateHistorySeries
	if contract != nil {
		need := coreSeriesNeed(contract)
		series = s.mandateHistorySeries(ctx, need.Key)
		ev := &rpc.CoreEvidence{SeriesKey: need.Key, Missing: series == nil, Calendar: string(contract.Market)}
		if series != nil {
			ev.Closes, ev.LastClose, ev.FetchedAt, ev.Source, ev.Stale = len(series.Closes), series.Dates[len(series.Dates)-1], series.FetchedAt, series.Source, mandateSeriesStale(series, now)
		}
		refused := ""
		if ev.ReadError, ev.ReadErrorAt = s.mandateSeriesFailure(need.Key); ev.ReadError != "" {
			refused = fmt.Sprintf("the last read of %s failed at %s UTC: %s", contract.label(), ev.ReadErrorAt.UTC().Format(time.DateTime), ev.ReadError)
		}
		res.Evidence = ev
		switch {
		case series == nil && refused != "":
			hold(fmt.Sprintf("no %s daily history: %s", sym, refused))
		case series == nil:
			hold(fmt.Sprintf("no %s daily history yet; the desk reads it on %s on the background lane", sym, contract.PrimaryExch))
		case series.Market != contract.Market:
			hold(fmt.Sprintf("the kept %s history was read for the %s calendar, not %s; the desk reads it again on %s", sym, nonEmptyString(string(series.Market), "Xetra"), venue, contract.PrimaryExch))
			series = nil
		case series.Contract.ConID > 0 && series.Contract.ConID != contract.ConID:
			hold(fmt.Sprintf("the series' contract %d is not the core's contract %d", series.Contract.ConID, contract.ConID))
		case ev.Stale && refused != "":
			hold(fmt.Sprintf("the %s daily history is stale: last close %s on the %s calendar; %s", sym, ev.LastClose, venue, refused))
		case ev.Stale:
			hold(fmt.Sprintf("the %s daily history is stale: last close %s on the %s calendar", sym, ev.LastClose, venue))
		}
	}

	// The month's decision.
	var sig core.Signal
	var window core.Window
	if series != nil && latest != "" {
		sessions, off := coreCalendar(series.Dates, latest, now, contract.Market)
		for _, d := range off {
			hold(fmt.Sprintf("the %s history carries a bar on %s, when %s was closed", sym, d, venue))
		}
		if series.Dates[len(series.Dates)-1] < latest {
			hold(fmt.Sprintf("the decision waits for %s's close of %s", sym, latest))
		} else {
			sig, window = core.Measure(sessions, series.Dates, series.Closes, latest)
			if sig.Status != core.StatusOK {
				hold(fmt.Sprintf("no decision for %s: %s", latest, sig.Reason))
			} else {
				index := core.Series{}
				if spy := s.mandateHistorySeries(ctx, "u:"+mandate.IndexSymbol); spy != nil {
					index = core.Series{Dates: spy.Dates, Closes: spy.Closes}
				}
				for _, g := range core.Guard(sig, window, index) {
					hold(g)
				}
			}
			res.Decision = &rpc.CoreDecision{Signal: sig}
		}
	}

	// The mandate: capital for the size, the book for the holding and the
	// headroom.
	result, input, at, fp := s.mandateVerdicts.current()
	var verdict *mandate.Verdict
	switch {
	case result == nil || result.Verdict == nil || input == nil || fp != c.FingerprintKey():
		hold("no mandate verdict has been measured for the policy in force")
	default:
		verdict = result.Verdict
		res.Mandate = &rpc.CoreMandateView{Status: verdict.Status, EvaluatedAt: at, BudgetAnnualSD: verdict.Capital.BudgetAnnualSD}
		if verdict.Risk != nil {
			res.Mandate.RiskAnnualSD = verdict.Risk.SDAnnualBase
		}
		if !verdict.Capital.Known {
			hold("the mandate's capital is unknown: " + strings.Join(verdict.Capital.Reasons, "; "))
		}
		if verdict.Status == mandate.StatusUnknown {
			why := "no reason given"
			if len(verdict.Reasons) > 0 {
				why = verdict.Reasons[0]
			}
			hold("the mandate verdict is unknown, so the holding cannot be confirmed: " + why)
		}
		if age := now.Sub(at); age > mandateVerdictMaxAge {
			hold(fmt.Sprintf("the mandate verdict is %s old", formatQuoteAge(age)))
		}
	}

	// The holding of the core's contract, and any other line of the fund:
	// another contract IBKR lists under the ISIN, or the fund's symbol in
	// its currency.
	held, price, priceSource := 0, 0.0, ""
	if input != nil && contract != nil {
		id := mandateConLegID(contract.ConID)
		qty := 0.0
		for _, l := range input.Legs {
			sameFund := input.LineISINs[l.ID] == contract.ISIN || (l.Underlying == core.Symbol || l.Underlying == contract.Symbol) && normCcy(l.Currency) == contract.Currency
			switch {
			case l.ID == id:
				qty += l.Quantity
				if l.Spot > 0 {
					price, priceSource = l.Spot, "mark"
				}
			case sameFund:
				hold(fmt.Sprintf("a second line of the fund (%s) is held under %s; the core counts only %s", l.Symbol, l.ID, contract.label()))
			}
		}
		if qty < 0 {
			hold(fmt.Sprintf("the book is short %s; the core never shorts", sym))
		}
		held = int(math.Max(0, math.Floor(qty)))
	}
	if price <= 0 && series != nil {
		price, priceSource = series.Closes[len(series.Closes)-1], "close"
	}
	res.Holding = &rpc.CoreHolding{Shares: held, Price: price, ValueBase: float64(held) * price, PriceSource: priceSource}

	// The size: frozen at the decision, lowered to today's capital.
	if sig.Status == core.StatusOK && verdict != nil && verdict.Capital.Known && len(c.Mandate.CoreUnapprovedKeys()) == 0 && contract != nil {
		on := core.Sizing{CoreVolPct: *c.Mandate.Sleeves.CoreVolPct, ScalePct: *p.ScalePct, MaxValuePct: *p.MaxValuePct, BudgetBase: verdict.Capital.BudgetBase, Room: verdict.Capital.Room}
		current := core.SizeTarget(sig, on)
		scope := coreScope(s.currentBrokerStateScope(), contract.ConID)
		frozen := s.coreFrozen(ctx, scope)
		switch {
		case frozen != nil && frozen.Decision == sig.Decision && frozen.Digest != sig.Digest:
			hold(fmt.Sprintf("core_target_drift: the closes behind %s's decision read differently than when it was frozen at %s; the month holds for review", sig.Decision, frozen.FrozenAt.UTC().Format(time.DateTime)))
			res.Decision = &rpc.CoreDecision{Signal: frozen.Signal, FrozenAt: frozen.FrozenAt, Drift: true}
		case frozen != nil && frozen.Decision == sig.Decision:
			res.Decision.FrozenAt = frozen.FrozenAt
		case len(res.Reasons) == 0:
			frozen = &coreFrozenTarget{Version: coreTargetDocVer, Scope: scope, Decision: sig.Decision, Digest: sig.Digest, FrozenAt: now, Signal: sig, On: on, Size: current}
			if err := s.freezeCore(ctx, frozen); err != nil && s.logger != nil {
				s.logger.Warnf("core target %s: frozen in memory, the database save failed: %v", sig.Decision, err)
			}
			res.Decision.FrozenAt = now
		default:
			frozen = nil // a month is frozen only on a clean reading
		}
		if frozen != nil && frozen.Decision == sig.Decision {
			target := min(frozen.Size.TargetShares, current.TargetShares)
			res.Sizing = &rpc.CoreSizingView{Frozen: frozen.Size, FrozenOn: frozen.On, Current: current, CurrentOn: on, Target: target, TargetBase: float64(target) * price}
		}
	}

	if len(res.Reasons) > 0 {
		res.Action = &core.Decision{Kind: core.ActionHold, Why: "the core holds: buys stop, and a sale the rule wants goes to you for review"}
		return res
	}
	if res.Sizing == nil {
		res.Action = &core.Decision{Kind: core.ActionHold, Why: "no target is sized"}
		return res
	}

	// The action, with a buy cut to the mandate's headroom.
	in := core.DecideInput{Target: res.Sizing.Target, TrendOn: sig.TrendOn, Held: held, Price: price, BandPct: *p.BandPct, MinOrderBase: *p.MinOrderBase, Holdable: held}
	if verdict.Status != mandate.StatusWithin {
		in.BuyBlocked = "the mandate verdict reads " + verdict.Status
	} else if !s.coreRuns() {
		// Allocations (2026-10-18): the core's allocation is its stage times its share
		// while it sends orders, and nothing until then.
		in.BuyBlocked = "the core sends no orders yet, so it has no allocation"
	} else if want := res.Sizing.Target - held; want > 0 && price > 0 {
		add := core.Headroom(want, func(q int) bool { return coreHeadroomFits(input, contract, q, price) })
		in.Holdable = held + add
		res.Sizing.Holdable = &in.Holdable
	}
	d := core.Decide(in)
	res.Action = &d
	res.Status = rpc.CoreStatusReady
	return res
}

// coreHeadroomFits reports whether the book with q more shares of the core
// stays at most at core.HeadroomFactor of the budget, carries no gap-cap
// breach, keeps the core's own gap at most at that share of its cap, and
// keeps the core inside its allocation: the forced reduction's target with
// margin, so a core buy cannot set up a forced sale by itself (design §3;
// review B1), and the gate's allocation check (allocations, 2026-10-18).
func coreHeadroomFits(book *mandate.Input, k *coreContract, q int, price float64) bool {
	if book == nil || k == nil {
		return false
	}
	if q <= 0 {
		return true
	}
	leg := mandate.Leg{ID: mandateConLegID(k.ConID), Symbol: k.Symbol, Underlying: k.Symbol, Kind: mandate.KindStock, Quantity: float64(q), Multiplier: 1, Currency: k.Currency, Spot: price, ISIN: k.ISIN}
	for _, l := range book.Legs {
		if l.ID == leg.ID {
			leg.Symbol, leg.Underlying = l.Symbol, l.Underlying // the held line's names, so the merged leg is one underlying
			if l.Spot > 0 {
				leg.Spot = l.Spot
			}
		}
	}
	with := mandate.Evaluate(mandate.WithLeg(*book, leg))
	if with.Status == mandate.StatusUnknown || with.Risk == nil || with.Stresses == nil {
		return false
	}
	if with.Risk.SDAnnualBase > core.HeadroomFactor*with.Capital.BudgetAnnualSD || len(with.Stresses.GapBreaches) > 0 {
		return false
	}
	for _, u := range with.Underlyings {
		if u.Underlying == leg.Underlying && u.GapLossBase > core.HeadroomFactor*u.GapCapBase {
			return false
		}
	}
	if allocation, ok := with.Allocation(mandate.SleeveCore); ok && allocation.Over {
		return false
	}
	return true
}

func corePolicyView(m *risk.ConstitutionMandate) *rpc.CorePolicyView {
	if m == nil || m.Core == nil {
		return nil
	}
	p := m.Core
	v := &rpc.CorePolicyView{Rule: p.Rule, ISIN: p.ISIN, ConID: p.ConID, Exchange: p.Exchange, Currency: p.Currency, ScalePct: p.ScalePct, BandPct: p.BandPct,
		MinOrderBase: p.MinOrderBase, MaxValuePct: p.MaxValuePct, Unapproved: m.CoreUnapprovedKeys()}
	if m.Sleeves != nil {
		v.CoreVolPct = m.Sleeves.CoreVolPct
	}
	return v
}
