# 🤖 OpenAI Provider Configuration

Complete guide for configuring OpenAI GPT models with Bifrost, including enterprise features, custom deployments, and advanced configurations.

## 📋 Supported Models

| Model Family | Models                                    | Capabilities                   |
| ------------ | ----------------------------------------- | ------------------------------ |
| **GPT-4o**   | `gpt-4o`, `gpt-4o-mini`                   | Chat, Vision, Tools, JSON mode |
| **GPT-4**    | `gpt-4`, `gpt-4-turbo`                    | Chat, Vision, Tools, JSON mode |
| **GPT-3.5**  | `gpt-3.5-turbo`, `gpt-3.5-turbo-instruct` | Chat, Completions              |
| **DALL-E**   | `dall-e-3`, `dall-e-2`                    | Image generation               |

---

## 🚀 Quick Start

<details>
<summary><strong>🔧 Go Package Usage</strong></summary>

### Basic Configuration

```go
import "github.com/maximhq/bifrost/core/schemas"

account := &schemas.Account{
    Providers: map[string]schemas.ProviderConfig{
        "openai": {
            Keys: []schemas.Key{
                {
                    Value:  "sk-your-openai-api-key",
                    Models: []string{"gpt-4o", "gpt-4o-mini"},
                    Weight: 1.0,
                },
            },
        },
    },
}
```

### Multiple API Keys with Load Balancing

```go
account := &schemas.Account{
    Providers: map[string]schemas.ProviderConfig{
        "openai": {
            Keys: []schemas.Key{
                {
                    Value:  "sk-key-1",
                    Models: []string{"gpt-4o", "gpt-4o-mini"},
                    Weight: 0.6, // 60% of traffic
                },
                {
                    Value:  "sk-key-2",
                    Models: []string{"gpt-4o-mini"},
                    Weight: 0.3, // 30% of traffic
                },
                {
                    Value:  "sk-key-3",
                    Models: []string{"gpt-4o"},
                    Weight: 0.1, // 10% of traffic
                },
            },
        },
    },
}
```

### Making Requests

```go
import "github.com/maximhq/bifrost/core"

client := bifrost.NewBifrostClient(account)

// Chat completion
response, err := client.CreateChatCompletion(&schemas.ChatCompletionRequest{
    Provider: "openai",
    Model:    "gpt-4o",
    Messages: []schemas.Message{
        {Role: "system", Content: "You are a helpful assistant."},
        {Role: "user", Content: "Explain quantum computing in simple terms."},
    },
    Params: schemas.RequestParams{
        MaxTokens:   500,
        Temperature: 0.7,
    },
})
```

</details>

<details>
<summary><strong>🌐 HTTP Transport Usage</strong></summary>

### Configuration File

```json
{
  "providers": {
    "openai": {
      "keys": [
        {
          "value": "env.OPENAI_API_KEY",
          "models": ["gpt-4o", "gpt-4o-mini", "gpt-4", "gpt-3.5-turbo"],
          "weight": 1.0
        }
      ]
    }
  }
}
```

### Multiple Keys Configuration

```json
{
  "providers": {
    "openai": {
      "keys": [
        {
          "value": "env.OPENAI_API_KEY_1",
          "models": ["gpt-4o", "gpt-4o-mini"],
          "weight": 0.6
        },
        {
          "value": "env.OPENAI_API_KEY_2",
          "models": ["gpt-4o-mini"],
          "weight": 0.4
        }
      ]
    }
  }
}
```

### Making Requests

```bash
# Chat completion
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "provider": "openai",
    "model": "gpt-4o",
    "messages": [
      {"role": "system", "content": "You are a helpful assistant."},
      {"role": "user", "content": "Explain quantum computing in simple terms."}
    ],
    "params": {
      "max_tokens": 500,
      "temperature": 0.7
    }
  }'

# Text completion
curl -X POST http://localhost:8080/v1/text/completions \
  -H "Content-Type: application/json" \
  -d '{
    "provider": "openai",
    "model": "gpt-3.5-turbo-instruct",
    "text": "The future of AI is",
    "params": {
      "max_tokens": 100,
      "temperature": 0.7
    }
  }'
```

</details>

---

## ⚙️ Advanced Configuration

### Custom Base URL & Headers

<details>
<summary><strong>🔧 Go Package Configuration</strong></summary>

```go
// Custom OpenAI deployment
providerConfig := schemas.ProviderConfig{
    Keys: []schemas.Key{
        {Value: "your-api-key", Models: []string{"gpt-4o"}, Weight: 1.0},
    },
    NetworkConfig: schemas.NetworkConfig{
        BaseURL: "https://api.openai.com/v1", // Custom endpoint
        ExtraHeaders: map[string]string{
            "OpenAI-Organization": "your-org-id",
            "OpenAI-Project":      "your-project-id",
            "User-Agent":          "MyApp/1.0",
        },
        DefaultRequestTimeoutInSeconds: 60,
        MaxRetries:                     3,
        RetryBackoffInitial:           200 * time.Millisecond,
        RetryBackoffMax:               5 * time.Second,
    },
}
```

</details>

<details>
<summary><strong>🌐 HTTP Transport Configuration</strong></summary>

