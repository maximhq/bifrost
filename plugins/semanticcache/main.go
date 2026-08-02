// Package semanticcache provides semantic caching integration for Bifrost plugin.
// This plugin caches responses using both direct hash matching (xxhash) and semantic similarity search (embeddings).
// It supports configurable caching behavior via the VectorStore abstraction, with TTL management and streaming response handling.
package semanticcache

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/vectorstore"
)

// Config contains configuration for the semantic cache plugin.
// The VectorStore abstraction handles the underlying storage implementation and its defaults.
// Only specify values you want to override from the semantic cache defaults.
//
// Modes:
//   - Semantic mode: set Provider + EmbeddingModel + Dimension > 0. Both direct
//     hash matching and embedding-based similarity search are enabled.
//   - Direct-only mode: set Provider="" and Dimension=1. The plugin disables
//     semantic search entirely; cache lookups go through the deterministic
//     direct hash path. Dimension=1 keeps stores that require a vector happy.
type Config struct {
	// Embedding Model settings - REQUIRED for semantic caching
	Provider       schemas.ModelProvider `json:"provider"`
	EmbeddingModel string                `json:"embedding_model,omitempty"` // Model to use for generating embeddings (optional)

	// Plugin behavior settings
	TTL                  time.Duration `json:"ttl,omitempty"`                    // Time-to-live for cached responses (default: 5min)
	Threshold            float64       `json:"threshold,omitempty"`              // Cosine similarity threshold for semantic matching (0 = unset → default 0.8)
	VectorStoreNamespace string        `json:"vector_store_namespace,omitempty"` // Namespace for vector store (optional)
	Dimension            int           `json:"dimension"`                        // Dimension for vector store (must be > 0 when Provider is set; use 1 for direct-only mode)

	// Advanced caching behavior
	DefaultCacheKey              string `json:"default_cache_key,omitempty"`              // Default cache key used when no per-request key is provided (optional, caching is disabled when empty and no per-request key is set)
	ConversationHistoryThreshold int    `json:"conversation_history_threshold,omitempty"` // Skip caching for requests with more than this number of messages in the conversation history (default: 3)
	CacheByModel                 *bool  `json:"cache_by_model,omitempty"`                 // Include model in cache key (default: true)
	CacheByProvider              *bool  `json:"cache_by_provider,omitempty"`              // Include provider in cache key (default: true)
	ExcludeSystemPrompt          *bool  `json:"exclude_system_prompt,omitempty"`          // Exclude system prompt in cache key (default: false)
}

// UnmarshalJSON implements custom JSON unmarshaling for Config so TTL accepts
// either a duration string ("1m", "1h") or a JSON number (seconds). All other
// fields decode through the default path via a type alias, so adding a new
// field on Config does not require touching this method.
func (c *Config) UnmarshalJSON(data []byte) error {
	// alias suppresses Config's UnmarshalJSON to avoid infinite recursion.
	// The outer TTL (json.RawMessage) shadows alias.TTL because the json
	// package picks the shallower field on a name conflict.
	type alias Config
	aux := &struct {
		TTL json.RawMessage `json:"ttl,omitempty"`
		*alias
	}{alias: (*alias)(c)}
	if err := json.Unmarshal(data, aux); err != nil {
		return fmt.Errorf("failed to unmarshal config: %w", err)
	}

	if len(aux.TTL) == 0 || string(aux.TTL) == "null" {
		return nil
	}

	// Try string first ("1m"); fall back to a JSON number (seconds).
	var s string
	if err := json.Unmarshal(aux.TTL, &s); err == nil {
		d, err := time.ParseDuration(s)
		if err != nil {
			return fmt.Errorf("failed to parse TTL duration string '%s': %w", s, err)
		}
		c.TTL = d
	} else {
		var seconds float64
		if err := json.Unmarshal(aux.TTL, &seconds); err != nil {
			return fmt.Errorf("unsupported TTL value: %s", string(aux.TTL))
		}
		c.TTL = time.Duration(seconds * float64(time.Second))
	}
	if c.TTL < 0 {
		return fmt.Errorf("TTL must be non-negative, got %v", c.TTL)
	}
	return nil
}

// StreamChunk is one chunk from a streaming response, retained until the
// stream completes so it can be persisted as part of the cache entry.
type StreamChunk struct {
	// Timestamp records when this chunk arrived at PostLLMHook. Used by the
	// reaper to drop accumulators stuck without a final chunk.
	Timestamp time.Time
	// Response is the chunk payload as delivered by the provider.
	Response *schemas.BifrostResponse
}

