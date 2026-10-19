package daemon

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/osauer/canary/v2/internal/daemon/corestore"
	"github.com/osauer/canary/v2/internal/mandate"
	"github.com/osauer/canary/v2/internal/register"
	"github.com/osauer/canary/v2/internal/risk"
	"github.com/osauer/canary/v2/internal/rpc"
)

// Placement for the register's other entries (internal-docs/design/
// mandate-risk-budget.md, "Placement", rules 1 to 5; built with the first
// order path that sends a non-core entry's rows, 2026-10-19). Each live
// entry trades one line, and its line is reserved to its sleeve as the
// core's is to the core (rule 1's reserved contract): every unit of it is
// the entry's whoever bought it, so no line is ever held by two sleeves, the
// placed units always sum to the broker's position (rule 4) and nothing is
// re-placed by hand (rule 5). One exception keeps rule 1's lineage: units of
// the line the account holds before the entry ever filled an order of its
// own are the owner's, so the line stays the owner's and the entry holds,
// naming them, until they are sold; a strategy never adopts a holding it
// did not open. The entry's own fills are read from Canary's journals: the
// proposal journal's submitted events of its rows (by id and line) and the
// order journal's executions of those orders.

// strategyFills is what an entry's own rows filled on its line: the shares
// bought less the shares sold, the cash those fills moved in the line's
// currency (spent negative), and how many executions.
type strategyFills struct {
	Units float64
	Cash  float64
	Execs int
}

// strategyOwnFills reads an entry's own fills on a line from the journals,
// in the selected broker scope. An unreadable journal reads as no fills, so
// a holding of the line holds the entry (fail closed).
func (s *Server) strategyOwnFills(ctx context.Context, id string, conID int) strategyFills {
	var out strategyFills
	if s == nil || s.tradeProposals == nil || s.tradeProposals.store == nil || s.orderJournal == nil || conID <= 0 {
		return out
	}
	scope := s.currentBrokerStateScope()
	var refs []string
	for _, ev := range s.tradeProposals.store.eventsSince(time.Time{}) {
		if ev.Type != "submitted" || ev.Bucket != rpc.TradeProposalBucketStrategyRebalance || ev.Strategy != id || ev.ConID != conID || ev.OrderRef == "" {
			continue
		}
		if !sameBrokerScope(scope, brokerStateScope{Account: ev.AccountID, Mode: ev.AccountMode}) || slices.Contains(refs, ev.OrderRef) {
			continue
		}
		refs = append(refs, ev.OrderRef)
	}
	if len(refs) == 0 {
		return out
	}
	store, err := s.orderJournal.coreStore()
	if err != nil {
		return out
	}
	seen := map[string]bool{}
	for _, ref := range refs {
		records, err := store.LoadOrderEvents(ctx, corestore.OrderQuery{OrderRef: ref, Limit: 10_000})
		if err != nil {
			return strategyFills{}
		}
		for _, record := range records {
			ev, err := decodeCoreOrderEvent(record)
			if err != nil || ev.Type != orderJournalEventStatusUpdated || strings.TrimSpace(ev.ExecID) == "" || !(ev.ExecShares > 0) || seen[ev.ExecID] {
				continue
			}
			price := ev.LastFillPrice
			if !(price > 0) {
				price = ev.AvgFillPrice
			}
			switch strings.ToUpper(strings.TrimSpace(nonEmptyString(ev.ExecSide, ev.Action))) {
			case "BOT", "BUY":
				out.Units += ev.ExecShares
				out.Cash -= ev.ExecShares * price
			case "SLD", "SELL":
				out.Units -= ev.ExecShares
				out.Cash += ev.ExecShares * price
			default:
				continue
			}
			seen[ev.ExecID] = true
			out.Execs++
		}
	}
	return out
}

