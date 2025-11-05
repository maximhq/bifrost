// Package schemas defines the core schemas and types used by the Bifrost system.
package schemas

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/bytedance/sonic"
)

const (
	DefaultInitialPoolSize = 5000
)

type KeySelector func(ctx *context.Context, keys []Key, providerKey ModelProvider, model string) (Key, error)

// BifrostConfig represents the configuration for initializing a Bifrost instance.
// It contains the necessary components for setting up the system including account details,
// plugins, logging, and initial pool size.
type BifrostConfig struct {
	Account            Account
	Plugins            []Plugin
	Logger             Logger
	InitialPoolSize    int         // Initial pool size for sync pools in Bifrost. Higher values will reduce memory allocations but will increase memory usage.
	DropExcessRequests bool        // If true, in cases where the queue is full, requests will not wait for the queue to be empty and will be dropped instead.
	MCPConfig          *MCPConfig  // MCP (Model Context Protocol) configuration for tool integration
	KeySelector        KeySelector // Custom key selector function
}

// ModelProvider represents the different AI model providers supported by Bifrost.
type ModelProvider string

const (
	OpenAI     ModelProvider = "openai"
	Azure      ModelProvider = "azure"
	Anthropic  ModelProvider = "anthropic"
	Bedrock    ModelProvider = "bedrock"
	Cohere     ModelProvider = "cohere"
	Vertex     ModelProvider = "vertex"
	Mistral    ModelProvider = "mistral"
	Ollama     ModelProvider = "ollama"
	Groq       ModelProvider = "groq"
	SGL        ModelProvider = "sgl"
	Parasail   ModelProvider = "parasail"
	Perplexity ModelProvider = "perplexity"
	Cerebras   ModelProvider = "cerebras"
	Gemini     ModelProvider = "gemini"
	OpenRouter ModelProvider = "openrouter"
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
	Perplexity,
	SGL,
	Vertex,
	OpenRouter,
}

// RequestType represents the type of request being made to a provider.
type RequestType string

const (
	ListModelsRequest           RequestType = "list_models"
	TextCompletionRequest       RequestType = "text_completion"
	TextCompletionStreamRequest RequestType = "text_completion_stream"
	ChatCompletionRequest       RequestType = "chat_completion"
	ChatCompletionStreamRequest RequestType = "chat_completion_stream"
	ResponsesRequest            RequestType = "responses"
	ResponsesStreamRequest      RequestType = "responses_stream"
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
	BifrostContextKeyVirtualKey          BifrostContextKey = "x-bf-vk"                        // string
	BifrostContextKeyRequestID           BifrostContextKey = "request-id"                     // string
	BifrostContextKeyFallbackRequestID   BifrostContextKey = "fallback-request-id"            // string
	BifrostContextKeyDirectKey           BifrostContextKey = "bifrost-direct-key"             // Key struct
	BifrostContextKeySelectedKey         BifrostContextKey = "bifrost-key-selected"           // string (to store the selected key ID (set by bifrost))
	BifrostContextKeyStreamEndIndicator  BifrostContextKey = "bifrost-stream-end-indicator"   // bool (set by bifrost)
	BifrostContextKeySkipKeySelection    BifrostContextKey = "bifrost-skip-key-selection"     // bool (will pass an empty key to the provider)
	BifrostContextKeyExtraHeaders        BifrostContextKey = "bifrost-extra-headers"          // map[string]string
	BifrostContextKeyURLPath             BifrostContextKey = "bifrost-extra-url-path"         // string
	BifrostContextKeyUseRawRequestBody   BifrostContextKey = "bifrost-use-raw-request-body"   // bool
	BifrostContextKeySendBackRawResponse BifrostContextKey = "bifrost-send-back-raw-response" // bool
)

// NOTE: for custom plugin implementation dealing with streaming short circuit,
// make sure to mark BifrostContextKeyStreamEndIndicator as true at the end of the stream.

//* Request Structs

// Fallback represents a fallback model to be used if the primary model is not available.
type Fallback struct {
	Provider ModelProvider `json:"provider"`
	Model    string        `json:"model"`
}

