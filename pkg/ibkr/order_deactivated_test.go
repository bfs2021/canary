package ibkr

import "testing"

func TestOpenOrderDeactivatedPresence(t *testing.T) {
	for _, state := range []*bool{nil, new(false), new(true)} {
		order := protoAppendInt64(nil, 3, 7001)
		if state != nil {
			order = protoAppendBool(order, 138, *state)
		}
		body := protoAppendMessage(protoAppendInt32(nil, 1, 101), 3, order)
		body = protoAppendMessage(body, 4, protoAppendString(nil, 1, "Inactive"))
		event, ok := ParseOrderLifecycleEvent(summarizeOpenOrderProtoCallback(body))
		if !ok || event.Status != "Inactive" || (event.Deactivated == nil) != (state == nil) {
			t.Fatalf("lost original status or presence: %+v", event)
		}
		if state != nil && *event.Deactivated != *state {
			t.Fatal("lost deactivation value")
		}
	}
	for _, bad := range [][]byte{protoAppendInt32(nil, 138, 2), protoAppendString(nil, 138, "true")} {
		body := protoAppendMessage(protoAppendInt32(nil, 1, 101), 3, bad)
		if _, ok := ParseOrderLifecycleEvent(summarizeOpenOrderProtoCallback(body)); ok {
			t.Fatal("accepted malformed deactivation")
		}
	}
}
