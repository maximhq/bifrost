// Package schemas defines the core schemas and types used by the Bifrost system.
package schemas

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
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
	KVStore            KVStore     // shared KV store for clustering/session stickiness; nil = disabled
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
	BatchDeleteRequest           RequestType = "batch_delete"
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
	PassthroughRequest           RequestType = "passthrough"
	PassthroughStreamRequest     RequestType = "passthrough_stream"
	UnknownRequest               RequestType = "unknown"
	WebSocketResponsesRequest    RequestType = "websocket_responses"
	RealtimeRequest              RequestType = "realtime"
)

// BifrostContextKey is a type for context keys used in Bifrost.
type BifrostContextKey string

// BifrostContextKeyRequestType is a context key for the request type.
const (
	BifrostContextKeySessionToken                        BifrostContextKey = "bifrost-session-token"                // string (session token for authentication - set by auth middleware)
	BifrostContextKeyVirtualKey                          BifrostContextKey = "x-bf-vk"                              // string
	BifrostContextKeyAPIKeyName                          BifrostContextKey = "x-bf-api-key"                         // string (explicit key name selection)
	BifrostContextKeyAPIKeyID                            BifrostContextKey = "x-bf-api-key-id"                      // string (explicit key ID selection, takes priority over name)
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
	BifrostContextKeyStreamIdleTimeout                   BifrostContextKey = "bifrost-stream-idle-timeout"          // time.Duration (per-chunk idle timeout for streaming)
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
	BifrostContextKeyHasEmittedMessageDelta              BifrostContextKey = "bifrost-has-emitted-message-delta"                 // bool (tracks whether message_delta was already emitted during streaming - avoids duplicates)
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
	BifrostContextKeyValidateKeys                        BifrostContextKey = "bifrost-validate-keys"                      // bool (triggers additional key validation during provider add/update)
	BifrostContextKeyProviderResponseHeaders             BifrostContextKey = "bifrost-provider-response-headers"          // map[string]string (set by provider handlers for response header forwarding)
	BifrostContextKeyLargePayloadMode                    BifrostContextKey = "bifrost-large-payload-mode"                 // bool (set by bifrost - DO NOT SET THIS MANUALLY)) indicates large payload streaming mode is active
	BifrostContextKeyLargePayloadReader                  BifrostContextKey = "bifrost-large-payload-reader"               // io.Reader (set by bifrost - DO NOT SET THIS MANUALLY)) upstream reader for large payloads
	BifrostContextKeyLargePayloadContentLength           BifrostContextKey = "bifrost-large-payload-content-length"       // int (set by bifrost - DO NOT SET THIS MANUALLY)) content length for large payloads
	BifrostContextKeyLargePayloadContentType             BifrostContextKey = "bifrost-large-payload-content-type"         // string (set by enterprise - DO NOT SET THIS MANUALLY)) original content type for large payload passthrough
	BifrostContextKeyLargePayloadMetadata                BifrostContextKey = "bifrost-large-payload-metadata"             // *LargePayloadMetadata (set by bifrost - DO NOT SET THIS MANUALLY)) routing metadata for large payloads
	BifrostContextKeyLargePayloadRequestThreshold        BifrostContextKey = "bifrost-large-payload-request-threshold"    // int64 (set by enterprise - DO NOT SET THIS MANUALLY)) request threshold used by transport heuristics
	BifrostContextKeyLargeResponseMode                   BifrostContextKey = "bifrost-large-response-mode"                // bool (set by bifrost - DO NOT SET THIS MANUALLY)) indicates large response streaming mode is active
	BifrostContextKeyLargePayloadRequestPreview          BifrostContextKey = "bifrost-large-payload-request-preview"      // string (set by bifrost - DO NOT SET THIS MANUALLY)) truncated request body preview for logging
	BifrostContextKeyLargePayloadResponsePreview         BifrostContextKey = "bifrost-large-payload-response-preview"     // string (set by bifrost - DO NOT SET THIS MANUALLY)) truncated response body preview for logging
	BifrostContextKeyLargeResponseReader                 BifrostContextKey = "bifrost-large-response-reader"              // io.ReadCloser (set by bifrost - DO NOT SET THIS MANUALLY)) upstream reader for large responses
	BifrostContextKeyLargeResponseContentLength          BifrostContextKey = "bifrost-large-response-content-length"      // int (set by bifrost - DO NOT SET THIS MANUALLY)) content length for large responses
	BifrostContextKeyLargeResponseContentType            BifrostContextKey = "bifrost-large-response-content-type"        // string (set by bifrost - DO NOT SET THIS MANUALLY)) upstream content type for large responses
	BifrostContextKeyLargeResponseContentDisposition     BifrostContextKey = "bifrost-large-response-content-disposition" // string (set by bifrost - DO NOT SET THIS MANUALLY)) downstream content disposition for large responses
	BifrostContextKeyLargeResponseThreshold              BifrostContextKey = "bifrost-large-response-threshold"           // int64 (set by enterprise - DO NOT SET THIS MANUALLY)) threshold for response streaming
	BifrostContextKeyLargePayloadPrefetchSize            BifrostContextKey = "bifrost-large-payload-prefetch-size"        // int (set by enterprise - DO NOT SET THIS MANUALLY)) prefetch buffer size for metadata extraction from large responses
	BifrostContextKeyDeferredUsage                       BifrostContextKey = "bifrost-deferred-usage"                     // chan *BifrostLLMUsage (set by provider Phase B — delivers usage after response streaming completes)
	BifrostContextKeyDeferredLargePayloadMetadata        BifrostContextKey = "bifrost-deferred-large-payload-metadata"    // <-chan *LargePayloadMetadata (set by enterprise Phase B request — delivers metadata after body streaming)
	BifrostContextKeySSEReaderFactory                    BifrostContextKey = "bifrost-sse-reader-factory"                 // *providerUtils.SSEReaderFactory (set by enterprise — replaces default bufio.Scanner SSE readers with streaming readers)
	BifrostContextKeySessionID                           BifrostContextKey = "bifrost-session-id"                         // string session ID for the request (session stickiness)
	BifrostContextKeySessionTTL                          BifrostContextKey = "bifrost-session-ttl"                        // time.Duration session TTL for the request (session stickiness)
	// BifrostContextKeyProviderOverride is used internally by Bifrost to pass per-request
	// credential and URL overrides from BifrostRequest to the provider layer.
	// Plugin developers should use req.UpdateAPIKey / req.UpdateProviderBaseURL instead.
	BifrostContextKeyProviderOverride BifrostContextKey = "bifrost-provider-override"
)

