# 🔧 Provider Configuration

Complete guide to configuring AI providers in Bifrost HTTP transport through `config.json`.

> **💡 Quick Start:** See the [30-second setup](../../../quickstart/http-transport.md) for basic provider configuration.

---

## 📋 Configuration Overview (File Based)

> You can directly use the UI (`http://localhost:{port}/providers`) to configure the providers.

Provider configuration in `config.json` defines:

- **API credentials** and key management
- **Supported models** for each provider
- **Network settings** and retry behavior
- **Concurrency controls** and performance tuning
- **Provider-specific metadata** (regions, endpoints, etc.)

```json
{
  "providers": {
    "openai": {
      /* provider config */
    },
    "anthropic": {
      /* provider config */
    },
    "bedrock": {
      /* provider config */
    }
  }
}
```

---

## 🔑 Basic Provider Setup

### **OpenAI**

```json
{
  "providers": {
    "openai": {
      "keys": [
        {
          "value": "env.OPENAI_API_KEY",
          "models": [
            "gpt-3.5-turbo",
            "gpt-4",
            "gpt-4o",
            "gpt-4o-mini",
            "gpt-4-turbo",
            "gpt-4-vision-preview"
          ],
          "weight": 1.0
        }
      ],
      "network_config": {
        "default_request_timeout_in_seconds": 30,
        "max_retries": 1,
        "retry_backoff_initial_ms": 100,
        "retry_backoff_max_ms": 2000
      },
      "concurrency_and_buffer_size": {
        "concurrency": 3,
        "buffer_size": 10
      }
    }
  }
}
```

### **Anthropic**

```json
{
  "providers": {
    "anthropic": {
      "keys": [
        {
          "value": "env.ANTHROPIC_API_KEY",
          "models": [
            "claude-2.1",
            "claude-3-sonnet-20240229",
            "claude-3-haiku-20240307",
            "claude-3-opus-20240229",
            "claude-3-5-sonnet-20240620"
          ],
          "weight": 1.0
        }
      ],
      "network_config": {
        "default_request_timeout_in_seconds": 30,
        "max_retries": 1,
        "retry_backoff_initial_ms": 100,
        "retry_backoff_max_ms": 2000
      },
      "concurrency_and_buffer_size": {
        "concurrency": 3,
        "buffer_size": 10
      }
    }
  }
}
```

---

## 🌊 Advanced Provider Configuration

### **AWS Bedrock**

```json
{
  "providers": {
    "bedrock": {
      "keys": [
        {
          "value": "env.BEDROCK_API_KEY",
          "models": [
            "anthropic.claude-v2:1",
            "mistral.mixtral-8x7b-instruct-v0:1",
            "mistral.mistral-large-2402-v1:0",
            "anthropic.claude-3-sonnet-20240229-v1:0"
          ],
          "weight": 1.0
        }
      ],
      "network_config": {
        "default_request_timeout_in_seconds": 30,
        "max_retries": 1,
        "retry_backoff_initial_ms": 100,
        "retry_backoff_max_ms": 2000
      },
      "meta_config": {
        "secret_access_key": "env.AWS_SECRET_ACCESS_KEY",
        "region": "us-east-1"
      },
      "concurrency_and_buffer_size": {
        "concurrency": 3,
        "buffer_size": 10
      }
    }
  }
}
```

### **Azure OpenAI**

```json
{
  "providers": {
    "azure": {
      "keys": [
        {
          "value": "env.AZURE_API_KEY",
          "models": ["gpt-4o"],
          "weight": 1.0
        }
      ],
      "network_config": {
        "default_request_timeout_in_seconds": 30,
        "max_retries": 1,
        "retry_backoff_initial_ms": 100,
        "retry_backoff_max_ms": 2000
      },
      "meta_config": {
        "endpoint": "env.AZURE_ENDPOINT",
        "deployments": {
          "gpt-4o": "gpt-4o-aug"
        },
        "api_version": "2024-08-01-preview"
      },
      "concurrency_and_buffer_size": {
        "concurrency": 3,
        "buffer_size": 10
      }
    }
  }
}
```

