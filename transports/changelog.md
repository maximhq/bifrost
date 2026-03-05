## ✨ Features

- **Anthropic Cache Control** — Added cache-control support for Anthropic tool calls
- **Maxim Attachments** — Added attachment support to Maxim plugin
- **Helm Graceful Shutdown** — Added graceful shutdown and HPA stabilization for streaming connections (thanks [@Edward-Upton](https://github.com/Edward-Upton)!)
- **Logstore Sonic Serialization** — Replaced encoding/json with sonic for logstore serialization (thanks [@davidrudduck](https://github.com/davidrudduck)!)

## 🐞 Fixed

- **Codex Compatibility** — Fixed fallback handling and request decompression for Codex compatibility
- **Anthropic SSE Streaming** — Use NewSSEScanner for Responses API streaming (thanks [@Edward-Upton](https://github.com/Edward-Upton)!)
- **Audio Filename Preservation** — Preserve original audio filename in transcription requests
- **Proxy Override** — Fixed proxy override handling
- **Raw Request Serialization** — Fixed raw request serialization in SSE events
- **Key List Models** — Fixed key list models serialization
- **Async Job Recovery** — Fixed async jobs stuck in "processing" on marshal failure
- **Valkey/Redis Vector Store** — Improved Valkey Search compatibility and correctness in Redis vector store
- **Semanticcache Nil Check** — Added nil check on message Content before accessing fields (thanks [@Javtor](https://github.com/Javtor)!)
- **Dashboard Overflow** — Resolved dashboard and provider config overflow regressions (thanks [@kunish](https://github.com/kunish)!)
- **Config Schema Alignment** — Fixed config schema and added test to verify Go model alignment
- **Security Patches** — Applied security patches including default anthropic error type fix
- **Prevent panic** in key selection when all keys have zero weight
- **Preserve original** audio filename in transcription requests
- **Async jobs** stuck in "processing" on marshal failure now correctly transition to "failed"
- **Prevent panic** in key selection when all keys have zero weight
- **Preserve original** audio filename in transcription requests
- **Async jobs** stuck in "processing" on marshal failure now correctly transition to "failed"
- **Adds attachment** support in Maxim plugin
