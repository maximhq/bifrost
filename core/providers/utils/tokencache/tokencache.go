// Package tokencache holds the one credential cache every token-minting provider shares.
//
// A provider that authenticates with something other than a static API key (an OAuth
// client-credentials grant, a JWT bearer assertion, a GitHub App installation chain) has
// to mint a short-lived token, reuse it until shortly before it expires, make sure a burst
// of concurrent requests mints exactly once, and stop hammering the token endpoint when
// the credential is misconfigured. Cache[T] does all of that; the provider supplies only a
// Minter that knows how to perform its exchange.
//
// Request-path cost. A warm Get is one sync.Map load, one atomic pointer load and one
// time.Now; it takes no lock and allocates nothing. A cold or expiring Get takes the slot
// mutex, so concurrent callers queue behind the single exchange and leave with its result.
// Slots are never deleted: the slot is the lock and also carries the negative cache, so
// deleting it on invalidation would split goroutines across two mutexes for as long as any
// of them still held the old pointer, and would reset the failure backoff every time, so a
// permanently misconfigured key would retry on every request. Invalidation stores nil into
// the entry pointer and leaves the slot in place. The cost is that a rotated credential
// leaks its old slot, a few hundred bytes, bounded by the number of distinct historical
// credentials the process has seen. Hold the cache per provider so a config reload drops it.
package tokencache

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

const (
	// DefaultExchangeTimeout bounds one mint. Callers queued on the slot mutex wait at most this long.
	DefaultExchangeTimeout = 20 * time.Second
	// DefaultRefreshMargin is how long before expiry a token counts as stale.
	DefaultRefreshMargin = 60 * time.Second
)

// Default backoffs for a cached failure. Permanent faults are configuration errors and
// there is no point retrying them quickly; transient ones deserve a short pause.
var (
	DefaultPermanentBackoff = Backoff{Base: 2 * time.Second, Cap: 60 * time.Second}
	DefaultTransientBackoff = Backoff{Base: 500 * time.Millisecond, Cap: 10 * time.Second}
)

// Entry is one cached token. It is immutable once stored and replaced wholesale, so a
// reader sees either the whole old token or the whole new one.
type Entry[T any] struct {
	Value     T         // The token, or whatever the minter produced
	ExpiresAt time.Time // When the upstream says the token stops working
	RefreshAt time.Time // When the cache stops trusting Value and mints again; zero means ExpiresAt
}

// Minter mints a fresh entry. prev is the slot's current entry, possibly stale, or nil; the
// minter decides which parts of it are still worth reusing (a GitHub installation token that
// outlives the Copilot token minted from it, for example). ctx carries the exchange deadline
// and is detached from the caller's cancellation.
type Minter[T any] func(ctx context.Context, prev *Entry[T]) (*Entry[T], *schemas.BifrostError)

// Backoff grows the retry delay geometrically from Base and clamps it at Cap.
type Backoff struct {
	Base time.Duration
	Cap  time.Duration
}

// Options tunes a Cache. The zero value selects every default.
type Options struct {
	ExchangeTimeout time.Duration                    // Bound on one mint (default DefaultExchangeTimeout)
	Permanent       Backoff                          // Backoff after a permanent failure (default DefaultPermanentBackoff)
	Transient       Backoff                          // Backoff after a transient failure (default DefaultTransientBackoff)
	IsPermanent     func(*schemas.BifrostError) bool // Classifies a mint error (default IsPermanentError)
	Now             func() time.Time                 // Clock, for tests (default time.Now)
}

// Cache maps a credential's cache key to its token slot.
type Cache[T any] struct {
	slots sync.Map // cache key -> *slot[T]
	opts  Options
}

// slot is one cache position holding the token and the negative cache behind one refresh lock.
type slot[T any] struct {
	mu      sync.Mutex
	entry   atomic.Pointer[Entry[T]]
	failure atomic.Pointer[failure]
}

