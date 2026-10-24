package daemon

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/osauer/canary/v2/internal/chainrec"
	edgecore "github.com/osauer/canary/v2/internal/edge"
	"github.com/osauer/canary/v2/internal/flexstmt"
	"github.com/osauer/canary/v2/internal/risk"
	"github.com/osauer/canary/v2/internal/rpc"
)

// replayHistoryLeadDays is how far before the window the underlying history
// must reach: the stock trail's 20-session average true range needs about a
// month of sessions before the first day.
const replayHistoryLeadDays = 45

// handleEdgeReplay replays long-option exit rules over the owner's own option
// trades (long-option-exits LO-V1). It reads only what Canary retains: the
// Flex statement projection Edge reads, the cached daily history of each
// underlying, the recorded option chains and the measured fill costs. The
// rule numbers come from the Rulebook and protection policies in force. It
// is read-only and never reaches the order path.
func (s *Server) handleEdgeReplay(ctx context.Context, req *rpc.Request) (*rpc.EdgeReplayResult, error) {
	var params rpc.EdgeReplayParams
	if err := decodeParams(req.Params, &params); err != nil {
		return nil, err
	}
	params, err := rpc.NormalizeEdgeReplayParams(params)
	if err != nil {
		return nil, errBadRequest(err.Error())
	}
	stateOnly := func(state, reason string) *rpc.EdgeReplayResult {
		return &rpc.EdgeReplayResult{SchemaVersion: rpc.EdgeReplaySchemaVersion, State: state, Reason: reason, NotExecution: true,
			Runs: []rpc.EdgeReplayRun{}, Scope: rpc.EdgeReplayScope{Underlyings: []string{}, ExcludedOther: map[string]int{}},
			Reconciliation: rpc.EdgeReplayReconciliation{ReconciliationNotes: []string{}}}
	}
	if s.cfg == nil || !s.cfg.Flex.Enabled || strings.TrimSpace(s.cfg.Flex.QueryID) == "" {
		return stateOnly(rpc.EdgeStateActionRequired, "flex_configuration_required"), nil
	}
	if s.coreStore == nil {
		return nil, fmt.Errorf("edge authority is unavailable")
	}
	scope := s.currentBrokerStateScope()
	if !brokerScopeConcrete(scope) {
		return stateOnly(rpc.EdgeStateUnavailable, "account_scope_unavailable"), nil
	}
	loadCtx, cancel := context.WithTimeout(ctx, edgeStatementLoadTimeout)
	projected, err := s.loadEdgeProjectionEvidence(loadCtx, scope, edgeScopeFingerprint(scope))
	cancel()
	if err != nil {
		return nil, err
	}
	statements := projected.Statements
	if len(statements) == 0 {
		return stateOnly(rpc.EdgeStateBackfilling, "statement_account_pending"), nil
	}
	var from, to time.Time
	if params.From != "" {
		from, _ = time.Parse(time.DateOnly, params.From)
	}
	if params.To != "" {
		to, _ = time.Parse(time.DateOnly, params.To)
	}

	policy, _ := s.protectionPolicies.Active()
	rulebook, rulebookStatus := s.activeRulebookPolicy()
	options := policy.Buckets.TrailingStop.Options
	replayPolicy := edgecore.ExitReplayPolicy{
		Exit: risk.OptionExitPolicy{
			MinDTE: options.MinDTE, LossExitPct: rulebook.ExitActLossPct, ProfitArmGainPct: options.ProfitArmGainPct,
			ProfitTrailPct: options.DefaultPct, LockedGainPct: options.LockedGainPct, MinTrailPct: options.MinPct, MaxTrailPct: options.MaxPct,
			MaxSpreadPctOfMid: options.MaxSpreadPctOfMid, MinTrailAbs: options.MinTrailAbs, SpreadMultiple: options.SpreadMultiple,
			ExpiryCloseDTE: rulebook.RunwayActDTE,
		},
		RecoverCostGainPct: options.RecoverCostGainPct, OptionNameActPct: rulebook.OptionNameActPct,
		StockTrailRangeMultiple: options.StockTrailRangeMultiple, HedgeSymbols: slices.Clone(rulebook.HedgeSymbols),
		Source: fmt.Sprintf("Rulebook %s v%d (%s) · protection %s v%d", rulebookStatus.PolicyID, rulebookStatus.PolicyVersion, rulebookStatus.Source, policy.PolicyID, policy.PolicyVersion),
	}

	underlyings := edgecore.ExitReplayUnderlyings(statements, from, to, replayPolicy.HedgeSymbols)
	historyRange := replayHistoryRange(statements, from, s.edgeNow())
	bars := map[string][]edgecore.UnderlyingBar{}
	symbols := make([]string, 0, len(underlyings))
	for _, underlying := range underlyings {
		symbols = append(symbols, underlying.Symbol)
		series, readErr := s.replayUnderlyingBars(ctx, underlying, historyRange)
		if readErr != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			// A missing history leaves that underlying's days untestable;
			// the coverage counts say so.
			continue
		}
		bars[underlying.Symbol] = series
	}
	chainMarks := s.replayChainMarks(ctx, symbols, from, to)
	halfSpread, samples, source := s.replayHalfSpread()

	result := edgecore.ExitReplay(edgecore.ExitReplayInput{
		Statements: statements, BaseCurrency: inferEdgeBaseCurrency(statements), From: from, To: to, Rules: params.Rules,
		Policy: replayPolicy, Underlyings: bars, ChainMarks: chainMarks,
		HalfSpreadPct: halfSpread, HalfSpreadSamples: samples, HalfSpreadSource: source,
	})
	if !sameBrokerScope(scope, s.currentBrokerStateScope()) {
		return stateOnly(rpc.EdgeStateUnavailable, "account_scope_changed"), nil
	}
	if err := rpc.ValidateEdgeReplayResult(result); err != nil {
		return nil, fmt.Errorf("invalid Edge replay: %w", err)
	}
	return &result, nil
}

