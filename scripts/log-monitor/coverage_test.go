package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testMonitorOptions(t *testing.T) options {
	t.Helper()
	dir := t.TempDir()
	opts := options{daemonLog: filepath.Join(dir, "daemon.log"), appLog: filepath.Join(dir, "app.log"), daemonOffset: filepath.Join(dir, "daemon.offset"), appOffset: filepath.Join(dir, "app.offset"), maxSignals: 10, commit: true}
	writeTestFile(t, opts.appLog, "level=INFO msg=ready\n")
	return opts
}

func TestRotationConsumesUnreadTailAndEntireReplacement(t *testing.T) {
	opts := testMonitorOptions(t)
	now := time.Now()
	writeTestFile(t, opts.daemonLog, "level=INFO msg=before\n")
	if _, err := run(opts, now); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, opts.daemonLog, "level=INFO msg=before\nlevel=ERROR msg=old-tail\n")
	if err := os.Rename(opts.daemonLog, opts.daemonLog+".1"); err != nil {
		t.Fatal(err)
	}
	// Longer than the saved offset: v1 silently skipped this ERROR.
	writeTestFile(t, opts.daemonLog, "level=ERROR msg=new-prefix\nlevel=INFO msg=after\nlevel=INFO msg=after\n")
	got, err := run(opts, now)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Daemon.OffsetReset || got.Daemon.NewLines != 4 || len(got.Daemon.Signals) != 2 {
		t.Fatalf("rotation lost evidence: %+v", got.Daemon)
	}
	again, err := run(opts, now)
	if err != nil || again.NeedsAttention {
		t.Fatalf("rotation replayed twice: %+v %v", again, err)
	}
}

// macOS gives the data volume a new device number at each boot. A cursor that
// differs only there names the same file, so the scan resumes at its
// checkpoint; on 2026-10-04 and 2026-10-05 a reboot replayed the whole week.
func TestRebootDeviceRenumberingKeepsTheCursor(t *testing.T) {
	opts := testMonitorOptions(t)
	now := time.Now()
	writeTestFile(t, opts.daemonLog, "level=INFO msg=before\nlevel=ERROR msg=already-reported\n")
	if _, err := run(opts, now); err != nil {
		t.Fatal(err)
	}
	c, err := readCursor(opts.daemonOffset)
	if err != nil {
		t.Fatal(err)
	}
	if c.Identity == "" {
		t.Skip("no file identity on this platform")
	}
	// The pre-boot device number, in the form cursors stored until 2026-10-05.
	c.Identity = "16777233:" + c.Identity
	if err := writeCursor(opts.daemonOffset, c); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, opts.daemonLog, "level=INFO msg=before\nlevel=ERROR msg=already-reported\nlevel=ERROR msg=new\n")
	got, err := run(opts, now)
	if err != nil {
		t.Fatal(err)
	}
	if got.Daemon.OffsetReset || got.Daemon.NewLines != 1 || len(got.Daemon.Signals) != 1 {
		t.Fatalf("device renumbering replayed the log: %+v", got.Daemon)
	}
}

