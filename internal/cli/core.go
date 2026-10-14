package cli

import (
	"context"
	"fmt"
	"math"
	"strings"

	"github.com/osauer/canary/v2/internal/core"
	"github.com/osauer/canary/v2/internal/rpc"
)

// runCore reads the daemon's core sleeve target
// (internal-docs/design/core-sleeve.md): the month's decision under the
// compiled rule, the target, the holding, the band and the action the rule
// would take. The daemon evaluates; this renders. Read-only: the core
// proposes nothing and places no order in this phase.
func runCore(ctx context.Context, env *Env, args []string) int {
	if idx := firstPositionalIndex(args); idx >= 0 && args[idx] == "resolve" {
		return runCoreResolve(ctx, env, append(append([]string{}, args[:idx]...), args[idx+1:]...))
	}
	fs := flagSet(env, "core")
	jsonOut := fs.Bool("json", false, "emit the typed core target")
	refresh := fs.Bool("refresh", false, "measure the mandate again before deciding")
	if err := fs.Parse(args); err != nil {
		return parseExit(err)
	}
	if fs.NArg() != 0 {
		return failUnexpectedArgs(env, fs)
	}
	res, err := rpc.FetchCoreTarget(ctx, env.Conn, *refresh)
	if err != nil {
		return fail(env, "core: %v", err)
	}
	if *jsonOut {
		return printJSON(env, res)
	}
	renderCore(env, res)
	return 0
}

// corePrice prints a fund price with its cents.
func corePrice(v float64) string {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return "unmeasurable"
	}
	return fmt.Sprintf("%.2f", v)
}

func corePct(v float64) string {
	return fmt.Sprintf("%.1f%%", v*100)
}

func coreShares(n int) string {
	return groupThousands(fmt.Sprint(n)) + " " + pluralWord(n, "share", "shares")
}

