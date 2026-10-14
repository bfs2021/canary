package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/osauer/canary/v2/internal/mandate"
	"github.com/osauer/canary/v2/internal/risk"
	"github.com/osauer/canary/v2/internal/rpc"
)

// mandateTestNow is a Wednesday during the US session, so the last completed
// session is the Tuesday before it.
var mandateTestNow = time.Date(2026, 10, 14, 15, 0, 0, 0, time.UTC)

func testMandateConstitution() *risk.Constitution {
	c := testConstitutionV3()
	c.SchemaVersion = 2
	c.OrderLimits = testOrderLimitsTable(10000)
	c.Mandate = &risk.ConstitutionMandate{MaxLossPct: new(30.0), VolTargetPct: new(15.0), TaxReserveBase: new(70000.0),
		StartedAt: "2026-10-01", GapShockPct: new(25.0), GapRoomPct: new(33.3),
		Sleeves: &risk.ConstitutionMandateSleeves{OwnerVolPct: new(7.0), CoreVolPct: new(6.0), OverlayVolPct: new(4.0)}}
	return c
}

// mandateTestServer is a preview server whose constitution carries the
// mandate, whose capital store observed one fresh equity reading and whose
// positions and daily history the test supplies.
func mandateTestServer(t *testing.T, c *risk.Constitution) (*Server, *riskCapitalStore) {
	t.Helper()
	srv, st, _, _ := brakeTestServer(t)
	srv.now = func() time.Time { return mandateTestNow }
	st.now = srv.now
	srv.riskPolicies.active = c
	st.Observe(300000, mandateTestNow.Add(-2*time.Minute), c, testLiveObserveScope, true)
	return srv, st
}

// mandateTestDates walks weekdays back from the last completed US session.
func mandateTestDates(n int) []string {
	last, _, ok := mandateLastSessions(mandateTestNow)
	if !ok {
		panic("no completed session")
	}
	day, _ := time.Parse(time.DateOnly, last)
	out := make([]string, n)
	for i := n - 1; i >= 0; i-- {
		out[i] = day.Format(time.DateOnly)
		day = day.AddDate(0, 0, -1)
		for day.Weekday() == time.Saturday || day.Weekday() == time.Sunday {
			day = day.AddDate(0, 0, -1)
		}
	}
	return out
}

// mandateTestSeries is a seeded random walk with the given annualised
// volatility ending on the last completed session; lag drops that many
// newest dates.
func mandateTestSeries(key string, vol float64, seed uint64, lag int) *mandateHistorySeries {
	dates := mandateTestDates(300)
	dates = dates[:len(dates)-lag]
	r := rand.New(rand.NewPCG(seed, seed+1))
	s := &mandateHistorySeries{Version: mandateHistoryDocVer, Key: key, Dates: dates, FetchedAt: mandateTestNow.Add(-time.Hour), Source: "test"}
	price := 100.0
	for range dates {
		s.Closes = append(s.Closes, price)
		price *= math.Exp(vol / math.Sqrt(252) * r.NormFloat64())
	}
	return s
}

func seedMandateHistory(t *testing.T, srv *Server, lag int) {
	t.Helper()
	for _, s := range []*mandateHistorySeries{
		mandateTestSeries("u:SPY", 0.16, 1, 0), mandateTestSeries("u:SYNA", 0.30, 2, lag), mandateTestSeries("u:SYNB", 0.40, 3, 0), mandateTestSeries("fx:USD", 0.08, 4, 0),
	} {
		if err := srv.saveMandateSeries(t.Context(), s); err != nil {
			t.Fatal(err)
		}
	}
}

func mandateTestPositions(synaShares float64) *rpc.PositionsResult {
	iv := 0.35
	fx := 0.9
	return &rpc.PositionsResult{
		AsOf: mandateTestNow.Add(-time.Minute),
		Authority: &rpc.AccountDataAuthority{Availability: rpc.AccountDataAvailable, Freshness: rpc.AccountDataFreshnessCurrent,
			Scope: rpc.AccountDataScope{AccountID: "DU1234567", AccountMode: rpc.AccountModePaper}},
		Portfolio: &rpc.PositionsPortfolio{BaseCurrency: "EUR"},
		Stocks: []rpc.PositionView{
			{Symbol: "SYNA", SecType: rpc.SecTypeStock, ConID: 9001, Currency: "USD", Quantity: synaShares, Multiplier: 1, Mark: 100, FXRate: &fx},
			{Symbol: "SYNB", SecType: rpc.SecTypeStock, ConID: 9002, Currency: "EUR", Quantity: 100, Multiplier: 1, Mark: 80},
			{Symbol: "SYNT", SecType: "BILL", ConID: 9003, Currency: "EUR", Quantity: 10000, Multiplier: 1, Mark: 99},
		},
		Options: []rpc.PositionView{
			{Symbol: "SYNA", SecType: rpc.SecTypeOption, ConID: 9004, Currency: "USD", Quantity: -2, Multiplier: 100, Mark: 3, Expiry: "20270115", Strike: 110, Right: "C", IV: &iv, Underlying: new(100.0), FXRate: &fx},
		},
	}
}

func mandateBuy(shares int) rpc.OrderDraft {
	return rpc.OrderDraft{Action: rpc.OrderActionBuy, Quantity: shares, LimitPrice: 100, Contract: rpc.ContractParams{ConID: 9001, Symbol: "SYNA", SecType: "STK", Currency: "USD"}}
}

var mandateOpen = rpc.OrderPositionImpact{Before: 0, After: 100, Effect: rpc.OrderPositionEffectOpen}

