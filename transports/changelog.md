## ✨ Features

- **Realtime Support** — Add WebSocket, WebRTC, and client secret handlers with session state management and transport context helpers
- **Fireworks AI Provider** — Add Fireworks AI as a first-class provider with native completions, responses, embeddings, and image generations (thanks [@ivanetchart](https://github.com/ivanetchart)!)
- **Per-User OAuth Consent** — Add per-user OAuth consent flow with identity selection and MCP authentication
- **Prompts Plugin** — New prompts plugin with direct key header resolver and selective message inclusion when committing prompt sessions
- **Access Profiles** — Add access profiles for fine-grained permission control
- **Bedrock Embeddings & Image Gen** — Add embeddings, image gen, edit and variation support to Bedrock
- **EnvVar Improvements** — Add IsSet method to EnvVar and auto-redact env-backed values in JSON serialization
- **Logging Tracking Fields** — Add support for tracking userId, teamId, customerId, and businessUnitId in logging
- **Virtual Keys Export** — Add sorting and CSV export to virtual keys table
- **Path Whitelisting** — Allow path whitelisting from security config
- **Server Bootstrap Timer** — Add server bootstrap timer for startup diagnostics

## 🐞 Fixed

- **Bedrock Tool Choice** — Fix bedrock tool choice conversion to auto
- **Bedrock Streaming Retries** — Retry retryable AWS exceptions and stale/closed-connection errors in bedrock streaming
- **Bedrock SigV4 Service** — Correct SigV4 service name for agent runtime rerank
- **MCP Tool Logs** — Fix MCP tool logs not being captured correctly
- **Routing Rule Targets** — Preserve routing rule targets for genai and bedrock paths
- **Provider Budget Duplication** — Fix provider level multiline budget duplication issue
- **Vertex Endpoint** — Fix vertex endpoint correction
- **Gemini Thinking Budget** — Fix thinking budget validation for gemini models
- **SQLite Migrations** — Fix SQLite migration connections, error handling, and disable foreign key checks during migration
- **Tool Parameter Schemas** — Preserve explicit empty tool parameter schemas for openai passthrough
- **List Models Output** — Include raw model ID in list-models output alongside aliases
- **Config Schema** — Fix config schema for bedrock key config
- **Data Race Fix** — Fix race in data reading from fasthttp request for integrations
- **Model Listing** — Unify /api/models and /api/models/details listing behavior
