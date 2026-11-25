package livestore

import (
	"context"
	"fmt"
	"hash/maphash"
	"sync"
	"time"
)

// tableShards is how many locks a table's entries are spread over.
const tableShards = 64

// TableOption configures a table at creation.
type TableOption func(*tableOptions)

// tableOptions collects a table's options.
type tableOptions struct {
	ttl     time.Duration
	gen     func() uint64
	handles bool
}

// WithTTL gives every entry Put stores an expiry of ttl. Expired entries read as missing and
// are swept in the background.
func WithTTL(ttl time.Duration) TableOption {
	return func(o *tableOptions) { o.ttl = ttl }
}

// WithGeneration makes the table a memo: an entry stored under an older generation than gen
// returns now reads as missing.
func WithGeneration(gen func() uint64) TableOption {
	return func(o *tableOptions) { o.gen = gen }
}

// HoldsHandles marks a table whose values hold live handles, such as compiled programs or
// clients, rather than plain data. It changes nothing in memory; a backing that stores tables
// outside the heap keeps these in memory, since a handle cannot be encoded.
func HoldsHandles() TableOption {
	return func(o *tableOptions) { o.handles = true }
}

// Table is a map a component owns and writes itself: state tied to an entity, derived views,
// runtime status. It has no loader, so a miss means nothing was written yet, and v1 never evicts
// from it for size. Put replaces a value whole; a value is never changed after Put. A table
// starts empty, so it may be created after LoadAll, from a plugin's Init.
type Table[K comparable, V any] struct {
	seed   maphash.Seed
	opts   tableOptions
	shards [tableShards]tableShard[K, V]
}

// tableShard is one lock and its part of the table. An entry that neither expires nor carries a
// generation is kept in plain as its value alone, so a table whose entries never expire pays
// nothing per entry for either; every other entry is in m. A key is in at most one of the two, and
// each map is made by the first write that needs it.
type tableShard[K comparable, V any] struct {
	mu    sync.RWMutex
	plain map[K]V
	m     map[K]tableEntry[V]
}

// get returns the entry under k from whichever map holds it. The caller holds mu.
func (sh *tableShard[K, V]) get(k K) (tableEntry[V], bool) {
	if v, ok := sh.plain[k]; ok {
		return tableEntry[V]{v: v}, true
	}
	e, ok := sh.m[k]
	return e, ok
}

// set stores e under k, in plain when it neither expires nor carries a generation and in m
// otherwise, and takes k out of the other map. The caller holds mu.
func (sh *tableShard[K, V]) set(k K, e tableEntry[V]) {
	if e.expires == 0 && e.gen == 0 {
		if sh.plain == nil {
			sh.plain = make(map[K]V)
		}
		sh.plain[k] = e.v
		delete(sh.m, k)
		return
	}
	if sh.m == nil {
		sh.m = make(map[K]tableEntry[V])
	}
	sh.m[k] = e
	delete(sh.plain, k)
}

// del removes k and reports whether either map held it. The caller holds mu.
func (sh *tableShard[K, V]) del(k K) bool {
	_, inPlain := sh.plain[k]
	_, inM := sh.m[k]
	delete(sh.plain, k)
	delete(sh.m, k)
	return inPlain || inM
}

// tableEntry is one value with its expiry (zero for none) and generation.
type tableEntry[V any] struct {
	v       V
	expires int64
	gen     uint64
}

// NewTable creates a table under name. It fails when the name is empty, already registered or in
// the store's reserved internal: range, and with ErrInSubscriber when ctx is a subscriber's.
func (s *Store) NewTable[K comparable, V any](ctx context.Context, name string, opts ...TableOption) (*Table[K, V], error) {
	if err := inSubscriber(ctx); err != nil {
		return nil, err
	}
	if err := publicTableName(name); err != nil {
		return nil, err
	}
	t := newTable[K, V](collectTableOptions(opts))
	if err := s.addTable(name, t, ""); err != nil {
		return nil, err
	}
	return t, nil
}

