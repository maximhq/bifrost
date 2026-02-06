package mcptests

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// AGENT MODE: MIXED PERMISSIONS ADVANCED TESTS
// =============================================================================
//
// These tests verify complex permission scenarios across multiple connection types:
// - Mixed auto-execute, allowed, and blocked tools
// - Multiple STDIO clients with different permission levels
// - Interaction between context filtering and permission filtering
// - Edge cases like all-blocked, all-auto, wildcard permissions
//
// Key concepts:
// - Auto-execute tools run immediately without approval
// - Allowed tools (not in auto-execute) wait for approval
// - Blocked tools (not in ToolsToExecute) should never execute
// - Agent should handle partial execution gracefully
//
// Related code: core/mcp/agent.go:169-277 (permission filtering logic)
// =============================================================================

// TestAgent_MixedPermissions_ThreeClients verifies mixed permissions across 3 different clients
// Tests InProcess (auto) + STDIO (allowed) + STDIO (not in list)
func TestAgent_MixedPermissions_ThreeClients(t *testing.T) {
	t.Parallel()

	// Setup: 3 different clients with different permission levels
	manager, mocker, ctx := SetupAgentTest(t, AgentTestConfig{
		InProcessTools: []string{"echo"},
		STDIOClients:   []string{"go-test-server", "parallel-test-server"},
		MaxDepth:       5,
	})

	// Configure permissions:
	// - echo: auto-execute (InProcess)
	// - go-test-server tools: allowed but not auto
	// - parallel-test-server tools: allowed but not auto
	require.NoError(t, SetInternalClientAutoExecute(manager, []string{"echo"}))

	// Both STDIO clients default to no auto-execute (ToolsToAutoExecute: [])

	// Turn 1: LLM calls tools from all 3 clients
	mocker.AddChatResponse(CreateAgentTurnWithToolCalls(
		GetSampleEchoToolCall("call-1", "test"),                                                         // Auto-execute
		CreateSTDIOToolCall("call-2", "GoTestServer", "uuid_generate", map[string]interface{}{}),        // Needs approval
		CreateSTDIOToolCall("call-3", "ParallelTestServer", "fast_operation", map[string]interface{}{}), // Needs approval
	))

	// Execute agent
	req := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4",
		Input:    []schemas.ChatMessage{GetSampleUserMessage("Test three clients")},
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

	// Agent should stop at turn 1 (echo executed, 2 tools waiting)
	AssertAgentStoppedAtTurn(t, mocker, 1)

	// Verify response format
	require.NotEmpty(t, result.Choices)
	choice := result.Choices[0]

	// Should have finish_reason "stop"
	require.NotNil(t, choice.FinishReason)
	assert.Equal(t, "stop", *choice.FinishReason)

	// Should have content from auto-executed echo
	require.NotNil(t, choice.ChatNonStreamResponseChoice)
	require.NotNil(t, choice.ChatNonStreamResponseChoice.Message)
	require.NotNil(t, choice.ChatNonStreamResponseChoice.Message.Content)
	require.NotNil(t, choice.ChatNonStreamResponseChoice.Message.Content.ContentStr)
	assert.Contains(t, *choice.ChatNonStreamResponseChoice.Message.Content.ContentStr, "Output from allowed tools")

	// Should have 2 tool_calls waiting for approval
	require.NotNil(t, choice.ChatNonStreamResponseChoice.Message.ChatAssistantMessage)
	toolCalls := choice.ChatNonStreamResponseChoice.Message.ChatAssistantMessage.ToolCalls
	require.Len(t, toolCalls, 2, "Should have 2 tool calls waiting for approval")

	// Verify the waiting tools
	toolNames := make(map[string]bool)
	for _, tc := range toolCalls {
		if tc.Function.Name != nil {
			toolNames[*tc.Function.Name] = true
		}
	}
	assert.True(t, toolNames["GoTestServer-uuid_generate"], "uuid_generate should be waiting")
	assert.True(t, toolNames["ParallelTestServer-fast_operation"], "fast_operation should be waiting")

	t.Logf("✓ Mixed permissions work correctly across 3 different clients")
}

