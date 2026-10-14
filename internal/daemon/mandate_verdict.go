package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/osauer/canary/v2/internal/mandate"
	"github.com/osauer/canary/v2/internal/risk"
	"github.com/osauer/canary/v2/internal/rpc"
)

// The mandate verdict (internal-docs/design/mandate-risk-budget.md): the
// daemon gathers the capital state, one current positions read and its
// decision-grade series, runs mandate.Evaluate and keeps the result. The
// additions gate reads the kept verdict (never a broker read on the order
// path) and refuses a risk-adding order while it is not within, is older
// than mandateVerdictMaxAge, or the order itself would take the risk over
// the budget.

const (
	// mandateVerdictMaxAge is how old a verdict the gate still trusts.
	mandateVerdictMaxAge = 5 * time.Minute
	// mandateVerdictRefreshEvery is the background cadence; a read serves a
	// verdict younger than mandateVerdictServeWithin as it is.
	mandateVerdictRefreshEvery = 2 * time.Minute
	mandateVerdictServeWithin  = 30 * time.Second
	mandateBudgetCode          = rpc.MandateBudgetBlockerCode
	// mandateStreakSpacing is how far apart two scheduled verdicts must be to
	// count as consecutive for the forced reduction; mandateStreakMaxGap is
	// how long a reading may wait for the next before the streak starts
	// again. A read's own evaluation never counts: only the background
	// cadence, so polling cannot build the streak in seconds.
	mandateStreakSpacing = 5 * time.Minute
	mandateStreakMaxGap  = 15 * time.Minute
)

type mandateVerdictCache struct {
	mu          sync.Mutex
	result      *rpc.MandateRiskResult
	input       *mandate.Input
	evaluatedAt time.Time
	fingerprint string
	// overStreak counts consecutive scheduled evaluations reading over or
	// reduce, each at least mandateStreakSpacing and at most
	// mandateStreakMaxGap after the last one counted (streakAt), under one
	// policy (streakFP); the forced reduction starts at two, or at one on a
	// gap-cap breach (mandate_reduction.go). Any reading, scheduled or not,
	// that is neither over nor reduce resets it. scheduledAt is the last
	// scheduled evaluation, which keeps the cadence however often reads
	// evaluate.
	overStreak  int
	streakAt    time.Time
	streakFP    string
	scheduledAt time.Time
}

// mandateVerdictKept is one read of the cache.
type mandateVerdictKept struct {
	result      *rpc.MandateRiskResult
	input       *mandate.Input
	evaluatedAt time.Time
	fingerprint string
	overStreak  int
}

func (c *mandateVerdictCache) current() (*rpc.MandateRiskResult, *mandate.Input, time.Time, string) {
	k := c.kept()
	return k.result, k.input, k.evaluatedAt, k.fingerprint
}

func (c *mandateVerdictCache) kept() mandateVerdictKept {
	c.mu.Lock()
	defer c.mu.Unlock()
	return mandateVerdictKept{result: c.result, input: c.input, evaluatedAt: c.evaluatedAt, fingerprint: c.fingerprint, overStreak: c.overStreak}
}

// set keeps one evaluation; scheduled says it came from the background
// cadence rather than a read, and only those count toward the streak.
func (c *mandateVerdictCache) set(result *rpc.MandateRiskResult, input *mandate.Input, at time.Time, fingerprint string, scheduled bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	breach := result != nil && result.Verdict != nil && (result.Verdict.Status == mandate.StatusOver || result.Verdict.Status == mandate.StatusReduce)
	switch {
	case !breach || (c.overStreak > 0 && fingerprint != c.streakFP):
		c.overStreak, c.streakAt, c.streakFP = 0, time.Time{}, ""
	}
	if breach && scheduled {
		gap := at.Sub(c.streakAt)
		switch {
		case c.overStreak > 0 && gap < mandateStreakSpacing:
			// Too soon after the last counted reading: it neither counts nor
			// resets.
		case c.overStreak > 0 && gap <= mandateStreakMaxGap:
			c.overStreak, c.streakAt = c.overStreak+1, at
		default:
			c.overStreak, c.streakAt, c.streakFP = 1, at, fingerprint
		}
	}
	if scheduled {
		c.scheduledAt = at
	}
	c.result, c.input, c.evaluatedAt, c.fingerprint = result, input, at, fingerprint
}

