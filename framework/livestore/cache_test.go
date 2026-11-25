package livestore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestCache returns a cache in a store of its own, closed when the test ends.
func newTestCache[V any](t *testing.T, opts ...CacheOption[V]) *Cache[V] {
	t.Helper()
	s, err := New()
	require.NoError(t, err)
	t.Cleanup(s.Close)
	c, err := s.NewCache[V](context.Background(), "test_cache", opts...)
	require.NoError(t, err)
	return c
}

// fillWith returns a loader serving a fixed value and index key.
func fillWith(value string, indexKey string) CacheLoader[string] {
	return func() (string, string, error) {
		return value, indexKey, nil
	}
}

// TestLiveCache_HitAndMiss checks a miss, a fill, and a hit on what was filled.
func TestLiveCache_HitAndMiss(t *testing.T) {
	c := newTestCache[string](t)
	_, ok := c.Get("k1")
	assert.False(t, ok)

	v, err := c.Fill(context.Background(), "k1", fillWith("v1", "id1"))
	require.NoError(t, err)
	assert.Equal(t, "v1", v)

	got, ok := c.Get("k1")
	assert.True(t, ok)
	assert.Equal(t, "v1", got)
	assert.Equal(t, 1, c.Len())
}

// TestLiveCache_ValidatorRejectionIsMissAndRemoval checks an entry the validator rejects reads as a miss and is removed.
func TestLiveCache_ValidatorRejectionIsMissAndRemoval(t *testing.T) {
	type timedValue struct {
		value     string
		expiresAt time.Time
	}
	c := newTestCache(t, WithValidator(func(v timedValue) bool {
		return time.Now().Before(v.expiresAt)
	}))
	_, err := c.Fill(context.Background(), "k1", func() (timedValue, string, error) {
		return timedValue{value: "v1", expiresAt: time.Now().Add(-time.Minute)}, "id1", nil
	})
	require.NoError(t, err)
	assert.Equal(t, 1, c.Len())

	_, ok := c.Get("k1")
	assert.False(t, ok, "rejected entry must be reported as a miss")
	assert.Equal(t, 0, c.Len(), "rejected entry must be removed")
}

// TestLiveCache_GetValidatorRejectionDoesNotEvictConcurrentlyUpdatedEntry covers
// the race the entry.version field exists for: setIfGeneration's
// existing-entry branch updates a value in place on the same *list.Element,
// so element identity alone can't tell a Get (mid-validator, evaluating a
// value it already read) that the entry has since been refreshed. Without
// the version check, rejecting the stale value would evict the fresh one
// installed concurrently.
func TestLiveCache_GetValidatorRejectionDoesNotEvictConcurrentlyUpdatedEntry(t *testing.T) {
	validatorEntered := make(chan struct{})
	releaseValidator := make(chan struct{})
	var pausedOnce atomic.Bool
	pausedOnce.Store(true)

	c := newTestCache(t, WithValidator(func(v string) bool {
		if pausedOnce.CompareAndSwap(true, false) {
			close(validatorEntered)
			<-releaseValidator
		}
		return v != "v1" // only the stale first value is rejected
	}))

	_, err := c.Fill(context.Background(), "k1", fillWith("v1", ""))
	require.NoError(t, err)

	getDone := make(chan struct{})
	var got string
	var ok bool
	go func() {
		defer close(getDone)
		got, ok = c.Get("k1")
	}()
	<-validatorEntered // Get has read "v1" and is now blocked inside validator.

	// While that validator call is still evaluating the stale "v1" it read,
	// a concurrent Fill updates the same key in place to "v2".
	_, err = c.Fill(context.Background(), "k1", fillWith("v2", ""))
	require.NoError(t, err)

	close(releaseValidator)
	<-getDone

	assert.False(t, ok, "the original Get call evaluated the stale value and must report a miss")
	assert.Empty(t, got, "a rejected Get returns the zero value")

	got2, ok2 := c.Get("k1")
	assert.True(t, ok2, "the concurrently-installed fresh value must not have been collaterally evicted")
	assert.Equal(t, "v2", got2)
}

