package daemon

import (
	"math"
	"slices"
	"time"

	"github.com/osauer/canary/v2/internal/mandate"
	"github.com/osauer/canary/v2/internal/risk"
)

// Mandate capital state (internal-docs/design/mandate-risk-budget.md): the
// high-water mark of the cash-flow-adjusted equity since the mandate's start
// date, kept inside the risk_capital document beside the adjusted peak so it
// is persisted, scoped and committed the same way. The tax reserve is
// subtracted at read time, so a changed reserve moves the high-water mark and
// today's risk base alike and never creates a drawdown.

const (
	mandateCapitalStateVer = 1
	// mandateDailySampleKeep bounds the daily adjusted-equity samples the
	// policy check's realised figures read.
	mandateDailySampleKeep = 400 * 24 * time.Hour
)

// mandateCapitalStateV1 is the mandate's part of riskCapitalStateFileV1.
type mandateCapitalStateV1 struct {
	Version int `json:"version"`
	// StartedAt is the policy's started_at this series counts from; a
	// different value in the policy starts the series again.
	StartedAt string `json:"started_at"`
	Seeded    bool   `json:"seeded"`
	// HWMAdjustedEquity is the high-water mark of equity − flows since the
	// start; HWMAsOf is the observation that set it.
	HWMAdjustedEquity float64   `json:"hwm_adjusted_equity_base"`
	HWMAsOf           time.Time `json:"hwm_as_of,omitzero"`
	// FlowsAtStart is the capital store's effective cumulative flow when the
	// series was seeded; flows since the start are measured against it.
	FlowsAtStart float64 `json:"flows_at_start_base"`
	// Daily is the latest adjusted equity per UTC day.
	Daily map[string]float64 `json:"daily_adjusted_equity,omitempty"`
}

// observeMandateLocked folds one equity observation into the mandate state.
// It returns whether the document must persist at once (a start or a new
// high-water mark); daily samples persist with the ordinary throttle.
func (st *riskCapitalStore) observeMandateLocked(c *risk.Constitution, equityBase float64, asOf time.Time, flows float64, now time.Time) bool {
	if c == nil || c.Mandate == nil {
		return false
	}
	start, ok := c.Mandate.MandateStart()
	if !ok || asOf.UTC().Before(start) {
		return false
	}
	force := false
	m := st.state.Mandate
	if m == nil || m.StartedAt != c.Mandate.StartedAt {
		m = &mandateCapitalStateV1{Version: mandateCapitalStateVer, StartedAt: c.Mandate.StartedAt, FlowsAtStart: flows}
		st.state.Mandate = m
		st.appendRiskPolicyJournal(map[string]any{
			"version": 1, "at": now.UTC(), "kind": "mandate_started", "started_at": c.Mandate.StartedAt,
			"flows_at_start_base": flows, "equity_base": equityBase, "equity_as_of": asOf.UTC(),
			"policy_fingerprint": constitutionFingerprint(c),
		})
		force = true
	}
	adjusted := equityBase - (flows - m.FlowsAtStart)
	if !m.Seeded || adjusted > m.HWMAdjustedEquity {
		st.appendRiskPolicyJournal(map[string]any{
			"version": 1, "at": now.UTC(), "kind": "mandate_hwm_advanced",
			"from_base": m.HWMAdjustedEquity, "to_base": adjusted, "seed": !m.Seeded,
			"equity_base": equityBase, "equity_as_of": asOf.UTC(),
			"policy_fingerprint": constitutionFingerprint(c),
		})
		m.Seeded = true
		m.HWMAdjustedEquity = adjusted
		m.HWMAsOf = asOf
		force = true
	}
	if m.Daily == nil {
		m.Daily = map[string]float64{}
	}
	m.Daily[asOf.UTC().Format(time.DateOnly)] = adjusted
	cutoff := now.UTC().Add(-mandateDailySampleKeep).Format(time.DateOnly)
	for day := range m.Daily {
		if day < cutoff {
			delete(m.Daily, day)
		}
	}
	return force
}

// mandateRealised is what the policy check reads of the risk base's own
// history: the realised drawdown and the realised volatility over the last
// 20 and 60 daily samples, annualised.
type mandateRealised struct {
	Samples    int
	Vol20Pct   *float64
	Vol60Pct   *float64
	LastSample string
}

