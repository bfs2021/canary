package daemon

import (
	"context"
	"github.com/osauer/canary/v2/internal/rpc"
	"reflect"
	"testing"
	"time"
)

func TestCallRoundingUsesFeeInclusiveQuantityAndRechecksRoundedLot(t *testing.T) {
	for _, tc := range []struct {
		name        string
		budget, fee float64
		round       bool
		want        int
		checked     []int
		over        bool
	}{
		{"nine up to ten", 5000, 12, true, 10, []int{9, 10}, true},
		{"six down to five", 3600, 12, true, 5, []int{6, 5}, false},
		{"fee removes tenth then explicit round restores it", 5500, 12, true, 10, []int{10, 9, 10}, true},
		{"unrounded budget keeps nine", 5500, 12, false, 9, []int{10, 9}, false},
		{"one remains one", 600, 12, true, 1, []int{1}, false},
		{"two remain two", 1200, 12, true, 2, []int{2}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := rpc.AddCallPlanParams{Contract: rpc.ContractParams{ConID: 10001, Symbol: "SYNX", SecType: "OPT", Exchange: "SMART", Currency: "USD", Right: "C", Multiplier: 100, Strike: 80, Expiry: "20261218", TradingClass: "SYNX"}, LimitPrice: 5.5, Budget: tc.budget, RoundSize: tc.round}
			var checked []int
			result, err := planCallQuantity(t.Context(), p, func(_ context.Context, p rpc.OrderPreviewParams) (*rpc.OrderPreviewResult, error) {
				checked = append(checked, p.Quantity)
				return &rpc.OrderPreviewResult{Draft: rpc.OrderDraft{Action: p.Action, OrderType: p.OrderType, Contract: p.Contract, Quantity: p.Quantity, LimitPrice: *p.LimitPrice}, WhatIf: rpc.OrderWhatIfResult{Status: rpc.OrderWhatIfStatusAccepted, Available: true, Margin: &rpc.OrderMarginImpact{MaxCommission: new(tc.fee), CommissionCurrency: "USD"}}}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if result.Quantity != tc.want || result.OverBudget != tc.over || result.Debit != float64(tc.want)*550+tc.fee || !reflect.DeepEqual(checked, tc.checked) {
				t.Fatalf("result=%+v checked=%v", result, checked)
			}
			if time.Since(result.AsOf) > time.Second {
				t.Fatal("missing current receipt")
			}
		})
	}
}

func TestCallSizingRejectsUnknownFeesAndChangedContract(t *testing.T) {
	p := rpc.AddCallPlanParams{Contract: rpc.ContractParams{ConID: 10001, Symbol: "SYNX", SecType: "OPT", Exchange: "SMART", Currency: "USD", Right: "C", Multiplier: 100, Strike: 80, Expiry: "20261218", TradingClass: "SYNX"}, LimitPrice: 5.5, Budget: 5000, RoundSize: true}
	for _, changed := range []bool{false, true} {
		_, err := planCallQuantity(t.Context(), p, func(_ context.Context, in rpc.OrderPreviewParams) (*rpc.OrderPreviewResult, error) {
			r := &rpc.OrderPreviewResult{Draft: rpc.OrderDraft{Action: in.Action, OrderType: in.OrderType, Contract: in.Contract, Quantity: in.Quantity, LimitPrice: *in.LimitPrice}}
			if changed {
				r.WhatIf = rpc.OrderWhatIfResult{Status: rpc.OrderWhatIfStatusAccepted, Available: true, Margin: &rpc.OrderMarginImpact{MaxCommission: new(12.0), CommissionCurrency: "USD"}}
				r.Draft.Contract.ConID++
			}
			return r, nil
		})
		if err == nil {
			t.Fatalf("accepted changed=%v", changed)
		}
	}
}
