package mcp

import (
	"context"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMCPManagerInitialization(t *testing.T) {
	ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrost(ctx)
	require.NoError(t, err)
	require.NotNil(t, b)

	// Verify MCP is configured
	clients, err := b.GetMCPClients()
	require.NoError(t, err)
	assert.NotNil(t, clients)
}

func TestLocalToolRegistration(t *testing.T) {
	ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrost(ctx)
	require.NoError(t, err)

	// Register test tools
	err = registerTestTools(b)
	require.NoError(t, err)

	// Verify tools are available
	clients, err := b.GetMCPClients()
	require.NoError(t, err)
	require.NotEmpty(t, clients)

	// Find the bifrostInternal client
	var bifrostClient *schemas.MCPClient
	for i := range clients {
		if clients[i].Config.ID == "bifrostInternal" {
			bifrostClient = &clients[i]
			break
		}
	}

	require.NotNil(t, bifrostClient, "bifrostInternal client should exist")
	assert.Equal(t, schemas.MCPConnectionStateConnected, bifrostClient.State)

	// Verify tools are registered
	toolNames := make(map[string]bool)
	for _, tool := range bifrostClient.Tools {
		toolNames[tool.Name] = true
	}

	assert.True(t, toolNames["echo"], "echo tool should be registered")
	assert.True(t, toolNames["add"], "add tool should be registered")
	assert.True(t, toolNames["multiply"], "multiply tool should be registered")
}

func TestToolDiscovery(t *testing.T) {
	ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), TestTimeout)
	defer cancel()

	// Use CodeMode since we're testing CodeMode tools (listToolFiles, readToolFile)
	b, err := setupTestBifrostWithCodeMode(ctx)
	require.NoError(t, err)
	// Tools are already registered in setupTestBifrostWithCodeMode

	// Test listToolFiles
	listToolCall := createResponsesToolCall("listToolFiles", schemas.OrderedMap{})
	result, bifrostErr := b.ExecuteResponsesMCPTool(ctx, listToolCall)
	requireNoBifrostError(t, bifrostErr)
	require.NotNil(t, result)
	require.NotNil(t, result.Content)
	require.NotNil(t, result.Content.ContentStr)

	responseText := *result.Content.ContentStr
	assert.Contains(t, responseText, "servers/", "Should list servers")
	assert.Contains(t, responseText, "BifrostClient.d.ts", "Should list BifrostClient server")

	// Test readToolFile
	readToolCall := createResponsesToolCall("readToolFile", schemas.OrderedMap{
		"fileName": "BifrostClient.d.ts",
	})
	result, bifrostErr = b.ExecuteResponsesMCPTool(ctx, readToolCall)
	requireNoBifrostError(t, bifrostErr)
	require.NotNil(t, result)
	require.NotNil(t, result.Content)
	require.NotNil(t, result.Content.ContentStr)

	responseText = *result.Content.ContentStr
	assert.Contains(t, responseText, "interface", "Should contain TypeScript interface declarations")
	assert.Contains(t, responseText, "echo", "Should contain echo tool definition")
	assert.Contains(t, responseText, "EchoInput", "Should contain echo input interface")
}

func TestToolExecution(t *testing.T) {
	ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrost(ctx)
	require.NoError(t, err)

	// Register test tools
	err = registerTestTools(b)
	require.NoError(t, err)

	// Test echo tool
	echoCall := createResponsesToolCall("echo", schemas.OrderedMap{
		"message": "test message",
	})
	result, bifrostErr := b.ExecuteResponsesMCPTool(ctx, echoCall)
	requireNoBifrostError(t, bifrostErr)
	require.NotNil(t, result)
	require.NotNil(t, result.Content)
	require.NotNil(t, result.Content.ContentStr)

	responseText := *result.Content.ContentStr
	assert.Equal(t, "test message", responseText)

	// Test add tool
	addCall := createResponsesToolCall("add", schemas.OrderedMap{
		"a": schemas.Ptr(5),
		"b": schemas.Ptr(3),
	})
	result, bifrostErr = b.ExecuteResponsesMCPTool(ctx, addCall)
	requireNoBifrostError(t, bifrostErr)
	require.NotNil(t, result)
	require.NotNil(t, result.Content)
	require.NotNil(t, result.Content.ContentStr)

	responseText = *result.Content.ContentStr
	assert.Equal(t, "8", responseText)

	// Test multiply tool
	multiplyCall := createResponsesToolCall("multiply", schemas.OrderedMap{
		"a": schemas.Ptr(4),
		"b": schemas.Ptr(7),
	})
	result, bifrostErr = b.ExecuteResponsesMCPTool(ctx, multiplyCall)
	requireNoBifrostError(t, bifrostErr)
	require.NotNil(t, result)
	require.NotNil(t, result.Content)
	require.NotNil(t, result.Content.ContentStr)

	responseText = *result.Content.ContentStr
	assert.Equal(t, "28", responseText)
}

