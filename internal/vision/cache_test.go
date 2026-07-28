package vision

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestResultCacheCoalescesConcurrentLoads(t *testing.T) {
	cache := newResultCache(8, time.Minute)
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32

	load := func(context.Context) (string, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		return "description", nil
	}

	type result struct {
		value  string
		source resultSource
		err    error
	}
	results := make(chan result, 2)
	for range 2 {
		go func() {
			value, source, err := cache.getOrLoad(context.Background(), "same", load)
			results <- result{value: value, source: source, err: err}
		}()
	}
	<-started
	waitForResultCacheWaiters(t, cache, "same", 2)
	close(release)

	first, second := <-results, <-results
	if first.err != nil || second.err != nil {
		t.Fatalf("errors: %v, %v", first.err, second.err)
	}
	if calls.Load() != 1 || first.value != "description" || second.value != "description" {
		t.Fatalf("calls=%d results=%+v %+v", calls.Load(), first, second)
	}
	if !((first.source == sourceLoaded && second.source == sourceShared) ||
		(first.source == sourceShared && second.source == sourceLoaded)) {
		t.Fatalf("sources=%v, %v; want one loaded and one shared", first.source, second.source)
	}
}

func TestResultCacheExpiresEntries(t *testing.T) {
	cache := newResultCache(8, time.Minute)
	now := time.Date(2026, time.July, 28, 12, 0, 0, 0, time.UTC)
	cache.now = func() time.Time { return now }
	var calls atomic.Int32
	load := func(context.Context) (string, error) {
		return "value", nil
	}
	countedLoad := func(ctx context.Context) (string, error) {
		calls.Add(1)
		return load(ctx)
	}

	if value, source, err := cache.getOrLoad(context.Background(), "key", countedLoad); err != nil || value != "value" || source != sourceLoaded {
		t.Fatalf("first load: value=%q source=%v err=%v", value, source, err)
	}
	now = now.Add(time.Minute - time.Nanosecond)
	if value, source, err := cache.getOrLoad(context.Background(), "key", countedLoad); err != nil || value != "value" || source != sourceCache {
		t.Fatalf("before expiry: value=%q source=%v err=%v", value, source, err)
	}
	now = now.Add(time.Nanosecond)
	if value, source, err := cache.getOrLoad(context.Background(), "key", countedLoad); err != nil || value != "value" || source != sourceLoaded {
		t.Fatalf("at expiry: value=%q source=%v err=%v", value, source, err)
	}
	if calls.Load() != 2 {
		t.Fatalf("load calls=%d, want 2", calls.Load())
	}
}

func TestResultCacheEvictsLeastRecentlyUsed(t *testing.T) {
	cache := newResultCache(2, time.Minute)
	var calls atomic.Int32
	load := func(context.Context) (string, error) {
		return "value", nil
	}
	loadKey := func(key string) resultSource {
		_, source, err := cache.getOrLoad(context.Background(), key, func(ctx context.Context) (string, error) {
			calls.Add(1)
			return load(ctx)
		})
		if err != nil {
			t.Fatalf("load %q: %v", key, err)
		}
		return source
	}

	if source := loadKey("a"); source != sourceLoaded {
		t.Fatalf("first a source=%v, want loaded", source)
	}
	if source := loadKey("b"); source != sourceLoaded {
		t.Fatalf("first b source=%v, want loaded", source)
	}
	if source := loadKey("a"); source != sourceCache {
		t.Fatalf("second a source=%v, want cache", source)
	}
	if source := loadKey("c"); source != sourceLoaded {
		t.Fatalf("first c source=%v, want loaded", source)
	}
	if source := loadKey("a"); source != sourceCache {
		t.Fatalf("third a source=%v, want cache", source)
	}
	if source := loadKey("b"); source != sourceLoaded {
		t.Fatalf("second b source=%v, want loaded after eviction", source)
	}
	if calls.Load() != 4 {
		t.Fatalf("load calls=%d, want 4", calls.Load())
	}
}

