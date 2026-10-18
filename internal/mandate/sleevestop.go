package mandate

import "time"

// The owner sleeve's drawdown stop (internal-docs/design/mandate-risk-budget.md,
// "Risk allocations" and "The forced reduction"; the owner's delegation of
// 2026-10-18 21:41 CEST, "Need your judgement, be on the aggressive side",
// and the agent's setting: the stop is 25% of the owner sleeve's own
// high-water mark, acting reduce-only to half the sleeve's risk, with the
// whole book's 30% maximum loss unchanged as the backstop; reviewed when the
// scorecard holds 100 entry weeks). The daemon keeps the sleeve's equity-like
// measure and its high-water mark (mandate_sleeve_stop.go); this package only
// carries the state into the evaluation and reads the sleeve's use against
// the stop's target, so the sizer and the gate judge one verdict.
//
// The stop releases with hysteresis (2026-10-18 22:00 WEST, after the quality
// loop showed the stop ratcheting): engaged at the stop, it holds its pinned
// target while the drawdown stays within SleeveStopReleaseBand points under
// the stop and releases only below that band. Without the band a drawdown
// oscillating around the stop released and re-engaged on alternate readings,
// each re-engagement pinning half of the already halved use, so two readings
// five minutes apart cut the sleeve to a quarter, then an eighth.

// SleeveStop is one sleeve's drawdown stop as the daemon measures it. In an
// Input it carries the persisted state; on a Verdict it also carries the
// sleeve's use on that evaluation and whether the stop still has risk to cut.
type SleeveStop struct {
	Sleeve string `json:"sleeve"`
	// StopPct is the policy's stop: the drawdown from the sleeve's high-water
	// mark, in percent of the mark, at which the stop engages. ReleasePct is
	// the drawdown under which an engaged stop releases: the stop less the
	// release band (SleeveStopRelease).
	StopPct    float64 `json:"stop_pct"`
	ReleasePct float64 `json:"release_pct,omitempty"`
	// Known is false, with Reasons, when the sleeve's measure could not be
	// read this round (the capital state unknown or stale, the book
	// unmeasured): the stop then holds its last state and sizes nothing new.
	Known   bool     `json:"known"`
	Reasons []string `json:"reasons,omitempty"`
	// StartedAt is the mandate's start the series counts from.
	StartedAt string `json:"started_at,omitempty"`
	// RiskBase is the sleeve's equity-like measure now: the book's
	// cash-flow-adjusted risk base less the machine sleeves' marked P&L since
	// the start. HWMRiskBase is its high-water mark (confirmed as the book's
	// is) and HWMAsOf when it was set.
	RiskBase    float64   `json:"risk_base,omitempty"`
	HWMRiskBase float64   `json:"hwm_risk_base,omitempty"`
	HWMAsOf     time.Time `json:"hwm_as_of,omitzero"`
	// MachinePnLBase is the machine sleeves' marked P&L since the start, the
	// part of the book's move that is not the owner's.
	MachinePnLBase float64 `json:"machine_pnl_base,omitempty"`
	// DrawdownBase and DrawdownPct are the fall from the high-water mark.
	DrawdownBase float64 `json:"drawdown_base,omitempty"`
	DrawdownPct  float64 `json:"drawdown_pct,omitempty"`
	// Engaged is true from the reading at which the drawdown reached the stop
	// until the reading at which it is back under ReleasePct; EngagedAt is
	// the first. While engaged the sleeve is reduce-only, a drawdown back
	// under the stop but inside the release band included.
	Engaged   bool      `json:"engaged"`
	EngagedAt time.Time `json:"engaged_at,omitzero"`
	// UseAtStopBase is the sleeve's use (its Euler contribution, in base
	// currency a year) on the verdict that engaged the stop; TargetUseBase is
	// half of it, the risk the forced reduction cuts the sleeve to. Both are
	// pinned for the engagement, through the release band, so a sleeve cut
	// to half is not cut again; only a release clears them, and the next
	// engagement pins the use of its own reading.
	UseAtStopBase float64 `json:"use_at_stop_base,omitempty"`
	TargetUseBase float64 `json:"target_use_base,omitempty"`
	// UseBase is the sleeve's use on this verdict; Over is true while the
	// stop is engaged and the use is above the target: the forced reduction
	// then has something to cut. Both are set by Evaluate.
	UseBase float64 `json:"use_base,omitempty"`
	Over    bool    `json:"over,omitempty"`
}

// SleeveStopHalf is the share of the sleeve's risk at the stop that the
// reduction keeps: the stop cuts the sleeve to half its risk.
const SleeveStopHalf = 0.5

// SleeveStopReleaseBand is how far under the stop, in points of the
// high-water mark, the drawdown must be back for an engaged stop to release:
// 25% stops at 25 and releases under 20. Five points is a fifth of the stop
// the owner delegated and the same width as the room the stop leaves under
// the book's 30% maximum loss; at the 15% volatility budget it is about five
// daily standard deviations, so a crossing back is a recovery, never one
// reading's noise. A stop under ten points keeps half of itself as the band,
// so the release stays above zero.
const SleeveStopReleaseBand = 5.0

// SleeveStopRelease is the drawdown, in percent of the high-water mark,
// under which a stop at stopPct releases.
func SleeveStopRelease(stopPct float64) float64 {
	return stopPct - min(SleeveStopReleaseBand, stopPct/2)
}

// ApplySleeveStop reads the sleeve's use on a measured verdict against the
// stop's target. Without an allocation report for the sleeve every leg is the
// owner's, so the use is the book's risk.
func ApplySleeveStop(v *Verdict, stop *SleeveStop) {
	if stop == nil {
		return
	}
	out := *stop
	if v.Risk != nil {
		out.UseBase = v.Risk.SDAnnualBase
		if a, ok := v.Allocation(out.Sleeve); ok {
			out.UseBase = a.UseBase
		}
		out.Over = out.Engaged && out.UseBase > out.TargetUseBase+allocationEpsilon
	}
	v.OwnerStop = &out
}
