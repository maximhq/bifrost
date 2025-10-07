package schemas

import (
	"bytes"
	"fmt"

	"github.com/bytedance/sonic"
)

// ChatParameters represents the parameters for a chat completion.
type ChatParameters struct {
	FrequencyPenalty    *float64            `json:"frequency_penalty,omitempty"`     // Penalizes frequent tokens
	LogitBias           *map[string]float64 `json:"logit_bias,omitempty"`            // Bias for logit values
	LogProbs            *bool               `json:"logprobs,omitempty"`              // Number of logprobs to return
	MaxCompletionTokens *int                `json:"max_completion_tokens,omitempty"` // Maximum number of tokens to generate
	Metadata            *map[string]any     `json:"metadata,omitempty"`              // Metadata to be returned with the response
	Modalities          []string            `json:"modalities,omitempty"`            // Modalities to be returned with the response
	ParallelToolCalls   *bool               `json:"parallel_tool_calls,omitempty"`
	PresencePenalty     *float64            `json:"presence_penalty,omitempty"`  // Penalizes repeated tokens
	PromptCacheKey      *string             `json:"prompt_cache_key,omitempty"`  // Prompt cache key
	ReasoningEffort     *string             `json:"reasoning_effort,omitempty"`  // "minimal" | "low" | "medium" | "high"
	ResponseFormat      *interface{}        `json:"response_format,omitempty"`   // Format for the response
	SafetyIdentifier    *string             `json:"safety_identifier,omitempty"` // Safety identifier
	Seed                *int                `json:"seed,omitempty"`
	ServiceTier         *string             `json:"service_tier,omitempty"`
	StreamOptions       *ChatStreamOptions  `json:"stream_options,omitempty"`
	Stop                []string            `json:"stop,omitempty"`
	Store               *bool               `json:"store,omitempty"`
	Temperature         *float64            `json:"temperature,omitempty"`
	TopLogProbs         *int                `json:"top_logprobs,omitempty"`
	TopP                *float64            `json:"top_p,omitempty"`       // Controls diversity via nucleus sampling
	ToolChoice          *ChatToolChoice     `json:"tool_choice,omitempty"` // Whether to call a tool
	Tools               []ChatTool          `json:"tools,omitempty"`       // Tools to use
	User                *string             `json:"user,omitempty"`        // User identifier for tracking
	Verbosity           *string             `json:"verbosity,omitempty"`   // "low" | "medium" | "high"

	// Dynamic parameters that can be provider-specific, they are directly
	// added to the request as is.
	ExtraParams map[string]interface{} `json:"-"`
}

// ChatStreamOptions represents the stream options for a chat completion.
type ChatStreamOptions struct {
	IncludeObfuscation *bool `json:"include_obfuscation,omitempty"`
	IncludeUsage       *bool `json:"include_usage,omitempty"` // Bifrost marks this as true by default
}

// ChatToolType represents the type of tool.
type ChatToolType string

// ChatToolType values
const (
	ChatToolTypeFunction ChatToolType = "function"
	ChatToolTypeCustom   ChatToolType = "custom"
)

// ChatTool represents a tool definition.
type ChatTool struct {
	Type     ChatToolType      `json:"type"`
	Function *ChatToolFunction `json:"function,omitempty"` // Function definition
	Custom   *ChatToolCustom   `json:"custom,omitempty"`   // Custom tool definition
}

// ChatToolFunction represents a function definition.
type ChatToolFunction struct {
	Name        string                  `json:"name"`                  // Name of the function
	Description *string                 `json:"description,omitempty"` // Description of the parameters
	Parameters  *ToolFunctionParameters `json:"parameters,omitempty"`  // A JSON schema object describing the parameters
	Strict      *bool                   `json:"strict,omitempty"`      // Whether to enforce strict parameter validation
}