func TestResultCacheDoesNotCacheFailures(t *testing.T) {
	cache := newResultCache(8, time.Minute)
	loadErr := errors.New("load failed")
	var calls atomic.Int32
	load := func(context.Context) (string, error) {
		if calls.Add(1) == 1 {
			return "", loadErr
		}
		return "recovered", nil
	}

	if value, source, err := cache.getOrLoad(context.Background(), "key", load); value != "" || source != sourceLoaded || !errors.Is(err, loadErr) {
		t.Fatalf("failed load: value=%q source=%v err=%v", value, source, err)
	}
	if value, source, err := cache.getOrLoad(context.Background(), "key", load); value != "recovered" || source != sourceLoaded || err != nil {
		t.Fatalf("retry: value=%q source=%v err=%v", value, source, err)
	}
	if calls.Load() != 2 {
		t.Fatalf("load calls=%d, want 2", calls.Load())
	}
}

func TestResultCacheOneCanceledWaiterDoesNotCancelSharedLoad(t *testing.T) {
	cache := newResultCache(8, time.Minute)
	started := make(chan struct{})
	release := make(chan struct{})
	loaderCanceled := make(chan struct{}, 1)
	load := func(ctx context.Context) (string, error) {
		close(started)
		select {
		case <-release:
			return "description", nil
		case <-ctx.Done():
			loaderCanceled <- struct{}{}
			return "", ctx.Err()
		}
	}

	firstCtx, cancelFirst := context.WithCancel(context.Background())
	firstResult := make(chan error, 1)
	go func() {
		_, _, err := cache.getOrLoad(firstCtx, "key", load)
		firstResult <- err
	}()
	<-started

	secondResult := make(chan struct {
		value  string
		source resultSource
		err    error
	}, 1)
	go func() {
		value, source, err := cache.getOrLoad(context.Background(), "key", load)
		secondResult <- struct {
			value  string
			source resultSource
			err    error
		}{value: value, source: source, err: err}
	}()
	waitForResultCacheWaiters(t, cache, "key", 2)

	cancelFirst()
	if err := <-firstResult; !errors.Is(err, context.Canceled) {
		t.Fatalf("first waiter error=%v, want context canceled", err)
	}
	select {
	case <-loaderCanceled:
		t.Fatal("shared loader was canceled while another waiter remained")
	default:
	}

	close(release)
	second := <-secondResult
	if second.err != nil || second.value != "description" || second.source != sourceShared {
		t.Fatalf("second waiter: value=%q source=%v err=%v", second.value, second.source, second.err)
	}
}

func TestResultCacheAllCanceledWaitersCancelSharedLoad(t *testing.T) {
	cache := newResultCache(8, time.Minute)
	started := make(chan struct{})
	loaderCanceled := make(chan struct{})
	load := func(ctx context.Context) (string, error) {
		close(started)
		<-ctx.Done()
		close(loaderCanceled)
		return "", ctx.Err()
	}

	firstCtx, cancelFirst := context.WithCancel(context.Background())
	secondCtx, cancelSecond := context.WithCancel(context.Background())
	results := make(chan error, 2)
	go func() {
		_, _, err := cache.getOrLoad(firstCtx, "key", load)
		results <- err
	}()
	<-started
	go func() {
		_, _, err := cache.getOrLoad(secondCtx, "key", load)
		results <- err
	}()
	waitForResultCacheWaiters(t, cache, "key", 2)

	cancelFirst()
	select {
	case <-loaderCanceled:
		t.Fatal("loader canceled before the last waiter left")
	default:
	}
	cancelSecond()

	select {
	case <-loaderCanceled:
	case <-time.After(time.Second):
		t.Fatal("loader was not canceled after all waiters left")
	}
	for range 2 {
		if err := <-results; !errors.Is(err, context.Canceled) {
			t.Fatalf("waiter error=%v, want context canceled", err)
		}
	}
}

