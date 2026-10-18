package daemon

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/osauer/canary/v2/internal/mandate"
	"github.com/osauer/canary/v2/internal/risk"
	"github.com/osauer/canary/v2/internal/rpc"
)

// Allocations (internal-docs/design/mandate-risk-budget.md, "Risk allocations";
// owner decision 2026-10-18 08:06 CEST: "give the machine its own seat
// (replacing D1 and largest-contributor-first)"). The machine's allocation is
// only as big as what it actually runs: each machine sleeve's stage times its
// allocation. The owner has the rest. Each position stays in the sleeve of
// whoever opened it, and the core's pinned contract is reserved to the core. The
// additions gate refuses an order that takes its own sleeve over its allocation,
// on top of every whole-book check; the forced reduction cuts the sleeve
// furthest over its allocation first.

// coreSendsOrders is whether this build serves the core's orders. Phase 2
// (core-sleeve.md items 7 and 8) adds the core_rebalance rows; until then
// the core is read-only, runs nothing and holds no allocation, whatever its
// scale_pct says.
const coreSendsOrders = false

// coreRuns reports whether the core sends orders in this daemon.
func (s *Server) coreRuns() bool {
	return coreSendsOrders || s != nil && s.coreSendsOrdersForTest
}

// mandateAllocationPlan is the allocation plan of the policy in force: the
// core's and the overlay's stage and allocation, and the core's contract placed
// with the core. coreRuns says whether the core sends orders. Every other leg is
// the owner's: no order path in this build places units in a machine sleeve by
// its fills, so placement is total and never unknown.
func mandateAllocationPlan(c *risk.Constitution, coreRuns bool, coreContracts []int) *mandate.AllocationPlan {
	if c == nil || c.Mandate == nil {
		return nil
	}
	m := c.Mandate
	var coreTarget, overlayTarget float64
	if sl := m.Sleeves; sl != nil {
		coreTarget, overlayTarget = policyCheckDeref(sl.CoreVolPct), policyCheckDeref(sl.OverlayVolPct)
	}
	coreAllocation := mandate.MachineAllocation{Sleeve: mandate.SleeveCore, TargetPct: coreTarget}
	switch {
	case m.Core == nil:
		coreAllocation.Note = "no [mandate.core]: the core is inactive"
	case !coreRuns:
		coreAllocation.Note = "the core sends no orders yet (its monthly rows are phase 2), so it runs nothing"
	case len(m.CoreUnapprovedKeys()) > 0:
		coreAllocation.Note = "the core holds: " + strings.Join(m.CoreUnapprovedKeys(), ", ") + " unwritten"
	default:
		coreAllocation.StagePct = policyCheckDeref(m.Core.ScalePct)
		coreAllocation.Note = fmt.Sprintf("scale_pct %g of core_vol_pct %g", coreAllocation.StagePct, coreTarget)
	}
	plan := &mandate.AllocationPlan{
		Machine: []mandate.MachineAllocation{coreAllocation, {Sleeve: mandate.SleeveOverlay, TargetPct: overlayTarget,
			Note: "no strategy runs in the overlay yet"}},
		Placed: map[string]string{},
	}
	if m.Core != nil {
		for _, id := range coreContracts {
			plan.Placed[mandateConLegID(id)] = mandate.SleeveCore
		}
	}
	return plan
}

// mandateCoreContracts are the core's contracts, reserved to the core
// whoever buys them: the line IBKR lists under the core's ISIN and the one
// the core last used (core_resolve.go, since the policy names only the
// fund and its currency, owner decision 2026-10-18 08:06 CEST), and a
// written [mandate.core] con_id. None before the first resolution with no
// con_id written; the core then runs nothing, so it has no allocation to keep,
// and a holding is adopted once the contract is known.
func (s *Server) mandateCoreContracts(ctx context.Context, c *risk.Constitution) []int {
	if c == nil || c.Mandate == nil || c.Mandate.Core == nil {
		return nil
	}
	out := s.coreContractIDs(ctx)
	if p := c.Mandate.Core.ConID; p != nil && *p > 0 && !slices.Contains(out, *p) {
		out = append(out, *p)
	}
	return out
}

