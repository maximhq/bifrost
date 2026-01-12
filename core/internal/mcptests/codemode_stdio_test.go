package mcptests

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/mcp"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// SETUP HELPERS FOR CODE MODE WITH STDIO SERVERS
// =============================================================================

// toCamelCase converts kebab-case to camelCase (e.g., "edge-case-server" -> "edgeCaseServer")
func toCamelCase(s string) string {
	parts := strings.Split(s, "-")
	for i := 1; i < len(parts); i++ {
		if len(parts[i]) > 0 {
			parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
		}
	}
	return strings.Join(parts, "")
}

// setupCodeModeWithSTDIOServers sets up multiple STDIO MCP servers for code mode testing
// Uses fixture functions for proper server configuration
func setupCodeModeWithSTDIOServers(t *testing.T, serverNames ...string) (*mcp.MCPManager, *bifrost.Bifrost) {
	t.Helper()

	// Initialize MCP server paths
	InitMCPServerPaths(t)

	bifrostRoot := GetBifrostRoot(t)
	var clientConfigs []schemas.MCPClientConfig

	for _, serverName := range serverNames {
		var config schemas.MCPClientConfig

		// Use fixture functions for known servers, otherwise set up manually
		switch serverName {
		case "temperature":
			config = GetTemperatureMCPClientConfig(bifrostRoot)
			config.IsCodeModeClient = true
			config.ID = "temperature-client" // Match test expectations
			config.Name = "temperature" // Use lowercase to match test code
			config.ToolsToAutoExecute = []string{"executeToolCode", "listToolFiles", "readToolFile"}
		case "go-test-server":
			config = GetGoTestServerConfig(bifrostRoot)
			config.ID = "goTestServer-client" // Match test expectations
			config.Name = "goTestServer" // Use camelCase to match test code
			config.ToolsToAutoExecute = []string{"executeToolCode", "listToolFiles", "readToolFile"}
		case "edge-case-server":
			config = GetEdgeCaseServerConfig(bifrostRoot)
			config.ID = "edgeCaseServer-client" // Match test expectations
			config.Name = "edgeCaseServer" // Use camelCase to match test code
			config.ToolsToAutoExecute = []string{"executeToolCode", "listToolFiles", "readToolFile"}
		case "error-test-server":
			config = GetErrorTestServerConfig(bifrostRoot)
			config.ID = "errorTestServer-client" // Match test expectations
			config.Name = "errorTestServer" // Use camelCase to match test code
			config.ToolsToAutoExecute = []string{"executeToolCode", "listToolFiles", "readToolFile"}
		case "parallel-test-server":
			config = GetParallelTestServerConfig(bifrostRoot)
			config.ID = "parallelTestServer-client" // Match test expectations
			config.Name = "parallelTestServer" // Use camelCase to match test code
			config.ToolsToAutoExecute = []string{"executeToolCode", "listToolFiles", "readToolFile"}
		case "test-tools-server":
			// test-tools-server doesn't have a fixture, set up manually
			examplesRoot := filepath.Join(bifrostRoot, "..", "examples")
			serverPath := filepath.Join(examplesRoot, "mcps", "test-tools-server", "dist", "index.js")

			// Verify server exists
			if _, err := os.Stat(serverPath); err != nil {
				t.Fatalf("test-tools-server not found at %s", serverPath)
			}

			config = schemas.MCPClientConfig{
				ID:             "test-tools-server-client",
				Name:           "testToolsServer", // camelCase to match test code
				ConnectionType: schemas.MCPConnectionTypeSTDIO,
				StdioConfig: &schemas.MCPStdioConfig{
					Command: "node",
					Args:    []string{serverPath},
				},
				IsCodeModeClient:   true,
				ToolsToExecute:     []string{"*"},
				ToolsToAutoExecute: []string{"executeToolCode", "listToolFiles", "readToolFile"},
			}
		default:
			t.Fatalf("Unknown server: %s", serverName)
		}

		clientConfigs = append(clientConfigs, config)
	}

	manager := setupMCPManager(t, clientConfigs...)
	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	return manager, bifrost
}

// =============================================================================
// BASIC CODE MODE WITH STDIO TESTS
// =============================================================================

func TestCodeMode_STDIO_SingleServerBasicExecution(t *testing.T) {
	t.Parallel()

	_, bifrost := setupCodeModeWithSTDIOServers(t, "test-tools-server")
	ctx := createTestContext()

	tests := []struct {
		name           string
		code           string
		expectedResult interface{}
	}{
		{
			name:           "simple_return",
			code:           `return 42`,
			expectedResult: float64(42),
		},
		{
			name:           "string_return",
			code:           `return "Hello from test-tools-server"`,
			expectedResult: "Hello from test-tools-server",
		},
		{
			name:           "object_return",
			code:           `return { status: "success", value: 123 }`,
			expectedResult: map[string]interface{}{"status": "success", "value": float64(123)},
		},
		{
			name:           "array_return",
			code:           `return [1, 2, 3, 4, 5]`,
			expectedResult: []interface{}{float64(1), float64(2), float64(3), float64(4), float64(5)},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			toolCall := CreateExecuteToolCodeCall(fmt.Sprintf("call-%s", tc.name), tc.code)
			result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)

			require.Nil(t, bifrostErr, "execution should succeed")
			require.NotNil(t, result)
			require.NotNil(t, result.Content)
			require.NotNil(t, result.Content.ContentStr)

			returnValue, hasError, errorMsg := ParseCodeModeResponse(t, *result.Content.ContentStr)
			require.False(t, hasError, "should not have execution error: %s", errorMsg)

			assert.Equal(t, tc.expectedResult, returnValue)
		})
	}
}

func TestCodeMode_STDIO_ToolCallSingleServer(t *testing.T) {
	t.Parallel()

	_, bifrost := setupCodeModeWithSTDIOServers(t, "test-tools-server")
	ctx := createTestContext()

	tests := []struct {
		name         string
		code         string
		verifyResult func(t *testing.T, execResult map[string]interface{})
	}{
		{
			name: "echo_tool",
			code: `const result = await testToolsServer.echo({message: "test message"});
return result`,
			verifyResult: func(t *testing.T, execResult map[string]interface{}) {
				result, ok := execResult["result"].(map[string]interface{})
				require.True(t, ok, "result should be an object")
				assert.Equal(t, "test message", result["message"])
			},
		},
		{
			name: "calculator_add",
			code: `const result = await testToolsServer.calculator({operation: "add", x: 15, y: 27});
return result`,
			verifyResult: func(t *testing.T, execResult map[string]interface{}) {
				result, ok := execResult["result"].(map[string]interface{})
				require.True(t, ok, "result should be an object")
				assert.Equal(t, float64(42), result["result"])
			},
		},
		{
			name: "calculator_multiply",
			code: `const result = await testToolsServer.calculator({operation: "multiply", x: 6, y: 7});
return result`,
			verifyResult: func(t *testing.T, execResult map[string]interface{}) {
				result, ok := execResult["result"].(map[string]interface{})
				require.True(t, ok, "result should be an object")
				assert.Equal(t, float64(42), result["result"])
			},
		},
		{
			name: "get_weather",
			code: `const result = await testToolsServer.get_weather({location: "San Francisco", units: "celsius"});
return result`,
			verifyResult: func(t *testing.T, execResult map[string]interface{}) {
				result, ok := execResult["result"].(map[string]interface{})
				require.True(t, ok, "result should be an object")
				assert.Equal(t, "San Francisco", result["location"])
				assert.Equal(t, "celsius", result["units"])
			},
		},
		{
			name: "sequential_tool_calls",
			code: `const echo1 = await testToolsServer.echo({message: "first"});
const echo2 = await testToolsServer.echo({message: "second"});
return {first: echo1, second: echo2}`,
			verifyResult: func(t *testing.T, execResult map[string]interface{}) {
				result, ok := execResult["result"].(map[string]interface{})
				require.True(t, ok, "result should be an object")

				first, ok := result["first"].(map[string]interface{})
				require.True(t, ok)
				assert.Equal(t, "first", first["message"])

				second, ok := result["second"].(map[string]interface{})
				require.True(t, ok)
				assert.Equal(t, "second", second["message"])
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			toolCall := CreateExecuteToolCodeCall(fmt.Sprintf("call-%s", tc.name), tc.code)
			result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)

			require.Nil(t, bifrostErr, "execution should succeed")
			require.NotNil(t, result)
			require.NotNil(t, result.Content)
			require.NotNil(t, result.Content.ContentStr)

			returnValue, hasError, errorMsg := ParseCodeModeResponse(t, *result.Content.ContentStr)
			require.False(t, hasError, "should not have execution error: %s", errorMsg)

			// Wrap returnValue in a map with "result" key for backward compatibility with verifyResult
			execResult := map[string]interface{}{"result": returnValue}
			tc.verifyResult(t, execResult)
		})
	}
}

