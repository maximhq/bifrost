package api

import (
	"encoding/json"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
)

// BedrockTextRequest represents the unified request structure for Bedrock's text completion API.
// This typed struct optimizes JSON marshalling performance and supports both Anthropic and Mistral models.
type BedrockTextRequest struct {
	Prompt            string                 `json:"prompt"`                         // Required: The prompt to complete
	MaxTokensToSample *int                   `json:"max_tokens_to_sample,omitempty"` // Anthropic: Maximum tokens to generate (0-4096, default 200)
	MaxTokens         *int                   `json:"max_tokens,omitempty"`           // Mistral: Maximum tokens to generate
	Temperature       *float64               `json:"temperature,omitempty"`          // Optional: Amount of randomness (0-1, default 1)
	TopP              *float64               `json:"top_p,omitempty"`                // Optional: Nucleus sampling (0-1, default 1)
	TopK              *int                   `json:"top_k,omitempty"`                // Optional: Top K sampling (0-500, default 250)
	StopSequences     []string               `json:"stop_sequences,omitempty"`       // Optional: Sequences that cause generation to stop
	ExtraParams       map[string]interface{} `json:"-"`
}

func (r *BedrockTextRequest) MarshalJSON() ([]byte, error) {
	// Use standard marshaling when no extra params - gives us type safety and performance
	if len(r.ExtraParams) == 0 {
		type Alias BedrockTextRequest
		return sonic.Marshal((*Alias)(r))
	}

	// When ExtraParams exist, use dynamic approach with conflict detection
	result := make(map[string]interface{}, 7+len(r.ExtraParams))

	// Add all fields directly - no reflection overhead
	result["prompt"] = r.Prompt

	// Track which JSON field names are set to avoid conflicts
	setFields := make(map[string]bool)
	setFields["prompt"] = true

	if r.MaxTokensToSample != nil {
		result["max_tokens_to_sample"] = *r.MaxTokensToSample
		setFields["max_tokens_to_sample"] = true
	}
	if r.MaxTokens != nil {
		result["max_tokens"] = *r.MaxTokens
		setFields["max_tokens"] = true
	}
	if r.Temperature != nil {
		result["temperature"] = *r.Temperature
		setFields["temperature"] = true
	}
	if r.TopP != nil {
		result["top_p"] = *r.TopP
		setFields["top_p"] = true
	}
	if r.TopK != nil {
		result["top_k"] = *r.TopK
		setFields["top_k"] = true
	}
	if len(r.StopSequences) > 0 {
		result["stop_sequences"] = r.StopSequences
		setFields["stop_sequences"] = true
	}

	// Add ExtraParams only if they don't conflict with existing fields
	for key, value := range r.ExtraParams {
		if !setFields[key] {
			result[key] = value
		}
		// Silently skip conflicting fields - this prevents overwriting typed fields
		// while still allowing unknown fields to pass through
	}

	return sonic.Marshal(result)
}

// BedrockAnthropicTextResponse represents the response structure from Bedrock's Anthropic text completion API.
// It includes the completion text and stop reason information.
type BedrockAnthropicTextResponse struct {
	Completion string `json:"completion"`  // Generated completion text
	StopReason string `json:"stop_reason"` // Reason for completion termination
	Stop       string `json:"stop"`        // Stop sequence that caused completion to stop
}

// BedrockMistralTextResponse represents the response structure from Bedrock's Mistral text completion API.
// It includes multiple output choices with their text and stop reasons.
type BedrockMistralTextResponse struct {
	Outputs []struct {
		Text       string `json:"text"`        // Generated text
		StopReason string `json:"stop_reason"` // Reason for completion termination
	} `json:"outputs"` // Array of output choices
}

