package daemon

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/osauer/canary/v2/internal/core"
	"github.com/osauer/canary/v2/internal/risk"
	"github.com/osauer/canary/v2/internal/rpc"
	ibkrlib "github.com/osauer/canary/v2/pkg/ibkr"
)

// The core's contract resolution (internal-docs/design/core-sleeve.md).
// IBKR's own symbol for a Xetra fund need not be the owner's ticker, and a
// contract-details or history request that names a contract id with a
// symbol IBKR does not use for it is answered "no security definition".
// So the owner pins the instrument by contract id, found by ISIN with
// `canary core resolve`, and the daemon asks for the pinned contract by its
// id alone before it reads the series, taking IBKR's symbol, venue and
// primary exchange from the answer.

const (
	// coreResolveFormTimeout bounds one contract-details form.
	coreResolveFormTimeout = 8 * time.Second
	// corePinResolveEvery is how often a resolved pin is asked again;
	// corePinRetryAfter spaces failed attempts.
	corePinResolveEvery = 6 * time.Hour
	corePinRetryAfter   = 15 * time.Minute
)

// coreResolveVenues are the venues the symbol fallbacks are asked on: Xetra
// (IBIS), Xetra's second segment (IBIS2) and SMART with Xetra as the
// primary exchange.
var coreResolveVenues = []ibkrlib.ListingVenue{{Exchange: "IBIS"}, {Exchange: "IBIS2"}, {Exchange: "SMART", PrimaryExch: "IBIS"}}

// corePin is the last resolution of the pinned contract id.
type corePin struct {
	conID   int
	listing ibkrlib.Listing
	at      time.Time
	err     string
}

// coreListings asks the broker for listings: the gateway's lookup, or the
// test's.
func (s *Server) coreListings(ctx context.Context, request ibkrlib.ListingRequest) ([]ibkrlib.Listing, *ibkrlib.BondLookupTrace, error) {
	if s.coreListingsForTest != nil {
		return s.coreListingsForTest(ctx, request)
	}
	c := s.gatewayConnector()
	if c == nil {
		return nil, nil, s.gatewayUnavailableError()
	}
	return c.Listings(ctx, request, coreResolveFormTimeout)
}

// handleCoreResolve lists every listing of an ISIN: the flag's, else the
// one [mandate.core] pins, else the compiled rule's. It never chooses one
// and writes nothing.
func (s *Server) handleCoreResolve(ctx context.Context, req *rpc.Request) (*rpc.CoreResolveResult, error) {
	var p rpc.CoreResolveParams
	if len(req.Params) > 0 && string(req.Params) != "null" {
		if err := decodeParams(req.Params, &p); err != nil {
			return nil, err
		}
	}
	var pinned *risk.ConstitutionMandateCore
	if s.riskPolicies != nil {
		if c := s.riskPolicies.snapshot().policy; c != nil && c.Mandate != nil {
			pinned = c.Mandate.Core
		}
	}
	res := &rpc.CoreResolveResult{AsOf: s.nowUTC(), ISIN: core.ISIN, ISINSource: "rule", Currency: strings.ToUpper(strings.TrimSpace(p.Currency)), Listings: []rpc.CoreListing{}, Attempts: []rpc.CoreResolveAttempt{}}
	switch {
	case strings.TrimSpace(p.ISIN) != "":
		res.ISIN, res.ISINSource = strings.ToUpper(strings.TrimSpace(p.ISIN)), "flag"
	case pinned != nil && strings.TrimSpace(pinned.ISIN) != "":
		res.ISIN, res.ISINSource = strings.ToUpper(strings.TrimSpace(pinned.ISIN)), "policy"
	}
	if !ibkrlib.ValidISIN(res.ISIN) {
		return nil, errBadRequest(fmt.Sprintf("%q is not an ISIN (two letters, nine letters or digits and a check digit)", res.ISIN))
	}
	if pinned != nil && pinned.ConID != nil && *pinned.ConID > 0 {
		res.PinnedConID = new(*pinned.ConID)
	}
	request := ibkrlib.ListingRequest{ISIN: res.ISIN, Currency: res.Currency, Venues: coreResolveVenues}
	if res.ISIN == core.ISIN {
		request.Symbols = []string{core.Symbol}
	}
	listings, trace, err := s.coreListings(ctx, request)
	if err != nil && !errors.Is(err, ibkrlib.ErrNoListing) {
		return nil, err
	}
	if trace != nil {
		for _, a := range trace.Attempts {
			res.Attempts = append(res.Attempts, rpc.CoreResolveAttempt{Form: a.Form, Outcome: a.Outcome, Lines: a.Lines, Code: a.Code, Message: a.Message})
		}
	}
	for _, l := range listings {
		row := rpc.CoreListing{ConID: l.ConID, Symbol: l.Symbol, LocalSymbol: l.LocalSymbol, SecType: l.SecType, Exchange: l.Exchange, PrimaryExch: l.PrimaryExch,
			Currency: l.Currency, TradingClass: l.TradingClass, LongName: l.LongName, ISIN: l.ISIN, Forms: slices.Clone(l.Forms)}
		if res.PinnedConID != nil && *res.PinnedConID == l.ConID {
			row.Pinned, res.PinnedFound = true, true
		}
		res.Listings = append(res.Listings, row)
	}
	switch {
	case len(res.Listings) == 0:
		res.Message = "IBKR found no listing for " + res.ISIN + "; every form's answer is listed. Check the ISIN, or ask again with --currency."
	case res.PinnedFound:
		res.Message = fmt.Sprintf("[mandate.core] con_id %d is among the listings; nothing to write.", *res.PinnedConID)
	case len(res.Listings) == 1:
		res.Message = fmt.Sprintf("One listing: write con_id = %d into [mandate.core] with a higher policy_version if it is the line you hold on %s.", res.Listings[0].ConID, core.Exchange)
	default:
		res.Message = fmt.Sprintf("%d listings match; none is chosen for you. Write the con_id of the line you trade on %s (Xetra) into [mandate.core] with a higher policy_version.", len(res.Listings), core.Exchange)
	}
	if res.PinnedConID != nil && !res.PinnedFound && len(res.Listings) > 0 {
		res.Message = fmt.Sprintf("[mandate.core] con_id %d is not among these listings. ", *res.PinnedConID) + res.Message
	}
	return res, nil
}

