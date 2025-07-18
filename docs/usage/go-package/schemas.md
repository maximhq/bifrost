# 📋 Schemas

Data structures, interfaces, and type definitions reference for Bifrost Go package. This guide focuses on practical usage patterns rather than comprehensive API documentation.

> **💡 Quick Start:** See the [30-second setup](../../quickstart/go-package.md) for basic schema usage examples.

---

## 📋 Schema Overview

Bifrost schemas define the structure for:

- **Request/Response data** across all providers
- **Configuration interfaces** for accounts and providers
- **Plugin interfaces** for custom middleware
- **MCP tool definitions** for external integrations
- **Error handling** with detailed error types

> **🔄 OpenAI Compatibility:** Bifrost follows OpenAI's request/response structure for maximum compatibility. This ensures easy migration from OpenAI and consistent behavior across all providers.

> **📖 Complete Reference:** All schemas have detailed GoDoc comments in the source code. This guide focuses on practical usage patterns.

---

## 🚀 Core Request/Response Schemas

### **BifrostRequest**

The primary request structure for all AI interactions:

```go
type BifrostRequest struct {
    Provider    ModelProvider    `json:"provider"`              // Required: OpenAI, Anthropic, etc.
    Model       string          `json:"model"`                 // Required: gpt-4o-mini, claude-3, etc.
    Input       RequestInput    `json:"input"`                 // Required: Messages or text
    Params      *ModelParameters `json:"params,omitempty"`      // Optional: Temperature, max tokens, etc.
    Fallbacks   []Fallback      `json:"fallbacks,omitempty"`   // Optional: Provider fallback chain
}

// Usage example
request := &schemas.BifrostRequest{
    Provider: schemas.OpenAI,
    Model:    "gpt-4o-mini",
    Input: schemas.RequestInput{
        ChatCompletionInput: &[]schemas.BifrostMessage{
            {
                Role: schemas.ModelChatMessageRoleUser,
                Content: schemas.MessageContent{ContentStr: &userMessage},
            },
        },
    },
    Params: &schemas.ModelParameters{
        Temperature: &[]float64{0.7}[0],
        MaxTokens:   &[]int{1000}[0],
    },
}
```

### **BifrostResponse**

The unified response structure across all providers:

```go
type BifrostResponse struct {
    ID          string                      `json:"id"`           // Unique response ID
    Object      string                      `json:"object"`       // Response type
    Choices     []BifrostResponseChoice     `json:"choices"`      // Response choices
    Model       string                      `json:"model"`        // Model used
    Created     int                         `json:"created"`      // Unix timestamp
    Usage       LLMUsage                    `json:"usage"`        // Token usage
    ExtraFields BifrostResponseExtraFields  `json:"extra_fields"` // Bifrost metadata
}

// Access response content
if len(response.Choices) > 0 {
    choice := response.Choices[0]

    // Text content
    if choice.BifrostNonStreamResponseChoice != nil && choice.BifrostNonStreamResponseChoice.Message.Content.ContentStr != nil {
        fmt.Println("Response:", *choice.BifrostNonStreamResponseChoice.Message.Content.ContentStr)
    }

    // Streaming content
    if choice.BifrostStreamResponseChoice != nil && choice.BifrostStreamResponseChoice.Delta.Content != nil {
        fmt.Println("Streaming Response:", *choice.BifrostStreamResponseChoice.Delta.Content)
    }

    // Tool calls
    if choice.BifrostNonStreamResponseChoice != nil && choice.BifrostNonStreamResponseChoice.Message.ToolCalls != nil {
        for _, toolCall := range *choice.BifrostNonStreamResponseChoice.Message.ToolCalls {
            // Handle tool call
        }
    }

    // Finish reason
    if choice.FinishReason != nil {
        fmt.Printf("Finished: %s\n", *choice.FinishReason) // "stop", "length", etc.
    }
}

// Usage information
fmt.Printf("Tokens used: %d (prompt: %d, completion: %d)\n",
    response.Usage.TotalTokens,
    response.Usage.PromptTokens,
    response.Usage.CompletionTokens)

// Provider metadata
fmt.Printf("Provider: %s, Latency: %v\n",
    response.ExtraFields.Provider,
    response.ExtraFields.Latency)
```

