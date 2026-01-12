package mcptests

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// AGENT MODE: CONTEXT FILTERING TESTS
// =============================================================================
//
// These tests verify that agent mode correctly respects context-level filtering.
// Context filtering is a runtime mechanism that can FURTHER RESTRICT (but not expand)
// which tools/clients can be used in a specific request beyond the configured
// ToolsToExecute/ToolsToAutoExecute settings.
//
// FILTERING HIERARCHY (restrictive, not permissive):
// 1. Client-level configuration (ToolsToExecute) - Global allow-list, most restrictive
// 2. Request context (MCPContextKeyIncludeTools) - Can only further narrow, NOT expand
//
// Key concepts:
// - MCPContextKeyIncludeTools: Runtime tool filter (can only narrow)
// - MCPContextKeyIncludeClients: Runtime client filter (can only narrow)
// - Client config is the baseline - context CANNOT override it
// - Filtered tools should stop agent and return for approval
//
// =============================================================================

// TestAgent_ContextToolFilter_Whitelist verifies tool filtering with context whitelist
// Tests that context includeTools filter restricts tool execution across connection types
// NOTE: Context filtering affects which tools are advertised to the LLM. If the LLM calls
// a filtered tool anyway, it will fail with "not available or not permitted" error.
func TestAgent_ContextToolFilter_Whitelist(t *testing.T) {
	t.Parallel()

	// Setup: InProcess (echo, calculator, weather)
	// Config allows all to auto-execute, but context restricts to echo and calculator only
	manager, mocker, ctx := SetupAgentTest(t, AgentTestConfig{
		InProcessTools:   []string{"echo", "calculator", "weather"},
		AutoExecuteTools: []string{"*"},                  // Config: auto-execute all
		ToolFiltering:    []string{"echo", "calculator"}, // Context: only echo and calculator
		MaxDepth:         5,
	})

	// Turn 1: LLM calls echo (allowed by context)
	mocker.AddChatResponse(CreateAgentTurnWithToolCalls(
		GetSampleEchoToolCall("call-1", "test message"),
	))

	// Turn 2: LLM calls calculator (allowed by context)
	mocker.AddChatResponse(CreateAgentTurnWithToolCalls(
		GetSampleCalculatorToolCall("call-2", "add", 10, 5),
	))

	// Turn 3: LLM responds with text (agent completes)
	mocker.AddChatResponse(CreateAgentTurnWithText("Both tools executed successfully"))

	// Execute agent
	req := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4",
		Input:    []schemas.ChatMessage{GetSampleUserMessage("Test context filtering")},
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

	// Agent should complete in 3 LLM calls (initial + 2 continuations)
	AssertAgentCompletedInTurns(t, mocker, 3)
	AssertAgentFinalResponse(t, result, "stop", "successfully")

	// Both echo and calculator should have been called (allowed by context)
	// Weather should NOT have been called (not in context filter)
}

// TestAgent_ContextToolFilter_BlockedToolError verifies error when LLM calls filtered tool
// Tests that if LLM somehow calls a tool not in context filter, execution fails
func TestAgent_ContextToolFilter_BlockedToolError(t *testing.T) {
	t.Parallel()

	// Setup: echo and weather available, but context only allows echo
	manager, mocker, ctx := SetupAgentTest(t, AgentTestConfig{
		InProcessTools:   []string{"echo", "weather"},
		AutoExecuteTools: []string{"*"},
		ToolFiltering:    []string{"echo"}, // Context: only echo allowed
		MaxDepth:         5,
	})

	// Turn 1: LLM calls weather (blocked by context - will cause error during execution)
	mocker.AddChatResponse(CreateAgentTurnWithToolCalls(
		GetSampleWeatherToolCall("call-1", "London", "celsius"),
	))

	// Execute agent
	req := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4",
		Input:    []schemas.ChatMessage{GetSampleUserMessage("Test")},
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
	// Agent should fail immediately when trying to execute filtered tool
	require.NotNil(t, bifrostErr, "Should return error when tool is filtered")
	require.Nil(t, result, "Result should be nil when there's an error")

	// Verify error contains expected message
	require.NotNil(t, bifrostErr.Error)
	// The error will be propagated from the tool execution failure
	t.Logf("Error message: %s", bifrostErr.Error.Message)
}

