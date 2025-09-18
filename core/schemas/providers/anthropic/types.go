package anthropic

import (
	"encoding/json"
	"fmt"

	"github.com/maximhq/bifrost/core/schemas"
)

// ==================== REQUEST TYPES ====================

// AnthropicTextRequest represents an Anthropic text completion request
type AnthropicTextRequest struct {
	Model             string    `json:"model"`
	Prompt            string    `json:"prompt"`
	MaxTokensToSample int       `json:"max_tokens_to_sample"`
	Temperature       *float64  `json:"temperature,omitempty"`
	TopP              *float64  `json:"top_p,omitempty"`
	TopK              *int      `json:"top_k,omitempty"`
	StopSequences     *[]string `json:"stop_sequences,omitempty"`
}

// AnthropicMessageRequest represents an Anthropic messages API request
type AnthropicMessageRequest struct {
	Model         string               `json:"model"`
	MaxTokens     int                  `json:"max_tokens"`
	Messages      []AnthropicMessage   `json:"messages"`
	System        *AnthropicContent    `json:"system,omitempty"`
	Temperature   *float64             `json:"temperature,omitempty"`
	TopP          *float64             `json:"top_p,omitempty"`
	TopK          *int                 `json:"top_k,omitempty"`
	StopSequences *[]string            `json:"stop_sequences,omitempty"`
	Stream        *bool                `json:"stream,omitempty"`
	Tools         *[]AnthropicTool     `json:"tools,omitempty"`
	ToolChoice    *AnthropicToolChoice `json:"tool_choice,omitempty"`
}

// IsStreamingRequested implements the StreamingRequest interface
func (r *AnthropicMessageRequest) IsStreamingRequested() bool {
	return r.Stream != nil && *r.Stream
}

// AnthropicMessage represents a message in Anthropic format
type AnthropicMessage struct {
	Role    string           `json:"role"`    // "user", "assistant"
	Content AnthropicContent `json:"content"` // Array of content blocks
}

// AnthropicContent represents content that can be either string or array of blocks
type AnthropicContent struct {
	ContentStr    *string
	ContentBlocks *[]AnthropicContentBlock
}

// MarshalJSON implements custom JSON marshalling for AnthropicContent.
// It marshals either ContentStr or ContentBlocks directly without wrapping.
func (mc AnthropicContent) MarshalJSON() ([]byte, error) {
	// Validation: ensure only one field is set at a time
	if mc.ContentStr != nil && mc.ContentBlocks != nil {
		return nil, fmt.Errorf("both ContentStr and ContentBlocks are set; only one should be non-nil")
	}

	if mc.ContentStr != nil {
		return json.Marshal(*mc.ContentStr)
	}
	if mc.ContentBlocks != nil {
		return json.Marshal(*mc.ContentBlocks)
	}
	// If both are nil, return null
	return json.Marshal(nil)
}

// UnmarshalJSON implements custom JSON unmarshalling for AnthropicContent.
// It determines whether "content" is a string or array and assigns to the appropriate field.
func (mc *AnthropicContent) UnmarshalJSON(data []byte) error {
	// First, try to unmarshal as a direct string
	var stringContent string
	if err := json.Unmarshal(data, &stringContent); err == nil {
		mc.ContentStr = &stringContent
		return nil
	}

	// Try to unmarshal as a direct array of ContentBlock
	var arrayContent []AnthropicContentBlock
	if err := json.Unmarshal(data, &arrayContent); err == nil {
		mc.ContentBlocks = &arrayContent
		return nil
	}

	return fmt.Errorf("content field is neither a string nor an array of ContentBlock")
}

// AnthropicContentBlock represents content in Anthropic message format
type AnthropicContentBlock struct {
	Type      string                `json:"type"`                  // "text", "image", "tool_use", "tool_result", "thinking"
	Text      *string               `json:"text,omitempty"`        // For text content
	Thinking  *string               `json:"thinking,omitempty"`    // For thinking content
	ToolUseID *string               `json:"tool_use_id,omitempty"` // For tool_result content
	ID        *string               `json:"id,omitempty"`          // For tool_use content
	Name      *string               `json:"name,omitempty"`        // For tool_use content
	Input     interface{}           `json:"input,omitempty"`       // For tool_use content
	Content   *AnthropicContent     `json:"content,omitempty"`     // For tool_result content
	Source    *AnthropicImageSource `json:"source,omitempty"`      // For image content
}

// AnthropicImageSource represents image source in Anthropic format
type AnthropicImageSource struct {
	Type      string  `json:"type"`                 // "base64" or "url"
	MediaType *string `json:"media_type,omitempty"` // "image/jpeg", "image/png", etc.
	Data      *string `json:"data,omitempty"`       // Base64-encoded image data
	URL       *string `json:"url,omitempty"`        // URL of the image
}

