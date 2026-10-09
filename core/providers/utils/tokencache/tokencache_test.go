package tokencache

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// clock is an injectable time source so backoff windows can be crossed without sleeping.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func newClock() *clock { return &clock{now: time.Unix(1_700_000_000, 0)} }

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// countingMinter mints "token-N" on the N-th call, with an optional delay and an optional
// error to return instead.
type countingMinter struct {
	calls   atomic.Int64
	delay   time.Duration
	advance time.Duration // fake time the exchange takes, applied to clock before returning
	err     atomic.Pointer[schemas.BifrostError]
	last    atomic.Pointer[Entry[string]] // prev seen on the latest call
	clock   *clock
	ttl     time.Duration
}

func (m *countingMinter) mint(ctx context.Context, prev *Entry[string]) (*Entry[string], *schemas.BifrostError) {
	n := m.calls.Add(1)
	m.last.Store(prev)
	if m.delay > 0 {
		time.Sleep(m.delay)
	}
	if m.advance > 0 {
		m.clock.Advance(m.advance)
	}
	if e := m.err.Load(); e != nil {
		return nil, e
	}
	ttl := m.ttl
	if ttl == 0 {
		ttl = time.Hour
	}
	now := m.clock.Now()
	return &Entry[string]{
		Value:     "token-" + string(rune('0'+n)),
		ExpiresAt: now.Add(ttl),
		RefreshAt: now.Add(ttl - time.Minute),
	}, nil
}

func statusError(status int) *schemas.BifrostError {
	return providerUtils.NewProviderAPIError("mint failed", nil, status, nil, nil)
}

func newCache(c *clock) *Cache[string] {
	return New[string](Options{Now: c.Now})
}

func TestGetSingleMint(t *testing.T) {
	t.Run("a warm token costs no mint", func(t *testing.T) {
		clk := newClock()
		cache := newCache(clk)
		m := &countingMinter{clock: clk}

		e, bErr := cache.Get(context.Background(), "k", m.mint)
		require.Nil(t, bErr)
		assert.Equal(t, "token-1", e.Value)

		for range 50 {
			e, bErr = cache.Get(context.Background(), "k", m.mint)
			require.Nil(t, bErr)
			assert.Equal(t, "token-1", e.Value)
		}
		assert.Equal(t, int64(1), m.calls.Load())
	})

	t.Run("concurrent cold starts collapse to one mint", func(t *testing.T) {
		clk := newClock()
		cache := newCache(clk)
		m := &countingMinter{clock: clk, delay: 30 * time.Millisecond}

		var wg sync.WaitGroup
		for range 100 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				e, bErr := cache.Get(context.Background(), "k", m.mint)
				assert.Nil(t, bErr)
				assert.Equal(t, "token-1", e.Value)
			}()
		}
		wg.Wait()
		assert.Equal(t, int64(1), m.calls.Load(), "the slot mutex plus the double-checked read must collapse the herd")
	})

	t.Run("distinct keys do not share a token", func(t *testing.T) {
		clk := newClock()
		cache := newCache(clk)
		m := &countingMinter{clock: clk}

		a, _ := cache.Get(context.Background(), "a", m.mint)
		b, _ := cache.Get(context.Background(), "b", m.mint)
		assert.NotEqual(t, a.Value, b.Value)
		assert.Equal(t, int64(2), m.calls.Load())
	})

	t.Run("a token is re-minted once RefreshAt passes", func(t *testing.T) {
		clk := newClock()
		cache := newCache(clk)
		m := &countingMinter{clock: clk, ttl: 10 * time.Minute}

		_, bErr := cache.Get(context.Background(), "k", m.mint)
		require.Nil(t, bErr)
		clk.Advance(8 * time.Minute)
		e, bErr := cache.Get(context.Background(), "k", m.mint)
		require.Nil(t, bErr)
		assert.Equal(t, "token-1", e.Value, "still inside the refresh margin")

		clk.Advance(2 * time.Minute)
		e, bErr = cache.Get(context.Background(), "k", m.mint)
		require.Nil(t, bErr)
		assert.Equal(t, "token-2", e.Value)
		assert.Equal(t, int64(2), m.calls.Load())
	})

	t.Run("a zero RefreshAt defaults to ExpiresAt", func(t *testing.T) {
		clk := newClock()
		cache := newCache(clk)
		calls := 0
		mint := func(context.Context, *Entry[string]) (*Entry[string], *schemas.BifrostError) {
			calls++
			return &Entry[string]{Value: "t", ExpiresAt: clk.Now().Add(time.Minute)}, nil
		}
		_, _ = cache.Get(context.Background(), "k", mint)
		clk.Advance(59 * time.Second)
		_, _ = cache.Get(context.Background(), "k", mint)
		assert.Equal(t, 1, calls)
		clk.Advance(2 * time.Second)
		_, _ = cache.Get(context.Background(), "k", mint)
		assert.Equal(t, 2, calls)
	})

	t.Run("a minter that returns nothing is an error, not a nil entry", func(t *testing.T) {
		cache := newCache(newClock())
		e, bErr := cache.Get(context.Background(), "k", func(context.Context, *Entry[string]) (*Entry[string], *schemas.BifrostError) {
			return nil, nil
		})
		assert.Nil(t, e)
		require.NotNil(t, bErr)
		assert.Contains(t, bErr.Error.Message, "neither a token nor an error")
	})
}

