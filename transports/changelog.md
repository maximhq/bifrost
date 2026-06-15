## ✨ Features

- **Skills Repository** — Added a full skills repository: dashboard UI with security warnings, navigation and access state, config-based registry, and management/serving APIs.
- **OpenCode Gateway Providers** — Added support for OpenCode gateway providers (Go, Zen) (thanks [@neta79](https://github.com/neta79)!)
- **Advisor Tool Compatibility** — Added advisor tool compatibility for Claude.
- **Environment Label Banner** — Added an environment label banner to the sidebar so the active environment is visible at a glance (thanks [@alexef](https://github.com/alexef)!)
- **Datadog Plugin Host/Port** — Added host and port configuration support for the Datadog plugin.
- **Postgres Password Command** — Added support for sourcing the Postgres password from a command (thanks [@dani29](https://github.com/dani29)!)
- **MCP Library Cleanup** — Custom MCP library entries are now hard-deleted and remote ones tombstoned.

## 🐞 Fixed

- **Config File-Wins Sync** — Plugins, governance entities, and client config now force file-wins sync when `source_of_truth=config.json`.
- **Skills API Response Bloat** — Cleared backend response bloat on skills APIs and adjusted the orphan-cleanup grace period.
- **Bedrock Tool Result Order** — Preserved `tool_result` order to match parallel `tool_use` blocks (thanks [@alexef](https://github.com/alexef)!)
- **Bedrock Cache TTL** — Set TTL in Bedrock cache points.
- **Gemini/Vertex Batch Conversion** — Fixed request conversion for Gemini/Vertex batch requests.
- **Routing-Pinned Key ID** — Commit the routing-pinned key ID to the reserved `BifrostContextKeyAPIKeyID` after `PreRequestHook` unblock.
- **Responses max_output_tokens** — Preserved `max_output_tokens` on Responses requests (thanks [@webagil-kevin](https://github.com/webagil-kevin)!)
- **VK Provider Blacklist Migration** — Run the VK provider blacklist migration before backfill (thanks [@nnNyx](https://github.com/nnNyx)!)
- **Ranking Trends Accuracy** — Stopped double-counting the boundary hour in matview ranking trends and gated ranking readers on the fresh-aggregate matview window.
- **Logstore Migrations** — Fixed duplicate migration runs for the logstore and added logging across all migrations.
- **MCP Usage Guide Button** — Fixed styling of the "Connect agent" trigger button in the MCP usage guide.
