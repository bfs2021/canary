package main

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// entry is one classified WARN/ERROR line that is not an anchor: a candidate
// signal until attribution assigns it to an incident, a restart or a run.
type entry struct {
	ts        time.Time
	timed     bool
	severity  string
	kind      string
	family    string
	subsystem string
	message   string
}

const maxSamplesPerFamily = 32

// familyAgg counts one family's lines.
type familyAgg struct {
	severity, kind, family, subsystem string
	count                             int
	samples                           map[string]int
	sampleOrder                       []string
	first, last                       time.Time
}

// collector merges entries by family, keeping a bounded set of distinct
// redacted samples per family.
type collector struct {
	byFamily map[string]*familyAgg
	order    []string
}

func newCollector() *collector { return &collector{byFamily: map[string]*familyAgg{}} }

func (c *collector) add(e entry, n int) {
	agg, ok := c.byFamily[e.family]
	if !ok {
		agg = &familyAgg{severity: e.severity, kind: e.kind, family: e.family, subsystem: e.subsystem, samples: map[string]int{}}
		c.byFamily[e.family] = agg
		c.order = append(c.order, e.family)
	}
	if severityRank(e.severity) > severityRank(agg.severity) {
		agg.severity, agg.kind = e.severity, e.kind
	}
	agg.count += max(n, 1)
	if _, seen := agg.samples[e.message]; seen || len(agg.samples) < maxSamplesPerFamily {
		if !seen {
			agg.sampleOrder = append(agg.sampleOrder, e.message)
		}
		agg.samples[e.message] += max(n, 1)
	}
	if e.timed {
		if agg.first.IsZero() || e.ts.Before(agg.first) {
			agg.first = e.ts
		}
		if e.ts.After(agg.last) {
			agg.last = e.ts
		}
	}
}

// topSamples returns up to n distinct samples, most frequent first.
func (a *familyAgg) topSamples(n int) []string {
	out := slices.Clone(a.sampleOrder)
	slices.SortStableFunc(out, func(x, y string) int { return a.samples[y] - a.samples[x] })
	if len(out) > n {
		out = out[:n]
	}
	return out
}

// dependents lists a collector's families, largest first, bounded.
func (c *collector) dependents() (out []dependent, lines int) {
	for _, family := range c.order {
		agg := c.byFamily[family]
		out = append(out, dependent{Family: family, Severity: agg.severity, Count: agg.count})
		lines += agg.count
	}
	slices.SortStableFunc(out, func(a, b dependent) int { return b.Count - a.Count })
	if len(out) > maxDependents {
		rest := 0
		for _, d := range out[maxDependents:] {
			rest += d.Count
		}
		out = append(out[:maxDependents:maxDependents], dependent{Family: "other", Severity: "WARN", Count: rest})
	}
	return out, lines
}

// analysis is one log's classification before the report is finalized.
type analysis struct {
	report     logReport
	entries    []entry // standalone candidates, in log order
	items      *collector
	clusters   []*cluster
	restarts   []*restartEvent
	runs       []*failureRun
	errorItems []entry
	lastBuild  string
}

func newAnalysis(scanned scannedLog) *analysis {
	return &analysis{report: newLogReport(scanned), items: newCollector()}
}

// addItem records a synthetic or app signal straight into the standalone
// items: coverage, crash output, loops.
func (a *analysis) addItem(severity, kind, family, message string, count int) {
	e := entry{severity: severity, kind: kind, family: family, subsystem: family, message: message}
	a.items.add(e, count)
	if severityRank(severity) == 2 {
		a.errorItems = append(a.errorItems, e)
	}
}

func lineEntry(line, severity, kind, family, message string) entry {
	ts, ok := logTime(line)
	return entry{ts: ts, timed: ok, severity: severity, kind: kind, family: family, subsystem: subsystemOf(family), message: message}
}

