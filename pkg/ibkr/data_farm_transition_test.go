package ibkr

import (
	"strings"
	"testing"
	"time"
)

func warnLines(buf *safeBuffer, substr string) []string {
	var out []string
	for _, line := range logLines(buf, substr) {
		if strings.Contains(line, "level=WARN") {
			out = append(out, line)
		}
	}
	return out
}

// Farm notices log at INFO, below the WARN-only production log. A short flap
// leaves no WARN at all; a break that lasts dataFarmBreakAttention warns
// once while broken and once, with the duration, when it recovers.
func TestDataFarmBreaksWarnOnlyWhenTheyLast(t *testing.T) {
	buf := captureConnectorLogs(t)
	c := &Connector{config: &ConnectorConfig{PreferredClientID: 15}}
	t0 := time.Date(2026, 9, 28, 4, 26, 0, 0, time.UTC)

	c.recordDataFarmNotice(2104, "Market data farm connection is OK:usfarm", t0)
	c.recordDataFarmNotice(2106, "HMDS data farm connection is OK:ushmds", t0)
	c.recordDataFarmNotice(2107, "HMDS data farm connection is inactive but should be available upon demand.fundfarm", t0)
	if lines := warnLines(buf, "data farm"); len(lines) != 0 {
		t.Fatalf("routine farm notices warned: %q", lines)
	}

	// A two-minute flap: break, repeat, recover. Transitions are INFO only.
	c.recordDataFarmNotice(2103, "Market data farm connection is broken:usfarm", t0.Add(time.Minute))
	c.recordDataFarmNotice(2103, "Market data farm connection is broken:usfarm", t0.Add(2*time.Minute))
	c.recordDataFarmNotice(2104, "Market data farm connection is OK:usfarm", t0.Add(3*time.Minute))
	if lines := warnLines(buf, "usfarm"); len(lines) != 0 {
		t.Fatalf("short flap warned: %q", lines)
	}
	if lines := logLines(buf, "market data farm usfarm is disconnected (code 2103)"); len(lines) != 1 || !strings.Contains(lines[0], "level=INFO") {
		t.Fatalf("break transition = %q, want one INFO line", lines)
	}

	// A break that lasts: the WARN lands on the next farm notice once five
	// minutes have passed, not before, and exactly once.
	c.recordDataFarmNotice(2105, "HMDS data farm connection is broken:ushmds", t0.Add(10*time.Minute))
	c.recordDataFarmNotice(2105, "HMDS data farm connection is broken:ushmds", t0.Add(12*time.Minute))
	if lines := warnLines(buf, "ushmds"); len(lines) != 0 {
		t.Fatalf("break warned before it lasted: %q", lines)
	}
	c.recordDataFarmNotice(2104, "Market data farm connection is OK:usfarm", t0.Add(15*time.Minute))
	c.recordDataFarmNotice(2104, "Market data farm connection is OK:usfarm", t0.Add(16*time.Minute))
	if lines := warnLines(buf, "historical data farm ushmds has been disconnected for 5m0s"); len(lines) != 1 {
		t.Fatalf("lasting break warnings = %q, want exactly one", lines)
	}
	c.recordDataFarmNotice(2106, "HMDS data farm connection is OK:ushmds", t0.Add(17*time.Minute))
	c.recordDataFarmNotice(2106, "HMDS data farm connection is OK:ushmds", t0.Add(18*time.Minute))
	recovered := warnLines(buf, "historical data farm ushmds recovered")
	if len(recovered) != 1 || !strings.Contains(recovered[0], "after 7m0s") {
		t.Fatalf("recovery warnings = %q, want one after 7m0s", recovered)
	}

	// The scheduled-mode worker's minute check notices a lone long break
	// without waiting for another farm notice.
	c.recordDataFarmNotice(2157, "Sec-def data farm connection is broken:secdefil", t0.Add(20*time.Minute))
	c.CheckBackendLogRelevance(t0.Add(24 * time.Minute))
	if lines := warnLines(buf, "secdefil"); len(lines) != 0 {
		t.Fatalf("sec-def break warned before it lasted: %q", lines)
	}
	c.CheckBackendLogRelevance(t0.Add(25 * time.Minute))
	c.CheckBackendLogRelevance(t0.Add(26 * time.Minute))
	if lines := warnLines(buf, "security definition data farm secdefil has been disconnected for"); len(lines) != 1 {
		t.Fatalf("sec-def break warnings = %q, want one", lines)
	}
}

