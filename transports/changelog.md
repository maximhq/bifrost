## ✨ Features

- **Gemini Interactions API** - Gemini and Vertex serve Responses requests through Google's Interactions API when a request sets `previous_response_id`, sets `background: true`, names an agent such as Deep Research, or uses a Google-run `tool_search`, with retrieve, cancel and delete. The `/genai` integration adds typed `/interactions` routes in the Gemini and Vertex path shapes: native for Google, translated for other providers, so governance, logging, pricing and fallbacks apply. A run is billed once, and reads or cancels of a finished run carry no cost
- **Per-Key and Per-Alias Service Tier** - A key or a key alias can set `service_tier`. It is sent on chat completions and Responses requests in place of the request's own tier, and an alias's tier overrides its key's. A spare weight-0 key set to `priority` can then serve Priority traffic only when rotation reaches it
- **MCP OAuth Client Credentials** - New `client_credentials` auth type for upstream MCP servers. Bifrost mints one shared token with the OAuth client credentials grant (`client_secret_basic`, `client_secret_post` or `private_key_jwt`), caches it per client and backs off on a refused credential. Set from the MCP client form, config.json or Helm
- **MCP AWS SigV4 Auth** - New `aws_sigv4` auth type for upstream MCP servers. Bifrost signs each request with its own AWS identity from the default credential chain, with an optional assumed role, to reach IAM-protected servers such as Amazon Bedrock AgentCore Gateway. Set from the MCP client form, config.json or Helm
- **MCP OAuth on Several Hostnames** - `oauth2_server_config.browser_base_url` sets the host that users' browsers open for the consent and device flow pages, for MCP clients that reach Bifrost on a hostname browsers cannot resolve. `additional_issuer_urls` (up to 10) lets one instance act as the OAuth issuer on other hostnames. Both are editable under Security settings
- **MCP Progress Notifications** - The `/mcp` gateway relays upstream `notifications/progress` on `tools/call` to a caller that sends a `_meta.progressToken`, under the caller's own token and ahead of the result
- **MCP Session Connect** - `POST /api/mcp/sessions/connect` lets a signed-in user start their own sign-in to a per-user OAuth or per-user headers MCP client, and returns them to the page where the sign-in started. Revoke and refresh on another user's session need the matching permission
- **Relay Async Jobs** - A relay route can declare a `job` for APIs such as AWS Textract that start background work and report the price only when it finishes. Bifrost polls the job, charges it once when it ends, and relays callers' own status checks free with no extra route. Relay Logs show how each job ended, including unpriced and untracked jobs, and settled costs reach OTel and Prometheus. Set up from one sheet in the dashboard, config.json, the relays API or Helm
- **Relay Enforced Body Fields** - `body_overrides` on a relay or a route forces JSON fields into every request body, whatever the caller sent. A route's value replaces the relay's, and `inherit_body_overrides: false` leaves the relay's fields out of a route. The usage preview prices the body the relay sends
- **Relay Route Matching** - A route path can hold one `**` in the middle, such as `/jobs/**/status`. Routes are tried most specific first instead of in list order, and the dashboard lists them in that order
  <Note>
  A broad route no longer shadows a narrower one by sitting above it. A wildcard segment never matches `.` or `..`, and a dot-segment path that matches no route is refused.
  </Note>
- **OpenAI Prewarm and Background Options** - Responses requests accept `prompt_cache_options.prewarm`, `prompt_cache_options.comparison_response_id` and `background_options` as typed fields. A prewarm request gets its own semantic cache key
- **OpenAI Access Programs** - Responses requests accept `access_programs` (Daybreak) on HTTP and WebSocket, forwarded to OpenAI only, and the program that served the request is returned
- **Vertex Batch Embeddings** - Vertex Gemini embedding models accept more than one input per request through `:batchEmbedContents`
- **Bedrock Credential Caching** - A Bedrock key with no static credentials loads the AWS default credential chain once per region instead of on every request

## 🐞 Fixed

