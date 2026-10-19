package daemon

import (
	"context"
	"errors"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/osauer/canary/v2/internal/core"
	"github.com/osauer/canary/v2/internal/mandate"
	"github.com/osauer/canary/v2/internal/rpc"
)

// The three defects the review of 2026-10-19 found in the strategy path,
// each with the test that failed before its fix: the kill bound measured
// against the full allocation while the stage invests a quarter of it; a
// sale "for your review" the standing mandate sent; a strategy buy refused
// on the whole-book verdict against the owner's per-sleeve rule
// (mandate-risk-budget.md, "The gate", 2026-10-18 15:31 CEST).

// The kill bound is measured against what the stage invests: K at the stage
// is the sleeve's allocation over the 15% floor times the scale, so a bound
// in units of a fully invested sleeve binds the sleeve as it is invested at
// scale 25 too. Against the full allocation (v3.25.1) the same marks read
// 0.033 K, a loss of 140% of the invested amount short of the bound. A mark
// kept before the scale was written reads the entry's current scale.
func TestStrategyKillIsMeasuredAgainstTheStagesCapital(t *testing.T) {
	c := testLiveTrendConstitution()
	c.Mandate.Strategies[trendFundID].KillDrawdownK = new(0.10)
	pos := mandateTestPositions(100)
	srv, engine := liveTrendServer(t, c, pos, coreDecisionNow)
	// Bought 100 at 100, marked at 80: a fall of 2,000 from the peak on a
	// 9,200 sleeve budget. The first mark is one v3.25.1 kept, without the
	// scale; the second the refresh keeps now, with it.
	appendRawLiveMark(t, srv, liveMark{Version: 1, Strategy: trendFundID, Session: "2026-07-31", Close: 100, Units: 100, Cash: -10000, SleeveBudget: 9200, RecordedAt: coreDecisionNow})
	appendTestLiveMark(t, srv, liveMark{Session: "2026-08-31", Close: 80, Units: 100, Cash: -10000, SleeveBudget: 9200, RecordedAt: coreDecisionNow})
	marks, err := srv.liveMarks(t.Context(), trendFundID)
	if err != nil || len(marks) != 2 || marks[0].ScalePct != 0 || marks[1].ScalePct != 25 {
		t.Fatalf("marks %+v %v, want the kept mark to carry the stage's scale", marks, err)
	}
	res := liveTrendTarget(t, srv)
	want := 2000 / (9200 * 0.25 / core.VolFloor)
	// Two kept marks and this reading, marked at the decision close.
	if res.Kill == nil || !res.Kill.Measured || res.Kill.Decisions != 3 || res.Kill.ScalePct != 25 || math.Abs(res.Kill.DrawdownK-want) > 1e-9 {
		t.Fatalf("kill %+v, want a drawdown of %.3f K of the stage's capital", res.Kill, want)
	}
	if !res.Kill.Breached || res.Status != rpc.CoreStatusHold || !slices.ContainsFunc(res.Reasons, func(r string) bool { return strings.Contains(r, "past the bound 0.1 K") }) {
		t.Fatalf("a 0.13 K fall past a 0.10 K bound: kill %+v status %s %v", res.Kill, res.Status, res.Reasons)
	}
	if _, rows, _ := strategyGeneration(t, srv, engine, pos); len(rows) != 0 {
		t.Fatalf("rows past the kill bound: %+v", rows)
	}
	// The shadow record's paper sleeve is measured the same way: a paper
	// buy of 100 at 1,000 marked at 900 on a 9,200 budget at scale 25.
	shadow := trendFundServer(t, testTrendFundConstitution(), pos)
	base := rpc.ShadowRecord{Version: 1, Strategy: trendFundID, Rule: c.Mandate.Strategies[trendFundID].Rule, SpecHash: trendFundHash, SpecCited: true, Stage: "shadow", RecordedAt: mandateTestNow.Add(-40 * 24 * time.Hour)}
	decision, fill, mark := base, base, base
	decision.Kind, decision.Session, decision.Decision = rpc.ShadowKindDecision, "2026-07-31", &rpc.ShadowDecision{TrendOn: true, TargetShares: 100, ScalePct: 25, Close: 1000, SleeveBudget: 9200}
	fill.Kind, fill.Session, fill.Fill = rpc.ShadowKindFill, "2026-08-03", &rpc.ShadowFill{Quantity: 100, Price: 1000, HoldingAfter: 100, Convention: rpc.ShadowFillConvention}
	mark.Kind, mark.Session, mark.Decision = rpc.ShadowKindDecision, "2026-08-28", &rpc.ShadowDecision{TrendOn: true, TargetShares: 100, ScalePct: 25, Close: 900, SleeveBudget: 9200}
	for _, r := range []rpc.ShadowRecord{decision, fill, mark} {
		if err := shadow.appendShadowRecord(t.Context(), r); err != nil {
			t.Fatal(err)
		}
	}
	shadow.refreshStrategies(t.Context())
	paper := trendFundTarget(t, shadow)
	if paper.Kill == nil || paper.Kill.Source != "" || paper.Kill.ScalePct != 25 || paper.Kill.DrawdownK < 10000/(9200*0.25/core.VolFloor)-1e-9 {
		t.Fatalf("shadow kill %+v, want the paper fall of 10,000 in units of the stage's capital", paper.Kill)
	}
}

