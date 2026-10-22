package daemon

import (
	"testing"
	"time"

	"github.com/osauer/canary/v2/internal/mandate"
	"github.com/osauer/canary/v2/internal/risk"
	"github.com/osauer/canary/v2/internal/rpc"
)

func reductionRoleBook(stockQuantity float64) *rpc.PositionsResult {
	row := budgetOptionLeg("SPY", 990001, "P", 40, 500, 0)
	row.Currency, row.Delta, row.Underlying, row.FXRate = "USD", new(-0.5), new(100.0), new(1.0)
	pos := &rpc.PositionsResult{Portfolio: &rpc.PositionsPortfolio{BaseCurrency: "USD"}, Options: []rpc.PositionView{row}, Stocks: []rpc.PositionView{{Symbol: "SYNTH", ConID: 990002, SecType: "STK", Currency: "USD", Quantity: stockQuantity, Mark: 100, MarketValue: stockQuantity * 100, MarketValueBase: new(stockQuantity * 100)}}}
	pos.ByUnderlying = groupByUnderlying(pos.Stocks, pos.Options, "USD", nil)
	return pos
}

func TestReductionUsesRulebookIndexPutClassification(t *testing.T) {
	pol := risk.DefaultRulebookPolicy()
	policy := budgetTestPolicy(rpc.BudgetReductionModeActive, 0, 0)
	policy.Buckets.BudgetReduction.Basis = rpc.BudgetBasisRulebook
	policy.Buckets.TrailingStop.Options.DefaultIndexPutsProtection = true
	engine := &proposalEngine{}
	for _, closed := range []bool{false, true} {
		now := optionExitTestTime()
		if closed {
			now = now.Add(-12 * time.Hour)
		}
		for _, protective := range []bool{false, true} {
			stockQuantity := 100.0
			if protective {
				stockQuantity = 10000
			}
			pos := reductionRoleBook(stockQuantity)
			in := risk.RuleInputs{BaseCurrency: "USD", NLVBase: new(100000.0), Positions: risk.SourceState{Healthy: true}, Account: risk.SourceState{Healthy: true}, Names: mapRuleNames(pos, pol, "USD")}
			evaluation := risk.EvaluateRulebook(in, pol)
			var premium risk.RuleRow
			for _, row := range evaluation.Rows {
				if row.ID == risk.RuleOptionLinePremium {
					premium = row
				}
			}
			input := budgetGovernorInput{Rulebook: pol, NLVBase: in.NLVBase, AccountBaseCurrency: "USD"}
			rows, _ := engine.budgetReductionProposals(policy, rpc.ProtectionPolicyStatus{}, input, nil, pos, rpc.TradeProposalSourceFingerprints{}, nil, brokerStateScope{}, now)
			if !protective && (premium.Status != risk.RuleStatusAct || len(rows) == 0) {
				t.Errorf("closed=%t: directional size breach %s has %d reduction proposals", closed, premium.Status, len(rows))
			}
			if protective && len(rows) != 0 {
				t.Errorf("closed=%t: actual protection offered for reduction", closed)
			}
			candidateInput := mandateReductionInput{Input: &mandate.Input{Legs: []mandate.Leg{{ID: mandateConLegID(990001), Underlying: "SPY", Kind: mandate.KindOption, Quantity: 40, Multiplier: 100, Right: "P"}}}}
			candidates, _, _ := engine.mandateCandidates(policy, candidateInput, pos, now)
			if (len(candidates) > 0) == protective {
				t.Errorf("closed=%t protective=%t: mandate has %d candidates", closed, protective, len(candidates))
			}
		}
	}
}

func TestReductionIndexPutRequiresFreshEconomicsBeforeAdmission(t *testing.T) {
	for _, bucket := range []string{rpc.TradeProposalBucketBudgetReduction, rpc.TradeProposalBucketMandateReduction} {
		p := rpc.TradeProposal{Symbol: "SPY", Bucket: bucket, Contract: rpc.ContractParams{Right: "P"}}
		for _, bind := range []bool{false, true} {
			if blockers := (&proposalEngine{}).checkOptionExitEconomics(t.Context(), p, nil, bind); len(blockers) == 0 {
				t.Errorf("%s bind=%t admitted without fresh complete-book evidence", bucket, bind)
			}
		}
	}
}

func TestReductionUnknownIndexPutIsHeldWithoutCallingItProtection(t *testing.T) {
	pos := reductionRoleBook(100)
	pos.Options[0].Delta = nil
	pos.ByUnderlying = groupByUnderlying(pos.Stocks, pos.Options, "USD", nil)
	pol := risk.DefaultRulebookPolicy()
	policy := budgetTestPolicy(rpc.BudgetReductionModeActive, 0, 0)
	policy.Buckets.BudgetReduction.Basis = rpc.BudgetBasisRulebook
	input := budgetGovernorInput{Rulebook: pol, NLVBase: new(100000.0), AccountBaseCurrency: "USD"}
	rows, st := (&proposalEngine{}).budgetReductionProposals(policy, rpc.ProtectionPolicyStatus{}, input, nil, pos, rpc.TradeProposalSourceFingerprints{}, nil, brokerStateScope{}, optionExitTestTime())
	if len(rows) != 0 || st.ProtectionLegs != 0 || st.ExcludedLegs != 1 {
		t.Fatalf("unknown role lost: rows=%d status=%+v", len(rows), st)
	}
}