---

## ⚡ Message and Content Schemas

### **BifrostMessage**

Unified message structure for conversations:

```go
type BifrostMessage struct {
    Role      ModelChatMessageRole `json:"role"`                // user, assistant, system, tool
    Content   MessageContent       `json:"content"`             // Text or multimodal content
    Name      *string             `json:"name,omitempty"`       // Message author name
    ToolCalls *[]ToolCall         `json:"tool_calls,omitempty"` // Function calls
    ToolCallID *string            `json:"tool_call_id,omitempty"` // Tool response ID
}

// System message
systemMsg := schemas.BifrostMessage{
    Role: schemas.ModelChatMessageRoleSystem,
    Content: schemas.MessageContent{
        ContentStr: &[]string{"You are a helpful assistant."}[0],
    },
}

// User message with text
userMsg := schemas.BifrostMessage{
    Role: schemas.ModelChatMessageRoleUser,
    Content: schemas.MessageContent{
        ContentStr: &userText,
    },
}

// User message with image
imageMsg := schemas.BifrostMessage{
    Role: schemas.ModelChatMessageRoleUser,
    Content: schemas.MessageContent{
        ContentBlocks: &[]schemas.ContentBlock{
            {
                Type: schemas.ContentBlockTypeText,
                Text: &[]string{"What's in this image?"}[0],
            },
            {
                Type: schemas.ContentBlockTypeImageURL,
                ImageURL: &schemas.ImageURLStruct{
                    URL: "https://example.com/image.jpg",
                },
            },
        },
    },
}

// Tool response message
toolMsg := schemas.BifrostMessage{
    Role:       schemas.ModelChatMessageRoleTool,
    ToolCallID: &toolCallID,
    Content: schemas.MessageContent{
        ContentStr: &toolResult,
    },
}
```

### **MessageContent**

Flexible content structure supporting text and multimodal inputs:

```go
type MessageContent struct {
    ContentStr    *string        `json:"content_str,omitempty"`    // Simple text content
    ContentBlocks *[]ContentBlock `json:"content_blocks,omitempty"` // Multimodal content
}

// Text-only content
textContent := schemas.MessageContent{
    ContentStr: &[]string{"Hello, world!"}[0],
}

// Multimodal content
multimodalContent := schemas.MessageContent{
    ContentBlocks: &[]schemas.ContentBlock{
        {
            Type: schemas.ContentBlockTypeText,
            Text: &[]string{"Analyze this image:"}[0],
        },
        {
            Type: schemas.ContentBlockTypeImageURL,
            ImageURL: &schemas.ImageURLStruct{
                URL:    imageURL,
                Detail: &[]string{"high"}[0], // "low", "high", "auto"
            },
        },
    },
}
```

---

## 🔧 Configuration Schemas

### **BifrostConfig**

Main configuration for initializing Bifrost:

```go
type BifrostConfig struct {
    Account            Account       `json:"account"`                        // Required: Provider configuration
    Plugins            []Plugin      `json:"plugins,omitempty"`              // Optional: Custom middleware
    Logger             Logger        `json:"logger,omitempty"`               // Optional: Custom logger
    InitialPoolSize    int          `json:"initial_pool_size,omitempty"`    // Optional: Worker pool size
    DropExcessRequests bool         `json:"drop_excess_requests,omitempty"` // Optional: Drop vs queue
    MCPConfig          *MCPConfig   `json:"mcp_config,omitempty"`           // Optional: MCP integration
}

// Basic configuration
config := schemas.BifrostConfig{
    Account: &MyAccount{},
}

// Production configuration
productionConfig := schemas.BifrostConfig{
    Account:            &MyAccount{},
    Plugins:            []schemas.Plugin{rateLimitPlugin, metricsPlugin},
    Logger:             jsonLogger,
    InitialPoolSize:    200,
    DropExcessRequests: false, // Wait for queue space
    MCPConfig: &schemas.MCPConfig{
        ClientConfigs: []schemas.MCPClientConfig{
            // MCP tool configurations
        },
    },
}
```

### **ModelParameters**

Request parameters for fine-tuning model behavior:

```go
type ModelParameters struct {
    Temperature      *float64     `json:"temperature,omitempty"`       // 0.0-2.0, creativity level
    MaxTokens        *int         `json:"max_tokens,omitempty"`        // Maximum response length
    TopP             *float64     `json:"top_p,omitempty"`            // 0.0-1.0, nucleus sampling
    PresencePenalty  *float64     `json:"presence_penalty,omitempty"`  // -2.0-2.0, topic diversity
    FrequencyPenalty *float64     `json:"frequency_penalty,omitempty"` // -2.0-2.0, repetition penalty
    StopSequences    *[]string    `json:"stop,omitempty"`             // Sequences to stop generation
    Tools            *[]Tool      `json:"tools,omitempty"`            // Available functions
    ToolChoice       *ToolChoice  `json:"tool_choice,omitempty"`      // Tool usage control
}

// Conservative parameters
conservative := &schemas.ModelParameters{
    Temperature:      &[]float64{0.3}[0],
    MaxTokens:        &[]int{500}[0],
    PresencePenalty:  &[]float64{0.1}[0],
    FrequencyPenalty: &[]float64{0.1}[0],
}

// Creative parameters
creative := &schemas.ModelParameters{
    Temperature: &[]float64{0.9}[0],
    MaxTokens:   &[]int{2000}[0],
    TopP:        &[]float64{0.95}[0],
}

// Tool-enabled parameters
withTools := &schemas.ModelParameters{
    Temperature: &[]float64{0.1}[0],
    Tools:       &[]schemas.Tool{myTool},
    ToolChoice:  &schemas.ToolChoice{ToolChoiceStr: &[]string{"auto"}[0]},
}
```

---

## 🛠️ Tool and MCP Schemas

### **Tool Definition**

Structure for defining AI tools/functions:

```go
type Tool struct {
    Type     string   `json:"type"`     // Always "function"
    Function Function `json:"function"` // Function definition
}

type Function struct {
    Name        string             `json:"name"`                  // Function name
    Description string             `json:"description"`           // What the function does
    Parameters  FunctionParameters `json:"parameters"`            // Input schema
}

type FunctionParameters struct {
    Type       string                 `json:"type"`       // "object"
    Properties map[string]interface{} `json:"properties"` // Parameter definitions
    Required   []string              `json:"required"`   // Required parameters
}

// Example tool definition
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
                    "description": "City name or coordinates",
                },
                "unit": map[string]interface{}{
                    "type":        "string",
                    "enum":        []string{"celsius", "fahrenheit"},
                    "description": "Temperature unit",
                },
            },
            Required: []string{"location"},
        },
    },
}
```

### **ToolChoice Control**

Control when and which tools the model uses:

```go
type ToolChoice struct {
    ToolChoiceStr    *string           `json:"tool_choice_str,omitempty"`    // "auto", "none", "required"
    ToolChoiceStruct *ToolChoiceStruct `json:"tool_choice_struct,omitempty"` // Specific function
}

type ToolChoiceStruct struct {
    Type     ToolChoiceType        `json:"type"`     // "function"
    Function ToolChoiceFunction    `json:"function"` // Function name
}

// Let model decide
auto := schemas.ToolChoice{
    ToolChoiceStr: &[]string{"auto"}[0],
}

// Never use tools
none := schemas.ToolChoice{
    ToolChoiceStr: &[]string{"none"}[0],
}

// Must use at least one tool
required := schemas.ToolChoice{
    ToolChoiceStr: &[]string{"required"}[0],
}

// Force specific tool
forceWeather := schemas.ToolChoice{
    ToolChoiceStruct: &schemas.ToolChoiceStruct{
        Type: schemas.ToolChoiceTypeFunction,
        Function: schemas.ToolChoiceFunction{
            Name: "get_weather",
        },
    },
}
```

---

## 📊 Interface Implementations

### **Account Interface**

Provider configuration and key management:

```go
type Account interface {
    GetConfiguredProviders() ([]ModelProvider, error)
    // GetKeysForProvider receives a context that can contain data from any source that sets
    // values before the Bifrost request. This includes application code, middleware, plugin
    // pre-hooks, or any other source. Implementations can use this context data to make
    // dynamic key selection decisions based on any values present during the request.
    GetKeysForProvider(ctx *context.Context, providerKey ModelProvider) ([]Key, error)
    GetConfigForProvider(ModelProvider) (*ProviderConfig, error)
}

// Example implementation pattern
type MyAccount struct {
    // Your configuration data
}

func (a *MyAccount) GetConfiguredProviders() ([]schemas.ModelProvider, error) {
    return []schemas.ModelProvider{
        schemas.OpenAI,
        schemas.Anthropic,
        schemas.Vertex,
    }, nil
}

// Example of context-aware key selection
func (a *MyAccount) GetKeysForProvider(ctx *context.Context, provider schemas.ModelProvider) ([]schemas.Key, error) {
    switch provider {
    case schemas.OpenAI:
        // Check for any context values
        if ctx != nil {
            // Example: Value set by application code
            if userRole, ok := (*ctx).Value("user_role").(string); ok && userRole == "premium" {
                return []schemas.Key{{
                    Value:  os.Getenv("OPENAI_PREMIUM_KEY"),
                    Models: []string{"gpt-4o"},
                    Weight: 1.0,
                }}, nil
            }
            
            // Example: Value set by middleware
            if region, ok := (*ctx).Value("geo_region").(string); ok && region == "eu" {
                return []schemas.Key{{
                    Value:  os.Getenv("OPENAI_EU_KEY"),
                    Models: []string{"gpt-4o"},
                    Weight: 1.0,
                }}, nil
            }
        }
        // Default key if no special context
        return []schemas.Key{{
            Value:  os.Getenv("OPENAI_API_KEY"),
            Models: []string{"gpt-4o-mini", "gpt-4o"},
            Weight: 1.0,
        }}, nil
    }
    return nil, fmt.Errorf("provider not supported")
}

func (a *MyAccount) GetConfigForProvider(provider schemas.ModelProvider) (*schemas.ProviderConfig, error) {
    return &schemas.ProviderConfig{
        NetworkConfig:            schemas.DefaultNetworkConfig,
        ConcurrencyAndBufferSize: schemas.DefaultConcurrencyAndBufferSize,
        // Provider-specific MetaConfig if needed
    }, nil
}
```

### **Plugin Interface**

Custom middleware for request/response processing:

```go
type Plugin interface {
    GetName() string
    PreHook(*context.Context, *BifrostRequest) (*BifrostRequest, *PluginShortCircuit, error)
    PostHook(*context.Context, *BifrostResponse, *BifrostError) (*BifrostResponse, *BifrostError, error)
    Cleanup() error
}

// Example plugin implementation
type LoggingPlugin struct {
    logger *log.Logger
}

func (p *LoggingPlugin) GetName() string {
    return "logging"
}

func (p *LoggingPlugin) PreHook(ctx *context.Context, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.PluginShortCircuit, error) {
    p.logger.Printf("Request: %s %s", req.Provider, req.Model)
    return req, nil, nil // Continue normal flow
}

func (p *LoggingPlugin) PostHook(ctx *context.Context, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
    if err != nil {
        p.logger.Printf("Error: %s", err.Error.Message)
    } else {
        p.logger.Printf("Success: %s", result.Model)
    }
    return result, err, nil // Pass through unchanged
}

func (p *LoggingPlugin) Cleanup() error {
    return nil
}
```

### **Logger Interface**

Custom logging implementation:

```go
type Logger interface {
    Log(LogLevel, string, ...LogField)
}

type LogField struct {
    Key   string
    Value interface{}
}

// Example logger implementation
type MyLogger struct {
    level schemas.LogLevel
}

func (l *MyLogger) Log(level schemas.LogLevel, message string, fields ...schemas.LogField) {
    if level < l.level {
        return
    }

    fieldsStr := ""
    for _, field := range fields {
        fieldsStr += fmt.Sprintf(" %s=%v", field.Key, field.Value)
    }

    fmt.Printf("[%s] %s%s\n", levelString(level), message, fieldsStr)
}
```

---

## 🚨 Error Handling Schemas

### **BifrostError**

Comprehensive error information:

