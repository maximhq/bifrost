// Package schemas defines the core schemas and types used by the Bifrost system.
package schemas

import (
	"fmt"

	"github.com/bytedance/sonic"
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
	Parasail  ModelProvider = "parasail"
	Cerebras  ModelProvider = "cerebras"
	Gemini    ModelProvider = "gemini"
)

// SupportedBaseProviders is the list of base providers allowed for custom providers.
var SupportedBaseProviders = []ModelProvider{
	Anthropic,
	Bedrock,
	Cohere,
	Gemini,
	OpenAI,
}

// StandardProviders is the list of all built-in (non-custom) providers.
var StandardProviders = []ModelProvider{
	Anthropic,
	Azure,
	Bedrock,
	Cerebras,
	Cohere,
	Gemini,
	Groq,
	Mistral,
	Ollama,
	OpenAI,
	Parasail,
	SGL,
	Vertex,
}

// RequestType represents the type of request being made to a provider.
type RequestType string

const (
	TextCompletionRequest       RequestType = "text_completion"
	ChatCompletionRequest       RequestType = "chat_completion"
	ChatCompletionStreamRequest RequestType = "chat_completion_stream"
	EmbeddingRequest            RequestType = "embedding"
	SpeechRequest               RequestType = "speech"
	SpeechStreamRequest         RequestType = "speech_stream"
	TranscriptionRequest        RequestType = "transcription"
	TranscriptionStreamRequest  RequestType = "transcription_stream"
)

// BifrostContextKey is a type for context keys used in Bifrost.
type BifrostContextKey string

// BifrostContextKeyRequestType is a context key for the request type.
const (
	BifrostContextKeyDirectKey          BifrostContextKey = "bifrost-direct-key"
	BifrostContextKeyStreamEndIndicator BifrostContextKey = "bifrost-stream-end-indicator"
	BifrostContextKeyRequestType        BifrostContextKey = "bifrost-request-type"
)

//* Request Structs

// RequestInput represents the input for a model request, which can be either
// a text completion, a chat completion, an embedding request, a speech request, or a transcription request.
type RequestInput struct {
	TextCompletionInput *string             `json:"text_completion_input,omitempty"`
	ChatCompletionInput *[]BifrostMessage   `json:"chat_completion_input,omitempty"`
	EmbeddingInput      *EmbeddingInput     `json:"embedding_input,omitempty"`
	SpeechInput         *SpeechInput        `json:"speech_input,omitempty"`
	TranscriptionInput  *TranscriptionInput `json:"transcription_input,omitempty"`
}

// EmbeddingInput represents the input for an embedding request.
type EmbeddingInput struct {
	Texts []string `json:"texts"`
}

// SpeechInput represents the input for a speech request.
type SpeechInput struct {
	Input          string           `json:"input"`
	VoiceConfig    SpeechVoiceInput `json:"voice"`
	Instructions   string           `json:"instructions,omitempty"`
	ResponseFormat string           `json:"response_format,omitempty"` // Default is "mp3"
}

type SpeechVoiceInput struct {
	Voice            *string
	MultiVoiceConfig []VoiceConfig
}

type VoiceConfig struct {
	Speaker string `json:"speaker"`
	Voice   string `json:"voice"`
}

// MarshalJSON implements custom JSON marshalling for SpeechVoiceInput.
// It marshals either Voice or MultiVoiceConfig directly without wrapping.
func (tc SpeechVoiceInput) MarshalJSON() ([]byte, error) {
	// Validation: ensure only one field is set at a time
	if tc.Voice != nil && len(tc.MultiVoiceConfig) > 0 {
		return nil, fmt.Errorf("both Voice and MultiVoiceConfig are set; only one should be non-nil")
	}

	if tc.Voice != nil {
		return sonic.Marshal(*tc.Voice)
	}
	if len(tc.MultiVoiceConfig) > 0 {
		return sonic.Marshal(tc.MultiVoiceConfig)
	}
	// If both are nil, return null
	return sonic.Marshal(nil)
}

