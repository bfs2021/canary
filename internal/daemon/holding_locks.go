package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/osauer/canary/v2/internal/daemon/corestore"
	"github.com/osauer/canary/v2/internal/rpc"
)

// The keep list follows the watchlist's durability: one state document under
// a revision fence, and a receipt event per request ID so a retry replays.
const stateKindHoldingLocks = "holding_locks"
const holdingLocksReceiptType = "holding_locks_saved"

type holdingLocksDocument struct {
	Version int               `json:"version"`
	Locks   []rpc.HoldingLock `json:"locks"`
}

type holdingLocksReceipt struct {
	Version       int                            `json:"version"`
	Request       rpc.HoldingLocksReplaceRequest `json:"request"`
	RequestDigest string                         `json:"request_digest"`
	SavedRevision int64                          `json:"saved_revision"`
}

func holdingLocksConflict() error {
	return &rpc.Error{Code: rpc.CodeHoldingLocksConflict, Message: "Locked holdings changed; refresh before saving"}
}

func decodeHoldingLocksReplace(req *rpc.Request) (rpc.HoldingLocksReplaceRequest, error) {
	var in rpc.HoldingLocksReplaceRequest
	if len(req.Params) > 32768 {
		return in, errBadRequest("holding locks request exceeds 32 KiB")
	}
	var wire struct {
		Locks            []rpc.HoldingLock `json:"locks"`
		ExpectedRevision *int64            `json:"expected_revision"`
		RequestID        string            `json:"request_id"`
	}
	err := decodeStrictPlatformSettingsJSON(req.Params, &wire)
	if err == nil {
		in.Locks, err = rpc.NormalizeHoldingLocks(wire.Locks)
	}
	if err != nil || wire.ExpectedRevision == nil {
		return in, errBadRequest("invalid holding locks request: locks, expected_revision and request_id required")
	}
	in.ExpectedRevision, in.RequestID = *wire.ExpectedRevision, wire.RequestID
	if err = rpc.ValidateWatchlistMutation(in.ExpectedRevision, in.RequestID); err != nil {
		return in, errBadRequest(err.Error())
	}
	return in, nil
}

// handleHoldingLocksList serves the keep list with each lock resolved
// against the current book: the contracts it covers now, and whether there
// is any. The book is read on a best-effort basis; without it the locks are
// served with Held unknown.
func (s *Server) handleHoldingLocksList(ctx context.Context) (*rpc.HoldingLocks, error) {
	out, err := s.acceptHoldingLocks(ctx, "", 0, false)
	if err != nil {
		return nil, err
	}
	s.decorateHoldingLocks(ctx, out)
	return out, nil
}

// decorateHoldingLocks resolves the served list against the book when one
// can be read. A sweep never reads the list this way: it resolves against
// the very book it selects from (rpc.HoldingLocks.Resolve).
func (s *Server) decorateHoldingLocks(ctx context.Context, out *rpc.HoldingLocks) {
	if out == nil || len(out.Locks) == 0 {
		return
	}
	var pos *rpc.PositionsResult
	var err error
	switch {
	case s.reducePositionsForTest != nil:
		pos, err = s.reducePositionsForTest(ctx)
	case s.gatewayConnector() != nil:
		readCtx, cancel := requestCtx(ctx, rpc.MethodPositionsList)
		defer cancel()
		pos, err = s.handlePositionsList(readCtx, &rpc.Request{})
	}
	if err != nil {
		return
	}
	out.Decorate(pos)
}

// holdingLocks is the keep list every sweep reads. An unreadable list fails
// the caller: a sweep never runs as if nothing were locked.
func (s *Server) holdingLocks(ctx context.Context) (rpc.HoldingLocks, error) {
	out, err := s.acceptHoldingLocks(ctx, "", 0, false)
	if err != nil {
		return rpc.HoldingLocks{}, err
	}
	return *out, nil
}

// sameHoldingLocks compares two normalized lists on their stored fields.
func sameHoldingLocks(a, b []rpc.HoldingLock) bool {
	return slices.EqualFunc(a, b, func(x, y rpc.HoldingLock) bool {
		return x.Symbol == y.Symbol && x.Role == y.Role && x.ConID == y.ConID
	})
}

