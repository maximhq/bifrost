# 🔧 Troubleshooting Guide

Complete troubleshooting guide for common Bifrost issues, debugging techniques, and solutions.

## 📑 Table of Contents

- [🚨 Common Issues](#-common-issues)
- [🏗️ Setup & Configuration](#️-setup--configuration)
- [🌐 Network & Connectivity](#-network--connectivity)
- [🔑 API Key & Authentication](#-api-key--authentication)
- [📊 Performance Issues](#-performance-issues)
- [🔍 Debugging Techniques](#-debugging-techniques)

---

## 🚨 Common Issues

### Quick Diagnostics

Run these commands to quickly identify common issues:

<details>
<summary><strong>🔧 Go Package - Health Check</strong></summary>

```go
// Test basic Bifrost functionality
func healthCheck() {
    account := &MyAccount{}

    client, err := bifrost.Init(schemas.BifrostConfig{
        Account: account,
        Logger:  bifrost.NewDefaultLogger(schemas.LogLevelDebug),
    })
    if err != nil {
        log.Fatalf("Failed to initialize Bifrost: %v", err)
    }
    defer client.Cleanup()

    // Test simple request
    result, err := client.ChatCompletionRequest(context.Background(), &schemas.BifrostRequest{
        Provider: schemas.OpenAI,
        Model:    "gpt-4o-mini",
        Input: schemas.RequestInput{
            ChatCompletionInput: &[]schemas.BifrostMessage{
                {Role: "user", Content: "Hello, world!"},
            },
        },
    })

    if err != nil {
        log.Printf("Health check failed: %v", err)
    } else {
        log.Printf("Health check passed: %s", result.Output.Message)
    }
}
```

</details>

<details>
<summary><strong>🌐 HTTP Transport - Health Check</strong></summary>

```bash
# Test HTTP transport connectivity
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "provider": "openai",
    "model": "gpt-4o-mini",
    "messages": [
      {"role": "user", "content": "Hello, world!"}
    ]
  }'

# Check server status
curl http://localhost:8080/health

# Check configuration
curl http://localhost:8080/v1/config/validate
```

</details>

---

## 🏗️ Setup & Configuration

### Bifrost Won't Start

| Issue                     | Symptoms                          | Solution                                |
| ------------------------- | --------------------------------- | --------------------------------------- |
| **Missing Configuration** | `panic: account is required`      | Implement `Account` interface properly  |
| **Invalid JSON Config**   | `invalid character` error         | Validate JSON with `jq . config.json`   |
| **Missing API Keys**      | `failed to get keys for provider` | Set required environment variables      |
| **Port Already in Use**   | `bind: address already in use`    | Change port or kill conflicting process |

<details>
<summary><strong>🔧 Configuration Validation</strong></summary>

```go
// Validate account implementation
func validateAccount(account schemas.Account) error {
    providers, err := account.GetConfiguredProviders()
    if err != nil {
        return fmt.Errorf("GetConfiguredProviders failed: %v", err)
    }

    for _, provider := range providers {
        keys, err := account.GetKeysForProvider(provider)
        if err != nil {
            return fmt.Errorf("GetKeysForProvider failed for %s: %v", provider, err)
        }

        if len(keys) == 0 {
            return fmt.Errorf("no keys configured for provider %s", provider)
        }

        config, err := account.GetConfigForProvider(provider)
        if err != nil {
            return fmt.Errorf("GetConfigForProvider failed for %s: %v", provider, err)
        }

        if config == nil {
            return fmt.Errorf("no config returned for provider %s", provider)
        }
    }

    return nil
}
```

</details>

### Environment Variable Issues

<details>
<summary><strong>🌍 Environment Variable Debugging</strong></summary>

```bash
# Check if environment variables are set
echo "OPENAI_API_KEY: ${OPENAI_API_KEY:0:10}..."
echo "ANTHROPIC_API_KEY: ${ANTHROPIC_API_KEY:0:15}..."

# List all Bifrost-related environment variables
env | grep -E "(OPENAI|ANTHROPIC|AZURE|BEDROCK|VERTEX|COHERE|MISTRAL|OLLAMA)"

# Test configuration loading
python -c "
import os
import json

# Test environment variable resolution
config = {
    'test_key': 'env.OPENAI_API_KEY'
}

if config['test_key'].startswith('env.'):
    env_var = config['test_key'][4:]
    value = os.getenv(env_var)
    if value:
        print(f'✅ {env_var}: {value[:10]}...')
    else:
        print(f'❌ {env_var}: Not set')
"
```

**Common Environment Variable Patterns:**

```bash
# Standard provider keys
export OPENAI_API_KEY="sk-..."
export ANTHROPIC_API_KEY="sk-ant-..."
export COHERE_API_KEY="..."
export MISTRAL_API_KEY="..."

# Cloud provider configurations
export AWS_REGION="us-east-1"
export AWS_SECRET_ACCESS_KEY="..."
export AZURE_ENDPOINT="https://your-resource.openai.azure.com"
export VERTEX_PROJECT_ID="your-project"

# Local configurations
export OLLAMA_BASE_URL="http://localhost:11434"
```

</details>

---

## 🌐 Network & Connectivity

### Connection Issues

| Issue                  | Symptoms                    | Solution                                    |
| ---------------------- | --------------------------- | ------------------------------------------- |
| **Provider Timeout**   | `request timeout`           | Increase `DefaultRequestTimeoutInSeconds`   |
| **Connection Refused** | `connection refused`        | Check provider URL and network connectivity |
| **SSL/TLS Errors**     | `certificate verify failed` | Check certificates or proxy configuration   |
| **Proxy Issues**       | `proxy connection failed`   | Verify proxy URL and credentials            |

<details>
<summary><strong>🌐 Network Debugging</strong></summary>

```bash
# Test provider connectivity
curl -v https://api.openai.com/v1/models \
  -H "Authorization: Bearer $OPENAI_API_KEY"

# Test with proxy
curl -v --proxy http://proxy.company.com:8080 \
  https://api.openai.com/v1/models \
  -H "Authorization: Bearer $OPENAI_API_KEY"

# Check DNS resolution
nslookup api.openai.com
nslookup api.anthropic.com

# Test local Ollama
curl http://localhost:11434/api/tags
```

**Enable Debug Logging:**

```go
// Go Package
client, err := bifrost.Init(schemas.BifrostConfig{
    Account: account,
    Logger:  bifrost.NewDefaultLogger(schemas.LogLevelDebug),
})
```

**Debug Log Examples:**

```
DEBUG: HTTP request to https://api.openai.com/v1/chat/completions
DEBUG: Request headers: Authorization: [REDACTED], Content-Type: application/json
DEBUG: Proxy configured: http://proxy.company.com:8080
DEBUG: Request timeout: 30s
DEBUG: Response status: 200 OK
DEBUG: Response time: 1.234s
```

</details>

### Rate Limiting Issues

<details>
<summary><strong>⏱️ Rate Limit Debugging</strong></summary>

**Symptoms:**

- HTTP 429 errors
- `rate_limit_exceeded` errors
- Slow response times

**Solutions:**

```go
// Increase retry configuration
schemas.NetworkConfig{
    MaxRetries:          3,
    RetryBackoffInitial: 1 * time.Second,    // Longer initial backoff
    RetryBackoffMax:     30 * time.Second,   // Longer max backoff
}

// Use multiple API keys with weights
func (a *MyAccount) GetKeysForProvider(provider schemas.ModelProvider) ([]schemas.Key, error) {
    return []schemas.Key{
        {Value: os.Getenv("OPENAI_API_KEY_1"), Weight: 0.5},
        {Value: os.Getenv("OPENAI_API_KEY_2"), Weight: 0.3},
        {Value: os.Getenv("OPENAI_API_KEY_3"), Weight: 0.2},
    }, nil
}

// Enable fallbacks
fallbacks := []schemas.Fallback{
    {Provider: schemas.Anthropic, Model: "claude-3-sonnet-20240229"},
    {Provider: schemas.Cohere, Model: "command-a-03-2025"},
}
```

</details>

---

## 🔑 API Key & Authentication

### Authentication Failures

| Issue                        | Symptoms                | Solution                          |
| ---------------------------- | ----------------------- | --------------------------------- |
| **Invalid API Key**          | `401 Unauthorized`      | Check API key format and validity |
| **Expired Key**              | `401 Unauthorized`      | Rotate to new API key             |
| **Insufficient Permissions** | `403 Forbidden`         | Check model access permissions    |
| **Quota Exceeded**           | `429 Too Many Requests` | Upgrade plan or wait for reset    |

<details>
<summary><strong>🔑 API Key Validation</strong></summary>

```bash
# Test OpenAI key
curl https://api.openai.com/v1/models \
  -H "Authorization: Bearer $OPENAI_API_KEY"

# Test Anthropic key
curl https://api.anthropic.com/v1/messages \
  -H "x-api-key: $ANTHROPIC_API_KEY" \
  -H "Content-Type: application/json" \
  -H "anthropic-version: 2023-06-01" \
  -d '{
    "model": "claude-3-sonnet-20240229",
    "max_tokens": 10,
    "messages": [{"role": "user", "content": "Hello"}]
  }'

# Test Azure endpoint
curl https://your-resource.openai.azure.com/openai/deployments/gpt-4o/chat/completions?api-version=2024-08-01-preview \
  -H "api-key: $AZURE_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "messages": [{"role": "user", "content": "Hello"}],
    "max_tokens": 10
  }'
```

**Key Format Validation:**

```go
func validateAPIKeys() {
    keys := map[string]string{
        "OpenAI":    os.Getenv("OPENAI_API_KEY"),
        "Anthropic": os.Getenv("ANTHROPIC_API_KEY"),
        "Azure":     os.Getenv("AZURE_API_KEY"),
    }

    for provider, key := range keys {
        if key == "" {
            log.Printf("❌ %s: API key not set", provider)
            continue
        }

        switch provider {
        case "OpenAI":
            if !strings.HasPrefix(key, "sk-") {
                log.Printf("❌ %s: Invalid key format (should start with 'sk-')", provider)
            } else {
                log.Printf("✅ %s: Key format valid", provider)
            }
        case "Anthropic":
            if !strings.HasPrefix(key, "sk-ant-") {
                log.Printf("❌ %s: Invalid key format (should start with 'sk-ant-')", provider)
            } else {
                log.Printf("✅ %s: Key format valid", provider)
            }
        }
    }
}
```

</details>

---

## 📊 Performance Issues

### High Latency

| Issue                 | Symptoms              | Solution                                 |
| --------------------- | --------------------- | ---------------------------------------- |
| **Slow Responses**    | High response times   | Increase concurrency, check network path |
| **Queue Buildup**     | Requests waiting      | Increase buffer size or concurrency      |
| **Memory Pressure**   | High GC frequency     | Increase pool size, tune memory settings |
| **Provider Overload** | Intermittent slowness | Distribute load, add fallbacks           |

<details>
<summary><strong>⚡ Performance Debugging</strong></summary>

```go
import (
    "time"
    "log"
)

// Measure request latency
func measureLatency() {
    start := time.Now()

    result, err := client.ChatCompletionRequest(ctx, request)

    latency := time.Since(start)

    if err != nil {
        log.Printf("Request failed after %v: %v", latency, err)
    } else {
        log.Printf("Request succeeded in %v", latency)
    }
}

// Monitor queue depth
func monitorQueues() {
    // This would require custom metrics in your application
    log.Printf("Queue depth: OpenAI=%d, Anthropic=%d", openaiQueue, anthropicQueue)
}
```

**Performance Tuning Checklist:**

- ✅ Increase `Concurrency` for higher throughput
- ✅ Increase `BufferSize` for request spikes
- ✅ Increase `InitialPoolSize` for memory optimization
- ✅ Enable `DropExcessRequests` for overload protection
- ✅ Add multiple API keys for load distribution
- ✅ Configure fallback providers

</details>

### Memory Issues

<details>
<summary><strong>💾 Memory Debugging</strong></summary>

```go
import (
    "runtime"
    "time"
)

func monitorMemory() {
    var m runtime.MemStats

    ticker := time.NewTicker(30 * time.Second)
    defer ticker.Stop()

    for range ticker.C {
        runtime.ReadMemStats(&m)

        log.Printf("Memory Stats:")
        log.Printf("  Alloc: %d KB", m.Alloc/1024)
        log.Printf("  TotalAlloc: %d KB", m.TotalAlloc/1024)
        log.Printf("  Sys: %d KB", m.Sys/1024)
        log.Printf("  NumGC: %d", m.NumGC)

        if m.Alloc > 100*1024*1024 { // 100MB
            log.Printf("WARNING: High memory usage detected")
        }
    }
}
```

**Memory Issue Solutions:**

| Issue                 | Solution                                            |
| --------------------- | --------------------------------------------------- |
| **Memory Leaks**      | Check for unclosed resources, enable debug logging  |
| **High Memory Usage** | Reduce pool size, reduce concurrency                |
| **Frequent GC**       | Increase pool size, optimize allocations            |
| **Out of Memory**     | Enable `DropExcessRequests`, increase instance size |

</details>

---

## 🔍 Debugging Techniques

### Enable Debug Logging

<details>
<summary><strong>📝 Logging Configuration</strong></summary>

```go
// Go Package - Enable debug logging
client, err := bifrost.Init(schemas.BifrostConfig{
    Account: account,
    Logger:  bifrost.NewDefaultLogger(schemas.LogLevelDebug),
})

// Custom logger with file output
logger := bifrost.NewFileLogger("debug.log", schemas.LogLevelDebug)
client, err := bifrost.Init(schemas.BifrostConfig{
    Account: account,
    Logger:  logger,
})
```

**Debug Log Analysis:**

```bash
# Filter debug logs
grep "DEBUG" debug.log | head -20

# Monitor specific provider
grep "openai" debug.log | tail -10

# Check error patterns
grep "ERROR" debug.log | sort | uniq -c

# Monitor retry attempts
grep "retry" debug.log
```

</details>

### Plugin Debugging

<details>
<summary><strong>🔌 Plugin Issues</strong></summary>

**Common Plugin Issues:**

| Issue                  | Symptoms            | Solution                     |
| ---------------------- | ------------------- | ---------------------------- |
| **Plugin Not Called**  | Hooks not executing | Check plugin registration    |
| **Plugin Errors**      | Requests failing    | Add error handling in plugin |
| **Performance Impact** | Slow requests       | Optimize plugin logic        |

**Debug Plugin Execution:**

```go
type DebugPlugin struct{}

func (p *DebugPlugin) PreHook(ctx *context.Context, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.ShortCircuitResponse, error) {
    log.Printf("PreHook called for provider: %s", req.Provider)
    return req, nil, nil
}

func (p *DebugPlugin) PostHook(ctx *context.Context, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
    if err != nil {
        log.Printf("PostHook called with error: %v", err)
    } else {
        log.Printf("PostHook called with success")
    }
    return result, err, nil
}
```

</details>

### MCP Debugging

<details>
<summary><strong>🛠️ MCP Issues</strong></summary>

**Common MCP Issues:**

| Issue                    | Symptoms               | Solution                               |
| ------------------------ | ---------------------- | -------------------------------------- |
| **MCP Client Failed**    | Tools not available    | Check MCP server connectivity          |
| **Tool Execution Fails** | Tool errors            | Verify tool parameters and permissions |
| **STDIO Issues**         | Process spawn failures | Check command path and environment     |

**Debug MCP Setup:**

```bash
# Test MCP server manually
npx -y @modelcontextprotocol/server-filesystem /tmp

# Check MCP client connectivity
curl -X POST http://localhost:3001/mcp \
  -H "Content-Type: application/json" \
  -d '{"method": "tools/list"}'

# Test STDIO MCP tools
echo '{"method": "tools/list"}' | npx -y @modelcontextprotocol/server-filesystem /tmp
```

</details>

### Request/Response Debugging

<details>
<summary><strong>📊 Request Analysis</strong></summary>

```go
// Log request details
func debugRequest(req *schemas.BifrostRequest) {
    log.Printf("Request Details:")
    log.Printf("  Provider: %s", req.Provider)
    log.Printf("  Model: %s", req.Model)
    log.Printf("  Messages: %d", len(*req.Input.ChatCompletionInput))

    if req.Params != nil {
        log.Printf("  Temperature: %v", req.Params.Temperature)
        log.Printf("  MaxTokens: %v", req.Params.MaxTokens)
    }

    if len(req.Fallbacks) > 0 {
        log.Printf("  Fallbacks: %d configured", len(req.Fallbacks))
    }
}

// Log response details
func debugResponse(resp *schemas.BifrostResponse) {
    log.Printf("Response Details:")
    log.Printf("  Status: Success")
    log.Printf("  Model: %s", resp.Model)
    log.Printf("  Provider: %s", resp.ExtraFields.Provider)
    log.Printf("  Usage: %+v", resp.Usage)

    if resp.ExtraFields.FallbackUsed {
        log.Printf("  Fallback: Used")
    }
}
```

</details>

---

## 🆘 Getting Help

### Support Channels

- **[GitHub Issues](https://github.com/maximhq/bifrost/issues)** - Bug reports and feature requests
- **[GitHub Discussions](https://github.com/maximhq/bifrost/discussions)** - Community support
- **[Documentation](../README.md)** - Complete documentation
- **[FAQ](faq.md)** - Frequently asked questions

### Bug Reports

When reporting issues, include:

1. **Bifrost version** and configuration
2. **Error messages** with full stack traces
3. **Debug logs** (with sensitive data removed)
4. **Minimal reproduction** steps
5. **Environment details** (OS, Go version, etc.)

### Performance Issues

For performance problems, include:

1. **Load characteristics** (RPS, request size)
2. **Resource usage** (CPU, memory, network)
3. **Configuration** (pool sizes, concurrency)
4. **Benchmark results** before and after changes

---

**Need immediate help?** Check our [❓ FAQ](faq.md) for quick answers to common questions.
