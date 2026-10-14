package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/osauer/canary/v2/internal/daemon/corestore"
	edgecore "github.com/osauer/canary/v2/internal/edge"
	"github.com/osauer/canary/v2/internal/rpc"
	"github.com/osauer/canary/v2/internal/scorecard"
	ibkrlib "github.com/osauer/canary/v2/pkg/ibkr"
)

// The decision scorecard (internal-docs/design/decision-scorecard.md): an
// append-only ledger of position changes per sleeve in daemon.db, a bounded
// history cache, and the evaluation the read serves. The worker owns every
// write; the read returns the saved publication.
const (
	scorecardLedgerScopePrefix    = "scorecard/ledger/"
	scorecardDecisionKind         = "scorecard.decision.v1"
	scorecardSource               = "scorecard"
	scorecardBarsStateKind        = "scorecard_bars"
	scorecardPublicationStateKind = "scorecard_publication"
	scorecardBarsVersion          = 1
	scorecardPublicationVersion   = 1

	// scorecardMaxFetchesPerPass bounds the IBKR history requests one pass
	// books on the background lane (60 per 10 minutes shared with every
	// other history reader); the rest wait for the next pass.
	scorecardMaxFetchesPerPass = 20
	// scorecardMaxBars bounds each cached series.
	scorecardMaxBars = 800
	// scorecardLookbackCalendarDays precedes the earliest decision so the
	// 252-session estimation window is covered.
	scorecardLookbackCalendarDays = 400
	// scorecardMaturityCalendarDays follows a decision until its 21st session.
	scorecardMaturityCalendarDays = 32
	scorecardMaxLookbackDays      = 3 * 365
	scorecardPassInterval         = 2 * time.Hour
	scorecardFetchTimeout         = 90 * time.Second
	scorecardLedgerPage           = 10000
)

type scorecardBarCache struct {
	Version          int                           `json:"version"`
	ScopeFingerprint string                        `json:"scope_fingerprint"`
	Names            map[string]scorecardBarSeries `json:"names"`
	Market           scorecardBarSeries            `json:"market"`
}

type scorecardBarSeries struct {
	ConID     int64     `json:"conid"`
	Symbol    string    `json:"symbol"`
	Currency  string    `json:"currency"`
	FetchedAt time.Time `json:"fetched_at,omitzero"`
	// Lookback is the calendar-day span the last fetch asked for; a name
	// whose history is shorter than that is not asked again for it.
	Lookback int                 `json:"lookback,omitempty"`
	Failure  string              `json:"failure,omitempty"`
	Bars     []edgecore.DailyBar `json:"bars"`
}

type scorecardPublication struct {
	Version             int                 `json:"version"`
	ScopeFingerprint    string              `json:"scope_fingerprint"`
	EvidenceFingerprint string              `json:"evidence_fingerprint,omitempty"`
	Result              rpc.ScorecardResult `json:"result"`
	UpdatedAt           time.Time           `json:"updated_at"`
}

func (s *Server) startScorecardWorker(ctx context.Context) {
	if s == nil || ctx == nil {
		return
	}
	s.mu.Lock()
	if s.scorecardWake != nil {
		s.mu.Unlock()
		return
	}
	s.scorecardWake = make(chan struct{}, 1)
	wake := s.scorecardWake
	s.scorecardWorkerWG.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.scorecardWorkerWG.Done()
		ticker := time.NewTicker(scorecardPassInterval)
		defer ticker.Stop()
		for {
			s.scorecardBusy.Store(true)
			if err := s.rebuildScorecard(ctx); err != nil && ctx.Err() == nil {
				s.warnf("decision scorecard pass failed: %v", err)
				s.publishScorecardState(context.WithoutCancel(ctx), rpc.ScorecardStateUnavailable, "authority_unavailable")
			}
			s.scorecardBusy.Store(false)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			case <-wake:
			}
		}
	}()
}

