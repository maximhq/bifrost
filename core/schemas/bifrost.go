// Package schemas defines the core schemas and types used by the Bifrost system.
package schemas

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
)

const (
	DefaultInitialPoolSize = 5000
)

type KeySelector func(ctx *BifrostContext, keys []Key, providerKey ModelProvider, model string) (Key, error)

// KeyPoolFilter is an optional hook called before key selection to veto keys
// from the available pool.
type KeyPoolFilter func(ctx *BifrostContext, provider ModelProvider, model string, keys []Key) ([]Key, error)

// BifrostConfig represents the configuration for initializing a Bifrost instance.
// It contains the necessary components for setting up the system including account details,
// plugins, logging, and initial pool size.
type BifrostConfig struct {
	Account            Account
	LLMPlugins         []LLMPlugin
	MCPPlugins         []MCPPlugin
	OAuth2Provider     OAuth2Provider
	MCPHeadersProvider MCPHeadersProvider // Backend for MCPAuthTypePerUserHeaders credential storage; nil disables per-user-headers auth (resolver errors at use)
	Logger             Logger
	Tracer             Tracer        // Tracer for distributed tracing (nil = NoOpTracer)
	InitialPoolSize    int           // Initial pool size for sync pools in Bifrost. Higher values will reduce memory allocations but will increase memory usage.
	DropExcessRequests bool          // If true, in cases where the queue is full, requests will not wait for the queue to be empty and will be dropped instead.
	MCPConfig          *MCPConfig    // MCP (Model Context Protocol) configuration for tool integration
	KeySelector        KeySelector   // Custom key selector function
	KeyPoolFilter      KeyPoolFilter // Optional hook to filter available keys before selection; nil = all keys eligible
	KVStore            KVStore       // shared KV store for clustering/session stickiness; nil = disabled
}

// ModelProvider represents the different AI model providers supported by Bifrost.
type ModelProvider string

const (
	OpenAI        ModelProvider = "openai"
	Azure         ModelProvider = "azure"
	Anthropic     ModelProvider = "anthropic"
	Bedrock       ModelProvider = "bedrock"
	BedrockMantle ModelProvider = "bedrock_mantle"
	Cohere        ModelProvider = "cohere"
	Vertex        ModelProvider = "vertex"
	Mistral       ModelProvider = "mistral"
	Ollama        ModelProvider = "ollama"
	OpencodeGo    ModelProvider = "opencode-go"
	OpencodeZen   ModelProvider = "opencode-zen"
	Groq          ModelProvider = "groq"
	SGL           ModelProvider = "sgl"
	Parasail      ModelProvider = "parasail"
	Perplexity    ModelProvider = "perplexity"
	Cerebras      ModelProvider = "cerebras"
	DeepSeek      ModelProvider = "deepseek"
	Gemini        ModelProvider = "gemini"
	OpenRouter    ModelProvider = "openrouter"
	Elevenlabs    ModelProvider = "elevenlabs"
	HuggingFace   ModelProvider = "huggingface"
	Nebius        ModelProvider = "nebius"
	XAI           ModelProvider = "xai"
	Replicate     ModelProvider = "replicate"
	VLLM          ModelProvider = "vllm"
	Runway        ModelProvider = "runway"
	Runware       ModelProvider = "runware"
	Fireworks     ModelProvider = "fireworks"
	Sarvam        ModelProvider = "sarvam"
	Wafer         ModelProvider = "wafer"
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
	BedrockMantle,
	Cerebras,
	Cohere,
	DeepSeek,
	Gemini,
	Groq,
	Mistral,
	Ollama,
	OpencodeGo,
	OpencodeZen,
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
	Runware,
	Fireworks,
	Sarvam,
	Wafer,
}

// RequestType represents the type of request being made to a provider.
type RequestType string

// Value implements driver.Valuer so database drivers that append typed
// column values (e.g. clickhouse-go batch inserts) can serialize the type.
func (r RequestType) Value() (driver.Value, error) {
	return string(r), nil
}

const (
	ListModelsRequest            RequestType = "list_models"
	TextCompletionRequest        RequestType = "text_completion"
	TextCompletionStreamRequest  RequestType = "text_completion_stream"
	ChatCompletionRequest        RequestType = "chat_completion"
	ChatCompletionStreamRequest  RequestType = "chat_completion_stream"
	ResponsesRequest             RequestType = "responses"
	ResponsesStreamRequest       RequestType = "responses_stream"
	ResponsesRetrieveRequest     RequestType = "responses_retrieve"
	ResponsesDeleteRequest       RequestType = "responses_delete"
	ResponsesCancelRequest       RequestType = "responses_cancel"
	ResponsesInputItemsRequest   RequestType = "responses_input_items"
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
	CachedContentCreateRequest   RequestType = "cached_content_create"
	CachedContentListRequest     RequestType = "cached_content_list"
	CachedContentRetrieveRequest RequestType = "cached_content_retrieve"
	CachedContentUpdateRequest   RequestType = "cached_content_update"
	CachedContentDeleteRequest   RequestType = "cached_content_delete"
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
	OCRRequest                   RequestType = "ocr"
	CountTokensRequest           RequestType = "count_tokens"
	CompactionRequest            RequestType = "compaction"
	MCPToolExecutionRequest      RequestType = "mcp_tool_execution"
	PassthroughRequest           RequestType = "passthrough"
	PassthroughStreamRequest     RequestType = "passthrough_stream"
	UnknownRequest               RequestType = "unknown"
	WebSocketResponsesRequest    RequestType = "websocket_responses"
	RealtimeRequest              RequestType = "realtime"
)

// BifrostContextKey is a type for context keys used in Bifrost.
type BifrostContextKey string

// MCPAuthMode describes which identity dimension a per-user OAuth row is keyed by.
// It is a derived view of context state at the point of token lookup, never
// stored as a context key. Derived via BifrostContext.MCPAuthMode().
type MCPAuthMode string

const (
	// MCPAuthModeUser: identity is a user id populated by an upstream auth
	// middleware or plugin. Token rows keyed by user_id.
	MCPAuthModeUser MCPAuthMode = "user"
	// MCPAuthModeVK: identity is a virtual key. Token rows keyed by vk_id.
	MCPAuthModeVK MCPAuthMode = "vk"
	// MCPAuthModeSession: identity is a client-issued opaque session ID, asserted
	// via the x-bf-mcp-session-id header. Token rows keyed by session_id.
	// Used when there's no VK or user; the caller owns the session ID and must
	// present it on every subsequent request to use the bound OAuth token.
	MCPAuthModeSession MCPAuthMode = "session"
	// MCPAuthModeNone: no identity dimension is present on the request (no
	// user, no VK, no session header). Lets callers branch on the mode
	// without mistaking an unauthenticated request for a session-mode caller.
	MCPAuthModeNone MCPAuthMode = "none"
)

