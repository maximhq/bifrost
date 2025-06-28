# 🌐 HTTP Transport Configuration

Complete guide for configuring Bifrost HTTP transport, including server settings, provider configuration, MCP integration, and production deployment options.

## 📋 Configuration Overview

Bifrost HTTP transport uses a JSON configuration file combined with environment variables for flexible, secure configuration management.

### **Configuration Structure**

```json
{
  "providers": {
    // Provider configurations
  },
  "mcp": {
    // MCP server configurations
  },
  "plugins": [
    // Plugin configurations
  ]
}
```

---

## 🚀 Quick Start

### **Minimal Configuration**

```json
{
  "providers": {
    "openai": {
      "keys": [
        {
          "value": "env.OPENAI_API_KEY",
          "models": ["gpt-4o", "gpt-4o-mini"],
          "weight": 1.0
        }
      ]
    }
  }
}
```

### **Environment Variables**

```bash
# Required API keys
export OPENAI_API_KEY="sk-your-openai-key"

# Optional server configuration
export APP_PORT=8080
export APP_POOL_SIZE=300
export APP_DROP_EXCESS_REQUESTS=false
```

### **Running the Server**

```bash
# Using Go binary
go install github.com/maximhq/bifrost/transports/bifrost-http@latest
bifrost-http -config config.json -port 8080

# Using Docker
docker run -p 8080:8080 \
  -v $(pwd)/config.json:/app/config/config.json \
  -e OPENAI_API_KEY \
  maximhq/bifrost
```

---

## ⚙️ Provider Configuration

### **Basic Provider Setup**

```json
{
  "providers": {
    "openai": {
      "keys": [
        {
          "value": "env.OPENAI_API_KEY",
          "models": ["gpt-4o", "gpt-4o-mini", "gpt-4", "gpt-3.5-turbo"],
          "weight": 1.0
        }
      ]
    },
    "anthropic": {
      "keys": [
        {
          "value": "env.ANTHROPIC_API_KEY",
          "models": ["claude-3-sonnet-20240229", "claude-3-haiku-20240307"],
          "weight": 1.0
        }
      ]
    }
  }
}
```

### **Multi-Key Load Balancing**

```json
{
  "providers": {
    "openai": {
      "keys": [
        {
          "value": "env.OPENAI_API_KEY_1",
          "models": ["gpt-4o"],
          "weight": 0.6
        },
        {
          "value": "env.OPENAI_API_KEY_2",
          "models": ["gpt-4o", "gpt-4o-mini"],
          "weight": 0.4
        }
      ]
    }
  }
}
```

### **Advanced Provider Configuration**

```json
{
  "providers": {
    "openai": {
      "keys": [
        {
          "value": "env.OPENAI_API_KEY",
          "models": ["gpt-4o"],
          "weight": 1.0
        }
      ],
      "network_config": {
        "base_url": "https://api.openai.com/v1",
        "extra_headers": {
          "OpenAI-Organization": "your-org-id",
          "User-Agent": "MyApp/1.0"
        },
        "default_request_timeout_in_seconds": 60,
        "max_retries": 3,
        "retry_backoff_initial": 200,
        "retry_backoff_max": 5000
      },
      "concurrency_and_buffer_size": {
        "concurrency": 20,
        "buffer_size": 200
      },
      "proxy_config": {
        "type": "http",
        "url": "http://proxy.company.com:8080",
        "username": "user",
        "password": "pass"
      }
    }
  }
}
```

---

## 🔐 Provider-Specific Configurations

### **Azure OpenAI**

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

### **AWS Bedrock**

```json
{
  "providers": {
    "bedrock": {
      "keys": [
        {
          "value": "env.BEDROCK_ACCESS_KEY",
          "models": ["anthropic.claude-3-sonnet-20240229-v1:0"],
          "weight": 1.0
        }
      ],
      "meta_config": {
        "secret_access_key": "env.AWS_SECRET_ACCESS_KEY",
        "region": "env.AWS_REGION",
        "session_token": "env.AWS_SESSION_TOKEN"
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
      "keys": [
        {
          "value": "env.VERTEX_API_KEY",
          "models": ["gemini-pro", "gemini-pro-vision"],
          "weight": 1.0
        }
      ],
      "meta_config": {
        "project_id": "env.VERTEX_PROJECT_ID",
        "location": "us-central1",
        "auth_credentials": "env.VERTEX_AUTH_CREDENTIALS"
      }
    }
  }
}
```

