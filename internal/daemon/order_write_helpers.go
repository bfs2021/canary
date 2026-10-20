package daemon

import (
	"fmt"
	"math"
	"strings"

	"github.com/osauer/canary/v2/internal/rpc"
)

func (s *Server) openOrderViewForWrite(id string, status rpc.TradingStatus) (rpc.OrderView, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return rpc.OrderView{}, errBadRequest("order id is required")
	}
	views, _, err := s.loadOrderViews()
	if err != nil {
		return rpc.OrderView{}, err
	}
	var scopeErr error
	for _, view := range views {
		if !orderViewMatchesID(view, id) {
			continue
		}
		if err := validateOrderViewMatchesGate(view, status); err != nil {
			// An identical local ref or broker ID in another route is not the
			// requested order. Keep looking for the exact route before failing.
			scopeErr = err
			continue
		}
		if !view.Open {
			return rpc.OrderView{}, errBadRequest("order is not open")
		}
		s.logOrderEndpointDifference(view, status)
		return view, nil
	}
	if scopeErr != nil {
		return rpc.OrderView{}, scopeErr
	}
	return rpc.OrderView{}, errBadRequest("order not found")
}

// validateOrderViewMatchesGate checks what names an order: its mode, account
// and client id. The socket it was placed through (TWS and IB Gateway listen
// on different ports of one login) is not a gate (owner, 2026-10-20 08:00
// CEST): an open order with the current account, mode and client id may be
// modified or cancelled from either; the difference is logged once.
func validateOrderViewMatchesGate(view rpc.OrderView, status rpc.TradingStatus) error {
	if !strings.EqualFold(view.Mode, status.Mode) {
		return fmt.Errorf("%w: order mode %s does not match current mode %s", ErrTradingDisabled, view.Mode, status.Mode)
	}
	if !strings.EqualFold(view.Account, status.Account) {
		return fmt.Errorf("%w: order account %s does not match current account %s", ErrTradingDisabled, view.Account, status.Account)
	}
	if view.ClientID != status.ClientID {
		return fmt.Errorf("%w: order client ID %d does not match current client ID %d", ErrTradingDisabled, view.ClientID, status.ClientID)
	}
	return nil
}

// logOrderEndpointDifference says once per order and socket, at INFO, that
// an order placed through one socket is now managed through another.
func (s *Server) logOrderEndpointDifference(view rpc.OrderView, status rpc.TradingStatus) {
	placed, current := strings.TrimSpace(view.Endpoint), strings.TrimSpace(status.Endpoint)
	if s == nil || placed == current {
		return
	}
	if _, seen := s.orderEndpointLogged.LoadOrStore(view.OrderRef+"\x1f"+current, struct{}{}); seen {
		return
	}
	s.infof("order %s: placed through %s, managed through %s", view.OrderRef, placed, current)
}

// orderJournalStatusFor keys the journal events of a write to the order's
// own route, the socket it was placed through: one order, one journal row,
// whichever socket the daemon manages it through now.
func orderJournalStatusFor(view rpc.OrderView, status rpc.TradingStatus) rpc.TradingStatus {
	status.Endpoint = view.Endpoint
	return status
}