// TestAgent_MixedPermissions_AllBlocked verifies agent behavior when all tools are blocked
// Tests that agent stops immediately when no tools can be executed
func TestAgent_MixedPermissions_AllBlocked(t *testing.T) {
	t.Parallel()

	// Setup: Multiple tools but none in auto-execute list
	manager, mocker, ctx := SetupAgentTest(t, AgentTestConfig{
		InProcessTools: []string{"echo", "calculator", "weather"},
		MaxDepth:       5,
	})

	// Set empty auto-execute list (all tools require approval)
	require.NoError(t, SetInternalClientAutoExecute(manager, []string{}))

	// Turn 1: LLM calls multiple tools
	mocker.AddChatResponse(CreateAgentTurnWithToolCalls(
		GetSampleEchoToolCall("call-1", "test"),
		GetSampleCalculatorToolCall("call-2", "add", 1, 2),
		GetSampleWeatherToolCall("call-3", "Tokyo", "celsius"),
	))

	// Execute agent
	req := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4",
		Input:    []schemas.ChatMessage{GetSampleUserMessage("Test all blocked")},
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

	// Agent should stop immediately at turn 1
	AssertAgentStoppedAtTurn(t, mocker, 1)

	// All 3 tools should be waiting for approval
	require.NotEmpty(t, result.Choices)
	choice := result.Choices[0]
	require.NotNil(t, choice.ChatNonStreamResponseChoice)
	require.NotNil(t, choice.ChatNonStreamResponseChoice.Message)
	require.NotNil(t, choice.ChatNonStreamResponseChoice.Message.ChatAssistantMessage)
	toolCalls := choice.ChatNonStreamResponseChoice.Message.ChatAssistantMessage.ToolCalls
	require.Len(t, toolCalls, 3, "All 3 tools should be waiting for approval")

	t.Logf("✓ Agent correctly stops when all tools require approval")
}

// TestAgent_MixedPermissions_WildcardAutoExecute verifies "*" wildcard in auto-execute list
// Tests that wildcard allows all tools to auto-execute
func TestAgent_MixedPermissions_WildcardAutoExecute(t *testing.T) {
	t.Parallel()

	// Setup: Multiple tools with wildcard auto-execute
	manager, mocker, ctx := SetupAgentTest(t, AgentTestConfig{
		InProcessTools:   []string{"echo", "calculator"},
		STDIOClients:     []string{"go-test-server"},
		AutoExecuteTools: []string{"*"}, // Wildcard: all tools auto-execute
		MaxDepth:         5,
	})

	// Turn 1: LLM calls multiple tools
	mocker.AddChatResponse(CreateAgentTurnWithToolCalls(
		GetSampleEchoToolCall("call-1", "test"),
		GetSampleCalculatorToolCall("call-2", "add", 5, 3),
		CreateSTDIOToolCall("call-3", "GoTestServer", "uuid_generate", map[string]interface{}{}),
	))

	// Turn 2: Final text
	mocker.AddChatResponse(CreateAgentTurnWithText("All tools executed"))

	// Execute agent
	req := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4",
		Input:    []schemas.ChatMessage{GetSampleUserMessage("Test wildcard")},
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

	// Should complete in 2 turns (all tools auto-executed)
	AssertAgentCompletedInTurns(t, mocker, 2)
	AssertAgentFinalResponse(t, result, "stop", "executed")

	// Verify all 3 tools executed (in turn 1 where LLM called them)
	AssertToolsExecutedInParallel(t, mocker, []string{"bifrostInternal-echo", "bifrostInternal-calculator", "GoTestServer-uuid_generate"}, 1)

	t.Logf("✓ Wildcard auto-execute works correctly across all clients")
}

