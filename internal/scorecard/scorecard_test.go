package scorecard

import (
	"encoding/json"
	"math"
	"math/rand/v2"
	"slices"
	"testing"
	"time"

	"github.com/osauer/canary/v2/internal/edge"
	"github.com/osauer/canary/v2/internal/flexstmt"
	"github.com/osauer/canary/v2/internal/rpc"
)

func TestImpliedVolRoundTrip(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		spot, strike, t, vol float64
		call                 bool
	}{
		{100, 100, 0.25, 0.30, true}, {100, 100, 0.25, 0.30, false},
		{100, 120, 0.08, 0.45, true}, {100, 80, 0.08, 0.45, false},
		{100, 90, 1.0, 0.20, true}, {100, 110, 1.0, 0.60, false},
		{50, 55, 0.02, 0.80, true}, {250, 200, 0.5, 0.15, false},
	} {
		price := Price(tc.spot, tc.strike, tc.t, tc.vol, RiskFreeRate, DividendYield, tc.call)
		got := ImpliedVol(price, tc.spot, tc.strike, tc.t, RiskFreeRate, DividendYield, tc.call)
		if math.Abs(got-tc.vol) > 1e-5 {
			t.Fatalf("iv(%+v) = %.6f, want %.6f", tc, got, tc.vol)
		}
	}
	// A fill at or below intrinsic has no solution and stays unknown.
	if got := ImpliedVol(20, 120, 100, 0.25, RiskFreeRate, DividendYield, true); got != 0 {
		t.Fatalf("below-intrinsic iv = %v, want 0", got)
	}
	if got := ImpliedVol(1, 100, 100, 0, RiskFreeRate, DividendYield, true); got != 0 {
		t.Fatalf("expired iv = %v, want 0", got)
	}
}

func TestDirectionSignForPutsAndShortCalls(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		right, side string
		want        int
	}{{RightCall, SideBuy, 1}, {RightCall, SideSell, -1}, {RightPut, SideBuy, -1}, {RightPut, SideSell, 1}, {"", SideBuy, 0}} {
		if got := DirectionSign(tc.right, tc.side); got != tc.want {
			t.Fatalf("sign(%s %s) = %d, want %d", tc.side, tc.right, got, tc.want)
		}
	}
	ev := edge.ChangeEvidence{Instruments: map[int64]flexstmt.Instrument{
		456: {ConID: 456, UnderlyingConID: 123, UnderlyingSymbol: "ACME", AssetClass: "OPT", Multiplier: new(100.0), Strike: new(100.0), Expiry: "20260320", PutCall: "P"},
		457: {ConID: 457, UnderlyingConID: 123, UnderlyingSymbol: "ACME", AssetClass: "OPT", Multiplier: new(100.0), Strike: new(100.0), Expiry: "20260320", PutCall: "C"},
	}}
	ev.Changes = []edge.PositionChange{
		{Key: "a", ConID: 456, UnderlyingConID: 123, Symbol: "ACME PUT", AssetClass: "OPT", Action: "open", ExecutedAt: day("2026-01-12"), DeltaQuantity: 2, PositionAfter: 2, ExecutionVWAP: new(3.0), Multiplier: new(100.0)},
		{Key: "b", ConID: 457, UnderlyingConID: 123, Symbol: "ACME CALL", AssetClass: "OPT", Action: "open", ExecutedAt: day("2026-01-12"), DeltaQuantity: -1, PositionAfter: -1, ExecutionVWAP: new(3.0), Multiplier: new(100.0)},
		{Key: "c", ConID: 123, Symbol: "ACME", AssetClass: "STK", Action: "open", ExecutedAt: day("2026-01-13"), DeltaQuantity: -10, PositionAfter: -10, ExecutionVWAP: new(100.0), Multiplier: new(1.0)},
		{Key: "d", ConID: 999, Symbol: "BILL", AssetClass: "BILL", Action: "open", ExecutedAt: day("2026-01-13"), DeltaQuantity: 10000},
	}
	ledger, skipped := BuildLedger(ev, nil)
	if len(ledger) != 3 || skipped[ReasonUnsupportedAsset] != 1 {
		t.Fatalf("ledger = %d rows, skipped = %v", len(ledger), skipped)
	}
	if ledger[0].Sign != -1 || ledger[0].Underlying != "ACME" || ledger[0].Option == nil || ledger[0].Option.Right != RightPut {
		t.Fatalf("long put = %+v", ledger[0])
	}
	if ledger[1].Sign != -1 || ledger[1].Side != SideSell || ledger[1].Option.Right != RightCall {
		t.Fatalf("short call = %+v", ledger[1])
	}
	if ledger[2].Sign != -1 || ledger[2].Instrument != InstrumentStock || ledger[2].UnderlyingConID != 123 {
		t.Fatalf("short stock = %+v", ledger[2])
	}
	if ledger[0].ClusterKey != "owner:2026-W03" || ledger[0].ID == ledger[1].ID || ledger[0].Sleeve != SleeveOwner {
		t.Fatalf("cluster or id = %+v", ledger[0])
	}
}

func TestClusterCountsAWeekOnce(t *testing.T) {
	t.Parallel()
	decisions := []Decision{
		{ID: "1", Sleeve: "owner", ClusterKey: "owner:2026-W02"}, {ID: "2", Sleeve: "owner", ClusterKey: "owner:2026-W02"}, {ID: "3", Sleeve: "owner", ClusterKey: "owner:2026-W02"},
		{ID: "4", Sleeve: "owner", ClusterKey: "owner:2026-W03"},
	}
	scores := []rpc.ScorecardHorizonScore{
		{Sessions: 10, Matured: true, Z: new(3.0), MarketReturn: new(0.01)}, {Sessions: 10, Matured: true, Z: new(1.0), MarketReturn: new(0.01)}, {Sessions: 10, Matured: true, Z: new(-1.0), MarketReturn: new(0.01)},
		{Sessions: 10, Matured: true, Z: new(2.0), MarketReturn: new(-0.02)},
	}
	got := sleeveEvidence("owner", rpc.ScorecardStreamEntries, 10, decisions, scores)
	if got.Clusters != 2 || got.Scored != 4 || math.Abs(got.SumZ-3) > 1e-12 || got.MeanZ == nil || math.Abs(*got.MeanZ-1.5) > 1e-12 {
		t.Fatalf("evidence = %+v", got)
	}
	if got.Gates.UpClusters != 1 || got.Gates.DownClusters != 1 || got.Gate != rpc.ScorecardGateAccruing {
		t.Fatalf("gates = %+v gate=%s", got.Gates, got.Gate)
	}
}

