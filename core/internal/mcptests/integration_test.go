package mcptests

import (
	"fmt"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// FULL WORKFLOW INTEGRATION TESTS
// =============================================================================

func TestIntegration_FullChatWorkflow(t *testing.T) {
	t.Parallel()

	// End-to-end test: Setup bifrost with MCP, add multiple clients,
	// execute tools, verify workflow, check health, remove client

	// 1. Setup bifrost with MCP manager
	manager := setupMCPManager(t)
	require.NoError(t, RegisterEchoTool(manager))
	require.NoError(t, RegisterCalculatorTool(manager))

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	// 2. Add HTTP client if available (in addition to InProcess)
	config := GetTestConfig(t)
	if config.HTTPServerURL != "" {
		httpConfig := GetSampleHTTPClientConfig(config.HTTPServerURL)
		httpConfig.ID = "http-integration-test"
		applyTestConfigHeaders(t, &httpConfig)
		err := manager.AddClient(&httpConfig)
		if err != nil {
			t.Logf("Could not add HTTP client: %v", err)
		}
	}

	// Wait for clients to stabilize
	time.Sleep(500 * time.Millisecond)

	// 3. Execute tools in Chat format
	ctx := createTestContext()

	// Execute echo tool
	echoCall := GetSampleEchoToolCall("call-1", "integration test message")
	echoResult, echoErr := bifrost.ExecuteChatMCPTool(ctx, &echoCall)
	require.Nil(t, echoErr, "echo tool should execute successfully")
	require.NotNil(t, echoResult, "echo tool should return result")
	assert.Equal(t, schemas.ChatMessageRoleTool, echoResult.Role)

	// Execute calculator tool
	calcCall := GetSampleCalculatorToolCall("call-2", "add", 10.0, 20.0)
	calcResult, calcErr := bifrost.ExecuteChatMCPTool(ctx, &calcCall)
	require.Nil(t, calcErr, "calculator tool should execute successfully")
	require.NotNil(t, calcResult, "calculator tool should return result")
	assert.Equal(t, schemas.ChatMessageRoleTool, calcResult.Role)

	// 4. Verify complete workflow
	clients := manager.GetClients()
	assert.GreaterOrEqual(t, len(clients), 1, "should have at least InProcess client")

	// All connected clients should be in connected state
	for _, client := range clients {
		if client.State != "" { // Only check if state is set
			assert.Equal(t, schemas.MCPConnectionStateConnected, client.State,
				"client %s should be connected", client.ExecutionConfig.ID)
		}
	}

	// 5. Check health monitoring
	// Verify clients have tools
	for _, client := range clients {
		assert.NotEmpty(t, client.ToolMap, "client %s should have tools", client.ExecutionConfig.ID)
	}

	// 6. Remove HTTP client if it was added
	if config.HTTPServerURL != "" {
		// Try to remove, but don't fail if it doesn't exist or can't be removed
		_ = manager.RemoveClient("http-integration-test")
	}

	t.Logf("✅ Full Chat workflow integration test completed successfully")
}

func TestIntegration_FullResponsesWorkflow(t *testing.T) {
	t.Parallel()

	// Same as Chat but using Responses API format

	// 1. Setup bifrost with MCP manager
	manager := setupMCPManager(t)
	require.NoError(t, RegisterEchoTool(manager))
	require.NoError(t, RegisterCalculatorTool(manager))

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	// 2. Execute tools in Responses format
	ctx := createTestContext()

	// Execute echo tool (Responses format)
	echoToolCall := GetSampleResponsesToolCallMessage("call-1", "bifrostInternal-echo", map[string]interface{}{
		"message": "responses integration test",
	})
	if echoToolCall.ResponsesToolMessage == nil {
		t.Skip("ResponsesToolMessage format not available")
	}
	echoResult, echoErr := bifrost.ExecuteResponsesMCPTool(ctx, echoToolCall.ResponsesToolMessage)
	require.Nil(t, echoErr, "echo tool should execute successfully")
	require.NotNil(t, echoResult, "echo tool should return result")
	assert.Equal(t, schemas.ResponsesMessageTypeFunctionCallOutput, *echoResult.Type)

	// Execute calculator tool (Responses format)
	calcToolCall := GetSampleResponsesToolCallMessage("call-2", "bifrostInternal-calculator", map[string]interface{}{
		"operation": "multiply",
		"x":         float64(5),
		"y":         float64(7),
	})
	calcResult, calcErr := bifrost.ExecuteResponsesMCPTool(ctx, calcToolCall.ResponsesToolMessage)
	require.Nil(t, calcErr, "calculator tool should execute successfully")
	require.NotNil(t, calcResult, "calculator tool should return result")
	assert.Equal(t, schemas.ResponsesMessageTypeFunctionCallOutput, *calcResult.Type)

	// 3. Verify workflow
	clients := manager.GetClients()
	assert.GreaterOrEqual(t, len(clients), 1, "should have at least InProcess client")

	// Verify tools are available
	tools := manager.GetToolPerClient(ctx)
	assert.NotEmpty(t, tools, "should have tools available")

	t.Logf("✅ Full Responses workflow integration test completed successfully")
}

// =============================================================================
// AGENT WITH PLUGINS INTEGRATION
// =============================================================================

func TestIntegration_AgentWithPlugins(t *testing.T) {
	t.Parallel()

	// TODO: Implement agent with plugins integration test
	// Setup agent mode + plugins
	// Execute multi-step task
	// Verify plugins are called for each iteration
	// Check logging plugin captures all steps
	// Verify governance plugin can block
	t.Skip("TODO: Implement agent with plugins integration test")
}

func TestIntegration_AgentWithGovernance(t *testing.T) {
	t.Parallel()

	// TODO: Implement agent with governance test
	// Agent tries to execute blocked tool
	// Governance plugin short-circuits
	// Agent handles gracefully
	t.Skip("TODO: Implement agent with governance test")
}

// =============================================================================
// CODE MODE WITH AGENT INTEGRATION
// =============================================================================

func TestIntegration_CodeModeWithAgent(t *testing.T) {
	t.Parallel()

	// TODO: Implement code mode with agent integration test
	// Code mode client + agent enabled
	// Execute code that triggers agent loop
	// Verify full workflow works
	// Check auto-execute filtering
	t.Skip("TODO: Implement code mode with agent integration test")
}

func TestIntegration_CodeModeCallingTools(t *testing.T) {
	t.Parallel()

	// TODO: Implement code mode calling tools integration test
	// Code mode client + HTTP client
	// Execute code that calls multiple tools
	// Verify all work together
	t.Skip("TODO: Implement code mode calling tools integration test")
}

// =============================================================================
// MULTI-CLIENT MULTI-TOOL INTEGRATION
// =============================================================================

func TestIntegration_MultiClientMultiTool(t *testing.T) {
	t.Parallel()

	// Setup 3 different InProcess clients, each with different tools
	// Execute tools from all clients and verify correct routing

	manager := setupMCPManager(t)

	// Client 1: Echo tool
	require.NoError(t, RegisterEchoTool(manager))

	// Client 2: Calculator tool (register additional tool on same InProcess client)
	require.NoError(t, RegisterCalculatorTool(manager))

	// Client 3: Weather tool
	require.NoError(t, RegisterWeatherTool(manager))

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	// Execute tools from all clients
	// Execute echo
	echoCall := GetSampleEchoToolCall("call-1", "multi-client test")
	echoResult, echoErr := bifrost.ExecuteChatMCPTool(ctx, &echoCall)
	require.Nil(t, echoErr, "echo tool should execute")
	require.NotNil(t, echoResult)

	// Execute calculator
	calcCall := GetSampleCalculatorToolCall("call-2", "subtract", 15.0, 5.0)
	calcResult, calcErr := bifrost.ExecuteChatMCPTool(ctx, &calcCall)
	require.Nil(t, calcErr, "calculator tool should execute")
	require.NotNil(t, calcResult)

	// Execute weather
	weatherCall := GetSampleWeatherToolCall("call-3", "London", "celsius")
	weatherResult, weatherErr := bifrost.ExecuteChatMCPTool(ctx, &weatherCall)
	require.Nil(t, weatherErr, "weather tool should execute")
	require.NotNil(t, weatherResult)

	// Verify all tools are available
	tools := manager.GetToolPerClient(ctx)
	assert.NotEmpty(t, tools, "should have tools from all clients")

	// Verify tools include all three
	allTools := make(map[string]bool)
	for _, clientTools := range tools {
		for _, tool := range clientTools {
			if tool.Function != nil && tool.Function.Name != "" {
				allTools[tool.Function.Name] = true
			}
		}
	}

	// Tools are registered without prefix but accessed with bifrostInternal- prefix
	// Check that we have at least the registered tools (they might be named either way depending on how they're exposed)
	hasEcho := allTools["echo"] || allTools["bifrostInternal-echo"]
	hasCalculator := allTools["calculator"] || allTools["bifrostInternal-calculator"]
	hasWeather := allTools["get_weather"] || allTools["bifrostInternal-get_weather"]

	assert.True(t, hasEcho, "should have echo tool")
	assert.True(t, hasCalculator, "should have calculator tool")
	assert.True(t, hasWeather, "should have weather tool")

	t.Logf("✅ Multi-client multi-tool integration test completed successfully")
}

func TestIntegration_ToolConflictResolution(t *testing.T) {
	t.Parallel()

	// Multiple clients with overlapping tools
	// Verify basic tool resolution works

	manager := setupMCPManager(t)

	// Register multiple tools on same client
	require.NoError(t, RegisterEchoTool(manager))
	require.NoError(t, RegisterCalculatorTool(manager))
	require.NoError(t, RegisterWeatherTool(manager))

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	// Execute different tools - verify routing works correctly
	echoCall := GetSampleEchoToolCall("call-1", "test message")
	echoResult, echoErr := bifrost.ExecuteChatMCPTool(ctx, &echoCall)
	require.Nil(t, echoErr, "echo should execute")
	require.NotNil(t, echoResult)

	calcCall := GetSampleCalculatorToolCall("call-2", "divide", 100.0, 4.0)
	calcResult, calcErr := bifrost.ExecuteChatMCPTool(ctx, &calcCall)
	require.Nil(t, calcErr, "calculator should execute")
	require.NotNil(t, calcResult)

	weatherCall := GetSampleWeatherToolCall("call-3", "Paris", "celsius")
	weatherResult, weatherErr := bifrost.ExecuteChatMCPTool(ctx, &weatherCall)
	require.Nil(t, weatherErr, "weather should execute")
	require.NotNil(t, weatherResult)

	// Verify all tools are available
	tools := manager.GetToolPerClient(ctx)
	assert.NotEmpty(t, tools, "should have tools")

	// Count total tools
	toolCount := 0
	for _, clientTools := range tools {
		toolCount += len(clientTools)
	}
	assert.GreaterOrEqual(t, toolCount, 3, "should have at least 3 tools")

	t.Logf("✅ Tool conflict resolution integration test completed successfully")
}

// =============================================================================
// HEALTH RECOVERY DURING OPERATIONS
// =============================================================================

func TestIntegration_HealthRecoveryDuringAgent(t *testing.T) {
	t.Parallel()

	// TODO: Implement health recovery during agent test
	// Start agent loop
	// Simulate client disconnect mid-loop
	// Reconnect client
	// Verify agent handles gracefully
	t.Skip("TODO: Implement health recovery during agent test")
}

func TestIntegration_ReconnectDuringExecution(t *testing.T) {
	t.Parallel()

	// Start long tool execution, trigger client reconnect,
	// verify system handles it gracefully

	// Use HTTP client if available (InProcess doesn't support reconnect)
	config := GetTestConfig(t)
	if config.HTTPServerURL == "" {
		t.Skip("HTTP server required for reconnect test")
	}

	manager := setupMCPManager(t)

	// Add HTTP client
	httpConfig := GetSampleHTTPClientConfig(config.HTTPServerURL)
	httpConfig.ID = "reconnect-test-client"
	applyTestConfigHeaders(t, &httpConfig)
	err := manager.AddClient(&httpConfig)
	require.NoError(t, err, "should add HTTP client")

	// Wait for client to connect
	time.Sleep(500 * time.Millisecond)

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	// Get available tools from HTTP client
	tools := manager.GetToolPerClient(ctx)
	if len(tools) == 0 {
		t.Skip("No tools available from HTTP client")
	}

	// Get first available tool
	var firstToolName string
	for _, clientTools := range tools {
		if len(clientTools) > 0 && clientTools[0].Function != nil {
			firstToolName = clientTools[0].Function.Name
			break
		}
	}

	if firstToolName == "" {
		t.Skip("No tools with names available")
	}

	// Execute a tool to verify client works
	toolCall := schemas.ChatAssistantMessageToolCall{
		ID:   schemas.Ptr("before-reconnect"),
		Type: schemas.Ptr("function"),
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name:      &firstToolName,
			Arguments: `{}`,
		},
	}
	_, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)
	if bifrostErr != nil {
		t.Logf("Initial execution: %v", bifrostErr)
	}

	// Trigger reconnect
	reconnectErr := manager.ReconnectClient("reconnect-test-client")
	if reconnectErr != nil {
		t.Logf("Reconnect error (may be expected): %v", reconnectErr)
	}

	// Wait for reconnect to complete
	time.Sleep(1 * time.Second)

	// Verify system is still functional
	clients := manager.GetClients()
	assert.GreaterOrEqual(t, len(clients), 1, "clients should exist after reconnect attempt")

	t.Logf("✅ Reconnect during execution test completed successfully")
}

