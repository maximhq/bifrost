package mcptests

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// AGENT MODE: MULTI-CONNECTION TYPE TESTS
// =============================================================================
//
// These tests verify that agent mode correctly orchestrates tool execution
// across multiple connection types simultaneously:
// - InProcess: Tools registered programmatically (echo, calculator, weather)
// - STDIO: External MCP servers via stdio (go-test-server, parallel-test-server)
// - HTTP/SSE: Remote MCP servers (future expansion)
//
// Key concepts:
// - Agent must handle tools from different clients in parallel
// - Permission filtering applies consistently across connection types
// - Tool execution happens concurrently regardless of connection type
// - Error handling works uniformly across all connection types
//
// Related code: core/mcp/agent.go:288-325 (parallel tool execution)
// =============================================================================

// TestAgent_MultiConnection_AllTypes verifies parallel execution across connection types
// Tests that agent can execute tools from InProcess and STDIO clients in parallel
func TestAgent_MultiConnection_AllTypes(t *testing.T) {
	t.Parallel()

	// Setup: InProcess tools + STDIO tools
	manager, mocker, ctx := SetupAgentTest(t, AgentTestConfig{
		InProcessTools:   []string{"echo", "calculator"},
		STDIOClients:     []string{"go-test-server"},
		AutoExecuteTools: []string{"*"}, // All tools auto-execute
		MaxDepth:         5,
	})

	// Turn 1: LLM calls tools from both InProcess and STDIO in parallel
	mocker.AddChatResponse(CreateAgentTurnWithToolCalls(
		// InProcess tools
		GetSampleEchoToolCall("call-1", "test message"),
		GetSampleCalculatorToolCall("call-2", "add", 10, 5),
		// STDIO tool
		CreateSTDIOToolCall("call-3", "GoTestServer", "uuid_generate", map[string]interface{}{}),
	))

	// Turn 2: LLM responds with text (agent completes)
	mocker.AddChatResponse(CreateAgentTurnWithText("All tools executed successfully"))

	// Execute agent
	req := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4",
		Input:    []schemas.ChatMessage{GetSampleUserMessage("Test multi-connection execution")},
	}

	initialResponse, initialErr := mocker.MakeChatRequest(ctx, req)
	require.Nil(t, initialErr)
	require.NotNil(t, initialResponse)

	result, bifrostErr := manager.CheckAndExecuteAgentForChatRequest(
		ctx, req, initialResponse, mocker.MakeChatRequest,
		func(ctx *schemas.BifrostContext, request *schemas.BifrostMCPRequest) (*schemas.BifrostMCPResponse, error) {
			return manager.ExecuteToolCall(ctx, request)
		},
	)

	// Assertions
	require.Nil(t, bifrostErr, "Should complete without error")
	require.NotNil(t, result)

	// Verify agent completed in 2 turns (initial LLM call with tool calls + final summarization call)
	AssertAgentCompletedInTurns(t, mocker, 2)
	AssertAgentFinalResponse(t, result, "stop", "successfully")

	// Verify all 3 tools were called in parallel (turn 1 = in the initial response)
	AssertToolsExecutedInParallel(t, mocker, []string{"echo", "calculator", "GoTestServer-uuid_generate"}, 1)

	t.Logf("✓ Successfully executed tools from InProcess and STDIO clients in parallel")
}

