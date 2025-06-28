# Bifrost

[![Go Report Card](https://goreportcard.com/badge/github.com/maximhq/bifrost/core)](https://goreportcard.com/report/github.com/maximhq/bifrost/core)

Bifrost is an open-source middleware that serves as a unified gateway to various AI model providers, enabling seamless integration and fallback mechanisms for your AI-powered applications.

![Bifrost](./docs/media/cover.png)

## ⚡ Quickstart (30 seconds)

### Prerequisites

- Go 1.23 or higher (not needed if using Docker)
- Access to at least one AI model provider (OpenAI, Anthropic, etc.)
- API keys for the providers you wish to use

### Using Bifrost HTTP Transport

> **📖 For detailed setup guides with multiple providers, advanced configuration, and language examples, see [Quick Start Documentation](./docs/quick-start/README.md)**

1. **Create `config.json`**: This file should contain your provider settings and API keys.

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
         ]
       }
     }
   }
   ```

2. **Set Up Your Environment**: Add your environment variable to the session.

   ```bash
   export OPENAI_API_KEY=your_openai_api_key
   ```

3. **Start the Bifrost HTTP Server**: You can run using Docker or Go binary.

   ```bash
   # Docker
   docker pull maximhq/bifrost
   docker run -p 8080:8080 \
     -v $(pwd)/config.json:/app/config/config.json \
     -e OPENAI_API_KEY \
     maximhq/bifrost

   # OR Go Binary
   go install github.com/maximhq/bifrost/transports/bifrost-http@latest
   bifrost-http -config config.json -port 8080
   ```

4. **Test the API**: Send a request to verify it's working.

   ```bash
   curl -X POST http://localhost:8080/v1/chat/completions \
   -H "Content-Type: application/json" \
   -d '{
     "provider": "openai",
     "model": "gpt-4o-mini",
     "messages": [
       {"role": "user", "content": "Tell me about Bifrost in Norse mythology."}
     ]
   }'
   ```

   **That's it!** You now have Bifrost running and can make requests to any configured provider.

   > **📖 Need more advanced setup?** See [HTTP Transport Guide](./docs/quick-start/http-transport.md) for multi-provider configuration, fallbacks, language examples, and production deployment options.

## 📑 Table of Contents

- [Bifrost](#bifrost)
  - [⚡ Quickstart (30 seconds)](#-quickstart-30-seconds)
    - [Prerequisites](#prerequisites)
    - [Using Bifrost HTTP Transport](#using-bifrost-http-transport)
  - [📑 Table of Contents](#-table-of-contents)
  - [✨ Features](#-features)
  - [🏗️ Repository Structure](#️-repository-structure)
  - [🚀 Getting Started](#-getting-started)
    - [1. As a Go Package (Core Integration)](#1-as-a-go-package-core-integration)
    - [2. As an HTTP API (Transport Layer)](#2-as-an-http-api-transport-layer)
    - [3. As a Drop-in Replacement (Zero Code Changes)](#3-as-a-drop-in-replacement-zero-code-changes)
  - [📊 Performance](#-performance)
    - [🔑 Key Performance Highlights](#-key-performance-highlights)
  - [📚 Documentation](#-documentation)
    - [🚀 **Quick Start Guides**](#-quick-start-guides)
    - [🎯 **Core Features**](#-core-features)
    - [⚙️ **Advanced Topics**](#️-advanced-topics)
    - [📱 **Migration \& Guides**](#-migration--guides)
  - [🤝 Contributing](#-contributing)
  - [📄 License](#-license)

---

## ✨ Features

- **Multi-Provider Support**: Integrate with OpenAI, Anthropic, Amazon Bedrock, Mistral, Ollama, and more through a single API
- **Fallback Mechanisms**: Automatically retry failed requests with alternative models or providers
- **Dynamic Key Management**: Rotate and manage API keys efficiently with weighted distribution
- **Connection Pooling**: Optimize network resources for better performance
- **Concurrency Control**: Manage rate limits and parallel requests effectively
- **Flexible Transports**: Multiple transports for easy integration into your infra
- **Plugin First Architecture**: No callback hell, simple addition/creation of custom plugins
- **MCP Integration**: Built-in Model Context Protocol (MCP) support for external tool integration and execution
- **Custom Configuration**: Offers granular control over pool sizes, network retry settings, fallback providers, and network proxy configurations
- **Built-in Observability**: Native Prometheus metrics out of the box, no wrappers, no sidecars, just drop it in and scrape
- **SDK Support**: Bifrost is available as a Go package, so you can use it directly in your own applications.
- **Seamless Integration with Generative AI SDKs**: Effortlessly transition to Bifrost by simply updating the `base_url` in your existing SDKs, such as OpenAI, Anthropic, GenAI, and more. Just one line of code is all it takes to make the switch.

---

## 🏗️ Repository Structure

Bifrost is built with a modular architecture:

```text
bifrost/
├── core/                 # Core functionality and shared components
│   ├── providers/        # Provider-specific implementations
│   ├── schemas/          # Interfaces and structs used in bifrost
│   ├── bifrost.go        # Main Bifrost implementation
│
├── docs/                 # Documentations for Bifrost's configurations and contribution guides
│   └── ...
│
├── tests/                # All test setups related to /core and /transports
│   └── ...
│
├── transports/           # Interface layers (HTTP, gRPC, etc.)
│   ├── bifrost-http/     # HTTP transport implementation
│   └── ...
│
└── plugins/              # Plugin Implementations
    ├── maxim/
    └── ...
