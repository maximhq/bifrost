# Redis Cache Plugin for Bifrost

This plugin provides Redis-based caching functionality for Bifrost requests. It caches responses based on request body hashes and returns cached responses for identical requests, significantly improving performance and reducing API costs.

## Features

- **High-Performance Hashing**: Uses xxhash for ultra-fast request body hashing
- **Asynchronous Caching**: Non-blocking cache writes for optimal response times
- **Response Caching**: Stores complete responses in Redis with configurable TTL
- **Streaming Cache Support**: Caches and retrieves streaming responses chunk by chunk
- **Cache Hit Detection**: Returns cached responses for identical requests
- **Intelligent Cache Recovery**: Automatically reconstructs streaming responses from cached chunks
- **Simple Setup**: Only requires Redis address and cache key - sensible defaults for everything else
- **Self-Contained**: Creates and manages its own Redis client

## Installation

```bash
go get github.com/maximhq/bifrost/core
go get github.com/maximhq/bifrost/plugins/redis
```

## Quick Start

### Basic Setup

```go
import (
    "github.com/maximhq/bifrost/plugins/redis"
    bifrost "github.com/maximhq/bifrost/core"
)

// Simple configuration - only Redis address and cache key are required!
config := redis.RedisPluginConfig{
    Addr:     "localhost:6379",     // Your Redis server address
    CacheKey: "x-my-cache-key",     // Context key for cache identification
}

// Create the plugin
plugin, err := redis.NewRedisPlugin(config, logger)
if err != nil {
    log.Fatal("Failed to create Redis plugin:", err)
}

// Use with Bifrost
bifrostConfig := schemas.BifrostConfig{
    Account: yourAccount,
    Plugins: []schemas.Plugin{plugin},
    // ... other config
}
```

That's it! The plugin uses Redis client defaults for connection handling and these defaults for caching:

- **TTL**: 5 minutes
- **CacheByModel**: true (include model in cache key)
- **CacheByProvider**: true (include provider in cache key)

**Important**: You must provide the cache key in your request context for caching to work:

```go
ctx := context.WithValue(ctx, redis.ContextKey("x-my-cache-key"), "cache-value")
response, err := client.ChatCompletionRequest(ctx, request)
```

### With Password Authentication

```go
config := redis.RedisPluginConfig{
    Addr:     "localhost:6379",
    CacheKey: "x-my-cache-key",
    Password: "your-redis-password",
}
```

### With Custom TTL and Prefix

```go
config := redis.RedisPluginConfig{
    Addr:     "localhost:6379",
    CacheKey: "x-my-cache-key",
    TTL:      time.Hour,           // Cache for 1 hour
    Prefix:   "myapp:cache:",      // Custom prefix
}
```

### Per-Request TTL Override (via Context)

You can override the cache TTL for individual requests by providing a TTL in the request context. Configure a `CacheTTLKey` on the plugin, then set a `time.Duration` value at that context key before making the request.

```go
// Configure plugin with a context key used to read per-request TTLs
config := redis.RedisPluginConfig{
    Addr:        "localhost:6379",
    CacheKey:    "x-my-cache-key",
    CacheTTLKey: "x-my-cache-ttl",    // The context key for reading TTL
    TTL:         5 * time.Minute,      // Fallback/default TTL
}

plugin, err := redis.NewRedisPlugin(config, logger)
// ... init Bifrost client with plugin

// Before making a request, set a per-request TTL
ctx := context.WithValue(ctx, redis.ContextKey("x-my-cache-ttl"), 30*time.Second)
resp, err := client.ChatCompletionRequest(ctx, request)
```

Notes:

- The context value must be of type `time.Duration`. If it is missing or of the wrong type, the plugin falls back to `config.TTL`.
- This applies to both regular and streaming requests. For streaming, the same per-request TTL applies to all chunks.

### With Different Database

```go
config := redis.RedisPluginConfig{
    Addr:     "localhost:6379",
    CacheKey: "x-my-cache-key",
    DB:       1,                   // Use Redis database 1
}
```

### Streaming Cache Example

```go
// Configure plugin for streaming cache
config := redis.RedisPluginConfig{
    Addr:     "localhost:6379",
    CacheKey: "x-stream-cache-key",
    TTL:      30 * time.Minute,    // Cache streaming responses for 30 minutes
}

// Use with streaming requests
ctx := context.WithValue(ctx, redis.ContextKey("x-stream-cache-key"), "stream-session-1")
stream, err := client.ChatCompletionStreamRequest(ctx, request)
// Subsequent identical requests will be served from cache as a reconstructed stream
```

### Custom Cache Key Configuration

