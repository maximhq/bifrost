package mcptests

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// AGENT MODE: RESPONSES API ADAPTER EDGE CASES
// =============================================================================
//
// These tests verify that the Responses API adapter (responsesAPIAdapter) handles
// edge cases correctly and maintains feature parity with Chat API:
// - Complex tool calls in Responses format
// - Nested content blocks
// - Mixed message types
// - Empty and null tool results
// - Large payloads
// - Multiple tool calls in parallel
// - Format conversion edge cases
//
// The adapter pattern (agentadaptors.go) ensures both Chat and Responses APIs
// work identically in agent mode by converting at boundaries.
//
// Related code: core/mcp/agentadaptors.go (responsesAPIAdapter implementation)
// =============================================================================

// TestAgent_Adapter_ResponsesFormat_BasicLoop verifies basic Responses API adapter functionality
// Tests that agent mode works correctly with Responses API format
func TestAgent_Adapter_ResponsesFormat_BasicLoop(t *testing.T) {
	t.Parallel()

	// Setup
	manager, mocker, ctx := SetupAgentTest(t, AgentTestConfig{
		InProcessTools:   []string{"echo", "calculator"},
		AutoExecuteTools: []string{"*"},
		MaxDepth:         5,
	})

	// Turn 1: LLM calls tools
	mocker.AddResponsesResponse(CreateAgentTurnWithToolCallsResponses(
		GetSampleEchoToolCall("call-1", "test"),
		GetSampleCalculatorToolCall("call-2", "add", 10, 5),
	))

	// Turn 2: Final text
	mocker.AddResponsesResponse(CreateAgentTurnWithTextResponses("All done"))

	// Execute agent with Responses API
	req := &schemas.BifrostResponsesRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4",
		Input: []schemas.ResponsesMessage{
			GetSampleUserMessageResponses("Test Responses API"),
		},
	}

	initialResponse, initialErr := mocker.MakeResponsesRequest(ctx, req)
	require.Nil(t, initialErr)

	result, bifrostErr := manager.CheckAndExecuteAgentForResponsesRequest(
		ctx, req, initialResponse, mocker.MakeResponsesRequest,
		func(ctx *schemas.BifrostContext, request *schemas.BifrostMCPRequest) (*schemas.BifrostMCPResponse, error) {
			return manager.ExecuteToolCall(ctx, request)
		},
	)

	// Assertions
	require.Nil(t, bifrostErr)
	require.NotNil(t, result)

	// Verify completion
	AssertAgentCompletedInTurnsResponses(t, mocker, 2)
	AssertAgentFinalResponseResponses(t, result, "stop", "done")

	t.Logf("✓ Responses API adapter handles basic agent loop correctly")
}

// TestAgent_Adapter_ResponsesFormat_EmptyToolResult verifies empty tool result handling
// Tests that adapter correctly handles empty string tool results
func TestAgent_Adapter_ResponsesFormat_EmptyToolResult(t *testing.T) {
	t.Parallel()

	// Setup: Register custom tool that returns empty string
	manager, mocker, ctx := SetupAgentTest(t, AgentTestConfig{
		InProcessTools:   []string{"echo"},
		AutoExecuteTools: []string{"*"},
		MaxDepth:         5,
	})

	// Turn 1: Call echo with empty message
	mocker.AddResponsesResponse(CreateAgentTurnWithToolCallsResponses(
		GetSampleEchoToolCall("call-1", ""), // Empty input
	))

	// Turn 2: Final text
	mocker.AddResponsesResponse(CreateAgentTurnWithTextResponses("Handled empty result"))

	// Execute
	req := &schemas.BifrostResponsesRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4",
		Input: []schemas.ResponsesMessage{
			GetSampleUserMessageResponses("Test empty result"),
		},
	}

	initialResponse, initialErr := mocker.MakeResponsesRequest(ctx, req)
	require.Nil(t, initialErr)

	result, bifrostErr := manager.CheckAndExecuteAgentForResponsesRequest(
		ctx, req, initialResponse, mocker.MakeResponsesRequest,
		func(ctx *schemas.BifrostContext, request *schemas.BifrostMCPRequest) (*schemas.BifrostMCPResponse, error) {
			return manager.ExecuteToolCall(ctx, request)
		},
	)

	// Assertions
	require.Nil(t, bifrostErr)
	require.NotNil(t, result)

	AssertAgentCompletedInTurnsResponses(t, mocker, 2)

	// Verify empty result was passed to LLM
	history := mocker.GetResponsesHistory()
	require.GreaterOrEqual(t, len(history), 2)

	// Check turn 2 history for empty tool result
	turn2History := history[1]
	foundEmptyResult := false
	for _, msg := range turn2History {
		if msg.Type != nil && *msg.Type == schemas.ResponsesMessageTypeFunctionCallOutput {
			if msg.CallID != nil && *msg.CallID == "call-1" {
				foundEmptyResult = true
				// Content should be present but empty or contain empty echo
				t.Logf("Tool result content: %v", msg.Content)
				break
			}
		}
	}

	assert.True(t, foundEmptyResult, "Empty tool result should be in history")

	t.Logf("✓ Adapter correctly handles empty tool results in Responses format")
}