func TestFailureBackoff(t *testing.T) {
	t.Run("a permanent failure is cached instead of retried per request", func(t *testing.T) {
		clk := newClock()
		cache := newCache(clk)
		m := &countingMinter{clock: clk}
		m.err.Store(statusError(http.StatusForbidden))

		for range 100 {
			_, bErr := cache.Get(context.Background(), "k", m.mint)
			require.NotNil(t, bErr)
			assert.Equal(t, http.StatusForbidden, *bErr.StatusCode)
		}
		assert.Equal(t, int64(1), m.calls.Load(), "every call inside the window must be answered from the negative cache")
	})

	t.Run("permanent backoff grows 2s 4s 8s and clamps at 60s", func(t *testing.T) {
		clk := newClock()
		cache := newCache(clk)
		m := &countingMinter{clock: clk}
		m.err.Store(statusError(http.StatusUnauthorized))

		expect := func(attempt int64, window time.Duration) {
			_, _ = cache.Get(context.Background(), "k", m.mint)
			require.Equal(t, attempt, m.calls.Load())
			clk.Advance(window - time.Millisecond)
			_, _ = cache.Get(context.Background(), "k", m.mint)
			require.Equal(t, attempt, m.calls.Load(), "a call just inside the %s window must not mint", window)
			clk.Advance(time.Millisecond)
		}
		expect(1, 2*time.Second)
		expect(2, 4*time.Second)
		expect(3, 8*time.Second)
		expect(4, 16*time.Second)
		expect(5, 32*time.Second)
		expect(6, 60*time.Second)
		expect(7, 60*time.Second)
	})

	t.Run("transient backoff grows 500ms 1s and clamps at 10s", func(t *testing.T) {
		clk := newClock()
		cache := newCache(clk)
		m := &countingMinter{clock: clk}
		m.err.Store(statusError(http.StatusServiceUnavailable))

		_, _ = cache.Get(context.Background(), "k", m.mint)
		clk.Advance(499 * time.Millisecond)
		_, _ = cache.Get(context.Background(), "k", m.mint)
		assert.Equal(t, int64(1), m.calls.Load())
		clk.Advance(time.Millisecond)
		_, _ = cache.Get(context.Background(), "k", m.mint)
		assert.Equal(t, int64(2), m.calls.Load())
		for range 10 {
			clk.Advance(time.Minute)
			_, _ = cache.Get(context.Background(), "k", m.mint)
		}
		clk.Advance(9999 * time.Millisecond)
		_, _ = cache.Get(context.Background(), "k", m.mint)
		assert.Equal(t, int64(12), m.calls.Load(), "the clamp is 10s, so 9.999s later is still inside the window")
	})

	t.Run("a success clears the failure and its attempt count", func(t *testing.T) {
		clk := newClock()
		cache := newCache(clk)
		m := &countingMinter{clock: clk}
		m.err.Store(statusError(http.StatusUnauthorized))

		_, _ = cache.Get(context.Background(), "k", m.mint)
		clk.Advance(time.Hour)
		_, _ = cache.Get(context.Background(), "k", m.mint)
		clk.Advance(time.Hour)
		m.err.Store(nil)
		e, bErr := cache.Get(context.Background(), "k", m.mint)
		require.Nil(t, bErr)
		assert.Equal(t, "token-3", e.Value)

		// A fresh failure starts again at the base window, not at 8s.
		clk.Advance(2 * time.Hour)
		m.err.Store(statusError(http.StatusUnauthorized))
		_, _ = cache.Get(context.Background(), "k", m.mint)
		clk.Advance(2*time.Second + time.Millisecond)
		_, _ = cache.Get(context.Background(), "k", m.mint)
		assert.Equal(t, int64(5), m.calls.Load())
	})

	t.Run("every caller gets its own copy of a cached failure", func(t *testing.T) {
		// Core writes routing info and provider metadata into the error it is handed, so a
		// pointer shared across requests would race and mix one request's details into another's.
		clk := newClock()
		cache := newCache(clk)
		m := &countingMinter{clock: clk}
		m.err.Store(statusError(http.StatusUnauthorized))

		_, first := cache.Get(context.Background(), "k", m.mint)
		_, second := cache.Get(context.Background(), "k", m.mint)
		require.NotNil(t, first)
		require.NotNil(t, second)
		assert.NotSame(t, first, second, "the recording caller and a later caller must not share one error")
		first.Error.Message = "mutated-by-request-one"
		_, third := cache.Get(context.Background(), "k", m.mint)
		assert.Equal(t, "mint failed", second.Error.Message)
		assert.Equal(t, "mint failed", third.Error.Message)
		assert.Equal(t, int64(1), m.calls.Load())
	})

	t.Run("the failure window is at least the endpoint's Retry-After", func(t *testing.T) {
		// A 429 with Retry-After: 7 must hold every caller off for 7s, not the 500ms transient base.
		clk := newClock()
		cache := newCache(clk)
		m := &countingMinter{clock: clk}
		limited := statusError(http.StatusTooManyRequests)
		limited.ExtraFields.RetryAfter = 7000
		m.err.Store(limited)

		_, _ = cache.Get(context.Background(), "k", m.mint)
		clk.Advance(6900 * time.Millisecond)
		_, bErr := cache.Get(context.Background(), "k", m.mint)
		require.NotNil(t, bErr)
		assert.Equal(t, int64(1), m.calls.Load(), "a mint inside the endpoint's Retry-After window prolongs the rate limit")
		clk.Advance(200 * time.Millisecond)
		_, _ = cache.Get(context.Background(), "k", m.mint)
		assert.Equal(t, int64(2), m.calls.Load())
	})

	t.Run("a transient refresh failure keeps serving the token until it expires", func(t *testing.T) {
		// RefreshAt is a minute before ExpiresAt. A 5xx from the endpoint inside that window
		// must not fail requests that the still-valid token could serve.
		clk := newClock()
		cache := newCache(clk)
		m := &countingMinter{clock: clk, ttl: 2 * time.Minute}

		_, _ = cache.Get(context.Background(), "k", m.mint)
		clk.Advance(90 * time.Second) // past RefreshAt, 30s before ExpiresAt
		m.err.Store(statusError(http.StatusServiceUnavailable))
		e, bErr := cache.Get(context.Background(), "k", m.mint)
		require.Nil(t, bErr, "a transient failure must not surface while the old token is valid")
		assert.Equal(t, "token-1", e.Value)
		assert.Equal(t, int64(2), m.calls.Load(), "the refresh was attempted")

		// Inside the failure window the old token is served from the fast path, no mint.
		e, bErr = cache.Get(context.Background(), "k", m.mint)
		require.Nil(t, bErr)
		assert.Equal(t, "token-1", e.Value)
		assert.Equal(t, int64(2), m.calls.Load())

		// Once the token itself has expired there is nothing left to serve.
		clk.Advance(40 * time.Second)
		_, bErr = cache.Get(context.Background(), "k", m.mint)
		require.NotNil(t, bErr)
		assert.Nil(t, cache.Peek("k"))
	})

	t.Run("a token that expires during a failed refresh is not served", func(t *testing.T) {
		// The refresh starts with 5s left on the old token and the endpoint takes 10s to fail
		// with a 503. By then the old token is dead; handing it out would send an expired bearer
		// upstream and turn a mint error that allows fallbacks into an upstream 401 that does not.
		clk := newClock()
		cache := newCache(clk)
		m := &countingMinter{clock: clk, ttl: 2 * time.Minute}

		_, _ = cache.Get(context.Background(), "k", m.mint)
		clk.Advance(115 * time.Second) // 5s before ExpiresAt
		m.advance = 10 * time.Second
		m.err.Store(statusError(http.StatusServiceUnavailable))
		e, bErr := cache.Get(context.Background(), "k", m.mint)
		require.NotNil(t, bErr, "an expired token must not be served, got %+v", e)
		require.NotNil(t, bErr.StatusCode)
		assert.Equal(t, http.StatusServiceUnavailable, *bErr.StatusCode, "the mint error surfaces, not an upstream 401")
		assert.Nil(t, cache.Peek("k"), "the expired entry is dropped")
	})

	t.Run("a permanent refresh failure drops the token at once", func(t *testing.T) {
		// A 401 means the credential is bad; serving the old token until it expires would hide
		// that for up to a refresh margin.
		clk := newClock()
		cache := newCache(clk)
		m := &countingMinter{clock: clk, ttl: 2 * time.Minute}

		_, _ = cache.Get(context.Background(), "k", m.mint)
		clk.Advance(90 * time.Second)
		m.err.Store(statusError(http.StatusUnauthorized))
		_, bErr := cache.Get(context.Background(), "k", m.mint)
		require.NotNil(t, bErr)
		assert.Nil(t, cache.Peek("k"))
	})

	t.Run("a failure drops the stale token so prev is nil on the retry", func(t *testing.T) {
		clk := newClock()
		cache := newCache(clk)
		m := &countingMinter{clock: clk, ttl: time.Minute}

		_, _ = cache.Get(context.Background(), "k", m.mint)
		clk.Advance(2 * time.Minute)
		m.err.Store(statusError(http.StatusBadGateway))
		_, bErr := cache.Get(context.Background(), "k", m.mint)
		require.NotNil(t, bErr)
		assert.Nil(t, cache.Peek("k"))

		clk.Advance(time.Minute)
		m.err.Store(nil)
		_, _ = cache.Get(context.Background(), "k", m.mint)
		assert.Nil(t, m.last.Load())
	})
}

