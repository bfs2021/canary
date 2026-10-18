package daemon

import (
	"context"
	"math"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"github.com/osauer/canary/v2/internal/core"
	"github.com/osauer/canary/v2/internal/mandate"
	"github.com/osauer/canary/v2/internal/rpc"
)

// Seats (internal-docs/design/mandate-risk-budget.md, "Seats"; owner
// decision 2026-10-18 08:06 CEST), witnessed on synthetic books.

// The machine's seat is only as big as what it runs: in this build the core
// sends no orders, so its stage is 0 whatever scale_pct says and the owner
// has the whole budget. The core's pinned contract is placed with the core
// either way; a book without [mandate.core] places nothing.
func TestMandateSeatPlanGivesTheMachineWhatItRuns(t *testing.T) {
	c := testCoreConstitution()
	plan := mandateSeatPlan(c, false, []int{coreTestConID})
	if len(plan.Machine) != 2 || plan.Machine[0].Sleeve != mandate.SleeveCore || plan.Machine[0].SharePct != 6 || plan.Machine[0].StagePct != 0 ||
		!strings.Contains(plan.Machine[0].Note, "sends no orders") {
		t.Fatalf("core seat while it sends no orders: %+v", plan.Machine)
	}
	if o := plan.Machine[1]; o.Sleeve != mandate.SleeveOverlay || o.SharePct != 4 || o.StagePct != 0 || !strings.Contains(o.Note, "no strategy runs") {
		t.Fatalf("overlay seat: %+v", o)
	}
	if plan.SleeveOf("con:9030") != mandate.SleeveCore || plan.SleeveOf("con:9001") != mandate.SleeveOwner {
		t.Fatalf("placement %+v", plan.Placed)
	}
	if running := mandateSeatPlan(c, true, []int{coreTestConID}); running.Machine[0].StagePct != 100 {
		t.Fatalf("a running core's stage is its scale_pct: %+v", running.Machine[0])
	}
	if bare := mandateSeatPlan(testMandateConstitution(), true, []int{coreTestConID}); len(bare.Placed) != 0 || bare.Machine[0].StagePct != 0 || !strings.Contains(bare.Machine[0].Note, "inactive") {
		t.Fatalf("no core table: %+v", bare)
	}
	if srv := (&Server{}); srv.coreRuns() {
		t.Fatal("this build serves no core orders, so the core runs nothing")
	}
}

// With the core running at its full scale (6 of the 15 points), the owner's
// additions stop at the owner's seat, the rest of the budget, even while the
// whole book has room; inside the seat they pass. With the core running
// nothing, the same order passes: the owner has the whole budget, as today.
func TestMandateGateHoldsTheOwnerToTheOwnersSeat(t *testing.T) {
	srv, _ := mandateTestServer(t, testCoreConstitution())
	seedMandateHistory(t, srv, 0)
	srv.mandatePositionsForTest = func(context.Context) (*rpc.PositionsResult, error) { return mandateTestPositions(100), nil }
	srv.coreSendsOrdersForTest = true
	res := srv.evaluateMandate(t.Context(), false)
	if res.Verdict.Status != mandate.StatusWithin {
		t.Fatalf("book: %s %v", res.Verdict.Status, res.Verdict.Reasons)
	}
	owner, _ := res.Verdict.Seat(mandate.SleeveOwner)
	coreSeat, _ := res.Verdict.Seat(mandate.SleeveCore)
	if budget := res.Verdict.Capital.BudgetAnnualSD; math.Abs(coreSeat.SeatBase-0.4*budget) > 1e-6 || math.Abs(owner.SeatBase-0.6*budget) > 1e-6 || owner.Over {
		t.Fatalf("seats owner %+v core %+v budget %g", owner, coreSeat, budget)
	}
	// The first size that takes the owner over its seat still fits the
	// whole budget, so only the seat refuses it.
	_, kept, _, _ := srv.mandateVerdicts.current()
	over := 0
	for q := 10; q <= 5000 && over == 0; q += 10 {
		book, _ := srv.mandateOrderBook(mandateBuy(q), kept, mandateTestNow)
		leg, _ := mandateOrderLeg(mandateBuy(q), &book)
		book.Seats = book.Seats.WithPlaced(leg.ID, mandate.SleeveOwner)
		with := mandate.Evaluate(mandate.WithLeg(book, leg))
		if s, _ := with.Seat(mandate.SleeveOwner); s.Over {
			if with.Risk.SDAnnualBase > with.Capital.BudgetAnnualSD {
				t.Fatalf("fixture: the book is over budget before the owner is over its seat at %d", q)
			}
			over = q
		}
	}
	if over == 0 {
		t.Fatal("fixture: no size takes the owner over its seat")
	}
	blocker := srv.mandateBudgetGate(mandateBuy(over), mandateOpen)
	if blocker == nil || blocker.Code != mandateBudgetCode || !strings.Contains(blocker.Message, "your seat would carry") || !strings.Contains(blocker.Message, "less the machine's seats") {
		t.Fatalf("owner over its seat: %+v", blocker)
	}
	if blocker := srv.mandateBudgetGate(mandateBuy(over-10), mandateOpen); blocker != nil {
		t.Fatalf("inside the owner's seat: %+v", blocker)
	}
	// The core running nothing gives the owner the whole budget back.
	srv.coreSendsOrdersForTest = false
	srv.evaluateMandate(t.Context(), false)
	if blocker := srv.mandateBudgetGate(mandateBuy(over), mandateOpen); blocker != nil {
		t.Fatalf("with no machine seat the owner has the whole budget: %+v", blocker)
	}
}

