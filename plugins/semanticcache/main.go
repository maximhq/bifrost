// Package semanticcache provides semantic caching integration for Bifrost plugin.
// This plugin caches request body hashes using xxhash and returns cached responses for identical requests.
// It supports configurable caching behavior via the VectorStore abstraction, including success-only caching and custom cache key generation.
package semanticcache

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
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
	store          vectorstore.VectorStore
	config         Config
	logger         schemas.Logger
	client         *bifrost.Bifrost
	isIndexCreated atomic.Bool // Track if semantic index was created (performance optimization)
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
	SemanticIndexName      string        = "bifrost_semantic_index"
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
		if plugin.isIndexCreated.Load() {
			plugin.logger.Warn(PluginLoggerPrefix + " Semantic search failed: " + err.Error())
		}
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

	// Cache hash, response, and embedding asynchronously to avoid blocking the response
	go func() {
		// Create a background context with timeout for the cache operation
		// This ensures the cache operation doesn't run indefinitely
		cacheCtx, cancel := context.WithTimeout(context.Background(), CacheSetTimeout)
		defer cancel()

		// Create cache keys
		hashKey := plugin.generateCacheKey(provider, model, requestID, "hash")

		// Use plugin prefix for both embedding and response keys for consistency
		embeddingKey := plugin.generateCacheKey(provider, model, requestID, "emb")
		responseKey := plugin.generateCacheKey(provider, model, requestID, "response")

		// Add "chunk_{index}" to the response key for streaming responses
		if plugin.isStreamingRequest(requestType) {
			responseKey = fmt.Sprintf("%s_chunk_%d", responseKey, res.ExtraFields.ChunkIndex)
		}

		// Store the hash (only once for the first chunk or non-streaming)
		if !plugin.isStreamingRequest(requestType) || res.ExtraFields.ChunkIndex == 0 {
			if err := plugin.store.Add(cacheCtx, hashKey, hash, cacheTTL); err != nil {
				plugin.logger.Warn(PluginLoggerPrefix + " Failed to cache hash asynchronously: " + err.Error())
			}

			// Store embedding with metadata using native vector search if available
			if embedding != nil {
				// Get metadata from context
				metadata, _ := (*ctx).Value(requestMetadataKey).(map[string]interface{})
				if metadata == nil {
					metadata = make(map[string]interface{})
				}

				// Ensure semantic index exists only once (performance optimization)
				if !plugin.isIndexCreated.Load() {
					embeddingDim := len(embedding)
					metadataFields := []string{"temperature", "max_tokens", "tools_hash", "tool_choice", "top_p", "top_k", "stop_sequences", "presence_penalty", "frequency_penalty", "parallel_tool_calls", "user", "voice", "attachments", "stream"}
					if err := plugin.store.EnsureSemanticIndex(cacheCtx, SemanticIndexName, plugin.config.Prefix, embeddingDim, metadataFields); err != nil {
						plugin.logger.Warn(PluginLoggerPrefix + " Failed to ensure semantic index: " + err.Error())
					} else {
						plugin.isIndexCreated.Store(true) // Mark as created - never call again
					}
				}

				if err := plugin.store.AddSemanticCache(cacheCtx, embeddingKey, embedding, metadata, cacheTTL); err != nil {
					plugin.logger.Warn(PluginLoggerPrefix + " Failed to cache embedding with metadata asynchronously: " + err.Error())
				}
			}
		}

		// Marshal response for caching
		responseData, err := json.Marshal(res)
		if err != nil {
			// If we can't marshal, just return the response without caching
			plugin.logger.Warn(PluginLoggerPrefix + " Failed to marshal response, continuing without caching")
			return
		}

		// Store the response
		if err := plugin.store.Add(cacheCtx, responseKey, string(responseData), cacheTTL); err != nil {
			plugin.logger.Warn(PluginLoggerPrefix + " Failed to cache response asynchronously: " + err.Error())
		} else {
			plugin.logger.Debug(fmt.Sprintf("%s Cached response for request %s", PluginLoggerPrefix, requestID))
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
	// Get all keys matching the prefix using SCAN
	var keys []string
	var cursor *string

	for {
		batch, c, err := plugin.store.GetAll(context.Background(), plugin.config.Prefix+"*", cursor, 1000)
		if err != nil {
			return fmt.Errorf("failed to scan keys for cleanup: %w", err)
		}
		keys = append(keys, batch...)
		cursor = c
		if cursor == nil {
			break
		}
	}

	if len(keys) > 0 {
		if err := plugin.store.Delete(context.Background(), keys); err != nil {
			return fmt.Errorf("failed to delete cache keys: %w", err)
		}
		plugin.logger.Info(fmt.Sprintf("%s Cleaned up %d cache entries", PluginLoggerPrefix, len(keys)))
	}

	// Also delete the semantic index to ensure clean state for next restart
	if plugin.isIndexCreated.Load() {
		if err := plugin.store.DropSemanticIndex(context.Background(), SemanticIndexName); err != nil {
			plugin.logger.Warn(PluginLoggerPrefix + " Failed to drop semantic index during cleanup: " + err.Error())
		} else {
			plugin.logger.Info(PluginLoggerPrefix + " Semantic index dropped successfully")
		}
	}

	plugin.client.Cleanup()

	return nil
}

// Public Methods for External Use

// ClearCacheForKey deletes cache entries for a specific request ID pattern.
// Updated to handle the new key format: {provider}-{model}-{reqid}-{suffix}
// It deletes both hash and response keys for the given pattern.
//
// Parameters:
//   - pattern: The pattern to match for deletion (e.g., "*-*-{requestID}-*")
//
// Returns:
//   - error: Any error that occurred during cache key deletion
func (plugin *Plugin) ClearCacheForKey(pattern string) error {
	// Ensure pattern has prefix
	if !strings.HasPrefix(pattern, plugin.config.Prefix) {
		pattern = plugin.config.Prefix + pattern
	}

	// Get all keys matching the pattern
	var keys []string
	var cursor *string
	for {
		batch, c, err := plugin.store.GetAll(context.Background(), pattern, cursor, 1000)
		if err != nil {
			plugin.logger.Warn(fmt.Sprintf("%s Failed to scan keys for deletion for pattern '%s': %v", PluginLoggerPrefix, pattern, err))
			return err
		}
		keys = append(keys, batch...)
		cursor = c
		if cursor == nil {
			break
		}
	}

	if len(keys) > 0 {
		if err := plugin.store.Delete(context.Background(), keys); err != nil {
			plugin.logger.Warn(fmt.Sprintf("%s Failed to delete %d cache keys for pattern '%s': %v", PluginLoggerPrefix, len(keys), pattern, err))
			return err
		}
		plugin.logger.Debug(fmt.Sprintf("%s Deleted %d cache entries for pattern %s", PluginLoggerPrefix, len(keys), pattern))
	}

	return nil
}

// ClearCacheForRequestID deletes cache entries for a specific request ID.
// It deletes both hash and response keys (including streaming chunks) for the given request ID.
//
// Parameters:
//   - req: The Bifrost request to generate the key pattern for
//   - requestID: The request ID to delete cache entries for
//
// Returns:
//   - error: Any error that occurred during cache key deletion
func (plugin *Plugin) ClearCacheForRequestID(req *schemas.BifrostRequest, requestID string) error {
	// Create patterns for hash and response keys
	hashPattern := plugin.generateCacheKey(req.Provider, req.Model, requestID, "hash")
	embeddingPattern := plugin.generateCacheKey(req.Provider, req.Model, requestID, "emb")
	responsePattern := plugin.generateCacheKey(req.Provider, req.Model, requestID, "response") + "*" // Include streaming chunks

	// Delete hash key
	if err := plugin.ClearCacheForKey(hashPattern); err != nil {
		plugin.logger.Warn(PluginLoggerPrefix + " Failed to delete hash key: " + err.Error())
	}

	// Delete embedding key
	if err := plugin.ClearCacheForKey(embeddingPattern); err != nil {
		plugin.logger.Warn(PluginLoggerPrefix + " Failed to delete embedding key: " + err.Error())
	}

	// Delete response keys (including chunks)
	if err := plugin.ClearCacheForKey(responsePattern); err != nil {
		plugin.logger.Warn(PluginLoggerPrefix + " Failed to delete response keys: " + err.Error())
	}

	return nil
}
