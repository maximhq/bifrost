package utils

import (
	"container/list"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/maximhq/bifrost/core/schemas"
)

// DefaultModelCapabilitiesCacheSize bounds the resident capability set. A
// deployment's working set is a few dozen models, so the cap is generous;
// anything evicted is refetched through the miss handler.
const DefaultModelCapabilitiesCacheSize = 2048

// capabilitiesCacheEntry is one LRU slot. A nil caps is a tombstone — the
// datasheet has no record for this key — cached so an unknown model does not
// re-query on every one of the ~10 capability checks a single request makes.
type capabilitiesCacheEntry struct {
	key        string
	caps       *schemas.ModelCapabilities
	generation uint64
}

// inflightCall represents an in-progress cache miss handler invocation.
// Multiple goroutines waiting for the same key share one call.
type inflightCall struct {
	done   chan struct{}
	result *schemas.ModelCapabilities
}

type modelCapabilitiesCache struct {
	mu               sync.RWMutex
	capacity         int
	items            map[string]*list.Element
	order            *list.List // front = most recently inserted/updated
	cacheMissHandler func(rowKey string) *schemas.ModelCapabilities

	// generation is bumped whenever the datasheet is reloaded. Entries stamped
	// with an older value read as misses, so one atomic write invalidates the
	// whole cache — tombstones included — without touching a single entry.
	generation atomic.Uint64

	// alias resolves "<model>|<provider>" to the datasheet row key holding its
	// record, so a miss costs one lookup instead of a candidate chain.
	aliasMu sync.RWMutex
	alias   map[string]string

	inflightMu sync.Mutex
	inflight   map[string]*inflightCall
}

var (
	globalModelCapabilitiesCache *modelCapabilitiesCache
	cacheOnce                    sync.Once
)

// knownAnthropicMaxOutputTokens provides static fallback defaults for Claude models
// when both cache and DB miss handler return nothing. Only Anthropic requires max_tokens.
var knownAnthropicMaxOutputTokens = map[string]int{
	"claude-opus-5":     128000,
	"claude-mythos":     128000,
	"claude-fable-5":    128000,
	"claude-opus-4-8":   128000,
	"claude-opus-4-7":   128000,
	"claude-opus-4-6":   128000,
	"claude-sonnet-5":   128000,
	"claude-sonnet-4-6": 64000,
	"claude-haiku-4-5":  64000,
	"claude-sonnet-4-5": 64000,
	"claude-opus-4-5":   64000,
	"claude-opus-4-1":   32000,
	"claude-sonnet-4":   64000,
	"claude-opus-4":     32000,
	"claude-sonnet-4-0": 64000,
	"claude-opus-4-0":   32000,
	"claude-3-5-sonnet": 8192,
	"claude-3-5-haiku":  8192,
	"claude-3-7-sonnet": 8192,
	"claude-3-opus":     4096,
	"claude-3-sonnet":   4096,
	"claude-3-haiku":    4096,
}

func newModelCapabilitiesCache(capacity int) *modelCapabilitiesCache {
	return &modelCapabilitiesCache{
		capacity: capacity,
		items:    make(map[string]*list.Element, capacity),
		order:    list.New(),
		alias:    make(map[string]string),
		inflight: make(map[string]*inflightCall),
	}
}

func getModelCapabilitiesCache() *modelCapabilitiesCache {
	cacheOnce.Do(func() {
		globalModelCapabilitiesCache = newModelCapabilitiesCache(DefaultModelCapabilitiesCacheSize)
	})
	return globalModelCapabilitiesCache
}

// lookup returns the cached record for key. found reports whether the key is
// present at the current generation; caps may still be nil, meaning the entry is
// a tombstone.
func (c *modelCapabilitiesCache) lookup(key string, generation uint64) (*schemas.ModelCapabilities, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	elem, ok := c.items[key]
	if !ok {
		return nil, false
	}
	entry := elem.Value.(*capabilitiesCacheEntry)
	if entry.generation != generation {
		// Stale: the datasheet reloaded since this was written.
		c.order.Remove(elem)
		delete(c.items, key)
		return nil, false
	}
	c.order.MoveToFront(elem)
	return entry.caps, true
}

