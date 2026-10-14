package core

import (
	"encoding/json"
	"math"
	"math/rand/v2"
	"os"
	"slices"
	"strings"
	"testing"
)

// parityFixture is the lab's core-ibcf-v1 fixture, byte for byte the file
// `lab core-fixture` writes (lab tests/fixtures/core_ibcf_v1_parity.json).
type parityFixture struct {
	Rule     string    `json:"rule"`
	Window   int       `json:"window"`
	Calendar []string  `json:"calendar"`
	Dates    []string  `json:"dates"`
	Closes   []float64 `json:"closes"`
	Missing  string    `json:"missing_bar"`
	Sizing   struct {
		CoreVolPct     float64 `json:"core_vol_pct"`
		BudgetBaseRoom float64 `json:"budget_base_room"`
		MaxValuePct    float64 `json:"max_value_pct"`
	} `json:"sizing"`
	Decisions []parityDecision `json:"decisions"`
}

type parityDecision struct {
	Decision   string                `json:"decision"`
	Status     string                `json:"status"`
	Reason     string                `json:"reason"`
	WindowFrom string                `json:"window_from"`
	MonthEnds  []string              `json:"month_ends"`
	Close      float64               `json:"close"`
	SMA10      float64               `json:"sma10"`
	TrendOn    bool                  `json:"trend_on"`
	SigmaEWMA  float64               `json:"sigma_ewma"`
	Sigma252   float64               `json:"sigma_252"`
	Sigma      float64               `json:"sigma"`
	Binding    string                `json:"binding"`
	Sizing     map[string]paritySize `json:"sizing"`
}

type paritySize struct {
	SleeveBudget float64 `json:"sleeve_budget"`
	Cap          float64 `json:"cap"`
	Notional     float64 `json:"notional"`
	TargetShares int     `json:"target_shares"`
}

func loadParity(t *testing.T) parityFixture {
	t.Helper()
	raw, err := os.ReadFile("testdata/core_ibcf_v1_parity.json")
	if err != nil {
		t.Fatal(err)
	}
	var f parityFixture
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	if f.Rule != RuleID || f.Window != WindowCloses {
		t.Fatalf("fixture is for %s window %d, this is %s window %d: a new rule version needs a new fixture", f.Rule, f.Window, RuleID, WindowCloses)
	}
	return f
}

func relClose(a, b float64) bool {
	if a == b {
		return true
	}
	return math.Abs(a-b) <= 1e-9*math.Max(math.Abs(a), math.Abs(b))
}

