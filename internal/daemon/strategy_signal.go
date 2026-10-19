package daemon

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/osauer/canary/v2/internal/core"
	"github.com/osauer/canary/v2/internal/mandate"
	"github.com/osauer/canary/v2/internal/marketcal"
	"github.com/osauer/canary/v2/internal/register"
	"github.com/osauer/canary/v2/internal/risk"
	"github.com/osauer/canary/v2/internal/rpc"
)

// An entry's signal series (strategy-evaluator.md §7; integrating session on
// the owner's delegated judgement, 2026-10-19 07:31 CEST): the trend and the
// volatility are the fund's, not one share class's, so an entry whose traded
// class is too young to fill the evaluator's window names another listed
// class of the same fund with enough history (signal_isin, signal_currency)
// and the currency its closes are measured in (signal_in). Until the traded
// line's own closes fill the window, the month is measured on that series,
// converted at each session's IDEALPRO close when it is listed in another
// currency than it is measured in, on its own listing's calendar; the
// target is sized, the limit checked and the kill rule marked on the traded
// line's own close. From the first decision whose window the traded line's
// closes fill, they take over, and the signal series is no longer read for
// the decision. A signal line that cannot be resolved, a series missing or
// stale, or a rate missing holds the entry, named; nothing falls back.

// strategySignalSeriesKey keys an entry's signal series by its id and the
// signal line's contract: its own namespace, never a book key.
func strategySignalSeriesKey(id string, conID int) string {
	return fmt.Sprintf("%s%s:signal:con:%d", strategySeriesKeyPrefix, id, conID)
}

// strategyFXNeed is the exchange-rate series that converts an entry's signal
// closes from the listing's currency to the one they are measured in.
func strategyFXNeed(id, from, to string) mandateHistoryNeed {
	return mandateHistoryNeed{Key: fmt.Sprintf("%s%s:fx:%s%s", strategySeriesKeyPrefix, id, from, to),
		Contract: rpc.ContractParams{Symbol: from, SecType: "CASH", Currency: to, Exchange: "IDEALPRO"}}
}

// strategyFXMaxGap is how far back a session's rate may be found: a rate
// series has no bar on a weekend, so the last close before the session
// within four days stands for it.
const strategyFXMaxGap = 4 * 24 * time.Hour

// strategyMeasure is the series a month is measured on when it is not the
// traded line's own: the signal line, its converted closes and calendar.
type strategyMeasure struct {
	Contract *coreContract
	Dates    []string
	Closes   []float64
	Market   marketcal.Market
	Label    string
	Evidence *rpc.CoreSignalEvidence
}

// strategyOwnSessions is how many of the window's sessions the traded
// line's own closes cover: the calendar's sessions from its first close to
// the decision day.
func strategyOwnSessions(series *mandateHistorySeries, decision string, now time.Time, market marketcal.Market) int {
	if series == nil || len(series.Dates) == 0 || decision == "" {
		return 0
	}
	sessions, _ := coreCalendar(series.Dates, decision, now, market)
	return len(sessions)
}

