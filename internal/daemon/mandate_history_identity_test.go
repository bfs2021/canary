package daemon

import (
	"context"
	"github.com/osauer/canary/v2/internal/mandate"
	"github.com/osauer/canary/v2/internal/rpc"
	"testing"
	"time"
)

func TestHistoryIdentityRetainedFXPairCannotChangeBase(t *testing.T) {
	store := openMarketTestCoreStore(t)
	prior := &Server{coreStore: store, now: func() time.Time { return mandateTestNow }}
	fx := mandateTestSeries("fx:USD", 0.08, 4, 0)
	fx.Contract = rpc.ContractParams{Symbol: "EUR", SecType: "CASH", Currency: "USD", Exchange: "IDEALPRO"}
	for i := range fx.Closes {
		fx.Closes[i] /= 100
	}
	stock := mandateTestSeries("u:SYNA", 0.30, 2, 0)
	stock.Contract = rpc.ContractParams{ConID: 9001, Symbol: "SYNA", SecType: "STK", Currency: "USD"}
	for _, s := range []*mandateHistorySeries{fx, stock} {
		if e := prior.saveMandateSeries(t.Context(), s); e != nil {
			t.Fatal(e)
		}
	}
	srv := &Server{coreStore: store, now: func() time.Time { return mandateTestNow }}
	reads := 0
	srv.mandateHistoryFetchForTest = func(context.Context, rpc.MarketHistoryParams, int, time.Time) (*rpc.MarketHistoryResult, error) {
		reads++
		return nil, nil
	}
	srv.refreshMandateSeries(t.Context(), &fakeSetupSource{}, mandateHistoryNeed{Key: "fx:USD", Contract: rpc.ContractParams{Symbol: "GBP", SecType: "CASH", Currency: "USD", Exchange: "IDEALPRO"}})
	retained := srv.mandateHistorySeries(t.Context(), "fx:USD")
	in := &mandate.Input{BaseCurrency: "GBP", Series: map[string]mandate.Series{}, FX: map[string]mandate.Series{}, FXNow: map[string]float64{}}
	book, missing := srv.mandateOrderBook(mandateBuy(10), in, mandateTestNow)
	t.Logf("requested GBP/USD; actual retained pair=%s/%s reads=%d missing=%v FXNow[USD]=%v derived from old EUR/USD=%v", retained.Contract.Symbol, retained.Contract.Currency, reads, missing, book.FXNow["USD"], 1/fx.Closes[len(fx.Closes)-1])
	if reads == 0 || len(missing) == 0 || book.FXNow["USD"] != 0 {
		t.Fatal("wrong-base retained FX admitted into new-base order risk input")
	}
}
func TestHistoryIdentitySameSymbolDifferentContractCannotShareHistory(t *testing.T) {
	srv := &Server{now: func() time.Time { return mandateTestNow }}
	history := mandateTestSeries("u:SYNA", 0.30, 2, 0)
	history.Contract = rpc.ContractParams{ConID: 9001, Symbol: "SYNA", SecType: "STK", Currency: "USD"}
	if e := srv.saveMandateSeries(t.Context(), history); e != nil {
		t.Fatal(e)
	}
	draft := mandateBuy(10)
	draft.Contract.ConID = 9002
	in := &mandate.Input{BaseCurrency: "USD", Series: map[string]mandate.Series{}, FX: map[string]mandate.Series{}, FXNow: map[string]float64{}}
	book, missing := srv.mandateOrderBook(draft, in, mandateTestNow)
	t.Logf("reviewed ConID=%d retained ConID=%d admitted closes=%d missing=%v", draft.Contract.ConID, history.Contract.ConID, len(book.Series["SYNA"].Closes), missing)
	if len(missing) == 0 || len(book.Series["SYNA"].Closes) > 0 {
		t.Fatal("wrong-contract historical returns admitted into order risk input")
	}
}
func TestHistoryIdentityMandateGateAcceptsWrongContractHistory(t *testing.T) {
	srv, _ := mandateTestServer(t, testMandateConstitution())
	seedMandateHistory(t, srv, 0)
	srv.mandatePositionsForTest = func(context.Context) (*rpc.PositionsResult, error) { return mandateTestPositions(100), nil }
	res := srv.evaluateMandate(t.Context(), false)
	if res.Verdict.Status != mandate.StatusWithin {
		t.Fatalf("fixture not within: %+v", res.Verdict)
	}
	draft := rpc.OrderDraft{Action: rpc.OrderActionBuy, Quantity: 10, LimitPrice: 100, Contract: rpc.ContractParams{ConID: 9100, Symbol: "SYNC", SecType: "STK", Currency: "EUR"}}
	before := srv.mandateBudgetGate(draft, mandateOpen)
	if before == nil {
		t.Fatal("fixture must initially lack exact history")
	}
	wrong := mandateTestSeries("u:SYNC", 0.25, 5, 0)
	wrong.Contract = rpc.ContractParams{ConID: 9101, Symbol: "SYNC", SecType: "STK", Currency: "USD"}
	if e := srv.saveMandateSeries(t.Context(), wrong); e != nil {
		t.Fatal(e)
	}
	after := srv.mandateBudgetGate(draft, mandateOpen)
	t.Logf("draft contract=9100/EUR, supplied history=9101/USD; before=%+v after=%+v", before, after)
	if after == nil {
		t.Fatal("mandate risk gate cleared by another contract's history")
	}
}

