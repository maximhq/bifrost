// Package schemas defines the core schemas and types used by the Bifrost system.
package schemas

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"

	"github.com/maximhq/bifrost/core/pool"
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
	LLMPlugins         []LLMPlugin
	MCPPlugins         []MCPPlugin
	OAuth2Provider     OAuth2Provider
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
	Replicate   ModelProvider = "replicate"
	VLLM        ModelProvider = "vllm"
	Runway      ModelProvider = "runway"
)

// SupportedBaseProviders is the list of base providers allowed for custom providers.
var SupportedBaseProviders = []ModelProvider{
	Anthropic,
	Bedrock,
	Cohere,
	Gemini,
	OpenAI,
	HuggingFace,
	Replicate,
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
	XAI,
	Replicate,
	VLLM,
	Runway,
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
	ImageEditRequest             RequestType = "image_edit"
	ImageEditStreamRequest       RequestType = "image_edit_stream"
	ImageVariationRequest        RequestType = "image_variation"
	VideoGenerationRequest       RequestType = "video_generation"
	VideoRetrieveRequest         RequestType = "video_retrieve"
	VideoDownloadRequest         RequestType = "video_download"
	VideoDeleteRequest           RequestType = "video_delete"
	VideoListRequest             RequestType = "video_list"
	VideoRemixRequest            RequestType = "video_remix"
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
	RerankRequest                RequestType = "rerank"
	CountTokensRequest           RequestType = "count_tokens"
	MCPToolExecutionRequest      RequestType = "mcp_tool_execution"
	UnknownRequest               RequestType = "unknown"
)

// BifrostContextKey is a type for context keys used in Bifrost.
type BifrostContextKey string