func TestResultCacheDoesNotJoinCanceledInflightCall(t *testing.T) {
	cache := newResultCache(8, time.Minute)
	oldStarted := make(chan struct{})
	oldCanceled := make(chan struct{})
	releaseOld := make(chan struct{})
	oldReleased := false
	defer func() {
		if !oldReleased {
			close(releaseOld)
		}
	}()
	oldLoad := func(ctx context.Context) (string, error) {
		close(oldStarted)
		<-ctx.Done()
		close(oldCanceled)
		<-releaseOld
		return "stale", nil
	}

	firstCtx, cancelFirst := context.WithCancel(context.Background())
	firstResult := make(chan error, 1)
	go func() {
		_, _, err := cache.getOrLoad(firstCtx, "key", oldLoad)
		firstResult <- err
	}()
	<-oldStarted

	cache.mu.Lock()
	oldCall := cache.inflight["key"]
	cache.mu.Unlock()
	if oldCall == nil {
		t.Fatal("old call is not inflight")
	}

	cancelFirst()
	if err := <-firstResult; !errors.Is(err, context.Canceled) {
		t.Fatalf("first waiter error=%v, want context canceled", err)
	}
	select {
	case <-oldCanceled:
	case <-time.After(time.Second):
		t.Fatal("old loader did not observe cancellation")
	}

	newStarted := make(chan struct{}, 2)
	releaseNew := make(chan struct{})
	newReleased := false
	defer func() {
		if !newReleased {
			close(releaseNew)
		}
	}()
	var newCalls atomic.Int32
	newLoad := func(context.Context) (string, error) {
		newCalls.Add(1)
		newStarted <- struct{}{}
		<-releaseNew
		return "fresh", nil
	}
	type result struct {
		value  string
		source resultSource
		err    error
	}
	secondResult := make(chan result, 1)
	go func() {
		value, source, err := cache.getOrLoad(context.Background(), "key", newLoad)
		secondResult <- result{value: value, source: source, err: err}
	}()
	select {
	case <-newStarted:
	case <-time.After(time.Second):
		t.Fatal("new request joined the canceled loader instead of starting a replacement")
	}

	cache.mu.Lock()
	newCall := cache.inflight["key"]
	cache.mu.Unlock()
	if newCall == nil || newCall == oldCall {
		t.Fatal("replacement call is not independently inflight")
	}

	oldReleased = true
	close(releaseOld)
	select {
	case <-oldCall.done:
	case <-time.After(time.Second):
		t.Fatal("old loader did not finish")
	}

	cache.mu.Lock()
	currentCall := cache.inflight["key"]
	_, staleCached := cache.entries["key"]
	cache.mu.Unlock()
	if currentCall != newCall {
		t.Fatal("old loader completion removed the replacement call")
	}
	if staleCached {
		t.Fatal("old loader published a result after losing all waiters")
	}

	thirdResult := make(chan result, 1)
	go func() {
		value, source, err := cache.getOrLoad(context.Background(), "key", newLoad)
		thirdResult <- result{value: value, source: source, err: err}
	}()
	waitForResultCacheWaiters(t, cache, "key", 2)

	newReleased = true
	close(releaseNew)
	second, third := <-secondResult, <-thirdResult
	if second.err != nil || second.value != "fresh" || second.source != sourceLoaded {
		t.Fatalf("replacement owner: value=%q source=%v err=%v", second.value, second.source, second.err)
	}
	if third.err != nil || third.value != "fresh" || third.source != sourceShared {
		t.Fatalf("replacement waiter: value=%q source=%v err=%v", third.value, third.source, third.err)
	}
	if newCalls.Load() != 1 {
		t.Fatalf("replacement load calls=%d, want 1", newCalls.Load())
	}

	if value, source, err := cache.getOrLoad(context.Background(), "key", func(context.Context) (string, error) {
		t.Fatal("fresh cached result unexpectedly reloaded")
		return "", nil
	}); err != nil || value != "fresh" || source != sourceCache {
		t.Fatalf("cache after replacement: value=%q source=%v err=%v", value, source, err)
	}
}

func waitForResultCacheWaiters(t *testing.T, cache *resultCache, key string, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		cache.mu.Lock()
		call := cache.inflight[key]
		got := 0
		if call != nil {
			got = call.waiters
		}
		cache.mu.Unlock()
		if got == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d waiters for %q", want, key)
}
