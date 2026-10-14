package cli

import (
	"time"

	"github.com/osauer/canary/v2/internal/rpc"
)

// goldenScorecard is a synthetic accruing scorecard: one owner sleeve with
// its entries and exits streams and an index entries stream (its empty index
// exits stream is not rendered), one rule-originated fill, stock, option and
// index decisions, and a few unknowns.
func goldenScorecard() rpc.ScorecardResult {
	at := goldenAt
	entryDay := time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)
	horizonDay := entryDay.AddDate(0, 0, 14)
	entries := func(h int) rpc.ScorecardSleeve {
		return rpc.ScorecardSleeve{
			Sleeve: "owner", Stream: rpc.ScorecardStreamEntries, Horizon: h, Gated: true, Decisions: 14, Scored: 11, Clusters: 7, MeanZ: new(0.31), SumZ: 2.17,
			PosteriorMean: new(0.020), PosteriorSD: new(0.097), PAbove005: new(0.38), PAbove010: new(0.20),
			EValue: 1.2, Gate: rpc.ScorecardGateAccruing,
			Gates:               rpc.ScorecardGates{FirstHalfMeanZ: new(0.45), SecondHalfMeanZ: new(0.20), HalvesAgree: true, UpMarketMeanZ: new(0.52), DownMarketMeanZ: new(-0.10), UpClusters: 4, DownClusters: 3},
			ClustersToEstablish: new(136),
			Wrapper:             &rpc.ScorecardWrapperSummary{Count: 5, MeanBase: -412.5, CILow: -1280.0, CIHigh: 310.0},
			RealisedVsImplied:   &rpc.ScorecardRealisedVsImplied{Count: 5, Median: 0.84},
			Unknown:             map[string]int{"immature": 2, "history_unavailable": 1},
		}
	}
	exits := func(h int) rpc.ScorecardSleeve {
		return rpc.ScorecardSleeve{
			Sleeve: "owner", Stream: rpc.ScorecardStreamExits, Horizon: h, Decisions: 7, Scored: 5, Clusters: 4, MeanZ: new(-0.12), SumZ: -0.48,
			PosteriorMean: new(-0.005), PosteriorSD: new(0.098), PAbove005: new(0.29), PAbove010: new(0.14),
			EValueAgainst: true,
			Gates:         rpc.ScorecardGates{FirstHalfMeanZ: new(-0.30), SecondHalfMeanZ: new(0.06), UpMarketMeanZ: new(-0.20), DownMarketMeanZ: new(0.04), UpClusters: 2, DownClusters: 2},
			Unknown:       map[string]int{"immature": 2},
		}
	}
	indexEntries := func(h int) rpc.ScorecardSleeve {
		return rpc.ScorecardSleeve{
			Sleeve: "owner", Stream: rpc.ScorecardStreamIndexEntries, Horizon: h, Decisions: 4, Scored: 3, Clusters: 3, MeanZ: new(0.42), SumZ: 1.26,
			PosteriorMean: new(0.012), PosteriorSD: new(0.099), PAbove005: new(0.35), PAbove010: new(0.18),
			EValue:                   1.03,
			Gates:                    rpc.ScorecardGates{FirstHalfMeanZ: new(0.90), SecondHalfMeanZ: new(0.18), HalvesAgree: true, UpMarketMeanZ: new(-0.35), DownMarketMeanZ: new(1.19), UpClusters: 2, DownClusters: 1},
			Wrapper:                  &rpc.ScorecardWrapperSummary{Count: 3, MeanBase: -220.0, CILow: -610.0, CIHigh: 140.0},
			RealisedVsImplied:        &rpc.ScorecardRealisedVsImplied{Count: 3, Median: 0.71},
			LongPutRealisedVsImplied: &rpc.ScorecardRealisedVsImplied{Count: 2, Median: 0.63},
			Unknown:                  map[string]int{"immature": 1},
		}
	}
	indexExits := func(h int) rpc.ScorecardSleeve {
		return rpc.ScorecardSleeve{Sleeve: "owner", Stream: rpc.ScorecardStreamIndexExits, Horizon: h, EValueAgainst: true, Unknown: map[string]int{}}
	}
	return rpc.ScorecardResult{
		SchemaVersion: "canary-scorecard-v1", State: rpc.ScorecardStateCurrent, AsOf: at, EvidenceTo: time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC), BaseCurrency: "EUR",
		PrimaryHorizon: 10, Horizons: []int{5, 10, 21},
		Sleeves: []rpc.ScorecardSleeve{entries(5), exits(5), indexEntries(5), indexExits(5), entries(10), exits(10), indexEntries(10), indexExits(10), entries(21), exits(21), indexEntries(21), indexExits(21)},
		Decisions: []rpc.ScorecardDecision{
			{ID: "decision_4e5f607182930a1b", Sleeve: "owner", Stream: rpc.ScorecardStreamIndexEntries, Underlying: "SPY", Symbol: "SPY 260930P00600000", Instrument: "option", Option: &rpc.ScorecardOptionContract{Expiry: "2026-09-30", Strike: 600, Right: "put", Multiplier: 100},
				Action: "open", Side: "buy", Sign: -1, Quantity: 2, FillPrice: new(6.10), Currency: "USD", NotionalBase: new(1098.0), ExecutedAt: entryDay.Add(3*24*time.Hour + 15*time.Hour), ClusterKey: "owner:2026-W35",
				Entry:  rpc.ScorecardEntry{SessionDay: new(entryDay.AddDate(0, 0, 3)), UnderlyingClose: new(641.2), MarketRSquared: new(1.0), IndexBasis: "list", ImpliedVol: new(0.17), Delta: new(-0.21)},
				Scores: []rpc.ScorecardHorizonScore{{Sessions: 5, Matured: true, Z: new(-0.35), TotalVolDaily: new(0.009)}, {Sessions: 10, Matured: true, Z: new(-0.48), TotalVolDaily: new(0.009), RealisedVsImplied: new(0.52), WrapperBase: new(-310.4)}, {Sessions: 21, Reason: "immature"}}},
			{ID: "decision_3d4e5f6071829304", Sleeve: "rule", Stream: rpc.ScorecardStreamExits, Origin: "proposal:trailing_stop", Underlying: "SYND", Symbol: "SYND", Instrument: "stock",
				Action: "exit", Side: "sell", Sign: -1, Quantity: 25, FillPrice: new(31.40), Currency: "USD", NotionalBase: new(706.5), ExecutedAt: entryDay.Add(2 * 24 * time.Hour), ClusterKey: "rule:2026-W35",
				Entry:  rpc.ScorecardEntry{SessionDay: new(entryDay.AddDate(0, 0, 2)), UnderlyingClose: new(31.2)},
				Scores: []rpc.ScorecardHorizonScore{{Sessions: 5, Matured: true, Z: new(0.4)}, {Sessions: 10, Matured: true, Z: new(0.9), RVIReason: "not_option", WrapperReason: "not_option"}, {Sessions: 21, Reason: "immature"}}},
			{ID: "decision_0a1b2c3d4e5f6071", Sleeve: "owner", Stream: rpc.ScorecardStreamEntries, Underlying: "SYNA", Symbol: "SYNA 260918C00120000", Instrument: "option", Option: &rpc.ScorecardOptionContract{Expiry: "2026-09-18", Strike: 120, Right: "call", Multiplier: 100},
				Action: "open", Side: "buy", Sign: 1, Quantity: 3, FillPrice: new(4.20), Currency: "USD", NotionalBase: new(1134.0), ExecutedAt: entryDay.Add(15 * time.Hour), ClusterKey: "owner:2026-W35",
				Entry:  rpc.ScorecardEntry{SessionDay: &entryDay, UnderlyingClose: new(118.4), ImpliedVol: new(0.42), Delta: new(0.47)},
				Scores: []rpc.ScorecardHorizonScore{{Sessions: 5, Matured: true, Z: new(0.8)}, {Sessions: 10, Matured: true, HorizonDay: &horizonDay, HorizonClose: new(124.1), Z: new(1.12), RealisedVsImplied: new(0.56), WrapperBase: new(-96.3)}, {Sessions: 21, Reason: "immature"}}},
			{ID: "decision_1b2c3d4e5f607182", Sleeve: "owner", Stream: rpc.ScorecardStreamExits, Underlying: "SYNB", Symbol: "SYNB", Instrument: "stock",
				Action: "trim", Side: "sell", Sign: -1, Quantity: 40, FillPrice: new(52.10), Currency: "USD", NotionalBase: new(1875.6), ExecutedAt: entryDay.Add(-3 * 24 * time.Hour), ClusterKey: "owner:2026-W34",
				Entry:  rpc.ScorecardEntry{SessionDay: new(entryDay.AddDate(0, 0, -3)), UnderlyingClose: new(51.9)},
				Scores: []rpc.ScorecardHorizonScore{{Sessions: 5, Matured: true, Z: new(-0.3)}, {Sessions: 10, Matured: true, Z: new(-0.62), RVIReason: "not_option", WrapperReason: "not_option"}, {Sessions: 21, Matured: true, Z: new(0.1)}}},
			{ID: "decision_2c3d4e5f60718293", Sleeve: "owner", Stream: rpc.ScorecardStreamEntries, Underlying: "SYNC", Symbol: "SYNC", Instrument: "stock",
				Action: "open", Side: "buy", Sign: 1, Quantity: 15, FillPrice: new(210.0), Currency: "USD", ExecutedAt: entryDay.Add(-10 * 24 * time.Hour), ClusterKey: "owner:2026-W33",
				Entry:  rpc.ScorecardEntry{Reason: "history_unavailable"},
				Scores: []rpc.ScorecardHorizonScore{{Sessions: 5, Reason: "history_unavailable"}, {Sessions: 10, Reason: "history_unavailable"}, {Sessions: 21, Reason: "history_unavailable"}}},
		},
		DecisionsTotal: 28,
		Coverage:       rpc.ScorecardCoverage{Ledger: 28, Stocks: 15, Options: 13, Entries: 14, Exits: 7, IndexEntries: 4, RuleOriginated: map[string]int{"proposal:trailing_stop": 2, "lifecycle": 1}, Skipped: map[string]int{"unsupported_asset": 2}, ScoredByHorizon: map[int]int{5: 23, 10: 21, 21: 14}, UnknownByReason: map[string]int{"immature": 16, "history_unavailable": 6}},
		History:        rpc.ScorecardHistory{Names: 11, Missing: 1, MarketSymbol: "SPY", MarketBars: 530, LastFetchedAt: at.Add(-time.Hour)},
		NotExecution:   true,
	}
}

// goldenScorecardBackfilling is the state-only result before the first
// evaluation has history to score.
func goldenScorecardBackfilling() rpc.ScorecardResult {
	return rpc.ScorecardResult{
		SchemaVersion: "canary-scorecard-v1", State: rpc.ScorecardStateBackfilling, Reason: "history_backfill_pending", AsOf: goldenAt,
		PrimaryHorizon: 10, Horizons: []int{5, 10, 21}, Sleeves: []rpc.ScorecardSleeve{}, Decisions: []rpc.ScorecardDecision{},
		Coverage: rpc.ScorecardCoverage{RuleOriginated: map[string]int{}, Skipped: map[string]int{}, ScoredByHorizon: map[int]int{}, UnknownByReason: map[string]int{}},
		History:  rpc.ScorecardHistory{MarketSymbol: "SPY"}, NotExecution: true,
	}
}
