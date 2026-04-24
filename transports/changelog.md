## ✨ Features

- **MCP Tool Groups** — Added `tool_groups` config with governance scoping (virtual key, team, customer, user, provider, API key) and camelCase Helm aliases for MCP client fields
- **Enterprise Helm Overlays** — Suite of composable overlays for guardrails, org governance, access profiles, customer budgets, teams, multi-customer governance, and SCIM/SSO
- **Semantic Cache Helm Layers** — Added `values-semantic-search-redis.yaml` and `values-semantic-search-weaviate.yaml` for Redis and Weaviate-backed semantic caches, plus a client-config overlay
- **Period Parameter in Dashboard/Logs APIs** — Added `period` param for relative time range queries on dashboard and logs endpoints
- **`provider_key_name` Alias** — Human-readable alias for routing targets and pricing overrides, resolved to `key_id` at config load time
- **`env.*` References for Proxy and TLS** — Proxy and TLS config fields (`url`, `username`, `password`, `ca_cert_pem`) now accept `env.VAR_NAME` for secret injection
- **MCP Duration Strings and Hash Reconciliation** — `tool_sync_interval` accepts Go duration strings; hash-based reconciliation prevents unnecessary MCP client restarts on config reload
- **Auto-fill Incoming Model for Fallbacks** — Routing rule fallback entries can omit the model; the incoming request model is substituted automatically at runtime
- **Namespace Tool Type** — Namespace tool container type in Responses API; non-OpenAI providers receive automatically flattened tool lists
- **Cache Creation Pricing** — Cache creation details for Claude models with 5-minute and 1-hour TTL pricing tiers
- **Governance Config Sync** — Model configs and provider governance bindings now sync from `config.json` to DB at startup
- **`business_units` and Team Fields** — Added `business_units`, `team_id`, `calendar_aligned`, and `virtual_key_count` to governance schema and Helm

## 🐞 Fixed

- **WebSocket /responses Reliability** — Fixed upstream handshake diagnostics, proper error capture, and WebSocket connection lifecycle in the native `/responses` path
- **Raw Request Passthrough Removed** — Removed `SendBackRawRequest` from all provider passthrough flows; passthrough streaming now sets proper SSE headers
- **WebSocket Nil Checks** — Improved `sendMessageSafely` nil guards, panic recovery, and client cleanup
- **Routing Rule Query Normalization** — Normalized `query` field to valid `RuleGroupType` and tightened schema validation
- **PydanticAI Null Text Fields** — Normalized null text content in PydanticAI stream response chunks
- **Budget and Team Co-creation** — Fixed creation of budgets and teams in the same request
- **Provider Reload** — Fixed keyless provider status updates during config reload; provider runtime now reloads correctly after key creation
- **OTel Metrics** — Fixed OpenTelemetry metrics pipeline not working (thanks [@tcx4c70](https://github.com/tcx4c70)!)
- **OTel Export** — Fixed OTEL exporting to correctly show input and output messages
- **Logging Request Type** — Resolved request type from pending data before streaming to prevent missing `Object` field in error logs
- **Multipart File Uploads** — Write multipart metadata before file content to fix upload ordering
- **Env Var Redacted Check** — Added missing redacted check for env var values
