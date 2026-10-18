package rpc

import "time"

// The shadow record (internal-docs/design/strategy-register.md, section 3):
// a strategy in shadow runs its production code on a paper position, and
// each decision, intent and simulated fill is one append-only record. It is
// research evidence for the lab's R2 gate; nothing in it is a proposal or an
// order, and no order path reads it.

// Shadow record kinds. A decision is the rule's frozen reading, a hold a
// decision the rule measured and could not freeze, an intent the order the
// rule would send for a session on the shadow holding, and a fill its
// simulated execution.
const (
	ShadowKindDecision = "decision"
	ShadowKindHold     = "hold"
	ShadowKindIntent   = "intent"
	ShadowKindFill     = "fill"
)

// ShadowFillConvention is how a shadow fill is simulated: the lab's R2
// convention, not the midpoint limit a live row sends.
const ShadowFillConvention = "the whole quantity at the session's close (the lab's R2 convention)"

// ShadowRecord is one record. Session is the decision date for a decision,
// a hold or an intent, and the session filled for a fill. SpecHash is the
// spec the compiled rule implements; SpecCited says whether the policy cites
// it or the compiled one stands.
type ShadowRecord struct {
	Version           int             `json:"version"`
	Strategy          string          `json:"strategy"`
	Rule              string          `json:"rule"`
	SpecHash          string          `json:"spec_hash"`
	SpecCited         bool            `json:"spec_cited"`
	Stage             string          `json:"stage"`
	PolicyFingerprint string          `json:"policy_fingerprint,omitempty"`
	Kind              string          `json:"kind"`
	Session           string          `json:"session"`
	RecordedAt        time.Time       `json:"recorded_at"`
	Status            string          `json:"status,omitempty"`
	Reasons           []string        `json:"reasons,omitempty"`
	Decision          *ShadowDecision `json:"decision,omitempty"`
	Intent            *ShadowIntent   `json:"intent,omitempty"`
	Fill              *ShadowFill     `json:"fill,omitempty"`
}

// ShadowDecision is the rule's reading at a decision: for the core, the
// trend, the volatility it sized on and which estimate binds, the target in
// shares at the written scale, and the digest of the frozen inputs.
type ShadowDecision struct {
	TrendOn      bool    `json:"trend_on"`
	Sigma        float64 `json:"sigma"`
	Binding      string  `json:"binding,omitempty"`
	TargetShares int     `json:"target_shares"`
	ScalePct     float64 `json:"scale_pct"`
	Digest       string  `json:"digest,omitempty"`
}

// ShadowIntent is what the rule would do for ForSession on the shadow
// holding: its action kind and why, and the signed quantity (positive buys,
// negative sells, zero for none or a wait). The live row's terms join it
// when the rule's row builder exists.
type ShadowIntent struct {
	ForSession    string  `json:"for_session"`
	Kind          string  `json:"kind"`
	Quantity      int     `json:"quantity"`
	Why           string  `json:"why"`
	Diff          int     `json:"diff"`
	BandShares    float64 `json:"band_shares"`
	HoldingBefore int     `json:"holding_before"`
}

// ShadowFill is an intent's simulated fill: the signed quantity at the
// price, and the shadow holding after it; Unobserved says why a fill has no
// price (the session's close is missing).
type ShadowFill struct {
	Quantity     int     `json:"quantity"`
	Price        float64 `json:"price,omitempty"`
	HoldingAfter int     `json:"holding_after"`
	Convention   string  `json:"convention"`
	Unobserved   string  `json:"unobserved,omitempty"`
}

// ShadowView is a strategy's shadow record as a read serves it: its stage,
// the shadow holding, how many records there are and the newest ones.
type ShadowView struct {
	Strategy    string         `json:"strategy"`
	Stage       string         `json:"stage"`
	Holding     int            `json:"holding"`
	Records     int            `json:"records"`
	Recent      []ShadowRecord `json:"recent,omitempty"`
	Unavailable string         `json:"unavailable,omitempty"`
}
