package openai

import "github.com/maximhq/bifrost/core/schemas"

// REQUEST TYPES

// OpenAIChatRequest represents an OpenAI chat completion request
type OpenAIChatRequest struct {
	Model               string                   `json:"model"`
	Messages            []schemas.BifrostMessage `json:"messages"`
	MaxTokens           *int                     `json:"max_tokens,omitempty"`
	Temperature         *float64                 `json:"temperature,omitempty"`
	TopP                *float64                 `json:"top_p,omitempty"`
	N                   *int                     `json:"n,omitempty"`
	Stop                interface{}              `json:"stop,omitempty"`
	PresencePenalty     *float64                 `json:"presence_penalty,omitempty"`
	FrequencyPenalty    *float64                 `json:"frequency_penalty,omitempty"`
	LogitBias           map[string]float64       `json:"logit_bias,omitempty"`
	User                *string                  `json:"user,omitempty"`
	Tools               *[]schemas.Tool          `json:"tools,omitempty"` // Reuse schema type
	ToolChoice          *schemas.ToolChoice      `json:"tool_choice,omitempty"`
	Stream              *bool                    `json:"stream,omitempty"`
	LogProbs            *bool                    `json:"logprobs,omitempty"`
	TopLogProbs         *int                     `json:"top_logprobs,omitempty"`
	ResponseFormat      interface{}              `json:"response_format,omitempty"`
	Seed                *int                     `json:"seed,omitempty"`
	MaxCompletionTokens *int                     `json:"max_completion_tokens,omitempty"`
	ReasoningEffort     *string                  `json:"reasoning_effort,omitempty"`
	StreamOptions       *map[string]interface{}  `json:"stream_options,omitempty"`
}
// OpenAISpeechRequest represents an OpenAI speech synthesis request
type OpenAISpeechRequest struct {
	Model          string   `json:"model"`
	Input          string   `json:"input"`
	Voice          string   `json:"voice"`
	ResponseFormat *string  `json:"response_format,omitempty"`
	Speed          *float64 `json:"speed,omitempty"`
	Instructions   *string  `json:"instructions,omitempty"`
	StreamFormat   *string  `json:"stream_format,omitempty"`
}

// OpenAITranscriptionRequest represents an OpenAI transcription request
// Note: This is used for JSON body parsing, actual form parsing is handled in the router
type OpenAITranscriptionRequest struct {
	Model                  string   `json:"model"`
	File                   []byte   `json:"file"` // Binary audio data
	Language               *string  `json:"language,omitempty"`
	Prompt                 *string  `json:"prompt,omitempty"`
	ResponseFormat         *string  `json:"response_format,omitempty"`
	Temperature            *float64 `json:"temperature,omitempty"`
	Include                []string `json:"include,omitempty"`
	TimestampGranularities []string `json:"timestamp_granularities,omitempty"`
	Stream                 *bool    `json:"stream,omitempty"`
}

// OpenAIEmbeddingRequest represents an OpenAI embedding request
type OpenAIEmbeddingRequest struct {
	Model          string      `json:"model"`
	Input          interface{} `json:"input"` // Can be string or []string
	EncodingFormat *string     `json:"encoding_format,omitempty"`
	Dimensions     *int        `json:"dimensions,omitempty"`
	User           *string     `json:"user,omitempty"`
}

// IsStreamingRequested implements the StreamingRequest interface
func (r *OpenAIChatRequest) IsStreamingRequested() bool {
	return r.Stream != nil && *r.Stream
}

// IsStreamingRequested implements the StreamingRequest interface for speech
func (r *OpenAISpeechRequest) IsStreamingRequested() bool {
	return r.StreamFormat != nil && *r.StreamFormat == "sse"
}

// IsStreamingRequested implements the StreamingRequest interface for transcription
func (r *OpenAITranscriptionRequest) IsStreamingRequested() bool {
	return r.Stream != nil && *r.Stream
}

// IsStreamingRequested implements the StreamingRequest interface for embeddings
// Note: Embeddings don't support streaming in OpenAI API
func (r *OpenAIEmbeddingRequest) IsStreamingRequested() bool {
	return false
}

// RESPONSE TYPES
// OpenAIChatResponse represents an OpenAI chat completion response
type OpenAIChatResponse struct {
	ID                string                          `json:"id"`
	Object            string                          `json:"object"`
	Created           int                             `json:"created"`
	Model             string                          `json:"model"`
	Choices           []schemas.BifrostResponseChoice `json:"choices"`
	Usage             *schemas.LLMUsage               `json:"usage,omitempty"` // Reuse schema type
	ServiceTier       *string                         `json:"service_tier,omitempty"`
	SystemFingerprint *string                         `json:"system_fingerprint,omitempty"`
}

// OpenAIStreamResponse represents a single chunk in the OpenAI streaming response
type OpenAIStreamResponse struct {
	ID                string               `json:"id"`
	Object            string               `json:"object"`
	Created           int                  `json:"created"`
	Model             string               `json:"model"`
	SystemFingerprint *string              `json:"system_fingerprint,omitempty"`
	Choices           []OpenAIStreamChoice `json:"choices"`
	Usage             *schemas.LLMUsage    `json:"usage,omitempty"`
}

// OpenAIStreamChoice represents a choice in a streaming response chunk
type OpenAIStreamChoice struct {
	Index        int                `json:"index"`
	Delta        *OpenAIStreamDelta `json:"delta,omitempty"`
	FinishReason *string            `json:"finish_reason,omitempty"`
	LogProbs     *schemas.LogProbs  `json:"logprobs,omitempty"`
}

// OpenAIStreamDelta represents the incremental content in a streaming chunk
type OpenAIStreamDelta struct {
	Role      *string             `json:"role,omitempty"`
	Content   *string             `json:"content,omitempty"`
	ToolCalls *[]schemas.ToolCall `json:"tool_calls,omitempty"`
}


// OpenAIEmbeddingResponse represents an OpenAI embedding response
type OpenAIEmbeddingResponse struct {
	Object            string                     `json:"object"`
	Data              []schemas.BifrostEmbedding `json:"data"`
	Model             string                     `json:"model"`
	Usage             *schemas.LLMUsage          `json:"usage,omitempty"`
	ServiceTier       *string                    `json:"service_tier,omitempty"`
	SystemFingerprint *string                    `json:"system_fingerprint,omitempty"`
}

// ERROR TYPES
// OpenAIChatError represents an OpenAI chat completion error response
type OpenAIChatError struct {
	EventID string `json:"event_id"` // Unique identifier for the error event
	Type    string `json:"type"`     // Type of error
	Error   struct {
		Type    string      `json:"type"`     // Error type
		Code    string      `json:"code"`     // Error code
		Message string      `json:"message"`  // Error message
		Param   interface{} `json:"param"`    // Parameter that caused the error
		EventID string      `json:"event_id"` // Event ID for tracking
	} `json:"error"`
}

// OpenAIChatErrorStruct represents the error structure of an OpenAI chat completion error response
type OpenAIChatErrorStruct struct {
	Type    string      `json:"type"`     // Error type
	Code    string      `json:"code"`     // Error code
	Message string      `json:"message"`  // Error message
	Param   interface{} `json:"param"`    // Parameter that caused the error
	EventID string      `json:"event_id"` // Event ID for tracking
}