```json
{
  "providers": {
    "openai": {
      "keys": [
        {
          "value": "env.OPENAI_API_KEY",
          "models": ["gpt-4o"],
          "weight": 1.0
        }
      ],
      "network_config": {
        "base_url": "https://api.openai.com/v1",
        "extra_headers": {
          "OpenAI-Organization": "your-org-id",
          "OpenAI-Project": "your-project-id"
        },
        "default_request_timeout_in_seconds": 60,
        "max_retries": 3
      }
    }
  }
}
```

</details>

### Performance Optimization

<details>
<summary><strong>🔧 Go Package Configuration</strong></summary>

```go
// High-performance configuration
providerConfig := schemas.ProviderConfig{
    Keys: []schemas.Key{
        {Value: "your-api-key", Models: []string{"gpt-4o"}, Weight: 1.0},
    },
    ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
        Concurrency: 20,  // Higher concurrency for OpenAI
        BufferSize:  200, // Larger buffer for burst traffic
    },
}
```

</details>

<details>
<summary><strong>🌐 HTTP Transport Configuration</strong></summary>

```json
{
  "providers": {
    "openai": {
      "keys": [...],
      "concurrency_and_buffer_size": {
        "concurrency": 20,
        "buffer_size": 200
      }
    }
  }
}
```

</details>

---

## 🛠️ Advanced Features

### Vision Models (GPT-4V)

<details>
<summary><strong>🔧 Go Package Usage</strong></summary>

```go
// Image analysis with GPT-4V
request := &schemas.ChatCompletionRequest{
    Provider: "openai",
    Model:    "gpt-4o",
    Messages: []schemas.Message{
        {
            Role: "user",
            Content: []schemas.ContentPart{
                {
                    Type: "text",
                    Text: "What's in this image?",
                },
                {
                    Type: "image_url",
                    ImageURL: &schemas.ImageURL{
                        URL: "https://example.com/image.jpg",
                    },
                },
            },
        },
    },
}
```

</details>

<details>
<summary><strong>🌐 HTTP Transport Usage</strong></summary>

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
          {"type": "text", "text": "What is in this image?"},
          {"type": "image_url", "image_url": {"url": "https://example.com/image.jpg"}}
        ]
      }
    ]
  }'
```

</details>

### Function/Tool Calling

<details>
<summary><strong>🔧 Go Package Usage</strong></summary>

```go
// Define tools
tools := []schemas.Tool{
    {
        Type: "function",
        Function: &schemas.Function{
            Name:        "get_weather",
            Description: "Get current weather for a location",
            Parameters: map[string]interface{}{
                "type": "object",
                "properties": map[string]interface{}{
                    "location": map[string]interface{}{
                        "type":        "string",
                        "description": "City and state, e.g. San Francisco, CA",
                    },
                },
                "required": []string{"location"},
            },
        },
    },
}

request := &schemas.ChatCompletionRequest{
    Provider: "openai",
    Model:    "gpt-4o",
    Messages: []schemas.Message{
        {Role: "user", Content: "What's the weather in New York?"},
    },
    Params: schemas.RequestParams{
        Tools: tools,
    },
}
```

</details>

<details>
<summary><strong>🌐 HTTP Transport Usage</strong></summary>

```bash
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "provider": "openai",
    "model": "gpt-4o",
    "messages": [
      {"role": "user", "content": "What is the weather in New York?"}
    ],
    "params": {
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
                  "description": "City and state, e.g. San Francisco, CA"
                }
              },
              "required": ["location"]
            }
          }
        }
      ]
    }
  }'
```

</details>

---

## 🔐 Security & Best Practices

### API Key Security

- **Environment Variables**: Always use environment variables for API keys
- **Key Rotation**: Regularly rotate OpenAI API keys
- **Organization Isolation**: Use separate keys for different environments
- **Usage Monitoring**: Monitor API key usage and costs

### Rate Limiting & Quotas

OpenAI implements rate limiting based on:

- **Requests per minute (RPM)**
- **Tokens per minute (TPM)**
- **Tokens per day (TPD)**

Configure appropriate timeouts and retry logic:

```go
NetworkConfig: schemas.NetworkConfig{
    DefaultRequestTimeoutInSeconds: 60,  // Longer timeout for complex requests
    MaxRetries:                     3,   // Retry failed requests
    RetryBackoffInitial:           200 * time.Millisecond,
    RetryBackoffMax:               5 * time.Second,
}
```

---

## 📊 Monitoring & Debugging

### Key Metrics

- **Request Success Rate**: Monitor API call success rates
- **Response Times**: Track latency to OpenAI API
- **Token Usage**: Monitor input/output token consumption
- **Cost Tracking**: Track costs across different models

### Common Issues & Solutions

| Issue                    | Cause               | Solution                                  |
| ------------------------ | ------------------- | ----------------------------------------- |
| **Rate Limit Errors**    | Too many requests   | Implement backoff, distribute across keys |
| **Timeout Errors**       | Network/API latency | Increase timeout, add retries             |
| **Invalid Model**        | Model name mismatch | Check available models, update config     |
| **Authentication Error** | Invalid/expired key | Verify API key, check organization        |

---

## 🔗 Related Documentation

- **[Provider System Overview](README.md)** - Multi-provider setup and fallbacks
- **[Azure OpenAI Configuration](azure.md)** - Enterprise OpenAI deployment
- **[Fallback Configuration](../fallbacks.md)** - Implementing provider fallbacks
- **[Error Handling](../../usage/errors.md)** - OpenAI error codes and handling

---

**⚡ Ready for production?** Check our [performance tuning guide](../../guides/tutorials/production-setup.md) for optimizing OpenAI at scale.
