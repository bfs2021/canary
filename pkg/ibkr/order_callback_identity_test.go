package ibkr

import "testing"

func rawContractIdentityFixture(t *testing.T, extra []byte) OrderLifecycleEvent {
	t.Helper()
	order := &IBKROrder{OrderID: 7, PermID: 101, ConID: 900001, Symbol: "SYNX", SecType: "STK", Exchange: "SMART", Currency: "USD", Action: "SELL", TotalQty: 100, OrderType: "LMT", LmtPrice: 12, TIF: "DAY", Account: "DU1234567", Transmit: true, OcaGroup: "synthetic-pair", OcaType: 2}
	contract, err := encodePlaceOrderContractProto(order)
	if err != nil {
		t.Fatal(err)
	}
	terms, err := encodePlaceOrderOrderProto(order)
	if err != nil {
		t.Fatal(err)
	}
	body := protoAppendInt32(nil, 1, 7)
	body = protoAppendMessage(body, 2, contract)
	body = protoAppendMessage(body, 3, append(terms, extra...))
	body = protoAppendMessage(body, 4, protoAppendString(nil, 1, "Submitted"))
	ev, ok := ParseOrderLifecycleEvent(summarizeOpenOrderProtoCallback(body))
	if !ok {
		t.Fatal("decode failed")
	}
	return ev
}
func TestRawCallbackPreservesContractIdentity(t *testing.T) {
	ev := rawContractIdentityFixture(t, nil)
	target := TWSOrderControlTarget{PermID: 101, ConID: 900002, Symbol: "SYNX", SecType: "STK", Action: "SELL", Quantity: 100, OrderType: "LMT", LimitPrice: 12, TIF: "DAY"}
	_, matchErr := findTWSOrder([]OrderLifecycleEvent{ev}, target)
	replacement, replaceErr := twsReplacementOrder(ev, TWSOrderControlReplacement{Quantity: 90, LimitPrice: 11.5}, "DU1234567")
	t.Logf("different reviewed ConID=900002 check error=%v; replacement=%+v error=%v", matchErr, replacement, replaceErr)
	if matchErr == nil {
		t.Error("accepted a mismatched reviewed contract")
	}
	if replaceErr != nil || replacement.ConID != 900001 {
		t.Error("replacement lost contract identity")
	}
	if ev.ConID != 900001 {
		t.Fatalf("raw Contract.conId=900001 lost: lifecycle ConID=%d; ModifyTermsKnown=%v unsupported=%q", ev.ConID, ev.ModifyTermsKnown, ev.ModifyUnsupported)
	}
}
