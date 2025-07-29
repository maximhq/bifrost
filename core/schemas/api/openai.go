package api

import (
	"encoding/json"
	"maps"

	"github.com/maximhq/bifrost/core/schemas"
)

// OpenAIResponse represents the response structure from the OpenAI API.
// It includes completion choices, model information, and usage statistics.
type OpenAIResponse struct {
	ID      string                          `json:"id"`      // Unique identifier for the completion
	Object  string                          `json:"object"`  // Type of completion (text.completion, chat.completion, or embedding)
	Choices []schemas.BifrostResponseChoice `json:"choices"` // Array of completion choices
	Data    []struct {                      // Embedding data
		Object    string `json:"object"`
		Embedding any    `json:"embedding"`
		Index     int    `json:"index"`
	} `json:"data,omitempty"`
	Model             string           `json:"model"`              // Model used for the completion
	Created           int              `json:"created"`            // Unix timestamp of completion creation
	ServiceTier       *string          `json:"service_tier"`       // Service tier used for the request
	SystemFingerprint *string          `json:"system_fingerprint"` // System fingerprint for the request
	Usage             schemas.LLMUsage `json:"usage"`              // Token usage statistics
}

// OpenAIError represents the error response structure from the OpenAI API.
// It includes detailed error information and event tracking.
type OpenAIError struct {
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

// OpenAIChatRequest represents an OpenAI chat completion request
type OpenAIChatRequest struct {
	Model            string                   `json:"model"`
	Messages         []schemas.BifrostMessage `json:"messages"`
	MaxTokens        *int                     `json:"max_tokens,omitempty"`
	Temperature      *float64                 `json:"temperature,omitempty"`
	TopP             *float64                 `json:"top_p,omitempty"`
	N                *int                     `json:"n,omitempty"`
	Stop             interface{}              `json:"stop,omitempty"`
	PresencePenalty  *float64                 `json:"presence_penalty,omitempty"`
	FrequencyPenalty *float64                 `json:"frequency_penalty,omitempty"`
	LogitBias        map[string]float64       `json:"logit_bias,omitempty"`
	User             *string                  `json:"user,omitempty"`
	Tools            *[]schemas.Tool          `json:"tools,omitempty"` // Reuse schema type
	ToolChoice       *schemas.ToolChoice      `json:"tool_choice,omitempty"`
	Stream           *bool                    `json:"stream,omitempty"`
	LogProbs         *bool                    `json:"logprobs,omitempty"`
	TopLogProbs      *int                     `json:"top_logprobs,omitempty"`
	ResponseFormat   interface{}              `json:"response_format,omitempty"`
	Seed             *int                     `json:"seed,omitempty"`
	ExtraParams      map[string]interface{}   `json:"-"`
}

func (r *OpenAIChatRequest) MarshalJSON() ([]byte, error) {
	result := make(map[string]interface{}, 18+len(r.ExtraParams))

	result["model"] = r.Model
	result["messages"] = r.Messages

	if r.MaxTokens != nil {
		result["max_tokens"] = *r.MaxTokens
	}
	if r.Temperature != nil {
		result["temperature"] = *r.Temperature
	}
	if r.TopP != nil {
		result["top_p"] = *r.TopP
	}
	if r.N != nil {
		result["n"] = *r.N
	}
	if r.Stop != nil {
		result["stop"] = r.Stop
	}
	if r.PresencePenalty != nil {
		result["presence_penalty"] = *r.PresencePenalty
	}
	if r.FrequencyPenalty != nil {
		result["frequency_penalty"] = *r.FrequencyPenalty
	}
	if r.LogitBias != nil {
		result["logit_bias"] = r.LogitBias
	}
	if r.User != nil {
		result["user"] = *r.User
	}
	if r.Tools != nil {
		result["tools"] = *r.Tools
	}
	if r.ToolChoice != nil {
		result["tool_choice"] = *r.ToolChoice
	}
	if r.LogProbs != nil {
		result["logprobs"] = *r.LogProbs
	}
	if r.TopLogProbs != nil {
		result["top_logprobs"] = *r.TopLogProbs
	}
	if r.ResponseFormat != nil {
		result["response_format"] = r.ResponseFormat
	}
	if r.Seed != nil {
		result["seed"] = *r.Seed
	}
	if r.Stream != nil {
		result["stream"] = *r.Stream
	}

	maps.Copy(result, r.ExtraParams)

	return json.Marshal(result)
}

type OpenAIEmbeddingRequest struct {
	Model          string   `json:"model"`
	Input          []string `json:"input"` // Can be string or []string
	EncodingFormat *string  `json:"encoding_format,omitempty"`
	Dimensions     *int     `json:"dimensions,omitempty"`
	User           *string  `json:"user,omitempty"`
	ExtraParams    map[string]interface{} `json:"-"`
}

func (r *OpenAIEmbeddingRequest) MarshalJSON() ([]byte, error) {
	result := make(map[string]interface{}, 5+len(r.ExtraParams))

	result["model"] = r.Model
	result["input"] = r.Input

	if r.EncodingFormat != nil {
		result["encoding_format"] = *r.EncodingFormat
	}
	if r.Dimensions != nil {
		result["dimensions"] = *r.Dimensions
	}
	if r.User != nil {
		result["user"] = *r.User
	}

	maps.Copy(result, r.ExtraParams)

	return json.Marshal(result)
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
	ExtraParams    map[string]interface{} `json:"-"`
}

func (r *OpenAISpeechRequest) MarshalJSON() ([]byte, error) {
	result := make(map[string]interface{}, 7+len(r.ExtraParams))

	result["model"] = r.Model
	result["input"] = r.Input
	result["voice"] = r.Voice

	if r.ResponseFormat != nil {
		result["response_format"] = *r.ResponseFormat
	}
	if r.Speed != nil {
		result["speed"] = *r.Speed
	}
	if r.Instructions != nil {
		result["instructions"] = *r.Instructions
	}
	if r.StreamFormat != nil {
		result["stream_format"] = *r.StreamFormat
	}

	maps.Copy(result, r.ExtraParams)

	return json.Marshal(result)
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

//response types

// OpenAIChatResponse represents an OpenAI chat completion response
type OpenAIChatResponse struct {
	ID     string     `json:"id"`
	Object string     `json:"object"`
	Data   []struct { // Embedding data
		Object    string `json:"object"`
		Embedding any    `json:"embedding"`
		Index     int    `json:"index"`
	} `json:"data,omitempty"`
	Created           int                             `json:"created"`
	Model             string                          `json:"model"`
	Choices           []schemas.BifrostResponseChoice `json:"choices"`
	Usage             *schemas.LLMUsage               `json:"usage,omitempty"` // Reuse schema type
	ServiceTier       *string                         `json:"service_tier,omitempty"`
	SystemFingerprint *string                         `json:"system_fingerprint,omitempty"`
}

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
