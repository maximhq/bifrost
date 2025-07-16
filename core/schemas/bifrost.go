// Package schemas defines the core schemas and types used by the Bifrost system.
package schemas

import (
	"encoding/json"
	"fmt"
)

const (
	DefaultInitialPoolSize = 100
)

// BifrostConfig represents the configuration for initializing a Bifrost instance.
// It contains the necessary components for setting up the system including account details,
// plugins, logging, and initial pool size.
type BifrostConfig struct {
	Account            Account
	Plugins            []Plugin
	Logger             Logger
	InitialPoolSize    int        // Initial pool size for sync pools in Bifrost. Higher values will reduce memory allocations but will increase memory usage.
	DropExcessRequests bool       // If true, in cases where the queue is full, requests will not wait for the queue to be empty and will be dropped instead.
	MCPConfig          *MCPConfig // MCP (Model Context Protocol) configuration for tool integration
}

// ModelChatMessageRole represents the role of a chat message
type ModelChatMessageRole string

const (
	ModelChatMessageRoleAssistant ModelChatMessageRole = "assistant"
	ModelChatMessageRoleUser      ModelChatMessageRole = "user"
	ModelChatMessageRoleSystem    ModelChatMessageRole = "system"
	ModelChatMessageRoleChatbot   ModelChatMessageRole = "chatbot"
	ModelChatMessageRoleTool      ModelChatMessageRole = "tool"
)

// ModelProvider represents the different AI model providers supported by Bifrost.
type ModelProvider string

const (
	OpenAI    ModelProvider = "openai"
	Azure     ModelProvider = "azure"
	Anthropic ModelProvider = "anthropic"
	Bedrock   ModelProvider = "bedrock"
	Cohere    ModelProvider = "cohere"
	Vertex    ModelProvider = "vertex"
	Mistral   ModelProvider = "mistral"
	Ollama    ModelProvider = "ollama"
	Groq      ModelProvider = "groq"
	SGL       ModelProvider = "sgl"
)

//* Request Structs

// RequestInput represents the input for a model request, which can be either
// a text completion, a chat completion, or an embedding request.
type RequestInput struct {
	TextCompletionInput *string           `json:"text_completion_input,omitempty"`
	ChatCompletionInput *[]BifrostMessage `json:"chat_completion_input,omitempty"`
	EmbeddingInput      *EmbeddingInput   `json:"embedding_input,omitempty"`
}

// EmbeddingInput represents the input for an embedding request.
type EmbeddingInput struct {
	Texts []string `json:"texts"`
}

// BifrostRequest represents a request to be processed by Bifrost.
// It must be provided when calling the Bifrost for text completion, chat completion, or embedding.
// It contains the model identifier, input data, and parameters for the request.
type BifrostRequest struct {
	Provider ModelProvider    `json:"provider"`
	Model    string           `json:"model"`
	Input    RequestInput     `json:"input"`
	Params   *ModelParameters `json:"params,omitempty"`

	// Fallbacks are tried in order, the first one to succeed is returned
	// Provider config must be available for each fallback's provider in account's GetConfigForProvider,
	// else it will be skipped.
	Fallbacks []Fallback `json:"fallbacks,omitempty"`
}

// Fallback represents a fallback model to be used if the primary model is not available.
type Fallback struct {
	Provider ModelProvider `json:"provider"`
	Model    string        `json:"model"`
}

