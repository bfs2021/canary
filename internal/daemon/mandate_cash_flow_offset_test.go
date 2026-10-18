package daemon

import (
	"encoding/json"
	"testing"
	"time"
)

// The broker reports valid same-session P&L. Between observations a real cash
// flow and market return offset; no statement/declaration has accounted for it.
func TestMandateOffsettingFlowMustRemainUnknown(t *testing.T) {
	for _, tc := range []struct {
		name                string
		cashFlow, marketPnL float64
	}{
		{"deposit offsets a loss exactly", 20000, -20000},
		{"deposit offsets most of a loss", 20000, -19000},
		{"withdrawal offsets a gain exactly", -20000, 20000},
		{"withdrawal offsets most of a gain", -20000, 19000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, c, at, capital := mandateFlowTestStore(t)
			const startEquity = 300000.0
			equity := startEquity + tc.cashFlow + tc.marketPnL
			observeWithPnL(st, c, equity, tc.marketPnL, at.Add(59*time.Minute))
			got := capital()
			t.Logf("synthetic flow=%g market_pnl=%g net_equity_move=%g residual=%g capital=%+v", tc.cashFlow, tc.marketPnL, equity-startEquity, equity-startEquity-tc.marketPnL, got)
			if got.Known || st.state.Mandate.FlowSuspect == nil {
				t.Fatalf("undeclared flow above threshold silently admitted after offsetting P&L: known=%v suspect=%+v", got.Known, st.state.Mandate.FlowSuspect)
			}
			observeWithPnL(st, c, equity, tc.marketPnL, at.Add(time.Hour))
			if got = capital(); got.Known || st.state.Mandate.FlowSuspect == nil {
				t.Fatalf("unresolved flow silently cleared by an unchanged follow-up: %+v", got)
			}
		})
	}
}

// Before 941b1e7d, ALL persisted real-flow suspicions lack move_base. Upgrade
// must not infer a false positive merely from a later offsetting market move.
func TestMandateLegacyRealDepositMustRemainUnknown(t *testing.T) {
	st, c, at, capital := mandateFlowTestStore(t)
	observeWithPnL(st, c, 320000, 0, at.Add(55*time.Minute))
	if got := capital(); got.Known || st.state.Mandate.FlowSuspect == nil {
		t.Fatalf("fixture must first hold genuine +20000 deposit: %+v", got)
	}
	st.mu.Lock()
	encoded, err := json.Marshal(st.state.Mandate)
	st.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	var persisted map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &persisted); err != nil {
		t.Fatal(err)
	}
	var suspect map[string]json.RawMessage
	if err := json.Unmarshal(persisted["flow_suspect"], &suspect); err != nil {
		t.Fatal(err)
	}
	delete(suspect, "move_base") // exact legacy wire shape, not a made-up value
	persisted["flow_suspect"], err = json.Marshal(suspect)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err = json.Marshal(persisted)
	if err != nil {
		t.Fatal(err)
	}
	var loaded mandateCapitalStateV1
	if err := json.Unmarshal(encoded, &loaded); err != nil {
		t.Fatal(err)
	}
	st.mu.Lock()
	st.state.Mandate = &loaded
	st.mu.Unlock()
	observeWithPnL(st, c, 300000, -20000, at.Add(59*time.Minute))
	got := capital()
	t.Logf("after genuine -20000 market move: capital=%+v suspect=%+v", got, st.state.Mandate.FlowSuspect)
	if got.Known || st.state.Mandate.FlowSuspect == nil {
		t.Fatal("upgrade cleared a genuine legacy deposit solely because a later loss returned equity to its base; no declaration or statement explained +20000 flow")
	}
	if st.state.Mandate.FlowSuspect.Unexplained != 20000 {
		t.Fatalf("residual must still be the unaccounted deposit: %+v", st.state.Mandate.FlowSuspect)
	}
}
