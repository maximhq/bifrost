## ✨ Features

- **`objectStorageExcludeFields`** - Configurable list of log payload fields that stay in the database instead of being offloaded to object storage
- **MCP External Base URL Split** - Split MCP external base URL into separate server and client URL fields for clearer reverse-proxy configuration

## 🐞 Fixed

- **Anthropic Integration Routing** - Skip model catalog routing when loadbalancer or governance routing has already selected the provider
- **Middleware API Key Auth** - Adjusted API key authentication handling in the middleware
- **Auth Config Disabled Context** - Update request context correctly when auth config is disabled
- **MCP Tool Field Resolution** - Resolve `tools_to_execute` and `tools_to_auto_execute` from existing config before validation in MCP client update
- **SCIM Page Layout** - Added `no-scrollbar` utility class and applied `no-padding-parent` to the SCIM page
- **SGL Extra Params Passthrough** - SGL provider now sets `BifrostContextKeyPassthroughExtraParams`, so SGLang vLLM-style extra-body params (`chat_template_kwargs`, `guided_json`, `guided_regex`, `separate_reasoning`) are no longer silently dropped (thanks [@hensapir](https://github.com/hensapir)!)
- **Bedrock Structured-Output Streaming** - Suppress non-tool content events (text deltas, reasoning, non-tool content-block starts) when structured output mode is active, preventing prose from corrupting the assembled JSON
- **`BifrostError` String Output** - Added `String()` method so logged errors render as JSON instead of decimal byte dumps
- **Streaming Latency Validation** - Allow zero-millisecond latency values (valid for sub-millisecond cache hits)
- **`NewUnsupportedOperationError` Context** - Now populates `Provider` and `RequestType` in `ExtraFields`

## 🔧 Maintenance

- **Streaming Accumulator Raw Request** - Moved raw request extraction to final chunk processing in the streaming accumulator
- **Provider Capability Matrix** - Re-enabled `ContextEditing` and `ContextManagementField` for Vertex; disabled `TaskBudgets` for Azure (not documented upstream); added `claude-4.6-sonnet` support to Bedrock test account
- **Schema Normalizer** - Added raw-byte JSON schema normalizer (`NormalizeSchemaForAnthropicRaw`) to avoid map round-trips during Anthropic schema preparation
- **Auth Middleware Context Keys** - Added `IsAPIKeyAuthContextKey` (short-circuit when API-key auth already passed) and `IsLocalAdminContextKey` (bypass RBAC when auth is disabled)
