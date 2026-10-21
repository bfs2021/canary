package ibkr

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// SubmitStockEntryStopForSessionGuarded sends exactly one held stock entry and
// its releasing fixed stop under one first-byte guard. Success means written,
// never broker acceptance or protection. There is no retry after uncertainty.
func (c *Connector) SubmitStockEntryStopForSessionGuarded(ctx context.Context, binding ConnectorSessionBinding, contract *Contract, entry *RawOrder, childID int, childRef string, stop float64, guard func() error) error {
	if !tradingEnabled {
		return definitelyUnsent(ErrTradingDisabled)
	}
	return c.submitStockEntryStop(ctx, binding, nil, contract, entry, childID, childRef, stop, guard)
}

// SubmitPaperStockEntryStopForSessionGuarded applies the ordinary paper gate
// to the same indivisible transport admission.
func (c *Connector) SubmitPaperStockEntryStopForSessionGuarded(ctx context.Context, binding ConnectorSessionBinding, gate PaperOrderGate, contract *Contract, entry *RawOrder, childID int, childRef string, stop float64, guard func() error) error {
	if err := gate.validate(); err != nil {
		return definitelyUnsent(err)
	}
	return c.submitStockEntryStop(ctx, binding, &gate, contract, entry, childID, childRef, stop, guard)
}

