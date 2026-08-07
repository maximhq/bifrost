## ✨ Features

- **Bedrock HTTP/2 PING Keepalives** — Opt-in HTTP/2 PING keepalives on the Bedrock provider via the new `http2_ping_interval_in_seconds` config key (0 = off), keeping quiet streams alive through intermediaries that sever idle connections (thanks [@jeremym-tanium](https://github.com/jeremym-tanium)!)
- **Expanded OTEL Metric Attributes** — Metrics now carry a service instance id plus team, customer, and business-unit ids and names, so exported series can be sliced by tenant without post-processing
- **Adaptive Thinking on Raw Passthrough** — Adaptive-only Anthropic models (Opus 4.7+, Opus 4.8, Opus 5, Sonnet 5, Fable 5, Mythos 5) now get legacy `thinking.type: "enabled"` rewritten to the adaptive form on the raw passthrough body as well as the typed request path

## 🐞 Fixed

- **SSE Heartbeat Mid-Line Corruption** — The stream reader now tracks line boundaries under a mutex and refuses to emit a heartbeat mid-line, fixing corrupted `data:` payloads on raw passthrough streams where a heartbeat could split a JSON line
- **SSE Heartbeat Frame Compatibility** — Dropped the trailing blank line from the heartbeat comment frame so non-conforming SSE decoders (e.g. openai-go ssestream before v3.43.0) no longer dispatch an empty event and abort mid-stream with "unexpected end of JSON input"
- **Lost Log Rows on Shared Trace IDs** — Concurrent HTTP requests that inherit the same W3C trace id no longer overwrite each other's pending log entry; the join now uses the per-request internal trace id (thanks [@AdityaPainuli](https://github.com/AdityaPainuli)!)
- **Budget Counters Reset on Force-Sync** — `config.json` force-sync no longer overwrites live `current_usage`, `last_reset`, and the token/request rate-limit counters with the values from the file
- **Transcription Filename Dropped** — The client's multipart filename is now carried through transcription ingress, so non-WAV containers are no longer relabelled `audio.mp3` and rejected upstream (thanks [@AdityaPainuli](https://github.com/AdityaPainuli)!)
- **Anthropic Mid-Conversation System Messages** — A `role:"system"` turn that cannot be forwarded natively is now inlined as a user turn instead of being dropped, the placement check enforces both of Anthropic's clauses, and Bedrock's system-reminder converter keeps its `cache_control` marker
- **Bedrock Streaming Correctness** — `ConverseStream` reports `stopReason: tool_use` for tool-use turns instead of `end_turn` (thanks [@axelray-dev](https://github.com/axelray-dev)!); `message_start` carries an all-zero `usage` object when figures are unknown so `@ai-sdk/anthropic` 4.0.6-4.0.32 accepts the frame; and encrypted reasoning is preserved as a replay signature when translating Responses history (thanks [@zachgersh](https://github.com/zachgersh)!)
- **Encrypted Reasoning Fail-Soft** — An upstream 400 caused by unverifiable replayed `encrypted_content` now strips the reasoning content and retries once instead of failing the request, which matters when a key pool rotates or a request falls back to another provider
- **Server-Side Tool Search** — `tool_search_tool_*` types are normalized on the Responses path, and `include_server_side_tool_invocations` now reaches the Gemini declaration-drop gate (thanks [@AdityaPainuli](https://github.com/AdityaPainuli)!)
- **DeepSeek Thinking on Multi-Turn** — Thinking is no longer silently disabled for ordinary multi-turn conversations through the OpenAI-compatible surface; the shim is non-mutating and inbound reasoning aliases are normalized
- **Vertex and Gemini Response Fidelity** — `generateContent` keeps `candidates[0].safetyRatings` and `avgLogprobs`, and the Vertex cached-content methods honour API-key or context-header auth instead of overwriting `Authorization` with an OAuth token (thanks [@TransactCharlie](https://github.com/TransactCharlie)!)
- **Custom Provider Base Resolution** — OpenAI models served through a custom provider now resolve to their built-in base provider before deciding reasoning item-id embedding
- **MCP Tool Errors Replayed as Success** — Failed MCP tool executions are now marked as errors instead of being replayed to the model as successful results, covering agent-loop failures, the MCP protocol's own `isError` flag, and CodeMode lookup/sandbox failures (thanks [@AidanAllchin](https://github.com/AidanAllchin)!)
- **Tool-Result Document Blocks** — `document` blocks in tool results survive the Anthropic to Responses conversion with a synthesized filename, and `FileURL`/`FileType` propagate through all three chat/responses conversion paths
- **Stream Termination Edge Cases** — A nil delta paired with a non-nil finish reason no longer aborts the stream, and GPT-5-series detection tolerates prefixed model names when resolving reasoning-effort support

## 🗄️ Database Migrations

- No new database migrations in this release.

## 🐙 Closed GitHub Issues

- [#5206](https://github.com/maximhq/bifrost/issues/5206) — Bedrock ConverseStream egress reports stopReason=end_turn for tool-use turns (should be tool_use)
- [#5211](https://github.com/maximhq/bifrost/issues/5211) — Bedrock streaming can drop with "unexpected EOF" when an intermediary idle timeout severs a quiet stream
- [#5256](https://github.com/maximhq/bifrost/issues/5256) — Concurrent HTTP requests sharing a W3C trace ID lose LLM log rows
- [#5279](https://github.com/maximhq/bifrost/issues/5279) — OpenAI /v1/responses to Anthropic drops the tool_search_tool_regex type, so server-side tool_search never runs
- [#5670](https://github.com/maximhq/bifrost/issues/5670) — Transcription drops the client's multipart filename, so non-WAV containers are relabelled audio.mp3 and rejected
- [#5679](https://github.com/maximhq/bifrost/issues/5679) — Anthropic Messages does not propagate Gemini mixed server/client tool opt-in
- [#5843](https://github.com/maximhq/bifrost/issues/5843) — generateContent (Gemini format) drops `candidates[0].safetyRatings` and `avgLogprobs` on Vertex AI responses
- [#5874](https://github.com/maximhq/bifrost/issues/5874) — SSE heartbeat frame aborts streams for openai-go ssestream consumers (< v3.43.0) with "unexpected end of JSON input"
- [#5885](https://github.com/maximhq/bifrost/issues/5885) — v1.6.8 omits message_start.message.usage on Bedrock-backed providers, breaking @ai-sdk/anthropic streaming
- [#5887](https://github.com/maximhq/bifrost/issues/5887) — DeepSeek thinking silently lost on ALL multi-turn requests via OpenAI-compat inbound (v1.6.7; regression from v1.6.3)
- [#5890](https://github.com/maximhq/bifrost/issues/5890) — chat completions surface drops tool_result `is_error`, so failed tool calls replay to the model as successful
- [#5902](https://github.com/maximhq/bifrost/issues/5902) — service_tier silently dropped for gpt-5.4 family, priority/fast requests downgrade to default
- [#5905](https://github.com/maximhq/bifrost/issues/5905) — v1.6.8 raw passthrough heartbeat can split SSE data lines and corrupt JSON
- [#5925](https://github.com/maximhq/bifrost/issues/5925) — config.json force-sync overwrites budget current_usage and last_reset on startup

## 🔧 Maintenance

- **Dependency Upgrades** — Bumped core to v1.7.7 and framework to v1.5.7; all plugins bumped to pick up the cascade (compat v0.1.33, governance v1.6.11, jsonparser v1.5.34, logging v1.6.7, maxim v1.6.34, mocker v1.5.34, modelcatalogresolver v1.0.15, otel v1.4.6, prompts v1.0.34, semanticcache v1.5.34, telemetry v1.5.34)