// TestAgent_MultiConnection_MixedPermissions verifies permission filtering across connection types
// Tests that auto-execute, allowed, and blocked tools work correctly across different clients
func TestAgent_MultiConnection_MixedPermissions(t *testing.T) {
	t.Parallel()

	// Setup: Different permissions for different connection types
	manager, mocker, ctx := SetupAgentTest(t, AgentTestConfig{
		InProcessTools: []string{"echo", "calculator", "weather"},
		STDIOClients:   []string{"go-test-server"},
		MaxDepth:       5,
	})

	// Configure permissions:
	// - echo: auto-execute (InProcess)
	// - calculator: allowed but not auto (InProcess)
	// - weather: allowed but not auto (InProcess)
	// - go-test-server tools: not in auto-execute list
	require.NoError(t, SetInternalClientAutoExecute(manager, []string{"echo"}))

	// Set STDIO client to allow but not auto-execute
	clients := manager.GetClients()
	for i := range clients {
		if clients[i].ExecutionConfig.ID == "go-test-server" {
			clients[i].ExecutionConfig.ToolsToAutoExecute = []string{} // Empty = no auto-execute
			require.NoError(t, manager.EditClient(clients[i].ExecutionConfig.ID, clients[i].ExecutionConfig))
			break
		}
	}

	// Turn 1: LLM calls mixed permission tools
	mocker.AddChatResponse(CreateAgentTurnWithToolCalls(
		GetSampleEchoToolCall("call-1", "test"), // Auto-execute
		GetSampleCalculatorToolCall("call-2", "add", 5, 3),                                                // Needs approval
		CreateSTDIOToolCall("call-3", "GoTestServer", "uuid_generate", map[string]interface{}{}), // Needs approval
	))

	// Execute agent
	req := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4",
		Input:    []schemas.ChatMessage{GetSampleUserMessage("Test mixed permissions")},
	}

	initialResponse, initialErr := mocker.MakeChatRequest(ctx, req)
	require.Nil(t, initialErr)

	result, bifrostErr := manager.CheckAndExecuteAgentForChatRequest(
		ctx, req, initialResponse, mocker.MakeChatRequest,
		func(ctx *schemas.BifrostContext, request *schemas.BifrostMCPRequest) (*schemas.BifrostMCPResponse, error) {
			return manager.ExecuteToolCall(ctx, request)
		},
	)

	// Assertions
	require.Nil(t, bifrostErr)
	require.NotNil(t, result)

	// Agent should stop at turn 1 (echo executed, calculator and uuid_generate waiting)
	AssertAgentStoppedAtTurn(t, mocker, 1)

	// Verify response has both content (echo result) and tool_calls (calculator + uuid_generate)
	require.NotEmpty(t, result.Choices)
	choice := result.Choices[0]

	// Should have finish_reason "stop" (agent stopped for approval)
	require.NotNil(t, choice.FinishReason)
	assert.Equal(t, "stop", *choice.FinishReason)

	// Should have content from auto-executed echo
	require.NotNil(t, choice.ChatNonStreamResponseChoice)
	require.NotNil(t, choice.ChatNonStreamResponseChoice.Message)
	require.NotNil(t, choice.ChatNonStreamResponseChoice.Message.Content)
	require.NotNil(t, choice.ChatNonStreamResponseChoice.Message.Content.ContentStr)
	assert.Contains(t, *choice.ChatNonStreamResponseChoice.Message.Content.ContentStr, "Output from allowed tools")

	// Should have tool_calls for non-auto tools
	require.NotNil(t, choice.ChatNonStreamResponseChoice.Message.ChatAssistantMessage)
	toolCalls := choice.ChatNonStreamResponseChoice.Message.ChatAssistantMessage.ToolCalls
	require.Len(t, toolCalls, 2, "Should have 2 tool calls waiting for approval")

	// Verify the waiting tools are calculator and uuid_generate
	toolNames := make(map[string]bool)
	for _, tc := range toolCalls {
		if tc.Function.Name != nil {
			toolNames[*tc.Function.Name] = true
		}
	}
	assert.True(t, toolNames["bifrostInternal-calculator"], "calculator should be waiting for approval")
	assert.True(t, toolNames["GoTestServer-uuid_generate"], "uuid_generate should be waiting for approval")

	t.Logf("✓ Mixed permissions work correctly across InProcess and STDIO clients")
}

