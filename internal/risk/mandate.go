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
	// IndexGapShockPct is the gap a broad index fund or index is stressed
	// by instead (owner decision D4, 2026-10-14 CEST: "12% for broad index
	// funds"); the instruments are the fixed list mandate.BroadIndexInstruments.
	// Optional: absent, every underlying takes gap_shock_pct, so a file
	// without it keeps its fingerprint and its behaviour.
	IndexGapShockPct *float64 `toml:"index_gap_shock_pct" json:"index_gap_shock_pct,omitempty"`
	// GapRoomPct is the share of the remaining loss room one name may lose
	// in that gap, in percent; the gap cap per underlying is
	// room × D × budget base × this.
	GapRoomPct *float64 `toml:"gap_room_pct" json:"gap_room_pct"`
	// Sleeves holds the volatility shares per sleeve; the machine's size its
	// allocations (allocations, 2026-10-18).
	Sleeves *ConstitutionMandateSleeves `toml:"sleeves" json:"sleeves,omitempty"`
	// Core is the core sleeve's rule and numbers
	// (internal-docs/design/core-sleeve.md). Optional: absent, the core is
	// inactive and every fingerprint is kept.
	Core *ConstitutionMandateCore `toml:"core" json:"core,omitempty"`
}

// ConstitutionMandateCore is [mandate.core]: which compiled rule the core
// sleeve runs, the instrument it is pinned to and the owner's numbers
// (owner decisions 2026-10-14; internal-docs/design/core-sleeve.md). Its
// volatility share is [mandate.sleeves] core_vol_pct. The rule's own
// parameters (ten months, span 63, 252 days, the 15% floor) are compiled
// with the rule version, never keys here. Every key is the owner's; a
// missing one holds the core and is named by CoreUnapprovedKeys, and never
// reaches the constitution's own approval list: a half-written table
// changes nothing outside the core. In this increment the core is
// read-only: it serves its target (core.target) and sends nothing.
type ConstitutionMandateCore struct {
	// Rule names the compiled rule, for example "core-ibcf-v1"; the daemon
	// holds the core on a rule it does not carry.
	Rule string `toml:"rule" json:"rule,omitempty"`
	// ISIN and Currency name the instrument; Canary finds the contract
	// IBKR lists under the ISIN in that currency and reads it on the
	// listing's primary exchange (owner decision 2026-10-18 08:06 CEST).
	// ConID and Exchange are optional: a written con_id (above 0) chooses
	// among several lines and must be one IBKR lists under the ISIN; a
	// written exchange must be a venue IBKR lists the contract on. 0, or
	// unwritten, lets the ISIN decide.
	ISIN     string `toml:"isin" json:"isin,omitempty"`
	ConID    *int   `toml:"con_id" json:"con_id,omitempty"`
	Exchange string `toml:"exchange" json:"exchange,omitempty"`
	Currency string `toml:"currency" json:"currency,omitempty"`
	// ScalePct is the commissioning ramp (25, then 50, then 100); 0
	// suspends the core.
	ScalePct *float64 `toml:"scale_pct" json:"scale_pct,omitempty"`
	// BandPct is the no-trade band in percent of the target shares.
	BandPct *float64 `toml:"band_pct" json:"band_pct,omitempty"`
	// MinOrderBase is the smallest order in base currency; a sale to zero
	// is never held by it.
	MinOrderBase *float64 `toml:"min_order_base" json:"min_order_base,omitempty"`
	// MaxValuePct caps the core's value at this share of the budget base ×
	// room.
	MaxValuePct *float64 `toml:"max_value_pct" json:"max_value_pct,omitempty"`
}

