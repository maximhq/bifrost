## ✨ Features

- **WebSocket Proxy Support** — Realtime and Responses WebSocket connections now route through the configured provider-level proxy (HTTP, SOCKS5, env-based) instead of always dialing direct
- **Configurable SCIM Buffer Sizes** — Added `WithFasthttpBufferSizes` option to `HTTPClientFactory` so IdP token endpoints returning headers larger than the 4KB default no longer fail SCIM/OAuth clients
- **Model Multi-Window Rate Limits** — Model-level rate-limit APIs, config schema support, and Model Limits UI for independent RPM/RPD/TPM/TPD rules
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

- **SSE Heartbeat Frame Compatibility** — Dropped the trailing blank line from the heartbeat comment frame so non-conforming SSE decoders (e.g. openai-go ssestream before v3.43.0) no longer dispatch an empty event and abort mid-stream with "unexpected end of JSON input"
- **Proactive SSE Disconnect Detection** — Moved SSE heartbeat handling into a shared structure so client disconnects during streaming are detected proactively instead of only when a producer loop attempts a write, fixing false-success logging on fast/bursty upstreams like Vertex
- **Closed Channel Panic on Stream Shutdown** — Fixed a race where a heartbeat goroutine mid-send on `eventCh` at shutdown could panic with "send on closed channel"
- **Budget Pruning Crash with `config.json` Source of Truth** — Tolerate `ErrNotFound` when pruning cascade-deleted budgets and configs, fixing a startup crash for API-created model configs absent from `config.json`
- **Bedrock Header Signing Denylist** — Fixed credential isolation so caller headers stored for Anthropic OAuth passthrough are no longer forwarded to other providers, preventing SigV4 signature mismatches on Bedrock
- **Deterministic Bedrock Tool Ordering** — Fixed non-deterministic tool ordering in `toolConfig` caused by map iteration, which was breaking Bedrock prompt-cache hits
- **Bedrock `cache_control` Translation** — `cache_control` markers on Anthropic-format content blocks, system blocks, and tools are now correctly translated through the Bedrock invoke and Converse paths instead of being silently dropped
- **Bedrock Adaptive Thinking Fixes** — Reasoning/thinking `max_tokens` validation errors now return HTTP 400 instead of 500; `tool.defer_loading` is gated on its own beta header; Nova2 web search and code execution tools handled correctly
- **Encrypted Reasoning Content Mismatch** — Fixed a mismatch where replaying OpenAI Responses API reasoning items through the Anthropic surface minted a fresh item id while forwarding the original encrypted content, which OpenAI rejected
- **Bedrock Invoke Content Retention** — Bedrock's InvokeModel route now correctly decodes Anthropic's type-discriminated image/tool_use/tool_result blocks instead of silently dropping them
- **Bedrock Document Message Placeholder** — Messages containing a document block without accompanying text no longer get rejected by Bedrock's Converse API
- **VK Provider Bulk Replace** — Virtual key provider config replacement is now a single bulk operation instead of per-provider round trips, fixing a hot-path slowdown at scale

## 🗄️ Database Migrations

- No new database migrations in this release.

## 🐙 Closed GitHub Issues

- [#5010](https://github.com/maximhq/bifrost/issues/5010) — Server-side SSE keepalive (comment heartbeat) to keep long-idle streams alive through intermediaries
- [#5874](https://github.com/maximhq/bifrost/issues/5874) — SSE heartbeat frame aborts streams for openai-go ssestream consumers (< v3.43.0) with "unexpected end of JSON input"
- [#5186](https://github.com/maximhq/bifrost/issues/5186) — Anthropic-surface replay of OpenAI encrypted reasoning mints a fresh item id, OpenAI 400s with "Encrypted content item_id did not match the target item id"

## 🔧 Maintenance

- **Dependency Upgrades** — Bumped core to v1.7.6, framework to v1.5.6, and governance to v1.6.10; all other plugins bumped to pick up the cascade (compat v0.1.32, jsonparser v1.5.33, logging v1.6.6, maxim v1.6.33, mocker v1.5.33, modelcatalogresolver v1.0.14, otel v1.4.5, prompts v1.0.33, semanticcache v1.5.33, telemetry v1.5.33)
- [#5074](https://github.com/maximhq/bifrost/issues/5074) - Fallback routing model selection is truncating model names
- [#5108](https://github.com/maximhq/bifrost/issues/5108) - Bedrock Converse: reasoning_config/thinking silently dropped on cross-provider translation, fallbacks lose extended thinking
- [#5308](https://github.com/maximhq/bifrost/issues/5308) - Responses API image blocks missing required "detail" field when converted from non-OpenAI providers
