package schemas

import (
	"fmt"

	"github.com/bytedance/sonic"
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
	ID *string `json:"id,omitempty"` // used for internal conversions

	Background         *bool                               `json:"background,omitempty"`
	Conversation       *ResponsesResponseConversation      `json:"conversation,omitempty"`
	CreatedAt          int                                 `json:"created_at"` // Unix timestamp when Response was created
	Error              *ResponsesResponseError             `json:"error,omitempty"`
	Include            []string                            `json:"include,omitempty"`            // Supported values: "web_search_call.action.sources", "code_interpreter_call.outputs", "computer_call_output.output.image_url", "file_search_call.results", "message.input_image.image_url", "message.output_text.logprobs", "reasoning.encrypted_content"
	IncompleteDetails  *ResponsesResponseIncompleteDetails `json:"incomplete_details,omitempty"` // Details about why the response is incomplete
	Instructions       *ResponsesResponseInstructions      `json:"instructions,omitempty"`
	MaxOutputTokens    *int                                `json:"max_output_tokens,omitempty"`
	MaxToolCalls       *int                                `json:"max_tool_calls,omitempty"`
	Metadata           *map[string]any                     `json:"metadata,omitempty"`
	Model              string                              `json:"model"`
	Output             []ResponsesMessage                  `json:"output,omitempty"`
	ParallelToolCalls  *bool                               `json:"parallel_tool_calls,omitempty"`
	PreviousResponseID *string                             `json:"previous_response_id,omitempty"`
	Prompt             *ResponsesPrompt                    `json:"prompt,omitempty"`            // Reference to a prompt template and variables
	PromptCacheKey     *string                             `json:"prompt_cache_key,omitempty"`  // Prompt cache key
	Reasoning          *ResponsesParametersReasoning       `json:"reasoning,omitempty"`         // Configuration options for reasoning models
	SafetyIdentifier   *string                             `json:"safety_identifier,omitempty"` // Safety identifier
	ServiceTier        *string                             `json:"service_tier,omitempty"`
	Status             *string                             `json:"status,omitempty"` // completed, failed, in_progress, cancelled, queued, or incomplete
	StreamOptions      *ResponsesStreamOptions             `json:"stream_options,omitempty"`
	StopReason         *string                             `json:"stop_reason,omitempty"` // Not in OpenAI's spec, but sent by other providers
	Store              *bool                               `json:"store,omitempty"`
	Temperature        *float64                            `json:"temperature,omitempty"`
	Text               *ResponsesTextConfig                `json:"text,omitempty"`
	TopLogProbs        *int                                `json:"top_logprobs,omitempty"`
	TopP               *float64                            `json:"top_p,omitempty"`       // Controls diversity via nucleus sampling
	ToolChoice         *ResponsesToolChoice                `json:"tool_choice,omitempty"` // Whether to call a tool
	Tools              []ResponsesTool                     `json:"tools,omitempty"`       // Tools to use
	Truncation         *string                             `json:"truncation,omitempty"`
	Usage              *ResponsesResponseUsage             `json:"usage,omitempty"`
	ExtraFields        BifrostResponseExtraFields          `json:"extra_fields"`

	// Perplexity-specific fields
	SearchResults []SearchResult `json:"search_results,omitempty"`
	Videos        []VideoResult  `json:"videos,omitempty"`
	Citations     []string       `json:"citations,omitempty"`
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
		return sonic.Marshal(*rc.ResponsesResponseConversationStr)
	}
	if rc.ResponsesResponseConversationStruct != nil {
		return sonic.Marshal(rc.ResponsesResponseConversationStruct)
	}
	// If both are nil, return null
	return sonic.Marshal(nil)
}

// UnmarshalJSON implements custom JSON unmarshalling for ResponsesMessageContent.
// It determines whether "content" is a string or array and assigns to the appropriate field.
// It also handles direct string/array content without a wrapper object.
func (rc *ResponsesResponseConversation) UnmarshalJSON(data []byte) error {
	// First, try to unmarshal as a direct string
	var stringContent string
	if err := sonic.Unmarshal(data, &stringContent); err == nil {
		rc.ResponsesResponseConversationStr = &stringContent
		return nil
	}

	// Try to unmarshal as a direct array of ContentBlock
	var structContent ResponsesResponseConversationStruct
	if err := sonic.Unmarshal(data, &structContent); err == nil {
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
		return sonic.Marshal(*rc.ResponsesResponseInstructionsStr)
	}
	if rc.ResponsesResponseInstructionsArray != nil {
		return sonic.Marshal(rc.ResponsesResponseInstructionsArray)
	}
	// If both are nil, return null
	return sonic.Marshal(nil)
}

