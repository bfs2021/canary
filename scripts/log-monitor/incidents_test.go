package main

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Synthetic lines only: symbols are SYNTH*, accounts and order numbers never
// appear.

var day = time.Date(2026, 10, 12, 0, 0, 0, 0, time.FixedZone("CEST", 2*3600))

func at(hhmmss string) time.Time {
	t, err := time.Parse("15:04:05", hhmmss)
	if err != nil {
		panic(err)
	}
	return day.Add(time.Duration(t.Hour())*time.Hour + time.Duration(t.Minute())*time.Minute + time.Duration(t.Second())*time.Second)
}

func warn(hhmmss, msg string) string {
	return "time=" + at(hhmmss).Format(time.RFC3339Nano) + ` level=WARN msg="` + msg + `" component="IBKR Connector"`
}

func info(hhmmss, msg string) string {
	return "time=" + at(hhmmss).Format(time.RFC3339Nano) + ` level=INFO msg="` + msg + `"`
}

// Both connected clients log each backend transition.
func loss(hhmmss string) []string {
	m := "IBKRConnector: TWS lost connectivity to the IBKR backend (code 1100); waiting for a 1101/1102 restore notice — during open trading session"
	return []string{warn(hhmmss, m), warn(hhmmss, m)}
}

func restore(hhmmss, after string, n int) []string {
	return []string{
		warn(hhmmss, "IBKRConnector: TWS restored connectivity to the IBKR backend after "+after+" (loss "+strconv.Itoa(n)+" this session, restore_code=1102); 3 cancel echoes (code 300) kept in debug logs"),
		warn(hhmmss, "IBKRConnector: TWS restored connectivity to the IBKR backend after "+after+" (loss "+strconv.Itoa(n+40)+" this session, restore_code=1102)"),
	}
}

func flat(groups ...[]string) []string { return slices.Concat(groups...) }

// A flap of losses every fifteen minutes is one incident; both clients'
// copies of each loss count once; a WARN three seconds after a loss belongs
// to it, and one long after the last restore stands alone.
func TestBackendFlapIsOneIncidentWithCollapsedClients(t *testing.T) {
	got := daemonReport(defaultMaxSignals, flat(
		loss("13:35:04"), restore("13:35:09", "5s", 30),
		[]string{warn("13:35:07", "market history SYNTHA 1D: IBKR refresh failed: HMDS server disconnect occurred; serving recorded history")},
		loss("13:50:04"), restore("13:50:10", "6s", 31),
		loss("14:05:04"), restore("14:05:11", "7s", 32),
		[]string{warn("16:00:00", "regime: SYNTHY spot subscribe delivered no tick")},
	)...)
	if len(got.Incidents) != 1 {
		t.Fatalf("incidents = %+v", got.Incidents)
	}
	inc := got.Incidents[0]
	if inc.Kind != "backend_link" || inc.Anchors["backend_loss"] != 3 || inc.Anchors["backend_restore"] != 3 || inc.Open {
		t.Fatalf("incident = %+v", inc)
	}
	if !inc.Start.Equal(at("13:35:04")) || !inc.End.Equal(at("14:05:11")) || inc.Duration != "30m" || inc.Attention != "long_incident" {
		t.Fatalf("span = %v → %v (%s) attention %q", inc.Start, inc.End, inc.Duration, inc.Attention)
	}
	if inc.DependentLines != 1 || inc.Dependents[0].Family != "market_history" {
		t.Fatalf("dependents = %+v", inc.Dependents)
	}
	if len(got.Signals) != 1 || got.Signals[0].Family != "regime" {
		t.Fatalf("standalone = %+v", got.Signals)
	}
}

// Anchors further apart than the gap are separate incidents; a short one is
// informational.
func TestDistantAnchorsAreSeparateIncidents(t *testing.T) {
	got := daemonReport(defaultMaxSignals, flat(
		loss("09:00:00"), restore("09:00:06", "6s", 1),
		loss("12:00:00"), restore("12:00:06", "6s", 2),
	)...)
	if len(got.Incidents) != 2 || got.Incidents[0].Attention != "" || got.Incidents[1].Attention != "" {
		t.Fatalf("incidents = %+v", got.Incidents)
	}
}