// BifrostContextKeyRequestType is a context key for the request type.
const (
	BifrostContextKeySessionToken      BifrostContextKey = "bifrost-session-token" // string (session token for authentication - set by auth middleware)
	BifrostContextKeyVirtualKey        BifrostContextKey = "x-bf-vk"               // string
	BifrostContextKeyAPIKeyName        BifrostContextKey = "x-bf-api-key"          // string (explicit key name selection)
	BifrostContextKeyAPIKeyID          BifrostContextKey = "x-bf-api-key-id"       // string (explicit key ID selection, takes priority over name)
	BifrostContextKeyDirectKey         BifrostContextKey = "x-bf-direct-key"       // schemas.Key (raw key supplied via x-bf-direct-key: true header; bypasses registered key pool)
	BifrostContextKeyRequestID         BifrostContextKey = "request-id"            // string
	BifrostContextKeyFallbackRequestID BifrostContextKey = "fallback-request-id"   // string

	// NOTE: []string is used for both keys, and by default all clients/tools are included (when nil).
	// If "*" is present, all clients/tools are included, and [] means no clients/tools are included.
	// Request context filtering takes priority over client config - context can override client exclusions.
	MCPContextKeyIncludeClients BifrostContextKey = "mcp-include-clients" // Context key for whitelist client filtering
	MCPContextKeyIncludeTools   BifrostContextKey = "mcp-include-tools"   // Context key for whitelist tool filtering (Note: toolName should be in "clientName-toolName" format for individual tools, or "clientName-*" for wildcard)

	BifrostContextKeySelectedKeyID                       BifrostContextKey = "bifrost-selected-key-id"                // string (to store the selected key ID (set by bifrost governance plugin - DO NOT SET THIS MANUALLY))
	BifrostContextKeySelectedKeyName                     BifrostContextKey = "bifrost-selected-key-name"              // string (to store the selected key name (set by bifrost governance plugin - DO NOT SET THIS MANUALLY))
	BifrostContextKeyGovernanceVirtualKeyID              BifrostContextKey = "bifrost-governance-virtual-key-id"      // string (to store the virtual key ID (set by bifrost governance plugin - DO NOT SET THIS MANUALLY))
	BifrostContextKeyGovernanceVirtualKeyName            BifrostContextKey = "bifrost-governance-virtual-key-name"    // string (to store the virtual key name (set by bifrost governance plugin - DO NOT SET THIS MANUALLY))
	BifrostContextKeyGovernanceTeamID                    BifrostContextKey = "bifrost-governance-team-id"             // string (to store the team ID (set by bifrost governance plugin - DO NOT SET THIS MANUALLY))
	BifrostContextKeyGovernanceTeamName                  BifrostContextKey = "bifrost-governance-team-name"           // string (to store the team name (set by bifrost governance plugin - DO NOT SET THIS MANUALLY))
	BifrostContextKeyGovernanceCustomerID                BifrostContextKey = "bifrost-governance-customer-id"         // string (to store the customer ID (set by bifrost governance plugin - DO NOT SET THIS MANUALLY))
	BifrostContextKeyGovernanceCustomerName              BifrostContextKey = "bifrost-governance-customer-name"       // string (to store the customer name (set by bifrost governance plugin - DO NOT SET THIS MANUALLY))
	BifrostContextKeyGovernanceBusinessUnitID            BifrostContextKey = "bifrost-governance-business-unit-id"    // string (to store the business unit ID (set by enterprise governance plugin - DO NOT SET THIS MANUALLY))
	BifrostContextKeyGovernanceBusinessUnitName          BifrostContextKey = "bifrost-governance-business-unit-name"  // string (to store the business unit name (set by enterprise governance plugin - DO NOT SET THIS MANUALLY))
	BifrostContextKeyGovernanceTeamIDs                   BifrostContextKey = "bifrost-governance-team-ids"            // []string (all teams a user/AP request belongs to; set by enterprise governance plugin - DO NOT SET THIS MANUALLY)
	BifrostContextKeyGovernanceTeamNames                 BifrostContextKey = "bifrost-governance-team-names"          // []string (display names, aligned with team-ids; set by enterprise governance plugin - DO NOT SET THIS MANUALLY)
	BifrostContextKeyGovernanceBusinessUnitIDs           BifrostContextKey = "bifrost-governance-business-unit-ids"   // []string (distinct BUs across the user's teams; set by enterprise governance plugin - DO NOT SET THIS MANUALLY)
	BifrostContextKeyGovernanceBusinessUnitNames         BifrostContextKey = "bifrost-governance-business-unit-names" // []string (display names, aligned with business-unit-ids; set by enterprise governance plugin - DO NOT SET THIS MANUALLY)
	BifrostContextKeyGovernanceCustomerIDs               BifrostContextKey = "bifrost-governance-customer-ids"        // []string (distinct customers a user/team request belongs to; set by enterprise governance plugin - DO NOT SET THIS MANUALLY)
	BifrostContextKeyGovernanceCustomerNames             BifrostContextKey = "bifrost-governance-customer-names"      // []string (display names, aligned with customer-ids; set by enterprise governance plugin - DO NOT SET THIS MANUALLY)
	BifrostContextKeyGovernanceScopedCustomerID          BifrostContextKey = "bifrost-governance-scoped-customer-id"  // string (resolved customer the request is scoped to via the x-bf-customer-id / x-bf-customer-name header on a team-VK path; set by the enterprise governance plugin - DO NOT SET THIS MANUALLY)
	BifrostContextKeyGovernanceRoutingRuleID             BifrostContextKey = "bifrost-governance-routing-rule-id"     // string (to store the routing rule ID (set by bifrost governance plugin - DO NOT SET THIS MANUALLY))
	BifrostContextKeyGovernanceRoutingRuleName           BifrostContextKey = "bifrost-governance-routing-rule-name"   // string (to store the routing rule name (set by bifrost governance plugin - DO NOT SET THIS MANUALLY))
	BifrostContextKeyRoutingPinnedAPIKeyID               BifrostContextKey = "bifrost-routing-pinned-api-key-id"      // string (provider key ID pinned by a matched routing rule target; resolved against the configured key pool during key selection and takes precedence over a caller-supplied pin (set by bifrost governance plugin - DO NOT SET THIS MANUALLY))
	BifrostContextKeySelectedPromptName                  BifrostContextKey = "bifrost-selected-prompt-name"           // string (display name of the selected prompt (set by prompts plugin - DO NOT SET THIS MANUALLY))
	BifrostContextKeySelectedPromptVersion               BifrostContextKey = "bifrost-selected-prompt-version"        // string (numeric version as string, e.g. "3" (set by prompts plugin - DO NOT SET THIS MANUALLY))
	BifrostContextKeySelectedPromptID                    BifrostContextKey = "bifrost-selected-prompt-id"             // string (id of the selected prompt (set by prompts plugin - DO NOT SET THIS MANUALLY))
	BifrostContextKeyGovernanceIncludeOnlyKeys           BifrostContextKey = "bf-governance-include-only-keys"        // []string (to store the include-only key IDs for provider config routing (set by bifrost governance plugin - DO NOT SET THIS MANUALLY))
	BifrostContextKeyNumberOfRetries                     BifrostContextKey = "bifrost-number-of-retries"              // int (to store the number of retries (set by bifrost - DO NOT SET THIS MANUALLY))
	BifrostContextKeyFallbackIndex                       BifrostContextKey = "bifrost-fallback-index"                 // int (to store the fallback index (set by bifrost - DO NOT SET THIS MANUALLY)) 0 for primary, 1 for first fallback, etc.
	BifrostContextKeyResolvedAlias                       BifrostContextKey = "bifrost-resolved-alias"                 // *ResolvedAlias (set by bifrost after key-level alias resolution — providers read this for model_family routing and provider-specific overrides; nil/absent when no alias matched)
	BifrostContextKeyStreamEndIndicator                  BifrostContextKey = "bifrost-stream-end-indicator"           // bool (set by bifrost - DO NOT SET THIS MANUALLY)
	BifrostContextKeyStreamGated                         BifrostContextKey = "bifrost-stream-gated"                   // bool (set by ctx.PauseStream/ResumeStream/EndStream when a plugin first engages the pause/resume gate; provider helpers use this as a fast-path check to skip Tracer.GateSend on streams that never engage the gate)
	BifrostContextKeyStreamIdleTimeout                   BifrostContextKey = "bifrost-stream-idle-timeout"            // time.Duration (per-chunk idle timeout for streaming)
	BifrostContextKeySkipKeySelection                    BifrostContextKey = "bifrost-skip-key-selection"             // bool (will pass an empty key to the provider)
	BifrostContextKeyExtraHeaders                        BifrostContextKey = "bifrost-extra-headers"                  // map[string][]string
	BifrostContextKeyURLPath                             BifrostContextKey = "bifrost-extra-url-path"                 // string
	BifrostContextKeyUseRawRequestBody                   BifrostContextKey = "bifrost-use-raw-request-body"
	BifrostContextKeyChangeRequestType                   BifrostContextKey = "bifrost-change-request-type"                      // RequestType (set by plugins to trigger request type conversion in core, e.g. text->chat or chat->responses)
	BifrostContextKeySendBackRawRequest                  BifrostContextKey = "bifrost-send-back-raw-request"                    // bool (per-request override — read by bifrost.go, never overwritten)
	BifrostContextKeySendBackRawResponse                 BifrostContextKey = "bifrost-send-back-raw-response"                   // bool (per-request override — read by bifrost.go, never overwritten)
	BifrostContextKeyIntegrationType                     BifrostContextKey = "bifrost-integration-type"                         // integration used in gateway (e.g. openai, anthropic, bedrock, etc.)
	BifrostContextKeyIsResponsesToChatCompletionFallback BifrostContextKey = "bifrost-is-responses-to-chat-completion-fallback" // bool (set by bifrost - DO NOT SET THIS MANUALLY)
	BifrostMCPAgentOriginalRequestID                     BifrostContextKey = "bifrost-mcp-agent-original-request-id"            // string (to store the original request ID for MCP agent mode)
	BifrostContextKeyParentMCPRequestID                  BifrostContextKey = "bf-parent-mcp-request-id"                         // string (parent request ID for nested tool calls from executeCode)
	BifrostContextKeyStructuredOutputToolName            BifrostContextKey = "bifrost-structured-output-tool-name"              // string (to store the name of the structured output tool (set by bifrost))
	BifrostContextKeyUserAgent                           BifrostContextKey = "bifrost-user-agent"                               // string (set by bifrost)
	BifrostContextKeySkipBudgetAndRateLimits             BifrostContextKey = "bifrost-skip-budget-and-rate-limits"              // bool (set by bifrost for read-only requests like list models that don't consume quota)
	BifrostContextKeySkipVirtualKeyUsageTracking         BifrostContextKey = "bifrost-skip-virtual-key-usage-tracking"          // bool (set by governance callers to skip VK usage while preserving VK auth/attribution)
	BifrostContextKeyTraceID                             BifrostContextKey = "bifrost-trace-id"                                 // string (trace ID for distributed tracing - set by tracing middleware)
	BifrostContextKeySpanID                              BifrostContextKey = "bifrost-span-id"                                  // string (current span ID for child span creation - set by tracer)
	BifrostContextKeyParentSpanID                        BifrostContextKey = "bifrost-parent-span-id"                           // string (parent span ID from W3C traceparent header - set by tracing middleware)
	BifrostContextKeyStreamStartTime                     BifrostContextKey = "bifrost-stream-start-time"                        // time.Time (start time for streaming TTFT calculation - set by bifrost)
	BifrostContextKeyTracer                              BifrostContextKey = "bifrost-tracer"                                   // Tracer (tracer instance for completing deferred spans - set by bifrost)
	BifrostContextKeyDeferTraceCompletion                BifrostContextKey = "bifrost-defer-trace-completion"                   // bool (signals trace completion should be deferred for streaming - set by streaming handlers)
	BifrostContextKeyTraceCompleter                      BifrostContextKey = "bifrost-trace-completer"                          // func([]PluginLogEntry) (callback to complete trace after streaming, receives transport plugin logs - set by tracing middleware)
	BifrostContextKeyAccumulatorID                       BifrostContextKey = "bifrost-accumulator-id"                           // string (ID for streaming accumulator lookup - set by tracer for accumulator operations)
	BifrostContextKeyMCPSessionID                        BifrostContextKey = "bifrost-mcp-session-id"                           // string (session-mode identity: any opaque value asserted by the caller via x-bf-mcp-session-id; binds the OAuth token row to subsequent /mcp calls when no VK or user is present)
	BifrostContextKeyMCPCallbackBaseURL                  BifrostContextKey = "bifrost-mcp-callback-base-url"                    // string (base URL like "https://host" — set by HTTP middleware. OAuth resolver appends /api/oauth/callback; headers resolver appends the workspace submit path. Used for both per-user OAuth and per-user headers auth flows)
	BifrostContextKeyIsMCPGateway                        BifrostContextKey = "bifrost-is-mcp-gateway"                           // bool (true when request is being handled via the MCP gateway path)
	BifrostContextKeyHasEmittedMessageDelta              BifrostContextKey = "bifrost-has-emitted-message-delta"                // bool (tracks whether message_delta was already emitted during streaming - avoids duplicates)
	BifrostContextKeySkipDBUpdate                        BifrostContextKey = "bifrost-skip-db-update"                           // bool (set by bifrost - DO NOT SET THIS MANUALLY)
	BifrostContextKeyGovernancePluginName                BifrostContextKey = "governance-plugin-name"                           // string (name of the governance plugin that processed the request - set by bifrost)
	BifrostContextKeyClusterNodeID                       BifrostContextKey = "bifrost-cluster-node-id"                          // string (cluster node ID for log attribution - set by enterprise server)
	BifrostContextKeyGovernanceBudgetIDs                 BifrostContextKey = "bifrost-governance-budget-ids"                    // []string (budget IDs applicable to this request - set by governance plugin)
	BifrostContextKeyGovernanceRateLimitIDs              BifrostContextKey = "bifrost-governance-rate-limit-ids"                // []string (rate limit IDs applicable to this request - set by governance plugin)
	BifrostContextKeyPromptsPluginName                   BifrostContextKey = "prompts-plugin-name"                              // string (name of the prompts plugin to use - set by bifrost - DO NOT SET THIS MANUALLY))
	BifrostContextKeyIsEnterprise                        BifrostContextKey = "is-enterprise"                                    // bool (set by bifrost - DO NOT SET THIS MANUALLY)
	BifrostContextKeyAvailableProviders                  BifrostContextKey = "available-providers"                              // []ModelProvider (set by internal bifrost components - DO NOT SET THIS MANUALLY))
	BifrostContextKeyStoreRawRequestResponse             BifrostContextKey = "bifrost-store-raw-request-response"               // bool (per-request override — read by bifrost.go, never overwritten)
	BifrostContextKeyCaptureRawRequest                   BifrostContextKey = "bifrost-capture-raw-request"                      // bool (set by bifrost - DO NOT SET THIS MANUALLY) — true when providers should capture raw request bytes
	BifrostContextKeyCaptureRawResponse                  BifrostContextKey = "bifrost-capture-raw-response"                     // bool (set by bifrost - DO NOT SET THIS MANUALLY) — true when providers should capture raw response bytes
	BifrostContextKeyDropRawRequestFromClient            BifrostContextKey = "bifrost-drop-raw-request-from-client"             // bool (set by bifrost - DO NOT SET THIS MANUALLY) — true when raw request should be stripped from the client-facing response
	BifrostContextKeyDropRawResponseFromClient           BifrostContextKey = "bifrost-drop-raw-response-from-client"            // bool (set by bifrost - DO NOT SET THIS MANUALLY) — true when raw response should be stripped from the client-facing response
	BifrostContextKeyShouldStoreRawInLogs                BifrostContextKey = "bifrost-should-store-raw-in-logs"                 // bool (set by bifrost - DO NOT SET THIS MANUALLY) — true when raw request/response should be persisted in log records
	BifrostContextKeyRetryDBFetch                        BifrostContextKey = "bifrost-retry-db-fetch"                           // bool (set by bifrost - DO NOT SET THIS MANUALLY)
	BifrostContextKeyIsCustomProvider                    BifrostContextKey = "bifrost-is-custom-provider"                       // bool (set by bifrost - DO NOT SET THIS MANUALLY)
	BifrostContextKeyHTTPRequestType                     BifrostContextKey = "bifrost-http-request-type"                        // RequestType (set by bifrost - DO NOT SET THIS MANUALLY)
	BifrostContextKeyHTTPRoute                           BifrostContextKey = "bifrost-http-route"                               // string (set by bifrost - DO NOT SET THIS MANUALLY — matched route template, set by HTTP transport; used as the low-cardinality metrics `path` label)
	BifrostContextKeyPassthroughExtraParams              BifrostContextKey = "bifrost-passthrough-extra-params"                 // bool
	BifrostContextKeyRoutingEnginesUsed                  BifrostContextKey = "bifrost-routing-engines-used"                     // []string (set by bifrost - DO NOT SET THIS MANUALLY) - list of routing engines used ("routing-rule", "governance", "loadbalancing", etc.)
	BifrostContextKeyRoutingEngineLogs                   BifrostContextKey = "bifrost-routing-engine-logs"                      // []RoutingEngineLogEntry (set by bifrost - DO NOT SET THIS MANUALLY) - list of routing engine log entries
	BifrostContextKeyTransportPluginLogs                 BifrostContextKey = "bifrost-transport-plugin-logs"                    // []PluginLogEntry (transport-layer plugin logs accumulated during HTTP transport hooks)
	BifrostContextKeyTransportPostHookCompleter          BifrostContextKey = "bifrost-transport-posthook-completer"             // func() (callback to run HTTPTransportPostHook after streaming - set by transport interceptor middleware)
	BifrostContextKeySkipPluginPipeline                  BifrostContextKey = "bifrost-skip-plugin-pipeline"                     // bool - skip plugin pipeline for the request
	BifrostContextKeyParentRequestID                     BifrostContextKey = "bifrost-parent-request-id"                        // string (parent linkage for grouped request logs like realtime turns)
	BifrostContextKeyRealtimeSessionID                   BifrostContextKey = "bifrost-realtime-session-id"                      // string
	BifrostContextKeyRealtimeProviderSessionID           BifrostContextKey = "bifrost-realtime-provider-session-id"             // string
	BifrostContextKeyRealtimeSource                      BifrostContextKey = "bifrost-realtime-source"                          // string ("ei" or "lm")
	BifrostContextKeyRealtimeEventType                   BifrostContextKey = "bifrost-realtime-event-type"                      // string
	BifrostContextKeyRealtimeTransport                   BifrostContextKey = "bifrost-realtime-transport"                       // string ("websocket" or "webrtc")
	BifrostContextKeyRealtimeVoice                       BifrostContextKey = "bifrost-realtime-voice"                           // string
	BifrostIsAsyncRequest                                BifrostContextKey = "bifrost-is-async-request"                         // bool (set by bifrost - DO NOT SET THIS MANUALLY)) - whether the request is an async request (only used in gateway)
	BifrostContextKeyRequestHeaders                      BifrostContextKey = "bifrost-request-headers"                          // map[string]string (all request headers with lowercased keys)
	BifrostContextKeyRequestQuery                        BifrostContextKey = "bifrost-request-query"                            // map[string]string (request query params with lowercased keys; consumed by governance routing CEL rules)
	BifrostContextKeyRoutingAllowedProviders             BifrostContextKey = "bifrost-routing-allowed-providers"                // []ModelProvider; when set, downstream routing layers (enterprise LB, model-catalog-resolver) must intersect their candidate providers with this set. Plugins set this when they have an opinion about which providers are valid for the request — even if they couldn't pick one themselves. Empty slice means "no provider is permitted" (fail-closed).
	BifrostContextKeyAllowPerRequestStorageOverride      BifrostContextKey = "bifrost-allow-per-request-storage-override"       // bool (set by transport from config — gates whether x-bf-disable-content-logging and x-bf-store-raw-request-response per-request overrides are honored)
	BifrostContextKeyAllowPerRequestRawOverride          BifrostContextKey = "bifrost-allow-per-request-raw-override"           // bool (set by transport from config — gates whether x-bf-send-back-raw-request and x-bf-send-back-raw-response per-request overrides are honored)
	BifrostContextKeyRedactionData                       BifrostContextKey = "bifrost-redaction-data"                           // RedactionData (set by enterprise guardrails plugin - DO NOT SET THIS MANUALLY)
	BifrostContextKeyDisableContentLogging               BifrostContextKey = "x-bf-disable-content-logging"                     // bool (per-request override for content logging; only honored when BifrostContextKeyAllowPerRequestStorageOverride is true. When retain_content_in_object_storage is on, disabled content is still offloaded to object storage as hidden instead of dropped)
	BifrostContextKeySkipListModelsGovernanceFiltering   BifrostContextKey = "bifrost-skip-list-models-governance-filtering"    // bool (set by bifrost - DO NOT SET THIS MANUALLY))
	BifrostContextKeySCIMClaims                          BifrostContextKey = "scim_claims"
	BifrostContextKeyUserID                              BifrostContextKey = "bifrost-user-id"                    // string (to store the user ID (set by enterprise auth middleware - DO NOT SET THIS MANUALLY))
	BifrostContextKeyUserName                            BifrostContextKey = "bifrost-user-name"                  // string (to store the user name (set by enterprise auth middleware - DO NOT SET THIS MANUALLY))
	BifrostContextKeyUserEmail                           BifrostContextKey = "bifrost-user-email"                 // string (to store the user email (set by enterprise auth middleware - DO NOT SET THIS MANUALLY))
	BifrostContextKeyQueryScope                          BifrostContextKey = "bifrost-query-scope"                // configstore.QueryScope (func that mutates a query; set by upstream wrapper - DO NOT SET THIS MANUALLY)
	BifrostContextKeyVisibilityFilterProvider            BifrostContextKey = "bifrost-visibility-filter-provider" // DEPRECATED: replaced by BifrostContextKeyQueryScope. Will be removed once all callers migrate.
	BifrostContextKeyTargetUserID                        BifrostContextKey = "target_user_id"
	BifrostContextKeyIsAzureUserAgent                    BifrostContextKey = "bifrost-is-azure-user-agent" // bool (set by bifrost - DO NOT SET THIS MANUALLY)) - whether the request is an Azure user agent (only used in gateway)
	BifrostContextKeyUserRoleID                          BifrostContextKey = "bifrost-user-role-id"
	BifrostContextKeyVideoOutputRequested                BifrostContextKey = "bifrost-video-output-requested"
	BifrostContextKeyValidateKeys                        BifrostContextKey = "bifrost-validate-keys"                      // bool (triggers additional key validation during provider add/update)
	BifrostContextKeyProviderResponseHeaders             BifrostContextKey = "bifrost-provider-response-headers"          // map[string]string (set by provider handlers for response header forwarding)
	BifrostContextKeyMCPAddedTools                       BifrostContextKey = "bifrost-mcp-added-tools"                    // []string (set by bifrost - DO NOT SET THIS MANUALLY)) - list of tools added to the request by MCP, all the tool are in the format "clientName-toolName"
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
	BifrostContextKeyStreamAccumulatedUsage              BifrostContextKey = "bifrost-stream-accumulated-usage"           // *BifrostLLMUsage handle, set ONCE by a streaming provider and mutated in place as usage arrives; read on cancel/timeout to bill partial usage that the provider already consumed
	BifrostContextKeyDeferredLargePayloadMetadata        BifrostContextKey = "bifrost-deferred-large-payload-metadata"    // <-chan *LargePayloadMetadata (set by enterprise Phase B request — delivers metadata after body streaming)
	BifrostContextKeySSEReaderFactory                    BifrostContextKey = "bifrost-sse-reader-factory"                 // *providerUtils.SSEReaderFactory (set by enterprise — replaces default bufio.Scanner SSE readers with streaming readers)
	BifrostContextKeySessionID                           BifrostContextKey = "bifrost-session-id"                         // string session ID for the request (session stickiness)
	BifrostContextKeySessionTTL                          BifrostContextKey = "bifrost-session-ttl"                        // time.Duration session TTL for the request (session stickiness)
	BifrostContextKeyMCPExtraHeaders                     BifrostContextKey = "bifrost-mcp-extra-headers"                  // map[string][]string (these headers are forwarded only to the MCP while tool execution if they are in the allowlist of the MCP client)
	BifrostContextKeyMCPLogID                            BifrostContextKey = "bifrost-mcp-log-id"                         // string (unique UUID for each MCP tool log entry - set per goroutine by agent executor - DO NOT SET THIS MANUALLY)
	BifrostContextKeyMCPHealthCheckRequest               BifrostContextKey = "bifrost-mcp-health-check-request"           // bool (set by bifrost - DO NOT SET THIS MANUALLY) - true when the MCP ping/list-tools request was generated by bifrost itself for health checks rather than originating from a caller
	BifrostContextKeyCompatConvertTextToChat             BifrostContextKey = "bifrost-compat-convert-text-to-chat"        // bool (per-request override from x-bf-compat header)
	BifrostContextKeyCompatConvertChatToResponses        BifrostContextKey = "bifrost-compat-convert-chat-to-responses"   // bool (per-request override from x-bf-compat header)
	BifrostContextKeyCompatShouldDropParams              BifrostContextKey = "bifrost-compat-should-drop-params"          // bool (per-request override from x-bf-compat header)
	BifrostContextKeyCompatShouldConvertParams           BifrostContextKey = "bifrost-compat-should-convert-params"       // bool (per-request override from x-bf-compat header)
	BifrostContextKeySupportsAssistantPrefill            BifrostContextKey = "bifrost-supports-assistant-prefill"         // bool (set by compat plugin) - if model supports assistant prefill
	BifrostContextKeyAttemptTrail                        BifrostContextKey = "bifrost-attempt-trail"                      // []KeyAttemptRecord (set by bifrost - DO NOT SET THIS MANUALLY) - per-attempt key selection history
	BifrostContextKeyDimensions                          BifrostContextKey = "bifrost-dimensions"                         // map[string]string (set by HTTP transport from x-bf-dim-* headers) BifrostContextKeyDimensions holds per-request key/value dimensions supplied via x-bf-dim-<key> request headers. These dimensions are forwarded to internal logs (as metadata)
	IsAPIKeyAuthContextKey                               BifrostContextKey = "is_api_key_auth"
	IsLocalAdminContextKey                               BifrostContextKey = "is_local_admin"                // bool (set by auth middleware when password-based auth succeeds - local admin user bypasses RBAC)
	BifrostContextKeyPassthroughOverridesPresent         BifrostContextKey = "passthrough_overrides_present" // bool (set by HTTP transport) - passthrough raw request requested
	BifrostContextKeyConnectionClosed                    BifrostContextKey = "connection_closed"
	BifrostContextKeyTempTokenScope                      BifrostContextKey = "bifrost-temp-token-scope"       // string (set by auth middleware when a temp token authorized the request - names the scope from the temptoken registry)
	BifrostContextKeyTempTokenResourceID                 BifrostContextKey = "bifrost-temp-token-resource-id" // string (set by auth middleware alongside the scope - the resource_id the token is bound to, e.g. an OAuth flow ID for mcp_auth)
	BifrostContextKeyAsyncWebhookEndpoint                BifrostContextKey = "bifrost-async-webhook-endpoint" // string (webhook endpoint name to notify when an async job finishes - carried as-is from the x-bf-async-webhook header; the submit path resolves and validates it before the job is created)
)