// UnmarshalJSON implements custom JSON unmarshalling for ResponsesMessageContent.
// It determines whether "content" is a string or array and assigns to the appropriate field.
// It also handles direct string/array content without a wrapper object.
func (rc *ResponsesResponseInstructions) UnmarshalJSON(data []byte) error {
	// First, try to unmarshal as a direct string
	var stringContent string
	if err := sonic.Unmarshal(data, &stringContent); err == nil {
		rc.ResponsesResponseInstructionsStr = &stringContent
		return nil
	}

	// Try to unmarshal as a direct array of ContentBlock
	var arrayContent []ResponsesMessage
	if err := sonic.Unmarshal(data, &arrayContent); err == nil {
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
	Effort          *string `json:"effort,omitempty"`           // "none" | "minimal" | "low" | "medium" | "high" (any value other than "none" will enable reasoning)
	GenerateSummary *string `json:"generate_summary,omitempty"` // Deprecated: use summary instead
	Summary         *string `json:"summary,omitempty"`          // "auto" | "concise" | "detailed"
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
	InputTokens         int                            `json:"input_tokens"`          // Number of input tokens
	InputTokensDetails  *ResponsesResponseInputTokens  `json:"input_tokens_details"`  // Detailed breakdown of input tokens
	OutputTokens        int                            `json:"output_tokens"`         // Number of output tokens
	OutputTokensDetails *ResponsesResponseOutputTokens `json:"output_tokens_details"` // Detailed breakdown of output tokens	TotalTokens int `json:"total_tokens"` // Total number of tokens used
	TotalTokens         int                            `json:"total_tokens"`          // Total number of tokens used
	Cost                *BifrostCost                   `json:"cost,omitempty"`        // Only for the providers which support cost calculation
}

type ResponsesResponseInputTokens struct {
	TextTokens  int `json:"text_tokens,omitempty"`  // Tokens for text input
	AudioTokens int `json:"audio_tokens,omitempty"` // Tokens for audio input
	ImageTokens int `json:"image_tokens,omitempty"` // Tokens for image input

	// For Providers which follow OpenAI's spec, CachedTokens means the number of input tokens read from the cache+input tokens used to create the cache entry. (because they do not differentiate between cache creation and cache read tokens)
	// For Providers which do not follow OpenAI's spec, CachedTokens means only the number of input tokens read from the cache.
	CachedTokens int `json:"cached_tokens,omitempty"`
}

type ResponsesResponseOutputTokens struct {
	TextTokens               int  `json:"text_tokens,omitempty"`
	AcceptedPredictionTokens int  `json:"accepted_prediction_tokens,omitempty"`
	AudioTokens              int  `json:"audio_tokens,omitempty"`
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
		return sonic.Marshal(*rc.ContentStr)
	}
	if rc.ContentBlocks != nil {
		return sonic.Marshal(rc.ContentBlocks)
	}
	// If both are nil, return null
	return sonic.Marshal(nil)
}

// UnmarshalJSON implements custom JSON unmarshalling for ResponsesMessageContent.
// It determines whether "content" is a string or array and assigns to the appropriate field.
// It also handles direct string/array content without a wrapper object.
func (rc *ResponsesMessageContent) UnmarshalJSON(data []byte) error {
	// First, try to unmarshal as a direct string
	var stringContent string
	if err := sonic.Unmarshal(data, &stringContent); err == nil {
		rc.ContentStr = &stringContent
		return nil
	}

	// Try to unmarshal as a direct array of ContentBlock
	var arrayContent []ResponsesMessageContentBlock
	if err := sonic.Unmarshal(data, &arrayContent); err == nil {
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

	*ResponsesOutputMessageContentText    // Normal text output from the model
	*ResponsesOutputMessageContentRefusal // Model refusal to answer

	// Not in OpenAI's schemas, but sent by a few providers (Anthropic, Bedrock are some of them)
	CacheControl *CacheControl `json:"cache_control,omitempty"`
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
	Annotations []ResponsesOutputMessageContentTextAnnotation `json:"annotations,omitempty"` // Citations and references
	LogProbs    []ResponsesOutputMessageContentTextLogProb    `json:"logprobs,omitempty"`    // Token log probabilities
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
		return sonic.Marshal(action.ResponsesComputerToolCallAction)
	}
	if action.ResponsesWebSearchToolCallAction != nil {
		return sonic.Marshal(action.ResponsesWebSearchToolCallAction)
	}
	if action.ResponsesLocalShellToolCallAction != nil {
		return sonic.Marshal(action.ResponsesLocalShellToolCallAction)
	}
	if action.ResponsesMCPApprovalRequestAction != nil {
		return sonic.Marshal(action.ResponsesMCPApprovalRequestAction)
	}
	return nil, fmt.Errorf("responses tool message action struct is neither a computer tool call action nor a web search tool call action nor a local shell tool call action nor a mcp approval request action")
}