func TestTruncationMigrationAndPartialRecords(t *testing.T) {
	opts := testMonitorOptions(t)
	now := time.Now()
	writeTestFile(t, opts.daemonLog, "level=INFO msg=before\nlevel=ERROR msg=unfinished")
	writeTestFile(t, opts.daemonOffset, "10\n")
	opts.commit = false
	before, _ := os.ReadFile(opts.daemonOffset)
	got, err := run(opts, now)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(opts.daemonOffset)
	if string(before) != string(after) || got.Daemon.NewLines != 1 || !got.Daemon.OffsetReset {
		t.Fatalf("migration dry run: %+v", got.Daemon)
	}
	opts.commit = true
	if _, err := run(opts, now); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(opts.daemonLog, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString("-complete\n")
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	got, err = run(opts, now)
	if err != nil || got.Daemon.NewLines != 1 || len(got.Daemon.Signals) != 1 || got.Daemon.Signals[0].Message != "unfinished-complete" {
		t.Fatalf("partial line lost: %+v %v", got.Daemon, err)
	}
	// Truncate/regrow the same inode to more lines than the prior offset.
	writeTestFile(t, opts.daemonLog, "level=ERROR msg=replaced\nlevel=INFO msg=x\nlevel=INFO msg=y\n")
	got, err = run(opts, now)
	if err != nil || !got.Daemon.OffsetReset || !got.NeedsAttention || got.Daemon.NewLines != 3 {
		t.Fatalf("truncate loss: %+v %v", got, err)
	}
}

// A broker failure recurring every day is never benign, but it needs the
// owner once: the first day it is new, the next days it is carried, and after
// standing-days at an unchanged rate it is a standing condition. Doubling
// brings it back.
func TestRecurringBrokerFailuresAreCarriedThenStanding(t *testing.T) {
	opts := testMonitorOptions(t)
	start := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	var lines strings.Builder
	for day := range 20 {
		ts := start.AddDate(0, 0, day)
		lines.WriteString("time=" + ts.Format(time.RFC3339) + " level=WARN msg=denied code=354\n")
		writeTestFile(t, opts.daemonLog, lines.String())
		got, err := run(opts, ts)
		if err != nil {
			t.Fatal(err)
		}
		if got.Daemon.KnownBenign != 0 || got.Daemon.Families["broker_code_354"] != 1 {
			t.Fatalf("access failure hidden: %+v", got.Daemon)
		}
		switch {
		case day == 0:
			if !got.NeedsAttention || got.Attention[0].Reason != "new_family" || got.Daemon.Signals[0].Status != "new" {
				t.Fatalf("day 0 = %+v", got)
			}
		case day < 7:
			if got.NeedsAttention || got.Daemon.Signals[0].Status != "carried" || got.Daemon.Signals[0].DaysSeen != day+1 {
				t.Fatalf("day %d = %+v", day, got.Daemon.Signals)
			}
		default:
			if got.NeedsAttention || len(got.Daemon.Signals) != 0 || len(got.Daemon.Standing) != 1 || got.Daemon.Standing[0].Days != day {
				t.Fatalf("day %d standing = %+v %+v", day, got.Daemon.Standing, got.Daemon.Signals)
			}
		}
	}
	// The rate quadruples: no longer standing, and grown since the last run.
	ts := start.AddDate(0, 0, 20)
	for range 4 {
		lines.WriteString("time=" + ts.Format(time.RFC3339) + " level=WARN msg=denied code=354\n")
	}
	writeTestFile(t, opts.daemonLog, lines.String())
	got, err := run(opts, ts)
	if err != nil || !got.NeedsAttention || got.Attention[0].Reason != "grew" {
		t.Fatalf("quadrupled rate = %+v %v", got, err)
	}
	for _, path := range []string{opts.daemonOffset, opts.openItemsPath()} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "denied") {
			t.Fatalf("raw message persisted in %s", path)
		}
		if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode = %v", path, info.Mode())
		}
	}
	raw, err := os.ReadFile(opts.daemonOffset)
	if err != nil {
		t.Fatal(err)
	}
	var cursor logCursor
	if err := json.Unmarshal(raw, &cursor); err != nil {
		t.Fatal(err)
	}
	if len(cursor.Days) != 21 {
		t.Fatal("days not retained")
	}
}