// ToolFunctionParameters represents the parameters for a function definition.
type ToolFunctionParameters struct {
	Type        string                 `json:"type"`                  // Type of the parameters
	Description *string                `json:"description,omitempty"` // Description of the parameters
	Required    []string               `json:"required,omitempty"`    // Required parameter names
	Properties  map[string]interface{} `json:"properties,omitempty"`  // Parameter properties
	Enum        []string               `json:"enum,omitempty"`        // Enum values for the parameters
}

type ChatToolCustom struct {
	Format *ChatToolCustomFormat `json:"format,omitempty"` // The input format
}

type ChatToolCustomFormat struct {
	Type    string                       `json:"type"` // always "text"
	Grammar *ChatToolCustomGrammarFormat `json:"grammar,omitempty"`
}

// ChatToolCustomGrammarFormat - A grammar defined by the user
type ChatToolCustomGrammarFormat struct {
	Definition string `json:"definition"` // The grammar definition
	Syntax     string `json:"syntax"`     // "lark" | "regex"
}

// ChatToolChoiceType  for all providers, make sure to check the provider's
// documentation to see which tool choices are supported.
type ChatToolChoiceType string

// ChatToolChoiceType values
const (
	ChatToolChoiceTypeNone     ChatToolChoiceType = "none"
	ChatToolChoiceTypeAny      ChatToolChoiceType = "any"
	ChatToolChoiceTypeRequired ChatToolChoiceType = "required"
	// ChatToolChoiceTypeFunction means a specific tool must be called
	ChatToolChoiceTypeFunction ChatToolChoiceType = "function"
	// ChatToolChoiceTypeAllowedTools means a specific tool must be called
	ChatToolChoiceTypeAllowedTools ChatToolChoiceType = "allowed_tools"
	// ChatToolChoiceTypeCustom means a custom tool must be called
	ChatToolChoiceTypeCustom ChatToolChoiceType = "custom"
)

// ChatToolChoiceStruct represents a tool choice.
type ChatToolChoiceStruct struct {
	Type         ChatToolChoiceType         `json:"type"`                    // Type of tool choice
	Function     ChatToolChoiceFunction     `json:"function,omitempty"`      // Function to call if type is ToolChoiceTypeFunction
	Custom       ChatToolChoiceCustom       `json:"custom,omitempty"`        // Custom tool to call if type is ToolChoiceTypeCustom
	AllowedTools ChatToolChoiceAllowedTools `json:"allowed_tools,omitempty"` // Allowed tools to call if type is ToolChoiceTypeAllowedTools
}

type ChatToolChoice struct {
	ChatToolChoiceStr    *string
	ChatToolChoiceStruct *ChatToolChoiceStruct
}

// MarshalJSON implements custom JSON marshalling for ChatMessageContent.
// It marshals either ContentStr or ContentBlocks directly without wrapping.
func (ctc ChatToolChoice) MarshalJSON() ([]byte, error) {
	// Validation: ensure only one field is set at a time
	if ctc.ChatToolChoiceStr != nil && ctc.ChatToolChoiceStruct != nil {
		return nil, fmt.Errorf("both ChatToolChoiceStr, ChatToolChoiceStruct are set; only one should be non-nil")
	}

	if ctc.ChatToolChoiceStr != nil {
		return sonic.Marshal(ctc.ChatToolChoiceStr)
	}
	if ctc.ChatToolChoiceStruct != nil {
		return sonic.Marshal(ctc.ChatToolChoiceStruct)
	}
	// If both are nil, return null
	return sonic.Marshal(nil)
}