// mandateOrderSleeve places an order by its contract id, which the order
// path resolves before every gate: the core's contract is the core's
// whoever sends it, everything else the owner's (no machine order path in
// this build sends anything else).
func mandateOrderSleeve(coreContracts []int, draft rpc.OrderDraft) string {
	if draft.Contract.ConID > 0 && slices.Contains(coreContracts, draft.Contract.ConID) {
		return mandate.SleeveCore
	}
	return mandate.SleeveOwner
}

// mandateSleeveName is a sleeve as the refusals and rows name it.
func mandateSleeveName(sleeve string) string {
	switch sleeve {
	case mandate.SleeveOwner:
		return "your sleeve"
	case mandate.SleeveCore:
		return "the core's sleeve"
	case mandate.SleeveOverlay:
		return "the overlay's sleeve"
	}
	return "the " + sleeve + " sleeve"
}

// mandateAllocationRefusal judges an order's sleeve: a sleeve over its
// allocation adds nothing, as a book over budget adds nothing, and an addition
// must leave its sleeve inside its allocation on the book with the order added.
// Nil when it fits, or when the evaluations carry no allocations (no plan).
func mandateAllocationRefusal(sleeve string, standing *mandate.Verdict, with mandate.Verdict, ccy, open string) *rpc.TradingBlocker {
	const action = "Size it smaller, or reduce in the same sleeve first; `canary mandate` has each sleeve's allocation."
	if standing != nil {
		if allocation, ok := standing.Allocation(sleeve); ok && allocation.Over {
			return &rpc.TradingBlocker{Code: mandateBudgetCode, Action: action,
				Message: "Mandate budget: " + mandateAllocationOverSentence(sleeve, allocation, mandateMachineAllocations(*standing), "", allocation.UseBase, ccy) + "; this order adds risk." + open}
		}
	}
	allocation, ok := with.Allocation(sleeve)
	if !ok || !allocation.Over {
		return nil
	}
	return &rpc.TradingBlocker{Code: mandateBudgetCode, Action: action,
		Message: "Mandate budget: with this order " + mandateAllocationOverSentence(sleeve, allocation, mandateMachineAllocations(with), "would", allocation.UseBase, ccy) + "." + open}
}

// mandateAllocationOverSentence says a sleeve's use against its allocation:
// "your sleeve would carry X a year, over its allocation Y (the budget less
// the machine's allocations of Z)"; without a verb, what it carries now.
func mandateAllocationOverSentence(sleeve string, allocation mandate.AllocationReport, machine float64, verb string, use float64, ccy string) string {
	carry := "carries"
	if verb != "" {
		carry = verb + " carry"
	}
	s := fmt.Sprintf("%s %s %s a year, over its allocation %s", mandateSleeveName(sleeve), carry, mandateMoney(use, ccy), mandateMoney(allocation.AllocationBase, ccy))
	switch {
	case sleeve == mandate.SleeveOwner:
		s += fmt.Sprintf(" (the budget less the machine's allocations of %s)", mandateMoney(machine, ccy))
	case allocation.AllocationBase <= 0 && allocation.Note != "":
		s += " (" + allocation.Note + ")"
	}
	if sleeve == mandate.SleeveCore {
		s += "; every unit of the core's fund is the core's"
	}
	return s
}

// mandateMachineAllocations is the machine's allocations in sum on a verdict.
func mandateMachineAllocations(v mandate.Verdict) float64 {
	sum := 0.0
	for _, s := range v.Allocations {
		if s.Sleeve != mandate.SleeveOwner {
			sum += s.AllocationBase
		}
	}
	return sum
}
