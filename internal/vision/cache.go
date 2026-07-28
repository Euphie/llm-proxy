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
	ctx     context.Context
	cancel  context.CancelFunc
	done    chan struct{}
	waiters int
	value   string
	err     error
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
	c.mu.Lock()
	if value, ok := c.getLocked(key); ok {
		c.mu.Unlock()
		return value, sourceCache, nil
	}

	call, ok := c.inflight[key]
	source := sourceShared
	if ok {
		call.waiters++
	} else {
		callCtx, cancel := context.WithCancel(context.Background())
		call = &inflightCall{
			ctx:     callCtx,
			cancel:  cancel,
			done:    make(chan struct{}),
			waiters: 1,
		}
		c.inflight[key] = call
		source = sourceLoaded
		go c.runLoad(key, call, load)
	}
	c.mu.Unlock()

	select {
	case <-call.done:
		return call.value, source, call.err
	case <-ctx.Done():
		c.mu.Lock()
		if c.inflight[key] == call {
			call.waiters--
			if call.waiters == 0 {
				delete(c.inflight, key)
				call.cancel()
			}
		}
		c.mu.Unlock()
		return "", source, ctx.Err()
	}
}

func (c *resultCache) runLoad(
	key string,
	call *inflightCall,
	load func(context.Context) (string, error),
) {
	value, err := load(call.ctx)

	c.mu.Lock()
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
