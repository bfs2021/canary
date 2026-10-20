package ibkr

import (
	"errors"
	"net"
	"testing"
	"time"
)

// An open order the broker reports for this client ID on the current socket
// is this client's to change, though a previous process placed it below the
// session's nextValidId (#120). On 2026-10-20 the daemon, moved from TWS to
// IB Gateway, could not resize its own trailing stops: the claim refused
// their IDs as "behind the consumed request/order frontier" because only
// orders this process placed counted as owned. The broker's permanent ID and
// account come with the report and ownership holds for that socket
// generation only. Another client's order (here a manual, client 0 one), a
// what-if, an order without a permanent ID or account, and an ID reported
// twice with different permanent IDs never count.
func TestInheritedOpenOrderIDIsOwnedForItsSession(t *testing.T) {
	manual := startupBurstOrder(8)
	manual.OrderID, manual.PermID, manual.ClientID = 7, 700099, 0
	whatIf := startupBurstOrder(9)
	whatIf.OrderID, whatIf.PermID, whatIf.WhatIf = 42, 0, true
	unsent := startupBurstOrder(10)
	unsent.OrderID, unsent.PermID = 43, 0
	noAccount := startupBurstOrder(11)
	noAccount.OrderID, noAccount.Account = 44, ""
	twice, again := startupBurstOrder(12), startupBurstOrder(13)
	twice.OrderID, again.OrderID = 45, 45
	conn := startupTestConnection(serveStartup(t, func(s *twsGatewaySession) {
		for _, o := range []testOpenOrderProtoCallback{startupBurstOrder(0), startupBurstOrder(1), manual, whatIf, unsent, noAccount, twice, again} {
			s.write(encodeOpenOrderProtoCallbackForTest(o))
		}
		s.write(twsGatewayFrame(msgOpenOrderEnd, "1"))
		s.write(twsGatewayFrame(msgManagedAccts, "1", "DU1234567"))
		s.write(twsGatewayFrame(msgNextValidID, "1", "100"))
	}), 1)
	if err := conn.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Disconnect() })

	if got, ok := conn.ReportedOwnOrder(40); !ok || got != (ReportedOwnOrder{PermID: 700000, Account: "DU1234567"}) {
		t.Fatalf("reported order 40: %+v ok=%t, want permanent ID 700000 for DU1234567", got, ok)
	}
	epoch := conn.BrokerSessionEpoch()
	if _, err := conn.claimOrderIDForForwardingAtEpoch(40, conn.orderIDOwned(40), &epoch); err != nil {
		t.Fatalf("claim of the reported own order 40: %v", err)
	}
	conn.discardOrderIDReservation(40)
	if _, err := conn.claimOrderIDCurrentEpoch(41, conn.orderIDOwned(41)); err != nil {
		t.Fatalf("claim of the reported own order 41: %v", err)
	}
	// never reported; a manual order; a what-if; unsent; no account
	for _, id := range []int{39, 7, 42, 43, 44} {
		if _, ok := conn.ReportedOwnOrder(id); ok {
			t.Fatalf("order %d recorded as this client's", id)
		}
		if _, err := conn.claimOrderIDCurrentEpoch(id, conn.orderIDOwned(id)); !errors.Is(err, ErrBrokerIDNamespaceConflict) {
			t.Fatalf("claim of order %d err=%v, want the frontier refusal", id, err)
		}
	}
	if got, ok := conn.ReportedOwnOrder(45); !ok || !got.Conflicted {
		t.Fatalf("order 45 reported twice with different permanent IDs: %+v ok=%t, want conflicted", got, ok)
	}
	if _, err := conn.claimOrderIDCurrentEpoch(45, conn.orderIDOwned(45)); !errors.Is(err, ErrBrokerIDNamespaceConflict) {
		t.Fatalf("claim of the conflicted order 45 err=%v, want the frontier refusal", err)
	}
	conn.resetOrderIDReadiness()
	conn.observeNextValidOrderID(100)
	if _, err := conn.claimOrderIDCurrentEpoch(40, conn.orderIDOwned(40)); !errors.Is(err, ErrBrokerIDNamespaceConflict) {
		t.Fatalf("claim of order 40 on a new socket generation err=%v, want refused until reported again", err)
	}
}

// A report for another account than the configured one never counts, and on
// a client-0 connection nothing counts: TWS reports hand orders as client 0,
// so there a report cannot tell this client's order from one entered by hand.
func TestInheritedOpenOrderIDNeedsTheAccountAndANonZeroClient(t *testing.T) {
	for _, tc := range []struct {
		name     string
		clientID int
		account  string
	}{
		{"another account", 15, "DU7654321"},
		{"client 0", 0, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			order := startupBurstOrder(0)
			order.ClientID = tc.clientID
			ln := serveStartup(t, func(s *twsGatewaySession) {
				s.write(encodeOpenOrderProtoCallbackForTest(order))
				s.write(twsGatewayFrame(msgOpenOrderEnd, "1"))
				s.write(twsGatewayFrame(msgManagedAccts, "1", "DU1234567"))
				s.write(twsGatewayFrame(msgNextValidID, "1", "100"))
			})
			conn := NewConnection(&ConnectionConfig{Host: "127.0.0.1", Port: ln.Addr().(*net.TCPAddr).Port, ClientID: tc.clientID, Account: tc.account,
				ConnectTimeout: 2 * time.Second, MaxClientIDRetries: 1, ManagedConnectionLogging: true})
			if err := conn.Connect(t.Context()); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = conn.Disconnect() })
			if got, ok := conn.ReportedOwnOrder(40); ok {
				t.Fatalf("order 40 recorded as this client's: %+v", got)
			}
			if _, err := conn.claimOrderIDCurrentEpoch(40, conn.orderIDOwned(40)); !errors.Is(err, ErrBrokerIDNamespaceConflict) {
				t.Fatalf("claim of order 40 err=%v, want the frontier refusal", err)
			}
		})
	}
}
