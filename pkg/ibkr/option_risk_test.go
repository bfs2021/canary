package ibkr

import (
	"bufio"
	"context"
	"strconv"
	"testing"
)

func exactModelFields(id int, tick, delta, underlying string) []string {
	return []string{"21", strconv.Itoa(id), tick, "0", "0.2", delta, "1.0", "0", "0.01", "0.1", "-0.1", underlying}
}

func TestExactOptionRiskReceiptIdentityAndAtomicComponents(t *testing.T) {
	conn, c, _, _, _ := newQueuedInstructionReconnectFixture(t)
	binding, _ := c.CaptureSession()
	contract := exactQuoteTestContract(900001)
	key, err := c.SubscribeMarketDataWithContractForSession(context.Background(), binding, contract, nil)
	if err != nil {
		t.Fatal(err)
	}
	id := exactQuoteSubscriptionReqID(t, c, key)
	conn.processMarketDataTypeAtEpoch([]string{"58", "1", strconv.Itoa(id), "1"}, binding.epoch)
	if c.OptionRiskForSession(binding, key) != nil {
		t.Fatal("empty subscription fabricated Greeks")
	}
	c.handleExactOptionRisk(binding, exactModelFields(id, "13", "0", "100"))
	r := c.OptionRiskForSession(binding, key)
	if r == nil || r.Delta == nil || *r.Delta != 0 || r.Underlying == nil || r.RequestID != id || r.Contract.ConID != contract.ConID || r.Contract.TradingClass != contract.TradingClass || r.DataType != 1 || r.ReceivedAt.Before(r.RequestedAt) {
		t.Fatalf("invalid zero-delta receipt: %+v", r)
	}
	firstAt := r.ReceivedAt
	*r.Delta = 0.5
	if *c.OptionRiskForSession(binding, key).Delta != 0 {
		t.Fatal("caller mutated receipt")
	}
	c.handleTickPrice([]string{"1", "3", strconv.Itoa(id), "1", "2", "0"})
	if !c.OptionRiskForSession(binding, key).ReceivedAt.Equal(firstAt) {
		t.Fatal("quote freshened computation")
	}
	c.handleExactOptionRisk(binding, exactModelFields(id, "13", "NaN", "110"))
	r = c.OptionRiskForSession(binding, key)
	if r.Delta != nil || r.Underlying == nil || *r.Underlying != 110 {
		t.Fatal("partial computation borrowed prior delta")
	}
	c.handleExactOptionRisk(binding, exactModelFields(id, "13", "-0.5", "NaN"))
	r = c.OptionRiskForSession(binding, key)
	if r.Delta == nil || r.Underlying != nil {
		t.Fatal("partial computation borrowed prior spot")
	}
	c.handleExactOptionRisk(binding, exactModelFields(id, "83", "-0.5", "100"))
	if c.OptionRiskForSession(binding, key).DataType != 3 {
		t.Fatal("delayed model upgraded by live notice")
	}
	c.handleExactOptionRisk(binding, exactModelFields(id, "13", "-0.5", "100"))
	conn.processMarketDataTypeAtEpoch([]string{"58", "1", strconv.Itoa(id), "2"}, binding.epoch)
	if c.OptionRiskForSession(binding, key).DataType == 1 {
		t.Fatal("later frozen notice ignored")
	}
	if err := c.UnsubscribeMarketDataForSession(context.Background(), binding, key); err != nil {
		t.Fatal(err)
	}
	if c.OptionRiskForSession(binding, key) != nil {
		t.Fatal("retired subscription retained authority")
	}
}

func TestExactOptionRiskRejectsClassConIDAndReconnectCrossTalk(t *testing.T) {
	conn, c, _, newSocket, _ := newQueuedInstructionReconnectFixture(t)
	a, _ := c.CaptureSession()
	ca := exactQuoteTestContract(900001)
	ka, err := c.SubscribeMarketDataWithContractForSession(context.Background(), a, ca, nil)
	if err != nil {
		t.Fatal(err)
	}
	idA := exactQuoteSubscriptionReqID(t, c, ka)
	cb := ca
	cb.ConID++
	cb.TradingClass = "SYNTHW"
	cb.LocalSymbol = "SYNTHW OPTION"
	kb, err := c.SubscribeMarketDataWithContractForSession(context.Background(), a, cb, nil)
	if err != nil {
		t.Fatal(err)
	}
	conn.processMarketDataTypeAtEpoch([]string{"58", "1", strconv.Itoa(idA), "1"}, a.epoch)
	c.handleExactOptionRisk(a, exactModelFields(idA, "13", "-0.5", "100"))
	if c.OptionRiskForSession(a, kb) != nil {
		t.Fatal("another class/ConID borrowed receipt")
	}
	conn.resetOrderIDReadiness()
	conn.writer = bufio.NewWriter(newSocket)
	conn.observeNextValidOrderIDAtEpoch(500, conn.BrokerSessionEpoch())
	b, _ := c.CaptureSession()
	kc, err := c.SubscribeMarketDataWithContractForSession(context.Background(), b, cb, nil)
	if err != nil {
		t.Fatal(err)
	}
	idC := exactQuoteSubscriptionReqID(t, c, kc)
	// Even a recycled successor request ID cannot accept an old-origin tick.
	c.handleExactOptionRisk(a, exactModelFields(idC, "13", "-0.9", "900"))
	if c.OptionRiskForSession(a, ka) != nil || c.OptionRiskForSession(b, kc) != nil {
		t.Fatal("reconnect retained old model authority")
	}
}

func TestExactOptionRiskIVIsAtomicAndCallerOwned(t *testing.T) {
	conn, c, _, _, _ := newQueuedInstructionReconnectFixture(t)
	binding, _ := c.CaptureSession()
	key, err := c.SubscribeMarketDataWithContractForSession(context.Background(), binding, exactQuoteTestContract(900001), nil)
	if err != nil {
		t.Fatal(err)
	}
	id := exactQuoteSubscriptionReqID(t, c, key)
	conn.processMarketDataTypeAtEpoch([]string{"58", "1", strconv.Itoa(id), "1"}, binding.epoch)
	f := exactModelFields(id, "13", "0.6", "100")
	c.handleExactOptionRisk(binding, f)
	r := c.OptionRiskForSession(binding, key)
	if r.IV == nil || *r.IV != .2 {
		t.Fatal("exact model IV missing")
	}
	*r.IV = .8
	if *c.OptionRiskForSession(binding, key).IV != .2 {
		t.Fatal("caller changed model IV")
	}
	f[4] = "NaN"
	c.handleExactOptionRisk(binding, f)
	if c.OptionRiskForSession(binding, key).IV != nil {
		t.Fatal("partial model borrowed old IV")
	}
}
