# 🌐 HTTP API Reference

Complete HTTP API documentation with RESTful endpoints, provider integrations, and OpenAPI specification.

## 📋 Quick Navigation

| Category                     | Description                       | Documentation                        |
| ---------------------------- | --------------------------------- | ------------------------------------ |
| **🏠 Core Endpoints**        | Native Bifrost API endpoints      | [📖 Endpoints →](endpoints.md)       |
| **🔄 Provider Integrations** | Drop-in compatible APIs           | [📖 Integrations →](integrations.md) |
| **📋 OpenAPI Spec**          | Machine-readable specification    | [📖 OpenAPI →](openapi.json)         |
| **💡 Examples**              | Complete request/response samples | [📖 Examples →](examples.md)         |

---

## ⚡ Quick Start

### 1. Start Bifrost Server

<details open>
<summary><strong>🐳 Docker (Recommended)</strong></summary>

```bash
# Create config.json
cat > config.json << EOF
{
  "providers": {
    "openai": {
      "keys": [
        {
          "value": "env.OPENAI_API_KEY",
          "models": ["gpt-4o-mini", "gpt-4o"],
          "weight": 1.0
        }
      ]
    }
  }
}
EOF

# Start Bifrost
docker run -p 8080:8080 \
  -v $(pwd)/config.json:/app/config/config.json \
  -e OPENAI_API_KEY \
  maximhq/bifrost
```

</details>

<details>
<summary><strong>📦 Binary</strong></summary>

```bash
# Install binary
go install github.com/maximhq/bifrost/transports/bifrost-http@latest

# Start server
bifrost-http -config config.json -port 8080
```

</details>

### 2. Make Your First Request

```bash
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "provider": "openai",
    "model": "gpt-4o-mini",
    "messages": [
      {"role": "user", "content": "Hello, Bifrost!"}
    ]
  }'
```

---

## 🏠 Native Bifrost API

### Core Endpoints

| Endpoint               | Method | Description                    |
| ---------------------- | ------ | ------------------------------ |
| `/v1/chat/completions` | POST   | Chat completion with fallbacks |
| `/v1/text/completions` | POST   | Text completion                |
| `/v1/mcp/tool/execute` | POST   | Execute MCP tools              |
| `/v1/providers`        | GET    | List configured providers      |
| `/v1/models`           | GET    | List available models          |
| `/metrics`             | GET    | Prometheus metrics             |

### Request Format

**Native Bifrost requests include provider selection:**

```json
{
  "provider": "openai",           // Required: Provider selection
  "model": "gpt-4o-mini",         // Required: Model name
  "messages": [...],              // Required: Chat messages
  "params": {                     // Optional: Model parameters
    "temperature": 0.7,
    "max_tokens": 1000
  },
  "fallbacks": [                  // Optional: Fallback providers
    {"provider": "anthropic", "model": "claude-3-sonnet-20240229"}
  ]
}
```

### Response Format

**Bifrost responses include additional metadata:**

```json
{
  "id": "chatcmpl-123",
  "object": "chat.completion",
  "choices": [
    {
      "message": {
        "role": "assistant",
        "content": "Hello! I'm Bifrost, your AI gateway."
      }
    }
  ],
  "extra_fields": {
    "provider": "openai",         // Which provider responded
    "latency": 1.234,            // Request latency in seconds
    "fallback_used": false,       // Whether fallback was used
    "billed_usage": {...}        // Cost tracking information
  }
}
```

**[📖 Complete Endpoints Reference →](endpoints.md)**

---

## 🔄 Provider-Compatible APIs

Drop-in replacements for existing provider SDKs with zero code changes.

### Available Integrations

| Provider      | Endpoint Pattern               | Authentication | Status |
| ------------- | ------------------------------ | -------------- | ------ |
| **OpenAI**    | `/openai/chat/completions`     | Bearer token   | ✅     |
| **Anthropic** | `/anthropic/v1/messages`       | X-API-Key      | ✅     |
| **Google**    | `/genai/v1beta/models/{model}` | X-API-Key      | ✅     |
| **LiteLLM**   | `/litellm/chat/completions`    | Bearer token   | ✅     |

### OpenAI Compatibility Example

**Replace this:**

```javascript
const openai = new OpenAI({
  baseURL: "https://api.openai.com/v1",
  apiKey: process.env.OPENAI_API_KEY,
});
```

**With this:**

```javascript
const openai = new OpenAI({
  baseURL: "http://localhost:8080/openai", // Only change the URL!
  apiKey: process.env.OPENAI_API_KEY,
});
```

### Benefits of Provider Compatibility

✅ **Zero Code Changes** - Only change the base URL  
✅ **Automatic Fallbacks** - Configure fallbacks via Bifrost config  
✅ **Unified Observability** - Single metrics and logging layer  
✅ **Multi-Provider** - Route to any provider behind the scenes

