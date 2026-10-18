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
	ibkrlib "github.com/osauer/canary/v2/pkg/ibkr"
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
	mgr := s.riskPolicies.snapshot()
	c := mgr.policy
	if !c.MandateActive() {
		res := mandateInactiveResult(s.nowUTC(), mgr)
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
	// The verdict's clock is read once the capital state is in hand, so it is
	// never older than the equity observation it is judged on.
	capital, _ := s.riskCapital.MandateCapitalForScope(c, scope, s.nowUTC())
	now := s.nowUTC()
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
		in.LineISINs = s.coreFundLines(ctx)
		in.Reasons = append(in.Reasons, reasons...)
		for _, need := range mandateHistoryNeeds(pos, base, in.LineISINs) {
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
	// The allocation plan follows the resolved strategy register; it reads
	// the legs, since a live strategy on hold keeps its reservation only
	// while its sleeve holds positions.
	resolved := s.resolvedRegister(c)
	in.Allocations = mandateAllocationPlan(c, resolved, s.mandateCoreContracts(ctx, c), in.Legs)
	verdict := mandate.Evaluate(in)
	fp := c.FingerprintKey()
	res := &rpc.MandateRiskResult{AsOf: now, Active: true, Verdict: &verdict, Evidence: evidence,
		PolicyFingerprint: &rpc.Fingerprint{Version: rpc.RiskConstitutionFingerprintVersion, Key: fp},
		Policy:            mandatePolicyView(c.Mandate)}
	res.Gate = s.mandateGateView(res, now)
	res.Register = mandateRegisterView(resolved)
	res.StatusSince = s.mandateStatusSince(ctx, verdict.Status, now)
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
	ccy := res.Verdict.BaseCurrency
	switch res.Verdict.Status {
	case mandate.StatusWithin:
	case mandate.StatusOver, mandate.StatusReduce:
		// Over budget, your sleeve keeps adding while it is inside its own
		// allocation (owner, 2026-10-18 15:31 CEST).
		if why := mandateOverBudgetHolds(res.Verdict, mandate.SleeveOwner, ccy); why != "" {
			return &rpc.MandateGateView{RefusesAdditions: true, Reason: mandateStatusSentence(res.Verdict, ccy) + "; " + why}
		}
		return &rpc.MandateGateView{Reason: mandateStatusSentence(res.Verdict, ccy) + ", but your sleeve is inside its allocation: an addition that keeps it there passes"}
	default:
		return &rpc.MandateGateView{RefusesAdditions: true, Reason: mandateStatusSentence(res.Verdict, ccy)}
	}
	// Within the budget, your additions still stop at your allocation.
	if allocation, ok := res.Verdict.Allocation(mandate.SleeveOwner); ok {
		if over, alone := mandateSleeveOver(allocation); over {
			return &rpc.MandateGateView{RefusesAdditions: true, Reason: mandateAllocationOverSentence(mandate.SleeveOwner, allocation, mandateMachineAllocations(*res.Verdict), "", mandateSleeveUse(allocation, alone), alone, res.Verdict.BaseCurrency)}
		}
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
// else adds risk. Cancels never reach either gate. The mandate gate alone
// also passes a bill purchase (mandateBillBuy).
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

// mandateBillBuy reports a BUY of a government bill (security type BILL).
// The mandate measures a held bill as carrying no equity or volatility
// risk, so buying one moves cash into a cash-like line and adds none the
// budget counts (owner, 2026-10-17 06:47 CEST). A bond stays risk-adding:
// its interest-rate risk is not measured, so a long bond could carry real
// risk counted as zero. The drawdown brake does not read this.
func mandateBillBuy(draft rpc.OrderDraft) bool {
	return strings.EqualFold(strings.TrimSpace(draft.Action), rpc.OrderActionBuy) &&
		strings.EqualFold(strings.TrimSpace(draft.Contract.SecType), ibkrlib.SecTypeBill)
}

// mandateBudgetGate is the additions gate at every enforcement point the
// drawdown brake has. While the constitution carries no [mandate] table it
// refuses nothing. Otherwise a risk-adding order other than a bill purchase
// is refused unless a kept verdict no older than mandateVerdictMaxAge reads
// within, and the order itself, re-evaluated on the same book, keeps the
// risk inside the budget and its own sleeve inside its allocation (mandate_allocations.go).
// Cheap by construction: the kept verdict and a simulation of milliseconds,
// never a broker read.
func (s *Server) mandateBudgetGate(draft rpc.OrderDraft, position rpc.OrderPositionImpact) *rpc.TradingBlocker {
	if s == nil || s.riskPolicies == nil || s.riskCapital == nil {
		return nil
	}
	if !s.riskAddingOrder(draft, position) || mandateBillBuy(draft) {
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
	sleeve := mandateOrderSleeve(s.mandateCoreContracts(context.Background(), c), draft)
	// A book over budget adds nothing, except in a sleeve inside its own risk
	// allocation (owner, 2026-10-18 15:31 CEST: "yes, it should. unless you
	// advise differently"): that sleeve keeps adding inside its allocation
	// while the forced reduction cuts the sleeve furthest over its own. An
	// unknown verdict adds nothing, ever.
	overBudget := false
	switch result.Verdict.Status {
	case mandate.StatusWithin:
	case mandate.StatusOver, mandate.StatusReduce:
		if why := mandateOverBudgetHolds(result.Verdict, sleeve, ccy); why != "" {
			return &rpc.TradingBlocker{Code: mandateBudgetCode, Message: "Mandate budget: " + mandateStatusSentence(result.Verdict, ccy) + "; " + why + "; this order adds risk." + open, Action: action}
		}
		overBudget = true
	default:
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
		// Fail closed: an order the evaluator cannot price is never passed
		// unmeasured (core sleeve review, 2026-10-14).
		return mandateUnpricedOrderGate(draft, result.Verdict, book, sleeve, overBudget, ccy, open, action)
	}
	// The order's units open in its sleeve: the allocations are judged with it
	// placed there.
	book.Allocations = book.Allocations.WithPlaced(leg.ID, sleeve)
	with := mandate.Evaluate(mandate.WithLeg(book, leg))
	if with.Status == mandate.StatusUnknown || with.Risk == nil {
		why := "no reason given"
		if len(with.Reasons) > 0 {
			why = with.Reasons[0]
		}
		return &rpc.TradingBlocker{Code: mandateBudgetCode, Message: "Mandate budget: with this order the book cannot be measured: " + why + "." + open, Action: action}
	}
	// The whole book with the order must stay inside the budget, unless the
	// book is over it already and the order's sleeve adds inside its own
	// allocation (checked below).
	budget := with.Capital.BudgetAnnualSD
	if !overBudget && with.Risk.SDAnnualBase > budget {
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
	// Allocations sit inside the whole-book limits: an addition also stays inside
	// its own sleeve's allocation (2026-10-18 08:06 CEST).
	return mandateAllocationRefusal(sleeve, result.Verdict, with, ccy, open)
}

// mandateUnpricedOrderGate judges a risk-adding order the evaluator cannot
// price. A stock or ETF without a price is refused. An option sale is
// refused: a short option's loss has no bound without the volatility its
// limit did not give. An option buy is judged by a volatility-free bound
// (mandateLongOptionBound) and refused without a limit, which gives no
// premium to bound it; its sleeve's own risk plus that bound must fit its
// allocation (a sleeve's use is at most its own risk, which the leg can raise by
// at most the bound). Any other security type is left as it was: this check
// judges only what the evaluator models.
func mandateUnpricedOrderGate(draft rpc.OrderDraft, standing *mandate.Verdict, book mandate.Input, sleeve string, overBudget bool, ccy, open, action string) *rpc.TradingBlocker {
	sym := normSym(draft.Contract.Symbol)
	refuse := func(msg, act string) *rpc.TradingBlocker {
		return &rpc.TradingBlocker{Code: mandateBudgetCode, Message: "Mandate budget: " + msg + open, Action: act}
	}
	switch strings.ToUpper(strings.TrimSpace(draft.Contract.SecType)) {
	case "STK", "ETF":
		return refuse("this order cannot be measured: no price for "+sym+".", action)
	case "OPT":
	default:
		return nil
	}
	if strings.EqualFold(draft.Action, rpc.OrderActionSell) {
		return refuse("this option sale cannot be measured: its limit gives no volatility on "+sym+"'s price, and a short option's loss has no bound without one.",
			"Sell at a limit the volatility can be solved from (above intrinsic value, before expiry), or reduce elsewhere first; `canary mandate` has the measurement.")
	}
	if !(draft.LimitPrice > 0) {
		return refuse("this option buy cannot be measured: without a limit there is no premium to bound its loss.", "Buy at a limit; `canary mandate` has the measurement.")
	}
	b, why := mandateLongOptionBound(draft, book, standing)
	if why != "" {
		return refuse("this option buy cannot be measured: "+why+".", action)
	}
	budget := standing.Capital.BudgetAnnualSD
	if !overBudget && b.riskAnnual > budget {
		return refuse(fmt.Sprintf("with this order the risk could reach %s a year, over the budget %s: the option's limit gives no volatility, so it is bounded by %s's full daily moves.",
			mandateMoney(b.riskAnnual, ccy), mandateMoney(budget, ccy), sym), "Size it smaller, or buy at a limit the volatility can be solved from; `canary mandate` has the measurement.")
	}
	if b.gapLoss > b.gapCap || b.breached {
		return refuse(fmt.Sprintf("with this order a gap in %s could lose %s (its premium at most on top of the book's %s), over the gap cap %s.", sym, mandateMoney(b.gapLoss, ccy), mandateMoney(b.gapLoss-b.premium, ccy), mandateMoney(b.gapCap, ccy)),
			"Size it smaller; `canary mandate` has each name's gap against its cap.")
	}
	if allocation, ok := standing.Allocation(sleeve); ok {
		if worst := allocation.StandaloneBase + (b.riskAnnual - standing.Risk.SDAnnualBase); worst > allocation.AllocationBase+mandateReductionEpsilon {
			return refuse("with this order "+mandateAllocationOverSentence(sleeve, allocation, mandateMachineAllocations(*standing), "could", worst, false, ccy)+
				": the option's limit gives no volatility, so the allocation is bounded by its own risk plus "+sym+"'s full daily moves.",
				"Size it smaller, or buy at a limit the volatility can be solved from; `canary mandate` has each allocation.")
		}
	}
	return nil
}

// mandateLongBound is a volatility-free bound on a long option the
// evaluator cannot price.
type mandateLongBound struct {
	premium    float64
	riskAnnual float64
	gapLoss    float64
	gapCap     float64
	breached   bool
}

// mandateLongOptionBound bounds a long option the evaluator cannot price
// (a limit below intrinsic value, an expiry reached, an implied volatility
// that does not solve) without a volatility, in the evaluator's own terms.
//
// Risk: the leg's one-day P&L in scenario k is V(S_k)·fx_k − V(S)·fx, and
// its spread over the scenarios is the spread of V(S_k)·fx_k: the decay is
// the same shift in every scenario. A vanilla option's price moves at most
// as much as its underlying (1-Lipschitz in spot) and is worth at most the
// spot (a call) or the strike (a put), so the spread is at most
// units × the largest rate × the root mean square of the underlying's
// daily moves, plus that largest value × the root mean square of the
// rate's daily relative moves. By the triangle inequality the book's
// annualised risk grows by at most √252 times that.
//
// Gap: an option bought at a limit loses at most what is paid for it, its
// premium at the limit, so the underlying's gap loss grows by at most that
// premium.
//
// Coarse, and only ever above the truth.
func mandateLongOptionBound(draft rpc.OrderDraft, book mandate.Input, standing *mandate.Verdict) (mandateLongBound, string) {
	var b mandateLongBound
	if standing == nil || standing.Risk == nil || standing.Stresses == nil {
		return b, "the standing verdict carries no risk to add the bound to"
	}
	sym := normSym(draft.Contract.Symbol)
	ccy := normCcy(draft.Contract.Currency)
	spot := 0.0
	for _, l := range book.Legs {
		if l.Underlying == sym && l.Spot > 0 {
			spot = l.Spot
			if ccy == "" {
				ccy = normCcy(l.Currency)
			}
		}
	}
	returnsKey := mandate.ReturnsSeries(sym, ccy)
	series, ok := book.Series[returnsKey]
	if !ok || len(series.Closes) < mandate.Closes {
		return b, "no decision-grade daily history for " + sym
	}
	if spot <= 0 && returnsKey == sym {
		spot = series.Closes[len(series.Closes)-1]
	}
	if spot <= 0 {
		return b, "no price for " + sym
	}
	// rms of the relative daily moves over the last mandate.Scenarios
	// returns, and the largest factor a move reached.
	moves := func(closes []float64) (rms, maxFactor float64) {
		closes = closes[len(closes)-mandate.Closes:]
		maxFactor, ss := 1.0, 0.0
		for i := 1; i < len(closes); i++ {
			r := closes[i]/closes[i-1] - 1
			ss += r * r
			maxFactor = math.Max(maxFactor, 1+r)
		}
		return math.Sqrt(ss / float64(len(closes)-1)), maxFactor
	}
	priceRMS, _ := moves(series.Closes)
	fx, fxRMS, fxMax := 1.0, 0.0, 1.0
	if ccy != "" && ccy != normCcy(book.BaseCurrency) {
		rate, ok := book.FXNow[ccy]
		fxSeries, okSeries := book.FX[ccy]
		if !ok || !(rate > 0) || !okSeries || len(fxSeries.Closes) < mandate.Closes {
			return b, "no " + book.BaseCurrency + "/" + ccy + " exchange rate with its history"
		}
		fx = rate
		fxRMS, fxMax = moves(fxSeries.Closes)
	}
	mult := float64(draft.Contract.Multiplier)
	if mult <= 0 {
		mult = 100
	}
	units := float64(draft.Quantity) * mult
	worth := spot // a call is worth at most the spot
	if strings.EqualFold(strings.TrimSpace(draft.Contract.Right), "P") {
		worth = draft.Contract.Strike // a put at most the strike
	}
	if !(worth > 0) {
		return b, "incomplete option terms"
	}
	spread1D := units*fx*fxMax*spot*priceRMS + units*worth*fx*fxMax*fxRMS
	b.premium = units * draft.LimitPrice * fx
	b.riskAnnual = standing.Risk.SDAnnualBase + math.Sqrt(mandate.Scenarios)*spread1D
	b.gapCap = standing.Stresses.GapCapBase
	for _, u := range standing.Underlyings {
		if u.Underlying == sym {
			b.gapLoss, b.gapCap, b.breached = u.GapLossBase, u.GapCapBase, u.GapBreach
		}
	}
	b.gapLoss += b.premium
	return b, ""
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
	if returns := mandate.ReturnsSeriesFor(sym, ccy, in.LineISINs[mandateConLegID(draft.Contract.ConID)]); returns != sym {
		// A line moved by its proxy's returns (mandate.ReturnsProxies): the
		// proxy is the index, which every verdict reads; its price is the
		// held line's or the order's limit, never the proxy's close.
		if _, ok := book.Series[returns]; !ok {
			missing = append(missing, mandateMissing{reason: "no daily history for " + returns + ", whose returns " + sym + " is measured by"})
		}
		held := false
		for _, l := range in.Legs {
			held = held || l.Underlying == sym && l.Spot > 0
		}
		if !held && !(draft.LimitPrice > 0) {
			missing = append(missing, mandateMissing{reason: "no price for " + sym + ": the book does not hold it and the order carries no limit"})
		}
	} else if _, ok := book.Series[sym]; !ok {
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
