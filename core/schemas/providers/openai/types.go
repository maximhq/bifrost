package openai

import "github.com/maximhq/bifrost/core/schemas"

// REQUEST TYPES

// OpenAITextCompletionRequest represents an OpenAI text completion request
type OpenAITextCompletionRequest struct {
	Model  string                      `json:"model"`             // Required: Model to use
	Prompt schemas.TextCompletionInput `json:"prompt"`            // Required: String or array of strings
	Echo   *bool                       `json:"echo,omitempty"`    // Echo back the prompt
	BestOf *int                        `json:"best_of,omitempty"` // Generate best_of completions server-side
	Suffix *string                     `json:"suffix,omitempty"`  // Suffix for completion
}

// OpenAIEmbeddingRequest represents an OpenAI embedding request
type OpenAIEmbeddingRequest struct {
	Model          string                 `json:"model"`
	Input          schemas.EmbeddingInput `json:"input"` // Can be string or []string
	EncodingFormat *string                `json:"encoding_format,omitempty"`
	Dimensions     *int                   `json:"dimensions,omitempty"`
	User           *string                `json:"user,omitempty"`
}

// OpenAIChatRequest represents an OpenAI chat completion request
type OpenAIChatRequest struct {
	Model    string                `json:"model"`
	Messages []schemas.ChatMessage `json:"messages"`

	schemas.CommonParameters
	schemas.ChatParameters
	Stream *bool `json:"stream,omitempty"`
}

type OpenAIResponsesRequest struct {
	Model string                     `json:"model"`
	Input []schemas.ResponsesMessage `json:"input"`

	schemas.CommonParameters
	schemas.ResponsesParameters
	Stream *bool `json:"stream,omitempty"`
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

// IsStreamingRequested implements the StreamingRequest interface for speech
func (r *OpenAISpeechRequest) IsStreamingRequested() bool {
	return r.StreamFormat != nil && *r.StreamFormat == "sse"
}

// IsStreamingRequested implements the StreamingRequest interface for transcription
func (r *OpenAITranscriptionRequest) IsStreamingRequested() bool {
	return r.Stream != nil && *r.Stream
}
