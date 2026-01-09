# MCP Test Suite

This directory contains comprehensive tests for the MCP (Model Context Protocol) functionality in Bifrost, covering code mode and non-code mode clients, auto-execute and non-auto-execute tools, and their various combinations.

## Overview

The test suite is organized into multiple test files covering different aspects of MCP:

1. **Client Configuration Tests** (`client_config_test.go`)
   - Single and multiple code mode clients
   - Single and multiple non-code mode clients
   - Mixed code mode + non-code mode clients
   - Client connection states
   - Client configuration updates

2. **Tool Execution Tests** (`tool_execution_test.go`)
   - Non-code mode tool execution (direct)
   - Code mode tool execution (`executeToolCode`)
   - Code mode calling code mode client tools
   - Code mode calling multiple servers
   - `listToolFiles` and `readToolFile` functionality

3. **Auto-Execute Configuration Tests** (`auto_execute_config_test.go`)
   - Tools in `ToolsToExecute` but not in `ToolsToAutoExecute`
   - Tools in both lists (auto-execute)
   - Tools in `ToolsToAutoExecute` but not in `ToolsToExecute` (should be skipped)
   - Wildcard configurations
   - Empty and nil configurations
   - Mixed auto-execute configurations

4. **Code Mode Auto-Execute Validation Tests** (`codemode_auto_execute_test.go`)
   - `executeToolCode` with code calling only auto-execute tools
   - `executeToolCode` with code calling non-auto-execute tools
   - `executeToolCode` with code calling mixed auto/non-auto tools
   - `executeToolCode` with no tool calls
   - `executeToolCode` with `listToolFiles`/`readToolFile` calls

5. **Agent Mode Tests** (`agent_mode_test.go`)
   - Agent mode configuration validation
   - Max depth configuration
   - Note: Full agent mode flow testing requires LLM integration (see `integration_test.go`)

6. **Edge Cases & Error Handling** (`edge_cases_test.go`)
   - Code mode client calling non-code mode client tool (runtime error)
   - Tool not in `ToolsToExecute` (should not be available)
   - Tool execution timeout
   - Tool execution error propagation
   - Empty code execution
   - Code with syntax errors
   - Code with TypeScript compilation errors
   - Code with runtime errors
   - Code calling tools with invalid arguments
   - Code mode tools always auto-executable

7. **Integration Tests** (`integration_test.go`)
   - Full workflow: `listToolFiles` → `readToolFile` → `executeToolCode`
   - Multiple code mode clients with different auto-execute configs
   - Tool filtering with code mode
   - Code mode and non-code mode tools in same request
   - Complex code execution scenarios
   - Error handling in code execution

8. **Basic MCP Connection Tests** (`mcp_connection_test.go`)
   - MCP manager initialization
   - Local tool registration
   - Tool discovery and execution
   - Multiple servers
   - Tool execution timeout and errors

## MCP Architecture

### Client Types

- **Code Mode Clients** (`IsCodeModeClient=true`):
  - Enable code mode tools: `listToolFiles`, `readToolFile`, `executeToolCode`
  - Tools accessible via TypeScript code execution in sandboxed VM
  - Only code mode clients appear in `listToolFiles` output

- **Non-Code Mode Clients** (`IsCodeModeClient=false`):
  - Tools exposed directly as function-calling tools
  - Cannot be called from `executeToolCode` code

### Tool Execution Modes

- **Auto-Execute Tools** (`ToolsToAutoExecute`):
  - Automatically executed in agent mode without user approval
  - Must also be in `ToolsToExecute` list
  - For `executeToolCode`: validates all tool calls within code against auto-execute list

- **Non-Auto-Execute Tools**:
  - Require explicit user approval in agent mode
  - Agent loop stops and returns these tools for user decision

### Agent Mode Behavior

When agent mode receives tool calls:

- **All auto-execute tools**: Executes all tools, makes new LLM call, continues loop
- **All non-auto-execute tools**: Stops immediately, returns tool calls in `tool_calls` field
- **Mixed scenario** (e.g., 3 auto-execute, 2 non-auto-execute):
  - Executes all auto-executable tools (3 in example)
  - Adds executed tool results to message content (formatted as JSON)
  - Includes non-auto-executable tool calls (2 in example) in `tool_calls` field
  - Sets `finish_reason` to "stop" (not "tool_calls") to prevent loop continuation
  - Returns immediately without making another LLM call

