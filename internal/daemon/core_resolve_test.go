package daemon

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/osauer/canary/v2/internal/core"
	"github.com/osauer/canary/v2/internal/rpc"
	ibkrlib "github.com/osauer/canary/v2/pkg/ibkr"
)

// coreFakeListings is a fake broker lookup: it records every request and
// answers with the listings given (or the error).
type coreFakeListings struct {
	asked    []ibkrlib.ListingRequest
	listings []ibkrlib.Listing
	trace    *ibkrlib.BondLookupTrace
	err      error
}

func (f *coreFakeListings) lookup(_ context.Context, r ibkrlib.ListingRequest) ([]ibkrlib.Listing, *ibkrlib.BondLookupTrace, error) {
	f.asked = append(f.asked, r)
	return f.listings, f.trace, f.err
}

func coreFakeBroker() *coreFakeListings {
	return &coreFakeListings{
		listings: []ibkrlib.Listing{
			{ConID: coreTestConID, Symbol: "IBCF1", LocalSymbol: "IBCF", SecType: "STK", Exchange: "IBIS", PrimaryExch: "IBIS", Currency: "EUR", TradingClass: "IBCF", LongName: "Synthetic Index Fund", ISIN: core.ISIN, Forms: []string{"STK by ISIN on SMART"}},
			{ConID: 9031, Symbol: "IUSE", SecType: "STK", Exchange: "SMART", PrimaryExch: "LSEETF", Currency: "EUR", ISIN: core.ISIN, Forms: []string{"STK by ISIN on SMART"}},
		},
		trace: &ibkrlib.BondLookupTrace{Attempts: []ibkrlib.BondLookupAttempt{{Form: "STK by ISIN on SMART", Outcome: ibkrlib.BondAttemptLine, Lines: 2}, {Form: "STK IBCF on IBIS2", Outcome: ibkrlib.BondAttemptRejected, Code: 200, Message: "No security definition has been found for the request"}}},
	}
}

// core.resolve asks by the rule's ISIN with the symbol fallbacks on IBIS,
// IBIS2 and SMART with primary IBIS, lists every listing without choosing,
// marks the pinned one, and takes the ISIN from the flag, else the policy,
// else the rule. An invalid ISIN is refused; no listing is an answer with
// every form's answer, not an error.
func TestCoreResolveListsEveryListing(t *testing.T) {
	c := testCoreConstitution()
	srv, _ := mandateTestServer(t, c)
	fake := coreFakeBroker()
	srv.coreListingsForTest = fake.lookup
	res, err := srv.handleCoreResolve(t.Context(), &rpc.Request{})
	if err != nil || len(res.Listings) != 2 || res.ISIN != core.ISIN || res.ISINSource != "policy" || !res.PinnedFound || !res.Listings[0].Pinned || res.Listings[1].Pinned || len(res.Attempts) != 2 {
		t.Fatalf("resolve %+v err %v", res, err)
	}
	asked := fake.asked[0]
	if asked.ISIN != core.ISIN || !slices.Equal(asked.Symbols, []string{core.Symbol}) || len(asked.Venues) != 3 || asked.Venues[2] != (ibkrlib.ListingVenue{Exchange: "SMART", PrimaryExch: "IBIS"}) {
		t.Fatalf("asked %+v", asked)
	}
	if !strings.Contains(res.Message, "nothing to write") {
		t.Fatalf("message %q", res.Message)
	}
	// Unpinned: several listings, none chosen.
	c.Mandate.Core.ConID = new(0)
	res, _ = srv.handleCoreResolve(t.Context(), &rpc.Request{})
	if res.PinnedConID != nil || res.PinnedFound || !strings.Contains(res.Message, "none is chosen for you") {
		t.Fatalf("unpinned %+v", res)
	}
	// The flag's ISIN, another fund: no symbol fallback, the currency sent.
	res, err = srv.handleCoreResolve(t.Context(), &rpc.Request{Params: []byte(`{"isin":"IE00B5BMR087","currency":"eur"}`)})
	if err != nil || res.ISINSource != "flag" || fake.asked[2].ISIN != "IE00B5BMR087" || len(fake.asked[2].Symbols) != 0 || fake.asked[2].Currency != "EUR" {
		t.Fatalf("flag %+v %+v err %v", res, fake.asked[2], err)
	}
	if _, err := srv.handleCoreResolve(t.Context(), &rpc.Request{Params: []byte(`{"isin":"IE00B3ZW0K17"}`)}); err == nil || !strings.Contains(err.Error(), "not an ISIN") {
		t.Fatalf("bad ISIN: %v", err)
	}
	// No listing: the answers are the result.
	fake.listings, fake.err = nil, ibkrlib.ErrNoListing
	res, err = srv.handleCoreResolve(t.Context(), &rpc.Request{})
	if err != nil || len(res.Listings) != 0 || len(res.Attempts) != 2 || !strings.Contains(res.Message, "found no listing") {
		t.Fatalf("none %+v err %v", res, err)
	}
	fake.err = ibkrlib.ErrIBKRUnavailable
	if _, err := srv.handleCoreResolve(t.Context(), &rpc.Request{}); !errors.Is(err, ibkrlib.ErrIBKRUnavailable) {
		t.Fatalf("gateway down: %v", err)
	}
}

