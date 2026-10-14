package daemon

import (
	"fmt"
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
	// mandateHighConfirm is how far apart two readings above the high-water
	// mark must be before the mark moves up: one glitchy equity print never
	// raises it, which would read as a drawdown from then on and, with the
	// forced reduction armed, sell the book back to a budget it never lost.
	mandateHighConfirm = 5 * time.Minute
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
	// PendingHigh is the lowest reading above the mark since PendingHighAt,
	// the first of them; a reading at least mandateHighConfirm later that is
	// still above the mark moves it to the lower of the two. Any reading at
	// or under the mark clears it.
	PendingHigh   float64   `json:"pending_high_adjusted_base,omitempty"`
	PendingHighAt time.Time `json:"pending_high_at,omitzero"`
	// Last is the previous observation the flow check compares with;
	// FlowSuspect an equity move the book's P&L does not explain, which
	// holds the room until a declared or statement flow explains it, or a
	// statement covering its day shows none.
	Last        *mandateFlowBase    `json:"last_observation,omitempty"`
	FlowSuspect *mandateFlowSuspect `json:"flow_suspect,omitempty"`
	// FlowsAtStart is the capital store's effective cumulative flow when the
	// series was seeded; flows since the start are measured against it.
	FlowsAtStart float64 `json:"flows_at_start_base"`
	// Daily is the latest adjusted equity per UTC day.
	Daily map[string]float64 `json:"daily_adjusted_equity,omitempty"`
}

// mandateSessionDate is the New York trading date of a time.
func mandateSessionDate(at time.Time) string {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		ny = time.UTC
	}
	return at.In(ny).Format(time.DateOnly)
}

// mandatePnLReading is the broker's daily P&L (account base currency, since
// the session's start) read with an equity observation, and that session.
type mandatePnLReading struct {
	DailyBase *float64
	Session   string
}

// mandateFlowBase is one observation as the flow check keeps it.
type mandateFlowBase struct {
	At         time.Time `json:"at"`
	EquityBase float64   `json:"equity_base"`
	FlowsBase  float64   `json:"flows_base"`
	DailyPnL   *float64  `json:"daily_pnl_base,omitempty"`
	Session    string    `json:"session,omitempty"`
}

// mandateFlowSuspect is an equity move the book's own P&L does not explain:
// a cash flow nobody has declared yet. Base is the observation before it.
type mandateFlowSuspect struct {
	At          time.Time       `json:"at"`
	Base        mandateFlowBase `json:"base"`
	EquityMove  float64         `json:"equity_move_base"`
	PnLMove     float64         `json:"pnl_move_base"`
	ThresholdAt float64         `json:"threshold_base"`
}

// mandateUnexplainedMove measures the equity move from base to an
// observation that the flows and the broker's daily P&L do not explain: the
// move, the P&L over the same span (the new session's own P&L when it spans
// a session start), the threshold max(1% of equity, 3 × |P&L move|), and
// whether the P&L is known on both ends; without it nothing is judged.
func mandateUnexplainedMove(base mandateFlowBase, equityBase, flows float64, pnl mandatePnLReading) (move, pnlMove, threshold float64, ok bool) {
	if pnl.DailyBase == nil || base.DailyPnL == nil {
		return 0, 0, 0, false
	}
	move = equityBase - base.EquityBase - (flows - base.FlowsBase)
	pnlMove = *pnl.DailyBase
	if base.Session == pnl.Session {
		pnlMove -= *base.DailyPnL
	}
	threshold = math.Max(0.01*equityBase, 3*math.Abs(pnlMove))
	return move, pnlMove, threshold, true
}