const (
	// DefaultLargePayloadRequestThresholdBytes is the default request-size heuristic
	// used by transport guards when no enterprise threshold is present on context.
	DefaultLargePayloadRequestThresholdBytes = 10 * 1024 * 1024 // 10MB
)

// ProviderOverride carries per-request credential, URL, and dialect overrides injected by
// plugin hooks. It is used internally by Bifrost; plugin developers should not construct it
// directly. Use req.Update* methods instead.
type ProviderOverride struct {
	// Key is the API credential for this request. If nil, key-pool selection applies normally.
	// Excluded from JSON serialization to prevent accidental credential exposure.
	Key *Key `json:"-"`
	// BaseURL overrides NetworkConfig.BaseURL for this request.
	// Provide the scheme, host, and port only -- no path prefix.
	// Bifrost appends the provider's full default path (e.g. "/v1/chat/completions").
	// Example: "https://eu.api.openai.com" (not "https://eu.api.openai.com/v1").
	// Use RequestPathOverrides in CustomProviderConfig to change the path suffix.
	// Honoured only by providers that use GetRequestPath internally (OpenAI, Anthropic,
	// Cohere, HuggingFace, Replicate). Other providers construct their URLs directly.
	BaseURL string `json:"base_url,omitempty"`
	// BaseProviderType specifies the API dialect to use when the provider name set via
	// UpdateProvider is not a built-in provider (e.g. "acme-openai").
	// Must be a supported built-in provider (e.g. schemas.OpenAI, schemas.Anthropic).
	// Bifrost routes the request through the base type's queue, so no separate static
	// config entry is needed for the alias. Per-request credentials and URL are
	// supplied via Key and BaseURL.
	BaseProviderType ModelProvider `json:"base_provider_type,omitempty"`
}

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

