package corestore

import (
	"errors"
	"testing"
	"time"
)

func TestStateEvidenceHeadRejectsDriftWithoutConsumingEvidence(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := t.Context()
	observed, err := s.AuthorityHead(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// A corrected evidence projection advances authority after it was read.
	if _, err := s.CompareAndSwapStateDocument(ctx, StateDocumentCAS{ScopeKey: "synthetic", Kind: "evidence", JSON: []byte(`{"revision":2}`)}); err != nil {
		t.Fatal(err)
	}
	current, err := s.AuthorityHead(ctx)
	if err != nil {
		t.Fatal(err)
	}
	update := StateDocumentCAS{ScopeKey: "synthetic", Kind: "resolution", ExpectedHead: &observed, JSON: []byte(`{"resolved":true}`)}
	events := []EventInput{{ScopeKey: "synthetic", EventKey: "resolution-one", Type: "exercise_resolved", Action: "update", Origin: "human_tty", OccurredAt: time.Now().UTC(), PayloadJSON: []byte(`{"evidence":"synthetic-one"}`)}}
	if _, _, err := s.CompareAndSwapStateDocumentWithEvents(ctx, update, events); !errors.Is(err, ErrAuthorityMismatch) {
		t.Fatalf("changed evidence accepted: %v", err)
	}
	if _, found, err := s.GetStateDocument(ctx, "synthetic", "resolution"); err != nil || found {
		t.Fatalf("failed resolution changed document: found=%v err=%v", found, err)
	}
	if rows, err := s.LoadEvents(ctx, EventQuery{ScopeKey: "synthetic", Type: "exercise_resolved"}); err != nil || len(rows) != 0 {
		t.Fatalf("failed resolution consumed evidence: events=%d err=%v", len(rows), err)
	}
	if head, err := s.AuthorityHead(ctx); err != nil || head != current {
		t.Fatalf("failed resolution advanced authority: %v", err)
	}
	// A fresh observation permits document and evidence receipt together.
	update.ExpectedHead = &current
	doc, receipts, err := s.CompareAndSwapStateDocumentWithEvents(ctx, update, events)
	if err != nil || doc.Revision != 1 || len(receipts) != 1 {
		t.Fatalf("current evidence rejected: revision=%d receipts=%d err=%v", doc.Revision, len(receipts), err)
	}
	rows, err := s.LoadEvents(ctx, EventQuery{ScopeKey: "synthetic", Type: "exercise_resolved"})
	if err != nil || len(rows) != 1 {
		t.Fatalf("successful resolution lacks evidence receipt: events=%d err=%v", len(rows), err)
	}
}
