## ✨ Features

- **Response Header Forwarding** — Forward provider response headers back to clients for all providers
- **Streaming Compression** — Added streaming compression support for provider responses
- **Bedrock Invoke & Count Tokens** — Added bedrock invoke support and count tokens for bedrock provider
- **Async Rerank** — Added async support for rerank requests
- **Dashboard Graphs** — Added new dashboard graphs and graph fixes
- **Gzip for Azure Speech** — Added gzip compression for azure speech streams
- **Grayswan Guardrails** — Added grayswan guardrails configuration
- **E2E Tests** — Added e2e tests for API
- **Azure Managed Identity** — option to provider key form (DefaultAzureCredential when no API key or Entra ID)
- **Bedrock STS AssumeRole** — support (role_arn, external_id, session_name) in key config for cross-account access


## 🐞 Fixed

- **Parallel Tool Call Deltas** — Route parallel tool call argument deltas by id/index to prevent argument merging during streaming (thanks [@KTS-o7](https://github.com/KTS-o7)!)
- **Count Tokens Route** — Count tokens route fixed to match OpenAI schema
  <Warning>
    **Breaking change.** The count tokens route has moved from `/v1/count_tokens` to `/v1/responses/input_tokens`, and the request body field has been renamed from incorrect `messages` to `input`. Please update your clients accordingly.
  </Warning>
- **Cached Tokens Handling** — Standardized cached tokens handling for all providers
  <Note>
    **Breaking change.** Cache tokens moved from usage top-level into `prompt_tokens_details` (Chat) and `input_tokens_details` (Responses), with standardized keys. If you have persisted data (logs, analytics, or custom storage), migrate: (1) move `usage.cache_read_input_tokens` → `usage.prompt_tokens_details.cached_read_tokens` or `usage.input_tokens_details.cached_read_tokens`; (2) move `usage.cache_creation_input_tokens` → `usage.prompt_tokens_details.cached_write_tokens` or `usage.input_tokens_details.cached_write_tokens`.
  </Note>
- **Team update** fixes team update flow by handling nil customer id
- **Bedrock Integration Routing** — Added missing routing logic for bedrock integration
- **Nil Tool Properties** — Handle nil properties in tool function parameters
- **Logprobs JSON Tag** — Fixed logprobs JSON tag in BifrostResponseChoice (thanks [@robechun](https://github.com/robechun)!)
- **Provider Deletion** — Fixed delete providers bug, empty provider list bug, and deleting provider not loaded in memory
- **Data Connectors** — Fixed enabling/disabling data connectors
- **OpenRouter Keys** — Added proper status check for openrouter keys
- **Governance Virtual Key** — Added governance wrong virtual key check
- **Pricing Config** — Normalized framework pricing config
- **Model Limit Validation** — Fixed model limit form validation
- **Dashboard Height** — Minor dashboard height fix