// lastScheduled is the time of the last scheduled evaluation.
func (c *mandateVerdictCache) lastScheduled() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.scheduledAt
}

// mandateVerdictFingerprint names one verdict for the rows sized on it: the
// evaluation time, the status, the risk and budget figures, the policy and
// the book's leg quantities. Two evaluations of the same book at the same
// instant share it; any other reading has its own.
func mandateVerdictFingerprint(res *rpc.MandateRiskResult) string {
	if res == nil || res.Verdict == nil {
		return ""
	}
	v := res.Verdict
	var b strings.Builder
	fmt.Fprintf(&b, "%s|%s|", v.AsOf.UTC().Format(time.RFC3339Nano), v.Status)
	if v.Risk != nil {
		fmt.Fprintf(&b, "%.6f|", v.Risk.SDAnnualBase)
	}
	fmt.Fprintf(&b, "%.6f|", v.Capital.BudgetAnnualSD)
	if res.PolicyFingerprint != nil {
		b.WriteString(res.PolicyFingerprint.Key)
	}
	for _, l := range v.Legs {
		fmt.Fprintf(&b, "|%s=%g", l.ID, l.Quantity)
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:6])
}

// mandateInactiveResult is the payload while the constitution carries no
// [mandate] table: nothing is measured and the gate is inactive.
func mandateInactiveResult(now time.Time, mgr riskPolicySnapshot) *rpc.MandateRiskResult {
	msg := "the risk constitution carries no [mandate] table; the mandate budget is inactive"
	if mgr.policy == nil {
		msg = "no risk constitution is in force; the mandate budget is inactive"
	}
	return &rpc.MandateRiskResult{AsOf: now, Active: false, Message: msg, Gate: &rpc.MandateGateView{}}
}

// refreshMandateVerdict re-evaluates on the background cadence; it never
// reads the account, which the ordinary account reads keep observed.
func (s *Server) refreshMandateVerdict(ctx context.Context) {
	if s.riskPolicies == nil {
		return
	}
	c := s.riskPolicies.snapshot().policy
	if !c.MandateActive() {
		s.mandateVerdicts.set(nil, nil, time.Time{}, "", true)
		return
	}
	// A revised policy is judged at once; otherwise the cadence decides. A
	// read's evaluation does not move it: the forced reduction counts only
	// scheduled verdicts.
	if _, _, _, fp := s.mandateVerdicts.current(); fp == c.FingerprintKey() {
		if last := s.mandateVerdicts.lastScheduled(); !last.IsZero() && s.nowUTC().Sub(last) < mandateVerdictRefreshEvery {
			return
		}
	}
	s.evaluateMandate(ctx, false)
}

// handleMandateRisk serves the daemon-owned verdict: a verdict younger than
// mandateVerdictServeWithin as it is, else a fresh evaluation after an
// account read, so the capital state is observed before it is judged.
func (s *Server) handleMandateRisk(ctx context.Context, req *rpc.Request) (*rpc.MandateRiskResult, error) {
	var p rpc.MandateRiskParams
	if len(req.Params) > 0 && string(req.Params) != "null" {
		if err := decodeParams(req.Params, &p); err != nil {
			return nil, err
		}
	}
	now := s.nowUTC()
	mgr := s.riskPolicies.snapshot()
	if !mgr.policy.MandateActive() {
		return mandateInactiveResult(now, mgr), nil
	}
	if result, _, at, fp := s.mandateVerdicts.current(); !p.Refresh && result != nil && fp == mgr.policy.FingerprintKey() && now.Sub(at) < mandateVerdictServeWithin {
		out := *result
		out.Gate = s.mandateGateView(&out, now)
		out.Reduction = s.mandateReductionView()
		return &out, nil
	}
	res := s.evaluateMandate(ctx, true)
	res.Reduction = s.mandateReductionView()
	return res, nil
}

