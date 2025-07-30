package api

import (
	"fmt"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
)

type AnthropicRequestConfig struct {
	URL                     string                   `json:"url"`
	AnthropicTextRequest    *AnthropicTextRequest    `json:"anthropic_text_request,omitempty"`
	AnthropicMessageRequest *AnthropicMessageRequest `json:"anthropic_message_request,omitempty"`
}

// AnthropicToolChoice represents the tool choice configuration for Anthropic's API.
// It specifies how tools should be used in the completion request.
type AnthropicToolChoice struct {
	Type                   schemas.ToolChoiceType `json:"type"`                      // Type of tool choice
	Name                   *string                `json:"name"`                      // Name of the tool to use
	DisableParallelToolUse *bool                  `json:"disable_parallel_tool_use"` // Whether to disable parallel tool use
}

// AnthropicTextResponse represents the response structure from Anthropic's text completion API.
// It includes the completion text, model information, and token usage statistics.
type AnthropicTextResponse struct {
	ID         string          `json:"id"`         // Unique identifier for the completion
	Type       string          `json:"type"`       // Type of completion
	Completion string          `json:"completion"` // Generated completion text
	Model      string          `json:"model"`      // Model used for the completion
	Usage      *AnthropicUsage `json:"usage"`      // Token usage statistics
}

// AnthropicChatResponse represents the response structure from Anthropic's chat completion API.
// It includes message content, model information, and token usage statistics.
type AnthropicChatResponse struct {
	ID           string                     `json:"id"`                      // Unique identifier for the completion
	Type         string                     `json:"type"`                    // Type of completion
	Role         string                     `json:"role"`                    // Role of the message sender
	Content      []AnthropicResponseContent `json:"content"`                 // Array of content items
	Model        string                     `json:"model"`                   // Model used for the completion
	StopReason   string                     `json:"stop_reason,omitempty"`   // Reason for completion termination
	StopSequence *string                    `json:"stop_sequence,omitempty"` // Sequence that caused completion to stop
	Usage        *AnthropicUsage            `json:"usage"`                   // Token usage statistics
}

type AnthropicResponseContent struct {
	Type     string                 `json:"type"`               // Type of content
	Text     string                 `json:"text,omitempty"`     // Text content
	Thinking string                 `json:"thinking,omitempty"` // Thinking process
	ID       string                 `json:"id"`                 // Content identifier
	Name     string                 `json:"name"`               // Name of the content
	Input    map[string]interface{} `json:"input"`              // Input parameters
}

// AnthropicStreamEvent represents a single event in the Anthropic streaming response.
// It corresponds to the various event types defined in Anthropic's Messages API streaming documentation.
type AnthropicStreamEvent struct {
	Type         string                  `json:"type"`
	Message      *AnthropicStreamMessage `json:"message,omitempty"`
	Index        *int                    `json:"index,omitempty"`
	ContentBlock *AnthropicContentBlock  `json:"content_block,omitempty"`
	Delta        *AnthropicDelta         `json:"delta,omitempty"`
	Usage        *schemas.LLMUsage       `json:"usage,omitempty"`
	Error        *AnthropicStreamError   `json:"error,omitempty"`
}

// AnthropicStreamMessage represents the message structure in streaming events.
// This appears in message_start events and contains the initial message structure.
type AnthropicStreamMessage struct {
	ID           string                  `json:"id"`
	Type         string                  `json:"type"`
	Role         string                  `json:"role"`
	Content      []AnthropicContentBlock `json:"content"`
	Model        string                  `json:"model"`
	StopReason   *string                 `json:"stop_reason"`
	StopSequence *string                 `json:"stop_sequence"`
	Usage        *schemas.LLMUsage       `json:"usage"`
}

// AnthropicContentBlock represents content in Anthropic message format
type AnthropicContentBlock struct {
	Type      string                `json:"type"`                  // "text", "image", "tool_use", "tool_result"
	Text      *string               `json:"text,omitempty"`        // For text content
	ToolUseID *string               `json:"tool_use_id,omitempty"` // For tool_result content
	ID        *string               `json:"id,omitempty"`          // For tool_use content
	Name      *string               `json:"name,omitempty"`        // For tool_use content
	Input     interface{}           `json:"input,omitempty"`       // For tool_use content
	Content   AnthropicContent      `json:"content,omitempty"`     // For tool_result content
	Source    *AnthropicImageSource `json:"source,omitempty"`      // For image content
}

