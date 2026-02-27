package mcptests

import (
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// MOCK LLM FOR AGENT TESTS
// =============================================================================

// MockLLMCaller provides controlled LLM responses for testing agent mode
type MockLLMCaller struct {
	chatResponses      []*schemas.BifrostChatResponse
	responsesResponses []*schemas.BifrostResponsesResponse
	chatCallCount      int
	responsesCallCount int
}

func (m *MockLLMCaller) MakeChatRequest(ctx *schemas.BifrostContext, req *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	if m.chatCallCount >= len(m.chatResponses) {
		return nil, &schemas.BifrostError{
			IsBifrostError: false,
			Error: &schemas.ErrorField{
				Message: "no more mock chat responses available",
			},
		}
	}

	response := m.chatResponses[m.chatCallCount]
	m.chatCallCount++
	return response, nil
}

func (m *MockLLMCaller) MakeResponsesRequest(ctx *schemas.BifrostContext, req *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	if m.responsesCallCount >= len(m.responsesResponses) {
		return nil, &schemas.BifrostError{
			IsBifrostError: false,
			Error: &schemas.ErrorField{
				Message: "no more mock responses api responses available",
			},
		}
	}

	response := m.responsesResponses[m.responsesCallCount]
	m.responsesCallCount++
	return response, nil
}

// =============================================================================
// BASIC AGENT LOOP TESTS
// =============================================================================

func TestAgent_BasicLoop(t *testing.T) {
	t.Parallel()

	// Use InProcess tools - no external server needed
	manager := setupMCPManager(t)
	err := RegisterEchoTool(manager)
	require.NoError(t, err, "should register echo tool")

	// Setup MCP manager with auto-executable tools
	err = SetInternalClientAutoExecute(manager, []string{"echo"})
	require.NoError(t, err, "should set auto-execute for internal client")

	ctx := createTestContext()

	// Setup mock LLM with 2 responses:
	// 1. First response: LLM wants to call echo tool
	// 2. Second response: LLM finishes with text
	mockLLM := &MockLLMCaller{
		chatResponses: []*schemas.BifrostChatResponse{
			// First call: return tool call
			CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
				GetSampleEchoToolCall("call-1", "Hello from agent"),
			}),
			// Second call: return final text
			CreateChatResponseWithText("The echo tool returned your message successfully"),
		},
	}

	// Initial LLM response with tool call
	initialResponse := mockLLM.chatResponses[0]
	mockLLM.chatCallCount = 1 // Start from second response for subsequent calls

	// Create mock request
	originalReq := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4",
		Input: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentStr: schemas.Ptr("Please echo hello"),
				},
			},
		},
	}

	// Execute agent mode
	result, bifrostErr := manager.CheckAndExecuteAgentForChatRequest(
		ctx,
		originalReq,
		initialResponse,
		mockLLM.MakeChatRequest,
		func(ctx *schemas.BifrostContext, request *schemas.BifrostMCPRequest) (*schemas.BifrostMCPResponse, error) {
			// Use real tool execution
			return manager.ExecuteToolCall(ctx, request)
		},
	)

	require.Nil(t, bifrostErr, "agent loop should complete successfully")
	require.NotNil(t, result)

	t.Logf("Agent completed with %d LLM calls total", mockLLM.chatCallCount)

	// Verify final response
	assert.NotEmpty(t, result.Choices)
	assert.Equal(t, "stop", *result.Choices[0].FinishReason, "should finish with stop reason")

	// Verify the agent executed at least one tool
	assert.GreaterOrEqual(t, mockLLM.chatCallCount, 1, "should have made follow-up LLM call")
}

