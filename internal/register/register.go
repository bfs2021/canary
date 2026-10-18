// Package register is the compiled side of the strategy register
// (internal-docs/design/strategy-register.md; owner decision 2026-10-18
// 09:01 CEST: "one register for all machine strategies, the ladder stage in
// Canary's risk policy, shadow on the production path, and strategies
// declaring the data Canary collects"). The policy side is
// risk.ConstitutionMandate.Register(): one owner-written entry per machine
// strategy. This package lists the rules this Canary carries and resolves
// each entry's effective stage and the part of its risk allocation it
// reserves. It holds no state and performs no I/O; the daemon is its only
// caller.
package register

import (
	"fmt"
	"slices"
	"strings"

	"github.com/osauer/canary/v2/internal/core"
	"github.com/osauer/canary/v2/internal/risk"
)

// Form says which policy form a rule runs in: the core's [mandate.core], or
// a [mandate.strategies.<id>] entry.
type Form string

// The two forms.
const (
	FormCore  Form = "core"
	FormEntry Form = "entry"
)

// SeriesNeed is one daily series a rule reads: the instrument by ISIN and
// currency (the line IBKR lists for it, judged on its listing's calendar),
// and how many sessions the rule's window needs.
type SeriesNeed struct {
	ISIN     string `json:"isin,omitempty"`
	Currency string `json:"currency"`
	Symbol   string `json:"symbol,omitempty"`
	Sessions int    `json:"sessions"`
}

// ChainNeed is one option-chain underlying a rule reads, recorded on the
// recorder's plan of that name (only "v1" exists).
type ChainNeed struct {
	Underlying string `json:"underlying"`
	Plan       string `json:"plan"`
}

// Data is what a rule declares it reads, compiled with its version.
type Data struct {
	Series []SeriesNeed `json:"series,omitempty"`
	Chains []ChainNeed  `json:"chains,omitempty"`
}

// Rule is one compiled rule: the lab spec it implements, the policy form it
// runs in, whether this build serves its orders, its proposal bucket and its
// declared data.
type Rule struct {
	ID       string `json:"id"`
	SpecID   string `json:"spec_id"`
	SpecHash string `json:"spec_hash"`
	Form     Form   `json:"form"`
	// Sends is whether this build serves the rule's orders. A strategy whose
	// rule does not send runs as shadow whatever its stage says.
	Sends bool `json:"sends"`
	// Bucket is the proposal bucket of the rule's rows; never
	// pre-authorisable.
	Bucket string `json:"bucket"`
	Data   Data   `json:"data"`
}

// CoreRebalanceBucket is the core's proposal bucket (core-sleeve.md §2).
const CoreRebalanceBucket = "core_rebalance"

// Catalogue is the rules this Canary carries. core-ibcf-v1 sends no orders
// in this build: its monthly rows are core-sleeve.md phase 2.
func Catalogue() []Rule {
	return []Rule{{
		ID: core.RuleID, SpecID: core.SpecID, SpecHash: core.SpecHash, Form: FormCore, Sends: false, Bucket: CoreRebalanceBucket,
		Data: Data{Series: []SeriesNeed{{ISIN: core.ISIN, Currency: core.Currency, Symbol: core.Symbol, Sessions: core.WindowCloses}}},
	}}
}

// Find returns the rule with the id, or false.
func Find(rules []Rule, id string) (Rule, bool) {
	i := slices.IndexFunc(rules, func(r Rule) bool { return r.ID == id })
	if i < 0 {
		return Rule{}, false
	}
	return rules[i], true
}

// State is an entry's effective stage.
type State string

// The effective stages: on hold, in shadow, live, or suspended.
const (
	StateHold      State = "hold"
	StateShadow    State = "shadow"
	StateLive      State = "live"
	StateSuspended State = "suspended"
)

