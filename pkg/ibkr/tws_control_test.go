package ibkr

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// twsControlGateway is a loopback TWS that speaks the v203 protocol far
// enough for the client-0 control path: the handshake, startAPI, a
// reqOpenOrders bind, cancelOrder, placeOrder and the heartbeat. Each test
// scripts what it holds and how it answers.
type twsControlGateway struct {
	ln net.Listener

	mu         sync.Mutex
	orders     []twsGatewayOrder
	nextID     int
	slotBusy   bool // answer client 0's startAPI with code 326 and hang up
	silentBind bool // list the orders but never send openOrderEnd
	silentAck  bool // accept cancel and place frames and never answer
	refuseCode int  // answer cancel and place with this broker error
	clients    []int
	binds      int
	cancels    []int
	places     []placeOrderProtoSummary
}

// twsGatewayOrder is one manual order TWS holds. Bound is the API order id
// TWS assigns when client 0 binds it; zero until then.
type twsGatewayOrder struct {
	PermID     int
	Bound      int
	Symbol     string
	Action     string
	Quantity   float64
	Filled     float64
	LimitPrice float64
	Status     string
}

func startTWSControlGateway(t *testing.T, orders ...twsGatewayOrder) *twsControlGateway {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	g := &twsControlGateway{ln: ln, orders: orders, nextID: 7}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go g.serve(c)
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return g
}

func (g *twsControlGateway) config() TWSOrderControlConfig {
	return TWSOrderControlConfig{
		Host: "127.0.0.1", Port: g.ln.Addr().(*net.TCPAddr).Port, Mode: "paper", Account: "DU1234567",
		ConnectTimeout: 5 * time.Second, BindTimeout: 2 * time.Second, AckTimeout: 2 * time.Second,
	}
}

type twsGatewaySession struct {
	c  net.Conn
	mu sync.Mutex
}

func (s *twsGatewaySession) write(body []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, _ = s.c.Write(append(binary.BigEndian.AppendUint32(nil, uint32(len(body))), body...))
}

func twsGatewayFrame(msgID int, fields ...string) []byte {
	body := binary.BigEndian.AppendUint32(nil, uint32(msgID))
	for _, f := range fields {
		body = append(body, f...)
		body = append(body, 0)
	}
	return body
}

func twsGatewayReadFrame(r *bufio.Reader) ([]byte, error) {
	var n [4]byte
	if _, err := io.ReadFull(r, n[:]); err != nil {
		return nil, err
	}
	body := make([]byte, binary.BigEndian.Uint32(n[:]))
	_, err := io.ReadFull(r, body)
	return body, err
}

func (g *twsControlGateway) serve(c net.Conn) {
	defer c.Close()
	s := &twsGatewaySession{c: c}
	r := bufio.NewReader(c)
	prefix := make([]byte, 4) // "API\0"
	if _, err := io.ReadFull(r, prefix); err != nil {
		return
	}
	if _, err := twsGatewayReadFrame(r); err != nil { // version descriptor
		return
	}
	s.write([]byte("203\x0020261011 19:36:00 CET\x00"))
	frame, err := twsGatewayReadFrame(r) // startAPI
	if err != nil || len(frame) < 4 || binary.BigEndian.Uint32(frame[:4]) != startAPI {
		return
	}
	fields := splitTWSFields(frame[4:])
	clientID := -1
	if len(fields) > 1 {
		clientID, _ = strconv.Atoi(fields[1])
	}
	g.mu.Lock()
	g.clients = append(g.clients, clientID)
	busy := g.slotBusy
	g.mu.Unlock()
	if busy {
		s.write(twsGatewayFrame(msgErrMsg, "2", "-1", "326", "Unable to connect as the client id is already in use. Retry with a unique client id."))
		return
	}
	s.write(twsGatewayFrame(msgNextValidID, "1", "100"))
	s.write(twsGatewayFrame(msgManagedAccts, "1", "DU1234567"))
	for {
		frame, err := twsGatewayReadFrame(r)
		if err != nil || len(frame) < 4 {
			return
		}
		switch int(binary.BigEndian.Uint32(frame[:4])) {
		case reqCurrentTime:
			s.write(twsGatewayFrame(msgCurrentTime, "1", strconv.FormatInt(time.Now().Unix(), 10)))
		case reqOpenOrders:
			g.answerBind(s)
		case protoCancelOrderMsgID:
			summary, err := parseCancelOrderProtoSummary(frame[4:])
			if err != nil {
				return
			}
			g.answerCancel(s, summary.orderID)
		case protoPlaceOrderMsgID:
			summary, err := parsePlaceOrderProtoSummary(frame[4:])
			if err != nil {
				return
			}
			g.answerPlace(s, summary)
		}
	}
}