// BedrockChatResponse represents the response structure from Bedrock's chat completion API.
// It includes message content, metrics, and token usage statistics.
type BedrockChatResponse struct {
	Metrics struct {
		Latency int `json:"latencyMs"` // Response latency in milliseconds
	} `json:"metrics"` // Performance metrics
	Output struct {
		Message struct {
			Content []struct {
				Text *string `json:"text"` // Message content
				// Bedrock returns a union type where either Text or ToolUse is present (mutually exclusive)
				BedrockAnthropicToolUseMessage
			} `json:"content"` // Array of message content
			Role string `json:"role"` // Role of the message sender
		} `json:"message"` // Message structure
	} `json:"output"` // Output structure
	StopReason string `json:"stopReason"` // Reason for completion termination
	Usage      struct {
		InputTokens  int `json:"inputTokens"`  // Number of input tokens used
		OutputTokens int `json:"outputTokens"` // Number of output tokens generated
		TotalTokens  int `json:"totalTokens"`  // Total number of tokens used
	} `json:"usage"` // Token usage statistics
}

type BedrockAnthropicToolUseMessage struct {
	ToolUse *BedrockAnthropicToolUse `json:"toolUse"`
}

type BedrockAnthropicToolUse struct {
	ToolUseID string                 `json:"toolUseId"`
	Name      string                 `json:"name"`
	Input     map[string]interface{} `json:"input"`
}

// BedrockError represents the error response structure from Bedrock's API.
type BedrockError struct {
	Message string `json:"message"` // Error message
}

// BedrockAnthropicSystemMessage represents a system message for Anthropic models.
type BedrockAnthropicSystemMessage struct {
	Text string `json:"text"` // System message text
}

// BedrockAnthropicTextMessage represents a text message for Anthropic models.
type BedrockAnthropicTextMessage struct {
	Type string `json:"type"` // Type of message
	Text string `json:"text"` // Message text
}

// BedrockMistralContent represents content for Mistral models.
type BedrockMistralContent struct {
	Text string `json:"text"` // Content text
}

// BedrockMistralChatMessage represents a chat message for Mistral models.
type BedrockMistralChatMessage struct {
	Role       schemas.ModelChatMessageRole `json:"role"`                   // Role of the message sender
	Content    []BedrockMistralContent      `json:"content"`                // Array of message content
	ToolCalls  *[]BedrockMistralToolCall    `json:"tool_calls,omitempty"`   // Optional tool calls
	ToolCallID *string                      `json:"tool_call_id,omitempty"` // Optional tool call ID
}

// BedrockAnthropicImageMessage represents an image message for Anthropic models.
type BedrockAnthropicImageMessage struct {
	Type  string                `json:"type"`  // Type of message
	Image BedrockAnthropicImage `json:"image"` // Image data
}

// BedrockAnthropicImage represents image data for Anthropic models.
type BedrockAnthropicImage struct {
	Format string                      `json:"format,omitempty"` // Image format
	Source BedrockAnthropicImageSource `json:"source,omitempty"` // Image source
}

// BedrockAnthropicImageSource represents the source of an image for Anthropic models.
type BedrockAnthropicImageSource struct {
	Bytes string `json:"bytes"` // Base64 encoded image data
}

// BedrockMistralToolCall represents a tool call for Mistral models.
type BedrockMistralToolCall struct {
	ID       string               `json:"id"`       // Tool call ID
	Function schemas.FunctionCall `json:"function"` // Function to call
}

// BedrockAnthropicToolCall represents a tool call for Anthropic models.
type BedrockAnthropicToolCall struct {
	ToolSpec BedrockAnthropicToolSpec `json:"toolSpec"` // Tool specification
}

// BedrockAnthropicToolSpec represents a tool specification for Anthropic models.
type BedrockAnthropicToolSpec struct {
	Name        string `json:"name"`        // Tool name
	Description string `json:"description"` // Tool description
	InputSchema struct {
		Json interface{} `json:"json"` // Input schema in JSON format
	} `json:"inputSchema"` // Input schema structure
}

// BedrockStreamMessageStartEvent is emitted when the assistant message starts.
type BedrockStreamMessageStartEvent struct {
	MessageStart struct {
		Role string `json:"role"` // e.g. "assistant"
	} `json:"messageStart"`
}

// BedrockStreamContentBlockDeltaEvent is sent for each content delta chunk (text, reasoning, tool use).
type BedrockStreamContentBlockDeltaEvent struct {
	ContentBlockDelta struct {
		Delta struct {
			Text             string          `json:"text,omitempty"`
			ReasoningContent json.RawMessage `json:"reasoningContent,omitempty"`
			ToolUse          json.RawMessage `json:"toolUse,omitempty"`
		} `json:"delta"`
		ContentBlockIndex int `json:"contentBlockIndex"`
	} `json:"contentBlockDelta"`
}