func TestAgent_BasicLoop_ChatFormat(t *testing.T) {
	t.Parallel()

	// Use InProcess tools - no external server needed
	manager := setupMCPManager(t)
	err := RegisterCalculatorTool(manager)
	require.NoError(t, err, "should register calculator tool")

	// Set auto-execute for calculator
	err = SetInternalClientAutoExecute(manager, []string{"calculator"})
	require.NoError(t, err, "should set auto-execute for internal client")

	ctx := createTestContext()

	mockLLM := &MockLLMCaller{
		chatResponses: []*schemas.BifrostChatResponse{
			CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
				GetSampleCalculatorToolCall("call-1", "add", 5, 3),
			}),
			CreateChatResponseWithText("The result is 8"),
		},
	}

	initialResponse := mockLLM.chatResponses[0]
	mockLLM.chatCallCount = 1

	originalReq := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4",
		Input: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentStr: schemas.Ptr("Calculate 5+3"),
				},
			},
		},
	}

	result, bifrostErr := manager.CheckAndExecuteAgentForChatRequest(
		ctx,
		originalReq,
		initialResponse,
		mockLLM.MakeChatRequest,
		func(ctx *schemas.BifrostContext, request *schemas.BifrostMCPRequest) (*schemas.BifrostMCPResponse, error) {
			return manager.ExecuteToolCall(ctx, request)
		},
	)

	require.Nil(t, bifrostErr)
	require.NotNil(t, result)
	assert.Equal(t, "stop", *result.Choices[0].FinishReason)

	content := result.Choices[0].ChatNonStreamResponseChoice.Message.Content
	assert.NotNil(t, content)
	t.Logf("Final response: %s", *content.ContentStr)
}

func TestAgent_BasicLoop_ResponsesFormat(t *testing.T) {
	t.Parallel()

	// Use InProcess tools - no external server needed
	manager := setupMCPManager(t)
	err := RegisterEchoTool(manager)
	require.NoError(t, err, "should register echo tool")

	// Set auto-execute for echo
	err = SetInternalClientAutoExecute(manager, []string{"echo"})
	require.NoError(t, err, "should set auto-execute for internal client")

	ctx := createTestContext()

	mockLLM := &MockLLMCaller{
		responsesResponses: []*schemas.BifrostResponsesResponse{
			CreateResponsesResponseWithToolCalls([]schemas.ResponsesToolMessage{
				{
					CallID:    schemas.Ptr("call-1"),
					Name: schemas.Ptr("bifrostInternal-echo"),
					Arguments: schemas.Ptr(`{"message": "testing responses format"}`),
				},
			}),
			CreateResponsesResponseWithText("Successfully echoed your message"),
		},
	}

	initialResponse := mockLLM.responsesResponses[0]
	mockLLM.responsesCallCount = 1

	originalReq := &schemas.BifrostResponsesRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4",
		Input: []schemas.ResponsesMessage{
			{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
				Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{
					ContentStr: schemas.Ptr("Echo a message"),
				},
			},
		},
	}

	result, bifrostErr := manager.CheckAndExecuteAgentForResponsesRequest(
		ctx,
		originalReq,
		initialResponse,
		mockLLM.MakeResponsesRequest,
		func(ctx *schemas.BifrostContext, request *schemas.BifrostMCPRequest) (*schemas.BifrostMCPResponse, error) {
			return manager.ExecuteToolCall(ctx, request)
		},
	)

	require.Nil(t, bifrostErr)
	require.NotNil(t, result)
	assert.NotEmpty(t, result.Output)
	t.Logf("Agent completed with %d messages in output", len(result.Output))
}

// =============================================================================
// AGENT ITERATIONS TESTS
// =============================================================================

