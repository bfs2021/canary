// Package chainrec records a daily option-chain snapshot for research: one
// row per contract per run, with quotes, model greeks and open interest, so
// the desk owns its own options panel instead of buying one.
//
// The package is broker-agnostic. The daemon supplies a [Broker] adapter over
// the gateway connector; selection, scheduling, pacing and persistence live
// here and are tested against fakes. Contract:
// internal-docs/design/option-chain-recorder.md.
package chainrec

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Fixed v1 plan. Only the switch and the explicit symbol list are settings;
// these numbers are the recorder's contract and change with the design note.
const (
	// StrikeWindowPct bounds selected strikes to spot ± 20 %.
	StrikeWindowPct = 0.20
	// MaxStrikesPerExpiry caps strikes per expiry; each strike records a call
	// and a put.
	MaxStrikesPerExpiry = 40
	// MaxSymbols caps underlyings per run: explicit symbols first, then held
	// underlyings alphabetically. The rest are recorded as skipped.
	MaxSymbols = 12
	// SnapshotLead is how long before the regular close a run starts.
	SnapshotLead = 30 * time.Minute
	// LatestStartBeforeClose is the last moment a late start (a daemon that
	// came up inside the window) may still begin a run.
	LatestStartBeforeClose = 10 * time.Minute
	// MaxLines is the number of market-data lines the recorder holds at once.
	MaxLines = 4
	// LineReserve is the number of free market-data lines that must remain
	// for the live display and order paths before the recorder opens one.
	LineReserve = 20
	// ContractWindow bounds how long one contract's line stays open waiting
	// for its quote, model tick and open interest.
	ContractWindow = 3 * time.Second
	// PacingBackoffBase is the first pause after a pacing signal; each
	// further consecutive signal doubles it up to PacingBackoffMax.
	PacingBackoffBase = 5 * time.Second
	// PacingBackoffMax caps one pause.
	PacingBackoffMax = 80 * time.Second
	// MaxPacingSignals consecutive pacing signals end the run as partial.
	MaxPacingSignals = 5
	// EntitlementGiveUp entitlement refusals on one symbol with no recorded
	// quote skip that symbol's remaining contracts.
	EntitlementGiveUp = 4
)

// DefaultSymbols is the explicit list in force while the setting is unset.
// XSP is not in it: the gateway layer routes XSP as a stock, so its chain
// cannot be discovered until it is classified as an index.
var DefaultSymbols = []string{"SPX", "SPY", "QQQ"}

// Settings is the owner's resolved preference.
type Settings struct {
	Enabled bool
	// Symbols is the explicit list; held option underlyings are added to it.
	Symbols []string
}

// Expiry slots a selected expiry fills.
const (
	SlotMonthly1 = "monthly_1"
	SlotMonthly2 = "monthly_2"
	SlotWeekly   = "weekly"
)

// Row statuses. The first three mean the line opened; the rest name why a
// contract has no observation.
const (
	StatusQuoted       = "quoted"     // a bid, ask or last arrived
	StatusModelOnly    = "model_only" // only the model tick (IV/greeks) arrived
	StatusNoQuote      = "no_quote"   // the line opened and nothing arrived in the window
	StatusNoContract   = "no_contract"
	StatusEntitlement  = "no_entitlement"
	StatusPacing       = "pacing"
	StatusUnavailable  = "gateway_unavailable"
	StatusRejected     = "rejected"
	StatusTimeout      = "timeout"
	StatusNotAttempted = "not_attempted"
)

// Stop reasons for contracts never requested and for partial runs.
const (
	StopDeadline        = "deadline"
	StopPacing          = "pacing"
	StopGateway         = "gateway_unavailable"
	StopSymbolNoEntitle = "symbol_no_entitlement"
	StopCancelled       = "cancelled"
)

// Run statuses.
const (
	RunRunning     = "running"
	RunComplete    = "complete"
	RunPartial     = "partial"
	RunFailed      = "failed"
	RunInterrupted = "interrupted"
)

// Symbol statuses after discovery.
const (
	SymbolPlanned          = "planned"
	SymbolChainUnavailable = "chain_unavailable"
	SymbolSpotUnavailable  = "spot_unavailable"
	SymbolNoExpiries       = "no_expiries"
	SymbolSkippedCap       = "skipped_symbol_cap"
	SymbolNotDiscovered    = "not_discovered"
)

// Symbol sources.
const (
	SourceExplicit = "explicit"
	SourceHeld     = "held"
	SourceBoth     = "explicit+held"
)

