package rpc

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// Holding locks are the owner's keep list: a locked line is never selected
// by a bulk reduction, the mandate's automatic reduction or any other sweep.
// A single-line order the owner places on it still works. The owner, not the
// sign of a delta, decides what is a hedge (owner, 2026-10-22 19:50 CEST).
const (
	MethodHoldingLocksList    = "holding_locks.list"
	MethodHoldingLocksReplace = "holding_locks.replace"
	CodeHoldingLocksConflict  = "holding_locks_conflict"
	HoldingLocksLimit         = 200

	HoldingLockRoleHedge = "hedge"
	HoldingLockRoleCore  = "core"
)

// HoldingLock is one locked line, keyed by its exact contract.
type HoldingLock struct {
	ConID  int    `json:"con_id"`
	Symbol string `json:"symbol"`
	Role   string `json:"role"`
}

// HoldingLocks is the accepted keep list and an optional mutation receipt.
type HoldingLocks struct {
	Version       int           `json:"version"`
	Revision      int64         `json:"revision"`
	Locks         []HoldingLock `json:"locks"`
	AsOf          time.Time     `json:"as_of"`
	RequestID     string        `json:"request_id,omitempty"`
	SavedRevision int64         `json:"saved_revision,omitempty"`
	Replay        bool          `json:"replay,omitempty"`
}

// Locked reports whether the exact contract is on the keep list.
func (l HoldingLocks) Locked(conID int) bool {
	return conID > 0 && slices.ContainsFunc(l.Locks, func(h HoldingLock) bool { return h.ConID == conID })
}

// HoldingLocksReplaceRequest replaces the keep list under a revision fence.
type HoldingLocksReplaceRequest struct {
	Locks            []HoldingLock `json:"locks"`
	ExpectedRevision int64         `json:"expected_revision"`
	RequestID        string        `json:"request_id"`
}

// NormalizeHoldingLocks orders the list by contract and rejects duplicates,
// unknown roles and unresolved contracts.
func NormalizeHoldingLocks(in []HoldingLock) ([]HoldingLock, error) {
	if in == nil || len(in) > HoldingLocksLimit {
		return nil, fmt.Errorf("locks must be an array of at most %d lines", HoldingLocksLimit)
	}
	out := make([]HoldingLock, 0, len(in))
	seen := map[int]bool{}
	for _, l := range in {
		l.Symbol = strings.ToUpper(strings.TrimSpace(l.Symbol))
		l.Role = strings.ToLower(strings.TrimSpace(l.Role))
		if l.ConID <= 0 || l.Symbol == "" || len(l.Symbol) > 64 {
			return nil, fmt.Errorf("each lock needs a positive con_id and a symbol")
		}
		if l.Role != HoldingLockRoleHedge && l.Role != HoldingLockRoleCore {
			return nil, fmt.Errorf("lock role must be hedge or core")
		}
		if seen[l.ConID] {
			return nil, fmt.Errorf("each contract is locked at most once")
		}
		seen[l.ConID] = true
		out = append(out, l)
	}
	slices.SortFunc(out, func(a, b HoldingLock) int { return a.ConID - b.ConID })
	return out, nil
}
