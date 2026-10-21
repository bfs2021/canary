package rpc

import "time"

// MethodAddCallPlan sizes a long call against an owner budget, without submission.
const MethodAddCallPlan = "add.call-plan"

// AddCallPlanParams contains exact intended terms and the owner's debit ceiling.
type AddCallPlanParams struct {
	RoundSize  bool           `json:"round_size,omitempty"`
	Contract   ContractParams `json:"contract"`
	LimitPrice float64        `json:"limit_price"`
	Budget     float64        `json:"budget"`
}

// AddCallPlanResult carries a checked lot, not a maximum-capacity claim or token.
type AddCallPlanResult struct {
	RoundSize  bool           `json:"round_size,omitempty"`
	OverBudget bool           `json:"over_budget,omitempty"`
	Contract   ContractParams `json:"contract"`
	LimitPrice float64        `json:"limit_price"`
	Budget     float64        `json:"budget"`
	Quantity   int            `json:"quantity"`
	FeeBound   float64        `json:"fee_bound"`
	Debit      float64        `json:"debit"`
	AsOf       time.Time      `json:"as_of"`
}