// mandatePolicyNumbers reads the [mandate] numbers the evaluator needs; a
// nil key reads zero, which EvaluateCapital reports as unknown.
func mandatePolicyNumbers(c *risk.Constitution) mandate.Policy {
	if c == nil || c.Mandate == nil {
		return mandate.Policy{}
	}
	m := c.Mandate
	return mandate.Policy{MaxLossPct: policyCheckDeref(m.MaxLossPct), VolTargetPct: policyCheckDeref(m.VolTargetPct),
		GapShockPct: policyCheckDeref(m.GapShockPct), GapRoomPct: policyCheckDeref(m.GapRoomPct)}
}

// MandateCapitalForScope evaluates the mandate's capital state from the
// persisted equity observation, the flows and the high-water mark of the
// selected account, and the realised figures from its daily samples.
func (st *riskCapitalStore) MandateCapitalForScope(c *risk.Constitution, scope brokerStateScope, now time.Time) (mandate.Capital, mandateRealised) {
	policy := mandatePolicyNumbers(c)
	if st == nil || c == nil || c.Mandate == nil {
		return mandate.EvaluateCapital(policy, mandate.CapitalInput{Now: now}), mandateRealised{}
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	st.loadLocked()
	if st.core != nil {
		if !brokerScopeConcrete(scope) {
			return mandate.Capital{Reasons: []string{"capital state requires one selected broker account"}}, mandateRealised{}
		}
		if err := st.selectScopeLocked(scope); err != nil {
			return mandate.Capital{Reasons: []string{riskCapitalPublicScopeReason(err)}}, mandateRealised{}
		}
	}
	// The read pairs the bound document's own persisted equity with its own
	// high-water mark, self-consistent for the bound account whatever the
	// session (as NudgeSnapshotForScope reads the ladder); with SQLite the
	// scope selected the document above.
	flows, _, _ := st.effectiveFlowsLocked(c)
	in := mandate.CapitalInput{BaseCurrency: c.Capital.BaseCurrency, StartedAt: c.Mandate.StartedAt,
		EquityBase: st.state.LastEquityBase, EquityAsOf: st.state.LastEquityAsOf, TaxReserveBase: policyCheckDeref(c.Mandate.TaxReserveBase), Now: now}
	if c.Capital.MaxEquityAgeMinutes != nil {
		in.EquityAgeLimit = time.Duration(*c.Capital.MaxEquityAgeMinutes) * time.Minute
	}
	m := st.state.Mandate
	if m != nil && m.StartedAt == c.Mandate.StartedAt && m.Seeded {
		in.Seeded = true
		in.HWMAdjustedEquity, in.HWMAsOf = m.HWMAdjustedEquity, m.HWMAsOf
		in.FlowsSinceStart = flows - m.FlowsAtStart
	}
	capital := mandate.EvaluateCapital(policy, in)
	var realised mandateRealised
	if m != nil && m.StartedAt == c.Mandate.StartedAt {
		realised = mandateRealisedFrom(m.Daily, in.TaxReserveBase)
	}
	return capital, realised
}

// mandateRealisedFrom measures the realised volatility of the risk base
// (adjusted equity after the reserve) from the daily samples: the sample
// standard deviation of daily log-returns over the last n samples, ×√252.
func mandateRealisedFrom(daily map[string]float64, reserve float64) mandateRealised {
	days := make([]string, 0, len(daily))
	for day, v := range daily {
		if v-reserve > 0 {
			days = append(days, day)
		}
	}
	slices.Sort(days)
	out := mandateRealised{Samples: len(days)}
	if len(days) > 0 {
		out.LastSample = days[len(days)-1]
	}
	vol := func(n int) *float64 {
		if len(days) < n+1 {
			return nil
		}
		window := days[len(days)-n-1:]
		returns := make([]float64, 0, n)
		for i := 1; i < len(window); i++ {
			returns = append(returns, math.Log((daily[window[i]]-reserve)/(daily[window[i-1]]-reserve)))
		}
		mean := 0.0
		for _, r := range returns {
			mean += r
		}
		mean /= float64(len(returns))
		variance := 0.0
		for _, r := range returns {
			variance += (r - mean) * (r - mean)
		}
		variance /= float64(len(returns) - 1)
		v := math.Sqrt(variance) * math.Sqrt(252) * 100
		return &v
	}
	out.Vol20Pct, out.Vol60Pct = vol(20), vol(60)
	return out
}
