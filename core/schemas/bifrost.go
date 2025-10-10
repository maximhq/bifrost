// Package schemas defines the core schemas and types used by the Bifrost system.
package schemas

import (
	"context"
	"fmt"

	"github.com/bytedance/sonic"
)

const (
	DefaultInitialPoolSize = 5000
)

type KeySelector func(ctx *context.Context, keys []Key, providerKey ModelProvider, model string) (Key, error)

// BifrostRequest is the request struct for all bifrost requests.
// only ONE of the following fields should be set:
// - TextCompletionRequest
// - ChatRequest
// - ResponsesRequest
// - EmbeddingRequest
// - SpeechRequest
// - TranscriptionRequest
type BifrostRequest struct {
	Provider    ModelProvider
	Model       string
	Fallbacks   []Fallback
	RequestType RequestType

	TextCompletionRequest *BifrostTextCompletionRequest
	ChatRequest           *BifrostChatRequest
	ResponsesRequest      *BifrostResponsesRequest
	EmbeddingRequest      *BifrostEmbeddingRequest
	SpeechRequest         *BifrostSpeechRequest
	TranscriptionRequest  *BifrostTranscriptionRequest
}

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
	SGL,
	Vertex,
	OpenRouter,
}

// RequestType represents the type of request being made to a provider.
type RequestType string

const (
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
	BifrostContextKeyRequestID          BifrostContextKey = "request-id"
	BifrostContextKeyFallbackRequestID  BifrostContextKey = "fallback-request-id"
	BifrostContextKeyVirtualKeyHeader   BifrostContextKey = "x-bf-vk"
	BifrostContextKeyDirectKey          BifrostContextKey = "bifrost-direct-key"
	BifrostContextKeySelectedKey        BifrostContextKey = "bifrost-key-selected" // To store the selected key ID (set by bifrost)
	BifrostContextKeyStreamEndIndicator BifrostContextKey = "bifrost-stream-end-indicator"
)

// NOTE: for custom plugin implementation dealing with streaming short circuit,
// make sure to mark BifrostContextKeyStreamEndIndicator as true at the end of the stream.

//* Request Structs

// BifrostTextCompletionRequest is the request struct for text completion requests
type BifrostTextCompletionRequest struct {
	Provider  ModelProvider             `json:"provider"`
	Model     string                    `json:"model"`
	Input     *TextCompletionInput      `json:"input,omitempty"`
	Params    *TextCompletionParameters `json:"params,omitempty"`
	Fallbacks []Fallback                `json:"fallbacks,omitempty"`
}

// ToBifrostChatRequest converts a Bifrost text completion request to a Bifrost chat completion request
// This method is discouraged to use, but is useful for litellm fallback flows
func (r *BifrostTextCompletionRequest) ToBifrostChatRequest() *BifrostChatRequest {
	if r == nil || r.Input == nil {
		return nil
	}
	message := ChatMessage{Role: ChatMessageRoleUser}
	if r.Input.PromptStr != nil {
		message.Content = &ChatMessageContent{
			ContentStr: r.Input.PromptStr,
		}
	} else if len(r.Input.PromptArray) > 0 {
		blocks := make([]ChatContentBlock, 0, len(r.Input.PromptArray))
		for _, prompt := range r.Input.PromptArray {
			blocks = append(blocks, ChatContentBlock{
				Type: ChatContentBlockTypeText,
				Text: &prompt,
			})
		}
		message.Content = &ChatMessageContent{
			ContentBlocks: blocks,
		}
	}
	params := ChatParameters{}
	if r.Params != nil {
		params.MaxCompletionTokens = r.Params.MaxTokens
		params.Temperature = r.Params.Temperature
		params.TopP = r.Params.TopP
		params.Stop = r.Params.Stop
		params.ExtraParams = r.Params.ExtraParams
		params.StreamOptions = r.Params.StreamOptions
		params.User = r.Params.User
		params.FrequencyPenalty = r.Params.FrequencyPenalty
		params.LogitBias = r.Params.LogitBias
		params.PresencePenalty = r.Params.PresencePenalty
		params.Seed = r.Params.Seed
	}
	return &BifrostChatRequest{
		Provider:  r.Provider,
		Model:     r.Model,
		Fallbacks: r.Fallbacks,
		Input:     []ChatMessage{message},
		Params:    &params,
	}
}