// evaluateMandate runs one evaluation and keeps it. With refreshAccount the
// account is read first (which observes equity into the capital state): a
// read's evaluation. Without it, the evaluation is the scheduled one, the
// only kind the forced reduction's streak counts.
func (s *Server) evaluateMandate(ctx context.Context, refreshAccount bool) *rpc.MandateRiskResult {
	now := s.nowUTC()
	mgr := s.riskPolicies.snapshot()
	c := mgr.policy
	if !c.MandateActive() {
		res := mandateInactiveResult(now, mgr)
		s.mandateVerdicts.set(nil, nil, time.Time{}, "", !refreshAccount)
		return res
	}
	if refreshAccount {
		acctCtx, cancel := requestCtx(ctx, rpc.MethodAccountSummary)
		_, _ = s.handleAccountSummary(acctCtx)
		cancel()
	}
	scope := s.currentBrokerStateScope()
	base := normCcy(c.Capital.BaseCurrency)
	capital, _ := s.riskCapital.MandateCapitalForScope(c, scope, now)
	in := mandate.Input{Now: now, BaseCurrency: base, Policy: mandatePolicyNumbers(c), Capital: capital,
		Series: map[string]mandate.Series{}, FX: map[string]mandate.Series{}, FXNow: map[string]float64{}}
	if unapproved := c.Mandate.UnapprovedKeys(); len(unapproved) > 0 {
		in.Reasons = append(in.Reasons, "mandate keys are unwritten: "+strings.Join(unapproved, ", "))
	}
	if base == "" {
		in.Reasons = append(in.Reasons, "capital.base_currency is unapproved")
	}
	evidence := &rpc.MandateEvidence{EvaluatedAt: now, EquityAsOf: capital.EquityAsOf}
	pos, err := s.mandatePositions(ctx)
	switch {
	case err != nil:
		in.Reasons = append(in.Reasons, "positions could not be read: "+errText(err))
	case pos == nil || !currentPortfolioAuthority(pos.Authority):
		in.Reasons = append(in.Reasons, "the positions read is not current for the selected account")
	case brokerScopeConcrete(scope) && !strings.EqualFold(strings.TrimSpace(pos.Authority.Scope.AccountID), strings.TrimSpace(scope.Account)):
		in.Reasons = append(in.Reasons, "the positions read belongs to another account than the selected one")
	case pos.Portfolio != nil && pos.Portfolio.BaseCurrency != "" && base != "" && normCcy(pos.Portfolio.BaseCurrency) != base:
		in.Reasons = append(in.Reasons, fmt.Sprintf("the account base currency %s differs from capital.base_currency %s", pos.Portfolio.BaseCurrency, base))
	}
	if pos != nil {
		evidence.PositionsAsOf = pos.AsOf
		var reasons []string
		in.Legs, in.NotModelled, in.FXNow, reasons = mandateLegsFromPositions(pos, base, now)
		in.Reasons = append(in.Reasons, reasons...)
		for _, need := range mandateHistoryNeeds(pos, base) {
			series := s.mandateHistorySeries(ctx, need.Key)
			row := rpc.MandateSeriesEvidence{Key: need.Key, Missing: series == nil}
			if series != nil {
				row.Closes, row.LastClose, row.FetchedAt, row.Source = len(series.Closes), series.Dates[len(series.Dates)-1], series.FetchedAt, series.Source
				row.Stale = mandateSeriesStale(series, now)
				conv := mandateSeriesInput(series, now)
				if ccy, ok := strings.CutPrefix(need.Key, "fx:"); ok {
					in.FX[ccy] = conv
				} else {
					in.Series[strings.TrimPrefix(need.Key, "u:")] = conv
				}
			}
			evidence.Series = append(evidence.Series, row)
		}
	}
	verdict := mandate.Evaluate(in)
	fp := c.FingerprintKey()
	res := &rpc.MandateRiskResult{AsOf: now, Active: true, Verdict: &verdict, Evidence: evidence,
		PolicyFingerprint: &rpc.Fingerprint{Version: rpc.RiskConstitutionFingerprintVersion, Key: fp},
		Policy:            mandatePolicyView(c.Mandate)}
	res.Gate = s.mandateGateView(res, now)
	s.mandateVerdicts.set(res, &in, now, fp, !refreshAccount)
	return res
}

func mandatePolicyView(m *risk.ConstitutionMandate) *rpc.MandatePolicyView {
	if m == nil {
		return nil
	}
	v := &rpc.MandatePolicyView{MaxLossPct: policyCheckDeref(m.MaxLossPct), VolTargetPct: policyCheckDeref(m.VolTargetPct), TaxReserveBase: policyCheckDeref(m.TaxReserveBase),
		StartedAt: m.StartedAt, GapShockPct: policyCheckDeref(m.GapShockPct), IndexGapShockPct: m.IndexGapShockPct, GapRoomPct: policyCheckDeref(m.GapRoomPct), Unapproved: m.UnapprovedKeys()}
	if m.Sleeves != nil {
		v.Sleeves = &rpc.MandateSleevesView{OwnerVolPct: m.Sleeves.OwnerVolPct, CoreVolPct: m.Sleeves.CoreVolPct, OverlayVolPct: m.Sleeves.OverlayVolPct}
	}
	return v
}

