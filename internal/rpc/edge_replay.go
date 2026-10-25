package rpc

import (
	"fmt"
	"math"
	"slices"
	"strings"
	"time"
)

// Edge exit replay (internal-docs/design/long-option-exits.md, LO-V1): a
// read-only replay of long-option exit rules over the owner's own option
// trades. Entries are the owner's buys; the rule set under test replaces the
// exits. It never touches the order path.
const (
	MethodEdgeReplay = "edge.replay"

	// EdgeReplaySchemaVersion is v2 since names without the daily history
	// their days need leave every run and are listed in missing_history.
	EdgeReplaySchemaVersion = "edge-replay-v2"

	// EdgeReplayRuleLossExit and the five rules after it are the single
	// rules; each runs alone as well as inside the sets.
	EdgeReplayRuleLossExit    = "loss_exit"
	EdgeReplayRuleExpiryClose = "expiry_close"
	EdgeReplayRuleProfitTrail = "profit_trail"
	EdgeReplayRuleRecoverCost = "recover_cost"
	EdgeReplayRuleTrimToCap   = "trim_to_cap"
	EdgeReplayRuleStockTrail  = "stock_trail"

	// EdgeReplaySetActual is the owner's actual path; sets B, C and D replace
	// the exits.
	EdgeReplaySetActual     = "A"
	EdgeReplaySetTodays     = "B"
	EdgeReplaySetHarvest    = "C"
	EdgeReplaySetStockTrail = "D"

	// EdgeReplayPricesActual and the two after it are the price bases of a
	// run.
	EdgeReplayPricesActual   = "actual"
	EdgeReplayPricesRecorded = "recorded"
	EdgeReplayPricesModelled = "modelled"

	// EdgeReplayHistoryGatewayOffline and the three after it say why an
	// underlying's daily history is missing: the broker gateway is offline
	// and Canary retains none for the days; the broker no longer serves the
	// days and Canary did not retain them; the broker has no history for the
	// instrument; the broker's read failed otherwise.
	EdgeReplayHistoryGatewayOffline        = "gateway_offline"
	EdgeReplayHistoryNotRetained           = "not_retained"
	EdgeReplayHistoryInstrumentUnavailable = "instrument_unavailable"
	EdgeReplayHistoryReadFailed            = "read_failed"

	// EdgeReplayReconciledFully and the two after it are the reconciliation
	// statuses: every check within tolerance and every contract with an Edge
	// cycle compared; within tolerance on the contracts compared, with
	// others named in not_compared; a check beyond tolerance.
	EdgeReplayReconciledFully  = "reconciled"
	EdgeReplayReconciledPartly = "partial"
	EdgeReplayUnreconciled     = "unreconciled"

	// EdgeReplayNotComparedHeldAtStart and the three after it say why a
	// contract with an Edge cycle is not compared with it: held at the
	// window's start; traded outside the cycle; incomplete broker P/L; closed
	// by exercise or assignment, where the broker's realized P/L on the
	// option carries value moved to or from the stock.
	EdgeReplayNotComparedHeldAtStart  = "held_at_start"
	EdgeReplayNotComparedOutsideCycle = "traded_outside_cycle"
	EdgeReplayNotComparedBrokerPNL    = "broker_pnl_incomplete"
	EdgeReplayNotComparedExercised    = "exercised"

	// EdgeReplayResidualCashVsRealized marks a compared contract whose cash
	// flows differ from the broker's realized P/L in its own currency.
	EdgeReplayResidualCashVsRealized = "cash_differs_from_broker_realized"
)

// EdgeReplayRules lists the single rules in render order.
var EdgeReplayRules = []string{EdgeReplayRuleLossExit, EdgeReplayRuleExpiryClose, EdgeReplayRuleProfitTrail, EdgeReplayRuleRecoverCost, EdgeReplayRuleTrimToCap, EdgeReplayRuleStockTrail}

// EdgeReplaySets lists the rule sets in render order.
var EdgeReplaySets = []string{EdgeReplaySetActual, EdgeReplaySetTodays, EdgeReplaySetHarvest, EdgeReplaySetStockTrail}