// coreResolvedNeed asks for the pinned contract by its id alone, on the
// policy's venue, and returns the series need with IBKR's symbol, venue,
// primary exchange and currency for it. A resolution is kept for
// corePinResolveEvery; a failure is retried after corePinRetryAfter, and
// the core names it while its series is missing.
func (s *Server) coreResolvedNeed(ctx context.Context, c *risk.Constitution, need mandateHistoryNeed) (mandateHistoryNeed, bool) {
	conID := need.Contract.ConID
	now := s.nowUTC()
	st := &s.coreTargets
	st.mu.Lock()
	pin := st.pin
	st.mu.Unlock()
	if pin.conID == conID {
		switch {
		case pin.err == "" && now.Sub(pin.at) < corePinResolveEvery:
			return coreNeedFor(need, pin.listing), true
		case pin.err != "" && now.Sub(pin.at) < corePinRetryAfter:
			return need, false
		}
	}
	exchange := need.Contract.Exchange
	listings, _, err := s.coreListings(ctx, ibkrlib.ListingRequest{ConID: conID, Currency: need.Contract.Currency, Venues: []ibkrlib.ListingVenue{{Exchange: exchange}, {}}})
	next := corePin{conID: conID, at: now}
	var found *ibkrlib.Listing
	for i := range listings {
		l := &listings[i]
		if l.ConID != conID {
			continue
		}
		if found == nil || strings.EqualFold(l.Exchange, exchange) && !strings.EqualFold(found.Exchange, exchange) {
			found = l
		}
	}
	switch {
	case err != nil && found == nil:
		next.err = errText(err)
	case found == nil:
		next.err = "IBKR answered no line for it"
	case !strings.EqualFold(found.Currency, need.Contract.Currency):
		next.err = fmt.Sprintf("IBKR lists it in %s, not %s", found.Currency, need.Contract.Currency)
	default:
		next.listing = *found
	}
	st.mu.Lock()
	st.pin = next
	st.mu.Unlock()
	if next.err != "" {
		if s.logger != nil && ctx.Err() == nil {
			s.logger.Warnf("core contract %d did not resolve on %s: %s; the core holds until it does", conID, exchange, next.err)
		}
		return need, false
	}
	return coreNeedFor(need, next.listing), true
}

// coreNeedFor is the series need asking IBKR for the listing as IBKR names
// it: the contract id with IBKR's symbol, the policy's venue (or the
// listing's when IBKR answered on another) and the listing's primary
// exchange.
func coreNeedFor(need mandateHistoryNeed, l ibkrlib.Listing) mandateHistoryNeed {
	out := need
	out.Contract.Symbol = l.Symbol
	out.Contract.Currency = l.Currency
	if l.Exchange != "" && !strings.EqualFold(l.Exchange, "SMART") {
		out.Contract.Exchange = l.Exchange
	}
	out.Contract.PrimaryExch = l.PrimaryExch
	return out
}

// corePinReason is the last failed resolution of the pinned contract, for
// the core's hold while its series is missing.
func (s *Server) corePinReason(conID int) string {
	st := &s.coreTargets
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.pin.conID != conID || st.pin.err == "" {
		return ""
	}
	return st.pin.err
}
