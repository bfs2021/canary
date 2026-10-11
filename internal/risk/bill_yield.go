package risk

import (
	"errors"
	"math"
	"strings"
	"time"
)

// Bill facts (internal-docs/design/bond-risk.md, "Bill and bond facts"):
// the simple money-market yield of a zero-coupon bill from its price per 100
// of face and its days to maturity, the price at a yield, and the split of a
// bill's gain since purchase into carry, rates and spread. Pure arithmetic:
// the daemon supplies prices, dates and benchmarks and says where they came
// from.

// Bill yield bases. A bill's yield is the simple annualised return from its
// price to par: (100 / price − 1) × basis / days. Bubills and BTFs trade on
// actual/360; US, UK and Canadian bills are quoted on actual/365. Coupon
// bonds are measured by MeasureBond instead and carry a BondYieldBasis.
const (
	BillYieldBasisAct360 = "simple_act360"
	BillYieldBasisAct365 = "simple_act365"
	// BondYieldBasisAnnual and BondYieldBasisSemiannual name MeasureBond's
	// yield to maturity, compounded at the coupon frequency (annual in EUR,
	// semi-annual elsewhere), time in days over 365.25.
	BondYieldBasisAnnual     = "ytm_annual"
	BondYieldBasisSemiannual = "ytm_semiannual"
)

// BillYieldBasis is the simple-yield convention of a currency's bills and
// its day-count denominator.
func BillYieldBasis(currency string) (basis string, dayCount float64) {
	if strings.EqualFold(strings.TrimSpace(currency), "EUR") {
		return BillYieldBasisAct360, 360
	}
	return BillYieldBasisAct365, 365
}

// BondYieldBasis names MeasureBond's convention for a currency.
func BondYieldBasis(currency string) string {
	if BondCouponsPerYear(currency) == 1 {
		return BondYieldBasisAnnual
	}
	return BondYieldBasisSemiannual
}

// BillYieldPct is a bill's simple annualised yield in percent at a price per
// 100 of face with days to maturity on a day-count basis.
func BillYieldPct(pricePer100 float64, days int, dayCount float64) (float64, error) {
	switch {
	case !(pricePer100 > 0) || math.IsInf(pricePer100, 0):
		return 0, errors.New("the price is not positive")
	case days <= 0:
		return 0, errors.New("the bill has matured")
	case !(dayCount > 0):
		return 0, errors.New("the day count is not positive")
	}
	return (100/pricePer100 - 1) * dayCount / float64(days) * 100, nil
}

// BillPricePer100 is the price per 100 of face at a simple yield in percent
// with days to maturity on a day-count basis.
func BillPricePer100(yieldPct float64, days int, dayCount float64) float64 {
	return 100 / (1 + yieldPct/100*float64(days)/dayCount)
}

// BillScenario is a bill's value change, in its currency, for a parallel
// move of bp basis points in its yield: face × (P(y + bp) − P(y)) / 100,
// negative on a rise. A model estimate from the price, not a quote.
func BillScenario(face, yieldPct float64, days int, dayCount, bp float64) float64 {
	return face * (BillPricePer100(yieldPct+bp/100, days, dayCount) - BillPricePer100(yieldPct, days, dayCount)) / 100
}

// BondLot is one purchase of a bill or bond: the face bought, the clean price
// per 100 paid, the purchase date, and, for the spread part, the benchmark's
// yield on a reference date (nil when none is on record). The reference is
// the purchase itself unless ReferenceYieldPct names the line's own yield on
// a later record date: the yield move before that date then counts as rates
// and only the move against the benchmark since it as spread.
type BondLot struct {
	Face              float64
	PricePer100       float64
	Date              time.Time
	BenchmarkPct      *float64
	ReferenceYieldPct *float64
}

// BondPnLSplit is a line's clean-price gain since purchase, in its currency,
// split into the parts that sum to it:
//
//   - Carry: the price moving along each lot's purchase-yield path to today
//     (pull to par for a bill, the clean-price roll for a bond; coupon income
//     is not included).
//   - Rates: the price effect of the benchmark yield's change since each lot
//     was bought, at today's cash flows. Without a benchmark it is the whole
//     yield effect and Spread is nil.
//   - Spread: the price effect of the line's yield moving against the
//     benchmark since the lot was bought; nil when no benchmark change is on
//     record for a lot.
type BondPnLSplit struct {
	Total  float64
	Carry  float64
	Rates  float64
	Spread *float64
	// PurchaseYieldPct is the face-weighted yield the lots were bought at.
	PurchaseYieldPct float64
}