func TestInvalidateAndExpire(t *testing.T) {
	t.Run("Invalidate mints from scratch but keeps the failure window", func(t *testing.T) {
		clk := newClock()
		cache := newCache(clk)
		m := &countingMinter{clock: clk}

		_, _ = cache.Get(context.Background(), "k", m.mint)
		cache.Invalidate("k")
		assert.Nil(t, cache.Peek("k"))
		e, bErr := cache.Get(context.Background(), "k", m.mint)
		require.Nil(t, bErr)
		assert.Equal(t, "token-2", e.Value)
		assert.Nil(t, m.last.Load(), "an invalidated entry must not reach the minter as prev")

		// A failure, then Invalidate, then another Get: the backoff still holds.
		clk.Advance(time.Hour)
		m.err.Store(statusError(http.StatusUnauthorized))
		_, _ = cache.Get(context.Background(), "k", m.mint)
		cache.Invalidate("k")
		_, _ = cache.Get(context.Background(), "k", m.mint)
		assert.Equal(t, int64(3), m.calls.Load(), "Invalidate must not reset the negative cache")
	})

	t.Run("Expire keeps the value so the minter can reuse it as prev", func(t *testing.T) {
		clk := newClock()
		cache := newCache(clk)
		m := &countingMinter{clock: clk}

		_, _ = cache.Get(context.Background(), "k", m.mint)
		cache.Expire("k")
		e, bErr := cache.Get(context.Background(), "k", m.mint)
		require.Nil(t, bErr)
		assert.Equal(t, "token-2", e.Value)
		prev := m.last.Load()
		require.NotNil(t, prev)
		assert.Equal(t, "token-1", prev.Value)
	})

	t.Run("a concurrent Invalidate is never undone by Expire", func(t *testing.T) {
		// Every serial order ends with an empty slot: Expire after Invalidate sees nil and
		// returns, Invalidate after Expire stores nil. Only a lost update (Expire storing a copy
		// it loaded before Invalidate cleared the slot) leaves a value behind.
		clk := newClock()
		cache := newCache(clk)
		m := &countingMinter{clock: clk}
		for i := range 2000 {
			cache.Invalidate("k")
			_, _ = cache.Get(context.Background(), "k", m.mint)
			var wg sync.WaitGroup
			wg.Add(2)
			go func() { defer wg.Done(); cache.Invalidate("k") }()
			go func() { defer wg.Done(); cache.Expire("k") }()
			wg.Wait()
			require.Nil(t, cache.Peek("k"), "iteration %d: Expire restored an entry Invalidate had cleared", i)
		}
	})

	t.Run("eviction refreshes exactly once, not once per goroutine", func(t *testing.T) {
		clk := newClock()
		cache := newCache(clk)
		m := &countingMinter{clock: clk, delay: 30 * time.Millisecond}

		_, _ = cache.Get(context.Background(), "k", m.mint)
		cache.Invalidate("k")

		var wg sync.WaitGroup
		for range 100 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, _ = cache.Get(context.Background(), "k", m.mint)
			}()
		}
		wg.Wait()
		assert.Equal(t, int64(2), m.calls.Load())
	})

	t.Run("Invalidate and Expire on an unknown key are no-ops", func(t *testing.T) {
		cache := newCache(newClock())
		cache.Invalidate("nope")
		cache.Expire("nope")
		assert.Nil(t, cache.Peek("nope"))
	})
}