func validateModifyDraft(view rpc.OrderView, draft rpc.OrderDraft) error {
	if !orderAlgorithmMatches(draft.AdaptivePriority, view.AdaptivePriority, view.AlgoStrategy, view.AlgoKnown) {
		return errBadRequest("order modify cannot change or discard the broker algorithm")
	}
	viewSecType := strings.ToUpper(strings.TrimSpace(view.SecType))
	if (viewSecType != "STK" && viewSecType != "OPT") || strings.ToUpper(strings.TrimSpace(draft.Contract.SecType)) != viewSecType {
		return errBadRequest("order modify supports STK and OPT contracts only, and cannot change the security type")
	}
	if !strings.EqualFold(view.Symbol, draft.Contract.Symbol) {
		return errBadRequest("order modify cannot change symbol")
	}
	if !strings.EqualFold(view.Action, draft.Action) {
		return errBadRequest("order modify cannot change action")
	}
	viewType := strings.ToUpper(strings.TrimSpace(view.OrderType))
	draftType := strings.ToUpper(strings.TrimSpace(draft.OrderType))
	switch viewType {
	case rpc.OrderTypeLMT, rpc.OrderTypeTRAIL, rpc.OrderTypeTRAILLIMIT:
	default:
		return errBadRequest("order modify supports LMT, TRAIL, and TRAIL LIMIT orders only")
	}
	if viewType != draftType {
		return errBadRequest("order modify cannot change order type")
	}
	if !strings.EqualFold(view.TIF, draft.TIF) {
		return errBadRequest("order modify cannot change time-in-force")
	}
	if view.TriggerMethod != draft.TriggerMethod {
		return errBadRequest("order modify cannot change trigger_method")
	}
	if isTrailOrderType(viewType) {
		if !strings.EqualFold(view.TIF, rpc.OrderTIFDay) && !strings.EqualFold(view.TIF, rpc.OrderTIFGTC) {
			return errBadRequest("order modify supports DAY or GTC time-in-force for TRAIL orders")
		}
	} else if !strings.EqualFold(view.TIF, rpc.OrderTIFDay) {
		return errBadRequest("order modify supports DAY time-in-force only")
	}
	if draft.OutsideRTH != view.OutsideRTH {
		return errBadRequest("order modify cannot change outside_rth")
	}
	if isTrailOrderType(draftType) {
		if err := validateModifyTrailDraft(draftType, draft); err != nil {
			return err
		}
	} else if draft.LimitPrice <= 0 || math.IsNaN(draft.LimitPrice) || math.IsInf(draft.LimitPrice, 0) {
		return errBadRequest("order modify requires a positive limit price")
	}
	maxQty := view.Remaining
	if maxQty <= 0 {
		maxQty = view.Quantity - view.Filled
	}
	if maxQty <= 0 {
		maxQty = view.Quantity
	}
	if draft.Quantity <= 0 || float64(draft.Quantity) > maxQty {
		return errBadRequest(fmt.Sprintf("order modify quantity must be positive and no more than remaining %.4g", maxQty))
	}
	if err := validateModifyContractRouting(view, draft.Contract); err != nil {
		return err
	}
	return nil
}

// validateModifyTrailDraft gates the replacement intent for a broker-side
// trailing stop: the trail offset, initial stop, and (for TRAIL LIMIT) limit
// offset may change; everything else is frozen by validateModifyDraft.
func validateModifyTrailDraft(orderType string, draft rpc.OrderDraft) error {
	if draft.LimitPrice != 0 {
		return errBadRequest("order modify TRAIL draft must not include limit_price")
	}
	trail := draft.Trail
	if trail == nil {
		return errBadRequest("order modify requires trail fields for TRAIL orders")
	}
	hasPercent := trail.TrailingPercent != nil
	hasAmount := trail.TrailingAmount != nil
	if hasPercent == hasAmount {
		return errBadRequest("order modify trail requires exactly one of trailing_percent or trailing_amount")
	}
	if hasPercent && !positiveFinite(*trail.TrailingPercent) {
		return errBadRequest("order modify trailing_percent must be positive")
	}
	if hasAmount && !positiveFinite(*trail.TrailingAmount) {
		return errBadRequest("order modify trailing_amount must be positive")
	}
	if !positiveFinite(trail.InitialStopPrice) {
		return errBadRequest("order modify requires a positive initial stop price")
	}
	switch orderType {
	case rpc.OrderTypeTRAIL:
		if trail.LimitOffset != nil {
			return errBadRequest("order modify TRAIL draft must not include limit_offset")
		}
	case rpc.OrderTypeTRAILLIMIT:
		if trail.LimitOffset == nil || !positiveFinite(*trail.LimitOffset) {
			return errBadRequest("order modify TRAIL LIMIT draft requires positive limit_offset")
		}
	}
	return nil
}

