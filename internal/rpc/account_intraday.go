package rpc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"time"
)

// MethodAccountIntraday reads the daemon's recorded net-liquidation samples
// for the current broker scope's US-equity sessions. It is display evidence
// only and feeds no decision.
const MethodAccountIntraday = "account.intraday"

// ValidateAccountIntradayParams permits no caller-supplied scope or series.
func ValidateAccountIntradayParams(raw json.RawMessage) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	var empty struct{}
	if err := d.Decode(&empty); err != nil {
		return err
	}
	if d.Decode(new(any)) != io.EOF {
		return fmt.Errorf("one empty account intraday object is required")
	}
	return nil
}

// AccountIntradayResult is the recorded account value of the current and the
// previous US-equity session. A bucket without an admissible broker read has
// no point; nothing is interpolated or carried forward.
type AccountIntradayResult struct {
	Kind   string    `json:"kind"`
	AsOf   time.Time `json:"as_of"`
	Status string    `json:"status"`
	Reason string    `json:"reason,omitempty"`
	// MarketSession is today's US-equity calendar session at AsOf, so a
	// reader can tell a session not yet open from a closed day.
	MarketSession *AccountIntradayMarketSession `json:"market_session,omitempty"`
	// Sessions are the retained sessions with samples, newest first.
	Sessions []AccountIntradaySession `json:"sessions"`
}

// AccountIntradayMarketSession is the calendar's reading of today.
type AccountIntradayMarketSession struct {
	Date   string    `json:"date"`
	State  string    `json:"state"`
	IsOpen bool      `json:"is_open"`
	Open   time.Time `json:"open,omitzero"`
	Close  time.Time `json:"close,omitzero"`
}

// AccountIntradaySession is one session's net liquidation in its base
// currency. OpeningValue is present only when the first point falls in the
// session's first bucket; it is never a later point standing in for the open.
type AccountIntradaySession struct {
	Date            string                 `json:"date"`
	Open            time.Time              `json:"open"`
	Close           time.Time              `json:"close"`
	BaseCurrency    string                 `json:"base_currency"`
	IntervalSeconds int                    `json:"interval_seconds"`
	OpeningValue    *float64               `json:"opening_value,omitempty"`
	Points          []AccountIntradayPoint `json:"points"`
}

// AccountIntradayPoint is one admissible broker read: net liquidation at the
// read's observation clock. IBKR supplies no valuation time.
type AccountIntradayPoint struct {
	At    time.Time `json:"at"`
	Value float64   `json:"value"`
}

// Account intraday result kind and statuses.
const (
	AccountIntradayKind        = "ibkr.account_intraday"
	AccountIntradayAvailable   = "available"
	AccountIntradayUnavailable = "unavailable"
)