const (
	// DefaultLargePayloadRequestThresholdBytes is the default request-size heuristic
	// used by transport guards when no enterprise threshold is present on context.
	DefaultLargePayloadRequestThresholdBytes = 10 * 1024 * 1024 // 10MB
)

// RoutingEngine constants
const (
	RoutingEngineGovernance     = "governance"
	RoutingEngineRoutingRule    = "routing-rule"
	RoutingEngineLoadbalancing  = "loadbalancing"
	RoutingEngineModelCatalog   = "model-catalog"
	RoutingEngineCircuitBreaker = "circuit-breaker"
	// RoutingEngineCore represents the Bifrost core orchestrator's own
	// routing decisions — primarily fallback transitions. Emitted when the
	// primary attempt fails and core advances through the fallback chain so
	// the per-request audit trail closes the loop on what plugin-level
	// engines (governance, loadbalancing, etc.) selected upstream.
	RoutingEngineCore = "core"
)

// KeyAttemptRecord captures the outcome of a single request attempt within executeRequestWithRetries.
// One record is appended per attempt regardless of whether the key changed between attempts.
//
// FailReason is populated on every failed attempt (retryable or terminal) and is nil only on a
// successful attempt. Status-derived values are: `rate_limit_error` (429), `authentication_error`
// (401/403), `billing_error` (402); otherwise the provider's error Type is used, falling back to
// `unknown`. Use it to inspect what went wrong on a given try.
//
// TriggeredRotation is true iff this attempt's per-key failure caused the next attempt to actually
// rotate to a *different* key. It is false on:
//   - the final (terminal) attempt of a request, regardless of outcome,
//   - any successful attempt,
//   - same-key retries (transient 5xx / network errors keep the same key),
//   - non-retryable failures,
//   - fixed-key paths and 429 pool-resets that re-pick the same key (no rotation actually happened).
//
// Use this (not FailReason) to count actual key rotations.
type KeyAttemptRecord struct {
	Attempt           int     `json:"attempt"`
	KeyID             string  `json:"key_id"`
	KeyName           string  `json:"key_name"`
	FailReason        *string `json:"fail_reason,omitempty"`
	TriggeredRotation bool    `json:"triggered_rotation"`
}

