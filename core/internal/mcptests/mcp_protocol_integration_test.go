package mcptests

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// MCP PROTOCOL INTEGRATION TESTS
// =============================================================================
//
// These tests use REAL MCP STDIO servers (Node.js-based) to test the actual
// MCP protocol end-to-end, including JSON-RPC communication, serialization,
// and protocol-level error handling.
//
// The test servers are located in: examples/mcps/
// - test-tools-server: Basic tools (echo, calculator, weather, delay, throw_error)
// - parallel-test-server: Tools with different delays for parallel testing
// - error-test-server: Tools that simulate various error scenarios
// - edge-case-server: Tools for edge case testing
//
// KNOWN ISSUE: Currently skipped due to "broken pipe" errors when communicating
// between mark3labs/mcp-go (Go client) and @modelcontextprotocol/sdk (Node.js servers).
// The servers connect successfully and tools are discovered, but tool execution fails.
// This appears to be a protocol compatibility issue that needs to be resolved.
//
// In the meantime, 95% of tests use InProcess tools which provide excellent coverage.
// =============================================================================

// TestProtocol_BasicToolExecution tests basic tool execution with real STDIO server
func TestProtocol_BasicToolExecution(t *testing.T) {
	t.Skip("Skipping due to mark3labs/mcp-go <-> @modelcontextprotocol/sdk compatibility issue (broken pipe)")
	t.Parallel()

	// Get path to test-tools-server
	serverPath := getTestToolsServerPath(t)

	// Create STDIO client config
	clientConfig := GetSampleSTDIOClientConfig("node", []string{serverPath})
	clientConfig.ID = "test-tools-client"
	clientConfig.Name = "TestToolsServer"

	// Create MCP manager with STDIO client
	manager := setupMCPManager(t, clientConfig)

	// Wait for connection to establish (STDIO servers need time to start)
	time.Sleep(5 * time.Second)

	// Verify client is connected
	clients := manager.GetClients()
	require.Len(t, clients, 1, "Should have one client")
	assert.Equal(t, schemas.MCPConnectionStateConnected, clients[0].State, "Client should be connected")

	// Get available tools
	ctx := createTestContext()
	tools := manager.GetAvailableTools(ctx)
	require.Greater(t, len(tools), 0, "Should have tools available")

	// Verify test-tools are available (tool names are prefixed with client name)
	toolNames := make([]string, 0, len(tools))
	for _, tool := range tools {
		if tool.Function != nil {
			toolNames = append(toolNames, tool.Function.Name)
		}
	}
	assert.Contains(t, toolNames, "TestToolsServer-echo", "Should have echo tool")
	assert.Contains(t, toolNames, "TestToolsServer-calculator", "Should have calculator tool")

	// Setup Bifrost instance
	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	// Test echo tool (use prefixed name)
	toolCall := schemas.ChatAssistantMessageToolCall{
		ID:   schemas.Ptr("call_echo_001"),
		Type: schemas.Ptr("function"),
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name:      schemas.Ptr("TestToolsServer-echo"),
			Arguments: `{"message": "Hello from protocol test"}`,
		},
	}

	result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)
	require.Nil(t, bifrostErr, "Echo tool should succeed")
	require.NotNil(t, result, "Should have result")
	assert.Equal(t, schemas.ChatMessageRoleTool, result.Role)

	// Verify response
	if result.Content != nil && result.Content.ContentStr != nil {
		var echoResponse map[string]interface{}
		err := json.Unmarshal([]byte(*result.Content.ContentStr), &echoResponse)
		require.NoError(t, err, "Should parse JSON response")
		assert.Equal(t, "Hello from protocol test", echoResponse["message"])
	}

	// Test calculator tool (use prefixed name)
	toolCall = schemas.ChatAssistantMessageToolCall{
		ID:   schemas.Ptr("call_calc_001"),
		Type: schemas.Ptr("function"),
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name:      schemas.Ptr("TestToolsServer-calculator"),
			Arguments: `{"operation": "add", "x": 42, "y": 58}`,
		},
	}

	result, bifrostErr = bifrost.ExecuteChatMCPTool(ctx, &toolCall)
	require.Nil(t, bifrostErr, "Calculator tool should succeed")
	require.NotNil(t, result, "Should have result")

	// Verify calculation
	if result.Content != nil && result.Content.ContentStr != nil {
		var calcResponse map[string]interface{}
		err := json.Unmarshal([]byte(*result.Content.ContentStr), &calcResponse)
		require.NoError(t, err, "Should parse JSON response")
		assert.Equal(t, float64(100), calcResponse["result"])
	}
}

