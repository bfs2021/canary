package scorecard

import (
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"sort"
	"strconv"
	"testing"
)

// The power analysis behind decision-scorecard.md ("Power"): synthetic
// selectors with a true directional score of 0, 0.05 and 0.10, trading k
// names a week drawn from a pool, their horizons overlapping as the real
// ones do. Each path runs the statistical bar of a gate week by week and
// records the first week it reads established or futile. Consistency gates
// (halves, markets, placebo) are left out, so the times are lower bounds
// for established; the kill rates are exact for the statistical rule.
//
// The table is printed with SCORECARD_POWER_PATHS=N (N paths per cell); the
// assertions below run on a small seeded sample.

// gateRule is one sequential rule under test.
type gateRule struct {
	name string
	// established reads the bar from an effective sample; futile the kill
	// rule. todayGate is the gate shipped on 2026-10-14 for comparison: one
	// unit per week through its mean, variance one, E ≥ 100, n ≥ 100,
	// P(score > 0.05) ≥ 0.95; futile at n ≥ 60 and P(score > 0.10) < 0.05.
	established func(nEff, sEff float64, weeks int) bool
	futile      func(nEff, sEff float64) bool
}

var todayGate = gateRule{
	name: "today (week means, E≥100, n≥100, P(>0.05)≥0.95)",
	established: func(n, s float64, _ int) bool {
		e, _ := EValue(n, s)
		return e >= 100 && n >= 100 && PosteriorAbove(n, s, 0.05) >= 0.95
	},
	futile: func(n, s float64) bool { return n >= 60 && PosteriorAbove(n, s, 0.10) < 0.05 },
}

var proposedGate = gateRule{
	name: "proposed (name-weeks, overlap, E≥20, n_eff≥60, 26 weeks, P(>0)≥0.975; futile LR≥20 vs 0.05)",
	established: func(n, s float64, weeks int) bool {
		e, _ := EValue(n, s)
		return e >= EstablishedEValue && n >= EstablishedUnits && weeks >= EstablishedWeeks && PosteriorAbove(n, s, 0) >= EstablishedPosterior
	},
	futile: func(n, s float64) bool { return n >= FutileUnits && FutilityEValue(n, s) >= FutileEValue },
}

// pathOutcome is the first week a path read established or futile; 0 when
// it never did within the horizon.
type pathOutcome struct{ established, futile int }

// simulatePath draws k name-week units a week from a rotating pool of names
// and reads both rules on the same draws. Each unit's z is the standardised
// sum of the h daily residual returns after its entry session; the sessions
// of overlapping units are shared, so the same name a week later shares
// half its path at h = 10 and two names in one week share the common part
// of each session's return at correlation 0.1. That is exactly the null
// covariance the proposed rule assumes (EffectiveSample), so the test is of
// the rule's validity under its own model; the placebo guards the rest.
func simulatePath(score float64, namesPerWeek, pool, maxWeeks, h int, rng *rand.Rand) (today, proposed pathOutcome) {
	common := map[int]float64{}
	own := map[[2]int]float64{}
	draw := func(name, session int) float64 {
		m, ok := common[session]
		if !ok {
			m = rng.NormFloat64()
			common[session] = m
		}
		key := [2]int{name, session}
		o, ok := own[key]
		if !ok {
			o = rng.NormFloat64()
			own[key] = o
		}
		return math.Sqrt(CrossNameResidualCorrelation)*m + math.Sqrt(1-CrossNameResidualCorrelation)*o
	}
	span := float64(h) * 7 / 5
	type placed struct {
		week int
		name int
	}
	var units []placed
	sum, v := 0.0, 0.0
	weekSum, weeks := 0.0, 0.0
	for week := 1; week <= maxWeeks; week++ {
		names := weekNames(week, namesPerWeek, pool)
		meanZ := 0.0
		for _, name := range names {
			z := score
			for d := 1; d <= h; d++ {
				z += draw(name, 5*week+d) / math.Sqrt(float64(h))
			}
			meanZ += z / float64(namesPerWeek)
			sum += z
			v++
			for _, other := range units {
				gap := float64(week-other.week) * 7
				if gap >= span {
					continue
				}
				rho := CrossNameResidualCorrelation
				if other.name == name {
					rho = 1
				}
				v += 2 * rho * (1 - gap/span)
			}
			units = append(units, placed{week: week, name: name})
		}
		cut := 0
		for cut < len(units) && float64(week-units[cut].week)*7 >= span {
			cut++
		}
		units = units[cut:]
		for session := range common {
			if session < 5*week-2*h {
				delete(common, session)
			}
		}
		for key := range own {
			if key[1] < 5*week-2*h {
				delete(own, key)
			}
		}
		// Today's rule: the week's mean is one unit of assumed variance 1.
		weeks++
		weekSum += meanZ
		if today.established == 0 && todayGate.established(weeks, weekSum, week) {
			today.established = week
		}
		if today.futile == 0 && todayGate.futile(weeks, weekSum) {
			today.futile = week
		}
		n := float64(week * namesPerWeek)
		nEff, sEff := n*n/v, sum*n/v
		if proposed.established == 0 && proposedGate.established(nEff, sEff, week) {
			proposed.established = week
		}
		if proposed.futile == 0 && proposedGate.futile(nEff, sEff) {
			proposed.futile = week
		}
		if today.established != 0 && today.futile != 0 && proposed.established != 0 && proposed.futile != 0 {
			break
		}
	}
	return today, proposed
}

