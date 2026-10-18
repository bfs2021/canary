package daemon

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/osauer/canary/v2/internal/daemon/corestore"
	edgecore "github.com/osauer/canary/v2/internal/edge"
	"github.com/osauer/canary/v2/internal/rpc"
	"github.com/osauer/canary/v2/internal/scorecard"
	ibkrlib "github.com/osauer/canary/v2/pkg/ibkr"
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
	// A name read today is not read again today; a name whose read failed
	// waits for its retry time; a covered name is not fetched at all.
	covered := make([]edgecore.DailyBar, 0, 500)
	for i := range 500 {
		covered = append(covered, edgecore.DailyBar{ConID: 2, Day: now.AddDate(0, 0, -500+i), Close: 100})
	}
	cache := scorecardBarCache{Names: map[string]scorecardBarSeries{
		"1": {ConID: 1, Failure: "historical data timeout for SYNA after 45s", FailedAt: now.Add(-time.Hour), Failures: 1, RetryAt: now.Add(time.Hour)},
		"2": {ConID: 2, FetchedAt: now.AddDate(0, 0, -1), Lookback: 443, Bars: covered},
	}, Market: scorecardBarSeries{FetchedAt: now.Add(-time.Hour), Lookback: 900, Bars: covered}}
	if needs := scorecardHistoryNeeds(ledger, cache, now); len(needs) != 0 {
		t.Fatalf("needs after today's fetches = %+v", needs)
	}
	// At its retry time the failed name is asked again, alone.
	if needs := scorecardHistoryNeeds(ledger, cache, now.Add(time.Hour)); len(needs) != 1 || needs[0].conid != 1 {
		t.Fatalf("needs at the retry time = %+v", needs)
	}
	// Tomorrow the market advances (the October decision is immature) and
	// the failed name is still due; the matured, covered name is not, and
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

// A failed read is not written off for the day: the name is asked again at
// its retry time (the next pass, then doubling to at most a day), and a
// success today is the only fetch that counts as today's.
func TestScorecardFailedSeriesIsRetriedAtItsRetryTimeNotTomorrow(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 18, 1, 24, 0, 0, time.UTC)
	entry := time.Date(2026, 10, 1, 15, 0, 0, 0, time.UTC)
	// A failure the shipped cache stamped as fetched today (no retry time
	// yet) is retried one pass later, not tomorrow.
	legacy := scorecardBarSeries{ConID: 1, Symbol: "SYNA", FetchedAt: now, Failure: "historical data timeout for SYNA after 45s"}
	if scorecardSeriesStale(legacy, entry, entry, now.Add(time.Hour)) {
		t.Fatal("a failed series was asked again before its retry time")
	}
	if !scorecardSeriesStale(legacy, entry, entry, now.Add(scorecardPassInterval)) {
		t.Fatal("a failed series was written off for the day instead of retried at its retry time")
	}
	fetched := scorecardBarSeries{ConID: 1, Symbol: "SYNA", FetchedAt: now, Lookback: 400, Bars: []edgecore.DailyBar{{ConID: 1, Day: now.AddDate(0, 0, -1), Close: 100}}}
	if scorecardSeriesStale(fetched, entry, entry, now.Add(scorecardPassInterval)) {
		t.Fatal("a series fetched today was asked again today")
	}
	failed := scorecardBarSeries{ConID: 1, Symbol: "SYNA", FetchedAt: now.AddDate(0, 0, -1), Failure: "historical data timeout for SYNA after 45s", FailedAt: now, Failures: 3, RetryAt: now.Add(8 * time.Hour)}
	if scorecardSeriesStale(failed, entry, entry, now.Add(7*time.Hour)) || !scorecardSeriesStale(failed, entry, entry, now.Add(8*time.Hour)) {
		t.Fatal("a failed series is asked again at its retry time, not before")
	}
	// The wait doubles per consecutive failure, from one pass to a day.
	for failures, want := range map[int]time.Duration{1: 2 * time.Hour, 2: 4 * time.Hour, 3: 8 * time.Hour, 4: 16 * time.Hour, 5: 24 * time.Hour, 9: 24 * time.Hour} {
		if got := scorecardRetryAfter(failures); got != want {
			t.Fatalf("retry after %d failures = %s, want %s", failures, got, want)
		}
	}
	// The log warns once per name per day; the result lists every failure.
	if !scorecardFailureWarnDue(scorecardBarSeries{}, now) || scorecardFailureWarnDue(failed, now.Add(3*time.Hour)) || !scorecardFailureWarnDue(failed, now.AddDate(0, 0, 1)) {
		t.Fatal("the failure warning is due once per name per day")
	}
}

