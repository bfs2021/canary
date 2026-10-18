package daemon

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/osauer/canary/v2/internal/core"
	"github.com/osauer/canary/v2/internal/mandate"
	"github.com/osauer/canary/v2/internal/register"
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

// registerRules is the compiled rule catalogue as this daemon runs it
// (internal-docs/design/strategy-register.md). A test may let the core's rule
// send, as the build will once its monthly rows exist (core-sleeve.md phase 2).
func (s *Server) registerRules() []register.Rule {
	rules := register.Catalogue()
	if s != nil && s.coreSendsOrdersForTest {
		for i := range rules {
			if rules[i].ID == core.RuleID {
				rules[i].Sends = true
			}
		}
	}
	return rules
}

// resolvedRegister is the policy's strategy register against this build's
// rules: each entry's effective stage and the part of its risk allocation it
// reserves. The allocation plan, the core's buy and the shadow record read
// this one result.
func (s *Server) resolvedRegister(c *risk.Constitution) []register.Resolved {
	if c == nil || c.Mandate == nil {
		return nil
	}
	return register.Resolve(c.Mandate.Register(), s.registerRules())
}

// resolvedCore is the core's entry in a resolved register, or false while
// the constitution carries no [mandate.core].
func resolvedCore(resolved []register.Resolved) (register.Resolved, bool) {
	for _, r := range resolved {
		if r.Entry.Form == risk.StrategyFormCore {
			return r, true
		}
	}
	return register.Resolved{}, false
}

// mandateAllocationPlan is the allocation plan of the policy in force, from
// the resolved register (internal-docs/design/strategy-register.md, section
// 2): one machine sleeve per entry, its allocation the entry's and its stage
// the part it reserves, which is its scale while it is live and nothing in
// shadow, suspended or on hold. A live entry that holds keeps its reservation
// while its sleeve holds positions, so a new build or a key the owner is
// fixing never reshuffles the allocations. The core's sleeve and the overlay
// are always listed, as before the register; the core's contract is placed
// with the core. Every other leg is the owner's: no order path in this build
// places units in a machine sleeve by its fills, so placement is total and
// never unknown.
func mandateAllocationPlan(c *risk.Constitution, resolved []register.Resolved, coreContracts []int, legs []mandate.Leg) *mandate.AllocationPlan {
	if c == nil || c.Mandate == nil {
		return nil
	}
	m := c.Mandate
	plan := &mandate.AllocationPlan{Placed: map[string]string{}}
	if m.Core != nil {
		for _, id := range coreContracts {
			plan.Placed[mandateConLegID(id)] = mandate.SleeveCore
		}
	}
	holds := func(sleeve string) bool {
		for _, l := range legs {
			if l.Quantity != 0 && plan.SleeveOf(l.ID) == sleeve {
				return true
			}
		}
		return false
	}
	var coreTarget, overlayTarget float64
	if sl := m.Sleeves; sl != nil {
		coreTarget, overlayTarget = policyCheckDeref(sl.CoreVolPct), policyCheckDeref(sl.OverlayVolPct)
	}
	coreLine := mandate.MachineAllocation{Sleeve: mandate.SleeveCore, TargetPct: coreTarget, Note: "no [mandate.core]: the core is inactive"}
	overlayLine := mandate.MachineAllocation{Sleeve: mandate.SleeveOverlay, TargetPct: overlayTarget,
		Note: "no strategy registered in the overlay yet"}
	var others []mandate.MachineAllocation
	for _, r := range resolved {
		line := registerAllocation(r, holds(r.Entry.Sleeve))
		switch r.Entry.Sleeve {
		case mandate.SleeveCore:
			coreLine = line
		case mandate.SleeveOverlay:
			overlayLine = line
		default:
			others = append(others, line)
		}
	}
	plan.Machine = append([]mandate.MachineAllocation{coreLine, overlayLine}, others...)
	return plan
}

// mandateRegisterView is the resolved register as the mandate serves it.
func mandateRegisterView(resolved []register.Resolved) []rpc.MandateStrategyView {
	out := make([]rpc.MandateStrategyView, 0, len(resolved))
	for _, r := range resolved {
		v := rpc.MandateStrategyView{ID: r.Entry.ID, Sleeve: r.Entry.Sleeve, Rule: r.Entry.Rule, RuleCompiled: r.Rule != nil, SpecHash: r.Entry.SpecHash, SpecCited: r.Entry.SpecHash != "",
			AllocationPct: r.AllocationPct, AllocationKey: r.Entry.AllocationKey, Stage: r.Entry.Stage, State: string(r.State), ScalePct: r.ScalePct, ReservedPct: r.ReservedPct,
			Reasons: r.Reasons, Notes: r.Notes}
		if r.Rule != nil {
			v.Sends = r.Rule.Sends
			for _, n := range r.Rule.Data.Series {
				v.DeclaredSeries = append(v.DeclaredSeries, fmt.Sprintf("%s %s daily closes, %d sessions", nonEmptyString(n.Symbol, n.ISIN), n.Currency, n.Sessions))
			}
			for _, n := range r.Rule.Data.Chains {
				v.DeclaredChains = append(v.DeclaredChains, n.Underlying)
			}
		}
		out = append(out, v)
	}
	return out
}

// registerAllocation is one resolved entry's line in the allocation plan.
func registerAllocation(r register.Resolved, holdsPositions bool) mandate.MachineAllocation {
	name := "the " + r.Entry.ID + " strategy"
	if r.Entry.Form == risk.StrategyFormCore {
		name = "the core"
	}
	line := mandate.MachineAllocation{Sleeve: r.Entry.Sleeve, TargetPct: r.AllocationPct}
	notes := ""
	if len(r.Notes) > 0 {
		notes = " (" + strings.Join(r.Notes, "; ") + ")"
	}
	switch r.State {
	case register.StateLive:
		line.StagePct = r.ReservedPct
		line.Note = fmt.Sprintf("%s is live at scale %g of its %g allocation", name, r.ReservedPct, r.AllocationPct)
	case register.StateShadow:
		line.Note = name + " runs in shadow: it serves its target and sends no orders, so it reserves nothing" + notes
	case register.StateSuspended:
		line.Note = name + " is suspended, so it reserves nothing"
	default:
		why := strings.Join(r.Reasons, "; ")
		if scale := policyCheckDeref(r.Entry.ScalePct); r.Entry.Stage == risk.StageLive && holdsPositions && scale > 0 {
			line.StagePct = scale
			line.Note = fmt.Sprintf("%s holds (%s); live and holding positions, it keeps its scale %g of its %g allocation", name, why, scale, r.AllocationPct)
		} else {
			line.Note = name + " holds: " + why
		}
	}
	return line
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
