package ibkr

import (
	"bufio"
	"context"
	"strconv"
	"testing"
	"time"
)

// IBKR allows a client two account-summary subscriptions. A cancel that fails
// while its socket stays up leaves one open at IBKR; two such failures refused
// every later read with code 322 until the socket ended (2026-10-21,
// 2026-10-22 and 2026-10-27, each after a Gateway stall). The next read on the
// same session must cancel the stranded subscription before it subscribes.
func TestAccountSummaryNextReadCancelsStrandedSubscription(t *testing.T) {
	c, conn, wire := settlementConnectorFixture(t)
	conn.writer = bufio.NewWriter(&settlementCancelFailWriter{wire: wire})
	read := func() <-chan error {
		done := make(chan error, 1)
		go func() {
			_, _, err := c.RequestAccountSummaryWithProvenance(context.Background(), time.Second)
			done <- err
		}()
		return done
	}
	answer := func(id int) {
		probeCallback(conn, id, "DU1234567", "NetLiquidation", "12345", "EUR")
		probeEnd(conn, id)
	}

	done := read()
	frames := probeFrames(t, wire, conn, 1)
	stranded, _ := strconv.Atoi(frames[0][2])
	answer(stranded)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := wire.frames(conn); len(got) != 1 {
		t.Fatalf("cancel reached the wire through a failing transport: %v", got)
	}

	conn.transportMu.Lock()
	conn.writer = bufio.NewWriter(wire)
	conn.transportMu.Unlock()
	done = read()
	frames = probeFrames(t, wire, conn, 2)
	if frames[1][0] != "63" || frames[1][2] != strconv.Itoa(stranded) {
		t.Fatalf("next read subscribed without cancelling stranded reqID %d: %v", stranded, frames)
	}
	frames = probeFrames(t, wire, conn, 3)
	if frames[2][0] != "62" {
		t.Fatalf("next read did not subscribe after the cancel: %v", frames)
	}
	next, _ := strconv.Atoi(frames[2][2])
	answer(next)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	frames = probeFrames(t, wire, conn, 4)
	if frames[3][0] != "63" || frames[3][2] != strconv.Itoa(next) {
		t.Fatalf("second read did not cancel its own subscription: %v", frames)
	}
	conn.summarySchedule.mu.Lock()
	leases := len(conn.summarySchedule.ordinary)
	conn.summarySchedule.mu.Unlock()
	if leases != 0 {
		t.Fatalf("%d subscription leases left after both cancels", leases)
	}
}