// AnthropicImageSource represents image source in Anthropic format
type AnthropicImageSource struct {
	Type      string  `json:"type"`                 // "base64" or "url"
	MediaType *string `json:"media_type,omitempty"` // "image/jpeg", "image/png", etc.
	Data      *string `json:"data,omitempty"`       // Base64-encoded image data
	URL       *string `json:"url,omitempty"`        // URL of the image
}

// AnthropicToolContent represents content within tool result blocks
type AnthropicToolContent struct {
	Type             string  `json:"type"`
	Title            string  `json:"title,omitempty"`
	URL              string  `json:"url,omitempty"`
	EncryptedContent string  `json:"encrypted_content,omitempty"`
	PageAge          *string `json:"page_age,omitempty"`
}

// AnthropicDelta represents incremental updates to content blocks during streaming.
// This includes all delta types: text_delta, input_json_delta, thinking_delta, and signature_delta.
type AnthropicDelta struct {
	Type         string  `json:"type"`
	Text         string  `json:"text,omitempty"`
	PartialJSON  string  `json:"partial_json,omitempty"`
	Thinking     string  `json:"thinking,omitempty"`
	Signature    string  `json:"signature,omitempty"`
	StopReason   *string `json:"stop_reason,omitempty"`
	StopSequence *string `json:"stop_sequence,omitempty"`
}

// AnthropicStreamError represents error events in the streaming response.
type AnthropicStreamError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// AnthropicError represents the error response structure from Anthropic's API.
// It includes error type and message information.
type AnthropicError struct {
	Type  string `json:"type"` // always "error"
	Error struct {
		Type    string `json:"type"`    // Error type
		Message string `json:"message"` // Error message
	} `json:"error"` // Error details
}

// URLTypeInfo contains extracted information about a URL
type URLTypeInfo struct {
	Type                 ImageContentType
	MediaType            *string
	DataURLWithoutPrefix *string // URL without the prefix (eg data:image/png;base64,iVBORw0KGgo...)
}

// ImageContentType represents the type of image content
type ImageContentType string

const (
	ImageContentTypeBase64 ImageContentType = "base64"
	ImageContentTypeURL    ImageContentType = "url"
)

type AnthropicImageContent struct {
	Type      ImageContentType `json:"type"`
	URL       string           `json:"url"`
	MediaType string           `json:"media_type,omitempty"`
}

// AnthropicMessage represents a message in Anthropic format
type AnthropicMessage struct {
	Role    string           `json:"role"`    // "user", "assistant"
	Content AnthropicContent `json:"content"` // Array of content blocks
}

type AnthropicContent struct {
	ContentStr    *string
	ContentBlocks *[]AnthropicContentBlock
}

// AnthropicTool represents a tool in Anthropic format
type AnthropicTool struct {
	Name        string  `json:"name"`
	Type        *string `json:"type,omitempty"`
	Description string  `json:"description"`
	InputSchema *struct {
		Type       string                 `json:"type"` // "object"
		Properties map[string]interface{} `json:"properties"`
		Required   []string               `json:"required"`
	} `json:"input_schema,omitempty"`
}

// AnthropicMessageResponse represents an Anthropic messages API response
type AnthropicMessageResponse struct {
	ID           string                  `json:"id"`
	Type         string                  `json:"type"`
	Role         string                  `json:"role"`
	Content      []AnthropicContentBlock `json:"content"`
	Model        string                  `json:"model"`
	StopReason   *string                 `json:"stop_reason,omitempty"`
	StopSequence *string                 `json:"stop_sequence,omitempty"`
	Usage        *AnthropicUsage         `json:"usage,omitempty"`
}