// Parity with the lab (design §1, acceptance test 1): on the lab's
// synthetic fixture every decision's status, month-ends, trend, binding and
// target shares match exactly and every float to 1e-9 relative, across a
// calm stretch (the floor binds), a crash and a volatility spike (the EWMA,
// then the 252-day window), a holiday month-end, a missing bar (the months
// whose window holds it hold) and the months before the window fills (not
// live).
func TestParityWithTheLab(t *testing.T) {
	f := loadParity(t)
	seen := map[string]bool{}
	for _, want := range f.Decisions {
		sig, w := Measure(f.Calendar, f.Dates, f.Closes, want.Decision)
		if sig.Status != want.Status {
			t.Fatalf("%s: status %s (%s), lab %s (%s)", want.Decision, sig.Status, sig.Reason, want.Status, want.Reason)
		}
		seen[sig.Status] = true
		if sig.Status != StatusOK {
			if sig.Reason != want.Reason {
				t.Fatalf("%s: reason %q, lab %q", want.Decision, sig.Reason, want.Reason)
			}
			continue
		}
		seen[sig.Binding] = true
		seen[map[bool]string{true: "on", false: "off"}[sig.TrendOn]] = true
		if sig.WindowFrom != want.WindowFrom || !slices.Equal(sig.MonthEnds, want.MonthEnds) || sig.TrendOn != want.TrendOn || sig.Binding != want.Binding {
			t.Fatalf("%s: window %s month-ends %v trend %v binding %s; lab %s %v %v %s", want.Decision, sig.WindowFrom, sig.MonthEnds, sig.TrendOn, sig.Binding, want.WindowFrom, want.MonthEnds, want.TrendOn, want.Binding)
		}
		if len(w.Closes) != WindowCloses {
			t.Fatalf("%s: window of %d closes", want.Decision, len(w.Closes))
		}
		for _, c := range []struct {
			name      string
			got, want float64
		}{{"close", sig.Close, want.Close}, {"sma10", sig.SMA10, want.SMA10}, {"sigma_ewma", sig.SigmaEWMA, want.SigmaEWMA}, {"sigma_252", sig.Sigma252, want.Sigma252}, {"sigma", sig.Sigma, want.Sigma}} {
			if !relClose(c.got, c.want) {
				t.Fatalf("%s: %s %.17g, lab %.17g", want.Decision, c.name, c.got, c.want)
			}
		}
		for scale, ws := range want.Sizing {
			var s float64
			if err := json.Unmarshal([]byte(scale), &s); err != nil {
				t.Fatal(err)
			}
			got := SizeTarget(sig, Sizing{CoreVolPct: f.Sizing.CoreVolPct, ScalePct: s, MaxValuePct: f.Sizing.MaxValuePct, BudgetBase: f.Sizing.BudgetBaseRoom, Room: 1})
			if got.TargetShares != ws.TargetShares || !relClose(got.Notional, ws.Notional) || !relClose(got.SleeveBudget, ws.SleeveBudget) || !relClose(got.Cap, ws.Cap) {
				t.Fatalf("%s at scale %s: %+v, lab %+v", want.Decision, scale, got, ws)
			}
		}
	}
	for _, need := range []string{StatusOK, StatusHold, StatusNotLive, BindingEWMA, Binding252, BindingFloor, "on", "off"} {
		if !seen[need] {
			t.Fatalf("the fixture never exercises %q", need)
		}
	}
	// The holiday month-end (31 December closed) is decided on the 30th.
	if !slices.ContainsFunc(f.Decisions, func(d parityDecision) bool {
		return d.Decision == "2024-12-30" && d.Status == StatusOK
	}) {
		t.Fatal("the fixture lacks the holiday month-end 2024-12-30")
	}
}

// A running month, a mid-month session and a day that is not a session are
// never decided; a missing bar holds and is never filled.
func TestMeasureRefusesWhatIsNotAMonthEndDecision(t *testing.T) {
	f := loadParity(t)
	last := f.Calendar[len(f.Calendar)-1]
	if sig, _ := Measure(f.Calendar, f.Dates, f.Closes, last); sig.Status == StatusOK {
		t.Fatalf("the running month's last known session decided: %+v", sig)
	}
	if sig, _ := Measure(f.Calendar, f.Dates, f.Closes, "2024-06-15"); sig.Status != StatusHold || !strings.Contains(sig.Reason, "not a session") {
		t.Fatalf("a Saturday: %+v", sig)
	}
	if sig, _ := Measure(f.Calendar, f.Dates, f.Closes, "2024-06-14"); sig.Status != StatusHold || !strings.Contains(sig.Reason, "not the last session") {
		t.Fatalf("mid-month: %+v", sig)
	}
	dates := slices.Clone(f.Dates)
	closes := slices.Clone(f.Closes)
	i := slices.Index(dates, "2024-06-14")
	dates, closes = slices.Delete(dates, i, i+1), slices.Delete(closes, i, i+1)
	if sig, _ := Measure(f.Calendar, dates, closes, "2024-06-28"); sig.Status != StatusHold || sig.Reason != "no close on 2024-06-14" {
		t.Fatalf("missing bar: %+v", sig)
	}
}

// Unknown capital is the daemon's hold; room 0 sizes nothing; the scale
// ramp scales the shares; the cap binds when the floor would let the
// notional past it.
func TestSizeTargetRoomScaleAndCap(t *testing.T) {
	sig := Signal{Status: StatusOK, TrendOn: true, Sigma: 0.2, Close: 100}
	s := Sizing{CoreVolPct: 6, ScalePct: 100, MaxValuePct: 40, BudgetBase: 250000, Room: 1}
	full := SizeTarget(sig, s)
	if full.SleeveBudget != 15000 || full.Notional != 75000 || full.TargetShares != 750 {
		t.Fatalf("full: %+v", full)
	}
	s.Room = 0
	if got := SizeTarget(sig, s); got.TargetShares != 0 || got.Notional != 0 {
		t.Fatalf("room 0: %+v", got)
	}
	s.Room = 1
	for scale, want := range map[float64]int{25: 187, 50: 375, 100: 750, 0: 0} {
		s.ScalePct = scale
		if got := SizeTarget(sig, s); got.TargetShares != want {
			t.Fatalf("scale %g: %d, want %d", scale, got.TargetShares, want)
		}
	}
	s.ScalePct, s.MaxValuePct = 100, 20
	if got := SizeTarget(sig, s); got.Notional != 50000 || got.TargetShares != 500 {
		t.Fatalf("cap: %+v", got)
	}
	sig.TrendOn = false
	if got := SizeTarget(sig, s); got.TargetShares != 0 {
		t.Fatalf("trend off: %+v", got)
	}
}

