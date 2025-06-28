# 🧠 Anthropic Provider Configuration

Complete guide for configuring Anthropic Claude models with Bifrost, including safety features, tool calling, and advanced configurations.

## 📋 Supported Models

| Model Family       | Models                                                                          | Capabilities                       |
| ------------------ | ------------------------------------------------------------------------------- | ---------------------------------- |
| **Claude 3**       | `claude-3-opus-20240229`, `claude-3-sonnet-20240229`, `claude-3-haiku-20240307` | Chat, Vision, Tools, Large context |
| **Claude 2**       | `claude-2.1`, `claude-2.0`                                                      | Chat, Large context                |
| **Claude Instant** | `claude-instant-1.2`                                                            | Fast responses, Cost-effective     |

---

## 🚀 Quick Start

<details>
<summary><strong>🔧 Go Package Usage</strong></summary>

### Basic Configuration

```go
import "github.com/maximhq/bifrost/core/schemas"

account := &schemas.Account{
    Providers: map[string]schemas.ProviderConfig{
        "anthropic": {
            Keys: []schemas.Key{
                {
                    Value:  "sk-ant-your-api-key",
                    Models: []string{"claude-3-sonnet-20240229", "claude-3-haiku-20240307"},
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
        "anthropic": {
            Keys: []schemas.Key{
                {
                    Value:  "sk-ant-key-1",
                    Models: []string{"claude-3-opus-20240229"},
                    Weight: 0.4, // 40% for Opus (expensive)
                },
                {
                    Value:  "sk-ant-key-2",
                    Models: []string{"claude-3-sonnet-20240229"},
                    Weight: 0.4, // 40% for Sonnet (balanced)
                },
                {
                    Value:  "sk-ant-key-3",
                    Models: []string{"claude-3-haiku-20240307"},
                    Weight: 0.2, // 20% for Haiku (fast)
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
    Provider: "anthropic",
    Model:    "claude-3-sonnet-20240229",
    Messages: []schemas.Message{
        {Role: "user", Content: "Explain the ethical implications of AI in healthcare."},
    },
    Params: schemas.RequestParams{
        MaxTokens:   1000,
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
    "anthropic": {
      "keys": [
        {
          "value": "env.ANTHROPIC_API_KEY",
          "models": ["claude-3-sonnet-20240229", "claude-3-haiku-20240307"],
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
    "anthropic": {
      "keys": [
        {
          "value": "env.ANTHROPIC_API_KEY_1",
          "models": ["claude-3-opus-20240229"],
          "weight": 0.3
        },
        {
          "value": "env.ANTHROPIC_API_KEY_2",
          "models": ["claude-3-sonnet-20240229"],
          "weight": 0.5
        },
        {
          "value": "env.ANTHROPIC_API_KEY_3",
          "models": ["claude-3-haiku-20240307"],
          "weight": 0.2
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
    "provider": "anthropic",
    "model": "claude-3-sonnet-20240229",
    "messages": [
      {"role": "user", "content": "Explain the ethical implications of AI in healthcare."}
    ],
    "params": {
      "max_tokens": 1000,
      "temperature": 0.7
    }
  }'

# Text completion (legacy format)
curl -X POST http://localhost:8080/v1/text/completions \
  -H "Content-Type: application/json" \
  -d '{
    "provider": "anthropic",
    "model": "claude-2.1",
    "text": "The future of sustainable energy is",
    "params": {
      "max_tokens": 500,
      "temperature": 0.8
    }
  }'
```

</details>

---

## ⚙️ Advanced Configuration

### Custom Headers & Enterprise Setup

<details>
<summary><strong>🔧 Go Package Configuration</strong></summary>

```go
// Enterprise Anthropic deployment
providerConfig := schemas.ProviderConfig{
    Keys: []schemas.Key{
        {Value: "sk-ant-your-api-key", Models: []string{"claude-3-sonnet-20240229"}, Weight: 1.0},
    },
    NetworkConfig: schemas.NetworkConfig{
        BaseURL: "https://api.anthropic.com", // Custom endpoint if needed
        ExtraHeaders: map[string]string{
            "anthropic-version": "2023-06-01",
            "User-Agent":        "MyApp/1.0",
        },
        DefaultRequestTimeoutInSeconds: 90,  // Longer timeout for Claude
        MaxRetries:                     3,
        RetryBackoffInitial:           300 * time.Millisecond,
        RetryBackoffMax:               10 * time.Second,
    },
}
```

</details>

<details>
<summary><strong>🌐 HTTP Transport Configuration</strong></summary>

```json
{
  "providers": {
    "anthropic": {
      "keys": [
        {
          "value": "env.ANTHROPIC_API_KEY",
          "models": ["claude-3-sonnet-20240229"],
          "weight": 1.0
        }
      ],
      "network_config": {
        "base_url": "https://api.anthropic.com",
        "extra_headers": {
          "anthropic-version": "2023-06-01"
        },
        "default_request_timeout_in_seconds": 90,
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
// High-performance configuration for Claude
providerConfig := schemas.ProviderConfig{
    Keys: []schemas.Key{
        {Value: "your-api-key", Models: []string{"claude-3-sonnet-20240229"}, Weight: 1.0},
    },
    ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
        Concurrency: 15,  // Moderate concurrency for Claude
        BufferSize:  150, // Buffer for burst traffic
    },
}
```

</details>

<details>
<summary><strong>🌐 HTTP Transport Configuration</strong></summary>

```json
{
  "providers": {
    "anthropic": {
      "keys": [...],
      "concurrency_and_buffer_size": {
        "concurrency": 15,
        "buffer_size": 150
      }
    }
  }
}
```

</details>

---

## 🛠️ Advanced Features

### Vision Models (Claude 3)