// A book over budget refuses a risk-adding order at the preview, at
// admission and at the wire guard with mandate_budget; a reduction, a close
// and a hedge entry pass.
func TestMandateGateRefusesAdditionsOverBudget(t *testing.T) {
	srv, _ := mandateTestServer(t, testMandateConstitution())
	seedMandateHistory(t, srv, 0)
	srv.mandatePositionsForTest = func(context.Context) (*rpc.PositionsResult, error) { return mandateTestPositions(3000), nil }
	res := srv.evaluateMandate(t.Context(), false)
	if !res.Active || res.Verdict == nil || res.Verdict.Status != mandate.StatusReduce {
		t.Fatalf("verdict %+v", res.Verdict)
	}
	if res.Gate == nil || !res.Gate.RefusesAdditions || len(res.Verdict.NotModelled) != 1 || !strings.Contains(res.Verdict.NotModelled[0], "bill") {
		t.Fatalf("gate %+v not modelled %v", res.Gate, res.Verdict.NotModelled)
	}
	if len(res.Evidence.Series) != 4 || res.Evidence.PositionsAsOf.IsZero() || res.Evidence.EquityAsOf.IsZero() {
		t.Fatalf("evidence %+v", res.Evidence)
	}

	srv.orderPreviewQuote = fixedPreviewQuote(99, 101)
	srv.orderPreviewPositionImpact = fixedPreviewPosition(0, 100, rpc.OrderPositionEffectOpen)
	limit := 100.0
	_, err := srv.previewOrder(t.Context(), rpc.OrderPreviewParams{Action: "buy", Quantity: 10, LimitPrice: &limit,
		Contract: rpc.ContractParams{ConID: 9002, Symbol: "SYNB", SecType: "STK", Currency: "EUR"}})
	blockers := previewFailureBlockers(err)
	if err == nil || len(blockers) != 1 || blockers[0].Code != mandateBudgetCode || !strings.Contains(blockers[0].Message, "over the budget") || !strings.Contains(blockers[0].Message, "stay open") {
		t.Fatalf("buy over budget: err %v blockers %+v, want mandate_budget", err, blockers)
	}
	if err := srv.mandateBudgetGateError(mandateBuy(10), mandateOpen); err == nil || !errors.Is(err, ErrTradingDisabled) || !strings.Contains(err.Error(), "Mandate budget") {
		t.Fatalf("admission over budget: %v", err)
	}
	// The wire guard reads the same gate on a risk-bound binding:
	// TestMandateWireGuardRefusesAdditionsOverBudget (trading build).
	for _, effect := range []string{rpc.OrderPositionEffectReduce, rpc.OrderPositionEffectClose} {
		sell := rpc.OrderDraft{Action: rpc.OrderActionSell, Quantity: 50, Contract: rpc.ContractParams{ConID: 9001, Symbol: "SYNA", SecType: "STK", Currency: "USD"}}
		if blocker := srv.mandateBudgetGate(sell, rpc.OrderPositionImpact{Before: 3000, After: 2950, Effect: effect}); blocker != nil {
			t.Fatalf("%s over budget: %+v, want allowed", effect, blocker)
		}
	}
	hedge := rpc.OrderDraft{Action: rpc.OrderActionBuy, Quantity: 1, Contract: rpc.ContractParams{Symbol: srv.rulebookPolicy().HedgeSymbols[0], SecType: "OPT", Right: "P", Currency: "USD"}}
	if blocker := srv.mandateBudgetGate(hedge, mandateOpen); blocker != nil {
		t.Fatalf("hedge put over budget: %+v, want allowed", blocker)
	}
}

// Stale history reads unknown and refuses additions; fresh history with a
// book within budget releases the gate on the next verdict, with no reset.
func TestMandateGateUnknownOnStaleHistoryThenReleasesWithin(t *testing.T) {
	srv, _ := mandateTestServer(t, testMandateConstitution())
	seedMandateHistory(t, srv, 3)
	srv.mandatePositionsForTest = func(context.Context) (*rpc.PositionsResult, error) { return mandateTestPositions(100), nil }
	res := srv.evaluateMandate(t.Context(), false)
	if res.Verdict.Status != mandate.StatusUnknown || !strings.Contains(strings.Join(res.Verdict.Reasons, " "), "SYNA is stale") {
		t.Fatalf("stale history: %s %v", res.Verdict.Status, res.Verdict.Reasons)
	}
	if blocker := srv.mandateBudgetGate(mandateBuy(10), mandateOpen); blocker == nil || blocker.Code != mandateBudgetCode || !strings.Contains(blocker.Message, "cannot be measured") {
		t.Fatalf("buy on stale history: %+v", blocker)
	}
	if blocker := srv.mandateBudgetGate(mandateBuy(10), rpc.OrderPositionImpact{Before: 100, After: 50, Effect: rpc.OrderPositionEffectReduce}); blocker != nil {
		t.Fatalf("reduce on stale history: %+v", blocker)
	}
	seedMandateHistory(t, srv, 0)
	res = srv.evaluateMandate(t.Context(), false)
	if res.Verdict.Status != mandate.StatusWithin || res.Gate.RefusesAdditions {
		t.Fatalf("fresh history: %s %v gate %+v", res.Verdict.Status, res.Verdict.Reasons, res.Gate)
	}
	if blocker := srv.mandateBudgetGate(mandateBuy(10), mandateOpen); blocker != nil {
		t.Fatalf("buy within budget: %+v", blocker)
	}
	sum := 0.0
	for _, l := range res.Verdict.Legs {
		sum += l.ContributionPct
	}
	if math.Abs(sum-100) > 1e-6 || len(res.Verdict.Legs) != 3 {
		t.Fatalf("contributions %v sum %g", res.Verdict.Legs, sum)
	}
	// An old verdict cannot be shown current.
	srv.now = func() time.Time { return mandateTestNow.Add(mandateVerdictMaxAge + time.Second) }
	if blocker := srv.mandateBudgetGate(mandateBuy(10), mandateOpen); blocker == nil || !strings.Contains(blocker.Message, "old") {
		t.Fatalf("buy on an old verdict: %+v", blocker)
	}
}

