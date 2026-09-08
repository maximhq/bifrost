## ✨ Features

- **Virtual Key Rotation Grace Period** - New rotation-state columns on `governance_virtual_keys` (`previous_value`, `previous_value_hash`, `previous_value_expires_at`, `rotated_at`) with encryption support, plus a `vk_rotation_cooldown` client config setting (duration string, default 0 = immediate flip) controlling how long a rotated-out key value keeps authenticating.
- feat: persist provider `prompt_cache` configuration (new `prompt_cache_json` column and migration), including on the read path cluster peers use to reload after a config broadcast
- feat: add scheduled automatic virtual key rotation on access profiles via `auto_rotation_interval` (#6806)
- feat: add the project governance dimension to log tables, filters, histograms, rankings and matviews with `project_ids` and `project` dimension support (#6702, #6704, #6705)
- feat: add virtual MCP tables, CRUD, endpoint slugs for MCP clients and virtual key assignments (#6746, #6747, #6750, #6791, #6826)
- feat: persist Databricks key config with DB columns, encryption, redaction, validation and merge support, and register Databricks in the pricing catalog (#6666, #6669)
- feat: persist GitHub Copilot app credentials with encryption and redaction (#6354)
- feat: add `grants` package for per-request identity, access and limits and wire grant creation into governance (#6306, #6307, #6308, #6641)
- feat: add `allow_all_providers` to virtual keys with a hash backfill migration and `VirtualKeyPruneGuard` (#6662, #6863)
- feat: replace `framework/batchaccounting` with `framework/jobaccounting`, generalise `batch_jobs` into a provider job table with `kind` and `params`, settle video jobs at completion, and add resolution-banded video pricing columns (#6672, #6673, #6674, #6675, #6728)
  <Warning>
  `TableBatchJob` is now `TableProviderJob` and the `JobStore`, `SweepStore`, `AggregateLogEmitter` and `UsageReporter` interfaces are renamed. Go consumers of the accounting package must update.
  </Warning>
- feat: add semantic complexity routing config wire, migrations, vector stores with the chromem backend, LLM classifier fallback, session-aware routing, warm coordination and generation reclamation, and `complexity_*` plus `session_id` log columns (#6163, #6164, #6166, #6177, #6317, #6722, #6727, #6807, #6846)
- feat: add `request_id` exact lookup, `compare_to_previous` on stats, `served_model`, `tool_call_names`, and `hidden_request_types` visibility filtering to the log store (#6693, #6694, #6719, #6890, #6911)
- feat: add cross-endpoint `GET /api/webhooks/deliveries` search with filter indexes (#6708)
- feat: record `MCPConnectionFailure` and OAuth token `status_reason` (#6794)
- feat: replace user-scoped model configs with `ExtraScopedIDsResolver`, add structured `SourceRef` on model configs and quota budgets, multi-scope filtering, `ScopedModelLimits`, and list every contributing rate limit in quota responses (#6715, #6729, #6752, #6800, #6829, #6858)
- feat: remove the plugin `version` field and reconcile plugins by config hash, support `SecretVar` in native plugin config, and preserve plugin `created_at` (#6250, #6600, #6935)
- feat: add native raw request redaction and `RawStreamTextCodec` support (#6365, #6386)
- feat: add `compat_azure_deepseek` client config column (#6737)
- feat: wire tracing into async job settlement (#6939)
- fix: emit billed usage token and cost attributes on failed requests (thanks [@vdemonchy](https://github.com/vdemonchy)!) (#6259)
- fix: preserve `created_at` across config sync and updates for budgets, rate limits, teams, customers, model configs, pricing overrides and routing rules (#6616, #6792)
- fix: encrypt rows one per transaction with cursor pagination and concurrent vault writes to prevent deadlocks and boot hangs (#6808)
- fix: drop legacy FK constraints on `oauth_user_tokens` and `oauth_user_sessions` that blocked MCP client deletion, and resolve the MCP client ID before vault hooks run (#6648, #6812)
- fix: delete stale routing rules inside the merge transaction and honour the row-visibility query scope on rule reads (#6638, #6934)
- fix: handle hex value fields and remove the score filter in the Redis vector store (#6772, #6773)
- fix: make filter data search case-insensitive across SQLite, Postgres and ClickHouse (#6915)
- fix: scope prompt child reads and writes to their parent prompt (#6761)
- fix: honour `allowed_models: ["*"]` when the live list-models store is empty for a provider (#6767)
- fix: patch fasthttp to remove races when closing streaming calls (#6799)
