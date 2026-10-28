package daemon

import (
	"strings"
	"sync"
	"time"

	"github.com/osauer/canary/v2/internal/mandate"
	"github.com/osauer/canary/v2/internal/rpc"
)

// Canary's own proposals are priced for the index fall the way a planned
// sweep is (owner, 2026-10-28 06:41 CET): the mandate's index stress on the
// kept book now and with the row filled, so a reader compares a proposal
// card with a preset card on one figure. The figures are served, never
// persisted, and read the kept evaluation only; a read never evaluates.

// mandateCurrentInput is the kept evaluation's input when it was made under
// the current policy, with the time it was made; otherwise why it cannot
// serve.
func (s *Server) mandateCurrentInput() (*mandate.Input, time.Time, string) {
	if s == nil {
		return nil, time.Time{}, "the mandate has not measured the book under the current policy yet"
	}
	_, input, at, fp := s.mandateVerdicts.current()
	if input == nil || s.riskPolicies == nil || fp != s.riskPolicies.snapshot().policy.FingerprintKey() {
		return nil, time.Time{}, "the mandate has not measured the book under the current policy yet"
	}
	return input, at, ""
}

// indexLossWithLegs measures the index stress on the kept book now and
// with the given order legs added (a leg of a held contract sums with it).
func indexLossWithLegs(in mandate.Input, add []mandate.Leg) rpc.IndexLoss {
	after := in
	after.Legs = append(append([]mandate.Leg(nil), in.Legs...), add...)
	return indexLossBetween(in, after)
}

// indexLossBetween measures the index stress on the book before and on the
// book after; unknown, with the before book's first reason, when either
// cannot be measured.
func indexLossBetween(before, after mandate.Input) rpc.IndexLoss {
	out := rpc.IndexLoss{IndexShockPct: mandate.IndexShockPct}
	b, a := mandate.Evaluate(before), mandate.Evaluate(after)
	if b.Stresses == nil || a.Stresses == nil {
		out.IndexLossUnknown = "the mandate cannot measure the book"
		if len(b.Reasons) > 0 {
			out.IndexLossUnknown = b.Reasons[0]
		}
		return out
	}
	out.IndexLossBeforeBase, out.IndexLossAfterBase = &b.Stresses.IndexLossBase, &a.Stresses.IndexLossBase
	return out
}

// proposalFillsNow reports whether a row is an order that fills when sent;
// a stop or trail fills only when the market reaches it, so its effect on
// the fall is not a figure.
func proposalFillsNow(p rpc.TradeProposal) bool {
	t := strings.ToUpper(strings.TrimSpace(p.OrderType))
	return p.Trail == nil && !strings.HasPrefix(t, "STP") && !strings.HasPrefix(t, "TRAIL")
}

// proposalCarriesEquityRisk reports whether the row's bucket moves equity
// exposure at all; cash and currency rows do not and carry no figure.
func proposalCarriesEquityRisk(p rpc.TradeProposal) bool {
	return p.Bucket != rpc.TradeProposalBucketCashSweep && p.Bucket != rpc.TradeProposalBucketCurrencyLeveling
}

// proposalIndexLossLeg prices a row as a leg of the kept book, through the
// same pricing the order gate uses (mandateOrderLeg); the string says why
// it cannot be.
func proposalIndexLossLeg(p rpc.TradeProposal, in *mandate.Input) (mandate.Leg, string) {
	if !proposalFillsNow(p) {
		return mandate.Leg{}, "a stop or trail fills only when the market reaches it; the loss is measured for orders that fill now"
	}
	draft := rpc.OrderDraft{Action: p.Action, Quantity: p.Quantity, Contract: p.Contract, OrderType: p.OrderType}
	if draft.Contract.SecType == "" {
		draft.Contract.SecType = p.SecType
	}
	if draft.Contract.Symbol == "" {
		draft.Contract.Symbol = p.Symbol
	}
	if p.LimitPrice != nil {
		draft.LimitPrice = *p.LimitPrice
	}
	leg, ok := mandateOrderLeg(draft, in)
	if !ok {
		return mandate.Leg{}, "the mandate cannot price this order without a guess"
	}
	return leg, ""
}

// proposalIndexLossCache keeps one snapshot's figures: Desk reads the list
// every few seconds and the figures change only with the evaluation or the
// rows.
type proposalIndexLossCache struct {
	mu   sync.Mutex
	key  string
	rows map[string]*rpc.IndexLoss
	all  *rpc.IndexLoss
	ccy  string
}

// decorateIndexLoss attaches the index-fall figures to each served row and
// to the snapshot: every actionable row filled together. A row with no
// figure says why; a cash or currency row carries none.
func (e *proposalEngine) decorateIndexLoss(snap *rpc.TradeProposalSnapshot) {
	if e == nil || snap == nil || e.server == nil {
		return
	}
	input, evaluatedAt, why := e.server.mandateCurrentInput()
	if input == nil {
		unknown := rpc.IndexLoss{IndexShockPct: mandate.IndexShockPct, IndexLossUnknown: why}
		snap.IndexLoss = &unknown
		for i := range snap.Proposals {
			if proposalCarriesEquityRisk(snap.Proposals[i]) {
				row := unknown
				snap.Proposals[i].IndexLoss = &row
			}
		}
		return
	}
	var keyParts []string
	for _, p := range snap.Proposals {
		keyParts = append(keyParts, p.Key+"@"+p.Revision+"#"+strings.Join(tradingBlockerCodes(p.Blockers), ","))
	}
	cacheKey := evaluatedAt.UTC().Format(time.RFC3339Nano) + "|" + snap.Revision + "|" + strings.Join(keyParts, ";")
	e.indexLoss.mu.Lock()
	defer e.indexLoss.mu.Unlock()
	if e.indexLoss.key != cacheKey {
		rows := make(map[string]*rpc.IndexLoss, len(snap.Proposals))
		var together []mandate.Leg
		for _, p := range snap.Proposals {
			if !proposalCarriesEquityRisk(p) {
				continue
			}
			leg, why := proposalIndexLossLeg(p, input)
			if why != "" {
				rows[p.Key] = &rpc.IndexLoss{IndexShockPct: mandate.IndexShockPct, IndexLossUnknown: why}
				continue
			}
			row := indexLossWithLegs(*input, []mandate.Leg{leg})
			rows[p.Key] = &row
			if len(p.Blockers) == 0 && !p.Shadow {
				together = append(together, leg)
			}
		}
		all := indexLossWithLegs(*input, together)
		e.indexLoss.key, e.indexLoss.rows, e.indexLoss.all, e.indexLoss.ccy = cacheKey, rows, &all, strings.ToUpper(input.BaseCurrency)
	}
	for i := range snap.Proposals {
		if figure, ok := e.indexLoss.rows[snap.Proposals[i].Key]; ok {
			row := *figure
			snap.Proposals[i].IndexLoss = &row
		}
	}
	all := *e.indexLoss.all
	snap.IndexLoss, snap.BaseCurrency = &all, e.indexLoss.ccy
}

func tradingBlockerCodes(blockers []rpc.TradingBlocker) []string {
	out := make([]string, 0, len(blockers))
	for _, b := range blockers {
		out = append(out, b.Code)
	}
	return out
}
