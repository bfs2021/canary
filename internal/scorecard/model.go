// Package scorecard is the decision scorecard's pure core. It builds the
// append-only ledger of position changes from Edge's classified changes,
// scores each decision at fixed horizons against the underlying's own
// history, and accumulates sequential evidence per sleeve. It performs no I/O
// and owns no runtime state; only the daemon evaluates it, readers fetch the
// daemon's result (internal-docs/design/decision-scorecard.md).
package scorecard

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/osauer/canary/v2/internal/edge"
	"github.com/osauer/canary/v2/internal/flexstmt"
	"github.com/osauer/canary/v2/internal/rpc"
)

// Schema, sleeve, horizon and estimation constants.
const (
	SchemaVersion = "canary-scorecard-v1"

	// SleeveOwner is the sleeve of hand and TWS trades: every decision
	// without a strategy lineage.
	SleeveOwner = "owner"

	PrimaryHorizon      = 10
	MaxHorizon          = 21
	LookbackSessions    = 252
	MinLookbackSessions = 60

	// RiskFreeRate and DividendYield enter the Black-Scholes inversion. The
	// rate is a documented constant USD policy-rate proxy; the broker's cash
	// rates are deposit tiers, not a risk-free curve.
	RiskFreeRate  = 0.04
	DividendYield = 0.0

	// PriorSD is the prior standard deviation on IC; Tau the mixture scale
	// of the e-value.
	PriorSD = 0.1
	Tau     = 0.2

	EstablishedEValue    = 100.0
	EstablishedClusters  = 100
	EstablishedPosterior = 0.95
	FutileClusters       = 60
	FutilePosterior      = 0.05

	BootstrapResamples = 1000

	// EntrySessionSlack is how many calendar days after the execution day
	// the first available bar may fall and still count as the execution
	// session.
	EntrySessionSlack = 4 * 24 * time.Hour

	InstrumentStock  = "stock"
	InstrumentOption = "option"

	SideBuy  = "buy"
	SideSell = "sell"

	RightCall = "call"
	RightPut  = "put"
)

// Reasons a score, an entry fact or a ledger row is unknown.
const (
	ReasonImmature                 = "immature"
	ReasonHistoryUnavailable       = "history_unavailable"
	ReasonMarketHistoryUnavailable = "market_history_unavailable"
	ReasonLookbackShort            = "lookback_short"
	ReasonEntrySessionMissing      = "entry_session_missing"
	ReasonFillUnknown              = "fill_unknown"
	ReasonInstrumentMetadata       = "instrument_metadata"
	ReasonImpliedVolUnknown        = "implied_vol_unknown"
	ReasonMarkUnavailable          = "mark_unavailable"
	ReasonFXUnknown                = "fx_unknown"
	ReasonNoFillQuote              = "no_fill_quote"
	ReasonNotOption                = "not_option"
	ReasonIdioVolZero              = "idio_vol_zero"
	ReasonUnsupportedAsset         = "unsupported_asset"
	ReasonZeroQuantity             = "zero_quantity"

	// OriginLifecycle marks a fill the broker's option lifecycle produced.
	OriginLifecycle = "lifecycle"
)

// Horizons is the closed set of post-entry session lenses.
var Horizons = [...]int{5, PrimaryHorizon, MaxHorizon}

// Decision is one persisted ledger row: the broker fill facts of one
// position change, its sleeve and its cluster. Lineage holds broker
// identifiers and never leaves the daemon.
type Decision struct {
	ID     string `json:"id"`
	Sleeve string `json:"sleeve"`
	// Stream is entries (open, add) or exits (trim, exit); Origin names the
	// rule or lifecycle source when the sleeve is rule.
	Stream          string          `json:"stream"`
	Origin          string          `json:"origin,omitempty"`
	StrategyRef     string          `json:"strategy_ref,omitempty"`
	Underlying      string          `json:"underlying"`
	UnderlyingConID int64           `json:"underlying_conid"`
	ConID           int64           `json:"conid"`
	Symbol          string          `json:"symbol"`
	Instrument      string          `json:"instrument"`
	Option          *OptionContract `json:"option,omitempty"`
	Action          string          `json:"action"`
	Side            string          `json:"side"`
	Sign            int             `json:"sign"`
	Quantity        float64         `json:"quantity"`
	FillPrice       *float64        `json:"fill_price,omitempty"`
	Multiplier      *float64        `json:"multiplier,omitempty"`
	Currency        string          `json:"currency,omitempty"`
	ExecutionFX     *float64        `json:"execution_fx,omitempty"`
	NotionalBase    *float64        `json:"notional_base,omitempty"`
	ExecutedAt      time.Time       `json:"executed_at"`
	PositionBefore  float64         `json:"position_before"`
	PositionAfter   float64         `json:"position_after"`
	PathUnbalanced  bool            `json:"path_unbalanced,omitempty"`
	ClusterKey      string          `json:"cluster_key"`
	Lineage         Lineage         `json:"lineage"`
}