// StreamAccumulator collects the chunks of a single streaming response so
// they can be flushed as one cache entry on the final chunk.
type StreamAccumulator struct {
	// mu serializes Chunks/IsComplete updates across the per-chunk PostLLMHook
	// invocations and the periodic reaper.
	mu sync.Mutex
	// RequestID is the BifrostContext request ID this accumulator is keyed by.
	RequestID string
	// StorageID is the cache entry ID the accumulated stream will be written under.
	StorageID string
	// Chunks holds every chunk seen so far, in arrival order.
	Chunks []*StreamChunk
	// LastSeenAt records the arrival time of the most recent chunk. The reaper
	// uses this so a long-running stream isn't evicted mid-flight; first-chunk
	// time alone would falsely flag still-active streams as abandoned.
	LastSeenAt time.Time
	// IsComplete is set when the final chunk has been observed; further final
	// chunks are no-ops to keep flush idempotent.
	IsComplete bool
	// Embedding is the request embedding to attach to the cache entry, or nil
	// for direct-only writes.
	Embedding []float32
	// Metadata is the unified metadata captured at first-chunk time and reused
	// at flush. expires_at is locked in here, so TTL is fixed at first chunk.
	Metadata map[string]any
	// TTL is retained for symmetry with Metadata; the effective expiry is the
	// expires_at value already baked into Metadata.
	TTL time.Duration
}

// EmbeddingRequestExecutor invokes the embedding endpoint on the bifrost
// client. The plugin calls it on cache misses to compute the request
// embedding for semantic similarity search and storage. It mirrors the
// signature of bifrost.Client.EmbeddingRequest.
type EmbeddingRequestExecutor func(ctx *schemas.BifrostContext, req *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError)

// Plugin implements schemas.LLMPlugin for semantic caching. It serves cached
// responses via two complementary lookup paths: a direct O(1) hash match on
// (provider, model, cache_key, request_hash, params_hash) for exact replays,
// and an embedding-based similarity search for semantically related content.
// Streaming responses are accumulated chunk-by-chunk and stored as a single
// entry on the final chunk; TTL bookkeeping is per-entry via expires_at.
type Plugin struct {
	store                    vectorstore.VectorStore
	config                   *Config
	logger                   schemas.Logger
	embeddingRequestExecutor EmbeddingRequestExecutor
	// streamAccumulators maps request ID → its in-progress *StreamAccumulator.
	streamAccumulators sync.Map
	// cacheStates maps request ID → its *cacheState (see state.go) for the
	// span between PreLLMHook and PostLLMHook.
	cacheStates sync.Map
	// writersWg tracks short-lived per-request goroutines (the async cache
	// writes spawned in PostLLMHook). WaitForPendingOperations blocks on this
	// — tests use it to flush writes before asserting on the store.
	writersWg sync.WaitGroup
	// cleanupWg tracks the long-running background loops (stream + cacheState
	// reapers). Only Cleanup blocks on this, after closing stopCh.
	cleanupWg sync.WaitGroup
	// stopCh is closed by Cleanup to signal the background reaper loops to exit.
	stopCh chan struct{}
	// cleanupOnce guards Cleanup so close(stopCh) doesn't panic if the harness
	// invokes Cleanup more than once (e.g. plugin registered against multiple
	// interface caches).
	cleanupOnce sync.Once
}

// Plugin constants
const (
	PluginName                          string        = "semantic_cache"
	DefaultVectorStoreNamespace         string        = "BifrostSemanticCachePlugin"
	CacheConnectionTimeout              time.Duration = 5 * time.Second
	CreateNamespaceTimeout              time.Duration = 30 * time.Second
	CacheSetTimeout                     time.Duration = 30 * time.Second
	DefaultCacheTTL                     time.Duration = 5 * time.Minute
	DefaultCacheThreshold               float64       = 0.8
	DefaultConversationHistoryThreshold int           = 3
)

// SelectFields enumerates the properties projected back from the vector store
// on a cache hit. params_hash and from_bifrost_semantic_cache_plugin are
// filter-only (used in WHERE-style queries to narrow matches) and intentionally
// omitted from this projection — keep them defined in VectorStoreProperties
// below so the store creates the columns/indexes, but don't fetch them.
var SelectFields = []string{"response", "stream_chunks", "expires_at", "cache_key", "provider", "model"}

