package daemon

import (
	"context"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/osauer/canary/v2/internal/mandate"
	"github.com/osauer/canary/v2/internal/risk"
	"github.com/osauer/canary/v2/internal/rpc"
)

// Owner decision 2026-10-23 18:22 CEST: an option buy adds risk within the
// premium limits, never within the volatility budget. On a book far over
// the budget a stock buy stays refused, an option buy inside the premium
// limits passes with the verdict as its warning, and a short option opened
// to sell stays on the volatility gate.
func TestMandateOptionAdditionsJudgedByPremiumNotVolatility(t *testing.T) {
	srv, _ := mandateTestServer(t, testMandateConstitution())
	seedMandateHistory(t, srv, 0)
	srv.mandatePositionsForTest = func(context.Context) (*rpc.PositionsResult, error) { return mandateTestPositions(3000), nil }
	res := srv.evaluateMandate(t.Context(), false)
	if res.Verdict.Status != mandate.StatusReduce || res.Gate == nil || !res.Gate.RefusesAdditions {
		t.Fatalf("fixture: %s gate %+v", res.Verdict.Status, res.Gate)
	}
	if blocker := srv.mandateBudgetGate(mandateBuy(10), mandateOpen); blocker == nil || blocker.Code != mandateBudgetCode {
		t.Fatalf("a stock buy over budget: %+v, want mandate_budget", blocker)
	}
	call := rpc.OrderDraft{Action: rpc.OrderActionBuy, Quantity: 10, LimitPrice: 8, Contract: rpc.ContractParams{Symbol: "SYNB", SecType: "OPT", Currency: "EUR", Right: "C", Strike: 80, Expiry: "20270115", Multiplier: 100}}
	blocker, warning := srv.mandateBudgetJudgement(call, mandateOpen)
	if blocker != nil || warning == nil || warning.Code != mandateBudgetCode || !strings.Contains(warning.Message, "over the budget") || !strings.Contains(warning.Message, "premium limits") {
		t.Fatalf("a call buy over budget: blocker %+v warning %+v, want passed with the verdict as its warning", blocker, warning)
	}
	// The gate view reads both: stock additions refused, option additions
	// open with the same sentence as their warning.
	if o := res.Gate.Options; o == nil || o.RefusesAdditions || o.Warning != res.Gate.Reason || o.Warning == "" {
		t.Fatalf("options gate view %+v (stock reason %q)", res.Gate.Options, res.Gate.Reason)
	}
	// A short option opened to sell has no premium to bound its loss: the
	// volatility gate judges it as before.
	sell := rpc.OrderDraft{Action: rpc.OrderActionSell, Quantity: 5, LimitPrice: 2, Contract: rpc.ContractParams{Symbol: "SYNB", SecType: "OPT", Currency: "EUR", Right: "P", Strike: 70, Expiry: "20270115", Multiplier: 100}}
	if blocker := srv.mandateBudgetGate(sell, mandateOpen); blocker == nil || blocker.Code != mandateBudgetCode {
		t.Fatalf("a put sold to open over budget: %+v, want mandate_budget", blocker)
	}
	// The preview carries the warning on the order it admits.
	if w := mandateOptionWarning(&rpc.TradingBlocker{Code: mandateBudgetCode, Message: "Mandate budget: x. Reductions, closes, cancels and hedges stay open.", Action: "a"}); w.Severity != "warning" || !strings.HasPrefix(w.Message, "Mandate budget: x. This option buy") {
		t.Fatalf("warning %+v", w)
	}
}

