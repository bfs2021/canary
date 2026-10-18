package daemon

import (
	"fmt"
	"slices"
	"strings"

	"github.com/osauer/canary/v2/internal/mandate"
	"github.com/osauer/canary/v2/internal/risk"
	"github.com/osauer/canary/v2/internal/rpc"
)

// Seats (internal-docs/design/mandate-risk-budget.md, "Seats"; owner
// decision 2026-10-18 08:06 CEST: "give the machine its own seat (replacing
// D1 and largest-contributor-first)"). The machine's seat is only as big as
// what it actually runs: each machine sleeve's stage times its share. The
// owner has the rest. Each position keeps the seat of whoever opened it, and
// the core's pinned contract is reserved to the core. The additions gate
// refuses an order that takes its own sleeve over its seat, on top of every
// whole-book check; the forced reduction cuts the sleeve furthest over its
// seat first.

// coreSendsOrders is whether this build serves the core's orders. Phase 2
// (core-sleeve.md items 7 and 8) adds the core_rebalance rows; until then
// the core is read-only, runs nothing and holds no seat, whatever its
// scale_pct says.
const coreSendsOrders = false

// coreRuns reports whether the core sends orders in this daemon.
func (s *Server) coreRuns() bool {
	return coreSendsOrders || s != nil && s.coreSendsOrdersForTest
}

// mandateSeatPlan is the seat plan of the policy in force: the core's and
// the overlay's stage and share, and the core's contract placed with the
// core. coreRuns says whether the core sends orders. Every other leg is the
// owner's: no order path in this build places units in a machine sleeve by
// its fills, so placement is total and never unknown.
func mandateSeatPlan(c *risk.Constitution, coreRuns bool) *mandate.SeatPlan {
	if c == nil || c.Mandate == nil {
		return nil
	}
	m := c.Mandate
	var coreShare, overlayShare float64
	if sl := m.Sleeves; sl != nil {
		coreShare, overlayShare = policyCheckDeref(sl.CoreVolPct), policyCheckDeref(sl.OverlayVolPct)
	}
	coreSeat := mandate.MachineSeat{Sleeve: mandate.SleeveCore, SharePct: coreShare}
	switch {
	case m.Core == nil:
		coreSeat.Note = "no [mandate.core]: the core is inactive"
	case !coreRuns:
		coreSeat.Note = "the core sends no orders yet (its monthly rows are phase 2), so it runs nothing"
	case len(m.CoreUnapprovedKeys()) > 0:
		coreSeat.Note = "the core holds: " + strings.Join(m.CoreUnapprovedKeys(), ", ") + " unwritten"
	default:
		coreSeat.StagePct = policyCheckDeref(m.Core.ScalePct)
		coreSeat.Note = fmt.Sprintf("scale_pct %g of core_vol_pct %g", coreSeat.StagePct, coreShare)
	}
	plan := &mandate.SeatPlan{
		Machine: []mandate.MachineSeat{coreSeat, {Sleeve: mandate.SleeveOverlay, SharePct: overlayShare,
			Note: "no strategy runs in the overlay (s1 was dropped on 2026-10-18)"}},
		Placed: map[string]string{},
	}
	for _, id := range mandateCoreContracts(c) {
		plan.Placed[fmt.Sprintf("con:%d", id)] = mandate.SleeveCore
	}
	return plan
}

// mandateCoreContracts are the core's contracts, reserved to the core
// whoever buys them: the pinned [mandate.core] con_id. None while it is
// unwritten; the core then runs nothing, so it has no seat to keep, and a
// holding is adopted once the contract is known.
func mandateCoreContracts(c *risk.Constitution) []int {
	if c == nil || c.Mandate == nil || c.Mandate.Core == nil || c.Mandate.Core.ConID == nil || *c.Mandate.Core.ConID <= 0 {
		return nil
	}
	return []int{*c.Mandate.Core.ConID}
}

// mandateOrderSleeve places an order by its contract id, which the order
// path resolves before every gate: the core's contract is the core's
// whoever sends it, everything else the owner's (no machine order path in
// this build sends anything else).
func mandateOrderSleeve(c *risk.Constitution, draft rpc.OrderDraft) string {
	if draft.Contract.ConID > 0 && slices.Contains(mandateCoreContracts(c), draft.Contract.ConID) {
		return mandate.SleeveCore
	}
	return mandate.SleeveOwner
}

// mandateSleeveName is a sleeve as the refusals and rows name it.
func mandateSleeveName(sleeve string) string {
	switch sleeve {
	case mandate.SleeveOwner:
		return "your seat"
	case mandate.SleeveCore:
		return "the core's seat"
	case mandate.SleeveOverlay:
		return "the overlay's seat"
	}
	return "the " + sleeve + " seat"
}

// mandateSeatRefusal judges an order's sleeve: a sleeve over its seat adds
// nothing, as a book over budget adds nothing, and an addition must leave
// its sleeve inside its seat on the book with the order added. Nil when it
// fits, or when the evaluations carry no seats (no plan).
func mandateSeatRefusal(sleeve string, standing *mandate.Verdict, with mandate.Verdict, ccy, open string) *rpc.TradingBlocker {
	const action = "Size it smaller, or reduce in the same seat first; `canary mandate` has each seat."
	if standing != nil {
		if seat, ok := standing.Seat(sleeve); ok && seat.Over {
			return &rpc.TradingBlocker{Code: mandateBudgetCode, Action: action,
				Message: "Mandate budget: " + mandateSeatOverSentence(sleeve, seat, mandateMachineSeats(*standing), "", seat.UseBase, ccy) + "; this order adds risk." + open}
		}
	}
	seat, ok := with.Seat(sleeve)
	if !ok || !seat.Over {
		return nil
	}
	return &rpc.TradingBlocker{Code: mandateBudgetCode, Action: action,
		Message: "Mandate budget: with this order " + mandateSeatOverSentence(sleeve, seat, mandateMachineSeats(with), "would", seat.UseBase, ccy) + "." + open}
}

// mandateSeatOverSentence says a sleeve's use against its seat: "your seat
// would carry X a year, over its Y (the budget less the machine's seats of
// Z)"; without a verb, what it carries now.
func mandateSeatOverSentence(sleeve string, seat mandate.SeatReport, machine float64, verb string, use float64, ccy string) string {
	carry := "carries"
	if verb != "" {
		carry = verb + " carry"
	}
	s := fmt.Sprintf("%s %s %s a year, over its %s", mandateSleeveName(sleeve), carry, mandateMoney(use, ccy), mandateMoney(seat.SeatBase, ccy))
	switch {
	case sleeve == mandate.SleeveOwner:
		s += fmt.Sprintf(" (the budget less the machine's seats of %s)", mandateMoney(machine, ccy))
	case seat.SeatBase <= 0 && seat.Note != "":
		s += " (" + seat.Note + ")"
	}
	if sleeve == mandate.SleeveCore {
		s += "; every unit of the core's fund is the core's"
	}
	return s
}

// mandateMachineSeats is the machine's seats in sum on a verdict.
func mandateMachineSeats(v mandate.Verdict) float64 {
	sum := 0.0
	for _, s := range v.Seats {
		if s.Sleeve != mandate.SleeveOwner {
			sum += s.SeatBase
		}
	}
	return sum
}
