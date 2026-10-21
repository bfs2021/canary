package rpc

import "time"

// MethodAddMarket is bounded, read-only discovery for the owner's Add composer.
const MethodAddMarket = "add.market"

// AddMarketParams identifies an underlying and optionally an exact selected call.
type AddMarketParams struct {
	Underlying ContractParams     `json:"underlying"`
	Selected   *ContractParams    `json:"selected,omitempty"`
	Refresh    []AddCallSelection `json:"refresh,omitempty"`
	Discover   bool               `json:"discover,omitempty"`
}

// AddCallSelection refreshes one previously resolved horizon without re-ranking.
type AddCallSelection struct {
	Horizon  int            `json:"horizon_days"`
	Contract ContractParams `json:"contract"`
}

// AddCallCandidate is a horizon and its eligible exact call, or its absence.
type AddCallCandidate struct {
	Horizon         int                         `json:"horizon_days"`
	DTE             int                         `json:"dte,omitempty"`
	Contract        *ContractParams             `json:"contract,omitempty"`
	Quote           *SetupOptionQuote           `json:"quote,omitempty"`
	PriceIncrements []SetupOptionPriceIncrement `json:"price_increments,omitempty"`
	Unavailable     string                      `json:"unavailable,omitempty"`
}

// AddMarketResult contains observations, never order authority or risk capacity.
type AddMarketResult struct {
	Version         int                         `json:"version"`
	Underlying      ContractParams              `json:"underlying"`
	Quote           *Quote                      `json:"quote,omitempty"`
	PriceIncrements []SetupOptionPriceIncrement `json:"price_increments,omitempty"`
	Calls           []AddCallCandidate          `json:"calls,omitempty"`
	Selected        *AddCallCandidate           `json:"selected,omitempty"`
	AsOf            time.Time                   `json:"as_of"`
}
