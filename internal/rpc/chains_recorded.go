package rpc

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// MethodChainsRecorded reads the option-chain recorder's runs and coverage.
const MethodChainsRecorded = "chains.recorded"

// MethodChainsRecordedRows pages through recorded contract rows for export.
const MethodChainsRecordedRows = "chains.recorded_rows"

// ChainsRecordedParams filters the run list: one session date and/or one
// underlying. Limit bounds the runs returned (default 10, at most 250).
type ChainsRecordedParams struct {
	Symbol string `json:"symbol,omitempty"`
	Date   string `json:"date,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

// Chains recorded paging bounds.
const (
	ChainsRecordedDefaultRuns = 10
	ChainsRecordedMaxRuns     = 250
	ChainsRowsDefaultLimit    = 2000
	ChainsRowsMaxLimit        = 10000
)

// NormalizeChainsRecordedParams validates the filters.
func NormalizeChainsRecordedParams(p ChainsRecordedParams) (ChainsRecordedParams, error) {
	var err error
	if p.Symbol, err = normalizeChainsSymbol(p.Symbol); err != nil {
		return p, err
	}
	if p.Date, err = normalizeChainsDate(p.Date); err != nil {
		return p, err
	}
	switch {
	case p.Limit < 0:
		return p, fmt.Errorf("limit must be positive")
	case p.Limit == 0:
		p.Limit = ChainsRecordedDefaultRuns
	case p.Limit > ChainsRecordedMaxRuns:
		p.Limit = ChainsRecordedMaxRuns
	}
	return p, nil
}

// ChainsRecordedRowsParams selects rows of one run (RunID) or of every run
// on one session date (Date), optionally one underlying. After is the Next
// cursor of the previous page.
type ChainsRecordedRowsParams struct {
	RunID  int64  `json:"run_id,omitempty"`
	Date   string `json:"date,omitempty"`
	Symbol string `json:"symbol,omitempty"`
	After  string `json:"after,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

// NormalizeChainsRecordedRowsParams validates the row selection.
func NormalizeChainsRecordedRowsParams(p ChainsRecordedRowsParams) (ChainsRecordedRowsParams, error) {
	var err error
	if p.Symbol, err = normalizeChainsSymbol(p.Symbol); err != nil {
		return p, err
	}
	if p.Date, err = normalizeChainsDate(p.Date); err != nil {
		return p, err
	}
	if p.RunID < 0 {
		return p, fmt.Errorf("run_id must be positive")
	}
	if p.RunID == 0 && p.Date == "" {
		return p, fmt.Errorf("run_id or date is required")
	}
	if p.After = strings.TrimSpace(p.After); p.After != "" {
		if n, err := strconv.ParseInt(p.After, 10, 64); err != nil || n < 0 {
			return p, fmt.Errorf("after must be the next cursor of a previous page")
		}
	}
	switch {
	case p.Limit < 0:
		return p, fmt.Errorf("limit must be positive")
	case p.Limit == 0:
		p.Limit = ChainsRowsDefaultLimit
	case p.Limit > ChainsRowsMaxLimit:
		p.Limit = ChainsRowsMaxLimit
	}
	return p, nil
}

func normalizeChainsSymbol(raw string) (string, error) {
	s := strings.ToUpper(strings.TrimSpace(raw))
	if s == "" {
		return "", nil
	}
	if len(s) > 12 || strings.IndexFunc(s, func(r rune) bool { return (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '.' }) >= 0 {
		return "", fmt.Errorf("symbol must be 1-12 letters, digits or '.'")
	}
	return s, nil
}

func normalizeChainsDate(raw string) (string, error) {
	d := strings.TrimSpace(raw)
	if d == "" {
		return "", nil
	}
	t, err := time.Parse(time.DateOnly, d)
	if err != nil || t.Format(time.DateOnly) != d {
		return "", fmt.Errorf("date must be a YYYY-MM-DD session date")
	}
	return d, nil
}

// ChainsRecordedResult is the recorder's state and its runs, newest first.
type ChainsRecordedResult struct {
	Version  int                  `json:"version"`
	AsOf     time.Time            `json:"as_of"`
	Recorder ChainRecorderState   `json:"recorder"`
	Store    ChainRecorderStore   `json:"store"`
	Runs     []ChainRecorderRun   `json:"runs"`
	Filter   ChainsRecordedParams `json:"filter"`
}

// ChainRecorderState is the switch, the explicit list and the fixed plan.
type ChainRecorderState struct {
	Enabled bool `json:"enabled"`
	// Symbols is the explicit list in force; held option underlyings are
	// added at run time.
	Symbols             []string   `json:"symbols"`
	SymbolsDefault      bool       `json:"symbols_default"`
	MaxSymbols          int        `json:"max_symbols"`
	Expiries            string     `json:"expiries"`
	StrikeWindowPct     float64    `json:"strike_window_pct"`
	MaxStrikesPerExpiry int        `json:"max_strikes_per_expiry"`
	LeadMinutes         int        `json:"lead_minutes"`
	MaxLines            int        `json:"max_lines"`
	LineReserve         int        `json:"line_reserve"`
	Running             bool       `json:"running"`
	NextRun             *time.Time `json:"next_run,omitempty"`
}

// ChainRecorderStore describes the separate recorder database.
type ChainRecorderStore struct {
	Present bool  `json:"present"`
	Runs    int   `json:"runs"`
	Rows    int64 `json:"rows"`
	Bytes   int64 `json:"bytes"`
}

// ChainRecorderRun is one snapshot run and its coverage.
type ChainRecorderRun struct {
	RunID        int64     `json:"run_id"`
	SessionDate  string    `json:"session_date"`
	SessionClose time.Time `json:"session_close"`
	ScheduledAt  time.Time `json:"scheduled_at"`
	StartedAt    time.Time `json:"started_at"`
	Deadline     time.Time `json:"deadline"`
	FinishedAt   time.Time `json:"finished_at,omitzero"`
	EarlyClose   bool      `json:"early_close,omitempty"`
	// Status is running, complete, partial, failed or interrupted (begun
	// and never closed, e.g. the daemon stopped mid-run).
	Status          string                      `json:"status"`
	StopReason      string                      `json:"stop_reason,omitempty"`
	HeldUnavailable bool                        `json:"held_unavailable,omitempty"`
	Coverage        ChainRecorderCoverage       `json:"coverage"`
	Symbols         []ChainRecorderSymbolResult `json:"symbols"`
}

// ChainRecorderCoverage counts a run's contracts. Planned is every contract
// discovery selected; Attempted opened (or tried to open) a line; Quoted
// received a bid, ask or last; TwoSided both a bid and an ask. WithIV,
// WithGreeks and WithOI count model IV, model delta and open interest.
type ChainRecorderCoverage struct {
	Planned      int            `json:"planned"`
	Attempted    int            `json:"attempted"`
	Quoted       int            `json:"quoted"`
	TwoSided     int            `json:"two_sided"`
	WithIV       int            `json:"with_iv"`
	WithGreeks   int            `json:"with_greeks"`
	WithOI       int            `json:"with_oi"`
	NotAttempted int            `json:"not_attempted"`
	ByStatus     map[string]int `json:"by_status,omitempty"`
}

// ChainRecorderSymbolResult is one underlying's discovery and coverage.
type ChainRecorderSymbolResult struct {
	Symbol       string                `json:"symbol"`
	Source       string                `json:"source"`
	Status       string                `json:"status"`
	Detail       string                `json:"detail,omitempty"`
	Spot         float64               `json:"spot,omitempty"`
	SpotSource   string                `json:"spot_source,omitempty"`
	SpotDataType string                `json:"spot_data_type,omitempty"`
	Expiries     []ChainRecorderExpiry `json:"expiries,omitempty"`
	Coverage     ChainRecorderCoverage `json:"coverage"`
}

// ChainRecorderExpiry is one selected expiry's strike selection.
type ChainRecorderExpiry struct {
	Date         string `json:"date"`
	TradingClass string `json:"trading_class"`
	Slot         string `json:"slot"`
	InWindow     int    `json:"in_window"`
	Selected     int    `json:"selected"`
	Contracts    int    `json:"contracts"`
}

// ChainsRecordedRowsResult is one page of rows. Next is empty on the last
// page.
type ChainsRecordedRowsResult struct {
	Rows []ChainRecordedRow `json:"rows"`
	Next string             `json:"next,omitempty"`
}

// ChainRecordedRow is one contract in one run. Nil prices, sizes and model
// values were not observed; they are never zero-filled. Times are UTC.
type ChainRecordedRow struct {
	RunID           int64     `json:"run_id"`
	SessionDate     string    `json:"session_date"`
	Symbol          string    `json:"symbol"`
	TradingClass    string    `json:"trading_class"`
	Expiry          string    `json:"expiry"`
	ExpirySlot      string    `json:"expiry_slot"`
	Strike          float64   `json:"strike"`
	Right           string    `json:"right"`
	Status          string    `json:"status"`
	Detail          string    `json:"detail,omitempty"`
	ConID           int       `json:"con_id,omitempty"`
	Bid             *float64  `json:"bid,omitempty"`
	Ask             *float64  `json:"ask,omitempty"`
	Last            *float64  `json:"last,omitempty"`
	BidSize         *float64  `json:"bid_size,omitempty"`
	AskSize         *float64  `json:"ask_size,omitempty"`
	Volume          *int64    `json:"volume,omitempty"`
	OpenInterest    *int64    `json:"open_interest,omitempty"`
	IV              *float64  `json:"iv,omitempty"`
	Delta           *float64  `json:"delta,omitempty"`
	Gamma           *float64  `json:"gamma,omitempty"`
	Vega            *float64  `json:"vega,omitempty"`
	Theta           *float64  `json:"theta,omitempty"`
	UnderlyingPrice *float64  `json:"underlying_price,omitempty"`
	ModelSource     string    `json:"model_source,omitempty"`
	Feed            string    `json:"feed,omitempty"`
	RequestedAt     time.Time `json:"requested_at,omitzero"`
	ObservedAt      time.Time `json:"observed_at,omitzero"`
	QuoteReceivedAt time.Time `json:"quote_received_at,omitzero"`
	LastTradeAt     time.Time `json:"last_trade_at,omitzero"`
}
