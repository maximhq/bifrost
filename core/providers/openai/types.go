package openai

import (
	"fmt"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
)

// REQUEST TYPES

// OpenAITextCompletionRequest represents an OpenAI text completion request
type OpenAITextCompletionRequest struct {
	Model  string                       `json:"model"`  // Required: Model to use
	Prompt *schemas.TextCompletionInput `json:"prompt"` // Required: String or array of strings

	schemas.TextCompletionParameters
	Stream *bool `json:"stream,omitempty"`

	// Bifrost specific field (only parsed when converting from Provider -> Bifrost request)
	Fallbacks []string `json:"fallbacks,omitempty"`
}

// IsStreamingRequested implements the StreamingRequest interface
func (r *OpenAITextCompletionRequest) IsStreamingRequested() bool {
	return r.Stream != nil && *r.Stream
}

// OpenAIEmbeddingRequest represents an OpenAI embedding request
type OpenAIEmbeddingRequest struct {
	Model string                  `json:"model"`
	Input *schemas.EmbeddingInput `json:"input"` // Can be string or []string

	schemas.EmbeddingParameters

	// Bifrost specific field (only parsed when converting from Provider -> Bifrost request)
	Fallbacks []string `json:"fallbacks,omitempty"`
}

// OpenAIChatRequest represents an OpenAI chat completion request
type OpenAIChatRequest struct {
	Model    string                `json:"model"`
	Messages []schemas.ChatMessage `json:"messages"`

	schemas.ChatParameters
	Stream *bool `json:"stream,omitempty"`

	//NOTE: MaxCompletionTokens is a new replacement for max_tokens but some providers still use max_tokens.
	// This Field is populated only for such providers and is NOT to be used externally.
	MaxTokens *int `json:"max_tokens,omitempty"`

	// Bifrost specific field (only parsed when converting from Provider -> Bifrost request)
	Fallbacks []string `json:"fallbacks,omitempty"`
}

// IsStreamingRequested implements the StreamingRequest interface
func (r *OpenAIChatRequest) IsStreamingRequested() bool {
	return r.Stream != nil && *r.Stream
}

// ResponsesRequestInput is a union of string and array of responses messages
type OpenAIResponsesRequestInput struct {
	OpenAIResponsesRequestInputStr   *string
	OpenAIResponsesRequestInputArray []schemas.ResponsesMessage
}

// UnmarshalJSON unmarshals the responses request input
func (r *OpenAIResponsesRequestInput) UnmarshalJSON(data []byte) error {
	var str string
	if err := sonic.Unmarshal(data, &str); err == nil {
		r.OpenAIResponsesRequestInputStr = &str
		r.OpenAIResponsesRequestInputArray = nil
		return nil
	}
	var array []schemas.ResponsesMessage
	if err := sonic.Unmarshal(data, &array); err == nil {
		r.OpenAIResponsesRequestInputStr = nil
		r.OpenAIResponsesRequestInputArray = array
		return nil
	}
	return fmt.Errorf("openai responses request input is neither a string nor an array of responses messages")
}

// MarshalJSON implements custom JSON marshalling for ResponsesRequestInput.
func (r *OpenAIResponsesRequestInput) MarshalJSON() ([]byte, error) {
	if r.OpenAIResponsesRequestInputStr != nil {
		return sonic.Marshal(*r.OpenAIResponsesRequestInputStr)
	}
	if r.OpenAIResponsesRequestInputArray != nil {
		return sonic.Marshal(r.OpenAIResponsesRequestInputArray)
	}
	return sonic.Marshal(nil)
}

type OpenAIResponsesRequest struct {
	Model string                      `json:"model"`
	Input OpenAIResponsesRequestInput `json:"input"`

	schemas.ResponsesParameters
	Stream *bool `json:"stream,omitempty"`

	// Bifrost specific field (only parsed when converting from Provider -> Bifrost request)
	Fallbacks []string `json:"fallbacks,omitempty"`
}

// IsStreamingRequested implements the StreamingRequest interface
func (r *OpenAIResponsesRequest) IsStreamingRequested() bool {
	return r.Stream != nil && *r.Stream
}