// A recovery after a long break warns even when nothing checked the break
// while it lasted: the duration alone earns the bookend.
func TestDataFarmLongBreakRecoveryWarnsUnchecked(t *testing.T) {
	buf := captureConnectorLogs(t)
	c := &Connector{config: &ConnectorConfig{PreferredClientID: 15}}
	t0 := time.Date(2026, 10, 4, 4, 26, 0, 0, time.UTC)
	c.recordDataFarmNotice(2105, "HMDS data farm connection is broken:ushmds", t0)
	c.recordDataFarmNotice(2106, "HMDS data farm connection is OK:ushmds", t0.Add(37*time.Minute))
	lines := warnLines(buf, "ushmds")
	if len(lines) != 1 || !strings.Contains(lines[0], "recovered") || !strings.Contains(lines[0], "after 37m0s") {
		t.Fatalf("long break recovery = %q, want one recovery WARN after 37m0s", lines)
	}
}

// 2110 had no WARN of its own; 1100/1101/1102 warn through the backend link
// tracker and must not gain a second line here.
func TestConnectivityBreakWarnsOnlyForCode2110(t *testing.T) {
	buf := captureConnectorLogs(t)
	c := &Connector{config: &ConnectorConfig{PreferredClientID: 15}}
	t0 := time.Date(2026, 9, 28, 4, 26, 0, 0, time.UTC)

	c.recordDataFarmNotice(1100, "Connectivity between IBKR and Trader Workstation has been lost.", t0)
	c.recordDataFarmNotice(1102, "Connectivity between IBKR and Trader Workstation has been restored - data maintained.", t0.Add(time.Minute))
	if lines := warnLines(buf, "IBKR"); len(lines) != 0 {
		t.Fatalf("1100/1102 warned twice: %q", lines)
	}
	c.recordDataFarmNotice(2110, "Connectivity between Trader Workstation and server is broken. It will be restored automatically.", t0.Add(2*time.Minute))
	c.recordDataFarmNotice(2110, "Connectivity between Trader Workstation and server is broken. It will be restored automatically.", t0.Add(3*time.Minute))
	if lines := warnLines(buf, "code 2110"); len(lines) != 1 {
		t.Fatalf("2110 warnings = %q, want one", lines)
	}
	// A connectivity break never takes the farm-break path, however long.
	c.CheckBackendLogRelevance(t0.Add(30 * time.Minute))
	if lines := warnLines(buf, "has been"); len(lines) != 0 {
		t.Fatalf("connectivity break took the farm path: %q", lines)
	}
}

// Two lanes on one TWS receive every farm notice; with a shared transition
// log the break, its lasting-break WARN and its recovery are written once,
// and the next break of the same farm is a new transition.
// Owner instruction (Oliver, in chat, 2026-10-13 09:04 CEST): dependents of
// an owned outage log through the owner.
func TestDataFarmTransitionsLogOncePerTWS(t *testing.T) {
	buf := captureConnectorLogs(t)
	shared := &FarmTransitionLog{}
	primary := &Connector{config: &ConnectorConfig{PreferredClientID: 15}}
	bulk := &Connector{config: &ConnectorConfig{PreferredClientID: 16}}
	primary.SetFarmTransitionLog(shared)
	bulk.SetFarmTransitionLog(shared)
	t0 := time.Date(2026, 10, 12, 4, 26, 0, 0, time.UTC)
	both := func(code int, msg string, at time.Time) {
		primary.recordDataFarmNotice(code, msg, at)
		bulk.recordDataFarmNotice(code, msg, at)
	}

	both(2104, "Market data farm connection is OK:synfarm", t0)
	both(2103, "Market data farm connection is broken:synfarm", t0.Add(time.Minute))
	if lines := logLines(buf, "synfarm is disconnected"); len(lines) != 1 {
		t.Fatalf("break lines = %q, want one", lines)
	}
	both(2103, "Market data farm connection is broken:synfarm", t0.Add(7*time.Minute))
	if lines := warnLines(buf, "synfarm has been disconnected for"); len(lines) != 1 {
		t.Fatalf("lasting break warnings = %q, want one", lines)
	}
	both(2104, "Market data farm connection is OK:synfarm", t0.Add(9*time.Minute))
	if lines := warnLines(buf, "synfarm recovered"); len(lines) != 1 {
		t.Fatalf("recovery warnings = %q, want one", lines)
	}

	both(2103, "Market data farm connection is broken:synfarm", t0.Add(20*time.Minute))
	if lines := logLines(buf, "synfarm is disconnected"); len(lines) != 2 {
		t.Fatalf("a new break after the recovery = %q, want a second line", lines)
	}

	// A connectivity break clears through 1102 without a recovery line; the
	// next 2110 is a new break.
	const broken2110 = "Connectivity between Trader Workstation and server is broken. It will be restored automatically."
	both(2110, broken2110, t0.Add(30*time.Minute))
	both(1102, "Connectivity between IB and Trader Workstation has been restored - data maintained.", t0.Add(31*time.Minute))
	both(2110, broken2110, t0.Add(32*time.Minute))
	if lines := warnLines(buf, "connection to IBKR broken (code 2110)"); len(lines) != 2 {
		t.Fatalf("2110 warnings = %q, want one per break", lines)
	}
}