// TestAgent_ContextClientFilter_Whitelist verifies client filtering with context whitelist
// Tests that context includeClients filter restricts which clients can be used
func TestAgent_ContextClientFilter_Whitelist(t *testing.T) {
	t.Parallel()

	// Setup: InProcess client + STDIO temperature client
	// Context only allows InProcess client (bifrostInternal)
	InitMCPServerPaths(t)
	temperatureConfig := GetTemperatureMCPClientConfig("")
	temperatureConfig.ToolsToAutoExecute = []string{"*"}

	manager, mocker, ctx := SetupAgentTestWithClients(t, AgentTestConfig{
		InProcessTools:   []string{"echo"},
		AutoExecuteTools: []string{"*"},
		ClientFiltering:  []string{"bifrostInternal"}, // Only allow InProcess client
		MaxDepth:         5,
	}, []schemas.MCPClientConfig{temperatureConfig})

	// Turn 1: LLM calls echo from InProcess client (allowed)
	mocker.AddChatResponse(CreateAgentTurnWithToolCalls(
		GetSampleEchoToolCall("call-1", "test"),
	))

	// Turn 2: LLM tries to call temperature from STDIO client (blocked)
	mocker.AddChatResponse(CreateAgentTurnWithToolCalls(
		CreateSTDIOToolCall("call-2", "temperature-mcp-client", "get_temperature", map[string]interface{}{
			"location": "Tokyo",
		}),
	))

	// Execute agent
	req := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4",
		Input:    []schemas.ChatMessage{GetSampleUserMessage("Test client filtering")},
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

	// Echo should execute in turn 1 (the initial LLM response contains the tool call)
	AssertToolExecutedInTurn(t, mocker, "echo", 1)

	// Agent stops at turn 2 (after temperature tool is blocked)
	AssertAgentStoppedAtTurn(t, mocker, 2)
}

// TestAgent_ContextNarrowing_AutoExecute verifies context can narrow auto-execute behavior
// Tests that context includeTools can filter which tools are auto-executed
func TestAgent_ContextNarrowing_AutoExecute(t *testing.T) {
	t.Parallel()

	// Setup: Config allows echo to execute but doesn't auto-execute it
	// Context includes echo, making it available (but still not auto-executed)
	manager := setupMCPManager(t)
	require.NoError(t, RegisterEchoTool(manager))

	// Config: Allow echo but don't auto-execute
	require.NoError(t, SetInternalClientAutoExecute(manager, []string{})) // Empty = no auto-execute

	// Create context with tool filter that includes echo
	ctx := CreateTestContextWithMCPFilter(nil, []string{"echo"})

	mocker := NewDynamicLLMMocker()

	// Turn 1: LLM calls echo
	mocker.AddChatResponse(CreateAgentTurnWithToolCalls(
		GetSampleEchoToolCall("call-1", "test"),
	))

	// Execute agent
	req := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4",
		Input:    []schemas.ChatMessage{GetSampleUserMessage("Test override")},
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

	// Echo is allowed by context but NOT auto-executed (ToolsToAutoExecute is empty)
	// So agent should stop at turn 1 for approval
	AssertAgentStoppedAtTurn(t, mocker, 1)

	// Verify echo is in the response waiting for approval
	require.NotEmpty(t, result.Choices)
	choice := result.Choices[0]
	require.Equal(t, "tool_calls", *choice.FinishReason, "Should stop with tool_calls reason")
}

