# Bifrost HTTP Transport API Reference

This document provides comprehensive API documentation for the Bifrost HTTP transport, which exposes REST endpoints for text and chat completions using various AI model providers.

## Base URL

```
http://localhost:8080
```

## OpenAPI Specification

The complete OpenAPI 3.0 specification is available as a JSON file:

📄 **[OpenAPI Specification (JSON)](openapi.json)**

This machine-readable specification can be used with:

- **Swagger UI** - Interactive API documentation
- **Postman** - Import for API testing
- **Code Generation** - Generate client SDKs in multiple languages
- **API Gateways** - Request/response validation
- **Testing Tools** - Automated API testing

## Authentication

API keys are configured through environment variables for each provider. See the [providers documentation](providers.md) for setup instructions.

## Endpoints

### 1. Chat Completions

**POST** `/v1/chat/completions`

Creates a chat completion using conversational messages.

#### Request Body

```json
{
  "provider": "openai",
  "model": "gpt-4o",
  "messages": [
    {
      "role": "user",
      "content": "What's the weather like today?"
    }
  ],
  "params": {
    "max_tokens": 1000,
    "temperature": 0.7,
    "tools": [
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
                "description": "The city and state, e.g. San Francisco, CA"
              }
            },
            "required": ["location"]
          }
        }
      }
    ]
  },
  "fallbacks": [
    {
      "provider": "anthropic",
      "model": "claude-3-sonnet-20240229"
    }
  ]
}
```

#### Response

```json
{
  "id": "chatcmpl-123",
  "object": "chat.completion",
  "choices": [
    {
      "index": 0,
      "message": {
        "role": "assistant",
        "content": "I'd be happy to help you check the weather! However, I'll need to know your location first.",
        "tool_calls": [
          {
            "id": "call_123",
            "type": "function",
            "function": {
              "name": "get_weather",
              "arguments": "{\"location\": \"user_location\"}"
            }
          }
        ]
      },
      "finish_reason": "tool_calls"
    }
  ],
  "model": "gpt-4o",
  "created": 1677652288,
  "usage": {
    "prompt_tokens": 56,
    "completion_tokens": 31,
    "total_tokens": 87
  },
  "extra_fields": {
    "provider": "openai",
    "model_params": {
      "max_tokens": 1000,
      "temperature": 0.7
    },
    "latency": 1.234,
    "raw_response": {}
  }
}
```

### 2. Text Completions

**POST** `/v1/text/completions`

Creates a text completion from a prompt.

#### Request Body

```json
{
  "provider": "openai",
  "model": "gpt-3.5-turbo-instruct",
  "text": "The future of AI is",
  "params": {
    "max_tokens": 100,
    "temperature": 0.7,
    "stop_sequences": ["\n\n"]
  },
  "fallbacks": [
    {
      "provider": "cohere",
      "model": "command"
    }
  ]
}
```

#### Response

```json
{
  "id": "cmpl-123",
  "object": "text.completion",
  "choices": [
    {
      "index": 0,
      "message": {
        "role": "assistant",
        "content": "The future of AI is incredibly promising, with advances in machine learning..."
      },
      "finish_reason": "stop"
    }
  ],
  "model": "gpt-3.5-turbo-instruct",
  "created": 1677652288,
  "usage": {
    "prompt_tokens": 5,
    "completion_tokens": 95,
    "total_tokens": 100
  },
  "extra_fields": {
    "provider": "openai",
    "model_params": {
      "max_tokens": 100,
      "temperature": 0.7
    },
    "latency": 0.856,
    "raw_response": {}
  }
}
```

### 3. Metrics

**GET** `/metrics`

Returns Prometheus metrics for monitoring and observability.

## Schema Definitions

### CompletionRequest

The main request object for both chat and text completions.

