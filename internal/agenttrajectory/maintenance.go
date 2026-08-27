package agenttrajectory

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"
)

const defaultMaintenanceInterval = time.Minute

type Maintenance struct {
	store *Store
	now   func() time.Time

	mu      sync.Mutex
	started bool
	closed  bool
	cancel  context.CancelFunc
	done    chan struct{}
	sweepMu sync.Mutex
}

func NewMaintenance(store *Store, now func() time.Time) *Maintenance {
	if now == nil {
		now = time.Now
	}
	return &Maintenance{store: store, now: now}
}

func (m *Maintenance) Sweep(ctx context.Context) error {
	if m == nil || m.store == nil {
		return nil
	}
	m.sweepMu.Lock()
	defer m.sweepMu.Unlock()
	_, timeoutErr := m.store.TimeoutIdle(ctx, m.now().UTC().Add(-defaultIdleTimeout))
	_, deleteErr := m.store.DeleteExpired(ctx)
	return errors.Join(timeoutErr, deleteErr)
}

func (m *Maintenance) Start(interval time.Duration) {
	if m == nil {
		return
	}
	if interval <= 0 {
		interval = defaultMaintenanceInterval
	}
	m.mu.Lock()
	if m.started || m.closed {
		m.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.started = true
	m.cancel = cancel
	m.done = make(chan struct{})
	done := m.done
	m.mu.Unlock()

	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := m.Sweep(ctx); err != nil && ctx.Err() == nil {
					slog.Error("Agent trajectory maintenance failed", "error", err)
				}
			case <-ctx.Done():
				return
			}
		}
	}()
}

func (m *Maintenance) Close() {
	if m == nil {
		return
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	cancel := m.cancel
	done := m.done
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
}
