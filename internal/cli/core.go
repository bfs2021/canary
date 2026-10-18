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

// renderCore renders a strategy target: the core's, headed "Core", or any
// register entry's, headed by its id (strategy-evaluator.md §6).
func renderCore(env *Env, res rpc.CoreTargetResult) {
	isCore := res.Strategy == "" || res.Strategy == "core"
	heading, name := "Core", "the core"
	if !isCore {
		heading, name = res.Strategy, "the "+res.Strategy+" strategy"
	}
	if !res.Active {
		riskDisplayLine(env, env.bold(heading)+"  ", "INACTIVE", env.dim)
		riskDisplayLine(env, "", nonEmpty(res.Message, "the risk constitution carries no [mandate.core] table"), nil)
		riskReadLine(env, "Next", "write [mandate.core] into risk-policy.toml with a higher policy_version; canary policy default constitution shows the keys")
		return
	}
	ccy := core.Currency
	sym := core.Symbol
	if !isCore {
		ccy, sym = "", res.Strategy
	}
	if res.Instrument != nil {
		ccy, sym = nonEmpty(res.Instrument.Currency, ccy), nonEmpty(res.Instrument.Symbol, nonEmpty(res.Instrument.ISIN, sym))
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
	riskDisplayLine(env, env.bold(heading)+"  ", headline, func(s string) string { return riskTone(env, tone, env.bold(s)) })
	spec := res.SpecHash
	if len(spec) > 12 {
		spec = spec[:12]
	}
	fund := ""
	if in := res.Instrument; in != nil {
		fund = strings.TrimSpace(in.ISIN + " " + in.Currency)
	}
	if isCore {
		riskReadLine(env, "Rule", res.Rule, "spec "+res.SpecID+" "+spec, fund)
	} else {
		riskReadLine(env, "Rule", res.Rule, "signal "+nonEmpty(res.Signal, "unwritten"), "spec cited "+nonEmpty(spec, "(none)"), fund, "sleeve "+res.Sleeve)
	}
	if st := res.Stage; st != nil {
		reserve := "reserves nothing"
		if st.ReservedPct > 0 {
			reserve = fmt.Sprintf("reserves %g%% of its allocation", st.ReservedPct)
		}
		cited := "spec cited"
		if !st.SpecHashCited {
			cited = "spec compiled, not cited"
		}
		riskReadLine(env, "Stage", append([]string{st.State, reserve, cited}, st.Notes...)...)
	}
	if in := res.Instrument; in != nil {
		riskReadLine(env, "Contract", coreContractLine(*in)...)
	}
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
			fmt.Sprintf("%s%% of %s × room %s%s, at most %s%% (%s)", fmtPctNum(on.AllocationPct), mandateAmount(on.BudgetBase, ccy), mandatePct(on.Room*100), sigma, fmtPctNum(on.MaxValuePct), mandateAmount(s.Current.Cap, ccy)),
			"scale "+fmtPctNum(on.ScalePct)+"%")
		if s.Frozen.TargetShares != s.Current.TargetShares {
			riskReadLine(env, "", fmt.Sprintf("frozen at %s, today's capital gives %s; the lower stands", coreShares(s.Frozen.TargetShares), coreShares(s.Current.TargetShares)))
		}
	}
	if h := res.Holding; h != nil {
		price := "no price yet"
		if h.Price > 0 {
			price = "price " + corePrice(h.Price) + " " + map[string]string{"mark": "mark", "close": "last close"}[h.PriceSource]
		}
		riskReadLine(env, "Holding", coreShares(h.Shares)+" ("+mandateAmount(h.ValueBase, ccy)+")", price)
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
			line = append(line, fmt.Sprintf("risk %s a year of the budget %s", mandateAmount(m.RiskAnnualSD, nonEmpty(m.BaseCurrency, "currency unavailable")), mandateAmount(m.BudgetAnnualSD, nonEmpty(m.BaseCurrency, "currency unavailable"))))
		}
		if res.Sizing != nil && res.Sizing.Holdable != nil {
			line = append(line, fmt.Sprintf("headroom admits a holding of %s (95%% of the budget and of the gap cap)", coreShares(*res.Sizing.Holdable)))
		}
		riskReadLine(env, "Mandate", line...)
	}
	if k := res.Kill; k != nil && (k.MaxDrawdownK != nil || k.Measured) {
		line := []string{}
		if k.Measured {
			line = append(line, fmt.Sprintf("drawdown %.2f K on the shadow record at %d %s", k.DrawdownK, k.Decisions, pluralWord(k.Decisions, "decision", "decisions")))
		}
		switch {
		case k.MaxDrawdownK != nil && k.Breached:
			line = append(line, env.red(fmt.Sprintf("past the bound %g K: buys stop; suspend it with stage = \"suspended\"", *k.MaxDrawdownK)))
		case k.MaxDrawdownK != nil:
			line = append(line, fmt.Sprintf("bound %g K", *k.MaxDrawdownK))
		default:
			line = append(line, "no bound written: cannot fire")
		}
		if k.Note != "" && k.MaxDrawdownK != nil {
			line = append(line, k.Note)
		}
		riskReadLine(env, "Kill rule", line...)
	}
	if res.NextDecision != "" {
		riskReadLine(env, "Next decision", res.NextDecision+" after the "+coreVenue(res.Instrument)+" close")
	}
	if d := res.Decision; d != nil && len(d.MonthEnds) > 0 {
		fmt.Fprintln(env.Stdout, "\n"+env.bold("Month-ends"))
		parts := make([]string, len(d.MonthEnds))
		for i, m := range d.MonthEnds {
			parts[i] = m + " " + corePrice(d.MonthEndCloses[i])
		}
		riskDisplayLine(env, "  ", strings.Join(parts, " · "), nil)
	}
	renderCoreShadow(env, res.Shadow)
	fmt.Fprintln(env.Stdout, "\n"+env.bold("Evidence"))
	if e := res.Evidence; e != nil {
		calendar := coreVenue(res.Instrument) + " calendar"
		switch {
		case e.Missing:
			riskWarnReadLine(env, "  "+e.SeriesKey, "no history yet", calendar)
		case e.Stale:
			riskWarnReadLine(env, "  "+e.SeriesKey, fmt.Sprintf("stale: %d closes to %s", e.Closes, e.LastClose), "read "+formatTimeShort(e.FetchedAt.Local()), calendar)
		default:
			riskReadLine(env, "  "+e.SeriesKey, fmt.Sprintf("%d closes to %s", e.Closes, e.LastClose), "read "+formatTimeShort(e.FetchedAt.Local()), calendar)
		}
		if e.ReadError != "" {
			riskWarnReadLine(env, "  Last read", "failed "+formatTimeShort(e.ReadErrorAt.Local()), e.ReadError)
		}
	}
	if m := res.Mandate; m != nil && !m.EvaluatedAt.IsZero() {
		riskReadLine(env, "  Mandate verdict", formatTimeShort(m.EvaluatedAt.Local()))
	}
	if isCore {
		riskReadLine(env, "", "Read-only: the core proposes nothing and places no order in this phase. It is a beta allocation with a drawdown filter that you chose, not an edge claim.")
	} else {
		riskReadLine(env, "", "Read-only: "+name+" proposes nothing and places no order in this build; its rule sends nothing, so it runs in shadow at most.")
	}
}