// TestLiveCache_EvictExactKey checks Evict removes one key and leaves the rest.
func TestLiveCache_EvictExactKey(t *testing.T) {
	c := newTestCache[string](t)
	_, err := c.Fill(context.Background(), "k1", fillWith("v1", "id1"))
	require.NoError(t, err)

	c.Evict("k1")
	_, ok := c.Get("k1")
	assert.False(t, ok)
	assert.Equal(t, 0, c.Len())

	// Evicting an absent key is a no-op, not a panic.
	c.Evict("never-set")
}

// TestLiveCache_EvictByIndex checks EvictByIndex removes the entry bound to an index key.
func TestLiveCache_EvictByIndex(t *testing.T) {
	c := newTestCache[string](t)
	_, err := c.Fill(context.Background(), "k1", fillWith("v1", "id1"))
	require.NoError(t, err)
	_, err = c.Fill(context.Background(), "k2", fillWith("v2", "id2"))
	require.NoError(t, err)

	c.EvictByIndex("id1")
	_, ok := c.Get("k1")
	assert.False(t, ok, "entry holding the evicted index key must be gone")
	_, ok = c.Get("k2")
	assert.True(t, ok, "unrelated entry must survive")

	// Unknown and empty index keys are no-ops.
	c.EvictByIndex("unknown")
	c.EvictByIndex("")
	assert.Equal(t, 1, c.Len())
}

// TestLiveCache_EvictWhere checks EvictWhere removes every matching key and only those.
func TestLiveCache_EvictWhere(t *testing.T) {
	c := newTestCache[string](t)
	for _, k := range []string{"a:1", "a:2", "b:1"} {
		_, err := c.Fill(context.Background(), k, fillWith("v", ""))
		require.NoError(t, err)
	}

	c.EvictWhere(func(key string) bool { return key[0] == 'a' })
	_, ok := c.Get("a:1")
	assert.False(t, ok)
	_, ok = c.Get("a:2")
	assert.False(t, ok)
	_, ok = c.Get("b:1")
	assert.True(t, ok)

	// A nil predicate is a no-op, not a panic.
	c.EvictWhere(nil)
	assert.Equal(t, 1, c.Len())
}

// TestLiveCache_EvictWhereGenerationBumpPrecedesPred exercises the ordering
// EvictWhere's generation++ move exists for: a concurrent Fill for a key
// that isn't cached yet (so it can never appear in EvictWhere's keys
// snapshot or its toRemove list) must still be discarded if its generation
// was captured before this EvictWhere call, even though EvictWhere's own
// predicate is still running when the fill completes.
func TestLiveCache_EvictWhereGenerationBumpPrecedesPred(t *testing.T) {
	c := newTestCache[string](t)
	// EvictWhere only invokes pred per cached key, so it needs at least one
	// existing entry to actually run pred (and thus block on it) at all.
	_, err := c.Fill(context.Background(), "existing-key", fillWith("v", ""))
	require.NoError(t, err)

	fillStarted := make(chan struct{})
	releaseFill := make(chan struct{})
	var fillWg sync.WaitGroup
	fillWg.Add(1)
	go func() {
		defer fillWg.Done()
		_, _ = c.Fill(context.Background(), "new-key", func() (string, string, error) {
			close(fillStarted)
			<-releaseFill
			return "fresh-value", "", nil
		})
	}()
	<-fillStarted // The fill has captured its generation and is now blocked
	// inside the loader, before EvictWhere has even started.

	predStarted := make(chan struct{})
	releasePred := make(chan struct{})
	var evictWg sync.WaitGroup
	evictWg.Add(1)
	go func() {
		defer evictWg.Done()
		c.EvictWhere(func(key string) bool {
			close(predStarted)
			<-releasePred
			return false
		})
	}()
	<-predStarted // With the fix, generation is already bumped by this point.

	// Let the fill's loader finish and attempt to install, racing
	// EvictWhere's still-in-progress predicate.
	close(releaseFill)
	fillWg.Wait()

	close(releasePred)
	evictWg.Wait()

	_, ok := c.Get("new-key")
	assert.False(t, ok, "a fill whose generation was captured before EvictWhere's bump must not install after it, even though the key wasn't cached yet when EvictWhere snapshotted its keys")
}