var VectorStoreProperties = map[string]vectorstore.VectorStoreProperties{
	"response": {
		DataType:    vectorstore.VectorStorePropertyTypeString,
		Description: "The response from the provider",
	},
	"stream_chunks": {
		DataType:    vectorstore.VectorStorePropertyTypeStringArray,
		Description: "The stream chunks from the provider",
	},
	"expires_at": {
		DataType:    vectorstore.VectorStorePropertyTypeInteger,
		Description: "The expiration time of the cache entry",
	},
	"cache_key": {
		DataType:    vectorstore.VectorStorePropertyTypeString,
		Description: "The cache key from the request",
	},
	"provider": {
		DataType:    vectorstore.VectorStorePropertyTypeString,
		Description: "The provider used for the request",
	},
	"model": {
		DataType:    vectorstore.VectorStorePropertyTypeString,
		Description: "The model used for the request",
	},
	"params_hash": {
		DataType:    vectorstore.VectorStorePropertyTypeString,
		Description: "The hash of the parameters used for the request",
	},
	"from_bifrost_semantic_cache_plugin": {
		DataType:    vectorstore.VectorStorePropertyTypeBoolean,
		Description: "Whether the cache entry was created by the BifrostSemanticCachePlugin",
	},
}

// Per-request context keys. Callers set these on BifrostContext before the
// request enters Bifrost; the plugin reads them in Pre/PostLLMHook. CacheKey
// (or Config.DefaultCacheKey) is the only one required for caching to engage.
const (
	CacheKey          schemas.BifrostContextKey = "semantic_cache-key"        // String. Required (or DefaultCacheKey) — bucket entries under a tenant/feature scope.
	CacheTTLKey       schemas.BifrostContextKey = "semantic_cache-ttl"        // time.Duration. Per-request override of Config.TTL.
	CacheThresholdKey schemas.BifrostContextKey = "semantic_cache-threshold"  // float64. Per-request override of the semantic similarity threshold.
	CacheTypeKey      schemas.BifrostContextKey = "semantic_cache-cache_type" // CacheType. Narrow lookup to a single path (direct or semantic).
	CacheNoStoreKey   schemas.BifrostContextKey = "semantic_cache-no_store"   // bool. Skip writing the response to cache (still served from cache on hit).
)

type CacheType string

const (
	CacheTypeDirect   CacheType = "direct"
	CacheTypeSemantic CacheType = "semantic"
)

// Init validates the configuration, creates the namespace in the underlying
// VectorStore, starts the background reaper goroutines, and returns a plugin
// ready to be wired into the Bifrost plugin pipeline.
//
// Note: Init mutates *config in place to fill in defaults — TTL, Threshold,
// CacheBy* — so the caller sees the resolved values after this returns.
func Init(ctx context.Context, config *Config, logger schemas.Logger, store vectorstore.VectorStore) (schemas.LLMPlugin, error) {
	if config == nil {
		return nil, fmt.Errorf("config is required")
	}
	if store == nil {
		return nil, fmt.Errorf("store is required")
	}
	if config.Dimension < 0 {
		return nil, fmt.Errorf("dimension must be non-negative, got %d", config.Dimension)
	}
	if config.Provider != "" && config.Dimension <= 0 {
		return nil, fmt.Errorf("dimension must be > 0 when provider is set (got dimension=%d, provider=%q)", config.Dimension, config.Provider)
	}
	// Set plugin-specific defaults
	if config.VectorStoreNamespace == "" {
		logger.Debug("Vector store namespace is not set, using default of %s", DefaultVectorStoreNamespace)
		config.VectorStoreNamespace = DefaultVectorStoreNamespace
	}
	if config.TTL == 0 {
		logger.Debug("TTL is not set, using default of %v", DefaultCacheTTL)
		config.TTL = DefaultCacheTTL
	}
	if config.Threshold == 0 {
		logger.Debug("Threshold is not set, using default of %v", DefaultCacheThreshold)
		config.Threshold = DefaultCacheThreshold
	}
	if config.ConversationHistoryThreshold == 0 {
		logger.Debug("Conversation history threshold is not set, using default of %d", DefaultConversationHistoryThreshold)
		config.ConversationHistoryThreshold = DefaultConversationHistoryThreshold
	}

	// Set cache behavior defaults
	if config.CacheByModel == nil {
		logger.Debug("CacheByModel is not set, defaulting to true")
		config.CacheByModel = new(true)
	}
	if config.CacheByProvider == nil {
		logger.Debug("CacheByProvider is not set, defaulting to true")
		config.CacheByProvider = new(true)
	}

	plugin := &Plugin{
		store:  store,
		config: config,
		logger: logger,
		stopCh: make(chan struct{}),
	}

	if config.Provider == "" && config.Dimension == 1 {
		logger.Info("Starting in direct-only mode (dimension=1, no embedding provider)")
	} else if config.Provider == "" {
		logger.Warn("Incomplete semantic mode config: missing provider, falling back to direct search only")
	}

	createCtx, cancel := context.WithTimeout(ctx, CreateNamespaceTimeout)
	defer cancel()
	if err := store.CreateNamespace(createCtx, config.VectorStoreNamespace, config.Dimension, VectorStoreProperties); err != nil {
		return nil, fmt.Errorf("failed to create namespace for semantic cache: %w", err)
	}

	plugin.cleanupWg.Add(1)
	go plugin.runStreamCleanupLoop()

	plugin.cleanupWg.Add(1)
	go plugin.runCacheStateCleanupLoop()

	return plugin, nil
}

