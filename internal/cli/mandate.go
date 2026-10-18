package cli

import (
	"context"
	"fmt"
	"math"
	"strings"

	"github.com/osauer/canary/v2/internal/mandate"
	"github.com/osauer/canary/v2/internal/rpc"
)

// runMandate reads the daemon's mandate risk verdict
// (internal-docs/design/mandate-risk-budget.md): the book's ex-ante
// volatility against the owner's budget, what drives it, the named stresses
// and the evidence clocks. The daemon evaluates; this renders.
func runMandate(ctx context.Context, env *Env, args []string) int {
	fs := flagSet(env, "mandate")
	jsonOut := fs.Bool("json", false, "emit the typed mandate verdict")
	refresh := fs.Bool("refresh", false, "read the account and positions again before judging")
	if err := fs.Parse(args); err != nil {
		return parseExit(err)
	}
	if fs.NArg() != 0 {
		return failUnexpectedArgs(env, fs)
	}
	res, err := rpc.FetchMandateRisk(ctx, env.Conn, *refresh)
	if err != nil {
		return fail(env, "mandate: %v", err)
	}
	if *jsonOut {
		return printJSON(env, res)
	}
	renderMandate(env, res)
	return 0
}

// mandateAmount prints a base-currency amount in whole units.
func mandateAmount(v float64, ccy string) string {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return "unmeasurable"
	}
	sign := ""
	if v < 0 {
		sign = "−"
	}
	return sign + groupThousands(fmt.Sprintf("%.0f", math.Abs(v))) + " " + ccy
}

func mandatePct(v float64) string {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return "∞"
	}
	return strings.Replace(fmt.Sprintf("%.0f%%", v), "-", "−", 1)
}

// mandateStressPnL states a stress figure, a loss when positive, as P&L in
// words: "loses 12,000 EUR" or "gains 73,847 EUR", never a signed loss a
// reader takes for the opposite.
func mandateStressPnL(loss float64, ccy string) string {
	if loss < 0 {
		return "gains " + mandateAmount(-loss, ccy)
	}
	return "loses " + mandateAmount(loss, ccy)
}

// mandateCap prints a gap cap as a bare grouped figure for a parenthesis.
func mandateCap(v float64) string {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return "unmeasurable"
	}
	return groupThousands(fmt.Sprintf("%.0f", math.Abs(v)))
}

// mandateShare prints a contribution share with one decimal.
func mandateShare(v float64) string {
	return strings.Replace(fmt.Sprintf("%.1f%%", v), "-", "−", 1)
}