// appendRawLiveMark keeps a mark as written, the way an older daemon left it.
func appendRawLiveMark(t *testing.T, srv *Server, m liveMark) {
	t.Helper()
	res := &rpc.StrategyTargetResult{Decision: &rpc.CoreDecision{Decision: m.Session, Close: m.Close, FrozenAt: m.RecordedAt},
		Sizing: &rpc.CoreSizingView{Current: core.Size{SleeveBudget: m.SleeveBudget}}, Instrument: &rpc.CoreInstrument{ConID: trendFundConID}}
	r, _ := srv.resolvedStrategy(srv.riskPolicies.snapshot().policy, trendFundID)
	r.ScalePct = m.ScalePct // the writer keeps the entry's scale; an older one kept none
	srv.liveMarkFillsForTest = func() strategyFills { return strategyFills{Units: m.Units, Cash: m.Cash, Execs: 1} }
	defer func() { srv.liveMarkFillsForTest = nil }()
	srv.recordLiveMark(t.Context(), res, r, m.RecordedAt)
}

// A live entry without a written kill bound holds, naming the key: its kill
// rule cannot fire, and a rule that cannot fire is no rule for a sleeve that
// trades. The core, live without one, holds the same way until the owner
// writes [mandate.core] kill_drawdown_k. Shadow, which sends nothing, keeps
// the note and runs. Before 2026-10-19 a live entry traded on the note.
func TestLiveEntryWithoutAKillBoundHolds(t *testing.T) {
	c := testLiveTrendConstitution()
	c.Mandate.Strategies[trendFundID].KillDrawdownK = nil
	pos := mandateTestPositions(100)
	srv, engine := liveTrendServer(t, c, pos, coreDecisionNow)
	res := liveTrendTarget(t, srv)
	want := "kill rule: kill_drawdown_k is unwritten in [mandate.strategies." + trendFundID + "], so the kill rule cannot fire"
	if res.Status != rpc.CoreStatusHold || !slices.ContainsFunc(res.Reasons, func(r string) bool { return strings.Contains(r, want) }) || res.Kill == nil || res.Kill.MaxDrawdownK != nil {
		t.Fatalf("status %s reasons %v kill %+v, want a hold naming %q", res.Status, res.Reasons, res.Kill, want)
	}
	if _, rows, _ := strategyGeneration(t, srv, engine, pos); len(rows) != 0 {
		t.Fatalf("rows without a kill bound: %+v", rows)
	}
	cc := testCoreConstitution()
	cc.Mandate.Core.KillDrawdownK = nil
	coreSrv := coreTestServer(t, cc, pos, 0.25)
	if res := coreEvaluate(t, coreSrv); res.Status != rpc.CoreStatusHold || !slices.ContainsFunc(res.Reasons, func(r string) bool { return strings.Contains(r, "kill_drawdown_k is unwritten in [mandate.core]") }) {
		t.Fatalf("the core without a bound: %s %v", res.Status, res.Reasons)
	}
	shadow := trendFundServer(t, testTrendFundConstitution(), pos)
	shadow.refreshStrategies(t.Context())
	if res := trendFundTarget(t, shadow); res.Status != rpc.CoreStatusReady || res.Kill == nil || res.Kill.MaxDrawdownK != nil || res.Kill.Note != killUnwrittenNote {
		t.Fatalf("shadow without a bound: %s %v kill %+v", res.Status, res.Reasons, res.Kill)
	}
}