// runStrategy reads the daemon's target for one registered strategy by its
// id in the strategy register (internal-docs/design/strategy-evaluator.md),
// in the core's shape. The daemon evaluates; this renders. Read-only: no
// entry proposes or sends anything in this build.
func runStrategy(ctx context.Context, env *Env, args []string) int {
	fs := flagSet(env, "strategy")
	jsonOut := fs.Bool("json", false, "emit the typed strategy target")
	refresh := fs.Bool("refresh", false, "measure the mandate again before deciding")
	if err := fs.Parse(args); err != nil {
		return parseExit(err)
	}
	if fs.NArg() != 1 {
		return printCommandUsage(env, "strategy")
	}
	res, err := rpc.FetchStrategyTarget(ctx, env.Conn, fs.Arg(0), *refresh)
	if err != nil {
		return fail(env, "strategy: %v", err)
	}
	if *jsonOut {
		return printJSON(env, res)
	}
	renderCore(env, res)
	return 0
}

// coreContractLine is the contract the core reads and would trade, as
// IBKR's record for the ISIN names it, and the calendar it is judged on;
// before IBKR has answered, the fund the policy names and that it is not
// resolved.
func coreContractLine(in rpc.CoreInstrument) []string {
	if !in.Resolved {
		line := []string{"not resolved"}
		if in.ConID > 0 {
			line = append(line, fmt.Sprintf("con_id %d written in [mandate.core]", in.ConID))
		}
		return line
	}
	name := in.Symbol
	if in.TradingClass != "" && in.TradingClass != in.Symbol {
		name += " (class " + in.TradingClass + ")"
	}
	return []string{fmt.Sprintf("%d %s on %s, %s", in.ConID, name, in.Exchange, in.Currency), nonEmpty(in.CalendarLabel, in.Calendar) + " calendar", "from IBKR's record for the ISIN"}
}

