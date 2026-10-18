// Package core computes the core sleeve's monthly target: the mandate's
// rule-based share in one broad index fund (internal-docs/design/core-sleeve.md).
// The rule is core-ibcf-v1, the lab's s0_core_sleeve spec as registered on
// 2026-10-14: IBCF held while its month-end close is above its 10-month
// average, sized so its volatility is at most its share of the mandate's
// budget. It is a beta allocation the owner chose with a drawdown filter,
// not an edge claim.
//
// Only the daemon evaluates: it gathers the decision-grade closes, the
// exchange calendar, the mandate's capital and the book, calls Measure,
// SizeTarget, Headroom and Decide, and serves the result as core.target.
// The package holds no state and performs no I/O. Its numbers are tested
// against the lab's Python implementation through a shared fixture
// (testdata/core_ibcf_v1_parity.json); a change on either side fails a
// test, and the rule's parameters change only with a new rule version.
package core

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// The rule and the spec it implements. A policy naming another rule holds.
const (
	RuleID   = "core-ibcf-v1"
	SpecID   = "s0_core_sleeve"
	SpecHash = "2408d09e618904c84219cacf6bc0f599e1a00ee3c4b04b0effbcc4521f45a441"
)

// The instrument core-ibcf-v1 is compiled for (owner decision 2026-10-14):
// the fund and its currency. Symbol is the owner's name for it; the
// listing, IBKR's symbol and the venue come from IBKR's contract record for
// the ISIN in that currency (owner decision 2026-10-18 08:06 CEST).
const (
	Symbol   = "IBCF"
	ISIN     = "IE00B3ZW0K18"
	Currency = "EUR"
)

// The rule's parameters: compiled with the rule version, never policy keys.
const (
	// WindowCloses is the fixed input window: the last 253 sessions to the
	// decision day, 252 daily returns.
	WindowCloses = 253
	// TrendMonths month-end closes, the decision's included, make SMA10.
	TrendMonths = 10
	// EWMASpan is the span of the exponentially weighted volatility.
	EWMASpan = 63
	// VolFloor is the lowest volatility the sleeve is sized at: s0's 15%
	// target with no leverage, restated.
	VolFloor = 0.15
	// AnnualDays annualises a daily volatility.
	AnnualDays = 252
)

// Data guards: a decision outside them holds for review.
const (
	// JumpPct is the daily move beyond which IBCF's own close must be
	// corroborated by SPY's two-day move of at least half the size.
	JumpPct = 8.0
	// SigmaMin and SigmaMax bound a believable volatility.
	SigmaMin = 0.05
	SigmaMax = 1.0
	// HeadroomFactor is the share of the mandate budget, and of each gap
	// cap, a core buy may fill: the forced reduction's own target, so a
	// core buy can never set up a forced sale by itself.
	HeadroomFactor = 0.95
)

// Signal statuses.
const (
	StatusOK      = "ok"
	StatusHold    = "hold"
	StatusNotLive = "not_live"
)

// Volatility bindings: which estimate set sigma.
const (
	BindingEWMA  = "ewma"
	Binding252   = "252"
	BindingFloor = "floor"
)

// Signal is one month's measurement. Everything but Status, Reason and
// Decision is zero unless Status is ok.
type Signal struct {
	Status   string `json:"status"`
	Reason   string `json:"reason,omitempty"`
	Decision string `json:"decision"`
	// WindowFrom is the first session of the fixed window.
	WindowFrom string `json:"window_from,omitempty"`
	// MonthEnds and MonthEndCloses are the ten month-ends SMA10 averages.
	MonthEnds      []string  `json:"month_ends,omitempty"`
	MonthEndCloses []float64 `json:"month_end_closes,omitempty"`
	Close          float64   `json:"close,omitempty"`
	SMA10          float64   `json:"sma10,omitempty"`
	TrendOn        bool      `json:"trend_on"`
	// SigmaEWMA and Sigma252 are annualised; Sigma is the larger of them
	// and VolFloor, and Binding names which one.
	SigmaEWMA float64 `json:"sigma_ewma,omitempty"`
	Sigma252  float64 `json:"sigma_252,omitempty"`
	Sigma     float64 `json:"sigma,omitempty"`
	Binding   string  `json:"binding,omitempty"`
	// Digest names the rule and the window's closes: a frozen month whose
	// inputs later read differently holds.
	Digest string `json:"digest,omitempty"`
}

