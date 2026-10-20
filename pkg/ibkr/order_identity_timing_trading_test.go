//go:build trading

package ibkr

import (
	"context"
	"errors"
	"testing"
	"time"
)

// A real openOrder on the same ID that arrives while its modify preview
// waits must still invalidate the inherited identity; a WhatIf echo must not.
func TestConflictingRealReportDuringModifyPreview(t *testing.T) {
	conn, connector, socket, gate := inheritedStopFixture(t)
	binding, ok := connector.CaptureSession()
	if !ok {
		t.Fatal("no session")
	}
	change := &OrderChangeIdentity{PermID: 700000, Account: "DU7654321", OrderRef: "ord-40"}
	contract, order := inheritedStopRawOrder(60, change)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	type outcome struct {
		res OrderWhatIfResult
		err error
	}
	done := make(chan outcome, 1)
	resetSocket(socket)
	go func() {
		res, err := connector.PreviewOrderWhatIfWithOrderIDForSession(ctx, binding, contract, order, 40)
		done <- outcome{res, err}
	}()
	deadline := time.Now().Add(time.Second)
	for socket.Len() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if socket.Len() == 0 {
		t.Fatal("preview never sent")
	}
	other := inheritedStopCallback(100, false)
	other.PermID, other.OrderRef = 700999, "ord-other"
	conn.processMessage(encodeOpenOrderProtoCallbackForTest(other))
	conn.processMessage(encodeOpenOrderProtoCallbackForTest(inheritedStopCallback(60, true)))
	got := <-done
	if got.err != nil || got.res.Status != OrderWhatIfStatusAccepted {
		t.Fatalf("preview result %+v err=%v", got.res, got.err)
	}
	report, _ := conn.ReportedOwnOrder(40)
	resetSocket(socket)
	err := connector.SubmitPaperOrderForSession(binding, gate, contract, order)
	t.Logf("after real conflicting openOrder during preview: report=%+v modify_err=%v bytes=%d", report, err, socket.Len())
	if !report.Conflicted || err == nil || socket.Len() != 0 {
		t.Fatal("a genuine conflicting broker report was lost during preview and the stale identity could still send")
	}
}

// Hold transport admission after the Connector has claimed/indexed the
// inherited order. A new exact report arrives before the first byte.
func TestModifyRechecksIdentityAfterClaimAtWire(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		name := "same_identity_control"
		if conflict {
			name = "conflicting_identity"
		}
		t.Run(name, func(t *testing.T) {
			conn, connector, socket, gate := inheritedStopFixture(t)
			binding, ok := connector.CaptureSession()
			if !ok {
				t.Fatal("no session")
			}
			change := &OrderChangeIdentity{PermID: 700000, Account: "DU7654321", OrderRef: "ord-40"}
			contract, order := inheritedStopRawOrder(60, change)
			resetSocket(socket)
			conn.transportMu.Lock()
			locked := true
			defer func() {
				if locked {
					conn.transportMu.Unlock()
				}
			}()
			done := make(chan error, 1)
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			go func() {
				done <- connector.SubmitPaperOrderForSessionGuarded(ctx, binding, gate, contract, order, nil)
			}()
			indexed := false
			deadline := time.Now().Add(time.Second)
			for time.Now().Before(deadline) {
				connector.orderMu.RLock()
				_, indexed = connector.openOrders["ord-40"]
				connector.orderMu.RUnlock()
				if indexed {
					break
				}
				time.Sleep(time.Millisecond)
			}
			if !indexed {
				t.Fatal("modify was not claimed/indexed")
			}
			event := inheritedStopCallback(100, false)
			if conflict {
				event.PermID, event.OrderRef = 700999, "ord-other"
			}
			conn.processMessage(encodeOpenOrderProtoCallbackForTest(event))
			report, _ := conn.ReportedOwnOrder(40)
			conn.transportMu.Unlock()
			locked = false
			err := <-done
			t.Logf("report before wire=%+v modify_err=%v bytes=%d", report, err, socket.Len())
			if conflict {
				if err == nil || socket.Len() != 0 || !errors.Is(err, ErrBrokerIDNamespaceConflict) {
					t.Fatal("a conflicting report known before the wire write did not refuse the stale modify")
				}
			} else if err != nil || socket.Len() == 0 {
				t.Fatal("same-identity control did not send")
			}
		})
	}
}

