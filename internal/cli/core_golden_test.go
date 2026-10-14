package cli

import (
	"time"

	"github.com/osauer/canary/v2/internal/core"
	"github.com/osauer/canary/v2/internal/rpc"
)

// goldenCore is a synthetic core target: the 30 September decision with
// the trend on and the 15% floor binding, a first buy cut by the mandate's
// headroom. Every figure is synthetic.
func goldenCore() rpc.CoreTargetResult {
	conID := 9030
	scale, band, minimum, maxValue, coreVol := 100.0, 10.0, 2500.0, 40.0, 6.0
	holdable := 908
	on := core.Sizing{CoreVolPct: 6, ScalePct: 100, MaxValuePct: 40, BudgetBase: 230000, Room: 1}
	size := core.Size{SleeveBudget: 13800, Cap: 92000, Notional: 92000, TargetShares: 958}
	return rpc.CoreTargetResult{AsOf: goldenAt, Active: true, Status: rpc.CoreStatusReady, ReadOnly: true,
		Rule: core.RuleID, SpecID: core.SpecID, SpecHash: core.SpecHash, PolicyRule: core.RuleID,
		PolicyFingerprint: &rpc.Fingerprint{Version: rpc.RiskConstitutionFingerprintVersion, Key: "sha256:synthetic"},
		Policy: &rpc.CorePolicyView{Rule: core.RuleID, ISIN: core.ISIN, ConID: &conID, Exchange: core.Exchange, Currency: core.Currency,
			ScalePct: &scale, BandPct: &band, MinOrderBase: &minimum, MaxValuePct: &maxValue, CoreVolPct: &coreVol},
		Instrument: &rpc.CoreInstrument{Symbol: core.Symbol, ISIN: core.ISIN, ConID: conID, Exchange: core.Exchange, Currency: core.Currency},
		Decision: &rpc.CoreDecision{Status: core.StatusOK, Decision: "2026-08-31", WindowFrom: "2025-08-26",
			MonthEnds:      []string{"2025-11-28", "2025-12-30", "2026-01-30", "2026-02-27", "2026-03-31", "2026-04-30", "2026-05-29", "2026-06-30", "2026-07-31", "2026-08-31"},
			MonthEndCloses: []float64{86.39, 89.83, 88.91, 85.3, 86.39, 89.03, 90.02, 92.62, 90.53, 95.95},
			Close:          95.95, SMA10: 89.497, TrendOn: true, SigmaEWMA: 0.1300579, Sigma252: 0.1158550, Sigma: 0.15, Binding: core.BindingFloor, Digest: "0e98bf0c9b554798",
			FrozenAt: goldenAt.Add(-5 * 24 * time.Hour)},
		Sizing:  &rpc.CoreSizingView{Frozen: size, FrozenOn: on, Current: size, CurrentOn: on, Target: 958, TargetBase: 92063.8, Holdable: &holdable},
		Holding: &rpc.CoreHolding{Shares: 0, Price: 96.1, ValueBase: 0, PriceSource: "close"},
		Action: &core.Decision{Kind: core.ActionBuy, Quantity: 908, Why: "first buy, cut by the mandate's headroom from 958 shares", Diff: 958, BandShares: 95.8,
			OrderValue: 87258.8, HeadroomCut: true},
		Mandate:      &rpc.CoreMandateView{Status: "within", EvaluatedAt: goldenAt.Add(-time.Minute), BudgetAnnualSD: 34500, RiskAnnualSD: 3267},
		NextDecision: "2026-09-30",
		Evidence:     &rpc.CoreEvidence{SeriesKey: "core:con:9030", Closes: 320, LastClose: "2026-09-04", FetchedAt: goldenAt.Add(-9 * time.Hour), Source: "IBKR historical bars · TRADES", Calendar: "de_xetra"},
	}
}

// goldenCoreHold is a core that holds: the decision day's close has not
// arrived and a key is unwritten.
func goldenCoreHold() rpc.CoreTargetResult {
	res := goldenCore()
	res.Status = rpc.CoreStatusHold
	res.Reasons = []string{"core keys are unwritten: mandate.core.band_pct", "the decision waits for IBCF's close of 2026-08-31"}
	res.Decision, res.Sizing = nil, nil
	res.Action = &core.Decision{Kind: core.ActionHold, Why: "the core holds: buys stop, and a sale the rule wants goes to you for review"}
	res.Evidence.Stale = true
	return res
}

// goldenCoreTrendOff sells the holding: the trend is off.
func goldenCoreTrendOff() rpc.CoreTargetResult {
	res := goldenCore()
	res.Decision.TrendOn, res.Decision.Close = false, 84.1
	res.Decision.Binding, res.Decision.Sigma, res.Decision.SigmaEWMA = core.BindingEWMA, 0.231, 0.231
	zero := core.Size{SleeveBudget: 13800, Cap: 92000}
	res.Sizing = &rpc.CoreSizingView{Frozen: zero, FrozenOn: res.Sizing.FrozenOn, Current: zero, CurrentOn: res.Sizing.CurrentOn}
	res.Holding = &rpc.CoreHolding{Shares: 640, Price: 84.3, ValueBase: 53952, PriceSource: "mark"}
	res.Action = &core.Decision{Kind: core.ActionSellAll, Quantity: 640, Why: "the trend is off: sell every share; the band and the minimum do not apply", Diff: -640, OrderValue: 53952}
	return res
}
