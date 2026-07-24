# ✨ Features

- **Anthropic Default Fallback Routing** — Added support for Anthropic's `fallbacks: "default"` preset, preserving it through the Bifrost round-trip and injecting the `server-side-fallback-2026-07-01` beta header for default-routing requests.
- **Mid-Conversation Tool Changes** — Added support for the `mid-conversation-tool-changes-2026-07-01` beta header, enabled for Anthropic and Bedrock Mantle.

## 🐞 Fixed

- **Opus 5 Compatibility** — Added Opus 5 detection to the Anthropic provider so it inherits Opus 4.8's request surface: `budget_tokens`, `temperature`, `top_p`, and `top_k` are stripped, and native `effort`, fast mode, and mid-conversation system messages are enabled.