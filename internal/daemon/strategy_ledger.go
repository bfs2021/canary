package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/osauer/canary/v2/internal/daemon/corestore"
)

// The placement ledger (mandate-risk-budget.md, "Placement", rules 1 to 4;
// refuter round 2, 2026-10-19): an entry's units of its line are carried
// forward from one verdict to the next, never recomputed from all of its
// fills, so a hand sale that took the entry's units lowers them for good
// and a later hand buy is the owner's. At each verdict the change in the
// broker's position is split into the entry's own fills since the last
// verdict and the rest, which is the owner's: a rise the owner's, a fall the
// owner's units first, then the entry's (rule 2). While the rest is not zero
// and the entry's own fills moved or one of its orders is in flight, the
// position and the journal have not met yet (an execution not journaled, a
// position not updated): the ledger is not moved and the placement is
// unknown (rule 4) until they agree, or until strategyLedgerSettle passes,
// after which the rest is the owner's as at any other time.

const (
	stateKindStrategyLedger = "strategy_placement"
	strategyLedgerDocVer    = 1
	// strategyLedgerSettle bounds how long the position and the journal may
	// disagree before the difference is booked as the owner's.
	strategyLedgerSettle = 10 * time.Minute
)

// strategyLedger is one entry's placement of one line in one broker scope.
type strategyLedger struct {
	Version  int                `json:"version"`
	Scope    string             `json:"scope"`
	Strategy string             `json:"strategy"`
	ConID    int                `json:"con_id"`
	Units    float64            `json:"units"`
	Held     float64            `json:"held"`
	Applied  map[string]float64 `json:"applied,omitempty"`
	At       time.Time          `json:"at"`
	// PendingSince is when the position and the journal last began to
	// disagree; zero while they agree.
	PendingSince time.Time `json:"pending_since,omitzero"`
}

type strategyLedgerStore struct {
	mu        sync.Mutex
	docs      map[string]*strategyLedger
	revisions map[string]int64
	loaded    map[string]bool
}

func strategyLedgerScope(key string) string {
	sum := sha256.Sum256([]byte(key))
	return "strategy/placement/" + hex.EncodeToString(sum[:])
}

func strategyLedgerKey(scope brokerStateScope, id string, conID int) string {
	return fmt.Sprintf("%s|%s|%s|%d", scope.Account, scope.Mode, id, conID)
}

// strategyLedgerLoad is the kept ledger of a key, nil while none is kept.
// Callers hold st.mu.
func (s *Server) strategyLedgerLoad(ctx context.Context, key string) *strategyLedger {
	st := &s.strategyLedgers
	if st.docs == nil {
		st.docs, st.revisions, st.loaded = map[string]*strategyLedger{}, map[string]int64{}, map[string]bool{}
	}
	if !st.loaded[key] {
		st.loaded[key] = true
		if s.coreStore != nil {
			if doc, ok, err := s.coreStore.GetStateDocument(ctx, strategyLedgerScope(key), stateKindStrategyLedger); err == nil && ok {
				var saved strategyLedger
				if json.Unmarshal(doc.JSON, &saved) == nil && saved.Version == strategyLedgerDocVer && saved.Scope == key {
					st.docs[key], st.revisions[key] = &saved, doc.Revision
				}
			}
		}
	}
	if l := st.docs[key]; l != nil {
		out := *l
		return &out
	}
	return nil
}

// strategyLedgerSave keeps a ledger; a failed write keeps it in memory and
// says so. Callers hold st.mu.
func (s *Server) strategyLedgerSave(ctx context.Context, l *strategyLedger) {
	st := &s.strategyLedgers
	saved := *l
	st.docs[l.Scope], st.loaded[l.Scope] = &saved, true
	if s.coreStore == nil {
		return
	}
	raw, err := json.Marshal(saved)
	if err != nil {
		return
	}
	doc, err := s.coreStore.CompareAndSwapStateDocument(ctx, corestore.StateDocumentCAS{ScopeKey: strategyLedgerScope(l.Scope), Kind: stateKindStrategyLedger, ExpectedRevision: st.revisions[l.Scope], JSON: raw})
	if err != nil {
		var conflict *corestore.RevisionConflictError
		if errors.As(err, &conflict) && conflict.Exists {
			// Another writer kept it first: re-read it next time.
			st.revisions[l.Scope] = conflict.Actual
			delete(st.docs, l.Scope)
			st.loaded[l.Scope] = false
		}
		s.warnf("strategy %s placement of %d: kept in memory, the database save failed: %v", l.Strategy, l.ConID, err)
		return
	}
	st.revisions[l.Scope] = doc.Revision
}

// strategyLedgerAdvance moves an entry's placement of a line to the broker's
// position now: its units, and why the placement is unknown when it is.
func (s *Server) strategyLedgerAdvance(ctx context.Context, id string, conID int, held float64, own strategyFills, inFlight bool, now time.Time) (units float64, unknown string) {
	key := strategyLedgerKey(s.currentBrokerStateScope(), id, conID)
	st := &s.strategyLedgers
	st.mu.Lock()
	defer st.mu.Unlock()
	l := s.strategyLedgerLoad(ctx, key)
	if l == nil {
		// The first verdict that sees the line: its own fills so far, never
		// more than the broker holds (strategyLineUnits).
		units, _ = strategyLineUnits(held, own)
		s.strategyLedgerSave(ctx, &strategyLedger{Version: strategyLedgerDocVer, Scope: key, Strategy: id, ConID: conID, Units: units, Held: held, Applied: own.ByRef, At: now})
		return units, ""
	}
	moved := 0.0
	for ref, shares := range own.ByRef {
		moved += shares - l.Applied[ref]
	}
	rest := held - l.Held - moved
	if math.Abs(rest) > 1e-9 && (math.Abs(moved) > 1e-9 || inFlight) {
		if l.PendingSince.IsZero() {
			l.PendingSince = now
			s.strategyLedgerSave(ctx, l)
		}
		if now.Sub(l.PendingSince) < strategyLedgerSettle {
			estimate := math.Min(math.Max(0, l.Units+moved), math.Max(0, held))
			return estimate, fmt.Sprintf("the broker's position of the line changed by %g while its own journaled fills moved %g", held-l.Held, moved)
		}
	}
	owner := l.Held - l.Units
	units = l.Units + moved
	if rest < 0 {
		owner += rest
		if owner < 0 {
			units, owner = units+owner, 0
		}
	} else {
		owner += rest
	}
	units = math.Min(math.Max(0, units), math.Max(0, held))
	s.strategyLedgerSave(ctx, &strategyLedger{Version: strategyLedgerDocVer, Scope: key, Strategy: id, ConID: conID, Units: units, Held: held, Applied: own.ByRef, At: now})
	return units, ""
}
