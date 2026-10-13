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

// Report finalization: incidents and family signals get their state —
// declared, standing, carried, returning or new — and the attention rules
// decide which of them need the owner. Open items persist beside the daemon
// cursor so tomorrow's run can say what resolved and what is still there.

const (
	openItemsVersion = 1
	// growthFactor is what "grew" means for a carried item and what "an
	// unchanged rate" tolerates for a standing one: at least double the
	// previous count (and three more lines) is growth, anything less is the
	// same condition again.
	growthFactor  = 2
	growthMinimum = 3
	maxAttention  = 25
	maxSuppressed = 20
	maxListed     = 20
)

type incident struct {
	Kind string `json:"kind"`
	// Subject names a failure run's subsystem.
	Subject  string    `json:"subject,omitempty"`
	Start    time.Time `json:"start"`
	End      time.Time `json:"end"`
	Duration string    `json:"duration"`
	Open     bool      `json:"open"`
	// Status is new, carried (open at the end of the previous run, or a
	// failure run seen then too) or standing.
	Status    string    `json:"status"`
	FirstSeen time.Time `json:"first_seen,omitzero"`
	// Anchors counts connectivity events, each client's copy of one event
	// collapsed into one.
	Anchors      map[string]int `json:"anchors,omitempty"`
	Clients      int            `json:"clients,omitempty"`
	Farms        []string       `json:"farms,omitempty"`
	OpenSubjects []string       `json:"open_subjects,omitempty"`
	ClosedBy     string         `json:"closed_by,omitempty"`
	Build        string         `json:"build,omitempty"`
	BuildChanged bool           `json:"build_changed,omitempty"`
	// Dependents are the families whose lines fell inside the incident's
	// window, attributed to it rather than reported as signals.
	Dependents     []dependent `json:"dependents,omitempty"`
	DependentLines int         `json:"dependent_lines,omitempty"`
	ErrorLines     int         `json:"error_lines,omitempty"`
	Sample         string      `json:"sample,omitempty"`
	Attention      string      `json:"attention,omitempty"`
}

type dependent struct {
	Family   string `json:"family"`
	Severity string `json:"severity"`
	Count    int    `json:"count"`
}

type standingItem struct {
	Family string `json:"family"`
	Days   int    `json:"days"`
	Count  int    `json:"count"`
	// Typical is the median daily count over the prior days.
	Typical int `json:"typical"`
}

type declaredItem struct {
	Item   string `json:"item"`
	Count  int    `json:"count"`
	Reason string `json:"reason,omitempty"`
}

type resolvedItem struct {
	Item      string    `json:"item"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
	Days      int       `json:"days"`
}

type attentionItem struct {
	Log    string `json:"log"`
	Reason string `json:"reason"`
	Item   string `json:"item"`
	Detail string `json:"detail"`
}

type openItemsFile struct {
	Version   int                 `json:"version"`
	LastBuild string              `json:"last_build,omitempty"`
	Items     map[string]openItem `json:"items"`
}

type openItem struct {
	Kind      string    `json:"kind"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
	Count     int       `json:"count"`
	Days      int       `json:"days"`
	// Open incident state, so the next run continues the same incident.
	Start        time.Time `json:"start,omitzero"`
	End          time.Time `json:"end,omitzero"`
	OpenSubjects []string  `json:"open_subjects,omitempty"`
	Farms        []string  `json:"farms,omitempty"`
}

func readOpenItems(path string) (openItemsFile, string) {
	empty := openItemsFile{Version: openItemsVersion, Items: map[string]openItem{}}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return empty, "missing"
	}
	var file openItemsFile
	if err != nil || json.Unmarshal(raw, &file) != nil || file.Version != openItemsVersion {
		return empty, "reset"
	}
	if file.Items == nil {
		file.Items = map[string]openItem{}
	}
	return file, "loaded"
}

func writeOpenItems(path string, file openItemsFile) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if err := tmp.Chmod(0o600); err != nil {
		return err
	}
	if err := json.NewEncoder(tmp).Encode(file); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

const connectivityKey = "incident:connectivity"

// seedCluster rebuilds the incident left open by the previous run.
func seedCluster(prior openItemsFile) *cluster {
	item, ok := prior.Items["daemon:"+connectivityKey]
	if !ok || len(item.OpenSubjects) == 0 {
		return nil
	}
	c := newCluster()
	c.start, c.end, c.chainEnd = item.Start, item.End, item.End
	c.carried, c.carriedFirst = true, item.FirstSeen
	for _, key := range item.OpenSubjects {
		c.states[key] = subjectState{open: true, at: item.End}
	}
	for _, farm := range item.Farms {
		c.farms[farm] = true
	}
	return c
}