// mandateLegsFromPositions maps one positions read to the evaluator's legs.
// Bills, bonds and conversions are listed as not modelled; a stale row, a
// row without a price, spot or FX rate, and any other security type are
// reasons for an unknown verdict, named per line.
func mandateLegsFromPositions(pos *rpc.PositionsResult, base string, now time.Time) (legs []mandate.Leg, notModelled []string, fxNow map[string]float64, reasons []string) {
	fxNow = map[string]float64{}
	noteFX := func(row rpc.PositionView) {
		ccy := normCcy(row.Currency)
		if ccy == "" || ccy == base {
			return
		}
		if rate, ok := positionBaseRate(row, base); ok {
			fxNow[ccy] = rate
		}
	}
	legID := mandateLegID
	for _, row := range pos.Stocks {
		secType := strings.ToUpper(strings.TrimSpace(row.SecType))
		switch {
		case secType == "BILL" || secType == "BOND":
			notModelled = append(notModelled, fmt.Sprintf("%s (%s, no equity risk modelled)", row.Symbol, strings.ToLower(secType)))
			continue
		case secType == "CASH":
			notModelled = append(notModelled, fmt.Sprintf("%s (currency conversion)", row.Symbol))
			continue
		case positionWarningHasCode(row.WarningDetails, zeroValueStockPositionCode):
			notModelled = append(notModelled, fmt.Sprintf("%s (zero-value row, likely inactive)", row.Symbol))
			continue
		}
		if !positionQuotesAsStock(row) {
			reasons = append(reasons, fmt.Sprintf("%s carries security type %s, which the mandate does not model", row.Symbol, secType))
			continue
		}
		if row.Stale {
			reasons = append(reasons, fmt.Sprintf("%s has a stale quote (%s)", row.Symbol, nonEmptyString(row.StaleReason, "no reason given")))
		}
		noteFX(row)
		legs = append(legs, mandate.Leg{ID: legID(row), Symbol: normSym(row.Symbol), Underlying: normSym(row.Symbol), Kind: mandate.KindStock,
			Quantity: row.Quantity, Multiplier: 1, Currency: normCcy(row.Currency), Spot: row.Mark})
	}
	for _, row := range pos.Options {
		if row.Stale {
			reasons = append(reasons, fmt.Sprintf("%s %s %g %s has a stale quote (%s)", row.Symbol, row.Expiry, row.Strike, row.Right, nonEmptyString(row.StaleReason, "no reason given")))
		}
		noteFX(row)
		spot := 0.0
		if row.Underlying != nil && *row.Underlying > 0 {
			spot = *row.Underlying
		} else if row.PrevClose != nil && *row.PrevClose > 0 {
			spot = *row.PrevClose
		}
		legs = append(legs, mandate.Leg{ID: legID(row), Symbol: fmt.Sprintf("%s %s %g %s", normSym(row.Symbol), row.Expiry, row.Strike, strings.ToUpper(row.Right)),
			Underlying: normSym(row.Symbol), Kind: mandate.KindOption, Quantity: row.Quantity, Multiplier: float64(optionMultiplier(row)),
			Currency: normCcy(row.Currency), Spot: spot, Right: strings.ToUpper(strings.TrimSpace(row.Right)), Strike: row.Strike,
			TimeToExpiryYears: mandateYearsToExpiry(row.Expiry, now), IV: row.IV})
	}
	return legs, notModelled, fxNow, reasons
}

// mandateLegID identifies a position row as the evaluator's leg: the exact
// contract id, or the identity fields when the row carries none.
func mandateLegID(row rpc.PositionView) string {
	if row.ConID > 0 {
		return fmt.Sprintf("con:%d", row.ConID)
	}
	return "sym:" + normSym(row.Symbol) + ":" + normCcy(row.Currency) + ":" + row.Expiry + ":" + row.Right + ":" + fmt.Sprint(row.Strike)
}