// TestLiveCache_Flush checks Flush drops every entry and index binding.
func TestLiveCache_Flush(t *testing.T) {
	c := newTestCache[string](t)
	for i := 1; i <= 3; i++ {
		_, err := c.Fill(context.Background(), fmt.Sprintf("k%d", i), fillWith("v", fmt.Sprintf("id%d", i)))
		require.NoError(t, err)
	}

	c.Flush()
	assert.Equal(t, 0, c.Len())
	for i := 1; i <= 3; i++ {
		_, ok := c.Get(fmt.Sprintf("k%d", i))
		assert.False(t, ok)
	}
}

// requests reports how many callers have registered for key, for tests that wait until a caller
// is queued behind a running load.
func (g *readGroup) requests(key string) uint64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	if st := g.state[key]; st != nil {
		return st.requested.Load()
	}
	return 0
}

// TestLiveCache_FillCallersQueuedBeforeALoadShareIt checks that callers registered before a load
// starts are all served by that one load.
func TestLiveCache_FillCallersQueuedBeforeALoadShareIt(t *testing.T) {
	c := newTestCache[string](t)
	held, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	c.fills.runLockedHook = func() {
		once.Do(func() {
			close(held)
			<-release
		})
	}
	var calls atomic.Int32
	loader := func() (string, string, error) {
		calls.Add(1)
		return "v1", "id1", nil
	}
	var wg sync.WaitGroup
	results := make([]string, 2)
	wg.Add(1)
	go func() {
		defer wg.Done()
		v, err := c.Fill(context.Background(), "k1", loader)
		assert.NoError(t, err)
		results[0] = v
	}()
	<-held // the first caller holds the key, before its load starts
	wg.Add(1)
	go func() {
		defer wg.Done()
		v, err := c.Fill(context.Background(), "k1", loader)
		assert.NoError(t, err)
		results[1] = v
	}()
	require.Eventually(t, func() bool { return c.fills.requests("k1") == 2 }, time.Second, time.Millisecond)
	close(release)
	wg.Wait()

	assert.Equal(t, int32(1), calls.Load(), "callers queued before the load share it")
	assert.Equal(t, []string{"v1", "v1"}, results)
}

// TestLiveCache_FillAfterAnEvictionDuringALoadGetsANewLoad checks the case a shared load would get
// wrong: a write evicts a key while a load that read the old value runs, and a caller arriving
// after the eviction gets a load that started after it, not the running one.
func TestLiveCache_FillAfterAnEvictionDuringALoadGetsANewLoad(t *testing.T) {
	c := newTestCache[string](t)
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	loader := func() (string, string, error) {
		if calls.Add(1) == 1 {
			close(started)
			<-release
			return "old", "id1", nil // read before the write
		}
		return "new", "id1", nil
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		v, err := c.Fill(context.Background(), "k1", loader)
		assert.NoError(t, err)
		assert.Equal(t, "old", v, "the first caller gets the load it started")
	}()
	<-started
	c.Evict("k1") // the write lands while the first load runs

	late := make(chan string, 1)
	go func() {
		v, err := c.Fill(context.Background(), "k1", loader)
		assert.NoError(t, err)
		late <- v
	}()
	require.Eventually(t, func() bool { return c.fills.requests("k1") == 2 }, time.Second, time.Millisecond)
	close(release)
	wg.Wait()

	assert.Equal(t, "new", <-late, "a caller arriving after the eviction never gets the older load")
	got, ok := c.Get("k1")
	require.True(t, ok)
	assert.Equal(t, "new", got, "the follow-up load is installed, the one that raced the eviction is not")
	assert.Equal(t, int32(2), calls.Load())
}

// TestLiveCache_FillBurstDuringALoadCostsOneMoreLoad checks that any number of callers arriving
// while a load runs share one follow-up load.
func TestLiveCache_FillBurstDuringALoadCostsOneMoreLoad(t *testing.T) {
	c := newTestCache[string](t)
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	loader := func() (string, string, error) {
		if calls.Add(1) == 1 {
			close(started)
			<-release
		}
		return fmt.Sprintf("v%d", calls.Load()), "", nil
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, err := c.Fill(context.Background(), "k1", loader)
		assert.NoError(t, err)
	}()
	<-started
	const burst = 8
	results := make([]string, burst)
	for i := range burst {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := c.Fill(context.Background(), "k1", loader)
			assert.NoError(t, err)
			results[i] = v
		}()
	}
	require.Eventually(t, func() bool { return c.fills.requests("k1") == burst+1 }, time.Second, time.Millisecond)
	close(release)
	wg.Wait()

	assert.Equal(t, int32(2), calls.Load(), "the burst shares one follow-up load")
	for _, v := range results {
		assert.Equal(t, "v2", v)
	}
}

