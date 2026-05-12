## ✨ Features

- **Push/Pull Telemetry Toggling** - Push-based and pull-based telemetry can now be toggled separately, and plugin hot-reload is fixed (#3433)
- **MCP Tool Manager Config Hash** - `mcp.tool_manager_config` is now included in the client config hash and synced on reload (#3432)

## 🐞 Fixed

- **OTEL Metric Fixes** - Resolved issues in the OTEL plugin metrics path (#3439)
- **Gemini Raw Request Scoping** - Raw-request handling is now applied only for the Gemini provider (#3437)
- **Gemini Fallback Propagation** - Fixed Gemini fallback propagation in the GenAI integration (#3338) (thanks [@Javtor](https://github.com/Javtor)!)
- **Anthropic Advisor Model Passthrough** - Fixed model passthrough prefix stripping in the advisor tool for Anthropic (#3420)
- **Empty Text/Signature Messages on Bedrock** - Drop messages with empty text or signature and convert thinking blocks for OpenAI Bedrock models (#3221)
- **Anthropic Trailing Assistant Messages** - Drop the last assistant message for Anthropic models and convert unsupported reasoning effort values for Mistral (#3203)
- **Compat Defaults Enabled** - All compat plugin settings are now enabled by default (#3202)
- **System-Only Message Role Conversion** - Convert role `system` to role `user` when only a system message is present for non-OpenAI models (#3200)
- **Compat CachePoint Drop** - Compat plugin now drops `cachePoint` for unsupported Bedrock models and non-Bedrock models (#3154)
