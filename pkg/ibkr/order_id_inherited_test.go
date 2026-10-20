package ibkr

import (
	"errors"
	"fmt"
	"net"
	"strconv"
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
	noRef := startupBurstOrder(14)
	noRef.OrderID = 46
	refs := func(orders ...testOpenOrderProtoCallback) []testOpenOrderProtoCallback {
		for i := range orders {
			orders[i].OrderRef = fmt.Sprintf("ord-%d", orders[i].OrderID)
		}
		return orders
	}
	conn := startupTestConnection(serveStartup(t, func(s *twsGatewaySession) {
		for _, o := range append(refs(startupBurstOrder(0), startupBurstOrder(1), manual, whatIf, unsent, noAccount, twice, again), noRef) {
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

	if got, ok := conn.ReportedOwnOrder(40); !ok || got != (ReportedOwnOrder{PermID: 700000, Account: "DU1234567", OrderRef: "ord-40"}) {
		t.Fatalf("reported order 40: %+v ok=%t, want permanent ID 700000 for DU1234567, ord-40", got, ok)
	}
	epoch := conn.BrokerSessionEpoch()
	change := func(id, perm int) *OrderChangeIdentity {
		return &OrderChangeIdentity{PermID: perm, Account: "du1234567", OrderRef: fmt.Sprintf("ord-%d", id)}
	}
	if _, err := conn.claimOrderIDForForwardingAtEpoch(40, conn.orderIDOwned(40), &epoch, change(40, 700000)); err != nil {
		t.Fatalf("claim of the reported own order 40: %v", err)
	}
	conn.discardOrderIDReservation(40)
	if _, err := conn.claimOrderIDForForwardingAtEpoch(41, conn.orderIDOwned(41), &epoch, change(41, 700001)); err != nil {
		t.Fatalf("claim of the reported own order 41: %v", err)
	}
	conn.discardOrderIDReservation(41)
	// The claim compares the report with the order the caller means to
	// change, under its lock: a report alone, or one naming another order,
	// never suffices.
	for name, wrong := range map[string]*OrderChangeIdentity{
		"no identity":          nil,
		"another permanent ID": change(40, 700009),
		"another account":      {PermID: 700000, Account: "DU7654321", OrderRef: "ord-40"},
		"another reference":    {PermID: 700000, Account: "DU1234567", OrderRef: "ord-other"},
		"no permanent ID":      {Account: "DU1234567", OrderRef: "ord-40"},
	} {
		if _, err := conn.claimOrderIDForForwardingAtEpoch(40, conn.orderIDOwned(40), &epoch, wrong); !errors.Is(err, ErrBrokerIDNamespaceConflict) {
			t.Fatalf("claim of order 40 with %s err=%v, want the frontier refusal", name, err)
		}
	}
	// A report without a reference does not name an order that has one.
	if _, err := conn.claimOrderIDForForwardingAtEpoch(46, conn.orderIDOwned(46), &epoch, change(46, 700014)); !errors.Is(err, ErrBrokerIDNamespaceConflict) {
		t.Fatalf("claim of order 46 reported without a reference err=%v, want the frontier refusal", err)
	}
	// never reported; a manual order; a what-if; unsent; no account
	for _, id := range []int{39, 7, 42, 43, 44} {
		if _, ok := conn.ReportedOwnOrder(id); ok {
			t.Fatalf("order %d recorded as this client's", id)
		}
		if _, err := conn.claimOrderIDForForwardingAtEpoch(id, conn.orderIDOwned(id), &epoch, change(id, 700099)); !errors.Is(err, ErrBrokerIDNamespaceConflict) {
			t.Fatalf("claim of order %d err=%v, want the frontier refusal", id, err)
		}
	}
	if got, ok := conn.ReportedOwnOrder(45); !ok || !got.Conflicted {
		t.Fatalf("order 45 reported twice with different permanent IDs: %+v ok=%t, want conflicted", got, ok)
	}
	if _, err := conn.claimOrderIDForForwardingAtEpoch(45, conn.orderIDOwned(45), &epoch, change(45, 700013)); !errors.Is(err, ErrBrokerIDNamespaceConflict) {
		t.Fatalf("claim of the conflicted order 45 err=%v, want the frontier refusal", err)
	}
	conn.resetOrderIDReadiness()
	conn.observeNextValidOrderID(100)
	now := conn.BrokerSessionEpoch()
	if _, err := conn.claimOrderIDForForwardingAtEpoch(40, conn.orderIDOwned(40), &now, change(40, 700000)); !errors.Is(err, ErrBrokerIDNamespaceConflict) {
		t.Fatalf("claim of order 40 on a new socket generation err=%v, want refused until reported again", err)
	}
	// Reported again on the new generation: a binding of the earlier one
	// still sees no report, the current one does.
	reported := startupBurstOrder(0)
	reported.OrderRef = "ord-40"
	conn.observeReportedOwnOrderAtEpoch(conn.decodeMessage(encodeOpenOrderProtoCallbackForTest(reported)), now)
	if _, ok := conn.reportedOwnOrderAtEpoch(40, epoch); ok {
		t.Fatal("order 40 reported to a binding of the earlier socket generation")
	}
	if _, ok := conn.reportedOwnOrderAtEpoch(40, now); !ok {
		t.Fatal("order 40 not reported to the current generation's binding")
	}
}

// A frame of an earlier socket generation never records a report: the
// observer checks the generation under the ID lock, and the reader drops a
// stale frame before it. The same frame at the current generation records.
func TestInheritedOpenOrderIDIgnoresAStaleSocketFrame(t *testing.T) {
	conn := startupTestConnection(serveStartup(t, func(s *twsGatewaySession) {
		s.write(twsGatewayFrame(msgManagedAccts, "1", "DU1234567"))
		s.write(twsGatewayFrame(msgNextValidID, "1", "100"))
	}), 1)
	if err := conn.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Disconnect() })
	frame := encodeOpenOrderProtoCallbackForTest(startupBurstOrder(0))
	current := conn.BrokerSessionEpoch()
	conn.processMessageAtEpoch(frame, current-1)
	conn.observeReportedOwnOrderAtEpoch(conn.decodeMessage(frame), current-1)
	if got, ok := conn.ReportedOwnOrder(40); ok {
		t.Fatalf("a stale-generation frame recorded order 40: %+v", got)
	}
	conn.observeReportedOwnOrderAtEpoch(conn.decodeMessage(frame), current)
	if _, ok := conn.ReportedOwnOrder(40); !ok {
		t.Fatal("the current-generation frame did not record order 40 (control)")
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
			epoch := conn.BrokerSessionEpoch()
			if _, err := conn.claimOrderIDForForwardingAtEpoch(40, conn.orderIDOwned(40), &epoch, &OrderChangeIdentity{PermID: 700000, Account: "DU1234567"}); !errors.Is(err, ErrBrokerIDNamespaceConflict) {
				t.Fatalf("claim of order 40 err=%v, want the frontier refusal", err)
			}
		})
	}
}

