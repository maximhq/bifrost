# 📚 Canonical Examples

Reusable code examples, implementations, and commands referenced throughout Bifrost documentation.

## 🏗️ Go Package Examples

### Basic Account Implementation

```go
package main

import (
    "os"
    "github.com/maximhq/bifrost/core/schemas"
)

// Canonical Account implementation - referenced throughout docs
type MyAccount struct{}

func (a *MyAccount) GetConfiguredProviders() ([]schemas.ModelProvider, error) {
    return []schemas.ModelProvider{schemas.OpenAI}, nil
}

func (a *MyAccount) GetKeysForProvider(provider schemas.ModelProvider) ([]schemas.Key, error) {
    return []schemas.Key{{
        Value:  os.Getenv("OPENAI_API_KEY"),
        Models: []string{"gpt-4o-mini"},
        Weight: 1.0,
    }}, nil
}

func (a *MyAccount) GetConfigForProvider(provider schemas.ModelProvider) (*schemas.ProviderConfig, error) {
    return &schemas.ProviderConfig{
        NetworkConfig:            schemas.DefaultNetworkConfig,
        ConcurrencyAndBufferSize: schemas.DefaultConcurrencyAndBufferSize,
    }, nil
}
```

### Multi-Provider Account Implementation

```go
type MultiProviderAccount struct{}

func (a *MultiProviderAccount) GetConfiguredProviders() ([]schemas.ModelProvider, error) {
    return []schemas.ModelProvider{schemas.OpenAI, schemas.Anthropic}, nil
}

func (a *MultiProviderAccount) GetKeysForProvider(provider schemas.ModelProvider) ([]schemas.Key, error) {
    switch provider {
    case schemas.OpenAI:
        return []schemas.Key{{
            Value: os.Getenv("OPENAI_API_KEY"),
            Models: []string{"gpt-4o-mini"},
            Weight: 1.0,
        }}, nil
    case schemas.Anthropic:
        return []schemas.Key{{
            Value: os.Getenv("ANTHROPIC_API_KEY"),
            Models: []string{"claude-3-sonnet-20240229"},
            Weight: 1.0,
        }}, nil
    }
    return nil, nil
}

func (a *MultiProviderAccount) GetConfigForProvider(provider schemas.ModelProvider) (*schemas.ProviderConfig, error) {
    return &schemas.ProviderConfig{
        NetworkConfig:            schemas.DefaultNetworkConfig,
        ConcurrencyAndBufferSize: schemas.DefaultConcurrencyAndBufferSize,
    }, nil
}
```

### Basic Chat Completion Request

```go
// Canonical chat completion example - referenced throughout docs
response, err := client.ChatCompletionRequest(context.Background(), &schemas.BifrostRequest{
    Provider: schemas.OpenAI,
    Model:    "gpt-4o-mini",
    Input: schemas.RequestInput{
        ChatCompletionInput: &[]schemas.BifrostMessage{{
            Role: schemas.ModelChatMessageRoleUser,
            Content: schemas.MessageContent{
                ContentStr: &[]string{"Hello! What is Bifrost?"}[0],
            },
        }},
    },
})
```

### Request with Fallbacks

```go
// Canonical fallback example - referenced throughout docs
response, err := client.ChatCompletionRequest(context.Background(), &schemas.BifrostRequest{
    Provider: schemas.OpenAI,
    Model:    "gpt-4o-mini",
    Input: schemas.RequestInput{
        ChatCompletionInput: &[]schemas.BifrostMessage{{
            Role: schemas.ModelChatMessageRoleUser,
            Content: schemas.MessageContent{
                ContentStr: &[]string{"Hello! What is Bifrost?"}[0],
            },
        }},
    },
    Fallbacks: []schemas.Fallback{
        {Provider: schemas.Anthropic, Model: "claude-3-sonnet-20240229"},
        {Provider: schemas.Cohere, Model: "command"},
    },
})
```

---

## 🌐 HTTP API Examples

### Basic Chat Completion

```bash
# Canonical curl example - referenced throughout docs
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "provider": "openai",
    "model": "gpt-4o-mini",
    "messages": [
      {"role": "user", "content": "Hello! What is Bifrost?"}
    ]
  }'
```

### Chat with Parameters

```bash
# Canonical parameterized request - referenced throughout docs
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "provider": "openai",
    "model": "gpt-4o-mini",
    "messages": [
      {"role": "user", "content": "Write a creative story"}
    ],
    "params": {
      "temperature": 0.9,
      "max_tokens": 500
    }
  }'
```

### Request with Fallbacks

```bash
# Canonical fallback request - referenced throughout docs
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "provider": "openai",
    "model": "gpt-4o",
    "messages": [{"role": "user", "content": "Hello!"}],
    "fallbacks": [
      {"provider": "anthropic", "model": "claude-3-sonnet-20240229"}
    ]
  }'
```

---

## 🐳 Docker Examples

### Basic Single Provider

```bash
# Canonical Docker setup - referenced throughout docs
docker run -p 8080:8080 \
  -v $(pwd)/config.json:/app/config/config.json \
  -e OPENAI_API_KEY \
  maximhq/bifrost
```

### Multi-Provider Setup

```bash
# Canonical multi-provider Docker - referenced throughout docs
docker run -p 8080:8080 \
  -v $(pwd)/config.json:/app/config/config.json \
  -e OPENAI_API_KEY \
  -e ANTHROPIC_API_KEY \
  -e COHERE_API_KEY \
  maximhq/bifrost
```

---

## 📋 Configuration Examples

### Basic Provider Config

```json
{
  "providers": {
    "openai": {
      "keys": [
        {
          "value": "env.OPENAI_API_KEY",
          "models": ["gpt-4o-mini"],
          "weight": 1.0
        }
      ]
    }
  }
}
```

### Multi-Provider Config

```json
{
  "providers": {
    "openai": {
      "keys": [
        {
          "value": "env.OPENAI_API_KEY",
          "models": ["gpt-4o-mini"],
          "weight": 1.0
        }
      ]
    },
    "anthropic": {
      "keys": [
        {
          "value": "env.ANTHROPIC_API_KEY",
          "models": ["claude-3-sonnet-20240229"],
          "weight": 1.0
        }
      ]
    }
  }
}
```

---

## 🔌 Plugin Examples

### Basic Logging Plugin

```go
// Canonical logging plugin - referenced throughout docs
type LoggingPlugin struct {
    name string
}

func (p *LoggingPlugin) GetName() string {
    return p.name
}

func (p *LoggingPlugin) PreHook(ctx *context.Context, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.PluginShortCircuit, error) {
    fmt.Printf("[%s] PreHook: Request to %s model %s\n",
        time.Now().Format(time.RFC3339), req.Provider, req.Model)
    return req, nil, nil
}

func (p *LoggingPlugin) PostHook(ctx *context.Context, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
    if err != nil {
        fmt.Printf("[%s] PostHook: Request failed: %s\n",
            time.Now().Format(time.RFC3339), err.Error.Message)
    } else {
        fmt.Printf("[%s] PostHook: Request succeeded\n",
            time.Now().Format(time.RFC3339))
    }
    return result, err, nil
}

func (p *LoggingPlugin) Cleanup() error {
    return nil
}
```

---

## 📝 Usage Notes

**This file serves as the canonical source for all examples used throughout the documentation.**

When updating examples, update them here first, then reference with backlinks in other documentation files.

All examples use the latest schema definitions from `/core/schemas/` and follow best practices for error handling and resource cleanup.