// RegisterState creates a table keyed by IDs of kind of: removing an entity of that kind
// removes its entry. It fails when the name is empty, registered or in the reserved internal:
// range, or the kind is empty or unknown, and with ErrInSubscriber when ctx is a subscriber's. An
// empty kind is refused rather than made a plain table, whose entries no removal would clear.
func (s *Store) RegisterState[V any](ctx context.Context, name string, of Kind, opts ...TableOption) (*Table[string, *V], error) {
	if err := inSubscriber(ctx); err != nil {
		return nil, err
	}
	if err := publicTableName(name); err != nil {
		return nil, err
	}
	if of == "" {
		return nil, fmt.Errorf("livestore: state %q needs a kind", name)
	}
	t := newTable[string, *V](collectTableOptions(opts))
	if err := s.addTable(name, t, of); err != nil {
		return nil, err
	}
	return t, nil
}

// collectTableOptions applies opts.
func collectTableOptions(opts []TableOption) tableOptions {
	var o tableOptions
	for _, fn := range opts {
		fn(&o)
	}
	return o
}

// newTable builds an empty table that no store tracks yet.
func newTable[K comparable, V any](o tableOptions) *Table[K, V] {
	t := &Table[K, V]{seed: maphash.MakeSeed(), opts: o}
	return t
}

// shard returns the shard that holds k.
func (t *Table[K, V]) shard(k K) *tableShard[K, V] {
	return &t.shards[maphash.Comparable(t.seed, k)%tableShards]
}

// live reports whether e is neither expired at the given time nor from an older generation. An at
// of zero reads the clock, and only for an entry that can expire, so a read of a table without a
// TTL never pays for it.
func (t *Table[K, V]) live(e tableEntry[V], at int64) bool {
	if e.expires != 0 {
		if at == 0 {
			at = now().UnixNano()
		}
		if e.expires <= at {
			return false
		}
	}
	if t.opts.gen != nil && e.gen != t.opts.gen() {
		return false
	}
	return true
}

// entry builds the stored form of v with the given time to live; zero means never.
func (t *Table[K, V]) entry(v V, ttl time.Duration) tableEntry[V] {
	e := tableEntry[V]{v: v}
	if ttl > 0 {
		e.expires = now().Add(ttl).UnixNano()
	}
	if t.opts.gen != nil {
		e.gen = t.opts.gen()
	}
	return e
}

// Get returns the value under k.
func (t *Table[K, V]) Get(k K) (V, bool) {
	sh := t.shard(k)
	sh.mu.RLock()
	e, ok := sh.get(k)
	sh.mu.RUnlock()
	if !ok || !t.live(e, 0) {
		var zero V
		return zero, false
	}
	return e.v, true
}

// Expiry returns when k's live entry expires, and false when k holds no live entry or its entry
// never expires.
func (t *Table[K, V]) Expiry(k K) (time.Time, bool) {
	sh := t.shard(k)
	sh.mu.RLock()
	e, ok := sh.get(k)
	sh.mu.RUnlock()
	if !ok || !t.live(e, 0) || e.expires == 0 {
		return time.Time{}, false
	}
	return time.Unix(0, e.expires), true
}

// Put stores v under k, with the table's TTL.
func (t *Table[K, V]) Put(k K, v V) {
	t.PutWithTTL(k, v, t.opts.ttl)
}

// PutWithTTL stores v under k, expiring after ttl; zero never expires.
func (t *Table[K, V]) PutWithTTL(k K, v V, ttl time.Duration) {
	t.putEntry(k, t.entry(v, ttl))
}

// putEntry stores e under k.
func (t *Table[K, V]) putEntry(k K, e tableEntry[V]) {
	sh := t.shard(k)
	sh.mu.Lock()
	sh.set(k, e)
	sh.mu.Unlock()
}

// GetOrCompute returns k's value, or runs compute and returns what it gives, storing it when
// compute says to, with the TTL compute gives (zero for none). On a table with a generation the
// value is stored under the generation read before compute ran, so a value computed while the
// data changed is computed again on the next call. compute may run concurrently for one key; its
// results must be equivalent. Callers that hand the value on to code that may change it clone it.
func (t *Table[K, V]) GetOrCompute(k K, compute func() (v V, ttl time.Duration, store bool)) V {
	var gen uint64
	if t.opts.gen != nil {
		gen = t.opts.gen()
	}
	if v, ok := t.Get(k); ok {
		return v
	}
	v, ttl, store := compute()
	if store {
		e := t.entry(v, ttl)
		e.gen = gen
		t.putEntry(k, e)
	}
	return v
}