func TestMintContext(t *testing.T) {
	t.Run("the mint survives the caller hanging up", func(t *testing.T) {
		cache := New[string](Options{ExchangeTimeout: 5 * time.Second})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		var sawErr error
		var deadline time.Duration
		_, bErr := cache.Get(ctx, "k", func(mctx context.Context, _ *Entry[string]) (*Entry[string], *schemas.BifrostError) {
			sawErr = mctx.Err()
			if d, ok := mctx.Deadline(); ok {
				deadline = time.Until(d)
			}
			return &Entry[string]{Value: "t", ExpiresAt: time.Now().Add(time.Hour)}, nil
		})
		require.Nil(t, bErr)
		assert.NoError(t, sawErr, "the exchange context must be detached from the caller's cancellation")
		assert.InDelta(t, 5*time.Second, deadline, float64(time.Second))
	})

	t.Run("a nil context is tolerated", func(t *testing.T) {
		cache := newCache(newClock())
		_, bErr := cache.Get(nil, "k", func(context.Context, *Entry[string]) (*Entry[string], *schemas.BifrostError) { //nolint:staticcheck
			return &Entry[string]{Value: "t", ExpiresAt: time.Now().Add(time.Hour)}, nil
		})
		assert.Nil(t, bErr)
	})
}

func TestBackoffFor(t *testing.T) {
	assert.Equal(t, 2*time.Second, DefaultPermanentBackoff.For(1))
	assert.Equal(t, 4*time.Second, DefaultPermanentBackoff.For(2))
	assert.Equal(t, 60*time.Second, DefaultPermanentBackoff.For(100))
	assert.Equal(t, 500*time.Millisecond, DefaultTransientBackoff.For(1))
	assert.Equal(t, 10*time.Second, DefaultTransientBackoff.For(100))
	assert.Equal(t, 2*time.Second, DefaultPermanentBackoff.For(0), "attempt counts below one get the base")
}

