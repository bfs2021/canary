package daemon

import (
	"strings"
	"testing"

	"github.com/osauer/canary/v2/internal/rpc"
	ibkrlib "github.com/osauer/canary/v2/pkg/ibkr"
)

// What names an order is its mode, account and client id; the socket it was
// placed through is not a gate (owner, 2026-10-20 08:00 CEST).
func TestValidateOrderViewMatchesGateNamesTheOrderByModeAccountAndClient(t *testing.T) {
	t.Parallel()
	status := rpc.TradingStatus{Mode: "live", Account: "U1234567", ClientID: 15, Endpoint: "127.0.0.1:4001"}
	view := rpc.OrderView{Mode: "live", Account: "U1234567", ClientID: 15, Endpoint: "127.0.0.1:7496", Open: true}
	if err := validateOrderViewMatchesGate(view, status); err != nil {
		t.Fatalf("order placed through TWS, daemon on the Gateway = %v, want accepted", err)
	}
	for name, differs := range map[string]func(rpc.OrderView) rpc.OrderView{
		"mode":    func(v rpc.OrderView) rpc.OrderView { v.Mode = "paper"; return v },
		"account": func(v rpc.OrderView) rpc.OrderView { v.Account = "U7654321"; return v },
		"client":  func(v rpc.OrderView) rpc.OrderView { v.ClientID = 0; return v },
	} {
		err := validateOrderViewMatchesGate(differs(view), status)
		if err == nil || !strings.Contains(err.Error(), "does not match current "+name) {
			t.Fatalf("another %s = %v, want the %s refusal", name, err, name)
		}
	}
}

// An order ID the broker reports for the daemon's client may be claimed
// though an earlier process placed it (#120), so a change proves the journal
// row and the broker's report mean the same order: the report must name the
// row's account and permanent ID, and an ID reported twice with different
// identities is never changed. A modify, which relies on the claim, also
// needs a permanent ID in the journal; a cancel refuses only a positive
// mismatch. Without a report there is nothing to compare.
func TestValidateReportedOwnOrderRefusesAnotherOrder(t *testing.T) {
	t.Parallel()
	reported := func(id int) (ibkrlib.ReportedOwnOrder, bool) {
		switch id {
		case 1002:
			return ibkrlib.ReportedOwnOrder{PermID: 790001, Account: "DU1234567"}, true
		case 1003:
			return ibkrlib.ReportedOwnOrder{PermID: 790003, Account: "DU1234567", Conflicted: true}, true
		case 1004:
			return ibkrlib.ReportedOwnOrder{PermID: 790004, Account: "DU7654321"}, true
		}
		return ibkrlib.ReportedOwnOrder{}, false
	}
	row := func(id, perm int) rpc.OrderView {
		return rpc.OrderView{ReservedOrderID: id, PermID: perm, Account: "du1234567"}
	}
	for _, tc := range []struct {
		name                         string
		view                         rpc.OrderView
		modifyRefused, cancelRefused bool
	}{
		{"same order", row(1002, 790001), false, false},
		{"another order behind the ID", row(1002, 790009), true, true},
		{"not reported", row(1001, 790009), false, false},
		{"journal without a permanent ID", row(1002, 0), true, false},
		{"reported twice with different identities", row(1003, 790003), true, true},
		{"reported for another account", row(1004, 790004), true, true},
		{"no broker order ID", row(0, 790001), false, false},
	} {
		if err := validateReportedOwnOrder(reported, tc.view, true); (err != nil) != tc.modifyRefused {
			t.Errorf("%s: modify err=%v, refused want %t", tc.name, err, tc.modifyRefused)
		}
		if err := validateReportedOwnOrder(reported, tc.view, false); (err != nil) != tc.cancelRefused {
			t.Errorf("%s: cancel err=%v, refused want %t", tc.name, err, tc.cancelRefused)
		}
	}
	if err := validateReportedOwnOrder(nil, row(1002, 790009), true); err != nil {
		t.Errorf("no connector report: err=%v, want nothing to compare", err)
	}
}