// GetName returns the canonical name used for plugin identification and logging.
func (plugin *Plugin) GetName() string {
	return PluginName
}

// HTTPTransportPreHook is not used by the semantic cache plugin.
func (plugin *Plugin) HTTPTransportPreHook(ctx *schemas.BifrostContext, req *schemas.HTTPRequest) (*schemas.HTTPResponse, error) {
	return nil, nil
}

// HTTPTransportPostHook is not used by the semantic cache plugin.
func (plugin *Plugin) HTTPTransportPostHook(ctx *schemas.BifrostContext, req *schemas.HTTPRequest, resp *schemas.HTTPResponse) error {
	return nil
}

// HTTPTransportStreamChunkHook passes streaming chunks through unchanged.
func (plugin *Plugin) HTTPTransportStreamChunkHook(ctx *schemas.BifrostContext, req *schemas.HTTPRequest, chunk *schemas.BifrostStreamChunk) (*schemas.BifrostStreamChunk, error) {
	return chunk, nil
}

// PreRequestHook implements schemas.LLMPlugin (no-op — required for plugin indexing).
func (plugin *Plugin) PreRequestHook(_ *schemas.BifrostContext, _ *schemas.BifrostRequest) error {
	return nil
}

// PreLLMHook performs the cache lookup before the request reaches the
// provider. It runs the direct hash path first (cheapest), falls back to
// semantic similarity search when configured, and short-circuits the
// pipeline with a cached response on hit. On miss, it leaves per-request
// state on the plugin keyed by request ID for PostLLMHook to consume when
// the upstream response arrives.
func (plugin *Plugin) PreLLMHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	cacheKey, ok := plugin.resolveCacheKey(ctx)
	if !ok {
		return req, nil, nil
	}

	// Without a request ID we have nowhere to anchor per-request state. The
	// framework always stamps this before plugin hooks run; direct callers
	// (tests, custom integrations) must set it too.
	requestID, ok := ctx.Value(schemas.BifrostContextKeyRequestID).(string)
	if !ok || requestID == "" {
		return req, nil, nil
	}

	if !isSemanticCacheSupportedRequestType(req.RequestType) {
		return req, nil, nil
	}

	// Create state up front so a reused/retried request ID never inherits stale fields.
	state := plugin.createCacheState(requestID)

	if plugin.isConversationHistoryThresholdExceeded(state, req) {
		plugin.clearCacheState(requestID)
		return req, nil, nil
	}

	performDirectSearch, performSemanticSearch := plugin.resolveCacheTypes(ctx)

	// If neither search path can produce a lookup in the current plugin
	// configuration, skip caching entirely (no read, no write). Concretely:
	//   - x-bf-cache-type=semantic against a direct-only plugin (Provider="",
	//     Dimension=1) — generateEmbedding would fail with "provider is
	//     required", PostLLMHook would still write an orphan entry under a
	//     random request UUID that no future read can find.
	//   - x-bf-cache-type=direct against a misconfigured semantic-only plugin
	//     where direct search is disabled.
	//   - An unknown cache-type header value (resolveCacheTypes returns false
	//     for both paths).
	// The embedding executor alone isn't a sufficient gate — the framework
	// wires it on every plugin, but the plugin's config decides whether
	// semantic search is actually viable.
	canDoSemanticSearch := plugin.embeddingRequestExecutor != nil &&
		plugin.config.Provider != "" &&
		plugin.config.EmbeddingModel != "" &&
		plugin.config.Dimension > 1 &&
		req.EmbeddingRequest == nil &&
		req.TranscriptionRequest == nil
	if !performDirectSearch && (!performSemanticSearch || !canDoSemanticSearch) {
		plugin.clearCacheState(requestID)
		msg := "skipping cache: no search path available for this request (cache_type narrowed to a path that the current plugin configuration cannot serve)"
		plugin.logger.Warn(msg)
		ctx.Log(schemas.LogLevelWarn, msg)
		return req, nil, nil
	}

	// Compute metadata + paramsHash once and reuse across both search paths.
	metadata, err := plugin.buildRequestMetadataForCaching(state, req)
	if err != nil {
		plugin.clearCacheState(requestID)
		plugin.logger.Debug("metadata build failed, caching disabled for this request: %v", err)
		return req, nil, nil
	}
	paramsHash, err := hashMap(metadata)
	if err != nil {
		plugin.clearCacheState(requestID)
		plugin.logger.Debug("params hash failed, caching disabled for this request: %v", err)
		return req, nil, nil
	}
	state.ParamsHash = paramsHash

	if performDirectSearch {
		shortCircuit, err := plugin.performDirectSearch(ctx, state, req, cacheKey, metadata, paramsHash)
		if err != nil {
			msg := fmt.Sprintf("direct search failed (vector store unreachable?): %v", err)
			plugin.logger.Warn(msg)
			ctx.Log(schemas.LogLevelWarn, msg)
		} else if shortCircuit != nil {
			return req, shortCircuit, nil
		}
	}

	if performSemanticSearch {
		// Reuse canDoSemanticSearch so the default cache-type path (both flags
		// true) applies the same provider/model/dimension gate as the explicit
		// semantic-only path — otherwise a misconfigured plugin wastes one
		// generateEmbedding round-trip per request before failing downstream.
		if !canDoSemanticSearch {
			plugin.setPlaceholderVectorIfRequired(state)
		} else {
			shortCircuit, err := plugin.performSemanticSearch(ctx, state, req, cacheKey, paramsHash)
			if err != nil {
				// Embedding failures (rate-limit, auth, timeout) are
				// operationally important — surface at Warn and on the response.
				msg := fmt.Sprintf("semantic search skipped: %v", err)
				plugin.logger.Warn(msg)
				ctx.Log(schemas.LogLevelWarn, msg)
			} else if shortCircuit != nil {
				return req, shortCircuit, nil
			}
		}
	} else if !performSemanticSearch {
		// Direct-only mode. If the vector store requires vectors for every entry
		// (Qdrant, Pinecone) we write a zero vector. Note: this collapses all
		// direct-only entries onto the same point in vector space, so a
		// semantic search across cache types under the same cache_key/params
		// could surface them. params_hash filtering is the actual isolation.
		plugin.setPlaceholderVectorIfRequired(state)
	}

	return req, nil, nil
}

