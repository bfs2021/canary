package ibkr

import (
	"errors"
	"testing"
)

// An open order the broker reports for this client ID on the current socket
// is this client's to change, though a previous process placed it below the
// session's nextValidId. On 2026-10-20 the daemon, moved from TWS to IB
// Gateway, could not resize its own trailing stops: the claim refused their
// IDs as "behind the consumed request/order frontier" because only orders
// this process placed counted as owned. The broker's permanent ID comes with
// the report, ownership holds for that socket generation only, and another
// client's (here a manual, client 0) order or a what-if never counts.
func TestInheritedOpenOrderIDIsOwnedForItsSession(t *testing.T) {
	manual := startupBurstOrder(8)
	manual.OrderID, manual.PermID, manual.ClientID = 7, 700099, 0
	whatIf := startupBurstOrder(9)
	whatIf.OrderID, whatIf.PermID, whatIf.WhatIf = 42, 0, true
	conn := startupTestConnection(serveStartup(t, func(s *twsGatewaySession) {
		for _, o := range []testOpenOrderProtoCallback{startupBurstOrder(0), startupBurstOrder(1), manual, whatIf} {
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

	if perm, ok := conn.ReportedOwnOrderPermID(40); !ok || perm != 700000 {
		t.Fatalf("reported order 40: perm=%d ok=%t, want 700000", perm, ok)
	}
	epoch := conn.BrokerSessionEpoch()
	if _, err := conn.claimOrderIDForForwardingAtEpoch(40, conn.orderIDOwned(40), &epoch); err != nil {
		t.Fatalf("claim of the reported own order 40: %v", err)
	}
	conn.discardOrderIDReservation(40)
	if _, err := conn.claimOrderIDCurrentEpoch(41, conn.orderIDOwned(41)); err != nil {
		t.Fatalf("claim of the reported own order 41: %v", err)
	}
	for _, id := range []int{39, 7, 42} { // never reported; a manual order; a what-if
		if _, ok := conn.ReportedOwnOrderPermID(id); ok {
			t.Fatalf("order %d recorded as this client's", id)
		}
		if _, err := conn.claimOrderIDCurrentEpoch(id, conn.orderIDOwned(id)); !errors.Is(err, ErrBrokerIDNamespaceConflict) {
			t.Fatalf("claim of order %d err=%v, want the frontier refusal", id, err)
		}
	}
	conn.resetOrderIDReadiness()
	conn.observeNextValidOrderID(100)
	if _, err := conn.claimOrderIDCurrentEpoch(40, conn.orderIDOwned(40)); !errors.Is(err, ErrBrokerIDNamespaceConflict) {
		t.Fatalf("claim of order 40 on a new socket generation err=%v, want refused until reported again", err)
	}
}
