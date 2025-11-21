# MCP CodeMode Test Suite

This directory contains comprehensive tests for the MCP (Model Context Protocol) CodeMode functionality in Bifrost.

## Overview

The test suite is organized into two main categories:

1. **Basic MCP Connection & Tool Registry Tests** (`mcp_connection_test.go`)
   - Uses default MCP mode (`MCPModeDefault`)
   - MCP manager initialization
   - Local tool registration
   - External MCP server connections
   - Tool discovery and execution

2. **CodeMode Execution Tests** (`codemode_test.go`)
   - Uses CodeMode MCP mode (`MCPModeCodeMode`)
   - Basic JavaScript execution
   - MCP tool calls
   - Import/export stripping
   - Expression analysis and auto-return
   - Error handling
   - Edge cases and complex scenarios
   - Environment and sandbox tests
   - Performance tests

## MCP Mode Configuration

The test suite uses different MCP modes depending on the test category:

- **Default Mode**: Used by `mcp_connection_test.go` for basic MCP functionality tests
- **CodeMode**: Used by `codemode_test.go` for JavaScript code execution tests

The mode is configured via the `Mode` field in `MCPConfig`:
- `schemas.MCPModeDefault` - Standard MCP tool execution
- `schemas.MCPModeCodeMode` - CodeMode with JavaScript execution capabilities

## Test Structure

### Setup Files

- `setup.go` - Test setup utilities for initializing Bifrost and registering test tools
- `fixtures.go` - Sample JavaScript code snippets and expected results
- `utils.go` - Test helper functions for assertions and validation

### Test Files

- `mcp_connection_test.go` - Basic MCP connection and tool registry tests
- `codemode_test.go` - Comprehensive CodeMode execution tests

## Running Tests

### Run all tests:
```bash
cd tests/core-mcp
go test -v ./...
```

### Run specific test:
```bash
go test -v -run TestSimpleExpression
```

### Run with coverage:
```bash
go test -v -cover ./...
```

## Test Tools

The test suite registers several test tools for testing:

1. **echo** - Simple echo that returns input
2. **add** - Adds two numbers
3. **multiply** - Multiplies two numbers
4. **get_data** - Returns structured data (object/array)
5. **error_tool** - Tool that always returns an error
6. **slow_tool** - Tool that takes time to execute
7. **complex_args_tool** - Tool that accepts complex nested arguments

## External MCP Server Testing

To test with external MCP servers, you can configure connection details via environment variables:

```bash
export EXTERNAL_MCP_CONNECTION_STRING="https://your-mcp-server.com"
export EXTERNAL_MCP_CONNECTION_TYPE="http"  # or "sse"
```

Then enable the `TestExternalMCPConnection` test by removing the `t.Skip()` call.

## Test Categories

### A. Basic Execution Tests
- Simple expressions
- Variable assignments
- Console logging
- Return values
- Auto-return functionality

### B. MCP Tool Call Tests
- Single tool calls
- Promise-based tool calls
- Tool call chains
- Error handling
- Multiple server tool calls
- Complex arguments

### C. Import/Export Stripping Tests
- ES6 import statement stripping
- ES6 export statement stripping
- Multiple import/export handling
- Comments preservation

### D. Expression Analysis & Auto-Return Tests
- Function call auto-return
- Promise chain auto-return
- Object literal auto-return
- Assignment statement handling
- Control flow statement handling
- Top-level return preservation

### E. Error Handling Tests
- Undefined variable errors
- Undefined server errors
- Undefined tool errors
- Tool call error propagation
- Syntax errors
- Runtime errors
- Timeout handling

### F. Edge Cases & Complex Scenarios
- Nested promise chains
- Promise error handling
- Complex data structures
- Multi-line expressions
- Empty code
- Whitespace-only code
- Comments-only code
- Function definitions

### G. Environment & Sandbox Tests
- Browser API absence (fetch, XMLHttpRequest, setTimeout)
- Node.js API absence (require, import)
- Async/await syntax limitations
- Environment object availability
- Server keys accessibility

### H. Long-Running & Performance Tests
- Long-running code execution
- Timeout behavior
- Large data structures
- Multiple concurrent tool calls

## Notes

- All tests use a timeout context to prevent hanging
- Tests are designed to be independent and can run in parallel
- The test suite uses the `bifrost-internal` server for local tool registration
- CodeMode tests verify that JavaScript code executes correctly in the sandboxed goja VM
- Error handling tests verify that helpful error hints are provided