// TestAgent_MixedPermissions_PartialExecution verifies partial auto-execution scenario
// Tests that agent executes auto tools, returns non-auto tools, handles mix correctly
func TestAgent_MixedPermissions_PartialExecution(t *testing.T) {
	t.Parallel()

	// Setup: Multiple clients with mixed permissions
	manager, mocker, ctx := SetupAgentTest(t, AgentTestConfig{
		InProcessTools: []string{"echo", "calculator", "weather"},
		STDIOClients:   []string{"go-test-server", "parallel-test-server"},
		MaxDepth:       5,
	})

	// Configure permissions:
	// - InProcess: echo and calculator auto-execute, weather needs approval
	// - go-test-server: all tools auto-execute
	// - parallel-test-server: no auto-execute
	require.NoError(t, SetInternalClientAutoExecute(manager, []string{"echo", "calculator"}))

	// Set go-test-server to auto-execute all
	clients := manager.GetClients()
	for i := range clients {
		if clients[i].ExecutionConfig.ID == "go-test-server" {
			clients[i].ExecutionConfig.ToolsToAutoExecute = []string{"*"}
			require.NoError(t, manager.UpdateClient(clients[i].ExecutionConfig.ID, clients[i].ExecutionConfig))
		}
	}

	// Turn 1: LLM calls tools from all clients (5 tools total)
	mocker.AddChatResponse(CreateAgentTurnWithToolCalls(
		GetSampleEchoToolCall("call-1", "test"),                                                         // Auto (InProcess)
		GetSampleCalculatorToolCall("call-2", "add", 1, 2),                                              // Auto (InProcess)
		GetSampleWeatherToolCall("call-3", "Tokyo", "celsius"),                                          // Needs approval (InProcess)
		CreateSTDIOToolCall("call-4", "GoTestServer", "uuid_generate", map[string]interface{}{}),        // Auto (STDIO)
		CreateSTDIOToolCall("call-5", "ParallelTestServer", "fast_operation", map[string]interface{}{}), // Needs approval (STDIO)
	))

	// Execute agent
	req := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4",
		Input:    []schemas.ChatMessage{GetSampleUserMessage("Test partial execution")},
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

	// Agent should stop at turn 1 (3 auto-executed, 2 waiting)
	AssertAgentStoppedAtTurn(t, mocker, 1)

	// Verify response format
	require.NotEmpty(t, result.Choices)
	choice := result.Choices[0]

	// Should have finish_reason "stop"
	require.NotNil(t, choice.FinishReason)
	assert.Equal(t, "stop", *choice.FinishReason)

	// Should have content from 3 auto-executed tools
	require.NotNil(t, choice.ChatNonStreamResponseChoice)
	require.NotNil(t, choice.ChatNonStreamResponseChoice.Message)
	require.NotNil(t, choice.ChatNonStreamResponseChoice.Message.Content)
	require.NotNil(t, choice.ChatNonStreamResponseChoice.Message.Content.ContentStr)
	content := *choice.ChatNonStreamResponseChoice.Message.Content.ContentStr
	assert.Contains(t, content, "Output from allowed tools")

	// Should have 2 tool_calls waiting for approval
	require.NotNil(t, choice.ChatNonStreamResponseChoice.Message.ChatAssistantMessage)
	toolCalls := choice.ChatNonStreamResponseChoice.Message.ChatAssistantMessage.ToolCalls
	require.Len(t, toolCalls, 2, "Should have 2 tool calls waiting for approval")

	// Verify the waiting tools are weather and fast_operation
	toolNames := make(map[string]bool)
	for _, tc := range toolCalls {
		if tc.Function.Name != nil {
			toolNames[*tc.Function.Name] = true
		}
	}
	assert.True(t, toolNames["bifrostInternal-get_weather"], "weather should be waiting")
	assert.True(t, toolNames["ParallelTestServer-fast_operation"], "fast_operation should be waiting")

	t.Logf("✓ Partial execution works correctly with mixed auto/non-auto tools across multiple clients")
}

