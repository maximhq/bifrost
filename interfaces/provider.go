package interfaces

import "encoding/json"

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

// Function represents a function definition for tool calls
type Function struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Parameters  interface{} `json:"parameters"`
}

// Tool represents a tool that can be used with the model
type Tool struct {
	Type     string   `json:"type"`
	Function Function `json:"function"`
}

// ModelParameters represents the parameters for model requests
type ModelParameters struct {
	TestRunEntryID *string     `json:"testRunEntryId"`
	PromptTools    *[]string   `json:"promptTools"`
	ToolChoice     *string     `json:"toolChoice"`
	Tools          *[]Tool     `json:"tools"`
	FunctionCall   *string     `json:"functionCall"`
	Functions      *[]Function `json:"functions"`
	// Dynamic parameters
	ExtraParams map[string]interface{} `json:"-"`
}

// RequestOptions represents options for model requests
type RequestOptions struct {
	UseCache       *bool   `json:"useCache"`
	WaitForModel   *bool   `json:"waitForModel"`
	CompletionType *string `json:"CompletionType"`
}

// FunctionCall represents a function call in a tool call
type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ToolCall represents a tool call in a message
type ToolCall struct {
	Type     *string         `json:"type"`
	ID       string          `json:"id"`
	Name     *string         `json:"name"`
	Input    json.RawMessage `json:"input"`
	Function *FunctionCall   `json:"function"`
}

type Citation struct {
	Start   *int         `json:"start"`
	End     *int         `json:"end"`
	Text    *string      `json:"text"`
	Sources *interface{} `json:"sources"`
	Type    *string      `json:"type"`
}

// ModelChatMessageRole represents the role of a chat message
type ModelChatMessageRole string

const (
	RoleAssistant ModelChatMessageRole = "assistant"
	RoleUser      ModelChatMessageRole = "user"
	RoleSystem    ModelChatMessageRole = "system"
	RoleModel     ModelChatMessageRole = "model"
	RoleTool      ModelChatMessageRole = "tool"
)

// CompletionResponseChoice represents a choice in the completion response
type CompletionResponseChoice struct {
	Role      ModelChatMessageRole `json:"role"`
	Content   string               `json:"content"`
	Image     json.RawMessage      `json:"image"`
	ToolCalls *[]ToolCall          `json:"tool_calls"`
	Citations *[]Citation          `json:"citation"`
}

// CompletionResultChoice represents a choice in the completion result
type CompletionResultChoice struct {
	Index      int                      `json:"index"`
	Message    CompletionResponseChoice `json:"message"`
	StopReason *string                  `json:"stop_reason"`
	Stop       *string                  `json:"stop"`
	LogProbs   *interface{}             `json:"logprobs"`
}

// ToolResult represents the result of a tool call
type ToolResult struct {
	Role       ModelChatMessageRole `json:"role"`
	Content    string               `json:"content"`
	ToolCallID string               `json:"tool_call_id"`
}

// ToolCallResult represents a single tool call result
type ToolCallResult struct {
	Name   string      `json:"name"`
	Result interface{} `json:"result"`
	Type   string      `json:"type"`
	ID     string      `json:"id"`
}

// ToolCallResults represents a collection of tool call results
type ToolCallResults struct {
	Version int              `json:"version"`
	Results []ToolCallResult `json:"results"`
}

// CompletionResult represents the complete result from a model completion
type CompletionResult struct {
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
	ID              string                      `json:"id"`
	Choices         []CompletionResultChoice    `json:"choices"`
	ChatHistory     *[]CompletionResponseChoice `json:"chat_history"`
	ToolCallResult  *interface{}                `json:"tool_call_result"`
	ToolCallResults *ToolCallResults            `json:"toolCallResults"`
	Provider        SupportedModelProvider      `json:"provider"`
	Usage           LLMUsage                    `json:"usage"`
	BilledUsage     *BilledLLMUsage             `json:"billed_usage"`
	Cost            *LLMInteractionCost         `json:"cost"`
	Model           string                      `json:"model"`
	Created         string                      `json:"created"`
	Params          *interface{}                `json:"modelParams"`
	Trace           *struct {
		Input  interface{} `json:"input"`
		Output interface{} `json:"output"`
	} `json:"trace"`
}

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

type Message struct {
	//* strict check for roles
	Role ModelChatMessageRole `json:"role"`
	//* need to make sure either content or imagecontent is provided
	Content      *string       `json:"content"`
	ImageContent *ImageContent `json:"imageContent"`
	ToolCalls    *[]ToolCall   `json:"toolCall"`
}

type ImageContent struct {
	Type      string `json:"type"`
	URL       string `json:"url"`
	MediaType string `json:"media_type"`
}

// type Content struct {
// 	Content      *string       `json:"content"`
// 	ImageContent *ImageContent `json:"imageContent"`
// }

// func (content *Content) MarshalJSON() ([]byte, error) {
// 	if content.Content != nil {
// 		return []byte(*content.Content), nil
// 	} else if content.ImageContent != nil {
// 		return json.Marshal(content.ImageContent)
// 	}

// 	return nil, fmt.Errorf("invalid content")
// }

// func (content *Content) UnmarshalJSON(val []byte) error {
// 	var s any
// 	json.Unmarshal(val, &s)

// 	switch s := s.(type) {
// 	case string:
// 		content.Content = &s
// 	case ImageContent:
// 		content.ImageContent = &s

// 	default:
// 		return fmt.Errorf("invalid stop")
// 	}

// 	return nil
// }

type NetworkConfig struct {
	DefaultRequestTimeoutInSeconds int `json:"defaultRequestTimeoutInSeconds"`
}

type MetaConfig struct {
	BedrockMetaConfig *BedrockMetaConfig `json:"bedrockMetaConfig"`
}

type ProviderConfig struct {
	NetworkConfig NetworkConfig `json:"networkConfig"`
	MetaConfig    *MetaConfig   `json:"metaConfig"`
}

type BedrockMetaConfig struct {
	SecretAccessKey   string            `json:"secretAccessKey"`
	Region            *string           `json:"region"`
	SessionToken      *string           `json:"sessionToken"`
	ARN               *string           `json:"arn"`
	InferenceProfiles map[string]string `json:"inferenceProfiles"`
}

// Provider defines the interface for AI model providers
type Provider interface {
	GetProviderKey() SupportedModelProvider
	TextCompletion(model, key, text string, params *ModelParameters) (*CompletionResult, error)
	ChatCompletion(model, key string, messages []Message, params *ModelParameters) (*CompletionResult, error)
}
