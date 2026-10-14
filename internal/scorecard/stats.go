package scorecard

import (
	"math"
	"math/rand/v2"
	"sort"
)

// ols regresses y on x with an intercept and returns the slope and the
// residual standard deviation (n−2 degrees of freedom). ok is false below
// three pairs or with a degenerate regressor.
func ols(x, y []float64) (beta, residualSD float64, ok bool) {
	n := len(x)
	if n < 3 || len(y) != n {
		return 0, 0, false
	}
	var sumX, sumY float64
	for i := range n {
		sumX += x[i]
		sumY += y[i]
	}
	meanX, meanY := sumX/float64(n), sumY/float64(n)
	var sxx, sxy float64
	for i := range n {
		dx := x[i] - meanX
		sxx += dx * dx
		sxy += dx * (y[i] - meanY)
	}
	if sxx <= 0 {
		return 0, 0, false
	}
	beta = sxy / sxx
	alpha := meanY - beta*meanX
	var sse float64
	for i := range n {
		e := y[i] - alpha - beta*x[i]
		sse += e * e
	}
	residualSD = math.Sqrt(sse / float64(n-2))
	return beta, residualSD, true
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

// PosteriorIC returns the posterior mean and standard deviation of IC after n
// cluster means summing to s, under the prior N(0, PriorSD²) and unit
// variance per cluster mean.
func PosteriorIC(n int, s float64) (mean, sd float64) {
	precision := 1/(PriorSD*PriorSD) + float64(n)
	return s / precision, 1 / math.Sqrt(precision)
}

// PosteriorAbove is P(IC > c) under that posterior.
func PosteriorAbove(n int, s, c float64) float64 {
	mean, sd := PosteriorIC(n, s)
	return 1 - normCDF((c-mean)/sd)
}

// EValue is the mixture-SPRT e-value after n clusters with running sum s,
// reported only while s is positive; otherwise 0 and against is true.
func EValue(n int, s float64) (e float64, against bool) {
	if n <= 0 || s <= 0 {
		return 0, true
	}
	scale := 1 + float64(n)*Tau*Tau
	return math.Exp(Tau*Tau*s*s/(2*scale)) / math.Sqrt(scale), false
}

// ClustersToEstablish projects the cluster count at which the e-value gate
// passes if every further cluster mean equals meanZ; ok is false when meanZ
// is not positive or the count exceeds 100,000.
func ClustersToEstablish(meanZ float64) (int, bool) {
	if meanZ <= 0 || math.IsNaN(meanZ) {
		return 0, false
	}
	for n := 1; n <= 100000; n++ {
		if e, _ := EValue(n, meanZ*float64(n)); e >= EstablishedEValue {
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
