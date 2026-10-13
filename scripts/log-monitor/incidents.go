package main

import (
	"regexp"
	"slices"
	"time"
)

// Incident clustering. A connectivity event (backend loss/restore, a farm
// break/recovery, the Gateway session going away/coming back) is an anchor;
// anchors that chain in time form one incident, and every WARN line inside
// an incident's window is attributed to it as a dependent instead of
// standing as its own signal. A daemon start is an anchor of its own: lines
// in the first minutes after it belong to the restart. Repeated failures of
// one subsystem outside any incident form a failure run. The owner reads
// "the backend flapped 07:49–18:49 and these families failed with it", not
// forty unrelated line shapes.

type engineConfig struct {
	// before and after widen an incident's span when attributing lines.
	before, after time.Duration
	// restartEdge attributes lines this soon after a daemon start to it.
	restartEdge time.Duration
	// gap chains anchors (and a subsystem's failures) into one incident.
	gap time.Duration
	// longIncident raises attention for an incident at least this long.
	longIncident time.Duration
	// standingDays is how many prior days a family must recur, at an
	// unchanged rate, to read as a standing condition.
	standingDays int
}

func defaultEngineConfig() engineConfig {
	return engineConfig{
		before:       60 * time.Second,
		after:        2 * time.Minute,
		restartEdge:  2 * time.Minute,
		gap:          30 * time.Minute,
		longIncident: 15 * time.Minute,
		standingDays: 7,
	}
}

const (
	maxIncidents       = 20
	maxDependents      = 8
	runMinMinutes      = 3
	anchorCollapseSpan = 2 * time.Second
)

// Anchor subjects and roles.
const (
	subjectBackend = "backend_link"
	subjectGateway = "gateway"
	subjectFarm    = "farm"

	roleBreak   = "break"
	roleRecover = "recover"
	roleSummary = "summary"
	roleClose   = "close"   // closes an open subject, never opens an incident
	roleRestart = "restart" // daemon start: closes the old session's subjects
)

// Farm transition codes: 2103/2105/2157 break, 2104/2106/2158 recover, and
// 2107/2108 report an inactive farm that is available on demand.
var (
	farmCodePattern   = regexp.MustCompile(`\bcode[ =]?(210[3-8]|215[78])\b`)
	farmNamePattern   = regexp.MustCompile(`farm ([A-Za-z0-9.]+) (?:has been disconnected|recovered|is )|(?:OK|broken|inactive)[^:]*:([A-Za-z0-9.]+)`)
	disconnectedFor   = regexp.MustCompile(`disconnected for ([0-9][0-9hmsµ.]*)`)
	recoveredAfter    = regexp.MustCompile(`(?:recovered|restored connectivity to the IBKR backend) (?:\(ok, code \d+\) )?after ([0-9][0-9hmsµ.]*)`)
	backendLoss       = regexp.MustCompile(`lost connectivity to the IBKR backend|\bcode[ =]?(?:1100|2110)\b`)
	backendRestore    = regexp.MustCompile(`restored connectivity to the IBKR backend|\bcode[ =]?110[12]\b`)
	episodeSummary    = regexp.MustCompile(`backend link episode ended: .* over ([0-9][0-9hmsµ.]*)`)
	gatewayDown       = regexp.MustCompile(`^(?:Gateway unavailable|Disconnection detected|Connection closed by|Heartbeat timeout|Client \d+: Failed to read|Error reading message)|IBKR API session lost`)
	gatewayRecovered  = regexp.MustCompile(`^Gateway connection recovered(?: after ([0-9][0-9hmsµ.]*))?`)
	anchorClient      = regexp.MustCompile(`\bcid=(\d+)|Client ID: (\d+)|^Client (\d+):`)
	recoverFarmCodes  = map[string]bool{"2104": true, "2106": true, "2158": true}
	inactiveFarmCodes = map[string]bool{"2107": true, "2108": true}
)

type anchor struct {
	ts, from time.Time
	subject  string
	role     string
	farm     string
	clients  map[string]bool
	errored  bool
	build    string
}

func (a anchor) stateKey() string {
	if a.subject == subjectFarm {
		return subjectFarm + ":" + a.farm
	}
	return a.subject
}

