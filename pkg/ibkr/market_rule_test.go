package ibkr

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestRouteMarketRuleUsesExactVenue(t *testing.T) {
	r := ResolvedOrderContract{Contract: Contract{Exchange: "SMART"}, ValidExchanges: "BOX,SMART,CBOE", MarketRuleIDs: "26,27,28"}
	id, err := routeMarketRule(r)
	if err != nil || id != 27 {
		t.Fatalf("route %d %v", id, err)
	}
	for _, ids := range []string{"26,27", "26,0,28", "26,bad,28"} {
		r.MarketRuleIDs = ids
		if _, err := routeMarketRule(r); err == nil {
			t.Fatal("invalid mapping accepted")
		}
	}
}
func TestMarketPriceRulesRejectIncompleteOrInvalidBands(t *testing.T) {
	good := []string{"93", "27", "2", "0", "0.01", "3", "0.05"}
	rows, err := decodePriceIncrements(good, 27)
	if err != nil || len(rows) != 2 || rows[1].Increment != .05 {
		t.Fatalf("%v %v", rows, err)
	}
	for _, bad := range [][]string{good[:6], {"93", "27", "1", "0", "NaN"}, {"93", "27", "2", "0", ".01", "0", ".05"}, {"93", "27", "1", "1", ".01"}, {"93", "28", "1", "0", ".01"}} {
		if _, err := decodePriceIncrements(bad, 27); err == nil {
			t.Fatal("malformed rule accepted")
		}
	}
}

func TestMarketRulesWireRejectsOtherRuleAndEpochAndHonorsCancellation(t *testing.T) {
	for _, mode := range []string{"complete", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			c, conn, out, _ := newMissTestConnector(t)
			c.mu.Lock()
			c.ready = true
			c.mu.Unlock()
			binding, ok := c.CaptureHistoricalSession()
			if !ok {
				t.Fatal("missing session")
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			type result struct {
				rows []PriceIncrement
				err  error
			}
			done := make(chan result, 1)
			go func() {
				rows, err := c.OrderPriceIncrements(ctx, binding, ResolvedOrderContract{Contract: Contract{ConID: 10001, Exchange: "SMART"}, ValidExchanges: "BOX,SMART", MarketRuleIDs: "26,27"})
				done <- result{rows, err}
			}()
			var frame []string
			for deadline := time.Now().Add(time.Second); time.Now().Before(deadline) && frame == nil; {
				for _, f := range decodeOutboundFrames(t, conn, out.Bytes()) {
					if f[0] == "91" {
						frame = f
					}
				}
				if frame == nil {
					time.Sleep(time.Millisecond)
				}
			}
			if len(frame) < 2 || frame[1] != "27" {
				t.Fatalf("wrong request %v", frame)
			}
			conn.dispatchHandlers(msgMarketRule, []string{"93", "28", "1", "0", ".01"}, conn.BrokerSessionEpoch())
			conn.dispatchHandlers(msgMarketRule, []string{"93", "27", "1", "0", ".01"}, conn.BrokerSessionEpoch()+1)
			select {
			case <-done:
				t.Fatal("accepted another rule or session")
			default:
			}
			if mode == "cancel" {
				cancel()
			} else {
				conn.dispatchHandlers(msgMarketRule, []string{"93", "27", "2", "0", ".01", "3", ".05"}, conn.BrokerSessionEpoch())
			}
			got := <-done
			if mode == "cancel" {
				if !errors.Is(got.err, context.Canceled) || got.rows != nil {
					t.Fatalf("cancel result %+v", got)
				}
			} else if got.err != nil || !reflect.DeepEqual(got.rows, []PriceIncrement{{0, .01}, {3, .05}}) {
				t.Fatalf("reply %+v", got)
			}
		})
	}
}
