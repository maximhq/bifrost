// Package schemas defines the core schemas and types used by the Bifrost system.
package schemas

import (
	"encoding/json"
	"errors"
)

const (
	DefaultInitialPoolSize = 5000
)

type KeySelector func(ctx *BifrostContext, keys []Key, providerKey ModelProvider, model string) (Key, error)

// BifrostConfig represents the configuration for initializing a Bifrost instance.
// It contains the necessary components for setting up the system including account details,
// plugins, logging, and initial pool size.
type BifrostConfig struct {
	Account            Account
	Plugins            []Plugin
	Logger             Logger
	Tracer             Tracer      // Tracer for distributed tracing (nil = NoOpTracer)
	InitialPoolSize    int         // Initial pool size for sync pools in Bifrost. Higher values will reduce memory allocations but will increase memory usage.
	DropExcessRequests bool        // If true, in cases where the queue is full, requests will not wait for the queue to be empty and will be dropped instead.
	MCPConfig          *MCPConfig  // MCP (Model Context Protocol) configuration for tool integration
	KeySelector        KeySelector // Custom key selector function
}

// ModelProvider represents the different AI model providers supported by Bifrost.
type ModelProvider string

const (
	OpenAI      ModelProvider = "openai"
	Azure       ModelProvider = "azure"
	Anthropic   ModelProvider = "anthropic"
	Bedrock     ModelProvider = "bedrock"
	Cohere      ModelProvider = "cohere"
	Vertex      ModelProvider = "vertex"
	Mistral     ModelProvider = "mistral"
	Ollama      ModelProvider = "ollama"
	Groq        ModelProvider = "groq"
	SGL         ModelProvider = "sgl"
	Parasail    ModelProvider = "parasail"
	Perplexity  ModelProvider = "perplexity"
	Cerebras    ModelProvider = "cerebras"
	Gemini      ModelProvider = "gemini"
	OpenRouter  ModelProvider = "openrouter"
	Elevenlabs  ModelProvider = "elevenlabs"
	HuggingFace ModelProvider = "huggingface"
	Nebius      ModelProvider = "nebius"
	XAI         ModelProvider = "xai"
)

// SupportedBaseProviders is the list of base providers allowed for custom providers.
var SupportedBaseProviders = []ModelProvider{
	Anthropic,
	Bedrock,
	Cohere,
	Gemini,
	OpenAI,
	HuggingFace,
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
	Elevenlabs,
	HuggingFace,
	Nebius,
}

// RequestType represents the type of request being made to a provider.
type RequestType string

const (
	ListModelsRequest            RequestType = "list_models"
	TextCompletionRequest        RequestType = "text_completion"
	TextCompletionStreamRequest  RequestType = "text_completion_stream"
	ChatCompletionRequest        RequestType = "chat_completion"
	ChatCompletionStreamRequest  RequestType = "chat_completion_stream"
	ResponsesRequest             RequestType = "responses"
	ResponsesStreamRequest       RequestType = "responses_stream"
	EmbeddingRequest             RequestType = "embedding"
	SpeechRequest                RequestType = "speech"
	SpeechStreamRequest          RequestType = "speech_stream"
	TranscriptionRequest         RequestType = "transcription"
	TranscriptionStreamRequest   RequestType = "transcription_stream"
	ImageGenerationRequest       RequestType = "image_generation"
	ImageGenerationStreamRequest RequestType = "image_generation_stream"
	BatchCreateRequest           RequestType = "batch_create"
	BatchListRequest             RequestType = "batch_list"
	BatchRetrieveRequest         RequestType = "batch_retrieve"
	BatchCancelRequest           RequestType = "batch_cancel"
	BatchResultsRequest          RequestType = "batch_results"
	FileUploadRequest            RequestType = "file_upload"
	FileListRequest              RequestType = "file_list"
	FileRetrieveRequest          RequestType = "file_retrieve"
	FileDeleteRequest            RequestType = "file_delete"
	FileContentRequest           RequestType = "file_content"
	ContainerCreateRequest       RequestType = "container_create"
	ContainerListRequest         RequestType = "container_list"
	ContainerRetrieveRequest     RequestType = "container_retrieve"
	ContainerDeleteRequest       RequestType = "container_delete"
	ContainerFileCreateRequest   RequestType = "container_file_create"
	ContainerFileListRequest     RequestType = "container_file_list"
	ContainerFileRetrieveRequest RequestType = "container_file_retrieve"
	ContainerFileContentRequest  RequestType = "container_file_content"
	ContainerFileDeleteRequest   RequestType = "container_file_delete"
	CountTokensRequest           RequestType = "count_tokens"
	UnknownRequest               RequestType = "unknown"
)

