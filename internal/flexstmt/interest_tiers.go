package flexstmt

import (
	"context"
	"strings"
	"time"
)

// InterestTier is one row of the Flex "Interest Details (Tiers)" section:
// the broker's own rate on one tier of one currency's balance on a value
// date. RatePct is the annual percentage as the statement writes it;
// Principal (the balance in the tier, negative when borrowed) and Interest
// (the day's interest on it) keep the broker's sign; Threshold is the
// balance the tier starts at; Type is the broker's interest type text.
// Element and attribute names follow IBKR's Flex reference for the section
// (TierInterestDetails rows of TierInterestDetail). No retained statement
// carried the section when this was written (2026-10-11), so the names are
// documented, not yet observed in a broker statement.
type InterestTier struct {
	Currency, Type, Tier                    string
	ValueDate                               time.Time
	Threshold, Principal, RatePct, Interest *float64
}

// InterestTiersStatement preserves the section's presence, its valid rows
// and a closed reason for rows left out. A missing section cannot certify a
// rate, and an invalid row never reads as a zero rate.
type InterestTiersStatement struct {
	Present bool
	Reason  string
	Rows    []InterestTier
}

func parseInterestTiers(ctx context.Context, data []byte, statements []Statement) {
	var doc struct {
		Statements []struct {
			Tiers *financingSection `xml:"TierInterestDetails"`
		} `xml:"FlexStatements>FlexStatement"`
	}
	if unmarshalContext(ctx, data, &doc) != nil || len(doc.Statements) != len(statements) {
		return
	}
	for i, raw := range doc.Statements {
		st := &statements[i]
		t := &InterestTiersStatement{Present: raw.Tiers != nil}
		st.InterestTiers = t
		if raw.Tiers == nil {
			continue
		}
		// A key seen twice is ambiguous evidence: both copies are left out.
		var rows []InterestTier
		count := map[string]int{}
		for _, row := range raw.Tiers.Rows {
			tier, reason, keep := parseInterestTier(row, *st)
			if !keep {
				continue
			}
			if reason != "" {
				if t.Reason == "" {
					t.Reason = reason
				}
				continue
			}
			count[interestTierKey(tier)]++
			rows = append(rows, tier)
		}
		for _, tier := range rows {
			if count[interestTierKey(tier)] > 1 {
				t.Reason = "duplicate_interest_tier_rows"
				continue
			}
			t.Rows = append(t.Rows, tier)
		}
	}
}

func interestTierKey(tier InterestTier) string {
	return strings.Join([]string{tier.Currency, tier.Type, tier.ValueDate.Format(time.DateOnly), tier.Tier}, "|")
}

// parseInterestTier reads one row. keep is false for a row that is not this
// account's evidence at all (a summary row, or another account's); reason
// names a row of this account that cannot be read.
func parseInterestTier(row financingNode, st Statement) (tier InterestTier, reason string, keep bool) {
	if row.XMLName.Local != "TierInterestDetail" {
		return tier, "unsupported_interest_tier_rows", true
	}
	currency := strings.ToUpper(row.text("currency"))
	if currency == "" || currency == "BASE_SUMMARY" {
		return tier, "", false
	}
	if a := row.text("accountId"); a != "" && a != st.AccountID {
		return tier, "", false
	}
	if row.text("model") != "" {
		return tier, "model_scope_unsupported", true
	}
	if !lendingCurrency(currency) {
		return tier, "interest_tier_currency_invalid", true
	}
	day, err := parseFlexDate(row.text("valueDate"))
	if err != nil {
		return tier, "interest_tier_date_invalid", true
	}
	tier = InterestTier{Currency: currency, Type: strings.TrimSpace(row.text("interestType")), Tier: strings.TrimSpace(row.text("tierBreak")), ValueDate: day}
	if tier.RatePct, err = optionalFloat("rate", row.text("rate")); err != nil || tier.RatePct == nil {
		return tier, "interest_tier_rate_missing", true
	}
	for _, item := range []struct {
		out  **float64
		keys []string
	}{{&tier.Threshold, []string{"balanceThreshold"}}, {&tier.Principal, []string{"totalPrincipal", "securitiesPrincipal"}}, {&tier.Interest, []string{"totalInterest", "securitiesInterest"}}} {
		for _, key := range item.keys {
			v, err := optionalFloat(key, row.text(key))
			if err != nil {
				return tier, "interest_tier_number_invalid", true
			}
			if v != nil {
				*item.out = v
				break
			}
		}
	}
	return tier, "", true
}
