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
	Model    string          `json:"model"`
	Messages []OpenAIMessage `json:"messages"`

	schemas.ChatParameters
	Stream *bool `json:"stream,omitempty"`

	//NOTE: MaxCompletionTokens is a new replacement for max_tokens but some providers still use max_tokens.
	// This Field is populated only for such providers and is NOT to be used externally.
	MaxTokens *int `json:"max_tokens,omitempty"`

	// Bifrost specific field (only parsed when converting from Provider -> Bifrost request)
	Fallbacks []string `json:"fallbacks,omitempty"`
}

type OpenAIMessage struct {
	Name    *string                     `json:"name,omitempty"` // for chat completions
	Role    schemas.ChatMessageRole     `json:"role,omitempty"`
	Content *schemas.ChatMessageContent `json:"content,omitempty"`

	// Embedded pointer structs - when non-nil, their exported fields are flattened into the top-level JSON object
	// IMPORTANT: Only one of the following can be non-nil at a time, otherwise the JSON marshalling will override the common fields
	*schemas.ChatToolMessage
	*OpenAIChatAssistantMessage
}

type OpenAIChatAssistantMessage struct {
	Refusal     *string                                  `json:"refusal,omitempty"`
	Reasoning   *string                                  `json:"reasoning,omitempty"`
	Annotations []schemas.ChatAssistantMessageAnnotation `json:"annotations,omitempty"`
	ToolCalls   []schemas.ChatAssistantMessageToolCall   `json:"tool_calls,omitempty"`
}

// MarshalJSON implements custom JSON marshalling for OpenAIChatRequest.
// It excludes the reasoning field and instead marshals reasoning_effort
// with the value of Reasoning.Effort if not nil.
func (r *OpenAIChatRequest) MarshalJSON() ([]byte, error) {
	if r == nil {
		return []byte("null"), nil
	}
	type Alias OpenAIChatRequest

	// Aux struct:
	// - Alias embeds all original fields
	// - Reasoning shadows the embedded ChatParameters.Reasoning
	//   so that "reasoning" is not emitted
	// - ReasoningEffort is emitted as "reasoning_effort"
	aux := struct {
		*Alias
		// Shadow the embedded "reasoning" field and omit it
		Reasoning       *schemas.ChatReasoning `json:"reasoning,omitempty"`
		ReasoningEffort *string                `json:"reasoning_effort,omitempty"`
	}{
		Alias: (*Alias)(r),
	}

	// DO NOT set aux.Reasoning → it stays nil and is omitted via omitempty, and also due to double reference to the same json field.

	if r.Reasoning != nil && r.Reasoning.Effort != nil {
		aux.ReasoningEffort = r.Reasoning.Effort
	}

	return sonic.Marshal(aux)
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
