# 🔌 Plugins

Custom middleware for request/response hooks, rate limiting, caching, and monitoring in Bifrost.

> **💡 Quick Start:** See the [30-second setup](../../quickstart/go-package.md) to add plugins to your Bifrost client.

---

## 📋 Plugin Overview

Plugins provide middleware functionality in Bifrost:

- **PreHook**: Intercept and modify requests before they reach providers
- **PostHook**: Modify responses after providers return
- **Cross-cutting concerns**: Rate limiting, caching, logging, monitoring
- **Custom logic**: Add functionality without modifying core Bifrost code

```go
type Plugin interface {
    GetName() string
    PreHook(ctx *context.Context, req *BifrostRequest) (*BifrostRequest, *PluginShortCircuit, error)
    PostHook(ctx *context.Context, result *BifrostResponse, err *BifrostError) (*BifrostResponse, *BifrostError, error)
    Cleanup() error
}
```

---

## 🚀 Basic Plugin Examples

### **Simple Logging Plugin**

```go
type LoggingPlugin struct {
    logger *log.Logger
}

func (p *LoggingPlugin) GetName() string {
    return "logging"
}

func (p *LoggingPlugin) PreHook(ctx *context.Context, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.PluginShortCircuit, error) {
    p.logger.Printf("Request: Provider=%s, Model=%s", req.Provider, req.Model)
    return req, nil, nil
}

func (p *LoggingPlugin) PostHook(ctx *context.Context, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
    if err != nil {
        p.logger.Printf("Error: %s", err.Error.Message)
    } else {
        p.logger.Printf("Success: Provider=%s", result.ExtraFields.Provider)
    }
    return result, err, nil
}

func (p *LoggingPlugin) Cleanup() error {
    return nil
}
```

### **Rate Limiting Plugin**

```go
type RateLimitPlugin struct {
    requests map[string]int
    mu       sync.Mutex
    limit    int
}

func (p *RateLimitPlugin) PreHook(ctx *context.Context, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.PluginShortCircuit, error) {
    userID := p.extractUserID(*ctx)

    p.mu.Lock()
    count := p.requests[userID]
    if count >= p.limit {
        p.mu.Unlock()

        // Rate limit exceeded - short circuit
        return req, &schemas.PluginShortCircuit{
            Error: &schemas.BifrostError{
                StatusCode: &[]int{429}[0],
                Error: schemas.ErrorField{
                    Message: "Rate limit exceeded",
                },
            },
        }, nil
    }

    p.requests[userID] = count + 1
    p.mu.Unlock()

    return req, nil, nil
}
```

### **Response Caching Plugin**

```go
type CachePlugin struct {
    cache map[string]*schemas.BifrostResponse
    mu    sync.RWMutex
}

func (p *CachePlugin) PreHook(ctx *context.Context, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.PluginShortCircuit, error) {
    cacheKey := p.generateCacheKey(req)

    p.mu.RLock()
    cached, exists := p.cache[cacheKey]
    p.mu.RUnlock()

    if exists {
        // Return cached response - short circuit
        return req, &schemas.PluginShortCircuit{
            Response: cached,
        }, nil
    }

    return req, nil, nil
}

func (p *CachePlugin) PostHook(ctx *context.Context, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
    if result != nil {
        cacheKey := p.generateCacheKeyFromResponse(result)

        p.mu.Lock()
        p.cache[cacheKey] = result
        p.mu.Unlock()
    }

    return result, err, nil
}
```

---

## 📖 Learn More

For advanced plugin development and complete examples:

- **[🏗️ Plugin Architecture](../../architecture/README.md)** - Understanding plugin system design (essential for new plugin development)
- **[🛠️ Plugin Development Guide](../../contributing/README.md)** - Step-by-step guide to building custom plugins
- **[📦 Plugin Store](https://github.com/maximhq/bifrost/tree/main/plugins)** - Ready-to-use community plugins

### **Using Plugins**

```go
// Add plugins to your Bifrost client
client, initErr := bifrost.Init(schemas.BifrostConfig{
    Account: &MyAccount{},
    Plugins: []schemas.Plugin{
        NewLoggingPlugin(),
        NewRateLimitPlugin(100), // 100 requests per user
        NewCachePlugin(time.Hour), // 1 hour cache
    },
})
defer client.Cleanup() // Calls Cleanup() on all plugins
```

> **⚡ Plugin Order:** Plugins execute in the order they're added. PreHooks run forward, PostHooks run in reverse order.
