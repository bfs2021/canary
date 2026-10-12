package ibkr

import (
	"strings"
	"testing"
)

func ocaTestOrder() IBKROrder {
	return IBKROrder{
		OrderID: 7, ClientID: 31, Account: "DU1234567",
		ConID: 4391, Symbol: "AMD", SecType: "STK", Exchange: "SMART", Currency: "USD",
		Action: "SELL", TotalQty: 70, OrderType: "LMT", LmtPrice: 100, TIF: "DAY", Transmit: true,
	}
}

// A one-cancels-all membership is encoded on the wire (Order.proto
// ocaGroup=27, ocaType=30) and read back from the broker's openOrder, which
// uses the same field numbers; an order without one reads none.
func TestPlaceOrderProtoCarriesOCAMembership(t *testing.T) {
	order := ocaTestOrder()
	order.OcaGroup, order.OcaType = "canary-oca-ord-prot", OcaTypeReduceWithBlock
	raw, err := encodePlaceOrderProtoBody(&order)
	if err != nil {
		t.Fatal(err)
	}
	event, ok := parseOpenOrderProtoEvent(summarizeOpenOrderProtoCallback(raw))
	if !ok || event.OcaGroup != "canary-oca-ord-prot" || event.OcaType != OcaTypeReduceWithBlock {
		t.Fatalf("membership lost on the wire: %+v", event)
	}
	plain := ocaTestOrder()
	raw, err = encodePlaceOrderProtoBody(&plain)
	if err != nil {
		t.Fatal(err)
	}
	event, ok = parseOpenOrderProtoEvent(summarizeOpenOrderProtoCallback(raw))
	if !ok || event.OcaGroup != "" || event.OcaType != 0 {
		t.Fatalf("plain order misread as a group member: %+v", event)
	}
}

// Half a membership never reaches the wire: a type without a group, or a
// group with no type or one IBKR does not define.
func TestPlaceOrderProtoRefusesHalfAnOCAMembership(t *testing.T) {
	for _, tc := range []struct {
		name  string
		group string
		typ   int
		want  string
	}{
		{name: "type without group", typ: OcaTypeReduceWithBlock, want: "needs an ocaGroup"},
		{name: "group without type", group: "canary-oca-x", want: "ocaType"},
		{name: "group with an undefined type", group: "canary-oca-x", typ: 4, want: "ocaType"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			order := ocaTestOrder()
			order.OcaGroup, order.OcaType = tc.group, tc.typ
			_, err := encodePlaceOrderProtoBody(&order)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want a refusal mentioning %q", err, tc.want)
			}
		})
	}
}
