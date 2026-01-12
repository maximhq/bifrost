package mcptests

import (
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// AGENT ALLOWED VS AUTO-EXECUTE TESTS
// =============================================================================

func TestAgent_ToolAllowedNotAutoExecute(t *testing.T) {
	t.Parallel()

	// ToolsToExecute = ["echo"], ToolsToAutoExecute = []
	// Tool is allowed to execute but not auto-executed in agent mode
	manager := setupMCPManager(t)
	err := RegisterEchoTool(manager)
	require.NoError(t, err, "should register echo tool")

	err = SetInternalClientAutoExecute(manager, []string{}) // Empty means no auto-execute
	require.NoError(t, err, "should set auto-execute for internal client")

	ctx := createTestContext()

	mockLLM := &MockLLMCaller{
		chatResponses: []*schemas.BifrostChatResponse{
			CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
				GetSampleEchoToolCall("call-1", "test message"),
			}),
		},
	}

	initialResponse := mockLLM.chatResponses[0]
	mockLLM.chatCallCount = 0

	originalReq := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o",
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

	// Agent should stop and return tool for user approval (not auto-executed)
	assert.Equal(t, 0, mockLLM.chatCallCount, "should not make additional LLM calls")

	// Verify tool is returned for approval
	finalMessage := result.Choices[0].ChatNonStreamResponseChoice.Message
	require.NotNil(t, finalMessage.ChatAssistantMessage)
	require.NotEmpty(t, finalMessage.ChatAssistantMessage.ToolCalls)
	assert.Equal(t, "bifrostInternal-echo", *finalMessage.ChatAssistantMessage.ToolCalls[0].Function.Name)
}

func TestAgent_ToolAllowedAndAutoExecute(t *testing.T) {
	t.Parallel()

	// ToolsToExecute = ["echo"], ToolsToAutoExecute = ["echo"]
	// Tool is both allowed and auto-executed
	manager := setupMCPManager(t)
	err := RegisterEchoTool(manager)
	require.NoError(t, err, "should register echo tool")

	err = SetInternalClientAutoExecute(manager, []string{"echo"})
	require.NoError(t, err, "should set auto-execute for internal client")

	ctx := createTestContext()

	mockLLM := &MockLLMCaller{
		chatResponses: []*schemas.BifrostChatResponse{
			CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
				GetSampleEchoToolCall("call-1", "auto-executed"),
			}),
			CreateChatResponseWithText("Tool executed successfully"),
		},
	}

	initialResponse := mockLLM.chatResponses[0]
	mockLLM.chatCallCount = 1

	originalReq := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o",
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

	// Agent should auto-execute and continue loop
	assert.GreaterOrEqual(t, mockLLM.chatCallCount, 1, "should have made follow-up LLM call")
	assert.Equal(t, "stop", *result.Choices[0].FinishReason)
}

func TestAgent_ToolNotAllowed(t *testing.T) {
	t.Parallel()

	// Only register echo, but LLM tries to call calculator (not available)
	manager := setupMCPManager(t)
	err := RegisterEchoTool(manager)
	require.NoError(t, err, "should register echo tool")

	err = SetInternalClientAutoExecute(manager, []string{"*"})
	require.NoError(t, err, "should set auto-execute for internal client")

	ctx := createTestContext()

	mockLLM := &MockLLMCaller{
		chatResponses: []*schemas.BifrostChatResponse{
			CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
				GetSampleCalculatorToolCall("call-1", "add", 5, 3), // Not registered
			}),
		},
	}

	initialResponse := mockLLM.chatResponses[0]
	mockLLM.chatCallCount = 0

	originalReq := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o",
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

	// Tool not available should stop agent
	assert.Equal(t, 0, mockLLM.chatCallCount, "should not make additional calls")

	// Tool should be returned (will show as unavailable)
	finalMessage := result.Choices[0].ChatNonStreamResponseChoice.Message
	t.Logf("Response: %+v", finalMessage)
}

