## ✨ Features

- **Kimchi Provider** - New `kimchi` provider for chat, Responses, embeddings and list models
- **Ranked Code Mode Tool Discovery** - `listToolFiles` takes an optional `query` and `limit` (1-20, default 8) and returns a ranked shortlist of the caller's visible tool files instead of the complete listing, so a large MCP fleet can be searched by intent. Tools are ranked lexically by name and description; `mcp.tool_manager_config.code_mode_discovery_ranker` (or `BIFROST_CODE_MODE_DISCOVERY_RANKER_URL` / `BIFROST_CODE_MODE_DISCOVERY_RANKER_TOKEN_FILE`) points it at an external HTTP ranker, with a fallback to the lexical shortlist on timeout or error. Without `query` the listing is unchanged

## 🐞 Fixed

- **Empty Chat Messages** - A chat completion request with missing or empty `messages` returns a 400 instead of crashing the gateway
- **Null Entries in Gemini Requests** - A `null` inside `parts` or `functionDeclarations` on a GenAI request is skipped instead of failing with a 500. Null entries in Gemini and Vertex responses are skipped too
- **Streaming Panic Recovery** - A panic in a provider stream, a stream chunk converter, a plugin post-hook or chunk hook, or stream teardown no longer takes down the gateway. The client gets a terminal error frame in the route's own format, and logging and billing record the failure

## 🔧 Maintenance

- **Runtime Image** - The Docker runtime image moves to Alpine 3.23.7

## 🗄️ Database Migrations

- No new database migrations in this release.
