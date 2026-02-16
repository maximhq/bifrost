package schemas

import (
	"fmt"
	"reflect"
	"sync"
	"time"
)

// =============================================================================
// OPENAI RESPONSES API SCHEMAS
// =============================================================================
//
// This file contains all the schema definitions for the OpenAI Responses API.
//
// Structure:
// 1. Core API Request/Response Structures
// 2. Input Message Structures
// 3. Output Message Structures
// 4. Tool Call Structures (organized by tool type)
// 5. Tool Configuration Structures
// 6. Tool Choice Configuration
//
// Union Types:
// - Many structs use "union types" where only one field should be set
// - These are implemented with pointer fields and custom JSON marshaling
// =============================================================================

// =============================================================================
// 1. CORE API REQUEST/RESPONSE STRUCTURES
// =============================================================================

type BifrostResponsesRequest struct {
	Provider       ModelProvider        `json:"provider"`
	Model          string               `json:"model"`
	Input          []ResponsesMessage   `json:"input,omitempty"`
	Params         *ResponsesParameters `json:"params,omitempty"`
	Fallbacks      []Fallback           `json:"fallbacks,omitempty"`
	RawRequestBody []byte               `json:"-"` // set bifrost-use-raw-request-body to true in ctx to use the raw request body. Bifrost will directly send this to the downstream provider.
}

func (r *BifrostResponsesRequest) GetRawRequestBody() []byte {
	return r.RawRequestBody
}

type BifrostResponsesResponse struct {
	ID     *string `json:"id,omitempty"` // used for internal conversions
	Object string  `json:"object"`       // "response"

	Background         *bool                               `json:"background,omitempty"`
	Conversation       *ResponsesResponseConversation      `json:"conversation,omitempty"`
	CreatedAt          int                                 `json:"created_at"`   // Unix timestamp when Response was created
	CompletedAt        *int                                `json:"completed_at"` // Unix timestamp when Response was completed
	Error              *ResponsesResponseError             `json:"error"`
	Include            []string                            `json:"include,omitempty"`  // Supported values: "web_search_call.action.sources", "code_interpreter_call.outputs", "computer_call_output.output.image_url", "file_search_call.results", "message.input_image.image_url", "message.output_text.logprobs", "reasoning.encrypted_content"
	IncompleteDetails  *ResponsesResponseIncompleteDetails `json:"incomplete_details"` // Details about why the response is incomplete
	Instructions       *ResponsesResponseInstructions      `json:"instructions"`
	MaxOutputTokens    *int                                `json:"max_output_tokens"`
	MaxToolCalls       *int                                `json:"max_tool_calls"`
	Metadata           *map[string]any                     `json:"metadata,omitempty"`
	Model              string                              `json:"model"`
	Output             []ResponsesMessage                  `json:"output"`
	ParallelToolCalls  *bool                               `json:"parallel_tool_calls,omitempty"`
	PreviousResponseID *string                             `json:"previous_response_id"`
	Prompt             *ResponsesPrompt                    `json:"prompt,omitempty"` // Reference to a prompt template and variables
	PromptCacheKey     *string                             `json:"prompt_cache_key"` // Prompt cache key
	PresencePenalty    *float64                            `json:"presence_penalty,omitempty"`
	FrequencyPenalty   *float64                            `json:"frequency_penalty,omitempty"`
	Reasoning          *ResponsesParametersReasoning       `json:"reasoning"`         // Configuration options for reasoning models
	SafetyIdentifier   *string                             `json:"safety_identifier"` // Safety identifier
	ServiceTier        *string                             `json:"service_tier"`
	Status             *string                             `json:"status,omitempty"` // completed, failed, in_progress, cancelled, queued, or incomplete
	StreamOptions      *ResponsesStreamOptions             `json:"stream_options,omitempty"`
	StopReason         *string                             `json:"stop_reason,omitempty"` // Not in OpenAI's spec, but sent by other providers
	Store              *bool                               `json:"store,omitempty"`
	Temperature        *float64                            `json:"temperature,omitempty"`
	Text               *ResponsesTextConfig                `json:"text,omitempty"`
	TopLogProbs        *int                                `json:"top_logprobs,omitempty"`
	TopP               *float64                            `json:"top_p,omitempty"`       // Controls diversity via nucleus sampling
	ToolChoice         *ResponsesToolChoice                `json:"tool_choice,omitempty"` // Whether to call a tool
	Tools              []ResponsesTool                     `json:"tools"`                 // Tools to use
	Truncation         *string                             `json:"truncation,omitempty"`
	Usage              *ResponsesResponseUsage             `json:"usage"`
	ExtraFields        BifrostResponseExtraFields          `json:"extra_fields"`

	// Perplexity-specific fields
	SearchResults []SearchResult `json:"search_results,omitempty"`
	Videos        []VideoResult  `json:"videos,omitempty"`
	Citations     []string       `json:"citations,omitempty"`
}

// bifrostResponsesResponsePool provides a pool for BifrostResponsesResponse objects.
var bifrostResponsesResponsePool = sync.Pool{
	New: func() interface{} {
		return &BifrostResponsesResponse{}
	},
}

// AcquireBifrostResponsesResponse gets a BifrostResponsesResponse from the pool and resets it.
func AcquireBifrostResponsesResponse() *BifrostResponsesResponse {
	r := bifrostResponsesResponsePool.Get().(*BifrostResponsesResponse)
	*r = BifrostResponsesResponse{}
	return r
}

// Release returns a BifrostResponsesResponse to the pool after clearing all fields.
// The caller must ensure no other goroutine holds a reference to this response.
// Do NOT use the response after calling Release().
func (resp *BifrostResponsesResponse) Release() {
	if resp == nil {
		return
	}
	// Nil all pointer and slice fields to break references and allow GC
	resp.ID = nil
	resp.Object = ""
	resp.Background = nil
	resp.Conversation = nil
	resp.CreatedAt = 0
	resp.CompletedAt = nil
	resp.Error = nil
	resp.Include = nil
	resp.IncompleteDetails = nil
	resp.Instructions = nil
	resp.MaxOutputTokens = nil
	resp.MaxToolCalls = nil
	resp.Metadata = nil
	resp.Model = ""
	resp.Output = nil
	resp.ParallelToolCalls = nil
	resp.PreviousResponseID = nil
	resp.Prompt = nil
	resp.PromptCacheKey = nil
	resp.PresencePenalty = nil
	resp.FrequencyPenalty = nil
	resp.Reasoning = nil
	resp.SafetyIdentifier = nil
	resp.ServiceTier = nil
	resp.Status = nil
	resp.StreamOptions = nil
	resp.StopReason = nil
	resp.Store = nil
	resp.Temperature = nil
	resp.Text = nil
	resp.TopLogProbs = nil
	resp.TopP = nil
	resp.ToolChoice = nil
	resp.Tools = nil
	resp.Truncation = nil
	resp.Usage = nil
	resp.ExtraFields = BifrostResponseExtraFields{}
	resp.SearchResults = nil
	resp.Videos = nil
	resp.Citations = nil
	bifrostResponsesResponsePool.Put(resp)
}

func (resp *BifrostResponsesResponse) WithDefaults() *BifrostResponsesResponse {
	if resp == nil {
		return nil
	}

	result := &BifrostResponsesResponse{
		ID:        resp.ID,
		CreatedAt: resp.CreatedAt,
		Model:     resp.Model,
	}

	// Object - default: "response"
	if resp.Object != "" {
		result.Object = resp.Object
	} else {
		result.Object = "response"
	}

	result.Conversation = resp.Conversation
	result.Include = resp.Include
	result.Metadata = resp.Metadata
	result.Prompt = resp.Prompt
	result.StreamOptions = resp.StreamOptions
	result.StopReason = resp.StopReason
	result.ExtraFields = resp.ExtraFields
	result.SearchResults = resp.SearchResults
	result.Videos = resp.Videos
	result.Citations = resp.Citations
	result.IncompleteDetails = resp.IncompleteDetails
	result.PreviousResponseID = resp.PreviousResponseID
	result.PromptCacheKey = resp.PromptCacheKey
	result.SafetyIdentifier = resp.SafetyIdentifier
	result.MaxToolCalls = resp.MaxToolCalls
	result.Instructions = resp.Instructions
	result.Error = resp.Error
	result.CompletedAt = resp.CompletedAt
	result.MaxOutputTokens = resp.MaxOutputTokens

	// Status - default: "completed"
	if resp.Status != nil {
		result.Status = resp.Status
	} else {
		result.Status = Ptr("completed")
	}

	// Output array - default: empty array
	if resp.Output != nil {
		result.Output = resp.Output
	} else {
		result.Output = []ResponsesMessage{}
	}

	if resp.Reasoning != nil {
		result.Reasoning = resp.Reasoning
	} else {
		result.Reasoning = &ResponsesParametersReasoning{}
	}

	// Sampling parameters - defaults: standard values
	result.Temperature = orDefault(resp.Temperature, 1.0)
	result.TopP = orDefault(resp.TopP, 1.0)
	result.PresencePenalty = orDefault(resp.PresencePenalty, 0.0)
	result.FrequencyPenalty = orDefault(resp.FrequencyPenalty, 0.0)

	// Response configuration - defaults: standard behavior
	result.Store = orDefault(resp.Store, true)
	result.Background = orDefault(resp.Background, false)
	result.ServiceTier = orDefault(resp.ServiceTier, "auto")
	result.Truncation = orDefault(resp.Truncation, "disabled")
	result.ParallelToolCalls = orDefault(resp.ParallelToolCalls, true)

	// Token limits - defaults: 0 (unlimited)
	result.TopLogProbs = orDefault(resp.TopLogProbs, 0)

	// Tools array - default: empty array
	if resp.Tools != nil {
		result.Tools = resp.Tools
	} else {
		result.Tools = []ResponsesTool{}
	}

	// Tool choice - default: "auto"
	if resp.ToolChoice != nil {
		result.ToolChoice = resp.ToolChoice
	} else {
		autoStr := "auto"
		result.ToolChoice = &ResponsesToolChoice{
			ResponsesToolChoiceStr: &autoStr,
		}
	}

	// Text config - default: text format with medium verbosity
	if resp.Text != nil {
		result.Text = &ResponsesTextConfig{
			Format:    resp.Text.Format,
			Verbosity: resp.Text.Verbosity,
		}
		if result.Text.Format == nil {
			result.Text.Format = &ResponsesTextConfigFormat{Type: "text"}
		}
		if result.Text.Verbosity == nil {
			result.Text.Verbosity = Ptr("medium")
		}
	} else {
		result.Text = &ResponsesTextConfig{
			Format:    &ResponsesTextConfigFormat{Type: "text"},
			Verbosity: Ptr("medium"),
		}
	}

	// Usage - ensure token details exist
	result.Usage = resp.Usage
	if result.Usage != nil {
		result.Usage.Iterations = nil
		result.Usage.Type = nil
		if result.Usage.InputTokensDetails == nil {
			result.Usage.InputTokensDetails = &ResponsesResponseInputTokens{CachedTokens: 0}
		}
		if result.Usage.OutputTokensDetails == nil {
			result.Usage.OutputTokensDetails = &ResponsesResponseOutputTokens{ReasoningTokens: 0}
		}
	}

	return result
}

// orDefault returns src if non-nil, otherwise returns a pointer to defaultVal
func orDefault[T any](src *T, defaultVal T) *T {
	if src != nil {
		return src
	}
	return Ptr(defaultVal)
}