func (action *ResponsesToolMessageActionStruct) UnmarshalJSON(data []byte) error {
	var computerToolCallAction ResponsesComputerToolCallAction
	if err := sonic.Unmarshal(data, &computerToolCallAction); err == nil {
		action.ResponsesComputerToolCallAction = &computerToolCallAction
		return nil
	}
	var webSearchToolCallAction ResponsesWebSearchToolCallAction
	if err := sonic.Unmarshal(data, &webSearchToolCallAction); err == nil {
		action.ResponsesWebSearchToolCallAction = &webSearchToolCallAction
		return nil
	}
	var localShellToolCallAction ResponsesLocalShellToolCallAction
	if err := sonic.Unmarshal(data, &localShellToolCallAction); err == nil {
		action.ResponsesLocalShellToolCallAction = &localShellToolCallAction
		return nil
	}
	var mcpApprovalRequestAction ResponsesMCPApprovalRequestAction
	if err := sonic.Unmarshal(data, &mcpApprovalRequestAction); err == nil {
		action.ResponsesMCPApprovalRequestAction = &mcpApprovalRequestAction
		return nil
	}
	return fmt.Errorf("responses tool message action struct is neither a computer tool call action nor a web search tool call action nor a local shell tool call action nor a mcp approval request action")
}

type ResponsesToolMessageOutputStruct struct {
	ResponsesToolCallOutputStr            *string // Common output string for tool calls and outputs (used by function, custom and local shell tool calls)
	ResponsesFunctionToolCallOutputBlocks []ResponsesMessageContentBlock
	ResponsesComputerToolCallOutput       *ResponsesComputerToolCallOutputData
}

func (output ResponsesToolMessageOutputStruct) MarshalJSON() ([]byte, error) {
	if output.ResponsesToolCallOutputStr != nil {
		return sonic.Marshal(*output.ResponsesToolCallOutputStr)
	}
	if output.ResponsesFunctionToolCallOutputBlocks != nil {
		return sonic.Marshal(output.ResponsesFunctionToolCallOutputBlocks)
	}
	if output.ResponsesComputerToolCallOutput != nil {
		return sonic.Marshal(output.ResponsesComputerToolCallOutput)
	}
	return nil, fmt.Errorf("responses tool message output struct is neither a string nor an array of responses message content blocks nor a computer tool call output data")
}
func (output *ResponsesToolMessageOutputStruct) UnmarshalJSON(data []byte) error {
	var str string
	if err := sonic.Unmarshal(data, &str); err == nil {
		output.ResponsesToolCallOutputStr = &str
		return nil
	}
	var array []ResponsesMessageContentBlock
	if err := sonic.Unmarshal(data, &array); err == nil {
		output.ResponsesFunctionToolCallOutputBlocks = array
		return nil
	}
	var computerToolCallOutput ResponsesComputerToolCallOutputData
	if err := sonic.Unmarshal(data, &computerToolCallOutput); err == nil {
		output.ResponsesComputerToolCallOutput = &computerToolCallOutput
		return nil
	}
	return fmt.Errorf("responses tool message output struct is neither a string nor an array of responses message content blocks nor a computer tool call output data")
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
	Sources []ResponsesWebSearchToolCallActionSearchSource `json:"sources,omitempty"`
	Pattern *string                                        `json:"pattern,omitempty"`
}

// ResponsesWebSearchToolCallActionSearchSource represents a web search action search source
type ResponsesWebSearchToolCallActionSearchSource struct {
	Type string `json:"type"` // always "url"
	URL  string `json:"url"`
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
		return sonic.Marshal(*rf.ResponsesFunctionToolCallOutputStr)
	}
	if rf.ResponsesFunctionToolCallOutputBlocks != nil {
		return sonic.Marshal(rf.ResponsesFunctionToolCallOutputBlocks)
	}
	// If both are nil, return null
	return sonic.Marshal(nil)
}