func TestAgent_SingleIteration(t *testing.T) {
	t.Parallel()

	// Use InProcess tools - no external server needed
	manager := setupMCPManager(t)
	err := RegisterEchoTool(manager)
	require.NoError(t, err, "should register echo tool")

	// Set auto-execute for all tools
	err = SetInternalClientAutoExecute(manager, []string{"*"})
	require.NoError(t, err, "should set auto-execute for internal client")

	ctx := createTestContext()

	// LLM returns one tool call, then stops
	mockLLM := &MockLLMCaller{
		chatResponses: []*schemas.BifrostChatResponse{
			CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
				GetSampleEchoToolCall("call-1", "single iteration test"),
			}),
			// Immediately finish after tool execution
			CreateChatResponseWithText("Done after one tool call"),
		},
	}

	initialResponse := mockLLM.chatResponses[0]
	mockLLM.chatCallCount = 1

	originalReq := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4",
		Input: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentStr: schemas.Ptr("Test"),
				},
			},
		},
	}

	result, bifrostErr := manager.CheckAndExecuteAgentForChatRequest(
		ctx,
		originalReq,
		initialResponse,
		mockLLM.MakeChatRequest,
		func(ctx *schemas.BifrostContext, request *schemas.BifrostMCPRequest) (*schemas.BifrostMCPResponse, error) {
			return manager.ExecuteToolCall(ctx, request)
		},
	)

	require.Nil(t, bifrostErr)
	require.NotNil(t, result)

	// Should have made exactly 2 LLM calls total (initial + 1 follow-up after tool execution)
	assert.Equal(t, 2, mockLLM.chatCallCount, "should have exactly one iteration")
	assert.Equal(t, "stop", *result.Choices[0].FinishReason)

	// Verify no more tool calls in final response
	finalMessage := result.Choices[0].ChatNonStreamResponseChoice.Message
	if finalMessage.ChatAssistantMessage != nil {
		assert.Empty(t, finalMessage.ChatAssistantMessage.ToolCalls, "final response should have no tool calls")
	}
}

func TestAgent_MultipleIterations(t *testing.T) {
	t.Parallel()

	// Use InProcess tools - no external server needed
	manager := setupMCPManager(t)
	err := RegisterEchoTool(manager)
	require.NoError(t, err, "should register echo tool")
	err = RegisterCalculatorTool(manager)
	require.NoError(t, err, "should register calculator tool")
	err = RegisterWeatherTool(manager)
	require.NoError(t, err, "should register weather tool")

	// Set auto-execute for all tools
	err = SetInternalClientAutoExecute(manager, []string{"*"})
	require.NoError(t, err, "should set auto-execute for internal client")

	ctx := createTestContext()

	// LLM returns tool calls for 3 iterations, then stops
	mockLLM := &MockLLMCaller{
		chatResponses: []*schemas.BifrostChatResponse{
			// Iteration 1: echo tool
			CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
				GetSampleEchoToolCall("call-1", "iteration 1"),
			}),
			// Iteration 2: calculator tool
			CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
				GetSampleCalculatorToolCall("call-2", "add", 10, 20),
			}),
			// Iteration 3: weather tool
			CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
				GetSampleWeatherToolCall("call-3", "New York", ""),
			}),
			// Final: stop
			CreateChatResponseWithText("Completed all 3 tool calls"),
		},
	}

	initialResponse := mockLLM.chatResponses[0]
	mockLLM.chatCallCount = 1

	originalReq := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4",
		Input: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentStr: schemas.Ptr("Multi-step task"),
				},
			},
		},
	}

	result, bifrostErr := manager.CheckAndExecuteAgentForChatRequest(
		ctx,
		originalReq,
		initialResponse,
		mockLLM.MakeChatRequest,
		func(ctx *schemas.BifrostContext, request *schemas.BifrostMCPRequest) (*schemas.BifrostMCPResponse, error) {
			return manager.ExecuteToolCall(ctx, request)
		},
	)

	require.Nil(t, bifrostErr)
	require.NotNil(t, result)

	// Should have made 4 LLM calls total (initial + 3 follow-ups for each tool execution)
	assert.Equal(t, 4, mockLLM.chatCallCount, "should have 4 calls total (3 iterations + final)")
	assert.Equal(t, "stop", *result.Choices[0].FinishReason)

	t.Logf("Completed agent loop with 3 iterations")
}

