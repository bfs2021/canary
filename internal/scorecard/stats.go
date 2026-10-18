package scorecard

import (
	"math"
	"math/rand/v2"
	"sort"
)

// fit is an OLS regression of y on x with an intercept. ok is false below
// three pairs or when x has no variance; totalSD and the sample size stand
// regardless.
type fit struct {
	beta float64
	// residualSD is the residual standard deviation (n−2 degrees of
	// freedom); totalSD is y's own sample standard deviation (n−1).
	residualSD, totalSD float64
	// rSquared is 1 − SSE/SST; 0 when y has no variance.
	rSquared float64
	n        int
	ok       bool
}

func ols(x, y []float64) fit {
	n := len(x)
	if len(y) != n || n < 2 {
		return fit{n: n}
	}
	var sumX, sumY float64
	for i := range n {
		sumX += x[i]
		sumY += y[i]
	}
	meanX, meanY := sumX/float64(n), sumY/float64(n)
	var sxx, sxy, syy float64
	for i := range n {
		dx, dy := x[i]-meanX, y[i]-meanY
		sxx += dx * dx
		sxy += dx * dy
		syy += dy * dy
	}
	out := fit{n: n, totalSD: math.Sqrt(syy / float64(n-1))}
	if n < 3 || sxx <= 0 {
		return out
	}
	out.beta = sxy / sxx
	alpha := meanY - out.beta*meanX
	var sse float64
	for i := range n {
		e := y[i] - alpha - out.beta*x[i]
		sse += e * e
	}
	out.residualSD = math.Sqrt(sse / float64(n-2))
	if syy > 0 {
		out.rSquared = max(0, 1-sse/syy)
	}
	out.ok = true
	return out
}

// fit2 is an OLS regression of y on two regressors with an intercept: the
// market and the name's sector fund. ok is false below four observations or
// when the regressors are collinear (the sector fund is the market, or
// either did not move).
type fit2 struct {
	beta1, beta2 float64
	// residualSD has n−3 degrees of freedom.
	residualSD float64
	n          int
	ok         bool
}

func ols2(x1, x2, y []float64) fit2 {
	n := len(y)
	if len(x1) != n || len(x2) != n || n < 4 {
		return fit2{n: n}
	}
	var m1, m2, my float64
	for i := range n {
		m1 += x1[i]
		m2 += x2[i]
		my += y[i]
	}
	m1, m2, my = m1/float64(n), m2/float64(n), my/float64(n)
	var s11, s22, s12, s1y, s2y float64
	for i := range n {
		d1, d2, dy := x1[i]-m1, x2[i]-m2, y[i]-my
		s11 += d1 * d1
		s22 += d2 * d2
		s12 += d1 * d2
		s1y += d1 * dy
		s2y += d2 * dy
	}
	det := s11*s22 - s12*s12
	// Collinear regressors leave the two-factor fit undefined: a relative
	// determinant below 1e-9 of its scale is treated as singular.
	if s11 <= 0 || s22 <= 0 || det <= 1e-9*s11*s22 {
		return fit2{n: n}
	}
	out := fit2{n: n, ok: true}
	out.beta1 = (s22*s1y - s12*s2y) / det
	out.beta2 = (s11*s2y - s12*s1y) / det
	alpha := my - out.beta1*m1 - out.beta2*m2
	var sse float64
	for i := range n {
		e := y[i] - alpha - out.beta1*x1[i] - out.beta2*x2[i]
		sse += e * e
	}
	out.residualSD = math.Sqrt(sse / float64(n-3))
	return out
}

func mean(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	total := 0.0
	for _, v := range values {
		total += v
	}
	return total / float64(len(values))
}

func median(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	middle := len(sorted) / 2
	if len(sorted)%2 == 0 {
		return (sorted[middle-1] + sorted[middle]) / 2
	}
	return sorted[middle]
}

// bootstrapMeanCI returns the 95% percentile bootstrap interval of the mean
// from a seeded generator, so the same evidence yields the same interval.
func bootstrapMeanCI(values []float64, resamples int) (low, high float64) {
	n := len(values)
	if n == 0 {
		return 0, 0
	}
	if n == 1 {
		return values[0], values[0]
	}
	rng := rand.New(rand.NewPCG(0x5ca1ab1e, uint64(n)))
	means := make([]float64, 0, resamples)
	for range resamples {
		total := 0.0
		for range n {
			total += values[rng.IntN(n)]
		}
		means = append(means, total/float64(n))
	}
	sort.Float64s(means)
	at := func(p float64) float64 {
		i := int(math.Round(p * float64(len(means)-1)))
		return means[max(0, min(len(means)-1, i))]
	}
	return at(0.025), at(0.975)
}

// The sequential statistics take the effective sample: nEff independent
// unit-variance units and their standardised running sum sEff, as
// EffectiveSample derives them from the name-week units and their overlap
// covariance. Without overlap nEff is the unit count and sEff the plain sum.

// PosteriorScore returns the posterior mean and standard deviation of the
// directional score (the mean z per unit) after an effective sample, under
// the prior N(0, PriorSD²).
func PosteriorScore(nEff, sEff float64) (mean, sd float64) {
	precision := 1/(PriorSD*PriorSD) + nEff
	return sEff / precision, 1 / math.Sqrt(precision)
}

// PosteriorAbove is P(score > c) under that posterior.
func PosteriorAbove(nEff, sEff, c float64) float64 {
	mean, sd := PosteriorScore(nEff, sEff)
	return 1 - normCDF((c-mean)/sd)
}

// EValue is the mixture-SPRT e-value for information in the decisions
// (score > 0 against score = 0) after an effective sample, reported only
// while the sum is positive; otherwise 0 and against is true.
func EValue(nEff, sEff float64) (e float64, against bool) {
	if nEff <= 0 || sEff <= 0 {
		return 0, true
	}
	scale := 1 + nEff*Tau*Tau
	return math.Exp(Tau*Tau*sEff*sEff/(2*scale)) / math.Sqrt(scale), false
}

// FutilityEValue is the likelihood ratio of no information (score = 0)
// against the smallest score worth a risk allocation (FutilityScore), after
// an effective sample. It is a test martingale under a true score of
// FutilityScore or more, so a sleeve with that much skill is called futile
// with probability at most 1/FutileEValue, whenever the reading stops.
func FutilityEValue(nEff, sEff float64) float64 {
	if nEff <= 0 {
		return 0
	}
	return math.Exp(-FutilityScore*sEff + FutilityScore*FutilityScore*nEff/2)
}

// UnitsToEstablish projects the effective unit count at which the e-value
// gate passes if every further unit adds meanScore; ok is false when the
// score is not positive or the count exceeds 100,000.
func UnitsToEstablish(meanScore float64) (int, bool) {
	if meanScore <= 0 || math.IsNaN(meanScore) {
		return 0, false
	}
	for n := 1; n <= 100000; n++ {
		if e, _ := EValue(float64(n), meanScore*float64(n)); e >= EstablishedEValue {
			return n, true
		}
	}
	return 0, false
}

// Holm returns Holm step-down adjusted p-values in the input order.
func Holm(p []float64) []float64 {
	n := len(p)
	out := make([]float64, n)
	if n == 0 {
		return out
	}
	order := make([]int, n)
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return p[order[a]] < p[order[b]] })
	running := 0.0
	for rank, idx := range order {
		adjusted := math.Min(1, float64(n-rank)*p[idx])
		running = math.Max(running, adjusted)
		out[idx] = running
	}
	return out
}
