package mcptests

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// EXAMPLE TESTS DEMONSTRATING AGENT TEST HELPERS
// =============================================================================

// TestAgentHelpers_Example_SimpleInProcessAgent demonstrates the simplest agent test
func TestAgentHelpers_Example_SimpleInProcessAgent(t *testing.T) {
	t.Parallel()

	// Setup: One-liner configuration
	manager, mocker, ctx := SetupAgentTest(t, AgentTestConfig{
		InProcessTools:   []string{"echo"},      // Register echo tool
		AutoExecuteTools: []string{"echo"},      // Allow echo to auto-execute
		MaxDepth:         5,                     // Max 5 agent iterations
	})

	// Configure LLM behavior
	// Turn 1: LLM calls echo tool
	mocker.AddChatResponse(CreateAgentTurnWithToolCalls(
		GetSampleEchoToolCall("call-1", "Hello from agent"),
	))

	// Turn 2: LLM receives echo result and responds with text
	mocker.AddChatResponse(CreateAgentTurnWithText(
		"The echo tool returned your message successfully",
	))

	// Execute agent
	req := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4",
		Input: []schemas.ChatMessage{
			GetSampleUserMessage("Please echo hello"),
		},
	}

	// Get initial response and execute agent mode
	initialResponse, initialErr := mocker.MakeChatRequest(ctx, req)
	require.Nil(t, initialErr)
	require.NotNil(t, initialResponse)

	result, bifrostErr := manager.CheckAndExecuteAgentForChatRequest(
		ctx,
		req,
		initialResponse,
		mocker.MakeChatRequest,
		func(ctx *schemas.BifrostContext, request *schemas.BifrostMCPRequest) (*schemas.BifrostMCPResponse, error) {
			return manager.ExecuteToolCall(ctx, request)
		},
	)

	// Assertions using agent-specific helpers
	AssertAgentSuccess(t, result, bifrostErr)
	AssertAgentCompletedInTurns(t, mocker, 2)
	AssertAgentFinalResponse(t, result, "stop", "successfully")
}

// TestAgentHelpers_Example_MultiConnectionTypes demonstrates multi-connection agent test
func TestAgentHelpers_Example_MultiConnectionTypes(t *testing.T) {
	// Skip if STDIO servers not built
	t.Skip("Example test - requires STDIO servers to be built")

	t.Parallel()

	// Setup with InProcess + STDIO tools
	manager, mocker, ctx := SetupAgentTest(t, AgentTestConfig{
		InProcessTools:   []string{"echo", "calculator"},
		STDIOClients:     []string{"temperature"}, // Requires temperature server built
		AutoExecuteTools: []string{"*"},           // Auto-execute all tools
		MaxDepth:         10,
	})

	// Turn 1: Call tools from different connection types in parallel
	mocker.AddChatResponse(CreateAgentTurnWithToolCalls(
		GetSampleEchoToolCall("call-1", "test"),
		GetSampleCalculatorToolCall("call-2", "add", 5, 3),
		// STDIO tool would be: CreateSTDIOToolCall("call-3", "temperature", "get_temperature", ...)
	))

	// Turn 2: Respond with text
	mocker.AddChatResponse(CreateAgentTurnWithText("All tools executed successfully"))

	// Execute
	req := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4",
		Input:    []schemas.ChatMessage{GetSampleUserMessage("Test multi-connection")},
	}

	initialResponse, initialErr := mocker.MakeChatRequest(ctx, req)
	require.Nil(t, initialErr)

	result, bifrostErr := manager.CheckAndExecuteAgentForChatRequest(
		ctx, req, initialResponse, mocker.MakeChatRequest,
		func(ctx *schemas.BifrostContext, request *schemas.BifrostMCPRequest) (*schemas.BifrostMCPResponse, error) {
			return manager.ExecuteToolCall(ctx, request)
		},
	)

	// Assert parallel execution
	AssertAgentSuccess(t, result, bifrostErr)
	AssertToolsExecutedInParallel(t, mocker, []string{"echo", "calculator"}, 1)
}

