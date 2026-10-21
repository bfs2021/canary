package daemon

import (
	"github.com/osauer/canary/v2/internal/rpc"
	"testing"
	"time"
)

func TestAddHorizonSkipsWeekAndKeepsDistinctRanges(t *testing.T) {
	now := time.Date(2026, 10, 21, 15, 0, 0, 0, time.UTC)
	rows := []rpc.SetupOptionExpiry{{Date: "20261028"}, {Date: "20261030"}, {Date: "20261120"}, {Date: "20261218"}, {Date: "20270219"}}
	for _, test := range []struct {
		target, min, max int
		want             string
	}{{14, 8, 21, "20261030"}, {30, 22, 45, "20261120"}, {60, 46, 90, "20261218"}, {120, 91, 150, "20270219"}} {
		date, _ := addHorizonExpiry(rows, test.target, test.min, test.max, now)
		if date != test.want {
			t.Fatalf("%d: %s", test.target, date)
		}
	}
}
func TestAddCallNeedsIndependentLiveModelEvidence(t *testing.T) {
	now := time.Now()
	q := &rpc.SetupOptionQuote{Status: "quoted", DataType: rpc.MarketDataLive, AsOf: now, IV: new(.3), Delta: new(.6), ModelAsOf: now, ModelDataType: rpc.MarketDataLive}
	row := rpc.AddCallCandidate{Contract: &rpc.ContractParams{ConID: 10001}, Quote: q, PriceIncrements: []rpc.SetupOptionPriceIncrement{{LowEdge: 0, Increment: .01}}}
	if !eligibleAddCall(row, now) {
		t.Fatal("fresh exact model rejected")
	}
	q.ModelAsOf = now.Add(-16 * time.Second)
	if eligibleAddCall(row, now) {
		t.Fatal("price freshened model")
	}
	q.ModelAsOf = now
	q.ModelDataType = rpc.MarketDataDelayed
	if eligibleAddCall(row, now) {
		t.Fatal("delayed model upgraded")
	}
}
func TestRoundCallQuantityPreservesSmallLotsAndRoundsBothDirections(t *testing.T) {
	for n, want := range map[int]int{0: 0, 1: 1, 2: 2, 3: 5, 6: 5, 7: 5, 8: 10, 9: 10, 10: 10, 12: 10, 13: 15} {
		if got := roundCallQuantity(n); got != want {
			t.Fatalf("%d -> %d, want %d", n, got, want)
		}
	}
}
