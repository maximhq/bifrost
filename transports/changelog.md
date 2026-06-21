## ✨ Features

- **Disable Model Discovery** — New per-provider `disable_model_discovery` option skips the live model-list fetch (and its per-key connection fan-out); `/v1/models` then serves only the statically configured models, letting built-in providers like Azure opt out the way custom providers already can (#4581)
- **Business Unit & User Tracing** — Traces now capture business unit and user names/IDs for richer attribution
- **Dashboard Export** — Added a dashboard export endpoint to download dashboard data
- **Virtual Key Rankings** — New Virtual Key Rankings tab in the dashboard
- **Group Traces by Sessions** — Traces can now be grouped by session via config.json and Helm
- **Guardrails Evaluation Mode** — Exposed evaluation mode in guardrail schemas

## 🐞 Fixed

- **Anthropic Streaming** — Fixed duplicate `message_start` event in the Anthropic stream (closes #4556)
- **Bedrock Streaming** — Encode Bedrock stream errors as EventStream exceptions (#4545)
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