func renderCore(env *Env, res rpc.CoreTargetResult) {
	if !res.Active {
		riskDisplayLine(env, env.bold("Core")+"  ", "INACTIVE", env.dim)
		riskDisplayLine(env, "", nonEmpty(res.Message, "the risk constitution carries no [mandate.core] table"), nil)
		riskReadLine(env, "Next", "write [mandate.core] into risk-policy.toml with a higher policy_version; canary policy default constitution shows the keys")
		return
	}
	ccy := core.Currency
	sym := core.Symbol
	if res.Instrument != nil {
		ccy, sym = nonEmpty(res.Instrument.Currency, ccy), nonEmpty(res.Instrument.Symbol, sym)
	}
	a := res.Action
	headline, tone := "HOLD", "yellow"
	if res.Status == rpc.CoreStatusReady && a != nil {
		switch a.Kind {
		case core.ActionBuy:
			headline, tone = fmt.Sprintf("WOULD BUY %s %s", groupThousands(fmt.Sprint(a.Quantity)), sym), "green"
		case core.ActionSell:
			headline, tone = fmt.Sprintf("WOULD SELL %s %s", groupThousands(fmt.Sprint(a.Quantity)), sym), "yellow"
		case core.ActionSellAll:
			headline, tone = fmt.Sprintf("WOULD SELL ALL %s %s", groupThousands(fmt.Sprint(a.Quantity)), sym), "yellow"
		case core.ActionWait:
			headline, tone = "WAITS", "yellow"
		default:
			headline, tone = "NO TRADE", "green"
		}
	}
	riskDisplayLine(env, env.bold("Core")+"  ", headline, func(s string) string { return riskTone(env, tone, env.bold(s)) })
	spec := res.SpecHash
	if len(spec) > 12 {
		spec = spec[:12]
	}
	instrument := ""
	if in := res.Instrument; in != nil {
		instrument = fmt.Sprintf("%s %s on %s", in.Symbol, in.ISIN, in.Exchange)
		if in.ConID > 0 {
			instrument += fmt.Sprintf(", contract %d", in.ConID)
		}
	}
	riskReadLine(env, "Rule", res.Rule, "spec "+res.SpecID+" "+spec, instrument)
	if a != nil {
		action := a.Why
		if a.Quantity > 0 && a.Kind != core.ActionHold {
			action = briefJoin(strings.ReplaceAll(a.Kind, "_", " ")+" "+coreShares(a.Quantity), a.Why)
		}
		riskReadLine(env, "Action", action)
	}
	if len(res.Reasons) > 0 {
		fmt.Fprintln(env.Stdout, "\n"+env.bold("Why it holds"))
		for _, r := range res.Reasons {
			riskDisplayLine(env, "  ", r, nil)
		}
	}
	if d := res.Decision; d != nil && d.Status == core.StatusOK {
		trend := fmt.Sprintf("close %s above its 10-month average %s: trend on", corePrice(d.Close), corePrice(d.SMA10))
		if !d.TrendOn {
			trend = fmt.Sprintf("close %s not above its 10-month average %s: trend off", corePrice(d.Close), corePrice(d.SMA10))
		}
		frozen := ""
		if !d.FrozenAt.IsZero() {
			frozen = "frozen " + formatTimeShort(d.FrozenAt.Local())
		}
		if d.Drift {
			frozen = env.red("drift since " + formatTimeShort(d.FrozenAt.Local()))
		}
		riskReadLine(env, "Decision", d.Decision, trend, frozen)
		binding := map[string]string{core.BindingFloor: "the 15% floor", core.BindingEWMA: "the EWMA", core.Binding252: "the 252-day"}[d.Binding]
		riskReadLine(env, "Volatility", corePct(d.Sigma)+" from "+binding, "EWMA "+corePct(d.SigmaEWMA), "252-day "+corePct(d.Sigma252))
	} else if d != nil && d.Reason != "" {
		riskReadLine(env, "Decision", d.Decision, d.Reason)
	}
	if s := res.Sizing; s != nil {
		on := s.CurrentOn
		sigma := ""
		if d := res.Decision; d != nil && d.Sigma > 0 {
			sigma = " ÷ " + corePct(d.Sigma) + " volatility"
		}
		riskReadLine(env, "Target", coreShares(s.Target)+" ("+mandateAmount(s.TargetBase, ccy)+")",
			fmt.Sprintf("%s%% of %s × room %s%s, at most %s%% (%s)", fmtPctNum(on.CoreVolPct), mandateAmount(on.BudgetBase, ccy), mandatePct(on.Room*100), sigma, fmtPctNum(on.MaxValuePct), mandateAmount(s.Current.Cap, ccy)),
			"scale "+fmtPctNum(on.ScalePct)+"%")
		if s.Frozen.TargetShares != s.Current.TargetShares {
			riskReadLine(env, "", fmt.Sprintf("frozen at %s, today's capital gives %s; the lower stands", coreShares(s.Frozen.TargetShares), coreShares(s.Current.TargetShares)))
		}
	}
	if h := res.Holding; h != nil {
		source := map[string]string{"mark": "mark", "close": "last close"}[h.PriceSource]
		riskReadLine(env, "Holding", coreShares(h.Shares)+" ("+mandateAmount(h.ValueBase, ccy)+")", "price "+corePrice(h.Price)+" "+source)
	}
	if a != nil && a.Kind != core.ActionHold && res.Policy != nil && res.Policy.BandPct != nil && res.Policy.MinOrderBase != nil {
		band := fmt.Sprintf("%s%% of the target (%.0f %s)", fmtPctNum(*res.Policy.BandPct), a.BandShares, pluralWord(int(math.Round(a.BandShares)), "share", "shares"))
		order := ""
		if a.OrderValue > 0 {
			order = "order " + mandateAmount(a.OrderValue, ccy)
		}
		riskReadLine(env, "Band", band, "minimum order "+mandateAmount(*res.Policy.MinOrderBase, ccy), order)
	}
	if m := res.Mandate; m != nil {
		line := []string{m.Status}
		if m.BudgetAnnualSD > 0 {
			line = append(line, fmt.Sprintf("risk %s a year of the budget %s", mandateAmount(m.RiskAnnualSD, ccy), mandateAmount(m.BudgetAnnualSD, ccy)))
		}
		if res.Sizing != nil && res.Sizing.Holdable != nil {
			line = append(line, fmt.Sprintf("headroom admits a holding of %s (95%% of the budget and of the gap cap)", coreShares(*res.Sizing.Holdable)))
		}
		riskReadLine(env, "Mandate", line...)
	}
	if res.NextDecision != "" {
		riskReadLine(env, "Next decision", res.NextDecision+" after the Xetra close")
	}
	if d := res.Decision; d != nil && len(d.MonthEnds) > 0 {
		fmt.Fprintln(env.Stdout, "\n"+env.bold("Month-ends"))
		parts := make([]string, len(d.MonthEnds))
		for i, m := range d.MonthEnds {
			parts[i] = m + " " + corePrice(d.MonthEndCloses[i])
		}
		riskDisplayLine(env, "  ", strings.Join(parts, " · "), nil)
	}
	fmt.Fprintln(env.Stdout, "\n"+env.bold("Evidence"))
	if e := res.Evidence; e != nil {
		switch {
		case e.Missing:
			riskReadLine(env, "  "+e.SeriesKey, env.yellow("no history yet"))
		case e.Stale:
			riskReadLine(env, "  "+e.SeriesKey, env.yellow(fmt.Sprintf("stale: %d closes to %s", e.Closes, e.LastClose)), "read "+formatTimeShort(e.FetchedAt.Local()), "Xetra calendar")
		default:
			riskReadLine(env, "  "+e.SeriesKey, fmt.Sprintf("%d closes to %s", e.Closes, e.LastClose), "read "+formatTimeShort(e.FetchedAt.Local()), "Xetra calendar")
		}
	}
	if m := res.Mandate; m != nil && !m.EvaluatedAt.IsZero() {
		riskReadLine(env, "  Mandate verdict", formatTimeShort(m.EvaluatedAt.Local()))
	}
	riskReadLine(env, "", "Read-only: the core proposes nothing and places no order in this phase. It is a beta allocation with a drawdown filter that you chose, not an edge claim.")
}