// Every unit of the core's pinned contract is the core's, whoever buys it:
// while the core runs nothing its seat is zero, so a hand buy of the fund is
// refused, by contract id or by its symbol without one; a sale passes. With
// the core running, a buy inside its seat passes and one past it is refused.
func TestMandateGatePlacesTheCoreFundWithTheCore(t *testing.T) {
	c := testCoreConstitution()
	srv := coreTestServer(t, c, mandateTestPositions(100), 0.25)
	srv.coreSendsOrdersForTest = false
	if res := srv.evaluateMandate(t.Context(), false); res.Verdict.Status != mandate.StatusWithin {
		t.Fatalf("book: %s %v", res.Verdict.Status, res.Verdict.Reasons)
	}
	buy := func(q int, conID int) rpc.OrderDraft {
		return rpc.OrderDraft{Action: rpc.OrderActionBuy, Quantity: q, LimitPrice: 100, Contract: rpc.ContractParams{ConID: conID, Symbol: core.Symbol, SecType: "STK", Currency: core.Currency, PrimaryExch: "IBIS"}}
	}
	blocker := srv.mandateBudgetGate(buy(300, coreTestConID), mandateOpen)
	if blocker == nil || !strings.Contains(blocker.Message, "the core's seat would carry") || !strings.Contains(blocker.Message, "sends no orders") ||
		!strings.Contains(blocker.Message, "every unit of the core's fund is the core's") {
		t.Fatalf("a hand buy of the core's fund while the core runs nothing: %+v", blocker)
	}
	if blocker := srv.mandateBudgetGate(buy(10, coreTestConID), rpc.OrderPositionImpact{Before: 10, After: 0, Effect: rpc.OrderPositionEffectClose}); blocker != nil {
		t.Fatalf("a sale of the core's fund: %+v", blocker)
	}
	if mandateOrderSleeve(srv.mandateCoreContracts(t.Context(), c), buy(10, 4242)) != mandate.SleeveOwner {
		t.Fatal("another listing under another contract id is the owner's")
	}

	srv.coreSendsOrdersForTest = true
	res := srv.evaluateMandate(t.Context(), false)
	seat, _ := res.Verdict.Seat(mandate.SleeveCore)
	if seat.SeatBase <= 0 {
		t.Fatalf("a running core's seat: %+v", seat)
	}
	if blocker := srv.mandateBudgetGate(buy(300, coreTestConID), mandateOpen); blocker != nil {
		t.Fatalf("a buy inside the core's seat: %+v", blocker)
	}
	// Far more than the seat holds: the seat refuses before the budget (the
	// core's seat is 40% of it here, and the owner holds the rest's share).
	big := int(math.Ceil(2 * seat.SeatBase / (0.16 * 100)))
	blocker = srv.mandateBudgetGate(buy(big, coreTestConID), mandateOpen)
	if blocker == nil || blocker.Code != mandateBudgetCode || !strings.Contains(blocker.Message, "the core's seat would carry") && !strings.Contains(blocker.Message, "over the budget") {
		t.Fatalf("a buy past the core's seat: %+v", blocker)
	}
}

