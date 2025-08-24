// Package semanticcache provides semantic caching integration for Bifrost plugin.
// This plugin caches request body hashes using xxhash and returns cached responses for identical requests.
// It supports configurable caching behavior via the VectorStore abstraction, including success-only caching and custom cache key generation.
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
	Prefix    string        `json:"prefix,omitempty"`    // Prefix for cache keys (optional)

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
		Prefix            string        `json:"prefix,omitempty"`
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
	c.Prefix = temp.Prefix
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
	ErrorDetails *schemas.BifrostError    // Error if any
}

// StreamAccumulator manages accumulation of streaming chunks for caching
type StreamAccumulator struct {
	RequestID      string                 // The request ID
	Chunks         []*StreamChunk         // All chunks for this stream
	IsComplete     bool                   // Whether the stream is complete
	FinalTimestamp time.Time              // When the stream completed
	Embedding      []float32              // Embedding for the original request
	Metadata       map[string]interface{} // Metadata for caching
	TTL            time.Duration          // TTL for this cache entry
	mu             sync.Mutex             // Protects chunk operations
}

// Plugin implements the schemas.Plugin interface for semantic caching.
// It caches responses based on xxhash of normalized requests and returns cached
// responses for identical requests. The plugin supports configurable caching behavior
// via the VectorStore abstraction, including success-only caching and custom cache key generation.
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
	DefaultKeyPrefix       string        = "semantic_cache"
)

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

	// Set plugin-specific defaults (not Redis defaults)
	if config.TTL == 0 {
		logger.Debug(PluginLoggerPrefix + " TTL is not set, using default of 5 minutes")
		config.TTL = DefaultCacheTTL
	}
	if config.Threshold == 0 {
		logger.Debug(PluginLoggerPrefix + " Threshold is not set, using default of " + strconv.FormatFloat(DefaultCacheThreshold, 'f', -1, 64))
		config.Threshold = DefaultCacheThreshold
	}
	if config.Prefix == "" {
		logger.Debug(PluginLoggerPrefix + " Prefix is not set, using default of " + DefaultKeyPrefix)
		config.Prefix = DefaultKeyPrefix
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
// It checks if a cached response exists for the request hash and returns it if found.
// Uses pattern-based lookup with new key format: {provider}-{model}-{reqid}-{suffix}
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

	shortCircuit, err := plugin.performDirectSearch(ctx, req, requestType)
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
	shortCircuit, err = plugin.performSemanticSearch(ctx, req, requestType)
	if err != nil {
		return req, nil, nil
	}

	if shortCircuit != nil {
		return req, shortCircuit, nil
	}

	return req, nil, nil
}