// strategySignalMeasure reads an entry's signal series for a month: the
// signal line as IBKR lists it, its closes under the entry's signal key,
// and the rate that converts them, each named in a hold when it is missing,
// stale or does not match its contract.
func (s *Server) strategySignalMeasure(ctx context.Context, r register.Resolved, name string, now time.Time) (*strategyMeasure, []string) {
	inst := r.Signal
	var holds []string
	hold := func(format string, args ...any) { holds = append(holds, fmt.Sprintf(format, args...)) }
	k, pinHold, lookupErr, asked := s.instrumentPinState(inst)
	switch {
	case k != nil:
	case pinHold != "":
		return nil, []string{"the signal line: " + pinHold}
	case lookupErr != "":
		return nil, []string{fmt.Sprintf("%s's signal line is not resolved: IBKR gave no answer for %s in %s (%s); the desk asks again on the background lane", name, inst.ISIN, inst.Currency, lookupErr)}
	case !asked:
		return nil, []string{fmt.Sprintf("%s's signal line is not resolved yet: the desk asks IBKR for %s in %s on the background lane", name, inst.ISIN, inst.Currency)}
	default:
		return nil, []string{fmt.Sprintf("%s's signal line %s in %s is not resolved", name, inst.ISIN, inst.Currency)}
	}
	ev := &rpc.CoreSignalEvidence{ISIN: inst.ISIN, Currency: k.Currency, MeasuredIn: r.SignalIn, Symbol: k.Symbol, ConID: k.ConID, Calendar: string(k.Market), InUse: true}
	need := instrumentSeriesNeed(strategySignalSeriesKey(r.Entry.ID, k.ConID), k)
	ev.SeriesKey = need.Key
	series := s.mandateHistoryForNeed(ctx, need)
	venue := nonEmptyString(marketcal.Label(k.Market), k.PrimaryExch)
	switch {
	case series == nil:
		hold("no daily history of the signal line %s yet; the desk reads it on %s on the background lane", k.Symbol, k.PrimaryExch)
	case series.Market != k.Market:
		hold("the kept history of the signal line %s was read for another calendar than %s; the desk reads it again", k.Symbol, venue)
		series = nil
	case series.Contract.ConID > 0 && series.Contract.ConID != k.ConID:
		hold("the signal series' contract %d is not the signal line's %d", series.Contract.ConID, k.ConID)
		series = nil
	case mandateSeriesStale(series, now):
		hold("the signal line %s's daily history is stale: last close %s on the %s calendar", k.Symbol, series.Dates[len(series.Dates)-1], venue)
		series = nil
	}
	if series == nil {
		return nil, holds
	}
	ev.Closes, ev.LastClose = len(series.Closes), series.Dates[len(series.Dates)-1]
	m := &strategyMeasure{Contract: k, Market: k.Market, Dates: series.Dates, Closes: series.Closes, Evidence: ev,
		Label: "|signal:" + inst.ISIN + ":" + k.Currency + ":" + r.SignalIn}
	if r.SignalIn == "" || r.SignalIn == k.Currency {
		return m, holds
	}
	fxNeed := strategyFXNeed(r.Entry.ID, k.Currency, r.SignalIn)
	ev.FXKey = fxNeed.Key
	fx := s.mandateHistoryForNeed(ctx, fxNeed)
	pair := k.Currency + "." + r.SignalIn
	switch {
	case fx == nil:
		hold("no daily %s rate yet to measure the signal line %s in %s; the desk reads it on IDEALPRO on the background lane", pair, k.Symbol, r.SignalIn)
		return nil, holds
	case mandateSeriesStale(fx, now):
		hold("the %s rate series is stale: last close %s", pair, fx.Dates[len(fx.Dates)-1])
		return nil, holds
	}
	closes := make([]float64, len(series.Closes))
	for i, d := range series.Dates {
		rate, ok := strategyRateOn(fx, d)
		if !ok {
			// Only a session inside the evaluator's window must convert:
			// an older one is dropped from the measured series.
			if len(series.Dates)-i <= core.WindowCloses {
				hold("no %s rate on or within four days before %s to measure the signal line %s in %s", pair, d, k.Symbol, r.SignalIn)
				return nil, holds
			}
			closes[i] = 0
			continue
		}
		closes[i] = series.Closes[i] * rate
	}
	// Sessions before the first convertible one are left out, never guessed.
	first := slices.IndexFunc(closes, func(v float64) bool { return v > 0 })
	if first < 0 {
		hold("no %s rate covers the signal line %s's closes", pair, k.Symbol)
		return nil, holds
	}
	m.Dates, m.Closes = series.Dates[first:], closes[first:]
	return m, holds
}

// strategyRateOn is a rate series' close on a date, or its last close before
// it within strategyFXMaxGap.
func strategyRateOn(fx *mandateHistorySeries, date string) (float64, bool) {
	day, err := time.Parse(time.DateOnly, date)
	if err != nil {
		return 0, false
	}
	i, found := slices.BinarySearch(fx.Dates, date)
	if found {
		return fx.Closes[i], fx.Closes[i] > 0
	}
	if i == 0 {
		return 0, false
	}
	prev, err := time.Parse(time.DateOnly, fx.Dates[i-1])
	if err != nil || day.Sub(prev) > strategyFXMaxGap {
		return 0, false
	}
	return fx.Closes[i-1], fx.Closes[i-1] > 0
}

// strategyLineCloseOn is the traded line's own close on or before a date in
// its series, with that session's date.
func strategyLineCloseOn(series *mandateHistorySeries, date string) (float64, string, bool) {
	if series == nil {
		return 0, "", false
	}
	i, found := slices.BinarySearch(series.Dates, date)
	if !found {
		i--
	}
	if i < 0 || i >= len(series.Dates) || !(series.Closes[i] > 0) {
		return 0, "", false
	}
	return series.Closes[i], series.Dates[i], true
}

