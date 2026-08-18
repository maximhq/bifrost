## ✨ Features

- **MCP Per-User OAuth** - MCP clients can hold per-user OAuth credentials and per-user headers, configurable from `config.json` as well as the UI, with a documented shared vs per-identity token lookup contract and VK/Users filters on the OAuth Grants and MCP Auth Sessions sidebars
- **Token Exchange IDP Credentials** - New `use_idp_credentials` on `token_exchange` reuses SSO login app credentials for providers that require it, such as Microsoft Entra ID; `client_id` becomes optional when it is set (#6068, #6069)
- **Bedrock VPC Endpoints** - AWS Bedrock keys can target VPC endpoints (#6064)
- **Per-Request Flat-Fee Pricing** - New `cost_per_request` field flows through datasheet sync, the cost engine, custom overrides and the UI override form (#6079)
- **Pricing Overrides in the Model Catalog** - `/api/models/details` exposes resolved pricing overrides, and catalog rows resolve overrides server-side (#6055, #6056)
- **MCP Tool Discovery Persistence** - Discovered MCP tools persist and resync uniformly across all client types through a hash-gated core callback, surviving restarts and propagating across a cluster
- **W3C Trace ID Propagation** - Requests carry a W3C trace ID on the context (#5945)
- **Cancellable Log Cost Recalculation** - Log cost recalculation tasks can be cancelled from the backend (#5801)
- **Separate OTEL Metrics Pipeline** - The OTEL collector supports a metrics tab independent of traces, plus separate headers for traces and metrics (#5939, #5940)
- **Roots-Only Log Filter** - New `roots_only` filter collapses fallback chains into their root entry with child aggregates (#5737)
- **MCP Log Redaction and Plugin Logs** - MCP tool logs carry redaction mappings and plugin logs (#5744, #5746)
- **User Agent and App Attribution in Logs** - Logs and MCP tool logs record user agent, app, source, decision, app key and device ID
- **S3 Log Export Metadata** - Additional metadata is written alongside S3 log exports (#6070)
- **Matview Maintenance Off Switch** - `matview_refresh_interval` accepts `"off"` to disable logstore matview maintenance entirely (thanks [@jeremym-tanium](https://github.com/jeremym-tanium)!) (#5693)
- **Video Request Info in Logs UI** - Video requests surface their details in the logs UI (#5946)
- **Shell Rewriter Hook** - The UI handler exposes a `ShellRewriter` hook for pre-hydration HTML rewriting (#5807)
- **Auth Skip Path** - Adds a context path letting trusted internal callers bypass auth resolution
- - **Runware passthrough** - Adds `runware_passthrough` path for handling passthrough mode for Runware provider

## 🐞 Fixed

- **GenAI SSE Heartbeats** - GenAI streams delimit heartbeat comments so Google SDK clients preserve the following event (thanks [@dani29](https://github.com/dani29)!) (#6240)
- **Path Normalization Auth Bypass** - Fixed a path normalization flaw that allowed auth to be bypassed (#5763)
- **Minimal Reasoning Effort on GPT-5 Models** - `reasoning_effort: "minimal"` is preserved for GPT-5-family OpenAI models instead of being downgraded to `low` (thanks [@jitokim](https://github.com/jitokim)!) (#6046)
- **Gemini Truncated Response Finish Reason** - Truncated Gemini responses report `MAX_TOKENS` instead of `OTHER` (thanks [@AdityaPainuli](https://github.com/AdityaPainuli)!) (#5979)
- **Null Tool-Call Function Name on Streaming** - Streaming continuation deltas no longer materialize an absent tool-call function name as `null` (thanks [@AdityaPainuli](https://github.com/AdityaPainuli)!) (#5966)
- **Bedrock Document Uploads** - Fixed Bedrock file handling in inference so office and PDF documents sent as OpenAI `type: "file"` are accepted (#5947)
- **xAI Usage Cost** - Fixed USD cost ticks for xAI usage (#5950)
- **Anthropic Encrypted Reasoning** - Added an Anthropic error branch when stripping encrypted reasoning content
- **MCP Reconnect and Lock Ordering** - Broke a lock-order inversion in `ConnectionCheckerManager`, rebuilt ephemeral clients across the whole connect+init retry, preserved last-known tool maps across close-first reconnects, bound connect attempts to entry identity, deduped background reconnects and gated SSE `OnConnectionLost` on connection identity
- **MCP OAuth Session Correctness** - Restricted `Reauthorize` to shared OAuth clients, rejected inactive tokens in `ValidateToken`, made the OAuth flow claim atomic against concurrent reauth, stopped dropping stored scopes on decode failure, and closed a verify-headers double-submit race that also dropped TLS, timeout and per-user-header fields
- **Session Stickiness Reconciliation** - `needs_session_stickiness` is pinned across `config.json` reconciliation, so an unrelated file edit can no longer silently revert a client to per-call
- **Credential Cache Cancellation** - `headerCredentialCache.Fill` and `userTokenCache.Fill` propagate context so a cancelled request unblocks instead of waiting on an unrelated leader; LRU entries carry a version so a rejected stale `Get` cannot evict a concurrently-updated value
- **Governance List-Models Call** - Budgets and rate limits no longer trigger a list-models call (#6051)
- **Realtime Response Create Input** - Guarded `response.create` input (#6050)
- **HTTP Server Timeouts** - Configured bounded `http.Server` timeouts and a request-body limit
- **MCP Client State Badges** - State badges render with spaces instead of underscores, and the state filter bucket was renamed from `disconnected` to `unstable`
- **Entra OBO Scope** - `offline_access` is combined with `<audience>/.default` for Entra OBO instead of replacing it (#6078)

## 🔧 Maintenance

- **Governance Route Families** - Editions can override governance route families (#5839)
- **Dependency Upgrades** - Dependabot updates across all modules, plus module path fixes (#6040, #5864)
- **Documentation** - config.schema.json doc fixes and Datadog env var reference fixes in the helm chart docs (#5938, #6019)

## 🗄️ Database Migrations

**configstore:**

- **add_mcp_client_pending_oauth_config_json_column** - Adds `pending_oauth_config_json` to `config_mcp_clients`. Reversible: drops the added column.
- **merge_oauth_token_tables** - Consolidates `oauth_tokens` and `oauth_user_tokens` into `mcp_oauth_tokens`. **Non-reversible**: rollback deliberately leaves `mcp_oauth_tokens` in place, because every OAuth read and write targets it from this migration onward and dropping it would destroy any token created or refreshed since, forcing every holder to re-authorize.
- **create_mcp_oauth_flows_table** - Creates `mcp_oauth_flows` to track in-flight OAuth flows. Reversible: drops the new table.
- **drop_oauth_config_pkce_columns** - Drops CSRF state, PKCE verifier and `expires_at` from the OAuth config table now that they live on `mcp_oauth_flows`. **Non-reversible**: forward-only, the dropped values were per-flow ephemeral and re-adding empty columns would restore nothing.
- **drop_oauth_config_token_id_column** - Drops `token_id`. **Non-reversible**: forward-only, it was a pure FK shortcut now reachable via `(oauth_config_id, auth_mode)`.
- **add_mcp_admin_auth_mode_indexes** - Adds admin partial unique indexes on `mcp_oauth_tokens` and `mcp_per_user_header_credentials`. Reversible: drops both indexes.
- **add_mcp_client_token_exchange_json_column** - Adds `token_exchange_json` to `config_mcp_clients`. Reversible: drops the added column.
- **add_needs_session_stickiness_column** - Adds `needs_session_stickiness` to `config_mcp_clients`. Reversible: drops the added column.
- **add_bedrock_endpoints_columns** - Adds Bedrock VPC endpoint columns to the keys table. Reversible: drops the added columns.
- **add_cost_per_request_pricing_column** - Adds `cost_per_request` to model pricing. Reversible: drops the added column.

**logstore:**

- **logs_add_guardrail_debug_column** - Adds `guardrail_debug` to logs. Reversible: drops the added column.
- **mcp_tool_logs_add_redaction_mapping_column** - Adds the redaction mapping column to MCP tool logs. **Non-reversible**: rollback is a no-op because dropping the column would permanently destroy reveal data for already-redacted MCP logs.
- **logs_add_user_agent_column** - Adds user agent and app columns, their indexes, and a `UserAgentMapping` table. Reversible: drops the indexes and the mapping table.
- **mcp_tool_logs_add_user_agent_column** - Adds user agent and app columns plus indexes to MCP tool logs. Reversible: drops both indexes and the `app` column.
- **mcp_tool_logs_add_endpoint_columns** - Adds `source`, `decision`, `app_key` and `device_id` to MCP tool logs. Reversible: drops all four columns.
- **mcp_tool_logs_add_plugin_logs_column** - Adds `plugin_logs` to MCP tool logs. Reversible: drops the added column.
- **logs_recreate_matviews_with_user_agent_column** and **logs_recreate_matviews_with_app_column** - Recreate the log materialized views to include the new columns. Rollback is a no-op because `ensureMatViews` recreates them on next startup.

<Warning>
**High-throughput deployments: run the logstore migrations during a low-activity window.**

Every logstore migration above alters `logs` or `mcp_tool_logs`, the two highest-insert tables in Bifrost, and several also build indexes on them. On a busy instance the index builds hold locks that block concurrent log inserts for the duration of the build, and the matview recreations rebuild against the full table. Schedule the upgrade for a low-traffic period, or expect elevated log-write latency and possible request-path backpressure while the migrations run.
</Warning>

<Warning>
`merge_oauth_token_tables`, `drop_oauth_config_pkce_columns` and `drop_oauth_config_token_id_column` transform or remove existing OAuth state and cannot be rolled back. Take a database backup before upgrading, and do not roll the binary back past this release once the migration has run.
</Warning>

## 🐙 Closed GitHub Issues

- [#123](https://github.com/maximhq/bifrost/issues/123) - Files API Support
- [#5472](https://github.com/maximhq/bifrost/issues/5472) - [Bug]: Bedrock rejects office/PDF document uploads via OpenAI `type:"file"` - "The PDF specified was not valid"
- [#5900](https://github.com/maximhq/bifrost/issues/5900) - [Bug]: Streaming continuation chunks materialize omitted tool-call metadata as null
- [#5978](https://github.com/maximhq/bifrost/issues/5978) - [Bug]: Gemini egress reports truncated responses as FinishReason OTHER, IncompleteDetails switch matches a string that never occurs
- [#6044](https://github.com/maximhq/bifrost/issues/6044) - [Bug]: normalizeOpenAIReasoningEffort maps 'minimal' to 'low' for ALL OpenAI models, even ones that natively support 'minimal'