func splitTWSFields(body []byte) []string {
	var out []string
	start := 0
	for i, b := range body {
		if b == 0 {
			out = append(out, string(body[start:i]))
			start = i + 1
		}
	}
	return out
}

func (g *twsControlGateway) answerBind(s *twsGatewaySession) {
	g.mu.Lock()
	g.binds++
	for i := range g.orders {
		if g.orders[i].Bound == 0 {
			g.orders[i].Bound = g.nextID
			g.nextID++
		}
	}
	orders := append([]twsGatewayOrder(nil), g.orders...)
	silent := g.silentBind
	g.mu.Unlock()
	for _, o := range orders {
		s.write(twsGatewayOpenOrder(o, 0))
		s.write(twsGatewayOrderStatus(o))
	}
	if !silent {
		s.write(twsGatewayFrame(msgOpenOrderEnd, "1"))
	}
}

func (g *twsControlGateway) answerCancel(s *twsGatewaySession, orderID int) {
	g.mu.Lock()
	g.cancels = append(g.cancels, orderID)
	refuse, silent := g.refuseCode, g.silentAck
	var order *twsGatewayOrder
	for i := range g.orders {
		if g.orders[i].Bound == orderID {
			order = &g.orders[i]
		}
	}
	if order != nil && refuse == 0 && !silent {
		order.Status = "Cancelled"
	}
	var cancelled twsGatewayOrder
	if order != nil {
		cancelled = *order
	}
	g.mu.Unlock()
	switch {
	case refuse != 0:
		s.write(twsGatewayFrame(msgErrMsg, "2", strconv.Itoa(orderID), strconv.Itoa(refuse), "Cancel attempted when order is not in a cancellable state"))
	case silent || order == nil:
	default:
		s.write(twsGatewayOrderStatus(cancelled))
	}
}

func (g *twsControlGateway) answerPlace(s *twsGatewaySession, summary placeOrderProtoSummary) {
	g.mu.Lock()
	g.places = append(g.places, summary)
	refuse, silent := g.refuseCode, g.silentAck
	var order *twsGatewayOrder
	for i := range g.orders {
		if g.orders[i].Bound == summary.orderID {
			order = &g.orders[i]
		}
	}
	var replaced twsGatewayOrder
	if order != nil && refuse == 0 && !silent {
		qty, _ := strconv.ParseFloat(summary.quantity, 64)
		order.Quantity, order.LimitPrice, order.Status = qty, summary.lmtPrice, "Submitted"
		replaced = *order
	}
	g.mu.Unlock()
	switch {
	case refuse != 0:
		s.write(twsGatewayFrame(msgErrMsg, "2", strconv.Itoa(summary.orderID), strconv.Itoa(refuse), "Order rejected"))
	case silent || order == nil:
	default:
		s.write(twsGatewayOpenOrder(replaced, 0))
		s.write(twsGatewayOrderStatus(replaced))
	}
}

func (g *twsControlGateway) snapshot() (clients []int, binds int, cancels []int, places []placeOrderProtoSummary) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]int(nil), g.clients...), g.binds, append([]int(nil), g.cancels...), append([]placeOrderProtoSummary(nil), g.places...)
}