// weekNames rotates the pool: week w trades names (w·k … w·k+k−1) mod pool.
func weekNames(week, k, pool int) []int {
	out := make([]int, k)
	for i := range k {
		out[i] = (week*k + i) % pool
	}
	return out
}

type cell struct {
	score              float64
	namesPerWeek, pool int
	establishedFirst   int
	futileFirst        int
	paths              int
	establishedWeeks   []int
}

func runCells(score float64, k, pool, paths, maxWeeks int) (today, proposed cell) {
	today = cell{score: score, namesPerWeek: k, pool: pool, paths: paths}
	proposed = today
	record := func(c *cell, o pathOutcome) {
		switch {
		case o.established != 0 && (o.futile == 0 || o.established < o.futile):
			c.establishedFirst++
			c.establishedWeeks = append(c.establishedWeeks, o.established)
		case o.futile != 0:
			c.futileFirst++
		}
	}
	for p := range paths {
		rng := rand.New(rand.NewPCG(uint64(p)+1, uint64(k*1000+int(score*1000))))
		t, pr := simulatePath(score, k, pool, maxWeeks, PrimaryHorizon, rng)
		record(&today, t)
		record(&proposed, pr)
	}
	sort.Ints(today.establishedWeeks)
	sort.Ints(proposed.establishedWeeks)
	return today, proposed
}

func (c cell) quantile(q float64) string {
	// Quantile over all paths, unreached paths counted as beyond the horizon.
	idx := int(q * float64(c.paths))
	if idx >= len(c.establishedWeeks) {
		return "—"
	}
	return fmt.Sprintf("%.1f y", float64(c.establishedWeeks[idx])/52)
}

// TestGatePowerAnalysis prints the power table with SCORECARD_POWER_PATHS
// set and, on a small seeded sample, checks the claims the design record
// makes: the proposed gate kills a score of 0.05 or 0.10 far less often than
// today's and reads a score of 0.10 at six rotating names a week within
// seven years on most paths, while zero skill is not established.
func TestGatePowerAnalysis(t *testing.T) {
	t.Parallel()
	paths, maxWeeks := 24, 20*52
	if raw := os.Getenv("SCORECARD_POWER_PATHS"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			paths = n
		}
	}
	type row struct {
		score float64
		k     int
		pool  int
	}
	rows := []row{{0, 1, 1}, {0, 6, 30}, {0.05, 1, 1}, {0.05, 6, 30}, {0.05, 12, 60}, {0.10, 1, 1}, {0.10, 6, 6}, {0.10, 6, 30}, {0.10, 12, 60}}
	today, proposed := map[row]cell{}, map[row]cell{}
	for _, r := range rows {
		today[r], proposed[r] = runCells(r.score, r.k, r.pool, paths, maxWeeks)
	}
	if os.Getenv("SCORECARD_POWER_PATHS") != "" {
		for _, rule := range []struct {
			name  string
			cells map[row]cell
		}{{todayGate.name, today}, {proposedGate.name, proposed}} {
			t.Logf("%s · %d paths · 20-year horizon", rule.name, paths)
			t.Logf("| score | names/week (pool) | established (median / 80th) | established first | futile first (true edge killed) |")
			for _, r := range rows {
				c := rule.cells[r]
				t.Logf("| %.2f | %d (%d) | %s / %s | %d%% | %d%% |", r.score, r.k, r.pool, c.quantile(0.5), c.quantile(0.8), 100*c.establishedFirst/paths, 100*c.futileFirst/paths)
			}
		}
	}
	for _, r := range rows {
		if r.score == 0 && proposed[r].establishedFirst > paths/10 {
			t.Fatalf("zero skill established on %d of %d paths (%+v)", proposed[r].establishedFirst, paths, r)
		}
		if r.score > 0 && proposed[r].futileFirst > paths/8 {
			t.Fatalf("a true score of %.2f was killed on %d of %d paths (%+v)", r.score, proposed[r].futileFirst, paths, r)
		}
	}
	six := proposed[row{0.10, 6, 30}]
	if six.establishedFirst < paths*3/4 || six.quantile(0.5) == "—" || six.establishedWeeks[paths/2] > 7*52 {
		t.Fatalf("a score of 0.10 on six rotating names a week: %d of %d established, median %s", six.establishedFirst, paths, six.quantile(0.5))
	}
	if slow := today[row{0.10, 6, 30}]; slow.establishedFirst > six.establishedFirst {
		t.Fatalf("today's gate established more paths (%d) than the proposed one (%d)", slow.establishedFirst, six.establishedFirst)
	}
}
