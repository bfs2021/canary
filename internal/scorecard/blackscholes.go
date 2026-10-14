package scorecard

import "math"

// normCDF is the standard normal cumulative distribution function.
func normCDF(x float64) float64 { return 0.5 * math.Erfc(-x/math.Sqrt2) }

func normPDF(x float64) float64 { return math.Exp(-0.5*x*x) / math.Sqrt(2*math.Pi) }

func bsD1(spot, strike, t, vol, r, q float64) float64 {
	return (math.Log(spot/strike) + (r-q+0.5*vol*vol)*t) / (vol * math.Sqrt(t))
}

// Price is the Black-Scholes price of a European option per share.
func Price(spot, strike, t, vol, r, q float64, call bool) float64 {
	if spot <= 0 || strike <= 0 || t <= 0 || vol <= 0 {
		return 0
	}
	d1 := bsD1(spot, strike, t, vol, r, q)
	d2 := d1 - vol*math.Sqrt(t)
	if call {
		return spot*math.Exp(-q*t)*normCDF(d1) - strike*math.Exp(-r*t)*normCDF(d2)
	}
	return strike*math.Exp(-r*t)*normCDF(-d2) - spot*math.Exp(-q*t)*normCDF(-d1)
}

// Delta is the Black-Scholes delta per share: positive for a call, negative
// for a put.
func Delta(spot, strike, t, vol, r, q float64, call bool) float64 {
	if spot <= 0 || strike <= 0 || t <= 0 || vol <= 0 {
		return 0
	}
	d1 := bsD1(spot, strike, t, vol, r, q)
	if call {
		return math.Exp(-q*t) * normCDF(d1)
	}
	return math.Exp(-q*t) * (normCDF(d1) - 1)
}

func vega(spot, strike, t, vol, r, q float64) float64 {
	if spot <= 0 || strike <= 0 || t <= 0 || vol <= 0 {
		return 0
	}
	return spot * math.Exp(-q*t) * normPDF(bsD1(spot, strike, t, vol, r, q)) * math.Sqrt(t)
}

// ImpliedVol inverts the Black-Scholes price for the volatility that
// reproduces price. It returns 0 when there is no solution: a price at or
// below intrinsic, no time to expiry, or a volatility outside [1%, 500%].
// Newton steps run from the Brenner-Subrahmanyam guess and fall back to
// bisection on a bracket, so a far-from-the-money fill still converges.
func ImpliedVol(price, spot, strike, t, r, q float64, call bool) float64 {
	const (
		minVol, maxVol = 0.01, 5.0
		tolerance      = 1e-7
	)
	if price <= 0 || spot <= 0 || strike <= 0 || t <= 0 {
		return 0
	}
	intrinsic := math.Max(0, spot*math.Exp(-q*t)-strike*math.Exp(-r*t))
	if !call {
		intrinsic = math.Max(0, strike*math.Exp(-r*t)-spot*math.Exp(-q*t))
	}
	if price <= intrinsic {
		return 0
	}
	if price >= Price(spot, strike, t, maxVol, r, q, call) || price <= Price(spot, strike, t, minVol, r, q, call) {
		return 0
	}
	vol := math.Sqrt(2*math.Pi/t) * (price / spot)
	if math.IsNaN(vol) || vol < 0.1 || vol > 2 {
		vol = 0.3
	}
	lo, hi := minVol, maxVol
	for range 100 {
		model := Price(spot, strike, t, vol, r, q, call)
		diff := model - price
		if math.Abs(diff) < tolerance {
			if vol < minVol || vol > maxVol {
				return 0
			}
			return vol
		}
		if diff > 0 {
			hi = math.Min(hi, vol)
		} else {
			lo = math.Max(lo, vol)
		}
		v := vega(spot, strike, t, vol, r, q)
		next := vol - diff/v
		if v < 1e-10 || math.IsNaN(next) || next <= lo || next >= hi {
			next = 0.5 * (lo + hi)
		}
		vol = next
	}
	if math.Abs(Price(spot, strike, t, vol, r, q, call)-price) < 1e-4 && vol >= minVol && vol <= maxVol {
		return vol
	}
	return 0
}