// finalizer carries the run-wide inputs of finalization.
type finalizer struct {
	cfg        engineConfig
	now        time.Time
	maxSignals int
	decls      declarations
	prior      openItemsFile
	next       openItemsFile
	attention  []attentionItem
}

func (f *finalizer) attend(log, reason, item, detail string) {
	for _, a := range f.attention {
		if a.Log == log && a.Item == item {
			return
		}
	}
	f.attention = append(f.attention, attentionItem{Log: log, Reason: reason, Item: item, Detail: detail})
}

func (f *finalizer) day(ts time.Time) time.Time {
	if ts.IsZero() {
		return f.now
	}
	return ts
}

// carry records an item as open for the next run and reports its carried
// state: first seen, days seen, and whether it grew since the last run.
func (f *finalizer) carry(key string, item openItem) (status string, firstSeen time.Time, days int, grew bool) {
	item.LastSeen, item.Days, item.FirstSeen = f.now, 1, f.now
	prior, ok := f.prior.Items[key]
	status = "new"
	if ok {
		status = "carried"
		item.FirstSeen = prior.FirstSeen
		item.Days = prior.Days
		if prior.LastSeen.UTC().Format("2006-01-02") != f.now.UTC().Format("2006-01-02") {
			item.Days++
		}
		grew = item.Count >= growthFactor*prior.Count && item.Count-prior.Count >= growthMinimum
	}
	f.next.Items[key] = item
	return status, item.FirstSeen, item.Days, grew
}

// history is the recurrence evidence before this scan: family -> day -> count.
type history map[string]map[string]int

func (h history) seen(family string) bool { return len(h[family]) > 0 }

// standing reports a family recurring on at least standingDays prior days
// whose count now is within growthFactor of its median daily count.
func (h history) standing(family string, count int, today string, minDays int) (standingItem, bool) {
	var counts []int
	for day, n := range h[family] {
		if day != today {
			counts = append(counts, n)
		}
	}
	if len(counts) < minDays || minDays < 1 {
		return standingItem{}, false
	}
	slices.Sort(counts)
	median := counts[len(counts)/2]
	if count > growthFactor*max(median, 1) {
		return standingItem{}, false
	}
	return standingItem{Family: family, Days: len(counts), Count: count, Typical: median}, true
}

func formatDuration(d time.Duration) string {
	d = d.Round(time.Second)
	if d >= time.Minute {
		d = d.Round(time.Minute)
		return strings.TrimSuffix(d.String(), "0s")
	}
	return d.String()
}