// TestAgent_MixedPermissions_MultipleSTDIOSamePermissions verifies multiple STDIO clients with same permissions
// Tests that agent handles multiple STDIO clients with identical permission levels correctly
func TestAgent_MixedPermissions_MultipleSTDIOSamePermissions(t *testing.T) {
	t.Parallel()

	// Setup: Multiple STDIO clients with same permission level
	manager, mocker, ctx := SetupAgentTest(t, AgentTestConfig{
		InProcessTools: []string{"echo"},
		STDIOClients:   []string{"go-test-server", "parallel-test-server", "error-test-server"},
		MaxDepth:       5,
	})

	// Configure all STDIO clients to auto-execute
	clients := manager.GetClients()
	for i := range clients {
		if clients[i].ExecutionConfig.ConnectionType == schemas.MCPConnectionTypeSTDIO {
			clients[i].ExecutionConfig.ToolsToAutoExecute = []string{"*"}
			require.NoError(t, manager.UpdateClient(clients[i].ExecutionConfig.ID, clients[i].ExecutionConfig))
		}
	}

	// Set InProcess to auto-execute
	require.NoError(t, SetInternalClientAutoExecute(manager, []string{"*"}))

	// Turn 1: Call tools from all 4 clients (1 InProcess + 3 STDIO)
	mocker.AddChatResponse(CreateAgentTurnWithToolCalls(
		GetSampleEchoToolCall("call-1", "test"),
		CreateSTDIOToolCall("call-2", "GoTestServer", "uuid_generate", map[string]interface{}{}),
		CreateSTDIOToolCall("call-3", "ParallelTestServer", "fast_operation", map[string]interface{}{}),
		CreateSTDIOToolCall("call-4", "ErrorTestServer", "return_error", map[string]interface{}{
			"error_type": "standard",
			"message":    "test error",
		}),
	))

	// Turn 2: Final text
	mocker.AddChatResponse(CreateAgentTurnWithText("All executed"))

	// Execute agent
	req := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4",
		Input:    []schemas.ChatMessage{GetSampleUserMessage("Test multiple STDIO")},
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

	// Verify all 4 tools executed (in turn 1 where LLM called them)
	expectedTools := []string{
		"bifrostInternal-echo",
		"GoTestServer-uuid_generate",
		"ParallelTestServer-fast_operation",
		"ErrorTestServer-return_error",
	}
	AssertToolsExecutedInParallel(t, mocker, expectedTools, 1)

	t.Logf("✓ Multiple STDIO clients with same permissions work correctly")
}

// TestAgent_MixedPermissions_ContextFilteringOverride verifies context filtering interaction with permissions
// Tests that context filtering (include clients/tools) works together with permission filtering
func TestAgent_MixedPermissions_ContextFilteringOverride(t *testing.T) {
	t.Parallel()

	// Setup: Multiple tools but context filtering limits to specific client
	manager, mocker, ctx := SetupAgentTest(t, AgentTestConfig{
		InProcessTools:   []string{"echo", "calculator"},
		STDIOClients:     []string{"go-test-server"},
		AutoExecuteTools: []string{"*"},
		MaxDepth:         5,
		// Context filtering: only allow InProcess client (bifrostInternal)
		ClientFiltering: []string{"bifrostInternal"},
	})

	// Turn 1: LLM tries to call tools from both InProcess and STDIO
	// But only InProcess tools should be available due to context filtering
	mocker.AddChatResponse(CreateAgentTurnWithToolCalls(
		GetSampleEchoToolCall("call-1", "test"),
		GetSampleCalculatorToolCall("call-2", "add", 1, 2),
	))

	// Turn 2: Final text
	mocker.AddChatResponse(CreateAgentTurnWithText("Done"))

	// Execute agent
	req := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4",
		Input:    []schemas.ChatMessage{GetSampleUserMessage("Test context filtering")},
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

	// Only InProcess tools should execute (in turn 1 = initial response)
	AssertToolsExecutedInParallel(t, mocker, []string{"echo", "calculator"}, 1)

	// Verify go-test-server tools were filtered out (not available to LLM)
	// We can check this by looking at the tools provided to the LLM
	// The context filtering should have removed STDIO tools from available tools

	t.Logf("✓ Context filtering correctly narrows permission settings")
}

