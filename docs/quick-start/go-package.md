# 🔧 Go Package Quick Start

Get Bifrost running in your Go application in 30 seconds with this minimal setup guide.

## ⚡ 30-Second Setup

### 1. Install Package

```bash
go mod init my-bifrost-app
go get github.com/maximhq/bifrost/core
```

### 2. Set Environment Variable

```bash
export OPENAI_API_KEY="your-openai-api-key"
```

### 3. Create `main.go`

**📖 [See complete implementation →](../usage/examples.md#basic-account-implementation)**

```go
package main

import (
    "context"
    "fmt"
    "github.com/maximhq/bifrost/core"
    "github.com/maximhq/bifrost/core/schemas"
)

func main() {
    // Initialize Bifrost
    client, err := bifrost.Init(schemas.BifrostConfig{
        Account: &MyAccount{}, // See canonical implementation above
    })
    if err != nil {
        panic(err)
    }
    defer client.Cleanup()

    // Make a request (see canonical example)
    response, err := client.ChatCompletionRequest(/* ... */)

    if err != nil {
        panic(err)
    }

    // Print response
    if len(response.Choices) > 0 && response.Choices[0].Message.Content.ContentStr != nil {
        fmt.Println("AI Response:", *response.Choices[0].Message.Content.ContentStr)
    }
}
```

**📖 [Complete request example →](../usage/examples.md#basic-chat-completion-request)**

### 4. Run Your App

```bash
go run main.go
```

**🎉 Success!** You should see an AI response in your terminal.

---

## 🚀 Next Steps

### Add More Providers

<details>
<summary><strong>Add Anthropic Support</strong></summary>

```go
// Add to environment
export ANTHROPIC_API_KEY="your-anthropic-key"

// Update GetConfiguredProviders
func (a *MyAccount) GetConfiguredProviders() ([]schemas.ModelProvider, error) {
    return []schemas.ModelProvider{schemas.OpenAI, schemas.Anthropic}, nil
}

// Update GetKeysForProvider
func (a *MyAccount) GetKeysForProvider(provider schemas.ModelProvider) ([]schemas.Key, error) {
    switch provider {
    case schemas.OpenAI:
        return []schemas.Key{{
            Value: os.Getenv("OPENAI_API_KEY"),
            Models: []string{"gpt-4o-mini"},
            Weight: 1.0,
        }}, nil
    case schemas.Anthropic:
        return []schemas.Key{{
            Value: os.Getenv("ANTHROPIC_API_KEY"),
            Models: []string{"claude-3-sonnet-20240229"},
            Weight: 1.0,
        }}, nil
    }
    return nil, nil
}
```

</details>

### Add Fallbacks

<details>
<summary><strong>Automatic Fallbacks</strong></summary>

**📖 [See complete fallback example →](../usage/examples.md#request-with-fallbacks)**

</details>

### Add Model Parameters

<details>
<summary><strong>Configure Model Behavior</strong></summary>

**📖 [See complete parameterized example →](../usage/examples.md#chat-with-parameters)**

</details>

---

## 📚 Learn More

| Topic              | Link                                                  | Description                      |
| ------------------ | ----------------------------------------------------- | -------------------------------- |
| **Complete Setup** | [Core Configuration](../configuration/core-config.md) | Production-ready configuration   |
| **All Providers**  | [Provider Guides](../features/providers/README.md)    | Setup for all 8 AI providers     |
| **Tool Calling**   | [MCP Integration](../features/mcp-integration.md)     | Add external tools and functions |
| **Plugins**        | [Plugin System](../features/plugins.md)               | Extend Bifrost functionality     |
| **API Reference**  | [Go Package API](../usage/go-package/README.md)       | Complete API documentation       |

---

## 🔄 Alternative: HTTP Transport

Prefer REST APIs? Try the **[HTTP Transport Quick Start](http-transport.md)** instead.

---

**🎯 Got it working? Move to [Configuration](../configuration/core-config.md) for production setup!**