// TestProtocol_ParallelExecution tests parallel tool execution with real STDIO server
func TestProtocol_ParallelExecution(t *testing.T) {
	t.Skip("Skipping due to mark3labs/mcp-go <-> @modelcontextprotocol/sdk compatibility issue (broken pipe)")
	t.Parallel()

	// Add parallel-test-server
	serverPath := getParallelTestServerPath(t)
	clientConfig := GetSampleSTDIOClientConfig("node", []string{serverPath})
	clientConfig.ID = "parallel-test-client"
	clientConfig.Name = "ParallelTestServer"

	manager := setupMCPManager(t, clientConfig)

	// Wait for connection to establish (STDIO servers need time to start)
	time.Sleep(5 * time.Second)

	// Verify connection
	clients := manager.GetClients()
	require.Len(t, clients, 1, "Should have one client")
	assert.Equal(t, schemas.MCPConnectionStateConnected, clients[0].State)

	// Setup Bifrost instance
	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	// Test multiple tools with different delays (tool names are prefixed with client name)
	testTools := []struct {
		name    string
		id      string
		toolArg string
	}{
		{"ParallelTestServer-fast_tool_1", "fast1", `{"id": "fast1"}`},
		{"ParallelTestServer-fast_tool_2", "fast2", `{"id": "fast2"}`},
		{"ParallelTestServer-medium_tool_1", "medium1", `{"id": "medium1"}`},
		{"ParallelTestServer-slow_tool_1", "slow1", `{"id": "slow1"}`},
	}

	for _, tt := range testTools {
		t.Run(tt.name, func(t *testing.T) {
			toolCall := schemas.ChatAssistantMessageToolCall{
				ID:   schemas.Ptr("call_" + tt.id),
				Type: schemas.Ptr("function"),
				Function: schemas.ChatAssistantMessageToolCallFunction{
					Name:      schemas.Ptr(tt.name),
					Arguments: tt.toolArg,
				},
			}

			result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)
			require.Nil(t, bifrostErr, "Tool should succeed")
			require.NotNil(t, result, "Should have result")

			// Parse and verify response
			if result.Content != nil && result.Content.ContentStr != nil {
				var response map[string]interface{}
				err := json.Unmarshal([]byte(*result.Content.ContentStr), &response)
				require.NoError(t, err, "Should parse JSON response")

				// Verify tool metadata (server returns unprefixed tool name in response)
				toolNameInResponse := response["tool"].(string)
				assert.Contains(t, tt.name, toolNameInResponse, "Tool name should match")
				assert.Equal(t, tt.id, response["id"], "Should have correct ID")
				assert.NotZero(t, response["delay_ms"], "Should have delay")
			}
		})
	}
}