// The local order index is rolled back on a definite pre-wire refusal.
// The new placed-ID ownership index must roll back with it.
func TestDefinitelyUnsentOrderDoesNotBecomePlaced(t *testing.T) {
	_, connector, socket, gate := inheritedStopFixture(t)
	binding, ok := connector.CaptureSession()
	if !ok {
		t.Fatal("no session")
	}
	contract, order := inheritedStopRawOrder(60, nil)
	order.OrderID, order.OrderRef = 150, "ord-never-sent"
	resetSocket(socket)
	refusal := errors.New("synthetic final authority refusal")
	err := connector.SubmitPaperOrderForSessionGuarded(t.Context(), binding, gate, contract, order, func() error {
		return refusal
	})
	if !errors.Is(err, refusal) || SendDispositionOf(err) != SendDispositionDefinitelyUnsent || socket.Len() != 0 {
		t.Fatalf("fixture pre-wire refusal err=%v disposition=%s bytes=%d", err, SendDispositionOf(err), socket.Len())
	}
	placed := connector.PlacedOrderID(150)
	order.Changes = &OrderChangeIdentity{PermID: 700150, Account: "DU7654321", OrderRef: "ord-never-sent"}
	err = connector.SubmitPaperOrderForSession(binding, gate, contract, order)
	t.Logf("after definite non-send: placed=%t subsequent_change_err=%v bytes=%d", placed, err, socket.Len())
	if placed || !errors.Is(err, ErrBrokerIDNamespaceConflict) || socket.Len() != 0 {
		t.Fatal("a definitely unsent order was treated as placed and bypassed the required identity report")
	}
}

func TestKnownOrderCannotKeepIdentityAfterDifferentAccountReport(t *testing.T) {
	conn, connector, socket, gate := inheritedStopFixture(t)
	binding, ok := connector.CaptureSession()
	if !ok {
		t.Fatal("no session")
	}
	other := inheritedStopCallback(100, false)
	other.PermID, other.OrderRef, other.Account = 700999, "ord-other", "DU1234567"
	conn.processMessage(encodeOpenOrderProtoCallbackForTest(other))
	report, _ := conn.ReportedOwnOrder(40)
	resetSocket(socket)
	contract, order := inheritedStopRawOrder(60, &OrderChangeIdentity{PermID: 700000, Account: "DU7654321", OrderRef: "ord-40"})
	err := connector.SubmitPaperOrderForSession(binding, gate, contract, order)
	t.Logf("after same-client/ID report for another account: report=%+v change_err=%v bytes=%d", report, err, socket.Len())
	if !report.Conflicted || err == nil || socket.Len() != 0 {
		t.Fatal("filtering another account's report preserved stale authority over its client-local order ID")
	}
}

// Local callback indexing is not proof that an order reached the broker.
func TestQueuedFreshOrderIsNotPlacedBeforeItsFirstByte(t *testing.T) {
	_, connector, socket, gate := inheritedStopFixture(t)
	binding, ok := connector.CaptureSession()
	if !ok {
		t.Fatal("no session")
	}
	contract, order := inheritedStopRawOrder(60, nil)
	order.OrderID, order.OrderRef = 150, "ord-not-yet-sent"
	resetSocket(socket)
	checked := false
	err := connector.SubmitPaperOrderForSessionGuarded(t.Context(), binding, gate, contract, order, func() error {
		checked = true
		if connector.PlacedOrderID(150) {
			return errors.New("callback indexing already granted placement authority before the first byte")
		}
		return nil
	})
	if err != nil || !checked || socket.Len() == 0 || !connector.PlacedOrderID(150) {
		t.Fatalf("send err=%v checked=%t bytes=%d placed-after=%t", err, checked, socket.Len(), connector.PlacedOrderID(150))
	}
	resetSocket(socket)
	refusal := errors.New("synthetic later refusal")
	order.Changes = &OrderChangeIdentity{PermID: 700150, Account: "DU7654321", OrderRef: order.OrderRef}
	err = connector.SubmitPaperOrderForSessionGuarded(t.Context(), binding, gate, contract, order, func() error { return refusal })
	if !errors.Is(err, refusal) || socket.Len() != 0 || !connector.PlacedOrderID(150) {
		t.Fatalf("earlier placement lost on refused change: err=%v bytes=%d placed=%t", err, socket.Len(), connector.PlacedOrderID(150))
	}
}

