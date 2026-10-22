package daemon

import (
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/osauer/canary/v2/internal/rpc"
	"github.com/osauer/canary/v2/pkg/ibkr"
)

// SecDef pools strikes across expiries: 772/773/774 are listed but this
// expiry has only five-dollar strikes. Resolving the nearest listed strikes
// one contract at a time drops the phantoms before any quote slot is used.
func TestNearestResolvedCallsDropPhantomStrikes(t *testing.T) {
	var mu sync.Mutex
	var asked []float64
	resolve := func(k float64) (ibkr.ResolvedOrderContract, error) {
		mu.Lock()
		asked = append(asked, k)
		mu.Unlock()
		if int(k)%5 != 0 {
			return ibkr.ResolvedOrderContract{}, errors.New("no definition")
		}
		return ibkr.ResolvedOrderContract{Contract: ibkr.Contract{ConID: 10000 + int(k), Strike: k}}, nil
	}
	listed := []float64{700, 765, 770, 772, 773, 774, 775, 780, 785, 900}
	got := nearestResolvedCalls(listed, 772.94, resolve)
	strikes := []float64{}
	for _, c := range got {
		strikes = append(strikes, c.Contract.Strike)
	}
	if !slices.Equal(strikes, []float64{775, 770, 780, 765, 785}) {
		t.Fatalf("resolved %v, want the real strikes nearest spot first", strikes)
	}
	if len(asked) != addResolveWidth || slices.Contains(asked, 700) || slices.Contains(asked, 900) {
		t.Fatalf("asked %v, want only the %d nearest listed strikes", asked, addResolveWidth)
	}
	now := time.Now()
	row := selectAddCall(got, 772.94, func(exact ibkr.ResolvedOrderContract) rpc.AddCallCandidate {
		delta := map[float64]float64{765: .75, 770: .62, 775: .47, 780: .35}[exact.Contract.Strike]
		contract := setupOptionContract(exact.Contract)
		return rpc.AddCallCandidate{Contract: &contract, PriceIncrements: []rpc.SetupOptionPriceIncrement{{LowEdge: 0, Increment: .01}}, Quote: &rpc.SetupOptionQuote{Status: rpc.SetupQuoteQuoted, DataType: rpc.MarketDataLive, ModelDataType: rpc.MarketDataLive, AsOf: now, ModelAsOf: now, IV: new(.2), Delta: &delta}}
	})
	if row.Contract == nil || row.Contract.Strike != 770 {
		t.Fatalf("selected %+v, want 770", row.Contract)
	}
}

// waitAll returns a func that blocks until n callers arrived, or fails.
func waitAll(t *testing.T, n int) func() {
	var mu sync.Mutex
	arrived, all := 0, make(chan struct{})
	return func() {
		mu.Lock()
		if arrived++; arrived == n {
			close(all)
		}
		mu.Unlock()
		select {
		case <-all:
		case <-time.After(2 * time.Second):
			t.Errorf("only %d of %d ran at once", arrived, n)
		}
	}
}

func TestAddCallSelectionQuotesItsThreeCandidatesAtOnce(t *testing.T) {
	calls := make([]ibkr.ResolvedOrderContract, 5)
	for i := range calls {
		calls[i].Contract.Strike = 100 + float64(i)
	}
	barrier := waitAll(t, 3)
	selectAddCall(calls, 100, func(ibkr.ResolvedOrderContract) rpc.AddCallCandidate {
		barrier()
		return rpc.AddCallCandidate{Unavailable: "test"}
	})
}

func TestAddDiscoveryRunsAllFourHorizonsAtOnce(t *testing.T) {
	barrier := waitAll(t, 4)
	addParallel(4, addHorizonSlots, func(int) { barrier() })
}

func sortedStrikes(s []float64) []float64 { return slices.Sorted(slices.Values(s)) }