// An option buy the evaluator cannot price is judged against its sleeve by
// the sleeve's own risk plus the volatility-free bound: a squeeze the whole
// budget would admit is refused once the owner's seat is smaller.
func TestMandateGateBoundsAnUnpricedOptionBuyByItsSeat(t *testing.T) {
	srv, _ := mandateTestServer(t, testCoreConstitution())
	seedMandateHistory(t, srv, 0)
	srv.mandatePositionsForTest = func(context.Context) (*rpc.PositionsResult, error) { return mandateTestPositions(100), nil }
	option := rpc.OrderDraft{Action: rpc.OrderActionBuy, Quantity: 1, LimitPrice: 40, Contract: rpc.ContractParams{Symbol: "SYNA", SecType: "OPT", Currency: "USD", Right: "C", Strike: 50, Expiry: "20270115", Multiplier: 100}}
	srv.evaluateMandate(t.Context(), false)
	// The largest count the whole book admits with the core running nothing.
	most := 0
	for q := 1; q <= 12; q++ {
		option.Quantity = q
		if srv.mandateBudgetGate(option, mandateOpen) != nil {
			break
		}
		most = q
	}
	if most == 0 {
		t.Fatal("fixture: no unpriced option buy fits the whole book")
	}
	option.Quantity = most
	srv.coreSendsOrdersForTest = true
	res := srv.evaluateMandate(t.Context(), false)
	owner, _ := res.Verdict.Seat(mandate.SleeveOwner)
	_, kept, _, _ := srv.mandateVerdicts.current()
	b, why := mandateLongOptionBound(option, *kept, res.Verdict)
	if why != "" {
		t.Fatal(why)
	}
	worst := owner.StandaloneBase + b.riskAnnual - res.Verdict.Risk.SDAnnualBase
	blocker := srv.mandateBudgetGate(option, mandateOpen)
	switch {
	case worst > owner.SeatBase && (blocker == nil || !strings.Contains(blocker.Message, "your seat could carry")):
		t.Fatalf("%d contracts bound at %.0f over the owner's seat %.0f: %+v", most, worst, owner.SeatBase, blocker)
	case worst <= owner.SeatBase && blocker != nil:
		t.Fatalf("%d contracts bound at %.0f inside the owner's seat %.0f: %+v", most, worst, owner.SeatBase, blocker)
	}
	if worst <= owner.SeatBase {
		t.Fatalf("fixture: the seat does not bind (%.0f of %.0f); the test proves nothing", worst, owner.SeatBase)
	}
}

// mandateSeatedFixture measures the reduction fixture's book with a seat plan:
// the core's lines placed with the core, its seat coreSeat of the budget.
func mandateSeatedFixture(t *testing.T, pos *rpc.PositionsResult, ratio float64, coreSeat float64, coreConIDs ...int) mandateReductionInput {
	t.Helper()
	in := mandateReductionFixture(t, pos, ratio, 1e9, 2)
	book := *in.Input
	placed := map[string]string{}
	for _, id := range coreConIDs {
		placed[mandateLegID(rpc.PositionView{ConID: id})] = mandate.SleeveCore
	}
	share := coreSeat * book.Capital.BudgetAnnualSD / book.Capital.BudgetBase * 100
	book.Seats = &mandate.SeatPlan{Machine: []mandate.MachineSeat{{Sleeve: mandate.SleeveCore, SharePct: share, StagePct: 100}, {Sleeve: mandate.SleeveOverlay, SharePct: 4}}, Placed: placed}
	return mandateReductionVerdict(book, 2)
}

func mandateContribution(v *mandate.Verdict, conID int) float64 {
	for _, l := range v.Legs {
		if l.ID == mandateLegID(rpc.PositionView{ConID: conID}) {
			return l.ContributionPct
		}
	}
	return 0
}