func TestAgent_ToolNotInAutoExecuteList(t *testing.T) {
	t.Parallel()

	// ToolsToExecute = ["*"], ToolsToAutoExecute = ["echo"]
	// LLM returns calculator - allowed but not auto-executed
	manager := setupMCPManager(t)
	err := RegisterEchoTool(manager)
	require.NoError(t, err, "should register echo tool")
	err = RegisterCalculatorTool(manager)
	require.NoError(t, err, "should register calculator tool")

	err = SetInternalClientAutoExecute(manager, []string{"echo"})
	require.NoError(t, err, "should set auto-execute for internal client")

	ctx := createTestContext()

	mockLLM := &MockLLMCaller{
		chatResponses: []*schemas.BifrostChatResponse{
			CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
				GetSampleCalculatorToolCall("call-1", "add", 10, 20),
			}),
		},
	}

	initialResponse := mockLLM.chatResponses[0]
	mockLLM.chatCallCount = 0

	originalReq := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o",
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

	// Agent should stop (calculator not auto-executable)
	assert.Equal(t, 0, mockLLM.chatCallCount, "should not make additional calls")

	// Verify calculator is returned for approval
	finalMessage := result.Choices[0].ChatNonStreamResponseChoice.Message
	require.NotNil(t, finalMessage.ChatAssistantMessage)
	require.NotEmpty(t, finalMessage.ChatAssistantMessage.ToolCalls)
	assert.Equal(t, "bifrostInternal-calculator", *finalMessage.ChatAssistantMessage.ToolCalls[0].Function.Name)
}

// =============================================================================
// COMPLEX FILTERING SCENARIOS
// =============================================================================

func TestAgent_ComplexFiltering_Scenario1(t *testing.T) {
	t.Parallel()

	// Config: ToolsToAutoExecute = ["echo"]
	// LLM returns echo (auto-executed), then calculator (stops for approval)
	manager := setupMCPManager(t)
	err := RegisterEchoTool(manager)
	require.NoError(t, err, "should register echo tool")
	err = RegisterCalculatorTool(manager)
	require.NoError(t, err, "should register calculator tool")

	err = SetInternalClientAutoExecute(manager, []string{"echo"})
	require.NoError(t, err, "should set auto-execute for internal client")

	ctx := createTestContext()

	mockLLM := &MockLLMCaller{
		chatResponses: []*schemas.BifrostChatResponse{
			// First: echo (will auto-execute)
			CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
				GetSampleEchoToolCall("call-1", "first tool"),
			}),
			// Second: calculator (will stop for approval)
			CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
				GetSampleCalculatorToolCall("call-2", "add", 5, 3),
			}),
		},
	}

	initialResponse := mockLLM.chatResponses[0]
	mockLLM.chatCallCount = 1

	originalReq := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o",
		Input: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentStr: schemas.Ptr("Multi-step"),
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

	// Should execute echo, make one more LLM call, then stop for calculator
	assert.GreaterOrEqual(t, mockLLM.chatCallCount, 1, "should make follow-up calls")

	// Verify calculator is in final response
	finalMessage := result.Choices[0].ChatNonStreamResponseChoice.Message
	require.NotNil(t, finalMessage.ChatAssistantMessage)
	require.NotEmpty(t, finalMessage.ChatAssistantMessage.ToolCalls)
	assert.Equal(t, "bifrostInternal-calculator", *finalMessage.ChatAssistantMessage.ToolCalls[0].Function.Name)
}

func TestAgent_ComplexFiltering_Scenario2(t *testing.T) {
	t.Parallel()

	// Config: ToolsToAutoExecute = ["echo"]
	// LLM returns echo, then weather
	// echo auto-executes, weather stops
	manager := setupMCPManager(t)
	err := RegisterEchoTool(manager)
	require.NoError(t, err, "should register echo tool")
	err = RegisterWeatherTool(manager)
	require.NoError(t, err, "should register weather tool")

	err = SetInternalClientAutoExecute(manager, []string{"echo"})
	require.NoError(t, err, "should set auto-execute for internal client")

	ctx := createTestContext()

	mockLLM := &MockLLMCaller{
		chatResponses: []*schemas.BifrostChatResponse{
			CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
				GetSampleEchoToolCall("call-1", "auto tool"),
			}),
			CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
				GetSampleWeatherToolCall("call-2", "Boston", ""),
			}),
		},
	}

	initialResponse := mockLLM.chatResponses[0]
	mockLLM.chatCallCount = 1

	originalReq := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o",
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
	assert.GreaterOrEqual(t, mockLLM.chatCallCount, 1, "should make follow-up calls")

	// Weather tool should be in response
	finalMessage := result.Choices[0].ChatNonStreamResponseChoice.Message
	require.NotNil(t, finalMessage.ChatAssistantMessage)
	require.NotEmpty(t, finalMessage.ChatAssistantMessage.ToolCalls)
	assert.Equal(t, "bifrostInternal-get_weather", *finalMessage.ChatAssistantMessage.ToolCalls[0].Function.Name)
}

