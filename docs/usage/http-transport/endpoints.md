# 🌐 HTTP API Endpoints

Complete reference for Bifrost HTTP transport API endpoints and usage patterns.

> **💡 Quick Start:** See the [30-second setup](../../quickstart/http-transport.md) for basic API usage.

---

## 📋 Endpoint Overview

Bifrost HTTP transport provides:

- **Unified API endpoints** for all providers
- **Drop-in compatible endpoints** for existing SDKs
- **MCP tool execution** endpoint
- **Prometheus metrics** endpoint

Base URL: `http://localhost:8080` (configurable)

---

## 🔄 Unified API Endpoints

> All endpoints and request/response formats are **OpenAI compatible**.

### **POST /v1/chat/completions**

Chat conversation endpoint supporting all providers.

**Request Body:**

```json
{
  "model": "openai/gpt-4o-mini",
  "messages": [
    {
      "role": "user",
      "content": "Hello, how are you?"
    }
  ],
  "params": {
    "temperature": 0.7,
    "max_tokens": 1000
  },
  "fallbacks": ["anthropic/claude-3-sonnet-20240229"]
}
```

**Response:**

```json
{
  "id": "chatcmpl-123",
  "object": "chat.completion",
  "created": 1677652288,
  "choices": [
    {
      "index": 0,
      "message": {
        "role": "assistant",
        "content": "Hello! I'm doing well, thank you for asking."
      },
      "finish_reason": "stop"
    }
  ],
  "usage": {
    "prompt_tokens": 9,
    "completion_tokens": 12,
    "total_tokens": 21
  }
}
```

**cURL Example:**

```bash
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "openai/gpt-4o-mini",
    "messages": [
      {"role": "user", "content": "What is the capital of France?"}
    ]
  }'
```

### **Streaming Responses**

To receive a stream of partial responses, set `"stream": true` in your request. The response will be a `text/event-stream` of Server-Sent Events (SSE).

**Request with Streaming:**

```json
{
  "model": "openai/gpt-4o-mini",
  "messages": [{"role": "user", "content": "Write a short story."}],
  "stream": true
}
```

**SSE Event Stream:**

Each event in the stream is a JSON object prefixed with `data: `. The stream is terminated by a `[DONE]` message.

```
data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1694268190,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}

data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1694268190,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{"content":"Once"},"finish_reason":null}]}

data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1694268190,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{"content":" upon"},"finish_reason":null}]}

data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1694268190,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{"content":" a"},"finish_reason":null}]}

data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1694268190,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{"content":" time"},"finish_reason":null}]}

data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1694268190,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}

data: [DONE]
```

### **POST /v1/text/completions**

Text completion endpoint for simple text generation.

**Request Body:**

```json
{
  "model": "openai/gpt-4o-mini",
  "text": "The future of AI is",
  "params": {
    "temperature": 0.8,
    "max_tokens": 150
  }
}
```

**Response:**

```json
{
  "id": "cmpl-123",
  "object": "text_completion",
  "created": 1677652288,
  "choices": [
    {
      "text": "incredibly promising, with advances in machine learning...",
      "index": 0,
      "finish_reason": "length"
    }
  ],
  "usage": {
    "prompt_tokens": 5,
    "completion_tokens": 150,
    "total_tokens": 155
  }
}
```

### **POST /v1/mcp/tool/execute**

Direct MCP tool execution endpoint.

**Request Body:**

```json
{
  "id": "call_123",
  "type": "function",
  "function": {
    "name": "read_file",
    "arguments": "{\"path\": \"config.json\"}"
  }
}
```

**Response:**

```json
{
  "role": "tool",
  "content": {
    "content_str": "{\n  \"providers\": {\n    \"openai\": {...}\n  }\n}"
  },
  "tool_call_id": "call_123"
}
```

---

## 🔗 Drop-in Compatible Endpoints

### **OpenAI Compatible**

**POST /openai/v1/chat/completions**