// ModelParameters represents the parameters that can be used to configure
// your request to the model. Bifrost follows a standard set of parameters which
// mapped to the provider's parameters.
type ModelParameters struct {
	ToolChoice        *ToolChoice `json:"tool_choice,omitempty"`         // Whether to call a tool
	Tools             *[]Tool     `json:"tools,omitempty"`               // Tools to use
	Temperature       *float64    `json:"temperature,omitempty"`         // Controls randomness in the output
	TopP              *float64    `json:"top_p,omitempty"`               // Controls diversity via nucleus sampling
	TopK              *int        `json:"top_k,omitempty"`               // Controls diversity via top-k sampling
	MaxTokens         *int        `json:"max_tokens,omitempty"`          // Maximum number of tokens to generate
	StopSequences     *[]string   `json:"stop_sequences,omitempty"`      // Sequences that stop generation
	PresencePenalty   *float64    `json:"presence_penalty,omitempty"`    // Penalizes repeated tokens
	FrequencyPenalty  *float64    `json:"frequency_penalty,omitempty"`   // Penalizes frequent tokens
	ParallelToolCalls *bool       `json:"parallel_tool_calls,omitempty"` // Enables parallel tool calls
	EncodingFormat    *string     `json:"encoding_format,omitempty"`     // Format for embedding output (e.g., "float", "base64")
	Dimensions        *int        `json:"dimensions,omitempty"`          // Number of dimensions for embedding output
	User              *string     `json:"user,omitempty"`                // User identifier for tracking
	// Dynamic parameters that can be provider-specific, they are directly
	// added to the request as is.
	ExtraParams map[string]interface{} `json:"-"`
}

// FunctionParameters represents the parameters for a function definition.
type FunctionParameters struct {
	Type        string                 `json:"type"`                  // Type of the parameters
	Description *string                `json:"description,omitempty"` // Description of the parameters
	Required    []string               `json:"required,omitempty"`    // Required parameter names
	Properties  map[string]interface{} `json:"properties,omitempty"`  // Parameter properties
	Enum        *[]string              `json:"enum,omitempty"`        // Enum values for the parameters
}

// Function represents a function that can be called by the model.
type Function struct {
	Name        string             `json:"name"`        // Name of the function
	Description string             `json:"description"` // Description of the function
	Parameters  FunctionParameters `json:"parameters"`  // Parameters of the function
}

// Tool represents a tool that can be used with the model.
type Tool struct {
	ID       *string  `json:"id,omitempty"` // Optional tool identifier
	Type     string   `json:"type"`         // Type of the tool
	Function Function `json:"function"`     // Function definition
}

// Combined tool choices for all providers, make sure to check the provider's
// documentation to see which tool choices are supported.
type ToolChoiceType string

const (
	// ToolChoiceTypeNone means no tool will be called
	ToolChoiceTypeNone ToolChoiceType = "none"
	// ToolChoiceTypeAuto means the model can choose whether to call a tool
	ToolChoiceTypeAuto ToolChoiceType = "auto"
	// ToolChoiceTypeAny means any tool can be called
	ToolChoiceTypeAny ToolChoiceType = "any"
	// ToolChoiceTypeFunction means a specific tool must be called (converted to "tool" for Anthropic)
	ToolChoiceTypeFunction ToolChoiceType = "function"
	// ToolChoiceTypeRequired means a tool must be called
	ToolChoiceTypeRequired ToolChoiceType = "required"
)

// ToolChoiceFunction represents a specific function to be called.
type ToolChoiceFunction struct {
	Name string `json:"name"` // Name of the function to call
}

// ToolChoiceStruct represents a specific tool choice.
type ToolChoiceStruct struct {
	Type     ToolChoiceType     `json:"type"`               // Type of tool choice
	Function ToolChoiceFunction `json:"function,omitempty"` // Function to call if type is ToolChoiceTypeFunction
}

// ToolChoice represents how a tool should be chosen for a request. (either a string or a struct)
type ToolChoice struct {
	ToolChoiceStr    *string
	ToolChoiceStruct *ToolChoiceStruct
}

// MarshalJSON implements custom JSON marshalling for ToolChoice.
// It marshals either ToolChoiceStr or ToolChoiceStruct directly without wrapping.
func (tc ToolChoice) MarshalJSON() ([]byte, error) {
	// Validation: ensure only one field is set at a time
	if tc.ToolChoiceStr != nil && tc.ToolChoiceStruct != nil {
		return nil, fmt.Errorf("both ToolChoiceStr and ToolChoiceStruct are set; only one should be non-nil")
	}

	if tc.ToolChoiceStr != nil {
		return json.Marshal(*tc.ToolChoiceStr)
	}
	if tc.ToolChoiceStruct != nil {
		return json.Marshal(*tc.ToolChoiceStruct)
	}
	// If both are nil, return null
	return json.Marshal(nil)
}