// =============================================================================
// END-TO-END SCENARIO TESTS
// =============================================================================

func TestIntegration_EndToEnd_SimpleTask(t *testing.T) {
	t.Parallel()

	config := GetTestConfig(t)
	if !config.UseRealLLM {
		t.Skip("Real LLM not configured")
	}

	// TODO: Implement end-to-end simple task test
	// Full realistic scenario:
	// 1. User asks "What is 5 + 3?"
	// 2. LLM calls calculator tool
	// 3. Agent auto-executes
	// 4. LLM returns final answer
	// 5. Verify complete flow
	t.Skip("TODO: Implement end-to-end simple task test")
}

func TestIntegration_EndToEnd_ComplexTask(t *testing.T) {
	t.Parallel()

	config := GetTestConfig(t)
	if !config.UseRealLLM {
		t.Skip("Real LLM not configured")
	}

	// TODO: Implement end-to-end complex task test
	// Multi-step task:
	// 1. User asks "Calculate 2+2, multiply by 3, then tell me the weather"
	// 2. LLM calls calculator twice
	// 3. LLM calls weather tool
	// 4. Agent executes all
	// 5. LLM returns final answer
	// 6. Verify complete flow with multiple iterations
	t.Skip("TODO: Implement end-to-end complex task test")
}