func (s *Server) kickScorecardRebuild() {
	if s == nil {
		return
	}
	s.mu.Lock()
	wake := s.scorecardWake
	s.mu.Unlock()
	if wake == nil {
		return
	}
	select {
	case wake <- struct{}{}:
	default:
	}
}

// rebuildScorecard is one pass: derive the ledger from the retained
// statements, append what is new, fetch the history still missing within the
// budget, evaluate and save the publication.
func (s *Server) rebuildScorecard(ctx context.Context) error {
	if s == nil || s.coreStore == nil {
		return fmt.Errorf("scorecard SQLite authority is unavailable")
	}
	if s.cfg == nil || !s.cfg.Flex.Enabled || strings.TrimSpace(s.cfg.Flex.QueryID) == "" {
		return s.publishScorecardState(ctx, rpc.ScorecardStateUnavailable, "flex_configuration_required")
	}
	scope := s.currentBrokerStateScope()
	if !brokerScopeConcrete(scope) {
		return s.publishScorecardState(ctx, rpc.ScorecardStateUnavailable, "account_scope_unavailable")
	}
	scopeFingerprint := edgeScopeFingerprint(scope)
	loadCtx, cancel := context.WithTimeout(ctx, edgeStatementLoadTimeout)
	projected, err := s.loadEdgeProjectionEvidence(loadCtx, scope, scopeFingerprint)
	cancel()
	if err != nil {
		return err
	}
	if len(projected.Statements) == 0 {
		return s.saveScorecardPublication(ctx, scorecardPublication{ScopeFingerprint: scopeFingerprint, Result: scorecardStateOnlyResult(rpc.ScorecardStateBackfilling, "statement_account_pending")})
	}
	base := inferEdgeBaseCurrency(projected.Statements)
	evidence, err := edgecore.PositionChanges(projected.Statements, base)
	if err != nil {
		return err
	}
	localEvents, localProposals, _ := s.edgeProtectionEvidence(ctx, scope)
	ledger, skipped := scorecard.BuildLedger(evidence, scorecardSleeveResolver(localEvents, localProposals))
	ledgerScope := scorecardLedgerScopePrefix + scopeFingerprint
	persisted, err := s.loadScorecardLedger(ctx, ledgerScope)
	if err != nil {
		return err
	}
	if err := s.appendScorecardDecisions(ctx, ledgerScope, persisted, ledger); err != nil {
		return err
	}
	current := map[string]bool{}
	for _, d := range ledger {
		current[d.ID] = true
	}
	superseded := 0
	for id := range persisted {
		if !current[id] {
			superseded++
		}
	}

	now := s.edgeNow()
	cache, err := s.loadScorecardBarCache(ctx)
	if err != nil {
		return err
	}
	if cache.ScopeFingerprint != scopeFingerprint {
		cache = scorecardBarCache{Version: scorecardBarsVersion, ScopeFingerprint: scopeFingerprint, Names: map[string]scorecardBarSeries{}}
	}
	needs := scorecardHistoryNeeds(ledger, cache, now)
	fetched, failed := 0, 0
	for _, need := range needs {
		if fetched >= scorecardMaxFetchesPerPass || ctx.Err() != nil {
			break
		}
		fetched++
		series := s.fetchScorecardSeries(ctx, need, now)
		if series.Failure != "" {
			failed++
		}
		if need.market {
			cache.Market = series
		} else {
			cache.Names[strconv.FormatInt(need.conid, 10)] = series
		}
		if err := s.saveScorecardBarCache(ctx, cache); err != nil {
			return err
		}
	}
	pending := len(needs) - fetched
	history := scorecard.History{Underlying: make(map[int64][]edgecore.DailyBar, len(cache.Names)), Market: append([]edgecore.DailyBar(nil), cache.Market.Bars...), MarketSymbol: "SPY"}
	missing := 0
	for _, series := range cache.Names {
		if len(series.Bars) == 0 {
			missing++
			continue
		}
		history.Underlying[series.ConID] = append([]edgecore.DailyBar(nil), series.Bars...)
	}
	state := rpc.ScorecardHistory{Names: len(cache.Names), Missing: missing, Pending: pending, LastFetchedAt: scorecardLastFetch(cache)}
	result := scorecard.Evaluate(scorecard.Input{
		AsOf: now, EvidenceTo: scorecardEvidenceTo(projected), BaseCurrency: base, Decisions: ledger, History: history,
		Marks: evidence.Marks, Events: evidence.Events, Skipped: skipped, Superseded: superseded, HistoryState: state,
	})
	switch {
	case pending > 0:
		result.State, result.Reason = rpc.ScorecardStateBackfilling, "history_backfill_pending"
	case failed > 0 || missing > 0:
		result.Reason = "history_partial"
	}
	if !sameBrokerScope(scope, s.currentBrokerStateScope()) {
		return nil
	}
	return s.saveScorecardPublication(ctx, scorecardPublication{ScopeFingerprint: scopeFingerprint, EvidenceFingerprint: projected.Fingerprint, Result: result})
}

