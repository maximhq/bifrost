package mcptests

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/mcp"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var initMCPServerPathsOnce sync.Once

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

	// Initialize MCP server paths (guarded against concurrent execution)
	initMCPServerPathsOnce.Do(func() {
		InitMCPServerPaths(t)
	})

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
			code:           `result = 42`,
			expectedResult: float64(42),
		},
		{
			name:           "string_return",
			code:           `result = "Hello from test-tools-server"`,
			expectedResult: "Hello from test-tools-server",
		},
		{
			name:           "object_return",
			code:           `result = {"status": "success", "value": 123}`,
			expectedResult: map[string]interface{}{"status": "success", "value": float64(123)},
		},
		{
			name:           "array_return",
			code:           `result = [1, 2, 3, 4, 5]`,
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
			code: `result = testToolsServer.echo(message="test message")`,
			verifyResult: func(t *testing.T, execResult map[string]interface{}) {
				result, ok := execResult["result"].(map[string]interface{})
				require.True(t, ok, "result should be an object")
				assert.Equal(t, "test message", result["message"])
			},
		},
		{
			name: "calculator_add",
			code: `result = testToolsServer.calculator(operation="add", x=15, y=27)`,
			verifyResult: func(t *testing.T, execResult map[string]interface{}) {
				result, ok := execResult["result"].(map[string]interface{})
				require.True(t, ok, "result should be an object")
				assert.Equal(t, float64(42), result["result"])
			},
		},
		{
			name: "calculator_multiply",
			code: `result = testToolsServer.calculator(operation="multiply", x=6, y=7)`,
			verifyResult: func(t *testing.T, execResult map[string]interface{}) {
				result, ok := execResult["result"].(map[string]interface{})
				require.True(t, ok, "result should be an object")
				assert.Equal(t, float64(42), result["result"])
			},
		},
		{
			name: "get_weather",
			code: `result = testToolsServer.get_weather(location="San Francisco", units="celsius")`,
			verifyResult: func(t *testing.T, execResult map[string]interface{}) {
				result, ok := execResult["result"].(map[string]interface{})
				require.True(t, ok, "result should be an object")
				assert.Equal(t, "San Francisco", result["location"])
				assert.Equal(t, "celsius", result["units"])
			},
		},
		{
			name: "sequential_tool_calls",
			code: `echo1 = testToolsServer.echo(message="first")
echo2 = testToolsServer.echo(message="second")
result = {"first": echo1, "second": echo2}`,
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
			code: `result = testToolsServer.echo(message="from test-tools")`,
			verifyResult: func(t *testing.T, execResult map[string]interface{}) {
				result, ok := execResult["result"].(map[string]interface{})
				require.True(t, ok)
				assert.Equal(t, "from test-tools", result["message"])
			},
		},
		{
			name: "call_tool_from_second_server",
			code: `result = temperature.get_temperature(location="Tokyo")`,
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
			code: `echo = testToolsServer.echo(message="hello")
temp = temperature.get_temperature(location="London")
result = {"echo": echo, "temp": temp}`,
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
			code: `calc1 = testToolsServer.calculator(operation="add", x=10, y=5)
calc2 = temperature.calculator(operation="multiply", x=3, y=4)
result = {"tools": calc1, "temp": calc2}`,
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
			code:           `result = testToolsServer.echo(message="allowed")`,
			shouldSucceed:    true,
			expectedInResult: "allowed",
		},
		{
			name:           "block_test_tools_server",
			includeClients: []string{"temperature"},
			code:           `result = testToolsServer.echo(message="blocked")`,
			shouldSucceed: false,
			expectedError: "undefined: testToolsServer",
		},
		{
			name:           "allow_only_temperature_server",
			includeClients: []string{"temperature"},
			code:           `result = temperature.get_temperature(location="Paris")`,
			shouldSucceed:    true,
			expectedInResult: "Paris",
		},
		{
			name:           "block_temperature_server",
			includeClients: []string{"testToolsServer"},
			code:           `result = temperature.get_temperature(location="blocked")`,
			shouldSucceed: false,
			expectedError: "undefined: temperature",
		},
		{
			name:           "allow_both_servers",
			includeClients: []string{"testToolsServer", "temperature"},
			code: `echo = testToolsServer.echo(message="both")
temp = temperature.get_temperature(location="NYC")
result = {"echo": echo, "temp": temp}`,
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
				errorFound := false
				if bifrostErr != nil {
					assert.Contains(t, bifrostErr.Error.Message, tc.expectedError)
					errorFound = true
				} else if result != nil && result.Content != nil && result.Content.ContentStr != nil {
					_, hasError, errorMsg := ParseCodeModeResponse(t, *result.Content.ContentStr)
					if hasError {
						assert.Contains(t, errorMsg, tc.expectedError)
						errorFound = true
					} else {
						// Check if return value contains error
						returnValue, _, _ := ParseCodeModeResponse(t, *result.Content.ContentStr)
						if returnValue != nil {
							if returnObj, ok := returnValue.(map[string]interface{}); ok {
								if errorField, ok := returnObj["error"]; ok {
									errorStr := fmt.Sprintf("%v", errorField)
									assert.Contains(t, errorStr, tc.expectedError)
									errorFound = true
								}
							}
						}
					}
				}
				if !errorFound {
					t.Errorf("expected error containing %q for blocked tool, but no error was observed", tc.expectedError)
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
			name:             "allow_only_echo",
			includeTools:     []string{"testToolsServer-echo"},
			code:             `result = testToolsServer.echo(message="allowed")`,
			shouldSucceed:    true,
			expectedInResult: "allowed",
		},
		{
			name:          "block_calculator_allow_echo",
			includeTools:  []string{"testToolsServer-echo"},
			code:          `result = testToolsServer.calculator(operation="add", x=1, y=2)`,
			shouldSucceed: false,
			expectedError: "calculator",
		},
		{
			name:         "wildcard_for_client",
			includeTools: []string{"testToolsServer-*"},
			code: `echo = testToolsServer.echo(message="test")
calc = testToolsServer.calculator(operation="add", x=5, y=3)
result = {"echo": echo, "calc": calc}`,
			shouldSucceed:    true,
			expectedInResult: "test",
		},
		{
			name:         "allow_multiple_specific_tools",
			includeTools: []string{"testToolsServer-echo", "testToolsServer-calculator"},
			code: `echo = testToolsServer.echo(message="multi")
calc = testToolsServer.calculator(operation="multiply", x=6, y=7)
result = {"echo": echo, "calc": calc}`,
			shouldSucceed:    true,
			expectedInResult: "multi",
		},
		{
			name:          "block_all_tools_empty_filter",
			includeTools:  []string{},
			code:          `result = testToolsServer.echo(message="blocked")`,
			shouldSucceed: false,
			expectedError: "undefined: testToolsServer",
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
			name:             "allow_server_and_specific_tool",
			includeClients:   []string{"testToolsServer"},
			includeTools:     []string{"testToolsServer-echo"},
			code:             `result = testToolsServer.echo(message="filtered")`,
			shouldSucceed:    true,
			expectedInResult: "filtered",
		},
		{
			name:           "allow_server_but_block_tool",
			includeClients: []string{"testToolsServer"},
			includeTools:   []string{"testToolsServer-calculator"},
			code:           `result = testToolsServer.echo(message="blocked")`,
			shouldSucceed:  false,
		},
		{
			name:           "allow_all_clients_specific_tools_from_each",
			includeClients: []string{"*"},
			includeTools:   []string{"testToolsServer-echo", "temperature-get_temperature"},
			code: `echo = testToolsServer.echo(message="test")
temp = temperature.get_temperature(location="Berlin")
result = {"echo": echo, "temp": temp}`,
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
			code: `def main():
    results = []
    for i in range(3):
        r = testToolsServer.echo(message="count_" + str(i))
        results.append(r)
    return results
result = main()`,
			verifyResult: func(t *testing.T, execResult map[string]interface{}) {
				results, ok := execResult["result"].([]interface{})
				require.True(t, ok, "result should be array")
				assert.Len(t, results, 3)
			},
		},
		{
			name: "conditional_tool_calls",
			code: `def main():
    x = 10
    if x > 5:
        return testToolsServer.calculator(operation="add", x=x, y=5)
    else:
        return testToolsServer.echo(message="small")
result = main()`,
			verifyResult: func(t *testing.T, execResult map[string]interface{}) {
				result, ok := execResult["result"].(map[string]interface{})
				require.True(t, ok)
				assert.Equal(t, float64(15), result["result"])
			},
		},
		{
			name: "sequential_tool_calls_list",
			code: `r1 = testToolsServer.echo(message="one")
r2 = testToolsServer.echo(message="two")
r3 = testToolsServer.echo(message="three")
result = [r1, r2, r3]`,
			verifyResult: func(t *testing.T, execResult map[string]interface{}) {
				results, ok := execResult["result"].([]interface{})
				require.True(t, ok)
				assert.Len(t, results, 3)
			},
		},
		{
			name: "data_transformation",
			code: `calc1 = testToolsServer.calculator(operation="add", x=10, y=20)
calc2 = testToolsServer.calculator(operation="multiply", x=5, y=3)
result = {
    "sum": calc1["result"],
    "product": calc2["result"],
    "total": calc1["result"] + calc2["result"]
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
			code: `result = edgeCaseServer.return_unicode(type="emoji")`,
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
			code: `r = edgeCaseServer.return_unicode(type="emoji")
result = {"type": r["type"], "length": r["length"], "starts_with_hello": r["text"].startswith("Hello")}`,
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
			code: `result = edgeCaseServer.return_binary(size=100, encoding="base64")`,
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
			code: `result = edgeCaseServer.return_binary(size=50, encoding="hex")`,
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
			code: `r = edgeCaseServer.return_binary(size=10, encoding="base64")
result = {"size": r["size"], "encoding": r["encoding"], "data_length": len(r["data"])}`,
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
			code: `r = edgeCaseServer.return_null()
result = {"empty_string": r["empty_string"], "empty_array": r["empty_array"]}`,
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
			code: `r = edgeCaseServer.return_null()
result = {"empty_object": r["empty_object"], "has_property": "empty_object" in r}`,
			verifyResult: func(t *testing.T, execResult map[string]interface{}) {
				result, ok := execResult["result"].(map[string]interface{})
				require.True(t, ok)
				assert.Equal(t, true, result["has_property"])
			},
		},
		{
			name: "null_null_value",
			code: `r = edgeCaseServer.return_null()
result = {"has_null": r["null_value"] == None, "zero": r["zero"], "false": r["false"]}`,
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
			code: `r = edgeCaseServer.return_null()
keys = list(r.keys())
result = {"key_count": len(keys), "has_empty_string": "empty_string" in r}`,
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
			code: `result = edgeCaseServer.return_nested_structure(depth=5)`,
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
			code: `result = edgeCaseServer.return_nested_structure(depth=10)`,
			verifyResult: func(t *testing.T, execResult map[string]interface{}) {
				result, ok := execResult["result"].(map[string]interface{})
				require.True(t, ok)
				assert.Equal(t, float64(10), result["depth"])
			},
		},
		{
			name: "special_chars_quotes",
			code: `r = edgeCaseServer.return_special_chars()
result = {"has_quotes": "quotes" in r, "has_backslashes": "backslashes" in r}`,
			verifyResult: func(t *testing.T, execResult map[string]interface{}) {
				result, ok := execResult["result"].(map[string]interface{})
				require.True(t, ok)
				assert.Equal(t, true, result["has_quotes"])
				assert.Equal(t, true, result["has_backslashes"])
			},
		},
		{
			name: "special_chars_newlines",
			code: `r = edgeCaseServer.return_special_chars()
result = {"has_newlines": "newlines" in r, "has_tabs": "tabs" in r}`,
			verifyResult: func(t *testing.T, execResult map[string]interface{}) {
				result, ok := execResult["result"].(map[string]interface{})
				require.True(t, ok)
				assert.Equal(t, true, result["has_newlines"])
				assert.Equal(t, true, result["has_tabs"])
			},
		},
		{
			name: "special_chars_all",
			code: `r = edgeCaseServer.return_special_chars()
keys = list(r.keys())
result = {"count": len(keys), "has_mixed": "mixed" in r}`,
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
			code: `r = edgeCaseServer.return_large_payload(size_kb=1)
result = {"item_count": r["item_count"], "requested_size_kb": r["requested_size_kb"]}`,
			verifyResult: func(t *testing.T, execResult map[string]interface{}) {
				result, ok := execResult["result"].(map[string]interface{})
				require.True(t, ok)
				assert.Equal(t, float64(1), result["requested_size_kb"])
				assert.Greater(t, result["item_count"], float64(0))
			},
		},
		{
			name: "extreme_sizes_normal",
			code: `r = edgeCaseServer.return_large_payload(size_kb=10)
result = {"item_count": r["item_count"], "requested_size_kb": r["requested_size_kb"]}`,
			verifyResult: func(t *testing.T, execResult map[string]interface{}) {
				result, ok := execResult["result"].(map[string]interface{})
				require.True(t, ok)
				assert.Equal(t, float64(10), result["requested_size_kb"])
				assert.Greater(t, result["item_count"], float64(0))
			},
		},
		{
			name: "extreme_sizes_large",
			code: `r = edgeCaseServer.return_large_payload(size_kb=100)
result = {
    "item_count": r["item_count"],
    "requested_size_kb": r["requested_size_kb"],
    "has_items": "items" in r
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
			code: `r = errorTestServer.return_error(error_type="network")
result = {"error_message": r}`,
			verifyResult: func(t *testing.T, execResult map[string]interface{}) {
				result, ok := execResult["result"].(map[string]interface{})
				require.True(t, ok)
				assert.Contains(t, result["error_message"], "Network")
			},
		},
		{
			name: "return_error_timeout",
			code: `r = errorTestServer.return_error(error_type="timeout")
result = {"error_message": r}`,
			verifyResult: func(t *testing.T, execResult map[string]interface{}) {
				result, ok := execResult["result"].(map[string]interface{})
				require.True(t, ok)
				assert.Contains(t, result["error_message"], "Timeout")
			},
		},
		{
			name: "return_error_validation",
			code: `r = errorTestServer.return_error(error_type="validation")
result = {"error_message": r}`,
			verifyResult: func(t *testing.T, execResult map[string]interface{}) {
				result, ok := execResult["result"].(map[string]interface{})
				require.True(t, ok)
				assert.Contains(t, result["error_message"], "Validation")
			},
		},
		{
			name: "return_error_permission",
			code: `r = errorTestServer.return_error(error_type="permission")
result = {"error_message": r}`,
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
			code: `result = errorTestServer.return_malformed_json()`,
			verifyResult: func(t *testing.T, execResult map[string]interface{}) {
				// return_malformed_json returns invalid JSON which should be handled
				result := execResult["result"]
				assert.NotNil(t, result)
			},
		},
		{
			name: "return_error",
			code: `result = errorTestServer.timeout_after(seconds=0.05)`,
			verifyResult: func(t *testing.T, execResult map[string]interface{}) {
				// Use timeout_after instead of return_error since return_error throws
				result, ok := execResult["result"].(map[string]interface{})
				require.True(t, ok)
				assert.Equal(t, float64(0.05), result["delayed_seconds"])
			},
		},
		{
			name: "timeout_after_short",
			code: `result = errorTestServer.timeout_after(seconds=0.1)`,
			verifyResult: func(t *testing.T, execResult map[string]interface{}) {
				result, ok := execResult["result"].(map[string]interface{})
				require.True(t, ok)
				assert.Equal(t, float64(0.1), result["delayed_seconds"])
			},
		},
		{
			name: "intermittent_fail_low_rate",
			code: `result = errorTestServer.intermittent_fail(fail_rate=0.1)`,
			verifyResult: func(t *testing.T, execResult map[string]interface{}) {
				// Either success or error
				result := execResult["result"]
				assert.NotNil(t, result)
			},
		},
		{
			name: "memory_intensive_small",
			code: `r = errorTestServer.memory_intensive(size_mb=1)
result = {"allocated_mb": r["allocated_mb"], "has_checksum": "checksum" in r}`,
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
			code: `r = errorTestServer.memory_intensive(size_mb=5)
result = {
    "allocated_mb": r["allocated_mb"],
    "allocated_bytes": r["allocated_bytes"],
    "has_checksum": "checksum" in r
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
			code: `r = errorTestServer.memory_intensive(size_mb=10)
result = {
    "allocated_mb": r["allocated_mb"],
    "allocated_bytes": r["allocated_bytes"],
    "has_message": "message" in r
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
			code: `result = errorTestServer.intermittent_fail(id="test-1", fail_rate=0.1)`,
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
			code: `result = errorTestServer.intermittent_fail(id="test-2", fail_rate=0.9)`,
			verifyResult: func(t *testing.T, execResult map[string]interface{}) {
				result, ok := execResult["result"].(map[string]interface{})
				require.True(t, ok)
				// Most likely error
				assert.NotNil(t, result)
			},
		},
		{
			name: "error_handling_in_code",
			code: `result = errorTestServer.return_error(error_type="network")`,
			verifyResult: func(t *testing.T, execResult map[string]interface{}) {
				// Either error message or error response
				result := execResult["result"]
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
			code: `result = parallelTestServer.fast_operation()`,
			verifyResult: func(t *testing.T, execResult map[string]interface{}) {
				result, ok := execResult["result"].(map[string]interface{})
				require.True(t, ok)
				assert.Equal(t, "fast", result["operation"])
				assert.Greater(t, result["elapsed_ms"], float64(0))
			},
		},
		{
			name: "medium_tool_1",
			code: `result = parallelTestServer.medium_operation()`,
			verifyResult: func(t *testing.T, execResult map[string]interface{}) {
				result, ok := execResult["result"].(map[string]interface{})
				require.True(t, ok)
				assert.Equal(t, "medium", result["operation"])
				assert.Greater(t, result["elapsed_ms"], float64(100))
			},
		},
		{
			name: "slow_tool_1",
			code: `result = parallelTestServer.slow_operation()`,
			verifyResult: func(t *testing.T, execResult map[string]interface{}) {
				result, ok := execResult["result"].(map[string]interface{})
				require.True(t, ok)
				assert.Equal(t, "slow", result["operation"])
				assert.Greater(t, result["elapsed_ms"], float64(500))
			},
		},
		{
			name: "variable_delay",
			code: `result = parallelTestServer.very_slow_operation()`,
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
			code: `r1 = parallelTestServer.fast_operation()
r2 = parallelTestServer.return_timestamp()
result = {"results": [r1, r2], "count": 2}`,
			verifyResult: func(t *testing.T, execResult map[string]interface{}) {
				result, ok := execResult["result"].(map[string]interface{})
				require.True(t, ok)
				results, ok := result["results"].([]interface{})
				require.True(t, ok)
				assert.Len(t, results, 2)
			},
		},
		{
			name: "parallel_mixed_speeds",
			code: `r1 = parallelTestServer.fast_operation()
r2 = parallelTestServer.medium_operation()
r3 = parallelTestServer.slow_operation()
result = {"results": [r1, r2, r3], "count": 3}`,
			verifyResult: func(t *testing.T, execResult map[string]interface{}) {
				result, ok := execResult["result"].(map[string]interface{})
				require.True(t, ok)
				assert.Equal(t, float64(3), result["count"])
			},
		},
		{
			name: "parallel_all_tools",
			code: `r1 = parallelTestServer.fast_operation()
r2 = parallelTestServer.return_timestamp()
r3 = parallelTestServer.medium_operation()
r4 = parallelTestServer.slow_operation()
r5 = parallelTestServer.very_slow_operation()
def get_op(r):
    if "operation" in r:
        return r["operation"]
    return "timestamp"
result = {"count": 5, "operations": [get_op(r1), get_op(r2), get_op(r3), get_op(r4), get_op(r5)]}`,
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

	_, bifrost := setupCodeModeWithSTDIOServers(t, "go-test-server", "edge-case-server", "error-test-server", "parallel-test-server")
	ctx := createTestContext()

	tests := []struct {
		name         string
		code         string
		verifyResult func(t *testing.T, execResult map[string]interface{})
	}{
		{
			name: "call_tools_from_all_servers",
			code: `r1 = goTestServer.string_transform(input="test-tools", operation="uppercase")
r2 = edgeCaseServer.return_unicode(type="emoji")
r3 = errorTestServer.timeout_after(seconds=0.05)
r4 = parallelTestServer.fast_operation()
result = {
    "count": 4,
    "goTest": r1,
    "edgeCase": r2,
    "errorTest": r3,
    "parallelTest": r4
}`,
			verifyResult: func(t *testing.T, execResult map[string]interface{}) {
				result, ok := execResult["result"].(map[string]interface{})
				require.True(t, ok)
				assert.Equal(t, float64(4), result["count"])

				goTest, ok := result["goTest"].(map[string]interface{})
				require.True(t, ok)
				assert.Equal(t, "TEST-TOOLS", goTest["result"])

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
			code: `transform = goTestServer.string_transform(input="first", operation="uppercase")
unicode = edgeCaseServer.return_unicode(type="emoji")
fast = parallelTestServer.fast_operation()
result = {"transform": transform, "unicode": unicode, "fast": fast}`,
			verifyResult: func(t *testing.T, execResult map[string]interface{}) {
				result, ok := execResult["result"].(map[string]interface{})
				require.True(t, ok)
				assert.NotNil(t, result["transform"])
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

	_, bifrost := setupCodeModeWithSTDIOServers(t, "go-test-server", "edge-case-server", "parallel-test-server")

	tests := []struct {
		name           string
		includeClients []string
		code           string
		shouldSucceed  bool
	}{
		{
			name:           "allow_only_go_test_and_edge_case",
			includeClients: []string{"goTestServer", "edgeCaseServer"},
			code: `r1 = goTestServer.string_transform(input="allowed", operation="uppercase")
r2 = edgeCaseServer.return_unicode(type="emoji")
result = [r1, r2]`,
			shouldSucceed: true,
		},
		{
			name:           "block_parallel_server",
			includeClients: []string{"goTestServer", "edgeCaseServer"},
			code:           `result = parallelTestServer.fast_operation()`,
			shouldSucceed:  false,
		},
		{
			name:           "allow_all_servers",
			includeClients: []string{"*"},
			code: `r1 = goTestServer.string_transform(input="all", operation="uppercase")
r2 = edgeCaseServer.return_unicode(type="emoji")
r3 = parallelTestServer.fast_operation()
result = {"count": 3}`,
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