// Within budget, an order that would take the risk over the budget is
// refused on its own, with the figure it would reach.
func TestMandateGateIncrementalCheckRefusesAnAdditionOverBudget(t *testing.T) {
	srv, _ := mandateTestServer(t, testMandateConstitution())
	seedMandateHistory(t, srv, 0)
	srv.mandatePositionsForTest = func(context.Context) (*rpc.PositionsResult, error) { return mandateTestPositions(100), nil }
	res := srv.evaluateMandate(t.Context(), false)
	if res.Verdict.Status != mandate.StatusWithin {
		t.Fatalf("book: %s %v", res.Verdict.Status, res.Verdict.Reasons)
	}
	if blocker := srv.mandateBudgetGate(mandateBuy(10), mandateOpen); blocker != nil {
		t.Fatalf("small buy: %+v", blocker)
	}
	blocker := srv.mandateBudgetGate(mandateBuy(5000), mandateOpen)
	if blocker == nil || blocker.Code != mandateBudgetCode || !strings.Contains(blocker.Message, "with this order the risk would be") {
		t.Fatalf("large buy: %+v", blocker)
	}
	// A new option priced from its limit on the held underlying's spot.
	call := rpc.OrderDraft{Action: rpc.OrderActionBuy, Quantity: 200, LimitPrice: 8, Contract: rpc.ContractParams{Symbol: "SYNA", SecType: "OPT", Currency: "USD", Right: "C", Strike: 100, Expiry: "20270115", Multiplier: 100}}
	if blocker := srv.mandateBudgetGate(call, mandateOpen); blocker == nil || !strings.Contains(blocker.Message, "with this order") {
		t.Fatalf("large call buy: %+v", blocker)
	}
	// An option whose volatility its limit cannot give, on a held
	// underlying, is left to the standing verdict.
	unpriced := rpc.OrderDraft{Action: rpc.OrderActionBuy, Quantity: 200, Contract: rpc.ContractParams{Symbol: "SYNA", SecType: "OPT", Currency: "USD", Right: "C", Strike: 100, Expiry: "20270115"}}
	if blocker := srv.mandateBudgetGate(unpriced, mandateOpen); blocker != nil {
		t.Fatalf("unpriced option: %+v", blocker)
	}
	// On an underlying the book does not hold, with no history, it is
	// refused: never passed unmeasured.
	unpriced.Contract.Symbol = "SYNZ"
	if blocker := srv.mandateBudgetGate(unpriced, mandateOpen); blocker == nil || !strings.Contains(blocker.Message, "no daily history for SYNZ") {
		t.Fatalf("option on an unheld name without history: %+v", blocker)
	}
}

// A first buy of a name the book does not hold is never passed unmeasured:
// without fresh decision-grade history it is refused and the series is
// requested on the background lane; once it has arrived the order is
// measured, admitted within the budget and refused over it. A currency
// without its exchange-rate history is refused the same way. Held names are
// measured as before and request nothing.
func TestMandateGateMeasuresANameTheBookDoesNotHold(t *testing.T) {
	srv, _ := mandateTestServer(t, testMandateConstitution())
	seedMandateHistory(t, srv, 0)
	srv.mandatePositionsForTest = func(context.Context) (*rpc.PositionsResult, error) { return mandateTestPositions(100), nil }
	if res := srv.evaluateMandate(t.Context(), false); res.Verdict.Status != mandate.StatusWithin {
		t.Fatalf("book: %s %v", res.Verdict.Status, res.Verdict.Reasons)
	}
	buy := func(sym, ccy string, shares int) rpc.OrderDraft {
		return rpc.OrderDraft{Action: rpc.OrderActionBuy, Quantity: shares, LimitPrice: 100, Contract: rpc.ContractParams{ConID: 9100, Symbol: sym, SecType: "STK", Currency: ccy}}
	}
	requested := func() []string {
		var keys []string
		for _, need := range srv.mandateHistoryRequests(mandateTestNow) {
			keys = append(keys, need.Key)
		}
		return keys
	}
	if blocker := srv.mandateBudgetGate(mandateBuy(10), mandateOpen); blocker != nil || len(requested()) != 0 {
		t.Fatalf("a held name: %+v, requested %v", blocker, requested())
	}
	blocker := srv.mandateBudgetGate(buy("SYNC", "EUR", 10), mandateOpen)
	if blocker == nil || blocker.Code != mandateBudgetCode || !strings.Contains(blocker.Message, "no daily history for SYNC yet; the desk is fetching it") {
		t.Fatalf("an unheld name without history: %+v", blocker)
	}
	if got := requested(); !slices.Equal(got, []string{"u:SYNC"}) {
		t.Fatalf("requested %v", got)
	}
	needs := srv.mandateHistoryNeedsWithRequests(mandateTestPositions(100), "EUR", mandateTestNow)
	if !slices.ContainsFunc(needs, func(n mandateHistoryNeed) bool {
		return n.Key == "u:SYNC" && n.Contract.Symbol == "SYNC" && n.Contract.ConID == 9100
	}) {
		t.Fatalf("the background pass does not read the request: %+v", needs)
	}
	if err := srv.saveMandateSeries(t.Context(), mandateTestSeries("u:SYNC", 0.25, 5, 0)); err != nil {
		t.Fatal(err)
	}
	if blocker := srv.mandateBudgetGate(buy("SYNC", "EUR", 10), mandateOpen); blocker != nil {
		t.Fatalf("an unheld name, history arrived, within budget: %+v", blocker)
	}
	if blocker := srv.mandateBudgetGate(buy("SYNC", "EUR", 20000), mandateOpen); blocker == nil || !strings.Contains(blocker.Message, "with this order the risk would be") {
		t.Fatalf("an unheld name, history arrived, over budget: %+v", blocker)
	}
	// A currency the book holds nothing in: the exchange rate's history too.
	if err := srv.saveMandateSeries(t.Context(), mandateTestSeries("u:SYND", 0.25, 6, 0)); err != nil {
		t.Fatal(err)
	}
	blocker = srv.mandateBudgetGate(buy("SYND", "CHF", 10), mandateOpen)
	if blocker == nil || !strings.Contains(blocker.Message, "no daily history for the EUR/CHF exchange rate yet") || strings.Contains(blocker.Message, "SYND yet") {
		t.Fatalf("a currency without its rate's history: %+v", blocker)
	}
	if got := requested(); !slices.Contains(got, "fx:CHF") {
		t.Fatalf("requested %v", got)
	}
	if err := srv.saveMandateSeries(t.Context(), mandateTestSeries("fx:CHF", 0.06, 7, 0)); err != nil {
		t.Fatal(err)
	}
	if blocker := srv.mandateBudgetGate(buy("SYND", "CHF", 10), mandateOpen); blocker != nil {
		t.Fatalf("a currency, history arrived, within budget: %+v", blocker)
	}
	// A request lapses after a day.
	if got := srv.mandateHistoryRequests(mandateTestNow.Add(25 * time.Hour)); len(got) != 0 {
		t.Fatalf("requests outlived a day: %+v", got)
	}
}

