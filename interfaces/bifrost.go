package interfaces

// ModelChatMessageRole represents the role of a chat message
type ModelChatMessageRole string

const (
	RoleAssistant ModelChatMessageRole = "assistant"
	RoleUser      ModelChatMessageRole = "user"
	RoleSystem    ModelChatMessageRole = "system"
	RoleChatbot   ModelChatMessageRole = "chatbot"
	RoleTool      ModelChatMessageRole = "tool"
)

type SupportedModelProvider string

const (
	OpenAI      SupportedModelProvider = "openai"
	Azure       SupportedModelProvider = "azure"
	HuggingFace SupportedModelProvider = "huggingface"
	Anthropic   SupportedModelProvider = "anthropic"
	Google      SupportedModelProvider = "google"
	Groq        SupportedModelProvider = "groq"
	Bedrock     SupportedModelProvider = "bedrock"
	Maxim       SupportedModelProvider = "maxim"
	Cohere      SupportedModelProvider = "cohere"
	Ollama      SupportedModelProvider = "ollama"
	Lmstudio    SupportedModelProvider = "lmstudio"
)

//* Request Structs

type RequestInput struct {
	TextCompletionInput *string
	ChatCompletionInput *[]Message
}

type BifrostRequest struct {
	Model  string
	Input  RequestInput
	Params *ModelParameters
}

// ModelParameters represents the parameters for model requests
type ModelParameters struct {
	ToolChoice *ToolChoice `json:"tool_choice,omitempty"`
	Tools      *[]Tool     `json:"tools,omitempty"`

	// Common model parameters
	Temperature       *float64  `json:"temperature,omitempty"`
	TopP              *float64  `json:"top_p,omitempty"`
	TopK              *int      `json:"top_k,omitempty"`
	MaxTokens         *int      `json:"max_tokens,omitempty"`
	StopSequences     *[]string `json:"stop_sequences,omitempty"`
	PresencePenalty   *float64  `json:"presence_penalty,omitempty"`
	FrequencyPenalty  *float64  `json:"frequency_penalty,omitempty"`
	ParallelToolCalls *bool     `json:"parallel_tool_calls"`

	// Dynamic parameters
	ExtraParams map[string]interface{} `json:"-"`
}

type FunctionParameters struct {
	Type       string                 `json:"type,"`
	Required   []string               `json:"required"`
	Properties map[string]interface{} `json:"properties"`
}

// Function represents a function definition for tool calls
type Function struct {
	Name        string             `json:"name"`
	Description string             `json:"description"`
	Parameters  FunctionParameters `json:"parameters"`
}

// Tool represents a tool that can be used with the model
type Tool struct {
	ID       *string  `json:"id,omitempty"`
	Type     string   `json:"type"`
	Function Function `json:"function"`
}

// combined tool choices for all providers
type ToolChoiceType string

const (
	ToolChoiceNone     ToolChoiceType = "none"
	ToolChoiceAuto     ToolChoiceType = "auto"
	ToolChoiceAny      ToolChoiceType = "any"
	ToolChoiceTool     ToolChoiceType = "tool"
	ToolChoiceRequired ToolChoiceType = "required"
)

type ToolChoiceFunction struct {
	Name string `json:"name"`
}

type ToolChoice struct {
	Type     ToolChoiceType     `json:"type"`
	Function ToolChoiceFunction `json:"function"`
}

type Message struct {
	//* strict check for roles
	Role ModelChatMessageRole `json:"role"`
	//* need to make sure either content or imagecontent is provided
	Content      *string       `json:"content,omitempty"`
	ImageContent *ImageContent `json:"image_content,omitempty"`
	ToolCalls    *[]Tool       `json:"tool_calls,omitempty"`
}

type ImageContent struct {
	Type      *string `json:"type"`
	URL       string  `json:"url"`
	MediaType *string `json:"media_type"`
	Detail    *string `json:"detail"`
}

//* Response Structs

// LLMUsage represents token usage information
type LLMUsage struct {
	PromptTokens            int                      `json:"prompt_tokens"`
	CompletionTokens        int                      `json:"completion_tokens"`
	TotalTokens             int                      `json:"total_tokens"`
	TokenDetails            *TokenDetails            `json:"prompt_tokens_details,omitempty"`
	CompletionTokensDetails *CompletionTokensDetails `json:"completion_tokens_details,omitempty"`
}

