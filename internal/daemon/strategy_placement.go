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
// order path that sends a non-core entry's rows, 2026-10-19). Each entry
// trades one line, and its units of the line are what its own rows filled:
// the executions of the orders its submitted rows sent, read from the
// proposal journal (the rows' submitted events carry the entry's id and
// line) and the order journal. An opening fill of its own order places
// units in its sleeve (rule 1); a sale takes its own units (rule 2), and a
// sale the broker's position shows beyond the owner's units takes the
// entry's (the owner's hand sale takes the owner's first); every other unit
// of the line is the owner's, a hand buy included. The placed units always
// sum to the broker's position (rule 4): a line the entry holds alone is
// placed in its sleeve whole, a line both hold is split into the owner's leg
// and the entry's own (strategyLegID), and while one of the entry's orders
// on the line is in flight and the journal and the position disagree, the
// placement is unknown and no risk-adding order passes until it is known.
// Nothing is re-placed by hand (rule 5). An order is placed by its origin:
// the entry's own row (OrderDraft.RegisterEntry) in the entry's sleeve,
// anything else on the line in the owner's, so the gate and the verdict
// agree. The core's reserved contract is unchanged: every unit of it is the
// core's whoever bought it.

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

// strategyLineUnits splits a held line between an entry and the owner by
// lineage: the entry's units are its own fills' net, never below zero and
// never above what the broker holds (a hand sale beyond the owner's units
// took the entry's, rule 2); the rest is the owner's. A line held short or
// flat is the owner's: an entry never shorts.
func strategyLineUnits(held float64, own strategyFills) (entry, owner float64) {
	if !(held > 0) {
		return 0, held
	}
	entry = math.Min(math.Max(0, own.Units), held)
	return entry, held - entry
}

// strategyOrderInFlight reports whether one of an entry's orders on a line
// is in flight: a row of it submitted in the last strategyInFlightFor whose
// order the journal shows no terminal state for. While one is, a broker
// position that disagrees with the journal's fills is an execution not yet
// journaled, so the line's placement is unknown rather than guessed.
func (s *Server) strategyOrderInFlight(ctx context.Context, id string, conID int) bool {
	if s == nil || s.tradeProposals == nil || s.tradeProposals.store == nil || s.orderJournal == nil {
		return false
	}
	scope := s.currentBrokerStateScope()
	since := s.nowUTC().Add(-strategyInFlightFor)
	store, err := s.orderJournal.coreStore()
	if err != nil {
		return false
	}
	for _, ev := range s.tradeProposals.store.eventsSince(since) {
		if ev.Type != "submitted" || ev.Bucket != rpc.TradeProposalBucketStrategyRebalance || ev.Strategy != id || ev.ConID != conID || ev.OrderRef == "" ||
			!sameBrokerScope(scope, brokerStateScope{Account: ev.AccountID, Mode: ev.AccountMode}) {
			continue
		}
		records, err := store.LoadOrderEvents(ctx, corestore.OrderQuery{OrderRef: ev.OrderRef, Limit: 10_000})
		if err != nil {
			return true // unreadable: not known to be done
		}
		done := false
		for _, record := range records {
			if o, err := decodeCoreOrderEvent(record); err == nil && strategyOrderTerminal(o) {
				done = true
			}
		}
		if !done {
			return true
		}
	}
	return false
}

// strategyInFlightFor bounds how long a submitted row counts as in flight
// without a terminal state: a DAY order ends with its session, so a row
// older than this is no longer working whatever the journal says.
const strategyInFlightFor = 3 * 24 * time.Hour

func strategyOrderTerminal(ev orderJournalEvent) bool {
	if ev.SendState == orderSendStateTerminal || ev.Type == orderJournalEventReconciledAbsent || ev.Type == orderJournalEventSendError {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(ev.Status)) {
	case "filled", "cancelled", "apicancelled", "inactive":
		return true
	}
	return false
}

// strategyPlacement is the register's placement of the book's lines: the
// leg each machine sleeve's units are in, the book's legs with the lines
// both an entry and the owner hold split, and why any line's placement is
// unknown.
type strategyPlacement struct {
	Placed  map[string]string
	Legs    []mandate.Leg
	Unknown []string
}

