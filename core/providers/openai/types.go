package openai

import (
	"encoding/json"
	"fmt"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
)

const (
	MinMaxCompletionTokens = 16
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

// UnmarshalJSON implements custom JSON unmarshalling for OpenAIChatRequest.
// This is needed because ChatParameters has a custom UnmarshalJSON method,
// which would otherwise "hijack" the unmarshalling and ignore the other fields
// (Model, Messages, Stream, MaxTokens, Fallbacks).
func (r *OpenAIChatRequest) UnmarshalJSON(data []byte) error {
	// Unmarshal the request-specific fields directly
	type baseFields struct {
		Model     string          `json:"model"`
		Messages  []OpenAIMessage `json:"messages"`
		Stream    *bool           `json:"stream,omitempty"`
		MaxTokens *int            `json:"max_tokens,omitempty"`
		Fallbacks []string        `json:"fallbacks,omitempty"`
	}
	var base baseFields
	if err := sonic.Unmarshal(data, &base); err != nil {
		return err
	}
	r.Model = base.Model
	r.Messages = base.Messages
	r.Stream = base.Stream
	r.MaxTokens = base.MaxTokens
	r.Fallbacks = base.Fallbacks

	// Unmarshal ChatParameters (which has its own custom unmarshaller)
	var params schemas.ChatParameters
	if err := sonic.Unmarshal(data, &params); err != nil {
		return err
	}
	r.ChatParameters = params

	return nil
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

// MarshalJSON implements custom JSON marshalling for OpenAIResponsesRequest.
// It sets parameters.reasoning.max_tokens to nil before marshaling.
func (r *OpenAIResponsesRequest) MarshalJSON() ([]byte, error) {
	type Alias OpenAIResponsesRequest

	// Manually marshal Input using its custom MarshalJSON method
	inputBytes, err := r.Input.MarshalJSON()
	if err != nil {
		return nil, err
	}

	// Aux struct:
	// - Alias embeds all original fields
	// - Input shadows the embedded Input field and uses json.RawMessage to preserve custom marshaling
	// - Reasoning shadows the embedded ResponsesParameters.Reasoning
	//   so that we can modify max_tokens before marshaling
	aux := struct {
		*Alias
		// Shadow the embedded "input" field to use custom marshaling
		Input json.RawMessage `json:"input"`
		// Shadow the embedded "reasoning" field to modify it
		Reasoning *schemas.ResponsesParametersReasoning `json:"reasoning,omitempty"`
	}{
		Alias: (*Alias)(r),
		Input: json.RawMessage(inputBytes),
	}

	// Copy reasoning but set MaxTokens to nil
	if r.Reasoning != nil {
		aux.Reasoning = &schemas.ResponsesParametersReasoning{
			Effort:          r.Reasoning.Effort,
			GenerateSummary: r.Reasoning.GenerateSummary,
			Summary:         r.Reasoning.Summary,
			MaxTokens:       nil, // Always set to nil
		}
	}

	return sonic.Marshal(aux)
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