// EdgeReplaySetRules returns the rules a set runs: B today's rules (loss
// line, expiry close, premium profit trail), C harvest (loss line, expiry
// close, recover cost, trim to the name cap), D stock trail (loss line,
// expiry close, stock trail). A runs no rule, and a single rule runs alone.
func EdgeReplaySetRules(id string) []string {
	switch id {
	case EdgeReplaySetActual:
		return []string{}
	case EdgeReplaySetTodays:
		return []string{EdgeReplayRuleLossExit, EdgeReplayRuleExpiryClose, EdgeReplayRuleProfitTrail}
	case EdgeReplaySetHarvest:
		return []string{EdgeReplayRuleLossExit, EdgeReplayRuleExpiryClose, EdgeReplayRuleRecoverCost, EdgeReplayRuleTrimToCap}
	case EdgeReplaySetStockTrail:
		return []string{EdgeReplayRuleLossExit, EdgeReplayRuleExpiryClose, EdgeReplayRuleStockTrail}
	}
	if slices.Contains(EdgeReplayRules, id) {
		return []string{id}
	}
	return nil
}

// EdgeReplayParams selects the replay window and the rule sets. Empty From
// and To mean every retained statement; empty Rules means every set and every
// single rule.
type EdgeReplayParams struct {
	From  string   `json:"from,omitempty"`
	To    string   `json:"to,omitempty"`
	Rules []string `json:"rules,omitempty"`
}

// NormalizeEdgeReplayParams is the input contract shared by the CLI and the
// daemon: dates are YYYY-MM-DD with From not after To, and every rule names a
// set (A-D) or a single rule. The actual set A always runs; it is the
// baseline every other run is compared with.
func NormalizeEdgeReplayParams(in EdgeReplayParams) (EdgeReplayParams, error) {
	out := EdgeReplayParams{From: strings.TrimSpace(in.From), To: strings.TrimSpace(in.To)}
	var from, to time.Time
	var err error
	if out.From != "" {
		if from, err = time.Parse(time.DateOnly, out.From); err != nil {
			return EdgeReplayParams{}, fmt.Errorf("replay from must be YYYY-MM-DD")
		}
	}
	if out.To != "" {
		if to, err = time.Parse(time.DateOnly, out.To); err != nil {
			return EdgeReplayParams{}, fmt.Errorf("replay to must be YYYY-MM-DD")
		}
	}
	if !from.IsZero() && !to.IsZero() && from.After(to) {
		return EdgeReplayParams{}, fmt.Errorf("replay from must not be after to")
	}
	seen := map[string]bool{EdgeReplaySetActual: true}
	out.Rules = []string{EdgeReplaySetActual}
	add := func(id string) {
		if !seen[id] {
			seen[id] = true
			out.Rules = append(out.Rules, id)
		}
	}
	if len(in.Rules) == 0 {
		for _, id := range EdgeReplaySets[1:] {
			add(id)
		}
		for _, id := range EdgeReplayRules {
			add(id)
		}
		return out, nil
	}
	for _, raw := range in.Rules {
		for part := range strings.SplitSeq(raw, ",") {
			id := strings.TrimSpace(part)
			if id == "" {
				continue
			}
			if upper := strings.ToUpper(id); slices.Contains(EdgeReplaySets, upper) {
				id = upper
			} else {
				id = strings.ToLower(id)
			}
			if EdgeReplaySetRules(id) == nil {
				return EdgeReplayParams{}, fmt.Errorf("replay rule %q is unknown; use A, B, C, D or %s", part, strings.Join(EdgeReplayRules, ", "))
			}
			add(id)
		}
	}
	return out, nil
}