// farmTransition reports a farm line's role and farm, and whether it is one.
// An inactive (2107/2108) line is a farm transition but not an anchor.
func farmTransition(message string) (role, farm string, ok bool) {
	m := farmCodePattern.FindStringSubmatch(message)
	if m == nil {
		return "", "", false
	}
	switch {
	case inactiveFarmCodes[m[1]]:
		role = "inactive"
	case recoverFarmCodes[m[1]]:
		role = roleRecover
	default:
		role = roleBreak
	}
	if n := farmNamePattern.FindStringSubmatch(message); n != nil {
		farm = n[1] + n[2]
	}
	if farm == "" {
		farm = "unnamed"
	}
	return role, farm, true
}

// parseAnchor classifies a WARN/ERROR connector message as an anchor event.
// from back-dates the event when the line itself says how long the condition
// had lasted, so a restore "after 15m16s" opens its incident at the loss.
func parseAnchor(message string, ts time.Time) (anchor, bool) {
	a := anchor{ts: ts, from: ts, clients: map[string]bool{}}
	if m := anchorClient.FindStringSubmatch(message); m != nil {
		a.clients[m[1]+m[2]+m[3]] = true
	}
	message = connectorPrefixOnly(message)
	switch {
	case episodeSummary.MatchString(message):
		a.subject, a.role = subjectBackend, roleSummary
		a.from = backdate(ts, episodeSummary.FindStringSubmatch(message)[1])
	case backendRestore.MatchString(message):
		a.subject, a.role = subjectBackend, roleRecover
	case backendLoss.MatchString(message):
		a.subject, a.role = subjectBackend, roleBreak
	case gatewayRecovered.MatchString(message):
		a.subject, a.role = subjectGateway, roleRecover
		if m := gatewayRecovered.FindStringSubmatch(message); m[1] != "" {
			a.from = backdate(ts, m[1])
		}
	case gatewayDown.MatchString(message):
		a.subject, a.role = subjectGateway, roleBreak
	default:
		role, farm, ok := farmTransition(message)
		if !ok || role == "inactive" {
			return anchor{}, false
		}
		a.subject, a.role, a.farm = subjectFarm, role, farm
		if m := disconnectedFor.FindStringSubmatch(message); m != nil {
			a.from = backdate(ts, m[1])
		}
	}
	if a.role == roleRecover && a.subject != subjectGateway {
		if m := recoveredAfter.FindStringSubmatch(message); m != nil {
			a.from = backdate(ts, m[1])
		}
	}
	return a, true
}

// connectorPrefixOnly strips the connector's own prefixes so anchored
// patterns ("^Gateway unavailable") see the message proper.
func connectorPrefixOnly(message string) string {
	return connectorPrefix.ReplaceAllString(message, "")
}

func backdate(ts time.Time, span string) time.Time {
	d, err := time.ParseDuration(span)
	if err != nil || d < 0 {
		return ts
	}
	return ts.Add(-d)
}

// collapseAnchors merges the copies each connected client logs of one event:
// the same subject, role and farm within a couple of seconds is one event,
// remembered with every client that reported it. Input is in log order.
func collapseAnchors(in []anchor) []anchor {
	slices.SortStableFunc(in, func(a, b anchor) int { return a.ts.Compare(b.ts) })
	var out []anchor
	for _, a := range in {
		merged := false
		for i := len(out) - 1; i >= 0 && a.ts.Sub(out[i].ts) <= anchorCollapseSpan; i-- {
			o := &out[i]
			if o.subject == a.subject && o.role == a.role && o.farm == a.farm {
				for c := range a.clients {
					o.clients[c] = true
				}
				o.errored = o.errored || a.errored
				if a.from.Before(o.from) {
					o.from = a.from
				}
				merged = true
				break
			}
		}
		if !merged {
			out = append(out, a)
		}
	}
	return out
}

type subjectState struct {
	open bool
	at   time.Time
}

// cluster is an incident under construction.
type cluster struct {
	start, chainEnd, end time.Time
	counts               map[string]int
	states               map[string]subjectState
	farms                map[string]bool
	clients              map[string]bool
	errorLines           int
	anchorErrors         int
	closedBy             string
	carried              bool
	carriedFirst         time.Time
	deps                 *collector
}

func newCluster() *cluster {
	return &cluster{counts: map[string]int{}, states: map[string]subjectState{}, farms: map[string]bool{}, clients: map[string]bool{}, deps: newCollector()}
}

func (c *cluster) open() bool {
	for _, s := range c.states {
		if s.open {
			return true
		}
	}
	return false
}

