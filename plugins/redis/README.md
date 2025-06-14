# Redis Cache Plugin for Bifrost

This plugin provides Redis-based caching functionality for Bifrost requests. It caches responses based on request body hashes and returns cached responses for identical requests, significantly improving performance and reducing API costs.

## Features

- **Request Body Hashing**: Uses SHA256 to generate consistent hashes from request bodies
- **Response Caching**: Stores complete responses in Redis with configurable TTL
- **Cache Hit Detection**: Returns cached responses for identical requests
- **Cache Management**: Provides utilities to clear cache and get cache information
- **Configurable**: Supports custom Redis connection settings, TTL, and key prefixes

## Installation

```bash
go get github.com/redis/go-redis/v9
go get github.com/maximhq/bifrost/core
```

## Usage

### Basic Setup

```go
import (
    "github.com/maximhq/bifrost/plugins/redis"
    "time"
)

// Configure the Redis plugin
config := redis.RedisPluginConfig{
    RedisAddr:     "localhost:6379",  // Redis server address
    RedisPassword: "",                // Redis password (if required)
    RedisDB:       0,                 // Redis database number
    TTL:           24 * time.Hour,    // Cache TTL (24 hours)
    Prefix:        "bifrost:cache:",  // Cache key prefix
}

// Create the plugin
plugin, err := redis.NewRedisPlugin(config)
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

### Configuration Options

| Option          | Type            | Default            | Description                       |
| --------------- | --------------- | ------------------ | --------------------------------- |
| `RedisAddr`     | `string`        | `"localhost:6379"` | Redis server address              |
| `RedisPassword` | `string`        | `""`               | Redis password (empty if no auth) |
| `RedisDB`       | `int`           | `0`                | Redis database number             |
| `TTL`           | `time.Duration` | `24 * time.Hour`   | Time-to-live for cached responses |
| `Prefix`        | `string`        | `"bifrost:cache:"` | Prefix for cache keys             |

### Advanced Usage

#### Cache Management

```go
// Get cache information
info, err := plugin.(*redis.Plugin).GetCacheInfo(ctx)
if err != nil {
    log.Printf("Cache info: %+v", info)
}

// Clear all cached entries
err = plugin.(*redis.Plugin).ClearCache(ctx)
if err != nil {
    log.Printf("Failed to clear cache: %v", err)
}

// Close Redis connection when done
defer plugin.(*redis.Plugin).Close()
```

## How It Works

### Request Hashing

The plugin generates a SHA256 hash of the normalized request including:

- Provider
- Model
- Input (chat completion or text completion)
- Parameters
- Fallbacks

Identical requests will always produce the same hash, enabling effective caching.

### Caching Flow

1. **PreHook**:

   - Generates hash from incoming request
   - Checks Redis for cached response
   - If found, returns cached response (skips provider call)
   - If not found, stores hash in context for PostHook

2. **PostHook**:
   - Retrieves hash from context
   - Stores the response in Redis with the hash as key
   - Sets TTL on the cached entry

### Cache Keys

Cache keys follow the pattern: `{prefix}{sha256_hash}`

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
- **Lower Latency**: Cache hits return immediately without network calls
- **Cost Savings**: Fewer API calls to expensive LLM providers
- **Improved Reliability**: Cached responses available even if provider is down

## Error Handling

The plugin is designed to fail gracefully:

- If Redis is unavailable, requests continue without caching
- If cache retrieval fails, requests proceed normally
- If cache storage fails, responses are returned without caching
- Malformed cached data is ignored and requests proceed normally

## Redis Connection

The plugin supports standard Redis connection options:

- Single Redis instance
- Redis with authentication
- Different Redis databases
- Custom connection timeouts (5-second timeout for initial connection)

## Cache Invalidation

- **TTL-based**: Entries automatically expire after the configured TTL
- **Manual**: Use `ClearCache()` to remove all entries
- **Selective**: Redis CLI can be used for manual key management

## Best Practices

1. **Set appropriate TTL**: Balance between cache efficiency and data freshness
2. **Use meaningful prefixes**: Helps organize cache keys in shared Redis instances
3. **Monitor cache hit rates**: Use `GetCacheInfo()` to track cache effectiveness
4. **Consider cache size**: Monitor Redis memory usage in production
5. **Handle Redis failures**: Ensure your application works without caching

## Security Considerations

- **Sensitive Data**: Be cautious about caching responses containing sensitive information
- **Redis Security**: Use password authentication and network security for Redis
- **Key Collisions**: The SHA256 hash makes collisions extremely unlikely
- **Data Isolation**: Use different Redis databases or prefixes for different environments