| Field       | Type                                  | Required | Description                                                                       |
| ----------- | ------------------------------------- | -------- | --------------------------------------------------------------------------------- |
| `provider`  | `string`                              | ✅       | AI model provider (`openai`, `anthropic`, `azure`, `bedrock`, `cohere`, `vertex`) |
| `model`     | `string`                              | ✅       | Model identifier (provider-specific)                                              |
| `messages`  | [`BifrostMessage[]`](#bifrostmessage) | ✅\*     | Array of chat messages (required for chat completions)                            |
| `text`      | `string`                              | ✅\*     | Text prompt (required for text completions)                                       |
| `params`    | [`ModelParameters`](#modelparameters) | ❌       | Model parameters and configuration                                                |
| `fallbacks` | [`Fallback[]`](#fallback)             | ❌       | Fallback providers and models                                                     |

\*Either `messages` or `text` is required depending on the endpoint.

### BifrostMessage

Represents a message in a chat conversation.

| Field           | Type                            | Required | Description                                          |
| --------------- | ------------------------------- | -------- | ---------------------------------------------------- |
| `role`          | `string`                        | ✅       | Message role (`user`, `assistant`, `system`, `tool`) |
| `content`       | `string`                        | ❌       | Text content of the message                          |
| `tool_call_id`  | `string`                        | ❌       | ID of the tool call (for tool messages)              |
| `tool_calls`    | [`ToolCall[]`](#toolcall)       | ❌       | Tool calls made by assistant                         |
| `image_content` | [`ImageContent`](#imagecontent) | ❌       | Image data in the message                            |
| `refusal`       | `string`                        | ❌       | Refusal message from assistant                       |
| `annotations`   | `Annotation[]`                  | ❌       | Message annotations                                  |
| `thought`       | `string`                        | ❌       | Assistant's internal thought process                 |

### ModelParameters

Configuration parameters for model behavior.

| Field                 | Type                        | Description                             |
| --------------------- | --------------------------- | --------------------------------------- |
| `temperature`         | `number`                    | Controls randomness (0.0-2.0)           |
| `top_p`               | `number`                    | Nucleus sampling parameter (0.0-1.0)    |
| `top_k`               | `integer`                   | Top-k sampling parameter                |
| `max_tokens`          | `integer`                   | Maximum tokens to generate              |
| `stop_sequences`      | `string[]`                  | Sequences that stop generation          |
| `presence_penalty`    | `number`                    | Penalizes repeated tokens (-2.0 to 2.0) |
| `frequency_penalty`   | `number`                    | Penalizes frequent tokens (-2.0 to 2.0) |
| `tools`               | [`Tool[]`](#tool)           | Available tools for the model           |
| `tool_choice`         | [`ToolChoice`](#toolchoice) | How tools should be chosen              |
| `parallel_tool_calls` | `boolean`                   | Enable parallel tool execution          |

### Tool

Defines a function that the model can call.

| Field      | Type                    | Required | Description                             |
| ---------- | ----------------------- | -------- | --------------------------------------- |
| `id`       | `string`                | ❌       | Unique tool identifier                  |
| `type`     | `string`                | ✅       | Tool type (currently only `"function"`) |
| `function` | [`Function`](#function) | ✅       | Function definition                     |

### Function

Defines the function details for a tool.

| Field         | Type                                        | Required | Description                |
| ------------- | ------------------------------------------- | -------- | -------------------------- |
| `name`        | `string`                                    | ✅       | Function name              |
| `description` | `string`                                    | ✅       | Function description       |
| `parameters`  | [`FunctionParameters`](#functionparameters) | ✅       | Function parameters schema |

### FunctionParameters

JSON Schema defining function parameters.

| Field         | Type       | Required | Description                         |
| ------------- | ---------- | -------- | ----------------------------------- |
| `type`        | `string`   | ✅       | Parameter type (usually `"object"`) |
| `description` | `string`   | ❌       | Parameter description               |
| `properties`  | `object`   | ❌       | Parameter properties (JSON Schema)  |
| `required`    | `string[]` | ❌       | Required parameter names            |
| `enum`        | `string[]` | ❌       | Enum values for parameters          |

### ToolChoice

Specifies how the model should choose tools.

| Field      | Type                                        | Required | Description                                                 |
| ---------- | ------------------------------------------- | -------- | ----------------------------------------------------------- |
| `type`     | `string`                                    | ✅       | Choice type (`none`, `auto`, `any`, `function`, `required`) |
| `function` | [`ToolChoiceFunction`](#toolchoicefunction) | ❌       | Specific function to call (when type is `function`)         |

### ToolChoiceFunction

Specifies a particular function to call.

| Field  | Type     | Required | Description                  |
| ------ | -------- | -------- | ---------------------------- |
| `name` | `string` | ✅       | Name of the function to call |

### Fallback

Defines a fallback provider and model.

| Field      | Type     | Required | Description            |
| ---------- | -------- | -------- | ---------------------- |
| `provider` | `string` | ✅       | Fallback provider name |
| `model`    | `string` | ✅       | Fallback model name    |

### BifrostResponse

The response object returned by both endpoints.

| Field                | Type                                                        | Description                                            |
| -------------------- | ----------------------------------------------------------- | ------------------------------------------------------ |
| `id`                 | `string`                                                    | Unique response identifier                             |
| `object`             | `string`                                                    | Response type (`chat.completion` or `text.completion`) |
| `choices`            | [`BifrostResponseChoice[]`](#bifrostresponsechoice)         | Array of completion choices                            |
| `model`              | `string`                                                    | Model used for generation                              |
| `created`            | `integer`                                                   | Unix timestamp of creation                             |
| `service_tier`       | `string`                                                    | Service tier used                                      |
| `system_fingerprint` | `string`                                                    | System fingerprint                                     |
| `usage`              | [`LLMUsage`](#llmusage)                                     | Token usage statistics                                 |
| `extra_fields`       | [`BifrostResponseExtraFields`](#bifrostresponseextrafields) | Additional Bifrost-specific data                       |

### BifrostResponseChoice

A single completion choice.

| Field           | Type                                | Description                         |
| --------------- | ----------------------------------- | ----------------------------------- |
| `index`         | `integer`                           | Choice index                        |
| `message`       | [`BifrostMessage`](#bifrostmessage) | The completion message              |
| `finish_reason` | `string`                            | Reason completion stopped           |
| `stop`          | `string`                            | Stop sequence that ended generation |
| `log_probs`     | `LogProbs`                          | Log probabilities (if requested)    |

### LLMUsage

Token usage statistics.

| Field                       | Type                      | Description                         |
| --------------------------- | ------------------------- | ----------------------------------- |
| `prompt_tokens`             | `integer`                 | Tokens in the prompt                |
| `completion_tokens`         | `integer`                 | Tokens in the completion            |
| `total_tokens`              | `integer`                 | Total tokens used                   |
| `completion_tokens_details` | `CompletionTokensDetails` | Detailed completion token breakdown |

### BifrostResponseExtraFields

Additional Bifrost-specific response data.

| Field          | Type                                  | Description                     |
| -------------- | ------------------------------------- | ------------------------------- |
| `provider`     | `string`                              | Provider used for the request   |
| `model_params` | [`ModelParameters`](#modelparameters) | Parameters used for the request |
| `latency`      | `number`                              | Request latency in seconds      |
| `chat_history` | [`BifrostMessage[]`](#bifrostmessage) | Full conversation history       |
| `billed_usage` | `BilledLLMUsage`                      | Billing usage information       |
| `raw_response` | `object`                              | Raw provider response           |

### ToolCall

Represents a tool call made by the assistant.

| Field      | Type                            | Description                 |
| ---------- | ------------------------------- | --------------------------- |
| `id`       | `string`                        | Unique tool call identifier |
| `type`     | `string`                        | Tool call type (`function`) |
| `function` | [`FunctionCall`](#functioncall) | Function call details       |

### FunctionCall

Details of a function call.

| Field       | Type     | Description                       |
| ----------- | -------- | --------------------------------- |
| `name`      | `string` | Function name                     |
| `arguments` | `string` | JSON string of function arguments |

### ImageContent

Image data in a message.

| Field        | Type     | Description                                |
| ------------ | -------- | ------------------------------------------ |
| `type`       | `string` | Content type                               |
| `url`        | `string` | Image URL or data URI                      |
| `media_type` | `string` | MIME type of the image                     |
| `detail`     | `string` | Image detail level (`low`, `high`, `auto`) |

### BifrostError

Error response format.

| Field              | Type                        | Description                           |
| ------------------ | --------------------------- | ------------------------------------- |
| `event_id`         | `string`                    | Unique error event ID                 |
| `type`             | `string`                    | Error type                            |
| `is_bifrost_error` | `boolean`                   | Whether error originated from Bifrost |
| `status_code`      | `integer`                   | HTTP status code                      |
| `error`            | [`ErrorField`](#errorfield) | Detailed error information            |

### ErrorField

Detailed error information.

| Field      | Type     | Description                     |
| ---------- | -------- | ------------------------------- |
| `type`     | `string` | Error type                      |
| `code`     | `string` | Error code                      |
| `message`  | `string` | Human-readable error message    |
| `param`    | `any`    | Parameter that caused the error |
| `event_id` | `string` | Error event ID                  |

## Supported Providers

| Provider         | Key         |
| ---------------- | ----------- |
| OpenAI           | `openai`    |
| Anthropic        | `anthropic` |
| Azure OpenAI     | `azure`     |
| AWS Bedrock      | `bedrock`   |
| Cohere           | `cohere`    |
| Google Vertex AI | `vertex`    |

## Error Codes

| Status Code | Description                                                     |
| ----------- | --------------------------------------------------------------- |
| `400`       | Bad Request - Invalid request format or missing required fields |
| `401`       | Unauthorized - Invalid or missing API key                       |
| `429`       | Too Many Requests - Rate limit exceeded                         |
| `500`       | Internal Server Error - Server or provider error                |
| `502`       | Bad Gateway - Provider service unavailable                      |
| `503`       | Service Unavailable - Bifrost service temporarily unavailable   |

## Rate Limiting

Rate limiting is handled by the individual providers. Bifrost respects provider rate limits and will return appropriate error responses when limits are exceeded.

## Examples

### Simple Chat

```bash
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "provider": "openai",
    "model": "gpt-4o",
    "messages": [
      {"role": "user", "content": "Hello, world!"}
    ]
  }'
```

### Chat with Tools

```bash
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "provider": "openai",
    "model": "gpt-4o",
    "messages": [
      {"role": "user", "content": "What'\''s the weather in San Francisco?"}
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
      "tool_choice": {"type": "function", "function": {"name": "get_weather"}}
    }
  }'
```

### Text Completion

```bash
curl -X POST http://localhost:8080/v1/text/completions \
  -H "Content-Type: application/json" \
  -d '{
    "provider": "openai",
    "model": "gpt-3.5-turbo-instruct",
    "text": "The benefits of artificial intelligence include",
    "params": {
      "max_tokens": 150,
      "temperature": 0.7
    }
  }'
```

### Using Fallbacks

```bash
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "provider": "openai",
    "model": "gpt-4o",
    "messages": [
      {"role": "user", "content": "Explain quantum computing"}
    ],
    "fallbacks": [
      {"provider": "anthropic", "model": "claude-3-sonnet-20240229"},
      {"provider": "cohere", "model": "command"}
    ]
  }'
```

## Integration Examples

### Python

```python
import requests

def chat_completion(messages, provider="openai", model="gpt-4o"):
    response = requests.post(
        "http://localhost:8080/v1/chat/completions",
        json={
            "provider": provider,
            "model": model,
            "messages": messages,
            "params": {"max_tokens": 1000}
        }
    )
    return response.json()

# Usage
result = chat_completion([
    {"role": "user", "content": "Hello, how are you?"}
])
print(result["choices"][0]["message"]["content"])
```

### Node.js

```javascript
const axios = require("axios");

async function chatCompletion(messages, provider = "openai", model = "gpt-4o") {
  try {
    const response = await axios.post(
      "http://localhost:8080/v1/chat/completions",
      {
        provider,
        model,
        messages,
        params: { max_tokens: 1000 },
      }
    );
    return response.data;
  } catch (error) {
    console.error("Error:", error.response?.data || error.message);
    throw error;
  }
}

// Usage
chatCompletion([{ role: "user", content: "Hello, how are you?" }]).then(
  (result) => {
    console.log(result.choices[0].message.content);
  }
);
```

### Go

```go
package main

import (
    "bytes"
    "encoding/json"
    "fmt"
    "net/http"
)

type ChatRequest struct {
    Provider string            `json:"provider"`
    Model    string            `json:"model"`
    Messages []BifrostMessage  `json:"messages"`
    Params   *ModelParameters  `json:"params,omitempty"`
}

type BifrostMessage struct {
    Role    string `json:"role"`
    Content string `json:"content"`
}

type ModelParameters struct {
    MaxTokens *int `json:"max_tokens,omitempty"`
}

func chatCompletion(messages []BifrostMessage) error {
    reqBody := ChatRequest{
        Provider: "openai",
        Model:    "gpt-4o",
        Messages: messages,
        Params:   &ModelParameters{MaxTokens: intPtr(1000)},
    }

    jsonData, _ := json.Marshal(reqBody)
    resp, err := http.Post(
        "http://localhost:8080/v1/chat/completions",
        "application/json",
        bytes.NewBuffer(jsonData),
    )
    if err != nil {
        return err
    }
    defer resp.Body.Close()

    var result map[string]interface{}
    json.NewDecoder(resp.Body).Decode(&result)
    fmt.Println(result)
    return nil
}

func intPtr(i int) *int { return &i }
```

## Configuration

The HTTP transport can be configured via command-line flags:

```bash
./bifrost-http \
  -config config.json \
  -port 8080 \
  -pool-size 300 \
  -drop-excess-requests \
  -plugins maxim \
  -maxim-log-repo-id your-repo-id \
  -prometheus-labels env,service
```

### Configuration Flags

| Flag                    | Description                      | Default  |
| ----------------------- | -------------------------------- | -------- |
| `-config`               | Path to configuration file       | Required |
| `-port`                 | Server port                      | `8080`   |
| `-pool-size`            | Initial connection pool size     | `300`    |
| `-drop-excess-requests` | Drop requests when queue is full | `false`  |
| `-plugins`              | Comma-separated list of plugins  | None     |
| `-maxim-log-repo-id`    | Maxim logging repository ID      | None     |
| `-prometheus-labels`    | Additional Prometheus labels     | None     |

## Monitoring

The `/metrics` endpoint provides Prometheus-compatible metrics for monitoring:

- Request counts by provider, model, and status
- Request latency histograms
- Token usage metrics
- Error rates and types
- Connection pool statistics
