## ✨ Features

- **Virtual Key Blocked Models** — Block specific models at the virtual key provider-config level; blocked models take priority over allowed models and are enforced by governance (#3653)
- **Virtual Key Ownership** — Virtual keys now capture and display a `created_by` user attribution (#3672)
- **MCP Log Attribution** — MCP tool logs are stamped with user, team, customer, and business unit IDs so MCP usage can be traced like LLM usage
- **Team & Business Unit Filters** — Added team and business unit filters across the dashboard and logs views (#3650)
- **Sticky Time Filters** — Time filter selections are preserved when navigating between sidebar items (#3647)

## ✨ Features

- **GigaChat Config and OpenAPI Exposure** — Exposed GigaChat in transport config and OpenAPI provider schemas
- **GigaChat Key Config Schema** — Added config schema validation for GigaChat auth modes and per-key endpoint overrides

## 🐞 Fixed

- **Idle Timeout Panic** — Fixed a panic in the streaming idle-timeout reader and added a guard to skip reads once the connection is closed (#3672)
- **Anthropic Streaming** — Preserve the tool-call stop reason in the Anthropic streaming fallback (#3640) (thanks [@dicnunz](https://github.com/dicnunz)!)
- **TTFT Metric** — Fixed the request start-time setting so the time-to-first-token metric is accurate (#3668)
- **Vertex Service Tier** — Map the Vertex traffic type to the correct Bifrost service tier (#3662)
- **Keyless Providers** — Fixed `ListModels` for providers configured without an API key (#3655)
- **Anthropic Tools** — Stopped forcing `type: custom` on Anthropic tool definitions (#3652)
- **Node Usage Reconciliation** — Added a monotonic log cursor so reconciliation no longer skips late async log writes (#3664)
- **Fallback Budget Tracking** — Clear the stale governance rejection flag on allow so successful fallback retries count toward budgets and rate limits (#3645)
- **Virtual Keys Table** — Table now fills available height with a sticky header and scrollable body (#3676)
- **Sheet Layout** — Removed save/cancel icons and fixed sheet layout growth in routing rule and virtual key sheets (#3675)
- **Toast Click-Through** — Toasts remain clickable above modal overlays (#3674)
- **Direct Access Control** — Reverted the virtual key `access_profile_id` direct access profile assignment shipped in v1.5.3; the `access_profile_id` column has been dropped (#3669, #3670)