```go
config := redis.RedisPluginConfig{
    Addr:            "localhost:6379",
    CacheKey:        "x-my-cache-key",
    CacheByModel:    bifrost.Ptr(false), // Don't include model in cache key
    CacheByProvider: bifrost.Ptr(true),  // Include provider in cache key
}
```

### Custom Redis Client Configuration

```go
config := redis.RedisPluginConfig{
    Addr:            "localhost:6379",
    CacheKey:        "x-my-cache-key",
    PoolSize:        20,                // Custom connection pool size
    DialTimeout:     5 * time.Second,   // Custom connection timeout
    ReadTimeout:     3 * time.Second,   // Custom read timeout
    ConnMaxLifetime: time.Hour,         // Custom connection lifetime
}
```

## Configuration Options

| Option            | Type            | Required | Default           | Description                         |
| ----------------- | --------------- | -------- | ----------------- | ----------------------------------- |
| `Addr`            | `string`        | ✅       | -                 | Redis server address (host:port)    |
| `CacheKey`        | `string`        | ✅       | -                 | Context key for cache identification|
| `CacheTTLKey`     | `string`        | ❌       | `""`              | Context key for per-request TTL override |
| `Username`        | `string`        | ❌       | `""`              | Username for Redis AUTH (Redis 6+) |
| `Password`        | `string`        | ❌       | `""`              | Password for Redis AUTH            |
| `DB`              | `int`           | ❌       | `0`               | Redis database number              |
| `TTL`             | `time.Duration` | ❌       | `5 * time.Minute` | Time-to-live for cached responses  |
| `Prefix`          | `string`        | ❌       | `""`              | Prefix for cache keys              |
| `CacheByModel`    | `*bool`         | ❌       | `true`            | Include model in cache key         |
| `CacheByProvider` | `*bool`         | ❌       | `true`            | Include provider in cache key      |

**Redis Connection Options** (all optional, Redis client uses its own defaults for zero values):

- `PoolSize`, `MinIdleConns`, `MaxIdleConns` - Connection pool settings
- `ConnMaxLifetime`, `ConnMaxIdleTime` - Connection lifetime settings
- `DialTimeout`, `ReadTimeout`, `WriteTimeout` - Timeout settings

All Redis configuration values are passed directly to the Redis client, which handles its own zero-value defaults. You only need to specify values you want to override from Redis client defaults.

## How It Works

The plugin generates an xxhash of the normalized request including:

- Provider (if CacheByProvider is true)
- Model (if CacheByModel is true)
- Input (chat completion or text completion)
- Parameters (includes tool calls)

Identical requests will always produce the same hash, enabling effective caching.

### Caching Flow

#### Regular Requests

1. **PreHook**: Checks Redis for cached response, returns immediately if found
2. **PostHook**: Stores the response in Redis asynchronously (non-blocking)
3. **Cleanup**: Clears all cached entries and closes connection on shutdown

#### Streaming Requests

1. **PreHook**: Checks Redis for cached chunks using pattern `{cache_key}_chunk_*`
2. **Cache Hit**: Reconstructs stream from cached chunks in correct order
3. **PostHook**: Stores each stream chunk with index: `{cache_key}_chunk_{index}`
4. **Stream Reconstruction**: Subsequent requests get sorted chunks as a new stream

**Asynchronous Caching**: Cache writes happen in background goroutines with a 30-second timeout, ensuring responses are never delayed by Redis operations. This provides optimal performance while maintaining cache functionality.

**Streaming Intelligence**: The plugin automatically detects streaming requests and handles chunk-based caching. Each chunk is stored with its index, allowing perfect reconstruction of the original stream order.

### Cache Keys

#### Regular Responses

Cache keys follow the pattern: `{prefix}{cache_value}_{xxhash}`

Example: `bifrost:cache:my-session_a1b2c3d4e5f6...`

#### Streaming Responses

Chunk keys follow the pattern: `{prefix}{cache_value}_{xxhash}_chunk_{index}`

Examples:

- `bifrost:cache:my-session_a1b2c3d4e5f6..._chunk_0`
- `bifrost:cache:my-session_a1b2c3d4e5f6..._chunk_1`
- `bifrost:cache:my-session_a1b2c3d4e5f6..._chunk_2`

## Manual Cache Invalidation

You can invalidate specific cached entries at runtime using the method `ClearCacheForKey(key string)` on the concrete `redis.Plugin` type. This deletes the provided key and, if it corresponds to a streaming response, all of its chunk entries (`<key>_chunk_*`).

### Getting the cache key from responses

