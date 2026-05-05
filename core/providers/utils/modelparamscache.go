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
	MaxOutputTokens         *int
	IsVertexMultiRegionOnly *bool // true when model is only available on Vertex multi-region pool endpoints (rep.googleapis.com)

	// BifrostOverrides carries per-(model, provider) manipulation hints
	// sourced from the bifrost datasheet. Populated from the same fetch
	// path that supplies MaxOutputTokens. nil when the datasheet has no
	// bifrost-specific overrides for the model. Callers should fall back
	// to the existing hardcoded helpers when this is nil.
	BifrostOverrides *schemas.BifrostOverrides
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

func (c *modelParamsCache) Delete(model string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if elem, ok := c.items[model]; ok {
		c.order.Remove(elem)
		delete(c.items, model)
	}
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

// DeleteModelParams removes a model from the cache.
func DeleteModelParams(model string) {
	getModelParamsCache().Delete(model)
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

// IsVertexMultiRegionOnlyModel reports whether the given model is flagged in the
// datasheet as only available on Google Vertex multi-region pool endpoints
// (aiplatform.{region}.rep.googleapis.com). Returns false on cache miss or if
// the flag is not set. Looks up using "vertex_ai/" prefix since model-parameters
// are stored with provider-prefixed keys.
func IsVertexMultiRegionOnlyModel(model string) bool {
	params, ok := GetModelParams("vertex_ai/" + model)
	if !ok || params.IsVertexMultiRegionOnly == nil {
		return false
	}
	return *params.IsVertexMultiRegionOnly
}

// GetBifrostOverrides returns the cached bifrost overrides for a model key,
// or nil on cache miss / no overrides. Callers must pass the
// provider-prefixed model key as it appears in the datasheet:
//
//   - "claude-opus-4-7"                       — Anthropic native
//   - "anthropic.claude-opus-4-7-...-v1:0"    — Bedrock canonical
//   - "us.anthropic.claude-...-v1:0"          — Bedrock regional alias
//   - "azure/claude-opus-4-7"                 — Azure
//   - "vertex_ai/claude-opus-4-7"             — Vertex Anthropic
//   - "vertex_ai/gemini-2.5-pro"              — Vertex Gemini
//
// Returns nil so callers can fall back to existing hardcoded helpers when
// the datasheet has no entry yet (e.g. brand-new model not in the seed).
//
// Most call sites should prefer GetBifrostOverridesForRequest, which
// understands per-provider key conventions and tries the right keys
// automatically.
func GetBifrostOverrides(modelKey string) *schemas.BifrostOverrides {
	params, ok := GetModelParams(modelKey)
	if !ok || params.BifrostOverrides == nil {
		return nil
	}
	return params.BifrostOverrides
}

// GetBifrostOverridesForRequest looks up bifrost overrides for a (provider,
// model) pair, trying the conventional datasheet keys for that provider.
//
// Lookup precedence (first hit wins):
//
//  1. The model string verbatim. Already-prefixed inputs like
//     "anthropic.claude-opus-4-7-...-v1:0" or "us.anthropic.claude-..."
//     match the datasheet key directly.
//  2. Provider-conventional prefix, picked by family on Bedrock:
//     - Bedrock + Claude    → "anthropic.<model>"
//     - Bedrock + Llama     → "meta.<model>"
//     - Bedrock + Mistral   → "mistral.<model>"
//     - Bedrock + Nova/etc. → "amazon.<model>"
//     - Vertex              → "vertex_ai/<model>" (Anthropic-on-Vertex
//       uses bare keys which are already covered by (1))
//     - Azure               → "azure/<model>"
//
// Returns nil on miss so callers can fall back to existing hardcoded helpers.
func GetBifrostOverridesForRequest(provider schemas.ModelProvider, model string) *schemas.BifrostOverrides {
	if model == "" {
		return nil
	}

	// (1) Verbatim. Bedrock and Vertex callers typically pass already-
	// prefixed model strings ("anthropic.claude-...", "us.anthropic....",
	// "claude-...@20251101"). Anthropic native passes the bare name which
	// also matches the datasheet key.
	if ov := GetBifrostOverrides(model); ov != nil {
		return ov
	}

	// (2) Provider-conventional prefix. Useful when a caller passes a bare
	// model name and the datasheet has it stored under a provider prefix.
	for _, candidate := range candidateBifrostOverrideKeys(provider, model) {
		if ov := GetBifrostOverrides(candidate); ov != nil {
			return ov
		}
	}

	return nil
}

// candidateBifrostOverrideKeys returns the provider-conventional datasheet
// keys to try for a bare model name (after the verbatim lookup misses).
// Order matters — the most likely match comes first. Used by
// GetBifrostOverridesForRequest.
func candidateBifrostOverrideKeys(provider schemas.ModelProvider, model string) []string {
	switch provider {
	case schemas.Vertex:
		return []string{"vertex_ai/" + model}
	case schemas.Azure:
		return []string{"azure/" + model}
	case schemas.Bedrock:
		// Bedrock prefixes are family-stamped on the datasheet:
		//   anthropic.<...>-v1:0   for Claude
		//   meta.<...>-v1:0        for Llama
		//   mistral.<...>-v1:0     for Mistral / Codestral
		//   amazon.<...>           for Nova / Titan
		//   ai21.<...>             for Jamba
		//   cohere.<...>           for Command R / Embed
		//   stability.<...>        for Stable Diffusion
		switch {
		case schemas.IsAnthropicModel(model):
			return []string{"anthropic." + model}
		case schemas.IsLlamaModel(model):
			return []string{"meta." + model}
		case schemas.IsMistralModel(model):
			return []string{"mistral." + model}
		case schemas.IsNovaModel(model):
			return []string{"amazon." + model}
		}
		return nil
	}
	return nil
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