// resolveCacheKey returns the per-request cache key (or the configured default)
// and a bool indicating whether the caller should proceed with caching.
func (plugin *Plugin) resolveCacheKey(ctx *schemas.BifrostContext) (string, bool) {
	if cacheKey, ok := ctx.Value(CacheKey).(string); ok && cacheKey != "" {
		return cacheKey, true
	}
	if plugin.config.DefaultCacheKey != "" {
		return plugin.config.DefaultCacheKey, true
	}
	return "", false
}

// resolveCacheTypes returns whether direct and semantic search paths should
// run for this request. Defaults both to true; an explicit CacheTypeKey on
// the context narrows to just one.
func (plugin *Plugin) resolveCacheTypes(ctx *schemas.BifrostContext) (direct bool, semantic bool) {
	direct, semantic = true, true
	ctxVal := ctx.Value(CacheTypeKey)
	if ctxVal == nil {
		return
	}
	cacheTypeVal, ok := ctxVal.(CacheType)
	if !ok {
		msg := fmt.Sprintf("CacheTypeKey is not a CacheType (got %T), using all available cache types", ctxVal)
		plugin.logger.Warn(msg)
		ctx.Log(schemas.LogLevelWarn, msg)
		return
	}
	direct = cacheTypeVal == CacheTypeDirect
	semantic = cacheTypeVal == CacheTypeSemantic
	return
}

// setPlaceholderVectorIfRequired writes a fixed unit-vector placeholder when
// the store mandates a non-zero vector per entry (e.g. Pinecone rejects
// all-zero vectors). The first element is set to 1 so the vector satisfies
// that constraint. All direct-cache entries share the same placeholder, so
// isolation is provided entirely by params_hash filtering — not proximity.
func (plugin *Plugin) setPlaceholderVectorIfRequired(state *cacheState) {
	if !plugin.store.RequiresVectors() || plugin.config.Dimension <= 0 {
		return
	}
	vec := make([]float32, plugin.config.Dimension)
	vec[0] = 1.0
	state.Embeddings = vec
}

