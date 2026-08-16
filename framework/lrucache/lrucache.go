// Package lrucache provides a bounded, generation-guarded LRU cache with
// single-flight fills, built for in-memory mirrors of database rows that are
// kept consistent by explicit eviction rather than TTLs.
//
// Design points, all load-bearing for correctness:
//
//   - Fills are single-flight: concurrent callers for the same key wait on
//     one leader and share its result and error. Errors are propagated to
//     every waiter but never cached, so a failed load is retried by the next
//     caller. A panicking loader releases its waiters with an error and
//     re-raises on the leader's own stack.
//   - A generation counter increments on every explicit eviction or flush.
//     A fill records the generation before running its loader and its
//     install is discarded if the generation moved, so a fill racing an
//     eviction can never re-install the value the eviction just removed.
//     Capacity (LRU) eviction does not increment it: dropping a still-valid
//     entry for space is not an invalidation.
//   - An optional secondary index maps a caller-chosen string (typically a
//     row's primary key) to the cache key holding it, so write paths that
//     only know the row identity can evict without recomputing the key.
//   - An optional validator runs on every hit; an entry it rejects is
//     removed and reported as a miss, so callers fall through to their full
//     load path (used, for example, to treat expired credentials as misses
//     while keeping all expiry policy in the caller).
//
// The cache owns its own locking and never holds a lock across a loader
// call, so loaders are free to perform database reads or network I/O. It
// must not be guarded by any caller-side lock that is itself held across
// I/O; give the cache exclusive ownership of its consistency instead.
//
// All methods are safe on a nil receiver: reads miss, evictions no-op, and
// Fill degrades to calling the loader directly. This lets consumers treat
// an absent cache as "caching disabled" without branching.
package lrucache

import (
	"container/list"
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
)

// Loader produces the value for a cache key on a miss. It returns the value,
// the secondary-index key to register for it (empty for none), and an error.
// Errors are shared with concurrent waiters but never cached.
type Loader[V any] func() (value V, indexKey string, err error)

// inflightCall represents one in-progress fill for a cache key. Concurrent
// callers for the same key wait on done and share result/err.
type inflightCall[V any] struct {
	done   chan struct{}
	result V
	err    error
}

type entry[V any] struct {
	key      string
	indexKey string
	value    V
	// version increments on every in-place value update (setIfGeneration's
	// existing-entry branch). An update reuses the same *list.Element, so
	// element identity alone can't distinguish "still the value I read" from
	// "replaced since I read it" — version closes that gap. See Get's use.
	version uint64
}

// Cache is a bounded LRU cache with single-flight fills. Construct with New;
// the zero value is not usable (but a nil *Cache is, see the package doc).
type Cache[V any] struct {
	mu       sync.Mutex
	capacity int
	items    map[string]*list.Element
	order    *list.List // front = most recently used
	// byIndex maps a secondary-index key to the cache key holding it.
	byIndex map[string]string
	// generation increments on every explicit eviction or flush; see the
	// package doc for the fill-race guarantee it provides.
	generation uint64
	// validator, when non-nil, is consulted on every hit; entries it
	// rejects are removed and reported as misses.
	validator func(V) bool

	inflightMu sync.Mutex
	inflight   map[string]*inflightCall[V]
}

// Option configures a Cache at construction time.
type Option[V any] func(*Cache[V])

// WithValidator installs a hit-time validity check: entries for which valid
// returns false are removed and reported as misses.
func WithValidator[V any](valid func(V) bool) Option[V] {
	return func(c *Cache[V]) {
		c.validator = valid
	}
}