// LargePayloadMetadata holds routing-relevant metadata selectively extracted from large payloads.
// This is used when the full request body is too large to parse (e.g., 400MB video upload).
// Only small routing/observability fields are extracted; the body itself streams through unchanged.
type LargePayloadMetadata struct {
	ResponseModalities []string // e.g., ["AUDIO"] for speech, ["IMAGE"] for image generation
	SpeechConfig       bool     // true if generationConfig.speechConfig is present
	Model              string   // model extracted without full body parsing (openai/anthropic multipart/json)
	StreamRequested    *bool    // stream flag when available in request payload metadata
}

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
	BatchDeleteRequest           *BifrostBatchDeleteRequest
	ContainerCreateRequest       *BifrostContainerCreateRequest
	ContainerListRequest         *BifrostContainerListRequest
	ContainerRetrieveRequest     *BifrostContainerRetrieveRequest
	ContainerDeleteRequest       *BifrostContainerDeleteRequest
	ContainerFileCreateRequest   *BifrostContainerFileCreateRequest
	ContainerFileListRequest     *BifrostContainerFileListRequest
	ContainerFileRetrieveRequest *BifrostContainerFileRetrieveRequest
	ContainerFileContentRequest  *BifrostContainerFileContentRequest
	ContainerFileDeleteRequest   *BifrostContainerFileDeleteRequest
	PassthroughRequest           *BifrostPassthroughRequest

	// ProviderOverride holds per-request credential and URL overrides set via
	// UpdateAPIKey and UpdateProviderBaseURL. It is read by Bifrost internally and
	// should not be set directly; use the Update* methods instead.
	// Excluded from JSON to prevent accidental serialization of override metadata.
	ProviderOverride *ProviderOverride `json:"-"`
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
	case br.BatchDeleteRequest != nil:
		if br.BatchDeleteRequest.Model != nil {
			return br.BatchDeleteRequest.Provider, *br.BatchDeleteRequest.Model, nil
		}
		return br.BatchDeleteRequest.Provider, "", nil
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
	case br.PassthroughRequest != nil:
		return br.PassthroughRequest.Provider, br.PassthroughRequest.Model, nil
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
	case br.FileUploadRequest != nil:
		br.FileUploadRequest.Provider = provider
	case br.FileListRequest != nil:
		br.FileListRequest.Provider = provider
	case br.FileRetrieveRequest != nil:
		br.FileRetrieveRequest.Provider = provider
	case br.FileDeleteRequest != nil:
		br.FileDeleteRequest.Provider = provider
	case br.FileContentRequest != nil:
		br.FileContentRequest.Provider = provider
	case br.BatchCreateRequest != nil:
		br.BatchCreateRequest.Provider = provider
	case br.BatchListRequest != nil:
		br.BatchListRequest.Provider = provider
	case br.BatchRetrieveRequest != nil:
		br.BatchRetrieveRequest.Provider = provider
	case br.BatchCancelRequest != nil:
		br.BatchCancelRequest.Provider = provider
	case br.BatchResultsRequest != nil:
		br.BatchResultsRequest.Provider = provider
	case br.BatchDeleteRequest != nil:
		br.BatchDeleteRequest.Provider = provider
	case br.ContainerCreateRequest != nil:
		br.ContainerCreateRequest.Provider = provider
	case br.ContainerListRequest != nil:
		br.ContainerListRequest.Provider = provider
	case br.ContainerRetrieveRequest != nil:
		br.ContainerRetrieveRequest.Provider = provider
	case br.ContainerDeleteRequest != nil:
		br.ContainerDeleteRequest.Provider = provider
	case br.ContainerFileCreateRequest != nil:
		br.ContainerFileCreateRequest.Provider = provider
	case br.ContainerFileListRequest != nil:
		br.ContainerFileListRequest.Provider = provider
	case br.ContainerFileRetrieveRequest != nil:
		br.ContainerFileRetrieveRequest.Provider = provider
	case br.ContainerFileContentRequest != nil:
		br.ContainerFileContentRequest.Provider = provider
	case br.ContainerFileDeleteRequest != nil:
		br.ContainerFileDeleteRequest.Provider = provider
	case br.PassthroughRequest != nil:
		br.PassthroughRequest.Provider = provider
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
	case br.FileUploadRequest != nil:
		br.FileUploadRequest.Model = &model
	case br.FileListRequest != nil:
		br.FileListRequest.Model = &model
	case br.FileRetrieveRequest != nil:
		br.FileRetrieveRequest.Model = &model
	case br.FileDeleteRequest != nil:
		br.FileDeleteRequest.Model = &model
	case br.FileContentRequest != nil:
		br.FileContentRequest.Model = &model
	case br.BatchCreateRequest != nil:
		br.BatchCreateRequest.Model = &model
	case br.BatchListRequest != nil:
		br.BatchListRequest.Model = &model
	case br.BatchRetrieveRequest != nil:
		br.BatchRetrieveRequest.Model = &model
	case br.BatchCancelRequest != nil:
		br.BatchCancelRequest.Model = &model
	case br.BatchResultsRequest != nil:
		br.BatchResultsRequest.Model = &model
	case br.BatchDeleteRequest != nil:
		br.BatchDeleteRequest.Model = &model
	case br.PassthroughRequest != nil:
		br.PassthroughRequest.Model = model
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

