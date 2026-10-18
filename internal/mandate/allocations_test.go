package mandate

import (
	"math"
	"math/rand/v2"
	"testing"
)

func testAllocationPlan(coreStage float64, placed map[string]string) *AllocationPlan {
	return &AllocationPlan{Machine: []MachineAllocation{{Sleeve: SleeveCore, TargetPct: 6, StagePct: coreStage}, {Sleeve: SleeveOverlay, TargetPct: 4}}, Placed: placed}
}

func near(a, b, tol float64) bool {
	return math.Abs(a-b) <= tol*math.Max(1, math.Max(math.Abs(a), math.Abs(b)))
}

// The uses add up to the book's risk, the allocations to the budget; the core's
// allocation is its stage times its allocation, the owner's the rest; every use is at
// most the sleeve's standalone risk.
func TestAllocationsAddUpToTheBookAndTheBudget(t *testing.T) {
	in := testInput(t, []Leg{
		{ID: "con:1", Symbol: "SYNA", Underlying: "SYNA", Kind: KindStock, Quantity: 300, Multiplier: 1, Currency: "USD", Spot: 100},
		{ID: "con:2", Symbol: "SYNB", Underlying: "SYNB", Kind: KindStock, Quantity: 200, Multiplier: 1, Currency: "EUR", Spot: 80},
		{ID: "con:3", Symbol: "SPY", Underlying: "SPY", Kind: KindStock, Quantity: 100, Multiplier: 1, Currency: "USD", Spot: 100},
	})
	in.Allocations = testAllocationPlan(25, map[string]string{"con:3": SleeveCore})
	v := Evaluate(in)
	if v.Status == StatusUnknown || len(v.Allocations) != 3 {
		t.Fatalf("status %s allocations %+v reasons %v", v.Status, v.Allocations, v.Reasons)
	}
	if v.Allocations[0].Sleeve != SleeveOwner || v.Allocations[1].Sleeve != SleeveCore || v.Allocations[2].Sleeve != SleeveOverlay {
		t.Fatalf("order %+v", v.Allocations)
	}
	budget := v.Capital.BudgetAnnualSD
	core, _ := v.Allocation(SleeveCore)
	owner, _ := v.Allocation(SleeveOwner)
	overlay, _ := v.Allocation(SleeveOverlay)
	if want := 0.25 * 0.06 * v.Capital.BudgetBase * v.Capital.Room; !near(core.AllocationBase, want, 1e-12) || core.AllocationPct != 1.5 {
		t.Fatalf("core allocation %g (%g%%), want %g (1.5%%)", core.AllocationBase, core.AllocationPct, want)
	}
	if overlay.AllocationBase != 0 || overlay.Legs != 0 || overlay.UseBase != 0 {
		t.Fatalf("overlay %+v", overlay)
	}
	if !near(owner.AllocationBase, budget-core.AllocationBase, 1e-12) || owner.AllocationPct != 13.5 {
		t.Fatalf("owner allocation %g (%g%%), want the rest %g", owner.AllocationBase, owner.AllocationPct, budget-core.AllocationBase)
	}
	if !near(owner.AllocationBase+core.AllocationBase+overlay.AllocationBase, budget, 1e-12) {
		t.Fatalf("allocations do not add up to the budget")
	}
	if !near(owner.UseBase+core.UseBase+overlay.UseBase, v.Risk.SDAnnualBase, 1e-9) || !near(owner.ContributionPct+core.ContributionPct, 100, 1e-9) {
		t.Fatalf("uses %g + %g, book %g", owner.UseBase, core.UseBase, v.Risk.SDAnnualBase)
	}
	if owner.Legs != 2 || core.Legs != 1 {
		t.Fatalf("legs %d %d", owner.Legs, core.Legs)
	}
	for _, s := range v.Allocations {
		if s.UseBase > s.StandaloneBase+1e-6 {
			t.Fatalf("%s use %g above its standalone %g", s.Sleeve, s.UseBase, s.StandaloneBase)
		}
		if !near(s.ExcessBase, s.UseBase-s.AllocationBase, 1e-12) || s.Over != (s.ExcessBase > allocationEpsilon) {
			t.Fatalf("%s excess %+v", s.Sleeve, s)
		}
	}
	// Without a plan nothing is measured per allocation.
	in.Allocations = nil
	if v := Evaluate(in); v.Allocations != nil {
		t.Fatalf("allocations without a plan: %+v", v.Allocations)
	}
}