func TestMissingHistoryIsUnknownNotZero(t *testing.T) {
	t.Parallel()
	d := Decision{ID: "x", Sleeve: "owner", Underlying: "ACME", UnderlyingConID: 123, ConID: 123, Symbol: "ACME", Instrument: InstrumentStock, Action: "open", Side: SideBuy, Sign: 1, Quantity: 10, ExecutedAt: day("2026-01-12"), ClusterKey: "owner:2026-W03"}
	res := Evaluate(Input{AsOf: day("2026-03-01"), Decisions: []Decision{d}, History: History{Market: syntheticBars(0, "2025-01-01", 400, 1, 500)}})
	if len(res.Decisions) != 1 || len(res.Decisions[0].Scores) != 3 {
		t.Fatalf("result = %+v", res)
	}
	for _, score := range res.Decisions[0].Scores {
		if score.Z != nil || score.Reason != ReasonHistoryUnavailable || score.Matured {
			t.Fatalf("score = %+v, want unknown", score)
		}
	}
	if res.Coverage.UnknownByReason[ReasonHistoryUnavailable] != 3 || len(res.Coverage.ScoredByHorizon) != 0 {
		t.Fatalf("coverage = %+v", res.Coverage)
	}
	var sleeve rpc.ScorecardSleeve
	for _, s := range res.Sleeves {
		if s.Horizon == PrimaryHorizon && s.Stream == rpc.ScorecardStreamEntries {
			sleeve = s
		}
	}
	if sleeve.Scored != 0 || sleeve.Clusters != 0 || sleeve.MeanZ != nil || !sleeve.EValueAgainst || sleeve.Gate != rpc.ScorecardGateAccruing || sleeve.Unknown[ReasonHistoryUnavailable] != 1 {
		t.Fatalf("sleeve = %+v", sleeve)
	}
}

func TestExposureUsesOnlyPreDecisionSessions(t *testing.T) {
	t.Parallel()
	market := syntheticBars(0, "2025-01-01", 420, 7, 400)
	clean := syntheticBars(123, "2025-01-01", 420, 11, 100)
	d := Decision{ID: "x", Sleeve: "owner", Underlying: "ACME", UnderlyingConID: 123, ConID: 123, Symbol: "ACME", Instrument: InstrumentStock, Action: "open", Side: SideBuy, Sign: 1, Quantity: 10, ExecutedAt: clean[300].Day, ClusterKey: "owner:w"}
	base := Evaluate(Input{AsOf: day("2026-06-01"), Decisions: []Decision{d}, History: History{Market: market, Underlying: map[int64][]edge.DailyBar{123: clean}}})
	// A sentinel after entry: every later close is a wild multiple.
	wild := append([]edge.DailyBar(nil), clean...)
	for i := 301; i < len(wild); i++ {
		wild[i].Close *= 2 + 5*float64(i%3)
	}
	shifted := Evaluate(Input{AsOf: day("2026-06-01"), Decisions: []Decision{d}, History: History{Market: market, Underlying: map[int64][]edge.DailyBar{123: wild}}})
	for i := range base.Decisions[0].Scores {
		a, b := base.Decisions[0].Scores[i], shifted.Decisions[0].Scores[i]
		if a.Z == nil || b.Z == nil || a.Beta == nil || b.Beta == nil {
			t.Fatalf("scores unknown: %+v / %+v", a, b)
		}
		if *a.Beta != *b.Beta || *a.IdioVolDaily != *b.IdioVolDaily {
			t.Fatalf("post-entry bars changed the estimate: beta %v/%v sigma %v/%v", *a.Beta, *b.Beta, *a.IdioVolDaily, *b.IdioVolDaily)
		}
		if *a.Z == *b.Z {
			t.Fatalf("horizon %d z did not move with the realised path", a.Sessions)
		}
	}
	entry := base.Decisions[0].Entry
	if entry.SessionDay == nil || !entry.SessionDay.Equal(clean[300].Day) || entry.UnderlyingClose == nil || *entry.UnderlyingClose != clean[300].Close {
		t.Fatalf("entry = %+v", entry)
	}
}

func TestZeroSkillKeepsEValueSmallAndReachesFutility(t *testing.T) {
	t.Parallel()
	rng := rand.New(rand.NewPCG(7, 11))
	sum, futileAt, maxE := 0.0, 0, 0.0
	for n := 1; n <= 3000; n++ {
		sum += rng.NormFloat64()
		e, _ := EValue(n, sum)
		maxE = math.Max(maxE, e)
		if futileAt == 0 && n >= FutileClusters && PosteriorAbove(n, sum, 0.10) < FutilePosterior {
			futileAt = n
		}
	}
	if maxE >= EstablishedEValue {
		t.Fatalf("zero skill reached e-value %.1f", maxE)
	}
	if futileAt == 0 {
		t.Fatal("zero skill never reached futility")
	}
}

