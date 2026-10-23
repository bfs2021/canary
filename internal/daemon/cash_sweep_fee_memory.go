package daemon

import (
	"maps"
	"sync"

	"github.com/osauer/canary/v2/internal/rpc"
)

// cashSweepFeeMemory keeps, per currency and bill instrument, the broker fee
// per unit of face from the last accepted WhatIf of a sweep buy, so the next
// cycle's bill selection weighs the fee before any preview (owner decision
// 2026-10-23 16:30 CEST). It is the daemon's memory: a restart forgets it,
// and every preview still checks the exact fee.
type cashSweepFeeMemory struct {
	mu    sync.Mutex
	rates map[string]float64
}

func (m *cashSweepFeeMemory) snapshot() map[string]float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return maps.Clone(m.rates)
}

// noteSweepFee records a fresh invest preview's fee bound per unit of face.
func (e *proposalEngine) noteSweepFee(prop rpc.TradeProposal, preview *rpc.OrderPreviewResult) {
	s := prop.CashSweep
	if e == nil || s == nil || s.Side != rpc.CashSweepSideInvest || preview == nil || preview.Draft.Bond == nil {
		return
	}
	fee, ok := cashSweepFeeUpper(preview, s.Currency)
	face := float64(preview.Draft.Quantity) * preview.Draft.Bond.FacePerUnit
	if !ok || !positiveFinite(face) {
		return
	}
	e.sweepFees.mu.Lock()
	defer e.sweepFees.mu.Unlock()
	if e.sweepFees.rates == nil {
		e.sweepFees.rates = map[string]float64{}
	}
	e.sweepFees.rates[cashSweepBillUnitKey(s.Currency, s.Instrument)] = fee / face
}
