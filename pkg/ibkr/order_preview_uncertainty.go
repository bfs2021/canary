package ibkr

import (
	"context"
	"fmt"
	"maps"
	"strings"
)

type previewStatusDisposition uint8

const (
	previewStatusOrdinary previewStatusDisposition = iota
	previewStatusIgnore
	previewStatusUncertain
)

func (c *Connection) setOrderStatusObserver(observer func([]string, uint64, previewStatusDisposition)) {
	c.openOrderObserverMu.Lock()
	c.orderStatusObserver = observer
	c.openOrderObserverMu.Unlock()
}

func (c *Connection) dispatchOrderStatusObserver(fields []string, epoch uint64, disposition previewStatusDisposition) {
	c.openOrderObserverMu.RLock()
	observer := c.orderStatusObserver
	c.openOrderObserverMu.RUnlock()
	if observer != nil {
		observer(fields, epoch, disposition)
	}
}

// A status callback has no WhatIf discriminator. For a preview reusing a
// working order ID, retain uncertainty rather than guessing which it describes.
// All maps and generations below are protected by reqIDMu and scoped to epoch.
func (c *Connection) beginModifyPreview(id int, epoch uint64) error {
	c.reqIDMu.Lock()
	defer c.reqIDMu.Unlock()
	if c.brokerSessionEpoch.Load() != epoch {
		return fmt.Errorf("%w: broker session changed before modify preview", ErrBrokerIDNamespaceConflict)
	}
	if c.modifyPreviewIDs[id] || c.modifyStatusUncertain[id] != 0 {
		return fmt.Errorf("%w: order preview is active or its status is uncertain; refresh open orders and preview again", ErrBrokerIDNamespaceConflict)
	}
	if c.modifyPreviewIDs == nil {
		c.modifyPreviewIDs = make(map[int]bool)
	}
	c.modifyPreviewIDs[id] = true
	c.modifyPreviewGeneration++
	return nil
}

// Client zero has no inherited-order adoption. Recovery may use the exact
// identity supplied for an ID this Connector actually placed, without creating
// an own-order report or acquiring authority over a hand order.
func (c *Connection) rememberLocalPreviewIdentity(id int, epoch uint64, change *OrderChangeIdentity, locallyPlaced bool) {
	if !locallyPlaced || change == nil || change.PermID <= 0 || strings.TrimSpace(change.Account) == "" || strings.TrimSpace(change.OrderRef) == "" {
		return
	}
	c.reqIDMu.Lock()
	defer c.reqIDMu.Unlock()
	if c.brokerSessionEpoch.Load() != epoch || c.config == nil || c.config.ClientID != 0 {
		return
	}
	if c.modifyLocalIdentity == nil {
		c.modifyLocalIdentity = make(map[int]OrderChangeIdentity)
	}
	if _, exists := c.modifyLocalIdentity[id]; !exists {
		c.modifyLocalIdentity[id] = *change
	}
}

func clientZeroSnapshotNames(id int, want OrderChangeIdentity, orders []OrderLifecycleEvent) bool {
	seen := false
	for _, ev := range orders {
		if ev.OrderID != id || !ev.ClientIDPresent || ev.ClientID != 0 || ev.WhatIf {
			continue
		}
		if ev.PermID != want.PermID || !strings.EqualFold(strings.TrimSpace(ev.Account), strings.TrimSpace(want.Account)) || strings.TrimSpace(ev.OrderRef) != strings.TrimSpace(want.OrderRef) || (ev.Status != "Submitted" && ev.Status != "PreSubmitted") {
			return false
		}
		seen = true
	}
	return seen
}

func (c *Connection) finishModifyPreview(id int, epoch uint64) bool {
	c.reqIDMu.Lock()
	defer c.reqIDMu.Unlock()
	if c.brokerSessionEpoch.Load() != epoch {
		return false
	}
	if c.modifyPreviewIDs[id] {
		delete(c.modifyPreviewIDs, id)
		c.modifyPreviewGeneration++
	}
	return c.modifyStatusUncertain[id] != 0
}