// store writes caps under key. A nil caps writes a tombstone.
func (c *modelCapabilitiesCache) store(key string, caps *schemas.ModelCapabilities, generation uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.storeLocked(key, caps, generation)
}

func (c *modelCapabilitiesCache) storeLocked(key string, caps *schemas.ModelCapabilities, generation uint64) {
	if elem, ok := c.items[key]; ok {
		entry := elem.Value.(*capabilitiesCacheEntry)
		entry.caps = caps
		entry.generation = generation
		c.order.MoveToFront(elem)
		return
	}

	if c.capacity > 0 && c.order.Len() >= c.capacity {
		c.evict()
	}

	c.items[key] = c.order.PushFront(&capabilitiesCacheEntry{key: key, caps: caps, generation: generation})
}

func (c *modelCapabilitiesCache) bulkStore(entries map[string]*schemas.ModelCapabilities, generation uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for key, caps := range entries {
		c.storeLocked(key, caps, generation)
	}
}

func (c *modelCapabilitiesCache) evict() {
	tail := c.order.Back()
	if tail == nil {
		return
	}
	c.order.Remove(tail)
	delete(c.items, tail.Value.(*capabilitiesCacheEntry).key)
}

func (c *modelCapabilitiesCache) delete(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if elem, ok := c.items[key]; ok {
		c.order.Remove(elem)
		delete(c.items, key)
	}
}

func (c *modelCapabilitiesCache) handler() func(string) *schemas.ModelCapabilities {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.cacheMissHandler
}

func (c *modelCapabilitiesCache) aliasFor(key string) string {
	c.aliasMu.RLock()
	defer c.aliasMu.RUnlock()
	return c.alias[key]
}

// fetch invokes the miss handler for rowKey, deduplicating concurrent calls for
// the same key so one request's capability checks cost at most one query.
func (c *modelCapabilitiesCache) fetch(rowKey string, handler func(string) *schemas.ModelCapabilities) *schemas.ModelCapabilities {
	c.inflightMu.Lock()
	if call, ok := c.inflight[rowKey]; ok {
		c.inflightMu.Unlock()
		<-call.done
		return call.result
	}
	call := &inflightCall{done: make(chan struct{})}
	c.inflight[rowKey] = call
	c.inflightMu.Unlock()

	call.result = handler(rowKey)
	close(call.done)

	c.inflightMu.Lock()
	delete(c.inflight, rowKey)
	c.inflightMu.Unlock()

	return call.result
}

// candidateRowKeys returns the datasheet row keys to try for a runtime model,
// most specific first: the model verbatim, whatever the alias index maps it to,
// and — for Claude, whose runtime IDs carry region prefixes and date suffixes —
// the collapsed base name and its alias target.
func (c *modelCapabilitiesCache) candidateRowKeys(model string, provider schemas.ModelProvider) []string {
	candidates := []string{model}
	add := func(candidate string) {
		if candidate != "" && !slices.Contains(candidates, candidate) {
			candidates = append(candidates, candidate)
		}
	}

	add(c.aliasFor(CapabilityCacheKey(model, provider)))

	// normalizeClaudeModelName is Claude-specific — it strips everything before
	// the last ".", which mangles names like "gpt-4.1-2025-04-14" into "1".
	if strings.Contains(model, "claude") {
		if base := normalizeClaudeModelName(model); base != model {
			add(c.aliasFor(CapabilityCacheKey(base, provider)))
			add(base)
		}
	}

	return candidates
}

// SetCacheMissHandler registers a callback invoked on cache miss. The
// handler should read the datasheet row for the given key and return its
// capability record, or nil if there is none. Results — including nil — are
// cached, so a model absent from the datasheet is queried once per generation.
func SetCacheMissHandler(fn func(rowKey string) *schemas.ModelCapabilities) {
	c := getModelCapabilitiesCache()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cacheMissHandler = fn
}