- **Regular responses**: When a response is served from cache, the plugin adds metadata to `response.ExtraFields.RawResponse`:
  - `bifrost_cached: true`
  - `bifrost_cache_key: "<prefix><cache_value>_<xxhash>"`
  Use this `bifrost_cache_key` as the argument to `ClearCacheForKey`.

- **Streaming responses**: Cached stream chunks include `bifrost_cache_key` for the specific chunk, in the form `"<base>_chunk_{index}"`. To invalidate the entire stream cache, strip the `"_chunk_{index}"` suffix to obtain the base key and pass that base key to `ClearCacheForKey`.

### Examples

```go
// Non-streaming: clear the cached response you just used
resp, err := client.ChatCompletionRequest(ctx, req)
if err != nil {
    // handle error
}

if resp != nil && resp.ExtraFields.RawResponse != nil {
    if raw, ok := resp.ExtraFields.RawResponse.(map[string]interface{}); ok {
        if keyAny, ok := raw["bifrost_cache_key"]; ok {
            if pluginImpl, ok := plugin.(*redis.Plugin); ok {
                _ = pluginImpl.ClearCacheForKey(keyAny.(string))
            }
        }
    }
}
```

```go
// Streaming: clear all chunks for a cached stream
for msg := range stream {
    if msg.BifrostResponse == nil {
        continue
    }
    raw := msg.BifrostResponse.ExtraFields.RawResponse
    rawMap, ok := raw.(map[string]interface{})
    if !ok {
        continue
    }
    keyAny, ok := rawMap["bifrost_cache_key"]
    if !ok {
        continue
    }
    chunkKey := keyAny.(string) // e.g., "<base>_chunk_3"

    // Derive base key by removing the trailing "_chunk_{index}" part
    baseKey := chunkKey
    if idx := strings.LastIndex(chunkKey, "_chunk_"); idx != -1 {
        baseKey = chunkKey[:idx]
    }

    if pluginImpl, ok := plugin.(*redis.Plugin); ok {
        _ = pluginImpl.ClearCacheForKey(baseKey)
    }
    break // we only need one chunk to compute the base key
}
```

To clear all entries managed by this plugin (by prefix), call `Cleanup()` during shutdown:

```go
_ = plugin.(*redis.Plugin).Cleanup()
```

## Testing

The plugin includes comprehensive tests for both regular and streaming cache functionality.

Run the tests with a Redis instance running:

```bash
# Start Redis (using Docker)
docker run -d -p 6379:6379 redis:latest

# Run all tests
go test -v

# Run specific tests
go test -run TestRedisPlugin -v        # Test regular caching
go test -run TestRedisPluginStreaming -v  # Test streaming cache
```

Tests will be skipped if Redis is not available. The tests validate:

- Cache hit/miss behavior
- Performance improvements (cache should be significantly faster)
- Content integrity (cached responses match originals)
- Streaming chunk ordering and reconstruction
- Provider information preservation

## Performance Benefits

- **Reduced API Calls**: Identical requests are served from cache
- **Ultra-Low Latency**: Cache hits return immediately, cache writes are non-blocking  
- **Streaming Efficiency**: Cached streams are reconstructed and delivered faster than original API calls
- **Cost Savings**: Fewer API calls to expensive LLM providers
- **Improved Reliability**: Cached responses available even if provider is down
- **High Throughput**: Asynchronous caching doesn't impact response times
- **Perfect Stream Fidelity**: Cached streams maintain exact chunk ordering and content

## Error Handling

The plugin is designed to fail gracefully:

- If Redis is unavailable during startup, plugin creation fails with clear error
- If Redis becomes unavailable during operation, requests continue without caching
- If cache retrieval fails, requests proceed normally
- If cache storage fails asynchronously, responses are unaffected (already returned)
- Malformed cached data is ignored and requests proceed normally
- Cache operations have timeouts to prevent resource leaks

## Best Practices

1. **Start Simple**: Use only `Addr` and `CacheKey` - let defaults handle the rest
2. **Choose meaningful cache keys**: Use descriptive context keys that identify cache sessions
3. **Set appropriate TTL**: Balance between cache efficiency and data freshness
4. **Use meaningful prefixes**: Helps organize cache keys in shared Redis instances
5. **Monitor Redis memory**: Track cache usage, especially for streaming responses (more chunks = more storage)
6. **Context management**: Always provide cache key in request context for caching to work
7. **Use `bifrost.Ptr()`**: For boolean pointer configuration options
8. **Streaming considerations**: Longer streams create more cache entries, adjust TTL accordingly

## Security Considerations

- **Sensitive Data**: Be cautious about caching responses containing sensitive information
- **Redis Security**: Use authentication and network security for Redis
- **Data Isolation**: Use different Redis databases or prefixes for different environments
