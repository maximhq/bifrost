// Package redis provides Redis caching integration for Bifrost plugin.
// This plugin caches request body hashes using xxhash and returns cached responses for identical requests.
// It supports configurable caching behavior including success-only caching and custom cache key generation.
package redis

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/cespare/xxhash/v2"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/redis/go-redis/v9"
)

// RedisPluginConfig contains configuration for the Redis plugin.
// All Redis client options are passed directly to the Redis client, which handles its own defaults.
// Only specify values you want to override from Redis client defaults.
type RedisPluginConfig struct {
	// Connection settings
	Addr     string `json:"addr"`               // Redis server address (host:port) - REQUIRED
	Username string `json:"username,omitempty"` // Username for Redis AUTH (optional)
	Password string `json:"password,omitempty"` // Password for Redis AUTH (optional)
	DB       int    `json:"db,omitempty"`       // Redis database number (default: 0)

	// Connection pool and timeout settings (passed directly to Redis client)
	PoolSize        int           `json:"pool_size,omitempty"`          // Maximum number of socket connections (optional)
	MinIdleConns    int           `json:"min_idle_conns,omitempty"`     // Minimum number of idle connections (optional)
	MaxIdleConns    int           `json:"max_idle_conns,omitempty"`     // Maximum number of idle connections (optional)
	ConnMaxLifetime time.Duration `json:"conn_max_lifetime,omitempty"`  // Connection maximum lifetime (optional)
	ConnMaxIdleTime time.Duration `json:"conn_max_idle_time,omitempty"` // Connection maximum idle time (optional)
	DialTimeout     time.Duration `json:"dial_timeout,omitempty"`       // Timeout for socket connection (optional)
	ReadTimeout     time.Duration `json:"read_timeout,omitempty"`       // Timeout for socket reads (optional)
	WriteTimeout    time.Duration `json:"write_timeout,omitempty"`      // Timeout for socket writes (optional)
	ContextTimeout  time.Duration `json:"context_timeout,omitempty"`    // Timeout for Redis operations (optional)

	// Plugin behavior settings
	TTL                 time.Duration `json:"ttl,omitempty"`                   // Time-to-live for cached responses (default: 5min)
	Prefix              string        `json:"prefix,omitempty"`                // Prefix for cache keys (optional)
	CacheOnlySuccessful *bool         `json:"cache_only_successful,omitempty"` // Only cache successful responses (default: true)

	// Advanced caching behavior
	CacheByModel    *bool `json:"cache_by_model,omitempty"`    // Include model in cache key (default: true)
	CacheByProvider *bool `json:"cache_by_provider,omitempty"` // Include provider in cache key (default: true)
}

// Plugin implements the schemas.Plugin interface for Redis caching.
// It caches responses based on xxhash of normalized requests and returns cached
// responses for identical requests. The plugin supports configurable caching behavior
// including success-only caching and custom cache key generation.
//
// Fields:
//   - client: Redis client instance for cache operations
//   - config: Plugin configuration including Redis and caching settings
//   - logger: Logger instance for plugin operations
type Plugin struct {
	client *redis.Client
	config RedisPluginConfig
	logger schemas.Logger
}

const (
	PluginName         string = "bifrost-redis"
	PluginLoggerPrefix string = "[Bifrost Redis Plugin]"
)

// NewRedisPlugin creates a new Redis plugin instance with the provided configuration.
// It establishes a connection to Redis, tests connectivity, and returns a configured plugin.
//
// All Redis client options are passed directly to the Redis client, which handles its own defaults.
// The plugin only sets defaults for its own behavior (TTL, CacheOnlySuccessful, etc.).
//
// Parameters:
//   - config: Redis and plugin configuration (only Addr is required)
//   - logger: Logger instance for the plugin
//
// Returns:
//   - schemas.Plugin: A configured Redis plugin instance
//   - error: Any error that occurred during plugin initialization or Redis connection
func NewRedisPlugin(config RedisPluginConfig, logger schemas.Logger) (schemas.Plugin, error) {
	// Validate required field
	if config.Addr == "" {
		return nil, fmt.Errorf("redis address (addr) is required")
	}

	// Set plugin-specific defaults (not Redis defaults)
	if config.TTL == 0 {
		logger.Warn(PluginLoggerPrefix + " TTL is not set, using default of 5 minutes")
		config.TTL = 5 * time.Minute
	}
	if config.ContextTimeout == 0 {
		config.ContextTimeout = 10 * time.Second // Only for our ping test
	}
	if config.CacheOnlySuccessful == nil {
		config.CacheOnlySuccessful = bifrost.Ptr(true) // Default to only caching successful responses
	}
	// Set cache behavior defaults
	if config.CacheByModel == nil {
		config.CacheByModel = bifrost.Ptr(true)
	}
	if config.CacheByProvider == nil {
		config.CacheByProvider = bifrost.Ptr(true)
	}

	// Create Redis client with all provided options
	opts := &redis.Options{
		Addr:            config.Addr,
		Username:        config.Username,
		Password:        config.Password,
		DB:              config.DB,
		PoolSize:        config.PoolSize,
		MinIdleConns:    config.MinIdleConns,
		MaxIdleConns:    config.MaxIdleConns,
		ConnMaxLifetime: config.ConnMaxLifetime,
		ConnMaxIdleTime: config.ConnMaxIdleTime,
		DialTimeout:     config.DialTimeout,
		ReadTimeout:     config.ReadTimeout,
		WriteTimeout:    config.WriteTimeout,
	}

	// Create Redis client
	client := redis.NewClient(opts)

	// Test connection with configured timeout
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := client.Ping(ctx).Result()
	if err != nil {
		client.Close()
		return nil, fmt.Errorf("failed to ping Redis at %s: %w", config.Addr, err)
	}

	logger.Info(fmt.Sprintf("%s Successfully connected to Redis at %s", PluginLoggerPrefix, config.Addr))

	return &Plugin{
		client: client,
		config: config,
		logger: logger,
	}, nil
}

