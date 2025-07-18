# 🏛️ Account Interface

Complete guide to implementing the Account interface for provider configuration, key management, and authentication in Bifrost.

> **💡 Quick Start:** See the [30-second setup](../../quickstart/go-package.md) for a minimal Account implementation.

---

## 📋 Interface Overview

The Account interface is your configuration provider that tells Bifrost:

- Which AI providers you want to use
- API keys for each provider
- Provider-specific settings (timeouts, retries, etc.)

```go
type Account interface {
    GetConfiguredProviders() ([]schemas.ModelProvider, error)
    GetKeysForProvider(providerKey schemas.ModelProvider) ([]schemas.Key, error)
    GetConfigForProvider(providerKey schemas.ModelProvider) (*schemas.ProviderConfig, error)
}
```

---

## 🚀 Basic Implementation

### **Minimal Account (Single Provider)**

Perfect for getting started or simple use cases:

```go
package main

import (
    "fmt"
    "os"
    "github.com/maximhq/bifrost/core/schemas"
)

type SimpleAccount struct{}

func (a *SimpleAccount) GetConfiguredProviders() ([]schemas.ModelProvider, error) {
    return []schemas.ModelProvider{schemas.OpenAI}, nil
}

func (a *SimpleAccount) GetKeysForProvider(provider schemas.ModelProvider) ([]schemas.Key, error) {
    if provider == schemas.OpenAI {
        apiKey := os.Getenv("OPENAI_API_KEY")
        if apiKey == "" {
            return nil, fmt.Errorf("OPENAI_API_KEY environment variable not set")
        }

        return []schemas.Key{{
            Value:  apiKey,
            Models: []string{"gpt-4o-mini", "gpt-4o", "gpt-3.5-turbo"}, // Keep Models empty to use any model
            Weight: 1.0,
        }}, nil
    }
    return nil, fmt.Errorf("provider %s not supported", provider)
}

func (a *SimpleAccount) GetConfigForProvider(provider schemas.ModelProvider) (*schemas.ProviderConfig, error) {
    if provider == schemas.OpenAI {
        return &schemas.ProviderConfig{
            NetworkConfig:            schemas.DefaultNetworkConfig,
            ConcurrencyAndBufferSize: schemas.DefaultConcurrencyAndBufferSize,
        }, nil
    }
    return nil, fmt.Errorf("provider %s not supported", provider)
}
```

---

## ⚡ Multi-Provider Implementation

### **Production-Ready Account**

Handles multiple providers with environment variable configuration:

```go
type MultiProviderAccount struct{}

func (a *MultiProviderAccount) GetConfiguredProviders() ([]schemas.ModelProvider, error) {
    var providers []schemas.ModelProvider

    // Check which providers have API keys configured
    if os.Getenv("OPENAI_API_KEY") != "" {
        providers = append(providers, schemas.OpenAI)
    }
    if os.Getenv("ANTHROPIC_API_KEY") != "" {
        providers = append(providers, schemas.Anthropic)
    }
    if os.Getenv("AZURE_API_KEY") != "" {
        providers = append(providers, schemas.Azure)
    }
    if os.Getenv("AWS_ACCESS_KEY_ID") != "" {
        providers = append(providers, schemas.Bedrock)
    }
    if os.Getenv("VERTEX_PROJECT_ID") != "" {
        providers = append(providers, schemas.Vertex)
    }

    if len(providers) == 0 {
        return nil, fmt.Errorf("no provider API keys configured")
    }

    return providers, nil
}

func (a *MultiProviderAccount) GetKeysForProvider(provider schemas.ModelProvider) ([]schemas.Key, error) {
    switch provider {
    case schemas.OpenAI:
        return []schemas.Key{{
            Value:  os.Getenv("OPENAI_API_KEY"),
            Models: []string{"gpt-4o-mini", "gpt-4o", "gpt-3.5-turbo"},
            Weight: 1.0,
        }}, nil

    case schemas.Anthropic:
        return []schemas.Key{{
            Value:  os.Getenv("ANTHROPIC_API_KEY"),
            Models: []string{"claude-3-sonnet-20240229", "claude-3-haiku-20240307"},
            Weight: 1.0,
        }}, nil

    case schemas.Azure:
        return []schemas.Key{{
            Value:  os.Getenv("AZURE_API_KEY"),
            Models: []string{"gpt-4o"},
            Weight: 1.0,
            AzureKeyConfig: &schemas.AzureKeyConfig{
                Endpoint:     os.Getenv("AZURE_ENDPOINT"),
                APIVersion: bifrost.Ptr("2024-08-01-preview"),
                Deployments: map[string]string{
                    "gpt-4o": "gpt-4o-deployment",
                },
            },
        }}, nil

    case schemas.Bedrock:
        return []schemas.Key{{
            Value:  os.Getenv("BEDROCK_API_KEY"),
            Models: []string{"anthropic.claude-3-sonnet-20240229-v1:0"},
            Weight: 1.0,
        }}, nil

    case schemas.Vertex:
        return []schemas.Key{{
            Models: []string{"google/gemini-2.0-flash-001"},
            Weight: 1.0,
            VertexKeyConfig: &schemas.VertexKeyConfig{
                ProjectID:       os.Getenv("VERTEX_PROJECT_ID"),
                Region:          "us-central1",
                AuthCredentials: os.Getenv("VERTEX_CREDENTIALS"),
            },
        }}, nil
    }

    return nil, fmt.Errorf("provider %s not supported", provider)
}

func (a *MultiProviderAccount) GetConfigForProvider(provider schemas.ModelProvider) (*schemas.ProviderConfig, error) {
    switch provider {
    case schemas.OpenAI:
        return &schemas.ProviderConfig{
            NetworkConfig:            schemas.DefaultNetworkConfig,
            ConcurrencyAndBufferSize: schemas.DefaultConcurrencyAndBufferSize,
        }, nil

    case schemas.Anthropic:
        return &schemas.ProviderConfig{
            NetworkConfig:            schemas.DefaultNetworkConfig,
            ConcurrencyAndBufferSize: schemas.DefaultConcurrencyAndBufferSize,
        }, nil

    case schemas.Azure:
        return &schemas.ProviderConfig{
            NetworkConfig: schemas.NetworkConfig{
                DefaultRequestTimeoutInSeconds: 60, // Azure can be slower
                MaxRetries:                     2,
                RetryBackoffInitial:            time.Second,
                RetryBackoffMax:                10 * time.Second,
            },
            ConcurrencyAndBufferSize: schemas.DefaultConcurrencyAndBufferSize,
        }, nil

    case schemas.Bedrock:
        return &schemas.ProviderConfig{
            NetworkConfig:            schemas.DefaultNetworkConfig,
            ConcurrencyAndBufferSize: schemas.DefaultConcurrencyAndBufferSize,
            MetaConfig: &meta.BedrockMetaConfig{
                SecretAccessKey: os.Getenv("AWS_SECRET_ACCESS_KEY"),
                Region:          bifrost.Ptr("us-east-1"),
            },
        }, nil

    case schemas.Vertex:
        return &schemas.ProviderConfig{
            NetworkConfig:            schemas.DefaultNetworkConfig,
            ConcurrencyAndBufferSize: schemas.DefaultConcurrencyAndBufferSize,
        }, nil
    }

    return nil, fmt.Errorf("provider %s not supported", provider)
}
```

