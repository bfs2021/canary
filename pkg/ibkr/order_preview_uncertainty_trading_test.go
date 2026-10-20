//go:build trading

package ibkr

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func previewStatusFrame(status string) []byte { return previewStatusFrameFor(40, 700000, 41, status) }

func previewStatusFrameFor(id, perm, client int, status string) []byte {
	body := protoAppendInt32(nil, 1, int32(id))
	body = protoAppendString(body, 2, status)
	body = protoAppendString(body, 3, "0")
	body = protoAppendString(body, 4, "100")
	body = protoAppendInt64(body, 6, int64(perm))
	body = protoAppendInt32(body, 9, int32(client))
	return encodeProtoCallbackFrameForTest(protoOrderStatusMsgID, body)
}

func TestModifyPreviewStatusUncertaintyAndRecovery(t *testing.T) {
	for _, mode := range []string{"working", "absent", "cancelled_after_working_report"} {
		t.Run(mode, func(t *testing.T) {
			conn, connector, socket, gate := inheritedStopFixture(t)
			connector.ensureHandlersRegistered(conn)
			binding, ok := connector.CaptureSession()
			if !ok {
				t.Fatal("no session")
			}
			var uncertain, actualCancel, legacyCancel, snapshots atomic.Int64
			connector.RegisterOrderLifecycleReceiptHandler(func(r OrderLifecycleReceipt) {
				if r.Uncertain {
					uncertain.Add(1)
				} else if r.Event.Status == "Cancelled" {
					actualCancel.Add(1)
				}
			})
			connector.RegisterOrderLifecycleHandler(func(ev OrderLifecycleEvent) {
				if ev.Status == "Cancelled" {
					legacyCancel.Add(1)
				}
			})
			connector.requestAllOpenOrders = func() error {
				snapshots.Add(1)
				if mode != "absent" {
					conn.processMessage(encodeOpenOrderProtoCallbackForTest(inheritedStopCallback(100, false)))
					conn.processMessage(previewStatusFrame("PreSubmitted"))
				}
				if mode == "cancelled_after_working_report" {
					conn.processMessage(previewStatusFrame("Cancelled"))
				}
				conn.processMessage(conn.encodeMsg(msgOpenOrderEnd, "1"))
				return nil
			}
			contract, order := inheritedStopRawOrder(60, &OrderChangeIdentity{PermID: 700000, Account: "DU7654321", OrderRef: "ord-40"})
			type outcome struct {
				result OrderWhatIfResult
				err    error
			}
			done := make(chan outcome, 1)
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			resetSocket(socket)
			go func() {
				r, e := connector.PreviewOrderWhatIfWithOrderIDForSession(ctx, binding, contract, order, 40)
				done <- outcome{r, e}
			}()
			deadline := time.Now().Add(time.Second)
			for socket.Len() == 0 && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if socket.Len() == 0 {
				t.Fatal("preview did not send")
			}
			generation := connector.OrderLifecycleGeneration()
			conn.processMessage(previewStatusFrame("Cancelled"))
			if uncertain.Load() != 1 || actualCancel.Load() != 0 || legacyCancel.Load() != 0 || connector.OrderLifecycleGeneration() == generation {
				t.Fatalf("ambiguous callback treated as fact or lost: uncertain=%d real=%d legacy=%d", uncertain.Load(), actualCancel.Load(), legacyCancel.Load())
			}
			// Even a locally indexed/placed order cannot bypass the uncertainty fence.
			resetSocket(socket)
			if err := connector.SubmitPaperOrderForSession(binding, gate, contract, order); err == nil || socket.Len() != 0 {
				t.Fatalf("uncertain modify err=%v bytes=%d", err, socket.Len())
			}
			conn.processMessage(encodeOpenOrderProtoCallbackForTest(inheritedStopCallback(60, true)))
			got := <-done
			if got.err != nil || got.result.Status != OrderWhatIfStatusUnavailable || !got.result.WorkingOrderUncertain || snapshots.Load() != 1 {
				t.Fatalf("result=%+v err=%v snapshots=%d", got.result, got.err, snapshots.Load())
			}
			resetSocket(socket)
			err := connector.SubmitPaperOrderForSession(binding, gate, contract, order)
			if mode == "working" {
				if err != nil || socket.Len() == 0 {
					t.Fatalf("fresh working report did not recover: %v", err)
				}
			} else if err == nil || socket.Len() != 0 {
				t.Fatalf("absent/later-cancelled order recovered: err=%v bytes=%d", err, socket.Len())
			}
			if mode == "cancelled_after_working_report" && actualCancel.Load() != 1 {
				t.Fatal("post-preview real cancellation not delivered")
			}
		})
	}
}

