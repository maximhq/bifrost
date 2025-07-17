# 🤖 Bifrost Client

Complete guide to using the main Bifrost client methods for chat completions, text completions, and request handling patterns.

> **💡 Quick Start:** See the [30-second setup](../../quickstart/go-package.md) to get a client running quickly.

---

## 📋 Client Overview

The Bifrost client is your main interface for making AI requests. It handles:

- **Request routing** to appropriate providers
- **Automatic fallbacks** when providers fail
- **Concurrent processing** with worker pools
- **Plugin execution** for custom middleware
- **MCP tool integration** for external capabilities

```go
// Initialize client
client, initErr := bifrost.Init(schemas.BifrostConfig{
    Account: &MyAccount{},
})
defer client.Cleanup() // Always cleanup!

// Make requests
response, bifrostErr := client.ChatCompletionRequest(ctx, request)
```

---

## 🚀 Core Methods

### **Chat Completion**

The primary method for conversational AI interactions:

```go
func (b *Bifrost) ChatCompletionRequest(
    ctx context.Context,
    req *schemas.BifrostRequest
) (*schemas.BifrostResponse, *schemas.BifrostError)
```

**Basic Example:**

```go
message := "Explain quantum computing in simple terms"
response, err := client.ChatCompletionRequest(context.Background(), &schemas.BifrostRequest{
    Provider: schemas.OpenAI,
    Model:    "gpt-4o-mini",
    Input: schemas.RequestInput{
        ChatCompletionInput: &[]schemas.BifrostMessage{
            {
                Role: schemas.ModelChatMessageRoleUser,
                Content: schemas.MessageContent{ContentStr: &message},
            },
        },
    },
})

if err != nil {
    log.Printf("Request failed: %v", err)
    return
}

// Access response
if len(response.Choices) > 0 && response.Choices[0].Message.Content.ContentStr != nil {
    fmt.Println("AI Response:", *response.Choices[0].Message.Content.ContentStr)
}
```

### **Text Completion**

For simple text generation without conversation context:

```go
func (b *Bifrost) TextCompletionRequest(
    ctx context.Context,
    req *schemas.BifrostRequest
) (*schemas.BifrostResponse, *schemas.BifrostError)
```

**Basic Example:**

```go
prompt := "Complete this story: Once upon a time in a digital realm,"
response, bifrostErr := client.TextCompletionRequest(context.Background(), &schemas.BifrostRequest{
    Provider: schemas.Anthropic,
    Model:    "claude-2.1", // Text completion models
        Input: schemas.RequestInput{
            TextCompletionInput: &prompt,
        },
        Params: &schemas.ModelParameters{
            MaxTokens: bifrost.Ptr(100),
        },
})
```

### **MCP Tool Execution**

Execute external tools manually for security and control:

```go
func (b *Bifrost) ExecuteMCPTool(
    ctx context.Context,
    toolCall schemas.ToolCall
) (*schemas.BifrostMessage, *schemas.BifrostError)
```

> **📖 Learn More:** See [MCP Integration](./mcp.md) for complete tool setup and usage patterns.

### **Cleanup**

Always cleanup resources when done:

```go
func (b *Bifrost) Cleanup()
```

**Example:**

```go
client, initErr := bifrost.Init(config)
if initErr != nil {
    log.Fatal(initErr)
}
defer client.Cleanup() // Ensures proper resource cleanup
```

---

## ⚡ Advanced Request Patterns

### **Multi-Turn Conversations**

Build conversational applications with message history:

```go
conversation := []schemas.BifrostMessage{
    {
        Role: schemas.ModelChatMessageRoleSystem,
        Content: schemas.MessageContent{ContentStr: &systemPrompt},
    },
    {
        Role: schemas.ModelChatMessageRoleUser,
        Content: schemas.MessageContent{ContentStr: &userMessage1},
    },
    {
        Role: schemas.ModelChatMessageRoleAssistant,
        Content: schemas.MessageContent{ContentStr: &assistantResponse1},
    },
    {
        Role: schemas.ModelChatMessageRoleUser,
        Content: schemas.MessageContent{ContentStr: &userMessage2},
    },
}

response, err := client.ChatCompletionRequest(ctx, &schemas.BifrostRequest{
    Provider: schemas.Anthropic,
    Model:    "claude-3-sonnet-20240229",
    Input: schemas.RequestInput{
        ChatCompletionInput: &conversation,
    },
})
```