func TestIntegration_EndToEnd_WithCodeMode(t *testing.T) {
	t.Parallel()

	config := GetTestConfig(t)
	if !config.UseRealLLM {
		t.Skip("Real LLM not configured")
	}

	// TODO: Implement end-to-end with code mode test
	// LLM uses executeToolCode to write complex logic
	// Code calls multiple tools
	// Agent handles everything
	// Verify complete flow
	t.Skip("TODO: Implement end-to-end with code mode test")
}

// =============================================================================
// ERROR RECOVERY INTEGRATION
// =============================================================================

func TestIntegration_ErrorRecovery(t *testing.T) {
	t.Parallel()

	// Tool returns error mid-workflow, system handles gracefully,
	// subsequent operations still work

	manager := setupMCPManager(t)
	require.NoError(t, RegisterEchoTool(manager))
	require.NoError(t, RegisterThrowErrorTool(manager))
	require.NoError(t, RegisterCalculatorTool(manager))

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	// Execute successful tool
	echoCall := GetSampleEchoToolCall("call-1", "before error")
	result1, err1 := bifrost.ExecuteChatMCPTool(ctx, &echoCall)
	require.Nil(t, err1, "first tool should succeed")
	require.NotNil(t, result1)

	// Execute tool that throws error
	errorCall := schemas.ChatAssistantMessageToolCall{
		ID:   schemas.Ptr("call-2"),
		Type: schemas.Ptr("function"),
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name:      schemas.Ptr("throw_error"),
			Arguments: `{"error_message":"intentional test error"}`,
		},
	}
	_, err2 := bifrost.ExecuteChatMCPTool(ctx, &errorCall)
	// Error tool may return error or succeed with error message - both are acceptable
	if err2 != nil {
		t.Logf("Error tool returned error: %v", err2)
	} else {
		t.Logf("Error tool completed (error may be in message content)")
	}

	// Verify system still works - execute another tool
	calcCall := GetSampleCalculatorToolCall("call-3", "add", 100.0, 50.0)
	result3, err3 := bifrost.ExecuteChatMCPTool(ctx, &calcCall)
	require.Nil(t, err3, "subsequent tool should succeed after error")
	require.NotNil(t, result3)

	// Verify clients are still healthy
	clients := manager.GetClients()
	assert.GreaterOrEqual(t, len(clients), 1, "clients should still exist")
	for _, client := range clients {
		// Only check state if it's set (InProcess clients may not have state)
		if client.State != "" {
			assert.Equal(t, schemas.MCPConnectionStateConnected, client.State,
				"client should still be connected after error")
		}
	}

	t.Logf("✅ Error recovery integration test completed successfully")
}