// EdgeReplayResult is the replay's public result. It names underlyings but no
// account, order, execution or contract identity.
type EdgeReplayResult struct {
	SchemaVersion string `json:"schema_version"`
	// State is current when the replay ran; otherwise Reason says what is
	// missing and the remaining fields are empty.
	State          string    `json:"state"`
	Reason         string    `json:"reason,omitempty"`
	From           time.Time `json:"from,omitzero"`
	To             time.Time `json:"to,omitzero"`
	StartStateDate time.Time `json:"start_state_date,omitzero"`
	BaseCurrency   string    `json:"base_currency,omitempty"`
	// StatementDays is the number of days the rules were evaluated on.
	StatementDays int `json:"statement_days"`
	// Incomplete is true when an underlying lacks the daily history its days
	// need; MissingHistory names each one and every run leaves it out.
	Incomplete     bool                       `json:"incomplete"`
	MissingHistory []EdgeReplayMissingHistory `json:"missing_history"`
	Parameters     EdgeReplayParameters       `json:"parameters"`
	Scope          EdgeReplayScope            `json:"scope"`
	Runs           []EdgeReplayRun            `json:"runs"`
	Reconciliation EdgeReplayReconciliation   `json:"reconciliation"`
	Method         EdgeReplayMethod           `json:"method"`
	NotExecution   bool                       `json:"not_execution"`
}

// EdgeReplayMissingHistory is one underlying left out of every run because
// its daily history does not span the days its options were held.
type EdgeReplayMissingHistory struct {
	Underlying string `json:"underlying"`
	// Reason is one of the EdgeReplayHistory reasons; Detail says what
	// Canary holds.
	Reason string `json:"reason"`
	Detail string `json:"detail,omitempty"`
	// NeededFrom and NeededTo are the first and last statement days the
	// name's options were held in the window; HistoryFrom and HistoryTo the
	// daily history the replay had, zero when it had none.
	NeededFrom  time.Time `json:"needed_from"`
	NeededTo    time.Time `json:"needed_to"`
	HistoryFrom time.Time `json:"history_from,omitzero"`
	HistoryTo   time.Time `json:"history_to,omitzero"`
	Contracts   int       `json:"contracts"`
	// ActualPNLBase is the owner's own P/L on the name's contracts in the
	// window, which no run includes.
	ActualPNLBase float64 `json:"actual_pnl_base"`
}

// EdgeReplayParameter is one rule number with the policy field it was read
// from.
type EdgeReplayParameter struct {
	Name   string  `json:"name"`
	Value  float64 `json:"value"`
	Unit   string  `json:"unit"`
	Source string  `json:"source"`
}

// EdgeReplayParameters are the rule numbers in force and the cost
// assumptions every exit is priced with.
type EdgeReplayParameters struct {
	Rules []EdgeReplayParameter `json:"rules"`
	// HalfSpreadPct is the half-spread charged on every rule exit, in percent
	// of the mark; nil when no measured option fill exists (exits then sell
	// at the mark).
	HalfSpreadPct     *float64 `json:"half_spread_pct,omitempty"`
	HalfSpreadSamples int      `json:"half_spread_samples"`
	HalfSpreadSource  string   `json:"half_spread_source"`
	// CommissionPerContractBase is the owner's actual average commission per
	// option contract over the window's exchange executions in scope.
	CommissionPerContractBase *float64 `json:"commission_per_contract_base,omitempty"`
	CommissionContracts       float64  `json:"commission_contracts"`
	// ModelRatePct is the interest rate of the modelled Black-Scholes run.
	ModelRatePct float64 `json:"model_rate_pct"`
	PolicySource string  `json:"policy_source"`
}

// EdgeReplayScope counts what the replay covers and what it excludes.
type EdgeReplayScope struct {
	Contracts       int      `json:"contracts"`
	Underlyings     []string `json:"underlyings"`
	BuyLots         int      `json:"buy_lots"`
	PreheldLots     int      `json:"preheld_lots"`
	ContractsBought float64  `json:"contracts_bought"`
	// ExcludedHedgeContracts are long puts on a Rulebook hedge symbol.
	ExcludedHedgeContracts  int `json:"excluded_hedge_contracts"`
	ExcludedHedgeExecutions int `json:"excluded_hedge_executions"`
	ExcludedShortContracts  int `json:"excluded_short_contracts"`
	ExcludedShortExecutions int `json:"excluded_short_executions"`
	// ExcludedOther counts contracts the replay cannot follow, by reason.
	ExcludedOther map[string]int `json:"excluded_other"`
}

