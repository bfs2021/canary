package ibkr

import (
	"context"
	"strings"
	"sync"
	"time"
)

type optionSubscriptionFlight struct {
	done chan struct{}
	err  error
}

// SubscribeOption holds a shared streaming line for one exact option. Readers
// of different trading classes must supply the class explicitly. Each success
// must be paired with UnsubscribeMarketData. Concurrent daemon readers should
// prefer HoldOption, whose release remains bound across session replacement.
func (c *Connector) SubscribeOption(ctx context.Context, underlying, tradingClass, expiryYMD string, strike float64, right string) (string, int, error) {
	key, id, _, err := c.acquireOption(ctx, underlying, tradingClass, expiryYMD, strike, right)
	return key, id, err
}

// HoldOption acquires one shared option reader and its idempotent release.
// The release cannot cancel a successor line even if a new socket reuses the
// same request ID. Each successful hold must be released by its caller.
func (c *Connector) HoldOption(ctx context.Context, underlying, tradingClass, expiryYMD string, strike float64, right string) (string, func() error, error) {
	key, _, sub, err := c.acquireOption(ctx, underlying, tradingClass, expiryYMD, strike, right)
	if err != nil {
		return "", nil, err
	}
	var once sync.Once
	var releaseErr error
	return key, func() error {
		once.Do(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			releaseErr = c.unsubscribeMarketData(ctx, key, nil, sub)
		})
		return releaseErr
	}, nil
}

func (c *Connector) acquireOption(ctx context.Context, underlying, tradingClass, expiryYMD string, strike float64, right string) (string, int, *Subscription, error) {
	underlying = strings.ToUpper(strings.TrimSpace(underlying))
	tradingClass = strings.ToUpper(strings.TrimSpace(tradingClass))
	if tradingClass == "" {
		tradingClass = underlying
	}
	key := optionMarketDataKeyForClass(underlying, tradingClass, expiryYMD, right, strike)
	for {
		if err := ctx.Err(); err != nil {
			return "", 0, nil, err
		}
		c.subMu.Lock()
		if pending := c.optionPending[key]; pending != nil {
			c.subMu.Unlock()
			select {
			case <-ctx.Done():
				return "", 0, nil, ctx.Err()
			case <-pending.done:
				if pending.err != nil {
					return "", 0, nil, pending.err
				}
				continue
			}
		}
		if sub := c.subscriptions[key]; sub != nil && sub.optionReaders > 0 && c.SessionCurrent(sub.optionSession) {
			sub.optionReaders++
			id := sub.ReqID
			c.subMu.Unlock()
			return key, id, sub, nil
		}
		if c.optionPending == nil {
			c.optionPending = make(map[string]*optionSubscriptionFlight)
		}
		pending := &optionSubscriptionFlight{done: make(chan struct{})}
		c.optionPending[key] = pending
		c.subMu.Unlock()

		// Contract resolution and slot admission happen once per key, without
		// holding the subscription mutex. Other contracts continue independently.
		actual, id, err := c.subscribeOption(ctx, underlying, tradingClass, expiryYMD, strike, right)
		c.subMu.Lock()
		sub := c.subscriptions[actual]
		if err == nil && (sub == nil || sub.ReqID != id || !c.SessionCurrent(sub.optionSession)) {
			err = ErrIBKRUnavailable
		}
		pending.err = err
		delete(c.optionPending, key)
		close(pending.done)
		c.subMu.Unlock()
		return actual, id, sub, err
	}
}