// mandateYearsToExpiry measures to the close of the expiry day in New York;
// an unreadable expiry reads zero, so the leg prices at intrinsic.
func mandateYearsToExpiry(expiryYMD string, now time.Time) float64 {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		ny = time.UTC
	}
	day, err := time.ParseInLocation("20060102", strings.TrimSpace(expiryYMD), ny)
	if err != nil {
		return 0
	}
	close := time.Date(day.Year(), day.Month(), day.Day(), 16, 0, 0, 0, ny)
	return math.Max(0, close.Sub(now).Hours()/24/365)
}

// mandateGateView says how the gate reads a result right now.
func (s *Server) mandateGateView(res *rpc.MandateRiskResult, now time.Time) *rpc.MandateGateView {
	if res == nil || !res.Active || res.Verdict == nil {
		return &rpc.MandateGateView{}
	}
	if age := now.Sub(res.AsOf); age > mandateVerdictMaxAge {
		return &rpc.MandateGateView{RefusesAdditions: true, Reason: fmt.Sprintf("the verdict is %s old; the gate trusts one up to %s", formatQuoteAge(age), mandateVerdictMaxAge)}
	}
	if res.Verdict.Status != mandate.StatusWithin {
		return &rpc.MandateGateView{RefusesAdditions: true, Reason: mandateStatusSentence(res.Verdict, res.Verdict.BaseCurrency)}
	}
	return &rpc.MandateGateView{}
}

// mandateStatusSentence states a verdict in one sentence for a refusal.
func mandateStatusSentence(v *mandate.Verdict, ccy string) string {
	switch v.Status {
	case mandate.StatusUnknown:
		why := "no reason given"
		if len(v.Reasons) > 0 {
			why = v.Reasons[0]
		}
		return "the mandate risk cannot be measured: " + why
	case mandate.StatusOver, mandate.StatusReduce:
		if v.Risk == nil {
			return "the mandate risk is " + v.Status + " budget"
		}
		sentence := fmt.Sprintf("the mandate risk %s a year is over the budget %s", mandateMoney(v.Risk.SDAnnualBase, ccy), mandateMoney(v.Capital.BudgetAnnualSD, ccy))
		if !math.IsInf(v.Risk.RatioToBudget, 0) && v.Capital.BudgetAnnualSD > 0 {
			sentence += fmt.Sprintf(" (%.0f%%)", v.Risk.RatioToBudget*100)
		} else if v.Capital.Known && v.Capital.BudgetAnnualSD <= 0 {
			sentence += " (the loss has reached the mandate's maximum, so no risk is in budget)"
		}
		if v.Stresses != nil && len(v.Stresses.GapBreaches) > 0 {
			sentence += "; a gap in " + strings.Join(v.Stresses.GapBreaches, ", ") + " would lose more than the gap cap " + mandateMoney(v.Stresses.GapCapBase, ccy)
		}
		return sentence
	}
	return "the mandate risk is within budget"
}

func mandateMoney(v float64, ccy string) string {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return "unmeasurable"
	}
	return policyCheckMoney(math.Round(v), ccy)
}

// riskAddingOrder is the one classification the drawdown brake and the
// mandate gate share: a close or reduce never adds risk, and a BUY of a put
// on a Rulebook hedge symbol is a hedge entry that stays open; everything
// else adds risk. Cancels never reach either gate.
func (s *Server) riskAddingOrder(draft rpc.OrderDraft, position rpc.OrderPositionImpact) bool {
	switch position.Effect {
	case rpc.OrderPositionEffectClose, rpc.OrderPositionEffectReduce:
		return false
	}
	if strings.EqualFold(draft.Action, "BUY") && strings.EqualFold(draft.Contract.SecType, "OPT") &&
		strings.EqualFold(draft.Contract.Right, "P") && s.rulebookPolicy().IsHedgeSymbol(draft.Contract.Symbol) {
		return false
	}
	return true
}