// AnthropicImageContent represents image content in Anthropic format
type AnthropicImageContent struct {
	Type      schemas.ImageContentType `json:"type"`
	URL       string                   `json:"url"`
	MediaType string                   `json:"media_type,omitempty"`
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

// AnthropicToolChoice represents tool choice in Anthropic format
type AnthropicToolChoice struct {
	Type                   string `json:"type"`                                // "auto", "any", "tool"
	Name                   string `json:"name,omitempty"`                      // For type "tool"
	DisableParallelToolUse *bool  `json:"disable_parallel_tool_use,omitempty"` // Whether to disable parallel tool use
}

// AnthropicToolContent represents content within tool result blocks
type AnthropicToolContent struct {
	Type             string  `json:"type"`
	Title            string  `json:"title,omitempty"`
	URL              string  `json:"url,omitempty"`
	EncryptedContent string  `json:"encrypted_content,omitempty"`
	PageAge          *string `json:"page_age,omitempty"`
}

// ==================== RESPONSE TYPES ====================

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

// AnthropicChatResponse represents the response structure from Anthropic's chat completion API (legacy)
type AnthropicChatResponse struct {
	ID      string `json:"id"`   // Unique identifier for the completion
	Type    string `json:"type"` // Type of completion
	Role    string `json:"role"` // Role of the message sender
	Content []struct {
		Type     string                 `json:"type"`               // Type of content
		Text     string                 `json:"text,omitempty"`     // Text content
		Thinking string                 `json:"thinking,omitempty"` // Thinking process
		ID       string                 `json:"id"`                 // Content identifier
		Name     string                 `json:"name"`               // Name of the content
		Input    map[string]interface{} `json:"input"`              // Input parameters
	} `json:"content"` // Array of content items
	Model        string  `json:"model"`                   // Model used for the completion
	StopReason   string  `json:"stop_reason,omitempty"`   // Reason for completion termination
	StopSequence *string `json:"stop_sequence,omitempty"` // Sequence that caused completion to stop
	Usage        struct {
		InputTokens  int `json:"input_tokens"`  // Number of input tokens used
		OutputTokens int `json:"output_tokens"` // Number of output tokens generated
	} `json:"usage"` // Token usage statistics
}

// AnthropicTextResponse represents the response structure from Anthropic's text completion API
type AnthropicTextResponse struct {
	ID         string `json:"id"`         // Unique identifier for the completion
	Type       string `json:"type"`       // Type of completion
	Completion string `json:"completion"` // Generated completion text
	Model      string `json:"model"`      // Model used for the completion
	Usage      struct {
		InputTokens  int `json:"input_tokens"`  // Number of input tokens used
		OutputTokens int `json:"output_tokens"` // Number of output tokens generated
	} `json:"usage"` // Token usage statistics
}

// AnthropicUsage represents usage information in Anthropic format
type AnthropicUsage struct {
	InputTokens              int `json:"input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
	OutputTokens             int `json:"output_tokens"`
}

// ==================== STREAMING TYPES ====================

// AnthropicStreamResponse represents a single chunk in the Anthropic streaming response
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

// AnthropicStreamEvent represents a single event in the Anthropic streaming response
type AnthropicStreamEvent struct {
	Type         string                  `json:"type"`
	Message      *AnthropicStreamMessage `json:"message,omitempty"`
	Index        *int                    `json:"index,omitempty"`
	ContentBlock *AnthropicContentBlock  `json:"content_block,omitempty"`
	Delta        *AnthropicDelta         `json:"delta,omitempty"`
	Usage        *AnthropicUsage         `json:"usage,omitempty"`
	Error        *AnthropicStreamError   `json:"error,omitempty"`
}

// AnthropicStreamMessage represents the message structure in streaming events
type AnthropicStreamMessage struct {
	ID           string                  `json:"id"`
	Type         string                  `json:"type"`
	Role         string                  `json:"role"`
	Content      []AnthropicContentBlock `json:"content"`
	Model        string                  `json:"model"`
	StopReason   *string                 `json:"stop_reason,omitempty"`
	StopSequence *string                 `json:"stop_sequence,omitempty"`
	Usage        *schemas.LLMUsage       `json:"usage,omitempty"`
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

// AnthropicDelta represents incremental updates to content blocks during streaming (legacy)
type AnthropicDelta struct {
	Type         string  `json:"type"`
	Text         string  `json:"text,omitempty"`
	PartialJSON  string  `json:"partial_json,omitempty"`
	Thinking     string  `json:"thinking,omitempty"`
	Signature    string  `json:"signature,omitempty"`
	StopReason   *string `json:"stop_reason,omitempty"`
	StopSequence *string `json:"stop_sequence,omitempty"`
}

// ==================== ERROR TYPES ====================

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

// AnthropicError represents the error response structure from Anthropic's API (legacy)
type AnthropicError struct {
	Type  string `json:"type"` // always "error"
	Error struct {
		Type    string `json:"type"`    // Error type
		Message string `json:"message"` // Error message
	} `json:"error"` // Error details
}

// AnthropicStreamError represents error events in the streaming response
type AnthropicStreamError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}
