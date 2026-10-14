package ibkr

import (
	"context"
	"testing"
)

// The load view counts held lines and the last pacing error without sending.
func TestMarketDataLoadReportsLinesAndPacing(t *testing.T) {
	if _, ok := (&Connector{}).MarketDataLoad(); ok {
		t.Fatal("a connector without a connection reported load")
	}
	conn := NewConnection(&ConnectionConfig{Host: "127.0.0.1", Port: 7497, ClientID: 41})
	t.Cleanup(conn.rateLimiter.Stop)
	connector := &Connector{conn: conn}
	load, ok := connector.MarketDataLoad()
	if !ok || load.LinesHeld != 0 || load.LinesCapacity != 100 || !load.LastPacingError.IsZero() {
		t.Fatalf("idle load = %+v ok=%v", load, ok)
	}
	if err := conn.rateLimiter.AcquireMarketDataSlot(context.Background()); err != nil {
		t.Fatal(err)
	}
	conn.rateLimiter.recordRateLimitError()
	load, _ = connector.MarketDataLoad()
	if load.LinesHeld != 1 || load.LastPacingError.IsZero() {
		t.Fatalf("load = %+v, want one line held and the pacing error", load)
	}
	conn.rateLimiter.ReleaseMarketDataSlot()
}
