package ibkr

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"testing"
	"time"
)

// syntheticListingFrame is a complete contractData frame (message 10) at
// server version 203 for a synthetic exchange-traded fund line: its venue,
// local symbol, primary exchange, long name and ISIN. Every identifier is
// synthetic.
func syntheticListingFrame(reqID int, conID, symbol, local, exchange, primary, isin string) []string {
	return []string{
		strconv.Itoa(msgContractData), strconv.Itoa(reqID), symbol, "STK", "", "", "", "", exchange, "EUR", local, "XETRA", symbol, conID, "0.001",
		"", "LMT", "IBIS,SMART", "1", "0", "Synthetic Index Fund " + symbol, primary, "", "", "", "", "MET", "20261014:0900-20261014:1730", "20261014:0900-20261014:1730",
		"", "0", "1", "ISIN", isin, "1", "", "", "1", "", "ETF", "1", "1", "1", "0",
	}
}

// nthListingRequestFrame is the n-th STK reqContractDetails frame written.
func nthListingRequestFrame(t *testing.T, conn *Connection, socket *safeBuffer, n int) []string {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		var stk [][]string
		for _, frame := range decodeOutboundFrames(t, conn, socket.Bytes()) {
			if len(frame) > 5 && frame[0] == strconv.Itoa(reqContractData) && frame[5] == "STK" {
				stk = append(stk, frame)
			}
		}
		if len(stk) >= n {
			return stk[n-1]
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("STK request frame %d was never written", n)
	return nil
}

// Every form is asked and every listing kept: by ISIN on SMART and with no
// exchange, then the symbol on IBIS, IBIS2 and SMART with primary IBIS. Two
// listings of the asked ISIN are both returned, never one chosen; a line
// found twice is one listing with both forms; a symbol line naming another
// ISIN is dropped; a rejection and a no-definition answer are recorded in
// the trace with IBKR's code and text. The frames ask exactly what each
// form names, the primary exchange included.
func TestListingsAsksEveryFormAndKeepsEveryListing(t *testing.T) {
	conn, connector, socket, _, _ := newQueuedInstructionReconnectFixture(t)
	type result struct {
		listings []Listing
		trace    *BondLookupTrace
		err      error
	}
	done := make(chan result, 1)
	request := ListingRequest{ISIN: "IE00B3ZW0K18", Symbols: []string{"IBCF"}, Currency: "EUR",
		Venues: []ListingVenue{{Exchange: "IBIS"}, {Exchange: "IBIS2"}, {Exchange: "SMART", PrimaryExch: "IBIS"}}}
	go func() {
		listings, trace, err := connector.Listings(context.Background(), request, 2*time.Second)
		done <- result{listings, trace, err}
	}()
	epoch := conn.BrokerSessionEpoch()
	// 1. By ISIN on SMART: two listings of the fund.
	frame := nthListingRequestFrame(t, conn, socket, 1)
	assertFields(t, frame, []fieldAssertion{{4, "", "symbol"}, {10, "SMART", "exchange"}, {11, "", "primaryExchange"}, {12, "EUR", "currency"}, {16, "ISIN", "secIdType"}, {17, "IE00B3ZW0K18", "secId"}})
	reqID := waitForHandlerReqID(t, conn, msgContractData)
	conn.dispatchHandlers(msgContractData, syntheticListingFrame(reqID, "900001", "IBCF", "IBCF", "SMART", "IBIS", "IE00B3ZW0K18"), epoch)
	conn.dispatchHandlers(msgContractData, syntheticListingFrame(reqID, "900002", "IUSE", "IUSE", "SMART", "LSEETF", "IE00B3ZW0K18"), epoch)
	prewarmTestEnd(conn, reqID)
	// 2. By ISIN with no exchange: rejected.
	frame = nthListingRequestFrame(t, conn, socket, 2)
	assertFields(t, frame, []fieldAssertion{{10, "", "exchange"}, {16, "ISIN", "secIdType"}})
	reqID = waitForHandlerReqIDAfter(t, conn, msgContractData, reqID)
	if !connector.failPendingContractDetails(reqID, 321, "Error validating request: exchange required") {
		t.Fatal("the no-exchange form was not armed for the broker's rejection")
	}
	// 3. The symbol on IBIS: the first listing again.
	frame = nthListingRequestFrame(t, conn, socket, 3)
	assertFields(t, frame, []fieldAssertion{{4, "IBCF", "symbol"}, {10, "IBIS", "exchange"}, {11, "", "primaryExchange"}, {16, "", "secIdType"}})
	reqID = waitForHandlerReqIDAfter(t, conn, msgContractData, reqID)
	conn.dispatchHandlers(msgContractData, syntheticListingFrame(reqID, "900001", "IBCF", "IBCF", "IBIS", "IBIS", "IE00B3ZW0K18"), epoch)
	prewarmTestEnd(conn, reqID)
	// 4. The symbol on IBIS2: no definition.
	nthListingRequestFrame(t, conn, socket, 4)
	reqID = waitForHandlerReqIDAfter(t, conn, msgContractData, reqID)
	if !connector.failPendingContractDetails(reqID, 200, "No security definition has been found for the request") {
		t.Fatal("the IBIS2 form was not armed for the broker's rejection")
	}
	// 5. The symbol on SMART with primary IBIS: another security's line.
	frame = nthListingRequestFrame(t, conn, socket, 5)
	assertFields(t, frame, []fieldAssertion{{4, "IBCF", "symbol"}, {10, "SMART", "exchange"}, {11, "IBIS", "primaryExchange"}})
	reqID = waitForHandlerReqIDAfter(t, conn, msgContractData, reqID)
	conn.dispatchHandlers(msgContractData, syntheticListingFrame(reqID, "900003", "IBCF", "IBCF", "SMART", "IBIS", "IE00B5BMR087"), epoch)
	prewarmTestEnd(conn, reqID)

	got := <-done
	if got.err != nil || len(got.listings) != 2 {
		t.Fatalf("listings %+v err %v", got.listings, got.err)
	}
	first, second := got.listings[0], got.listings[1]
	if first.ConID != 900001 || first.Symbol != "IBCF" || first.LocalSymbol != "IBCF" || first.PrimaryExch != "IBIS" || first.Currency != "EUR" || first.ISIN != "IE00B3ZW0K18" ||
		first.LongName != "Synthetic Index Fund IBCF" || first.TradingClass != "IBCF" || !slices.Equal(first.Forms, []string{"STK by ISIN on SMART", "STK IBCF on IBIS"}) {
		t.Fatalf("first listing %+v", first)
	}
	if second.ConID != 900002 || second.PrimaryExch != "LSEETF" || second.Symbol != "IUSE" {
		t.Fatalf("second listing %+v", second)
	}
	if len(got.trace.Attempts) != 5 {
		t.Fatalf("trace %+v", got.trace.Attempts)
	}
	for i, want := range []struct {
		outcome string
		code    int
		lines   int
	}{{BondAttemptLine, 0, 2}, {BondAttemptRejected, 321, 0}, {BondAttemptLine, 0, 1}, {BondAttemptRejected, 200, 0}, {BondAttemptNoLine, 0, 0}} {
		if a := got.trace.Attempts[i]; a.Outcome != want.outcome || a.Code != want.code || a.Lines != want.lines {
			t.Fatalf("attempt %d = %+v", i, a)
		}
	}
}

// By contract id the form names no symbol, so IBKR's own symbol comes back
// whatever the owner's ticker is; nothing found is ErrNoListing with the
// trace; a malformed request is never sent.
func TestListingsByContractIDAndErrors(t *testing.T) {
	conn, connector, socket, _, _ := newQueuedInstructionReconnectFixture(t)
	done := make(chan error, 1)
	var listings []Listing
	go func() {
		var err error
		listings, _, err = connector.Listings(context.Background(), ListingRequest{ConID: 900001, Currency: "EUR", Venues: []ListingVenue{{Exchange: "IBIS"}}}, 2*time.Second)
		done <- err
	}()
	frame := nthListingRequestFrame(t, conn, socket, 1)
	assertFields(t, frame, []fieldAssertion{{3, "900001", "conId"}, {4, "", "symbol"}, {10, "IBIS", "exchange"}, {12, "EUR", "currency"}})
	reqID := waitForHandlerReqID(t, conn, msgContractData)
	conn.dispatchHandlers(msgContractData, syntheticListingFrame(reqID, "900001", "IBCF", "IBCF", "IBIS", "IBIS", "IE00B3ZW0K18"), conn.BrokerSessionEpoch())
	prewarmTestEnd(conn, reqID)
	if err := <-done; err != nil || len(listings) != 1 || listings[0].Symbol != "IBCF" || listings[0].Exchange != "IBIS" {
		t.Fatalf("by contract id: %+v %v", listings, err)
	}

	go func() {
		_, _, err := connector.Listings(context.Background(), ListingRequest{ISIN: "IE00B3ZW0K18", Currency: "EUR"}, 2*time.Second)
		done <- err
	}()
	nthListingRequestFrame(t, conn, socket, 2)
	reqID = waitForHandlerReqIDAfter(t, conn, msgContractData, reqID)
	connector.failPendingContractDetails(reqID, 200, "No security definition has been found for the request")
	nthListingRequestFrame(t, conn, socket, 3)
	reqID = waitForHandlerReqIDAfter(t, conn, msgContractData, reqID)
	connector.failPendingContractDetails(reqID, 200, "No security definition has been found for the request")
	if err := <-done; !errors.Is(err, ErrNoListing) {
		t.Fatalf("nothing found: %v", err)
	}
	for _, bad := range []ListingRequest{{ISIN: "IE00B3ZW0K17"}, {}, {ISIN: "IE00B3ZW0K18", Currency: "EURO"}} {
		if _, _, err := connector.Listings(context.Background(), bad, time.Second); err == nil || errors.Is(err, ErrNoListing) {
			t.Fatalf("a malformed request %+v was sent: %v", bad, err)
		}
	}
}
