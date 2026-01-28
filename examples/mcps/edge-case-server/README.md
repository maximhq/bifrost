# Edge Case MCP Server

MCP STDIO server optimized for testing edge cases and unusual scenarios.

## Tools

- **unicode_tool** - Returns Unicode text including emojis and right-to-left characters
- **binary_data** - Returns binary-like data in various encodings (base64, hex, raw)
- **empty_response** - Returns various types of empty responses (empty string, object, array, null)
- **null_fields** - Returns responses with configurable null fields
- **deeply_nested** - Returns deeply nested data structures up to specified depth
- **special_chars** - Returns text with special characters (quotes, backslashes, newlines, control chars)
- **zero_length** - Returns zero-length content
- **extreme_sizes** - Returns data of various extreme sizes (tiny, normal, huge)

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

This server is designed to test edge case handling in Bifrost's MCP integration via STDIO transport.

### Example Tool Calls

```typescript
// Test Unicode handling
{
  "name": "unicode_tool",
  "arguments": {
    "id": "test-1",
    "include_emojis": true,
    "include_rtl": true
  }
}

// Test binary data
{
  "name": "binary_data",
  "arguments": {
    "id": "test-2",
    "encoding": "base64"
  }
}

// Test deeply nested structures
{
  "name": "deeply_nested",
  "arguments": {
    "id": "test-3",
    "depth": 20
  }
}

// Test special characters
{
  "name": "special_chars",
  "arguments": {
    "id": "test-4",
    "char_type": "all"
  }
}
```
