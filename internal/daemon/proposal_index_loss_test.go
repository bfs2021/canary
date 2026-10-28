package daemon

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/osauer/canary/v2/internal/rpc"
)

// indexLossServer is a daemon whose kept mandate evaluation measures pos
// under the current policy, so served proposals and plans read it.
func indexLossServer(t *testing.T, pos *rpc.PositionsResult) *Server {
	t.Helper()
	s := &Server{}
	s.riskPolicies = newRiskPolicyManager("", time.Minute, nil)
	s.riskPolicies.active = testMandateConstitution()
	fixture := mandateReductionFixture(t, pos, 0.5, 1000000, 0)
	s.mandateVerdicts.set(fixture.Result, fixture.Input, mandateTestNow, s.riskPolicies.snapshot().policy.FingerprintKey(), false)
	return s
}

// Canary's own proposals are priced for the index fall exactly as the
// reduce plan prices its legs (owner, 2026-10-28 06:41 CET): a sell of 40
// of 100 index shares reads the same before and after as the plan cutting those
// 40, a trailing stop reads unknown because it fills only when reached, a
// cash sweep row carries no figure, and the snapshot's figure fills every
// actionable row together.
func TestProposalIndexLossMatchesTheReducePlan(t *testing.T) {
	// The index line's beta is 1 by construction, so selling it must lessen
	// the fall's loss; the synthetic names' betas are whatever their series
	// give.
	pos := mandateReductionBook(mandateReductionStock("SPY", 9101, 100, 50), mandateReductionStock("SYNB", 9102, 200, 30))
	s := indexLossServer(t, pos)

	plan := &rpc.TradeProposalReducePortfolioResult{}
	s.reduceIndexLoss(t.Context(), plan, map[int]float64{9101: -40})
	if plan.IndexLossBeforeBase == nil || plan.IndexLossAfterBase == nil {
		t.Fatalf("precondition: the plan prices the cut: %+v", plan.IndexLoss)
	}
	both := &rpc.TradeProposalReducePortfolioResult{}
	s.reduceIndexLoss(t.Context(), both, map[int]float64{9101: -40, 9102: -50})

	sell := func(key, symbol string, conID, qty int) rpc.TradeProposal {
		return rpc.TradeProposal{Key: key, Revision: "r1", Bucket: rpc.TradeProposalBucketRiskReduction, Action: rpc.OrderActionSell, Quantity: qty,
			Symbol: symbol, SecType: "STK", OrderType: rpc.OrderTypeLMT, Contract: rpc.ContractParams{ConID: conID, Symbol: symbol, SecType: "STK", Currency: "EUR"}}
	}
	trail := sell("ts-SYNB", "SYNB", 9102, 200)
	trail.Bucket, trail.OrderType, trail.Trail = rpc.TradeProposalBucketTrailingStop, rpc.OrderTypeTRAIL, &rpc.OrderTrailSpec{OffsetType: "percent", TrailingPercent: new(8.0)}
	blocked := sell("rr-SYNB-blocked", "SYNB", 9102, 50)
	blocked.Blockers = []rpc.TradingBlocker{{Code: "synthetic_blocker"}}
	sweep := rpc.TradeProposal{Key: "cs-1", Revision: "r1", Bucket: rpc.TradeProposalBucketCashSweep, Action: rpc.OrderActionBuy, Quantity: 1, Symbol: "BILL"}
	snap := rpc.TradeProposalSnapshot{Revision: "snap-1", Proposals: []rpc.TradeProposal{sell("rr-SPY", "SPY", 9101, 40), trail, blocked, sweep}}
	e := &proposalEngine{server: s}
	e.decorateIndexLoss(&snap)

	near := func(a, b *float64) bool { return a != nil && b != nil && math.Abs(*a-*b) < 1e-6 }
	row := snap.Proposals[0].IndexLoss
	if row == nil || !near(row.IndexLossBeforeBase, plan.IndexLossBeforeBase) || !near(row.IndexLossAfterBase, plan.IndexLossAfterBase) || row.IndexShockPct != plan.IndexShockPct {
		t.Fatalf("proposal figure = %+v, plan figure = %+v; want the same", row, plan.IndexLoss)
	}
	if *row.IndexLossAfterBase >= *row.IndexLossBeforeBase {
		t.Fatalf("selling a long must lessen the fall's loss: %+v", row)
	}
	if got := snap.Proposals[1].IndexLoss; got == nil || got.IndexLossAfterBase != nil || !strings.Contains(got.IndexLossUnknown, "stop or trail") {
		t.Fatalf("trailing stop figure = %+v, want unknown and why", got)
	}
	if got := snap.Proposals[2].IndexLoss; got == nil || got.IndexLossAfterBase == nil {
		t.Fatalf("a blocked row is still priced on its own: %+v", got)
	}
	if snap.Proposals[3].IndexLoss != nil {
		t.Fatal("a cash sweep row carries no index-fall figure")
	}
	// Together: only the actionable sell; the blocked row and the stop stay out.
	if snap.IndexLoss == nil || !near(snap.IndexLoss.IndexLossAfterBase, plan.IndexLossAfterBase) || snap.BaseCurrency != "EUR" {
		t.Fatalf("snapshot figure = %+v %q, want the actionable rows filled together", snap.IndexLoss, snap.BaseCurrency)
	}
	snap.Proposals[2].Blockers = nil
	e.decorateIndexLoss(&snap)
	if !near(snap.IndexLoss.IndexLossAfterBase, both.IndexLossAfterBase) {
		t.Fatalf("snapshot figure after the blocker cleared = %+v, want both sells = %+v", snap.IndexLoss, both.IndexLoss)
	}

	// One JSON shape for the plan and the proposals.
	planJSON, _ := json.Marshal(plan.IndexLoss)
	rowJSON, _ := json.Marshal(row)
	var planKeys, rowKeys map[string]any
	_ = json.Unmarshal(planJSON, &planKeys)
	_ = json.Unmarshal(rowJSON, &rowKeys)
	for _, k := range []string{"index_shock_pct", "index_loss_before_base", "index_loss_after_base"} {
		if _, ok := planKeys[k]; !ok {
			t.Errorf("plan JSON lacks %s: %s", k, planJSON)
		}
		if _, ok := rowKeys[k]; !ok {
			t.Errorf("proposal JSON lacks %s: %s", k, rowJSON)
		}
	}
	var wire map[string]json.RawMessage
	raw, _ := json.Marshal(snap.Proposals[0])
	if err := json.Unmarshal(raw, &wire); err != nil || wire["index_loss"] == nil {
		t.Fatalf("proposal wire lacks index_loss: %s", raw)
	}
}