// BifrostContextKey is a type for context keys used in Bifrost.
type BifrostContextKey string

// BifrostContextKeyRequestType is a context key for the request type.
const (
	BifrostContextKeyVirtualKey                          BifrostContextKey = "x-bf-vk"                      // string
	BifrostContextKeyAPIKeyName                          BifrostContextKey = "x-bf-api-key"                 // string (explicit key name selection)
	BifrostContextKeyRequestID                           BifrostContextKey = "request-id"                   // string
	BifrostContextKeyFallbackRequestID                   BifrostContextKey = "fallback-request-id"          // string
	BifrostContextKeyDirectKey                           BifrostContextKey = "bifrost-direct-key"           // Key struct
	BifrostContextKeySelectedKeyID                       BifrostContextKey = "bifrost-selected-key-id"      // string (to store the selected key ID (set by bifrost - DO NOT SET THIS MANUALLY))
	BifrostContextKeySelectedKeyName                     BifrostContextKey = "bifrost-selected-key-name"    // string (to store the selected key name (set by bifrost - DO NOT SET THIS MANUALLY))
	BifrostContextKeyNumberOfRetries                     BifrostContextKey = "bifrost-number-of-retries"    // int (to store the number of retries (set by bifrost - DO NOT SET THIS MANUALLY))
	BifrostContextKeyFallbackIndex                       BifrostContextKey = "bifrost-fallback-index"       // int (to store the fallback index (set by bifrost - DO NOT SET THIS MANUALLY)) 0 for primary, 1 for first fallback, etc.
	BifrostContextKeyStreamEndIndicator                  BifrostContextKey = "bifrost-stream-end-indicator" // bool (set by bifrost - DO NOT SET THIS MANUALLY))
	BifrostContextKeySkipKeySelection                    BifrostContextKey = "bifrost-skip-key-selection"   // bool (will pass an empty key to the provider)
	BifrostContextKeyExtraHeaders                        BifrostContextKey = "bifrost-extra-headers"        // map[string][]string
	BifrostContextKeyURLPath                             BifrostContextKey = "bifrost-extra-url-path"       // string
	BifrostContextKeyUseRawRequestBody                   BifrostContextKey = "bifrost-use-raw-request-body"
	BifrostContextKeySendBackRawRequest                  BifrostContextKey = "bifrost-send-back-raw-request"                    // bool
	BifrostContextKeySendBackRawResponse                 BifrostContextKey = "bifrost-send-back-raw-response"                   // bool
	BifrostContextKeyIntegrationType                     BifrostContextKey = "bifrost-integration-type"                         // integration used in gateway (e.g. openai, anthropic, bedrock, etc.)
	BifrostContextKeyIsResponsesToChatCompletionFallback BifrostContextKey = "bifrost-is-responses-to-chat-completion-fallback" // bool (set by bifrost - DO NOT SET THIS MANUALLY))
	BifrostMCPAgentOriginalRequestID                     BifrostContextKey = "bifrost-mcp-agent-original-request-id"            // string (to store the original request ID for MCP agent mode)
	BifrostContextKeyStructuredOutputToolName            BifrostContextKey = "bifrost-structured-output-tool-name"              // string (to store the name of the structured output tool (set by bifrost))
	BifrostContextKeyUserAgent                           BifrostContextKey = "bifrost-user-agent"                               // string (set by bifrost)
	BifrostContextKeyTraceID                             BifrostContextKey = "bifrost-trace-id"                                 // string (trace ID for distributed tracing - set by tracing middleware)
	BifrostContextKeySpanID                              BifrostContextKey = "bifrost-span-id"                                  // string (current span ID for child span creation - set by tracer)
	BifrostContextKeyParentSpanID                        BifrostContextKey = "bifrost-parent-span-id"                           // string (parent span ID from W3C traceparent header - set by tracing middleware)
	BifrostContextKeyStreamStartTime                     BifrostContextKey = "bifrost-stream-start-time"                        // time.Time (start time for streaming TTFT calculation - set by bifrost)
	BifrostContextKeyTracer                              BifrostContextKey = "bifrost-tracer"                                   // Tracer (tracer instance for completing deferred spans - set by bifrost)
	BifrostContextKeyDeferTraceCompletion                BifrostContextKey = "bifrost-defer-trace-completion"                   // bool (signals trace completion should be deferred for streaming - set by streaming handlers)
	BifrostContextKeyTraceCompleter                      BifrostContextKey = "bifrost-trace-completer"                          // func() (callback to complete trace after streaming - set by tracing middleware)
	BifrostContextKeyPostHookSpanFinalizer               BifrostContextKey = "bifrost-posthook-span-finalizer"                  // func(context.Context) (callback to finalize post-hook spans after streaming - set by bifrost)
	BifrostContextKeyAccumulatorID                       BifrostContextKey = "bifrost-accumulator-id"                           // string (ID for streaming accumulator lookup - set by tracer for accumulator operations)
	BifrostContextKeySkipDBUpdate                        BifrostContextKey = "bifrost-skip-db-update"                           // bool (set by bifrost - DO NOT SET THIS MANUALLY))
	BifrostContextKeyGovernancePluginName                BifrostContextKey = "governance-plugin-name"                           // string (name of the governance plugin that processed the request - set by bifrost)
	BifrostContextKeyIsEnterprise                        BifrostContextKey = "is-enterprise"                                    // bool (set by bifrost - DO NOT SET THIS MANUALLY))
	BifrostContextKeyAvailableProviders                  BifrostContextKey = "available-providers"                              // []ModelProvider (set by bifrost - DO NOT SET THIS MANUALLY))
	BifrostContextKeyRawRequestResponseForLogging        BifrostContextKey = "bifrost-raw-request-response-for-logging"         // bool (set by bifrost - DO NOT SET THIS MANUALLY))
	BifrostContextKeyRetryDBFetch                       BifrostContextKey = "bifrost-retry-db-fetch"                            // bool (set by bifrost - DO NOT SET THIS MANUALLY))
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
// - CountTokensRequest
// - EmbeddingRequest
// - SpeechRequest
// - TranscriptionRequest
// - ImageGenerationRequest
// NOTE: Bifrost Request is submitted back to pool after every use so DO NOT keep references to this struct after use, especially in go routines.
type BifrostRequest struct {
	RequestType RequestType

	ListModelsRequest            *BifrostListModelsRequest
	TextCompletionRequest        *BifrostTextCompletionRequest
	ChatRequest                  *BifrostChatRequest
	ResponsesRequest             *BifrostResponsesRequest
	CountTokensRequest           *BifrostResponsesRequest
	EmbeddingRequest             *BifrostEmbeddingRequest
	SpeechRequest                *BifrostSpeechRequest
	TranscriptionRequest         *BifrostTranscriptionRequest
	ImageGenerationRequest       *BifrostImageGenerationRequest
	FileUploadRequest            *BifrostFileUploadRequest
	FileListRequest              *BifrostFileListRequest
	FileRetrieveRequest          *BifrostFileRetrieveRequest
	FileDeleteRequest            *BifrostFileDeleteRequest
	FileContentRequest           *BifrostFileContentRequest
	BatchCreateRequest           *BifrostBatchCreateRequest
	BatchListRequest             *BifrostBatchListRequest
	BatchRetrieveRequest         *BifrostBatchRetrieveRequest
	BatchCancelRequest           *BifrostBatchCancelRequest
	BatchResultsRequest          *BifrostBatchResultsRequest
	ContainerCreateRequest       *BifrostContainerCreateRequest
	ContainerListRequest         *BifrostContainerListRequest
	ContainerRetrieveRequest     *BifrostContainerRetrieveRequest
	ContainerDeleteRequest       *BifrostContainerDeleteRequest
	ContainerFileCreateRequest   *BifrostContainerFileCreateRequest
	ContainerFileListRequest     *BifrostContainerFileListRequest
	ContainerFileRetrieveRequest *BifrostContainerFileRetrieveRequest
	ContainerFileContentRequest  *BifrostContainerFileContentRequest
	ContainerFileDeleteRequest   *BifrostContainerFileDeleteRequest
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
	case br.CountTokensRequest != nil:
		return br.CountTokensRequest.Provider, br.CountTokensRequest.Model, br.CountTokensRequest.Fallbacks
	case br.EmbeddingRequest != nil:
		return br.EmbeddingRequest.Provider, br.EmbeddingRequest.Model, br.EmbeddingRequest.Fallbacks
	case br.SpeechRequest != nil:
		return br.SpeechRequest.Provider, br.SpeechRequest.Model, br.SpeechRequest.Fallbacks
	case br.TranscriptionRequest != nil:
		return br.TranscriptionRequest.Provider, br.TranscriptionRequest.Model, br.TranscriptionRequest.Fallbacks
	case br.ImageGenerationRequest != nil:
		return br.ImageGenerationRequest.Provider, br.ImageGenerationRequest.Model, br.ImageGenerationRequest.Fallbacks
	case br.FileUploadRequest != nil:
		if br.FileUploadRequest.Model != nil {
			return br.FileUploadRequest.Provider, *br.FileUploadRequest.Model, nil
		}
		return br.FileUploadRequest.Provider, "", nil
	case br.FileListRequest != nil:
		if br.FileListRequest.Model != nil {
			return br.FileListRequest.Provider, *br.FileListRequest.Model, nil
		}
		return br.FileListRequest.Provider, "", nil
	case br.FileRetrieveRequest != nil:
		if br.FileRetrieveRequest.Model != nil {
			return br.FileRetrieveRequest.Provider, *br.FileRetrieveRequest.Model, nil
		}
		return br.FileRetrieveRequest.Provider, "", nil
	case br.FileDeleteRequest != nil:
		if br.FileDeleteRequest.Model != nil {
			return br.FileDeleteRequest.Provider, *br.FileDeleteRequest.Model, nil
		}
		return br.FileDeleteRequest.Provider, "", nil
	case br.FileContentRequest != nil:
		if br.FileContentRequest.Model != nil {
			return br.FileContentRequest.Provider, *br.FileContentRequest.Model, nil
		}
		return br.FileContentRequest.Provider, "", nil
	case br.BatchCreateRequest != nil:
		if br.BatchCreateRequest.Model != nil {
			return br.BatchCreateRequest.Provider, *br.BatchCreateRequest.Model, nil
		}
		return br.BatchCreateRequest.Provider, "", nil
	case br.BatchListRequest != nil:
		if br.BatchListRequest.Model != nil {
			return br.BatchListRequest.Provider, *br.BatchListRequest.Model, nil
		}
		return br.BatchListRequest.Provider, "", nil
	case br.BatchRetrieveRequest != nil:
		if br.BatchRetrieveRequest.Model != nil {
			return br.BatchRetrieveRequest.Provider, *br.BatchRetrieveRequest.Model, nil
		}
		return br.BatchRetrieveRequest.Provider, "", nil
	case br.BatchCancelRequest != nil:
		if br.BatchCancelRequest.Model != nil {
			return br.BatchCancelRequest.Provider, *br.BatchCancelRequest.Model, nil
		}
		return br.BatchCancelRequest.Provider, "", nil
	case br.BatchResultsRequest != nil:
		if br.BatchResultsRequest.Model != nil {
			return br.BatchResultsRequest.Provider, *br.BatchResultsRequest.Model, nil
		}
		return br.BatchResultsRequest.Provider, "", nil
	case br.ContainerCreateRequest != nil:
		return br.ContainerCreateRequest.Provider, "", nil
	case br.ContainerListRequest != nil:
		return br.ContainerListRequest.Provider, "", nil
	case br.ContainerRetrieveRequest != nil:
		return br.ContainerRetrieveRequest.Provider, "", nil
	case br.ContainerDeleteRequest != nil:
		return br.ContainerDeleteRequest.Provider, "", nil
	case br.ContainerFileCreateRequest != nil:
		return br.ContainerFileCreateRequest.Provider, "", nil
	case br.ContainerFileListRequest != nil:
		return br.ContainerFileListRequest.Provider, "", nil
	case br.ContainerFileRetrieveRequest != nil:
		return br.ContainerFileRetrieveRequest.Provider, "", nil
	case br.ContainerFileContentRequest != nil:
		return br.ContainerFileContentRequest.Provider, "", nil
	case br.ContainerFileDeleteRequest != nil:
		return br.ContainerFileDeleteRequest.Provider, "", nil
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
	case br.CountTokensRequest != nil:
		br.CountTokensRequest.Provider = provider
	case br.EmbeddingRequest != nil:
		br.EmbeddingRequest.Provider = provider
	case br.SpeechRequest != nil:
		br.SpeechRequest.Provider = provider
	case br.TranscriptionRequest != nil:
		br.TranscriptionRequest.Provider = provider
	case br.ImageGenerationRequest != nil:
		br.ImageGenerationRequest.Provider = provider
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
	case br.CountTokensRequest != nil:
		br.CountTokensRequest.Model = model
	case br.EmbeddingRequest != nil:
		br.EmbeddingRequest.Model = model
	case br.SpeechRequest != nil:
		br.SpeechRequest.Model = model
	case br.TranscriptionRequest != nil:
		br.TranscriptionRequest.Model = model
	case br.ImageGenerationRequest != nil:
		br.ImageGenerationRequest.Model = model
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
	case br.CountTokensRequest != nil:
		br.CountTokensRequest.Fallbacks = fallbacks
	case br.EmbeddingRequest != nil:
		br.EmbeddingRequest.Fallbacks = fallbacks
	case br.SpeechRequest != nil:
		br.SpeechRequest.Fallbacks = fallbacks
	case br.TranscriptionRequest != nil:
		br.TranscriptionRequest.Fallbacks = fallbacks
	case br.ImageGenerationRequest != nil:
		br.ImageGenerationRequest.Fallbacks = fallbacks
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
	case br.CountTokensRequest != nil:
		br.CountTokensRequest.RawRequestBody = rawRequestBody
	case br.EmbeddingRequest != nil:
		br.EmbeddingRequest.RawRequestBody = rawRequestBody
	case br.SpeechRequest != nil:
		br.SpeechRequest.RawRequestBody = rawRequestBody
	case br.TranscriptionRequest != nil:
		br.TranscriptionRequest.RawRequestBody = rawRequestBody
	case br.ImageGenerationRequest != nil:
		br.ImageGenerationRequest.RawRequestBody = rawRequestBody
	}
}

