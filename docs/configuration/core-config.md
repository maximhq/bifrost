# 🔧 Go Package Configuration

Complete configuration guide for Bifrost Go package integration with detailed examples, best practices, and advanced configuration patterns.

## 📋 Overview

The Bifrost Go package uses a type-safe configuration system built around three core interfaces:

- **[Account Interface](#account-interface)** - Provider and key management (Required)
- **[BifrostConfig](#bifrost-config)** - Client initialization options (Required)
- **[Provider Configuration](#provider-configuration)** - Per-provider settings (Optional)

**Benefits:**

- ✅ **Type Safety** - Compile-time validation of configurations
- ✅ **Flexibility** - Implement custom logic for key/config management
- ✅ **Dynamic Updates** - Change configurations at runtime
- ✅ **Environment Aware** - Different configs per environment
- ✅ **Validation** - Built-in configuration validation

---

## 🏗️ Account Interface

The `Account` interface is the heart of Bifrost configuration, providing provider management, API keys, and per-provider settings.

### Interface Definition

```go
type Account interface {
    GetConfiguredProviders() ([]ModelProvider, error)
    GetKeysForProvider(ModelProvider) ([]Key, error)
    GetConfigForProvider(ModelProvider) (*ProviderConfig, error)
}
```

### Basic Implementation

<details open>
<summary><strong>Simple Account Implementation</strong></summary>

```go
package main

import (
    "os"
    "fmt"
    "github.com/maximhq/bifrost/core/schemas"
)

type SimpleAccount struct{}

func (a *SimpleAccount) GetConfiguredProviders() ([]schemas.ModelProvider, error) {
    return []schemas.ModelProvider{
        schemas.OpenAI,
        schemas.Anthropic,
    }, nil
}

func (a *SimpleAccount) GetKeysForProvider(provider schemas.ModelProvider) ([]schemas.Key, error) {
    switch provider {
    case schemas.OpenAI:
        return []schemas.Key{
            {
                Value:  os.Getenv("OPENAI_API_KEY"),
                Models: []string{"gpt-4o-mini", "gpt-4o"},
                Weight: 1.0,
            },
        }, nil
    case schemas.Anthropic:
        return []schemas.Key{
            {
                Value:  os.Getenv("ANTHROPIC_API_KEY"),
                Models: []string{"claude-3-5-sonnet-20241022"},
                Weight: 1.0,
            },
        }, nil
    default:
        return nil, fmt.Errorf("provider %s not configured", provider)
    }
}

func (a *SimpleAccount) GetConfigForProvider(provider schemas.ModelProvider) (*schemas.ProviderConfig, error) {
    return &schemas.ProviderConfig{
        NetworkConfig:            schemas.DefaultNetworkConfig,
        ConcurrencyAndBufferSize: schemas.DefaultConcurrencyAndBufferSize,
    }, nil
}
```

</details>

### Advanced Implementation

<details>
<summary><strong>Production-Ready Account Implementation</strong></summary>

```go
package main

import (
    "os"
    "fmt"
    "sync"
    "time"
    "github.com/maximhq/bifrost/core/schemas"
)

type ProductionAccount struct {
    environment    string
    keyCache      map[schemas.ModelProvider][]schemas.Key
    configCache   map[schemas.ModelProvider]*schemas.ProviderConfig
    cacheMutex    sync.RWMutex
    cacheExpiry   time.Time
    cacheDuration time.Duration
}

func NewProductionAccount() *ProductionAccount {
    return &ProductionAccount{
        environment:   getEnvironment(),
        keyCache:      make(map[schemas.ModelProvider][]schemas.Key),
        configCache:   make(map[schemas.ModelProvider]*schemas.ProviderConfig),
        cacheDuration: 5 * time.Minute,
    }
}

func getEnvironment() string {
    env := os.Getenv("ENV")
    if env == "" {
        return "development"
    }
    return env
}

func (a *ProductionAccount) GetConfiguredProviders() ([]schemas.ModelProvider, error) {
    providers := []schemas.ModelProvider{}

    // Add providers based on available environment variables
    if os.Getenv("OPENAI_API_KEY") != "" || os.Getenv("OPENAI_PROD_KEY") != "" {
        providers = append(providers, schemas.OpenAI)
    }
    if os.Getenv("ANTHROPIC_API_KEY") != "" || os.Getenv("ANTHROPIC_PROD_KEY") != "" {
        providers = append(providers, schemas.Anthropic)
    }
    if os.Getenv("AWS_ACCESS_KEY_ID") != "" && os.Getenv("AWS_SECRET_ACCESS_KEY") != "" {
        providers = append(providers, schemas.Bedrock)
    }
    if os.Getenv("AZURE_OPENAI_ENDPOINT") != "" && os.Getenv("AZURE_OPENAI_API_KEY") != "" {
        providers = append(providers, schemas.Azure)
    }

    if len(providers) == 0 {
        return nil, fmt.Errorf("no providers configured - check environment variables")
    }

    return providers, nil
}

func (a *ProductionAccount) GetKeysForProvider(provider schemas.ModelProvider) ([]schemas.Key, error) {
    // Check cache first
    a.cacheMutex.RLock()
    if time.Now().Before(a.cacheExpiry) {
        if keys, exists := a.keyCache[provider]; exists {
            a.cacheMutex.RUnlock()
            return keys, nil
        }
    }
    a.cacheMutex.RUnlock()

    // Load keys based on environment and provider
    keys, err := a.loadKeysForProvider(provider)
    if err != nil {
        return nil, err
    }

    // Cache the result
    a.cacheMutex.Lock()
    a.keyCache[provider] = keys
    a.cacheExpiry = time.Now().Add(a.cacheDuration)
    a.cacheMutex.Unlock()

    return keys, nil
}

func (a *ProductionAccount) loadKeysForProvider(provider schemas.ModelProvider) ([]schemas.Key, error) {
    switch provider {
    case schemas.OpenAI:
        return a.getOpenAIKeys()
    case schemas.Anthropic:
        return a.getAnthropicKeys()
    case schemas.Bedrock:
        return a.getBedrockKeys()
    case schemas.Azure:
        return a.getAzureKeys()
    default:
        return nil, fmt.Errorf("unsupported provider: %s", provider)
    }
}

func (a *ProductionAccount) getOpenAIKeys() ([]schemas.Key, error) {
    switch a.environment {
    case "production":
        return []schemas.Key{
            {
                Value:  getRequiredEnv("OPENAI_PROD_KEY_1"),
                Models: []string{"gpt-4o", "gpt-4o-mini"},
                Weight: 0.7,
            },
            {
                Value:  getRequiredEnv("OPENAI_PROD_KEY_2"),
                Models: []string{"gpt-4o"},
                Weight: 0.3,
            },
        }, nil
    case "staging":
        return []schemas.Key{
            {
                Value:  getRequiredEnv("OPENAI_STAGING_KEY"),
                Models: []string{"gpt-4o-mini"},
                Weight: 1.0,
            },
        }, nil
    default: // development
        return []schemas.Key{
            {
                Value:  getRequiredEnv("OPENAI_API_KEY"),
                Models: []string{"gpt-4o-mini"},
                Weight: 1.0,
            },
        }, nil
    }
}

func (a *ProductionAccount) getAnthropicKeys() ([]schemas.Key, error) {
    switch a.environment {
    case "production":
        return []schemas.Key{
            {
                Value:  getRequiredEnv("ANTHROPIC_PROD_KEY"),
                Models: []string{"claude-3-5-sonnet-20241022"},
                Weight: 1.0,
            },
        }, nil
    default:
        return []schemas.Key{
            {
                Value:  getRequiredEnv("ANTHROPIC_API_KEY"),
                Models: []string{"claude-3-5-sonnet-20241022"},
                Weight: 1.0,
            },
        }, nil
    }
}

func (a *ProductionAccount) getBedrockKeys() ([]schemas.Key, error) {
    return []schemas.Key{
        {
            Value:  getRequiredEnv("AWS_ACCESS_KEY_ID"),
            Models: []string{"anthropic.claude-3-5-sonnet-20241022-v2:0"},
            Weight: 1.0,
        },
    }, nil
}

func (a *ProductionAccount) getAzureKeys() ([]schemas.Key, error) {
    return []schemas.Key{
        {
            Value:  getRequiredEnv("AZURE_OPENAI_API_KEY"),
            Models: []string{"gpt-4", "gpt-35-turbo"},
            Weight: 1.0,
        },
    }, nil
}

func (a *ProductionAccount) GetConfigForProvider(provider schemas.ModelProvider) (*schemas.ProviderConfig, error) {
    // Check cache first
    a.cacheMutex.RLock()
    if time.Now().Before(a.cacheExpiry) {
        if config, exists := a.configCache[provider]; exists {
            a.cacheMutex.RUnlock()
            return config, nil
        }
    }
    a.cacheMutex.RUnlock()

    config := a.buildConfigForProvider(provider)

    // Cache the result
    a.cacheMutex.Lock()
    a.configCache[provider] = config
    a.cacheMutex.Unlock()

    return config, nil
}

func (a *ProductionAccount) buildConfigForProvider(provider schemas.ModelProvider) *schemas.ProviderConfig {
    baseConfig := &schemas.ProviderConfig{
        NetworkConfig:            a.getNetworkConfig(),
        ConcurrencyAndBufferSize: a.getConcurrencyConfig(),
    }

    // Provider-specific configurations
    switch provider {
    case schemas.Azure:
        baseConfig.MetaConfig = map[string]interface{}{
            "endpoint":   getRequiredEnv("AZURE_OPENAI_ENDPOINT"),
            "api_version": os.Getenv("AZURE_OPENAI_API_VERSION"),
            "deployments": map[string]string{
                "gpt-4":        "gpt-4-deployment",
                "gpt-35-turbo": "gpt-35-turbo-deployment",
            },
        }
    case schemas.Bedrock:
        baseConfig.MetaConfig = map[string]interface{}{
            "region":            getRequiredEnv("AWS_REGION"),
            "secret_access_key": getRequiredEnv("AWS_SECRET_ACCESS_KEY"),
        }
    }

    return baseConfig
}

func (a *ProductionAccount) getNetworkConfig() schemas.NetworkConfig {
    config := schemas.DefaultNetworkConfig

    // Environment-specific timeouts
    switch a.environment {
    case "production":
        config.DefaultRequestTimeoutInSeconds = 60
        config.MaxRetries = 3
    case "staging":
        config.DefaultRequestTimeoutInSeconds = 45
        config.MaxRetries = 2
    default:
        config.DefaultRequestTimeoutInSeconds = 30
        config.MaxRetries = 1
    }

    // Configure proxy if available
    if proxyURL := os.Getenv("HTTP_PROXY"); proxyURL != "" {
        config.ProxyConfig = &schemas.ProxyConfig{
            Type: schemas.HttpProxy,
            URL:  proxyURL,
        }
    }

    return config
}

func (a *ProductionAccount) getConcurrencyConfig() schemas.ConcurrencyAndBufferSize {
    config := schemas.DefaultConcurrencyAndBufferSize

    // Environment-specific concurrency
    switch a.environment {
    case "production":
        config.Concurrency = 50
        config.BufferSize = 500
    case "staging":
        config.Concurrency = 20
        config.BufferSize = 200
    default:
        config.Concurrency = 10
        config.BufferSize = 100
    }

    return config
}

func getRequiredEnv(key string) string {
    value := os.Getenv(key)
    if value == "" {
        panic(fmt.Sprintf("Required environment variable %s is not set", key))
    }
    return value
}
```

</details>

---

## 🏗️ BifrostConfig

The `BifrostConfig` struct configures the Bifrost client initialization with account, plugins, logging, and performance settings.

### Configuration Structure

```go
type BifrostConfig struct {
    Account            Account      // Required: Account implementation
    Plugins            []Plugin     // Optional: Middleware plugins
    Logger             Logger       // Optional: Custom logger
    InitialPoolSize    int          // Optional: Memory pool size
    DropExcessRequests bool         // Optional: Request overflow behavior
    MCPConfig          *MCPConfig   // Optional: MCP tool integration
}
```

### Basic Configuration

<details open>
<summary><strong>Minimal Configuration</strong></summary>

```go
func main() {
    account := &SimpleAccount{}

    client, err := bifrost.Init(schemas.BifrostConfig{
        Account: account,  // Only required field
    })
    if err != nil {
        panic(err)
    }
    defer client.Cleanup()

    // Client is ready to use with default settings
}
```

</details>

### Advanced Configuration

<details>
<summary><strong>Production Configuration</strong></summary>

```go
func main() {
    // Create production account
    account := NewProductionAccount()

    // Create custom logger
    logger := bifrost.NewDefaultLogger(schemas.LogLevelInfo)

    // Create plugins
    plugins := []schemas.Plugin{
        NewMetricsPlugin(),
        NewRateLimitPlugin(),
    }

    // Create MCP configuration
    mcpConfig := &schemas.MCPConfig{
        ClientConfigs: []schemas.MCPClientConfig{
            {
                Name:           "filesystem",
                ConnectionType: schemas.MCPConnectionTypeSTDIO,
                StdioConfig: &schemas.MCPStdioConfig{
                    Command: "npx",
                    Args:    []string{"@modelcontextprotocol/server-filesystem", "/tmp"},
                },
                ToolsToSkip: []string{"rm", "delete"}, // Skip dangerous operations
            },
        },
    }

    // Initialize with full configuration
    client, err := bifrost.Init(schemas.BifrostConfig{
        Account:            account,
        Plugins:            plugins,
        Logger:             logger,
        InitialPoolSize:    500,                    // Larger pool for high throughput
        DropExcessRequests: true,                   // Drop excess requests under load
        MCPConfig:          mcpConfig,              // Enable MCP tools
    })
    if err != nil {
        panic(err)
    }
    defer client.Cleanup()

    // Client is ready for production use
}
```

</details>

---

## 🔧 Provider Configuration

Per-provider configuration allows fine-tuning of network settings, concurrency, and provider-specific options.

### ProviderConfig Structure

```go
type ProviderConfig struct {
    NetworkConfig            NetworkConfig
    ConcurrencyAndBufferSize ConcurrencyAndBufferSize
    MetaConfig              map[string]interface{}  // Provider-specific settings
}
```

### Network Configuration

<details>
<summary><strong>Network Settings</strong></summary>

```go
func (a *MyAccount) GetConfigForProvider(provider schemas.ModelProvider) (*schemas.ProviderConfig, error) {
    config := &schemas.ProviderConfig{
        NetworkConfig: schemas.NetworkConfig{
            BaseURL:                        "",                    // Use default provider URL
            ExtraHeaders:                   map[string]string{     // Custom headers
                "X-Organization":  "my-org",
                "X-Environment":   "production",
            },
            DefaultRequestTimeoutInSeconds: 60,                    // 60 second timeout
            MaxRetries:                     3,                     // Retry 3 times
            RetryBackoffInitial:            500 * time.Millisecond, // Initial backoff
            RetryBackoffMax:                10 * time.Second,      // Max backoff
            ProxyConfig: &schemas.ProxyConfig{                    // HTTP proxy
                Type: schemas.HttpProxy,
                URL:  "http://proxy.company.com:8080",
            },
        },
    }
    return config, nil
}
```

**Network Configuration Options:**

| Field                            | Description              | Default          |
| -------------------------------- | ------------------------ | ---------------- |
| `BaseURL`                        | Custom provider endpoint | Provider default |
| `ExtraHeaders`                   | Additional HTTP headers  | `{}`             |
| `DefaultRequestTimeoutInSeconds` | Request timeout          | `30`             |
| `MaxRetries`                     | Retry attempts           | `0`              |
| `RetryBackoffInitial`            | Initial retry delay      | `500ms`          |
| `RetryBackoffMax`                | Maximum retry delay      | `5s`             |
| `ProxyConfig`                    | Proxy configuration      | `nil`            |

</details>

### Concurrency Configuration

<details>
<summary><strong>Concurrency Settings</strong></summary>

```go
func (a *MyAccount) GetConfigForProvider(provider schemas.ModelProvider) (*schemas.ProviderConfig, error) {
    config := &schemas.ProviderConfig{
        ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
            Concurrency: 20,   // 20 concurrent requests per provider
            BufferSize:  200,  // Queue up to 200 requests
        },
    }

    // Adjust based on provider capabilities
    switch provider {
    case schemas.OpenAI:
        config.ConcurrencyAndBufferSize.Concurrency = 50  // OpenAI handles more concurrency
    case schemas.Anthropic:
        config.ConcurrencyAndBufferSize.Concurrency = 10  // Anthropic prefers less concurrency
    }

    return config, nil
}
```

**Concurrency Configuration Options:**

| Field         | Description                      | Default | Recommended Range |
| ------------- | -------------------------------- | ------- | ----------------- |
| `Concurrency` | Concurrent requests per provider | `10`    | `5-50`            |
| `BufferSize`  | Request queue size               | `100`   | `50-1000`         |

</details>

### Provider-Specific Meta Configuration

<details>
<summary><strong>Azure OpenAI Configuration</strong></summary>

```go
func (a *MyAccount) GetConfigForProvider(provider schemas.ModelProvider) (*schemas.ProviderConfig, error) {
    if provider == schemas.Azure {
        return &schemas.ProviderConfig{
            NetworkConfig:            schemas.DefaultNetworkConfig,
            ConcurrencyAndBufferSize: schemas.DefaultConcurrencyAndBufferSize,
            MetaConfig: map[string]interface{}{
                "endpoint":   "https://my-resource.openai.azure.com",
                "api_version": "2024-02-15-preview",
                "deployments": map[string]string{
                    "gpt-4":        "my-gpt-4-deployment",
                    "gpt-35-turbo": "my-gpt-35-turbo-deployment",
                },
            },
        }, nil
    }

    return &schemas.ProviderConfig{
        NetworkConfig:            schemas.DefaultNetworkConfig,
        ConcurrencyAndBufferSize: schemas.DefaultConcurrencyAndBufferSize,
    }, nil
}
```

</details>

<details>
<summary><strong>AWS Bedrock Configuration</strong></summary>

```go
func (a *MyAccount) GetConfigForProvider(provider schemas.ModelProvider) (*schemas.ProviderConfig, error) {
    if provider == schemas.Bedrock {
        return &schemas.ProviderConfig{
            NetworkConfig:            schemas.DefaultNetworkConfig,
            ConcurrencyAndBufferSize: schemas.DefaultConcurrencyAndBufferSize,
            MetaConfig: map[string]interface{}{
                "region":             "us-east-1",
                "secret_access_key":  os.Getenv("AWS_SECRET_ACCESS_KEY"),
                "session_token":      os.Getenv("AWS_SESSION_TOKEN"),  // Optional
                "arn":               os.Getenv("BEDROCK_ARN"),         // Optional
            },
        }, nil
    }

    return &schemas.ProviderConfig{
        NetworkConfig:            schemas.DefaultNetworkConfig,
        ConcurrencyAndBufferSize: schemas.DefaultConcurrencyAndBufferSize,
    }, nil
}
```

</details>

<details>
<summary><strong>Google Vertex AI Configuration</strong></summary>

```go
func (a *MyAccount) GetConfigForProvider(provider schemas.ModelProvider) (*schemas.ProviderConfig, error) {
    if provider == schemas.Vertex {
        return &schemas.ProviderConfig{
            NetworkConfig:            schemas.DefaultNetworkConfig,
            ConcurrencyAndBufferSize: schemas.DefaultConcurrencyAndBufferSize,
            MetaConfig: map[string]interface{}{
                "project_id":       os.Getenv("VERTEX_PROJECT_ID"),
                "location":         "us-central1",
                "auth_credentials": os.Getenv("VERTEX_AUTH_CREDENTIALS"),
            },
        }, nil
    }

    return &schemas.ProviderConfig{
        NetworkConfig:            schemas.DefaultNetworkConfig,
        ConcurrencyAndBufferSize: schemas.DefaultConcurrencyAndBufferSize,
    }, nil
}
```

</details>

---

## 🔌 Plugin Configuration

Plugins extend Bifrost functionality with custom middleware for logging, metrics, rate limiting, and more.

### Plugin Interface

```go
type Plugin interface {
    Name() string
    PreHook(ctx context.Context, request *BifrostRequest) (*BifrostRequest, error)
    PostHook(ctx context.Context, response *BifrostResponse) (*BifrostResponse, error)
}
```

### Example Plugin Implementation

<details>
<summary><strong>Metrics Plugin</strong></summary>

```go
import (
    "context"
    "time"
    "github.com/prometheus/client_golang/prometheus"
    "github.com/maximhq/bifrost/core/schemas"
)

type MetricsPlugin struct {
    requestsTotal   *prometheus.CounterVec
    requestDuration *prometheus.HistogramVec
    tokensTotal     *prometheus.CounterVec
}

func NewMetricsPlugin() *MetricsPlugin {
    return &MetricsPlugin{
        requestsTotal: prometheus.NewCounterVec(
            prometheus.CounterOpts{
                Name: "bifrost_requests_total",
                Help: "Total number of requests",
            },
            []string{"provider", "model", "status"},
        ),
        requestDuration: prometheus.NewHistogramVec(
            prometheus.HistogramOpts{
                Name: "bifrost_request_duration_seconds",
                Help: "Request duration in seconds",
            },
            []string{"provider", "model"},
        ),
        tokensTotal: prometheus.NewCounterVec(
            prometheus.CounterOpts{
                Name: "bifrost_tokens_total",
                Help: "Total number of tokens used",
            },
            []string{"provider", "model", "type"},
        ),
    }
}

func (p *MetricsPlugin) Name() string {
    return "metrics"
}

func (p *MetricsPlugin) PreHook(ctx context.Context, request *schemas.BifrostRequest) (*schemas.BifrostRequest, error) {
    // Add start time to context
    ctx = context.WithValue(ctx, "start_time", time.Now())

    // Count the request
    p.requestsTotal.WithLabelValues(
        string(request.Provider),
        request.Model,
        "started",
    ).Inc()

    return request, nil
}

func (p *MetricsPlugin) PostHook(ctx context.Context, response *schemas.BifrostResponse) (*schemas.BifrostResponse, error) {
    // Get start time from context
    startTime, ok := ctx.Value("start_time").(time.Time)
    if ok {
        duration := time.Since(startTime)
        p.requestDuration.WithLabelValues(
            string(response.ExtraFields.Provider),
            response.Model,
        ).Observe(duration.Seconds())
    }

    // Count completion
    p.requestsTotal.WithLabelValues(
        string(response.ExtraFields.Provider),
        response.Model,
        "completed",
    ).Inc()

    // Count tokens
    if response.Usage.PromptTokens > 0 {
        p.tokensTotal.WithLabelValues(
            string(response.ExtraFields.Provider),
            response.Model,
            "prompt",
        ).Add(float64(response.Usage.PromptTokens))
    }

    if response.Usage.CompletionTokens > 0 {
        p.tokensTotal.WithLabelValues(
            string(response.ExtraFields.Provider),
            response.Model,
            "completion",
        ).Add(float64(response.Usage.CompletionTokens))
    }

    return response, nil
}

// Register metrics with Prometheus
func (p *MetricsPlugin) RegisterMetrics(registry *prometheus.Registry) {
    registry.MustRegister(p.requestsTotal)
    registry.MustRegister(p.requestDuration)
    registry.MustRegister(p.tokensTotal)
}
```

</details>

<details>
<summary><strong>Rate Limiting Plugin</strong></summary>

```go
import (
    "context"
    "fmt"
    "sync"
    "time"
    "golang.org/x/time/rate"
    "github.com/maximhq/bifrost/core/schemas"
)

type RateLimitPlugin struct {
    limiters map[string]*rate.Limiter
    mutex    sync.RWMutex
}

func NewRateLimitPlugin() *RateLimitPlugin {
    return &RateLimitPlugin{
        limiters: make(map[string]*rate.Limiter),
    }
}

func (p *RateLimitPlugin) Name() string {
    return "rate_limit"
}

func (p *RateLimitPlugin) PreHook(ctx context.Context, request *schemas.BifrostRequest) (*schemas.BifrostRequest, error) {
    limiter := p.getLimiter(string(request.Provider))

    if !limiter.Allow() {
        return nil, fmt.Errorf("rate limit exceeded for provider %s", request.Provider)
    }

    return request, nil
}

func (p *RateLimitPlugin) PostHook(ctx context.Context, response *schemas.BifrostResponse) (*schemas.BifrostResponse, error) {
    // No post-processing needed for rate limiting
    return response, nil
}

func (p *RateLimitPlugin) getLimiter(provider string) *rate.Limiter {
    p.mutex.RLock()
    limiter, exists := p.limiters[provider]
    p.mutex.RUnlock()

    if exists {
        return limiter
    }

    p.mutex.Lock()
    defer p.mutex.Unlock()

    // Check again in case another goroutine created it
    if limiter, exists := p.limiters[provider]; exists {
        return limiter
    }

    // Create new limiter (10 requests per second with burst of 20)
    limiter = rate.NewLimiter(rate.Limit(10), 20)
    p.limiters[provider] = limiter

    return limiter
}
```

</details>

### Using Plugins

```go
func main() {
    account := &MyAccount{}

    // Create plugins
    metricsPlugin := NewMetricsPlugin()
    rateLimitPlugin := NewRateLimitPlugin()

    // Initialize Bifrost with plugins
    client, err := bifrost.Init(schemas.BifrostConfig{
        Account: account,
        Plugins: []schemas.Plugin{
            metricsPlugin,
            rateLimitPlugin,
        },
        Logger: bifrost.NewDefaultLogger(schemas.LogLevelInfo),
    })
    if err != nil {
        panic(err)
    }
    defer client.Cleanup()

    // Register metrics (if using Prometheus)
    registry := prometheus.NewRegistry()
    metricsPlugin.RegisterMetrics(registry)

    // Plugins will automatically intercept all requests
    response, err := client.ChatCompletion(ctx, request)
}
```

---

## 🗂️ Memory Configuration

Optimize memory usage and performance with memory pool configuration.

### Memory Pool Settings

<details>
<summary><strong>Memory Pool Configuration</strong></summary>

```go
func main() {
    account := &MyAccount{}

    client, err := bifrost.Init(schemas.BifrostConfig{
        Account:         account,
        InitialPoolSize: 500,  // Larger pool for high-throughput applications
    })
    if err != nil {
        panic(err)
    }
    defer client.Cleanup()
}
```

**Pool Size Guidelines:**

| Application Type       | Recommended Pool Size | Memory Usage |
| ---------------------- | --------------------- | ------------ |
| **Low Traffic**        | `50-100`              | ~1-2 MB      |
| **Medium Traffic**     | `200-300`             | ~4-6 MB      |
| **High Traffic**       | `500-1000`            | ~10-20 MB    |
| **Ultra High Traffic** | `1000+`               | ~20+ MB      |

</details>

### Request Overflow Behavior

<details>
<summary><strong>Drop Excess Requests</strong></summary>

```go
func main() {
    account := &MyAccount{}

    client, err := bifrost.Init(schemas.BifrostConfig{
        Account:            account,
        DropExcessRequests: true,  // Drop requests when buffers are full
    })
    if err != nil {
        panic(err)
    }
    defer client.Cleanup()

    // Under high load, excess requests will be dropped instead of queued
    // This prevents memory accumulation and improves response times
}
```

**When to use `DropExcessRequests`:**

- ✅ **High traffic applications** - Prevent memory buildup
- ✅ **Real-time systems** - Prefer fast failures over slow responses
- ✅ **Load testing** - Avoid artificial queuing effects
- ❌ **Critical requests** - When every request must be processed

</details>

---

## 📚 Configuration Examples

### Development Environment

<details>
<summary><strong>Development Configuration</strong></summary>

```go
func NewDevelopmentAccount() *DevelopmentAccount {
    return &DevelopmentAccount{}
}

type DevelopmentAccount struct{}

func (a *DevelopmentAccount) GetConfiguredProviders() ([]schemas.ModelProvider, error) {
    return []schemas.ModelProvider{schemas.OpenAI}, nil
}

func (a *DevelopmentAccount) GetKeysForProvider(provider schemas.ModelProvider) ([]schemas.Key, error) {
    return []schemas.Key{
        {
            Value:  os.Getenv("OPENAI_API_KEY"),
            Models: []string{"gpt-4o-mini"},  // Use cheaper model for dev
            Weight: 1.0,
        },
    }, nil
}

func (a *DevelopmentAccount) GetConfigForProvider(provider schemas.ModelProvider) (*schemas.ProviderConfig, error) {
    return &schemas.ProviderConfig{
        NetworkConfig: schemas.NetworkConfig{
            DefaultRequestTimeoutInSeconds: 30,
            MaxRetries:                     1,  // Fail fast in development
        },
        ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
            Concurrency: 5,   // Lower concurrency for dev
            BufferSize:  50,
        },
    }, nil
}

func main() {
    client, err := bifrost.Init(schemas.BifrostConfig{
        Account:         NewDevelopmentAccount(),
        Logger:          bifrost.NewDefaultLogger(schemas.LogLevelDebug), // Verbose logging
        InitialPoolSize: 50,   // Smaller pool for dev
    })
}
```

</details>

### Staging Environment

<details>
<summary><strong>Staging Configuration</strong></summary>

```go
func NewStagingAccount() *StagingAccount {
    return &StagingAccount{}
}

type StagingAccount struct{}

func (a *StagingAccount) GetConfiguredProviders() ([]schemas.ModelProvider, error) {
    return []schemas.ModelProvider{
        schemas.OpenAI,
        schemas.Anthropic,  // Test fallbacks in staging
    }, nil
}

func (a *StagingAccount) GetKeysForProvider(provider schemas.ModelProvider) ([]schemas.Key, error) {
    switch provider {
    case schemas.OpenAI:
        return []schemas.Key{
            {
                Value:  os.Getenv("OPENAI_STAGING_KEY"),
                Models: []string{"gpt-4o-mini", "gpt-4o"},
                Weight: 1.0,
            },
        }, nil
    case schemas.Anthropic:
        return []schemas.Key{
            {
                Value:  os.Getenv("ANTHROPIC_STAGING_KEY"),
                Models: []string{"claude-3-5-sonnet-20241022"},
                Weight: 1.0,
            },
        }, nil
    }
    return nil, fmt.Errorf("provider not configured")
}

func (a *StagingAccount) GetConfigForProvider(provider schemas.ModelProvider) (*schemas.ProviderConfig, error) {
    return &schemas.ProviderConfig{
        NetworkConfig: schemas.NetworkConfig{
            DefaultRequestTimeoutInSeconds: 45,
            MaxRetries:                     2,  // More retries than dev
        },
        ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
            Concurrency: 15,  // Medium concurrency
            BufferSize:  150,
        },
    }, nil
}

func main() {
    client, err := bifrost.Init(schemas.BifrostConfig{
        Account:         NewStagingAccount(),
        Logger:          bifrost.NewDefaultLogger(schemas.LogLevelInfo),
        InitialPoolSize: 200,  // Medium pool size
    })
}
```

</details>

### Production Environment

<details>
<summary><strong>Production Configuration</strong></summary>

```go
// Use the ProductionAccount from the advanced example above

func main() {
    // Create production plugins
    metricsPlugin := NewMetricsPlugin()
    rateLimitPlugin := NewRateLimitPlugin()

    client, err := bifrost.Init(schemas.BifrostConfig{
        Account: NewProductionAccount(),
        Plugins: []schemas.Plugin{
            metricsPlugin,
            rateLimitPlugin,
        },
        Logger:             bifrost.NewDefaultLogger(schemas.LogLevelWarn), // Less verbose logging
        InitialPoolSize:    1000,  // Large pool for production
        DropExcessRequests: true,  // Drop excess requests under load
    })
    if err != nil {
        panic(err)
    }
    defer client.Cleanup()

    // Register metrics with Prometheus
    registry := prometheus.NewRegistry()
    metricsPlugin.RegisterMetrics(registry)

    // Set up HTTP server for metrics
    http.Handle("/metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))
    go http.ListenAndServe(":9090", nil)

    // Production client is ready
}
```

</details>

---

## 📚 Related Documentation

- **[🔗 Provider Configurations](../features/providers.md)** - Provider-specific settings
- **[🔑 Key Management](../features/key-management.md)** - API key strategies
- **[🔌 Plugin System](../features/plugins.md)** - Plugin development
- **[📊 Observability](../features/observability.md)** - Monitoring and metrics
- **[⚡ Memory Management](../features/memory-management.md)** - Performance tuning

---

**Need help?** Check our [❓ FAQ](../guides/faq.md) or [🔧 Troubleshooting Guide](../guides/troubleshooting.md).