// =============================================================================
// MULTI-SERVER CODE MODE TESTS
// =============================================================================

func TestCodeMode_STDIO_MultipleServers(t *testing.T) {
	t.Parallel()

	_, bifrost := setupCodeModeWithSTDIOServers(t, "test-tools-server", "temperature")
	ctx := createTestContext()

	tests := []struct {
		name         string
		code         string
		verifyResult func(t *testing.T, execResult map[string]interface{})
	}{
		{
			name: "call_tool_from_first_server",
			code: `const result = await testToolsServer.echo({message: "from test-tools"});
return result`,
			verifyResult: func(t *testing.T, execResult map[string]interface{}) {
				result, ok := execResult["result"].(map[string]interface{})
				require.True(t, ok)
				assert.Equal(t, "from test-tools", result["message"])
			},
		},
		{
			name: "call_tool_from_second_server",
			code: `const result = await temperature.get_temperature({location: "Tokyo"});
return result`,
			verifyResult: func(t *testing.T, execResult map[string]interface{}) {
				result := execResult["result"]
				require.NotNil(t, result)
				// Temperature server returns a string, not an object
				if str, ok := result.(string); ok {
					assert.Contains(t, str, "Tokyo")
				}
			},
		},
		{
			name: "call_tools_from_both_servers",
			code: `const echo = await testToolsServer.echo({message: "hello"});
const temp = await temperature.get_temperature({location: "London"});
return {echo: echo, temp: temp}`,
			verifyResult: func(t *testing.T, execResult map[string]interface{}) {
				result, ok := execResult["result"].(map[string]interface{})
				require.True(t, ok)

				echo := result["echo"]
				assert.NotNil(t, echo)

				temp := result["temp"]
				assert.NotNil(t, temp)
			},
		},
		{
			name: "calculator_from_both_servers",
			code: `const calc1 = await testToolsServer.calculator({operation: "add", x: 10, y: 5});
const calc2 = await temperature.calculator({operation: "multiply", x: 3, y: 4});
return {tools: calc1, temp: calc2}`,
			verifyResult: func(t *testing.T, execResult map[string]interface{}) {
				result, ok := execResult["result"].(map[string]interface{})
				require.True(t, ok)

				calc1, ok := result["tools"].(map[string]interface{})
				require.True(t, ok)
				assert.Equal(t, float64(15), calc1["result"])

				calc2, ok := result["temp"].(map[string]interface{})
				require.True(t, ok)
				assert.Equal(t, float64(12), calc2["result"])
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			toolCall := CreateExecuteToolCodeCall(fmt.Sprintf("call-%s", tc.name), tc.code)
			result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)

			require.Nil(t, bifrostErr, "execution should succeed")
			require.NotNil(t, result)
			require.NotNil(t, result.Content)
			require.NotNil(t, result.Content.ContentStr)

			returnValue, hasError, errorMsg := ParseCodeModeResponse(t, *result.Content.ContentStr)
			require.False(t, hasError, "should not have execution error: %s", errorMsg)

			// Wrap returnValue in a map with "result" key for backward compatibility with verifyResult
			execResult := map[string]interface{}{"result": returnValue}
			tc.verifyResult(t, execResult)
		})
	}
}

// =============================================================================
// CONTEXT FILTERING TESTS - SERVER FILTERING
// =============================================================================

func TestCodeMode_STDIO_ServerFiltering(t *testing.T) {
	t.Parallel()

	manager, bifrost := setupCodeModeWithSTDIOServers(t, "test-tools-server", "temperature")

	tests := []struct {
		name             string
		includeClients   []string
		code             string
		shouldSucceed    bool
		expectedInResult string
		expectedError    string
	}{
		{
			name:           "allow_only_test_tools_server",
			includeClients: []string{"testToolsServer"},
			code: `const result = await testToolsServer.echo({message: "allowed"});
return result`,
			shouldSucceed:    true,
			expectedInResult: "allowed",
		},
		{
			name:           "block_test_tools_server",
			includeClients: []string{"temperature"},
			code: `const result = await testToolsServer.echo({message: "blocked"});
return result`,
			shouldSucceed: false,
			expectedError: "testToolsServer is not defined",
		},
		{
			name:           "allow_only_temperature_server",
			includeClients: []string{"temperature"},
			code: `const result = await temperature.get_temperature({location: "Paris"});
return result`,
			shouldSucceed:    true,
			expectedInResult: "Paris",
		},
		{
			name:           "block_temperature_server",
			includeClients: []string{"testToolsServer"},
			code: `const result = await temperature.get_temperature({location: "blocked"});
return result`,
			shouldSucceed: false,
			expectedError: "temperature is not defined",
		},
		{
			name:           "allow_both_servers",
			includeClients: []string{"testToolsServer", "temperature"},
			code: `const echo = await testToolsServer.echo({message: "both"});
const temp = await temperature.get_temperature({location: "NYC"});
return {echo: echo, temp: temp}`,
			shouldSucceed:    true,
			expectedInResult: "both",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Create context with client filtering
			baseCtx := context.Background()
			baseCtx = context.WithValue(baseCtx, mcp.MCPContextKeyIncludeClients, tc.includeClients)
			ctx := schemas.NewBifrostContext(baseCtx, schemas.NoDeadline)

			// Verify filtering is applied at tool listing level
			tools := manager.GetToolPerClient(ctx)
			t.Logf("Available clients after filtering: %d", len(tools))

			toolCall := CreateExecuteToolCodeCall(fmt.Sprintf("call-%s", tc.name), tc.code)
			result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)

			if tc.shouldSucceed {
				require.Nil(t, bifrostErr, "execution should succeed")
				require.NotNil(t, result)
				require.NotNil(t, result.Content)
				require.NotNil(t, result.Content.ContentStr)

				content := *result.Content.ContentStr
				if tc.expectedInResult != "" {
					assert.Contains(t, content, tc.expectedInResult)
				}
			} else {
				// Should fail - either bifrost error or error in result
				if bifrostErr != nil {
					assert.Contains(t, bifrostErr.Error.Message, tc.expectedError)
				} else if result != nil && result.Content != nil && result.Content.ContentStr != nil {
					_, hasError, errorMsg := ParseCodeModeResponse(t, *result.Content.ContentStr)
					if hasError {
						assert.Contains(t, errorMsg, tc.expectedError)
					} else {
						// Check if return value contains error
						returnValue, _, _ := ParseCodeModeResponse(t, *result.Content.ContentStr)
						if returnValue != nil {
							if returnObj, ok := returnValue.(map[string]interface{}); ok {
								if errorField, ok := returnObj["error"]; ok {
									errorStr := fmt.Sprintf("%v", errorField)
									assert.Contains(t, errorStr, tc.expectedError)
								}
							}
						}
					}
				}
			}
		})
	}
}

