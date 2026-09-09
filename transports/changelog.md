## ✨ Features

- **Claude Cowork Proxy Support** - `claude-cowork` user agents are identified as the Claude Cowork app in logs and dashboards, and text documents that Cowork sends as base64 data URLs (`text/*` and JSON media types) are decoded into Anthropic `text` document sources on both the chat and Responses paths instead of being forwarded as opaque base64 (#7012)
- **Overhead Component Histogram** - New opt-in `bifrost_overhead_component_microseconds` histogram in the Prometheus and OTel exporters, split by `overhead_component`, enabled with `overhead_breakdown_enabled` on the telemetry and OTel plugin config (off by default, requires active tracing since it is computed from completed spans). The breakdown computation moves into a shared `framework/overhead` package, the Prometheus and OTel observability forms gain the toggle, and the UI latency breakdown renames the `scheduling` category to `miscellaneous` (#6980)
- **Upstream-Authenticated Identity in MCP Server Auth** - When an upstream auth layer has already verified the bearer and stamped the user onto the request, the MCP server accepts that identity first instead of rejecting the foreign JWT on an unknown key ID. OAuth strict mode is excluded and still verifies every token itself (#7010)
- **Standalone Virtual Key RBAC Operation** - New `CreateStandalone` RBAC operation on virtual keys decides whether a role may create keys outside access-profile governance; the VK sheet locks the governance fields and applies the access profile for roles without it (#7025)

## 🐞 Fixed

- **Bedrock Tool Result Documents** - Document blocks inside tool results are preserved when converting to Bedrock Converse instead of being dropped. Document materialization is centralized, and explicitly unsupported formats or required documents with neither inline data nor a fetchable URL are rejected up front (thanks [@michaeldunn9](https://github.com/michaeldunn9)!) (#5663)
- **MCP JWT Identity per Token Mode** - MCP JWTs no longer record every mode as an MCP token credential on the grant: vk-mode tokens settle as the virtual key they name so governance applies that key's permit, user-mode tokens attribute the request to the user, and session-mode tokens record nothing so they are refused when authentication is enforced (#7011)

## 🔧 Maintenance

- **Dependency Upgrades** - `google.golang.org/grpc` bumped to v1.83.2 across all Go modules; the CI newman tooling pins a patched `csv-parse` through a compat shim; Python integration test dependencies refreshed (#7019)

## 🗄️ Database Migrations

- No new database migrations in this release.

## 🐙 Closed GitHub Issues

- [#5661](https://github.com/maximhq/bifrost/issues/5661) - Anthropic document blocks are dropped from Bedrock tool results