func TestHistoryIdentityRequestsDoNotSuppressDistinctContract(t *testing.T) {
	srv := &Server{now: func() time.Time { return mandateTestNow }}
	wanted := mandateHistoryNeed{Key: "u:SYNA", Contract: rpc.ContractParams{ConID: 9100, Symbol: "SYNA", SecType: "STK", Currency: "USD", Exchange: "SMART"}}
	srv.requestMandateHistory(wanted)
	needs := srv.mandateHistoryNeedsWithRequests(t.Context(), mandateTestPositions(100), "EUR", mandateTestNow)
	found := false
	for _, n := range needs {
		if bindMandateHistoryNeed(n).Key == bindMandateHistoryNeed(wanted).Key {
			found = true
		}
	}
	if !found {
		t.Fatal("held logical symbol permanently suppressed a distinct pending identity")
	}
}

func TestHistoryIdentityRejectsIncompleteLegacyMetadata(t *testing.T) {
	srv := &Server{now: func() time.Time { return mandateTestNow }}
	series := mandateTestSeries("u:SYNA", .3, 2, 0)
	series.Contract = rpc.ContractParams{}
	if err := srv.saveMandateSeries(t.Context(), series); err != nil {
		t.Fatal(err)
	}
	need := mandateHistoryNeed{Key: "u:SYNA", Contract: rpc.ContractParams{ConID: 9001, Symbol: "SYNA", SecType: "STK", Currency: "USD"}}
	if srv.mandateHistoryForNeed(t.Context(), need) != nil {
		t.Fatal("identity-free retained history admitted")
	}
}

func TestHistoryIdentityDuplicateSymbolsFailClosed(t *testing.T) {
	pos := mandateTestPositions(100)
	other := pos.Stocks[0]
	other.ConID += 100
	pos.Stocks = append(pos.Stocks, other)
	found := false
	for _, need := range mandateHistoryNeeds(pos, "EUR", nil) {
		if need.Contract.Symbol == other.Symbol {
			found = true
			if !need.Ambiguous {
				t.Fatal("distinct same-symbol positions share returns")
			}
		}
	}
	if !found {
		t.Fatal("fixture stock not in needs")
	}
}

func TestHistoryIdentityHeldStockBindsOptionUnderlying(t *testing.T) {
	srv := &Server{now: func() time.Time { return mandateTestNow }}
	need := mandateHistoryNeed{Key: "u:SYNA", Contract: rpc.ContractParams{ConID: 9001, Symbol: "SYNA", SecType: "STK", Currency: "USD", Exchange: "SMART", PrimaryExch: "NASDAQ"}}
	series := mandateTestSeries("u:SYNA", .3, 2, 0)
	series.Key = bindMandateHistoryNeed(need).Key
	series.Contract = need.Contract
	if err := srv.saveMandateSeries(t.Context(), series); err != nil {
		t.Fatal(err)
	}
	draft := mandateBuy(10)
	draft.Contract.SecType = "OPT"
	draft.Contract.ConID = 99999
	in := &mandate.Input{BaseCurrency: "USD", Legs: []mandate.Leg{{ID: "con:9001", Kind: mandate.KindStock, Underlying: "SYNA", Currency: "USD"}}, Series: map[string]mandate.Series{}}
	book, missing := srv.mandateOrderBook(draft, in, mandateTestNow)
	if len(missing) != 0 || len(book.Series["SYNA"].Closes) == 0 {
		t.Fatalf("exact held underlying not reusable: %+v", missing)
	}
	// The option's contract is never an underlying identity.
	bad := *series
	bad.Contract.ConID = 99999
	bad.Key = bindMandateHistoryNeed(mandateHistoryNeed{Key: "u:SYNA", Contract: bad.Contract}).Key
	srv.mandateHistory = mandateHistoryStore{}
	if err := srv.saveMandateSeries(t.Context(), &bad); err != nil {
		t.Fatal(err)
	}
	if _, missing := srv.mandateOrderBook(draft, in, mandateTestNow); len(missing) == 0 {
		t.Fatal("option ID accepted as stock history identity")
	}
}
