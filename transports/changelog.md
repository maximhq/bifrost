## ✨ Features

- **Passthrough Streaming Accumulation** — Added accumulator for passthrough streaming responses, enabling proper logging and cost tracking on raw provider streams
- **Auto-Resolve Provider** — Inference and integration routes now auto-resolve the provider when no provider prefix is given on the model name
- **Per-Request Content Logging Overrides** — Opt-in per-request overrides for content logging and raw request/response visibility, with DB migrations and live-reload
- **Unified Dimension Headers (`x-bf-dim-*`)** — New unified dimension headers automatically forwarded to logs, traces, Prometheus, and Maxim tags
- **OpenAI Realtime Audio (Base64)** — Audio base64 encoding support for OpenAI realtime provider (thanks [@Mahmoud-Khater](https://github.com/Mahmoud-Khater)!)
- **Local Cache Hit Rate Speedometer** — Dashboard speedometer showing local cache hit rate (thanks [@loss-and-quick](https://github.com/loss-and-quick)!)
- **VK-Scoped Model Lists** — Model list endpoints now scoped to virtual-key-allowed providers and models via request headers
- **MCP Reverse Proxy OAuth** — External base URL support for reverse-proxy MCP OAuth flows
- **`schemas.Duration` Type** — Go duration string support for MCP, Redis, Weaviate, and mocker duration fields
- **Finish Reasons in OTEL Root Spans** — Finish reasons added to root spans, with correct model and provider names propagated
- **Routing Rules Scope Cache** — Cache routing rules per scope upfront, plus model-catalog routing engine label and icon

## 🐞 Fixed

- **OTEL Cost Info** — Fixed cost info in OTEL calls and response tools
- **Migrations Conflict Resolution** — Fixed migrations for conflicts
- **WebSocket /responses Reliability** — WebSocket responses now working with improved logging, cost tracking, and VK stripping
- **MarshalJSON Auto-Redaction** — Removed `MarshalJSON` auto-redaction; explicit redaction now applied to env-backed fields in `ProxyConfig`, `ClientConfig`, and `AzureKeyConfig`
- **Vertex `google/` Prefix** — Strip `google/` prefix from Vertex model IDs across all request types
- **Vertex Multi-Region Routing** — Multi-region-only models now route to multi-region endpoints when the provider key is configured for a single region only
- **OAuth Token `expires_at`** — `expires_at` is now nullable; refresh/reconnect guarded on nil expiry
- **OpenAI Responses Tool Fields** — Preserved tool fields in OpenAI responses (thanks [@princepal9120](https://github.com/princepal9120)!)
- **Semantic Cache Determinism** — Deterministic request hashing and `CacheDebug` propagation in streaming (thanks [@loss-and-quick](https://github.com/loss-and-quick)!)
- **Streaming Pool-Reuse Corruption** — Snapshot `RequestType` before closure to prevent pool-reuse corruption in streaming requests
- **Self-Looping Chain Rules** — Chain rules with self-loops now continue evaluating subsequent rules instead of halting
- **Default Routing Provider Filter** — Filter out unconfigured providers in default routing
- **Network Config Fallback for Ollama/SGL** — Fall back to network config if key config URL is not set for Ollama and SGL
- **`base_url` Backward Compatibility** — `base_url` added to `network_config` for backward compatibility
- **Streaming Pipeline `RawRequest`** — Propagate `RawRequest` through streaming pipeline and fix pool leak (thanks [@loss-and-quick](https://github.com/loss-and-quick)!)
- **Logging Streaming Errors** — Improved streaming error handling in logging plugin (thanks [@loss-and-quick](https://github.com/loss-and-quick)!)
- **`governance_budgets` Join** — Corrected join condition to use `virtual_key_id`
- **OTEL Input/Output Messages** — Fixed input/output messages propagation to root span
- **`resolvePeriod` UTC** — Fixed UTC handling in `resolvePeriod` time calculation
- **Dockerfile.local** — `Dockerfile.local` now uses local packages (thanks [@ReStranger](https://github.com/ReStranger)!)
- **Semanticcache Provider Keys** — Inherit provider keys from global client in semanticcache plugin

## 🔧 Maintenance

- **Helm Chart Upgrades** — Guardrails Helm chart upgrade; Helm `apply` step added; Kubernetes pod-discovery RBAC templates added
- **Dashboard UI Polish** — Popover scrolling, sheets/cluster page indentation, save-button validation, dialog overflow, fixed `ChartCard` heights, broader `ComboboxSelect` adoption (pricing, routing, assignment fields)
- **Plugin Lifecycle Logging** — Added log level param to `AppendRoutingEngineLog`; trimmed unused dependencies in semanticcache
- **OpenAPI Regeneration** — Regenerated `openapi.json`