// PutIfAbsent stores v under k, expiring after ttl, only when k holds no live value. It reports
// whether it stored v.
func (t *Table[K, V]) PutIfAbsent(k K, v V, ttl time.Duration) bool {
	e := t.entry(v, ttl)
	sh := t.shard(k)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	if cur, ok := sh.get(k); ok && t.live(cur, 0) {
		return false
	}
	sh.set(k, e)
	return true
}

// CompareAndSwap stores next under k in t only if the live value is still old, compared with
// ==; a missing value compares as the zero value, so a nil old inserts only when k is absent.
// State tables hold pointers, and a value is never changed after it is stored, so "the same
// pointer" means nobody wrote since it was read. It is a function rather than a method so the
// compiler can require V to be comparable.
func CompareAndSwap[K, V comparable](t *Table[K, V], k K, old, next V) bool {
	e := t.entry(next, t.opts.ttl)
	sh := t.shard(k)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	var cur V
	if c, ok := sh.get(k); ok && t.live(c, 0) {
		cur = c.v
	}
	if cur != old {
		return false
	}
	sh.set(k, e)
	return true
}

// Delete removes k.
func (t *Table[K, V]) Delete(k K) {
	sh := t.shard(k)
	sh.mu.Lock()
	sh.del(k)
	sh.mu.Unlock()
}

// Len reports how many live entries t holds. It visits every shard, so it is for setup, resets
// and reports, not for a per-request path.
func (t *Table[K, V]) Len() int {
	at := now().UnixNano()
	n := 0
	for i := range t.shards {
		sh := &t.shards[i]
		sh.mu.RLock()
		for _, v := range sh.plain {
			if t.live(tableEntry[V]{v: v}, at) {
				n++
			}
		}
		for _, e := range sh.m {
			if t.live(e, at) {
				n++
			}
		}
		sh.mu.RUnlock()
	}
	return n
}

// Flush drops every entry of t. Unlike Delete it calls no delete observer: it is for a component
// resetting state it owns, not for a write that others following the table should hear about.
func (t *Table[K, V]) Flush() {
	t.clear()
}

// Range calls fn for every live entry until fn returns false. Each shard is copied before fn
// runs, so fn may write the table.
func (t *Table[K, V]) Range(fn func(k K, v V) bool) {
	at := now().UnixNano()
	for i := range t.shards {
		sh := &t.shards[i]
		sh.mu.RLock()
		keys := make([]K, 0, len(sh.plain)+len(sh.m))
		vals := make([]V, 0, len(sh.plain)+len(sh.m))
		for k, v := range sh.plain {
			if t.live(tableEntry[V]{v: v}, at) {
				keys = append(keys, k)
				vals = append(vals, v)
			}
		}
		for k, e := range sh.m {
			if t.live(e, at) {
				keys = append(keys, k)
				vals = append(vals, e.v)
			}
		}
		sh.mu.RUnlock()
		for j := range keys {
			if !fn(keys[j], vals[j]) {
				return
			}
		}
	}
}

// sweep removes entries that are expired at the given time or from an older generation.
func (t *Table[K, V]) sweep(at int64) {
	for i := range t.shards {
		sh := &t.shards[i]
		sh.mu.Lock()
		if t.opts.gen != nil {
			// A memo's entries stored under generation zero are in plain, and age with the rest.
			for k, v := range sh.plain {
				if !t.live(tableEntry[V]{v: v}, at) {
					delete(sh.plain, k)
				}
			}
		}
		for k, e := range sh.m {
			if !t.live(e, at) {
				delete(sh.m, k)
			}
		}
		sh.mu.Unlock()
	}
}

// clear removes every entry.
func (t *Table[K, V]) clear() {
	for i := range t.shards {
		sh := &t.shards[i]
		sh.mu.Lock()
		sh.plain, sh.m = nil, nil
		sh.mu.Unlock()
	}
}

// deleteKey removes the entry for an entity ID, for state tables keyed by string.
func (t *Table[K, V]) deleteKey(id string) {
	if k, ok := any(id).(K); ok {
		t.Delete(k)
	}
}