// HasCacheMissHandler reports whether a lazy loader is registered. When
// none is, nothing can refill an evicted entry, so the sync path seeds the cache
// with every record instead of relying on demand loading.
func HasCacheMissHandler() bool {
	return getModelCapabilitiesCache().handler() != nil
}

// InvalidateCapabilities invalidates every cached record — hits and
// tombstones alike — in O(1). The datasheet sync calls this after applying a
// reload, which is the only thing that can change the underlying rows, so a
// single bump here is enough to pick up an updated sheet.
func InvalidateCapabilities() {
	getModelCapabilitiesCache().generation.Add(1)
}

// SetModelCapabilitiesAliases replaces the resident alias index mapping
// "<model>|<provider>" to the datasheet row key that carries its record. The
// sync path rebuilds this wholesale from the parsed feed.
func SetModelCapabilitiesAliases(alias map[string]string) {
	if alias == nil {
		alias = map[string]string{}
	}
	c := getModelCapabilitiesCache()
	c.aliasMu.Lock()
	c.alias = alias
	c.aliasMu.Unlock()
}

// SetCapabilitiesCacheCapacity resizes the cache. A capacity of 0 disables
// eviction, which is required when no miss handler is registered: nothing could
// refill an evicted entry, so a bounded cache would silently lose most of the
// datasheet.
func SetCapabilitiesCacheCapacity(capacity int) {
	c := getModelCapabilitiesCache()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.capacity = capacity
	for capacity > 0 && c.order.Len() > capacity {
		c.evict()
	}
}

// BulkSetModelCapabilities seeds many records at once, keyed by
// CapabilityCacheKey. Used when no miss handler is registered and the cache is
// therefore the only copy of the data.
func BulkSetModelCapabilities(entries map[string]*schemas.ModelCapabilities) {
	c := getModelCapabilitiesCache()
	c.bulkStore(entries, c.generation.Load())
}

// SetModelCapability inserts one record under the given cache key. The sync path
// uses BulkSetModelCapabilities; this is for single-entry callers and tests.
func SetModelCapability(cacheKey string, caps *schemas.ModelCapabilities) {
	c := getModelCapabilitiesCache()
	c.store(cacheKey, caps, c.generation.Load())
}

// DeleteModelCapability removes one cache key (test cleanup).
func DeleteModelCapability(cacheKey string) {
	getModelCapabilitiesCache().delete(cacheKey)
}

// GetModelCapabilities returns the record cached under the exact cache key, or
// nil on miss. Records live under the CapabilityCacheKey composite, so callers
// should almost always use CapabilitiesFor, which builds that key from the
// runtime (provider, model) and resolves aliases. This raw form is exported
// mainly for the sync path and tests.
func GetModelCapabilities(cacheKey string) *schemas.ModelCapabilities {
	c := getModelCapabilitiesCache()
	caps, _ := c.lookup(cacheKey, c.generation.Load())
	return caps
}

// CapabilityCacheKey builds the key a capability record is stored under:
// "<model>|<provider>". This mirrors the pricing store's (model, provider)
// keying, so a model's capabilities stay distinct per provider — e.g.
// supports_speed=true on Anthropic vs absent on Vertex for the same model.
func CapabilityCacheKey(model string, provider schemas.ModelProvider) string {
	return model + "|" + string(provider)
}