// RoutingEngineLogEntry represents a log entry from a routing engine
// format: [timestamp] [engine] - message
type RoutingEngineLogEntry struct {
	Engine    string   `json:"engine"` // e.g., "governance", "routing-rule", "openrouter"
	Level     LogLevel `json:"level"`
	Message   string   `json:"message"`   // Human-readable decision/action message
	Timestamp int64    `json:"timestamp"` // Unix milliseconds
}

// PluginLogEntry represents a structured log entry emitted by a plugin via ctx.Log().
type PluginLogEntry struct {
	PluginName string   `json:"plugin_name"`
	Level      LogLevel `json:"level"`
	Message    string   `json:"message"`
	Timestamp  int64    `json:"timestamp"` // Unix milliseconds
}

// GroupPluginLogsByName groups a flat slice of plugin log entries by plugin name.
// Returns nil if the input is empty.
func GroupPluginLogsByName(logs []PluginLogEntry) map[string][]PluginLogEntry {
	if len(logs) == 0 {
		return nil
	}
	grouped := make(map[string][]PluginLogEntry, min(len(logs), 4))
	for _, entry := range logs {
		grouped[entry.PluginName] = append(grouped[entry.PluginName], entry)
	}
	return grouped
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
	ResponsesRetrieveRequest     *BifrostResponsesRetrieveRequest
	ResponsesDeleteRequest       *BifrostResponsesDeleteRequest
	ResponsesCancelRequest       *BifrostResponsesCancelRequest
	ResponsesInputItemsRequest   *BifrostResponsesInputItemsRequest
	CountTokensRequest           *BifrostResponsesRequest
	CompactionRequest            *BifrostCompactionRequest
	EmbeddingRequest             *BifrostEmbeddingRequest
	RerankRequest                *BifrostRerankRequest
	OCRRequest                   *BifrostOCRRequest
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
	CachedContentCreateRequest   *BifrostCachedContentCreateRequest
	CachedContentListRequest     *BifrostCachedContentListRequest
	CachedContentRetrieveRequest *BifrostCachedContentRetrieveRequest
	CachedContentUpdateRequest   *BifrostCachedContentUpdateRequest
	CachedContentDeleteRequest   *BifrostCachedContentDeleteRequest
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
	case br.ResponsesRetrieveRequest != nil:
		return br.ResponsesRetrieveRequest.Provider, "", nil
	case br.ResponsesDeleteRequest != nil:
		return br.ResponsesDeleteRequest.Provider, "", nil
	case br.ResponsesCancelRequest != nil:
		return br.ResponsesCancelRequest.Provider, "", nil
	case br.ResponsesInputItemsRequest != nil:
		return br.ResponsesInputItemsRequest.Provider, "", nil
	case br.CountTokensRequest != nil:
		return br.CountTokensRequest.Provider, br.CountTokensRequest.Model, br.CountTokensRequest.Fallbacks
	case br.CompactionRequest != nil:
		return br.CompactionRequest.Provider, br.CompactionRequest.Model, br.CompactionRequest.Fallbacks
	case br.EmbeddingRequest != nil:
		return br.EmbeddingRequest.Provider, br.EmbeddingRequest.Model, br.EmbeddingRequest.Fallbacks
	case br.RerankRequest != nil:
		return br.RerankRequest.Provider, br.RerankRequest.Model, br.RerankRequest.Fallbacks
	case br.OCRRequest != nil:
		return br.OCRRequest.Provider, br.OCRRequest.Model, br.OCRRequest.Fallbacks
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
	case br.CachedContentCreateRequest != nil:
		return br.CachedContentCreateRequest.Provider, br.CachedContentCreateRequest.Model, nil
	case br.CachedContentListRequest != nil:
		if br.CachedContentListRequest.Model != nil {
			return br.CachedContentListRequest.Provider, *br.CachedContentListRequest.Model, nil
		}
		return br.CachedContentListRequest.Provider, "", nil
	case br.CachedContentRetrieveRequest != nil:
		if br.CachedContentRetrieveRequest.Model != nil {
			return br.CachedContentRetrieveRequest.Provider, *br.CachedContentRetrieveRequest.Model, nil
		}
		return br.CachedContentRetrieveRequest.Provider, "", nil
	case br.CachedContentUpdateRequest != nil:
		if br.CachedContentUpdateRequest.Model != nil {
			return br.CachedContentUpdateRequest.Provider, *br.CachedContentUpdateRequest.Model, nil
		}
		return br.CachedContentUpdateRequest.Provider, "", nil
	case br.CachedContentDeleteRequest != nil:
		if br.CachedContentDeleteRequest.Model != nil {
			return br.CachedContentDeleteRequest.Provider, *br.CachedContentDeleteRequest.Model, nil
		}
		return br.CachedContentDeleteRequest.Provider, "", nil
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
	case br.ResponsesRetrieveRequest != nil:
		br.ResponsesRetrieveRequest.Provider = provider
	case br.ResponsesDeleteRequest != nil:
		br.ResponsesDeleteRequest.Provider = provider
	case br.ResponsesCancelRequest != nil:
		br.ResponsesCancelRequest.Provider = provider
	case br.ResponsesInputItemsRequest != nil:
		br.ResponsesInputItemsRequest.Provider = provider
	case br.CountTokensRequest != nil:
		br.CountTokensRequest.Provider = provider
	case br.CompactionRequest != nil:
		br.CompactionRequest.Provider = provider
	case br.EmbeddingRequest != nil:
		br.EmbeddingRequest.Provider = provider
	case br.RerankRequest != nil:
		br.RerankRequest.Provider = provider
	case br.OCRRequest != nil:
		br.OCRRequest.Provider = provider
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
	case br.CachedContentCreateRequest != nil:
		br.CachedContentCreateRequest.Provider = provider
	case br.CachedContentListRequest != nil:
		br.CachedContentListRequest.Provider = provider
	case br.CachedContentRetrieveRequest != nil:
		br.CachedContentRetrieveRequest.Provider = provider
	case br.CachedContentUpdateRequest != nil:
		br.CachedContentUpdateRequest.Provider = provider
	case br.CachedContentDeleteRequest != nil:
		br.CachedContentDeleteRequest.Provider = provider
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
	case br.CompactionRequest != nil:
		br.CompactionRequest.Model = model
	case br.EmbeddingRequest != nil:
		br.EmbeddingRequest.Model = model
	case br.RerankRequest != nil:
		br.RerankRequest.Model = model
	case br.OCRRequest != nil:
		br.OCRRequest.Model = model
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
	case br.BatchCreateRequest != nil:
		if br.BatchCreateRequest.Model != nil {
			br.BatchCreateRequest.Model = new(model)
		}
	case br.CachedContentCreateRequest != nil:
		br.CachedContentCreateRequest.Model = model
	case br.CachedContentListRequest != nil:
		if br.CachedContentListRequest.Model != nil {
			br.CachedContentListRequest.Model = new(model)
		}
	case br.CachedContentRetrieveRequest != nil:
		if br.CachedContentRetrieveRequest.Model != nil {
			br.CachedContentRetrieveRequest.Model = new(model)
		}
	case br.CachedContentUpdateRequest != nil:
		if br.CachedContentUpdateRequest.Model != nil {
			br.CachedContentUpdateRequest.Model = new(model)
		}
	case br.CachedContentDeleteRequest != nil:
		if br.CachedContentDeleteRequest.Model != nil {
			br.CachedContentDeleteRequest.Model = new(model)
		}
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
	case br.CompactionRequest != nil:
		br.CompactionRequest.Fallbacks = fallbacks
	case br.EmbeddingRequest != nil:
		br.EmbeddingRequest.Fallbacks = fallbacks
	case br.RerankRequest != nil:
		br.RerankRequest.Fallbacks = fallbacks
	case br.OCRRequest != nil:
		br.OCRRequest.Fallbacks = fallbacks
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
	case br.ResponsesRetrieveRequest != nil:
		br.ResponsesRetrieveRequest.RawRequestBody = rawRequestBody
	case br.ResponsesDeleteRequest != nil:
		br.ResponsesDeleteRequest.RawRequestBody = rawRequestBody
	case br.ResponsesCancelRequest != nil:
		br.ResponsesCancelRequest.RawRequestBody = rawRequestBody
	case br.ResponsesInputItemsRequest != nil:
		br.ResponsesInputItemsRequest.RawRequestBody = rawRequestBody
	case br.CountTokensRequest != nil:
		br.CountTokensRequest.RawRequestBody = rawRequestBody
	case br.CompactionRequest != nil:
		br.CompactionRequest.RawRequestBody = rawRequestBody
	case br.EmbeddingRequest != nil:
		br.EmbeddingRequest.RawRequestBody = rawRequestBody
	case br.RerankRequest != nil:
		br.RerankRequest.RawRequestBody = rawRequestBody
	case br.OCRRequest != nil:
		br.OCRRequest.RawRequestBody = rawRequestBody
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
	case br.CachedContentCreateRequest != nil:
		br.CachedContentCreateRequest.RawRequestBody = rawRequestBody
	case br.CachedContentListRequest != nil:
		br.CachedContentListRequest.RawRequestBody = rawRequestBody
	case br.CachedContentRetrieveRequest != nil:
		br.CachedContentRetrieveRequest.RawRequestBody = rawRequestBody
	case br.CachedContentUpdateRequest != nil:
		br.CachedContentUpdateRequest.RawRequestBody = rawRequestBody
	case br.CachedContentDeleteRequest != nil:
		br.CachedContentDeleteRequest.RawRequestBody = rawRequestBody
	}
}

type MCPRequestType string

const (
	MCPRequestTypePing      MCPRequestType = "ping"
	MCPRequestTypeListTools MCPRequestType = "list_tools"

	// [DEPRECATED] these will be replaced by MCPRequestTypeExecuteTool in the next major bump, but are kept for backward compatibility for now since some tools still rely on the old fields
	MCPRequestTypeChatToolCall      MCPRequestType = "chat_tool_call"      // Chat API format
	MCPRequestTypeResponsesToolCall MCPRequestType = "responses_tool_call" // Responses API format

	// Will be used in from the next major bump
	MCPRequestTypeExecuteTool MCPRequestType = "execute_tool"
)

