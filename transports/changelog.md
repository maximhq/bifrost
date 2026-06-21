## ✨ Features

- **Failed-Request Billing** — Bill for tokens a provider already processed when a stream is cancelled or times out, so partial usage is still accounted (closes #3357)
- **Airgapped Local Sync** — Model catalog pricing and model parameters can now sync from local files for airgapped deployments (closes #4305)
- **MCP Extra Header Forwarding** — Allowlisted per-request extra headers are now forwarded to MCP tool calls, including `ping`/`list_tools`
- **Business Unit & User Tracing** — Traces now capture business unit and user names/IDs for richer attribution
- **Dashboard Export** — Added a dashboard export endpoint to download dashboard data
- **Virtual Key Rankings** — New Virtual Key Rankings tab in the dashboard
- **Group Traces by Sessions** — Traces can now be grouped by session via config.json and Helm
- **Guardrails Evaluation Mode** — Exposed evaluation mode in guardrail schemas

## 🐞 Fixed

- **Anthropic Streaming** — Fixed duplicate `message_start` event in the Anthropic stream (closes #4556)
- **Bedrock Streaming** — Encode Bedrock stream errors as EventStream exceptions, fixing ChecksumMismatch / corrupted EventStream on PostLLMHook errors (closes #4545) (thanks [@jstar0](https://github.com/jstar0)!)
- **Bedrock MCP Tools** — Strip provider-unsupported server tools (e.g. `mcp`) on the Bedrock/Anthropic Responses path instead of failing the whole request, restoring pre-v1.5.0 behavior (closes #3795)
- **Bedrock Streaming Buffering** — Send `Accept-Encoding: identity` on Bedrock streams so responses arrive incrementally instead of buffering until generation completes (fixes TTFB collapse) (closes #4542)
- **OpenRouter Server Tools** — Preserve OpenRouter server tools (`openrouter:web_search`) on the OpenAI Responses path instead of stripping them in `filterUnsupportedTools` (thanks [@abdenasseraroukhsiss](https://github.com/abdenasseraroukhsiss)!)
- **Vertex Image URLs** — Remote image URLs are now inlined to base64 for Anthropic-on-Vertex, which rejects url-source images (previously only documents were inlined)
- **Bedrock MiniMax** — Fixed Bedrock signature handling for MiniMax models
- **Bedrock Nova** — Fixed Nova model handling on Bedrock
- **Bedrock Batch** — Corrected model id in Bedrock batch requests
- **Feature Gating** — Resolve model names for feature gating; return 403 correctly and fix 403 errors on list-models requests
- **Governance on OAuth** — Run governance on Claude Code OAuth requests when a virtual key is present; removed the skip-key-selection check from the pre-LLM hook
- **Conflict Handling** — Return 409 for Conflict errors
- **Container Delete** — Added explicit content-type header for container delete
- **File Serving** — Handle URL-encoded file names in URL params for the single-file serving endpoint
- **Plugins Config** — Fixed redaction setting in plugins config (#4486)
- **Migrations** — Fixed canonical_model_view migration

## 🔧 Maintenance

- **Network Defaults** — Updated default network config timings
- **Dependencies** — Dependabot dependency updates

## 🐙 Closed GitHub Issues

- [#2887](https://github.com/maximhq/bifrost/issues/2887) — Listing models for a virtual key not associated with all providers adds errors to logs
- [#3357](https://github.com/maximhq/bifrost/issues/3357) — Bifrost Billing Discrepancy Analysis Report
- [#3795](https://github.com/maximhq/bifrost/issues/3795) — MCP tools fail with Bedrock provider in v1.5.0
- [#4305](https://github.com/maximhq/bifrost/issues/4305) — Model parameters can't be configured locally
- [#4530](https://github.com/maximhq/bifrost/issues/4530) — /v1/responses strips OpenRouter server tools (openrouter:web_search) via filterUnsupportedTools
- [#4542](https://github.com/maximhq/bifrost/issues/4542) — Bedrock streaming responses buffered server-side — TTFB matches total generation time
- [#4545](https://github.com/maximhq/bifrost/issues/4545) — Bedrock streaming ChecksumMismatch / corrupted AWS EventStream
- [#4556](https://github.com/maximhq/bifrost/issues/4556) — Duplicate `message_start` SSE frame on Anthropic passthrough
