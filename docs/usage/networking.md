# 🌐 Networking

Network configuration including proxy support, connection pooling, custom headers, timeout management, and retry logic.

## 📋 Overview

**Networking Features:**

- ✅ **Proxy Support** - HTTP, SOCKS5, and environment-based proxy configuration
- ✅ **Connection Pooling** - Optimize network resources and performance
- ✅ **Custom Headers** - Add authentication, organization, or tracking headers
- ✅ **Timeout Control** - Fine-grained timeout configuration per provider
- ✅ **Retry Logic** - Exponential backoff with configurable retry policies
- ✅ **Base URL Override** - Custom endpoints for enterprise deployments

**Benefits:**

- 🚀 **Better Performance** - Connection reuse and pooling
- 🛡️ **Enterprise Ready** - Proxy and firewall compatibility
- ⚡ **Fault Tolerance** - Automatic retry with backoff strategies
- 🔧 **Flexible Deployment** - Custom endpoints and headers

---

## ⚡ Basic Network Configuration

### Default Network Settings

<details open>
<summary><strong>🔧 Go Package Usage</strong></summary>

```go
func (a *MyAccount) GetConfigForProvider(provider schemas.ModelProvider) (*schemas.ProviderConfig, error) {
    return &schemas.ProviderConfig{
        NetworkConfig: schemas.NetworkConfig{
            // Custom endpoint (optional)
            BaseURL: "https://api.openai.com",

            // Custom headers
            ExtraHeaders: map[string]string{
                "X-Organization": "my-org-id",
                "X-Environment":  "production",
                "User-Agent":     "MyApp/1.0",
            },

            // Timeout configuration
            DefaultRequestTimeoutInSeconds: 60,  // 60 second timeout

            // Retry configuration
            MaxRetries:          3,                        // Retry up to 3 times
            RetryBackoffInitial: 500 * time.Millisecond,  // Start with 500ms
            RetryBackoffMax:     10 * time.Second,        // Max 10 seconds
        },
    }, nil
}
```

**Network Configuration Options:**

| Field                            | Type                | Description              | Default          |
| -------------------------------- | ------------------- | ------------------------ | ---------------- |
| `BaseURL`                        | `string`            | Custom provider endpoint | Provider default |
| `ExtraHeaders`                   | `map[string]string` | Additional HTTP headers  | `{}`             |
| `DefaultRequestTimeoutInSeconds` | `int`               | Request timeout          | `30`             |
| `MaxRetries`                     | `int`               | Retry attempts           | `0`              |
| `RetryBackoffInitial`            | `time.Duration`     | Initial retry delay      | `500ms`          |
| `RetryBackoffMax`                | `time.Duration`     | Maximum retry delay      | `5s`             |

</details>

<details>
<summary><strong>🌐 HTTP Transport Usage</strong></summary>

**Configuration (`config.json`):**

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
          "X-Environment": "production",
          "User-Agent": "MyApp/1.0"
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

---

## 🔗 Proxy Configuration

### HTTP Proxy

<details>
<summary><strong>🔧 Go Package - HTTP Proxy</strong></summary>

```go
func (a *MyAccount) GetConfigForProvider(provider schemas.ModelProvider) (*schemas.ProviderConfig, error) {
    return &schemas.ProviderConfig{
        ProxyConfig: &schemas.ProxyConfig{
            Type:     schemas.HttpProxy,
            URL:      "http://proxy.company.com:8080",
            Username: "proxy-user",     // Optional authentication
            Password: "proxy-pass",     // Optional authentication
        },
        NetworkConfig: schemas.NetworkConfig{
            DefaultRequestTimeoutInSeconds: 45,  // Increase timeout for proxy
        },
    }, nil
}
```

**Proxy Configuration Options:**

| Field      | Type        | Description                  | Required |
| ---------- | ----------- | ---------------------------- | -------- |
| `Type`     | `ProxyType` | Proxy type (http/socks5/env) | ✅       |
| `URL`      | `string`    | Proxy server URL             | ✅       |
| `Username` | `string`    | Proxy authentication user    | ❌       |
| `Password` | `string`    | Proxy authentication pass    | ❌       |

</details>