// UnmarshalJSON implements custom JSON unmarshalling for ChatMessageContent.
// It determines whether "content" is a string or array and assigns to the appropriate field.
// It also handles direct string/array content without a wrapper object.
func (ctc *ChatToolChoice) UnmarshalJSON(data []byte) error {
	// First, try to unmarshal as a direct string
	var toolChoiceStr string
	if err := sonic.Unmarshal(data, &toolChoiceStr); err == nil {
		ctc.ChatToolChoiceStr = &toolChoiceStr
		ctc.ChatToolChoiceStruct = nil
		return nil
	}

	// Try to unmarshal as a direct array of ContentBlock
	var chatToolChoice ChatToolChoiceStruct
	if err := sonic.Unmarshal(data, &chatToolChoice); err == nil {
		ctc.ChatToolChoiceStr = nil
		ctc.ChatToolChoiceStruct = &chatToolChoice
		return nil
	}

	return fmt.Errorf("tool_choice field is neither a string nor a ChatToolChoiceStruct object")
}

// ChatToolChoiceFunction represents a function choice.
type ChatToolChoiceFunction struct {
	Name string `json:"name"`
}

// ChatToolChoiceCustom represents a custom choice.
type ChatToolChoiceCustom struct {
	Name string `json:"name"`
}

// ChatToolChoiceAllowedTools represents a allowed tools choice.
type ChatToolChoiceAllowedTools struct {
	Mode  string                           `json:"mode"` // "auto" | "required"
	Tools []ChatToolChoiceAllowedToolsTool `json:"tools"`
}

// ChatToolChoiceAllowedToolsTool represents a allowed tools tool.
type ChatToolChoiceAllowedToolsTool struct {
	Type     string                 `json:"type"` // "function"
	Function ChatToolChoiceFunction `json:"function,omitempty"`
}

// ChatMessageRole represents the role of a chat message
type ChatMessageRole string

// ChatMessageRole values
const (
	ChatMessageRoleAssistant ChatMessageRole = "assistant"
	ChatMessageRoleUser      ChatMessageRole = "user"
	ChatMessageRoleSystem    ChatMessageRole = "system"
	ChatMessageRoleTool      ChatMessageRole = "tool"
	ChatMessageRoleDeveloper ChatMessageRole = "developer"
)

// ChatMessage represents a message in a chat conversation.
type ChatMessage struct {
	Name    *string            `json:"name,omitempty"` // for chat completions
	Role    ChatMessageRole    `json:"role,omitempty"`
	Content ChatMessageContent `json:"content,omitempty"`

	// Embedded pointer structs - when non-nil, their exported fields are flattened into the top-level JSON object
	// IMPORTANT: Only one of the following can be non-nil at a time, otherwise the JSON marshalling will override the common fields
	*ChatToolMessage
	*ChatAssistantMessage
}

// ChatMessageContent represents a content in a message.
type ChatMessageContent struct {
	ContentStr    *string
	ContentBlocks []ChatContentBlock
}

// MarshalJSON implements custom JSON marshalling for ChatMessageContent.
// It marshals either ContentStr or ContentBlocks directly without wrapping.
func (mc ChatMessageContent) MarshalJSON() ([]byte, error) {
	// Validation: ensure only one field is set at a time
	if mc.ContentStr != nil && mc.ContentBlocks != nil {
		return nil, fmt.Errorf("both Content string and Content blocks are set; only one should be non-nil")
	}

	if mc.ContentStr != nil {
		return sonic.Marshal(*mc.ContentStr)
	}
	if mc.ContentBlocks != nil {
		return sonic.Marshal(mc.ContentBlocks)
	}
	// If both are nil, return null
	return sonic.Marshal(nil)
}

// UnmarshalJSON implements custom JSON unmarshalling for ChatMessageContent.
// It determines whether "content" is a string or array and assigns to the appropriate field.
// It also handles direct string/array content without a wrapper object.
func (mc *ChatMessageContent) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		mc.ContentStr = nil
		mc.ContentBlocks = nil
		return nil
	}

	// First, try to unmarshal as a direct string
	var stringContent string
	if err := sonic.Unmarshal(data, &stringContent); err == nil {
		mc.ContentStr = &stringContent
		mc.ContentBlocks = nil
		return nil
	}

	// Try to unmarshal as a direct array of ContentBlock
	var arrayContent []ChatContentBlock
	if err := sonic.Unmarshal(data, &arrayContent); err == nil {
		mc.ContentBlocks = arrayContent
		mc.ContentStr = nil
		return nil
	}

	return fmt.Errorf("content field is neither a string nor an array of Content blocks")
}