- **Root-Level Union Tool Schemas** - MCP tools whose input schema is a root-level `oneOf`, `anyOf` or `allOf` keep their parameters through discovery and `/mcp` listing. Each provider gets the union or one merged object, depending on what the model accepts (`supports_root_schema_composition` in the datasheet)
- **Bedrock Document Blocks** - `/bedrock` InvokeModel forwards Anthropic `document` blocks, including URL sources, and returns a 4xx for input it cannot forward instead of dropping it
- **Bedrock Tool Results** - The `/bedrock` invoke and Converse routes carry every member of a tool result, including images and documents, instead of only the first text or JSON member
- **Bedrock Reasoning-Only Responses** - A Converse response cut off at `max_tokens` while the model is still reasoning returns the reasoning block instead of an empty message
- **Bedrock Grok Reasoning Replay** - Signed reasoning from Grok models on Bedrock is replayed on later turns
- **OpenRouter Reasoning Details** - `reasoning_details` (signed thinking and redacted blocks) are replayed on assistant messages sent to OpenRouter, so Claude no longer re-reasons on every tool-call turn
- **OpenRouter prompt_cache_options** - OpenRouter Responses requests send `prompt_cache_options` only with mode `explicit`
- **Anthropic Signed Thinking Blocks** - Signed thinking blocks with empty text (`thinking.display: "omitted"`) are kept on multi-turn requests
- **Claude Code Reasoning Retry** - The strip-and-retry for a refused foreign `redacted_thinking` block now runs on `/anthropic/v1/messages` for Claude Code
- **Advisor Tool Aliases** - An Anthropic advisor tool's model is resolved through the key's aliases, like the request model
- **Codex Replayed Function Calls** - `summary` is sent only on reasoning items, so replayed `function_call` items with `encrypted_content` are accepted by OpenAI
- **OpenAI Image Edits by URL** - Image edits that reference an input image by URL work on OpenAI and Azure
- **Vertex Roleless Items** - Responses items with no role (Codex `agent_message` and `function_call`) get a role before they are sent to Vertex Gemini
- **Vertex Passthrough Body Rewrite** - The Vertex passthrough rewrites only the `model`, `cachedContent` and `operationName` fields and no longer alters prompt text
- **Vertex Batch Partner Models** - Vertex batch creation accepts `<publisher>/<id>` model names such as `meta/llama-3.3-70b-instruct-maas`
- **Vertex Priority Tier** - Priority PayGo is accepted on the `us` and `eu` multi-region endpoints as well as `global`, with an updated model list
- **MCP Tools Without a Description** - MCP tools with no description no longer fail on Bedrock Converse with an empty description error
- **MCP Refresh Token Race** - Replicas sharing a config store no longer redeem the same MCP refresh token at the same time, which made upstreams such as Notion revoke the grant
- **Failed Stream Spans** - A stream that ends on a read error, such as an idle timeout, is traced as failed instead of successful
- **OTel Metric Labels** - OTel metrics carry the same labels as Prometheus: `alias`, `app`, `routing_engine_used`, `routing_rule_id`, `routing_rule_name` and `complexity_tier`
- **Relay Caller Overrides** - A caller can no longer replace a relay's `extra_headers`, switch the method with `X-HTTP-Method-Override`, or pass its own query credential next to the relay's
- **Relay Timeout Errors** - A relay timeout names the relay and the timeout that fired. Billed request bodies allow 1000 levels of nesting instead of 64
- **Relay Span Dimensions** - Relay spans carry the same user, app, team and customer dimensions as LLM spans
- **Usage Lost on Config Refresh** - A budget or rate-limit charge that lands while a virtual key, team, customer, model config or provider is refreshed is no longer lost, and a refresh cannot move a reset boundary backward
- **Usage Baselines** - Budget and rate-limit resets and dumps update usage and its persisted baseline together, so already persisted usage is not counted again
- **Virtual Key Reload Race** - Overlapping reloads of one virtual key are serialized, so a rotated-out key value cannot come back
- **Firefox Dashboard Banner** - Firefox no longer shows the "Part of the dashboard couldn't load" banner on every page
- **Onboarding Checklist** - The onboarding checklist no longer loads every virtual key on each dashboard page

## 🗄️ Database Migrations

<Warning>
This release changes the `relay_logs` table (`relay_logs_add_job_debug_widen_job_status`): it adds the `job_debug` column and widens `job_status`. Bifrost handles concurrent access on the application side, but on large log tables the change still takes locks and adds load. Deploy during a low-traffic window.
</Warning>

- **add_key_service_tier_column** - Adds a nullable `service_tier` column to provider keys. Reversible: drops the column.
- **widen_batch_jobs_provider_status_and_endpoint** - Widens `batch_jobs.provider_status` and `endpoint` to `TEXT` on Postgres. Non-reversible: narrowing the columns would cut stored relay job statuses and routes.
- **add_mcp_client_client_credentials_json_column** - Adds the client credentials config column to MCP clients. Reversible: drops the column.
- **add_mcp_client_aws_sigv4_json_column** - Adds the AWS SigV4 config column to MCP clients. Reversible: drops the column.
- **add_mcp_oauth_flow_origin_column** - Adds an `origin` column to `mcp_oauth_flows`. Reversible: drops the column.
- **add_notification_subject_resolution_columns** - Adds `subject_type`, `subject_id`, `resolved_at` and `resolution` to notifications. Reversible: drops the columns.
- **add_notification_subject_index** - Adds `idx_notifications_subject`, built concurrently. Reversible: drops the index.
- **relay_logs_add_job_debug_widen_job_status** - Adds a nullable `job_debug` column to `relay_logs` and widens `job_status` to `TEXT` on Postgres. Non-reversible: dropping `job_debug` or narrowing `job_status` would lose recorded job details.

## 🐙 Closed GitHub Issues

- [#5274](https://github.com/maximhq/bifrost/issues/5274) - reasoning_details silently dropped from incoming OpenAI-compatible chat requests - Anthropic thinking-signature replay never reaches the provider
- [#9308](https://github.com/maximhq/bifrost/issues/9308) - Bedrock InvokeModel route silently drops Anthropic document blocks
- [#9516](https://github.com/maximhq/bifrost/issues/9516) - `/bedrock` invoke and Converse routes flatten `tool_result` content to its first text/JSON member, so images, documents and further text blocks never reach the model
- [#9857](https://github.com/maximhq/bifrost/issues/9857) - Firefox always shows the "Bifrost is upgrading" banner (modulepreload of the entry chunk fires an error event)