### **Google Vertex AI**

```json
{
  "providers": {
    "vertex": {
      "keys": [],
      "meta_config": {
        "project_id": "env.VERTEX_PROJECT_ID",
        "region": "us-central1",
        "auth_credentials": "env.VERTEX_CREDENTIALS"
      },
      "concurrency_and_buffer_size": {
        "concurrency": 3,
        "buffer_size": 10
      }
    }
  }
}
```

---

## 🔐 Key Management

### **Multiple API Keys**

Balance load across multiple keys:

```json
{
  "providers": {
    "openai": {
      "keys": [
        {
          "value": "env.OPENAI_API_KEY_1",
          "models": ["gpt-4o-mini", "gpt-4o"],
          "weight": 0.7
        },
        {
          "value": "env.OPENAI_API_KEY_2",
          "models": ["gpt-4o-mini"],
          "weight": 0.3
        }
      ]
    }
  }
}
```

### **Model-Specific Keys**

Different keys for different models:

```json
{
  "providers": {
    "openai": {
      "keys": [
        {
          "value": "env.OPENAI_API_KEY_BASIC",
          "models": ["gpt-3.5-turbo", "gpt-4o-mini"],
          "weight": 1.0
        },
        {
          "value": "env.OPENAI_API_KEY_PREMIUM",
          "models": ["gpt-4o", "gpt-4-turbo"],
          "weight": 1.0
        }
      ]
    }
  }
}
```

---

## 🌐 Network Configuration

### **Custom Headers and Timeouts**

```json
{
  "providers": {
    "openai": {
      "keys": [
        /* ... */
      ],
      "network_config": {
        "extra_headers": {
          "X-Organization-ID": "org-123",
          "X-Environment": "production"
        },
        "default_request_timeout_in_seconds": 60,
        "max_retries": 3,
        "retry_backoff_initial_ms": 200,
        "retry_backoff_max_ms": 5000
      }
    }
  }
}
```

### **Proxy Configuration**

```json
{
  "providers": {
    "openai": {
      "keys": [
        /* ... */
      ],
      "network_config": {
        "proxy_url": "http://proxy.company.com:8080",
        "proxy_auth": {
          "username": "env.PROXY_USER",
          "password": "env.PROXY_PASS"
        }
      }
    }
  }
}
```

---

## ⚡ Performance Tuning

### **Concurrency Controls**

```json
{
  "providers": {
    "openai": {
      "keys": [
        /* ... */
      ],
      "concurrency_and_buffer_size": {
        "concurrency": 10, // Number of concurrent requests
        "buffer_size": 50 // Request queue buffer size
      }
    },
    "anthropic": {
      "keys": [
        /* ... */
      ],
      "concurrency_and_buffer_size": {
        "concurrency": 5, // Lower concurrency for rate limits
        "buffer_size": 20
      }
    }
  }
}
```

### **High-Volume Configuration**

For production workloads:

```json
{
  "providers": {
    "openai": {
      "keys": [
        {
          "value": "env.OPENAI_API_KEY_1",
          "models": ["gpt-4o-mini"],
          "weight": 0.5
        },
        {
          "value": "env.OPENAI_API_KEY_2",
          "models": ["gpt-4o-mini"],
          "weight": 0.5
        }
      ],
      "network_config": {
        "default_request_timeout_in_seconds": 45,
        "max_retries": 2,
        "retry_backoff_initial_ms": 150,
        "retry_backoff_max_ms": 3000
      },
      "concurrency_and_buffer_size": {
        "concurrency": 20,
        "buffer_size": 100
      }
    }
  }
}
```

---

## 🌍 Multi-Provider Setup

### **Production Configuration**