Agent mode respects `maxAgentDepth` limit and returns an error if exceeded.

## Test Structure

### Setup Files

- `setup.go` - Test setup utilities for initializing Bifrost and configuring clients
  - `setupTestBifrost()` - Basic Bifrost instance
  - `setupTestBifrostWithCodeMode()` - Bifrost with code mode enabled
  - `setupTestBifrostWithMCPConfig()` - Bifrost with custom MCP config
  - `setupCodeModeClient()` - Helper to create code mode client config
  - `setupNonCodeModeClient()` - Helper to create non-code mode client config
  - `setupClientWithAutoExecute()` - Helper to create client with auto-execute config
  - `registerTestTools()` - Registers test tools (echo, add, multiply, etc.)

- `fixtures.go` - Sample TypeScript code snippets and expected results
  - Basic expressions and tool calls
  - Auto-execute validation scenarios
  - Mixed client scenarios
  - Edge case scenarios

- `utils.go` - Test helper functions for assertions and validation
  - `createToolCall()` - Creates tool call messages
  - `assertExecutionResult()` - Validates execution results
  - `assertAgentModeResponse()` - Validates agent mode response structure
  - `extractExecutedToolResults()` - Extracts executed tool results from agent mode response
  - `canAutoExecuteTool()` - Checks if a tool can be auto-executed
  - `createMCPClientConfig()` - Creates MCP client configs

## Running Tests

### Run all tests:
```bash
cd tests/core-mcp
go test -v ./...
```

### Run specific test file:
```bash
go test -v -run TestClientConfig ./...
```

### Run specific test:
```bash
go test -v -run TestSingleCodeModeClient
```

### Run with coverage:
```bash
go test -v -cover ./...
```

### Run tests by category:
```bash
# Client configuration tests
go test -v -run "^Test.*Client.*" ./...

# Tool execution tests
go test -v -run "^Test.*Tool.*" ./...

# Auto-execute tests
go test -v -run "^Test.*Auto.*" ./...

# Edge case tests
go test -v -run "^Test.*Error|^Test.*Timeout|^Test.*Empty" ./...

# Integration tests
go test -v -run "^Test.*Workflow|^Test.*Integration" ./...
```

## Test Tools

The test suite registers several test tools:

1. **echo** - Simple echo that returns input
2. **add** - Adds two numbers
3. **multiply** - Multiplies two numbers
4. **get_data** - Returns structured data (object/array)
5. **error_tool** - Tool that always returns an error
6. **slow_tool** - Tool that takes time to execute
7. **complex_args_tool** - Tool that accepts complex nested arguments

## Key Test Scenarios

### Scenario 1: Mixed Auto-Execute and Non-Auto-Execute Tools (Critical)

When agent mode receives 5 tool calls: 3 auto-execute, 2 non-auto-execute:
- Agent executes the 3 auto-execute tools
- Adds their results to message content (JSON formatted)
- Includes the 2 non-auto-execute tool calls in `tool_calls` field
- Sets `finish_reason` to "stop"
- Stops immediately (no further LLM call)
- Response structure validated correctly

### Scenario 2: Code Mode Client + Auto-Execute Tools

- Setup: Code mode client with tools configured for auto-execute
- Test: `executeToolCode` with code calling these tools should auto-execute in agent mode

### Scenario 3: Mixed Client Types

- Setup: One code mode client + one non-code mode client
- Test: Code mode tools only see code mode client, non-code mode tools available separately

### Scenario 4: Auto-Execute Validation in Code

- Setup: Code mode client with mixed auto-execute config
- Test: `executeToolCode` validates all tool calls in code against auto-execute list

### Scenario 5: Code Mode Tools Always Auto-Execute

- Setup: Code mode enabled
- Test: `listToolFiles` and `readToolFile` always auto-execute regardless of config

## Notes

- All tests use a timeout context to prevent hanging
- Tests are designed to be independent and can run in parallel
- The test suite uses the `bifrostInternal` server for local tool registration
- Code mode tests verify that TypeScript code is transpiled and executes correctly in the sandboxed goja VM
- TypeScript compilation errors are caught and reported with helpful hints
- Async/await syntax is automatically transpiled to Promise chains compatible with goja
- Error handling tests verify that helpful error hints are provided for both runtime and TypeScript compilation errors
- Agent mode tests verify the critical mixed auto-execute/non-auto-execute scenario where some tools are executed and others are returned for user approval