// mandateStrategyPlacement places the book: the core's lines in the core's
// sleeve (every unit, whoever bought it), and each live or suspended entry's
// line by its own fills. The core's lines win a collision, which the policy
// refuses anyway (risk.validateRegister).
func (s *Server) mandateStrategyPlacement(ctx context.Context, c *risk.Constitution, resolved []register.Resolved, legs []mandate.Leg) strategyPlacement {
	out := strategyPlacement{Placed: map[string]string{}, Legs: legs}
	core := map[int]bool{}
	if c != nil && c.Mandate != nil && c.Mandate.Core != nil {
		for _, id := range s.mandateCoreContracts(ctx, c) {
			core[id] = true
			out.Placed[mandateConLegID(id)] = mandate.SleeveCore
		}
	}
	split := false
	for _, r := range resolved {
		if !strategyReserves(r.Entry) || r.Entry.Sleeve == "" {
			continue
		}
		for _, conID := range s.strategyLines(ctx, r) {
			if core[conID] {
				continue
			}
			id := mandateConLegID(conID)
			i := slices.IndexFunc(out.Legs, func(l mandate.Leg) bool { return l.ID == id })
			if i < 0 {
				continue // not held: nothing to place
			}
			own := s.strategyOwnFills(ctx, r.Entry.ID, conID)
			held := out.Legs[i].Quantity
			if held != math.Max(0, own.Units) && s.strategyOrderInFlight(ctx, r.Entry.ID, conID) {
				out.Unknown = append(out.Unknown, fmt.Sprintf("an order of %s on %s is in flight and the broker's position (%g) and its journaled fills (%g) differ; placement is known again once the order's fills are journaled",
					strategyName(r.Entry), nonEmptyString(out.Legs[i].Symbol, id), held, own.Units))
			}
			entry, owner := strategyLineUnits(held, own)
			switch {
			case entry <= 0:
				// The owner's line: the entry holds none of it.
			case owner == 0:
				out.Placed[id] = r.Entry.Sleeve
			default:
				if !split {
					out.Legs, split = slices.Clone(out.Legs), true
				}
				leg := out.Legs[i]
				out.Legs[i].Quantity = owner
				leg.ID, leg.Quantity = strategyLegID(r.Entry.Sleeve, conID), entry
				out.Legs = append(out.Legs, leg)
				out.Placed[leg.ID] = r.Entry.Sleeve
			}
		}
	}
	return out
}

// strategyUnitsInBook is an entry's units of a line in a placed book: the
// line's leg when the plan places it in the entry's sleeve, and its own leg
// of a split line.
func strategyUnitsInBook(in *mandate.Input, sleeve string, conID int) float64 {
	if in == nil {
		return 0
	}
	id, own := mandateConLegID(conID), strategyLegID(sleeve, conID)
	units := 0.0
	for _, l := range in.Legs {
		if l.ID == own || l.ID == id && in.Allocations.SleeveOf(id) == sleeve {
			units += l.Quantity
		}
	}
	return units
}

// strategyOwnerUnitsInBook is the owner's units of an entry's line in a
// placed book.
func strategyOwnerUnitsInBook(in *mandate.Input, conID int) float64 {
	if in == nil {
		return 0
	}
	id := mandateConLegID(conID)
	units := 0.0
	for _, l := range in.Legs {
		if l.ID == id && in.Allocations.SleeveOf(id) == mandate.SleeveOwner {
			units += l.Quantity
		}
	}
	return units
}

