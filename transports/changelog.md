[feat]: add an optional stop reason column to the logs table [@Constantine3](https://github.com/Constantine3)
## ✨ Features

- **OpenAI Workload Identity Federation** - `oauth_key_config` gains a `token_exchange` grant (RFC 8693): Bifrost reads a workload identity token, such as a Kubernetes projected service account token, from `subject_token_file` and exchanges it for an access token, so an OpenAI key needs no API key. `BIFROST_OIDC_ALLOWED_CREDENTIAL_DIRS` bounds which files may be read and `BIFROST_TOKEN_EXCHANGE_URLS` (default `https://auth.openai.com/oauth/token`) where the token may be sent. The dashboard can create and edit such keys
- **Claude Code Gateway Sign-In** - Claude Code can sign in to Bifrost with `/login` (Cloud gateway) at `<issuer_url>/gateway/claude-code`: device-code sign-in through the OAuth consent page (virtual key, user or anonymous), silent token refresh, Messages inference resolved to the approved virtual key, managed settings, and optional forwarding of Claude Code's OTLP telemetry to your collectors, stamped with the signed-in virtual key, team, customer or user. Configured under `oauth2_server_config.gateways.claude_code` or on the new Settings → Gateways page, independent of the MCP auth mode (#7963) [@jimseiwert](https://github.com/jimseiwert). The gateway reserves the `/gateway` route prefix: a `server.base_path` whose first segment is `gateway` is now refused at startup, so move such a deployment to another prefix before upgrading
- **MachGen Provider** - New `machgen` provider for MachGen's image and video models: text-to-image, image edit, and async video generation with retrieve and download. `/v1/models` lists the models each key can use, with their task types. Set up from the UI or `providers.machgen` in config.json
- **Kimchi Provider** - New `kimchi` provider for chat, Responses, embeddings and list models
- **Trace Dimensions on Child Spans** - An OTEL profile with `apply_trace_dimensions_to_child_spans` set merges the request's `x-bf-dim-*` dimensions onto every exported span (LLM call, plugin, retry, fallback, MCP tool), not only the root HTTP span. Span attributes win on conflict, and `path`, `method` and keys in reserved attribute namespaces (`gen_ai.`, `http.`, `bifrost.`, ...) are skipped (#3770)
- **Codex WebSocket Through ChatGPT Sign-In** - `/chatgpt_passthrough/backend-api/codex/responses` accepts the WebSocket Codex opens with `supports_websockets = true` and relays it to ChatGPT with the caller's ChatGPT token. Each turn is checked against the virtual key and logged and billed like a turn sent over HTTP. A request without a ChatGPT token gets a 426, which moves Codex to HTTP right away

## 🐞 Fixed

- **Empty Chat Messages** - A chat completion request with missing or empty `messages` returns a 400 instead of crashing the gateway
- **Budget Update on config.json Virtual Keys** - Updating the budget of a virtual key provisioned from config.json no longer fails with a 500 `invalid reset duration format: ` when its standalone budget is adopted into the key's model config
- **Null Entries in Gemini Requests** - A `null` inside `parts` or `functionDeclarations` on a GenAI request is skipped instead of failing with a 500. Null entries in Gemini and Vertex responses are skipped too
- **Streaming Panic Recovery** - A panic in a provider stream, a stream chunk converter, a plugin post-hook or chunk hook, or stream teardown no longer takes down the gateway. The client gets a terminal error frame in the route's own format, and logging and billing record the failure

## 🔧 Maintenance

- **Runtime Image** - The Docker runtime image moves to Alpine 3.23.7

## 🗄️ Database Migrations

- `add_oauth2_authorize_requests_device_code_columns` - Adds the nullable `device_code_hash` and `user_code_hash` columns to `oauth2_authorize_requests`, with a bounded lock wait on PostgreSQL.
- `add_oauth2_authorize_requests_device_code_indexes` - Builds their unique indexes concurrently on PostgreSQL, so writes are never blocked.