// UnmarshalJSON implements custom JSON unmarshalling for ToolChoice.
// It determines whether "tool_choice" is a string or struct and assigns to the appropriate field.
// It also handles direct string/array content without a wrapper object.
func (tc *ToolChoice) UnmarshalJSON(data []byte) error {
	// First, try to unmarshal as a direct string
	var stringContent string
	if err := json.Unmarshal(data, &stringContent); err == nil {
		tc.ToolChoiceStr = &stringContent
		return nil
	}

	// Try to unmarshal as a direct struct of ToolChoiceStruct
	var toolChoiceStruct ToolChoiceStruct
	if err := json.Unmarshal(data, &toolChoiceStruct); err == nil {
		// Validate the Type field is not empty and is a valid value
		if toolChoiceStruct.Type == "" {
			return fmt.Errorf("tool_choice struct has empty type field")
		}

		tc.ToolChoiceStruct = &toolChoiceStruct
		return nil
	}

	return fmt.Errorf("tool_choice field is neither a string nor a struct")
}

// BifrostMessage represents a message in a chat conversation.
type BifrostMessage struct {
	Role    ModelChatMessageRole `json:"role"`
	Content MessageContent       `json:"content"`

	// Embedded pointer structs - when non-nil, their exported fields are flattened into the top-level JSON object
	// IMPORTANT: Only one of the following can be non-nil at a time, otherwise the JSON marshalling will override the common fields
	*ToolMessage
	*AssistantMessage
}

type MessageContent struct {
	ContentStr    *string
	ContentBlocks *[]ContentBlock
}

