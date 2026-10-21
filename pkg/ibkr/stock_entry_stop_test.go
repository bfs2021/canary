//go:build trading

package ibkr

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"testing"
)

func TestStockEntryStopUsesHeldParentAndOneGuard(t *testing.T) {
	_, c, socket, _, gate := newQueuedInstructionReconnectFixture(t)
	session, _ := c.CaptureSession()
	parentID, err := c.ReserveOrderIDForSession(session)
	if err != nil {
		t.Fatal(err)
	}
	childID, err := c.ReserveOrderIDForSession(session)
	if err != nil {
		t.Fatal(err)
	}
	entry := &RawOrder{OrderID: parentID, ClientID: gate.ClientID, Account: gate.Account, OrderRef: "test-entry", Action: "BUY", OrderType: "LMT", LmtPrice: 80, TotalQty: 10, TIF: "DAY"}
	calls := 0
	err = c.SubmitPaperStockEntryStopForSessionGuarded(context.Background(), session, gate, &Contract{ConID: 10001, Symbol: "SYNX", SecType: "STK", Exchange: "SMART", Currency: "USD"}, entry, childID, "test-child", 76, func() error { calls++; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("guards %d", calls)
	}
	data := socket.Bytes()
	var frames [][]byte
	for len(data) > 0 {
		if len(data) < 4 {
			t.Fatal("partial prefix")
		}
		n := int(binary.BigEndian.Uint32(data[:4]))
		if n > len(data)-4 {
			t.Fatal("partial frame")
		}
		frames = append(frames, data[4:4+n])
		data = data[4+n:]
	}
	if len(frames) != 2 {
		t.Fatalf("frames %d", len(frames))
	}
	a, err := parsePlaceOrderProtoSummary(frames[0][4:])
	if err != nil {
		t.Fatal(err)
	}
	b, err := parsePlaceOrderProtoSummary(frames[1][4:])
	if err != nil {
		t.Fatal(err)
	}
	if a.quantity != "10" || b.quantity != "10" || a.lmtPrice != 80 || b.auxPrice != 76 || b.parentID != parentID || b.triggerMethod != 2 || b.outsideRth || a.transmit || a.whatIf || a.action != "BUY" || a.orderType != "LMT" || b.whatIf || !b.transmit || b.action != "SELL" || b.orderType != "STP" || b.tif != "GTC" {
		t.Fatalf("unexpected pair %+v %+v", a, b)
	}
	if !c.PlacedOrderID(parentID) || !c.PlacedOrderID(childID) {
		t.Fatal("pair identity not retained")
	}
	before := len(socket.Bytes())
	err = c.SubmitPaperStockEntryStopForSessionGuarded(context.Background(), session, gate, &Contract{ConID: 10001, Symbol: "SYNX", SecType: "STK", Exchange: "SMART", Currency: "USD"}, entry, childID, "test-child", 76, nil)
	if err == nil || len(socket.Bytes()) != before {
		t.Fatal("replay was not refused")
	}
}

func TestStockEntryStopGuardRefusalWritesNeitherLeg(t *testing.T) {
	_, c, socket, _, gate := newQueuedInstructionReconnectFixture(t)
	session, _ := c.CaptureSession()
	a, _ := c.ReserveOrderIDForSession(session)
	b, _ := c.ReserveOrderIDForSession(session)
	err := c.SubmitPaperStockEntryStopForSessionGuarded(context.Background(), session, gate, &Contract{ConID: 10001, Symbol: "SYNX", SecType: "STK", Exchange: "SMART", Currency: "USD"}, &RawOrder{OrderID: a, ClientID: gate.ClientID, Account: gate.Account, OrderRef: "test-entry", Action: "BUY", OrderType: "LMT", LmtPrice: 80, TotalQty: 10, TIF: "DAY"}, b, "test-child", 76, func() error { return errors.New("authority drift") })
	if err == nil || SendDispositionOf(err) != SendDispositionDefinitelyUnsent || len(socket.Bytes()) != 0 || c.PlacedOrderID(a) || c.PlacedOrderID(b) {
		t.Fatalf("guard refusal: %v", err)
	}
}

type firstPairFrameOnly struct{ bytes []byte }

func (w *firstPairFrameOnly) Write(p []byte) (int, error) {
	n := 4 + int(binary.BigEndian.Uint32(p[:4]))
	if n > len(p) {
		return 0, io.ErrUnexpectedEOF
	}
	w.bytes = append(w.bytes, p[:n]...)
	return n, io.ErrClosedPipe
}
func TestStockEntryStopPartialTransportIsUncertainAndNeverRetried(t *testing.T) {
	conn, c, _, _, gate := newQueuedInstructionReconnectFixture(t)
	writer := &firstPairFrameOnly{}
	conn.writer = bufio.NewWriterSize(writer, 65536)
	session, _ := c.CaptureSession()
	a, _ := c.ReserveOrderIDForSession(session)
	b, _ := c.ReserveOrderIDForSession(session)
	entry := &RawOrder{OrderID: a, ClientID: gate.ClientID, Account: gate.Account, OrderRef: "partial-entry", Action: "BUY", OrderType: "LMT", LmtPrice: 80, TotalQty: 10, TIF: "DAY"}
	contract := &Contract{ConID: 10001, Symbol: "SYNX", SecType: "STK", Exchange: "SMART", Currency: "USD"}
	err := c.SubmitPaperStockEntryStopForSessionGuarded(context.Background(), session, gate, contract, entry, b, "partial-stop", 76, nil)
	if err == nil || SendDispositionOf(err) != SendDispositionMayHaveWritten || !c.PlacedOrderID(a) || !c.PlacedOrderID(b) {
		t.Fatalf("lost uncertain pair: %v", err)
	}
	before := len(writer.bytes)
	err = c.SubmitPaperStockEntryStopForSessionGuarded(context.Background(), session, gate, contract, entry, b, "partial-stop", 76, nil)
	if err == nil || len(writer.bytes) != before {
		t.Fatal("uncertain pair retried")
	}
}