// TestLiveCache_FillFollowerReturnsOnOwnContextCancellation checks a waiting caller returns when its own context ends.
func TestLiveCache_FillFollowerReturnsOnOwnContextCancellation(t *testing.T) {
	c := newTestCache[string](t)
	leaderStarted := make(chan struct{})
	release := make(chan struct{})
	loader := func() (string, string, error) {
		close(leaderStarted)
		<-release
		return "v1", "id1", nil
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = c.Fill(context.Background(), "k1", loader)
	}()
	<-leaderStarted

	// A follower whose own ctx is canceled must return immediately with
	// ctx.Err(), not block until the (still in-flight, possibly slow or
	// unrelated) leader finishes.
	followerCtx, cancel := context.WithCancel(context.Background())
	cancel()
	followerDone := make(chan struct{})
	go func() {
		defer close(followerDone)
		_, err := c.Fill(followerCtx, "k1", loader)
		assert.ErrorIs(t, err, context.Canceled)
	}()

	select {
	case <-followerDone:
	case <-time.After(2 * time.Second):
		t.Fatal("follower did not return on its own canceled context")
	}

	// The leader is unaffected by the follower's cancellation and still
	// completes normally.
	close(release)
	wg.Wait()
	got, ok := c.Get("k1")
	assert.True(t, ok)
	assert.Equal(t, "v1", got)
}

// TestLiveCache_ErrorSharedNotCached checks a loader error reaches every waiter and is not cached.
func TestLiveCache_ErrorSharedNotCached(t *testing.T) {
	c := newTestCache[string](t)
	sentinel := errors.New("load failed")
	var calls atomic.Int32
	failing := func() (string, string, error) {
		calls.Add(1)
		return "", "", sentinel
	}

	_, err := c.Fill(context.Background(), "k1", failing)
	assert.ErrorIs(t, err, sentinel)
	assert.Equal(t, 0, c.Len(), "errors must not be cached")

	// A second call retries the loader instead of serving the error.
	_, err = c.Fill(context.Background(), "k1", failing)
	assert.ErrorIs(t, err, sentinel)
	assert.Equal(t, int32(2), calls.Load())
}

// TestLiveCache_SharedFillsShareALoadsOwnTimeout checks that when a WithSharedFills cache's load
// fails on a deadline of its own while its caller's context is live, the callers that arrived
// during the load share that error: the load runs once, as one upstream call that timed out.
func TestLiveCache_SharedFillsShareALoadsOwnTimeout(t *testing.T) {
	c := newTestCache[string](t, WithSharedFills[string]())
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	loader := func() (string, string, error) {
		if calls.Add(1) == 1 {
			close(started)
			<-release
		}
		return "", "", fmt.Errorf("upstream: %w", context.DeadlineExceeded)
	}
	const callers = 4
	errs := make(chan error, callers)
	fill := func() {
		_, err := c.Fill(context.Background(), "k1", loader)
		errs <- err
	}
	go fill()
	<-started
	for range callers - 1 {
		go fill()
	}
	require.Eventually(t, func() bool { return c.fills.requests("k1") == callers }, time.Second, time.Millisecond)
	close(release)
	for range callers {
		require.ErrorIs(t, <-errs, context.DeadlineExceeded)
	}
	assert.Equal(t, int32(1), calls.Load(), "callers that arrived during the load share its timeout")
}

// TestLiveCache_GenerationGuardDiscardsStaleFill checks a fill racing an eviction does not re-install its value.
func TestLiveCache_GenerationGuardDiscardsStaleFill(t *testing.T) {
	c := newTestCache[string](t)
	started := make(chan struct{})
	release := make(chan struct{})

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		v, err := c.Fill(context.Background(), "k1", func() (string, string, error) {
			close(started)
			<-release
			return "stale", "id1", nil
		})
		// The caller still receives the loaded value; only the install is
		// discarded. assert, not require: require.NoError calls
		// t.FailNow(), which per the testing package's own contract must
		// only be called from the goroutine running the test.
		assert.NoError(t, err)
		assert.Equal(t, "stale", v)
	}()

	<-started
	c.Evict("k1") // lands while the load is in flight
	close(release)
	wg.Wait()

	_, ok := c.Get("k1")
	assert.False(t, ok, "a fill racing an eviction must not install its result")
}