func TestIsPermanentError(t *testing.T) {
	assert.False(t, IsPermanentError(nil))
	assert.False(t, IsPermanentError(providerUtils.NewConfigurationError("x")), "no status means the endpoint was not reached")
	assert.False(t, IsPermanentError(statusError(0)))
	assert.False(t, IsPermanentError(statusError(http.StatusTooManyRequests)))
	assert.False(t, IsPermanentError(statusError(http.StatusBadGateway)))
	assert.True(t, IsPermanentError(statusError(http.StatusUnauthorized)))
	assert.True(t, IsPermanentError(statusError(http.StatusForbidden)))
	assert.True(t, IsPermanentError(statusError(http.StatusBadRequest)))
}

func TestCacheKey(t *testing.T) {
	t.Run("length framing separates fields", func(t *testing.T) {
		assert.NotEqual(t, CacheKey("v", "a|b", "c"), CacheKey("v", "a", "b|c"))
		assert.NotEqual(t, CacheKey("v", "ab", ""), CacheKey("v", "a", "b"))
	})
	t.Run("the version namespaces the key", func(t *testing.T) {
		assert.NotEqual(t, CacheKey("v1", "a"), CacheKey("v2", "a"))
	})
	t.Run("equal inputs give equal keys", func(t *testing.T) {
		assert.Equal(t, CacheKey("v", "a", "b"), CacheKey("v", "a", "b"))
	})
	t.Run("the key does not contain its inputs", func(t *testing.T) {
		assert.NotContains(t, CacheKey("v", "my-secret"), "my-secret")
		assert.Len(t, CacheKey("v"), 64)
	})
}