// BedrockStreamContentBlockStopEvent indicates the end of a content block.
type BedrockStreamContentBlockStopEvent struct {
	ContentBlockStop struct {
		ContentBlockIndex int `json:"contentBlockIndex"`
	} `json:"contentBlockStop"`
}

// BedrockStreamMessageStopEvent marks the end of the assistant message.
type BedrockStreamMessageStopEvent struct {
	MessageStop struct {
		StopReason string `json:"stopReason"` // e.g. "stop", "max_tokens", "tool_use"
	} `json:"messageStop"`
}

// BedrockStreamMetadataEvent contains metadata after streaming ends.
type BedrockStreamMetadataEvent struct {
	Metadata struct {
		Usage struct {
			InputTokens  int `json:"inputTokens"`
			OutputTokens int `json:"outputTokens"`
			TotalTokens  int `json:"totalTokens"`
		} `json:"usage"`
		Metrics struct {
			LatencyMs float64 `json:"latencyMs"`
		} `json:"metrics"`
	} `json:"metadata"`
}

// BedrockChatRequest represents the unified request structure for Bedrock's chat completion API.
// This typed struct optimizes JSON marshalling performance and supports various models.
type BedrockChatRequest struct {
	Messages    []BedrockMistralChatMessage `json:"messages"`              // Formatted messages
	Tools       []BedrockAnthropicToolCall  `json:"tools,omitempty"`       // Optional tool definitions
	ToolChoice  *string                     `json:"tool_choice,omitempty"` // Optional tool choice ("auto", "any", "none")
	MaxTokens   *int                        `json:"max_tokens,omitempty"`  // Maximum tokens to generate
	Temperature *float64                    `json:"temperature,omitempty"` // Sampling temperature
	TopP        *float64                    `json:"top_p,omitempty"`       // Nucleus sampling
	ExtraParams map[string]interface{}      `json:"-"`
}

func (r *BedrockChatRequest) MarshalJSON() ([]byte, error) {
	// Use standard marshaling when no extra params - gives us type safety and performance
	if len(r.ExtraParams) == 0 {
		type Alias BedrockChatRequest
		return sonic.Marshal((*Alias)(r))
	}

	// When ExtraParams exist, use dynamic approach with conflict detection
	result := make(map[string]interface{}, 6+len(r.ExtraParams))

	// Add all fields directly - no reflection overhead
	result["messages"] = r.Messages

	// Track which JSON field names are set to avoid conflicts
	setFields := make(map[string]bool)
	setFields["messages"] = true

	if r.MaxTokens != nil {
		result["max_tokens"] = *r.MaxTokens
		setFields["max_tokens"] = true
	}
	if r.Temperature != nil {
		result["temperature"] = *r.Temperature
		setFields["temperature"] = true
	}
	if r.TopP != nil {
		result["top_p"] = *r.TopP
		setFields["top_p"] = true
	}
	if r.Tools != nil {
		result["tools"] = r.Tools
		setFields["tools"] = true
	}
	if r.ToolChoice != nil {
		result["tool_choice"] = *r.ToolChoice
		setFields["tool_choice"] = true
	}

	// Add ExtraParams only if they don't conflict with existing fields
	for key, value := range r.ExtraParams {
		if !setFields[key] {
			result[key] = value
		}
		// Silently skip conflicting fields - this prevents overwriting typed fields
		// while still allowing unknown fields to pass through
	}

	return sonic.Marshal(result)
}

// BedrockTool represents a tool definition for Bedrock models.
type BedrockTool struct {
	Type     string          `json:"type"`     // Tool type (e.g., "function")
	Function BedrockFunction `json:"function"` // Function definition
}

// BedrockFunction represents a function definition for tools.
type BedrockFunction struct {
	Name        string                 `json:"name"`        // Function name
	Description string                 `json:"description"` // Function description
	Parameters  map[string]interface{} `json:"parameters"`  // Function parameters schema
}

