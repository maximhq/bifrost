// Package semanticcache provides semantic caching integration for Bifrost plugin.
// This plugin caches responses using both direct hash matching (xxhash) and semantic similarity search (embeddings).
// It supports configurable caching behavior via the VectorStore abstraction, with TTL management and streaming response handling.
package semanticcache

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework"
	"github.com/maximhq/bifrost/framework/vectorstore"
)

// Config contains configuration for the semantic cache plugin.
// The VectorStore abstraction handles the underlying storage implementation and its defaults.
// Only specify values you want to override from the semantic cache defaults.
type Config struct {
	CacheKey          string `json:"cache_key"`           // Cache key for context lookup - REQUIRED
	CacheTTLKey       string `json:"cache_ttl_key"`       // Cache TTL key for context lookup (optional)
	CacheThresholdKey string `json:"cache_threshold_key"` // Cache threshold for context lookup (optional)

	// Embedding Model settings
	Provider       schemas.ModelProvider `json:"provider"`
	Keys           []schemas.Key         `json:"keys"`
	EmbeddingModel string                `json:"embedding_model,omitempty"` // Model to use for generating embeddings (optional)

	// Plugin behavior settings
	TTL       time.Duration `json:"ttl,omitempty"`       // Time-to-live for cached responses (default: 5min)
	Threshold float64       `json:"threshold,omitempty"` // Cosine similarity threshold for semantic matching (default: 0.8)

	// Advanced caching behavior
	CacheByModel    *bool `json:"cache_by_model,omitempty"`    // Include model in cache key (default: true)
	CacheByProvider *bool `json:"cache_by_provider,omitempty"` // Include provider in cache key (default: true)
}

// UnmarshalJSON implements custom JSON unmarshaling for semantic cache Config.
// It supports TTL parsing from both string durations ("1m", "1hr") and numeric seconds for configurable cache behavior.
func (c *Config) UnmarshalJSON(data []byte) error {
	// Define a temporary struct to avoid infinite recursion
	type TempConfig struct {
		CacheKey          string        `json:"cache_key"`
		CacheTTLKey       string        `json:"cache_ttl_key"`
		CacheThresholdKey string        `json:"cache_threshold_key"`
		Provider          string        `json:"provider"`
		Keys              []schemas.Key `json:"keys"`
		EmbeddingModel    string        `json:"embedding_model,omitempty"`
		TTL               interface{}   `json:"ttl,omitempty"`
		Threshold         float64       `json:"threshold,omitempty"`
		CacheByModel      *bool         `json:"cache_by_model,omitempty"`
		CacheByProvider   *bool         `json:"cache_by_provider,omitempty"`
	}

	var temp TempConfig
	if err := json.Unmarshal(data, &temp); err != nil {
		return fmt.Errorf("failed to unmarshal config: %w", err)
	}

	// Set simple fields
	c.CacheKey = temp.CacheKey
	c.CacheTTLKey = temp.CacheTTLKey
	c.CacheThresholdKey = temp.CacheThresholdKey
	c.Provider = schemas.ModelProvider(temp.Provider)
	c.Keys = temp.Keys
	c.EmbeddingModel = temp.EmbeddingModel
	c.CacheByModel = temp.CacheByModel
	c.CacheByProvider = temp.CacheByProvider
	c.Threshold = temp.Threshold

	// Handle TTL field with custom parsing for VectorStore-backed cache behavior
	if temp.TTL != nil {
		switch v := temp.TTL.(type) {
		case string:
			// Try parsing as duration string (e.g., "1m", "1hr") for semantic cache TTL
			duration, err := time.ParseDuration(v)
			if err != nil {
				return fmt.Errorf("failed to parse TTL duration string '%s': %w", v, err)
			}
			c.TTL = duration
		case int:
			// Handle integer seconds for semantic cache TTL
			c.TTL = time.Duration(v) * time.Second
		default:
			// Try converting to string and parsing as number for semantic cache TTL
			ttlStr := fmt.Sprintf("%v", v)
			if seconds, err := strconv.ParseFloat(ttlStr, 64); err == nil {
				c.TTL = time.Duration(seconds * float64(time.Second))
			} else {
				return fmt.Errorf("unsupported TTL type: %T (value: %v)", v, v)
			}
		}
	}

	return nil
}

// StreamChunk represents a single chunk from a streaming response
type StreamChunk struct {
	Timestamp    time.Time                // When chunk was received
	Response     *schemas.BifrostResponse // The actual response chunk
	FinishReason *string                  // If this is the final chunk
}

