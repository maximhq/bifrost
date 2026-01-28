# MCP-Only Plugin Example

This example demonstrates a plugin that only implements the `MCPPlugin` interface for Model Context Protocol governance.

## Features

- **PreMCPHook**: Intercepts MCP requests before execution
  - Validates tool/resource calls
  - Implements governance policies (blocking dangerous tools)
  - Adds audit trails
  - Can short-circuit calls with custom responses
  
- **PostMCPHook**: Intercepts MCP responses after execution
  - Logs responses
  - Transforms error messages
  - Accesses audit trails from context

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
| `blocked_tools` | array of strings | `["dangerous_tool"]` | List of tool names to block |
| `enable_audit` | boolean | `true` | Enable audit trail logging |
| `enable_logging` | boolean | `true` | Enable detailed logging |
| `transform_errors` | boolean | `true` | Transform 404 errors to user-friendly messages |
| `custom_error_message` | string | `"Tool is not allowed..."` | Custom error message for blocked tools |

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
