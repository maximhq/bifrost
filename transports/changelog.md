## ✨ Features

- **Direct API Key Header** - Pass a provider API key directly via request header (#3817)
- **MCP Per-User Authentication** - New per-user header auth type with credential storage and lazy-auth submission flow (#3703, #3704, #3705)
- **MCP TLS Configuration** - Configurable TLS (insecureSkipVerify, caCertPem) for HTTP/SSE MCP client connections (#3779, #3783)
- **MCP Sessions Management** - Filter, search, and pagination on the MCP sessions list API and table, plus a can_reauth identity gate (#3823, #3824, #3825)
- **Tool Call Execution UI** - Inline tool-call execution, stop streaming, bulk execute/submit, and a redesigned tool-call UI (#3837, #3843)
- **Dimension Rankings Dashboard** - New dashboard tabs for team, customer, BU, and user rankings, backed by a GetDimensionRankings API (#3766)
- **Model Pricing Attributes** - additional_attributes on model pricing rows with management API and UI editor (#3829)
- **Prompt Cache Retention** - Prompt cache retention parameter on responses requests (#3810)
- **Opus 4.8 Support** - System message handling and compatibility for Opus 4.8 (#3878, #3868)
- **Key Rotation** - Rotate keys on 401/402/403 and return 502 upstream_credentials_exhausted when all keys are permanently dead (#3491)
- **OTel Metrics** - OTel spec compatible metrics plus provider and semantic cache attributes in metrics export (#3865, #3816)
- **Sheet Navigation** - Prev/next keyboard navigation and URL state across virtual key, MCP client, and routing rule sheets (#3739, #3740, #3744, #3745)
- **Go 1.26.3** - Upgraded toolchain to Go 1.26.3 (#3782)

## 🐞 Fixed

- **Bedrock Tool Names** - Truncate Bedrock function/tool names to the provider length limit
- **Bedrock Guardrails** - Set guardrail config in Bedrock request built from responses (#3862)
- **Anthropic Tool Use** - Default Anthropic tool_use input to {} when arguments are absent (#3880)
- **Responses Streaming** - Fixed responses stream events (#3838)
- **Compat Flow** - Fixed missing parameter parsing on the compat flow (#3881)
- **Passthrough API Version** - Set a default API version in passthrough requests as a fallback (#3853)
- **Virtual Key Updates** - Avoid overriding optional fields during virtual key update (#3855)
- **User-Mode Flows** - Gate user-mode flows on caller user_id, skip temp token mint, and unify flow/credential kind filtering for pending flows (#3841, #3859)
- **Partial Tool Calls** - Handle partial tool call execution failures and return successful results (#3849)
- **URL Query Escaping** - Support escaped characters in URL query parameters (#3826)
- **MCP Auth Errors** - Inline banner and retry support for MCP auth-required errors (#3856)
- **JSON Editor Height** - Cap JSON editor max height at 400px in message views (#3842)