// generateRequestHash creates an xxhash of the request for caching.
// It normalizes the request by including only the relevant fields based on configuration:
// - Provider (if CacheByProvider is true)
// - Model (if CacheByModel is true)
// - Input (chat completion or text completion)
// - Parameters (all parameters are included)
//
// Note: Fallbacks are excluded as they only affect error handling, not the actual response.
//
// Parameters:
//   - req: The Bifrost request to hash
//
// Returns:
//   - string: Hexadecimal representation of the xxhash
//   - error: Any error that occurred during request normalization or hashing
func (plugin *Plugin) generateRequestHash(req *schemas.BifrostRequest) (string, error) {
	// Create a normalized request for hashing
	// Note: Fallbacks are excluded as they only affect error handling, not the actual response
	normalizedReq := struct {
		Provider schemas.ModelProvider    `json:"provider,omitempty"`
		Model    string                   `json:"model,omitempty"`
		Input    schemas.RequestInput     `json:"input"`
		Params   *schemas.ModelParameters `json:"params,omitempty"`
	}{
		Input: req.Input,
	}

	// Include provider and model based on configuration
	if plugin.config.CacheByProvider != nil && *plugin.config.CacheByProvider {
		normalizedReq.Provider = req.Provider
	}
	if plugin.config.CacheByModel != nil && *plugin.config.CacheByModel {
		normalizedReq.Model = req.Model
	}

	// Include all parameters in cache key
	normalizedReq.Params = req.Params

	// Marshal to JSON for consistent hashing
	jsonData, err := json.Marshal(normalizedReq)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	// Generate hash based on configured algorithm
	hash := xxhash.Sum64(jsonData)
	return fmt.Sprintf("%x", hash), nil
}

// ContextKey is a custom type for context keys to prevent key collisions
type ContextKey string

const (
	RequestHashKey ContextKey = "redis_request_hash"
)

// GetName returns the canonical name of the Redis plugin.
// This name is used for plugin identification and logging purposes.
//
// Returns:
//   - string: The plugin name "bifrost-redis"
func (p *Plugin) GetName() string {
	return PluginName
}

// PreHook is called before a request is processed by Bifrost.
// It checks if a cached response exists for the request hash and returns it if found.
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
	// Generate hash for the request
	hash, err := plugin.generateRequestHash(req)
	if err != nil {
		// If we can't generate hash, just continue without caching
		plugin.logger.Warn(PluginLoggerPrefix + " Failed to generate request hash, continuing without caching")
		return req, nil, nil
	}

	// Store hash in context for PostHook
	*ctx = context.WithValue(*ctx, RequestHashKey, hash)

	// Create cache key
	cacheKey := plugin.config.Prefix + hash

	// Check if cached response exists
	cachedData, err := plugin.client.Get(*ctx, cacheKey).Result()
	if err != nil {
		if err == redis.Nil {
			plugin.logger.Debug(PluginLoggerPrefix + " No cached response found, continuing with request")
			// No cached response found, continue with normal processing
			return req, nil, nil
		}
		// Log error but continue processing
		plugin.logger.Warn(PluginLoggerPrefix + " Failed to get cached response, continuing without caching")
		return req, nil, nil
	} else {
		plugin.logger.Info(fmt.Sprintf("%s Found cached response for request %s, returning it", PluginLoggerPrefix, cacheKey))
	}

	// Unmarshal cached response
	var cachedResponse schemas.BifrostResponse
	if err := json.Unmarshal([]byte(cachedData), &cachedResponse); err != nil {
		// If we can't unmarshal, just continue without cached response
		plugin.logger.Warn(PluginLoggerPrefix + " Failed to unmarshal cached response, continuing without caching")
		return req, nil, nil
	}

	// Mark response as cached in extra fields
	if cachedResponse.ExtraFields.RawResponse == nil {
		cachedResponse.ExtraFields.RawResponse = make(map[string]interface{})
	}
	if rawResponseMap, ok := cachedResponse.ExtraFields.RawResponse.(map[string]interface{}); ok {
		rawResponseMap["bifrost_cached"] = true
		rawResponseMap["bifrost_cache_key"] = hash
	}

	// Return cached response
	return req, &schemas.PluginShortCircuit{
		Response: &cachedResponse,
	}, nil
}