// TestProtocol_ErrorHandling tests error scenarios with real STDIO server
func TestProtocol_ErrorHandling(t *testing.T) {
	t.Skip("Skipping due to mark3labs/mcp-go <-> @modelcontextprotocol/sdk compatibility issue (broken pipe)")
	t.Parallel()

	// Add error-test-server
	serverPath := getErrorTestServerPath(t)
	clientConfig := GetSampleSTDIOClientConfig("node", []string{serverPath})
	clientConfig.ID = "error-test-client"
	clientConfig.Name = "ErrorTestServer"

	manager := setupMCPManager(t, clientConfig)

	// Wait for connection to establish (STDIO servers need time to start)
	time.Sleep(5 * time.Second)

	// Verify connection
	clients := manager.GetClients()
	require.Len(t, clients, 1, "Should have one client")
	assert.Equal(t, schemas.MCPConnectionStateConnected, clients[0].State)

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	t.Run("IntermittentFailure", func(t *testing.T) {
		// Test intermittent_fail tool with 100% fail rate
		toolCall := schemas.ChatAssistantMessageToolCall{
			ID:   schemas.Ptr("call_fail_001"),
			Type: schemas.Ptr("function"),
			Function: schemas.ChatAssistantMessageToolCallFunction{
				Name:      schemas.Ptr("ErrorTestServer-intermittent_fail"),
				Arguments: `{"id": "fail1", "fail_rate": 1.0}`,
			},
		}

		result, _ := bifrost.ExecuteChatMCPTool(ctx, &toolCall)

		// The tool should return an error result
		require.NotNil(t, result, "Should have result even for errors")

		// Verify error content contains error information
		if result.Content != nil && result.Content.ContentStr != nil {
			assert.Contains(t, *result.Content.ContentStr, "error", "Should contain error message")
		}
	})

	t.Run("NetworkError", func(t *testing.T) {
		toolCall := schemas.ChatAssistantMessageToolCall{
			ID:   schemas.Ptr("call_network_001"),
			Type: schemas.Ptr("function"),
			Function: schemas.ChatAssistantMessageToolCallFunction{
				Name:      schemas.Ptr("ErrorTestServer-network_error"),
				Arguments: `{"id": "net1", "error_type": "timeout"}`,
			},
		}

		result, _ := bifrost.ExecuteChatMCPTool(ctx, &toolCall)
		require.NotNil(t, result, "Should have result")

		// Verify error contains expected message
		if result.Content != nil && result.Content.ContentStr != nil {
			assert.Contains(t, *result.Content.ContentStr, "timeout", "Should mention timeout")
		}
	})

	t.Run("LargePayload", func(t *testing.T) {
		// Test large_payload tool with 100KB payload
		toolCall := schemas.ChatAssistantMessageToolCall{
			ID:   schemas.Ptr("call_large_001"),
			Type: schemas.Ptr("function"),
			Function: schemas.ChatAssistantMessageToolCallFunction{
				Name:      schemas.Ptr("ErrorTestServer-large_payload"),
				Arguments: `{"id": "large1", "size_kb": 100}`,
			},
		}

		result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)
		require.Nil(t, bifrostErr, "Large payload should succeed")
		require.NotNil(t, result, "Should have result")

		// Verify large payload was received
		if result.Content != nil && result.Content.ContentStr != nil {
			var response map[string]interface{}
			err := json.Unmarshal([]byte(*result.Content.ContentStr), &response)
			require.NoError(t, err, "Should parse large payload JSON")
			assert.Equal(t, float64(100), response["size_kb"])
			assert.NotEmpty(t, response["payload"], "Should have payload")
		}
	})
}

