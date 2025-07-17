# 🔗 Providers

Multi-provider support with unified API across all AI providers. Switch between providers seamlessly or configure automatic fallbacks.

## 🎯 Supported Providers

| Provider              | Models                                 | Features                            | Enterprise |
| --------------------- | -------------------------------------- | ----------------------------------- | ---------- |
| **OpenAI**         | GPT-4o, GPT-4 Turbo, GPT-4, GPT-3.5    | Function calling, streaming, vision | ✅         |
| **Anthropic**      | Claude 3.5 Sonnet, Claude 3 Opus/Haiku | Tool use, vision, 200K context      | ✅         |
| **Azure OpenAI**   | Enterprise GPT deployment              | Private networks, compliance        | ✅         |
| **Amazon Bedrock** | Claude, Titan, Cohere, Meta            | Multi-model platform, VPC           | ✅         |
| **Google Vertex**  | Gemini Pro, PaLM, Codey                | Enterprise AI platform              | ✅         |
| **Cohere**         | Command, Embed, Rerank                 | Enterprise NLP, multilingual        | ✅         |
| **Mistral**        | Mistral Large, Medium, Small           | European AI, cost-effective         | ✅         |
| **Ollama**         | Llama, Mistral, CodeLlama              | Local deployment, privacy           | ✅         |
| **Groq**           | Mixtral, Llama, Gemma                  | Enterprise AI platform              | ✅         |
| **SGLang**         | Qwen                 | Enterprise AI platform              | ✅         |

---

## ⚡ Basic Provider Usage

### Single Provider Setup

<details open>
<summary><strong>🔧 Go Package Usage</strong></summary>

```go
package main

import (
    "context"
    "fmt"
    "os"
    "github.com/maximhq/bifrost/core"
    "github.com/maximhq/bifrost/core/schemas"
)

// Account implementation
type MyAccount struct{}

func (a *MyAccount) GetConfiguredProviders() ([]schemas.ModelProvider, error) {
    return []schemas.ModelProvider{schemas.OpenAI}, nil
}

func (a *MyAccount) GetKeysForProvider(provider schemas.ModelProvider) ([]schemas.Key, error) {
    switch provider {
    case schemas.OpenAI:
        return []schemas.Key{
            {
                Value:  os.Getenv("OPENAI_API_KEY"),
                Models: []string{"gpt-4o-mini", "gpt-4o"},
                Weight: 1.0,
            },
        }, nil
    }
    return nil, fmt.Errorf("provider %s not configured", provider)
}

func (a *MyAccount) GetConfigForProvider(provider schemas.ModelProvider) (*schemas.ProviderConfig, error) {
    return &schemas.ProviderConfig{
        NetworkConfig:            schemas.DefaultNetworkConfig,
        ConcurrencyAndBufferSize: schemas.DefaultConcurrencyAndBufferSize,
    }, nil
}

func main() {
    account := &MyAccount{}

    // Initialize Bifrost
    bf, err := bifrost.Init(schemas.BifrostConfig{
        Account:         account,
        InitialPoolSize: 100,
        Logger:          bifrost.NewDefaultLogger(schemas.LogLevelInfo),
    })
    if err != nil {
        panic(err)
    }
    defer bf.Cleanup()

    // Use OpenAI
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

    if err != nil {
        panic(err)
    }

    fmt.Printf("Response: %+v\n", response)
}
```

</details>

<details>
<summary><strong>🌐 HTTP Transport Usage</strong></summary>

**1. Configuration (`config.json`):**

```json
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
```

**2. Environment Variables:**

```bash
export OPENAI_API_KEY=your_openai_api_key
```

**3. Usage Examples:**

```bash
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "openai/gpt-4o-mini",
    "messages": [{"role": "user", "content": "Hello from OpenAI!"}]
  }'
```

</details>

---

## 🚀 Multi-Provider Setup

Configure multiple providers for fallbacks and load distribution.

<details>
<summary><strong>🔧 Go Package - Multi-Provider</strong></summary>