func TestModifyPreviewUncertaintyOldFlightAndEpochControls(t *testing.T) {
	conn, connector, _, _ := inheritedStopFixture(t)
	connector.ensureHandlersRegistered(conn)
	epoch := conn.BrokerSessionEpoch()
	oldTokens, oldGeneration, oldSafe := conn.modifyStatusRecoveryTokens(epoch)
	if !oldSafe {
		t.Fatal("initial snapshot not safe")
	}
	if err := conn.beginModifyPreview(40, epoch); err != nil {
		t.Fatal(err)
	}
	if err := conn.beginModifyPreview(40, epoch); err == nil {
		t.Fatal("overlapping preview admitted")
	}
	fields := conn.decodeMessage(previewStatusFrame("Cancelled"))
	disposition := conn.observeReportedOrderStatusAtEpoch(fields, epoch)
	if disposition != previewStatusUncertain {
		t.Fatal("no uncertainty")
	}
	conn.finishModifyPreview(40, epoch)
	// A callback already classified during preview stays uncertain after it ends.
	var uncertain atomic.Int64
	connector.RegisterOrderLifecycleReceiptHandler(func(r OrderLifecycleReceipt) {
		if r.Uncertain {
			uncertain.Add(1)
		}
	})
	connector.notifyOrderLifecycleReceiptFrom(conn, epoch, fields, disposition)
	if uncertain.Load() != 1 {
		t.Fatal("receipt lost its uncertainty classification")
	}
	ev, ok := ParseOrderLifecycleEvent(conn.decodeMessage(encodeOpenOrderProtoCallbackForTest(inheritedStopCallback(100, false))))
	if !ok {
		t.Fatal("bad fixture")
	}
	if conn.recoverModifyStatusFromSnapshot(epoch, oldTokens, oldGeneration, []OrderLifecycleEvent{ev}) || !conn.modifyStatusIsUncertain(40, epoch) {
		t.Fatal("old snapshot cleared ambiguity")
	}
	tokens, generation, safe := conn.modifyStatusRecoveryTokens(epoch)
	if !safe {
		t.Fatal("post-window snapshot not eligible")
	}
	ev.WhatIf = true
	conn.recoverModifyStatusFromSnapshot(epoch, tokens, generation, []OrderLifecycleEvent{ev})
	if !conn.modifyStatusIsUncertain(40, epoch) {
		t.Fatal("preview echo cleared ambiguity")
	}
	ev.WhatIf = false
	conn.recoverModifyStatusFromSnapshot(epoch-1, tokens, generation, []OrderLifecycleEvent{ev})
	if !conn.modifyStatusIsUncertain(40, epoch) {
		t.Fatal("old epoch cleared ambiguity")
	}
	conn.recoverModifyStatusFromSnapshot(epoch, tokens, generation, []OrderLifecycleEvent{ev})
	if conn.modifyStatusIsUncertain(40, epoch) {
		t.Fatal("fresh matching snapshot did not recover")
	}
}

func TestClientZeroLocallyPlacedPreviewRecoversOnlyExactIdentity(t *testing.T) {
	conn, c, socket, _ := inheritedStopFixture(t)
	conn.config.ClientID = 0
	c.ensureHandlersRegistered(conn)
	binding, _ := c.CaptureSession()
	contract, order := inheritedStopRawOrder(60, nil)
	order.OrderID, order.OrderRef, order.ClientID = 150, "ord-local-zero", 0
	if err := c.SubmitOrderForSession(binding, contract, order); err != nil || !c.PlacedOrderID(150) {
		t.Fatalf("client0 local placement fixture failed: %v", err)
	}
	callback := inheritedStopCallback(60, false)
	callback.OrderID, callback.PermID, callback.OrderRef, callback.ClientID = 150, 700150, "ord-local-zero", 0
	conn.processMessage(encodeOpenOrderProtoCallbackForTest(callback))
	if _, adopted := conn.ReportedOwnOrder(150); adopted {
		t.Fatal("client0 report unexpectedly granted inherited ownership")
	}
	order.Changes = &OrderChangeIdentity{PermID: 700150, Account: "DU7654321", OrderRef: "ord-local-zero"}
	c.requestAllOpenOrders = func() error {
		conn.processMessage(encodeOpenOrderProtoCallbackForTest(callback))
		conn.processMessage(previewStatusFrameFor(150, 700150, 0, "PreSubmitted"))
		conn.processMessage(conn.encodeMsg(msgOpenOrderEnd, "1"))
		return nil
	}
	type previewOutcome struct {
		result OrderWhatIfResult
		err    error
	}
	done := make(chan previewOutcome, 1)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	resetSocket(socket)
	go func() {
		result, err := c.PreviewOrderWhatIfWithOrderIDForSession(ctx, binding, contract, order, 150)
		done <- previewOutcome{result, err}
	}()
	deadline := time.Now().Add(time.Second)
	for socket.Len() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if socket.Len() == 0 || !conn.IsWhatIfOrderID(150) {
		t.Fatal("client0 preview did not start")
	}
	conn.processMessage(previewStatusFrameFor(150, 700150, 0, "Cancelled"))
	echo := callback
	echo.WhatIf = true
	conn.processMessage(encodeOpenOrderProtoCallbackForTest(echo))
	got := <-done
	if got.err != nil || got.result.Status != OrderWhatIfStatusUnavailable {
		t.Fatalf("client0 ambiguous preview result=%+v err=%v", got.result, got.err)
	}
	if _, adopted := conn.ReportedOwnOrder(150); adopted {
		t.Fatal("client0 recovery invented inherited ownership")
	}
	resetSocket(socket)
	if err := c.SubmitOrderForSession(binding, contract, order); err != nil || socket.Len() == 0 {
		t.Fatalf("exact fresh client0 local-order recovery failed: err=%v bytes=%d", err, socket.Len())
	}
}