// MarshalJSON implements custom JSON marshalling for MessageContent.
// It marshals either ContentStr or ContentBlocks directly without wrapping.
func (mc MessageContent) MarshalJSON() ([]byte, error) {
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

// UnmarshalJSON implements custom JSON unmarshalling for MessageContent.
// It determines whether "content" is a string or array and assigns to the appropriate field.
// It also handles direct string/array content without a wrapper object.
func (mc *MessageContent) UnmarshalJSON(data []byte) error {
	// First, try to unmarshal as a direct string
	var stringContent string
	if err := json.Unmarshal(data, &stringContent); err == nil {
		mc.ContentStr = &stringContent
		return nil
	}

	// Try to unmarshal as a direct array of ContentBlock
	var arrayContent []ContentBlock
	if err := json.Unmarshal(data, &arrayContent); err == nil {
		mc.ContentBlocks = &arrayContent
		return nil
	}

	return fmt.Errorf("content field is neither a string nor an array of ContentBlock")
}

type ContentBlockType string

const (
	ContentBlockTypeText  ContentBlockType = "text"
	ContentBlockTypeImage ContentBlockType = "image_url"
)

type ContentBlock struct {
	Type     ContentBlockType `json:"type"`
	Text     *string          `json:"text,omitempty"`
	ImageURL *ImageURLStruct  `json:"image_url,omitempty"`
}

// ToolMessage represents a message from a tool
type ToolMessage struct {
	ToolCallID *string `json:"tool_call_id,omitempty"`
}

// AssistantMessage represents a message from an assistant
type AssistantMessage struct {
	Refusal     *string      `json:"refusal,omitempty"`
	Annotations []Annotation `json:"annotations,omitempty"`
	ToolCalls   *[]ToolCall  `json:"tool_calls,omitempty"`
	Thought     *string      `json:"thought,omitempty"`
}

// ImageContent represents image data in a message.
type ImageURLStruct struct {
	URL    string  `json:"url"`
	Detail *string `json:"detail,omitempty"`
}

//* Response Structs

// BifrostResponse represents the complete result from any bifrost request.
type BifrostResponse struct {
	ID                string                     `json:"id,omitempty"`
	Object            string                     `json:"object,omitempty"` // text.completion, chat.completion, or embedding
	Choices           []BifrostResponseChoice    `json:"choices,omitempty"`
	Embedding         [][]float32                `json:"data,omitempty"` // Maps to "data" field in provider responses (e.g., OpenAI embedding format)
	Model             string                     `json:"model,omitempty"`
	Created           int                        `json:"created,omitempty"` // The Unix timestamp (in seconds).
	ServiceTier       *string                    `json:"service_tier,omitempty"`
	SystemFingerprint *string                    `json:"system_fingerprint,omitempty"`
	Usage             *LLMUsage                  `json:"usage,omitempty"`
	ExtraFields       BifrostResponseExtraFields `json:"extra_fields"`
}

// LLMUsage represents token usage information
type LLMUsage struct {
	PromptTokens            int                      `json:"prompt_tokens"`
	CompletionTokens        int                      `json:"completion_tokens"`
	TotalTokens             int                      `json:"total_tokens"`
	TokenDetails            *TokenDetails            `json:"prompt_tokens_details,omitempty"`
	CompletionTokensDetails *CompletionTokensDetails `json:"completion_tokens_details,omitempty"`
}

// TokenDetails provides detailed information about token usage.
// It is not provided by all model providers.
type TokenDetails struct {
	CachedTokens int `json:"cached_tokens,omitempty"`
	AudioTokens  int `json:"audio_tokens,omitempty"`
}

// CompletionTokensDetails provides detailed information about completion token usage.
// It is not provided by all model providers.
type CompletionTokensDetails struct {
	ReasoningTokens          int `json:"reasoning_tokens,omitempty"`
	AudioTokens              int `json:"audio_tokens,omitempty"`
	AcceptedPredictionTokens int `json:"accepted_prediction_tokens,omitempty"`
	RejectedPredictionTokens int `json:"rejected_prediction_tokens,omitempty"`
}

// BilledLLMUsage represents the billing information for token usage.
type BilledLLMUsage struct {
	PromptTokens     *float64 `json:"prompt_tokens,omitempty"`
	CompletionTokens *float64 `json:"completion_tokens,omitempty"`
	SearchUnits      *float64 `json:"search_units,omitempty"`
	Classifications  *float64 `json:"classifications,omitempty"`
}

// LogProb represents the log probability of a token.
type LogProb struct {
	Bytes   []int   `json:"bytes,omitempty"`
	LogProb float64 `json:"logprob"`
	Token   string  `json:"token"`
}

// ContentLogProb represents log probability information for content.
type ContentLogProb struct {
	Bytes       []int     `json:"bytes"`
	LogProb     float64   `json:"logprob"`
	Token       string    `json:"token"`
	TopLogProbs []LogProb `json:"top_logprobs"`
}

// TextCompletionLogProb represents log probability information for text completion.
type TextCompletionLogProb struct {
	TextOffset    []int                `json:"text_offset"`
	TokenLogProbs []float64            `json:"token_logprobs"`
	Tokens        []string             `json:"tokens"`
	TopLogProbs   []map[string]float64 `json:"top_logprobs"`
}

// LogProbs represents the log probabilities for different aspects of a response.
type LogProbs struct {
	Content []ContentLogProb      `json:"content,omitempty"`
	Refusal []LogProb             `json:"refusal,omitempty"`
	Text    TextCompletionLogProb `json:"text,omitempty"`
}

// FunctionCall represents a call to a function.
type FunctionCall struct {
	Name      *string `json:"name"`
	Arguments string  `json:"arguments"` // stringified json as retured by OpenAI, might not be a valid JSON always
}

// ToolCall represents a tool call in a message
type ToolCall struct {
	Type     *string      `json:"type,omitempty"`
	ID       *string      `json:"id,omitempty"`
	Function FunctionCall `json:"function"`
}

// Citation represents a citation in a response.
type Citation struct {
	StartIndex int          `json:"start_index"`
	EndIndex   int          `json:"end_index"`
	Title      string       `json:"title"`
	URL        *string      `json:"url,omitempty"`
	Sources    *interface{} `json:"sources,omitempty"`
	Type       *string      `json:"type,omitempty"`
}

// Annotation represents an annotation in a response.
type Annotation struct {
	Type     string   `json:"type"`
	Citation Citation `json:"url_citation"`
}

// BifrostResponseChoice represents a choice in the completion result.
// This struct can represent either a streaming or non-streaming response choice.
// IMPORTANT: Only one of BifrostNonStreamResponseChoice or BifrostStreamResponseChoice
// should be non-nil at a time.
type BifrostResponseChoice struct {
	Index        int     `json:"index"`
	FinishReason *string `json:"finish_reason,omitempty"`

	*BifrostNonStreamResponseChoice
	*BifrostStreamResponseChoice
}

// BifrostNonStreamResponseChoice represents a choice in the non-stream response
type BifrostNonStreamResponseChoice struct {
	Message    BifrostMessage `json:"message"`
	StopString *string        `json:"stop,omitempty"`
	LogProbs   *LogProbs      `json:"log_probs,omitempty"`
}

// BifrostStreamResponseChoice represents a choice in the stream response
type BifrostStreamResponseChoice struct {
	Delta BifrostStreamDelta `json:"delta"` // Partial message info
}

// BifrostStreamDelta represents a delta in the stream response
type BifrostStreamDelta struct {
	Role      *string    `json:"role,omitempty"`       // Only in the first chunk
	Content   *string    `json:"content,omitempty"`    // May be empty string or null
	Thought   *string    `json:"thought,omitempty"`    // May be empty string or null
	Refusal   *string    `json:"refusal,omitempty"`    // Refusal content if any
	ToolCalls []ToolCall `json:"tool_calls,omitempty"` // If tool calls used (supports incremental updates)
}

// BifrostResponseExtraFields contains additional fields in a response.
type BifrostResponseExtraFields struct {
	Provider    ModelProvider     `json:"provider"`
	Params      ModelParameters   `json:"model_params"`
	Latency     *float64          `json:"latency,omitempty"`
	ChatHistory *[]BifrostMessage `json:"chat_history,omitempty"`
	BilledUsage *BilledLLMUsage   `json:"billed_usage,omitempty"`
	RawResponse interface{}       `json:"raw_response"`
}

const (
	RequestCancelled = "request_cancelled"
)

type BifrostStream struct {
	*BifrostResponse
	*BifrostError
}

// BifrostError represents an error from the Bifrost system.
//
// PLUGIN DEVELOPERS: When creating BifrostError in PreHook or PostHook, you can set AllowFallbacks:
// - AllowFallbacks = &true: Bifrost will try fallback providers if available
// - AllowFallbacks = &false: Bifrost will return this error immediately, no fallbacks
// - AllowFallbacks = nil: Treated as true by default (fallbacks allowed for resilience)
type BifrostError struct {
	Provider       ModelProvider `json:"-"`
	EventID        *string       `json:"event_id,omitempty"`
	Type           *string       `json:"type,omitempty"`
	IsBifrostError bool          `json:"is_bifrost_error"`
	StatusCode     *int          `json:"status_code,omitempty"`
	Error          ErrorField    `json:"error"`
	AllowFallbacks *bool         `json:"-"` // Optional: Controls fallback behavior (nil = true by default)
}

// ErrorField represents detailed error information.
type ErrorField struct {
	Type    *string     `json:"type,omitempty"`
	Code    *string     `json:"code,omitempty"`
	Message string      `json:"message"`
	Error   error       `json:"error,omitempty"`
	Param   interface{} `json:"param,omitempty"`
	EventID *string     `json:"event_id,omitempty"`
}
