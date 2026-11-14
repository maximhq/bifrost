## ✨ Features

- **Kimchi Provider** - New `kimchi` provider for chat, Responses, embeddings and list models

## 🐞 Fixed

- **Empty Chat Messages** - A chat completion request with missing or empty `messages` returns a 400 instead of crashing the gateway
- **Null Entries in Gemini Requests** - A `null` inside `parts` or `functionDeclarations` on a GenAI request is skipped instead of failing with a 500. Null entries in Gemini and Vertex responses are skipped too
- **Streaming Panic Recovery** - A panic in a provider stream, a stream chunk converter, a plugin post-hook or chunk hook, or stream teardown no longer takes down the gateway. The client gets a terminal error frame in the route's own format, and logging and billing record the failure
- **Ambiguous Bare Decision Models** - A `/v1/decisions` request whose bare model id more than one configured provider serves returns a 400 naming those providers instead of going to the alphabetically first one. Send `provider/model` or add a routing rule that sets the provider. `/typesafe/v1/systemone` sends a bare model to `typesafe` when it serves the model and the virtual key allows it, as `/openai/v1/decisions` does for `openai`

## 🔧 Maintenance

- **Runtime Image** - The Docker runtime image moves to Alpine 3.23.7

## 🗄️ Database Migrations

- No new database migrations in this release.