// coreVenue names the core's exchange calendar for a line: its label once
// the contract is resolved, else "exchange".
func coreVenue(in *rpc.CoreInstrument) string {
	if in == nil || !in.Resolved {
		return "exchange"
	}
	return nonEmpty(in.CalendarLabel, nonEmpty(in.Calendar, in.Exchange))
}

// fmtPctNum prints a percentage number without a trailing zero fraction.
func fmtPctNum(v float64) string {
	if v == math.Trunc(v) {
		return fmt.Sprintf("%.0f", v)
	}
	return fmt.Sprintf("%g", v)
}

// runCoreResolve lists every listing IBKR knows for the core's ISIN (the
// flag's, else the one [mandate.core] names, else the compiled rule's) and
// marks the one the core uses, so the owner can choose among several by
// writing its contract id. Read-only: it never chooses a listing and never
// writes the policy.
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
		if l.Used {
			parts = append(parts, "THE CORE USES IT")
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

// renderCoreShadow lists the core's shadow record
// (internal-docs/design/strategy-register.md, section 3): the paper position
// its production code holds and the newest records. Nothing in it is sent.
func renderCoreShadow(env *Env, v *rpc.ShadowView) {
	if v == nil {
		return
	}
	fmt.Fprintln(env.Stdout, "\n"+env.bold("Shadow")+"  the rule on a paper position; nothing is sent")
	if v.Unavailable != "" {
		riskWarnReadLine(env, "  Record", "unavailable", v.Unavailable)
		return
	}
	riskReadLine(env, "  Paper position", coreShares(v.Holding), fmt.Sprintf("%d %s", v.Records, pluralWord(v.Records, "record", "records")))
	for _, r := range v.Recent {
		switch {
		case r.Kind == rpc.ShadowKindDecision && r.Decision != nil:
			trend := "trend off"
			if r.Decision.TrendOn {
				trend = "trend on"
			}
			riskReadLine(env, "  "+r.Session, "decision", trend, "target "+coreShares(r.Decision.TargetShares)+" at scale "+fmtPctNum(r.Decision.ScalePct)+"%")
		case r.Kind == rpc.ShadowKindIntent && r.Intent != nil:
			what := strings.ReplaceAll(r.Intent.Kind, "_", " ")
			if q := r.Intent.Quantity; q != 0 {
				what += " " + coreShares(int(math.Abs(float64(q))))
			}
			riskReadLine(env, "  "+r.Session, "intent for "+r.Intent.ForSession, what, r.Intent.Why)
		case r.Kind == rpc.ShadowKindFill && r.Fill != nil && r.Fill.Unobserved != "":
			riskWarnReadLine(env, "  "+r.Session, "fill unobserved", r.Fill.Unobserved)
		case r.Kind == rpc.ShadowKindFill && r.Fill != nil:
			side := "bought"
			if r.Fill.Quantity < 0 {
				side = "sold"
			}
			riskReadLine(env, "  "+r.Session, "fill", side+" "+coreShares(int(math.Abs(float64(r.Fill.Quantity))))+" at "+corePrice(r.Fill.Price)+" (the close)", "paper position "+coreShares(r.Fill.HoldingAfter))
		case r.Kind == rpc.ShadowKindHold:
			riskWarnReadLine(env, "  "+r.Session, "held", r.Reasons...)
		}
	}
}
