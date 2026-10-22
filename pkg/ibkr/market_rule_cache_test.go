package ibkr

import (
	"reflect"
	"testing"
	"time"
)

// Price rules are static within a broker session. A second contract on the
// same rule must not wait on another broker round trip.
func TestMarketRulesAreReadOncePerSession(t *testing.T) {
	c, conn, out, _ := newMissTestConnector(t)
	c.mu.Lock()
	c.ready = true
	c.mu.Unlock()
	binding, ok := c.CaptureHistoricalSession()
	if !ok {
		t.Fatal("missing session")
	}
	call := ResolvedOrderContract{Contract: Contract{ConID: 10001, Exchange: "SMART"}, ValidExchanges: "SMART", MarketRuleIDs: "27"}
	requests := func() int {
		n := 0
		for _, f := range decodeOutboundFrames(t, conn, out.Bytes()) {
			if f[0] == "91" {
				n++
			}
		}
		return n
	}
	done := make(chan []PriceIncrement, 1)
	go func() {
		rows, _ := c.OrderPriceIncrements(t.Context(), binding, call)
		done <- rows
	}()
	for deadline := time.Now().Add(time.Second); requests() == 0 && time.Now().Before(deadline); {
		time.Sleep(time.Millisecond)
	}
	conn.processMessageAtEpoch(conn.encodeMsg(msgMarketRule, 27, 2, 0, .01, 3, .05), conn.BrokerSessionEpoch())
	want := []PriceIncrement{{0, .01}, {3, .05}}
	if got := <-done; !reflect.DeepEqual(got, want) {
		t.Fatalf("first read %v", got)
	}
	other := call
	other.Contract.ConID = 10002
	got, err := c.OrderPriceIncrements(t.Context(), binding, other)
	if err != nil || !reflect.DeepEqual(got, want) || requests() != 1 {
		t.Fatalf("second read on the same rule: %v %v requests=%d", got, err, requests())
	}
	got[0].Increment = 9
	if again, _ := c.OrderPriceIncrements(t.Context(), binding, other); again[0].Increment != .01 {
		t.Fatal("a caller's copy changed the cached rule")
	}
}
