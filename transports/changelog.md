- feat: VK provider config key_ids now supports ["*"] wildcard to allow all keys; empty key_ids denies all; handler resolves wildcard to AllowAllKeys flag without DB key lookups
- feat: add option to disable automatic MCP tool injection per request
- feat: virtual key MCP configs now act as an execution-time allow-list — tools not permitted by the VK are blocked at inference and MCP tool execution

## BREAKING CHANGES — explicit empty arrays now mean deny-all; absent inner fields now mean allow-all

The following fields in `governance.virtual_keys[*]` in config.json have changed semantics:

| Field | Old behaviour | New behaviour | Type |
|---|---|---|---|
| `provider_configs: []` | allow all providers | **deny all providers** | breaking |
| `provider_configs` absent | allow all providers | allow all providers — deprecated, see below | deprecated |
| `provider_configs[*].allowed_keys: []` | allow all keys | **deny all keys** | breaking |
| `provider_configs[*].allowed_keys` absent | deny all keys (bug) | **allow all keys** — deprecated, see below | breaking + deprecated |
| `mcp_configs: []` | allow all MCP clients | **deny all MCP clients** | breaking |
| `mcp_configs` absent | allow all MCP clients | allow all MCP clients — deprecated, see below | deprecated |
| `mcp_configs[*].tools_to_execute: []` | deny all tools | deny all tools | unchanged |
| `mcp_configs[*].tools_to_execute` absent | deny all tools (bug) | **allow all tools** — deprecated, see below | breaking + deprecated |

### Migration guide

**`provider_configs: []` / `mcp_configs: []`** — previously allowed all; now deny all. To keep allow-all behaviour, list entries explicitly instead of using an empty array.

**`provider_configs[*].allowed_keys` absent** — previously (buggy) denied all keys; now allows all keys and emits a deprecation warning. If you intended deny-all, add `allowed_keys: []` explicitly. If you intended allow-all, add `allowed_keys: ["*"]` to silence the warning.

**`mcp_configs[*].tools_to_execute` absent** — previously (buggy) denied all tools for that client; now allows all tools and emits a deprecation warning. If you intended deny-all, add `tools_to_execute: []` explicitly. If you intended allow-all, add `tools_to_execute: ["*"]` to silence the warning.

### Deprecation notice

Omitting `provider_configs`, `mcp_configs`, `allowed_keys`, or `tools_to_execute` entirely (absent key) currently defaults to allow-all and emits a startup warning. For `provider_configs` and `mcp_configs`, the implicit allow-all expands to whichever providers / MCP clients are present at startup — it is a boot-time snapshot, not a live wildcard. **This implicit allow-all will be removed in the next major version** — absent and `[]` will both mean deny-all (deny-by-default). Migrate by always specifying these fields explicitly in config.json.