// PostLLMHook caches the upstream response keyed by the storageID resolved
// in PreLLMHook (deterministic directCacheID for direct hits, request UUID
// otherwise). The store write runs in a goroutine tracked by writersWg with
// its own background context + CacheSetTimeout, so client cancellation
// after the response is delivered doesn't drop the cache write. Returns the
// response unmodified — caching never alters the request flow.
func (plugin *Plugin) PostLLMHook(ctx *schemas.BifrostContext, res *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	if bifrostErr != nil {
		// We rely on errors always arriving as the final chunk for streams, so
		// we abort caching here without further bookkeeping. Any partial
		// accumulator from a prior chunk gets reaped by the periodic cleanup.
		return res, bifrostErr, nil
	}

	requestID, ok := ctx.Value(schemas.BifrostContextKeyRequestID).(string)
	if !ok {
		return res, nil, nil
	}

	extraFields := res.GetExtraFields()
	requestType := extraFields.RequestType
	cacheDebug := extraFields.CacheDebug

	// Final-chunk signaling for cache replays: stampCacheDebugForHit only
	// stamps CacheDebug.CacheHit=true on the LAST replay chunk (see search.go).
	// When we see that stamp, we set the stream-end indicator on the root ctx
	// synchronously — same goroutine as the rest of the post-hook chain.
	//
	// Why not set the indicator from the cache replay goroutine instead? It
	// races: the producer can advance to its next iteration (and SetValue)
	// while the receiver is still running PostLLMHooks for the previous
	// chunk, poisoning that chunk's IsFinalChunk read.
	if bifrost.IsStreamRequestType(requestType) && cacheDebug != nil && cacheDebug.CacheHit {
		ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
	}
	// Cache hit replay: cache_debug was already stamped in PreLLMHook by
	// stampCacheDebugForHit. There's nothing further to do here — no new
	// telemetry to stamp, no write to perform.
	if cacheDebug != nil && cacheDebug.CacheHit {
		plugin.clearCacheState(requestID)
		return res, nil, nil
	}

	cacheKey, ok := plugin.resolveCacheKey(ctx)
	if !ok {
		return res, nil, nil
	}
	provider := extraFields.Provider
	model := extraFields.OriginalModelRequested
	isStream := bifrost.IsStreamRequestType(requestType)
	isFinalChunk := bifrost.IsFinalChunk(ctx)

	state := plugin.getCacheState(requestID)
	if state == nil || state.ParamsHash == "" {
		// PreLLMHook bailed before computing the params hash (unsupported
		// request type, conversation-history threshold, metadata error,
		// no-search-path narrow, etc.). Without state we have no telemetry
		// to stamp and no entry to write.
		return res, nil, nil
	}

	// Free state once the request is fully observed. For non-streams that's
	// after this PostLLMHook returns; for streams, only on the final chunk.
	defer func() {
		if !isStream || isFinalChunk {
			plugin.clearCacheState(requestID)
		}
	}()

	// PreLLMHook short-circuited from cache (non-final stream chunks of a
	// replay land here). Telemetry is already stamped on the final chunk by
	// stampCacheDebugForHit; non-final chunks have no telemetry to add.
	// Without this guard non-final chunks would slip into addStreamingResponse
	// and trigger a duplicate write at the same directCacheID
	// (Weaviate 422 "id already exists").
	if state.ShortCircuited {
		return res, nil, nil
	}

	storageID, embedding, shouldStoreEmbeddings := plugin.resolveStorageIDAndEmbedding(ctx, state, requestID, requestType)

	// Stamp cache_debug telemetry FIRST so callers can observe that the
	// plugin ran a lookup, regardless of whether we then choose to skip
	// writing the entry (no-store header, large-payload modes, etc.).
	// Observability shouldn't depend on the write decision — that was
	// previously the case and made the cache layer invisible to callers
	// using no-store.
	plugin.stampCacheDebugForMiss(state, extraFields, storageID, isStream, isFinalChunk)

	// Now decide whether to actually write. Skipping the write still
	// leaves cache_debug stamped above.
	if plugin.shouldSkipCacheWrite(ctx) {
		return res, nil, nil
	}

	cacheTTL := plugin.resolveTTL(ctx)
	paramsHash := state.ParamsHash

	embeddingToStore := embedding
	if !shouldStoreEmbeddings {
		embeddingToStore = nil
	}

	// A store that requires vectors (Qdrant, Pinecone) rejects an empty-vector
	// upsert ("Expected some vectors"). If embedding generation failed upstream
	// (e.g. transient provider error, key resolution failure), skip the write
	// rather than emit a broken point. cache_debug was already stamped above,
	// so the miss stays observable.
	if plugin.store.RequiresVectors() && len(embeddingToStore) == 0 {
		// PostLLMHook runs once per streaming chunk; warn only once per request.
		if !isStream || isFinalChunk {
			plugin.logger.Warn("Skipping semantic cache write (namespace=%s, id=%s): store requires vectors but no embedding is available (embedding generation likely failed)", plugin.config.VectorStoreNamespace, storageID)
		}
		return res, nil, nil
	}

	plugin.writersWg.Add(1)
	go func() {
		defer plugin.writersWg.Done()
		cacheCtx, cancel := context.WithTimeout(context.Background(), CacheSetTimeout)
		defer cancel()

		unifiedMetadata := plugin.buildUnifiedMetadata(provider, model, paramsHash, cacheKey, cacheTTL)
		if isStream {
			if err := plugin.addStreamingResponse(cacheCtx, requestID, storageID, res, embeddingToStore, unifiedMetadata, cacheTTL, isFinalChunk); err != nil {
				plugin.logger.Warn("Failed to cache streaming response (namespace=%s, id=%s): %v. The cache_id stamped on the response will not resolve on subsequent lookups.", plugin.config.VectorStoreNamespace, storageID, err)
			}
		} else {
			if err := plugin.addNonStreamingResponse(cacheCtx, storageID, res, embeddingToStore, unifiedMetadata, cacheTTL); err != nil {
				plugin.logger.Warn("Failed to cache single response (namespace=%s, id=%s): %v. The cache_id stamped on the response will not resolve on subsequent lookups.", plugin.config.VectorStoreNamespace, storageID, err)
			}
		}
	}()

	return res, nil, nil
}

