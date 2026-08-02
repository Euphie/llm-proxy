package gateway

import (
	"net/http"
	"sync"
	"sync/atomic"

	"github.com/Euphie/llm-proxy/internal/profile"
)

type Builder func(profile.Record) (http.Handler, error)

type runtime struct {
	record  profile.Record
	handler http.Handler
}

type snapshot struct {
	defaultID int64
	byID      map[int64]*runtime
	bySlug    map[string]*runtime
}

type Registry struct {
	mu      sync.Mutex
	current atomic.Pointer[snapshot]
}

type preparedSnapshot struct {
	next *snapshot
}

func NewRegistry() *Registry {
	registry := &Registry{}
	registry.current.Store(emptySnapshot(0))
	return registry
}

func (r *Registry) Load(records []profile.Record, defaultID int64, build Builder) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	next := emptySnapshot(defaultID)
	for _, record := range records {
		item, err := buildRuntime(record, build)
		if err != nil {
			return err
		}
		next.byID[record.ID] = item
		next.bySlug[record.Slug] = item
	}
	r.current.Store(next)
	return nil
}

func (r *Registry) Upsert(record profile.Record, defaultID int64, build Builder) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	current := r.load()
	next := &snapshot{
		defaultID: defaultID,
		byID:      cloneRuntimeMap(current.byID),
		bySlug:    cloneRuntimeMap(current.bySlug),
	}
	if previous := next.byID[record.ID]; previous != nil {
		delete(next.bySlug, previous.record.Slug)
	}

	item, err := buildRuntime(record, build)
	if err != nil {
		return err
	}
	next.byID[record.ID] = item
	next.bySlug[record.Slug] = item
	r.current.Store(next)
	return nil
}

func (r *Registry) prepareUpsert(
	record profile.Record,
	defaultID int64,
	build Builder,
) (*preparedSnapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	current := r.load()
	next := &snapshot{
		defaultID: defaultID,
		byID:      cloneRuntimeMap(current.byID),
		bySlug:    cloneRuntimeMap(current.bySlug),
	}
	if previous := next.byID[record.ID]; previous != nil {
		delete(next.bySlug, previous.record.Slug)
	}
	item, err := buildRuntime(record, build)
	if err != nil {
		return nil, err
	}
	next.byID[record.ID] = item
	next.bySlug[record.Slug] = item
	return &preparedSnapshot{next: next}, nil
}

func (r *Registry) publishPrepared(prepared *preparedSnapshot) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.current.Store(prepared.next)
}

func (r *Registry) Delete(id, defaultID int64) {
	r.mu.Lock()
	defer r.mu.Unlock()

	current := r.load()
	next := &snapshot{
		defaultID: defaultID,
		byID:      cloneRuntimeMap(current.byID),
		bySlug:    cloneRuntimeMap(current.bySlug),
	}
	if previous := next.byID[id]; previous != nil {
		delete(next.bySlug, previous.record.Slug)
		delete(next.byID, id)
	}
	r.current.Store(next)
}

func (r *Registry) SetDefault(id int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	current := r.load()
	item := current.byID[id]
	if item == nil {
		return profile.ErrNotFound
	}
	if item.handler == nil {
		return profile.ErrDefaultRequired
	}
	r.current.Store(&snapshot{
		defaultID: id,
		byID:      current.byID,
		bySlug:    current.bySlug,
	})
	return nil
}

func (r *Registry) load() *snapshot {
	if current := r.current.Load(); current != nil {
		return current
	}
	return emptySnapshot(0)
}

func buildRuntime(record profile.Record, build Builder) (*runtime, error) {
	item := &runtime{record: record}
	if !record.Enabled {
		return item, nil
	}
	handler, err := build(record)
	if err != nil {
		return nil, err
	}
	item.handler = handler
	return item, nil
}

func emptySnapshot(defaultID int64) *snapshot {
	return &snapshot{
		defaultID: defaultID,
		byID:      make(map[int64]*runtime),
		bySlug:    make(map[string]*runtime),
	}
}

func cloneRuntimeMap[K comparable](source map[K]*runtime) map[K]*runtime {
	cloned := make(map[K]*runtime, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}