func twsGatewayOpenOrder(o twsGatewayOrder, clientID int) []byte {
	return encodeOpenOrderProtoCallbackForTest(testOpenOrderProtoCallback{
		OrderID: o.Bound, PermID: o.PermID, ClientID: clientID,
		Symbol: o.Symbol, SecType: "STK", Exchange: "SMART", PrimaryExch: "NASDAQ", Currency: "USD", LocalSymbol: o.Symbol, TradingClass: "NMS",
		Action: o.Action, Quantity: strconv.FormatFloat(o.Quantity, 'f', -1, 64), OrderType: "LMT", LimitPrice: o.LimitPrice, TIF: "DAY",
		Account: "DU1234567", Transmit: true, Status: o.Status,
	})
}

// twsGatewayOrderStatus encodes an orderStatus protocol-buffer callback with
// the field numbers parseOrderStatusProtoCallback reads.
func twsGatewayOrderStatus(o twsGatewayOrder) []byte {
	var body []byte
	body = protoAppendInt32(body, 1, int32(o.Bound))
	body = protoAppendString(body, 2, o.Status)
	body = protoAppendString(body, 3, strconv.FormatFloat(o.Filled, 'f', -1, 64))
	body = protoAppendString(body, 4, strconv.FormatFloat(o.Quantity-o.Filled, 'f', -1, 64))
	body = protoAppendInt64(body, 6, int64(o.PermID))
	body = protoAppendInt32(body, 9, 0)
	return encodeProtoCallbackFrameForTest(protoOrderStatusMsgID, body)
}

func twsControlTestOrder() twsGatewayOrder {
	return twsGatewayOrder{PermID: 31, Symbol: "AAPL", Action: "BUY", Quantity: 100, LimitPrice: 150, Status: "Submitted"}
}

func twsControlTestTarget() TWSOrderControlTarget {
	return TWSOrderControlTarget{PermID: 31, Symbol: "AAPL", SecType: "STK", Action: "BUY", Quantity: 100, OrderType: "LMT", LimitPrice: 150, TIF: "DAY"}
}

// The happy path: client 0 connects, binds, cancels the one reviewed order,
// hears the broker confirm it and disconnects. The daemon's own client id
// never appears on the wire.
func TestTWSOrderControlCancelsAManualOrder(t *testing.T) {
	g := startTWSControlGateway(t, twsControlTestOrder())
	receipt, err := RunTWSOrderControl(t.Context(), g.config(), TWSOrderControlCancel, twsControlTestTarget(), TWSOrderControlReplacement{})
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if receipt.Outcome != TWSOrderControlConfirmed || receipt.BoundOrderID != 7 || receipt.BrokerStatus != "Cancelled" || receipt.Bound.PermID != 31 {
		t.Fatalf("receipt = %+v, want confirmed cancel of bound order 7", receipt)
	}
	if receipt.ConnectedAt.IsZero() || receipt.DisconnectedAt.Before(receipt.ConnectedAt) || receipt.ServerVersion != 203 {
		t.Fatalf("receipt session = %+v, want connect then disconnect on v203", receipt)
	}
	clients, binds, cancels, places := g.snapshot()
	if len(clients) != 1 || clients[0] != 0 || binds != 1 || len(cancels) != 1 || cancels[0] != 7 || len(places) != 0 {
		t.Fatalf("gateway saw clients=%v binds=%d cancels=%v places=%d, want one client-0 bind and one cancel", clients, binds, cancels, len(places))
	}
}