// shouldSkipCacheWrite returns true if the upstream response should NOT be
// written to the cache store. Telemetry (cache_debug) is stamped before this
// is consulted, so callers retain observability on misses even when no_store
// or large-payload modes are in effect. The cache-hit-replay case is handled
// separately as an early return in PostLLMHook because it must short-circuit
// before stamping (cache_debug for hits is already populated by
// stampCacheDebugForHit during PreLLMHook).
func (plugin *Plugin) shouldSkipCacheWrite(ctx *schemas.BifrostContext) bool {
	if isLargePayload, ok := ctx.Value(schemas.BifrostContextKeyLargePayloadMode).(bool); ok && isLargePayload {
		return true
	}
	if isLargeResponse, ok := ctx.Value(schemas.BifrostContextKeyLargeResponseMode).(bool); ok && isLargeResponse {
		return true
	}
	if noStore, ok := ctx.Value(CacheNoStoreKey).(bool); ok && noStore {
		return true
	}
	return false
}

// resolveStorageIDAndEmbedding picks the storage ID (deterministic directCacheID
// when direct search ran, else the request UUID) and resolves the embedding
// from per-request state. shouldStoreEmbeddings is false for explicit
// direct-only requests on stores that don't require vectors — those entries
// skip the embedding column entirely.
func (plugin *Plugin) resolveStorageIDAndEmbedding(ctx *schemas.BifrostContext, state *cacheState, requestID string, requestType schemas.RequestType) (storageID string, embedding []float32, shouldStoreEmbeddings bool) {
	storageID = requestID
	if state.DirectCacheID != "" {
		storageID = state.DirectCacheID
	}

	shouldStoreEmbeddings = true
	if cacheTypeVal, isCacheType := ctx.Value(CacheTypeKey).(CacheType); isCacheType && cacheTypeVal == CacheTypeDirect && !plugin.store.RequiresVectors() {
		shouldStoreEmbeddings = false
	}

	isEmbeddingOrTranscription := requestType == schemas.EmbeddingRequest || requestType == schemas.TranscriptionRequest
	needsEmbedding := shouldStoreEmbeddings && !isEmbeddingOrTranscription
	needsZeroVector := isEmbeddingOrTranscription && plugin.store.RequiresVectors()

	if needsEmbedding || needsZeroVector {
		// embedding may still be nil — fine for direct hash matching unless the
		// store requires vectors (in which case Add will reject downstream).
		embedding = state.Embeddings
	}
	return storageID, embedding, shouldStoreEmbeddings
}

// stampCacheDebugForMiss attaches cache miss telemetry to the response. It
// always sets CacheHit=false and CacheID to the storage ID where the entry
// will be written, so the caller can later invalidate via ClearCacheForCacheID.
// Embedding-cost fields (ProviderUsed/ModelUsed/InputTokens) are only stamped
// when semantic search actually ran. For streams, only the final chunk is
// stamped to avoid duplicating telemetry.
func (plugin *Plugin) stampCacheDebugForMiss(state *cacheState, extraFields *schemas.BifrostResponseExtraFields, storageID string, isStream, isFinalChunk bool) {
	if isStream && !isFinalChunk {
		return
	}
	if extraFields.CacheDebug == nil {
		extraFields.CacheDebug = &schemas.BifrostCacheDebug{}
	}
	cd := extraFields.CacheDebug
	cd.CacheHit = false
	cd.CacheID = bifrost.Ptr(storageID)
	if state.EmbeddingsInputTokens > 0 {
		inputTokens := state.EmbeddingsInputTokens
		cd.ProviderUsed = bifrost.Ptr(string(plugin.config.Provider))
		cd.ModelUsed = bifrost.Ptr(plugin.config.EmbeddingModel)
		cd.InputTokens = &inputTokens
	}
}

