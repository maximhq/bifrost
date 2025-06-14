# Bifrost Provider System

Bifrost supports multiple AI model providers, each with its own configuration options and capabilities. This document explains how to configure providers and develop new ones.

## 1. Supported Providers

Bifrost currently supports the following providers:

- OpenAI
- Anthropic
- Azure
- Bedrock
- Cohere
- Vertex
- Mistral
- Ollama

## 2. Provider Configuration

### Basic Configuration Structure

```golang
schemas.ProviderConfig{
    NetworkConfig: schemas.NetworkConfig{
        BaseURL:                        "https://api.custom-deployment.com", // Custom base URL (optional)
        ExtraHeaders:                   map[string]string{                   // Additional headers (optional)
            "X-Organization-ID": "org-123",
            "X-Environment":     "production",
            "User-Agent":        "MyApp/1.0 Bifrost/1.0",
        },
        DefaultRequestTimeoutInSeconds: 30,
        MaxRetries:                     2,
        RetryBackoffInitial:            100 * time.Millisecond,
        RetryBackoffMax:                2 * time.Second,
    },
    ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
        Concurrency: 3,  // Number of concurrent requests
        BufferSize:  10, // Maximum requests in queue
    },
    ProxyConfig: &schemas.ProxyConfig{
        Type: schemas.HttpProxy,
        URL:  "http://your-proxy:port",
    },
}
```

### Default Values

```golang
const (
    DefaultMaxRetries              = 0
    DefaultRetryBackoffInitial     = 500 * time.Millisecond
    DefaultRetryBackoffMax         = 5 * time.Second
    DefaultRequestTimeoutInSeconds = 30
    DefaultBufferSize              = 100
    DefaultConcurrency             = 10
)
```

## 3. Provider-Specific Meta Configurations

Few providers new meta configs for their setup.

### Azure

```golang
meta.AzureMetaConfig{
    Endpoint:   "https://your-resource.openai.azure.com",
    APIVersion: "2024-02-15-preview",
    Deployments: map[string]string{
        "gpt-4": "gpt-4-deployment",
        "gpt-35-turbo": "gpt-35-turbo-deployment",
    },
}
```

### Bedrock

```golang
meta.BedrockMetaConfig{
    SecretAccessKey:    os.Getenv("BEDROCK_ACCESS_KEY"),
    Region:             "us-east-1",
    SessionToken:       os.Getenv("BEDROCK_SESSION_TOKEN"), // Optional
    ARN:                os.Getenv("BEDROCK_ARN"),           // Optional
    InferenceProfiles:  map[string]string{
        "gpt-4": "gpt-4-deployment-profile",
    }
}
```

### Vertex

```golang
meta.VertexMetaConfig{
    ProjectID:          os.Getenv("VERTEX_PROJECT_ID"),
    Location:           "us-central1",
    AuthCredentials:    os.Getenv("VERTEX_AUTH_CREDENTIALS"), // GCP Auth creds
}
```

## 4. API Key Management

### Key Weights

Bifrost supports weighted distribution of requests across multiple API keys. The weight determines the relative frequency of key usage:

- Weights are normalized (sum to 1.0)
- Higher weight = more frequent usage
- Equal weights if not specified
- Model-specific key assignment

Example with weights:

```golang
[]schemas.Key{
    {
        Value:  os.Getenv("OPEN_AI_API_KEY1"),
        Models: []string{"gpt-4", "gpt-4-turbo"},
        Weight: 0.6,  // 60% of requests for these models
    },
    {
        Value:  os.Getenv("OPEN_AI_API_KEY2"),
        Models: []string{"gpt-4-turbo"},
        Weight: 0.3,  // 30% of requests for gpt-4-turbo
    },
    {
        Value:  os.Getenv("OPEN_AI_API_KEY3"),
        Models: []string{"gpt-4"},
        Weight: 0.1,  // 10% of requests for gpt-4
    },
}
```

### Key Selection Logic

1. Filters keys that support the requested model
2. Normalizes weights of available keys
3. Uses weighted random selection
4. Falls back to first available key if selection fails

## 5. Proxy Configuration

Bifrost supports various proxy types for provider connections:

### HTTP Proxy

```golang
schemas.ProxyConfig{
    Type:     schemas.HttpProxy,
    URL:      "http://proxy.example.com:8080",
    Username: "user",     // Optional
    Password: "pass",     // Optional
}
```

### SOCKS5 Proxy

```golang
schemas.ProxyConfig{
    Type:     schemas.Socks5Proxy,
    URL:      "socks5://proxy.example.com:1080",
    Username: "user",     // Optional
    Password: "pass",     // Optional
}
```

### Environment Proxy

```golang
schemas.ProxyConfig{
    Type: schemas.EnvProxy,
    // Uses HTTP_PROXY, HTTPS_PROXY environment variables
}
```

### Proxy Best Practices

1. **Security**

   - Use HTTPS proxies when possible
   - Rotate proxy credentials regularly
   - Monitor proxy performance

2. **Performance**

   - Choose geographically close proxies
   - Monitor proxy latency
   - Implement proxy fallbacks

