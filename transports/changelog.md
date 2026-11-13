## ✨ Features

- **Kimchi Provider** - New `kimchi` provider for chat, Responses, embeddings and list models
- **Decision Models in /v1/models** - `GET /v1/models` adds `decisions` to `architecture.output_modalities` for models that serve only decisions (TypeSafe, Gemini Decision, Clef, Perplexity decider, Baseten, Ollama System One, Microsoft Decision and Databricks decision models), so clients can find them without the management API. Models that also serve chat, such as `gpt-6-luna` and vLLM or SGLang models, are left unmarked

## 🐞 Fixed

- **Empty Chat Messages** - A chat completion request with missing or empty `messages` returns a 400 instead of crashing the gateway
- **Null Entries in Gemini Requests** - A `null` inside `parts` or `functionDeclarations` on a GenAI request is skipped instead of failing with a 500. Null entries in Gemini and Vertex responses are skipped too
- **Streaming Panic Recovery** - A panic in a provider stream, a stream chunk converter, a plugin post-hook or chunk hook, or stream teardown no longer takes down the gateway. The client gets a terminal error frame in the route's own format, and logging and billing record the failure

## 🔧 Maintenance

- **Runtime Image** - The Docker runtime image moves to Alpine 3.23.7

## 🗄️ Database Migrations

- No new database migrations in this release.
