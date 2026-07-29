package gateway

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Euphie/llm-proxy/internal/profile"
)

var ErrRuntimeSync = errors.New("profile runtime synchronization failed")

const runtimeSyncTimeout = 5 * time.Second

type ProfileStore interface {
	Save(context.Context, profile.SaveInput, bool) (profile.Record, error)
	LoadSnapshot(context.Context) ([]profile.Record, int64, error)
	Copy(context.Context, int64, string, string) (profile.Record, error)
	SetDefault(context.Context, int64) error
	Delete(context.Context, int64, int64) error
}

type Coordinator struct {
	mu       sync.Mutex
	store    ProfileStore
	registry *Registry
	build    Builder
	dirty    bool
	ready    atomic.Bool
}

func NewCoordinator(
	store ProfileStore,
	registry *Registry,
	build Builder,
) *Coordinator {
	return &Coordinator{store: store, registry: registry, build: build}
}

// Ready reports whether the registry reflects the last known Store snapshot.
func (c *Coordinator) Ready() bool {
	return c.ready.Load()
}

func (c *Coordinator) Save(
	ctx context.Context,
	input profile.SaveInput,
	makeDefault bool,
) (profile.Record, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.repair(ctx); err != nil {
		return profile.Record{}, err
	}
	record, err := c.store.Save(ctx, input, makeDefault)
	if err != nil {
		return profile.Record{}, err
	}
	if err := c.publish(ctx, func(syncCtx context.Context) error {
		return c.upsert(syncCtx, record)
	}); err != nil {
		return record, err
	}
	return record, nil
}

func (c *Coordinator) Copy(
	ctx context.Context,
	id int64,
	slug string,
	displayName string,
) (profile.Record, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.repair(ctx); err != nil {
		return profile.Record{}, err
	}
	record, err := c.store.Copy(ctx, id, slug, displayName)
	if err != nil {
		return profile.Record{}, err
	}
	if err := c.publish(ctx, func(syncCtx context.Context) error {
		return c.upsert(syncCtx, record)
	}); err != nil {
		return record, err
	}
	return record, nil
}

func (c *Coordinator) SetDefault(ctx context.Context, id int64) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.repair(ctx); err != nil {
		return err
	}
	if err := c.store.SetDefault(ctx, id); err != nil {
		return err
	}
	return c.publish(ctx, func(syncCtx context.Context) error {
		if err := c.registry.SetDefault(id); err != nil {
			return c.resync(syncCtx, err)
		}
		return nil
	})
}

func (c *Coordinator) Delete(
	ctx context.Context,
	id int64,
	replacementDefaultID int64,
) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.repair(ctx); err != nil {
		return err
	}
	if err := c.store.Delete(ctx, id, replacementDefaultID); err != nil {
		return err
	}
	return c.publish(ctx, func(syncCtx context.Context) error {
		_, defaultID, err := c.store.LoadSnapshot(syncCtx)
		if err != nil {
			return c.resync(syncCtx, err)
		}
		c.registry.Delete(id, defaultID)
		return nil
	})
}

func (c *Coordinator) Reload(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.markDirty()
	syncCtx, cancel := newRuntimeSyncContext(ctx)
	defer cancel()
	if err := c.reload(syncCtx); err != nil {
		return fmt.Errorf("%w: %w", ErrRuntimeSync, err)
	}
	c.markSynchronized()
	return nil
}

func (c *Coordinator) repair(ctx context.Context) error {
	if !c.dirty {
		return nil
	}
	syncCtx, cancel := newRuntimeSyncContext(ctx)
	defer cancel()
	if err := c.reload(syncCtx); err != nil {
		return fmt.Errorf("%w: repair full resync: %w", ErrRuntimeSync, err)
	}
	c.markSynchronized()
	return nil
}

func (c *Coordinator) publish(
	ctx context.Context,
	publish func(context.Context) error,
) error {
	c.markDirty()
	syncCtx, cancel := newRuntimeSyncContext(ctx)
	defer cancel()
	if err := publish(syncCtx); err != nil {
		return err
	}
	c.markSynchronized()
	return nil
}

func (c *Coordinator) markDirty() {
	c.dirty = true
	c.ready.Store(false)
}

func (c *Coordinator) markSynchronized() {
	c.dirty = false
	c.ready.Store(true)
}

func (c *Coordinator) upsert(ctx context.Context, record profile.Record) error {
	_, defaultID, err := c.store.LoadSnapshot(ctx)
	if err == nil {
		err = c.registry.Upsert(record, defaultID, c.build)
	}
	if err != nil {
		return c.resync(ctx, err)
	}
	return nil
}

func (c *Coordinator) resync(ctx context.Context, publishErr error) error {
	if err := c.reload(ctx); err != nil {
		return runtimeSyncError(publishErr, err)
	}
	return nil
}

func (c *Coordinator) reload(ctx context.Context) error {
	records, defaultID, err := c.store.LoadSnapshot(ctx)
	if err != nil {
		return fmt.Errorf("load Profile snapshot: %w", err)
	}
	if err := c.registry.Load(records, defaultID, c.build); err != nil {
		return fmt.Errorf("publish Profile snapshot: %w", err)
	}
	return nil
}

func runtimeSyncError(publishErr, resyncErr error) error {
	return fmt.Errorf(
		"%w: %w",
		ErrRuntimeSync,
		errors.Join(
			fmt.Errorf("incremental publish: %w", publishErr),
			fmt.Errorf("full resync: %w", resyncErr),
		),
	)
}

func newRuntimeSyncContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), runtimeSyncTimeout)
}