// ChatContentBlockType represents the type of content block in a message.
type ChatContentBlockType string

// ChatContentBlockType values
const (
	ChatContentBlockTypeText       ChatContentBlockType = "text"
	ChatContentBlockTypeImage      ChatContentBlockType = "image_url"
	ChatContentBlockTypeInputAudio ChatContentBlockType = "input_audio"
	ChatContentBlockTypeFile       ChatContentBlockType = "input_file"
	ChatContentBlockTypeRefusal    ChatContentBlockType = "refusal"
)

// ChatContentBlock represents a content block in a message.
type ChatContentBlock struct {
	Type           ChatContentBlockType `json:"type"`
	Text           *string              `json:"text,omitempty"`
	Refusal        *string              `json:"refusal,omitempty"`
	ImageURLStruct *ChatInputImage      `json:"image_url,omitempty"`
	InputAudio     *ChatInputAudio      `json:"input_audio,omitempty"`
	File           *ChatInputFile       `json:"file,omitempty"`
}

// ChatInputImage represents image data in a message.
type ChatInputImage struct {
	URL    string  `json:"url"`
	Detail *string `json:"detail,omitempty"`
}

// ChatInputAudio represents audio data in a message.
// Data carries the audio payload as a string (e.g., data URL or provider-accepted encoded content).
// Format is optional (e.g., "wav", "mp3"); when nil, providers may attempt auto-detection.
type ChatInputAudio struct {
	Data   string  `json:"data"`
	Format *string `json:"format,omitempty"`
}

// ChatInputFile represents a file in a message.
type ChatInputFile struct {
	FileData *string `json:"file_data,omitempty"` // Base64 encoded file data
	FileID   *string `json:"file_id,omitempty"`   // Reference to uploaded file
	Filename *string `json:"filename,omitempty"`  // Name of the file
}

// ChatToolMessage represents a tool message in a chat conversation.
type ChatToolMessage struct {
	ToolCallID *string `json:"tool_call_id,omitempty"`
}

// ChatAssistantMessage represents a message in a chat conversation.
type ChatAssistantMessage struct {
	Refusal     *string                          `json:"refusal,omitempty"`
	Annotations []ChatAssistantMessageAnnotation `json:"annotations,omitempty"`
	ToolCalls   []ChatAssistantMessageToolCall   `json:"tool_calls,omitempty"`
}

// ChatAssistantMessageAnnotation represents an annotation in a response.
type ChatAssistantMessageAnnotation struct {
	Type     string                                 `json:"type"`
	Citation ChatAssistantMessageAnnotationCitation `json:"url_citation"`
}

// ChatAssistantMessageAnnotationCitation represents a citation in a response.
type ChatAssistantMessageAnnotationCitation struct {
	StartIndex int          `json:"start_index"`
	EndIndex   int          `json:"end_index"`
	Title      string       `json:"title"`
	URL        *string      `json:"url,omitempty"`
	Sources    *interface{} `json:"sources,omitempty"`
	Type       *string      `json:"type,omitempty"`
}

// ChatAssistantMessageToolCall represents a tool call in a message
type ChatAssistantMessageToolCall struct {
	Type     *string                              `json:"type,omitempty"`
	ID       *string                              `json:"id,omitempty"`
	Function ChatAssistantMessageToolCallFunction `json:"function"`
}

// ChatAssistantMessageToolCallFunction represents a call to a function.
type ChatAssistantMessageToolCallFunction struct {
	Name      *string `json:"name"`
	Arguments string  `json:"arguments"` // stringified json as retured by OpenAI, might not be a valid JSON always
}
