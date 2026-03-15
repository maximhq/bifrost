## ✨ Features

- **WebSocket based responses API** — Added WebSocket transport for responses API (OpenAI)
- **Anthropic Passthrough** — Added native Anthropic passthrough endpoint
- **Prompt Repository** — Added HTTP handlers for prompt management with RBAC (folders, prompts, versions, sessions)
- **Streaming Request Decompression** — Threshold-gated streaming decompression with pooled readers, replacing BodyUncompressed()
- **Model Parameters API** — Added model parameters table and API endpoint with in-memory caching
- **Virtual Key Limit Resets** — Added virtual key limit reset functionality
- **Session Stickiness** — Added session stickiness in key selection for consistent routing
- **Pricing Engine Refactor** — Unified cost calculation with quality-based image and video pricing
- **Image Configuration** — Added size/aspect ratio config for Gemini and size-to-resolution conversion for Replicate
- **Large Payload Support** — Added large payload awareness across transport hooks, plugins, and response streaming
- **Raw Request/Response Storage** — Allow storing raw request/response without returning them to clients (thanks [@Vaibhav701161](https://github.com/Vaibhav701161)!)
- **ChatReasoning Enabled Field** — Added Enabled field to ChatReasoning struct (thanks [@mango766](https://github.com/mango766)!)

## 🐞 Fixed

- **Deterministic Tool Schema** — Fixed deterministic tool schema serialization for Anthropic prompt caching (thanks [@Edward-Upton](https://github.com/Edward-Upton)!)
- **CORS Wildcard** — Fixed CORS issue with allowing * origin
- **TLS Termination** — Allow TLS termination inside Bifrost server through config
- **Bedrock toolChoice** — Fixed toolChoice silently dropped on Bedrock /converse and /converse-stream endpoints
- **Count Tokens Passthrough** — Fixed request body passthrough for count tokens endpoint for Anthropic and Vertex
- **Chat Finish Reason** — Map chat finish_reason to responses status and preserve terminal stream semantics
- **Tool Call Indexes** — Fixed streaming tool call indices for parallel tool calls in chat completions stream
- **Video Pricing** — Fixed video pricing calculation
- **SQLite Migration** — Prevented CASCADE deletion during routing targets migration
- **Log Serialization** — Reduced logstore serialization overhead and batch cost updates
- **Log List Queries** — Avoid loading raw_request/raw_response in log list queries (thanks [@Vaibhav701161](https://github.com/Vaibhav701161)!)
- **MCP Reconnection** — Improved MCP client reconnection with exponential backoff and connection timeout
- **Responses Input Messages** — Set responses input messages in gen_ai.input.messages
- **Helm Fixes** — Fixed Helm chart and test issues