func (c *cluster) openSubjects() []string {
	var out []string
	for key, s := range c.states {
		if s.open {
			out = append(out, key)
		}
	}
	slices.Sort(out)
	return out
}

func (c *cluster) add(a anchor) {
	if c.start.IsZero() || a.from.Before(c.start) {
		c.start = a.from
	}
	if a.ts.After(c.chainEnd) {
		c.chainEnd = a.ts
	}
	if a.role != roleSummary && a.ts.After(c.end) {
		c.end = a.ts
	}
	name := a.role
	switch a.subject {
	case subjectBackend:
		name = map[string]string{roleBreak: "backend_loss", roleRecover: "backend_restore", roleSummary: "backend_episode_summary"}[a.role]
	case subjectGateway:
		name = map[string]string{roleBreak: "gateway_down", roleRecover: "gateway_recovered"}[a.role]
	case subjectFarm:
		name = map[string]string{roleBreak: "farm_break", roleRecover: "farm_recover"}[a.role]
		c.farms[a.farm] = true
	}
	c.counts[name]++
	for client := range a.clients {
		c.clients[client] = true
	}
	if a.errored {
		c.errorLines++
		c.anchorErrors++
	}
	switch a.role {
	case roleBreak:
		c.states[a.stateKey()] = subjectState{open: true, at: a.ts}
	case roleRecover:
		c.states[a.stateKey()] = subjectState{open: false, at: a.ts}
	case roleSummary:
		if _, ok := c.states[subjectBackend]; !ok {
			c.states[subjectBackend] = subjectState{at: a.ts}
		}
	}
}

// closeSubjects closes every open subject matching keep, at ts.
func (c *cluster) closeSubjects(ts time.Time, by string, match func(string) bool) {
	closed := false
	for key, s := range c.states {
		if s.open && match(key) {
			c.states[key] = subjectState{open: false, at: ts}
			closed = true
		}
	}
	if closed {
		if ts.After(c.end) {
			c.end = ts
		}
		if ts.After(c.chainEnd) {
			c.chainEnd = ts
		}
		c.closedBy = by
	}
}

func (c *cluster) merge(o *cluster) {
	if o.start.Before(c.start) {
		c.start = o.start
	}
	if o.chainEnd.After(c.chainEnd) {
		c.chainEnd = o.chainEnd
	}
	if o.end.After(c.end) {
		c.end = o.end
	}
	for k, v := range o.counts {
		c.counts[k] += v
	}
	for k, s := range o.states {
		if cur, ok := c.states[k]; !ok || s.at.After(cur.at) {
			c.states[k] = s
		}
	}
	for k := range o.farms {
		c.farms[k] = true
	}
	for k := range o.clients {
		c.clients[k] = true
	}
	c.errorLines += o.errorLines
	c.anchorErrors += o.anchorErrors
	if o.closedBy != "" {
		c.closedBy = o.closedBy
	}
	if o.carried {
		c.carried, c.carriedFirst = true, o.carriedFirst
	}
}

func (c *cluster) kind() string {
	for _, subject := range []string{subjectGateway, subjectBackend} {
		if _, ok := c.states[subject]; ok {
			return subject
		}
	}
	if len(c.farms) > 0 {
		return subjectFarm
	}
	return subjectBackend
}

// clusterAnchors chains anchors into connectivity incidents. An open
// incident absorbs every later anchor until a closing one (a restore, a
// Gateway reconnect, or a daemon start for the old session's subjects)
// arrives; a closed one absorbs anchors starting within gap of its end.
// seed is an incident carried open from the previous run.
func clusterAnchors(anchors []anchor, gap time.Duration, seed *cluster) []*cluster {
	var clusters []*cluster
	var cur *cluster
	if seed != nil {
		cur = seed
		clusters = append(clusters, seed)
	}
	for _, a := range anchors {
		switch a.role {
		case roleRestart:
			if cur != nil {
				cur.closeSubjects(a.ts, "daemon_restart", func(key string) bool { return key != subjectGateway })
			}
			continue
		case roleClose:
			if cur != nil {
				cur.closeSubjects(a.ts, "gateway_connected", func(key string) bool { return key == subjectGateway })
			}
			continue
		}
		if cur == nil || !cur.open() && a.from.After(cur.chainEnd.Add(gap)) {
			cur = newCluster()
			clusters = append(clusters, cur)
		}
		cur.add(a)
	}
	// A back-dated event (an episode summary, a long-break report) can reach
	// across an earlier, separate cluster: merge clusters whose spans chain.
	slices.SortStableFunc(clusters, func(a, b *cluster) int { return a.start.Compare(b.start) })
	var merged []*cluster
	for _, c := range clusters {
		if n := len(merged); n > 0 {
			prev := merged[n-1]
			if !c.start.After(prev.chainEnd.Add(gap)) {
				prev.merge(c)
				continue
			}
		}
		merged = append(merged, c)
	}
	return merged
}