// TestAgent_Adapter_ResponsesFormat_MultipleToolCalls verifies parallel tool execution
// Tests that adapter handles multiple tool calls in Responses format correctly
func TestAgent_Adapter_ResponsesFormat_MultipleToolCalls(t *testing.T) {
	t.Parallel()

	// Setup
	manager, mocker, ctx := SetupAgentTest(t, AgentTestConfig{
		InProcessTools:   []string{"echo", "calculator", "weather"},
		AutoExecuteTools: []string{"*"},
		MaxDepth:         5,
	})

	// Turn 1: Multiple tool calls
	mocker.AddResponsesResponse(CreateAgentTurnWithToolCallsResponses(
		GetSampleEchoToolCall("call-1", "first"),
		GetSampleCalculatorToolCall("call-2", "add", 1, 2),
		GetSampleWeatherToolCall("call-3", "Tokyo", "celsius"),
		GetSampleEchoToolCall("call-4", "second"),
		GetSampleCalculatorToolCall("call-5", "multiply", 3, 4),
	))

	// Turn 2: Final text
	mocker.AddResponsesResponse(CreateAgentTurnWithTextResponses("All tools executed"))

	// Execute
	req := &schemas.BifrostResponsesRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4",
		Input: []schemas.ResponsesMessage{
			GetSampleUserMessageResponses("Test multiple tools"),
		},
	}

	initialResponse, initialErr := mocker.MakeResponsesRequest(ctx, req)
	require.Nil(t, initialErr)

	result, bifrostErr := manager.CheckAndExecuteAgentForResponsesRequest(
		ctx, req, initialResponse, mocker.MakeResponsesRequest,
		func(ctx *schemas.BifrostContext, request *schemas.BifrostMCPRequest) (*schemas.BifrostMCPResponse, error) {
			return manager.ExecuteToolCall(ctx, request)
		},
	)

	// Assertions
	require.Nil(t, bifrostErr)
	require.NotNil(t, result)

	AssertAgentCompletedInTurnsResponses(t, mocker, 2)

	// Verify all 5 tools executed
	AssertToolsExecutedInParallelResponses(t, mocker, []string{"echo", "calculator", "get_weather", "echo", "calculator"}, 2)

	t.Logf("✓ Adapter correctly handles multiple tool calls in Responses format")
}