// AnthropicUsage represents usage information in Anthropic format
type AnthropicUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// AnthropicStreamResponse represents a single chunk in the Anthropic streaming response
// This matches the format expected by Anthropic's streaming API clients
type AnthropicStreamResponse struct {
	Type         string                  `json:"type"`
	ID           *string                 `json:"id,omitempty"`
	Model        *string                 `json:"model,omitempty"`
	Index        *int                    `json:"index,omitempty"`
	Message      *AnthropicStreamMessage `json:"message,omitempty"`
	ContentBlock *AnthropicContentBlock  `json:"content_block,omitempty"`
	Delta        *AnthropicStreamDelta   `json:"delta,omitempty"`
	Usage        *AnthropicUsage         `json:"usage,omitempty"`
}

// AnthropicStreamDelta represents the incremental content in a streaming chunk
type AnthropicStreamDelta struct {
	Type         string  `json:"type"`
	Text         *string `json:"text,omitempty"`
	Thinking     *string `json:"thinking,omitempty"`
	PartialJSON  *string `json:"partial_json,omitempty"`
	StopReason   *string `json:"stop_reason,omitempty"`
	StopSequence *string `json:"stop_sequence,omitempty"`
}

// AnthropicMessageError represents an Anthropic messages API error response
type AnthropicMessageError struct {
	Type  string                      `json:"type"`  // always "error"
	Error AnthropicMessageErrorStruct `json:"error"` // Error details
}

// AnthropicMessageErrorStruct represents the error structure of an Anthropic messages API error response
type AnthropicMessageErrorStruct struct {
	Type    string `json:"type"`    // Error type
	Message string `json:"message"` // Error message
}

// AnthropicMessageRequest represents an Anthropic messages API request
type AnthropicMessageRequest struct {
	Model            string                 `json:"model"`
	MaxTokens        int                    `json:"max_tokens"`
	Messages         []AnthropicMessage     `json:"messages"`
	System           *AnthropicContent      `json:"system,omitempty"`
	Temperature      *float64               `json:"temperature,omitempty"`
	TopP             *float64               `json:"top_p,omitempty"`
	TopK             *int                   `json:"top_k,omitempty"`
	StopSequences    []string               `json:"stop_sequences,omitempty"`
	Stream           *bool                  `json:"stream,omitempty"`
	Tools            []AnthropicTool        `json:"tools,omitempty"`
	ToolChoice       *AnthropicToolChoice   `json:"tool_choice,omitempty"`
	AnthropicVersion *string                `json:"anthropic_version,omitempty"`
	Region           *string                `json:"region,omitempty"`
	ExtraParams      map[string]interface{} `json:"-"`
}

func (mr *AnthropicMessageRequest) MarshalJSON() ([]byte, error) {
	// Use standard marshaling when no extra params - gives us type safety and performance
	if len(mr.ExtraParams) == 0 {
		type Alias AnthropicMessageRequest
		return sonic.Marshal((*Alias)(mr))
	}

	// When ExtraParams exist, use dynamic approach with conflict detection
	result := make(map[string]interface{}, 13+len(mr.ExtraParams))

	// Add all fields directly - no reflection overhead
	result["model"] = mr.Model
	result["max_tokens"] = mr.MaxTokens
	result["messages"] = mr.Messages
	result["system"] = mr.System

	// Track which JSON field names are set to avoid conflicts
	setFields := make(map[string]bool)
	setFields["model"] = true
	setFields["max_tokens"] = true
	setFields["messages"] = true
	setFields["system"] = true

	if mr.Temperature != nil {
		result["temperature"] = *mr.Temperature
		setFields["temperature"] = true
	}
	if mr.TopP != nil {
		result["top_p"] = *mr.TopP
		setFields["top_p"] = true
	}
	if mr.TopK != nil {
		result["top_k"] = *mr.TopK
		setFields["top_k"] = true
	}
	if mr.StopSequences != nil {
		result["stop_sequences"] = mr.StopSequences
		setFields["stop_sequences"] = true
	}
	if mr.Stream != nil {
		result["stream"] = *mr.Stream
		setFields["stream"] = true
	}
	if mr.Tools != nil {
		result["tools"] = mr.Tools
		setFields["tools"] = true
	}
	if mr.ToolChoice != nil {
		result["tool_choice"] = mr.ToolChoice
		setFields["tool_choice"] = true
	}
	if mr.AnthropicVersion != nil {
		result["anthropic_version"] = *mr.AnthropicVersion
		setFields["anthropic_version"] = true
	}
	if mr.Region != nil {
		result["region"] = *mr.Region
		setFields["region"] = true
	}

	// Add ExtraParams only if they don't conflict with existing fields
	for key, value := range mr.ExtraParams {
		if !setFields[key] {
			result[key] = value
		}
		// Silently skip conflicting fields - this prevents overwriting typed fields
		// while still allowing unknown fields to pass through
	}

	return sonic.Marshal(result)
}

