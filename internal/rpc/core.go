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

// MethodCoreResolve lists every listing IBKR knows for an ISIN (by default
// the one [mandate.core] pins, else the compiled rule's), so the owner can
// write the contract id into [mandate.core]. Read-only: it never chooses a
// listing and never writes the policy.
const MethodCoreResolve = "core.resolve"

// CoreResolveParams is the input for MethodCoreResolve. ISIN overrides the
// default; Currency, when set, narrows every request form to it.
type CoreResolveParams struct {
	ISIN     string `json:"isin,omitempty"`
	Currency string `json:"currency,omitempty"`
}

// CoreResolveResult is the core.resolve payload: every listing found, the
// contract id the policy pins (nil while unwritten or 0), and every
// request form with IBKR's answer.
type CoreResolveResult struct {
	AsOf time.Time `json:"as_of"`
	ISIN string    `json:"isin"`
	// ISINSource is "flag", "policy" or "rule": where the ISIN came from.
	ISINSource string        `json:"isin_source"`
	Currency   string        `json:"currency,omitempty"`
	Listings   []CoreListing `json:"listings"`
	// PinnedConID is [mandate.core] con_id when written and positive;
	// PinnedFound says a listing carries it.
	PinnedConID *int                 `json:"pinned_con_id,omitempty"`
	PinnedFound bool                 `json:"pinned_found,omitempty"`
	Attempts    []CoreResolveAttempt `json:"attempts"`
	// Message says what the owner does next.
	Message string `json:"message"`
}

// CoreListing is one listing IBKR answered for the ISIN.
type CoreListing struct {
	ConID        int    `json:"con_id"`
	Symbol       string `json:"symbol"`
	LocalSymbol  string `json:"local_symbol,omitempty"`
	SecType      string `json:"sec_type,omitempty"`
	Exchange     string `json:"exchange,omitempty"`
	PrimaryExch  string `json:"primary_exchange,omitempty"`
	Currency     string `json:"currency,omitempty"`
	TradingClass string `json:"trading_class,omitempty"`
	LongName     string `json:"long_name,omitempty"`
	// ISIN is the one IBKR's frame named; empty when it named none.
	ISIN  string   `json:"isin,omitempty"`
	Forms []string `json:"forms"`
	// Pinned says [mandate.core] con_id names this listing.
	Pinned bool `json:"pinned,omitempty"`
}

// CoreResolveAttempt is one request form and IBKR's answer: line (with how
// many listings it kept), no_line, rejected (with IBKR's code and text) or
// failed.
type CoreResolveAttempt struct {
	Form    string `json:"form"`
	Outcome string `json:"outcome"`
	Lines   int    `json:"lines,omitempty"`
	Code    int    `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

// FetchCoreResolve asks the daemon for the listings of an ISIN.
func FetchCoreResolve(ctx context.Context, conn interface {
	Call(context.Context, string, any, any) error
}, params CoreResolveParams) (CoreResolveResult, error) {
	var out CoreResolveResult
	if err := conn.Call(ctx, MethodCoreResolve, params, &out); err != nil {
		return CoreResolveResult{}, err
	}
	return out, nil
}
