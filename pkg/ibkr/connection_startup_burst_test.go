package ibkr

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// startupBurstOrder is one synthetic open order of the startup burst: a GTC
// trailing stop in a one-cancels-all group.
func startupBurstOrder(i int) testOpenOrderProtoCallback {
	symbol := fmt.Sprintf("SYN%c", 'A'+i%26)
	return testOpenOrderProtoCallback{
		OrderID: 40 + i, PermID: 700000 + i, ClientID: 15,
		Symbol: symbol, SecType: "STK", Exchange: "SMART", PrimaryExch: "NASDAQ", Currency: "USD", LocalSymbol: symbol, TradingClass: "NMS",
		Action: "SELL", Quantity: "10", OrderType: "TRAIL", TrailingPercent: 8, TrailStopPrice: 92, TIF: "GTC",
		Account: "DU1234567", OcaGroup: "oca-synthetic-1", Transmit: true, Status: "PreSubmitted",
	}
}

// serveStartup is a loopback Gateway that answers the version handshake and,
// after START_API, runs the script of its nth accepted connection (the last
// script for every later one). The session stays open until the client hangs
// up, unless the script closes it.
func serveStartup(t *testing.T, scripts ...func(s *twsGatewaySession)) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	var accepted atomic.Int32
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			script := scripts[min(int(accepted.Add(1))-1, len(scripts)-1)]
			go func(c net.Conn) {
				defer c.Close()
				s := &twsGatewaySession{c: c}
				r := bufio.NewReader(c)
				if _, err := io.ReadFull(r, make([]byte, 4)); err != nil { // "API\0"
					return
				}
				if _, err := twsGatewayReadFrame(r); err != nil { // version descriptor
					return
				}
				s.write([]byte("203\x0020261019 21:53:00 CET\x00"))
				if _, err := twsGatewayReadFrame(r); err != nil { // START_API
					return
				}
				script(s)
				_, _ = io.Copy(io.Discard, r)
			}(c)
		}
	}()
	return ln
}

// startupBurst is what IB Gateway sends after START_API with "Download open
// orders on connection" on: an openOrder and an orderStatus per open order,
// openOrderEnd, managedAccounts and nextValidId, then the farm-status
// notices, in the order IB Gateway 10.50 sent them on 2026-10-19.
// withNextValidID false leaves nextValidId out.
func startupBurst(orders int, withNextValidID bool) func(*twsGatewaySession) {
	return func(s *twsGatewaySession) {
		for i := range orders {
			o := startupBurstOrder(i)
			s.write(encodeOpenOrderProtoCallbackForTest(o))
			s.write(twsGatewayOrderStatus(twsGatewayOrder{Bound: o.OrderID, PermID: o.PermID, Quantity: 10, Status: o.Status}))
		}
		s.write(twsGatewayFrame(msgOpenOrderEnd, "1"))
		s.write(twsGatewayFrame(msgManagedAccts, "1", "DU1234567"))
		if withNextValidID {
			s.write(twsGatewayFrame(msgNextValidID, "1", "100"))
		}
		s.write(twsGatewayFrame(msgErrMsg, "2", "-1", "2104", "Market data farm connection is OK:usfarm"))
		s.write(twsGatewayFrame(msgErrMsg, "2", "-1", "2106", "HMDS data farm connection is OK:ushmds"))
		s.write(twsGatewayFrame(msgErrMsg, "2", "-1", "2158", "Sec-def data farm connection is OK:secdefil"))
	}
}

func startupTestConnection(ln net.Listener, retries int) *Connection {
	return NewConnection(&ConnectionConfig{Host: "127.0.0.1", Port: ln.Addr().(*net.TCPAddr).Port, ClientID: 15, ConnectTimeout: 2 * time.Second, MaxClientIDRetries: retries, ManagedConnectionLogging: true})
}

