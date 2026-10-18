package risk

import (
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"
)

// The strategy register (internal-docs/design/strategy-register.md; owner
// decision 2026-10-18 09:01 CEST: "Accept the four review fixes: one
// register for all machine strategies, the ladder stage in Canary's risk
// policy, shadow on the production path, and strategies declaring the data
// Canary collects."). Every machine strategy is one owner-written entry:
// [mandate.core] is the first, kept under its name, and every later one is a
// [mandate.strategies.<id>] table. Code reads Register(), never the tables.

// Ladder stages as the policy writes them.
const (
	StageShadow    = "shadow"
	StageLive      = "live"
	StageSuspended = "suspended"
)

// The two policy forms of an entry.
const (
	StrategyFormCore  = "core"
	StrategyFormEntry = "entry"
)

// CoreStrategyID is the core's id and sleeve in the register.
const CoreStrategyID = "core"

// OverlaySleeve takes its allocation from [mandate.sleeves] overlay_vol_pct.
const OverlaySleeve = "overlay"

// ConstitutionMandateStrategy is one [mandate.strategies.<id>] table. Every
// key is the owner's; a missing one holds that strategy and is named on it,
// never in the constitution's approval list. A rule's own owner numbers, when
// a rule needs any, are added here with the code that reads them.
type ConstitutionMandateStrategy struct {
	// Rule names the compiled rule; SpecHash the lab registry's hash of the
	// spec it implements, as the owner cites it.
	Rule     string `toml:"rule" json:"rule,omitempty"`
	SpecHash string `toml:"spec_hash" json:"spec_hash,omitempty"`
	// Sleeve is where the strategy's positions count. The overlay takes its
	// allocation from [mandate.sleeves] overlay_vol_pct; every other sleeve
	// from RiskAllocationPct.
	Sleeve            string   `toml:"sleeve" json:"sleeve,omitempty"`
	RiskAllocationPct *float64 `toml:"risk_allocation_pct" json:"risk_allocation_pct,omitempty"`
	// Stage is shadow, live or suspended; ScalePct the scale the rule sizes
	// at (25, 50 or 100), reserved only while live.
	Stage    string   `toml:"stage" json:"stage,omitempty"`
	ScalePct *float64 `toml:"scale_pct" json:"scale_pct,omitempty"`
	// The evaluator's keys (internal-docs/design/strategy-evaluator.md),
	// read by a parameterised rule such as timed-holding-v1 and held on
	// when unwritten: the instrument by ISIN and currency (ConID and
	// Exchange optional pins, as in [mandate.core]), the signal from the
	// library, the no-trade band, the minimum order and the value cap.
	ISIN         string   `toml:"isin" json:"isin,omitempty"`
	ConID        *int     `toml:"con_id" json:"con_id,omitempty"`
	Exchange     string   `toml:"exchange" json:"exchange,omitempty"`
	Currency     string   `toml:"currency" json:"currency,omitempty"`
	Signal       string   `toml:"signal" json:"signal,omitempty"`
	BandPct      *float64 `toml:"band_pct" json:"band_pct,omitempty"`
	MinOrderBase *float64 `toml:"min_order_base" json:"min_order_base,omitempty"`
	MaxValuePct  *float64 `toml:"max_value_pct" json:"max_value_pct,omitempty"`
	// KillDrawdownK is the kill rule's drawdown bound in units of sleeve
	// capital K, as the lab's design run writes it; optional, and unwritten
	// the kill rule cannot fire (strategy-evaluator.md §3).
	KillDrawdownK *float64 `toml:"kill_drawdown_k" json:"kill_drawdown_k,omitempty"`
}