// finalize turns one log's analysis into its report section.
func (f *finalizer) finalize(log string, a *analysis, h history) logReport {
	result := a.report
	today := f.now.UTC().Format("2006-01-02")

	// Families whose standalone signal the owner declared also cover their
	// ERROR lines; every other ERROR needs the owner.
	declaredFamilies := map[string]bool{}

	// Standalone family signals.
	var signals []signal
	for _, family := range a.items.order {
		agg := a.items.byFamily[family]
		samples := agg.topSamples(4)
		sig := signal{Severity: agg.severity, Kind: agg.kind, Family: family, Count: agg.count, Distinct: len(agg.samples), First: agg.first, Last: agg.last}
		if len(samples) > 0 {
			sig.Message = samples[0]
			sig.OtherSamples = samples[1:]
		}
		keys := withLog(log, family, agg.kind)
		if decl, ok := f.decls.find(keys, strings.Join(samples, "\n"), f.day(agg.first), f.day(agg.last)); ok && agg.kind != "panic" && agg.kind != "crash_output" {
			declaredFamilies[family] = true
			result.Declared = append(result.Declared, declaredItem{Item: log + ":" + family, Count: agg.count, Reason: decl.Reason})
			continue
		}
		key := log + ":family:" + family
		if st, ok := h.standing(family, agg.count, today, f.cfg.standingDays); ok && severityRank(agg.severity) < 2 {
			f.carry(key, openItem{Kind: "standing", Count: agg.count})
			result.Standing = append(result.Standing, st)
			continue
		}
		status, first, days, grew := f.carry(key, openItem{Kind: "family", Count: agg.count})
		if status == "new" && h.seen(family) {
			status = "returning"
		}
		sig.Status, sig.DaysSeen = status, max(days, len(h[family]))
		if status == "carried" {
			sig.FirstSeen = first
		}
		item := log + ":" + family
		switch {
		case severityRank(agg.severity) == 2:
			sig.Attention = "error"
		case status == "new" && agg.kind == "log_coverage":
			sig.Attention = "coverage"
		case status == "new":
			sig.Attention = "new_family"
		case grew:
			sig.Attention = "grew"
		}
		if sig.Attention != "" {
			f.attend(log, sig.Attention, item, fmt.Sprintf("%d× %s", sig.Count, sig.Message))
		}
		signals = append(signals, sig)
	}
	for _, e := range a.errorItems {
		if !declaredFamilies[e.family] || e.kind == "panic" || e.kind == "crash_output" {
			f.attend(log, "error", log+":"+e.family, e.severity+" "+e.message)
		}
	}

	// Connectivity incidents.
	var incidents []incident
	for _, c := range a.clusters {
		inc := c.report()
		if inc.Open {
			// Carry an open incident even when declared, so the next run
			// continues it rather than starting a new one.
			first := c.start
			if c.carried {
				first = c.carriedFirst
			}
			f.next.Items[log+":"+connectivityKey] = openItem{Kind: "incident", FirstSeen: first, LastSeen: f.now, Count: c.eventCount(), Days: 1,
				Start: c.start, End: inc.End, OpenSubjects: inc.OpenSubjects, Farms: inc.Farms}
		}
		subjects := c.subjectKeys()
		allDeclared, reason := len(subjects) > 0, ""
		for _, keys := range subjects {
			decl, ok := f.decls.find(withLog(log, keys...), "", c.start, f.day(c.end))
			if !ok {
				allDeclared = false
				break
			}
			reason = decl.Reason
		}
		if allDeclared {
			result.Declared = append(result.Declared, declaredItem{Item: log + ":incident:" + inc.Kind, Count: c.eventCount(), Reason: reason})
			continue
		}
		inc.Status = "new"
		if c.carried {
			inc.Status, inc.FirstSeen = "carried", c.carriedFirst
		}
		duration := inc.End.Sub(inc.Start)
		switch {
		case c.anchorErrors > 0:
			inc.Attention = "error"
		case inc.Open:
			inc.Attention = "open_incident"
		case duration >= f.cfg.longIncident:
			inc.Attention = "long_incident"
		}
		if inc.Attention != "" {
			detail := fmt.Sprintf("%s %s → %s (%s)", inc.Kind, inc.Start.Format(time.RFC3339), inc.End.Format(time.RFC3339), inc.Duration)
			if inc.Open {
				detail = fmt.Sprintf("%s open since %s (%s so far)", inc.Kind, inc.Start.Format(time.RFC3339), inc.Duration)
			}
			f.attend(log, inc.Attention, log+":incident:"+inc.Kind+"@"+inc.Start.UTC().Format(time.RFC3339), detail)
		}
		incidents = append(incidents, inc)
	}

	// Daemon restarts.
	for _, r := range a.restarts {
		inc := incident{Kind: "daemon_restart", Start: r.start, End: r.ts, Build: r.build, BuildChanged: r.buildChanged, Status: "new"}
		if r.stopOnly {
			inc.Kind, inc.Open = "daemon_stop", true
		}
		inc.Duration = formatDuration(inc.End.Sub(inc.Start))
		inc.Dependents, inc.DependentLines = r.deps.dependents()
		if decl, ok := f.decls.find(withLog(log, inc.Kind), "", r.start); ok {
			result.Declared = append(result.Declared, declaredItem{Item: log + ":incident:" + inc.Kind, Count: 1, Reason: decl.Reason})
			continue
		}
		if inc.Open {
			inc.Attention = "open_incident"
			f.attend(log, inc.Attention, log+":incident:daemon_stop@"+r.start.UTC().Format(time.RFC3339), "daemon stopped at "+r.start.Format(time.RFC3339)+" with no start after it")
		}
		incidents = append(incidents, inc)
	}

	// Failure runs.
	for _, run := range a.runs {
		inc := incident{Kind: "failure_run", Subject: run.subsystem, Start: run.start, End: run.end, Duration: formatDuration(run.end.Sub(run.start))}
		inc.Dependents, inc.DependentLines = run.deps.dependents()
		var families []string
		var sampleText []string
		standing, novel := true, false
		for _, family := range run.deps.order {
			agg := run.deps.byFamily[family]
			families = append(families, family)
			sampleText = append(sampleText, agg.topSamples(1)...)
			if _, ok := h.standing(family, agg.count, today, f.cfg.standingDays); !ok {
				standing = false
			}
			if !h.seen(family) {
				novel = true
			}
			if severityRank(agg.severity) == 2 {
				inc.ErrorLines += agg.count
			}
		}
		if top := largest(run.deps); top != nil {
			inc.Sample = top.topSamples(1)[0]
		}
		keys := withLog(log, append([]string{run.subsystem, "run:" + run.subsystem}, families...)...)
		if decl, ok := f.decls.find(keys, strings.Join(sampleText, "\n"), run.start, run.end); ok {
			result.Declared = append(result.Declared, declaredItem{Item: log + ":run:" + run.subsystem, Count: inc.DependentLines, Reason: decl.Reason})
			continue
		}
		key := log + ":run:" + run.subsystem
		status, first, _, grew := f.carry(key, openItem{Kind: "run", Count: inc.DependentLines})
		inc.Status = status
		if status == "carried" {
			inc.FirstSeen = first
		}
		if standing {
			inc.Status = "standing"
		} else if status != "carried" || grew {
			switch {
			case run.end.Sub(run.start) >= f.cfg.longIncident:
				inc.Attention = "long_incident"
			case novel:
				inc.Attention = "new_family"
			case grew:
				inc.Attention = "grew"
			}
		}
		if inc.Attention != "" {
			f.attend(log, inc.Attention, key+"@"+run.start.UTC().Format(time.RFC3339), fmt.Sprintf("%s failures %s → %s (%d lines in %d minutes)", run.subsystem, run.start.Format(time.RFC3339), run.end.Format(time.RFC3339), inc.DependentLines, run.minutes))
		}
		incidents = append(incidents, inc)
	}
	slices.SortStableFunc(incidents, func(a, b incident) int { return a.Start.Compare(b.Start) })
	if len(incidents) > maxIncidents {
		// Keep every incident that needs the owner, then the latest others.
		keep := make([]bool, len(incidents))
		left := maxIncidents
		for i := range incidents {
			if incidents[i].Attention != "" && left > 0 {
				keep[i], left = true, left-1
			}
		}
		for i := len(incidents) - 1; i >= 0 && left > 0; i-- {
			if !keep[i] {
				keep[i], left = true, left-1
			}
		}
		var kept []incident
		for i, inc := range incidents {
			if keep[i] {
				kept = append(kept, inc)
			} else {
				result.IncidentsOmitted++
			}
		}
		incidents = kept
	}
	result.Incidents = incidents

	// Items carried from the previous run and absent now resolved; report
	// each once. An open incident is never resolved here: the run that
	// closes it reports it, closed.
	for key, item := range f.prior.Items {
		if !strings.HasPrefix(key, log+":") || key == log+":"+connectivityKey {
			continue
		}
		if _, still := f.next.Items[key]; still {
			continue
		}
		result.Resolved = append(result.Resolved, resolvedItem{Item: strings.Replace(strings.TrimPrefix(key, log+":"), "family:", "", 1), FirstSeen: item.FirstSeen, LastSeen: item.LastSeen, Days: item.Days})
	}
	slices.SortFunc(result.Resolved, func(a, b resolvedItem) int { return strings.Compare(a.Item, b.Item) })
	result.Resolved = clip(result.Resolved, maxListed, &result.ListsOmitted)
	result.Standing = clip(result.Standing, maxListed, &result.ListsOmitted)
	result.Declared = clip(result.Declared, maxListed, &result.ListsOmitted)

	result.Signals = signals
	finalizeSignals(&result, f.maxSignals)
	return result
}