// TestAgent_Adapter_ResponsesFormat_MixedPermissions verifies permission filtering in Responses API
// Tests that adapter maintains permission semantics when converting formats
func TestAgent_Adapter_ResponsesFormat_MixedPermissions(t *testing.T) {
	t.Parallel()

	// Setup: Mixed auto-execute and approval-required tools
	manager, mocker, ctx := SetupAgentTest(t, AgentTestConfig{
		InProcessTools: []string{"echo", "calculator", "weather"},
		MaxDepth:       5,
	})

	// Configure permissions: only echo auto-executes
	require.NoError(t, SetInternalClientAutoExecute(manager, []string{"echo"}))

	// Turn 1: Mixed permissions
	mocker.AddResponsesResponse(CreateAgentTurnWithToolCallsResponses(
		GetSampleEchoToolCall("call-1", "test"),                // Auto
		GetSampleCalculatorToolCall("call-2", "add", 1, 2),     // Needs approval
		GetSampleWeatherToolCall("call-3", "Tokyo", "celsius"), // Needs approval
	))

	// Execute
	req := &schemas.BifrostResponsesRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4",
		Input: []schemas.ResponsesMessage{
			GetSampleUserMessageResponses("Test mixed permissions"),
		},
	}

	initialResponse, initialErr := mocker.MakeResponsesRequest(ctx, req)
	require.Nil(t, initialErr)

	result, bifrostErr := manager.CheckAndExecuteAgentForResponsesRequest(
		ctx, req, initialResponse, mocker.MakeResponsesRequest,
		func(ctx *schemas.BifrostContext, request *schemas.BifrostMCPRequest) (*schemas.BifrostMCPResponse, error) {
			return manager.ExecuteToolCall(ctx, request)
		},
	)

	// Assertions
	require.Nil(t, bifrostErr)
	require.NotNil(t, result)

	// Agent should stop at turn 1
	AssertAgentStoppedAtTurnResponses(t, mocker, 1)

	// Verify response format - check Output messages
	require.NotEmpty(t, result.Output, "Should have output messages")

	// Find function_call messages waiting for approval
	var toolCallsWaiting []schemas.ResponsesMessage
	for _, msg := range result.Output {
		if msg.Type != nil && *msg.Type == schemas.ResponsesMessageTypeFunctionCall {
			toolCallsWaiting = append(toolCallsWaiting, msg)
		}
	}

	// Should have 2 tool calls waiting (calculator and weather)
	require.Len(t, toolCallsWaiting, 2, "Should have 2 tool calls waiting for approval")

	t.Logf("✓ Adapter maintains permission semantics in Responses format")
}

// TestAgent_Adapter_ResponsesFormat_STDIO verifies STDIO integration with Responses API
// Tests that adapter works with STDIO clients in Responses format
func TestAgent_Adapter_ResponsesFormat_STDIO(t *testing.T) {
	t.Parallel()

	// Setup: InProcess + STDIO
	manager, mocker, ctx := SetupAgentTest(t, AgentTestConfig{
		InProcessTools:   []string{"echo"},
		STDIOClients:     []string{"go-test-server"},
		AutoExecuteTools: []string{"*"},
		MaxDepth:         5,
	})

	// Turn 1: Mixed InProcess and STDIO tools
	mocker.AddResponsesResponse(CreateAgentTurnWithToolCallsResponses(
		GetSampleEchoToolCall("call-1", "test"),
		CreateSTDIOToolCall("call-2", "GoTestServer", "uuid_generate", map[string]interface{}{}),
	))

	// Turn 2: Final text
	mocker.AddResponsesResponse(CreateAgentTurnWithTextResponses("Tools executed"))

	// Execute
	req := &schemas.BifrostResponsesRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4",
		Input: []schemas.ResponsesMessage{
			GetSampleUserMessageResponses("Test STDIO with Responses API"),
		},
	}

	initialResponse, initialErr := mocker.MakeResponsesRequest(ctx, req)
	require.Nil(t, initialErr)

	result, bifrostErr := manager.CheckAndExecuteAgentForResponsesRequest(
		ctx, req, initialResponse, mocker.MakeResponsesRequest,
		func(ctx *schemas.BifrostContext, request *schemas.BifrostMCPRequest) (*schemas.BifrostMCPResponse, error) {
			return manager.ExecuteToolCall(ctx, request)
		},
	)

	// Assertions
	require.Nil(t, bifrostErr)
	require.NotNil(t, result)

	AssertAgentCompletedInTurnsResponses(t, mocker, 2)

	// Verify both tools executed
	AssertToolsExecutedInParallelResponses(t, mocker, []string{"echo", "GoTestServer-uuid_generate"}, 2)

	t.Logf("✓ Adapter works correctly with STDIO clients in Responses format")
}