// A modify sends the bound order back under its bound id with the new
// quantity and limit, as client 0, and confirms on the echo of those terms.
func TestTWSOrderControlModifiesAManualOrder(t *testing.T) {
	g := startTWSControlGateway(t, twsControlTestOrder())
	receipt, err := RunTWSOrderControl(t.Context(), g.config(), TWSOrderControlModify, twsControlTestTarget(), TWSOrderControlReplacement{Quantity: 60, LimitPrice: 149.5})
	if err != nil {
		t.Fatalf("modify: %v", err)
	}
	if receipt.Outcome != TWSOrderControlConfirmed || receipt.BoundOrderID != 7 {
		t.Fatalf("receipt = %+v, want confirmed modify of bound order 7", receipt)
	}
	_, _, cancels, places := g.snapshot()
	if len(cancels) != 0 || len(places) != 1 {
		t.Fatalf("gateway saw cancels=%v places=%d, want exactly one place", cancels, len(places))
	}
	p := places[0]
	if p.orderID != 7 || p.clientID != 0 || p.symbol != "AAPL" || p.action != "BUY" || p.quantity != "60" || p.lmtPrice != 149.5 || p.orderType != "LMT" || p.tif != "DAY" || p.account != "DU1234567" || !p.transmit || p.whatIf {
		t.Fatalf("replacement frame = %+v, want bound id 7 as client 0 with qty 60 at 149.5", p)
	}
}

// The order TWS holds differs from what the owner reviewed: nothing is sent.
func TestTWSOrderControlRefusesChangedTerms(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*TWSOrderControlTarget)
		want   string
	}{
		{"limit", func(t *TWSOrderControlTarget) { t.LimitPrice = 151 }, "limit price is 150, reviewed 151"},
		{"quantity", func(t *TWSOrderControlTarget) { t.Quantity = 90 }, "quantity is 100, reviewed 90"},
		{"side", func(t *TWSOrderControlTarget) { t.Action = "SELL" }, "side is BUY, reviewed SELL"},
		{"contract", func(t *TWSOrderControlTarget) { t.Symbol = "MSFT" }, "symbol is AAPL, reviewed MSFT"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := startTWSControlGateway(t, twsControlTestOrder())
			target := twsControlTestTarget()
			tc.change(&target)
			receipt, err := RunTWSOrderControl(t.Context(), g.config(), TWSOrderControlCancel, target, TWSOrderControlReplacement{})
			if !errors.Is(err, ErrTWSOrderControlChanged) || receipt.Outcome != TWSOrderControlRefused {
				t.Fatalf("err=%v outcome=%s, want a changed-terms refusal", err, receipt.Outcome)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("reason = %q, want it to say %q", err, tc.want)
			}
			_, binds, cancels, places := g.snapshot()
			if binds != 1 || len(cancels) != 0 || len(places) != 0 {
				t.Fatalf("gateway saw binds=%d cancels=%v places=%d, want the bind only", binds, cancels, len(places))
			}
		})
	}
}

// An order that filled, was cancelled, or is not among client 0's orders is
// refused before any write.
func TestTWSOrderControlRefusesAnOrderThatIsGoneOrFilled(t *testing.T) {
	filled := twsControlTestOrder()
	filled.Filled, filled.Status = 100, "Filled"
	for _, tc := range []struct {
		name   string
		orders []twsGatewayOrder
		want   error
	}{
		{"filled", []twsGatewayOrder{filled}, ErrTWSOrderControlNotWorking},
		{"absent", nil, ErrTWSOrderControlNotFound},
		{"another order", []twsGatewayOrder{{PermID: 32, Symbol: "AAPL", Action: "BUY", Quantity: 100, LimitPrice: 150, Status: "Submitted"}}, ErrTWSOrderControlNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := startTWSControlGateway(t, tc.orders...)
			receipt, err := RunTWSOrderControl(t.Context(), g.config(), TWSOrderControlCancel, twsControlTestTarget(), TWSOrderControlReplacement{})
			if !errors.Is(err, tc.want) || receipt.Outcome != TWSOrderControlRefused {
				t.Fatalf("err=%v outcome=%s, want %v", err, receipt.Outcome, tc.want)
			}
			_, _, cancels, places := g.snapshot()
			if len(cancels) != 0 || len(places) != 0 {
				t.Fatalf("gateway saw cancels=%v places=%d, want none", cancels, len(places))
			}
		})
	}
}

