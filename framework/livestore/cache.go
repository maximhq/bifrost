package livestore

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

// CacheLoader produces the value for a cache key on a miss: the value, the secondary-index key to
// register for it (empty for none), and an error. Errors are shared with concurrent callers but
// never cached.
type CacheLoader[V any] func() (value V, indexKey string, err error)

// Cache holds only the values someone asked for, fetched by a loader on a miss from wherever they
// live (the config store, another service), and dropped by expiry or eviction. A miss means "go
// fetch", never "does not exist": data whose every row memory must hold is a Collection, and data
// the process writes itself is a Table.
//
//   - Fill never hands a caller a load that started before its call, so a caller whose own write
//     and eviction committed while a load was running gets a fresh load, not that one. Callers that
//     arrive while a load runs share one follow-up load, so a burst costs at most two loads. A failed
//     load is not cached, so the next caller retries it. A panicking loader releases its waiters with
//     an error and re-raises on the stack of the caller that ran it.
//   - A generation counter increments on every eviction and flush. A fill records it before its
//     loader runs and drops its result if it moved, so a fill racing an eviction never re-installs
//     what the eviction removed.
//   - An optional secondary index maps a caller-chosen string (typically a row's primary key) to the
//     cache key holding it, so a write path that knows only the row can evict it.
//   - An optional validator runs on every hit; an entry it rejects is removed and reported as a
//     miss, so callers fall through to their full load path.
//
// The cache holds no lock across a loader call, so loaders may do database reads or network I/O.
// Unlike an LRU, a Cache has no capacity: entries stay until evicted or expired.
type Cache[V any] struct {
	store   *Store
	entries *Table[string, cacheEntry[V]]
	// byIndex maps a secondary-index key to the cache key holding it; a binding expires with its
	// entry.
	byIndex *Table[string, string]

	// mu guards generation, byIndex and every write to entries, so a generation check and the write
	// it allows are one step.
	mu         sync.Mutex
	generation uint64
	// versions numbers every install, so each entry's version is unique for the cache's life.
	versions    uint64
	validator   func(V) bool
	ttl         func(V) time.Duration
	negative    func(V) bool
	negativeTTL time.Duration

	// fills runs loads, one per key at a time, never sharing a load that started before a caller.
	fills readGroup
}

// cacheEntry is one cached value, its secondary-index key, and the version it was installed at,
// unique for the cache's life, so a validator's verdict on a value read earlier is acted on only if
// it is still that value.
type cacheEntry[V any] struct {
	indexKey string
	value    V
	version  uint64
}

// CacheOption configures a Cache.
type CacheOption[V any] func(*Cache[V])

// WithSharedFills makes callers of Fill that arrive while a load for the key is running share that
// load's result or error, instead of waiting for a load that starts after their call. It is for
// loads whose answer does not depend on when they started, such as a call to an upstream service,
// where concurrent callers should cause one call. Errors are still never cached.
func WithSharedFills[V any]() CacheOption[V] {
	return func(c *Cache[V]) { c.fills.shareInFlight = true }
}

// WithValidator installs a hit-time validity check: entries for which valid returns false are
// removed and reported as misses.
func WithValidator[V any](valid func(V) bool) CacheOption[V] {
	return func(c *Cache[V]) { c.validator = valid }
}

// WithCacheTTL gives each entry the TTL ttl reports for its value, so entries nobody reads again do
// not stay. Zero keeps the entry until it is evicted; a negative TTL means the value has already
// run out, and it is not installed.
func WithCacheTTL[V any](ttl func(V) time.Duration) CacheOption[V] {
	return func(c *Cache[V]) { c.ttl = ttl }
}

// WithCacheNegativeTTL keeps negative results, the ones negative matches, for ttl only. Zero keeps
// them until they are evicted, as for any entry, and a negative ttl keeps no negative result.
func WithCacheNegativeTTL[V any](negative func(V) bool, ttl time.Duration) CacheOption[V] {
	return func(c *Cache[V]) { c.negative, c.negativeTTL = negative, ttl }
}