func TestICPointThreeIsEstablishedAtTheClosedFormCount(t *testing.T) {
	t.Parallel()
	closedForm, ok := ClustersToEstablish(0.3)
	if !ok {
		t.Fatal("no projection for IC 0.3")
	}
	brute := 0
	for n := 1; n <= 1000; n++ {
		if e, _ := EValue(n, 0.3*float64(n)); e >= EstablishedEValue {
			brute = n
			break
		}
	}
	if closedForm != brute || closedForm < 100 || closedForm > 200 {
		t.Fatalf("closed form %d, brute %d", closedForm, brute)
	}
	// A seeded simulation with cluster means ~ N(0.3, 1): the full gate
	// (e-value, n ≥ 100, posterior, halves and markets) passes within twice
	// the closed-form count and never before 100 clusters.
	rng := rand.New(rand.NewPCG(3, 5))
	var decisions []Decision
	var scores []rpc.ScorecardHorizonScore
	established := 0
	for n := 1; n <= 2*closedForm; n++ {
		decisions = append(decisions, Decision{ID: string(rune('a'+n%26)) + string(rune('0'+n/26)), Sleeve: "sim", ClusterKey: clusterKeyFor("sim", n)})
		scores = append(scores, rpc.ScorecardHorizonScore{Sessions: PrimaryHorizon, Matured: true, Z: new(0.3 + rng.NormFloat64()), MarketReturn: new(float64(1-2*(n%2)) * 0.01)})
		got := sleeveEvidence("sim", rpc.ScorecardStreamEntries, PrimaryHorizon, decisions, scores)
		if got.Gate == rpc.ScorecardGateEstablished {
			if n < EstablishedClusters {
				t.Fatalf("established at %d clusters", n)
			}
			established = n
			break
		}
	}
	if established == 0 {
		t.Fatalf("IC 0.3 was not established within %d clusters", 2*closedForm)
	}
}

func TestHolmStepDown(t *testing.T) {
	t.Parallel()
	got := Holm([]float64{0.01, 0.04, 0.03})
	want := []float64{0.03, 0.06, 0.06}
	for i := range want {
		if math.Abs(got[i]-want[i]) > 1e-12 {
			t.Fatalf("holm = %v, want %v", got, want)
		}
	}
}

func TestOptionWrapperAndRealisedVsImplied(t *testing.T) {
	t.Parallel()
	underlying := syntheticBars(123, "2025-01-01", 420, 13, 100)
	market := syntheticBars(0, "2025-01-01", 420, 17, 400)
	entryIdx := 320
	entryDay := underlying[entryIdx].Day
	spot := underlying[entryIdx].Close
	expiry := entryDay.AddDate(0, 0, 60)
	tYears := expiry.Sub(entryDay).Hours() / 24 / 365
	fill := Price(spot, 100, tYears, 0.35, RiskFreeRate, DividendYield, true)
	d := Decision{ID: "opt", Sleeve: "owner", Underlying: "ACME", UnderlyingConID: 123, ConID: 456, Symbol: "ACME CALL", Instrument: InstrumentOption,
		Option: &OptionContract{Expiry: expiry.Format(time.DateOnly), Strike: 100, Right: RightCall, Multiplier: 100}, Action: "open", Side: SideBuy, Sign: 1, Quantity: 2,
		FillPrice: &fill, Multiplier: new(100.0), Currency: "USD", ExecutionFX: new(0.9), ExecutedAt: entryDay.Add(15 * time.Hour), ClusterKey: "owner:w"}
	horizonDay := underlying[entryIdx+PrimaryHorizon].Day
	mark := 9.0
	res := Evaluate(Input{AsOf: day("2026-06-01"), BaseCurrency: "EUR", Decisions: []Decision{d},
		History: History{Market: market, Underlying: map[int64][]edge.DailyBar{123: underlying}},
		Marks:   map[int64][]edge.ContractMark{456: {{Day: horizonDay, Price: mark}}}})
	entry := res.Decisions[0].Entry
	if entry.ImpliedVol == nil || math.Abs(*entry.ImpliedVol-0.35) > 1e-5 || entry.Delta == nil || *entry.Delta <= 0 || *entry.Delta >= 1 {
		t.Fatalf("entry = %+v", entry)
	}
	var score rpc.ScorecardHorizonScore
	for _, s := range res.Decisions[0].Scores {
		if s.Sessions == PrimaryHorizon {
			score = s
		}
	}
	if score.Z == nil || score.RealisedVsImplied == nil || score.WrapperBase == nil {
		t.Fatalf("score = %+v", score)
	}
	ru := math.Log(underlying[entryIdx+PrimaryHorizon].Close / spot)
	wantRVI := math.Abs(ru) / (0.35 * math.Sqrt(float64(PrimaryHorizon)/252))
	if math.Abs(*score.RealisedVsImplied-wantRVI) > 1e-9 {
		t.Fatalf("rvi = %v, want %v", *score.RealisedVsImplied, wantRVI)
	}
	optionPnL := 2 * 100 * (mark - fill) * 0.9
	stockPnL := *entry.Delta * 100 * 2 * (underlying[entryIdx+PrimaryHorizon].Close - spot) * 0.9
	if math.Abs(*score.WrapperBase-(optionPnL-stockPnL)) > 1e-6 {
		t.Fatalf("wrapper = %v, want %v", *score.WrapperBase, optionPnL-stockPnL)
	}
	// Without a mark on the horizon day the wrapper is unknown, the
	// direction score stands.
	bare := Evaluate(Input{AsOf: day("2026-06-01"), BaseCurrency: "EUR", Decisions: []Decision{d}, History: History{Market: market, Underlying: map[int64][]edge.DailyBar{123: underlying}}})
	for _, s := range bare.Decisions[0].Scores {
		if s.Sessions == PrimaryHorizon && (s.Z == nil || s.WrapperBase != nil || s.WrapperReason != ReasonMarkUnavailable) {
			t.Fatalf("bare score = %+v", s)
		}
	}
}