// BifrostContextKeyRequestType is a context key for the request type.
const (
	BifrostContextKeyVirtualKey                          BifrostContextKey = "x-bf-vk"                              // string
	BifrostContextKeyAPIKeyName                          BifrostContextKey = "x-bf-api-key"                         // string (explicit key name selection)
	BifrostContextKeyRequestID                           BifrostContextKey = "request-id"                           // string
	BifrostContextKeyFallbackRequestID                   BifrostContextKey = "fallback-request-id"                  // string
	BifrostContextKeyDirectKey                           BifrostContextKey = "bifrost-direct-key"                   // Key struct
	BifrostContextKeySelectedKeyID                       BifrostContextKey = "bifrost-selected-key-id"              // string (to store the selected key ID (set by bifrost governance plugin - DO NOT SET THIS MANUALLY))
	BifrostContextKeySelectedKeyName                     BifrostContextKey = "bifrost-selected-key-name"            // string (to store the selected key name (set by bifrost governance plugin - DO NOT SET THIS MANUALLY))
	BifrostContextKeyGovernanceVirtualKeyID              BifrostContextKey = "bifrost-governance-virtual-key-id"    // string (to store the virtual key ID (set by bifrost governance plugin - DO NOT SET THIS MANUALLY))
	BifrostContextKeyGovernanceVirtualKeyName            BifrostContextKey = "bifrost-governance-virtual-key-name"  // string (to store the virtual key name (set by bifrost governance plugin - DO NOT SET THIS MANUALLY))
	BifrostContextKeyGovernanceTeamID                    BifrostContextKey = "bifrost-governance-team-id"           // string (to store the team ID (set by bifrost governance plugin - DO NOT SET THIS MANUALLY))
	BifrostContextKeyGovernanceTeamName                  BifrostContextKey = "bifrost-governance-team-name"         // string (to store the team name (set by bifrost governance plugin - DO NOT SET THIS MANUALLY))
	BifrostContextKeyGovernanceCustomerID                BifrostContextKey = "bifrost-governance-customer-id"       // string (to store the customer ID (set by bifrost governance plugin - DO NOT SET THIS MANUALLY))
	BifrostContextKeyGovernanceCustomerName              BifrostContextKey = "bifrost-governance-customer-name"     // string (to store the customer name (set by bifrost governance plugin - DO NOT SET THIS MANUALLY))
	BifrostContextKeyGovernanceUserID                    BifrostContextKey = "bifrost-governance-user-id"           // string (to store the user ID (set by enterprise governance plugin - DO NOT SET THIS MANUALLY))
	BifrostContextKeyGovernanceRoutingRuleID             BifrostContextKey = "bifrost-governance-routing-rule-id"   // string (to store the routing rule ID (set by bifrost governance plugin - DO NOT SET THIS MANUALLY))
	BifrostContextKeyGovernanceRoutingRuleName           BifrostContextKey = "bifrost-governance-routing-rule-name" // string (to store the routing rule name (set by bifrost governance plugin - DO NOT SET THIS MANUALLY))
	BifrostContextKeyGovernanceIncludeOnlyKeys           BifrostContextKey = "bf-governance-include-only-keys"      // []string (to store the include-only key IDs for provider config routing (set by bifrost governance plugin - DO NOT SET THIS MANUALLY))
	BifrostContextKeyNumberOfRetries                     BifrostContextKey = "bifrost-number-of-retries"            // int (to store the number of retries (set by bifrost - DO NOT SET THIS MANUALLY))
	BifrostContextKeyFallbackIndex                       BifrostContextKey = "bifrost-fallback-index"               // int (to store the fallback index (set by bifrost - DO NOT SET THIS MANUALLY)) 0 for primary, 1 for first fallback, etc.
	BifrostContextKeyStreamEndIndicator                  BifrostContextKey = "bifrost-stream-end-indicator"         // bool (set by bifrost - DO NOT SET THIS MANUALLY))
	BifrostContextKeySkipKeySelection                    BifrostContextKey = "bifrost-skip-key-selection"           // bool (will pass an empty key to the provider)
	BifrostContextKeyExtraHeaders                        BifrostContextKey = "bifrost-extra-headers"                // map[string][]string
	BifrostContextKeyURLPath                             BifrostContextKey = "bifrost-extra-url-path"               // string
	BifrostContextKeyUseRawRequestBody                   BifrostContextKey = "bifrost-use-raw-request-body"
	BifrostContextKeySendBackRawRequest                  BifrostContextKey = "bifrost-send-back-raw-request"                    // bool
	BifrostContextKeySendBackRawResponse                 BifrostContextKey = "bifrost-send-back-raw-response"                   // bool
	BifrostContextKeyIntegrationType                     BifrostContextKey = "bifrost-integration-type"                         // integration used in gateway (e.g. openai, anthropic, bedrock, etc.)
	BifrostContextKeyIsResponsesToChatCompletionFallback BifrostContextKey = "bifrost-is-responses-to-chat-completion-fallback" // bool (set by bifrost - DO NOT SET THIS MANUALLY))
	BifrostMCPAgentOriginalRequestID                     BifrostContextKey = "bifrost-mcp-agent-original-request-id"            // string (to store the original request ID for MCP agent mode)
	BifrostContextKeyParentMCPRequestID                  BifrostContextKey = "bf-parent-mcp-request-id"                         // string (parent request ID for nested tool calls from executeCode)
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
	BifrostContextKeyRetryDBFetch                        BifrostContextKey = "bifrost-retry-db-fetch"                           // bool (set by bifrost - DO NOT SET THIS MANUALLY))
	BifrostContextKeyIsCustomProvider                    BifrostContextKey = "bifrost-is-custom-provider"                       // bool (set by bifrost - DO NOT SET THIS MANUALLY))
	BifrostContextKeyHTTPRequestType                     BifrostContextKey = "bifrost-http-request-type"                        // RequestType (set by bifrost - DO NOT SET THIS MANUALLY))
	BifrostContextKeyPassthroughExtraParams              BifrostContextKey = "bifrost-passthrough-extra-params"                 // bool
	BifrostContextKeyRoutingEnginesUsed                  BifrostContextKey = "bifrost-routing-engines-used"                     // []string (set by bifrost - DO NOT SET THIS MANUALLY) - list of routing engines used ("routing-rule", "governance", "loadbalancing", etc.)
	BifrostContextKeyRoutingEngineLogs                   BifrostContextKey = "bifrost-routing-engine-logs"                      // []RoutingEngineLogEntry (set by bifrost - DO NOT SET THIS MANUALLY) - list of routing engine log entries
	BifrostContextKeySkipPluginPipeline                  BifrostContextKey = "bifrost-skip-plugin-pipeline"                     // bool - skip plugin pipeline for the request
	BifrostIsAsyncRequest                                BifrostContextKey = "bifrost-is-async-request"                         // bool (set by bifrost - DO NOT SET THIS MANUALLY)) - whether the request is an async request (only used in gateway)
	BifrostContextKeyRequestHeaders                      BifrostContextKey = "bifrost-request-headers"                          // map[string]string (all request headers with lowercased keys)
	BifrostContextKeySkipListModelsGovernanceFiltering   BifrostContextKey = "bifrost-skip-list-models-governance-filtering"    // bool (set by bifrost - DO NOT SET THIS MANUALLY))
	BifrostContextKeySCIMClaims                          BifrostContextKey = "scim_claims"
	BifrostContextKeyUserID                              BifrostContextKey = "user_id"
	BifrostContextKeyTargetUserID                        BifrostContextKey = "target_user_id"
	BifrostContextKeyIsAzureUserAgent                    BifrostContextKey = "bifrost-is-azure-user-agent" // bool (set by bifrost - DO NOT SET THIS MANUALLY)) - whether the request is an Azure user agent (only used in gateway)
	BifrostContextKeyVideoOutputRequested                BifrostContextKey = "bifrost-video-output-requested"
	BifrostContextKeyProviderResponseHeaders             BifrostContextKey = "bifrost-provider-response-headers" // map[string]string (set by provider streaming handlers - DO NOT SET THIS MANUALLY)
)

// RoutingEngine constants
const (
	RoutingEngineGovernance    = "governance"
	RoutingEngineRoutingRule   = "routing-rule"
	RoutingEngineLoadbalancing = "loadbalancing"
)