// PostHook is called after a response is received from a provider.
// It caches both the hash and response using the new key format: {provider}-{model}-{reqid}-{suffix}
// with optional filtering based on configurable caching behavior.
//
// The function performs the following operations:
// 1. Checks configurable caching behavior and skips caching for unsuccessful responses if configured
// 2. Retrieves the request hash and ID from the context (set during PreHook)
// 3. Marshals the response for storage
// 4. Stores both the hash and response in the VectorStore-backed cache asynchronously (non-blocking)
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
			// If the cache hit is true, we should cache direct only when the cache hit type is semantic
			cacheHitType, ok := (*ctx).Value(CacheHitTypeKey).(CacheType)
			if ok && cacheHitType == CacheTypeDirect {
				return res, nil, nil
			}
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
		embedding, ok = (*ctx).Value(requestEmbeddingKey).([]float32)
		if !ok {
			plugin.logger.Warn(PluginLoggerPrefix + " Embedding is not a []float32, continuing without caching")
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
			metadata = make(map[string]interface{})
		}

		// Build unified metadata with provider, model, and all params
		unifiedMetadata := plugin.buildUnifiedMetadata(provider, model, metadata, hash, cacheKey, cacheTTL)

		// Handle streaming vs non-streaming responses
		if plugin.isStreamingRequest(requestType) {
			if err := plugin.addStreamingResponse(cacheCtx, requestID, res, embedding, unifiedMetadata, cacheTTL); err != nil {
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
// It removes all cached entries with the configured prefix from the VectorStore-backed cache.
// Updated to handle the new key format: {provider}-{model}-{reqid}-{suffix}
//
// The function performs the following operations:
// 1. Retrieves all cache keys matching the configured prefix pattern
// 2. Deletes all matching cache entries from the VectorStore-backed cache
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

	plugin.logger.Info(PluginLoggerPrefix + " Starting cleanup of cache entries...")

	// Get all entries and filter client-side to avoid Weaviate stopwords issues
	// Empty string filters cause issues, so we'll get all entries and filter them ourselves
	var totalDeleted int
	var cursor *string

	for {
		// Get batch of all entries (no filters to avoid stopwords issues)
		results, newCursor, err := plugin.store.GetAll(ctx, nil, cursor, 100)
		if err != nil {
			plugin.logger.Warn(fmt.Sprintf("%s Failed to query entries for cleanup: %v", PluginLoggerPrefix, err))
			break
		}

		if len(results) == 0 {
			break
		}

		// Filter and extract IDs for deletion (only our cache entries)
		var idsToDelete []string
		for _, result := range results {
			if searchResult, ok := result.(vectorstore.SearchResult); ok {
				// Check if this is one of our cache entries by looking at properties
				if plugin.isCacheEntry(searchResult.Properties) {
					idsToDelete = append(idsToDelete, searchResult.ID)
				}
			}
		}

		// Delete this batch
		if len(idsToDelete) > 0 {
			err = plugin.store.Delete(ctx, idsToDelete)
			if err != nil {
				plugin.logger.Warn(fmt.Sprintf("%s Failed to delete cache entries: %v", PluginLoggerPrefix, err))
			} else {
				totalDeleted += len(idsToDelete)
				plugin.logger.Debug(fmt.Sprintf("%s Deleted %d cache entries", PluginLoggerPrefix, len(idsToDelete)))
			}
		}

		cursor = newCursor
		if cursor == nil {
			break
		}
	}

	if totalDeleted > 0 {
		plugin.logger.Info(fmt.Sprintf("%s Cleanup completed - deleted %d cache entries", PluginLoggerPrefix, totalDeleted))
	} else {
		plugin.logger.Info(PluginLoggerPrefix + " Cleanup completed - no cache entries found to delete")
	}

	plugin.client.Cleanup()

	return nil
}

// isCacheEntry checks if the given properties belong to a cache entry created by this plugin
func (plugin *Plugin) isCacheEntry(properties interface{}) bool {
	if properties == nil {
		return false
	}

	propsMap, ok := properties.(map[string]interface{})
	if !ok {
		return false
	}

	// Check for our cache-specific fields
	// We identify cache entries by the presence of request_hash or cache_key fields
	if requestHash, exists := propsMap["request_hash"]; exists {
		if requestHashStr, ok := requestHash.(string); ok && requestHashStr != "" {
			return true
		}
	}

	if cacheKey, exists := propsMap["cache_key"]; exists {
		if cacheKeyStr, ok := cacheKey.(string); ok && cacheKeyStr != "" {
			return true
		}
	}

	return false
}

// CleanupExpiredEntries removes expired cache entries in batches
func (plugin *Plugin) CleanupExpiredEntries() error {
	ctx := context.Background()
	plugin.logger.Info("[Semantic Cache] Starting cleanup of expired cache entries...")

	currentTime := time.Now().Unix()
	var totalDeleted int
	var cursor *string

	for {
		// Get batch of all entries
		results, newCursor, err := plugin.store.GetAll(ctx, nil, cursor, 100)
		if err != nil {
			plugin.logger.Warn(fmt.Sprintf("[Semantic Cache] Failed to query entries for TTL cleanup: %v", err))
			break
		}

		if len(results) == 0 {
			break
		}

		// Filter expired entries and collect their IDs
		var expiredIDs []string
		for _, result := range results {
			if searchResult, ok := result.(vectorstore.SearchResult); ok {
				if plugin.isExpiredEntry(searchResult.Properties, currentTime) {
					expiredIDs = append(expiredIDs, searchResult.ID)
				}
			}
		}

		// Delete expired entries
		if len(expiredIDs) > 0 {
			err = plugin.store.Delete(ctx, expiredIDs)
			if err != nil {
				plugin.logger.Warn(fmt.Sprintf("[Semantic Cache] Failed to delete expired entries: %v", err))
			} else {
				totalDeleted += len(expiredIDs)
				plugin.logger.Debug(fmt.Sprintf("[Semantic Cache] Deleted %d expired entries", len(expiredIDs)))
			}
		}

		cursor = newCursor
		if cursor == nil {
			break
		}
	}

	if totalDeleted > 0 {
		plugin.logger.Info(fmt.Sprintf("[Semantic Cache] TTL cleanup completed - deleted %d expired entries", totalDeleted))
	} else {
		plugin.logger.Debug("[Semantic Cache] TTL cleanup completed - no expired entries found")
	}

	return nil
}

// isExpiredEntry checks if a cache entry has expired based on its expires_at timestamp
func (plugin *Plugin) isExpiredEntry(properties interface{}, currentTime int64) bool {
	if properties == nil {
		return false
	}

	propsMap, ok := properties.(map[string]interface{})
	if !ok {
		return false
	}

	// Check if this is a cache entry (has cache_key or request_hash)
	if !plugin.isCacheEntry(properties) {
		return false
	}

	// Check expiration timestamp
	if expiresAtRaw, exists := propsMap["expires_at"]; exists {
		if expiresAt, ok := expiresAtRaw.(float64); ok {
			return int64(expiresAt) < currentTime
		}
	}

	// If no expires_at field, consider it non-expired (for backward compatibility)
	return false
}

// Public Methods for External Use

// ClearCacheForKey deletes cache entries for a specific request ID or pattern.
// With the new unified VectorStore interface, this is simplified.
//
// Parameters:
//   - key: The specific key or ID to delete
//
// Returns:
//   - error: Any error that occurred during cache key deletion
func (plugin *Plugin) ClearCacheForKey(key string) error {
	// With the new unified interface, we delete by specific ID
	if err := plugin.store.Delete(context.Background(), []string{key}); err != nil {
		plugin.logger.Warn(fmt.Sprintf("%s Failed to delete cache key '%s': %v", PluginLoggerPrefix, key, err))
		return err
	}

	plugin.logger.Debug(fmt.Sprintf("%s Deleted cache entry for key %s", PluginLoggerPrefix, key))
	return nil
}

// ClearCacheForRequestID deletes cache entries for a specific request ID.
// With the new unified VectorStore interface, this deletes the single unified entry.
//
// Parameters:
//   - req: The Bifrost request (unused in new implementation)
//   - requestID: The request ID to delete cache entries for
//
// Returns:
//   - error: Any error that occurred during cache key deletion
func (plugin *Plugin) ClearCacheForRequestID(req *schemas.BifrostRequest, requestID string) error {
	// With the new unified interface, we delete the single entry by its ID
	if err := plugin.ClearCacheForKey(requestID); err != nil {
		plugin.logger.Warn(PluginLoggerPrefix + " Failed to delete cache entry: " + err.Error())
		return err
	}

	return nil
}

// buildUnifiedMetadata constructs the unified metadata structure for VectorEntry
func (plugin *Plugin) buildUnifiedMetadata(provider schemas.ModelProvider, model string, params map[string]interface{}, requestHash string, cacheKey string, ttl time.Duration) map[string]interface{} {
	unifiedMetadata := make(map[string]interface{})

	// Top-level fields (outside params)
	unifiedMetadata["provider"] = string(provider)
	unifiedMetadata["model"] = model
	unifiedMetadata["request_hash"] = requestHash
	unifiedMetadata["cache_key"] = cacheKey

	// Calculate expiration timestamp (current time + TTL)
	expiresAt := time.Now().Add(ttl).Unix()
	unifiedMetadata["expires_at"] = expiresAt

	// Individual param fields will be stored as params_* by the vectorstore
	// We pass the params map to the vectorstore, and it handles the individual field storage
	if len(params) > 0 {
		unifiedMetadata["params"] = params
	}

	return unifiedMetadata
}

// addSingleResponse stores a single (non-streaming) response in unified VectorEntry format
func (plugin *Plugin) addSingleResponse(ctx context.Context, responseID string, res *schemas.BifrostResponse, embedding []float32, metadata map[string]interface{}, ttl time.Duration) error {
	// Marshal response as string
	responseData, err := json.Marshal(res)
	if err != nil {
		return fmt.Errorf("failed to marshal response: %w", err)
	}

	// Add response field to metadata
	metadata["response"] = string(responseData)

	// Store unified entry using new VectorStore interface
	if err := plugin.store.Add(ctx, responseID, embedding, metadata); err != nil {
		return fmt.Errorf("failed to store unified cache entry: %w", err)
	}

	plugin.logger.Debug(fmt.Sprintf("%s Successfully cached single response with ID: %s", PluginLoggerPrefix, responseID))
	return nil
}

// addStreamingResponse handles streaming response storage by accumulating chunks
func (plugin *Plugin) addStreamingResponse(ctx context.Context, responseID string, res *schemas.BifrostResponse, embedding []float32, metadata map[string]interface{}, ttl time.Duration) error {
	// Create accumulator if it doesn't exist
	accumulator := plugin.getOrCreateStreamAccumulator(responseID, embedding, metadata, ttl)

	// Create chunk from current response
	chunk := &StreamChunk{
		Timestamp: time.Now(),
		Response:  res,
	}

	// Check for finish reason or errors to mark as final chunk
	if res != nil && len(res.Choices) > 0 {
		choice := res.Choices[0]
		if choice.BifrostStreamResponseChoice != nil {
			chunk.FinishReason = choice.FinishReason
		}
	}

	// Add chunk to accumulator
	if err := plugin.addStreamChunk(responseID, chunk); err != nil {
		return fmt.Errorf("failed to add stream chunk: %w", err)
	}

	// If this is the final chunk, process accumulated chunks synchronously
	// This ensures the complete stream is cached before the request finishes
	if accumulator.IsComplete {
		if processErr := plugin.processAccumulatedStream(ctx, responseID); processErr != nil {
			plugin.logger.Warn(fmt.Sprintf("%s Failed to process accumulated stream for request %s: %v", PluginLoggerPrefix, responseID, processErr))
		}
	}

	return nil
}

// ========= Streaming State Management Methods =========

// createStreamAccumulator creates a new stream accumulator for a request
func (plugin *Plugin) createStreamAccumulator(requestID string, embedding []float32, metadata map[string]interface{}, ttl time.Duration) *StreamAccumulator {
	accumulator := &StreamAccumulator{
		RequestID:  requestID,
		Chunks:     make([]*StreamChunk, 0),
		IsComplete: false,
		Embedding:  embedding,
		Metadata:   metadata,
		TTL:        ttl,
	}

	plugin.streamAccumulators.Store(requestID, accumulator)
	return accumulator
}

// getOrCreateStreamAccumulator gets or creates a stream accumulator for a request
func (plugin *Plugin) getOrCreateStreamAccumulator(requestID string, embedding []float32, metadata map[string]interface{}, ttl time.Duration) *StreamAccumulator {
	if accumulator, exists := plugin.streamAccumulators.Load(requestID); exists {
		return accumulator.(*StreamAccumulator)
	}

	// Create new accumulator if it doesn't exist
	return plugin.createStreamAccumulator(requestID, embedding, metadata, ttl)
}

// addStreamChunk adds a chunk to the stream accumulator
func (plugin *Plugin) addStreamChunk(requestID string, chunk *StreamChunk) error {
	// Get accumulator (should exist if properly initialized)
	accumulatorInterface, exists := plugin.streamAccumulators.Load(requestID)
	if !exists {
		return fmt.Errorf("stream accumulator not found for request %s", requestID)
	}

	accumulator := accumulatorInterface.(*StreamAccumulator)
	accumulator.mu.Lock()
	defer accumulator.mu.Unlock()

	// Add chunk to the list (chunks arrive in order)
	accumulator.Chunks = append(accumulator.Chunks, chunk)

	// Check if this is the final chunk
	if chunk.FinishReason != nil || chunk.ErrorDetails != nil {
		accumulator.IsComplete = true
		accumulator.FinalTimestamp = chunk.Timestamp
	}

	return nil
}

// processAccumulatedStream processes all accumulated chunks and caches the complete stream
func (plugin *Plugin) processAccumulatedStream(ctx context.Context, requestID string) error {
	accumulatorInterface, exists := plugin.streamAccumulators.Load(requestID)
	if !exists {
		return fmt.Errorf("stream accumulator not found for request %s", requestID)
	}

	accumulator := accumulatorInterface.(*StreamAccumulator)
	accumulator.mu.Lock()
	defer accumulator.mu.Unlock()

	// Ensure cleanup happens
	defer plugin.cleanupStreamAccumulator(requestID)

	// Build complete stream_responses from accumulated chunks
	var streamResponses []string
	for _, chunk := range accumulator.Chunks {
		if chunk.Response != nil {
			chunkData, err := json.Marshal(chunk.Response)
			if err != nil {
				plugin.logger.Warn(fmt.Sprintf("%s Failed to marshal stream chunk: %v", PluginLoggerPrefix, err))
				continue
			}
			streamResponses = append(streamResponses, string(chunkData))
		}
	}

	// Add stream_responses to metadata
	finalMetadata := make(map[string]interface{})
	for k, v := range accumulator.Metadata {
		finalMetadata[k] = v
	}
	finalMetadata["stream_responses"] = streamResponses

	// Store complete unified entry using original requestID
	if err := plugin.store.Add(ctx, requestID, accumulator.Embedding, finalMetadata); err != nil {
		return fmt.Errorf("failed to store complete streaming cache entry: %w", err)
	}

	plugin.logger.Debug(fmt.Sprintf("%s Successfully cached complete stream with %d chunks, ID: %s", PluginLoggerPrefix, len(streamResponses), requestID))
	return nil
}

// cleanupStreamAccumulator removes the stream accumulator for a request
func (plugin *Plugin) cleanupStreamAccumulator(requestID string) {
	plugin.streamAccumulators.Delete(requestID)
}

// cleanupOldStreamAccumulators removes stream accumulators older than 5 minutes
func (plugin *Plugin) cleanupOldStreamAccumulators() {
	fiveMinutesAgo := time.Now().Add(-5 * time.Minute)
	cleanedCount := 0

	plugin.streamAccumulators.Range(func(key, value interface{}) bool {
		requestID := key.(string)
		accumulator := value.(*StreamAccumulator)
		accumulator.mu.Lock()
		defer accumulator.mu.Unlock()

		// Check if this accumulator is old (no activity for 5 minutes)
		if len(accumulator.Chunks) > 0 {
			firstChunkTime := accumulator.Chunks[0].Timestamp
			if firstChunkTime.Before(fiveMinutesAgo) {
				plugin.streamAccumulators.Delete(requestID)
				cleanedCount++
				plugin.logger.Debug(fmt.Sprintf("%s Cleaned up old stream accumulator for request %s", PluginLoggerPrefix, requestID))
			}
		}
		return true
	})

	if cleanedCount > 0 {
		plugin.logger.Debug(fmt.Sprintf("%s Cleaned up %d old stream accumulators", PluginLoggerPrefix, cleanedCount))
	}
}