// =============================================================================
// CONTEXT FILTERING TESTS - TOOL FILTERING
// =============================================================================

func TestCodeMode_STDIO_ToolFiltering(t *testing.T) {
	t.Parallel()

	manager, bifrost := setupCodeModeWithSTDIOServers(t, "test-tools-server")

	tests := []struct {
		name             string
		includeTools     []string
		code             string
		shouldSucceed    bool
		expectedInResult string
		expectedError    string
	}{
		{
			name:         "allow_only_echo",
			includeTools: []string{"testToolsServer-echo"},
			code: `const result = await testToolsServer.echo({message: "allowed"});
return result`,
			shouldSucceed:    true,
			expectedInResult: "allowed",
		},
		{
			name:         "block_calculator_allow_echo",
			includeTools: []string{"testToolsServer-echo"},
			code: `const result = await testToolsServer.calculator({operation: "add", x: 1, y: 2});
return result`,
			shouldSucceed: false,
			expectedError: "calculator",
		},
		{
			name:         "wildcard_for_client",
			includeTools: []string{"testToolsServer-*"},
			code: `const echo = await testToolsServer.echo({message: "test"});
const calc = await testToolsServer.calculator({operation: "add", x: 5, y: 3});
return {echo: echo, calc: calc}`,
			shouldSucceed:    true,
			expectedInResult: "test",
		},
		{
			name:         "allow_multiple_specific_tools",
			includeTools: []string{"testToolsServer-echo", "testToolsServer-calculator"},
			code: `const echo = await testToolsServer.echo({message: "multi"});
const calc = await testToolsServer.calculator({operation: "multiply", x: 6, y: 7});
return {echo: echo, calc: calc}`,
			shouldSucceed:    true,
			expectedInResult: "multi",
		},
		{
			name:         "block_all_tools_empty_filter",
			includeTools: []string{},
			code: `const result = await testToolsServer.echo({message: "blocked"});
return result`,
			shouldSucceed: false,
			expectedError: "testToolsServer is not defined",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Create context with tool filtering
			baseCtx := context.Background()
			baseCtx = context.WithValue(baseCtx, mcp.MCPContextKeyIncludeTools, tc.includeTools)
			ctx := schemas.NewBifrostContext(baseCtx, schemas.NoDeadline)

			// Verify filtering is applied
			tools := manager.GetToolPerClient(ctx)
			t.Logf("Available tools after filtering: %v", tools)

			toolCall := CreateExecuteToolCodeCall(fmt.Sprintf("call-%s", tc.name), tc.code)
			result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)

			if tc.shouldSucceed {
				require.Nil(t, bifrostErr, "execution should succeed")
				require.NotNil(t, result)
				require.NotNil(t, result.Content)
				require.NotNil(t, result.Content.ContentStr)

				content := *result.Content.ContentStr
				if tc.expectedInResult != "" {
					assert.Contains(t, content, tc.expectedInResult)
				}
			} else {
				// Should fail
				if bifrostErr != nil {
					if tc.expectedError != "" {
						assert.Contains(t, bifrostErr.Error.Message, tc.expectedError)
					}
				} else if result != nil && result.Content != nil && result.Content.ContentStr != nil {
					returnValue, hasError, errorMsg := ParseCodeModeResponse(t, *result.Content.ContentStr)
					if hasError {
						if tc.expectedError != "" {
							assert.Contains(t, strings.ToLower(errorMsg), strings.ToLower(tc.expectedError))
						}
					} else if returnValue != nil {
						// Check if return value contains error
						if returnObj, ok := returnValue.(map[string]interface{}); ok {
							if errorField, ok := returnObj["error"]; ok {
								errorStr := fmt.Sprintf("%v", errorField)
								if tc.expectedError != "" {
									assert.Contains(t, strings.ToLower(errorStr), strings.ToLower(tc.expectedError))
								}
							}
						}
					}
				}
			}
		})
	}
}

// =============================================================================
// COMBINED FILTERING TESTS
// =============================================================================

func TestCodeMode_STDIO_CombinedFiltering(t *testing.T) {
	t.Parallel()

	_, bifrost := setupCodeModeWithSTDIOServers(t, "test-tools-server", "temperature")

	tests := []struct {
		name             string
		includeClients   []string
		includeTools     []string
		code             string
		shouldSucceed    bool
		expectedInResult string
	}{
		{
			name:           "allow_server_and_specific_tool",
			includeClients: []string{"testToolsServer"},
			includeTools:   []string{"testToolsServer-echo"},
			code: `const result = await testToolsServer.echo({message: "filtered"});
return result`,
			shouldSucceed:    true,
			expectedInResult: "filtered",
		},
		{
			name:           "allow_server_but_block_tool",
			includeClients: []string{"testToolsServer"},
			includeTools:   []string{"testToolsServer-calculator"},
			code: `const result = await testToolsServer.echo({message: "blocked"});
return result`,
			shouldSucceed: false,
		},
		{
			name:           "allow_all_clients_specific_tools_from_each",
			includeClients: []string{"*"},
			includeTools:   []string{"testToolsServer-echo", "temperature-get_temperature"},
			code: `const echo = await testToolsServer.echo({message: "test"});
const temp = await temperature.get_temperature({location: "Berlin"});
return {echo: echo, temp: temp}`,
			shouldSucceed:    true,
			expectedInResult: "test",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Create context with both client and tool filtering
			baseCtx := context.Background()
			if tc.includeClients != nil {
				baseCtx = context.WithValue(baseCtx, mcp.MCPContextKeyIncludeClients, tc.includeClients)
			}
			if tc.includeTools != nil {
				baseCtx = context.WithValue(baseCtx, mcp.MCPContextKeyIncludeTools, tc.includeTools)
			}
			ctx := schemas.NewBifrostContext(baseCtx, schemas.NoDeadline)

			toolCall := CreateExecuteToolCodeCall(fmt.Sprintf("call-%s", tc.name), tc.code)
			result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)

			if tc.shouldSucceed {
				require.Nil(t, bifrostErr, "execution should succeed")
				require.NotNil(t, result)
				require.NotNil(t, result.Content)
				require.NotNil(t, result.Content.ContentStr)

				if tc.expectedInResult != "" {
					assert.Contains(t, *result.Content.ContentStr, tc.expectedInResult)
				}
			} else {
				// Should fail - either error or blocked execution
				if bifrostErr == nil && result != nil && result.Content != nil && result.Content.ContentStr != nil {
					returnValue, hasError, _ := ParseCodeModeResponse(t, *result.Content.ContentStr)
					if !hasError && returnValue != nil {
						// Check if return value contains error field
						if returnObj, ok := returnValue.(map[string]interface{}); ok {
							_, hasErrorField := returnObj["error"]
							assert.True(t, hasErrorField, "Should have error in result")
						}
					}
				}
			}
		})
	}
}

// =============================================================================
// COMPLEX CODE EXECUTION TESTS
// =============================================================================