// TestAgent_MultiConnection_SequentialAfterParallel verifies sequential execution after parallel
// Tests that agent can do parallel execution, then continue with sequential turns
func TestAgent_MultiConnection_SequentialAfterParallel(t *testing.T) {
	t.Parallel()

	// Setup
	manager, mocker, ctx := SetupAgentTest(t, AgentTestConfig{
		InProcessTools:   []string{"echo", "calculator"},
		STDIOClients:     []string{"go-test-server"},
		AutoExecuteTools: []string{"*"},
		MaxDepth:         5,
	})

	// Turn 1: Parallel execution across connection types
	mocker.AddChatResponse(CreateAgentTurnWithToolCalls(
		GetSampleEchoToolCall("call-1", "first"),
		CreateSTDIOToolCall("call-2", "GoTestServer", "uuid_generate", map[string]interface{}{}),
	))

	// Turn 2: Single tool execution (sequential)
	mocker.AddChatResponse(CreateAgentTurnWithToolCalls(
		GetSampleCalculatorToolCall("call-3", "add", 10, 5),
	))

	// Turn 3: Another single tool
	mocker.AddChatResponse(CreateAgentTurnWithToolCalls(
		GetSampleEchoToolCall("call-4", "last"),
	))

	// Turn 4: Final text
	mocker.AddChatResponse(CreateAgentTurnWithText("All done"))

	// Execute agent
	req := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4",
		Input:    []schemas.ChatMessage{GetSampleUserMessage("Test sequential after parallel")},
	}

	initialResponse, initialErr := mocker.MakeChatRequest(ctx, req)
	require.Nil(t, initialErr)

	result, bifrostErr := manager.CheckAndExecuteAgentForChatRequest(
		ctx, req, initialResponse, mocker.MakeChatRequest,
		func(ctx *schemas.BifrostContext, request *schemas.BifrostMCPRequest) (*schemas.BifrostMCPResponse, error) {
			return manager.ExecuteToolCall(ctx, request)
		},
	)

	// Assertions
	require.Nil(t, bifrostErr)
	require.NotNil(t, result)

	// Should complete in 4 turns
	AssertAgentCompletedInTurns(t, mocker, 4)
	AssertAgentFinalResponse(t, result, "stop", "done")

	t.Logf("✓ Agent correctly handles parallel then sequential execution across connection types")
}

// TestAgent_MultiConnection_ErrorInSTDIO verifies error handling for STDIO tools
// Tests that errors from STDIO tools are properly propagated in agent mode
func TestAgent_MultiConnection_ErrorInSTDIO(t *testing.T) {
	t.Parallel()

	// Setup with error-test-server
	manager, mocker, ctx := SetupAgentTest(t, AgentTestConfig{
		InProcessTools:   []string{"echo"},
		STDIOClients:     []string{"error-test-server"},
		AutoExecuteTools: []string{"*"},
		MaxDepth:         5,
	})

	// Turn 1: Call echo (success) and error tool (will error)
	mocker.AddChatResponse(CreateAgentTurnWithToolCalls(
		GetSampleEchoToolCall("call-1", "test"),
		CreateSTDIOToolCall("call-2", "ErrorTestServer", "return_error", map[string]interface{}{
			"error_type": "standard",
			"message":    "Test error from STDIO",
		}),
	))

	// Turn 2: LLM continues after receiving error
	mocker.AddChatResponse(CreateAgentTurnWithText("Handled the error"))

	// Execute agent
	req := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4",
		Input:    []schemas.ChatMessage{GetSampleUserMessage("Test error handling")},
	}

	initialResponse, initialErr := mocker.MakeChatRequest(ctx, req)
	require.Nil(t, initialErr)

	result, bifrostErr := manager.CheckAndExecuteAgentForChatRequest(
		ctx, req, initialResponse, mocker.MakeChatRequest,
		func(ctx *schemas.BifrostContext, request *schemas.BifrostMCPRequest) (*schemas.BifrostMCPResponse, error) {
			return manager.ExecuteToolCall(ctx, request)
		},
	)

	// Assertions
	require.Nil(t, bifrostErr, "Agent should not fail on tool error")
	require.NotNil(t, result)

	// Agent should continue to turn 2
	AssertAgentCompletedInTurns(t, mocker, 2)

	// Verify that error was passed to LLM as tool result
	// The error should be in the conversation history
	history := mocker.GetChatHistory()
	require.GreaterOrEqual(t, len(history), 2)

	// Check turn 2 history for error message
	turn2History := history[1]
	foundError := false
	for _, msg := range turn2History {
		if msg.Role == schemas.ChatMessageRoleTool {
			if msg.ChatToolMessage != nil && msg.ChatToolMessage.ToolCallID != nil {
				if *msg.ChatToolMessage.ToolCallID == "call-2" {
					// This is the error tool result
					if msg.Content != nil && msg.Content.ContentStr != nil {
						content := *msg.Content.ContentStr
						// Should contain error message
						if len(content) > 0 {
							foundError = true
							t.Logf("Error tool result: %s", content)
						}
					}
				}
			}
		}
	}

	assert.True(t, foundError, "Error from STDIO tool should be in conversation history")

	t.Logf("✓ Errors from STDIO tools are properly handled in agent mode")
}

