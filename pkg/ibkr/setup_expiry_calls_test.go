package ibkr

import (
	"bufio"
	"context"
	"errors"
	"strconv"
	"testing"
	"time"
)

func setupExpiryCallFrame(id, conID int, strike float64) []string {
	f := syntheticContractDetailsFields(maxClientVersion, "OPT", "")
	f[1], f[2], f[4] = strconv.Itoa(id), "SYNTH", "20270219"
	f[6], f[7], f[10] = strconv.FormatFloat(strike, 'f', -1, 64), "C", "SYNTH 270219C"+strconv.Itoa(conID)
	f[12], f[13], f[15], f[17], f[19] = "SYNTH", strconv.Itoa(conID), "100", "SMART,CBOE", "17"
	f[21], f[37] = "ARCA", "27,28"
	return f
}

func setupExpiryUnderlying() Contract {
	return Contract{ConID: 17, Symbol: "SYNTH", SecType: "STK", Exchange: "SMART", PrimaryExch: "ARCA", Currency: "USD"}
}

func TestSetupExpiryCallsKeepsEarlyCompleteInventoryAndPriceMetadata(t *testing.T) {
	c, conn, _, _ := newMissTestConnector(t)
	c.ready = true
	binding, _ := c.CaptureSession()
	conn.writer = bufio.NewWriter(prewarmReplyWriter{func() {
		conn.reqIDMu.Lock()
		id := conn.reqIDSeq - 1
		conn.reqIDMu.Unlock()
		for _, k := range []int{775, 770, 770} {
			conn.dispatchHandlers(msgContractData, setupExpiryCallFrame(id, 10000+k, float64(k)), binding.epoch)
		}
		prewarmTestEnd(conn, id)
		prewarmTestEnd(conn, id)
	}})
	rows, err := c.FetchSetupExpiryCalls(t.Context(), binding, setupExpiryUnderlying(), "20270219", time.Second)
	if err != nil || len(rows) != 2 || rows[0].Contract.Strike != 770 || rows[1].Contract.Strike != 775 {
		t.Fatalf("complete expiry not retained/deduplicated: %+v %v", rows, err)
	}
	for _, row := range rows {
		if row.UnderConID != 17 || row.MinTick != .01 || row.ValidExchanges != "SMART,CBOE" || row.MarketRuleIDs != "27,28" || row.TimeZoneID != "UTC" || row.Contract.Exchange != "SMART" || row.Contract.LocalSymbol == "" {
			t.Fatalf("resolved quote/routing metadata lost: %+v", row)
		}
	}
}

func TestSetupExpiryCallsFailsClosedOnIncompleteOrInvalidInventory(t *testing.T) {
	for _, mode := range []string{"complete", "no-end", "cancel", "overflow", "underlying", "expiry", "class", "multiplier", "right", "strike-conflict", "id-conflict", "broker-rejection"} {
		t.Run(mode, func(t *testing.T) {
			c, conn, out, _ := newMissTestConnector(t)
			c.ready = true
			binding, _ := c.CaptureSession()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			type result struct {
				rows []ResolvedOrderContract
				err  error
			}
			done := make(chan result, 1)
			go func() {
				rows, err := c.FetchSetupExpiryCalls(ctx, binding, setupExpiryUnderlying(), "20270219", 200*time.Millisecond)
				done <- result{rows, err}
			}()
			id := waitForPendingContractDetails(t, c)
			var wire []string
			for deadline := time.Now().Add(time.Second); time.Now().Before(deadline) && wire == nil; {
				for _, f := range decodeOutboundFrames(t, conn, out.Bytes()) {
					if f[0] == strconv.Itoa(reqContractData) && f[2] == strconv.Itoa(id) {
						wire = f
					}
				}
				if wire == nil {
					time.Sleep(time.Millisecond)
				}
			}
			if len(wire) < 15 || wire[4] != "SYNTH" || wire[5] != "OPT" || wire[6] != "20270219" || wire[7] != "" || wire[8] != "C" || wire[9] != "100" || wire[10] != "SMART" || wire[11] != "ARCA" || wire[12] != "USD" || wire[14] != "SYNTH" {
				t.Fatalf("request did not constrain one standard call expiry: %v", wire)
			}
			f := setupExpiryCallFrame(id, 10770, 770)
			conn.dispatchHandlers(msgContractData, f, binding.epoch+1)
			conn.dispatchHandlers(msgContractDataEnd, []string{strconv.Itoa(msgContractDataEnd), "1", strconv.Itoa(id)}, binding.epoch+1)
			select {
			case got := <-done:
				t.Fatalf("wrong epoch completed inventory: %+v", got)
			default:
			}
			conn.dispatchHandlers(msgContractData, f, binding.epoch)
			if mode != "complete" {
				bad := setupExpiryCallFrame(id, 10775, 775)
				switch mode {
				case "underlying":
					bad[19] = "18"
				case "expiry":
					bad[4] = "20270115"
				case "class":
					bad[12] = "SYNTH1"
				case "multiplier":
					bad[15] = "10"
				case "right":
					bad[7] = "P"
				case "strike-conflict":
					bad[6] = "770"
				case "id-conflict":
					bad[13] = "10770"
				}
				conn.dispatchHandlers(msgContractData, bad, binding.epoch)
			}
			switch mode {
			case "no-end":
			case "cancel":
				cancel()
			case "overflow":
				for range setupExpiryCallLimit {
					conn.dispatchHandlers(msgContractData, f, binding.epoch)
				}
				prewarmTestEnd(conn, id)
			case "broker-rejection":
				c.failPendingContractDetails(id, 200, "Synthetic contract rejection")
			default:
				prewarmTestEnd(conn, id)
			}
			got := <-done
			if mode == "complete" {
				if got.err != nil || len(got.rows) != 1 {
					t.Fatalf("complete inventory: %+v", got)
				}
			} else if got.err == nil || got.rows != nil {
				t.Fatalf("partial or invalid inventory escaped: %+v", got)
			}
			if mode == "no-end" && !errors.Is(got.err, context.DeadlineExceeded) || mode == "cancel" && !errors.Is(got.err, context.Canceled) {
				t.Fatalf("wrong cancellation result: %v", got.err)
			}
		})
	}
}

func TestSetupExpiryCallsRejectsRetiredSession(t *testing.T) {
	c, conn, _, _ := newMissTestConnector(t)
	c.ready = true
	binding, _ := c.CaptureSession()
	done := make(chan error, 1)
	go func() {
		rows, err := c.FetchSetupExpiryCalls(t.Context(), binding, setupExpiryUnderlying(), "20270219", 200*time.Millisecond)
		if len(rows) != 0 {
			t.Errorf("retired session returned contracts: %v", rows)
		}
		done <- err
	}()
	id := waitForPendingContractDetails(t, c)
	conn.resetOrderIDReadiness()
	conn.dispatchHandlers(msgContractData, setupExpiryCallFrame(id, 10770, 770), binding.epoch)
	conn.dispatchHandlers(msgContractDataEnd, []string{strconv.Itoa(msgContractDataEnd), "1", strconv.Itoa(id)}, binding.epoch)
	if err := <-done; err == nil {
		t.Fatal("retired inventory remained authoritative")
	}
}