// scorecardHistoryNeed is one history fetch the ledger still needs.
type scorecardHistoryNeed struct {
	market   bool
	conid    int64
	symbol   string
	currency string
	lookback int
	earliest time.Time
}

// scorecardHistoryNeeds lists the names whose cached history does not yet
// cover their decisions, the market proxy first, then by earliest decision.
// A name fetched (or failed) today is not fetched again today.
func scorecardHistoryNeeds(ledger []scorecard.Decision, cache scorecardBarCache, now time.Time) []scorecardHistoryNeed {
	type span struct {
		symbol, currency string
		earliest, latest time.Time
	}
	spans := map[int64]*span{}
	var earliestAll, latestAll time.Time
	for _, d := range ledger {
		if d.UnderlyingConID == 0 {
			continue
		}
		day := d.ExecutedAt.UTC()
		item := spans[d.UnderlyingConID]
		if item == nil {
			item = &span{symbol: d.Underlying, currency: d.Currency, earliest: day, latest: day}
			spans[d.UnderlyingConID] = item
		}
		if day.Before(item.earliest) {
			item.earliest = day
		}
		if day.After(item.latest) {
			item.latest = day
		}
		if earliestAll.IsZero() || day.Before(earliestAll) {
			earliestAll = day
		}
		if day.After(latestAll) {
			latestAll = day
		}
	}
	var out []scorecardHistoryNeed
	if !earliestAll.IsZero() && scorecardSeriesStale(cache.Market, earliestAll, latestAll, now) {
		out = append(out, scorecardHistoryNeed{market: true, symbol: "SPY", currency: "USD", lookback: scorecardLookback(earliestAll, now), earliest: earliestAll})
	}
	ids := make([]int64, 0, len(spans))
	for conid := range spans {
		ids = append(ids, conid)
	}
	sort.Slice(ids, func(i, j int) bool {
		a, b := spans[ids[i]], spans[ids[j]]
		if !a.earliest.Equal(b.earliest) {
			return a.earliest.Before(b.earliest)
		}
		return ids[i] < ids[j]
	})
	for _, conid := range ids {
		item := spans[conid]
		series := cache.Names[strconv.FormatInt(conid, 10)]
		if !scorecardSeriesStale(series, item.earliest, item.latest, now) {
			continue
		}
		out = append(out, scorecardHistoryNeed{conid: conid, symbol: item.symbol, currency: item.currency, lookback: scorecardLookback(item.earliest, now), earliest: item.earliest})
	}
	return out
}

// scorecardSeriesStale reports whether a series must be fetched: it is
// empty, was fetched with a shorter lookback than the earliest decision now
// needs, or ends before the latest decision could have matured and was not
// read since. One fetch per name per session.
func scorecardSeriesStale(series scorecardBarSeries, earliest, latest, now time.Time) bool {
	if !series.FetchedAt.IsZero() && series.FetchedAt.UTC().Format(time.DateOnly) == now.UTC().Format(time.DateOnly) {
		return false
	}
	if len(series.Bars) == 0 {
		return true
	}
	if scorecardLookback(earliest, now) > series.Lookback+30 {
		return true
	}
	last := series.Bars[len(series.Bars)-1].Day
	maturity := latest.AddDate(0, 0, scorecardMaturityCalendarDays)
	return last.Before(maturity) && series.FetchedAt.Before(minTime(maturity, now).Add(-18*time.Hour))
}

