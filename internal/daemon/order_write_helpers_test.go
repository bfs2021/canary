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