func TestEvaluateIsDeterministic(t *testing.T) {
	t.Parallel()
	underlying := syntheticBars(123, "2025-01-01", 420, 19, 100)
	market := syntheticBars(0, "2025-01-01", 420, 23, 400)
	var decisions []Decision
	for i := range 30 {
		idx := 280 + i*3
		decisions = append(decisions, Decision{ID: string(rune('a' + i)), Sleeve: "owner", Underlying: "ACME", UnderlyingConID: 123, ConID: 123, Symbol: "ACME", Instrument: InstrumentStock, Action: "add", Side: SideBuy, Sign: 1, Quantity: 1, ExecutedAt: underlying[idx].Day, ClusterKey: clusterKeyFor("owner", i/2)})
	}
	in := Input{AsOf: day("2026-06-01"), Decisions: decisions, History: History{Market: market, Underlying: map[int64][]edge.DailyBar{123: underlying}}}
	a, _ := json.Marshal(Evaluate(in))
	b, _ := json.Marshal(Evaluate(in))
	if string(a) != string(b) {
		t.Fatal("evaluation differed between runs")
	}
	res := Evaluate(in)
	if res.DecisionsTotal != 30 || !res.Decisions[0].ExecutedAt.After(res.Decisions[29].ExecutedAt) {
		t.Fatalf("decisions = %d, order newest first = %v", res.DecisionsTotal, res.Decisions[0].ExecutedAt)
	}
}

// syntheticBars is a seeded random walk of positive closes on weekdays.
func syntheticBars(conid int64, start string, count int, seed uint64, first float64) []edge.DailyBar {
	rng := rand.New(rand.NewPCG(seed, seed+1))
	out := make([]edge.DailyBar, 0, count)
	current := day(start)
	close := first
	for len(out) < count {
		if current.Weekday() != time.Saturday && current.Weekday() != time.Sunday {
			close *= math.Exp(0.015*rng.NormFloat64() + 0.0002)
			out = append(out, edge.DailyBar{ConID: conid, Day: current, Close: close})
		}
		current = current.AddDate(0, 0, 1)
	}
	return out
}

func clusterKeyFor(sleeve string, week int) string {
	return sleeve + ":" + time.Date(2020, 1, 6, 0, 0, 0, 0, time.UTC).AddDate(0, 0, 7*week).Format("2006-01-02")
}

func day(v string) time.Time {
	parsed, err := time.Parse(time.DateOnly, v)
	if err != nil {
		panic(err)
	}
	return parsed
}

func TestExitsNeverMoveTheEntriesGate(t *testing.T) {
	t.Parallel()
	underlying := syntheticBars(123, "2023-01-01", 900, 29, 100)
	market := syntheticBars(0, "2023-01-01", 900, 31, 400)
	var decisions []Decision
	// Two entries and three hundred exits, the exits all in distinct weeks.
	for i := range 302 {
		action, stream, side, sign := "exit", rpc.ScorecardStreamExits, SideSell, -1
		if i < 2 {
			action, stream, side, sign = "open", rpc.ScorecardStreamEntries, SideBuy, 1
		}
		idx := 300 + (i % 500)
		decisions = append(decisions, Decision{ID: "d" + string(rune('0'+i%10)) + string(rune('a'+i/10%26)) + string(rune('a'+i/260)), Sleeve: "owner", Stream: stream, Underlying: "ACME", UnderlyingConID: 123, ConID: 123, Symbol: "ACME", Instrument: InstrumentStock, Action: action, Side: side, Sign: sign, Quantity: 1, ExecutedAt: underlying[idx].Day, ClusterKey: clusterKeyFor("owner", i)})
	}
	res := Evaluate(Input{AsOf: day("2026-12-01"), Decisions: decisions, History: History{Market: market, Underlying: map[int64][]edge.DailyBar{123: underlying}}})
	var entries, exits rpc.ScorecardSleeve
	for _, s := range res.Sleeves {
		if s.Horizon != PrimaryHorizon {
			continue
		}
		switch s.Stream {
		case rpc.ScorecardStreamEntries:
			entries = s
		case rpc.ScorecardStreamExits:
			exits = s
		}
	}
	if !entries.Gated || entries.Decisions != 2 || entries.Clusters > 2 || entries.Gate != rpc.ScorecardGateAccruing {
		t.Fatalf("entries = %+v", entries)
	}
	if exits.Gated || exits.Gate != "" || exits.Decisions != 300 || exits.Clusters < 200 || exits.MeanZ == nil {
		t.Fatalf("exits = %+v", exits)
	}
	if res.Coverage.Entries != 2 || res.Coverage.Exits != 300 {
		t.Fatalf("coverage = %+v", res.Coverage)
	}
	// Even an exits stream with overwhelming evidence stays ungated.
	var exitDecisions []Decision
	var exitScores []rpc.ScorecardHorizonScore
	for n := range 400 {
		exitDecisions = append(exitDecisions, Decision{Sleeve: "owner", Stream: rpc.ScorecardStreamExits, ClusterKey: clusterKeyFor("owner", n)})
		exitScores = append(exitScores, rpc.ScorecardHorizonScore{Sessions: PrimaryHorizon, Matured: true, Z: new(2.0), MarketReturn: new(float64(1-2*(n%2)) * 0.01)})
	}
	loud := sleeveEvidence("owner", rpc.ScorecardStreamExits, PrimaryHorizon, exitDecisions, exitScores)
	if loud.Gated || loud.Gate != "" || loud.EValue < EstablishedEValue || loud.HolmAdjustedP != nil {
		t.Fatalf("loud exits = %+v", loud)
	}
}