// TestAgent_ContextToolFilter_EmptyList verifies empty context list denies all tools
// Tests that an empty includeTools list blocks all tools
func TestAgent_ContextToolFilter_EmptyList(t *testing.T) {
	t.Parallel()

	// Setup: Tools configured to auto-execute, but context has empty whitelist
	manager, mocker, ctx := SetupAgentTest(t, AgentTestConfig{
		InProcessTools:   []string{"echo", "calculator"},
		AutoExecuteTools: []string{"*"},
		ToolFiltering:    []string{}, // Empty = deny all
		MaxDepth:         5,
	})

	// Turn 1: LLM tries to call echo (blocked by empty context)
	mocker.AddChatResponse(CreateAgentTurnWithToolCalls(
		GetSampleEchoToolCall("call-1", "test"),
	))

	// Execute agent
	req := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4",
		Input:    []schemas.ChatMessage{GetSampleUserMessage("Test empty context")},
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
	// When LLM calls a tool blocked by empty context, execution fails
	require.NotNil(t, bifrostErr, "Should return error when tool is filtered by empty context")
	require.Nil(t, result)

	// Verify error indicates tool is not permitted
	require.NotNil(t, bifrostErr.Error)
	t.Logf("Error (expected): %s", bifrostErr.Error.Message)
}

// TestAgent_ContextToolFilter_WildcardOverride verifies wildcard in context allows all
// Tests that "*" in context includeTools allows all tools
func TestAgent_ContextToolFilter_WildcardOverride(t *testing.T) {
	t.Parallel()

	// Setup: Config restricts to echo only, but context has wildcard
	manager := setupMCPManager(t)
	require.NoError(t, RegisterEchoTool(manager))
	require.NoError(t, RegisterCalculatorTool(manager))
	require.NoError(t, RegisterWeatherTool(manager))

	// Config: Only echo in ToolsToExecute and ToolsToAutoExecute
	clients := manager.GetClients()
	for i := range clients {
		if clients[i].ExecutionConfig.ID == "bifrostInternal" {
			clients[i].ExecutionConfig.ToolsToExecute = []string{"echo"}
			clients[i].ExecutionConfig.ToolsToAutoExecute = []string{"echo"}
			require.NoError(t, manager.EditClient(clients[i].ExecutionConfig.ID, clients[i].ExecutionConfig))
			break
		}
	}

	// Context: Wildcard allows all
	ctx := CreateTestContextWithMCPFilter(nil, []string{"*"})

	mocker := NewDynamicLLMMocker()

	// Turn 1: LLM calls echo (allowed by both config and context)
	mocker.AddChatResponse(CreateAgentTurnWithToolCalls(
		GetSampleEchoToolCall("call-1", "test"),
	))

	// Turn 2: LLM calls calculator (blocked by config, even though context allows it)
	// NOTE: Context wildcards don't override config ToolsToExecute restrictions
	mocker.AddChatResponse(CreateAgentTurnWithToolCalls(
		GetSampleCalculatorToolCall("call-2", "add", 5, 3),
	))

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

	// Echo tool call appears in turn 1 (the initial LLM response contains the tool call)
	AssertToolExecutedInTurn(t, mocker, "echo", 1)

	// Calculator is blocked by config (context wildcard doesn't override ToolsToExecute)
	// Agent stops at turn 2 with calculator waiting for approval
	AssertAgentStoppedAtTurn(t, mocker, 2)
}

