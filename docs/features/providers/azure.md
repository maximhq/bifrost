# ☁️ Azure OpenAI Provider Configuration

Complete guide for configuring Azure OpenAI with Bifrost, including enterprise deployment patterns, managed identity, and Azure-specific configurations.

## 📋 Supported Models

| Model Family | Models                              | Deployment Names                              |
| ------------ | ----------------------------------- | --------------------------------------------- |
| **GPT-4o**   | `gpt-4o`, `gpt-4o-mini`             | `gpt-4o-deployment`, `gpt-4o-mini-deployment` |
| **GPT-4**    | `gpt-4`, `gpt-4-turbo`, `gpt-4-32k` | `gpt-4-deployment`, `gpt-4-turbo-deployment`  |
| **GPT-3.5**  | `gpt-35-turbo`, `gpt-35-turbo-16k`  | `gpt-35-turbo-deployment`                     |
| **DALL-E**   | `dall-e-3`, `dall-e-2`              | `dalle3-deployment`                           |

---

## 🚀 Quick Start

<details>
<summary><strong>🔧 Go Package Usage</strong></summary>

### Basic Configuration with Meta Config

```go
import (
    "github.com/maximhq/bifrost/core/schemas"
    "github.com/maximhq/bifrost/core/schemas/meta"
)

account := &schemas.Account{
    Providers: map[string]schemas.ProviderConfig{
        "azure": {
            Keys: []schemas.Key{
                {
                    Value:  "your-azure-openai-api-key",
                    Models: []string{"gpt-4o", "gpt-35-turbo"},
                    Weight: 1.0,
                },
            },
            MetaConfig: &meta.AzureMetaConfig{
                Endpoint:   "https://your-resource.openai.azure.com",
                APIVersion: "2024-02-15-preview",
                Deployments: map[string]string{
                    "gpt-4o":       "gpt-4o-deployment",
                    "gpt-35-turbo": "gpt-35-turbo-deployment",
                },
            },
        },
    },
}
```

### Multiple Azure Resources

```go
account := &schemas.Account{
    Providers: map[string]schemas.ProviderConfig{
        "azure": {
            Keys: []schemas.Key{
                {
                    Value:  "key-for-resource-1",
                    Models: []string{"gpt-4o"},
                    Weight: 0.6,
                },
                {
                    Value:  "key-for-resource-2",
                    Models: []string{"gpt-35-turbo"},
                    Weight: 0.4,
                },
            },
            MetaConfig: &meta.AzureMetaConfig{
                Endpoint:   "https://resource-1.openai.azure.com",
                APIVersion: "2024-02-15-preview",
                Deployments: map[string]string{
                    "gpt-4o":       "gpt-4o-prod-deployment",
                    "gpt-35-turbo": "gpt-35-turbo-deployment",
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
    Provider: "azure",
    Model:    "gpt-4o", // Maps to gpt-4o-deployment
    Messages: []schemas.Message{
        {Role: "user", Content: "Explain Azure OpenAI benefits for enterprise."},
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
    "azure": {
      "keys": [
        {
          "value": "env.AZURE_OPENAI_API_KEY",
          "models": ["gpt-4o", "gpt-35-turbo"],
          "weight": 1.0
        }
      ],
      "meta_config": {
        "endpoint": "env.AZURE_ENDPOINT",
        "api_version": "2024-02-15-preview",
        "deployments": {
          "gpt-4o": "gpt-4o-deployment",
          "gpt-35-turbo": "gpt-35-turbo-deployment"
        }
      }
    }
  }
}
```

### Multiple Azure Resources Configuration