type ResponsesParameters struct {
	Background         *bool                         `json:"background,omitempty"`
	Conversation       *string                       `json:"conversation,omitempty"`
	Include            []string                      `json:"include,omitempty"` // Supported values: "web_search_call.action.sources", "code_interpreter_call.outputs", "computer_call_output.output.image_url", "file_search_call.results", "message.input_image.image_url", "message.output_text.logprobs", "reasoning.encrypted_content"
	Instructions       *string                       `json:"instructions,omitempty"`
	MaxOutputTokens    *int                          `json:"max_output_tokens,omitempty"`
	MaxToolCalls       *int                          `json:"max_tool_calls,omitempty"`
	Metadata           *map[string]any               `json:"metadata,omitempty"`
	ParallelToolCalls  *bool                         `json:"parallel_tool_calls,omitempty"`
	PreviousResponseID *string                       `json:"previous_response_id,omitempty"`
	PromptCacheKey     *string                       `json:"prompt_cache_key,omitempty"`  // Prompt cache key
	Reasoning          *ResponsesParametersReasoning `json:"reasoning,omitempty"`         // Configuration options for reasoning models
	SafetyIdentifier   *string                       `json:"safety_identifier,omitempty"` // Safety identifier
	ServiceTier        *string                       `json:"service_tier,omitempty"`
	StreamOptions      *ResponsesStreamOptions       `json:"stream_options,omitempty"`
	Store              *bool                         `json:"store,omitempty"`
	Temperature        *float64                      `json:"temperature,omitempty"`
	Text               *ResponsesTextConfig          `json:"text,omitempty"`
	TopLogProbs        *int                          `json:"top_logprobs,omitempty"`
	TopP               *float64                      `json:"top_p,omitempty"`       // Controls diversity via nucleus sampling
	ToolChoice         *ResponsesToolChoice          `json:"tool_choice,omitempty"` // Whether to call a tool
	Tools              []ResponsesTool               `json:"tools,omitempty"`       // Tools to use
	Truncation         *string                       `json:"truncation,omitempty"`
	User               *string                       `json:"user,omitempty"`
	// Dynamic parameters that can be provider-specific, they are directly
	// added to the request as is.
	ExtraParams map[string]interface{} `json:"-"`
}

type ResponsesStreamOptions struct {
	IncludeObfuscation *bool `json:"include_obfuscation,omitempty"`
}

type ResponsesTextConfig struct {
	Format    *ResponsesTextConfigFormat `json:"format,omitempty"`    // An object specifying the format that the model must output
	Verbosity *string                    `json:"verbosity,omitempty"` // "low" | "medium" | "high" or null
}

type ResponsesTextConfigFormat struct {
	Type       string                               `json:"type"`             // "text" | "json_schema" | "json_object"
	Name       *string                              `json:"name,omitempty"`   // Name of the format
	JSONSchema *ResponsesTextConfigFormatJSONSchema `json:"schema,omitempty"` // when type == "json_schema"
	Strict     *bool                                `json:"strict,omitempty"`
}

// ResponsesTextConfigFormatJSONSchema represents a JSON schema specification
// It supports JSON Schema fields used by various providers for structured outputs.
type ResponsesTextConfigFormatJSONSchema struct {
	Name                 *string                     `json:"name,omitempty"`
	Schema               *any                        `json:"schema,omitempty"`
	Description          *string                     `json:"description,omitempty"`
	Strict               *bool                       `json:"strict,omitempty"`
	AdditionalProperties *AdditionalPropertiesStruct `json:"additionalProperties,omitempty"`
	Properties           *map[string]any             `json:"properties,omitempty"`
	Required             []string                    `json:"required,omitempty"`
	Type                 *string                     `json:"type,omitempty"`

	// JSON Schema definition fields
	Defs        *map[string]any `json:"$defs,omitempty"`       // JSON Schema draft 2019-09+ definitions
	Definitions *map[string]any `json:"definitions,omitempty"` // Legacy JSON Schema draft-07 definitions
	Ref         *string         `json:"$ref,omitempty"`        // Reference to definition

	// Array schema fields
	Items    *map[string]any `json:"items,omitempty"`    // Array element schema
	MinItems *int64          `json:"minItems,omitempty"` // Minimum array length
	MaxItems *int64          `json:"maxItems,omitempty"` // Maximum array length

	// Composition fields (union types)
	AnyOf []map[string]any `json:"anyOf,omitempty"` // Union types (any of these schemas)
	OneOf []map[string]any `json:"oneOf,omitempty"` // Exclusive union types (exactly one of these)
	AllOf []map[string]any `json:"allOf,omitempty"` // Schema intersection (all of these)

	// String validation fields
	Format    *string `json:"format,omitempty"`    // String format (email, date, uri, etc.)
	Pattern   *string `json:"pattern,omitempty"`   // Regex pattern for strings
	MinLength *int64  `json:"minLength,omitempty"` // Minimum string length
	MaxLength *int64  `json:"maxLength,omitempty"` // Maximum string length

	// Number validation fields
	Minimum *float64 `json:"minimum,omitempty"` // Minimum number value
	Maximum *float64 `json:"maximum,omitempty"` // Maximum number value

	// Misc fields
	Title            *string     `json:"title,omitempty"`            // Schema title
	Default          interface{} `json:"default,omitempty"`          // Default value
	Nullable         *bool       `json:"nullable,omitempty"`         // Nullable indicator (OpenAPI 3.0 style)
	Enum             []string    `json:"enum,omitempty"`             // Enum values
	PropertyOrdering []string    `json:"propertyOrdering,omitempty"` // Ordering of properties, specific to Gemini
}

type ResponsesResponseConversation struct {
	ResponsesResponseConversationStr    *string
	ResponsesResponseConversationStruct *ResponsesResponseConversationStruct
}

// MarshalJSON implements custom JSON marshalling for ResponsesMessageContent.
// It marshals either ContentStr or ContentBlocks directly without wrapping.
func (rc ResponsesResponseConversation) MarshalJSON() ([]byte, error) {
	// Validation: ensure only one field is set at a time
	if rc.ResponsesResponseConversationStr != nil && rc.ResponsesResponseConversationStruct != nil {
		return nil, fmt.Errorf("both ResponsesResponseConversationStr and ResponsesResponseConversationStruct are set; only one should be non-nil")
	}

	if rc.ResponsesResponseConversationStr != nil {
		return Marshal(*rc.ResponsesResponseConversationStr)
	}
	if rc.ResponsesResponseConversationStruct != nil {
		return Marshal(rc.ResponsesResponseConversationStruct)
	}
	// If both are nil, return null
	return Marshal(nil)
}

// UnmarshalJSON implements custom JSON unmarshalling for ResponsesMessageContent.
// It determines whether "content" is a string or array and assigns to the appropriate field.
// It also handles direct string/array content without a wrapper object.
func (rc *ResponsesResponseConversation) UnmarshalJSON(data []byte) error {
	// First, try to unmarshal as a direct string
	var stringContent string
	if err := Unmarshal(data, &stringContent); err == nil {
		rc.ResponsesResponseConversationStr = &stringContent
		return nil
	}

	// Try to unmarshal as a direct array of ContentBlock
	var structContent ResponsesResponseConversationStruct
	if err := Unmarshal(data, &structContent); err == nil {
		rc.ResponsesResponseConversationStruct = &structContent
		return nil
	}

	return fmt.Errorf("content field is neither a string nor a struct")
}

type ResponsesResponseInstructions struct {
	ResponsesResponseInstructionsStr   *string
	ResponsesResponseInstructionsArray []ResponsesMessage
}

// MarshalJSON implements custom JSON marshalling for ResponsesMessageContent.
// It marshals either ContentStr or ContentBlocks directly without wrapping.
func (rc ResponsesResponseInstructions) MarshalJSON() ([]byte, error) {
	// Validation: ensure only one field is set at a time
	if rc.ResponsesResponseInstructionsStr != nil && rc.ResponsesResponseInstructionsArray != nil {
		return nil, fmt.Errorf("both ResponsesMessageContentStr and ResponsesMessageContentBlocks are set; only one should be non-nil")
	}

	if rc.ResponsesResponseInstructionsStr != nil {
		return Marshal(*rc.ResponsesResponseInstructionsStr)
	}
	if rc.ResponsesResponseInstructionsArray != nil {
		return Marshal(rc.ResponsesResponseInstructionsArray)
	}
	// If both are nil, return null
	return Marshal(nil)
}

// UnmarshalJSON implements custom JSON unmarshalling for ResponsesMessageContent.
// It determines whether "content" is a string or array and assigns to the appropriate field.
// It also handles direct string/array content without a wrapper object.
func (rc *ResponsesResponseInstructions) UnmarshalJSON(data []byte) error {
	// First, try to unmarshal as a direct string
	var stringContent string
	if err := Unmarshal(data, &stringContent); err == nil {
		rc.ResponsesResponseInstructionsStr = &stringContent
		return nil
	}

	// Try to unmarshal as a direct array of ContentBlock
	var arrayContent []ResponsesMessage
	if err := Unmarshal(data, &arrayContent); err == nil {
		rc.ResponsesResponseInstructionsArray = arrayContent
		return nil
	}

	return fmt.Errorf("content field is neither a string nor an array of Messages")
}

type ResponsesPrompt struct {
	ID        string         `json:"id"`
	Variables map[string]any `json:"variables"`
	Version   *string        `json:"version,omitempty"`
}

type ResponsesParametersReasoning struct {
	Effort          *string `json:"effort"`                     // "none" | "minimal" | "low" | "medium" | "high" (any value other than "none" will enable reasoning)
	GenerateSummary *string `json:"generate_summary,omitempty"` // Deprecated: use summary instead
	Summary         *string `json:"summary"`                    // "auto" | "concise" | "detailed"
	MaxTokens       *int    `json:"max_tokens,omitempty"`       // Maximum number of tokens to generate for the reasoning output (required for anthropic)
}

type ResponsesResponseConversationStruct struct {
	ID string `json:"id"` // The unique ID of the conversation
}

type ResponsesResponseError struct {
	Code    string `json:"code"`    // The error code for the response
	Message string `json:"message"` // A human-readable description of the error
}

type ResponsesResponseIncompleteDetails struct {
	Reason string `json:"reason"` // The reason why the response is incomplete
}

type ResponsesResponseUsage struct {
	Type                *string                        `json:"type,omitempty"`        // type field is sent by anthropic
	InputTokens         int                            `json:"input_tokens"`          // Number of input tokens
	InputTokensDetails  *ResponsesResponseInputTokens  `json:"input_tokens_details"`  // Detailed breakdown of input tokens
	OutputTokens        int                            `json:"output_tokens"`         // Number of output tokens
	OutputTokensDetails *ResponsesResponseOutputTokens `json:"output_tokens_details"` // Detailed breakdown of output tokens	TotalTokens int `json:"total_tokens"` // Total number of tokens used
	TotalTokens         int                            `json:"total_tokens"`          // Total number of tokens used
	Cost                *BifrostCost                   `json:"cost,omitempty"`        // Only for the providers which support cost calculation
	Iterations          []ResponsesResponseUsage       `json:"iterations,omitempty"`  // iterations field is sent by anthropic
}

type ResponsesResponseInputTokens struct {
	TextTokens  int `json:"text_tokens,omitempty"`  // Tokens for text input
	AudioTokens int `json:"audio_tokens,omitempty"` // Tokens for audio input
	ImageTokens int `json:"image_tokens,omitempty"` // Tokens for image input

	// For Providers which follow OpenAI's spec, CachedTokens means the number of input tokens read from the cache+input tokens used to create the cache entry. (because they do not differentiate between cache creation and cache read tokens)
	// For Providers which do not follow OpenAI's spec, CachedTokens means only the number of input tokens read from the cache.
	CachedTokens int `json:"cached_tokens"`
}

type ResponsesResponseOutputTokens struct {
	TextTokens               int  `json:"text_tokens,omitempty"`
	AcceptedPredictionTokens int  `json:"accepted_prediction_tokens,omitempty"`
	AudioTokens              int  `json:"audio_tokens,omitempty"`
	ImageTokens              *int `json:"image_tokens,omitempty"`
	ReasoningTokens          int  `json:"reasoning_tokens"` // Required for few OpenAI models
	RejectedPredictionTokens int  `json:"rejected_prediction_tokens,omitempty"`
	CitationTokens           *int `json:"citation_tokens,omitempty"`
	NumSearchQueries         *int `json:"num_search_queries,omitempty"`

	// This means the number of input tokens used to create the cache entry. (cache creation tokens)
	CachedTokens int `json:"cached_tokens,omitempty"` // Not in OpenAI's schemas, but sent by a few providers (Anthropic, Bedrock are some of them)
}

// =============================================================================
// 2. INPUT MESSAGE STRUCTURES
// =============================================================================

type ResponsesMessageType string