// TestProtocol_EdgeCases tests edge cases with real STDIO server
func TestProtocol_EdgeCases(t *testing.T) {
	t.Skip("Skipping due to mark3labs/mcp-go <-> @modelcontextprotocol/sdk compatibility issue (broken pipe)")
	t.Parallel()

	// Add edge-case-server
	serverPath := getEdgeCaseServerPath(t)
	clientConfig := GetSampleSTDIOClientConfig("node", []string{serverPath})
	clientConfig.ID = "edge-case-client"
	clientConfig.Name = "EdgeCaseServer"

	manager := setupMCPManager(t, clientConfig)

	// Wait for connection to establish (STDIO servers need time to start)
	time.Sleep(5 * time.Second)

	// Verify connection
	clients := manager.GetClients()
	require.Len(t, clients, 1, "Should have one client")
	assert.Equal(t, schemas.MCPConnectionStateConnected, clients[0].State)

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	t.Run("UnicodeText", func(t *testing.T) {
		toolCall := schemas.ChatAssistantMessageToolCall{
			ID:   schemas.Ptr("call_unicode_001"),
			Type: schemas.Ptr("function"),
			Function: schemas.ChatAssistantMessageToolCallFunction{
				Name:      schemas.Ptr("EdgeCaseServer-unicode_tool"),
				Arguments: `{"id": "unicode1", "include_emojis": true, "include_rtl": true}`,
			},
		}

		result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)
		require.Nil(t, bifrostErr, "Unicode tool should succeed")
		require.NotNil(t, result, "Should have result")

		// Verify Unicode was preserved
		if result.Content != nil && result.Content.ContentStr != nil {
			var response map[string]interface{}
			err := json.Unmarshal([]byte(*result.Content.ContentStr), &response)
			require.NoError(t, err, "Should parse Unicode JSON")
			unicodeText := response["unicode_text"].(string)
			assert.Contains(t, unicodeText, "Ω", "Should contain Greek letters")
			assert.True(t, strings.Contains(unicodeText, "😀") || len(unicodeText) > 20, "Should contain emojis or extended text")
		}
	})

	t.Run("DeeplyNested", func(t *testing.T) {
		toolCall := schemas.ChatAssistantMessageToolCall{
			ID:   schemas.Ptr("call_nested_001"),
			Type: schemas.Ptr("function"),
			Function: schemas.ChatAssistantMessageToolCallFunction{
				Name:      schemas.Ptr("EdgeCaseServer-deeply_nested"),
				Arguments: `{"id": "nested1", "depth": 15}`,
			},
		}

		result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)
		require.Nil(t, bifrostErr, "Deeply nested tool should succeed")
		require.NotNil(t, result, "Should have result")

		// Verify nested structure
		if result.Content != nil && result.Content.ContentStr != nil {
			var response map[string]interface{}
			err := json.Unmarshal([]byte(*result.Content.ContentStr), &response)
			require.NoError(t, err, "Should parse nested JSON")
			assert.Equal(t, float64(15), response["depth"])
			assert.NotNil(t, response["data"], "Should have nested data")
		}
	})

	t.Run("EmptyResponse", func(t *testing.T) {
		toolCall := schemas.ChatAssistantMessageToolCall{
			ID:   schemas.Ptr("call_empty_001"),
			Type: schemas.Ptr("function"),
			Function: schemas.ChatAssistantMessageToolCallFunction{
				Name:      schemas.Ptr("EdgeCaseServer-empty_response"),
				Arguments: `{"id": "empty1", "type": "empty_object"}`,
			},
		}

		result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)
		require.Nil(t, bifrostErr, "Empty response tool should succeed")
		require.NotNil(t, result, "Should have result")

		// Verify empty object
		if result.Content != nil && result.Content.ContentStr != nil {
			var response map[string]interface{}
			err := json.Unmarshal([]byte(*result.Content.ContentStr), &response)
			require.NoError(t, err, "Should parse empty response JSON")
			assert.NotNil(t, response["data"], "Should have data field")
		}
	})

	t.Run("NullFields", func(t *testing.T) {
		toolCall := schemas.ChatAssistantMessageToolCall{
			ID:   schemas.Ptr("call_null_001"),
			Type: schemas.Ptr("function"),
			Function: schemas.ChatAssistantMessageToolCallFunction{
				Name:      schemas.Ptr("EdgeCaseServer-null_fields"),
				Arguments: `{"id": "null1", "null_count": 5}`,
			},
		}

		result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)
		require.Nil(t, bifrostErr, "Null fields tool should succeed")
		require.NotNil(t, result, "Should have result")

		// Verify null fields are preserved
		if result.Content != nil && result.Content.ContentStr != nil {
			var response map[string]interface{}
			err := json.Unmarshal([]byte(*result.Content.ContentStr), &response)
			require.NoError(t, err, "Should parse null fields JSON")

			// Check that null fields exist
			nullCount := 0
			for key, value := range response {
				if strings.HasPrefix(key, "null_field_") && value == nil {
					nullCount++
				}
			}
			assert.Equal(t, 5, nullCount, "Should have 5 null fields")
		}
	})

	t.Run("SpecialCharacters", func(t *testing.T) {
		toolCall := schemas.ChatAssistantMessageToolCall{
			ID:   schemas.Ptr("call_special_001"),
			Type: schemas.Ptr("function"),
			Function: schemas.ChatAssistantMessageToolCallFunction{
				Name:      schemas.Ptr("EdgeCaseServer-special_chars"),
				Arguments: `{"id": "special1", "char_type": "all"}`,
			},
		}

		result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)
		require.Nil(t, bifrostErr, "Special chars tool should succeed")
		require.NotNil(t, result, "Should have result")

		// Verify special characters are properly escaped
		if result.Content != nil && result.Content.ContentStr != nil {
			var response map[string]interface{}
			err := json.Unmarshal([]byte(*result.Content.ContentStr), &response)
			require.NoError(t, err, "Should parse special chars JSON")
			text := response["text"].(string)
			assert.Contains(t, text, "quotes", "Should contain quotes text")
			assert.Contains(t, text, "\\", "Should contain backslashes")
		}
	})
}