<details>
<summary><strong>🌐 HTTP Transport - HTTP Proxy</strong></summary>

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
      "proxy_config": {
        "type": "http",
        "url": "http://proxy.company.com:8080",
        "username": "proxy-user",
        "password": "proxy-pass"
      },
      "network_config": {
        "default_request_timeout_in_seconds": 45
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

<details>
<summary><strong>🌐 HTTP Transport - SOCKS5 Proxy</strong></summary>

```json
{
  "providers": {
    "openai": {
      "proxy_config": {
        "type": "socks5",
        "url": "socks5://proxy.company.com:1080",
        "username": "socks-user",
        "password": "socks-pass"
      }
    }
  }
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
            // Automatically uses HTTP_PROXY, HTTPS_PROXY, NO_PROXY environment variables
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

<details>
<summary><strong>🌐 HTTP Transport - Environment Proxy</strong></summary>

```json
{
  "providers": {
    "openai": {
      "proxy_config": {
        "type": "env"
      }
    }
  }
}
```

**Environment Variables:**

```bash
export HTTP_PROXY=http://proxy.company.com:8080
export HTTPS_PROXY=https://proxy.company.com:8443
export NO_PROXY=localhost,127.0.0.1,.company.com
```

</details>

> Please note that the proxy configuration is not supported for **streaming requests**, and for **Bedrock** and **Vertex** Providers.

---

## ⏱️ Timeout & Retry Configuration

### Basic Retry Logic

<details>
<summary><strong>🔧 Go Package - Retry Configuration</strong></summary>

```go
func (a *MyAccount) GetConfigForProvider(provider schemas.ModelProvider) (*schemas.ProviderConfig, error) {
    return &schemas.ProviderConfig{
        NetworkConfig: schemas.NetworkConfig{
            // Timeout settings
            DefaultRequestTimeoutInSeconds: 30,

            // Retry settings with exponential backoff
            MaxRetries:          3,                         // Retry up to 3 times
            RetryBackoffInitial: 500 * time.Millisecond,   // Start with 500ms
            RetryBackoffMax:     10 * time.Second,         // Cap at 10 seconds
        },
    }, nil
}
```

**Retry Logic:**

```text
Attempt 1: Request fails
Wait: 500ms (initial backoff)

Attempt 2: Request fails
Wait: 1000ms (2x backoff)

Attempt 3: Request fails
Wait: 2000ms (2x backoff)

Attempt 4: Request fails
Give up after 3 retries
```

</details>

<details>
<summary><strong>🌐 HTTP Transport - Retry Configuration</strong></summary>

```json
{
  "providers": {
    "openai": {
      "network_config": {
        "default_request_timeout_in_seconds": 30,
        "max_retries": 3,
        "retry_backoff_initial_ms": 500,
        "retry_backoff_max_ms": 10000
      }
    }
  }
}
```

</details>

### Provider-Specific Timeouts

<details>
<summary><strong>🔧 Go Package - Provider-Specific Timeouts</strong></summary>

```go
func (a *MyAccount) GetConfigForProvider(provider schemas.ModelProvider) (*schemas.ProviderConfig, error) {
    switch provider {
    case schemas.OpenAI:
        return &schemas.ProviderConfig{
            NetworkConfig: schemas.NetworkConfig{
                DefaultRequestTimeoutInSeconds: 30,  // Fast timeout for OpenAI
                MaxRetries: 2,
            },
        }, nil
    case schemas.Anthropic:
        return &schemas.ProviderConfig{
            NetworkConfig: schemas.NetworkConfig{
                DefaultRequestTimeoutInSeconds: 60,  // Longer timeout for Claude
                MaxRetries: 3,
            },
        }, nil
    case schemas.Bedrock:
        return &schemas.ProviderConfig{
            NetworkConfig: schemas.NetworkConfig{
                DefaultRequestTimeoutInSeconds: 120, // Longest timeout for Bedrock
                MaxRetries: 1,                       // Fewer retries for AWS
            },
        }, nil
    }
    return &schemas.ProviderConfig{}, nil
}
```

</details>

<details>
<summary><strong>🌐 HTTP Transport - Provider-Specific Timeouts</strong></summary>

```json
{
  "providers": {
    "openai": {
      "network_config": {
        "default_request_timeout_in_seconds": 30,
        "max_retries": 2
      }
    },
    "anthropic": {
      "network_config": {
        "default_request_timeout_in_seconds": 60,
        "max_retries": 3
      }
    },
    "bedrock": {
      "network_config": {
        "default_request_timeout_in_seconds": 120,
        "max_retries": 1
      }
    }
  }
}
```

</details>

---

## 📋 Custom Headers

### Authentication Headers

<details>
<summary><strong>🔧 Go Package - Custom Headers</strong></summary>

```go
func (a *MyAccount) GetConfigForProvider(provider schemas.ModelProvider) (*schemas.ProviderConfig, error) {
    switch provider {
    case schemas.OpenAI:
        return &schemas.ProviderConfig{
            NetworkConfig: schemas.NetworkConfig{
                ExtraHeaders: map[string]string{
                    "OpenAI-Organization": os.Getenv("OPENAI_ORG_ID"),
                    "OpenAI-Project":      os.Getenv("OPENAI_PROJECT_ID"),
                    "User-Agent":          "MyApp/1.0.0",
                    "X-Request-ID":        generateRequestID(),
                },
            },
        }, nil
    case schemas.Anthropic:
        return &schemas.ProviderConfig{
            NetworkConfig: schemas.NetworkConfig{
                ExtraHeaders: map[string]string{
                    "User-Agent":       "MyApp/1.0.0",
                    "X-Source":         "bifrost-gateway",
                    "anthropic-version": "2023-06-01",
                },
            },
        }, nil
    }
    return &schemas.ProviderConfig{}, nil
}

func generateRequestID() string {
    return fmt.Sprintf("req_%d", time.Now().UnixNano())
}
```

</details>

<details>
<summary><strong>🌐 HTTP Transport - Custom Headers</strong></summary>

```json
{
  "providers": {
    "openai": {
      "network_config": {
        "extra_headers": {
          "OpenAI-Organization": "org-your-org-id",
          "OpenAI-Project": "proj-your-project-id",
          "User-Agent": "MyApp/1.0.0",
          "X-Source": "bifrost-gateway"
        }
      }
    },
    "anthropic": {
      "network_config": {
        "extra_headers": {
          "User-Agent": "MyApp/1.0.0",
          "X-Source": "bifrost-gateway",
          "anthropic-version": "2023-06-01"
        }
      }
    }
  }
}
```

</details>

### Tracking and Monitoring Headers

<details>
<summary><strong>🔧 Go Package - Monitoring Headers</strong></summary>

```go
func (a *MyAccount) GetConfigForProvider(provider schemas.ModelProvider) (*schemas.ProviderConfig, error) {
    return &schemas.ProviderConfig{
        NetworkConfig: schemas.NetworkConfig{
            ExtraHeaders: map[string]string{
                // Tracking headers
                "X-Request-ID":     generateRequestID(),
                "X-Session-ID":     getSessionID(),
                "X-User-ID":        getUserID(),
                "X-Environment":    os.Getenv("ENVIRONMENT"),

                // Application metadata
                "X-App-Version":    "1.2.3",
                "X-Build-Hash":     getBuildHash(),
                "X-Deployment-ID":  getDeploymentID(),

                // Monitoring
                "X-Trace-ID":       getTraceID(),
                "X-Span-ID":        getSpanID(),
            },
        },
    }, nil
}
```

</details>

<details>
<summary><strong>🌐 HTTP Transport - Monitoring Headers</strong></summary>

```json
{
  "providers": {
    "openai": {
      "network_config": {
        "extra_headers": {
          "X-Environment": "production",
          "X-App-Version": "1.2.3",
          "X-Build-Hash": "abc123def",
          "X-Deployment-ID": "deploy-456",
          "X-Source": "bifrost-gateway"
        }
      }
    }
  }
}
```

</details>

---

## 🔧 Enterprise Configuration

### Corporate Network Setup

<details>
<summary><strong>🏢 Enterprise Network Configuration</strong></summary>

**Go Package - Enterprise Setup:**

```go
func (a *MyAccount) GetConfigForProvider(provider schemas.ModelProvider) (*schemas.ProviderConfig, error) {
    return &schemas.ProviderConfig{
        // Corporate proxy
        ProxyConfig: &schemas.ProxyConfig{
            Type:     schemas.HttpProxy,
            URL:      "http://corporate-proxy.company.com:8080",
            Username: os.Getenv("PROXY_USER"),
            Password: os.Getenv("PROXY_PASS"),
        },

        NetworkConfig: schemas.NetworkConfig{
            // Conservative timeouts for corporate networks
            DefaultRequestTimeoutInSeconds: 90,

            // Corporate headers
            ExtraHeaders: map[string]string{
                "X-Corporate-ID":   os.Getenv("CORP_ID"),
                "X-Department":     "AI-Team",
                "X-Cost-Center":    "CC-123",
                "X-Compliance":     "SOC2-Type2",
            },

            // Aggressive retry for unreliable corporate networks
            MaxRetries:          5,
            RetryBackoffInitial: 1 * time.Second,
            RetryBackoffMax:     30 * time.Second,
        },
    }, nil
}
```

**HTTP Transport - Enterprise Setup:**

```json
{
  "providers": {
    "openai": {
      "proxy_config": {
        "type": "http",
        "url": "http://corporate-proxy.company.com:8080",
        "username": "env.PROXY_USER",
        "password": "env.PROXY_PASS"
      },
      "network_config": {
        "default_request_timeout_in_seconds": 90,
        "extra_headers": {
          "X-Corporate-ID": "corp-123",
          "X-Department": "AI-Team",
          "X-Cost-Center": "CC-123",
          "X-Compliance": "SOC2-Type2"
        },
        "max_retries": 5,
        "retry_backoff_initial_ms": 1000,
        "retry_backoff_max_ms": 30000
      }
    }
  }
}
```

</details>

### Multi-Region Configuration

<details>
<summary><strong>🌍 Multi-Region Setup</strong></summary>

**Go Package - Regional Endpoints:**

```go
func (a *MyAccount) GetConfigForProvider(provider schemas.ModelProvider) (*schemas.ProviderConfig, error) {
    region := os.Getenv("DEPLOYMENT_REGION")

    switch provider {
    case schemas.OpenAI:
        // Use regional endpoints for better latency
        baseURL := "https://api.openai.com"
        if region == "eu-west-1" {
            baseURL = "https://api.openai.com"  // OpenAI doesn't have regional endpoints
        }

        return &schemas.ProviderConfig{
            NetworkConfig: schemas.NetworkConfig{
                BaseURL: baseURL,
                ExtraHeaders: map[string]string{
                    "X-Region":           region,
                    "X-Preferred-Region": "eu-west-1",
                },
            },
        }, nil

    case schemas.Bedrock:
        // Use actual AWS regions
        bedrockRegion := "us-east-1"
        if region == "eu-west-1" {
            bedrockRegion = "eu-west-1"
        }

        return &schemas.ProviderConfig{
            MetaConfig: map[string]interface{}{
                "region": bedrockRegion,
            },
        }, nil
    }

    return &schemas.ProviderConfig{}, nil
}
```

</details>

---

## 🛠️ Best Practices

### Timeout Strategy

<details>
<summary><strong>⏱️ Recommended Timeout Values</strong></summary>

| Use Case             | Timeout | Max Retries | Initial Backoff |
| -------------------- | ------- | ----------- | --------------- |
| **Interactive Chat** | 30s     | 2           | 500ms           |
| **Batch Processing** | 120s    | 5           | 1s              |
| **Real-time API**    | 15s     | 1           | 250ms           |
| **Background Jobs**  | 300s    | 3           | 2s              |

```go
// Example: Interactive chat configuration
func getInteractiveChatConfig() *schemas.ProviderConfig {
    return &schemas.ProviderConfig{
        NetworkConfig: schemas.NetworkConfig{
            DefaultRequestTimeoutInSeconds: 30,
            MaxRetries:                     2,
            RetryBackoffInitial:            500 * time.Millisecond,
            RetryBackoffMax:                5 * time.Second,
        },
    }
}
```

</details>

### Proxy Best Practices

<details>
<summary><strong>🔗 Proxy Configuration Tips</strong></summary>

**Corporate Environment:**

```bash
# Set proxy environment variables
export HTTP_PROXY=http://proxy.corp.com:8080
export HTTPS_PROXY=http://proxy.corp.com:8080
export NO_PROXY=localhost,127.0.0.1,*.corp.com

# Test proxy connectivity
curl -v --proxy $HTTP_PROXY https://api.openai.com/v1/models
```

**Docker Environment:**

```dockerfile
# Pass proxy settings to container
ENV HTTP_PROXY=http://proxy.company.com:8080
ENV HTTPS_PROXY=http://proxy.company.com:8080
ENV NO_PROXY=localhost,127.0.0.1
```

</details>

---

## 🎯 Next Steps

| **Task**                     | **Documentation**                         |
| ---------------------------- | ----------------------------------------- |
| **🔑 Configure API keys**    | [Key Management](key-management.md)       |
| **🔗 Set up providers**      | [Providers](providers.md)                 |
| **⚡ Optimize performance**  | [Memory Management](memory-management.md) |
| **❌ Handle network errors** | [Error Handling](errors.md)               |

> **💡 Tip:** Always test your proxy and timeout settings in a staging environment before deploying to production.
