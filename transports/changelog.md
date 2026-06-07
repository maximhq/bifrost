## ✨ Features

- **OpenAI Compaction** — Added OpenAI conversation compaction support across core, framework, logging, and the API surface (#4053)
- **Multi-Customer & Org Hierarchy** — Logs and usage tracking now support multiple customers, teams, and business units, including business unit CRUD, team assignment, and governance endpoints in the OpenAPI spec (#4066, #4041, #4082)
- **Provider-Level Governance** — Budgets & limits are now scope-aware and can be applied at the virtual-key top level and per provider, wired from the model configs table, with UI filters for scope and providers (#3938, #3937, #3939, #3981, #3962)
- **Customer Budgets** — Customers support multiple budgets and `calendar_aligned` budget windows (#3998, #3997)
- **Virtual Key Attribution & Controls** — Added a `created_by` user attribution column and a `blacklisted_models` column for virtual key provider configs (#3672, #3653)
- **Request Header Capture** — OTel and Maxim observability plugins capture `request_headers` by pattern, with wildcard support (e.g. `x-custom-*`); logging gained the same wildcard header capture (#4012, #3958)
- **OTel Content Controls & Collectors** — New `disable_content_logging` option drops message/tool content from exported spans, plus support for multiple OTel collectors (#4064, #3894)
- **xAI x_search** — Added xAI `x_search` tool support (#3976)
- **URL Validation** — Added fetch URL validation with private-network configuration and link-local blocking (#3947, #3991)
- **File Scheme Pricing URLs** — Pricing source URLs now accept the `file://` scheme for air-gapped and self-hosted deployments (#4045)
- **Paginated Virtual Keys** — Virtual key fetching is paginated to handle deployments with very large numbers of keys (#3957)
- **Client IP Resolution** — Resolve client IP from `X-Forwarded-For`/`X-Real-IP` headers
- **SCIM Provisioning** — Added `attributeType`/`attributeValue` SCIM provisioning fields
- **Helm/Config Schema** — Added `roles` RBAC governance config and `per_user_oauth` MCP auth to the Helm chart and config schema (#4004, #4009)
- **Log Navigation UI** — Added a "View logs" menu item to customer, team, and virtual key tables, clickable links in log detail views, a customer detail sheet, and a reusable `BudgetDisplay` component (#4073, #4054, #4026, #4055)
- **Faster First Paint** — Added an inline loading shell to `#root` before React mounts (#4063)
- **Materialized View Alias** — Added an `alias` column to the materialized view with filter support (#4078)

## 🐞 Fixed

- **Fetch URL IP Checks** — Hardened fetch URL IP checks against SSRF (#4092)
- **Mantle Model Matching** — Broadened Mantle model matching to all `gpt` variants (#4091)
- **Empty Thinking Blocks** — Strip thinking blocks when the signature is empty (#4079)
- **OpenAI Stream Usage** — Removed usage from the `responses.created` event in the OpenAI stream (#4080)
- **Prompt Cache Key** — Set the prompt cache key from the Anthropic integration (#4086)
- **Upstream Failure Status** — Map upstream connection failures to 502 instead of 400 (#3929) (thanks [@chris-colinsky](https://github.com/chris-colinsky)!)
- **Gemini Schema Constraints** — Accept numeric schema integer constraints for Gemini (#3994) (thanks [@yanhao98](https://github.com/yanhao98)!)
- **Files Provider Param** — Accept the `?provider=` query param on `GET /v1/files` (#3971) (thanks [@alexef](https://github.com/alexef)!)
- **Optional Batch Model** — Made the `model` field optional on `POST /v1/batches` (#3973) (thanks [@alexef](https://github.com/alexef)!)
- **Helm Azure Config** — Added missing `azure_key_config` fields to the Helm schema (#3996) (thanks [@axelray-dev](https://github.com/axelray-dev)!)
- **Text Completion Chunk Model** — Added the missing `Model` field to `TextCompletionChunkResponse` (#3970) (thanks [@kuishou68](https://github.com/kuishou68)!)
- **MCP Inline stdio Env** — MCP stdio server configs accept inline environment variable assignments (#3861) (thanks [@Shushmitaaaa](https://github.com/Shushmitaaaa)!)
- **Orphaned Tool Results** — Orphaned tool results in the OpenAI to Anthropic conversion flow are no longer rejected by the Anthropic API (#3919)
- **Node Usage Reconciliation** — Added a monotonic `inc_number` log cursor so node usage reconciliation does not skip late async log writes (#3664)
- **Bedrock Output Assessments** — Corrected the type of `outputAssessments` in Bedrock responses (#4028)
- **Model Pool Pricing Reloads** — Preserve non-pricing model pool entries across pricing reloads (#3999)
- **Ghost Node Reconciliation** — Replicate the VK hierarchy flow for ghost node reconciliation (#4088)
- **VK Double Usage Counting** — Fixed double usage counting when creating a virtual key (#4070)
- **Model Config Lifecycle** — Cascade deletes for model configs and removal of stale in-memory model configs (#4051, #4043)
- **FTS Index Cap** — Reduced the FTS index `left()` cap from 800k to 250k chars to stay within the tsvector limit (#4057)
- **Sync Worker Drift** — Reduced the sync worker ticker period to 5m to prevent threshold drift (#4023)
- **Passthrough** — Fixed passthrough budgets, gated passthrough models per VK, model extraction for Azure passthrough, and restricted fallbacks/provider selection to the VK boundary (#3941, #3988, #3983, #3924)
- **Provider Response Headers** — Strip provider response headers and add a content-type filter (#3955, #4024)
- **Stream Handling** — Drain non-SSE stream readers and retry stale connections (#3956, #3967)
- **Azure Claude** — Strip Azure diagnostic property for Claude models (#3925)
- **Compat max_tokens** — Preserve chat `max_tokens` during param filtering (#3992)
- **Raw Request Flag** — Removed the raw request flag from providers that don't support it (#4058)
- **UI Fixes** — Standardized page container layout, virtual key model configs UI, and dashboard chart tooltips (#4046, #4052, #4044)

## 🔧 Maintenance

- **Dependency Upgrades** — Bumped transitive `golang.org/x` dependencies (crypto, net, sys, text) for Docker Scout CVE remediation and `recharts` to 3.8.1; cascaded version bumps across all modules (#3900, #4003)