// TestLiveCache_UpsertRebindsIndex checks refilling a key under a new index key moves the binding.
func TestLiveCache_UpsertRebindsIndex(t *testing.T) {
	c := newTestCache[string](t)
	_, err := c.Fill(context.Background(), "k1", fillWith("v1", "id-old"))
	require.NoError(t, err)

	// Re-fill the same key with a new index key after evicting: the old
	// index mapping must not linger.
	c.Evict("k1")
	_, err = c.Fill(context.Background(), "k1", fillWith("v2", "id-new"))
	require.NoError(t, err)

	c.EvictByIndex("id-old")
	got, ok := c.Get("k1")
	assert.True(t, ok, "stale index key must not evict the rebound entry")
	assert.Equal(t, "v2", got)

	c.EvictByIndex("id-new")
	_, ok = c.Get("k1")
	assert.False(t, ok)
}

// TestLiveCache_IndexKeyCollisionEvictsPreviousOwner checks binding an index key to a new key evicts its previous owner.
func TestLiveCache_IndexKeyCollisionEvictsPreviousOwner(t *testing.T) {
	c := newTestCache[string](t)
	_, err := c.Fill(context.Background(), "k1", fillWith("v1", "shared-id"))
	require.NoError(t, err)

	// A second cache key rebinds the same index key without evicting k1
	// first. k1 must be evicted as a side effect, or it would be cached but
	// unreachable through the index forever.
	_, err = c.Fill(context.Background(), "k2", fillWith("v2", "shared-id"))
	require.NoError(t, err)

	_, ok := c.Get("k1")
	assert.False(t, ok, "previous owner of a rebound index key must be evicted")
	assert.Equal(t, 1, c.Len())

	c.EvictByIndex("shared-id")
	_, ok = c.Get("k2")
	assert.False(t, ok, "the new owner must still be reachable through the index before this evict")
	assert.Equal(t, 0, c.Len())
}

// TestLiveCache_IndexOwnerEvictionBumpsGeneration pins the gap CodeRabbit flagged
// in evictIndexOwnerLocked: unlike every other removal path in this file
// (Evict, EvictByIndex, EvictWhere, Flush, and the LRU-capacity eviction),
// removing a rebound index key's previous owner did not bump c.generation.
// That's an implicit invalidation of the owner's key exactly like an
// explicit Evict, so a stale in-flight Fill for that owner (one that
// captured its generation before the rebind) must be discarded by
// setIfGeneration's generation check, not allowed to re-install and steal
// the index binding back out from under the legitimate new owner.
//
// Sequence: k1 is cached under indexKey "id". A refill of k1 starts,
// captures the current generation, and blocks in its loader. While it's
// blocked, k2 completes a Fill under the same indexKey "id", which evicts
// k1 as the previous owner. k1's stale loader then finishes: its generation
// check must see the eviction and discard the stale result, leaving k2 as
// the sole, reachable owner of "id".
func TestLiveCache_IndexOwnerEvictionBumpsGeneration(t *testing.T) {
	c := newTestCache[string](t)
	_, err := c.Fill(context.Background(), "k1", fillWith("v1", "id"))
	require.NoError(t, err)

	started := make(chan struct{})
	release := make(chan struct{})

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		v, err := c.Fill(context.Background(), "k1", func() (string, string, error) {
			close(started)
			<-release
			return "v1-stale-refill", "id", nil
		})
		assert.NoError(t, err)
		assert.Equal(t, "v1-stale-refill", v)
	}()

	<-started
	// k2 rebinds "id" while k1's refill is still blocked in its loader:
	// this must evict k1 (the current owner) as a side effect AND bump
	// c.generation, exactly like any other eviction in this file.
	_, err = c.Fill(context.Background(), "k2", fillWith("v2", "id"))
	require.NoError(t, err)

	close(release)
	wg.Wait()

	got, ok := c.Get("k2")
	assert.True(t, ok, "k2 must still be cached and reachable through the index: a stale racing refill of the evicted previous owner must not steal the index binding back")
	assert.Equal(t, "v2", got)

	_, ok = c.Get("k1")
	assert.False(t, ok, "k1's stale refill must be discarded by the generation guard, not reinstalled")
}