// refreshStrategySignal reads an entry's signal line, its series and its
// rate series on the background lane, after the verdict, while it is
// written: every refresh, so the series is fresh the month the traded
// line's own closes still do not fill the window.
func (s *Server) refreshStrategySignal(ctx context.Context, r register.Resolved) {
	if r.Signal.ISIN == "" {
		return
	}
	k := s.resolveInstrument(ctx, r.Signal, strategyName(r.Entry)+"'s signal line", strategyTable(r.Entry))
	conn := s.gatewayConnector()
	if k == nil || conn == nil {
		return
	}
	s.refreshMandateSeries(ctx, conn, instrumentSeriesNeed(strategySignalSeriesKey(r.Entry.ID, k.ConID), k))
	if r.SignalIn != "" && r.SignalIn != k.Currency {
		s.refreshMandateSeries(ctx, conn, strategyFXNeed(r.Entry.ID, k.Currency, r.SignalIn))
	}
}

// strategyRiskSeries is the series the mandate measures an entry's line on:
// its own closes once they hold the history the mandate needs and are
// fresh, else, while the line is young, its signal series, the same fund's
// older class, whose returns stand for the line's as SPY's stand for the
// core's fund (mandate.ReturnsProxies); nil while neither can be read. from
// names the signal line when it stands in.
func (s *Server) strategyRiskSeries(ctx context.Context, r register.Resolved, k *coreContract, now time.Time) (series *mandateHistorySeries, from string) {
	if k == nil {
		return nil, ""
	}
	own := s.mandateHistorySeries(ctx, seriesKeyFor(r, k.ConID))
	if own != nil && !mandateSeriesStale(own, now) && len(own.Closes) >= mandate.Closes || r.Signal.ISIN == "" {
		return own, ""
	}
	m, holds := s.strategySignalMeasure(ctx, r, strategyName(r.Entry), now)
	if m == nil || len(holds) > 0 || len(m.Closes) < mandate.Closes {
		return own, ""
	}
	fetched := now
	if raw := s.mandateHistorySeries(ctx, m.Evidence.SeriesKey); raw != nil {
		fetched = raw.FetchedAt
	}
	return &mandateHistorySeries{Version: mandateHistoryDocVer, Key: m.Evidence.SeriesKey, Dates: m.Dates, Closes: m.Closes, FetchedAt: fetched, Source: "signal series", Market: m.Market,
		Contract: rpc.ContractParams{ConID: m.Contract.ConID, Symbol: m.Contract.Symbol, SecType: "STK", Currency: m.Contract.Currency, Exchange: m.Contract.PrimaryExch, PrimaryExch: m.Contract.PrimaryExch}}, m.Contract.Symbol
}

// withStrategyRiskSeries places the register's young lines on their signal
// series in a book the mandate measures: every entry line the book holds
// whose own series is missing or short of the history the mandate needs.
// The evidence names each stand-in.
func (s *Server) withStrategyRiskSeries(ctx context.Context, resolved []register.Resolved, in *mandate.Input, evidence *rpc.MandateEvidence, now time.Time) {
	for _, r := range resolved {
		if r.Entry.Form == risk.StrategyFormCore || r.Signal.ISIN == "" {
			continue
		}
		k, _, _, _ := s.instrumentPinState(s.requestedInstrument(r))
		if k == nil || !slices.ContainsFunc(in.Legs, func(l mandate.Leg) bool { return mandateLegConID(l.ID) == k.ConID }) {
			continue
		}
		if kept, ok := in.Series[k.Symbol]; ok && len(kept.Closes) >= mandate.Closes && !kept.Stale {
			continue
		}
		series, from := s.strategyRiskSeries(ctx, r, k, now)
		if from == "" || series == nil {
			continue
		}
		in.Series[k.Symbol] = mandateSeriesInput(series, now)
		if evidence != nil {
			evidence.Series = append(evidence.Series, rpc.MandateSeriesEvidence{Key: series.Key, Closes: len(series.Closes), LastClose: series.Dates[len(series.Dates)-1], FetchedAt: series.FetchedAt,
				Source: "returns of " + from + ", standing in for " + k.Symbol + " until its own closes fill the history"})
		}
	}
}
