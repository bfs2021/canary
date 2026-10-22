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
	Planning        *AddCallPlanning            `json:"planning,omitempty"`
}

// AddCallPlanning is closed-session context, never live selection or order
// evidence. ObservedAt is when the source was received, not an exchange time;
// a broker midpoint receipt never acquires a session date by inference.
type AddCallPlanning struct {
	Price       *float64  `json:"price,omitempty"`
	PriceSource string    `json:"price_source,omitempty"` // broker_midpoint
	ObservedAt  time.Time `json:"observed_at,omitzero"`
	DeltaStatus string    `json:"delta_status"` // in_range | unavailable | out_of_range
}

// AddMarketResult contains observations, never order authority or risk capacity.
type AddMarketResult struct {
	Version            int                         `json:"version"`
	Underlying         ContractParams              `json:"underlying"`
	Quote              *Quote                      `json:"quote,omitempty"`
	PriceIncrements    []SetupOptionPriceIncrement `json:"price_increments,omitempty"`
	Calls              []AddCallCandidate          `json:"calls,omitempty"`
	Selected           *AddCallCandidate           `json:"selected,omitempty"`
	AsOf               time.Time                   `json:"as_of"`
	OptionsSession     MarketSession               `json:"options_session"`
	OptionsLastSession *MarketSession              `json:"options_last_session,omitempty"`
	OptionMode         string                      `json:"option_mode"` // live | planning | unavailable
}