---

## 🛠️ MCP Integration Configuration

### **Basic MCP Setup**

```json
{
  "providers": {
    "openai": {
      "keys": [
        { "value": "env.OPENAI_API_KEY", "models": ["gpt-4o"], "weight": 1.0 }
      ]
    }
  },
  "mcp": {
    "client_configs": [
      {
        "name": "filesystem",
        "connection_type": "stdio",
        "stdio_config": {
          "command": "npx",
          "args": ["-y", "@modelcontextprotocol/server-filesystem", "/tmp"],
          "envs": ["NODE_ENV", "FILESYSTEM_ROOT"]
        },
        "tools_to_skip": [],
        "tools_to_execute": []
      }
    ]
  }
}
```

### **Multiple MCP Servers**

```json
{
  "mcp": {
    "client_configs": [
      {
        "name": "filesystem",
        "connection_type": "stdio",
        "stdio_config": {
          "command": "npx",
          "args": ["-y", "@modelcontextprotocol/server-filesystem", "/tmp"],
          "envs": ["NODE_ENV"]
        }
      },
      {
        "name": "web-search",
        "connection_type": "http",
        "connection_string": "http://localhost:3001/mcp",
        "tools_to_skip": [],
        "tools_to_execute": ["search_web", "get_page_content"]
      },
      {
        "name": "real-time-data",
        "connection_type": "sse",
        "connection_string": "http://localhost:3002/sse"
      }
    ]
  }
}
```

### **MCP Environment Variables**

```bash
# MCP-specific environment variables (no env. prefix)
export NODE_ENV=production
export FILESYSTEM_ROOT=/tmp
export WEATHER_API_KEY=your-weather-key
export DATABASE_URL=postgres://user:pass@localhost/db
```

> **Important**: MCP environment variables in the `envs` array do **NOT** use the `env.` prefix. Bifrost checks these variables exist before establishing MCP connections.

---

## 🔧 Server Configuration

### **Runtime Configuration**

```bash
# Command line flags
bifrost-http \
  -config config.json \
  -port 8080 \
  -pool-size 300 \
  -drop-excess-requests=false \
  -plugins maxim,mocker \
  -prometheus-labels team-id,task-id,location
```

### **Environment Variables**

| Variable                   | Default | Description                           |
| -------------------------- | ------- | ------------------------------------- |
| `APP_PORT`                 | `8080`  | Server port                           |
| `APP_POOL_SIZE`            | `300`   | Connection pool size                  |
| `APP_DROP_EXCESS_REQUESTS` | `false` | Drop excess requests when buffer full |
| `APP_PLUGINS`              | `""`    | Comma-separated list of plugins       |

### **Docker Configuration**

```bash
# Docker with environment variables
docker run -p 8080:8080 \
  -v $(pwd)/config.json:/app/config/config.json \
  -e OPENAI_API_KEY \
  -e ANTHROPIC_API_KEY \
  -e APP_PORT=8080 \
  -e APP_POOL_SIZE=500 \
  -e APP_DROP_EXCESS_REQUESTS=true \
  maximhq/bifrost
```

---

## 🔌 Plugin Configuration

### **Built-in Plugins**

```bash
# Enable plugins via command line
bifrost-http -config config.json -plugins maxim,mocker

# Or via environment variable
export APP_PLUGINS=maxim,mocker
```

### **Plugin-Specific Configuration**

Some plugins may require additional configuration. Check individual plugin documentation:

- **Maxim Plugin**: Performance optimization and caching
- **Mocker Plugin**: Mock responses for testing

---

## 📊 Monitoring & Observability