// mandateTestLongOptionBook holds a long SYNB call book worth a third of the
// 300,000 EUR net liquidation beside the stock fixture, and a hedge-listed
// index put, so the premium budget and the hedge exclusion are exercised.
func mandateTestLongOptionBook(t *testing.T, srv *Server, calls float64) *rpc.PositionsResult {
	t.Helper()
	pos := mandateTestUngrouped(100)
	iv := 0.3
	mv := calls * 100 * 20
	pos.Options = append(pos.Options, rpc.PositionView{Symbol: "SYNB", SecType: rpc.SecTypeOption, ConID: 9005, Currency: "EUR", Quantity: calls, Multiplier: 100, Mark: 20, AvgCost: 1800,
		MarketValue: mv, MarketValueBase: &mv, Expiry: "20270115", Strike: 80, Right: "C", IV: &iv, Underlying: new(80.0),
		Delta: new(0.6), Gamma: new(0.02), Theta: new(-0.05), Vega: new(0.3)})
	hedge := srv.rulebookPolicy().HedgeSymbols[0]
	hmv := 3000.0
	pos.Options = append(pos.Options, rpc.PositionView{Symbol: hedge, SecType: rpc.SecTypeOption, ConID: 9006, Currency: "USD", Quantity: 1, Multiplier: 100, Mark: 30, AvgCost: 3500, FXRate: new(0.9),
		MarketValue: 3000, MarketValueBase: &hmv, Expiry: "20270115", Strike: 500, Right: "P", IV: &iv, Underlying: new(560.0),
		Delta: new(-0.3), Gamma: new(0.01), Theta: new(-0.1), Vega: new(0.5)})
	return mandateTestGrouped(pos)
}

// The book's premium at risk is capped by the premium budget's act level of
// the regime set in force (35% by default): a buy that would take the book
// past it is refused naming the figure, the budget and the room; the
// protection put is outside the budget.
func TestMandatePremiumGateCapsTheBook(t *testing.T) {
	srv, _ := mandateTestServer(t, testMandateConstitution())
	seedMandateHistory(t, srv, 0)
	srv.mandatePositionsForTest = func(context.Context) (*rpc.PositionsResult, error) { return mandateTestLongOptionBook(t, srv, 50), nil }
	res := srv.evaluateMandate(t.Context(), false)
	e := res.Exposure
	if e == nil || e.NLVBase == nil || *e.NLVBase != 300000 || e.NamePremiumCapPct != 15 || e.BookPremiumCapPct != 35 {
		t.Fatalf("exposure %+v", e)
	}
	// 50 calls at 20 EUR: 100,000 EUR of premium, over the 90,000 paid, so
	// counted at its value: 33.3% of net liquidation; the hedge put's 3,500
	// paid is left out and named.
	if e.Book.PremiumAtRiskBase == nil || math.Abs(*e.Book.PremiumAtRiskBase-100000) > 1e-6 || e.Book.HedgePremiumBase == nil || math.Abs(*e.Book.HedgePremiumBase-3500*0.9) > 1e-6 || e.Book.PremiumOver {
		t.Fatalf("book premium %+v", e.Book)
	}
	names := map[string]rpc.MandateExposureRow{}
	for _, n := range e.Names {
		names[n.Underlying] = n
	}
	if n := names["SYNB"]; n.PremiumAtRiskBase == nil || *n.PremiumAtRiskBase != 100000 || n.PremiumCapBase == nil || *n.PremiumCapBase != 45000 || !n.PremiumOver || n.PremiumRoomBase == nil || *n.PremiumRoomBase != 0 {
		t.Fatalf("SYNB premium %+v", n)
	}
	// A 2% buy on SYNA: the name has room, the book has not.
	call := rpc.OrderDraft{Action: rpc.OrderActionBuy, Quantity: 10, LimitPrice: 8, Contract: rpc.ContractParams{Symbol: "SYNA", SecType: "OPT", Currency: "USD", Right: "C", Strike: 100, Expiry: "20270115", Multiplier: 100}}
	blocker := srv.mandateBudgetGate(call, mandateOpen)
	if blocker == nil || blocker.Code != rpc.MandatePremiumBlockerCode || !strings.Contains(blocker.Message, "the book's premium at risk would be 107,200 EUR, 35.7% of net liquidation 300,000 EUR, over the 35.0% premium budget") || !strings.Contains(blocker.Action, "room for 5,000 EUR more premium") {
		t.Fatalf("a buy past the premium budget: %+v", blocker)
	}
	// Half the size fits the budget: 4,500 EUR more, 34.8%.
	call.Quantity = 5
	if blocker, _ := srv.mandateBudgetJudgement(call, mandateOpen); blocker != nil {
		t.Fatalf("a buy inside the premium budget: %+v", blocker)
	}
	// A buy of SYNB itself is refused by its own name's cap first.
	call.Contract = rpc.ContractParams{Symbol: "SYNB", SecType: "OPT", Currency: "EUR", Right: "C", Strike: 80, Expiry: "20270115", Multiplier: 100}
	call.Quantity, call.LimitPrice = 1, 20
	if blocker := srv.mandateBudgetGate(call, mandateOpen); blocker == nil || !strings.Contains(blocker.Message, "premium at risk in SYNB would be 102,000 EUR, 34.0% of net liquidation 300,000 EUR, over the 15.0% limit for one name (now 33.3%)") {
		t.Fatalf("a buy past the name's cap: %+v", blocker)
	}
	// The options gate view reads open while the book is under its cap.
	if o := res.Gate.Options; o == nil || o.RefusesAdditions {
		t.Fatalf("options gate %+v", res.Gate.Options)
	}
}