// TestLiveCache_CallbacksReenteringCacheDoNotDeadlock checks a validator or predicate may call back
// into the cache through a method that takes c.mu, so neither may run while c.mu is held.
func TestLiveCache_CallbacksReenteringCacheDoNotDeadlock(t *testing.T) {
	var c *Cache[string]
	c = newTestCache(t, WithValidator(func(string) bool {
		// A validator that calls back into the cache must not deadlock on
		// c.mu, which Get would still be holding if it weren't released
		// before this callback runs. Evict takes c.mu; Len would not.
		c.Evict("unrelated")
		return true
	}))
	_, err := c.Fill(context.Background(), "k1", fillWith("v1", "id1"))
	require.NoError(t, err)
	_, err = c.Fill(context.Background(), "k2", fillWith("v2", "id2"))
	require.NoError(t, err)

	done := make(chan struct{})
	go func() {
		defer close(done)
		c.Get("k1")

		c.EvictWhere(func(key string) bool {
			// Same for an EvictWhere predicate.
			c.Evict("unrelated")
			return key == "k2"
		})
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("callback re-entering the cache deadlocked")
	}

	_, ok := c.Get("k2")
	assert.False(t, ok, "EvictWhere must still remove the matched entry")
}

// TestLiveCache_LoaderPanicReleasesWaitersWithError checks a panicking loader releases its waiters with an error.
func TestLiveCache_LoaderPanicReleasesWaitersWithError(t *testing.T) {
	c := newTestCache[string](t)
	started := make(chan struct{})

	var waiterErr error
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-started
		_, waiterErr = c.Fill(context.Background(), "k1", fillWith("never-used", ""))
	}()

	func() {
		defer func() {
			r := recover()
			require.NotNil(t, r, "the leader must re-panic")
		}()
		_, _ = c.Fill(context.Background(), "k1", func() (string, string, error) {
			close(started)
			// Give the waiter a moment to join the inflight call; if it
			// races past cleanup instead, it just runs its own fresh fill,
			// which this test also accepts below.
			time.Sleep(20 * time.Millisecond)
			panic("boom")
		})
	}()
	wg.Wait()

	if waiterErr != nil {
		assert.Contains(t, waiterErr.Error(), "panicked")
	}
	// Either way the key must not be wedged: a fresh fill succeeds.
	v, err := c.Fill(context.Background(), "k1", fillWith("v-after", ""))
	require.NoError(t, err)
	assert.Equal(t, "v-after", v)
}

// TestCacheEncodeDecodeKey_RoundTrip checks DecodeKey returns the parts EncodeKey joined.
func TestCacheEncodeDecodeKey_RoundTrip(t *testing.T) {
	parts := []string{"user", "alice", "client-1"}
	key := EncodeKey(parts...)
	got, ok := DecodeKey(key, len(parts))
	require.True(t, ok)
	assert.Equal(t, parts, got)
}