// ListedExpiry is one listed (date, trading class) pair and its strikes, as
// the security-definition reply lists them. Date is YYYY-MM-DD: IBKR's last
// trade date, so AM-settled index monthlies appear on the day before
// settlement.
type ListedExpiry struct {
	Date         string
	TradingClass string
	Strikes      []float64
}

// SelectedExpiry is a listed expiry chosen for one slot.
type SelectedExpiry struct {
	Date         string
	TradingClass string
	Slot         string
	Strikes      []float64
}

// Spot is the underlying price the strike window is centred on.
type Spot struct {
	Price    float64
	Source   string // last, mid, mark, prev_close, ...
	DataType string // live, frozen, delayed, prev_close
	AsOf     time.Time
}

// Contract is one exact option the run requests.
type Contract struct {
	Symbol       string
	TradingClass string
	Expiry       string // YYYY-MM-DD
	Slot         string
	Strike       float64
	Right        string // C or P
	// rank orders the work: strikes nearest spot first across every symbol,
	// so a run cut short keeps the money.
	rank int
	sym  int
}

// ExpiryYMD is the compact expiry the gateway expects.
func (c Contract) ExpiryYMD() string { return strings.ReplaceAll(c.Expiry, "-", "") }

// Key is the contract's natural key inside one run.
func (c Contract) Key() string {
	return fmt.Sprintf("%s|%s|%s|%g|%s", c.Symbol, c.TradingClass, c.Expiry, c.Strike, c.Right)
}

// Quote is what one contract's line delivered inside its window. Nil means
// not observed, never zero.
type Quote struct {
	ConID           int
	Bid, Ask, Last  *float64
	BidSize         *float64
	AskSize         *float64
	Volume          *int64
	OpenInterest    *int64
	IV              *float64
	Delta, Gamma    *float64
	Vega, Theta     *float64
	UnderlyingPrice *float64
	// ModelSource is "live" (tick 13) or "delayed" (tick 83) when a model
	// tick arrived.
	ModelSource string
	// Feed is the line's market-data type: live, frozen, delayed or
	// delayed-frozen.
	Feed string
	// QuoteReceivedAt is when this process received the newest bid or ask
	// tick; IBKR quote ticks carry no exchange timestamp.
	QuoteReceivedAt time.Time
	// LastTradeAt is the broker's own last-trade time (tick 45) when sent.
	LastTradeAt time.Time
	RequestedAt time.Time
	ObservedAt  time.Time
}

// Status classifies an opened line by what it delivered.
func (q Quote) Status() string {
	switch {
	case q.Bid != nil || q.Ask != nil || q.Last != nil:
		return StatusQuoted
	case q.IV != nil || q.Delta != nil:
		return StatusModelOnly
	default:
		return StatusNoQuote
	}
}

// Row is one contract's record in one run.
type Row struct {
	Contract
	Status string
	Detail string
	Quote
}

// Failure is a classified broker refusal for one contract.
type Failure struct {
	Status string // one of the failure row statuses
	Err    error
}

// Error names the failure status and the underlying broker error.
func (f *Failure) Error() string {
	if f.Err == nil {
		return f.Status
	}
	return f.Status + ": " + f.Err.Error()
}

// Unwrap returns the underlying broker error.
func (f *Failure) Unwrap() error { return f.Err }

// Fail wraps err as a classified failure.
func Fail(status string, err error) error { return &Failure{Status: status, Err: err} }

// failureStatus returns err's classified status, or rejected.
func failureStatus(err error) string {
	if f, ok := errors.AsType[*Failure](err); ok && f.Status != "" {
		return f.Status
	}
	return StatusRejected
}

// NormalizeSymbol upper-cases and validates one underlying symbol.
func NormalizeSymbol(raw string) (string, error) {
	s := strings.ToUpper(strings.TrimSpace(raw))
	if s == "" || len(s) > 12 {
		return "", fmt.Errorf("symbol %q must be 1-12 characters", raw)
	}
	for _, r := range s {
		if (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '.' {
			return "", fmt.Errorf("symbol %q may contain only letters, digits and '.'", raw)
		}
	}
	return s, nil
}

// NormalizeSymbols validates, upper-cases and de-duplicates a list in order.
func NormalizeSymbols(raw []string) ([]string, error) {
	out := make([]string, 0, len(raw))
	for _, r := range raw {
		s, err := NormalizeSymbol(r)
		if err != nil {
			return nil, err
		}
		if !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	if len(out) > MaxSymbols {
		return nil, fmt.Errorf("at most %d explicit symbols", MaxSymbols)
	}
	return out, nil
}
