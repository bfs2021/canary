package daemon

import (
	"errors"
	"slices"
	"strings"
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

// Owner, 2026-10-22 20:34 CEST: lower call delta for longer expiries.
func TestAddDeltaBandsFollowDaysToExpiry(t *testing.T) {
	for _, tc := range []struct {
		dte      int
		min, max float64
	}{{13, .5, .7}, {21, .5, .7}, {22, .35, .5}, {29, .35, .5}, {45, .35, .5}, {57, .25, .4}, {120, .25, .4}} {
		if b := addDeltaBandForDTE(tc.dte); b.Min != tc.min || b.Max != tc.max {
			t.Errorf("%d days: %+v, want %v–%v", tc.dte, b, tc.min, tc.max)
		}
	}
}

// A long-dated row keeps the in-band call nearest its cap: the cheapest call
// that still carries the owner's maximum delta.
func TestAddLongHorizonPicksTheDeltaNearestItsCap(t *testing.T) {
	expiry := time.Now().AddDate(0, 0, 120).Format("20060102")
	deltas := map[float64]float64{325: .55, 330: .52, 350: .42, 355: .39, 360: .36}
	calls := []ibkr.ResolvedOrderContract{}
	for k := range deltas {
		calls = append(calls, ibkr.ResolvedOrderContract{Contract: ibkr.Contract{ConID: int(k), Strike: k, Expiry: expiry}})
	}
	now := time.Now()
	quote := func(exact ibkr.ResolvedOrderContract) rpc.AddCallCandidate {
		contract := setupOptionContract(exact.Contract)
		return rpc.AddCallCandidate{Contract: &contract, PriceIncrements: []rpc.SetupOptionPriceIncrement{{LowEdge: 0, Increment: .01}}, Quote: &rpc.SetupOptionQuote{Status: rpc.SetupQuoteQuoted, DataType: rpc.MarketDataLive, ModelDataType: rpc.MarketDataLive, AsOf: now, ModelAsOf: now, IV: new(.42), Delta: new(deltas[exact.Contract.Strike])}}
	}
	if row, iv := selectAddCallNear(calls, 325.5, quote); row.Contract != nil || iv != .42 {
		t.Fatalf("at-the-money calls are above the 0.40 cap, want none and the volatility to aim with: %+v %v", row.Contract, iv)
	}
	aim := addAimStrike(325.5, .42, 120, .4)
	if aim < 350 || aim > 360 {
		t.Fatalf("aim %v, want the strike where delta is about 0.40", aim)
	}
	if row, _ := selectAddCallNear(calls, aim, quote); row.Contract == nil || row.Contract.Strike != 355 {
		t.Fatalf("picked %+v, want 355 (0.39, nearest the cap)", row.Contract)
	}
}

// Spot moved so all three calls near it sit above 0.50: the 30-day row must
// aim again, not come back empty (seen live 2026-10-22 21:03 CEST).
func TestAddThirtyDayRowAimsBelowItsCapWhenSpotStrikesAreAbove(t *testing.T) {
	expiry := time.Now().AddDate(0, 0, 29).Format("20060102")
	deltas := map[float64]float64{320: .57, 322.5: .54, 325: .515, 327.5: .49, 330: .465}
	all := []ibkr.ResolvedOrderContract{}
	for k := range deltas {
		all = append(all, ibkr.ResolvedOrderContract{Contract: ibkr.Contract{ConID: int(k * 10), Strike: k, Expiry: expiry}})
	}
	now := time.Now()
	quote := func(exact ibkr.ResolvedOrderContract) rpc.AddCallCandidate {
		contract := setupOptionContract(exact.Contract)
		return rpc.AddCallCandidate{Contract: &contract, PriceIncrements: []rpc.SetupOptionPriceIncrement{{LowEdge: 0, Increment: .01}}, Quote: &rpc.SetupOptionQuote{Status: rpc.SetupQuoteQuoted, DataType: rpc.MarketDataLive, ModelDataType: rpc.MarketDataLive, AsOf: now, ModelAsOf: now, IV: new(.42), Delta: new(deltas[exact.Contract.Strike])}}
	}
	nearSpot := []ibkr.ResolvedOrderContract{all[0], all[1], all[2]}
	for i, c := range all {
		switch c.Contract.Strike {
		case 320, 322.5, 325:
			nearSpot[map[float64]int{320: 0, 322.5: 1, 325: 2}[c.Contract.Strike]] = all[i]
		}
	}
	row, spent := selectAddCallAimed(nearSpot, 322.6, 29, quote, func(float64) []ibkr.ResolvedOrderContract { return all })
	if row.Contract == nil || row.Contract.Strike != 327.5 || !strings.Contains(spent, "aim=") {
		t.Fatalf("picked %+v (%s), want 327.5 at 0.49 after aiming", row.Contract, spent)
	}
}

// A SecDef list can mix 1, 2.5 and 5 USD spacing. A long expiry with only
// five-dollar strikes finds one or two in the first eight; the search widens
// instead of falling back to the slow complete inventory.
func TestNearestResolvedCallsWidenPastMixedSpacing(t *testing.T) {
	listed := []float64{}
	for k := 300.0; k <= 350; k++ {
		listed = append(listed, k)
		if k < 350 {
			listed = append(listed, k+.5)
		}
	}
	var mu sync.Mutex
	asked := 0
	got := nearestResolvedCalls(listed, 323.4, func(k float64) (ibkr.ResolvedOrderContract, error) {
		mu.Lock()
		asked++
		mu.Unlock()
		if int(k*10)%50 != 0 {
			return ibkr.ResolvedOrderContract{}, errors.New("no definition")
		}
		return ibkr.ResolvedOrderContract{Contract: ibkr.Contract{ConID: int(k), Strike: k}}, nil
	})
	strikes := []float64{}
	for _, c := range got {
		strikes = append(strikes, c.Contract.Strike)
	}
	if len(strikes) < 3 || strikes[0] != 325 || strikes[1] != 320 || asked > addResolveWidth*addResolveBatches {
		t.Fatalf("resolved %v after %d lookups, want 325, 320 and a third without the inventory", strikes, asked)
	}
}
