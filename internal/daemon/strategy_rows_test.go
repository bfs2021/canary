package daemon

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/osauer/canary/v2/internal/core"
	edgecore "github.com/osauer/canary/v2/internal/edge"
	"github.com/osauer/canary/v2/internal/marketcal"
	"github.com/osauer/canary/v2/internal/mandate"
	"github.com/osauer/canary/v2/internal/register"
	"github.com/osauer/canary/v2/internal/risk"
	"github.com/osauer/canary/v2/internal/rpc"
	ibkrlib "github.com/osauer/canary/v2/pkg/ibkr"
)

// Rows for any live register entry (Desk docs/strategy-engine.md, build 2;
// strategy-register.md section 2), witnessed on the synthetic trend fund
// written live in a sleeve of its own beside the live core: its month's
// decision is one strategy_rebalance row with the core's terms on its own
// line, keyed by its id and sleeve, through the same gates, and its line is
// reserved to its sleeve once it holds it alone.

const trendLiveSleeve = "trend"

// testLiveTrendConstitution is the core's constitution with the trend fund
// written live in its own sleeve, at a 4% risk allocation.
func testLiveTrendConstitution() *risk.Constitution {
	c := testTrendFundConstitution()
	e := c.Mandate.Strategies[trendFundID]
	e.Stage, e.Sleeve, e.RiskAllocationPct = risk.StageLive, trendLiveSleeve, new(4.0)
	return c
}

// liveTrendServer is the core test server at a clock with the live trend
// fund: IBKR's answer for both ISINs, the fund's London series under its
// own key, an order journal, the proposal engine, both lines resolved and
// the mandate measured on them.
func liveTrendServer(t *testing.T, c *risk.Constitution, pos *rpc.PositionsResult, now time.Time) (*Server, *proposalEngine) {
	t.Helper()
	srv := coreTestServer(t, c, pos, 0.25)
	srv.now = func() time.Time { return now }
	srv.coreListingsForTest = (&byISINListings{listings: []ibkrlib.Listing{coreTestListing(), trendFundListing()}}).lookup
	series := coreTestSeriesOn(t, trendFundListing(), marketcal.MarketUKLSE, coreTestDatesOn(t, marketcal.MarketUKLSE), 0.25)
	series.Key = strategySeriesKey(trendFundID, trendFundConID)
	if err := srv.saveMandateSeries(t.Context(), series); err != nil {
		t.Fatal(err)
	}
	srv.orderJournal = newTestOrderJournalStore(t, filepath.Join(t.TempDir(), "orders"))
	engine := &proposalEngine{server: srv, now: srv.now, queued: &queuedAuthStore{}, store: &proposalStore{}}
	srv.tradeProposals = engine
	measure(t, srv)
	return srv, engine
}

// measure resolves every entry's line and measures the mandate on it, as
// the background lane does.
func measure(t *testing.T, srv *Server) {
	t.Helper()
	srv.refreshStrategies(t.Context())
	if res := srv.evaluateMandate(t.Context(), false); res.Verdict == nil {
		t.Fatal("no mandate verdict")
	}
}

func liveTrendTarget(t *testing.T, srv *Server) *rpc.StrategyTargetResult {
	t.Helper()
	r, ok := srv.resolvedStrategy(srv.riskPolicies.snapshot().policy, trendFundID)
	if !ok {
		t.Fatal("no trend-fund entry")
	}
	return srv.evaluateStrategyTarget(t.Context(), srv.nowUTC(), r)
}

// strategyGeneration generates one proposal set and returns its rows by
// bucket and every other entry's status.
func strategyGeneration(t *testing.T, srv *Server, engine *proposalEngine, pos *rpc.PositionsResult) (coreRows, entryRows []rpc.TradeProposal, st []rpc.TradeProposalCoreStatus) {
	t.Helper()
	rows, _, _, _, _, _, st := engine.generateBookWithStrategies(t.Context(), defaultProtectionPolicy(), rpc.ProtectionPolicyStatus{}, nil, pos, rpc.TradeProposalSourceFingerprints{}, nil, srv.currentBrokerStateScope(), srv.now())
	for _, p := range rows {
		switch p.Bucket {
		case rpc.TradeProposalBucketCoreRebalance:
			coreRows = append(coreRows, p)
		case rpc.TradeProposalBucketStrategyRebalance:
			entryRows = append(entryRows, p)
		}
	}
	return coreRows, entryRows, st
}