// UnmarshalJSON implements custom JSON unmarshalling for SpeechVoiceInput.
// It determines whether "voice" is a string or a VoiceConfig object/array and assigns to the appropriate field.
// It also handles direct string/array content without a wrapper object.
func (tc *SpeechVoiceInput) UnmarshalJSON(data []byte) error {
	// First, try to unmarshal as a direct string
	var stringContent string
	if err := sonic.Unmarshal(data, &stringContent); err == nil {
		tc.Voice = &stringContent
		return nil
	}

	// Try to unmarshal as an array of VoiceConfig objects
	var voiceConfigs []VoiceConfig
	if err := sonic.Unmarshal(data, &voiceConfigs); err == nil {
		// Validate each VoiceConfig and append to MultiVoiceConfig
		for _, config := range voiceConfigs {
			if config.Voice == "" {
				return fmt.Errorf("voice config has empty voice field")
			}
			tc.MultiVoiceConfig = append(tc.MultiVoiceConfig, config)
		}
		return nil
	}

	return fmt.Errorf("voice field is neither a string, nor an array of VoiceConfig objects")
}

type TranscriptionInput struct {
	File           []byte  `json:"file"`
	Language       *string `json:"language,omitempty"`
	Prompt         *string `json:"prompt,omitempty"`
	ResponseFormat *string `json:"response_format,omitempty"` // Default is "json"
	Format         *string `json:"file_format,omitempty"`     // Type of file, not required in openai, but required in gemini
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
		return sonic.Marshal(*tc.ToolChoiceStr)
	}
	if tc.ToolChoiceStruct != nil {
		return sonic.Marshal(*tc.ToolChoiceStruct)
	}
	// If both are nil, return null
	return sonic.Marshal(nil)
}