func TestNormalizePEM(t *testing.T) {
	assert.Equal(t, "-----BEGIN X-----\nabc\n-----END X-----", NormalizePEM(`-----BEGIN X-----\nabc\n-----END X-----`))
	assert.Equal(t, "a\nb", NormalizePEM("a\r\nb"))
	assert.Equal(t, "a\\nb\nc", NormalizePEM("a\\nb\nc\n"), "a real newline present means the escapes are literal")
}

// BenchmarkGetWarm is the request-path cost of a cached token: one sync.Map load, one
// atomic load, one time.Now. It must not take the lock or allocate.
func BenchmarkGetWarm(b *testing.B) {
	cache := New[string](Options{})
	mint := func(context.Context, *Entry[string]) (*Entry[string], *schemas.BifrostError) {
		return &Entry[string]{Value: "t", ExpiresAt: time.Now().Add(time.Hour), RefreshAt: time.Now().Add(time.Hour)}, nil
	}
	_, _ = cache.Get(context.Background(), "k", mint)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, bErr := cache.Get(context.Background(), "k", mint); bErr != nil {
			b.Fatal(bErr)
		}
	}
}

func BenchmarkCacheKey(b *testing.B) {
	b.ReportAllocs()
	for range b.N {
		_ = CacheKey("oauthkey-v1", "client_credentials", "https://login.example.com/oauth2/v2.0/token",
			"client-id", "a-long-client-secret-value-0123456789", "", "", "", "api://audience", "", "", "header", "scope-a scope-b", "")
	}
}
