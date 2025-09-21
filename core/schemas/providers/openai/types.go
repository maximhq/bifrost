package openai

import "github.com/maximhq/bifrost/core/schemas"

// REQUEST TYPES

// OpenAIChatRequest represents an OpenAI chat completion request
type OpenAIChatRequest struct {
	Model    string                   `json:"model"`
	Messages []schemas.BifrostMessage `json:"messages"`

	// Embed ModelParameters to avoid duplication
	*schemas.ModelParameters
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

// OpenAITranscriptionResponse represents an OpenAI transcription response
type OpenAITranscriptionResponse struct {
	ID                string                     `json:"id"`
	Object            string                     `json:"object"`
	Created           int                        `json:"created"`
	Model             string                     `json:"model"`
	Transcribe        *schemas.BifrostTranscribe `json:"transcribe"`
	Usage             *schemas.LLMUsage          `json:"usage,omitempty"`
	SystemFingerprint *string                    `json:"system_fingerprint,omitempty"`
}

// OpenAIEmbeddingRequest represents an OpenAI embedding request
type OpenAIEmbeddingRequest struct {
	Model          string                 `json:"model"`
	Input          schemas.EmbeddingInput `json:"input"` // Can be string or []string
	EncodingFormat *string                `json:"encoding_format,omitempty"`
	Dimensions     *int                   `json:"dimensions,omitempty"`
	User           *string                `json:"user,omitempty"`
}

// OpenAITextCompletionRequest represents an OpenAI text completion request
type OpenAITextCompletionRequest struct {
	Model  string      `json:"model"`  // Required: Model to use
	Prompt interface{} `json:"prompt"` // Required: String or array of strings

	// Embed ModelParameters to avoid duplication
	*schemas.ModelParameters

	// OpenAI-specific text completion parameters not in core ModelParameters
	Echo   *bool   `json:"echo,omitempty"`    // Echo back the prompt
	BestOf *int    `json:"best_of,omitempty"` // Generate best_of completions server-side
	Suffix *string `json:"suffix,omitempty"`  // Suffix for completion
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
type OpenAIChatCompletionStreamResponse struct {
	ID                string                             `json:"id"`
	Object            string                             `json:"object"`
	Created           int                                `json:"created"`
	Model             string                             `json:"model"`
	SystemFingerprint *string                            `json:"system_fingerprint,omitempty"`
	Choices           []OpenAIChatCompletionStreamChoice `json:"choices"`
	Usage             *schemas.LLMUsage                  `json:"usage,omitempty"`
}

// OpenAIStreamChoice represents a choice in a streaming response chunk
type OpenAIChatCompletionStreamChoice struct {
	Index        int                              `json:"index"`
	Delta        *OpenAIChatCompletionStreamDelta `json:"delta,omitempty"`
	FinishReason *string                          `json:"finish_reason,omitempty"`
	LogProbs     *schemas.LogProbs                `json:"logprobs,omitempty"`
}

// OpenAIStreamDelta represents the incremental content in a streaming chunk
type OpenAIChatCompletionStreamDelta struct {
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

// OpenAITextCompletionChoice represents a completion choice in the text completion response
type OpenAITextCompletionChoice struct {
	Text         string                         `json:"text"`                    // Generated completion text
	Index        int                            `json:"index"`                   // Index of this choice
	FinishReason *string                        `json:"finish_reason,omitempty"` // Reason completion finished
	Logprobs     *schemas.TextCompletionLogProb `json:"logprobs,omitempty"`      // Log probability information
}

// OpenAITextCompletionResponse represents an OpenAI text completion response
type OpenAITextCompletionResponse struct {
	ID                string                       `json:"id"`                           // Unique identifier
	Object            string                       `json:"object"`                       // Always "text_completion"
	Created           int                          `json:"created"`                      // Unix timestamp
	Model             string                       `json:"model"`                        // Model used
	Choices           []OpenAITextCompletionChoice `json:"choices"`                      // Completion choices
	Usage             *schemas.LLMUsage            `json:"usage,omitempty"`              // Token usage
	SystemFingerprint *string                      `json:"system_fingerprint,omitempty"` // System fingerprint
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
