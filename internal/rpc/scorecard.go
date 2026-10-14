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
// Every figure carries its sample; a gate word never stands alone.
type ScorecardSleeve struct {
	Sleeve    string `json:"sleeve"`
	Horizon   int    `json:"horizon"`
	Decisions int    `json:"decisions"`
	Scored    int    `json:"scored"`
	// Clusters is the number of sleeve × entry-week clusters with a score.
	Clusters int      `json:"clusters"`
	MeanZ    *float64 `json:"mean_z,omitempty"`
	SumZ     float64  `json:"sum_z"`
	// Posterior on IC with prior N(0, 0.1²) and unit variance per cluster.
	PosteriorMean *float64 `json:"posterior_mean,omitempty"`
	PosteriorSD   *float64 `json:"posterior_sd,omitempty"`
	PAbove005     *float64 `json:"p_ic_above_0_05,omitempty"`
	PAbove010     *float64 `json:"p_ic_above_0_10,omitempty"`
	// EValue is the mixture-SPRT e-value while the running sum is positive;
	// EValueAgainst is true when the sum is not positive and the e-value is
	// reported as 0.
	EValue        float64        `json:"e_value"`
	EValueAgainst bool           `json:"e_value_against"`
	Gate          string         `json:"gate"`
	Gates         ScorecardGates `json:"gates"`
	// ClustersToEstablish projects, at the current mean z per cluster, how
	// many clusters the e-value gate needs; nil when the mean is not positive.
	ClustersToEstablish *int                        `json:"clusters_to_establish,omitempty"`
	Wrapper             *ScorecardWrapperSummary    `json:"wrapper,omitempty"`
	RealisedVsImplied   *ScorecardRealisedVsImplied `json:"realised_vs_implied,omitempty"`
	// HolmAdjustedP is the Holm-corrected p-value (1/E bound) across live
	// sleeves; nil when one sleeve is live.
	HolmAdjustedP *float64       `json:"holm_adjusted_p,omitempty"`
	Unknown       map[string]int `json:"unknown"`
}

// ScorecardGates discloses each component of the established gate.
type ScorecardGates struct {
	EValue          bool     `json:"e_value"`
	Clusters        bool     `json:"clusters"`
	Posterior       bool     `json:"posterior"`
	HalvesAgree     bool     `json:"halves_agree"`
	MarketsAgree    bool     `json:"markets_agree"`
	FirstHalfMeanZ  *float64 `json:"first_half_mean_z,omitempty"`
	SecondHalfMeanZ *float64 `json:"second_half_mean_z,omitempty"`
	UpMarketMeanZ   *float64 `json:"up_market_mean_z,omitempty"`
	DownMarketMeanZ *float64 `json:"down_market_mean_z,omitempty"`
	UpClusters      int      `json:"up_clusters"`
	DownClusters    int      `json:"down_clusters"`
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
	ID          string                   `json:"id"`
	Sleeve      string                   `json:"sleeve"`
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
	NotionalBase *float64                `json:"notional_base,omitempty"`
	ExecutedAt   time.Time               `json:"executed_at"`
	ClusterKey   string                  `json:"cluster_key"`
	Entry        ScorecardEntry          `json:"entry"`
	Scores       []ScorecardHorizonScore `json:"scores"`
}

// ScorecardOptionContract identifies an option leg.
type ScorecardOptionContract struct {
	Expiry     string  `json:"expiry"`
	Strike     float64 `json:"strike"`
	Right      string  `json:"right"`
	Multiplier float64 `json:"multiplier"`
}

// ScorecardEntry carries the entry facts derived from history: the
// underlying close of the execution session and, for options, the implied
// volatility inverted from the fill and the delta at entry.
type ScorecardEntry struct {
	SessionDay      *time.Time `json:"session_day,omitempty"`
	UnderlyingClose *float64   `json:"underlying_close,omitempty"`
	ImpliedVol      *float64   `json:"implied_vol,omitempty"`
	Delta           *float64   `json:"delta,omitempty"`
	Reason          string     `json:"reason,omitempty"`
}

// ScorecardHorizonScore is one decision's score at one horizon, or the
// reason it is unknown.
type ScorecardHorizonScore struct {
	Sessions          int        `json:"sessions"`
	Matured           bool       `json:"matured"`
	Reason            string     `json:"reason,omitempty"`
	HorizonDay        *time.Time `json:"horizon_day,omitempty"`
	HorizonClose      *float64   `json:"horizon_close,omitempty"`
	UnderlyingReturn  *float64   `json:"underlying_return,omitempty"`
	MarketReturn      *float64   `json:"market_return,omitempty"`
	Beta              *float64   `json:"beta,omitempty"`
	IdioVolDaily      *float64   `json:"idio_vol_daily,omitempty"`
	Z                 *float64   `json:"z,omitempty"`
	RealisedVsImplied *float64   `json:"realised_vs_implied,omitempty"`
	RVIReason         string     `json:"realised_vs_implied_reason,omitempty"`
	WrapperBase       *float64   `json:"wrapper_base,omitempty"`
	WrapperReason     string     `json:"wrapper_reason,omitempty"`
	SlippageBp        *float64   `json:"slippage_bp,omitempty"`
	SlippageReason    string     `json:"slippage_reason,omitempty"`
}

// ScorecardCoverage makes the ledger and its gaps explicit.
type ScorecardCoverage struct {
	Ledger          int            `json:"ledger"`
	Stocks          int            `json:"stocks"`
	Options         int            `json:"options"`
	Skipped         map[string]int `json:"skipped"`
	ScoredByHorizon map[int]int    `json:"scored_by_horizon"`
	UnknownByReason map[string]int `json:"unknown_by_reason"`
	// Superseded counts persisted ledger rows no longer in the current
	// derivation (restated statements); they stay in the audit record.
	Superseded int `json:"superseded"`
}

// ScorecardHistory reports the history cache behind the scores.
type ScorecardHistory struct {
	Names         int       `json:"names"`
	Missing       int       `json:"missing"`
	Pending       int       `json:"pending"`
	MarketSymbol  string    `json:"market_symbol"`
	MarketBars    int       `json:"market_bars"`
	LastFetchedAt time.Time `json:"last_fetched_at,omitzero"`
}

// ScorecardMethod travels with every result.
type ScorecardMethod struct {
	Direction           string  `json:"direction"`
	Lookback            string  `json:"lookback"`
	Clusters            string  `json:"clusters"`
	Posterior           string  `json:"posterior"`
	EValue              string  `json:"e_value"`
	Gates               string  `json:"gates"`
	Wrapper             string  `json:"wrapper"`
	RealisedVsImplied   string  `json:"realised_vs_implied"`
	Slippage            string  `json:"slippage"`
	RiskFreeRate        float64 `json:"risk_free_rate"`
	DividendYield       float64 `json:"dividend_yield"`
	NoCausalClaim       bool    `json:"no_causal_claim"`
	NotInvestmentAdvice bool    `json:"not_investment_advice"`
}
