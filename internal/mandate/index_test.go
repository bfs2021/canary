package mandate

import (
	"math"
	"slices"
	"testing"
)

// A broad index takes the owner's index gap (D4, 2026-10-14: 12%), a single
// name keeps the one-name gap, and without index_gap_shock_pct every
// underlying takes gap_shock_pct as before.
func TestBroadIndexTakesTheIndexGap(t *testing.T) {
	spy := Leg{ID: "con:1", Symbol: IndexSymbol, Underlying: IndexSymbol, Kind: KindStock, Quantity: 1500, Multiplier: 1, Currency: "USD", Spot: 100}
	syna := Leg{ID: "con:2", Symbol: "SYNA", Underlying: "SYNA", Kind: KindStock, Quantity: 1500, Multiplier: 1, Currency: "USD", Spot: 100}
	in := testInput(t, []Leg{spy, syna})
	capBase := GapCapBase(in.Policy, in.Capital)
	value := 1500 * 100 * 0.9

	// Absent: both take 25% and both breach the cap.
	v := Evaluate(in)
	if v.Status != StatusReduce || !slices.Equal(v.Stresses.GapBreaches, []string{IndexSymbol, "SYNA"}) && !slices.Equal(v.Stresses.GapBreaches, []string{"SYNA", IndexSymbol}) {
		t.Fatalf("without the index gap: %s breaches %v (cap %g, gap loss %g)", v.Status, v.Stresses.GapBreaches, capBase, value*0.25)
	}
	if v.Stresses.IndexGapShockPct != 25 {
		t.Fatalf("index gap without the key %g, want the one-name 25", v.Stresses.IndexGapShockPct)
	}

	// Written: the index takes 12% and fits; the name keeps 25%.
	in.Policy.IndexGapShockPct = 12
	v = Evaluate(in)
	if !slices.Equal(v.Stresses.GapBreaches, []string{"SYNA"}) || v.Stresses.IndexGapShockPct != 12 {
		t.Fatalf("with the index gap: breaches %v stresses %+v", v.Stresses.GapBreaches, v.Stresses)
	}
	for _, u := range v.Underlyings {
		switch u.Underlying {
		case IndexSymbol:
			if !u.BroadIndex || u.GapShockPct != 12 || math.Abs(u.GapDownLossBase-value*0.12) > 1e-6 || u.GapBreach {
				t.Fatalf("index underlying %+v", u)
			}
		case "SYNA":
			if u.BroadIndex || u.GapShockPct != 25 || math.Abs(u.GapDownLossBase-value*0.25) > 1e-6 || !u.GapBreach {
				t.Fatalf("single name %+v", u)
			}
		}
	}
}

// The list matches symbol and currency together, case-insensitively; a
// symbol on the list in another currency, or a name off it, is not a broad
// index.
func TestIsBroadIndexMatchesSymbolAndCurrency(t *testing.T) {
	for _, c := range []struct {
		symbol, currency string
		want             bool
	}{
		{"IBCF", "EUR", true}, {" ibcf ", "eur", true}, {"IBCF", "USD", false},
		{"SPY", "USD", true}, {"SPY", "EUR", false}, {"SPX", "USD", true}, {"XSP", "USD", true}, {"NDX", "USD", true},
		{"QQQ", "USD", true}, {"IWM", "USD", true}, {"RUT", "USD", true}, {"DIA", "USD", true},
		{"SXR8", "EUR", true}, {"CSPX", "USD", true}, {"EUNL", "EUR", true}, {"VWCE", "EUR", true},
		{"AAPL", "USD", false}, {"SYNA", "USD", false}, {"", "", false},
	} {
		if got := IsBroadIndex(c.symbol, c.currency); got != c.want {
			t.Errorf("IsBroadIndex(%q, %q) = %v, want %v", c.symbol, c.currency, got, c.want)
		}
	}
	for _, b := range BroadIndexInstruments {
		if b.ISIN != "" && (len(b.ISIN) != 12 || b.ISIN[:2] != "IE") {
			t.Errorf("%s: ISIN %q is not an Irish UCITS ISIN", b.Symbol, b.ISIN)
		}
		if len(b.Currencies) == 0 || b.Name == "" {
			t.Errorf("%s: incomplete entry %+v", b.Symbol, b)
		}
	}
}

// One leg off the list keeps the whole underlying on the one-name gap.
func TestBroadIndexNeedsEveryLeg(t *testing.T) {
	legs := []Leg{{Underlying: "SPY", Currency: "USD"}, {Underlying: "SPY", Currency: "EUR"}}
	if broadIndexUnderlying(legs, "SPY") {
		t.Fatal("a mixed underlying read as a broad index")
	}
	if !broadIndexUnderlying(legs[:1], "SPY") || broadIndexUnderlying(legs, "QQQ") {
		t.Fatal("broad index by underlying")
	}
}
