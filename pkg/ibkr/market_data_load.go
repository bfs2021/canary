package ibkr

import "time"

// MarketDataLoad is a read-only view of the connection's market-data
// pressure, for background readers that must yield to interactive work.
type MarketDataLoad struct {
	// LinesHeld and LinesCapacity are the market-data subscription slots in
	// use and the pool's size.
	LinesHeld     int
	LinesCapacity int
	// LastPacingError is when the broker last answered any request on this
	// connection with a pacing error (code 100); zero when none was seen.
	LastPacingError time.Time
}

// MarketDataLoad reports the current connection's line usage and pacing
// state. It sends nothing. False when no connection is attached.
func (c *Connector) MarketDataLoad() (MarketDataLoad, bool) {
	if c == nil {
		return MarketDataLoad{}, false
	}
	c.mu.RLock()
	conn := c.conn
	c.mu.RUnlock()
	if conn == nil || conn.rateLimiter == nil {
		return MarketDataLoad{}, false
	}
	rl := conn.rateLimiter
	metrics := rl.GetMetrics()
	return MarketDataLoad{
		LinesHeld:       rl.marketDataSubs.Count(),
		LinesCapacity:   cap(rl.marketDataSubs.ch),
		LastPacingError: metrics.LastRateLimitError,
	}, true
}
