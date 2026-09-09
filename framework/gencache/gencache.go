// Package gencache provides a generation-stamped memo cache: a value cache whose
// entries are invalidated wholesale when a monotonic "generation" counter advances.
//
// Unlike an LRU, there is no per-entry TTL and no eviction order. Every entry
// carries the generation it was computed under; a read is a hit only if that stamp
// still matches the current generation. Any change to the underlying data bumps the
// generation, which invalidates every entry at once without touching the map.
//
// It fits when: the value is a pure function of some shared state, that state
// changes rarely relative to reads, and a single monotonic counter can stand in for
// "the state changed" (e.g. a sum of write counters across the stores the value is
// derived from).
package gencache

import "sync"

// GenFunc returns the current generation. It must strictly increase (never repeat a
// prior value) whenever the data the cache derives from changes; any increase
// invalidates every cached entry. It is called on every Get/GetOrCompute, so keep it
// cheap — typically a sum of atomic counters.
type GenFunc func() uint64

type entry[V any] struct {
	gen uint64
	val V
}

// Cache is a concurrent generation-stamped memo. The zero value is not usable;
// construct one with New.
type Cache[V any] struct {
	mu         sync.RWMutex
	m          map[string]entry[V]
	gen        GenFunc
	maxEntries int
	clone      func(V) V
	skipStore  func(V) bool
}

// Option configures a Cache.
type Option[V any] func(*Cache[V])

// WithClone makes Get and GetOrCompute return clone(v) rather than the stored value,
// so callers cannot mutate cached state through the value they receive. Use it for
// mutable values such as slices or maps (e.g. a wrapper around slices.Clone).
// Default: the stored value is returned as-is, which is correct only when V is
// treated as immutable.
func WithClone[V any](clone func(V) V) Option[V] {
	return func(c *Cache[V]) { c.clone = clone }
}

// WithSkipStore leaves results matching skip out of the cache: they are still
// returned, but recomputed on the next call. Use it to avoid caching "not found"
// answers, e.g. func(v []T) bool { return len(v) == 0 }. Default: every result is
// stored.
func WithSkipStore[V any](skip func(V) bool) Option[V] {
	return func(c *Cache[V]) { c.skipStore = skip }
}

// New builds a Cache. gen supplies the current generation (see GenFunc). maxEntries
// bounds the map: when inserting a new key would exceed it, the whole map is flushed
// first, because cache keys are often client-controlled and an unbounded map is a
// memory-growth vector. A maxEntries <= 0 means unbounded.
func New[V any](gen GenFunc, maxEntries int, opts ...Option[V]) *Cache[V] {
	c := &Cache[V]{
		m:          make(map[string]entry[V]),
		gen:        gen,
		maxEntries: maxEntries,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

func (c *Cache[V]) out(v V) V {
	if c.clone != nil {
		return c.clone(v)
	}
	return v
}

// Get returns the cached value for key when it is present and still current
// (honoring WithClone). The bool reports whether it was a live hit; a stale or
// missing entry returns the zero value and false.
func (c *Cache[V]) Get(key string) (V, bool) {
	gen := c.gen()
	c.mu.RLock()
	e, ok := c.m[key]
	c.mu.RUnlock()
	if ok && e.gen == gen {
		return c.out(e.val), true
	}
	var zero V
	return zero, false
}

// GetOrCompute returns the current cached value for key, or computes it with
// compute, stores it, and returns it.
//
// The stored entry is stamped with the generation read BEFORE compute ran: if the
// underlying data changes while compute is in flight, the entry is left stale and
// the next call recomputes — the cache never serves a stale read. Honors WithClone
// on the returned value and WithSkipStore (a skipped result is returned but not
// cached). compute may run concurrently for the same key under load; it must be
// safe to call redundantly and its results must be equivalent.
func (c *Cache[V]) GetOrCompute(key string, compute func() V) V {
	gen := c.gen()

	c.mu.RLock()
	e, ok := c.m[key]
	c.mu.RUnlock()
	if ok && e.gen == gen {
		return c.out(e.val)
	}

	val := compute()
	if c.skipStore != nil && c.skipStore(val) {
		return c.out(val)
	}

	c.mu.Lock()
	if c.maxEntries > 0 {
		if _, exists := c.m[key]; !exists && len(c.m) >= c.maxEntries {
			c.m = make(map[string]entry[V])
		}
	}
	c.m[key] = entry[V]{gen: gen, val: val}
	c.mu.Unlock()

	return c.out(val)
}

// Len returns the number of entries currently held, counting stale ones that have
// not yet been overwritten or flushed.
func (c *Cache[V]) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.m)
}

// Flush drops every entry.
func (c *Cache[V]) Flush() {
	c.mu.Lock()
	c.m = make(map[string]entry[V])
	c.mu.Unlock()
}
