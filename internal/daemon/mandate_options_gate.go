package daemon

import (
	"fmt"
	"strings"
	"time"

	"github.com/osauer/canary/v2/internal/mandate"
	"github.com/osauer/canary/v2/internal/risk"
	"github.com/osauer/canary/v2/internal/rpc"
)

// Options at the additions gate (owner decision 2026-10-23 18:22 CEST):
//
//   - An option buy adds risk within the premium limits: the premium at risk
//     on its name may reach at most option_name_act_pct of NLV with the order,
//     and the book's at most the premium budget's act level of the regime set
//     in force. Either breach refuses the order with the figure, the limit
//     and the name (mandate_premium).
//   - The mandate's additions gate (since 2026-10-27, spec TM-04.12, the
//     loss measure against the loss room, the gap caps and the sleeve
//     allocations; before, the volatility budget) keeps gating stock and ETF
//     additions. For an option buy its judgement, with the 1-day expected
//     shortfall and the beta-adjusted exposure, is computed and shown, and
//     carried on the preview as a warning, never a block; the premium limits
//     count protection since 2026-10-27 (the owner: "Count it").
//   - The holds that bound a loss rather than a volatility stay for every
//     addition: at the maximum loss (no room) nothing adds, and the owner
//     sleeve's drawdown stop keeps the sleeve reduce-only while engaged.
//   - The owner's one-shot override of mandate.vol_target_pct (canary policy
//     override, human-only, journaled, expiring under
//     override.max_duration_hours) lifts the additions gate for the owner's
//     additions. What still bounds risk meanwhile: the drawdown brake and the
//     no-room hold at the 30% maximum loss, the owner sleeve's stop, every
//     order cap, the premium limits above, the device confirmation of every
//     order and the override's expiry (risk.MandateAdditionsOverrideControl).
//
// A short option opened to sell is no option addition here: its loss has no
// premium to bound it, so it stays on the volatility gate as before.

// mandateOptionAddition reports a BUY of an option: the addition the premium
// limits gate.
func mandateOptionAddition(draft rpc.OrderDraft) bool {
	return strings.EqualFold(strings.TrimSpace(draft.Action), rpc.OrderActionBuy) &&
		strings.EqualFold(strings.TrimSpace(draft.Contract.SecType), "OPT")
}

// mandateAdditionsOverride returns the active override of the volatility
// budget's additions gate for the selected account, the latest-expiring one
// when several are active, or nil.
func (s *Server) mandateAdditionsOverride(now time.Time) *rpc.MandateOverrideView {
	if s == nil || s.riskCapital == nil {
		return nil
	}
	var best *rpc.MandateOverrideView
	for _, o := range s.riskCapital.OverridesSnapshotForScope(s.currentBrokerStateScope()) {
		if !o.Active || o.Control != risk.MandateAdditionsOverrideControl || !now.Before(o.ExpiresAt) {
			continue
		}
		if best == nil || o.ExpiresAt.After(best.ExpiresAt) {
			best = &rpc.MandateOverrideView{ID: o.ID, Control: o.Control, Reason: o.Reason, GrantedAt: o.GrantedAt, ExpiresAt: o.ExpiresAt}
		}
	}
	return best
}

// mandateHardHolds are the holds every addition keeps whatever gates it: at
// the maximum loss no sleeve adds, and the owner sleeve's drawdown stop
// holds its sleeve while engaged. Nil when neither holds.
func mandateHardHolds(v *mandate.Verdict, sleeve, ccy, open string) *rpc.TradingBlocker {
	if v == nil {
		return nil
	}
	if v.Capital.Known && !(v.Capital.Room > 0) {
		return &rpc.TradingBlocker{Code: mandateBudgetCode, Message: "Mandate budget: " + mandateStatusSentence(v, ccy) + "; at the maximum loss no sleeve adds." + open,
			Action: "Reduce or close instead; the book goes to its hedges at the maximum loss."}
	}
	if why := mandateOwnerStopHolds(v, sleeve, ccy); why != "" {
		return &rpc.TradingBlocker{Code: mandateBudgetCode, Message: "Mandate budget: " + why + "; this order adds risk." + open,
			Action: fmt.Sprintf("Reduce or close instead; the stop releases when the sleeve's drawdown is back under %g%%. `canary mandate` has the sleeve's drawdown and high-water mark.", v.OwnerStop.ReleasePct)}
	}
	return nil
}