// IsExecuteTool reports whether this is one of the execute-tool request variants
// (Chat, Responses, or the future unified ExecuteTool). Used by MCPPlugin pre/post
// hooks to skip non-tool envelope ops (Ping/ListTools) without sniffing pointer
// fields on the request/response.
//
// NOTE: this helper exists because three execute-tool request types currently
// coexist for backwards compat (ChatToolCall + ResponsesToolCall are deprecated).
// Once callers fully migrate to MCPRequestTypeExecuteTool, this method will be
// removed and consumers should switch to `t == MCPRequestTypeExecuteTool` directly.
func (t MCPRequestType) IsExecuteTool() bool {
	switch t {
	case MCPRequestTypeChatToolCall,
		MCPRequestTypeResponsesToolCall,
		MCPRequestTypeExecuteTool:
		return true
	}
	return false
}

// OTelMethodName returns the OTel semconv mcp.method.name for this request type
// (tools/call, tools/list, ping). Unknown types fall back to the raw string.
func (t MCPRequestType) OTelMethodName() string {
	switch {
	case t.IsExecuteTool():
		return "tools/call"
	case t == MCPRequestTypeListTools:
		return "tools/list"
	case t == MCPRequestTypePing:
		return "ping"
	default:
		return string(t)
	}
}

// BifrostMCPRequest is the envelope for MCP requests that flow through the generic
// PreMCPHook/PostMCPHook pipeline (Ping, ListTools, ExecuteTool variants). Connect
// requests do NOT use this envelope — they are dispatched via the typed
// MCPConnectionPlugin interface using *BifrostMCPConnectRequest directly.
//
// Exactly one of the embedded sub-request pointers is populated, matched by RequestType:
//   - RequestType == MCPRequestTypePing       → BifrostMCPPingRequest
//   - RequestType == MCPRequestTypeListTools  → BifrostMCPListToolsRequest
//   - RequestType == MCPRequestTypeExecuteTool / MCPRequestTypeChatToolCall / MCPRequestTypeResponsesToolCall → BifrostMCPExecuteToolRequest
type BifrostMCPRequest struct {
	RequestType MCPRequestType
	ClientName  string // MCP client this request targets (always set, regardless of request type)

	*BifrostMCPPingRequest
	*BifrostMCPListToolsRequest

	// [DEPRECATED] these will be replaced by BifrostMCPExecuteToolRequest in the next major bump, but are kept for backward compatibility for now since some tools still rely on the old fields
	*ChatAssistantMessageToolCall
	*ResponsesToolMessage

	// Will be used in from the next major bump
	*BifrostMCPExecuteToolRequest
}

// BifrostMCPConnectRequest carries the prepared inputs for an MCP connect operation.
// Fields marked "mutable" may be modified by a plugin's PreMCPHook and the mutated values
// will be used for the actual transport creation; "observe-only" fields are passed to plugins
// for context but mutations are ignored (changing the transport type mid-flight would break
// the rest of the connect codepath).
type BifrostMCPConnectRequest struct {
	ClientName       string            // observe-only — name of the client being connected
	ConnectionType   MCPConnectionType // observe-only — transport type being established (http/stdio/sse/inprocess)
	AuthType         MCPAuthType       // observe-only — authentication mode configured on the client
	ConnectionString *string           // mutable — URL for http/sse, nil for stdio/inprocess
	Headers          map[string]string // mutable — transport-level headers (http/sse only; stdio/inprocess ignore)
	StdioCommand     *string           // mutable — command for stdio connections (nil otherwise)
	StdioArgs        []string          // mutable — argv for stdio connections (nil otherwise)
}

// BifrostMCPPingRequest is intentionally empty: the wire ping rides over the existing
// transport and has no per-call headers or parameters. Plugins observe via ClientName on
// the parent BifrostMCPRequest and may short-circuit (synthetic healthy/unhealthy).
type BifrostMCPPingRequest struct{}

// BifrostMCPListToolsRequest is intentionally empty for the same reason as ping: list_tools
// reuses the existing transport's headers. Plugins observe via ClientName and may short-circuit
// (e.g. cached tool list).
type BifrostMCPListToolsRequest struct{}

// Keeping the stub for now, will be used from the next major bump when we remove the old ChatToolCall and ResponsesToolMessage fields.
// Note that the tool name and arguments are not standardized in this struct yet since they are still being pulled from the old fields for backward compatibility,
// but they will be standardized in the future when we remove the old fields.
type BifrostMCPExecuteToolRequest struct{}

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
	ResponsesDeleteResponse       *BifrostResponsesDeleteResponse
	ResponsesInputItemsResponse   *BifrostResponsesInputItemsResponse
	CountTokensResponse           *BifrostCountTokensResponse
	CompactionResponse            *BifrostCompactionResponse
	EmbeddingResponse             *BifrostEmbeddingResponse
	RerankResponse                *BifrostRerankResponse
	OCRResponse                   *BifrostOCRResponse
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
	CachedContentCreateResponse   *BifrostCachedContentCreateResponse
	CachedContentListResponse     *BifrostCachedContentListResponse
	CachedContentRetrieveResponse *BifrostCachedContentRetrieveResponse
	CachedContentUpdateResponse   *BifrostCachedContentUpdateResponse
	CachedContentDeleteResponse   *BifrostCachedContentDeleteResponse
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
	case r.ResponsesDeleteResponse != nil:
		return &r.ResponsesDeleteResponse.ExtraFields
	case r.ResponsesInputItemsResponse != nil:
		return &r.ResponsesInputItemsResponse.ExtraFields
	case r.CountTokensResponse != nil:
		return &r.CountTokensResponse.ExtraFields
	case r.CompactionResponse != nil:
		return &r.CompactionResponse.ExtraFields
	case r.EmbeddingResponse != nil:
		return &r.EmbeddingResponse.ExtraFields
	case r.RerankResponse != nil:
		return &r.RerankResponse.ExtraFields
	case r.OCRResponse != nil:
		return &r.OCRResponse.ExtraFields
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
	case r.CachedContentCreateResponse != nil:
		return &r.CachedContentCreateResponse.ExtraFields
	case r.CachedContentListResponse != nil:
		return &r.CachedContentListResponse.ExtraFields
	case r.CachedContentRetrieveResponse != nil:
		return &r.CachedContentRetrieveResponse.ExtraFields
	case r.CachedContentUpdateResponse != nil:
		return &r.CachedContentUpdateResponse.ExtraFields
	case r.CachedContentDeleteResponse != nil:
		return &r.CachedContentDeleteResponse.ExtraFields
	}

	return &BifrostResponseExtraFields{}
}

// syncDeprecatedFromRoutingInfo backfills the deprecated Provider /
// OriginalModelRequested / ResolvedModelUsed triplet on an ExtraFields-like
// target from a finalized RoutingInfo, applying the rules documented on each
// deprecated field. Centralized so PopulateRoutingInfo and
// SetFallbackRoutingInfo cannot drift apart.
func syncDeprecatedFromRoutingInfo(info RoutingInfo, provider *ModelProvider, originalModelRequested, resolvedModelUsed *string) {
	if provider != nil && info.Provider != "" {
		*provider = info.Provider
	}
	// OriginalModelRequested: collapses to the caller-sent model. On a fallback
	// attempt that's the primary's model (the user never asked for the fallback's);
	// otherwise it's this attempt's model.
	if originalModelRequested != nil {
		if info.IsFallback && info.PrimaryModel != nil && *info.PrimaryModel != "" {
			*originalModelRequested = *info.PrimaryModel
		} else if info.Model != "" {
			*originalModelRequested = info.Model
		}
	}
	// ResolvedModelUsed: the wire model. Alias's ModelID when an alias matched,
	// otherwise the attempt's Model.
	if resolvedModelUsed != nil {
		if info.ResolvedKeyAlias != nil && info.ResolvedKeyAlias.ModelID != "" {
			*resolvedModelUsed = info.ResolvedKeyAlias.ModelID
		} else if info.Model != "" {
			*resolvedModelUsed = info.Model
		}
	}
}

// PopulateRoutingInfo sets ExtraFields.RoutingInfo on the active sub-response
// and keeps the deprecated Provider/OriginalModelRequested/ResolvedModelUsed
// triplet in sync per their documented derivation rules.
// Core always calls this both before and after RunPostLLMHooks so any plugin
// modifications are no-ops — tampering with RoutingInfo inside plugins is
// discouraged.
func (r *BifrostResponse) PopulateRoutingInfo(info RoutingInfo) {
	if r == nil {
		return
	}
	if ef := r.GetExtraFields(); ef != nil {
		// ServerSideFallbackModel is the one provider-owned field on RoutingInfo:
		// the orchestrator cannot see a model swap that happened inside a single
		// upstream call, so carry the provider's value across this overwrite.
		// Streaming relies on this too — the closure's snapshot predates the final
		// usage chunk that reveals the handoff.
		if info.ServerSideFallbackModel == nil {
			info.ServerSideFallbackModel = ef.RoutingInfo.ServerSideFallbackModel
		}
		ef.RoutingInfo = info
		syncDeprecatedFromRoutingInfo(info, &ef.Provider, &ef.OriginalModelRequested, &ef.ResolvedModelUsed)
	}
}

// PopulateRoutingInfo sets ExtraFields.RoutingInfo on the error and syncs the
// deprecated triplet. Core calls this both before and after RunPostLLMHooks
// alongside PopulateExtraFields.
func (e *BifrostError) PopulateRoutingInfo(info RoutingInfo) {
	if e == nil {
		return
	}
	e.ExtraFields.RoutingInfo = info
	syncDeprecatedFromRoutingInfo(info, &e.ExtraFields.Provider, &e.ExtraFields.OriginalModelRequested, &e.ExtraFields.ResolvedModelUsed)
}

// SetFallbackRoutingInfo marks the active sub-response's RoutingInfo as a
// fallback attempt and records the primary attempt's provider/model. Also
// re-syncs the deprecated OriginalModelRequested to the primary model per
// its documented derivation rule.
// Called by the orchestrator (handleRequest) on each fallback attempt's
// result/error — the per-attempt code never sets these fields itself.
func (r *BifrostResponse) SetFallbackRoutingInfo(primaryProvider ModelProvider, primaryModel string) {
	if r == nil {
		return
	}
	ef := r.GetExtraFields()
	if ef == nil {
		return
	}
	ef.RoutingInfo.IsFallback = true
	if primaryProvider != "" {
		p := primaryProvider
		ef.RoutingInfo.PrimaryProvider = &p
	}
	if primaryModel != "" {
		m := primaryModel
		ef.RoutingInfo.PrimaryModel = &m
	}
	syncDeprecatedFromRoutingInfo(ef.RoutingInfo, &ef.Provider, &ef.OriginalModelRequested, &ef.ResolvedModelUsed)
}

// SetFallbackRoutingInfo is the BifrostError counterpart — see the
// BifrostResponse method for semantics.
func (e *BifrostError) SetFallbackRoutingInfo(primaryProvider ModelProvider, primaryModel string) {
	if e == nil {
		return
	}
	e.ExtraFields.RoutingInfo.IsFallback = true
	if primaryProvider != "" {
		p := primaryProvider
		e.ExtraFields.RoutingInfo.PrimaryProvider = &p
	}
	if primaryModel != "" {
		m := primaryModel
		e.ExtraFields.RoutingInfo.PrimaryModel = &m
	}
	syncDeprecatedFromRoutingInfo(e.ExtraFields.RoutingInfo, &e.ExtraFields.Provider, &e.ExtraFields.OriginalModelRequested, &e.ExtraFields.ResolvedModelUsed)
}

