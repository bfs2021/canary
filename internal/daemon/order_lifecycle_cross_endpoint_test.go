package daemon

import (
	"testing"

	"github.com/osauer/canary/v2/internal/rpc"
)

// A callback pairs with a row placed through another socket only when
// exactly one row qualifies and no row of the scope contradicts it; the
// callback's own route always wins (the v3.25.4 review's finding 2).
func TestCrossEndpointPairingRefusesAmbiguousOrContradictedRows(t *testing.T) {
	t.Parallel()
	row := func(endpoint, ref string, orderID, perm int) rpc.OrderView {
		return rpc.OrderView{Endpoint: endpoint, ClientID: 31, Account: "DU1234567", Mode: "paper", OrderRef: ref, ReservedOrderID: orderID, PermID: perm, Open: true}
	}
	callback := orderJournalEvent{Type: orderJournalEventBrokerAcknowledged, Endpoint: "127.0.0.1:4002", ClientID: 31, clientIDPresent: true,
		Account: "DU1234567", Mode: "paper", OrderRef: "ord-prot", ReservedOrderID: 1002, PermID: 790001}
	for _, tc := range []struct {
		name     string
		ev       func(orderJournalEvent) orderJournalEvent
		views    []rpc.OrderView
		endpoint string // the paired row's endpoint, "" for no pairing
	}{
		{"one row on another socket", nil,
			[]rpc.OrderView{row("127.0.0.1:7497", "ord-prot", 1002, 790001)}, "127.0.0.1:7497"},
		{"an unacknowledged row on another socket", nil,
			[]rpc.OrderView{row("127.0.0.1:7497", "ord-prot", 1002, 0)}, "127.0.0.1:7497"},
		{"an unacknowledged row under another order ID",
			func(ev orderJournalEvent) orderJournalEvent { ev.ReservedOrderID = 1003; return ev },
			[]rpc.OrderView{row("127.0.0.1:7497", "ord-prot", 1002, 0)}, ""},
		{"an unacknowledged row, callback without the order ID",
			func(ev orderJournalEvent) orderJournalEvent { ev.ReservedOrderID = 0; return ev },
			[]rpc.OrderView{row("127.0.0.1:7497", "ord-prot", 1002, 0)}, ""},
		{"the callback's own route wins", nil,
			[]rpc.OrderView{row("127.0.0.1:7497", "ord-prot", 1002, 0), row("127.0.0.1:4002", "ord-prot", 1002, 0)}, "127.0.0.1:4002"},
		{"two unacknowledged rows on other sockets", nil,
			[]rpc.OrderView{row("127.0.0.1:7497", "ord-prot", 1002, 0), row("127.0.0.1:7496", "ord-prot", 1002, 0)}, ""},
		{"the reference on this socket under another permanent ID", nil,
			[]rpc.OrderView{row("127.0.0.1:7497", "ord-prot", 1002, 790001), row("127.0.0.1:4002", "ord-prot", 1002, 790002)}, ""},
		{"an error callback without client or permanent ID",
			func(ev orderJournalEvent) orderJournalEvent {
				ev.Type, ev.OrderRef, ev.PermID, ev.clientIDPresent = orderJournalEventBrokerError, "", 0, false
				return ev
			},
			[]rpc.OrderView{row("127.0.0.1:7497", "ord-prot", 1002, 790001)}, ""},
		{"a row of another client on another socket", nil,
			[]rpc.OrderView{func() rpc.OrderView { v := row("127.0.0.1:7497", "ord-prot", 1002, 790001); v.ClientID = 0; return v }()}, ""},
	} {
		ev := callback
		if tc.ev != nil {
			ev = tc.ev(ev)
		}
		view, ok := orderJournalViewForLifecycleEvent(ev, tc.views)
		switch {
		case tc.endpoint == "" && ok:
			t.Errorf("%s: paired with the row on %s, want no pairing", tc.name, view.Endpoint)
		case tc.endpoint != "" && (!ok || view.Endpoint != tc.endpoint):
			t.Errorf("%s: paired=%t endpoint %s, want the row on %s", tc.name, ok, view.Endpoint, tc.endpoint)
		}
	}
}