// Snapshot both markers under one lock interval. Completion clears the
// generic marker before the modify marker, so it cannot look like a new-order
// preview halfway through classification. No path holds whatIfOrdersMu while
// acquiring reqIDMu.
func (c *Connection) orderPreviewDisposition(id int, epoch uint64) previewStatusDisposition {
	c.reqIDMu.Lock()
	defer c.reqIDMu.Unlock()
	if c.brokerSessionEpoch.Load() != epoch {
		return previewStatusOrdinary
	}
	c.whatIfOrdersMu.Lock()
	defer c.whatIfOrdersMu.Unlock()
	if c.modifyPreviewIDs[id] {
		return previewStatusUncertain
	}
	if _, ok := c.whatIfOrderIDs[id]; ok {
		return previewStatusIgnore
	}
	return previewStatusOrdinary
}

func (c *Connection) modifyPreviewActive(id int, epoch uint64) bool {
	c.reqIDMu.Lock()
	defer c.reqIDMu.Unlock()
	return c.brokerSessionEpoch.Load() == epoch && c.modifyPreviewIDs[id]
}

func (c *Connection) modifyStatusIsUncertain(id int, epoch uint64) bool {
	c.reqIDMu.Lock()
	defer c.reqIDMu.Unlock()
	return c.brokerSessionEpoch.Load() == epoch && c.modifyStatusUncertain[id] != 0
}

// Recovery belongs to a request begun after the preview ended, never an older
// shared flight that may have filtered the working order as a preview echo.
func (c *Connection) modifyStatusRecoveryTokens(epoch uint64) (map[int]uint64, uint64, bool) {
	c.reqIDMu.Lock()
	defer c.reqIDMu.Unlock()
	tokens := make(map[int]uint64)
	valid := c.brokerSessionEpoch.Load() == epoch && len(c.modifyPreviewIDs) == 0
	if valid {
		maps.Copy(tokens, c.modifyStatusUncertain)
	}
	return tokens, c.modifyPreviewGeneration, valid
}

func (c *Connection) recoverModifyStatusFromSnapshot(epoch uint64, tokens map[int]uint64, generation uint64, orders []OrderLifecycleEvent) bool {
	unlock := c.lockEvidenceChange()
	defer unlock()
	c.reqIDMu.Lock()
	defer c.reqIDMu.Unlock()
	if c.brokerSessionEpoch.Load() != epoch || len(c.modifyPreviewIDs) != 0 || c.modifyPreviewGeneration != generation {
		return false
	}
	for _, ev := range orders {
		serial := tokens[ev.OrderID]
		if serial == 0 || c.modifyStatusUncertain[ev.OrderID] != serial || ev.WhatIf ||
			!ev.ClientIDPresent || c.config == nil || ev.ClientID != c.config.ClientID ||
			(ev.Status != "Submitted" && ev.Status != "PreSubmitted") {
			continue
		}
		if c.config.ClientID == 0 {
			want, known := c.modifyLocalIdentity[ev.OrderID]
			if known && clientZeroSnapshotNames(ev.OrderID, want, orders) {
				delete(c.modifyStatusUncertain, ev.OrderID)
			}
			continue
		}
		report, ok := c.reportedOwnOrderIDs[ev.OrderID]
		if !ok || !report.matches(&OrderChangeIdentity{PermID: ev.PermID, Account: strings.TrimSpace(ev.Account), OrderRef: strings.TrimSpace(ev.OrderRef)}) {
			continue
		}
		delete(c.modifyStatusUncertain, ev.OrderID)
	}
	return true
}

func (c *Connector) refreshAfterAmbiguousPreview(ctx context.Context, binding ConnectorSessionBinding) {
	// Let a pre-window flight reach its own terminator; it cannot recover this
	// order. Caller cancellation leaves the fence intact and does not poison a
	// request merely because this waiter stopped waiting.
	c.openOrderSnapshotMu.Lock()
	old := c.openOrderSnapshot
	c.openOrderSnapshotMu.Unlock()
	if old != nil {
		select {
		case <-old.done:
		case <-ctx.Done():
			return
		}
	}
	if !c.SessionCurrent(binding) || ctx.Err() != nil {
		return
	}
	_, _ = c.SnapshotOpenOrders(ctx)
}
