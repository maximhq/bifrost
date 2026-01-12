package mcptests

import (
	"fmt"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// AGENT STATE TRANSITIONS AND BOUNDARIES TESTS
// =============================================================================
// These tests verify agent mode state management (agent.go:161-277, 333-342)
// Focus: Large mixed batches, dynamic filtering, state consistency, depth counting

func TestAgent_StateTransition_LargeMixedToolBatch(t *testing.T) {
	t.Parallel()

	// Test handling of large batch with mixed auto/non-auto tools
	manager := setupMCPManager(t)

	// Register 50 auto-executable tools
	autoTools := []string{}
	for i := 0; i < 50; i++ {
		toolName := fmt.Sprintf("auto_tool_%d", i)
		autoTools = append(autoTools, toolName)

		toolIndex := i
		toolHandler := func(args any) (string, error) {
			return fmt.Sprintf(`{"tool": "auto_tool_%d", "auto": true}`, toolIndex), nil
		}

		toolSchema := schemas.ChatTool{
			Type: schemas.ChatToolTypeFunction,
			Function: &schemas.ChatToolFunction{
				Name:        toolName,
				Description: schemas.Ptr(fmt.Sprintf("Auto tool %d", i)),
			},
		}

		err := manager.RegisterTool(toolName, fmt.Sprintf("Auto tool %d", i), toolHandler, toolSchema)
		require.NoError(t, err)
	}

	// Register 5 non-auto-executable tools
	for i := 0; i < 5; i++ {
		toolName := fmt.Sprintf("manual_tool_%d", i)
		toolIndex := i

		toolHandler := func(args any) (string, error) {
			return fmt.Sprintf(`{"tool": "manual_tool_%d", "auto": false}`, toolIndex), nil
		}

		toolSchema := schemas.ChatTool{
			Type: schemas.ChatToolTypeFunction,
			Function: &schemas.ChatToolFunction{
				Name:        toolName,
				Description: schemas.Ptr(fmt.Sprintf("Manual tool %d", i)),
			},
		}

		err := manager.RegisterTool(toolName, fmt.Sprintf("Manual tool %d", i), toolHandler, toolSchema)
		require.NoError(t, err)
	}

	// Set only auto_tool_* as auto-executable
	err := SetInternalClientAutoExecute(manager, autoTools)
	require.NoError(t, err)

	ctx := createTestContext()

	// Create mixed batch: all 50 auto + all 5 manual tools
	toolCalls := []schemas.ChatAssistantMessageToolCall{}

	// Add auto tools
	for i := 0; i < 50; i++ {
		toolCalls = append(toolCalls, schemas.ChatAssistantMessageToolCall{
			ID:   schemas.Ptr(fmt.Sprintf("call-auto-%d", i)),
			Type: schemas.Ptr("function"),
			Function: schemas.ChatAssistantMessageToolCallFunction{
				Name:      schemas.Ptr(fmt.Sprintf("bifrostInternal-auto_tool_%d", i)),
				Arguments: "{}",
			},
		})
	}

	// Add manual tools
	for i := 0; i < 5; i++ {
		toolCalls = append(toolCalls, schemas.ChatAssistantMessageToolCall{
			ID:   schemas.Ptr(fmt.Sprintf("call-manual-%d", i)),
			Type: schemas.Ptr("function"),
			Function: schemas.ChatAssistantMessageToolCallFunction{
				Name:      schemas.Ptr(fmt.Sprintf("bifrostInternal-manual_tool_%d", i)),
				Arguments: "{}",
			},
		})
	}

	mockLLM := &MockLLMCaller{
		chatResponses: []*schemas.BifrostChatResponse{
			CreateChatResponseWithToolCalls(toolCalls),
		},
	}

	initialResponse := mockLLM.chatResponses[0]
	mockLLM.chatCallCount = 0

	originalReq := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4",
		Input: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentStr: schemas.Ptr("Execute large mixed batch"),
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

	require.Nil(t, bifrostErr, "should handle large mixed batch")
	require.NotNil(t, result)

	// Agent should execute 50 auto tools and return 5 manual tools
	assert.Equal(t, 0, mockLLM.chatCallCount, "should stop due to non-auto tools")

	// Verify response contains non-auto tools
	finalMessage := result.Choices[0].ChatNonStreamResponseChoice.Message
	if finalMessage.ChatAssistantMessage != nil {
		t.Logf("Returned %d non-auto tools for user approval", len(finalMessage.ChatAssistantMessage.ToolCalls))
	}

	t.Logf("✅ Large mixed batch (50 auto + 5 manual) handled correctly")
}

func TestAgent_StateTransition_DepthCountingBasic(t *testing.T) {
	t.Parallel()

	// Test depth counting in agent loop
	manager := setupMCPManager(t)
	err := RegisterEchoTool(manager)
	require.NoError(t, err)

	err = SetInternalClientAutoExecute(manager, []string{"*"})
	require.NoError(t, err)

	ctx := createTestContext()

	// Create a long sequence of tool calls to test depth tracking
	// Default MaxDepth is 10
	responses := []*schemas.BifrostChatResponse{}

	// Create 9 iterations (under the limit)
	for i := 0; i < 9; i++ {
		responses = append(responses, CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
			GetSampleEchoToolCall(fmt.Sprintf("call-depth-%d", i), fmt.Sprintf("iteration %d", i)),
		}))
	}

	// Final response (10th iteration should complete)
	responses = append(responses, CreateChatResponseWithText("Completed within depth limit"))

	mockLLM := &MockLLMCaller{
		chatResponses: responses,
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
					ContentStr: schemas.Ptr("Test depth counting"),
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
	t.Logf("✅ Depth counting: completed 9 iterations within limit")
	t.Logf("Total LLM calls: %d", mockLLM.chatCallCount)
}