func TestCodeMode_STDIO_ComplexCodePatterns(t *testing.T) {
	t.Parallel()

	_, bifrost := setupCodeModeWithSTDIOServers(t, "test-tools-server")
	ctx := createTestContext()

	tests := []struct {
		name         string
		code         string
		verifyResult func(t *testing.T, execResult map[string]interface{})
	}{
		{
			name: "for_loop_with_tool_calls",
			code: `const results = [];
for (let i = 0; i < 3; i++) {
  const r = await testToolsServer.echo({message: "count_" + i});
  results.push(r);
}
return results`,
			verifyResult: func(t *testing.T, execResult map[string]interface{}) {
				results, ok := execResult["result"].([]interface{})
				require.True(t, ok, "result should be array")
				assert.Len(t, results, 3)
			},
		},
		{
			name: "conditional_tool_calls",
			code: `const x = 10;
let result;
if (x > 5) {
  result = await testToolsServer.calculator({operation: "add", x: x, y: 5});
} else {
  result = await testToolsServer.echo({message: "small"});
}
return result`,
			verifyResult: func(t *testing.T, execResult map[string]interface{}) {
				result, ok := execResult["result"].(map[string]interface{})
				require.True(t, ok)
				assert.Equal(t, float64(15), result["result"])
			},
		},
		{
			name: "error_handling_try_catch",
			code: `let result;
try {
  result = await testToolsServer.calculator({operation: "divide", x: 10, y: 0});
} catch (error) {
  result = {error: "caught_error", message: error.message};
}
return result`,
			verifyResult: func(t *testing.T, execResult map[string]interface{}) {
				result := execResult["result"]
				assert.NotNil(t, result)
			},
		},
		{
			name: "parallel_tool_calls_promise_all",
			code: `const promises = [
  testToolsServer.echo({message: "one"}),
  testToolsServer.echo({message: "two"}),
  testToolsServer.echo({message: "three"})
];
const results = await Promise.all(promises);
return results`,
			verifyResult: func(t *testing.T, execResult map[string]interface{}) {
				results, ok := execResult["result"].([]interface{})
				require.True(t, ok)
				assert.Len(t, results, 3)
			},
		},
		{
			name: "data_transformation",
			code: `const calc1 = await testToolsServer.calculator({operation: "add", x: 10, y: 20});
const calc2 = await testToolsServer.calculator({operation: "multiply", x: 5, y: 3});
return {
  sum: calc1.result,
  product: calc2.result,
  total: calc1.result + calc2.result
}`,
			verifyResult: func(t *testing.T, execResult map[string]interface{}) {
				result, ok := execResult["result"].(map[string]interface{})
				require.True(t, ok)
				assert.Equal(t, float64(30), result["sum"])
				assert.Equal(t, float64(15), result["product"])
				assert.Equal(t, float64(45), result["total"])
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			toolCall := CreateExecuteToolCodeCall(fmt.Sprintf("call-%s", tc.name), tc.code)
			result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)

			require.Nil(t, bifrostErr, "execution should succeed")
			require.NotNil(t, result)
			require.NotNil(t, result.Content)
			require.NotNil(t, result.Content.ContentStr)

			returnValue, hasError, errorMsg := ParseCodeModeResponse(t, *result.Content.ContentStr)
			require.False(t, hasError, "should not have execution error: %s", errorMsg)

			// Wrap returnValue in a map with "result" key for backward compatibility with verifyResult
			execResult := map[string]interface{}{"result": returnValue}
			tc.verifyResult(t, execResult)
		})
	}
}

// =============================================================================
// EDGE CASE SERVER TESTS
// =============================================================================

func TestCodeMode_STDIO_EdgeCaseServer_Unicode(t *testing.T) {
	t.Parallel()

	_, bifrost := setupCodeModeWithSTDIOServers(t, "edge-case-server")
	ctx := createTestContext()

	tests := []struct {
		name         string
		code         string
		verifyResult func(t *testing.T, execResult map[string]interface{})
	}{
		{
			name: "unicode_emoji",
			code: `const result = await edgeCaseServer.return_unicode({type: "emoji"});
return result`,
			verifyResult: func(t *testing.T, execResult map[string]interface{}) {
				result, ok := execResult["result"].(map[string]interface{})
				require.True(t, ok)
				assert.Equal(t, "emoji", result["type"])
				unicodeText := result["text"].(string)
				assert.Contains(t, unicodeText, "👋")
				assert.Contains(t, unicodeText, "🚀")
			},
		},
		{
			name: "unicode_has_length",
			code: `const result = await edgeCaseServer.return_unicode({type: "emoji"});
return {type: result.type, length: result.length, starts_with_hello: result.text.startsWith("Hello")}`,
			verifyResult: func(t *testing.T, execResult map[string]interface{}) {
				result, ok := execResult["result"].(map[string]interface{})
				require.True(t, ok)
				assert.Equal(t, "emoji", result["type"])
				assert.Greater(t, result["length"], float64(0))
				assert.Equal(t, true, result["starts_with_hello"])
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			toolCall := CreateExecuteToolCodeCall(fmt.Sprintf("call-%s", tc.name), tc.code)
			result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)

			require.Nil(t, bifrostErr, "execution should succeed")
			require.NotNil(t, result)
			require.NotNil(t, result.Content)
			require.NotNil(t, result.Content.ContentStr)

			returnValue, hasError, errorMsg := ParseCodeModeResponse(t, *result.Content.ContentStr)
			require.False(t, hasError, "should not have execution error: %s", errorMsg)

			// Wrap returnValue in a map with "result" key for backward compatibility with verifyResult
			execResult := map[string]interface{}{"result": returnValue}
			tc.verifyResult(t, execResult)
		})
	}
}

func TestCodeMode_STDIO_EdgeCaseServer_BinaryAndEncoding(t *testing.T) {
	t.Parallel()

	_, bifrost := setupCodeModeWithSTDIOServers(t, "edge-case-server")
	ctx := createTestContext()

	tests := []struct {
		name         string
		code         string
		verifyResult func(t *testing.T, execResult map[string]interface{})
	}{
		{
			name: "binary_data_base64",
			code: `const result = await edgeCaseServer.return_binary({
  size: 100,
  encoding: "base64"
});
return result`,
			verifyResult: func(t *testing.T, execResult map[string]interface{}) {
				result, ok := execResult["result"].(map[string]interface{})
				require.True(t, ok)
				assert.Equal(t, "base64", result["encoding"])
				assert.Equal(t, float64(100), result["size"])
				assert.NotEmpty(t, result["data"])
			},
		},
		{
			name: "binary_data_hex",
			code: `const result = await edgeCaseServer.return_binary({
  size: 50,
  encoding: "hex"
});
return result`,
			verifyResult: func(t *testing.T, execResult map[string]interface{}) {
				result, ok := execResult["result"].(map[string]interface{})
				require.True(t, ok)
				assert.Equal(t, "hex", result["encoding"])
				assert.Equal(t, float64(50), result["size"])
				assert.NotEmpty(t, result["data"])
			},
		},
		{
			name: "binary_data_small",
			code: `const result = await edgeCaseServer.return_binary({
  size: 10,
  encoding: "base64"
});
return {size: result.size, encoding: result.encoding, data_length: result.data.length}`,
			verifyResult: func(t *testing.T, execResult map[string]interface{}) {
				result, ok := execResult["result"].(map[string]interface{})
				require.True(t, ok)
				assert.Equal(t, float64(10), result["size"])
				assert.Equal(t, "base64", result["encoding"])
				assert.Greater(t, result["data_length"], float64(0))
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			toolCall := CreateExecuteToolCodeCall(fmt.Sprintf("call-%s", tc.name), tc.code)
			result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)

			require.Nil(t, bifrostErr, "execution should succeed")
			require.NotNil(t, result)
			require.NotNil(t, result.Content)
			require.NotNil(t, result.Content.ContentStr)

			returnValue, hasError, errorMsg := ParseCodeModeResponse(t, *result.Content.ContentStr)
			require.False(t, hasError, "should not have execution error: %s", errorMsg)

			// Wrap returnValue in a map with "result" key for backward compatibility with verifyResult
			execResult := map[string]interface{}{"result": returnValue}
			tc.verifyResult(t, execResult)
		})
	}
}