// fmtPctNum prints a percentage number without a trailing zero fraction.
func fmtPctNum(v float64) string {
	if v == math.Trunc(v) {
		return fmt.Sprintf("%.0f", v)
	}
	return fmt.Sprintf("%g", v)
}

// runCoreResolve lists every listing IBKR knows for the core's ISIN (the
// flag's, else the one [mandate.core] pins, else the compiled rule's) so
// the owner can write its contract id. Read-only: it never chooses a
// listing and never writes the policy.
func runCoreResolve(ctx context.Context, env *Env, args []string) int {
	fs := flagSet(env, "core resolve")
	jsonOut := fs.Bool("json", false, "emit the typed listings")
	isin := fs.String("isin", "", "the ISIN to resolve (default: [mandate.core] isin, else the rule's)")
	currency := fs.String("currency", "", "ask only for listings in this currency")
	if err := fs.Parse(args); err != nil {
		return parseExit(err)
	}
	if fs.NArg() != 0 {
		return failUnexpectedArgs(env, fs)
	}
	res, err := rpc.FetchCoreResolve(ctx, env.Conn, rpc.CoreResolveParams{ISIN: *isin, Currency: *currency})
	if err != nil {
		return fail(env, "core resolve: %v", err)
	}
	if *jsonOut {
		return printJSON(env, res)
	}
	renderCoreResolve(env, res)
	return 0
}

func renderCoreResolve(env *Env, res rpc.CoreResolveResult) {
	source := map[string]string{"flag": "asked", "policy": "from [mandate.core]", "rule": "the rule's instrument"}[res.ISINSource]
	headline, tone := fmt.Sprintf("%d %s", len(res.Listings), pluralWord(len(res.Listings), "LISTING", "LISTINGS")), "green"
	switch {
	case len(res.Listings) == 0:
		headline, tone = "NO LISTING", "red"
	case len(res.Listings) > 1 && !res.PinnedFound:
		tone = "yellow"
	}
	riskDisplayLine(env, env.bold("Core resolve")+"  ", headline+" for "+res.ISIN, func(s string) string { return riskTone(env, tone, env.bold(s)) })
	riskReadLine(env, "ISIN", res.ISIN, source, nonEmpty(res.Currency, "every currency"))
	for _, l := range res.Listings {
		parts := []string{fmt.Sprintf("con_id %d", l.ConID), l.Symbol}
		if l.LocalSymbol != "" && l.LocalSymbol != l.Symbol {
			parts[1] += " (local " + l.LocalSymbol + ")"
		}
		venue := nonEmpty(l.Exchange, "no exchange")
		if l.PrimaryExch != "" {
			venue += ", primary " + l.PrimaryExch
		}
		parts = append(parts, venue, l.Currency)
		if l.TradingClass != "" {
			parts = append(parts, "class "+l.TradingClass)
		}
		parts = append(parts, l.LongName)
		switch {
		case l.ISIN == "":
			parts = append(parts, "ISIN not named by IBKR")
		case l.ISIN != res.ISIN:
			parts = append(parts, "ISIN "+l.ISIN)
		}
		parts = append(parts, "found by "+strings.Join(l.Forms, ", "))
		style := func(s string) string { return s }
		if l.Pinned {
			parts = append(parts, "PINNED")
			style = env.green
		}
		riskDisplayLine(env, "  ", briefJoin(parts...), style)
	}
	pinned := "not written"
	if res.PinnedConID != nil {
		pinned = fmt.Sprintf("%d", *res.PinnedConID)
		if !res.PinnedFound {
			pinned += ", not among these listings"
		}
	}
	riskReadLine(env, "Pinned", "[mandate.core] con_id "+pinned)
	riskReadLine(env, "Next", res.Message)
	fmt.Fprintln(env.Stdout, "\n"+env.bold("Asked"))
	for _, a := range res.Attempts {
		answer := a.Outcome
		switch a.Outcome {
		case "line":
			answer = fmt.Sprintf("%d %s", a.Lines, pluralWord(a.Lines, "listing", "listings"))
		case "rejected":
			answer = fmt.Sprintf("IBKR %d \"%s\"", a.Code, a.Message)
		case "no_line":
			answer = nonEmpty(a.Message, "no line")
		case "failed":
			answer = "failed: " + a.Message
		}
		riskReadLine(env, "  "+a.Form, answer)
	}
	riskReadLine(env, "", "Read-only: nothing is chosen for you and the policy is not written.")
}
