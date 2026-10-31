## ✨ Features

- **Relays** - New relays call non-LLM APIs (Tavily, AWS Textract, Azure Speech, ...) through `/relay/{name}/{path}` with one stored credential, per-route pricing and usage mapping, and streaming. Virtual keys get `relay_grants`, and relay calls are charged to the caller's virtual key, team and customer budgets. Relay Logs record metadata, route-matched headers and (with content logging on) streamed response bodies. Routes can be imported from an OpenAPI document pasted, uploaded or fetched by URL, and header matchers accept any header name. Relays sync from config.json, and a credential is tracked by its `env.` reference, so rotating the value does not re-sync the config
- **Warp Topics** - Warp groups the requests in your logs into topics and broader themes. It files each request under a topic as it is indexed, discovers new topics from unmatched requests, and counts topics over a time window with trends, metrics and scoped reads. Logs can be filtered by topic, and topics link back to their requests. Warp is configurable from config.json, its model calls run over the streaming Responses path, and dead topic rows and deleted logs are pruned
- **MiniMax Provider** - New `minimax` provider for chat completions, Responses, image generation, async video generation, text-to-speech and speech-to-text, with `thinking` passthrough and the `base_resp` error envelope mapped to HTTP status codes
- **BytePlus Provider** - New `byteplus` provider for BytePlus ModelArk: Seed chat completions with `reasoning_effort` mapped onto Ark's `thinking` switch, the native Responses API, embeddings through the multimodal endpoint, Seedream image generation, editing and streaming, and async Seedance video generation (priced by output tokens) with list support
- **AWS SageMaker Provider** - New `sagemaker` provider for real-time endpoints. Chat goes through SageMaker's OpenAI-compatible route (vLLM, SGLang, LMI, llama.cpp containers), and `/sagemaker_passthrough` forwards raw `InvokeEndpoint` and `InvokeEndpointWithResponseStream` calls
- **Azure Speech and Live APIs** - Azure keys reach Azure AI Speech for SSML text-to-speech and fast transcription (`speech/<model>`, enabled with `use_speech_endpoints`), Azure Voice Live realtime sessions (`use_voice_live_endpoints`, with object voices and Voice Live pricing), and GPT Live over `/openai/v1/live/sessions`. A new `/azure` route group lets Azure AI Speech clients and the Azure OpenAI SDK point at Bifrost as their Azure resource, voices appear in list models, `Ocp-Apim-Subscription-Key` is accepted, and Azure Whisper transcriptions are billed by audio duration
- **Bedrock Nova Sonic Realtime** - Amazon Nova Sonic speech-to-speech on the OpenAI Realtime-compatible surface. Clients keep speaking OpenAI Realtime events, and Bifrost translates them onto Bedrock's signed HTTP/2 event stream through a new realtime connector for providers that do not speak WebSocket
- **MCP Prompts and Resources** - Bifrost discovers the prompts, resources and resource templates an upstream MCP server offers and serves them on the MCP gateway (`/mcp`), with `completion/complete` for their arguments. Each server gets `prompts_to_expose` and `resources_to_expose` allow-lists, and virtual keys, Virtual MCPs and include headers narrow them like tools. Prompt and resource reads are logged
- **MCP Protocol 2026-07-28** - mcp-go moves to v1.3.0. A global and per-server max protocol version setting (`auto`, `2026-07-28`, `2025-11-25`, `2025-06-18`) caps what Bifrost offers upstream. Tool results keep images, audio and structured content instead of flattening to text, and tool output schemas, titles, icons and annotations reach clients. Persistent connections re-list tools as soon as an upstream sends `tools/list_changed`, and per-call clients reuse HTTP connections
  <Note>
  The MCP gateway now validates `Origin` as the Streamable HTTP spec requires. Browser requests to `/mcp` from an origin that is not allowed get a `403`. Requests with no `Origin` header (CLIs, SDKs, server-side clients) are unaffected.
  </Note>
