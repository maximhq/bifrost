# 🔗 Providers

Multi-provider support with unified API across all AI providers. Switch between providers seamlessly or configure automatic fallbacks.

## 🎯 Supported Providers

| Provider              | Models                                 | Features                            | Enterprise |
| --------------------- | -------------------------------------- | ----------------------------------- | ---------- |
| **OpenAI**         | GPT-4o, GPT-4 Turbo, GPT-4, GPT-3.5    | Function calling, streaming, vision, audio | ✅         |
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

func (a *MyAccount) GetKeysForProvider(ctx *context.Context, provider schemas.ModelProvider) ([]schemas.Key, error) {
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
func (a *MyAccount) GetKeysForProvider(ctx *context.Context, provider schemas.ModelProvider) ([]schemas.Key, error) {
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
          "models": ["anthropic.claude-3-5-sonnet-20241022-v2:0"],
          "weight": 1.0,
          "bedrock_key_config": {
            "access_key": "env.AWS_ACCESS_KEY_ID",
            "secret_key": "env.AWS_SECRET_ACCESS_KEY",
            "session_token": "env.AWS_SESSION_TOKEN",
            "region": "us-east-1",
            "arn": "arn:aws:iam::123456789012:role/BedrockRole"
          }
        }
      ],
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
func (a *MyAccount) GetKeysForProvider(ctx *context.Context, provider schemas.ModelProvider) ([]schemas.Key, error) {
    if provider == schemas.Azure {
        return []schemas.Key{
            {
                Value: "your-azure-api-key", 
                Models: []string{"gpt-4o"}, // These models are mapped to the deployment
                Weight: 1.0,
                AzureKeyConfig: &schemas.AzureKeyConfig{
                    Endpoint: "https://your-resource.openai.azure.com",
                    Deployments: map[string]string{
                        "gpt-4o": "gpt-4o-deployment",
                    },
                    APIVersion: StrPtr("2024-02-15-preview"),
                },
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
    "azure": {
      "keys": [
        {
          "value": "env.AZURE_OPENAI_API_KEY",
          "models": ["gpt-4o"],
          "weight": 1.0,
          "azure_key_config": {
            "endpoint": "https://your-resource.openai.azure.com",
            "deployments": {
              "gpt-4o": "gpt-4o-deployment"
            },
            "api_version": "2024-02-15-preview"
          }
        }
      ]
    }
  }
}
```

</details>

<details>
<summary><strong>Google Vertex AI Configuration</strong></summary>

**Go Package:**

```go
func (a *MyAccount) GetKeysForProvider(ctx *context.Context, provider schemas.ModelProvider) ([]schemas.Key, error) {
    if provider == schemas.Vertex {
        return []schemas.Key{
            {
                Models: []string{"gemini-pro"}, // These models are just for mapping to keys
                Weight: 1.0,
                VertexKeyConfig: &schemas.VertexKeyConfig{
                    ProjectID: "your-project-id",
                    Location:  "us-central1",
                    AuthCredentials: os.Getenv("VERTEX_AUTH_CREDENTIALS"), // Or read from file
                },
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
    "vertex": {
      "keys": [
        {
          "models": ["google/gemini-2.0-flash-001"],
          "weight": 1.0,
          "vertex_key_config": {
            "project_id": "your-project-id",
            "region": "us-central1",
            "auth_credentials": "env.VERTEX_AUTH_CREDENTIALS"
          }
        }
      ]
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

func (a *MyAccount) GetKeysForProvider(ctx *context.Context, provider schemas.ModelProvider) ([]schemas.Key, error) {
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
| **🔊 Audio Speech**   | ✅     | ❌        | ❌    | ❌      | ❌     | ❌     | ❌      | ❌     | ❌     | ❌     |
| **🎤 Transcription** | ✅     | ❌        | ❌    | ❌      | ❌     | ❌     | ❌      | ❌     | ❌     | ❌     |
| **Custom Base URL**  | ✅     | ✅        | ✅    | ❌      | ❌     | ✅     | ✅      | ✅     | ✅     | ✅     |
| **Proxy Support**    | ✅     | ✅        | ✅    | ❌      | ❌     | ✅     | ✅      | ✅     | ✅     | ✅      |

### **🔊 Audio Features Details**

| Provider | Speech Synthesis | Transcription | Models Available | Streaming Support |
| -------- | --------------- | ------------- | ---------------- | ----------------- |
| **OpenAI** | ✅ Full Support | ✅ Full Support | `tts-1`, `tts-1-hd`, `whisper-1` | ✅ Both |
| **Anthropic** | ❌ Not Available | ❌ Not Available | N/A | ❌ N/A |
| **Azure OpenAI** | ❌ Not Available | ❌ Not Available | N/A | ❌ N/A |
| **Amazon Bedrock** | ❌ Not Available | ❌ Not Available | N/A | ❌ N/A |
| **Google Vertex** | ❌ Not Available | ❌ Not Available | N/A | ❌ N/A |
| **Cohere** | ❌ Not Available | ❌ Not Available | N/A | ❌ N/A |
| **Mistral** | ❌ Not Available | ❌ Not Available | N/A | ❌ N/A |
| **Ollama** | ❌ Not Available | ❌ Not Available | N/A | ❌ N/A |
| **Groq** | ❌ Not Available | ❌ Not Available | N/A | ❌ N/A |
| **SGLang** | ❌ Not Available | ❌ Not Available | N/A | ❌ N/A |

> **📝 Note:** Audio features are currently supported only through OpenAI. Other providers return `unsupported operation` errors for audio requests. This allows you to use fallback chains where non-audio requests can still use other providers.

**OpenAI Audio Features:**

- **Speech Synthesis (TTS)**: Convert text to speech with 6 voice options
  - Models: `tts-1` (standard), `tts-1-hd` (higher quality)
  - Voices: `alloy`, `echo`, `fable`, `onyx`, `nova`, `shimmer`
  - Formats: `mp3`, `opus`, `aac`, `flac`, `wav`, `pcm`
  - Streaming: ✅ Server-Sent Events (SSE)

- **Audio Transcription (STT)**: Convert speech to text with advanced features
  - Model: `whisper-1`
  - Features: Word-level timing, segment breakdown, language detection
  - Formats: `json`, `text`, `srt`, `verbose_json`, `vtt`
  - Languages: 50+ supported languages with auto-detection
  - File formats: `mp3`, `mp4`, `mpeg`, `mpga`, `m4a`, `wav`, `webm`
  - Streaming: ✅ Real-time transcription

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
