package flexstmt

import (
	"os"
	"strings"
	"testing"
	"time"
)

func interestTiersFixture(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile("testdata/interest-tiers-synthetic.xml")
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// The section's rows are read per currency, interest type, value date and
// tier with the broker's signs kept; another account's row and the summary
// row are not this account's evidence; ordinary sections are untouched.
func TestInterestTiersParseCurrenciesSidesAndDays(t *testing.T) {
	statements, err := Parse([]byte(interestTiersFixture(t)))
	if err != nil {
		t.Fatal(err)
	}
	st := statements[0]
	tiers := st.InterestTiers
	if tiers == nil || !tiers.Present || tiers.Reason != "" || len(tiers.Rows) != 6 {
		t.Fatalf("tiers=%+v", tiers)
	}
	if len(st.Equity) != 2 || len(st.Trades) != 0 || len(st.Cash) != 0 {
		t.Fatal("the tiers section altered ordinary evidence")
	}
	byKey := map[string]InterestTier{}
	for _, row := range tiers.Rows {
		byKey[row.Currency+"/"+row.ValueDate.Format(time.DateOnly)+"/"+row.Tier] = row
	}
	usd := byKey["USD/2026-10-09/2"]
	if usd.Type != "IBKR Credit Interest" || *usd.RatePct != 3.38 || *usd.Threshold != 10000 || *usd.Principal != 42000 || *usd.Interest != 3.89 {
		t.Fatalf("USD tier 2: %+v", usd)
	}
	if prev := byKey["USD/2026-10-08/2"]; *prev.RatePct != 3.40 {
		t.Fatalf("the earlier value date was merged into the latest: %+v", prev)
	}
	eur := byKey["EUR/2026-10-09/1"]
	if eur.Type != "IBKR Debit Interest" || *eur.Principal != -30000 || *eur.Interest != -2.86 || *eur.RatePct != 3.482 {
		t.Fatalf("EUR debit tier lost its sign: %+v", eur)
	}
	if chf := byKey["CHF/2026-10-09/2"]; *chf.RatePct != -0.206 {
		t.Fatalf("a negative broker rate was not kept: %+v", chf)
	}
	for key := range byKey {
		if strings.HasPrefix(key, "GBP/") || strings.HasPrefix(key, "BASE_SUMMARY") {
			t.Fatalf("another account's or the summary row was read as evidence: %s", key)
		}
	}
}

// A statement without the section says so, an empty section is present
// with no rows, and neither is read as a zero rate.
func TestInterestTiersMissingVersusEmptySection(t *testing.T) {
	fixture := interestTiersFixture(t)
	start, end := strings.Index(fixture, "<TierInterestDetails>"), strings.Index(fixture, "</TierInterestDetails>")+len("</TierInterestDetails>")
	without := fixture[:start] + fixture[end:]
	rows, err := Parse([]byte(without))
	if err != nil || rows[0].InterestTiers == nil || rows[0].InterestTiers.Present || len(rows[0].InterestTiers.Rows) != 0 {
		t.Fatalf("absent section was not reported absent: %+v (%v)", rows[0].InterestTiers, err)
	}
	rows, err = Parse([]byte(fixture[:start] + "<TierInterestDetails/>" + fixture[end:]))
	if err != nil || !rows[0].InterestTiers.Present || len(rows[0].InterestTiers.Rows) != 0 || rows[0].InterestTiers.Reason != "" {
		t.Fatalf("present empty section lost: %+v (%v)", rows[0].InterestTiers, err)
	}
}

// An unreadable row is left out with a closed reason and never fabricated;
// the rest of the statement is still retained.
func TestInterestTiersInvalidRowsAreLeftOutWithAReason(t *testing.T) {
	fixture := interestTiersFixture(t)
	for name, tc := range map[string]struct{ input, reason string }{
		"rate missing":       {strings.Replace(fixture, `rate="3.38"`, ``, 1), "interest_tier_rate_missing"},
		"rate not a number":  {strings.Replace(fixture, `rate="3.38"`, `rate="NaN"`, 1), "interest_tier_rate_missing"},
		"value date invalid": {strings.Replace(fixture, `valueDate="20261009" tierBreak="2" balanceThreshold="10000" securitiesPrincipal="42000"`, `valueDate="2026-13-09" tierBreak="2" balanceThreshold="10000" securitiesPrincipal="42000"`, 1), "interest_tier_date_invalid"},
		"threshold invalid":  {strings.Replace(fixture, `balanceThreshold="10000" securitiesPrincipal="42000"`, `balanceThreshold="ten" securitiesPrincipal="42000"`, 1), "interest_tier_number_invalid"},
		"model scope":        {strings.Replace(fixture, `model="" currency="EUR"`, `model="MODELTEST" currency="EUR"`, 1), "model_scope_unsupported"},
		"unknown row":        {strings.Replace(fixture, `<TierInterestDetail accountId="DU1234567" acctAlias="" model="" currency="EUR"`, `<InterestTierSummary accountId="DU1234567" acctAlias="" model="" currency="EUR"`, 1), "unsupported_interest_tier_rows"},
		"duplicate row":      {strings.Replace(fixture, `<TierInterestDetail accountId="DU1234567" acctAlias="" model="" currency="EUR"`, `<TierInterestDetail accountId="DU1234567" acctAlias="" model="" currency="EUR" fxRateToBase="1" interestType="IBKR Debit Interest" valueDate="20261009" tierBreak="1" balanceThreshold="0" totalPrincipal="-30000" rate="3.482" totalInterest="-2.86"/><TierInterestDetail accountId="DU1234567" acctAlias="" model="" currency="EUR"`, 1), "duplicate_interest_tier_rows"},
	} {
		t.Run(name, func(t *testing.T) {
			rows, err := Parse([]byte(tc.input))
			if err != nil || len(rows[0].Equity) != 2 {
				t.Fatalf("an interest tier row broke ordinary reporting: %v", err)
			}
			tiers := rows[0].InterestTiers
			if tiers.Reason != tc.reason || len(tiers.Rows) != 5 {
				t.Fatalf("reason %q rows %d, want %q and 5", tiers.Reason, len(tiers.Rows), tc.reason)
			}
		})
	}
}