```go
func (a *MyAccount) GetKeysForProvider(provider schemas.ModelProvider) ([]schemas.Key, error) {
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
    case schemas.Bedrock:
        return []schemas.Key{
            {
                Value:  os.Getenv("AWS_ACCESS_KEY_ID"),
                Models: []string{"anthropic.claude-3-5-sonnet-20241022-v2:0"},
                Weight: 1.0,
            },
        }, nil
    }
    return nil, fmt.Errorf("provider %s not configured", provider)
}

func (a *MyAccount) GetConfigForProvider(provider schemas.ModelProvider) (*schemas.ProviderConfig, error) {
    switch provider {
    case schemas.Bedrock:
        return &schemas.ProviderConfig{
            MetaConfig: map[string]interface{}{
                "region":            "us-east-1",
                "secret_access_key": os.Getenv("AWS_SECRET_ACCESS_KEY"),
            },
        }, nil
    }
    return &schemas.ProviderConfig{}, nil
}

// Usage example with fallback
func useWithFallback(bf *bifrost.Bifrost) {
    providers := []schemas.ModelProvider{
        schemas.OpenAI,
        schemas.Anthropic,
        schemas.Bedrock,
    }

    for _, provider := range providers {
        response, err := bf.ChatCompletion(context.Background(), schemas.BifrostRequest{
            Provider: provider,
            Model:    "gpt-4o-mini", // This will map to equivalent model per provider
            Input: schemas.RequestInput{
                ChatCompletionInput: &[]schemas.BifrostMessage{
                    {
                        Role:    schemas.ModelChatMessageRoleUser,
                        Content: schemas.MessageContent{ContentStr: &[]string{"Hello!"}[0]},
                    },
                },
            },
        })

        if err == nil {
            fmt.Printf("Success with %s: %+v\n", provider, response)
            break
        }
        fmt.Printf("Failed with %s: %v\n", provider, err)
    }
}
```

</details>

<details>
<summary><strong>🌐 HTTP Transport - Multi-Provider</strong></summary>

**Configuration (`config.json`):**

```json
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

**Client-side fallback example:**

```bash
#!/bin/bash

# Try OpenAI first
response=$(curl -s -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "openai/gpt-4o-mini",
    "messages": [{"role": "user", "content": "Hello!"}]
  }')

# Check if request failed, try Anthropic
if [[ $? -ne 0 ]] || [[ $(echo "$response" | jq -r '.error') != "null" ]]; then
  echo "OpenAI failed, trying Anthropic..."
  response=$(curl -s -X POST http://localhost:8080/v1/chat/completions \
    -H "Content-Type: application/json" \
    -d '{
      "provider": "anthropic",
      "model": "claude-3-5-sonnet-20241022",
      "messages": [{"role": "user", "content": "Hello!"}]
    }')
fi

echo "$response"
```

</details>

---

## 🔧 Provider-Specific Configuration

### Enterprise Providers

<details>
<summary><strong>Azure OpenAI Configuration</strong></summary>

**Go Package:**

```go
func (a *MyAccount) GetConfigForProvider(provider schemas.ModelProvider) (*schemas.ProviderConfig, error) {
    if provider == schemas.Azure {
        return &schemas.ProviderConfig{
            NetworkConfig: schemas.NetworkConfig{
                BaseURL: "https://your-resource.openai.azure.com",
            },
            MetaConfig: map[string]interface{}{
                "api_version": "2024-02-15-preview",
                "deployment":  "gpt-4o-deployment",
            },
        }, nil
    }
    return &schemas.ProviderConfig{}, nil
}
```

**HTTP Transport:**

```json
{
  "providers": {
    "azure": {
      "keys": [
        {
          "value": "env.AZURE_OPENAI_API_KEY",
          "models": ["gpt-4o"],
          "weight": 1.0
        }
      ],
      "network_config": {
        "base_url": "https://your-resource.openai.azure.com"
      },
      "meta_config": {
        "api_version": "2024-02-15-preview",
        "deployment": "gpt-4o-deployment"
      }
    }
  }
}
```

</details>

<details>
<summary><strong>Google Vertex AI Configuration</strong></summary>

**Go Package:**

```go
func (a *MyAccount) GetConfigForProvider(provider schemas.ModelProvider) (*schemas.ProviderConfig, error) {
    if provider == schemas.Vertex {
        return &schemas.ProviderConfig{
            MetaConfig: map[string]interface{}{
                "project_id": "your-project-id",
                "location":   "us-central1",
                "credentials_path": "/path/to/service-account.json",
            },
        }, nil
    }
    return &schemas.ProviderConfig{}, nil
}
```

**HTTP Transport:**

```json
{
  "providers": {
    "vertex": {
      "keys": [
        {
          "value": "file:/path/to/service-account.json",
          "models": ["gemini-pro"],
          "weight": 1.0
        }
      ],
      "meta_config": {
        "project_id": "your-project-id",
        "location": "us-central1"
      }
    }
  }
}
```

</details>

<details>
<summary><strong>Local Ollama Configuration</strong></summary>

**Go Package:**

```go
func (a *MyAccount) GetConfigForProvider(provider schemas.ModelProvider) (*schemas.ProviderConfig, error) {
    if provider == schemas.Ollama {
        return &schemas.ProviderConfig{
            NetworkConfig: schemas.NetworkConfig{
                BaseURL: "http://localhost:11434",
            },
        }, nil
    }
    return &schemas.ProviderConfig{}, nil
}

