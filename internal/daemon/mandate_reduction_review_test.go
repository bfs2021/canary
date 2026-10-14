package daemon

import (
	"context"
	"encoding/json"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/osauer/canary/v2/internal/mandate"
	"github.com/osauer/canary/v2/internal/rpc"
)

// Regression tests for the safety review of the forced reduction
// (2026-10-14): coverage units, conditional working orders, the maximum
// loss, the high-water mark against a glitch, the merge under the latch and
// the veto clock within a breach episode.

func mandatePlanFor(st *rpc.TradeProposalMandateStatus, conID int) (rpc.TradeProposalMandatePlanOrder, bool) {
	for _, o := range st.Plan {
		if o.Contract.ConID == conID {
			return o, true
		}
	}
	return rpc.TradeProposalMandatePlanOrder{}, false
}

// Shares that cover short calls are never sold below the cover: the batch
// buys the calls back first, as its leading rows, and the share sale waits
// for the buyback to fill; the next verdict, on the book after the fill,
// sells only the shares the buyback freed. The plan keeps the cover at every
// step, so an assignment can never flip the line short.
func TestMandateReductionCoveredCallBuysBackBeforeTheShares(t *testing.T) {
	t.Parallel()
	for _, ratio := range []float64{1.3, 2, 4} {
		call := mandateReductionCall("SYNA", 9010, -30, 90, 100, 12, "20261120")
		pos := mandateReductionBook(mandateReductionStock("SYNA", 9001, 3000, 100), call)
		in := mandateReductionFixture(t, pos, ratio, 1e7, 2)
		rows, st := mandateReductionRun(t, in, pos)
		assertMandateRowsReduceOnly(t, rows, pos)
		stockPlan, ok := mandatePlanFor(st, 9001)
		callPlan, okCall := mandatePlanFor(st, 9010)
		if !ok || !okCall || stockPlan.Units <= 0 || callPlan.Units <= 0 {
			t.Fatalf("ratio %g: plan %+v", ratio, st.Plan)
		}
		if float64(3000-stockPlan.Units) < 100*float64(30-callPlan.Units) {
			t.Fatalf("ratio %g: the plan leaves %d shares under %d short calls", ratio, 3000-stockPlan.Units, 30-callPlan.Units)
		}
		if callPlan.Rank >= stockPlan.Rank || stockPlan.Quantity != 0 || !strings.Contains(stockPlan.Waits, "cover short options") {
			t.Fatalf("ratio %g: the share sale does not wait for the buyback: %+v / %+v", ratio, callPlan, stockPlan)
		}
		if _, sold := mandateRowFor(rows, 9001); sold {
			t.Fatalf("ratio %g: shares sold while every share covers a call", ratio)
		}
		buy, ok := mandateRowFor(rows, 9010)
		if !ok || buy.Action != rpc.OrderActionBuy || buy.Quantity != callPlan.Units || buy.Mandate.Rank != callPlan.Rank || !strings.Contains(buy.Reason, "bought back first") {
			t.Fatalf("ratio %g: buyback row %+v", ratio, buy)
		}
		// The buyback fills; the next verdict sells the freed shares, never
		// more.
		left := -30 + float64(callPlan.Units)
		after := mandateReductionBook(mandateReductionStock("SYNA", 9001, 3000, 100), mandateReductionCall("SYNA", 9010, left, 90, 100, 12, "20261120"))
		next := mandateReductionFixture(t, after, ratio, 1e7, 3)
		next.Input.Capital = in.Input.Capital
		next = mandateReductionVerdict(*next.Input, 3)
		next.Episode = st.Episode
		rows, _ = mandateReductionRun(t, next, after)
		if sale, ok := mandateRowFor(rows, 9001); ok && float64(sale.Quantity) > 3000+100*left+1e-9 {
			t.Fatalf("ratio %g: sells %d shares with %g short calls left", ratio, sale.Quantity, -left)
		}
	}
	// Shares beyond the cover are free to go at once.
	call := mandateReductionCall("SYNA", 9010, -10, 90, 100, 12, "20261120")
	pos := mandateReductionBook(mandateReductionStock("SYNA", 9001, 3000, 100), call)
	rows, st := mandateReductionRun(t, mandateReductionFixture(t, pos, 1.3, 1e7, 2), pos)
	if sale, ok := mandateRowFor(rows, 9001); !ok || sale.Quantity > 2000 {
		t.Fatalf("free shares: %+v plan %+v", sale, st.Plan)
	}
}