// Keys are built on request paths, so one is a single allocation of its exact size, whatever its
// parts' lengths.
func TestCacheEncodeKey_BuildsTheKeyInOneAllocation(t *testing.T) {
	cases := [][]string{
		{"user", "alice", "client-1"},
		{"client-1", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"},
		{"", strings.Repeat("x", 1234), "y"},
	}
	for _, parts := range cases {
		var key string
		assert.Equal(t, 1.0, testing.AllocsPerRun(100, func() { key = EncodeKey(parts...) }), "parts %q", parts)
		got, ok := DecodeKey(key, len(parts))
		require.True(t, ok)
		assert.Equal(t, parts, got)
	}
}

// TestCacheEncodeDecodeKey_NoCollisionOnEmbeddedDelimiter pins the reason this
// codec exists over a plain separator join: a value that happens to
// contain the separator (or, here, digits and a colon shaped like a length
// prefix) must not let one tuple's key collide with a different tuple's.
func TestCacheEncodeDecodeKey_NoCollisionOnEmbeddedDelimiter(t *testing.T) {
	keyA := EncodeKey("user", "a\x00b", "c")
	keyB := EncodeKey("user", "a", "b\x00c")
	assert.NotEqual(t, keyA, keyB, "distinct tuples must not alias to the same key")

	gotA, ok := DecodeKey(keyA, 3)
	require.True(t, ok)
	assert.Equal(t, []string{"user", "a\x00b", "c"}, gotA)

	gotB, ok := DecodeKey(keyB, 3)
	require.True(t, ok)
	assert.Equal(t, []string{"user", "a", "b\x00c"}, gotB)
}

// TestCacheDecodeKey_RejectsMalformedInput checks DecodeKey refuses keys EncodeKey cannot build.
func TestCacheDecodeKey_RejectsMalformedInput(t *testing.T) {
	tests := []struct {
		name string
		key  string
		n    int
	}{
		{"empty string", "", 3},
		{"no colon", "abc", 1},
		{"non-numeric length", "x:abc", 1},
		{"negative length", "-1:a", 1},
		{"length exceeds remaining bytes", "10:ab", 1},
		{"trailing garbage after all parts", "1:a1:btrailing", 2},
		{"too few parts", "4:user", 2},
		{"negative part count", "1:a", -1},
		{"non-canonical length prefix with leading plus", "+1:a", 1},
		{"non-canonical length prefix with leading zero", "01:a", 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, ok := DecodeKey(tc.key, tc.n)
			assert.False(t, ok)
		})
	}
}

// TestLiveCache_TTLDropsEntriesAndTheirIndex checks WithCacheTTL: an entry is read until its TTL
// passes and not after, its index binding goes with it, a value whose TTL has already run out is not
// installed, and a zero TTL keeps the entry.
func TestLiveCache_TTLDropsEntriesAndTheirIndex(t *testing.T) {
	type timed struct {
		v   string
		ttl time.Duration
	}
	clock := setClock(t, time.Unix(1_000_000, 0))
	c := newTestCache(t, WithCacheTTL(func(v timed) time.Duration { return v.ttl }))
	ctx := context.Background()
	_, err := c.Fill(ctx, "soon", func() (timed, string, error) { return timed{"a", 80 * time.Millisecond}, "row-a", nil })
	require.NoError(t, err)
	_, err = c.Fill(ctx, "past", func() (timed, string, error) { return timed{"b", -1}, "", nil })
	require.NoError(t, err)
	_, err = c.Fill(ctx, "never", func() (timed, string, error) { return timed{"c", 0}, "", nil })
	require.NoError(t, err)

	_, ok := c.Get("soon")
	require.True(t, ok)
	owner, ok := c.byIndex.Get("row-a")
	require.True(t, ok, "the index binding is present while its entry is live")
	require.Equal(t, "soon", owner)
	entryExpiry, ok := c.entries.Expiry("soon")
	require.True(t, ok, "the entry has a TTL")
	indexExpiry, ok := c.byIndex.Expiry("row-a")
	require.True(t, ok, "the index binding has a TTL")
	require.Equal(t, entryExpiry, indexExpiry, "the index binding expires with its entry")
	_, ok = c.Get("past")
	require.False(t, ok, "a value whose TTL has run out is not installed")

	*clock = clock.Add(81 * time.Millisecond)
	_, ok = c.Get("soon")
	require.False(t, ok, "the entry is not read once its TTL passes")
	_, ok = c.byIndex.Get("row-a")
	require.False(t, ok, "the index binding expires with its entry")
	_, ok = c.Get("never")
	require.True(t, ok, "a zero TTL keeps the entry")
}