// NewCache creates a cache under name. Its two tables are the store's own, internal:<name> and
// internal:<name>_index, so no component can drop them from under it. It fails
// when the name is empty or a cache of that name exists, and with ErrInSubscriber when ctx is a
// subscriber's.
func (s *Store) NewCache[V any](ctx context.Context, name string, opts ...CacheOption[V]) (*Cache[V], error) {
	if err := inSubscriber(ctx); err != nil {
		return nil, err
	}
	if name == "" {
		return nil, errors.New("livestore: empty cache name")
	}
	entries := newTable[string, cacheEntry[V]](internalPrefix+name, tableOptions{})
	if err := s.addTable(entries.Name(), entries, ""); err != nil {
		return nil, err
	}
	byIndex := newTable[string, string](internalPrefix+name+"_index", tableOptions{})
	if err := s.addTable(byIndex.Name(), byIndex, ""); err != nil {
		s.dropTable(entries.Name())
		return nil, err
	}
	c := &Cache[V]{store: s, entries: entries, byIndex: byIndex}
	for _, opt := range opts {
		opt(c)
	}
	return c, nil
}

// Get returns the cached value for key. An entry the validator rejects is removed and reported as a
// miss.
func (c *Cache[V]) Get(key string) (V, bool) {
	var zero V
	ent, ok := c.entries.Get(key)
	if !ok {
		return zero, false
	}
	// The validator is caller code and may call back into the cache, so it runs without c.mu; the
	// entry is removed only if it is still the version that was judged.
	if c.validator != nil && !c.validator(ent.value) {
		c.mu.Lock()
		if cur, ok := c.entries.Get(key); ok && cur.version == ent.version {
			c.removeLocked(key, cur)
		}
		c.mu.Unlock()
		return zero, false
	}
	return ent.value, true
}

// Expiry returns when key's entry expires, and false when key holds no live entry or its entry
// never expires.
func (c *Cache[V]) Expiry(key string) (time.Time, bool) {
	return c.entries.Expiry(key)
}

// Fill runs load for key and installs its result, unless an eviction or flush landed while the
// load ran; errors are returned but never cached. A caller never receives a load that started
// before its call: one that arrives while a load runs waits for it and shares one follow-up load
// with every other caller that arrived meanwhile, unless the cache was built WithSharedFills. A waiting caller whose ctx ends returns ctx.Err()
// without waiting further.
func (c *Cache[V]) Fill(ctx context.Context, key string, load CacheLoader[V]) (V, error) {
	var panicked any
	out, err := c.fills.DoValue(ctx, key, func() (any, error) {
		c.mu.Lock()
		gen := c.generation
		c.mu.Unlock()
		value, indexKey, err := c.runLoader(load, &panicked)
		if err == nil {
			c.setIfGeneration(key, indexKey, value, gen)
		}
		return value, err
	})
	if panicked != nil {
		panic(panicked)
	}
	value, _ := out.(V)
	return value, err
}

// runLoader calls load, turning a panic into an error for the callers the load serves and
// recording it in panicked, so Fill re-raises it on the stack of the caller that ran the load.
func (c *Cache[V]) runLoader(load CacheLoader[V], panicked *any) (value V, indexKey string, err error) {
	defer func() {
		if r := recover(); r != nil {
			*panicked = r
			err = fmt.Errorf("livestore: cache loader panicked: %v", r)
		}
	}()
	return load()
}