// Resolved is one entry as this build runs it.
type Resolved struct {
	Entry risk.StrategyEntry `json:"entry"`
	// Rule is the compiled rule the entry names; nil when it is not
	// compiled.
	Rule  *Rule `json:"rule,omitempty"`
	State State `json:"state"`
	// ScalePct is the scale the rule sizes at, in shadow and live.
	ScalePct float64 `json:"scale_pct"`
	// ReservedPct is the part of the allocation reserved, in percent: the
	// scale while live, 0 in every other state.
	ReservedPct float64 `json:"reserved_pct"`
	// AllocationPct is the entry's risk allocation, percent a year of the
	// budget base at full room; 0 while unwritten.
	AllocationPct float64 `json:"allocation_pct"`
	// Reasons name every hold; Notes say why a written stage runs as
	// another (live written, the build sends nothing; a stage unwritten).
	Reasons []string `json:"reasons,omitempty"`
	Notes   []string `json:"notes,omitempty"`
}

// Reserves reports whether the entry reserves any of its allocation.
func (r Resolved) Reserves() bool { return r.State == StateLive && r.ReservedPct > 0 }

// Resolve gives each entry its effective state against the compiled rules.
// An entry holds on an unwritten key, a rule this Canary does not carry, a
// written spec hash that is not the rule's, or a rule in the other form; a
// live stage whose rule this build does not send runs as shadow. Only a
// live entry reserves its allocation, at its scale.
func Resolve(entries []risk.StrategyEntry, rules []Rule) []Resolved {
	out := make([]Resolved, 0, len(entries))
	for _, e := range entries {
		out = append(out, resolveOne(e, rules))
	}
	return out
}

func resolveOne(e risk.StrategyEntry, rules []Rule) Resolved {
	r := Resolved{Entry: e}
	if e.AllocationPct != nil {
		r.AllocationPct = *e.AllocationPct
	}
	if e.ScalePct != nil {
		r.ScalePct = *e.ScalePct
	}
	hold := func(format string, args ...any) { r.Reasons = append(r.Reasons, fmt.Sprintf(format, args...)) }
	if len(e.Unwritten) > 0 {
		hold("%s unwritten", strings.Join(e.Unwritten, ", "))
	}
	if e.Rule != "" {
		rule, ok := Find(rules, e.Rule)
		switch {
		case !ok:
			hold("rule %s is not compiled into this Canary", e.Rule)
		default:
			r.Rule = &rule
			if e.SpecHash != "" && e.SpecHash != rule.SpecHash {
				hold("spec_hash %s is not the spec rule %s implements (%s)", e.SpecHash, rule.ID, rule.SpecHash)
			}
			if want := formOf(e); rule.Form != want {
				if rule.Form == FormCore {
					hold("rule %s runs only as [mandate.core]", rule.ID)
				} else {
					hold("[mandate.core] runs only the core's rule; %s is a [mandate.strategies] rule", rule.ID)
				}
			}
		}
	}
	if len(r.Reasons) > 0 {
		r.State, r.ScalePct = StateHold, 0
		return r
	}
	stage := e.Stage
	if stage == "" {
		// Only the core's first form leaves its stage unwritten without a
		// hold: it runs in shadow, as before the register.
		stage = risk.StageShadow
		r.Notes = append(r.Notes, "stage unwritten: shadow, as before the register")
	}
	if e.Form == risk.StrategyFormCore && e.SpecHash == "" {
		r.Notes = append(r.Notes, "spec_hash unwritten: the compiled rule's own spec stands")
	}
	switch {
	case stage == risk.StageSuspended, r.ScalePct <= 0:
		r.State, r.ScalePct = StateSuspended, 0
	case stage == risk.StageLive && r.Rule.Sends:
		r.State, r.ReservedPct = StateLive, r.ScalePct
	case stage == risk.StageLive:
		r.State = StateShadow
		r.Notes = append(r.Notes, fmt.Sprintf("live written; this build sends no orders for %s, so it runs as shadow", r.Rule.ID))
	default:
		r.State = StateShadow
	}
	return r
}

func formOf(e risk.StrategyEntry) Form {
	if e.Form == risk.StrategyFormCore {
		return FormCore
	}
	return FormEntry
}
