package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/osauer/canary/v2/internal/daemon/corestore"
	edgecore "github.com/osauer/canary/v2/internal/edge"
	"github.com/osauer/canary/v2/internal/rpc"
	ibkrlib "github.com/osauer/canary/v2/pkg/ibkr"
)

// The exit replay's daily history (internal-docs/design/long-option-exits.md,
// LO-V1). A replay must give the same answer tomorrow without the gateway, so
// it reads retained history before the broker and keeps what it used:
//
//   - its own retained bars, one state document per underlying, written by
//     every replay that read more than it held and never evicted;
//   - the mandate's decision-grade closes (mandate_history.go): retained and
//     never evicted, but only for names held now, only the newest 320
//     sessions, and closes without a high or low;
//   - the market-history chart cache: high and low included, but a display
//     cache that evicts its oldest series at 750 (corestore/chart_cache.go);
//   - the broker, through the chart path, only when the retained sources do
//     not span the days the name needs.
//
// Neither existing store could hold this without changing its contract: the
// mandate's series is a bounded rolling window its verdict judges freshness
// on, and the chart cache's eviction is what bounds it. The replay's document
// is the union of the bars it read, so later runs only add to it.

const (
	stateKindReplayHistory = "edge_replay_history"
	replayHistoryDocVer    = 1
	// replayHistoryKeep bounds one document at about ten years of sessions.
	replayHistoryKeep = 2600
	// replayRestatedPct is the close difference on an overlapping day that
	// marks a fresher read as a restatement (a split): what was read before
	// it is then dropped rather than spliced onto a different basis.
	replayRestatedPct = 1.0
	// replaySameClosePct is the difference under which a close-only bar
	// leaves an earlier bar's high and low in place.
	replaySameClosePct = 0.5
)

// replayHistorySource is the method line the result carries.
const replayHistorySource = "Daily bars come from the history earlier replays retained, then the mandate's decision-grade closes (no high or low), then the market-history cache, and from the broker only when those do not span the days a name needs; a cash-index underlying is read as the index. The bars used are retained, so a later run without the gateway gives the same answer."

// replayHistoryDoc is one underlying's retained daily history.
type replayHistoryDoc struct {
	Version  int                `json:"version"`
	Contract rpc.ContractParams `json:"contract"`
	// AsOf is the newest source time of the bars merged into it.
	AsOf time.Time          `json:"as_of"`
	Bars []replayHistoryBar `json:"bars"`
}

type replayHistoryBar struct {
	Day   string  `json:"day"`
	Close float64 `json:"close"`
	High  float64 `json:"high,omitempty"`
	Low   float64 `json:"low,omitempty"`
}

// replaySource is one read of an underlying's daily bars and its source
// time.
type replaySource struct {
	asOf time.Time
	bars map[string]edgecore.UnderlyingBar
}

// replayHistoryContract is the contract an underlying's history is read
// for: a cash index (index names it) as the index, with the identity the
// market tape and the market view's index references use so their retained
// series serves the replay; every other underlying as a stock.
func replayHistoryContract(underlying edgecore.ExitReplayUnderlying, index func(string) (rpc.ContractParams, bool)) rpc.ContractParams {
	currency := underlying.Currency
	if currency == "" {
		currency = "USD"
	}
	if contract, ok := index(underlying.Symbol); ok {
		contract.PrimaryExch = ""
		return contract
	}
	return rpc.ContractParams{Symbol: underlying.Symbol, SecType: "STK", Exchange: "SMART", Currency: currency}
}

func replayHistoryScope(c rpc.ContractParams) string {
	sum := sha256.Sum256([]byte(c.SecType + "|" + c.Symbol + "|" + c.Currency))
	return "edge/replay/history/" + hex.EncodeToString(sum[:])
}