func renderMandate(env *Env, res rpc.MandateRiskResult) {
	if !res.Active || res.Verdict == nil {
		riskDisplayLine(env, env.bold("Mandate")+"  ", "INACTIVE", env.dim)
		riskDisplayLine(env, "", nonEmpty(res.Message, "the risk constitution carries no [mandate] table"), nil)
		riskReadLine(env, "Next", "write [mandate] into risk-policy.toml with a higher policy_version; canary policy default constitution shows the keys")
		return
	}
	v := res.Verdict
	ccy := nonEmpty(v.BaseCurrency, "")
	headline, tone := "UNKNOWN", "yellow"
	switch v.Status {
	case mandate.StatusWithin:
		headline, tone = "WITHIN BUDGET", "green"
	case mandate.StatusOver:
		headline, tone = "OVER BUDGET", "yellow"
	case mandate.StatusReduce:
		headline, tone = "REDUCE", "red"
	}
	riskDisplayLine(env, env.bold("Mandate")+"  ", headline, func(s string) string { return riskTone(env, tone, env.bold(s)) })
	if v.Risk != nil {
		share := "budget 0"
		if v.Capital.BudgetAnnualSD > 0 {
			share = mandatePct(v.Risk.RatioToBudget*100) + " of the budget " + mandateAmount(v.Capital.BudgetAnnualSD, ccy)
		}
		riskReadLine(env, "Risk", mandateAmount(v.Risk.SDAnnualBase, ccy)+" a year", "1-day "+mandateAmount(v.Risk.SD1DBase, ccy), "99% expected shortfall "+mandateAmount(v.Risk.ES99Base, ccy), share)
	}
	if c := v.Capital; c.Known && res.Policy != nil {
		riskReadLine(env, "Budget", fmt.Sprintf("%s × %s at room %s", mandatePct(res.Policy.VolTargetPct), mandateAmount(c.BudgetBase, ccy), mandatePct(c.Room*100)),
			fmt.Sprintf("loss %s of the %s maximum from the high-water mark %s", mandatePct(c.DrawdownPct), mandatePct(res.Policy.MaxLossPct), mandateAmount(c.HWMRiskBase, ccy)))
		flows := ""
		if c.FlowsSinceStart != 0 {
			flows = "flows since " + c.StartedAt + " " + mandateAmount(c.FlowsSinceStart, ccy)
		}
		riskReadLine(env, "Risk base", mandateAmount(c.RiskBase, ccy)+" = equity "+mandateAmount(c.EquityBase, ccy)+" − tax reserve "+mandateAmount(c.TaxReserveBase, ccy), flows)
	}
	if res.Gate != nil {
		if res.Gate.RefusesAdditions {
			riskDisplayLine(env, "Additions · ", briefJoin("refused", res.Gate.Reason), env.red)
		} else {
			riskDisplayLine(env, "Additions · ", "open · reductions, closes, cancels and hedges always are", env.green)
		}
	}
	renderMandateReduction(env, res.Reduction, ccy)
	renderMandateAllocations(env, v, ccy)
	if len(v.Reasons) > 0 {
		fmt.Fprintln(env.Stdout, "\n"+env.bold("Why unknown"))
		for _, reason := range v.Reasons {
			riskDisplayLine(env, "  ", reason, nil)
		}
	}
	if len(v.Underlyings) > 0 {
		fmt.Fprintln(env.Stdout, "\n"+env.bold("Contributions")+"  share of the risk · beta to "+mandate.IndexSymbol+" · one-name gap against its cap")
		for _, u := range v.Underlyings {
			shock := u.GapShockPct
			if shock <= 0 {
				shock = v.Stresses.GapShockPct
			}
			gap := fmt.Sprintf("gap −%s %s", mandatePct(shock), mandateStressPnL(u.GapDownLossBase, ccy))
			if u.GapUpLossBase != nil {
				gap += fmt.Sprintf(", +%s %s", mandatePct(shock), mandateStressPnL(*u.GapUpLossBase, ccy))
			}
			gap += " (cap " + mandateCap(u.GapCapBase) + ")"
			line := briefJoin(u.Underlying, mandateShare(u.ContributionPct), fmt.Sprintf("beta %.2f", u.Beta), gap)
			style := func(s string) string { return s }
			if u.GapBreach {
				line += " · BREACH"
				style = env.red
			}
			riskDisplayLine(env, "  ", line, style)
		}
	}
	if len(v.Legs) > 1 {
		fmt.Fprintln(env.Stdout, "\n"+env.bold("Positions"))
		for _, l := range v.Legs {
			if l.Unknown != "" {
				riskDisplayLine(env, "  ", fmt.Sprintf("%s: %s", l.Symbol, l.Unknown), env.yellow)
				continue
			}
			riskDisplayLine(env, "  ", briefJoin(l.Symbol, mandateShare(l.ContributionPct), mandateAmount(l.ValueBase, ccy)), nil)
		}
	}
	if s := v.Stresses; s != nil {
		fmt.Fprintln(env.Stdout, "\n"+env.bold("Stresses"))
		riskReadLine(env, "  Index −"+mandatePct(s.IndexShockPct)+" through beta", mandateStressPnL(s.IndexLossBase, ccy))
		breaches := "none"
		if len(s.GapBreaches) > 0 {
			breaches = strings.Join(s.GapBreaches, ", ")
		}
		riskReadLine(env, "  Gap cap per name", mandateAmount(s.GapCapBase, ccy), "breaches: "+breaches)
		if v.Risk != nil {
			riskReadLine(env, "  Currency share of the risk", mandatePct(v.Risk.FXContributionPct))
		}
	}
	if len(v.NotModelled) > 0 {
		riskReadLine(env, "Not modelled", strings.Join(v.NotModelled, "; "))
	}
	fmt.Fprintln(env.Stdout, "\n"+env.bold("Evidence"))
	if e := res.Evidence; e != nil {
		riskReadLine(env, "  Verdict", formatTimeShort(e.EvaluatedAt.Local()))
		if !e.EquityAsOf.IsZero() {
			riskReadLine(env, "  Equity", formatTimeShort(e.EquityAsOf.Local()))
		}
		if !e.PositionsAsOf.IsZero() {
			riskReadLine(env, "  Positions", formatTimeShort(e.PositionsAsOf.Local()))
		}
		if v.Scenarios > 0 {
			riskReadLine(env, "  Scenarios", fmt.Sprintf("%d daily returns, %s to %s", v.Scenarios, v.ScenariosFrom, v.ScenariosTo))
		}
		for _, s := range e.Series {
			switch {
			case s.Missing:
				riskWarnReadLine(env, "  "+s.Key, "no history yet")
			case s.Stale:
				riskWarnReadLine(env, "  "+s.Key, fmt.Sprintf("stale: %d closes to %s", s.Closes, s.LastClose), "read "+formatTimeShort(s.FetchedAt.Local()))
			default:
				riskReadLine(env, "  "+s.Key, fmt.Sprintf("%d closes to %s", s.Closes, s.LastClose), "read "+formatTimeShort(s.FetchedAt.Local()))
			}
		}
	}
	riskReadLine(env, "", "The mandate refuses risk-adding orders. Its reduction is a set of proposals (canary proposals); the daemon places them itself only when mandate_reduction is pre-authorised, after the veto window.")
}