func TestForeignFirstReportCannotEraseCollision(t *testing.T) {
	conn, connector, socket, gate := inheritedStopFixture(t)
	conn.resetOrderIDReadiness()
	conn.observeNextValidOrderID(100)
	binding, ok := connector.CaptureSession()
	if !ok {
		t.Fatal("no fresh session")
	}
	other := inheritedStopCallback(100, false)
	other.PermID, other.OrderRef, other.Account = 700999, "ord-other", "DU1234567"
	conn.processMessage(encodeOpenOrderProtoCallbackForTest(other))
	conn.processMessage(encodeOpenOrderProtoCallbackForTest(inheritedStopCallback(100, false)))
	report, _ := conn.ReportedOwnOrder(40)
	resetSocket(socket)
	contract, order := inheritedStopRawOrder(60, &OrderChangeIdentity{PermID: 700000, Account: "DU7654321", OrderRef: "ord-40"})
	err := connector.SubmitPaperOrderForSession(binding, gate, contract, order)
	t.Logf("foreign-account then own-account same-client/ID reports: report=%+v change_err=%v bytes=%d", report, err, socket.Len())
	if !report.Conflicted || err == nil || socket.Len() != 0 {
		t.Fatal("reversing the two contradictory reports changed order identity authority")
	}
}

func TestContradictoryStatusCannotPreserveOldOpenOrderAuthority(t *testing.T) {
	conn, connector, socket, gate := inheritedStopFixture(t)
	connector.ensureHandlersRegistered(conn)
	binding, _ := connector.CaptureSession()
	body := protoAppendInt32(nil, 1, 40)
	body = protoAppendString(body, 2, "Submitted")
	body = protoAppendString(body, 3, "0")
	body = protoAppendString(body, 4, "100")
	body = protoAppendInt64(body, 6, 700999)
	body = protoAppendInt32(body, 9, 41)
	frame := encodeProtoCallbackFrameForTest(protoOrderStatusMsgID, body)
	event, ok := ParseOrderLifecycleEvent(conn.decodeMessage(frame))
	if !ok || !event.ClientIDPresent || event.ClientID != 41 || event.PermID != 700999 {
		t.Fatalf("fixture callback did not name the conflicting identity: %+v", event)
	}
	conn.processMessage(frame)
	report, _ := conn.ReportedOwnOrder(40)
	resetSocket(socket)
	contract, order := inheritedStopRawOrder(60, &OrderChangeIdentity{PermID: 700000, Account: "DU7654321", OrderRef: "ord-40"})
	err := connector.SubmitPaperOrderForSession(binding, gate, contract, order)
	t.Logf("after exact same-client/ID different-PermID status: report=%+v change_err=%v bytes=%d", report, err, socket.Len())
	if !report.Conflicted || err == nil || socket.Len() != 0 {
		t.Fatal("contradictory broker status left an older openOrder's write authority intact")
	}
}

