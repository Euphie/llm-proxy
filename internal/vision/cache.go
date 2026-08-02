package vision

import (
	"container/list"
	"context"
	"sync"
	"time"
)

type resultSource uint8

const (
	sourceCache resultSource = iota
	sourceLoaded
	sourceShared
)

type cacheEntry struct {
	key       string
	value     string
	expiresAt time.Time
}

type inflightCall struct {
	owner   context.Context
	ctx     context.Context
	cancel  context.CancelFunc
	done    chan struct{}
	waiters int
	trace   cacheLoadTrace
	value   string
	err     error
}

type cacheLoadTrace struct {
	callID       string
	ownerTraceID string
}

type resultCache struct {
	mu       sync.Mutex
	capacity int
	ttl      time.Duration
	now      func() time.Time
	lru      *list.List
	entries  map[string]*list.Element
	inflight map[string]*inflightCall
}

func newResultCache(capacity int, ttl time.Duration) *resultCache {
	return &resultCache{
		capacity: capacity,
		ttl:      ttl,
		now:      time.Now,
		lru:      list.New(),
		entries:  make(map[string]*list.Element),
		inflight: make(map[string]*inflightCall),
	}
}

func (c *resultCache) getOrLoad(
	ctx context.Context,
	key string,
	load func(context.Context) (string, error),
) (string, resultSource, error) {
	value, source, _, err := c.getOrLoadTrace(ctx, key, load)
	return value, source, err
}

func (c *resultCache) getOrLoadTrace(
	ctx context.Context,
	key string,
	load func(context.Context) (string, error),
) (string, resultSource, cacheLoadTrace, error) {
	for {
		c.mu.Lock()
		if value, ok := c.getLocked(key); ok {
			c.mu.Unlock()
			return value, sourceCache, cacheLoadTrace{}, nil
		}

		call := c.inflight[key]
		owner := false
		if call != nil && call.owner.Err() != nil {
			delete(c.inflight, key)
			call.cancel()
			call = nil
		}
		if call == nil {
			callCtx, cancel := context.WithCancel(ctx)
			ownerTraceID := requestTraceID(ctx)
			trace := cacheLoadTrace{
				callID:       newOpaqueTraceID(),
				ownerTraceID: ownerTraceID,
			}
			call = &inflightCall{
				owner:   ctx,
				ctx:     withLoadTrace(callCtx, trace.callID, ownerTraceID),
				cancel:  cancel,
				done:    make(chan struct{}),
				waiters: 1,
				trace:   trace,
			}
			c.inflight[key] = call
			owner = true
			go c.runLoad(key, call, load)
		} else {
			call.waiters++
		}
		c.mu.Unlock()

		if owner {
			<-call.done
			return call.value, sourceLoaded, call.trace, call.err
		}

		select {
		case <-call.done:
			if call.owner.Err() != nil && ctx.Err() == nil {
				continue
			}
			return call.value, sourceShared, call.trace, call.err
		case <-call.owner.Done():
			c.mu.Lock()
			if c.inflight[key] == call {
				delete(c.inflight, key)
				call.cancel()
			}
			call.waiters--
			c.mu.Unlock()
			if err := ctx.Err(); err != nil {
				return "", sourceShared, call.trace, err
			}
			continue
		case <-ctx.Done():
			c.mu.Lock()
			call.waiters--
			c.mu.Unlock()
			return "", sourceShared, call.trace, ctx.Err()
		}
	}
}

func (c *resultCache) runLoad(
	key string,
	call *inflightCall,
	load func(context.Context) (string, error),
) {
	value, err := load(call.ctx)

	c.mu.Lock()
	if ownerErr := call.owner.Err(); ownerErr != nil {
		value = ""
		err = ownerErr
	}
	if c.inflight[key] == call {
		if err == nil && value != "" {
			c.putLocked(key, value)
		}
		delete(c.inflight, key)
	}
	call.value = value
	call.err = err
	close(call.done)
	call.cancel()
	c.mu.Unlock()
}

func (c *resultCache) getLocked(key string) (string, bool) {
	element, ok := c.entries[key]
	if !ok {
		return "", false
	}
	entry := element.Value.(*cacheEntry)
	if !c.now().Before(entry.expiresAt) {
		c.removeLocked(element)
		return "", false
	}
	c.lru.MoveToFront(element)
	return entry.value, true
}

func (c *resultCache) putLocked(key, value string) {
	if c.capacity <= 0 {
		return
	}

	expiresAt := c.now().Add(c.ttl)
	if element, ok := c.entries[key]; ok {
		entry := element.Value.(*cacheEntry)
		entry.value = value
		entry.expiresAt = expiresAt
		c.lru.MoveToFront(element)
		return
	}

	element := c.lru.PushFront(&cacheEntry{
		key:       key,
		value:     value,
		expiresAt: expiresAt,
	})
	c.entries[key] = element
	if c.lru.Len() > c.capacity {
		c.removeLocked(c.lru.Back())
	}
}

func (c *resultCache) removeLocked(element *list.Element) {
	entry := element.Value.(*cacheEntry)
	delete(c.entries, entry.key)
	c.lru.Remove(element)
}
