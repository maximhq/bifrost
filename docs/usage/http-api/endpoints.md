# HTTP API Endpoints Reference

Complete reference for all Bifrost HTTP API endpoints, including request/response schemas, examples, and error handling.

## 📑 Endpoint Categories

| Category                                               | Description                      | Endpoints                                        |
| ------------------------------------------------------ | -------------------------------- | ------------------------------------------------ |
| **[Chat Completions](#-chat-completions)**             | Conversational AI interactions   | `POST /v1/chat/completions`                      |
| **[Text Completions](#-text-completions)**             | Text continuation and completion | `POST /v1/text/completions`                      |
| **[MCP Integration](#-mcp-integration)**               | Model Context Protocol tools     | `POST /v1/mcp/tool/execute`, `GET /v1/mcp/tools` |
| **[Provider Compatibility](#-provider-compatibility)** | Drop-in API replacements         | OpenAI, Anthropic, GenAI compatible endpoints    |
| **[System Endpoints](#-system-endpoints)**             | Health, metrics, configuration   | `GET /health`, `GET /metrics`                    |

---

## 💬 Chat Completions

### `POST /v1/chat/completions`

Create a chat completion using conversational messages. Supports tool calling, image inputs, and multiple AI providers with automatic fallbacks.

#### Request Schema

```json
{
  "provider": "string", // Required: AI provider
  "model": "string", // Required: Model identifier
  "messages": [], // Required: Array of messages
  "params": {}, // Optional: Model parameters
  "fallbacks": [], // Optional: Fallback providers
  "extra_fields": {} // Optional: Provider-specific fields
}
```

#### Message Schema

```json
{
  "role": "user|assistant|system|tool",
  "content": "string|array", // Text or structured content
  "tool_call_id": "string", // For tool response messages
  "tool_calls": [], // Tool calls from assistant
  "refusal": "string", // Assistant refusal message
  "annotations": [], // Message annotations
  "thought": "string" // Assistant reasoning
}
```

#### Content Types

<details>
<summary><strong>Simple Text Content</strong></summary>

```json
{
  "role": "user",
  "content": "Hello, how are you today?"
}
```

</details>

<details>
<summary><strong>Structured Content (Text + Image)</strong></summary>

```json
{
  "role": "user",
  "content": [
    {
      "type": "text",
      "text": "What's happening in this image?"
    },
    {
      "type": "image_url",
      "image_url": {
        "url": "https://example.com/image.jpg",
        "detail": "high"
      }
    }
  ]
}
```

</details>

<details>
<summary><strong>Tool Response Message</strong></summary>

```json
{
  "role": "tool",
  "content": "The weather in San Francisco is 72°F and sunny.",
  "tool_call_id": "call_abc123"
}
```

</details>

#### Model Parameters

```json
{
  "temperature": 0.7, // Randomness (0.0-2.0)
  "top_p": 0.9, // Nucleus sampling (0.0-1.0)
  "top_k": 40, // Top-k sampling
  "max_tokens": 1000, // Maximum tokens to generate
  "stop_sequences": ["END"], // Stop generation sequences
  "presence_penalty": 0.0, // Repeated token penalty (-2.0-2.0)
  "frequency_penalty": 0.0, // Frequent token penalty (-2.0-2.0)
  "tools": [], // Available tools
  "tool_choice": "auto", // Tool calling behavior
  "parallel_tool_calls": true // Enable parallel tool calls
}
```

#### Tool Definition

```json
{
  "type": "function",
  "function": {
    "name": "get_weather",
    "description": "Get current weather for a location",
    "parameters": {
      "type": "object",
      "properties": {
        "location": {
          "type": "string",
          "description": "City and state, e.g. San Francisco, CA"
        },
        "unit": {
          "type": "string",
          "enum": ["celsius", "fahrenheit"],
          "description": "Temperature unit"
        }
      },
      "required": ["location"]
    }
  }
}
```

#### Example Requests

<details>
<summary><strong>Basic Chat</strong></summary>

```bash
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "provider": "openai",
    "model": "gpt-4o-mini",
    "messages": [
      {
        "role": "system",
        "content": "You are a helpful assistant."
      },
      {
        "role": "user",
        "content": "Explain quantum computing in simple terms."
      }
    ],
    "params": {
      "temperature": 0.7,
      "max_tokens": 500
    }
  }'
```

</details>

<details>
<summary><strong>Tool Calling</strong></summary>

```bash
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "provider": "openai",
    "model": "gpt-4o-mini",
    "messages": [
      {
        "role": "user",
        "content": "What'\''s the weather in New York?"
      }
    ],
    "params": {
      "tools": [
        {
          "type": "function",
          "function": {
            "name": "get_weather",
            "description": "Get current weather",
            "parameters": {
              "type": "object",
              "properties": {
                "location": {"type": "string"}
              },
              "required": ["location"]
            }
          }
        }
      ],
      "tool_choice": "auto"
    }
  }'
```

</details>

<details>
<summary><strong>With Fallbacks</strong></summary>

```bash
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "provider": "openai",
    "model": "gpt-4o",
    "messages": [
      {
        "role": "user",
        "content": "Write a creative story about time travel."
      }
    ],
    "fallbacks": [
      {
        "provider": "anthropic",
        "model": "claude-3-sonnet-20240229"
      },
      {
        "provider": "cohere",
        "model": "command"
      }
    ]
  }'
```

</details>

<details>
<summary><strong>Image Analysis</strong></summary>

```bash
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "provider": "openai",
    "model": "gpt-4o",
    "messages": [
      {
        "role": "user",
        "content": [
          {
            "type": "text",
            "text": "Describe what you see in this image."
          },
          {
            "type": "image_url",
            "image_url": {
              "url": "https://example.com/photo.jpg",
              "detail": "high"
            }
          }
        ]
      }
    ]
  }'
```

</details>

#### Response Schema

```json
{
  "id": "chatcmpl-123",
  "object": "chat.completion",
  "choices": [
    {
      "index": 0,
      "message": {
        "role": "assistant",
        "content": "Response text",
        "tool_calls": [], // If tools were called
        "refusal": null, // If request was refused
        "annotations": [] // Message annotations
      },
      "finish_reason": "stop", // "stop", "length", "tool_calls"
      "log_probs": null
    }
  ],
  "model": "gpt-4o-mini",
  "created": 1677652288,
  "usage": {
    "prompt_tokens": 12,
    "completion_tokens": 19,
    "total_tokens": 31
  },
  "extra_fields": {
    "provider": "openai",
    "model_params": {},
    "latency": 1.234,
    "raw_response": {}
  }
}
```

---

## 📝 Text Completions

### `POST /v1/text/completions`

Create a text completion by continuing the provided text prompt.

#### Request Schema

```json
{
  "provider": "string", // Required: AI provider
  "model": "string", // Required: Model identifier
  "text": "string", // Required: Text to complete
  "params": {}, // Optional: Model parameters
  "fallbacks": [] // Optional: Fallback providers
}
```

#### Example Request

```bash
curl -X POST http://localhost:8080/v1/text/completions \
  -H "Content-Type: application/json" \
  -d '{
    "provider": "anthropic",
    "model": "claude-2.1",
    "text": "The future of artificial intelligence is",
    "params": {
      "temperature": 0.8,
      "max_tokens": 200
    }
  }'
```

#### Response Schema

```json
{
  "id": "cmpl-123",
  "object": "text.completion",
  "choices": [
    {
      "index": 0,
      "message": {
        "role": "assistant",
        "content": "bright and full of possibilities..."
      },
      "finish_reason": "stop"
    }
  ],
  "model": "claude-2.1",
  "created": 1677652288,
  "usage": {
    "prompt_tokens": 8,
    "completion_tokens": 25,
    "total_tokens": 33
  },
  "extra_fields": {
    "provider": "anthropic",
    "model_params": {},
    "latency": 2.1
  }
}
```

---

## 🛠️ MCP Integration

### `POST /v1/mcp/tool/execute`

Execute a tool call returned by an AI model.

#### Request Schema

```json
{
  "id": "string", // Tool call ID
  "type": "function", // Always "function"
  "function": {
    "name": "string", // Function name
    "arguments": "string" // JSON string of arguments
  }
}
```

#### Example Request

```bash
curl -X POST http://localhost:8080/v1/mcp/tool/execute \
  -H "Content-Type: application/json" \
  -d '{
    "id": "call_abc123",
    "type": "function",
    "function": {
      "name": "list_files",
      "arguments": "{\"path\": \"/tmp\"}"
    }
  }'
```

#### Response Schema

```json
{
  "role": "tool",
  "content": "file1.txt\nfile2.txt\nfile3.txt",
  "tool_call_id": "call_abc123"
}
```

## 🔄 Provider Compatibility

Bifrost provides drop-in API compatibility with popular AI providers.

### OpenAI Compatible (By default Bifrost is totally compatible with OpenAI API)

- `POST openai/v1/chat/completions` - Direct OpenAI format
- `POST openai/v1/completions` - Legacy completion format

### Anthropic Compatible

- `POST anthropic/v1/messages` - Claude API format

### Google GenAI Compatible

- `POST google/v1/models/{model}:generateContent` - Gemini API format

#### Example: OpenAI Drop-in Replacement

```bash
# Replace api.openai.com with your Bifrost instance
curl -X POST http://localhost:8080/openai/v1/chat/completions \
  -H "Authorization: Bearer $OPENAI_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4o-mini",
    "messages": [
      {"role": "user", "content": "Hello!"}
    ]
  }'
```

---

## 🔧 System Endpoints

### `GET /metrics`

Prometheus metrics endpoint.

#### Sample Metrics

```prometheus
# Request metrics
bifrost_upstream_requests_total{provider="openai",status="success"} 1234
bifrost_upstream_latency_seconds_sum{provider="openai"} 45.67

# System metrics
http_requests_total{endpoint="/v1/chat/completions",status="200"} 5678
http_request_duration_seconds_sum{endpoint="/v1/chat/completions"} 123.45

# Queue metrics
bifrost_queue_depth{provider="openai"} 12
bifrost_active_workers{provider="openai"} 8
```

---

## ❌ Error Responses

### Error Schema

```json
{
  "error": {
    "type": "invalid_request_error",
    "code": "missing_required_field",
    "message": "The 'provider' field is required",
    "param": "provider"
  },
  "event_id": "evt_123456789",
  "status_code": 400,
  "provider": "openai",
  "model": "gpt-4o-mini"
}
```

### Common Error Types

| Error Type              | HTTP Status | Description              |
| ----------------------- | ----------- | ------------------------ |
| `invalid_request_error` | 400         | Malformed request        |
| `authentication_error`  | 401         | Invalid API key          |
| `permission_error`      | 403         | Insufficient permissions |
| `not_found_error`       | 404         | Model/provider not found |
| `rate_limit_error`      | 429         | Rate limit exceeded      |
| `api_error`             | 500         | Provider API error       |
| `overloaded_error`      | 503         | System overloaded        |

### Error Handling Examples

<details>
<summary><strong>Invalid Provider</strong></summary>

```json
{
  "error": {
    "type": "invalid_request_error",
    "code": "invalid_provider",
    "message": "Provider 'unknown' is not supported. Supported providers: openai, anthropic, azure, bedrock, cohere, vertex, mistral, ollama",
    "param": "provider"
  },
  "event_id": "evt_abc123",
  "status_code": 400
}
```

</details>

<details>
<summary><strong>Rate Limit Exceeded</strong></summary>

```json
{
  "error": {
    "type": "rate_limit_error",
    "code": "rate_limit_exceeded",
    "message": "Rate limit exceeded for model gpt-4o. Try again in 60 seconds",
    "param": null
  },
  "provider": "openai",
  "model": "gpt-4o",
  "status_code": 429
}
```

</details>

<details>
<summary><strong>Fallback Chain Failed</strong></summary>

```json
{
  "error": {
    "type": "api_error",
    "code": "all_providers_failed",
    "message": "Primary provider and all fallbacks failed",
    "details": {
      "primary_error": "Rate limit exceeded",
      "fallback_errors": ["Model not available", "Authentication failed"]
    }
  },
  "status_code": 500
}
```

</details>

---

## 🚀 Multi-Turn Conversations

Complete example of a multi-turn conversation with tool calling:

<details>
<summary><strong>Step 1: Initial Request</strong></summary>

```bash
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "provider": "openai",
    "model": "gpt-4o-mini",
    "messages": [
      {
        "role": "user",
        "content": "Can you list the files in the /tmp directory?"
      }
    ]
  }'
```

**Response:**

```json
{
  "choices": [
    {
      "message": {
        "role": "assistant",
        "content": null,
        "tool_calls": [
          {
            "id": "call_abc123",
            "type": "function",
            "function": {
              "name": "list_files",
              "arguments": "{\"path\": \"/tmp\"}"
            }
          }
        ]
      },
      "finish_reason": "tool_calls"
    }
  ]
}
```

</details>

<details>
<summary><strong>Step 2: Execute Tool</strong></summary>

```bash
curl -X POST http://localhost:8080/v1/mcp/tool/execute \
  -H "Content-Type: application/json" \
  -d '{
    "id": "call_abc123",
    "type": "function",
    "function": {
      "name": "list_files",
      "arguments": "{\"path\": \"/tmp\"}"
    }
  }'
```

**Response:**

```json
{
  "role": "tool",
  "content": "config.json\ndata.csv\nreadme.txt",
  "tool_call_id": "call_abc123"
}
```

</details>

<details>
<summary><strong>Step 3: Continue Conversation</strong></summary>

```bash
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "provider": "openai",
    "model": "gpt-4o-mini",
    "messages": [
      {
        "role": "user",
        "content": "Can you list the files in the /tmp directory?"
      },
      {
        "role": "assistant",
        "content": null,
        "tool_calls": [
          {
            "id": "call_abc123",
            "type": "function",
            "function": {
              "name": "list_files",
              "arguments": "{\"path\": \"/tmp\"}"
            }
          }
        ]
      },
      {
        "role": "tool",
        "content": "config.json\ndata.csv\nreadme.txt",
        "tool_call_id": "call_abc123"
      }
    ]
  }'
```

**Response:**

```json
{
  "choices": [
    {
      "message": {
        "role": "assistant",
        "content": "I found 3 files in the /tmp directory:\n1. config.json\n2. data.csv\n3. readme.txt\n\nWould you like me to read the contents of any of these files?"
      },
      "finish_reason": "stop"
    }
  ]
}
```

</details>

---

_For more examples and integration guides, see the main [HTTP API Reference](README.md)._
