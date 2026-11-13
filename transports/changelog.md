## ✨ Features

- **Kimchi Provider** - New `kimchi` provider for chat, Responses, embeddings and list models
- **Decision Route Probe** - New `POST /api/providers/{provider}/decisions/probe` sends one fixed decision request to exactly one provider and model, with no fallbacks and no routing rules, and reports whether it answered, which model answered, latency, usage, and on failure the provider's status, error type, code, and message with credentials removed
- **Logical Alias for Decision Models** - The routing rules docs show how to map a logical model name such as `judge` to a decision model with ordered fallbacks, globally or per virtual key

## 🐞 Fixed

- **Empty Chat Messages** - A chat completion request with missing or empty `messages` returns a 400 instead of crashing the gateway
- **Null Entries in Gemini Requests** - A `null` inside `parts` or `functionDeclarations` on a GenAI request is skipped instead of failing with a 500. Null entries in Gemini and Vertex responses are skipped too
- **Streaming Panic Recovery** - A panic in a provider stream, a stream chunk converter, a plugin post-hook or chunk hook, or stream teardown no longer takes down the gateway. The client gets a terminal error frame in the route's own format, and logging and billing record the failure

## 🔧 Maintenance

- **Runtime Image** - The Docker runtime image moves to Alpine 3.23.7

## 🗄️ Database Migrations

- No new database migrations in this release.