// setIfGeneration installs value under key unless an eviction or flush moved the generation since
// gen was read. A value whose TTL has already run out is not installed, and the entry it would have
// replaced keeps its index binding, so an eviction by that index key still reaches it. The TTL is
// worked out before c.mu is taken: the TTL functions are caller code and may call back into the
// cache, and they read only the value.
func (c *Cache[V]) setIfGeneration(key, indexKey string, value V, gen uint64) {
	ttl, keep := c.entryTTL(value)
	if !keep {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.generation != gen {
		return
	}
	c.versions++
	next := cacheEntry[V]{indexKey: indexKey, value: value, version: c.versions}
	if cur, ok := c.entries.Get(key); ok && cur.indexKey != indexKey && cur.indexKey != "" {
		if owner, _ := c.byIndex.Get(cur.indexKey); owner == key {
			c.byIndex.Delete(cur.indexKey)
		}
	}
	if indexKey != "" {
		c.evictIndexOwnerLocked(indexKey, key)
		c.byIndex.PutWithTTL(indexKey, key, ttl)
	}
	c.entries.PutWithTTL(key, next, ttl)
}

// entryTTL is the TTL an entry for value is stored with, zero for none, and false when the value has
// already run out, which a negative TTL means for negative results as for any other.
func (c *Cache[V]) entryTTL(value V) (time.Duration, bool) {
	if c.negative != nil && c.negative(value) {
		return c.negativeTTL, c.negativeTTL >= 0
	}
	if c.ttl == nil {
		return 0, true
	}
	ttl := c.ttl(value)
	return ttl, ttl >= 0
}

// evictIndexOwnerLocked removes the entry bound to indexKey when it is not newKey, an invalidation
// of that key: the generation moves so a fill for the former owner cannot re-install it. Caller
// holds c.mu.
func (c *Cache[V]) evictIndexOwnerLocked(indexKey, newKey string) {
	owner, ok := c.byIndex.Get(indexKey)
	if !ok || owner == newKey {
		return
	}
	if cur, ok := c.entries.Get(owner); ok {
		c.generation++
		c.removeLocked(owner, cur)
	}
}

// removeLocked drops key's entry and its index binding. Caller holds c.mu.
func (c *Cache[V]) removeLocked(key string, ent cacheEntry[V]) {
	c.entries.Delete(key)
	if ent.indexKey != "" {
		if owner, _ := c.byIndex.Get(ent.indexKey); owner == key {
			c.byIndex.Delete(ent.indexKey)
		}
	}
}

// Evict removes the entry for key, if any.
func (c *Cache[V]) Evict(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.generation++
	if cur, ok := c.entries.Get(key); ok {
		c.removeLocked(key, cur)
	}
}

// EvictByIndex removes the entry registered under indexKey, if any.
func (c *Cache[V]) EvictByIndex(indexKey string) {
	if indexKey == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.generation++
	if key, ok := c.byIndex.Get(indexKey); ok {
		if cur, ok := c.entries.Get(key); ok {
			c.removeLocked(key, cur)
		}
	}
}

// EvictWhere removes every entry whose key matches pred: a linear sweep for rare bulk
// invalidations. The generation moves before pred runs, so a fill that completes during the sweep
// sees the invalidation even for a key not cached when the sweep started.
func (c *Cache[V]) EvictWhere(pred func(key string) bool) {
	if pred == nil {
		return
	}
	c.mu.Lock()
	c.generation++
	c.mu.Unlock()
	// pred is caller code and runs without c.mu, as Get's validator does.
	var keys []string
	c.entries.Range(func(key string, _ cacheEntry[V]) bool {
		if pred(key) {
			keys = append(keys, key)
		}
		return true
	})
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, key := range keys {
		if cur, ok := c.entries.Get(key); ok {
			c.removeLocked(key, cur)
		}
	}
}

// Flush drops every entry.
func (c *Cache[V]) Flush() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.generation++
	c.entries.clear()
	c.byIndex.clear()
}

// Len reports the number of entries.
func (c *Cache[V]) Len() int {
	n := 0
	c.entries.Range(func(string, cacheEntry[V]) bool { n++; return true })
	return n
}

// EncodeKey builds a collision-free composite key from parts, for
// callers whose key is a tuple of caller-controlled strings (e.g.
// (auth mode, identity, mcp client ID): identity is frequently a
// caller-asserted string with no charset restriction). A naive
// separator-joined key lets one part's content forge a boundary, e.g.
// join("\x00", "a\x00b", "c") and join("\x00", "a", "b\x00c") would build
// the identical string, aliasing two distinct tuples onto one entry
// and skewing any eviction predicate that parses the key back apart.
// Length-prefixing each part makes that forgery impossible regardless of
// what bytes a part contains. Pair with DecodeKey to parse it back. The key
// is built in one allocation of its exact size.
func EncodeKey(parts ...string) string {
	size := 0
	for _, p := range parts {
		size += decimalLen(len(p)) + 1 + len(p)
	}
	var b strings.Builder
	b.Grow(size)
	var digits [20]byte
	for _, p := range parts {
		b.Write(strconv.AppendInt(digits[:0], int64(len(p)), 10))
		b.WriteByte(':')
		b.WriteString(p)
	}
	return b.String()
}

// decimalLen is how many digits strconv.Itoa writes for n, which is never
// negative here.
func decimalLen(n int) int {
	digits := 1
	for n >= 10 {
		n /= 10
		digits++
	}
	return digits
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