// The episode summary's span reaches back to its first loss, joining an
// earlier cluster (a farm break) into one incident; the summary itself does
// not extend the incident's end.
func TestEpisodeSummaryBackdatesTheIncident(t *testing.T) {
	got := daemonReport(defaultMaxSignals, flat(
		[]string{
			warn("08:13:26", "IBKRConnector: [cid=15] IBKR market data farm synthbond has been disconnected for 19m21s (code 2103); requests it serves keep waiting"),
			warn("08:13:26", "IBKRConnector: [cid=16] IBKR market data farm synthbond has been disconnected for 19m21s (code 2103); requests it serves keep waiting"),
			warn("08:30:10", "IBKRConnector: [cid=15] IBKR market data farm synthbond recovered (ok, code 2104) after 36m5s"),
			warn("08:30:10", "IBKRConnector: [cid=16] IBKR market data farm synthbond recovered (ok, code 2104) after 36m5s"),
		},
		loss("13:35:04"), restore("13:35:09", "5s", 3),
		[]string{
			warn("13:52:26", "IBKRConnector: TWS backend link episode ended: 29 losses over 6h3m5s, longest outage 6s"),
			warn("13:52:26", "IBKRConnector: TWS backend link episode ended: 29 losses over 6h3m5s, longest outage 6s"),
		},
	)...)
	if len(got.Incidents) != 1 {
		t.Fatalf("incidents = %+v", got.Incidents)
	}
	inc := got.Incidents[0]
	if !inc.Start.Equal(at("07:49:21")) || !inc.End.Equal(at("13:35:09")) {
		t.Fatalf("span = %v → %v", inc.Start, inc.End)
	}
	if inc.Anchors["farm_break"] != 1 || inc.Anchors["farm_recover"] != 1 || inc.Anchors["backend_episode_summary"] != 1 || inc.Clients != 2 {
		t.Fatalf("anchors = %+v clients %d", inc.Anchors, inc.Clients)
	}
	if !slices.Equal(inc.Farms, []string{"synthbond"}) {
		t.Fatalf("farms = %v", inc.Farms)
	}
}

// One eleven-farm outage logged as 2103/2105/2157 breaks and 2104/2106/2158
// recoveries by two clients is one farm incident, not 44 signals. 2108
// (inactive, available on demand) is counted but anchors nothing.
func TestFarmTransitionsAreOneFamily(t *testing.T) {
	var lines []string
	farms := []struct{ name, kind, broken, ok string }{
		{"synthfarm", "market data farm", "2103", "2104"},
		{"synthhmds", "historical data farm", "2105", "2106"},
		{"synthsec", "security definition data farm", "2157", "2158"},
	}
	for _, cid := range []string{"15", "16"} {
		for _, f := range farms {
			lines = append(lines, warn("17:14:09", "IBKRConnector: [cid="+cid+"] IBKR "+f.kind+" "+f.name+" has been disconnected for 15m3s (code "+f.broken+"); requests it serves keep waiting"))
		}
	}
	for _, cid := range []string{"15", "16"} {
		for _, f := range farms {
			lines = append(lines, warn("17:14:10", "IBKRConnector: [cid="+cid+"] IBKR "+f.kind+" "+f.name+" recovered (ok, code "+f.ok+") after 15m4s"))
		}
	}
	lines = append(lines, warn("18:30:00", "IBKRConnector: [cid=15] System notice global code=2108: Market data farm connection is inactive but should be available upon demand.synthfarm"))
	got := daemonReport(defaultMaxSignals, lines...)
	if len(got.Incidents) != 1 || len(got.Signals) != 0 {
		t.Fatalf("incidents = %+v signals = %+v", got.Incidents, got.Signals)
	}
	inc := got.Incidents[0]
	if inc.Kind != "farm" || inc.Anchors["farm_break"] != 3 || inc.Anchors["farm_recover"] != 3 || inc.Clients != 2 || inc.Open || len(inc.Farms) != 3 {
		t.Fatalf("farm incident = %+v", inc)
	}
	if !inc.Start.Equal(at("16:59:06")) || inc.Attention != "long_incident" {
		t.Fatalf("farm outage start = %v attention %q", inc.Start, inc.Attention)
	}
	if got.Families["farm_transition"] != 13 || got.KnownBenign != 1 {
		t.Fatalf("families = %v benign = %d", got.Families, got.KnownBenign)
	}
}