const (
	ResponsesMessageTypeMessage              ResponsesMessageType = "message"
	ResponsesMessageTypeFileSearchCall       ResponsesMessageType = "file_search_call"
	ResponsesMessageTypeComputerCall         ResponsesMessageType = "computer_call"
	ResponsesMessageTypeComputerCallOutput   ResponsesMessageType = "computer_call_output"
	ResponsesMessageTypeWebSearchCall        ResponsesMessageType = "web_search_call"
	ResponsesMessageTypeFunctionCall         ResponsesMessageType = "function_call"
	ResponsesMessageTypeFunctionCallOutput   ResponsesMessageType = "function_call_output"
	ResponsesMessageTypeCodeInterpreterCall  ResponsesMessageType = "code_interpreter_call"
	ResponsesMessageTypeLocalShellCall       ResponsesMessageType = "local_shell_call"
	ResponsesMessageTypeLocalShellCallOutput ResponsesMessageType = "local_shell_call_output"
	ResponsesMessageTypeMCPCall              ResponsesMessageType = "mcp_call"
	ResponsesMessageTypeCustomToolCall       ResponsesMessageType = "custom_tool_call"
	ResponsesMessageTypeCustomToolCallOutput ResponsesMessageType = "custom_tool_call_output"
	ResponsesMessageTypeImageGenerationCall  ResponsesMessageType = "image_generation_call"
	ResponsesMessageTypeMCPListTools         ResponsesMessageType = "mcp_list_tools"
	ResponsesMessageTypeMCPApprovalRequest   ResponsesMessageType = "mcp_approval_request"
	ResponsesMessageTypeMCPApprovalResponses ResponsesMessageType = "mcp_approval_responses"
	ResponsesMessageTypeReasoning            ResponsesMessageType = "reasoning"
	ResponsesMessageTypeItemReference        ResponsesMessageType = "item_reference"
	ResponsesMessageTypeRefusal              ResponsesMessageType = "refusal"
)

// ResponsesMessage is a union type that can contain different types of input items
// Only one of the fields should be set at a time
type ResponsesMessage struct {
	ID     *string               `json:"id,omitempty"` // Common ID field for most item types
	Type   *ResponsesMessageType `json:"type,omitempty"`
	Status *string               `json:"status,omitempty"` // "in_progress" | "completed" | "incomplete" | "interpreting" | "failed"

	Role    *ResponsesMessageRoleType `json:"role,omitempty"`
	Content *ResponsesMessageContent  `json:"content,omitempty"`

	*ResponsesToolMessage // For Tool calls and outputs

	// Reasoning
	// gpt-oss models include only reasoning_text content blocks in a message, while other openai models include summaries+encrypted_content
	*ResponsesReasoning
}

type ResponsesMessageRoleType string

const (
	ResponsesInputMessageRoleAssistant ResponsesMessageRoleType = "assistant"
	ResponsesInputMessageRoleUser      ResponsesMessageRoleType = "user"
	ResponsesInputMessageRoleSystem    ResponsesMessageRoleType = "system"
	ResponsesInputMessageRoleDeveloper ResponsesMessageRoleType = "developer"
)

// ResponsesMessageContent is a union type that can be either a string or array of content blocks
type ResponsesMessageContent struct {
	ContentStr *string // Simple text content

	// Output will ALWAYS be an array of content blocks
	ContentBlocks []ResponsesMessageContentBlock // Rich content with multiple media types
}

// MarshalJSON implements custom JSON marshalling for ResponsesMessageContent.
// It marshals either ContentStr or ContentBlocks directly without wrapping.
func (rc ResponsesMessageContent) MarshalJSON() ([]byte, error) {
	// Validation: ensure only one field is set at a time
	if rc.ContentStr != nil && rc.ContentBlocks != nil {
		return nil, fmt.Errorf("both ResponsesMessageContentStr and ResponsesMessageContentBlocks are set; only one should be non-nil")
	}

	if rc.ContentStr != nil {
		return Marshal(*rc.ContentStr)
	}
	if rc.ContentBlocks != nil {
		return Marshal(rc.ContentBlocks)
	}
	// If both are nil, return null
	return Marshal(nil)
}

// UnmarshalJSON implements custom JSON unmarshalling for ResponsesMessageContent.
// It determines whether "content" is a string or array and assigns to the appropriate field.
// It also handles direct string/array content without a wrapper object.
func (rc *ResponsesMessageContent) UnmarshalJSON(data []byte) error {
	// First, try to unmarshal as a direct string
	var stringContent string
	if err := Unmarshal(data, &stringContent); err == nil {
		rc.ContentStr = &stringContent
		return nil
	}

	// Try to unmarshal as a direct array of ContentBlock
	var arrayContent []ResponsesMessageContentBlock
	if err := Unmarshal(data, &arrayContent); err == nil {
		rc.ContentBlocks = arrayContent
		return nil
	}

	return fmt.Errorf("content field is neither a string nor an array of Content blocks")
}

type ResponsesMessageContentBlockType string

const (
	ResponsesInputMessageContentBlockTypeText  ResponsesMessageContentBlockType = "input_text"
	ResponsesInputMessageContentBlockTypeImage ResponsesMessageContentBlockType = "input_image"
	ResponsesInputMessageContentBlockTypeFile  ResponsesMessageContentBlockType = "input_file"
	ResponsesInputMessageContentBlockTypeAudio ResponsesMessageContentBlockType = "input_audio"
	ResponsesOutputMessageContentTypeText      ResponsesMessageContentBlockType = "output_text"
	ResponsesOutputMessageContentTypeRefusal   ResponsesMessageContentBlockType = "refusal"
	ResponsesOutputMessageContentTypeReasoning ResponsesMessageContentBlockType = "reasoning_text"

	// gemini sends rendered content in google search results
	ResponsesOutputMessageContentTypeRenderedContent ResponsesMessageContentBlockType = "rendered_content"

	ResponsesOutputMessageContentTypeCompaction ResponsesMessageContentBlockType = "compaction"
)

// ResponsesMessageContentBlock represents different types of content (text, image, file, audio)
// Only one of the content type fields should be set
type ResponsesMessageContentBlock struct {
	Type      ResponsesMessageContentBlockType `json:"type"`
	FileID    *string                          `json:"file_id,omitempty"` // Reference to uploaded file
	Text      *string                          `json:"text,omitempty"`
	Signature *string                          `json:"signature,omitempty"` // Signature of the content (for reasoning)

	*ResponsesInputMessageContentBlockImage
	*ResponsesInputMessageContentBlockFile
	Audio *ResponsesInputMessageContentBlockAudio `json:"input_audio,omitempty"`

	*ResponsesOutputMessageContentText            // Normal text output from the model
	*ResponsesOutputMessageContentRefusal         // Model refusal to answer
	*ResponsesOutputMessageContentRenderedContent // Rendered content from search entry point
	*ResponsesOutputMessageContentCompaction      // Compaction content from the model

	// Not in OpenAI's schemas, but sent by a few providers (Anthropic, Bedrock are some of them)
	CacheControl *CacheControl `json:"cache_control,omitempty"`
	Citations    *Citations    `json:"citations,omitempty"`
}

type ResponsesOutputMessageContentCompaction struct {
	Summary string `json:"summary,omitempty"` // The compaction summary text
}
type ResponsesOutputMessageContentRenderedContent struct {
	RenderedContent string `json:"rendered_content"` // HTML/styled content from search entry point
}

type Citations struct {
	Enabled *bool `json:"enabled,omitempty"`
}
type ResponsesInputMessageContentBlockImage struct {
	ImageURL *string `json:"image_url,omitempty"`
	Detail   *string `json:"detail,omitempty"` // "low" | "high" | "auto"
}

type ResponsesInputMessageContentBlockFile struct {
	FileData *string `json:"file_data,omitempty"` // Base64 encoded file data or plain text
	FileURL  *string `json:"file_url,omitempty"`  // Direct URL to file
	Filename *string `json:"filename,omitempty"`  // Name of the file
	FileType *string `json:"file_type,omitempty"` // MIME type (e.g., "application/pdf", "text/plain")
}

type ResponsesInputMessageContentBlockAudio struct {
	Format string `json:"format"` // "mp3" or "wav"
	Data   string `json:"data"`   // base64 encoded audio data
}

// =============================================================================
// 3. OUTPUT MESSAGE STRUCTURES
// =============================================================================

type ResponsesOutputMessageContentText struct {
	Annotations []ResponsesOutputMessageContentTextAnnotation `json:"annotations"` // Citations and references
	LogProbs    []ResponsesOutputMessageContentTextLogProb    `json:"logprobs"`    // Token log probabilities
}

type ResponsesOutputMessageContentTextAnnotation struct {
	Type        string  `json:"type"`                  // "file_citation" | "url_citation" | "container_file_citation" | "file_path"
	Index       *int    `json:"index,omitempty"`       // Common index field (FileCitation, FilePath)
	FileID      *string `json:"file_id,omitempty"`     // Common file ID field (FileCitation, ContainerFileCitation, FilePath)
	Text        *string `json:"text,omitempty"`        // Text of the citation
	StartIndex  *int    `json:"start_index,omitempty"` // Common start index field (URLCitation, ContainerFileCitation)
	EndIndex    *int    `json:"end_index,omitempty"`   // Common end index field (URLCitation, ContainerFileCitation)
	Filename    *string `json:"filename,omitempty"`
	Title       *string `json:"title,omitempty"`
	URL         *string `json:"url,omitempty"`
	ContainerID *string `json:"container_id,omitempty"`

	// Anthropic specific fields
	StartCharIndex  *int    `json:"start_char_index,omitempty"`
	EndCharIndex    *int    `json:"end_char_index,omitempty"`
	StartPageNumber *int    `json:"start_page_number,omitempty"`
	EndPageNumber   *int    `json:"end_page_number,omitempty"`
	StartBlockIndex *int    `json:"start_block_index,omitempty"`
	EndBlockIndex   *int    `json:"end_block_index,omitempty"`
	Source          *string `json:"source,omitempty"`
	EncryptedIndex  *string `json:"encrypted_index,omitempty"`
}

// ResponsesOutputMessageContentTextLogProb represents log probability information for content.
type ResponsesOutputMessageContentTextLogProb struct {
	Bytes       []int     `json:"bytes"`
	LogProb     float64   `json:"logprob"`
	Token       string    `json:"token"`
	TopLogProbs []LogProb `json:"top_logprobs"`
}
type ResponsesOutputMessageContentRefusal struct {
	Refusal string `json:"refusal"`
}

type ResponsesToolMessage struct {
	CallID    *string                           `json:"call_id,omitempty"` // Common call ID for tool calls and outputs
	Name      *string                           `json:"name,omitempty"`    // Common name field for tool calls
	Arguments *string                           `json:"arguments,omitempty"`
	Output    *ResponsesToolMessageOutputStruct `json:"output,omitempty"`
	Action    *ResponsesToolMessageActionStruct `json:"action,omitempty"`
	Error     *string                           `json:"error,omitempty"`

	// Tool calls and outputs
	*ResponsesFileSearchToolCall
	*ResponsesComputerToolCall
	*ResponsesComputerToolCallOutput
	*ResponsesCodeInterpreterToolCall
	*ResponsesMCPToolCall
	*ResponsesCustomToolCall
	*ResponsesImageGenerationCall

	// MCP-specific
	*ResponsesMCPListTools
	*ResponsesMCPApprovalResponse
}

type ResponsesToolMessageActionStruct struct {
	ResponsesComputerToolCallAction   *ResponsesComputerToolCallAction
	ResponsesWebSearchToolCallAction  *ResponsesWebSearchToolCallAction
	ResponsesLocalShellToolCallAction *ResponsesLocalShellToolCallAction
	ResponsesMCPApprovalRequestAction *ResponsesMCPApprovalRequestAction
}

func (action ResponsesToolMessageActionStruct) MarshalJSON() ([]byte, error) {
	if action.ResponsesComputerToolCallAction != nil {
		return Marshal(action.ResponsesComputerToolCallAction)
	}
	if action.ResponsesWebSearchToolCallAction != nil {
		return Marshal(action.ResponsesWebSearchToolCallAction)
	}
	if action.ResponsesLocalShellToolCallAction != nil {
		return Marshal(action.ResponsesLocalShellToolCallAction)
	}
	if action.ResponsesMCPApprovalRequestAction != nil {
		return Marshal(action.ResponsesMCPApprovalRequestAction)
	}
	return nil, fmt.Errorf("responses tool message action struct is empty")
}

