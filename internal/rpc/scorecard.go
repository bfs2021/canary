package rpc

import (
	"fmt"
	"strings"
	"time"
)

// MethodScorecardRead serves the daemon's decision scorecard: the ledger of
// position changes per sleeve with their per-decision scores and the
// sequential evidence per sleeve. The daemon evaluates; readers fetch.
const MethodScorecardRead = "scorecard.read"

// Scorecard states and evidence gates.
const (
	ScorecardStateCurrent     = "current"
	ScorecardStateBackfilling = "backfilling"
	ScorecardStateUnavailable = "unavailable"

	ScorecardGateEstablished = "established"
	ScorecardGateFutile      = "futile"
	ScorecardGateAccruing    = "accruing"

	// ScorecardStreamEntries and ScorecardStreamExits separate two skills:
	// entries (open, add) ask whether the name moves with the decision and
	// carry the gates; exits (trim, exit) ask whether it moves against the
	// position afterwards and are reported, never gated.
	ScorecardStreamEntries = "entries"
	ScorecardStreamExits   = "exits"
	// ScorecardStreamIndexEntries and ScorecardStreamIndexExits hold the
	// decisions on a broad index underlying: market timing and hedges, a
	// different skill from single-name selection. Scored on the index's own
	// total volatility, reported, never gated.
	ScorecardStreamIndexEntries = "index_entries"
	ScorecardStreamIndexExits   = "index_exits"

	// ScorecardIndexBasisList and ScorecardIndexBasisRSquared say why a
	// decision's underlying counts as an index: it is on the fixed list of
	// broad index products, or its pre-entry regression on the market proxy
	// fits with R² at or above the index threshold.
	ScorecardIndexBasisList     = "list"
	ScorecardIndexBasisRSquared = "r_squared"

	// ScorecardSleeveRule is the sleeve of fills Canary's own rules or the
	// broker's lifecycle produced; they never count for an owner stream.
	ScorecardSleeveRule = "rule"
	// ScorecardSleeveCore is the sleeve of the core's own rows
	// (core_rebalance): reported as the core's, never as the owner's
	// decisions (core-sleeve.md, review S1).
	ScorecardSleeveCore = "core"

	// MaxScorecardDecisions bounds the decisions one read returns.
	MaxScorecardDecisions = 2000
	// DefaultScorecardDecisions is the decision page when the caller names
	// no limit.
	DefaultScorecardDecisions = 50
)