func TestPreviewRechecksIdentityAtWire(t *testing.T) {
	conn, connector, socket, _ := inheritedStopFixture(t)
	binding, _ := connector.CaptureSession()
	contract, order := inheritedStopRawOrder(60, &OrderChangeIdentity{PermID: 700000, Account: "DU7654321", OrderRef: "ord-40"})
	resetSocket(socket)
	conn.transportMu.Lock()
	locked := true
	defer func() {
		if locked {
			conn.transportMu.Unlock()
		}
	}()
	type outcome struct {
		result OrderWhatIfResult
		err    error
	}
	done := make(chan outcome, 1)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	go func() {
		result, err := connector.PreviewOrderWhatIfWithOrderIDForSession(ctx, binding, contract, order, 40)
		done <- outcome{result, err}
	}()
	deadline := time.Now().Add(time.Second)
	for !conn.IsWhatIfOrderID(40) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !conn.IsWhatIfOrderID(40) {
		t.Fatal("preview never claimed ID")
	}
	other := inheritedStopCallback(100, false)
	other.PermID, other.OrderRef = 700999, "ord-other"
	conn.processMessage(encodeOpenOrderProtoCallbackForTest(other))
	conn.transportMu.Unlock()
	locked = false
	result := <-done
	if result.result.Status != OrderWhatIfStatusUnavailable || socket.Len() != 0 {
		t.Fatalf("preview after conflict: %+v err=%v bytes=%d", result.result, result.err, socket.Len())
	}
}

