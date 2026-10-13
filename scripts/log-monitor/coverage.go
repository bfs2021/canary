package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

func defaultAppLog(home, goos string) string {
	if goos == "darwin" {
		return filepath.Join(home, "Library", "Logs", "ibkr", "app.err.log")
	}
	return filepath.Join(home, ".local", "state", "ibkr", "ibkr-app.log")
}

// applyCoverage reports a log that could not be fully read as a coverage
// family. Each family carries across runs like any other: the first run that
// finds an app log stale needs the owner; the same stale log the next day is
// carried, not new, and a states-file declaration ("app:log_coverage_stale")
// records that the app was stopped on purpose.
func applyCoverage(a *analysis, scanned scannedLog, now time.Time, staleAfter time.Duration) {
	if scanned.state == "missing" {
		a.addItem("WARN", "log_coverage", "log_coverage_missing", "expected log is missing; service health is unverified", 1)
	}
	if scanned.coverage != "" {
		a.addItem("WARN", "log_coverage", "log_coverage_rotation", scanned.coverage, 1)
	}
	if scanned.state != "missing" && staleAfter > 0 && now.Sub(scanned.modified) > staleAfter {
		a.addItem("WARN", "log_coverage", "log_coverage_stale", "log is inactive beyond the configured coverage interval; service health is unverified", 1)
	}
}

// mirrorRecord is the sync record a log mirror keeps beside its copies. Only
// the last completed sync matters here: an unchanged copy is neutral while the
// mirror is current, and no evidence at all once it falls behind.
type mirrorRecord struct {
	Version     int       `json:"version"`
	LastSuccess time.Time `json:"last_success"`
}

type mirrorReport struct {
	State       string    `json:"state"`
	LastSuccess time.Time `json:"last_success,omitzero"`
}

func applyMirrorCoverage(a *analysis, path string, now time.Time, staleAfter time.Duration) {
	report, message := readMirror(path, now, staleAfter)
	a.report.Mirror = &report
	if message != "" {
		a.addItem("WARN", "log_coverage", "log_coverage_mirror", message, 1)
	}
}

func readMirror(path string, now time.Time, staleAfter time.Duration) (mirrorReport, string) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return mirrorReport{State: "missing"}, "log mirror has no sync record; service health is unverified"
	}
	var record mirrorRecord
	if err != nil || json.Unmarshal(raw, &record) != nil || record.Version != 1 {
		return mirrorReport{State: "invalid"}, "log mirror sync record is unreadable; service health is unverified"
	}
	if record.LastSuccess.IsZero() {
		return mirrorReport{State: "never_synced"}, "log mirror has not completed a sync; service health is unverified"
	}
	report := mirrorReport{State: "current", LastSuccess: record.LastSuccess}
	if age := now.Sub(record.LastSuccess); staleAfter > 0 && age > staleAfter {
		report.State = "stale"
		elapsed := strings.TrimSuffix(age.Round(time.Minute).String(), "0s")
		return report, "log mirror last synced " + elapsed + " ago; service health is unverified"
	}
	return report, ""
}

// maxFamiliesPerDay bounds the recurrence history a cursor retains.
const maxFamiliesPerDay = 200

// trackFamilies keeps 28 UTC date buckets of family counts — the closed
// broker notice families and every WARN/ERROR family the scan classified —
// never raw log content. It returns the history as it stood before this scan,
// which tells a new family from one seen before and a standing condition from
// one whose rate changed. Short restart windows also span scans here.
func trackFamilies(a *analysis, scanned *scannedLog, now time.Time) history {
	if scanned.cursor.Days == nil {
		scanned.cursor.Days = map[string]map[string]int{}
	}
	cutoff := now.UTC().AddDate(0, 0, -27).Format("2006-01-02")
	today := now.UTC().Format("2006-01-02")
	changed := map[string]bool{}
	starts := scanned.cursor.Starts
	// A cursor reset replays lines the previous cursor had already counted.
	// Adding them again doubled every replayed day (2026-10-02 showed 3,380
	// code-300 notices for 1,690 lines); rebuild those days from the replay
	// instead so a day's bucket always reflects one pass over its lines.
	if scanned.offsetReset {
		for _, line := range scanned.lines {
			if ts, ok := logTime(line); ok && !ts.After(now) {
				delete(scanned.cursor.Days, ts.UTC().Format("2006-01-02"))
			}
		}
	}
	prior := history{}
	for day, counts := range scanned.cursor.Days {
		if day < cutoff || day > today {
			continue
		}
		for family, n := range counts {
			if prior[family] == nil {
				prior[family] = map[string]int{}
			}
			prior[family][day] = n
		}
	}
	count := func(ts time.Time, family string) {
		day := ts.UTC().Format("2006-01-02")
		if day < cutoff {
			return
		}
		bucket := scanned.cursor.Days[day]
		if bucket == nil {
			bucket = map[string]int{}
			scanned.cursor.Days[day] = bucket
		}
		if _, ok := bucket[family]; !ok && len(bucket) >= maxFamiliesPerDay {
			return
		}
		bucket[family]++
		changed[day+"\x00"+family] = true
	}
	for _, line := range scanned.lines {
		ts, ok := logTime(line)
		if !ok || ts.After(now) {
			continue
		}
		if (isDaemonStart(line) || isAppStart(line)) && ts.After(now.Add(-restartLoopWindow)) {
			starts = append(starts, ts)
		}
		if family := daemonNoticeFamily(line); family != "" {
			count(ts, family)
		}
	}
	for _, e := range a.entries {
		if e.timed && !e.ts.After(now) && e.kind != "broker_read_notice" {
			count(e.ts, e.family)
		}
	}
	for day, counts := range scanned.cursor.Days {
		if day < cutoff || day > today {
			delete(scanned.cursor.Days, day)
			continue
		}
		if n := counts["broker_code_2129_indicative"]; changed[day+"\x00broker_code_2129_indicative"] && n > 150 {
			a.addItem("WARN", "noise_loop", "noise_loop", fmt.Sprintf("indicative-data notices exceeded 150 occurrences on %s", day), n)
		}
	}
	starts = slices.DeleteFunc(starts, func(ts time.Time) bool { return ts.Before(now.Add(-restartLoopWindow)) || ts.After(now) })
	slices.SortFunc(starts, func(a, b time.Time) int { return a.Compare(b) })
	if len(scanned.lines) > 0 && restartLoopCount(starts) > restartLoopStarts {
		if _, found := a.items.byFamily["restart_loop"]; !found {
			a.addItem("WARN", "restart_loop", "restart_loop", "service started repeatedly across scan windows within "+restartLoopWindow.String(), len(starts))
		}
	}
	if len(starts) > 128 {
		starts = starts[len(starts)-128:]
	}
	scanned.cursor.Starts = starts
	return prior
}