// A long put that protects its own stock, on a name no Rulebook lists as a
// hedge, is protection: the batch never sells it, and every cut it makes on
// the shares lowers the book's risk, so the shares it leaves unprotected
// never make the book riskier.
func TestMandateReductionProtectivePutIsNeverSold(t *testing.T) {
	t.Parallel()
	for _, ratio := range []float64{1.3, 2, 4} {
		put := mandateReductionCall("SYNA", 9011, 30, 95, 100, 3, "20261120")
		put.Right = "P"
		pos := mandateReductionBook(mandateReductionStock("SYNA", 9001, 3000, 100), put)
		in := mandateReductionFixture(t, pos, ratio, 1e7, 2)
		rows, st := mandateReductionRun(t, in, pos)
		assertMandateRowsReduceOnly(t, rows, pos)
		if _, sold := mandateRowFor(rows, 9011); sold || !slices.ContainsFunc(st.LeftOut, func(s string) bool { return strings.Contains(s, "P 95") && strings.Contains(s, "protection") }) {
			t.Fatalf("ratio %g: the protective put is a candidate: rows %+v left out %v", ratio, rows, st.LeftOut)
		}
		before := st.RiskAnnualBase
		cut := map[int]float64{}
		for _, o := range st.Plan {
			cut[o.Contract.ConID] = float64(o.Units)
			if risk := mandateRiskAfter(t, in, cut); risk > before+1e-6 {
				t.Fatalf("ratio %g: cut %+v makes the book riskier (%.0f > %.0f)", ratio, o, risk, before)
			} else {
				before = risk
			}
		}
	}
}

// A line a working stop or trail holds leaves the plan: netting would hold
// its row for as long as the stop works, so the next contributor is cut
// instead and the line is named. A working immediate order (a sale in
// flight, partly filled) keeps its line in the plan: the cut it asks for is
// on its way.
func TestMandateReductionWorkingOrdersAndThePlan(t *testing.T) {
	t.Parallel()
	pos := mandateReductionBook(mandateReductionStock("SYNA", 9001, 3000, 100), mandateReductionStock("SYNB", 9002, 500, 80))
	in := mandateReductionFixture(t, pos, 1.1, 1e7, 2)
	in.WorkingOrder = func(row rpc.PositionView, _ string) (bool, bool) { return row.ConID == 9001, row.ConID == 9001 }
	rows, st := mandateReductionRun(t, in, pos)
	if _, ok := mandatePlanFor(st, 9001); ok || len(rows) == 0 || rows[0].Contract.ConID != 9002 {
		t.Fatalf("a stop-held line stayed in the plan: rows %+v plan %+v", rows, st.Plan)
	}
	if !slices.ContainsFunc(st.LeftOut, func(s string) bool {
		return strings.HasPrefix(s, "SYNA") && strings.Contains(s, "working stop or trail")
	}) {
		t.Fatalf("the held line is not named: %v", st.LeftOut)
	}
	if st.TargetReached != (mandateRiskAfter(t, in, map[int]float64{9002: float64(rows[0].Mandate.Units)}) <= 0.95*in.Result.Verdict.Capital.BudgetAnnualSD+1e-6) {
		t.Fatalf("target reached %v disagrees with the plan without SYNA", st.TargetReached)
	}

	// Partial fill: 100 of a 200-share sale filled, the rest still working.
	// The next verdict measures the 2,900 shares; the sale in flight keeps
	// SYNA in the plan, so SYNB is not cut for the shares already going.
	filled := mandateReductionBook(mandateReductionStock("SYNA", 9001, 2900, 100), mandateReductionStock("SYNB", 9002, 500, 80))
	next := mandateReductionFixture(t, filled, 1.1, 1e7, 3)
	next.Input.Capital = in.Input.Capital
	next = mandateReductionVerdict(*next.Input, 3)
	next.WorkingOrder = func(row rpc.PositionView, _ string) (bool, bool) { return row.ConID == 9001, false }
	rows, st = mandateReductionRun(t, next, filled)
	if syna, ok := mandateRowFor(rows, 9001); !ok || !st.TargetReached || syna.Mandate.Rank != 1 {
		t.Fatalf("partial fill: rows %+v status %+v", rows, st)
	}
	if _, ok := mandateRowFor(rows, 9002); ok {
		t.Fatalf("SYNB cut while SYNA's sale is in flight and suffices: %+v", rows)
	}
}