### **Prometheus Metrics**

```bash
# Enable Prometheus with custom labels
bifrost-http \
  -config config.json \
  -prometheus-labels team-id,environment,version

# Metrics available at /metrics endpoint
curl http://localhost:8080/metrics
```

**Available Metrics:**

- `http_requests_total` - Total HTTP requests
- `http_request_duration_seconds` - Request duration
- `http_request_size_bytes` - Request size
- `http_response_size_bytes` - Response size
- `bifrost_upstream_requests_total` - Provider requests
- `bifrost_upstream_latency_seconds` - Provider latency

### **Custom Prometheus Labels**

Add custom labels via request headers:

```bash
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "x-bf-prom-team-id: backend-team" \
  -H "x-bf-prom-environment: production" \
  -d '{"provider": "openai", "model": "gpt-4o", "messages": [...]}'
```

---

## 🚀 Production Configuration Examples

### **High-Availability Setup**

```json
{
  "providers": {
    "openai-primary": {
      "keys": [
        { "value": "env.OPENAI_API_KEY_1", "models": ["gpt-4o"], "weight": 0.7 }
      ]
    },
    "openai-secondary": {
      "keys": [
        { "value": "env.OPENAI_API_KEY_2", "models": ["gpt-4o"], "weight": 0.3 }
      ]
    },
    "anthropic-fallback": {
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

### **Multi-Region Azure Setup**

```json
{
  "providers": {
    "azure-eastus": {
      "keys": [
        { "value": "env.AZURE_EASTUS_KEY", "models": ["gpt-4o"], "weight": 0.6 }
      ],
      "meta_config": {
        "endpoint": "https://eastus-resource.openai.azure.com",
        "api_version": "2024-02-15-preview",
        "deployments": { "gpt-4o": "gpt-4o-eastus-deployment" }
      }
    },
    "azure-westus": {
      "keys": [
        { "value": "env.AZURE_WESTUS_KEY", "models": ["gpt-4o"], "weight": 0.4 }
      ],
      "meta_config": {
        "endpoint": "https://westus-resource.openai.azure.com",
        "api_version": "2024-02-15-preview",
        "deployments": { "gpt-4o": "gpt-4o-westus-deployment" }
      }
    }
  }
}
```

### **Performance-Optimized Configuration**

```json
{
  "providers": {
    "openai": {
      "keys": [
        { "value": "env.OPENAI_API_KEY", "models": ["gpt-4o"], "weight": 1.0 }
      ],
      "concurrency_and_buffer_size": {
        "concurrency": 50,
        "buffer_size": 500
      },
      "network_config": {
        "default_request_timeout_in_seconds": 30,
        "max_retries": 2,
        "retry_backoff_initial": 100,
        "retry_backoff_max": 2000
      }
    }
  }
}
```

---

## 🔒 Security Best Practices

### **Environment Variable Management**

```bash
# Use env. prefix for sensitive values in config
{
  "providers": {
    "openai": {
      "keys": [{"value": "env.OPENAI_API_KEY", "weight": 1.0}]
    }
  }
}

# Set in environment
export OPENAI_API_KEY="sk-your-actual-key"
```

### **Network Security**

```json
{
  "providers": {
    "openai": {
      "keys": [...],
      "proxy_config": {
        "type": "http",
        "url": "http://corporate-proxy:8080"
      },
      "network_config": {
        "extra_headers": {
          "User-Agent": "MyApp/1.0",
          "X-Forwarded-For": "internal"
        }
      }
    }
  }
}
```

---

## 🔗 Related Documentation

- **[Core Configuration](core-config.md)** - Go package configuration
- **[Environment Variables](environment-variables.md)** - Complete environment variable reference
- **[Security Best Practices](security.md)** - Security configuration guide
- **[Provider Configuration](../features/providers/README.md)** - Provider-specific setup
- **[MCP Integration](../features/mcp-integration.md)** - Complete MCP guide

---

**⚡ Ready for production?** Check our [deployment guides](deployment/README.md) for scaling and optimization strategies.
