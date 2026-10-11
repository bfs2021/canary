package risk

import (
	"math"
	"testing"
	"time"
)

// A bill at 99.40 with 54 days left yields 4.08% on actual/365 and 4.02%
// on actual/360; the price at the yield comes back to 99.40; a 25 bp rise
// on 50,000 of face costs about 18 USD (the console's "+25 bp → −18 USD").
func TestBillYieldKnownAnswers(t *testing.T) {
	y365, err := BillYieldPct(99.40, 54, 365)
	if err != nil {
		t.Fatal(err)
	}
	near(t, "act/365 yield", y365, 4.080, 0.005)
	y360, err := BillYieldPct(99.40, 54, 360)
	if err != nil {
		t.Fatal(err)
	}
	near(t, "act/360 yield", y360, 4.024, 0.005)
	near(t, "price at yield", BillPricePer100(y365, 54, 365), 99.40, 1e-9)
	near(t, "+25 bp on 50,000", BillScenario(50000, y365, 54, 365, 25), -18.3, 0.2)
	near(t, "-25 bp on 50,000", BillScenario(50000, y365, 54, 365, -25), 18.3, 0.2)
	near(t, "DV01 on 50,000", -BillScenario(50000, y365, 54, 365, 1), 0.73, 0.02)

	if basis, count := BillYieldBasis("EUR"); basis != BillYieldBasisAct360 || count != 360 {
		t.Fatalf("EUR basis = %s/%g", basis, count)
	}
	if basis, count := BillYieldBasis("usd"); basis != BillYieldBasisAct365 || count != 365 {
		t.Fatalf("USD basis = %s/%g", basis, count)
	}
	if BondYieldBasis("EUR") != BondYieldBasisAnnual || BondYieldBasis("USD") != BondYieldBasisSemiannual {
		t.Fatal("bond yield basis per currency")
	}
	for _, bad := range []struct {
		price    float64
		days     int
		dayCount float64
	}{{0, 10, 365}, {99, 0, 365}, {99, 10, 0}, {math.Inf(1), 10, 365}} {
		if _, err := BillYieldPct(bad.price, bad.days, bad.dayCount); err == nil {
			t.Fatalf("BillYieldPct(%v) accepted", bad)
		}
	}
}