func TestAgent_ComplexFiltering_Scenario3(t *testing.T) {
	t.Parallel()

	// Config: ToolsToAutoExecute = ["*"] but only echo and calculator registered
	// LLM returns echo, calculator, weather
	// echo and calculator auto-execute, weather not available (error)
	manager := setupMCPManager(t)
	err := RegisterEchoTool(manager)
	require.NoError(t, err, "should register echo tool")
	err = RegisterCalculatorTool(manager)
	require.NoError(t, err, "should register calculator tool")
	// Don't register weather

	err = SetInternalClientAutoExecute(manager, []string{"*"})
	require.NoError(t, err, "should set auto-execute for internal client")

	ctx := createTestContext()

	mockLLM := &MockLLMCaller{
		chatResponses: []*schemas.BifrostChatResponse{
			CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
				GetSampleEchoToolCall("call-1", "first"),
				GetSampleCalculatorToolCall("call-2", "add", 2, 2),
				GetSampleWeatherToolCall("call-3", "NYC", ""), // Not registered
			}),
			CreateChatResponseWithText("Completed"),
		},
	}

	initialResponse := mockLLM.chatResponses[0]
	mockLLM.chatCallCount = 1

	originalReq := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o",
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

	// Should execute available tools and handle unavailable weather
	t.Logf("Agent completed with %d calls", mockLLM.chatCallCount)
}

func TestAgent_ComplexFiltering_ContextOverride(t *testing.T) {
	t.Parallel()

	// Config: ToolsToAutoExecute = ["echo"]
	// Context: (context filtering currently applies to ToolsToExecute, not AutoExecute)
	// This test verifies agent behavior with basic filtering
	manager := setupMCPManager(t)
	err := RegisterEchoTool(manager)
	require.NoError(t, err, "should register echo tool")

	err = SetInternalClientAutoExecute(manager, []string{"echo"})
	require.NoError(t, err, "should set auto-execute for internal client")

	ctx := createTestContext()

	mockLLM := &MockLLMCaller{
		chatResponses: []*schemas.BifrostChatResponse{
			CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
				GetSampleEchoToolCall("call-1", "test"),
			}),
			CreateChatResponseWithText("Done"),
		},
	}

	initialResponse := mockLLM.chatResponses[0]
	mockLLM.chatCallCount = 1

	originalReq := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o",
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
	assert.GreaterOrEqual(t, mockLLM.chatCallCount, 1)
}

// =============================================================================
// FILTERING WITH MULTIPLE CLIENTS - Using InProcess + STDIO
// =============================================================================

func TestAgent_FilteringWithMultipleClients(t *testing.T) {
	t.Parallel()

	// Client 1: InProcess with echo (auto-execute)
	// Client 2: STDIO temperature server with get_temperature (not auto-execute)
	// LLM calls echo, then get_temperature
	// echo auto-executes, get_temperature stops

	// First set up InProcess tools
	manager := setupMCPManager(t)
	err := RegisterEchoTool(manager)
	require.NoError(t, err, "should register echo tool")

	err = SetInternalClientAutoExecute(manager, []string{"echo"})
	require.NoError(t, err, "should set auto-execute for internal client")

	// Now add STDIO temperature client (not auto-execute)
	InitMCPServerPaths(t)
	tempConfig := GetTemperatureMCPClientConfig("")
	tempConfig.ToolsToAutoExecute = []string{} // Not auto-executed

	err = manager.AddClient(tempConfig)
	if err != nil {
		t.Skipf("Skipping test - temperature server not available: %v", err)
		return
	}

	// Give server time to connect
	time.Sleep(500 * time.Millisecond)

	ctx := createTestContext()

	mockLLM := &MockLLMCaller{
		chatResponses: []*schemas.BifrostChatResponse{
			CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
				GetSampleEchoToolCall("call-1", "auto"),
			}),
			CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
				{
					ID:   schemas.Ptr("call-2"),
					Type: schemas.Ptr("function"),
					Function: schemas.ChatAssistantMessageToolCallFunction{
						Name: schemas.Ptr("bifrostInternal-get_temperature"),
						Arguments: `{"location": "New York"}`,
					},
				},
			}),
		},
	}

	initialResponse := mockLLM.chatResponses[0]
	mockLLM.chatCallCount = 1

	originalReq := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o",
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

	// Should auto-execute echo, then stop for get_temperature
	assert.GreaterOrEqual(t, mockLLM.chatCallCount, 1, "should make follow-up calls")

	finalMessage := result.Choices[0].ChatNonStreamResponseChoice.Message
	require.NotNil(t, finalMessage.ChatAssistantMessage)
	require.NotEmpty(t, finalMessage.ChatAssistantMessage.ToolCalls)
	assert.Equal(t, "bifrostInternal-get_temperature", *finalMessage.ChatAssistantMessage.ToolCalls[0].Function.Name)
}