**[📖 Complete Integration Guide →](integrations.md)**

---

## 🛠️ Advanced Features

### MCP Tool Integration

Execute external tools through the MCP (Model Context Protocol) system:

```bash
# 1. Chat completion with tool calls
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "provider": "openai",
    "model": "gpt-4o-mini",
    "messages": [
      {"role": "user", "content": "List files in /tmp directory"}
    ]
  }'

# Response includes tool_calls
{
  "choices": [{
    "message": {
      "role": "assistant",
      "tool_calls": [{
        "id": "call_123",
        "type": "function",
        "function": {
          "name": "list_files",
          "arguments": "{\"path\": \"/tmp\"}"
        }
      }]
    }
  }]
}

# 2. Execute the tool call
curl -X POST http://localhost:8080/v1/mcp/tool/execute \
  -H "Content-Type: application/json" \
  -d '{
    "id": "call_123",
    "type": "function",
    "function": {
      "name": "list_files",
      "arguments": "{\"path\": \"/tmp\"}"
    }
  }'

# Tool execution result
{
  "role": "tool",
  "content": "file1.txt\nfile2.txt\nconfig.json",
  "tool_call_id": "call_123"
}
```

**[📖 Complete MCP Guide →](../features/mcp-integration.md)**

### Prometheus Metrics

Monitor Bifrost performance with built-in metrics:

```bash
curl http://localhost:8080/metrics
```

**Available Metrics:**

- `http_requests_total` - Total HTTP requests
- `http_request_duration_seconds` - Request latency
- `bifrost_upstream_requests_total` - Provider API calls
- `bifrost_upstream_latency_seconds` - Provider API latency

**[📖 Complete Observability Guide →](../features/observability.md)**

### Custom Headers & Labels

Add custom Prometheus labels via request headers:

```bash
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "x-bf-prom-team-id: platform" \
  -H "x-bf-prom-service: chatbot" \
  -d '{...}'
```

**Start Bifrost with custom labels:**

```bash
bifrost-http -config config.json -prometheus-labels team-id,service
```

---

## 🔍 Error Handling

### Error Response Format

All HTTP errors follow a consistent structure:

```json
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

### Common HTTP Status Codes

| Code | Type                  | Description                      |
| ---- | --------------------- | -------------------------------- |
| 200  | Success               | Request completed successfully   |
| 400  | Client Error          | Invalid request format           |
| 401  | Authentication Error  | Invalid or missing API key       |
| 404  | Not Found             | Endpoint or model not found      |
| 429  | Rate Limit            | Provider rate limit exceeded     |
| 500  | Internal Server Error | Bifrost internal error           |
| 502  | Bad Gateway           | Provider API error               |
| 503  | Service Unavailable   | Provider temporarily unavailable |

**[📖 Complete Error Reference →](../errors.md)**

---

## 📚 Configuration Reference

### Environment Variables

| Variable                   | Description                 | Default |
| -------------------------- | --------------------------- | ------- |
| `APP_PORT`                 | Server port                 | 8080    |
| `APP_POOL_SIZE`            | Connection pool size        | 300     |
| `APP_DROP_EXCESS_REQUESTS` | Drop excess requests        | false   |
| `APP_PLUGINS`              | Comma-separated plugin list | ""      |

**[📖 Complete Configuration Guide →](../../configuration/http-config.md)**

### Docker Configuration

```dockerfile
# Production Dockerfile example
FROM maximhq/bifrost:latest

# Copy configuration
COPY config.json /app/config/config.json

# Set environment variables
ENV APP_PORT=8080
ENV APP_POOL_SIZE=500
ENV APP_DROP_EXCESS_REQUESTS=true

# Expose port
EXPOSE 8080

# Health check
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD curl -f http://localhost:8080/health || exit 1
```

**[📖 Complete Deployment Guide →](../../configuration/deployment/)**

---

## 📚 Next Steps

### Learning Path

1. **[🚀 Quick Start](../../quick-start/http-transport.md)** - Get running in 30 seconds
2. **[🏠 Core Endpoints](endpoints.md)** - Learn all available endpoints
3. **[🔄 Provider Integrations](integrations.md)** - Set up drop-in replacements
4. **[💡 Complete Examples](examples.md)** - See real-world usage patterns

### Advanced Topics

- **[🔧 Configuration](../../configuration/http-config.md)** - Advanced server configuration
- **[🚀 Deployment](../../configuration/deployment/)** - Production deployment guides
- **[📊 Monitoring](../../features/observability.md)** - Observability and metrics
- **[🔧 Troubleshooting](../../guides/troubleshooting.md)** - Common issues and solutions

---

**Need help?** Check our [❓ FAQ](../../guides/faq.md) or [🔧 Troubleshooting Guide](../../guides/troubleshooting.md).
