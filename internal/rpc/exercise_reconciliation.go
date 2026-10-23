package rpc

import "time"

const (
	// MethodOpportunitiesExercises lists durable pending exercise reservations.
	MethodOpportunitiesExercises = "opportunities.exercises"
	// MethodOpportunitiesReconcileExercise records human-attested IBKR completion evidence.
	MethodOpportunitiesReconcileExercise = "opportunities.reconcile_exercise"
)

// ExercisePendingResult lists pending exercises and their available evidence.
type ExercisePendingResult struct {
	Requests []ExercisePendingRequest `json:"requests"`
	AsOf     time.Time                `json:"as_of"`
}

// ExercisePendingRequest identifies one durable exercise reservation.
type ExercisePendingRequest struct {
	RequestRef  string                      `json:"request_ref"`
	Revision    string                      `json:"revision"`
	State       string                      `json:"state"`
	Symbol      string                      `json:"symbol"`
	Quantity    int                         `json:"quantity"`
	Option      ContractParams              `json:"option"`
	Underlying  ContractParams              `json:"underlying"`
	SubmittedAt time.Time                   `json:"submitted_at"`
	Evidence    []ExerciseEvidenceCandidate `json:"evidence"`
	Blockers    []TradingBlocker            `json:"blockers,omitempty"`
}

// ExerciseEvidenceCandidate identifies a complete imported IBKR exercise event.
type ExerciseEvidenceCandidate struct {
	ID string    `json:"id"`
	At time.Time `json:"at"`
}

// ExerciseReconcileParams identifies the request and evidence to resolve.
// ConfirmComplete is the human's attestation that this evidence resolves the
// exact request in full with no remainder pending. It is not inferred from
// similar positions or from a successful socket write.
type ExerciseReconcileParams struct {
	RequestRef      string `json:"request_ref"`
	Revision        string `json:"revision"`
	EvidenceID      string `json:"evidence_id"`
	ConfirmComplete bool   `json:"confirm_complete"`
	Origin          string `json:"origin"`
}

// ExerciseReconcileResult reports a durable human-attested evidence resolution.
type ExerciseReconcileResult struct {
	Accepted   bool      `json:"accepted"`
	Replay     bool      `json:"replay"`
	RequestRef string    `json:"request_ref"`
	Revision   string    `json:"revision"`
	EvidenceID string    `json:"evidence_id"`
	Provenance string    `json:"provenance"`
	AsOf       time.Time `json:"as_of"`
}
