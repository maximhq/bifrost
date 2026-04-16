## ✨ Features

- **Claude Opus 4.7 Support** — Added compatibility for Anthropic's Claude Opus 4.7 model, including adaptive thinking, task-budgets beta header, `display` parameter handling, and "xhigh" effort mapping

## 🐞 Fixed

- **Gemini Tool Outputs** — Handle content block tool outputs in Responses API path for `function_call_output` messages (thanks [@tom-diacono](https://github.com/tom-diacono)!)
- **Bedrock Streaming** — Emit `message_stop` event for Anthropic invoke stream and case-insensitive `anthropic-beta` header merging (thanks [@tefimov](https://github.com/tefimov)!)
- **Bedrock Tool Images** — Preserve image content blocks in tool results when converting Anthropic Messages to Bedrock Converse API (thanks [@Edward-Upton](https://github.com/Edward-Upton)!)
- **Gemini Thinking Level** — Preserved `thinkingLevel` parameters across round-trip conversions and corrected finish reason mapping
- **Anthropic WebSearch** — Removed the Claude Code user agent restriction so WebSearch tool arguments flow for all clients
- **Responses Streaming Errors** — Capture errors mid-stream in the Responses API so transport clients see failures instead of silent termination
- **Anthropic Request Fallbacks** — Dropped fallback fields from outgoing Anthropic requests to avoid schema validation errors
- **Async Context Propagation** — Preserve context values in async requests so downstream handlers retain request-scoped data
- **Custom Providers** — Allow custom providers without a list-models endpoint to accept any model rather than restricting on virtual key registration
- **OTEL Plugin** — Default `insecure` to `true` in config.json and include fallbacks in emitted OTEL metrics
- **Payload Marshalling** — Removed unnecessary marshalling of payload in the transport path
- **Helm mcpClientConfig** — Fixed templating for `mcpClientConfig` (thanks [@crust3780](https://github.com/crust3780)!)
- **Helm Chart** — Refreshed the helm chart with validation fixes and removed the prerelease tag