// StrategyEntry is one register entry in either form, as code reads it.
type StrategyEntry struct {
	ID       string `json:"id"`
	Form     string `json:"form"`
	Rule     string `json:"rule,omitempty"`
	SpecHash string `json:"spec_hash,omitempty"`
	Sleeve   string `json:"sleeve"`
	// AllocationPct is the risk allocation, percent a year of the budget
	// base at full room; AllocationKey the key it is read from.
	AllocationPct *float64 `json:"allocation_pct,omitempty"`
	AllocationKey string   `json:"allocation_key"`
	// Stage is as written; empty only for the core's first form, which then
	// runs in shadow.
	Stage    string   `json:"stage,omitempty"`
	ScalePct *float64 `json:"scale_pct,omitempty"`
	// The evaluator's keys as the entry writes them (the core's from
	// [mandate.core]); nil or empty while unwritten. KeyPrefix names the
	// table the keys are read from ("mandate.core", "mandate.strategies.<id>").
	KeyPrefix     string   `json:"key_prefix"`
	ISIN          string   `json:"isin,omitempty"`
	ConID         *int     `json:"con_id,omitempty"`
	Exchange      string   `json:"exchange,omitempty"`
	Currency      string   `json:"currency,omitempty"`
	Signal        string   `json:"signal,omitempty"`
	BandPct       *float64 `json:"band_pct,omitempty"`
	MinOrderBase  *float64 `json:"min_order_base,omitempty"`
	MaxValuePct   *float64 `json:"max_value_pct,omitempty"`
	KillDrawdownK *float64 `json:"kill_drawdown_k,omitempty"`
	// Unwritten names the keys whose absence holds the strategy.
	Unwritten []string `json:"unwritten,omitempty"`
}

// Register lists every machine strategy: the core first, then the
// [mandate.strategies] entries by id. Nil without [mandate].
func (m *ConstitutionMandate) Register() []StrategyEntry {
	if m == nil {
		return nil
	}
	var out []StrategyEntry
	if c := m.Core; c != nil {
		e := StrategyEntry{ID: CoreStrategyID, Form: StrategyFormCore, Rule: c.Rule, SpecHash: c.SpecHash, Sleeve: CoreStrategyID,
			AllocationKey: "mandate.sleeves.core_vol_pct", Stage: c.Stage, ScalePct: c.ScalePct, Unwritten: m.CoreUnapprovedKeys(),
			KeyPrefix: "mandate.core", ISIN: c.ISIN, ConID: c.ConID, Exchange: c.Exchange, Currency: c.Currency,
			BandPct: c.BandPct, MinOrderBase: c.MinOrderBase, MaxValuePct: c.MaxValuePct, KillDrawdownK: c.KillDrawdownK}
		if m.Sleeves != nil {
			e.AllocationPct = m.Sleeves.CoreVolPct
		}
		if c.Stage == StageLive && strings.TrimSpace(c.SpecHash) == "" {
			e.Unwritten = append(e.Unwritten, "mandate.core.spec_hash")
		}
		out = append(out, e)
	}
	ids := make([]string, 0, len(m.Strategies))
	for id := range m.Strategies {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		s := m.Strategies[id]
		if s == nil {
			continue
		}
		key := func(k string) string { return "mandate.strategies." + id + "." + k }
		e := StrategyEntry{ID: id, Form: StrategyFormEntry, Rule: s.Rule, SpecHash: s.SpecHash, Sleeve: s.Sleeve, Stage: s.Stage, ScalePct: s.ScalePct,
			KeyPrefix: "mandate.strategies." + id, ISIN: s.ISIN, ConID: s.ConID, Exchange: s.Exchange, Currency: s.Currency, Signal: s.Signal,
			BandPct: s.BandPct, MinOrderBase: s.MinOrderBase, MaxValuePct: s.MaxValuePct, KillDrawdownK: s.KillDrawdownK}
		var missing []string
		for _, k := range []struct {
			name    string
			missing bool
		}{
			{"rule", strings.TrimSpace(s.Rule) == ""},
			{"spec_hash", strings.TrimSpace(s.SpecHash) == ""},
			{"sleeve", strings.TrimSpace(s.Sleeve) == ""},
			{"stage", strings.TrimSpace(s.Stage) == ""},
			{"scale_pct", s.ScalePct == nil && (s.Stage == StageShadow || s.Stage == StageLive)},
		} {
			if k.missing {
				missing = append(missing, key(k.name))
			}
		}
		if s.Sleeve == OverlaySleeve {
			e.AllocationKey = "mandate.sleeves.overlay_vol_pct"
			if m.Sleeves != nil {
				e.AllocationPct = m.Sleeves.OverlayVolPct
			}
		} else {
			e.AllocationKey = key("risk_allocation_pct")
			e.AllocationPct = s.RiskAllocationPct
		}
		if e.AllocationPct == nil {
			missing = append(missing, e.AllocationKey)
		}
		e.Unwritten = missing
		out = append(out, e)
	}
	return out
}

