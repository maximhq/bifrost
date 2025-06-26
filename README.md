# Bifrost

[![Go Report Card](https://goreportcard.com/badge/github.com/maximhq/bifrost/core)](https://goreportcard.com/report/github.com/maximhq/bifrost/core)

Bifrost is an open-source middleware that serves as a unified gateway to various AI model providers, enabling seamless integration and fallback mechanisms for your AI-powered applications.

## ⚡ Quickstart

### Prerequisites

- Go 1.23 or higher (not needed if using Docker)
- Access to at least one AI model provider (OpenAI, Anthropic, etc.)
- API keys for the providers you wish to use

### A. Using Bifrost as an HTTP Server

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
   export ANTHROPIC_API_KEY=your_anthropic_api_key
   ```

   Note: Ensure you add all variables stated in your `config.json` file.

3. **Start the Bifrost HTTP Server**:

   You can run the server using either a Go Binary or Docker (if Go is not installed).

   #### i) Using Go Binary

   - Install the transport package:

     ```bash
     go install github.com/maximhq/bifrost/transports/bifrost-http@latest
     ```

   - Run the server (ensure Go is in your PATH):

     ```bash
     bifrost-http -config config.json -port 8080 -pool-size 300
     ```

   #### ii) OR Using Docker

   - Pull the Docker image:

     ```bash
     docker pull maximhq/bifrost
     ```

   - Run the Docker container:

     ```bash
     docker run -p 8080:8080 \
       -v $(pwd)/config.json:/app/config/config.json \
       -e OPENAI_API_KEY \
       -e ANTHROPIC_API_KEY \
       maximhq/bifrost
     ```

     Note: Ensure you mount your config file and add all environment variables referenced in your `config.json` file.

4. **Using the API**: Once the server is running, you can send requests to the HTTP endpoints.

   ```bash
   curl -X POST http://localhost:8080/v1/chat/completions \
   -H "Content-Type: application/json" \
   -d '{
     "provider": "openai",
     "model": "gpt-4o-mini",
     "messages": [
       {"role": "system", "content": "You are a helpful assistant."},
       {"role": "user", "content": "Tell me about Bifrost in Norse mythology."}
     ]
   }'
   ```

For additional HTTP server configuration options, read [this](https://github.com/maximhq/bifrost/blob/main/transports/README.md).

### B. Using Bifrost as a Go Package

1. **Implement Your Account Interface**: First, create an account that follows [Bifrost's account interface](https://github.com/maximhq/bifrost/blob/main/core/schemas/account.go).

   ```golang
   type BaseAccount struct{}

   func (baseAccount *BaseAccount) GetConfiguredProviders() ([]schemas.ModelProvider, error) {
     return []schemas.ModelProvider{schemas.OpenAI}, nil
   }

   func (baseAccount *BaseAccount) GetKeysForProvider(providerKey schemas.ModelProvider) ([]schemas.Key, error) {
       return []schemas.Key{
         {
           Value:  os.Getenv("OPENAI_API_KEY"),
           Models: []string{"gpt-4o-mini"},
           Weight: 1.0,
         },
       }, nil
   }

   func (baseAccount *BaseAccount) GetConfigForProvider(providerKey schemas.ModelProvider) (*schemas.ProviderConfig, error) {
       return &schemas.ProviderConfig{
          NetworkConfig:            schemas.DefaultNetworkConfig,
          ConcurrencyAndBufferSize: schemas.DefaultConcurrencyAndBufferSize,
       }, nil
   }
   ```

   Bifrost uses these methods to get all the keys and configurations it needs to call the providers. See the [Additional Configurations](#additional-configurations) section for additional customization options.

2. **Initialize Bifrost**: Set up the Bifrost instance by providing your account implementation.

   ```golang
   account := BaseAccount{}

   client, err := bifrost.Init(schemas.BifrostConfig{
     Account: &account,
   })
   ```

3. **Use Bifrost**: Make your First LLM Call!

   ```golang
     bifrostResult, bifrostErr := bifrost.ChatCompletionRequest(
      context.Background(),
      &schemas.BifrostRequest{
         Provider: schemas.OpenAI,
         Model: "gpt-4o-mini", // make sure you have configured gpt-4o-mini in your account interface
         Input: schemas.RequestInput{
           ChatCompletionInput: bifrost.Ptr([]schemas.BifrostMessage{{
            Role: schemas.ModelChatMessageRoleUser,
            Content: schemas.MessageContent{
              ContentStr: bifrost.Ptr("What is a LLM gateway?"),
            },
           }}),
         },
       },
     )
   ```

   You can add model parameters by including `Params: &schemas.ModelParameters{...yourParams}` in ChatCompletionRequest.

## 📑 Table of Contents

- [Bifrost](#bifrost)
  - [⚡ Quickstart](#-quickstart)
    - [Prerequisites](#prerequisites)
    - [A. Using Bifrost as an HTTP Server](#a-using-bifrost-as-an-http-server)
      - [i) Using Go Binary](#i-using-go-binary)
      - [ii) OR Using Docker](#ii-or-using-docker)
    - [B. Using Bifrost as a Go Package](#b-using-bifrost-as-a-go-package)
  - [📑 Table of Contents](#-table-of-contents)
  - [🔍 Overview](#-overview)
  - [✨ Features](#-features)
  - [🏗️ Repository Structure](#️-repository-structure)
  - [🚀 Getting Started](#-getting-started)
    - [Package Structure](#package-structure)
    - [Additional Configurations](#additional-configurations)
  - [📊 Benchmarks](#-benchmarks)
    - [Test Environment](#test-environment)
      - [1. t3.medium(2 vCPUs, 4GB RAM)](#1-t3medium2-vcpus-4gb-ram)
      - [2. t3.xlarge(4 vCPUs, 16GB RAM)](#2-t3xlarge4-vcpus-16gb-ram)
    - [Performance Metrics](#performance-metrics)
    - [Key Performance Highlights](#key-performance-highlights)
  - [🤝 Contributing](#-contributing)
  - [📄 License](#-license)

---

## 🔍 Overview

Bifrost acts as a bridge between your applications and multiple AI providers (OpenAI, Anthropic, Amazon Bedrock, Mistral, Ollama, etc.). It provides a consistent API while handling:

- Authentication and key management
- Request routing and load balancing
- Fallback mechanisms for reliability
- Unified request and response formatting
- Connection pooling and concurrency control

With Bifrost, you can focus on building your AI-powered applications without worrying about the underlying provider-specific implementations. It handles all the complexities of key and provider management, providing a fixed input and output format so you don't need to modify your codebase for different providers.

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
│   ├── bifrost-http/             # HTTP transport implementation
│   └── ...
│
└── plugins/              # Plugin Implementations
    ├── maxim/
    └── ...
```

The system uses a provider-agnostic approach with well-defined interfaces to easily extend to new AI providers. All interfaces are defined in `core/schemas/` and can be used as a reference for contributions.

---

## 🚀 Getting Started

If you want to **set up the Bifrost API quickly**, [check the transports documentation](https://github.com/maximhq/bifrost/tree/main/transports/README.md).

### Package Structure

Bifrost is divided into three Go packages: core, plugins, and transports.

1. **core**: This package contains the core implementation of Bifrost as a Go package.
2. **plugins**: This package serves as an extension to core. Plugins support both traditional instantiation and dynamic loading via configuration files.

   **Traditional Plugin Usage:**

   ```golang
   // go get github.com/maximhq/bifrost/plugins/maxim
   maximPlugin, err := maxim.NewMaximLoggerPlugin(os.Getenv("MAXIM_API_KEY"), os.Getenv("MAXIM_LOGGER_ID"))
   if err != nil {
     return nil, err
   }

   // Initialize Bifrost
   client, err := bifrost.Init(schemas.BifrostConfig{
     Account: &account,
     Plugins: []schemas.Plugin{maximPlugin},
   })
   ```

   **Dynamic Plugin Loading:**
   All plugins must implement an `Init(config json.RawMessage) (schemas.Plugin, error)` function for dynamic loading via configuration files. See [Plugin Documentation](https://github.com/maximhq/bifrost/blob/main/docs/plugins.md) for details.

3. **transports**: This package contains transport clients like HTTP to expose your Bifrost client. You can either `go get` this package or directly use the independent Dockerfile to quickly spin up your [Bifrost API](https://github.com/maximhq/bifrost/tree/main/transports/README.md) (read more on this).

### Additional Configurations

- [Memory Management](https://github.com/maximhq/bifrost/blob/main/docs/memory-management.md)
- [Logger](https://github.com/maximhq/bifrost/blob/main/docs/logger.md)
- [Plugins](https://github.com/maximhq/bifrost/blob/main/docs/plugins.md)
- [Provider Configurations](https://github.com/maximhq/bifrost/blob/main/docs/providers.md)
- [Fallbacks](https://github.com/maximhq/bifrost/blob/main/docs/fallbacks.md)
- [MCP Integration](https://github.com/maximhq/bifrost/blob/main/docs/mcp.md)

---

## 📊 Benchmarks

Bifrost has been tested under high load conditions to ensure optimal performance. The following results were obtained from benchmark tests running at 5000 requests per second (RPS) on different AWS EC2 instances.

### Test Environment

#### 1. t3.medium(2 vCPUs, 4GB RAM)

- Buffer Size: 15,000
- Initial Pool Size: 10,000

#### 2. t3.xlarge(4 vCPUs, 16GB RAM)

- Buffer Size: 20,000
- Initial Pool Size: 15,000

### Performance Metrics

| Metric                    | t3.medium     | t3.xlarge      |
| ------------------------- | ------------- | -------------- |
| Success Rate              | 100.00%       | 100.00%        |
| Average Request Size      | 0.13 KB       | 0.13 KB        |
| **Average Response Size** | **`1.37 KB`** | **`10.32 KB`** |
| Average Latency           | 2.12s         | 1.61s          |
| Peak Memory Usage         | 1312.79 MB    | 3340.44 MB     |
| Queue Wait Time           | 47.13 µs      | 1.67 µs        |
| Key Selection Time        | 16 ns         | 10 ns          |
| Message Formatting        | 2.19 µs       | 2.11 µs        |
| Params Preparation        | 436 ns        | 417 ns         |
| Request Body Preparation  | 2.65 µs       | 2.36 µs        |
| JSON Marshaling           | 63.47 µs      | 26.80 µs       |
| Request Setup             | 6.59 µs       | 7.17 µs        |
| HTTP Request              | 1.56s         | 1.50s          |
| Error Handling            | 189 ns        | 162 ns         |
| Response Parsing          | 11.30 ms      | 2.11 ms        |
| **Bifrost's Overhead**    | **`59 µs\*`** | **`11 µs\*`**  |

_\*Bifrost's overhead is measured at 59 µs on t3.medium and 11 µs on t3.xlarge, excluding the time taken for JSON marshalling and the HTTP call to the LLM, both of which are required in any custom implementation._

**Note**: On the t3.xlarge, we tested with significantly larger response payloads (~10 KB average vs ~1 KB on t3.medium). Even so, response parsing time dropped dramatically thanks to better CPU throughput and Bifrost's optimized memory reuse.

### Key Performance Highlights

- **Perfect Success Rate**: 100% request success rate under high load on both instances
- **Total Overhead**: Less than only _15µs added per request_ on average
- **Efficient Queue Management**: Minimal queue wait time (1.67 µs on t3.xlarge)
- **Fast Key Selection**: Near-instantaneous key selection (10 ns on t3.xlarge)
- **Improved Performance on t3.xlarge**:
  - 24% faster average latency
  - 81% faster response parsing
  - 58% faster JSON marshaling
  - Significantly reduced queue wait times

One of Bifrost's key strengths is its flexibility in configuration. You can freely decide the tradeoff between memory usage and processing speed by adjusting Bifrost's configurations. This flexibility allows you to optimize Bifrost for your specific use case, whether you prioritize speed, memory efficiency, or a balance between the two.

- Higher buffer and pool sizes (like in t3.xlarge) improve speed but use more memory
- Lower configurations (like in t3.medium) use less memory but may have slightly higher latencies
- You can fine-tune these parameters based on your specific needs and available resources

  - Initial Pool Size: Determines the initial allocation of resources
  - Buffer and Concurrency Settings: Controls the queue size and maximum number of concurrent requests (adjustable per provider).
  - Retry and Timeout Configurations: Customizable based on your requirements for each provider.

Curious? Run your own benchmarks. The [Bifrost Benchmarking](https://github.com/maximhq/bifrost-benchmarking) repo has everything you need to test it in your own environment.

**🏛️ Curious how we handle scales of 10k+ RPS?** Check out our [System Architecture Documentation](./docs/system-architecture.md) for detailed insights into Bifrost's high-performance design, memory management, and scaling strategies.

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