// replayHistoryRange picks the shortest daily-history range that reaches
// replayHistoryLeadDays before the window's first day, so a range already
// cached for the market view is reused.
func replayHistoryRange(statements []flexstmt.Statement, from, now time.Time) string {
	first := from
	if first.IsZero() {
		for _, st := range statements {
			if !st.FromDate.IsZero() && (first.IsZero() || st.FromDate.Before(first)) {
				first = st.FromDate
			}
		}
	}
	need := first.AddDate(0, 0, -replayHistoryLeadDays)
	for _, label := range []string{"6M", "1Y", "5Y"} {
		days, _, err := marketHistoryWindow(label, now)
		if err == nil && !now.AddDate(0, 0, -days).After(need) {
			return label
		}
	}
	return "5Y"
}

// replayUnderlyingBars reads one underlying's daily bars through the market
// history path: the retained series when one exists, else the same bounded
// fetch the market view makes.
func (s *Server) replayUnderlyingBars(ctx context.Context, underlying edgecore.ExitReplayUnderlying, historyRange string) ([]edgecore.UnderlyingBar, error) {
	currency := underlying.Currency
	if currency == "" {
		currency = "USD"
	}
	p := rpc.MarketHistoryParams{Contract: rpc.ContractParams{Symbol: underlying.Symbol, SecType: "STK", Exchange: "SMART", Currency: currency}, Range: historyRange}
	key, normalized, err := marketHistoryIdentity(p)
	if err != nil {
		return nil, err
	}
	var result *rpc.MarketHistoryResult
	if saved, storedAt, loadErr := s.loadMarketHistory(ctx, key); loadErr != nil {
		return nil, loadErr
	} else if saved != nil {
		result = selectStoredHistory(saved, storedAt, normalized, s.edgeNow(), "cache", "", false)
	} else if result, err = s.marketHistoryRequest(ctx, p); err != nil {
		return nil, err
	}
	if result == nil {
		return nil, fmt.Errorf("no history for %s", underlying.Symbol)
	}
	out := make([]edgecore.UnderlyingBar, 0, len(result.Points))
	for _, point := range result.Points {
		if point.Value <= 0 || point.At.IsZero() {
			continue
		}
		y, m, d := point.At.UTC().Date()
		bar := edgecore.UnderlyingBar{Day: time.Date(y, m, d, 0, 0, 0, 0, time.UTC), Close: point.Value}
		if point.High != nil {
			bar.High = *point.High
		}
		if point.Low != nil {
			bar.Low = *point.Low
		}
		out = append(out, bar)
	}
	return out, nil
}

// replayChainMarks reads the recorded two-sided quotes of the underlyings in
// the window. A recorder that never ran supplies none.
func (s *Server) replayChainMarks(ctx context.Context, symbols []string, from, to time.Time) []edgecore.RecordedOptionMark {
	store, err := s.chainRecorderStoreHandle(ctx, false)
	if err != nil || store == nil || len(symbols) == 0 {
		return nil
	}
	first, last := "0000-01-01", "9999-12-31"
	if !from.IsZero() {
		first = from.AddDate(0, 0, -10).Format(time.DateOnly)
	}
	if !to.IsZero() {
		last = to.Format(time.DateOnly)
	}
	quotes, err := store.TwoSidedQuotes(ctx, first, last, symbols)
	if err != nil {
		return nil
	}
	return replayMarksFromQuotes(quotes)
}

func replayMarksFromQuotes(quotes []chainrec.TwoSidedQuote) []edgecore.RecordedOptionMark {
	out := make([]edgecore.RecordedOptionMark, 0, len(quotes))
	for _, q := range quotes {
		day, err := time.Parse(time.DateOnly, q.SessionDate)
		if err != nil {
			continue
		}
		out = append(out, edgecore.RecordedOptionMark{ConID: q.ConID, Underlying: q.Symbol, Expiry: q.Expiry, Strike: q.Strike, Right: q.Right, Day: day, Bid: q.Bid, Ask: q.Ask})
	}
	return out
}

// replayHalfSpread is the median fill-vs-mid half-spread of the retained
// measured option fills (the same records canary reporting costs reads), in
// percent of the fill price.
func (s *Server) replayHalfSpread() (*float64, int, string) {
	const none = "retained execution costs hold no measured option fill; rule exits sell at the mark"
	s.mu.Lock()
	ep := s.endpoint
	s.mu.Unlock()
	status := s.fillQuoteAccountStatus(ep)
	if s.fillQuotes == nil || !brokerScopeAccountConcrete(status.Account) {
		return nil, 0, none
	}
	var pcts []float64
	for _, record := range s.fillQuotes.list(status.Account, status.Mode) {
		fill := costFillFromRecord(record)
		if fill.SecType != "OPT" || fill.Status != rpc.CostFillMeasured || fill.HalfSpread == nil || fill.Price <= 0 || fill.Quantity <= 0 || fill.Multiplier <= 0 {
			continue
		}
		pcts = append(pcts, *fill.HalfSpread/(fill.Price*fill.Quantity*fill.Multiplier)*100)
	}
	if len(pcts) == 0 {
		return nil, 0, none
	}
	slices.Sort(pcts)
	median := pcts[len(pcts)/2]
	if len(pcts)%2 == 0 {
		median = (pcts[len(pcts)/2-1] + pcts[len(pcts)/2]) / 2
	}
	return &median, len(pcts), fmt.Sprintf("median fill-vs-mid half-spread of %d measured option fills in the retained execution costs (canary reporting costs)", len(pcts))
}