// TestAgent_MultiConnection_LargeParallelBatch verifies handling of many tools across connections
// Tests that agent can handle a larger batch of parallel tools from different clients
func TestAgent_MultiConnection_LargeParallelBatch(t *testing.T) {
	t.Parallel()

	// Setup: Multiple InProcess tools + STDIO tools
	manager, mocker, ctx := SetupAgentTest(t, AgentTestConfig{
		InProcessTools:   []string{"echo", "calculator", "weather", "get_time"},
		STDIOClients:     []string{"go-test-server", "parallel-test-server"},
		AutoExecuteTools: []string{"*"},
		MaxDepth:         5,
	})

	// Turn 1: Call 8 tools in parallel (4 InProcess + 4 STDIO)
	mocker.AddChatResponse(CreateAgentTurnWithToolCalls(
		// InProcess tools
		GetSampleEchoToolCall("call-1", "msg1"),
		GetSampleCalculatorToolCall("call-2", "add", 1, 2),
		GetSampleWeatherToolCall("call-3", "Tokyo", "celsius"),
		CreateInProcessToolCall("call-4", "get_time", map[string]interface{}{"timezone": "UTC"}),
		// STDIO tools
		CreateSTDIOToolCall("call-5", "GoTestServer", "uuid_generate", map[string]interface{}{}),
		CreateSTDIOToolCall("call-6", "GoTestServer", "string_transform", map[string]interface{}{
			"input":     "test",
			"operation": "uppercase",
		}),
		CreateSTDIOToolCall("call-7", "ParallelTestServer", "fast_operation", map[string]interface{}{}),
		CreateSTDIOToolCall("call-8", "ParallelTestServer", "return_timestamp", map[string]interface{}{}),
	))

	// Turn 2: Final text
	mocker.AddChatResponse(CreateAgentTurnWithText("All 8 tools executed"))

	// Execute agent
	req := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4",
		Input:    []schemas.ChatMessage{GetSampleUserMessage("Test large parallel batch")},
	}

	initialResponse, initialErr := mocker.MakeChatRequest(ctx, req)
	require.Nil(t, initialErr)

	result, bifrostErr := manager.CheckAndExecuteAgentForChatRequest(
		ctx, req, initialResponse, mocker.MakeChatRequest,
		func(ctx *schemas.BifrostContext, request *schemas.BifrostMCPRequest) (*schemas.BifrostMCPResponse, error) {
			return manager.ExecuteToolCall(ctx, request)
		},
	)

	// Assertions
	require.Nil(t, bifrostErr)
	require.NotNil(t, result)

	// Should complete in 2 turns
	AssertAgentCompletedInTurns(t, mocker, 2)

	// Verify all 8 tools executed in parallel (in turn 1 where LLM called them)
	expectedTools := []string{
		"bifrostInternal-echo",
		"bifrostInternal-calculator",
		"bifrostInternal-get_weather",
		"bifrostInternal-get_time",
		"GoTestServer-uuid_generate",
		"GoTestServer-string_transform",
		"ParallelTestServer-fast_operation",
		"ParallelTestServer-return_timestamp",
	}
	AssertToolsExecutedInParallel(t, mocker, expectedTools, 1)

	t.Logf("✓ Successfully executed 8 tools in parallel across InProcess and STDIO clients")
}
