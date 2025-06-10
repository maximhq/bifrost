// Package redis provides Redis caching integration for Bifrost plugin.
// This plugin caches request body hashes and returns cached responses for identical requests.
package redis

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/redis/go-redis/v9"
)

// RedisPluginConfig contains configuration for the Redis plugin behavior
type RedisPluginConfig struct {
	TTL    time.Duration // Time-to-live for cached responses
	Prefix string        // Prefix for cache keys
}

// Plugin implements the schemas.Plugin interface for Redis caching.
// It caches responses based on request body hashes and returns cached
// responses for identical requests.
type Plugin struct {
	client *redis.Client
	config RedisPluginConfig
	logger schemas.Logger
}

// NewRedisPlugin creates a new Redis plugin instance
//
// Parameters:
//   - client: Pre-configured Redis client
//   - config: Configuration for the Redis plugin behavior
//   - logger: Logger instance for the plugin
//
// Returns:
//   - schemas.Plugin: A configured Redis plugin instance
//   - error: Any error that occurred during plugin initialization
func NewRedisPlugin(client *redis.Client, config RedisPluginConfig, logger schemas.Logger) (schemas.Plugin, error) {
	if client == nil {
		return nil, fmt.Errorf("redis client cannot be nil")
	}

	// Set default values
	if config.TTL == 0 {
		logger.Warn("[Bifrost Redis Plugin] TTL is not set, using default of 5 minutes")
		config.TTL = 5 * time.Minute // Default to 5 minutes
	}

	// Test connection
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := client.Ping(ctx).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to ping Redis: %w", err)
	}

	return &Plugin{
		client: client,
		config: config,
		logger: logger,
	}, nil
}

// generateRequestHash creates a SHA256 hash of the request for caching
func (p *Plugin) generateRequestHash(req *schemas.BifrostRequest) (string, error) {
	// Create a normalized request for hashing
	// Note: Fallbacks are excluded as they only affect error handling, not the actual response
	normalizedReq := struct {
		Provider schemas.ModelProvider    `json:"provider"`
		Model    string                   `json:"model"`
		Input    schemas.RequestInput     `json:"input"`
		Params   *schemas.ModelParameters `json:"params,omitempty"`
	}{
		Provider: req.Provider,
		Model:    req.Model,
		Input:    req.Input,
		Params:   req.Params,
	}

	// Marshal to JSON for consistent hashing
	jsonData, err := json.Marshal(normalizedReq)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	// Generate SHA256 hash
	hash := sha256.Sum256(jsonData)
	return fmt.Sprintf("%x", hash), nil
}

// ContextKey is a custom type for context keys to prevent key collisions
type ContextKey string

const (
	RequestHashKey ContextKey = "redis_request_hash"
)

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
func (p *Plugin) PreHook(ctx *context.Context, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.BifrostResponse, error) {
	// Generate hash for the request
	hash, err := p.generateRequestHash(req)
	if err != nil {
		// If we can't generate hash, just continue without caching
		p.logger.Warn("[Bifrost Redis Plugin] Failed to generate request hash, continuing without caching")
		return req, nil, nil
	}

	// Store hash in context for PostHook
	*ctx = context.WithValue(*ctx, RequestHashKey, hash)

	// Create cache key
	cacheKey := p.config.Prefix + hash

	// Check if cached response exists
	cachedData, err := p.client.Get(*ctx, cacheKey).Result()
	if err != nil {
		if err == redis.Nil {
			p.logger.Debug("[Bifrost Redis Plugin] No cached response found, continuing with request")
			// No cached response found, continue with normal processing
			return req, nil, nil
		}
		// Log error but continue processing
		p.logger.Warn("[Bifrost Redis Plugin] Failed to get cached response, continuing without caching")
		return req, nil, nil
	} else {
		p.logger.Debug(fmt.Sprintf("[Bifrost Redis Plugin] Found cached response for request %s, returning it", cacheKey))
	}

	// Unmarshal cached response
	var cachedResponse schemas.BifrostResponse
	if err := json.Unmarshal([]byte(cachedData), &cachedResponse); err != nil {
		// If we can't unmarshal, just continue without cached response
		p.logger.Warn("[Bifrost Redis Plugin] Failed to unmarshal cached response, continuing without caching")
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
	return req, &cachedResponse, nil
}

// PostHook is called after a response is received from a provider.
// It caches the response using the request hash as the key.
//
// Parameters:
//   - ctx: Pointer to the context.Context
//   - result: The response from the provider
//
// Returns:
//   - *schemas.BifrostResponse: The original response
//   - error: Any error that occurred during caching
func (p *Plugin) PostHook(ctx *context.Context, result *schemas.BifrostResponse) (*schemas.BifrostResponse, error) {
	// Extract the hash from context
	hashValue := (*ctx).Value(RequestHashKey)
	if hashValue == nil {
		// If we don't have the hash, we can't cache
		p.logger.Warn("[Bifrost Redis Plugin] No hash found in context, continuing without caching")
		return result, nil
	}

	hash, ok := hashValue.(string)
	if !ok {
		p.logger.Warn("[Bifrost Redis Plugin] Hash is not a string, continuing without caching")
		return result, nil
	}

	// Create cache key
	cacheKey := p.config.Prefix + hash

	// Marshal response for caching
	responseData, err := json.Marshal(result)
	if err != nil {
		// If we can't marshal, just return the response without caching
		p.logger.Warn("[Bifrost Redis Plugin] Failed to marshal response, continuing without caching")
		return result, nil
	}

	// Cache the response
	err = p.client.Set(*ctx, cacheKey, responseData, p.config.TTL).Err()
	if err != nil {
		// Log error but don't fail the request
		p.logger.Warn("[Bifrost Redis Plugin] Failed to cache response, continuing without caching")
	} else {
		p.logger.Debug(fmt.Sprintf("[Bifrost Redis Plugin] Cached response for request %s", cacheKey))
	}

	return result, nil
}