// The owner's per-sleeve rule at the decision step as at the gate: a book
// over budget no longer holds the core's buy while the core is inside its
// own allocation and the buy creates or deepens no gap-cap breach. The row
// is served open while the forced reduction cuts the sleeve over its
// allocation, and the gate admits the same buy, so the two never disagree.
// An unknown verdict holds it. Until 2026-10-19 any verdict but within
// refused the buy ("the mandate verdict reads reduce"): the core's October
// 2026 row.
func TestCoreRowIsServedInsideItsAllocationWhileTheBookIsOverBudget(t *testing.T) {
	over := mandateTestPositions(3000)
	srv, engine := coreRowServer(t, testCoreConstitution(), over, 0.25, coreDecisionNow)
	verdict := srv.evaluateMandate(t.Context(), false).Verdict
	if verdict.Status != mandate.StatusReduce && verdict.Status != mandate.StatusOver {
		t.Fatalf("the fixture's book reads %s, want over budget", verdict.Status)
	}
	allocation, ok := verdict.Allocation(mandate.SleeveCore)
	if isOver, _ := mandateSleeveOver(allocation); !ok || isOver {
		t.Fatalf("the fixture's core is over its allocation: %+v", allocation)
	}
	rows, st := coreRows(t, srv, engine, over)
	if len(rows) != 1 || rows[0].Action != rpc.OrderActionBuy || rows[0].State != rpc.TradeProposalStateGenerated || len(rows[0].Blockers) != 0 || !st.Served || st.Kind != core.ActionBuy {
		t.Fatalf("over budget, the core inside its allocation: rows %+v status %+v, want the buy served open", rows, st)
	}
	// The gate reads the row's buy the same way.
	row := rows[0]
	buy := rpc.OrderDraft{Action: rpc.OrderActionBuy, Quantity: row.Quantity, LimitPrice: *row.LimitPrice, Contract: row.Contract}
	if b := srv.mandateBudgetGate(buy, rpc.OrderPositionImpact{Before: 0, After: float64(row.Quantity), Effect: rpc.OrderPositionEffectOpen}); b != nil {
		t.Fatalf("the gate refuses the buy the decision step served: %+v", b)
	}
	// The decision is recorded as served.
	srv.refreshCoreTarget(t.Context())
	if d := srv.evaluateCoreTarget(t.Context(), srv.now()).LastDecision; d == nil || d.Kind != core.ActionBuy || !d.Outcome.Served || d.RowKey != row.Key {
		t.Fatalf("recorded decision %+v", d)
	}
	// The verdict unknown: the core holds, and no row is served.
	srv.mandatePositionsForTest = func(context.Context) (*rpc.PositionsResult, error) { return nil, errors.New("positions unavailable") }
	if res := srv.evaluateMandate(t.Context(), false); res.Verdict == nil || res.Verdict.Status != mandate.StatusUnknown {
		t.Fatalf("verdict %+v, want unknown", res.Verdict)
	}
	rows, st = coreRows(t, srv, engine, over)
	if len(rows) != 0 || st == nil || st.Served || st.Kind != core.ActionHold || !strings.Contains(strings.Join(st.Reasons, " "), "unknown") {
		t.Fatalf("verdict unknown: rows %+v status %+v, want a hold", rows, st)
	}
}