### **Automatic Fallbacks**

Ensure reliability with provider fallbacks:

```go
response, err := client.ChatCompletionRequest(ctx, &schemas.BifrostRequest{
    Provider: schemas.OpenAI,        // Primary provider
    Model:    "gpt-4o-mini",
    Input:    input, // your input here
    Fallbacks: []schemas.Fallback{
        {Provider: schemas.Anthropic, Model: "claude-3-sonnet-20240229"},
        {Provider: schemas.Vertex, Model: "gemini-pro"},
        {Provider: schemas.Cohere, Model: "command-a-03-2025"},
    },
})

// Bifrost automatically tries fallbacks if primary fails
// Check which provider was actually used:
fmt.Printf("Used provider: %s\n", response.ExtraFields.Provider)
```

### **Request Parameters**

Fine-tune model behavior with parameters:

```go
temperature := 0.7
maxTokens := 1000
stopSequences := []string{"\n\n", "END"}

response, err := client.ChatCompletionRequest(ctx, &schemas.BifrostRequest{
    Provider: schemas.OpenAI,
    Model:    "gpt-4o-mini",
    Input:    input, // your input here
    Params: &schemas.ModelParameters{
        Temperature:     &temperature,
        MaxTokens:       &maxTokens,
        StopSequences:   &stopSequences,
    },
})
```

---

## 🛠️ Tool Calling

### **Basic Tool Usage**

Enable models to call external functions:

```go
// Define your tool
weatherTool := schemas.Tool{
    Type: "function",
    Function: schemas.Function{
        Name:        "get_weather",
        Description: "Get current weather for a location",
        Parameters: schemas.FunctionParameters{
            Type: "object",
            Properties: map[string]interface{}{
                "location": map[string]interface{}{
                    "type":        "string",
                    "description": "City name",
                },
                "unit": map[string]interface{}{
                    "type": "string",
                    "enum": []string{"celsius", "fahrenheit"},
                },
            },
            Required: []string{"location"},
        },
    },
}

// Make request with tools
auto := "auto"
response, err := client.ChatCompletionRequest(ctx, &schemas.BifrostRequest{
    Provider: schemas.OpenAI,
    Model:    "gpt-4o-mini",
    Input:    input, // your input here
    Params: &schemas.ModelParameters{
        Tools:      &[]schemas.Tool{weatherTool},
        ToolChoice: &schemas.ToolChoice{ToolChoiceStr: &auto},
    },
})

// Check if model wants to call tools
if len(response.Choices) > 0 && response.Choices[0].Message.ToolCalls != nil {
    for _, toolCall := range *response.Choices[0].Message.ToolCalls {
        if toolCall.Function.Name != nil && *toolCall.Function.Name == "get_weather" {
            // Handle the tool call
            result := handleWeatherCall(toolCall.Function.Arguments)

            // Add tool result to conversation and continue
            // ... (see MCP documentation for automated tool handling)
        }
    }
}
```

### **Tool Choice Control**

Control when and which tools the model uses:

```go
// Auto: Model decides whether to call tools
auto := "auto"
toolChoice := &schemas.ToolChoice{ToolChoiceStr: &auto}

// None: Never call tools
none := "none"
toolChoice := &schemas.ToolChoice{ToolChoiceStr: &none}

// Required: Must call at least one tool
required := "required"
toolChoice := &schemas.ToolChoice{ToolChoiceStr: &required}

// Specific function: Must call this specific tool
toolChoice := &schemas.ToolChoice{
    ToolChoiceStruct: &schemas.ToolChoiceStruct{
        Type: schemas.ToolChoiceTypeFunction,
        Function: schemas.ToolChoiceFunction{
            Name: "get_weather",
        },
    },
}
```

---

## 🖼️ Multimodal Requests

### **Image Analysis**

Send images for analysis (supported by GPT-4V, Claude, etc.):

