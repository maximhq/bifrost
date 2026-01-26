# HTTP MCP Server Without Ping Support

This is a sample MCP server implementation that runs over HTTP but **does not support the optional `ping` method**. This demonstrates how to configure Bifrost to use the `listTools` health check method instead of ping.

## What is This?

Many MCP servers may not implement the optional `ping` method from the MCP specification. This example shows:

1. **How to build an MCP server** that only supports the core methods (`list_tools`, `call_tool`) but not `ping`
2. **How to configure Bifrost** to work with such servers using `is_ping_available: false`
3. **Why this matters**: When `is_ping_available` is `false`, Bifrost will use `listTools` for health checks instead of the lightweight `ping` method

## Running the Server

### Prerequisites

```bash
go 1.21+
```

### Start the Server

```bash
# From this directory
go run main.go
```

Output:
```
MCP server listening on http://localhost:3001/mcp
Note: This server does NOT support ping. Use is_ping_available=false in Bifrost config.
```

## Connecting via Bifrost

### Configuration (config.json)

```json
{
  "mcp": {
    "client_configs": [
      {
        "name": "http_no_ping_server",
        "connection_type": "http",
        "connection_string": "http://localhost:3001/mcp",
        "is_ping_available": false,
        "tools_to_execute": ["*"]
      }
    ]
  }
}
```

### Via API

```bash
curl -X POST http://localhost:8080/api/mcp/client \
  -H "Content-Type: application/json" \
  -d '{
    "name": "http_no_ping_server",
    "connection_type": "http",
    "connection_string": "http://localhost:3001/mcp",
    "is_ping_available": false,
    "tools_to_execute": ["*"]
  }'
```

### Via Web UI

1. Navigate to **MCP Gateway**
2. Click **New MCP Server**
3. Fill in:
   - **Name**: `http_no_ping_server`
   - **Connection Type**: HTTP
   - **Connection URL**: `http://localhost:3001/mcp`
   - **Ping Available for Health Check**: Toggle OFF (disabled)
4. Click **Create**

## Available Tools

This server provides three simple tools for testing:

### 1. echo
Echoes back the input message.

```json
{
  "name": "echo",
  "arguments": {
    "message": "Hello, World!"
  }
}
```

### 2. add
Adds two numbers together.

```json
{
  "name": "add",
  "arguments": {
    "a": 5,
    "b": 3
  }
}
```

### 3. greet
Greets someone by name.

```json
{
  "name": "greet",
  "arguments": {
    "name": "Alice"
  }
}
```

## Health Check Behavior

When you add this server to Bifrost with `is_ping_available: false`:

1. Bifrost will **NOT** send `ping` requests (since the server doesn't support them)
2. Instead, Bifrost will use `listTools` every 10 seconds to check server health
3. If `listTools` fails 5 consecutive times, the server will be marked as `disconnected`

**Why `listTools` instead of `ping`?**
- `ping` is lighter and faster, but optional in MCP
- `listTools` is heavier but guaranteed to exist on all MCP servers
- Using `listTools` for health checks is a fallback for servers without `ping` support

## Implementation Notes

This example intentionally:

- ✅ Supports all core MCP methods (list_tools, call_tool)
- ✅ Returns proper JSON-RPC responses
- ✅ Works over HTTP
- ❌ Does NOT implement the `ping` method
- ❌ Returns a JSON-RPC method-not-found error (-32601) when ping is attempted

### How Ping is Blocked

The mcp-go library's `NewStreamableHTTPServer` automatically includes ping support by default. To demonstrate a server without ping, this example uses **HTTP middleware** that:

1. Intercepts all POST requests
2. Checks if the request is a `ping` method call
3. If it's a ping request, returns a JSON-RPC error: `{"code": -32601, "message": "Method not found: ping is not supported by this server"}`
4. For all other requests (list_tools, call_tool), passes them through normally

This allows us to:
- ✅ Keep the simple mcp-go server implementation
- ✅ Transparently block ping requests at the HTTP layer
- ✅ Return proper JSON-RPC error responses
- ✅ Demonstrate the `is_ping_available=false` behavior in Bifrost

## Key Learning: is_ping_available

The `is_ping_available` setting is important because:

| Setting | Health Check Method | When to Use |
|---------|-------------------|-----------|
| `true` (default) | Lightweight `ping` | When your server supports ping (recommended) |
| `false` | Heavier `listTools` | When your server doesn't support ping |

## See Also

- [MCP Specification](https://spec.modelcontextprotocol.io/)
- [Bifrost MCP Documentation](../../docs/mcp/connecting-to-servers.mdx)
- [Health Monitoring Guide](../../docs/mcp/connecting-to-servers.mdx#health-monitoring)