- **MCP OAuth CIMD and Issuer Binding** - Upstream MCP OAuth publishes a Client ID Metadata Document at `/api/oauth/client-metadata.json` and registers with it, and the MCP OAuth server accepts CIMD clients. Each MCP client's credentials are bound to the authorization server that issued them, and authorization responses are checked against it (RFC 8414, RFC 9207). The OAuth issuer URL is editable under Settings > Security, and token-exchange settings are validated when a client is created or edited
- **Native Decisions on More Providers** - `/v1/decisions` runs natively on vLLM and SGLang, on Databricks System One (OpenJev), and on Azure gpt-6-luna. OpenAI decisions accept conversation items, JSON parts and prompt caching, and unprefixed models are left to routing instead of being pinned to OpenAI or Typesafe
- **GitHub Copilot Token Auth** - GitHub Copilot keys take a fine-grained PAT or a GitHub OAuth token in `value`, and the UI signs in with GitHub through the device flow. Claude models go to Copilot's native `/v1/messages`, OpenAI models to native `/responses`, and embeddings to `/embeddings`
  <Warning>
  The GitHub App installation mode is removed. `github_copilot_key_config` is no longer accepted, and a GitHub Copilot key without a token in `value` is refused at setup. Replace App-based keys with a PAT or OAuth token before upgrading.
  </Warning>
