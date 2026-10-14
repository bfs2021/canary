package scorecard

import (
	"encoding/json"
	"math"
	"math/rand/v2"
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
		for _, id := range change.ExecutionIDs {
			if id == "e-stop" {
				return Sleeve{Name: rpc.ScorecardSleeveRule, Origin: "proposal:trailing_stop"}
			}
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
