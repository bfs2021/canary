//go:build trading

package ibkr

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"
)

// inheritedStopFixture is a Connector on client 41 of DU7654321 whose socket
// generation started at nextValidId 100 and whose connect download reported
// order 40, a GTC trailing stop an earlier process placed (permanent ID
// 700000, reference ord-40). Outbound frames land in the returned buffer.
func inheritedStopFixture(t *testing.T) (*Connection, *Connector, *safeBuffer, PaperOrderGate) {
	t.Helper()
	conn, connector, socket, _, gate := newQueuedInstructionReconnectFixture(t)
	conn.processMessage(encodeOpenOrderProtoCallbackForTest(inheritedStopCallback(100, false)))
	if _, ok := conn.ReportedOwnOrder(40); !ok {
		t.Fatal("the connect download did not record order 40")
	}
	return conn, connector, socket, gate
}

func inheritedStopCallback(qty int, whatIf bool) testOpenOrderProtoCallback {
	return testOpenOrderProtoCallback{
		OrderID: 40, PermID: 700000, ClientID: 41, Symbol: "SYNX", SecType: "STK", Exchange: "SMART", PrimaryExch: "NASDAQ",
		Currency: "USD", LocalSymbol: "SYNX", TradingClass: "NMS", Action: "SELL", Quantity: strconv.Itoa(qty), OrderType: "TRAIL",
		TrailingPercent: 8, TrailStopPrice: 92, TIF: "GTC", Account: "DU7654321", OrderRef: "ord-40",
		WhatIf: whatIf, Transmit: true, Status: "PreSubmitted",
	}
}

func resetSocket(s *safeBuffer) {
	s.mu.Lock()
	s.buf.Reset()
	s.mu.Unlock()
}

func inheritedStopRawOrder(qty int, change *OrderChangeIdentity) (*Contract, *RawOrder) {
	contract := &Contract{Symbol: "SYNX", SecType: "STK", Exchange: "SMART", PrimaryExch: "NASDAQ", Currency: "USD"}
	order := &RawOrder{
		OrderID: 40, ClientID: 41, Action: "SELL", TotalQty: qty, OrderType: "TRAIL", TrailingPercent: 8, TrailStopPrice: 92,
		TIF: "GTC", OrderRef: "ord-40", Account: "DU7654321", Changes: change,
	}
	return contract, order
}

// previewInherited runs the modify preview of order 40 at qty on binding and
// answers its what-if, as the broker does.
func previewInherited(t *testing.T, conn *Connection, connector *Connector, socket *safeBuffer, binding ConnectorSessionBinding, qty int, change *OrderChangeIdentity) (OrderWhatIfResult, error) {
	t.Helper()
	resetSocket(socket)
	contract, order := inheritedStopRawOrder(qty, change)
	type outcome struct {
		result OrderWhatIfResult
		err    error
	}
	done := make(chan outcome, 1)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	go func() {
		result, err := connector.PreviewOrderWhatIfWithOrderIDForSession(ctx, binding, contract, order, 40)
		done <- outcome{result, err}
	}()
	deadline := time.Now().Add(time.Second)
	for socket.Len() == 0 && time.Now().Before(deadline) {
		select {
		case got := <-done:
			return got.result, got.err
		case <-time.After(5 * time.Millisecond):
		}
	}
	if socket.Len() == 0 {
		got := <-done
		return got.result, got.err
	}
	conn.processMessage(encodeOpenOrderProtoCallbackForTest(inheritedStopCallback(qty, true)))
	got := <-done
	return got.result, got.err
}

func sentPlaceOrder(t *testing.T, socket *safeBuffer) placeOrderProtoSummary {
	t.Helper()
	payload := extractFramePayload(t, socket)
	summary, err := parsePlaceOrderProtoSummary(payload[4:])
	if err != nil {
		t.Fatalf("parse sent placeOrder: %v", err)
	}
	return summary
}