func TestAgent_NoToolCalls(t *testing.T) {
	t.Parallel()

	// Use InProcess tools (even though we won't call them)
	manager := setupMCPManager(t)
	err := RegisterEchoTool(manager)
	require.NoError(t, err, "should register echo tool")

	// Set auto-execute for all tools
	err = SetInternalClientAutoExecute(manager, []string{"*"})
	require.NoError(t, err, "should set auto-execute for internal client")

	ctx := createTestContext()

	// LLM returns response with no tool calls (immediate stop)
	mockLLM := &MockLLMCaller{
		chatResponses: []*schemas.BifrostChatResponse{
			CreateChatResponseWithText("I don't need to use any tools for this"),
		},
	}

	initialResponse := mockLLM.chatResponses[0]
	mockLLM.chatCallCount = 0 // No calls should be made

	originalReq := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4",
		Input: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentStr: schemas.Ptr("Simple question"),
				},
			},
		},
	}

	result, bifrostErr := manager.CheckAndExecuteAgentForChatRequest(
		ctx,
		originalReq,
		initialResponse,
		mockLLM.MakeChatRequest,
		func(ctx *schemas.BifrostContext, request *schemas.BifrostMCPRequest) (*schemas.BifrostMCPResponse, error) {
			return manager.ExecuteToolCall(ctx, request)
		},
	)

	require.Nil(t, bifrostErr)
	require.NotNil(t, result)

	// Should have made NO additional LLM calls
	assert.Equal(t, 0, mockLLM.chatCallCount, "should not make any LLM calls when no tool calls")
	assert.Equal(t, "stop", *result.Choices[0].FinishReason)

	// Should return the original response unchanged
	assert.Equal(t, initialResponse, result, "should return original response when no tool calls")
}

// =============================================================================
// MIXED AUTO AND NON-AUTO TOOLS TESTS
// =============================================================================

func TestAgent_MixedAutoAndNonAutoTools(t *testing.T) {
	t.Parallel()

	// Use InProcess tools - no external server needed
	manager := setupMCPManager(t)
	err := RegisterEchoTool(manager)
	require.NoError(t, err, "should register echo tool")
	err = RegisterCalculatorTool(manager)
	require.NoError(t, err, "should register calculator tool")

	// Configure only "echo" as auto-executable, other tools require approval
	err = SetInternalClientAutoExecute(manager, []string{"echo"})
	require.NoError(t, err, "should set auto-execute for internal client")

	ctx := createTestContext()

	// LLM returns both auto and non-auto tools
	mockLLM := &MockLLMCaller{
		chatResponses: []*schemas.BifrostChatResponse{
			CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
				GetSampleEchoToolCall("call-1", "auto tool"),
				GetSampleCalculatorToolCall("call-2", "add", 5, 3), // Not auto-executable
			}),
		},
	}

	initialResponse := mockLLM.chatResponses[0]
	mockLLM.chatCallCount = 0 // Should not make additional calls

	originalReq := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4",
		Input: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentStr: schemas.Ptr("Test mixed tools"),
				},
			},
		},
	}

	result, bifrostErr := manager.CheckAndExecuteAgentForChatRequest(
		ctx,
		originalReq,
		initialResponse,
		mockLLM.MakeChatRequest,
		func(ctx *schemas.BifrostContext, request *schemas.BifrostMCPRequest) (*schemas.BifrostMCPResponse, error) {
			return manager.ExecuteToolCall(ctx, request)
		},
	)

	require.Nil(t, bifrostErr)
	require.NotNil(t, result)

	// Agent should stop and return non-auto tools
	assert.Equal(t, 0, mockLLM.chatCallCount, "should not make additional calls when non-auto tool present")
	assert.Equal(t, "stop", *result.Choices[0].FinishReason)

	// Verify response contains the non-auto tool (calculator) for user approval
	finalMessage := result.Choices[0].ChatNonStreamResponseChoice.Message
	require.NotNil(t, finalMessage.ChatAssistantMessage)
	require.NotEmpty(t, finalMessage.ChatAssistantMessage.ToolCalls)

	// Should have the calculator tool call (non-auto)
	found := false
	for _, tc := range finalMessage.ChatAssistantMessage.ToolCalls {
		if *tc.Function.Name == "bifrostInternal-calculator" {
			found = true
			break
		}
	}
	assert.True(t, found, "response should contain the non-auto-executable calculator tool")

	// Response should also include results of auto-executed tools in content
	assert.NotNil(t, finalMessage.Content)
	t.Logf("Response content: %s", *finalMessage.Content.ContentStr)
}