func (c *Connector) submitStockEntryStop(ctx context.Context, binding ConnectorSessionBinding, gate *PaperOrderGate, contract *Contract, entry *RawOrder, childID int, childRef string, stop float64, guard func() error) error {
	if c == nil || ctx == nil || contract == nil || entry == nil {
		return definitelyUnsent(fmt.Errorf("stock entry and stop require an exact session, contract and entry"))
	}
	if err := ctx.Err(); err != nil {
		return definitelyUnsent(err)
	}
	if !c.SessionCurrent(binding) {
		return definitelyUnsent(fmt.Errorf("stock entry session changed"))
	}
	if down, _ := c.backendConnectivityDown(); down {
		return definitelyUnsent(fmt.Errorf("broker backend connectivity unavailable"))
	}
	if contract.ConID <= 0 || contract.SecType != "STK" || len(contract.ComboLegs) > 0 || entry.Action != "BUY" || entry.OrderType != "LMT" || entry.TIF != "DAY" || entry.TotalQty < 1 || entry.TotalQty > 1000000 || entry.OrderID <= 0 || childID <= entry.OrderID || entry.Changes != nil || entry.OcaGroup != "" || entry.OcaType != 0 || entry.OutsideRth || entry.Account == "" || strings.TrimSpace(entry.OrderRef) == "" || strings.TrimSpace(childRef) == "" || childRef == entry.OrderRef || !isPositiveFiniteStockPrice(entry.LmtPrice) || !isPositiveFiniteStockPrice(stop) || stop >= entry.LmtPrice {
		return definitelyUnsent(fmt.Errorf("invalid stock entry and attached stop terms"))
	}
	conn := binding.connection
	if gate != nil && (entry.Account != gate.Account || entry.ClientID != gate.ClientID) {
		return definitelyUnsent(fmt.Errorf("stock entry does not match paper gate"))
	}
	if conn.serverVersion < minServerVerProtoBufPlaceOrder {
		return definitelyUnsent(fmt.Errorf("attached stock stop requires current broker protocol"))
	}
	parent := rawIBKROrder(contract, entry)
	parent.Transmit = false
	parent.WhatIf = false
	child := rawIBKROrder(contract, &RawOrder{OrderID: childID, ClientID: entry.ClientID, Account: entry.Account, OrderRef: childRef, Action: "SELL", TotalQty: entry.TotalQty, OrderType: "STP", AuxPrice: stop, TIF: "GTC", TriggerMethod: 2, OpenClose: "C"})
	child.ParentID = entry.OrderID
	child.Transmit = true
	orders := []*IBKROrder{parent, child}
	// Fully validate and encode both legs before claiming either for dispatch.
	frames := make([][]byte, 2)
	for i, o := range orders {
		if err := ValidateOrder(o); err != nil {
			return definitelyUnsent(err)
		}
		for _, v := range []string{o.Account, o.OrderRef, o.Symbol, o.Exchange, o.Currency, o.LocalSymbol, o.TradingClass, o.PrimaryExch} {
			if err := ensureASCII("stock entry/stop identity", v); err != nil {
				return definitelyUnsent(err)
			}
		}
		if gate != nil {
			if err := gate.validateConnection(conn); err != nil {
				return definitelyUnsent(err)
			}
		}
		var err error
		frames[i], err = encodePlaceOrderProtoFrame(o)
		if err != nil {
			return definitelyUnsent(err)
		}
	}
	c.brokerIDNamespaceMu.Lock()
	for _, o := range orders {
		if c.feeRequestOwnsID(o.OrderID) || c.PlacedOrderID(o.OrderID) {
			c.brokerIDNamespaceMu.Unlock()
			return definitelyUnsent(fmt.Errorf("attached order ID already in use"))
		}
	}
	for _, o := range orders {
		_, fresh, err := conn.claimOrderIDForForwardingAtEpochFresh(o.OrderID, false, &binding.epoch, nil)
		if err != nil || !fresh {
			for _, p := range orders {
				conn.discardOrderIDReservation(p.OrderID)
			}
			c.brokerIDNamespaceMu.Unlock()
			if err == nil {
				err = fmt.Errorf("attached order ID must be newly reserved")
			}
			return definitelyUnsent(err)
		}
		c.orderIDHighWater = max(c.orderIDHighWater, o.OrderID)
	}
	defer func() {
		for _, o := range orders {
			conn.discardOrderIDReservation(o.OrderID)
		}
	}()
	c.orderMu.Lock()
	// References are reserved by the durable pair journal. Refuse aliasing rather
	// than overwriting a pending entry belonging to another operation.
	for _, o := range orders {
		_, refExists := c.openOrders[o.OrderRef]
		_, idExists := c.brokerOrderIndex[strconv.Itoa(o.OrderID)]
		if refExists || idExists {
			c.orderMu.Unlock()
			c.brokerIDNamespaceMu.Unlock()
			return definitelyUnsent(fmt.Errorf("attached order reference already in use"))
		}
	}
	for _, o := range orders {
		id := strconv.Itoa(o.OrderID)
		now := time.Now()
		c.openOrders[o.OrderRef] = &trackedOrder{ID: o.OrderRef, BrokerID: id, Symbol: o.Symbol, Side: OrderSide(o.Action), Quantity: float64(o.TotalQty), OrderType: mapIBOrderType(o.OrderType), LimitPrice: o.LmtPrice, StopPrice: o.AuxPrice, TimeInForce: mapIBTimeInForce(o.TIF), Status: OrderStatusPending, CreatedAt: now, UpdatedAt: now}
		c.brokerOrderIndex[id] = o.OrderRef
	}
	c.orderMu.Unlock()
	c.brokerIDNamespaceMu.Unlock()
	err := conn.sendBoundFrames(ctx, frames[0], RequestTypeOrder, binding.epoch, true, guard, 0, frames[1:])
	c.orderMu.Lock()
	if err != nil && SendDispositionOf(err) == SendDispositionDefinitelyUnsent {
		for _, o := range orders {
			delete(c.openOrders, o.OrderRef)
			delete(c.brokerOrderIndex, strconv.Itoa(o.OrderID))
		}
	} else {
		if c.placedOrderIDs == nil {
			c.placedOrderIDs = make(map[int]struct{})
		}
		for _, o := range orders {
			c.placedOrderIDs[o.OrderID] = struct{}{}
		}
	}
	c.orderMu.Unlock()
	// Never claim Submitted from a socket write; exact callbacks own lifecycle.
	return err
}

func isPositiveFiniteStockPrice(p float64) bool { return p > 0 && !math.IsNaN(p) && !math.IsInf(p, 0) }
