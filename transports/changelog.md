## ✨ Features

- **Calendar-Aligned Budgets** — Added calendar alignment support for budget periods in governance

## 🐞 Fixed

- **SSE Error Events** — Handle SSE error events for 429 rate-limit and other error status codes during streaming
- **Anthropic Max Tokens** — Pick max tokens for Anthropic from model params cache instead of hardcoded values
- **Anthropic Streaming Usage** — Fixed usage token reporting for Anthropic streaming responses
- **Anthropic Cache Tokens** — Fixed Anthropic cache token reporting in non-streaming responses
- **Embedding Precision** — Preserved provider precision in embedding responses instead of truncating float values
- **Provider Caching** — Removed pending marshal-to-map to fix caching issues at provider level
- **Claude Office Suite** — Fixed support for Claude office suite add-on model routing
- **Semantic Cache Config** — Hardened direct-only config handling and aligned UI types for semantic cache
- **Semantic Cache Count Tokens** — Skip unsupported count_tokens requests in semantic cache plugin
- **Telemetry Events** — Removed reason field from telemetry events
- **CORS Headers** — Fixed wildcard allowed headers for CORS
- **UI Routing Display** — Shows selected virtual key and routing rule in UI