<details>
<summary><strong>🔧 Go Package Usage</strong></summary>

```go
// Image analysis with Claude 3
request := &schemas.ChatCompletionRequest{
    Provider: "anthropic",
    Model:    "claude-3-sonnet-20240229",
    Messages: []schemas.Message{
        {
            Role: "user",
            Content: []schemas.ContentPart{
                {
                    Type: "text",
                    Text: "What do you see in this image? Please describe it in detail.",
                },
                {
                    Type: "image_url",
                    ImageURL: &schemas.ImageURL{
                        URL: "data:image/jpeg;base64,/9j/4AAQSkZJRgABAQAAAQ...", // Base64 image
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
    "provider": "anthropic",
    "model": "claude-3-sonnet-20240229",
    "messages": [
      {
        "role": "user",
        "content": [
          {"type": "text", "text": "What do you see in this image?"},
          {"type": "image_url", "image_url": {"url": "data:image/jpeg;base64,..."}}
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
// Define tools for Claude
tools := []schemas.Tool{
    {
        Type: "function",
        Function: &schemas.Function{
            Name:        "analyze_sentiment",
            Description: "Analyze the sentiment of given text",
            Parameters: map[string]interface{}{
                "type": "object",
                "properties": map[string]interface{}{
                    "text": map[string]interface{}{
                        "type":        "string",
                        "description": "The text to analyze for sentiment",
                    },
                    "detailed": map[string]interface{}{
                        "type":        "boolean",
                        "description": "Whether to provide detailed analysis",
                    },
                },
                "required": []string{"text"},
            },
        },
    },
}

request := &schemas.ChatCompletionRequest{
    Provider: "anthropic",
    Model:    "claude-3-sonnet-20240229",
    Messages: []schemas.Message{
        {Role: "user", Content: "Analyze the sentiment of this review: 'This product is amazing!'"},
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
    "provider": "anthropic",
    "model": "claude-3-sonnet-20240229",
    "messages": [
      {"role": "user", "content": "Analyze the sentiment of this text"}
    ],
    "params": {
      "tools": [
        {
          "type": "function",
          "function": {
            "name": "analyze_sentiment",
            "description": "Analyze the sentiment of given text",
            "parameters": {
              "type": "object",
              "properties": {
                "text": {"type": "string", "description": "Text to analyze"},
                "detailed": {"type": "boolean", "description": "Detailed analysis"}
              },
              "required": ["text"]
            }
          }
        }
      ]
    }
  }'
```

</details>

---

## 🔐 Security & Safety Features

### Claude's Built-in Safety

Anthropic's Claude models come with built-in safety features:

- **Constitutional AI**: Trained to be helpful, harmless, and honest
- **Content Filtering**: Automatic filtering of harmful content
- **Bias Mitigation**: Reduced bias in responses
- **Privacy Protection**: No training on user conversations

### API Key Security

- **Environment Variables**: Always use environment variables for API keys
- **Key Rotation**: Regularly rotate Anthropic API keys
- **Organization Isolation**: Use separate keys for different environments
- **Usage Monitoring**: Monitor API key usage and costs

### Rate Limiting & Usage

Anthropic implements rate limiting based on:

- **Requests per minute**
- **Tokens per minute**
- **Monthly usage quotas**

Configure appropriate timeouts:

```go
NetworkConfig: schemas.NetworkConfig{
    DefaultRequestTimeoutInSeconds: 90,  // Claude can be slower for complex tasks
    MaxRetries:                     3,   // Retry failed requests
    RetryBackoffInitial:           300 * time.Millisecond,
    RetryBackoffMax:               10 * time.Second,
}
```

---

## 📊 Model Selection Guide

### When to Use Each Model

| Model               | Use Case                           | Speed  | Cost   | Context Length |
| ------------------- | ---------------------------------- | ------ | ------ | -------------- |
| **Claude 3 Opus**   | Complex analysis, creative writing | Slower | High   | 200K tokens    |
| **Claude 3 Sonnet** | Balanced tasks, general use        | Medium | Medium | 200K tokens    |
| **Claude 3 Haiku**  | Simple tasks, quick responses      | Fast   | Low    | 200K tokens    |
| **Claude 2.1**      | Legacy support, long context       | Medium | Medium | 200K tokens    |

### Performance Characteristics

- **Opus**: Best quality, highest reasoning capability
- **Sonnet**: Balanced performance and cost
- **Haiku**: Fastest responses, most cost-effective
- **Claude 2.1**: Strong performance, reliable fallback

---

## 📊 Monitoring & Debugging

### Key Metrics

- **Request Success Rate**: Monitor API call success rates
- **Response Times**: Track latency to Anthropic API
- **Token Usage**: Monitor input/output token consumption
- **Cost Tracking**: Track costs across different Claude models

### Common Issues & Solutions

| Issue                    | Cause                    | Solution                                  |
| ------------------------ | ------------------------ | ----------------------------------------- |
| **Rate Limit Errors**    | Too many requests        | Implement backoff, distribute across keys |
| **Timeout Errors**       | Complex requests         | Increase timeout for Claude models        |
| **Content Filter**       | Safety filters triggered | Review and modify request content         |
| **Authentication Error** | Invalid/expired key      | Verify API key, check account status      |

---

## 🔗 Related Documentation

- **[Provider System Overview](README.md)** - Multi-provider setup and fallbacks
- **[OpenAI Configuration](openai.md)** - Alternative provider setup
- **[Fallback Configuration](../fallbacks.md)** - Implementing provider fallbacks
- **[Error Handling](../../usage/errors.md)** - Anthropic error codes and handling

---

**⚡ Ready for production?** Check our [performance tuning guide](../../guides/tutorials/production-setup.md) for optimizing Claude at scale.