// mandatePremiumGate judges an option buy by the premium limits on the
// exposure the verdict was measured with: the order's premium at its limit
// (contracts × multiplier × limit, in base currency) added to the name's and
// the book's premium at risk. Fails closed: no limit, no exchange rate, no
// NLV or an unmeasured premium refuses the order, never passes it.
func (s *Server) mandatePremiumGate(draft rpc.OrderDraft, result *rpc.MandateRiskResult, input *mandate.Input, ccy, open string) *rpc.TradingBlocker {
	refuse := func(msg, act string) *rpc.TradingBlocker {
		return &rpc.TradingBlocker{Code: rpc.MandatePremiumBlockerCode, Message: "Mandate premium: " + msg + open, Action: act}
	}
	const measure = "`canary mandate` has each name's premium at risk against its cap."
	sym := normSym(draft.Contract.Symbol)
	exposure := result.Exposure
	if exposure == nil {
		return refuse("this option buy cannot be measured: no current positions read carries the book's premium at risk.", "Retry once the positions read is current; "+measure)
	}
	if exposure.NLVBase == nil || !(*exposure.NLVBase > 0) {
		return refuse("this option buy cannot be measured: "+nonEmptyString(exposure.NLVReason, "net liquidation value is unknown")+".", "Retry once the account has been read; "+measure)
	}
	if !(draft.LimitPrice > 0) {
		return refuse("this option buy cannot be measured: without a limit there is no premium to bound its loss.", "Buy at a limit; "+measure)
	}
	fx := 1.0
	if c := normCcy(draft.Contract.Currency); c != "" && c != normCcy(ccy) {
		rate, ok := input.FXNow[c]
		if !ok || !(rate > 0) {
			return refuse("this option buy cannot be measured: no "+ccy+"/"+c+" exchange rate for its premium.", "Retry once the rate has been read; "+measure)
		}
		fx = rate
	}
	mult := float64(draft.Contract.Multiplier)
	if mult <= 0 {
		mult = 100
	}
	premium := float64(draft.Quantity) * mult * draft.LimitPrice * fx
	nlv := *exposure.NLVBase
	nameNow := 0.0
	for _, row := range exposure.Names {
		if row.Underlying != sym {
			continue
		}
		if row.PremiumAtRiskBase == nil {
			return refuse("this option buy cannot be measured: the premium at risk in "+sym+" is unknown ("+strings.Join(row.Unknown, "; ")+").", "Retry once every "+sym+" leg carries a value or a price paid; "+measure)
		}
		nameNow = *row.PremiumAtRiskBase
	}
	nameCap := exposure.NamePremiumCapPct / 100 * nlv
	if after := nameNow + premium; after > nameCap+mandateReductionEpsilon {
		return refuse(fmt.Sprintf("with this order the premium at risk in %s would be %s, %s of net liquidation %s, over the %s limit for one name (now %s).",
			sym, mandateMoney(after, ccy), mandatePremiumPct(after/nlv*100), mandateMoney(nlv, ccy), mandatePremiumPct(exposure.NamePremiumCapPct), mandatePremiumPct(nameNow/nlv*100)),
			fmt.Sprintf("Size it smaller: %s has room for %s more premium under the limit. %s", sym, mandateMoney(max(0, nameCap-nameNow), ccy), measure))
	}
	if exposure.Book.PremiumAtRiskBase == nil {
		return refuse("this option buy cannot be measured: the book's premium at risk is unknown ("+strings.Join(exposure.Book.Unknown, "; ")+").", "Retry once every long option carries a value or a price paid; "+measure)
	}
	bookNow := *exposure.Book.PremiumAtRiskBase
	bookCap := exposure.BookPremiumCapPct / 100 * nlv
	if after := bookNow + premium; after > bookCap+mandateReductionEpsilon {
		set := ""
		if exposure.BookPremiumSet != "" {
			set = " of the " + exposure.BookPremiumSet + " set"
		}
		return refuse(fmt.Sprintf("with this order the book's premium at risk would be %s, %s of net liquidation %s, over the %s premium budget%s (now %s).",
			mandateMoney(after, ccy), mandatePremiumPct(after/nlv*100), mandateMoney(nlv, ccy), mandatePremiumPct(exposure.BookPremiumCapPct), set, mandatePremiumPct(bookNow/nlv*100)),
			fmt.Sprintf("Size it smaller: the book has room for %s more premium under the budget, or sell premium first. %s", mandateMoney(max(0, bookCap-bookNow), ccy), measure))
	}
	return nil
}

