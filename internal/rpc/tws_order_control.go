package rpc

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"
)

// TWS order control reaches an order placed by hand in TWS: the daemon opens
// a separate, short-lived connection as client id 0 (the only client TWS lets
// bind its manual orders), binds them, finds the one order by its permanent
// id and the terms the owner reviewed, cancels or changes it, and
// disconnects. The daemon's own connection and client id stay as they are.
// Side effect, by TWS design: the order is an API order from then on.
//
// It is available only when trading.tws_order_control.enabled is on; on the
// live account also only after a paper proof (owner decision "TWS B",
// 2026-10-11 19:36 CEST). Every ordinary broker-write gate applies.
const (
	MethodOrderTWSCancel = "order.tws_cancel"
	MethodOrderTWSModify = "order.tws_modify"
)

// TWS order control outcomes. Confirmed and pending are accepted; refused
// means nothing changed; uncertain means the request may have reached TWS
// and only a later open-order read settles it, never a retry.
const (
	TWSOrderControlOutcomeConfirmed = "confirmed"
	TWSOrderControlOutcomePending   = "pending"
	TWSOrderControlOutcomeRefused   = "refused"
	TWSOrderControlOutcomeUncertain = "uncertain"
)

// TWS order control blocker codes, on a refused or uncertain result.
const (
	TWSOrderControlBlockerOff            = "tws_order_control_off"
	TWSOrderControlBlockerInventory      = "tws_inventory_unavailable"
	TWSOrderControlBlockerNotFound       = "tws_order_not_found"
	TWSOrderControlBlockerChanged        = "tws_order_changed"
	TWSOrderControlBlockerNotWorking     = "tws_order_not_working"
	TWSOrderControlBlockerSlotBusy       = "tws_client0_busy"
	TWSOrderControlBlockerConnect        = "tws_control_connect"
	TWSOrderControlBlockerBind           = "tws_bind_incomplete"
	TWSOrderControlBlockerBrokerRefused  = "tws_broker_refused"
	TWSOrderControlBlockerUncertain      = "tws_outcome_uncertain"
	TWSOrderControlBlockerRequestReplay  = "tws_request_conflict"
	TWSOrderControlBlockerGate           = "tws_control_gate"
	TWSOrderControlBlockerNotEligible    = "tws_order_not_eligible"
	TWSOrderControlBlockerJournalFailure = "tws_journal_unavailable"
)

// OrderTWSControlTerms are the terms of the TWS order as the owner reviewed
// them. The daemon refuses when the order working at the broker, or the
// order TWS binds, differs in any of them.
type OrderTWSControlTerms struct {
	ConID      int     `json:"con_id,omitempty"`
	Symbol     string  `json:"symbol"`
	SecType    string  `json:"sec_type,omitempty"`
	Action     string  `json:"action"`
	Quantity   float64 `json:"quantity"`
	Filled     float64 `json:"filled,omitempty"`
	OrderType  string  `json:"order_type"`
	LimitPrice float64 `json:"limit_price,omitempty"`
	TIF        string  `json:"tif,omitempty"`
}

// OrderTWSControlReplacement is a change's new quantity and limit price.
// Only DAY limit orders on stocks and ETFs are changed; the quantity may not
// exceed the reviewed quantity.
type OrderTWSControlReplacement struct {
	Quantity   float64 `json:"quantity"`
	LimitPrice float64 `json:"limit_price"`
}

// OrderTWSControlParams names one manual TWS order by its permanent id and
// the reviewed terms. RequestID is the caller's idempotency key: a repeat
// with the same intent returns the first outcome and sends nothing; a
// repeat with another intent is refused.
type OrderTWSControlParams struct {
	RequestID   string                      `json:"request_id,omitempty"`
	PermID      int                         `json:"perm_id"`
	Terms       OrderTWSControlTerms        `json:"terms"`
	Replacement *OrderTWSControlReplacement `json:"replacement,omitempty"`
	Origin      string                      `json:"origin,omitempty"`
}