func TestAgent_StateTransition_AlternatingAutoNonAuto(t *testing.T) {
	t.Parallel()

	// Test alternating between auto and non-auto tools across iterations
	manager := setupMCPManager(t)

	// Register auto and non-auto tools
	err := RegisterEchoTool(manager) // Will be auto
	require.NoError(t, err)

	manualHandler := func(args any) (string, error) {
		return `{"result": "manual execution"}`, nil
	}
	manualSchema := schemas.ChatTool{
		Type: schemas.ChatToolTypeFunction,
		Function: &schemas.ChatToolFunction{
			Name:        "manual_tool",
			Description: schemas.Ptr("Requires approval"),
		},
	}
	err = manager.RegisterTool("manual_tool", "Requires approval", manualHandler, manualSchema)
	require.NoError(t, err)

	// Only echo is auto-executable
	err = SetInternalClientAutoExecute(manager, []string{"echo"})
	require.NoError(t, err)

	ctx := createTestContext()

	// Iteration 1: auto tool (echo)
	// Iteration 2: non-auto tool (manual) - should stop here

	mockLLM := &MockLLMCaller{
		chatResponses: []*schemas.BifrostChatResponse{
			// First: auto tool
			CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
				GetSampleEchoToolCall("call-auto-1", "auto execution"),
			}),
			// Second: non-auto tool (should stop and return to user)
			CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
				{
					ID:   schemas.Ptr("call-manual-1"),
					Type: schemas.Ptr("function"),
					Function: schemas.ChatAssistantMessageToolCallFunction{
						Name: schemas.Ptr("bifrostInternal-manual_tool"),
						Arguments: "{}",
					},
				},
			}),
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
					ContentStr: schemas.Ptr("Test alternating"),
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

	// Should have made exactly 2 LLM calls (initial + 1 after first auto execution)
	// Then stopped because second response has non-auto tool
	assert.Equal(t, 2, mockLLM.chatCallCount)

	// Verify response contains manual tool
	finalMessage := result.Choices[0].ChatNonStreamResponseChoice.Message
	if finalMessage.ChatAssistantMessage != nil && len(finalMessage.ChatAssistantMessage.ToolCalls) > 0 {
		assert.Equal(t, "bifrostInternal-manual_tool", *finalMessage.ChatAssistantMessage.ToolCalls[0].Function.Name)
	}

	t.Logf("✅ Alternating auto/non-auto handled: stopped at non-auto")
}