// recordEntryFill journals one of the entry's own fills the way the write
// path leaves it: the proposal journal's submitted event of its row and the
// order journal's execution of that order.
func recordEntryFill(t *testing.T, srv *Server, ref, execID, side string, shares, price float64, at time.Time) {
	t.Helper()
	scope := srv.currentBrokerStateScope()
	st := srv.tradeProposals.store
	st.mu.Lock()
	st.events = append(st.events, proposalEvent{At: at, Type: "submitted", Key: "strategy_rebalance:synthetic", Bucket: rpc.TradeProposalBucketStrategyRebalance,
		AccountID: scope.Account, AccountMode: scope.Mode, OrderRef: ref, Strategy: trendFundID, Sleeve: trendLiveSleeve, ConID: trendFundConID})
	st.mu.Unlock()
	ev := originTestOrder(ref, 900+len(execID), at)
	ev.Type, ev.Status, ev.ConID, ev.Symbol, ev.Action = orderJournalEventStatusUpdated, "Filled", trendFundConID, "SYNF", side
	ev.ExecID, ev.ExecShares, ev.ExecSide, ev.LastFillPrice, ev.Filled = execID, shares, map[string]string{rpc.OrderActionBuy: "BOT", rpc.OrderActionSell: "SLD"}[side], price, shares
	if err := srv.orderJournal.Append(ev); err != nil {
		t.Fatal(err)
	}
}

