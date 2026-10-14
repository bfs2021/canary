package rpc

import (
	"context"
	"time"

	"github.com/osauer/canary/v2/internal/mandate"
)

// MethodMandateRisk serves the daemon-owned mandate risk verdict: the
// ex-ante volatility of the book by historical simulation against the
// owner's budget (internal-docs/design/mandate-risk-budget.md).
const MethodMandateRisk = "mandate.risk"

// MandateRiskParams is the input for MethodMandateRisk. Refresh asks the
// daemon to read the account and positions again before judging; otherwise
// a verdict younger than its refresh interval is served as it is.
type MandateRiskParams struct {
	Refresh bool `json:"refresh,omitempty"`
}

// MandateBudgetBlockerCode is the blocker of a risk-adding order the mandate
// gate refuses: the verdict is not within, or the addition would take the
// risk over the budget.
const MandateBudgetBlockerCode = "mandate_budget"

// MandateRiskResult is the mandate.risk payload. Active is false, with
// Message, while the constitution carries no [mandate] table; the verdict
// is then absent and the gate inactive.
type MandateRiskResult struct {
	AsOf   time.Time `json:"as_of"`
	Active bool      `json:"active"`
	// Message says why the mandate is inactive, or how the verdict was read.
	Message string `json:"message,omitempty"`
	// PolicyFingerprint identifies the constitution the verdict read.
	PolicyFingerprint *Fingerprint `json:"policy_fingerprint,omitempty"`
	// Policy echoes the mandate numbers in force.
	Policy *MandatePolicyView `json:"policy,omitempty"`
	// Verdict is the daemon's evaluation; nil while inactive.
	Verdict *mandate.Verdict `json:"verdict,omitempty"`
	// Evidence carries the clocks the verdict rests on.
	Evidence *MandateEvidence `json:"evidence,omitempty"`
	// Gate says how the additions gate reads the verdict right now.
	Gate *MandateGateView `json:"gate,omitempty"`
}

// MandatePolicyView is the [mandate] table as the daemon read it.
type MandatePolicyView struct {
	MaxLossPct     float64 `json:"max_loss_pct"`
	VolTargetPct   float64 `json:"vol_target_pct"`
	TaxReserveBase float64 `json:"tax_reserve_base"`
	StartedAt      string  `json:"started_at"`
	GapShockPct    float64 `json:"gap_shock_pct"`
	GapRoomPct     float64 `json:"gap_room_pct"`
	// Sleeves are the owner's recorded volatility shares; nil when unwritten.
	Sleeves *MandateSleevesView `json:"sleeves,omitempty"`
	// Unapproved names the material keys still unwritten.
	Unapproved []string `json:"unapproved,omitempty"`
}

// MandateSleevesView echoes [mandate.sleeves]; nil means unwritten.
type MandateSleevesView struct {
	OwnerVolPct   *float64 `json:"owner_vol_pct,omitempty"`
	CoreVolPct    *float64 `json:"core_vol_pct,omitempty"`
	OverlayVolPct *float64 `json:"overlay_vol_pct,omitempty"`
}

// MandateEvidence is every clock behind the verdict.
type MandateEvidence struct {
	// EvaluatedAt is when the daemon ran the simulation.
	EvaluatedAt time.Time `json:"evaluated_at,omitzero"`
	// EquityAsOf and PositionsAsOf are the reads the verdict used.
	EquityAsOf    time.Time `json:"equity_as_of,omitzero"`
	PositionsAsOf time.Time `json:"positions_as_of,omitzero"`
	// Series lists each decision-grade series with its clocks.
	Series []MandateSeriesEvidence `json:"series,omitempty"`
}

// MandateSeriesEvidence is one decision-grade series' provenance.
type MandateSeriesEvidence struct {
	Key string `json:"key"`
	// Closes is how many daily closes the series holds; LastClose the date
	// of the newest.
	Closes    int    `json:"closes"`
	LastClose string `json:"last_close,omitempty"`
	// FetchedAt is the source time of the last successful broker read, and
	// Source the broker's basis label.
	FetchedAt time.Time `json:"fetched_at,omitzero"`
	Source    string    `json:"source,omitempty"`
	Stale     bool      `json:"stale,omitempty"`
	Missing   bool      `json:"missing,omitempty"`
}

// MandateGateView is the additions gate as it reads the verdict now.
type MandateGateView struct {
	// RefusesAdditions is true whenever the verdict is not within, or is
	// older than the gate accepts.
	RefusesAdditions bool   `json:"refuses_additions"`
	Reason           string `json:"reason,omitempty"`
}

// FetchMandateRisk reads the daemon's mandate verdict. The daemon evaluates
// it (mandate.Evaluate), so a reader compiled at an older version still
// shows the installed daemon's judgement.
func FetchMandateRisk(ctx context.Context, conn interface {
	Call(context.Context, string, any, any) error
}, refresh bool) (MandateRiskResult, error) {
	var out MandateRiskResult
	if err := conn.Call(ctx, MethodMandateRisk, MandateRiskParams{Refresh: refresh}, &out); err != nil {
		return MandateRiskResult{}, err
	}
	return out, nil
}