```go
// Image from URL
imageMessage := schemas.BifrostMessage{
    Role: schemas.ModelChatMessageRoleUser,
    Content: schemas.MessageContent{
        ContentBlocks: &[]schemas.ContentBlock{
            {
                Type: schemas.ContentBlockTypeText,
                Text: bifrost.Ptr("What is this image about?"),
            },
            {
                Type: schemas.ContentBlockTypeImage,
                ImageURL: &schemas.ImageURLStruct{
                    URL:    "https://example.com/image.jpg",
                    Detail: &detail, // "high", "low", or "auto"
                },
            },
        },
    },
}

// Image from base64
base64Image := "data:image/jpeg;base64,/9j/4AAQSkZJRgABA..."
imageMessageBase64 := schemas.BifrostMessage{
    Role: schemas.ModelChatMessageRoleUser,
    Content: schemas.MessageContent{
        ContentBlocks: &[]schemas.ContentBlock{
            {
                Type: schemas.ContentBlockTypeText,
                Text: bifrost.Ptr("What is this image about?"),
            },
            {
                Type: schemas.ContentBlockTypeImage,
                ImageURL: &schemas.ImageURLStruct{
                    URL: base64Image,
                },
            },
        },
    },
}

response, err := client.ChatCompletionRequest(ctx, &schemas.BifrostRequest{
    Provider: schemas.OpenAI,
    Model:    "gpt-4o", // Multimodal model
    Input: schemas.RequestInput{
        ChatCompletionInput: &[]schemas.BifrostMessage{imageMessage},
    },
})
```

---

## 🔄 Context Management

### **Context with Timeouts**

Control request timeouts and cancellation:

```go
// Request with timeout
ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
defer cancel()

response, err := client.ChatCompletionRequest(ctx, request)
if err != nil {
    if ctx.Err() == context.DeadlineExceeded {
        fmt.Println("Request timed out")
    }
}

// Cancellable request
ctx, cancel := context.WithCancel(context.Background())

// Cancel from another goroutine
go func() {
    time.Sleep(5 * time.Second)
    cancel()
}()

response, err := client.ChatCompletionRequest(ctx, request)
```

### **Context with Values**

Pass metadata through request context:

```go
// Add request metadata
ctx := context.WithValue(context.Background(), "user_id", "user123")
ctx = context.WithValue(ctx, "session_id", "session456")

// Plugins can access these values
response, err := client.ChatCompletionRequest(ctx, request)
```

---

## 📊 Response Handling

### **Response Structure**

Understanding the response format:

```go
type BifrostResponse struct {
    ID                string                     `json:"id"`
    Object            string                     `json:"object"`
    Choices           []BifrostResponseChoice    `json:"choices"`
    Model             string                     `json:"model"`
    Created           int                        `json:"created"`
    Usage             LLMUsage                   `json:"usage"`
    ExtraFields       BifrostResponseExtraFields `json:"extra_fields"`
}

// Access response data
if len(response.Choices) > 0 {
    choice := response.Choices[0]

    // Text content
    if choice.Message.Content.ContentStr != nil {
        content := *choice.Message.Content.ContentStr
    }

    // Tool calls
    if choice.Message.ToolCalls != nil {
        for _, toolCall := range *choice.Message.ToolCalls {
            // Handle tool call
        }
    }

    // Finish reason
    if choice.FinishReason != nil {
        reason := *choice.FinishReason // "stop", "length", "tool_calls", etc.
    }
}

// Provider metadata
providerUsed := response.ExtraFields.Provider
latency := response.ExtraFields.Latency
tokenUsage := response.Usage
```

### **Error Handling**

Handle different types of errors:

```go
response, err := client.ChatCompletionRequest(ctx, request)
if err != nil {
    // Check if it's a Bifrost error
    if err.IsBifrostError {
        fmt.Printf("Bifrost error: %s\n", err.Error.Message)
    }

    // Check for specific error types
    if err.Error.Type != nil {
        switch *err.Error.Type {
        case schemas.RequestCancelled:
            fmt.Println("Request was cancelled")
        case schemas.ErrProviderRequest:
            fmt.Println("Provider request failed")
        default:
            fmt.Printf("Error type: %s\n", *err.Error.Type)
        }
    }

    // Check HTTP status code
    if err.StatusCode != nil {
        fmt.Printf("HTTP Status: %d\n", *err.StatusCode)
    }

    return
}
```