---

## 🔧 Advanced Configuration

### **Load Balanced Keys**

Distribute requests across multiple API keys for higher rate limits:

```go
func (a *AdvancedAccount) GetKeysForProvider(provider schemas.ModelProvider) ([]schemas.Key, error) {
    if provider == schemas.OpenAI {
        return []schemas.Key{
            {
                Value:  os.Getenv("OPENAI_KEY_1"),
                Models: []string{"gpt-4o-mini", "gpt-4o"},
                Weight: 0.6, // 60% of requests
            },
            {
                Value:  os.Getenv("OPENAI_KEY_2"),
                Models: []string{"gpt-4o-mini", "gpt-4o"},
                Weight: 0.4, // 40% of requests
            },
        }, nil
    }
    // ... other providers
}
```

### **Custom Network Settings**

Optimize timeouts and retries for different providers:

```go
func (a *AdvancedAccount) GetConfigForProvider(provider schemas.ModelProvider) (*schemas.ProviderConfig, error) {
    switch provider {
    case schemas.OpenAI:
        return &schemas.ProviderConfig{
            NetworkConfig: schemas.NetworkConfig{
                DefaultRequestTimeoutInSeconds: 30,
                MaxRetries:                     3,
                RetryBackoffInitial:            500 * time.Millisecond,
                RetryBackoffMax:                5 * time.Second,
                ExtraHeaders: map[string]string{
                    "X-Custom-Header": "my-app-v1.0",
                },
            },
            ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
                Concurrency: 20, // Higher concurrency for high-throughput
                BufferSize:  200,
            },
        }, nil

    case schemas.Anthropic:
        return &schemas.ProviderConfig{
            NetworkConfig: schemas.NetworkConfig{
                DefaultRequestTimeoutInSeconds: 45, // Anthropic can be slower
                MaxRetries:                     2,
                RetryBackoffInitial:            time.Second,
                RetryBackoffMax:                8 * time.Second,
            },
            ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
                Concurrency: 10, // Lower concurrency for stability
                BufferSize:  50,
            },
        }, nil
    }
    return nil, fmt.Errorf("provider %s not supported", provider)
}
```

### **Proxy Configuration**

Route traffic through proxies for compliance or geographic requirements:

```go
func (a *ProxyAccount) GetConfigForProvider(provider schemas.ModelProvider) (*schemas.ProviderConfig, error) {
    config := &schemas.ProviderConfig{
        NetworkConfig:            schemas.DefaultNetworkConfig,
        ConcurrencyAndBufferSize: schemas.DefaultConcurrencyAndBufferSize,
    }

    // Add proxy for corporate network
    if os.Getenv("USE_PROXY") == "true" {
        config.ProxyConfig = &schemas.ProxyConfig{
            Type:     schemas.HttpProxy,
            URL:      os.Getenv("PROXY_URL"),
            Username: os.Getenv("PROXY_USERNAME"),
            Password: os.Getenv("PROXY_PASSWORD"),
        }
    }

    return config, nil
}
```