func TestRuleOriginatedFillsNeverCountForOwner(t *testing.T) {
	t.Parallel()
	ev := edge.ChangeEvidence{Instruments: map[int64]flexstmt.Instrument{}, LifecycleTradeIDs: map[string]bool{"t-assign": true}}
	ev.Changes = []edge.PositionChange{
		{Key: "a", ConID: 123, Symbol: "ACME", AssetClass: "STK", Action: "open", ExecutedAt: day("2026-01-12"), DeltaQuantity: 10, PositionAfter: 10, ExecutionVWAP: new(100.0), Multiplier: new(1.0), ExecutionIDs: []string{"e-hand"}},
		{Key: "b", ConID: 123, Symbol: "ACME", AssetClass: "STK", Action: "exit", ExecutedAt: day("2026-01-20"), DeltaQuantity: -10, PositionBefore: 10, ExecutionVWAP: new(95.0), Multiplier: new(1.0), ExecutionIDs: []string{"e-stop"}},
		{Key: "c", ConID: 123, Symbol: "ACME", AssetClass: "STK", Action: "open", ExecutedAt: day("2026-02-02"), DeltaQuantity: 100, PositionAfter: 100, ExecutionVWAP: new(90.0), Multiplier: new(1.0), TradeIDs: []string{"t-assign"}},
	}
	resolve := func(change edge.PositionChange) Sleeve {
		if slices.Contains(change.ExecutionIDs, "e-stop") {
			return Sleeve{Name: rpc.ScorecardSleeveRule, Origin: "proposal:trailing_stop"}
		}
		return Sleeve{Name: SleeveOwner}
	}
	ledger, _ := BuildLedger(ev, resolve)
	if len(ledger) != 3 || ledger[0].Sleeve != SleeveOwner || ledger[0].Stream != rpc.ScorecardStreamEntries {
		t.Fatalf("ledger = %+v", ledger)
	}
	if ledger[1].Sleeve != rpc.ScorecardSleeveRule || ledger[1].Origin != "proposal:trailing_stop" || ledger[1].Stream != rpc.ScorecardStreamExits {
		t.Fatalf("stop fill = %+v", ledger[1])
	}
	if ledger[2].Sleeve != rpc.ScorecardSleeveRule || ledger[2].Origin != OriginLifecycle {
		t.Fatalf("assignment fill = %+v", ledger[2])
	}
	// The id names the fill, not the sleeve: a reclassified fill keeps it.
	owned, _ := BuildLedger(ev, OwnerSleeve)
	if owned[1].ID != ledger[1].ID || owned[1].Sleeve != SleeveOwner {
		t.Fatalf("reclassified id changed: %s vs %s", owned[1].ID, ledger[1].ID)
	}
	res := Evaluate(Input{AsOf: day("2026-03-01"), Decisions: ledger, History: History{Market: syntheticBars(0, "2025-01-01", 400, 1, 500)}})
	for _, s := range res.Sleeves {
		if s.Sleeve == rpc.ScorecardSleeveRule {
			t.Fatalf("rule sleeve was scored: %+v", s)
		}
		want := 0
		if s.Stream == rpc.ScorecardStreamEntries {
			want = 1
		}
		if s.Sleeve == SleeveOwner && s.Decisions != want {
			t.Fatalf("owner %s stream counts %d decisions, want 1 entry and 0 exits", s.Stream, s.Decisions)
		}
	}
	if res.Coverage.RuleOriginated["proposal:trailing_stop"] != 1 || res.Coverage.RuleOriginated[OriginLifecycle] != 1 || res.Coverage.Entries != 1 || res.Coverage.Exits != 0 || res.Coverage.Ledger != 3 {
		t.Fatalf("coverage = %+v", res.Coverage)
	}
	if len(res.Decisions) != 3 || res.Decisions[0].Sleeve != rpc.ScorecardSleeveRule || res.Decisions[0].Origin != OriginLifecycle {
		t.Fatalf("decisions = %+v", res.Decisions)
	}
}

// derivedBars follows the market's daily log returns with a loading and adds
// seeded noise: loading 1 with little noise is an ETF that is effectively the
// market, a small loading with large noise a single name.
func derivedBars(conid int64, market []edge.DailyBar, loading, noise float64, seed uint64) []edge.DailyBar {
	rng := rand.New(rand.NewPCG(seed, seed+1))
	out := make([]edge.DailyBar, len(market))
	close := 50.0
	for i, bar := range market {
		if i > 0 {
			close *= math.Exp(loading*math.Log(bar.Close/market[i-1].Close) + noise*rng.NormFloat64())
		}
		out[i] = edge.DailyBar{ConID: conid, Day: bar.Day, Close: close}
	}
	return out
}

// preEntryLogReturns are the daily log returns the estimate may use: the
// LookbackSessions window ending the session before entry.
func preEntryLogReturns(bars []edge.DailyBar, entryIdx int) []float64 {
	var out []float64
	for i := max(0, entryIdx-LookbackSessions) + 1; i < entryIdx; i++ {
		out = append(out, math.Log(bars[i].Close/bars[i-1].Close))
	}
	return out
}

func sampleSD(values []float64) float64 {
	m := mean(values)
	sum := 0.0
	for _, v := range values {
		sum += (v - m) * (v - m)
	}
	return math.Sqrt(sum / float64(len(values)-1))
}

func scoreAt(d rpc.ScorecardDecision, h int) rpc.ScorecardHorizonScore {
	for _, s := range d.Scores {
		if s.Sessions == h {
			return s
		}
	}
	return rpc.ScorecardHorizonScore{}
}

func sleeveAt(res rpc.ScorecardResult, sleeve, stream string, h int) (rpc.ScorecardSleeve, bool) {
	for _, s := range res.Sleeves {
		if s.Sleeve == sleeve && s.Stream == stream && s.Horizon == h {
			return s, true
		}
	}
	return rpc.ScorecardSleeve{}, false
}

func decisionByID(res rpc.ScorecardResult, id string) rpc.ScorecardDecision {
	for _, d := range res.Decisions {
		if d.ID == id {
			return d
		}
	}
	return rpc.ScorecardDecision{}
}