//* Response Structs

// BifrostResponse represents the complete result from any bifrost request.
type BifrostResponse struct {
	TextCompletionResponse        *BifrostTextCompletionResponse
	ChatResponse                  *BifrostChatResponse
	ResponsesResponse             *BifrostResponsesResponse
	ResponsesStreamResponse       *BifrostResponsesStreamResponse
	CountTokensResponse           *BifrostCountTokensResponse
	EmbeddingResponse             *BifrostEmbeddingResponse
	SpeechResponse                *BifrostSpeechResponse
	SpeechStreamResponse          *BifrostSpeechStreamResponse
	TranscriptionResponse         *BifrostTranscriptionResponse
	TranscriptionStreamResponse   *BifrostTranscriptionStreamResponse
	ImageGenerationResponse       *BifrostImageGenerationResponse
	ImageGenerationStreamResponse *BifrostImageGenerationStreamResponse
	FileUploadResponse            *BifrostFileUploadResponse
	FileListResponse              *BifrostFileListResponse
	FileRetrieveResponse          *BifrostFileRetrieveResponse
	FileDeleteResponse            *BifrostFileDeleteResponse
	FileContentResponse           *BifrostFileContentResponse
	BatchCreateResponse           *BifrostBatchCreateResponse
	BatchListResponse             *BifrostBatchListResponse
	BatchRetrieveResponse         *BifrostBatchRetrieveResponse
	BatchCancelResponse           *BifrostBatchCancelResponse
	BatchResultsResponse          *BifrostBatchResultsResponse
	ContainerCreateResponse       *BifrostContainerCreateResponse
	ContainerListResponse         *BifrostContainerListResponse
	ContainerRetrieveResponse     *BifrostContainerRetrieveResponse
	ContainerDeleteResponse       *BifrostContainerDeleteResponse
	ContainerFileCreateResponse   *BifrostContainerFileCreateResponse
	ContainerFileListResponse     *BifrostContainerFileListResponse
	ContainerFileRetrieveResponse *BifrostContainerFileRetrieveResponse
	ContainerFileContentResponse  *BifrostContainerFileContentResponse
	ContainerFileDeleteResponse   *BifrostContainerFileDeleteResponse
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
	case r.CountTokensResponse != nil:
		return &r.CountTokensResponse.ExtraFields
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
	case r.ImageGenerationResponse != nil:
		return &r.ImageGenerationResponse.ExtraFields
	case r.ImageGenerationStreamResponse != nil:
		return &r.ImageGenerationStreamResponse.ExtraFields
	case r.FileUploadResponse != nil:
		return &r.FileUploadResponse.ExtraFields
	case r.FileListResponse != nil:
		return &r.FileListResponse.ExtraFields
	case r.FileRetrieveResponse != nil:
		return &r.FileRetrieveResponse.ExtraFields
	case r.FileDeleteResponse != nil:
		return &r.FileDeleteResponse.ExtraFields
	case r.FileContentResponse != nil:
		return &r.FileContentResponse.ExtraFields
	case r.BatchCreateResponse != nil:
		return &r.BatchCreateResponse.ExtraFields
	case r.BatchListResponse != nil:
		return &r.BatchListResponse.ExtraFields
	case r.BatchRetrieveResponse != nil:
		return &r.BatchRetrieveResponse.ExtraFields
	case r.BatchCancelResponse != nil:
		return &r.BatchCancelResponse.ExtraFields
	case r.BatchResultsResponse != nil:
		return &r.BatchResultsResponse.ExtraFields
	case r.ContainerCreateResponse != nil:
		return &r.ContainerCreateResponse.ExtraFields
	case r.ContainerListResponse != nil:
		return &r.ContainerListResponse.ExtraFields
	case r.ContainerRetrieveResponse != nil:
		return &r.ContainerRetrieveResponse.ExtraFields
	case r.ContainerDeleteResponse != nil:
		return &r.ContainerDeleteResponse.ExtraFields
	case r.ContainerFileCreateResponse != nil:
		return &r.ContainerFileCreateResponse.ExtraFields
	case r.ContainerFileListResponse != nil:
		return &r.ContainerFileListResponse.ExtraFields
	case r.ContainerFileRetrieveResponse != nil:
		return &r.ContainerFileRetrieveResponse.ExtraFields
	case r.ContainerFileContentResponse != nil:
		return &r.ContainerFileContentResponse.ExtraFields
	case r.ContainerFileDeleteResponse != nil:
		return &r.ContainerFileDeleteResponse.ExtraFields
	}

	return &BifrostResponseExtraFields{}
}

