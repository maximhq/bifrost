## ✨ Features

- **Denylist Models** — Provider keys support `blacklisted_models` field to exclude specific models from routing and filtered list-models; denylist takes precedence over the `models` allow list

## 🐞 Fixed

- **MCP Gateway Headers** — Fixed support for `x-bf-mcp-include-clients` and `x-bf-mcp-include-tools` headers to filter MCP tools/list response
- **Bedrock Duplicate Events** — Fixed duplicate `content_block_stop` events in Bedrock streaming responses
- **Reasoning Content Marshaling** — Fixed `reasoning_content` JSON tag in OpenAI response types
- **OTEL Streaming Traces** — Fixed response capture in OTEL tracing for streaming calls
- **Broken Pipe Handling** — Added broken pipe detection to connection pool error handler
- **Cache Token Streaming** — Fixed cache token capture for streaming calls across Anthropic and Bedrock providers
- **Vertex Embedding URL** — Fixed global region URL construction in Vertex embedding method
- **Bedrock Reasoning Merge** — Fixed reasoning content merge logic for Bedrock provider
- **Bedrock HTTP/2 Toggle** — Fixed enforce HTTP/2 toggle behavior for Bedrock provider
- **Codex Store Parameter** — Fixed `store` parameter handling for Codex conversations
- **Gemini Duplicate Text** — Skipped `OutputTextDone` events to prevent duplicate text in Gemini GenAI streaming (thanks [@ava-grace-zoe](https://github.com/ava-grace-zoe)!)
- **Gemini Thought Signatures** — Handled missing thought signatures in Gemini provider (thanks [@mrcfps](https://github.com/mrcfps)!)
- **Replicate Model Slugs** — Refined replicate model slug resolution in model catalog (thanks [@brianphillips](https://github.com/brianphillips)!)
- **Logging Default** — Kept logging enabled by default for new configurations
- **Gin Migration Deadlocks** — Moved all gin migrations to Go to avoid deadlocks
- **WebSocket Concurrent Writes** — Fixed concurrent write safety in WebSocket Responses API sessions
- **Persist Store Config** — Persisted store raw request/response config at provider level (thanks [@chenbo515](https://github.com/chenbo515)!)
