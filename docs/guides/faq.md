# ❓ Frequently Asked Questions

Answers to common questions about Bifrost setup, usage, and best practices.

## 📑 Table of Contents

- [🚀 Getting Started](#-getting-started)
- [⚙️ Configuration](#️-configuration)
- [🔗 Providers & Models](#-providers--models)
- [⚡ Performance](#-performance)
- [🔄 Fallbacks](#-fallbacks)
- [🛠️ MCP & Tools](#️-mcp--tools)
- [🔧 Troubleshooting](#-troubleshooting)

---

## 🚀 Getting Started

### What is Bifrost?

**Bifrost is a unified AI model gateway** that provides a single interface to multiple AI providers (OpenAI, Anthropic, Bedrock, etc.). It offers features like automatic fallbacks, load balancing, key rotation, and tool integration.

### Which approach should I choose: Go Package or HTTP Transport?

| Use Case                  | Recommended Approach    | Why                           |
| ------------------------- | ----------------------- | ----------------------------- |
| **Go applications**       | 🔧 Go Package           | Best performance, type safety |
| **Non-Go applications**   | 🌐 HTTP Transport       | Language-agnostic REST API    |
| **Microservices**         | 🌐 HTTP Transport       | Central AI gateway            |
| **Existing integrations** | 🔄 Drop-in replacements | Zero code changes             |

### How do I get started quickly?

**30-second setup with HTTP Transport:**

```bash
# Pull and run with Docker
docker pull maximhq/bifrost
echo '{"providers":{"openai":{"keys":[{"value":"env.OPENAI_API_KEY","models":["gpt-4o-mini"],"weight":1.0}]}}}' > config.json
export OPENAI_API_KEY="your-key"
# 📖 [Docker setup examples →](../usage/examples.md#basic-single-provider)
```

**2-minute setup with Go Package:**

```go
go get github.com/maximhq/bifrost/core
// Implement Account interface and you're ready!
```

---

## ⚙️ Configuration

### How do I configure multiple providers?

<details>
<summary><strong>Multiple Provider Setup</strong></summary>

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
    },
    "anthropic": {
      "keys": [
        {
          "value": "env.ANTHROPIC_API_KEY",
          "models": ["claude-3-sonnet-20240229"],
          "weight": 1.0
        }
      ]
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
      }
    }
  }
}
```

</details>

### Can I use environment variables in configuration?

**Yes!** Prefix any value with `env.` to reference an environment variable:

```json
{
  "providers": {
    "openai": {
      "keys": [{ "value": "env.OPENAI_API_KEY" }],
      "network_config": {
        "base_url": "env.OPENAI_BASE_URL"
      }
    }
  }
}
```

### How do I set up multiple API keys for load balancing?

```json
{
  "providers": {
    "openai": {
      "keys": [
        { "value": "env.OPENAI_API_KEY_1", "weight": 0.6 },
        { "value": "env.OPENAI_API_KEY_2", "weight": 0.3 },
        { "value": "env.OPENAI_API_KEY_3", "weight": 0.1 }
      ]
    }
  }
}
```

The weight determines the traffic distribution (60%, 30%, 10% in this example).

---

## 🔗 Providers & Models

### Which providers does Bifrost support?

| Provider          | Status  | Models                         | Notes                    |
| ----------------- | ------- | ------------------------------ | ------------------------ |
| **OpenAI**        | ✅ Full | GPT-4o, GPT-4o-mini, etc.      | Complete support         |
| **Anthropic**     | ✅ Full | Claude 3.5 Sonnet, Haiku, Opus | Complete support         |
| **Azure OpenAI**  | ✅ Full | All Azure OpenAI models        | Requires endpoint config |
| **AWS Bedrock**   | ✅ Full | Claude, Mistral, Llama         | Requires AWS credentials |
| **Google Vertex** | ✅ Full | Gemini models                  | Requires GCP setup       |
| **Cohere**        | ✅ Full | Command models                 | Chat completion only     |
| **Mistral AI**    | ✅ Full | Mistral models, Pixtral        | Chat and vision          |
| **Ollama**        | ✅ Full | Local models                   | Requires local setup     |

### Can I use custom endpoints?

**Yes!** Many providers support custom base URLs:

```json
{
  "providers": {
    "openai": {
      "network_config": {
        "base_url": "https://custom-proxy.company.com/openai"
      }
    },
    "ollama": {
      "network_config": {
        "base_url": "http://localhost:11434"
      }
    }
  }
}
```

### How do I know which models are available?

Check the provider's documentation:

- **OpenAI**: [Platform docs](https://platform.openai.com/docs/models)
- **Anthropic**: [API docs](https://docs.anthropic.com/claude/docs/models-overview)
- **Azure**: Your deployment names
- **Bedrock**: [Model IDs](https://docs.aws.amazon.com/bedrock/latest/userguide/model-ids-arns.html)

---

## ⚡ Performance

### How fast is Bifrost?

**Benchmarks show minimal overhead:**

| Instance Type | Success Rate | Avg Latency | Bifrost Overhead |
| ------------- | ------------ | ----------- | ---------------- |
| t3.medium     | 100.00%      | 2.12s       | **59 µs**        |
| t3.xlarge     | 100.00%      | 1.61s       | **11 µs**        |

Most latency comes from the AI provider, not Bifrost.

### How do I optimize for high throughput?

<details>
<summary><strong>High-Throughput Configuration</strong></summary>

```go
// Go Package - High performance
client, err := bifrost.Init(schemas.BifrostConfig{
    Account:            account,
    InitialPoolSize:    20000,  // Large object pool
    DropExcessRequests: true,   // Fail-fast under load
})

// Provider configuration
schemas.ProviderConfig{
    ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
        Concurrency: 100,    // High concurrency
        BufferSize:  1000,   // Large buffer
    },
}
```

**For 10k+ RPS:**

- Instance: c5.2xlarge or larger
- Pool Size: 20,000+
- Concurrency: 50-100 per provider
- Buffer Size: 1000+ per provider

</details>

### How much memory does Bifrost use?

Memory usage depends on configuration:

| Pool Size | Expected Memory |
| --------- | --------------- |
| 100       | ~2 MB           |
| 1,000     | ~20 MB          |
| 10,000    | ~200 MB         |

Plus provider-specific buffers and connection pools.

---

## 🔄 Fallbacks

### How do fallbacks work?

Fallbacks automatically try alternative providers/models when the primary fails:

```json
{
  "provider": "openai",
  "model": "gpt-4o",
  "messages": [...],
  "fallbacks": [
    {"provider": "anthropic", "model": "claude-3-sonnet-20240229"},
    {"provider": "bedrock", "model": "anthropic.claude-3-sonnet-20240229-v1:0"}
  ]
}
```

If OpenAI fails → try Anthropic → try Bedrock.

### When do fallbacks trigger?

**Automatic fallbacks trigger on:**

- ✅ Rate limiting (429 errors)
- ✅ Server errors (5xx)
- ✅ Network timeouts
- ✅ Provider unavailability

**Fallbacks DON'T trigger on:**

- ❌ Authentication errors (4xx)
- ❌ Invalid requests (400)
- ❌ Quota exhaustion (in some cases)

### Can I control fallback behavior?

**Yes!** Use different strategies:

```go
// Go Package - Conditional fallbacks
request := &schemas.BifrostRequest{
    Provider: schemas.OpenAI,
    Model:    "gpt-4o",
    Fallbacks: []schemas.Fallback{
        {
            Provider: schemas.Anthropic,
            Model:    "claude-3-sonnet-20240229",
            Condition: "rate_limit_only", // Custom logic
        },
    },
}
```

---

## 🛠️ MCP & Tools

### What is MCP integration?

**Model Context Protocol (MCP)** allows AI models to use external tools:

- 🗂️ **File operations** (read, write, list files)
- 🌐 **Web search** (search engines, APIs)
- 📊 **Data access** (databases, APIs)
- 🧮 **Calculations** (math, statistics)

### How do I set up MCP tools?

<details>
<summary><strong>MCP Configuration</strong></summary>

```json
{
  "mcp": {
    "client_configs": [
      {
        "name": "filesystem",
        "connection_type": "stdio",
        "stdio_config": {
          "command": "npx",
          "args": ["-y", "@modelcontextprotocol/server-filesystem", "/tmp"]
        }
      },
      {
        "name": "web-search",
        "connection_type": "http",
        "connection_string": "http://localhost:3001/mcp"
      }
    ]
  }
}
```

</details>

### Do tools work with all providers?

**Tool calling support by provider:**

| Provider      | Tool Support | Auto-Execution |
| ------------- | ------------ | -------------- |
| **OpenAI**    | ✅ Full      | ✅ Yes         |
| **Anthropic** | ✅ Full      | ✅ Yes         |
| **Azure**     | ✅ Full      | ✅ Yes         |
| **Bedrock**   | ✅ Full      | ✅ Yes         |
| **Vertex**    | ✅ Full      | ✅ Yes         |
| **Mistral**   | ✅ Full      | ✅ Yes         |
| **Ollama**    | ✅ Full      | ✅ Yes         |
| **Cohere**    | ✅ Full      | ❌ No          |

### Can I create custom tools?

**Yes!** Register custom tools in Go:

```go
// Register custom tool
err := client.RegisterMCPTool("get_weather", "Get current weather",
    func(args any) (string, error) {
        // Your tool implementation
        return "Sunny, 72°F", nil
    },
    toolSchema,
)
```

---

## 🔧 Troubleshooting

### Why am I getting "account is required" error?

You need to implement the `Account` interface:

```go
// 📖 [Complete Account Implementation →](../usage/examples.md#basic-account-implementation)


```

### Why are my requests slow?

**Common causes and solutions:**

| Issue               | Solution                                  |
| ------------------- | ----------------------------------------- |
| **Low concurrency** | Increase `Concurrency` in provider config |
| **Small buffers**   | Increase `BufferSize`                     |
| **Network issues**  | Check connectivity, add proxy if needed   |
| **Rate limiting**   | Add more API keys, configure fallbacks    |
| **Memory pressure** | Increase `InitialPoolSize`                |

### How do I enable debug logging?

```go
// Go Package
client, err := bifrost.Init(schemas.BifrostConfig{
    Account: account,
    Logger:  bifrost.NewDefaultLogger(schemas.LogLevelDebug),
})
```

### Why isn't my configuration loading?

**Check these common issues:**

1. **JSON syntax**: Validate with `jq . config.json`
2. **Environment variables**: Ensure they're set and accessible
3. **File permissions**: Check file is readable
4. **Path**: Verify config file path is correct

### Can I use Bifrost in production?

**Absolutely!** Bifrost is designed for production use:

- ✅ **Battle-tested**: Handles 10k+ RPS in production
- ✅ **Reliable**: Automatic fallbacks and retries
- ✅ **Secure**: No data storage, pass-through only
- ✅ **Observable**: Built-in metrics and logging
- ✅ **Scalable**: Horizontal scaling support

---

## 💡 Best Practices

### Security

- 🔒 Use environment variables for API keys
- 🔒 Rotate API keys regularly
- 🔒 Use HTTPS for all communications
- 🔒 Implement proper access controls

### Performance

- ⚡ Configure appropriate pool sizes
- ⚡ Use multiple API keys for load distribution
- ⚡ Set up fallback providers
- ⚡ Monitor memory usage and tune accordingly

### Reliability

- 🛡️ Configure retry policies
- 🛡️ Set appropriate timeouts
- 🛡️ Use fallback providers
- 🛡️ Monitor provider health

---

**Still have questions?** Check our [🔧 Troubleshooting Guide](troubleshooting.md) or [create an issue](https://github.com/maximhq/bifrost/issues) on GitHub.
