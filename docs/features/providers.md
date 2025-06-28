# Multi-Provider Support

Bifrost provides unified access to **8 major AI providers** through a single, consistent interface. Switch between providers seamlessly or configure automatic fallbacks for maximum reliability.

## 🎯 Supported Providers

| Provider              | Models                                 | Features                            | Enterprise |
| --------------------- | -------------------------------------- | ----------------------------------- | ---------- |
| **🤖 OpenAI**         | GPT-4o, GPT-4 Turbo, GPT-4, GPT-3.5    | Function calling, streaming, vision | ✅         |
| **🧠 Anthropic**      | Claude 3.5 Sonnet, Claude 3 Opus/Haiku | Tool use, vision, 200K context      | ✅         |
| **☁️ Azure OpenAI**   | Enterprise GPT deployment              | Private networks, compliance        | ✅         |
| **🏛️ Amazon Bedrock** | Claude, Titan, Cohere, Meta            | Multi-model platform, VPC           | ✅         |
| **🔍 Google Vertex**  | Gemini Pro, PaLM, Codey                | Enterprise AI platform              | ✅         |
| **💬 Cohere**         | Command, Embed, Rerank                 | Enterprise NLP, multilingual        | ✅         |
| **🌟 Mistral**        | Mistral Large, Medium, Small           | European AI, cost-effective         | ✅         |
| **🏠 Ollama**         | Llama, Mistral, CodeLlama              | Local deployment, privacy           | ✅         |

---

## ⚡ Quick Setup

<details open>
<summary><strong>🔧 Go Package Usage</strong></summary>

```go
package main

import (
    "context"
    "os"
    "github.com/maximhq/bifrost/core"
    "github.com/maximhq/bifrost/core/schemas"
)

// 📖 [Multi-Provider Account Implementation →](../usage/examples.md#multi-provider-account-implementation)

func main() {
    account := &MyAccount{}

    // Initialize Bifrost with multi-provider support
    bf, err := bifrost.Init(schemas.BifrostConfig{
        Account:         account,
        InitialPoolSize: 100,
        Logger:          bifrost.NewDefaultLogger(schemas.LogLevelInfo),
    })
    if err != nil {
        panic(err)
    }
    defer bf.Cleanup()

    // Use any provider seamlessly
    response, err := bf.ChatCompletion(context.Background(), schemas.BifrostRequest{
        Provider: schemas.OpenAI,
        Model:    "gpt-4o-mini",
        Input: schemas.RequestInput{
            ChatCompletionInput: &[]schemas.BifrostMessage{
                {
                    Role:    schemas.ModelChatMessageRoleUser,
                    Content: schemas.MessageContent{ContentStr: &[]string{"Hello from OpenAI!"}[0]},
                },
            },
        },
    })
}
```

**📖 [Complete Go Package Setup Guide →](../quick-start/go-package.md)**

</details>

<details>
<summary><strong>🌐 HTTP Transport Usage</strong></summary>

**1. Configuration file (`config.json`):**

```json
{
  "providers": {
    "openai": {
      "keys": [
        {
          "value": "env.OPENAI_API_KEY",
          "models": ["gpt-4o-mini", "gpt-4"],
          "weight": 1.0
        }
      ]
    },
    "anthropic": {
      "keys": [
        {
          "value": "env.ANTHROPIC_API_KEY",
          "models": ["claude-3-5-sonnet-20241022"],
          "weight": 1.0
        }
      ]
    },
    "bedrock": {
      "keys": [
        {
          "value": "env.AWS_ACCESS_KEY_ID",
          "models": ["anthropic.claude-3-5-sonnet-20241022-v2:0"],
          "weight": 1.0
        }
      ],
      "meta_config": {
        "region": "us-east-1",
        "secret_access_key": "env.AWS_SECRET_ACCESS_KEY"
      }
    }
  }
}
```

**2. Start Bifrost server:**

```bash
docker run -p 8080:8080 \
  -v $(pwd)/config.json:/app/config/config.json \
  -e OPENAI_API_KEY \
  -e ANTHROPIC_API_KEY \
  -e AWS_ACCESS_KEY_ID \
  -e AWS_SECRET_ACCESS_KEY \
  maximhq/bifrost
```

**3. Use any provider:**

```bash
# OpenAI
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "provider": "openai",
    "model": "gpt-4o-mini",
    "messages": [{"role": "user", "content": "Hello from OpenAI!"}]
  }'

# Anthropic
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "provider": "anthropic",
    "model": "claude-3-5-sonnet-20241022",
    "messages": [{"role": "user", "content": "Hello from Anthropic!"}]
  }'

# Bedrock
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "provider": "bedrock",
    "model": "anthropic.claude-3-5-sonnet-20241022-v2:0",
    "messages": [{"role": "user", "content": "Hello from Bedrock!"}]
  }'
```

**📖 [Complete HTTP Transport Setup Guide →](../quick-start/http-transport.md)**

</details>

---

## 🎯 Next Steps

Ready to implement multi-provider AI? Choose your path:

| **Feature**                   | **Documentation**                                         | **Time Investment** |
| ----------------------------- | --------------------------------------------------------- | ------------------- |
| **🔄 Fallback Systems**       | [Fallback Guide](fallbacks.md)                            | 5 minutes           |
| **🛠️ MCP Tool Integration**   | [MCP Guide](mcp-integration.md)                           | 10 minutes          |
| **🔌 Custom Plugins**         | [Plugin System](plugins.md)                               | 15 minutes          |
| **📊 Monitoring**             | [Observability Guide](observability.md)                   | 10 minutes          |
| **🏗️ Architecture Deep Dive** | [System Architecture](../architecture/system-overview.md) | 20 minutes          |

### 📖 Complete Reference

- **[📋 API Reference](../usage/)** - Complete API documentation
- **[🏗️ Production Deployment](../configuration/deployment/)** - Scale for enterprise
- **[🔧 Configuration Guide](../configuration/)** - Advanced setup options
- **[❓ Troubleshooting](../guides/troubleshooting.md)** - Common issues and solutions

---

**🚀 Ready to deploy?** Start with our [📖 Quick Start Guides](../quick-start/) to get running in under 2 minutes.
