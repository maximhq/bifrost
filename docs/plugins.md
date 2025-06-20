# Bifrost Plugin System

Bifrost provides a powerful plugin system that allows you to extend and customize the request/response pipeline. Plugins can implement rate limiting, caching, authentication, logging, monitoring, and more.

## Table of Contents

1. [Plugin Architecture Overview](#1-plugin-architecture-overview)
2. [Plugin Interface](#2-plugin-interface)
3. [Plugin Lifecycle](#3-plugin-lifecycle)
4. [Plugin Execution Flow](#4-plugin-execution-flow)
5. [Short-Circuit Behavior](#5-short-circuit-behavior)
6. [Error Handling & Fallbacks](#6-error-handling--fallbacks)
7. [Building Custom Plugins](#7-building-custom-plugins)
8. [Plugin Examples](#8-plugin-examples)
9. [Best Practices](#9-best-practices)
10. [Plugin Development Guidelines](#10-plugin-development-guidelines)
11. [Troubleshooting Guide](#11-troubleshooting-guide)
12. [Performance Optimization](#12-performance-optimization)

## 1. Plugin Architecture Overview

Bifrost plugins follow a **PreHook → Provider → PostHook** pattern with support for short-circuiting and fallback control.

### Key Concepts

- **PreHook**: Executed before provider call - can modify requests or short-circuit
- **PostHook**: Executed after provider response - can modify responses or recover from errors
- **Short-Circuit**: Plugin can skip provider call and return response/error directly
- **Fallback Control**: Plugins can control whether fallback providers should be tried
- **Pipeline Symmetry**: Every PreHook execution gets a corresponding PostHook call

## 2. Plugin Interface

```go
type Plugin interface {
    // GetName returns the name of the plugin
    GetName() string

    // PreHook is called before a request is processed by a provider
    // Can modify request, short-circuit with response, or short-circuit with error
    PreHook(ctx *context.Context, req *BifrostRequest) (*BifrostRequest, *PluginShortCircuit, error)

    // PostHook is called after a response or after PreHook short-circuit
    // Can modify response/error or recover from errors
    PostHook(ctx *context.Context, result *BifrostResponse, err *BifrostError) (*BifrostResponse, *BifrostError, error)

    // Cleanup is called on bifrost shutdown
    Cleanup() error
}

type PluginShortCircuit struct {
    Response *BifrostResponse // If set, skip provider and return this response
    Error    *BifrostError    // If set, skip provider and return this error
}
```

## 3. Plugin Lifecycle

```mermaid
stateDiagram-v2
    [*] --> PluginInit: Plugin Creation
    PluginInit --> Registered: Add to BifrostConfig
    Registered --> PreHookCall: Request Received

    PreHookCall --> ModifyRequest: Normal Flow
    PreHookCall --> ShortCircuitResponse: Return Response
    PreHookCall --> ShortCircuitError: Return Error

    ModifyRequest --> ProviderCall: Send to Provider
    ProviderCall --> PostHookCall: Receive Response

    ShortCircuitResponse --> PostHookCall: Skip Provider
    ShortCircuitError --> PostHookCall: Pipeline Symmetry

    PostHookCall --> ModifyResponse: Process Result
    PostHookCall --> RecoverError: Error Recovery
    PostHookCall --> FallbackCheck: Check AllowFallbacks
    PostHookCall --> ResponseReady: Pass Through

    FallbackCheck --> TryFallback: AllowFallbacks=true/nil
    FallbackCheck --> ResponseReady: AllowFallbacks=false
    TryFallback --> PreHookCall: Next Provider

    ModifyResponse --> ResponseReady: Modified
    RecoverError --> ResponseReady: Recovered
    ResponseReady --> [*]: Return to Client

    Registered --> CleanupCall: Bifrost Shutdown
    CleanupCall --> [*]: Plugin Destroyed
```

## 4. Plugin Execution Flow

### Normal Flow (No Short-Circuit)

```mermaid
sequenceDiagram
    participant Client
    participant Bifrost
    participant Plugin1
    participant Plugin2
    participant Provider

    Client->>Bifrost: Request
    Bifrost->>Plugin1: PreHook(request)
    Plugin1-->>Bifrost: modified request
    Bifrost->>Plugin2: PreHook(request)
    Plugin2-->>Bifrost: modified request
    Bifrost->>Provider: API Call
    Provider-->>Bifrost: response
    Bifrost->>Plugin2: PostHook(response)
    Plugin2-->>Bifrost: modified response
    Bifrost->>Plugin1: PostHook(response)
    Plugin1-->>Bifrost: modified response
    Bifrost-->>Client: Final Response
```

### With Short-Circuit Response

```mermaid
sequenceDiagram
    participant Client
    participant Bifrost
    participant Plugin1
    participant Plugin2
    participant Provider

    Client->>Bifrost: Request
    Bifrost->>Plugin1: PreHook(request)
    Plugin1-->>Bifrost: PluginShortCircuit{Response}
    Note over Provider: Provider call skipped
    Bifrost->>Plugin1: PostHook(response)
    Plugin1-->>Bifrost: modified response
    Bifrost-->>Client: Final Response
```

### With Short-Circuit Error (Allow Fallbacks)

```mermaid
sequenceDiagram
    participant Client
    participant Bifrost
    participant Plugin1
    participant Provider1
    participant Provider2

    Client->>Bifrost: Request (Provider1 + Fallback Provider2)
    Bifrost->>Plugin1: PreHook(request)
    Plugin1-->>Bifrost: PluginShortCircuit{Error, AllowFallbacks=true}
    Note over Provider1: Provider1 call skipped
    Bifrost->>Plugin1: PostHook(error)
    Plugin1-->>Bifrost: error unchanged

    Note over Bifrost: Try fallback provider
    Bifrost->>Plugin1: PreHook(request for Provider2)
    Plugin1-->>Bifrost: modified request
    Bifrost->>Provider2: API Call
    Provider2-->>Bifrost: response
    Bifrost->>Plugin1: PostHook(response)
    Plugin1-->>Bifrost: modified response
    Bifrost-->>Client: Final Response
```

### Complex Plugin Decision Flow

```mermaid
graph TD
    A["Client Request"] --> B["Bifrost"]
    B --> C["Auth Plugin PreHook"]
    C --> D{"Authenticated?"}
    D -->|No| E["Return Auth Error<br/>AllowFallbacks=false"]
    D -->|Yes| F["RateLimit Plugin PreHook"]
    F --> G{"Rate Limited?"}
    G -->|Yes| H["Return Rate Error<br/>AllowFallbacks=nil"]
    G -->|No| I["Cache Plugin PreHook"]
    I --> J{"Cache Hit?"}
    J -->|Yes| K["Return Cached Response"]
    J -->|No| L["Provider API Call"]
    L --> M["Cache Plugin PostHook"]
    M --> N["Store in Cache"]
    N --> O["RateLimit Plugin PostHook"]
    O --> P["Auth Plugin PostHook"]
    P --> Q["Final Response"]

    E --> R["Skip Fallbacks"]
    H --> S["Try Fallback Provider"]
    K --> T["Skip Provider Call"]
```

## 5. Short-Circuit Behavior

Plugins can short-circuit the normal flow in two ways:

### 1. Short-Circuit with Response (Success)

```go
func (p *CachePlugin) PreHook(ctx *context.Context, req *BifrostRequest) (*BifrostRequest, *PluginShortCircuit, error) {
    if cachedResponse := p.getFromCache(req); cachedResponse != nil {
        // Return cached response, skip provider call
        return req, &PluginShortCircuit{
            Response: cachedResponse,
        }, nil
    }
    return req, nil, nil
}
```

### 2. Short-Circuit with Error

```go
func (p *AuthPlugin) PreHook(ctx *context.Context, req *BifrostRequest) (*BifrostRequest, *PluginShortCircuit, error) {
    if !p.isAuthenticated(req) {
        // Return error, skip provider call
        return req, &PluginShortCircuit{
            Error: &BifrostError{
                Error: ErrorField{Message: "authentication failed"},
                AllowFallbacks: &false, // Don't try other providers
            },
        }, nil
    }
    return req, nil, nil
}
```

## 6. Error Handling & Fallbacks

When plugins return errors, they control whether Bifrost should try fallback providers:

### AllowFallbacks Control

```go
// Allow fallbacks (default behavior)
&BifrostError{
    Error: ErrorField{Message: "rate limit exceeded"},
    AllowFallbacks: nil, // nil = true by default
}

// Explicitly allow fallbacks
&BifrostError{
    Error: ErrorField{Message: "temporary failure"},
    AllowFallbacks: &true,
}

// Prevent fallbacks
&BifrostError{
    Error: ErrorField{Message: "authentication failed"},
    AllowFallbacks: &false,
}
```

### Fallback Decision Matrix

| Error Type         | AllowFallbacks  | Behavior                                                   |
| ------------------ | --------------- | ---------------------------------------------------------- |
| Rate Limiting      | `nil` or `true` | ✅ Try fallbacks (other providers may not be rate limited) |
| Temporary Failure  | `nil` or `true` | ✅ Try fallbacks (may succeed with different provider)     |
| Authentication     | `false`         | ❌ No fallbacks (fundamental failure)                      |
| Validation Error   | `false`         | ❌ No fallbacks (request is invalid)                       |
| Security Violation | `false`         | ❌ No fallbacks (security concern)                         |

### PostHook Error Recovery

Plugins can recover from errors in PostHook:

```go
func (p *RetryPlugin) PostHook(ctx *context.Context, result *BifrostResponse, err *BifrostError) (*BifrostResponse, *BifrostError, error) {
    if err != nil && p.shouldRetry(err) {
        // Recover by calling provider again
        if retryResponse := p.retry(ctx); retryResponse != nil {
            return retryResponse, nil, nil // Recovered successfully
        }
    }
    return result, err, nil
}
```

## 7. Building Custom Plugins

### Basic Plugin Structure

```go
type CustomPlugin struct {
    config CustomConfig
    // Add your fields here
}

func NewCustomPlugin(config CustomConfig) *CustomPlugin {
    return &CustomPlugin{config: config}
}

func (p *CustomPlugin) GetName() string {
    return "CustomPlugin"
}

func (p *CustomPlugin) PreHook(ctx *context.Context, req *BifrostRequest) (*BifrostRequest, *PluginShortCircuit, error) {
    // Modify request or short-circuit
    return req, nil, nil
}

func (p *CustomPlugin) PostHook(ctx *context.Context, result *BifrostResponse, err *BifrostError) (*BifrostResponse, *BifrostError, error) {
    // Modify response/error or recover from errors
    return result, err, nil
}

func (p *CustomPlugin) Cleanup() error {
    // Clean up resources
    return nil
}
```

### Plugin Development Checklist

- [ ] Handle nil response and error in PostHook
- [ ] Set appropriate AllowFallbacks for errors
- [ ] Implement proper cleanup in Cleanup()
- [ ] Add configuration validation
- [ ] Write comprehensive tests
- [ ] Document behavior and configuration

## 8. Plugin Examples

### Rate Limiting Plugin

```go
type RateLimitPlugin struct {
    limiters map[ModelProvider]*rate.Limiter
    mu       sync.RWMutex
}

func NewRateLimitPlugin(limits map[ModelProvider]float64) *RateLimitPlugin {
    limiters := make(map[ModelProvider]*rate.Limiter)
    for provider, limit := range limits {
        limiters[provider] = rate.NewLimiter(rate.Limit(limit), 1)
    }
    return &RateLimitPlugin{limiters: limiters}
}

func (p *RateLimitPlugin) GetName() string {
    return "RateLimitPlugin"
}

func (p *RateLimitPlugin) PreHook(ctx *context.Context, req *BifrostRequest) (*BifrostRequest, *PluginShortCircuit, error) {
    p.mu.RLock()
    limiter, exists := p.limiters[req.Provider]
    p.mu.RUnlock()

    if exists && !limiter.Allow() {
        // Rate limited - allow fallbacks to other providers
        return req, &PluginShortCircuit{
            Error: &BifrostError{
                Error: ErrorField{
                    Message: fmt.Sprintf("rate limit exceeded for %s", req.Provider),
                },
                AllowFallbacks: nil, // Allow fallbacks by default
            },
        }, nil
    }

    return req, nil, nil
}

func (p *RateLimitPlugin) PostHook(ctx *context.Context, result *BifrostResponse, err *BifrostError) (*BifrostResponse, *BifrostError, error) {
    return result, err, nil
}

func (p *RateLimitPlugin) Cleanup() error {
    return nil
}
```

### Authentication Plugin

```go
type AuthPlugin struct {
    validator TokenValidator
}

func NewAuthPlugin(validator TokenValidator) *AuthPlugin {
    return &AuthPlugin{validator: validator}
}

func (p *AuthPlugin) GetName() string {
    return "AuthPlugin"
}

func (p *AuthPlugin) PreHook(ctx *context.Context, req *BifrostRequest) (*BifrostRequest, *PluginShortCircuit, error) {
    if !p.validator.IsValid(*ctx, req) {
        // Authentication failed - don't try fallbacks
        return req, &PluginShortCircuit{
            Error: &BifrostError{
                Error: ErrorField{
                    Message: "authentication failed",
                    Type:    &authErrorType,
                },
                AllowFallbacks: &false, // Don't try other providers
            },
        }, nil
    }

    return req, nil, nil
}

func (p *AuthPlugin) PostHook(ctx *context.Context, result *BifrostResponse, err *BifrostError) (*BifrostResponse, *BifrostError, error) {
    return result, err, nil
}

func (p *AuthPlugin) Cleanup() error {
    return p.validator.Cleanup()
}
```

### Caching Plugin with Recovery

```go
type CachePlugin struct {
    cache Cache
    ttl   time.Duration
}

func (p *CachePlugin) PreHook(ctx *context.Context, req *BifrostRequest) (*BifrostRequest, *PluginShortCircuit, error) {
    key := p.generateKey(req)
    if cachedResponse := p.cache.Get(key); cachedResponse != nil {
        // Return cached response, skip provider
        return req, &PluginShortCircuit{
            Response: cachedResponse,
        }, nil
    }

    return req, nil, nil
}

func (p *CachePlugin) PostHook(ctx *context.Context, result *BifrostResponse, err *BifrostError) (*BifrostResponse, *BifrostError, error) {
    if result != nil {
        // Cache successful response
        key := p.generateKeyFromResponse(result)
        p.cache.Set(key, result, p.ttl)
    }

    return result, err, nil
}
```

## 9. Best Practices

### Plugin Design

1. **Keep plugins focused** - Each plugin should have a single responsibility
2. **Make plugins configurable** - Use configuration structs for flexibility
3. **Handle edge cases** - Always check for nil values and error conditions
4. **Be mindful of performance** - Plugins add latency to every request

### Error Handling

1. **Default to allowing fallbacks** - Unless the error is fundamental
2. **Use appropriate error types** - Help categorize different failure modes
3. **Provide clear error messages** - Include context about what failed
4. **Consider error recovery** - PostHooks can recover from certain errors

### Resource Management

1. **Implement proper cleanup** - Release resources in Cleanup()
2. **Use context for cancellation** - Respect request timeouts
3. **Avoid memory leaks** - Clean up goroutines and connections
4. **Handle concurrent access** - Use proper synchronization

### Testing

1. **Test all code paths** - Including error conditions and edge cases
2. **Test short-circuit behavior** - Verify responses and error handling
3. **Test fallback control** - Ensure AllowFallbacks works correctly
4. **Test plugin interactions** - Verify behavior with multiple plugins

## 10. Plugin Development Guidelines

### Plugin Structure Requirements

Each plugin should be organized as follows:

```
plugins/
└── your-plugin-name/
    ├── main.go           # Plugin implementation
    ├── plugin_test.go    # Comprehensive tests
    ├── README.md         # Documentation with examples
    └── go.mod            # Module definition
```

### Using Plugins

```go
import (
    "github.com/maximhq/bifrost/core"
    "github.com/your-org/your-plugin"
)

client, err := bifrost.Init(schemas.BifrostConfig{
    Account: &yourAccount,
    Plugins: []schemas.Plugin{
        your_plugin.NewYourPlugin(config),
        // Add more plugins as needed
    },
})
```

### Plugin Execution Order

Plugins execute in the order they are registered:

```go
Plugins: []schemas.Plugin{
    authPlugin,      // PreHook: 1st, PostHook: 3rd
    rateLimitPlugin, // PreHook: 2nd, PostHook: 2nd
    loggingPlugin,   // PreHook: 3rd, PostHook: 1st
}
```

**PreHook Order**: Auth → RateLimit → Logging → Provider  
**PostHook Order**: Provider → Logging → RateLimit → Auth

### Contribution Guidelines

1. **Design Discussion**

   - Open an issue to discuss your plugin idea
   - Explain the use case and design approach
   - Get feedback before implementation

2. **Implementation Standards**

   - Follow Go best practices and conventions
   - Include comprehensive error handling
   - Ensure thread safety where needed
   - Add extensive test coverage (>80%)

3. **Testing Requirements**

   - Unit tests for all functionality
   - Integration tests with Bifrost
   - Test error scenarios and edge cases
   - Test short-circuit behavior
   - Test fallback control

4. **Documentation Standards**
   - Clear, comprehensive README
   - Code comments for complex logic
   - Usage examples
   - Performance characteristics

### Plugin Testing Best Practices

```go
func TestYourPlugin_PreHook(t *testing.T) {
    tests := []struct {
        name           string
        config         YourPluginConfig
        request        *schemas.BifrostRequest
        expectShortCircuit bool
        expectError    bool
        expectFallbacks bool
    }{
        {
            name: "valid request passes through",
            config: YourPluginConfig{EnableFeature: true},
            request: &schemas.BifrostRequest{/* valid request */},
            expectShortCircuit: false,
        },
        {
            name: "invalid request short-circuits with error",
            config: YourPluginConfig{EnableFeature: true},
            request: &schemas.BifrostRequest{/* invalid request */},
            expectShortCircuit: true,
            expectError: true,
            expectFallbacks: false,
        },
        // Add more test cases
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            plugin := NewYourPlugin(tt.config)
            ctx := context.Background()

            req, shortCircuit, err := plugin.PreHook(&ctx, tt.request)

            // Assertions
            if tt.expectError {
                assert.NotNil(t, err)
            } else {
                assert.Nil(t, err)
            }

            if tt.expectShortCircuit {
                assert.NotNil(t, shortCircuit)
                if shortCircuit.Error != nil && shortCircuit.Error.AllowFallbacks != nil {
                    assert.Equal(t, tt.expectFallbacks, *shortCircuit.Error.AllowFallbacks)
                }
            } else {
                assert.Nil(t, shortCircuit)
            }
        })
    }
}
```

## 11. Troubleshooting Guide

### Common Issues

#### 1. Plugin Not Being Called

**Symptoms**: Plugin hooks are never executed  
**Solutions**:

```go
// Ensure plugin is properly registered
client, err := bifrost.Init(schemas.BifrostConfig{
    Account: &account,
    Plugins: []schemas.Plugin{
        yourPlugin, // Make sure it's in the list
    },
})

// Check plugin implements interface correctly
var _ schemas.Plugin = (*YourPlugin)(nil)
```

#### 2. Short-Circuit Not Working

**Symptoms**: Provider is still called despite returning PluginShortCircuit  
**Solutions**:

```go
// Correct: Either Response OR Error, not both
return req, &schemas.PluginShortCircuit{
    Response: cachedResponse, // OR Error, not both
}, nil

// Incorrect: Don't return error with PluginShortCircuit
return req, &schemas.PluginShortCircuit{...}, fmt.Errorf("error")
```

#### 3. Fallback Behavior Not Working

**Symptoms**: Fallbacks not tried when expected, or tried when they shouldn't be  
**Solutions**:

```go
// For PreHook short-circuits, use PluginShortCircuit
return req, &schemas.PluginShortCircuit{
    Error: &schemas.BifrostError{
        Error: schemas.ErrorField{Message: "error"},
        AllowFallbacks: &false, // Explicitly control fallbacks
    },
}, nil
```

#### 4. Memory Leaks

**Solutions**:

```go
func (p *YourPlugin) Cleanup() error {
    // Close channels
    close(p.stopChan)

    // Cancel contexts
    p.cancel()

    // Close connections
    if p.conn != nil {
        p.conn.Close()
    }

    // Wait for goroutines
    p.wg.Wait()

    return nil
}
```

#### 5. Race Conditions

**Solutions**:

```go
type ThreadSafePlugin struct {
    mu    sync.RWMutex
    state map[string]interface{}
}

func (p *ThreadSafePlugin) PreHook(ctx *context.Context, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.PluginShortCircuit, error) {
    p.mu.Lock()
    defer p.mu.Unlock()

    // Safe access to shared state
    p.state[req.ID] = "processing"
    return req, nil, nil
}
```

## 12. Performance Optimization

1. **Minimize Hook Latency**

   - Avoid blocking operations in hooks
   - Use goroutines for background work
   - Cache expensive computations

2. **Efficient Resource Usage**

   - Pool connections and resources
   - Use sync.Pool for frequently allocated objects
   - Implement proper cleanup

3. **Monitor Memory Usage**
   - Profile your plugin under load
   - Watch for memory leaks
   - Use appropriate data structures

## Summary

This documentation provides complete coverage for Bifrost plugin development:

- **Architecture & Lifecycle** - Understanding the plugin system and execution flow
- **Interface & Behavior** - Exact method signatures and short-circuit capabilities
- **Error Handling** - Complete control over fallback behavior with AllowFallbacks
- **Practical Examples** - Real-world plugins for rate limiting, auth, and caching
- **Development Guidelines** - Best practices, testing, and contribution standards
- **Troubleshooting** - Solutions for common issues and performance optimization