// BifrostChatRequest is the request struct for chat completion requests
type BifrostChatRequest struct {
	Provider  ModelProvider   `json:"provider"`
	Model     string          `json:"model"`
	Input     []ChatMessage   `json:"input,omitempty"`
	Params    *ChatParameters `json:"params,omitempty"`
	Fallbacks []Fallback      `json:"fallbacks,omitempty"`
}

type BifrostResponsesRequest struct {
	Provider  ModelProvider        `json:"provider"`
	Model     string               `json:"model"`
	Input     []ResponsesMessage   `json:"input,omitempty"`
	Params    *ResponsesParameters `json:"params,omitempty"`
	Fallbacks []Fallback           `json:"fallbacks,omitempty"`
}

type BifrostEmbeddingRequest struct {
	Provider  ModelProvider        `json:"provider"`
	Model     string               `json:"model"`
	Input     *EmbeddingInput      `json:"input,omitempty"`
	Params    *EmbeddingParameters `json:"params,omitempty"`
	Fallbacks []Fallback           `json:"fallbacks,omitempty"`
}

type BifrostSpeechRequest struct {
	Provider  ModelProvider     `json:"provider"`
	Model     string            `json:"model"`
	Input     *SpeechInput      `json:"input,omitempty"`
	Params    *SpeechParameters `json:"params,omitempty"`
	Fallbacks []Fallback        `json:"fallbacks,omitempty"`
}

type BifrostTranscriptionRequest struct {
	Provider  ModelProvider            `json:"provider"`
	Model     string                   `json:"model"`
	Input     *TranscriptionInput      `json:"input,omitempty"`
	Params    *TranscriptionParameters `json:"params,omitempty"`
	Fallbacks []Fallback               `json:"fallbacks,omitempty"`
}

// Fallback represents a fallback model to be used if the primary model is not available.
type Fallback struct {
	Provider ModelProvider `json:"provider"`
	Model    string        `json:"model"`
}

//* Response Structs

// BifrostResponse represents the complete result from any bifrost request.
type BifrostResponse struct {
	ID                string                      `json:"id,omitempty"`
	Object            string                      `json:"object,omitempty"` // text.completion, chat.completion, embedding, speech, transcribe
	Choices           []BifrostChatResponseChoice `json:"choices,omitempty"`
	Data              []BifrostEmbedding          `json:"data,omitempty"`       // Maps to "data" field in provider responses (e.g., OpenAI embedding format)
	Speech            *BifrostSpeech              `json:"speech,omitempty"`     // Maps to "speech" field in provider responses (e.g., OpenAI speech format)
	Transcribe        *BifrostTranscribe          `json:"transcribe,omitempty"` // Maps to "transcribe" field in provider responses (e.g., OpenAI transcription format)
	Model             string                      `json:"model,omitempty"`
	Created           int                         `json:"created,omitempty"` // The Unix timestamp (in seconds).
	ServiceTier       *string                     `json:"service_tier,omitempty"`
	SystemFingerprint *string                     `json:"system_fingerprint,omitempty"`
	Usage             *LLMUsage                   `json:"usage,omitempty"`
	ExtraFields       BifrostResponseExtraFields  `json:"extra_fields"`

	*ResponsesResponse
}

// ToTextCompletionResponse converts a Bifrost response to a Bifrost text completion response
func (r *BifrostResponse) ToTextCompletionResponse() {
	r.Object = "text_completion"
	r.ExtraFields.RequestType = TextCompletionRequest
	if len(r.Choices) == 0 {
		return
	}
	choice := r.Choices[0]
	if choice.BifrostStreamResponseChoice != nil && choice.BifrostStreamResponseChoice.Delta != nil {
		r.Choices = []BifrostChatResponseChoice{
			{
				Index: 0,
				BifrostTextCompletionResponseChoice: &BifrostTextCompletionResponseChoice{
					Text: choice.Delta.Content,
				},
				FinishReason: choice.FinishReason,
				LogProbs:     choice.LogProbs,
			},
		}
	}
	if choice.BifrostNonStreamResponseChoice != nil {
		msg := choice.BifrostNonStreamResponseChoice.Message
		var textContent *string
		if msg != nil && msg.Content != nil && msg.Content.ContentStr != nil {
			textContent = msg.Content.ContentStr
		}
		r.Choices = []BifrostChatResponseChoice{
			{
				Index: 0,
				BifrostTextCompletionResponseChoice: &BifrostTextCompletionResponseChoice{
					Text: textContent,
				},
				FinishReason: choice.FinishReason,
				LogProbs:     choice.LogProbs,
			},
		}
	}
}