// replayHistory returns one underlying's daily bars and, when they do not
// span the days it needs, why. Retained sources come first; the broker is
// asked only when they fall short, and the merged bars are kept.
func (s *Server) replayHistory(ctx context.Context, underlying edgecore.ExitReplayUnderlying, historyRange string) ([]edgecore.UnderlyingBar, edgecore.ExitReplayHistoryNote) {
	contract := replayHistoryContract(underlying, rpc.IndexUnderlyingContract)
	retained, revision := s.loadReplayHistory(ctx, contract)
	sources := []replaySource{retained}
	if contract.SecType == "STK" {
		if series := s.mandateHistorySeries(ctx, "u:"+contract.Symbol); series != nil && (series.Contract.Currency == "" || series.Contract.Currency == contract.Currency) {
			src := replaySource{asOf: series.FetchedAt, bars: map[string]edgecore.UnderlyingBar{}}
			for i, day := range series.Dates {
				if at, err := time.Parse(time.DateOnly, day); err == nil {
					src.bars[day] = edgecore.UnderlyingBar{Day: at, Close: series.Closes[i]}
				}
			}
			sources = append(sources, src)
		}
	}
	p := rpc.MarketHistoryParams{Contract: contract, Range: historyRange}
	note := edgecore.ExitReplayHistoryNote{}
	key, _, identityErr := marketHistoryIdentity(p)
	if identityErr == nil {
		if saved, _, err := s.loadMarketHistory(ctx, key); err == nil && saved != nil {
			sources = append(sources, replayHistorySourceOf(saved.Result))
		}
	}
	merged := mergeReplaySources(sources)
	first := underlying.First.AddDate(0, 0, -replayHistoryLeadDays)
	if !edgecore.ExitReplayHistoryCovers(merged, first, underlying.Last) {
		switch {
		case identityErr != nil:
			note = edgecore.ExitReplayHistoryNote{Reason: rpc.EdgeReplayHistoryInstrumentUnavailable, Detail: identityErr.Error()}
		case s.gatewayConnector() == nil:
			note = edgecore.ExitReplayHistoryNote{Reason: rpc.EdgeReplayHistoryGatewayOffline, Detail: replayHeldText(merged)}
		default:
			result, err := s.marketHistoryRequest(ctx, p)
			switch {
			case err != nil && errors.Is(err, ibkrlib.ErrIBKRUnavailable):
				note = edgecore.ExitReplayHistoryNote{Reason: rpc.EdgeReplayHistoryGatewayOffline, Detail: replayHeldText(merged)}
			case err != nil && (marketHistoryVerdict(err) || errors.Is(err, ibkrlib.ErrNoListing)):
				note = edgecore.ExitReplayHistoryNote{Reason: rpc.EdgeReplayHistoryInstrumentUnavailable, Detail: errText(err)}
			case err != nil:
				note = edgecore.ExitReplayHistoryNote{Reason: rpc.EdgeReplayHistoryReadFailed, Detail: errText(err)}
			case result != nil && result.Cache != nil && result.Cache.RefreshFailed:
				note = edgecore.ExitReplayHistoryNote{Reason: rpc.EdgeReplayHistoryReadFailed, Detail: "the broker's read failed; " + replayHeldText(merged)}
			case result != nil:
				sources = append(sources, replayHistorySourceOf(*result))
				merged = mergeReplaySources(sources)
				note = edgecore.ExitReplayHistoryNote{Reason: rpc.EdgeReplayHistoryNotRetained, Detail: replayHeldText(merged)}
			}
		}
	}
	s.saveReplayHistory(ctx, contract, retained, revision, merged, sources)
	if edgecore.ExitReplayHistoryCovers(merged, underlying.First, underlying.Last) {
		note = edgecore.ExitReplayHistoryNote{}
	} else if note.Reason == "" {
		note = edgecore.ExitReplayHistoryNote{Reason: rpc.EdgeReplayHistoryNotRetained, Detail: replayHeldText(merged)}
	}
	return merged, note
}

// replayHeldText says what daily history Canary holds for a name.
func replayHeldText(bars []edgecore.UnderlyingBar) string {
	if len(bars) == 0 {
		return "no retained history"
	}
	return fmt.Sprintf("history from %s to %s", bars[0].Day.Format(time.DateOnly), bars[len(bars)-1].Day.Format(time.DateOnly))
}

func replayHistorySourceOf(result rpc.MarketHistoryResult) replaySource {
	src := replaySource{asOf: result.AsOf, bars: map[string]edgecore.UnderlyingBar{}}
	if result.Interval != "1 day" {
		return src
	}
	for _, point := range result.Points {
		if !isFinitePrice(point.Value) || point.At.IsZero() {
			continue
		}
		y, m, d := point.At.UTC().Date()
		bar := edgecore.UnderlyingBar{Day: time.Date(y, m, d, 0, 0, 0, 0, time.UTC), Close: point.Value}
		if point.High != nil && point.Low != nil {
			bar.High, bar.Low = *point.High, *point.Low
		}
		src.bars[bar.Day.Format(time.DateOnly)] = bar
	}
	return src
}