func TestIndexOptionIsScoredOnTotalVol(t *testing.T) {
	t.Parallel()
	market := syntheticBars(0, "2025-01-01", 420, 17, 400)
	// The market proxy as an option underlying: regressed on itself it leaves
	// no residual noise, so the single-name z is undefined.
	spy := make([]edge.DailyBar, len(market))
	for i, bar := range market {
		spy[i] = edge.DailyBar{ConID: 9001, Day: bar.Day, Close: bar.Close}
	}
	if f := ols(preEntryLogReturns(market, 300), preEntryLogReturns(spy, 300)); !f.ok || f.residualSD != 0 || f.rSquared != 1 {
		t.Fatalf("self-regression = %+v, want zero residual and R² 1", f)
	}
	entryIdx := 300
	entryDay, spot := spy[entryIdx].Day, spy[entryIdx].Close
	expiry := entryDay.AddDate(0, 0, 45)
	strike := math.Round(spot * 0.95)
	fill := Price(spot, strike, expiry.Sub(entryDay).Hours()/24/365, 0.18, RiskFreeRate, DividendYield, false)
	d := Decision{ID: "spy-put", Sleeve: SleeveOwner, Stream: rpc.ScorecardStreamEntries, Underlying: "SPY", UnderlyingConID: 9001, ConID: 9002, Symbol: "SPY PUT", Instrument: InstrumentOption,
		Option: &OptionContract{Expiry: expiry.Format(time.DateOnly), Strike: strike, Right: RightPut, Multiplier: 100}, Action: "open", Side: SideBuy, Sign: -1, Quantity: 2,
		FillPrice: &fill, Multiplier: new(100.0), Currency: "USD", ExecutedAt: entryDay.Add(15 * time.Hour), ClusterKey: "owner:2026-W01"}
	res := Evaluate(Input{AsOf: day("2026-06-01"), Decisions: []Decision{d}, History: History{Market: market, Underlying: map[int64][]edge.DailyBar{9001: spy}}})

	got := res.Decisions[0]
	if got.Stream != rpc.ScorecardStreamIndexEntries || got.Entry.IndexBasis != rpc.ScorecardIndexBasisList || got.Entry.MarketRSquared == nil || *got.Entry.MarketRSquared != 1 {
		t.Fatalf("decision = stream %q basis %q r2 %v", got.Stream, got.Entry.IndexBasis, got.Entry.MarketRSquared)
	}
	sigma := sampleSD(preEntryLogReturns(spy, entryIdx))
	for _, h := range Horizons {
		score := scoreAt(got, h)
		if score.Z == nil || score.TotalVolDaily == nil || score.IdioVolDaily != nil {
			t.Fatalf("horizon %d score = %+v", h, score)
		}
		want := -math.Log(spy[entryIdx+h].Close/spot) / (sigma * math.Sqrt(float64(h)))
		if math.Abs(*score.TotalVolDaily-sigma) > 1e-15 || math.Abs(*score.Z-want) > 1e-12 {
			t.Fatalf("horizon %d z = %v sigma %v, want %v and %v", h, *score.Z, *score.TotalVolDaily, want, sigma)
		}
	}
	if res.Coverage.UnknownByReason[ReasonIdioVolZero] != 0 || res.Coverage.IndexEntries != 1 || res.Coverage.Entries != 0 || res.Coverage.ScoredByHorizon[PrimaryHorizon] != 1 {
		t.Fatalf("coverage = %+v", res.Coverage)
	}
	index, ok := sleeveAt(res, SleeveOwner, rpc.ScorecardStreamIndexEntries, PrimaryHorizon)
	if !ok || index.Gated || index.Gate != "" || index.Scored != 1 || index.MeanZ == nil {
		t.Fatalf("index sleeve = %+v", index)
	}
	rvi := scoreAt(got, PrimaryHorizon).RealisedVsImplied
	if rvi == nil || index.LongPutRealisedVsImplied == nil || index.LongPutRealisedVsImplied.Count != 1 || index.LongPutRealisedVsImplied.Median != *rvi {
		t.Fatalf("long-put realised/implied = %+v, decision %v", index.LongPutRealisedVsImplied, rvi)
	}
	if entries, _ := sleeveAt(res, SleeveOwner, rpc.ScorecardStreamEntries, PrimaryHorizon); entries.Decisions != 0 || !entries.Gated {
		t.Fatalf("entries sleeve took the index decision: %+v", entries)
	}
}