// EdgeReplayRun is one rule set or single rule over the window.
type EdgeReplayRun struct {
	ID     string   `json:"id"`
	Rules  []string `json:"rules"`
	Prices string   `json:"prices"`
	// PNLBase is the run's option P/L over the window: exits and end values
	// less the cost of the owner's buys and the start value of positions held
	// at the start.
	PNLBase          float64             `json:"pnl_base"`
	DiffVsActualBase float64             `json:"diff_vs_actual_base"`
	Exits            int                 `json:"exits"`
	ExitsByReason    []EdgeReplayReason  `json:"exits_by_reason"`
	VsOwner          EdgeReplayTiming    `json:"vs_owner"`
	ContractsVsOwner EdgeReplayTiming    `json:"contracts_vs_owner"`
	PeakValueBase    float64             `json:"peak_value_base"`
	PeakDate         time.Time           `json:"peak_date,omitzero"`
	PeakNLVBase      *float64            `json:"peak_nlv_base,omitempty"`
	PeakPctNLV       *float64            `json:"peak_pct_nlv,omitempty"`
	Coverage         EdgeReplayCoverage  `json:"coverage"`
	ByUnderlying     []EdgeReplayNameRow `json:"by_underlying"`
}

// EdgeReplayReason counts exit events (one contract on one day) and the
// contracts they sold.
type EdgeReplayReason struct {
	Reason string  `json:"reason"`
	Events int     `json:"events"`
	Units  float64 `json:"units"`
}

// EdgeReplayTiming compares a run's exit day with the owner's.
type EdgeReplayTiming struct {
	Earlier    int `json:"earlier"`
	Later      int `json:"later"`
	Same       int `json:"same"`
	Untestable int `json:"untestable"`
}

// EdgeReplayCoverage counts held contract-days by the price that decided
// them. Untestable days keep the owner's actual outcome for the contracts
// still held.
type EdgeReplayCoverage struct {
	StatementDays    int     `json:"statement_days"`
	TradeCloseDays   int     `json:"trade_close_days"`
	ChainMidDays     int     `json:"chain_mid_days"`
	ModelledDays     int     `json:"modelled_days"`
	UntestableEvents int     `json:"untestable_events"`
	UnitsKeptActual  float64 `json:"units_kept_actual"`
	// CloseOnlyRangeDays counts the daily bars without a high and low whose
	// true range entered the stock trail as the close-to-close move;
	// TrailUnmeasuredDays the held contract-days the trail could not measure
	// for want of 21 sessions of history. Both are zero without the trail.
	CloseOnlyRangeDays  int `json:"close_only_range_days"`
	TrailUnmeasuredDays int `json:"trail_unmeasured_days"`
	// ClosedSessionDays counts the held contract-days on which the
	// underlying's exchange held no session (a holiday, by Canary's official
	// exchange calendar): the previous close and mark carry, no rule acts.
	ClosedSessionDays int `json:"closed_session_days"`
}

// EdgeReplayNameRow is one underlying's P/L in a run.
type EdgeReplayNameRow struct {
	Underlying       string  `json:"underlying"`
	PNLBase          float64 `json:"pnl_base"`
	DiffVsActualBase float64 `json:"diff_vs_actual_base"`
}