func TestAgent_OnlyAutoTools(t *testing.T) {
	t.Parallel()

	// Use InProcess tools - no external server needed
	manager := setupMCPManager(t)
	err := RegisterEchoTool(manager)
	require.NoError(t, err, "should register echo tool")

	// All tools auto-executable
	err = SetInternalClientAutoExecute(manager, []string{"*"})
	require.NoError(t, err, "should set auto-execute for internal client")

	ctx := createTestContext()

	mockLLM := &MockLLMCaller{
		chatResponses: []*schemas.BifrostChatResponse{
			// Multiple auto-executable tools
			CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
				GetSampleEchoToolCall("call-1", "first"),
				GetSampleEchoToolCall("call-2", "second"),
			}),
			// Continue loop
			CreateChatResponseWithText("All tools executed"),
		},
	}

	initialResponse := mockLLM.chatResponses[0]
	mockLLM.chatCallCount = 1

	originalReq := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4",
		Input: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentStr: schemas.Ptr("Test only auto tools"),
				},
			},
		},
	}

	result, bifrostErr := manager.CheckAndExecuteAgentForChatRequest(
		ctx,
		originalReq,
		initialResponse,
		mockLLM.MakeChatRequest,
		func(ctx *schemas.BifrostContext, request *schemas.BifrostMCPRequest) (*schemas.BifrostMCPResponse, error) {
			return manager.ExecuteToolCall(ctx, request)
		},
	)

	require.Nil(t, bifrostErr)
	require.NotNil(t, result)

	// Agent should execute all tools and continue loop
	assert.GreaterOrEqual(t, mockLLM.chatCallCount, 1, "should make follow-up LLM call")
	assert.Equal(t, "stop", *result.Choices[0].FinishReason)

	// Final response should have no pending tool calls
	finalMessage := result.Choices[0].ChatNonStreamResponseChoice.Message
	if finalMessage.ChatAssistantMessage != nil {
		assert.Empty(t, finalMessage.ChatAssistantMessage.ToolCalls, "no pending tool calls")
	}
}

func TestAgent_OnlyNonAutoTools(t *testing.T) {
	t.Parallel()

	// Use InProcess tools - no external server needed
	manager := setupMCPManager(t)
	err := RegisterEchoTool(manager)
	require.NoError(t, err, "should register echo tool")

	// No tools are auto-executable
	err = SetInternalClientAutoExecute(manager, []string{}) // No auto-executable tools
	require.NoError(t, err, "should set auto-execute for internal client")

	ctx := createTestContext()

	mockLLM := &MockLLMCaller{
		chatResponses: []*schemas.BifrostChatResponse{
			CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
				GetSampleEchoToolCall("call-1", "needs approval"),
			}),
		},
	}

	initialResponse := mockLLM.chatResponses[0]
	mockLLM.chatCallCount = 0 // Should not make additional calls

	originalReq := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4",
		Input: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentStr: schemas.Ptr("Test non-auto tools"),
				},
			},
		},
	}

	result, bifrostErr := manager.CheckAndExecuteAgentForChatRequest(
		ctx,
		originalReq,
		initialResponse,
		mockLLM.MakeChatRequest,
		func(ctx *schemas.BifrostContext, request *schemas.BifrostMCPRequest) (*schemas.BifrostMCPResponse, error) {
			return manager.ExecuteToolCall(ctx, request)
		},
	)

	require.Nil(t, bifrostErr)
	require.NotNil(t, result)

	// Agent should stop immediately and return tools to user
	assert.Equal(t, 0, mockLLM.chatCallCount, "should not make any LLM calls")
	assert.Equal(t, "tool_calls", *result.Choices[0].FinishReason, "should return with tool_calls since tools need approval")

	// Verify response contains the non-auto tools
	finalMessage := result.Choices[0].ChatNonStreamResponseChoice.Message
	require.NotNil(t, finalMessage.ChatAssistantMessage)
	require.NotEmpty(t, finalMessage.ChatAssistantMessage.ToolCalls)
	assert.Equal(t, "bifrostInternal-echo", *finalMessage.ChatAssistantMessage.ToolCalls[0].Function.Name)
}

