package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/osauer/canary/v2/internal/rpc"
)

// budgetPlanIndent aligns the governor's candidates and plan under the value
// column of the Budget header row.
const budgetPlanIndent = "               "

// renderProposalBudgetPlan prints the premium budget governor's ranked
// candidates and its whole plan under the Budget header (amendment
// 2026-09-30): what it would sell first and why, and every order the
// measured excess needs, with the cycle each can go in.
func renderProposalBudgetPlan(out io.Writer, st *rpc.TradeProposalBudgetStatus) {
	if st == nil {
		return
	}
	for i, c := range st.Candidates {
		fmt.Fprintf(out, "%s%-11s%d. %s  %d ct × %s  %s\n", budgetPlanIndent, budgetPlanLabel(i, "candidates"), c.Rank,
			queuedContractLabel(c.Contract), c.Contracts, formatProposalMoney(c.UnitValueBase, st.BaseCurrency), c.Why)
	}
	for i, o := range st.Plan {
		fmt.Fprintf(out, "%s%-11s%d. sell %d %s · raises %s · cycle %d · rank %d\n", budgetPlanIndent, budgetPlanLabel(i, "plan"), i+1,
			o.Contracts, queuedContractLabel(o.Contract), formatProposalMoney(o.RaisesBase, st.BaseCurrency), o.Cycle, o.Rank)
	}
}

func budgetPlanLabel(i int, label string) string {
	if i > 0 {
		return ""
	}
	return label
}

// formatProposalBudgetGate names the advisory review state of the Rulebook
// limits a rulebook-basis governor sells against, when they are not a
// reviewed owner file, and a ranking made without a Rulebook result.
func formatProposalBudgetGate(st *rpc.TradeProposalBudgetStatus) []string {
	var parts []string
	if review := budgetRulebookReviewText(st.RulebookReview); review != "" {
		parts = append(parts, review)
	}
	if st.RankingWithoutRulebook {
		parts = append(parts, "ranked without a Rulebook result")
	}
	return parts
}

// budgetRulebookReviewText is the header's word for a review state; empty
// when the limits are a reviewed owner file or the basis reads none.
func budgetRulebookReviewText(review string) string {
	switch review {
	case rpc.BudgetRulebookUnreviewed:
		return "Rulebook limits: Canary's defaults, not yet reviewed"
	case rpc.BudgetRulebookNoFile:
		return "Rulebook limits: no Rulebook policy file; compiled defaults"
	case rpc.BudgetRulebookDrift:
		return "Rulebook limits: the file on disk is not the one in force"
	case rpc.BudgetRulebookError:
		return "Rulebook limits: the file could not be read"
	}
	return ""
}

// formatProposalMandateStatus is the one-line account of the mandate's
// forced reduction for the list header: its state and why.
func formatProposalMandateStatus(st *rpc.TradeProposalMandateStatus) string {
	if st == nil {
		return ""
	}
	return briefJoin(strings.ReplaceAll(st.State, "_", " "), st.Reason)
}

// formatProposalMandate is one mandate_reduction row in plain words: its
// place in the batch, why it is there, its share of the risk and the verdict
// it was sized on.
func formatProposalMandate(m *rpc.TradeProposalMandate) string {
	if m == nil {
		return ""
	}
	parts := []string{fmt.Sprintf("rank %d of %d", m.Rank, max(m.BatchRows, m.Rank)), mandatePhaseWords(m.Phase), mandateShare(m.ContributionPct) + " of the risk"}
	verdict := fmt.Sprintf("sized on the %s verdict of %s", m.VerdictStatus, m.VerdictAt.Local().Format("15:04 MST"))
	if m.BudgetAnnualSD > 0 {
		verdict += fmt.Sprintf(": risk %s a year, %s of the budget %s", mandateAmount(m.RiskAnnualBase, m.BaseCurrency),
			mandatePct(m.RiskAnnualBase/m.BudgetAnnualSD*100), mandateAmount(m.BudgetAnnualSD, m.BaseCurrency))
	}
	parts = append(parts, verdict)
	if !m.ConfirmedAt.IsZero() {
		parts = append(parts, "kept by the verdict of "+m.ConfirmedAt.Local().Format("15:04 MST"))
	}
	parts = append(parts, "this cut takes "+mandateAmount(m.RiskCutAnnualBase, m.BaseCurrency)+" a year off")
	if m.Held {
		parts = append(parts, "held: "+m.HoldReason)
	}
	return briefJoin(parts...)
}