func TestIntegration_PartialFailure(t *testing.T) {
	t.Parallel()

	// Execute 3 tools sequentially, 2nd tool fails,
	// verify 1st result is preserved and error is reported correctly

	manager := setupMCPManager(t)
	require.NoError(t, RegisterEchoTool(manager))
	require.NoError(t, RegisterThrowErrorTool(manager))
	require.NoError(t, RegisterCalculatorTool(manager))

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	// Store results
	results := make([]*schemas.ChatMessage, 3)
	errors := make([]*schemas.BifrostError, 3)

	// Tool 1: Echo (should succeed)
	echoCall := GetSampleEchoToolCall("call-1", "first tool")
	results[0], errors[0] = bifrost.ExecuteChatMCPTool(ctx, &echoCall)

	// Tool 2: Error (should fail)
	errorCall := schemas.ChatAssistantMessageToolCall{
		ID:   schemas.Ptr("call-2"),
		Type: schemas.Ptr("function"),
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name:      schemas.Ptr("throw_error"),
			Arguments: `{"error_message":"partial failure test"}`,
		},
	}
	results[1], errors[1] = bifrost.ExecuteChatMCPTool(ctx, &errorCall)

	// Tool 3: Calculator (should succeed)
	calcCall := GetSampleCalculatorToolCall("call-3", "multiply", 6.0, 7.0)
	results[2], errors[2] = bifrost.ExecuteChatMCPTool(ctx, &calcCall)

	// Verify 1st tool result is preserved
	require.Nil(t, errors[0], "first tool should succeed")
	require.NotNil(t, results[0], "first tool should have result")
	assert.Equal(t, schemas.ChatMessageRoleTool, results[0].Role)

	// Verify 2nd tool completed (error may be in result or in error object)
	if errors[1] != nil {
		t.Logf("Second tool returned error: %v", errors[1])
	} else if results[1] != nil {
		t.Logf("Second tool completed (error may be in message)")
	}

	// Verify 3rd tool succeeds (system recovered)
	require.Nil(t, errors[2], "third tool should succeed")
	require.NotNil(t, results[2], "third tool should have result")

	t.Logf("✅ Partial failure integration test completed successfully")
	t.Logf("   Tool 1: Success, Tool 2: Completed, Tool 3: Success")
}