// Property (review notes): on random inputs the target never rises as the
// drawdown deepens (room falls), nor as volatility rises.
func TestTargetNonIncreasingInDrawdownAndVolatility(t *testing.T) {
	r := rand.New(rand.NewPCG(7, 8))
	for range 5000 {
		sig := Signal{Status: StatusOK, TrendOn: true, Sigma: VolFloor + r.Float64()*0.5, Close: 20 + r.Float64()*200}
		s := Sizing{CoreVolPct: 1 + r.Float64()*10, ScalePct: []float64{25, 50, 100}[r.IntN(3)], MaxValuePct: 10 + r.Float64()*60, BudgetBase: 50000 + r.Float64()*1e6, Room: r.Float64()}
		base := SizeTarget(sig, s)
		deeper := s
		deeper.Room = s.Room * r.Float64()
		if got := SizeTarget(sig, deeper); got.TargetShares > base.TargetShares {
			t.Fatalf("room %g → %g raised the target %d → %d", s.Room, deeper.Room, base.TargetShares, got.TargetShares)
		}
		volatile := sig
		volatile.Sigma = sig.Sigma * (1 + r.Float64())
		if got := SizeTarget(volatile, s); got.TargetShares > base.TargetShares {
			t.Fatalf("sigma %g → %g raised the target", sig.Sigma, volatile.Sigma)
		}
	}
}

// The EWMA is pandas' (closed form checked by the lab against
// ewm(span=63).var()); here: a constant series has none, fewer returns
// than the span read zero, and it weights the recent past more.
func TestSigmaEstimators(t *testing.T) {
	flat := make([]float64, 252)
	if SigmaEWMA(flat) != 0 || SigmaSample(flat) != 0 {
		t.Fatal("a flat series has volatility")
	}
	if SigmaEWMA(make([]float64, EWMASpan-1)) != 0 {
		t.Fatal("too few returns")
	}
	r := rand.New(rand.NewPCG(1, 2))
	calmThenWild := make([]float64, 252)
	for i := range calmThenWild {
		vol := 0.005
		if i >= 230 {
			vol = 0.03
		}
		calmThenWild[i] = vol * r.NormFloat64()
	}
	if SigmaEWMA(calmThenWild) <= SigmaSample(calmThenWild) {
		t.Fatalf("the EWMA %g does not lead the sample %g after a spike", SigmaEWMA(calmThenWild), SigmaSample(calmThenWild))
	}
}

// The data guards hold an implausible volatility and an uncorroborated
// jump; SPY's move over the two US sessions spanning IBCF's day (review
// S5) corroborates a real one.
func TestGuardHoldsSuspectData(t *testing.T) {
	sig := Signal{Status: StatusOK, SigmaEWMA: 0.2, Sigma252: 0.18}
	w := Window{Dates: []string{"2026-03-02", "2026-03-03", "2026-03-04"}, Closes: []float64{100, 100.5, 90}}
	index := Series{Dates: []string{"2026-02-27", "2026-03-02", "2026-03-03", "2026-03-04"}, Closes: []float64{500, 501, 499, 460}}
	if got := Guard(sig, w, index); len(got) != 0 {
		t.Fatalf("a corroborated crash held: %v", got)
	}
	// SPY flat over the same two days: a probable price error.
	flat := Series{Dates: index.Dates, Closes: []float64{500, 501, 499, 500}}
	if got := Guard(sig, w, flat); len(got) != 1 || !strings.Contains(got[0], "probable price error") {
		t.Fatalf("an uncorroborated crash: %v", got)
	}
	// No SPY close on or after the day yet (decided before the US close).
	early := Series{Dates: index.Dates[:3], Closes: index.Closes[:3]}
	if got := Guard(sig, w, early); len(got) != 1 || !strings.Contains(got[0], "waits for SPY") {
		t.Fatalf("before the US close: %v", got)
	}
	sig.Sigma252 = 1.4
	if got := Guard(sig, Window{}, index); len(got) != 1 || !strings.Contains(got[0], "252-day volatility") {
		t.Fatalf("sigma bound: %v", got)
	}
}

