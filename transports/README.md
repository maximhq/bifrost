# 🌐 Bifrost Transports

Bifrost Transports let you run Bifrost as a blazing-fast HTTP API or integrate it directly as a Go package. Connect to any AI provider (OpenAI, Anthropic, Bedrock, and more) in seconds with automatic fallbacks and advanced features.

📖 **Complete documentation**: [docs/usage/http-transport/](../docs/usage/http-transport/)

---

## 🚀 Quick Start

### Docker

```bash
# Pull and run Bifrost HTTP API
docker pull maximhq/bifrost
docker run -p 8080:8080 maximhq/bifrost

# 🖥️ Open web interface for visual configuration
# macOS:
open http://localhost:8080
# Linux:
xdg-open http://localhost:8080
# Windows:
start http://localhost:8080
# Or simply open http://localhost:8080 manually in your browser
```

**🎉 That's it!** No config files needed. Configure providers, monitor requests, and manage everything through the **built-in web interface**.

### Binary

```bash
# Install and run locally
npx -y @maximhq/bifrost

# Open web interface at http://localhost:8080
```

### Volume Mount (Optional)

```bash
# For configuration persistence across restarts
docker run -p 8080:8080 -v $(pwd)/data:/app/data maximhq/bifrost
```

**Ready in 30 seconds!** See [HTTP Transport Quickstart](../docs/quickstart/http-transport.md) for detailed setup.

---

## 🌟 Key Features

| Feature                       | Description                                                         | Learn More                                                 |
| ----------------------------- | ------------------------------------------------------------------- | ---------------------------------------------------------- |
| **🖥️ Built-in Web UI**        | Visual configuration, live monitoring, request logs, and analytics  | Open `http://localhost:8080` after startup                 |
| **🔄 Multi-Provider Support** | OpenAI, Anthropic, Azure, Bedrock, Vertex, Cohere, Mistral, Ollama, Groq, SGLang | [Provider Setup](../docs/usage/providers.md)               |
| **🔌 Drop-in Compatibility**  | Replace OpenAI/Anthropic/GenAI APIs with zero code changes          | [Integrations](../docs/usage/http-transport/integrations/) |
| **🛠️ MCP Tool Calling**       | Enable AI models to use external tools (filesystem, web, databases) | [MCP Guide](../docs/mcp.md)                                |
| **⚡ Plugin System**          | Add analytics, caching, rate limiting, custom logic                 | [Plugin System](../docs/plugins.md)                        |
| **📊 Built-in Monitoring**    | Prometheus metrics at `/metrics` endpoint                           | [Monitoring](../docs/usage/http-transport/endpoints.md)    |
| **🔀 Automatic Fallbacks**    | Seamless failover between providers and models                      | [Fallback Config](../docs/usage/providers.md)              |

---

## 🎯 Usage Examples

### Basic Chat Completion

```bash
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "openai/gpt-4o-mini",
    "messages": [{"role": "user", "content": "Hello!"}]
  }'
```

### Drop-in OpenAI Replacement

```python
import openai

# Just change the base_url - everything else stays the same!
client = openai.OpenAI(
    base_url="http://localhost:8080/openai",
    api_key="dummy-key"  # Handled by Bifrost
)

response = client.chat.completions.create(
    model="gpt-4o-mini",
    messages=[{"role": "user", "content": "Hello from Bifrost!"}]
)
```

### Multi-Provider Fallbacks

```bash
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "openai/gpt-4o-mini",
    "messages": [{"role": "user", "content": "Hello!"}],
    "fallbacks": [
      {"provider": "anthropic", "model": "claude-3-5-sonnet-20241022"},
      {"provider": "bedrock", "model": "anthropic.claude-3-sonnet-20240229-v1:0"}
    ]
  }'
```

**More examples**: [HTTP Transport Endpoints](../docs/usage/http-transport/endpoints.md)

---

## ⚙️ Configuration

Bifrost supports **two configuration modes**:

### 1. 🚀 Dynamic Configuration (Recommended)

**No config file needed!** Start the container and configure via **Web UI** or **API**:

```bash
# Start with zero configuration
docker run -p 8080:8080 maximhq/bifrost

# Open web interface for easy configuration
# macOS: open http://localhost:8080
# Linux: xdg-open http://localhost:8080
# Windows: start http://localhost:8080
# Or simply open http://localhost:8080 manually in your browser
```

🖥️ **Web UI Features:**

- **Visual provider setup** - Add OpenAI, Anthropic, Bedrock, etc.
- **Real-time configuration** - Changes apply immediately
- **Live monitoring** - Request logs, metrics, and analytics
- **Export/Import** - Save configurations as JSON

📡 **Or configure via API:**

```bash
# Add providers programmatically
curl -X POST http://localhost:8080/providers \
  -H "Content-Type: application/json" \
  -d '{
    "provider": "openai",
    "keys": [{"value": "env.OPENAI_API_KEY", "models": ["gpt-4o-mini"], "weight": 1.0}],
    "network_config": {"default_request_timeout_in_seconds": 30},
    "concurrency_and_buffer_size": {"concurrency": 3, "buffer_size": 10}
  }'

# Save configuration to file for persistence
curl -X POST http://localhost:8080/config/save
```

**Benefits**: Perfect for containerized deployments, GitOps workflows, and both visual and API-first configuration management.

### 2. 📄 File-based Configuration

Traditional config file approach _(Volume mount needed when using docker)_:

