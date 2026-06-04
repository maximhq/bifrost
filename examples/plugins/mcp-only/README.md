# MCP-Only Plugin Example

This example demonstrates a plugin that implements both the `MCPPlugin` and `MCPConnectionPlugin` interfaces for Model Context Protocol governance. It covers all four MCP hook surfaces: the two envelope hooks (per tool call) and the two typed Connect hooks (per client transport setup).

## Features

### Per-call envelope hooks (`MCPPlugin`)

- **PreMCPHook**: Intercepts MCP envelope requests (ping / list_tools / execute_tool) before execution
  - Validates tool/resource calls
  - Implements governance policies (blocking dangerous tools)
  - Adds audit trails
  - Can short-circuit calls with custom responses

- **PostMCPHook**: Intercepts MCP envelope responses after execution
  - Logs responses
  - Transforms error messages
  - Accesses audit trails from context

### Typed Connect hooks (`MCPConnectionPlugin`)

- **PreMCPConnectionHook**: Runs once per MCP client when its transport is being established
  - Observes connection type and auth type (observe-only fields)
  - Mutates transport-level inputs: ConnectionString, Headers (HTTP/SSE), StdioCommand/StdioArgs (STDIO)
  - Can short-circuit to refuse a connection (e.g. blocklisted client names)

- **PostMCPConnectionHook**: Runs after the upstream MCP handshake completes
  - Reads ServerInfo, ProtocolVersion, and ServerCapabilities
  - Logs / gates on advertised capabilities (e.g. warn if `Tools` not supported)
  - Can transform handshake errors

## Use Cases

- **Security & Governance**
  - Block unauthorized tool calls
  - Enforce access control policies
  - Validate tool parameters
  
- **Observability**
  - Log all MCP interactions
  - Track tool usage
  - Monitor resource access
  
- **Error Handling**
  - Transform error messages
  - Add retry logic
  - Provide fallback responses

## Building

```bash
make build
```

This creates `build/mcp-only.so`

## Configuration

Add to your Bifrost config:

```json
{
  "plugins": [
    {
      "path": "/path/to/mcp-only.so",
      "name": "mcp-only",
      "display_name": "MCP Tool Governance",
      "enabled": true,
      "type": "mcp",
      "config": {
        "blocked_tools": ["dangerous_tool", "risky_operation"],
        "blocked_clients": ["staging-only-mcp"],
        "audit_header": "X-Bifrost-Audit",
        "enable_audit": true,
        "enable_logging": true,
        "transform_errors": true,
        "custom_error_message": "Tool is not allowed by security policy"
      }
    }
  ]
}
```

**Note:** 
- `name` is the system identifier (from `GetName()`) and is **not editable**
- `display_name` is shown in the UI and is **editable** by users

### Configuration Options

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `blocked_tools` | array of strings | `["dangerous_tool"]` | Tool names to block in `PreMCPHook` (envelope) |
| `blocked_clients` | array of strings | `[]` | MCP client names to refuse connections to in `PreMCPConnectionHook` |
| `audit_header` | string | `""` | If non-empty, injected into HTTP/SSE Connect request headers; STDIO/InProcess transports ignore headers and silently skip |
| `enable_audit` | boolean | `true` | Enable audit trail logging |
| `enable_logging` | boolean | `true` | Enable detailed logging |
| `transform_errors` | boolean | `true` | Transform 404 errors to user-friendly messages |
| `custom_error_message` | string | `"Tool is not allowed..."` | Custom error message for blocked tools / clients |

### Example Configurations

**Block multiple tools:**
```json
{
  "config": {
    "blocked_tools": ["delete_data", "modify_system", "unsafe_exec"],
    "custom_error_message": "This tool is disabled for security reasons"
  }
}
```

**Minimal logging:**
```json
{
  "config": {
    "enable_audit": false,
    "enable_logging": false,
    "transform_errors": false
  }
}
```

**Allow all tools:**
```json
{
  "config": {
    "blocked_tools": []
  }
}
```