// ScorecardReadParams selects one sleeve and bounds the decision list.
type ScorecardReadParams struct {
	Sleeve string `json:"sleeve,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

// NormalizeScorecardReadParams is the one input contract shared by CLI, MCP
// and the daemon.
func NormalizeScorecardReadParams(in ScorecardReadParams) (ScorecardReadParams, error) {
	out := in
	out.Sleeve = strings.ToLower(strings.TrimSpace(out.Sleeve))
	if len(out.Sleeve) > 64 {
		return ScorecardReadParams{}, fmt.Errorf("scorecard sleeve name is too long")
	}
	if out.Limit == 0 {
		out.Limit = DefaultScorecardDecisions
	}
	if out.Limit < 1 || out.Limit > MaxScorecardDecisions {
		return ScorecardReadParams{}, fmt.Errorf("scorecard limit must be between 1 and %d", MaxScorecardDecisions)
	}
	return out, nil
}

// ScorecardResult is the daemon's decision scorecard. It carries no account,
// order, execution or statement identity: decisions are named by opaque ids.
type ScorecardResult struct {
	SchemaVersion string    `json:"schema_version"`
	State         string    `json:"state"`
	Reason        string    `json:"reason,omitempty"`
	AsOf          time.Time `json:"as_of,omitzero"`
	// EvidenceTo is the last statement day the ledger covers.
	EvidenceTo     time.Time           `json:"evidence_to,omitzero"`
	BaseCurrency   string              `json:"base_currency,omitempty"`
	PrimaryHorizon int                 `json:"primary_horizon"`
	Horizons       []int               `json:"horizons"`
	Sleeves        []ScorecardSleeve   `json:"sleeves"`
	Decisions      []ScorecardDecision `json:"decisions"`
	// DecisionsTotal is the number of decisions the read could have returned
	// before the limit.
	DecisionsTotal int               `json:"decisions_total"`
	Coverage       ScorecardCoverage `json:"coverage"`
	History        ScorecardHistory  `json:"history"`
	Method         ScorecardMethod   `json:"method"`
	NotExecution   bool              `json:"not_execution"`
}

// ScorecardSleeve is the sequential evidence for one sleeve at one horizon.
// Every figure carries its sample; a gate word never stands alone. The
// evidence unit is the name-week: one underlying in one ISO entry week;
// overlapping horizons shrink the units to an effective sample.
type ScorecardSleeve struct {
	Sleeve  string `json:"sleeve"`
	Stream  string `json:"stream"`
	Horizon int    `json:"horizon"`
	// Gated is true for the entries stream only; the exits and index streams
	// report the same statistics with no gate.
	Gated     bool `json:"gated"`
	Decisions int  `json:"decisions"`
	Scored    int  `json:"scored"`
	// SectorAdjusted counts the scored decisions whose z is net of the
	// name's sector fund as well as the market.
	SectorAdjusted int `json:"sector_adjusted"`
	// Units is the number of name-week units with a score; Weeks the
	// distinct entry weeks among them; EffectiveUnits the units after the
	// overlap covariance (n²/V). Clusters repeats Units under the key the
	// week-cluster gate used; deprecated, removed one release after v3.24.
	Units          int     `json:"units"`
	Weeks          int     `json:"weeks"`
	EffectiveUnits float64 `json:"effective_units"`
	Clusters       int     `json:"clusters"`
	// DirectionalScore is the mean z per unit: the measure the gates are
	// about. MeanZ repeats it under its old key (deprecated, one release).
	DirectionalScore *float64 `json:"directional_score,omitempty"`
	MeanZ            *float64 `json:"mean_z,omitempty"`
	SumZ             float64  `json:"sum_z"`
	// Posterior on the directional score with prior N(0, 0.1²) over the
	// effective sample: P(score > 0), P(score > 0.05) and P(score > 0.10).
	// PAbove005 and PAbove010 repeat the last two under the keys that named
	// the score "IC" (deprecated, one release).
	PosteriorMean  *float64 `json:"posterior_mean,omitempty"`
	PosteriorSD    *float64 `json:"posterior_sd,omitempty"`
	PScoreAbove0   *float64 `json:"p_score_above_0,omitempty"`
	PScoreAbove005 *float64 `json:"p_score_above_0_05,omitempty"`
	PScoreAbove010 *float64 `json:"p_score_above_0_10,omitempty"`
	PAbove005      *float64 `json:"p_ic_above_0_05,omitempty"`
	PAbove010      *float64 `json:"p_ic_above_0_10,omitempty"`
	// EValue is the mixture-SPRT e-value for information while the running
	// sum is positive; EValueAgainst is true when the sum is not positive
	// and the e-value is reported as 0. FutilityEValue is the likelihood
	// ratio of no information against the smallest score worth a risk
	// allocation; it grows while the decisions carry none.
	EValue         float64        `json:"e_value"`
	EValueAgainst  bool           `json:"e_value_against"`
	FutilityEValue float64        `json:"futility_e_value"`
	Gate           string         `json:"gate,omitempty"`
	Gates          ScorecardGates `json:"gates"`
	// UnitsToEstablish projects, at the current score per unit, how many
	// effective units the e-value gate needs; WeeksToEstablish converts
	// that at the sleeve's own pace of effective units per entry week.
	// ClustersToEstablish repeats UnitsToEstablish (deprecated, one
	// release). All nil when the score is not positive.
	UnitsToEstablish    *int                        `json:"units_to_establish,omitempty"`
	WeeksToEstablish    *int                        `json:"weeks_to_establish,omitempty"`
	ClustersToEstablish *int                        `json:"clusters_to_establish,omitempty"`
	Placebo             *ScorecardPlacebo           `json:"placebo,omitempty"`
	Slippage            *ScorecardSlippageSummary   `json:"slippage,omitempty"`
	Wrapper             *ScorecardWrapperSummary    `json:"wrapper,omitempty"`
	RealisedVsImplied   *ScorecardRealisedVsImplied `json:"realised_vs_implied,omitempty"`
	// LongPutRealisedVsImplied is the realised-vs-implied median of the
	// bought puts in an index entries stream: did the hedges realise the move
	// they paid for? Nil elsewhere.
	LongPutRealisedVsImplied *ScorecardRealisedVsImplied `json:"long_put_realised_vs_implied,omitempty"`
	// HolmAdjustedP is the Holm-corrected p-value (1/E bound) across live
	// sleeves; nil when one sleeve is live.
	HolmAdjustedP *float64       `json:"holm_adjusted_p,omitempty"`
	Unknown       map[string]int `json:"unknown"`
}

// ScorecardGates discloses each component of the established gate.
type ScorecardGates struct {
	EValue    bool `json:"e_value"`
	Units     bool `json:"units"`
	Weeks     bool `json:"weeks"`
	Posterior bool `json:"posterior"`
	// Placebo is true when no more than the allowed share of sign-shuffled
	// replicates reaches the statistical bar (or the sample is too small for
	// the bar at all).
	Placebo      bool `json:"placebo"`
	HalvesAgree  bool `json:"halves_agree"`
	MarketsAgree bool `json:"markets_agree"`
	// Clusters repeats Units (deprecated, one release).
	Clusters        bool     `json:"clusters"`
	FirstHalfMeanZ  *float64 `json:"first_half_mean_z,omitempty"`
	SecondHalfMeanZ *float64 `json:"second_half_mean_z,omitempty"`
	UpMarketMeanZ   *float64 `json:"up_market_mean_z,omitempty"`
	DownMarketMeanZ *float64 `json:"down_market_mean_z,omitempty"`
	UpUnits         int      `json:"up_units"`
	DownUnits       int      `json:"down_units"`
	// UpClusters and DownClusters repeat UpUnits and DownUnits (deprecated,
	// one release).
	UpClusters   int `json:"up_clusters"`
	DownClusters int `json:"down_clusters"`
}

// ScorecardPlacebo is the sign-shuffle placebo: the units' signs are
// redrawn at random Replicates times and the statistical bar (e-value,
// posterior and effective sample) is re-read each time. Established counts
// the replicates that reached it; StandardisedSD is the standard deviation
// of the shuffled standardised sums, about 1 when the overlap covariance is
// calibrated and below it when the covariance is conservative.
type ScorecardPlacebo struct {
	Replicates     int     `json:"replicates"`
	Established    int     `json:"established"`
	Rate           float64 `json:"rate"`
	StandardisedSD float64 `json:"standardised_sd"`
}

// ScorecardSlippageSummary sums the half-spread the stream's decisions paid
// from the quotes retained at their fills, in base currency, with the
// decisions that carried none by reason.
type ScorecardSlippageSummary struct {
	Decisions      int            `json:"decisions"`
	Quoted         int            `json:"quoted"`
	HalfSpreadBase float64        `json:"half_spread_base"`
	MeanBp         *float64       `json:"mean_bp,omitempty"`
	Unquoted       map[string]int `json:"unquoted"`
}

// ScorecardSlippage is the half-spread one decision paid: measured from
// the retained quote (Status measured) or unknown with the reason.
type ScorecardSlippage struct {
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
	// Match is exec_id when the statement's execution ids met retained
	// records, time when the retained fill on the same contract nearest in
	// time (within the daemon's tolerance) stood in.
	Match          string   `json:"match,omitempty"`
	Fills          int      `json:"fills,omitempty"`
	Quoted         int      `json:"quoted,omitempty"`
	HalfSpread     *float64 `json:"half_spread,omitempty"`
	Currency       string   `json:"currency,omitempty"`
	HalfSpreadBase *float64 `json:"half_spread_base,omitempty"`
	Bp             *float64 `json:"bp,omitempty"`
	GapMS          *int64   `json:"gap_ms,omitempty"`
}

// ScorecardWrapperSummary is the mean option wrapper P&L with a bootstrap
// interval, in base currency.
type ScorecardWrapperSummary struct {
	Count    int     `json:"count"`
	MeanBase float64 `json:"mean_base"`
	CILow    float64 `json:"ci_low"`
	CIHigh   float64 `json:"ci_high"`
}

// ScorecardRealisedVsImplied is the median realised move relative to the
// implied move at entry.
type ScorecardRealisedVsImplied struct {
	Count  int     `json:"count"`
	Median float64 `json:"median"`
}

// ScorecardDecision is one ledger decision with its entry facts and scores.
type ScorecardDecision struct {
	ID     string `json:"id"`
	Sleeve string `json:"sleeve"`
	// Stream is entries or exits, or index_entries or index_exits when the
	// underlying is a broad index (Entry.IndexBasis says why); Origin names
	// the rule or lifecycle source of a rule-sleeve fill.
	Stream      string                   `json:"stream"`
	Origin      string                   `json:"origin,omitempty"`
	StrategyRef string                   `json:"strategy_ref,omitempty"`
	Underlying  string                   `json:"underlying"`
	Symbol      string                   `json:"symbol"`
	Instrument  string                   `json:"instrument"`
	Option      *ScorecardOptionContract `json:"option,omitempty"`
	Action      string                   `json:"action"`
	Side        string                   `json:"side"`
	Sign        int                      `json:"sign"`
	Quantity    float64                  `json:"quantity"`
	FillPrice   *float64                 `json:"fill_price,omitempty"`
	Currency    string                   `json:"currency,omitempty"`
	// NotionalBase is the changed quantity at the fill price, in base
	// currency at execution FX.
	NotionalBase *float64  `json:"notional_base,omitempty"`
	ExecutedAt   time.Time `json:"executed_at"`
	// ClusterKey is the sleeve × entry-week key; UnitKey adds the
	// underlying, the name-week unit this decision's z contributes to.
	ClusterKey string                  `json:"cluster_key"`
	UnitKey    string                  `json:"unit_key"`
	Entry      ScorecardEntry          `json:"entry"`
	Scores     []ScorecardHorizonScore `json:"scores"`
	// Slippage is the half-spread paid at the fill, from the retained
	// quote; it does not depend on the horizon.
	Slippage *ScorecardSlippage `json:"slippage,omitempty"`
}

// ScorecardOptionContract identifies an option leg.
type ScorecardOptionContract struct {
	Expiry     string  `json:"expiry"`
	Strike     float64 `json:"strike"`
	Right      string  `json:"right"`
	Multiplier float64 `json:"multiplier"`
}

// ScorecardEntry carries the entry facts derived from history: the
// underlying close of the execution session, the fit of the pre-entry
// regression on the market proxy, the index classification and, for
// options, the implied volatility inverted from the fill and the delta at
// entry.
type ScorecardEntry struct {
	SessionDay      *time.Time `json:"session_day,omitempty"`
	UnderlyingClose *float64   `json:"underlying_close,omitempty"`
	// MarketRSquared is the R² of the pre-entry regression of the
	// underlying's daily log returns on the market proxy's.
	MarketRSquared *float64 `json:"market_r_squared,omitempty"`
	// IndexBasis is list or r_squared when the underlying is scored as an
	// index; empty for a single name.
	IndexBasis string `json:"index_basis,omitempty"`
	// MarketAdjustment is spy_sector when the single-name z is net of the
	// market and the name's sector fund (Sector, SectorFund), spy when only
	// the market could be removed (SectorReason says why).
	MarketAdjustment string   `json:"market_adjustment,omitempty"`
	Sector           string   `json:"sector,omitempty"`
	SectorFund       string   `json:"sector_fund,omitempty"`
	SectorReason     string   `json:"sector_reason,omitempty"`
	ImpliedVol       *float64 `json:"implied_vol,omitempty"`
	Delta            *float64 `json:"delta,omitempty"`
	Reason           string   `json:"reason,omitempty"`
}

// ScorecardHorizonScore is one decision's score at one horizon, or the
// reason it is unknown. A single-name z is scaled by IdioVolDaily, an index
// z by TotalVolDaily, the underlying's own pre-entry daily volatility.
type ScorecardHorizonScore struct {
	Sessions          int        `json:"sessions"`
	Matured           bool       `json:"matured"`
	Reason            string     `json:"reason,omitempty"`
	HorizonDay        *time.Time `json:"horizon_day,omitempty"`
	HorizonClose      *float64   `json:"horizon_close,omitempty"`
	UnderlyingReturn  *float64   `json:"underlying_return,omitempty"`
	MarketReturn      *float64   `json:"market_return,omitempty"`
	Beta              *float64   `json:"beta,omitempty"`
	SectorBeta        *float64   `json:"sector_beta,omitempty"`
	SectorReturn      *float64   `json:"sector_return,omitempty"`
	IdioVolDaily      *float64   `json:"idio_vol_daily,omitempty"`
	TotalVolDaily     *float64   `json:"total_vol_daily,omitempty"`
	Z                 *float64   `json:"z,omitempty"`
	RealisedVsImplied *float64   `json:"realised_vs_implied,omitempty"`
	RVIReason         string     `json:"realised_vs_implied_reason,omitempty"`
	WrapperBase       *float64   `json:"wrapper_base,omitempty"`
	WrapperReason     string     `json:"wrapper_reason,omitempty"`
	// SlippageBp and SlippageReason repeat the decision's Slippage at each
	// horizon (deprecated, one release): the half-spread does not depend on
	// the horizon.
	SlippageBp     *float64 `json:"slippage_bp,omitempty"`
	SlippageReason string   `json:"slippage_reason,omitempty"`
}

// ScorecardCoverage makes the ledger and its gaps explicit.
type ScorecardCoverage struct {
	Ledger  int `json:"ledger"`
	Stocks  int `json:"stocks"`
	Options int `json:"options"`
	Entries int `json:"entries"`
	Exits   int `json:"exits"`
	// IndexEntries and IndexExits count the owner-stream decisions on a
	// broad index underlying; they are not in Entries or Exits.
	IndexEntries int `json:"index_entries"`
	IndexExits   int `json:"index_exits"`
	// RuleOriginated counts the ledger rows Canary's rules or the broker's
	// lifecycle produced, by origin; they are in no owner stream.
	RuleOriginated  map[string]int `json:"rule_originated"`
	Skipped         map[string]int `json:"skipped"`
	ScoredByHorizon map[int]int    `json:"scored_by_horizon"`
	UnknownByReason map[string]int `json:"unknown_by_reason"`
	// SlippageQuoted counts the decisions with a measured half-spread;
	// SlippageByReason the rest by reason.
	SlippageQuoted   int            `json:"slippage_quoted"`
	SlippageByReason map[string]int `json:"slippage_by_reason"`
	// Superseded counts persisted ledger rows no longer in the current
	// derivation (restated statements); they stay in the audit record.
	Superseded int `json:"superseded"`
}

// ScorecardHistory reports the history cache behind the scores.
type ScorecardHistory struct {
	Names   int `json:"names"`
	Missing int `json:"missing"`
	Pending int `json:"pending"`
	// Failures lists every name whose last history read failed since its
	// last successful one, the market proxy first, then by symbol.
	Failures     []ScorecardHistoryFailure `json:"failures,omitempty"`
	MarketSymbol string                    `json:"market_symbol"`
	MarketBars   int                       `json:"market_bars"`
	// SectorBars is the sessions held per sector fund the ledger needs.
	SectorBars    map[string]int `json:"sector_bars,omitempty"`
	LastFetchedAt time.Time      `json:"last_fetched_at,omitzero"`
}

// ScorecardHistoryFailure is one name whose last history read failed since
// its last successful one: the broker's answer, when it came and when the
// daemon asks again. The closes of the last success, if any, stay behind the
// scores meanwhile.
type ScorecardHistoryFailure struct {
	Symbol string `json:"symbol"`
	// ConID is the exact contract read; 0 for the market proxy.
	ConID int64 `json:"conid,omitempty"`
	// Bars is how many closes the last successful read left behind.
	Bars     int       `json:"bars"`
	Failure  string    `json:"failure"`
	FailedAt time.Time `json:"failed_at"`
	RetryAt  time.Time `json:"retry_at"`
}

// ScorecardMethod travels with every result.
type ScorecardMethod struct {
	Direction        string `json:"direction"`
	DirectionalScore string `json:"directional_score"`
	Index            string `json:"index"`
	Streams          string `json:"streams"`
	Lookback         string `json:"lookback"`
	Units            string `json:"units"`
	Overlap          string `json:"overlap"`
	// Clusters repeats Units (deprecated, one release).
	Clusters            string  `json:"clusters"`
	Posterior           string  `json:"posterior"`
	EValue              string  `json:"e_value"`
	Futility            string  `json:"futility"`
	Placebo             string  `json:"placebo"`
	Gates               string  `json:"gates"`
	Wrapper             string  `json:"wrapper"`
	RealisedVsImplied   string  `json:"realised_vs_implied"`
	Slippage            string  `json:"slippage"`
	RiskFreeRate        float64 `json:"risk_free_rate"`
	DividendYield       float64 `json:"dividend_yield"`
	NoCausalClaim       bool    `json:"no_causal_claim"`
	NotInvestmentAdvice bool    `json:"not_investment_advice"`
}
