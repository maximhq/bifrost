# 🔧 Go Package Usage

Complete guide to using Bifrost as a Go package in your applications. This section focuses on practical implementation patterns and code examples.

> **💡 New to Bifrost?** Start with the [📖 30-second setup guide](../../quickstart/go-package.md) to get running quickly.

## 📋 Quick Reference

### **Core Components**

| Component                                    | Purpose                                      | Time to Learn |
| -------------------------------------------- | -------------------------------------------- | ------------- |
| **[🏛️ Account Interface](./account.md)**     | Provider configuration and key management    | 5 min         |
| **[🤖 Bifrost Client](./bifrost-client.md)** | Main client methods and request handling     | 10 min        |
| **[🔌 Plugins](./plugins.md)**               | Custom middleware and request/response hooks | 15 min        |
| **[🛠️ MCP Integration](./mcp.md)**           | Tool calling and external integrations       | 15 min        |
| **[📊 Logging](./logging.md)**               | Custom logging and monitoring                | 5 min         |
| **[📋 Schemas](./schemas.md)**               | Data structures and interfaces reference     | 10 min        |

### **Usage Patterns**

<details>
<summary><strong>🚀 Basic Usage (Most Common)</strong></summary>

```go
import (
    bifrost "github.com/maximhq/bifrost/core"
    "github.com/maximhq/bifrost/core/schemas"
)

// Simple account implementation
type MyAccount struct{}
// ... implement Account interface

func main() {
    client, _ := bifrost.Init(schemas.BifrostConfig{
        Account: &MyAccount{},
    })
    defer client.Cleanup()

    response, err := client.ChatCompletionRequest(context.Background(), &schemas.BifrostRequest{
        Provider: schemas.OpenAI,
        Model:    "gpt-4o-mini",
        Input: schemas.RequestInput{
            ChatCompletionInput: &[]schemas.BifrostMessage{
                {Role: schemas.ModelChatMessageRoleUser, Content: schemas.MessageContent{ContentStr: &message}},
            },
        },
    })
}
```

</details>

<details>
<summary><strong>⚡ Multi-Provider with Fallbacks</strong></summary>

```go
response, err := client.ChatCompletionRequest(ctx, &schemas.BifrostRequest{
    Provider: schemas.OpenAI,
    Model:    "gpt-4o-mini",
    Input:    input, // your input here
    Fallbacks: []schemas.Fallback{
        {Provider: schemas.Anthropic, Model: "claude-3-sonnet-20240229"},
        {Provider: schemas.Vertex, Model: "gemini-pro"},
    },
})
```

</details>

<details>
<summary><strong>🛠️ Tool Calling</strong></summary>

```go
response, err := client.ChatCompletionRequest(ctx, &schemas.BifrostRequest{
    Provider: schemas.OpenAI,
    Model:    "gpt-4o-mini",
    Input:    input, // your input here
    Params: &schemas.ModelParameters{
        Tools: &[]schemas.Tool{weatherTool},
        ToolChoice: &schemas.ToolChoice{ToolChoiceStr: &auto},
    },
})
```

</details>

<details>
<summary><strong>🔌 With Custom Plugin</strong></summary>

```go
client, _ := bifrost.Init(schemas.BifrostConfig{
    Account: &MyAccount{},
    Plugins: []schemas.Plugin{&MyCustomPlugin{}},
})
```

</details>

---

## 🎯 Common Use Cases

### **"I want to..."**

| Goal                              | Start Here                            | Example Code                 |
| --------------------------------- | ------------------------------------- | ---------------------------- |
| **Add multiple AI providers**     | [Account Interface](./account.md)     | Multi-provider setup         |
| **Handle failover automatically** | [Bifrost Client](./bifrost-client.md) | Fallback configuration       |
| **Add custom logging/monitoring** | [Plugins](./plugins.md)               | Rate limiting, caching       |
| **Use external tools/APIs**       | [MCP Integration](./mcp.md)           | Database queries, web search |
| **Optimize for production**       | [Account Interface](./account.md)     | Connection pooling, keys     |
| **Debug requests/responses**      | [Logging](./logging.md)               | Custom logger setup          |
| **Build a chatbot with tools**    | [MCP Integration](./mcp.md)           | Tool registration            |
| **Understand error types**        | [Schemas](./schemas.md)               | BifrostError handling        |
| **Add rate limiting**             | [Plugins](./plugins.md)               | PreHook implementation       |
| **Cache responses**               | [Plugins](./plugins.md)               | PostHook response caching    |

---

## 🏗️ Architecture Overview

**Understanding the Flow:**

```
Your App → Account → Bifrost Client → Plugins → Provider → Response
```

- **[Account Interface](./account.md)**: Configuration provider (keys, settings, provider configs)
- **[Bifrost Client](./bifrost-client.md)**: Core request router with fallbacks and concurrency
- **[Plugins](./plugins.md)**: Request/response middleware (rate limiting, caching, monitoring)
- **[MCP Integration](./mcp.md)**: Tool calling and external service integration

> **🏛️ Deep Architecture:** For system internals, worker design, and performance details, see [Architecture Documentation](../../architecture/).

---

## 🌐 Language Integrations

**Using HTTP Transport Instead?**

If you need to use Bifrost from non-Go languages (Python, Node.js, etc.) or in microservices:

- **[🌐 HTTP Transport Setup](../../quickstart/http-transport.md)** - 30-second API setup
- **[📡 HTTP Transport Usage](../http-transport/)** - REST API documentation
- **[🔄 Drop-in Integration](../../quickstart/integrations.md)** - Replace OpenAI/Anthropic URLs

> **💡 Tip:** HTTP transport hosts the same Go package via REST API, so concepts like Account and Plugins are configured via JSON instead of Go code.

---

## 🔧 Advanced Configuration

### **Performance Tuning**

- [Memory Management](../memory-management.md) - Buffer sizes, concurrency settings
- [Networking](../networking.md) - Proxies, timeouts, connection pooling
- [Key Management](../key-management.md) - Load balancing, rotation

### **Production Setup**

- [Error Handling](../errors.md) - Error types and recovery patterns
- [Provider Configuration](../providers.md) - All 10+ providers setup

### **Development**

- [Logging](./logging.md) - Debug visibility
- [Schemas](./schemas.md) - Type definitions

---

## 📚 Next Steps

**Quick Start Path:**

1. **[⚡ 30-second setup](../../quickstart/go-package.md)** - Get running now
2. **[🏛️ Account setup](./account.md)** - Configure providers and keys
3. **[🤖 Client usage](./bifrost-client.md)** - Learn core methods
4. **[🔌 Add plugins](./plugins.md)** - Customize behavior (optional)

**Advanced Features:**

- **[🛠️ MCP Integration](./mcp.md)** - Tool calling (if needed)
- **[📊 Production](../providers.md)** - All providers setup