// The core's series is read for the pinned contract as IBKR names it: the
// contract id is asked first with no symbol (IBKR's own symbol for a Xetra
// fund need not be the owner's ticker, and a contract id with another
// symbol is answered "no security definition"), and the history read
// carries the contract id, IBKR's symbol, IBIS and the listing's primary
// exchange; normalised for the broker, the route stays on IBIS. The
// resolution is kept, and a failed one holds the core and names itself.
func TestCoreSeriesAsksThePinnedContractAsIBKRNamesIt(t *testing.T) {
	c := testCoreConstitution()
	srv, _ := mandateTestServer(t, c)
	fake := coreFakeBroker()
	srv.coreListingsForTest = fake.lookup
	need, ok := coreHistoryNeed(c)
	if !ok {
		t.Fatal("no core need")
	}
	resolved, ok := srv.coreResolvedNeed(t.Context(), c, need)
	if !ok {
		t.Fatal("the pinned contract did not resolve")
	}
	asked := fake.asked[0]
	if asked.ConID != coreTestConID || asked.ISIN != "" || len(asked.Symbols) != 0 || asked.Venues[0].Exchange != core.Exchange || asked.Currency != core.Currency {
		t.Fatalf("asked %+v", asked)
	}
	want := rpc.ContractParams{ConID: coreTestConID, Symbol: "IBCF1", SecType: "STK", Currency: "EUR", Exchange: "IBIS", PrimaryExch: "IBIS"}
	if resolved.Key != coreSeriesKey(coreTestConID) || resolved.Contract != want {
		t.Fatalf("need %+v", resolved)
	}
	var got rpc.MarketHistoryParams
	srv.mandateHistoryFetchForTest = func(_ context.Context, p rpc.MarketHistoryParams, _ int, _ time.Time) (*rpc.MarketHistoryResult, error) {
		got = p
		return nil, errors.New("synthetic stop")
	}
	_, _ = srv.fetchMandateSeries(t.Context(), resolved, mandateTestNow)
	if got.Contract != want {
		t.Fatalf("history asked for %+v", got.Contract)
	}
	contract, _, routed, err := normaliseStockQuoteContract(got.Contract)
	if err != nil || !routed || contract.ConID != coreTestConID || contract.Symbol != "IBCF1" || contract.Exchange != "IBIS" || contract.PrimaryExch != "IBIS" || contract.Currency != "EUR" {
		t.Fatalf("broker contract %+v routed %v err %v", contract, routed, err)
	}
	// Kept: the next pass asks nothing.
	if _, ok := srv.coreResolvedNeed(t.Context(), c, need); !ok || len(fake.asked) != 1 {
		t.Fatalf("asked again: %d", len(fake.asked))
	}
	// A pin IBKR does not answer holds the core and names it.
	srv.coreTargets = coreTargetStore{}
	fake.listings, fake.err = nil, ibkrlib.ErrNoListing
	if _, ok := srv.coreResolvedNeed(t.Context(), c, need); ok {
		t.Fatal("an unanswered pin resolved")
	}
	srv.mandatePositionsForTest = func(context.Context) (*rpc.PositionsResult, error) { return mandateTestPositions(100), nil }
	seedMandateHistory(t, srv, 0)
	res := coreEvaluate(t, srv)
	if res.Status != rpc.CoreStatusHold || !strings.Contains(strings.Join(res.Reasons, " "), "the pinned contract 9030 did not resolve on IBIS") {
		t.Fatalf("reasons %v", res.Reasons)
	}
	// A listing in another currency is not the pin.
	srv.coreTargets = coreTargetStore{}
	fake.err = nil
	fake.listings = []ibkrlib.Listing{{ConID: coreTestConID, Symbol: "IBCF", Exchange: "IBIS", Currency: "USD"}}
	if _, ok := srv.coreResolvedNeed(t.Context(), c, need); ok || !strings.Contains(srv.corePinReason(coreTestConID), "lists it in USD") {
		t.Fatalf("another currency: %q", srv.corePinReason(coreTestConID))
	}
}

// With con_id 0 (or unwritten) the core holds with the one thing to do:
// run canary core resolve and write the con_id. 0 is accepted by the
// policy; a negative id is not.
func TestCoreTargetUnpinnedSaysResolve(t *testing.T) {
	for _, conID := range []*int{new(0), nil} {
		c := testCoreConstitution()
		c.Mandate.Core.ConID = conID
		srv, _ := mandateTestServer(t, c)
		seedMandateHistory(t, srv, 0)
		srv.mandatePositionsForTest = func(context.Context) (*rpc.PositionsResult, error) { return mandateTestPositions(100), nil }
		res := coreEvaluate(t, srv)
		reasons := strings.Join(res.Reasons, " | ")
		if res.Status != rpc.CoreStatusHold || !strings.Contains(reasons, "run `canary core resolve` and write the con_id") || strings.Contains(reasons, "mandate.core.con_id") || res.Sizing != nil {
			t.Fatalf("con_id %v: %s %v", conID, res.Status, res.Reasons)
		}
		if _, ok := coreHistoryNeed(c); ok {
			t.Fatal("an unpinned core reads a series")
		}
	}
}