// Another API client holds id 0: TWS answers 326 and the path refuses
// without binding or writing.
func TestTWSOrderControlRefusesWhenClientZeroIsBusy(t *testing.T) {
	g := startTWSControlGateway(t, twsControlTestOrder())
	g.mu.Lock()
	g.slotBusy = true
	g.mu.Unlock()
	receipt, err := RunTWSOrderControl(t.Context(), g.config(), TWSOrderControlCancel, twsControlTestTarget(), TWSOrderControlReplacement{})
	if !errors.Is(err, ErrTWSOrderControlSlotBusy) || receipt.Outcome != TWSOrderControlRefused {
		t.Fatalf("err=%v outcome=%s, want the busy-slot refusal", err, receipt.Outcome)
	}
	clients, binds, cancels, _ := g.snapshot()
	if len(clients) != 1 || clients[0] != 0 || binds != 0 || len(cancels) != 0 {
		t.Fatalf("gateway saw clients=%v binds=%d cancels=%v, want one refused client-0 attempt", clients, binds, cancels)
	}
}

// A bind that never ends is a refusal: the order is not identified, so
// nothing is sent.
func TestTWSOrderControlRefusesAnIncompleteBind(t *testing.T) {
	g := startTWSControlGateway(t, twsControlTestOrder())
	g.mu.Lock()
	g.silentBind = true
	g.mu.Unlock()
	cfg := g.config()
	cfg.BindTimeout = 300 * time.Millisecond
	receipt, err := RunTWSOrderControl(t.Context(), cfg, TWSOrderControlCancel, twsControlTestTarget(), TWSOrderControlReplacement{})
	if !errors.Is(err, ErrTWSOrderControlBind) || receipt.Outcome != TWSOrderControlRefused {
		t.Fatalf("err=%v outcome=%s, want the bind refusal", err, receipt.Outcome)
	}
	_, _, cancels, _ := g.snapshot()
	if len(cancels) != 0 {
		t.Fatalf("gateway saw cancels=%v, want none", cancels)
	}
}

// A sent request without an answer is uncertain: reported as such, sent
// exactly once, never retried.
func TestTWSOrderControlSilenceAfterTheSendIsUncertain(t *testing.T) {
	for _, action := range []string{TWSOrderControlCancel, TWSOrderControlModify} {
		t.Run(action, func(t *testing.T) {
			g := startTWSControlGateway(t, twsControlTestOrder())
			g.mu.Lock()
			g.silentAck = true
			g.mu.Unlock()
			cfg := g.config()
			cfg.AckTimeout = 300 * time.Millisecond
			receipt, err := RunTWSOrderControl(t.Context(), cfg, action, twsControlTestTarget(), TWSOrderControlReplacement{Quantity: 50, LimitPrice: 149})
			if !errors.Is(err, ErrTWSOrderControlUncertain) || receipt.Outcome != TWSOrderControlUncertain || receipt.BoundOrderID != 7 {
				t.Fatalf("err=%v receipt=%+v, want an uncertain outcome on bound order 7", err, receipt)
			}
			_, _, cancels, places := g.snapshot()
			if got := len(cancels) + len(places); got != 1 {
				t.Fatalf("gateway saw cancels=%v places=%d, want exactly one send", cancels, len(places))
			}
		})
	}
}

// A broker error for the bound order is a refusal with the broker's code;
// the order stays as it was.
func TestTWSOrderControlBrokerRefusalCarriesTheCode(t *testing.T) {
	g := startTWSControlGateway(t, twsControlTestOrder())
	g.mu.Lock()
	g.refuseCode = 161
	g.mu.Unlock()
	receipt, err := RunTWSOrderControl(t.Context(), g.config(), TWSOrderControlCancel, twsControlTestTarget(), TWSOrderControlReplacement{})
	if !errors.Is(err, ErrTWSOrderControlRefused) || receipt.Outcome != TWSOrderControlRefused || receipt.BrokerCode != 161 || receipt.BrokerMessage == "" {
		t.Fatalf("err=%v receipt=%+v, want a refusal carrying code 161", err, receipt)
	}
}