// validateProtectiveModifyQuantity is the position-mismatch write gate. When
// a close-only protective order no longer fits the current position, exactly
// one modify shape is accepted: reduce the quantity to the position's
// coverage with the trail unchanged. Everything else (re-pricing, partial
// reductions, increases) is rejected until the size matches the position
// again. current comes from a FRESH positions read at the caller — a caller
// that cannot obtain one must fail closed rather than skip this check.
func validateProtectiveModifyQuantity(view rpc.OrderView, draft rpc.OrderDraft, current float64) error {
	if !orderViewIsCloseProtective(view) {
		return nil
	}
	remaining := orderViewRemainingQuantity(view)
	if orderViewActionCanCloseQuantity(view, current, remaining) {
		return nil
	}
	coverage := protectiveCloseCoverage(view, current)
	if coverage <= 0 {
		return errBadRequest("position is flat or reversed; this protective order cannot be modified — cancel it instead")
	}
	if math.Abs(float64(draft.Quantity)-coverage) > 1e-9 {
		return errBadRequest(fmt.Sprintf("position no longer covers this protective order; modify must reduce quantity to exactly %.4g", coverage))
	}
	if !trailSpecsEquivalent(view.Trail, draft.Trail) {
		return errBadRequest("protective-order reduce must keep the existing trail parameters unchanged; re-price after the size matches the position")
	}
	return nil
}

func trailSpecsEquivalent(a, b *rpc.OrderTrailSpec) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	eq := func(x, y *float64) bool {
		if x == nil || y == nil {
			return x == nil && y == nil
		}
		return math.Abs(*x-*y) <= 1e-9
	}
	return eq(a.TrailingAmount, b.TrailingAmount) &&
		eq(a.TrailingPercent, b.TrailingPercent) &&
		eq(a.LimitOffset, b.LimitOffset) &&
		math.Abs(a.InitialStopPrice-b.InitialStopPrice) <= 1e-9
}

func modifyContractForView(view rpc.OrderView, contract rpc.ContractParams) rpc.ContractParams {
	contract.ConID = view.ConID
	contract.Symbol = view.Symbol
	contract.SecType = view.SecType
	contract.Exchange = view.Exchange
	contract.PrimaryExch = view.PrimaryExch
	contract.Currency = view.Currency
	contract.LocalSymbol = view.LocalSymbol
	contract.TradingClass = view.TradingClass
	// An option's identity is the open order's; a modify cannot name another.
	contract.Expiry = view.Expiry
	contract.Strike = view.Strike
	contract.Right = view.Right
	contract.Multiplier = view.Multiplier
	return contract
}

func validateModifyContractRouting(view rpc.OrderView, contract rpc.ContractParams) error {
	if view.Exchange != "" && contract.Exchange != "" && !strings.EqualFold(view.Exchange, contract.Exchange) {
		return errBadRequest("order modify cannot change exchange")
	}
	if view.PrimaryExch != "" && contract.PrimaryExch != "" && !strings.EqualFold(view.PrimaryExch, contract.PrimaryExch) {
		return errBadRequest("order modify cannot change primary exchange")
	}
	if view.Currency != "" && contract.Currency != "" && !strings.EqualFold(view.Currency, contract.Currency) {
		return errBadRequest("order modify cannot change currency")
	}
	if view.LocalSymbol != "" && contract.LocalSymbol != "" && !strings.EqualFold(view.LocalSymbol, contract.LocalSymbol) {
		return errBadRequest("order modify cannot change local symbol")
	}
	if view.TradingClass != "" && contract.TradingClass != "" && !strings.EqualFold(view.TradingClass, contract.TradingClass) {
		return errBadRequest("order modify cannot change trading class")
	}
	return nil
}

// Legacy callbacks cannot certify Adaptive. Preserve pre-existing plain-order
// handling when algorithm metadata is unavailable, but never ignore a known
// broker strategy or convert an incomplete Adaptive observation to plain.
func orderAlgorithmMatches(expectedPriority, observedPriority, observedStrategy string, known bool) bool {
	if expectedPriority != observedPriority {
		return false
	}
	if expectedPriority != "" {
		return known && observedStrategy == "Adaptive"
	}
	return observedStrategy == ""
}