// PostHook is called after a response is received from a provider.
// It caches the response using the request hash as the key, with optional filtering
// based on the CacheOnlySuccessful configuration.
//
// The function performs the following operations:
// 1. Checks if CacheOnlySuccessful is enabled and skips caching for unsuccessful responses
// 2. Retrieves the request hash from the context (set during PreHook)
// 3. Marshals the response for storage
// 4. Stores the response in Redis asynchronously (non-blocking)
//
// The Redis SET operation runs in a separate goroutine to avoid blocking the response.
// The function gracefully handles errors and continues without caching if any step fails,
// ensuring that response processing is never interrupted by caching issues.
//
// Parameters:
//   - ctx: Pointer to the context.Context containing the request hash
//   - res: The response from the provider to be cached
//   - bifrostErr: The error from the provider, if any (used for success determination)
//
// Returns:
//   - *schemas.BifrostResponse: The original response, unmodified
//   - *schemas.BifrostError: The original error, unmodified
//   - error: Any error that occurred during caching preparation (always nil as errors are handled gracefully)
func (plugin *Plugin) PostHook(ctx *context.Context, res *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	// Check if we should only cache successful responses
	if plugin.config.CacheOnlySuccessful != nil && *plugin.config.CacheOnlySuccessful {
		// If result is nil, it means there was an error (unsuccessful response)
		if res == nil {
			plugin.logger.Debug(PluginLoggerPrefix + " Skipping cache for unsuccessful response (result is nil)")
			return res, nil, nil
		}
	}

	// Get the hash from context
	hashValue := (*ctx).Value(RequestHashKey)
	if hashValue == nil {
		// If we don't have the hash, we can't cache
		plugin.logger.Debug(PluginLoggerPrefix + " No hash found in context, continuing without caching")
		return res, nil, nil
	}

	hash, ok := hashValue.(string)
	if !ok {
		plugin.logger.Debug(PluginLoggerPrefix + " Hash is not a string, continuing without caching")
		return res, nil, nil
	}

	// Create cache key
	cacheKey := plugin.config.Prefix + hash

	// Marshal response for caching
	responseData, err := json.Marshal(res)
	if err != nil {
		// If we can't marshal, just return the response without caching
		plugin.logger.Warn(PluginLoggerPrefix + " Failed to marshal response, continuing without caching")
		return res, nil, nil
	}

	// Cache the response asynchronously to avoid blocking the response
	go func() {
		// Create a background context with timeout for the cache operation
		// This ensures the cache operation doesn't run indefinitely
		cacheCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		// Perform the Redis SET operation
		err := plugin.client.Set(cacheCtx, cacheKey, responseData, plugin.config.TTL).Err()
		if err != nil {
			plugin.logger.Warn(PluginLoggerPrefix + " Failed to cache response asynchronously: " + err.Error())
		} else {
			plugin.logger.Info(fmt.Sprintf("%s Cached response for request %s", PluginLoggerPrefix, cacheKey))
		}
	}()

	return res, nil, nil
}

// Cleanup performs cleanup operations for the Redis plugin.
// It removes all cached entries with the configured prefix and closes the Redis connection.
//
// The function performs the following operations:
// 1. Retrieves all cache keys matching the configured prefix pattern
// 2. Deletes all matching cache entries from Redis
// 3. Closes the Redis client connection
//
// This method should be called when shutting down the application to ensure
// proper resource cleanup and prevent connection leaks.
//
// Returns:
//   - error: Any error that occurred during cleanup operations
func (plugin *Plugin) Cleanup() error {
	keys, err := plugin.client.Keys(context.Background(), plugin.config.Prefix+"*").Result()
	if err != nil {
		return fmt.Errorf("failed to get keys for cleanup: %w", err)
	}

	if len(keys) > 0 {
		err = plugin.client.Del(context.Background(), keys...).Err()
		if err != nil {
			return fmt.Errorf("failed to delete cache keys: %w", err)
		}
		plugin.logger.Info(fmt.Sprintf("%s Cleaned up %d cache entries", PluginLoggerPrefix, len(keys)))
	}

	err = plugin.client.Close()
	if err != nil {
		return fmt.Errorf("failed to close Redis client: %w", err)
	}

	plugin.logger.Info(PluginLoggerPrefix + " Successfully closed Redis connection")
	return nil
}