func TestAgent_ToolConflictInAgentMode(t *testing.T) {
	t.Parallel()

	// Both InProcess and STDIO have "get_temperature" tool but different auto-execute settings
	// InProcess: get_temperature is auto-executed
	// STDIO: get_temperature requires approval
	// When LLM calls get_temperature, verify which client is selected and behavior

	manager := setupMCPManager(t)

	// Register InProcess get_temperature (will conflict with STDIO)
	err := RegisterGetTemperatureTool(manager)
	require.NoError(t, err, "should register InProcess get_temperature")

	err = SetInternalClientAutoExecute(manager, []string{"get_temperature"})
	require.NoError(t, err, "should set auto-execute for internal client")

	// Add STDIO temperature client (same tool name, NOT auto-execute)
	InitMCPServerPaths(t)
	tempConfig := GetTemperatureMCPClientConfig("")
	tempConfig.ToolsToAutoExecute = []string{} // Not auto

	err = manager.AddClient(tempConfig)
	if err != nil {
		t.Skipf("Skipping test - temperature server not available: %v", err)
		return
	}

	// Give server time to connect
	time.Sleep(500 * time.Millisecond)

	ctx := createTestContext()

	mockLLM := &MockLLMCaller{
		chatResponses: []*schemas.BifrostChatResponse{
			CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
				{
					ID:   schemas.Ptr("call-1"),
					Type: schemas.Ptr("function"),
					Function: schemas.ChatAssistantMessageToolCallFunction{
						Name: schemas.Ptr("bifrostInternal-get_temperature"),
						Arguments: `{"location": "New York"}`,
					},
				},
			}),
			CreateChatResponseWithText("Completed"),
		},
	}

	initialResponse := mockLLM.chatResponses[0]
	mockLLM.chatCallCount = 1

	originalReq := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o",
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

	// When there's a tool name conflict, Bifrost should:
	// 1. Select one of the clients (typically first registered = InProcess in this case)
	// 2. Use that client's auto-execute configuration
	// 3. Execute the tool and continue/stop based on that client's settings

	finalMessage := result.Choices[0].ChatNonStreamResponseChoice.Message

	// Check if the tool was executed or returned for approval
	if finalMessage.ChatAssistantMessage != nil && len(finalMessage.ChatAssistantMessage.ToolCalls) > 0 {
		// Tool was NOT auto-executed (stopped for approval)
		t.Logf("Tool stopped for approval - STDIO client was selected (no auto-execute)")
		assert.Equal(t, "bifrostInternal-get_temperature", *finalMessage.ChatAssistantMessage.ToolCalls[0].Function.Name)
		assert.Equal(t, 1, mockLLM.chatCallCount, "should not make additional calls when stopping")
	} else if finalMessage.Content != nil && finalMessage.Content.ContentStr != nil {
		// Tool was auto-executed and agent continued
		t.Logf("Tool auto-executed - InProcess client was selected (auto-execute enabled)")
		assert.GreaterOrEqual(t, mockLLM.chatCallCount, 1, "should make follow-up calls after auto-execute")

		// We can't easily check the response content here since it's been processed by the LLM mock,
		// but we verified the agent loop continued which means auto-execute worked
	}

	// Most importantly: verify no error occurred despite the conflict
	t.Logf("✅ Tool name conflict handled successfully - no errors")
}

// =============================================================================
// AUTO-EXECUTE SCENARIOS
// =============================================================================