// mandateVerdictHolds is the one reading of the standing verdict: within
// passes; over or reduce passes a sleeve inside its allocation and says the
// book is over budget; a sleeve over its allocation, by use or on its own,
// is held with the gate's sentence; at the maximum loss no sleeve adds; an
// unknown verdict, or none, holds.
func TestMandateVerdictHoldsJudgesTheOrdersSleeve(t *testing.T) {
	inside := mandate.AllocationReport{Sleeve: mandate.SleeveCore, AllocationBase: 1000, UseBase: 400, StandaloneBase: 600}
	byUse, alone := inside, inside
	byUse.UseBase, byUse.Over = 1200, true
	alone.StandaloneBase = 1500
	verdict := func(status string, room float64, a mandate.AllocationReport) *mandate.Verdict {
		return &mandate.Verdict{Status: status, BaseCurrency: "EUR", Capital: mandate.Capital{Known: true, BudgetAnnualSD: 15000, Room: room},
			Risk: &mandate.Risk{SDAnnualBase: 20000, RatioToBudget: 20000.0 / 15000}, Reasons: []string{"positions unavailable"},
			Allocations: []mandate.AllocationReport{{Sleeve: mandate.SleeveOwner, AllocationBase: 9000, UseBase: 19600, StandaloneBase: 19800, Over: true}, a}}
	}
	for _, tc := range []struct {
		name       string
		v          *mandate.Verdict
		why        []string
		overBudget bool
	}{
		{"within", verdict(mandate.StatusWithin, 0.7, inside), nil, false},
		{"over, the sleeve inside its allocation", verdict(mandate.StatusOver, 0.7, inside), nil, true},
		{"reduce, the sleeve inside its allocation", verdict(mandate.StatusReduce, 0.7, inside), nil, true},
		{"over, the sleeve over by use", verdict(mandate.StatusReduce, 0.7, byUse), []string{"is over the budget 15,000 EUR", "the core's sleeve carries 1,200 EUR a year, over its allocation 1,000 EUR"}, false},
		{"over, the sleeve over on its own", verdict(mandate.StatusReduce, 0.7, alone), []string{"the core's sleeve carries 1,500 EUR a year on its own, over its allocation 1,000 EUR"}, false},
		{"over, at the maximum loss", verdict(mandate.StatusReduce, 0, inside), []string{"at the maximum loss no sleeve adds"}, false},
		{"unknown", verdict(mandate.StatusUnknown, 0.7, inside), []string{"the mandate risk cannot be measured: positions unavailable"}, false},
		{"no verdict", nil, []string{"no verdict has been measured for the policy in force yet"}, false},
	} {
		why, overBudget := mandateVerdictHolds(tc.v, mandate.SleeveCore, "EUR")
		if (why == "") != (len(tc.why) == 0) || overBudget != tc.overBudget || slices.ContainsFunc(tc.why, func(w string) bool { return !strings.Contains(why, w) }) {
			t.Fatalf("%s: why %q over budget %v, want %q %v", tc.name, why, overBudget, tc.why, tc.overBudget)
		}
	}
}

// A sale served while the entry holds goes to the owner for review, and for
// your review means the owner's (owner, 2026-10-19): the row says so, the
// daemon marks it (core.owner_review), the wire guard refuses it under the
// standing mandate, and an open sale (trend off, nothing holding) is not
// marked. Until 2026-10-19 the words said review while the controller took
// the sale as a candidate and the daemon sent it.
func TestHeldEntrysSaleToZeroIsTheOwnersToConfirm(t *testing.T) {
	pos := withIBCF(mandateTestPositions(100), 10, 60)
	c := testCoreConstitution()
	c.Mandate.Core.KillDrawdownK = nil // the hold: the kill rule cannot fire
	srv, engine := coreRowServer(t, c, pos, -0.6, coreDecisionNow)
	rows, st := coreRows(t, srv, engine, pos)
	if len(rows) != 1 || rows[0].Action != rpc.OrderActionSell || rows[0].Core == nil || !rows[0].Core.OwnerReview || rows[0].Core.Kind != core.ActionSellAll || rows[0].Quantity != 10 || !st.Served {
		t.Fatalf("the held core's sale: rows %+v status %+v, want the sale to zero marked for the owner's review", rows, st)
	}
	if !strings.Contains(rows[0].Reason, "goes to you for review") || !slices.ContainsFunc(rows[0].Details, func(d string) bool { return strings.Contains(d, "the standing mandate never sends it") }) {
		t.Fatalf("the row's words: %q %q", rows[0].Reason, rows[0].Details)
	}
	if b := deskOwnerReviewBlocker(rows[0]); b == nil || b.Code != rpc.DeskBlockerOwnerReview || !strings.Contains(b.Message, "goes to you for review") || !strings.Contains(b.Message, "nothing was journaled") {
		t.Fatalf("the wire guard on the held sale: %+v", b)
	}
	openSrv, openEngine := coreRowServer(t, testCoreConstitution(), pos, -0.6, coreDecisionNow)
	open, _ := coreRows(t, openSrv, openEngine, pos)
	if len(open) != 1 || open[0].Action != rpc.OrderActionSell || open[0].Core.OwnerReview || deskOwnerReviewBlocker(open[0]) != nil || !strings.Contains(open[0].Details[0], "Desk's controller sends it") {
		t.Fatalf("an open sale: %+v", open)
	}
}