// Clone returns an independent copy of the request. The one active inner request
// field is deep-copied so mutations to scalar fields (Provider, Model) on the clone
// do not affect the original. Read-only content fields such as input messages and
// tool definitions are intentionally shared via slice headers -- Clone is designed
// for use in prepareFallbackRequest, which only writes Provider and Model, never
// the content. ProviderOverride is also deep-copied so the caller can nil or replace
// it without aliasing the original.
// Clone returns a copy of the request suitable for use in prepareFallbackRequest.
//
// What IS independently copied:
//   - ProviderOverride struct (so niling it on the clone does not affect the original)
//   - The one active inner request struct (so SetProvider/UpdateModel writes to the
//     clone's Provider/Model scalars without aliasing the original's inner pointer)
//
// What is NOT deep-copied, and why:
//   - Slice and map fields inside the inner request struct (e.g. Input []ChatMessage,
//     Tools []Tool, Fallbacks []Fallback, ExtraParams map[string]interface{}).
//     These share the same underlying array/map with the original.
//     This is intentional: Clone() exists only to let prepareFallbackRequest write
//     routing metadata (Provider, Model) independently per fallback attempt.
//     Plugin hooks MUST NOT mutate content fields (messages, tools, metadata);
//     they may only rewrite routing metadata through the Update* methods.
//     Deep-copying content across all 35 request types would add significant
//     complexity and per-fallback heap allocation for no practical benefit.
//   - ProviderOverride.Key (pointer): safe to share because prepareFallbackRequest
//     immediately nils ProviderOverride after Clone(), and hooks create a fresh one
//     via req.UpdateAPIKey if needed.
func (br *BifrostRequest) Clone() BifrostRequest {
	if br == nil {
		return BifrostRequest{}
	}
	clone := *br
	if br.ProviderOverride != nil {
		ovr := *br.ProviderOverride // copies BaseURL and BaseProviderType by value
		clone.ProviderOverride = &ovr
	}
	switch {
	case br.ListModelsRequest != nil:
		tmp := *br.ListModelsRequest
		clone.ListModelsRequest = &tmp
	case br.TextCompletionRequest != nil:
		tmp := *br.TextCompletionRequest
		clone.TextCompletionRequest = &tmp
	case br.ChatRequest != nil:
		tmp := *br.ChatRequest
		clone.ChatRequest = &tmp
	case br.ResponsesRequest != nil:
		tmp := *br.ResponsesRequest
		clone.ResponsesRequest = &tmp
	case br.CountTokensRequest != nil:
		tmp := *br.CountTokensRequest
		clone.CountTokensRequest = &tmp
	case br.EmbeddingRequest != nil:
		tmp := *br.EmbeddingRequest
		clone.EmbeddingRequest = &tmp
	case br.RerankRequest != nil:
		tmp := *br.RerankRequest
		clone.RerankRequest = &tmp
	case br.SpeechRequest != nil:
		tmp := *br.SpeechRequest
		clone.SpeechRequest = &tmp
	case br.TranscriptionRequest != nil:
		tmp := *br.TranscriptionRequest
		clone.TranscriptionRequest = &tmp
	case br.ImageGenerationRequest != nil:
		tmp := *br.ImageGenerationRequest
		clone.ImageGenerationRequest = &tmp
	case br.ImageEditRequest != nil:
		tmp := *br.ImageEditRequest
		clone.ImageEditRequest = &tmp
	case br.ImageVariationRequest != nil:
		tmp := *br.ImageVariationRequest
		clone.ImageVariationRequest = &tmp
	case br.VideoGenerationRequest != nil:
		tmp := *br.VideoGenerationRequest
		clone.VideoGenerationRequest = &tmp
	case br.VideoRetrieveRequest != nil:
		tmp := *br.VideoRetrieveRequest
		clone.VideoRetrieveRequest = &tmp
	case br.VideoDownloadRequest != nil:
		tmp := *br.VideoDownloadRequest
		clone.VideoDownloadRequest = &tmp
	case br.VideoListRequest != nil:
		tmp := *br.VideoListRequest
		clone.VideoListRequest = &tmp
	case br.VideoDeleteRequest != nil:
		tmp := *br.VideoDeleteRequest
		clone.VideoDeleteRequest = &tmp
	case br.VideoRemixRequest != nil:
		tmp := *br.VideoRemixRequest
		clone.VideoRemixRequest = &tmp
	case br.FileUploadRequest != nil:
		tmp := *br.FileUploadRequest
		clone.FileUploadRequest = &tmp
	case br.FileListRequest != nil:
		tmp := *br.FileListRequest
		clone.FileListRequest = &tmp
	case br.FileRetrieveRequest != nil:
		tmp := *br.FileRetrieveRequest
		clone.FileRetrieveRequest = &tmp
	case br.FileDeleteRequest != nil:
		tmp := *br.FileDeleteRequest
		clone.FileDeleteRequest = &tmp
	case br.FileContentRequest != nil:
		tmp := *br.FileContentRequest
		clone.FileContentRequest = &tmp
	case br.BatchCreateRequest != nil:
		tmp := *br.BatchCreateRequest
		clone.BatchCreateRequest = &tmp
	case br.BatchListRequest != nil:
		tmp := *br.BatchListRequest
		clone.BatchListRequest = &tmp
	case br.BatchRetrieveRequest != nil:
		tmp := *br.BatchRetrieveRequest
		clone.BatchRetrieveRequest = &tmp
	case br.BatchCancelRequest != nil:
		tmp := *br.BatchCancelRequest
		clone.BatchCancelRequest = &tmp
	case br.BatchResultsRequest != nil:
		tmp := *br.BatchResultsRequest
		clone.BatchResultsRequest = &tmp
	case br.BatchDeleteRequest != nil:
		tmp := *br.BatchDeleteRequest
		clone.BatchDeleteRequest = &tmp
	case br.ContainerCreateRequest != nil:
		tmp := *br.ContainerCreateRequest
		clone.ContainerCreateRequest = &tmp
	case br.ContainerListRequest != nil:
		tmp := *br.ContainerListRequest
		clone.ContainerListRequest = &tmp
	case br.ContainerRetrieveRequest != nil:
		tmp := *br.ContainerRetrieveRequest
		clone.ContainerRetrieveRequest = &tmp
	case br.ContainerDeleteRequest != nil:
		tmp := *br.ContainerDeleteRequest
		clone.ContainerDeleteRequest = &tmp
	case br.ContainerFileCreateRequest != nil:
		tmp := *br.ContainerFileCreateRequest
		clone.ContainerFileCreateRequest = &tmp
	case br.ContainerFileListRequest != nil:
		tmp := *br.ContainerFileListRequest
		clone.ContainerFileListRequest = &tmp
	case br.ContainerFileRetrieveRequest != nil:
		tmp := *br.ContainerFileRetrieveRequest
		clone.ContainerFileRetrieveRequest = &tmp
	case br.ContainerFileContentRequest != nil:
		tmp := *br.ContainerFileContentRequest
		clone.ContainerFileContentRequest = &tmp
	case br.ContainerFileDeleteRequest != nil:
		tmp := *br.ContainerFileDeleteRequest
		clone.ContainerFileDeleteRequest = &tmp
	case br.PassthroughRequest != nil:
		tmp := *br.PassthroughRequest
		clone.PassthroughRequest = &tmp
	}
	return clone
}