// PopulateExtraFields sets RequestType, Provider, OriginalModelRequested, and ResolvedModelUsed on the
// active sub-response. Core always calls this both before and after RunPostLLMHooks, so any plugin
// modifications to these 4 fields are no-ops — tampering with them inside plugins is discouraged.
func (r *BifrostResponse) PopulateExtraFields(requestType RequestType, provider ModelProvider, originalModelRequested string, resolvedModelUsed string) {
	if r == nil {
		return
	}
	resolvedModel := resolvedModelUsed
	if resolvedModel == "" {
		resolvedModel = originalModelRequested
	}
	switch {
	case r.ListModelsResponse != nil:
		r.ListModelsResponse.ExtraFields.RequestType = requestType
		r.ListModelsResponse.ExtraFields.Provider = provider
		r.ListModelsResponse.ExtraFields.OriginalModelRequested = originalModelRequested
		r.ListModelsResponse.ExtraFields.ResolvedModelUsed = resolvedModel
	case r.TextCompletionResponse != nil:
		r.TextCompletionResponse.ExtraFields.RequestType = requestType
		r.TextCompletionResponse.ExtraFields.Provider = provider
		r.TextCompletionResponse.ExtraFields.OriginalModelRequested = originalModelRequested
		r.TextCompletionResponse.ExtraFields.ResolvedModelUsed = resolvedModel
	case r.ChatResponse != nil:
		r.ChatResponse.ExtraFields.RequestType = requestType
		r.ChatResponse.ExtraFields.Provider = provider
		r.ChatResponse.ExtraFields.OriginalModelRequested = originalModelRequested
		r.ChatResponse.ExtraFields.ResolvedModelUsed = resolvedModel
	case r.ResponsesResponse != nil:
		r.ResponsesResponse.ExtraFields.RequestType = requestType
		r.ResponsesResponse.ExtraFields.Provider = provider
		r.ResponsesResponse.ExtraFields.OriginalModelRequested = originalModelRequested
		r.ResponsesResponse.ExtraFields.ResolvedModelUsed = resolvedModel
	case r.ResponsesDeleteResponse != nil:
		r.ResponsesDeleteResponse.ExtraFields.RequestType = requestType
		r.ResponsesDeleteResponse.ExtraFields.Provider = provider
		r.ResponsesDeleteResponse.ExtraFields.OriginalModelRequested = originalModelRequested
		r.ResponsesDeleteResponse.ExtraFields.ResolvedModelUsed = resolvedModel
	case r.ResponsesInputItemsResponse != nil:
		r.ResponsesInputItemsResponse.ExtraFields.RequestType = requestType
		r.ResponsesInputItemsResponse.ExtraFields.Provider = provider
		r.ResponsesInputItemsResponse.ExtraFields.OriginalModelRequested = originalModelRequested
		r.ResponsesInputItemsResponse.ExtraFields.ResolvedModelUsed = resolvedModel
	case r.ResponsesStreamResponse != nil:
		r.ResponsesStreamResponse.ExtraFields.RequestType = requestType
		r.ResponsesStreamResponse.ExtraFields.Provider = provider
		r.ResponsesStreamResponse.ExtraFields.OriginalModelRequested = originalModelRequested
		r.ResponsesStreamResponse.ExtraFields.ResolvedModelUsed = resolvedModel
	case r.CountTokensResponse != nil:
		r.CountTokensResponse.ExtraFields.RequestType = requestType
		r.CountTokensResponse.ExtraFields.Provider = provider
		r.CountTokensResponse.ExtraFields.OriginalModelRequested = originalModelRequested
		r.CountTokensResponse.ExtraFields.ResolvedModelUsed = resolvedModel
	case r.CompactionResponse != nil:
		r.CompactionResponse.ExtraFields.RequestType = requestType
		r.CompactionResponse.ExtraFields.Provider = provider
		r.CompactionResponse.ExtraFields.OriginalModelRequested = originalModelRequested
		r.CompactionResponse.ExtraFields.ResolvedModelUsed = resolvedModel
	case r.EmbeddingResponse != nil:
		r.EmbeddingResponse.ExtraFields.RequestType = requestType
		r.EmbeddingResponse.ExtraFields.Provider = provider
		r.EmbeddingResponse.ExtraFields.OriginalModelRequested = originalModelRequested
		r.EmbeddingResponse.ExtraFields.ResolvedModelUsed = resolvedModel
	case r.RerankResponse != nil:
		r.RerankResponse.ExtraFields.RequestType = requestType
		r.RerankResponse.ExtraFields.Provider = provider
		r.RerankResponse.ExtraFields.OriginalModelRequested = originalModelRequested
		r.RerankResponse.ExtraFields.ResolvedModelUsed = resolvedModel
	case r.SpeechResponse != nil:
		r.SpeechResponse.ExtraFields.RequestType = requestType
		r.SpeechResponse.ExtraFields.Provider = provider
		r.SpeechResponse.ExtraFields.OriginalModelRequested = originalModelRequested
		r.SpeechResponse.ExtraFields.ResolvedModelUsed = resolvedModel
	case r.SpeechStreamResponse != nil:
		r.SpeechStreamResponse.ExtraFields.RequestType = requestType
		r.SpeechStreamResponse.ExtraFields.Provider = provider
		r.SpeechStreamResponse.ExtraFields.OriginalModelRequested = originalModelRequested
		r.SpeechStreamResponse.ExtraFields.ResolvedModelUsed = resolvedModel
	case r.TranscriptionResponse != nil:
		r.TranscriptionResponse.ExtraFields.RequestType = requestType
		r.TranscriptionResponse.ExtraFields.Provider = provider
		r.TranscriptionResponse.ExtraFields.OriginalModelRequested = originalModelRequested
		r.TranscriptionResponse.ExtraFields.ResolvedModelUsed = resolvedModel
	case r.TranscriptionStreamResponse != nil:
		r.TranscriptionStreamResponse.ExtraFields.RequestType = requestType
		r.TranscriptionStreamResponse.ExtraFields.Provider = provider
		r.TranscriptionStreamResponse.ExtraFields.OriginalModelRequested = originalModelRequested
		r.TranscriptionStreamResponse.ExtraFields.ResolvedModelUsed = resolvedModel
	case r.ImageGenerationResponse != nil:
		r.ImageGenerationResponse.ExtraFields.RequestType = requestType
		r.ImageGenerationResponse.ExtraFields.Provider = provider
		r.ImageGenerationResponse.ExtraFields.OriginalModelRequested = originalModelRequested
		r.ImageGenerationResponse.ExtraFields.ResolvedModelUsed = resolvedModel
	case r.ImageGenerationStreamResponse != nil:
		r.ImageGenerationStreamResponse.ExtraFields.RequestType = requestType
		r.ImageGenerationStreamResponse.ExtraFields.Provider = provider
		r.ImageGenerationStreamResponse.ExtraFields.OriginalModelRequested = originalModelRequested
		r.ImageGenerationStreamResponse.ExtraFields.ResolvedModelUsed = resolvedModel
	case r.VideoGenerationResponse != nil:
		r.VideoGenerationResponse.ExtraFields.RequestType = requestType
		r.VideoGenerationResponse.ExtraFields.Provider = provider
		r.VideoGenerationResponse.ExtraFields.OriginalModelRequested = originalModelRequested
		r.VideoGenerationResponse.ExtraFields.ResolvedModelUsed = resolvedModel
	case r.VideoDownloadResponse != nil:
		r.VideoDownloadResponse.ExtraFields.RequestType = requestType
		r.VideoDownloadResponse.ExtraFields.Provider = provider
		r.VideoDownloadResponse.ExtraFields.OriginalModelRequested = originalModelRequested
		r.VideoDownloadResponse.ExtraFields.ResolvedModelUsed = resolvedModel
	case r.VideoListResponse != nil:
		r.VideoListResponse.ExtraFields.RequestType = requestType
		r.VideoListResponse.ExtraFields.Provider = provider
		r.VideoListResponse.ExtraFields.OriginalModelRequested = originalModelRequested
		r.VideoListResponse.ExtraFields.ResolvedModelUsed = resolvedModel
	case r.VideoDeleteResponse != nil:
		r.VideoDeleteResponse.ExtraFields.RequestType = requestType
		r.VideoDeleteResponse.ExtraFields.Provider = provider
		r.VideoDeleteResponse.ExtraFields.OriginalModelRequested = originalModelRequested
		r.VideoDeleteResponse.ExtraFields.ResolvedModelUsed = resolvedModel
	case r.FileUploadResponse != nil:
		r.FileUploadResponse.ExtraFields.RequestType = requestType
		r.FileUploadResponse.ExtraFields.Provider = provider
		r.FileUploadResponse.ExtraFields.OriginalModelRequested = originalModelRequested
		r.FileUploadResponse.ExtraFields.ResolvedModelUsed = resolvedModel
	case r.FileListResponse != nil:
		r.FileListResponse.ExtraFields.RequestType = requestType
		r.FileListResponse.ExtraFields.Provider = provider
		r.FileListResponse.ExtraFields.OriginalModelRequested = originalModelRequested
		r.FileListResponse.ExtraFields.ResolvedModelUsed = resolvedModel
	case r.FileRetrieveResponse != nil:
		r.FileRetrieveResponse.ExtraFields.RequestType = requestType
		r.FileRetrieveResponse.ExtraFields.Provider = provider
		r.FileRetrieveResponse.ExtraFields.OriginalModelRequested = originalModelRequested
		r.FileRetrieveResponse.ExtraFields.ResolvedModelUsed = resolvedModel
	case r.FileDeleteResponse != nil:
		r.FileDeleteResponse.ExtraFields.RequestType = requestType
		r.FileDeleteResponse.ExtraFields.Provider = provider
		r.FileDeleteResponse.ExtraFields.OriginalModelRequested = originalModelRequested
		r.FileDeleteResponse.ExtraFields.ResolvedModelUsed = resolvedModel
	case r.FileContentResponse != nil:
		r.FileContentResponse.ExtraFields.RequestType = requestType
		r.FileContentResponse.ExtraFields.Provider = provider
		r.FileContentResponse.ExtraFields.OriginalModelRequested = originalModelRequested
		r.FileContentResponse.ExtraFields.ResolvedModelUsed = resolvedModel
	case r.BatchCreateResponse != nil:
		r.BatchCreateResponse.ExtraFields.RequestType = requestType
		r.BatchCreateResponse.ExtraFields.Provider = provider
		r.BatchCreateResponse.ExtraFields.OriginalModelRequested = originalModelRequested
		r.BatchCreateResponse.ExtraFields.ResolvedModelUsed = resolvedModel
	case r.BatchListResponse != nil:
		r.BatchListResponse.ExtraFields.RequestType = requestType
		r.BatchListResponse.ExtraFields.Provider = provider
		r.BatchListResponse.ExtraFields.OriginalModelRequested = originalModelRequested
		r.BatchListResponse.ExtraFields.ResolvedModelUsed = resolvedModel
	case r.BatchRetrieveResponse != nil:
		r.BatchRetrieveResponse.ExtraFields.RequestType = requestType
		r.BatchRetrieveResponse.ExtraFields.Provider = provider
		r.BatchRetrieveResponse.ExtraFields.OriginalModelRequested = originalModelRequested
		r.BatchRetrieveResponse.ExtraFields.ResolvedModelUsed = resolvedModel
	case r.BatchCancelResponse != nil:
		r.BatchCancelResponse.ExtraFields.RequestType = requestType
		r.BatchCancelResponse.ExtraFields.Provider = provider
		r.BatchCancelResponse.ExtraFields.OriginalModelRequested = originalModelRequested
		r.BatchCancelResponse.ExtraFields.ResolvedModelUsed = resolvedModel
	case r.BatchDeleteResponse != nil:
		r.BatchDeleteResponse.ExtraFields.RequestType = requestType
		r.BatchDeleteResponse.ExtraFields.Provider = provider
		r.BatchDeleteResponse.ExtraFields.OriginalModelRequested = originalModelRequested
		r.BatchDeleteResponse.ExtraFields.ResolvedModelUsed = resolvedModel
	case r.BatchResultsResponse != nil:
		r.BatchResultsResponse.ExtraFields.RequestType = requestType
		r.BatchResultsResponse.ExtraFields.Provider = provider
		r.BatchResultsResponse.ExtraFields.OriginalModelRequested = originalModelRequested
		r.BatchResultsResponse.ExtraFields.ResolvedModelUsed = resolvedModel
	case r.ContainerCreateResponse != nil:
		r.ContainerCreateResponse.ExtraFields.RequestType = requestType
		r.ContainerCreateResponse.ExtraFields.Provider = provider
		r.ContainerCreateResponse.ExtraFields.OriginalModelRequested = originalModelRequested
		r.ContainerCreateResponse.ExtraFields.ResolvedModelUsed = resolvedModel
	case r.ContainerListResponse != nil:
		r.ContainerListResponse.ExtraFields.RequestType = requestType
		r.ContainerListResponse.ExtraFields.Provider = provider
		r.ContainerListResponse.ExtraFields.OriginalModelRequested = originalModelRequested
		r.ContainerListResponse.ExtraFields.ResolvedModelUsed = resolvedModel
	case r.ContainerRetrieveResponse != nil:
		r.ContainerRetrieveResponse.ExtraFields.RequestType = requestType
		r.ContainerRetrieveResponse.ExtraFields.Provider = provider
		r.ContainerRetrieveResponse.ExtraFields.OriginalModelRequested = originalModelRequested
		r.ContainerRetrieveResponse.ExtraFields.ResolvedModelUsed = resolvedModel
	case r.ContainerDeleteResponse != nil:
		r.ContainerDeleteResponse.ExtraFields.RequestType = requestType
		r.ContainerDeleteResponse.ExtraFields.Provider = provider
		r.ContainerDeleteResponse.ExtraFields.OriginalModelRequested = originalModelRequested
		r.ContainerDeleteResponse.ExtraFields.ResolvedModelUsed = resolvedModel
	case r.ContainerFileCreateResponse != nil:
		r.ContainerFileCreateResponse.ExtraFields.RequestType = requestType
		r.ContainerFileCreateResponse.ExtraFields.Provider = provider
		r.ContainerFileCreateResponse.ExtraFields.OriginalModelRequested = originalModelRequested
		r.ContainerFileCreateResponse.ExtraFields.ResolvedModelUsed = resolvedModel
	case r.ContainerFileListResponse != nil:
		r.ContainerFileListResponse.ExtraFields.RequestType = requestType
		r.ContainerFileListResponse.ExtraFields.Provider = provider
		r.ContainerFileListResponse.ExtraFields.OriginalModelRequested = originalModelRequested
		r.ContainerFileListResponse.ExtraFields.ResolvedModelUsed = resolvedModel
	case r.ContainerFileRetrieveResponse != nil:
		r.ContainerFileRetrieveResponse.ExtraFields.RequestType = requestType
		r.ContainerFileRetrieveResponse.ExtraFields.Provider = provider
		r.ContainerFileRetrieveResponse.ExtraFields.OriginalModelRequested = originalModelRequested
		r.ContainerFileRetrieveResponse.ExtraFields.ResolvedModelUsed = resolvedModel
	case r.ContainerFileContentResponse != nil:
		r.ContainerFileContentResponse.ExtraFields.RequestType = requestType
		r.ContainerFileContentResponse.ExtraFields.Provider = provider
		r.ContainerFileContentResponse.ExtraFields.OriginalModelRequested = originalModelRequested
		r.ContainerFileContentResponse.ExtraFields.ResolvedModelUsed = resolvedModel
	case r.ContainerFileDeleteResponse != nil:
		r.ContainerFileDeleteResponse.ExtraFields.RequestType = requestType
		r.ContainerFileDeleteResponse.ExtraFields.Provider = provider
		r.ContainerFileDeleteResponse.ExtraFields.OriginalModelRequested = originalModelRequested
		r.ContainerFileDeleteResponse.ExtraFields.ResolvedModelUsed = resolvedModel
	case r.OCRResponse != nil:
		r.OCRResponse.ExtraFields.RequestType = requestType
		r.OCRResponse.ExtraFields.Provider = provider
		r.OCRResponse.ExtraFields.OriginalModelRequested = originalModelRequested
		r.OCRResponse.ExtraFields.ResolvedModelUsed = resolvedModel
	case r.PassthroughResponse != nil:
		r.PassthroughResponse.ExtraFields.RequestType = requestType
		r.PassthroughResponse.ExtraFields.Provider = provider
		r.PassthroughResponse.ExtraFields.OriginalModelRequested = originalModelRequested
		r.PassthroughResponse.ExtraFields.ResolvedModelUsed = resolvedModel
	case r.CachedContentCreateResponse != nil:
		r.CachedContentCreateResponse.ExtraFields.RequestType = requestType
		r.CachedContentCreateResponse.ExtraFields.Provider = provider
		r.CachedContentCreateResponse.ExtraFields.OriginalModelRequested = originalModelRequested
		r.CachedContentCreateResponse.ExtraFields.ResolvedModelUsed = resolvedModel
	case r.CachedContentListResponse != nil:
		r.CachedContentListResponse.ExtraFields.RequestType = requestType
		r.CachedContentListResponse.ExtraFields.Provider = provider
		r.CachedContentListResponse.ExtraFields.OriginalModelRequested = originalModelRequested
		r.CachedContentListResponse.ExtraFields.ResolvedModelUsed = resolvedModel
	case r.CachedContentRetrieveResponse != nil:
		r.CachedContentRetrieveResponse.ExtraFields.RequestType = requestType
		r.CachedContentRetrieveResponse.ExtraFields.Provider = provider
		r.CachedContentRetrieveResponse.ExtraFields.OriginalModelRequested = originalModelRequested
		r.CachedContentRetrieveResponse.ExtraFields.ResolvedModelUsed = resolvedModel
	case r.CachedContentUpdateResponse != nil:
		r.CachedContentUpdateResponse.ExtraFields.RequestType = requestType
		r.CachedContentUpdateResponse.ExtraFields.Provider = provider
		r.CachedContentUpdateResponse.ExtraFields.OriginalModelRequested = originalModelRequested
		r.CachedContentUpdateResponse.ExtraFields.ResolvedModelUsed = resolvedModel
	case r.CachedContentDeleteResponse != nil:
		r.CachedContentDeleteResponse.ExtraFields.RequestType = requestType
		r.CachedContentDeleteResponse.ExtraFields.Provider = provider
		r.CachedContentDeleteResponse.ExtraFields.OriginalModelRequested = originalModelRequested
		r.CachedContentDeleteResponse.ExtraFields.ResolvedModelUsed = resolvedModel
	}
}