func (action *ResponsesToolMessageActionStruct) UnmarshalJSON(data []byte) error {
	// First, peek at the type field to determine which variant to unmarshal
	var typeStruct struct {
		Type string `json:"type"`
	}
	if err := Unmarshal(data, &typeStruct); err != nil {
		return fmt.Errorf("failed to peek at type field: %w", err)
	}

	// Based on the type, unmarshal into the appropriate variant
	switch typeStruct.Type {
	case "exec":
		var localShellToolCallAction ResponsesLocalShellToolCallAction
		if err := Unmarshal(data, &localShellToolCallAction); err != nil {
			return fmt.Errorf("failed to unmarshal local shell tool call action: %w", err)
		}
		action.ResponsesLocalShellToolCallAction = &localShellToolCallAction
		return nil

	case "mcp_approval_request":
		var mcpApprovalRequestAction ResponsesMCPApprovalRequestAction
		if err := Unmarshal(data, &mcpApprovalRequestAction); err != nil {
			return fmt.Errorf("failed to unmarshal mcp approval request action: %w", err)
		}
		action.ResponsesMCPApprovalRequestAction = &mcpApprovalRequestAction
		return nil

	case "search", "open_page", "find":
		var webSearchToolCallAction ResponsesWebSearchToolCallAction
		if err := Unmarshal(data, &webSearchToolCallAction); err != nil {
			return fmt.Errorf("failed to unmarshal web search tool call action: %w", err)
		}
		action.ResponsesWebSearchToolCallAction = &webSearchToolCallAction
		return nil

	case "click", "double_click", "drag", "keypress", "move", "screenshot", "scroll", "type", "wait", "zoom":
		var computerToolCallAction ResponsesComputerToolCallAction
		if err := Unmarshal(data, &computerToolCallAction); err != nil {
			return fmt.Errorf("failed to unmarshal computer tool call action: %w", err)
		}
		action.ResponsesComputerToolCallAction = &computerToolCallAction
		return nil

	default:
		// use computer tool, as it can have many possible actions
		var computerToolCallAction ResponsesComputerToolCallAction
		if err := Unmarshal(data, &computerToolCallAction); err != nil {
			return fmt.Errorf("failed to unmarshal computer tool call action: %w", err)
		}
		action.ResponsesComputerToolCallAction = &computerToolCallAction
		return nil
	}
}

type ResponsesToolMessageOutputStruct struct {
	ResponsesToolCallOutputStr            *string // Common output string for tool calls and outputs (used by function, custom and local shell tool calls)
	ResponsesFunctionToolCallOutputBlocks []ResponsesMessageContentBlock
	ResponsesComputerToolCallOutput       *ResponsesComputerToolCallOutputData
}

func (output ResponsesToolMessageOutputStruct) MarshalJSON() ([]byte, error) {
	if output.ResponsesToolCallOutputStr != nil {
		return Marshal(*output.ResponsesToolCallOutputStr)
	}
	if output.ResponsesFunctionToolCallOutputBlocks != nil {
		return Marshal(output.ResponsesFunctionToolCallOutputBlocks)
	}
	if output.ResponsesComputerToolCallOutput != nil {
		return Marshal(output.ResponsesComputerToolCallOutput)
	}
	return nil, fmt.Errorf("responses tool message output struct is neither a string nor an array of responses message content blocks nor a computer tool call output data nor an image generation call output")
}
func (output *ResponsesToolMessageOutputStruct) UnmarshalJSON(data []byte) error {
	var str string
	if err := Unmarshal(data, &str); err == nil {
		output.ResponsesToolCallOutputStr = &str
		return nil
	}
	var array []ResponsesMessageContentBlock
	if err := Unmarshal(data, &array); err == nil {
		output.ResponsesFunctionToolCallOutputBlocks = array
		return nil
	}
	var computerToolCallOutput ResponsesComputerToolCallOutputData
	if err := Unmarshal(data, &computerToolCallOutput); err == nil {
		output.ResponsesComputerToolCallOutput = &computerToolCallOutput
		return nil
	}
	return fmt.Errorf("responses tool message output struct is neither a string nor an array of responses message content blocks nor a computer tool call output data nor an image generation call output")
}

// =============================================================================
// 4. TOOL CALL STRUCTURES (organized by tool type)
// =============================================================================

// -----------------------------------------------------------------------------
// File Search Tool
// -----------------------------------------------------------------------------

type ResponsesFileSearchToolCall struct {
	Queries []string                            `json:"queries"`
	Results []ResponsesFileSearchToolCallResult `json:"results,omitempty"`
}

type ResponsesFileSearchToolCallResult struct {
	Attributes *map[string]any `json:"attributes,omitempty"`
	FileID     *string         `json:"file_id,omitempty"`
	Filename   *string         `json:"filename,omitempty"`
	Score      *float64        `json:"score,omitempty"`
	Text       *string         `json:"text,omitempty"`
}

// ResponsesComputerToolCall represents a computer tool call
type ResponsesComputerToolCall struct {
	PendingSafetyChecks []ResponsesComputerToolCallPendingSafetyCheck `json:"pending_safety_checks,omitempty"`
}