Drop-in replacement for OpenAI API:

```bash
curl -X POST http://localhost:8080/openai/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4o-mini",
    "messages": [
      {"role": "user", "content": "Hello!"}
    ]
  }'
```

### **Anthropic Compatible**

**POST /anthropic/v1/messages**

Drop-in replacement for Anthropic API:

```bash
curl -X POST http://localhost:8080/anthropic/v1/messages \
  -H "Content-Type: application/json" \
  -d '{
    "model": "claude-3-sonnet-20240229",
    "max_tokens": 1000,
    "messages": [
      {"role": "user", "content": "Hello!"}
    ]
  }'
```

### **Google GenAI Compatible**

**POST /genai/v1beta/models/{model}:generateContent**

Drop-in replacement for Google GenAI API:

```bash
curl -X POST http://localhost:8080/genai/v1beta/models/gemini-pro:generateContent \
  -H "Content-Type: application/json" \
  -d '{
    "contents": [{
      "parts": [{"text": "Hello!"}]
    }]
  }'
```

---

## 📊 Monitoring Endpoints

### **GET /metrics**

Prometheus metrics endpoint:

```bash
curl http://localhost:8080/metrics
```

**Sample Metrics:**

```prometheus
# HELP bifrost_requests_total Total number of requests
# TYPE bifrost_requests_total counter
bifrost_requests_total{provider="openai",model="gpt-4o-mini",status="success"} 1247

# HELP bifrost_request_duration_seconds Request duration in seconds
# TYPE bifrost_request_duration_seconds histogram
bifrost_request_duration_seconds_bucket{provider="openai",le="0.5"} 823
bifrost_request_duration_seconds_bucket{provider="openai",le="1.0"} 1156

# HELP bifrost_provider_errors_total Provider error count
# TYPE bifrost_provider_errors_total counter
bifrost_provider_errors_total{provider="openai",error_type="rate_limit"} 23
```

---

## 🔧 Request Parameters

### **Common Parameters**

| Parameter   | Type   | Description             | Example                                  |
| ----------- | ------ | ----------------------- | ---------------------------------------- |
| `model`     | string | Provider and model name | `"openai/gpt-4o-mini"`                   |
| `params`    | object | Model parameters        | `{"temperature": 0.7}`                   |
| `fallbacks` | array  | Fallback model names    | `["anthropic/claude-3-sonnet-20240229"]` |

### **Model Parameters**

| Parameter           | Type    | Default          | Description                  |
| ------------------- | ------- | ---------------- | ---------------------------- |
| `temperature`       | float   | 1.0              | Randomness (0.0-2.0)         |
| `max_tokens`        | integer | Provider default | Maximum tokens to generate   |
| `top_p`             | float   | 1.0              | Nucleus sampling (0.0-1.0)   |
| `frequency_penalty` | float   | 0.0              | Frequency penalty (-2.0-2.0) |
| `presence_penalty`  | float   | 0.0              | Presence penalty (-2.0-2.0)  |
| `stop`              | array   | null             | Stop sequences               |

### **Chat Message Format**

```json
{
  "role": "user|assistant|system|tool",
  "content": "text content",
  "tool_calls": [...],
  "tool_call_id": "call_123"
}
```

**Multimodal Content:**

```json
{
  "role": "user",
  "content": [
    {
      "type": "text",
      "text": "What's in this image?"
    },
    {
      "type": "image_url",
      "image_url": {
        "url": "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAA..."
      }
    }
  ]
}
```

---

## 🛠️ Tool Calling

### **Automatic Tool Integration**

MCP tools are automatically available in chat completions:

```bash
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "openai/gpt-4o-mini",
    "messages": [
      {"role": "user", "content": "List files in the current directory"}
    ]
  }'
```

**Response with Tool Calls:**

