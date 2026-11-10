## ✨ Features

- **Workload Identity Federation for OpenAI Keys** - New `token_exchange` grant for `oauth_key_config`: Bifrost exchanges a platform-issued token read from `subject_token_file` (such as a Kubernetes projected service account token) for a short-lived access token, so OpenAI keys need no stored API key. Accepted in `config.json`, the Helm values schema and the management API; the token file must sit in a directory the operator lists in `BIFROST_SUBJECT_TOKEN_DIRS`, and the token endpoint must be listed in `BIFROST_TOKEN_EXCHANGE_URLS`

## 🐞 Fixed

- **Empty Chat Messages** - A chat completion request with missing or empty `messages` returns a 400 instead of crashing the gateway
- **Null Entries in Gemini Requests** - A `null` inside `parts` or `functionDeclarations` on a GenAI request is skipped instead of failing with a 500. Null entries in Gemini and Vertex responses are skipped too
- **Streaming Panic Recovery** - A panic in a provider stream, a stream chunk converter, a plugin post-hook or chunk hook, or stream teardown no longer takes down the gateway. The client gets a terminal error frame in the route's own format, and logging and billing record the failure

## 🔧 Maintenance

- **Runtime Image** - The Docker runtime image moves to Alpine 3.23.7

## 🗄️ Database Migrations

- No new database migrations in this release.
