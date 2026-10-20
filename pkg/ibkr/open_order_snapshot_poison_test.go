package ibkr

import (
	"bufio"
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"
)

// A snapshot whose openOrderEnd misses its deadline poisons its socket
// generation; OpenOrderSnapshotPoisoned says so, with the time, for that
// generation only, so the daemon can redial and read open orders again.
func TestOpenOrderSnapshotPoisonedReportsTheCurrentSession(t *testing.T) {
	c := readyBrokerEvidenceTestConnector(t)
	if _, poisoned := c.OpenOrderSnapshotPoisoned(); poisoned {
		t.Fatal("a fresh session reads poisoned")
	}
	c.openOrderSnapshotTimeout = 50 * time.Millisecond
	c.requestAllOpenOrders = func() error { return nil } // the Gateway never answers
	before := time.Now()
	if _, err := c.SnapshotOpenOrders(t.Context()); !errors.Is(err, ErrOpenOrderSnapshotPoisoned) {
		t.Fatalf("unanswered snapshot err=%v, want poisoned", err)
	}
	since, poisoned := c.OpenOrderSnapshotPoisoned()
	if !poisoned || since.Before(before) || since.After(time.Now()) {
		t.Fatalf("poisoned=%t since=%s, want poisoned at the deadline", poisoned, since)
	}
	c.conn.resetOrderIDReadiness() // a new socket generation, as a redial makes
	if _, poisoned := c.OpenOrderSnapshotPoisoned(); poisoned {
		t.Fatal("the poison outlived its socket generation")
	}
}

// An execDetailsEnd that arrives after its flight gave up is the Gateway's
// late answer, not an unknown message. On 2026-10-20 one, during IBKR's
// nightly reset, logged "Unhandled message ID 55" at WARN. It is named, with
// how late it was, at WARN while the logging schedule wants attention and at
// INFO otherwise; an end no request ever waited for stays in the debug log.
func TestLateExecDetailsEndIsNamedNotUnhandled(t *testing.T) {
	for _, tc := range []struct {
		name     string
		onDuty   bool
		protobuf bool
		want     string
	}{
		{"legacy frame on duty", true, false, "level=WARN"},
		{"protobuf frame off duty", false, true, "level=INFO"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			buf := captureConnectorLogs(t)
			SetLogLevel("debug")
			t.Cleanup(func() { SetLogLevel("info") })
			c := readyBrokerEvidenceTestConnector(t)
			conn := c.conn
			setServerVersionReady(conn, maxClientVersion)
			c.SetBackendLogRequired(func(time.Time, time.Time) bool { return tc.onDuty })
			c.ensureHandlersRegistered(conn)
			conn.writer = bufio.NewWriterSize(&captureWriter{wrote: make(chan struct{}, 1)}, 64*1024)

			snap, err := c.FetchExecutions(context.Background(), "DU1234567", 50*time.Millisecond)
			if err != nil || snap.Complete {
				t.Fatalf("snapshot=%+v err=%v, want an incomplete answer at the deadline", snap, err)
			}
			end := func(reqID int) []byte {
				if tc.protobuf {
					return encodeProtoCallbackFrameForTest(protoExecDetailsEndMsgID, protoAppendInt32(nil, 1, int32(reqID)))
				}
				return conn.encodeMsg(msgExecDetailsEnd, "1", strconv.Itoa(reqID))
			}
			conn.processMessage(end(snap.RequestID))
			conn.processMessage(end(snap.RequestID + 1000))

			if lines := logLines(buf, "Unhandled message ID"); len(lines) != 0 {
				t.Fatalf("late end logged as unhandled: %q", lines)
			}
			late := logLines(buf, "answered executions request "+strconv.Itoa(snap.RequestID)+" ")
			if len(late) != 1 || !strings.Contains(late[0], tc.want) || !strings.Contains(late[0], "after its deadline") {
				t.Fatalf("late end lines %q, want one at %s", late, tc.want)
			}
			if lines := logLines(buf, "which no request is waiting for"); len(lines) != 1 || !strings.Contains(lines[0], "level=DEBUG") {
				t.Fatalf("unknown end lines %q, want one at DEBUG", lines)
			}
		})
	}
}

// A caller's cancellation is not the broker answering late, and an end
// later than executionsExpiredKeep is no longer attributed: both stay in the
// debug log.
func TestExecDetailsEndAfterCancelOrLongAfterStaysDebug(t *testing.T) {
	buf := captureConnectorLogs(t)
	SetLogLevel("debug")
	t.Cleanup(func() { SetLogLevel("info") })
	c := readyBrokerEvidenceTestConnector(t)
	conn := c.conn
	setServerVersionReady(conn, maxClientVersion)
	c.ensureHandlersRegistered(conn)
	conn.writer = bufio.NewWriterSize(&captureWriter{wrote: make(chan struct{}, 1)}, 64*1024)

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(20*time.Millisecond, cancel)
	cancelled, err := c.FetchExecutions(ctx, "DU1234567", time.Minute)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v, want the caller's cancellation", err)
	}
	conn.processMessage(conn.encodeMsg(msgExecDetailsEnd, "1", strconv.Itoa(cancelled.RequestID)))

	expired, _ := c.FetchExecutions(context.Background(), "DU1234567", 20*time.Millisecond)
	c.noteLateExecDetailsEnd([]string{"55", "1", strconv.Itoa(expired.RequestID)}, time.Now().Add(executionsExpiredKeep+time.Minute))

	if lines := logLines(buf, "answered executions request"); len(lines) != 0 {
		t.Fatalf("a cancelled or long-stale end was named late: %q", lines)
	}
	if lines := logLines(buf, "which no request is waiting for"); len(lines) != 2 {
		t.Fatalf("debug lines %q, want two", lines)
	}
}