// mandateBudgetGate is the additions gate at every enforcement point the
// drawdown brake has. While the constitution carries no [mandate] table it
// refuses nothing. Otherwise a risk-adding order is refused unless a kept
// verdict no older than mandateVerdictMaxAge reads within, and the order
// itself, re-evaluated on the same book, keeps the risk inside the budget.
// Cheap by construction: the kept verdict and a simulation of milliseconds,
// never a broker read.
func (s *Server) mandateBudgetGate(draft rpc.OrderDraft, position rpc.OrderPositionImpact) *rpc.TradingBlocker {
	if s == nil || s.riskPolicies == nil || s.riskCapital == nil {
		return nil
	}
	if !s.riskAddingOrder(draft, position) {
		return nil
	}
	c := s.riskPolicies.snapshot().policy
	if !c.MandateActive() {
		return nil
	}
	now := s.nowUTC()
	const open = " Reductions, closes, cancels and hedges stay open."
	const action = "Reduce or close instead, or wait for the verdict to read within; `canary mandate` has the measurement."
	result, input, at, fp := s.mandateVerdicts.current()
	if result == nil || result.Verdict == nil || input == nil || fp != c.FingerprintKey() {
		return &rpc.TradingBlocker{Code: mandateBudgetCode, Message: "Mandate budget: no verdict has been measured for the policy in force yet; this order adds risk." + open, Action: action}
	}
	if age := now.Sub(at); age > mandateVerdictMaxAge {
		return &rpc.TradingBlocker{Code: mandateBudgetCode, Message: fmt.Sprintf("Mandate budget: the verdict is %s old and cannot be shown current; this order adds risk.", formatQuoteAge(age)) + open, Action: action}
	}
	ccy := result.Verdict.BaseCurrency
	if result.Verdict.Status != mandate.StatusWithin {
		return &rpc.TradingBlocker{Code: mandateBudgetCode, Message: "Mandate budget: " + mandateStatusSentence(result.Verdict, ccy) + "; this order adds risk." + open, Action: action}
	}
	// A name or currency the book does not hold yet has no decision-grade
	// history in the verdict: the order is measured on the series the store
	// keeps, and refused, its series requested on the background lane, while
	// none is fresh. Never measured blind, never passed unmeasured.
	book, missing := s.mandateOrderBook(draft, input, now)
	if len(missing) > 0 {
		var why []string
		var needs []mandateHistoryNeed
		for _, m := range missing {
			why = append(why, m.reason)
			if m.need.Key != "" {
				needs = append(needs, m.need)
			}
		}
		if len(needs) > 0 {
			s.requestMandateHistory(needs...)
		}
		return &rpc.TradingBlocker{Code: mandateBudgetCode, Message: "Mandate budget: this order cannot be measured: " + strings.Join(why, "; ") + "." + open,
			Action: "Retry once the history has arrived (the background lane reads it within minutes); `canary mandate` has the measurement."}
	}
	leg, ok := mandateOrderLeg(draft, &book)
	if !ok {
		return nil // an option whose volatility its limit cannot give: the standing verdict decides
	}
	with := mandate.Evaluate(mandate.WithLeg(book, leg))
	if with.Status == mandate.StatusUnknown || with.Risk == nil {
		why := "no reason given"
		if len(with.Reasons) > 0 {
			why = with.Reasons[0]
		}
		return &rpc.TradingBlocker{Code: mandateBudgetCode, Message: "Mandate budget: with this order the book cannot be measured: " + why + "." + open, Action: action}
	}
	budget := with.Capital.BudgetAnnualSD
	if with.Risk.SDAnnualBase > budget {
		return &rpc.TradingBlocker{Code: mandateBudgetCode,
			Message: fmt.Sprintf("Mandate budget: with this order the risk would be %s a year, over the budget %s (now %s).", mandateMoney(with.Risk.SDAnnualBase, ccy), mandateMoney(budget, ccy), mandateMoney(result.Verdict.Risk.SDAnnualBase, ccy)) + open,
			Action:  "Size it smaller, or reduce elsewhere first; `canary mandate` has the measurement."}
	}
	// The gap caps bind an addition as the budget does: an order that
	// would create a gap-cap breach, or deepen one, is refused, or the
	// forced reduction would sell it back on the next readings
	// (core-sleeve.md, review B1).
	if u, ok := mandateGapBreachAdded(result.Verdict, with); ok {
		return &rpc.TradingBlocker{Code: mandateBudgetCode,
			Message: fmt.Sprintf("Mandate budget: with this order a %s gap in %s would lose %s, over the gap cap %s.", mandateGapPct(u.GapShockPct), u.Underlying, mandateMoney(u.GapLossBase, ccy), mandateMoney(u.GapCapBase, ccy)) + open,
			Action:  "Size it smaller; `canary mandate` has each name's gap against its cap."}
	}
	return nil
}