// mergeReplaySources merges reads oldest source time first: a fresher bar
// replaces an older one, except that a close-only bar with the same close
// keeps the older bar's high and low, and a fresher read that restates an
// overlapping close drops everything read before it.
func mergeReplaySources(sources []replaySource) []edgecore.UnderlyingBar {
	ordered := slices.Clone(sources)
	slices.SortStableFunc(ordered, func(a, b replaySource) int { return a.asOf.Compare(b.asOf) })
	acc := map[string]edgecore.UnderlyingBar{}
	for _, src := range ordered {
		for day, bar := range src.bars {
			if old, ok := acc[day]; ok && math.Abs(bar.Close-old.Close)/old.Close*100 > replayRestatedPct {
				clear(acc)
				break
			}
		}
		for day, bar := range src.bars {
			old, ok := acc[day]
			if ok && bar.High <= 0 && old.High > 0 && math.Abs(bar.Close-old.Close)/old.Close*100 <= replaySameClosePct {
				continue
			}
			acc[day] = bar
		}
	}
	out := make([]edgecore.UnderlyingBar, 0, len(acc))
	for _, day := range slices.Sorted(maps.Keys(acc)) {
		out = append(out, acc[day])
	}
	return out
}

func (s *Server) loadReplayHistory(ctx context.Context, contract rpc.ContractParams) (replaySource, int64) {
	src := replaySource{bars: map[string]edgecore.UnderlyingBar{}}
	if s.coreStore == nil {
		return src, 0
	}
	doc, ok, err := s.coreStore.GetStateDocument(ctx, replayHistoryScope(contract), stateKindReplayHistory)
	if err != nil || !ok {
		return src, 0
	}
	var saved replayHistoryDoc
	if json.Unmarshal(doc.JSON, &saved) != nil || saved.Version != replayHistoryDocVer || saved.Contract.Symbol != contract.Symbol || saved.Contract.SecType != contract.SecType || saved.Contract.Currency != contract.Currency {
		return src, doc.Revision
	}
	src.asOf = saved.AsOf
	for _, bar := range saved.Bars {
		at, err := time.Parse(time.DateOnly, bar.Day)
		if err != nil || !isFinitePrice(bar.Close) {
			continue
		}
		src.bars[bar.Day] = edgecore.UnderlyingBar{Day: at, Close: bar.Close, High: bar.High, Low: bar.Low}
	}
	return src, doc.Revision
}

// saveReplayHistory keeps the merged bars when they differ from the retained
// ones. A failed save leaves this run's answer unchanged and is logged.
func (s *Server) saveReplayHistory(ctx context.Context, contract rpc.ContractParams, retained replaySource, revision int64, merged []edgecore.UnderlyingBar, sources []replaySource) {
	if s.coreStore == nil || len(merged) == 0 {
		return
	}
	if len(merged) > replayHistoryKeep {
		merged = merged[len(merged)-replayHistoryKeep:]
	}
	same := len(merged) == len(retained.bars)
	doc := replayHistoryDoc{Version: replayHistoryDocVer, Contract: rpc.ContractParams{Symbol: contract.Symbol, SecType: contract.SecType, Exchange: contract.Exchange, Currency: contract.Currency}}
	for _, src := range sources {
		if src.asOf.After(doc.AsOf) {
			doc.AsOf = src.asOf
		}
	}
	for _, bar := range merged {
		day := bar.Day.Format(time.DateOnly)
		if old, ok := retained.bars[day]; !ok || old.Close != bar.Close || old.High != bar.High || old.Low != bar.Low {
			same = false
		}
		doc.Bars = append(doc.Bars, replayHistoryBar{Day: day, Close: bar.Close, High: bar.High, Low: bar.Low})
	}
	if same {
		return
	}
	raw, err := json.Marshal(doc)
	if err == nil {
		_, err = s.coreStore.CompareAndSwapStateDocument(ctx, corestore.StateDocumentCAS{ScopeKey: replayHistoryScope(contract), Kind: stateKindReplayHistory, ExpectedRevision: revision, JSON: raw})
	}
	if err != nil && s.logger != nil {
		s.logger.Warnf("edge replay: keeping %d daily bars of %s failed: %v; this run's answer stands, the next run reads again", len(doc.Bars), strings.ToUpper(contract.Symbol), err)
	}
}