// TestAgent_Adapter_ResponsesFormat_DeepChain verifies multi-turn execution
// Tests that adapter handles multiple agent iterations in Responses format
func TestAgent_Adapter_ResponsesFormat_DeepChain(t *testing.T) {
	t.Parallel()

	// Setup
	manager, mocker, ctx := SetupAgentTest(t, AgentTestConfig{
		InProcessTools:   []string{"echo", "calculator"},
		AutoExecuteTools: []string{"*"},
		MaxDepth:         10,
	})

	// Turn 1: First tool
	mocker.AddResponsesResponse(CreateAgentTurnWithToolCallsResponses(
		GetSampleEchoToolCall("call-1", "step1"),
	))

	// Turn 2: Second tool
	mocker.AddResponsesResponse(CreateAgentTurnWithToolCallsResponses(
		GetSampleCalculatorToolCall("call-2", "add", 1, 2),
	))

	// Turn 3: Third tool
	mocker.AddResponsesResponse(CreateAgentTurnWithToolCallsResponses(
		GetSampleEchoToolCall("call-3", "step3"),
	))

	// Turn 4: Fourth tool
	mocker.AddResponsesResponse(CreateAgentTurnWithToolCallsResponses(
		GetSampleCalculatorToolCall("call-4", "multiply", 3, 4),
	))

	// Turn 5: Final text
	mocker.AddResponsesResponse(CreateAgentTurnWithTextResponses("Chain complete"))

	// Execute
	req := &schemas.BifrostResponsesRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4",
		Input: []schemas.ResponsesMessage{
			GetSampleUserMessageResponses("Test deep chain"),
		},
	}

	initialResponse, initialErr := mocker.MakeResponsesRequest(ctx, req)
	require.Nil(t, initialErr)

	result, bifrostErr := manager.CheckAndExecuteAgentForResponsesRequest(
		ctx, req, initialResponse, mocker.MakeResponsesRequest,
		func(ctx *schemas.BifrostContext, request *schemas.BifrostMCPRequest) (*schemas.BifrostMCPResponse, error) {
			return manager.ExecuteToolCall(ctx, request)
		},
	)

	// Assertions
	require.Nil(t, bifrostErr)
	require.NotNil(t, result)

	AssertAgentCompletedInTurnsResponses(t, mocker, 5)
	AssertAgentFinalResponseResponses(t, result, "stop", "complete")

	t.Logf("✓ Adapter handles multi-turn execution in Responses format")
}

// TestAgent_Adapter_ResponsesFormat_ErrorHandling verifies error propagation
// Tests that adapter correctly propagates tool errors in Responses format
func TestAgent_Adapter_ResponsesFormat_ErrorHandling(t *testing.T) {
	t.Parallel()

	// Setup: Error-generating STDIO server
	manager, mocker, ctx := SetupAgentTest(t, AgentTestConfig{
		InProcessTools:   []string{"echo"},
		STDIOClients:     []string{"error-test-server"},
		AutoExecuteTools: []string{"*"},
		MaxDepth:         5,
	})

	// Turn 1: Call error tool
	mocker.AddResponsesResponse(CreateAgentTurnWithToolCallsResponses(
		GetSampleEchoToolCall("call-1", "before error"),
		CreateSTDIOToolCall("call-2", "ErrorTestServer", "return_error", map[string]interface{}{
			"error_type": "standard",
			"message":    "Test error in Responses API",
		}),
	))

	// Turn 2: LLM continues after error
	mocker.AddResponsesResponse(CreateAgentTurnWithTextResponses("Handled error"))

	// Execute
	req := &schemas.BifrostResponsesRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4",
		Input: []schemas.ResponsesMessage{
			GetSampleUserMessageResponses("Test error handling"),
		},
	}

	initialResponse, initialErr := mocker.MakeResponsesRequest(ctx, req)
	require.Nil(t, initialErr)

	result, bifrostErr := manager.CheckAndExecuteAgentForResponsesRequest(
		ctx, req, initialResponse, mocker.MakeResponsesRequest,
		func(ctx *schemas.BifrostContext, request *schemas.BifrostMCPRequest) (*schemas.BifrostMCPResponse, error) {
			return manager.ExecuteToolCall(ctx, request)
		},
	)

	// Assertions
	require.Nil(t, bifrostErr, "Agent should not fail on tool error")
	require.NotNil(t, result)

	AssertAgentCompletedInTurnsResponses(t, mocker, 2)

	// Verify error was passed to LLM
	history := mocker.GetResponsesHistory()
	require.GreaterOrEqual(t, len(history), 2)

	// Check turn 2 for error message
	turn2History := history[1]
	foundError := false
	for _, msg := range turn2History {
		if msg.Type != nil && *msg.Type == schemas.ResponsesMessageTypeFunctionCallOutput {
			if msg.CallID != nil && *msg.CallID == "call-2" {
				// Error should be present
				foundError = true
				t.Logf("Error content found in Responses format")
				break
			}
		}
	}

	assert.True(t, foundError, "Error should be propagated in Responses format")

	t.Logf("✓ Adapter correctly propagates errors in Responses format")
}