// Over budget, the sleeve furthest over its seat is cut first, and within it
// the largest contributor: here the core, holding its fund while it has no
// seat, is cut before the owner's larger line; then the owner's.
func TestMandateReductionCutsTheSleeveFurthestOverItsSeatFirst(t *testing.T) {
	pos := mandateReductionBook(mandateReductionStock("SYNA", 8101, 900, 100), mandateReductionStock("SYNB", 8102, 300, 100), mandateReductionStock("IBCF", 8103, 1100, 100))
	in := mandateSeatedFixture(t, pos, 1.3, 0, 8103)
	v := in.Result.Verdict
	core, _ := v.Seat(mandate.SleeveCore)
	owner, _ := v.Seat(mandate.SleeveOwner)
	if !(mandateContribution(v, 8101) > mandateContribution(v, 8103)) || !(core.ExcessBase > owner.ExcessBase) {
		t.Fatalf("fixture: SYNA %.1f%% IBCF %.1f%%, core excess %.0f owner excess %.0f", mandateContribution(v, 8101), mandateContribution(v, 8103), core.ExcessBase, owner.ExcessBase)
	}
	rows, st := mandateReductionRun(t, in, pos)
	if st.State != rpc.MandateReductionReducing || len(rows) == 0 {
		t.Fatalf("state %s rows %d: %s", st.State, len(rows), st.Reason)
	}
	assertMandateRowsReduceOnly(t, rows, pos)
	first := rows[0]
	if first.Contract.ConID != 8103 || first.Mandate.Rank != 1 || first.Mandate.Sleeve != mandate.SleeveCore || !strings.Contains(first.Reason, "the core's seat, the seat furthest over") {
		t.Fatalf("first row %d %+v: %s", first.Contract.ConID, first.Mandate, first.Reason)
	}
	for _, row := range rows[1:] {
		if row.Mandate.Sleeve != mandate.SleeveOwner {
			t.Fatalf("after the core, the owner's lines: %+v", row.Mandate)
		}
	}
	if st.Plan[0].Sleeve != mandate.SleeveCore {
		t.Fatalf("plan %+v", st.Plan)
	}
}

// A machine sleeve whose own risk fits its seat is not cut for the owner's
// excess, even as the largest contributor: the old rule would have cut the
// core's fund first, the seats cut only the owner's lines.
func TestMandateReductionLeavesASleeveInsideItsSeat(t *testing.T) {
	pos := mandateReductionBook(mandateReductionStock("SYNA", 8201, 420, 100), mandateReductionStock("SYNB", 8202, 300, 100), mandateReductionStock("IBCF", 8203, 1080, 100))
	probe := mandateSeatedFixture(t, pos, 1.3, 0, 8203)
	coreAlone, _ := probe.Result.Verdict.Seat(mandate.SleeveCore)
	seat := 1.02 * coreAlone.StandaloneBase / probe.Result.Verdict.Capital.BudgetAnnualSD
	in := mandateSeatedFixture(t, pos, 1.3, seat, 8203)
	v := in.Result.Verdict
	core, _ := v.Seat(mandate.SleeveCore)
	if core.Over || seat >= 0.95 || mandateContribution(v, 8203) < mandateContribution(v, 8201) || mandateContribution(v, 8203) < mandateContribution(v, 8202) {
		t.Fatalf("fixture: seat %.2f of the budget, core %+v, IBCF %.1f%% SYNA %.1f%% SYNB %.1f%%", seat, core, mandateContribution(v, 8203), mandateContribution(v, 8201), mandateContribution(v, 8202))
	}
	rows, st := mandateReductionRun(t, in, pos)
	if st.State != rpc.MandateReductionReducing || len(rows) == 0 {
		t.Fatalf("state %s: %s", st.State, st.Reason)
	}
	assertMandateRowsReduceOnly(t, rows, pos)
	for _, row := range rows {
		if row.Contract.ConID == 8203 || row.Mandate.Sleeve != mandate.SleeveOwner {
			t.Fatalf("the core inside its seat was cut: %+v", row.Mandate)
		}
	}
	// Without seats the old rule takes the largest contributor, the fund.
	old := mandateReductionFixture(t, pos, 1.3, 1e9, 2)
	if rows, _ := mandateReductionRun(t, old, pos); len(rows) == 0 || rows[0].Contract.ConID != 8203 {
		t.Fatalf("fixture: without seats the fund is not cut first: %+v", rows)
	}
}