// SplitBillPnL splits a bill's clean gain since purchase at today's price
// per 100 and days to maturity on a day-count basis. Each lot's purchase
// yield is read from its price and its days to maturity on its date; the
// spread part needs the benchmark's yield now and on every lot's date.
func SplitBillPnL(lots []BondLot, pricePer100 float64, days int, dayCount float64, maturity time.Time, benchmarkNowPct *float64) (BondPnLSplit, error) {
	if len(lots) == 0 {
		return BondPnLSplit{}, errors.New("no purchase lot")
	}
	if _, err := BillYieldPct(pricePer100, days, dayCount); err != nil {
		return BondPnLSplit{}, err
	}
	var out BondPnLSplit
	spread, spreadKnown, face := 0.0, benchmarkNowPct != nil, 0.0
	for _, lot := range lots {
		lotDays := daysBetween(lot.Date, maturity)
		y0, err := BillYieldPct(lot.PricePer100, lotDays, dayCount)
		if err != nil {
			return BondPnLSplit{}, err
		}
		face += lot.Face
		out.PurchaseYieldPct += y0 * lot.Face
		carryPrice := BillPricePer100(y0, days, dayCount)
		out.Carry += lot.Face * (carryPrice - lot.PricePer100) / 100
		out.Total += lot.Face * (pricePer100 - lot.PricePer100) / 100
		if !spreadKnown || lot.BenchmarkPct == nil {
			spreadKnown = false
			out.Rates += lot.Face * (pricePer100 - carryPrice) / 100
			continue
		}
		// The yield move counted as rates: the benchmark's change since the
		// reference date, plus the line's own move from purchase to that
		// date when the reference postdates the purchase.
		ratesMove := *benchmarkNowPct - *lot.BenchmarkPct
		if lot.ReferenceYieldPct != nil {
			ratesMove += *lot.ReferenceYieldPct - y0
		}
		ratesPrice := BillPricePer100(y0+ratesMove, days, dayCount)
		out.Rates += lot.Face * (ratesPrice - carryPrice) / 100
		spread += lot.Face * (pricePer100 - ratesPrice) / 100
	}
	if face > 0 {
		out.PurchaseYieldPct /= face
	}
	if spreadKnown {
		out.Spread = &spread
	} else {
		// Without a benchmark on every lot, the yield effect is all rates.
		out.Rates = out.Total - out.Carry
	}
	return out, nil
}

// SplitBondPnL splits a coupon bond's clean-price gain since purchase into
// carry (the clean price rolling along each lot's purchase yield to today)
// and rates (the rest of the yield effect); coupon income is not included
// and no benchmark is read, so Spread stays nil.
func SplitBondPnL(lots []BondLot, cleanPer100, couponPct float64, maturity, asOf time.Time, couponsPerYear int) (BondPnLSplit, error) {
	if len(lots) == 0 {
		return BondPnLSplit{}, errors.New("no purchase lot")
	}
	var out BondPnLSplit
	face := 0.0
	for _, lot := range lots {
		bought, err := MeasureBond(lot.PricePer100, couponPct, maturity, lot.Date, couponsPerYear)
		if err != nil {
			return BondPnLSplit{}, err
		}
		carryPrice, err := BondCleanPriceAtYield(bought.YieldPct, couponPct, maturity, asOf, couponsPerYear)
		if err != nil {
			return BondPnLSplit{}, err
		}
		face += lot.Face
		out.PurchaseYieldPct += bought.YieldPct * lot.Face
		out.Carry += lot.Face * (carryPrice - lot.PricePer100) / 100
		out.Total += lot.Face * (cleanPer100 - lot.PricePer100) / 100
	}
	if face > 0 {
		out.PurchaseYieldPct /= face
	}
	out.Rates = out.Total - out.Carry
	return out, nil
}

// BondCleanPriceAtYield is the clean price per 100 of a coupon bond at a
// yield to maturity in percent, on MeasureBond's conventions.
func BondCleanPriceAtYield(yieldPct, couponPct float64, maturity, asOf time.Time, couponsPerYear int) (float64, error) {
	// Measure at par only to build the cash flows and accrued interest;
	// the price at the yield follows from them.
	m, err := MeasureBond(100, couponPct, maturity, asOf, couponsPerYear)
	if err != nil {
		return 0, err
	}
	return m.price(yieldPct/100) - m.AccruedPer100, nil
}

// daysBetween counts calendar days from one UTC day to another, rounding
// up so a maturity later in the day still counts.
func daysBetween(from, to time.Time) int {
	day := func(t time.Time) time.Time {
		y, m, d := t.UTC().Date()
		return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	}
	return int(math.Ceil(day(to).Sub(day(from)).Hours() / 24))
}