```json
{
  "providers": {
    "openai": {
      "keys": [
        {
          "value": "env.OPENAI_API_KEY",
          "models": ["gpt-4o-mini", "gpt-4o", "gpt-4-turbo"],
          "weight": 1.0
        }
      ],
      "concurrency_and_buffer_size": {
        "concurrency": 15,
        "buffer_size": 75
      }
    },
    "anthropic": {
      "keys": [
        {
          "value": "env.ANTHROPIC_API_KEY",
          "models": ["claude-3-sonnet-20240229", "claude-3-haiku-20240307"],
          "weight": 1.0
        }
      ],
      "concurrency_and_buffer_size": {
        "concurrency": 10,
        "buffer_size": 50
      }
    },
    "bedrock": {
      "keys": [
        {
          "value": "env.BEDROCK_API_KEY",
          "models": ["anthropic.claude-3-sonnet-20240229-v1:0"],
          "weight": 1.0
        }
      ],
      "meta_config": {
        "secret_access_key": "env.AWS_SECRET_ACCESS_KEY",
        "region": "us-east-1"
      },
      "concurrency_and_buffer_size": {
        "concurrency": 8,
        "buffer_size": 40
      }
    },
    "cohere": {
      "keys": [
        {
          "value": "env.COHERE_API_KEY",
          "models": ["command-a-03-2025"],
          "weight": 1.0
        }
      ],
      "concurrency_and_buffer_size": {
        "concurrency": 5,
        "buffer_size": 25
      }
    }
  }
}
```

---

## 🔧 Environment Variables

### **Required Variables**

Set these environment variables before starting Bifrost:

```bash
# OpenAI
export OPENAI_API_KEY="sk-..."

# Anthropic
export ANTHROPIC_API_KEY="sk-ant-..."

# AWS Bedrock
export BEDROCK_API_KEY="your-access-key"
export AWS_SECRET_ACCESS_KEY="your-secret-key"

# Azure OpenAI
export AZURE_API_KEY="your-azure-key"
export AZURE_ENDPOINT="https://your-resource.openai.azure.com"

# Google Vertex AI
export VERTEX_PROJECT_ID="your-project-id"
export VERTEX_CREDENTIALS="/path/to/service-account.json"

# Cohere
export COHERE_API_KEY="your-cohere-key"

# Mistral
export MISTRAL_API_KEY="your-mistral-key"
```

### **Docker Environment**

```bash
docker run -p 8080:8080 \
  -v $(pwd)/config.json:/app/config/config.json \
  -e OPENAI_API_KEY \
  -e ANTHROPIC_API_KEY \
  -e BEDROCK_API_KEY \
  -e AWS_SECRET_ACCESS_KEY \
  maximhq/bifrost
```

---

## 🧪 Testing Configuration

### **Validate Provider Setup**

```bash
# Test OpenAI provider
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "openai/gpt-4o-mini",
    "messages": [{"role": "user", "content": "Test message"}]
  }'

# Test with fallbacks
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "openai/gpt-4o-mini",
    "messages": [{"role": "user", "content": "Test message"}],
    "fallbacks": [
      "anthropic/claude-3-sonnet-20240229"
    ]
  }'
```

### **Configuration Validation**

```bash
# Start Bifrost with config validation
bifrost-http -config config.json -validate

# Check which providers are loaded
curl http://localhost:8080/metrics | grep bifrost_providers
```

---

## 📚 Related Documentation

- **[🌐 HTTP Transport Overview](../README.md)** - Main HTTP transport guide
- **[🌐 Endpoints](../endpoints.md)** - Available HTTP endpoints
- **[🔗 Migration Guide](../integrations/migration-guide.md)** - Migrating from existing providers
- **[🛠️ MCP Configuration](./mcp.md)** - Adding external tools

> **🏛️ Architecture:** For provider selection algorithms and load balancing, see [Architecture Documentation](../../../architecture/README.md).
