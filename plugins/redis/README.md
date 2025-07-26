# Redis Cache Plugin for Bifrost

This plugin provides Redis-based caching functionality for Bifrost requests. It caches responses based on request body hashes and returns cached responses for identical requests, significantly improving performance and reducing API costs.

## Features

- **High-Performance Hashing**: Uses xxhash for ultra-fast request body hashing
- **Asynchronous Caching**: Non-blocking cache writes for optimal response times
- **Response Caching**: Stores complete responses in Redis with configurable TTL
- **Cache Hit Detection**: Returns cached responses for identical requests
- **Simple Setup**: Only requires Redis address - sensible defaults for everything else
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

// Simple configuration - only Redis address is required!
config := redis.RedisPluginConfig{
    Addr: "localhost:6379",  // Your Redis server address
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
- **CacheOnlySuccessful**: true (only cache successful responses)
- **CacheByModel**: true (include model in cache key)
- **CacheByProvider**: true (include provider in cache key)

### With Password Authentication

```go
config := redis.RedisPluginConfig{
    Addr:     "localhost:6379",
    Password: "your-redis-password",
}
```

### With Custom TTL and Prefix

```go
config := redis.RedisPluginConfig{
    Addr:   "localhost:6379",
    TTL:    time.Hour,           // Cache for 1 hour
    Prefix: "myapp:cache:",      // Custom prefix
}
```

### With Different Database

```go
config := redis.RedisPluginConfig{
    Addr: "localhost:6379",
    DB:   1,                     // Use Redis database 1
}
```

### Cache All Responses (Including Errors)

```go
config := redis.RedisPluginConfig{
    Addr:                "localhost:6379",
    CacheOnlySuccessful: bifrost.Ptr(false), // Cache both successful and error responses
}
```

### Custom Cache Key Configuration

```go
config := redis.RedisPluginConfig{
    Addr:            "localhost:6379",
    CacheByModel:    bifrost.Ptr(false), // Don't include model in cache key
    CacheByProvider: bifrost.Ptr(true),  // Include provider in cache key
}
```

### Custom Redis Client Configuration

```go
config := redis.RedisPluginConfig{
    Addr:            "localhost:6379",
    PoolSize:        20,                // Custom connection pool size
    DialTimeout:     5 * time.Second,   // Custom connection timeout
    ReadTimeout:     3 * time.Second,   // Custom read timeout
    ConnMaxLifetime: time.Hour,         // Custom connection lifetime
}
```

## Configuration Options

| Option                | Type            | Required | Default           | Description                        |
| --------------------- | --------------- | -------- | ----------------- | ---------------------------------- |
| `Addr`                | `string`        | ✅       | -                 | Redis server address (host:port)   |
| `Username`            | `string`        | ❌       | `""`              | Username for Redis AUTH (Redis 6+) |
| `Password`            | `string`        | ❌       | `""`              | Password for Redis AUTH            |
| `DB`                  | `int`           | ❌       | `0`               | Redis database number              |
| `TTL`                 | `time.Duration` | ❌       | `5 * time.Minute` | Time-to-live for cached responses  |
| `Prefix`              | `string`        | ❌       | `""`              | Prefix for cache keys              |
| `CacheOnlySuccessful` | `*bool`         | ❌       | `true`            | Only cache successful responses    |
| `CacheByModel`        | `*bool`         | ❌       | `true`            | Include model in cache key         |
| `CacheByProvider`     | `*bool`         | ❌       | `true`            | Include provider in cache key      |

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

1. **PreHook**: Checks Redis for cached response, returns immediately if found
2. **PostHook**: Stores the response in Redis asynchronously (non-blocking)
3. **Cleanup**: Clears all cached entries and closes connection on shutdown

**Asynchronous Caching**: Cache writes happen in background goroutines with a 30-second timeout, ensuring responses are never delayed by Redis operations. This provides optimal performance while maintaining cache functionality.

### Cache Keys

Cache keys follow the pattern: `{prefix}{xxhash}`

Example: `bifrost:cache:a1b2c3d4e5f6...`

## Testing

Run the tests with a Redis instance running:

```bash
# Start Redis (using Docker)
docker run -d -p 6379:6379 redis:latest

# Run tests
go test ./...
```

Tests will be skipped if Redis is not available.

## Performance Benefits

- **Reduced API Calls**: Identical requests are served from cache
- **Ultra-Low Latency**: Cache hits return immediately, cache writes are non-blocking
- **Cost Savings**: Fewer API calls to expensive LLM providers
- **Improved Reliability**: Cached responses available even if provider is down
- **High Throughput**: Asynchronous caching doesn't impact response times

## Error Handling

The plugin is designed to fail gracefully:

- If Redis is unavailable during startup, plugin creation fails with clear error
- If Redis becomes unavailable during operation, requests continue without caching
- If cache retrieval fails, requests proceed normally
- If cache storage fails asynchronously, responses are unaffected (already returned)
- Malformed cached data is ignored and requests proceed normally
- Cache operations have timeouts to prevent resource leaks

## Best Practices

1. **Start Simple**: Use only `Addr` and let defaults handle the rest
2. **Set appropriate TTL**: Balance between cache efficiency and data freshness
3. **Use meaningful prefixes**: Helps organize cache keys in shared Redis instances
4. **Monitor Redis memory**: Track cache usage in production environments
5. **Use `bifrost.Ptr()`**: For boolean pointer configuration options

## Security Considerations

- **Sensitive Data**: Be cautious about caching responses containing sensitive information
- **Redis Security**: Use authentication and network security for Redis
- **Data Isolation**: Use different Redis databases or prefixes for different environments