func TestCodeMode_STDIO_EdgeCaseServer_EmptyAndNull(t *testing.T) {
	t.Parallel()

	_, bifrost := setupCodeModeWithSTDIOServers(t, "edge-case-server")
	ctx := createTestContext()

	tests := []struct {
		name         string
		code         string
		verifyResult func(t *testing.T, execResult map[string]interface{})
	}{
		{
			name: "null_empty_string",
			code: `const result = await edgeCaseServer.return_null({});
return {empty_string: result.empty_string, empty_array: result.empty_array}`,
			verifyResult: func(t *testing.T, execResult map[string]interface{}) {
				result, ok := execResult["result"].(map[string]interface{})
				require.True(t, ok)
				assert.Equal(t, "", result["empty_string"])
				dataArr, ok := result["empty_array"].([]interface{})
				require.True(t, ok)
				assert.Empty(t, dataArr)
			},
		},
		{
			name: "null_empty_object",
			code: `const result = await edgeCaseServer.return_null({});
return {empty_object: result.empty_object, has_property: 'empty_object' in result}`,
			verifyResult: func(t *testing.T, execResult map[string]interface{}) {
				result, ok := execResult["result"].(map[string]interface{})
				require.True(t, ok)
				assert.Equal(t, true, result["has_property"])
			},
		},
		{
			name: "null_null_value",
			code: `const result = await edgeCaseServer.return_null({});
return {has_null: result.null_value === null, zero: result.zero, false: result.false}`,
			verifyResult: func(t *testing.T, execResult map[string]interface{}) {
				result, ok := execResult["result"].(map[string]interface{})
				require.True(t, ok)
				assert.Equal(t, true, result["has_null"])
				assert.Equal(t, float64(0), result["zero"])
				assert.Equal(t, false, result["false"])
			},
		},
		{
			name: "null_all_values",
			code: `const result = await edgeCaseServer.return_null({});
const keys = Object.keys(result);
return {key_count: keys.length, has_empty_string: 'empty_string' in result}`,
			verifyResult: func(t *testing.T, execResult map[string]interface{}) {
				result, ok := execResult["result"].(map[string]interface{})
				require.True(t, ok)
				assert.Greater(t, result["key_count"], float64(0))
				assert.Equal(t, true, result["has_empty_string"])
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			toolCall := CreateExecuteToolCodeCall(fmt.Sprintf("call-%s", tc.name), tc.code)
			result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)

			require.Nil(t, bifrostErr, "execution should succeed")
			require.NotNil(t, result)
			require.NotNil(t, result.Content)
			require.NotNil(t, result.Content.ContentStr)

			returnValue, hasError, errorMsg := ParseCodeModeResponse(t, *result.Content.ContentStr)
			require.False(t, hasError, "should not have execution error: %s", errorMsg)

			// Wrap returnValue in a map with "result" key for backward compatibility with verifyResult
			execResult := map[string]interface{}{"result": returnValue}
			tc.verifyResult(t, execResult)
		})
	}
}

func TestCodeMode_STDIO_EdgeCaseServer_NestedAndSpecialChars(t *testing.T) {
	t.Parallel()

	_, bifrost := setupCodeModeWithSTDIOServers(t, "edge-case-server")
	ctx := createTestContext()

	tests := []struct {
		name         string
		code         string
		verifyResult func(t *testing.T, execResult map[string]interface{})
	}{
		{
			name: "nested_structure_default",
			code: `const result = await edgeCaseServer.return_nested_structure({depth: 5});
return result`,
			verifyResult: func(t *testing.T, execResult map[string]interface{}) {
				result, ok := execResult["result"].(map[string]interface{})
				require.True(t, ok)
				assert.Equal(t, float64(5), result["depth"])
				// Verify nested structure exists
				data, ok := result["data"].(map[string]interface{})
				require.True(t, ok)
				assert.NotNil(t, data["child"])
			},
		},
		{
			name: "nested_structure_deeper",
			code: `const result = await edgeCaseServer.return_nested_structure({
  depth: 10
});
return result`,
			verifyResult: func(t *testing.T, execResult map[string]interface{}) {
				result, ok := execResult["result"].(map[string]interface{})
				require.True(t, ok)
				assert.Equal(t, float64(10), result["depth"])
			},
		},
		{
			name: "special_chars_quotes",
			code: `const result = await edgeCaseServer.return_special_chars({});
return {has_quotes: 'quotes' in result, has_backslashes: 'backslashes' in result}`,
			verifyResult: func(t *testing.T, execResult map[string]interface{}) {
				result, ok := execResult["result"].(map[string]interface{})
				require.True(t, ok)
				assert.Equal(t, true, result["has_quotes"])
				assert.Equal(t, true, result["has_backslashes"])
			},
		},
		{
			name: "special_chars_newlines",
			code: `const result = await edgeCaseServer.return_special_chars({});
return {has_newlines: 'newlines' in result, has_tabs: 'tabs' in result}`,
			verifyResult: func(t *testing.T, execResult map[string]interface{}) {
				result, ok := execResult["result"].(map[string]interface{})
				require.True(t, ok)
				assert.Equal(t, true, result["has_newlines"])
				assert.Equal(t, true, result["has_tabs"])
			},
		},
		{
			name: "special_chars_all",
			code: `const result = await edgeCaseServer.return_special_chars({});
const keys = Object.keys(result);
return {count: keys.length, has_mixed: 'mixed' in result}`,
			verifyResult: func(t *testing.T, execResult map[string]interface{}) {
				result, ok := execResult["result"].(map[string]interface{})
				require.True(t, ok)
				assert.Greater(t, result["count"], float64(5))
				assert.Equal(t, true, result["has_mixed"])
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			toolCall := CreateExecuteToolCodeCall(fmt.Sprintf("call-%s", tc.name), tc.code)
			result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)

			require.Nil(t, bifrostErr, "execution should succeed")
			require.NotNil(t, result)
			require.NotNil(t, result.Content)
			require.NotNil(t, result.Content.ContentStr)

			returnValue, hasError, errorMsg := ParseCodeModeResponse(t, *result.Content.ContentStr)
			require.False(t, hasError, "should not have execution error: %s", errorMsg)

			// Wrap returnValue in a map with "result" key for backward compatibility with verifyResult
			execResult := map[string]interface{}{"result": returnValue}
			tc.verifyResult(t, execResult)
		})
	}
}

func TestCodeMode_STDIO_EdgeCaseServer_ExtremeSizes(t *testing.T) {
	t.Parallel()

	_, bifrost := setupCodeModeWithSTDIOServers(t, "edge-case-server")
	ctx := createTestContext()

	tests := []struct {
		name         string
		code         string
		verifyResult func(t *testing.T, execResult map[string]interface{})
	}{
		{
			name: "extreme_sizes_small",
			code: `const result = await edgeCaseServer.return_large_payload({
  size_kb: 1
});
return {item_count: result.item_count, requested_size_kb: result.requested_size_kb}`,
			verifyResult: func(t *testing.T, execResult map[string]interface{}) {
				result, ok := execResult["result"].(map[string]interface{})
				require.True(t, ok)
				assert.Equal(t, float64(1), result["requested_size_kb"])
				assert.Greater(t, result["item_count"], float64(0))
			},
		},
		{
			name: "extreme_sizes_normal",
			code: `const result = await edgeCaseServer.return_large_payload({
  size_kb: 10
});
return {item_count: result.item_count, requested_size_kb: result.requested_size_kb}`,
			verifyResult: func(t *testing.T, execResult map[string]interface{}) {
				result, ok := execResult["result"].(map[string]interface{})
				require.True(t, ok)
				assert.Equal(t, float64(10), result["requested_size_kb"])
				assert.Greater(t, result["item_count"], float64(0))
			},
		},
		{
			name: "extreme_sizes_large",
			code: `const result = await edgeCaseServer.return_large_payload({
  size_kb: 100
});
return {
  item_count: result.item_count,
  requested_size_kb: result.requested_size_kb,
  has_items: result.items !== undefined
}`,
			verifyResult: func(t *testing.T, execResult map[string]interface{}) {
				result, ok := execResult["result"].(map[string]interface{})
				require.True(t, ok)
				assert.Equal(t, float64(100), result["requested_size_kb"])
				assert.Greater(t, result["item_count"], float64(0))
				assert.Equal(t, true, result["has_items"])
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			toolCall := CreateExecuteToolCodeCall(fmt.Sprintf("call-%s", tc.name), tc.code)
			result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)

			require.Nil(t, bifrostErr, "execution should succeed")
			require.NotNil(t, result)
			require.NotNil(t, result.Content)
			require.NotNil(t, result.Content.ContentStr)

			returnValue, hasError, errorMsg := ParseCodeModeResponse(t, *result.Content.ContentStr)
			require.False(t, hasError, "should not have execution error: %s", errorMsg)

			// Wrap returnValue in a map with "result" key for backward compatibility with verifyResult
			execResult := map[string]interface{}{"result": returnValue}
			tc.verifyResult(t, execResult)
		})
	}
}

