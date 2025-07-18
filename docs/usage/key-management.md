# 🔑 Key Management

Advanced API key management with weighted distribution, automatic rotation, and model-specific assignments across all providers.

## 📋 Overview

**Key Management Features:**

- ✅ **Multiple Keys per Provider** - Distribute load across multiple API keys
- ✅ **Weighted Distribution** - Control traffic distribution with custom weights
- ✅ **Model-Specific Keys** - Assign keys to specific models only
- ✅ **Automatic Rotation** - Seamless failover when keys are rate-limited
- ✅ **Load Balancing** - Intelligent request distribution
- ✅ **Cost Optimization** - Use different keys for different cost tiers

**Benefits:**

- 🛡️ **Higher Rate Limits** - Combine multiple keys for increased throughput
- ⚡ **Improved Reliability** - Automatic failover prevents service interruption
- 💰 **Cost Control** - Route traffic based on budget and usage patterns
- 🔧 **Zero Downtime** - Hot-swap keys without service interruption

---

## ⚡ Basic Key Setup

### Single Key Configuration

<details open>
<summary><strong>🔧 Go Package Usage</strong></summary>

The `GetKeysForProvider` method allows you to implement custom key selection logic for each provider. The method receives a context parameter that carries data set by plugin pre-hooks, enabling dynamic key selection based on plugin-defined criteria.

For example, plugins can set request metadata, user preferences, or routing rules in the context during their pre-hook phase. Your key management implementation can then access this data to make informed decisions about which keys to return. This is particularly useful for scenarios like:

- Route requests to specific API keys based on user roles or permissions
- Implement key rotation based on request patterns
- Apply custom rate limiting or quota management
- Select keys based on geographical routing rules
- Use different keys for different types of requests or model configurations

Here's a basic example implementation:

```go
func (a *MyAccount) GetKeysForProvider(ctx *context.Context, provider schemas.ModelProvider) ([]schemas.Key, error) {
    switch provider {
    case schemas.OpenAI:
        return []schemas.Key{
            {
                Value:  os.Getenv("OPENAI_API_KEY"),
                Models: []string{"gpt-4o-mini", "gpt-4o"},
                Weight: 1.0,  // 100% of traffic
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
    }
    return nil, fmt.Errorf("provider not configured")
}
```

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
    }
  }
}
```

**Environment variables:**

```bash
export OPENAI_API_KEY="sk-..."
export ANTHROPIC_API_KEY="sk-ant-..."
```

</details>

---

### Context-Aware Key Selection

<details>
<summary><strong>🔧 Go Package - Context Usage</strong></summary>

The `GetKeysForProvider` method receives a context that can contain data from any source that sets values before the Bifrost request. This includes plugin pre-hooks, application logic, middleware, or direct context manipulation. Here's an example that demonstrates various context-based key selection strategies:

```go
type ContextAwareAccount struct {
    standardKeys []schemas.Key
    premiumKeys  []schemas.Key
}

func (a *ContextAwareAccount) GetKeysForProvider(ctx *context.Context, provider schemas.ModelProvider) ([]schemas.Key, error) {
    if provider != schemas.OpenAI {
        return nil, fmt.Errorf("provider not supported")
    }

    // Access context values from any source
    if ctx != nil {
        // Example: Application-set user role
        if userRole, ok := (*ctx).Value("user_role").(string); ok {
            switch userRole {
            case "premium":
                return a.premiumKeys, nil
            case "standard":
                return a.standardKeys, nil
            }
        }

        // Example: Middleware-set geographic region
        if region, ok := (*ctx).Value("geo_region").(string); ok {
            // Return region-specific keys
            switch region {
            case "eu":
                return []schemas.Key{{
                    Value:  os.Getenv("OPENAI_EU_KEY"),
                    Models: []string{"gpt-4o-mini", "gpt-4o"},
                    Weight: 1.0,
                }}, nil
            case "us":
                return []schemas.Key{{
                    Value:  os.Getenv("OPENAI_US_KEY"),
                    Models: []string{"gpt-4o-mini", "gpt-4o"},
                    Weight: 1.0,
                }}, nil
            }
        }

        // Example: Plugin-set request priority
        if priority, ok := (*ctx).Value("request_priority").(string); ok {
            switch priority {
            case "high":
                return []schemas.Key{{
                    Value:  os.Getenv("OPENAI_DEDICATED_KEY"),
                    Models: []string{"gpt-4o"},
                    Weight: 1.0,
                }}, nil
            }
        }

        // Example: Direct context value from application code
        if customKey, ok := (*ctx).Value("custom_api_key").(string); ok {
            return []schemas.Key{{
                Value:  customKey,
                Models: []string{"gpt-4o-mini", "gpt-4o"},
                Weight: 1.0,
            }}, nil
        }
    }

    // Default to standard keys if no context or matching criteria
    return a.standardKeys, nil
}
```

This implementation demonstrates:
- Reading context values set by various sources
- Application-level user role based selection
- Geographic routing from middleware
- Priority-based selection from plugins
- Custom key injection through direct context manipulation

You can set context values in several ways:

```go
// Direct in your application code
ctx := context.WithValue(context.Background(), "user_role", "premium")