// Without a kept evaluation under the current policy every equity row and
// the snapshot say the figure is unknown and why; a read never evaluates.
func TestProposalIndexLossUnknownWithoutAnEvaluation(t *testing.T) {
	s := &Server{}
	snap := rpc.TradeProposalSnapshot{Proposals: []rpc.TradeProposal{{Key: "rr-1", Bucket: rpc.TradeProposalBucketRiskReduction, Action: rpc.OrderActionSell, Quantity: 1, Symbol: "SYNA"}}}
	(&proposalEngine{server: s}).decorateIndexLoss(&snap)
	if snap.IndexLoss == nil || snap.IndexLoss.IndexLossUnknown == "" || snap.Proposals[0].IndexLoss == nil || snap.Proposals[0].IndexLoss.IndexLossBeforeBase != nil {
		t.Fatalf("snapshot = %+v row = %+v, want unknown with a reason", snap.IndexLoss, snap.Proposals[0].IndexLoss)
	}
}

// A plan of named proposals prices exactly the named rows (owner,
// 2026-10-28 06:41 CET): the figure equals the snapshot's for the same
// rows, a row it cannot price leaves the whole figure unknown, an unknown
// key is a blocked leg, and nothing from it can be submitted.
func TestReducePlanPricesNamedProposals(t *testing.T) {
	pos := mandateReductionBook(mandateReductionStock("SPY", 9101, 100, 50), mandateReductionStock("SYNB", 9102, 200, 30))
	s := indexLossServer(t, pos)
	sell := func(key, symbol string, conID, qty int) rpc.TradeProposal {
		return rpc.TradeProposal{Key: key, Revision: "r1", State: "generated", Bucket: rpc.TradeProposalBucketRiskReduction, Action: rpc.OrderActionSell, Quantity: qty,
			Symbol: symbol, SecType: "STK", OrderType: rpc.OrderTypeLMT, Contract: rpc.ContractParams{ConID: conID, Symbol: symbol, SecType: "STK", Currency: "EUR"}}
	}
	trail := sell("ts-SYNB", "SYNB", 9102, 200)
	trail.Bucket, trail.OrderType = rpc.TradeProposalBucketTrailingStop, rpc.OrderTypeTRAIL
	scope := brokerStateScope{Account: "DU1234567", Mode: rpc.AccountModePaper}
	s.tradeProposals = &proposalEngine{server: s, scope: func() brokerStateScope { return scope },
		snapshot: rpc.TradeProposalSnapshot{Kind: rpc.TradeProposalSnapshotKind, Revision: "snap-1", AccountID: scope.Account, AccountMode: scope.Mode,
			Proposals: []rpc.TradeProposal{sell("rr-SPY", "SPY", 9101, 40), sell("rr-SYNB", "SYNB", 9102, 50), trail}}}

	plan, err := s.reducePortfolioPreview(t.Context(), rpc.TradeProposalReducePortfolioParams{PlanOnly: true, ProposalKeys: []string{"rr-SPY", "rr-SYNB", "rr-SPY"}})
	if err != nil {
		t.Fatal(err)
	}
	served := s.tradeProposals.snapshotForRead(false, false)
	if plan.Mode != rpc.ReduceModeProposals || !plan.PlanOnly || plan.LegCount != 2 || plan.BlockedCount != 0 || plan.BaseCurrency != "EUR" {
		t.Fatalf("plan = %+v, want two priced legs in EUR", plan)
	}
	cuts := &rpc.TradeProposalReducePortfolioResult{}
	s.reduceIndexLoss(t.Context(), cuts, map[int]float64{9101: -40, 9102: -50})
	near := func(a, b *float64) bool { return a != nil && b != nil && math.Abs(*a-*b) < 1e-6 }
	if !near(plan.IndexLossAfterBase, cuts.IndexLossAfterBase) || !near(plan.IndexLossBeforeBase, cuts.IndexLossBeforeBase) || !near(plan.IndexLossAfterBase, served.IndexLoss.IndexLossAfterBase) {
		t.Fatalf("named plan %+v, sweep plan %+v, snapshot %+v; want one figure", plan.IndexLoss, cuts.IndexLoss, served.IndexLoss)
	}
	if plan.Legs[0].ConID != 9101 || plan.Legs[0].Action != rpc.OrderActionSell || plan.Legs[0].ReduceQuantity != 40 {
		t.Fatalf("leg = %+v, want the row's own size", plan.Legs[0])
	}

	withStop, _ := s.reducePortfolioPreview(t.Context(), rpc.TradeProposalReducePortfolioParams{PlanOnly: true, ProposalKeys: []string{"rr-SPY", "ts-SYNB", "gone"}})
	if withStop.IndexLossAfterBase != nil || !strings.Contains(withStop.IndexLossUnknown, "2 of 3") || withStop.BlockedCount != 2 {
		t.Fatalf("plan with a stop and an unknown key = %+v, want the figure unknown and two blocked legs", withStop)
	}
	if code := withStop.Legs[2].Blockers[0].Code; code != "proposal_unknown" {
		t.Fatalf("unknown key blocker = %s", code)
	}
	refused, _ := s.reducePortfolioPreview(t.Context(), rpc.TradeProposalReducePortfolioParams{ProposalKeys: []string{"rr-SPY"}})
	if len(refused.Blockers) == 0 || refused.IndexLossAfterBase != nil {
		t.Fatalf("named proposals without plan_only = %+v, want refused", refused)
	}
}