func TestHighRSquaredETFIsRoutedToIndex(t *testing.T) {
	t.Parallel()
	market := syntheticBars(0, "2025-01-01", 420, 23, 400)
	broad := derivedBars(201, market, 1.0, 0.002, 41) // effectively the market, off the list
	single := derivedBars(202, market, 0.5, 0.02, 43) // a name with its own noise
	entryIdx := 300
	at := market[entryIdx].Day.Add(15 * time.Hour)
	decisions := []Decision{
		{ID: "broad", Sleeve: SleeveOwner, Stream: rpc.ScorecardStreamEntries, Underlying: "BRDX", UnderlyingConID: 201, ConID: 201, Symbol: "BRDX", Instrument: InstrumentStock, Action: "open", Side: SideBuy, Sign: 1, Quantity: 10, ExecutedAt: at, ClusterKey: "owner:2026-W01"},
		{ID: "broad-exit", Sleeve: SleeveOwner, Stream: rpc.ScorecardStreamExits, Underlying: "BRDX", UnderlyingConID: 201, ConID: 201, Symbol: "BRDX", Instrument: InstrumentStock, Action: "exit", Side: SideSell, Sign: -1, Quantity: 10, ExecutedAt: market[entryIdx+3].Day, ClusterKey: "owner:2026-W02"},
		{ID: "single", Sleeve: SleeveOwner, Stream: rpc.ScorecardStreamEntries, Underlying: "SNGL", UnderlyingConID: 202, ConID: 202, Symbol: "SNGL", Instrument: InstrumentStock, Action: "open", Side: SideBuy, Sign: 1, Quantity: 10, ExecutedAt: at, ClusterKey: "owner:2026-W01"},
		{ID: "qqq", Sleeve: SleeveOwner, Stream: rpc.ScorecardStreamEntries, Underlying: "qqq", UnderlyingConID: 202, ConID: 203, Symbol: "QQQ", Instrument: InstrumentStock, Action: "open", Side: SideBuy, Sign: 1, Quantity: 10, ExecutedAt: at, ClusterKey: "owner:2026-W01"},
	}
	res := Evaluate(Input{AsOf: day("2026-06-01"), Decisions: decisions, History: History{Market: market, Underlying: map[int64][]edge.DailyBar{201: broad, 202: single}}})

	b := decisionByID(res, "broad")
	if b.Stream != rpc.ScorecardStreamIndexEntries || b.Entry.IndexBasis != rpc.ScorecardIndexBasisRSquared || b.Entry.MarketRSquared == nil || *b.Entry.MarketRSquared < IndexRSquared {
		t.Fatalf("broad ETF = stream %q basis %q r2 %v", b.Stream, b.Entry.IndexBasis, b.Entry.MarketRSquared)
	}
	score := scoreAt(b, PrimaryHorizon)
	want := math.Log(broad[entryIdx+PrimaryHorizon].Close/broad[entryIdx].Close) / (sampleSD(preEntryLogReturns(broad, entryIdx)) * math.Sqrt(PrimaryHorizon))
	if score.Z == nil || math.Abs(*score.Z-want) > 1e-12 || score.IdioVolDaily != nil || score.Beta == nil {
		t.Fatalf("broad ETF score = %+v, want z %v", score, want)
	}
	if exit := decisionByID(res, "broad-exit"); exit.Stream != rpc.ScorecardStreamIndexExits {
		t.Fatalf("broad ETF exit stream = %q", exit.Stream)
	}
	// A list member is an index whatever its fit: here it shares the
	// single name's noisy series.
	if q := decisionByID(res, "qqq"); q.Stream != rpc.ScorecardStreamIndexEntries || q.Entry.IndexBasis != rpc.ScorecardIndexBasisList || *q.Entry.MarketRSquared >= IndexRSquared {
		t.Fatalf("list member = stream %q basis %q", q.Stream, q.Entry.IndexBasis)
	}
	if s := decisionByID(res, "single"); s.Stream != rpc.ScorecardStreamEntries || s.Entry.IndexBasis != "" || s.Entry.MarketRSquared == nil || *s.Entry.MarketRSquared >= IndexRSquared {
		t.Fatalf("single name = stream %q basis %q r2 %v", s.Stream, s.Entry.IndexBasis, s.Entry.MarketRSquared)
	}
	entries, _ := sleeveAt(res, SleeveOwner, rpc.ScorecardStreamEntries, PrimaryHorizon)
	indexEntries, _ := sleeveAt(res, SleeveOwner, rpc.ScorecardStreamIndexEntries, PrimaryHorizon)
	indexExits, _ := sleeveAt(res, SleeveOwner, rpc.ScorecardStreamIndexExits, PrimaryHorizon)
	if entries.Decisions != 1 || !entries.Gated || indexEntries.Decisions != 2 || indexEntries.Gated || indexEntries.Gate != "" || indexExits.Decisions != 1 || indexExits.Gated {
		t.Fatalf("streams: entries %d, index entries %d (gated %v), index exits %d", entries.Decisions, indexEntries.Decisions, indexEntries.Gated, indexExits.Decisions)
	}
	if indexEntries.LongPutRealisedVsImplied != nil {
		t.Fatalf("stock index entries reported a long-put median: %+v", indexEntries.LongPutRealisedVsImplied)
	}
	if res.Coverage.Entries != 1 || res.Coverage.Exits != 0 || res.Coverage.IndexEntries != 2 || res.Coverage.IndexExits != 1 {
		t.Fatalf("coverage = %+v", res.Coverage)
	}
}

func TestSingleNameScoreIsUnchanged(t *testing.T) {
	t.Parallel()
	market := syntheticBars(0, "2025-01-01", 420, 29, 400)
	name := derivedBars(301, market, 1.3, 0.018, 47)
	entryIdx := 310
	d := Decision{ID: "acme", Sleeve: SleeveOwner, Stream: rpc.ScorecardStreamEntries, Underlying: "ACME", UnderlyingConID: 301, ConID: 301, Symbol: "ACME", Instrument: InstrumentStock, Action: "open", Side: SideSell, Sign: -1, Quantity: 10, ExecutedAt: market[entryIdx].Day, ClusterKey: "owner:2026-W01"}
	res := Evaluate(Input{AsOf: day("2026-06-01"), Decisions: []Decision{d}, History: History{Market: market, Underlying: map[int64][]edge.DailyBar{301: name}}})
	got := res.Decisions[0]
	if got.Stream != rpc.ScorecardStreamEntries || got.Entry.IndexBasis != "" {
		t.Fatalf("single name routed: stream %q basis %q", got.Stream, got.Entry.IndexBasis)
	}
	// The market-adjusted z, computed here from first principles.
	x, y := preEntryLogReturns(market, entryIdx), preEntryLogReturns(name, entryIdx)
	mx, my := mean(x), mean(y)
	var sxx, sxy float64
	for i := range x {
		sxx += (x[i] - mx) * (x[i] - mx)
		sxy += (x[i] - mx) * (y[i] - my)
	}
	beta := sxy / sxx
	var sse float64
	for i := range x {
		e := y[i] - (my - beta*mx) - beta*x[i]
		sse += e * e
	}
	idio := math.Sqrt(sse / float64(len(x)-2))
	for _, h := range Horizons {
		score := scoreAt(got, h)
		ru := math.Log(name[entryIdx+h].Close / name[entryIdx].Close)
		rm := math.Log(market[entryIdx+h].Close / market[entryIdx].Close)
		want := -(ru - beta*rm) / (idio * math.Sqrt(float64(h)))
		if score.Z == nil || score.TotalVolDaily != nil || score.IdioVolDaily == nil || math.Abs(*score.IdioVolDaily-idio) > 1e-12 || math.Abs(*score.Beta-beta) > 1e-9 || math.Abs(*score.Z-want) > 1e-9 {
			t.Fatalf("horizon %d score = %+v, want z %v beta %v idio %v", h, score, want, beta, idio)
		}
	}
}

