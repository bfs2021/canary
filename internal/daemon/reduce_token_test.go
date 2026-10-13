package daemon

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/osauer/canary/v2/internal/config"
	"github.com/osauer/canary/v2/internal/rpc"
)

const (
	reduceTestStockConID  = 101
	reduceTestOptionConID = 202
)

func reduceTestStockRow(qty float64) rpc.PositionView {
	return rpc.PositionView{Symbol: "SYN", SecType: "STK", ConID: reduceTestStockConID, Currency: "USD", Exchange: "SMART", Quantity: qty, Mark: 25}
}

func reduceTestOptionRow(qty float64) rpc.PositionView {
	return rpc.PositionView{Symbol: "SYN", SecType: "OPTION", ConID: reduceTestOptionConID, Currency: "USD", Exchange: "SMART",
		Quantity: qty, Mark: 2.5, Right: "C", Strike: 30, Expiry: "20261218", Multiplier: 100, Delta: new(0.5), Underlying: new(25.0)}
}

// newReducePreviewTestServer is a paper preview rig holding the given rows:
// the quote is fixed per security type and the position impact is read from
// the rows, so a reduce preview runs the production pricing and gates.
func newReducePreviewTestServer(t *testing.T, rows ...rpc.PositionView) (*Server, *rpc.PositionsResult) {
	t.Helper()
	srv := newOrderPreviewTestServer(t, config.Trading{Mode: config.TradingModePaper})
	var stocks, options []rpc.PositionView
	for _, row := range rows {
		if positionWireSecType(row.SecType) == "OPT" {
			options = append(options, row)
		} else {
			stocks = append(stocks, row)
		}
	}
	pos := reduceTestPortfolio(stocks, options)
	srv.reducePositionsForTest = func(context.Context) (*rpc.PositionsResult, error) { return pos, nil }
	srv.orderPreviewQuote = func(_ context.Context, c rpc.ContractParams, _ time.Duration) (rpc.OrderQuoteSnapshot, error) {
		bid, ask := 24.9, 25.1
		if c.SecType == "OPT" {
			bid, ask = 2.4, 2.6
		}
		mid := (bid + ask) / 2
		return rpc.OrderQuoteSnapshot{Symbol: c.Symbol, Bid: &bid, Ask: &ask, Midpoint: &mid, DataType: rpc.MarketDataLive, QuoteQuality: "firm"}, nil
	}
	srv.orderPreviewPositionImpact = func(_ context.Context, c rpc.ContractParams, action string, qty int) (rpc.OrderPositionImpact, error) {
		row, blockers := findReducePosition(pos, c.ConID, "")
		if len(blockers) > 0 {
			t.Fatalf("position impact for an unheld contract %d: %+v", c.ConID, blockers)
		}
		after := row.Quantity - float64(qty)
		if action == rpc.OrderActionBuy {
			after = row.Quantity + float64(qty)
		}
		return rpc.OrderPositionImpact{Before: row.Quantity, After: after, Effect: classifyPositionEffect(row.Quantity, after)}, nil
	}
	srv.orderPreviewWhatIf = func(context.Context, rpc.OrderDraft) (rpc.OrderWhatIfResult, error) {
		return rpc.OrderWhatIfResult{Status: rpc.OrderWhatIfStatusAccepted, Available: true}, nil
	}
	return srv, pos
}

func TestReduceAdaptivePriorityResolves(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		in, want string
		refused  bool
	}{
		{"", "Normal", false}, {"Normal", "Normal", false}, {"patient", "Patient", false}, {"URGENT", "Urgent", false},
		{"none", "", false}, {"off", "", false}, {"fast", "", true}, {"Adaptive", "", true},
	} {
		got, blockers := reduceAdaptivePriority(tc.in)
		if got != tc.want || (len(blockers) > 0) != tc.refused || (tc.refused && blockers[0].Code != "bad_request") {
			t.Errorf("reduceAdaptivePriority(%q) = %q %+v, want %q refused=%v", tc.in, got, blockers, tc.want, tc.refused)
		}
	}
}