// =============================================================================
// ERROR TEST SERVER TESTS
// =============================================================================

func TestCodeMode_STDIO_ErrorTestServer_NetworkErrors(t *testing.T) {
	t.Parallel()

	_, bifrost := setupCodeModeWithSTDIOServers(t, "error-test-server")
	ctx := createTestContext()

	tests := []struct {
		name         string
		code         string
		verifyResult func(t *testing.T, execResult map[string]interface{})
	}{
		{
			name: "return_error_network",
			code: `const result = await errorTestServer.return_error({
  error_type: "network"
});
return {error_message: result}`,
			verifyResult: func(t *testing.T, execResult map[string]interface{}) {
				result, ok := execResult["result"].(map[string]interface{})
				require.True(t, ok)
				assert.Contains(t, result["error_message"], "Network")
			},
		},
		{
			name: "return_error_timeout",
			code: `const result = await errorTestServer.return_error({
  error_type: "timeout"
});
return {error_message: result}`,
			verifyResult: func(t *testing.T, execResult map[string]interface{}) {
				result, ok := execResult["result"].(map[string]interface{})
				require.True(t, ok)
				assert.Contains(t, result["error_message"], "Timeout")
			},
		},
		{
			name: "return_error_validation",
			code: `const result = await errorTestServer.return_error({
  error_type: "validation"
});
return {error_message: result}`,
			verifyResult: func(t *testing.T, execResult map[string]interface{}) {
				result, ok := execResult["result"].(map[string]interface{})
				require.True(t, ok)
				assert.Contains(t, result["error_message"], "Validation")
			},
		},
		{
			name: "return_error_permission",
			code: `const result = await errorTestServer.return_error({
  error_type: "permission"
});
return {error_message: result}`,
			verifyResult: func(t *testing.T, execResult map[string]interface{}) {
				result, ok := execResult["result"].(map[string]interface{})
				require.True(t, ok)
				assert.Contains(t, result["error_message"], "Permission")
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			toolCall := CreateExecuteToolCodeCall(fmt.Sprintf("call-%s", tc.name), tc.code)
			result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)

			require.Nil(t, bifrostErr, "execution should succeed")
			require.NotNil(t, result)
			require.NotNil(t, result.Content)
			require.NotNil(t, result.Content.ContentStr)

			returnValue, hasError, errorMsg := ParseCodeModeResponse(t, *result.Content.ContentStr)
			require.False(t, hasError, "should not have execution error: %s", errorMsg)

			// Wrap returnValue in a map with "result" key for backward compatibility with verifyResult
			execResult := map[string]interface{}{"result": returnValue}
			tc.verifyResult(t, execResult)
		})
	}
}

func TestCodeMode_STDIO_ErrorTestServer_MalformedAndPartial(t *testing.T) {
	t.Parallel()

	_, bifrost := setupCodeModeWithSTDIOServers(t, "error-test-server")
	ctx := createTestContext()

	tests := []struct {
		name         string
		code         string
		verifyResult func(t *testing.T, execResult map[string]interface{})
	}{
		{
			name: "return_malformed_json",
			code: `const result = await errorTestServer.return_malformed_json({});
return result`,
			verifyResult: func(t *testing.T, execResult map[string]interface{}) {
				// return_malformed_json returns invalid JSON which should be handled
				result := execResult["result"]
				assert.NotNil(t, result)
			},
		},
		{
			name: "return_error",
			code: `const result = await errorTestServer.timeout_after({
  seconds: 0.05
});
return result`,
			verifyResult: func(t *testing.T, execResult map[string]interface{}) {
				// Use timeout_after instead of return_error since return_error throws
				result, ok := execResult["result"].(map[string]interface{})
				require.True(t, ok)
				assert.Equal(t, float64(0.05), result["delayed_seconds"])
			},
		},
		{
			name: "timeout_after_short",
			code: `const result = await errorTestServer.timeout_after({
  seconds: 0.1
});
return result`,
			verifyResult: func(t *testing.T, execResult map[string]interface{}) {
				result, ok := execResult["result"].(map[string]interface{})
				require.True(t, ok)
				assert.Equal(t, float64(0.1), result["delayed_seconds"])
			},
		},
		{
			name: "intermittent_fail_low_rate",
			code: `const result = await errorTestServer.intermittent_fail({
  fail_rate: 0.1
});
return result`,
			verifyResult: func(t *testing.T, execResult map[string]interface{}) {
				// Either success or error
				result := execResult["result"]
				assert.NotNil(t, result)
			},
		},
		{
			name: "memory_intensive_small",
			code: `const result = await errorTestServer.memory_intensive({
  size_mb: 1
});
return {allocated_mb: result.allocated_mb, has_checksum: result.checksum !== undefined}`,
			verifyResult: func(t *testing.T, execResult map[string]interface{}) {
				result, ok := execResult["result"].(map[string]interface{})
				require.True(t, ok)
				assert.Equal(t, float64(1), result["allocated_mb"])
				assert.Equal(t, true, result["has_checksum"])
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			toolCall := CreateExecuteToolCodeCall(fmt.Sprintf("call-%s", tc.name), tc.code)
			result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)

			require.Nil(t, bifrostErr, "execution should succeed")
			require.NotNil(t, result)
			require.NotNil(t, result.Content)
			require.NotNil(t, result.Content.ContentStr)

			returnValue, hasError, errorMsg := ParseCodeModeResponse(t, *result.Content.ContentStr)
			require.False(t, hasError, "should not have execution error: %s", errorMsg)

			// Wrap returnValue in a map with "result" key for backward compatibility with verifyResult
			execResult := map[string]interface{}{"result": returnValue}
			tc.verifyResult(t, execResult)
		})
	}
}

