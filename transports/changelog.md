## ✨ Features

- **Claude Sonnet 5 Support** - Added compatibility for the Claude Sonnet 5 model family in the Anthropic provider: adaptive-only thinking and sampling-parameter gating, the effort parameter, adaptive thinking, computer-use and text-editor tool generations, dynamic web search filtering, and default max output tokens.

## 🐞 Fixed

- **Bedrock Error Type Extraction** - Fixed error type extraction for Bedrock provider responses.
- **Gemini/Imagen Aspect Ratio** - Added first-class `aspect_ratio` support for Gemini and Imagen image generation and edit requests; an explicit aspect ratio now takes precedence over any size-derived value and is backfilled into generation responses and stream events.
- **Plan Cache Migration** - Fixed a regression where the config-hash recompute migration failed on upgrade from a pre-1.6 schema, causing `undefined column` errors on PostgreSQL and SQLite.
- **Custom Provider Key Form (Bedrock)** - Fixed the custom provider API key form for Bedrock.

## 🐙 Closed GitHub Issues

- [#4797](https://github.com/maximhq/bifrost/issues/4797) — [Bug]: configstore migration order — refresh_config_hash_after_mcp_external_server_url_removal (#139) selects dump_errors_in_console_logs before add_dump_errors_in_console_logs_column (#160) adds it