---

## 💾 Configuration Patterns

### **JSON Configuration File**

Load configuration from external files:

```go
type JSONAccount struct {
    config map[string]interface{}
}

func NewJSONAccount(configPath string) (*JSONAccount, error) {
    data, err := os.ReadFile(configPath)
    if err != nil {
        return nil, err
    }

    var config map[string]interface{}
    if err := json.Unmarshal(data, &config); err != nil {
        return nil, err
    }

    return &JSONAccount{config: config}, nil
}

func (a *JSONAccount) GetConfiguredProviders() ([]schemas.ModelProvider, error) {
    providers, ok := a.config["providers"].(map[string]interface{})
    if !ok {
        return nil, fmt.Errorf("invalid providers configuration")
    }

    var result []schemas.ModelProvider
    for providerName := range providers {
        result = append(result, schemas.ModelProvider(providerName))
    }

    return result, nil
}
```

### **Database-Backed Account**

Dynamic configuration from database:

```go
type DatabaseAccount struct {
    db *sql.DB
}

func (a *DatabaseAccount) GetKeysForProvider(provider schemas.ModelProvider) ([]schemas.Key, error) {
    rows, err := a.db.Query(`
        SELECT api_key, models, weight
        FROM provider_keys
        WHERE provider = ? AND active = true
    `, string(provider))
    if err != nil {
        return nil, err
    }
    defer rows.Close()

    var keys []schemas.Key
    for rows.Next() {
        var key schemas.Key
        var modelsJSON string

        err := rows.Scan(&key.Value, &modelsJSON, &key.Weight)
        if err != nil {
            continue
        }

        json.Unmarshal([]byte(modelsJSON), &key.Models)
        keys = append(keys, key)
    }

    return keys, nil
}
```

---

## 🔒 Security Best Practices

### **API Key Management**

```go
// ✅ Good: Use environment variables
apiKey := os.Getenv("OPENAI_API_KEY")

// ✅ Good: Use key management services
apiKey := getFromVault("openai-api-key")

// ❌ Bad: Hardcode keys
apiKey := "sk-..." // Never do this!
```

### **Error Handling**

```go
func (a *Account) GetKeysForProvider(provider schemas.ModelProvider) ([]schemas.Key, error) {
    apiKey := os.Getenv("OPENAI_API_KEY")
    if apiKey == "" {
        return nil, fmt.Errorf("OPENAI_API_KEY not configured")
    }

    // Validate key format
    if !strings.HasPrefix(apiKey, "sk-") {
        return nil, fmt.Errorf("invalid OpenAI API key format")
    }

    return []schemas.Key{{
        Value:  apiKey,
        Models: []string{"gpt-4o-mini"},
        Weight: 1.0,
    }}, nil
}
```

---

## 🧪 Testing Your Account

### **Unit Tests**

```go
func TestAccount(t *testing.T) {
    // Set test environment
    os.Setenv("OPENAI_API_KEY", "sk-test-key")
    defer os.Unsetenv("OPENAI_API_KEY")

    account := &MyAccount{}

    // Test provider discovery
    providers, err := account.GetConfiguredProviders()
    assert.NoError(t, err)
    assert.Contains(t, providers, schemas.OpenAI)

    // Test key retrieval
    keys, err := account.GetKeysForProvider(schemas.OpenAI)
    assert.NoError(t, err)
    assert.Len(t, keys, 1)
    assert.Equal(t, "sk-test-key", keys[0].Value)
}
```

### **Integration Test**

```go
func TestAccountWithBifrost(t *testing.T) {
    account := &MyAccount{}

    client, initErr := bifrost.Init(schemas.BifrostConfig{
        Account: account,
    })
    assert.NoError(t, initErr)
    defer client.Cleanup()

    // Test that configuration works
    response, err := client.ChatCompletionRequest(context.Background(), &schemas.BifrostRequest{
        Provider: schemas.OpenAI,
        Model:    "gpt-4o-mini",
        Input: schemas.RequestInput{
            ChatCompletionInput: &[]schemas.BifrostMessage{
                {Role: schemas.ModelChatMessageRoleUser, Content: schemas.MessageContent{ContentStr: &testMessage}},
            },
        },
    })
    assert.NoError(t, err)
    assert.NotNil(t, response)
}
```

---

## 📚 Related Documentation

- **[🤖 Bifrost Client](./bifrost-client.md)** - Using your Account with the client
- **[🔗 Provider Configuration](../providers.md)** - Settings for all 10+ providers
- **[🔑 Key Management](../key-management.md)** - Advanced key rotation and distribution
- **[🌐 HTTP Transport](../../quickstart/http-transport.md)** - JSON-based configuration alternative

> **🏛️ Architecture:** For how Account fits into the overall system, see [System Design](../../architecture/).
