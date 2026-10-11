package rpc

import "time"

// Bill and bond facts for the console (internal-docs/design/bond-risk.md,
// "Bill and bond facts", owner decisions 2026-10-11 14:07 and 15:39 CEST):
// the price a held bill or bond trades at, its yield on a stated basis, the
// yield's change since the prior close, what a 25 bp move would do to the
// position, what it pays at maturity, and its gain since purchase split into
// carry, rates and spread. Every figure is Canary's: the browser does no
// financial arithmetic. A fact that cannot be stated is nil with a reason;
// nothing is read as zero.

// Where a bond fact's price was read from.
const (
	BondPriceSourceBid  = "bid"
	BondPriceSourceAsk  = "ask"
	BondPriceSourceLast = "last"
	BondPriceSourceMark = "mark"
)

// Where a line's purchase price and date came from.
const (
	// BondPurchaseSourceJournal: Canary's order journal holds every filled
	// buy of the line, so lots carry their own price and date.
	BondPurchaseSourceJournal = "canary_order_journal"
	// BondPurchaseSourceBrokerCost: the broker's average cost (read back
	// from its market value, mark and unrealised gain) gives the price; the
	// date is the journal's earliest buy when it has one, else unknown.
	BondPurchaseSourceBrokerCost = "broker_average_cost"
)

// Where a line's face value came from.
const (
	// BondFaceSourceQuantityUnit: quantity × the instrument's face per unit
	// (A5 conventions, cash-sweep.md).
	BondFaceSourceQuantityUnit = "quantity_unit"
	// BondFaceSourceMarketValue: the broker's market value × 100 / mark,
	// for a line whose quantity unit Canary does not know.
	BondFaceSourceMarketValue = "broker_market_value"
)

// BondScenarioModel names how the rate scenario is computed: a full
// repricing on the stated yield basis, a model estimate and not a quote.
const BondScenarioModel = "full repricing on the stated yield basis; a model estimate, not a quote"

// BondFacts are one held line's console facts. Prices are per 100 of face
// and money is in the line's currency. Unavailable, when set, says why no
// price-based fact could be stated; the face and maturity facts stand on
// their own.
type BondFacts struct {
	Currency        string `json:"currency"`
	PriceConvention string `json:"price_convention"`
	// The quote: bid and ask with the quote's clock and freshness, the
	// broker's mark, and the prior session's close as the broker sent it.
	Bid         *float64  `json:"bid,omitempty"`
	Ask         *float64  `json:"ask,omitempty"`
	Last        *float64  `json:"last,omitempty"`
	Mark        *float64  `json:"mark,omitempty"`
	Close       *float64  `json:"close,omitempty"`
	QuoteAsOf   time.Time `json:"quote_as_of,omitzero"`
	QuoteFresh  bool      `json:"quote_fresh"`
	QuoteNote   string    `json:"quote_note,omitempty"`
	PriceSource string    `json:"price_source,omitempty"`
	// Price is the price the yield and scenarios are read at (PriceSource
	// says which), and YieldPct its yield on YieldBasis (risk.BillYieldBasis*
	// for a bill, risk.BondYieldBasis* for a coupon bond). The broker's own
	// yield ticks are not used: their basis is unstated.
	Price          *float64 `json:"price,omitempty"`
	YieldPct       *float64 `json:"yield_pct,omitempty"`
	YieldBasis     string   `json:"yield_basis,omitempty"`
	YieldBasisNote string   `json:"yield_basis_note,omitempty"`
	// The yield at the prior session's close on the same basis, with that
	// session's date, and the change since in basis points.
	PriorCloseYieldPct     *float64 `json:"prior_close_yield_pct,omitempty"`
	PriorCloseDate         string   `json:"prior_close_date,omitempty"`
	PriorCloseNote         string   `json:"prior_close_note,omitempty"`
	YieldChangeBp          *float64 `json:"yield_change_bp,omitempty"`
	YieldChangeUnavailable string   `json:"yield_change_unavailable,omitempty"`
	// Rate scenario at the price: the position's value change for a 25 bp
	// rise and fall, and the value lost per 1 bp rise (DV01), in the line's
	// currency; ScenarioModel says how.
	ValueChangeUp25Bp   *float64 `json:"value_change_up_25bp,omitempty"`
	ValueChangeDown25Bp *float64 `json:"value_change_down_25bp,omitempty"`
	DV01                *float64 `json:"dv01,omitempty"`
	ScenarioModel       string   `json:"scenario_model,omitempty"`
	// Face and maturity.
	FaceValue      *float64             `json:"face_value,omitempty"`
	FaceSource     string               `json:"face_source,omitempty"`
	PaysAtMaturity *BondMaturityPayment `json:"pays_at_maturity,omitempty"`
	// Since purchase.
	Purchase                 *BondPurchase `json:"purchase,omitempty"`
	PnL                      *BondPnLSplit `json:"pnl,omitempty"`
	SincePurchaseUnavailable string        `json:"since_purchase_unavailable,omitempty"`
	Unavailable              string        `json:"unavailable,omitempty"`
}