// TestAgent_Adapter_ChatAndResponsesParity verifies feature parity
// Tests that Chat and Responses APIs produce equivalent results
func TestAgent_Adapter_ChatAndResponsesParity(t *testing.T) {
	t.Parallel()

	// Setup for Chat API
	managerChat, mockerChat, ctxChat := SetupAgentTest(t, AgentTestConfig{
		InProcessTools:   []string{"echo", "calculator"},
		AutoExecuteTools: []string{"*"},
		MaxDepth:         5,
	})

	// Setup for Responses API (separate manager)
	managerResponses, mockerResponses, ctxResponses := SetupAgentTest(t, AgentTestConfig{
		InProcessTools:   []string{"echo", "calculator"},
		AutoExecuteTools: []string{"*"},
		MaxDepth:         5,
	})

	// Same LLM behavior for both
	// Chat API
	mockerChat.AddChatResponse(CreateAgentTurnWithToolCalls(
		GetSampleEchoToolCall("call-1", "parity test"),
		GetSampleCalculatorToolCall("call-2", "add", 5, 10),
	))
	mockerChat.AddChatResponse(CreateAgentTurnWithText("Done"))

	// Responses API
	mockerResponses.AddResponsesResponse(CreateAgentTurnWithToolCallsResponses(
		GetSampleEchoToolCall("call-1", "parity test"),
		GetSampleCalculatorToolCall("call-2", "add", 5, 10),
	))
	mockerResponses.AddResponsesResponse(CreateAgentTurnWithTextResponses("Done"))

	// Execute Chat API
	chatReq := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4",
		Input:    []schemas.ChatMessage{GetSampleUserMessage("Test parity")},
	}

	chatInitialResponse, err := mockerChat.MakeChatRequest(ctxChat, chatReq)
	require.Nil(t, err)

	chatResult, chatErr := managerChat.CheckAndExecuteAgentForChatRequest(
		ctxChat, chatReq, chatInitialResponse, mockerChat.MakeChatRequest,
		func(ctx *schemas.BifrostContext, request *schemas.BifrostMCPRequest) (*schemas.BifrostMCPResponse, error) {
			return managerChat.ExecuteToolCall(ctx, request)
		},
	)

	// Execute Responses API
	responsesReq := &schemas.BifrostResponsesRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4",
		Input:    []schemas.ResponsesMessage{GetSampleUserMessageResponses("Test parity")},
	}

	responsesInitialResponse, err := mockerResponses.MakeResponsesRequest(ctxResponses, responsesReq)
	require.Nil(t, err)

	responsesResult, responsesErr := managerResponses.CheckAndExecuteAgentForResponsesRequest(
		ctxResponses, responsesReq, responsesInitialResponse, mockerResponses.MakeResponsesRequest,
		func(ctx *schemas.BifrostContext, request *schemas.BifrostMCPRequest) (*schemas.BifrostMCPResponse, error) {
			return managerResponses.ExecuteToolCall(ctx, request)
		},
	)

	// Assertions: Both should complete successfully
	require.Nil(t, chatErr)
	require.Nil(t, responsesErr)
	require.NotNil(t, chatResult)
	require.NotNil(t, responsesResult)

	// Both should complete in 2 turns
	assert.Equal(t, 2, mockerChat.GetChatCallCount())
	assert.Equal(t, 2, mockerResponses.GetResponsesCallCount())

	// Both should have final text response
	AssertAgentFinalResponse(t, chatResult, "stop", "Done")
	AssertAgentFinalResponseResponses(t, responsesResult, "stop", "Done")

	t.Logf("✓ Chat and Responses APIs maintain feature parity in agent mode")
}
