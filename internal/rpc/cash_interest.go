package rpc

import (
	"slices"
	"time"
)

// Broker cash interest rates, read from the Flex statement's "Interest
// Details (Tiers)" section (owner decision 2026-10-11 14:07 CEST: cash credit
// and debit rates are the broker's live rates; an entered number is a bug).
// IBKR resets its benchmark once per business day, so the latest value date
// is the live rate. A currency with no row for a side (never held, or never
// borrowed), no statement carrying the section, or a latest value date older
// than CashInterestMaxValueDateBusinessDays reads unavailable with the reason
// and the last value date seen: never zero, never an assumed number. A broker
// rate of exactly zero (the first slice of a balance earns nothing) is a
// real rate and reads as 0.
const (
	CashInterestSourceFlexTiers  = "flex_interest_tiers"
	CashInterestStateAvailable   = "available"
	CashInterestStateUnavailable = "unavailable"
	// CashInterestMaxValueDateBusinessDays is how many business days old the
	// latest value date may be and still count as the live rate (the
	// statement for day T arrives on the morning of T+1).
	CashInterestMaxValueDateBusinessDays = 2
	// CashInterestBalanceLedger: the tier was picked for the ledger's current
	// cash; CashInterestBalanceStatement: the ledger carried no cash figure,
	// so the tier was picked for the statement's own balance on the value
	// date.
	CashInterestBalanceLedger    = "ledger"
	CashInterestBalanceStatement = "statement"
)

// CashInterestRates is one currency's broker rates: the credit side for its
// cash, the debit side for a margin loan in it. Field names are stable for
// Desk, which reads them from the account's currency rows.
type CashInterestRates struct {
	Source string `json:"source"`
	// ReadAt is when Canary last read the retained statements for these
	// rates; the value dates below are the broker's clocks.
	ReadAt time.Time        `json:"read_at,omitzero"`
	Credit CashInterestSide `json:"credit"`
	Debit  CashInterestSide `json:"debit"`
}

// CashInterestSide is one side's rate and the tiers it was picked from.
type CashInterestSide struct {
	State  string `json:"state"`
	Reason string `json:"reason,omitempty"`
	// Rate is the annual decimal rate (0.0338 for 3.38% a year) on the tier
	// the balance's top unit falls in: what the next unit of cash earns, or
	// the next unit borrowed costs. Tier names that tier.
	Rate *float64 `json:"rate,omitempty"`
	Tier string   `json:"tier,omitempty"`
	// Balance is the balance the tier was picked for, in the currency's own
	// unit and without sign; BalanceSource says whose balance.
	Balance       *float64 `json:"balance,omitempty"`
	BalanceSource string   `json:"balance_source,omitempty"`
	// ValueDate is the broker's value date of the rate (YYYY-MM-DD);
	// GeneratedAt is when the broker generated the statement that carried
	// it. LastValueDate is the latest value date seen when the side is
	// unavailable for being stale.
	ValueDate     string    `json:"value_date,omitempty"`
	LastValueDate string    `json:"last_value_date,omitempty"`
	GeneratedAt   time.Time `json:"generated_at,omitzero"`
	// Tiers are every tier the statement listed for the value date, lowest
	// threshold first.
	Tiers []CashInterestTier `json:"tiers,omitempty"`
}

// CashInterestTier is one tier of a side on the value date: the balance it
// starts at, its annual decimal rate, the broker's principal in it and the
// day's interest on that principal (both with the broker's sign).
type CashInterestTier struct {
	Tier      string   `json:"tier"`
	Threshold *float64 `json:"threshold,omitempty"`
	Rate      float64  `json:"rate"`
	Principal *float64 `json:"principal,omitempty"`
	Interest  *float64 `json:"interest,omitempty"`
}

// CloneCashInterestRates deep-copies a currency's rates; nil stays nil.
func CloneCashInterestRates(in *CashInterestRates) *CashInterestRates {
	if in == nil {
		return nil
	}
	out := *in
	out.Credit = *CloneCashInterestSide(&in.Credit)
	out.Debit = *CloneCashInterestSide(&in.Debit)
	return &out
}

// CloneCashInterestSide deep-copies one side; nil stays nil.
func CloneCashInterestSide(in *CashInterestSide) *CashInterestSide {
	if in == nil {
		return nil
	}
	out := *in
	out.Rate = cloneCashSweepFloat(in.Rate)
	out.Balance = cloneCashSweepFloat(in.Balance)
	out.Tiers = slices.Clone(in.Tiers)
	for i := range out.Tiers {
		t := &out.Tiers[i]
		t.Threshold = cloneCashSweepFloat(t.Threshold)
		t.Principal = cloneCashSweepFloat(t.Principal)
		t.Interest = cloneCashSweepFloat(t.Interest)
	}
	return &out
}
