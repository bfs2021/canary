package daemon

import (
	"context"
	"errors"
	"math"
	"math/rand/v2"
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
	// A leg the gate cannot price on its own is left to the standing verdict.
	unpriced := rpc.OrderDraft{Action: rpc.OrderActionBuy, Quantity: 200, Contract: rpc.ContractParams{Symbol: "SYNZ", SecType: "OPT", Currency: "USD", Right: "C", Strike: 100, Expiry: "20270115"}}
	if blocker := srv.mandateBudgetGate(unpriced, mandateOpen); blocker != nil {
		t.Fatalf("unpriced option: %+v", blocker)
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
	st.Observe(300000, mandateTestNow.Add(-3*24*time.Hour), c, testLiveObserveScope, true)
	// A new high counts once a reading at least five minutes later
	// confirms it, at the lower of the two.
	st.Observe(312000, mandateTestNow.Add(-2*24*time.Hour), c, testLiveObserveScope, true)
	st.Observe(310000, mandateTestNow.Add(-2*24*time.Hour+10*time.Minute), c, testLiveObserveScope, true)
	st.Observe(295000, mandateTestNow.Add(-time.Minute), c, testLiveObserveScope, true)
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
	st.Observe(245000, mandateTestNow, c, testLiveObserveScope, true)
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
	st.Observe(300000, at, c, testLiveObserveScope, true)
	mark := func() float64 {
		t.Helper()
		capital, _ := st.MandateCapitalForScope(c, brokerStateScope{}, mandateTestNow)
		return capital.HWMRiskBase
	}
	seeded := mark()
	st.Observe(600000, at.Add(time.Minute), c, testLiveObserveScope, true) // the glitch
	st.Observe(300000, at.Add(7*time.Minute), c, testLiveObserveScope, true)
	if got := mark(); got != seeded {
		t.Fatalf("a single spike moved the mark: %v → %v", seeded, got)
	}
	st.Observe(320000, at.Add(10*time.Minute), c, testLiveObserveScope, true)
	st.Observe(330000, at.Add(12*time.Minute), c, testLiveObserveScope, true)
	if got := mark(); got != seeded {
		t.Fatalf("a high confirmed too soon: %v", got)
	}
	st.Observe(325000, at.Add(16*time.Minute), c, testLiveObserveScope, true)
	if got := mark(); got != seeded+20000 {
		t.Fatalf("a confirmed high: mark %v, want %v (the lower of the readings)", got, seeded+20000)
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