func scorecardLookback(earliest, now time.Time) int {
	days := int(now.Sub(earliest.AddDate(0, 0, -scorecardLookbackCalendarDays)).Hours()/24) + 1
	return max(scorecardLookbackCalendarDays, min(scorecardMaxLookbackDays, days))
}

// fetchScorecardSeries reads one name's daily closes on the background lane:
// the exact contract first, then the symbol route for an index underlying. A
// failure is recorded with today's fetch time so the budget is not spent on
// it again this session.
func (s *Server) fetchScorecardSeries(ctx context.Context, need scorecardHistoryNeed, now time.Time) scorecardBarSeries {
	readCtx, cancel := context.WithTimeout(ibkrlib.WithRequestPriority(ctx, ibkrlib.PriorityBackground), scorecardFetchTimeout)
	defer cancel()
	series := scorecardBarSeries{ConID: need.conid, Symbol: need.symbol, Currency: need.currency, FetchedAt: now, Lookback: need.lookback}
	var bars []edgecore.DailyBar
	var err error
	if need.market {
		bars, err = s.fetchEdgeMarketBars(readCtx, need.symbol, need.lookback)
	} else {
		bars, err = s.fetchEdgeBars(readCtx, edgeContractSpec{ConID: need.conid, Symbol: need.symbol, Currency: need.currency}, need.lookback)
		if err != nil && need.symbol != "" && readCtx.Err() == nil {
			bars, err = s.fetchEdgeMarketBars(readCtx, need.symbol, need.lookback)
		}
	}
	if err != nil {
		series.Failure = err.Error()
		return series
	}
	series.Bars = scorecardNormalizeBars(bars, need.conid)
	return series
}