// renderMandateReduction prints the forced reduction under the gate: its
// state, who places the rows and when, and the rows in dispatch order.
func renderMandateReduction(env *Env, r *rpc.MandateReductionView, ccy string) {
	if r == nil {
		return
	}
	state := strings.ReplaceAll(r.State, "_", " ")
	var style func(string) string
	var head string
	switch {
	case r.Held && r.Rows > 0:
		head, style = fmt.Sprintf("%d %s not sent: %s", r.Rows, pluralWord(r.Rows, "row", "rows"), r.HoldReason), env.yellow
	case r.Rows > 0 && r.PreAuthorised:
		head, style = fmt.Sprintf("%d %s, pre-authorised", r.Rows, pluralWord(r.Rows, "row", "rows")), env.yellow
		if r.Pending > 0 && !r.VetoUntil.IsZero() {
			head += fmt.Sprintf(": the daemon places %d after the veto window, the last at %s", r.Pending, formatTimeShort(r.VetoUntil.Local()))
		}
	case r.Rows > 0:
		head, style = fmt.Sprintf("%d %s wait for your approval; mandate_reduction is not pre-authorised", r.Rows, pluralWord(r.Rows, "row", "rows")), env.yellow
	default:
		head = r.Reason
	}
	riskDisplayLine(env, "Reduction · ", briefJoin(state, head), style)
	for _, o := range r.Orders {
		verb := strings.ToLower(o.Action)
		parts := []string{fmt.Sprintf("%d. %s %d %s", o.Rank, verb, o.Quantity, queuedContractLabel(o.Contract)), mandatePhaseWords(o.Phase), mandateShare(o.ContributionPct) + " of the risk"}
		switch {
		case o.Held:
			parts = append(parts, "held")
		case o.Blocked != "":
			parts = append(parts, "blocked: "+o.Blocked)
		case !o.SubmitAt.IsZero():
			parts = append(parts, "places itself at "+formatTimeShort(o.SubmitAt.Local())+" unless vetoed")
		case o.Automatic != "":
			parts = append(parts, o.Automatic)
		case !r.PreAuthorised:
			parts = append(parts, "waits for your approval")
		}
		riskDisplayLine(env, "  ", briefJoin(parts...), nil)
	}
}

// mandatePhaseWords names why a reduction row is in the batch.
// renderMandateAllocations lists each sleeve's use against its allocation (allocations,
// 2026-10-18): yours, the rest of the budget, then the machine's, each its
// stage times its allocation while it sends orders.
func renderMandateAllocations(env *Env, v *mandate.Verdict, ccy string) {
	if len(v.Allocations) == 0 {
		return
	}
	fmt.Fprintln(env.Stdout, "\n"+env.bold("Risk allocations")+"  each sleeve's use against its allocation, a year · alone: its own risk")
	for _, s := range v.Allocations {
		name, detail := strings.ToUpper(s.Sleeve[:1])+s.Sleeve[1:], s.Note
		switch {
		case s.Sleeve == mandate.SleeveOwner:
			name, detail = "Owner", "the budget less the machine's allocations"
		case s.StagePct > 0:
			detail = fmt.Sprintf("stage %s of its %s allocation", mandatePct(s.StagePct), mandatePct(s.TargetPct))
		}
		line := briefJoin(name, mandateAmount(s.UseBase, ccy)+" of "+mandateAmount(s.AllocationBase, ccy), "alone "+mandateAmount(s.StandaloneBase, ccy))
		style := func(s string) string { return s }
		if s.Over {
			line += " · OVER by " + mandateAmount(s.ExcessBase, ccy)
			style = env.red
		}
		riskDisplayLine(env, "  ", briefJoin(line, detail), style)
	}
}

func mandatePhaseWords(phase string) string {
	switch phase {
	case rpc.MandatePhaseExpiry:
		return "expires in the money first"
	case rpc.MandatePhaseGap:
		return "gap over its cap"
	case rpc.MandatePhaseRisk:
		return "largest risk"
	}
	return phase
}