// mandateGapBreachAdded finds the first underlying whose gap loss breaches
// its cap with the order and did not before it, or breaches by more than it
// did: an addition that creates or deepens a gap-cap breach.
func mandateGapBreachAdded(before *mandate.Verdict, with mandate.Verdict) (mandate.UnderlyingReport, bool) {
	prior := map[string]mandate.UnderlyingReport{}
	if before != nil {
		for _, u := range before.Underlyings {
			prior[u.Underlying] = u
		}
	}
	for _, u := range with.Underlyings {
		if !u.GapBreach {
			continue
		}
		was, held := prior[u.Underlying]
		if !held || !was.GapBreach || u.GapLossBase > was.GapLossBase+1e-6 {
			return u, true
		}
	}
	return mandate.UnderlyingReport{}, false
}

// mandateGapPct names a gap shock in a refusal: "−25%".
func mandateGapPct(v float64) string {
	return "−" + strconv.FormatFloat(v, 'f', -1, 64) + "%"
}

// mandateBudgetGateError is the gate as the admission and wire-guard checks
// report it: a trading-disabled error carrying the blocker's words.
func (s *Server) mandateBudgetGateError(draft rpc.OrderDraft, position rpc.OrderPositionImpact) error {
	blocker := s.mandateBudgetGate(draft, position)
	if blocker == nil {
		return nil
	}
	return fmt.Errorf("%w: %s", ErrTradingDisabled, blocker.Message)
}

// mandateMissing is one series an order needs that the store cannot give
// fresh, and why; need is empty when nothing can be fetched for it.
type mandateMissing struct {
	need   mandateHistoryNeed
	reason string
}

// mandateOrderBook is the kept book with the decision-grade series an order
// needs and the verdict does not carry (a name or currency the book does
// not hold), read from the history store, or what is missing: the
// underlying's series, the base currency's pair against the order's
// currency, or the currency itself. A store read, never a broker read.
func (s *Server) mandateOrderBook(draft rpc.OrderDraft, in *mandate.Input, now time.Time) (mandate.Input, []mandateMissing) {
	book := *in
	book.Series, book.FX, book.FXNow = maps.Clone(in.Series), maps.Clone(in.FX), maps.Clone(in.FXNow)
	if book.Series == nil {
		book.Series = map[string]mandate.Series{}
	}
	if book.FX == nil {
		book.FX = map[string]mandate.Series{}
	}
	if book.FXNow == nil {
		book.FXNow = map[string]float64{}
	}
	sym := normSym(draft.Contract.Symbol)
	ccy := normCcy(draft.Contract.Currency)
	for _, l := range in.Legs {
		if l.Underlying == sym && ccy == "" {
			ccy = normCcy(l.Currency)
		}
	}
	if sym == "" {
		return book, []mandateMissing{{reason: "the order names no instrument"}}
	}
	if ccy == "" {
		return book, []mandateMissing{{reason: "the order names no currency for " + sym}}
	}
	fresh := func(key string) (*mandateHistorySeries, bool) {
		series := s.mandateHistorySeries(context.Background(), key)
		return series, series != nil && !mandateSeriesStale(series, now) && len(series.Closes) >= mandate.Closes
	}
	var missing []mandateMissing
	if _, ok := book.Series[sym]; !ok {
		contract := rpc.ContractParams{Symbol: sym, SecType: "STK", Currency: ccy, Exchange: "SMART"}
		if strings.EqualFold(draft.Contract.SecType, "STK") || strings.EqualFold(draft.Contract.SecType, "ETF") {
			contract.ConID, contract.PrimaryExch = draft.Contract.ConID, strings.ToUpper(strings.TrimSpace(draft.Contract.PrimaryExch))
		}
		if series, ok := fresh("u:" + sym); ok {
			book.Series[sym] = mandateSeriesInput(series, now)
		} else {
			missing = append(missing, mandateMissing{need: mandateHistoryNeed{Key: "u:" + sym, Contract: contract}, reason: "no daily history for " + sym + " yet; the desk is fetching it"})
		}
	}
	if base := normCcy(in.BaseCurrency); ccy != base {
		if _, ok := book.FX[ccy]; !ok {
			if series, ok := fresh("fx:" + ccy); ok {
				book.FX[ccy] = mandateSeriesInput(series, now)
			} else {
				missing = append(missing, mandateMissing{need: mandateHistoryNeed{Key: "fx:" + ccy, Contract: rpc.ContractParams{Symbol: base, SecType: "CASH", Currency: ccy, Exchange: "IDEALPRO"}},
					reason: "no daily history for the " + base + "/" + ccy + " exchange rate yet; the desk is fetching it"})
			}
		}
		if _, ok := book.FXNow[ccy]; !ok {
			if fx, ok := book.FX[ccy]; ok && len(fx.Closes) > 0 {
				book.FXNow[ccy] = fx.Closes[len(fx.Closes)-1]
			}
		}
	}
	return book, missing
}