// CoreUnapprovedKeys lists the [mandate.core] keys the owner has not
// written (con_id and exchange are optional: IBKR's record for the ISIN
// names them), and the two [mandate] keys the core reads: its sleeve share and
// the index gap. Without index_gap_shock_pct the core's fund would be
// stressed as a single name, its target would sit at the gap cap's margin,
// and a modest rise would breach it and set up a forced sale the next
// month buys back (core sleeve review, 2026-10-14).
func (m *ConstitutionMandate) CoreUnapprovedKeys() []string {
	if m == nil || m.Core == nil {
		return nil
	}
	c := m.Core
	var out []string
	for _, k := range []struct {
		key     string
		missing bool
	}{
		{"mandate.core.rule", strings.TrimSpace(c.Rule) == ""},
		{"mandate.core.isin", strings.TrimSpace(c.ISIN) == ""},
		{"mandate.core.currency", strings.TrimSpace(c.Currency) == ""},
		{"mandate.core.scale_pct", c.ScalePct == nil},
		{"mandate.core.band_pct", c.BandPct == nil},
		{"mandate.core.min_order_base", c.MinOrderBase == nil},
		{"mandate.core.max_value_pct", c.MaxValuePct == nil},
		{"mandate.sleeves.core_vol_pct", m.Sleeves == nil || m.Sleeves.CoreVolPct == nil},
		{"mandate.index_gap_shock_pct", m.IndexGapShockPct == nil},
	} {
		if k.missing {
			out = append(out, k.key)
		}
	}
	return out
}

func (c *ConstitutionMandateCore) validate() error {
	if c == nil {
		return nil
	}
	finite := func(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }
	if v := c.ScalePct; v != nil && (!finite(*v) || *v < 0 || *v > 100) {
		return fmt.Errorf("mandate.core.scale_pct must be in [0, 100]")
	}
	if v := c.BandPct; v != nil && (!finite(*v) || *v <= 0 || *v >= 100) {
		return fmt.Errorf("mandate.core.band_pct must be in (0, 100)")
	}
	if v := c.MinOrderBase; v != nil && (!finite(*v) || *v < 0) {
		return fmt.Errorf("mandate.core.min_order_base must not be negative")
	}
	if v := c.MaxValuePct; v != nil && (!finite(*v) || *v <= 0 || *v > 100) {
		return fmt.Errorf("mandate.core.max_value_pct must be in (0, 100]")
	}
	if v := c.ConID; v != nil && *v < 0 {
		return fmt.Errorf("mandate.core.con_id must be an IBKR contract id, or 0 to let the ISIN decide")
	}
	if s := c.ISIN; s != "" && !isinShape(s) {
		return fmt.Errorf("mandate.core.isin %q must be twelve upper-case letters and digits", s)
	}
	for _, k := range []struct{ key, v string }{{"exchange", c.Exchange}, {"currency", c.Currency}, {"rule", c.Rule}} {
		if k.v != strings.TrimSpace(k.v) {
			return fmt.Errorf("mandate.core.%s %q carries spaces", k.key, k.v)
		}
	}
	if c.Exchange != strings.ToUpper(c.Exchange) || c.Currency != strings.ToUpper(c.Currency) {
		return fmt.Errorf("mandate.core.exchange and mandate.core.currency are written in upper case")
	}
	return nil
}

func isinShape(s string) bool {
	if len(s) != 12 {
		return false
	}
	for _, r := range s {
		if (r < 'A' || r > 'Z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}

// ConstitutionMandateSleeves is [mandate.sleeves]: annualised volatility in
// percent of the budget base per sleeve (owner 7, core 6, overlay 4 as
// decided 2026-10-14, then shares of one book whose sum exceeded the target
// because the sleeves are not perfectly correlated). Since the allocations of
// 2026-10-18 08:06 CEST (internal-docs/design/mandate-risk-budget.md,
// "Risk allocations") a machine sleeve's allocation is its stage times its share, measured by
// Euler contributions, which add up: the machine's shares together may not
// exceed vol_target_pct, and the owner's allocation is the rest of the budget
// (at full stages 15 − 6 − 4 = 5 points), so owner_vol_pct is recorded and
// not read.
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
	}{{"max_loss_pct", m.MaxLossPct}, {"vol_target_pct", m.VolTargetPct}, {"gap_shock_pct", m.GapShockPct}, {"index_gap_shock_pct", m.IndexGapShockPct}, {"gap_room_pct", m.GapRoomPct}} {
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
	if err := m.Core.validate(); err != nil {
		return err
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
		// The machine's allocations sit inside the budget: shares above the target
		// would leave the owner no allocation and turn a forced reduction on the
		// owner's whole book first.
		if t := m.VolTargetPct; t != nil {
			machine := 0.0
			for _, v := range []*float64{sl.CoreVolPct, sl.OverlayVolPct} {
				if v != nil {
					machine += *v
				}
			}
			if machine > *t {
				return fmt.Errorf("mandate.sleeves.core_vol_pct + overlay_vol_pct (%g) must not exceed mandate.vol_target_pct (%g): the machine's allocations sit inside the budget", machine, *t)
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