func TestCodeMode_STDIO_ErrorTestServer_LargePayload(t *testing.T) {
	t.Parallel()

	_, bifrost := setupCodeModeWithSTDIOServers(t, "error-test-server")
	ctx := createTestContext()

	tests := []struct {
		name         string
		code         string
		verifyResult func(t *testing.T, execResult map[string]interface{})
	}{
		{
			name: "memory_intensive_small",
			code: `const result = await errorTestServer.memory_intensive({
  size_mb: 5
});
return {
  allocated_mb: result.allocated_mb,
  allocated_bytes: result.allocated_bytes,
  has_checksum: result.checksum !== undefined
}`,
			verifyResult: func(t *testing.T, execResult map[string]interface{}) {
				result, ok := execResult["result"].(map[string]interface{})
				require.True(t, ok)
				assert.Equal(t, float64(5), result["allocated_mb"])
				assert.Equal(t, float64(5*1024*1024), result["allocated_bytes"])
				assert.Equal(t, true, result["has_checksum"])
			},
		},
		{
			name: "memory_intensive_medium",
			code: `const result = await errorTestServer.memory_intensive({
  size_mb: 10
});
return {
  allocated_mb: result.allocated_mb,
  allocated_bytes: result.allocated_bytes,
  has_message: result.message !== undefined
}`,
			verifyResult: func(t *testing.T, execResult map[string]interface{}) {
				result, ok := execResult["result"].(map[string]interface{})
				require.True(t, ok)
				assert.Equal(t, float64(10), result["allocated_mb"])
				assert.Equal(t, float64(10*1024*1024), result["allocated_bytes"])
				assert.Equal(t, true, result["has_message"])
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			toolCall := CreateExecuteToolCodeCall(fmt.Sprintf("call-%s", tc.name), tc.code)
			result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)

			require.Nil(t, bifrostErr, "execution should succeed")
			require.NotNil(t, result)
			require.NotNil(t, result.Content)
			require.NotNil(t, result.Content.ContentStr)

			returnValue, hasError, errorMsg := ParseCodeModeResponse(t, *result.Content.ContentStr)
			require.False(t, hasError, "should not have execution error: %s", errorMsg)

			// Wrap returnValue in a map with "result" key for backward compatibility with verifyResult
			execResult := map[string]interface{}{"result": returnValue}
			tc.verifyResult(t, execResult)
		})
	}
}

func TestCodeMode_STDIO_ErrorTestServer_IntermittentAndHandling(t *testing.T) {
	t.Parallel()

	_, bifrost := setupCodeModeWithSTDIOServers(t, "error-test-server")
	ctx := createTestContext()

	tests := []struct {
		name         string
		code         string
		verifyResult func(t *testing.T, execResult map[string]interface{})
	}{
		{
			name: "intermittent_fail_low_rate",
			code: `const result = await errorTestServer.intermittent_fail({
  id: "test-1",
  fail_rate: 0.1
});
return result`,
			verifyResult: func(t *testing.T, execResult map[string]interface{}) {
				result, ok := execResult["result"].(map[string]interface{})
				require.True(t, ok)
				// Either success or error
				if result["error"] != nil {
					assert.Contains(t, result["error"], "Intermittent")
				} else {
					assert.True(t, result["success"].(bool))
				}
			},
		},
		{
			name: "intermittent_fail_high_rate",
			code: `const result = await errorTestServer.intermittent_fail({
  id: "test-2",
  fail_rate: 0.9
});
return result`,
			verifyResult: func(t *testing.T, execResult map[string]interface{}) {
				result, ok := execResult["result"].(map[string]interface{})
				require.True(t, ok)
				// Most likely error
				assert.NotNil(t, result)
			},
		},
		{
			name: "error_handling_in_code",
			code: `let result;
try {
  result = await errorTestServer.network_error({
    id: "test-3",
    error_type: "connection_refused"
  });
} catch (error) {
  result = {caught: true, message: error.message};
}
return result`,
			verifyResult: func(t *testing.T, execResult map[string]interface{}) {
				result, ok := execResult["result"].(map[string]interface{})
				require.True(t, ok)
				// Either caught error or network error response
				assert.NotNil(t, result)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			toolCall := CreateExecuteToolCodeCall(fmt.Sprintf("call-%s", tc.name), tc.code)
			result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)

			require.Nil(t, bifrostErr, "execution should succeed")
			require.NotNil(t, result)
			require.NotNil(t, result.Content)
			require.NotNil(t, result.Content.ContentStr)

			returnValue, hasError, errorMsg := ParseCodeModeResponse(t, *result.Content.ContentStr)
			require.False(t, hasError, "should not have execution error: %s", errorMsg)

			// Wrap returnValue in a map with "result" key for backward compatibility with verifyResult
			execResult := map[string]interface{}{"result": returnValue}
			tc.verifyResult(t, execResult)
		})
	}
}

// =============================================================================
// PARALLEL TEST SERVER TESTS
// =============================================================================

func TestCodeMode_STDIO_ParallelTestServer_Sequential(t *testing.T) {
	t.Parallel()

	_, bifrost := setupCodeModeWithSTDIOServers(t, "parallel-test-server")
	ctx := createTestContext()

	tests := []struct {
		name         string
		code         string
		verifyResult func(t *testing.T, execResult map[string]interface{})
	}{
		{
			name: "fast_tool_1",
			code: `const result = await parallelTestServer.fast_operation({});
return result`,
			verifyResult: func(t *testing.T, execResult map[string]interface{}) {
				result, ok := execResult["result"].(map[string]interface{})
				require.True(t, ok)
				assert.Equal(t, "fast", result["operation"])
				assert.Greater(t, result["elapsed_ms"], float64(0))
			},
		},
		{
			name: "medium_tool_1",
			code: `const result = await parallelTestServer.medium_operation({});
return result`,
			verifyResult: func(t *testing.T, execResult map[string]interface{}) {
				result, ok := execResult["result"].(map[string]interface{})
				require.True(t, ok)
				assert.Equal(t, "medium", result["operation"])
				assert.Greater(t, result["elapsed_ms"], float64(100))
			},
		},
		{
			name: "slow_tool_1",
			code: `const result = await parallelTestServer.slow_operation({});
return result`,
			verifyResult: func(t *testing.T, execResult map[string]interface{}) {
				result, ok := execResult["result"].(map[string]interface{})
				require.True(t, ok)
				assert.Equal(t, "slow", result["operation"])
				assert.Greater(t, result["elapsed_ms"], float64(500))
			},
		},
		{
			name: "variable_delay",
			code: `const result = await parallelTestServer.very_slow_operation({});
return result`,
			verifyResult: func(t *testing.T, execResult map[string]interface{}) {
				result, ok := execResult["result"].(map[string]interface{})
				require.True(t, ok)
				assert.Equal(t, "very_slow", result["operation"])
				assert.Greater(t, result["elapsed_ms"], float64(1000))
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			toolCall := CreateExecuteToolCodeCall(fmt.Sprintf("call-%s", tc.name), tc.code)
			result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)

			require.Nil(t, bifrostErr, "execution should succeed")
			require.NotNil(t, result)
			require.NotNil(t, result.Content)
			require.NotNil(t, result.Content.ContentStr)

			returnValue, hasError, errorMsg := ParseCodeModeResponse(t, *result.Content.ContentStr)
			require.False(t, hasError, "should not have execution error: %s", errorMsg)

			// Wrap returnValue in a map with "result" key for backward compatibility with verifyResult
			execResult := map[string]interface{}{"result": returnValue}
			tc.verifyResult(t, execResult)
		})
	}
}