// OptionContract identifies an option leg.
type OptionContract struct {
	Expiry     string  `json:"expiry"`
	Strike     float64 `json:"strike"`
	Right      string  `json:"right"`
	Multiplier float64 `json:"multiplier"`
}

// Lineage is the broker identity behind a decision. It stays in daemon.db.
type Lineage struct {
	RecordIDs    []string `json:"record_ids,omitempty"`
	ExecutionIDs []string `json:"execution_ids,omitempty"`
	OrderIDs     []string `json:"order_ids,omitempty"`
	TradeIDs     []string `json:"trade_ids,omitempty"`
}

// Sleeve is a resolver's answer: the sleeve a fill belongs to, the strategy
// lineage behind it and, for the rule sleeve, the rule or event that
// produced it.
type Sleeve struct {
	Name        string
	StrategyRef string
	Origin      string
}

// SleeveResolver assigns a sleeve to a change from its order lineage. The
// default resolver returns the owner sleeve; the daemon's resolver reads
// Canary's order journal and proposal records to tag rule-originated fills.
type SleeveResolver func(edge.PositionChange) Sleeve

// OwnerSleeve is the default resolver: every fill is the owner's.
func OwnerSleeve(edge.PositionChange) Sleeve { return Sleeve{Name: SleeveOwner} }

// StreamOf is the stream of an action: open and add are entries, trim and
// exit are exits.
func StreamOf(action string) string {
	switch action {
	case edge.ActionOpen, edge.ActionAdd:
		return rpc.ScorecardStreamEntries
	default:
		return rpc.ScorecardStreamExits
	}
}

// BuildLedger derives the ledger rows from Edge's classified changes, oldest
// first. Stocks, ETFs and options enter; every other asset class is counted
// under skipped. It is deterministic under reordered evidence.
func BuildLedger(ev edge.ChangeEvidence, resolve SleeveResolver) ([]Decision, map[string]int) {
	if resolve == nil {
		resolve = OwnerSleeve
	}
	skipped := map[string]int{}
	out := make([]Decision, 0, len(ev.Changes))
	for _, change := range ev.Changes {
		if almostZero(change.DeltaQuantity) {
			skipped[ReasonZeroQuantity]++
			continue
		}
		instrument := ""
		switch change.AssetClass {
		case "STK", "ETF":
			instrument = InstrumentStock
		case "OPT":
			instrument = InstrumentOption
		default:
			skipped[ReasonUnsupportedAsset]++
			continue
		}
		resolved := resolve(change)
		sleeve := strings.ToLower(strings.TrimSpace(resolved.Name))
		if sleeve == "" {
			sleeve = SleeveOwner
		}
		strategyRef, origin := resolved.StrategyRef, resolved.Origin
		// A fill the broker's lifecycle rows share a trade id with is a
		// delivery, not a decision, whatever the resolver said.
		for _, tradeID := range change.TradeIDs {
			if ev.LifecycleTradeIDs[tradeID] {
				sleeve, origin = rpc.ScorecardSleeveRule, OriginLifecycle
				break
			}
		}
		if sleeve == rpc.ScorecardSleeveRule && origin == "" {
			origin = "rule"
		}
		side := SideBuy
		if change.DeltaQuantity < 0 {
			side = SideSell
		}
		d := Decision{
			Sleeve: sleeve, Stream: StreamOf(change.Action), Origin: origin, StrategyRef: strategyRef, ConID: change.ConID, Symbol: change.Symbol, Instrument: instrument,
			Action: change.Action, Side: side, Quantity: math.Abs(change.DeltaQuantity),
			FillPrice: positive(change.ExecutionVWAP), Multiplier: positive(change.Multiplier), Currency: change.Currency,
			ExecutionFX: positive(change.ExecutionFX), NotionalBase: positive(change.ExecutionNotionalBase),
			ExecutedAt: change.ExecutedAt.UTC(), PositionBefore: change.PositionBefore, PositionAfter: change.PositionAfter, PathUnbalanced: change.PathUnbalanced,
			Lineage: Lineage{RecordIDs: append([]string(nil), change.RecordIDs...), ExecutionIDs: append([]string(nil), change.ExecutionIDs...), OrderIDs: append([]string(nil), change.OrderIDs...), TradeIDs: append([]string(nil), change.TradeIDs...)},
		}
		meta, haveMeta := ev.Instruments[change.ConID]
		switch instrument {
		case InstrumentStock:
			d.Underlying, d.UnderlyingConID = change.Symbol, change.ConID
			d.Sign = 1
			if side == SideSell {
				d.Sign = -1
			}
		case InstrumentOption:
			d.UnderlyingConID = change.UnderlyingConID
			if d.UnderlyingConID == 0 && haveMeta {
				d.UnderlyingConID = meta.UnderlyingConID
			}
			d.Underlying = firstNonEmpty(change.UnderlyingSymbol, meta.UnderlyingSymbol, optionRootSymbol(change.Symbol))
			if haveMeta {
				if contract := optionContractFrom(meta, d.Multiplier); contract != nil {
					d.Option = contract
					if d.Multiplier == nil {
						d.Multiplier = &contract.Multiplier
					}
					d.Sign = DirectionSign(contract.Right, side)
				}
			}
		}
		year, week := d.ExecutedAt.ISOWeek()
		d.ClusterKey = fmt.Sprintf("%s:%04d-W%02d", sleeve, year, week)
		d.ID = decisionID(d)
		out = append(out, d)
	}
	return out, skipped
}

