package cli

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/osauer/canary/v2/internal/rpc"
	"github.com/osauer/canary/v2/internal/scorecard"
)

func runScorecard(ctx context.Context, env *Env, args []string) int {
	fs := flagSet(env, "scorecard")
	sleeve := fs.String("sleeve", "", "one sleeve, for example owner (default: every sleeve)")
	horizon := fs.Int("horizon", scorecard.PrimaryHorizon, "sessions after entry to read: 5, 10 or 21")
	limit := fs.Int("limit", 10, "decisions to list, newest first (1-2000)")
	jsonOut := fs.Bool("json", false, "emit the full typed scorecard")
	if err := fs.Parse(args); err != nil {
		return parseExit(err)
	}
	if fs.NArg() != 0 {
		return failUnexpectedArgs(env, fs)
	}
	if *horizon != 5 && *horizon != scorecard.PrimaryHorizon && *horizon != scorecard.MaxHorizon {
		return fail(env, "scorecard: --horizon must be 5, 10 or 21")
	}
	res, err := scorecard.Fetch(ctx, env.Conn, rpc.ScorecardReadParams{Sleeve: *sleeve, Limit: *limit})
	if err != nil {
		return fail(env, "scorecard: %v", err)
	}
	if *jsonOut {
		return printJSON(env, res)
	}
	renderScorecard(env, res, *horizon)
	return 0
}

// renderScorecard says in plain words what the daemon's evidence means and
// how large the sample is. A sleeve is never called skilled before the
// established gate; accruing reads as "not enough evidence yet".
func renderScorecard(env *Env, res rpc.ScorecardResult, horizon int) {
	out := env.Stdout
	state := res.State
	if state == "" {
		state = "unavailable"
	}
	tone := ""
	if state != rpc.ScorecardStateCurrent {
		tone = "yellow"
	}
	riskDisplayLine(env, env.bold("Decision scorecard")+"  ", strings.ToUpper(state), func(s string) string { return riskTone(env, tone, env.bold(s)) })
	scored := "unavailable"
	if !res.AsOf.IsZero() {
		scored = res.AsOf.Local().Format("2 Jan 15:04 MST")
	}
	evidence := ""
	if !res.EvidenceTo.IsZero() {
		evidence = "statements to " + res.EvidenceTo.Format(time.DateOnly)
	}
	riskReadLine(env, "Scored", scored, evidence, strings.ReplaceAll(res.Reason, "_", " "))
	if res.State != rpc.ScorecardStateCurrent && len(res.Sleeves) == 0 {
		riskReadLine(env, "", scorecardStateWords(res))
		riskReadLine(env, "", "Read-only review of past fills; it places no orders.")
		return
	}
	riskReadLine(env, "Horizon", fmt.Sprintf("%d sessions after entry", horizon), fmt.Sprintf("primary %d", res.PrimaryHorizon), "others "+scorecardOtherHorizons(res, horizon))

	shown := 0
	for _, sleeve := range res.Sleeves {
		if sleeve.Horizon != horizon {
			continue
		}
		shown++
		fmt.Fprintln(out)
		renderScorecardSleeve(env, sleeve, res.BaseCurrency)
	}
	if shown == 0 {
		fmt.Fprintln(out)
		riskReadLine(env, "Sleeves", "none scored yet")
	}
	renderScorecardDecisions(env, res, horizon)
	fmt.Fprintln(out)
	riskReadLine(env, "Coverage", fmt.Sprintf("%d decisions in the ledger", res.Coverage.Ledger), fmt.Sprintf("%d stock", res.Coverage.Stocks), fmt.Sprintf("%d option", res.Coverage.Options),
		fmt.Sprintf("%d entries", res.Coverage.Entries), fmt.Sprintf("%d exits", res.Coverage.Exits), scorecardCounts("rule-originated, in no owner stream", res.Coverage.RuleOriginated),
		scorecardCounts("skipped", res.Coverage.Skipped), scorecardCounts("unknown", res.Coverage.UnknownByReason))
	history := fmt.Sprintf("%d names", res.History.Names)
	if res.History.Missing > 0 || res.History.Pending > 0 {
		history = fmt.Sprintf("%d names, %d without history, %d still to fetch", res.History.Names, res.History.Missing, res.History.Pending)
	}
	riskReadLine(env, "History", history, fmt.Sprintf("%s %d sessions", res.History.MarketSymbol, res.History.MarketBars))
	fmt.Fprintln(out)
	riskReadLine(env, "Reading", "z is the underlying's move from the close of the fill's session, net of its market exposure, in units of its own daily noise; positive means the move went the decision's way. Entries (opens and adds) carry the gates; exits (trims and exits) are reported, never gated. Each entry week counts once. The e-value is the weight of evidence for information in the decisions (100 is the bar); the posterior gives the chance the true mean z per week exceeds 0.05 and 0.10.")
	riskReadLine(env, "", "A sleeve is established only when every gate on its entries passes; until then nothing here says whether the decisions carry information. Fills made by Canary's rules or the broker's lifecycle are the rule sleeve and count for no one. Read-only review of past fills; it places no orders.")
}