// UnmarshalJSON implements custom JSON unmarshalling for ResponsesFunctionToolCallOutput.
// It determines whether "content" is a string or array and assigns to the appropriate field.
// It also handles direct string/array content without a wrapper object.
func (rf *ResponsesFunctionToolCallOutput) UnmarshalJSON(data []byte) error {
	// Parse as generic object to check if it contains content-like fields
	var genericObj map[string]interface{}
	if err := sonic.Unmarshal(data, &genericObj); err != nil {
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
	if err := sonic.Unmarshal(data, &stringContent); err == nil {
		rf.ResponsesFunctionToolCallOutputStr = &stringContent
		return nil
	}

	// Try to unmarshal as a direct array of ContentBlock
	var arrayContent []ResponsesMessageContentBlock
	if err := sonic.Unmarshal(data, &arrayContent); err == nil {
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
		return sonic.Marshal(o.ResponsesCodeInterpreterOutputLogs)
	}
	if o.ResponsesCodeInterpreterOutputImage != nil {
		return sonic.Marshal(o.ResponsesCodeInterpreterOutputImage)
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
	if err := sonic.Unmarshal(data, &typeStruct); err != nil {
		return fmt.Errorf("failed to read type field: %w", err)
	}

	// Unmarshal into the appropriate concrete type based on the type field
	switch typeStruct.Type {
	case "logs":
		var logs ResponsesCodeInterpreterOutputLogs
		if err := sonic.Unmarshal(data, &logs); err != nil {
			return fmt.Errorf("failed to unmarshal logs output: %w", err)
		}
		o.ResponsesCodeInterpreterOutputLogs = &logs
		o.ResponsesCodeInterpreterOutputImage = nil
		return nil

	case "image":
		var image ResponsesCodeInterpreterOutputImage
		if err := sonic.Unmarshal(data, &image); err != nil {
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
		return sonic.Marshal(tc.ResponsesToolChoiceStr)
	}
	if tc.ResponsesToolChoiceStruct != nil {
		return sonic.Marshal(tc.ResponsesToolChoiceStruct)
	}
	// If both are nil, return null
	return sonic.Marshal(nil)
}

// UnmarshalJSON implements custom JSON unmarshalling for ChatMessageContent.
// It determines whether "content" is a string or array and assigns to the appropriate field.
// It also handles direct string/array content without a wrapper object.
func (tc *ResponsesToolChoice) UnmarshalJSON(data []byte) error {
	// First, try to unmarshal as a direct string
	var toolChoiceStr string
	if err := sonic.Unmarshal(data, &toolChoiceStr); err == nil {
		tc.ResponsesToolChoiceStr = &toolChoiceStr
		return nil
	}

	// Try to unmarshal as a direct array of ContentBlock
	var responsesToolChoiceStruct ResponsesToolChoiceStruct
	if err := sonic.Unmarshal(data, &responsesToolChoiceStruct); err == nil {
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

// ResponsesToolFunction represents a tool function
type ResponsesToolFunction struct {
	Parameters *ToolFunctionParameters `json:"parameters,omitempty"` // A JSON schema object describing the parameters
	Strict     *bool                   `json:"strict,omitempty"`     // Whether to enforce strict parameter validation
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

	return sonic.Marshal(result)
}

// UnmarshalJSON implements custom JSON unmarshaling for ResponsesToolFileSearchFilter
func (f *ResponsesToolFileSearchFilter) UnmarshalJSON(data []byte) error {
	// First, unmarshal into a map to inspect the type field
	var raw map[string]interface{}
	if err := sonic.Unmarshal(data, &raw); err != nil {
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
		if err := sonic.Unmarshal(data, f.ResponsesToolFileSearchComparisonFilter); err != nil {
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
		if err := sonic.Unmarshal(data, f.ResponsesToolFileSearchCompoundFilter); err != nil {
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
}

// ResponsesToolWebSearchFilters represents filters for web search
type ResponsesToolWebSearchFilters struct {
	AllowedDomains []string `json:"allowed_domains"` // Allowed domains for the search
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
		return sonic.Marshal(*as.Setting)
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
		return sonic.Marshal(obj)
	}
	// If all are nil, return null
	return sonic.Marshal(nil)
}

// UnmarshalJSON implements custom JSON unmarshalling for ResponsesToolMCPAllowedToolsApprovalSetting
func (as *ResponsesToolMCPAllowedToolsApprovalSetting) UnmarshalJSON(data []byte) error {
	// First, try to unmarshal as a direct string
	var settingStr string
	if err := sonic.Unmarshal(data, &settingStr); err == nil {
		as.Setting = &settingStr
		return nil
	}

	// Try to unmarshal as an object with always/never fields
	var obj struct {
		Always *ResponsesToolMCPAllowedToolsApprovalFilter `json:"always,omitempty"`
		Never  *ResponsesToolMCPAllowedToolsApprovalFilter `json:"never,omitempty"`
	}
	if err := sonic.Unmarshal(data, &obj); err == nil {
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
	ResponsesStreamResponseTypeWebSearchCallSearching         ResponsesStreamResponseType = "response.web_search_call.searching"
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
	Item        *ResponsesMessage `json:"item,omitempty"`

	ContentIndex *int                          `json:"content_index,omitempty"`
	ItemID       *string                       `json:"item_id,omitempty"`
	Part         *ResponsesMessageContentBlock `json:"part,omitempty"`

	Delta     *string                                    `json:"delta,omitempty"`
	Signature *string                                    `json:"signature,omitempty"` // Not in OpenAI's spec, but sent by other providers
	LogProbs  []ResponsesOutputMessageContentTextLogProb `json:"logprobs,omitempty"`

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
