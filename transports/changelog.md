## ✨ Features

- **Projects (Enterprise)** - Named, budgeted governance scope a request opts into with the `x-bf-project-id` or `x-bf-project-name` header. Managed via `/api/governance/projects` and `governance.projects` in config.json (access rule, members, budgets, rate limits, provider configs, `allow_all_providers`). `project_id`/`project_name` land on logs, MCP tool logs, span attributes and metric labels, with a `project_ids` filter, a `project` histogram and ranking dimension, a Governance → Projects page and a Project Rankings dashboard tab. A request naming a project it cannot use is refused with `access_blocked` (#6702, #6703, #6704, #6705, #6777, #6878, #6896, #6897, #6898, #6952)
- **Virtual MCPs** - Replace MCP Tool Groups with named tool bundles drawn from one or more MCP clients, managed at `/api/mcp/virtual-mcps`, declared under `mcp.virtual_mcps` in config.json (`mcp.tool_groups` is deprecated) and Helm `bifrost.mcp.virtualMcps`, assignable to virtual keys from the VK sheet, and served at `/mcp/<endpoint_slug>`. Direct MCP clients also get an `endpoint_slug` and are reachable at `/mcp/<slug>`. Tool whitelisting is now explicit: `["*"]` grants all tools, `[]` grants none, a named list grants exactly those. Disabled clients and stale definitions are never served. `/workspace/mcp-tool-groups` redirects to the new Virtual MCPs page (#6746, #6747, #6748, #6749, #6750, #6751, #6791, #6826, #6872, #6873, #6874, #6904, #6905, #6919, #6957)
- **Databricks Provider** - First-class `databricks/<model>` provider covering Model Serving and Unity AI Gateway with PAT or OAuth M2M auth via `databricks_key_config` (`workspace_url`, `api_format`, `client_id`/`client_secret`, `forward_gateway_tags`). Requests are sanitized per model against datasheet capabilities, `reasoning_effort` is translated to Anthropic `thinking` on Claude endpoints, remote images are inlined, native Responses calls fall back to chat emulation, and upstream error messages are surfaced. The UI adds the key form and a guided migration from a custom provider named `databricks` (#6665, #6666, #6667, #6668, #6669, #6670, #6671, #6676, #6679, #6770, #6876, #6958)
- **GitHub Copilot Provider** - `github_copilot` provider that mints installation tokens server-to-server from GitHub App credentials (`github_copilot_key_config`: `app_id`, `installation_id`, `repository_id`, `private_key`, optional `github_domain`), supporting chat completions, Responses and list models with cost tracked in GitHub AI Credits. Configurable through config.json and the API only: it is hidden from the Add Provider picker pending release testing (#6352, #6353, #6354, #6355, #6356, #6357, #6895)
- **Semantic Complexity Routing** - The keyword scorer in the complexity router is replaced by an embedding-based classifier over three tiers with curated exemplar phrases (backfilled by migration, 750 combined phrase cap), a pluggable vector store including an embedded `chromem` backend with cross-node warm coordination, an optional LLM classifier fallback (`semantic.fallback: llm`), and session-aware routing that keeps a session at its highest observed tier. New status, generations and retry endpoints under `/api/routing/complexity-analyzer-*`, `complexity_*` log columns and filters, and routing embedding/LLM request and cost counters. `tier_boundaries` is deprecated and ignored (#6163, #6164, #6165, #6166, #6167, #6168, #6177, #6282, #6317, #6722, #6727, #6807, #6838, #6846, #6865)
  <Warning>
  The routing metadata field on responses and log rows is renamed from `routing_debug` to `routing_metadata` with no alias. Governance error codes `virtual_key_not_found` and `virtual_key_blocked` are renamed to `access_not_found` and `access_blocked`. Update any consumer matching on those names.
  </Warning>
- **Per-Request Grants** - Every request now settles its identity (virtual key, MCP JWT, WebSocket key, ephemeral secret, GenAI session) onto one resolved access grant that governance checks, charges and filters with, so checked and billed limits cannot diverge and async jobs, WebRTC relays and WebSocket upgrades keep their identity. MCP runs a single shared server with tool visibility decided per request by governance admission, and every models listing (including integration routes) is narrowed by resolved access. `allow_on_all_virtual_keys` on MCP clients is renamed to `allow_by_default` (old key still accepted), and the list filter `all_virtual_keys` becomes `allowed_by_default` (#6306, #6307, #6308, #6309, #6311, #6313, #6314, #6641, #6642, #6643, #6649, #6678, #6701, #6706, #6768, #6801, #6857)
- **Virtual Key Rotation Cooldown** - New `client.vk_rotation_cooldown` setting (duration string, e.g. "5m"): after a rotation the previous key value keeps authenticating until the grace window expires. config.json VK sync now treats a changed value as an explicit rotation (with console warning) and recognizes the previously rotated-out value as "no change".
- **Scheduled Virtual Key Rotation** - Access profiles gain `auto_rotation_interval` (1h to 365d, off by default) with `next_rotation_at` and read-only `last_rotation_at`. A background job rotates managed keys in batches, honours the rotation cooldown, posts dashboard notifications and advances the schedule; a manual rotation inside the window is respected (#6806)
- **Allow All Providers on Virtual Keys** - `allow_all_providers` on virtual keys, projects and access profiles grants every configured provider, including ones added later, without listing them in `provider_configs`. Explicit provider entries still apply their model lists, and budgets and rate limits are unchanged. Exposed in the VK sheet, the API, config.json and Helm. A `VirtualKeyPruneGuard` stops config.json reconciliation from pruning access-profile-owned keys (#6662, #6663, #6827, #6863, #6875, #6953, #6954)
- **Prompt Cache Auto-Injection** - New provider `prompt_cache` block (`auto_inject`, `ttl`, `cache_control_injection_points`) synthesizes cache breakpoints for clients that send none, so agentic clients like Codex stop paying the cache-write rate every turn. Off by default, capped at four markers, never touches caller-supplied markers, overridable per request with `x-bf-prompt-cache-auto-inject`. Edited from a new Prompt Caching tab in the provider sheet and extended to the gpt-5.6 family (`prompt_cache_breakpoint` plus explicit cache mode) (#6697, #6698, #6699, #6700, #6753)
- **Azure DeepSeek Chat Completions Routing** - Responses requests to Azure DeepSeek models from coding harnesses are routed to Chat Completions because the DeepSeek Responses endpoint rejects `reasoning.effort`; models without a Responses endpoint fall back the same way, including on Bedrock Mantle. Controlled by `compat.azure_deepseek` (default true). The compat plugin also logs every dropped parameter and request-type conversion as structured per-request log entries (#6326, #6634, #6737)
- **Native Passthrough Redaction** - Guardrail PII redaction now applies to Anthropic Messages and Gemini GenAI passthrough traffic, rewriting only content-bearing fields, and to native SSE streams through a paused-buffer codec that rewrites `content_block_delta` text before release (#6365, #6386)
- **Video Job Accounting** - Async video generation is billed at settlement: a settler polls jobs to a terminal state, prices from captured params or provider-reported dimensions with new resolution-banded per-second rates (480p, 720p, 1024p, 1080p, 4k), records failures at zero and parks unpriceable jobs for backfill. The `batch_jobs` table is generalised into a provider job table with `kind` and `params` columns, and the log detail sheet gains a Video Details block (#6672, #6673, #6674, #6675, #6728, #6839)
- **Webhook Deliveries Page** - `GET /api/webhooks/deliveries` searches delivery history across all endpoints by endpoint, event, outcome, status class, request or delivery ID and time window, paginated by delivery group. A Webhooks → Deliveries page adds filters, live polling, manual redelivery and deep links from each endpoint, with a topbar breadcrumb trail (#6707, #6708, #6709, #6710, #6714, #6893)
- **Request ID Lookup and Period Comparison in Logs** - Logs, stats and histogram endpoints accept an exact `request_id` that bypasses the time window; the search box auto-detects a UUID or `id:` prefix. `GET /api/logs/stats?compare_to_previous=true` returns the previous period, powering a segmented metric strip with sparklines and change badges (#6694, #6695, #6719, #6720, #6788)
- **Hidden Request Types** - `logs_store.hidden_request_types` (Helm `storage.logsStore.hiddenRequestTypes`) hides whole request types such as `count_tokens` from every log read path without affecting writes, cost recalculation or access control; shown read-only under Config → Logging (#6890, #6891, #6892, #6894)
- **Tool Call Names Filter** - Logs gain a `tool_call_names` column, recorded even when content logging is off, with a matching filter on the logs and histogram endpoints and in the logs sidebar (#6911, #6912, #6913)
- **Served and Canonical Model in Logs** - The model the provider actually served is persisted as `served_model` and shown when it differs from the request, and the logs model column displays the canonical name with the requested name as fallback (#6602, #6693)
- **MCP Connection Failure Details** - `GET /api/mcp/clients` returns `last_failure` (stage, message, timestamps) and per-node `node_states`, OAuth tokens record a `status_reason`, and the server sheet shows the failure in the state badge popover plus a credential block with scopes, refresh-token presence and expiry (#6780, #6794, #6795, #6796)
- **Scoped Model Limits and Quota Sources** - Model configs and quota budgets carry a structured `SourceRef` naming what governs them, `GET /api/governance/model-configs` accepts a comma-separated `scope`, quota responses tag each budget and rate limit with its `source` and list every contributing `rate_limits` entry, and read-only scopes render as view-only in the UI with scope labels on budget and rate limit cells. Provider-scoped budgets now participate in load-balancing candidate exclusion (#6715, #6729, #6733, #6752, #6800, #6810, #6813, #6829, #6830, #6843, #6856, #6858, #6860)
- **Plugin Config Hash Reconciliation** - The plugin `version` field is removed from config.json, the API, Helm and docs; a leftover key is ignored. Plugin sync is now driven by a SHA-256 hash of the config entry, so a changed entry syncs automatically. Custom Go plugins can use `SecretVar` in their config, and a plugin's `created_at` survives updates (#6250, #6336, #6337, #6600, #6935)
- **Tracing Controls** - New `export_overhead_spans` toggle (Helm and the Configure Tracing sheet) controls whether internal overhead spans are exported, converter work is split into finer span buckets, batch and video settlement emit spans from the async sweeper, and a Latency and Overhead Breakdown docs page explains the log detail view (#6588, #6637, #6939, #6945)
- **Log Level Tabs for Plugin and Routing Logs** - Routing decision and plugin logs carry a level and can be filtered by it in the log detail view (#6811, #6814)
- **UI Improvements** - Custom providers whose name collides with a first-party integration prompt a switch, virtual key reveal and copy events are audited through an enterprise hook, access-profile-managed keys get a fallback creation view, sheets get a refreshed design with sticky headers, the logs page handles small screens, cached and uncached input tokens are broken down in a tooltip, and the Raw JSON tab explains when raw storage is disabled (#6596, #6608, #6610, #6618, #6619, #6651, #6786, #6789, #6790, #6793, #6820, #6833, #6845, #6871, #6940, #6951)
- **Helm Chart Updates** - Values and schema for projects, Databricks keys, access-profile mappings, VK rotation cooldown, virtual MCPs, `allow_all_providers`, hidden request types and guardrail `send_all_conversation_turns`; the SCIM block renders as-is when `enabled: false` (#6663, #6758, #6869, #6892, #6904, #6917, #6953)
- **Baseten on Hugging Face** - Baseten is discoverable as a Hugging Face inference provider (thanks [@nicolastoulemont](https://github.com/nicolastoulemont)!) (#6633)
- **Magic Hour in MCP Library** - Magic Hour added to the MCP library (thanks [@runshouse](https://github.com/runshouse)!) (#6691)

## 🐞 Fixed

- **Streaming Hangs and Connection Leaks** - A patched fasthttp fixes a race when closing streams, abandoned streams are drained in the background so the upstream connection returns to the pool, streams that send only heartbeats after `finish_reason` now terminate, and a `does_not_send_done_marker` toggle on custom providers ends the stream at `finish_reason` for upstreams that never send `[DONE]` (#6799, #6802, #6803, #6948, #6960)
- **DeepSeek Reasoning Lost on Multi-Turn** - Assistant `reasoning_content` is aliased instead of stripped for Groq and Cerebras, so thinking survives multi-turn requests on the OpenAI-compatible inbound (#6949)
- **Reasoning Summary Stream Events** - `summary_index`, summary text and signatures are populated on `reasoning_summary_*` events for Anthropic, Bedrock and Gemini (#6902)
- **Responses-to-Chat finish_reason** - Chat `finish_reason` is derived from the Responses terminal state and incomplete details instead of being dropped (#6901, #6920)
- **Anthropic Stream Truncation** - `response.incomplete` and `response.failed` emit proper `message_delta`, `message_stop` and `error` events instead of truncating the stream, and a missing terminal text suffix is synthesized from `output_text.done` (#6159, #6805)
- **Anthropic-on-Vertex Passthrough** - Usage and stream terminal detection for Anthropic models in Vertex GenAI passthrough mode go through the Anthropic parsers (#6639)
- **Anthropic Passthrough for Non-Claude Models** - Raw-body passthrough is cleared based on the resolved provider and model pair, so non-Claude models on Vertex, Azure and Bedrock Mantle are converted instead of forwarded as Anthropic payloads, and it is also cleared when the provider does not support the output config format (#6798)
- **Unsupported Reasoning Signature** - The encrypted reasoning signature is stripped when the upstream reports the field as unsupported, such as Bedrock Converse replaying a Claude signature onto a non-Anthropic model
- **Bedrock Reasoning Blocks** - Unsigned reasoning blocks are dropped from Converse replays to Claude, which rejects them, while Nova and MiniMax keep receiving them, and native Grok or OpenAI reasoning summaries on Converse responses are rendered instead of dropped (#6834, #6942)
- **Bedrock Null Content on Empty Assistant Messages** - An assistant message with no text and no tool calls no longer serializes as `content:null`, which Converse rejected outright (thanks [@Jesse-Schultz-Relativity](https://github.com/Jesse-Schultz-Relativity)!) (#6732)
- **Bedrock Model Routing to Converse** - Bedrock models route to the Converse API as intended (#6655)
- **GenAI Signature Drop** - Standalone `thoughtSignature` parts with empty text are no longer dropped on native GenAI (#6745)
- **Ollama max_tokens** - Ollama receives `max_tokens` instead of the unsupported `max_completion_tokens` (#6607)
- **Cohere Rerank Documents** - Rerank documents are sent as Cohere v2 strings rather than objects (thanks [@AdityaPainuli](https://github.com/AdityaPainuli)!) (#6654)
- **Nullable Response Fields** - Spec-required nullable response fields are marshalled as `null` instead of omitted (thanks [@PSR94](https://github.com/PSR94)!) (#6723)
- **Model Arrays from OpenAI-Compatible APIs** - Top-level arrays returned by OpenAI-compatible model listings are accepted (thanks [@dani29](https://github.com/dani29)!) (#6712)
- **GPT-6 Astra Reasoning Effort** - Max reasoning effort is preserved instead of being downgraded to high (thanks [@nettee](https://github.com/nettee)!) (#6881)
- **Forced Tool Choice** - Anthropic `tool_choice: any` maps to `required` on OpenAI egress, gated on the provider capability flag (thanks [@Atharva-Kanherkar](https://github.com/Atharva-Kanherkar)!) (#6888, #6903)
- **Azure Reasoning Efforts** - Reasoning effort handling for Azure-hosted models (#6877)
- **Thinking Block Modification Error** - Replayed thinking blocks no longer trigger a modification error (#6854)
- **Custom Provider in List Models** - Custom providers are skipped in list models when the request is not allowed to use them (#6853)
- **Vertex GenAI Model Names** - Vertex GenAI resource model names are normalised to bare IDs for governance and key selection (#6918)
- **Allowed Models Wildcard** - `allowed_models: ["*"]` no longer returns `model_blocked` when the live list-models store is empty for a provider (#6767)
- **OpenRouter Prompt Caching on Responses** - `cache_control` breakpoints are translated correctly for OpenRouter Claude models on the Responses API (#6692)
- **Plugin Config Reverted on Restart** - Plugin config edited via UI or API is no longer reverted from config.json on restart under `source_of_truth: split`; see Plugin Config Hash Reconciliation (#6250)
- **Realtime Observability and Auth** - WebSocket Responses turns emit `llm.call` spans, realtime auth survives KV replication, and realtime WebSocket and WebRTC routes honour `enforce_auth_on_inference` (#6592, #6759, #6943)
- **Budget State Preserved Across Edits** - Changing a budget's reset frequency or fiscal-quarter setting no longer resets accumulated usage or drops `quarter_start_month`, new model budgets start empty, and budget IDs survive edits (#6810, #6813, #6932)
- **Routing Rule Persistence** - Stale routing rules are deleted inside the merge transaction to avoid priority collisions, and rule reads honour the row-visibility query scope (#6638, #6934)
- **created_at Preserved on Sync** - `created_at` survives config sync and updates for budgets, rate limits, teams, customers, model configs, pricing overrides, routing rules and plugins (#6616, #6792, #6935)
- **Vault Encryption Deadlocks at Boot** - Rows are encrypted one per transaction with cursor pagination and concurrent vault writes, preventing deadlocks and boot hangs (#6808)
- **MCP Client Deletion** - Legacy FK constraints on `oauth_user_tokens` and `oauth_user_sessions` are dropped so deleting an MCP client no longer fails, and the client ID is resolved before vault hooks run (#6648, #6812)
- **SSRF Hardening for MCP** - Unauthenticated callers cannot register stdio MCP clients or private addresses, all MCP HTTP clients dial through the SSRF guard, and the Teredo prefix is blocked (#6757, #6760)
- **Rate Limits on Model-less Passthrough** - Rate limits apply to passthrough requests that carry no model (#6774)
- **Redis Semantic Cache** - Hex value fields are handled and the score filter is removed from the Redis store (#6772, #6773)
- **Prompt Child Scoping** - Prompt child reads and writes are scoped to their parent prompt (#6761)
- **Billed Usage on Failed Requests** - Tracing emits billed token and cost attributes on failed requests (thanks [@vdemonchy](https://github.com/vdemonchy)!) (#6259)
- **File Response MIME Type** - File responses carry the MIME type (#6684)
- **Logs Filter Search Case** - Filter data search is case-insensitive on SQLite, Postgres and ClickHouse (#6915)
- **UI Fixes** - Logout no longer cascades into 401s, the OSS build declares `VKCreationPolicyResponse` (thanks [@markdawson](https://github.com/markdawson)!), virtual key loading state is consistent, managed VK state uses the server flag, and background polling pauses while an edit sheet is open (#6610, #6776, #6793, #6855, #6859)
- **Dependency and Security Updates** - Dependabot and CodeQL fixes across modules (#6696, #6832, #6835, #6836, #6837)

## 🗄️ Database Migrations

- **backfill_default_complexity_exemplars_v2** - Rewrites the `complexity_semantic_config` governance row, appending curated default exemplar phrases and seeding the semantic row on pre-split installs. Non-reversible: appended default phrases cannot be distinguished safely from administrator-owned phrases.
- **add_vk_rotation_cooldown_columns** - Adds `previous_value`, `previous_value_hash`, `previous_value_expires_at`, `rotated_at` and an index to `governance_virtual_keys`. Reversible: rolls back by dropping the columns.
- **add_vk_rotation_cooldown_client_column** - Adds `vk_rotation_cooldown_ns` to `config_client`. Reversible: rolls back by dropping the column.
- **drop_legacy_oauth_user_fk_constraints** - Drops the MCP client and virtual key FK constraints on `oauth_user_tokens` and `oauth_user_sessions`. Reversible: recreates the constraints, which can fail if orphan rows accumulated meanwhile.
- **add_virtual_mcp_tables** - Creates the virtual MCP and VK-to-virtual-MCP tables, adds `endpoint_slug` and backfills unique slugs. Non-reversible: no rollback is defined.
- **add_video_resolution_pricing_columns** - Adds resolution-banded video per-second rate columns to `model_pricing`. Non-reversible: dropping them would permanently delete custom per-resolution prices; the columns are additive and older binaries ignore them.
- **add_provider_job_kind_columns**, **swap_provider_job_indexes** - Adds `kind` (default `batch`) and `params` to `batch_jobs` and swaps the identity and sweeper indexes to include `kind`, concurrently on Postgres. Reversible only while no non-batch jobs or captured params exist; otherwise the rollback refuses to avoid merging job namespaces and discarding pricing basis.
- **add_compat_azure_deepseek_column** - Adds `compat_azure_deepseek` to `config_client` and sets it true on existing rows. Reversible: rolls back by dropping the column.
- **clear_plugin_config_hashes** - Blanks `config_hash` on every plugin row so hash-based reconciliation starts clean. Non-reversible in effect: the rollback is a no-op.
- **add_mcp_oauth_token_status_reason_column** - Adds `status_reason` to `mcp_oauth_tokens`. Reversible: rolls back by dropping the column.
- **add_databricks_key_config_columns** - Adds the five `databricks_*` key columns. Reversible: rolls back by dropping the columns.
- **add_github_copilot_config_columns** - Adds the five `github_copilot_*` key columns. Non-reversible: dropping them would permanently delete stored GitHub App private keys, which GitHub only issues once; the columns are additive and older binaries ignore them.
- **add_mcp_client_endpoint_slug** - Adds `endpoint_slug` to `config_mcp_clients` and backfills a unique slug for every row, then builds the unique index concurrently. Non-reversible: the column and backfill step has no rollback; the index step drops cleanly.
- **add_allow_all_providers_to_virtual_key** - Adds `allow_all_providers` (default false) to `governance_virtual_keys`. Reversible: rolls back by dropping the column.
- **backfill_vk_allow_all_providers_hash** - Recomputes `config_hash` for every virtual key. Non-reversible in effect: the rollback is a no-op.
- **add_prompt_cache_json_column** - Adds `prompt_cache_json` to the provider table. Reversible: rolls back by dropping the column.
- **Log store** - Nine additive migrations (`logs_add_complexity_routing_columns`, `logs_add_session_id_column`, `logs_add_routing_metadata_column`, `webhook_deliveries_add_filter_indexes_v1`, `logs_add_video_debug_column`, `logs_add_project_columns`, `mcp_tool_logs_add_project_columns`, `logs_add_served_model_column`, `logs_add_tool_call_names_column`) add nullable columns to `logs` and `mcp_tool_logs` and filter indexes on `webhook_deliveries`. No backfill, no data rewrite. All reversible: each rolls back by dropping what it added.
  <Warning>
  This release adds new columns to the log store. Each `ADD COLUMN` takes an `ACCESS EXCLUSIVE` lock on `logs`, the highest-volume table, and on Postgres the migration waits at most 5 seconds for that lock before failing the boot and retrying on the next one. Upgrade during a low-activity window so the lock is acquired immediately and no queries queue behind it.
  </Warning>

  **To apply the log store schema ahead of the upgrade**, run the statements below against the log store database. They match what the migrator executes, and every statement is idempotent. After the DDL you must also record the nine migration IDs in the `migrations` table (shown after the SQLite block) so the next boot treats them as applied.

  Postgres:
  ```sql
  -- logs_add_complexity_routing_columns
  BEGIN; SET LOCAL lock_timeout = '5s';
  ALTER TABLE logs ADD COLUMN IF NOT EXISTS complexity_tier varchar(50);
  ALTER TABLE logs ADD COLUMN IF NOT EXISTS complexity_mechanism varchar(50);
  ALTER TABLE logs ADD COLUMN IF NOT EXISTS complexity_score decimal;
  COMMIT;
  -- logs_add_session_id_column
  BEGIN; SET LOCAL lock_timeout = '5s';
  ALTER TABLE logs ADD COLUMN IF NOT EXISTS session_id varchar(255);
  COMMIT;
  -- logs_add_routing_metadata_column
  BEGIN; SET LOCAL lock_timeout = '5s';
  ALTER TABLE logs ADD COLUMN IF NOT EXISTS routing_metadata text;
  COMMIT;
  -- webhook_deliveries_add_filter_indexes_v1 is a no-op on Postgres; its indexes are built concurrently below
  -- logs_add_video_debug_column
  ALTER TABLE logs ADD COLUMN IF NOT EXISTS video_debug text;
  -- logs_add_project_columns
  ALTER TABLE logs ADD COLUMN IF NOT EXISTS project_id varchar(255);
  ALTER TABLE logs ADD COLUMN IF NOT EXISTS project_name varchar(255);
  -- mcp_tool_logs_add_project_columns
  ALTER TABLE mcp_tool_logs ADD COLUMN IF NOT EXISTS project_id varchar(255);
  ALTER TABLE mcp_tool_logs ADD COLUMN IF NOT EXISTS project_name varchar(255);
  -- logs_add_served_model_column
  ALTER TABLE logs ADD COLUMN IF NOT EXISTS served_model varchar(255);
  -- logs_add_tool_call_names_column
  BEGIN; SET LOCAL lock_timeout = '5s';
  ALTER TABLE logs ADD COLUMN IF NOT EXISTS tool_call_names text;
  COMMIT;
  -- Indexes Bifrost builds after startup, outside a transaction
  CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_logs_complexity_tier ON logs(complexity_tier) WHERE complexity_tier IS NOT NULL;
  CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_logs_complexity_mechanism ON logs(complexity_mechanism) WHERE complexity_mechanism IS NOT NULL;
  CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_logs_session_id ON logs(session_id) WHERE session_id IS NOT NULL;
  CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_logs_tool_call_names_arr ON logs USING GIN (string_to_array(tool_call_names, ',')) WHERE tool_call_names IS NOT NULL;
  CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_logs_project_id ON logs(project_id);
  CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_mcp_logs_project_id ON mcp_tool_logs(project_id);
  CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_webhook_deliveries_endpoint_created ON webhook_deliveries(endpoint_id, created_at DESC);
  CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_webhook_deliveries_outcome ON webhook_deliveries(outcome);
  CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_webhook_deliveries_event ON webhook_deliveries(event);
  CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_webhook_deliveries_request_id ON webhook_deliveries(request_id);
  ```

  SQLite (no `IF NOT EXISTS` on `ADD COLUMN`; skip any column that already exists):
  ```sql
  ALTER TABLE logs ADD COLUMN complexity_tier varchar(50);
  ALTER TABLE logs ADD COLUMN complexity_mechanism varchar(50);
  ALTER TABLE logs ADD COLUMN complexity_score real;
  ALTER TABLE logs ADD COLUMN session_id varchar(255);
  CREATE INDEX IF NOT EXISTS idx_logs_session_id ON logs(session_id) WHERE session_id IS NOT NULL;
  ALTER TABLE logs ADD COLUMN routing_metadata text;
  CREATE INDEX IF NOT EXISTS idx_webhook_deliveries_endpoint_created ON webhook_deliveries(endpoint_id, created_at DESC);
  CREATE INDEX IF NOT EXISTS idx_webhook_deliveries_outcome ON webhook_deliveries(outcome);
  CREATE INDEX IF NOT EXISTS idx_webhook_deliveries_event ON webhook_deliveries(event);
  CREATE INDEX IF NOT EXISTS idx_webhook_deliveries_request_id ON webhook_deliveries(request_id);
  ALTER TABLE logs ADD COLUMN video_debug text;
  ALTER TABLE logs ADD COLUMN project_id varchar(255);
  ALTER TABLE logs ADD COLUMN project_name varchar(255);
  ALTER TABLE mcp_tool_logs ADD COLUMN project_id varchar(255);
  ALTER TABLE mcp_tool_logs ADD COLUMN project_name varchar(255);
  ALTER TABLE logs ADD COLUMN served_model varchar(255);
  ALTER TABLE logs ADD COLUMN tool_call_names text;
  ```

  Then record the migrations in the log store's `migrations` table (Postgres and SQLite). Run in this order. On SQLite use `CURRENT_TIMESTAMP` instead of `NOW()`:
  ```sql
  INSERT INTO migrations (id, sequence, applied_at, status) SELECT 'logs_add_complexity_routing_columns', COALESCE(MAX(sequence), 0) + 1, NOW(), 'success' FROM migrations;
  INSERT INTO migrations (id, sequence, applied_at, status) SELECT 'logs_add_session_id_column', COALESCE(MAX(sequence), 0) + 1, NOW(), 'success' FROM migrations;
  INSERT INTO migrations (id, sequence, applied_at, status) SELECT 'logs_add_routing_metadata_column', COALESCE(MAX(sequence), 0) + 1, NOW(), 'success' FROM migrations;
  INSERT INTO migrations (id, sequence, applied_at, status) SELECT 'webhook_deliveries_add_filter_indexes_v1', COALESCE(MAX(sequence), 0) + 1, NOW(), 'success' FROM migrations;
  INSERT INTO migrations (id, sequence, applied_at, status) SELECT 'logs_add_video_debug_column', COALESCE(MAX(sequence), 0) + 1, NOW(), 'success' FROM migrations;
  INSERT INTO migrations (id, sequence, applied_at, status) SELECT 'logs_add_project_columns', COALESCE(MAX(sequence), 0) + 1, NOW(), 'success' FROM migrations;
  INSERT INTO migrations (id, sequence, applied_at, status) SELECT 'mcp_tool_logs_add_project_columns', COALESCE(MAX(sequence), 0) + 1, NOW(), 'success' FROM migrations;
  INSERT INTO migrations (id, sequence, applied_at, status) SELECT 'logs_add_served_model_column', COALESCE(MAX(sequence), 0) + 1, NOW(), 'success' FROM migrations;
  INSERT INTO migrations (id, sequence, applied_at, status) SELECT 'logs_add_tool_call_names_column', COALESCE(MAX(sequence), 0) + 1, NOW(), 'success' FROM migrations;
  ```

  ClickHouse has no migration ledger; Bifrost reconciles missing columns on boot with `ADD COLUMN IF NOT EXISTS` (add `ON CLUSTER` when configured):
  ```sql
  ALTER TABLE logs ADD COLUMN IF NOT EXISTS complexity_tier Nullable(String);
  ALTER TABLE logs ADD COLUMN IF NOT EXISTS complexity_mechanism Nullable(String);
  ALTER TABLE logs ADD COLUMN IF NOT EXISTS complexity_score Nullable(Float64);
  ALTER TABLE logs ADD COLUMN IF NOT EXISTS session_id Nullable(String);
  ALTER TABLE logs ADD COLUMN IF NOT EXISTS routing_metadata String;
  ALTER TABLE logs ADD COLUMN IF NOT EXISTS video_debug String;
  ALTER TABLE logs ADD COLUMN IF NOT EXISTS project_id Nullable(String);
  ALTER TABLE logs ADD COLUMN IF NOT EXISTS project_name Nullable(String);
  ALTER TABLE logs ADD COLUMN IF NOT EXISTS served_model Nullable(String);
  ALTER TABLE logs ADD COLUMN IF NOT EXISTS tool_call_names Nullable(String);
  ALTER TABLE mcp_tool_logs ADD COLUMN IF NOT EXISTS project_id Nullable(String);
  ALTER TABLE mcp_tool_logs ADD COLUMN IF NOT EXISTS project_name Nullable(String);
  ```

## 🐙 Closed GitHub Issues

- [#2765](https://github.com/maximhq/bifrost/issues/2765) - Bedrock provider does not sanitize empty content blocks (regression from #1189 fix)
- [#5887](https://github.com/maximhq/bifrost/issues/5887) - DeepSeek thinking silently lost on ALL multi-turn requests via OpenAI-compat inbound (v1.6.7; regression from v1.6.3)
- [#6073](https://github.com/maximhq/bifrost/issues/6073) - GenAI passthrough in Vertex mode breaks Anthropic models
- [#6132](https://github.com/maximhq/bifrost/issues/6132) - Ollama provider: max_tokens / max_completion_tokens silently dropped from forwarded request
- [#6143](https://github.com/maximhq/bifrost/issues/6143) - data race - fasthttp requestStream released to pool while SSE reader is still inside Read (stream cancellation)
- [#6180](https://github.com/maximhq/bifrost/issues/6180) - explicit prompt cache for Bedrock Mantle GPT-5.6 Responses
- [#6265](https://github.com/maximhq/bifrost/issues/6265) - Realtime/WebSocket Responses turns produce no llm.call span, so span-based observability connectors export them unattributed
- [#6290](https://github.com/maximhq/bifrost/issues/6290) - OpenRouter Claude prompt caching remains broken on Responses API
- [#6434](https://github.com/maximhq/bifrost/issues/6434) - Plugin config edited via UI/API is reverted from config.json on every restart under source_of_truth: split
- [#6624](https://github.com/maximhq/bifrost/issues/6624) - Bedrock reasoning signature field is dropped for Anthropic models, which require it present
- [#6631](https://github.com/maximhq/bifrost/issues/6631) - add Baseten to Hugging Face inference providers
- [#6640](https://github.com/maximhq/bifrost/issues/6640) - v2.0.0 rerank sends documents as objects ({"text": ...}) to Cohere-based custom providers, breaking servers that expect Cohere v2 strings
- [#6657](https://github.com/maximhq/bifrost/issues/6657) - Fireworks virtual key with allowed_models: ["*"] blocks every model (empty synced catalog; explicit list works)
- [#6689](https://github.com/maximhq/bifrost/issues/6689) - Responses omit spec-required nullable fields
- [#6690](https://github.com/maximhq/bifrost/issues/6690) - [MCP Library] Add: Magic Hour
- [#6711](https://github.com/maximhq/bifrost/issues/6711) - Support array responses from OpenAI-compatible model APIs
- [#6730](https://github.com/maximhq/bifrost/issues/6730) - Native GenAI drops empty text from standalone thoughtSignature parts
- [#6775](https://github.com/maximhq/bifrost/issues/6775) - OSS ui typecheck fails since #6618 (VKCreationPolicyResponse missing from fallback types)
- [#6784](https://github.com/maximhq/bifrost/issues/6784) - Chat completion stream hangs forever after finish_reason when upstream omits [DONE] but keeps sending heartbeats
- [#6831](https://github.com/maximhq/bifrost/issues/6831) - Responses-to-Chat mux drops non-streaming finish_reason
- [#6880](https://github.com/maximhq/bifrost/issues/6880) - GPT-6 Astra max reasoning effort is silently downgraded to high
- [#6887](https://github.com/maximhq/bifrost/issues/6887) - Anthropic tool_choice {type: any} forwarded to OpenAI as "any" instead of "required"
- [#6914](https://github.com/maximhq/bifrost/issues/6914) - Team current spend is reset after adjusting budget limit even when choosing Preserve Usage