// Headroom returns the largest quantity fits accepts, never one it refuses.
func TestHeadroomBisects(t *testing.T) {
	calls := 0
	fits := func(limit int) func(int) bool {
		return func(q int) bool { calls++; return q <= limit }
	}
	for _, c := range []struct{ want, limit, got int }{{100, 1000, 100}, {100, 37, 37}, {100, 0, 0}, {0, 50, 0}, {1, 0, 0}} {
		calls = 0
		if got := Headroom(c.want, fits(c.limit)); got != c.got {
			t.Fatalf("Headroom(%d) with limit %d = %d, want %d", c.want, c.limit, got, c.got)
		}
		if calls > 10 {
			t.Fatalf("%d evaluations for %d shares", calls, c.want)
		}
	}
	// A non-monotone fit never yields a refused quantity.
	odd := func(q int) bool { return q%7 != 3 && q < 60 }
	if got := Headroom(100, odd); !odd(got) {
		t.Fatalf("Headroom returned %d, which does not fit", got)
	}
}

// The band, the minimum and the headroom (design §1, acceptance test 5).
func TestDecideBandMinimumAndHeadroom(t *testing.T) {
	in := DecideInput{Target: 1000, TrendOn: true, Price: 50, BandPct: 10, MinOrderBase: 2500, Holdable: 5000}
	cases := []struct {
		name  string
		edit  func(*DecideInput)
		kind  string
		qty   int
		check func(Decision) bool
	}{
		{"first buy passes the band", func(i *DecideInput) { i.Held = 0 }, ActionBuy, 1000, nil},
		{"inside the band", func(i *DecideInput) { i.Held = 920 }, ActionNone, 0, func(d Decision) bool { return d.InsideBand }},
		{"beyond the band", func(i *DecideInput) { i.Held = 850 }, ActionBuy, 150, nil},
		{"over the target beyond the band", func(i *DecideInput) { i.Held = 1200 }, ActionSell, 200, nil},
		{"trend off sells all, ignoring band and minimum", func(i *DecideInput) { i.Held, i.TrendOn, i.Target = 10, false, 0 }, ActionSellAll, 10, nil},
		{"a small buy waits for the minimum", func(i *DecideInput) { i.Target, i.Held = 120, 80 }, ActionWait, 40, func(d Decision) bool { return d.BelowMinimum }},
		{"a small sale waits for the minimum", func(i *DecideInput) { i.Target, i.Held = 40, 80 }, ActionWait, 40, func(d Decision) bool { return d.BelowMinimum }},
		{"headroom cuts a buy", func(i *DecideInput) { i.Held, i.Holdable = 0, 600 }, ActionBuy, 600, func(d Decision) bool { return d.HeadroomCut }},
		{"no headroom", func(i *DecideInput) { i.Held, i.Holdable = 500, 500 }, ActionWait, 0, func(d Decision) bool { return d.HeadroomCut }},
		{"a blocked buy waits", func(i *DecideInput) { i.Held, i.BuyBlocked = 0, "the mandate verdict reads over" }, ActionWait, 0, func(d Decision) bool { return strings.Contains(d.Why, "reads over") }},
		{"a blocked buy still sells", func(i *DecideInput) { i.Held, i.BuyBlocked = 1500, "the mandate verdict reads over" }, ActionSell, 500, nil},
		{"at the target", func(i *DecideInput) { i.Held = 1000 }, ActionNone, 0, nil},
		{"nothing to do", func(i *DecideInput) { i.Target, i.Held = 0, 0 }, ActionNone, 0, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			x := in
			c.edit(&x)
			d := Decide(x)
			if d.Kind != c.kind || d.Quantity != c.qty || (c.check != nil && !c.check(d)) || d.Why == "" {
				t.Fatalf("%+v", d)
			}
			if d.Kind == ActionSell || d.Kind == ActionSellAll {
				if d.Quantity > x.Held {
					t.Fatalf("sold %d of %d held", d.Quantity, x.Held)
				}
			}
		})
	}
}