// LLMUsage represents token usage information
type LLMUsage struct {
	PromptTokens     int `json:"prompt_tokens,omitempty"`
	CompletionTokens int `json:"completion_tokens,omitempty"`
	TotalTokens      int `json:"total_tokens"`

	*ResponsesExtendedResponseUsage
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
	Content []ContentLogProb `json:"content,omitempty"`
	Refusal []LogProb        `json:"refusal,omitempty"`

	*TextCompletionLogProb
}

type BifrostEmbedding struct {
	Index     int                      `json:"index"`
	Object    string                   `json:"object"`    // embedding
	Embedding BifrostEmbeddingResponse `json:"embedding"` // can be []float32 or string
}

type BifrostEmbeddingResponse struct {
	EmbeddingStr     *string
	EmbeddingArray   []float32
	Embedding2DArray [][]float32
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
		be.EmbeddingArray = arrayContent
		return nil
	}

	// Try to unmarshal as a direct 2D array of float32
	var arrayContent2D [][]float32
	if err := sonic.Unmarshal(data, &arrayContent2D); err == nil {
		be.Embedding2DArray = arrayContent2D
		return nil
	}

	return fmt.Errorf("embedding field is neither a string nor an array of float32 nor a 2D array of float32")
}

// BifrostChatResponseChoice represents a choice in the completion result.
// This struct can represent either a streaming or non-streaming response choice.
// IMPORTANT: Only one of BifrostNonStreamResponseChoice or BifrostStreamResponseChoice
// should be non-nil at a time.
type BifrostChatResponseChoice struct {
	Index        int       `json:"index"`
	FinishReason *string   `json:"finish_reason,omitempty"`
	LogProbs     *LogProbs `json:"log_probs,omitempty"`

	*BifrostTextCompletionResponseChoice
	*BifrostNonStreamResponseChoice
	*BifrostStreamResponseChoice
}

type BifrostTextCompletionResponseChoice struct {
	Text *string `json:"text,omitempty"`
}

// BifrostNonStreamResponseChoice represents a choice in the non-stream response
type BifrostNonStreamResponseChoice struct {
	Message    *ChatMessage `json:"message"`
	StopString *string      `json:"stop,omitempty"`
}

// BifrostStreamResponseChoice represents a choice in the stream response
type BifrostStreamResponseChoice struct {
	Delta *BifrostStreamDelta `json:"delta,omitempty"` // Partial message info
}

// BifrostStreamDelta represents a delta in the stream response
type BifrostStreamDelta struct {
	Role      *string                        `json:"role,omitempty"`       // Only in the first chunk
	Content   *string                        `json:"content,omitempty"`    // May be empty string or null
	Thought   *string                        `json:"thought,omitempty"`    // May be empty string or null
	Refusal   *string                        `json:"refusal,omitempty"`    // Refusal content if any
	ToolCalls []ChatAssistantMessageToolCall `json:"tool_calls,omitempty"` // If tool calls used (supports incremental updates)
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

// BifrostResponseExtraFields contains additional fields in a response.
type BifrostResponseExtraFields struct {
	RequestType    RequestType        `json:"request_type"`
	Provider       ModelProvider      `json:"provider"`
	ModelRequested string             `json:"model_requested"`
	Latency        int64              `json:"latency,omitempty"` // in milliseconds
	BilledUsage    *BilledLLMUsage    `json:"billed_usage,omitempty"`
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
	EventID        *string                 `json:"event_id,omitempty"`
	Type           *string                 `json:"type,omitempty"`
	IsBifrostError bool                    `json:"is_bifrost_error"`
	StatusCode     *int                    `json:"status_code,omitempty"`
	Error          *ErrorField             `json:"error"`
	AllowFallbacks *bool                   `json:"-"` // Optional: Controls fallback behavior (nil = true by default)
	StreamControl  *StreamControl          `json:"-"` // Optional: Controls stream behavior
	ExtraFields    BifrostErrorExtraFields `json:"extra_fields,omitempty"`
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

type BifrostErrorExtraFields struct {
	Provider       ModelProvider `json:"provider"`
	ModelRequested string        `json:"model_requested"`
	RequestType    RequestType   `json:"request_type"`
}