func scorecardStateWords(res rpc.ScorecardResult) string {
	switch res.Reason {
	case "flex_configuration_required":
		return "Flex statement setup is required before decisions can be scored."
	case "snapshot_pending", "statement_account_pending":
		return "The first pass over the broker statements has not completed yet."
	case "history_backfill_pending":
		return "Price history is still being fetched within the broker's budget; scores appear as names arrive."
	case "account_scope_unavailable", "account_scope_changed":
		return "The broker account is not confirmed, so no scorecard is served."
	default:
		return "The scorecard is not available: " + strings.ReplaceAll(res.Reason, "_", " ") + "."
	}
}

func scorecardOtherHorizons(res rpc.ScorecardResult, horizon int) string {
	var others []string
	for _, h := range res.Horizons {
		if h != horizon {
			others = append(others, fmt.Sprintf("%d", h))
		}
	}
	if len(others) == 0 {
		return "none"
	}
	return strings.Join(others, ", ")
}

func renderScorecardSleeve(env *Env, s rpc.ScorecardSleeve, base string) {
	if !s.Gated {
		riskDisplayLine(env, env.bold("Sleeve "+s.Sleeve+" · exits")+"  ", "REPORTED, NOT GATED", func(t string) string { return env.dim(env.bold(t)) })
		riskDisplayLine(env, "  ", "Trims and exits: did the name move against the position afterwards? Exit timing is a different skill from entry selection and never gates the sleeve.", nil)
	} else {
		gate := strings.ToUpper(s.Gate)
		tone := ""
		switch s.Gate {
		case rpc.ScorecardGateEstablished:
			tone = "green"
		case rpc.ScorecardGateFutile:
			tone = "red"
		}
		riskDisplayLine(env, env.bold("Sleeve "+s.Sleeve+" · entries")+"  ", gate, func(t string) string { return riskTone(env, tone, env.bold(t)) })
		riskDisplayLine(env, "  ", scorecardGateWords(s), nil)
	}
	riskReadLine(env, "  Sample", fmt.Sprintf("%d decisions", s.Decisions), fmt.Sprintf("%d scored", s.Scored), fmt.Sprintf("%d entry weeks (each counts once)", s.Clusters))
	if s.Clusters == 0 {
		return
	}
	riskReadLine(env, "  Mean z per week", scorecardSigned(s.MeanZ, 2), fmt.Sprintf("running sum %+.1f", s.SumZ))
	eValue := "0 (against)"
	if !s.EValueAgainst {
		eValue = fmt.Sprintf("%.1f of %.0f", s.EValue, scorecard.EstablishedEValue)
	}
	riskReadLine(env, "  Evidence", "e-value "+eValue, "P(IC > 0.05) "+scorecardProb(s.PAbove005), "P(IC > 0.10) "+scorecardProb(s.PAbove010))
	if s.Gated && s.ClustersToEstablish != nil {
		riskReadLine(env, "  Distance", fmt.Sprintf("at this mean the e-value gate needs about %d weeks; the sample gate needs %d", *s.ClustersToEstablish, scorecard.EstablishedClusters))
	}
	halves := "halves " + scorecardSigned(s.Gates.FirstHalfMeanZ, 2) + " / " + scorecardSigned(s.Gates.SecondHalfMeanZ, 2)
	markets := fmt.Sprintf("up markets %s (%d) / down %s (%d)", scorecardSigned(s.Gates.UpMarketMeanZ, 2), s.Gates.UpClusters, scorecardSigned(s.Gates.DownMarketMeanZ, 2), s.Gates.DownClusters)
	riskReadLine(env, "  Consistency", halves, markets)
	if s.Wrapper != nil || s.RealisedVsImplied != nil {
		values := []string{}
		if w := s.Wrapper; w != nil {
			values = append(values, fmt.Sprintf("wrapper mean %s (95%% %s to %s) over %d", formatMoneyCcyForPnL(w.MeanBase, base), formatMoneyCcyForPnL(w.CILow, base), formatMoneyCcyForPnL(w.CIHigh, base), w.Count))
		}
		if r := s.RealisedVsImplied; r != nil {
			values = append(values, fmt.Sprintf("realised/implied median %.2f over %d", r.Median, r.Count))
		}
		riskReadLine(env, "  Options", values...)
	}
	if s.HolmAdjustedP != nil {
		riskReadLine(env, "  Across sleeves", fmt.Sprintf("Holm-adjusted p %.3f", *s.HolmAdjustedP))
	}
	if unknown := scorecardCounts("", s.Unknown); unknown != "" {
		riskReadLine(env, "  Unknown", unknown)
	}
}

