## ✨ Features

- **Dedicated Streaming Client** — Each provider now uses a separate HTTP client for streaming requests with read-timeout cleared, eliminating premature SSE/EventStream termination on long-running responses; per-chunk idle detection is enforced via `NewIdleTimeoutReader`
- **Anthropic Structured Outputs** — Added `response_format` and structured output support for Anthropic models across chat completions and Responses API, including JSON-schema and JSON-object formats with order-preserving merge of additional model request fields (thanks [@emirhanmutlu-natuvion](https://github.com/emirhanmutlu-natuvion)!)
- **MCP Tool Annotations** — Preserve MCP tool annotations (`title`, `readOnly`, `destructive`, `idempotent`, `openWorld`) in bidirectional conversion between MCP tools and Bifrost chat tools so agents can reason about tool behavior
- **Claude Opus 4.7 Support** — Added compatibility for Claude Opus 4.7, including adaptive thinking, task-budgets beta header, `display` parameter handling, and "xhigh" effort mapping
- **Routing Rules Auto-resolve Model** — Provider-only fallbacks now automatically inherit the incoming model, removing the need to repeat model names in fallback config

## 🐞 Fixed

- **Anthropic Empty Thinking Blocks** — Strip `thinking`-typed content blocks with empty `"thinking"` fields before sending to Anthropic, preventing HTTP 400 errors from Claude Code requests
- **Plugin Timer Concurrent Map Panic** — Added `streamingMu sync.Mutex` to `PluginPipeline` to guard `postHookTimings` across concurrent goroutines during streaming; also fixed a double-pool-release race on streaming errors
- **Stream Cancellation Safety** — Guarded channel sends and finalizer protection prevent goroutine leaks when clients disconnect mid-stream
- **Gemini Tool Outputs** — Handle content block tool outputs in Responses API path for `function_call_output` messages (thanks [@tom-diacono](https://github.com/tom-diacono)!)
- **Gemini Thinking Level** — Preserved `thinkingLevel` parameters across round-trip conversions and corrected finish reason mapping
- **Bedrock Streaming** — Emit `message_stop` event for Anthropic invoke stream and case-insensitive `anthropic-beta` header merging (thanks [@tefimov](https://github.com/tefimov)!)
- **Bedrock Tool Images** — Preserve image content blocks in tool results when converting Anthropic Messages to Bedrock Converse API (thanks [@Edward-Upton](https://github.com/Edward-Upton)!)
- **OpenAI Tool Result Output** — Flatten array-form `tool_result` content into a string before marshaling for the Responses API so strict upstreams no longer reject with HTTP 400 (thanks [@martingiguere](https://github.com/martingiguere)!)
- **vLLM Token Usage** — Treat `delta.content=""` same as `nil` in streaming so the synthesis chunk retains `finish_reason`, restoring token usage in logs and UI
- **Anthropic WebSearch** — Removed the Claude Code user agent restriction so WebSearch tool arguments flow for all clients
- **Anthropic Request Fallbacks** — Dropped fallback fields from outgoing Anthropic requests to avoid schema validation errors
- **Responses Streaming Errors** — Capture errors mid-stream in the Responses API so clients see failures instead of silent termination
- **Async Context Propagation** — Preserve context values in async requests so downstream handlers retain request-scoped data
- **Custom Providers** — Allow custom providers without a list-models endpoint to accept any model rather than restricting on virtual key registration
- **OTEL Plugin** — Default `insecure` to `true` in config.json and include fallbacks in emitted OTEL metrics
- **Logs UI** — Switched from WebSocket push to polling for log updates; fixed polling mechanism and defaults the time range to the last hour in logs and dashboard
- **Helm mcpClientConfig** — Fixed templating for `mcpClientConfig` (thanks [@crust3780](https://github.com/crust3780)!)
- **Payload Marshalling** — Removed unnecessary marshalling of payload in the transport path