// Window is the fixed input window behind an ok Signal, for the data guards.
type Window struct {
	Dates  []string
	Closes []float64
}

// MonthEnds returns the last session of each month among ascending dates.
func MonthEnds(sessions []string) []string {
	var out []string
	for i, d := range sessions {
		if i+1 == len(sessions) || month(sessions[i+1]) != month(d) {
			out = append(out, d)
		}
	}
	return out
}

func month(date string) string {
	if len(date) < 7 {
		return date
	}
	return date[:7]
}

// Measure computes the signal at one decision day from the exchange
// calendar's sessions and the instrument's closes (both ascending ISO
// dates). The decision must be the last session of its month; the window
// is the last WindowCloses calendar sessions to it, and a session without
// a close holds the month: a missing bar is never filled.
func Measure(calendar, dates []string, closes []float64, decision string) (Signal, Window) {
	sig := Signal{Status: StatusHold, Decision: decision}
	k := -1
	for i, d := range calendar {
		if d == decision {
			k = i
			break
		}
	}
	switch {
	case k < 0:
		sig.Reason = decision + " is not a session"
		return sig, Window{}
	case k+1 < len(calendar) && month(calendar[k+1]) == month(decision):
		sig.Reason = decision + " is not the last session of its month"
		return sig, Window{}
	case k+1 < WindowCloses:
		sig.Status, sig.Reason = StatusNotLive, fmt.Sprintf("needs %d sessions to %s, has %d", WindowCloses, decision, k+1)
		return sig, Window{}
	}
	window := calendar[k+1-WindowCloses : k+1]
	byDate := make(map[string]float64, len(dates))
	for i, d := range dates {
		if i < len(closes) {
			byDate[d] = closes[i]
		}
	}
	w := Window{Dates: window, Closes: make([]float64, len(window))}
	for i, d := range window {
		c, ok := byDate[d]
		if !ok {
			sig.Reason = "no close on " + d
			return sig, Window{}
		}
		if !(c > 0) || math.IsInf(c, 0) {
			sig.Reason = "no usable close on " + d
			return sig, Window{}
		}
		w.Closes[i] = c
	}
	ends := MonthEnds(window)
	if len(ends) < TrendMonths || ends[len(ends)-1] != decision {
		sig.Status, sig.Reason = StatusNotLive, fmt.Sprintf("needs %d month-ends", TrendMonths)
		return sig, Window{}
	}
	ends = ends[len(ends)-TrendMonths:]
	sum := 0.0
	sig.MonthEndCloses = make([]float64, len(ends))
	for i, d := range ends {
		sig.MonthEndCloses[i] = byDate[d]
		sum += byDate[d]
	}
	returns := make([]float64, len(w.Closes)-1)
	for i := 1; i < len(w.Closes); i++ {
		returns[i-1] = w.Closes[i]/w.Closes[i-1] - 1
	}
	sig.Status, sig.Reason = StatusOK, ""
	sig.WindowFrom, sig.MonthEnds = window[0], ends
	sig.Close = byDate[decision]
	sig.SMA10 = sum / float64(len(ends))
	sig.TrendOn = sig.Close > sig.SMA10
	sig.SigmaEWMA, sig.Sigma252 = SigmaEWMA(returns), SigmaSample(returns)
	sig.Sigma = math.Max(math.Max(sig.SigmaEWMA, sig.Sigma252), VolFloor)
	switch {
	case math.Max(sig.SigmaEWMA, sig.Sigma252) < VolFloor:
		sig.Binding = BindingFloor
	case sig.SigmaEWMA >= sig.Sigma252:
		sig.Binding = BindingEWMA
	default:
		sig.Binding = Binding252
	}
	sig.Digest = Digest(w)
	return sig, w
}

