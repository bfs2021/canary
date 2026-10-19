package core

import "math"

// The replay (strategy-evaluator.md §9): the rule run over the month-ends
// of a stored daily series, as the lab's backtest runs it over its own, so
// the two tables can be compared decision by decision. It is the "did we
// build the right thing" check the owner asked for on 2026-10-19: the
// parity fixture proves the arithmetic on synthetic closes; the replay
// proves the daemon's data, calendar and rule make the decisions the lab's
// backtest records for the same spec on real month-ends. The paper path
// carries a holding from one decision to the next under the entry's band
// and minimum at a fixed, stated sizing, so the table's action column is
// the rule's, not the account's: no capital, no headroom, no mandate.

// ReplaySizing is the fixed sizing every replay runs at, the lab's
// (lab/replay.py REPLAY_SIZING, lab/core_fixture.py SIZING at scale 100):
// a 6% allocation of a 250,000 base at full room, capped at 40%. A number
// in the table is comparable only under the same sizing, so both sides
// print it.
var ReplaySizing = Sizing{AllocationPct: 6, ScalePct: 100, MaxValuePct: 40, BudgetBase: 250000, Room: 1}

// ReplayTerms are the entry's band and minimum the paper path applies.
type ReplayTerms struct {
	BandPct      float64 `json:"band_pct"`
	MinOrderBase float64 `json:"min_order_base"`
}

// ReplayRow is one decision of the replay: the month's signal, the target
// under ReplaySizing and what the paper path did with it.
type ReplayRow struct {
	Decision string `json:"decision"`
	Status   string `json:"status"`
	Reason   string `json:"reason,omitempty"`
	// The signal, as Signal carries it (zero unless Status is ok).
	WindowFrom     string    `json:"window_from,omitempty"`
	MonthEnds      []string  `json:"month_ends,omitempty"`
	MonthEndCloses []float64 `json:"month_end_closes,omitempty"`
	Close          float64   `json:"close,omitempty"`
	SMA10          float64   `json:"sma10,omitempty"`
	TrendOn        bool      `json:"trend_on"`
	SigmaEWMA      float64   `json:"sigma_ewma,omitempty"`
	Sigma252       float64   `json:"sigma_252,omitempty"`
	Sigma          float64   `json:"sigma,omitempty"`
	Binding        string    `json:"binding,omitempty"`
	// Notional and TargetShares are the target under ReplaySizing.
	Notional     float64 `json:"notional"`
	TargetShares int     `json:"target_shares"`
	// Action and Quantity are what the paper path does at this decision
	// (Decide under the terms, the decision close as the price, no
	// headroom); HeldBefore and HeldAfter the paper holding around it. A
	// hold or not_live month acts "hold" and keeps its holding.
	Action     string `json:"action"`
	Quantity   int    `json:"quantity,omitempty"`
	HeldBefore int    `json:"held_before"`
	HeldAfter  int    `json:"held_after"`
}

// Replay runs the rule under a trend filter at every month-end of the
// calendar from From to To inclusive (ascending ISO dates; the caller
// bounds To at the last decided month-end, since a running month has no
// decision) over the series' closes, carrying the paper holding from
// zero. Each decision is MeasureWith at that day, sized by SizeTarget
// under ReplaySizing and decided by Decide under the terms: the same three
// calls the daemon's monthly evaluation makes, so a disagreement with the
// lab's table is a disagreement about data, calendar or rule, never about
// a second implementation of the rule.
func Replay(trend Trend, label string, calendar, dates []string, closes []float64, from, to string, terms ReplayTerms) []ReplayRow {
	var rows []ReplayRow
	held := 0
	for _, d := range MonthEnds(calendar) {
		if d < from || d > to {
			continue
		}
		sig, _ := MeasureWith(trend, label, calendar, dates, closes, d)
		row := ReplayRow{Decision: d, Status: sig.Status, Reason: sig.Reason, WindowFrom: sig.WindowFrom, MonthEnds: sig.MonthEnds, MonthEndCloses: sig.MonthEndCloses,
			Close: sig.Close, SMA10: sig.SMA10, TrendOn: sig.TrendOn, SigmaEWMA: sig.SigmaEWMA, Sigma252: sig.Sigma252, Sigma: sig.Sigma, Binding: sig.Binding,
			Action: ActionHold, HeldBefore: held, HeldAfter: held}
		if sig.Status == StatusOK {
			size := SizeTarget(sig, ReplaySizing)
			row.Notional, row.TargetShares = size.Notional, size.TargetShares
			dec := Decide(DecideInput{Target: size.TargetShares, TrendOn: sig.TrendOn, Held: held, Price: sig.Close, BandPct: terms.BandPct, MinOrderBase: terms.MinOrderBase, Holdable: int(math.MaxInt32)})
			row.Action, row.Quantity = dec.Kind, dec.Quantity
			switch dec.Kind {
			case ActionBuy:
				held += dec.Quantity
			case ActionSell, ActionSellAll:
				held -= dec.Quantity
			}
			row.HeldAfter = held
		}
		rows = append(rows, row)
	}
	return rows
}