// DirectionSign is the underlying-direction sign of a fill: +1 for long
// exposure, −1 for short. A long put or a short call is −1; a short put or a
// long call is +1. An unknown right is 0.
func DirectionSign(right, side string) int {
	sign := 0
	switch right {
	case RightCall:
		sign = 1
	case RightPut:
		sign = -1
	default:
		return 0
	}
	if side == SideSell {
		sign = -sign
	}
	return sign
}

// optionContractFrom reads the option identity from the statement's
// instrument row; incomplete metadata yields nil and the decision scores as
// unknown with that reason.
func optionContractFrom(meta flexstmt.Instrument, multiplier *float64) *OptionContract {
	right := ""
	switch strings.ToUpper(strings.TrimSpace(meta.PutCall)) {
	case "C", "CALL":
		right = RightCall
	case "P", "PUT":
		right = RightPut
	}
	expiry := ""
	for _, layout := range []string{"20060102", "2006-01-02"} {
		if parsed, err := time.Parse(layout, strings.TrimSpace(meta.Expiry)); err == nil {
			expiry = parsed.Format(time.DateOnly)
			break
		}
	}
	if right == "" || expiry == "" || meta.Strike == nil || *meta.Strike <= 0 {
		return nil
	}
	mult := 0.0
	if multiplier != nil {
		mult = *multiplier
	} else if meta.Multiplier != nil && *meta.Multiplier > 0 {
		mult = *meta.Multiplier
	}
	if mult <= 0 {
		return nil
	}
	return &OptionContract{Expiry: expiry, Strike: *meta.Strike, Right: right, Multiplier: mult}
}

func decisionID(d Decision) string {
	h := sha256.New()
	h.Write([]byte("canary.scorecard.decision.v1"))
	// The sleeve is an attribute, not identity: a fill keeps its id when
	// later lineage reclassifies it.
	for _, value := range []string{strconv.FormatInt(d.ConID, 10), d.ExecutedAt.UTC().Format(time.RFC3339Nano), d.Side, d.Action, strconv.FormatFloat(d.Quantity, 'g', -1, 64), strings.Join(d.Lineage.RecordIDs, ",")} {
		h.Write([]byte{0})
		h.Write([]byte(value))
	}
	return "decision_" + hex.EncodeToString(h.Sum(nil)[:12])
}

func optionRootSymbol(symbol string) string {
	fields := strings.Fields(symbol)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

func positive(v *float64) *float64 {
	if v == nil || *v <= 0 || math.IsNaN(*v) || math.IsInf(*v, 0) {
		return nil
	}
	value := *v
	return &value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func almostZero(v float64) bool { return math.Abs(v) <= 1e-9 }