// BondMaturityPayment is what the line pays at maturity: its face, in its
// currency, on the maturity date, in Days from today.
type BondMaturityPayment struct {
	Amount   float64 `json:"amount"`
	Currency string  `json:"currency"`
	Date     string  `json:"date"`
	Days     int     `json:"days"`
}

// BondPurchase is how and when the line was bought. Date is the first lot's
// day (YYYY-MM-DD); PricePer100 the face-weighted clean price paid; Lots the
// number of buys; YieldPct the face-weighted yield at purchase on the facts'
// basis. Source is a BondPurchaseSource* constant and Note what the source
// could not say.
type BondPurchase struct {
	Date        string   `json:"date,omitempty"`
	PricePer100 float64  `json:"price_per_100"`
	Lots        int      `json:"lots,omitempty"`
	YieldPct    *float64 `json:"yield_pct,omitempty"`
	Source      string   `json:"source"`
	Note        string   `json:"note,omitempty"`
}

// BondPnLSplit is the line's clean-price gain since purchase in its
// currency and the parts that sum to it. Carry and Rates are nil when the
// purchase date is unknown (Total is then the broker's unrealised gain);
// Spread is nil with SpreadUnavailable saying why when no benchmark change
// is on record. Scope says what the figures cover: the clean price, so
// coupon income is not included.
type BondPnLSplit struct {
	Currency          string         `json:"currency"`
	Scope             string         `json:"scope"`
	Total             float64        `json:"total"`
	Carry             *float64       `json:"carry,omitempty"`
	Rates             *float64       `json:"rates,omitempty"`
	Spread            *float64       `json:"spread,omitempty"`
	SpreadUnavailable string         `json:"spread_unavailable,omitempty"`
	Benchmark         *BondBenchmark `json:"benchmark,omitempty"`
	TotalSource       string         `json:"total_source,omitempty"`
}

// BondPnLScopeCleanPrice is the scope of every BondPnLSplit: clean price
// against clean purchase price; coupon income is not included.
const BondPnLScopeCleanPrice = "clean_price"

// Where a BondPnLSplit's total came from.
const (
	BondPnLTotalSourceLots   = "purchase_lots"
	BondPnLTotalSourceBroker = "broker_unrealized"
)

// BondBenchmark is the sovereign bill a non-US bill's yield is compared
// against (a BTF against the Bubill nearest in maturity): its identity,
// its yield now on the facts' basis with its quote clock, the spread now in
// basis points, and, when records allow, the spread when the line was bought
// (SpreadSince names the record date the spread part runs from, which may
// postdate the purchase).
type BondBenchmark struct {
	ConID        int       `json:"con_id,omitempty"`
	ISIN         string    `json:"isin,omitempty"`
	Issuer       string    `json:"issuer,omitempty"`
	Maturity     string    `json:"maturity,omitempty"`
	YieldPct     *float64  `json:"yield_pct,omitempty"`
	YieldBasis   string    `json:"yield_basis,omitempty"`
	QuoteAsOf    time.Time `json:"quote_as_of,omitzero"`
	SpreadBpNow  *float64  `json:"spread_bp_now,omitempty"`
	SpreadBpThen *float64  `json:"spread_bp_then,omitempty"`
	SpreadSince  string    `json:"spread_since,omitempty"`
	Source       string    `json:"source,omitempty"`
	Note         string    `json:"note,omitempty"`
}

// CloneBondFacts deep-copies bond facts; nil stays nil.
func CloneBondFacts(in *BondFacts) *BondFacts {
	if in == nil {
		return nil
	}
	out := *in
	for _, p := range []**float64{&out.Bid, &out.Ask, &out.Last, &out.Mark, &out.Close, &out.Price, &out.YieldPct, &out.PriorCloseYieldPct,
		&out.YieldChangeBp, &out.ValueChangeUp25Bp, &out.ValueChangeDown25Bp, &out.DV01, &out.FaceValue} {
		*p = cloneCashSweepFloat(*p)
	}
	if in.PaysAtMaturity != nil {
		out.PaysAtMaturity = new(*in.PaysAtMaturity)
	}
	if in.Purchase != nil {
		p := *in.Purchase
		p.YieldPct = cloneCashSweepFloat(p.YieldPct)
		out.Purchase = &p
	}
	if in.PnL != nil {
		p := *in.PnL
		p.Carry, p.Rates, p.Spread = cloneCashSweepFloat(p.Carry), cloneCashSweepFloat(p.Rates), cloneCashSweepFloat(p.Spread)
		if in.PnL.Benchmark != nil {
			b := *in.PnL.Benchmark
			b.YieldPct, b.SpreadBpNow, b.SpreadBpThen = cloneCashSweepFloat(b.YieldPct), cloneCashSweepFloat(b.SpreadBpNow), cloneCashSweepFloat(b.SpreadBpThen)
			p.Benchmark = &b
		}
		out.PnL = &p
	}
	return &out
}