// BifrostRequest is the request struct for all bifrost requests.
// only ONE of the following fields should be set:
// - ListModelsRequest
// - TextCompletionRequest
// - ChatRequest
// - ResponsesRequest
// - EmbeddingRequest
// - SpeechRequest
// - TranscriptionRequest
// NOTE: Bifrost Request is submitted back to pool after every use so DO NOT keep references to this struct after use, especially in go routines.
type BifrostRequest struct {
	RequestType RequestType

	ListModelsRequest     *BifrostListModelsRequest
	TextCompletionRequest *BifrostTextCompletionRequest
	ChatRequest           *BifrostChatRequest
	ResponsesRequest      *BifrostResponsesRequest
	EmbeddingRequest      *BifrostEmbeddingRequest
	SpeechRequest         *BifrostSpeechRequest
	TranscriptionRequest  *BifrostTranscriptionRequest
}

// GetRequestFields returns the provider, model, and fallbacks from the request.
func (br *BifrostRequest) GetRequestFields() (provider ModelProvider, model string, fallbacks []Fallback) {
	switch {
	case br.TextCompletionRequest != nil:
		return br.TextCompletionRequest.Provider, br.TextCompletionRequest.Model, br.TextCompletionRequest.Fallbacks
	case br.ChatRequest != nil:
		return br.ChatRequest.Provider, br.ChatRequest.Model, br.ChatRequest.Fallbacks
	case br.ResponsesRequest != nil:
		return br.ResponsesRequest.Provider, br.ResponsesRequest.Model, br.ResponsesRequest.Fallbacks
	case br.EmbeddingRequest != nil:
		return br.EmbeddingRequest.Provider, br.EmbeddingRequest.Model, br.EmbeddingRequest.Fallbacks
	case br.SpeechRequest != nil:
		return br.SpeechRequest.Provider, br.SpeechRequest.Model, br.SpeechRequest.Fallbacks
	case br.TranscriptionRequest != nil:
		return br.TranscriptionRequest.Provider, br.TranscriptionRequest.Model, br.TranscriptionRequest.Fallbacks
	}

	return "", "", nil
}

func (br *BifrostRequest) SetProvider(provider ModelProvider) {
	switch {
	case br.TextCompletionRequest != nil:
		br.TextCompletionRequest.Provider = provider
	case br.ChatRequest != nil:
		br.ChatRequest.Provider = provider
	case br.ResponsesRequest != nil:
		br.ResponsesRequest.Provider = provider
	case br.EmbeddingRequest != nil:
		br.EmbeddingRequest.Provider = provider
	case br.SpeechRequest != nil:
		br.SpeechRequest.Provider = provider
	case br.TranscriptionRequest != nil:
		br.TranscriptionRequest.Provider = provider
	}
}

func (br *BifrostRequest) SetModel(model string) {
	switch {
	case br.TextCompletionRequest != nil:
		br.TextCompletionRequest.Model = model
	case br.ChatRequest != nil:
		br.ChatRequest.Model = model
	case br.ResponsesRequest != nil:
		br.ResponsesRequest.Model = model
	case br.EmbeddingRequest != nil:
		br.EmbeddingRequest.Model = model
	case br.SpeechRequest != nil:
		br.SpeechRequest.Model = model
	case br.TranscriptionRequest != nil:
		br.TranscriptionRequest.Model = model
	}
}

func (br *BifrostRequest) SetFallbacks(fallbacks []Fallback) {
	switch {
	case br.TextCompletionRequest != nil:
		br.TextCompletionRequest.Fallbacks = fallbacks
	case br.ChatRequest != nil:
		br.ChatRequest.Fallbacks = fallbacks
	case br.ResponsesRequest != nil:
		br.ResponsesRequest.Fallbacks = fallbacks
	case br.EmbeddingRequest != nil:
		br.EmbeddingRequest.Fallbacks = fallbacks
	case br.SpeechRequest != nil:
		br.SpeechRequest.Fallbacks = fallbacks
	case br.TranscriptionRequest != nil:
		br.TranscriptionRequest.Fallbacks = fallbacks
	}
}