// scorecardNormalizeBars keeps one positive close per day, oldest first,
// bounded to the newest scorecardMaxBars sessions.
func scorecardNormalizeBars(bars []edgecore.DailyBar, conid int64) []edgecore.DailyBar {
	byDay := map[string]edgecore.DailyBar{}
	for _, bar := range bars {
		if bar.Close > 0 && !bar.Day.IsZero() {
			byDay[bar.Day.UTC().Format(time.DateOnly)] = edgecore.DailyBar{ConID: conid, Day: bar.Day, Close: bar.Close}
		}
	}
	keys := make([]string, 0, len(byDay))
	for key := range byDay {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if len(keys) > scorecardMaxBars {
		keys = keys[len(keys)-scorecardMaxBars:]
	}
	out := make([]edgecore.DailyBar, 0, len(keys))
	for _, key := range keys {
		out = append(out, byDay[key])
	}
	return out
}

func scorecardLastFetch(cache scorecardBarCache) time.Time {
	last := cache.Market.FetchedAt
	for _, series := range cache.Names {
		if series.FetchedAt.After(last) {
			last = series.FetchedAt
		}
	}
	return last
}

func scorecardEvidenceTo(projected edgeProjectionEvidence) time.Time {
	var out time.Time
	for _, statement := range projected.Statements {
		if statement.ToDate.After(out) {
			out = statement.ToDate
		}
	}
	return out
}

// loadScorecardLedger returns the persisted decision ids for one scope.
func (s *Server) loadScorecardLedger(ctx context.Context, scopeKey string) (map[string]bool, error) {
	out := map[string]bool{}
	var after int64
	for {
		page, err := s.coreStore.ListObservations(ctx, corestore.ObservationQuery{ScopeKey: scopeKey, Source: scorecardSource, Kind: scorecardDecisionKind, AfterObservationID: after, Limit: scorecardLedgerPage})
		if err != nil {
			return nil, fmt.Errorf("load scorecard ledger: %w", err)
		}
		for _, row := range page {
			var d scorecard.Decision
			if err := json.Unmarshal(row.Payload, &d); err != nil {
				return nil, fmt.Errorf("decode scorecard decision %d: %w", row.ID, err)
			}
			out[d.ID] = true
			after = max(after, row.ID)
		}
		if len(page) < scorecardLedgerPage {
			return out, nil
		}
	}
}

// appendScorecardDecisions appends the decisions not yet persisted, in ledger
// order, as immutable non-decision observations. Known ids are never
// rewritten.
func (s *Server) appendScorecardDecisions(ctx context.Context, scopeKey string, known map[string]bool, ledger []scorecard.Decision) error {
	var inputs []corestore.ObservationInput
	for _, d := range ledger {
		if known[d.ID] {
			continue
		}
		payload, err := json.Marshal(d)
		if err != nil {
			return err
		}
		inputs = append(inputs, corestore.ObservationInput{ScopeKey: scopeKey, Source: scorecardSource, Kind: scorecardDecisionKind, ObservedAt: d.ExecutedAt, ContentType: "application/json", Payload: payload, DecisionEligible: false})
		known[d.ID] = true
	}
	for start := 0; start < len(inputs); start += 500 {
		end := min(len(inputs), start+500)
		if _, err := s.coreStore.AppendObservations(ctx, inputs[start:end]); err != nil {
			return fmt.Errorf("append scorecard ledger: %w", err)
		}
	}
	return nil
}

func (s *Server) loadScorecardBarCache(ctx context.Context) (scorecardBarCache, error) {
	out := scorecardBarCache{Version: scorecardBarsVersion, Names: map[string]scorecardBarSeries{}}
	doc, ok, err := s.coreStore.GetStateDocument(ctx, daemonStateScope, scorecardBarsStateKind)
	if err != nil || !ok {
		return out, err
	}
	if err := json.Unmarshal(doc.JSON, &out); err != nil {
		return out, fmt.Errorf("decode scorecard history cache: %w", err)
	}
	if out.Version != scorecardBarsVersion {
		return scorecardBarCache{Version: scorecardBarsVersion, Names: map[string]scorecardBarSeries{}}, nil
	}
	if out.Names == nil {
		out.Names = map[string]scorecardBarSeries{}
	}
	return out, nil
}

func (s *Server) saveScorecardBarCache(ctx context.Context, cache scorecardBarCache) error {
	cache.Version = scorecardBarsVersion
	raw, err := json.Marshal(cache)
	if err != nil {
		return err
	}
	return s.replaceEdgeStateDocument(ctx, scorecardBarsStateKind, raw)
}

func (s *Server) loadScorecardPublication(ctx context.Context) (scorecardPublication, bool, error) {
	doc, ok, err := s.coreStore.GetStateDocument(ctx, daemonStateScope, scorecardPublicationStateKind)
	if err != nil || !ok {
		return scorecardPublication{}, ok, err
	}
	var out scorecardPublication
	if err := json.Unmarshal(doc.JSON, &out); err != nil {
		return out, false, fmt.Errorf("decode scorecard publication: %w", err)
	}
	if out.Version != scorecardPublicationVersion {
		return scorecardPublication{}, false, nil
	}
	return out, true, nil
}

func (s *Server) saveScorecardPublication(ctx context.Context, publication scorecardPublication) error {
	publication.Version = scorecardPublicationVersion
	publication.UpdatedAt = s.edgeNow()
	if publication.Result.AsOf.IsZero() {
		publication.Result.AsOf = publication.UpdatedAt
	}
	raw, err := json.Marshal(publication)
	if err != nil {
		return err
	}
	return s.replaceEdgeStateDocument(ctx, scorecardPublicationStateKind, raw)
}

func (s *Server) publishScorecardState(ctx context.Context, state, reason string) error {
	if s == nil || s.coreStore == nil {
		return nil
	}
	publication, ok, _ := s.loadScorecardPublication(ctx)
	if !ok {
		publication = scorecardPublication{Result: scorecardStateOnlyResult(state, reason)}
	} else {
		publication.Result.State, publication.Result.Reason = state, reason
	}
	return s.saveScorecardPublication(ctx, publication)
}

func scorecardStateOnlyResult(state, reason string) rpc.ScorecardResult {
	return rpc.ScorecardResult{
		SchemaVersion: scorecard.SchemaVersion, State: state, Reason: reason, PrimaryHorizon: scorecard.PrimaryHorizon, Horizons: append([]int(nil), scorecard.Horizons[:]...),
		Sleeves: []rpc.ScorecardSleeve{}, Decisions: []rpc.ScorecardDecision{},
		Coverage: rpc.ScorecardCoverage{RuleOriginated: map[string]int{}, Skipped: map[string]int{}, ScoredByHorizon: map[int]int{}, UnknownByReason: map[string]int{}},
		History:  rpc.ScorecardHistory{MarketSymbol: "SPY"}, NotExecution: true,
	}
}

// scorecardSleeveResolver reads Canary's own order journal and proposal
// records: a fill whose execution the journal matches to a protection or
// automatic proposal bucket, or to a daemon-originated write, is the rule
// sleeve with that origin. Every other fill (hand, TWS, console, or any fill
// the bounded journal read could not see) is the owner's.
func scorecardSleeveResolver(events []orderJournalEvent, proposals []proposalEvent) scorecard.SleeveResolver {
	byExecution := map[string][]orderJournalEvent{}
	for _, ev := range events {
		if ev.ExecID != "" {
			byExecution[ev.ExecID] = append(byExecution[ev.ExecID], ev)
		}
	}
	bucketByRef := map[string]string{}
	for _, p := range proposals {
		if p.Type == "submitted" && p.OrderRef != "" && p.Bucket != "" {
			bucketByRef[p.OrderRef] = p.Bucket
		}
	}
	return func(change edgecore.PositionChange) scorecard.Sleeve {
		for _, execID := range change.ExecutionIDs {
			for _, ev := range byExecution[execID] {
				if origin := scorecardRuleOrigin(ev, bucketByRef); origin != "" {
					return scorecard.Sleeve{Name: rpc.ScorecardSleeveRule, Origin: origin}
				}
			}
		}
		return scorecard.Sleeve{Name: scorecard.SleeveOwner}
	}
}

func scorecardRuleOrigin(ev orderJournalEvent, bucketByRef map[string]string) string {
	if bucket := bucketByRef[ev.OrderRef]; ev.OrderRef != "" && scorecardRuleBucket(bucket) {
		return "proposal:" + bucket
	}
	switch ev.Origin {
	case rpc.OrderOriginDaemonPreAuthorised, rpc.OrderOriginDaemonProtectiveGuard:
		return "origin:" + ev.Origin
	}
	if ev.TransmitOrigin == corestore.OriginDaemon {
		return "origin:" + string(ev.TransmitOrigin)
	}
	return ""
}

// scorecardRuleBucket names the proposal buckets whose fills are a rule's,
// not a decision: protection, hygiene, budget and expiry exits, strategy
// exits and the cash sweeps.
func scorecardRuleBucket(bucket string) bool {
	switch bucket {
	// "option_profit_trail" has no rpc constant yet; the owner named it as
	// a rule bucket, so a proposal that carries it is tagged when it exists.
	case rpc.TradeProposalBucketTrailingStop, rpc.TradeProposalBucketOptionLossExit, "option_profit_trail",
		rpc.TradeProposalBucketBudgetReduction, rpc.TradeProposalBucketOptionExpiryClose, rpc.TradeProposalBucketThetaHygiene,
		rpc.TradeProposalBucketRiskReduction, rpc.TradeProposalBucketStrategyExit, rpc.TradeProposalBucketOptionExitReview,
		rpc.TradeProposalBucketCashSweep, rpc.TradeProposalBucketCurrencyLeveling:
		return true
	default:
		return false
	}
}
