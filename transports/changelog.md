## ✨ Features

- **`objectStorageExcludeFields`** - Configurable list of log payload fields that stay in the database instead of being offloaded to object storage
- **MCP External Base URL Split** - Split MCP external base URL into separate server and client URL fields for clearer reverse-proxy configuration

## 🐞 Fixed

- **Anthropic Integration Routing** - Skip model catalog routing when loadbalancer or governance routing has already selected the provider
- **Middleware API Key Auth** - Adjusted API key authentication handling in the middleware
- **Auth Config Disabled Context** - Update request context correctly when auth config is disabled
- **MCP Tool Field Resolution** - Resolve `tools_to_execute` and `tools_to_auto_execute` from existing config before validation in MCP client update
- **SCIM Page Layout** - Added `no-scrollbar` utility class and applied `no-padding-parent` to the SCIM page

## 🔧 Maintenance

- **Streaming Accumulator Raw Request** - Moved raw request extraction to final chunk processing in the streaming accumulator