// Without a [mandate] table nothing changes: the gate refuses nothing, the
// verdict reads inactive and the refresh loop reads no history.
func TestMandateAbsentChangesNothing(t *testing.T) {
	srv, st, c, now := brakeTestServer(t)
	st.Observe(260000, now.Add(-2*time.Minute), c, testLiveObserveScope, true)
	fetches := 0
	srv.mandateHistoryFetchForTest = func(context.Context, rpc.MarketHistoryParams, int, time.Time) (*rpc.MarketHistoryResult, error) {
		fetches++
		return nil, errors.New("must not be called")
	}
	srv.mandatePositionsForTest = func(context.Context) (*rpc.PositionsResult, error) { return mandateTestPositions(3000), nil }
	srv.refreshMandateHistoryOnce(t.Context())
	srv.refreshMandateVerdict(t.Context())
	if fetches != 0 {
		t.Fatalf("an inactive mandate read history %d times", fetches)
	}
	if blocker := srv.mandateBudgetGate(mandateBuy(10), mandateOpen); blocker != nil {
		t.Fatalf("gate without a mandate: %+v", blocker)
	}
	res, err := srv.handleMandateRisk(t.Context(), &rpc.Request{})
	if err != nil || res.Active || res.Verdict != nil || res.Gate.RefusesAdditions || !strings.Contains(res.Message, "no [mandate] table") {
		t.Fatalf("inactive result: %+v err %v", res, err)
	}
	srv.orderPreviewPositionImpact = fixedPreviewPosition(0, 100, rpc.OrderPositionEffectOpen)
	if _, err := srv.previewOrder(t.Context(), brakeStockBuy()); err != nil {
		t.Fatalf("buy without a mandate: %v", err)
	}
	if st.state.Mandate != nil {
		t.Fatal("an observation without a mandate wrote mandate state")
	}
}

// An incomplete table is active and unknown: the verdict names the keys and
// additions are refused until they are written.
func TestMandateIncompleteTableRefusesAdditions(t *testing.T) {
	c := testMandateConstitution()
	c.Mandate.GapRoomPct = nil
	srv, _ := mandateTestServer(t, c)
	seedMandateHistory(t, srv, 0)
	srv.mandatePositionsForTest = func(context.Context) (*rpc.PositionsResult, error) { return mandateTestPositions(100), nil }
	res := srv.evaluateMandate(t.Context(), false)
	if res.Verdict.Status != mandate.StatusUnknown || !strings.Contains(strings.Join(res.Verdict.Reasons, " "), "mandate.gap_room_pct") || len(res.Policy.Unapproved) != 1 {
		t.Fatalf("incomplete table: %s %v %+v", res.Verdict.Status, res.Verdict.Reasons, res.Policy)
	}
	if blocker := srv.mandateBudgetGate(mandateBuy(10), mandateOpen); blocker == nil {
		t.Fatal("incomplete table admitted an addition")
	}
}

// The high-water mark seeds at the first observation on or after the start,
// advances with gains, treats a withdrawal as a lower base and not a
// drawdown, and starts again when started_at changes.
func TestMandateCapitalStateHighWaterMarkAndFlows(t *testing.T) {
	st := newTestRiskCapitalStore(t)
	st.now = func() time.Time { return mandateTestNow }
	c := testMandateConstitution()
	reconcileNow(t, st)
	before := mandateTestNow.Add(-20 * 24 * time.Hour) // before the start: not observed by the mandate
	st.Observe(400000, before, c, testLiveObserveScope, true)
	if st.state.Mandate != nil {
		t.Fatal("an observation before the start seeded the mandate")
	}
	// Each move is the book's own P&L, as the broker reports it.
	observeWithPnL(st, c, 300000, 0, mandateTestNow.Add(-3*24*time.Hour))
	// A new high counts once a reading at least five minutes later
	// confirms it, at the lower of the two.
	observeWithPnL(st, c, 312000, 12000, mandateTestNow.Add(-2*24*time.Hour))
	observeWithPnL(st, c, 310000, 10000, mandateTestNow.Add(-2*24*time.Hour+10*time.Minute))
	observeWithPnL(st, c, 295000, -15000, mandateTestNow.Add(-time.Minute))
	capital, realised := st.MandateCapitalForScope(c, brokerStateScope{}, mandateTestNow)
	if !capital.Known || capital.HWMRiskBase != 240000 || capital.RiskBase != 225000 || capital.DrawdownBase != 15000 || math.Abs(capital.DrawdownPct-6.25) > 1e-9 {
		t.Fatalf("capital %+v", capital)
	}
	if realised.Samples != 3 || realised.Vol20Pct != nil {
		t.Fatalf("realised %+v", realised)
	}
	// A withdrawal of 50,000 lowers the budget base, not the drawdown.
	if _, err := st.ApplyCapitalEventForPolicy(rpc.CapitalEventParams{Type: "withdrawal", AmountBase: 50000}, rpc.OrderOriginHumanTTY, c); err != nil {
		t.Fatal(err)
	}
	observeWithPnL(st, c, 245000, -15000, mandateTestNow)
	capital, _ = st.MandateCapitalForScope(c, brokerStateScope{}, mandateTestNow)
	if !capital.Known || capital.FlowsSinceStart != -50000 || capital.BudgetBase != 190000 || capital.DrawdownBase != 15000 {
		t.Fatalf("after a withdrawal %+v", capital)
	}
	// A new start date starts the series again from the next observation.
	c.Mandate.StartedAt = "2026-10-14"
	st.now = func() time.Time { return mandateTestNow.Add(time.Minute) }
	st.Observe(245000, mandateTestNow.Add(time.Minute), c, testLiveObserveScope, true)
	capital, _ = st.MandateCapitalForScope(c, brokerStateScope{}, mandateTestNow.Add(time.Minute))
	if !capital.Known || capital.DrawdownBase != 0 || capital.FlowsSinceStart != 0 || capital.BudgetBase != 175000 {
		t.Fatalf("after a restart %+v", capital)
	}
	// Stale equity is unknown, never within.
	later := mandateTestNow.Add(time.Duration(*c.Capital.MaxEquityAgeMinutes+2) * time.Minute)
	if capital, _ = st.MandateCapitalForScope(c, brokerStateScope{}, later); capital.Known || !capital.EquityStale {
		t.Fatalf("stale equity %+v", capital)
	}
}

