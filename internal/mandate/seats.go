package mandate

import (
	"cmp"
	"maps"
	"math"
	"slices"
)

// Seats (internal-docs/design/mandate-risk-budget.md, "Seats"; owner
// decision 2026-10-18 08:06 CEST: "give the machine its own seat (replacing
// D1 and largest-contributor-first)"). Each sleeve has a seat inside the
// whole-book budget: a machine sleeve's seat is its stage times its share,
// the owner's is the rest. A sleeve's use is the sum of its legs' Euler
// contributions, so the uses add up to the book's risk as the seats add up to
// the budget.

// Sleeve names.
const (
	SleeveOwner   = "owner"
	SleeveCore    = "core"
	SleeveOverlay = "overlay"
)

// Sleeves is the order seats are listed in and ties are broken in.
var Sleeves = []string{SleeveOwner, SleeveCore, SleeveOverlay}

// seatEpsilon absorbs float noise when a use is compared with its seat.
const seatEpsilon = 1e-6

// MachineSeat is one machine sleeve's seat as the policy and the daemon's
// build give it.
type MachineSeat struct {
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

// SeatPlan is what the seats are measured on: the machine's seats and the
// machine sleeve each placed leg belongs to. A leg not in Placed is the
// owner's.
type SeatPlan struct {
	Machine []MachineSeat
	Placed  map[string]string
}

// SleeveOf names the sleeve a leg is placed in; every leg is the owner's
// while the plan places none.
func (p *SeatPlan) SleeveOf(legID string) string {
	if p != nil {
		if s, ok := p.Placed[legID]; ok && s != "" {
			return s
		}
	}
	return SleeveOwner
}

// WithPlaced returns a copy of the plan with one more leg placed: an order's
// leg, judged in the sleeve it would open units in.
func (p *SeatPlan) WithPlaced(legID, sleeve string) *SeatPlan {
	if p == nil {
		return nil
	}
	out := &SeatPlan{Machine: p.Machine, Placed: make(map[string]string, len(p.Placed)+1)}
	maps.Copy(out.Placed, p.Placed)
	if sleeve != "" && sleeve != SleeveOwner {
		out.Placed[legID] = sleeve
	}
	return out
}

// SeatReport is one sleeve's seat and its use on a measured book.
type SeatReport struct {
	Sleeve string `json:"sleeve"`
	// SharePct and StagePct are a machine sleeve's share and stage; the
	// owner's seat is the rest and has neither.
	SharePct float64 `json:"share_pct"`
	StagePct float64 `json:"stage_pct"`
	// SeatPct is the seat in percent a year of the budget base at full room:
	// a machine sleeve's share × stage, the owner's the target less the
	// machine's, never below zero.
	SeatPct float64 `json:"seat_pct"`
	// SeatBase is the seat in base currency a year: SeatPct of the budget
	// base × room, the budget's own scaling.
	SeatBase float64 `json:"seat_base"`
	// UseBase is the sum of the sleeve's legs' Euler contributions in base
	// currency a year; ContributionPct is the same as a share of the book's
	// risk. The uses of all sleeves add up to the book's risk.
	UseBase         float64 `json:"use_base"`
	ContributionPct float64 `json:"contribution_pct"`
	// StandaloneBase is the sleeve's risk measured alone, annualised: an
	// upper bound on its use, which is the standalone risk times its
	// correlation with the book.
	StandaloneBase float64 `json:"standalone_base"`
	// ExcessBase is use less seat: positive over the seat, negative the room
	// left. Over is true when the use exceeds the seat.
	ExcessBase float64 `json:"excess_base"`
	Over       bool    `json:"over,omitempty"`
	// Legs counts the legs placed in the sleeve.
	Legs int    `json:"legs"`
	Note string `json:"note,omitempty"`
}

// Seat returns one sleeve's report on a verdict.
func (v Verdict) Seat(sleeve string) (SeatReport, bool) {
	for _, s := range v.Seats {
		if s.Sleeve == sleeve {
			return s, true
		}
	}
	return SeatReport{}, false
}

// seatReports measures every sleeve on one simulation: legs, their scenario
// P&L and Euler shares are index-aligned.
func seatReports(plan *SeatPlan, p Policy, c Capital, legs []Leg, legPnL [][]float64, contributionPct []float64, risk *Risk) []SeatReport {
	byName := map[string]*SeatReport{}
	var names []string
	add := func(name string) *SeatReport {
		if r, ok := byName[name]; ok {
			return r
		}
		r := &SeatReport{Sleeve: name}
		byName[name] = r
		names = append(names, name)
		return r
	}
	owner := add(SleeveOwner)
	machinePct, machineBase := 0.0, 0.0
	for _, m := range plan.Machine {
		r := add(m.Sleeve)
		r.SharePct, r.StagePct, r.Note = m.SharePct, m.StagePct, m.Note
		r.SeatPct = m.SharePct * m.StagePct / 100
		r.SeatBase = r.SeatPct / 100 * c.BudgetBase * c.Room
		machinePct += r.SeatPct
		machineBase += r.SeatBase
	}
	owner.SeatPct = math.Max(0, p.VolTargetPct-machinePct)
	owner.SeatBase = math.Max(0, c.BudgetAnnualSD-machineBase)
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
	out := make([]SeatReport, 0, len(names))
	for _, name := range names {
		r := byName[name]
		r.UseBase = r.ContributionPct / 100 * risk.SDAnnualBase
		if p := pnl[name]; p != nil {
			_, variance := meanVar(p)
			r.StandaloneBase = math.Sqrt(variance) * math.Sqrt(Scenarios)
		}
		r.ExcessBase = r.UseBase - r.SeatBase
		r.Over = r.ExcessBase > seatEpsilon
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