// A Gateway that downloads open orders on connection sends two messages per
// open order and openOrderEnd before managedAccounts and nextValidId. Startup
// read at most ten messages, so from four open orders on nextValidId fell
// past the cap and every connect failed: the live daemon after five GTC
// trailing stops on 2026-10-19 (nextValidId thirteenth). Startup reads until
// nextValidId, and every open order in the burst reaches the order handlers.
// Three and four are the old cap's last pass and first failure.
func TestConnectWithOpenOrdersInTheStartupBurst(t *testing.T) {
	for _, orders := range []int{0, 3, 4, 5, 50} {
		t.Run(fmt.Sprintf("%d open orders", orders), func(t *testing.T) {
			conn := startupTestConnection(serveStartup(t, startupBurst(orders, true)), 1)
			var mu sync.Mutex
			perms := map[int]string{} // permId -> OCA group
			statuses := 0
			conn.RegisterHandler(msgOpenOrder, func(fields []string) {
				ev, ok := ParseOrderLifecycleEvent(fields)
				if !ok {
					return
				}
				mu.Lock()
				perms[ev.PermID] = ev.OcaGroup
				mu.Unlock()
			})
			conn.RegisterHandler(msgOrderStatus, func([]string) {
				mu.Lock()
				statuses++
				mu.Unlock()
			})
			err := conn.Connect(t.Context())
			t.Cleanup(func() { _ = conn.Disconnect() })
			if err != nil {
				t.Fatalf("Connect with %d open orders: %v", orders, err)
			}
			if !conn.IsConnected() || !conn.BrokerIDNamespaceReady() {
				t.Fatalf("connected=%t namespace ready=%t, want both", conn.IsConnected(), conn.BrokerIDNamespaceReady())
			}
			deadline := time.Now().Add(2 * time.Second)
			for {
				mu.Lock()
				seen, seenStatuses := len(perms), statuses
				mu.Unlock()
				if seen == orders && seenStatuses == orders {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("observed %d open orders and %d order statuses, want %d each", seen, seenStatuses, orders)
				}
				time.Sleep(5 * time.Millisecond)
			}
			mu.Lock()
			defer mu.Unlock()
			for i := range orders {
				if group, ok := perms[700000+i]; !ok || group != "oca-synthetic-1" {
					t.Fatalf("open order perm %d: seen=%t group=%q", 700000+i, ok, group)
				}
			}
		})
	}
}

// A burst that never carries nextValidId fails startup, and the failure says
// how many messages the Gateway sent after START_API: the session was
// accepted and talking, which the daemon reports instead of blaming the
// Gateway's socket settings.
func TestStartupWithoutNextValidIDNamesWhatWasRead(t *testing.T) {
	conn := startupTestConnection(serveStartup(t, startupBurst(5, false)), 1)
	conn.startupReadBudget = 300 * time.Millisecond
	connector := &Connector{conn: conn}
	done := make(chan error, 1)
	go func() { done <- conn.Connect(t.Context()) }()
	// The failure is readable while Connect still backs off before its retry.
	deadline := time.Now().Add(2 * time.Second)
	messages, err := connector.SessionStartFailure()
	for err == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		messages, err = connector.SessionStartFailure()
	}
	// 5 openOrder + 5 orderStatus + openOrderEnd + managedAccounts + 3 farm notices.
	if messages != 15 || err == nil || !strings.Contains(err.Error(), "nextValidId not received during startup after 15 messages within 300ms") {
		t.Fatalf("session start failure = %d, %v; want 15 messages and the budget named", messages, err)
	}
	if connectErr := <-done; connectErr == nil {
		t.Fatal("Connect without nextValidId succeeded")
	}
	_ = conn.Disconnect()
}