func TestIndexClassAndScaleNeverLookAhead(t *testing.T) {
	t.Parallel()
	market := syntheticBars(0, "2025-01-01", 420, 31, 400)
	broad := derivedBars(401, market, 1.0, 0.002, 53)
	single := derivedBars(402, market, 0.4, 0.02, 59)
	entryIdx := 300
	decisions := []Decision{
		{ID: "broad", Sleeve: SleeveOwner, Stream: rpc.ScorecardStreamEntries, Underlying: "BRDX", UnderlyingConID: 401, ConID: 401, Symbol: "BRDX", Instrument: InstrumentStock, Action: "open", Side: SideBuy, Sign: 1, Quantity: 1, ExecutedAt: market[entryIdx].Day, ClusterKey: "owner:w"},
		{ID: "single", Sleeve: SleeveOwner, Stream: rpc.ScorecardStreamEntries, Underlying: "SNGL", UnderlyingConID: 402, ConID: 402, Symbol: "SNGL", Instrument: InstrumentStock, Action: "open", Side: SideBuy, Sign: 1, Quantity: 1, ExecutedAt: market[entryIdx].Day, ClusterKey: "owner:w"},
	}
	base := Evaluate(Input{AsOf: day("2026-06-01"), Decisions: decisions, History: History{Market: market, Underlying: map[int64][]edge.DailyBar{401: broad, 402: single}}})
	// After entry the broad ETF turns into noise and the single name into
	// the market itself: a look-ahead would swap their classes.
	noisy, tracking := append([]edge.DailyBar(nil), broad...), append([]edge.DailyBar(nil), single...)
	rng := rand.New(rand.NewPCG(61, 67))
	for i := entryIdx + 1; i < len(market); i++ {
		noisy[i].Close = noisy[i-1].Close * math.Exp(0.05*rng.NormFloat64())
		tracking[i].Close = tracking[i-1].Close * market[i].Close / market[i-1].Close
	}
	shifted := Evaluate(Input{AsOf: day("2026-06-01"), Decisions: decisions, History: History{Market: market, Underlying: map[int64][]edge.DailyBar{401: noisy, 402: tracking}}})
	for _, id := range []string{"broad", "single"} {
		a, b := decisionByID(base, id), decisionByID(shifted, id)
		if a.Stream != b.Stream || a.Entry.IndexBasis != b.Entry.IndexBasis || *a.Entry.MarketRSquared != *b.Entry.MarketRSquared {
			t.Fatalf("%s: post-entry bars changed the class: %q/%q r2 %v/%v", id, a.Stream, b.Stream, *a.Entry.MarketRSquared, *b.Entry.MarketRSquared)
		}
		sa, sb := scoreAt(a, PrimaryHorizon), scoreAt(b, PrimaryHorizon)
		if sa.Z == nil || sb.Z == nil || *sa.Z == *sb.Z {
			t.Fatalf("%s: z did not move with the realised path: %v / %v", id, sa.Z, sb.Z)
		}
		if id == "broad" && (sa.TotalVolDaily == nil || *sa.TotalVolDaily != *sb.TotalVolDaily) {
			t.Fatalf("broad: post-entry bars changed sigma_total: %v / %v", sa.TotalVolDaily, sb.TotalVolDaily)
		}
	}
	if decisionByID(base, "broad").Stream != rpc.ScorecardStreamIndexEntries || decisionByID(base, "single").Stream != rpc.ScorecardStreamEntries {
		t.Fatal("classes are not the ones the pre-entry fit implies")
	}
}

func TestOnlyFlatSeriesLeaveTheVolatilityUndefined(t *testing.T) {
	t.Parallel()
	market := syntheticBars(0, "2025-01-01", 420, 37, 400)
	flat := make([]edge.DailyBar, len(market))
	for i, bar := range market {
		flat[i] = edge.DailyBar{ConID: 501, Day: bar.Day, Close: 25}
	}
	at := market[300].Day
	decisions := []Decision{
		{ID: "stale", Sleeve: SleeveOwner, Stream: rpc.ScorecardStreamEntries, Underlying: "STAL", UnderlyingConID: 501, ConID: 501, Symbol: "STAL", Instrument: InstrumentStock, Action: "open", Side: SideBuy, Sign: 1, Quantity: 1, ExecutedAt: at, ClusterKey: "owner:w"},
		{ID: "stale-index", Sleeve: SleeveOwner, Stream: rpc.ScorecardStreamEntries, Underlying: "SPX", UnderlyingConID: 501, ConID: 502, Symbol: "SPX", Instrument: InstrumentStock, Action: "open", Side: SideBuy, Sign: 1, Quantity: 1, ExecutedAt: at, ClusterKey: "owner:w"},
	}
	res := Evaluate(Input{AsOf: day("2026-06-01"), Decisions: decisions, History: History{Market: market, Underlying: map[int64][]edge.DailyBar{501: flat}}})
	if s := decisionByID(res, "stale"); s.Stream != rpc.ScorecardStreamEntries || s.Entry.MarketRSquared != nil || scoreAt(s, PrimaryHorizon).Reason != ReasonIdioVolZero {
		t.Fatalf("flat single name = stream %q reason %q", s.Stream, scoreAt(s, PrimaryHorizon).Reason)
	}
	if s := decisionByID(res, "stale-index"); s.Stream != rpc.ScorecardStreamIndexEntries || scoreAt(s, PrimaryHorizon).Reason != ReasonTotalVolZero {
		t.Fatalf("flat index = stream %q reason %q", s.Stream, scoreAt(s, PrimaryHorizon).Reason)
	}
}