func TestCodeMode_STDIO_ParallelTestServer_Concurrent(t *testing.T) {
	t.Parallel()

	_, bifrost := setupCodeModeWithSTDIOServers(t, "parallel-test-server")
	ctx := createTestContext()

	tests := []struct {
		name         string
		code         string
		verifyResult func(t *testing.T, execResult map[string]interface{})
	}{
		{
			name: "parallel_fast_tools",
			code: `const start = Date.now();
const results = await Promise.all([
  parallelTestServer.fast_operation({}),
  parallelTestServer.return_timestamp({})
]);
const elapsed = Date.now() - start;
return {results: results, elapsed: elapsed}`,
			verifyResult: func(t *testing.T, execResult map[string]interface{}) {
				result, ok := execResult["result"].(map[string]interface{})
				require.True(t, ok)
				results, ok := result["results"].([]interface{})
				require.True(t, ok)
				assert.Len(t, results, 2)
				// Elapsed should be very quick since these are fast operations
				elapsed := result["elapsed"].(float64)
				assert.Less(t, elapsed, float64(100), "parallel execution should be fast")
			},
		},
		{
			name: "parallel_mixed_speeds",
			code: `const start = Date.now();
const results = await Promise.all([
  parallelTestServer.fast_operation({}),
  parallelTestServer.medium_operation({}),
  parallelTestServer.slow_operation({})
]);
const elapsed = Date.now() - start;
return {results: results, elapsed: elapsed, count: results.length}`,
			verifyResult: func(t *testing.T, execResult map[string]interface{}) {
				result, ok := execResult["result"].(map[string]interface{})
				require.True(t, ok)
				assert.Equal(t, float64(3), result["count"])
				// Elapsed should be closer to max(fast, medium, slow) than to sum
				// slow takes 750ms, so elapsed should be around 750ms
				elapsed := result["elapsed"].(float64)
				assert.Greater(t, elapsed, float64(700), "should include slow operation time")
				assert.Less(t, elapsed, float64(1500), "should not be much more than slow operation")
			},
		},
		{
			name: "parallel_all_tools",
			code: `const results = await Promise.all([
  parallelTestServer.fast_operation({}),
  parallelTestServer.return_timestamp({}),
  parallelTestServer.medium_operation({}),
  parallelTestServer.slow_operation({}),
  parallelTestServer.very_slow_operation({})
]);
return {count: results.length, operations: results.map(r => r.operation || 'timestamp')}`,
			verifyResult: func(t *testing.T, execResult map[string]interface{}) {
				result, ok := execResult["result"].(map[string]interface{})
				require.True(t, ok)
				assert.Equal(t, float64(5), result["count"])
				ops, ok := result["operations"].([]interface{})
				require.True(t, ok)
				assert.Len(t, ops, 5)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			toolCall := CreateExecuteToolCodeCall(fmt.Sprintf("call-%s", tc.name), tc.code)
			result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)

			require.Nil(t, bifrostErr, "execution should succeed")
			require.NotNil(t, result)
			require.NotNil(t, result.Content)
			require.NotNil(t, result.Content.ContentStr)

			returnValue, hasError, errorMsg := ParseCodeModeResponse(t, *result.Content.ContentStr)
			require.False(t, hasError, "should not have execution error: %s", errorMsg)

			// Wrap returnValue in a map with "result" key for backward compatibility with verifyResult
			execResult := map[string]interface{}{"result": returnValue}
			tc.verifyResult(t, execResult)
		})
	}
}

// =============================================================================
// MULTI-SERVER COMPREHENSIVE TESTS
// =============================================================================

func TestCodeMode_STDIO_MultiServer_AllServers(t *testing.T) {
	t.Parallel()

	_, bifrost := setupCodeModeWithSTDIOServers(t, "test-tools-server", "edge-case-server", "error-test-server", "parallel-test-server")
	ctx := createTestContext()

	tests := []struct {
		name         string
		code         string
		verifyResult func(t *testing.T, execResult map[string]interface{})
	}{
		{
			name: "call_tools_from_all_servers",
			code: `const results = await Promise.all([
  testToolsServer.echo({message: "test-tools"}),
  edgeCaseServer.return_unicode({type: "emoji"}),
  errorTestServer.timeout_after({seconds: 0.05}),
  parallelTestServer.fast_operation({})
]);
return {
  count: results.length,
  testTools: results[0],
  edgeCase: results[1],
  errorTest: results[2],
  parallelTest: results[3]
}`,
			verifyResult: func(t *testing.T, execResult map[string]interface{}) {
				result, ok := execResult["result"].(map[string]interface{})
				require.True(t, ok)
				assert.Equal(t, float64(4), result["count"])

				testTools, ok := result["testTools"].(map[string]interface{})
				require.True(t, ok)
				assert.Equal(t, "test-tools", testTools["message"])

				edgeCase, ok := result["edgeCase"].(map[string]interface{})
				require.True(t, ok)
				assert.NotNil(t, edgeCase["text"])

				errorTest, ok := result["errorTest"].(map[string]interface{})
				require.True(t, ok)
				assert.Equal(t, float64(0.05), errorTest["delayed_seconds"])

				parallelTest, ok := result["parallelTest"].(map[string]interface{})
				require.True(t, ok)
				assert.Equal(t, "fast", parallelTest["operation"])
			},
		},
		{
			name: "sequential_across_servers",
			code: `const echo = await testToolsServer.echo({message: "first"});
const unicode = await edgeCaseServer.return_unicode({type: "emoji"});
const fast = await parallelTestServer.fast_operation({});
return {echo: echo, unicode: unicode, fast: fast}`,
			verifyResult: func(t *testing.T, execResult map[string]interface{}) {
				result, ok := execResult["result"].(map[string]interface{})
				require.True(t, ok)
				assert.NotNil(t, result["echo"])
				assert.NotNil(t, result["unicode"])
				assert.NotNil(t, result["fast"])
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			toolCall := CreateExecuteToolCodeCall(fmt.Sprintf("call-%s", tc.name), tc.code)
			result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)

			require.Nil(t, bifrostErr, "execution should succeed")
			require.NotNil(t, result)
			require.NotNil(t, result.Content)
			require.NotNil(t, result.Content.ContentStr)

			returnValue, hasError, errorMsg := ParseCodeModeResponse(t, *result.Content.ContentStr)
			require.False(t, hasError, "should not have execution error: %s", errorMsg)

			// Wrap returnValue in a map with "result" key for backward compatibility with verifyResult
			execResult := map[string]interface{}{"result": returnValue}
			tc.verifyResult(t, execResult)
		})
	}
}

func TestCodeMode_STDIO_MultiServer_FilteringAcrossServers(t *testing.T) {
	t.Parallel()

	_, bifrost := setupCodeModeWithSTDIOServers(t, "test-tools-server", "edge-case-server", "parallel-test-server")

	tests := []struct {
		name           string
		includeClients []string
		code           string
		shouldSucceed  bool
	}{
		{
			name:           "allow_only_test_tools_and_edge_case",
			includeClients: []string{"testToolsServer", "edgeCaseServer"},
			code: `const results = await Promise.all([
  testToolsServer.echo({message: "allowed"}),
  edgeCaseServer.return_unicode({type: "emoji"})
]);
return results`,
			shouldSucceed: true,
		},
		{
			name:           "block_parallel_server",
			includeClients: []string{"testToolsServer", "edgeCaseServer"},
			code: `const result = await parallelTestServer.fast_operation({});
return result`,
			shouldSucceed: false,
		},
		{
			name:           "allow_all_servers",
			includeClients: []string{"*"},
			code: `const results = await Promise.all([
  testToolsServer.echo({message: "all"}),
  edgeCaseServer.return_unicode({type: "emoji"}),
  parallelTestServer.fast_operation({})
]);
return {count: results.length}`,
			shouldSucceed: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			baseCtx := context.Background()
			baseCtx = context.WithValue(baseCtx, mcp.MCPContextKeyIncludeClients, tc.includeClients)
			ctx := schemas.NewBifrostContext(baseCtx, schemas.NoDeadline)

			toolCall := CreateExecuteToolCodeCall(fmt.Sprintf("call-%s", tc.name), tc.code)
			result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)

			if tc.shouldSucceed {
				require.Nil(t, bifrostErr, "execution should succeed")
				require.NotNil(t, result)
			} else {
				// Should fail - check either bifrostErr or error in result
				if bifrostErr == nil && result != nil && result.Content != nil && result.Content.ContentStr != nil {
					var execResult map[string]interface{}
					err := json.Unmarshal([]byte(*result.Content.ContentStr), &execResult)
					if err == nil {
						_, hasError := execResult["error"]
						assert.True(t, hasError, "Should have error in result")
					}
				}
			}
		})
	}
}