// At the maximum loss the room is nil, so are the budget and every gap cap:
// the batch reduces the book to what it may not cut (a hedge-listed put
// stays), and a verdict back within withdraws it. The verdict encodes.
func TestMandateReductionAtTheMaximumLoss(t *testing.T) {
	t.Parallel()
	hedge := mandateReductionCall("SPY", 9020, 10, 400, 420, 5, "20261120")
	hedge.Right = "P"
	pos := mandateReductionBook(mandateReductionStock("SYNA", 9001, 3000, 100), mandateReductionStock("SYNB", 9002, 500, 80), hedge)
	fixture := mandateReductionFixture(t, pos, 1.1, 1e7, 1)
	book := *fixture.Input
	book.Capital.Room, book.Capital.BudgetAnnualSD = 0, 0
	in := mandateReductionVerdict(book, 1)
	v := in.Result.Verdict
	if v.Status != mandate.StatusReduce || !math.IsInf(v.Risk.RatioToBudget, 1) || v.Stresses == nil || len(v.Stresses.GapBreaches) == 0 {
		t.Fatalf("verdict at room 0: %s ratio %v breaches %+v", v.Status, v.Risk.RatioToBudget, v.Stresses)
	}
	raw, err := json.Marshal(in.Result)
	if err != nil || !strings.Contains(string(raw), `"ratio_to_budget":null`) {
		t.Fatalf("encode at room 0: %v", err)
	}
	var back rpc.MandateRiskResult
	if err := json.Unmarshal(raw, &back); err != nil || back.Verdict.Status != mandate.StatusReduce || !math.IsInf(back.Verdict.Risk.RatioToBudget, 1) {
		t.Fatalf("round trip at room 0: %+v %v", back.Verdict, err)
	}
	if !strings.Contains(mandateStatusSentence(v, "EUR"), "maximum") {
		t.Fatalf("sentence %q", mandateStatusSentence(v, "EUR"))
	}
	rows, st := mandateReductionRun(t, in, pos)
	for _, conID := range []int{9001, 9002} {
		if row, ok := mandateRowFor(rows, conID); !ok || row.PositionEffect != rpc.OrderPositionEffectClose {
			t.Fatalf("line %d not closed at the maximum loss: %+v (status %+v)", conID, row, st)
		}
	}
	if _, ok := mandateRowFor(rows, 9020); ok || !slices.ContainsFunc(st.LeftOut, func(s string) bool { return strings.HasPrefix(s, "SPY") }) {
		t.Fatalf("the hedge was cut: rows %+v left out %v", rows, st.LeftOut)
	}
	// A room that collapsed on one glitchy reading acts at once (every gap
	// cap is breached), and the next verdict, within, withdraws it all.
	within := mandateReductionFixture(t, pos, 0.9, 1e7, 0)
	within.Previous, within.Episode = rows, st.Episode
	if rows, st := mandateReductionRun(t, within, pos); len(rows) != 0 || st.State != rpc.MandateReductionWithin || st.Episode != "" {
		t.Fatalf("after the glitch: rows %d status %+v", len(rows), st)
	}
}

// Under a latched drawdown brake a pre-authorised stock trailing stop is
// placed at once, and a mandate sale waits its full window: the sale never
// covers the stop then, so protection is not delayed. Without the latch an
// immediate sale goes first, as the owner decided. Two pre-authorised
// reductions of one line merge into the larger.
func TestMandateReductionMergeWithAutomaticChannels(t *testing.T) {
	t.Parallel()
	row := mandateReductionStock("SYNA", 9001, 3000, 100)
	policy := defaultProtectionPolicy()
	build := func(bucket string, qty int) rpc.TradeProposal {
		p := baseProposal(policy, rpc.ProtectionPolicyStatus{}, rpc.TradeProposalSourceFingerprints{}, mandateTestNow, bucket, row, rpc.OrderActionSell, qty, rpc.OrderPositionEffectReduce, bucket)
		if bucket == rpc.TradeProposalBucketTrailingStop {
			p.OrderType, p.PositionEffect = rpc.OrderTypeTRAIL, rpc.OrderPositionEffectClose
		}
		if bucket == rpc.TradeProposalBucketMandateReduction {
			p.NeverSkipVeto = true
			p.Mandate = &rpc.TradeProposalMandate{Rank: 1, BatchRows: 1, Phase: rpc.MandatePhaseRisk, Units: qty}
		}
		return p
	}
	automatic := func(rpc.TradeProposal) sameContractChannel { return sameContractAutomatic }
	live := func(rows []rpc.TradeProposal) []string {
		var out []string
		for _, p := range rows {
			if len(p.Blockers) == 0 {
				out = append(out, p.Bucket)
			}
		}
		return out
	}
	for _, latched := range []bool{false, true} {
		rows := []rpc.TradeProposal{build(rpc.TradeProposalBucketTrailingStop, 3000), build(rpc.TradeProposalBucketMandateReduction, 600)}
		mergeSameContractProposalsLatched(rows, automatic, latched)
		got := live(rows)
		want := []string{rpc.TradeProposalBucketMandateReduction}
		if latched {
			want = []string{rpc.TradeProposalBucketTrailingStop, rpc.TradeProposalBucketMandateReduction}
		}
		if !slices.Equal(got, want) {
			t.Fatalf("latched %v: live %v, want %v", latched, got, want)
		}
	}
	rows := []rpc.TradeProposal{build(rpc.TradeProposalBucketRiskReduction, 400), build(rpc.TradeProposalBucketMandateReduction, 600)}
	mergeSameContractProposals(rows, automatic)
	if got := live(rows); !slices.Equal(got, []string{rpc.TradeProposalBucketMandateReduction}) || rows[1].Quantity != 600 {
		t.Fatalf("two automatic reductions: live %v", got)
	}
}

