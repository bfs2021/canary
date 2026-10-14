package risk

import (
	"fmt"
	"math"
	"strings"
	"time"
)

// ConstitutionMandate is the [mandate] table: the owner's maximum loss and
// volatility budget on the risk base (equity after the tax reserve), counted
// from a start date with a cash-flow-adjusted high-water mark
// (internal-docs/design/mandate-risk-budget.md; owner decisions 2026-10-14
// 06:26-06:52 CEST). A pointer: a file without the table keeps every
// existing fingerprint and the feature is inactive. Material keys are
// pointers too: nil is unapproved, and validation never backfills them. The
// gap numbers are the owner's as written; the proposals (25 and 33.3) live
// in the template's comments, never in code that acts.
type ConstitutionMandate struct {
	// MaxLossPct is D: the largest loss of the budget base the owner
	// tolerates, in percent. Room runs from 1 at a new high to 0 at D.
	MaxLossPct *float64 `toml:"max_loss_pct" json:"max_loss_pct"`
	// VolTargetPct is the annualised volatility budget at full room, in
	// percent of the budget base (the owner's choice: D/2).
	VolTargetPct *float64 `toml:"vol_target_pct" json:"vol_target_pct"`
	// TaxReserveBase is the part of equity that is not risk base, in the
	// account base currency ("Tax comes from this account").
	TaxReserveBase *float64 `toml:"tax_reserve_base" json:"tax_reserve_base"`
	// StartedAt is the date (YYYY-MM-DD) the high-water mark counts from;
	// a change starts the series again.
	StartedAt string `toml:"started_at" json:"started_at"`
	// GapShockPct is the one-name gap each underlying is stressed by, in
	// percent of its price (down, and up where a short call or short stock
	// loses on the upside).
	GapShockPct *float64 `toml:"gap_shock_pct" json:"gap_shock_pct"`
	// GapRoomPct is the share of the remaining loss room one name may lose
	// in that gap, in percent; the gap cap per underlying is
	// room × D × budget base × this.
	GapRoomPct *float64 `toml:"gap_room_pct" json:"gap_room_pct"`
	// Sleeves records the owner's volatility shares per sleeve; recorded,
	// not enforced in this increment.
	Sleeves *ConstitutionMandateSleeves `toml:"sleeves" json:"sleeves,omitempty"`
}

// ConstitutionMandateSleeves is [mandate.sleeves]: annualised volatility in
// percent of the budget base per sleeve (owner 7, core 6, overlay 4 as
// decided 2026-10-14). They are shares of one book, not addends: their sum
// exceeds the target by design, because the sleeves are not perfectly
// correlated.
type ConstitutionMandateSleeves struct {
	OwnerVolPct   *float64 `toml:"owner_vol_pct" json:"owner_vol_pct"`
	CoreVolPct    *float64 `toml:"core_vol_pct" json:"core_vol_pct"`
	OverlayVolPct *float64 `toml:"overlay_vol_pct" json:"overlay_vol_pct"`
}

// MandateStartLayout is the date form started_at is written in.
const MandateStartLayout = "2006-01-02"

func (m *ConstitutionMandate) validate(semantics ConstitutionSemantics) error {
	if m == nil {
		return nil
	}
	// The table belongs to the current format: schema 2, or a legacy file
	// at policy_version 4 or later, which carries the same semantics. An
	// older revision cannot name it, and an older daemon refuses the keys.
	if !semantics.ProcessReminders {
		return fmt.Errorf("[mandate] requires schema 2 or legacy policy_version >= 4")
	}
	pct := func(key string, v *float64) error {
		if v != nil && (math.IsNaN(*v) || math.IsInf(*v, 0) || *v <= 0 || *v > 100) {
			return fmt.Errorf("mandate.%s must be in (0, 100]", key)
		}
		return nil
	}
	for _, k := range []struct {
		key string
		v   *float64
	}{{"max_loss_pct", m.MaxLossPct}, {"vol_target_pct", m.VolTargetPct}, {"gap_shock_pct", m.GapShockPct}, {"gap_room_pct", m.GapRoomPct}} {
		if err := pct(k.key, k.v); err != nil {
			return err
		}
	}
	if v := m.TaxReserveBase; v != nil && (math.IsNaN(*v) || math.IsInf(*v, 0) || *v < 0) {
		return fmt.Errorf("mandate.tax_reserve_base must not be negative")
	}
	if s := strings.TrimSpace(m.StartedAt); s != "" {
		if _, err := time.Parse(MandateStartLayout, s); err != nil || s != m.StartedAt {
			return fmt.Errorf("mandate.started_at %q must be a date written as YYYY-MM-DD", m.StartedAt)
		}
	}
	if sl := m.Sleeves; sl != nil {
		for _, k := range []struct {
			key string
			v   *float64
		}{{"sleeves.owner_vol_pct", sl.OwnerVolPct}, {"sleeves.core_vol_pct", sl.CoreVolPct}, {"sleeves.overlay_vol_pct", sl.OverlayVolPct}} {
			if err := pct(k.key, k.v); err != nil {
				return err
			}
		}
	}
	return nil
}

// UnapprovedKeys lists the material mandate keys the owner has not written.
// Sleeves are a record, never approval material.
func (m *ConstitutionMandate) UnapprovedKeys() []string {
	if m == nil {
		return nil
	}
	var out []string
	if m.MaxLossPct == nil {
		out = append(out, "mandate.max_loss_pct")
	}
	if m.VolTargetPct == nil {
		out = append(out, "mandate.vol_target_pct")
	}
	if m.TaxReserveBase == nil {
		out = append(out, "mandate.tax_reserve_base")
	}
	if strings.TrimSpace(m.StartedAt) == "" {
		out = append(out, "mandate.started_at")
	}
	if m.GapShockPct == nil {
		out = append(out, "mandate.gap_shock_pct")
	}
	if m.GapRoomPct == nil {
		out = append(out, "mandate.gap_room_pct")
	}
	return out
}

// MandateActive reports whether the constitution carries a [mandate] table:
// the feature is then active, and its gate refuses additions until every
// material key is written and the verdict reads within.
func (c *Constitution) MandateActive() bool {
	return c != nil && c.Mandate != nil
}

// MandateStart returns the start date, or false while it is unwritten or
// unparseable (Validate refuses the latter).
func (m *ConstitutionMandate) MandateStart() (time.Time, bool) {
	if m == nil {
		return time.Time{}, false
	}
	t, err := time.Parse(MandateStartLayout, strings.TrimSpace(m.StartedAt))
	if err != nil {
		return time.Time{}, false
	}
	return t.UTC(), true
}
