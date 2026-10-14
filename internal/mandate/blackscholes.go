package mandate

import "math"

// Option pricing for the revaluation: European Black-Scholes with the rate
// and the dividend yield at zero, which is what the daemon's gamma model
// assumes too. Over a one-day horizon neither moves a price enough to matter
// next to the volatility, and the mandate states the simplification.

// normCDF is the standard-normal cumulative distribution function.
func normCDF(x float64) float64 { return 0.5 * math.Erfc(-x/math.Sqrt2) }

// OptionPrice is the Black-Scholes value per share of one call or put at
// spot S, strike K, t years to expiry and volatility vol (decimal). At or
// past expiry it is the intrinsic value. Non-positive inputs price at
// intrinsic as well, so a degenerate leg never prices negative.
func OptionPrice(isCall bool, spot, strike, t, vol float64) float64 {
	if spot <= 0 || strike <= 0 {
		return 0
	}
	if t <= 0 || vol <= 0 {
		return intrinsic(isCall, spot, strike)
	}
	sqrtT := math.Sqrt(t)
	d1 := (math.Log(spot/strike) + 0.5*vol*vol*t) / (vol * sqrtT)
	d2 := d1 - vol*sqrtT
	if isCall {
		return spot*normCDF(d1) - strike*normCDF(d2)
	}
	return strike*normCDF(-d2) - spot*normCDF(-d1)
}

func intrinsic(isCall bool, spot, strike float64) float64 {
	if isCall {
		return math.Max(0, spot-strike)
	}
	return math.Max(0, strike-spot)
}

// ImpliedVolatility back-solves the volatility that prices an option at
// price per share, by bisection on OptionPrice, which is monotone in vol.
// It reports false when no volatility in [1%, 500%] prices it: a price at
// or below intrinsic, above the no-arbitrage bound, or a leg at expiry.
func ImpliedVolatility(isCall bool, price, spot, strike, t float64) (float64, bool) {
	if price <= 0 || spot <= 0 || strike <= 0 || t <= 0 || math.IsNaN(price) || math.IsInf(price, 0) {
		return 0, false
	}
	const lo, hi = 0.01, 5.0
	if price <= intrinsic(isCall, spot, strike) || price < OptionPrice(isCall, spot, strike, t, lo) || price > OptionPrice(isCall, spot, strike, t, hi) {
		return 0, false
	}
	a, b := lo, hi
	for range 100 {
		mid := 0.5 * (a + b)
		if OptionPrice(isCall, spot, strike, t, mid) < price {
			a = mid
		} else {
			b = mid
		}
		if b-a < 1e-7 {
			break
		}
	}
	return 0.5 * (a + b), true
}