// mandateReservedIDs is the lines protection's automatic sales leave out:
// the core's (owner decision D5, 2026-10-18 08:06 CEST) and every line the
// kept verdict places whole in another entry's sleeve (extended under the
// owner's instruction of 2026-10-19 07:07 CEST). A line the owner holds
// units of, alone or beside an entry, keeps its automatic protection.
func (s *Server) mandateReservedIDs(ctx context.Context) []int {
	if s == nil {
		return nil
	}
	out := s.coreContractIDs(ctx)
	_, input, _, _ := s.mandateVerdicts.current()
	if input == nil || input.Allocations == nil {
		return out
	}
	for legID, sleeve := range input.Allocations.Placed {
		raw, ok := strings.CutPrefix(legID, "con:")
		if !ok || sleeve == mandate.SleeveOwner || sleeve == mandate.SleeveCore {
			continue
		}
		if id, err := strconv.Atoi(raw); err == nil && id > 0 && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

// strategyForeignHold names why a live entry holds while the owner holds
// units of its line, or "" when the owner holds none.
func strategyForeignHold(name, sym string, owner float64) string {
	if !(owner > 0) {
		return ""
	}
	return fmt.Sprintf("%g %s of %s are yours (bought by hand, not by %s): they stay in your sleeve, and %s trades only a line it holds alone, so it holds until you sell them",
		owner, pluralNoun(int(math.Round(owner)), "unit"), sym, name, name)
}

// mandateEntryByID is a resolved register entry other than the core by id.
func mandateEntryByID(resolved []register.Resolved, id string) (register.Resolved, bool) {
	for _, r := range resolved {
		if r.Entry.Form != risk.StrategyFormCore && r.Entry.ID == id {
			return r, true
		}
	}
	return register.Resolved{}, false
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

// mandateOrderPlacement places an order by its origin on a placed book: the
// sleeve its units open in and the leg they join. The core's contract is
// the core's whoever sends it; an entry's own row (entrySleeve set) is the
// entry's, on the line's leg when the plan places the line in its sleeve,
// else on its own leg beside the owner's; any other order is the owner's,
// on a leg of the owner's own when the line's placed units are a machine
// sleeve's. An empty leg means the order's own contract leg.
func mandateOrderPlacement(plan *mandate.AllocationPlan, draft rpc.OrderDraft, entrySleeve string) (sleeve, legID string) {
	if draft.Contract.ConID <= 0 {
		return mandate.SleeveOwner, ""
	}
	id := mandateConLegID(draft.Contract.ConID)
	placed := plan.SleeveOf(id)
	switch {
	case placed == mandate.SleeveCore:
		return mandate.SleeveCore, ""
	case entrySleeve != "" && placed == entrySleeve:
		return entrySleeve, ""
	case entrySleeve != "":
		return entrySleeve, strategyLegID(entrySleeve, draft.Contract.ConID)
	case placed != mandate.SleeveOwner:
		return mandate.SleeveOwner, id + ":owner"
	}
	return mandate.SleeveOwner, ""
}

// registerSleeveOf is the sleeve of the entry an order names as its own,
// "" for an order no entry sent.
func (s *Server) registerSleeveOf(c *risk.Constitution, entry string) string {
	if entry == "" {
		return ""
	}
	if r, ok := mandateEntryByID(s.resolvedRegister(c), entry); ok {
		return r.Entry.Sleeve
	}
	return ""
}

// mandateEntryOrderRefusal says why an order an entry names as its own is
// not one it may send, "" when it is: a stock order of one of the entry's
// lines while the entry is live.
func (s *Server) mandateEntryOrderRefusal(c *risk.Constitution, draft rpc.OrderDraft) string {
	r, ok := mandateEntryByID(s.resolvedRegister(c), draft.RegisterEntry)
	switch {
	case !ok:
		return "the order names strategy " + draft.RegisterEntry + ", which is not in the register"
	case r.State != register.StateLive:
		return strategyName(r.Entry) + " is " + string(r.State) + " and sends nothing"
	case !strings.EqualFold(strings.TrimSpace(draft.Contract.SecType), "STK"):
		return strategyName(r.Entry) + " trades only its own fund line; a " + nonEmptyString(draft.Contract.SecType, "non-stock") + " order is never its own"
	case draft.Contract.ConID <= 0 || !slices.Contains(s.strategyLines(context.Background(), r), draft.Contract.ConID):
		return strategyName(r.Entry) + " trades only its own fund line; this order is on another contract"
	}
	return ""
}

// mandateLegConID is the contract id a stock leg's units are of, whichever
// sleeve's leg they are: con:<id>, a split line's sleeve:<sleeve>:con:<id>,
// or an order's con:<id>:owner. Zero for any other id.
func mandateLegConID(legID string) int {
	if _, after, found := strings.CutLast(legID, "con:"); found {
		raw, _, _ := strings.Cut(after, ":")
		if id, err := strconv.Atoi(raw); err == nil && id > 0 {
			return id
		}
	}
	return 0
}
