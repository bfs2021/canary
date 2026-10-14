package scorecard

import (
	"math"
	"sort"
	"strings"
	"time"

	"github.com/osauer/canary/v2/internal/edge"
	"github.com/osauer/canary/v2/internal/rpc"
)

// Input is everything Evaluate needs; the daemon assembles it from the
// persisted ledger, the history cache and the retained statements.
type Input struct {
	AsOf         time.Time
	EvidenceTo   time.Time
	BaseCurrency string
	Decisions    []Decision
	History      History
	Marks        map[int64][]edge.ContractMark
	Events       map[int64][]edge.ContractEvent
	Skipped      map[string]int
	Superseded   int
	HistoryState rpc.ScorecardHistory
}

// Evaluate scores every decision at every horizon and accumulates the
// evidence per sleeve. It is deterministic: the same input yields the same
// result, including the bootstrap intervals.
func Evaluate(in Input) rpc.ScorecardResult {
	out := rpc.ScorecardResult{
		SchemaVersion: SchemaVersion, State: rpc.ScorecardStateCurrent, AsOf: in.AsOf.UTC(), EvidenceTo: in.EvidenceTo.UTC(), BaseCurrency: strings.ToUpper(strings.TrimSpace(in.BaseCurrency)),
		PrimaryHorizon: PrimaryHorizon, Horizons: append([]int(nil), Horizons[:]...),
		Sleeves: []rpc.ScorecardSleeve{}, Decisions: []rpc.ScorecardDecision{},
		Coverage: rpc.ScorecardCoverage{RuleOriginated: map[string]int{}, Skipped: map[string]int{}, ScoredByHorizon: map[int]int{}, UnknownByReason: map[string]int{}, Superseded: in.Superseded},
		History:  in.HistoryState, Method: defaultMethod(), NotExecution: true,
	}
	out.History.MarketSymbol = firstNonEmpty(in.History.MarketSymbol, "SPY")
	out.History.MarketBars = len(in.History.Market)
	for reason, count := range in.Skipped {
		out.Coverage.Skipped[reason] = count
	}
	market := newSeries(in.History.Market)
	underlyingSeries := map[int64]series{}
	seriesFor := func(conid int64) series {
		if s, ok := underlyingSeries[conid]; ok {
			return s
		}
		s := newSeries(in.History.Underlying[conid])
		underlyingSeries[conid] = s
		return s
	}
	exits := exitsByContract(in.Decisions)

	type scored struct {
		decision Decision
		entry    rpc.ScorecardEntry
		scores   []rpc.ScorecardHorizonScore
	}
	rows := make([]scored, 0, len(in.Decisions))
	for _, d := range in.Decisions {
		out.Coverage.Ledger++
		if d.Instrument == InstrumentOption {
			out.Coverage.Options++
		} else {
			out.Coverage.Stocks++
		}
		if d.Stream == "" {
			d.Stream = StreamOf(d.Action)
		}
		if d.Sleeve == rpc.ScorecardSleeveRule {
			out.Coverage.RuleOriginated[firstNonEmpty(d.Origin, "rule")]++
		} else if d.Stream == rpc.ScorecardStreamEntries {
			out.Coverage.Entries++
		} else {
			out.Coverage.Exits++
		}
		underlying := seriesFor(d.UnderlyingConID)
		entry, entryIdx, entryOK := entryFacts(d, underlying)
		c := scoreContext{decision: d, entry: entry, entryIdx: entryIdx, entryOK: entryOK, underlying: underlying, market: market, marks: in.Marks[d.ConID], events: in.Events[d.ConID], exits: exits[d.ConID]}
		scores := make([]rpc.ScorecardHorizonScore, 0, len(Horizons))
		for _, h := range Horizons {
			score := scoreHorizon(c, h)
			if score.Z != nil {
				out.Coverage.ScoredByHorizon[h]++
			} else if score.Reason != "" {
				out.Coverage.UnknownByReason[score.Reason]++
			}
			scores = append(scores, score)
		}
		rows = append(rows, scored{decision: d, entry: entry, scores: scores})
	}

	// Rule-originated fills are ledger rows, never evidence for a sleeve:
	// a protection stop or an assignment is not a decision anyone made.
	sleeves := map[string]bool{}
	for _, row := range rows {
		if row.decision.Sleeve != rpc.ScorecardSleeveRule {
			sleeves[row.decision.Sleeve] = true
		}
	}
	names := make([]string, 0, len(sleeves))
	for name := range sleeves {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, h := range Horizons {
		for _, name := range names {
			for _, stream := range []string{rpc.ScorecardStreamEntries, rpc.ScorecardStreamExits} {
				var decisions []Decision
				var scores []rpc.ScorecardHorizonScore
				for _, row := range rows {
					if row.decision.Sleeve != name || row.decision.Stream != stream {
						continue
					}
					decisions = append(decisions, row.decision)
					scores = append(scores, row.scores[horizonIndex(h)])
				}
				out.Sleeves = append(out.Sleeves, sleeveEvidence(name, stream, h, decisions, scores))
			}
		}
	}
	applyHolm(out.Sleeves, PrimaryHorizon)

	for _, row := range rows {
		out.Decisions = append(out.Decisions, publicDecision(row.decision, row.entry, row.scores))
	}
	sort.SliceStable(out.Decisions, func(i, j int) bool {
		if !out.Decisions[i].ExecutedAt.Equal(out.Decisions[j].ExecutedAt) {
			return out.Decisions[i].ExecutedAt.After(out.Decisions[j].ExecutedAt)
		}
		return out.Decisions[i].ID < out.Decisions[j].ID
	})
	out.DecisionsTotal = len(out.Decisions)
	return out
}

func horizonIndex(h int) int {
	for i, value := range Horizons {
		if value == h {
			return i
		}
	}
	return 0
}

// exitsByContract lists, per contract, the fills that left it flat.
func exitsByContract(decisions []Decision) map[int64][]exitFill {
	out := map[int64][]exitFill{}
	for _, d := range decisions {
		if d.FillPrice == nil || !almostZero(d.PositionAfter) {
			continue
		}
		out[d.ConID] = append(out[d.ConID], exitFill{day: dayStart(d.ExecutedAt), price: *d.FillPrice})
	}
	for conid, fills := range out {
		sort.Slice(fills, func(i, j int) bool { return fills[i].day.Before(fills[j].day) })
		out[conid] = fills
	}
	return out
}

type cluster struct {
	key    string
	sumZ   float64
	sumMkt float64
	count  int
}

// sleeveEvidence accumulates one sleeve stream's evidence at one horizon:
// each sleeve × entry-week cluster counts once, through the mean z of its
// decisions. Only the entries stream is gated.
func sleeveEvidence(name, stream string, h int, decisions []Decision, scores []rpc.ScorecardHorizonScore) rpc.ScorecardSleeve {
	gated := stream == rpc.ScorecardStreamEntries
	out := rpc.ScorecardSleeve{Sleeve: name, Stream: stream, Horizon: h, Gated: gated, Decisions: len(decisions), Unknown: map[string]int{}}
	if gated {
		out.Gate = rpc.ScorecardGateAccruing
	}
	byKey := map[string]*cluster{}
	var wrappers, rvis []float64
	for i, score := range scores {
		if score.Z == nil {
			if score.Reason != "" {
				out.Unknown[score.Reason]++
			}
			continue
		}
		out.Scored++
		key := decisions[i].ClusterKey
		c := byKey[key]
		if c == nil {
			c = &cluster{key: key}
			byKey[key] = c
		}
		c.sumZ += *score.Z
		c.count++
		if score.MarketReturn != nil {
			c.sumMkt += *score.MarketReturn
		}
		if score.WrapperBase != nil {
			wrappers = append(wrappers, *score.WrapperBase)
		}
		if score.RealisedVsImplied != nil {
			rvis = append(rvis, *score.RealisedVsImplied)
		}
	}
	clusters := make([]*cluster, 0, len(byKey))
	for _, c := range byKey {
		clusters = append(clusters, c)
	}
	// Cluster keys are sleeve:YYYY-Www, so lexical order is chronological.
	sort.Slice(clusters, func(i, j int) bool { return clusters[i].key < clusters[j].key })
	means := make([]float64, 0, len(clusters))
	var up, down []float64
	for _, c := range clusters {
		m := c.sumZ / float64(c.count)
		means = append(means, m)
		if c.sumMkt > 0 {
			up = append(up, m)
		} else {
			down = append(down, m)
		}
	}
	n := len(means)
	out.Clusters = n
	if len(wrappers) > 0 {
		low, high := bootstrapMeanCI(wrappers, BootstrapResamples)
		out.Wrapper = &rpc.ScorecardWrapperSummary{Count: len(wrappers), MeanBase: mean(wrappers), CILow: low, CIHigh: high}
	}
	if len(rvis) > 0 {
		out.RealisedVsImplied = &rpc.ScorecardRealisedVsImplied{Count: len(rvis), Median: median(rvis)}
	}
	if n == 0 {
		out.EValueAgainst = true
		return out
	}
	sum := 0.0
	for _, m := range means {
		sum += m
	}
	meanZ := sum / float64(n)
	out.SumZ, out.MeanZ = sum, &meanZ
	pm, psd := PosteriorIC(n, sum)
	p005, p010 := PosteriorAbove(n, sum, 0.05), PosteriorAbove(n, sum, 0.10)
	out.PosteriorMean, out.PosteriorSD, out.PAbove005, out.PAbove010 = &pm, &psd, &p005, &p010
	out.EValue, out.EValueAgainst = EValue(n, sum)
	if projected, ok := ClustersToEstablish(meanZ); ok {
		out.ClustersToEstablish = &projected
	}
	gates := rpc.ScorecardGates{EValue: out.EValue >= EstablishedEValue, Clusters: n >= EstablishedClusters, Posterior: p005 >= EstablishedPosterior, UpClusters: len(up), DownClusters: len(down)}
	if n >= 2 {
		first, second := mean(means[:n/2]), mean(means[n/2:])
		gates.FirstHalfMeanZ, gates.SecondHalfMeanZ = &first, &second
		gates.HalvesAgree = sameSign(first, second)
	}
	if len(up) > 0 && len(down) > 0 {
		upMean, downMean := mean(up), mean(down)
		gates.UpMarketMeanZ, gates.DownMarketMeanZ = &upMean, &downMean
		gates.MarketsAgree = sameSign(upMean, downMean)
	}
	out.Gates = gates
	if !gated {
		return out
	}
	switch {
	case gates.EValue && gates.Clusters && gates.Posterior && gates.HalvesAgree && gates.MarketsAgree:
		out.Gate = rpc.ScorecardGateEstablished
	case n >= FutileClusters && p010 < FutilePosterior:
		out.Gate = rpc.ScorecardGateFutile
	default:
		out.Gate = rpc.ScorecardGateAccruing
	}
	return out
}

func sameSign(a, b float64) bool {
	return a != 0 && b != 0 && math.Signbit(a) == math.Signbit(b)
}

// applyHolm corrects the sleeves' p-values (the 1/E bound) across live
// sleeves' entries streams at the primary horizon; with one sleeve nothing
// is adjusted.
func applyHolm(sleeves []rpc.ScorecardSleeve, h int) {
	var idx []int
	var p []float64
	for i := range sleeves {
		if sleeves[i].Horizon != h || !sleeves[i].Gated || sleeves[i].Clusters == 0 {
			continue
		}
		idx = append(idx, i)
		value := 1.0
		if sleeves[i].EValue > 0 {
			value = math.Min(1, 1/sleeves[i].EValue)
		}
		p = append(p, value)
	}
	if len(idx) < 2 {
		return
	}
	adjusted := Holm(p)
	for k, i := range idx {
		value := adjusted[k]
		sleeves[i].HolmAdjustedP = &value
	}
}

func publicDecision(d Decision, entry rpc.ScorecardEntry, scores []rpc.ScorecardHorizonScore) rpc.ScorecardDecision {
	out := rpc.ScorecardDecision{
		ID: d.ID, Sleeve: d.Sleeve, Stream: firstNonEmpty(d.Stream, StreamOf(d.Action)), Origin: d.Origin, StrategyRef: d.StrategyRef, Underlying: d.Underlying, Symbol: d.Symbol, Instrument: d.Instrument,
		Action: d.Action, Side: d.Side, Sign: d.Sign, Quantity: d.Quantity, FillPrice: d.FillPrice, Currency: d.Currency, NotionalBase: d.NotionalBase,
		ExecutedAt: d.ExecutedAt, ClusterKey: d.ClusterKey, Entry: entry, Scores: scores,
	}
	if d.Option != nil {
		out.Option = &rpc.ScorecardOptionContract{Expiry: d.Option.Expiry, Strike: d.Option.Strike, Right: d.Option.Right, Multiplier: d.Option.Multiplier}
	}
	return out
}

func defaultMethod() rpc.ScorecardMethod {
	return rpc.ScorecardMethod{
		Direction:           "z = sign × (r_u − β·r_SPY) / (σ_idio·√h): log returns from the execution-session close to the close h sessions later, so the intraday move after the fill is excluded; sign is +1 for long exposure and −1 for short (a long put or short call is −1).",
		Lookback:            "β and σ_idio from an OLS of daily log returns on SPY over the 252 sessions ending the session before entry; at least 60 sessions; nothing after entry enters.",
		Streams:             "Entries (open, add) ask whether the name moves with the decision and carry the gates and the Holm correction; exits (trim, exit) ask whether it moves against the position afterwards and are reported with the same statistics, never gated. Fills Canary's rules or the broker's lifecycle produced are the rule sleeve and count for no owner stream.",
		Clusters:            "Decisions in one sleeve, stream and ISO entry week form one cluster that counts once through its mean z.",
		Posterior:           "Posterior on IC with prior N(0, 0.1²) and unit variance per cluster mean: P(IC > 0.05) and P(IC > 0.10).",
		EValue:              "Mixture-SPRT e-value E_n = (1+nτ²)^(−1/2)·exp(τ²S_n²/(2(1+nτ²))), τ = 0.2, reported only while S_n > 0, else 0 (against).",
		Gates:               "established: E ≥ 100, n ≥ 100 clusters, P(IC > 0.05) ≥ 0.95, and the same sign of mean z in both chronological halves and in up- and down-market clusters; futile: n ≥ 60 and P(IC > 0.10) < 0.05; otherwise accruing. Holm correction across sleeves when more than one is live.",
		Wrapper:             "Option P&L to the horizon, or to the exit or lifecycle event when earlier, minus the P&L of a static delta-equivalent stock position (delta at entry × multiplier × signed quantity), in base currency at execution FX; the option value is a statement mark on the horizon day, the closing fill, zero at expiration or intrinsic at exercise/assignment, never interpolated.",
		RealisedVsImplied:   "|r_u| / (IV·√(h/252)), IV inverted from the fill by Black-Scholes at the execution-session close.",
		Slippage:            "Unknown: no quote at the fill is retained (markouts are read 30 minutes and one close after the fill).",
		RiskFreeRate:        RiskFreeRate,
		DividendYield:       DividendYield,
		NoCausalClaim:       true,
		NotInvestmentAdvice: true,
	}
}
