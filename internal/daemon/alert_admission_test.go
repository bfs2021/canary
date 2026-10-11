package daemon

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestAlertProducerWaitingForWriterHonorsCancellation(t *testing.T) {
	c := newAlertShadowComposer(&alertEpisodeRegistry{})
	input := alertShadowDataHealthInput{AsOf: time.Now().UTC(), Scope: alertShadowTestBrokerScope(t)}
	c.mu.Lock()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := c.ObserveDataHealth(ctx, input)
		done <- err
	}()
	cancel()
	select {
	case err := <-done:
		c.mu.Unlock()
		if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "wait for alert producer") {
			t.Fatalf("cancelled admission did not identify lock contention: %v", err)
		}
	case <-time.After(250 * time.Millisecond):
		c.mu.Unlock()
		<-done
		t.Fatal("cancelled producer remained blocked behind another writer")
	}
	if len(c.scopes) != 0 {
		t.Fatal("cancelled admission mutated producer state")
	}
}