// resolveTTL returns the per-request TTL override if present, else the plugin
// default. A non-positive override (0 or negative) is treated as "use default"
// to mirror how Config.UnmarshalJSON + Init treat TTL=0 at construction time —
// otherwise a header of "0s" would yield expires_at=now and silently kill the
// cache write for the affected request, which is rarely what the caller wants.
func (plugin *Plugin) resolveTTL(ctx *schemas.BifrostContext) time.Duration {
	if v := ctx.Value(CacheTTLKey); v != nil {
		if ttl, ok := v.(time.Duration); ok {
			if ttl > 0 {
				return ttl
			}
			plugin.logger.Debug("ignoring non-positive per-request TTL override %v, falling back to plugin default", ttl)
		} else {
			plugin.logger.Warn("TTL is not a time.Duration, using default TTL")
		}
	}
	return plugin.config.TTL
}

// WaitForPendingOperations blocks until all pending cache operations (goroutines) complete.
// This is useful in tests to ensure cache entries are stored before checking for cache hits.
// It does NOT wait on background loops — those only exit on Cleanup.
func (plugin *Plugin) WaitForPendingOperations() {
	plugin.writersWg.Wait()
}

// Cleanup signals the background loops to stop and waits for in-flight cache
// writes to drain before returning. When CleanUpOnShutdown is true, it then
// deletes every entry tagged from_bifrost_semantic_cache_plugin and drops
// the namespace — useful for ephemeral test environments. The default is to
// leave entries in place so they can serve subsequent process restarts.
func (plugin *Plugin) Cleanup() error {
	plugin.cleanupOnce.Do(func() {
		close(plugin.stopCh)
		plugin.writersWg.Wait()
		plugin.cleanupWg.Wait()

		// Final sweep: the periodic reaper only fires once per streamCleanupInterval,
		// so any abandoned accumulator added in the window between the last tick
		// and stopCh is still in memory. This call evicts those before we return.
		plugin.cleanupOldStreamAccumulators()
	})
	return nil
}

// SetEmbeddingRequestExecutor wires up the function the plugin uses to call
// out to the embedding provider. Must be set before the plugin starts
// serving traffic; semantic search is silently skipped while it's nil.
func (plugin *Plugin) SetEmbeddingRequestExecutor(executor EmbeddingRequestExecutor) {
	plugin.embeddingRequestExecutor = executor
}

// ClearCacheForKey deletes every entry written under the given cache_key.
// Use this to invalidate a tenant or feature scope in bulk. Per-entry
// deletion is available via ClearCacheForCacheID.
func (plugin *Plugin) ClearCacheForKey(cacheKey string) error {
	queries := []vectorstore.Query{
		{
			Field:    "cache_key",
			Operator: vectorstore.QueryOperatorEqual,
			Value:    cacheKey,
		},
		{
			Field:    "from_bifrost_semantic_cache_plugin",
			Operator: vectorstore.QueryOperatorEqual,
			Value:    true,
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), CacheSetTimeout)
	defer cancel()
	results, err := plugin.store.DeleteAll(ctx, plugin.config.VectorStoreNamespace, queries)
	if err != nil {
		plugin.logger.Warn("Failed to delete cache entries for key '%s': %v", cacheKey, err)
		return err
	}

	for _, result := range results {
		if result.Status == vectorstore.DeleteStatusError {
			plugin.logger.Warn("Failed to delete cache entry for key %s: %s", result.ID, result.Error)
		}
	}

	plugin.logger.Debug("Deleted all cache entries for key %s", cacheKey)

	return nil
}

// ClearCacheForCacheID deletes a single cache entry by its storage ID. The
// caller obtains the ID from BifrostResponse.ExtraFields.CacheDebug.CacheID,
// which is stamped on both cache hits and cache misses — so the same handle
// works whether the request wrote the entry or read it.
func (plugin *Plugin) ClearCacheForCacheID(cacheID string) error {
	if cacheID == "" {
		return fmt.Errorf("cache ID is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), CacheSetTimeout)
	defer cancel()
	if err := plugin.store.Delete(ctx, plugin.config.VectorStoreNamespace, cacheID); err != nil {
		plugin.logger.Warn("Failed to delete cache entry %s: %v", cacheID, err)
		return err
	}
	plugin.logger.Debug("Deleted cache entry %s", cacheID)
	return nil
}