// UnmarshalJSON implements custom JSON unmarshalling for ToolChoice.
// It determines whether "tool_choice" is a string or struct and assigns to the appropriate field.
// It also handles direct string/array content without a wrapper object.
func (tc *ToolChoice) UnmarshalJSON(data []byte) error {
	// First, try to unmarshal as a direct string
	var stringContent string
	if err := sonic.Unmarshal(data, &stringContent); err == nil {
		tc.ToolChoiceStr = &stringContent
		return nil
	}

	// Try to unmarshal as a direct struct of ToolChoiceStruct
	var toolChoiceStruct ToolChoiceStruct
	if err := sonic.Unmarshal(data, &toolChoiceStruct); err == nil {
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
func (mc *MessageContent) UnmarshalJSON(data []byte) error {
	// First, try to unmarshal as a direct string
	var stringContent string
	if err := sonic.Unmarshal(data, &stringContent); err == nil {
		mc.ContentStr = &stringContent
		return nil
	}

	// Try to unmarshal as a direct array of ContentBlock
	var arrayContent []ContentBlock
	if err := sonic.Unmarshal(data, &arrayContent); err == nil {
		mc.ContentBlocks = &arrayContent
		return nil
	}

	return fmt.Errorf("content field is neither a string nor an array of ContentBlock")
}

type ContentBlockType string

const (
	ContentBlockTypeText       ContentBlockType = "text"
	ContentBlockTypeImage      ContentBlockType = "image_url"
	ContentBlockTypeInputAudio ContentBlockType = "input_audio"
)

type ContentBlock struct {
	Type       ContentBlockType  `json:"type"`
	Text       *string           `json:"text,omitempty"`
	ImageURL   *ImageURLStruct   `json:"image_url,omitempty"`
	InputAudio *InputAudioStruct `json:"input_audio,omitempty"`
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

// InputAudioStruct represents audio data in a message.
// Data carries the audio payload as a string (e.g., data URL or provider-accepted encoded content).
// Format is optional (e.g., "wav", "mp3"); when nil, providers may attempt auto-detection.
type InputAudioStruct struct {
	Data   string  `json:"data"`
	Format *string `json:"format,omitempty"`
}

//* Response Structs

// BifrostResponse represents the complete result from any bifrost request.
type BifrostResponse struct {
	ID                string                     `json:"id,omitempty"`
	Object            string                     `json:"object,omitempty"` // text.completion, chat.completion, embedding, speech, transcribe
	Choices           []BifrostResponseChoice    `json:"choices,omitempty"`
	Data              []BifrostEmbedding         `json:"data,omitempty"`       // Maps to "data" field in provider responses (e.g., OpenAI embedding format)
	Speech            *BifrostSpeech             `json:"speech,omitempty"`     // Maps to "speech" field in provider responses (e.g., OpenAI speech format)
	Transcribe        *BifrostTranscribe         `json:"transcribe,omitempty"` // Maps to "transcribe" field in provider responses (e.g., OpenAI transcription format)
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

type AudioLLMUsage struct {
	InputTokens        int                `json:"input_tokens"`
	InputTokensDetails *AudioTokenDetails `json:"input_tokens_details,omitempty"`
	OutputTokens       int                `json:"output_tokens"`
	TotalTokens        int                `json:"total_tokens"`
}

type AudioTokenDetails struct {
	TextTokens  int `json:"text_tokens"`
	AudioTokens int `json:"audio_tokens"`
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

type BifrostEmbedding struct {
	Index     int                      `json:"index"`
	Object    string                   `json:"object"`    // embedding
	Embedding BifrostEmbeddingResponse `json:"embedding"` // can be []float32 or string
}

type BifrostEmbeddingResponse struct {
	EmbeddingStr     *string
	EmbeddingArray   *[]float32
	Embedding2DArray *[][]float32
}

func (be BifrostEmbeddingResponse) MarshalJSON() ([]byte, error) {
	if be.EmbeddingStr != nil {
		return sonic.Marshal(be.EmbeddingStr)
	}
	if be.EmbeddingArray != nil {
		return sonic.Marshal(be.EmbeddingArray)
	}
	if be.Embedding2DArray != nil {
		return sonic.Marshal(be.Embedding2DArray)
	}
	return nil, fmt.Errorf("no embedding found")
}

func (be *BifrostEmbeddingResponse) UnmarshalJSON(data []byte) error {
	// First, try to unmarshal as a direct string
	var stringContent string
	if err := sonic.Unmarshal(data, &stringContent); err == nil {
		be.EmbeddingStr = &stringContent
		return nil
	}

	// Try to unmarshal as a direct array of float32
	var arrayContent []float32
	if err := sonic.Unmarshal(data, &arrayContent); err == nil {
		be.EmbeddingArray = &arrayContent
		return nil
	}

	// Try to unmarshal as a direct 2D array of float32
	var arrayContent2D [][]float32
	if err := sonic.Unmarshal(data, &arrayContent2D); err == nil {
		be.Embedding2DArray = &arrayContent2D
		return nil
	}

	return fmt.Errorf("embedding field is neither a string nor an array of float32 nor a 2D array of float32")
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

type BifrostSpeech struct {
	Usage *AudioLLMUsage `json:"usage,omitempty"`
	Audio []byte         `json:"audio"`

	*BifrostSpeechStreamResponse
}
type BifrostSpeechStreamResponse struct {
	Type string `json:"type"`
}

// BifrostTranscribe represents transcription response data
type BifrostTranscribe struct {
	// Common fields for both streaming and non-streaming
	Text     string                 `json:"text"`
	LogProbs []TranscriptionLogProb `json:"logprobs,omitempty"`
	Usage    *TranscriptionUsage    `json:"usage,omitempty"`

	// Embedded structs for specific fields only
	*BifrostTranscribeNonStreamResponse
	*BifrostTranscribeStreamResponse
}

// BifrostTranscribeNonStreamResponse represents non-streaming specific fields only
type BifrostTranscribeNonStreamResponse struct {
	Task     *string                `json:"task,omitempty"`     // e.g., "transcribe"
	Language *string                `json:"language,omitempty"` // e.g., "english"
	Duration *float64               `json:"duration,omitempty"` // Duration in seconds
	Words    []TranscriptionWord    `json:"words,omitempty"`
	Segments []TranscriptionSegment `json:"segments,omitempty"`
}

// BifrostTranscribeStreamResponse represents streaming specific fields only
type BifrostTranscribeStreamResponse struct {
	Type  *string `json:"type,omitempty"`  // "transcript.text.delta" or "transcript.text.done"
	Delta *string `json:"delta,omitempty"` // For delta events
}

// TranscriptionLogProb represents log probability information for transcription
type TranscriptionLogProb struct {
	Token   string  `json:"token"`
	LogProb float64 `json:"logprob"`
	Bytes   []int   `json:"bytes"`
}

// TranscriptionWord represents word-level timing information
type TranscriptionWord struct {
	Word  string  `json:"word"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}

// TranscriptionSegment represents segment-level transcription information
type TranscriptionSegment struct {
	ID               int     `json:"id"`
	Seek             int     `json:"seek"`
	Start            float64 `json:"start"`
	End              float64 `json:"end"`
	Text             string  `json:"text"`
	Tokens           []int   `json:"tokens"`
	Temperature      float64 `json:"temperature"`
	AvgLogProb       float64 `json:"avg_logprob"`
	CompressionRatio float64 `json:"compression_ratio"`
	NoSpeechProb     float64 `json:"no_speech_prob"`
}

// TranscriptionUsage represents usage information for transcription
type TranscriptionUsage struct {
	Type              string             `json:"type"` // "tokens" or "duration"
	InputTokens       *int               `json:"input_tokens,omitempty"`
	InputTokenDetails *AudioTokenDetails `json:"input_token_details,omitempty"`
	OutputTokens      *int               `json:"output_tokens,omitempty"`
	TotalTokens       *int               `json:"total_tokens,omitempty"`
	Seconds           *int               `json:"seconds,omitempty"` // For duration-based usage
}

// BifrostResponseExtraFields contains additional fields in a response.
type BifrostResponseExtraFields struct {
	Provider    ModelProvider      `json:"provider"`
	Params      ModelParameters    `json:"model_params"`
	Latency     *float64           `json:"latency,omitempty"`
	ChatHistory *[]BifrostMessage  `json:"chat_history,omitempty"`
	BilledUsage *BilledLLMUsage    `json:"billed_usage,omitempty"`
	ChunkIndex  int                `json:"chunk_index"` // used for streaming responses to identify the chunk index, will be 0 for non-streaming responses
	RawResponse interface{}        `json:"raw_response"`
	CacheDebug  *BifrostCacheDebug `json:"cache_debug,omitempty"`
}

// BifrostCacheDebug represents debug information about the cache.
type BifrostCacheDebug struct {
	CacheHit        bool     `json:"cache_hit"`
	CacheHitType    string   `json:"cache_hit_type"`
	CacheID         string   `json:"cache_id"`
	CacheThreshold  *float64 `json:"cache_threshold,omitempty"`
	CacheSimilarity *float64 `json:"cache_similarity,omitempty"`
}

const (
	RequestCancelled = "request_cancelled"
)

// BifrostStream represents a stream of responses from the Bifrost system.
// Either BifrostResponse or BifrostError will be non-nil.
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
	Provider       ModelProvider  `json:"-"`
	EventID        *string        `json:"event_id,omitempty"`
	Type           *string        `json:"type,omitempty"`
	IsBifrostError bool           `json:"is_bifrost_error"`
	StatusCode     *int           `json:"status_code,omitempty"`
	Error          ErrorField     `json:"error"`
	AllowFallbacks *bool          `json:"-"` // Optional: Controls fallback behavior (nil = true by default)
	StreamControl  *StreamControl `json:"-"` // Optional: Controls stream behavior
}

type StreamControl struct {
	LogError   *bool `json:"log_error,omitempty"`   // Optional: Controls logging of error
	SkipStream *bool `json:"skip_stream,omitempty"` // Optional: Controls skipping of stream chunk
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
