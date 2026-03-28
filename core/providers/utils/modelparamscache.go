package utils

import (
	"container/list"
	"strings"
	"sync"

	"github.com/maximhq/bifrost/core/schemas"
)

const DefaultModelParamsCacheSize = 2048

// ModelParams holds cached parameters for a model.
// Add new fields here as more model-level parameters need caching.
type ModelParams struct {
	MaxOutputTokens *int
}

type modelParamsCacheEntry struct {
	model  string
	params ModelParams
}

// inflightCall represents an in-progress cache miss handler invocation.
// Multiple goroutines waiting for the same model share one call.
type inflightCall struct {
	done   chan struct{}
	result *ModelParams
}

type modelParamsCache struct {
	mu               sync.RWMutex
	capacity         int
	items            map[string]*list.Element
	order            *list.List // front = most recently inserted/updated
	cacheMissHandler func(model string) *ModelParams

	inflightMu sync.Mutex
	inflight   map[string]*inflightCall
}

var (
	globalModelParamsCache *modelParamsCache
	cacheOnce              sync.Once
)

// knownAnthropicMaxOutputTokens provides static fallback defaults for Claude models
// when both cache and DB miss handler return nothing. Only Anthropic requires max_tokens.
var knownAnthropicMaxOutputTokens = map[string]int{
	"claude-opus-4-6":   128000,
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

func newModelParamsCache(capacity int) *modelParamsCache {
	return &modelParamsCache{
		capacity: capacity,
		items:    make(map[string]*list.Element, capacity),
		order:    list.New(),
		inflight: make(map[string]*inflightCall),
	}
}

func getModelParamsCache() *modelParamsCache {
	cacheOnce.Do(func() {
		globalModelParamsCache = newModelParamsCache(DefaultModelParamsCacheSize)
	})
	return globalModelParamsCache
}

func (c *modelParamsCache) Get(model string) (ModelParams, bool) {
	c.mu.Lock()
	elem, ok := c.items[model]
	if ok {
		c.order.MoveToFront(elem)
		params := elem.Value.(*modelParamsCacheEntry).params
		c.mu.Unlock()
		return params, true
	}
	handler := c.cacheMissHandler
	c.mu.Unlock()

	if handler == nil {
		return ModelParams{}, false
	}

	// Deduplicate concurrent miss handler calls for the same model.
	c.inflightMu.Lock()
	if call, ok := c.inflight[model]; ok {
		c.inflightMu.Unlock()
		<-call.done
		if call.result == nil {
			return ModelParams{}, false
		}
		return *call.result, true
	}
	call := &inflightCall{done: make(chan struct{})}
	c.inflight[model] = call
	c.inflightMu.Unlock()

	result := handler(model)
	call.result = result
	close(call.done)

	c.inflightMu.Lock()
	delete(c.inflight, model)
	c.inflightMu.Unlock()

	if result == nil {
		return ModelParams{}, false
	}
	c.Set(model, *result)
	return *result, true
}

func (c *modelParamsCache) Set(model string, params ModelParams) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if elem, ok := c.items[model]; ok {
		elem.Value.(*modelParamsCacheEntry).params = params
		c.order.MoveToFront(elem)
		return
	}

	if c.order.Len() >= c.capacity {
		c.evict()
	}

	entry := &modelParamsCacheEntry{model: model, params: params}
	elem := c.order.PushFront(entry)
	c.items[model] = elem
}

func (c *modelParamsCache) BulkSet(entries map[string]ModelParams) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for model, params := range entries {
		if elem, ok := c.items[model]; ok {
			elem.Value.(*modelParamsCacheEntry).params = params
			c.order.MoveToFront(elem)
			continue
		}

		if c.order.Len() >= c.capacity {
			c.evict()
		}

		entry := &modelParamsCacheEntry{model: model, params: params}
		elem := c.order.PushFront(entry)
		c.items[model] = elem
	}
}

func (c *modelParamsCache) evict() {
	tail := c.order.Back()
	if tail == nil {
		return
	}
	c.order.Remove(tail)
	delete(c.items, tail.Value.(*modelParamsCacheEntry).model)
}

// GetModelParams returns the cached parameters for a model.
// On cache miss, calls the registered miss handler (if any) to load from DB.
func GetModelParams(model string) (ModelParams, bool) {
	return getModelParamsCache().Get(model)
}

// SetModelParams sets the parameters for a model in the cache.
func SetModelParams(model string, params ModelParams) {
	getModelParamsCache().Set(model, params)
}

// BulkSetModelParams sets parameters for multiple models at once.
func BulkSetModelParams(entries map[string]ModelParams) {
	getModelParamsCache().BulkSet(entries)
}

// SetCacheMissHandler registers a callback invoked on cache miss.
// The handler should query the DB for the model's parameters and return them,
// or nil if not found. The result is automatically cached.
func SetCacheMissHandler(fn func(model string) *ModelParams) {
	c := getModelParamsCache()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cacheMissHandler = fn
}

// GetMaxOutputTokens returns the cached max_output_tokens for a model.
// Returns 0, false on cache miss or if max_output_tokens is not set.
func GetMaxOutputTokens(model string) (int, bool) {
	params, ok := GetModelParams(model)
	if !ok || params.MaxOutputTokens == nil {
		return 0, false
	}
	return *params.MaxOutputTokens, true
}

// GetMaxOutputTokensOrDefault returns the cached max_output_tokens for a model,
// or the provided default value on cache miss. For Claude models, falls back to
// known static defaults before using the caller's default.
func GetMaxOutputTokensOrDefault(model string, defaultValue int) int {
	if m, ok := GetMaxOutputTokens(model); ok {
		return m
	}
	if strings.Contains(model, "claude") {
		base := normalizeClaudeModelName(model)
		if base != model {
			if m, ok := GetMaxOutputTokens(base); ok {
				return m
			}
		}
		if m, ok := knownAnthropicMaxOutputTokens[base]; ok {
			return m
		}
	}
	return defaultValue
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