// One glitchy equity print never raises the high-water mark: a new high
// counts once a reading at least five minutes later confirms it, at the
// lower of the two, so a spike can never read as a drawdown from then on.
func TestMandateHighWaterMarkIgnoresASingleGlitch(t *testing.T) {
	st := newTestRiskCapitalStore(t)
	st.now = func() time.Time { return mandateTestNow }
	c := testMandateConstitution()
	reconcileNow(t, st)
	at := mandateTestNow.Add(-time.Hour)
	observeWithPnL(st, c, 300000, 0, at)
	mark := func() float64 {
		t.Helper()
		capital, _ := st.MandateCapitalForScope(c, brokerStateScope{}, mandateTestNow)
		return capital.HWMRiskBase
	}
	seeded := mark()
	observeWithPnL(st, c, 600000, 0, at.Add(time.Minute)) // the glitch
	observeWithPnL(st, c, 300000, 0, at.Add(7*time.Minute))
	if got := mark(); got != seeded {
		t.Fatalf("a single spike moved the mark: %v → %v", seeded, got)
	}
	observeWithPnL(st, c, 320000, 20000, at.Add(10*time.Minute))
	observeWithPnL(st, c, 330000, 30000, at.Add(12*time.Minute))
	if got := mark(); got != seeded {
		t.Fatalf("a high confirmed too soon: %v", got)
	}
	observeWithPnL(st, c, 325000, 25000, at.Add(16*time.Minute))
	if got := mark(); got != seeded+20000 {
		t.Fatalf("a confirmed high: mark %v, want %v (the lower of the readings)", got, seeded+20000)
	}
}

// observeWithPnL is one account read: equity with the broker's daily P&L
// of the same session.
func observeWithPnL(st *riskCapitalStore, c *risk.Constitution, equity, pnl float64, at time.Time) {
	st.ObserveWithPnL(equity, mandatePnLReading{DailyBase: &pnl, Session: mandateSessionDate(at)}, at, c, testLiveObserveScope, true)
}

// mandateFlowTestStore is a capital store seeded at 300,000 an hour before
// mandateTestNow, with the helpers the flow tests read it through.
func mandateFlowTestStore(t *testing.T) (*riskCapitalStore, *risk.Constitution, time.Time, func() mandate.Capital) {
	t.Helper()
	st := newTestRiskCapitalStore(t)
	st.now = func() time.Time { return mandateTestNow }
	c := testMandateConstitution()
	reconcileNow(t, st)
	at := mandateTestNow.Add(-time.Hour)
	observeWithPnL(st, c, 300000, 0, at)
	return st, c, at, func() mandate.Capital {
		t.Helper()
		got, _ := st.MandateCapitalForScope(c, brokerStateScope{}, mandateTestNow)
		return got
	}
}

// The part of an equity move that declared flows and the book's own P&L
// leave unexplained, more than 1% of equity (or 1,000), is a suspected cash
// flow: the room is held (the capital reads unknown, so additions stay
// refused and the reduction sizes nothing) and the high-water mark does not
// move. It is measured cumulatively, so a later P&L move never offsets it:
// it clears when a declared or statement flow accounts for it, when the
// reading that raised it is undone, or when a statement covering its day is
// reconciled. A move the P&L explains is no flow; without the broker's P&L
// the whole move counts.
func TestMandateUnexplainedEquityMoveHoldsTheRoom(t *testing.T) {
	suspected := func(t *testing.T, got mandate.Capital) bool {
		t.Helper()
		return !got.Known && len(got.Reasons) > 0 && strings.Contains(got.Reasons[0], "a cash flow is suspected")
	}
	t.Run("a market move the P&L explains is no flow", func(t *testing.T) {
		st, c, at, capital := mandateFlowTestStore(t)
		observeWithPnL(st, c, 285000, -15000, at.Add(10*time.Minute))
		if got := capital(); !got.Known {
			t.Fatalf("a P&L-explained move held the room: %v", got.Reasons)
		}
	})
	t.Run("an undeclared 3% withdrawal stays suspect through a +1% day", func(t *testing.T) {
		st, c, at, capital := mandateFlowTestStore(t)
		observeWithPnL(st, c, 291000, 0, at.Add(10*time.Minute))
		observeWithPnL(st, c, 294000, 3000, at.Add(20*time.Minute))
		observeWithPnL(st, c, 294100, 3100, at.Add(30*time.Minute))
		if got := capital(); !suspected(t, got) {
			t.Fatalf("a later P&L move offset the withdrawal: %+v", got)
		}
		// Declared, it is accounted for and the room returns; the drawdown
		// is the market's, never the withdrawal.
		if _, err := st.ApplyCapitalEventForPolicy(rpc.CapitalEventParams{Type: "withdrawal", AmountBase: 9000}, rpc.OrderOriginHumanTTY, c); err != nil {
			t.Fatal(err)
		}
		observeWithPnL(st, c, 294100, 3100, at.Add(35*time.Minute))
		if got := capital(); !got.Known || got.DrawdownBase > 1 {
			t.Fatalf("after the declaration: %+v", got)
		}
	})
	t.Run("an 8% withdrawal on a -4% day is flagged", func(t *testing.T) {
		st, c, at, capital := mandateFlowTestStore(t)
		observeWithPnL(st, c, 264000, -12000, at.Add(10*time.Minute))
		if got := capital(); !suspected(t, got) {
			t.Fatalf("a withdrawal on a falling day passed: %+v", got)
		}
	})
	t.Run("a 10% jump without P&L is flagged, a small move is not", func(t *testing.T) {
		st, c, at, capital := mandateFlowTestStore(t)
		st.ObserveWithPnL(301500, mandatePnLReading{}, at.Add(5*time.Minute), c, testLiveObserveScope, true)
		if got := capital(); !got.Known {
			t.Fatalf("a 0.5%% move without P&L held the room: %v", got.Reasons)
		}
		st.ObserveWithPnL(331650, mandatePnLReading{}, at.Add(10*time.Minute), c, testLiveObserveScope, true)
		if got := capital(); !suspected(t, got) {
			t.Fatalf("a 10%% jump without P&L passed: %+v", got)
		}
		// Undone by the next reading (a glitch), it clears.
		st.ObserveWithPnL(301600, mandatePnLReading{}, at.Add(12*time.Minute), c, testLiveObserveScope, true)
		if got := capital(); !got.Known {
			t.Fatalf("an undone glitch stayed suspect: %v", got.Reasons)
		}
	})
	t.Run("a deposit declared late never reads as drawdown", func(t *testing.T) {
		st, c, at, capital := mandateFlowTestStore(t)
		mark := capital().HWMRiskBase
		observeWithPnL(st, c, 330000, 0, at.Add(10*time.Minute))
		observeWithPnL(st, c, 330200, 200, at.Add(20*time.Minute))
		if got := capital(); !suspected(t, got) || got.HWMRiskBase != 0 && got.HWMRiskBase != mark {
			t.Fatalf("an undeclared deposit: %+v", got)
		}
		if _, err := st.ApplyCapitalEventForPolicy(rpc.CapitalEventParams{Type: "deposit", AmountBase: 30000}, rpc.OrderOriginHumanTTY, c); err != nil {
			t.Fatal(err)
		}
		observeWithPnL(st, c, 330300, 300, at.Add(30*time.Minute))
		if got := capital(); !got.Known || got.HWMRiskBase != mark || got.DrawdownBase > 1 {
			t.Fatalf("a deposit declared late read as drawdown: %+v", got)
		}
	})
	t.Run("a statement covering the day clears what nothing else explains", func(t *testing.T) {
		st, c, at, capital := mandateFlowTestStore(t)
		observeWithPnL(st, c, 280000, 0, at.Add(10*time.Minute))
		observeWithPnL(st, c, 280100, 100, at.Add(20*time.Minute))
		if got := capital(); !suspected(t, got) {
			t.Fatalf("not suspected: %+v", got)
		}
		st.mu.Lock()
		st.state.StatementCoverageTo = at
		st.mu.Unlock()
		observeWithPnL(st, c, 280200, 200, at.Add(30*time.Minute))
		if got := capital(); !got.Known {
			t.Fatalf("a statement covering the day did not clear it: %v", got.Reasons)
		}
	})
	t.Run("overnight the new session's own P&L is the yardstick", func(t *testing.T) {
		st, c, at, _ := mandateFlowTestStore(t)
		next := at.Add(24 * time.Hour)
		st.now = func() time.Time { return next.Add(time.Minute) }
		observeWithPnL(st, c, 280000, 100, next)
		if got, _ := st.MandateCapitalForScope(c, brokerStateScope{}, next.Add(time.Minute)); !suspected(t, got) {
			t.Fatalf("an overnight drop the new session's P&L does not explain passed: %+v", got)
		}
	})
}