// A later attempt that fails otherwise replaces the recorded failure: a
// Gateway that answered and then stopped listening, refused the client id or
// hung up is reported as that, not as the session that did not start (a
// stale record turned the terminal 326 verdict into "did not start").
func TestSessionStartFailureIsTheLatestAttempts(t *testing.T) {
	refuse326 := func(s *twsGatewaySession) {
		s.write(twsGatewayFrame(msgErrMsg, "2", "-1", "326", "Unable to connect as the client id is already in use. Retry with a unique client id."))
		_ = s.c.Close()
	}
	hangUp := func(s *twsGatewaySession) { _ = s.c.Close() }
	for _, tc := range []struct {
		name    string
		retry   func(*twsGatewaySession) // nil: the listener closes before the retry
		wantErr error
	}{
		{"refused dial", nil, nil},
		{"client id in use", refuse326, errClientIDInUse},
		{"closed after START_API", hangUp, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scripts := []func(*twsGatewaySession){startupBurst(5, false)}
			if tc.retry != nil {
				scripts = append(scripts, tc.retry)
			}
			ln := serveStartup(t, scripts...)
			conn := startupTestConnection(ln, 2)
			conn.startupReadBudget = 200 * time.Millisecond
			done := make(chan error, 1)
			go func() { done <- conn.Connect(t.Context()) }()
			deadline := time.Now().Add(2 * time.Second)
			for _, err := conn.SessionStartFailure(); err == nil; _, err = conn.SessionStartFailure() {
				if time.Now().After(deadline) {
					t.Fatal("first attempt recorded no session-start failure")
				}
				time.Sleep(10 * time.Millisecond)
			}
			if tc.retry == nil {
				_ = ln.Close() // the retry after the 2 s backoff finds nothing listening
			}
			err := <-done
			if err == nil || (tc.wantErr != nil && !errors.Is(err, tc.wantErr)) {
				t.Fatalf("Connect = %v, want a failure wrapping %v", err, tc.wantErr)
			}
			if messages, err := conn.SessionStartFailure(); err != nil || messages != 0 {
				t.Fatalf("after the retry: session start failure = %d, %v; want none", messages, err)
			}
			_ = conn.Disconnect()
		})
	}
}

// Startup also waits for managedAccounts when it follows nextValidId, as the
// old one-second read did, so the account is known when Connect returns.
func TestStartupWaitsForManagedAccountsAfterNextValidID(t *testing.T) {
	conn := startupTestConnection(serveStartup(t, func(s *twsGatewaySession) {
		s.write(twsGatewayFrame(msgNextValidID, "1", "100"))
		time.Sleep(100 * time.Millisecond)
		s.write(twsGatewayFrame(msgManagedAccts, "1", "DU1234567"))
	}), 1)
	if err := conn.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Disconnect() })
	if got := conn.GetAccountCode(); got != "DU1234567" {
		t.Fatalf("account when Connect returned = %q", got)
	}
}

// The budget counts time spent waiting on the wire, not the handlers' work on
// each message: a slow order journal must not turn a burst the Gateway sent
// in full into "nextValidId not received". Fifty orders overflow the read
// buffer, so the socket is read again after the handlers' 500 ms.
func TestStartupBudgetCountsOnlyWireTime(t *testing.T) {
	conn := startupTestConnection(serveStartup(t, startupBurst(50, true)), 1)
	conn.startupReadBudget = 300 * time.Millisecond
	conn.RegisterHandler(msgOpenOrder, func([]string) { time.Sleep(10 * time.Millisecond) })
	if err := conn.Connect(t.Context()); err != nil {
		t.Fatalf("Connect with 500 ms of handler work under a 300 ms wire budget: %v", err)
	}
	_ = conn.Disconnect()
}

// A 326 on a socket the Gateway leaves open ends startup at once instead of
// waiting out the budget.
func TestStartupEndsAtClientIDInUse(t *testing.T) {
	conn := startupTestConnection(serveStartup(t, func(s *twsGatewaySession) {
		s.write(twsGatewayFrame(msgErrMsg, "2", "-1", "326", "Unable to connect as the client id is already in use. Retry with a unique client id."))
	}), 1)
	began := time.Now()
	err := conn.Connect(t.Context())
	_ = conn.Disconnect()
	if !errors.Is(err, errClientIDInUse) {
		t.Fatalf("Connect = %v, want client id in use", err)
	}
	if elapsed := time.Since(began); elapsed > 2*time.Second {
		t.Fatalf("326 took %s to end startup", elapsed)
	}
}