// classifyDaemon sorts daemon lines into anchors, lifecycle, benign counts
// and candidate entries, then clusters anchors into incidents and attributes
// entries to them. seed is an incident carried open from the previous run.
func classifyDaemon(scanned scannedLog, cfg engineConfig, seed *cluster, lastBuild string) *analysis {
	a := newAnalysis(scanned)
	result := &a.report
	restarts := lifecycleTimes(scanned.lines, isDaemonLifecycle)
	var anchors, starts, stops []anchor
	var candidates []entry
	for _, line := range scanned.lines {
		trimmed := strings.TrimSpace(line)
		level := severity(trimmed)
		ts, timed := logTime(trimmed)
		switch {
		case trimmed == "":
			result.Informational++
		case isPanicLine(trimmed):
			e := lineEntry(trimmed, "ERROR", "panic", "panic", "daemon panic detected")
			candidates = append(candidates, e)
		case level == "FATAL":
			msg := safeMessage(trimmed)
			candidates = append(candidates, lineEntry(trimmed, "FATAL", "log_level", genericFamily(extractSlogMessage(trimmed)), msg))
		case isStackContinuation(line):
			result.Informational++
		case isDaemonLifecycle(trimmed):
			result.Families["lifecycle"]++
			result.Informational++
			build := daemonBuild(trimmed)
			if build != "" && !slices.Contains(result.Builds, build) {
				result.Builds = append(result.Builds, build)
			}
			if !timed {
				continue
			}
			switch {
			case strings.Contains(trimmed, "canary daemon serving"):
				starts = append(starts, anchor{ts: ts, from: ts, role: roleRestart, build: build})
				anchors = append(anchors, anchor{ts: ts, from: ts, role: roleRestart})
			case strings.Contains(trimmed, "IBKR connector stopped"):
				stops = append(stops, anchor{ts: ts, from: ts})
			case isDaemonStart(trimmed):
				anchors = append(anchors, anchor{ts: ts, from: ts, role: roleClose, subject: subjectGateway})
			}
		case isFarmLine(trimmed):
			result.Families["farm_transition"]++
			role, _, _ := farmTransition(extractSlogMessage(trimmed))
			if role == "inactive" || (level != "WARN" && level != "ERROR") || !timed {
				result.KnownBenign++
				continue
			}
			if an, ok := parseAnchor(extractSlogMessage(trimmed), ts); ok {
				an.errored = level == "ERROR"
				anchors = append(anchors, an)
			}
		case (level == "WARN" || level == "ERROR") && timed && isAnchorLine(trimmed):
			an, _ := parseAnchor(extractSlogMessage(trimmed), ts)
			an.errored = level == "ERROR"
			result.Families[an.subject]++
			anchors = append(anchors, an)
		case daemonNoticeFamily(trimmed) != "":
			family := daemonNoticeFamily(trimmed)
			result.Families[family]++
			if level != "WARN" && level != "ERROR" {
				// The daemon already judged this notice routine (a known
				// entitlement gap, a verdict its requester records).
				result.Informational++
			} else if family == "broker_code_2129_indicative" && level != "ERROR" {
				result.KnownBenign++
			} else {
				message := "broker read notice requires outcome assessment: " + family
				if family == "broker_code_200_no_definition" || family == "broker_code_354" {
					// A definition failure or a refused market-data line is
					// about one contract; name it so a recurring miss can be
					// traced without the raw log. Owner instruction (Oliver,
					// 2026-10-12 06:40 CEST): code 354 names its contract too.
					message += " (" + noticeContractLabel(trimmed) + ")"
				}
				candidates = append(candidates, lineEntry(trimmed, level, "broker_read_notice", family, message))
			}
		case isRateLimiterWarning(trimmed) && nearRestart(trimmed, restarts):
			result.Families["shutdown_rate_limiter"]++
			result.KnownBenign++
		case level == "WARN" || level == "ERROR":
			candidates = append(candidates, lineEntry(trimmed, level, "log_level", genericFamily(extractSlogMessage(trimmed)), safeMessage(trimmed)))
		default:
			result.Informational++
		}
	}
	if count := restartLoopCount(lifecycleTimes(scanned.lines, isDaemonStart)); count > restartLoopStarts {
		a.addItem("WARN", "restart_loop", "restart_loop", "daemon connected repeatedly within "+restartLoopWindow.String(), count)
	}

	slices.SortStableFunc(anchors, func(x, y anchor) int { return x.ts.Compare(y.ts) })
	var events, control []anchor
	for _, an := range anchors {
		if an.role == roleRestart || an.role == roleClose {
			control = append(control, an)
		} else {
			events = append(events, an)
		}
	}
	events = collapseAnchors(events)
	stream := append(events, control...)
	slices.SortStableFunc(stream, func(x, y anchor) int { return x.ts.Compare(y.ts) })
	a.clusters = clusterAnchors(stream, cfg.gap, seed)
	a.restarts = buildRestarts(starts, stops, lastBuild)
	for _, r := range a.restarts {
		if r.build != "" {
			a.lastBuild = r.build
		}
	}
	if a.lastBuild == "" {
		a.lastBuild = lastBuild
	}

	var standalone []entry
	for _, e := range candidates {
		if severityRank(e.severity) == 2 {
			a.errorItems = append(a.errorItems, e)
		}
		if owner := a.restartFor(e, cfg); owner != nil {
			owner.deps.add(e, 1)
			continue
		}
		if owner := a.clusterFor(e, cfg); owner != nil {
			owner.deps.add(e, 1)
			if severityRank(e.severity) == 2 {
				owner.errorLines++
			}
			continue
		}
		standalone = append(standalone, e)
	}
	var rest []entry
	a.runs, rest = buildFailureRuns(standalone, cfg.gap)
	for _, e := range rest {
		a.items.add(e, 1)
	}
	a.entries = append(a.entries, candidates...)
	return a
}