// AnthropicTextRequest represents an Anthropic text completion API request
type AnthropicTextRequest struct {
	Model             string                 `json:"model"`                          // Required: Model identifier
	Prompt            string                 `json:"prompt"`                         // Required: Text prompt for completion
	MaxTokensToSample int                    `json:"max_tokens_to_sample,omitempty"` // Optional: Maximum tokens to generate
	Temperature       *float64               `json:"temperature,omitempty"`          // Optional: Sampling temperature (0-1)
	TopP              *float64               `json:"top_p,omitempty"`                // Optional: Nucleus sampling (0-1)
	TopK              *int                   `json:"top_k,omitempty"`                // Optional: Top K sampling
	StopSequences     []string               `json:"stop_sequences,omitempty"`       // Optional: Sequences that stop generation
	Stream            *bool                  `json:"stream,omitempty"`               // Optional: Enable streaming
	ExtraParams       map[string]interface{} `json:"-"`
}


func (r *AnthropicTextRequest) MarshalJSON() ([]byte, error) {
	// Use standard marshaling when no extra params - gives us type safety and performance
	if len(r.ExtraParams) == 0 {
		type Alias AnthropicTextRequest
		return sonic.Marshal((*Alias)(r))
	}

	// When ExtraParams exist, use dynamic approach with conflict detection
	result := make(map[string]interface{}, 8+len(r.ExtraParams))

	result["model"] = r.Model
	result["prompt"] = r.Prompt
	result["max_tokens_to_sample"] = r.MaxTokensToSample

	// Track which JSON field names are set to avoid conflicts
	setFields := make(map[string]bool)
	setFields["model"] = true
	setFields["prompt"] = true
	setFields["max_tokens_to_sample"] = true

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
	if r.StopSequences != nil {
		result["stop_sequences"] = r.StopSequences
		setFields["stop_sequences"] = true
	}
	if r.Stream != nil {
		result["stream"] = *r.Stream
		setFields["stream"] = true
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

// IsStreamingRequested implements the StreamingRequest interface
func (r *AnthropicMessageRequest) IsStreamingRequested() bool {
	return r.Stream != nil && *r.Stream
}

// MarshalJSON implements custom JSON marshalling for MessageContent.
// It marshals either ContentStr or ContentBlocks directly without wrapping.
func (mc AnthropicContent) MarshalJSON() ([]byte, error) {
	// Validation: ensure only one field is set at a time
	if mc.ContentStr != nil && mc.ContentBlocks != nil {
		return nil, fmt.Errorf("both ContentStr and ContentBlocks are set; only one should be non-nil")
	}

	if mc.ContentStr != nil {
		return sonic.Marshal(*mc.ContentStr)
	}
	if mc.ContentBlocks != nil {
		return sonic.Marshal(*mc.ContentBlocks)
	}
	// If both are nil, return null
	return sonic.Marshal(nil)
}

// UnmarshalJSON implements custom JSON unmarshalling for MessageContent.
// It determines whether "content" is a string or array and assigns to the appropriate field.
// It also handles direct string/array content without a wrapper object.
func (mc *AnthropicContent) UnmarshalJSON(data []byte) error {
	// First, try to unmarshal as a direct string
	var stringContent string
	if err := sonic.Unmarshal(data, &stringContent); err == nil {
		mc.ContentStr = &stringContent
		return nil
	}

	// Try to unmarshal as a direct array of ContentBlock
	var arrayContent []AnthropicContentBlock
	if err := sonic.Unmarshal(data, &arrayContent); err == nil {
		mc.ContentBlocks = &arrayContent
		return nil
	}

	return fmt.Errorf("content field is neither a string nor an array of ContentBlock")
}
