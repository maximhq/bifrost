# 🌐 Networking Configuration

Bifrost provides comprehensive networking features including proxy support, connection pooling, custom headers, timeout configuration, and retry logic.

## 📑 Table of Contents

- [🌐 Networking Configuration](#-networking-configuration)
  - [📑 Table of Contents](#-table-of-contents)
  - [🌐 Network Configuration](#-network-configuration)
    - [Basic Network Settings](#basic-network-settings)
  - [🔗 Proxy Support](#-proxy-support)
    - [HTTP Proxy](#http-proxy)
    - [SOCKS5 Proxy](#socks5-proxy)
    - [Environment-Based Proxy](#environment-based-proxy)
  - [⏱️ Timeouts \& Retries](#️-timeouts--retries)
    - [Retry Configuration](#retry-configuration)
  - [📋 Custom Headers](#-custom-headers)
  - [⚡ Connection Pooling](#-connection-pooling)

---

## 🌐 Network Configuration

### Basic Network Settings

<details>
<summary><strong>🔧 Go Package - Network Configuration</strong></summary>

```go
import (
    "time"
    "github.com/maximhq/bifrost/core/schemas"
)

func (a *MyAccount) GetConfigForProvider(provider schemas.ModelProvider) (*schemas.ProviderConfig, error) {
    return &schemas.ProviderConfig{
        NetworkConfig: schemas.NetworkConfig{
            BaseURL:                        "https://api.openai.com",  // Custom endpoint
            ExtraHeaders:                   map[string]string{          // Custom headers
                "X-Organization":  "my-org",
                "X-Environment":   "production",
            },
            DefaultRequestTimeoutInSeconds: 60,                         // 60 second timeout
            MaxRetries:                     3,                          // Retry 3 times
            RetryBackoffInitial:            500 * time.Millisecond,     // Initial backoff
            RetryBackoffMax:                10 * time.Second,           // Max backoff
        },
    }, nil
}
```

**Network Configuration Options:**

| Field                            | Description              | Default          |
| -------------------------------- | ------------------------ | ---------------- |
| `BaseURL`                        | Custom provider endpoint | Provider default |
| `ExtraHeaders`                   | Additional HTTP headers  | `{}`             |
| `DefaultRequestTimeoutInSeconds` | Request timeout          | `30`             |
| `MaxRetries`                     | Retry attempts           | `0`              |
| `RetryBackoffInitial`            | Initial retry delay      | `500ms`          |
| `RetryBackoffMax`                | Maximum retry delay      | `5s`             |

</details>

<details>
<summary><strong>🌐 HTTP Transport - Network Configuration</strong></summary>

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
      ],
      "network_config": {
        "base_url": "https://api.openai.com",
        "extra_headers": {
          "X-Organization-ID": "org-123",
          "X-Environment": "production"
        },
        "default_request_timeout_in_seconds": 30,
        "max_retries": 1,
        "retry_backoff_initial_ms": 100,
        "retry_backoff_max_ms": 2000
      }
    }
  }
}
```

</details>

## 🔗 Proxy Support

Bifrost supports multiple proxy types for enterprise deployments.

### HTTP Proxy

<details>
<summary><strong>🔧 Go Package - HTTP Proxy</strong></summary>

```go
func (a *MyAccount) GetConfigForProvider(provider schemas.ModelProvider) (*schemas.ProviderConfig, error) {
    return &schemas.ProviderConfig{
        ProxyConfig: &schemas.ProxyConfig{
            Type:     schemas.HttpProxy,
            URL:      "http://proxy.company.com:8080",
            Username: "proxy-user",     // Optional
            Password: "proxy-pass",     // Optional
        },
    }, nil
}
```

</details>

<details>
<summary><strong>🌐 HTTP Transport - HTTP Proxy</strong></summary>

```json
{
  "providers": {
    "openai": {
      "proxy_config": {
        "type": "http",
        "url": "http://proxy.company.com:8080",
        "username": "proxy-user",
        "password": "proxy-pass"
      }
    }
  }
}
```

</details>

### SOCKS5 Proxy

<details>
<summary><strong>🔧 Go Package - SOCKS5 Proxy</strong></summary>

```go
func (a *MyAccount) GetConfigForProvider(provider schemas.ModelProvider) (*schemas.ProviderConfig, error) {
    return &schemas.ProviderConfig{
        ProxyConfig: &schemas.ProxyConfig{
            Type:     schemas.Socks5Proxy,
            URL:      "socks5://proxy.company.com:1080",
            Username: "socks-user",     // Optional
            Password: "socks-pass",     // Optional
        },
    }, nil
}
```

</details>

### Environment-Based Proxy

<details>
<summary><strong>🔧 Go Package - Environment Proxy</strong></summary>

```go
func (a *MyAccount) GetConfigForProvider(provider schemas.ModelProvider) (*schemas.ProviderConfig, error) {
    return &schemas.ProviderConfig{
        ProxyConfig: &schemas.ProxyConfig{
            Type: schemas.EnvProxy,
            // Uses HTTP_PROXY, HTTPS_PROXY, NO_PROXY environment variables
        },
    }, nil
}
```

**Environment Variables:**

```bash
export HTTP_PROXY=http://proxy.company.com:8080
export HTTPS_PROXY=https://proxy.company.com:8443
export NO_PROXY=localhost,127.0.0.1,.company.com
```

</details>

## ⏱️ Timeouts & Retries

Configure robust timeout and retry behavior for reliable operations.

### Retry Configuration

<details>
<summary><strong>🔄 Retry Logic</strong></summary>

```go
// Go Package - Exponential Backoff
schemas.NetworkConfig{
    MaxRetries:          3,                         // Retry up to 3 times
    RetryBackoffInitial: 500 * time.Millisecond,   // Start with 500ms
    RetryBackoffMax:     10 * time.Second,         // Cap at 10 seconds
}
```

**Retry Behavior:**

1. **First retry**: 500ms delay
2. **Second retry**: ~1s delay (exponential + jitter)
3. **Third retry**: ~2s delay (exponential + jitter)
4. **Cap**: Maximum 10s delay

**Retryable Conditions:**

- ✅ Rate limit errors (429)
- ✅ Server errors (5xx)
- ✅ Network timeouts
- ✅ Connection failures
- ❌ Authentication errors (4xx)
- ❌ Invalid requests (400)

</details>

## 📋 Custom Headers

Add custom headers for authentication, tracking, or provider-specific requirements.

<details>
<summary><strong>🔧 Go Package - Custom Headers</strong></summary>

```go
func (a *MyAccount) GetConfigForProvider(provider schemas.ModelProvider) (*schemas.ProviderConfig, error) {
    return &schemas.ProviderConfig{
        NetworkConfig: schemas.NetworkConfig{
            ExtraHeaders: map[string]string{
                "X-Organization-ID":    "org-12345",
                "X-Environment":        "production",
                "X-Request-Source":     "bifrost-gateway",
                "User-Agent":           "MyApp/1.0 Bifrost/1.0",
            },
        },
    }, nil
}
```

</details>

## ⚡ Connection Pooling

Optimize performance with connection reuse and pooling.

**Connection Pool Features:**

- ✅ **HTTP/2 Support**: Multiplexed connections where supported
- ✅ **Keep-Alive**: Persistent connections reduce overhead
- ✅ **Automatic Scaling**: Pool size matches concurrency settings
- ✅ **Provider Isolation**: Separate pools per provider

**Pool Size Guidelines:**

| Load Level     | Concurrency | Expected Performance |
| -------------- | ----------- | -------------------- |
| **Low**        | 5-10        | < 100 RPS            |
| **Medium**     | 10-50       | 100-1000 RPS         |
| **High**       | 50-500      | 1000-5000 RPS        |
| **Ultra High** | 500-1000    | 5000+ RPS            |

---

**Need help?** Check our [❓ FAQ](../guides/faq.md) or [🔧 Troubleshooting Guide](../guides/troubleshooting.md).
