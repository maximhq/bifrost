- [feat]: `exa` is a configurable provider for `POST /v1/tools/search` and `POST /v1/tools/fetch` [@Radiergummi](https://github.com/Radiergummi)
- [feat]: Search and Fetch fallbacks may name a provider alone; an unknown one is refused [@Radiergummi](https://github.com/Radiergummi)
- [feat]: `POST /v1/tools/search` takes the provider-neutral Search fields [@Radiergummi](https://github.com/Radiergummi)
- [feat]: DeepSeek web tools at `POST /v1/tools/search` and `POST /v1/tools/fetch`, routed to DeepSeek when a body carries no model [@jimaldon](https://github.com/jimaldon)

## 🐞 Fixed

- **Empty Chat Messages** - A chat completion request with missing or empty `messages` returns a 400 instead of crashing the gateway
- **Null Entries in Gemini Requests** - A `null` inside `parts` or `functionDeclarations` on a GenAI request is skipped instead of failing with a 500. Null entries in Gemini and Vertex responses are skipped too
- **Streaming Panic Recovery** - A panic in a provider stream, a stream chunk converter, a plugin post-hook or chunk hook, or stream teardown no longer takes down the gateway. The client gets a terminal error frame in the route's own format, and logging and billing record the failure

## 🔧 Maintenance

- **Runtime Image** - The Docker runtime image moves to Alpine 3.23.7

## 🗄️ Database Migrations

- No new database migrations in this release.