// The exposure through the greeks: net delta in money, through the beta,
// gamma per 1% move, theta a day, vega a point, per name and for the book,
// from the positions' greeks and the verdict's betas; a missing greek or a
// stale quote leaves the figure absent with its reason, never zero.
func TestMandateExposureMeasuresGreeksNeverZero(t *testing.T) {
	srv, _ := mandateTestServer(t, testMandateConstitution())
	seedMandateHistory(t, srv, 0)
	pos := mandateTestUngrouped(100)
	// The SYNA short calls carry greeks; a second SYNA leg has no vega.
	pos.Options[0].Delta, pos.Options[0].Gamma, pos.Options[0].Theta, pos.Options[0].Vega = new(0.4), new(0.02), new(-0.05), new(0.3)
	pos.Options = append(pos.Options, rpc.PositionView{Symbol: "SYNB", SecType: rpc.SecTypeOption, ConID: 9007, Currency: "EUR", Quantity: 3, Multiplier: 100, Mark: 2, AvgCost: 250,
		MarketValue: 600, MarketValueBase: new(600.0), Expiry: "20270115", Strike: 90, Right: "C", IV: new(0.3), Underlying: new(80.0),
		Delta: new(0.2), Gamma: new(0.03), Theta: new(-0.02)})
	srv.mandatePositionsForTest = func(context.Context) (*rpc.PositionsResult, error) { return mandateTestGrouped(pos), nil }
	res := srv.evaluateMandate(t.Context(), false)
	if res.Verdict.Status != mandate.StatusWithin || res.Exposure == nil {
		t.Fatalf("fixture: %s exposure %v", res.Verdict.Status, res.Exposure != nil)
	}
	e := res.Exposure
	rows := map[string]rpc.MandateExposureRow{}
	for _, n := range e.Names {
		rows[n.Underlying] = n
	}
	near := func(got *float64, want float64) bool { return got != nil && math.Abs(*got-want) < 1e-6 }
	// SYNA: 100 shares at 100 USD (0.9) = 9,000; −2 calls × 100 × 0.4 × 100 × 0.9 = −7,200.
	syna := rows["SYNA"]
	if !near(syna.DeltaBase, 1800) || !near(syna.GammaBase, 0.02*0.01*100*-200*100*0.9) || !near(syna.ThetaBase, -0.05*-200*0.9) || !near(syna.VegaBase, 0.3*-200*0.9) || syna.Legs != 2 || syna.Options != 1 {
		t.Fatalf("SYNA %+v", syna)
	}
	if syna.Beta == nil || syna.ContributionPct == nil || !near(syna.DeltaBetaBase, 1800**syna.Beta) {
		t.Fatalf("SYNA beta %+v", syna)
	}
	// SYNB: 100 shares at 80 = 8,000; 3 calls × 100 × 0.2 × 80 = 4,800; no vega.
	synb := rows["SYNB"]
	if !near(synb.DeltaBase, 12800) || !near(synb.GammaBase, 0.03*0.01*80*300*80) || !near(synb.ThetaBase, -0.02*300) || synb.VegaBase != nil || !slices.ContainsFunc(synb.Unknown, func(s string) bool { return strings.Contains(s, "no vega") }) {
		t.Fatalf("SYNB %+v", synb)
	}
	if !near(synb.PremiumAtRiskBase, 750) || !near(synb.PremiumPctNLV, 0.25) || !near(synb.PremiumRoomBase, 44250) {
		t.Fatalf("SYNB premium %+v (the 750 paid, above the 600 value)", synb)
	}
	// The book: delta and gamma and theta sum; vega is unknown because
	// SYNB's is, and says so.
	if !near(e.Book.DeltaBase, 1800+12800) || e.Book.VegaBase != nil || !slices.Contains(e.Book.Unknown, "vega unknown for SYNB") || !near(e.Book.PremiumAtRiskBase, 750) {
		t.Fatalf("book %+v", e.Book)
	}
	// Names are listed by delta exposure, largest first; the bill is no name.
	if len(e.Names) != 2 || e.Names[0].Underlying != "SYNB" {
		t.Fatalf("names %+v", e.Names)
	}
	// A stale option quote leaves every greek of its name unknown.
	pos.Options[0].Stale, pos.Options[0].StaleReason = true, "quote retained"
	res = srv.evaluateMandate(t.Context(), false)
	for _, n := range res.Exposure.Names {
		if n.Underlying == "SYNA" && (n.DeltaBase != nil || n.ThetaBase != nil || !slices.ContainsFunc(n.Unknown, func(s string) bool { return strings.Contains(s, "quote stale") })) {
			t.Fatalf("stale SYNA %+v", n)
		}
	}
	if res.Exposure.Book.DeltaBase != nil {
		t.Fatalf("book delta with a stale leg: %+v", res.Exposure.Book)
	}
}