// Issue #120 end to end on the Connector, below every daemon seam: a
// restarted client changes the stop an earlier process placed. The modify
// preview's what-if of order 40 and the modify send both claim the
// inherited ID by naming the order (OrderChangeIdentity) the connect
// download reported; the stop is resized, previewed and resized again, and
// cancelled. A what-if or modify without the identity, or naming another
// permanent ID, is refused at the claim before any byte is written.
func TestRestartedClientResizesAndCancelsAnInheritedStop(t *testing.T) {
	conn, connector, socket, gate := inheritedStopFixture(t)
	binding, ok := connector.CaptureSession()
	if !ok {
		t.Fatal("no session binding")
	}
	change := &OrderChangeIdentity{PermID: 700000, Account: "DU7654321", OrderRef: "ord-40"}

	if _, err := previewInherited(t, conn, connector, socket, binding, 60, nil); !errors.Is(err, ErrBrokerIDNamespaceConflict) {
		t.Fatalf("what-if of order 40 without its identity err=%v, want the claim refusal", err)
	}
	if _, err := previewInherited(t, conn, connector, socket, binding, 60, &OrderChangeIdentity{PermID: 700009, Account: "DU7654321", OrderRef: "ord-40"}); !errors.Is(err, ErrBrokerIDNamespaceConflict) {
		t.Fatalf("what-if of order 40 naming another permanent ID err=%v, want the claim refusal", err)
	}

	for _, qty := range []int{60, 30} {
		result, err := previewInherited(t, conn, connector, socket, binding, qty, change)
		if err != nil || result.Status != OrderWhatIfStatusAccepted {
			t.Fatalf("modify preview of order 40 at %d: %+v err=%v, want accepted", qty, result, err)
		}
		resetSocket(socket)
		contract, order := inheritedStopRawOrder(qty, change)
		if err := connector.SubmitPaperOrderForSession(binding, gate, contract, order); err != nil {
			t.Fatalf("modify of order 40 to %d: %v", qty, err)
		}
		if sent := sentPlaceOrder(t, socket); sent.orderID != 40 || sent.quantity != strconv.Itoa(qty) || sent.orderRef != "ord-40" || sent.whatIf {
			t.Fatalf("sent modify = %+v, want order 40 at %d", sent, qty)
		}
		conn.processMessage(encodeOpenOrderProtoCallbackForTest(inheritedStopCallback(qty, false)))
	}

	resetSocket(socket)
	contract, wrong := inheritedStopRawOrder(10, &OrderChangeIdentity{PermID: 700009, Account: "DU7654321", OrderRef: "ord-40"})
	if err := connector.SubmitPaperOrderForSession(binding, gate, contract, wrong); !errors.Is(err, ErrBrokerIDNamespaceConflict) || socket.Len() != 0 {
		t.Fatalf("modify naming another permanent ID err=%v wrote %d bytes, want the claim refusal and nothing sent", err, socket.Len())
	}

	resetSocket(socket)
	if err := connector.CancelPaperOrderForSession(binding, gate, 40); err != nil {
		t.Fatalf("cancel of order 40: %v", err)
	}
	if socket.Len() == 0 {
		t.Fatal("cancel of order 40 wrote nothing")
	}
	if connector.PlacedOrderID(40) {
		t.Fatal("order 40 counts as placed by this process after it was only modified")
	}

	// A new order this process places is its own: placed here, and changed
	// later without naming an identity.
	resetSocket(socket)
	contract, fresh := inheritedStopRawOrder(5, nil)
	fresh.OrderID, fresh.OrderRef = 0, "ord-new"
	if err := connector.SubmitPaperOrderForSession(binding, gate, contract, fresh); err != nil {
		t.Fatalf("new order: %v", err)
	}
	placed := sentPlaceOrder(t, socket).orderID
	if placed < 100 || !connector.PlacedOrderID(placed) {
		t.Fatalf("new order ID %d placed=%t, want an ID from the frontier counted as placed here", placed, connector.PlacedOrderID(placed))
	}
	resetSocket(socket)
	contract, change2 := inheritedStopRawOrder(4, nil)
	change2.OrderID, change2.OrderRef = placed, "ord-new"
	if err := connector.SubmitPaperOrderForSession(binding, gate, contract, change2); err != nil {
		t.Fatalf("change of this process's own order %d: %v", placed, err)
	}
}

// A modify naming an order the broker never reported and this process never
// placed is refused before any byte, though its ID lies ahead of the
// frontier, and the ID is not marked placed here (refuter round 3, R3-2).
func TestChangeOfAnUnreportedOrderAheadOfTheFrontierIsRefused(t *testing.T) {
	_, connector, socket, gate := inheritedStopFixture(t)
	binding, ok := connector.CaptureSession()
	if !ok {
		t.Fatal("no session binding")
	}
	resetSocket(socket)
	contract, order := inheritedStopRawOrder(60, &OrderChangeIdentity{PermID: 700150, Account: "DU7654321", OrderRef: "ord-150"})
	order.OrderID, order.OrderRef = 150, "ord-150"
	err := connector.SubmitPaperOrderForSession(binding, gate, contract, order)
	if !errors.Is(err, ErrBrokerIDNamespaceConflict) || socket.Len() != 0 || connector.PlacedOrderID(150) {
		t.Fatalf("change of unreported order 150: err=%v wrote %d bytes placed=%t, want the claim refusal, nothing sent, not placed here", err, socket.Len(), connector.PlacedOrderID(150))
	}
}
