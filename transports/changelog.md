## ✨ Features

- **Dimension Header Propagation to Child Spans** — `x-bf-dim-*` HTTP headers are now stored on the trace at ingress and merged onto every exported span (root, LLM call, plugin pre/posthook, retry, fallback, MCP tool) by observability plugins, instead of attaching only to the root HTTP span. Reserved suffixes `path` and `method` are still skipped, and span-level attributes win on conflict (#3770)
- **Azure v1 API Migration** — Migrated Azure provider to the v1 API: removed the `api-version` query parameter and the `/openai/deployments/{model}/...` URL pattern in favor of `/openai/v1/{operation}`; the `api_version` field has been dropped from `AzureKeyConfig` (#3661, #3756)
- **EnvVar Support for OTEL & Prometheus Configs** — `CollectorURL`, `MetricsEndpoint`, headers, push gateway URL, and basic auth credentials can now be sourced from environment variables (e.g., `env.OTEL_COLLECTOR_URL`); added a new `ConfigMarshallerPlugin` interface that lets plugins control storage/redaction round-trips (#3651)
- **OTel Extra Header Forwarding** — `x-bf-eh-*` extra headers forwarded to upstream providers are now also emitted on the request span under `gen_ai.request.extra_header.*` for end-to-end tracing (#3730)
- **OTel Semantic Conventions** — Aligned OTel attribute keys with the OpenTelemetry GenAI spec (canonical `gen_ai.*` and new `bifrost.*` attributes); legacy attributes are retained in parallel to avoid breaking existing dashboards (#3732)
- **VK Quota with Provider Configs** — `GetVirtualKeyQuotaByValue` and the `getVirtualKeyQuota` HTTP response now include `provider_configs` with their budgets and rate limits (#3721)
- **MCP Temp Token Non-Auth Toggle** — Added `mcp_enable_temp_token_auth` client config flag to gate short-lived MCP token minting for non-authenticated users (#3720)
- **Responses Stream in JSON Parser** — `jsonparser` plugin now handles OpenAI Responses API streaming (`ResponsesStreamRequest`) in addition to chat completions (#3749)
- **Session API Rework** — Logout now calls both the password-based session logout and OAuth logout endpoints and resets all RTK Query cache state (#3698)

## 🔒 Security

- **Go Dependency CVE Remediation** — Updated `golang.org/x` dependencies flagged by Docker Scout, clearing 20 advisories (severity up to 10.0): `crypto` v0.49.0 → v0.52.0, `net` v0.52.0 → v0.55.0, `sys` v0.42.0 → v0.45.0, `text` v0.35.0 → v0.37.0, `term` v0.41.0 → v0.43.0 (cli). Verified with `govulncheck` against the live Go vulnerability database: zero vulnerabilities remain in any module (#3900)
- **Hardened Container Image** — Removed the standalone GNU `wget` package from the Alpine runtime image, eliminating CVE-2025-69194 (8.8); the `HEALTHCHECK` now uses the built-in busybox `wget` applet, with no functional change

## 🐞 Fixed

- **Ollama Streaming Auth** — Ollama streaming text and chat requests now forward the configured API key as an `Authorization: Bearer` header (#3906)
- **SGL Streaming Auth** — SGL provider now sends the `Authorization` header on streaming requests (#3307) (thanks [@hensapir](https://github.com/hensapir)!)
- **Governance & Logging APIs** — Removed the `from_memory` query parameter; virtual key and config list APIs now return consistent DB-backed results, with VK names batch-fetched in a single query (#3903)
- **Streaming Latency for Observability** — Deferred root span termination to the trace completer callback for streaming requests so request latency is no longer inflated by header-flush time (#3762)
- **Stream Cancellation Race** — Set `BifrostContextKeyConnectionClosed` before closing the stream and short-circuit `idleTimeoutReader.Read` when the connection is already closed to avoid panics and hangs on cancellation (#3733)
- **Bedrock Cache Points** — Strip cache points from Bedrock requests for models that do not support prompt caching (e.g., GLM, Llama) to avoid Converse API errors (#3754)
- **Bedrock Empty Text Blocks** — Skip empty/nil text blocks during Bedrock response conversion to avoid invalid messages (#3747)
- **Bedrock Reasoning + Tools** — Preserve reasoning content blocks on assistant turns that also contain tool calls in the Bedrock chat converter (#3690)
- **Bedrock Search Content & Video** — Restored search content and video parts that were being dropped from Bedrock-native passthrough requests (#3729)
- **Structured Output Stop Reason** — Fixed an incorrect `tool_calls` finish reason when structured output is combined with extended-thinking tools (#3685)
- **Gemini Tool Schema Passthrough** — Forward full tool parameter schemas via `parametersJsonSchema` instead of the lossy `parameters` form; corrected tool response role to `user`; resolved structured output + tools conflict (#3761)
- **Anthropic Stop Reason & Tool Versions** — Normalized stop reason mapping (`end_turn` to `stop`, `tool_use` to `tool_calls`, `max_tokens` to `length`) and upgraded `text_editor_20250124`/`str_replace_editor` to `text_editor_20250728` for computer-use tools (#3761)
- **Azure Endpoint Redaction** — Fixed a panic when `AzureKeyConfig.Endpoint` is a literal value rather than an env reference (#3761)
- **Auth Middleware Path Match** — Match temp-token auth middleware whitelist against the request path only, not the full URI with query parameters (#3737)
- **Governance Blocked Models UI** — Restored the missing Blocked Models create/edit UI in the VK provider config sheet (#3750)
- **Logging Plugin Cleanup Drain** — Fixed a shutdown race where `batchWriter` could drop in-flight log entries; `Cleanup` now drains both the recovered batch and remaining queue within a 30-second budget (#3717)
- **Model Rankings Empty Entries** — Excluded entries with empty `model` values from model rankings matview queries so blank rows no longer surface in the UI (#3758)
- **User Filter Duplicates** — Recreated `mv_filter_users` matview to require non-empty `user_name`, eliminating duplicate filter dropdown entries (#3764)
- **User Filter Display Name** — Use `user_name` instead of `user_id` as the display label for users in logging filters (#3691)
- **Large Numeric ID Precision** — Preserve large numeric IDs in URL search params by skipping JSON parsing for plain strings (#3692)

## 🔧 Refactors & Chores

- **Error Propagation for GetAvailable\* APIs** — `GetAvailable*` methods on `LoggerPlugin`/`LogManager` now return wrapped errors instead of silently logging and returning empty slices (#3759)
- **Governance Blocklist Matching** — Use `slices.Contains` for VK blocked-model matching for clearer code with identical semantics (#3727)
- **Exported `ResolvePeriod`** — Renamed `resolvePeriod` to `ResolvePeriod` so external packages can reuse the period parsing (#3763)

## 📚 Docs

- **OTEL Env Var Documentation** — Documented `env.VAR_NAME` support for `collector_url`, `metrics_endpoint`, and headers in OTEL/Prometheus plugin docs
- **OTEL OSS Features & Examples** — Added OTEL documentation to the OSS features list with usage examples (#3731)
- **Anthropic Auth Recommendation** — Recommend `ANTHROPIC_AUTH_TOKEN` over `ANTHROPIC_CUSTOM_HEADERS` for Claude Code authentication (#3686)