// New constructs a Cache bounded to capacity entries. A non-positive
// capacity panics: the bound is part of the type's contract, and silently
// defaulting it would hide a caller bug.
func New[V any](capacity int, opts ...Option[V]) *Cache[V] {
	if capacity <= 0 {
		panic("lrucache: capacity must be positive")
	}
	c := &Cache[V]{
		capacity: capacity,
		items:    make(map[string]*list.Element),
		order:    list.New(),
		byIndex:  make(map[string]string),
		inflight: make(map[string]*inflightCall[V]),
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Get returns the cached value for key. An entry rejected by the validator
// is removed and reported as a miss.
func (c *Cache[V]) Get(key string) (V, bool) {
	var zero V
	if c == nil {
		return zero, false
	}
	c.mu.Lock()
	elem, ok := c.items[key]
	if !ok {
		c.mu.Unlock()
		return zero, false
	}
	ent := elem.Value.(*entry[V])
	value := ent.value
	version := ent.version
	validator := c.validator
	c.mu.Unlock()

	// validator runs without c.mu held: it's caller code and may re-enter
	// the cache (e.g. call Len()), which would deadlock on this
	// non-reentrant mutex if still held here. Re-check the entry is still
	// the same one — by *list.Element identity AND version — before acting
	// on it. Identity alone isn't enough: setIfGeneration's existing-entry
	// branch updates ent.value in place on the same element, so a
	// concurrent Fill installing a fresh value between the unlock above and
	// this re-lock would otherwise look unchanged, and this validator's
	// verdict on the stale value we already read could wrongly evict it.
	if validator != nil && !validator(value) {
		c.mu.Lock()
		if cur, ok := c.items[key]; ok && cur == elem && cur.Value.(*entry[V]).version == version {
			c.removeLocked(cur)
		}
		c.mu.Unlock()
		return zero, false
	}

	c.mu.Lock()
	if cur, ok := c.items[key]; ok && cur == elem {
		c.order.MoveToFront(cur)
	}
	c.mu.Unlock()
	return value, true
}

// Fill runs load for key with single-flight deduplication: concurrent
// callers for the same key wait for one leader and share its result and
// error. A successful result is cached unless an eviction or flush landed
// while the load was in flight; errors are propagated but never cached.
//
// A follower waits on its own ctx as well as the leader's completion: if
// ctx is canceled first, Fill returns ctx.Err() immediately rather than
// blocking until the (possibly unrelated, possibly slow) leader finishes.
// The leader itself is unaffected by ctx here — canceling it is load's own
// responsibility, since load is the one actually doing the I/O.
func (c *Cache[V]) Fill(ctx context.Context, key string, load Loader[V]) (V, error) {
	if c == nil {
		v, _, err := load()
		return v, err
	}
	c.inflightMu.Lock()
	if call, ok := c.inflight[key]; ok {
		c.inflightMu.Unlock()
		select {
		case <-call.done:
			return call.result, call.err
		case <-ctx.Done():
			var zero V
			return zero, ctx.Err()
		}
	}
	call := &inflightCall[V]{done: make(chan struct{})}
	c.inflight[key] = call
	c.inflightMu.Unlock()

	// Deferred so a panicking loader still releases the waiters and clears
	// the inflight slot instead of wedging this key for the process
	// lifetime. Waiters must never mistake a panicked load for success, so
	// an error is set before the channel closes and the panic is re-raised
	// for the leader's own stack.
	defer func() {
		// Delete before close, not after: a new caller acquiring inflightMu
		// between the two would otherwise find the stale map entry, see
		// call.done already closed, and incorrectly reuse this completed
		// (possibly errored or panic-derived) result instead of starting its
		// own fresh load. Already-blocked followers are unaffected — they
		// hold a direct reference to call, not a map lookup.
		c.inflightMu.Lock()
		delete(c.inflight, key)
		c.inflightMu.Unlock()
		if r := recover(); r != nil {
			call.err = fmt.Errorf("lrucache: loader panicked: %v", r)
			close(call.done)
			panic(r)
		}
		close(call.done)
	}()

	gen := c.currentGeneration()
	value, indexKey, err := load()
	call.result, call.err = value, err

	if err == nil {
		c.setIfGeneration(key, indexKey, value, gen)
	}
	return value, err
}

func (c *Cache[V]) currentGeneration() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.generation
}

// setIfGeneration installs value under key unless the generation moved since
// gen was read, meaning an eviction or flush invalidated state while the
// loader was running.
func (c *Cache[V]) setIfGeneration(key, indexKey string, value V, gen uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.generation != gen {
		return
	}
	if elem, ok := c.items[key]; ok {
		ent := elem.Value.(*entry[V])
		if ent.indexKey != indexKey && ent.indexKey != "" {
			if mapped, ok := c.byIndex[ent.indexKey]; ok && mapped == key {
				delete(c.byIndex, ent.indexKey)
			}
		}
		ent.value = value
		ent.indexKey = indexKey
		ent.version++
		if indexKey != "" {
			c.evictIndexOwnerLocked(indexKey, key)
			c.byIndex[indexKey] = key
		}
		c.order.MoveToFront(elem)
		return
	}
	// Remove any prior owner of indexKey before checking capacity: otherwise
	// a rebind onto a full cache can remove both the LRU tail (capacity
	// eviction) and the old owner (index rebind), leaving the cache below
	// capacity for one insert.
	if indexKey != "" {
		c.evictIndexOwnerLocked(indexKey, key)
	}
	if c.order.Len() >= c.capacity {
		if tail := c.order.Back(); tail != nil {
			c.removeLocked(tail)
		}
	}
	elem := c.order.PushFront(&entry[V]{key: key, indexKey: indexKey, value: value})
	c.items[key] = elem
	if indexKey != "" {
		c.byIndex[indexKey] = key
	}
}

// evictIndexOwnerLocked removes the cache entry currently bound to indexKey,
// if any and if it isn't newKey itself. Rebinding an index key to a
// different cache key without this would leave the previous owner cached
// but unreachable through the index, so EvictByIndex could never remove it.
// Caller holds c.mu.
func (c *Cache[V]) evictIndexOwnerLocked(indexKey, newKey string) {
	owner, ok := c.byIndex[indexKey]
	if !ok || owner == newKey {
		return
	}
	if ownerElem, ok := c.items[owner]; ok {
		// Removing another key's entry is an invalidation of that key, not a
		// capacity eviction: bump generation so an in-flight fill for the
		// former owner cannot re-install it and steal the index binding back.
		c.generation++
		c.removeLocked(ownerElem)
	}
}

// removeLocked drops an entry from the list, the key map, and the secondary
// index. Caller holds c.mu.
func (c *Cache[V]) removeLocked(elem *list.Element) {
	ent := elem.Value.(*entry[V])
	c.order.Remove(elem)
	delete(c.items, ent.key)
	if ent.indexKey != "" {
		if mapped, ok := c.byIndex[ent.indexKey]; ok && mapped == ent.key {
			delete(c.byIndex, ent.indexKey)
		}
	}
}

// Evict removes the entry for an exact key, if present.
func (c *Cache[V]) Evict(key string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.generation++
	if elem, ok := c.items[key]; ok {
		c.removeLocked(elem)
	}
}

// EvictByIndex removes the entry registered under the given secondary-index
// key, if any.
func (c *Cache[V]) EvictByIndex(indexKey string) {
	if c == nil || indexKey == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.generation++
	if key, ok := c.byIndex[indexKey]; ok {
		if elem, ok := c.items[key]; ok {
			c.removeLocked(elem)
		}
	}
}

// EvictWhere removes every entry whose cache key matches pred. A linear
// sweep under the lock; intended for rare bulk invalidations, not hot
// paths.
func (c *Cache[V]) EvictWhere(pred func(key string) bool) {
	if c == nil || pred == nil {
		return
	}
	c.mu.Lock()
	// Bumped here, before pred runs — not after, in the second locked
	// section below. Evict/EvictByIndex/Flush all bump generation
	// immediately for the same reason: pred can run arbitrarily long (it's
	// caller code, possibly re-entering the cache), and any Fill whose
	// generation check lands in that window must see this invalidation as
	// already having happened, even for a key that wasn't cached yet when
	// this call started (and so can never appear in toRemove below).
	c.generation++
	keys := make([]string, 0, c.order.Len())
	for elem := c.order.Front(); elem != nil; elem = elem.Next() {
		keys = append(keys, elem.Value.(*entry[V]).key)
	}
	c.mu.Unlock()

	// pred runs without c.mu held, for the same re-entrancy reason as
	// Get's validator call above.
	var toRemove []string
	for _, key := range keys {
		if pred(key) {
			toRemove = append(toRemove, key)
		}
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	for _, key := range toRemove {
		if elem, ok := c.items[key]; ok {
			c.removeLocked(elem)
		}
	}
}

// Flush drops every cached entry.
func (c *Cache[V]) Flush() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.generation++
	c.items = make(map[string]*list.Element)
	c.byIndex = make(map[string]string)
	c.order.Init()
}

// Len reports the number of cached entries.
func (c *Cache[V]) Len() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.order.Len()
}