// BifrostMCPResponse is the envelope for MCP responses that flow through the generic
// PostMCPHook pipeline (Ping, ListTools, ExecuteTool variants). Connect responses do
// NOT use this envelope — they are dispatched via the typed MCPConnectionPlugin
// interface using *BifrostMCPConnectResponse directly.
//
// Exactly one of the embedded sub-response pointers is populated, matched by the
// originating request's RequestType. ExtraFields (ClientName / ToolName / Latency)
// applies to all envelope variants. For execute-tool requests in the back-compat
// window, the direct ChatMessage / ResponsesMessage fields are populated instead of
// any embedded sub-response.
type BifrostMCPResponse struct {
	*BifrostMCPPingResponse
	*BifrostMCPListToolsResponse

	// [DEPRECATED] back-compat fields for execute-tool requests; will move into
	// BifrostMCPExecuteToolResponse in the next major bump.
	ChatMessage      *ChatMessage
	ResponsesMessage *ResponsesMessage

	// Empty stub today; will hold ChatMessage/ResponsesMessage in the next major bump.
	*BifrostMCPExecuteToolResponse

	ExtraFields BifrostMCPResponseExtraFields
}

// Latency for envelope MCP responses (ping, list_tools, execute_tool) is reported via
// BifrostMCPResponse.ExtraFields.Latency (milliseconds). Connect carries its own
// ExtraFields on BifrostMCPConnectResponse below — see typed Connect path.

type BifrostMCPConnectResponse struct {
	ConnectionInfo     *MCPClientConnectionInfo // Connection metadata after the handshake completes
	ServerInfo         *MCPServerInfo           // Name + version from the initialize handshake
	ProtocolVersion    string                   // Negotiated MCP protocol version
	ServerCapabilities *MCPServerCapabilities   // Which MCP feature groups the server claims to support
	ExtraFields        BifrostMCPResponseExtraFields
}

// PopulateExtraFields backfills ClientName on the Connect response when it's not
// already set. Mirrors BifrostMCPResponse.PopulateExtraFields. Connect has no tool
// name, so only ClientName is populated.
func (r *BifrostMCPConnectResponse) PopulateExtraFields(clientName string) {
	if r == nil {
		return
	}
	if r.ExtraFields.ClientName == "" {
		r.ExtraFields.ClientName = clientName
	}
}

// MCPServerInfo mirrors the ServerInfo portion of the MCP initialize handshake.
type MCPServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// MCPServerCapabilities mirrors the high-level capability flags from the MCP initialize handshake.
// Only the booleans Bifrost cares about today; can grow as needed.
type MCPServerCapabilities struct {
	Tools     bool `json:"tools"`     // server supports tools/list + tools/call
	Resources bool `json:"resources"` // server supports resources
	Prompts   bool `json:"prompts"`   // server supports prompts
	Logging   bool `json:"logging"`   // server supports logging
}

type BifrostMCPPingResponse struct{}

type BifrostMCPListToolsResponse struct {
	Tools           map[string]ChatTool // Discovered tools keyed by client-prefixed name
	ToolNameMapping map[string]string   // sanitized_name -> original_mcp_name
	RawToolCount    int                 // Count returned by the MCP server before Bifrost-side filtering
	SkippedTools    []SkippedMCPTool    // Tools Bifrost dropped during conversion + reason
}

// SkippedMCPTool describes a tool that the MCP server returned but Bifrost did not include
// in the final tool map (e.g. invalid normalized name).
type SkippedMCPTool struct {
	OriginalName string `json:"original_name"`
	Reason       string `json:"reason"`
}

// Keeping the stub for now, will be used from the next major bump when we move ChatMessage
// and ResponsesMessage into this struct.
type BifrostMCPExecuteToolResponse struct{}

// PopulateExtraFields backfills ExtraFields.{MCPRequestType, ClientName, ToolName}
// when they aren't already set on the response. Mirrors BifrostResponse.PopulateExtraFields
// and is used by every MCP gate to ensure short-circuit responses carry the same
// attribution as real wire-call responses.
func (r *BifrostMCPResponse) PopulateExtraFields(mcpRequestType MCPRequestType, clientName, toolName string) {
	if r == nil {
		return
	}
	if r.ExtraFields.MCPRequestType == "" {
		r.ExtraFields.MCPRequestType = mcpRequestType
	}
	if r.ExtraFields.ClientName == "" {
		r.ExtraFields.ClientName = clientName
	}
	if r.ExtraFields.ToolName == "" {
		r.ExtraFields.ToolName = toolName
	}
}

