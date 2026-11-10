## ✨ Features

- **Request Timeout Header** - `x-bf-request-timeout` (duration string or whole seconds, clamped to 30m) sets one total deadline for a request or stream, across every fallback attempt and MCP agent turn. When it fires, streams end with a terminal `504 request_timed_out` error and other requests fail with 504 (thanks [@public-aanp-tanium](https://github.com/public-aanp-tanium)!)
- **Stream Idle Timeout Header** - `x-bf-stream-idle-timeout` (duration string or whole seconds, clamped to 30m) overrides the stream idle timeout for one request without changing the provider's `stream_idle_timeout_in_seconds` (thanks [@public-aanp-tanium](https://github.com/public-aanp-tanium)!)

## 🐞 Fixed

- **Empty Chat Messages** - A chat completion request with missing or empty `messages` returns a 400 instead of crashing the gateway
- **Null Entries in Gemini Requests** - A `null` inside `parts` or `functionDeclarations` on a GenAI request is skipped instead of failing with a 500. Null entries in Gemini and Vertex responses are skipped too
- **Streaming Panic Recovery** - A panic in a provider stream, a stream chunk converter, a plugin post-hook or chunk hook, or stream teardown no longer takes down the gateway. The client gets a terminal error frame in the route's own format, and logging and billing record the failure

## 🔧 Maintenance

- **Runtime Image** - The Docker runtime image moves to Alpine 3.23.7

## 🗄️ Database Migrations

- No new database migrations in this release.