// UpdateProvider sets the provider for this request. It is equivalent to changing the
// Provider field on the underlying request type but works regardless of request type.
// If provider is empty, the request's existing provider is used unchanged.
// For non-built-in names, pair with UpdateBaseProviderType to specify the API dialect;
// built-in providers (e.g. OpenAI, Gemini) auto-initialise without it.
func (br *BifrostRequest) UpdateProvider(provider ModelProvider) error {
	if br == nil {
		return errors.New("bifrost request is nil")
	}
	if provider == "" {
		return nil
	}
	br.SetProvider(provider)
	return nil
}

// UpdateModel sets the model for this request. It is equivalent to changing the Model
// field on the underlying request type but works regardless of request type.
func (br *BifrostRequest) UpdateModel(model string) error {
	if br == nil {
		return errors.New("bifrost request is nil")
	}
	if model == "" {
		return nil
	}
	br.SetModel(model)
	return nil
}

// UpdateAPIKey sets a per-request API credential, bypassing the key pool for this request.
// This is the recommended way for plugins to inject dynamic credentials at request time.
func (br *BifrostRequest) UpdateAPIKey(key Key) {
	if br == nil {
		return
	}
	if br.ProviderOverride == nil {
		br.ProviderOverride = &ProviderOverride{}
	}
	br.ProviderOverride.Key = &key
}