// BedrockToolConfig represents tool configuration for Bedrock requests.
type BedrockToolConfig struct {
	Tools []BedrockAnthropicToolCall `json:"tools"` // Array of tool specifications
}

// BedrockTitanEmbeddingRequest represents the request structure for Titan embedding API.
type BedrockTitanEmbeddingRequest struct {
	InputText      string                 `json:"inputText"`                // Text to embed
	Dimensions     *int                   `json:"dimensions,omitempty"`     // Dimensions to embed
	Normalize      *bool                  `json:"normalize,omitempty"`      // Normalize the embedding
	EmbeddingTypes []interface{}          `json:"embeddingTypes,omitempty"` // Embedding types to embed
	ExtraParams    map[string]interface{} `json:"-"`
}

func (r *BedrockTitanEmbeddingRequest) MarshalJSON() ([]byte, error) {
	// Use standard marshaling when no extra params - gives us type safety and performance
	if len(r.ExtraParams) == 0 {
		type Alias BedrockTitanEmbeddingRequest
		return sonic.Marshal((*Alias)(r))
	}

	// When ExtraParams exist, use dynamic approach with conflict detection
	result := make(map[string]interface{}, 4+len(r.ExtraParams))

	// Add all fields directly - no reflection overhead
	result["inputText"] = r.InputText

	// Track which JSON field names are set to avoid conflicts
	setFields := make(map[string]bool)
	setFields["inputText"] = true

	if r.Dimensions != nil {
		result["dimensions"] = *r.Dimensions
		setFields["dimensions"] = true
	}
	if r.Normalize != nil {
		result["normalize"] = *r.Normalize
		setFields["normalize"] = true
	}
	if len(r.EmbeddingTypes) > 0 {
		result["embeddingTypes"] = r.EmbeddingTypes
		setFields["embeddingTypes"] = true
	}

	// Add ExtraParams only if they don't conflict with existing fields
	for key, value := range r.ExtraParams {
		if !setFields[key] {
			result[key] = value
		}
		// Silently skip conflicting fields - this prevents overwriting typed fields
		// while still allowing unknown fields to pass through
	}

	return sonic.Marshal(result)
}

// BedrockCohereEmbeddingRequest represents the request structure for Cohere embedding API.
type BedrockCohereEmbeddingRequest struct {
	Texts          []string               `json:"texts"`                     // Texts to embed
	InputType      string                 `json:"input_type"`                // Input type (e.g., "search_document")
	Images         []string               `json:"images,omitempty"`          // Images to embed
	Truncate       *string                `json:"truncate,omitempty"`        // Truncate the embedding
	EmbeddingTypes []string               `json:"embedding_types,omitempty"` // Embedding types to embed
	ExtraParams    map[string]interface{} `json:"-"`
}

func (r *BedrockCohereEmbeddingRequest) MarshalJSON() ([]byte, error) {
	// Use standard marshaling when no extra params - gives us type safety and performance
	if len(r.ExtraParams) == 0 {
		type Alias BedrockCohereEmbeddingRequest
		return sonic.Marshal((*Alias)(r))
	}

	// When ExtraParams exist, use dynamic approach with conflict detection
	result := make(map[string]interface{}, 5+len(r.ExtraParams))

	// Add all fields directly - no reflection overhead
	result["texts"] = r.Texts
	result["input_type"] = r.InputType

	// Track which JSON field names are set to avoid conflicts
	setFields := make(map[string]bool)
	setFields["texts"] = true
	setFields["input_type"] = true

	if r.Truncate != nil {
		result["truncate"] = *r.Truncate
		setFields["truncate"] = true
	}
	if r.Images != nil {
		result["images"] = r.Images
		setFields["images"] = true
	}
	if len(r.EmbeddingTypes) > 0 {
		result["embedding_types"] = r.EmbeddingTypes
		setFields["embedding_types"] = true
	}

	// Add ExtraParams only if they don't conflict with existing fields
	for key, value := range r.ExtraParams {
		if !setFields[key] {
			result[key] = value
		}
		// Silently skip conflicting fields - this prevents overwriting typed fields
		// while still allowing unknown fields to pass through
	}

	return sonic.Marshal(result)
}