func (br *BifrostRequest) SetRawRequestBody(rawRequestBody []byte) {
	switch {
	case br.TextCompletionRequest != nil:
		br.TextCompletionRequest.RawRequestBody = rawRequestBody
	case br.ChatRequest != nil:
		br.ChatRequest.RawRequestBody = rawRequestBody
	case br.ResponsesRequest != nil:
		br.ResponsesRequest.RawRequestBody = rawRequestBody
	case br.EmbeddingRequest != nil:
		br.EmbeddingRequest.RawRequestBody = rawRequestBody
	case br.SpeechRequest != nil:
		br.SpeechRequest.RawRequestBody = rawRequestBody
	case br.TranscriptionRequest != nil:
		br.TranscriptionRequest.RawRequestBody = rawRequestBody
	}
}

//* Response Structs

// BifrostResponse represents the complete result from any bifrost request.
type BifrostResponse struct {
	TextCompletionResponse      *BifrostTextCompletionResponse
	ChatResponse                *BifrostChatResponse
	ResponsesResponse           *BifrostResponsesResponse
	ResponsesStreamResponse     *BifrostResponsesStreamResponse
	EmbeddingResponse           *BifrostEmbeddingResponse
	SpeechResponse              *BifrostSpeechResponse
	SpeechStreamResponse        *BifrostSpeechStreamResponse
	TranscriptionResponse       *BifrostTranscriptionResponse
	TranscriptionStreamResponse *BifrostTranscriptionStreamResponse
}

func (r *BifrostResponse) GetExtraFields() *BifrostResponseExtraFields {
	switch {
	case r.TextCompletionResponse != nil:
		return &r.TextCompletionResponse.ExtraFields
	case r.ChatResponse != nil:
		return &r.ChatResponse.ExtraFields
	case r.ResponsesResponse != nil:
		return &r.ResponsesResponse.ExtraFields
	case r.ResponsesStreamResponse != nil:
		return &r.ResponsesStreamResponse.ExtraFields
	case r.EmbeddingResponse != nil:
		return &r.EmbeddingResponse.ExtraFields
	case r.SpeechResponse != nil:
		return &r.SpeechResponse.ExtraFields
	case r.SpeechStreamResponse != nil:
		return &r.SpeechStreamResponse.ExtraFields
	case r.TranscriptionResponse != nil:
		return &r.TranscriptionResponse.ExtraFields
	case r.TranscriptionStreamResponse != nil:
		return &r.TranscriptionStreamResponse.ExtraFields
	}

	return &BifrostResponseExtraFields{}
}

// BifrostResponseExtraFields contains additional fields in a response.
type BifrostResponseExtraFields struct {
	RequestType    RequestType        `json:"request_type"`
	Provider       ModelProvider      `json:"provider,omitempty"`
	ModelRequested string             `json:"model_requested,omitempty"`
	Latency        int64              `json:"latency"`     // in milliseconds (for streaming responses this will be each chunk latency, and the last chunk latency will be the total latency)
	ChunkIndex     int                `json:"chunk_index"` // used for streaming responses to identify the chunk index, will be 0 for non-streaming responses
	RawResponse    interface{}        `json:"raw_response,omitempty"`
	CacheDebug     *BifrostCacheDebug `json:"cache_debug,omitempty"`
}

// BifrostCacheDebug represents debug information about the cache.
type BifrostCacheDebug struct {
	CacheHit bool `json:"cache_hit"`

	CacheID *string `json:"cache_id,omitempty"`
	HitType *string `json:"hit_type,omitempty"`

	// Semantic cache only (provider, model, and input tokens will be present for semantic cache, even if cache is not hit)
	ProviderUsed *string `json:"provider_used,omitempty"`
	ModelUsed    *string `json:"model_used,omitempty"`
	InputTokens  *int    `json:"input_tokens,omitempty"`

	// Semantic cache only (only when cache is hit)
	Threshold  *float64 `json:"threshold,omitempty"`
	Similarity *float64 `json:"similarity,omitempty"`
}

const (
	RequestCancelled = "request_cancelled"
)