// CapabilitiesFor looks up the capability record for a (provider, model) pair.
//
// Records are keyed by the datasheet's own row key, but the runtime model can be
// a Bedrock dotted id ("us.anthropic.claude-...-v1:0"), a Vertex "@version" id,
// or a dated variant. The alias index resolves those to the row that carries the
// record; for Claude the collapsed base name is tried as well.
//
// A resolved record is also stored under the key that was asked for, so repeat
// lookups for the same runtime name hit directly. A lookup that resolves to
// nothing is tombstoned for the same reason.
//
// Returns nil on miss so callers can fall back to existing hardcoded helpers.
func CapabilitiesFor(provider schemas.ModelProvider, model string) *schemas.ModelCapabilities {
	if model == "" {
		return nil
	}

	c := getModelCapabilitiesCache()
	generation := c.generation.Load()
	key := CapabilityCacheKey(model, provider)

	// The requested key is authoritative: a tombstone here means a previous
	// lookup already walked every candidate and found nothing.
	if caps, found := c.lookup(key, generation); found {
		return caps
	}

	candidates := c.candidateRowKeys(model, provider)

	// Another runtime name may have already resolved the same row.
	for _, candidate := range candidates[1:] {
		if caps, found := c.lookup(CapabilityCacheKey(candidate, provider), generation); found && caps != nil {
			c.store(key, caps, generation)
			return caps
		}
	}

	if handler := c.handler(); handler != nil {
		for _, candidate := range candidates {
			caps := c.fetch(candidate, handler)
			if caps == nil {
				continue
			}
			c.store(CapabilityCacheKey(candidate, provider), caps, generation)
			c.store(key, caps, generation)
			return caps
		}
	}

	c.store(key, nil, generation)
	return nil
}

// GetMaxOutputTokensOrDefault returns the (provider, model)'s max_output_tokens
// from the capability record, or the provided default on miss. Claude falls back
// to the static table first, since Anthropic rejects a request without
// max_tokens and a cold or incomplete datasheet must not cap a model at the
// caller's generic default.
func GetMaxOutputTokensOrDefault(provider schemas.ModelProvider, model string, defaultValue int) int {
	if caps := CapabilitiesFor(provider, model); caps != nil && caps.MaxOutputTokens != nil {
		return *caps.MaxOutputTokens
	}
	if strings.Contains(model, "claude") {
		if m, ok := knownAnthropicMaxOutputTokens[normalizeClaudeModelName(model)]; ok {
			return m
		}
	}
	return defaultValue
}

// IsVertexMultiRegionOnlyModel reports whether the given model is flagged in the
// datasheet as only available on Google Vertex multi-region pool endpoints
// (aiplatform.{region}.rep.googleapis.com). Returns false when the flag is not set.
func IsVertexMultiRegionOnlyModel(model string) bool {
	if caps := CapabilitiesFor(schemas.Vertex, model); caps != nil && caps.IsVertexMultiRegionOnly != nil {
		return *caps.IsVertexMultiRegionOnly
	}
	return false
}

// normalizeClaudeModelName extracts the base Claude model name from
// provider-specific model ID formats.
//
// Examples:
//
//	"claude-sonnet-4-20250514"                     → "claude-sonnet-4"
//	"anthropic.claude-sonnet-4-20250514-v1:0"      → "claude-sonnet-4"
//	"us.anthropic.claude-sonnet-4-20250514-v1:0"   → "claude-sonnet-4"
//	"claude-3-5-sonnet-20241022"                   → "claude-3-5-sonnet"
func normalizeClaudeModelName(model string) string {
	// Strip region + provider prefixes (us.anthropic., anthropic., etc.)
	if idx := strings.LastIndex(model, "."); idx >= 0 {
		model = model[idx+1:]
	}
	// Strip "@version" alias marker (Vertex/Bedrock, e.g. "...-4-5@20251001")
	if idx := strings.Index(model, "@"); idx >= 0 {
		model = model[:idx]
	}
	// Strip Bedrock version suffix (":0", ":1", etc.) and the preceding "-v1"/"-v2"
	if idx := strings.Index(model, ":"); idx >= 0 {
		model = model[:idx]
		if len(model) >= 3 {
			suffix := model[len(model)-3:]
			if suffix == "-v1" || suffix == "-v2" {
				model = model[:len(model)-3]
			}
		}
	}
	// Strip "-v1", "-v2" even without colon (e.g., "anthropic.claude-opus-4-6-v1")
	if strings.HasSuffix(model, "-v1") || strings.HasSuffix(model, "-v2") {
		model = model[:len(model)-3]
	}
	// Strip date version suffix using schemas.BaseModelName
	return schemas.BaseModelName(model)
}
