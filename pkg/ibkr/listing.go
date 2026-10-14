package ibkr

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Listings: every listing IBKR knows for one security, asked by ISIN, by
// contract id, or by symbol on named venues, for a read-only resolver
// (Canary's `canary core resolve`). Unlike the order and chart resolution
// it never picks one line: it asks every form, keeps every line that does
// not contradict the ISIN asked, and returns them all with the trace of
// each form's answer, so a caller shows the owner every candidate and the
// owner writes the contract id.

// ListingVenue is one exchange (and optional primary exchange) a symbol
// fallback is asked on.
type ListingVenue struct {
	Exchange    string
	PrimaryExch string
}

// ListingRequest names a security: an ISIN, or a contract id, and symbol
// fallbacks asked on each venue. Currency, when set, is sent with every
// form. SecType defaults to STK (IBKR lists ETFs as STK).
type ListingRequest struct {
	ISIN     string
	ConID    int
	Symbols  []string
	Venues   []ListingVenue
	Currency string
	SecType  string
}

// Listing is one contract IBKR answered: its identity, its venue, its long
// name and the ISIN its frame named (empty when the frame named none), and
// the request forms that found it.
type Listing struct {
	ConID          int
	Symbol         string
	LocalSymbol    string
	SecType        string
	Exchange       string
	PrimaryExch    string
	Currency       string
	TradingClass   string
	LongName       string
	ISIN           string
	ValidExchanges string
	MinTick        float64
	Forms          []string
}

// ErrNoListing means no request form found a line for the request.
var ErrNoListing = errors.New("no listing for the request")

// forms are the reqContractDetails contracts for the request, in order: by
// contract id on each venue (or on its own when none is named); by ISIN on
// SMART and with no exchange; then each symbol on each venue.
func (r ListingRequest) forms() ([]bondWireForm, error) {
	secType := strings.ToUpper(strings.TrimSpace(r.SecType))
	if secType == "" {
		secType = "STK"
	}
	ccy := strings.ToUpper(strings.TrimSpace(r.Currency))
	if ccy != "" && !bondCurrencyCode(ccy) {
		return nil, fmt.Errorf("listing request currency %q is not a three-letter code", r.Currency)
	}
	isin := strings.ToUpper(strings.TrimSpace(r.ISIN))
	if isin != "" && !ValidISIN(isin) {
		return nil, fmt.Errorf("listing request ISIN %q fails its check digit", r.ISIN)
	}
	venues := make([]ListingVenue, 0, len(r.Venues))
	for _, v := range r.Venues {
		venues = append(venues, ListingVenue{Exchange: strings.ToUpper(strings.TrimSpace(v.Exchange)), PrimaryExch: strings.ToUpper(strings.TrimSpace(v.PrimaryExch))})
	}
	venueLabel := func(v ListingVenue) string {
		label := nonEmptyVenue(v.Exchange)
		if v.PrimaryExch != "" {
			label += " (primary " + v.PrimaryExch + ")"
		}
		return label
	}
	var forms []bondWireForm
	if r.ConID > 0 {
		if len(venues) == 0 {
			venues = []ListingVenue{{}}
		}
		for _, v := range venues {
			forms = append(forms, bondWireForm{label: fmt.Sprintf("%s contract id %d on %s", secType, r.ConID, venueLabel(v)),
				contract: Contract{ConID: r.ConID, SecType: secType, Exchange: v.Exchange, PrimaryExch: v.PrimaryExch, Currency: ccy}})
		}
	}
	if isin != "" {
		forms = append(forms,
			bondWireForm{label: secType + " by ISIN on SMART", contract: Contract{SecType: secType, Exchange: "SMART", Currency: ccy, SecIDType: BondIdentifierISIN, SecID: isin}},
			bondWireForm{label: secType + " by ISIN, no exchange", contract: Contract{SecType: secType, Currency: ccy, SecIDType: BondIdentifierISIN, SecID: isin}})
	}
	for _, raw := range r.Symbols {
		sym := strings.ToUpper(strings.TrimSpace(raw))
		if sym == "" {
			continue
		}
		for _, v := range venues {
			forms = append(forms, bondWireForm{label: fmt.Sprintf("%s %s on %s", secType, sym, venueLabel(v)),
				contract: Contract{Symbol: sym, SecType: secType, Exchange: v.Exchange, PrimaryExch: v.PrimaryExch, Currency: ccy}})
		}
	}
	if len(forms) == 0 {
		return nil, fmt.Errorf("listing request needs an ISIN, a contract id or a symbol on a venue")
	}
	return forms, nil
}