// StreamAccumulator manages accumulation of streaming chunks for caching
type StreamAccumulator struct {
	RequestID      string                 // The request ID
	Chunks         []*StreamChunk         // All chunks for this stream
	IsComplete     bool                   // Whether the stream is complete
	HasError       bool                   // Whether any chunk in the stream had an error
	FinalTimestamp time.Time              // When the stream completed
	Embedding      []float32              // Embedding for the original request
	Metadata       map[string]interface{} // Metadata for caching
	TTL            time.Duration          // TTL for this cache entry
	mu             sync.Mutex             // Protects chunk operations
}

// Plugin implements the schemas.Plugin interface for semantic caching.
// It caches responses using a two-tier approach: direct hash matching for exact requests
// and semantic similarity search for related content. The plugin supports configurable caching behavior
// via the VectorStore abstraction, including TTL management and streaming response handling.
//
// Fields:
//   - store: VectorStore instance for semantic cache operations
//   - config: Plugin configuration including semantic cache and caching settings
//   - logger: Logger instance for plugin operations
type Plugin struct {
	store              vectorstore.VectorStore
	config             Config
	logger             schemas.Logger
	client             *bifrost.Bifrost
	streamAccumulators sync.Map // Track stream accumulators by request ID
}

// Plugin constants
const (
	PluginName             string        = "semantic_cache"
	PluginLoggerPrefix     string        = "[Semantic Cache]"
	CacheConnectionTimeout time.Duration = 5 * time.Second
	CacheSetTimeout        time.Duration = 30 * time.Second
	DefaultCacheTTL        time.Duration = 5 * time.Minute
	DefaultCacheThreshold  float64       = 0.8
)

var SelectFields = []string{"request_hash", "response", "stream_chunks", "expires_at", "cache_key", "provider", "model"}

type PluginAccount struct {
	provider schemas.ModelProvider
	keys     []schemas.Key
}

func (pa *PluginAccount) GetConfiguredProviders() ([]schemas.ModelProvider, error) {
	return []schemas.ModelProvider{pa.provider}, nil
}

func (pa *PluginAccount) GetKeysForProvider(ctx *context.Context, providerKey schemas.ModelProvider) ([]schemas.Key, error) {
	return pa.keys, nil
}

func (pa *PluginAccount) GetConfigForProvider(providerKey schemas.ModelProvider) (*schemas.ProviderConfig, error) {
	return &schemas.ProviderConfig{
		NetworkConfig:            schemas.DefaultNetworkConfig,
		ConcurrencyAndBufferSize: schemas.DefaultConcurrencyAndBufferSize,
	}, nil
}

// Dependencies is a list of dependencies that the plugin requires.
var Dependencies []framework.FrameworkDependency = []framework.FrameworkDependency{framework.FrameworkDependencyVectorStore}