// Replaying a confirmed cancel finds no working order: the second run binds
// again, sees the order cancelled and sends nothing.
func TestTWSOrderControlReplayAfterACancelSendsNothing(t *testing.T) {
	g := startTWSControlGateway(t, twsControlTestOrder())
	if _, err := RunTWSOrderControl(t.Context(), g.config(), TWSOrderControlCancel, twsControlTestTarget(), TWSOrderControlReplacement{}); err != nil {
		t.Fatalf("first cancel: %v", err)
	}
	receipt, err := RunTWSOrderControl(t.Context(), g.config(), TWSOrderControlCancel, twsControlTestTarget(), TWSOrderControlReplacement{})
	if !errors.Is(err, ErrTWSOrderControlNotWorking) || receipt.Outcome != TWSOrderControlRefused {
		t.Fatalf("replay err=%v outcome=%s, want a not-working refusal", err, receipt.Outcome)
	}
	clients, binds, cancels, _ := g.snapshot()
	if len(clients) != 2 || binds != 2 || len(cancels) != 1 {
		t.Fatalf("gateway saw clients=%v binds=%d cancels=%v, want two sessions and one cancel", clients, binds, cancels)
	}
}

// The gate refuses what the path must never do, before any socket is opened.
func TestTWSOrderControlGateRefusesBeforeConnecting(t *testing.T) {
	g := startTWSControlGateway(t, twsControlTestOrder())
	base := g.config()
	live := base
	live.Mode, live.Account = "live", "U1234567"
	paperOnLive := base
	paperOnLive.Account = "U1234567"
	noEndpoint := base
	noEndpoint.Port = 0
	for _, tc := range []struct {
		name        string
		cfg         TWSOrderControlConfig
		action      string
		target      TWSOrderControlTarget
		replacement TWSOrderControlReplacement
		skipOn      bool
	}{
		{name: "paper needs a DU account", cfg: paperOnLive, action: TWSOrderControlCancel, target: twsControlTestTarget()},
		{name: "endpoint required", cfg: noEndpoint, action: TWSOrderControlCancel, target: twsControlTestTarget()},
		{name: "perm id required", cfg: base, action: TWSOrderControlCancel, target: TWSOrderControlTarget{Symbol: "AAPL", Action: "BUY", Quantity: 1, OrderType: "LMT"}},
		{name: "unknown action", cfg: base, action: "replace", target: twsControlTestTarget()},
		{name: "modify needs LMT", cfg: base, action: TWSOrderControlModify, target: func() TWSOrderControlTarget {
			t := twsControlTestTarget()
			t.OrderType = "TRAIL"
			return t
		}(), replacement: TWSOrderControlReplacement{Quantity: 1, LimitPrice: 1}},
		{name: "modify cannot grow", cfg: base, action: TWSOrderControlModify, target: twsControlTestTarget(), replacement: TWSOrderControlReplacement{Quantity: 101, LimitPrice: 1}},
		{name: "live needs the trading build", cfg: live, action: TWSOrderControlCancel, target: twsControlTestTarget(), skipOn: tradingEnabled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.skipOn {
				t.Skip("gate passes in this build")
			}
			receipt, err := RunTWSOrderControl(t.Context(), tc.cfg, tc.action, tc.target, tc.replacement)
			if !errors.Is(err, ErrTWSOrderControlGate) || receipt.Outcome != TWSOrderControlRefused {
				t.Fatalf("err=%v outcome=%s, want a gate refusal", err, receipt.Outcome)
			}
		})
	}
	clients, _, _, _ := g.snapshot()
	if len(clients) != 0 {
		t.Fatalf("gateway saw clients=%v, want no connection", clients)
	}
}

// A cancelled context before the connect is a gate refusal, not an attempt.
func TestTWSOrderControlRefusesACancelledContext(t *testing.T) {
	g := startTWSControlGateway(t, twsControlTestOrder())
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	receipt, err := RunTWSOrderControl(ctx, g.config(), TWSOrderControlCancel, twsControlTestTarget(), TWSOrderControlReplacement{})
	if !errors.Is(err, ErrTWSOrderControlGate) || receipt.Outcome != TWSOrderControlRefused {
		t.Fatalf("err=%v outcome=%s, want a gate refusal", err, receipt.Outcome)
	}
}
