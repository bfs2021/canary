package daemon

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/osauer/canary/v2/internal/rpc"
)

func (s *Server) handleAddCallPlan(ctx context.Context, req *rpc.Request) (*rpc.AddCallPlanResult, error) {
	var p rpc.AddCallPlanParams
	if err := decodeParams(req.Params, &p); err != nil {
		return nil, err
	}
	c := p.Contract
	date, err := time.Parse("20060102", c.Expiry)
	if err != nil || !date.After(time.Now().Add(7*24*time.Hour)) || c.ConID <= 0 || c.SecType != "OPT" || c.Currency != "USD" || c.Exchange != "SMART" || c.Right != "C" || c.Multiplier != 100 || c.TradingClass == "" || !positiveFinite(p.Budget) || p.Budget > 1e9 || !positiveFinite(p.LimitPrice) {
		return nil, errBadRequest("Choose an exact long call, entry limit and positive budget")
	}
	return planCallQuantity(ctx, p, s.previewOrder)
}

// Every candidate, including a rounded-up lot, receives its own exact broker
// preview. Rounding is an explicit draft preference, never spending authority.
func planCallQuantity(ctx context.Context, p rpc.AddCallPlanParams, previewOrder func(context.Context, rpc.OrderPreviewParams) (*rpc.OrderPreviewResult, error)) (*rpc.AddCallPlanResult, error) {
	c := p.Contract
	n := int(math.Min(1000000, math.Floor(p.Budget/(p.LimitPrice*100))))
	rounded := false
	for attempt := 0; attempt < 4 && n > 0; attempt++ {
		preview, err := previewOrder(ctx, rpc.OrderPreviewParams{Action: rpc.OrderActionBuy, Contract: c, Quantity: n, OrderType: "LMT", LimitPrice: new(p.LimitPrice), Strategy: "explicit-limit", TIF: "DAY", AdaptivePriority: "Normal"})
		if err != nil {
			return nil, err
		}
		fee, ok := cashSweepFeeUpper(preview, c.Currency)
		if !ok {
			return nil, fmt.Errorf("an accepted broker fee bound is required to size this call")
		}
		d := preview.Draft
		if d.Action != rpc.OrderActionBuy || d.OrderType != "LMT" || d.Contract.SecType != "OPT" || d.Contract.Right != "C" || d.Contract.Multiplier != 100 || d.Contract.Currency != c.Currency || d.Contract.Exchange != c.Exchange || d.Contract.ConID != c.ConID || d.Contract.Symbol != c.Symbol || d.Contract.Strike != c.Strike || d.Contract.Expiry != c.Expiry || d.Contract.TradingClass != c.TradingClass || d.Quantity != n || d.LimitPrice != p.LimitPrice {
			return nil, fmt.Errorf("call identity or terms changed during sizing")
		}
		debit := float64(n)*p.LimitPrice*100 + fee
		if !positiveFinite(debit) {
			return nil, fmt.Errorf("call debit is not finite and positive")
		}
		if debit <= p.Budget || rounded {
			if p.RoundSize && !rounded {
				next := roundCallQuantity(n)
				if next != n {
					n = next
					rounded = true
					continue
				}
			}
			return &rpc.AddCallPlanResult{RoundSize: p.RoundSize, OverBudget: debit > p.Budget, Contract: d.Contract, LimitPrice: p.LimitPrice, Budget: p.Budget, Quantity: n, FeeBound: fee, Debit: debit, AsOf: time.Now()}, nil
		}
		next := int(math.Floor((p.Budget - fee) / (p.LimitPrice * 100)))
		n = min(n-1, next)
	}
	return nil, fmt.Errorf("no checked call quantity fits this budget")
}

func roundCallQuantity(n int) int {
	if n < 3 {
		return n
	}
	return ((n + 2) / 5) * 5
}