// Re-sized within its breach episode, a row keeps its veto clock: the new
// revision's record is due when the first one was, its notice is the same
// alert episode, and a batch that grows every verdict is never starved. A
// new episode starts a new window; an owner's veto holds the line for the
// rest of the episode.
func TestMandateReductionResizeKeepsTheVetoClock(t *testing.T) {
	t.Parallel()
	rig := newAutomaticTestRig(t, automaticMandateAuthority)
	first := rig.mandateProposal(automaticTestStockRow(), 20, 1, 1)
	rev1 := rig.install(first)
	rig.cycle()
	rec1 := rig.record(first.Key, rev1)
	rig.notice(rec1)
	rec1 = rig.record(first.Key, rev1)
	for step, qty := range []int{30, 25, 40} {
		rig.advance(5 * time.Minute)
		grown := rig.mandateProposal(automaticTestStockRow(), qty, 1, 1)
		rev := rig.install(grown)
		rig.cycle()
		rec := rig.record(grown.Key, rev)
		if !rec.SubmitAt.Equal(rec1.SubmitAt) || !rec.NoticedAt.Equal(rec1.NoticedAt) || rec.Episode != first.Mandate.Episode {
			t.Fatalf("step %d: re-sized record %+v, first %+v", step, rec, rec1)
		}
		n1, _ := alertShadowAutomaticEpisodeKey(alertShadowBrokerScope{account: "DU1234567", mode: "paper"}, automaticNoticeKey{Key: first.Key, Revision: rev1, Episode: rec1.Episode})
		n2, _ := alertShadowAutomaticEpisodeKey(alertShadowBrokerScope{account: "DU1234567", mode: "paper"}, automaticNoticeKey{Key: grown.Key, Revision: rev, Episode: rec.Episode})
		if n1 != n2 {
			t.Fatalf("step %d: a re-size opened a new notice", step)
		}
	}
	rig.advance(16 * time.Minute) // 31 minutes after the first notice
	rig.cycle()
	if rig.revalidations != 1 {
		t.Fatalf("attempts at the first window's end = %d, want 1", rig.revalidations)
	}

	// A new episode starts a new window.
	rig = newAutomaticTestRig(t, automaticMandateAuthority)
	a := rig.mandateProposal(automaticTestStockRow(), 20, 1, 1)
	revA := rig.install(a)
	rig.cycle()
	recA := rig.record(a.Key, revA)
	rig.advance(10 * time.Minute)
	b := rig.mandateProposal(automaticTestStockRow(), 30, 1, 1)
	b.Mandate.Episode = "mandate-synthetic-2"
	revB := rig.install(b)
	rig.cycle()
	if recB := rig.record(b.Key, revB); !recB.SubmitAt.After(recA.SubmitAt) {
		t.Fatalf("a new episode kept the old clock: %s vs %s", recB.SubmitAt, recA.SubmitAt)
	}

	// The owner vetoes the line: a re-size in the same episode gets no record.
	rig = newAutomaticTestRig(t, automaticMandateAuthority)
	v1 := rig.mandateProposal(automaticTestStockRow(), 20, 1, 1)
	revV := rig.install(v1)
	rig.cycle()
	if res, err := rig.engine.Veto(context.Background(), rpc.TradeProposalVetoParams{Key: v1.Key, Revision: revV, Origin: rpc.OrderOriginHumanTTY}); err != nil || !res.Accepted {
		t.Fatalf("veto: %+v %v", res, err)
	}
	v2 := rig.mandateProposal(automaticTestStockRow(), 30, 1, 1)
	revV2 := rig.install(v2)
	rig.cycle()
	rig.noRecord(v2.Key, revV2)
	if a := rig.engine.Snapshot(false).Proposals[0].Automatic; a == nil || a.State != rpc.TradeProposalAutomaticVetoed {
		t.Fatalf("the re-sized row does not show the episode's veto: %+v", a)
	}
	if rig.engine.sameContractChannels()(v2) != sameContractExcluded {
		t.Fatal("the merge counts on a vetoed line being placed")
	}
	rig.advance(time.Hour)
	rig.cycle()
	if rig.revalidations != 0 {
		t.Fatalf("a vetoed line was attempted in its episode: %d", rig.revalidations)
	}
}