// The owner's one-shot override of mandate.vol_target_pct lifts the
// volatility budget's additions gate for the owner's orders: a stock buy a
// book over budget refused passes with the override as its warning, the
// gate view names it, and the other mandate keys take no override. The holds
// that bound a loss stay: at the maximum loss nothing adds.
func TestMandateAdditionsOverrideLiftsTheVolatilityGate(t *testing.T) {
	c := testMandateConstitution()
	c.Override.MaxDurationHours = new(24)
	srv, st := mandateTestServer(t, c)
	seedMandateHistory(t, srv, 0)
	srv.mandatePositionsForTest = func(context.Context) (*rpc.PositionsResult, error) { return mandateTestPositions(3000), nil }
	res := srv.evaluateMandate(t.Context(), false)
	if res.Verdict.Status != mandate.StatusReduce || srv.mandateBudgetGate(mandateBuy(10), mandateOpen) == nil {
		t.Fatalf("fixture: %s", res.Verdict.Status)
	}
	// Only the volatility target takes an override; the mandate keys are
	// constitution keys once the table is written.
	if !slices.ContainsFunc(risk.ConstitutionLimits(c), func(l risk.ConstitutionLimit) bool { return l.Key == risk.MandateAdditionsOverrideControl }) {
		t.Fatal("mandate.vol_target_pct is not a constitution limit")
	}
	if _, err := st.GrantOverrideForScope(rpc.OverrideParams{Control: "mandate.max_loss_pct", Reason: "test", Hours: 2}, c, srv.currentBrokerStateScope()); err == nil || !strings.Contains(err.Error(), "only mandate.vol_target_pct takes a one-shot override") {
		t.Fatalf("an override of max_loss_pct: %v, want refused", err)
	}
	rec, err := st.GrantOverrideForScope(rpc.OverrideParams{Control: risk.MandateAdditionsOverrideControl, Reason: "earnings week, adding by hand", Hours: 2}, c, srv.currentBrokerStateScope())
	if err != nil {
		t.Fatal(err)
	}
	blocker, warning := srv.mandateBudgetJudgement(mandateBuy(10), mandateOpen)
	if blocker != nil || warning == nil || !strings.Contains(warning.Message, "your override of mandate.vol_target_pct lifts the volatility budget until") || !strings.Contains(warning.Message, "earnings week, adding by hand") {
		t.Fatalf("a stock buy under the override: blocker %+v warning %+v", blocker, warning)
	}
	res = srv.evaluateMandate(t.Context(), false)
	if g := res.Gate; g == nil || g.RefusesAdditions || g.Override == nil || g.Override.ID != rec.ID || !strings.Contains(g.Reason, "lifts the volatility budget") || g.Options == nil || g.Options.RefusesAdditions {
		t.Fatalf("gate view under the override: %+v", res.Gate)
	}
	// A machine sleeve's order reads no override: the core's line stays judged.
	// (Covered by the placement tests; here the sleeve argument alone.)
	if hard := mandateHardHolds(res.Verdict, mandate.SleeveOwner, "EUR", ""); hard != nil {
		t.Fatalf("hard hold with room left: %+v", hard)
	}
	noRoom := *res.Verdict
	noRoom.Capital.Room, noRoom.Capital.BudgetAnnualSD = 0, 0
	if hard := mandateHardHolds(&noRoom, mandate.SleeveOwner, "EUR", ""); hard == nil || !strings.Contains(hard.Message, "at the maximum loss no sleeve adds") {
		t.Fatalf("hard hold at the maximum loss: %+v", hard)
	}
	// Expired, the gate binds again.
	srv.now = func() time.Time { return mandateTestNow.Add(3 * time.Hour) }
	st.now = srv.now
	if o := srv.mandateAdditionsOverride(srv.nowUTC()); o != nil {
		t.Fatalf("an expired override still read: %+v", o)
	}
}

