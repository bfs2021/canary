package ibkr

import "testing"

func TestRegressionManualModifyPreservesOCAAndAlgo(t *testing.T) {
	bound := OrderLifecycleEvent{ModifyTermsKnown: true, OrderID: 7, PermID: 101, ClientID: 0, ClientIDPresent: true, Symbol: "SYNX", SecType: "STK", ConID: 900001, Exchange: "SMART", Currency: "USD", Action: "SELL", TotalQuantity: 100, OrderType: "LMT", LimitPrice: 12, TIF: "DAY", Status: "Submitted", OcaGroup: "synthetic-exit-pair", OcaType: 2, AlgoKnown: true, AlgoStrategy: "Adaptive", AdaptivePriority: "Normal"}
	target := TWSOrderControlTarget{PermID: 101, ConID: 900001, Symbol: "SYNX", SecType: "STK", Action: "SELL", Quantity: 100, OrderType: "LMT", LimitPrice: 12, TIF: "DAY"}
	replacement := TWSOrderControlReplacement{Quantity: 100, LimitPrice: 11.5}
	if err := replacement.validate(target); err != nil {
		t.Fatal(err)
	}
	accepted, err := findTWSOrder([]OrderLifecycleEvent{bound}, target)
	if err != nil {
		t.Fatal(err)
	}
	got, err := twsReplacementOrder(accepted, replacement, "DU1234567")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := encodePlaceOrderProtoBody(got); err != nil {
		t.Fatal(err)
	}
	if got.OcaGroup != bound.OcaGroup || got.OcaType != bound.OcaType || got.AdaptivePriority != bound.AdaptivePriority {
		t.Fatalf("accepted modified order loses existing controls: OCA=%q/%d -> %q/%d; Adaptive=%q -> %q", bound.OcaGroup, bound.OcaType, got.OcaGroup, got.OcaType, bound.AdaptivePriority, got.AdaptivePriority)
	}
}

func TestManualModifyChecksRawCallbackTerms(t *testing.T) {
	original := &IBKROrder{OrderID: 7, PermID: 101, Symbol: "SYNX", SecType: "STK", Exchange: "SMART", PrimaryExch: "NASDAQ", Currency: "USD", Action: "SELL", TotalQty: 100, OrderType: "LMT", LmtPrice: 12, TIF: "DAY", Account: "DU1234567", Transmit: true, OcaGroup: "synthetic-exit-pair", OcaType: 2, AdaptivePriority: "Normal", OpenClose: "C"}
	contract, err := encodePlaceOrderContractProto(original)
	if err != nil {
		t.Fatal(err)
	}
	order, err := encodePlaceOrderOrderProto(original)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		extra  []byte
		reject bool
	}{
		{"supported", nil, false},
		{"untransmitted", protoAppendBool(nil, 66, false), true},
		{"parent", protoAppendInt32(nil, 4, 9), true},
		{"hidden", protoAppendBool(nil, 20, true), true},
		{"display-size", protoAppendInt32(nil, 7, 10), true},
		{"condition", protoAppendMessage(nil, 102, []byte{8, 1}), true},
		{"scheduled", protoAppendString(nil, 13, "20261020 12:00:00"), true},
		{"extra-algo", protoAppendMessage(nil, 62, protoAppendString(protoAppendString(nil, 1, "other"), 2, "value")), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := protoAppendInt32(nil, 1, 7)
			body = protoAppendMessage(body, 2, contract)
			body = protoAppendMessage(body, 3, append(append([]byte(nil), order...), tc.extra...))
			body = protoAppendMessage(body, 4, protoAppendString(nil, 1, "Submitted"))
			ev, ok := ParseOrderLifecycleEvent(summarizeOpenOrderProtoCallback(body))
			if !ok {
				t.Fatal("decode failed")
			}
			replacement, err := twsReplacementOrder(ev, TWSOrderControlReplacement{Quantity: 90, LimitPrice: 11.5}, "DU1234567")
			if tc.reject {
				if err == nil {
					t.Fatalf("unsupported terms accepted: %+v", ev)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			wire, err := encodePlaceOrderProtoBody(replacement)
			if err != nil {
				t.Fatal(err)
			}
			final, err := parseOpenOrderProtoCallback(wire)
			if err != nil {
				t.Fatal(err)
			}
			if final.ocaGroup != original.OcaGroup || final.ocaType != original.OcaType || final.adaptivePriority != original.AdaptivePriority || final.primaryExch != original.PrimaryExch || final.openClose != original.OpenClose {
				t.Fatalf("terms lost on wire: %+v", final)
			}
		})
	}
}
