package daemon

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/osauer/canary/v2/internal/rpc"
	ibkr "github.com/osauer/canary/v2/pkg/ibkr"
)

// The stock's first price arrives while secdef metadata is loading. Discovery
// must consume that callback from the held subscription, without opening a new
// quote line or retaining the earlier empty snapshot. This uses the production
// connector, decoder, subscription manager and quote path throughout.
func TestAddDiscoveryUsesUnderlyingQuoteArrivingDuringMetadata(t *testing.T) {
	runAddSnapshotScenario(t, "cache")
}

func TestAddUnknownCalendarSkipsOptionAcquisition(t *testing.T) {
	runAddSnapshotScenario(t, "unknown")
}

func TestAddClosedPlanningRetainsDatedHistoryWithoutGreeks(t *testing.T) {
	runAddSnapshotScenario(t, "planning")
}

func runAddSnapshotScenario(t *testing.T, scenario string) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	expiry := time.Now().AddDate(0, 0, 30).Format("20060102")
	var mu sync.Mutex
	var stockRequests []string
	var optionStrikes []string
	var listingRequests, optionHistoryRequests int
	// Explicit calendar scenarios use the existing server clock. The cache
	// regression is valid both live and in planning: its exact contract and
	// shared-subscription assertions do not depend on execution eligibility.
	calendarAt := time.Date(2026, 10, 22, 16, 0, 0, 0, time.UTC)
	if scenario == "unknown" {
		calendarAt = time.Date(2029, 1, 2, 16, 0, 0, 0, time.UTC)
	}
	if scenario == "planning" {
		calendarAt = time.Date(2026, 10, 22, 23, 0, 0, 0, time.UTC)
	}
	lastSession := addOptionsCalendar(calendarAt).last
	lastDate := "20261022"
	if lastSession != nil {
		lastDate = strings.ReplaceAll(lastSession.Date, "-", "")
	}
	done := make(chan error, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			done <- err
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
		r := bufio.NewReader(conn)
		prefix := make([]byte, 4)
		if _, err := io.ReadFull(r, prefix); err != nil {
			done <- err
			return
		}
		if _, err := readTWSFrame(r); err != nil {
			done <- err
			return
		}
		if err := writeTWSFrame(conn, []byte("203\x0020261022 12:00:00 UTC\x00")); err != nil {
			done <- err
			return
		}
		if _, err := readTWSFrame(r); err != nil {
			done <- err
			return
		}
		var writeErr error
		send := func(id uint32, fields ...string) {
			if writeErr == nil {
				body := binary.BigEndian.AppendUint32(nil, id)
				writeErr = writeTWSFrame(conn, append(body, strings.Join(fields, "\x00")+"\x00"...))
			}
		}
		send(9, "1", "1")          // nextValidId
		send(15, "1", "DU1234567") // managedAccounts: no one-second startup grace
		for writeErr == nil {
			frame, err := readTWSFrame(r)
			if err != nil {
				done <- nil // normal connector shutdown
				return
			}
			if len(frame) < 4 {
				continue
			}
			f := append([]string{strconv.Itoa(int(binary.BigEndian.Uint32(frame[:4])))}, strings.Split(string(frame[4:]), "\x00")...)
			switch f[0] {
			case "9": // exact stock or expiry inventory
				id := f[2]
				if f[5] == "STK" {
					send(10, addSnapshotContractFields(id, "STK", "", "", "17")...)
				} else if f[3] != "0" {
					send(10, addSnapshotContractFields(id, "OPT", expiry, f[7], f[3])...)
				} else {
					for _, strike := range []string{"100", "110", "120"} {
						send(10, addSnapshotContractFields(id, "OPT", expiry, strike, "10"+strike)...)
					}
				}
				send(52, "1", id)
			case "91":
				send(93, f[1], "1", "0", "0.01")
			case "1": // subscribe; no stock price is sent here, even after metadata
				id := f[2]
				mu.Lock()
				if f[5] == "STK" {
					stockRequests = append(stockRequests, id)
				} else {
					optionStrikes = append(optionStrikes, f[7])
				}
				mu.Unlock()
				if f[5] == "STK" && scenario == "unknown" {
					send(58, "1", id, "1")
					send(1, "6", id, "1", "109.99", "100", "0")
					send(1, "6", id, "2", "110.01", "100", "0")
					send(1, "6", id, "4", "110", "100", "0")
					send(1, "6", id, "9", "109", "100", "0")
				}
				if f[5] == "OPT" {
					feed := "1"
					if scenario == "planning" {
						feed = "2"
					}
					send(58, "1", id, feed)
					send(1, "6", id, "1", "1.00", "100", "0")
					send(1, "6", id, "2", "1.02", "100", "0")
					if scenario != "planning" {
						send(21, id, "13", "0", "0.2", "0.6", "1.01", "0", "0.01", "0.1", "-0.1", "110")
					}
				}
			case "78":
				mu.Lock()
				listingRequests++
				for _, id := range stockRequests {
					send(58, "1", id, "1")
					send(1, "6", id, "1", "109.99", "100", "0")
					send(1, "6", id, "2", "110.01", "100", "0")
					send(1, "6", id, "4", "110", "100", "0")
					send(1, "6", id, "9", "109", "100", "0")
				}
				mu.Unlock()
				send(75, f[1], "SMART", "17", "SYNTH", "100", "1", expiry, "3", "100", "110", "120")
				send(76, f[1])
			case "20":
				if scenario == "planning" && f[4] == "OPT" {
					mu.Lock()
					optionHistoryRequests++
					mu.Unlock()
					send(17, f[1], "1", lastDate, "1.0", "1.1", "0.9", "1.07", "100", "1.02", "4")
					send(108, f[1], lastDate, lastDate)
				} else {
					send(17, f[1], "", "", "0")
				}
			}
		}
		done <- writeErr
	}()
	cfg := ibkr.DefaultConfig()
	cfg.Host, cfg.Port, cfg.ClientID, cfg.AutoReconnect = "127.0.0.1", ln.Addr().(*net.TCPAddr).Port, 71, false
	c := ibkr.NewConnector(&ibkr.ConnectorConfig{BaseConfig: cfg})
	ctx, cancel := context.WithTimeout(t.Context(), 12*time.Second)
	defer cancel()
	if err := c.Start(ctx); err != nil || !c.IsReady() {
		t.Fatalf("fake broker not ready: %v %s", err, c.LastError())
	}
	t.Cleanup(func() {
		_ = c.Stop()
		if err := <-done; err != nil {
			t.Errorf("fake broker: %v", err)
		}
	})
	s := newTestServer(t)
	s.connector = c
	if scenario != "cache" {
		s.now = func() time.Time { return calendarAt }
	}
	params, _ := json.Marshal(rpc.AddMarketParams{Underlying: rpc.ContractParams{ConID: 17, Symbol: "SYNTH", SecType: "STK", Exchange: "SMART", Currency: "USD"}, Discover: true})
	got, err := s.handleAddMarket(ctx, &rpc.Request{Params: params})
	if err != nil {
		t.Fatal(err)
	}
	spot, _ := quoteCurrentPrice(got.Quote)
	if spot == nil || *spot != 110 {
		t.Fatalf("metadata-time callback was not consumed from the held stock cache: spot=%v", spot)
	}
	if scenario == "unknown" {
		mu.Lock()
		defer mu.Unlock()
		if got.OptionMode != "unavailable" || got.OptionsSession.State != "unknown" || len(got.Calls) != 4 || listingRequests != 0 || len(optionStrikes) != 0 || len(stockRequests) != 1 {
			t.Fatalf("unknown calendar acquired options: mode=%s listings=%d options=%v stock=%d", got.OptionMode, listingRequests, optionStrikes, len(stockRequests))
		}
		for _, row := range got.Calls {
			if row.Contract != nil || row.Unavailable != "Options calendar unavailable" {
				t.Fatalf("unknown calendar suggested a call: %+v", row)
			}
		}
		return
	}
	if len(got.Calls) != 4 || got.Calls[1].Contract == nil || got.Calls[1].Contract.Strike != 110 || got.Calls[1].Unavailable != "" {
		t.Fatalf("late underlying price did not select the nearest eligible call: %+v", got.Calls)
	}
	mu.Lock()
	defer mu.Unlock()
	if scenario == "planning" {
		row := got.Calls[1]
		p := row.Planning
		if got.OptionMode != "planning" || p == nil || p.Price == nil || *p.Price != 1.07 || p.PriceSource != "last_session_close" || p.SessionDate != lastSession.Date || p.DeltaStatus != "unavailable" || eligibleAddCall(row, time.Now()) || optionHistoryRequests != 1 || len(optionStrikes) != 3 {
			t.Fatalf("closed read lost dated history or invented eligibility: row=%+v planning=%+v history=%d strikes=%v", row, p, optionHistoryRequests, optionStrikes)
		}
	} else if len(optionStrikes) != 1 || optionStrikes[0] != "110" {
		t.Fatalf("strike ranking changed: %v", optionStrikes)
	}
	if len(stockRequests) != 1 {
		t.Fatalf("held cache was not reused: stock lines=%d", len(stockRequests))
	}
}

// ContractDetails v203 payload, using synthetic public identity and rules.
func addSnapshotContractFields(id, secType, expiry, strike, conID string) []string {
	right, multiplier, underID := "", "1", "0"
	if secType == "OPT" {
		right, multiplier, underID = "C", "100", "17"
	}
	return []string{id, "SYNTH", secType, expiry, "", strike, right, "SMART", "USD", "SYNTH " + conID, "SYNTH", "SYNTH", conID, "0.01", multiplier, "LMT", "SMART", "1", underID, "Synthetic", "ARCA", "", "", "", "", "UTC", "", "", "", "0", "0", "0", "SYNTH", "STK", "27", expiry, "", "1", "1", "1", "0"}
}