// RoutingEngineLogEntry represents a log entry from a routing engine
// format: [timestamp] [engine] - message
type RoutingEngineLogEntry struct {
	Engine    string // e.g., "governance", "routing-rule", "openrouter"
	Message   string // Human-readable decision/action message
	Timestamp int64  // Unix milliseconds
}

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
// - RerankRequest
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
	RerankRequest                *BifrostRerankRequest
	SpeechRequest                *BifrostSpeechRequest
	TranscriptionRequest         *BifrostTranscriptionRequest
	ImageGenerationRequest       *BifrostImageGenerationRequest
	ImageEditRequest             *BifrostImageEditRequest
	ImageVariationRequest        *BifrostImageVariationRequest
	VideoGenerationRequest       *BifrostVideoGenerationRequest
	VideoRetrieveRequest         *BifrostVideoRetrieveRequest
	VideoDownloadRequest         *BifrostVideoDownloadRequest
	VideoListRequest             *BifrostVideoListRequest
	VideoRemixRequest            *BifrostVideoRemixRequest
	VideoDeleteRequest           *BifrostVideoDeleteRequest
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
	case br.ListModelsRequest != nil:
		return br.ListModelsRequest.Provider, "", nil
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
	case br.RerankRequest != nil:
		return br.RerankRequest.Provider, br.RerankRequest.Model, br.RerankRequest.Fallbacks
	case br.SpeechRequest != nil:
		return br.SpeechRequest.Provider, br.SpeechRequest.Model, br.SpeechRequest.Fallbacks
	case br.TranscriptionRequest != nil:
		return br.TranscriptionRequest.Provider, br.TranscriptionRequest.Model, br.TranscriptionRequest.Fallbacks
	case br.ImageGenerationRequest != nil:
		return br.ImageGenerationRequest.Provider, br.ImageGenerationRequest.Model, br.ImageGenerationRequest.Fallbacks
	case br.ImageEditRequest != nil:
		return br.ImageEditRequest.Provider, br.ImageEditRequest.Model, br.ImageEditRequest.Fallbacks
	case br.ImageVariationRequest != nil:
		return br.ImageVariationRequest.Provider, br.ImageVariationRequest.Model, br.ImageVariationRequest.Fallbacks
	case br.VideoGenerationRequest != nil:
		return br.VideoGenerationRequest.Provider, br.VideoGenerationRequest.Model, br.VideoGenerationRequest.Fallbacks
	case br.VideoRetrieveRequest != nil:
		return br.VideoRetrieveRequest.Provider, "", nil
	case br.VideoDownloadRequest != nil:
		return br.VideoDownloadRequest.Provider, "", nil
	case br.VideoListRequest != nil:
		return br.VideoListRequest.Provider, "", nil
	case br.VideoDeleteRequest != nil:
		return br.VideoDeleteRequest.Provider, "", nil
	case br.VideoRemixRequest != nil:
		return br.VideoRemixRequest.Provider, "", nil
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
	case br.ListModelsRequest != nil:
		br.ListModelsRequest.Provider = provider
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
	case br.RerankRequest != nil:
		br.RerankRequest.Provider = provider
	case br.SpeechRequest != nil:
		br.SpeechRequest.Provider = provider
	case br.TranscriptionRequest != nil:
		br.TranscriptionRequest.Provider = provider
	case br.ImageGenerationRequest != nil:
		br.ImageGenerationRequest.Provider = provider
	case br.ImageEditRequest != nil:
		br.ImageEditRequest.Provider = provider
	case br.ImageVariationRequest != nil:
		br.ImageVariationRequest.Provider = provider
	case br.VideoGenerationRequest != nil:
		br.VideoGenerationRequest.Provider = provider
	case br.VideoRetrieveRequest != nil:
		br.VideoRetrieveRequest.Provider = provider
	case br.VideoDownloadRequest != nil:
		br.VideoDownloadRequest.Provider = provider
	case br.VideoListRequest != nil:
		br.VideoListRequest.Provider = provider
	case br.VideoDeleteRequest != nil:
		br.VideoDeleteRequest.Provider = provider
	case br.VideoRemixRequest != nil:
		br.VideoRemixRequest.Provider = provider
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
	case br.RerankRequest != nil:
		br.RerankRequest.Model = model
	case br.SpeechRequest != nil:
		br.SpeechRequest.Model = model
	case br.TranscriptionRequest != nil:
		br.TranscriptionRequest.Model = model
	case br.ImageGenerationRequest != nil:
		br.ImageGenerationRequest.Model = model
	case br.ImageEditRequest != nil:
		br.ImageEditRequest.Model = model
	case br.ImageVariationRequest != nil:
		br.ImageVariationRequest.Model = model
	case br.VideoGenerationRequest != nil:
		br.VideoGenerationRequest.Model = model
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
	case br.RerankRequest != nil:
		br.RerankRequest.Fallbacks = fallbacks
	case br.SpeechRequest != nil:
		br.SpeechRequest.Fallbacks = fallbacks
	case br.TranscriptionRequest != nil:
		br.TranscriptionRequest.Fallbacks = fallbacks
	case br.ImageGenerationRequest != nil:
		br.ImageGenerationRequest.Fallbacks = fallbacks
	case br.ImageEditRequest != nil:
		br.ImageEditRequest.Fallbacks = fallbacks
	case br.ImageVariationRequest != nil:
		br.ImageVariationRequest.Fallbacks = fallbacks
	case br.VideoGenerationRequest != nil:
		br.VideoGenerationRequest.Fallbacks = fallbacks
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
	case br.RerankRequest != nil:
		br.RerankRequest.RawRequestBody = rawRequestBody
	case br.SpeechRequest != nil:
		br.SpeechRequest.RawRequestBody = rawRequestBody
	case br.TranscriptionRequest != nil:
		br.TranscriptionRequest.RawRequestBody = rawRequestBody
	case br.ImageGenerationRequest != nil:
		br.ImageGenerationRequest.RawRequestBody = rawRequestBody
	case br.ImageEditRequest != nil:
		br.ImageEditRequest.RawRequestBody = rawRequestBody
	case br.ImageVariationRequest != nil:
		br.ImageVariationRequest.RawRequestBody = rawRequestBody
	case br.VideoGenerationRequest != nil:
		br.VideoGenerationRequest.RawRequestBody = rawRequestBody
	case br.VideoRemixRequest != nil:
		br.VideoRemixRequest.RawRequestBody = rawRequestBody
	}
}

