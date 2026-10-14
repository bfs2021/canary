package daemon

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/osauer/canary/v2/internal/daemon/corestore"
	edgecore "github.com/osauer/canary/v2/internal/edge"
	"github.com/osauer/canary/v2/internal/rpc"
	"github.com/osauer/canary/v2/internal/scorecard"
)

func TestScorecardLedgerAppendsEachDecisionOnceAndReloads(t *testing.T) {
	store, err := corestore.Open(t.Context(), corestore.Options{Path: filepath.Join(privateTestDir(t), "daemon.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	srv := &Server{coreStore: store}
	scope := scorecardLedgerScopePrefix + "scope_test"
	at := time.Date(2026, 1, 12, 15, 0, 0, 0, time.UTC)
	ledger := []scorecard.Decision{
		{ID: "decision_a", Sleeve: "owner", Underlying: "SYNA", UnderlyingConID: 1, ConID: 1, Symbol: "SYNA", Instrument: scorecard.InstrumentStock, Action: "open", Side: "buy", Sign: 1, Quantity: 10, ExecutedAt: at, ClusterKey: "owner:2026-W03"},
		{ID: "decision_b", Sleeve: "owner", Underlying: "SYNA", UnderlyingConID: 1, ConID: 1, Symbol: "SYNA", Instrument: scorecard.InstrumentStock, Action: "exit", Side: "sell", Sign: -1, Quantity: 10, ExecutedAt: at.AddDate(0, 0, 9), ClusterKey: "owner:2026-W04"},
	}
	known, err := srv.loadScorecardLedger(t.Context(), scope)
	if err != nil || len(known) != 0 {
		t.Fatalf("fresh ledger = %v, %v", known, err)
	}
	if err := srv.appendScorecardDecisions(t.Context(), scope, known, ledger); err != nil {
		t.Fatal(err)
	}
	reloaded, err := srv.loadScorecardLedger(t.Context(), scope)
	if err != nil || len(reloaded) != 2 || !reloaded["decision_a"] || !reloaded["decision_b"] {
		t.Fatalf("reloaded ledger = %v, %v", reloaded, err)
	}
	// A second pass with the same derivation appends nothing; a new
	// decision appends once.
	ledger = append(ledger, scorecard.Decision{ID: "decision_c", Sleeve: "owner", ConID: 2, UnderlyingConID: 2, Instrument: scorecard.InstrumentStock, ExecutedAt: at.AddDate(0, 0, 20)})
	if err := srv.appendScorecardDecisions(t.Context(), scope, reloaded, ledger); err != nil {
		t.Fatal(err)
	}
	rows, err := store.ListObservations(t.Context(), corestore.ObservationQuery{ScopeKey: scope, Source: scorecardSource, Kind: scorecardDecisionKind})
	if err != nil || len(rows) != 3 {
		t.Fatalf("observations = %d, %v", len(rows), err)
	}
	for _, row := range rows {
		if row.DecisionEligible {
			t.Fatalf("ledger row %d is decision-eligible", row.ID)
		}
	}
}

func TestScorecardHistoryNeedsPlanMarketFirstOncePerSession(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)
	ledger := []scorecard.Decision{
		{ID: "a", UnderlyingConID: 2, Underlying: "SYNB", Currency: "USD", ExecutedAt: time.Date(2026, 9, 1, 15, 0, 0, 0, time.UTC)},
		{ID: "b", UnderlyingConID: 1, Underlying: "SYNA", Currency: "USD", ExecutedAt: time.Date(2025, 9, 1, 15, 0, 0, 0, time.UTC)},
		{ID: "c", UnderlyingConID: 1, Underlying: "SYNA", Currency: "USD", ExecutedAt: time.Date(2026, 10, 1, 15, 0, 0, 0, time.UTC)},
		{ID: "d", UnderlyingConID: 0, Underlying: "", ExecutedAt: now},
	}
	needs := scorecardHistoryNeeds(ledger, scorecardBarCache{Names: map[string]scorecardBarSeries{}}, now)
	if len(needs) != 3 || !needs[0].market || needs[1].conid != 1 || needs[2].conid != 2 {
		t.Fatalf("needs = %+v", needs)
	}
	// The market and the oldest name need 400 days before the 2025 entry;
	// the September name needs 400 days before its own entry (443 today).
	if needs[0].lookback < 400+365 || needs[1].lookback != needs[0].lookback || needs[2].lookback != 443 || needs[0].lookback > scorecardMaxLookbackDays {
		t.Fatalf("lookbacks = %d %d %d", needs[0].lookback, needs[1].lookback, needs[2].lookback)
	}
	// A name fetched (even unsuccessfully) today is not fetched again
	// today; a covered name is not fetched at all.
	covered := make([]edgecore.DailyBar, 0, 500)
	for i := range 500 {
		covered = append(covered, edgecore.DailyBar{ConID: 2, Day: now.AddDate(0, 0, -500+i), Close: 100})
	}
	cache := scorecardBarCache{Names: map[string]scorecardBarSeries{
		"1": {ConID: 1, FetchedAt: now.Add(-2 * time.Hour), Failure: "gateway unavailable"},
		"2": {ConID: 2, FetchedAt: now.AddDate(0, 0, -1), Lookback: 443, Bars: covered},
	}, Market: scorecardBarSeries{FetchedAt: now.Add(-time.Hour), Lookback: 900, Bars: covered}}
	if needs := scorecardHistoryNeeds(ledger, cache, now); len(needs) != 0 {
		t.Fatalf("needs after today's fetches = %+v", needs)
	}
	// Tomorrow the market advances (the October decision is immature) and
	// the failed name is tried again; the matured, covered name is not, and
	// a short listing fetched with the full lookback is not asked again.
	tomorrow := now.AddDate(0, 0, 1)
	needs = scorecardHistoryNeeds(ledger, cache, tomorrow)
	if len(needs) != 2 || !needs[0].market || needs[1].conid != 1 {
		t.Fatalf("needs tomorrow = %+v", needs)
	}
	short := scorecardBarSeries{ConID: 2, FetchedAt: now.AddDate(0, 0, -1), Lookback: 443, Bars: covered[400:]}
	if scorecardSeriesStale(short, ledger[0].ExecutedAt, ledger[0].ExecutedAt, tomorrow) {
		t.Fatal("a short listing fetched with the full lookback was asked again")
	}
	if got := scorecardNormalizeBars(append([]edgecore.DailyBar{{Day: now, Close: 0}, {Day: now, Close: 5}}, covered...), 2); len(got) != 501 || got[500].Close != 5 || got[0].ConID != 2 {
		t.Fatalf("normalized = %d bars, last %+v", len(got), got[len(got)-1])
	}
}

func TestScorecardReadBoundsToSleeveAndPage(t *testing.T) {
	t.Parallel()
	result := rpc.ScorecardResult{
		Sleeves:   []rpc.ScorecardSleeve{{Sleeve: "owner", Horizon: 10, Clusters: 3}, {Sleeve: "algo", Horizon: 10, Clusters: 1}},
		Decisions: []rpc.ScorecardDecision{{ID: "1", Sleeve: "owner"}, {ID: "2", Sleeve: "algo"}, {ID: "3", Sleeve: "owner"}, {ID: "4", Sleeve: "owner"}},
	}
	got, err := scorecardBoundResult(result, rpc.ScorecardReadParams{Sleeve: "owner", Limit: 2})
	if err != nil || len(got.Sleeves) != 1 || got.Sleeves[0].Clusters != 3 || got.DecisionsTotal != 3 || len(got.Decisions) != 2 || got.Decisions[1].ID != "3" {
		t.Fatalf("bound = %+v, %v", got, err)
	}
	if _, err := scorecardBoundResult(result, rpc.ScorecardReadParams{Sleeve: "nobody", Limit: 2}); err == nil {
		t.Fatal("unknown sleeve was accepted")
	}
	all, err := scorecardBoundResult(result, rpc.ScorecardReadParams{Limit: 50})
	if err != nil || len(all.Sleeves) != 2 || all.DecisionsTotal != 4 || len(all.Decisions) != 4 {
		t.Fatalf("unbounded = %+v, %v", all, err)
	}
}

func TestScorecardSleeveResolverTagsRuleOriginsNeverOwner(t *testing.T) {
	t.Parallel()
	events := []orderJournalEvent{
		{ExecID: "e-stop", OrderRef: "canary-stop-1", Origin: rpc.OrderOriginPairedDevice},
		{ExecID: "e-sweep", OrderRef: "canary-sweep-1", Origin: rpc.OrderOriginPairedDevice},
		{ExecID: "e-pre", OrderRef: "canary-pre-1", Origin: rpc.OrderOriginDaemonPreAuthorised},
		{ExecID: "e-daemon", OrderRef: "canary-d-1", TransmitOrigin: corestore.OriginDaemon},
		{ExecID: "e-hand", OrderRef: "canary-hand-1", Origin: rpc.OrderOriginHumanTTY},
	}
	proposals := []proposalEvent{
		{Type: "submitted", OrderRef: "canary-stop-1", Bucket: rpc.TradeProposalBucketTrailingStop},
		{Type: "submitted", OrderRef: "canary-sweep-1", Bucket: rpc.TradeProposalBucketCashSweep},
		{Type: "previewed", OrderRef: "canary-hand-1", Bucket: rpc.TradeProposalBucketTrailingStop},
	}
	resolve := scorecardSleeveResolver(events, proposals)
	for _, tc := range []struct {
		exec       string
		wantSleeve string
		wantOrigin string
	}{
		{"e-stop", rpc.ScorecardSleeveRule, "proposal:trailing_stop"},
		{"e-sweep", rpc.ScorecardSleeveRule, "proposal:cash_sweep"},
		{"e-pre", rpc.ScorecardSleeveRule, "origin:" + rpc.OrderOriginDaemonPreAuthorised},
		{"e-daemon", rpc.ScorecardSleeveRule, "origin:daemon_internal"},
		{"e-hand", scorecard.SleeveOwner, ""},
		{"e-tws", scorecard.SleeveOwner, ""},
	} {
		got := resolve(edgecore.PositionChange{ExecutionIDs: []string{tc.exec}})
		if got.Name != tc.wantSleeve || got.Origin != tc.wantOrigin {
			t.Fatalf("%s resolved to %+v, want %s %q", tc.exec, got, tc.wantSleeve, tc.wantOrigin)
		}
	}
}

// The mandate's forced reduction is a rule's sale, never the owner's: a fill
// of a mandate_reduction row approved on the paired device counts as rule,
// as one the daemon placed under pre-authorisation does by its origin.
func TestScorecardSleeveResolverTagsMandateReductionAsRule(t *testing.T) {
	t.Parallel()
	if !scorecardRuleBucket(rpc.TradeProposalBucketMandateReduction) {
		t.Fatal("mandate_reduction is not a rule bucket")
	}
	events := []orderJournalEvent{
		{ExecID: "e-mandate", OrderRef: "canary-mandate-1", Origin: rpc.OrderOriginPairedDevice},
		{ExecID: "e-mandate-auto", OrderRef: "canary-mandate-2", Origin: rpc.OrderOriginDaemonPreAuthorised},
	}
	proposals := []proposalEvent{
		{Type: "submitted", OrderRef: "canary-mandate-1", Bucket: rpc.TradeProposalBucketMandateReduction},
		{Type: "submitted", OrderRef: "canary-mandate-2", Bucket: rpc.TradeProposalBucketMandateReduction},
	}
	resolve := scorecardSleeveResolver(events, proposals)
	for _, exec := range []string{"e-mandate", "e-mandate-auto"} {
		got := resolve(edgecore.PositionChange{ExecutionIDs: []string{exec}})
		if got.Name != rpc.ScorecardSleeveRule || got.Origin != "proposal:"+rpc.TradeProposalBucketMandateReduction {
			t.Fatalf("%s resolved to %+v, want rule proposal:mandate_reduction", exec, got)
		}
	}
}