func scorecardGateWords(s rpc.ScorecardSleeve) string {
	switch s.Gate {
	case rpc.ScorecardGateEstablished:
		return "Opens and adds: every gate passes, the evidence says these entries carry information at this horizon."
	case rpc.ScorecardGateFutile:
		return "Opens and adds: the evidence says these entries carry no usable information at this horizon; the posterior leaves under a 5% chance of a mean z above 0.10."
	default:
		return "Opens and adds: not enough evidence yet to say either way."
	}
}

func scorecardSigned(v *float64, decimals int) string {
	if v == nil {
		return "—"
	}
	return fmt.Sprintf("%+.*f", decimals, *v)
}

func scorecardProb(v *float64) string {
	if v == nil {
		return "—"
	}
	return fmt.Sprintf("%.2f", *v)
}

func scorecardCounts(label string, counts map[string]int) string {
	if len(counts) == 0 {
		return ""
	}
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s %d", strings.ReplaceAll(key, "_", " "), counts[key]))
	}
	if label == "" {
		return strings.Join(parts, ", ")
	}
	return label + ": " + strings.Join(parts, ", ")
}

func renderScorecardDecisions(env *Env, res rpc.ScorecardResult, horizon int) {
	out := env.Stdout
	fmt.Fprintln(out)
	title := fmt.Sprintf("Latest decisions · %d of %d", len(res.Decisions), res.DecisionsTotal)
	fmt.Fprintln(out, env.bold(title))
	if len(res.Decisions) == 0 {
		riskReadLine(env, "  ", "none in the ledger")
		return
	}
	fmt.Fprintf(out, "  %-10s %-8s %-5s %-4s %8s %6s  %s\n", "DATE", "NAME", "ACT", "SIDE", "QTY", "z", "NOTE")
	for _, d := range res.Decisions {
		var score *rpc.ScorecardHorizonScore
		for i := range d.Scores {
			if d.Scores[i].Sessions == horizon {
				score = &d.Scores[i]
			}
		}
		z, note := "—", ""
		if score != nil {
			if score.Z != nil {
				z = fmt.Sprintf("%+.2f", *score.Z)
			}
			note = scorecardDecisionNote(d, score, res.BaseCurrency)
		}
		name := d.Underlying
		if d.Instrument == scorecard.InstrumentOption && d.Option != nil {
			name = fmt.Sprintf("%s %s", d.Underlying, strings.ToUpper(d.Option.Right[:1]))
		}
		line := fmt.Sprintf("  %-10s %-8s %-5s %-4s %8s %6s", d.ExecutedAt.Format(time.DateOnly), sanitizeRunText(name), d.Action, d.Side, formatQuantity(d.Quantity), z)
		if note != "" {
			line += "  " + env.dim(sanitizeRunText(note))
		}
		fmt.Fprintln(out, line)
	}
}

func scorecardDecisionNote(d rpc.ScorecardDecision, score *rpc.ScorecardHorizonScore, base string) string {
	parts := []string{}
	if d.Sleeve == rpc.ScorecardSleeveRule {
		parts = append(parts, "rule "+strings.ReplaceAll(strings.ReplaceAll(d.Origin, "proposal:", ""), "_", " ")+", counts for no one")
	}
	if score.Z == nil {
		if score.Reason != "" {
			parts = append(parts, strings.ReplaceAll(score.Reason, "_", " "))
		} else {
			parts = append(parts, "unknown")
		}
		return strings.Join(parts, ", ")
	}
	if d.Instrument == scorecard.InstrumentOption {
		if score.WrapperBase != nil {
			parts = append(parts, "wrapper "+formatMoneyCcyForPnL(*score.WrapperBase, base))
		} else if score.WrapperReason != "" {
			parts = append(parts, "wrapper "+strings.ReplaceAll(score.WrapperReason, "_", " "))
		}
		if score.RealisedVsImplied != nil {
			parts = append(parts, fmt.Sprintf("moved %.1fx implied", *score.RealisedVsImplied))
		}
	}
	return strings.Join(parts, ", ")
}

func formatQuantity(q float64) string {
	if q == float64(int64(q)) {
		return fmt.Sprintf("%d", int64(q))
	}
	return fmt.Sprintf("%.2f", q)
}
