## ✨ Features

- **Kimchi Provider** - New `kimchi` provider for chat, Responses, embeddings and list models
- **Wally Provider** - `wally` is a built-in provider for Wally (RunAnywhere) open-weight models over an OpenAI-compatible API, with a config schema entry, dashboard entry and docs page (thanks [@sanchitmonga22](https://github.com/sanchitmonga22)!)

## 🐞 Fixed

- **Empty Chat Messages** - A chat completion request with missing or empty `messages` returns a 400 instead of crashing the gateway
- **Null Entries in Gemini Requests** - A `null` inside `parts` or `functionDeclarations` on a GenAI request is skipped instead of failing with a 500. Null entries in Gemini and Vertex responses are skipped too
- **Streaming Panic Recovery** - A panic in a provider stream, a stream chunk converter, a plugin post-hook or chunk hook, or stream teardown no longer takes down the gateway. The client gets a terminal error frame in the route's own format, and logging and billing record the failure

## 🔧 Maintenance

- **Runtime Image** - The Docker runtime image moves to Alpine 3.23.7

## 🗄️ Database Migrations

- No new database migrations in this release.
