package cli

import (
	"time"

	"github.com/osauer/canary/v2/internal/rpc"
)

// goldenScorecard is a synthetic accruing scorecard: one owner sleeve with
// its entries and exits streams and an index entries stream (its empty index
// exits stream is not rendered), one rule-originated fill, stock, option and
// index decisions, a few unknowns, and one decision with a measured
// half-spread beside the fills before quote capture and one the daemon
// never observed.
func goldenScorecard() rpc.ScorecardResult {
	at := goldenAt
	entryDay := time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)
	horizonDay := entryDay.AddDate(0, 0, 14)
	entries := func(h int) rpc.ScorecardSleeve {
		return rpc.ScorecardSleeve{
			Sleeve: "owner", Stream: rpc.ScorecardStreamEntries, Horizon: h, Gated: true, Decisions: 14, Scored: 11, SectorAdjusted: 8, Units: 9, Weeks: 7, EffectiveUnits: 7.4, Clusters: 9, DirectionalScore: new(0.31), MeanZ: new(0.31), SumZ: 2.79,
			PosteriorMean: new(0.021), PosteriorSD: new(0.096), PScoreAbove0: new(0.59), PScoreAbove005: new(0.38), PScoreAbove010: new(0.20), PAbove005: new(0.38), PAbove010: new(0.20),
			EValue: 1.2, FutilityEValue: 0.9, Gate: rpc.ScorecardGateAccruing,
			Gates:            rpc.ScorecardGates{Weeks: false, Placebo: true, FirstHalfMeanZ: new(0.45), SecondHalfMeanZ: new(0.20), HalvesAgree: true, UpMarketMeanZ: new(0.52), DownMarketMeanZ: new(-0.10), UpUnits: 5, DownUnits: 4, UpClusters: 5, DownClusters: 4},
			UnitsToEstablish: new(136), WeeksToEstablish: new(129), ClustersToEstablish: new(136),
			Placebo:           &rpc.ScorecardPlacebo{Replicates: 200, Established: 0, Rate: 0, StandardisedSD: 0.91},
			Slippage:          &rpc.ScorecardSlippageSummary{Decisions: 14, Quoted: 3, HalfSpreadBase: 41.3, MeanBp: new(6.2), Unquoted: map[string]int{"no_fill_quote": 10, "execution_not_observed": 1}},
			Wrapper:           &rpc.ScorecardWrapperSummary{Count: 5, MeanBase: -412.5, CILow: -1280.0, CIHigh: 310.0},
			RealisedVsImplied: &rpc.ScorecardRealisedVsImplied{Count: 5, Median: 0.84},
			Unknown:           map[string]int{"immature": 2, "history_unavailable": 1},
		}
	}
	exits := func(h int) rpc.ScorecardSleeve {
		return rpc.ScorecardSleeve{
			Sleeve: "owner", Stream: rpc.ScorecardStreamExits, Horizon: h, Decisions: 7, Scored: 5, SectorAdjusted: 4, Units: 4, Weeks: 4, EffectiveUnits: 3.6, Clusters: 4, DirectionalScore: new(-0.12), MeanZ: new(-0.12), SumZ: -0.48,
			PosteriorMean: new(-0.005), PosteriorSD: new(0.098), PScoreAbove0: new(0.48), PScoreAbove005: new(0.29), PScoreAbove010: new(0.14), PAbove005: new(0.29), PAbove010: new(0.14),
			EValueAgainst: true, FutilityEValue: 1.1,
			Gates:    rpc.ScorecardGates{Placebo: true, FirstHalfMeanZ: new(-0.30), SecondHalfMeanZ: new(0.06), UpMarketMeanZ: new(-0.20), DownMarketMeanZ: new(0.04), UpUnits: 2, DownUnits: 2, UpClusters: 2, DownClusters: 2},
			Placebo:  &rpc.ScorecardPlacebo{Replicates: 200, Established: 0, Rate: 0, StandardisedSD: 0.88},
			Slippage: &rpc.ScorecardSlippageSummary{Decisions: 7, Quoted: 0, Unquoted: map[string]int{"no_fill_quote": 7}},
			Unknown:  map[string]int{"immature": 2},
		}
	}
	indexEntries := func(h int) rpc.ScorecardSleeve {
		return rpc.ScorecardSleeve{
			Sleeve: "owner", Stream: rpc.ScorecardStreamIndexEntries, Horizon: h, Decisions: 4, Scored: 3, Units: 3, Weeks: 3, EffectiveUnits: 2.1, Clusters: 3, DirectionalScore: new(0.42), MeanZ: new(0.42), SumZ: 1.26,
			PosteriorMean: new(0.012), PosteriorSD: new(0.099), PScoreAbove0: new(0.55), PScoreAbove005: new(0.35), PScoreAbove010: new(0.18), PAbove005: new(0.35), PAbove010: new(0.18),
			EValue: 1.03, FutilityEValue: 0.95,
			Gates:                    rpc.ScorecardGates{Placebo: true, FirstHalfMeanZ: new(0.90), SecondHalfMeanZ: new(0.18), HalvesAgree: true, UpMarketMeanZ: new(-0.35), DownMarketMeanZ: new(1.19), UpUnits: 2, DownUnits: 1, UpClusters: 2, DownClusters: 1},
			Placebo:                  &rpc.ScorecardPlacebo{Replicates: 200, Established: 0, Rate: 0, StandardisedSD: 0.97},
			Slippage:                 &rpc.ScorecardSlippageSummary{Decisions: 4, Quoted: 1, HalfSpreadBase: 12.6, MeanBp: new(4.1), Unquoted: map[string]int{"no_fill_quote": 3}},
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
		SchemaVersion: "canary-scorecard-v2", State: rpc.ScorecardStateCurrent, AsOf: at, EvidenceTo: time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC), BaseCurrency: "EUR",
		PrimaryHorizon: 10, Horizons: []int{5, 10, 21},
		Sleeves: []rpc.ScorecardSleeve{entries(5), exits(5), indexEntries(5), indexExits(5), entries(10), exits(10), indexEntries(10), indexExits(10), entries(21), exits(21), indexEntries(21), indexExits(21)},
		Decisions: []rpc.ScorecardDecision{
			{ID: "decision_4e5f607182930a1b", Sleeve: "owner", Stream: rpc.ScorecardStreamIndexEntries, Underlying: "SPY", Symbol: "SPY 260930P00600000", Instrument: "option", Option: &rpc.ScorecardOptionContract{Expiry: "2026-09-30", Strike: 600, Right: "put", Multiplier: 100},
				Action: "open", Side: "buy", Sign: -1, Quantity: 2, FillPrice: new(6.10), Currency: "USD", NotionalBase: new(1098.0), ExecutedAt: entryDay.Add(3*24*time.Hour + 15*time.Hour), ClusterKey: "owner:2026-W35", UnitKey: "owner:2026-W35:756733",
				Entry:    rpc.ScorecardEntry{SessionDay: new(entryDay.AddDate(0, 0, 3)), UnderlyingClose: new(641.2), MarketRSquared: new(1.0), IndexBasis: "list", ImpliedVol: new(0.17), Delta: new(-0.21)},
				Scores:   []rpc.ScorecardHorizonScore{{Sessions: 5, Matured: true, Z: new(-0.35), TotalVolDaily: new(0.009), SlippageBp: new(4.1)}, {Sessions: 10, Matured: true, Z: new(-0.48), TotalVolDaily: new(0.009), RealisedVsImplied: new(0.52), WrapperBase: new(-310.4), SlippageBp: new(4.1)}, {Sessions: 21, Reason: "immature", SlippageBp: new(4.1)}},
				Slippage: &rpc.ScorecardSlippage{Status: "measured", Match: "exec_id", Fills: 1, Quoted: 1, HalfSpread: new(5.0), Currency: "USD", HalfSpreadBase: new(4.3), Bp: new(4.1), GapMS: new(int64(2600))}},
			{ID: "decision_3d4e5f6071829304", Sleeve: "rule", Stream: rpc.ScorecardStreamExits, Origin: "proposal:trailing_stop", Underlying: "SYND", Symbol: "SYND", Instrument: "stock",
				Action: "exit", Side: "sell", Sign: -1, Quantity: 25, FillPrice: new(31.40), Currency: "USD", NotionalBase: new(706.5), ExecutedAt: entryDay.Add(2 * 24 * time.Hour), ClusterKey: "rule:2026-W35", UnitKey: "rule:2026-W35:4",
				Entry:    rpc.ScorecardEntry{SessionDay: new(entryDay.AddDate(0, 0, 2)), UnderlyingClose: new(31.2), MarketAdjustment: "spy", SectorReason: "sector_unknown"},
				Scores:   []rpc.ScorecardHorizonScore{{Sessions: 5, Matured: true, Z: new(0.4), SlippageReason: "no_fill_quote"}, {Sessions: 10, Matured: true, Z: new(0.9), RVIReason: "not_option", WrapperReason: "not_option", SlippageReason: "no_fill_quote"}, {Sessions: 21, Reason: "immature", SlippageReason: "no_fill_quote"}},
				Slippage: &rpc.ScorecardSlippage{Status: "unknown", Reason: "no_fill_quote"}},
			{ID: "decision_0a1b2c3d4e5f6071", Sleeve: "owner", Stream: rpc.ScorecardStreamEntries, Underlying: "SYNA", Symbol: "SYNA 260918C00120000", Instrument: "option", Option: &rpc.ScorecardOptionContract{Expiry: "2026-09-18", Strike: 120, Right: "call", Multiplier: 100},
				Action: "open", Side: "buy", Sign: 1, Quantity: 3, FillPrice: new(4.20), Currency: "USD", NotionalBase: new(1134.0), ExecutedAt: entryDay.Add(15 * time.Hour), ClusterKey: "owner:2026-W35", UnitKey: "owner:2026-W35:1",
				Entry:    rpc.ScorecardEntry{SessionDay: &entryDay, UnderlyingClose: new(118.4), MarketAdjustment: "spy_sector", Sector: "Information Technology", SectorFund: "XLK", ImpliedVol: new(0.42), Delta: new(0.47)},
				Scores:   []rpc.ScorecardHorizonScore{{Sessions: 5, Matured: true, Z: new(0.8), SectorBeta: new(1.2), SlippageReason: "execution_not_observed"}, {Sessions: 10, Matured: true, HorizonDay: &horizonDay, HorizonClose: new(124.1), Z: new(1.12), SectorBeta: new(1.2), SectorReturn: new(0.021), RealisedVsImplied: new(0.56), WrapperBase: new(-96.3), SlippageReason: "execution_not_observed"}, {Sessions: 21, Reason: "immature", SlippageReason: "execution_not_observed"}},
				Slippage: &rpc.ScorecardSlippage{Status: "unknown", Reason: "execution_not_observed"}},
			{ID: "decision_1b2c3d4e5f607182", Sleeve: "owner", Stream: rpc.ScorecardStreamExits, Underlying: "SYNB", Symbol: "SYNB", Instrument: "stock",
				Action: "trim", Side: "sell", Sign: -1, Quantity: 40, FillPrice: new(52.10), Currency: "USD", NotionalBase: new(1875.6), ExecutedAt: entryDay.Add(-3 * 24 * time.Hour), ClusterKey: "owner:2026-W34", UnitKey: "owner:2026-W34:2",
				Entry:    rpc.ScorecardEntry{SessionDay: new(entryDay.AddDate(0, 0, -3)), UnderlyingClose: new(51.9), MarketAdjustment: "spy_sector", Sector: "Health Care", SectorFund: "XLV"},
				Scores:   []rpc.ScorecardHorizonScore{{Sessions: 5, Matured: true, Z: new(-0.3), SlippageReason: "no_fill_quote"}, {Sessions: 10, Matured: true, Z: new(-0.62), RVIReason: "not_option", WrapperReason: "not_option", SlippageReason: "no_fill_quote"}, {Sessions: 21, Matured: true, Z: new(0.1), SlippageReason: "no_fill_quote"}},
				Slippage: &rpc.ScorecardSlippage{Status: "unknown", Reason: "no_fill_quote"}},
			{ID: "decision_2c3d4e5f60718293", Sleeve: "owner", Stream: rpc.ScorecardStreamEntries, Underlying: "SYNC", Symbol: "SYNC", Instrument: "stock",
				Action: "open", Side: "buy", Sign: 1, Quantity: 15, FillPrice: new(210.0), Currency: "USD", ExecutedAt: entryDay.Add(-10 * 24 * time.Hour), ClusterKey: "owner:2026-W33", UnitKey: "owner:2026-W33:3",
				Entry:    rpc.ScorecardEntry{Reason: "history_unavailable"},
				Scores:   []rpc.ScorecardHorizonScore{{Sessions: 5, Reason: "history_unavailable", SlippageReason: "no_fill_quote"}, {Sessions: 10, Reason: "history_unavailable", SlippageReason: "no_fill_quote"}, {Sessions: 21, Reason: "history_unavailable", SlippageReason: "no_fill_quote"}},
				Slippage: &rpc.ScorecardSlippage{Status: "unknown", Reason: "no_fill_quote"}},
		},
		DecisionsTotal: 28,
		Coverage:       rpc.ScorecardCoverage{Ledger: 28, Stocks: 15, Options: 13, Entries: 14, Exits: 7, IndexEntries: 4, RuleOriginated: map[string]int{"proposal:trailing_stop": 2, "lifecycle": 1}, Skipped: map[string]int{"unsupported_asset": 2}, ScoredByHorizon: map[int]int{5: 23, 10: 21, 21: 14}, UnknownByReason: map[string]int{"immature": 16, "history_unavailable": 6}, SlippageQuoted: 4, SlippageByReason: map[string]int{"no_fill_quote": 23, "execution_not_observed": 1}},
		History: rpc.ScorecardHistory{Names: 11, Missing: 1, MarketSymbol: "SPY", MarketBars: 530, SectorBars: map[string]int{"XLK": 530, "XLV": 530}, LastFetchedAt: at.Add(-time.Hour), Failures: []rpc.ScorecardHistoryFailure{
			{Symbol: "SPY", Bars: 530, Failure: "historical data timeout for SPY after 45s: context deadline exceeded", FailedAt: at.Add(-time.Hour), RetryAt: at.Add(time.Hour)},
			{Symbol: "SYNC", ConID: 3, Failure: "contract details unresolved for SYNC: no security definition has been found", FailedAt: at.Add(-3 * time.Hour), RetryAt: at.Add(5 * time.Hour)},
		}},
		NotExecution: true,
	}
}

// goldenScorecardBackfilling is the state-only result before the first
// evaluation has history to score.
func goldenScorecardBackfilling() rpc.ScorecardResult {
	return rpc.ScorecardResult{
		SchemaVersion: "canary-scorecard-v2", State: rpc.ScorecardStateBackfilling, Reason: "history_backfill_pending", AsOf: goldenAt,
		PrimaryHorizon: 10, Horizons: []int{5, 10, 21}, Sleeves: []rpc.ScorecardSleeve{}, Decisions: []rpc.ScorecardDecision{},
		Coverage: rpc.ScorecardCoverage{RuleOriginated: map[string]int{}, Skipped: map[string]int{}, ScoredByHorizon: map[int]int{}, UnknownByReason: map[string]int{}},
		History:  rpc.ScorecardHistory{MarketSymbol: "SPY"}, NotExecution: true,
	}
}