// EncodeKey builds a collision-free composite cache key from parts, for
// callers whose key is a tuple of caller-controlled strings (e.g.
// (auth mode, identity, mcp client ID) — identity is frequently a
// caller-asserted string with no charset restriction). A naive
// separator-joined key lets one part's content forge a boundary — e.g.
// join("\x00", "a\x00b", "c") and join("\x00", "a", "b\x00c") would build
// the identical string, aliasing two distinct tuples onto one cache entry
// and skewing any eviction predicate that parses the key back apart.
// Length-prefixing each part makes that forgery impossible regardless of
// what bytes a part contains. Pair with DecodeKey to parse it back.
func EncodeKey(parts ...string) string {
	var b strings.Builder
	for _, p := range parts {
		b.WriteString(strconv.Itoa(len(p)))
		b.WriteByte(':')
		b.WriteString(p)
	}
	return b.String()
}

// DecodeKey is EncodeKey's inverse: it parses exactly n length-prefixed
// parts out of key, in the order EncodeKey wrote them. ok is false for a
// key that isn't in the length-prefixed form EncodeKey builds for n parts
// (impossible for keys a well-behaved caller produces with EncodeKey).
func DecodeKey(key string, n int) (parts []string, ok bool) {
	if n < 0 {
		return nil, false
	}
	rest := key
	parts = make([]string, 0, n)
	for range n {
		i := strings.IndexByte(rest, ':')
		if i < 0 {
			return nil, false
		}
		lengthText := rest[:i]
		length, err := strconv.Atoi(lengthText)
		if err != nil || length < 0 || strconv.Itoa(length) != lengthText {
			return nil, false
		}
		rest = rest[i+1:]
		if length > len(rest) {
			return nil, false
		}
		parts = append(parts, rest[:length])
		rest = rest[length:]
	}
	if rest != "" {
		return nil, false
	}
	return parts, true
}