// strategyLines are an entry's lines: the one its last resolution uses, the
// one it last used (kept in the database) and a written con_id, so a held
// entry or a resolution in progress never drops its reservation.
func (s *Server) strategyLines(ctx context.Context, r register.Resolved) []int {
	e := r.Entry
	isin, ccy := strings.TrimSpace(e.ISIN), normCcy(e.Currency)
	if isin == "" || ccy == "" {
		return nil
	}
	var out []int
	add := func(id int) {
		if id > 0 && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	inst := r.Instrument
	if inst.ISIN == "" {
		inst = register.Instrument{ISIN: isin, Currency: ccy, Exchange: e.Exchange}
		if e.ConID != nil {
			inst.ConID = *e.ConID
		}
	}
	if k, _, _, _ := s.instrumentPinState(inst); k != nil {
		add(k.ConID)
	}
	if used := s.coreUsed(ctx, isin, ccy); used != nil {
		add(used.ConID)
	}
	if e.ConID != nil {
		add(*e.ConID)
	}
	return out
}

// strategyReserves reports whether an entry's written stage reserves its
// line: live, or suspended (its units stay in its sleeve until sold,
// strategy-register.md section 7). An entry in shadow, the technical state
// of one never written live, leaves the owner's units the owner's.
func strategyReserves(e risk.StrategyEntry) bool {
	return e.Form != risk.StrategyFormCore && (e.Stage == risk.StageLive || e.Stage == risk.StageSuspended)
}

// strategyForeignUnits is the units of the entry's line the account holds
// that the entry did not open: the whole holding while the entry has no
// fill of its own on the line, else none (every unit of a reserved line is
// the entry's once it has traded it, as the core's).
func strategyForeignUnits(legs []mandate.Leg, conID int, own strategyFills) float64 {
	if own.Execs > 0 {
		return 0
	}
	held := 0.0
	id := mandateConLegID(conID)
	for _, l := range legs {
		if l.ID == id {
			held += l.Quantity
		}
	}
	return held
}

// mandateReservedContracts places every reserved line in its sleeve: the
// core's lines in the core's, and each other entry's lines in its own while
// it reserves them and the account holds no units of them it did not open.
// The core's lines win a collision. Keyed by contract id.
func (s *Server) mandateReservedContracts(ctx context.Context, c *risk.Constitution, resolved []register.Resolved, legs []mandate.Leg) map[int]string {
	out := map[int]string{}
	for _, id := range s.mandateCoreContracts(ctx, c) {
		out[id] = mandate.SleeveCore
	}
	for _, r := range resolved {
		if !strategyReserves(r.Entry) || r.Entry.Sleeve == "" {
			continue
		}
		for _, id := range s.strategyLines(ctx, r) {
			if _, taken := out[id]; taken {
				continue
			}
			if strategyForeignUnits(legs, id, s.strategyOwnFills(ctx, r.Entry.ID, id)) != 0 {
				continue // the owner's units: the entry holds (strategyForeignHold)
			}
			out[id] = r.Entry.Sleeve
		}
	}
	return out
}

// mandateReservedIDs is the reserved lines' contract ids, the core's and
// every other entry's: protection's automatic sales leave them out (owner
// decision D5 for the core, 2026-10-18 08:06 CEST; extended to every entry
// that reserves a line on the owner's instruction of 2026-10-19 07:07 CEST,
// "task build 2 and then 6 as well").
func (s *Server) mandateReservedIDs(ctx context.Context) []int {
	if s == nil || s.riskPolicies == nil {
		return nil
	}
	c := s.riskPolicies.snapshot().policy
	if c == nil || c.Mandate == nil {
		return nil
	}
	out := s.coreContractIDs(ctx)
	for _, r := range s.resolvedRegister(c) {
		if !strategyReserves(r.Entry) {
			continue
		}
		for _, id := range s.strategyLines(ctx, r) {
			if !slices.Contains(out, id) {
				out = append(out, id)
			}
		}
	}
	return out
}

// strategyForeignHold names why a live entry holds over units of its line
// it did not open, or "" when it holds none.
func strategyForeignHold(name, sym string, foreign float64) string {
	if foreign == 0 {
		return ""
	}
	return fmt.Sprintf("%g %s", math.Abs(foreign), pluralNoun(int(math.Round(math.Abs(foreign))), "unit")) + " of " + sym + " are held that " + name +
		" did not buy: they stay in your sleeve, and " + name + " trades only a line it holds alone, so it holds until you sell them"
}

// mandateReservedFromPlan is the reserved lines an allocation plan places,
// by contract id, so the gate places an order on the kept verdict's own
// placement.
func mandateReservedFromPlan(plan *mandate.AllocationPlan) map[int]string {
	out := map[int]string{}
	if plan == nil {
		return out
	}
	for legID, sleeve := range plan.Placed {
		raw, ok := strings.CutPrefix(legID, "con:")
		if !ok {
			continue
		}
		if id, err := strconv.Atoi(raw); err == nil && id > 0 {
			out[id] = sleeve
		}
	}
	return out
}

// strategyHealth is each register entry on `canary status`: its effective
// stage, and its row or why none stands, from the proposal engine's last
// served generation. Cheap by construction: the resolved register and the
// served snapshot, never an evaluation or a broker read.
func (s *Server) strategyHealth() []rpc.StrategyHealth {
	if s == nil || s.riskPolicies == nil {
		return nil
	}
	c := s.riskPolicies.snapshot().policy
	resolved := s.resolvedRegister(c)
	if len(resolved) == 0 {
		return nil
	}
	var snap rpc.TradeProposalSnapshot
	if s.tradeProposals != nil {
		snap = s.tradeProposals.snapshotForRead(false, false)
	}
	statusOf := func(r register.Resolved) *rpc.TradeProposalCoreStatus {
		if r.Entry.Form == risk.StrategyFormCore {
			return snap.CoreRebalance
		}
		for i := range snap.StrategyRebalance {
			if snap.StrategyRebalance[i].Strategy == r.Entry.ID {
				return &snap.StrategyRebalance[i]
			}
		}
		return nil
	}
	out := make([]rpc.StrategyHealth, 0, len(resolved))
	for _, r := range resolved {
		h := rpc.StrategyHealth{ID: r.Entry.ID, Sleeve: r.Entry.Sleeve, State: string(r.State)}
		st := statusOf(r)
		switch {
		case r.State == register.StateHold:
			h.Why = strings.Join(r.Reasons, "; ")
		case st == nil:
			h.Why = "no proposal generation has read it yet"
		case st.Served:
			for _, p := range snap.Proposals {
				if p.Key != st.RowKey {
					continue
				}
				h.Row = &rpc.StrategyHealthRow{Key: p.Key, Action: p.Action, Quantity: p.Quantity, Symbol: p.Symbol, State: p.State}
				if p.Core != nil {
					h.Row.Session = p.Core.Session
				}
				if p.Readiness != nil {
					h.Row.Readiness = p.Readiness.Code
				}
			}
			if h.Row == nil {
				h.Why = "its row was served and is no longer in the snapshot"
			}
		case len(st.Reasons) > 0:
			h.Why = st.Reasons[0]
		default:
			h.Why = nonEmptyString(st.Why, "no row is due")
		}
		out = append(out, h)
	}
	return out
}

// strategyLineSeries is the fresh decision-grade series a register entry
// keeps for one of its lines, read by contract id; nil when no entry trades
// the line or its series is missing, stale or short.
func (s *Server) strategyLineSeries(conID int, now time.Time) *mandateHistorySeries {
	if s == nil || s.riskPolicies == nil || conID <= 0 {
		return nil
	}
	ctx := context.Background()
	for _, r := range s.resolvedRegister(s.riskPolicies.snapshot().policy) {
		if r.Entry.Form == risk.StrategyFormCore || !slices.Contains(s.strategyLines(ctx, r), conID) {
			continue
		}
		series := s.mandateHistorySeries(ctx, strategySeriesKey(r.Entry.ID, conID))
		if series != nil && !mandateSeriesStale(series, now) && len(series.Closes) >= mandate.Closes &&
			(series.Contract.ConID == 0 || series.Contract.ConID == conID) {
			return series
		}
	}
	return nil
}