- **Passthrough Route Allowlist** - Each provider has a passthrough route policy that controls which routes `/openai_passthrough`, `/anthropic_passthrough`, `/azure_passthrough`, `/genai_passthrough` and the other passthrough endpoints forward. Custom providers can be reached on their base provider's passthrough route through `x-model-provider` once they have a policy
- **Codex with ChatGPT Sign-In** - `/chatgpt_passthrough` forwards `GET /backend-api/codex/models`, so Codex signed in with ChatGPT runs through Bifrost with the virtual key in `x-bf-vk`. The model list skips the virtual key's model allowlist because it runs no model, and each responses turn is still checked. Only a token ChatGPT issued is forwarded, and the served `service_tier` is recorded in passthrough logs
- **Vertex OpenAI-Format Endpoints** - A Vertex alias can point at a custom Vertex endpoint (vLLM or another container) that accepts OpenAI chat completions, through `vertex_endpoint` with `raw_predict` or `predict`, and non-chat operations are no longer refused on those aliases
- **Azure Blob Storage** - Log payload and skills-file storage on Azure Blob with default Azure identity or connection-string authentication, JSON/gzip and Parquet support, and config-file configuration. Structured environment and vault references can omit `value` (thanks [@CallumWayve](https://github.com/CallumWayve)!)
- **Log CSV Export** - The Logs page exports the filtered logs to CSV (thanks [@Shashank2985](https://github.com/Shashank2985)!)
- **Fallback Chain in Log Details** - Log details show a banner and timeline for the fallback chain a request went through
- **Hide the Setup Checklist** - A new `disable_onboarding` flag hides the setup checklist on stateless and provider-less deployments
- **Developer Role and Mid-Conversation System Messages** - Models whose datasheet row sets `supports_developer_role` to false get developer messages as `system`, and system messages after the opening run are folded in for models that reject them
- **Gemini 4 Thinking Level** - A thinking budget sent to Gemini 4 models becomes `thinkingLevel` (up to 1024 is `low`, up to 8192 is `medium`, anything larger is `high`), since these models refuse `thinkingBudget`
- **Gemini Long-Decode Continuation** - Gemini hops that end with `finishReason: CONTINUATION` carry their `continuationToken`, so clients can request the next hop
- **Fireworks disable_store** - Fireworks Responses requests honour `disable_store`
- **Databricks List Models** - `/v1/models?provider=databricks` lists the key's configured models and aliases instead of returning nothing
- **Model Creation Time** - The model catalog keeps the creation time each provider reports for a model
- **Realtime Metadata** - Plugins can carry opted-in HTTP metadata into native realtime turns and Live billing units
- **ClickHouse User Agent Mappings** - The ClickHouse log store gets the user agent mappings table the SQL stores already have
- **Bedrock Tool Strict Mode** - `strict` on a tool reaches Converse `toolSpec.strict` for Claude on Bedrock
- **Tracing Hooks** - `HoldTraceFlush` delays trace export until background writers such as post-delivery guardrails finish, and `SetSpanStartTime` lets a span opened after its work shows the work's real duration

## 🐞 Fixed

- **Startup No Longer Fails on a New Key's Unknown Grant** - A virtual key added in config.json whose `agent_grants` (or `relay_grants`) named an agent or relay that is not stored failed the whole governance sync, and Bifrost exited at startup. That grant is now skipped with a warning and the key is created, as an existing key's update already did. Agent grants in config.json belong on the agent (`agents[].virtual_key_ids`): agents load after virtual keys, so a key cannot grant an agent declared in the same file on its first start
- **Anthropic Sonnet 5.5 Passthrough** - Native Sonnet 5.5 requests forward the caller's reasoning and sampling settings on both the typed and raw paths (thanks [@valentinyanakiev](https://github.com/valentinyanakiev)!)
- **Anthropic message_delta** - Streaming `message_delta` frames keep their required `delta` object and report only the usage the upstream sent (thanks [@valentinyanakiev](https://github.com/valentinyanakiev)!)
- **DeepSeek Document Blocks** - Document content blocks are rejected before egress on DeepSeek's Anthropic endpoint, which documents them as unsupported but silently ignores them (thanks [@valentinyanakiev](https://github.com/valentinyanakiev)!)
- **Bridged Thinking Signatures** - Thinking blocks bridged from Chat Completions `reasoning_content` no longer get a synthetic signature (thanks [@rxchitrx](https://github.com/rxchitrx)!)
- **Anthropic Web Search on Chat Completions** - The streamed web search query stays server-side instead of leaking as an orphan `tool_calls` delta, and text-block citations map to `annotations[].url_citation` (thanks [@brianhadley2026](https://github.com/brianhadley2026)!)
- **Gemini Tool-Call IDs** - Gemini and Vertex receive the tool-call id they issued instead of a ~500-character id carrying the embedded thought signature, which the model copied as text (thanks [@Vivek1898](https://github.com/Vivek1898)!)
- **Bedrock Connection Errors** - A failed connection and a stream dropped after the `200` report a `502` from upstream instead of a `400`, so Anthropic clients such as Claude Code get a retryable `api_error` (thanks [@dinhxuanvu](https://github.com/dinhxuanvu)!)
- **Bedrock Streaming stop_reason** - `invoke-with-response-stream` ends a tool-call turn with `stop_reason: tool_use` instead of `end_turn` (thanks [@pcbeingused333](https://github.com/pcbeingused333)!)
- **Native web_search_call Replay** - OpenAI Responses requests keep the `id` and `status` of a native `web_search_call`, which OpenAI and Codex require on replay (thanks [@pcbeingused333](https://github.com/pcbeingused333)!)
- **extra_content-Only Stream Chunks** - OpenAI-compatible chat streams forward chunks whose delta carries only `extra_content` (thanks [@Javtor](https://github.com/Javtor)!)
- **Deterministic Datasheet Prices** - Datasheet sync stores one deterministic price when `bedrock` and `bedrock_mantle` rows share a model name (thanks [@aharshit123456](https://github.com/aharshit123456)!)
- **Sampling Params on Rows Without model_parameters** - Compat no longer strips every sampling param and substitutes the default `max_tokens` on datasheet rows without `model_parameters` (thanks [@ege-arhan](https://github.com/ege-arhan)!)
- **Logstore Without cgo** - `framework/logstore` builds with `CGO_ENABLED=0` again (thanks [@jeremym-tanium](https://github.com/jeremym-tanium)!)
- **Providers Page Load Error** - The providers page shows a load error instead of the "no providers configured" onboarding screen when the providers request fails (thanks [@hmdsefi](https://github.com/hmdsefi)!)
- **Anthropic Advisor Tool** - Advisor passes (`advisor_20260301`) are billed at the advisor model's rate instead of going unbilled, and governance checks the model a server tool names against the virtual key's model restrictions
- **Bedrock Tool Results and Prompt Caching** - A tool message with one text block renders like the same content sent as a string, so prompt caching holds across turns
- **Bedrock fallback-credit Beta** - The `fallback-credit-2026-06-01` beta header is no longer rewritten to a date Bedrock rejects
- **Bedrock GLM Tool Schemas** - The top-level tool-schema `pattern` that `zai.glm-5.3` rejects is dropped
- **Image Detail in Tool Outputs** - `input_image` blocks inside a tool output get the default `detail`, so strict OpenAI-compatible backends such as self-hosted vLLM accept them
- **Responses Reasoning Deltas** - Reasoning delta events that carry only a signature keep their required `delta` field
- **Mid-Conversation Tool Changes and Thinking Display** - `tool_addition` / `tool_removal` blocks are stripped on surfaces without the mid-conversation tool changes beta, and `thinking.display: "updates"` gets its beta header or is downgraded where unsupported
- **Responses WebSocket** - Responses WebSocket sessions accept `response.interrupt`, stay on the same provider key across turns, retry a refused encrypted reasoning replay, and a failed passthrough Responses stream ends with `response.failed`
- **Realtime Sessions** - Realtime turn lifecycles, logs and upstream errors are corrected, so an audio commit no longer blocks the next `response.create`, and the client socket closes when the upstream ends the session
- **OpenRouter Image Generation** - Images returned on `message.images` and streamed `delta.images` reach the response
- **Databricks** - The output ceiling is sent as `max_tokens`, `reasoning_effort` becomes `output_config.effort` on adaptive-only Claude, and key aliases stay unprefixed when a virtual key load balances them
- **List Models** - Each list-models page is bounded instead of the whole walk, so paginated providers are no longer cut off in aggregate listings. Aliases allowed only by `regex:` entries are listed, Vertex OAuth keys with deployments or allowlists list the Model Garden catalog, `unfiltered=true` returns the whole catalog, and integration routes such as `/anthropic/v1/models` include `max_input_tokens` from the model catalog
- **Image Pricing** - Image models called over chat and Responses are priced from their image generation row, token rates fall back to per-image pricing when the row has none, Gemini image usage counts `thoughtsTokenCount`, and GitHub Copilot datasheet rows match their provider
- **Routing** - Complexity classification is skipped when other rule conditions already decide the outcome, and embeddings fall back to single-text requests when a batch is refused
- **Latency and Spans** - Multi-turn MCP code-mode requests keep upstream time across turns, streaming spans carry token usage, chunk count and TTFC, plugin post-hook spans have real durations, background spans stay out of the overhead breakdown, and `bifrost_overhead_latency_microseconds` is emitted in Prometheus
- **Held Streams** - A stream held by a plugin commits its response as soon as the upstream produces output instead of waiting for the whole generation
- **MCP OAuth** - Protected resource metadata is used only when its resource matches the MCP server URL (RFC 9728), abandoned OAuth installs are swept, an expired token with no refresh token is marked `needs_reauth`, a public client can change its authorize and token URLs without a secret, and token and header credential evictions reach other processes
- **MCP Sessions and Errors** - A `404` is treated as a dead session only on protocol revisions that have sessions, an upstream `input_required` returns a clear error, and a rejected credential is refreshed in the background when an auth retry is skipped
- **Materialized View Refreshes** - Dashboard materialized view maintenance no longer keeps running on Postgres after the pod that started it is gone
- **Config Reload** - A config.json without a complexity `classifier` keeps the runtime classifier on reload, and SQLite client-config writes are serialized
- **npx Checksum** - The `npx` launcher verifies the downloaded gateway binary's checksum before running it
- **UI** - The date range picker uses the active timezone, the default cache key and vector store namespace can be cleared, MCP log links open a log outside the loaded page, and the MCP registry OAuth callback page is redesigned

## 🗄️ Database Migrations

- **add_client_config_skip_access_profile_user_virtual_key_lifecycle_columns** - Adds two client config columns that control access-profile user virtual key creation and deletion. Reversible: rolls back by dropping both columns.
- **add_passthrough_json_column** - Adds the per-provider passthrough route allowlist. Non-reversible: dropping it would delete every provider's passthrough allowlist.
- **add_warp_topic_discovery_min_requests_column** - Adds the Warp topic discovery threshold. Reversible: without the column discovery runs at the default.
- **add_azure_use_speech_endpoints_column** - Adds the Azure key `use_speech_endpoints` flag. Reversible: rolls back by dropping the column.
- **add_azure_use_voice_live_endpoints_column** - Adds the Azure key `use_voice_live_endpoints` flag. Reversible: rolls back by dropping the column.
- **add_sagemaker_key_columns** - Adds the SageMaker key config columns. Reversible: rolls back by dropping the columns.
- **widen_oauth2_client_id_columns** - Widens OAuth `client_id` columns beyond `varchar(255)` for CIMD URLs. Non-reversible: longer client IDs may already be stored.
- **add_oauth2_refresh_token_client_display_name_column** - Adds a client display name to OAuth refresh tokens. Reversible: rolls back by dropping the column.
- **add_mcp_max_protocol_version_columns** - Adds the global and per-server MCP protocol cap. Non-reversible: dropping the columns would delete every configured cap.
- **add_mcp_client_discovered_resources_prompts_columns** - Stores discovered MCP resources, resource templates and prompts. Reversible: rolls back by dropping the columns.
- **add_mcp_client_expose_lists_columns** - Adds `prompts_to_expose` and `resources_to_expose`. Non-reversible: dropping the allow-lists would expose every prompt and resource they restrict.
- **add_oauth_issuer_binding_columns** - Binds MCP OAuth credentials to their issuing authorization server. Reversible: rolls back by dropping the columns.
- **add_mcp_client_discovered_tool_details_column** - Stores discovered MCP tool titles, icons and output schemas. Reversible: rolls back by dropping the column.
- **add_relays_tables** - Creates the relay, route and virtual key grant tables. Non-reversible: dropping them would delete relay endpoints, their credentials and virtual key grants.
- **add_video_token_pricing_column** - Adds `output_cost_per_video_token`. Non-reversible: dropping it would delete custom per-token video prices and bill token-metered video at $0.
- **add_voice_live_pricing_columns** - Adds Azure Voice Live pricing columns. Non-reversible: dropping them would delete every Voice Live rate an operator set.
- **logs_add_warp_topic_tables** - Creates the Warp topic, assignment and unmatched tables in the log store. Non-reversible once populated: topics outlive the requests they were built from and cannot be rebuilt from the logs.
- **logs_add_warp_vector_deletes_table** - Creates the pending vector deletes table. Non-reversible while it holds rows: they list deleted logs whose vectors are still in the vector store.
- **mcp_tool_logs_add_method_column** - Adds `method` to MCP tool logs for prompt and resource reads. Reversible: rolls back by dropping the column.
- **add_relay_logs_table** - Creates the relay logs table (also created in ClickHouse). Non-reversible: it holds recorded call history and charges.

All new migrations only add columns or tables, so older binaries ignore them during a rolling upgrade.

## 🐙 Closed GitHub Issues

- [#5649](https://github.com/maximhq/bifrost/issues/5649) - [Bug]: defaultImageDetail fix (#5352) doesn't cover images inside function_call_output (tool results), Responses API to self-host vLLM still fails
- [#7189](https://github.com/maximhq/bifrost/issues/7189) - [Bug]: Realtime audio commit blocks subsequent response.create with active-response error
- [#7319](https://github.com/maximhq/bifrost/issues/7319) - [Bug]: /anthropic/v1/models omits max_input_tokens - integration list-models route never enriches from the model catalog
- [#7655](https://github.com/maximhq/bifrost/issues/7655) - [Bug]: Anthropic web search on /v1/chat/completions leaks the search query as an orphan tool_calls delta and drops citations
- [#8152](https://github.com/maximhq/bifrost/issues/8152) - [Feature]: Allow setup checklist to be dismissed persistently on stateless / provider-less deployments
- [#8268](https://github.com/maximhq/bifrost/issues/8268) - [Bug]: Providers page shows the "no providers configured" onboarding screen when the providers request fails
- [#8382](https://github.com/maximhq/bifrost/issues/8382) - [Bug]: Bifrost 2.2.6 strips web_search_call.status from Responses history, breaking Codex replay
- [#8489](https://github.com/maximhq/bifrost/issues/8489) - [Bug]: Datasheet sync stores a random price when bedrock and bedrock_mantle rows share a model name
- [#8546](https://github.com/maximhq/bifrost/issues/8546) - [Bug]: Gemini/Vertex receives the full `_ts_`-embedded call id as `functionCall.id`, so the model sees (and copies) ~500-char ids
- [#8547](https://github.com/maximhq/bifrost/issues/8547) - [Bug]: Abandoned shared OAuth MCP installs leave oauth_configs rows pending forever
- [#8549](https://github.com/maximhq/bifrost/issues/8549) - [Bug]: thinking blocks bridged from Chat Completions reasoning_content get a synthetic brid_ signature
- [#8550](https://github.com/maximhq/bifrost/issues/8550) - [Bug]: Bedrock invoke-with-response-stream reports `stop_reason: "end_turn"` for tool-use turns
- [#8594](https://github.com/maximhq/bifrost/issues/8594) - [Feature]: Export filtered logs to CSV from the Logs page
- [#8759](https://github.com/maximhq/bifrost/issues/8759) - [Bug]: GET /v1/models omits models from a paginated provider in aggregate listings
- [#8765](https://github.com/maximhq/bifrost/issues/8765) - [Bug]: Bedrock datasheet rows without model_parameters make compat drop all sampling params and substitute the model default max_tokens
- [#8801](https://github.com/maximhq/bifrost/issues/8801) - [Bug]: Bedrock: a tool message with one text block renders as text, but the same content as a string renders as json, which breaks prompt caching across turns
- [#8824](https://github.com/maximhq/bifrost/issues/8824) - [Bug]: framework/logstore no longer builds with CGO_ENABLED=0
- [#8934](https://github.com/maximhq/bifrost/issues/8934) - [Bug]: OpenAI-compatible chat streams drop chunks whose delta carries only extra_content
- [#8963](https://github.com/maximhq/bifrost/issues/8963) - [Bug]: Bedrock stream dropped mid-response reaches Anthropic clients as invalid_request_error
- [#8970](https://github.com/maximhq/bifrost/issues/8970) - [Bug]: Bedrock returns HTTP 400 when it can't connect to the provider
