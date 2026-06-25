## ✨ Features

- **Runware Provider** — Added Runware provider support, including image and video generation operations
- **Runway Image Operations** — Added Runway image generation operations
- **Customer Attribution** — Added `x-bf-customer-id` and `x-bf-customer-name` header support for per-customer attribution
- **Enriched Model Listing** — `list models` now returns `ContextLength`, `MaxInputTokens`, `MaxOutputTokens`, `Architecture`, and `WebSearch` pricing sourced from pricing entries
- **Streaming Pause/Resume** — Added pause/resume flows for streaming calls
- **Session Trace Grouping** — Added `group_traces_by_session` support to the OTEL/Datadog plugins
- **Root Span Content Toggle** — Added a toggle to disable root-span content logging
- **Password Policy** — Added password-policy validation with inline error and sticky save button to the security view
- **Error Sanitization** — Internal error details (stack traces, SQL) are now sanitized before being sent to clients
- **Cluster Discovery Env Refs** — Added `env.VAR_NAME` support to `dns_names` in cluster discovery config
- **Server Logs Config** — Added configurable server logging
- **Bedrock Streaming Errors** — Added `__type` return for Bedrock errors in streaming paths
- **Secret References** — Added typed `SecretVar` env/vault reference support (`env.*`, `vault.*`) across config and UI, replacing `EnvVar`


## 🐞 Fixed

- **Streaming Memory** — Reduced memory usage on streaming request/response paths
- **Provider Config Sync** — `allow_all_keys` and `blacklisted_models` now sync from the `config.json` source of truth (thanks [@acarpe](https://github.com/acarpe)!) (closes #4640)
- **Provider Keys Payload** — `PUT /api/providers/{provider}` no longer silently discards `keys`/blocked-model edits (thanks [@aeciolevy](https://github.com/aeciolevy)!) (closes #4648)
- **Custom Header Base URL** — Fixed base-URL protocol handling when a custom header is set
- **VK Quota Usage** — Fixed the start time for virtual-key quota model usage
- **Model Budgets** — Fixed model budget attachment from virtual keys
- **Structured Streaming Errors** — Preserved structured errors for streaming plugin blocks
- **Log Hygiene** — Removed leaking request bodies from console logs

## 🐙 Closed GitHub Issues

- [#2347](https://github.com/maximhq/bifrost/issues/2347) — MCP tool ordering is non-deterministic, breaking prefix-based prompt caching
- [#3443](https://github.com/maximhq/bifrost/issues/3443) — Anthropic→OpenAI streaming tool_call deltas violate OpenAI spec on continuation chunks
- [#4068](https://github.com/maximhq/bifrost/issues/4068) — Bedrock: mid-conversation system messages hoisted into top-level `system` block break prompt caching
- [#4413](https://github.com/maximhq/bifrost/issues/4413) — OpenAI Responses streaming returns empty `error.message` on context_length_exceeded
- [#4460](https://github.com/maximhq/bifrost/issues/4460) — GLM-5.2 `reasoning_effort` "max" silently downgraded to "high"
- [#4496](https://github.com/maximhq/bifrost/issues/4496) — Frequent intermittent broken pipe / closed connection errors with vllm provider
- [#4544](https://github.com/maximhq/bifrost/issues/4544) — Cerebras + `/anthropic` endpoint fails after first turn with 400 provider API error
- [#4606](https://github.com/maximhq/bifrost/issues/4606) — Realtime socket request observability logs not recorded since v1.5.2
- [#4608](https://github.com/maximhq/bifrost/issues/4608) — ResponsesMessage drops author/recipient/encrypted_content, breaking Codex multi_agent_v2 subagent spawning
- [#4617](https://github.com/maximhq/bifrost/issues/4617) — idle-timeout timer goroutine can panic in `closeBodyStream` and crash the process
- [#4622](https://github.com/maximhq/bifrost/issues/4622) — Bedrock Converse document blocks with format xlsx/xls/doc/docx silently rewritten to pdf
- [#4627](https://github.com/maximhq/bifrost/issues/4627) — Gemini video reference fields sent under `parameters`
- [#4640](https://github.com/maximhq/bifrost/issues/4640) — provider config `key_ids:["*"]` not synced to `allow_all_keys` for existing virtual keys
- [#4648](https://github.com/maximhq/bifrost/issues/4648) — `PUT /api/providers/{provider}` silently discards `payload.Keys`