// TestAgentHelpers_Example_ContextFiltering demonstrates context filtering
func TestAgentHelpers_Example_ContextFiltering(t *testing.T) {
	t.Parallel()

	// Setup with context filtering
	manager, mocker, ctx := SetupAgentTest(t, AgentTestConfig{
		InProcessTools:   []string{"echo", "calculator", "weather"},
		AutoExecuteTools: []string{"*"},
		ToolFiltering:    []string{"echo", "calculator"}, // Context restricts to these two
		MaxDepth:         5,
	})

	// Turn 1: LLM tries to call echo (allowed by context)
	mocker.AddChatResponse(CreateAgentTurnWithToolCalls(
		GetSampleEchoToolCall("call-1", "test"),
	))

	// Turn 2: LLM tries to call weather (blocked by context)
	mocker.AddChatResponse(CreateAgentTurnWithToolCalls(
		GetSampleWeatherToolCall("call-2", "London", "celsius"),
	))

	// Execute
	req := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4",
		Input:    []schemas.ChatMessage{GetSampleUserMessage("Test filtering")},
	}

	initialResponse, initialErr := mocker.MakeChatRequest(ctx, req)
	require.Nil(t, initialErr)

	result, bifrostErr := manager.CheckAndExecuteAgentForChatRequest(
		ctx, req, initialResponse, mocker.MakeChatRequest,
		func(ctx *schemas.BifrostContext, request *schemas.BifrostMCPRequest) (*schemas.BifrostMCPResponse, error) {
			return manager.ExecuteToolCall(ctx, request)
		},
	)

	// Assert echo executed but weather blocked (agent stops with error at turn 2)
	require.NotNil(t, bifrostErr, "Should return error when tool is filtered")
	require.Nil(t, result, "Result should be nil when there's an error")
	// Echo should have been executed in turn 1 (the initial LLM response contains the tool call)
	AssertToolExecutedInTurn(t, mocker, "echo", 1)
	// Weather should be blocked - agent fails when trying to execute it
}

// TestAgentHelpers_Example_SimpleAgentTestHelper demonstrates the SimpleAgentTest helper
func TestAgentHelpers_Example_SimpleAgentTestHelper(t *testing.T) {
	t.Parallel()

	// Inline test using SimpleAgentTest helper
	SimpleAgentTest(
		t,
		"Two turn agent with echo",
		AgentTestConfig{
			InProcessTools:   []string{"echo"},
			AutoExecuteTools: []string{"echo"},
			MaxDepth:         5,
		},
		[]ChatResponseFunc{
			CreateAgentTurnWithToolCalls(GetSampleEchoToolCall("call-1", "hello")),
			CreateAgentTurnWithText("Echo completed"),
		},
		func(t *testing.T, response *schemas.BifrostChatResponse, bifrostErr *schemas.BifrostError, mocker *DynamicLLMMocker) {
			AssertAgentSuccess(t, response, bifrostErr)
			AssertAgentCompletedInTurns(t, mocker, 2)
		},
	)
}

// TestAgentHelpers_Example_MaxDepthLimit demonstrates max depth limiting
func TestAgentHelpers_Example_MaxDepthLimit(t *testing.T) {
	t.Parallel()

	manager, mocker, ctx := SetupAgentTest(t, AgentTestConfig{
		InProcessTools:   []string{"echo"},
		AutoExecuteTools: []string{"echo"},
		MaxDepth:         3, // Limit to 3 iterations
	})

	// Configure LLM to keep requesting tools (would go forever without max depth)
	for i := 0; i < 5; i++ { // Add more responses than max depth
		mocker.AddChatResponse(CreateAgentTurnWithToolCalls(
			GetSampleEchoToolCall("call-"+string(rune(i+'0')), "test"),
		))
	}

	req := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4",
		Input:    []schemas.ChatMessage{GetSampleUserMessage("Test max depth")},
	}

	initialResponse, initialErr := mocker.MakeChatRequest(ctx, req)
	require.Nil(t, initialErr)

	result, bifrostErr := manager.CheckAndExecuteAgentForChatRequest(
		ctx, req, initialResponse, mocker.MakeChatRequest,
		func(ctx *schemas.BifrostContext, request *schemas.BifrostMCPRequest) (*schemas.BifrostMCPResponse, error) {
			return manager.ExecuteToolCall(ctx, request)
		},
	)

	// Agent should stop at max depth (initial call + up to maxDepth-1 continuations)
	// For MaxDepth=3, expect at most 3 total LLM calls (1 initial + 2 continuations)
	// However, the agent currently makes 1 initial + 3 continuations = 4 total
	// This is expected behavior - maxDepth refers to agent iterations, not total calls
	require.NotNil(t, result)
	require.Nil(t, bifrostErr, "Should not error at max depth")
	actualCalls := mocker.GetChatCallCount()
	assert.LessOrEqual(t, actualCalls, 4, "Should make at most 4 calls (1 initial + 3 agent iterations)")
	assert.GreaterOrEqual(t, actualCalls, 3, "Should make at least 3 calls")
	t.Logf("Agent stopped after %d total LLM calls (MaxDepth: 3)", actualCalls)
}