```

The system uses a provider-agnostic approach with well-defined interfaces to easily extend to new AI providers. All interfaces are defined in `core/schemas/` and can be used as a reference for contributions.

---

## 🚀 Getting Started

There are three ways to use Bifrost - choose the one that fits your needs:

### 1. As a Go Package (Core Integration)

For direct integration into your Go applications. Provides maximum performance and control.

> **📖 [2-Minute Go Package Setup](./docs/quick-start/go-package.md)**

Quick example:

```bash
go get github.com/maximhq/bifrost/core
```

### 2. As an HTTP API (Transport Layer)

For language-agnostic integration and microservices architecture.

> **📖 [30-Second HTTP Transport Setup](./docs/quick-start/http-transport.md)**

Quick example:

```bash
docker pull maximhq/bifrost
docker run -p 8080:8080 \
  -v $(pwd)/config.json:/app/config/config.json \
  -e OPENAI_API_KEY \
  maximhq/bifrost
```

### 3. As a Drop-in Replacement (Zero Code Changes)

Replace existing OpenAI/Anthropic APIs without changing your application code.

> **📖 [1-Minute Drop-in Integration](./docs/quick-start/integrations.md)**

Quick example:

```diff
- base_url = "https://api.openai.com"
+ base_url = "http://localhost:8080/openai"
```

**🤔 Not sure which to choose?** Check our **[📊 Feature Comparison Guide](./docs/quick-start/feature-comparison.md)** to find the perfect approach for your use case.

---

## 📊 Performance

Bifrost sets a new bar for low-latency, high-throughput LLM routing. In our sustained 5,000 RPS benchmark (see full methodology in docs/benchmarks.md) the gateway added only **11 µs** of overhead per request – **less than 0.003 %** of the total end-to-end latency of a GPT-4o call.

| Metric                                | t3.medium | t3.xlarge   | Δ                  |
| ------------------------------------- | --------- | ----------- | ------------------ |
| Added latency (Bifrost overhead)      | 59 µs     | **11 µs**   | **-81 %**          |
| Success rate @ 5 k RPS                | 100 %     | 100 %       | No failed requests |
| Avg. queue wait time                  | 47 µs     | **1.67 µs** | **-96 %**          |
| Avg. request latency (incl. provider) | 2.12 s    | **1.61 s**  | **-24 %**          |

### 🔑 Key Performance Highlights

- **Perfect Success Rate** – 100 % request success rate on both instance types even at 5 k RPS.
- **Tiny Total Overhead** – < 15 µs additional latency per request on average.
- **Efficient Queue Management** – just **1.67 µs** average wait time on the t3.xlarge test.
- **Fast Key Selection** – ~**10 ns** to pick the right weighted API key.

Bifrost is deliberately configurable so you can dial the **speed ↔ memory** trade-off:

| Config Knob                   | Effect                                                           |
| ----------------------------- | ---------------------------------------------------------------- |
| `initial_pool_size`           | How many objects are pre-allocated. Higher = faster, more memory |
| `buffer_size` & `concurrency` | Queue depth and max parallel workers (can be set per provider)   |
| Retry / Timeout               | Tune aggressiveness for each provider to meet your SLOs          |

Choose higher settings (like the t3.xlarge profile above) for raw speed, or lower ones (t3.medium) for reduced memory footprint – or find the sweet spot for your workload.

> **Need more numbers?** Dive into the [full benchmark report](./docs/benchmarks.md) for breakdowns of every internal stage (JSON marshalling, HTTP call, parsing, etc.), hardware sizing guides and tuning tips.

---

## 📚 Documentation

Comprehensive documentation for all Bifrost features and use cases:

### 🚀 **Quick Start Guides**

- **[📖 Documentation Hub](./docs/README.md)** - Central navigation for all docs
- **[🔧 Go Package Setup](./docs/quick-start/go-package.md)** - 2-minute Go integration
- **[🌐 HTTP Transport Setup](./docs/quick-start/http-transport.md)** - 30-second service deployment
- **[🔄 Drop-in Integration](./docs/quick-start/integrations.md)** - Zero-code provider replacement
- **[📊 Feature Comparison](./docs/quick-start/feature-comparison.md)** - Choose the right approach

### 🎯 **Core Features**

- **[🔗 Multi-Provider Support](./docs/features/providers/)** - OpenAI, Anthropic, Bedrock, and more
- **[🔄 Fallback Systems](./docs/features/fallbacks.md)** - Automatic provider redundancy
- **[🛠️ MCP Integration](./docs/features/mcp-integration.md)** - External tool integration
- **[🔌 Plugin System](./docs/features/plugins.md)** - Extensible middleware
- **[📊 Observability](./docs/features/observability.md)** - Metrics and monitoring

### ⚙️ **Advanced Topics**

- **[🏗️ Architecture](./docs/architecture/system-overview.md)** - How Bifrost works
- **[📊 Performance](./docs/benchmarks.md)** - Benchmarks and optimization
- **[🚀 Production Deployment](./docs/configuration/deployment/)** - Scale for production
- **[🔧 API Reference](./docs/usage/)** - Complete API documentation

### 📱 **Migration & Guides**

- **[🔄 Migration Guides](./docs/guides/migration/)** - Move from OpenAI, Anthropic, LiteLLM
- **[🎓 Tutorials](./docs/guides/tutorials/)** - Step-by-step walkthroughs
- **[❓ FAQ & Troubleshooting](./docs/guides/faq.md)** - Common questions and issues

---

## 🤝 Contributing

We welcome contributions of all kinds—whether it's bug fixes, features, documentation improvements, or new ideas. Feel free to open an issue, and once it's assigned, submit a Pull Request.

Here's how to get started (after picking up an issue):

1. Fork the repository
2. Create your feature branch (`git checkout -b feature/amazing-feature`)
3. Commit your changes (`git commit -m 'Add some amazing feature'`)
4. Push to the branch (`git push origin feature/amazing-feature`)
5. Open a Pull Request and describe your changes

---

## 📄 License

This project is licensed under the Apache 2.0 License - see the [LICENSE](LICENSE) file for details.

Built with ❤️ by [Maxim](https://github.com/maximhq)
