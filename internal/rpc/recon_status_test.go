package rpc

import "testing"

// The two evaluation reasons added on 2026-10-18 are valid only under the
// state each belongs to: a stale report needs attention, a pending
// evaluation waits.
func TestValidateReconAutomationStatusEvaluationReasons(t *testing.T) {
	current := ReconFetchStatus{Configured: true, State: ReconReportStateCurrent}
	for _, tc := range []struct {
		name          string
		state, reason string
		valid         bool
	}{
		{"stale needs attention", ReconEvaluationStateAttentionRequired, ReconEvaluationReasonReportStale, true},
		{"stale is not a failure", ReconEvaluationStateFailed, ReconEvaluationReasonReportStale, false},
		{"pending waits", ReconEvaluationStateWaiting, ReconEvaluationReasonEvaluationPending, true},
		{"pending is not complete", ReconEvaluationStateComplete, ReconEvaluationReasonEvaluationPending, false},
		{"pending is not attention", ReconEvaluationStateAttentionRequired, ReconEvaluationReasonEvaluationPending, false},
		{"failed keeps its reasons", ReconEvaluationStateFailed, ReconEvaluationReasonEvaluationFailed, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateReconAutomationStatus(ReconAutomationStatus{Report: current,
				Evaluation: ReconEvaluationStatus{State: tc.state, Reason: tc.reason, Detail: "why, in words"}})
			if (err == nil) != tc.valid {
				t.Fatalf("%s/%s: err=%v, want valid=%v", tc.state, tc.reason, err, tc.valid)
			}
		})
	}
}