func (a *analysis) restartFor(e entry, cfg engineConfig) *restartEvent {
	if !e.timed {
		return nil
	}
	for _, r := range a.restarts {
		if !r.stopOnly && !e.ts.Before(r.start) && !e.ts.After(r.ts.Add(cfg.restartEdge)) {
			return r
		}
	}
	return nil
}

func (a *analysis) clusterFor(e entry, cfg engineConfig) *cluster {
	if !e.timed {
		return nil
	}
	for _, c := range a.clusters {
		if c.owns(e.ts, cfg) {
			return c
		}
	}
	return nil
}

func isFarmLine(line string) bool {
	return farmCodePattern.MatchString(line) && !strings.Contains(line, "System notice reqID")
}

func isAnchorLine(line string) bool {
	_, ok := parseAnchor(extractSlogMessage(line), time.Time{})
	return ok
}

func classifyApp(scanned scannedLog) *analysis {
	a := newAnalysis(scanned)
	result := &a.report
	panicActive := false
	add := func(line, level, kind, family, message string) {
		e := lineEntry(line, level, kind, family, message)
		a.items.add(e, 1)
		a.entries = append(a.entries, e)
		if severityRank(level) == 2 {
			a.errorItems = append(a.errorItems, e)
		}
	}
	for _, line := range scanned.lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			result.Informational++
			continue
		}
		if isPanicLine(trimmed) {
			panicActive = true
			add(trimmed, "ERROR", "panic", "panic", "app panic detected")
			continue
		}
		if panicActive && isStackContinuation(line) {
			result.Informational++
			continue
		}
		panicActive = false

		level := severity(trimmed)
		if level == "FATAL" {
			add(trimmed, level, "log_level", genericFamily(appMessage(trimmed)), safeMessage(trimmed))
			continue
		}
		if isRequestCompleted(trimmed) {
			status := httpStatus(trimmed)
			if status >= 500 {
				add(trimmed, "ERROR", "http_5xx", "http_5xx", fmt.Sprintf("app request completed with status %d", status))
			} else if status >= 100 {
				result.Families["request_completed"]++
				result.KnownBenign++
			} else {
				add(trimmed, "WARN", "malformed_access_log", "malformed_access_log", "app access log omitted a valid HTTP status")
			}
			continue
		}
		if isAppLifecycle(trimmed) {
			result.Families["lifecycle"]++
			result.Informational++
			continue
		}
		switch level {
		case "WARN", "ERROR", "FATAL":
			add(trimmed, level, "log_level", genericFamily(appMessage(trimmed)), safeMessage(trimmed))
		case "DEBUG", "INFO":
			result.Informational++
		default:
			add(trimmed, "WARN", "missing_level", "missing_level", "production app line omitted an explicit severity")
		}
	}
	if count := restartLoopCount(lifecycleTimes(scanned.lines, isAppStart)); count > restartLoopStarts {
		a.addItem("WARN", "restart_loop", "restart_loop", "app started repeatedly within "+restartLoopWindow.String(), count)
	}
	return a
}

// appMessage is a line's message for family derivation: the slog msg, else
// the legacy text after date, time and level.
func appMessage(line string) string {
	if m := extractSlogMessage(line); m != "" {
		return m
	}
	fields := strings.Fields(line)
	if len(fields) >= 3 && looksLegacyDate(fields[0]) {
		start := 2
		if severity(line) != "" {
			start = 3
		}
		if len(fields) > start {
			return strings.Join(fields[start:], " ")
		}
	}
	return ""
}