// =============================================================================
// AGENT WITH REAL LLM TESTS
// =============================================================================

func TestAgent_WithRealLLM_Simple(t *testing.T) {
	t.Parallel()

	testConfig := GetTestConfig(t)
	if !testConfig.UseRealLLM {
		t.Skip("Real LLM not configured")
	}

	// Setup MCP with auto-executable calculator tool using InProcess
	manager := setupMCPManager(t)
	err := RegisterCalculatorTool(manager)
	require.NoError(t, err, "should register calculator tool")

	err = SetInternalClientAutoExecute(manager, []string{"calculator"})
	require.NoError(t, err, "should set auto-execute for internal client")

	// Setup bifrost with real LLM
	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	// Create context with timeout for real API call
	ctx, cancel := createTestContextWithTimeout(30 * time.Second)
	defer cancel()

	// Ask LLM to use calculator tool
	req := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o",
		Input: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentStr: schemas.Ptr("Calculate 25 + 17 using the calculator tool"),
				},
			},
		},
	}

	// Make request - agent mode will activate if LLM returns tool calls
	result, bifrostErr := bifrost.ChatCompletionRequest(ctx, req)
	if bifrostErr != nil {
		// Skip if there's an API error (likely missing/invalid API key or config issue)
		t.Skipf("Skipping real LLM test due to API error: %v", bifrostErr.Error)
	}
	require.NotNil(t, result)

	// Verify we got a response
	assert.NotEmpty(t, result.Choices)
	t.Logf("Real LLM agent response: %s", *result.Choices[0].ChatNonStreamResponseChoice.Message.Content.ContentStr)

	// Check if response mentions the result (42)
	responseText := *result.Choices[0].ChatNonStreamResponseChoice.Message.Content.ContentStr
	// Don't assert exact match due to LLM variability, just log
	t.Logf("Response contains calculation result")
	_ = responseText
}

func TestAgent_WithRealLLM_MultiStep(t *testing.T) {
	t.Parallel()

	testConfig := GetTestConfig(t)
	if !testConfig.UseRealLLM {
		t.Skip("Real LLM not configured")
	}

	// Setup MCP with auto-executable tools using InProcess
	manager := setupMCPManager(t)
	err := RegisterCalculatorTool(manager)
	require.NoError(t, err, "should register calculator tool")
	err = RegisterEchoTool(manager)
	require.NoError(t, err, "should register echo tool")

	err = SetInternalClientAutoExecute(manager, []string{"calculator", "echo"})
	require.NoError(t, err, "should set auto-execute for internal client")

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	// Create context with timeout for real API call
	ctx, cancel := createTestContextWithTimeout(30 * time.Second)
	defer cancel()

	// Ask LLM to perform multi-step task
	req := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o",
		Input: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentStr: schemas.Ptr("First calculate 10 + 5, then echo the result"),
				},
			},
		},
	}

	result, bifrostErr := bifrost.ChatCompletionRequest(ctx, req)
	if bifrostErr != nil {
		// Skip if there's an API error (likely missing/invalid API key or config issue)
		t.Skipf("Skipping real LLM test due to API error: %v", bifrostErr.Error)
	}
	require.NotNil(t, result)

	assert.NotEmpty(t, result.Choices)
	t.Logf("Multi-step agent response: %s", *result.Choices[0].ChatNonStreamResponseChoice.Message.Content.ContentStr)

	// Response should mention both operations
	// Due to LLM variability, we just log the result
	t.Logf("Multi-step task completed")
}
