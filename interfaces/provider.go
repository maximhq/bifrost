package interfaces

import "encoding/json"

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

// ModelParameters represents the parameters for model requests
type ModelParameters struct {
	TestRunEntryID *string   `json:"test_run_entry_id"`
	PromptTools    *[]string `json:"prompt_tools"`
	ToolChoice     *string   `json:"tool_choice"`
	Tools          *[]Tool   `json:"tools"`

	// Common model parameters
	Temperature      *float64  `json:"temperature"`
	TopP             *float64  `json:"top_p"`
	TopK             *int      `json:"top_k"`
	MaxTokens        *int      `json:"max_tokens"`
	StopSequences    *[]string `json:"stop_sequences"`
	PresencePenalty  *float64  `json:"presence_penalty"`
	FrequencyPenalty *float64  `json:"frequency_penalty"`

	// Dynamic parameters
	ExtraParams map[string]interface{} `json:"-"`
}

type FunctionParameters struct {
	Type       string                 `json:"type"`
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
	ID       *string  `json:"id"`
	Type     string   `json:"type"`
	Function Function `json:"function"`
}

type Message struct {
	//* strict check for roles
	Role ModelChatMessageRole `json:"role"`
	//* need to make sure either content or imagecontent is provided
	Content      *string       `json:"content"`
	ImageContent *ImageContent `json:"image_content"`
	ToolCalls    *[]Tool       `json:"tool_calls"`
}

type ImageContent struct {
	Type      string `json:"type"`
	URL       string `json:"url"`
	MediaType string `json:"media_type"`
}

type NetworkConfig struct {
	DefaultRequestTimeoutInSeconds int `json:"default_request_timeout_in_seconds"`
}

type MetaConfig struct {
	SecretAccessKey   string            `json:"secret_access_key"`
	Region            *string           `json:"region"`
	SessionToken      *string           `json:"session_token"`
	ARN               *string           `json:"arn"`
	InferenceProfiles map[string]string `json:"inference_profiles"`
}

type ProviderConfig struct {
	NetworkConfig NetworkConfig `json:"network_config"`
	MetaConfig    *MetaConfig   `json:"meta_config"`
}

//* Response Structs

// LLMUsage represents token usage information
type LLMUsage struct {
	PromptTokens     int      `json:"prompt_tokens"`
	CompletionTokens int      `json:"completion_tokens"`
	TotalTokens      int      `json:"total_tokens"`
	Latency          *float64 `json:"latency"`
}

type BilledLLMUsage struct {
	PromptTokens     *float64 `json:"prompt_tokens"`
	CompletionTokens *float64 `json:"completion_tokens"`
	SearchUnits      *float64 `json:"search_units"`
	Classifications  *float64 `json:"classifications"`
}

// LLMInteractionCost represents cost information for LLM interactions
type LLMInteractionCost struct {
	Input  float64 `json:"input"`
	Output float64 `json:"output"`
	Total  float64 `json:"total"`
}

// ToolCall represents a tool call in a message
type ToolCall struct {
	Type      *string         `json:"type"`
	ID        *string         `json:"id"`
	Name      *string         `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type Citation struct {
	Start   *int         `json:"start"`
	End     *int         `json:"end"`
	Text    *string      `json:"text"`
	Sources *interface{} `json:"sources"`
	Type    *string      `json:"type"`
}

// BifrostResponseChoiceMessage represents a choice in the completion response
type BifrostResponseChoiceMessage struct {
	Role      ModelChatMessageRole `json:"role"`
	Content   string               `json:"content"`
	Image     json.RawMessage      `json:"image"`
	ToolCalls *[]ToolCall          `json:"tool_calls"`
	Citations *[]Citation          `json:"citations"`
}

// BifrostResponseChoice represents a choice in the completion result
type BifrostResponseChoice struct {
	Index      int                          `json:"index"`
	Message    BifrostResponseChoiceMessage `json:"message"`
	StopReason *string                      `json:"stop_reason"`
	Stop       *string                      `json:"stop"`
	LogProbs   *interface{}                 `json:"log_probs"`
}

// BifrostResponse represents the complete result from a model completion
type BifrostResponse struct {
	ID          string                          `json:"id"`
	Choices     []BifrostResponseChoice         `json:"choices"`
	ChatHistory *[]BifrostResponseChoiceMessage `json:"chat_history"`
	Provider    SupportedModelProvider          `json:"provider"`
	Usage       LLMUsage                        `json:"usage"`
	BilledUsage *BilledLLMUsage                 `json:"billed_usage"`
	Cost        *LLMInteractionCost             `json:"cost"`
	Model       string                          `json:"model"`
	Created     string                          `json:"created"`
	Params      *interface{}                    `json:"model_params"`
	RawResponse interface{}                     `json:"raw_response"`
}

// TODO third party providers
// Provider defines the interface for AI model providers
type Provider interface {
	GetProviderKey() SupportedModelProvider
	TextCompletion(model, key, text string, params *ModelParameters) (*BifrostResponse, error)
	ChatCompletion(model, key string, messages []Message, params *ModelParameters) (*BifrostResponse, error)
}