// A suspected cash flow makes the verdict unknown: additions are refused and
// the forced reduction sizes nothing until it is explained.
func TestMandateSuspectedFlowMakesTheVerdictUnknown(t *testing.T) {
	srv, st := mandateTestServer(t, testMandateConstitution())
	seedMandateHistory(t, srv, 0)
	srv.mandatePositionsForTest = func(context.Context) (*rpc.PositionsResult, error) { return mandateTestPositions(1000), nil }
	if res := srv.evaluateMandate(t.Context(), false); res.Verdict.Status != mandate.StatusWithin {
		t.Fatalf("baseline verdict %s %v", res.Verdict.Status, res.Verdict.Reasons)
	}
	c := srv.riskPolicies.active
	session := mandateSessionDate(mandateTestNow)
	pnl := 100.0
	st.ObserveWithPnL(300000, mandatePnLReading{DailyBase: &pnl, Session: session}, mandateTestNow.Add(-90*time.Second), c, testLiveObserveScope, true)
	st.ObserveWithPnL(280000, mandatePnLReading{DailyBase: &pnl, Session: session}, mandateTestNow.Add(-time.Minute), c, testLiveObserveScope, true)
	res := srv.evaluateMandate(t.Context(), false)
	if res.Verdict.Status != mandate.StatusUnknown || !slices.ContainsFunc(res.Verdict.Reasons, func(r string) bool { return strings.Contains(r, "a cash flow is suspected") }) {
		t.Fatalf("verdict under a suspected flow: %s %v", res.Verdict.Status, res.Verdict.Reasons)
	}
	if blocker := srv.mandateBudgetGate(mandateBuy(10), mandateOpen); blocker == nil || blocker.Code != mandateBudgetCode {
		t.Fatalf("an addition passed under a suspected flow: %+v", blocker)
	}
	if _, err := json.Marshal(res); err != nil {
		t.Fatal(err)
	}
}

// Realised volatility reads from the daily samples once enough exist.
func TestMandateRealisedVolatility(t *testing.T) {
	daily := map[string]float64{}
	day := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	value := 300000.0
	for i := range 70 {
		if i%2 == 0 {
			value *= 1.01
		} else {
			value /= 1.01
		}
		daily[day.AddDate(0, 0, i).Format(time.DateOnly)] = value
	}
	r := mandateRealisedFrom(daily, 70000)
	if r.Samples != 70 || r.Vol20Pct == nil || r.Vol60Pct == nil || *r.Vol20Pct < 10 || *r.Vol60Pct < 10 {
		t.Fatalf("realised %+v", r)
	}
}