```go
type BifrostError struct {
    IsBifrostError bool        `json:"is_bifrost_error"`      // true for Bifrost errors
    StatusCode     *int        `json:"status_code,omitempty"` // HTTP status code
    Error          ErrorField  `json:"error"`                 // Error details
    AllowFallbacks *bool       `json:"-"` // For plugin developers only
}

type ErrorField struct {
    Type    *string `json:"type,omitempty"`    // Error type classification
    Message string  `json:"message"`           // Human-readable message
    Code    *string `json:"code,omitempty"`    // Provider-specific error code
}

// Handle errors
response, err := client.ChatCompletionRequest(ctx, request)
if err != nil {
    if err.IsBifrostError {
        // Bifrost-specific error
        if err.StatusCode != nil {
            fmt.Printf("HTTP Status: %d\n", *err.StatusCode)
        }

        if err.Error.Type != nil {
            switch *err.Error.Type {
            case schemas.RequestCancelled:
                fmt.Println("Request was cancelled")
            case schemas.ErrProviderRequest:
                fmt.Println("Provider request failed")
            case schemas.ErrRateLimit:
                fmt.Println("Rate limit exceeded")
            }
        }
    } else {
        // Standard Go error
        fmt.Printf("Error: %s\n", err.Error.Message)
    }
}
```

---

## 🎯 Common Usage Patterns

### **Provider Selection**

Available providers and typical models:

```go
// All supported providers
providers := []schemas.ModelProvider{
    schemas.OpenAI,      // GPT models
    schemas.Anthropic,   // Claude models
    schemas.Azure,       // Azure OpenAI
    schemas.Bedrock,     // AWS Bedrock
    schemas.Vertex,      // Google Vertex AI
    schemas.Cohere,      // Cohere models
    schemas.Mistral,     // Mistral models
    schemas.Ollama,      // Local Ollama
    schemas.Groq,        // Groq models
}

// Popular model choices
openAIModels := []string{
    "gpt-4o-mini",           // Fast, cost-effective
    "gpt-4o",                // Most capable
    "gpt-3.5-turbo",         // Legacy, still good
}

anthropicModels := []string{
    "claude-3-haiku-20240307",   // Fastest
    "claude-3-sonnet-20240229",  // Balanced
    "claude-3-opus-20240229",    // Most capable
}
```

### **Request Building Patterns**

Common request patterns:

```go
// Simple chat
func simpleChat(message string) *schemas.BifrostRequest {
    return &schemas.BifrostRequest{
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
    }
}

// Conversation with system prompt
func conversationWithSystem(systemPrompt, userMessage string) *schemas.BifrostRequest {
    return &schemas.BifrostRequest{
        Provider: schemas.Anthropic,
        Model:    "claude-3-sonnet-20240229",
        Input: schemas.RequestInput{
            ChatCompletionInput: &[]schemas.BifrostMessage{
                {
                    Role: schemas.ModelChatMessageRoleSystem,
                    Content: schemas.MessageContent{ContentStr: &systemPrompt},
                },
                {
                    Role: schemas.ModelChatMessageRoleUser,
                    Content: schemas.MessageContent{ContentStr: &userMessage},
                },
            },
        },
        Params: &schemas.ModelParameters{
            Temperature: &[]float64{0.7}[0],
            MaxTokens:   &[]int{1000}[0],
        },
    }
}

// With fallbacks
func reliableRequest(message string) *schemas.BifrostRequest {
    return &schemas.BifrostRequest{
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
        Fallbacks: []schemas.Fallback{
            {Provider: schemas.Anthropic, Model: "claude-3-haiku-20240307"},
            {Provider: schemas.Vertex, Model: "gemini-pro"},
        },
    }
}
```

---

## 📚 Related Documentation

- **[🤖 Bifrost Client](./bifrost-client.md)** - Using schemas with the client
- **[🏛️ Account Interface](./account.md)** - Account schema implementation
- **[🔌 Plugins](./plugins.md)** - Plugin schema implementation
- **[🛠️ MCP Integration](./mcp.md)** - MCP schema usage
- **[📊 Logging](./logging.md)** - Logger schema implementation

> **📖 Source Code:** For complete schema definitions and GoDoc documentation, see the [core/schemas directory](https://github.com/maximhq/bifrost/tree/main/core/schemas).