// BifrostStream represents a stream of responses from the Bifrost system.
// Either BifrostResponse or BifrostError will be non-nil.
type BifrostStream struct {
	*BifrostTextCompletionResponse
	*BifrostChatResponse
	*BifrostResponsesStreamResponse
	*BifrostSpeechStreamResponse
	*BifrostTranscriptionStreamResponse
	*BifrostError
}

// MarshalJSON implements custom JSON marshaling for BifrostStream.
// This ensures that only the non-nil embedded struct is marshaled,
func (bs BifrostStream) MarshalJSON() ([]byte, error) {
	if bs.BifrostTextCompletionResponse != nil {
		return sonic.Marshal(bs.BifrostTextCompletionResponse)
	} else if bs.BifrostChatResponse != nil {
		return sonic.Marshal(bs.BifrostChatResponse)
	} else if bs.BifrostResponsesStreamResponse != nil {
		return sonic.Marshal(bs.BifrostResponsesStreamResponse)
	} else if bs.BifrostSpeechStreamResponse != nil {
		return sonic.Marshal(bs.BifrostSpeechStreamResponse)
	} else if bs.BifrostTranscriptionStreamResponse != nil {
		return sonic.Marshal(bs.BifrostTranscriptionStreamResponse)
	} else if bs.BifrostError != nil {
		return sonic.Marshal(bs.BifrostError)
	}
	// Return empty object if both are nil (shouldn't happen in practice)
	return []byte("{}"), nil
}

// BifrostError represents an error from the Bifrost system.
//
// PLUGIN DEVELOPERS: When creating BifrostError in PreHook or PostHook, you can set AllowFallbacks:
// - AllowFallbacks = &true: Bifrost will try fallback providers if available
// - AllowFallbacks = &false: Bifrost will return this error immediately, no fallbacks
// - AllowFallbacks = nil: Treated as true by default (fallbacks allowed for resilience)
type BifrostError struct {
	EventID        *string                 `json:"event_id,omitempty"`
	Type           *string                 `json:"type,omitempty"`
	IsBifrostError bool                    `json:"is_bifrost_error"`
	StatusCode     *int                    `json:"status_code,omitempty"`
	Error          *ErrorField             `json:"error"`
	AllowFallbacks *bool                   `json:"-"` // Optional: Controls fallback behavior (nil = true by default)
	StreamControl  *StreamControl          `json:"-"` // Optional: Controls stream behavior
	ExtraFields    BifrostErrorExtraFields `json:"extra_fields,omitempty"`
}

// StreamControl represents stream control options.
type StreamControl struct {
	LogError   *bool `json:"log_error,omitempty"`   // Optional: Controls logging of error
	SkipStream *bool `json:"skip_stream,omitempty"` // Optional: Controls skipping of stream chunk
}

// ErrorField represents detailed error information.
type ErrorField struct {
	Type    *string     `json:"type,omitempty"`
	Code    *string     `json:"code,omitempty"`
	Message string      `json:"message"`
	Error   error       `json:"-"`
	Param   interface{} `json:"param,omitempty"`
	EventID *string     `json:"event_id,omitempty"`
}

// MarshalJSON implements custom JSON marshaling for ErrorField.
// It converts the Error field (error interface) to a string.
func (e *ErrorField) MarshalJSON() ([]byte, error) {
	type Alias ErrorField
	aux := &struct {
		Error *string `json:"error,omitempty"`
		*Alias
	}{
		Alias: (*Alias)(e),
	}

	if e.Error != nil {
		errStr := e.Error.Error()
		aux.Error = &errStr
	}

	return json.Marshal(aux)
}

func (e *ErrorField) UnmarshalJSON(data []byte) error {
	type Alias ErrorField
	aux := &struct {
		Error *string `json:"error,omitempty"`
		*Alias
	}{
		Alias: (*Alias)(e),
	}

	if err := json.Unmarshal(data, aux); err != nil {
		return err
	}

	if aux.Error != nil {
		e.Error = errors.New(*aux.Error)
	}

	return nil
}

// BifrostErrorExtraFields contains additional fields in an error response.
type BifrostErrorExtraFields struct {
	Provider       ModelProvider `json:"provider"`
	ModelRequested string        `json:"model_requested"`
	RequestType    RequestType   `json:"request_type"`
}