// The history store refreshes a due series once per broker session on the
// background lane, keeps the newest closes with their source time, and
// judges freshness against the last completed session.
func TestMandateHistoryRefreshOncePerSessionAndFreshness(t *testing.T) {
	srv, _ := mandateTestServer(t, testMandateConstitution())
	dates := mandateTestDates(mandateHistoryKeep + 10)
	fetches := 0
	srv.mandateHistoryFetchForTest = func(_ context.Context, p rpc.MarketHistoryParams, tail int, now time.Time) (*rpc.MarketHistoryResult, error) {
		fetches++
		if tail != -mandateHistoryFetchDays || p.Range != "5Y" {
			t.Fatalf("fetch asked for %d days of %s", tail, p.Range)
		}
		res := &rpc.MarketHistoryResult{Contract: p.Contract, Interval: "1 day", AsOf: now, Source: "IBKR historical bars · TRADES"}
		for i, d := range dates {
			at, _ := time.Parse(time.DateOnly, d)
			res.Points = append(res.Points, rpc.MarketHistoryPoint{At: at, Value: 100 + float64(i)})
		}
		return res, nil
	}
	need := mandateHistoryNeed{Key: "u:SYNA", Contract: rpc.ContractParams{Symbol: "SYNA", SecType: "STK", Currency: "USD"}}
	fresh, err := srv.fetchMandateSeries(t.Context(), need, mandateTestNow)
	if err != nil {
		t.Fatal(err)
	}
	if len(fresh.Closes) != mandateHistoryKeep || fresh.Dates[len(fresh.Dates)-1] != dates[len(dates)-1] || fresh.FetchedAt.IsZero() || fresh.Source == "" {
		t.Fatalf("fetched series %d closes through %s", len(fresh.Closes), fresh.Dates[len(fresh.Dates)-1])
	}
	if mandateSeriesStale(fresh, mandateTestNow) || mandateSeriesDue(fresh, mandateTestNow) {
		t.Fatal("a series through the last completed session read stale or due")
	}
	lagged := mandateTestSeries("u:SYNA", 0.3, 5, 1)
	if mandateSeriesStale(lagged, mandateTestNow) || !mandateSeriesDue(lagged, mandateTestNow) {
		t.Fatal("a series one session behind must be due and not yet stale")
	}
	if !mandateSeriesStale(mandateTestSeries("u:SYNA", 0.3, 5, 2), mandateTestNow) || !mandateSeriesStale(nil, mandateTestNow) {
		t.Fatal("a series two sessions behind, or none, must be stale")
	}
	// The needs of a book: every underlying, the index and the base pair.
	needs := mandateHistoryNeeds(mandateTestPositions(100), "EUR")
	var keys []string
	for _, n := range needs {
		keys = append(keys, n.Key)
	}
	if strings.Join(keys, " ") != "fx:USD u:SPY u:SYNA u:SYNB" {
		t.Fatalf("needs %v", keys)
	}
	for _, n := range needs {
		if n.Key == "fx:USD" && (n.Contract.SecType != "CASH" || n.Contract.Symbol != "EUR" || n.Contract.Currency != "USD" || n.Contract.Exchange != "IDEALPRO") {
			t.Fatalf("fx need %+v", n.Contract)
		}
	}
	// An FX series is inverted to base units per unit of the currency.
	pair := &mandateHistorySeries{Key: "fx:USD", Dates: []string{"2026-10-12", "2026-10-13"}, Closes: []float64{1.25, 1.0}, FetchedAt: mandateTestNow}
	if conv := mandateSeriesInput(pair, mandateTestNow); conv.Closes[0] != 0.8 || conv.Closes[1] != 1 {
		t.Fatalf("inverted pair %v", conv.Closes)
	}
	// Persistence round trip through the core store.
	if err := srv.saveMandateSeries(t.Context(), fresh); err != nil {
		t.Fatal(err)
	}
	srv.mandateHistory = mandateHistoryStore{}
	if loaded := srv.mandateHistorySeries(t.Context(), "u:SYNA"); loaded == nil || len(loaded.Closes) != mandateHistoryKeep || !loaded.FetchedAt.Equal(fresh.FetchedAt) {
		t.Fatalf("reloaded series %+v", loaded)
	}
}

// Within budget, an addition that would take one name's gap loss over its
// cap is refused on its own, though the volatility budget would admit it,
// with the gap and the cap it would breach; the same notional in a broad
// index fund passes at the owner's index gap (D4) and is refused without
// it. A smaller addition inside the cap passes (core-sleeve.md, review B1).
func TestMandateGateRefusesAnAdditionThatBreachesAGapCap(t *testing.T) {
	c := testMandateConstitution()
	srv, _ := mandateTestServer(t, c)
	seedMandateHistory(t, srv, 0)
	for _, s := range []*mandateHistorySeries{mandateTestSeries("u:SYNC", 0.25, 5, 0), mandateTestSeries("u:SXR8", 0.12, 9, 0)} {
		if err := srv.saveMandateSeries(t.Context(), s); err != nil {
			t.Fatal(err)
		}
	}
	srv.mandatePositionsForTest = func(context.Context) (*rpc.PositionsResult, error) { return mandateTestPositions(100), nil }
	res := srv.evaluateMandate(t.Context(), false)
	if res.Verdict.Status != mandate.StatusWithin {
		t.Fatalf("book: %s %v", res.Verdict.Status, res.Verdict.Reasons)
	}
	capBase := res.Verdict.Stresses.GapCapBase // 0.30 × 230,000 × 0.333 at full room
	buy := func(sym string, shares int) rpc.OrderDraft {
		return rpc.OrderDraft{Action: rpc.OrderActionBuy, Quantity: shares, LimitPrice: 100, Contract: rpc.ContractParams{Symbol: sym, SecType: "STK", Currency: "EUR"}}
	}
	// An unheld name is priced at its last decision-grade close.
	spot := func(key string) float64 {
		series := srv.mandateHistorySeries(t.Context(), key)
		return series.Closes[len(series.Closes)-1]
	}
	inside := int(capBase / (spot("u:SYNC") * 0.25))
	if blocker := srv.mandateBudgetGate(buy("SYNC", inside), mandateOpen); blocker != nil {
		t.Fatalf("a buy inside the gap cap: %+v", blocker)
	}
	blocker := srv.mandateBudgetGate(buy("SYNC", inside+20), mandateOpen)
	if blocker == nil || blocker.Code != mandateBudgetCode || !strings.Contains(blocker.Message, "a −25% gap in SYNC would lose") || !strings.Contains(blocker.Message, "over the gap cap") {
		t.Fatalf("a buy over the gap cap: %+v", blocker)
	}
	// The volatility alone would admit it.
	_, kept, _, _ := srv.mandateVerdicts.current()
	book, missing := srv.mandateOrderBook(buy("SYNC", inside+20), kept, mandateTestNow)
	leg, ok := mandateOrderLeg(buy("SYNC", inside+20), &book)
	if len(missing) != 0 || !ok {
		t.Fatalf("order book: %v %v", missing, ok)
	}
	if with := mandate.Evaluate(mandate.WithLeg(book, leg)); with.Risk == nil || with.Risk.SDAnnualBase > with.Capital.BudgetAnnualSD {
		t.Fatalf("fixture: the volatility budget already refuses %+v", with.Risk)
	}
	// A broad index fund worth six times the cap: its 25% gap loses one
	// and a half caps, refused; its 12% gap 0.72 of a cap, admitted. At
	// 8.6 times the cap even the 12% gap breaches.
	fund := buy("SXR8", int(6*capBase/spot("u:SXR8")))
	if blocker := srv.mandateBudgetGate(fund, mandateOpen); blocker == nil || !strings.Contains(blocker.Message, "a −25% gap in SXR8") {
		t.Fatalf("a broad index without index_gap_shock_pct: %+v", blocker)
	}
	c.Mandate.IndexGapShockPct = new(12.0)
	if res := srv.evaluateMandate(t.Context(), false); res.Verdict.Status != mandate.StatusWithin {
		t.Fatalf("book with the index gap: %s %v", res.Verdict.Status, res.Verdict.Reasons)
	}
	if blocker := srv.mandateBudgetGate(fund, mandateOpen); blocker != nil {
		t.Fatalf("a broad index at the 12%% index gap: %+v", blocker)
	}
	if blocker := srv.mandateBudgetGate(buy("SXR8", int(8.6*capBase/spot("u:SXR8"))), mandateOpen); blocker == nil || !strings.Contains(blocker.Message, "a −12% gap in SXR8") {
		t.Fatalf("a broad index over its cap at 12%%: %+v", blocker)
	}
}

