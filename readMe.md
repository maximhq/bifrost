# Bifrost

![Bifrost Logo](https://via.placeholder.com/150x150.png?text=Bifrost)

## 🌈 The Bridge Between Your Application and AI Providers

Bifrost is an open-source middleware that serves as a unified gateway to various AI model providers, enabling seamless integration and fallback mechanisms for your AI-powered applications.

---

## 📋 Table of Contents

- [Overview](#overview)
- [Features](#features)
- [Architecture](#architecture)
- [Getting Started](#getting-started)
  - [Prerequisites](#prerequisites)
  - [Installation](#installation)
  - [Docker Setup](#docker-setup)
- [Configuration](#configuration)
- [Usage](#usage)
  - [HTTP Transport](#http-transport)
  - [Text Completions](#text-completions)
  - [Chat Completions](#chat-completions)
- [Advanced Features](#advanced-features)
  - [Fallbacks](#fallbacks)
  - [Key Management](#key-management)
  - [Concurrency Control](#concurrency-control)
- [API Reference](#api-reference)
- [Contributing](#contributing)
- [License](#license)

---

## 🔍 Overview

Bifrost acts as a bridge between your applications and multiple AI providers (OpenAI, Anthropic, Amazon Bedrock, etc.). It provides a consistent API interface while handling:

- Authentication & key management
- Request routing & load balancing
- Fallback mechanisms for reliability
- Unified response formatting
- Connection pooling & concurrency control

With Bifrost, you can focus on building your AI-powered applications without worrying about the underlying provider-specific implementations.

---

## ✨ Features

- **Multi-Provider Support**: Integrate with OpenAI, Anthropic, Amazon Bedrock, and more through a single API
- **Fallback Mechanisms**: Automatically retry failed requests with alternative models or providers
- **Dynamic Key Management**: Rotate and manage API keys efficiently
- **Connection Pooling**: Optimize network resources for better performance
- **Concurrency Control**: Manage rate limits and parallel requests effectively
- **HTTP Transport**: RESTful API interface for easy integration
- **Custom Configuration**: Flexible JSON-based configuration

---

## 🏗️ Architecture

Bifrost is built with a modular architecture:

```
bifrost/
├── core/         # Core functionality and shared components
├── transports/   # Interface layers (HTTP, gRPC, etc.)
│   ├── http/     # HTTP transport implementation
│   └── ...
└── providers/    # AI provider-specific implementations
    ├── openai/
    ├── anthropic/
    ├── bedrock/
    └── ...
```

The system uses a provider-agnostic approach with well-defined interfaces to easily extend to new AI providers.

---

## 🚀 Getting Started

### Prerequisites

- Go 1.18 or higher
- Access to at least one AI model provider (OpenAI, Anthropic, etc.)
- API keys for the providers you wish to use

### Installation

```bash
# Clone the repository
git clone https://github.com/maximhq/bifrost.git
cd bifrost

# Build the HTTP transport
cd transports
go build -o bifrost ./http
```

### Docker Setup

You can also run Bifrost using Docker:

```bash
docker build \
  --build-arg CONFIG_PATH=./config.example.json \
  --build-arg ENV_PATH=./.env \
  --build-arg PORT=8080 \
  --build-arg POOL_SIZE=300 \
  -t bifrost-transports ./transports

docker run -p 8080:8080 bifrost-transports
```

---

## ⚙️ Configuration

Bifrost uses a combination of a JSON configuration file and environment variables:

1. Create a configuration file based on the example:
   ```bash
   cp transports/config.example.json config.json
   ```

2. Set up your environment variables in a `.env` file:
   ```bash
   cp transports/.env.example .env
   ```

3. Edit both files to configure your providers and API keys.

Example configuration:

```json
{
  "openai": {
    "keys": [
      {
        "api_key": "${OPENAI_API_KEY}",
        "organization_id": "${OPENAI_ORG_ID}"
      }
    ],
    "network_config": {
      "timeout_ms": 30000,
      "max_retries": 3
    },
    "concurrency_and_buffer_size": {
      "max_concurrency": 10,
      "channel_buffer_size": 100
    }
  },
  "anthropic": {
    "keys": [
      {
        "api_key": "${ANTHROPIC_API_KEY}"
      }
    ]
  }
}
```

---

## 🧰 Usage

### HTTP Transport

Start the HTTP server:

```bash
./bifrost -config config.json -env .env -port 8080 -pool-size 300
```

### Text Completions

```bash
curl -X POST http://localhost:8080/v1/text/completions \
  -H "Content-Type: application/json" \
  -d '{
    "provider": "openai",
    "model": "gpt-4",
    "text": "Once upon a time in the land of AI,",
    "params": {
      "temperature": 0.7,
      "max_tokens": 100
    }
  }'
```

### Chat Completions

```bash
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "provider": "anthropic",
    "model": "claude-3-opus-20240229",
    "messages": [
      {"role": "system", "content": "You are a helpful assistant."},
      {"role": "user", "content": "Tell me about Bifrost in Norse mythology."}
    ],
    "params": {
      "temperature": 0.7,
      "max_tokens": 500
    }
  }'
```

---

## 🔧 Advanced Features

### Fallbacks

Configure fallback options in your requests:

```json
{
  "provider": "openai",
  "model": "gpt-4",
  "messages": [...],
  "fallbacks": [
    {
      "provider": "anthropic",
      "model": "claude-3-opus-20240229"
    },
    {
      "provider": "bedrock",
      "model": "anthropic.claude-3-sonnet-20240229-v1:0"
    }
  ]
}
```

### Key Management

Bifrost supports multiple API keys per provider for load balancing and redundancy.

### Concurrency Control

Fine-tune concurrency settings per provider to manage rate limits effectively.

---

## 📘 API Reference

### Endpoints

- `/v1/text/completions`: Text completion requests
- `/v1/chat/completions`: Chat completion requests

For detailed API documentation, see the [API Reference](docs/api-reference.md).

---

## 🤝 Contributing

Contributions are welcome! Please feel free to submit a Pull Request.

1. Fork the repository
2. Create your feature branch (`git checkout -b feature/amazing-feature`)
3. Commit your changes (`git commit -m 'Add some amazing feature'`)
4. Push to the branch (`git push origin feature/amazing-feature`)
5. Open a Pull Request

---

## 📄 License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.

---

Built with ❤️ by [Maxim](https://github.com/maximhq)