// mandateOptionsGateView reads the premium limits for an option buy now:
// refused while the book's premium at risk is at or over its cap or cannot
// be measured, else open, with the volatility gate's refusal as a warning.
func mandateOptionsGateView(res *rpc.MandateRiskResult, vol *rpc.MandateGateView) *rpc.MandateOptionsGateView {
	view := &rpc.MandateOptionsGateView{}
	if vol != nil && vol.RefusesAdditions {
		view.Warning = vol.Reason
	}
	e := res.Exposure
	switch {
	case e == nil:
		view.RefusesAdditions, view.Reason = true, "no current positions read carries the book's premium at risk"
	case e.NLVBase == nil:
		view.RefusesAdditions, view.Reason = true, nonEmptyString(e.NLVReason, "net liquidation value is unknown")
	case e.Book.PremiumAtRiskBase == nil:
		view.RefusesAdditions, view.Reason = true, "the book's premium at risk is unknown: "+strings.Join(e.Book.Unknown, "; ")
	case e.Book.PremiumOver:
		view.RefusesAdditions = true
		view.Reason = fmt.Sprintf("the book's premium at risk %s is %s of net liquidation, at the %s premium budget", mandateMoney(*e.Book.PremiumAtRiskBase, res.Verdict.BaseCurrency), mandatePremiumPct(policyCheckDeref(e.Book.PremiumPctNLV)), mandatePremiumPct(e.BookPremiumCapPct))
	}
	return view
}

// mandateOptionWarning carries the volatility gate's refusal on an option
// buy that the premium limits pass.
func mandateOptionWarning(vol *rpc.TradingBlocker) *rpc.DataWarning {
	return &rpc.DataWarning{Code: mandateBudgetCode, Scope: "risk_policy", Severity: "warning",
		Message: strings.TrimSuffix(vol.Message, " Reductions, closes, cancels and hedges stay open.") + " This option buy is judged by the premium limits instead; the volatility budget is its warning.",
		Action:  vol.Action}
}

// mandateOverrideSentence names an active override on the gate view.
func mandateOverrideSentence(o *rpc.MandateOverrideView, v *mandate.Verdict, ccy string) string {
	return fmt.Sprintf("your override of %s lifts the volatility budget until %s (%q); the verdict reads: %s", o.Control, o.ExpiresAt.UTC().Format("2006-01-02 15:04 UTC"), o.Reason, mandateStatusSentence(v, ccy))
}

// mandateOverrideWarning carries an active override on a preview the
// volatility gate would otherwise have refused or judged.
func mandateOverrideWarning(o *rpc.MandateOverrideView, v *mandate.Verdict, ccy string) *rpc.DataWarning {
	return &rpc.DataWarning{Code: mandateBudgetCode, Scope: "risk_policy", Severity: "warning",
		Message: "Mandate budget: " + mandateOverrideSentence(o, v, ccy) + ". The maximum loss, the owner sleeve's stop, the order caps, the premium limits and the device confirmation still bind.",
		Action:  "Let the override expire, or revoke it by a policy revision; `canary mandate` has the measurement."}
}