// failure is the negative cache. Without it, a permanently misconfigured key issues a token
// request for every inbound request, and the upstream's penalty lands on the whole credential.
type failure struct {
	err        *schemas.BifrostError
	retryAfter time.Time
	attempts   int
}

// New returns an empty cache. Pass Options{} for the defaults.
func New[T any](opts Options) *Cache[T] {
	if opts.ExchangeTimeout <= 0 {
		opts.ExchangeTimeout = DefaultExchangeTimeout
	}
	if opts.Permanent == (Backoff{}) {
		opts.Permanent = DefaultPermanentBackoff
	}
	if opts.Transient == (Backoff{}) {
		opts.Transient = DefaultTransientBackoff
	}
	if opts.IsPermanent == nil {
		opts.IsPermanent = IsPermanentError
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Cache[T]{opts: opts}
}

// Get returns the cached entry for cacheKey, minting one when there is none or it is due for
// refresh. Concurrent callers for the same key collapse onto one mint. A failure is cached
// and returned to every caller until its backoff elapses.
func (c *Cache[T]) Get(ctx context.Context, cacheKey string, mint Minter[T]) (*Entry[T], *schemas.BifrostError) {
	s := c.slot(cacheKey)
	now := c.opts.Now()

	// Fast path: one atomic load and one comparison, never blocking.
	e := s.entry.Load()
	if fresh(e, now) {
		return e, nil
	}
	// Checked before the lock, so a misconfigured key does not queue every in-flight request
	// behind a mutex just to be told no. A token past RefreshAt but not yet expired keeps
	// serving through the window: the refresh already failed once, and the old token is still
	// good for whatever is left of its life.
	if f := s.failure.Load(); f != nil && now.Before(f.retryAfter) {
		if usable(e, now) {
			return e, nil
		}
		return nil, copyError(f.err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Double-checked read. Every goroutine that queued behind the one that actually minted
	// exits here with the fresh token, having issued no request.
	now = c.opts.Now()
	e = s.entry.Load()
	if fresh(e, now) {
		return e, nil
	}
	if f := s.failure.Load(); f != nil && now.Before(f.retryAfter) {
		if usable(e, now) {
			return e, nil
		}
		return nil, copyError(f.err)
	}

	// WithoutCancel: the caller that happens to trigger a mint may hang up, but others are
	// blocked on the slot mutex waiting for its result. Killing the mint because one client
	// disconnected would turn one cancellation into many failures. Context values survive,
	// so tracing and latency accounting still land correctly.
	if ctx == nil {
		ctx = context.Background()
	}
	exchangeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.opts.ExchangeTimeout)
	defer cancel()

	prev := e
	e, bErr := mint(exchangeCtx, prev)
	if bErr == nil && e == nil {
		bErr = providerUtils.NewConfigurationError("token minter returned neither a token nor an error")
	}
	if bErr != nil {
		permanent := c.opts.IsPermanent(bErr)
		s.recordFailure(bErr, permanent, c)
		// A transient failure (5xx, 429, unreachable endpoint) during the refresh margin must not
		// fail requests the previous token can still serve; it is kept until it expires and the
		// retry happens after the backoff. A permanent failure means the credential itself is
		// bad, so the old token goes at once rather than hiding that for up to a margin. The
		// exchange may have taken longer than the token had left, so expiry is judged now, not
		// against the time read before the mint started.
		if !permanent && usable(prev, c.opts.Now()) {
			return prev, nil
		}
		s.entry.Store(nil)
		return nil, copyError(bErr)
	}
	if e.RefreshAt.IsZero() {
		e.RefreshAt = e.ExpiresAt
	}
	s.entry.Store(e)
	s.failure.Store(nil)
	return e, nil
}

// Invalidate discards the cached token for cacheKey so the next Get mints from scratch. The
// slot and its failure backoff stay, which is what keeps a same-key retry after an upstream
// 401 from becoming a loop.
func (c *Cache[T]) Invalidate(cacheKey string) {
	if s, ok := c.slots.Load(cacheKey); ok {
		s.(*slot[T]).entry.Store(nil)
	}
}

// Expire marks the cached token for cacheKey as due for refresh but keeps its value, so the
// next mint receives it as prev and can reuse whatever parts are still valid.
func (c *Cache[T]) Expire(cacheKey string) {
	s, ok := c.slots.Load(cacheKey)
	if !ok {
		return
	}
	// Swap only against the entry that was read: a plain Store would resurrect an entry that
	// Invalidate cleared between the load and the store.
	for {
		e := s.(*slot[T]).entry.Load()
		if e == nil {
			return
		}
		expired := *e
		expired.RefreshAt = time.Time{}
		if s.(*slot[T]).entry.CompareAndSwap(e, &expired) {
			return
		}
	}
}

// Peek returns the cached entry for cacheKey without minting, or nil. It is for tests and
// diagnostics; request paths use Get.
func (c *Cache[T]) Peek(cacheKey string) *Entry[T] {
	if s, ok := c.slots.Load(cacheKey); ok {
		return s.(*slot[T]).entry.Load()
	}
	return nil
}

func (c *Cache[T]) slot(cacheKey string) *slot[T] {
	if existing, ok := c.slots.Load(cacheKey); ok {
		return existing.(*slot[T])
	}
	actual, _ := c.slots.LoadOrStore(cacheKey, &slot[T]{})
	return actual.(*slot[T])
}

// fresh reports whether e may be served without a mint. A zero RefreshAt (set by Expire) is
// never fresh.
func fresh[T any](e *Entry[T], now time.Time) bool {
	return e != nil && now.Before(e.RefreshAt)
}

// usable reports whether e, though due for refresh, has not yet expired.
func usable[T any](e *Entry[T], now time.Time) bool {
	return e != nil && now.Before(e.ExpiresAt)
}

// copyError returns a shallow copy of a cached error. Core writes routing info and provider
// metadata into the error it is handed, so the cached original must never be shared between
// callers: each request gets its own struct, with the same message and status.
func copyError(bErr *schemas.BifrostError) *schemas.BifrostError {
	if bErr == nil {
		return nil
	}
	c := *bErr
	if bErr.Error != nil {
		field := *bErr.Error
		c.Error = &field
	}
	return &c
}

// recordFailure caches an error so repeated requests do not hammer the token endpoint. The
// window is the backoff for this attempt, or the endpoint's own Retry-After when that is
// longer: every caller shares the slot, so none of them may hit the endpoint before the
// pause it asked for has elapsed.
func (s *slot[T]) recordFailure(bErr *schemas.BifrostError, permanent bool, c *Cache[T]) {
	attempts := 1
	if prev := s.failure.Load(); prev != nil {
		attempts = prev.attempts + 1
	}
	backoff := c.opts.Transient
	if permanent {
		backoff = c.opts.Permanent
	}
	delay := backoff.For(attempts)
	if hinted := time.Duration(bErr.ExtraFields.RetryAfter) * time.Millisecond; hinted > delay {
		delay = hinted
	}
	s.failure.Store(&failure{
		err:        bErr,
		retryAfter: c.opts.Now().Add(delay),
		attempts:   attempts,
	})
}

// For returns the delay for the n-th consecutive failure: Base doubled n-1 times, clamped to Cap.
func (b Backoff) For(attempts int) time.Duration {
	delay := b.Base
	for i := 1; i < attempts && delay < b.Cap; i++ {
		delay *= 2
	}
	if delay > b.Cap {
		delay = b.Cap
	}
	return delay
}

// IsPermanentError reports whether a mint failure is a configuration fault rather than a
// transient one. Rate limits, server errors and failures with no HTTP status (the endpoint
// could not be reached) are transient; any other upstream status means something about the
// credential is wrong.
func IsPermanentError(bErr *schemas.BifrostError) bool {
	if bErr == nil || bErr.StatusCode == nil {
		return false
	}
	switch status := *bErr.StatusCode; {
	case status == 0, status == http.StatusTooManyRequests, status >= 500:
		return false
	default:
		return true
	}
}