func (s *Server) acceptHoldingLocks(ctx context.Context, requestID string, savedRevision int64, replay bool) (*rpc.HoldingLocks, error) {
	if s.coreStore == nil {
		return nil, fmt.Errorf("holding locks durability unavailable")
	}
	out := rpc.HoldingLocks{Version: 1, Locks: []rpc.HoldingLock{}, RequestID: requestID, SavedRevision: savedRevision, Replay: replay}
	err := s.coreStore.WithAcceptedStateDocument(ctx, daemonStateScope, stateKindHoldingLocks, func(doc corestore.StateDocument, found bool) error {
		if found {
			var data holdingLocksDocument
			if err := decodeStrictPlatformSettingsJSON(doc.JSON, &data); err != nil || data.Version != 1 || doc.Revision <= 0 {
				return fmt.Errorf("invalid holding locks document")
			}
			// A list saved before 2026-10-28 named one contract per lock and
			// was ordered by contract; normalizing it folds the entries of
			// one name into one lock on the underlying. The migration is at
			// read, every read; the next save that changes the list writes
			// it folded.
			normalized, err := rpc.NormalizeHoldingLocks(data.Locks)
			if err != nil {
				return fmt.Errorf("invalid holding locks: %w", err)
			}
			out.Locks, out.Revision = normalized, doc.Revision
		}
		if savedRevision > out.Revision {
			return fmt.Errorf("holding locks receipt exceeds accepted state")
		}
		out.AsOf = s.orderNow().UTC()
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("holding locks durability unconfirmed: %w", err)
	}
	return &out, nil
}

func (s *Server) handleHoldingLocksReplace(ctx context.Context, req *rpc.Request) (*rpc.HoldingLocks, error) {
	in, err := decodeHoldingLocksReplace(req)
	if err != nil {
		return nil, err
	}
	// A successful lock save orders after any in-flight broker transaction.
	// The next bulk or automatic reduction must therefore read the new list.
	s.brokerWriteMu.Lock()
	defer s.brokerWriteMu.Unlock()
	s.holdingLocksMu.Lock()
	defer s.holdingLocksMu.Unlock()
	if s.coreStore == nil || !s.coreStore.Health().Ready {
		return nil, fmt.Errorf("holding locks durability unavailable")
	}
	rawRequest, _ := json.Marshal(in)
	digest := sha256.Sum256(rawRequest)
	requestDigest := hex.EncodeToString(digest[:])
	idHash := sha256.Sum256([]byte(in.RequestID))
	eventKey := "holding_locks:" + hex.EncodeToString(idHash[:])
	event, found, err := s.coreStore.GetEvent(ctx, daemonStateScope, eventKey)
	if err != nil {
		return nil, fmt.Errorf("holding locks receipt unavailable")
	}
	if found {
		var receipt holdingLocksReceipt
		if event.Type != holdingLocksReceiptType || event.Action != coreEventActionUpdate || decodeStrictPlatformSettingsJSON(event.PayloadJSON, &receipt) != nil || receipt.Version != 1 || receipt.SavedRevision < 1 {
			return nil, fmt.Errorf("invalid holding locks receipt")
		}
		canonical, _ := json.Marshal(receipt.Request)
		verified := sha256.Sum256(canonical)
		if hex.EncodeToString(verified[:]) != receipt.RequestDigest {
			return nil, fmt.Errorf("holding locks receipt digest mismatch")
		}
		if receipt.RequestDigest != requestDigest {
			return nil, holdingLocksConflict()
		}
		out, err := s.acceptHoldingLocks(ctx, in.RequestID, receipt.SavedRevision, true)
		if err != nil {
			return nil, err
		}
		s.decorateHoldingLocks(ctx, out)
		return out, nil
	}
	current, err := s.acceptHoldingLocks(ctx, "", 0, false)
	if err != nil {
		return nil, err
	}
	if in.ExpectedRevision != current.Revision {
		return nil, holdingLocksConflict()
	}
	changed := current.Revision == 0 || !sameHoldingLocks(current.Locks, in.Locks)
	revision := current.Revision
	if changed {
		revision++
	}
	rawReceipt, _ := json.Marshal(holdingLocksReceipt{Version: 1, Request: in, RequestDigest: requestDigest, SavedRevision: revision})
	input := corestore.EventInput{ScopeKey: daemonStateScope, EventKey: eventKey, Type: holdingLocksReceiptType, Action: coreEventActionUpdate, Origin: rpc.OrderOriginAgent, OccurredAt: s.orderNow().UTC(), PayloadJSON: rawReceipt}
	cas := corestore.StateDocumentCAS{ScopeKey: daemonStateScope, Kind: stateKindHoldingLocks, ExpectedRevision: current.Revision}
	if changed {
		cas.JSON, _ = json.Marshal(holdingLocksDocument{Version: 1, Locks: in.Locks})
		_, _, err = s.coreStore.CompareAndSwapStateDocumentWithEvents(ctx, cas, []corestore.EventInput{input})
	} else {
		_, err = s.coreStore.AppendEventsAtStateRevision(ctx, cas, []corestore.EventInput{input})
	}
	if errors.Is(err, corestore.ErrRevisionConflict) {
		return nil, holdingLocksConflict()
	}
	if err != nil {
		return nil, fmt.Errorf("holding locks save unconfirmed")
	}
	out, err := s.acceptHoldingLocks(ctx, in.RequestID, revision, false)
	if err != nil {
		return nil, err
	}
	s.decorateHoldingLocks(ctx, out)
	return out, nil
}
