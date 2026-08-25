<Note>
v2.0.0 is the first stable release on the 2.0 line. This changelog rolls up `2.0.0-prerelease1` (based on [v1.6.3](https://docs.getbifrost.ai/changelogs/v1.6.3)), `2.0.0-prerelease2`, `2.0.0-prerelease3` and the final release window, so it is the complete delta for a deployment upgrading from any v1.6.x release. Fixes that also shipped on the v1.6.x line after v1.6.3 are listed once here.
</Note>

<Warning>
**Breaking changes.** Read the [v2.0.0 migration guide](https://docs.getbifrost.ai/migration-guides/v2.0.0) before upgrading.

- **Custom plugin downloads are SSRF-protected** - a plugin `path` pointing at an http(s) URL is rejected if it resolves to a loopback, private, CGNAT, link-local or otherwise non-public address, and every custom plugin path is re-verified on each restart, including ones defined in `config.json`.
- **Custom plugin create and update require admin authentication** - `POST /api/plugins` and `PUT /api/plugins/{name}` reject a custom `path` when the caller only got through because dashboard auth is disabled or unconfigured.
- **Governance APIs moved under `/api/governance/*`** - `/api/teams`, `/api/users`, `/api/roles`, `/api/audit-logs` and other top-level governance paths moved under one namespace; Team and User lists use `limit`/`offset` pagination. Routing rules and the complexity analyzer moved from `/api/governance/*` to `/api/routing/rules` and `/api/routing/complexity-analyzer-config`; the old paths remain as deprecated aliases.
- **`HTTPTransportPreHook` now runs after authentication** - the pipeline is `HTTPTransportPreAuthHook -> auth -> HTTPTransportPreHook -> handler`. Plugins that inject a credential (`x-bf-vk`, `Authorization`, `x-api-key`) must move that work to the new `HTTPTransportPreAuthHook`, and Go plugins implementing `HTTPTransportPlugin` must add the method (`.so` plugins that predate it are skipped for that phase).
- **Legacy telemetry attributes removed** - the `gen_ai.*`-namespaced Bifrost-internal span attributes, `gen_ai.usage.prompt_tokens`/`completion_tokens`, the nanosecond `time_to_first_token` attribute and `x-bf-prom-*` request-header Prometheus dimensions are gone from the OTel and Prometheus connectors. Dashboards should read the `bifrost.*` keys and `time_to_first_chunk`.
- **Gemini tool preference** - a Gemini API request carrying both function declarations and Google Search without `include_server_side_tool_invocations` now keeps the function declarations and drops Google Search (previously the opposite). Set `include_server_side_tool_invocations: true` to send both on Gemini 3 models. Vertex is unaffected.
</Warning>

## ✨ Features

- **Batch Accounting** - Provider batch jobs are tracked in a new `batch_jobs` table and settled asynchronously: results are priced per model from catalog batch rates (0.5 default ratio) on the `/results` path, one aggregate cost log is written idempotently with the creating request's identity, a background sweeper with ownership fencing re-drives jobs that timed out, settled usage is charged exactly once to the creating user's budgets and rate limits (including unscoped virtual key budgets on model-less batch-create requests), mixed-model batch rows are repriced during cost recalculation, and the log detail view shows a Batch Details block with per-state request counts and the settled cost (#5291, #5292, #5293, #5294, #5295, #5296, #6109, #6121, #6376, #6410, #6474, #6505)
- **Claude-on-Vertex Batches** - Vertex batch jobs route Anthropic models to `publishers/anthropic/...`, build Claude-on-Vertex JSONL instances, round-trip `custom_id`, and preserve `tools`, `toolConfig`, `cachedContent`, `labels` and `display_name` on Gemini/Vertex batch requests (#5368)
- **Input / Output Cost Split** - Every log carries `input_cost`, `output_cost` and `additional_cost` (guardrails, semantic cache, MCP) next to the total, across the RDB, ClickHouse, matviews, recalculation and the quota API; speech, transcription and OCR usages carry `BifrostCost`; the log detail view shows the split with per-category detail (#6511)
- **Bifrost Overhead Latency** - `upstream_latency` and `overhead_latency` are recorded on every log, aggregated (avg, p90, p95, p99) in the dashboard's new Bifrost Overhead chart and shown in the log detail view; the overhead is decomposed by span self-time into serialization, conversion, plugins, middleware, key selection, queue wait, networking, client delivery and scheduling buckets (including streaming per-chunk parse, conversion and backpressure and the worker hand-off), persisted to `overhead_breakdown` and rendered as a stacked bar in the log detail view; a `bifrost_overhead_latency_microseconds` histogram is exported to Prometheus and OpenTelemetry and `upstream_latency_ms`/`overhead_latency_ms` tags to Maxim, while breakdown spans are kept out of observability connectors (#5533, #5534, #5535, #6345, #6388, #6389, #6433, #6470, #6495)
- **Notification Center** - Role-targeted dashboard notifications stored in the database, delivered over WebSocket and surfaced in a topbar tray via `GET/POST /api/notifications` (#6207, #6227, #6324)
- **Topbar and Responsive Dashboard** - Persistent topbar with page titles, theme toggle, external links, user menu and version; responsive layouts across all views with truncation and tooltips for long values and icon-only buttons; version-skew detection with an auto-reloading upgrading screen (#6196, #6105, #6126, #6204, #6232, #6330, #6370, #6476, #6485, #6493)
- **Video Edits** - `POST /v1/videos/edits` applies prompt-driven edits, upscaling and background removal to an existing video supplied as bytes, a URL or a provider video ID, on OpenAI and Runware (#6270)
- **Runware Chat, Catalog and Media Operations** - Chat completions, streaming and Responses via Runware's OpenAI-compatible endpoint, `ListModels` from the curated catalog, image upscale via `/v1/images/edits` (`type=upscale`), image-to-3D and async 3D generation via `/v1/videos` (`type=3d`), provider-reported per-task cost, and a raw `/runware_passthrough` route (#6260, #6372, #6208, #6075)
- **JSON Image Edits** - `POST /v1/images/edits` accepts JSON bodies with URL or base64 images and typed extra params in addition to multipart (#6418)
- **OpenAI Ultrafast Service Tier** - `service_tier: "ultrafast"` is forwarded only to models that support it and billed at dedicated ultrafast rates, with matching custom pricing override fields (#6396, #6399)
- **Service Tier on Logs** - Logs record the tier actually served, including Anthropic's `service_tier` from `message_start` on streams, with a Service Tier column and detail field so repricing uses the served tier (#6233, #6236)
- **Pricing Fields** - New per-request flat fee (`cost_per_request`), megapixel-based image tiers (4/8/16/32/64 MP), per-size and joint size+quality image rates for `gpt-image-1`-style models, and `input_cost_per_query` for rerank flow through datasheet sync, the cost engine, custom overrides, the API and the UI override form; upscale output resolution is backfilled from `target`/`factor` on Replicate so tiered rates bill the real output size (#6079, #6082, #6083, #6379, #6380)
- **Model Catalog Pricing and Overrides** - Pricing data in the model catalog (thanks [@johnbrett](https://github.com/johnbrett)!), with resolved pricing overrides exposed on `/api/models/details` and on catalog rows, shown in the dashboard (#6055, #6056, #6058)
- **Typed Embeddings on Bedrock** - Titan V2 `embeddingTypes` and Cohere `embedding_types` on Converse, the native invoke route and LangChain `BedrockEmbeddings` (#6381)
- **Rerank Upgrades** - Structured JSON documents, `return_documents`, `next_token` pagination, caller document IDs preserved in every result, Cohere-shaped errors, cross-provider responses converted back to the caller's wire shape, and `/genai/v1/rank` served cross-provider (#6328, #6301, #6432)
- **OpenRouter Speech, Transcription and Embeddings** - TTS and STT through OpenRouter's audio endpoints, and embedding models included in `ListModels` (#5734, #6264)
- **Grok on Bedrock Mantle** - `xai.` models route through the `openai/v1` Mantle path (#6022)
- **Gemini 3 Thinking Levels** - A per-model `thinkingLevel` support table clamps requested levels to the rungs each model implements; `reasoning_effort: "none"` sets the model's floor level instead of zeroing `thinkingBudget` (#6280)
- **Datasheet-Backed Compatibility** - Anthropic, Bedrock, Cohere and Gemini request shaping (adaptive thinking, native effort, disable-reasoning, mid-conversation system turns, computer-use and text-editor tool generations, default max output tokens, tool validation) is resolved from model capabilities instead of hardcoded model-name checks (#6281, #6492)
- **Reasoning Effort None** - Models that reason by default but do not support reasoning with tool calls get `reasoning.effort: "none"` when they advertise `supports_none_reasoning_effort`, instead of losing `reasoning` entirely (#6293)
- **HTTP Transport Pre-Auth Hook** - New `HTTPTransportPreAuthHook` plugin phase runs before transport authentication so plugins can inject credentials such as `x-bf-vk`; a `virtual-key-from-config` native plugin example ships alongside it (#6375, #6373)
- **Plugin Inject Limits** - Per-plugin `semaphore_size` and `inject_timeout` on `PluginConfig` bound observability `Inject` calls so a hung connector releases its slot (#6341)
- **Harness Session Autodetection** - Claude Code, Codex CLI and OpenCode session headers populate the session ID when `x-bf-session-id` is absent (#6333)
- **Auth and Model Check Skip Paths** - Context keys let trusted internal callers bypass auth resolution, and let evaluate-only requests such as `/inspect` bypass the virtual key provider and model allowlists while budgets and rate limits still apply (#6124, #6479)
- **Passthrough Encoding Negotiation** - Forwarded `Accept-Encoding` is filtered to decodable codecs (gzip, deflate, brotli, zstd; gzip and identity for streams) and chained content encodings are decoded (#6360)
- **Routing Plugin** - Routing rules and the complexity router live in a dedicated `routing` plugin that runs after governance so rules evaluate on the fully stamped context; endpoints moved to `/api/routing/rules` and `/api/routing/complexity-analyzer-config` with deprecated `/api/governance/*` aliases; complexity routing now reads the text of mixed text+image turns (#6144, #6145, #6146, #6147, #6253)
- **Dimension Scope Ceiling** - Grouped log analytics (rankings, histograms, key pairs) are bounded to the customer, team, business unit, user and virtual key ids the caller may see (#6262)
- **MCP Per-User OAuth and Token Exchange** - MCP clients can hold per-user OAuth credentials and per-user headers, configurable from `config.json` as well as the UI, with a documented shared vs per-identity token lookup contract, `oauth_config.resource` (RFC 8707), VK/Users filters on the OAuth Grants and MCP Auth Sessions sidebars and one shared create/install client form; `token_exchange` gains `use_idp_credentials` to reuse SSO login app credentials for providers such as Microsoft Entra ID (`client_id` becomes optional) and combines `offline_access` with `<audience>/.default` for Entra OBO; shared-OAuth clients show `needs_reauth` when their token row is invalidated, `Reauthorize` is limited to shared clients, the OAuth flow claim is atomic against concurrent reauth, stored scopes survive a decode failure, and credential caches propagate cancellation and version their entries (#6068, #6069, #6078, #6411, #6428, #6429, #6504)
- **MCP Connection Lifecycle and Tool Discovery** - Discovered tools persist and resync uniformly across all client types through a hash-gated core callback, surviving restarts and propagating across a cluster; connections use make-before-break reconnects with ephemeral clients rebuilt across the whole connect+init retry, last-known tool maps preserved, connect attempts bound to entry identity and background reconnects deduped; `needs_session_stickiness` is pinned across `config.json` reconciliation; updating static headers on a sticky client pre-flight verifies the new credential and swaps it onto the live connection, per-call shared-credential clients refresh tools synchronously, and a failed enable parks the client at `Disabled` so it can be retried; the global `tool_sync_interval` hot-reloads and re-times running checkers; state badges render with spaces and the `disconnected` filter bucket is now `unstable` (#6409, #6430, #6431, #6483, #6502)
- **Air-Gapped MCP Catalog** - `mcp_library_sync_interval: 0` disables catalog sync and `file://` URLs load the MCP server library from disk (#6195)
- **MCP Log Redaction and Plugin Logs** - MCP tool logs carry redaction mappings and plugin logs (#5744, #5746)
- **Splunk Connector Configuration** - `config.schema.json`, Helm values and dashboard entries for the Splunk HEC observability connector (#6296, #6091, #6099)
- **Helm Broker Clustering** - `bifrost.cluster.type: broker` with broker address, port and TLS settings alongside the existing mesh transport (#6398)
- **HTTP/2 Ping Interval in the UI** - Provider network configuration exposes `http2_ping_interval_in_seconds` (#6228)
- **Status Code Badges** - Error and passthrough logs show the upstream HTTP status code in the log detail header (#5536)
- **Server-Side Tool Calls in Logs** - `web_search_call`, `code_interpreter_call` and similar Responses items render their full payload in the log detail view (#6475)
- **Gemini Server-Side Tool Calls** - Gemini `toolCall`/`toolResponse` parts surface as `web_search_call` items with their own call ID and queries, unmapped tool types are preserved on the native round-trip, and each `thoughtSignature` appears exactly once on replay (#6071)
- **Bedrock VPC Endpoints** - AWS Bedrock keys can target VPC endpoints (#6064)
- **W3C Trace ID Propagation** - Requests carry a W3C trace ID on the context (#5945)
- **Durable Background Jobs** - New `sidekiq` background-job table, store methods, and runner with recovery and reaper; cost recalculation migrated to a durable, resumable and cancellable job with polling instead of SSE (#5800, #5801)
- **Separate OTEL Metrics Pipeline** - The OTEL collector supports a metrics tab independent of traces, plus separate headers for traces and metrics (#5939, #5940)
- **Grouped Logs View** - The logs table groups fallback chains under expandable roots backed by the new `roots_only` filter with child aggregates, and the model catalog persists tab, search and provider in the URL (#5522, #5737, #6059)
- **User Agent and App Attribution** - Logs and MCP tool logs record user agent, app, source, decision, app key and device ID, with custom user-agent mapping and dashboard dimension rankings; MCP tool logs observed by the Bifrost Edge agent can be ingested with device, app key, decision and source attribution
- **S3 Log Export Metadata** - Additional metadata is written alongside S3 log exports (#6070)
- **Matview Maintenance Off Switch** - `matview_refresh_interval` accepts `"off"` to disable logstore matview maintenance entirely (thanks [@jeremym-tanium](https://github.com/jeremym-tanium)!) (#5693)
- **Video Request Info in Logs UI** - Video requests surface their details in the logs UI (#5946)
- **Shell Rewriter Hook** - The UI handler exposes a `ShellRewriter` hook for pre-hydration HTML rewriting (#5807)
- **Custom Branding** - Logo and icon branding support with an OSS fallback stub, cached in localStorage to prevent a logo flash on load (#5806, #6096)
- **User Assignment on Virtual Keys** - Users can be assigned from the virtual key sheet (#5863)
- **Quarterly Budgets** - Quarterly budget windows with a configurable fiscal year start for customers and virtual key provider configs, surfaced in budget labels (#5996, #5997, #5999, #6115, #6116)
- **Sarvam AI Provider** - Added Sarvam AI as a first-class provider with chat, text-to-speech, and speech-to-text support (thanks [@Purvi09](https://github.com/Purvi09)!)
- **ElevenLabs Sound Effects** - Added text-to-sound generation support via `/v1/sound-generation` (thanks [@SecretSun](https://github.com/SecretSun)!)
- **Bedrock Project Scoping** - Added optional `project_id` to Bedrock and Bedrock Mantle key configs with per-alias overrides for Bedrock, Bedrock Mantle, and Vertex, plus UI support
- **Trace Redaction** - Phase-scoped redaction and revealing, transient redaction data field for guardrails, and trace content redaction before connector export
- **Audit Log Object Storage** - S3/GCS object storage config schema for audit log archival
- **Alerting Configuration** - Alerting schema in `config.schema.json` with declarative channels and CEL-based rules, Helm chart support, and enterprise fallback pages
- **Canonical Model Names** - Dashboard model rankings now show canonical model names instead of inference-profile IDs (thanks [@satyamkrishna](https://github.com/satyamkrishna)!)
- **OAuth2 Hardening** - Allowlist for private-use redirect URI schemes (RFC 8252 §7.1) and a `shouldSweep` gate on the OAuth2 sweep worker
- **Mirrored Schema Support** - `schema_url` / `BIFROST_SCHEMA_URL` for mirrored schema locations in isolated deployments
- **Vertex Single-Region Config** - Enforce single-region configuration in Vertex key config
- **Helm Chart Updates** - `bifrost.alerting`, audit-log object storage, `postgresql.external.port` string support, and `bifrost.mcp.toolGroups[*].id`
- **ChatGPT Passthrough** - Added a ChatGPT passthrough route on the OpenAI integration with dedicated request handling
- **Edge Fallback Pages** - Added fallback pages for Bifrost Edge control views (config, devices, inventory) backed by governance resolver support
- **Agent Handover View** - Added an agent handover page with seeded end-to-end data support
- **First-Time Setup Token** - A setup token gates first-time setup so a fresh deployment is not open to the world, and the onboarding checklist is back, completing its dashboard auth step on SSO deployments (#5759, #5784, #6322)

## 🐞 Fixed

- **Structured Output Schema Order** - `response_format` JSON schemas are forwarded byte-for-byte to OpenAI, Anthropic, Bedrock, Gemini and Cohere so the model generates fields in the caller's declared order instead of a re-sorted one (#6235)
- **Thinking Block Typing on Streams** - Reasoning items carrying both an encrypted payload and a visible summary open as `thinking` blocks instead of `redacted_thinking` (#6292)
- **Replayed Thinking Blocks via `bedrock/` Prefix** - Content-less `tool_result` blocks are kept, interleaved block order is preserved, `incomplete` maps to `error` on Converse, and pending reasoning is consumed by its owning item, so multi-turn tool use no longer wedges (#6346)
- **Gemini 400s on Claude Code Traffic** - Trailing assistant prefills are trimmed and mid-conversation system turns are inlined for Gemini/Vertex; `extra_fields` is echoed on `/anthropic/v1/messages` (#6363)
- **Bedrock Tool Use IDs** - IDs longer than 64 characters or outside Bedrock's charset (such as Gemini thought-signature IDs) are aliased deterministically on both `tool_use` and `tool_result` (#6300)
- **Azure Responses Stream Errors** - Terminal `error` and `response.failed` events inside an already-open HTTP 200 SSE stream are surfaced as errors with their nested type, code and message (thanks [@dani29](https://github.com/dani29)!) (#6302)
- **GenAI SSE Heartbeats** - GenAI streams delimit heartbeat comments so Google SDK clients preserve the following event, while older openai-go clients keep the bare heartbeat (thanks [@dani29](https://github.com/dani29)!) (#6252)
- **OpenCode max_tokens** - `max_tokens` is preserved for OpenCode-compatible chat endpoints (thanks [@Alex-wangyang](https://github.com/Alex-wangyang)!) (#6458)
- **HuggingFace Streaming Usage** - HuggingFace is no longer listed as omitting the `[DONE]` marker, and `stream_options.include_usage` defaults on its chat streaming path, so streamed calls stop reporting zero tokens and zero cost (thanks [@elliottrabac](https://github.com/elliottrabac)!) (#6478)
- **Provider Key Name on Update** - A key PUT that omits `name` no longer clears it, and already-exists errors keep their constraint detail (thanks [@cpsc](https://github.com/cpsc)!) (#6417)
- **Bedrock Mantle Streaming** - Bedrock Mantle is registered in `ProviderSendsDoneMarker` so streams end after `finish_reason` (#6021)
- **URL-Sourced Files and Images** - `gs://` URIs go to Gemini/Gemma as `fileData.fileUri` and are read from Cloud Storage for Claude-on-Vertex, `s3://` references go to Bedrock Converse as `s3Location`, Bedrock rerank synthesizes the foundation-model ARN from a bare model ID, OpenAI file blocks keep `file_url`, non-http schemes pass through on the OpenAI and native-Anthropic paths, and Gemini always emits a candidate with its finish reason and drops payload-free parts (#6239)
- **Together and Alias Pricing** - The management catalog resolves runtime provider `together` to the datasheet identity and prices configured aliases through their target model (thanks [@dani29](https://github.com/dani29)!) (#6257, #6320)
- **Redis Vector Store TAG Escaping** - All RediSearch special characters are escaped in TAG query values (thanks [@AdityaPainuli](https://github.com/AdityaPainuli)!) (#5351)
- **MCP Tool Sync Interval Corruption** - Toggling an MCP client's enable/disable switch no longer corrupts `tool_sync_interval`; the value is a whole number of minutes, negative values are rejected instead of silently disabling sync, and re-enabling a per-call client restarts its discovery cycle (#6409, #6502)
- **MCP Tool Map Staleness** - `SetClientTools` replaces the in-memory tool map instead of merging, so tools removed upstream leave memory once the database has dropped them (#6484)
- **SSE Reconnect Identity** - `OnConnectionLost` on SSE MCP clients is gated on connection identity so a stale connection cannot tear down its replacement
- **Connector Header Redaction** - `Authorization`, `x-api-key`, Cloudflare Access and AWS ALB OIDC headers are redacted before export to every observability backend (#6371)
- **Vertex Mixed Tools** - Vertex AI accepts function declarations and Google Search in the same request without `includeServerSideToolInvocations`, and search localization via `retrievalConfig.latLng` is preserved (#6066)
- **Gemini Tool Preference** - When tool combination is disabled, function declarations win over Google Search so the model can still call the caller's tools (#6065)
- **Bedrock Stop Reasons** - Bedrock `content_filter` and `guardrail_intervened` stop reasons map to `incomplete` status with a `content_filter` reason
- **Encrypted Reasoning on Compaction** - The fail-soft that strips `encrypted_content` before retrying a rejected request also covers `/v1/responses/compact` and count-tokens requests, and recognizes Anthropic's `redacted_thinking` rejection (#6041, #5960)
- **DAC-Scoped VK Reads** - `from_memory` virtual key reads are blocked for DAC-scoped callers
- **Path Normalization Auth Bypass** - Fixed a path normalization flaw that allowed auth to be bypassed (#5763)
- **Minimal Reasoning Effort on GPT-5 Models** - `reasoning_effort: "minimal"` is preserved for GPT-5-family OpenAI models instead of being downgraded to `low` (thanks [@jitokim](https://github.com/jitokim)!) (#6046)
- **Gemini Truncated Response Finish Reason** - Truncated Gemini responses report `MAX_TOKENS` instead of `OTHER` (thanks [@AdityaPainuli](https://github.com/AdityaPainuli)!) (#5979)
- **Null Tool-Call Function Name on Streaming** - Streaming continuation deltas no longer materialize an absent tool-call function name as `null` (thanks [@AdityaPainuli](https://github.com/AdityaPainuli)!) (#5966)
- **Bedrock Document Uploads** - Fixed Bedrock file handling in inference so office and PDF documents sent as OpenAI `type: "file"` are accepted (#5947)
- **xAI Usage Cost** - Fixed USD cost ticks for xAI usage (#5950)
- **Governance List-Models Call** - Budgets and rate limits no longer trigger a list-models call (#6051)
- **Realtime Response Create Input** - Guarded `response.create` input (#6050)
- **Governance Rate-Limit Reset CPU** - Guards against invalid reset timeouts, parallelized resting-budget flows only when absolutely required, and fixed the calendar-based alignment qualifier
- **Masked Key Persistence** - Never persist masked provider key previews to config storage (thanks [@eyeveil](https://github.com/eyeveil)!)
- **OpenShift Arbitrary UIDs** - Build-time group-0 ownership with no runtime chown (thanks [@eyeveil](https://github.com/eyeveil)!)
- **Passthrough Virtual Key Attribution** - Passthrough calls via the Azure `api-key` header now attribute to the virtual key (thanks [@eyeveil](https://github.com/eyeveil)!)
- **Rerank for Custom Providers** - `/v1/rerank` now works with custom OpenAI-compatible providers (thanks [@eyeveil](https://github.com/eyeveil)!)
- **Responses Stream Usage** - Persist stream usage when providers omit or reuse sequence numbers (thanks [@eyeveil](https://github.com/eyeveil)!)
- **Wildcard allowed_models Repair** - Repair bare wildcard `allowed_models` rows that broke admin provider updates (thanks [@eyeveil](https://github.com/eyeveil)!)
- **Streaming Error Panic** - Nil-safe tracing span lookup prevents panics on streaming errors (thanks [@eyeveil](https://github.com/eyeveil)!)
- **Anthropic Tool ID Sanitization** - Sanitize `tool_use`/`tool_result` ids to Anthropic's charset (thanks [@Shaik-Sirajuddin](https://github.com/Shaik-Sirajuddin)!)
- **Realtime Transcription Sessions** - Support GA transcription-type sessions in `POST /v1/realtime/client_secrets` (thanks [@Shaik-Sirajuddin](https://github.com/Shaik-Sirajuddin)!)
- **Diarized Transcription** - Support `diarized_json` segments and ElevenLabs speaker passthrough (thanks [@Shaik-Sirajuddin](https://github.com/Shaik-Sirajuddin)!)
- **Model Discovery** - Skip disabled keys when scheduling model-discovery fetches (thanks [@Shaik-Sirajuddin](https://github.com/Shaik-Sirajuddin)!)
- **MCP Timeout Placeholder** - Show the real global default in the MCP tool execution timeout placeholder (thanks [@Shaik-Sirajuddin](https://github.com/Shaik-Sirajuddin)!)
- **Redacted Thinking Round-Trip** - Round-trip Anthropic `redacted_thinking` blocks on the Responses surface (thanks [@fus3r](https://github.com/fus3r)!)
- **Streaming Accumulation** - Preserve citation annotations and `finish_reason` in the accumulated streaming response (thanks [@fus3r](https://github.com/fus3r)!)
- **Gemini Grounded Streaming** - Reset web-search flag when recycling pooled stream state so `web_search_call` items keep emitting (thanks [@fus3r](https://github.com/fus3r)!)
- **Bedrock Truncation Signal** - Signal `max_output_tokens` truncation on the Responses API (thanks [@jeremym-tanium](https://github.com/jeremym-tanium)!)
- **Bedrock Reasoning Config** - Preserve `reasoning_config` on cross-provider translation so fallbacks keep extended thinking (thanks [@Purvi09](https://github.com/Purvi09)!)
- **Anthropic tool_search** - Forward and rebuild server-side `tool_search` on the Responses path (thanks [@ws4charlie](https://github.com/ws4charlie)!)
- **OpenAI Responses Input** - Strip `role` from non-message input items (thanks [@nettee](https://github.com/nettee)!) and serialize compaction request `input` correctly (thanks [@mcclurmc](https://github.com/mcclurmc)!)
- **additional_tools Support** - Added `additional_tools` message type support, preserving nested tool types on `/v1/responses`
- **Plugin Stream Errors** - Emit structured plugin stream errors on integration routes (thanks [@jeffhos](https://github.com/jeffhos)!)
- **Pooled Object Hygiene** - Zero pooled ChannelMessage references on release and sweep orphaned deferred spans in trace store TTL cleanup (thanks [@citrocat](https://github.com/citrocat)!)
- **Hybrid Log Token Usage** - Rebuild token usage from denormalized columns in hybrid log list (thanks [@G-XD](https://github.com/G-XD)!)
- **MCP Tool Ordering** - Deterministic MCP tool ordering for prompt cache stability
- **MCP Inline-Auth Links** - Warn callers not to truncate the `#t=` temp-token fragment (thanks [@MarcusPeng](https://github.com/MarcusPeng)!)
- **Gemini Fixes** - Web search options map to Google Search grounding, file upload MIME types preserved, and video reference fields map to instances (thanks [@vojthor](https://github.com/vojthor)!)
- **OpenAI Parameters** - Honor service tier in chat completion and cap max reasoning effort
- **Anthropic Costing** - Correct inference geo cost and cache rate for fast mode
- **SecretVar Parsing** - Parse `SecretVar` JSON with `ref`/`env_var` fields even when `value` is absent
- **Telemetry** - Forward request id and trace id, reduce metrics cardinality explosion risk, and send status codes on OTEL metrics
- **Dashboard** - Preserve active time period when applying dimension filters, adjust bucket size thresholds for month-range durations, show user popover with `preferred_username` fallback, filter provider-level keys from the prompt manager selector (thanks [@rlex](https://github.com/rlex)!), skip password validation for redacted credentials, and improve `ModelMultiselect` empty and error states
- **API Key Provider Selection** - Fixed provider selection for API keys
- **Azure Auth Headers** - Pass Azure auth headers in helpers
- **Stream Delta Schema** - Added `ExtraContent` to `ChatStreamResponseChoiceDelta` (thanks [@nghodkicisco](https://github.com/nghodkicisco)!)
- **API Auth Bypass** - Stopped `/api/devices` bypassing auth via the `/api/dev` prefix
- **Bedrock Error Types** - Surface the AWS exception type (`X-Amzn-Errortype`) on non-streaming Bedrock error responses instead of dropping it

## 🔧 Maintenance

- **Hot-Path Performance** - Cached serialization for shared MCP tools, a direct `OrderedMap` JSON writer, bulk span attribute writes with cached span pointers, reusable worker delivery timers, retained span attribute maps, generation-stamped memoization of `GetProvidersForModel` and `GetModelsForProvider` via the new `gencache` package, sonic-based JSON responses, and a plugin-log existence check before draining (#6242, #6241, #5956, #5957, #5657, #6387, #5641, #6224, #6268, #6211)
- **Go Toolchain** - Modules build with Go 1.26.6 and the Nix flake pins 1.26.7 (#6269, #6385)
- **Dependency Upgrades** - Dependabot updates across all modules, newman 6.2.2 with pinned transitive overrides, module path fixes and `openai_config` referenced from every provider config schema (#6040, #5864, #6267, #6305, #6275)
- **Test Coverage** - vLLM instances provisioned on RunPod in the release pipeline, Runware harness coverage including `/v1/images/edits` and `/v1/videos`, batch and pricing-override lifecycle harness cases, an Anthropic `message_start` usage regression test, LangChain rerank and embedding integration tests, and e2e fixes for dashboard auth, budget reset and MCP state (#5541, #6303, #6319, #6299, #6327, #6432, #6351)
- **Documentation** - v2.0.0 migration guide with the governance namespace mapping and a v1.5.x downgrade guide for `prerelease3` deployments, v2.0.0 availability callouts, routing API namespace docs, Bedrock application inference profiles, Splunk connector docs, config.schema.json and Datadog env var reference fixes, and Discord badge fixes (thanks [@Swpn0neel](https://github.com/Swpn0neel)!) (#6332, #6374, #6420, #6147, #6203, #6099, #5938, #6019, #6425, #6448)
- **Helm** - Chart releases v2.1.35 and v2.1.36 (#6129, #6249)
- **Governance Route Families** - Editions can override governance route families (#5839)

## 🗄️ Database Migrations

All migrations below are new relative to v1.6.11. Deployments on an older v1.6.x release should also review the intermediate v1.6.x changelogs.

**configstore:**

- **add_mcp_client_pending_oauth_config_json_column** - Adds `pending_oauth_config_json` to `config_mcp_clients`. Reversible: drops the added column.
- **merge_oauth_token_tables** - Consolidates `oauth_tokens` and `oauth_user_tokens` into `mcp_oauth_tokens`. **Non-reversible**: rollback deliberately leaves `mcp_oauth_tokens` in place, because every OAuth read and write targets it from this migration onward and dropping it would destroy any token created or refreshed since, forcing every holder to re-authorize.
- **create_mcp_oauth_flows_table** - Creates `mcp_oauth_flows` to track in-flight OAuth flows. Reversible: drops the new table.
- **drop_oauth_config_pkce_columns** - Drops CSRF state, PKCE verifier and `expires_at` from the OAuth config table now that they live on `mcp_oauth_flows`. **Non-reversible**: forward-only, the dropped values were per-flow ephemeral and re-adding empty columns would restore nothing.
- **drop_oauth_config_token_id_column** - Drops `token_id`. **Non-reversible**: forward-only, it was a pure FK shortcut now reachable via `(oauth_config_id, auth_mode)`.
- **add_mcp_admin_auth_mode_indexes** - Adds admin partial unique indexes on `mcp_oauth_tokens` and `mcp_per_user_header_credentials`. Reversible: drops both indexes.
- **add_mcp_client_token_exchange_json_column** - Adds `token_exchange_json` to `config_mcp_clients`. Reversible: drops the added column.
- **add_needs_session_stickiness_column** - Adds `needs_session_stickiness` to `config_mcp_clients`. Reversible: drops the added column.
- **add_bedrock_endpoints_columns** - Adds Bedrock VPC endpoint columns to the keys table. Reversible: drops the added columns.
- **add_cost_per_request_pricing_column** - Adds `cost_per_request` to model pricing. Reversible: drops the added column.
- **add_notifications_table** - Creates the `notifications` table for the dashboard notification center. Reversible: drops the table.
- **add_batch_jobs_table** - Creates `batch_jobs` with a unique `(provider, batch_id)` identity index, a sweeper scan index and a runner-id index. Reversible: drops the table.
- **add_image_megapixel_tier_pricing_columns** - Adds the five `output_cost_per_image_above_{4,8,16,32,64}_megapixels` columns to model pricing. Reversible: drops the added columns.
- **add_input_cost_per_query_column** - Adds `input_cost_per_query` to model pricing for rerank. Reversible: drops the added column.
- **add_ultrafast_pricing_columns** - Adds the four `*_ultrafast` token rate columns to model pricing. Reversible: drops the added columns.
- **add_image_size_quality_pricing_columns** - Adds the 14 per-size and size+quality image output rate columns to model pricing. Reversible: drops the added columns.
- **add_batch_jobs_attribution_columns** - Adds `user_id`, `team_id`, `customer_id` and `source_log_id` to `batch_jobs` plus a `user_id` index. Reversible: drops the index and the four columns.

**logstore:**

- **logs_add_guardrail_debug_column** - Adds `guardrail_debug` to logs. Reversible: drops the added column.
- **mcp_tool_logs_add_redaction_mapping_column** - Adds the redaction mapping column to MCP tool logs. **Non-reversible**: rollback is a no-op because dropping the column would permanently destroy reveal data for already-redacted MCP logs.
- **logs_add_user_agent_column** - Adds user agent and app columns, their indexes, and a `UserAgentMapping` table. Reversible: drops the indexes and the mapping table.
- **mcp_tool_logs_add_user_agent_column** - Adds user agent and app columns plus indexes to MCP tool logs. Reversible: drops both indexes and the `app` column.
- **logs_recreate_matviews_with_app_column** - Recreates the log materialized views to include the user agent and app columns. Rollback is a no-op because `ensureMatViews` recreates them on next startup.
- **mcp_tool_logs_add_endpoint_columns** - Adds `source`, `decision`, `app_key` and `device_id` to MCP tool logs. Reversible: drops all four columns.
- **mcp_tool_logs_add_plugin_logs_column** - Adds `plugin_logs` to MCP tool logs. Reversible: drops the added column.
- **logs_add_video_edit_input_column** - Adds `video_edit_input` to logs. Reversible: drops the added column.
- **logs_add_upstream_and_overhead_latency_columns** - Adds `upstream_latency` and `overhead_latency` to logs. Reversible: drops both columns.
- **logs_add_batch_debug_column** - Adds `batch_debug` to logs. Reversible: drops the added column.
- **logs_add_cost_breakdown_columns** - Adds `input_cost`, `output_cost` and `additional_cost` to logs. Reversible: drops the three columns.
- **logs_recreate_matviews_with_cost_breakdown** - Marks the hourly matview for rebuild with the cost split columns; `repairMatViewShapes` drops and recreates `mv_logs_hourly` on the next startup. Rollback is a no-op because `ensureMatViews` recreates it on next startup.
- **logs_add_overhead_breakdown_column** - Adds `overhead_breakdown` to logs. Reversible: drops the added column.

<Warning>
**High-throughput deployments: run the logstore migrations during a low-activity window.**

Every logstore migration above alters `logs` or `mcp_tool_logs`, the two highest-insert tables in Bifrost, and several also build indexes on them. On a busy instance the index builds hold locks that block concurrent log inserts for the duration of the build, and the matview recreations rebuild against the full table. Schedule the upgrade for a low-traffic period, or expect elevated log-write latency and possible request-path backpressure while the migrations run.
</Warning>

<Warning>
`merge_oauth_token_tables`, `drop_oauth_config_pkce_columns` and `drop_oauth_config_token_id_column` transform or remove existing OAuth state and cannot be rolled back. Take a database backup before upgrading, and do not roll the binary back past this release once the migration has run.
</Warning>

## 🐙 Closed GitHub Issues

- [#123](https://github.com/maximhq/bifrost/issues/123) - Files API Support
- [#2347](https://github.com/maximhq/bifrost/issues/2347) - MCP tool ordering is non-deterministic, breaking prefix-based prompt caching
- [#3455](https://github.com/maximhq/bifrost/issues/3455) - Segfault/nil dereference panic in Bedrock provider
- [#4318](https://github.com/maximhq/bifrost/issues/4318) - allowed_models persisted as bare "*" string blocks subsequent provider updates
- [#4353](https://github.com/maximhq/bifrost/issues/4353) - config.db corruption from masked-key preview in provider_configs JSON column
- [#4367](https://github.com/maximhq/bifrost/issues/4367) - Image incompatible with OpenShift arbitrary UIDs
- [#4402](https://github.com/maximhq/bifrost/issues/4402) - Vertex provider drops image blocks whose URL uses gs:// scheme
- [#4477](https://github.com/maximhq/bifrost/issues/4477) - Passthrough calls using a Virtual Key log as actual key
- [#4679](https://github.com/maximhq/bifrost/issues/4679) - Bedrock Responses API does not signal max_output_tokens truncation
- [#4689](https://github.com/maximhq/bifrost/issues/4689) - Custom providers cannot set budget
- [#4712](https://github.com/maximhq/bifrost/issues/4712) - ElevenLabs sound effects (/v1/sound-generation)
- [#4780](https://github.com/maximhq/bifrost/issues/4780) - Anthropic server-side tool_search results are dropped on /v1/responses
- [#4834](https://github.com/maximhq/bifrost/issues/4834) - /v1/rerank is not available with custom providers
- [#4846](https://github.com/maximhq/bifrost/issues/4846) - Responses stream usage present in response.completed but not persisted in LLM Logs
- [#4851](https://github.com/maximhq/bifrost/issues/4851) - Governance rate-limit reset causes high CPU in BumpRateLimitUsage
- [#4870](https://github.com/maximhq/bifrost/issues/4870) - Pooled ChannelMessage retains request body, context, and undelivered response while idle
- [#4940](https://github.com/maximhq/bifrost/issues/4940) - Show canonical model names instead of Bedrock inference-profile IDs in Model Rankings
- [#4963](https://github.com/maximhq/bifrost/issues/4963) - Streaming finish_reason dropped from the accumulated (logged) response
- [#5002](https://github.com/maximhq/bifrost/issues/5002) - gpt-4o-transcribe-diarize transcription fails due to string segment IDs
- [#5013](https://github.com/maximhq/bifrost/issues/5013) - OpenAI /responses/compact input serialized as a JSON object causing 400
- [#5026](https://github.com/maximhq/bifrost/issues/5026) - [Bug]: Toggling an MCP client's enable/disable switch corrupts its tool_sync_interval (nanoseconds resent as minutes)
- [#5027](https://github.com/maximhq/bifrost/issues/5027) - MCP Tool Execution Timeout placeholder shows 0 instead of real global default
- [#5036](https://github.com/maximhq/bifrost/issues/5036) - Plugin StreamInterceptionError is flattened on integration routes
- [#5037](https://github.com/maximhq/bifrost/issues/5037) - Disabled keys break provider model discovery
- [#5051](https://github.com/maximhq/bifrost/issues/5051) - Add Sarvam AI provider (chat + TTS/STT)
- [#5061](https://github.com/maximhq/bifrost/issues/5061) - Streaming responses drop citation annotations from the accumulated message
- [#5093](https://github.com/maximhq/bifrost/issues/5093) - Streaming /v1/responses drops Anthropic redacted_thinking blocks
- [#5097](https://github.com/maximhq/bifrost/issues/5097) - Anthropic rejects replayed tool_use/tool_result ids from non-conforming upstream providers
- [#5100](https://github.com/maximhq/bifrost/issues/5100) - additional_tools loses nested tool types on /v1/responses
- [#5101](https://github.com/maximhq/bifrost/issues/5101) - Chat-to-Responses tool replay sends role on function_call input items
- [#5108](https://github.com/maximhq/bifrost/issues/5108) - Bedrock reasoning_config silently dropped on cross-provider translation
- [#5113](https://github.com/maximhq/bifrost/issues/5113) - Gemini/Vertex streaming stops emitting web_search_call items after first grounded request
- [#5432](https://github.com/maximhq/bifrost/issues/5432) - Add TTS and STT support for OpenRouter
- [#5472](https://github.com/maximhq/bifrost/issues/5472) - [Bug]: Bedrock rejects office/PDF document uploads via OpenAI `type:"file"` - "The PDF specified was not valid"
- [#5871](https://github.com/maximhq/bifrost/issues/5871) - [Bug]: AWS Bedrock Mantle streaming is broken
- [#5874](https://github.com/maximhq/bifrost/issues/5874) - [Bug]: SSE heartbeat frame aborts streams for openai-go ssestream consumers (< v3.43.0) with "unexpected end of JSON input"
- [#5885](https://github.com/maximhq/bifrost/issues/5885) - [Bug]: v1.6.8 omits message_start.message.usage on Bedrock-backed providers, breaking @ai-sdk/anthropic streaming
- [#5900](https://github.com/maximhq/bifrost/issues/5900) - [Bug]: Streaming continuation chunks materialize omitted tool-call metadata as null
- [#5978](https://github.com/maximhq/bifrost/issues/5978) - [Bug]: Gemini egress reports truncated responses as FinishReason OTHER, IncompleteDetails switch matches a string that never occurs
- [#6044](https://github.com/maximhq/bifrost/issues/6044) - [Bug]: normalizeOpenAIReasoningEffort maps 'minimal' to 'low' for ALL OpenAI models, even ones that natively support 'minimal'
- [#6240](https://github.com/maximhq/bifrost/issues/6240) - [Bug]: GenAI SSE heartbeat framing causes @google/genai to silently drop the following data event
- [#6248](https://github.com/maximhq/bifrost/issues/6248) - [Bug]: OpenRouter embedding models missing from Semantic Cache dropdown
- [#6334](https://github.com/maximhq/bifrost/issues/6334) - [Bug]: Gemini/Vertex provider fails on Claude Code assistant prefills and mid-conversation system turns (Gemini 3.6 Flash & 3.7 Flash HTTP 400)
- [#6342](https://github.com/maximhq/bifrost/issues/6342) - [Bug]: Anthropic ingress with bedrock/ prefix restructures replayed thinking blocks, wedging multi-turn tool use on claude-opus-4-8
- [#6416](https://github.com/maximhq/bifrost/issues/6416) - [Bug]: Provider key update silently clears "name" when omitted, then the unique-name index 409s subsequent updates
- [#6457](https://github.com/maximhq/bifrost/issues/6457) - [Bug]: OpenCode chat endpoints drop max completion limit