// Digest names the rule, the spec and the window's dates and closes.
func Digest(w Window) string {
	var b strings.Builder
	b.WriteString(RuleID + "|" + SpecHash)
	for i, d := range w.Dates {
		b.WriteString("|" + d + "=" + strconv.FormatFloat(w.Closes[i], 'g', -1, 64))
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:8])
}

// SigmaEWMA is the annualised exponentially weighted volatility of the
// returns: weights (1 − α)^(n−1−i) with α = 2 / (EWMASpan + 1), the
// variance around the weighted mean with the bias correction
// (Σw)² / ((Σw)² − Σw²), as pandas ewm(span=63, adjust=True).var() computes
// it. Zero with fewer than EWMASpan returns.
func SigmaEWMA(r []float64) float64 {
	n := len(r)
	if n < EWMASpan {
		return 0
	}
	alpha := 2.0 / float64(EWMASpan+1)
	var sw, sw2, swx float64
	w := make([]float64, n)
	for i := range r {
		w[i] = math.Pow(1-alpha, float64(n-1-i))
		sw += w[i]
		sw2 += w[i] * w[i]
		swx += w[i] * r[i]
	}
	mean := swx / sw
	var ss float64
	for i := range r {
		d := r[i] - mean
		ss += w[i] * d * d
	}
	biased := ss / sw
	return math.Sqrt(biased * sw * sw / (sw*sw - sw2) * AnnualDays)
}

// SigmaSample is the annualised sample standard deviation (n − 1).
func SigmaSample(r []float64) float64 {
	n := len(r)
	if n < 2 {
		return 0
	}
	mean := 0.0
	for _, v := range r {
		mean += v
	}
	mean /= float64(n)
	ss := 0.0
	for _, v := range r {
		ss += (v - mean) * (v - mean)
	}
	return math.Sqrt(ss / float64(n-1) * AnnualDays)
}

// Sizing is what the target is sized on: the owner's numbers and the
// mandate's capital at the decision.
type Sizing struct {
	CoreVolPct  float64 `json:"core_vol_pct"`
	ScalePct    float64 `json:"scale_pct"`
	MaxValuePct float64 `json:"max_value_pct"`
	BudgetBase  float64 `json:"budget_base"`
	Room        float64 `json:"room"`
}

// Size is the target in money and shares.
type Size struct {
	// SleeveBudget is S = core_vol_pct / 100 × budget base × room, the
	// sleeve's volatility share in base currency a year.
	SleeveBudget float64 `json:"sleeve_budget"`
	// Cap is max_value_pct / 100 × budget base × room.
	Cap float64 `json:"cap"`
	// Notional is S / sigma while the trend is on, else 0, at most Cap.
	Notional float64 `json:"notional"`
	// TargetShares is ⌊Notional × scale_pct / 100 / close⌋.
	TargetShares int `json:"target_shares"`
}

// SizeTarget sizes an ok signal. The order of the arithmetic is the lab's,
// so the fixture matches to the last bit where it can.
func SizeTarget(sig Signal, s Sizing) Size {
	out := Size{SleeveBudget: s.CoreVolPct / 100 * s.BudgetBase * s.Room, Cap: s.MaxValuePct / 100 * s.BudgetBase * s.Room}
	if sig.Status != StatusOK || !sig.TrendOn || !(sig.Sigma > 0) || !(sig.Close > 0) {
		return out
	}
	out.Notional = math.Min(out.SleeveBudget/sig.Sigma, out.Cap)
	shares := math.Floor(out.Notional * s.ScalePct / 100 / sig.Close)
	if shares > 0 && !math.IsInf(shares, 0) {
		out.TargetShares = int(shares)
	}
	return out
}