// A contract held by two sleeves splits its contribution exactly in
// proportion to the units each holds: the Euler contribution is linear in a
// leg's units.
func TestAllocationsSplitAContractByUnits(t *testing.T) {
	whole := testInput(t, []Leg{
		{ID: "a", Symbol: "SYNA", Underlying: "SYNA", Kind: KindStock, Quantity: 400, Multiplier: 1, Currency: "USD", Spot: 100},
		{ID: "b", Symbol: "SYNB", Underlying: "SYNB", Kind: KindStock, Quantity: 200, Multiplier: 1, Currency: "EUR", Spot: 80},
	})
	whole.Allocations = testAllocationPlan(100, nil)
	split := whole
	split.Legs = []Leg{whole.Legs[0], whole.Legs[0], whole.Legs[1]}
	split.Legs[0].ID, split.Legs[0].Quantity = "a/owner", 300
	split.Legs[1].ID, split.Legs[1].Quantity = "a/core", 100
	split.Allocations = testAllocationPlan(100, map[string]string{"a/core": SleeveCore})
	w, s := Evaluate(whole), Evaluate(split)
	if !near(w.Risk.SDAnnualBase, s.Risk.SDAnnualBase, 1e-12) {
		t.Fatalf("the split changed the book's risk: %g vs %g", w.Risk.SDAnnualBase, s.Risk.SDAnnualBase)
	}
	var lineA float64
	for _, l := range w.Legs {
		if l.ID == "a" {
			lineA = l.ContributionPct
		}
	}
	core, _ := s.Allocation(SleeveCore)
	if !near(core.ContributionPct, lineA/4, 1e-9) {
		t.Fatalf("core holds a quarter of the line: %g%%, want %g%%", core.ContributionPct, lineA/4)
	}
}

// At the maximum loss the room is zero: every allocation is zero and a sleeve with
// risk is over it. A machine written above the target leaves the owner a
// zero allocation, never a negative one.
func TestAllocationsAtRoomZeroAndAnOverfullMachine(t *testing.T) {
	in := testInput(t, []Leg{{ID: "a", Symbol: "SYNA", Underlying: "SYNA", Kind: KindStock, Quantity: 300, Multiplier: 1, Currency: "USD", Spot: 100}})
	in.Capital = knownCapital(t, 300000-0.3*230000, 70000, 300000, 0)
	in.Allocations = testAllocationPlan(100, nil)
	v := Evaluate(in)
	if v.Capital.Room != 0 {
		t.Fatalf("room %g", v.Capital.Room)
	}
	for _, s := range v.Allocations {
		if s.AllocationBase != 0 {
			t.Fatalf("%s allocation %g at room 0", s.Sleeve, s.AllocationBase)
		}
	}
	if owner, _ := v.Allocation(SleeveOwner); !owner.Over {
		t.Fatalf("owner at room 0 %+v", owner)
	}

	in = testInput(t, in.Legs)
	in.Allocations = &AllocationPlan{Machine: []MachineAllocation{{Sleeve: SleeveCore, TargetPct: 10, StagePct: 100}, {Sleeve: SleeveOverlay, TargetPct: 8, StagePct: 100}}}
	v = Evaluate(in)
	owner, _ := v.Allocation(SleeveOwner)
	if owner.AllocationBase != 0 || owner.AllocationPct != 0 || !owner.Over {
		t.Fatalf("owner under an overfull machine %+v", owner)
	}
}

// Property: a sleeve whose own risk fits its allocation stays inside it whatever
// the other sleeves add, because its use is its standalone risk times its
// correlation with the book.
func TestAllocationASleeveInsideItsShareStaysInside(t *testing.T) {
	base := testInput(t, nil)
	r := rand.New(rand.NewPCG(5, 6))
	for trial := range 40 {
		in := base
		in.Allocations = testAllocationPlan(100, map[string]string{"core": SleeveCore})
		// The core: an index line sized near its allocation on its own.
		in.Legs = []Leg{{ID: "core", Symbol: "SPY", Underlying: "SPY", Kind: KindStock, Quantity: 1, Multiplier: 1, Currency: "USD", Spot: 100}}
		alone := Evaluate(in)
		allocation, _ := alone.Allocation(SleeveCore)
		in.Legs[0].Quantity = math.Floor(allocation.AllocationBase / alone.Risk.SDAnnualBase * (0.5 + 0.5*r.Float64()))
		// The owner: anything, long or short, large or small.
		for _, l := range []struct{ sym, ccy string }{{"SYNA", "USD"}, {"SYNB", "EUR"}, {"SPY", "USD"}} {
			in.Legs = append(in.Legs, Leg{ID: "own:" + l.sym, Symbol: l.sym, Underlying: l.sym, Kind: KindStock, Quantity: math.Round((r.Float64()*2 - 0.6) * 3000),
				Multiplier: 1, Currency: l.ccy, Spot: 100})
		}
		v := Evaluate(in)
		core, _ := v.Allocation(SleeveCore)
		if core.StandaloneBase > core.AllocationBase+1e-6 {
			t.Fatalf("trial %d: the core's own risk %g is above its allocation %g", trial, core.StandaloneBase, core.AllocationBase)
		}
		if core.Over || core.UseBase > core.StandaloneBase+1e-6 {
			t.Fatalf("trial %d: core use %g, standalone %g, allocation %g", trial, core.UseBase, core.StandaloneBase, core.AllocationBase)
		}
	}
}
