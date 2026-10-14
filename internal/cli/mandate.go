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
	if len(v.Reasons) > 0 {
		fmt.Fprintln(env.Stdout, "\n"+env.bold("Why unknown"))
		for _, reason := range v.Reasons {
			riskDisplayLine(env, "  ", reason, nil)
		}
	}
	if len(v.Underlyings) > 0 {
		fmt.Fprintln(env.Stdout, "\n"+env.bold("Contributions")+"  share of the risk · beta to "+mandate.IndexSymbol+" · gap loss against the cap")
		for _, u := range v.Underlyings {
			gap := fmt.Sprintf("gap −%s %s", mandatePct(v.Stresses.GapShockPct), mandateAmount(u.GapDownLossBase, ccy))
			if u.GapUpLossBase != nil {
				gap += fmt.Sprintf(", +%s %s", mandatePct(v.Stresses.GapShockPct), mandateAmount(*u.GapUpLossBase, ccy))
			}
			line := briefJoin(u.Underlying, mandateShare(u.ContributionPct), fmt.Sprintf("beta %.2f", u.Beta), gap, "cap "+mandateAmount(u.GapCapBase, ccy))
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
		riskReadLine(env, "  Index −"+mandatePct(s.IndexShockPct)+" through beta", mandateAmount(s.IndexLossBase, ccy))
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
				riskReadLine(env, "  "+s.Key, env.yellow("no history yet"))
			case s.Stale:
				riskReadLine(env, "  "+s.Key, env.yellow(fmt.Sprintf("stale: %d closes to %s", s.Closes, s.LastClose)), "read "+formatTimeShort(s.FetchedAt.Local()))
			default:
				riskReadLine(env, "  "+s.Key, fmt.Sprintf("%d closes to %s", s.Closes, s.LastClose), "read "+formatTimeShort(s.FetchedAt.Local()))
			}
		}
	}
	riskReadLine(env, "", "Measurement and gate only: the mandate refuses risk-adding orders; it places none.")
}