// Series is one daily close series for the guards (the index proxy).
type Series struct {
	Dates  []string
	Closes []float64
}

// Guard returns the data guards' reasons to hold an ok signal: a volatility
// outside SigmaMin..SigmaMax, or a daily move beyond JumpPct that SPY's move
// over the two US sessions spanning it does not show at half the size and
// the same sign. IBCF closes at 17:30 CET, before the US close, so its day
// runs from the previous session's 17:30 to its own: the US close before
// the previous session to the first US close on or after its own day
// spans it (review S5).
func Guard(sig Signal, w Window, index Series) []string {
	if sig.Status != StatusOK {
		return nil
	}
	var reasons []string
	for _, v := range []struct {
		name string
		val  float64
	}{{"the EWMA volatility", sig.SigmaEWMA}, {"the 252-day volatility", sig.Sigma252}} {
		if v.val < SigmaMin || v.val > SigmaMax {
			reasons = append(reasons, fmt.Sprintf("%s %.1f%% is outside %.0f%% to %.0f%%", v.name, v.val*100, SigmaMin*100, SigmaMax*100))
		}
	}
	for i := 1; i < len(w.Closes); i++ {
		r := w.Closes[i]/w.Closes[i-1] - 1
		if math.Abs(r)*100 <= JumpPct {
			continue
		}
		prev, day := w.Dates[i-1], w.Dates[i]
		from, to := -1, -1
		for j, d := range index.Dates {
			if d < prev {
				from = j
			}
			if d >= day && to < 0 {
				to = j
			}
		}
		if from < 0 || to < 0 || !(index.Closes[from] > 0) {
			reasons = append(reasons, fmt.Sprintf("a %+.1f%% close on %s waits for SPY's close to corroborate it", r*100, day))
			continue
		}
		ir := index.Closes[to]/index.Closes[from] - 1
		if ir*r <= 0 || math.Abs(ir) < math.Abs(r)/2 {
			reasons = append(reasons, fmt.Sprintf("a %+.1f%% close on %s is not in SPY's %+.1f%% over the same two days: a probable price error", r*100, day, ir*100))
		}
	}
	return reasons
}

// Headroom is the largest q in [0, want] for which fits(q) holds, found by
// bisection; 0 when even the smallest step does not fit. The result is
// always one fits accepted (or 0), so a non-monotone book can only make
// it smaller, never larger than what fits.
func Headroom(want int, fits func(q int) bool) int {
	if want <= 0 {
		return 0
	}
	if fits(want) {
		return want
	}
	lo, hi := 0, want // fits(lo) is assumed (no addition), fits(hi) is false
	for hi-lo > 1 {
		mid := lo + (hi-lo)/2
		if fits(mid) {
			lo = mid
		} else {
			hi = mid
		}
	}
	return lo
}

// Action kinds: what the rule would do at the next window. Phase 1 serves
// it read-only; nothing is proposed or sent.
const (
	ActionNone    = "none"
	ActionBuy     = "buy"
	ActionSell    = "sell"
	ActionSellAll = "sell_all"
	ActionWait    = "wait"
	ActionHold    = "hold"
)

// DecideInput is everything Decide reads.
type DecideInput struct {
	// Target is the shares the rule wants (the frozen target, lowered to
	// the current capital's); TrendOn the decision's trend.
	Target  int
	TrendOn bool
	// Held is the confirmed holding of the pinned contract; Price the
	// current price (the held line's mark, else the last close).
	Held  int
	Price float64
	// BandPct and MinOrderBase are the owner's no-trade band and minimum.
	BandPct      float64
	MinOrderBase float64
	// Holdable is the largest holding the mandate's headroom admits
	// (Held + Headroom); BuyBlocked names why no buy may be sized at all.
	Holdable   int
	BuyBlocked string
}

