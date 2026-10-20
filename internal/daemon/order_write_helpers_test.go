package daemon

import (
	"strings"
	"testing"

	"github.com/osauer/canary/v2/internal/rpc"
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
// though an earlier process placed it, so a modify proves the journal row
// and the broker mean the same order by the permanent ID: a mismatch is
// refused, and without a report or a permanent ID there is nothing to
// compare.
func TestValidateReportedPermIDRefusesAnotherOrder(t *testing.T) {
	reported := func(id int) (int, bool) {
		switch id {
		case 77838:
			return 409649694, true
		case 77839:
			return 0, true
		}
		return 0, false
	}
	for _, tc := range []struct {
		name    string
		view    rpc.OrderView
		refused bool
	}{
		{"same order", rpc.OrderView{ReservedOrderID: 77838, PermID: 409649694}, false},
		{"another order behind the ID", rpc.OrderView{ReservedOrderID: 77838, PermID: 409649691}, true},
		{"not reported", rpc.OrderView{ReservedOrderID: 70000, PermID: 409649691}, false},
		{"reported without a permanent ID", rpc.OrderView{ReservedOrderID: 77839, PermID: 409649691}, false},
		{"journal without a permanent ID", rpc.OrderView{ReservedOrderID: 77838}, false},
	} {
		err := validateReportedPermID(reported, tc.view)
		if (err != nil) != tc.refused {
			t.Errorf("%s: err=%v, refused want %t", tc.name, err, tc.refused)
		}
	}
}