// The rulebook migration writes option_name_act_pct into an older file at
// the owner's 15% with its provenance, leaving the policy in force unchanged.
func TestRulebookMigrationWritesTheOptionNameCap(t *testing.T) {
	template := string(RulebookPolicyTemplate("v9.9.8"))
	var lines []string
	for line := range strings.SplitSeq(template, "\n") {
		if strings.Contains(line, "option_name_act_pct") || strings.HasPrefix(line, "# Canary defaults, not yet reviewed.") {
			continue
		}
		lines = append(lines, line)
	}
	old := []byte(strings.Join(lines, "\n"))
	before, err := parseRulebookPolicy(old)
	if err != nil || !slices.Equal(before.missing, []string{"option_name_act_pct"}) || before.policy.OptionNameActPct != 15 {
		t.Fatalf("older file: err %v missing %v cap %v", err, before.missing, before.policy.OptionNameActPct)
	}
	out, changes, _, err := migrateRulebookPolicyFile(old, "v9.9.9")
	if err != nil || !slices.Equal(changes, []string{"added option_name_act_pct at Canary's default"}) {
		t.Fatalf("migrate: %v %v", err, changes)
	}
	after, err := parseRulebookPolicy(out)
	if err != nil || len(after.missing) != 0 || after.policy.OptionNameActPct != 15 || after.policy.EffectiveFingerprintKey() != before.policy.EffectiveFingerprintKey() {
		t.Fatalf("migrated: err %v missing %v", err, after.missing)
	}
	if !strings.Contains(string(out), "option_name_act_pct = 15") || !strings.Contains(string(out), "2026-10-23 18:22 CEST") {
		t.Fatalf("the key and its provenance are not written:\n%s", out)
	}
	for _, bad := range []string{"option_name_act_pct = -1", "option_name_act_pct = 1001"} {
		if _, err := parseRulebookPolicy([]byte(strings.Replace(string(out), "option_name_act_pct = 15", bad, 1))); err == nil {
			t.Fatalf("%s accepted", bad)
		}
	}
}