// Decision is what the rule would do and why.
type Decision struct {
	Kind     string `json:"kind"`
	Quantity int    `json:"quantity,omitempty"`
	Why      string `json:"why"`
	// Diff is target − held; BandShares the band in shares.
	Diff       int     `json:"diff"`
	BandShares float64 `json:"band_shares"`
	InsideBand bool    `json:"inside_band,omitempty"`
	// OrderValue is the quantity at the price, in base currency.
	OrderValue   float64 `json:"order_value,omitempty"`
	BelowMinimum bool    `json:"below_minimum,omitempty"`
	// HeadroomCut says a buy was cut to what the mandate's headroom admits.
	HeadroomCut bool `json:"headroom_cut,omitempty"`
}

// Decide applies the band, the minimum and the headroom: a sale to zero
// when the trend is off (the band and the minimum never stop it), a first
// buy past the band, otherwise a move only when the holding is off the
// target by more than the band; a buy is cut to the headroom and waits
// below the minimum. The core never sells more than it holds.
func Decide(in DecideInput) Decision {
	held := max(in.Held, 0)
	d := Decision{Diff: in.Target - held, BandShares: in.BandPct / 100 * float64(in.Target)}
	value := func(q int) float64 { return float64(q) * in.Price }
	switch {
	case !in.TrendOn && held > 0:
		d.Kind, d.Quantity, d.OrderValue = ActionSellAll, held, value(held)
		d.Why = "the trend is off: sell every share; the band and the minimum do not apply"
		return d
	case d.Diff == 0:
		d.Kind, d.Why = ActionNone, "the holding equals the target"
		if in.Target == 0 {
			d.Why = "the target is zero and nothing is held"
		}
		return d
	}
	first := held == 0 && in.Target > 0
	if !first && math.Abs(float64(d.Diff)) <= d.BandShares {
		d.Kind, d.InsideBand = ActionNone, true
		d.Why = fmt.Sprintf("inside the band: off the target by %d shares, the band is %.0f", abs(d.Diff), d.BandShares)
		return d
	}
	if d.Diff < 0 {
		q := min(-d.Diff, held)
		d.Quantity, d.OrderValue = q, value(q)
		if d.OrderValue < in.MinOrderBase {
			d.Kind, d.BelowMinimum = ActionWait, true
			d.Why = fmt.Sprintf("a sale of %d shares is below the minimum order", q)
			return d
		}
		d.Kind, d.Why = ActionSell, fmt.Sprintf("the holding is %d shares over the target, beyond the band", q)
		return d
	}
	if in.BuyBlocked != "" {
		d.Kind, d.Why = ActionWait, "no buy: "+in.BuyBlocked
		return d
	}
	q := min(d.Diff, in.Holdable-held)
	if q <= 0 {
		d.Kind, d.HeadroomCut = ActionWait, true
		d.Why = fmt.Sprintf("no headroom: more %s would take the book past %.0f%% of the budget or a gap cap, or the core past its seat", Symbol, HeadroomFactor*100)
		return d
	}
	d.Quantity, d.OrderValue, d.HeadroomCut = q, value(q), q < d.Diff
	if d.OrderValue < in.MinOrderBase {
		d.Kind, d.BelowMinimum = ActionWait, true
		d.Why = fmt.Sprintf("a buy of %d shares is below the minimum order", q)
		return d
	}
	d.Kind = ActionBuy
	switch {
	case first && d.HeadroomCut:
		d.Why = fmt.Sprintf("first buy, cut by the mandate's headroom from %d shares", d.Diff)
	case first:
		d.Why = "first buy toward the target"
	case d.HeadroomCut:
		d.Why = fmt.Sprintf("the holding is %d shares under the target; cut by the mandate's headroom", d.Diff)
	default:
		d.Why = fmt.Sprintf("the holding is %d shares under the target, beyond the band", d.Diff)
	}
	return d
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