type TokenDetails struct {
	CachedTokens int `json:"cached_tokens,omitempty"`
	AudioTokens  int `json:"audio_tokens,omitempty"`
}

type CompletionTokensDetails struct {
	ReasoningTokens          int `json:"reasoning_tokens,omitempty"`
	AudioTokens              int `json:"audio_tokens,omitempty"`
	AcceptedPredictionTokens int `json:"accepted_prediction_tokens,omitempty"`
	RejectedPredictionTokens int `json:"rejected_prediction_tokens,omitempty"`
}

type BilledLLMUsage struct {
	PromptTokens     *float64 `json:"prompt_tokens,omitempty"`
	CompletionTokens *float64 `json:"completion_tokens,omitempty"`
	SearchUnits      *float64 `json:"search_units,omitempty"`
	Classifications  *float64 `json:"classifications,omitempty"`
}

type LogProb struct {
	Bytes   []int   `json:"bytes,omitempty"`
	LogProb float64 `json:"logprob"`
	Token   string  `json:"token"`
}

type ContentLogProb struct {
	Bytes       []int     `json:"bytes"`
	LogProb     float64   `json:"logprob"`
	Token       string    `json:"token"`
	TopLogProbs []LogProb `json:"top_logprobs"`
}

type LogProbs struct {
	Content []ContentLogProb `json:"content"`
	Refusal []LogProb        `json:"refusal"`
}

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

type Citation struct {
	StartIndex int          `json:"start_index"`
	EndIndex   int          `json:"end_index"`
	Title      string       `json:"title"`
	URL        *string      `json:"url,omitempty"`
	Sources    *interface{} `json:"sources,omitempty"`
	Type       *string      `json:"type,omitempty"`
}

type Annotation struct {
	Type     string   `json:"type"`
	Citation Citation `json:"url_citation"`
}

// BifrostResponseChoiceMessage represents a choice in the completion response
type BifrostResponseChoiceMessage struct {
	Role        ModelChatMessageRole `json:"role"`
	Content     *string              `json:"content,omitempty"`
	Refusal     *string              `json:"refusal,omitempty"`
	Annotations []Annotation         `json:"annotations,omitempty"`
	ToolCalls   *[]ToolCall          `json:"tool_calls,omitempty"`
}

// BifrostResponseChoice represents a choice in the completion result
type BifrostResponseChoice struct {
	Index        int                          `json:"index"`
	Message      BifrostResponseChoiceMessage `json:"message"`
	FinishReason *string                      `json:"finish_reason,omitempty"`
	StopString   *string                      `json:"stop,omitempty"`
	LogProbs     *LogProbs                    `json:"log_probs,omitempty"`
}

type BifrostResponseExtraFields struct {
	Provider    SupportedModelProvider          `json:"provider"`
	Params      ModelParameters                 `json:"model_params"`
	Latency     *float64                        `json:"latency,omitempty"`
	ChatHistory *[]BifrostResponseChoiceMessage `json:"chat_history,omitempty"`
	BilledUsage *BilledLLMUsage                 `json:"billed_usage,omitempty"`
	RawResponse interface{}                     `json:"raw_response"`
}

// BifrostResponse represents the complete result from a model completion
type BifrostResponse struct {
	ID                string                     `json:"id"`
	Object            string                     `json:"object"` // text.completion or chat.completion
	Choices           []BifrostResponseChoice    `json:"choices"`
	Model             string                     `json:"model"`
	Created           int                        `json:"created"` // The Unix timestamp (in seconds).
	ServiceTier       *string                    `json:"service_tier,omitempty"`
	SystemFingerprint *string                    `json:"system_fingerprint,omitempty"`
	Usage             LLMUsage                   `json:"usage"`
	ExtraFields       BifrostResponseExtraFields `json:"extra_fields"`
}

type BifrostError struct {
	EventID        *string    `json:"event_id"`
	Type           *string    `json:"type"`
	IsBifrostError bool       `json:"is_bifrost_error"`
	StatusCode     *int       `json:"status_code"`
	Error          ErrorField `json:"error"`
}

type ErrorField struct {
	Type    *string     `json:"type"`
	Code    *string     `json:"code"`
	Message string      `json:"message"`
	Error   error       `json:"error"`
	Param   interface{} `json:"param"`
	EventID *string     `json:"event_id"`
}
