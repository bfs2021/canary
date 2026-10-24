package rpc

import (
	"slices"
	"testing"
)

func TestNormalizeEdgeReplayParams(t *testing.T) {
	all, err := NormalizeEdgeReplayParams(EdgeReplayParams{})
	if err != nil {
		t.Fatal(err)
	}
	want := append(append([]string{}, EdgeReplaySets...), EdgeReplayRules...)
	if !slices.Equal(all.Rules, want) {
		t.Fatalf("default rules = %v, want %v", all.Rules, want)
	}
	got, err := NormalizeEdgeReplayParams(EdgeReplayParams{From: "2026-08-24", To: "2026-10-23", Rules: []string{"b, LOSS_EXIT", "c", "b"}})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.Rules, []string{"A", "B", "loss_exit", "C"}) {
		t.Fatalf("rules = %v; A always runs, sets upper-case, rules lower-case, duplicates once", got.Rules)
	}
	for _, bad := range []EdgeReplayParams{{From: "24.08.2026"}, {To: "2026-13-01"}, {From: "2026-10-24", To: "2026-10-23"}, {Rules: []string{"E"}}, {Rules: []string{"trail"}}} {
		if _, err := NormalizeEdgeReplayParams(bad); err == nil {
			t.Fatalf("%+v accepted", bad)
		}
	}
}

func TestValidateEdgeReplayResult(t *testing.T) {
	ok := EdgeReplayResult{SchemaVersion: EdgeReplaySchemaVersion, NotExecution: true}
	if err := ValidateEdgeReplayResult(ok); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []EdgeReplayResult{{SchemaVersion: "edge-replay-v0", NotExecution: true}, {SchemaVersion: EdgeReplaySchemaVersion}} {
		if ValidateEdgeReplayResult(bad) == nil {
			t.Fatalf("%+v accepted", bad)
		}
	}
}
