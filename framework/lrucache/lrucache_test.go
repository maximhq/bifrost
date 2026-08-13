package lrucache

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fillWith returns a Loader serving a fixed value and index key.
func fillWith(value string, indexKey string) Loader[string] {
	return func() (string, string, error) {
		return value, indexKey, nil
	}
}

func TestCache_HitAndMiss(t *testing.T) {
	c := New[string](4)
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

func TestCache_CapacityEvictionAndIndexCleanup(t *testing.T) {
	c := New[string](2)
	for i := 1; i <= 3; i++ {
		_, err := c.Fill(context.Background(), fmt.Sprintf("k%d", i), fillWith(fmt.Sprintf("v%d", i), fmt.Sprintf("id%d", i)))
		require.NoError(t, err)
	}
	assert.Equal(t, 2, c.Len())

	// k1 was least recently used and must be gone, along with its index
	// entry: evicting by its old index key must not disturb survivors.
	_, ok := c.Get("k1")
	assert.False(t, ok)
	c.EvictByIndex("id1")
	assert.Equal(t, 2, c.Len())

	_, ok = c.Get("k2")
	assert.True(t, ok)
	_, ok = c.Get("k3")
	assert.True(t, ok)
}

func TestCache_ValidatorRejectionIsMissAndRemoval(t *testing.T) {
	type timedValue struct {
		value     string
		expiresAt time.Time
	}
	c := New(4, WithValidator(func(v timedValue) bool {
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

// TestCache_GetValidatorRejectionDoesNotEvictConcurrentlyUpdatedEntry covers
// the race the entry.version field exists for: setIfGeneration's
// existing-entry branch updates a value in place on the same *list.Element,
// so element identity alone can't tell a Get (mid-validator, evaluating a
// value it already read) that the entry has since been refreshed. Without
// the version check, rejecting the stale value would evict the fresh one
// installed concurrently.
func TestCache_GetValidatorRejectionDoesNotEvictConcurrentlyUpdatedEntry(t *testing.T) {
	validatorEntered := make(chan struct{})
	releaseValidator := make(chan struct{})
	var pausedOnce atomic.Bool
	pausedOnce.Store(true)

	c := New(4, WithValidator(func(v string) bool {
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

func TestCache_EvictExactKey(t *testing.T) {
	c := New[string](4)
	_, err := c.Fill(context.Background(), "k1", fillWith("v1", "id1"))
	require.NoError(t, err)

	c.Evict("k1")
	_, ok := c.Get("k1")
	assert.False(t, ok)
	assert.Equal(t, 0, c.Len())

	// Evicting an absent key is a no-op, not a panic.
	c.Evict("never-set")
}

func TestCache_EvictByIndex(t *testing.T) {
	c := New[string](4)
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

func TestCache_EvictWhere(t *testing.T) {
	c := New[string](8)
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

// TestCache_EvictWhereGenerationBumpPrecedesPred exercises the ordering
// EvictWhere's generation++ move exists for: a concurrent Fill for a key
// that isn't cached yet (so it can never appear in EvictWhere's keys
// snapshot or its toRemove list) must still be discarded if its generation
// was captured before this EvictWhere call, even though EvictWhere's own
// predicate is still running when the fill completes.
func TestCache_EvictWhereGenerationBumpPrecedesPred(t *testing.T) {
	c := New[string](4)
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

func TestCache_Flush(t *testing.T) {
	c := New[string](4)
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

func TestCache_InflightDedup(t *testing.T) {
	c := New[string](4)
	var calls atomic.Int32
	release := make(chan struct{})
	loader := func() (string, string, error) {
		calls.Add(1)
		<-release
		return "v1", "id1", nil
	}

	var wg sync.WaitGroup
	results := make([]string, 2)
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := c.Fill(context.Background(), "k1", loader)
			// assert, not require: require.NoError calls t.FailNow(), which
			// per the testing package's own contract must only be called
			// from the goroutine running the test.
			assert.NoError(t, err)
			results[i] = v
		}()
	}
	// Let both goroutines reach the fill before releasing the leader.
	assert.Eventually(t, func() bool { return calls.Load() == 1 }, time.Second, time.Millisecond)
	close(release)
	wg.Wait()

	assert.Equal(t, int32(1), calls.Load(), "loader must run exactly once")
	assert.Equal(t, []string{"v1", "v1"}, results)
}

func TestCache_FillFollowerReturnsOnOwnContextCancellation(t *testing.T) {
	c := New[string](4)
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

func TestCache_ErrorSharedNotCached(t *testing.T) {
	c := New[string](4)
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

func TestCache_GenerationGuardDiscardsStaleFill(t *testing.T) {
	c := New[string](4)
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

func TestCache_UpsertRebindsIndex(t *testing.T) {
	c := New[string](4)
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

func TestCache_IndexKeyCollisionEvictsPreviousOwner(t *testing.T) {
	c := New[string](4)
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

// TestCache_IndexOwnerEvictionBumpsGeneration pins the gap CodeRabbit flagged
// in evictIndexOwnerLocked: unlike every other removal path in this file
// (Evict, EvictByIndex, EvictWhere, Flush, and the LRU-capacity eviction),
// removing a rebound index key's previous owner did not bump c.generation.
// That's an implicit invalidation of the owner's key exactly like an
// explicit Evict, so a stale in-flight Fill for that owner — one that
// captured its generation before the rebind — must be discarded by
// setIfGeneration's generation check, not allowed to re-install and steal
// the index binding back out from under the legitimate new owner.
//
// Sequence: k1 is cached under indexKey "id". A refill of k1 starts,
// captures the current generation, and blocks in its loader. While it's
// blocked, k2 completes a Fill under the same indexKey "id" — this evicts
// k1 as the previous owner. k1's stale loader then finishes: its generation
// check must see the eviction and discard the stale result, leaving k2 as
// the sole, reachable owner of "id".
func TestCache_IndexOwnerEvictionBumpsGeneration(t *testing.T) {
	c := New[string](4)
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
	// k2 rebinds "id" while k1's refill is still blocked in its loader —
	// this must evict k1 (the current owner) as a side effect AND bump
	// c.generation, exactly like any other eviction in this file.
	_, err = c.Fill(context.Background(), "k2", fillWith("v2", "id"))
	require.NoError(t, err)

	close(release)
	wg.Wait()

	got, ok := c.Get("k2")
	assert.True(t, ok, "k2 must still be cached and reachable through the index — a stale racing refill of the evicted previous owner must not steal the index binding back")
	assert.Equal(t, "v2", got)

	_, ok = c.Get("k1")
	assert.False(t, ok, "k1's stale refill must be discarded by the generation guard, not reinstalled")
}

func TestCache_IndexKeyRebindOnFullCachePreservesCapacity(t *testing.T) {
	c := New[string](2)
	_, err := c.Fill(context.Background(), "k1", fillWith("v1", "shared-id"))
	require.NoError(t, err)
	_, err = c.Fill(context.Background(), "k2", fillWith("v2", ""))
	require.NoError(t, err)

	// Move k1 to the front so k2 (not the index-colliding k1) is the LRU tail.
	_, ok := c.Get("k1")
	require.True(t, ok)

	// k3 rebinds "shared-id" from k1 (not the LRU tail) while the cache is
	// already full. This must evict exactly one entry — k1, the rebound
	// index's prior owner — not two (k1 plus the LRU tail k2).
	_, err = c.Fill(context.Background(), "k3", fillWith("v3", "shared-id"))
	require.NoError(t, err)

	assert.Equal(t, 2, c.Len(), "capacity must be preserved: only the rebound index's prior owner should be evicted, not the LRU tail too")
	_, ok = c.Get("k1")
	assert.False(t, ok, "k1 (the previous shared-id owner) must be evicted")
	_, ok = c.Get("k2")
	assert.True(t, ok, "k2 must survive: it was not involved in the index rebind")
	_, ok = c.Get("k3")
	assert.True(t, ok)
}

func TestCache_CallbacksReenteringCacheDoNotDeadlock(t *testing.T) {
	var c *Cache[string]
	c = New(4, WithValidator(func(string) bool {
		// A validator that calls back into the cache must not deadlock on
		// c.mu, which Get would still be holding if it weren't released
		// before this callback runs.
		c.Len()
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
			c.Len()
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

func TestCache_LoaderPanicReleasesWaitersWithError(t *testing.T) {
	c := New[string](4)
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

func TestCache_NilSafety(t *testing.T) {
	var c *Cache[string]
	_, ok := c.Get("k1")
	assert.False(t, ok)
	c.Evict("k1")
	c.EvictByIndex("id1")
	c.EvictWhere(func(string) bool { return true })
	c.Flush()
	assert.Equal(t, 0, c.Len())

	v, err := c.Fill(context.Background(), "k1", fillWith("v1", "id1"))
	require.NoError(t, err)
	assert.Equal(t, "v1", v, "nil cache must degrade to calling the loader directly")
}

func TestNew_PanicsOnNonPositiveCapacity(t *testing.T) {
	assert.Panics(t, func() { New[string](0) })
	assert.Panics(t, func() { New[string](-1) })
}

func TestEncodeDecodeKey_RoundTrip(t *testing.T) {
	parts := []string{"user", "alice", "client-1"}
	key := EncodeKey(parts...)
	got, ok := DecodeKey(key, len(parts))
	require.True(t, ok)
	assert.Equal(t, parts, got)
}

// TestEncodeDecodeKey_NoCollisionOnEmbeddedDelimiter pins the reason this
// codec exists over a plain separator join: a value that happens to
// contain the separator (or, here, digits and a colon shaped like a length
// prefix) must not let one tuple's key collide with a different tuple's.
func TestEncodeDecodeKey_NoCollisionOnEmbeddedDelimiter(t *testing.T) {
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

func TestDecodeKey_RejectsMalformedInput(t *testing.T) {
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
