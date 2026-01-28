# Multi-Interface Plugin Example

This example demonstrates a plugin that implements **all plugin interfaces**:
- `HTTPTransportPlugin`
- `LLMPlugin`
- `MCPPlugin`
- `ObservabilityPlugin`

## Features

### HTTPTransportPlugin
- Tracks request count across all requests
- Adds request number header
- Calculates HTTP request duration
- Stores HTTP metadata in context for other hooks

### LLMPlugin
- Accesses HTTP context metadata
- Adds dynamic system prompts
- Tracks LLM call duration
- Logs request/response details

### MCPPlugin
- Accesses HTTP context metadata
- Logs all MCP tool/resource calls
- Tracks MCP call duration
- Implements governance for MCP calls

### ObservabilityPlugin
- Receives completed traces asynchronously
- Formats traces as JSON
- Ready for integration with OTEL, Datadog, Jaeger, etc.
- Demonstrates end-to-end request tracking

## Context Flow

This plugin demonstrates how context flows through different hooks:

1. **HTTPTransportPreHook** → Stores HTTP metadata
2. **PreLLMHook/PreMCPHook** → Accesses HTTP metadata, stores LLM/MCP metadata
3. **PostLLMHook/PostMCPHook** → Accesses stored timing data
4. **HTTPTransportPostHook** → Adds final headers
5. **Inject** → Receives complete trace asynchronously

## Use Cases

- **Full-stack observability** - Track requests from HTTP to LLM/MCP and back
- **Unified governance** - Apply policies at multiple layers
- **Performance monitoring** - Measure duration at each layer
- **Audit trails** - Complete request/response logging
- **Custom analytics** - Correlate HTTP, LLM, and MCP metrics

## Building

```bash
make build
```

This creates `build/multi-interface.so`

## Configuration

Add to your Bifrost config:

```json
{
  "plugins": [
    {
      "path": "/path/to/multi-interface.so",
      "name": "multi-interface",
      "display_name": "Full-Stack Observability",
      "enabled": true,
      "type": "auto",
      "config": {
        "enable_http_hooks": true,
        "enable_llm_hooks": true,
        "enable_mcp_hooks": true,
        "enable_observability": true,
        "enable_logging": true,
        "track_requests": true,
        "inject_uptime": true,
        "custom_header_prefix": "X-Multi-Plugin"
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
| `enable_http_hooks` | boolean | `true` | Enable HTTP transport layer hooks |
| `enable_llm_hooks` | boolean | `true` | Enable LLM request/response hooks |
| `enable_mcp_hooks` | boolean | `true` | Enable MCP request/response hooks |
| `enable_observability` | boolean | `true` | Enable observability/trace injection |
| `enable_logging` | boolean | `true` | Enable detailed logging |
| `track_requests` | boolean | `true` | Track and count requests |
| `inject_uptime` | boolean | `true` | Inject server uptime in LLM system messages |
| `custom_header_prefix` | string | `"X-Multi-Plugin"` | Custom prefix for HTTP response headers |

### Example Configurations

**LLM-only mode:**
```json
{
  "config": {
    "enable_http_hooks": false,
    "enable_llm_hooks": true,
    "enable_mcp_hooks": false,
    "enable_observability": false
  }
}
```

**Observability-focused:**
```json
{
  "config": {
    "enable_http_hooks": true,
    "enable_llm_hooks": true,
    "enable_mcp_hooks": true,
    "enable_observability": true,
    "enable_logging": false,
    "track_requests": true
  }
}
```

**Minimal overhead:**
```json
{
  "config": {
    "enable_logging": false,
    "track_requests": false,
    "inject_uptime": false
  }
}
```

**Custom headers:**
```json
{
  "config": {
    "custom_header_prefix": "X-Custom-Plugin"
  }
}
```

## Hook Execution Order

For a typical LLM request:

1. `HTTPTransportPreHook` (HTTP layer entry)
2. `PreLLMHook` (Before LLM provider)
3. *LLM Provider Call*
4. `PostLLMHook` (After LLM provider)
5. `HTTPTransportPostHook` (HTTP layer exit)
6. `Inject` (Asynchronous trace delivery)

For an MCP request:

1. `HTTPTransportPreHook` (HTTP layer entry)
2. `PreMCPHook` (Before MCP server)
3. *MCP Server Call*
4. `PostMCPHook` (After MCP server)
5. `HTTPTransportPostHook` (HTTP layer exit)
6. `Inject` (Asynchronous trace delivery)

## Notes

- This plugin tracks state across requests (request count, start time)
- Context metadata flows from HTTP → LLM/MCP hooks
- `Inject` is called asynchronously after response is sent
- Perfect template for building comprehensive observability solutions
