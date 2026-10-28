package daemon

import (
	"github.com/osauer/canary/v2/internal/mandate"
	"strings"
	"testing"
)

func TestMandatePartialStopsCountCoveredLoss(t *testing.T) {
	for _, tc := range []struct {
		name           string
		quantity, stop float64
	}{{"long", 100, 90}, {"short", -100, 110}} {
		t.Run(tc.name, func(t *testing.T) {
			row := mandateReductionStock("SYNA", 9001, tc.quantity, 100)
			pos := mandateReductionBook(row)
			in := *mandateReductionFixture(t, pos, 0.5, 100000, 0).Input
			in.Legs, _ = mandateApplyStops(in.Legs, pos, "EUR", map[string][]mandateArmedStop{mandateLegID(row): {{StopPrice: tc.stop, Quantity: 50}}}, "")
			if in.Legs[0].StopArmed {
				t.Fatal("partial cover must not read fully armed")
			}
			v := mandate.Evaluate(in)
			if v.Loss == nil || v.Loss.StopLossBase != 500 || v.Loss.TotalBase != 500 {
				t.Fatalf("partial stop loss = %+v, want 500", v.Loss)
			}
			if len(v.Loss.Unstopped) != 1 || !strings.Contains(v.Loss.Unstopped[0], "50 of 100 shares stopped") {
				t.Fatalf("uncovered residual not disclosed: %+v", v.Loss.Unstopped)
			}
			if len(v.Legs) != 1 || v.Legs[0].LossBase != 500 {
				t.Fatalf("leg loss = %+v", v.Legs)
			}
		})
	}
}