// Under the standing mandate the submit path refuses a row served for the
// owner's review before journaling (the session's episode stays free, the
// row stays the owner's), whatever its readiness; the same row without the
// mark reaches the gate. A synthetic entry's strategy_rebalance sale.
func TestDeskExecutionSubmitRefusesARowForTheOwnersReview(t *testing.T) {
	s, scope := deskExecutionTestServer(t)
	e := notFoundEngine(s)
	s.tradeProposals = e
	s.gatewayReadyForTrading = func() bool { return true }
	row := rpc.TradeProposal{Key: "strategy_rebalance:syn", Revision: "r1", Bucket: rpc.TradeProposalBucketStrategyRebalance, Action: rpc.OrderActionSell, OrderType: rpc.OrderTypeLMT, PositionEffect: rpc.OrderPositionEffectClose,
		Core: &rpc.TradeProposalCore{Session: "2026-10-01", Sleeve: "trend", Strategy: "s6-trend", Rule: "timed-holding-v1", Kind: core.ActionSellAll, OwnerReview: true, Terms: rpc.CoreRowTerms{Episode: "strategy:s6-trend:DU1234567:paper:2026-09"}}}
	e.snapshot = rpc.TradeProposalSnapshot{Kind: rpc.TradeProposalSnapshotKind, Revision: "r1", AccountID: scope.Account, AccountMode: scope.Mode, Proposals: []rpc.TradeProposal{row}}
	if ready := e.snapshotForRead(false, false).Proposals[0].Readiness; ready == nil || ready.Code != rpc.ReadinessReady {
		t.Fatalf("the served row reads %+v, want ready", ready)
	}
	full := installDeskMandate(t, s, scope, "full", true)
	item := rpc.DeskExecutionItem{RequestID: "req-review", EpisodeID: "strategy:s6-trend:DU1234567:paper:2026-09:2026-10-01", Intent: rpc.DeskExecutionIntent{Class: rpc.DeskClassReduce, Proposal: &rpc.DeskExecutionProposal{Key: row.Key, Revision: "r1"}}}
	got, err := deskExecutionSubmit(t, s, rpc.DeskExecutionSubmitParams{Automatic: &full, Items: []rpc.DeskExecutionItem{item}})
	if err != nil || got.Receipts[0].Outcome != rpc.DeskOutcomeRefused || got.Receipts[0].Blockers[0].Code != rpc.DeskBlockerOwnerReview || !strings.Contains(got.Receipts[0].Message, "strategy s6-trend (timed-holding-v1), sleeve trend holds") {
		t.Fatalf("for the owner's review: %+v %v", got, err)
	}
	if r := deskExecutionLookup(t, s, item.RequestID)[0]; r.Outcome != rpc.DeskOutcomeAbsent {
		t.Fatalf("an owner-review refusal was journaled: %+v", r)
	}
	// The same sale, not for review: it reaches the gate (here the row is gone).
	row.Core.OwnerReview = false
	e.snapshot.Proposals = []rpc.TradeProposal{row}
	got, err = deskExecutionSubmit(t, s, rpc.DeskExecutionSubmitParams{Automatic: &full, Items: []rpc.DeskExecutionItem{item}})
	if err != nil || got.Receipts[0].Outcome != rpc.DeskOutcomeRefused || got.Receipts[0].Blockers[0].Code != "proposal_not_found" {
		t.Fatalf("an open sale at the gate: %+v %v", got, err)
	}
}

// A row served for the owner's review never stands for another row: a
// forced reduction of the same line is neither covered by it nor blocked,
// so the mandate's de-risking does not wait on a human; an open sale of the
// core still merges with the reduction as before (refuter round 1,
// 2026-10-19: the larger review sale covered the reduction).
func TestOwnerReviewRowStaysOutOfTheSameContractMerge(t *testing.T) {
	review := sameContractRow(rpc.TradeProposalBucketCoreRebalance, 7, 10, rpc.OrderTypeLMT)
	review.PositionEffect = rpc.OrderPositionEffectClose
	review.Core = &rpc.TradeProposalCore{Kind: core.ActionSellAll, OwnerReview: true}
	reduction := sameContractRow(rpc.TradeProposalBucketMandateReduction, 7, 5, rpc.OrderTypeLMT)
	rows := []rpc.TradeProposal{review, reduction}
	mergeSameContractProposals(rows, nil)
	if rows[1].CoveredBy != "" || len(rows[1].Blockers) != 0 || rows[0].CoveredBy != "" || len(rows[0].Covers) != 0 || len(rows[0].Blockers) != 0 {
		t.Fatalf("the review sale joined the merge: %+v %+v", rows[0], rows[1])
	}
	open := review
	open.Core = &rpc.TradeProposalCore{Kind: core.ActionSellAll}
	rows = []rpc.TradeProposal{open, reduction}
	mergeSameContractProposals(rows, nil)
	if rows[1].CoveredBy != rows[0].Key || len(rows[0].Covers) != 1 {
		t.Fatalf("an open core sale no longer covers the reduction of its line: %+v %+v", rows[0], rows[1])
	}
}
