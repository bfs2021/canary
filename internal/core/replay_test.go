package core

import (
	"encoding/json"
	"slices"
	"testing"
)

// The replay over the parity fixture's months is the fixture's decisions
// (status and reason, the window, the trend, the volatility and the target
// at scale 100 under ReplaySizing), decided in order with the paper
// holding carried: the first buy when the trend is on, a sale of every
// share when it turns off, nothing inside the band.
func TestReplayReproducesTheParityFixtureAndCarriesThePaperHolding(t *testing.T) {
	f := loadParity(t)
	last := f.Decisions[len(f.Decisions)-1].Decision
	rows := Replay(TrendSMA10, CoreLabel, f.Calendar, f.Dates, f.Closes, f.Calendar[0], last, ReplayTerms{BandPct: 10, MinOrderBase: 2500})
	if len(rows) != len(f.Decisions) {
		t.Fatalf("%d rows for %d fixture decisions", len(rows), len(f.Decisions))
	}
	held := 0
	var kinds []string
	for i, want := range f.Decisions {
		row := rows[i]
		if row.Decision != want.Decision || row.Status != want.Status || row.Reason != want.Reason {
			t.Fatalf("row %d: %s %s %q, fixture %s %s %q", i, row.Decision, row.Status, row.Reason, want.Decision, want.Status, want.Reason)
		}
		if row.HeldBefore != held {
			t.Fatalf("%s: held before %d, carried %d", row.Decision, row.HeldBefore, held)
		}
		if want.Status != StatusOK {
			if row.Action != ActionHold || row.HeldAfter != held || row.TargetShares != 0 {
				t.Fatalf("%s: a %s month acts %s, target %d, held %d", row.Decision, want.Status, row.Action, row.TargetShares, row.HeldAfter)
			}
			continue
		}
		if row.WindowFrom != want.WindowFrom || !slices.Equal(row.MonthEnds, want.MonthEnds) || row.TrendOn != want.TrendOn || row.Binding != want.Binding {
			t.Fatalf("%s: window %s month-ends %v trend %v binding %s; fixture %s %v %v %s", row.Decision, row.WindowFrom, row.MonthEnds, row.TrendOn, row.Binding, want.WindowFrom, want.MonthEnds, want.TrendOn, want.Binding)
		}
		for _, c := range []struct {
			name      string
			got, want float64
		}{{"close", row.Close, want.Close}, {"sma10", row.SMA10, want.SMA10}, {"sigma_ewma", row.SigmaEWMA, want.SigmaEWMA}, {"sigma_252", row.Sigma252, want.Sigma252}, {"sigma", row.Sigma, want.Sigma}} {
			if !relClose(c.got, c.want) {
				t.Fatalf("%s: %s %.17g, fixture %.17g", row.Decision, c.name, c.got, c.want)
			}
		}
		ws, ok := want.Sizing[json.Number("100").String()]
		if !ok {
			t.Fatalf("%s: the fixture carries no sizing at scale 100", want.Decision)
		}
		if row.TargetShares != ws.TargetShares || !relClose(row.Notional, ws.Notional) {
			t.Fatalf("%s: target %d notional %.17g, fixture %d %.17g", row.Decision, row.TargetShares, row.Notional, ws.TargetShares, ws.Notional)
		}
		// The paper path.
		kinds = append(kinds, row.Action)
		switch {
		case !row.TrendOn && held > 0:
			if row.Action != ActionSellAll || row.Quantity != held || row.HeldAfter != 0 {
				t.Fatalf("%s: trend off holding %d acts %s %d, held after %d", row.Decision, held, row.Action, row.Quantity, row.HeldAfter)
			}
		case held == 0 && row.TargetShares > 0:
			if row.Action != ActionBuy || row.Quantity != row.TargetShares || row.HeldAfter != row.TargetShares {
				t.Fatalf("%s: first buy acts %s %d, held after %d", row.Decision, row.Action, row.Quantity, row.HeldAfter)
			}
		case row.Action == ActionNone:
			if row.HeldAfter != held {
				t.Fatalf("%s: none moved the holding %d to %d", row.Decision, held, row.HeldAfter)
			}
		case row.Action == ActionBuy:
			if row.HeldAfter != held+row.Quantity || row.HeldAfter != row.TargetShares {
				t.Fatalf("%s: buy %d from %d gives %d, target %d", row.Decision, row.Quantity, held, row.HeldAfter, row.TargetShares)
			}
		case row.Action == ActionSell:
			if row.HeldAfter != held-row.Quantity || row.HeldAfter != row.TargetShares {
				t.Fatalf("%s: sell %d from %d gives %d, target %d", row.Decision, row.Quantity, held, row.HeldAfter, row.TargetShares)
			}
		}
		held = row.HeldAfter
	}
	for _, k := range []string{ActionBuy, ActionSellAll, ActionNone} {
		if !slices.Contains(kinds, k) {
			t.Fatalf("the fixture's paper path never %s: %v", k, kinds)
		}
	}
}

// The span bounds the decisions: nothing before From, nothing after To,
// and a month whose window is not filled is a not_live row, not a gap.
func TestReplayKeepsToItsSpan(t *testing.T) {
	f := loadParity(t)
	ends := MonthEnds(f.Calendar)
	from, to := ends[3], ends[len(ends)-5]
	rows := Replay(TrendSMA10, CoreLabel, f.Calendar, f.Dates, f.Closes, from, to, ReplayTerms{BandPct: 10})
	if len(rows) == 0 || rows[0].Decision != from || rows[len(rows)-1].Decision != to {
		t.Fatalf("rows %v for %s to %s", rows, from, to)
	}
	if rows[0].Status != StatusNotLive {
		t.Fatalf("the first month %s reads %s, not not_live", from, rows[0].Status)
	}
	for i := 1; i < len(rows); i++ {
		if rows[i].Decision <= rows[i-1].Decision {
			t.Fatalf("rows out of order at %d: %s after %s", i, rows[i].Decision, rows[i-1].Decision)
		}
	}
	if n := len(Replay(TrendSMA10, CoreLabel, f.Calendar, f.Dates, f.Closes, "2099-01-01", "2099-12-31", ReplayTerms{})); n != 0 {
		t.Fatalf("%d rows outside the calendar", n)
	}
}