// Validate refuses a request the daemon must not act on.
func (p OrderTWSControlParams) Validate(modify bool) error {
	if p.PermID <= 0 {
		return fmt.Errorf("perm_id is required")
	}
	if len(p.RequestID) > 128 || strings.TrimSpace(p.RequestID) != p.RequestID {
		return fmt.Errorf("request_id is at most 128 bytes without surrounding space")
	}
	t := p.Terms
	if strings.TrimSpace(t.Symbol) == "" || strings.TrimSpace(t.Action) == "" || strings.TrimSpace(t.OrderType) == "" {
		return fmt.Errorf("terms need symbol, action and order_type")
	}
	if !finitePositive(t.Quantity) {
		return fmt.Errorf("terms need a positive quantity")
	}
	if t.Filled < 0 || math.IsNaN(t.Filled) || math.IsInf(t.Filled, 0) {
		return fmt.Errorf("terms filled must be zero or positive")
	}
	if !modify {
		if p.Replacement != nil {
			return fmt.Errorf("a cancel carries no replacement")
		}
		return nil
	}
	if p.Replacement == nil {
		return fmt.Errorf("a change needs a replacement quantity and limit price")
	}
	r := *p.Replacement
	if !finitePositive(r.Quantity) || r.Quantity != math.Trunc(r.Quantity) {
		return fmt.Errorf("replacement quantity must be a positive whole number")
	}
	if r.Quantity > t.Quantity {
		return fmt.Errorf("replacement quantity cannot exceed the reviewed quantity")
	}
	if r.Quantity <= t.Filled {
		return fmt.Errorf("replacement quantity must exceed the filled quantity")
	}
	if !finitePositive(r.LimitPrice) {
		return fmt.Errorf("replacement limit price must be positive")
	}
	return nil
}

func finitePositive(v float64) bool {
	return v > 0 && !math.IsNaN(v) && !math.IsInf(v, 0)
}

// OrderTWSControlDigest binds a request to its outcome: "sha256:" and the
// hex SHA-256 of the compact JSON of the action, permanent id, terms and
// replacement.
func OrderTWSControlDigest(action string, p OrderTWSControlParams) string {
	raw, _ := json.Marshal(struct {
		Action      string                      `json:"action"`
		PermID      int                         `json:"perm_id"`
		Terms       OrderTWSControlTerms        `json:"terms"`
		Replacement *OrderTWSControlReplacement `json:"replacement,omitempty"`
	}{action, p.PermID, p.Terms, p.Replacement})
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// OrderTWSControlResult reports what happened. Accepted is true for a
// confirmed or pending outcome. BoundOrderID is the API order id TWS
// assigned on binding, in client 0's namespace; it is not one of the
// daemon's own order ids. Order is the order as TWS reported it on binding,
// projected like an untracked row; it is not a broker statement.
type OrderTWSControlResult struct {
	Accepted      bool             `json:"accepted"`
	Outcome       string           `json:"outcome"`
	Action        string           `json:"action"`
	RequestID     string           `json:"request_id,omitempty"`
	Digest        string           `json:"digest,omitempty"`
	PermID        int              `json:"perm_id"`
	BoundOrderID  int              `json:"bound_order_id,omitempty"`
	ClientID      int              `json:"client_id"`
	Mode          string           `json:"mode,omitempty"`
	Account       string           `json:"account,omitempty"`
	Endpoint      string           `json:"endpoint,omitempty"`
	Origin        string           `json:"origin,omitempty"`
	BoundFromTWS  bool             `json:"bound_from_tws"`
	Order         *OrderView       `json:"order,omitempty"`
	BrokerStatus  string           `json:"broker_status,omitempty"`
	BrokerCode    int              `json:"broker_code,omitempty"`
	Blockers      []TradingBlocker `json:"blockers,omitempty"`
	Message       string           `json:"message,omitempty"`
	PaperProvenAt time.Time        `json:"paper_proven_at,omitzero"`
	AsOf          time.Time        `json:"as_of"`
}