// TestLiveCache_RejectedRefillKeepsTheEntryIndexed checks that a refill whose value has already run
// out leaves the entry it did not replace reachable by its index key, so an eviction by that key
// (such as a revocation by row id) still removes it.
func TestLiveCache_RejectedRefillKeepsTheEntryIndexed(t *testing.T) {
	type timed struct {
		v   string
		ttl time.Duration
	}
	c := newTestCache(t, WithCacheTTL(func(v timed) time.Duration { return v.ttl }))
	ctx := context.Background()
	_, err := c.Fill(ctx, "k", func() (timed, string, error) { return timed{"a", 0}, "old-id", nil })
	require.NoError(t, err)
	_, err = c.Fill(ctx, "k", func() (timed, string, error) { return timed{"b", -1}, "new-id", nil })
	require.NoError(t, err)
	got, ok := c.Get("k")
	require.True(t, ok, "a refill that is not installed leaves the entry in place")
	require.Equal(t, "a", got.v)

	c.EvictByIndex("old-id")
	_, ok = c.Get("k")
	require.False(t, ok, "the entry still in place must be evictable by its index key")
}

// TestLiveCache_NegativeTTLDropsNegativesOnly checks WithCacheNegativeTTL: a negative result is
// kept for the negative TTL only, while others stay until evicted.
func TestLiveCache_NegativeTTLDropsNegativesOnly(t *testing.T) {
	clock := setClock(t, time.Unix(1_000_000, 0))
	c := newTestCache(t, WithCacheNegativeTTL(func(v string) bool { return v == "" }, 50*time.Millisecond))
	ctx := context.Background()
	_, err := c.Fill(ctx, "unknown", fillWith("", ""))
	require.NoError(t, err)
	_, err = c.Fill(ctx, "known", fillWith("v", ""))
	require.NoError(t, err)
	_, ok := c.Get("unknown")
	require.True(t, ok, "a negative result is kept for its TTL")

	*clock = clock.Add(51 * time.Millisecond)
	_, ok = c.Get("unknown")
	require.False(t, ok, "a negative result is dropped once its TTL passes")
	_, ok = c.Get("known")
	require.True(t, ok, "other results stay")
}

// TestLiveCache_NegativeNegativeTTLKeepsNoNegatives checks that a negative TTL for negative results
// keeps none, as a negative TTL means for every entry: the result has already run out.
func TestLiveCache_NegativeNegativeTTLKeepsNoNegatives(t *testing.T) {
	c := newTestCache(t, WithCacheNegativeTTL(func(v string) bool { return v == "" }, -time.Second))
	ctx := context.Background()
	got, err := c.Fill(ctx, "unknown", fillWith("", ""))
	require.NoError(t, err)
	require.Empty(t, got, "the caller still gets the negative result")
	_, err = c.Fill(ctx, "known", fillWith("v", ""))
	require.NoError(t, err)
	_, ok := c.Get("unknown")
	require.False(t, ok, "a negative result with a negative TTL must not be kept")
	require.Equal(t, 1, c.Len(), "only the other result is kept")
}

// TestLiveCache_TTLFunctionReenteringCacheDoesNotDeadlock checks that a WithCacheTTL function that
// calls back into the cache does not deadlock: it is caller code, like a validator, so it runs
// without the cache's lock. Here it reads an entry the validator rejects, which takes that lock to
// remove the entry.
func TestLiveCache_TTLFunctionReenteringCacheDoesNotDeadlock(t *testing.T) {
	var c *Cache[string]
	c = newTestCache(t,
		WithValidator(func(v string) bool { return v != "stale" }),
		WithCacheTTL(func(string) time.Duration {
			c.Get("other")
			return time.Minute
		}))
	_, err := c.Fill(context.Background(), "other", fillWith("stale", ""))
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() {
		_, err := c.Fill(context.Background(), "k", fillWith("v", "id"))
		done <- err
	}()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("a TTL function re-entering the cache deadlocked")
	}
	got, ok := c.Get("k")
	require.True(t, ok, "the fill installs its value")
	require.Equal(t, "v", got)
	require.Equal(t, 1, c.Len(), "the rejected entry the TTL function read is removed")
}

// TestCacheTablesAreTheStoresOwn checks that a cache's tables are the store's own, out of reach of
// DropTable, and that a name holds one cache.
func TestCacheTablesAreTheStoresOwn(t *testing.T) {
	s := newStore()
	t.Cleanup(s.Close)
	ctx := context.Background()
	if _, err := s.NewCache[string](ctx, "tokens"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.NewCache[string](ctx, "tokens"); err == nil {
		t.Fatal("a second cache under one name must fail")
	}
	if err := s.DropTable(ctx, "internal:tokens"); err == nil {
		t.Fatal("DropTable must not reach a cache's tables")
	}
}