// A failed read keeps the closes of the last successful one and leaves its
// session stamp alone; a gateway that is away ends the pass with no mark
// against any name. The result lists every failure with its retry time.
func TestScorecardFetchPassKeepsBarsOnFailureAndStopsWhenTheGatewayIsAway(t *testing.T) {
	store, err := corestore.Open(t.Context(), corestore.Options{Path: filepath.Join(privateTestDir(t), "daemon.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Date(2026, 10, 18, 1, 24, 0, 0, time.UTC)
	yesterday := now.AddDate(0, 0, -1)
	srv := &Server{coreStore: store}
	srv.edgeFetchMarketBarsFn = func(context.Context, string, int) ([]ibkrlib.HistoricalBar, error) {
		return nil, errors.New("historical data timeout for SPY after 45s")
	}
	srv.edgeFetchBarsFn = func(_ context.Context, contract ibkrlib.Contract, _ int) ([]ibkrlib.HistoricalBar, error) {
		if contract.ConID == 1 {
			return []ibkrlib.HistoricalBar{{Date: "2026-10-16", Close: 101}, {Date: "2026-10-17", Close: 102}}, nil
		}
		return nil, fmt.Errorf("gateway unavailable: %w", ibkrlib.ErrIBKRUnavailable)
	}
	kept := []edgecore.DailyBar{{Day: yesterday.AddDate(0, 0, -1), Close: 500}, {Day: yesterday, Close: 501}}
	cache := scorecardBarCache{Version: scorecardBarsVersion, ScopeFingerprint: "scope_test", Names: map[string]scorecardBarSeries{}, Market: scorecardBarSeries{Symbol: "SPY", Currency: "USD", FetchedAt: yesterday, Lookback: 400, Bars: kept}}
	needs := []scorecardHistoryNeed{
		{market: true, symbol: "SPY", currency: "USD", lookback: 400},
		{conid: 1, symbol: "SYNA", currency: "USD", lookback: 400},
		{conid: 2, symbol: "SYNB", currency: "USD", lookback: 400},
		{conid: 3, symbol: "SYNC", currency: "USD", lookback: 400},
	}
	pending, err := srv.fetchScorecardNeeds(t.Context(), &cache, needs, now)
	if err != nil || pending != 2 {
		t.Fatalf("pending = %d, %v; want 2 (the pass ends when the gateway is away)", pending, err)
	}
	market := cache.Market
	if len(market.Bars) != 2 || !market.FetchedAt.Equal(yesterday) || market.Failure == "" || !market.FailedAt.Equal(now) || market.Failures != 1 || !market.RetryAt.Equal(now.Add(2*time.Hour)) {
		t.Fatalf("failed market series = %+v; want yesterday's bars and stamp kept, failure recorded, retry in one pass", market)
	}
	name := cache.Names["1"]
	if len(name.Bars) != 2 || !name.FetchedAt.Equal(now) || name.Failure != "" || name.Lookback != 400 {
		t.Fatalf("fetched series = %+v", name)
	}
	if _, ok := cache.Names["2"]; ok {
		t.Fatal("a name the gateway could not serve was marked")
	}
	reloaded, err := srv.loadScorecardBarCache(t.Context())
	if err != nil || len(reloaded.Market.Bars) != 2 || reloaded.Market.Failure != market.Failure || len(reloaded.Names) != 1 {
		t.Fatalf("saved cache = %+v, %v", reloaded, err)
	}
	history, state := scorecardHistoryFromCache(cache, pending)
	if len(history.Market) != 2 || len(history.Underlying[1]) != 2 || state.Names != 1 || state.Missing != 0 || state.Pending != 2 || !state.LastFetchedAt.Equal(now) {
		t.Fatalf("history = %d market bars, %d names; state = %+v", len(history.Market), len(history.Underlying), state)
	}
	if len(state.Failures) != 1 || state.Failures[0].Symbol != "SPY" || state.Failures[0].ConID != 0 || state.Failures[0].Bars != 2 || state.Failures[0].Failure != market.Failure || !state.Failures[0].RetryAt.Equal(now.Add(2*time.Hour)) {
		t.Fatalf("failures = %+v", state.Failures)
	}
	// A second failure doubles the wait and keeps the bars; a success
	// clears the failure and stamps the session.
	again, err := srv.fetchScorecardSeries(t.Context(), cache.Market, needs[0], now.Add(2*time.Hour))
	if err == nil || len(again.Bars) != 2 || again.Failures != 2 || !again.RetryAt.Equal(now.Add(6*time.Hour)) {
		t.Fatalf("second failure = %+v, %v", again, err)
	}
	srv.edgeFetchMarketBarsFn = func(context.Context, string, int) ([]ibkrlib.HistoricalBar, error) {
		return []ibkrlib.HistoricalBar{{Date: "2026-10-17", Close: 503}}, nil
	}
	fixed, err := srv.fetchScorecardSeries(t.Context(), again, needs[0], now.Add(6*time.Hour))
	if err != nil || len(fixed.Bars) != 1 || fixed.Failure != "" || fixed.Failures != 0 || !fixed.RetryAt.IsZero() || !fixed.FetchedAt.Equal(now.Add(6*time.Hour)) {
		t.Fatalf("recovered series = %+v, %v", fixed, err)
	}
}