// A legacy (pre-protobuf) openOrder frame, as servers below version 201 send
// it, still reports an order of this client: its client ID, account,
// reference and permanent ID are read by IB's legacy layout, with the
// message version field below server version 145 (the refuter's second
// round, F3). Without them no report was recorded on such servers and a
// cancel of an order an earlier process placed could never pass.
func TestInheritedOpenOrderIDFromALegacyFrame(t *testing.T) {
	legacy := func(withVersion bool, clientID string) []string {
		fields := []string{strconv.Itoa(msgOpenOrder)}
		if withVersion {
			fields = append(fields, "34")
		}
		return append(fields,
			"40", "910001", "SYNX", "STK", "", "0", "", "", "SMART", "USD", "SYNX", "NMS", // order ID, contract
			"SELL", "100", "TRAIL", "0", "0", "GTC", "", "DU1234567", "C", "0", "ord-40", clientID, "700000", // through permId
			"0", "0", "0", "", "", "", "") // later order fields
	}
	for _, tc := range []struct {
		name          string
		serverVersion int
		fields        []string
		want          bool
	}{
		{"server 176, no version field", 176, legacy(false, "15"), true},
		{"server 140, version field", 140, legacy(true, "15"), true},
		{"another client", 176, legacy(false, "16"), false},
		{"client ID not a number", 176, legacy(false, "x"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn := NewConnection(&ConnectionConfig{Host: "127.0.0.1", Port: 4002, ClientID: 15})
			t.Cleanup(conn.rateLimiter.Stop)
			setServerVersionReady(conn, tc.serverVersion)
			conn.observeReportedOwnOrderAtEpoch(tc.fields, conn.BrokerSessionEpoch())
			got, ok := conn.ReportedOwnOrder(40)
			if ok != tc.want || (ok && got != (ReportedOwnOrder{PermID: 700000, Account: "DU1234567", OrderRef: "ord-40"})) {
				t.Fatalf("report = %+v ok=%t, want %t (permanent ID 700000, DU1234567, ord-40)", got, ok, tc.want)
			}
		})
	}
}

