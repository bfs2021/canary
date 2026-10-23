package ibkr

import "testing"

// An OrderState double IBKR did not send reads unknown, never 0: a bill
// WhatIf carried a commission and no min/max envelope (2026-10-23), which
// read as an explicit zero maximum below the commission.
func TestOrderStateAbsentDoubleIsUnknown(t *testing.T) {
	body := protoAppendDouble(nil, 2, 1000)
	body = protoAppendDouble(body, 8, 1250)
	body = protoAppendDouble(body, 11, 18.875)
	body = protoAppendDouble(body, 13, 0)
	body = protoAppendString(body, 14, "EUR")
	var summary inboundOrderProtoSummary
	if err := parseInboundOrderStateProto(body, &summary); err != nil {
		t.Fatal(err)
	}
	if got := summary.stateDouble(11, summary.commission); got != "18.875" {
		t.Fatalf("commission = %q", got)
	}
	if got := summary.stateDouble(12, summary.minCommission); got != "" || parseIBKRFloatPtr(got) != nil {
		t.Fatalf("absent min commission = %q, want unknown", got)
	}
	if got := summary.stateDouble(13, summary.maxCommission); got != "0" {
		t.Fatalf("an explicit zero maximum = %q, want 0", got)
	}
}
