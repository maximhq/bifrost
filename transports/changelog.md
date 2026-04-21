## ✨ Features

- **Claude Opus 4.7** — Added compatibility for Anthropic's Claude Opus 4.7 model, including adaptive thinking, task-budgets beta header, `display` parameter handling, and "xhigh" effort mapping
- **Anthropic Structured Outputs** — Added `response_format` and structured output support for Anthropic models across chat completions and Responses API, with order-preserving merge of additional model request fields (thanks [@emirhanmutlu-natuvion](https://github.com/emirhanmutlu-natuvion)!)
- **MCP Tool Annotations** — Preserve MCP tool annotations (`title`, `readOnly`, `destructive`, `idempotent`, `openWorld`) in bidirectional conversion so agents can reason about tool behavior
- **Anthropic Server Tools** — Expanded Anthropic chat schema and Responses converters to surface server-side tools (web search, code execution, computer use containers) end-to-end
- **OCR Request Support** — Added OCR request type with stream terminal detection, full body accumulation for passthrough streams, input logging with detail view, and per-request pricing support
- **Team Budgets** — New team budget system with per-team spending tracking, atomic ratelimit updates, and database structure support
- **Single Log Export** — Export individual log entries from the logs view and MCP logs sheet
- **Deny-by-Default Virtual Keys** — Virtual key provider and MCP configs now block all access when empty; automatic migration backfills existing keys to preserve behavior
- **User Agent Detection** — Improved multi-user-agent detection with tool call reduplication fix for mixed-client environments
- **Per-User OAuth Codemode** — OAuth server selection and validation per-user in codemode

## 🐞 Fixed

- **Provider Queue Shutdown Panic** — Eliminated `send on closed channel` panics in provider queue shutdown by leaving channels open and exiting workers via the done signal; stale producers transparently re-route to new queues during `UpdateProvider`
- **OpenAI Tool Result Output** — Flatten array-form `tool_result` output into a newline-joined string for the Responses API so strict upstreams (Ollama Cloud, openai-go typed models) no longer reject with HTTP 400 (thanks [@martingiguere](https://github.com/martingiguere)!)
- **vLLM Token Usage** — Treat `delta.content=""` the same as `nil` in streaming so the synthesis chunk retains its `finish_reason`, restoring token usage attribution in logs and UI
- **Gemini Tool Outputs** — Handle content block tool outputs in Responses API path for `function_call_output` messages (thanks [@tom-diacono](https://github.com/tom-diacono)!)
- **Bedrock Streaming** — Emit `message_stop` event for Anthropic invoke stream and case-insensitive `anthropic-beta` header merging (thanks [@tefimov](https://github.com/tefimov)!)
- **Bedrock Tool Images** — Preserve image content blocks in tool results when converting Anthropic Messages to Bedrock Converse API (thanks [@Edward-Upton](https://github.com/Edward-Upton)!)
- **Gemini Thinking Level** — Preserve `thinkingLevel` parameters across round-trip conversions and correct finish reason mapping
- **Anthropic WebSearch** — Removed the Claude Code user agent restriction so WebSearch tool arguments flow for all clients
- **Responses Streaming Errors** — Capture errors mid-stream in the Responses API so transport clients see failures instead of silent termination
- **Anthropic Request Fallbacks** — Dropped fallback fields from outgoing Anthropic requests to avoid schema validation errors
- **Tool Execution Header** — Remove redundant static header assignment in tool execution flow
- **Virtual Key Configs** — Virtual key configurations cleaned up correctly on provider changes; fix key creation and management edge cases
- **Virtual Key Management** — Fix virtual key creation validation and update handling
- **vLLM Extra Params** — Extra parameters now properly passed through to vLLM providers
- **OAuth Query Params** — Preserve existing query parameters when building OAuth upstream authorize URLs
- **Streaming Timeouts** — Separate streaming clients per provider to prevent read timeout collisions
- **Plugin Timer Concurrency** — Fix concurrent map access in plugin timer causing potential race conditions
- **Async Context Propagation** — Preserve context values in async requests so downstream handlers retain request-scoped data
- **Custom Providers** — Allow custom providers without a list-models endpoint to accept any model rather than restricting on virtual key registration
- **OTel Insecure Default** — OTel plugin now defaults `insecure` to true when omitted, enabling HTTP collectors without explicit config; OTel semconv updated to v1.40.0
- **Helm mcpClientConfig** — Fixed templating for `mcpClientConfig` (thanks [@crust3780](https://github.com/crust3780)!)
- **Helm Chart** — Refreshed helm chart with validation fixes