// BifrostResponseExtraFields contains additional fields in a response.
type BifrostResponseExtraFields struct {
	RequestType     RequestType        `json:"request_type"`
	Provider        ModelProvider      `json:"provider,omitempty"`
	ModelRequested  string             `json:"model_requested,omitempty"`
	ModelDeployment string             `json:"model_deployment,omitempty"` // only present for providers which use model deployments (e.g. Azure, Bedrock)
	Latency         int64              `json:"latency"`                    // in milliseconds (for streaming responses this will be each chunk latency, and the last chunk latency will be the total latency)
	ChunkIndex      int                `json:"chunk_index"`                // used for streaming responses to identify the chunk index, will be 0 for non-streaming responses
	RawRequest      interface{}        `json:"raw_request,omitempty"`
	RawResponse     interface{}        `json:"raw_response,omitempty"`
	CacheDebug      *BifrostCacheDebug `json:"cache_debug,omitempty"`
	ParseErrors     []BatchError       `json:"parse_errors,omitempty"` // errors encountered while parsing JSONL batch results
	LiteLLMCompat   bool               `json:"litellm_compat,omitempty"`
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
	RequestTimedOut  = "request_timed_out"
)

// BifrostStreamChunk represents a stream of responses from the Bifrost system.
// Either BifrostResponse or BifrostError will be non-nil.
type BifrostStreamChunk struct {
	*BifrostTextCompletionResponse
	*BifrostChatResponse
	*BifrostResponsesStreamResponse
	*BifrostSpeechStreamResponse
	*BifrostTranscriptionStreamResponse
	*BifrostImageGenerationStreamResponse
	*BifrostError
}