// A live entry's decision day creates exactly one strategy_rebalance row
// beside the core's own row: the entry's buy of its line by contract id, the
// core's terms (a DAY limit at the live mid, the window, the due time) under
// the entry's own episode, keyed by its id and sleeve; it passes the typed
// exception within its sized quantity only, is classified a strategy
// addition for Desk, is never pre-authorised and never queued, its line
// leaves protection's automatic sales, and its decision, row view, status
// line and kill mark are recorded once. The core's row is unchanged.
func TestStrategyRowOnTheDecisionDayBesideTheCore(t *testing.T) {
	pos := mandateTestPositions(100)
	srv, engine := liveTrendServer(t, testLiveTrendConstitution(), pos, coreDecisionNow)
	res := liveTrendTarget(t, srv)
	if res.Status != rpc.CoreStatusReady || res.Action == nil || res.Action.Kind != core.ActionBuy || res.ReadOnly || res.Decision.Decision != "2026-09-30" {
		t.Fatalf("verdict %s %v action %+v read-only %v", res.Status, res.Reasons, res.Action, res.ReadOnly)
	}
	coreRows, entryRows, statuses := strategyGeneration(t, srv, engine, pos)
	if len(coreRows) != 1 || len(entryRows) != 1 {
		t.Fatalf("core rows %d, entry rows %d; want one each", len(coreRows), len(entryRows))
	}
	p := entryRows[0]
	if p.Action != rpc.OrderActionBuy || p.Quantity != res.Action.Quantity || p.MaxQuantity != p.Quantity || p.PositionEffect != rpc.OrderPositionEffectOpen || p.Key == coreRows[0].Key ||
		p.OrderType != rpc.OrderTypeLMT || p.TIF != rpc.OrderTIFDay || p.Contract.ConID != trendFundConID || p.Contract.Exchange != "SMART" || p.Contract.PrimaryExch != "LSEETF" ||
		p.Contract.Market != "uk" || p.Contract.SecType != "STK" || p.State != rpc.TradeProposalStateGenerated || len(p.Blockers) != 0 || p.Shadow || !strings.HasPrefix(p.Key, rpc.TradeProposalBucketStrategyRebalance+":") {
		t.Fatalf("row %+v", p)
	}
	c := p.Core
	if c == nil || c.Strategy != trendFundID || c.Sleeve != trendLiveSleeve || c.Rule != register.TimedHoldingRuleID || c.Stage != "live" || c.Session != "2026-10-01" ||
		c.Kind != core.ActionBuy || c.ConID != trendFundConID || c.Target != res.Sizing.Target || c.DecisionClose != res.Decision.Close {
		t.Fatalf("figures %+v", c)
	}
	if !strings.HasPrefix(c.Terms.Episode, "strategy:"+trendFundID+":") || !strings.HasSuffix(c.Terms.Episode, ":2026-09") || c.Terms.PrimaryExchange != "LSEETF" || len(c.Terms.Window) != core.WindowSessions || c.Terms.Window[0] != "2026-10-01" {
		t.Fatalf("terms %+v", c.Terms)
	}
	if !strings.Contains(p.Reason, "trend-fund strategy (sleeve trend), decision 2026-09-30") || !strings.Contains(strings.Join(p.Details, " "), "standing mandate") {
		t.Fatalf("reason %q details %v", p.Reason, p.Details)
	}
	if k := coreRows[0].Core; k == nil || k.Strategy != "" || k.Rule != "" || k.Sleeve != "core" || !strings.HasPrefix(k.Terms.Episode, "core:") {
		t.Fatalf("the core's row %+v", k)
	}
	if len(statuses) != 1 || statuses[0].Strategy != trendFundID || statuses[0].Sleeve != trendLiveSleeve || !statuses[0].Served || statuses[0].RowKey != p.Key || statuses[0].State != "live" {
		t.Fatalf("statuses %+v", statuses)
	}
	// The typed exception admits the row's own buy within its sized
	// quantity and nothing more; the Desk contract classifies it as a
	// strategy addition bound to its session's episode; it is due fifteen
	// minutes after the US cash open, never pre-authorised and never queued.
	x, ok := coreOpenExceptionFor(p)
	if !ok || x.Strategy != trendFundID || x.ConID != trendFundConID || x.MaxQuantity != p.Quantity {
		t.Fatalf("exception %+v %v", x, ok)
	}
	preview := &rpc.OrderPreviewResult{Draft: rpc.OrderDraft{Action: rpc.OrderActionBuy, Contract: p.Contract, Quantity: p.Quantity, OrderType: rpc.OrderTypeLMT, TIF: rpc.OrderTIFDay, LimitPrice: c.DecisionClose},
		Position: rpc.OrderPositionImpact{Effect: rpc.OrderPositionEffectOpen}}
	if b := x.previewBlockers(preview); len(b) != 0 {
		t.Fatalf("the row's own buy refused: %+v", b)
	}
	preview.Draft.Quantity++
	if b := x.previewBlockers(preview); len(b) != 1 || !strings.Contains(b[0].Message, "the trend-fund row's DAY limit buy") {
		t.Fatalf("one share more: %+v", b)
	}
	stray := p
	stray.Core = &rpc.TradeProposalCore{Kind: core.ActionBuy, ConID: trendFundConID, Currency: "EUR", Target: c.Target}
	if _, ok := coreOpenExceptionFor(stray); ok {
		t.Fatal("a strategy_rebalance buy that names no entry passed the exception")
	}
	if deskExecutionClass(p) != rpc.DeskClassAdd || deskExecutionStrategyEpisode(p) != c.Terms.Episode+":2026-10-01" || !strings.Contains(deskExecutionStrategyName(p), "strategy trend-fund (timed-holding-v1), sleeve trend") {
		t.Fatalf("desk class %q episode %q name %q", deskExecutionClass(p), deskExecutionStrategyEpisode(p), deskExecutionStrategyName(p))
	}
	if due, ok := coreRowDueAt(p, srv.now()); !ok || !due.Equal(time.Date(2026, 10, 1, 13, 45, 0, 0, time.UTC)) {
		t.Fatalf("due %s %v", due, ok)
	}
	if b := coreDueBlockers(p, time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)); len(b) != 1 || b[0].Code != coreBeforeDueCode || !strings.Contains(b[0].Message, "the trend-fund row is sent from") {
		t.Fatalf("before the due time %+v", b)
	}
	if validPreAuthorisedBucket(rpc.TradeProposalBucketStrategyRebalance) || automaticBucketFor(p) != "" || engine.automaticBucket(p) != "" {
		t.Fatal("a strategy row is pre-authorisable")
	}
	if !engine.coreLine(p) {
		t.Fatal("the live entry's line is inside protection's automatic sales")
	}
	// The decision is recorded once with the row's key, the row is served on
	// the entry's verdict and on `canary status`, and the kill rule's first
	// mark is kept from its own fills (none yet).
	for range 2 {
		srv.refreshStrategies(t.Context())
	}
	scope := frozenScopeFor(func() register.Resolved { r, _ := srv.resolvedStrategy(srv.riskPolicies.snapshot().policy, trendFundID); return r }(), srv.currentBrokerStateScope(), trendFundConID)
	if f := srv.coreFrozen(t.Context(), scope); f == nil || f.Row == nil || !f.Row.Served || f.Row.RowKey != p.Key || f.Row.Kind != core.ActionBuy || f.Row.Stage != "live" {
		t.Fatalf("recorded decision %+v", f)
	}
	engine.snapshot = rpc.TradeProposalSnapshot{Kind: "trade-proposals", Proposals: append(coreRows, entryRows...), StrategyRebalance: statuses,
		AccountID: srv.currentBrokerStateScope().Account, AccountMode: srv.currentBrokerStateScope().Mode}
	again := liveTrendTarget(t, srv)
	if again.Row == nil || again.Row.Key != p.Key || again.Row.Session != "2026-10-01" || again.Row.Terms.Episode != c.Terms.Episode {
		t.Fatalf("row view %+v", again.Row)
	}
	if d := again.LastDecision; d == nil || d.Decision != "2026-09-30" || !d.Outcome.Served || d.RowKey != p.Key {
		t.Fatalf("last decision %+v", again.LastDecision)
	}
	health := srv.strategyHealth()
	if len(health) != 2 || health[0].ID != risk.CoreStrategyID || health[1].ID != trendFundID || health[1].Row == nil || health[1].Row.Key != p.Key || health[1].Row.Session != "2026-10-01" || health[1].State != "live" {
		t.Fatalf("status lines %+v", health)
	}
	marks, err := srv.liveMarks(t.Context(), trendFundID)
	if err != nil || len(marks) != 1 || marks[0].Session != "2026-09-30" || marks[0].Units != 0 || marks[0].Cash != 0 || !(marks[0].SleeveBudget > 0) {
		t.Fatalf("kill marks %+v %v", marks, err)
	}
	if records, _ := srv.shadowRecords(t.Context(), trendFundID); len(records) != 0 {
		t.Fatalf("a live entry wrote shadow records: %+v", records)
	}
	if again.Kill == nil || again.Kill.Source != rpc.KillSourceOwnFills || !again.Kill.Measured || again.Kill.Breached {
		t.Fatalf("kill %+v", again.Kill)
	}
}

