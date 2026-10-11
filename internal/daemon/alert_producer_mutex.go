package daemon

import (
	"context"
	"sync"
)

// alertProducerMutex serializes observations without letting a queued producer
// outlive its deadline. Read-only snapshots retain ordinary mutex semantics.
type alertProducerMutex struct {
	once sync.Once
	held chan struct{}
}

func (m *alertProducerMutex) lockContext(ctx context.Context) error {
	m.once.Do(func() { m.held = make(chan struct{}, 1) })
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case m.held <- struct{}{}:
		if err := ctx.Err(); err != nil {
			m.Unlock()
			return err
		}
		return nil
	}
}

// Lock waits for exclusive access without a producer deadline.
func (m *alertProducerMutex) Lock() {
	_ = m.lockContext(context.Background())
}

// Unlock releases exclusive access.
func (m *alertProducerMutex) Unlock() {
	<-m.held
}
