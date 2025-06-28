# Bifrost Documentation

Welcome to Bifrost - the unified AI model gateway that provides seamless integration with multiple AI providers through a single API.

## 🚀 Quick Start

Choose your preferred way to use Bifrost:

| Usage Mode                 | Best For                            | Setup Time | Documentation                                            |
| -------------------------- | ----------------------------------- | ---------- | -------------------------------------------------------- |
| **🔧 Go Package**          | Direct integration, maximum control | 2 minutes  | [📖 Go Package Guide](quick-start/go-package.md)         |
| **🌐 HTTP Transport**      | Language-agnostic, microservices    | 30 seconds | [📖 HTTP Transport Guide](quick-start/http-transport.md) |
| **🔄 Drop-in Replacement** | Existing apps, zero code changes    | 1 minute   | [📖 Integration Guide](quick-start/integrations.md)      |

**New to Bifrost?** Start with the [📊 Feature Comparison](quick-start/feature-comparison.md) to understand which approach fits your needs.

---

## 📚 Documentation Sections

### 🎯 Core Features

Essential Bifrost capabilities with examples for both Go package and HTTP transport usage.

- **[🔗 Providers](features/providers/)** - Multi-provider AI model support (OpenAI, Anthropic, Bedrock, etc.)
- **[🔄 Fallbacks](features/fallbacks.md)** - Automatic failover between providers and models
- **[🛠️ MCP Integration](features/mcp-integration.md)** - Model Context Protocol for external tools
- **[🔌 Plugins](features/plugins.md)** - Extensible middleware system
- **[🔑 Key Management](features/key-management.md)** - API key rotation and weighted distribution
- **[📝 Logging](features/logging.md)** - Configurable logging system
- **[⚡ Memory Management](features/memory-management.md)** - Performance optimization
- **[🌐 Networking](features/networking.md)** - Proxies, timeouts, and retries
- **[📊 Observability](features/observability.md)** - Metrics, monitoring, and debugging

### 🔄 Drop-in API Replacements

Replace existing provider APIs without changing your application code.

- **[🎯 Overview](features/integrations/)** - How drop-in replacements work
- **[🤖 OpenAI Compatible](features/integrations/openai-compatible.md)** - `/openai/chat/completions`
- **[🧠 Anthropic Compatible](features/integrations/anthropic-compatible.md)** - `/anthropic/v1/messages`
- **[🔍 Google GenAI Compatible](features/integrations/genai-compatible.md)** - `/genai/v1beta/models/{model}`
- **[⚡ LiteLLM Compatible](features/integrations/litellm-compatible.md)** - `/litellm/chat/completions`
- **[📱 Migration Guide](features/integrations/migration-guide.md)** - Step-by-step app migration

### ⚙️ Configuration & Deployment

Production-ready setup and configuration guides.

- **[🔧 Core Configuration](configuration/core-config.md)** - Go package configuration
- **[🌐 HTTP Configuration](configuration/http-config.md)** - HTTP transport configuration
- **[🌍 Environment Variables](configuration/environment-variables.md)** - All environment variables
- **[🔒 Security](configuration/security.md)** - Security best practices
- **[🚀 Deployment](configuration/deployment/)** - Docker, Kubernetes, production setup

### 🏗️ Architecture & Performance

Deep dive into Bifrost's design and performance characteristics.

- **[🏛️ System Overview](architecture/system-overview.md)** - High-level architecture
- **[🔄 Request Flow](architecture/request-flow.md)** - Request processing pipeline
- **[📊 Benchmarks](benchmarks.md)** - Performance metrics and test results
- **[⚡ Concurrency](architecture/concurrency.md)** - Worker pools and threading
- **[💡 Design Decisions](architecture/design-decisions.md)** - Why we built it this way

### 📖 API Reference

Comprehensive API documentation for both usage modes.

- **[🔧 Go Package API](usage/go-package/)** - Complete Go package reference
- **[🌐 HTTP API](usage/http-api/)** - REST endpoints and examples
- **[📋 OpenAPI Spec](usage/http-api/openapi.json)** - Machine-readable specification

### 📚 Guides & Tutorials

Step-by-step guides for common use cases and migration scenarios.

- **[🎓 Tutorials](guides/tutorials/)** - Build your first chatbot, add fallbacks, etc.
- **[🔄 Migration](guides/migration/)** - Migrate from OpenAI, Anthropic, LiteLLM
- **[❓ Troubleshooting](guides/troubleshooting.md)** - Common issues and solutions
- **[❓ FAQ](guides/faq.md)** - Frequently asked questions

### 🤝 Contributing

Help improve Bifrost for everyone.

- **[🚀 Getting Started](contributing/development.md)** - Development setup
- **[🧪 Testing](contributing/testing.md)** - Testing guidelines
- **[📝 Documentation](contributing/documentation.md)** - Documentation standards
- **[💻 Code Style](contributing/code-style.md)** - Code style guide

---

## 🔍 What's New

- **🔄 Drop-in API Replacements** - Replace OpenAI/Anthropic endpoints without code changes
- **🛠️ Enhanced MCP Integration** - Comprehensive Model Context Protocol support
- **📊 Performance Benchmarks** - Detailed performance metrics and optimization guides
- **🚀 Production Deployment** - Complete Docker, Kubernetes, and scaling guides

---

## 💡 Quick Links

| I Want To...                      | Go Here                                                                |
| --------------------------------- | ---------------------------------------------------------------------- |
| **Get started in 30 seconds**     | [⚡ Quick Start](quick-start/)                                         |
| **Replace my OpenAI integration** | [🤖 OpenAI Compatible API](features/integrations/openai-compatible.md) |
| **Add fallback providers**        | [🔄 Fallbacks Guide](features/fallbacks.md)                            |
| **Deploy to production**          | [🚀 Production Deployment](configuration/deployment/production.md)     |
| **Understand the architecture**   | [🏛️ System Overview](architecture/system-overview.md)                  |
| **See performance benchmarks**    | [📊 Benchmarks](benchmarks.md)                                         |
| **Migrate from another gateway**  | [🔄 Migration Guides](guides/migration/)                               |
| **Contribute to the project**     | [🤝 Contributing Guide](contributing/)                                 |

---

**Need help?** Check our [❓ FAQ](guides/faq.md) or [🔧 Troubleshooting Guide](guides/troubleshooting.md).

Built with ❤️ by [Maxim](https://github.com/maximhq)