// mandateFlowCheckLocked raises, keeps or clears the flow suspicion for one
// observation. An equity move larger than max(1% of equity, 3 × the book's
// own P&L move) since the last observation, with no declared or statement
// flow to account for it, is a suspected cash flow: an undeclared
// withdrawal would read as a drawdown and shrink the budget, a deposit
// would raise the high-water mark and read as a drawdown once declared. The
// suspicion clears once the move from the observation before it is
// explained (a flow declared or incorporated from the statement, or the
// P&L catching up), or a statement covering its day has been reconciled.
func (st *riskCapitalStore) mandateFlowCheckLocked(m *mandateCapitalStateV1, c *risk.Constitution, equityBase float64, pnl mandatePnLReading, asOf time.Time, flows float64, now time.Time) {
	if s := m.FlowSuspect; s != nil {
		move, _, threshold, ok := mandateUnexplainedMove(s.Base, equityBase, flows, pnl)
		statement := !st.state.StatementCoverageTo.IsZero() && !utcDateBefore(st.state.StatementCoverageTo, s.At)
		if (ok && math.Abs(move) <= threshold) || statement {
			st.appendRiskPolicyJournal(map[string]any{
				"version": 1, "at": now.UTC(), "kind": "mandate_flow_cleared",
				"suspected_at": s.At.UTC(), "equity_base": equityBase, "flows_base": flows, "by_statement": statement && !(ok && math.Abs(move) <= threshold),
				"policy_fingerprint": constitutionFingerprint(c),
			})
			m.FlowSuspect = nil
		}
	} else if m.Last != nil {
		if move, pnlMove, threshold, ok := mandateUnexplainedMove(*m.Last, equityBase, flows, pnl); ok && math.Abs(move) > threshold {
			m.FlowSuspect = &mandateFlowSuspect{At: asOf, Base: *m.Last, EquityMove: move, PnLMove: pnlMove, ThresholdAt: threshold}
			st.appendRiskPolicyJournal(map[string]any{
				"version": 1, "at": now.UTC(), "kind": "mandate_flow_suspected",
				"equity_move_base": move, "pnl_move_base": pnlMove, "threshold_base": threshold, "equity_base": equityBase, "equity_as_of": asOf.UTC(),
				"policy_fingerprint": constitutionFingerprint(c),
			})
		}
	}
	m.Last = &mandateFlowBase{At: asOf, EquityBase: equityBase, FlowsBase: flows, DailyPnL: cloneFloat64Ptr(pnl.DailyBase), Session: pnl.Session}
}

// observeMandateLocked folds one equity observation into the mandate state.
// It returns whether the document must persist at once (a start, a new
// high-water mark, or a flow suspicion raised or cleared); daily samples
// persist with the ordinary throttle.
func (st *riskCapitalStore) observeMandateLocked(c *risk.Constitution, equityBase float64, pnl mandatePnLReading, asOf time.Time, flows float64, now time.Time) bool {
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
	suspected := m.FlowSuspect != nil
	st.mandateFlowCheckLocked(m, c, equityBase, pnl, asOf, flows, now)
	if (m.FlowSuspect != nil) != suspected {
		force = true
	}
	advance := func(to float64) {
		st.appendRiskPolicyJournal(map[string]any{
			"version": 1, "at": now.UTC(), "kind": "mandate_hwm_advanced",
			"from_base": m.HWMAdjustedEquity, "to_base": to, "seed": !m.Seeded,
			"equity_base": equityBase, "equity_as_of": asOf.UTC(),
			"policy_fingerprint": constitutionFingerprint(c),
		})
		m.Seeded = true
		m.HWMAdjustedEquity = to
		m.HWMAsOf = asOf
		force = true
	}
	switch {
	case !m.Seeded:
		advance(adjusted)
	case m.FlowSuspect != nil:
		// The room is held while a flow is suspected: no new high, and a
		// pending one does not carry over.
		m.PendingHigh, m.PendingHighAt = 0, time.Time{}
	case adjusted <= m.HWMAdjustedEquity:
		// The high did not hold: a pending reading above the mark was a
		// glitch or a moment.
		m.PendingHigh, m.PendingHighAt = 0, time.Time{}
	case m.PendingHighAt.IsZero():
		m.PendingHigh, m.PendingHighAt = adjusted, asOf
	case asOf.Sub(m.PendingHighAt) < mandateHighConfirm:
		m.PendingHigh = math.Min(m.PendingHigh, adjusted)
	default:
		// Confirmed by a reading far enough apart: the mark moves to the
		// lower of the two, and a higher reading waits for its own.
		to := math.Min(m.PendingHigh, adjusted)
		advance(to)
		m.PendingHigh, m.PendingHighAt = 0, time.Time{}
		if adjusted > to {
			m.PendingHigh, m.PendingHighAt = adjusted, asOf
		}
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
	if m != nil && m.StartedAt == c.Mandate.StartedAt && m.FlowSuspect != nil {
		s := m.FlowSuspect
		capital.Known = false
		capital.Reasons = append([]string{fmt.Sprintf("a cash flow is suspected: equity moved %s at %s that the book's own P&L (%s) does not explain; the room is held until the flow is declared (canary policy capital-event) or a statement explains it",
			mandateMoney(s.EquityMove, in.BaseCurrency), s.At.UTC().Format(time.DateTime), mandateMoney(s.PnLMove, in.BaseCurrency))}, capital.Reasons...)
	}
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