func (a *MyAccount) GetKeysForProvider(provider schemas.ModelProvider) ([]schemas.Key, error) {
    if provider == schemas.Ollama {
        return []schemas.Key{
            {
                Value:  "ollama", // Ollama doesn't need real API keys
                Models: []string{"llama2", "mistral", "codellama"},
                Weight: 1.0,
            },
        }, nil
    }
    return nil, fmt.Errorf("provider not configured")
}
```

**HTTP Transport:**

```json
{
  "providers": {
    "ollama": {
      "keys": [
        {
          "value": "ollama",
          "models": ["llama2", "mistral", "codellama"],
          "weight": 1.0
        }
      ],
      "network_config": {
        "base_url": "http://localhost:11434"
      }
    }
  }
}
```

</details>

---

## 📋 Provider Features Matrix

| Feature              | OpenAI | Anthropic | Azure | Bedrock | Vertex | Cohere | Mistral | Ollama | Groq   | SGLang |  
| -------------------- | ------ | --------- | ----- | ------- | ------ | ------ | ------- | ------ | ------ | ------ |
| **Chat Completion**  | ✅     | ✅        | ✅    | ✅      | ✅     | ✅     | ✅      | ✅     | ✅     | ✅     |
| **Function Calling** | ✅     | ✅        | ✅    | ✅      | ✅     | ❌     | ✅      | ✅     | ✅     | ✅     |
| **Streaming**        | ✅     | ✅        | ✅    | ✅      | ✅     | ✅     | ✅      | ✅     | ✅     | ✅     |
| **Vision**           | ✅     | ✅        |  ✅    | ✅      | ✅     | ❌     | ✅      | ✅     | ❌     | ✅     |
| **JSON Mode**        | ✅     | ✅        | ✅    | ✅      | ✅     | ❌     | ✅      | ✅     | ✅     | ✅     |
| **Custom Base URL**  | ✅     | ✅        | ✅    | ❌      | ❌     | ✅     | ✅      | ✅     | ✅     | ✅     |
| **Proxy Support**    | ✅     | ✅        | ✅    | ❌      | ❌     | ✅     | ✅      | ✅     | ✅     | ✅      |

---

## 🎯 Next Steps

| **Task**                           | **Documentation**                         |
| ---------------------------------- | ----------------------------------------- |
| **🔑 Configure multiple API keys** | [Key Management](key-management.md)       |
| **🌐 Set up networking & proxies** | [Networking](networking.md)               |
| **⚡ Optimize performance**        | [Memory Management](memory-management.md) |
| **❌ Handle errors gracefully**    | [Error Handling](errors.md)               |
| **🔧 Go Package deep dive**        | [Go Package Usage](go-package/)           |
| **🌐 HTTP Transport setup**        | [HTTP Transport Usage](http-transport/)   |

> **💡 Tip:** All responses from Bifrost follow OpenAI's format regardless of the underlying provider, ensuring consistent integration across your application.