// Carry, rates and spread sum to the gain since purchase, lot by lot: a
// bill bought at 99.18 with 124 days left and priced 99.40 at 54 days gained
// 110 on 50,000 of face; held at its purchase yield it would have accreted
// more, so rates are negative; with a benchmark that moved by the same
// amount, the spread part is zero and rates carry the whole yield effect.
func TestSplitBillPnLSums(t *testing.T) {
	maturity := time.Date(2026, 12, 4, 0, 0, 0, 0, time.UTC)
	bought := maturity.AddDate(0, 0, -124)
	today := 54
	lots := []BondLot{{Face: 50000, PricePer100: 99.18, Date: bought}}
	split, err := SplitBillPnL(lots, 99.40, today, 365, maturity, nil)
	if err != nil {
		t.Fatal(err)
	}
	near(t, "total", split.Total, 110, 1e-9)
	near(t, "carry+rates", split.Carry+split.Rates, split.Total, 1e-9)
	if split.Spread != nil || split.Carry <= split.Total || split.Rates >= 0 {
		t.Fatalf("split = %+v", split)
	}
	near(t, "purchase yield", split.PurchaseYieldPct, 2.434, 0.005)

	// The benchmark moved exactly as the bill's yield did: no spread part.
	ownNow, _ := BillYieldPct(99.40, today, 365)
	benchThen, benchNow := 2.0, 2.0+(ownNow-split.PurchaseYieldPct)
	lots[0].BenchmarkPct = &benchThen
	withBench, err := SplitBillPnL(lots, 99.40, today, 365, maturity, &benchNow)
	if err != nil {
		t.Fatal(err)
	}
	if withBench.Spread == nil {
		t.Fatal("spread missing with a benchmark on every lot")
	}
	near(t, "spread zero", *withBench.Spread, 0, 1e-6)
	near(t, "bench sum", withBench.Carry+withBench.Rates+*withBench.Spread, withBench.Total, 1e-9)
	near(t, "bench carry", withBench.Carry, split.Carry, 1e-9)

	// A benchmark that did not move leaves the whole yield effect as spread.
	flat := 2.0
	allSpread, err := SplitBillPnL(lots, 99.40, today, 365, maturity, &flat)
	if err != nil {
		t.Fatal(err)
	}
	near(t, "all spread sum", allSpread.Carry+allSpread.Rates+*allSpread.Spread, allSpread.Total, 1e-9)
	near(t, "all spread rates", allSpread.Rates, 0, 1e-6)
	near(t, "all spread", *allSpread.Spread, split.Rates, 1e-6)

	// A benchmark record that postdates the purchase names the line's own
	// yield that day as the reference: the move before it is rates, and an
	// unchanged relation since leaves a zero spread part.
	recordDay := BondLot{Face: 50000, PricePer100: 99.18, Date: bought, BenchmarkPct: &flat, ReferenceYieldPct: &ownNow}
	sinceRecord, err := SplitBillPnL([]BondLot{recordDay}, 99.40, today, 365, maturity, &flat)
	if err != nil {
		t.Fatal(err)
	}
	near(t, "since-record spread", *sinceRecord.Spread, 0, 1e-6)
	near(t, "since-record rates", sinceRecord.Rates, split.Rates, 1e-6)
	near(t, "since-record sum", sinceRecord.Carry+sinceRecord.Rates+*sinceRecord.Spread, sinceRecord.Total, 1e-9)

	// Two lots, one without a benchmark on record: the spread part is not
	// stated and the yield effect is all rates.
	two := append(lots, BondLot{Face: 20000, PricePer100: 99.30, Date: maturity.AddDate(0, 0, -90)})
	mixed, err := SplitBillPnL(two, 99.40, today, 365, maturity, &benchNow)
	if err != nil {
		t.Fatal(err)
	}
	if mixed.Spread != nil {
		t.Fatalf("spread stated with a lot off record: %+v", mixed)
	}
	near(t, "two lots total", mixed.Total, 110+20, 1e-9)
	near(t, "two lots sum", mixed.Carry+mixed.Rates, mixed.Total, 1e-9)
	if _, err := SplitBillPnL(nil, 99.4, today, 365, maturity, nil); err == nil {
		t.Fatal("no lot accepted")
	}
}

// A coupon bond's clean price at its own yield comes back to the price it
// was measured at; held at an unchanged yield the whole clean gain is
// carry; a yield rise makes rates negative; the parts sum.
func TestSplitBondPnLSums(t *testing.T) {
	asOf := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
	maturity := asOf.AddDate(7, 0, 0)
	bought := asOf.AddDate(0, -6, 0)
	m, err := MeasureBond(98.5, 4, maturity, bought, 2)
	if err != nil {
		t.Fatal(err)
	}
	back, err := BondCleanPriceAtYield(m.YieldPct, 4, maturity, bought, 2)
	if err != nil {
		t.Fatal(err)
	}
	near(t, "round trip", back, 98.5, 1e-6)

	lots := []BondLot{{Face: 10000, PricePer100: 98.5, Date: bought}}
	unchanged, err := BondCleanPriceAtYield(m.YieldPct, 4, maturity, asOf, 2)
	if err != nil {
		t.Fatal(err)
	}
	same, err := SplitBondPnL(lots, unchanged, 4, maturity, asOf, 2)
	if err != nil {
		t.Fatal(err)
	}
	near(t, "unchanged rates", same.Rates, 0, 1e-6)
	near(t, "unchanged sum", same.Carry+same.Rates, same.Total, 1e-9)

	higher, err := SplitBondPnL(lots, unchanged-2, 4, maturity, asOf, 2)
	if err != nil {
		t.Fatal(err)
	}
	if higher.Rates >= 0 || higher.Spread != nil {
		t.Fatalf("yield rise = %+v", higher)
	}
	near(t, "rise total", higher.Total, 10000*(unchanged-2-98.5)/100, 1e-9)
	near(t, "rise sum", higher.Carry+higher.Rates, higher.Total, 1e-9)
	near(t, "rise carry", higher.Carry, same.Carry, 1e-9)
}
