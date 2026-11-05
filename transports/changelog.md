## ✨ Features

- **Trusted Forwarded Public Host** - Added deploy-time `server.trusted_forwarded_host`, disabled by default, to use the first trimmed `X-Forwarded-Host` value for public base URLs only when it exactly matches the configured hostname and optional port, case-insensitively. Otherwise `Host` is used; valid explicit public URL overrides still win, scheme and base path are preserved, and the incoming `Host` is not rewritten
  Invalid authorities are rejected before store initialization, including ports outside `0`–`65535`. OAuth client metadata adds `Vary: X-Forwarded-Host` without replacing existing cache variation fields.
- **MCP Per-User Tool Discovery** - Per-user OAuth and per-user headers clients can set `per_user_discovery`, so each caller sees the tools, prompts and resources their own credential unlocks on the MCP gateway. Discovery is stored at sign-in, shared across callers with identical results, and removed with the credential. A per-caller connect tool, caller counts in the sessions table and registry, and a Refresh tools action on the sessions page come with it. Turning it off sends a per-user OAuth server back to pending verification
- **MCP Apps on the Gateway** - `/mcp/<slug>` serves MCP Apps tools and `ui://` resources as the server sent them, to callers that declared MCP Apps. Aggregate `/mcp` never lists app-only tools and namespaces `ui://` URIs per client. App-only tools never reach models, tools are accepted by the names their own server gave them, and MCP logs record Apps use
- **MCP Multi Round-Trip** - Bifrost passes an upstream server's `input_required` result through to a gateway caller that can answer it, and forwards callers' declared capabilities upstream. MCP logs record each round and link a call's rounds by `round_trip_id`
- **Faster Boot with MCP Clients** - MCP clients from config.json connect in the background, so an unreachable client no longer delays the HTTP listener
- **Serve Under a Base Path** - `server.base_path` (or `-base-path` / `BIFROST_BASE_PATH`) serves the API and UI under a URL prefix. Redirects, OAuth bootstrap URLs, webhook `result_url`, MCP discovery and every copyable URL in the UI carry the prefix. Helm gets `basePath`
- **SOCKS5 and TLS-Inspecting Global Proxies** - The global proxy accepts `socks5`, takes a `ca_cert_pem` to trust a TLS-inspecting proxy, and always verifies targets through the proxy. The Inference and API toggles now take effect
- **Anthropic Workload Identity Federation** - Anthropic keys (and custom providers with an Anthropic base) can carry `anthropic_key_config.workload_identity` instead of a static key. Bifrost exchanges the workload's OIDC token for a short-lived access token and refreshes it before expiry. The key form gets an API Key / Workload Identity tab
- **Caller Identity on Logs** - LLM, MCP and relay logs record the caller's user email and, with `logs_store.capture_client_ip` on, their client IP. Both show on log pages and in CSV exports, and export to object storage
- **Relay Logs Match LLM Logs** - The relay log store, API and UI reach parity with LLM logs: session and correlation ids, large payload previews, filters and capture behaviour. The relay detail header is redesigned and OpenAPI import moves to a side sheet
- **Multi-Value Metadata Filters** - Log metadata filters accept several values per key
- **Gemini Interactions Billing** - On `/genai_passthrough`, the Interactions agent is recorded as the model, poll records name it, and each run is billed and counted as one request
- **Container and Image Tool Billing** - Containers started through OpenAI and Azure code interpreter and hosted shell, and Anthropic code execution, are counted on `usage.tool_usage.container_sessions` and billed at the `container` rate, including under `previous_response_id` and `conversation`. OpenAI image generation tool tokens are billed at the image model's rates and show as `image_cost`
- **OpenAI 12h Prompt Cache** - Prompt cache breakpoints accept a TTL, and 12h cache writes are priced from new datasheet columns
- **Complexity Router Confidence Thresholds** - Decision model routers take confidence thresholds before they act on a classification
- **Optional Routing Rule IDs** - config.json routing rules no longer need an `id`. A rule without one matches the stored rule by name and scope, and a priority collision with an undeclared stored rule logs a warning instead of stopping startup
- **Dashboard Languages** - The dashboard is fully translated with i18next across 11 locales. Numbers and dates follow the selected language, non-English catalogs lazy-load, and the language toggle lives in client settings (thanks [@occcat](https://github.com/occcat)!)
- **Command Palette** - Press ⌘K to jump anywhere in the workspace
- **MCP Server UI** - The MCP server sheet splits into a read-only view and a settings editor, tool parameters toggle between table and JSON, extra access grants have their own registry, and code mode settings collapse into an accordion
- **Sticky Log Message Filter** - The log detail messages filter persists across reloads
- **Duration Strings for `tool_sync_interval`** - Accepts values like `30m` alongside bare minutes

## 🐞 Fixed

- **Anthropic Prompt Caching on Chat Messages** - Message-level `cache_control` is kept on chat completion messages, and OpenAI-style `prompt_cache_breakpoint` markers become `cache_control` for Claude on Anthropic and Bedrock (thanks [@Atharva-Kanherkar](https://github.com/Atharva-Kanherkar)!)
- **Zero Token Usage** - Explicitly reported zero token counts from OpenAI-compatible providers are preserved instead of dropped (thanks [@Constantine3](https://github.com/Constantine3)!)
- **Anthropic Web Tool Versions** - Explicit web search and web fetch versions are kept instead of upgraded, and web search and code execution are dropped for Claude on Bedrock (thanks [@ykakade](https://github.com/ykakade)!)
- **Anthropic Empty Content** - Responses requests drop empty text blocks instead of failing with a 400, and text documents with empty `data` keep their source type (thanks [@david-choi-querypie](https://github.com/david-choi-querypie)!)
- **Claude Haiku 5.x Parameters** - Haiku 5.x takes reasoning effort like Sonnet 5.x, sampling params are dropped, and custom Anthropic-based providers read their base provider's capabilities (thanks [@macdod](https://github.com/macdod)!)
- **Codex on Claude** - Codex's client-side tool search runs as the caller's own, a tool re-declared mid-session no longer fails as a duplicate after namespace flattening, and invalid tool arguments in history replay as `{}` instead of a 500
- **Codex on OpenAI Responses** - Assistant messages carrying `encrypted_content` keep their content, and a `function_call_output` with a name and no `call_id` keeps its name
- **Gemini Item IDs** - Non-streamed Gemini item ids stay under 64 characters, reasoning items get ids, and Bedrock Mantle replay keeps them
- **vLLM and SGLang Reasoning** - Unsigned thinking blocks and replayed reasoning content are kept, and raw `reasoning_text` stream events emit Anthropic thinking deltas
- **Streamed Web Search Query** - Streamed Anthropic `web_search` blocks always carry the search query
- **DeepSeek V3.2 on Bedrock** - Leaked DSML tags are stripped, and `stopSequences` is omitted for Converse models that reject it
- **Databricks Safeguards** - The Anthropic-only `safeguards` param is stripped from Databricks chat and Responses requests
- **GitHub Copilot Tool Search** - Tool search works without the beta header Copilot rejects
- **Chat to Responses Conversion** - Assistant text next to tool calls is kept
- **Runware Async Output** - Bifrost polls the task when a sync request turns async
- **Multi-Key Stored Objects** - Follow-up calls on a stored response, container or video find the key that created it instead of returning 404
- **Routing Rule Fallbacks on 4xx** - Routing-rule fallbacks run when the primary target returns a 4xx
- **Pricing Model Names** - Pricing lookups match model names case-insensitively, and Azure Model Router deployments bill the served model's tokens
- **Dropped Requests Skipped Post-Hooks** - Requests rejected before reaching a provider worker (full queue, shutdown, cancelled wait) run their post-hooks, and telemetry no longer counts them as upstream traffic
- **ClickHouse Log Detail at Scale** - Opening a single log no longer scans the whole logs table (thanks [@17jmumford](https://github.com/17jmumford)!)
- **Log Retention Lag** - Postgres retention runs hourly, keeps draining past a short batch, and lets one replica drain each table. ClickHouse dedupes deletes per cutoff
- **Fallback Chain Banner** - The banner shows only for real fallback attempts, not for session rows sharing a parent id
- **Realtime and Live Logging Headers** - Setup-request headers reach realtime and Live log rows
- **Config-File-Only Mode Logs** - The dashboard and MCP logs show in config-file-only mode
- **Unmatched API Paths** - Unknown `/api` paths return a JSON 404 instead of the dashboard shell (thanks [@pcbeingused333](https://github.com/pcbeingused333)!)
- **Config Sync** - Unchanged framework URLs skip the URL accessibility check, and a config.json MCP client keeps its stored id
- **Relay Passthrough** - Relays match passthrough for headers and streams
- **Drop-Params Key Aliases** - Supported-param lookups resolve key aliases
- **Complexity Router Subagents** - Complexity session state is scoped per subagent
- **MCP OAuth Server** - ChatGPT and other CIMD clients sign in, including documents that list grants the server does not offer (thanks [@camferguson](https://github.com/camferguson)!). A replayed authorization code revokes its grant, an expired virtual key cannot consent or refresh, a grant bound before an issuer change cannot refresh, a trailing slash in `issuer_url` no longer produces `//`, and every `/mcp` 401 carries `WWW-Authenticate`
- **MCP Connections** - Permanent connect errors stop retrying, invalid `ca_cert_pem` is refused, a failed enable stays unstable with its checker retrying, TLS and SSE header edits apply to the live client, a per-call credential refreshes once on a 401, a tool's own 401 is no longer treated as an upstream auth failure, and `tls_config` is kept on per-user headers clients
- **MCP Tool Calls** - Upstream tools are called by their exact name, a tool's error flag survives on `/mcp` and in Responses, every code mode call inside agent mode is logged, code mode meta tools go through governance, `tool_execution_timeout` applies to code mode and in-process clients, and timeout errors report the caller's deadline
- **MCP Admin API** - Client create, rename, reconnect, OAuth and virtual key mistakes return 400, 404 or 409 instead of 500. Unknown auth types and per-user auth on SSE and STDIO are refused, `vk_configs` are checked before an update applies, deleting a client reloads its virtual keys and drops its prices, and `max_instructions_length` is returned in the client list
- **MCP Per-User Auth** - A revoke is not undone by a sign-in finishing during it, pending OAuth flows can be revoked from the sessions list, per-user headers keep their sample values as the admin credential, a stored OpenAPI credential cannot fill a caller-supplied header, and sign-in prompts no longer create MCP log rows
- **MCP Library** - Invalid catalog entries are skipped, permanent fetch errors stop retrying, and a custom entry can reuse a deleted entry's name

## 🗄️ Database Migrations

- **add_anthropic_workload_identity_column** - Adds `config_keys.anthropic_workload_identity_json`. Non-reversible: dropping it would delete every Anthropic key's workload identity config. Additive, so older binaries ignore it
- **add_mcp_per_user_discoveries** - Adds `config_mcp_clients.per_user_discovery` and the `mcp_per_user_discoveries` table with its indexes. Reversible: drops the indexes, table and column
- **add_mcp_client_pending_verification_column** - Adds `config_mcp_clients.pending_verification` and backfills it to true for OAuth and per-user clients still awaiting verification. Reversible: drops the column
- **add_cache_creation_above_12hr_pricing_columns** - Adds eight 12h cache write pricing columns. Reversible: drops them
- **mcp_tool_logs_add_round_trip_id_column**, **logs_add_client_ip_column**, **logs_add_user_email_column** - Add `round_trip_id`, `client_ip` and `user_email` to the log tables. Reversible only while the columns are empty: rollback is refused once any row holds a value
- **relay_logs_add_session_id**, **relay_logs_add_correlation_columns**, **relay_logs_add_large_payload_response** - Add session, correlation and preview columns to relay logs. Non-reversible: dropping them would lose recorded relay data
  <Note>
  ClickHouse log stores add bloom-filter skip indexes on `id` and `parent_request_id` and backfill them once with a background `MATERIALIZE INDEX`. Startup does not wait for it.
  </Note>

## 🐙 Closed GitHub Issues

- [#3261](https://github.com/maximhq/bifrost/issues/3261) - Routing-rule fallbacks not invoked when primary target returns 4xx (chat completions)
- [#6764](https://github.com/maximhq/bifrost/issues/6764) - Dashboard UI full multi-language i18n (i18next, 11 locales)
- [#8037](https://github.com/maximhq/bifrost/issues/8037) - OpenAPI spec omits the 100-row clamp and default of 25 on paginated list endpoints
- [#8712](https://github.com/maximhq/bifrost/issues/8712) - Message-level cache_control on chat messages is dropped, so Anthropic prompt caching never triggers
- [#8749](https://github.com/maximhq/bifrost/issues/8749) - Custom OpenAI-compatible provider responses drop prompt_tokens/completion_tokens - only total_tokens survives
- [#8849](https://github.com/maximhq/bifrost/issues/8849) - Anthropic route: a text document with empty `data` loses `source.type`, upstream 400
- [#9150](https://github.com/maximhq/bifrost/issues/9150) - Anthropic /v1/messages overrides the client's explicit web_search version, implicitly enabling code_execution
- [#9316](https://github.com/maximhq/bifrost/issues/9316) - ClickHouse logs store: single-log detail scans the whole logs table, so the log drawer never loads
- [#9318](https://github.com/maximhq/bifrost/issues/9318) - Anthropic Responses converter forwards empty text blocks, upstream rejects with 400
- [#9324](https://github.com/maximhq/bifrost/issues/9324) - Persist message-type filter selection in the chat UI
- [#9490](https://github.com/maximhq/bifrost/issues/9490) - Claude Haiku 5.5 behind a custom Anthropic-base provider: reasoning effort is dropped and temperature is forwarded