// Units of the line the account holds that the entry never bought stay the
// owner's (placement rule 1): the line is placed in the owner's sleeve and
// the live entry holds, naming them, with no row. Once the entry's own row
// has filled, the line is reserved to its sleeve: every unit counts as its
// holding, the gate places a hand order on it in the entry's sleeve, a
// non-stock order on it is refused, and its fills score in its sleeve.
func TestStrategyLineIsReservedOnlyOnceItsOwnFillsHoldIt(t *testing.T) {
	pos := withSYNF(mandateTestPositions(100), 500, 90)
	srv, engine := liveTrendServer(t, testLiveTrendConstitution(), pos, coreDecisionNow)
	own := *srv.mandateHistorySeries(t.Context(), strategySeriesKey(trendFundID, trendFundConID))
	own.Key = "u:SYNF"
	if err := srv.saveMandateSeries(t.Context(), &own); err != nil {
		t.Fatal(err)
	}
	measure(t, srv)
	_, input, _, _ := srv.mandateVerdicts.current()
	if sleeve := input.Allocations.SleeveOf(mandateConLegID(trendFundConID)); sleeve != mandate.SleeveOwner {
		t.Fatalf("the owner's units were placed in %s", sleeve)
	}
	res := liveTrendTarget(t, srv)
	if res.Status != rpc.CoreStatusHold || !slices.ContainsFunc(res.Reasons, func(r string) bool { return strings.Contains(r, "500 units of SYNF are held that the trend-fund strategy did not buy") }) || res.Holding.Shares != 0 {
		t.Fatalf("status %s reasons %v holding %+v", res.Status, res.Reasons, res.Holding)
	}
	if _, rows, _ := strategyGeneration(t, srv, engine, pos); len(rows) != 0 {
		t.Fatalf("rows over units the entry did not buy: %+v", rows)
	}

	// The entry's own row fills: the same 500 units, now bought by it.
	recordEntryFill(t, srv, "synthetic-trend-1", "exec-trend-1", rpc.OrderActionBuy, 500, 90, coreDecisionNow.Add(-time.Hour))
	if f := srv.strategyOwnFills(t.Context(), trendFundID, trendFundConID); f.Units != 500 || f.Cash != -45000 || f.Execs != 1 {
		t.Fatalf("own fills %+v", f)
	}
	measure(t, srv)
	_, input, _, _ = srv.mandateVerdicts.current()
	if sleeve := input.Allocations.SleeveOf(mandateConLegID(trendFundConID)); sleeve != trendLiveSleeve {
		t.Fatalf("the entry's line is placed in %s", sleeve)
	}
	if res := liveTrendTarget(t, srv); res.Holding.Shares != 500 || slices.ContainsFunc(res.Reasons, func(r string) bool { return strings.Contains(r, "did not buy") }) {
		t.Fatalf("holding %+v reasons %v", res.Holding, res.Reasons)
	}
	reserved := mandateReservedFromPlan(input.Allocations)
	hand := rpc.OrderDraft{Action: rpc.OrderActionBuy, Contract: rpc.ContractParams{ConID: trendFundConID, Symbol: "SYNF", SecType: "STK", Currency: "EUR"}, Quantity: 10, OrderType: rpc.OrderTypeLMT, LimitPrice: 90}
	if mandateReservedOrderSleeve(reserved, hand) != trendLiveSleeve || mandateReservedOrderSleeve(reserved, rpc.OrderDraft{Contract: rpc.ContractParams{ConID: 4242}}) != mandate.SleeveOwner {
		t.Fatal("orders are not placed by the reserved line")
	}
	combo := hand
	combo.Contract.SecType = "BAG"
	if b := srv.mandateBudgetGate(combo, rpc.OrderPositionImpact{Effect: rpc.OrderPositionEffectIncrease}); b == nil || !strings.Contains(b.Message, "the trend sleeve trades only its own fund line") {
		t.Fatalf("a combination order on the entry's line: %+v", b)
	}
	resolver := scorecardSleeveResolver([]orderJournalEvent{{ExecID: "exec-trend-1", OrderRef: "synthetic-trend-1"}}, srv.tradeProposals.store.eventsSince(time.Time{}))
	if got := resolver(edgecore.PositionChange{ExecutionIDs: []string{"exec-trend-1"}}); got.Name != trendLiveSleeve || got.StrategyRef != trendFundID || got.Origin != "proposal:strategy_rebalance" {
		t.Fatalf("scorecard sleeve %+v", got)
	}
}

