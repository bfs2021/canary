package mandate

import (
	"math"
	"testing"
)

// The owner sleeve's stop rides on the verdict: the sleeve's use is its
// allocation's use, or the book's risk without allocations, and the stop is
// over only while engaged with the use above its target. Without a stop the
// verdict carries none.
func TestSleeveStopReadsTheOwnersUseAgainstItsTarget(t *testing.T) {
	legs := []Leg{
		{ID: "con:1", Symbol: "SYNA", Underlying: "SYNA", Kind: KindStock, Quantity: 400, Multiplier: 1, Currency: "USD", Spot: 100},
		{ID: "con:3", Symbol: "SYNB", Underlying: "SYNB", Kind: KindStock, Quantity: 300, Multiplier: 1, Currency: "EUR", Spot: 80},
	}
	in := testInput(t, legs)
	if v := Evaluate(in); v.OwnerStop != nil {
		t.Fatalf("a verdict without a stop carries one: %+v", v.OwnerStop)
	}
	in.Allocations = testAllocationPlan(100, map[string]string{"con:3": SleeveCore})
	plain := Evaluate(in)
	owner, _ := plain.Allocation(SleeveOwner)
	in.OwnerStop = &SleeveStop{Sleeve: SleeveOwner, StopPct: 25, ReleasePct: 20, Known: true, Engaged: true, TargetUseBase: owner.UseBase / 2}
	v := Evaluate(in)
	if v.OwnerStop == nil || !v.OwnerStop.Over || v.OwnerStop.UseBase != owner.UseBase || v.OwnerStop.TargetUseBase != owner.UseBase/2 || v.OwnerStop.ReleasePct != 20 {
		t.Fatalf("engaged over its target: %+v (owner use %g)", v.OwnerStop, owner.UseBase)
	}
	in.OwnerStop.TargetUseBase = owner.UseBase
	if v := Evaluate(in); v.OwnerStop.Over {
		t.Fatalf("at the target the stop has nothing to cut: %+v", v.OwnerStop)
	}
	in.OwnerStop.TargetUseBase, in.OwnerStop.Engaged = owner.UseBase/2, false
	if v := Evaluate(in); v.OwnerStop.Over || !v.OwnerStop.Known {
		t.Fatalf("not engaged, never over: %+v", v.OwnerStop)
	}
	// Without allocations every leg is the owner's: the use is the book's risk.
	in.Allocations, in.OwnerStop.Engaged = nil, true
	v = Evaluate(in)
	if v.OwnerStop.UseBase != v.Risk.SDAnnualBase || !v.OwnerStop.Over {
		t.Fatalf("without allocations: %+v risk %g", v.OwnerStop, v.Risk.SDAnnualBase)
	}
	// An unmeasured book carries the stop as given, with no use.
	in.Legs[0].Spot = 0
	if v := Evaluate(in); v.Status != StatusUnknown || v.OwnerStop == nil || v.OwnerStop.Over || v.OwnerStop.UseBase != 0 {
		t.Fatalf("unknown book: %s %+v", v.Status, v.OwnerStop)
	}
	if SleeveStopHalf != 0.5 || math.Abs(0.5*owner.UseBase-in.OwnerStop.TargetUseBase) > 1e-9 {
		t.Fatalf("the stop cuts to half the sleeve's risk")
	}
}

// The release sits five points under the stop, or half the stop when the
// stop is smaller than ten: 25 releases under 20, 10 under 5, 6 under 3.
func TestSleeveStopReleaseIsFivePointsUnderTheStop(t *testing.T) {
	for stop, want := range map[float64]float64{25: 20, 10: 5, 6: 3, 4: 2} {
		if got := SleeveStopRelease(stop); math.Abs(got-want) > 1e-12 {
			t.Fatalf("stop %g releases under %g, want %g", stop, got, want)
		}
	}
	if SleeveStopReleaseBand != 5 {
		t.Fatalf("band %g", SleeveStopReleaseBand)
	}
}