func nonEmptyVenue(exchange string) string {
	if exchange == "" {
		return "no exchange"
	}
	return exchange
}

// Listings asks the connected gateway every form of the request, one
// reqContractDetails each, and returns every listing found, by contract id
// in the order first seen. A line whose frame names another ISIN than the
// one asked is dropped (a symbol is free text and can name another
// security); a line naming none is kept with an empty ISIN. A form the
// gateway answers without a line, or rejects, is recorded and the next
// asked; a timeout or a changed session ends the lookup. No listing at all
// is ErrNoListing, with every answer in the trace.
func (c *Connector) Listings(ctx context.Context, request ListingRequest, timeout time.Duration) ([]Listing, *BondLookupTrace, error) {
	forms, err := request.forms()
	if err != nil {
		return nil, nil, err
	}
	binding, ok := c.CaptureSession()
	if !ok {
		return nil, nil, ErrIBKRUnavailable
	}
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	isin := strings.ToUpper(strings.TrimSpace(request.ISIN))
	trace := &BondLookupTrace{Request: listingDescription(request), ServerVersion: binding.connection.serverVersion}
	var out []Listing
	index := map[int]int{}
	for _, form := range forms {
		lines, attempt, err := c.bondContractDetailsOnce(ctx, binding, form, timeout)
		if err != nil {
			attempt, miss := bondLookupMiss(attempt, err)
			trace.Attempts = append(trace.Attempts, attempt)
			if !miss {
				return out, trace, err
			}
			continue
		}
		kept := 0
		for _, d := range stampAsked(lines, form.contract) {
			named := d.ISIN()
			if isin != "" && named != "" && named != isin {
				continue
			}
			kept++
			if i, seen := index[d.ConID]; seen {
				out[i].Forms = append(out[i].Forms, form.label)
				if out[i].ISIN == "" {
					out[i].ISIN = named
				}
				if out[i].LongName == "" {
					out[i].LongName = d.LongName
				}
				if out[i].PrimaryExch == "" {
					out[i].PrimaryExch = d.PrimaryExch
				}
				continue
			}
			index[d.ConID] = len(out)
			out = append(out, Listing{ConID: d.ConID, Symbol: d.Symbol, LocalSymbol: d.LocalSymbol, SecType: d.SecType, Exchange: d.Exchange, PrimaryExch: d.PrimaryExch,
				Currency: d.Currency, TradingClass: d.TradingClass, LongName: d.LongName, ISIN: named, ValidExchanges: d.ValidExchanges, MinTick: d.MinTick, Forms: []string{form.label}})
		}
		attempt.Lines = kept
		if kept > 0 {
			attempt.Outcome = BondAttemptLine
		} else {
			attempt.Outcome, attempt.Message = BondAttemptNoLine, fmt.Sprintf("the %d lines IBKR answered name another ISIN", len(lines))
		}
		trace.Attempts = append(trace.Attempts, attempt)
		if !c.SessionCurrent(binding) {
			return out, trace, fmt.Errorf("broker session changed during the listing lookup")
		}
	}
	if len(out) == 0 {
		return nil, trace, fmt.Errorf("%w: %s", ErrNoListing, trace.Request)
	}
	return out, trace, nil
}

func listingDescription(r ListingRequest) string {
	var parts []string
	if r.ISIN != "" {
		parts = append(parts, "ISIN "+strings.ToUpper(strings.TrimSpace(r.ISIN)))
	}
	if r.ConID > 0 {
		parts = append(parts, fmt.Sprintf("contract id %d", r.ConID))
	}
	if len(r.Symbols) > 0 {
		parts = append(parts, "symbols "+strings.Join(r.Symbols, ", "))
	}
	if ccy := strings.ToUpper(strings.TrimSpace(r.Currency)); ccy != "" {
		parts = append(parts, "in "+ccy)
	}
	return strings.Join(parts, " · ")
}
