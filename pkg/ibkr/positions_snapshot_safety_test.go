package ibkr

import (
	"bufio"
	"context"
	"errors"
	"strconv"
	"testing"
	"time"
)

type positionsRefreshResult struct {
	rows []*RawPosition
	err  error
}

func positionsRefreshFixture(t *testing.T, version int) (*Connector, *Connection, *safeBuffer, ConnectorSessionBinding) {
	t.Helper()
	conn, wire := newReadyWireTestConnection(t)
	setServerVersionReady(conn, version)
	conn.account = "DU_SYNTHETIC"
	connector := NewConnector(&ConnectorConfig{})
	connector.conn.rateLimiter.Stop()
	connector.conn, connector.ready = conn, true
	binding, ok := connector.CaptureSession()
	if !ok {
		t.Fatal("missing ready synthetic session")
	}
	return connector, conn, wire, binding
}

func startPositionsRefresh(c *Connector, ctx context.Context, binding ConnectorSessionBinding, budget time.Duration) <-chan positionsRefreshResult {
	answer := make(chan positionsRefreshResult, 1)
	go func() {
		rows, err := c.RefreshPositionsForSession(ctx, binding, budget)
		answer <- positionsRefreshResult{rows, err}
	}()
	return answer
}

func positionsFrames(t *testing.T, conn *Connection, wire *safeBuffer, count int) [][]string {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		frames := decodeOutboundFrames(t, conn, wire.Bytes())
		if len(frames) >= count {
			return frames
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("missing positions frame %d", count)
	return nil
}

func positionsRequestID(t *testing.T, fields []string) int {
	t.Helper()
	if len(fields) != 6 || fields[0] != "74" || fields[1] != "1" || fields[3] != "DU_SYNTHETIC" || fields[4] != "" {
		t.Fatalf("unexpected reqPositionsMulti frame: %+v", fields)
	}
	id, err := strconv.Atoi(fields[2])
	if err != nil || id <= 0 {
		t.Fatalf("invalid positions request ID: %q", fields[2])
	}
	return id
}

func feedPositionsMulti(conn *Connection, id int, account, model, quantity string) {
	conn.processMessage(conn.encodeMsg(msgPositionMulti, 1, id, account, 202, "SYN", "OPT", "20261218", 30, "C", 100, "SMART", "USD", "SYN", "SYN", quantity, 250, model))
}

func positionsAnswer(t *testing.T, answer <-chan positionsRefreshResult) positionsRefreshResult {
	t.Helper()
	select {
	case got := <-answer:
		return got
	case <-time.After(time.Second):
		t.Fatal("position snapshot did not honor completion or cancellation")
		return positionsRefreshResult{}
	}
}

func TestRefreshPositionsCorrelatesRepeatedCompletedSnapshots(t *testing.T) {
	for _, version := range []int{102, 103, 202, maxClientVersion, 207} {
		t.Run(strconv.Itoa(version), func(t *testing.T) {
			c, conn, wire, binding := positionsRefreshFixture(t, version)
			if version < 103 || version >= 207 {
				if _, err := c.RefreshPositionsForSession(t.Context(), binding, time.Second); err == nil || wire.Len() != 0 {
					t.Fatal("unsupported position protocol sent a request")
				}
				return
			}
			first := startPositionsRefresh(c, t.Context(), binding, time.Second)
			id := positionsRequestID(t, positionsFrames(t, conn, wire, 1)[0])
			feedPositionsMulti(conn, id, "DU_SYNTHETIC", "", "5")
			conn.processMessage(conn.encodeMsg(msgPositionMultiEnd, 1, id))
			got := positionsAnswer(t, first)
			if got.err != nil || len(got.rows) != 1 || got.rows[0].Position != 5 || got.rows[0].Contract.ConID != 202 || got.rows[0].Account != "DU_SYNTHETIC" {
				t.Fatalf("first snapshot=%+v err=%v", got.rows, got.err)
			}
			frames := positionsFrames(t, conn, wire, 2)
			if frames[1][0] != "75" || frames[1][2] != strconv.Itoa(id) {
				t.Fatalf("missing correlated cancel: %+v", frames[1])
			}
			second := startPositionsRefresh(c, t.Context(), binding, time.Second)
			secondID := positionsRequestID(t, positionsFrames(t, conn, wire, 3)[2])
			if secondID == id {
				t.Fatal("request ID was reused")
			}
			feedPositionsMulti(conn, id, "DU_SYNTHETIC", "", "5")
			conn.processMessage(conn.encodeMsg(msgPositionMultiEnd, 1, id))
			select {
			case <-second:
				t.Fatal("retired subscription end certified the new request")
			default:
			}
			conn.processMessage(conn.encodeMsg(msgPositionMultiEnd, 1, secondID))
			got = positionsAnswer(t, second)
			if got.err != nil || len(got.rows) != 0 {
				t.Fatalf("old update contaminated empty fresh snapshot: %+v %v", got.rows, got.err)
			}
			if len(conn.GetPositions()) != 0 {
				t.Fatal("snapshot mutated streaming portfolio")
			}
		})
	}
}

func TestRefreshPositionsTimeoutCancelsAndIgnoresLateResponse(t *testing.T) {
	c, conn, wire, binding := positionsRefreshFixture(t, maxClientVersion)
	first := startPositionsRefresh(c, t.Context(), binding, 30*time.Millisecond)
	id := positionsRequestID(t, positionsFrames(t, conn, wire, 1)[0])
	if _, err := c.RefreshPositionsForSession(t.Context(), binding, time.Second); err == nil {
		t.Fatal("overlapping refresh was admitted")
	}
	if got := positionsAnswer(t, first); got.err == nil || got.rows != nil {
		t.Fatal("unfinished snapshot did not time out")
	}
	frames := positionsFrames(t, conn, wire, 2)
	if frames[1][0] != "75" || frames[1][2] != strconv.Itoa(id) {
		t.Fatal("timeout did not cancel its subscription")
	}
	second := startPositionsRefresh(c, t.Context(), binding, time.Second)
	secondID := positionsRequestID(t, positionsFrames(t, conn, wire, 3)[2])
	feedPositionsMulti(conn, id, "DU_SYNTHETIC", "", "5")
	conn.processMessage(conn.encodeMsg(msgPositionMultiEnd, 1, id))
	feedPositionsMulti(conn, secondID, "DU_SYNTHETIC", "", "2")
	conn.processMessage(conn.encodeMsg(msgPositionMultiEnd, 1, secondID))
	if got := positionsAnswer(t, second); got.err != nil || len(got.rows) != 1 || got.rows[0].Position != 2 {
		t.Fatalf("late prior response affected successor: %+v %v", got.rows, got.err)
	}
}

func TestRefreshPositionsRejectsInvalidEvidence(t *testing.T) {
	for _, mode := range []string{"foreign account", "model", "NaN", "malformed", "malformed end", "canceled", "session replaced", "epoch changed", "account changed"} {
		t.Run(mode, func(t *testing.T) {
			c, conn, wire, binding := positionsRefreshFixture(t, maxClientVersion)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			answer := startPositionsRefresh(c, ctx, binding, 50*time.Millisecond)
			id := positionsRequestID(t, positionsFrames(t, conn, wire, 1)[0])
			switch mode {
			case "foreign account":
				feedPositionsMulti(conn, id, "DU_OTHER", "", "5")
			case "model":
				feedPositionsMulti(conn, id, "DU_SYNTHETIC", "MODEL", "5")
			case "NaN":
				feedPositionsMulti(conn, id, "DU_SYNTHETIC", "", "NaN")
			case "malformed":
				conn.processMessage(conn.encodeMsg(msgPositionMulti, 1, id, "DU_SYNTHETIC"))
			case "malformed end":
				conn.processMessage(conn.encodeMsg(msgPositionMultiEnd, 99, id))
			case "canceled":
				cancel()
			case "session replaced":
				replacement, _ := newReadyWireTestConnection(t)
				c.mu.Lock()
				c.conn = replacement
				c.mu.Unlock()
			case "epoch changed":
				conn.resetOrderIDReadiness()
			case "account changed":
				conn.accountMu.Lock()
				conn.account = "DU_OTHER"
				conn.accountMu.Unlock()
			}
			conn.processMessageAtEpoch(conn.encodeMsg(msgPositionMultiEnd, 1, id), binding.epoch)
			if got := positionsAnswer(t, answer); got.err == nil || got.rows != nil {
				t.Fatalf("invalid evidence returned %+v %v", got.rows, got.err)
			}
		})
	}
}

func TestRefreshPositionsRejectsCanceledOrRetiredSessionBeforeSend(t *testing.T) {
	c, conn, wire, binding := positionsRefreshFixture(t, maxClientVersion)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := c.RefreshPositionsForSession(ctx, binding, time.Second); err == nil {
		t.Fatal("canceled caller started a refresh")
	}
	conn.brokerSessionEpoch.Add(1)
	if _, err := c.RefreshPositionsForSession(t.Context(), binding, time.Second); err == nil {
		t.Fatal("retired session started a refresh")
	}
	if wire.Len() != 0 {
		t.Fatal("invalid caller/session wrote a positions request")
	}
}

type positionsCancelFailure struct{}

func (positionsCancelFailure) Write([]byte) (int, error) {
	return 0, errors.New("synthetic cancellation write failure")
}

func TestRefreshPositionsUnconfirmedCancelBlocksFurtherSubscriptions(t *testing.T) {
	c, conn, wire, binding := positionsRefreshFixture(t, maxClientVersion)
	answer := startPositionsRefresh(c, t.Context(), binding, time.Second)
	id := positionsRequestID(t, positionsFrames(t, conn, wire, 1)[0])
	conn.writeMu.Lock()
	conn.writer = bufio.NewWriter(positionsCancelFailure{})
	conn.writeMu.Unlock()
	conn.processMessage(conn.encodeMsg(msgPositionMultiEnd, 1, id))
	if got := positionsAnswer(t, answer); got.err == nil || got.rows != nil {
		t.Fatal("failed cancellation returned a usable snapshot")
	}
	conn.writeMu.Lock()
	conn.writer = bufio.NewWriter(wire)
	conn.writeMu.Unlock()
	if _, err := c.RefreshPositionsForSession(t.Context(), binding, time.Second); err == nil {
		t.Fatal("new subscription admitted with prior cancellation unconfirmed")
	}
	if frames := decodeOutboundFrames(t, conn, wire.Bytes()); len(frames) != 1 {
		t.Fatalf("new subscription reached wire after uncertain cancel: %+v", frames)
	}
}