// The Gateway going away opens an incident that stays open at the end of the
// window; lines while it is down are its dependents.
func TestGatewayOutageStaysOpen(t *testing.T) {
	got := daemonReport(defaultMaxSignals,
		warn("20:47:04", "Gateway unavailable: IBKR API session lost: EOF (observations=1, duration=0s; repeated attempts remain in debug logs)"),
		warn("21:02:35", "Gateway unavailable: no TWS / IB Gateway / IBKR Desktop process running; waiting for one to start (observations=326, duration=15m)"),
		warn("21:10:00", "gamma scheduler: gateway connector unavailable, skipping compute"),
		warn("23:32:36", "Gateway unavailable: no TWS / IB Gateway / IBKR Desktop process running; waiting for one to start (observations=3276, duration=2h45m)"),
	)
	if len(got.Incidents) != 1 || len(got.Signals) != 0 {
		t.Fatalf("report = %+v", got)
	}
	inc := got.Incidents[0]
	if inc.Kind != "gateway" || !inc.Open || inc.Attention != "open_incident" || inc.Anchors["gateway_down"] != 3 || !slices.Equal(inc.OpenSubjects, []string{"gateway"}) {
		t.Fatalf("gateway incident = %+v", inc)
	}
	if inc.DependentLines != 1 || inc.Dependents[0].Family != "gamma_scheduler" {
		t.Fatalf("dependents = %+v", inc.Dependents)
	}
}

// Lines in the first minutes after a daemon start belong to the restart;
// a subsystem failing through the evening outside any incident is one
// failure run.
func TestRestartEdgeAndFailureRun(t *testing.T) {
	prewarm := "gamma.prewarm class=SPY expiry=20261016 listed=0 cached=0 dropped=0 elapsed=50.1s err=prewarm SPY 20261016 class=SPY route attempts SMART+ARCA[10.1s: contract details request failed (IBKR 200)]"
	got := daemonReport(defaultMaxSignals,
		info("19:01:13", "canary daemon serving v9.9.9 (build aaaa1111) on /tmp/synthetic.sock (gateway=127.0.0.1:7496, clientID=15)"),
		info("19:01:13", "Connected to IB Gateway 127.0.0.1:7496 (clientID=15, tls=false)"),
		warn("19:01:43", "official series SYNTHSERIES: fetched series ends 2026-10-06, 7 days behind; serving it"),
		warn("19:03:01", prewarm),
		warn("19:11:59", prewarm),
		warn("19:12:31", "gamma.abort reason=early_abort landed=0/735 elapsed=2m17s no_contract=0"),
		warn("19:36:59", prewarm),
		warn("20:01:59", prewarm),
	)
	if len(got.Incidents) != 2 || len(got.Signals) != 0 {
		t.Fatalf("report = %+v", got)
	}
	restart, run := got.Incidents[0], got.Incidents[1]
	if restart.Kind != "daemon_restart" || restart.Build != "aaaa1111" || restart.DependentLines != 2 {
		t.Fatalf("restart = %+v", restart)
	}
	if run.Kind != "failure_run" || run.Subject != "gamma" || run.DependentLines != 4 || !run.Start.Equal(at("19:11:59")) || run.Attention != "long_incident" {
		t.Fatalf("run = %+v", run)
	}
	if !strings.Contains(run.Sample, "contract details request failed (IBKR 200)") {
		t.Fatalf("run sample = %q", run.Sample)
	}
}

func TestCountsAndOrdinalsNormalize(t *testing.T) {
	for in, want := range map[string]string{
		"TWS restored connectivity to the IBKR backend after [duration] (loss 30 this session, restore_code=1102); 7 cancel echoes (code 300) kept": "TWS restored connectivity to the IBKR backend after [duration] (loss N this session, restore_code=1102); N cancel echoes (code 300) kept",
		"TWS backend link episode ended: 29 losses over [duration], longest outage [duration]":                                                      "TWS backend link episode ended: N losses over [duration], longest outage [duration]",
		"Disconnection detected (Client ID: 15): EOF":                                                                                               "Disconnection detected (Client N): EOF",
		"Client 16: Failed to read length: EOF":                                                                                                     "Client N: Failed to read length: EOF",
		"[IBKR cid=N] System notice code=200 waiting for a 1101/1102 restore":                                                                       "[IBKR cid=N] System notice code=200 waiting for a 1101/1102 restore",
		"market history SYNTHA 1D: contract details request failed (IBKR 200); 365d cache":                                                          "market history SYNTHA 1D: contract details request failed (IBKR 200); 365d cache",
		"Gateway connection recovered after [duration] (8 unavailable observations)":                                                                "Gateway connection recovered after [duration] (N unavailable observations)",
		"order 2 of 3": "order N of N",
	} {
		if got := normalizeCounts(in); got != want {
			t.Errorf("normalizeCounts(%q) = %q, want %q", in, got, want)
		}
	}
	// Every restore line of a session is one sample.
	got := daemonReport(defaultMaxSignals, flat(restore("10:00:00", "5s", 1), restore("11:00:00", "6s", 2), restore("12:00:00", "7s", 3))...)
	if len(got.Incidents) != 3 {
		t.Fatalf("incidents = %+v", got.Incidents)
	}
	for in, want := range map[string]string{
		"[IBKR cid=15] System notice reqID=1 (SYNTHX STK) code=354: not subscribed": "broker_code_354",
		"IBKRConnector: [cid=15] gamma.prewarm class=SPY":                           "gamma",
		"gamma compute: scope=spy+spx failed":                                       "gamma_compute",
		"market history SYNTHA 1D: IBKR refresh failed":                             "market_history",
		"Historical data request pacing violation":                                  "historical_data",
		"SYNTHQ 42": "other",
	} {
		if got := genericFamily(in); got != want {
			t.Errorf("genericFamily(%q) = %q, want %q", in, got, want)
		}
	}
}

