✨ Features

- Custom SSE Stream Reader — Replaced fasthttp’s default stream reader with a custom implementation to reduce bursts in SSE streaming
- MCP Config Validation — Added validation for MCP tool configurations in config.json
- Max Open Connections — Exposed max-open-connections for provider domains as a configurable field
- Dashboard improvements - Added a bunch of new tabs and new graphs to the dashboard including Model Ranking, Cache usage, MCP usage etc
- Improved dashboard and logs performance - We have improved LLM logs and Dashboard UI performance (~1400x faster) for large number of logs.
​
🐞 Fixed

- Passthrough Streaming — Fixed passthrough streaming responses being buffered instead of streamed (thanks @jasonwells!)
- MCP Notifications — Fixed MCP notifications returning incorrect status code (thanks @tcx4c70!)
- Streaming Function Calls — Fixed function_call items not included in streaming response.completed output (thanks @majiayu000!)
- Bedrock API Key Auth — Fixed Bedrock API key authentication without requiring bedrock_key_config (thanks @eliasbiondo!)
- Bedrock Token Count Fallback — Added fallback to estimated token count when count-tokens API is unsupported (thanks @Edward-Upton!)
- Anthropic Thinking Fixes — Fixed OpenAI-to-Anthropic-to-OpenAI thinking content conversion
- Anthropic Header Selection — Fixed Anthropic header selection across providers
- Gemini OpenAI Integration — Fixed Gemini flow for OpenAI-compatible integration
- Semantic Cache Hashing — Fixed deterministic tools_hash and params_hash in semantic cache (thanks @ragokan!)
- Anthropic Compaction — Added compaction support for Anthropic provider