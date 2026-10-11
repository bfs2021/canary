package ibkr

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestEpochQuoteWaitingForCapacityCannotLeakSuccessorSlot(t *testing.T) {
	_, conn, _, _ := newQuoteFallbackFixture(t)
	for id := 10000; id < 10100; id++ {
		if err := conn.acquireMarketDataSlot(t.Context(), id); err != nil {
			t.Fatal(err)
		}
	}
	conn.reqIDMu.Lock()
	before := conn.reqIDSeq
	conn.reqIDMu.Unlock()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := conn.requestMarketDataWithContractForEpoch(ctx, exactQuoteTestContract(900001), "", false, false, 1, nil)
		done <- err
	}()
	deadline := time.After(time.Second)
	for {
		conn.reqIDMu.Lock()
		allocated := conn.reqIDSeq > before
		conn.reqIDMu.Unlock()
		if allocated {
			break
		}
		select {
		case <-deadline:
			t.Fatal("request did not reach slot admission")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	// A reconnect occurs while the old request is waiting for capacity.
	conn.brokerSessionEpoch.Store(2)
	conn.releaseMarketDataSlot(10000)
	if err := <-done; err == nil {
		t.Fatal("retired request crossed the broker session")
	}
	if got := marketDataSlotCount(conn); got != 99 {
		t.Fatalf("retired request leaked a successor-epoch slot: %d", got)
	}
}

func TestOverlappingOptionReadersKeepLineUntilLastRelease(t *testing.T) {
	c, conn, _, _ := newQuoteFallbackFixture(t)
	conn.optionContractCache[optionContractKey("SYNTH", "SYNTH", "20261218", 100, "C")] = ContractDetailsLite{
		ConID: 900001, Symbol: "SYNTH", SecType: "OPT", TradingClass: "SYNTH", Expiry: "20261218", Strike: 100, Right: "C", Exchange: "SMART", Currency: "USD", Multiplier: 100,
	}
	key, id, err := c.SubscribeOption(t.Context(), "SYNTH", "SYNTH", "20261218", 100, "C")
	if err != nil {
		t.Fatal(err)
	}
	other, otherID, err := c.SubscribeOption(t.Context(), "SYNTH", "SYNTH", "20261218", 100, "C")
	if err != nil || other != key || otherID != id {
		t.Fatalf("overlapping read did not share one request: %s %d %v", other, otherID, err)
	}
	if err := c.UnsubscribeMarketData(key); err != nil {
		t.Fatal(err)
	}
	if marketDataSlotCount(conn) != 1 || c.subscriptions[key] == nil {
		t.Fatal("first reader cancelled the line still owned by the second reader")
	}
	if err := c.UnsubscribeMarketData(key); err != nil {
		t.Fatal(err)
	}
	if marketDataSlotCount(conn) != 0 || len(c.optReqIDs) != 0 {
		t.Fatal("last reader retained a slot or obsolete option tick routing")
	}
}

func TestConcurrentOptionHoldsReleaseExactlyOneLine(t *testing.T) {
	c, conn, _, _ := newQuoteFallbackFixture(t)
	conn.optionContractCache[optionContractKey("SYNTH", "SYNTH", "20261218", 100, "C")] = ContractDetailsLite{
		ConID: 900001, Symbol: "SYNTH", SecType: "OPT", TradingClass: "SYNTH", Expiry: "20261218", Strike: 100, Right: "C", Exchange: "SMART", Currency: "USD", Multiplier: 100,
	}
	const readers = 20
	start := make(chan struct{})
	releases := make(chan func() error, readers)
	errors := make(chan error, readers)
	var wg sync.WaitGroup
	for range readers {
		wg.Go(func() {
			<-start
			_, release, err := c.HoldOption(t.Context(), "SYNTH", "SYNTH", "20261218", 100, "C")
			if err != nil {
				errors <- err
				return
			}
			releases <- release
		})
	}
	close(start)
	wg.Wait()
	close(errors)
	for err := range errors {
		t.Fatal(err)
	}
	if got := marketDataSlotCount(conn); got != 1 {
		t.Fatalf("overlapping starts reserved %d slots, want one", got)
	}
	for range readers {
		release := <-releases
		if err := release(); err != nil {
			t.Fatal(err)
		}
		if err := release(); err != nil {
			t.Fatal(err)
		}
	}
	if marketDataSlotCount(conn) != 0 || len(c.optReqIDs) != 0 {
		t.Fatal("concurrent readers leaked slot or tick routing")
	}
}

func TestOptionReleaseCannotRemoveSuccessorWithReusedRequestID(t *testing.T) {
	c, conn, _, _ := newQuoteFallbackFixture(t)
	conn.optionContractCache[optionContractKey("SYNTH", "SYNTH", "20261218", 100, "C")] = ContractDetailsLite{
		ConID: 900001, Symbol: "SYNTH", SecType: "OPT", TradingClass: "SYNTH", Expiry: "20261218", Strike: 100, Right: "C", Exchange: "SMART", Currency: "USD", Multiplier: 100,
	}
	key, release, err := c.HoldOption(t.Context(), "SYNTH", "SYNTH", "20261218", 100, "C")
	if err != nil {
		t.Fatal(err)
	}
	// Model a replacement generation whose broker reuses the numeric ID.
	c.subMu.Lock()
	successor := *c.subscriptions[key]
	c.subscriptions[key] = &successor
	c.subMu.Unlock()
	if err := release(); err != nil {
		t.Fatal(err)
	}
	if c.subscriptions[key] != &successor || marketDataSlotCount(conn) != 1 {
		t.Fatal("old release removed the successor subscription")
	}
	if err := c.unsubscribeMarketData(t.Context(), key, nil, &successor); err != nil {
		t.Fatal(err)
	}
	if marketDataSlotCount(conn) != 0 {
		t.Fatal("successor could not release its own line")
	}
}

func TestOptionPendingReaderCancellationDoesNotCancelOwner(t *testing.T) {
	c, conn, _, _ := newQuoteFallbackFixture(t)
	conn.optionContractCache[optionContractKey("SYNTH", "SYNTH", "20261218", 100, "C")] = ContractDetailsLite{
		ConID: 900001, Symbol: "SYNTH", SecType: "OPT", TradingClass: "SYNTH", Expiry: "20261218", Strike: 100, Right: "C", Exchange: "SMART", Currency: "USD", Multiplier: 100,
	}
	for id := 10000; id < 10100; id++ {
		if err := conn.acquireMarketDataSlot(t.Context(), id); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	releases := make(chan func() error, 1)
	errors := make(chan error, 1)
	go func() {
		_, release, err := c.HoldOption(ctx, "SYNTH", "SYNTH", "20261218", 100, "C")
		errors <- err
		releases <- release
	}()
	for {
		c.subMu.RLock()
		pending := len(c.optionPending)
		c.subMu.RUnlock()
		if pending != 0 {
			break
		}
		if ctx.Err() != nil {
			t.Fatal("owner never entered option admission")
		}
		time.Sleep(time.Millisecond)
	}
	waitCtx, cancelWait := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancelWait()
	if _, _, err := c.HoldOption(waitCtx, "SYNTH", "SYNTH", "20261218", 100, "C"); err != context.DeadlineExceeded {
		t.Fatalf("waiting reader ignored its deadline: %v", err)
	}
	conn.releaseMarketDataSlot(10000)
	if err := <-errors; err != nil {
		t.Fatalf("cancelled waiter affected owner: %v", err)
	}
	if err := (<-releases)(); err != nil {
		t.Fatal(err)
	}
	if marketDataSlotCount(conn) != 99 {
		t.Fatal("cancelled waiter retained a reference or slot")
	}
}

func TestOptionReleaseRemovesRoutingBeforeAdmittingSuccessor(t *testing.T) {
	c, conn, _, _ := newQuoteFallbackFixture(t)
	conn.optionContractCache[optionContractKey("SYNTH", "SYNTH", "20261218", 100, "C")] = ContractDetailsLite{
		ConID: 900001, Symbol: "SYNTH", SecType: "OPT", TradingClass: "SYNTH", Expiry: "20261218", Strike: 100, Right: "C", Exchange: "SMART", Currency: "USD", Multiplier: 100,
	}
	key, release, err := c.HoldOption(t.Context(), "SYNTH", "SYNTH", "20261218", 100, "C")
	if err != nil {
		t.Fatal(err)
	}
	// Stall routing cleanup. Admission must stay closed until the old route
	// is removed, or cleanup could erase a successor's reused request ID.
	c.optMu.Lock()
	done := make(chan error, 1)
	go func() { done <- release() }()
	reached, admitted := false, false
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if !c.subMu.TryRLock() {
			reached = true
			break
		}
		admitted = c.subscriptions[key] == nil
		c.subMu.RUnlock()
		if admitted {
			reached = true
			break
		}
		time.Sleep(time.Millisecond)
	}
	c.optMu.Unlock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !reached {
		t.Fatal("release did not reach routing cleanup")
	}
	if admitted {
		t.Fatal("replacement could be admitted while retired option routing still existed")
	}
}
