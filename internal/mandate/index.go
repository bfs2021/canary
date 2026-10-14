package mandate

import (
	"slices"
	"strings"
)

// BroadIndexInstrument is one instrument the index gap shock applies to: a
// fund or an index over hundreds of companies, which no single company's
// news can gap by a one-name stress (owner decision D4, 2026-10-14: "12%
// for broad index funds"; internal-docs/design/core-sleeve.md). A position
// row carries no ISIN, so a line matches by its broker symbol and its
// currency together; the ISIN names the fund behind every listing.
type BroadIndexInstrument struct {
	Symbol     string
	Currencies []string
	ISIN       string
	Name       string
}

// BroadIndexInstruments is the fixed list. It is compiled, not policy: a
// line not on it keeps the one-name gap_shock_pct, the stricter stress, so
// an omission errs towards the larger gap and an addition needs a reviewed
// change. Index options match through their underlying symbol.
var BroadIndexInstruments = []BroadIndexInstrument{
	// US-listed S&P 500, Nasdaq-100, Russell 2000 and Dow ETFs and the
	// cash indices their options settle on.
	{Symbol: "SPY", Currencies: []string{"USD"}, Name: "SPDR S&P 500 ETF"},
	{Symbol: "SPX", Currencies: []string{"USD"}, Name: "S&P 500 index"},
	{Symbol: "SPXW", Currencies: []string{"USD"}, Name: "S&P 500 index (weekly options)"},
	{Symbol: "XSP", Currencies: []string{"USD"}, Name: "Mini S&P 500 index"},
	{Symbol: "QQQ", Currencies: []string{"USD"}, Name: "Invesco QQQ (Nasdaq-100)"},
	{Symbol: "NDX", Currencies: []string{"USD"}, Name: "Nasdaq-100 index"},
	{Symbol: "IWM", Currencies: []string{"USD"}, Name: "iShares Russell 2000 ETF"},
	{Symbol: "RUT", Currencies: []string{"USD"}, Name: "Russell 2000 index"},
	{Symbol: "DIA", Currencies: []string{"USD"}, Name: "SPDR Dow Jones Industrial Average ETF"},
	// UCITS S&P 500 funds.
	{Symbol: "IBCF", Currencies: []string{"EUR"}, ISIN: "IE00B3ZW0K18", Name: "iShares S&P 500 EUR Hedged UCITS ETF (Acc)"},
	{Symbol: "SXR8", Currencies: []string{"EUR"}, ISIN: "IE00B5BMR087", Name: "iShares Core S&P 500 UCITS ETF (Acc)"},
	{Symbol: "CSPX", Currencies: []string{"USD"}, ISIN: "IE00B5BMR087", Name: "iShares Core S&P 500 UCITS ETF (Acc)"},
	{Symbol: "VUSA", Currencies: []string{"EUR", "GBP"}, ISIN: "IE00B3XXRP09", Name: "Vanguard S&P 500 UCITS ETF (Dist)"},
	{Symbol: "VUAA", Currencies: []string{"EUR", "USD"}, ISIN: "IE00BFMXXD54", Name: "Vanguard S&P 500 UCITS ETF (Acc)"},
	// UCITS world funds.
	{Symbol: "EUNL", Currencies: []string{"EUR"}, ISIN: "IE00B4L5Y983", Name: "iShares Core MSCI World UCITS ETF (Acc)"},
	{Symbol: "IWDA", Currencies: []string{"EUR", "USD"}, ISIN: "IE00B4L5Y983", Name: "iShares Core MSCI World UCITS ETF (Acc)"},
	{Symbol: "SWDA", Currencies: []string{"GBP"}, ISIN: "IE00B4L5Y983", Name: "iShares Core MSCI World UCITS ETF (Acc)"},
	{Symbol: "VWCE", Currencies: []string{"EUR"}, ISIN: "IE00BK5BQT80", Name: "Vanguard FTSE All-World UCITS ETF (Acc)"},
	{Symbol: "VWRL", Currencies: []string{"EUR", "GBP"}, ISIN: "IE00B3RBWM25", Name: "Vanguard FTSE All-World UCITS ETF (Dist)"},
	{Symbol: "VGWL", Currencies: []string{"EUR"}, ISIN: "IE00B3RBWM25", Name: "Vanguard FTSE All-World UCITS ETF (Dist)"},
}

// IsBroadIndex reports whether a line of this symbol and currency is on
// the broad index list. Both must match: a symbol alone can name another
// company on another venue.
func IsBroadIndex(symbol, currency string) bool {
	symbol, currency = strings.ToUpper(strings.TrimSpace(symbol)), strings.ToUpper(strings.TrimSpace(currency))
	for _, b := range BroadIndexInstruments {
		if b.Symbol == symbol && slices.Contains(b.Currencies, currency) {
			return true
		}
	}
	return false
}

// GapShockPctFor is the gap one underlying is stressed by: the index gap
// for a broad index when the owner wrote one, else the one-name gap.
func (p Policy) GapShockPctFor(broadIndex bool) float64 {
	if broadIndex && p.IndexGapShockPct > 0 {
		return p.IndexGapShockPct
	}
	return p.GapShockPct
}