// MarshalJSON implements custom JSON marshaling for BifrostStreamChunk.
// This ensures that only the non-nil embedded struct is marshaled,
func (bs BifrostStreamChunk) MarshalJSON() ([]byte, error) {
	if bs.BifrostTextCompletionResponse != nil {
		return Marshal(bs.BifrostTextCompletionResponse)
	} else if bs.BifrostChatResponse != nil {
		return Marshal(bs.BifrostChatResponse)
	} else if bs.BifrostResponsesStreamResponse != nil {
		return Marshal(bs.BifrostResponsesStreamResponse)
	} else if bs.BifrostSpeechStreamResponse != nil {
		return Marshal(bs.BifrostSpeechStreamResponse)
	} else if bs.BifrostTranscriptionStreamResponse != nil {
		return Marshal(bs.BifrostTranscriptionStreamResponse)
	} else if bs.BifrostImageGenerationStreamResponse != nil {
		return Marshal(bs.BifrostImageGenerationStreamResponse)
	} else if bs.BifrostError != nil {
		return Marshal(bs.BifrostError)
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
	ExtraFields    BifrostErrorExtraFields `json:"extra_fields"`
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
	RawRequest     interface{}   `json:"raw_request,omitempty"`
	RawResponse    interface{}   `json:"raw_response,omitempty"`
	LiteLLMCompat  bool          `json:"litellm_compat,omitempty"`
}