// mandateOrderLeg prices an order as a leg of the kept book: a stock at the
// held line's spot (else, for a name not held, its last decision-grade
// close, else the order's limit), an option at the held contract's terms
// and IV, or, for a contract not held, at the volatility its limit price
// implies on the underlying's spot (held, else its last decision-grade
// close). False when the addition cannot be priced without a guess.
func mandateOrderLeg(draft rpc.OrderDraft, in *mandate.Input) (mandate.Leg, bool) {
	qty := float64(draft.Quantity)
	if strings.EqualFold(draft.Action, rpc.OrderActionSell) {
		qty = -qty
	}
	sym := normSym(draft.Contract.Symbol)
	ccy := normCcy(draft.Contract.Currency)
	id := ""
	if draft.Contract.ConID > 0 {
		id = fmt.Sprintf("con:%d", draft.Contract.ConID)
	}
	var held *mandate.Leg
	var underlyingSpot float64
	for i := range in.Legs {
		l := &in.Legs[i]
		if id != "" && l.ID == id {
			held = l
		}
		if l.Underlying == sym && l.Spot > 0 {
			underlyingSpot = l.Spot
			if ccy == "" {
				ccy = l.Currency
			}
		}
	}
	if ccy != "" && ccy != strings.ToUpper(in.BaseCurrency) {
		if _, ok := in.FXNow[ccy]; !ok {
			return mandate.Leg{}, false
		}
	}
	if underlyingSpot <= 0 {
		// A name the book does not hold: its last decision-grade close.
		if series, ok := in.Series[sym]; ok && len(series.Closes) > 0 {
			underlyingSpot = series.Closes[len(series.Closes)-1]
		}
	}
	switch strings.ToUpper(draft.Contract.SecType) {
	case "STK", "ETF":
		spot := underlyingSpot
		if held != nil && held.Kind == mandate.KindStock {
			spot = held.Spot
		}
		if spot <= 0 {
			spot = draft.LimitPrice
		}
		if spot <= 0 || qty == 0 {
			return mandate.Leg{}, false
		}
		if id == "" {
			id = "order:" + sym
		}
		if held != nil {
			id = held.ID
		}
		return mandate.Leg{ID: id, Symbol: sym, Underlying: sym, Kind: mandate.KindStock, Quantity: qty, Multiplier: 1, Currency: ccy, Spot: spot}, true
	case "OPT":
		if held != nil && held.Kind == mandate.KindOption && held.IV != nil {
			leg := *held
			leg.Quantity = qty
			return leg, true
		}
		right := strings.ToUpper(strings.TrimSpace(draft.Contract.Right))
		mult := float64(draft.Contract.Multiplier)
		if mult <= 0 {
			mult = 100
		}
		t := mandateYearsToExpiry(draft.Contract.Expiry, in.Now)
		if underlyingSpot <= 0 || draft.Contract.Strike <= 0 || (right != "C" && right != "P") || draft.LimitPrice <= 0 || t <= 0 || qty == 0 {
			return mandate.Leg{}, false
		}
		iv, ok := mandate.ImpliedVolatility(right == "C", draft.LimitPrice, underlyingSpot, draft.Contract.Strike, t)
		if !ok {
			return mandate.Leg{}, false
		}
		if id == "" {
			id = fmt.Sprintf("order:%s:%s:%g:%s", sym, draft.Contract.Expiry, draft.Contract.Strike, right)
		}
		return mandate.Leg{ID: id, Symbol: fmt.Sprintf("%s %s %g %s", sym, draft.Contract.Expiry, draft.Contract.Strike, right), Underlying: sym, Kind: mandate.KindOption,
			Quantity: qty, Multiplier: mult, Currency: ccy, Spot: underlyingSpot, Right: right, Strike: draft.Contract.Strike, TimeToExpiryYears: t, IV: &iv}, true
	}
	return mandate.Leg{}, false
}
