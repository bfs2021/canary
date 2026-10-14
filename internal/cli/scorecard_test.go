package cli

import (
	"time"

	"github.com/osauer/canary/v2/internal/rpc"
)

// goldenScorecard is a synthetic accruing scorecard: one owner sleeve with
// a small sample, one stock and one option decision, and a few unknowns.
func goldenScorecard() rpc.ScorecardResult {
	at := goldenAt
	entryDay := time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)
	horizonDay := entryDay.AddDate(0, 0, 14)
	sleeve := func(h int) rpc.ScorecardSleeve {
		return rpc.ScorecardSleeve{
			Sleeve: "owner", Horizon: h, Decisions: 24, Scored: 18, Clusters: 9, MeanZ: new(0.31), SumZ: 2.79,
			PosteriorMean: new(0.026), PosteriorSD: new(0.096), PAbove005: new(0.40), PAbove010: new(0.22),
			EValue: 1.4, Gate: rpc.ScorecardGateAccruing,
			Gates:               rpc.ScorecardGates{FirstHalfMeanZ: new(0.45), SecondHalfMeanZ: new(0.20), HalvesAgree: true, UpMarketMeanZ: new(0.52), DownMarketMeanZ: new(-0.10), UpClusters: 6, DownClusters: 3},
			ClustersToEstablish: new(136),
			Wrapper:             &rpc.ScorecardWrapperSummary{Count: 7, MeanBase: -412.5, CILow: -1280.0, CIHigh: 310.0},
			RealisedVsImplied:   &rpc.ScorecardRealisedVsImplied{Count: 7, Median: 0.84},
			Unknown:             map[string]int{"immature": 4, "history_unavailable": 2},
		}
	}
	return rpc.ScorecardResult{
		SchemaVersion: "canary-scorecard-v1", State: rpc.ScorecardStateCurrent, AsOf: at, EvidenceTo: time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC), BaseCurrency: "EUR",
		PrimaryHorizon: 10, Horizons: []int{5, 10, 21},
		Sleeves: []rpc.ScorecardSleeve{sleeve(5), sleeve(10), sleeve(21)},
		Decisions: []rpc.ScorecardDecision{
			{ID: "decision_0a1b2c3d4e5f6071", Sleeve: "owner", Underlying: "SYNA", Symbol: "SYNA 260918C00120000", Instrument: "option", Option: &rpc.ScorecardOptionContract{Expiry: "2026-09-18", Strike: 120, Right: "call", Multiplier: 100},
				Action: "open", Side: "buy", Sign: 1, Quantity: 3, FillPrice: new(4.20), Currency: "USD", NotionalBase: new(1134.0), ExecutedAt: entryDay.Add(15 * time.Hour), ClusterKey: "owner:2026-W35",
				Entry:  rpc.ScorecardEntry{SessionDay: &entryDay, UnderlyingClose: new(118.4), ImpliedVol: new(0.42), Delta: new(0.47)},
				Scores: []rpc.ScorecardHorizonScore{{Sessions: 5, Matured: true, Z: new(0.8)}, {Sessions: 10, Matured: true, HorizonDay: &horizonDay, HorizonClose: new(124.1), Z: new(1.12), RealisedVsImplied: new(0.56), WrapperBase: new(-96.3)}, {Sessions: 21, Reason: "immature"}}},
			{ID: "decision_1b2c3d4e5f607182", Sleeve: "owner", Underlying: "SYNB", Symbol: "SYNB", Instrument: "stock",
				Action: "trim", Side: "sell", Sign: -1, Quantity: 40, FillPrice: new(52.10), Currency: "USD", NotionalBase: new(1875.6), ExecutedAt: entryDay.Add(-3 * 24 * time.Hour), ClusterKey: "owner:2026-W34",
				Entry:  rpc.ScorecardEntry{SessionDay: new(entryDay.AddDate(0, 0, -3)), UnderlyingClose: new(51.9)},
				Scores: []rpc.ScorecardHorizonScore{{Sessions: 5, Matured: true, Z: new(-0.3)}, {Sessions: 10, Matured: true, Z: new(-0.62), RVIReason: "not_option", WrapperReason: "not_option"}, {Sessions: 21, Matured: true, Z: new(0.1)}}},
			{ID: "decision_2c3d4e5f60718293", Sleeve: "owner", Underlying: "SYNC", Symbol: "SYNC", Instrument: "stock",
				Action: "open", Side: "buy", Sign: 1, Quantity: 15, FillPrice: new(210.0), Currency: "USD", ExecutedAt: entryDay.Add(-10 * 24 * time.Hour), ClusterKey: "owner:2026-W33",
				Entry:  rpc.ScorecardEntry{Reason: "history_unavailable"},
				Scores: []rpc.ScorecardHorizonScore{{Sessions: 5, Reason: "history_unavailable"}, {Sessions: 10, Reason: "history_unavailable"}, {Sessions: 21, Reason: "history_unavailable"}}},
		},
		DecisionsTotal: 24,
		Coverage:       rpc.ScorecardCoverage{Ledger: 24, Stocks: 15, Options: 9, Skipped: map[string]int{"unsupported_asset": 2}, ScoredByHorizon: map[int]int{5: 20, 10: 18, 21: 14}, UnknownByReason: map[string]int{"immature": 14, "history_unavailable": 6}},
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
		Coverage: rpc.ScorecardCoverage{Skipped: map[string]int{}, ScoredByHorizon: map[int]int{}, UnknownByReason: map[string]int{}},
		History:  rpc.ScorecardHistory{MarketSymbol: "SPY"}, NotExecution: true,
	}
}
