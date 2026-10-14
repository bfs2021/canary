package rpc

import (
	"context"
	"time"

	"github.com/osauer/canary/v2/internal/core"
)

// MethodCoreTarget serves the daemon-owned core sleeve target: the month's
// decision under the compiled rule, the target shares, the holding, the
// band test and the action the rule would take
// (internal-docs/design/core-sleeve.md). Read-only: in this increment the
// core serves no proposal row and sends no order.
const MethodCoreTarget = "core.target"

// CoreTargetParams is the input for MethodCoreTarget. Refresh asks the
// daemon to measure the mandate again first.
type CoreTargetParams struct {
	Refresh bool `json:"refresh,omitempty"`
}

// Core target statuses.
const (
	// CoreStatusReady: the month is decided and the action is computed.
	CoreStatusReady = "ready"
	// CoreStatusHold: something the decision needs is missing, stale,
	// suspect or unwritten; Reasons name it. Buys stop; a sale the rule
	// wants goes to the owner.
	CoreStatusHold = "hold"
	// CoreStatusInactive: the constitution carries no [mandate.core].
	CoreStatusInactive = "inactive"
)

// CoreTargetResult is the core.target payload.
type CoreTargetResult struct {
	AsOf   time.Time `json:"as_of"`
	Active bool      `json:"active"`
	Status string    `json:"status"`
	// Message says why the core is inactive.
	Message string `json:"message,omitempty"`
	// Reasons name every hold.
	Reasons []string `json:"reasons,omitempty"`
	// ReadOnly is true while the core places nothing: the action is what
	// the rule would do, never a proposal or an order.
	ReadOnly bool `json:"read_only"`
	// Rule and SpecHash name the compiled rule and the lab spec it
	// implements (s0_core_sleeve); PolicyRule is the rule the policy names.
	Rule       string `json:"rule"`
	SpecID     string `json:"spec_id"`
	SpecHash   string `json:"spec_hash"`
	PolicyRule string `json:"policy_rule,omitempty"`
	// PolicyFingerprint identifies the constitution read.
	PolicyFingerprint *Fingerprint     `json:"policy_fingerprint,omitempty"`
	Policy            *CorePolicyView  `json:"policy,omitempty"`
	Instrument        *CoreInstrument  `json:"instrument,omitempty"`
	Decision          *CoreDecision    `json:"decision,omitempty"`
	Sizing            *CoreSizingView  `json:"sizing,omitempty"`
	Holding           *CoreHolding     `json:"holding,omitempty"`
	Action            *core.Decision   `json:"action,omitempty"`
	Mandate           *CoreMandateView `json:"mandate,omitempty"`
	// NextDecision is the next month's decision day on the exchange
	// calendar, when the calendar covers it.
	NextDecision string        `json:"next_decision,omitempty"`
	Evidence     *CoreEvidence `json:"evidence,omitempty"`
}

// CorePolicyView is [mandate.core] as the daemon read it, with the sleeve
// share from [mandate.sleeves]; nil numbers are unwritten.
type CorePolicyView struct {
	Rule         string   `json:"rule,omitempty"`
	ISIN         string   `json:"isin,omitempty"`
	ConID        *int     `json:"con_id,omitempty"`
	Exchange     string   `json:"exchange,omitempty"`
	Currency     string   `json:"currency,omitempty"`
	ScalePct     *float64 `json:"scale_pct,omitempty"`
	BandPct      *float64 `json:"band_pct,omitempty"`
	MinOrderBase *float64 `json:"min_order_base,omitempty"`
	MaxValuePct  *float64 `json:"max_value_pct,omitempty"`
	CoreVolPct   *float64 `json:"core_vol_pct,omitempty"`
	Unapproved   []string `json:"unapproved,omitempty"`
}

// CoreInstrument is the instrument the compiled rule trades.
type CoreInstrument struct {
	Symbol   string `json:"symbol"`
	ISIN     string `json:"isin"`
	ConID    int    `json:"con_id,omitempty"`
	Exchange string `json:"exchange"`
	Currency string `json:"currency"`
}

// CoreDecision is the month's decision: the signal, and when it was frozen.
type CoreDecision struct {
	core.Signal
	// FrozenAt is when the daemon first decided this month; the target is
	// frozen then and only lowered afterwards. Drift says a later reading
	// of the same window's closes differs from the frozen one.
	FrozenAt time.Time `json:"frozen_at,omitzero"`
	Drift    bool      `json:"drift,omitempty"`
}

// CoreSizingView is the target in money and shares: frozen at the
// decision, and at today's capital; Target is the lower of the two.
type CoreSizingView struct {
	Frozen     core.Size   `json:"frozen"`
	FrozenOn   core.Sizing `json:"frozen_on"`
	Current    core.Size   `json:"current"`
	CurrentOn  core.Sizing `json:"current_on"`
	Target     int         `json:"target_shares"`
	TargetBase float64     `json:"target_value_base"`
	// Holdable is the largest holding the mandate's headroom admits:
	// the book at most at 0.95 of the budget and the core's gap at most at
	// 0.95 of its cap. Nil when no headroom was measured (no buy wanted).
	Holdable *int `json:"holdable,omitempty"`
}

// CoreHolding is the confirmed holding of the pinned contract.
type CoreHolding struct {
	Shares    int     `json:"shares"`
	Price     float64 `json:"price"`
	ValueBase float64 `json:"value_base"`
	// PriceSource is "mark" (the held line) or "close" (the last close).
	PriceSource string `json:"price_source"`
}

// CoreMandateView is the mandate verdict the core read.
type CoreMandateView struct {
	Status         string    `json:"status"`
	EvaluatedAt    time.Time `json:"evaluated_at,omitzero"`
	BudgetAnnualSD float64   `json:"budget_annual_sd_base,omitempty"`
	RiskAnnualSD   float64   `json:"risk_annual_sd_base,omitempty"`
}

// CoreEvidence is every clock and source behind the target.
type CoreEvidence struct {
	SeriesKey string    `json:"series_key"`
	Closes    int       `json:"closes"`
	LastClose string    `json:"last_close,omitempty"`
	FetchedAt time.Time `json:"fetched_at,omitzero"`
	Source    string    `json:"source,omitempty"`
	Stale     bool      `json:"stale,omitempty"`
	Missing   bool      `json:"missing,omitempty"`
	// Calendar names the exchange calendar and its coverage.
	Calendar string `json:"calendar,omitempty"`
}

// FetchCoreTarget reads the daemon's core target. The daemon evaluates it
// (internal/core), so a reader compiled at an older version still shows
// the installed daemon's decision.
func FetchCoreTarget(ctx context.Context, conn interface {
	Call(context.Context, string, any, any) error
}, refresh bool) (CoreTargetResult, error) {
	var out CoreTargetResult
	if err := conn.Call(ctx, MethodCoreTarget, CoreTargetParams{Refresh: refresh}, &out); err != nil {
		return CoreTargetResult{}, err
	}
	return out, nil
}