// Init creates a new semantic cache plugin instance with the provided configuration.
// It uses the VectorStore abstraction for cache operations and returns a configured plugin.
//
// The VectorStore handles the underlying storage implementation and its defaults.
// The plugin only sets defaults for its own behavior (TTL, cache key generation, etc.).
//
// Parameters:
//   - config: Semantic cache and plugin configuration (CacheKey is required)
//   - logger: Logger instance for the plugin
//   - store: VectorStore instance for cache operations
//
// Returns:
//   - schemas.Plugin: A configured semantic cache plugin instance
//   - error: Any error that occurred during plugin initialization
func Init(ctx context.Context, config Config, logger schemas.Logger, store vectorstore.VectorStore) (schemas.Plugin, error) {
	if config.CacheKey == "" {
		return nil, fmt.Errorf("cache key is required")
	}

	// Set plugin-specific defaults (not VectorStore defaults)
	if config.TTL == 0 {
		logger.Debug(PluginLoggerPrefix + " TTL is not set, using default of 5 minutes")
		config.TTL = DefaultCacheTTL
	}
	if config.Threshold == 0 {
		logger.Debug(PluginLoggerPrefix + " Threshold is not set, using default of " + strconv.FormatFloat(DefaultCacheThreshold, 'f', -1, 64))
		config.Threshold = DefaultCacheThreshold
	}

	// Set cache behavior defaults
	if config.CacheByModel == nil {
		config.CacheByModel = bifrost.Ptr(true)
	}
	if config.CacheByProvider == nil {
		config.CacheByProvider = bifrost.Ptr(true)
	}

	if config.Provider == "" || config.Keys == nil {
		return nil, fmt.Errorf("provider and keys are required for semantic cache")
	}

	bifrost, err := bifrost.Init(schemas.BifrostConfig{
		Logger: logger,
		Account: &PluginAccount{
			provider: config.Provider,
			keys:     config.Keys,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to initialize bifrost for semantic cache: %w", err)
	}

	return &Plugin{
		store:  store,
		config: config,
		logger: logger,
		client: bifrost,
	}, nil
}

// ContextKey is a custom type for context keys to prevent key collisions
type ContextKey string

const (
	requestIDKey        ContextKey = "semantic_cache_request_id"
	requestHashKey      ContextKey = "semantic_cache_request_hash"
	requestEmbeddingKey ContextKey = "semantic_cache_embedding"
	requestMetadataKey  ContextKey = "semantic_cache_metadata"
	requestModelKey     ContextKey = "semantic_cache_model"
	requestProviderKey  ContextKey = "semantic_cache_provider"
	isCacheHitKey       ContextKey = "semantic_cache_is_cache_hit"
	CacheHitTypeKey     ContextKey = "semantic_cache_cache_hit_type"
)

type CacheType string

const (
	CacheTypeDirect   CacheType = "direct"
	CacheTypeSemantic CacheType = "semantic"
)

// GetName returns the canonical name of the semantic cache plugin.
// This name is used for plugin identification and logging purposes.
//
// Returns:
//   - string: The plugin name for semantic cache
func (plugin *Plugin) GetName() string {
	return PluginName
}

// PreHook is called before a request is processed by Bifrost.
// It performs a two-stage cache lookup: first direct hash matching, then semantic similarity search.
// Uses UUID-based keys for entries stored in the VectorStore.
//
// Parameters:
//   - ctx: Pointer to the context.Context
//   - req: The incoming Bifrost request
//
// Returns:
//   - *schemas.BifrostRequest: The original request
//   - *schemas.BifrostResponse: Cached response if found, nil otherwise
//   - error: Any error that occurred during cache lookup
func (plugin *Plugin) PreHook(ctx *context.Context, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.PluginShortCircuit, error) {
	// Get the cache key from the context
	var cacheKey string
	var ok bool
	if ctx != nil {
		cacheKey, ok = (*ctx).Value(ContextKey(plugin.config.CacheKey)).(string)
		if !ok || cacheKey == "" {
			plugin.logger.Debug(PluginLoggerPrefix + " No cache key found in context key: " + plugin.config.CacheKey + ", continuing without caching")
			return req, nil, nil
		}
	} else {
		return req, nil, nil
	}

	// Generate UUID for this request
	requestID := uuid.New().String()

	// Store request ID, model, and provider in context for PostHook
	*ctx = context.WithValue(*ctx, requestIDKey, requestID)
	*ctx = context.WithValue(*ctx, requestModelKey, req.Model)
	*ctx = context.WithValue(*ctx, requestProviderKey, req.Provider)

	requestType, ok := (*ctx).Value(bifrost.BifrostContextKeyRequestType).(bifrost.RequestType)
	if !ok {
		return req, nil, nil
	}

	shortCircuit, err := plugin.performDirectSearch(ctx, req, requestType, cacheKey)
	if err != nil {
		plugin.logger.Warn(PluginLoggerPrefix + " Direct search failed: " + err.Error())
		// Don't return - continue to semantic search fallback
		shortCircuit = nil // Ensure we don't use an invalid shortCircuit
	}

	if shortCircuit != nil {
		return req, shortCircuit, nil
	}

	if req.Input.EmbeddingInput != nil || req.Input.TranscriptionInput != nil {
		plugin.logger.Debug(PluginLoggerPrefix + " Skipping semantic search for embedding/transcription input")
		return req, nil, nil
	}

	// Try semantic search as fallback
	shortCircuit, err = plugin.performSemanticSearch(ctx, req, requestType, cacheKey)
	if err != nil {
		return req, nil, nil
	}

	if shortCircuit != nil {
		return req, shortCircuit, nil
	}

	return req, nil, nil
}

// PostHook is called after a response is received from a provider.
// It caches responses in the VectorStore using UUID-based keys with unified metadata structure
// including provider, model, request hash, and TTL. Handles both single and streaming responses.
//
// The function performs the following operations:
// 1. Checks configurable caching behavior and skips caching for unsuccessful responses if configured
// 2. Retrieves the request hash and ID from the context (set during PreHook)
// 3. Marshals the response for storage
// 4. Stores the unified cache entry in the VectorStore asynchronously (non-blocking)
//
// The VectorStore Add operation runs in a separate goroutine to avoid blocking the response.
// The function gracefully handles errors and continues without caching if any step fails,
// ensuring that response processing is never interrupted by caching issues.
//
// Parameters:
//   - ctx: Pointer to the context.Context containing the request hash and ID
//   - res: The response from the provider to be cached
//   - bifrostErr: The error from the provider, if any (used for success determination)
//
// Returns:
//   - *schemas.BifrostResponse: The original response, unmodified
//   - *schemas.BifrostError: The original error, unmodified
//   - error: Any error that occurred during caching preparation (always nil as errors are handled gracefully)
func (plugin *Plugin) PostHook(ctx *context.Context, res *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	if bifrostErr != nil {
		return res, bifrostErr, nil
	}

	isCacheHit := (*ctx).Value(isCacheHitKey)
	if isCacheHit != nil {
		isCacheHitValue, ok := isCacheHit.(bool)
		if ok && isCacheHitValue {
			return res, nil, nil
		}
	}

	// Get the request type from context
	requestType, ok := (*ctx).Value(bifrost.BifrostContextKeyRequestType).(bifrost.RequestType)
	if !ok {
		return res, nil, nil
	}

	// Get the request ID from context
	requestID, ok := (*ctx).Value(requestIDKey).(string)
	if !ok {
		plugin.logger.Warn(PluginLoggerPrefix + " Request ID is not a string, continuing without caching")
		return res, nil, nil
	}

	// Get the hash from context
	hash, ok := (*ctx).Value(requestHashKey).(string)
	if !ok {
		plugin.logger.Warn(PluginLoggerPrefix + " Hash is not a string, continuing without caching")
		return res, nil, nil
	}

	// Get embedding from context if available (only generated during semantic search)
	var embedding []float32
	if requestType != bifrost.EmbeddingRequest && requestType != bifrost.TranscriptionRequest {
		embeddingValue := (*ctx).Value(requestEmbeddingKey)
		if embeddingValue != nil {
			embedding, ok = embeddingValue.([]float32)
			if !ok {
				plugin.logger.Warn(PluginLoggerPrefix + " Embedding is not a []float32, continuing without caching")
				return res, nil, nil
			}
		} else {
			// There was no embedding generated, so we can't cache this request
			plugin.logger.Debug(PluginLoggerPrefix + " No embedding generated, continuing without caching")
			return res, nil, nil
		}
	}

	// Get the provider from context
	provider, ok := (*ctx).Value(requestProviderKey).(schemas.ModelProvider)
	if !ok {
		plugin.logger.Warn(PluginLoggerPrefix + " Provider is not a schemas.ModelProvider, continuing without caching")
		return res, nil, nil
	}

	// Get the model from context
	model, ok := (*ctx).Value(requestModelKey).(string)
	if !ok {
		plugin.logger.Warn(PluginLoggerPrefix + " Model is not a string, continuing without caching")
		return res, nil, nil
	}

	cacheKey, ok := (*ctx).Value(ContextKey(plugin.config.CacheKey)).(string)
	if !ok {
		plugin.logger.Warn(PluginLoggerPrefix + " Cache key is not a string, continuing without caching")
		return res, nil, nil
	}

	cacheTTL := plugin.config.TTL

	if plugin.config.CacheTTLKey != "" {
		ttlValue := (*ctx).Value(ContextKey(plugin.config.CacheTTLKey))
		if ttlValue != nil {
			// Get the request TTL from the context
			ttl, ok := ttlValue.(time.Duration)
			if !ok {
				plugin.logger.Warn(PluginLoggerPrefix + " TTL is not a time.Duration, using default TTL")
			} else {
				cacheTTL = ttl
			}
		}
	}

	// Cache everything in a unified VectorEntry asynchronously to avoid blocking the response
	go func() {
		// Create a background context with timeout for the cache operation
		cacheCtx, cancel := context.WithTimeout(context.Background(), CacheSetTimeout)
		defer cancel()

		// Get metadata from context
		metadata, _ := (*ctx).Value(requestMetadataKey).(map[string]interface{})
		if metadata == nil {
			// Default to empty metadata if not provided in context (common for non-parameterized requests)
			metadata = make(map[string]interface{})
		}

		// Build unified metadata with provider, model, and all params
		unifiedMetadata := plugin.buildUnifiedMetadata(provider, model, metadata, hash, cacheKey, cacheTTL)

		// Handle streaming vs non-streaming responses
		if plugin.isStreamingRequest(requestType) {
			if err := plugin.addStreamingResponse(cacheCtx, requestID, res, bifrostErr, embedding, unifiedMetadata, cacheTTL); err != nil {
				plugin.logger.Warn(fmt.Sprintf("%s Failed to cache streaming response: %v", PluginLoggerPrefix, err))
			}
		} else {
			if err := plugin.addSingleResponse(cacheCtx, requestID, res, embedding, unifiedMetadata, cacheTTL); err != nil {
				plugin.logger.Warn(fmt.Sprintf("%s Failed to cache single response: %v", PluginLoggerPrefix, err))
			}
		}
	}()

	return res, nil, nil
}

// Cleanup performs cleanup operations for the semantic cache plugin.
// It removes all cached entries created by this plugin from the VectorStore.
// Identifies cache entries by the presence of semantic cache-specific fields (request_hash, cache_key).
//
// The function performs the following operations:
// 1. Retrieves all entries and filters client-side to identify cache entries
// 2. Deletes all matching cache entries from the VectorStore in batches
//
// This method should be called when shutting down the application to ensure
// proper resource cleanup.
//
// Returns:
//   - error: Any error that occurred during cleanup operations
func (plugin *Plugin) Cleanup() error {
	// Clean up all cache entries created by this plugin
	// We identify them by the presence of "request_hash" and "cache_key" fields which are unique to our cache entries
	ctx := context.Background()

	// Clean up old stream accumulators first
	plugin.cleanupOldStreamAccumulators()

	plugin.logger.Debug(PluginLoggerPrefix + " Starting cleanup of cache entries...")

	// Delete all cache entries created by this plugin
	queries := []vectorstore.Query{
		{
			Field:    "from_bifrost_semantic_cache_plugin",
			Operator: vectorstore.QueryOperatorEqual,
			Value:    true,
		},
	}

	results, err := plugin.store.DeleteAll(ctx, queries)
	if err != nil {
		plugin.logger.Warn(fmt.Sprintf("%s Failed to delete cache entries: %v", PluginLoggerPrefix, err))
		return err
	}

	for _, result := range results {
		if result.Status == vectorstore.DeleteStatusError {
			plugin.logger.Warn(fmt.Sprintf("%s Failed to delete cache entry: %s", PluginLoggerPrefix, result.Error))
		}
	}

	plugin.logger.Info(fmt.Sprintf("%s Cleanup completed - deleted all cache entries", PluginLoggerPrefix))

	plugin.client.Cleanup()

	return nil
}

// Public Methods for External Use

// ClearCacheForKey deletes cache entries for a specific cache key.
// Uses the unified VectorStore interface for deletion of all entries with the given cache key.
//
// Parameters:
//   - cacheKey: The specific cache key to delete
//
// Returns:
//   - error: Any error that occurred during cache key deletion
func (plugin *Plugin) ClearCacheForKey(cacheKey string) error {
	// Delete all entries with "cache_key" equal to the given cacheKey
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

	results, err := plugin.store.DeleteAll(context.Background(), queries)
	if err != nil {
		plugin.logger.Warn(fmt.Sprintf("%s Failed to delete cache entries for key '%s': %v", PluginLoggerPrefix, cacheKey, err))
		return err
	}

	for _, result := range results {
		if result.Status == vectorstore.DeleteStatusError {
			plugin.logger.Warn(fmt.Sprintf("%s Failed to delete cache entry for key %s: %s", PluginLoggerPrefix, result.ID, result.Error))
		}
	}

	plugin.logger.Debug(fmt.Sprintf("%s Deleted all cache entries for key %s", PluginLoggerPrefix, cacheKey))

	return nil
}

// ClearCacheForRequestID deletes cache entries for a specific request ID.
// Uses the unified VectorStore interface to delete the single entry by its UUID.
//
// Parameters:
//   - requestID: The UUID-based request ID to delete cache entries for
//
// Returns:
//   - error: Any error that occurred during cache key deletion
func (plugin *Plugin) ClearCacheForRequestID(requestID string) error {
	// With the unified VectorStore interface, we delete the single entry by its UUID
	if err := plugin.store.Delete(context.Background(), requestID); err != nil {
		plugin.logger.Warn(fmt.Sprintf("%s Failed to delete cache entry: %v", PluginLoggerPrefix, err))
		return err
	}

	plugin.logger.Debug(fmt.Sprintf("%s Deleted cache entry for key %s", PluginLoggerPrefix, requestID))

	return nil
}
