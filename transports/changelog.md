## ✨ Features

- **Fireworks AI Provider** — Added Fireworks AI as a first-class provider (thanks [@ivanetchart](https://github.com/ivanetchart)!)
- **Unified Models API** — Unified /api/models and /api/models/details listing behavior
- **Server Bootstrap Timer** — Added server bootstrap timer for performance monitoring
- **Security Path Whitelisting** — Allow path whitelisting from security config
- **Large Payload Optimizations** — Updated config schema for large payload optimizations
- **Virtual Keys Table** — Added sorting and CSV export to virtual keys table
- **Combobox Refactor** — Removed base-ui dependencies and recreated combobox using Radix primitives
- **Switch Component** — Added async support and loading state to Switch component

## 🐞 Fixed

- **Bedrock Streaming Retries** — Retry retryable AWS exceptions and stale/closed-connection errors (thanks [@KTS-o7](https://github.com/KTS-o7)!)
- **Gemini Thinking Budget** — Fixed thinking budget validation for Gemini models
- **Integration Data Race** — Fixed race in data reading from fasthttp request for integrations
- **Beta Headers** — Fixed case-insensitive lookup in merge beta headers
- **Deprecated Config Field** — Replaced enforce_governance_header with enforce_auth_on_inference
- **Bedrock Config Schema** — Fixed config schema for Bedrock key config
- **OpenAI Codex** — Fixed store flag for OpenAI Codex
- **Delete Button Styling** — Standardized delete button styling with red theme across workspace tables