// A breach the book already carries is never deepened by an addition, and
// a gap-cap check never refuses an order that leaves the breach as it was.
func TestMandateGapBreachAddedComparesWithTheStandingBook(t *testing.T) {
	before := &mandate.Verdict{Underlyings: []mandate.UnderlyingReport{{Underlying: "SYNA", GapLossBase: 30000, GapCapBase: 20000, GapBreach: true}, {Underlying: "SYNB", GapLossBase: 1000, GapCapBase: 20000}}}
	same := mandate.Verdict{Underlyings: []mandate.UnderlyingReport{{Underlying: "SYNA", GapLossBase: 30000, GapCapBase: 20000, GapBreach: true}, {Underlying: "SYNB", GapLossBase: 5000, GapCapBase: 20000}}}
	if u, ok := mandateGapBreachAdded(before, same); ok {
		t.Fatalf("an addition elsewhere refused for a standing breach: %+v", u)
	}
	deeper := mandate.Verdict{Underlyings: []mandate.UnderlyingReport{{Underlying: "SYNA", GapLossBase: 31000, GapCapBase: 20000, GapBreach: true}}}
	if u, ok := mandateGapBreachAdded(before, deeper); !ok || u.Underlying != "SYNA" {
		t.Fatal("a deeper breach passed")
	}
	created := mandate.Verdict{Underlyings: []mandate.UnderlyingReport{{Underlying: "SYNC", GapLossBase: 21000, GapCapBase: 20000, GapBreach: true}}}
	if u, ok := mandateGapBreachAdded(before, created); !ok || u.Underlying != "SYNC" {
		t.Fatal("a new breach passed")
	}
}

// A held IBCF line is measured on SPY's returns (review S5): the book reads
// no IBCF series, so none can make its verdict stale; an IBCF order the
// book does not hold is priced at its limit, and one without a limit is
// refused, never passed unmeasured.
func TestMandateMeasuresIBCFOnTheIndex(t *testing.T) {
	srv, _ := mandateTestServer(t, testMandateConstitution())
	seedMandateHistory(t, srv, 0)
	pos := mandateTestPositions(100)
	pos.Stocks = append(pos.Stocks, rpc.PositionView{Symbol: "IBCF", SecType: rpc.SecTypeStock, ConID: 9020, Currency: "EUR", PrimaryExch: "IBIS", Quantity: 200, Multiplier: 1, Mark: 50})
	for _, need := range mandateHistoryNeeds(pos, "EUR") {
		if need.Key == "u:IBCF" {
			t.Fatalf("the book reads an IBCF series: %+v", need)
		}
	}
	srv.mandatePositionsForTest = func(context.Context) (*rpc.PositionsResult, error) { return pos, nil }
	res := srv.evaluateMandate(t.Context(), false)
	if res.Verdict.Status != mandate.StatusWithin {
		t.Fatalf("book with IBCF: %s %v", res.Verdict.Status, res.Verdict.Reasons)
	}
	var found bool
	for _, u := range res.Verdict.Underlyings {
		if u.Underlying == "IBCF" {
			found = math.Abs(u.Beta-1) < 1e-12
		}
	}
	if !found {
		t.Fatalf("IBCF not measured at beta 1: %+v", res.Verdict.Underlyings)
	}
	srv.mandatePositionsForTest = func(context.Context) (*rpc.PositionsResult, error) { return mandateTestPositions(100), nil }
	if res := srv.evaluateMandate(t.Context(), false); res.Verdict.Status != mandate.StatusWithin {
		t.Fatalf("book: %s %v", res.Verdict.Status, res.Verdict.Reasons)
	}
	buy := rpc.OrderDraft{Action: rpc.OrderActionBuy, Quantity: 100, LimitPrice: 50, Contract: rpc.ContractParams{Symbol: "IBCF", SecType: "STK", Currency: "EUR", PrimaryExch: "IBIS"}}
	if blocker := srv.mandateBudgetGate(buy, mandateOpen); blocker != nil {
		t.Fatalf("an IBCF buy at a limit: %+v", blocker)
	}
	if got := srv.mandateHistoryRequests(mandateTestNow); len(got) != 0 {
		t.Fatalf("an IBCF buy requested a series: %+v", got)
	}
	buy.Quantity = 20000
	if blocker := srv.mandateBudgetGate(buy, mandateOpen); blocker == nil || !strings.Contains(blocker.Message, "with this order") {
		t.Fatalf("a large IBCF buy: %+v", blocker)
	}
	buy.Quantity, buy.LimitPrice = 100, 0
	if blocker := srv.mandateBudgetGate(buy, mandateOpen); blocker == nil || !strings.Contains(blocker.Message, "no price for IBCF") {
		t.Fatalf("an IBCF buy without a limit: %+v", blocker)
	}
}