// A sleeve over its seat is cut toward its seat, not past it for another
// sleeve's excess: the core goes first, by less than the old rule took from
// it, and the owner's lines carry the owner's excess.
func TestMandateReductionStopsASleevesCutAtItsSeat(t *testing.T) {
	pos := mandateReductionBook(mandateReductionStock("SYNA", 8301, 600, 100), mandateReductionStock("SYNB", 8302, 300, 100), mandateReductionStock("IBCF", 8303, 3000, 100))
	probe := mandateSeatedFixture(t, pos, 1.3, 0, 8303)
	coreUse, _ := probe.Result.Verdict.Seat(mandate.SleeveCore)
	// The core over its seat by 70% of the book's excess: the furthest over,
	// but its seat is reached before the book's target.
	budget := probe.Result.Verdict.Capital.BudgetAnnualSD
	seat := (coreUse.UseBase - 0.7*(probe.Result.Verdict.Risk.SDAnnualBase-budget)) / budget
	in := mandateSeatedFixture(t, pos, 1.3, seat, 8303)
	v := in.Result.Verdict
	core, _ := v.Seat(mandate.SleeveCore)
	owner, _ := v.Seat(mandate.SleeveOwner)
	if !core.Over || core.ExcessBase <= owner.ExcessBase {
		t.Fatalf("fixture: core %+v owner %+v", core, owner)
	}
	rows, st := mandateReductionRun(t, in, pos)
	assertMandateRowsReduceOnly(t, rows, pos)
	cutOf := func(plan []rpc.TradeProposalMandatePlanOrder) int {
		for _, p := range plan {
			if p.Contract.ConID == 8303 {
				return p.Units
			}
		}
		return 0
	}
	if len(rows) == 0 || rows[0].Contract.ConID != 8303 || !slices.ContainsFunc(rows, func(r rpc.TradeProposal) bool { return r.Mandate.Sleeve == mandate.SleeveOwner }) {
		t.Fatalf("the core first, then the owner: %+v", st.Plan)
	}
	_, old := mandateReductionRun(t, mandateReductionFixture(t, pos, 1.3, 1e9, 2), pos)
	if cut, was := cutOf(st.Plan), cutOf(old.Plan); cut <= 0 || cut >= was {
		t.Fatalf("the core's cut %d, the old rule's %d", cut, was)
	}
}

// With the machine holding nothing, as today, the batch is the old one.
func TestMandateReductionWithAnEmptyMachineIsUnchanged(t *testing.T) {
	pos := mandateReductionBook(mandateReductionStock("SYNA", 8401, 900, 100), mandateReductionStock("SYNB", 8402, 400, 100), mandateReductionStock("SYNC", 8403, 500, 100))
	old := mandateReductionFixture(t, pos, 1.4, 1e9, 2)
	seated := mandateSeatedFixture(t, pos, 1.4, 0)
	a, _ := mandateReductionRun(t, old, pos)
	b, _ := mandateReductionRun(t, seated, pos)
	if len(a) == 0 || len(a) != len(b) {
		t.Fatalf("rows %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i].Contract.ConID != b[i].Contract.ConID || a[i].Quantity != b[i].Quantity || a[i].Mandate.Units != b[i].Mandate.Units {
			t.Fatalf("row %d: %d×%d vs %d×%d", i, a[i].Contract.ConID, a[i].Mandate.Units, b[i].Contract.ConID, b[i].Mandate.Units)
		}
	}
}

// Property: on synthetic books with a machine seat, the seats' batch
// reaches the target wherever the old order does, and every row the batch
// may send alone lowers the book's risk.
func TestMandateReductionSeatsNeverStopShort(t *testing.T) {
	r := rand.New(rand.NewPCG(11, 12))
	for trial := range 16 {
		pos := mandateReductionBook(mandateReductionStock("SYNA", 8501, float64(100+r.IntN(900)), 100), mandateReductionStock("SYNB", 8502, float64(100+r.IntN(600)), 100),
			mandateReductionStock("SYNC", 8503, float64(100+r.IntN(900)), 100), mandateReductionStock("IBCF", 8504, float64(200+r.IntN(2800)), 100))
		ratio := 1.05 + r.Float64()
		probe := mandateSeatedFixture(t, pos, ratio, 0, 8504)
		coreUse, _ := probe.Result.Verdict.Seat(mandate.SleeveCore)
		seat := r.Float64() * 1.5 * coreUse.UseBase / probe.Result.Verdict.Capital.BudgetAnnualSD
		in := mandateSeatedFixture(t, pos, ratio, min(seat, 0.9), 8504)
		_, st := mandateReductionRun(t, in, pos)
		_, old := mandateReductionRun(t, mandateReductionFixture(t, pos, ratio, 1e9, 2), pos)
		if old.TargetReached && !st.TargetReached {
			t.Fatalf("trial %d: the old order reaches the target, the seats' does not: %s", trial, st.Reason)
		}
		rows, _ := mandateReductionRun(t, in, pos)
		before := mandateRiskAfter(t, in, nil)
		for _, row := range rows {
			if after := mandateRiskAfter(t, in, map[int]float64{row.Contract.ConID: float64(row.Quantity)}); after >= before {
				t.Fatalf("trial %d: selling %d of %d alone leaves the risk %.0f, not under %.0f", trial, row.Quantity, row.Contract.ConID, after, before)
			}
		}
	}
}