// The kill rule of a live entry is measured on its own fills at each
// decision close: past the written bound the entry holds before the month
// can be frozen, naming its own fills, and serves no row.
func TestStrategyKillOnOwnFillsHoldsPastTheBound(t *testing.T) {
	c := testLiveTrendConstitution()
	c.Mandate.Strategies[trendFundID].KillDrawdownK = new(0.01)
	pos := mandateTestPositions(100)
	srv, engine := liveTrendServer(t, c, pos, coreDecisionNow)
	// Two kept marks: bought 100 at 100, then marked at 80, a fall of 2,000
	// from the peak, 0.033 K of a 9,200 sleeve budget.
	for _, m := range []liveMark{{Session: "2026-07-31", Close: 100, Units: 100, Cash: -10000, SleeveBudget: 9200}, {Session: "2026-08-31", Close: 80, Units: 100, Cash: -10000, SleeveBudget: 9200}} {
		m.Version, m.Strategy, m.RecordedAt = 1, trendFundID, coreDecisionNow
		appendTestLiveMark(t, srv, m)
	}
	res := liveTrendTarget(t, srv)
	if res.Kill == nil || !res.Kill.Breached || res.Kill.Source != rpc.KillSourceOwnFills || res.Kill.Decisions < 2 {
		t.Fatalf("kill %+v", res.Kill)
	}
	if res.Status != rpc.CoreStatusHold || !slices.ContainsFunc(res.Reasons, func(r string) bool { return strings.Contains(r, "kill rule: the drawdown on its own fills is") }) {
		t.Fatalf("status %s reasons %v", res.Status, res.Reasons)
	}
	if _, rows, _ := strategyGeneration(t, srv, engine, pos); len(rows) != 0 {
		t.Fatalf("rows past the kill bound: %+v", rows)
	}
}

func appendTestLiveMark(t *testing.T, srv *Server, m liveMark) {
	t.Helper()
	res := &rpc.StrategyTargetResult{Decision: &rpc.CoreDecision{Signal: core.Signal{Decision: m.Session, Close: m.Close}, FrozenAt: m.RecordedAt},
		Sizing: &rpc.CoreSizingView{Current: core.Size{SleeveBudget: m.SleeveBudget}}, Instrument: &rpc.CoreInstrument{ConID: trendFundConID}}
	r, _ := srv.resolvedStrategy(srv.riskPolicies.snapshot().policy, trendFundID)
	// The writer reads the entry's own fills: journal them so the kept mark
	// carries the units and cash under test.
	srv.liveMarkFillsForTest = func() strategyFills { return strategyFills{Units: m.Units, Cash: m.Cash, Execs: 1} }
	defer func() { srv.liveMarkFillsForTest = nil }()
	srv.recordLiveMark(t.Context(), res, r, m.RecordedAt)
}
