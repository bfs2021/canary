package daemon

import (
	"testing"
	"time"
)

func TestMandateContinuousPnLCrossingZeroIsNotResetEvidence(t *testing.T) {
	st := newTestRiskCapitalStore(t)
	st.now = func() time.Time { return mandateTestNow }
	c := testMandateConstitution()
	reconcileNow(t, st)
	// Both observations are in the middle of the same US session. P&L is
	// continuous: earlier gains are subsequently lost, offset by a deposit.
	observeWithPnL(st, c, 300000, 20000, mandateTestNow.Add(-5*time.Minute))
	observeWithPnL(st, c, 300500, 500, mandateTestNow.Add(-time.Minute))
	got, _ := st.MandateCapitalForScope(c, brokerStateScope{}, mandateTestNow)
	t.Logf("same-session P&L 20000 -> 500, equity 300000 -> 300500, undeclared deposit=20000: capital=%+v suspect=%+v", got, st.state.Mandate.FlowSuspect)
	if got.Known || st.state.Mandate.FlowSuspect == nil {
		t.Fatal("valid intraday P&L passing near zero was mistaken for a reset and an offsetting +20000 deposit was missed")
	}
}

func TestMandateActualSessionDateResetStillWorks(t *testing.T) {
	for _, tc := range []struct {
		name    string
		deposit float64
		known   bool
	}{
		{"reset with genuine small new-day gain", 0, true},
		{"reset plus undeclared deposit", 20000, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := newTestRiskCapitalStore(t)
			st.now = func() time.Time { return mandateTestNow }
			c := testMandateConstitution()
			reconcileNow(t, st)
			observeWithPnL(st, c, 300000, 20000, mandateTestNow.Add(-24*time.Hour))
			observeWithPnL(st, c, 300500+tc.deposit, 500, mandateTestNow.Add(-time.Minute))
			got, _ := st.MandateCapitalForScope(c, brokerStateScope{}, mandateTestNow)
			if got.Known != tc.known {
				t.Fatalf("new session daily P&L must be its own step, while extra flow remains unexplained: %+v", got)
			}
		})
	}
}