type MCPRequestType string

const (
	MCPRequestTypeChatToolCall      MCPRequestType = "chat_tool_call"      // Chat API format
	MCPRequestTypeResponsesToolCall MCPRequestType = "responses_tool_call" // Responses API format
)

// BifrostMCPRequest is the request struct for all MCP requests.
// only ONE of the following fields should be set:
// - ChatAssistantMessageToolCall
// - ResponsesToolMessage
type BifrostMCPRequest struct {
	RequestType MCPRequestType

	*ChatAssistantMessageToolCall
	*ResponsesToolMessage
}

func (r *BifrostMCPRequest) GetToolName() string {
	if r.ChatAssistantMessageToolCall != nil {
		if r.ChatAssistantMessageToolCall.Function.Name != nil {
			return *r.ChatAssistantMessageToolCall.Function.Name
		}
	}
	if r.ResponsesToolMessage != nil {
		if r.ResponsesToolMessage.Name != nil {
			return *r.ResponsesToolMessage.Name
		}
	}
	return ""
}

func (r *BifrostMCPRequest) GetToolArguments() interface{} {
	if r.ChatAssistantMessageToolCall != nil {
		return r.ChatAssistantMessageToolCall.Function.Arguments
	}
	if r.ResponsesToolMessage != nil {
		return r.ResponsesToolMessage.Arguments
	}
	return nil
}

//* Response Structs

