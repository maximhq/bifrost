# Test Tools MCP Server

Standard MCP STDIO server with common test tools for integration testing.

## Tools

- **echo** - Echoes back a message
- **calculator** - Basic arithmetic operations (add, subtract, multiply, divide)
- **get_weather** - Mock weather data
- **delay** - Delays execution for testing timeouts
- **throw_error** - Throws an error for testing error handling

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

This server is designed to be used with Bifrost's MCP integration tests via STDIO transport.
