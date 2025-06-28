# 📋 Bifrost Go Package Schemas

Complete reference for all data structures and interfaces in the Bifrost Go package, based on actual schema definitions from `core/schemas/`.

## 📑 Schema Categories

| Category                                                  | Description                           | Key Types                                             |
| --------------------------------------------------------- | ------------------------------------- | ----------------------------------------------------- |
| **[Core Request/Response](#-core-requestresponse-types)** | Essential request/response structures | `BifrostRequest`, `BifrostResponse`, `BifrostMessage` |
| **[Configuration](#-configuration-schemas)**              | Account and provider configuration    | `BifrostConfig`, `Account`, `ProviderConfig`          |
| **[Provider System](#-provider-interfaces)**              | Provider implementation contracts     | `Provider`, `MetaConfig`, `Key`                       |
| **[Plugin System](#-plugin-schemas)**                     | Plugin development interfaces         | `Plugin`, `PluginConfig`                              |
| **[MCP Integration](#-mcp-schemas)**                      | Model Context Protocol types          | `MCPConfig`, `ClientConfig`                           |
| **[Error Handling](#-error-types)**                       | Error structures and types            | `BifrostError`, `ErrorField`                          |

---

## 🏗️ Core Request/Response Types

### **BifrostRequest**

Main request structure for all model interactions.

```go
type BifrostRequest struct {
    Provider  ModelProvider `json:"provider"`          // AI provider (openai, anthropic, etc.)
    Model     string        `json:"model"`             // Model identifier
    Input     RequestInput  `json:"input"`             // Input data (text or chat)
    Params    *ModelParameters `json:"params,omitempty"` // Model parameters
    Fallbacks []Fallback    `json:"fallbacks,omitempty"` // Fallback providers
}
```

**Supported Providers:**

```go
const (
    OpenAI    ModelProvider = "openai"
    Azure     ModelProvider = "azure"
    Anthropic ModelProvider = "anthropic"
    Bedrock   ModelProvider = "bedrock"
    Cohere    ModelProvider = "cohere"
    Vertex    ModelProvider = "vertex"
    Mistral   ModelProvider = "mistral"
    Ollama    ModelProvider = "ollama"
)
```

**Usage Example:**

```go
request := &schemas.BifrostRequest{
    Provider: schemas.OpenAI,
    Model:    "gpt-4o-mini",
    Input: schemas.RequestInput{
        ChatCompletionInput: &[]schemas.BifrostMessage{
            {Role: schemas.ModelChatMessageRoleSystem, Content: schemas.MessageContent{ContentStr: &systemPrompt}},
            {Role: schemas.ModelChatMessageRoleUser, Content: schemas.MessageContent{ContentStr: &userMessage}},
        },
    },
    Params: &schemas.ModelParameters{
        MaxTokens:   &maxTokens,
        Temperature: &temperature,
    },
}
```

### **RequestInput**

Input data for requests - supports both text and chat completions.

```go
type RequestInput struct {
    TextCompletionInput *string            `json:"text_completion_input,omitempty"`
    ChatCompletionInput *[]BifrostMessage  `json:"chat_completion_input,omitempty"`
}
```

### **BifrostMessage**

Message structure for chat conversations.

```go
type BifrostMessage struct {
    Role    ModelChatMessageRole `json:"role"`
    Content MessageContent       `json:"content"`

    // Optional embedded structs (only one should be non-nil)
    *ToolMessage      // For tool response messages
    *AssistantMessage // For assistant messages with tool calls
}
```

**Message Roles:**

```go
const (
    ModelChatMessageRoleAssistant ModelChatMessageRole = "assistant"
    ModelChatMessageRoleUser      ModelChatMessageRole = "user"
    ModelChatMessageRoleSystem    ModelChatMessageRole = "system"
    ModelChatMessageRoleTool      ModelChatMessageRole = "tool"
)
```

### **MessageContent**

Flexible content that supports both simple text and structured content (with images).

```go
type MessageContent struct {
    ContentStr    *string        // Simple text content
    ContentBlocks *[]ContentBlock // Structured content (text + images)
}
```

**Content Block Types:**

```go
type ContentBlock struct {
    Type     ContentBlockType `json:"type"`
    Text     *string          `json:"text,omitempty"`
    ImageURL *ImageURLStruct  `json:"image_url,omitempty"`
}

const (
    ContentBlockTypeText     ContentBlockType = "text"
    ContentBlockTypeImageURL ContentBlockType = "image_url"
)
```

**Usage Examples:**

```go
// Simple text message
textMessage := schemas.BifrostMessage{
    Role: schemas.ModelChatMessageRoleUser,
    Content: schemas.MessageContent{
        ContentStr: &[]string{"Hello, world!"}[0],
    },
}

// Structured message with image
structuredMessage := schemas.BifrostMessage{
    Role: schemas.ModelChatMessageRoleUser,
    Content: schemas.MessageContent{
        ContentBlocks: &[]schemas.ContentBlock{
            {Type: schemas.ContentBlockTypeText, Text: &[]string{"What's in this image?"}[0]},
            {Type: schemas.ContentBlockTypeImageURL, ImageURL: &schemas.ImageURLStruct{
                URL: "https://example.com/image.jpg",
                Detail: &[]string{"high"}[0],
            }},
        },
    },
}
```

### **ModelParameters**

Comprehensive parameters for model configuration.

```go
type ModelParameters struct {
    ToolChoice        *ToolChoice `json:"tool_choice,omitempty"`
    Tools             *[]Tool     `json:"tools,omitempty"`
    Temperature       *float64    `json:"temperature,omitempty"`
    TopP              *float64    `json:"top_p,omitempty"`
    TopK              *int        `json:"top_k,omitempty"`
    MaxTokens         *int        `json:"max_tokens,omitempty"`
    StopSequences     *[]string   `json:"stop_sequences,omitempty"`
    PresencePenalty   *float64    `json:"presence_penalty,omitempty"`
    FrequencyPenalty  *float64    `json:"frequency_penalty,omitempty"`
    ParallelToolCalls *bool       `json:"parallel_tool_calls,omitempty"`
    ExtraParams       map[string]interface{} `json:"-"` // Provider-specific params
}
```

### **Tool & ToolChoice**

Tool calling support for function execution.

```go
type Tool struct {
    ID       *string  `json:"id,omitempty"`
    Type     string   `json:"type"`         // "function"
    Function Function `json:"function"`
}

type Function struct {
    Name        string             `json:"name"`
    Description string             `json:"description"`
    Parameters  FunctionParameters `json:"parameters"`
}

type ToolChoice struct {
    ToolChoiceStr    *string           // "auto", "none", "required"
    ToolChoiceStruct *ToolChoiceStruct // Specific function selection
}

type ToolChoiceStruct struct {
    Type     ToolChoiceType     `json:"type"`
    Function ToolChoiceFunction `json:"function,omitempty"`
}
```

### **BifrostResponse**

Complete response structure with provider metadata.

```go
type BifrostResponse struct {
    ID                string                     `json:"id,omitempty"`
    Object            string                     `json:"object,omitempty"`
    Choices           []BifrostResponseChoice    `json:"choices,omitempty"`
    Model             string                     `json:"model,omitempty"`
    Created           int                        `json:"created,omitempty"`
    ServiceTier       *string                    `json:"service_tier,omitempty"`
    SystemFingerprint *string                    `json:"system_fingerprint,omitempty"`
    Usage             LLMUsage                   `json:"usage,omitempty"`
    ExtraFields       BifrostResponseExtraFields `json:"extra_fields"`
}

type BifrostResponseChoice struct {
    Index        int            `json:"index"`
    Message      BifrostMessage `json:"message"`
    FinishReason *string        `json:"finish_reason,omitempty"`
    StopString   *string        `json:"stop,omitempty"`
    LogProbs     *LogProbs      `json:"log_probs,omitempty"`
}

type BifrostResponseExtraFields struct {
    Provider    ModelProvider     `json:"provider"`
    Params      ModelParameters   `json:"model_params"`
    Latency     *float64          `json:"latency,omitempty"`
    ChatHistory *[]BifrostMessage `json:"chat_history,omitempty"`
    BilledUsage *BilledLLMUsage   `json:"billed_usage,omitempty"`
    RawResponse interface{}       `json:"raw_response"`
}
```

---

## ⚙️ Configuration Schemas

### **BifrostConfig**

Main configuration for initializing Bifrost.

```go
type BifrostConfig struct {
    Account            Account      // Account interface implementation
    Plugins            []Plugin     // Plugin instances
    Logger             Logger       // Logger interface implementation
    InitialPoolSize    int          // Object pool size (default: 100)
    DropExcessRequests bool         // Drop vs. queue excess requests
    MCPConfig          *MCPConfig   // MCP configuration
}
```

### **Account Interface**

Interface for managing provider configurations.

```go
type Account interface {
    // GetConfiguredProviders returns available providers
    GetConfiguredProviders() ([]ModelProvider, error)

    // GetKeysForProvider returns API keys for a provider
    GetKeysForProvider(providerKey ModelProvider) ([]Key, error)

    // GetConfigForProvider returns provider configuration
    GetConfigForProvider(providerKey ModelProvider) (*ProviderConfig, error)
}
```

### **Key**

API key configuration with load balancing.

```go
type Key struct {
    Value  string   `json:"value"`  // API key value (supports "env.VAR_NAME")
    Models []string `json:"models"` // Supported models
    Weight float64  `json:"weight"` // Load balancing weight (0.0-1.0)
}
```

### **ProviderConfig**

Complete provider configuration.

```go
type ProviderConfig struct {
    NetworkConfig            NetworkConfig            `json:"network_config"`
    MetaConfig               MetaConfig               `json:"meta_config,omitempty"`
    ConcurrencyAndBufferSize ConcurrencyAndBufferSize `json:"concurrency_and_buffer_size"`
    Logger                   Logger                   `json:"logger"`
    ProxyConfig              *ProxyConfig             `json:"proxy_config,omitempty"`
}
```

### **NetworkConfig**

Network settings for provider connections.

```go
type NetworkConfig struct {
    BaseURL                        string            `json:"base_url,omitempty"`
    ExtraHeaders                   map[string]string `json:"extra_headers,omitempty"`
    DefaultRequestTimeoutInSeconds int               `json:"default_request_timeout_in_seconds"`
    MaxRetries                     int               `json:"max_retries"`
    RetryBackoffInitial            time.Duration     `json:"retry_backoff_initial"`
    RetryBackoffMax                time.Duration     `json:"retry_backoff_max"`
}
```

### **ConcurrencyAndBufferSize**

Concurrency and buffering configuration.

```go
type ConcurrencyAndBufferSize struct {
    Concurrency int `json:"concurrency"` // Number of workers (default: 10)
    BufferSize  int `json:"buffer_size"` // Queue buffer size (default: 100)
}
```

### **ProxyConfig**

Proxy configuration for network requests.

```go
type ProxyConfig struct {
    Type     ProxyType `json:"type"`     // "none", "http", "socks5", "environment"
    URL      string    `json:"url"`      // Proxy URL
    Username string    `json:"username"` // Auth username
    Password string    `json:"password"` // Auth password
}
```

---

## 🔌 Provider Interfaces

### **Provider Interface**

Interface for implementing AI providers.

```go
type Provider interface {
    GetProviderKey() ModelProvider

    TextCompletion(ctx context.Context, model, key, text string,
                  params *ModelParameters) (*BifrostResponse, *BifrostError)

    ChatCompletion(ctx context.Context, model, key string,
                  messages []BifrostMessage, params *ModelParameters) (*BifrostResponse, *BifrostError)
}
```

### **MetaConfig Interface**

Provider-specific configuration interface.

```go
type MetaConfig interface {
    GetSecretAccessKey() *string      // AWS/Bedrock secret key
    GetRegion() *string               // Provider region
    GetSessionToken() *string         // Session token
    GetARN() *string                  // Amazon Resource Name
    GetInferenceProfiles() map[string]string // Inference profiles
    GetEndpoint() *string             // Custom endpoint
    GetDeployments() map[string]string // Azure deployments
    GetAPIVersion() *string           // API version
    GetProjectID() *string            // Google Cloud project ID
    GetAuthCredentials() *string      // Auth credentials
}
```

---

## 🔌 Plugin Schemas

### **Plugin Interface**

Interface for implementing plugins.

```go
type Plugin interface {
    GetName() string

    PreHook(ctx *context.Context, req *BifrostRequest) (*BifrostRequest, *PluginShortCircuit, error)

    PostHook(ctx *context.Context, result *BifrostResponse, err *BifrostError) (*BifrostResponse, *BifrostError, error)

    Cleanup() error
}
```

### **PluginShortCircuit**

Short-circuit response for plugins.

```go
type PluginShortCircuit struct {
    Response *BifrostResponse `json:"response,omitempty"`
    Error    *BifrostError    `json:"error,omitempty"`
}
```

---

## 🛠️ MCP Schemas

### **MCPConfig**

Model Context Protocol configuration.

```go
type MCPConfig struct {
    ClientConfigs []ClientConfig `json:"client_configs"`
}

type ClientConfig struct {
    Name           string       `json:"name"`
    ConnectionType string       `json:"connection_type"` // "stdio", "http", "sse"

    // For STDIO connections
    StdioConfig *StdioConfig `json:"stdio_config,omitempty"`

    // For HTTP/SSE connections
    ConnectionString string `json:"connection_string,omitempty"`

    // Tool filtering
    ToolsToSkip    []string `json:"tools_to_skip"`
    ToolsToExecute []string `json:"tools_to_execute"`
}

type StdioConfig struct {
    Command string   `json:"command"`
    Args    []string `json:"args"`
    Envs    []string `json:"envs"` // Environment variables required
}
```

---

## ❌ Error Types

### **BifrostError**

Comprehensive error structure.

```go
type BifrostError struct {
    EventID        *string    `json:"event_id,omitempty"`
    Type           *string    `json:"type,omitempty"`
    IsBifrostError bool       `json:"is_bifrost_error"`
    StatusCode     *int       `json:"status_code,omitempty"`
    Error          ErrorField `json:"error"`
}

type ErrorField struct {
    Type    *string     `json:"type,omitempty"`
    Code    *string     `json:"code,omitempty"`
    Message string      `json:"message"`
    Error   error       `json:"error,omitempty"`
    Param   interface{} `json:"param,omitempty"`
    EventID *string     `json:"event_id,omitempty"`
}
```

---

## 🚀 Usage Examples

### **Complete Setup Example**

```go
package main

import (
    "context"
    "github.com/maximhq/bifrost/core"
    "github.com/maximhq/bifrost/core/schemas"
)

type MyAccount struct {
    providers map[string]schemas.ProviderConfig
}

func (a *MyAccount) GetConfiguredProviders() ([]schemas.ModelProvider, error) {
    return []schemas.ModelProvider{schemas.OpenAI, schemas.Anthropic}, nil
}

func (a *MyAccount) GetKeysForProvider(provider schemas.ModelProvider) ([]schemas.Key, error) {
    switch provider {
    case schemas.OpenAI:
        return []schemas.Key{
            {Value: "sk-your-key", Models: []string{"gpt-4o-mini"}, Weight: 1.0},
        }, nil
    case schemas.Anthropic:
        return []schemas.Key{
            {Value: "your-anthropic-key", Models: []string{"claude-3-sonnet-20240229"}, Weight: 1.0},
        }, nil
    }
    return nil, nil
}

func (a *MyAccount) GetConfigForProvider(provider schemas.ModelProvider) (*schemas.ProviderConfig, error) {
    return &schemas.ProviderConfig{
        NetworkConfig: schemas.DefaultNetworkConfig,
        ConcurrencyAndBufferSize: schemas.DefaultConcurrencyAndBufferSize,
    }, nil
}

func main() {
    account := &MyAccount{}

    config := schemas.BifrostConfig{
        Account:            account,
        InitialPoolSize:    10000,
        DropExcessRequests: false,
    }

    client, err := bifrost.Init(config)
    if err != nil {
        panic(err)
    }
    defer client.Cleanup()

    // Make a request
    request := &schemas.BifrostRequest{
        Provider: schemas.OpenAI,
        Model:    "gpt-4o-mini",
        Input: schemas.RequestInput{
            ChatCompletionInput: &[]schemas.BifrostMessage{
                {
                    Role: schemas.ModelChatMessageRoleUser,
                    Content: schemas.MessageContent{
                        ContentStr: &[]string{"Hello, world!"}[0],
                    },
                },
            },
        },
        Params: &schemas.ModelParameters{
            MaxTokens:   &[]int{100}[0],
            Temperature: &[]float64{0.7}[0],
        },
    }

    response, err := client.Request(context.Background(), request)
    if err != nil {
        panic(err)
    }

    // Access response
    if len(response.Choices) > 0 {
        message := response.Choices[0].Message
        if message.Content.ContentStr != nil {
            println(*message.Content.ContentStr)
        }
    }
}
```

---

_For implementation details and advanced usage patterns, see the main [Go Package API Reference](README.md)._