```json
{
  "providers": {
    "azure": {
      "keys": [
        {
          "value": "env.AZURE_API_KEY_1",
          "models": ["gpt-4o"],
          "weight": 0.7
        },
        {
          "value": "env.AZURE_API_KEY_2",
          "models": ["gpt-35-turbo"],
          "weight": 0.3
        }
      ],
      "meta_config": {
        "endpoint": "env.AZURE_ENDPOINT",
        "api_version": "2024-02-15-preview",
        "deployments": {
          "gpt-4o": "gpt-4o-prod-deployment",
          "gpt-35-turbo": "gpt-35-turbo-deployment"
        }
      }
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
    "provider": "azure",
    "model": "gpt-4o",
    "messages": [
      {"role": "user", "content": "Explain Azure OpenAI benefits for enterprise."}
    ],
    "params": {
      "max_tokens": 500,
      "temperature": 0.7
    }
  }'
```

</details>

---

## ⚙️ Enterprise Configuration

### Multi-Region Deployment

<details>
<summary><strong>🔧 Go Package Configuration</strong></summary>

```go
// Multi-region Azure setup for high availability
account := &schemas.Account{
    Providers: map[string]schemas.ProviderConfig{
        "azure-primary": {
            Keys: []schemas.Key{
                {Value: "primary-region-key", Models: []string{"gpt-4o"}, Weight: 0.7},
            },
            MetaConfig: &meta.AzureMetaConfig{
                Endpoint:   "https://eastus-resource.openai.azure.com",
                APIVersion: "2024-02-15-preview",
                Deployments: map[string]string{
                    "gpt-4o": "gpt-4o-eastus-deployment",
                },
            },
        },
        "azure-secondary": {
            Keys: []schemas.Key{
                {Value: "secondary-region-key", Models: []string{"gpt-4o"}, Weight: 0.3},
            },
            MetaConfig: &meta.AzureMetaConfig{
                Endpoint:   "https://westus-resource.openai.azure.com",
                APIVersion: "2024-02-15-preview",
                Deployments: map[string]string{
                    "gpt-4o": "gpt-4o-westus-deployment",
                },
            },
        },
    },
}
```

</details>

### Private Endpoint Configuration

<details>
<summary><strong>🔧 Go Package Configuration</strong></summary>

```go
// Private endpoint configuration
providerConfig := schemas.ProviderConfig{
    Keys: []schemas.Key{
        {Value: "your-api-key", Models: []string{"gpt-4o"}, Weight: 1.0},
    },
    MetaConfig: &meta.AzureMetaConfig{
        Endpoint:   "https://your-private-endpoint.cognitiveservices.azure.com",
        APIVersion: "2024-02-15-preview",
        Deployments: map[string]string{
            "gpt-4o": "private-gpt-4o-deployment",
        },
    },
    NetworkConfig: schemas.NetworkConfig{
        ExtraHeaders: map[string]string{
            "User-Agent": "MyApp/1.0",
            "X-Azure-Ref": "private-deployment",
        },
        DefaultRequestTimeoutInSeconds: 60,
        MaxRetries: 3,
    },
}
```

</details>

---

## 🔐 Authentication Methods

### API Key Authentication

Most common method for Azure OpenAI:

```go
// Standard API key authentication
keys := []schemas.Key{
    {
        Value:  "your-azure-openai-api-key",
        Models: []string{"gpt-4o"},
        Weight: 1.0,
    },
}
```

### Managed Identity (Future Enhancement)

While Bifrost currently uses API keys, Azure Managed Identity integration is planned for future releases.

---

## 🛠️ Advanced Features

### Deployment Management

<details>
<summary><strong>🔧 Go Package Configuration</strong></summary>

```go
// Advanced deployment mapping
metaConfig := &meta.AzureMetaConfig{
    Endpoint:   "https://your-resource.openai.azure.com",
    APIVersion: "2024-02-15-preview",
    Deployments: map[string]string{
        // Production deployments
        "gpt-4o":           "gpt-4o-prod-deployment",
        "gpt-4":            "gpt-4-prod-deployment",
        "gpt-35-turbo":     "gpt-35-turbo-prod-deployment",

        // Specialized deployments
        "gpt-4o-vision":    "gpt-4o-vision-deployment",
        "dall-e-3":         "dalle3-prod-deployment",
    },
}
```