var (
	strategyIDShape = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	sleeveShape     = regexp.MustCompile(`^[a-z][a-z0-9_]{0,23}$`)
	specHashShape   = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// validateRegister refuses what makes the register contradictory: shapes,
// a reserved sleeve, one sleeve or one rule registered twice, the overlay
// with an allocation of its own, a scale off the ladder, and machine
// allocations above the budget's target. A missing key is never refused:
// it holds its strategy only.
func (m *ConstitutionMandate) validateRegister() error {
	if c := m.Core; c != nil {
		if s := c.SpecHash; s != "" && !specHashShape.MatchString(s) {
			return fmt.Errorf("mandate.core.spec_hash %q must be the lab registry's 64-character lower-case hex hash", s)
		}
		if s := c.Stage; s != "" && !validStage(s) {
			return fmt.Errorf("mandate.core.stage %q must be shadow, live or suspended", s)
		}
	}
	sleeves := map[string]string{}
	rules := map[string]string{}
	if m.Core != nil && m.Core.Rule != "" {
		rules[m.Core.Rule] = CoreStrategyID
	}
	allocated := 0.0
	ids := make([]string, 0, len(m.Strategies))
	for id := range m.Strategies {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		s := m.Strategies[id]
		key := "mandate.strategies." + id
		switch {
		case s == nil:
			continue
		case id == CoreStrategyID:
			return fmt.Errorf("[%s] is refused: the core has one form, [mandate.core]", key)
		case !strategyIDShape.MatchString(id):
			return fmt.Errorf("[%s]: a strategy id is lower-case letters, digits and hyphens, at most 32, starting with a letter", key)
		}
		for _, k := range []struct{ name, v string }{{"rule", s.Rule}, {"spec_hash", s.SpecHash}, {"sleeve", s.Sleeve}, {"stage", s.Stage}} {
			if k.v != strings.TrimSpace(k.v) {
				return fmt.Errorf("%s.%s %q carries spaces", key, k.name, k.v)
			}
		}
		if s.SpecHash != "" && !specHashShape.MatchString(s.SpecHash) {
			return fmt.Errorf("%s.spec_hash %q must be the lab registry's 64-character lower-case hex hash", key, s.SpecHash)
		}
		if s.Stage != "" && !validStage(s.Stage) {
			return fmt.Errorf("%s.stage %q must be shadow, live or suspended", key, s.Stage)
		}
		if v := s.ScalePct; v != nil && *v != 25 && *v != 50 && *v != 100 {
			return fmt.Errorf("%s.scale_pct must be 25, 50 or 100 (the ladder's small, larger and full steps)", key)
		}
		if err := validateEvaluatorKeys(key, s.ISIN, s.ConID, s.Exchange, s.Currency, s.Signal, s.BandPct, s.MinOrderBase, s.MaxValuePct, s.KillDrawdownK); err != nil {
			return err
		}
		if s.Sleeve != "" {
			switch {
			case !sleeveShape.MatchString(s.Sleeve):
				return fmt.Errorf("%s.sleeve %q: a sleeve is lower-case letters, digits and underscores, at most 24, starting with a letter", key, s.Sleeve)
			case s.Sleeve == "owner" || s.Sleeve == "rule" || s.Sleeve == CoreStrategyID:
				return fmt.Errorf("%s.sleeve %q is reserved", key, s.Sleeve)
			case sleeves[s.Sleeve] != "":
				return fmt.Errorf("%s.sleeve %q is also %s's: one strategy per sleeve", key, s.Sleeve, sleeves[s.Sleeve])
			}
			sleeves[s.Sleeve] = id
		}
		if s.Rule != "" {
			if other := rules[s.Rule]; other != "" {
				return fmt.Errorf("%s.rule %q is also %s's: one entry per rule", key, s.Rule, other)
			}
			rules[s.Rule] = id
		}
		if v := s.RiskAllocationPct; v != nil {
			if s.Sleeve == OverlaySleeve {
				return fmt.Errorf("%s.risk_allocation_pct is refused: the overlay's allocation is mandate.sleeves.overlay_vol_pct", key)
			}
			if math.IsNaN(*v) || math.IsInf(*v, 0) || *v <= 0 || *v > 100 {
				return fmt.Errorf("%s.risk_allocation_pct must be in (0, 100]", key)
			}
			allocated += *v
		}
	}
	if t := m.VolTargetPct; t != nil {
		machine := allocated
		if sl := m.Sleeves; sl != nil {
			for _, v := range []*float64{sl.CoreVolPct, sl.OverlayVolPct} {
				if v != nil {
					machine += *v
				}
			}
		}
		if machine > *t {
			return fmt.Errorf("mandate.sleeves.core_vol_pct + overlay_vol_pct + every mandate.strategies risk_allocation_pct (%g) must not exceed mandate.vol_target_pct (%g): the machine's allocations sit inside the budget", machine, *t)
		}
	}
	return nil
}

func validStage(s string) bool {
	return s == StageShadow || s == StageLive || s == StageSuspended
}

// validateEvaluatorKeys refuses an evaluator key of the wrong shape under
// a table (internal-docs/design/strategy-evaluator.md §2); a missing key
// is never refused here, it holds its strategy.
func validateEvaluatorKeys(key, isin string, conID *int, exchange, currency, signal string, bandPct, minOrderBase, maxValuePct, killDrawdownK *float64) error {
	finite := func(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }
	if isin != "" && !isinShape(isin) {
		return fmt.Errorf("%s.isin %q must be twelve upper-case letters and digits", key, isin)
	}
	if conID != nil && *conID < 0 {
		return fmt.Errorf("%s.con_id must be an IBKR contract id, or 0 to let the ISIN decide", key)
	}
	for _, k := range []struct{ name, v string }{{"exchange", exchange}, {"currency", currency}, {"signal", signal}} {
		if k.v != strings.TrimSpace(k.v) {
			return fmt.Errorf("%s.%s %q carries spaces", key, k.name, k.v)
		}
	}
	if exchange != strings.ToUpper(exchange) || currency != strings.ToUpper(currency) {
		return fmt.Errorf("%s.exchange and %s.currency are written in upper case", key, key)
	}
	if v := bandPct; v != nil && (!finite(*v) || *v <= 0 || *v >= 100) {
		return fmt.Errorf("%s.band_pct must be in (0, 100)", key)
	}
	if v := minOrderBase; v != nil && (!finite(*v) || *v < 0) {
		return fmt.Errorf("%s.min_order_base must not be negative", key)
	}
	if v := maxValuePct; v != nil && (!finite(*v) || *v <= 0 || *v > 100) {
		return fmt.Errorf("%s.max_value_pct must be in (0, 100]", key)
	}
	if v := killDrawdownK; v != nil && (!finite(*v) || *v <= 0) {
		return fmt.Errorf("%s.kill_drawdown_k must be positive: the drawdown bound in units of sleeve capital", key)
	}
	return nil
}