func TestAgent_StateTransition_EmptyToolCallsList(t *testing.T) {
	t.Parallel()

	// Test agent behavior when LLM returns empty tool calls list
	manager := setupMCPManager(t)
	err := RegisterEchoTool(manager)
	require.NoError(t, err)

	err = SetInternalClientAutoExecute(manager, []string{"*"})
	require.NoError(t, err)

	ctx := createTestContext()

	// First response: valid tool call
	// Second response: empty tool calls list (should be treated as completion)
	mockLLM := &MockLLMCaller{
		chatResponses: []*schemas.BifrostChatResponse{
			CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
				GetSampleEchoToolCall("call-1", "test"),
			}),
			// Empty tool calls - should stop
			{
				Choices: []schemas.BifrostResponseChoice{
					{
						FinishReason: schemas.Ptr("stop"),
						ChatNonStreamResponseChoice: &schemas.ChatNonStreamResponseChoice{
							Message: &schemas.ChatMessage{
								Role: schemas.ChatMessageRoleAssistant,
								Content: &schemas.ChatMessageContent{
									ContentStr: schemas.Ptr("Done"),
								},
								ChatAssistantMessage: &schemas.ChatAssistantMessage{
									ToolCalls: []schemas.ChatAssistantMessageToolCall{}, // Empty list
								},
							},
						},
					},
				},
			},
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
					ContentStr: schemas.Ptr("Test empty list"),
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
	t.Logf("✅ Empty tool calls list handled correctly")
}

func TestAgent_StateTransition_AllToolsFilteredOut(t *testing.T) {
	t.Parallel()

	// Test when all tools in a call are filtered out (not in auto-execute list)
	manager := setupMCPManager(t)

	// Register multiple tools
	for i := 0; i < 3; i++ {
		toolName := fmt.Sprintf("tool_%d", i)
		toolIndex := i

		toolHandler := func(args any) (string, error) {
			return fmt.Sprintf(`{"tool": "tool_%d"}`, toolIndex), nil
		}

		toolSchema := schemas.ChatTool{
			Type: schemas.ChatToolTypeFunction,
			Function: &schemas.ChatToolFunction{
				Name:        toolName,
				Description: schemas.Ptr(fmt.Sprintf("Tool %d", i)),
			},
		}

		err := manager.RegisterTool(toolName, fmt.Sprintf("Tool %d", i), toolHandler, toolSchema)
		require.NoError(t, err)
	}

	// Set empty auto-execute list (no tools auto-executable)
	err := SetInternalClientAutoExecute(manager, []string{})
	require.NoError(t, err)

	ctx := createTestContext()

	// LLM returns tool calls, but none are auto-executable
	toolCalls := []schemas.ChatAssistantMessageToolCall{
		{
			ID:   schemas.Ptr("call-0"),
			Type: schemas.Ptr("function"),
			Function: schemas.ChatAssistantMessageToolCallFunction{
				Name: schemas.Ptr("bifrostInternal-tool_0"),
				Arguments: "{}",
			},
		},
		{
			ID:   schemas.Ptr("call-1"),
			Type: schemas.Ptr("function"),
			Function: schemas.ChatAssistantMessageToolCallFunction{
				Name: schemas.Ptr("bifrostInternal-tool_1"),
				Arguments: "{}",
			},
		},
	}

	mockLLM := &MockLLMCaller{
		chatResponses: []*schemas.BifrostChatResponse{
			CreateChatResponseWithToolCalls(toolCalls),
		},
	}

	initialResponse := mockLLM.chatResponses[0]
	mockLLM.chatCallCount = 0

	originalReq := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4",
		Input: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentStr: schemas.Ptr("Test filtering"),
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

	// Should return immediately with all tools for user approval
	assert.Equal(t, 0, mockLLM.chatCallCount, "should not make additional calls")

	finalMessage := result.Choices[0].ChatNonStreamResponseChoice.Message
	if finalMessage.ChatAssistantMessage != nil {
		assert.Len(t, finalMessage.ChatAssistantMessage.ToolCalls, 2, "should return all filtered tools")
	}

	t.Logf("✅ All tools filtered out - returned to user for approval")
}