// TestProtocol_ToolCallIDPreservation tests that tool call IDs are preserved through protocol
func TestProtocol_ToolCallIDPreservation(t *testing.T) {
	t.Skip("Skipping due to mark3labs/mcp-go <-> @modelcontextprotocol/sdk compatibility issue (broken pipe)")
	t.Parallel()

	serverPath := getTestToolsServerPath(t)
	clientConfig := GetSampleSTDIOClientConfig("node", []string{serverPath})
	clientConfig.ID = "id-test-client"
	clientConfig.Name = "IDTestServer"

	manager := setupMCPManager(t, clientConfig)

	// Wait for connection to establish (STDIO servers need time to start)
	time.Sleep(5 * time.Second)

	// Verify connection
	clients := manager.GetClients()
	require.Len(t, clients, 1, "Should have one client")
	assert.Equal(t, schemas.MCPConnectionStateConnected, clients[0].State)

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	testCases := []struct {
		name   string
		callID string
	}{
		{"standard_id", "call_12345"},
		{"uuid_style", "550e8400-e29b-41d4-a716-446655440000"},
		{"special_chars", "call_test_🔧_001"},
		{"long_id", "call_" + strings.Repeat("x", 100)},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			toolCall := schemas.ChatAssistantMessageToolCall{
				ID:   schemas.Ptr(tc.callID),
				Type: schemas.Ptr("function"),
				Function: schemas.ChatAssistantMessageToolCallFunction{
					Name:      schemas.Ptr("IDTestServer-echo"),
					Arguments: `{"message": "test"}`,
				},
			}

			result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)
			require.Nil(t, bifrostErr, "Tool should succeed")
			require.NotNil(t, result, "Should have result")

			// Verify tool call ID is preserved
			if result.ToolCallID != nil {
				assert.Equal(t, tc.callID, *result.ToolCallID, "Tool call ID should be preserved")
			}
		})
	}
}

// =============================================================================
// HELPER FUNCTIONS
// =============================================================================

// getTestToolsServerPath returns the path to the test-tools-server
func getTestToolsServerPath(t *testing.T) string {
	t.Helper()

	// Path from bifrost root: examples/mcps/test-tools-server/dist/index.js
	// Current working directory is: core/internal/mcptests
	path := filepath.Join("..", "..", "..", "examples", "mcps", "test-tools-server", "dist", "index.js")
	return path
}

// getParallelTestServerPath returns the path to the parallel-test-server
func getParallelTestServerPath(t *testing.T) string {
	t.Helper()
	path := filepath.Join("..", "..", "..", "examples", "mcps", "parallel-test-server", "dist", "index.js")
	return path
}

// getErrorTestServerPath returns the path to the error-test-server
func getErrorTestServerPath(t *testing.T) string {
	t.Helper()
	path := filepath.Join("..", "..", "..", "examples", "mcps", "error-test-server", "dist", "index.js")
	return path
}

// getEdgeCaseServerPath returns the path to the edge-case-server
func getEdgeCaseServerPath(t *testing.T) string {
	t.Helper()
	path := filepath.Join("..", "..", "..", "examples", "mcps", "edge-case-server", "dist", "index.js")
	return path
}