// OpenAISpeechRequest represents an OpenAI speech synthesis request
type OpenAISpeechRequest struct {
	Model string `json:"model"`
	Input string `json:"input"`

	schemas.SpeechParameters
	StreamFormat *string `json:"stream_format,omitempty"`

	// Bifrost specific field (only parsed when converting from Provider -> Bifrost request)
	Fallbacks []string `json:"fallbacks,omitempty"`
}

// OpenAITranscriptionRequest represents an OpenAI transcription request
// Note: This is used for JSON body parsing, actual form parsing is handled in the router
type OpenAITranscriptionRequest struct {
	Model string `json:"model"`
	File  []byte `json:"file"` // Binary audio data

	schemas.TranscriptionParameters
	Stream *bool `json:"stream,omitempty"`

	// Bifrost specific field (only parsed when converting from Provider -> Bifrost request)
	Fallbacks []string `json:"fallbacks,omitempty"`
}

// IsStreamingRequested implements the StreamingRequest interface for speech
func (r *OpenAISpeechRequest) IsStreamingRequested() bool {
	return r.StreamFormat != nil && *r.StreamFormat == "sse"
}

// IsStreamingRequested implements the StreamingRequest interface for transcription
func (r *OpenAITranscriptionRequest) IsStreamingRequested() bool {
	return r.Stream != nil && *r.Stream
}

// MODEL TYPES
type OpenAIModel struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	OwnedBy string `json:"owned_by"`
	Created *int64 `json:"created,omitempty"`

	// GROQ specific fields
	Active        *bool `json:"active,omitempty"`
	ContextWindow *int  `json:"context_window,omitempty"`
}

type OpenAIListModelsResponse struct {
	Object string        `json:"object"`
	Data   []OpenAIModel `json:"data"`
}

// OpenAIImageGenerationRequest is the struct for Image Generation requests by OpenAI.
type OpenAIImageGenerationRequest struct {
	Model          string  `json:"model"`
	Prompt         string  `json:"prompt"`
	N              *int    `json:"n,omitempty"`
	Size           *string `json:"size,omitempty"`
	Quality        *string `json:"quality,omitempty"`
	Style          *string `json:"style,omitempty"`
	Stream         *bool   `json:"stream,omitempty"`
	ResponseFormat *string `json:"response_format,omitempty"`
	User           *string `json:"user,omitempty"`
}

// OpenAIImageGenerationResponse is the struct for Image Generation responses by OpenAI.
type OpenAIImageGenerationResponse struct {
	Created int64 `json:"created"`
	Data    []struct {
		URL           string `json:"url,omitempty"`
		B64JSON       string `json:"b64_json,omitempty"`
		RevisedPrompt string `json:"revised_prompt,omitempty"`
	} `json:"data"`
	Background   *string                     `json:"background,omitempty"`
	OutputFormat *string                     `json:"output_format,omitempty"`
	Size         *string                     `json:"size,omitempty"`
	Quality      *string                     `json:"quality,omitempty"`
	Usage        *OpenAIImageGenerationUsage `json:"usage"`
}

type OpenAIImageGenerationUsage struct {
	TotalTokens  int `json:"total_tokens,omitempty"`
	InputTokens  int `json:"input_tokens,omitempty"`
	OutputTokens int `json:"output_tokens,omitempty"`

	InputTokensDetails *struct {
		TextTokens  int `json:"text_tokens,omitempty"`
		ImageTokens int `json:"image_tokens,omitempty"`
	} `json:"input_tokens_details,omitempty"`
}

// OpenAIImageStreamResponse is the struct for Image Generation streaming responses by OpenAI.
type OpenAIImageStreamResponse struct {
	Type              ImageGenerationEventType    `json:"type,omitempty"`
	B64JSON           *string                     `json:"b64_json,omitempty"`
	PartialImageIndex int                         `json:"partial_image_index,omitempty"`
	Usage             *OpenAIImageGenerationUsage `json:"usage,omitempty"`
}