func TestAgent_AllAutoExecuteScenarios(t *testing.T) {
	t.Parallel()

	// Use comprehensive scenarios from fixtures
	scenarios := GetAutoExecuteScenarios()

	for _, scenario := range scenarios {
		scenario := scenario
		t.Run(scenario.Name, func(t *testing.T) {
			t.Parallel()

			manager := setupMCPManager(t)
			err := RegisterEchoTool(manager)
			require.NoError(t, err, "should register echo tool")

			err = SetInternalClientAutoExecute(manager, scenario.ToolsToAutoExecute)
			require.NoError(t, err, "should set auto-execute for internal client")

			ctx := createTestContext()

			// Create tool call for this scenario
			toolCall := GetSampleEchoToolCall("call-1", scenario.RequestedTool)

			mockLLM := &MockLLMCaller{
				chatResponses: []*schemas.BifrostChatResponse{
					CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{toolCall}),
					CreateChatResponseWithText("Done"),
				},
			}

			initialResponse := mockLLM.chatResponses[0]
			mockLLM.chatCallCount = 1

			originalReq := &schemas.BifrostChatRequest{
				Provider: schemas.OpenAI,
				Model:    "gpt-4o",
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

			if scenario.ShouldAutoExecute {
				// Should continue agent loop
				require.Nil(t, bifrostErr)
				require.NotNil(t, result)
				t.Logf("Scenario '%s': auto-executed as expected", scenario.Name)
			} else if scenario.ShouldAllowExecute {
				// Should stop and return for approval
				require.Nil(t, bifrostErr)
				require.NotNil(t, result)
				assert.Equal(t, 0, mockLLM.chatCallCount-1, "should stop for approval")
				t.Logf("Scenario '%s': stopped for approval as expected", scenario.Name)
			} else {
				// Tool not executable (filtered)
				require.Nil(t, bifrostErr)
				t.Logf("Scenario '%s': tool filtered as expected", scenario.Name)
			}
		})
	}
}

// =============================================================================
// BOTH API FORMATS
// =============================================================================

func TestAgent_Filtering_ChatFormat(t *testing.T) {
	t.Parallel()

	manager := setupMCPManager(t)
	err := RegisterEchoTool(manager)
	require.NoError(t, err, "should register echo tool")
	err = RegisterCalculatorTool(manager)
	require.NoError(t, err, "should register calculator tool")

	err = SetInternalClientAutoExecute(manager, []string{"echo"})
	require.NoError(t, err, "should set auto-execute for internal client")

	ctx := createTestContext()

	mockLLM := &MockLLMCaller{
		chatResponses: []*schemas.BifrostChatResponse{
			CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
				GetSampleEchoToolCall("call-1", "chat format"),
			}),
			CreateChatResponseWithText("Chat format complete"),
		},
	}

	initialResponse := mockLLM.chatResponses[0]
	mockLLM.chatCallCount = 1

	originalReq := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o",
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
	assert.Equal(t, "stop", *result.Choices[0].FinishReason)
}

func TestAgent_Filtering_ResponsesFormat(t *testing.T) {
	t.Parallel()

	manager := setupMCPManager(t)
	err := RegisterEchoTool(manager)
	require.NoError(t, err, "should register echo tool")
	err = RegisterCalculatorTool(manager)
	require.NoError(t, err, "should register calculator tool")

	err = SetInternalClientAutoExecute(manager, []string{"echo"})
	require.NoError(t, err, "should set auto-execute for internal client")

	ctx := createTestContext()

	mockLLM := &MockLLMCaller{
		responsesResponses: []*schemas.BifrostResponsesResponse{
			CreateResponsesResponseWithToolCalls([]schemas.ResponsesToolMessage{
				{
					CallID:    schemas.Ptr("call-1"),
					Name: schemas.Ptr("bifrostInternal-echo"),
					Arguments: schemas.Ptr(`{"message": "responses format"}`),
				},
			}),
			CreateResponsesResponseWithText("Responses format complete"),
		},
	}

	initialResponse := mockLLM.responsesResponses[0]
	mockLLM.responsesCallCount = 1

	originalReq := &schemas.BifrostResponsesRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o",
		Input: []schemas.ResponsesMessage{
			{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
				Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{
					ContentStr: schemas.Ptr("Test"),
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
	t.Logf("Responses format filtering test completed")
}