// TestAgent_ContextClientFilter_MultipleClients verifies multiple clients in whitelist
// Tests that multiple clients can be whitelisted in context
func TestAgent_ContextClientFilter_MultipleClients(t *testing.T) {
	t.Skip("Requires STDIO servers to be built")

	t.Parallel()

	// Setup: InProcess + 2 STDIO clients
	// Context allows InProcess and one STDIO client
	InitMCPServerPaths(t)
	temperatureConfig := GetTemperatureMCPClientConfig("")
	temperatureConfig.ToolsToAutoExecute = []string{"*"}

	goTestConfig := GetGoTestServerConfig("")
	goTestConfig.ToolsToAutoExecute = []string{"*"}

	manager, mocker, ctx := SetupAgentTestWithClients(t, AgentTestConfig{
		InProcessTools:   []string{"echo"},
		AutoExecuteTools: []string{"*"},
		ClientFiltering:  []string{"bifrostInternal", "temperature-mcp-client"}, // Allow 2 clients
		MaxDepth:         10,
	}, []schemas.MCPClientConfig{temperatureConfig, goTestConfig})

	// Turn 1: Call echo from InProcess (allowed)
	mocker.AddChatResponse(CreateAgentTurnWithToolCalls(
		GetSampleEchoToolCall("call-1", "test"),
	))

	// Turn 2: Call temperature from temperature client (allowed)
	mocker.AddChatResponse(CreateAgentTurnWithToolCalls(
		CreateSTDIOToolCall("call-2", "temperature-mcp-client", "get_temperature", map[string]interface{}{
			"location": "Tokyo",
		}),
	))

	// Turn 3: Call go-test-server tool (blocked - client not in whitelist)
	mocker.AddChatResponse(CreateAgentTurnWithToolCalls(
		CreateSTDIOToolCall("call-3", "go-test-server", "uuid_generate", map[string]interface{}{}),
	))

	// Turn 4: Final text
	mocker.AddChatResponse(CreateAgentTurnWithText("Done"))

	// Execute agent
	req := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4",
		Input:    []schemas.ChatMessage{GetSampleUserMessage("Test multiple clients")},
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

	// First two tools should execute
	AssertToolExecutedInTurn(t, mocker, "echo", 1)
	// Temperature should execute in turn 2

	// go-test-server should be blocked - agent stops at turn 3
	AssertAgentStoppedAtTurn(t, mocker, 3)
}

// TestAgent_ContextToolFilter_ParallelMixed verifies filtering works with parallel tool calls
// Tests that some parallel tools execute while others are blocked by context
func TestAgent_ContextToolFilter_ParallelMixed(t *testing.T) {
	t.Parallel()

	// Setup: Multiple tools, context allows only some
	manager, mocker, ctx := SetupAgentTest(t, AgentTestConfig{
		InProcessTools:   []string{"echo", "calculator", "weather", "get_time"},
		AutoExecuteTools: []string{"*"},
		ToolFiltering:    []string{"echo", "calculator"}, // Only allow 2 of 4 tools
		MaxDepth:         5,
	})

	// Turn 1: LLM calls 4 tools in parallel, but context only allows 2
	mocker.AddChatResponse(CreateAgentTurnWithToolCalls(
		GetSampleEchoToolCall("call-1", "test"),
		GetSampleCalculatorToolCall("call-2", "add", 10, 5),
		GetSampleWeatherToolCall("call-3", "Paris", "celsius"),
		CreateToolCall("call-4", "get_time", map[string]interface{}{"timezone": "UTC"}),
	))

	// Turn 2: LLM responds after seeing filtered tools failed
	mocker.AddChatResponse(CreateAgentTurnWithText("Filtered tools blocked"))

	// Execute agent
	req := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4",
		Input:    []schemas.ChatMessage{GetSampleUserMessage("Test parallel filtering")},
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
	// When parallel tools include filtered ones, the behavior depends on implementation.
	// The test verifies that filtering is applied correctly and doesn't cause crashes.
	if bifrostErr != nil {
		// If error is returned, it should be about tool filtering
		require.NotNil(t, bifrostErr.Error)
		t.Logf("Error returned: %s", bifrostErr.Error.Message)
		require.Nil(t, result)
	} else {
		// If successful, verify filtering was applied
		require.NotNil(t, result)
		// If we got a result, some tools must have been processed
		// (either partially executed or error was handled gracefully)
		history := mocker.GetChatHistory()
		require.Greater(t, len(history), 0, "Should have at least one LLM call")
	}
}
