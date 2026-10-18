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
	// Reduction is the forced reduction as the proposal engine last served
	// it: the batch waiting, held or withdrawn. Nil while inactive or before
	// the engine has generated.
	Reduction *MandateReductionView `json:"reduction,omitempty"`
}

// MandateReductionView is the forced reduction in brief for the mandate
// surface: how many rows wait, until when the veto runs, whether the owner
// pre-authorised the bucket, and a hold or a withdrawal.
type MandateReductionView struct {
	State  string `json:"state"`
	Reason string `json:"reason,omitempty"`
	// Rows is the batch; Pending the rows the daemon will place after their
	// window (pre-authorised, with a record waiting); VetoUntil the latest
	// window end among them.
	Rows          int       `json:"rows"`
	Pending       int       `json:"pending,omitempty"`
	VetoUntil     time.Time `json:"veto_until,omitzero"`
	PreAuthorised bool      `json:"pre_authorised"`
	Held          bool      `json:"held,omitempty"`
	HoldReason    string    `json:"hold_reason,omitempty"`
	Withdrawn     int       `json:"withdrawn,omitempty"`
	// Orders is the batch in dispatch order.
	Orders []MandateReductionOrder `json:"orders,omitempty"`
}

// MandateReductionOrder is one row of the forced reduction as the mandate
// surface lists it: what it sells or buys back, why, and when the daemon
// places it unless vetoed.
type MandateReductionOrder struct {
	Rank     int            `json:"rank"`
	Key      string         `json:"key"`
	Contract ContractParams `json:"contract"`
	Action   string         `json:"action"`
	Quantity int            `json:"quantity"`
	// Phase is expiry, gap or risk (MandatePhase*); ContributionPct the
	// leg's share of the variance on the verdict it was sized on.
	Phase           string  `json:"phase"`
	ContributionPct float64 `json:"contribution_pct"`
	// Automatic is the row's automatic-submission state (pending, deferred,
	// submitting, submitted, vetoed, superseded or failed), empty when no
	// record exists; SubmitAt is when its veto window ends.
	Automatic string    `json:"automatic,omitempty"`
	SubmitAt  time.Time `json:"submit_at,omitzero"`
	// Held says the row waits for a fresh verdict; Blocked names the first
	// blocker when the row cannot be sent right now.
	Held    bool   `json:"held,omitempty"`
	Blocked string `json:"blocked,omitempty"`
}

// MandatePolicyView is the [mandate] table as the daemon read it.
type MandatePolicyView struct {
	MaxLossPct     float64 `json:"max_loss_pct"`
	VolTargetPct   float64 `json:"vol_target_pct"`
	TaxReserveBase float64 `json:"tax_reserve_base"`
	StartedAt      string  `json:"started_at"`
	GapShockPct    float64 `json:"gap_shock_pct"`
	// IndexGapShockPct is the broad index gap; nil while unwritten, when
	// every underlying takes GapShockPct.
	IndexGapShockPct *float64 `json:"index_gap_shock_pct,omitempty"`
	GapRoomPct       float64  `json:"gap_room_pct"`
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
	// RefusesAdditions is true whenever the verdict is not within, is older
	// than the gate accepts, or the owner's allocation is over (allocations, 2026-10-18):
	// it reads the gate for the owner's additions.
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