// Adaptive rides only on the one shape the broker layer sends it for: a US
// stock or ETF limit routed SMART. An option, a non-USD line or a directed
// route sends a plain limit at the mid.
func TestReduceAdaptiveSupportedPerContract(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		contract rpc.ContractParams
		want     bool
	}{
		{"us stock smart", rpc.ContractParams{SecType: "STK", Exchange: "SMART", Currency: "USD"}, true},
		{"us etf smart", rpc.ContractParams{SecType: "ETF", Exchange: "SMART", Currency: "USD"}, true},
		{"us option", rpc.ContractParams{SecType: "OPT", Exchange: "SMART", Currency: "USD"}, false},
		{"german stock", rpc.ContractParams{SecType: "STK", Exchange: "SMART", PrimaryExch: "IBIS", Currency: "EUR", Market: "de"}, false},
		{"directed route", rpc.ContractParams{SecType: "STK", Exchange: "ARCA", Currency: "USD"}, false},
		{"bond", rpc.ContractParams{SecType: "BOND", Exchange: "SMART", Currency: "USD"}, false},
	} {
		if got := reduceAdaptiveSupported(tc.contract); got != tc.want {
			t.Errorf("%s: reduceAdaptiveSupported = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestBuildPreparedReduceCarriesAdaptiveOnlyWhereSupported(t *testing.T) {
	t.Parallel()
	stock := buildPreparedReduce(reduceTestStockRow(100), 50, 0, "Normal")
	if stock.params.AdaptivePriority != "Normal" || stock.algoNote != "" {
		t.Fatalf("stock prepared = priority %q note %q, want Adaptive Normal with no note", stock.params.AdaptivePriority, stock.algoNote)
	}
	plain := buildPreparedReduce(reduceTestStockRow(100), 50, 0, "")
	if plain.params.AdaptivePriority != "" || plain.algoNote != "" {
		t.Fatalf("plain prepared = priority %q note %q, want a plain limit with no note", plain.params.AdaptivePriority, plain.algoNote)
	}
	option := buildPreparedReduce(reduceTestOptionRow(5), 2, 0, "Normal")
	if option.params.AdaptivePriority != "" || option.algoNote != reduceAdaptiveNote {
		t.Fatalf("option prepared = priority %q note %q, want a plain limit that says Adaptive is not supported", option.params.AdaptivePriority, option.algoNote)
	}
	if option.params.Strategy != rpc.OrderStrategyPatientLimit || option.params.OrderType != rpc.OrderTypeLMT || option.params.TIF != rpc.OrderTIFDay {
		t.Fatalf("option prepared order = %+v, want a patient LMT DAY at the mid", option.params)
	}
}

func TestReducePriceBoundCheck(t *testing.T) {
	t.Parallel()
	quote := func(bid, ask float64) rpc.OrderQuoteSnapshot {
		mid := (bid + ask) / 2
		return rpc.OrderQuoteSnapshot{Bid: &bid, Ask: &ask, Midpoint: &mid}
	}
	reviewed := quote(99.9, 100.1)
	bound, blockers := reducePriceBoundCheck(reviewed, quote(100.3, 100.5), "STK")
	if len(blockers) != 0 || bound.ReferenceMid != 100 || bound.MaxMovePct != reducePriceBoundPct || math.Abs(bound.Low-99.5) > 1e-9 || math.Abs(bound.High-100.5) > 1e-9 {
		t.Fatalf("a 0.4%% move: bound %+v blockers %+v, want admitted within 99.5 to 100.5", bound, blockers)
	}
	if _, blockers := reducePriceBoundCheck(reviewed, quote(100.5, 100.7), "STK"); len(blockers) != 1 || blockers[0].Code != "price_moved" {
		t.Fatalf("a 0.6%% move: blockers %+v, want price_moved", blockers)
	}
	if _, blockers := reducePriceBoundCheck(reviewed, quote(99.3, 99.5), "STK"); len(blockers) != 1 || blockers[0].Code != "price_moved" {
		t.Fatalf("a 0.6%% move down: blockers %+v, want price_moved", blockers)
	}
	if _, blockers := reducePriceBoundCheck(reviewed, rpc.OrderQuoteSnapshot{}, "STK"); len(blockers) != 1 || blockers[0].Code != "quote_unavailable" {
		t.Fatalf("no current quote: blockers %+v, want quote_unavailable", blockers)
	}
	if _, blockers := reducePriceBoundCheck(rpc.OrderQuoteSnapshot{}, quote(99.9, 100.1), "STK"); len(blockers) != 1 || blockers[0].Code != "price_bound_unavailable" {
		t.Fatalf("no reviewed mid: blockers %+v, want price_bound_unavailable", blockers)
	}
	// Options get the owner's 2% band (2026-10-13 06:59 CEST): a 1.5% move is
	// admitted, a 2.5% move refused.
	option := quote(4.95, 5.05)
	bound, blockers = reducePriceBoundCheck(option, quote(5.03, 5.12), "OPT")
	if len(blockers) != 0 || bound.MaxMovePct != reduceOptionPriceBoundPct || math.Abs(bound.Low-4.9) > 1e-9 || math.Abs(bound.High-5.1) > 1e-9 {
		t.Fatalf("an option 1.5%% move: bound %+v blockers %+v, want admitted within 4.9 to 5.1", bound, blockers)
	}
	if _, blockers := reducePriceBoundCheck(option, quote(5.08, 5.17), "OPT"); len(blockers) != 1 || blockers[0].Code != "price_moved" {
		t.Fatalf("an option 2.5%% move: blockers %+v, want price_moved", blockers)
	}
	if _, blockers := reducePriceBoundCheck(option, quote(5.03, 5.12), "STK"); len(blockers) != 1 || blockers[0].Code != "price_moved" {
		t.Fatalf("a stock 1.5%% move: blockers %+v, want price_moved under the 0.5%% band", blockers)
	}
}

func TestReduceTokenPositionCheck(t *testing.T) {
	t.Parallel()
	draft := rpc.OrderDraft{Action: rpc.OrderActionSell, Quantity: 50, Contract: rpc.ContractParams{ConID: reduceTestStockConID, Symbol: "SYN", SecType: "STK"}}
	for _, tc := range []struct {
		name string
		held []rpc.PositionView
		want string
	}{
		{"still held", []rpc.PositionView{reduceTestStockRow(100)}, ""},
		{"grew", []rpc.PositionView{reduceTestStockRow(120)}, ""},
		{"exactly the reduction", []rpc.PositionView{reduceTestStockRow(50)}, ""},
		{"shrank below the reduction", []rpc.PositionView{reduceTestStockRow(40)}, "position_changed"},
		{"gone", nil, "position_changed"},
		{"flipped short", []rpc.PositionView{reduceTestStockRow(-10)}, "position_changed"},
	} {
		_, blockers := reduceTokenPositionCheck(reduceTestPortfolio(tc.held, nil), draft)
		got := ""
		if len(blockers) > 0 {
			got = blockers[0].Code
		}
		if got != tc.want {
			t.Errorf("%s: blockers %+v, want %q", tc.name, blockers, tc.want)
		}
	}
	if _, blockers := reduceTokenPositionCheck(nil, draft); len(blockers) != 1 || blockers[0].Code != "positions_unavailable" {
		t.Fatalf("no positions: blockers %+v, want positions_unavailable", blockers)
	}
}

// The preview hands out the signed token with the exact terms it binds: the
// terms are the token's own draft, Adaptive Normal on a limit at the mid,
// and the bound the submit will hold the market to.
func TestReducePreviewReturnsTokenAndExactTerms(t *testing.T) {
	t.Parallel()
	srv, _ := newReducePreviewTestServer(t, reduceTestStockRow(100))
	res, err := srv.tradeProposalReducePreview(t.Context(), rpc.TradeProposalReduceParams{Symbol: "SYN", Percent: 50})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Accepted || !res.SubmitEligible || res.PreviewToken == "" || res.Terms == nil || len(res.Blockers) != 0 {
		t.Fatalf("preview = %+v, want an accepted preview with a token and terms", res)
	}
	terms := res.Terms
	if terms.Action != rpc.OrderActionSell || terms.Quantity != 50 || terms.OrderType != rpc.OrderTypeLMT || terms.TIF != rpc.OrderTIFDay ||
		terms.LimitPrice != 25 || terms.Algo != "Adaptive" || terms.AdaptivePriority != "Normal" || terms.AlgoNote != "" ||
		terms.Contract.ConID != reduceTestStockConID || terms.PreviewTokenID != res.PreviewTokenID || !terms.ExpiresAt.Equal(res.PreviewTokenExpiresAt) {
		t.Fatalf("terms = %+v, want SELL 50 SYN LMT 25.00 DAY Adaptive Normal under token %s", terms, res.PreviewTokenID)
	}
	if terms.PriceBound.ReferenceMid != 25 || terms.PriceBound.MaxMovePct != reducePriceBoundPct || math.Abs(terms.PriceBound.Low-24.875) > 1e-9 || math.Abs(terms.PriceBound.High-25.125) > 1e-9 {
		t.Fatalf("price bound = %+v, want the reviewed mid 25 ±%.2f%%", terms.PriceBound, reducePriceBoundPct)
	}
	payload, err := srv.orderTokens.verify(res.PreviewToken)
	if err != nil {
		t.Fatalf("the preview's token does not verify: %v", err)
	}
	draft := payload.Draft
	if payload.TokenID != res.PreviewTokenID || draft.Source != reduceOrderSource || draft.Quantity != terms.Quantity || draft.LimitPrice != terms.LimitPrice ||
		draft.AdaptivePriority != terms.AdaptivePriority || draft.Action != terms.Action || draft.TIF != terms.TIF || draft.Contract.ConID != terms.Contract.ConID {
		t.Fatalf("token draft = %+v, want the terms the preview returned (%+v)", draft, terms)
	}
	if payload.ExpiresAt.Sub(payload.IssuedAt) != orderPreviewTokenTTL || !payload.ExpiresAt.Equal(terms.ExpiresAt) {
		t.Fatalf("token issued %s expires %s, want the %s preview TTL stated in the terms (%s)", payload.IssuedAt, payload.ExpiresAt, orderPreviewTokenTTL, terms.ExpiresAt)
	}
	if res.Preview == nil || res.Preview.Draft.AdaptivePriority != "Normal" {
		t.Fatalf("sanitized preview = %+v, want the Adaptive draft disclosed", res.Preview)
	}
}

func TestReducePreviewHonoursThePriorityAsked(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		asked, wantPriority, wantAlgo string
	}{
		{"Patient", "Patient", "Adaptive"}, {"urgent", "Urgent", "Adaptive"}, {"none", "", ""},
	} {
		srv, _ := newReducePreviewTestServer(t, reduceTestStockRow(100))
		res, err := srv.tradeProposalReducePreview(t.Context(), rpc.TradeProposalReduceParams{ConID: reduceTestStockConID, Percent: 25, AdaptivePriority: tc.asked})
		if err != nil {
			t.Fatal(err)
		}
		if !res.Accepted || res.Terms == nil || res.Terms.AdaptivePriority != tc.wantPriority || res.Terms.Algo != tc.wantAlgo || res.Terms.AlgoNote != "" || res.Terms.Quantity != 25 {
			t.Fatalf("asked %q: preview = %+v terms %+v, want priority %q algo %q and 25 shares", tc.asked, res, res.Terms, tc.wantPriority, tc.wantAlgo)
		}
	}
	srv, _ := newReducePreviewTestServer(t, reduceTestStockRow(100))
	res, err := srv.tradeProposalReducePreview(t.Context(), rpc.TradeProposalReduceParams{ConID: reduceTestStockConID, Percent: 25, AdaptivePriority: "fast"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Accepted || res.PreviewToken != "" || len(res.Blockers) != 1 || res.Blockers[0].Code != "bad_request" {
		t.Fatalf("an unknown priority minted a token: %+v", res)
	}
}

// An option reduction is a plain limit at the mid, and the terms say why.
func TestReducePreviewOptionSendsPlainLimitAndSaysWhy(t *testing.T) {
	t.Parallel()
	srv, _ := newReducePreviewTestServer(t, reduceTestOptionRow(5))
	res, err := srv.tradeProposalReducePreview(t.Context(), rpc.TradeProposalReduceParams{ConID: reduceTestOptionConID, Percent: 50})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Accepted || res.PreviewToken == "" || res.Terms == nil {
		t.Fatalf("option preview = %+v, want an accepted preview with a token", res)
	}
	terms := res.Terms
	if terms.Algo != "" || terms.AdaptivePriority != "" || terms.AlgoNote != reduceAdaptiveNote || terms.Quantity != 2 || terms.OrderType != rpc.OrderTypeLMT || terms.LimitPrice != 2.5 || terms.Contract.SecType != "OPT" {
		t.Fatalf("option terms = %+v, want a plain LMT 2.50 for 2 contracts noting %q", terms, reduceAdaptiveNote)
	}
	payload, err := srv.orderTokens.verify(res.PreviewToken)
	if err != nil || payload.Draft.AdaptivePriority != "" {
		t.Fatalf("option token = %+v %v, want a plain-limit draft", payload.Draft, err)
	}
}

// A portfolio preview hands out one token per eligible leg with its terms.
func TestReducePortfolioPreviewReturnsTokenPerLeg(t *testing.T) {
	t.Parallel()
	second := reduceTestStockRow(200)
	second.Symbol, second.ConID, second.Mark = "SYNB", 102, 10
	srv, _ := newReducePreviewTestServer(t, reduceTestStockRow(100), second)
	srv.orderPreviewQuote = func(_ context.Context, c rpc.ContractParams, _ time.Duration) (rpc.OrderQuoteSnapshot, error) {
		bid, ask := 24.9, 25.1
		if c.ConID == 102 {
			bid, ask = 9.95, 10.05
		}
		mid := (bid + ask) / 2
		return rpc.OrderQuoteSnapshot{Symbol: c.Symbol, Bid: &bid, Ask: &ask, Midpoint: &mid, DataType: rpc.MarketDataLive, QuoteQuality: "firm"}, nil
	}
	res, err := srv.reducePortfolioPreview(t.Context(), rpc.TradeProposalReducePortfolioParams{Percent: 50})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Accepted || len(res.Legs) != 2 || res.EligibleCount != 2 {
		t.Fatalf("portfolio preview = %+v, want two eligible legs", res)
	}
	for _, leg := range res.Legs {
		if leg.PreviewToken == "" || leg.Terms == nil || leg.Terms.Quantity != leg.ReduceQuantity || leg.Terms.AdaptivePriority != "Normal" || leg.Terms.PreviewTokenID != leg.PreviewTokenID {
			t.Fatalf("leg %s = %+v terms %+v, want its own token and terms", leg.Symbol, leg, leg.Terms)
		}
		if payload, err := srv.orderTokens.verify(leg.PreviewToken); err != nil || payload.Draft.Contract.ConID != leg.ConID || payload.Draft.Quantity != leg.ReduceQuantity {
			t.Fatalf("leg %s token = %+v %v, want its own draft", leg.Symbol, payload.Draft, err)
		}
	}
}

// In the standard build a tokened submit is refused by the write gate before
// the token is read, like every other broker write.
func TestReduceTokenSubmitFailsClosedWithoutTheTradingBuild(t *testing.T) {
	if orderWritesAvailable {
		t.Skip("trading build")
	}
	t.Parallel()
	srv, _ := newReducePreviewTestServer(t, reduceTestStockRow(100))
	res, err := srv.tradeProposalReduceSubmit(t.Context(), rpc.TradeProposalReduceParams{PreviewToken: "ibkrp4.synthetic.token", RequestRef: "req-1"})
	if err != nil || res.Accepted || len(res.Blockers) == 0 {
		t.Fatalf("submit = %+v %v, want a refusal from the write gate", res, err)
	}
}
