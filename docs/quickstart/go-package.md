# 🔧 Go Package Quick Start

Get Bifrost running in your Go application in 30 seconds with this minimal setup guide.

![Bifrost Go Package Demo Video](../media/package-demo.mp4)

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

```go
package main

import (
    "context"
    "fmt"
    "os"
    bifrost "github.com/maximhq/bifrost/core"
    "github.com/maximhq/bifrost/core/schemas"
)

// Simple account implementation
type MyAccount struct{}

func (a *MyAccount) GetConfiguredProviders() ([]schemas.ModelProvider, error) {
    return []schemas.ModelProvider{schemas.OpenAI}, nil
}

func (a *MyAccount) GetKeysForProvider(provider schemas.ModelProvider) ([]schemas.Key, error) {
	if provider == schemas.OpenAI {
		return []schemas.Key{{
			Value:  os.Getenv("OPENAI_API_KEY"),
			Models: []string{"gpt-4o-mini"},
			Weight: 1.0,
		}}, nil
	}
	return nil, fmt.Errorf("provider %s not supported", provider)
}

func (a *MyAccount) GetConfigForProvider(provider schemas.ModelProvider) (*schemas.ProviderConfig, error) {
	if provider == schemas.OpenAI {
		// Return default config (can be customized for advanced use cases)
		return &schemas.ProviderConfig{
			NetworkConfig:            schemas.DefaultNetworkConfig,
			ConcurrencyAndBufferSize: schemas.DefaultConcurrencyAndBufferSize,
		}, nil
	}
	return nil, fmt.Errorf("provider %s not supported", provider)
}


func main() {
	client, initErr := bifrost.Init(schemas.BifrostConfig{
		Account: &MyAccount{},
	})
	if initErr != nil {
		panic(initErr)
	}
	defer client.Cleanup()

	messages := []schemas.BifrostMessage{
		{Role: schemas.ModelChatMessageRoleUser, Content: schemas.MessageContent{ContentStr: bifrost.Ptr("Hello, Bifrost!")}},
	}

	response, bifrostErr := client.ChatCompletionRequest(context.Background(), &schemas.BifrostRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o-mini",
		Input: schemas.RequestInput{
			ChatCompletionInput: &messages,
		},
	})

	if bifrostErr != nil {
		panic(bifrostErr)
	}

	if len(response.Choices) > 0 && response.Choices[0].Message.Content.ContentStr != nil {
		fmt.Println("AI Response:", *response.Choices[0].Message.Content.ContentStr)
	}

}

```

### 4. Run Your App

```bash
go run main.go
```

**🎉 Success!** You should see an AI response in your terminal.

---

## 🚀 Next Steps (5 minutes each)

### **🔄 Add Multiple Providers**

```go
// Add to environment
export ANTHROPIC_API_KEY="your-anthropic-key"

// Update GetConfiguredProviders
func (a *MyAccount) GetConfiguredProviders() ([]schemas.ModelProvider, error) {
    return []schemas.ModelProvider{schemas.OpenAI, schemas.Anthropic}, nil
}

// Update GetKeysForProvider to handle both providers
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
    return nil, fmt.Errorf("provider %s not supported", provider)
}

// GetConfigForProvider remains the same
func (a *MyAccount) GetConfigForProvider(provider schemas.ModelProvider) (*schemas.ProviderConfig, error) {
    return &schemas.ProviderConfig{
        NetworkConfig:            schemas.DefaultNetworkConfig,
        ConcurrencyAndBufferSize: schemas.DefaultConcurrencyAndBufferSize,
    }, nil
}
```

### **⚡ Add Automatic Fallbacks**

```go
// Request with fallback providers

messages := []schemas.BifrostMessage{
		{Role: schemas.ModelChatMessageRoleUser, Content: schemas.MessageContent{ContentStr: bifrost.Ptr("Hello, Bifrost!")}},
	}

	response, bifrostErr := client.ChatCompletionRequest(context.Background(), &schemas.BifrostRequest{
		Provider: schemas.OpenAI, // Primary provider
		Model:    "gpt-4o-mini",
		Input: schemas.RequestInput{
			ChatCompletionInput: &messages,
		},
		Params: &schemas.ModelParameters{
			MaxTokens: bifrost.Ptr(100),
		},
		Fallbacks: []schemas.Fallback{
			{Provider: schemas.Anthropic, Model: "claude-3-sonnet-20240229"},
		},
	})
```

### **🛠️ Add Tool Calling**

```go
// Add tools to your request
messages := []schemas.BifrostMessage{
		{Role: schemas.ModelChatMessageRoleUser, Content: schemas.MessageContent{ContentStr: bifrost.Ptr("Which tool can I use to get the weather?")}},
	}

response, bifrostErr := client.ChatCompletionRequest(context.Background(), &schemas.BifrostRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o-mini",
		Input: schemas.RequestInput{
			ChatCompletionInput: &messages,
		},
		Params: &schemas.ModelParameters{
			Tools: &[]schemas.Tool{
				{
					Type: "function",
					Function: schemas.Function{
						Name:        "get_weather",
						Description: "Get current weather information",
						Parameters: schemas.FunctionParameters{
							Type: "object",
							Properties: map[string]interface{}{
								"location": map[string]interface{}{
									"type":        "string",
									"description": "City name",
								},
							},
						},
					},
				},
			},
		},
	})
```

---

## 💬 Need Help?

**🔗 [Join our Discord](https://getmax.im/bifrost-discord)** for real-time setup assistance and Go-specific support!

---

## 📚 Learn More

| What You Want                | Where to Go                                             | Time      |
| ---------------------------- | ------------------------------------------------------- | --------- |
| **Complete setup guide**     | [📖 Go Package Usage](../usage/go-package/)             | 10 min    |
| **Add all 10+ providers**     | [🔗 Providers](../providers.md)                         | 5 min     |
| **Production configuration** | [👤 Account Management](../usage/go-package/account.md) | 15 min    |
| **Custom plugins**           | [🔌 Plugins](../usage/go-package/plugins.md)            | 20 min    |
| **MCP integration**          | [🛠️ MCP](../usage/go-package/mcp.md)                    | 15 min    |
| **Full API reference**       | [📊 Schemas](../usage/go-package/schemas.md)            | Reference |

---

## 🔄 Prefer HTTP API?

If you want to use Bifrost from Python, Node.js, or other languages, try the **[HTTP Transport Quick Start](http-transport.md)** instead.

---

## 💡 Why Go Package?

- ✅ **Type safety** - Compile-time validation
- ✅ **Performance** - No HTTP overhead
- ✅ **Custom logic** - Full programmatic control
- ✅ **Advanced features** - Complete plugin system access

**🎯 Ready for production? Check out [Complete Go Usage Guide](../usage/go-package/) →**