// In middleware
func MyMiddleware(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        ctx := context.WithValue(r.Context(), "geo_region", "eu")
        next.ServeHTTP(w, r.WithContext(ctx))
    })
}

// In a plugin's PreHook
func (p *MyPlugin) PreHook(ctx *context.Context, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.PluginShortCircuit, error) {
    *ctx = context.WithValue(*ctx, "request_priority", "high")
    return req, nil, nil
}

// When making a Bifrost request
ctx := context.WithValue(context.Background(), "custom_api_key", "sk-...")
response, err := client.ChatCompletionRequest(ctx, request)
```

</details>

---

## 🔄 Key Distribution Strategies

### Load Balancing Strategy

Distribute requests evenly across multiple keys for maximum throughput:

<details>
<summary><strong>🔧 Go Package - Equal Distribution</strong></summary>

```go
func (a *MyAccount) GetKeysForProvider(ctx *context.Context, provider schemas.ModelProvider) ([]schemas.Key, error) {
    if provider == schemas.OpenAI {
        return []schemas.Key{
            {
                Value:  os.Getenv("OPENAI_KEY_1"),
                Models: []string{"gpt-4o-mini", "gpt-4o"},
                Weight: 0.25,  // 25% each for even distribution
            },
            {
                Value:  os.Getenv("OPENAI_KEY_2"),
                Models: []string{"gpt-4o-mini", "gpt-4o"},
                Weight: 0.25,
            },
            {
                Value:  os.Getenv("OPENAI_KEY_3"),
                Models: []string{"gpt-4o-mini", "gpt-4o"},
                Weight: 0.25,
            },
            {
                Value:  os.Getenv("OPENAI_KEY_4"),
                Models: []string{"gpt-4o-mini", "gpt-4o"},
                Weight: 0.25,
            },
        }, nil
    }
    return nil, fmt.Errorf("provider not configured")
}
```

</details>

<details>
<summary><strong>🌐 HTTP Transport - Equal Distribution</strong></summary>

```json
{
  "providers": {
    "openai": {
      "keys": [
        {
          "value": "env.OPENAI_KEY_1",
          "models": ["gpt-4o-mini", "gpt-4o"],
          "weight": 0.25
        },
        {
          "value": "env.OPENAI_KEY_2",
          "models": ["gpt-4o-mini", "gpt-4o"],
          "weight": 0.25
        },
        {
          "value": "env.OPENAI_KEY_3",
          "models": ["gpt-4o-mini", "gpt-4o"],
          "weight": 0.25
        },
        {
          "value": "env.OPENAI_KEY_4",
          "models": ["gpt-4o-mini", "gpt-4o"],
          "weight": 0.25
        }
      ]
    }
  }
}
```

**Environment setup:**

```bash
export OPENAI_KEY_1="sk-1..."
export OPENAI_KEY_2="sk-2..."
export OPENAI_KEY_3="sk-3..."
export OPENAI_KEY_4="sk-4..."
```

</details>

### Tiered Access Strategy

Use premium keys for expensive models, standard keys for cheaper models:

<details>
<summary><strong>🔧 Go Package - Tiered Strategy</strong></summary>

```go
func (a *MyAccount) GetKeysForProvider(ctx *context.Context, provider schemas.ModelProvider) ([]schemas.Key, error) {
    if provider == schemas.OpenAI {
        return []schemas.Key{
            // Standard keys for cheap models
            {
                Value:  os.Getenv("OPENAI_STANDARD_KEY_1"),
                Models: []string{"gpt-4o-mini"},  // Cheap model only
                Weight: 0.4,
            },
            {
                Value:  os.Getenv("OPENAI_STANDARD_KEY_2"),
                Models: []string{"gpt-4o-mini"},
                Weight: 0.3,
            },
            // Premium keys for expensive models
            {
                Value:  os.Getenv("OPENAI_PREMIUM_KEY_1"),
                Models: []string{"gpt-4o", "gpt-4o-mini"},  // All models
                Weight: 0.2,
            },
            {
                Value:  os.Getenv("OPENAI_PREMIUM_KEY_2"),
                Models: []string{"gpt-4o", "gpt-4o-mini"},
                Weight: 0.1,
            },
        }, nil
    }
    return nil, fmt.Errorf("provider not configured")
}
```

**Result:** Cost optimization with dedicated premium keys for expensive models

</details>

<details>
<summary><strong>🌐 HTTP Transport - Tiered Strategy</strong></summary>

```json
{
  "providers": {
    "openai": {
      "keys": [
        {
          "value": "env.OPENAI_STANDARD_KEY_1",
          "models": ["gpt-4o-mini"],
          "weight": 0.4
        },
        {
          "value": "env.OPENAI_STANDARD_KEY_2",
          "models": ["gpt-4o-mini"],
          "weight": 0.3
        },
        {
          "value": "env.OPENAI_PREMIUM_KEY_1",
          "models": ["gpt-4o", "gpt-4o-mini"],
          "weight": 0.2
        },
        {
          "value": "env.OPENAI_PREMIUM_KEY_2",
          "models": ["gpt-4o", "gpt-4o-mini"],
          "weight": 0.1
        }
      ]
    }
  }
}
```

</details>

### Priority-Based Strategy

Route traffic based on key priority and reliability:

<details>
<summary><strong>🔧 Go Package - Priority Strategy</strong></summary>

```go
func (a *MyAccount) GetKeysForProvider(ctx *context.Context, provider schemas.ModelProvider) ([]schemas.Key, error) {
    if provider == schemas.OpenAI {
        return []schemas.Key{
            // Primary key (highest priority)
            {
                Value:  os.Getenv("OPENAI_PRIMARY_KEY"),
                Models: []string{"gpt-4o-mini", "gpt-4o"},
                Weight: 0.6,  // 60% traffic to primary
            },
            // Secondary keys (backup)
            {
                Value:  os.Getenv("OPENAI_BACKUP_KEY_1"),
                Models: []string{"gpt-4o-mini", "gpt-4o"},
                Weight: 0.3,  // 30% to first backup
            },
            {
                Value:  os.Getenv("OPENAI_BACKUP_KEY_2"),
                Models: []string{"gpt-4o-mini", "gpt-4o"},
                Weight: 0.1,  // 10% to second backup
            },
        }, nil
    }
    return nil, fmt.Errorf("provider not configured")
}
```

</details>

<details>
<summary><strong>🌐 HTTP Transport - Priority Strategy</strong></summary>

```json
{
  "providers": {
    "openai": {
      "keys": [
        {
          "value": "env.OPENAI_PRIMARY_KEY",
          "models": ["gpt-4o-mini", "gpt-4o"],
          "weight": 0.6
        },
        {
          "value": "env.OPENAI_BACKUP_KEY_1",
          "models": ["gpt-4o-mini", "gpt-4o"],
          "weight": 0.3
        },
        {
          "value": "env.OPENAI_BACKUP_KEY_2",
          "models": ["gpt-4o-mini", "gpt-4o"],
          "weight": 0.1
        }
      ]
    }
  }
}
```

</details>

---

## 🎯 Advanced Key Patterns

### Multi-Provider Key Management

<details>
<summary><strong>🔧 Go Package - Cross-Provider Keys</strong></summary>

```go
func (a *MyAccount) GetKeysForProvider(ctx *context.Context, provider schemas.ModelProvider) ([]schemas.Key, error) {
    switch provider {
    case schemas.OpenAI:
        return []schemas.Key{
            {
                Value:  os.Getenv("OPENAI_KEY_1"),
                Models: []string{"gpt-4o-mini", "gpt-4o"},
                Weight: 0.7,
            },
            {
                Value:  os.Getenv("OPENAI_KEY_2"),
                Models: []string{"gpt-4o"},
                Weight: 0.3,
            },
        }, nil
    case schemas.Anthropic:
        return []schemas.Key{
            {
                Value:  os.Getenv("ANTHROPIC_KEY_1"),
                Models: []string{"claude-3-5-sonnet-20241022"},
                Weight: 0.8,
            },
            {
                Value:  os.Getenv("ANTHROPIC_KEY_2"),
                Models: []string{"claude-3-5-sonnet-20241022"},
                Weight: 0.2,
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
```

</details>

<details>
<summary><strong>🌐 HTTP Transport - Cross-Provider Keys</strong></summary>

```json
{
  "providers": {
    "openai": {
      "keys": [
        {
          "value": "env.OPENAI_KEY_1",
          "models": ["gpt-4o-mini", "gpt-4o"],
          "weight": 0.7
        },
        {
          "value": "env.OPENAI_KEY_2",
          "models": ["gpt-4o"],
          "weight": 0.3
        }
      ]
    },
    "anthropic": {
      "keys": [
        {
          "value": "env.ANTHROPIC_KEY_1",
          "models": ["claude-3-5-sonnet-20241022"],
          "weight": 0.8
        },
        {
          "value": "env.ANTHROPIC_KEY_2",
          "models": ["claude-3-5-sonnet-20241022"],
          "weight": 0.2
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

</details>

### Dynamic Key Selection

<details>
<summary><strong>🔧 Go Package - Runtime Key Selection</strong></summary>

```go
type DynamicAccount struct {
    keyRotationInterval time.Duration
    lastRotation        time.Time
    currentKeyIndex     int
    keys                map[schemas.ModelProvider][]schemas.Key
}

func (a *DynamicAccount) GetKeysForProvider(ctx *context.Context, provider schemas.ModelProvider) ([]schemas.Key, error) {
    // Rotate keys every hour
    if time.Since(a.lastRotation) > a.keyRotationInterval {
        a.rotateKeys()
        a.lastRotation = time.Now()
    }

    if keys, exists := a.keys[provider]; exists {
        return keys, nil
    }
    return nil, fmt.Errorf("provider not configured")
}

func (a *DynamicAccount) rotateKeys() {
    // Implement key rotation logic
    // Could fetch new keys from secret management system
    log.Info("Rotating API keys...")
}
```

</details>

<details>
<summary><strong>🌐 HTTP Transport - Hot Key Reload</strong></summary>

This feature is under development.

</details>

---

## 📊 Key Selection Algorithm

Bifrost uses weighted random selection for key distribution:

```text
Key Selection Process:
1. Filter keys by requested model
2. Calculate total weight of available keys
3. Generate random number between 0 and total weight
4. Select key based on weighted probability
5. Fallback to next available key if selected key fails
```

**Example with 3 keys:**

| Key   | Weight | Probability | Traffic Distribution |
| ----- | ------ | ----------- | -------------------- |
| Key A | 0.5    | 50%         | ~50% of requests     |
| Key B | 0.3    | 30%         | ~30% of requests     |
| Key C | 0.2    | 20%         | ~20% of requests     |

---

## 🛠️ Best Practices

### Security Best Practices

<details>
<summary><strong>🔒 Environment Variable Management</strong></summary>

**Recommended approach:**

```bash
# Use descriptive naming
export OPENAI_PRIMARY_KEY="sk-..."
export OPENAI_FALLBACK_KEY="sk-..."
export ANTHROPIC_PRODUCTION_KEY="sk-ant-..."

# Avoid hardcoding in config files
# ❌ Bad
{
  "value": "sk-actual-key-here"
}

# ✅ Good
{
  "value": "env.OPENAI_API_KEY"
}
```

</details>

<details>
<summary><strong>🔄 Key Rotation Schedule</strong></summary>

**Recommended rotation schedule:**

```text
• Production keys: Every 30 days
• Development keys: Every 90 days
• Backup keys: Every 60 days
• Emergency keys: Keep fresh, rotate every 14 days
```

**Implementation:**

```go
// Track key age and force rotation
type KeyWithMetadata struct {
    schemas.Key
    CreatedAt time.Time
    LastUsed  time.Time
}

func (k *KeyWithMetadata) ShouldRotate() bool {
    return time.Since(k.CreatedAt) > 30*24*time.Hour // 30 days
}
```

</details>

### Performance Optimization

<details>
<summary><strong>⚡ Weight Optimization</strong></summary>

**High-throughput scenario:**

```json
{
  "providers": {
    "openai": {
      "keys": [
        {
          "value": "env.OPENAI_HIGH_LIMIT_KEY",
          "models": ["gpt-4o-mini"],
          "weight": 0.8
        },
        {
          "value": "env.OPENAI_STANDARD_KEY",
          "models": ["gpt-4o-mini"],
          "weight": 0.2
        }
      ]
    }
  }
}
```

**Cost-optimized scenario:**

```json
{
  "providers": {
    "openai": {
      "keys": [
        {
          "value": "env.OPENAI_CHEAP_KEY",
          "models": ["gpt-4o-mini"],
          "weight": 0.9
        },
        {
          "value": "env.OPENAI_PREMIUM_KEY",
          "models": ["gpt-4o"],
          "weight": 0.1
        }
      ]
    }
  }
}
```

</details>

---

## 🎯 Next Steps

| **Task**                    | **Documentation**                         |
| --------------------------- | ----------------------------------------- |
| **🔗 Configure providers**  | [Providers](providers.md)                 |
| **🌐 Set up networking**    | [Networking](networking.md)               |
| **⚡ Optimize performance** | [Memory Management](memory-management.md) |
| **❌ Handle failures**      | [Error Handling](errors.md)               |

> **💡 Tip:** Use weights that sum to 1.0 for easier percentage calculations, but Bifrost automatically normalizes weights if they don't sum to 1.0.