// UpdateProviderBaseURL overrides the provider's base URL for this request, enabling
// routing to geo-specific or tenant-specific endpoints (e.g. "https://eu.api.openai.com").
// The URL should be the scheme + host only, without a trailing slash or path prefix.
//
// Note: BaseURL override is honoured only by providers that resolve their request URL
// via GetRequestPath (OpenAI, Anthropic, Cohere, HuggingFace, Replicate). Providers that
// construct their URLs directly (Gemini, Groq, Mistral, etc.) ignore this field; for those,
// set a full URL on the BifrostContext before calling the API method:
//
//	ctx.SetValue(schemas.BifrostContextKeyURLPath, "https://custom.host.com/v1/chat")
//
// Note: BifrostContextKeyURLPath is a restricted key and cannot be set inside a PreLLMHook
// via ctx.SetValue. Set it on the request context before invoking the Bifrost API method.
func (br *BifrostRequest) UpdateProviderBaseURL(baseURL string) {
	if br == nil {
		return
	}
	if br.ProviderOverride == nil {
		br.ProviderOverride = &ProviderOverride{}
	}
	br.ProviderOverride.BaseURL = baseURL
}

// UpdateBaseProviderType sets the API dialect for this request when the provider name
// (set via UpdateProvider) is not a built-in provider name. Bifrost routes the request
// through the base type's existing queue (e.g. schemas.OpenAI for any OpenAI-compatible
// endpoint), so no separate static config entry is needed for the alias.
func (br *BifrostRequest) UpdateBaseProviderType(bt ModelProvider) {
	if br == nil {
		return
	}
	if bt == "" {
		return
	}
	if br.ProviderOverride == nil {
		br.ProviderOverride = &ProviderOverride{}
	}
	br.ProviderOverride.BaseProviderType = bt
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
	BatchDeleteResponse           *BifrostBatchDeleteResponse
	ContainerCreateResponse       *BifrostContainerCreateResponse
	ContainerListResponse         *BifrostContainerListResponse
	ContainerRetrieveResponse     *BifrostContainerRetrieveResponse
	ContainerDeleteResponse       *BifrostContainerDeleteResponse
	ContainerFileCreateResponse   *BifrostContainerFileCreateResponse
	ContainerFileListResponse     *BifrostContainerFileListResponse
	ContainerFileRetrieveResponse *BifrostContainerFileRetrieveResponse
	ContainerFileContentResponse  *BifrostContainerFileContentResponse
	ContainerFileDeleteResponse   *BifrostContainerFileDeleteResponse
	PassthroughResponse           *BifrostPassthroughResponse
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
	case r.BatchDeleteResponse != nil:
		return &r.BatchDeleteResponse.ExtraFields
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
	case r.PassthroughResponse != nil:
		return &r.PassthroughResponse.ExtraFields
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
	*BifrostPassthroughResponse
	*BifrostError
}

// MarshalJSON implements custom JSON marshaling for BifrostStreamChunk.
// This ensures that only the non-nil embedded struct is marshaled,
func (bs BifrostStreamChunk) MarshalJSON() ([]byte, error) {
	if bs.BifrostTextCompletionResponse != nil {
		return MarshalSorted(bs.BifrostTextCompletionResponse)
	} else if bs.BifrostChatResponse != nil {
		return MarshalSorted(bs.BifrostChatResponse)
	} else if bs.BifrostResponsesStreamResponse != nil {
		return MarshalSorted(bs.BifrostResponsesStreamResponse)
	} else if bs.BifrostSpeechStreamResponse != nil {
		return MarshalSorted(bs.BifrostSpeechStreamResponse)
	} else if bs.BifrostTranscriptionStreamResponse != nil {
		return MarshalSorted(bs.BifrostTranscriptionStreamResponse)
	} else if bs.BifrostImageGenerationStreamResponse != nil {
		return MarshalSorted(bs.BifrostImageGenerationStreamResponse)
	} else if bs.BifrostPassthroughResponse != nil {
		return MarshalSorted(bs.BifrostPassthroughResponse)
	} else if bs.BifrostError != nil {
		return MarshalSorted(bs.BifrostError)
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
