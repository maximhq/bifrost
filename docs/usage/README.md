# 📖 API Reference

Complete API documentation for both Bifrost usage modes with detailed examples, schemas, and error handling.

## 🎯 Choose Your Integration Mode

| Mode                  | Best For                            | Documentation                      |
| --------------------- | ----------------------------------- | ---------------------------------- |
| **🔧 Go Package**     | Direct integration, maximum control | [📖 Go Package API →](go-package/) |
| **🌐 HTTP Transport** | Language-agnostic, microservices    | [📖 HTTP API →](http-api/)         |

---

## 🔧 Go Package API

Direct Go integration with type-safe interfaces and advanced configuration options.

### Core Components

- **[🏠 Bifrost Client](go-package/bifrost-client.md)** - Main client methods and lifecycle
- **[📋 Schemas & Types](go-package/schemas.md)** - Data structures and interfaces
- **[🔗 Provider APIs](go-package/providers.md)** - Provider-specific configurations
- **[💡 Examples](go-package/examples.md)** - Complete usage examples

### Key Features

✅ **Type Safety** - Full Go type system integration  
✅ **Memory Pooling** - High-performance object reuse  
✅ **Plugin System** - Extensible middleware architecture  
✅ **Direct Control** - Fine-grained configuration options

```go
// Quick example
client, err := bifrost.Init(schemas.BifrostConfig{
    Account: &myAccount,
    Logger:  bifrost.NewDefaultLogger(schemas.LogLevelInfo),
})

response, err := client.ChatCompletion(ctx, schemas.BifrostRequest{
    Provider: schemas.OpenAI,
    Model:    "gpt-4o-mini",
    Input:    requestInput,
})
```

---

## 🌐 HTTP API

RESTful API with OpenAPI specification for language-agnostic integration.

### Endpoint Categories

- **[🏠 Core Endpoints](http-api/endpoints.md)** - Native Bifrost API endpoints
- **[🔄 Integration APIs](http-api/integrations.md)** - Provider-compatible endpoints
- **[📋 OpenAPI Spec](http-api/openapi.json)** - Machine-readable specification
- **[💡 Examples](http-api/examples.md)** - Complete request/response examples

### Provider Integrations

✅ **Drop-in Replacement** - Compatible with existing provider SDKs  
✅ **Zero Code Changes** - Replace URLs only  
✅ **Full Feature Parity** - All Bifrost features available  
✅ **Unified Monitoring** - Single observability layer

```bash
# Quick example - OpenAI Compatible
curl -X POST http://localhost:8080/openai/chat/completions \
  -H "Authorization: Bearer $OPENAI_API_KEY" \
  -d '{"model": "gpt-4o-mini", "messages": [...]}'
```

---

## ❌ Error Handling

Comprehensive error management for both Go package and HTTP API usage.

### Error Categories

- **🔧 Client Errors** - Invalid requests, authentication failures
- **🌐 Provider Errors** - Upstream API failures, rate limits
- **⚡ System Errors** - Internal Bifrost errors, resource exhaustion
- **🔄 Fallback Errors** - Fallback chain failures, configuration issues

### Error Response Format

Both Go package and HTTP API use consistent error structures:

<details>
<summary><strong>🔧 Go Package Error Handling</strong></summary>

```go
response, err := client.ChatCompletion(ctx, request)
if err != nil {
    var bifrostErr *schemas.BifrostError
    if errors.As(err, &bifrostErr) {
        log.Printf("Bifrost error: %s (code: %s)",
            bifrostErr.Error.Message,
            *bifrostErr.Error.Code)
    }
}
```

</details>

<details>
<summary><strong>🌐 HTTP API Error Handling</strong></summary>

```bash
# Error response structure
{
  "error": {
    "type": "rate_limit_error",
    "code": "rate_limit_exceeded",
    "message": "Rate limit exceeded for model gpt-4o",
    "param": "model"
  },
  "is_bifrost_error": true,
  "status_code": 429
}
```

</details>

**[📖 Complete Error Reference →](errors.md)**

---

## 🔗 Cross-References

### Quick Navigation

| I Want To...               | Go Package                                        | HTTP API                                           |
| -------------------------- | ------------------------------------------------- | -------------------------------------------------- |
| **Make a chat completion** | [ChatCompletion](go-package/bifrost-client.md)    | [POST /v1/chat/completions](http-api/endpoints.md) |
| **Configure fallbacks**    | [BifrostRequest.Fallbacks](go-package/schemas.md) | [Request body fallbacks](http-api/examples.md)     |
| **Handle tool calls**      | [MCP Integration](go-package/examples.md)         | [Tool execution](http-api/examples.md)             |
| **Replace OpenAI API**     | [Provider switching](go-package/providers.md)     | [OpenAI compatibility](http-api/integrations.md)   |
| **Monitor performance**    | [Observability hooks](go-package/examples.md)     | [Metrics endpoints](http-api/endpoints.md)         |

### Related Documentation

- **[🚀 Quick Start](../quick-start/)** - Get started guides
- **[🎯 Features](../features/)** - Feature documentation
- **[⚙️ Configuration](../configuration/)** - Setup and configuration
- **[🏗️ Architecture](../architecture/)** - System design and performance

---

**Need help?** Check our [❓ FAQ](../guides/faq.md) or [🔧 Troubleshooting Guide](../guides/troubleshooting.md).