// BifrostResponseExtraFields contains additional fields in a response.
type BifrostResponseExtraFields struct {
	RequestType RequestType `json:"request_type"`
	RoutingInfo RoutingInfo `json:"routing_info"`
	// Deprecated: use RoutingInfo.Provider. Still populated for backward
	// compatibility; new consumers should read from RoutingInfo.
	Provider ModelProvider `json:"provider,omitempty"`
	// Deprecated: use RoutingInfo.PrimaryModel when RoutingInfo.IsFallback
	// is true, otherwise RoutingInfo.Model — both branches collapse to the
	// model string the caller sent in the request. Still populated for
	// backward compatibility; new consumers should read from RoutingInfo.
	OriginalModelRequested string `json:"original_model_requested,omitempty"`
	// Deprecated: use RoutingInfo.ResolvedKeyAlias.ModelID when an alias
	// matched (i.e. RoutingInfo.ResolvedKeyAlias != nil), otherwise
	// RoutingInfo.Model. Still populated for backward compatibility; new
	// consumers should read from RoutingInfo.
	ResolvedModelUsed         string             `json:"resolved_model_used,omitempty"`
	Latency                   int64              `json:"latency"`     // in milliseconds (for streaming responses this will be each chunk latency, and the last chunk latency will be the total latency)
	ChunkIndex                int                `json:"chunk_index"` // used for streaming responses to identify the chunk index, will be 0 for non-streaming responses
	RawRequest                interface{}        `json:"raw_request,omitempty"`
	RawResponse               interface{}        `json:"raw_response,omitempty"`
	CacheDebug                *BifrostCacheDebug `json:"cache_debug,omitempty"`
	ParseErrors               []BatchError       `json:"parse_errors,omitempty"` // errors encountered while parsing JSONL batch results
	ConvertedRequestType      RequestType        `json:"converted_request_type,omitempty"`
	DroppedCompatPluginParams []string           `json:"dropped_compat_plugin_params,omitempty"` // params dropped by the compat plugin based on model catalog
	ProviderResponseHeaders   map[string]string  `json:"provider_response_headers,omitempty"`    // HTTP response headers from the provider (filtered to exclude transport-level headers)
	PassthroughPath           string             `json:"passthrough_path,omitempty"`             // Stripped provider path for passthrough requests, e.g. "/v1/chat/completions"
}

type RoutingInfo struct {
	// What actually handled this attempt
	Provider ModelProvider `json:"provider,omitempty"`
	Model    string        `json:"model,omitempty"` // model name passed to this attempt's key
	Key      string        `json:"key,omitempty"`   // KeyName of the key used

	// Populated only when Model matched an entry in this key's Aliases map
	ResolvedKeyAlias *ResolvedKeyAlias `json:"resolved_key_alias,omitempty"`

	IsFallback bool `json:"is_fallback,omitempty"`

	// What the caller asked for, before any fallback resolution (populated only when fallback resolution occurred)
	PrimaryProvider *ModelProvider `json:"primary_provider,omitempty"`
	PrimaryModel    *string        `json:"primary_model,omitempty"`

	// ServerSideFallbackModel names the model that actually produced the response
	// when the provider swapped models *inside* a single upstream call — today only
	// Anthropic's server-side fallback (server-side-fallback-2026-06-01). Model
	// still names what the caller asked for, since routing never saw the swap.
	//
	// Unlike every other field here this one is provider-owned, not written by the
	// orchestrator: only the provider can see a handoff that happened within its own
	// response. PopulateRoutingInfo preserves it across core's overwrite. Nil on
	// every ordinary response, so pricing behaviour is unchanged when it is absent.
	ServerSideFallbackModel *string `json:"server_side_fallback_model,omitempty"`
}

type ResolvedKeyAlias struct {
	ModelID     string       `json:"model_id"`               // wire model identifier actually sent to the provider
	ModelName   *string      `json:"model_name,omitempty"`   // canonical name (used for pricing/logs)
	ModelFamily *ModelFamily `json:"model_family,omitempty"` // resolved family for routing
}

type BifrostMCPResponseExtraFields struct {
	MCPRequestType MCPRequestType `json:"mcp_request_type"` // request type this response corresponds to — lets PostMCPHook discriminate ping/list_tools from tool execute on success too
	ClientName     string         `json:"client_name"`
	ToolName       string         `json:"tool_name"` // empty for all but MCPRequestTypeExecuteTool requests for backwards compat, will be a pointer from next major bump.
	Latency        int64          `json:"latency"`   // in milliseconds
}

// BifrostCacheDebug represents debug information about the cache.
type BifrostCacheDebug struct {
	CacheHit bool `json:"cache_hit"`

	CacheID *string `json:"cache_id,omitempty"`
	HitType *string `json:"hit_type,omitempty"`

	RequestedProvider *string `json:"requested_provider,omitempty"`
	RequestedModel    *string `json:"requested_model,omitempty"`

	// Semantic cache only (provider, model, and input tokens will be present for semantic cache, even if cache is not hit)
	ProviderUsed *string `json:"provider_used,omitempty"`
	ModelUsed    *string `json:"model_used,omitempty"`
	InputTokens  *int    `json:"input_tokens,omitempty"`

	// Semantic cache only (only when cache is hit)
	Threshold  *float64 `json:"threshold,omitempty"`
	Similarity *float64 `json:"similarity,omitempty"`

	// CacheHitLatency is the time in milliseconds spent serving the cache hit
	// (lookup + response build). Only set when CacheHit is true.
	CacheHitLatency *int64 `json:"cache_hit_latency,omitempty"`
}

const (
	RequestCancelled         = "request_cancelled"
	RequestTimedOut          = "request_timed_out"
	RequestDropped           = "request_dropped"
	ProviderConnectionFailed = "provider_connection_failed"
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

	// Response carries the full upstream Response object for a mid-stream
	// OpenAI Responses-API "response.failed" event, so the client-facing SSE
	// payload matches OpenAI's real wire shape (a "response" object, not a
	// wrapped error) instead of being flattened into just .Error. Nil/omitted
	// for every other error and provider — populated only by the OpenAI
	// Responses-stream handler, and only for the native OpenAI provider.
	Response *BifrostResponsesResponse `json:"response,omitempty"`

	// SequenceNumber mirrors the upstream response.failed event's own
	// sequence_number. Only meaningful (and only set) alongside Response.
	SequenceNumber *int `json:"-"`
}

// MarshalJSON implements custom JSON marshaling for BifrostError.
// When Response is set (currently only the OpenAI Responses-stream
// response.failed case), the client-facing payload is OpenAI's own
// "response.failed" event shape (type/response/sequence_number) instead of
// Bifrost's generic wrapped-error envelope — the official SDK's typed
// parsing for that event expects a "response" object, not .error/.extra_fields.
// Every other error (Response nil) marshals exactly as before.
func (e BifrostError) MarshalJSON() ([]byte, error) {
	if e.Response != nil {
		type responseFailedShape struct {
			Type           *string                   `json:"type,omitempty"`
			Response       *BifrostResponsesResponse `json:"response,omitempty"`
			SequenceNumber *int                      `json:"sequence_number,omitempty"`
		}
		return MarshalSorted(&responseFailedShape{
			Type:           e.Type,
			Response:       e.Response,
			SequenceNumber: e.SequenceNumber,
		})
	}
	type Alias BifrostError
	return MarshalSorted((Alias)(e))
}

// PopulateExtraFields sets RequestType, Provider, OriginalModelRequested, and ResolvedModelUsed on the
// error's ExtraFields. Core always calls this both before and after RunPostLLMHooks, so any plugin
// modifications to these 4 fields are no-ops — tampering with them inside plugins is discouraged.
func (e *BifrostError) PopulateExtraFields(requestType RequestType, provider ModelProvider, originalModelRequested string, resolvedModelUsed string) {
	if e == nil {
		return
	}
	e.ExtraFields.RequestType = requestType
	e.ExtraFields.Provider = provider
	e.ExtraFields.OriginalModelRequested = originalModelRequested
	if resolvedModelUsed != "" {
		e.ExtraFields.ResolvedModelUsed = resolvedModelUsed
	} else {
		e.ExtraFields.ResolvedModelUsed = originalModelRequested
	}
}

// String renders the error as JSON for logging and test diagnostics.
// Without this, fmt's reflection printer walks ExtraFields.RawRequest /
// RawResponse (which typically hold json.RawMessage = []byte) and dumps
// every byte as a decimal, producing unreadable output.
func (e *BifrostError) String() string {
	if e == nil {
		return "<nil>"
	}
	b, err := MarshalSorted(e)
	if err != nil {
		return fmt.Sprintf("BifrostError{marshal_err=%v}", err)
	}
	return string(b)
}

func (e *BifrostError) GetErrorString() string {
	if e == nil {
		return ""
	}
	if e.Error != nil && e.Error.Message != "" {
		return e.Error.Message
	} else if e.StatusCode != nil {
		switch *e.StatusCode {
		case 401:
			return "unauthorized"
		case 403:
			return "forbidden"
		case 404:
			return "endpoint not found"
		case 405:
			return "method not allowed"
		case 429:
			return "rate limit exceeded"
		case 500:
			return "internal server error"
		case 502:
			return "bad gateway"
		case 503:
			return "service unavailable"
		case 504:
			return "gateway timeout"
		default:
			if e.Error != nil && e.Error.Message != "" {
				return e.Error.Message
			}
			return fmt.Sprintf("HTTP %d error", *e.StatusCode)
		}
	} else if e.Type != nil {
		return *e.Type
	} else {
		return "unknown error"
	}
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
	RoutingInfo RoutingInfo `json:"routing_info"`
	// Deprecated: use RoutingInfo.Provider. Still populated for backward
	// compatibility; new consumers should read from RoutingInfo.
	Provider ModelProvider `json:"provider,omitempty"`
	// Deprecated: use RoutingInfo.PrimaryModel when RoutingInfo.IsFallback
	// is true, otherwise RoutingInfo.Model — both branches collapse to the
	// model string the caller sent in the request. Still populated for
	// backward compatibility; new consumers should read from RoutingInfo.
	OriginalModelRequested string `json:"original_model_requested,omitempty"`
	// Deprecated: use RoutingInfo.ResolvedKeyAlias.ModelID when an alias
	// matched (i.e. RoutingInfo.ResolvedKeyAlias != nil), otherwise
	// RoutingInfo.Model. Still populated for backward compatibility; new
	// consumers should read from RoutingInfo.
	ResolvedModelUsed         string                `json:"resolved_model_used,omitempty"`
	RequestType               RequestType           `json:"request_type,omitempty"`
	MCPRequestType            MCPRequestType        `json:"mcp_request_type,omitempty"`
	RawRequest                interface{}           `json:"raw_request,omitempty"`
	RawResponse               interface{}           `json:"raw_response,omitempty"`
	ConvertedRequestType      RequestType           `json:"converted_request_type,omitempty"`
	DroppedCompatPluginParams []string              `json:"dropped_compat_plugin_params,omitempty"`
	Latency                   int64                 `json:"latency,omitempty"` // in milliseconds
	KeyStatuses               []KeyStatus           `json:"key_statuses,omitempty"`
	MCPAuthRequired           *MCPAuthRequiredError `json:"mcp_auth_required,omitempty"` // Set when a per-user MCP tool requires the caller to complete an inline auth flow (OAuth or headers)
	// BilledUsage carries provider-reported token usage that was consumed even
	// though the request ultimately failed or was cancelled (e.g. a stream
	// aborted mid-response, or a 5xx returned after input tokens were
	// processed). Providers populate it on cancel/error paths so downstream
	// post-LLM hooks (governance billing, logging cost) can charge for tokens
	// the provider actually billed us for. Nil when the failure consumed no
	// tokens (e.g. 401/403/429 before the model ran).
	BilledUsage *BifrostLLMUsage `json:"billed_usage,omitempty"`
}