---

## 🔧 Advanced Configuration

### **Custom Initialization**

Configure client behavior during initialization:

```go
// Production configuration
client, initErr := bifrost.Init(schemas.BifrostConfig{
    Account:            &MyAccount{},
    Plugins:            []schemas.Plugin{&MyPlugin{}},
    Logger:             customLogger,
    InitialPoolSize:    200,           // Higher pool for performance
    DropExcessRequests: false,         // Wait for queue space (safer)
    MCPConfig: &schemas.MCPConfig{
        ClientConfigs: []schemas.MCPClientConfig{
            {
                Name:           "weather-tools",
                ConnectionType: schemas.MCPConnectionTypeSTDIO,
                StdioConfig: &schemas.MCPStdioConfig{
                    Command: "npx",
                    Args:    []string{"-y", "weather-mcp-server"},
                },
            },
        },
    },
})
```

### **Graceful Cleanup**

Always cleanup resources properly:

```go
func main() {
    client, initErr := bifrost.Init(config)
    if initErr != nil {
        log.Fatal(initErr)
    }

    // Setup graceful shutdown
    defer client.Cleanup()

    // Handle OS signals for clean shutdown
    c := make(chan os.Signal, 1)
    signal.Notify(c, os.Interrupt, syscall.SIGTERM)

    go func() {
        <-c
        fmt.Println("Shutting down gracefully...")
        client.Cleanup()
        os.Exit(0)
    }()

    // Your application logic
    // ...
}
```

---

## 🧪 Testing Client Usage

### **Unit Tests**

Test client methods with mock providers:

```go
func TestChatCompletion(t *testing.T) {
    account := &TestAccount{}
    client, initErr := bifrost.Init(schemas.BifrostConfig{
        Account: account,
    })
    require.Nil(t, initErr)
    defer client.Cleanup()

    message := "Hello, test!"
    response, err := client.ChatCompletionRequest(context.Background(), &schemas.BifrostRequest{
        Provider: schemas.OpenAI,
        Model:    "gpt-4o-mini",
        Input: schemas.RequestInput{
            ChatCompletionInput: &[]schemas.BifrostMessage{
                {Role: schemas.ModelChatMessageRoleUser, Content: schemas.MessageContent{ContentStr: &message}},
            },
        },
    })

    assert.NoError(t, err)
    assert.NotNil(t, response)
    assert.Greater(t, len(response.Choices), 0)
}
```

### **Integration Tests**

Test with real providers (requires API keys):

```go
func TestIntegrationChatCompletion(t *testing.T) {
    if testing.Short() {
        t.Skip("Skipping integration test")
    }

    // Requires real API key
    if os.Getenv("OPENAI_API_KEY") == "" {
        t.Skip("OPENAI_API_KEY not set")
    }

    account := &ProductionAccount{}
    client, initErr := bifrost.Init(schemas.BifrostConfig{
        Account: account,
    })
    require.Nil(t, initErr)
    defer client.Cleanup()

    // Test actual request
    message := "What is 2+2?"
    response, err := client.ChatCompletionRequest(context.Background(), &schemas.BifrostRequest{
        Provider: schemas.OpenAI,
        Model:    "gpt-4o-mini",
        Input: schemas.RequestInput{
            ChatCompletionInput: &[]schemas.BifrostMessage{
                {Role: schemas.ModelChatMessageRoleUser, Content: schemas.MessageContent{ContentStr: &message}},
            },
        },
    })

    assert.NoError(t, err)
    assert.Contains(t, *response.Choices[0].Message.Content.ContentStr, "4")
}
```

---

## 📚 Related Documentation

- **[🏛️ Account Interface](./account.md)** - Configure providers and keys
- **[🔌 Plugins](./plugins.md)** - Add custom middleware
- **[🛠️ MCP Integration](./mcp.md)** - Tool calling and external integrations
- **[📋 Schemas](./schemas.md)** - Data structures and interfaces reference
- **[🌐 HTTP Transport](../http-transport/)** - REST API alternative

> **🏛️ Architecture:** For system internals and performance details, see [Architecture Documentation](../../architecture/).