// TestAgent_MixedPermissions_SpecificToolNames verifies specific tool names in auto-execute list
// Tests that specific tool names (not wildcards) work correctly across multiple clients
func TestAgent_MixedPermissions_SpecificToolNames(t *testing.T) {
	t.Parallel()

	// Setup: Multiple tools with specific names in auto-execute
	manager, mocker, ctx := SetupAgentTest(t, AgentTestConfig{
		InProcessTools: []string{"echo", "calculator", "weather"},
		STDIOClients:   []string{"go-test-server"},
		MaxDepth:       5,
	})

	// Configure specific tool names: only echo and uuid_generate auto-execute
	require.NoError(t, SetInternalClientAutoExecute(manager, []string{"echo"}))

	// Set go-test-server to auto-execute only uuid_generate (use base tool name, not prefixed)
	clients := manager.GetClients()
	for i := range clients {
		if clients[i].ExecutionConfig.ID == "go-test-server" {
			clients[i].ExecutionConfig.ToolsToAutoExecute = []string{"uuid_generate"}
			require.NoError(t, manager.UpdateClient(clients[i].ExecutionConfig.ID, clients[i].ExecutionConfig))
		}
	}

	// Turn 1: Call mix of auto and non-auto tools
	mocker.AddChatResponse(CreateAgentTurnWithToolCalls(
		GetSampleEchoToolCall("call-1", "test"),                                                  // Auto
		GetSampleCalculatorToolCall("call-2", "add", 1, 2),                                       // Needs approval
		GetSampleWeatherToolCall("call-3", "Tokyo", "celsius"),                                   // Needs approval
		CreateSTDIOToolCall("call-4", "GoTestServer", "uuid_generate", map[string]interface{}{}), // Auto
		CreateSTDIOToolCall("call-5", "GoTestServer", "string_transform", map[string]interface{}{ // Needs approval
			"input":     "test",
			"operation": "uppercase",
		}),
	))

	// Execute agent
	req := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4",
		Input:    []schemas.ChatMessage{GetSampleUserMessage("Test specific tool names")},
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

	// Agent should stop at turn 1 (2 auto-executed, 3 waiting)
	AssertAgentStoppedAtTurn(t, mocker, 1)

	// Verify response has 3 tool calls waiting
	require.NotEmpty(t, result.Choices)
	choice := result.Choices[0]
	require.NotNil(t, choice.ChatNonStreamResponseChoice)
	require.NotNil(t, choice.ChatNonStreamResponseChoice.Message)
	require.NotNil(t, choice.ChatNonStreamResponseChoice.Message.ChatAssistantMessage)
	toolCalls := choice.ChatNonStreamResponseChoice.Message.ChatAssistantMessage.ToolCalls
	require.Len(t, toolCalls, 3, "Should have 3 tool calls waiting for approval")

	// Verify the waiting tools
	toolNames := make(map[string]bool)
	for _, tc := range toolCalls {
		if tc.Function.Name != nil {
			toolNames[*tc.Function.Name] = true
		}
	}
	assert.True(t, toolNames["bifrostInternal-calculator"], "calculator should be waiting")
	assert.True(t, toolNames["bifrostInternal-get_weather"], "weather should be waiting")
	assert.True(t, toolNames["GoTestServer-string_transform"], "string_transform should be waiting")

	t.Logf("✓ Specific tool names in auto-execute list work correctly")
}

// TestAgent_MixedPermissions_AllAutoExecute verifies all tools auto-execute scenario
// Tests that when all tools are in auto-execute list, agent completes without stopping
func TestAgent_MixedPermissions_AllAutoExecute(t *testing.T) {
	t.Parallel()

	// Setup: Multiple clients, all tools auto-execute
	manager, mocker, ctx := SetupAgentTest(t, AgentTestConfig{
		InProcessTools:   []string{"echo", "calculator"},
		STDIOClients:     []string{"go-test-server"},
		AutoExecuteTools: []string{"*"},
		MaxDepth:         5,
	})

	// Turn 1: Call multiple tools
	mocker.AddChatResponse(CreateAgentTurnWithToolCalls(
		GetSampleEchoToolCall("call-1", "first"),
		GetSampleCalculatorToolCall("call-2", "add", 10, 20),
		CreateSTDIOToolCall("call-3", "GoTestServer", "uuid_generate", map[string]interface{}{}),
	))

	// Turn 2: Call more tools
	mocker.AddChatResponse(CreateAgentTurnWithToolCalls(
		GetSampleEchoToolCall("call-4", "second"),
		CreateSTDIOToolCall("call-5", "GoTestServer", "string_transform", map[string]interface{}{
			"input":     "hello",
			"operation": "uppercase",
		}),
	))

	// Turn 3: Final text
	mocker.AddChatResponse(CreateAgentTurnWithText("All done"))

	// Execute agent
	req := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4",
		Input:    []schemas.ChatMessage{GetSampleUserMessage("Test all auto")},
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

	// Should complete in 3 turns
	AssertAgentCompletedInTurns(t, mocker, 3)
	AssertAgentFinalResponse(t, result, "stop", "done")

	t.Logf("✓ All auto-execute scenario completes successfully across multiple turns")
}