3. **Configuration**

   - Set appropriate timeouts
   - Configure retry policies
   - Monitor proxy errors

## 6. Extra Headers Configuration

Bifrost supports custom headers that can be added to all requests sent to a provider. This is useful for enterprise deployments, custom authentication, or provider-specific requirements.

### Configuration

Extra headers are configured in the `NetworkConfig` section:

```golang
schemas.NetworkConfig{
    ExtraHeaders: map[string]string{
        "X-Organization-ID": "org-123",
        "X-Environment":     "production",
        "User-Agent":        "MyApp/1.0 Bifrost/1.0",
        "X-Custom-Auth":     "bearer-token-xyz",
    },
}
```

### JSON Configuration

```json
{
  "openai": {
    "keys": [...],
    "network_config": {
      "extra_headers": {
        "X-Organization-ID": "org-123",
        "X-Environment": "production",
        "User-Agent": "MyApp/1.0 Bifrost/1.0"
      }
    }
  }
}
```

### Use Cases

1. **Enterprise Deployments**

   - Organization or tenant identification
   - Custom authentication headers
   - Environment tracking (dev/staging/prod)

2. **Self-hosted Providers**

   - Custom routing headers for Ollama deployments
   - Load balancer identification
   - Custom API versions

3. **Monitoring & Observability**

   - Request source identification
   - Custom correlation IDs
   - Application version tracking

4. **Provider-specific Requirements**
   - Beta feature flags
   - Custom API versions
   - Regional preferences

### Header Precedence

Headers configured in `extra_headers` are applied before mandatory provider headers. If there are conflicts (such as duplicate header names), the mandatory headers will take precedence and overwrite or ignore the `extra_headers` values. This ensures that critical provider functionality is not compromised by custom header configurations.

**Important Notes:**

- Authorization headers are automatically filtered out from `extra_headers` for security reasons
- Provider-specific mandatory headers (like API keys, content-type, etc.) always take precedence
- Custom headers should not conflict with standard HTTP headers required by the provider

### Best Practices

1. **Security**

   - Use environment variables for sensitive headers
   - Avoid hardcoding authentication tokens
   - Review headers regularly for security implications

2. **Performance**

   - Keep header count minimal for performance
   - Use short, descriptive header names
   - Monitor header impact on request size

3. **Compliance**
   - Document custom headers for audit purposes
   - Ensure headers comply with HTTP standards
   - Validate header values before deployment

## 7. Provider Development Guidelines

### 1. Provider Structure

All providers should be implemented in the `core/providers` directory. The structure should be:

```
core/
├── providers/
│   ├── your_provider.go    # Provider implementation
│   └── ...                 # Other provider implementations
└── schemas/
    └── meta/
        └── your_provider.go    # Provider-specific meta configuration
```

### 2. Provider Interface

```golang
type Provider interface {
    // GetProviderKey returns the provider's identifier
    GetProviderKey() ModelProvider

    // TextCompletion performs a text completion request
    TextCompletion(model, key, text string, params *ModelParameters) (*BifrostResponse, *BifrostError)

    // ChatCompletion performs a chat completion request
    ChatCompletion(model, key string, messages []Message, params *ModelParameters) (*BifrostResponse, *BifrostError)
}
```

### 3. Meta Configuration

If your provider requires additional configuration beyond the standard `ProviderConfig`, implement a meta configuration in `core/schemas/meta/your_provider.go`:

```golang
// YourProviderMetaConfig implements the MetaConfig interface
type YourProviderMetaConfig struct {
    // Add your provider-specific fields here
    Endpoint   string            `json:"endpoint"`
    APIVersion string            `json:"api_version"`
    // ... other fields
}

// Implement all required methods from the MetaConfig interface
func (c *YourProviderMetaConfig) GetSecretAccessKey() *string { /* ... */ }
func (c *YourProviderMetaConfig) GetRegion() *string { /* ... */ }
// ... implement other interface methods
```

The meta configuration must implement all methods from the `MetaConfig` interface defined in `core/schemas/provider.go`. Return `nil` for methods that don't apply to your provider.

### 4. Development Process

1. Open an issue to discuss the new provider
2. Create a pull request with:
   - Provider implementation in `core/providers/`
   - Addition of provider key in `ModelProvider` in `/core/schemas/bifrost.go`
   - Meta configuration in `core/schemas/meta/` (if needed)
   - Tests in `core/tests` with `Test{ProviderName}` function name.
   - Documentation update in `docs/providers.go`

### 5. Implementation Requirements

1. **Error Handling**

   - Use standard Bifrost error types
   - Gracefully handling and logging (using bifrost logger) all runtime errors

2. **Configuration**

   - Support provider-specific settings through meta configuration (if needed)
   - Implement default values
   - Validate configuration
   - Implement sync pools for optimized resource allocations

3. **Testing**

   - Unit tests for all methods (using `core/tests/setup.go` file)
   - Integration tests
   - Error case coverage

4. **Documentation**
   - Provider capabilities
   - Configuration options
   - Meta configuration usage