func TestLocallyPlacedContradictionStillRefusesAtWire(t *testing.T) {
	conn, connector, socket, gate := inheritedStopFixture(t)
	binding, _ := connector.CaptureSession()
	contract, order := inheritedStopRawOrder(100, nil)
	order.OrderID, order.OrderRef = 150, "ord-local"
	resetSocket(socket)
	if err := connector.SubmitPaperOrderForSession(binding, gate, contract, order); err != nil {
		t.Fatal(err)
	}
	if !connector.PlacedOrderID(150) {
		t.Fatal("fixture order was not placed")
	}
	report := inheritedStopCallback(100, false)
	report.OrderID, report.PermID, report.OrderRef = 150, 700150, "ord-local"
	conn.processMessage(encodeOpenOrderProtoCallbackForTest(report))
	order.TotalQty = 30
	order.Changes = &OrderChangeIdentity{PermID: 700150, Account: "DU7654321", OrderRef: "ord-local"}
	resetSocket(socket)
	conn.transportMu.Lock()
	locked := true
	defer func() {
		if locked {
			conn.transportMu.Unlock()
		}
	}()
	done := make(chan error, 1)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	go func() {
		done <- connector.SubmitPaperOrderForSessionGuarded(ctx, binding, gate, contract, order, nil)
	}()
	indexed := false
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		connector.orderMu.RLock()
		indexed = connector.openOrders["ord-local"].Quantity == 30
		connector.orderMu.RUnlock()
		if indexed {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !indexed {
		t.Fatal("modify was not indexed")
	}
	report.PermID, report.OrderRef = 700999, "ord-other"
	conn.processMessage(encodeOpenOrderProtoCallbackForTest(report))
	conn.transportMu.Unlock()
	locked = false
	err := <-done
	if !errors.Is(err, ErrBrokerIDNamespaceConflict) || socket.Len() != 0 || !connector.PlacedOrderID(150) {
		t.Fatalf("locally placed identity conflict err=%v bytes=%d placed=%t", err, socket.Len(), connector.PlacedOrderID(150))
	}
}

func TestReportCannotCrossEvidenceCommit(t *testing.T) {
	conn, connector, _, _ := inheritedStopFixture(t)
	before, _ := conn.ReportedOwnOrder(40)
	entered, done := make(chan struct{}), make(chan struct{})
	conn.messageAfterInitialEpochCheck = func(id int) {
		if id == msgOpenOrder {
			close(entered)
		}
	}
	other := inheritedStopCallback(100, false)
	other.PermID, other.OrderRef = 700999, "ord-other"
	connector.evidenceBarrier.Lock()
	go func() {
		conn.processMessage(encodeOpenOrderProtoCallbackForTest(other))
		close(done)
	}()
	<-entered
	select {
	case <-done:
		connector.evidenceBarrier.Unlock()
		t.Fatal("report mutation crossed a protected evidence interval")
	case <-time.After(25 * time.Millisecond):
	}
	during, _ := conn.ReportedOwnOrder(40)
	connector.evidenceBarrier.Unlock()
	<-done
	after, _ := conn.ReportedOwnOrder(40)
	if during != before || !after.Conflicted {
		t.Fatalf("before=%+v during=%+v after=%+v", before, during, after)
	}
}

func TestStatusFirstDoesNotGrantOrEraseIdentity(t *testing.T) {
	for _, statusPermID := range []int{700000, 700999} {
		t.Run(map[int]string{700000: "matching_control", 700999: "contradiction"}[statusPermID], func(t *testing.T) {
			conn, connector, socket, gate := inheritedStopFixture(t)
			connector.ensureHandlersRegistered(conn)
			conn.resetOrderIDReadiness()
			conn.observeNextValidOrderID(100)
			binding, _ := connector.CaptureSession()
			body := protoAppendInt32(nil, 1, 40)
			body = protoAppendString(body, 2, "Submitted")
			body = protoAppendString(body, 3, "0")
			body = protoAppendString(body, 4, "100")
			body = protoAppendInt64(body, 6, int64(statusPermID))
			body = protoAppendInt32(body, 9, 41)
			conn.processMessage(encodeProtoCallbackFrameForTest(protoOrderStatusMsgID, body))
			contract, order := inheritedStopRawOrder(60, &OrderChangeIdentity{PermID: 700000, Account: "DU7654321", OrderRef: "ord-40"})
			resetSocket(socket)
			if err := connector.SubmitPaperOrderForSession(binding, gate, contract, order); !errors.Is(err, ErrBrokerIDNamespaceConflict) || socket.Len() != 0 {
				t.Fatalf("status alone granted ownership: err=%v bytes=%d", err, socket.Len())
			}
			conn.processMessage(encodeOpenOrderProtoCallbackForTest(inheritedStopCallback(100, false)))
			report, _ := conn.ReportedOwnOrder(40)
			resetSocket(socket)
			err := connector.SubmitPaperOrderForSession(binding, gate, contract, order)
			t.Logf("status PermID%d then openOrder700000: report=%+v err=%v bytes=%d", statusPermID, report, err, socket.Len())
			if statusPermID != 700000 {
				if !report.Conflicted || err == nil || socket.Len() != 0 {
					t.Fatal("status-first ordering erased an exact identity contradiction")
				}
			} else if err != nil || socket.Len() == 0 || report.Conflicted {
				t.Fatal("matching status then openOrder control did not send")
			}
		})
	}
}

func TestStatusScopeControls(t *testing.T) {
	for _, name := range []string{"matching", "other_client", "client_zero", "omitted_client", "zero_perm_id", "stale_epoch", "other_order_id"} {
		t.Run(name, func(t *testing.T) {
			conn, connector, socket, gate := inheritedStopFixture(t)
			connector.ensureHandlersRegistered(conn)
			binding, _ := connector.CaptureSession()
			before, _ := conn.ReportedOwnOrder(40)
			id, perm, client, epoch := 40, 700999, 41, conn.BrokerSessionEpoch()
			switch name {
			case "matching":
				perm = 700000
			case "other_client":
				client = 42
			case "client_zero":
				client = 0
			case "zero_perm_id":
				perm = 0
			case "stale_epoch":
				epoch--
			case "other_order_id":
				id = 41
			}
			body := protoAppendInt32(nil, 1, int32(id))
			body = protoAppendString(body, 2, "Submitted")
			body = protoAppendString(body, 3, "0")
			body = protoAppendString(body, 4, "100")
			body = protoAppendInt64(body, 6, int64(perm))
			if name != "omitted_client" {
				body = protoAppendInt32(body, 9, int32(client))
			}
			conn.processMessageAtEpoch(encodeProtoCallbackFrameForTest(protoOrderStatusMsgID, body), epoch)
			after, _ := conn.ReportedOwnOrder(40)
			if after != before {
				t.Fatalf("unrelated status changed identity: before=%+v after=%+v", before, after)
			}
			resetSocket(socket)
			contract, order := inheritedStopRawOrder(60, &OrderChangeIdentity{PermID: 700000, Account: "DU7654321", OrderRef: "ord-40"})
			if err := connector.SubmitPaperOrderForSession(binding, gate, contract, order); err != nil || socket.Len() == 0 {
				t.Fatalf("valid inherited modify was blocked: err=%v bytes=%d", err, socket.Len())
			}
		})
	}
}