</details>

### Content Filtering Configuration

<details>
<summary><strong>🔧 Go Package Usage</strong></summary>

```go
// Request with Azure content filtering
request := &schemas.ChatCompletionRequest{
    Provider: "azure",
    Model:    "gpt-4o",
    Messages: []schemas.Message{
        {Role: "user", Content: "Generate a creative story about space exploration."},
    },
    Params: schemas.RequestParams{
        MaxTokens:   1000,
        Temperature: 0.8,
        // Azure-specific parameters can be passed through
        ExtraParams: map[string]interface{}{
            "content_filter": map[string]interface{}{
                "severity": "medium",
            },
        },
    },
}
```

</details>

---

## 📊 Performance Optimization

### Quota Management

Azure OpenAI provides quota management at the deployment level:

<details>
<summary><strong>🔧 Go Package Configuration</strong></summary>

```go
// Optimized for Azure quotas
providerConfig := schemas.ProviderConfig{
    Keys: []schemas.Key{
        {Value: "high-quota-key", Models: []string{"gpt-4o"}, Weight: 0.8},
        {Value: "backup-key", Models: []string{"gpt-4o"}, Weight: 0.2},
    },
    ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
        Concurrency: 10,  // Match Azure deployment quota
        BufferSize:  100,
    },
}
```

</details>

### Regional Load Balancing

```go
// Distribute load across Azure regions
account := &schemas.Account{
    Providers: map[string]schemas.ProviderConfig{
        "azure": {
            Keys: []schemas.Key{
                {Value: "eastus-key", Models: []string{"gpt-4o"}, Weight: 0.5},
                {Value: "westus-key", Models: []string{"gpt-4o"}, Weight: 0.3},
                {Value: "northeurope-key", Models: []string{"gpt-4o"}, Weight: 0.2},
            },
        },
    },
}
```

---

## 🔐 Security Best Practices

### Network Security

- **Private Endpoints**: Use Azure Private Link for enhanced security
- **Virtual Networks**: Deploy within your Azure VNet
- **Network Security Groups**: Control traffic to Azure OpenAI resources
- **Firewall Rules**: Restrict access to specific IP ranges

### Identity & Access Management

- **Azure RBAC**: Use role-based access control
- **Resource Groups**: Organize resources by environment
- **Key Vault Integration**: Store API keys in Azure Key Vault
- **Monitoring**: Enable Azure Monitor and Application Insights

### Compliance Features

- **Data Residency**: Control where data is processed
- **Encryption**: Data encrypted in transit and at rest
- **Audit Logs**: Comprehensive logging and auditing
- **Compliance**: SOC 2, HIPAA, and other certifications

---

## 📊 Monitoring & Debugging

### Azure-Specific Metrics

- **Token Quotas**: Monitor quota usage across deployments
- **Regional Performance**: Track latency across Azure regions
- **Deployment Health**: Monitor individual deployment status
- **Cost Tracking**: Track costs by deployment and region

### Common Issues & Solutions

| Issue                    | Cause                     | Solution                            |
| ------------------------ | ------------------------- | ----------------------------------- |
| **Quota Exceeded**       | Deployment quota limits   | Distribute load, scale deployments  |
| **Deployment Not Found** | Incorrect deployment name | Verify deployment mapping           |
| **Regional Outage**      | Azure service issues      | Implement multi-region fallbacks    |
| **Authentication Error** | Invalid key/endpoint      | Verify Azure resource configuration |

---

## 🔗 Related Documentation

- **[Provider System Overview](README.md)** - Multi-provider setup and fallbacks
- **[OpenAI Configuration](openai.md)** - Standard OpenAI deployment
- **[Fallback Configuration](../fallbacks.md)** - Implementing provider fallbacks
- **[Error Handling](../../usage/errors.md)** - Azure error codes and handling

---

**⚡ Ready for enterprise deployment?** Check our [production setup guide](../../guides/tutorials/production-setup.md) for Azure OpenAI at scale.