func TestCoverageAndSeverityCannotBeBenign(t *testing.T) {
	if got := defaultAppLog("/synthetic", "darwin"); got != "/synthetic/Library/Logs/ibkr/app.err.log" {
		t.Fatalf("wrong launchd source: %s", got)
	}
	opts := testMonitorOptions(t)
	writeTestFile(t, opts.daemonLog, "level=FATAL msg=terminated code=354\n")
	opts.staleAfter = 24 * time.Hour
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(opts.appLog, old, old); err != nil {
		t.Fatal(err)
	}
	got, err := run(opts, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !got.NeedsAttention || got.Daemon.Signals[0].Severity != "FATAL" || len(got.App.Signals) != 1 || got.App.Signals[0].Kind != "log_coverage" {
		t.Fatalf("coverage/severity hidden: %+v", got)
	}
}

func TestBenignThresholdUsesCalendarDaysAcrossWindows(t *testing.T) {
	opts := testMonitorOptions(t)
	ts := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	line := "time=" + ts.Format(time.RFC3339) + " level=WARN msg=indicative code=2129\n"
	writeTestFile(t, opts.daemonLog, strings.Repeat(line, 100))
	first, err := run(opts, ts)
	if err != nil || first.NeedsAttention {
		t.Fatalf("first window: %+v %v", first, err)
	}
	writeTestFile(t, opts.daemonLog, strings.Repeat(line, 151))
	second, err := run(opts, ts)
	if err != nil || !second.NeedsAttention || second.Daemon.Signals[0].Count != 151 {
		t.Fatalf("split-day threshold hidden: %+v %v", second, err)
	}
}

func TestRestartDoesNotHideActualPacingAndOldNoiseDaysStayQuiet(t *testing.T) {
	opts := testMonitorOptions(t)
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	writeTestFile(t, opts.daemonLog, "time="+now.Format(time.RFC3339)+" level=INFO msg=\"Connected to IB Gateway\"\n"+
		"time="+now.Format(time.RFC3339)+" level=WARN msg=\"[IBKR RateLimiter] Circuit breaker open\"\n")
	got, err := run(opts, now)
	if err != nil || !got.NeedsAttention || got.Daemon.KnownBenign != 0 {
		t.Fatalf("pacing hidden near start: %+v %v", got, err)
	}
	line := "time=" + now.Format(time.RFC3339) + " level=WARN msg=indicative code=2129\n"
	writeTestFile(t, opts.daemonLog, strings.Repeat(line, 151))
	if _, err := run(opts, now); err != nil {
		t.Fatal(err)
	}
	next := now.AddDate(0, 0, 1)
	writeTestFile(t, opts.daemonLog, strings.Repeat(line, 151)+"time="+next.Format(time.RFC3339)+" level=WARN msg=indicative code=2129\n")
	got, err = run(opts, next)
	if err != nil || got.NeedsAttention {
		t.Fatalf("old volume paged again: %+v %v", got, err)
	}
}

func TestFatalCannotBeHiddenByLifecycleOrSuccessfulAccessText(t *testing.T) {
	for _, line := range []string{"level=FATAL msg=\"Connected to IB Gateway\"", "level=FATAL msg=\"canary app serving\"", "level=FATAL msg=\"Request completed\" status=200"} {
		for _, daemon := range []bool{true, false} {
			got := finalizeForTest(scannedLog{state: "scanned", lines: []string{line}}, 10, daemon)
			if len(got.Signals) != 1 || got.Signals[0].Severity != "FATAL" || got.Signals[0].Attention != "error" {
				t.Fatalf("fatal hidden: %+v", got)
			}
		}
	}
}

func TestMirrorSyncRecordGatesCoverage(t *testing.T) {
	now := time.Date(2026, 9, 28, 6, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, record, state, message string
	}{
		{"local", "-", "", ""},
		{"absent", "", "missing", "log mirror has no sync record"},
		{"garbage", "{", "invalid", "sync record is unreadable"},
		{"unknown version", `{"version":2,"last_success":"2026-09-28T05:55:00Z"}`, "invalid", "sync record is unreadable"},
		{"never synced", `{"version":1,"last_success":null}`, "never_synced", "has not completed a sync"},
		{"stale", `{"version":1,"last_success":"2026-09-28T04:58:00Z"}`, "stale", "last synced 1h2m ago"},
		{"current", `{"version":1,"last_success":"2026-09-28T05:55:00Z","files":{}}`, "current", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := testMonitorOptions(t)
			opts.mirrorStaleAfter = 20 * time.Minute
			writeTestFile(t, opts.daemonLog, "level=INFO msg=ready\n")
			if tc.record != "-" {
				opts.daemonMirror = filepath.Join(t.TempDir(), "mirror.json")
			}
			if tc.record != "" && tc.record != "-" {
				writeTestFile(t, opts.daemonMirror, tc.record)
			}
			got, err := run(opts, now)
			if err != nil {
				t.Fatal(err)
			}
			if tc.state == "" {
				if got.Daemon.Mirror != nil {
					t.Fatalf("local log reported a mirror: %+v", got.Daemon.Mirror)
				}
			} else if got.Daemon.Mirror == nil || got.Daemon.Mirror.State != tc.state {
				t.Fatalf("mirror state = %+v, want %s", got.Daemon.Mirror, tc.state)
			}
			var coverage []string
			for _, s := range got.Daemon.Signals {
				if s.Kind == "log_coverage" {
					coverage = append(coverage, s.Message)
				}
			}
			if tc.message == "" {
				if got.NeedsAttention || len(coverage) != 0 {
					t.Fatalf("current mirror needs attention: %+v", got.Daemon)
				}
				return
			}
			if !got.NeedsAttention || len(coverage) != 1 || !strings.Contains(coverage[0], tc.message) {
				t.Fatalf("coverage = %q, want %q", coverage, tc.message)
			}
		})
	}
}

// Replaying a file after a cursor reset rebuilds the replayed days' family
// counts; it must not add them on top of the counts the lost cursor had.
func TestReplayAfterCursorResetDoesNotDoubleCountFamilies(t *testing.T) {
	opts := testMonitorOptions(t)
	opts.commit = true
	now := time.Date(2026, 10, 4, 6, 0, 0, 0, time.UTC)
	var lines strings.Builder
	for i := range 5 {
		ts := time.Date(2026, 10, 2, 7, i, 0, 0, time.UTC)
		lines.WriteString("time=" + ts.Format(time.RFC3339) + ` level=WARN msg="System notice reqID=1 (SPY OPT) code=300 @ x: Can't find EId"` + "\n")
	}
	writeTestFile(t, opts.daemonLog, lines.String())
	if _, err := run(opts, now); err != nil {
		t.Fatal(err)
	}
	if days := cursorDays(t, opts.daemonOffset); len(days) != 1 || days["2026-10-02"]["broker_code_300"] != 5 {
		t.Fatalf("first pass days = %v", days)
	}
	// Invalidate the cursor by rewriting the same lines with one more day.
	lines.WriteString("time=2026-10-03T07:00:00Z level=WARN msg=\"System notice reqID=2 (SPY OPT) code=300 @ x: Can't find EId\"\n")
	writeTestFile(t, opts.daemonLog, "time=2026-10-02T06:59:00Z level=INFO msg=prefix-changed\n"+lines.String())
	got, err := run(opts, now)
	if err != nil || !got.Daemon.OffsetReset {
		t.Fatalf("replay not detected: %+v %v", got.Daemon, err)
	}
	if days := cursorDays(t, opts.daemonOffset); days["2026-10-02"]["broker_code_300"] != 5 || days["2026-10-03"]["broker_code_300"] != 1 {
		t.Fatalf("replayed counts = %v, want 5 and 1", days)
	}
}

func cursorDays(t *testing.T, path string) map[string]map[string]int {
	t.Helper()
	c, err := readCursor(path)
	if err != nil {
		t.Fatal(err)
	}
	return c.Days
}