// BifrostResponse represents the complete result from any bifrost request.
type BifrostResponse struct {
	ListModelsResponse            *BifrostListModelsResponse
	TextCompletionResponse        *BifrostTextCompletionResponse
	ChatResponse                  *BifrostChatResponse
	ResponsesResponse             *BifrostResponsesResponse
	ResponsesStreamResponse       *BifrostResponsesStreamResponse
	CountTokensResponse           *BifrostCountTokensResponse
	EmbeddingResponse             *BifrostEmbeddingResponse
	RerankResponse                *BifrostRerankResponse
	SpeechResponse                *BifrostSpeechResponse
	SpeechStreamResponse          *BifrostSpeechStreamResponse
	TranscriptionResponse         *BifrostTranscriptionResponse
	TranscriptionStreamResponse   *BifrostTranscriptionStreamResponse
	ImageGenerationResponse       *BifrostImageGenerationResponse
	ImageGenerationStreamResponse *BifrostImageGenerationStreamResponse
	VideoGenerationResponse       *BifrostVideoGenerationResponse
	VideoDownloadResponse         *BifrostVideoDownloadResponse
	VideoListResponse             *BifrostVideoListResponse
	VideoDeleteResponse           *BifrostVideoDeleteResponse
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
	case r.ListModelsResponse != nil:
		return &r.ListModelsResponse.ExtraFields
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
	case r.RerankResponse != nil:
		return &r.RerankResponse.ExtraFields
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
	case r.VideoGenerationResponse != nil:
		return &r.VideoGenerationResponse.ExtraFields
	case r.VideoDownloadResponse != nil:
		return &r.VideoDownloadResponse.ExtraFields
	case r.VideoListResponse != nil:
		return &r.VideoListResponse.ExtraFields
	case r.VideoDeleteResponse != nil:
		return &r.VideoDeleteResponse.ExtraFields
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

// BifrostMCPResponse is the response struct for all MCP responses.
// only ONE of the following fields should be set:
// - ChatMessage
// - ResponsesMessage
type BifrostMCPResponse struct {
	ChatMessage      *ChatMessage
	ResponsesMessage *ResponsesMessage
	ExtraFields      BifrostMCPResponseExtraFields
}

// bifrostMCPResponsePool provides a pool for BifrostMCPResponse objects.
var bifrostMCPResponsePool = pool.New[BifrostMCPResponse]("BifrostMCPResponse", func() *BifrostMCPResponse {
	return &BifrostMCPResponse{}
})

// AcquireBifrostMCPResponse gets a BifrostMCPResponse from the pool and resets it.
func AcquireBifrostMCPResponse() *BifrostMCPResponse {
	return bifrostMCPResponsePool.Get()
}

// ReleaseBifrostMCPResponse returns a BifrostMCPResponse to the pool.
func ReleaseBifrostMCPResponse(r *BifrostMCPResponse) {
	if r == nil {
		return
	}
	r.ChatMessage = nil
	r.ResponsesMessage = nil
	r.ExtraFields = BifrostMCPResponseExtraFields{}
	bifrostMCPResponsePool.Put(r)
}

// BifrostResponseExtraFields contains additional fields in a response.
type BifrostResponseExtraFields struct {
	RequestType             RequestType        `json:"request_type"`
	Provider                ModelProvider      `json:"provider,omitempty"`
	ModelRequested          string             `json:"model_requested,omitempty"`
	ModelDeployment         string             `json:"model_deployment,omitempty"` // only present for providers which use model deployments (e.g. Azure, Bedrock)
	Latency                 int64              `json:"latency"`                    // in milliseconds (for streaming responses this will be each chunk latency, and the last chunk latency will be the total latency)
	ChunkIndex              int                `json:"chunk_index"`                // used for streaming responses to identify the chunk index, will be 0 for non-streaming responses
	RawRequest              interface{}        `json:"raw_request,omitempty"`
	RawResponse             interface{}        `json:"raw_response,omitempty"`
	CacheDebug              *BifrostCacheDebug `json:"cache_debug,omitempty"`
	ParseErrors             []BatchError       `json:"parse_errors,omitempty"` // errors encountered while parsing JSONL batch results
	LiteLLMCompat           bool               `json:"litellm_compat,omitempty"`
	ProviderResponseHeaders map[string]string  `json:"provider_response_headers,omitempty"` // HTTP response headers from the provider (filtered to exclude transport-level headers)
}

// bifrostResponseExtraFieldsPool provides a pool for BifrostResponseExtraFields objects.
var bifrostResponseExtraFieldsPool = pool.New[BifrostResponseExtraFields]("BifrostResponseExtraFields", func() *BifrostResponseExtraFields {
	return &BifrostResponseExtraFields{}
})

// AcquireBifrostResponseExtraFields gets a BifrostResponseExtraFields from the pool and resets it.
func AcquireBifrostResponseExtraFields() *BifrostResponseExtraFields {
	return bifrostResponseExtraFieldsPool.Get()
}

// ReleaseBifrostResponseExtraFields returns a BifrostResponseExtraFields to the pool.
func ReleaseBifrostResponseExtraFields(r *BifrostResponseExtraFields) {
	if r == nil {
		return
	}
	r.RequestType = ""
	r.Provider = ""
	r.ModelRequested = ""
	r.ModelDeployment = ""
	r.Latency = 0
	r.ChunkIndex = 0
	r.RawRequest = nil
	r.RawResponse = nil
	r.CacheDebug = nil
	r.ParseErrors = nil
	r.LiteLLMCompat = false
	r.ProviderResponseHeaders = nil
	bifrostResponseExtraFieldsPool.Put(r)
}

type BifrostMCPResponseExtraFields struct {
	ClientName string `json:"client_name"`
	ToolName   string `json:"tool_name"`
	Latency    int64  `json:"latency"` // in milliseconds
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
// PLUGIN DEVELOPERS: When creating BifrostError in PreLLMHook or PostLLMHook, you can set AllowFallbacks:
// - AllowFallbacks = &true: Bifrost will try fallback providers if available
// - AllowFallbacks = &false: Bifrost will return this error immediately, no fallbacks
// - AllowFallbacks = nil: Treated as true by default (fallbacks allowed for resilience)
type BifrostError struct {
	EventID        *string                  `json:"event_id,omitempty"`
	Type           *string                  `json:"type,omitempty"`
	IsBifrostError bool                     `json:"is_bifrost_error"`
	StatusCode     *int                     `json:"status_code,omitempty"`
	Error          *ErrorField              `json:"error"`
	AllowFallbacks *bool                    `json:"-"` // Optional: Controls fallback behavior (nil = true by default)
	StreamControl  *StreamControl           `json:"-"` // Optional: Controls stream behavior
	ExtraFields    *BifrostErrorExtraFields `json:"extra_fields,omitempty"`
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
	aux := &struct {
		Type    *string     `json:"type,omitempty"`
		Code    interface{} `json:"code,omitempty"`
		Message string      `json:"message"`
		Error   *string     `json:"error,omitempty"`
		Param   interface{} `json:"param,omitempty"`
		EventID *string     `json:"event_id,omitempty"`
	}{}

	if err := json.Unmarshal(data, aux); err != nil {
		return err
	}

	e.Type = aux.Type
	e.Message = aux.Message
	e.Param = aux.Param
	e.EventID = aux.EventID
	if aux.Error != nil {
		e.Error = errors.New(*aux.Error)
	}
	if aux.Code != nil {
		switch v := aux.Code.(type) {
		case string:
			e.Code = &v
		case float64:
			s := strconv.FormatInt(int64(v), 10)
			e.Code = &s
		default:
			s := fmt.Sprint(aux.Code)
			e.Code = &s
		}
	}
	return nil
}

var bifrostErrorFieldPool = pool.New("BifrostErrorField", func() *ErrorField {
	return &ErrorField{}
})

// AcquireBifrostErrorField gets a ErrorField from the pool and resets it.
func AcquireBifrostErrorField() *ErrorField {
	return bifrostErrorFieldPool.Get()
}

// ReleaseBifrostErrorField returns a ErrorField to the pool.
func ReleaseBifrostErrorField(e *ErrorField) {
	if e == nil {
		return
	}
	e.Type = nil
	e.Code = nil
	e.Message = ""
	e.Error = nil
	e.Param = nil
	e.EventID = nil
	bifrostErrorFieldPool.Put(e)
}

// BifrostErrorExtraFields contains additional fields in an error response.
type BifrostErrorExtraFields struct {
	Provider       ModelProvider `json:"provider,omitempty"`
	ModelRequested string        `json:"model_requested,omitempty"`
	RequestType    RequestType   `json:"request_type,omitempty"`
	RawRequest     interface{}   `json:"raw_request,omitempty"`
	RawResponse    interface{}   `json:"raw_response,omitempty"`
	LiteLLMCompat  bool          `json:"litellm_compat,omitempty"`
	KeyStatuses    []KeyStatus   `json:"key_statuses,omitempty"`
}

// AcquireBifrostErrorExtraFields gets a BifrostErrorExtraFields from the pool and resets it.
var bifrostErrorExtraFieldsPool = pool.New("BifrostErrorExtraFields", func() *BifrostErrorExtraFields {
	return &BifrostErrorExtraFields{}
})

// AcquireBifrostErrorExtraFields gets a BifrostErrorExtraFields from the pool and resets it.
func AcquireBifrostErrorExtraFields() *BifrostErrorExtraFields {
	return bifrostErrorExtraFieldsPool.Get()
}

// ReleaseBifrostErrorExtraFields returns a BifrostErrorExtraFields to the pool.
func ReleaseBifrostErrorExtraFields(r *BifrostErrorExtraFields) {
	if r == nil {
		return
	}
	r.Provider = ""
	r.ModelRequested = ""
	r.RequestType = ""
	r.RawRequest = nil
	r.RawResponse = nil
	r.LiteLLMCompat = false
	r.KeyStatuses = nil
	bifrostErrorExtraFieldsPool.Put(r)
}

// bifrostErrorPool provides a pool for BifrostError objects to reduce allocations
// during streaming error detection. This is particularly useful in streaming handlers
// where error checking happens on every chunk but errors are rare.
var bifrostErrorPool = pool.New("BifrostError", func() *BifrostError {
	return &BifrostError{}
})

// AcquireBifrostError gets a BifrostError from the pool and resets it.
// Use this for streaming error detection to avoid allocations on every chunk.
func AcquireBifrostError() *BifrostError {
	err := bifrostErrorPool.Get()
	err.Error = AcquireBifrostErrorField()
	err.StatusCode = nil
	err.Type = nil
	err.EventID = nil
	return err
}

// ReleaseBifrostError returns a BifrostError to the pool.
// Do NOT release errors that are passed to error handlers or sent on channels,
// as they may still be in use.
func ReleaseBifrostError(err *BifrostError) {
	if err != nil {
		err.EventID = nil
		err.Type = nil
		err.IsBifrostError = false
		err.StatusCode = nil
		if err.Error != nil {
			ReleaseBifrostErrorField(err.Error)
			err.Error = nil
		}
		err.AllowFallbacks = nil
		err.StreamControl = nil

		if err.ExtraFields != nil {
			ReleaseBifrostErrorExtraFields(err.ExtraFields)
			err.ExtraFields = nil
		}
		bifrostErrorPool.Put(err)
	}
}

// bifrostRequestPool provides a pool for BifrostRequest objects to reduce allocations.
// BifrostRequest is a container struct with pointers to specific request types.
var bifrostRequestPool = pool.New("BifrostRequest", func() *BifrostRequest {
	return &BifrostRequest{}
})

// AcquireBifrostRequest gets a BifrostRequest from the pool and resets it.
func AcquireBifrostRequest() *BifrostRequest {
	return bifrostRequestPool.Get()
}

// ReleaseBifrostRequest returns a BifrostRequest to the pool.
func ReleaseBifrostRequest(br *BifrostRequest) {
	if br == nil {
		return
	}
	// Nil all pointer fields to break references and allow GC of inner structs
	br.RequestType = ""
	br.ListModelsRequest = nil
	br.TextCompletionRequest = nil
	br.ChatRequest = nil
	br.ResponsesRequest = nil
	br.CountTokensRequest = nil
	br.EmbeddingRequest = nil
	br.SpeechRequest = nil
	br.TranscriptionRequest = nil
	br.ImageGenerationRequest = nil
	br.ImageEditRequest = nil
	br.ImageVariationRequest = nil
	br.FileUploadRequest = nil
	br.FileListRequest = nil
	br.FileRetrieveRequest = nil
	br.FileDeleteRequest = nil
	br.FileContentRequest = nil
	br.BatchCreateRequest = nil
	br.BatchListRequest = nil
	br.BatchRetrieveRequest = nil
	br.BatchCancelRequest = nil
	br.BatchResultsRequest = nil
	br.ContainerCreateRequest = nil
	br.ContainerListRequest = nil
	br.ContainerRetrieveRequest = nil
	br.ContainerDeleteRequest = nil
	br.ContainerFileCreateRequest = nil
	br.ContainerFileListRequest = nil
	br.ContainerFileRetrieveRequest = nil
	br.ContainerFileContentRequest = nil
	br.ContainerFileDeleteRequest = nil
	bifrostRequestPool.Put(br)
}

// bifrostResponsePool provides a pool for BifrostResponse wrapper objects to reduce
// per-chunk allocations during streaming. The BifrostResponse is a temporary container
// that wraps inner response pointers; it is no longer needed after the BifrostStreamChunk
// copies those pointers and is sent on the channel.
var bifrostResponsePool = sync.Pool{
	New: func() interface{} {
		return &BifrostResponse{}
	},
}

// AcquireBifrostResponse gets a BifrostResponse from the pool and resets it.
func AcquireBifrostResponse() *BifrostResponse {
	r := bifrostResponsePool.Get().(*BifrostResponse)
	*r = BifrostResponse{}
	return r
}

// ReleaseBifrostResponse returns a BifrostResponse to the pool.
// The caller must ensure no other goroutine holds a reference to this response.
func ReleaseBifrostResponse(r *BifrostResponse) {
	if r == nil {
		return
	}
	if r.ListModelsResponse != nil {
		ReleaseBifrostListModelsResponse(r.ListModelsResponse)
		r.ListModelsResponse = nil
	}
	if r.TextCompletionResponse != nil {
		ReleaseBifrostTextCompletionResponse(r.TextCompletionResponse)
		r.TextCompletionResponse = nil
	}
	if r.ChatResponse != nil {
		ReleaseBifrostChatResponse(r.ChatResponse)
		r.ChatResponse = nil
	}
	if r.ResponsesResponse != nil {
		ReleaseBifrostResponsesResponse(r.ResponsesResponse)
		r.ResponsesResponse = nil
	}
	if r.ResponsesStreamResponse != nil {
		ReleaseBifrostResponsesStreamResponse(r.ResponsesStreamResponse)
		r.ResponsesStreamResponse = nil
	}
	if r.CountTokensResponse != nil {
		ReleaseBifrostCountTokensResponse(r.CountTokensResponse)
		r.CountTokensResponse = nil
	}
	if r.EmbeddingResponse != nil {
		ReleaseBifrostEmbeddingResponse(r.EmbeddingResponse)
		r.EmbeddingResponse = nil
	}
	if r.SpeechResponse != nil {
		ReleaseBifrostSpeechResponse(r.SpeechResponse)
		r.SpeechResponse = nil
	}
	if r.SpeechStreamResponse != nil {
		ReleaseBifrostSpeechStreamResponse(r.SpeechStreamResponse)
		r.SpeechStreamResponse = nil
	}
	if r.TranscriptionResponse != nil {
		ReleaseBifrostTranscriptionResponse(r.TranscriptionResponse)
		r.TranscriptionResponse = nil
	}
	if r.TranscriptionStreamResponse != nil {
		ReleaseBifrostTranscriptionStreamResponse(r.TranscriptionStreamResponse)
		r.TranscriptionStreamResponse = nil
	}
	if r.ImageGenerationResponse != nil {
		ReleaseBifrostImageGenerationResponse(r.ImageGenerationResponse)
		r.ImageGenerationResponse = nil
	}
	if r.ImageGenerationStreamResponse != nil {
		ReleaseBifrostImageGenerationStreamResponse(r.ImageGenerationStreamResponse)
		r.ImageGenerationStreamResponse = nil
	}	
	if r.FileUploadResponse != nil {
		ReleaseBifrostFileUploadResponse(r.FileUploadResponse)
		r.FileUploadResponse = nil
	}
	if r.FileListResponse != nil {
		ReleaseBifrostFileListResponse(r.FileListResponse)
		r.FileListResponse = nil
	}
	if r.FileRetrieveResponse != nil {
		ReleaseBifrostFileRetrieveResponse(r.FileRetrieveResponse)
		r.FileRetrieveResponse = nil
	}
	if r.FileDeleteResponse != nil {
		ReleaseBifrostFileDeleteResponse(r.FileDeleteResponse)
		r.FileDeleteResponse = nil
	}
	if r.FileContentResponse != nil {
		ReleaseBifrostFileContentResponse(r.FileContentResponse)
		r.FileContentResponse = nil
	}
	if r.BatchCreateResponse != nil {
		ReleaseBifrostBatchCreateResponse(r.BatchCreateResponse)
		r.BatchCreateResponse = nil
	}
	if r.BatchListResponse != nil {
		ReleaseBifrostBatchListResponse(r.BatchListResponse)
		r.BatchListResponse = nil
	}
	if r.BatchRetrieveResponse != nil {
		ReleaseBifrostBatchRetrieveResponse(r.BatchRetrieveResponse)
		r.BatchRetrieveResponse = nil
	}
	if r.BatchCancelResponse != nil {
		ReleaseBifrostBatchCancelResponse(r.BatchCancelResponse)
		r.BatchCancelResponse = nil
	}
	if r.BatchResultsResponse != nil {
		ReleaseBifrostBatchResultsResponse(r.BatchResultsResponse)
		r.BatchResultsResponse = nil
	}
	if r.ContainerCreateResponse != nil {
		ReleaseBifrostContainerCreateResponse(r.ContainerCreateResponse)
		r.ContainerCreateResponse = nil
	}
	if r.ContainerListResponse != nil {
		ReleaseBifrostContainerListResponse(r.ContainerListResponse)
		r.ContainerListResponse = nil
	}
	if r.ContainerRetrieveResponse != nil {
		ReleaseBifrostContainerRetrieveResponse(r.ContainerRetrieveResponse)
		r.ContainerRetrieveResponse = nil
	}
	if r.ContainerDeleteResponse != nil {
		ReleaseBifrostContainerDeleteResponse(r.ContainerDeleteResponse)
		r.ContainerDeleteResponse = nil
	}
	if r.ContainerFileCreateResponse != nil {
		ReleaseBifrostContainerFileCreateResponse(r.ContainerFileCreateResponse)
		r.ContainerFileCreateResponse = nil
	}
	if r.ContainerFileListResponse != nil {
		ReleaseBifrostContainerFileListResponse(r.ContainerFileListResponse)
		r.ContainerFileListResponse = nil
	}
	if r.ContainerFileRetrieveResponse != nil {
		ReleaseBifrostContainerFileRetrieveResponse(r.ContainerFileRetrieveResponse)
		r.ContainerFileRetrieveResponse = nil
	}
	if r.ContainerFileContentResponse != nil {
		ReleaseBifrostContainerFileContentResponse(r.ContainerFileContentResponse)
		r.ContainerFileContentResponse = nil
	}
	if r.ContainerFileDeleteResponse != nil {
		ReleaseBifrostContainerFileDeleteResponse(r.ContainerFileDeleteResponse)
		r.ContainerFileDeleteResponse = nil
	}	
	bifrostResponsePool.Put(r)
}

// bifrostStreamChunkPool provides a pool for BifrostStreamChunk objects to reduce
// per-chunk allocations during streaming. Each chunk sent on the streaming channel
// allocates a BifrostStreamChunk; pooling avoids this heap allocation.
var bifrostStreamChunkPool = sync.Pool{
	New: func() interface{} {
		return &BifrostStreamChunk{}
	},
}

// AcquireBifrostStreamChunk gets a BifrostStreamChunk from the pool and resets it.
func AcquireBifrostStreamChunk() *BifrostStreamChunk {
	c := bifrostStreamChunkPool.Get().(*BifrostStreamChunk)
	*c = BifrostStreamChunk{}
	return c
}

// ReleaseBifrostStreamChunk returns a BifrostStreamChunk to its pool.
// NOTE: The inner BifrostChatResponse is NOT released here because post-hook
// goroutines (e.g. PostLLMHook) may still hold a reference to it via the
// original BifrostResponse. Releasing it while a goroutine reads it causes
// a data race (nil-pointer panic on ChatResponse.Usage). The ChatResponse
// is reclaimed by GC once all references are dropped.
func ReleaseBifrostStreamChunk(c *BifrostStreamChunk) {
	if c == nil {
		return
	}
	// Release the BifrostError back to its pool if present
	if c.BifrostError != nil {
		ReleaseBifrostError(c.BifrostError)
	}
	c.BifrostChatResponse = nil
	c.BifrostTextCompletionResponse = nil
	c.BifrostResponsesStreamResponse = nil
	c.BifrostSpeechStreamResponse = nil
	c.BifrostTranscriptionStreamResponse = nil
	c.BifrostImageGenerationStreamResponse = nil
	c.BifrostError = nil
	bifrostStreamChunkPool.Put(c)
}