// ResponsesComputerToolCallPendingSafetyCheck represents a pending safety check
type ResponsesComputerToolCallPendingSafetyCheck struct {
	ID      string `json:"id"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ResponsesComputerToolCallAction represents the different types of computer actions
type ResponsesComputerToolCallAction struct {
	Type    string                                `json:"type"`             // "click" | "double_click" | "drag" | "keypress" | "move" | "screenshot" | "scroll" | "type" | "wait" | "zoom"
	X       *int                                  `json:"x,omitempty"`      // Common X coordinate field (Click, DoubleClick, Move, Scroll)
	Y       *int                                  `json:"y,omitempty"`      // Common Y coordinate field (Click, DoubleClick, Move, Scroll)
	Button  *string                               `json:"button,omitempty"` // "left" | "right" | "wheel" | "back" | "forward"
	Path    []ResponsesComputerToolCallActionPath `json:"path,omitempty"`
	Keys    []string                              `json:"keys,omitempty"`
	ScrollX *int                                  `json:"scroll_x,omitempty"`
	ScrollY *int                                  `json:"scroll_y,omitempty"`
	Text    *string                               `json:"text,omitempty"`
	Region  []int                                 `json:"region,omitempty"` // [x1, y1, x2, y2] for zoom action (Anthropic Opus 4.5)
}

type ResponsesComputerToolCallActionPath struct {
	X int `json:"x"`
	Y int `json:"y"`
}

// ResponsesComputerToolCallOutput represents a computer tool call output
type ResponsesComputerToolCallOutput struct {
	AcknowledgedSafetyChecks []ResponsesComputerToolCallAcknowledgedSafetyCheck `json:"acknowledged_safety_checks,omitempty"`
}

// ResponsesComputerToolCallOutputData represents a computer screenshot image used with the computer use tool
type ResponsesComputerToolCallOutputData struct {
	Type     string  `json:"type"` // always "computer_screenshot"
	FileID   *string `json:"file_id,omitempty"`
	ImageURL *string `json:"image_url,omitempty"`
}

// ResponsesComputerToolCallAcknowledgedSafetyCheck represents a safety check that has been acknowledged by the developer
type ResponsesComputerToolCallAcknowledgedSafetyCheck struct {
	ID      string  `json:"id"`
	Code    *string `json:"code,omitempty"`
	Message *string `json:"message,omitempty"`
}

// -----------------------------------------------------------------------------
// Web Search Tool
// -----------------------------------------------------------------------------

// ResponsesWebSearchToolCallAction represents the different types of web search actions
type ResponsesWebSearchToolCallAction struct {
	Type    string                                         `json:"type"`          // "search" | "open_page" | "find"
	URL     *string                                        `json:"url,omitempty"` // Common URL field (OpenPage, Find)
	Query   *string                                        `json:"query,omitempty"`
	Queries []string                                       `json:"queries,omitempty"`
	Sources []ResponsesWebSearchToolCallActionSearchSource `json:"sources,omitempty"`
	Pattern *string                                        `json:"pattern,omitempty"`
}

// ResponsesWebSearchToolCallActionSearchSource represents a web search action search source
type ResponsesWebSearchToolCallActionSearchSource struct {
	Type string `json:"type"` // always "url"
	URL  string `json:"url"`

	// Anthropic specific fields
	Title            *string `json:"title,omitempty"`
	EncryptedContent *string `json:"encrypted_content,omitempty"`
	PageAge          *string `json:"page_age,omitempty"`
}

// -----------------------------------------------------------------------------
// Function Tool
// -----------------------------------------------------------------------------

// ResponsesFunctionToolCallOutput represents a function tool call output
type ResponsesFunctionToolCallOutput struct {
	ResponsesFunctionToolCallOutputStr    *string //A JSON string of the output of the function tool call.
	ResponsesFunctionToolCallOutputBlocks []ResponsesMessageContentBlock
}

// MarshalJSON implements custom JSON marshalling for ResponsesFunctionToolCallOutput.
// It marshals either ContentStr or ContentBlocks directly without wrapping.
func (rf ResponsesFunctionToolCallOutput) MarshalJSON() ([]byte, error) {
	// Validation: ensure only one field is set at a time
	if rf.ResponsesFunctionToolCallOutputStr != nil && rf.ResponsesFunctionToolCallOutputBlocks != nil {
		return nil, fmt.Errorf("both ResponsesFunctionToolCallOutputStr and ResponsesFunctionToolCallOutputBlocks are set; only one should be non-nil")
	}

	if rf.ResponsesFunctionToolCallOutputStr != nil {
		return Marshal(*rf.ResponsesFunctionToolCallOutputStr)
	}
	if rf.ResponsesFunctionToolCallOutputBlocks != nil {
		return Marshal(rf.ResponsesFunctionToolCallOutputBlocks)
	}
	// If both are nil, return null
	return Marshal(nil)
}

// UnmarshalJSON implements custom JSON unmarshalling for ResponsesFunctionToolCallOutput.
// It determines whether "content" is a string or array and assigns to the appropriate field.
// It also handles direct string/array content without a wrapper object.
func (rf *ResponsesFunctionToolCallOutput) UnmarshalJSON(data []byte) error {
	// Parse as generic object to check if it contains content-like fields
	var genericObj map[string]interface{}
	if err := Unmarshal(data, &genericObj); err != nil {
		return err
	}

	// If the object doesn't contain typical content fields, it's probably not meant for this struct
	// (e.g., it's a tool call, not a tool call output)
	hasContentFields := false
	for key := range genericObj {
		if key == "content" || key == "output" || key == "result" {
			hasContentFields = true
			break
		}
	}

	if !hasContentFields {
		return nil // Skip unmarshaling if no relevant content fields
	}

	// First, try to unmarshal as a direct string
	var stringContent string
	if err := Unmarshal(data, &stringContent); err == nil {
		rf.ResponsesFunctionToolCallOutputStr = &stringContent
		return nil
	}

	// Try to unmarshal as a direct array of ContentBlock
	var arrayContent []ResponsesMessageContentBlock
	if err := Unmarshal(data, &arrayContent); err == nil {
		rf.ResponsesFunctionToolCallOutputBlocks = arrayContent
		return nil
	}

	return fmt.Errorf("content field is neither a string nor an array of Content blocks")
}

// -----------------------------------------------------------------------------
// Reasoning
// -----------------------------------------------------------------------------

// ResponsesReasoning represents a reasoning output
type ResponsesReasoning struct {
	Summary          []ResponsesReasoningSummary `json:"summary"`
	EncryptedContent *string                     `json:"encrypted_content,omitempty"`
}

// ResponsesReasoningContentBlockType represents the type of reasoning content
type ResponsesReasoningContentBlockType string

// ResponsesReasoningContentBlockType values
const (
	ResponsesReasoningContentBlockTypeSummaryText ResponsesReasoningContentBlockType = "summary_text"
)

// ResponsesReasoningSummary represents a reasoning content block
type ResponsesReasoningSummary struct {
	Type ResponsesReasoningContentBlockType `json:"type"`
	Text string                             `json:"text"`
}

// -----------------------------------------------------------------------------
// Image Generation Tool
// -----------------------------------------------------------------------------

// ResponsesImageGenerationCall represents an image generation tool call
type ResponsesImageGenerationCall struct {
	Result string `json:"result"`
}

// -----------------------------------------------------------------------------
// Code Interpreter Tool
// -----------------------------------------------------------------------------

// ResponsesCodeInterpreterToolCall represents a code interpreter tool call
type ResponsesCodeInterpreterToolCall struct {
	Code        *string                          `json:"code"`         // The code to run, or null if not available
	ContainerID string                           `json:"container_id"` // The ID of the container used to run the code
	Outputs     []ResponsesCodeInterpreterOutput `json:"outputs"`      // The outputs generated by the code interpreter, can be null
}

// ResponsesCodeInterpreterOutput represents a code interpreter output
type ResponsesCodeInterpreterOutput struct {
	*ResponsesCodeInterpreterOutputLogs
	*ResponsesCodeInterpreterOutputImage
}

// MarshalJSON implements custom JSON marshaling for ResponsesCodeInterpreterOutput
func (o ResponsesCodeInterpreterOutput) MarshalJSON() ([]byte, error) {
	// Error if both variants are set
	if o.ResponsesCodeInterpreterOutputLogs != nil && o.ResponsesCodeInterpreterOutputImage != nil {
		return nil, fmt.Errorf("ResponsesCodeInterpreterOutput cannot have both Logs and Image set")
	}

	// Marshal whichever one is present
	if o.ResponsesCodeInterpreterOutputLogs != nil {
		return Marshal(o.ResponsesCodeInterpreterOutputLogs)
	}
	if o.ResponsesCodeInterpreterOutputImage != nil {
		return Marshal(o.ResponsesCodeInterpreterOutputImage)
	}

	// Return null if neither is set
	return []byte("null"), nil
}

// UnmarshalJSON implements custom JSON unmarshaling for ResponsesCodeInterpreterOutput
func (o *ResponsesCodeInterpreterOutput) UnmarshalJSON(data []byte) error {
	// Handle null case
	if string(data) == "null" {
		return nil
	}

	// First, peek at the type field to determine which variant to unmarshal
	var typeStruct struct {
		Type string `json:"type"`
	}
	if err := Unmarshal(data, &typeStruct); err != nil {
		return fmt.Errorf("failed to read type field: %w", err)
	}

	// Unmarshal into the appropriate concrete type based on the type field
	switch typeStruct.Type {
	case "logs":
		var logs ResponsesCodeInterpreterOutputLogs
		if err := Unmarshal(data, &logs); err != nil {
			return fmt.Errorf("failed to unmarshal logs output: %w", err)
		}
		o.ResponsesCodeInterpreterOutputLogs = &logs
		o.ResponsesCodeInterpreterOutputImage = nil
		return nil

	case "image":
		var image ResponsesCodeInterpreterOutputImage
		if err := Unmarshal(data, &image); err != nil {
			return fmt.Errorf("failed to unmarshal image output: %w", err)
		}
		o.ResponsesCodeInterpreterOutputImage = &image
		o.ResponsesCodeInterpreterOutputLogs = nil
		return nil

	default:
		return fmt.Errorf("unknown ResponsesCodeInterpreterOutput type: %s", typeStruct.Type)
	}
}

// ResponsesCodeInterpreterOutputLogs represents the logs output from the code interpreter
type ResponsesCodeInterpreterOutputLogs struct {
	Logs string `json:"logs"`
	Type string `json:"type"` // always "logs"
}

// ResponsesCodeInterpreterOutputImage represents the image output from the code interpreter
type ResponsesCodeInterpreterOutputImage struct {
	Type string `json:"type"` // always "image"
	URL  string `json:"url"`
}

// -----------------------------------------------------------------------------
// Local Shell Tool
// -----------------------------------------------------------------------------

// ResponsesLocalShellCallAction represents the different types of local shell actions
type ResponsesLocalShellToolCallAction struct {
	Command          []string `json:"command"`
	Env              []string `json:"env"`
	Type             string   `json:"type"` // always "exec"
	TimeoutMS        *int     `json:"timeout_ms,omitempty"`
	User             *string  `json:"user,omitempty"`
	WorkingDirectory *string  `json:"working_directory,omitempty"`
}

// -----------------------------------------------------------------------------
// MCP (Model Context Protocol) Tools
// -----------------------------------------------------------------------------

// ResponsesMCPListTools represents a list of MCP tools
type ResponsesMCPListTools struct {
	ServerLabel string             `json:"server_label"`
	Tools       []ResponsesMCPTool `json:"tools"`
}

// ResponsesMCPTool represents an MCP tool
type ResponsesMCPTool struct {
	Name        string          `json:"name"`
	InputSchema map[string]any  `json:"input_schema"`
	Description *string         `json:"description,omitempty"`
	Annotations *map[string]any `json:"annotations,omitempty"`
}

// ResponsesMCPApprovalRequestAction represents the different types of MCP approval request actions
type ResponsesMCPApprovalRequestAction struct {
	ID          string `json:"id"`
	Type        string `json:"type"` // always "mcp_approval_request"
	Name        string `json:"name"`
	ServerLabel string `json:"server_label"`
	Arguments   string `json:"arguments"`
}

// ResponsesMCPApprovalResponse represents a MCP approval response
type ResponsesMCPApprovalResponse struct {
	ApprovalResponseID string  `json:"approval_response_id"`
	Approve            bool    `json:"approve"`
	Reason             *string `json:"reason,omitempty"`
}

// ResponsesMCPToolCall represents a MCP tool call
type ResponsesMCPToolCall struct {
	ServerLabel string `json:"server_label"` // The label of the MCP server running the tool
}

// -----------------------------------------------------------------------------
// Custom Tools
// -----------------------------------------------------------------------------

// ResponsesCustomToolCall represents a custom tool call
type ResponsesCustomToolCall struct {
	Input string `json:"input"` // The input for the custom tool call generated by the model
}

// =============================================================================
// 5. TOOL CHOICE CONFIGURATION
// =============================================================================

// Combined tool choices for all providers, make sure to check the provider's
// documentation to see which tool choices are supported

// ResponsesToolChoiceType represents the type of tool choice
type ResponsesToolChoiceType string

// ResponsesToolChoiceType values
const (
	// ResponsesToolChoiceTypeNone means no tool should be called
	ResponsesToolChoiceTypeNone ResponsesToolChoiceType = "none"
	// ResponsesToolChoiceTypeAuto means an automatic tool should be called
	ResponsesToolChoiceTypeAuto ResponsesToolChoiceType = "auto"
	// ResponsesToolChoiceTypeAny means any tool can be called
	ResponsesToolChoiceTypeAny ResponsesToolChoiceType = "any"
	// ResponsesToolChoiceTypeRequired means a specific tool must be called
	ResponsesToolChoiceTypeRequired ResponsesToolChoiceType = "required"
	// ResponsesToolChoiceTypeFunction means a specific tool must be called
	ResponsesToolChoiceTypeFunction ResponsesToolChoiceType = "function"
	// ResponsesToolChoiceTypeAllowedTools means a specific tool must be called
	ResponsesToolChoiceTypeAllowedTools ResponsesToolChoiceType = "allowed_tools"
	// ResponsesToolChoiceTypeFileSearch means a file search tool must be called
	ResponsesToolChoiceTypeFileSearch ResponsesToolChoiceType = "file_search"
	// ResponsesToolChoiceTypeWebSearchPreview means a web search preview tool must be called
	ResponsesToolChoiceTypeWebSearchPreview ResponsesToolChoiceType = "web_search_preview"
	// ResponsesToolChoiceTypeComputerUsePreview means a computer use preview tool must be called
	ResponsesToolChoiceTypeComputerUsePreview ResponsesToolChoiceType = "computer_use_preview"
	// ResponsesToolChoiceTypeCodeInterpreter means a code interpreter tool must be called
	ResponsesToolChoiceTypeCodeInterpreter ResponsesToolChoiceType = "code_interpreter"
	// ResponsesToolChoiceTypeImageGeneration means an image generation tool must be called
	ResponsesToolChoiceTypeImageGeneration ResponsesToolChoiceType = "image_generation"
	// ResponsesToolChoiceTypeMCP means an MCP tool must be called
	ResponsesToolChoiceTypeMCP ResponsesToolChoiceType = "mcp"
	// ResponsesToolChoiceTypeCustom means a custom tool must be called
	ResponsesToolChoiceTypeCustom ResponsesToolChoiceType = "custom"
)

// ResponsesToolChoiceStruct represents a tool choice struct
type ResponsesToolChoiceStruct struct {
	Type        ResponsesToolChoiceType             `json:"type"`                   // Type of tool choice
	Mode        *string                             `json:"mode,omitempty"`         //"none" | "auto" | "required"
	Name        *string                             `json:"name,omitempty"`         // Common name field for function/MCP/custom tools
	ServerLabel *string                             `json:"server_label,omitempty"` // Common server label field for MCP tools
	Tools       []ResponsesToolChoiceAllowedToolDef `json:"tools,omitempty"`
}

// ResponsesToolChoice represents a tool choice
type ResponsesToolChoice struct {
	ResponsesToolChoiceStr    *string
	ResponsesToolChoiceStruct *ResponsesToolChoiceStruct
}

// MarshalJSON implements custom JSON marshalling for ChatMessageContent.
// It marshals either ContentStr or ContentBlocks directly without wrapping.
func (tc ResponsesToolChoice) MarshalJSON() ([]byte, error) {
	// Validation: ensure only one field is set at a time
	if tc.ResponsesToolChoiceStr != nil && tc.ResponsesToolChoiceStruct != nil {
		return nil, fmt.Errorf("both ResponsesToolChoiceStr, ResponsesToolChoiceStruct are set; only one should be non-nil")
	}

	if tc.ResponsesToolChoiceStr != nil {
		return Marshal(tc.ResponsesToolChoiceStr)
	}
	if tc.ResponsesToolChoiceStruct != nil {
		return Marshal(tc.ResponsesToolChoiceStruct)
	}
	// If both are nil, return null
	return Marshal(nil)
}

// UnmarshalJSON implements custom JSON unmarshalling for ChatMessageContent.
// It determines whether "content" is a string or array and assigns to the appropriate field.
// It also handles direct string/array content without a wrapper object.
func (tc *ResponsesToolChoice) UnmarshalJSON(data []byte) error {
	// First, try to unmarshal as a direct string
	var toolChoiceStr string
	if err := Unmarshal(data, &toolChoiceStr); err == nil {
		tc.ResponsesToolChoiceStr = &toolChoiceStr
		return nil
	}

	// Try to unmarshal as a direct array of ContentBlock
	var responsesToolChoiceStruct ResponsesToolChoiceStruct
	if err := Unmarshal(data, &responsesToolChoiceStruct); err == nil {
		tc.ResponsesToolChoiceStruct = &responsesToolChoiceStruct
		return nil
	}

	return fmt.Errorf("tool_choice field is neither a string nor a ResponsesToolChoiceStruct object")
}

// ResponsesToolChoiceAllowedToolDef represents a tool choice allowed tool definition
type ResponsesToolChoiceAllowedToolDef struct {
	Type        string  `json:"type"`                   // "function" | "mcp" | "image_generation"
	Name        *string `json:"name,omitempty"`         // for function tools
	ServerLabel *string `json:"server_label,omitempty"` // for MCP tools
}

// =============================================================================
// 7. TOOL CONFIGURATION STRUCTURES
// =============================================================================

type ResponsesToolType string

const (
	ResponsesToolTypeFunction           ResponsesToolType = "function"
	ResponsesToolTypeFileSearch         ResponsesToolType = "file_search"
	ResponsesToolTypeComputerUsePreview ResponsesToolType = "computer_use_preview"
	ResponsesToolTypeWebSearch          ResponsesToolType = "web_search"
	ResponsesToolTypeMCP                ResponsesToolType = "mcp"
	ResponsesToolTypeCodeInterpreter    ResponsesToolType = "code_interpreter"
	ResponsesToolTypeImageGeneration    ResponsesToolType = "image_generation"
	ResponsesToolTypeLocalShell         ResponsesToolType = "local_shell"
	ResponsesToolTypeCustom             ResponsesToolType = "custom"
	ResponsesToolTypeWebSearchPreview   ResponsesToolType = "web_search_preview"
)

// ResponsesTool represents a tool
type ResponsesTool struct {
	Type        ResponsesToolType `json:"type"`                  // "function" | "file_search" | "computer_use_preview" | "web_search" | "web_search_2025_08_26" | "mcp" | "code_interpreter" | "image_generation" | "local_shell" | "custom" | "web_search_preview" | "web_search_preview_2025_03_11"
	Name        *string           `json:"name,omitempty"`        // Common name field (Function, Custom tools)
	Description *string           `json:"description,omitempty"` // Common description field (Function, Custom tools)

	// Not in OpenAI's schemas, but sent by a few providers (Anthropic, Bedrock are some of them)
	CacheControl *CacheControl `json:"cache_control,omitempty"`

	*ResponsesToolFunction
	*ResponsesToolFileSearch
	*ResponsesToolComputerUsePreview
	*ResponsesToolWebSearch
	*ResponsesToolMCP
	*ResponsesToolCodeInterpreter
	*ResponsesToolImageGeneration
	*ResponsesToolLocalShell
	*ResponsesToolCustom
	*ResponsesToolWebSearchPreview
}

// MarshalJSON implements custom JSON marshaling for ResponsesTool
// It merges common fields with the appropriate embedded struct based on type
func (t ResponsesTool) MarshalJSON() ([]byte, error) {
	// Start with common fields
	result := map[string]interface{}{
		"type": t.Type,
	}

	if t.Name != nil {
		result["name"] = t.Name
	}
	if t.Description != nil {
		result["description"] = t.Description
	}
	if t.CacheControl != nil {
		result["cache_control"] = t.CacheControl
	}

	// Based on type, marshal the appropriate embedded struct
	switch t.Type {
	case ResponsesToolTypeFunction:
		if t.ResponsesToolFunction != nil {
			bytes, err := Marshal(t.ResponsesToolFunction)
			if err != nil {
				return nil, err
			}
			var funcFields map[string]interface{}
			if err := Unmarshal(bytes, &funcFields); err != nil {
				return nil, err
			}
			for k, v := range funcFields {
				result[k] = v
			}
		}

	case ResponsesToolTypeFileSearch:
		if t.ResponsesToolFileSearch != nil {
			bytes, err := Marshal(t.ResponsesToolFileSearch)
			if err != nil {
				return nil, err
			}
			var fileSearchFields map[string]interface{}
			if err := Unmarshal(bytes, &fileSearchFields); err != nil {
				return nil, err
			}
			for k, v := range fileSearchFields {
				result[k] = v
			}
		}

	case ResponsesToolTypeComputerUsePreview:
		if t.ResponsesToolComputerUsePreview != nil {
			bytes, err := Marshal(t.ResponsesToolComputerUsePreview)
			if err != nil {
				return nil, err
			}
			var computerFields map[string]interface{}
			if err := Unmarshal(bytes, &computerFields); err != nil {
				return nil, err
			}
			for k, v := range computerFields {
				result[k] = v
			}
		}

	case ResponsesToolTypeWebSearch:
		if t.ResponsesToolWebSearch != nil {
			bytes, err := Marshal(t.ResponsesToolWebSearch)
			if err != nil {
				return nil, err
			}
			var webSearchFields map[string]interface{}
			if err := Unmarshal(bytes, &webSearchFields); err != nil {
				return nil, err
			}
			for k, v := range webSearchFields {
				result[k] = v
			}
		}

	case ResponsesToolTypeMCP:
		if t.ResponsesToolMCP != nil {
			bytes, err := Marshal(t.ResponsesToolMCP)
			if err != nil {
				return nil, err
			}
			var mcpFields map[string]interface{}
			if err := Unmarshal(bytes, &mcpFields); err != nil {
				return nil, err
			}
			for k, v := range mcpFields {
				result[k] = v
			}
		}

	case ResponsesToolTypeCodeInterpreter:
		if t.ResponsesToolCodeInterpreter != nil {
			bytes, err := Marshal(t.ResponsesToolCodeInterpreter)
			if err != nil {
				return nil, err
			}
			var codeInterpreterFields map[string]interface{}
			if err := Unmarshal(bytes, &codeInterpreterFields); err != nil {
				return nil, err
			}
			for k, v := range codeInterpreterFields {
				result[k] = v
			}
		}

	case ResponsesToolTypeImageGeneration:
		if t.ResponsesToolImageGeneration != nil {
			bytes, err := Marshal(t.ResponsesToolImageGeneration)
			if err != nil {
				return nil, err
			}
			var imageGenFields map[string]interface{}
			if err := Unmarshal(bytes, &imageGenFields); err != nil {
				return nil, err
			}
			for k, v := range imageGenFields {
				result[k] = v
			}
		}

	case ResponsesToolTypeLocalShell:
		if t.ResponsesToolLocalShell != nil {
			bytes, err := Marshal(t.ResponsesToolLocalShell)
			if err != nil {
				return nil, err
			}
			var localShellFields map[string]interface{}
			if err := Unmarshal(bytes, &localShellFields); err != nil {
				return nil, err
			}
			for k, v := range localShellFields {
				result[k] = v
			}
		}

	case ResponsesToolTypeCustom:
		if t.ResponsesToolCustom != nil {
			bytes, err := Marshal(t.ResponsesToolCustom)
			if err != nil {
				return nil, err
			}
			var customFields map[string]interface{}
			if err := Unmarshal(bytes, &customFields); err != nil {
				return nil, err
			}
			for k, v := range customFields {
				result[k] = v
			}
		}

	case ResponsesToolTypeWebSearchPreview:
		if t.ResponsesToolWebSearchPreview != nil {
			bytes, err := Marshal(t.ResponsesToolWebSearchPreview)
			if err != nil {
				return nil, err
			}
			var webSearchPreviewFields map[string]interface{}
			if err := Unmarshal(bytes, &webSearchPreviewFields); err != nil {
				return nil, err
			}
			for k, v := range webSearchPreviewFields {
				result[k] = v
			}
		}
	}

	return Marshal(result)
}

// UnmarshalJSON implements custom JSON unmarshaling for ResponsesTool
// It unmarshals common fields first, then the appropriate embedded struct based on type
func (t *ResponsesTool) UnmarshalJSON(data []byte) error {
	// First unmarshal into a map to inspect the type
	var raw map[string]interface{}
	if err := Unmarshal(data, &raw); err != nil {
		return err
	}

	// Extract type field
	typeValue, ok := raw["type"]
	if !ok {
		return fmt.Errorf("missing required 'type' field in ResponsesTool")
	}

	typeStr, ok := typeValue.(string)
	if !ok {
		return fmt.Errorf("'type' field must be a string")
	}
	t.Type = ResponsesToolType(typeStr)

	// Unmarshal common fields
	if name, ok := raw["name"].(string); ok {
		t.Name = &name
	}
	if description, ok := raw["description"].(string); ok {
		t.Description = &description
	}
	if cacheControl, ok := raw["cache_control"]; ok {
		bytes, err := Marshal(cacheControl)
		if err != nil {
			return err
		}
		var cc CacheControl
		if err := Unmarshal(bytes, &cc); err != nil {
			return err
		}
		t.CacheControl = &cc
	}

	// Based on type, unmarshal into the appropriate embedded struct
	switch t.Type {
	case ResponsesToolTypeFunction:
		var funcTool ResponsesToolFunction
		if err := Unmarshal(data, &funcTool); err != nil {
			return err
		}
		t.ResponsesToolFunction = &funcTool

	case ResponsesToolTypeFileSearch:
		var fileSearchTool ResponsesToolFileSearch
		if err := Unmarshal(data, &fileSearchTool); err != nil {
			return err
		}
		t.ResponsesToolFileSearch = &fileSearchTool

	case ResponsesToolTypeComputerUsePreview:
		var computerTool ResponsesToolComputerUsePreview
		if err := Unmarshal(data, &computerTool); err != nil {
			return err
		}
		t.ResponsesToolComputerUsePreview = &computerTool

	case ResponsesToolTypeWebSearch:
		var webSearchTool ResponsesToolWebSearch
		if err := Unmarshal(data, &webSearchTool); err != nil {
			return err
		}
		t.ResponsesToolWebSearch = &webSearchTool

	case ResponsesToolTypeMCP:
		var mcpTool ResponsesToolMCP
		if err := Unmarshal(data, &mcpTool); err != nil {
			return err
		}
		t.ResponsesToolMCP = &mcpTool

	case ResponsesToolTypeCodeInterpreter:
		var codeInterpreterTool ResponsesToolCodeInterpreter
		if err := Unmarshal(data, &codeInterpreterTool); err != nil {
			return err
		}
		t.ResponsesToolCodeInterpreter = &codeInterpreterTool

	case ResponsesToolTypeImageGeneration:
		var imageGenTool ResponsesToolImageGeneration
		if err := Unmarshal(data, &imageGenTool); err != nil {
			return err
		}
		t.ResponsesToolImageGeneration = &imageGenTool

	case ResponsesToolTypeLocalShell:
		var localShellTool ResponsesToolLocalShell
		if err := Unmarshal(data, &localShellTool); err != nil {
			return err
		}
		t.ResponsesToolLocalShell = &localShellTool

	case ResponsesToolTypeCustom:
		var customTool ResponsesToolCustom
		if err := Unmarshal(data, &customTool); err != nil {
			return err
		}
		t.ResponsesToolCustom = &customTool

	case ResponsesToolTypeWebSearchPreview:
		var webSearchPreviewTool ResponsesToolWebSearchPreview
		if err := Unmarshal(data, &webSearchPreviewTool); err != nil {
			return err
		}
		t.ResponsesToolWebSearchPreview = &webSearchPreviewTool
	}

	return nil
}

// ResponsesToolFunction represents a tool function
type ResponsesToolFunction struct {
	Parameters *ToolFunctionParameters `json:"parameters,omitempty"` // A JSON schema object describing the parameters
	Strict     *bool                   `json:"strict"`               // Whether to enforce strict parameter validation
}

// ResponsesToolFileSearch represents a tool file search
type ResponsesToolFileSearch struct {
	VectorStoreIDs []string                               `json:"vector_store_ids"`          // The IDs of the vector stores to search
	Filters        *ResponsesToolFileSearchFilter         `json:"filters,omitempty"`         // A filter to apply
	MaxNumResults  *int                                   `json:"max_num_results,omitempty"` // Maximum results (1-50)
	RankingOptions *ResponsesToolFileSearchRankingOptions `json:"ranking_options,omitempty"` // Ranking options for search
}

// ResponsesToolFileSearchFilter represents a file search filter
type ResponsesToolFileSearchFilter struct {
	Type string `json:"type"` // "eq" | "ne" | "gt" | "gte" | "lt" | "lte" | "and" | "or"

	// Filter types - only one should be set
	*ResponsesToolFileSearchComparisonFilter
	*ResponsesToolFileSearchCompoundFilter
}

// MarshalJSON implements custom JSON marshaling for ResponsesToolFileSearchFilter
func (f *ResponsesToolFileSearchFilter) MarshalJSON() ([]byte, error) {
	// Validate that exactly one filter type is set
	if f.ResponsesToolFileSearchComparisonFilter != nil && f.ResponsesToolFileSearchCompoundFilter != nil {
		return nil, fmt.Errorf("both comparison and compound filters are set; only one should be non-nil")
	}
	if f.ResponsesToolFileSearchComparisonFilter == nil && f.ResponsesToolFileSearchCompoundFilter == nil {
		return nil, fmt.Errorf("neither comparison nor compound filter is set; exactly one must be non-nil")
	}

	// Create a map to hold the JSON data
	result := make(map[string]interface{})
	result["type"] = f.Type

	// Marshal the appropriate embedded struct based on type
	switch f.Type {
	case "eq", "ne", "gt", "gte", "lt", "lte":
		if f.ResponsesToolFileSearchComparisonFilter == nil {
			return nil, fmt.Errorf("comparison filter is nil but type is %s", f.Type)
		}
		// Copy fields from the embedded struct
		result["key"] = f.ResponsesToolFileSearchComparisonFilter.Key
		result["value"] = f.ResponsesToolFileSearchComparisonFilter.Value
	case "and", "or":
		if f.ResponsesToolFileSearchCompoundFilter == nil {
			return nil, fmt.Errorf("compound filter is nil but type is %s", f.Type)
		}
		// Copy fields from the embedded struct
		result["filters"] = f.ResponsesToolFileSearchCompoundFilter.Filters
	default:
		return nil, fmt.Errorf("unknown filter type: %s", f.Type)
	}

	return Marshal(result)
}

// UnmarshalJSON implements custom JSON unmarshaling for ResponsesToolFileSearchFilter
func (f *ResponsesToolFileSearchFilter) UnmarshalJSON(data []byte) error {
	// First, unmarshal into a map to inspect the type field
	var raw map[string]interface{}
	if err := Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("failed to unmarshal filter JSON: %w", err)
	}

	// Extract the type field
	typeValue, ok := raw["type"]
	if !ok {
		return fmt.Errorf("missing required 'type' field in filter")
	}

	typeStr, ok := typeValue.(string)
	if !ok {
		return fmt.Errorf("'type' field must be a string, got %T", typeValue)
	}

	f.Type = typeStr

	// Initialize the appropriate embedded struct based on type
	switch typeStr {
	case "eq", "ne", "gt", "gte", "lt", "lte":
		// This is a comparison filter
		f.ResponsesToolFileSearchComparisonFilter = &ResponsesToolFileSearchComparisonFilter{}
		f.ResponsesToolFileSearchCompoundFilter = nil

		// Unmarshal into the comparison filter
		if err := Unmarshal(data, f.ResponsesToolFileSearchComparisonFilter); err != nil {
			return fmt.Errorf("failed to unmarshal comparison filter: %w", err)
		}

		// Validate required fields
		if f.ResponsesToolFileSearchComparisonFilter.Key == "" {
			return fmt.Errorf("comparison filter missing required 'key' field")
		}
		if f.ResponsesToolFileSearchComparisonFilter.Value == nil {
			return fmt.Errorf("comparison filter missing required 'value' field")
		}

	case "and", "or":
		// This is a compound filter
		f.ResponsesToolFileSearchCompoundFilter = &ResponsesToolFileSearchCompoundFilter{}
		f.ResponsesToolFileSearchComparisonFilter = nil

		// Unmarshal into the compound filter
		if err := Unmarshal(data, f.ResponsesToolFileSearchCompoundFilter); err != nil {
			return fmt.Errorf("failed to unmarshal compound filter: %w", err)
		}

		// Validate required fields
		if f.ResponsesToolFileSearchCompoundFilter.Filters == nil {
			return fmt.Errorf("compound filter missing required 'filters' field")
		}
		if len(f.ResponsesToolFileSearchCompoundFilter.Filters) == 0 {
			return fmt.Errorf("compound filter 'filters' array cannot be empty")
		}

	default:
		return fmt.Errorf("unknown filter type: %s (supported types: eq, ne, gt, gte, lt, lte, and, or)", typeStr)
	}

	return nil
}

// ResponsesToolFileSearchComparisonFilter represents a file search comparison filter
type ResponsesToolFileSearchComparisonFilter struct {
	Key   string      `json:"key"`   // The key to compare against the value
	Type  string      `json:"type"`  //
	Value interface{} `json:"value"` // The value to compare (string, number, or boolean)
}

// ResponsesToolFileSearchCompoundFilter represents a file search compound filter
type ResponsesToolFileSearchCompoundFilter struct {
	Filters []ResponsesToolFileSearchFilter `json:"filters"` // Array of filters to combine
}

// ResponsesToolFileSearchRankingOptions represents a file search ranking options
type ResponsesToolFileSearchRankingOptions struct {
	Ranker         *string  `json:"ranker,omitempty"`          // The ranker to use
	ScoreThreshold *float64 `json:"score_threshold,omitempty"` // Score threshold (0-1)
}

// ResponsesToolComputerUsePreview represents a tool computer use preview
type ResponsesToolComputerUsePreview struct {
	DisplayHeight int    `json:"display_height"` // The height of the computer display
	DisplayWidth  int    `json:"display_width"`  // The width of the computer display
	Environment   string `json:"environment"`    // The type of computer environment to control

	EnableZoom *bool `json:"enable_zoom,omitempty"` // for computer tool in anthropic only
}

// ResponsesToolWebSearch represents a tool web search
type ResponsesToolWebSearch struct {
	Filters           *ResponsesToolWebSearchFilters      `json:"filters,omitempty"`             // Filters for the search
	SearchContextSize *string                             `json:"search_context_size,omitempty"` // "low" | "medium" | "high"
	UserLocation      *ResponsesToolWebSearchUserLocation `json:"user_location,omitempty"`       // The approximate location of the user

	// Anthropic only
	MaxUses *int `json:"max_uses,omitempty"` // Maximum number of uses for the search
}

// ResponsesToolWebSearchFilters represents filters for web search
type ResponsesToolWebSearchFilters struct {
	AllowedDomains []string `json:"allowed_domains,omitempty"` // Allowed domains for the search
	BlockedDomains []string `json:"blocked_domains,omitempty"` // Blocked domains for the search, only used in anthropic

	// Gemini only
	// Filter search results to a specific time range.
	// If users set a start time, they must set an end time (and vice versa).
	TimeRangeFilter *Interval `json:"time_range_filter,omitempty"`
}

// Interval represents a time interval, encoded as a start time (inclusive) and an end time (exclusive).
// The start time must be less than or equal to the end time.
// When the start equals the end time, the interval is an empty interval.
// (matches no time)
// When both start and end are unspecified, the interval matches any time.
type Interval struct {
	// Optional. The start time of the interval.
	StartTime time.Time `json:"start_time,omitempty"`
	// Optional. The end time of the interval.
	EndTime time.Time `json:"end_time,omitempty"`
}

func (i *Interval) UnmarshalJSON(data []byte) error {
	type Alias Interval
	aux := &struct {
		StartTime *time.Time `json:"start_time,omitempty"`
		EndTime   *time.Time `json:"end_time,omitempty"`
		*Alias
	}{
		Alias: (*Alias)(i),
	}

	if err := Unmarshal(data, &aux); err != nil {
		return err
	}

	if !reflect.ValueOf(aux.StartTime).IsZero() {
		i.StartTime = time.Time(*aux.StartTime)
	}

	if !reflect.ValueOf(aux.EndTime).IsZero() {
		i.EndTime = time.Time(*aux.EndTime)
	}

	return nil
}

func (i *Interval) MarshalJSON() ([]byte, error) {
	type Alias Interval
	aux := &struct {
		StartTime *time.Time `json:"start_time,omitempty"`
		EndTime   *time.Time `json:"end_time,omitempty"`
		*Alias
	}{
		Alias: (*Alias)(i),
	}

	if !reflect.ValueOf(i.StartTime).IsZero() {
		aux.StartTime = (*time.Time)(&i.StartTime)
	}

	if !reflect.ValueOf(i.EndTime).IsZero() {
		aux.EndTime = (*time.Time)(&i.EndTime)
	}

	return Marshal(aux)
}

// ResponsesToolWebSearchUserLocation - The approximate location of the user
type ResponsesToolWebSearchUserLocation struct {
	City     *string `json:"city,omitempty"`     // Free text input for the city
	Country  *string `json:"country,omitempty"`  // Two-letter ISO country code
	Region   *string `json:"region,omitempty"`   // Free text input for the region
	Timezone *string `json:"timezone,omitempty"` // IANA timezone
	Type     *string `json:"type,omitempty"`     // always "approximate"
}

// ResponsesToolMCP - Give the model access to additional tools via remote MCP servers
type ResponsesToolMCP struct {
	ServerLabel       string                                       `json:"server_label"`                 // A label for this MCP server
	AllowedTools      *ResponsesToolMCPAllowedTools                `json:"allowed_tools,omitempty"`      // List of allowed tool names or filter
	Authorization     *string                                      `json:"authorization,omitempty"`      // OAuth access token
	ConnectorID       *string                                      `json:"connector_id,omitempty"`       // Service connector ID
	Headers           *map[string]string                           `json:"headers,omitempty"`            // Optional HTTP headers
	RequireApproval   *ResponsesToolMCPAllowedToolsApprovalSetting `json:"require_approval,omitempty"`   // Tool approval settings
	ServerDescription *string                                      `json:"server_description,omitempty"` // Optional server description
	ServerURL         *string                                      `json:"server_url,omitempty"`         // The URL for the MCP server
}

// ResponsesToolMCPAllowedTools - List of allowed tool names or a filter object
type ResponsesToolMCPAllowedTools struct {
	// Either a simple array of tool names or a filter object
	ToolNames []string                            `json:",omitempty"`
	Filter    *ResponsesToolMCPAllowedToolsFilter `json:",omitempty"`
}

// ResponsesToolMCPAllowedToolsFilter - A filter object to specify which tools are allowed
type ResponsesToolMCPAllowedToolsFilter struct {
	ReadOnly  *bool    `json:"read_only,omitempty"`  // Whether tool is read-only
	ToolNames []string `json:"tool_names,omitempty"` // List of allowed tool names
}

// ResponsesToolMCPAllowedToolsApprovalSetting - Specify which tools require approval
type ResponsesToolMCPAllowedToolsApprovalSetting struct {
	// Either a string setting or filter objects
	Setting *string                                     `json:",omitempty"` // "always" | "never"
	Always  *ResponsesToolMCPAllowedToolsApprovalFilter `json:"always,omitempty"`
	Never   *ResponsesToolMCPAllowedToolsApprovalFilter `json:"never,omitempty"`
}

// MarshalJSON implements custom JSON marshalling for ResponsesToolMCPAllowedToolsApprovalSetting
func (as ResponsesToolMCPAllowedToolsApprovalSetting) MarshalJSON() ([]byte, error) {
	// Validation: ensure only one representation is set
	if as.Setting != nil && (as.Always != nil || as.Never != nil) {
		return nil, fmt.Errorf("only one of 'Setting' or ('Always'/'Never') can be set")
	}

	if as.Setting != nil {
		return Marshal(*as.Setting)
	}
	if as.Always != nil || as.Never != nil {
		// Marshal as an object with always/never fields
		obj := make(map[string]interface{})
		if as.Always != nil {
			obj["always"] = as.Always
		}
		if as.Never != nil {
			obj["never"] = as.Never
		}
		return Marshal(obj)
	}
	// If all are nil, return null
	return Marshal(nil)
}

// UnmarshalJSON implements custom JSON unmarshalling for ResponsesToolMCPAllowedToolsApprovalSetting
func (as *ResponsesToolMCPAllowedToolsApprovalSetting) UnmarshalJSON(data []byte) error {
	// First, try to unmarshal as a direct string
	var settingStr string
	if err := Unmarshal(data, &settingStr); err == nil {
		as.Setting = &settingStr
		return nil
	}

	// Try to unmarshal as an object with always/never fields
	var obj struct {
		Always *ResponsesToolMCPAllowedToolsApprovalFilter `json:"always,omitempty"`
		Never  *ResponsesToolMCPAllowedToolsApprovalFilter `json:"never,omitempty"`
	}
	if err := Unmarshal(data, &obj); err == nil {
		as.Always = obj.Always
		as.Never = obj.Never
		return nil
	}

	return fmt.Errorf("require_approval field is neither a string nor an object with always/never filters")
}

// ResponsesToolMCPAllowedToolsApprovalFilter - Filter for approval settings
type ResponsesToolMCPAllowedToolsApprovalFilter struct {
	ReadOnly  *bool    `json:"read_only,omitempty"`  // Whether tool is read-only
	ToolNames []string `json:"tool_names,omitempty"` // List of tool names
}

// ResponsesToolCodeInterpreter represents a tool code interpreter
type ResponsesToolCodeInterpreter struct {
	Container interface{} `json:"container"` // Container ID or object with file IDs
}

// ResponsesToolImageGeneration represents a tool image generation
type ResponsesToolImageGeneration struct {
	Background        *string                                     `json:"background,omitempty"`         // "transparent" | "opaque" | "auto"
	InputFidelity     *string                                     `json:"input_fidelity,omitempty"`     // "high" | "low"
	InputImageMask    *ResponsesToolImageGenerationInputImageMask `json:"input_image_mask,omitempty"`   // Optional mask for inpainting
	Model             *string                                     `json:"model,omitempty"`              // Image generation model
	Moderation        *string                                     `json:"moderation,omitempty"`         // Moderation level
	OutputCompression *int                                        `json:"output_compression,omitempty"` // Compression level (0-100)
	OutputFormat      *string                                     `json:"output_format,omitempty"`      // "png" | "webp" | "jpeg"
	PartialImages     *int                                        `json:"partial_images,omitempty"`     // Number of partial images (0-3)
	Quality           *string                                     `json:"quality,omitempty"`            // "low" | "medium" | "high" | "auto"
	Size              *string                                     `json:"size,omitempty"`               // Image size
}

// ResponsesToolImageGenerationInputImageMask represents a image generation input image mask
type ResponsesToolImageGenerationInputImageMask struct {
	FileID   *string `json:"file_id,omitempty"`   // File ID for the mask image
	ImageURL *string `json:"image_url,omitempty"` // Base64-encoded mask image
}

// ResponsesToolLocalShell represents a tool local shell
type ResponsesToolLocalShell struct {
	// No unique fields needed since Type is now in the top-level struct
}

// ResponsesToolCustom represents a custom tool
type ResponsesToolCustom struct {
	Format *ResponsesToolCustomFormat `json:"format,omitempty"` // The input format
}

// ResponsesToolCustomFormat represents the input format for the custom tool
type ResponsesToolCustomFormat struct {
	Type string `json:"type"` // always "text"

	// For Grammar
	Definition *string `json:"definition,omitempty"` // The grammar definition
	Syntax     *string `json:"syntax,omitempty"`     // "lark" | "regex"
}

// ResponsesToolWebSearchPreview represents a web search preview
type ResponsesToolWebSearchPreview struct {
	SearchContextSize *string                             `json:"search_context_size,omitempty"` // "low" | "medium" | "high"
	UserLocation      *ResponsesToolWebSearchUserLocation `json:"user_location,omitempty"`       // The user's location
}

// ======================================================= Streaming Structs =======================================================

type ResponsesStreamResponseType string

const (
	// Ping events are just keepalive (sent by very few providers, Anthropic is one of them)
	ResponsesStreamResponseTypePing ResponsesStreamResponseType = "response.ping"

	ResponsesStreamResponseTypeCreated    ResponsesStreamResponseType = "response.created"
	ResponsesStreamResponseTypeInProgress ResponsesStreamResponseType = "response.in_progress"
	ResponsesStreamResponseTypeCompleted  ResponsesStreamResponseType = "response.completed"
	ResponsesStreamResponseTypeFailed     ResponsesStreamResponseType = "response.failed"
	ResponsesStreamResponseTypeIncomplete ResponsesStreamResponseType = "response.incomplete"

	ResponsesStreamResponseTypeOutputItemAdded ResponsesStreamResponseType = "response.output_item.added"
	ResponsesStreamResponseTypeOutputItemDone  ResponsesStreamResponseType = "response.output_item.done"

	ResponsesStreamResponseTypeContentPartAdded ResponsesStreamResponseType = "response.content_part.added"
	ResponsesStreamResponseTypeContentPartDone  ResponsesStreamResponseType = "response.content_part.done"

	ResponsesStreamResponseTypeOutputTextDelta ResponsesStreamResponseType = "response.output_text.delta"
	ResponsesStreamResponseTypeOutputTextDone  ResponsesStreamResponseType = "response.output_text.done"

	ResponsesStreamResponseTypeRefusalDelta ResponsesStreamResponseType = "response.refusal.delta"
	ResponsesStreamResponseTypeRefusalDone  ResponsesStreamResponseType = "response.refusal.done"

	ResponsesStreamResponseTypeFunctionCallArgumentsDelta     ResponsesStreamResponseType = "response.function_call_arguments.delta"
	ResponsesStreamResponseTypeFunctionCallArgumentsDone      ResponsesStreamResponseType = "response.function_call_arguments.done"
	ResponsesStreamResponseTypeFileSearchCallInProgress       ResponsesStreamResponseType = "response.file_search_call.in_progress"
	ResponsesStreamResponseTypeFileSearchCallSearching        ResponsesStreamResponseType = "response.file_search_call.searching"
	ResponsesStreamResponseTypeFileSearchCallResultsAdded     ResponsesStreamResponseType = "response.file_search_call.results.added"
	ResponsesStreamResponseTypeFileSearchCallResultsCompleted ResponsesStreamResponseType = "response.file_search_call.results.completed"
	ResponsesStreamResponseTypeWebSearchCallInProgress        ResponsesStreamResponseType = "response.web_search_call.in_progress"
	ResponsesStreamResponseTypeWebSearchCallSearching         ResponsesStreamResponseType = "response.web_search_call.searching"
	ResponsesStreamResponseTypeWebSearchCallCompleted         ResponsesStreamResponseType = "response.web_search_call.completed"
	ResponsesStreamResponseTypeWebSearchCallResultsAdded      ResponsesStreamResponseType = "response.web_search_call.results.added"
	ResponsesStreamResponseTypeWebSearchCallResultsCompleted  ResponsesStreamResponseType = "response.web_search_call.results.completed"

	ResponsesStreamResponseTypeReasoningSummaryPartAdded ResponsesStreamResponseType = "response.reasoning_summary_part.added"
	ResponsesStreamResponseTypeReasoningSummaryPartDone  ResponsesStreamResponseType = "response.reasoning_summary_part.done"
	ResponsesStreamResponseTypeReasoningSummaryTextDelta ResponsesStreamResponseType = "response.reasoning_summary_text.delta"
	ResponsesStreamResponseTypeReasoningSummaryTextDone  ResponsesStreamResponseType = "response.reasoning_summary_text.done"

	ResponsesStreamResponseTypeImageGenerationCallCompleted    ResponsesStreamResponseType = "response.image_generation_call.completed"
	ResponsesStreamResponseTypeImageGenerationCallGenerating   ResponsesStreamResponseType = "response.image_generation_call.generating"
	ResponsesStreamResponseTypeImageGenerationCallInProgress   ResponsesStreamResponseType = "response.image_generation_call.in_progress"
	ResponsesStreamResponseTypeImageGenerationCallPartialImage ResponsesStreamResponseType = "response.image_generation_call.partial_image"

	ResponsesStreamResponseTypeMCPCallArgumentsDelta  ResponsesStreamResponseType = "response.mcp_call_arguments.delta"
	ResponsesStreamResponseTypeMCPCallArgumentsDone   ResponsesStreamResponseType = "response.mcp_call_arguments.done"
	ResponsesStreamResponseTypeMCPCallCompleted       ResponsesStreamResponseType = "response.mcp_call.completed"
	ResponsesStreamResponseTypeMCPCallFailed          ResponsesStreamResponseType = "response.mcp_call.failed"
	ResponsesStreamResponseTypeMCPCallInProgress      ResponsesStreamResponseType = "response.mcp_call.in_progress"
	ResponsesStreamResponseTypeMCPListToolsCompleted  ResponsesStreamResponseType = "response.mcp_list_tools.completed"
	ResponsesStreamResponseTypeMCPListToolsFailed     ResponsesStreamResponseType = "response.mcp_list_tools.failed"
	ResponsesStreamResponseTypeMCPListToolsInProgress ResponsesStreamResponseType = "response.mcp_list_tools.in_progress"

	ResponsesStreamResponseTypeCodeInterpreterCallInProgress   ResponsesStreamResponseType = "response.code_interpreter_call.in_progress"
	ResponsesStreamResponseTypeCodeInterpreterCallInterpreting ResponsesStreamResponseType = "response.code_interpreter_call.interpreting"
	ResponsesStreamResponseTypeCodeInterpreterCallCompleted    ResponsesStreamResponseType = "response.code_interpreter_call.completed"
	ResponsesStreamResponseTypeCodeInterpreterCallCodeDelta    ResponsesStreamResponseType = "response.code_interpreter_call_code.delta"
	ResponsesStreamResponseTypeCodeInterpreterCallCodeDone     ResponsesStreamResponseType = "response.code_interpreter_call_code.done"

	ResponsesStreamResponseTypeOutputTextAnnotationAdded ResponsesStreamResponseType = "response.output_text.annotation.added"
	ResponsesStreamResponseTypeOutputTextAnnotationDone  ResponsesStreamResponseType = "response.output_text.annotation.done"

	ResponsesStreamResponseTypeQueued ResponsesStreamResponseType = "response.queued"

	ResponsesStreamResponseTypeCustomToolCallInputDelta ResponsesStreamResponseType = "response.custom_tool_call_input.delta"
	ResponsesStreamResponseTypeCustomToolCallInputDone  ResponsesStreamResponseType = "response.custom_tool_call_input.done"

	ResponsesStreamResponseTypeError ResponsesStreamResponseType = "error"
)

type BifrostResponsesStreamResponse struct {
	Type           ResponsesStreamResponseType `json:"type"`
	SequenceNumber int                         `json:"sequence_number"`

	Response *BifrostResponsesResponse `json:"response,omitempty"`

	OutputIndex *int              `json:"output_index,omitempty"`
	Item        *ResponsesMessage `json:"item"`

	ContentIndex *int                          `json:"content_index,omitempty"`
	ItemID       *string                       `json:"item_id,omitempty"`
	Part         *ResponsesMessageContentBlock `json:"part,omitempty"`

	Delta     *string                                    `json:"delta,omitempty"`
	Signature *string                                    `json:"signature,omitempty"` // Not in OpenAI's spec, but sent by other providers
	LogProbs  []ResponsesOutputMessageContentTextLogProb `json:"logprobs"`

	Text *string `json:"text,omitempty"` // Full text of the output item, comes with event "response.output_text.done"

	Refusal *string `json:"refusal,omitempty"`

	Arguments *string `json:"arguments,omitempty"`

	PartialImageB64   *string `json:"partial_image_b64,omitempty"`
	PartialImageIndex *int    `json:"partial_image_index,omitempty"`

	Annotation      *ResponsesOutputMessageContentTextAnnotation `json:"annotation,omitempty"`
	AnnotationIndex *int                                         `json:"annotation_index,omitempty"`

	Code    *string `json:"code,omitempty"`
	Message *string `json:"message,omitempty"`
	Param   *string `json:"param,omitempty"`

	ExtraFields BifrostResponseExtraFields `json:"extra_fields"`

	// Perplexity-specific fields
	SearchResults []SearchResult `json:"search_results,omitempty"`
	Videos        []VideoResult  `json:"videos,omitempty"`
	Citations     []string       `json:"citations,omitempty"`
}

func (resp *BifrostResponsesStreamResponse) WithDefaults() *BifrostResponsesStreamResponse {
	if resp == nil {
		return nil
	}

	// Filter out non-OpenAI response types
	if resp.Type == ResponsesStreamResponseTypePing {
		return nil
	}

	result := &BifrostResponsesStreamResponse{
		Type:           resp.Type,
		SequenceNumber: resp.SequenceNumber,
	}

	// Copy nested response (applies defaults)
	result.Response = resp.Response.WithDefaults()

	// Copy all streaming-specific fields
	result.OutputIndex = resp.OutputIndex
	result.Item = resp.Item
	result.ContentIndex = resp.ContentIndex
	result.ItemID = resp.ItemID
	result.Part = resp.Part
	result.Delta = resp.Delta
	result.Signature = resp.Signature
	result.Text = resp.Text
	result.Refusal = resp.Refusal
	result.Arguments = resp.Arguments
	result.PartialImageB64 = resp.PartialImageB64
	result.PartialImageIndex = resp.PartialImageIndex
	result.Annotation = resp.Annotation
	result.AnnotationIndex = resp.AnnotationIndex
	result.Code = resp.Code
	result.Message = resp.Message
	result.Param = resp.Param
	result.LogProbs = resp.LogProbs

	// Apply event-specific defaults
	switch resp.Type {
	case ResponsesStreamResponseTypeOutputItemAdded:
		// Default item status to "in_progress"
		if result.Item != nil && result.Item.Status == nil {
			result.Item.Status = Ptr("in_progress")
		}

	case ResponsesStreamResponseTypeOutputTextDelta, ResponsesStreamResponseTypeOutputTextDone:
		// Ensure logprobs array exists
		if result.LogProbs == nil {
			result.LogProbs = []ResponsesOutputMessageContentTextLogProb{}
		}

	case ResponsesStreamResponseTypeContentPartAdded, ResponsesStreamResponseTypeContentPartDone:
		// Ensure part has proper structure
		if result.Part == nil {
			result.Part = &ResponsesMessageContentBlock{
				Type: ResponsesOutputMessageContentTypeText,
				Text: Ptr(""),
				ResponsesOutputMessageContentText: &ResponsesOutputMessageContentText{
					LogProbs:    []ResponsesOutputMessageContentTextLogProb{},
					Annotations: []ResponsesOutputMessageContentTextAnnotation{},
				},
			}
		} else if result.Part.ResponsesOutputMessageContentText == nil {
			result.Part.ResponsesOutputMessageContentText = &ResponsesOutputMessageContentText{
				LogProbs:    []ResponsesOutputMessageContentTextLogProb{},
				Annotations: []ResponsesOutputMessageContentTextAnnotation{},
			}
		} else {
			// Ensure nested arrays exist
			if result.Part.ResponsesOutputMessageContentText.LogProbs == nil {
				result.Part.ResponsesOutputMessageContentText.LogProbs = []ResponsesOutputMessageContentTextLogProb{}
			}
			if result.Part.ResponsesOutputMessageContentText.Annotations == nil {
				result.Part.ResponsesOutputMessageContentText.Annotations = []ResponsesOutputMessageContentTextAnnotation{}
			}
		}
	}

	return result
}
