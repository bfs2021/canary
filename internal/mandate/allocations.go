package mandate

import (
	"cmp"
	"maps"
	"math"
	"slices"
)

// Allocations (internal-docs/design/mandate-risk-budget.md, "Risk allocations";
// owner decision 2026-10-18 08:06 CEST: "give the machine its own seat
// (replacing D1 and largest-contributor-first)"). Each sleeve has an allocation
// inside the whole-book budget: a machine sleeve's allocation is reserved only
// as far as it runs: its stage times its allocation, the owner's is the rest. A
// sleeve's use is the sum of its legs' Euler contributions, so the uses add up
// to the book's risk as the allocations add up to the budget.

// Sleeve names.
const (
	SleeveOwner   = "owner"
	SleeveCore    = "core"
	SleeveOverlay = "overlay"
)

// Sleeves is the order allocations are listed in and ties are broken in.
var Sleeves = []string{SleeveOwner, SleeveCore, SleeveOverlay}

// allocationEpsilon absorbs float noise when a use is compared with its
// allocation.
const allocationEpsilon = 1e-6

// MachineAllocation is one machine sleeve's allocation as the policy and the
// daemon's build give it.
type MachineAllocation struct {
	Sleeve string `json:"sleeve"`
	// SharePct is the sleeve's share in percent a year of the budget base at
	// full room ([mandate.sleeves]).
	SharePct float64 `json:"share_pct"`
	// StagePct is how much of the share the sleeve runs: the core's
	// scale_pct, a lab strategy's stage; 0 while it sends no orders.
	StagePct float64 `json:"stage_pct"`
	// Note says why the stage is what it is.
	Note string `json:"note,omitempty"`
}

// AllocationPlan is what the allocations are measured on: the machine's
// allocations and the machine sleeve each placed leg belongs to. A leg not in
// Placed is the owner's.
type AllocationPlan struct {
	Machine []MachineAllocation
	Placed  map[string]string
}

// SleeveOf names the sleeve a leg is placed in; every leg is the owner's
// while the plan places none.
func (p *AllocationPlan) SleeveOf(legID string) string {
	if p != nil {
		if s, ok := p.Placed[legID]; ok && s != "" {
			return s
		}
	}
	return SleeveOwner
}

// WithPlaced returns a copy of the plan with one more leg placed: an order's
// leg, judged in the sleeve it would open units in.
func (p *AllocationPlan) WithPlaced(legID, sleeve string) *AllocationPlan {
	if p == nil {
		return nil
	}
	out := &AllocationPlan{Machine: p.Machine, Placed: make(map[string]string, len(p.Placed)+1)}
	maps.Copy(out.Placed, p.Placed)
	if sleeve != "" && sleeve != SleeveOwner {
		out.Placed[legID] = sleeve
	}
	return out
}

// AllocationReport is one sleeve's allocation and its use on a measured book.
type AllocationReport struct {
	Sleeve string `json:"sleeve"`
	// SharePct and StagePct are a machine sleeve's share and stage; the
	// owner's allocation is the rest and has neither.
	SharePct float64 `json:"share_pct"`
	StagePct float64 `json:"stage_pct"`
	// AllocationPct is the allocation in percent a year of the budget base at
	// full room: a machine sleeve's share × stage, the owner's the target less
	// the machine's, never below zero.
	AllocationPct float64 `json:"allocation_pct"`
	// AllocationBase is the allocation in base currency a year: AllocationPct of
	// the budget base × room, the budget's own scaling.
	AllocationBase float64 `json:"allocation_base"`
	// UseBase is the sum of the sleeve's legs' Euler contributions in base
	// currency a year; ContributionPct is the same as a share of the book's
	// risk. The uses of all sleeves add up to the book's risk.
	UseBase         float64 `json:"use_base"`
	ContributionPct float64 `json:"contribution_pct"`
	// StandaloneBase is the sleeve's risk measured alone, annualised: an
	// upper bound on its use, which is the standalone risk times its
	// correlation with the book.
	StandaloneBase float64 `json:"standalone_base"`
	// ExcessBase is use less allocation: positive over the allocation, negative
	// the room left. Over is true when the use exceeds the allocation.
	ExcessBase float64 `json:"excess_base"`
	Over       bool    `json:"over,omitempty"`
	// Legs counts the legs placed in the sleeve.
	Legs int    `json:"legs"`
	Note string `json:"note,omitempty"`
}

// Allocation returns one sleeve's report on a verdict.
func (v Verdict) Allocation(sleeve string) (AllocationReport, bool) {
	for _, s := range v.Allocations {
		if s.Sleeve == sleeve {
			return s, true
		}
	}
	return AllocationReport{}, false
}

// allocationReports measures every sleeve on one simulation: legs, their scenario
// P&L and Euler shares are index-aligned.
func allocationReports(plan *AllocationPlan, p Policy, c Capital, legs []Leg, legPnL [][]float64, contributionPct []float64, risk *Risk) []AllocationReport {
	byName := map[string]*AllocationReport{}
	var names []string
	add := func(name string) *AllocationReport {
		if r, ok := byName[name]; ok {
			return r
		}
		r := &AllocationReport{Sleeve: name}
		byName[name] = r
		names = append(names, name)
		return r
	}
	owner := add(SleeveOwner)
	machinePct, machineBase := 0.0, 0.0
	for _, m := range plan.Machine {
		r := add(m.Sleeve)
		r.SharePct, r.StagePct, r.Note = m.SharePct, m.StagePct, m.Note
		r.AllocationPct = m.SharePct * m.StagePct / 100
		r.AllocationBase = r.AllocationPct / 100 * c.BudgetBase * c.Room
		machinePct += r.AllocationPct
		machineBase += r.AllocationBase
	}
	owner.AllocationPct = math.Max(0, p.VolTargetPct-machinePct)
	owner.AllocationBase = math.Max(0, c.BudgetAnnualSD-machineBase)
	pnl := map[string][]float64{}
	for i, l := range legs {
		name := plan.SleeveOf(l.ID)
		r := add(name)
		r.Legs++
		r.ContributionPct += contributionPct[i]
		sum := pnl[name]
		if sum == nil {
			sum = make([]float64, len(legPnL[i]))
			pnl[name] = sum
		}
		for k, v := range legPnL[i] {
			sum[k] += v
		}
	}
	slices.SortStableFunc(names, func(a, b string) int { return cmp.Compare(sleeveRank(a), sleeveRank(b)) })
	out := make([]AllocationReport, 0, len(names))
	for _, name := range names {
		r := byName[name]
		r.UseBase = r.ContributionPct / 100 * risk.SDAnnualBase
		if p := pnl[name]; p != nil {
			_, variance := meanVar(p)
			r.StandaloneBase = math.Sqrt(variance) * math.Sqrt(Scenarios)
		}
		r.ExcessBase = r.UseBase - r.AllocationBase
		r.Over = r.ExcessBase > allocationEpsilon
		out = append(out, *r)
	}
	return out
}

// sleeveRank orders the known sleeves first, in Sleeves order.
func sleeveRank(name string) int {
	if i := slices.Index(Sleeves, name); i >= 0 {
		return i
	}
	return len(Sleeves)
}