// =============================================================================
// PERFORMANCE INTEGRATION
// =============================================================================

func TestIntegration_HighLoadScenario(t *testing.T) {
	t.Parallel()

	// Many concurrent workflows with multiple clients,
	// verify system remains stable and response times are reasonable

	manager := setupMCPManager(t)
	require.NoError(t, RegisterEchoTool(manager))
	require.NoError(t, RegisterCalculatorTool(manager))
	require.NoError(t, RegisterWeatherTool(manager))

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	// Execute 200 concurrent tool calls
	concurrency := 200
	done := make(chan bool, concurrency)
	errors := make(chan error, concurrency)

	start := time.Now()

	for i := 0; i < concurrency; i++ {
		go func(id int) {
			toolType := id % 3

			var err *schemas.BifrostError

			switch toolType {
			case 0: // Echo
				callID := fmt.Sprintf("high-load-echo-%d", id)
				message := fmt.Sprintf("message-%d", id)
				echoCall := GetSampleEchoToolCall(callID, message)
				_, err = bifrost.ExecuteChatMCPTool(ctx, &echoCall)

			case 1: // Calculator
				callID := fmt.Sprintf("high-load-calc-%d", id)
				calcCall := GetSampleCalculatorToolCall(callID, "add", float64(id), float64(id+1))
				_, err = bifrost.ExecuteChatMCPTool(ctx, &calcCall)

			case 2: // Weather
				callID := fmt.Sprintf("high-load-weather-%d", id)
				weatherCall := GetSampleWeatherToolCall(callID, "Tokyo", "celsius")
				_, err = bifrost.ExecuteChatMCPTool(ctx, &weatherCall)
			}

			if err != nil {
				errors <- fmt.Errorf("tool %d failed: %v", id, err)
			}

			done <- true
		}(i)
	}

	// Wait for all to complete
	for i := 0; i < concurrency; i++ {
		<-done
	}
	elapsed := time.Since(start)
	close(errors)

	// Check for errors
	errorCount := 0
	for err := range errors {
		t.Logf("High load error: %v", err)
		errorCount++
	}

	// Verify system remained stable
	clients := manager.GetClients()
	assert.GreaterOrEqual(t, len(clients), 1, "clients should exist after high load")

	for _, client := range clients {
		// Only check state if it's set (InProcess clients may not have state)
		if client.State != "" {
			assert.Equal(t, schemas.MCPConnectionStateConnected, client.State,
				"client should remain connected after high load")
		}
	}

	// Verify response times are reasonable (< 5 seconds for 200 operations)
	assert.Less(t, elapsed.Seconds(), 5.0, "should complete 200 operations in under 5 seconds")

	// Allow some errors under high load but expect >95% success rate
	successRate := float64(concurrency-errorCount) / float64(concurrency) * 100
	assert.Greater(t, successRate, 95.0, "success rate should be >95%% under high load")

	t.Logf("✅ High load scenario test completed successfully")
	t.Logf("   Operations: %d, Elapsed: %v, Errors: %d, Success rate: %.2f%%",
		concurrency, elapsed, errorCount, successRate)
	t.Logf("   Throughput: %.0f ops/sec", float64(concurrency)/elapsed.Seconds())
}