// EdgeReplayReconciliation ties the actual baseline to the broker's cash and
// to Edge's option cycles for the same window and contracts.
type EdgeReplayReconciliation struct {
	ActualUnitsBase float64 `json:"actual_units_base"`
	ActualCashBase  float64 `json:"actual_cash_base"`
	FlowsBase       float64 `json:"flows_base"`
	StartValueBase  float64 `json:"start_value_base"`
	EndValueBase    float64 `json:"end_value_base"`
	// BrokerRealizedBase is the broker's realized P/L on the window's
	// executions in scope, converted as Edge converts it.
	BrokerRealizedBase *float64 `json:"broker_realized_base,omitempty"`
	BrokerRealizedGaps int      `json:"broker_realized_gaps"`
	EdgeCycles         int      `json:"edge_cycles"`
	EdgeCycleContracts int      `json:"edge_cycle_contracts"`
	ComparedContracts  int      `json:"compared_contracts"`
	EdgeCyclesPNLBase  float64  `json:"edge_cycles_pnl_base"`
	ActualOnCyclesBase float64  `json:"actual_on_cycles_base"`
	FXTranslationBase  float64  `json:"fx_translation_base"`
	ResidualBase       float64  `json:"residual_base"`
	CycleCashResidual  float64  `json:"cycle_cash_residual"`
	// Reconciled is true when every check is within tolerance on the
	// contracts compared; Status adds whether every contract with an Edge
	// cycle was compared (one of the EdgeReplayReconciled statuses).
	Reconciled bool   `json:"reconciled"`
	Status     string `json:"status"`
	// NotCompared names each contract with an Edge cycle the comparison
	// leaves out and why; ResidualContracts each compared contract whose
	// cash flows differ from the broker's realized P/L.
	NotCompared         []EdgeReplayReconContract `json:"not_compared"`
	ResidualContracts   []EdgeReplayReconContract `json:"residual_contracts"`
	ReconciliationNotes []string                  `json:"notes"`
}

// EdgeReplayReconContract names one contract in the reconciliation: its
// underlying, its expiry, strike and right, and the reason it is listed.
type EdgeReplayReconContract struct {
	Underlying string `json:"underlying"`
	Contract   string `json:"contract"`
	Reason     string `json:"reason"`
}

// EdgeReplayMethod discloses how the replay prices and decides.
type EdgeReplayMethod struct {
	Evaluation string `json:"evaluation"`
	History    string `json:"history"`
	Exits      string `json:"exits"`
	Untestable string `json:"untestable"`
	Modelled   string `json:"modelled"`
	HighWater  string `json:"high_water"`
	Harvest    string `json:"harvest"`
	StockTrail string `json:"stock_trail"`
	Actual     string `json:"actual"`
	Exclusions string `json:"exclusions"`
}

// ValidateEdgeReplayResult rejects a result a reader must not render: wrong
// schema, missing not-execution marker, or a non-finite amount.
func ValidateEdgeReplayResult(r EdgeReplayResult) error {
	if r.SchemaVersion != EdgeReplaySchemaVersion {
		return fmt.Errorf("edge replay schema %q is not %s", r.SchemaVersion, EdgeReplaySchemaVersion)
	}
	if !r.NotExecution {
		return fmt.Errorf("edge replay result must be marked not_execution")
	}
	finite := func(values ...float64) bool {
		for _, v := range values {
			if math.IsNaN(v) || math.IsInf(v, 0) {
				return false
			}
		}
		return true
	}
	if r.Incomplete != (len(r.MissingHistory) > 0) {
		return fmt.Errorf("edge replay incomplete flag disagrees with its missing history")
	}
	for _, gap := range r.MissingHistory {
		if strings.TrimSpace(gap.Underlying) == "" || gap.Reason == "" || !finite(gap.ActualPNLBase) {
			return fmt.Errorf("edge replay missing history row is incomplete or non-finite")
		}
	}
	rec := r.Reconciliation
	if !finite(rec.ActualUnitsBase, rec.ActualCashBase, rec.FlowsBase, rec.StartValueBase, rec.EndValueBase, rec.EdgeCyclesPNLBase, rec.ActualOnCyclesBase, rec.FXTranslationBase, rec.ResidualBase, rec.CycleCashResidual) {
		return fmt.Errorf("edge replay reconciliation carries a non-finite amount")
	}
	for _, run := range r.Runs {
		if !finite(run.PNLBase, run.DiffVsActualBase, run.PeakValueBase) {
			return fmt.Errorf("edge replay run %s carries a non-finite amount", run.ID)
		}
		for _, row := range run.ByUnderlying {
			if !finite(row.PNLBase, row.DiffVsActualBase) {
				return fmt.Errorf("edge replay run %s carries a non-finite amount", run.ID)
			}
		}
	}
	return nil
}