// A what-if in flight for an order ID echoes that order's permanent ID under
// the preview's own reference; it is not a second identity of the order
// (refuter round 3, R3-1). While a what-if is in flight for the ID the report
// observer skips its frames, on the protobuf and the legacy path alike; a
// genuine second reference still marks the report conflicted.
func TestInheritedOpenOrderIDIgnoresAWhatIfEcho(t *testing.T) {
	legacy := func(ref string) []string {
		return append([]string{strconv.Itoa(msgOpenOrder),
			"40", "910001", "SYNX", "STK", "", "0", "", "", "SMART", "USD", "SYNX", "NMS",
			"SELL", "100", "TRAIL", "0", "0", "GTC", "", "DU1234567", "C", "0", ref, "15", "700000"},
			"0", "0", "0", "", "", "", "")
	}
	for _, tc := range []struct {
		name          string
		serverVersion int
		frame         func(*Connection, string) []string
	}{
		{"protobuf", minServerVerProtoBufPlaceOrder, func(c *Connection, ref string) []string {
			o := startupBurstOrder(0)
			o.OrderRef = ref
			return c.decodeMessage(encodeOpenOrderProtoCallbackForTest(o))
		}},
		{"legacy", 176, func(_ *Connection, ref string) []string { return legacy(ref) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn := NewConnection(&ConnectionConfig{Host: "127.0.0.1", Port: 4002, ClientID: 15})
			t.Cleanup(conn.rateLimiter.Stop)
			setServerVersionReady(conn, tc.serverVersion)
			epoch := conn.BrokerSessionEpoch()
			want := ReportedOwnOrder{PermID: 700000, Account: "DU1234567", OrderRef: "ord-40"}
			conn.observeReportedOwnOrderAtEpoch(tc.frame(conn, "ord-40"), epoch)
			if got, ok := conn.ReportedOwnOrder(40); !ok || got != want {
				t.Fatalf("report = %+v ok=%t, want %+v", got, ok, want)
			}
			conn.markWhatIfOrderID(40)
			conn.observeReportedOwnOrderAtEpoch(tc.frame(conn, "preview-synthetic"), epoch)
			conn.clearWhatIfOrderID(40)
			if got, ok := conn.ReportedOwnOrder(40); !ok || got != want {
				t.Fatalf("after the what-if echo: report = %+v ok=%t, want it unchanged %+v", got, ok, want)
			}
			conn.observeReportedOwnOrderAtEpoch(tc.frame(conn, "ord-other"), epoch)
			if got, ok := conn.ReportedOwnOrder(40); !ok || !got.Conflicted {
				t.Fatalf("after a genuine second reference: report = %+v ok=%t, want conflicted", got, ok)
			}
		})
	}
}

// A send that names an order to change claims its ID only through this
// process's placement or the broker's matching report, never because the ID
// happens to lie at or above the frontier (refuter round 3, R3-2).
func TestClaimOfAChangeNeedsOwnershipOrAReport(t *testing.T) {
	conn := NewConnection(&ConnectionConfig{Host: "127.0.0.1", Port: 4002, ClientID: 15})
	t.Cleanup(conn.rateLimiter.Stop)
	setServerVersionReady(conn, minServerVerProtoBufPlaceOrder)
	conn.observeNextValidOrderID(100)
	epoch := conn.BrokerSessionEpoch()
	change := &OrderChangeIdentity{PermID: 700150, Account: "DU1234567", OrderRef: "ord-150"}
	if _, fresh, err := conn.claimOrderIDForForwardingAtEpochFresh(150, false, &epoch, change); !errors.Is(err, ErrBrokerIDNamespaceConflict) || fresh {
		t.Fatalf("claim of a change of the unreported, unplaced order 150 ahead of the frontier: fresh=%t err=%v, want the refusal", fresh, err)
	}
	if _, fresh, err := conn.claimOrderIDForForwardingAtEpochFresh(150, false, &epoch, nil); err != nil || !fresh {
		t.Fatalf("claim of a new order at 150: fresh=%t err=%v, want a fresh claim", fresh, err)
	}
}
