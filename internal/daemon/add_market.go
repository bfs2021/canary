package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"sync"
	"time"

	"github.com/osauer/canary/v2/internal/rpc"
	ibkr "github.com/osauer/canary/v2/pkg/ibkr"
)

func (s *Server) handleAddMarket(ctx context.Context, req *rpc.Request) (*rpc.AddMarketResult, error) {
	var p rpc.AddMarketParams
	if err := decodeParams(req.Params, &p); err != nil {
		return nil, err
	}
	u := p.Underlying
	if u.Symbol == "" || len(u.Symbol) > 32 || u.SecType != "STK" || u.Currency != "USD" || u.Exchange != "SMART" || u.ConID < 0 || u.Expiry != "" || u.Strike != 0 || u.Right != "" {
		return nil, errBadRequest("Add requires an exact SMART USD stock underlying")
	}
	if len(p.Refresh) > 4 || p.Discover && len(p.Refresh) > 0 {
		return nil, errBadRequest("Choose discovery or at most four exact call refreshes")
	}
	seen := map[int]bool{}
	for _, item := range p.Refresh {
		if !slices.Contains([]int{14, 30, 60, 120}, item.Horizon) || seen[item.Horizon] || !validAddCallSelection(item.Contract, u.Symbol) {
			return nil, errBadRequest("Invalid call horizon or identity")
		}
		seen[item.Horizon] = true
	}
	c := s.gatewayConnector()
	if c == nil {
		return nil, s.gatewayUnavailableError()
	}
	binding, ok := c.CaptureHistoricalSession()
	if !ok {
		return nil, s.gatewayUnavailableError()
	}
	exact, err := c.ResolveOrderContractForSession(ctx, binding, ibkr.Contract{ConID: u.ConID, Symbol: u.Symbol, SecType: "STK", Currency: "USD", Exchange: "SMART"}, 8*time.Second)
	if err != nil {
		return nil, err
	}
	if exact.Contract.Symbol != u.Symbol || exact.Contract.SecType != "STK" || exact.Contract.Currency != "USD" || u.ConID > 0 && exact.Contract.ConID != u.ConID {
		return nil, errBadRequest("underlying identity changed")
	}
	// Keep the exact underlying subscription alive while TWS computes option models.
	if p.Discover || p.Selected != nil || len(p.Refresh) > 0 {
		key, err := c.SubscribeMarketDataWithContractForSession(ctx, binding, exact.Contract, defaultGenericTicks)
		if err != nil {
			return nil, err
		}
		defer func() {
			cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_ = c.UnsubscribeMarketDataForSession(cleanup, binding, key)
		}()
	}
	out := &rpc.AddMarketResult{Version: 1, Underlying: setupOptionContract(exact.Contract)}
	raw, _ := json.Marshal(rpc.QuoteSnapshotParams{Contract: out.Underlying, TimeoutMs: 3000})
	out.Quote, err = s.handleQuoteSnapshot(ctx, &rpc.Request{Params: raw})
	if err != nil {
		return nil, err
	}
	out.PriceIncrements = addPriceRules(ctx, c, binding, exact)
	if p.Selected != nil {
		requested := *p.Selected
		if !validAddCallSelection(requested, u.Symbol) {
			return nil, errBadRequest("choose an exact standard call")
		}
		row := s.addCallMarket(ctx, c, binding, exact.Contract, requested.Expiry, requested.Strike)
		if row.Contract != nil && !sameAddCall(*row.Contract, requested) {
			return nil, errBadRequest("selected call identity changed")
		}
		out.Selected = &row
	}
	if len(p.Refresh) > 0 {
		out.Calls = make([]rpc.AddCallCandidate, len(p.Refresh))
		addParallel(len(p.Refresh), func(i int) {
			item := p.Refresh[i]
			row := s.addCallMarket(ctx, c, binding, exact.Contract, item.Contract.Expiry, item.Contract.Strike)
			if row.Contract != nil && !sameAddCall(*row.Contract, item.Contract) {
				row = rpc.AddCallCandidate{Unavailable: "Call identity changed"}
			}
			row.Horizon = item.Horizon
			_, row.DTE = addHorizonExpiry([]rpc.SetupOptionExpiry{{Date: item.Contract.Expiry}}, item.Horizon, 8, 150, time.Now())
			out.Calls[i] = row
		})
	}
	if p.Discover {
		listing, err := c.FetchSetupOptionStrikes(ctx, binding, exact.Contract, 10*time.Second)
		if err != nil {
			return nil, err
		}
		expiries, _ := setupOptionExpiries(listing, time.Now())
		spot, _ := quoteCurrentPrice(out.Quote)
		out.Calls = make([]rpc.AddCallCandidate, 4)
		addParallel(4, func(i int) {
			target := []int{14, 30, 60, 120}[i]
			bounds := [][2]int{{8, 21}, {22, 45}, {46, 90}, {91, 150}}[i]
			date, dte := addHorizonExpiry(expiries, target, bounds[0], bounds[1], time.Now())
			row := rpc.AddCallCandidate{Horizon: target, DTE: dte, Unavailable: "No eligible call"}
			if date != "" && spot != nil && *spot > 0 {
				strikes, err := setupListedStrikes(listing, u.Symbol, date)
				if err == nil {
					slices.SortFunc(strikes, func(a, b float64) int { return compareDistance(a, b, *spot) })
					for _, k := range strikes[:min(3, len(strikes))] {
						candidate := s.addCallMarket(ctx, c, binding, exact.Contract, date, k)
						candidate.Horizon, candidate.DTE = target, dte
						if eligibleAddCall(candidate, time.Now()) {
							row = candidate
							break
						}
					}
				}
			}
			out.Calls[i] = row
		})
	}
	if !c.HistoricalSessionCurrent(binding) {
		return nil, fmt.Errorf("broker session changed during Add discovery")
	}
	out.AsOf = time.Now()
	return out, nil
}
func compareDistance(a, b, spot float64) int {
	da, db := math.Abs(a-spot), math.Abs(b-spot)
	if da < db || da == db && a < b {
		return -1
	}
	if da > db || a > b {
		return 1
	}
	return 0
}
func addHorizonExpiry(expiries []rpc.SetupOptionExpiry, target, minDTE, maxDTE int, now time.Time) (string, int) {
	loc, _ := time.LoadLocation("America/New_York")
	day := now.In(loc).Format("20060102")
	today, _ := time.Parse("20060102", day)
	best, dte, distance := "", 0, 10000
	for _, e := range expiries {
		date, err := time.Parse("20060102", e.Date)
		if err != nil {
			continue
		}
		n := int(date.Sub(today).Hours() / 24)
		delta := int(math.Abs(float64(n - target)))
		if n >= minDTE && n <= maxDTE && (delta < distance || delta == distance && e.Date < best) {
			best, dte, distance = e.Date, n, delta
		}
	}
	return best, dte
}
func addPriceRules(ctx context.Context, c *ibkr.Connector, binding ibkr.ConnectorSessionBinding, exact ibkr.ResolvedOrderContract) []rpc.SetupOptionPriceIncrement {
	rules, err := c.OrderPriceIncrements(ctx, binding, exact)
	if err != nil {
		return nil
	}
	out := make([]rpc.SetupOptionPriceIncrement, 0, len(rules))
	for _, r := range rules {
		out = append(out, rpc.SetupOptionPriceIncrement{LowEdge: r.LowEdge, Increment: r.Increment})
	}
	return out
}
func (s *Server) addCallMarket(ctx context.Context, c *ibkr.Connector, binding ibkr.ConnectorSessionBinding, under ibkr.Contract, expiry string, strike float64) rpc.AddCallCandidate {
	row := rpc.AddCallCandidate{Unavailable: "Exact call evidence unavailable"}
	parsed, err := time.Parse("20060102", expiry)
	if err != nil || parsed.Before(time.Now().Add(7*24*time.Hour)) || !positiveFinite(strike) {
		return row
	}
	requested := ibkr.Contract{Symbol: under.Symbol, SecType: "OPT", Exchange: "SMART", Currency: "USD", Expiry: expiry, Strike: strike, Right: "C", Multiplier: 100, TradingClass: under.Symbol}
	exact, err := c.ResolveOrderContractForSession(ctx, binding, requested, 5*time.Second)
	if err != nil || ibkr.ValidateSetupCallResolution(exact, under, expiry, strike) != nil {
		return row
	}
	contract := setupOptionContract(exact.Contract)
	contract.MinTick = exact.MinTick
	row.Contract = &contract
	row.PriceIncrements = addPriceRules(ctx, c, binding, exact)
	row.Quote = s.setupCallQuote(ctx, c, binding, contract)
	if eligibleAddCall(row, time.Now()) {
		row.Unavailable = ""
	}
	return row
}
func eligibleAddCall(row rpc.AddCallCandidate, now time.Time) bool {
	q := row.Quote
	return row.Contract != nil && len(row.PriceIncrements) > 0 && q != nil && q.Status == rpc.SetupQuoteQuoted && q.DataType == rpc.MarketDataLive && q.ModelDataType == rpc.MarketDataLive && q.Delta != nil && *q.Delta >= .5 && *q.Delta <= .7 && q.IV != nil && *q.IV > 0 && !q.AsOf.IsZero() && !q.ModelAsOf.IsZero() && !q.AsOf.After(now.Add(time.Second)) && !q.ModelAsOf.After(now.Add(time.Second)) && now.Sub(q.AsOf) <= 15*time.Second && now.Sub(q.ModelAsOf) <= 15*time.Second
}

// Two active reads bound TWS subscription pressure while refreshing the comparison.
func addParallel(n int, read func(int)) {
	var wg sync.WaitGroup
	slots := make(chan struct{}, 2)
	for i := range n {
		wg.Go(func() { ; slots <- struct{}{}; defer func() { <-slots }(); read(i) })
	}
	wg.Wait()
}
func validAddCallSelection(c rpc.ContractParams, symbol string) bool {
	return c.Symbol == symbol && c.SecType == "OPT" && c.Right == "C" && c.Multiplier == 100 && c.ConID > 0 && c.Currency == "USD" && c.Exchange == "SMART" && c.TradingClass != "" && positiveFinite(c.Strike)
}
func sameAddCall(a, b rpc.ContractParams) bool {
	return a.ConID == b.ConID && a.Symbol == b.Symbol && a.SecType == b.SecType && a.Expiry == b.Expiry && a.Strike == b.Strike && a.Right == b.Right && a.TradingClass == b.TradingClass && a.Multiplier == b.Multiplier && a.Currency == b.Currency && a.Exchange == b.Exchange
}