// window is the span whose lines a cluster owns; an open cluster owns
// everything after its start.
func (c *cluster) owns(ts time.Time, cfg engineConfig) bool {
	if ts.Before(c.start.Add(-cfg.before)) {
		return false
	}
	return c.open() || !ts.After(c.chainEnd.Add(cfg.after))
}

// restartEvent is one daemon start (with a stop just before it folded in) or
// a stop never followed by a start.
type restartEvent struct {
	start, ts    time.Time
	stopOnly     bool
	build        string
	buildChanged bool
	deps         *collector
}

// buildRestarts pairs stop and start markers. A start within five minutes
// after a stop is one restart beginning at the stop.
func buildRestarts(starts, stops []anchor, lastBuild string) []*restartEvent {
	var out []*restartEvent
	used := map[int]bool{}
	previous := lastBuild
	for _, s := range starts {
		ev := &restartEvent{start: s.ts, ts: s.ts, build: s.build, deps: newCollector()}
		for i, stop := range stops {
			if !used[i] && !stop.ts.After(s.ts) && s.ts.Sub(stop.ts) <= 5*time.Minute {
				used[i] = true
				ev.start = stop.ts
			}
		}
		if s.build != "" {
			ev.buildChanged = previous != "" && previous != s.build
			previous = s.build
		}
		out = append(out, ev)
	}
	for i, stop := range stops {
		if used[i] {
			continue
		}
		followed := false
		for _, s := range starts {
			if s.ts.After(stop.ts) {
				followed = true
			}
		}
		if !followed {
			out = append(out, &restartEvent{start: stop.ts, ts: stop.ts, stopOnly: true, deps: newCollector()})
		}
	}
	slices.SortFunc(out, func(a, b *restartEvent) int { return a.start.Compare(b.start) })
	return out
}

// failureRun is a subsystem's standalone failures chained in time.
type failureRun struct {
	subsystem  string
	start, end time.Time
	minutes    int
	deps       *collector
}

// buildFailureRuns clusters unattributed entries per subsystem. A chain of
// failures in at least runMinMinutes distinct minutes is a run; the rest stay
// standalone.
func buildFailureRuns(entries []entry, gap time.Duration) (runs []*failureRun, rest []entry) {
	bySubsystem := map[string][]int{}
	var order []string
	for i, e := range entries {
		if !e.timed {
			continue
		}
		if _, ok := bySubsystem[e.subsystem]; !ok {
			order = append(order, e.subsystem)
		}
		bySubsystem[e.subsystem] = append(bySubsystem[e.subsystem], i)
	}
	consumed := map[int]bool{}
	for _, subsystem := range order {
		idx := bySubsystem[subsystem]
		slices.SortStableFunc(idx, func(a, b int) int { return entries[a].ts.Compare(entries[b].ts) })
		for i := 0; i < len(idx); {
			j := i + 1
			for j < len(idx) && entries[idx[j]].ts.Sub(entries[idx[j-1]].ts) <= gap {
				j++
			}
			minutes := map[int64]bool{}
			for _, k := range idx[i:j] {
				minutes[entries[k].ts.Unix()/60] = true
			}
			if len(minutes) >= runMinMinutes {
				run := &failureRun{subsystem: subsystem, start: entries[idx[i]].ts, end: entries[idx[j-1]].ts, minutes: len(minutes), deps: newCollector()}
				for _, k := range idx[i:j] {
					run.deps.add(entries[k], 1)
					consumed[k] = true
				}
				runs = append(runs, run)
			}
			i = j
		}
	}
	for i, e := range entries {
		if !consumed[i] {
			rest = append(rest, e)
		}
	}
	slices.SortFunc(runs, func(a, b *failureRun) int { return a.start.Compare(b.start) })
	return runs, rest
}
