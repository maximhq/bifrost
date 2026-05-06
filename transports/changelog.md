<Warning>
**v1.5.0 contains multiple breaking changes.** See the **[v1.5.0 Migration Guide](/migration-guides/v1.5.0)** for full before/after examples, automatic migration details, and a step-by-step checklist before upgrading.
</Warning>

## ✨ Features

### Providers & models

- **Claude Opus 4.7** - Compatibility for Anthropic's Claude Opus 4.7 model, including adaptive thinking, task-budgets beta header, `display` parameter handling, and "xhigh" effort mapping
- **Anthropic Structured Outputs** - `response_format` and structured-output support across chat completions and Responses API, with order-preserving merge of additional model request fields (thanks [@emirhanmutlu-natuvion](https://github.com/emirhanmutlu-natuvion)!)
- **Anthropic Server Tools** - Surface server-side tools (web search, code execution, computer use containers) end-to-end across Anthropic chat schema and Responses converters
- **Anthropic Computer Use** - Cross-provider parity fixes for Anthropic computer use across Bedrock, Vertex, and Anthropic
- **Fireworks AI Provider** - Add Fireworks AI as a first-class provider with native completions, responses, embeddings, and image generations (thanks [@ivanetchart](https://github.com/ivanetchart)!)
- **StabilityAI on Bedrock** - StabilityAI image generation through the Bedrock provider
- **Bedrock Embeddings & Image Gen** - Embeddings, image generation, image edit, and variation support on Bedrock
- **Bedrock Structured-Output Fallback** - Synthetic structured-output tool fallback for Bedrock Converse API
- **Azure Container API** - Azure provider now supports the container API for code-execution / computer-use scenarios
- **Azure Passthrough** - Native Azure passthrough support for Responses, chat completions, embeddings, and audio
- **Gemini Named Content Cache** - Named content cache support on Gemini
- **Realtime Support** - WebSocket, WebRTC, and client-secret handlers with session state management and transport-context helpers; OpenAI Realtime audio base64 encoding (thanks [@Mahmoud-Khater](https://github.com/Mahmoud-Khater)!)
- **OCR Request Support** - First-class OCR request type with stream terminal detection, full body accumulation for passthrough streams, input logging with detail view, and per-request pricing
- **vLLM / SGL Compatibility** - Extra-body params (`chat_template_kwargs`, `guided_json`, `guided_regex`, `separate_reasoning`) flow through vLLM and SGL via `BifrostContextKeyPassthroughExtraParams` (thanks [@hensapir](https://github.com/hensapir)!)

### MCP

- **MCP Tool Groups** - `tool_groups` config with governance scoping (virtual key, team, customer, user, provider, API key) and camelCase Helm aliases for MCP client fields
- **MCP Tool Annotations** - Preserve `title`, `readOnly`, `destructive`, `idempotent`, `openWorld` annotations across bidirectional conversion so agents can reason about tool behavior
- **MCP Reverse Proxy OAuth** - External base-URL support for reverse-proxy MCP OAuth flows; later split into separate server and client URL fields for clearer reverse-proxy configuration
- **MCP Tool Discovery** - Discovered tools and tool-name mapping columns added to MCP clients
- **MCP Per-Tool Access Control** - Virtual-key MCP configs now act as an execution-time allow-list; tools not permitted by the VK are blocked at inference and MCP tool execution
- **MCP Disable Auto Tool Inject** - Per-request opt-out via `MCPToolManagerConfig` and `BifrostContextKeyMCPAddedTools` tracking
- **MCP Header Filters** - `x-bf-mcp-include-clients` and `x-bf-mcp-include-tools` request headers filter the MCP `tools/list` response when Bifrost runs as an MCP gateway
- **MCP Request-Level Headers** - Per-request extra headers on MCP tool execution via `BifrostContextKeyMCPExtraHeaders`
- **MCP Duration Strings + Hash Reconciliation** - `tool_sync_interval` accepts Go duration strings; hash-based reconciliation prevents unnecessary MCP client restarts on config reload
- **MCP OAuth Edit** - Ability to edit pre-existing MCP OAuth details
- **MCP `disabled` Toggle** - `disabled` field on MCP clients for toggling connection without removing the config
- **MCP OAuth `EnvVar` Refs** - `client_id` and `client_secret` accept `EnvVar` references for secret injection
- **MCP Clients on All VKs** - Option to allow MCP clients to run on all virtual keys without explicit assignment

### Governance, RBAC & teams

- **Access Profiles** - Fine-grained permission control via access profiles, seedable declaratively from `config.json` and Helm values (provider restrictions, model allowlists, budgets, rate limits, MCP server/tool controls)
- **Team Budgets** - Per-team spending tracking with atomic rate-limit updates, DB tables, and `business_units`, `team_id`, `calendar_aligned`, `virtual_key_count` fields in governance schema and Helm
- **Granular RBAC** - Replaced the single `Governance` RBAC check with granular per-resource permissions; enforcement on routing rules (view/edit/create), model provider create/update, and MCP tool groups routes
- **Direct Key Bypass Removed** - Removed direct key bypass from HTTP gateway and Go SDK; all keys now flow through governance
- **Unique Team Names** - Enforce unique `governance_teams.name` with deduplication migration
- **`GetTeamByName`** - Config store interface and RDB now support team lookup by name

### Routing, logging & observability

- **Auto-Resolve Provider** - Inference and integration routes now auto-resolve the provider when no provider prefix is given on the model name
- **Auto-fill Incoming Model for Fallbacks** - Routing rule fallback entries can omit the model; the incoming request model is substituted automatically at runtime
- **Self-Looping Chain Rules** - Chain rules with self-loops continue evaluating subsequent rules instead of halting
- **Routing Rules Scope Cache** - Cache routing rules per scope upfront, plus model-catalog routing engine label and icon
- **Per-Request Content Logging Overrides** - Opt-in per-request overrides for content logging and raw request/response visibility, with DB migrations and live-reload
- **Unified Dimension Headers (`x-bf-dim-*`)** - Forwarded automatically to logs, traces, Prometheus, and Maxim tags
- **Logging Tracking Fields** - Track `userId`, `teamId`, `customerId`, and `businessUnitId` across logging, Maxim, and OTEL
- **`parent_request_id`, `user_ids`, `aliases` URL State** - Propagated through logs and traces for cross-request correlation
- **Trace Attribute Flow** - Custom trace attributes flow through the OTEL exporter
- **Finish Reasons in OTEL Root Spans** - Finish reasons added to root spans, with correct model and provider names propagated
- **Local Cache Hit Rate Speedometer** - Dashboard speedometer showing local cache hit rate (thanks [@loss-and-quick](https://github.com/loss-and-quick)!)
- **Single Log Export** - Export individual log entries from the logs view and MCP logs sheet
- **Virtual Keys CSV Export** - Sorting and CSV export from the virtual keys table
- **Period Parameter** - `period` param for relative time-range queries on dashboard and logs endpoints
- **Passthrough Streaming Accumulation** - Accumulator for passthrough streaming responses, enabling proper logging and cost tracking on raw provider streams
- **VK-Scoped Model Lists** - Model list endpoints scoped to virtual-key-allowed providers and models via request headers
- **`objectStorageExcludeFields`** - Configurable list of log payload fields that stay in the database instead of being offloaded to object storage

### Pricing

- **Pricing Overrides** - Scoped pricing overrides per provider/key/model
- **272k Token Tier** - 272k token tier pricing support
- **Flex / Priority Tiers** - Flex and priority tier pricing with `service_tier`-based selection
- **Cache Creation Pricing** - 5-minute and 1-hour TTL pricing tiers for Claude cache creation
- **Container Creation Cost** - Per-request container creation cost support

### Configuration & deploy

- **Dedicated Provider Keys API** - Keys are managed via `/api/providers/{provider}/keys` endpoints instead of being embedded in provider create/update payloads
- **`key_ids` Wildcard** - VK provider config `key_ids` supports `["*"]` to allow all keys; empty `key_ids` denies all
- **Empty-Array Conventions** - `[]` means deny-all, `["*"]` means allow-all across models, tools, and keys
- **Deny-by-Default Virtual Keys** - VK provider and MCP configs block all access when empty; automatic migration backfills existing keys to preserve behavior
- **Model Alias** - Map model names to provider-specific identifiers (deployment names, inference profile ARNs, fine-tuned IDs, custom names) via per-key alias config
- **`provider_key_name` Alias** - Human-readable alias for routing targets and pricing overrides, resolved to `key_id` at config load
- **`env.*` References for Proxy and TLS** - `url`, `username`, `password`, `ca_cert_pem` accept `env.VAR_NAME` for secret injection
- **`schemas.Duration`** - Go duration string support for MCP, Redis, Weaviate, and mocker duration fields
- **Path Whitelisting** - Path whitelisting from security config
- **Server Bootstrap Timer** - Startup diagnostics
- **Plugin Trace-Level Logging** - Plugins can inject logs at trace level via `ctx.Log(schemas.LogLevelInfo, ...)`
- **Per-User OAuth Consent** - Per-user OAuth consent flow with identity selection and MCP authentication; OAuth server selection and validation per-user in codemode
- **Prompts Plugin** - New prompts plugin with direct key header resolver and selective message inclusion when committing prompt sessions
- **EnvVar Improvements** - `IsSet` method on `EnvVar` and auto-redaction of env-backed values in JSON serialization
- **Optional `tx` in `DeleteVirtualKey`** - External transactions can drive VK deletion atomically
- **Trial Expiry Banner** - Sticky trial-expiry banner replacing the sidebar indicator

### Helm & enterprise

- **Enterprise Helm Overlays** - Composable overlays for guardrails, org governance, access profiles, customer budgets, teams, multi-customer governance, and SCIM/SSO
- **Semantic Cache Helm Layers** - `values-semantic-search-redis.yaml` and `values-semantic-search-weaviate.yaml` plus a client-config overlay
- **Key IDs in Helm** - `key_ids` is now the preferred field for pinning provider keys in Helm virtual key configurations

## 🐞 Fixed

### Providers

- **Provider Queue Shutdown Panic** - Eliminated `send on closed channel` panics by leaving channels open and exiting workers via the done signal; stale producers transparently re-route to new queues during `UpdateProvider`
- **Provider Update Stalls** - Avoid provider update stalls under high load
- **Keyless Provider Reload** - Broadcast provider config changes to cluster for keyless providers; provider runtime reloads correctly after key creation
- **Default Routing Provider Filter** - Filter out unconfigured providers in default routing
- **Custom Providers** - Custom providers without a list-models endpoint accept any model rather than restricting on virtual-key registration
- **OpenAI Tool Result Output** - Flatten array-form `tool_result` output into a newline-joined string for the Responses API so strict upstreams (Ollama Cloud, openai-go typed models) no longer reject with HTTP 400 (thanks [@martingiguere](https://github.com/martingiguere)!)
- **OpenAI Responses Tool Fields** - Preserve tool fields in OpenAI responses (thanks [@princepal9120](https://github.com/princepal9120)!)
- **OpenAI Transcription Formats** - Handle `text`, `vtt`, and `srt` response formats in OpenAI transcription response
- **Anthropic WebSearch** - Removed the Claude Code user agent restriction so WebSearch tool arguments flow for all clients
- **Anthropic Request Fallbacks** - Dropped fallback fields from outgoing Anthropic requests to avoid schema validation errors
- **Anthropic Empty Thinking Block** - Drop empty thinking block for Anthropic provider on Claude Code
- **Anthropic Integration Routing** - Skip model catalog routing when loadbalancer or governance routing has already selected the provider
- **Bedrock Streaming** - Emit `message_stop` event for Anthropic invoke stream and case-insensitive `anthropic-beta` header merging (thanks [@tefimov](https://github.com/tefimov)!)
- **Bedrock Streaming Retries** - Retry retryable AWS exceptions and stale/closed-connection errors
- **Bedrock Tool Choice** - Convert tool choice to `auto` correctly
- **Bedrock SigV4 Service** - Correct SigV4 service name for agent-runtime rerank
- **Bedrock Tool Images** - Preserve image content blocks in tool results when converting Anthropic Messages to Bedrock Converse API (thanks [@Edward-Upton](https://github.com/Edward-Upton)!)
- **Bedrock Structured-Output Streaming** - Suppress non-tool content events (text deltas, reasoning, non-tool content-block starts) when structured output mode is active, preventing prose from corrupting the assembled JSON
- **Bedrock Llama `toolChoice.tool`** - Omit `toolChoice.tool` on Meta Llama variants under Bedrock Converse to satisfy upstream rejection (thanks [@ryan-orphic](https://github.com/ryan-orphic)!)
- **Vertex Endpoint** - Vertex endpoint correction
- **Vertex `google/` Prefix** - Strip `google/` prefix from Vertex model IDs across all request types
- **Vertex Multi-Region Routing** - Multi-region-only models now route to multi-region endpoints when the provider key is configured for a single region only
- **Gemini Tool Outputs** - Handle content block tool outputs in Responses API path for `function_call_output` messages (thanks [@tom-diacono](https://github.com/tom-diacono)!)
- **Gemini Thinking Level** - Preserve `thinkingLevel` parameters across round-trip conversions and correct finish-reason mapping
- **Gemini Thinking Budget** - Thinking budget validation for Gemini models
- **vLLM Token Usage** - Treat `delta.content=""` the same as `nil` in streaming so the synthesis chunk retains its `finish_reason`, restoring token-usage attribution in logs and UI
- **vLLM Extra Params** - Extra parameters now passed through to vLLM providers
- **PydanticAI Null Text Fields** - Normalize null text content in PydanticAI stream response chunks
- **Embedding Model Backfill** - Backfill `Model` in embedding response when provider omits it

### MCP & OAuth

- **MCP Tool Logs** - MCP tool logs are captured correctly
- **MCP Tool Field Resolution** - Resolve `tools_to_execute` and `tools_to_auto_execute` from existing config before validation in MCP client update
- **OAuth Query Params** - Preserve existing query parameters when building OAuth upstream authorize URLs
- **OAuth Token `expires_at`** - Nullable; refresh/reconnect guarded on nil expiry
- **OAuth Permanent Errors** - Only treat `invalid_grant` and `unauthorized_client` as permanent OAuth errors; transient refresh failures no longer mark configs expired
- **OAuth Per-User Reauth** - Handle per-user OAuth re-auth, refresh token expiry, and reconnection
- **OAuth Credential Rotation** - Temporarily disabled OAuth credential rotation and header reconciliation pending follow-up work
- **OAuth2 Token Source Cache** - Cache OAuth2 token source to eliminate per-request overhead
- **`oauth_client_id` / `oauth_client_secret` Validation** - Excluded from config field validation that previously rejected env-backed values
- **Per-User OAuth Codemode** - Use per-user OAuth servers in codemode

### Streaming, transport & runtime

- **Streaming Post-Hook Race** - Race where fasthttp `RequestCtx` could be recycled before transport post-hooks completed in streaming goroutines; eagerly captures request/response snapshots before the handler returns
- **Streaming Pool-Reuse Corruption** - Snapshot `RequestType` before closure to prevent pool-reuse corruption
- **Streaming Pipeline `RawRequest`** - Propagate `RawRequest` through the streaming pipeline and fix pool leak (thanks [@loss-and-quick](https://github.com/loss-and-quick)!)
- **Streaming Timeouts** - Separate streaming clients per provider to prevent read-timeout collisions
- **Streaming Latency Validation** - Allow zero-millisecond latency values (valid for sub-millisecond cache hits)
- **Streaming Error Logs** - Improved streaming error log handling (thanks [@loss-and-quick](https://github.com/loss-and-quick)!)
- **Logging Streaming Errors** - Improved streaming error handling in logging plugin (thanks [@loss-and-quick](https://github.com/loss-and-quick)!)
- **Logging Request Type** - Resolve request type from pending data before streaming to prevent missing `Object` field in error logs
- **Responses Streaming Errors** - Capture errors mid-stream in the Responses API so transport clients see failures instead of silent termination
- **Async Context Propagation** - Preserve context values in async requests so downstream handlers retain request-scoped data
- **Async User Values** - Propagate user values through all async inference handlers and job submissions
- **Async Log Store Exceptions** - Exception handling in async log store jobs
- **Trace Completer Safety** - Trace completer accepts transport logs as a parameter instead of reading from potentially recycled context
- **Trace Completion Deadlock** - SSE heartbeats and deferred trace completion to prevent deadlock
- **Plugin Timer Concurrency** - Concurrent map access in plugin timer
- **WebSocket `/responses` Reliability** - Upstream handshake diagnostics, proper error capture, WebSocket lifecycle, VK stripping, logging, and cost tracking
- **WebSocket Nil Checks** - `sendMessageSafely` nil guards, panic recovery, and client cleanup
- **WebSocket Extra Headers** - Forward extra headers on responses websocket upstreams
- **Raw Request Passthrough** - Removed `SendBackRawRequest` from all provider passthrough flows; passthrough streaming sets proper SSE headers
- **Network Config Fallback** - Fall back to network config if key-config URL is not set for Ollama and SGL
- **`base_url` Backward Compatibility** - `base_url` added to `network_config` for backward compatibility
- **`ResponseToolMessage` Namespace** - Namespace fix in `ResponseToolMessage` for cross-provider compatibility
- **Tool Execution Header** - Removed redundant static header assignment in tool-execution flow

### Governance, virtual keys & teams

- **Self-Looping Chain Rules** - Chain rules with self-loops continue evaluating subsequent rules
- **Virtual Key Configs** - VK configurations cleaned up correctly on provider changes
- **Virtual Key Management** - VK creation validation and update handling
- **Routing Rule Targets** - Preserve routing-rule targets for genai and bedrock paths
- **Routing Rule Query Normalization** - Normalize `query` field to valid `RuleGroupType` and tighten schema validation
- **Provider Budget Duplication** - Provider-level multi-line budget duplication issue
- **Governance Budgets in Model Providers** - Persisted correctly across server restarts
- **`governance_budgets` Join** - Corrected join condition to use `virtual_key_id`
- **Budget and Team Co-creation** - Fixed creation of budgets and teams in the same request
- **Access Profile Rate Limits** - Rate-limit counters for access profiles were always showing 0; now persisted correctly to the database
- **Gossip Baseline & Orphaned Rate Limits** - Add gossip baseline methods and clean up orphaned rate limits
- **Default Routing Provider Filter** - Filter out unconfigured providers in default routing
- **`after` Pagination** - Graceful error for invalid `after` values by letting upstream pagination handle them

### Caching, OTEL & telemetry

- **Semantic Cache Determinism** - Deterministic request hashing and `CacheDebug` propagation in streaming (thanks [@loss-and-quick](https://github.com/loss-and-quick)!)
- **Semanticcache Provider Keys** - Inherit provider keys from global client in semanticcache plugin
- **OTel Metrics** - OpenTelemetry metrics pipeline (thanks [@tcx4c70](https://github.com/tcx4c70)!)
- **OTel Export** - OTEL exporting now correctly shows input and output messages
- **OTel Cost Info** - Cost info in OTEL calls and response tools
- **OTel Insecure Default** - OTel plugin defaults `insecure` to `true` when omitted, enabling HTTP collectors without explicit config; OTel semconv updated to v1.40.0
- **OTel Input/Output Messages** - Propagation to root span
- **`resolvePeriod` UTC** - UTC handling in `resolvePeriod` time calculation
- **Prometheus Telemetry Plugin** - Nil config handling

### Database & migrations

- **SQLite Migrations** - SQLite migration connections, error handling, and disabled foreign-key checks during migration
- **Migrations Conflict Resolution** - Resolved migration conflicts
- **Migration Pools Cached Plan** - Use simple-query protocol for migration pools to prevent cached plan errors
- **Calendar-Aligned Propagation** - `calendar_aligned` propagation in v1.5.0-prerelease4 migration
- **Multipart File Uploads** - Write multipart metadata before file content to fix upload ordering

### Configuration, env & misc

- **MarshalJSON Auto-Redaction** - Removed `MarshalJSON` auto-redaction; explicit redaction is now applied to env-backed fields in `ProxyConfig`, `ClientConfig`, and `AzureKeyConfig`
- **Env Var Redacted Check** - Added missing redacted check for env var values
- **EnvVar JSON Serialization** - Auto-redact env-backed values in `EnvVar` JSON serialization
- **Tool Parameter Schemas** - Preserve explicit empty tool parameter schemas for OpenAI passthrough
- **Config Schema** - Bedrock key config schema fix
- **List Models Output** - Include raw model ID alongside aliases
- **Model Listing** - Unify `/api/models` and `/api/models/details` listing behavior
- **Model Alias Tracking** - Split `ModelRequested` into `OriginalModelRequested` and `ResolvedModelUsed` for accurate model-alias resolution tracking
- **Data Race in fasthttp Read** - Race in data reading from fasthttp request for integrations
- **Fallback Stream State** - Clear `BifrostContextKeyStreamEndIndicator` before fallback requests so stale streaming state doesn't carry into retries
- **API Key Auth Middleware** - Adjusted API key authentication handling in middleware
- **Auth Config Disabled Context** - Update request context correctly when auth config is disabled
- **`BifrostError` String Output** - `String()` method so logged errors render as JSON instead of decimal byte dumps
- **`NewUnsupportedOperationError` Context** - Now populates `Provider` and `RequestType` in `ExtraFields`
- **SCIM Page Layout** - Added `no-scrollbar` utility class and applied `no-padding-parent` to the SCIM page
- **Teams View OSS/Enterprise Split** - Extracted full TeamsView into the shared fallback component so it works correctly in OSS builds; fixed pagination offset snap-back and RBAC loading state race
- **`MockConfigStore` Duplicate** - Removed duplicate `GetOauthConfigsByIDs` from `MockConfigStore`

### Helm

- **Helm `mcpClientConfig`** - Templating fix (thanks [@crust3780](https://github.com/crust3780)!)
- **Helm Encryption Key** - `encryptionKey` is properly optional for StatefulSet deployments when using a Kubernetes secret reference
- **Helm Chart** - Validation refresh
- **Dockerfile.local** - Uses local packages (thanks [@ReStranger](https://github.com/ReStranger)!)

## 🔧 Maintenance

- **`IsActive` / `Enabled` Pointer Types** - Refactored `IsActive` and `Enabled` to pointer types with nil-as-default semantics so unset fields no longer collapse to `false`
- **Streaming Accumulator Raw Request** - Moved raw request extraction to final chunk processing in the streaming accumulator
- **Provider Capability Matrix** - Re-enabled `ContextEditing` and `ContextManagementField` for Vertex; disabled `TaskBudgets` for Azure (not documented upstream); added `claude-4.6-sonnet` support to Bedrock test account
- **Schema Normalizer** - `NormalizeSchemaForAnthropicRaw` (gjson/sjson) avoids `map[string]interface{}` round-trips during Anthropic schema preparation
- **Auth Middleware Context Keys** - Added `IsAPIKeyAuthContextKey` (short-circuit when API-key auth already passed) and `IsLocalAdminContextKey` (bypass RBAC when auth is disabled)
- **Helm Chart Upgrades** - Guardrails Helm chart upgrade; Helm `apply` step added; Kubernetes pod-discovery RBAC templates added
- **Dashboard UI Polish** - Popover scrolling, sheets/cluster page indentation, save-button validation, dialog overflow, fixed `ChartCard` heights, broader `ComboboxSelect` adoption (pricing, routing, assignment fields)
- **Plugin Lifecycle Logging** - Log level param on `AppendRoutingEngineLog`; trimmed unused dependencies in semanticcache
- **Test Harness** - Test harness for quick checks
  - **Parallel Model Listing** - Parallelize model listing for providers to speed up startup time
