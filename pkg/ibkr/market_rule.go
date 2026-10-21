package ibkr

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// PriceIncrement is one broker-supplied price band for an exact route.
type PriceIncrement struct {
	LowEdge   float64 `json:"low_edge"`
	Increment float64 `json:"increment"`
}

func routeMarketRule(r ResolvedOrderContract) (int, error) {
	venues, ids := strings.Split(r.ValidExchanges, ","), strings.Split(r.MarketRuleIDs, ",")
	if len(venues) != len(ids) {
		return 0, fmt.Errorf("route price rules unavailable")
	}
	found := 0
	for i, v := range venues {
		if strings.TrimSpace(v) != r.Contract.Exchange {
			continue
		}
		id, err := strconv.Atoi(strings.TrimSpace(ids[i]))
		if err != nil || id <= 0 || found != 0 && found != id {
			return 0, fmt.Errorf("route price rule ambiguous")
		}
		found = id
	}
	if found == 0 {
		return 0, fmt.Errorf("route price rule unavailable")
	}
	return found, nil
}
func decodePriceIncrements(fields []string, id int) ([]PriceIncrement, error) {
	invalid := fmt.Errorf("invalid market price rule")
	if len(fields) < 3 || fields[0] != strconv.Itoa(msgMarketRule) || fields[1] != strconv.Itoa(id) {
		return nil, invalid
	}
	n, err := strconv.Atoi(fields[2])
	if err != nil || n < 1 || n > 128 || len(fields) != 3+n*2 {
		return nil, invalid
	}
	out := make([]PriceIncrement, 0, n)
	for i := range n {
		edge, e1 := strconv.ParseFloat(fields[3+2*i], 64)
		inc, e2 := strconv.ParseFloat(fields[4+2*i], 64)
		if e1 != nil || e2 != nil || math.IsNaN(edge) || math.IsInf(edge, 0) || edge < 0 || math.IsNaN(inc) || math.IsInf(inc, 0) || inc <= 0 || i == 0 && edge != 0 || i > 0 && edge <= out[i-1].LowEdge {
			return nil, invalid
		}
		out = append(out, PriceIncrement{LowEdge: edge, Increment: inc})
	}
	return out, nil
}

// OrderPriceIncrements reads the route's complete price grid on the same
// broker session that resolved its contract. Missing rules never become .01.
func (c *Connector) OrderPriceIncrements(ctx context.Context, binding ConnectorSessionBinding, r ResolvedOrderContract) ([]PriceIncrement, error) {
	if !c.SessionCurrent(binding) || r.Contract.ConID <= 0 {
		return nil, fmt.Errorf("exact broker session required")
	}
	id, err := routeMarketRule(r)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	type result struct {
		rows []PriceIncrement
		err  error
	}
	done := make(chan result, 1)
	conn := binding.connection
	handler := conn.RegisterHandlerAtEpoch(msgMarketRule, func(fields []string, epoch uint64) {
		if epoch != binding.epoch || len(fields) < 2 || fields[1] != strconv.Itoa(id) {
			return
		}
		rows, err := decodePriceIncrements(fields, id)
		select {
		case done <- result{rows, err}:
		default:
		}
	})
	defer conn.UnregisterHandler(msgMarketRule, handler)
	// reqMarketRule(91) carries only the rule ID; the reply(93) is rule ID,
	// count, then lowEdge/increment pairs (TWS socket API).
	if err := conn.sendMessageWithTypeContextForEpoch(ctx, conn.encodeMsg(91, id), RequestTypeGeneral, binding.epoch, true); err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case out := <-done:
		if !c.SessionCurrent(binding) {
			return nil, fmt.Errorf("broker session changed during price rule read")
		}
		return out.rows, out.err
	}
}