func TestMultipleServers(t *testing.T) {
	ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), TestTimeout)
	defer cancel()

	// Use CodeMode since we're testing CodeMode tools (listToolFiles)
	b, err := setupTestBifrostWithCodeMode(ctx)
	require.NoError(t, err)
	// Tools are already registered in setupTestBifrostWithCodeMode

	// Verify we have at least one server
	clients, err := b.GetMCPClients()
	require.NoError(t, err)
	require.NotEmpty(t, clients)

	// Test listToolFiles with multiple servers
	listToolCall := createResponsesToolCall("listToolFiles", schemas.OrderedMap{})
	result, bifrostErr := b.ExecuteResponsesMCPTool(ctx, listToolCall)
	requireNoBifrostError(t, bifrostErr)
	require.NotNil(t, result)
	require.NotNil(t, result.Content)
	require.NotNil(t, result.Content.ContentStr)

	responseText := *result.Content.ContentStr
	assert.Contains(t, responseText, "BifrostClient.d.ts", "Should list BifrostClient server")
}

// TestExternalMCPConnection tests connection to external MCP server
// This test requires external MCP credentials to be provided via environment variables
// or test configuration. For now, it's a placeholder that can be enabled when credentials are available.
func TestExternalMCPConnection(t *testing.T) {
	t.Skip("Skipping external MCP connection test - requires credentials")

	ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), TestTimeout)
	defer cancel()

	_, err := setupTestBifrost(ctx)
	require.NoError(t, err)

	// Example: Connect to external MCP server
	// Uncomment and configure when credentials are available
	/*
		connectionString := os.Getenv("EXTERNAL_MCP_CONNECTION_STRING")
		if connectionString == "" {
			t.Skip("EXTERNAL_MCP_CONNECTION_STRING not set")
		}

		err = connectExternalMCP(b, "external-server", "external-1", "http", connectionString)
		require.NoError(t, err)

		// Verify connection
		clients := b.GetMCPClients()
		found := false
		for _, client := range clients {
			if client.Config.ID == "external-1" {
				found = true
				assert.Equal(t, schemas.MCPConnectionStateConnected, client.State)
				break
			}
		}
		assert.True(t, found, "External client should be connected")
	*/
}

func TestToolExecutionTimeout(t *testing.T) {
	ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrost(ctx)
	require.NoError(t, err)

	// Register test tools
	err = registerTestTools(b)
	require.NoError(t, err)

	// Test slow tool with short timeout
	slowCall := createResponsesToolCall("slow_tool", schemas.OrderedMap{
		"delay_ms": schemas.Ptr(100),
	})

	start := time.Now()
	result, bifrostErr := b.ExecuteResponsesMCPTool(ctx, slowCall)
	duration := time.Since(start)

	requireNoBifrostError(t, bifrostErr)
	require.NotNil(t, result)
	assert.GreaterOrEqual(t, duration, 100*time.Millisecond, "Should take at least 100ms")
}

func TestToolExecutionError(t *testing.T) {
	ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrost(ctx)
	require.NoError(t, err)

	// Register test tools
	err = registerTestTools(b)
	require.NoError(t, err)

	// Test error tool - tool execution succeeds but result contains error message
	errorCall := createResponsesToolCall("error_tool", schemas.OrderedMap{})
	result, bifrostErr := b.ExecuteResponsesMCPTool(ctx, errorCall)

	// Tool execution should succeed (no bifrostErr), but result should contain error message
	requireNoBifrostError(t, bifrostErr)
	require.NotNil(t, result)
	require.NotNil(t, result.Content)
	require.NotNil(t, result.Content.ContentStr)

	responseText := *result.Content.ContentStr
	assert.Contains(t, responseText, "Error:", "Result should contain error message")
	assert.Contains(t, responseText, "this tool always fails", "Result should contain the error text")
}

func TestComplexArgsTool(t *testing.T) {
	ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), TestTimeout)
	defer cancel()

	b, err := setupTestBifrost(ctx)
	require.NoError(t, err)

	// Register test tools
	err = registerTestTools(b)
	require.NoError(t, err)

	// Test complex args tool
	complexCall := createResponsesToolCall("complex_args_tool", schemas.OrderedMap{
		"data": map[string]interface{}{
			"nested": map[string]interface{}{
				"value": float64(42),
				"array": []interface{}{1, 2, 3},
			},
		},
	})
	result, bifrostErr := b.ExecuteResponsesMCPTool(ctx, complexCall)
	requireNoBifrostError(t, bifrostErr)
	require.NotNil(t, result)
	require.NotNil(t, result.Content)
	require.NotNil(t, result.Content.ContentStr)

	responseText := *result.Content.ContentStr
	assert.Contains(t, responseText, "Received data", "Should process complex args")
	assert.Contains(t, responseText, "42", "Should contain nested value")
}