```json
{
  "choices": [
    {
      "message": {
        "role": "assistant",
        "content": null,
        "tool_calls": [
          {
            "id": "call_123",
            "type": "function",
            "function": {
              "name": "list_directory",
              "arguments": "{\"path\": \".\"}"
            }
          }
        ]
      }
    }
  ]
}
```

### **Multi-turn Tool Conversations**

```bash
# Initial request
curl -X POST http://localhost:8080/v1/chat/completions \
  -d '{
    "model": "openai/gpt-4o-mini",
    "messages": [
      {"role": "user", "content": "Read the README.md file"},
      {
        "role": "assistant",
        "tool_calls": [{
          "id": "call_123",
          "type": "function",
          "function": {"name": "read_file", "arguments": "{\"path\": \"README.md\"}"}
        }]
      },
      {
        "role": "tool",
        "content": {"content_str": "# Bifrost\n\nBifrost is..."},
        "tool_call_id": "call_123"
      },
      {"role": "user", "content": "Summarize the main features"}
    ]
  }'
```

---

## 🔄 Error Handling

### **Error Response Format**

```json
{
  "error": {
    "message": "Invalid provider: nonexistent",
    "type": "invalid_request_error",
    "code": "invalid_provider"
  },
  "status_code": 400
}
```

### **Common Error Codes**

| Status | Code                    | Description          |
| ------ | ----------------------- | -------------------- |
| 400    | `invalid_request_error` | Bad request format   |
| 401    | `authentication_error`  | Invalid API key      |
| 403    | `permission_error`      | Access denied        |
| 429    | `rate_limit_error`      | Rate limit exceeded  |
| 500    | `internal_error`        | Server error         |
| 503    | `service_unavailable`   | Provider unavailable |

### **Error Response Examples**

**Missing Provider:**

```json
{
  "error": {
    "message": "Provider is required",
    "type": "invalid_request_error",
    "code": "missing_provider"
  },
  "status_code": 400
}
```

**Rate Limit:**

```json
{
  "error": {
    "message": "Rate limit exceeded for provider openai",
    "type": "rate_limit_error",
    "code": "rate_limit_exceeded"
  },
  "status_code": 429
}
```

---

## 🌍 Language SDK Examples

### **Python (OpenAI SDK)**

```python
import openai

client = openai.OpenAI(
    base_url="http://localhost:8080/openai",
    api_key="your-openai-key"
)

response = client.chat.completions.create(
    model="gpt-4o-mini",
    messages=[{"role": "user", "content": "Hello!"}]
)
```

### **JavaScript (OpenAI SDK)**

```javascript
import OpenAI from "openai";

const openai = new OpenAI({
  baseURL: "http://localhost:8080/openai",
  apiKey: process.env.OPENAI_API_KEY,
});

const response = await openai.chat.completions.create({
  model: "gpt-4o-mini",
  messages: [{ role: "user", content: "Hello!" }],
});
```

### **Go (Direct HTTP)**

```go
import (
    "bytes"
    "encoding/json"
    "net/http"
)

type ChatRequest struct {
    Provider string `json:"provider"`
    Model    string `json:"model"`
    Messages []Message `json:"messages"`
}

func makeRequest() {
    req := ChatRequest{
        Provider: "openai",
        Model:    "gpt-4o-mini",
        Messages: []Message{
            {Role: "user", Content: "Hello!"},
        },
    }

    body, _ := json.Marshal(req)
    resp, err := http.Post(
        "http://localhost:8080/v1/chat/completions",
        "application/json",
        bytes.NewBuffer(body),
    )
}
```

---

## 📚 Related Documentation

- **[🌐 HTTP Transport Overview](./README.md)** - Main HTTP transport guide
- **[🔧 Configuration](./configuration/)** - Provider and MCP setup
- **[🔗 Integrations](./integrations/)** - Drop-in API replacements
- **[📝 OpenAPI Specification](./openapi.json)** - Complete API schema

> **🏛️ Architecture:** For endpoint implementation details and performance, see [Architecture Documentation](../../architecture/README.md).