// writeStates writes an owner states file for a test.
func writeStates(t *testing.T, decls string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "states.json")
	writeTestFile(t, path, `{"version":1,"declarations":[`+decls+`]}`)
	return path
}

// A bond-market holiday declared for its date silences the usbond farm that
// day only; a key-like match never declares a family whose text merely
// mentions the word; a missing states file declares nothing and is no error;
// an expired declaration no longer applies.
func TestDeclarationsSilenceOnlyWhatTheyName(t *testing.T) {
	farmDay := func(date string) string {
		ts := date + "T08:13:26+02:00"
		return "time=" + ts + ` level=WARN msg="IBKRConnector: [cid=15] IBKR market data farm usbond has been disconnected for 19m21s (code 2103); requests it serves keep waiting"` + "\n" +
			"time=" + date + `T08:20:00+02:00 level=WARN msg="IBKRConnector: [cid=15] IBKR market data farm usbond recovered (ok, code 2104) after 26m"` + "\n" +
			"time=" + date + `T10:40:00+02:00 level=WARN msg="gamma.combine.spy_unavailable err=zero-gamma: check gateway entitlement"` + "\n"
	}
	states := writeStates(t, `{"match":"usbond","reason":"US bond market holiday","dates":["2026-10-12"],"declared_by":"owner"},
		{"match":"gateway","reason":"TWS stopped on purpose","until":"2026-10-12"},
		{"match":"app:log_coverage_stale","reason":"app stopped on purpose","until":"2026-10-11"}`)

	opts := testMonitorOptions(t)
	opts.states = states
	opts.staleAfter = time.Hour
	old := time.Now().Add(-48 * time.Hour)
	writeTestFile(t, opts.daemonLog, farmDay("2026-10-12"))
	if err := os.Chtimes(opts.appLog, old, old); err != nil {
		t.Fatal(err)
	}
	got, err := run(opts, time.Date(2026, 10, 12, 22, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if got.Declarations.State != "loaded" || got.Declarations.Loaded != 3 {
		t.Fatalf("declarations = %+v", got.Declarations)
	}
	if len(got.Daemon.Declared) != 1 || got.Daemon.Declared[0].Item != "daemon:incident:farm" || got.Daemon.Declared[0].Reason != "US bond market holiday" {
		t.Fatalf("declared = %+v", got.Daemon.Declared)
	}
	// "gateway" is key-like: the gamma family mentioning it is not declared.
	if len(got.Daemon.Signals) != 1 || got.Daemon.Signals[0].Family != "gamma" {
		t.Fatalf("signals = %+v", got.Daemon.Signals)
	}
	// The app declaration expired the day before.
	if len(got.App.Signals) != 1 || got.App.Signals[0].Family != "log_coverage_stale" || len(got.App.Declared) != 0 {
		t.Fatalf("app = %+v", got.App)
	}

	// The next day the holiday declaration no longer applies.
	f, _ := os.OpenFile(opts.daemonLog, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(farmDay("2026-10-13"))
	f.Close()
	got, err = run(opts, time.Date(2026, 10, 13, 22, 0, 0, 0, time.UTC))
	if err != nil || len(got.Daemon.Declared) != 0 || len(got.Daemon.Incidents) != 1 || got.Daemon.Incidents[0].Kind != "farm" {
		t.Fatalf("day after = %+v %v", got.Daemon, err)
	}

	missing := loadDeclarations(filepath.Join(t.TempDir(), "absent.json"))
	if missing.report.State != "missing" || len(missing.items) != 0 {
		t.Fatalf("missing = %+v", missing)
	}
	invalid := writeStates(t, `{"match":"","until":"2026-12-31"},{"match":"forever"},{"match":"A phrase","until":"2026-12-31"}`)
	if d := loadDeclarations(invalid); d.report.Loaded != 1 || d.report.Ignored != 2 || d.items[0].Match != "a phrase" {
		t.Fatalf("validation = %+v", d)
	}
	broken := filepath.Join(t.TempDir(), "broken.json")
	writeTestFile(t, broken, "{")
	if d := loadDeclarations(broken); d.report.State != "invalid" {
		t.Fatalf("broken = %+v", d.report)
	}
}

// A phrase declaration matches the redacted sample text.
func TestPhraseDeclarationMatchesSampleText(t *testing.T) {
	opts := testMonitorOptions(t)
	opts.states = writeStates(t, `{"match":"spot subscribe delivered no tick","reason":"known feed gap","until":"2026-12-31"}`)
	writeTestFile(t, opts.daemonLog, warn("10:00:00", "regime: SYNTHY spot subscribe delivered no tick")+"\n")
	got, err := run(opts, time.Date(2026, 10, 12, 22, 0, 0, 0, time.UTC))
	if err != nil || got.NeedsAttention || len(got.Daemon.Signals) != 0 || len(got.Daemon.Declared) != 1 {
		t.Fatalf("phrase = %+v %v", got, err)
	}
}

// An item seen yesterday and not today is reported once as resolved; one
// still present is carried with its first-seen time; an incident open at the
// end of a window stays open into the next run until a closing anchor.
func TestCarryForwardResolvesCarriesAndContinuesOpenIncidents(t *testing.T) {
	opts := testMonitorOptions(t)
	lines := []string{
		warn("20:47:04", "Gateway unavailable: IBKR API session lost: EOF (observations=1, duration=0s)"),
		warn("06:00:00", "regime: SYNTHY spot subscribe delivered no tick"),
		warn("06:01:00", "fx rate SYNTH/USD: live resolution failed"),
	}
	// Day 1: the gateway drops at night; two families appear in the morning
	// before it (out of time order is fine for a test of carry state).
	writeTestFile(t, opts.daemonLog, strings.Join(lines, "\n")+"\n")
	first, err := run(opts, time.Date(2026, 10, 12, 22, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	reasons := map[string]string{}
	for _, a := range first.Attention {
		reasons[a.Reason] = a.Item
	}
	if reasons["open_incident"] == "" || reasons["new_family"] == "" {
		t.Fatalf("first attention = %+v", first.Attention)
	}
	open, state := readOpenItems(opts.openItemsPath())
	if state != "loaded" || !slices.Equal(open.Items["daemon:"+connectivityKey].OpenSubjects, []string{"gateway"}) {
		t.Fatalf("open items = %+v %s", open, state)
	}
	if info, _ := os.Stat(opts.openItemsPath()); info.Mode().Perm() != 0o600 {
		t.Fatalf("open items mode = %v", info.Mode())
	}

	// Day 2: the Gateway is still down until it reconnects mid-morning;
	// regime fails again after that, fx is gone.
	next := func(hhmmss, msg string, level string) string {
		return "time=" + day.AddDate(0, 0, 1).Add(at(hhmmss).Sub(day)).Format(time.RFC3339) + " level=" + level + ` msg="` + msg + `"`
	}
	lines = append(lines,
		next("06:00:00", "gamma scheduler: gateway connector unavailable, skipping compute", "WARN"),
		next("09:19:25", "Connected to IB Gateway 127.0.0.1:7496 (clientID=15, tls=false)", "INFO"),
		next("10:00:00", "regime: SYNTHY spot subscribe delivered no tick", "WARN"),
	)
	writeTestFile(t, opts.daemonLog, strings.Join(lines, "\n")+"\n")
	second, err := run(opts, time.Date(2026, 10, 13, 22, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if second.CarryState != "loaded" {
		t.Fatalf("carry state = %s", second.CarryState)
	}
	if len(second.Daemon.Incidents) != 1 {
		t.Fatalf("incidents = %+v", second.Daemon.Incidents)
	}
	gw := second.Daemon.Incidents[0]
	if gw.Kind != "gateway" || gw.Open || gw.Status != "carried" || gw.ClosedBy != "gateway_connected" || !gw.Start.Equal(at("20:47:04")) ||
		gw.Attention != "long_incident" || gw.DependentLines != 1 || gw.Dependents[0].Family != "gamma_scheduler" {
		t.Fatalf("carried gateway incident = %+v", gw)
	}
	if len(second.Daemon.Signals) != 1 || second.Daemon.Signals[0].Family != "regime" || second.Daemon.Signals[0].Status != "carried" ||
		second.Daemon.Signals[0].FirstSeen.IsZero() || second.Daemon.Signals[0].Attention != "" {
		t.Fatalf("signals = %+v", second.Daemon.Signals)
	}
	if len(second.Daemon.Resolved) != 1 || second.Daemon.Resolved[0].Item != "fx_rate" {
		t.Fatalf("resolved = %+v", second.Daemon.Resolved)
	}
	open, _ = readOpenItems(opts.openItemsPath())
	if _, still := open.Items["daemon:"+connectivityKey]; still {
		t.Fatalf("closed incident still open: %+v", open.Items)
	}

	// Day 3: nothing new; regime resolves, reported once, then never again.
	third, err := run(opts, time.Date(2026, 10, 14, 22, 0, 0, 0, time.UTC))
	if err != nil || third.NeedsAttention || len(third.Daemon.Resolved) != 1 || third.Daemon.Resolved[0].Item != "regime" {
		t.Fatalf("third = %+v %v", third.Daemon, err)
	}
	fourth, err := run(opts, time.Date(2026, 10, 15, 22, 0, 0, 0, time.UTC))
	if err != nil || len(fourth.Daemon.Resolved) != 0 {
		t.Fatalf("fourth = %+v %v", fourth.Daemon, err)
	}
	raw, _ := os.ReadFile(opts.openItemsPath())
	if strings.Contains(string(raw), "SYNTH") {
		t.Fatalf("open items hold raw text: %s", raw)
	}
}

// needs_attention follows the rules and nothing else: errors, new families,
// long or open incidents, coverage the first time, and carried items that
// grew. A carried family at its usual rate and a short, closed incident are
// informational.
func TestAttentionRules(t *testing.T) {
	opts := testMonitorOptions(t)
	write := func(lines ...string) {
		raw, _ := os.ReadFile(opts.daemonLog)
		writeTestFile(t, opts.daemonLog, string(raw)+strings.Join(lines, "\n")+"\n")
	}
	runAt := func(d int) report {
		got, err := run(opts, time.Date(2026, 10, 12+d, 22, 0, 0, 0, time.UTC))
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	write(warn("06:00:00", "breadth: fetch SYNTHIDX: upstream slow"))
	if got := runAt(0); len(got.Attention) != 1 || got.Attention[0].Reason != "new_family" || got.Attention[0].Item != "daemon:breadth" {
		t.Fatalf("new family = %+v", got.Attention)
	}
	shift := func(d int, hhmmss string) string {
		return day.AddDate(0, 0, d).Add(at(hhmmss).Sub(day)).Format(time.RFC3339)
	}
	// Day 1: the family at its usual rate and a short, closed backend loss
	// are informational.
	write(
		"time="+shift(1, "06:00:00")+` level=WARN msg="breadth: fetch SYNTHIDX: upstream slow"`,
		"time="+shift(1, "07:00:00")+` level=WARN msg="IBKRConnector: TWS lost connectivity to the IBKR backend (code 1100); waiting for a 1101/1102 restore notice"`,
		"time="+shift(1, "07:00:05")+` level=WARN msg="IBKRConnector: TWS restored connectivity to the IBKR backend after 5s (loss 1 this session, restore_code=1102)"`,
	)
	if got := runAt(1); got.NeedsAttention || got.Daemon.Signals[0].Status != "carried" || len(got.Daemon.Incidents) != 1 {
		t.Fatalf("carried and short = %+v", got)
	}
	// Day 2: the family quadruples; an ERROR appears.
	for range 4 {
		write("time=" + shift(2, "06:00:00") + ` level=WARN msg="breadth: fetch SYNTHIDX: upstream slow"`)
	}
	write("time=" + shift(2, "06:30:00") + ` level=ERROR msg="Client ID 15 already in use; not auto-walking"`)
	got := runAt(2)
	reasons := map[string]string{}
	for _, a := range got.Attention {
		reasons[a.Item] = a.Reason
	}
	if reasons["daemon:breadth"] != "grew" || reasons["daemon:client"] != "error" || len(got.Attention) != 2 {
		t.Fatalf("attention = %+v", got.Attention)
	}
	if !got.NeedsAttention {
		t.Fatal("attention items without needs_attention")
	}
}
