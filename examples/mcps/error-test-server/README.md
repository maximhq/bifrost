# Error Test MCP Server

MCP STDIO server optimized for testing error scenarios and edge cases.

## Tools

- **malformed_json** - Returns malformed JSON (truncated, invalid escapes, unclosed brackets, mixed types)
- **timeout_tool** - Hangs for specified duration to test timeout handling
- **intermittent_fail** - Randomly fails based on fail_rate to test retry logic
- **network_error** - Simulates network errors (connection refused, timeout, DNS failure, SSL errors)
- **large_payload** - Returns very large payloads to test size limits
- **partial_response** - Returns incomplete responses to test handling
- **invalid_content_type** - Returns content with mismatched type declaration

## Usage

```bash
# Install dependencies
npm install

# Build
npm run build

# Run
node dist/index.js
```

## Integration Testing

This server is designed to test error handling in Bifrost's MCP integration via STDIO transport.

### Example Tool Calls

```typescript
// Test malformed JSON
{
  "name": "malformed_json",
  "arguments": {
    "id": "test-1",
    "json_type": "truncated"
  }
}

// Test timeout
{
  "name": "timeout_tool",
  "arguments": {
    "id": "test-2",
    "timeout_ms": 3000
  }
}

// Test intermittent failures
{
  "name": "intermittent_fail",
  "arguments": {
    "id": "test-3",
    "fail_rate": 0.7
  }
}

// Test large payloads
{
  "name": "large_payload",
  "arguments": {
    "id": "test-4",
    "size_kb": 500
  }
}
```
