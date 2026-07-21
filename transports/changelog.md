## ✨ Features

- **Wafer AI Provider** - Added Wafer AI as a supported provider
- **Async Webhooks** - New webhook delivery system for async jobs: configurable webhook endpoints (config.json, admin API, and UI), SSRF-safe delivery dispatcher with retries, delivery history with server-side pagination/search/filtering, and inference `request_id` propagation through async jobs and webhook payloads; failed jobs now inline `error`/`error_omitted` fields
- **Reasoning Token Tracking** - Anthropic extended-thinking tokens are now tracked as `ReasoningTokens` across chat, responses, and passthrough
- **Retain Content Toggle** - New toggle to always retain request/response content in object storage regardless of retention cleanup
- **Throughput Metrics** - Tokens/sec throughput histogram endpoints, dashboard metrics, and throughput in model rankings and trend data
- **MCP Metrics** - MCP metrics exported via OTEL and the telemetry (Prometheus) plugin, plus a `resource` parameter on the MCP OAuth handshake
- **Routing Rule Validation** - Routing CEL expressions and `scope_id` references are now validated at write time in create/update handlers
- **Network Config** - Configurable keep-alive duration in network config
- **Object Storage Archival** - Added `archiveInterval`, `archiveGracePeriod`, and `archiveMaxObjectBytes` settings
- **Connector User Email Export** - Connectors can now export user emails
- **Logs UI** - Server fallback model shown in logs, content-disabled message on the logs UI, persisted page-size preference, and `prompt_tokens`/`completion_tokens` in search stats

## 🐞 Fixed

- **Anthropic Fallbacks** - Fixed fallback handling and refusal responses on the Anthropic surface, and billing now attributes usage to the fallback model actually served
- **Bedrock Reasoning** - Fixed double emission of reasoning content on Bedrock streams
- **Fallback Model Names** - Made `RefineModelForProvider` idempotent so fallback routing no longer truncates model names (fixes Groq/Replicate/Parasail prefix handling)
- **OpenAI Image Blocks** - `input_image` blocks now default `detail` to `auto`, fixing strict downstream validators such as vLLM (thanks [@AdityaPainuli](https://github.com/AdityaPainuli)!)
- **Streaming Responses Surface** - Completed visible thinking items, completed Cohere terminal events with the output array, fixed reasoning item streaming in the mux, and handled line-by-line errors in streaming
- **Azure Structured Output** - Structured output format is converted to a tool for Azure in the Anthropic integration; unsupported reasoning summary values are dropped for the Azure model router
- **DeepSeek and SGLang** - Anthropic-compatible APIs supported via key-level setting for SGLang and used for DeepSeek chat/responses; count-tokens handling added for both
- **vLLM** - Moved vLLM to the native Responses API
- **Fireworks** - Added support for Anthropic APIs in Fireworks
- **Mistral OCR** - Raw request capture and log storage enabled for Mistral OCR requests
- **Rolling Deploy Safety** - Materialized-view read path is gated on a shape check to prevent "column does not exist" errors during rolling deploys
- **MCP Tool Sync** - Out-of-range `tool_sync_interval` minutes are rejected to prevent nanosecond-scale sync loops
- **Routing Rules** - Unresolved virtual keys are excluded from the scope ID set and empty routing rule fields are normalized
- **Pricing Fallback** - Chat and Responses pricing fallback now works bidirectionally
- **OTEL Content Attributes** - OTEL now uses the central method for content attribute checks

## 🐙 Closed GitHub Issues

- [#5074](https://github.com/maximhq/bifrost/issues/5074) - Fallback routing model selection is truncating model names
- [#5108](https://github.com/maximhq/bifrost/issues/5108) - Bedrock Converse: reasoning_config/thinking silently dropped on cross-provider translation, fallbacks lose extended thinking
- [#5308](https://github.com/maximhq/bifrost/issues/5308) - Responses API image blocks missing required "detail" field when converted from non-OpenAI providers
