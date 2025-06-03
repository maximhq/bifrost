// Package schemas defines the core schemas and types used by the Bifrost system.
package schemas

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
	InitialPoolSize    int  // Initial pool size for sync pools in Bifrost. Higher values will reduce memory allocations but will increase memory usage.
	DropExcessRequests bool // If true, in cases where the queue is full, requests will not wait for the queue to be empty and will be dropped instead.
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
)

//* Request Structs

// RequestInput represents the input for a model request, which can be either
// a text completion or a chat completion, but either one must be provided.
type RequestInput struct {
	TextCompletionInput *string           `json:"text_completion_input,omitempty"`
	ChatCompletionInput *[]BifrostMessage `json:"chat_completion_input,omitempty"`
}

// BifrostRequest represents a request to be processed by Bifrost.
// It must be provided when calling the Bifrost for text completion or chat completion.
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
	ToolChoice        *ToolChoice `json:"tool_choice,omitempty"`
	Tools             *[]Tool     `json:"tools,omitempty"`
	Temperature       *float64    `json:"temperature,omitempty"`         // Controls randomness in the output
	TopP              *float64    `json:"top_p,omitempty"`               // Controls diversity via nucleus sampling
	TopK              *int        `json:"top_k,omitempty"`               // Controls diversity via top-k sampling
	MaxTokens         *int        `json:"max_tokens,omitempty"`          // Maximum number of tokens to generate
	StopSequences     *[]string   `json:"stop_sequences,omitempty"`      // Sequences that stop generation
	PresencePenalty   *float64    `json:"presence_penalty,omitempty"`    // Penalizes repeated tokens
	FrequencyPenalty  *float64    `json:"frequency_penalty,omitempty"`   // Penalizes frequent tokens
	ParallelToolCalls *bool       `json:"parallel_tool_calls,omitempty"` // Enables parallel tool calls
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

// ToolChoice represents how a tool should be chosen for a request.
type ToolChoice struct {
	Type     ToolChoiceType     `json:"type"`               // Type of tool choice
	Function ToolChoiceFunction `json:"function,omitempty"` // Function to call if type is ToolChoiceTypeFunction
}

// BifrostMessage represents a message in a chat conversation.
type BifrostMessage struct {
	Role    ModelChatMessageRole `json:"role"`
	Content *string              `json:"content,omitempty"`

	// Embedded pointer structs - when non-nil, their exported fields are flattened into the top-level JSON object
	// IMPORTANT: Only one of the following can be non-nil at a time, otherwise the JSON marshalling will override the common fields
	*UserMessage
	*ToolMessage
	*AssistantMessage
}

// UserMessage represents a message from a user
type UserMessage struct {
	ImageContent *ImageContent `json:"image_content,omitempty"`
}

// ToolMessage represents a message from a tool
type ToolMessage struct {
	ImageContent *ImageContent `json:"image_content,omitempty"`
	ToolCallID   *string       `json:"tool_call_id,omitempty"`
}

// AssistantMessage represents a message from an assistant
type AssistantMessage struct {
	Refusal     *string      `json:"refusal,omitempty"`
	Annotations []Annotation `json:"annotations,omitempty"`
	ToolCalls   *[]ToolCall  `json:"tool_calls,omitempty"`
	Thought     *string      `json:"thought,omitempty"`
}

// ImageContent represents image data in a message.
type ImageContent struct {
	Type      *string `json:"type"`
	URL       string  `json:"url"`
	MediaType *string `json:"media_type"`
	Detail    *string `json:"detail"`
}

//* Response Structs

// BifrostResponse represents the complete result from any bifrost request.
type BifrostResponse struct {
	ID                string                     `json:"id,omitempty"`
	Object            string                     `json:"object,omitempty"` // text.completion or chat.completion
	Choices           []BifrostResponseChoice    `json:"choices,omitempty"`
	Model             string                     `json:"model,omitempty"`
	Created           int                        `json:"created,omitempty"` // The Unix timestamp (in seconds).
	ServiceTier       *string                    `json:"service_tier,omitempty"`
	SystemFingerprint *string                    `json:"system_fingerprint,omitempty"`
	Usage             LLMUsage                   `json:"usage,omitempty"`
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

// BifrostResponseChoice represents a choice in the completion result
type BifrostResponseChoice struct {
	Index        int            `json:"index"`
	Message      BifrostMessage `json:"message"`
	FinishReason *string        `json:"finish_reason,omitempty"`
	StopString   *string        `json:"stop,omitempty"`
	LogProbs     *LogProbs      `json:"log_probs,omitempty"`
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

// BifrostError represents an error from the Bifrost system.
type BifrostError struct {
	EventID        *string    `json:"event_id,omitempty"`
	Type           *string    `json:"type,omitempty"`
	IsBifrostError bool       `json:"is_bifrost_error"`
	StatusCode     *int       `json:"status_code,omitempty"`
	Error          ErrorField `json:"error"`
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
