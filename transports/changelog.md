## ✨ Features

- **Claude Code Gateway Sign-In** - Claude Code can sign in to Bifrost with `/login` (Cloud gateway) at `<issuer_url>/gateway/claude-code`: device-code sign-in through the OAuth consent page (virtual key, user or anonymous), silent token refresh, Messages inference resolved to the approved virtual key, managed settings, and optional forwarding of Claude Code's OTLP telemetry to your collectors, stamped with the signed-in virtual key, team, customer or user. Configured under `oauth2_server_config.gateways.claude_code` or on the new Settings → Gateways page, independent of the MCP auth mode (#7963) [@jimseiwert](https://github.com/jimseiwert). The gateway reserves the `/gateway` route prefix: a `server.base_path` whose first segment is `gateway` is now refused at startup, so move such a deployment to another prefix before upgrading
- **Kimchi Provider** - New `kimchi` provider for chat, Responses, embeddings and list models

## 🐞 Fixed

- **Empty Chat Messages** - A chat completion request with missing or empty `messages` returns a 400 instead of crashing the gateway
- **Null Entries in Gemini Requests** - A `null` inside `parts` or `functionDeclarations` on a GenAI request is skipped instead of failing with a 500. Null entries in Gemini and Vertex responses are skipped too
- **Streaming Panic Recovery** - A panic in a provider stream, a stream chunk converter, a plugin post-hook or chunk hook, or stream teardown no longer takes down the gateway. The client gets a terminal error frame in the route's own format, and logging and billing record the failure

## 🔧 Maintenance

- **Runtime Image** - The Docker runtime image moves to Alpine 3.23.7

## 🗄️ Database Migrations

- `add_oauth2_authorize_requests_device_code_columns` - Adds the nullable `device_code_hash` and `user_code_hash` columns to `oauth2_authorize_requests`, with a bounded lock wait on PostgreSQL.
- `add_oauth2_authorize_requests_device_code_indexes` - Builds their unique indexes concurrently on PostgreSQL, so writes are never blocked.