func clip[T any](items []T, n int, omitted *int) []T {
	if len(items) <= n {
		return items
	}
	*omitted += len(items) - n
	return items[:n:n]
}

func largest(c *collector) *familyAgg {
	var top *familyAgg
	for _, family := range c.order {
		if agg := c.byFamily[family]; top == nil || agg.count > top.count {
			top = agg
		}
	}
	return top
}

// subjectKeys lists, per subject of the incident, the keys a declaration may
// name: backend_link, gateway, or a farm by name.
func (c *cluster) subjectKeys() [][]string {
	var out [][]string
	keys := make([]string, 0, len(c.states))
	for key := range c.states {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		switch {
		case strings.HasPrefix(key, subjectFarm+":"):
			name := strings.TrimPrefix(key, subjectFarm+":")
			out = append(out, []string{key, name, subjectFarm})
		case key == subjectBackend:
			out = append(out, []string{subjectBackend, "backend"})
		default:
			out = append(out, []string{key})
		}
	}
	return out
}

func (c *cluster) eventCount() int {
	n := 0
	for _, v := range c.counts {
		n += v
	}
	return n
}

func (c *cluster) report() incident {
	end := c.end
	if end.IsZero() || end.Before(c.start) {
		end = c.chainEnd
	}
	inc := incident{Kind: c.kind(), Start: c.start, End: end, Open: c.open(), ClosedBy: c.closedBy, ErrorLines: c.errorLines}
	inc.Duration = formatDuration(end.Sub(c.start))
	if len(c.counts) > 0 {
		inc.Anchors = c.counts
	}
	inc.Clients = len(c.clients)
	for farm := range c.farms {
		inc.Farms = append(inc.Farms, farm)
	}
	slices.Sort(inc.Farms)
	if len(inc.Farms) > 16 {
		inc.Farms = append(inc.Farms[:16:16], "…")
	}
	inc.OpenSubjects = c.openSubjects()
	if inc.Open {
		inc.ClosedBy = ""
	}
	inc.Dependents, inc.DependentLines = c.deps.dependents()
	return inc
}