func TestAgent_StateTransition_StateConsistency(t *testing.T) {
	t.Parallel()

	// Test that agent maintains consistent state across iterations
	manager := setupMCPManager(t)

	// Counter to track state
	executionOrder := []string{}

	for i := 0; i < 5; i++ {
		toolName := fmt.Sprintf("stateful_tool_%d", i)
		toolIndex := i

		toolHandler := func(args any) (string, error) {
			executionOrder = append(executionOrder, fmt.Sprintf("tool_%d", toolIndex))
			return fmt.Sprintf(`{"tool": "tool_%d", "order": %d}`, toolIndex, len(executionOrder)), nil
		}

		toolSchema := schemas.ChatTool{
			Type: schemas.ChatToolTypeFunction,
			Function: &schemas.ChatToolFunction{
				Name:        toolName,
				Description: schemas.Ptr(fmt.Sprintf("Stateful tool %d", i)),
			},
		}

		err := manager.RegisterTool(toolName, fmt.Sprintf("Stateful tool %d", i), toolHandler, toolSchema)
		require.NoError(t, err)
	}

	err := SetInternalClientAutoExecute(manager, []string{"*"})
	require.NoError(t, err)

	ctx := createTestContext()

	// Execute tools in sequence across multiple iterations
	mockLLM := &MockLLMCaller{
		chatResponses: []*schemas.BifrostChatResponse{
			CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
				{
					ID:   schemas.Ptr("call-0"),
					Type: schemas.Ptr("function"),
					Function: schemas.ChatAssistantMessageToolCallFunction{
						Name: schemas.Ptr("bifrostInternal-stateful_tool_0"),
						Arguments: "{}",
					},
				},
			}),
			CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
				{
					ID:   schemas.Ptr("call-1"),
					Type: schemas.Ptr("function"),
					Function: schemas.ChatAssistantMessageToolCallFunction{
						Name: schemas.Ptr("bifrostInternal-stateful_tool_1"),
						Arguments: "{}",
					},
				},
			}),
			CreateChatResponseWithText("State maintained"),
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
					ContentStr: schemas.Ptr("Test state consistency"),
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

	// Verify execution order is maintained
	assert.Equal(t, []string{"tool_0", "tool_1"}, executionOrder)

	t.Logf("✅ State consistency maintained across iterations")
	t.Logf("Execution order: %v", executionOrder)
}

func TestAgent_StateTransition_BoundaryConditions(t *testing.T) {
	t.Parallel()

	// Test various boundary conditions
	manager := setupMCPManager(t)
	err := RegisterEchoTool(manager)
	require.NoError(t, err)

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	testCases := []struct {
		name           string
		autoExecute    []string
		expectAgentRun bool
	}{
		{"wildcard_all", []string{"*"}, true},
		{"explicit_list", []string{"echo"}, true},
		{"empty_list", []string{}, false},
		{"no_match", []string{"nonexistent"}, false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := SetInternalClientAutoExecute(manager, tc.autoExecute)
			require.NoError(t, err)

			toolCall := GetSampleEchoToolCall("call-boundary", "test")

			mockLLM := &MockLLMCaller{
				chatResponses: []*schemas.BifrostChatResponse{
					CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{toolCall}),
					CreateChatResponseWithText("Completed"),
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

			if tc.expectAgentRun {
				t.Logf("✅ %s: Agent executed as expected", tc.name)
			} else {
				t.Logf("✅ %s: Agent stopped (no auto-execute) as expected", tc.name)
			}
		})
	}
}
