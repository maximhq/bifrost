[fix]: keep virtual key active defaults consistent during config sync [@Constantine3](https://github.com/Constantine3)

## ✨ Features

- **Kimchi Provider** - New `kimchi` provider for chat, Responses, embeddings and list models

## 🐞 Fixed

- **Empty Chat Messages** - A chat completion request with missing or empty `messages` returns a 400 instead of crashing the gateway
- **Null Entries in Gemini Requests** - A `null` inside `parts` or `functionDeclarations` on a GenAI request is skipped instead of failing with a 500. Null entries in Gemini and Vertex responses are skipped too
- **Streaming Panic Recovery** - A panic in a provider stream, a stream chunk converter, a plugin post-hook or chunk hook, or stream teardown no longer takes down the gateway. The client gets a terminal error frame in the route's own format, and logging and billing record the failure

## 🔧 Maintenance

- **Runtime Image** - The Docker runtime image moves to Alpine 3.23.7

## 🗄️ Database Migrations

- `normalize_virtual_key_is_active` repairs NULL virtual key active states to `true`, preserving explicit `false`, configuration hashes, and update timestamps. Each key repair commits separately; interrupted backfills resume on the next attempt. The migration is non-rollbackable, and reads normalize NULL values written by older replicas during rolling upgrades.