```json
{
  "client": {
    "drop_excess_requests": false,
    "prometheus_labels": ["model", "provider"]
  },
  "providers": {
    "openai": {
      "keys": [
        {
          "value": "env.OPENAI_API_KEY",
          "models": ["gpt-4o-mini"],
          "weight": 1.0
        }
      ],
      "concurrency_and_buffer_size": {
        "concurrency": 3,
        "buffer_size": 10
      }
    }
  }
}
```

**Client Configuration Options:**

- `drop_excess_requests`: Whether to drop requests when queues are full (default: `false`)
- `initial_pool_size`: Initial connection pool size (default: `300`)
- `prometheus_labels`: Custom labels for Prometheus metrics

**Learn More:**

- [Provider Setup Guide](../docs/usage/http-transport/configuration/providers.md)
- [MCP Configuration](../docs/usage/http-transport/configuration/mcp.md)
- [Plugin Configuration](../docs/usage/http-transport/configuration/plugins.md)
- [Complete Examples](config.example.json)

---

## 🛠️ Advanced Features

### MCP Tool Integration

Enable AI models to use external tools like filesystem operations, web search, and databases:

```bash
# AI automatically uses configured tools
curl -X POST http://localhost:8080/v1/chat/completions \
  -d '{"model": "openai/gpt-4o-mini",
       "messages": [{"role": "user", "content": "List files in /tmp"}]}'
```

**Learn more**: [MCP Integration Guide](../docs/mcp.md)

### Plugin System

Add custom middleware for analytics, caching, rate limiting:

```bash
# Run with plugins
npx -y @maximhq/bifrost -plugins "maxim,redis"
```

**Available plugins**: [Plugin Repository](https://github.com/maximhq/bifrost/tree/main/plugins) | [Plugin Guide](../docs/plugins.md)

### Prometheus Monitoring

Built-in metrics collection at `/metrics`:

```bash
curl http://localhost:8080/metrics
```

**Custom labels**: Configure via `prometheus_labels` in config file or web UI

---

## 🔧 Runtime Configuration

### For Binary

| Flag       | Default   | Description                              |
| ---------- | --------- | ---------------------------------------- |
| `-app-dir` | .         | Application data directory (config+logs) |
| `-port`    | 8080      | HTTP server port                         |
| `-host`    | localhost | Host to bind server to                   |
| `-plugins` | -         | Comma-separated plugin list              |

### Understanding App Directory & Docker Volumes

> **📖 Detailed Guide:** See [Understanding App Directory & Docker Volumes](../docs/quickstart/http-transport.md#understanding-app-directory--docker-volumes) for complete details on data persistence and Docker deployment.

**Quick Reference:**

- Default app directory: Current working directory (`.`)
- Docker volume mount: `-v $(pwd)/data:/app/data`
- Key files: `config.json`, `logs/`

For complete setup instructions, deployment scenarios, and best practices, see the [detailed guide](../docs/quickstart/http-transport.md#understanding-app-directory--docker-volumes).

### For Docker

| Variable      | Default   | Description                    |
| ------------- | --------- | ------------------------------ |
| `APP_PORT`    | 8080      | Server port override           |
| `APP_HOST`    | 0.0.0.0   | Host to bind server to         |
| `APP_PLUGINS` | -         | Plugin list override           |

**Network Configuration Examples:**

```bash
# Listen on all interfaces (for container access)
docker run -p 8080:8080 -e APP_HOST=0.0.0.0 maximhq/bifrost

# IPv6 support - listen on all IPv6 interfaces
docker run -p 8080:8080 -e APP_HOST=:: maximhq/bifrost

# Specific interface binding
docker run -p 8080:8080 -e APP_HOST=192.168.1.100 maximhq/bifrost
```

---

## 📚 Documentation

### 🎯 Getting Started

- **[⚡ 30-Second Quickstart](../docs/quickstart/http-transport.md)** - Get running immediately
- **[🔧 Configuration Guide](../docs/usage/http-transport/configuration/)** - Providers, MCP, plugins
- **[🔄 Migration Guide](../docs/usage/http-transport/integrations/migration-guide.md)** - Migrate from existing providers

### 🚀 Core Features

- **[🔗 Multi-Provider Support](../docs/usage/providers.md)** - 10+ AI providers with fallbacks
- **[🛠️ MCP Integration](../docs/mcp.md)** - External tool calling for AI models
- **[🔌 Plugin System](../docs/plugins.md)** - Extensible middleware architecture

### 🌐 API Integrations

- **[🤖 OpenAI Compatible](../docs/usage/http-transport/integrations/openai-compatible.md)** - Drop-in OpenAI replacement
- **[🧠 Anthropic Compatible](../docs/usage/http-transport/integrations/anthropic-compatible.md)** - Drop-in Anthropic replacement
- **[🔍 GenAI Compatible](../docs/usage/http-transport/integrations/genai-compatible.md)** - Drop-in Google GenAI replacement

### 🏛️ Architecture & Performance

- **[📊 Benchmarks](../docs/benchmarks.md)** - Performance metrics and optimization
- **[🏗️ Architecture](../docs/architecture/)** - System design and internals
- **[💡 Examples](../docs/examples/)** - Real-world usage patterns

---

## 🎉 Ready to Scale?

🚀 **Production Deployment**: [Production Guide](../docs/usage/http-transport/configuration/)
📈 **Performance Tuning**: [Benchmarks & Optimization](../docs/benchmarks.md)
🔍 **Troubleshooting**: [Common Issues](../docs/usage/errors.md)

---

_Built with ❤️ by [Maxim](https://github.com/maximhq)_
