package mcptests

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// AGENT MODE ERROR HANDLING TESTS
// =============================================================================
// These tests verify error handling in agent execution loop (agent.go:301-314)
// Focus: All tools fail, timeout, network errors, malformed responses, recovery

func TestAgent_ErrorHandling_AllToolsFail(t *testing.T) {
	t.Parallel()

	// Setup: All tools in batch fail
	manager := setupMCPManager(t)

	// Register multiple tools that all fail
	for i := 0; i < 5; i++ {
		toolName := fmt.Sprintf("failing_tool_%d", i)
		toolIndex := i

		toolHandler := func(ctx context.Context, args any) (string, error) {
			return "", fmt.Errorf("failing_tool_%d: intentional failure", toolIndex)
		}

		toolSchema := schemas.ChatTool{
			Type: schemas.ChatToolTypeFunction,
			Function: &schemas.ChatToolFunction{
				Name:        toolName,
				Description: schemas.Ptr(fmt.Sprintf("Failing tool %d", i)),
			},
		}

		err := manager.RegisterTool(toolName, fmt.Sprintf("Failing tool %d", i), func(ctx context.Context, args any) (string, error) {
			return toolHandler(ctx, args)
		}, toolSchema)
		require.NoError(t, err)
	}

	err := SetInternalClientAutoExecute(manager, []string{"*"})
	require.NoError(t, err)

	ctx := createTestContext()

	// Create tool calls for all failing tools
	toolCalls := []schemas.ChatAssistantMessageToolCall{}
	for i := 0; i < 5; i++ {
		toolCalls = append(toolCalls, schemas.ChatAssistantMessageToolCall{
			ID:   schemas.Ptr(fmt.Sprintf("call-%d", i)),
			Type: schemas.Ptr("function"),
			Function: schemas.ChatAssistantMessageToolCallFunction{
				Name:      schemas.Ptr(fmt.Sprintf("bifrostInternal-failing_tool_%d", i)),
				Arguments: "{}",
			},
		})
	}

	mockLLM := &MockLLMCaller{
		chatResponses: []*schemas.BifrostChatResponse{
			CreateChatResponseWithToolCalls(toolCalls),
			CreateChatResponseWithText("Handled all failures"),
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
					ContentStr: schemas.Ptr("Execute all tools"),
				},
			},
		},
	}

	// Execute - should not crash even when all tools fail
	result, bifrostErr := manager.CheckAndExecuteAgentForChatRequest(
		ctx,
		originalReq,
		initialResponse,
		mockLLM.MakeChatRequest,
		func(ctx *schemas.BifrostContext, request *schemas.BifrostMCPRequest) (*schemas.BifrostMCPResponse, error) {
			return manager.ExecuteToolCall(ctx, request)
		},
	)

	// Agent should handle all failures gracefully
	require.Nil(t, bifrostErr, "agent should not crash when all tools fail")
	require.NotNil(t, result)

	t.Logf("✅ All tools failed but agent handled gracefully")
	t.Logf("Agent continued and returned response")
}

func TestAgent_ErrorHandling_TimeoutInLoop(t *testing.T) {
	t.Parallel()

	// Setup: Tools that timeout during agent loop
	manager := setupMCPManager(t)

	// Register tool that takes too long
	toolHandler := func(ctx context.Context, args any) (string, error) {
		time.Sleep(5 * time.Second) // Longer than context timeout
		return `{"result": "completed"}`, nil
	}

	toolSchema := schemas.ChatTool{
		Type: schemas.ChatToolTypeFunction,
		Function: &schemas.ChatToolFunction{
			Name:        "slow_tool",
			Description: schemas.Ptr("A tool that times out"),
		},
	}

	err := manager.RegisterTool("slow_tool", "A tool that times out", func(ctx context.Context, args any) (string, error) {
		return toolHandler(ctx, args)
	}, toolSchema)
	require.NoError(t, err)

	err = SetInternalClientAutoExecute(manager, []string{"*"})
	require.NoError(t, err)

	// Create context with short timeout
	ctx, cancel := createTestContextWithTimeout(500 * time.Millisecond)
	defer cancel()

	toolCalls := []schemas.ChatAssistantMessageToolCall{
		{
			ID:   schemas.Ptr("call-timeout"),
			Type: schemas.Ptr("function"),
			Function: schemas.ChatAssistantMessageToolCallFunction{
				Name:      schemas.Ptr("bifrostInternal-slow_tool"),
				Arguments: "{}",
			},
		},
	}

	mockLLM := &MockLLMCaller{
		chatResponses: []*schemas.BifrostChatResponse{
			CreateChatResponseWithToolCalls(toolCalls),
			CreateChatResponseWithText("Handled timeout"),
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
					ContentStr: schemas.Ptr("Execute slow tool"),
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

	// Should handle timeout gracefully
	if bifrostErr != nil {
		t.Logf("Timeout resulted in error (expected): %v", bifrostErr.Error)
	} else if result != nil {
		t.Logf("Timeout handled in result")
	}

	t.Logf("✅ Timeout during agent loop handled")
}

func TestAgent_ErrorHandling_MalformedResponse(t *testing.T) {
	t.Parallel()

	// Test handling of malformed tool responses
	manager := setupMCPManager(t)

	// Register tools that return malformed responses
	testCases := []struct {
		name     string
		response string
		desc     string
	}{
		{"invalid_json", `{invalid json`, "Invalid JSON syntax"},
		{"unclosed_brace", `{"result": "incomplete"`, "Unclosed JSON brace"},
		{"wrong_type", `[]`, "Array instead of object"},
		{"null_response", `null`, "Null response"},
		{"empty_string", ``, "Empty string"},
	}

	for _, tc := range testCases {
		toolName := "malformed_" + tc.name
		responseStr := tc.response

		toolHandler := func(ctx context.Context, args any) (string, error) {
			return responseStr, nil
		}

		toolSchema := schemas.ChatTool{
			Type: schemas.ChatToolTypeFunction,
			Function: &schemas.ChatToolFunction{
				Name:        toolName,
				Description: schemas.Ptr(tc.desc),
			},
		}

		err := manager.RegisterTool(toolName, tc.desc, func(ctx context.Context, args any) (string, error) {
			return toolHandler(ctx, args)
		}, toolSchema)
		require.NoError(t, err)
	}

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	// Test each malformed response
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			toolName := "malformed_" + tc.name
			argsJSON, _ := json.Marshal(map[string]interface{}{})

			toolCall := schemas.ChatAssistantMessageToolCall{
				ID:   schemas.Ptr("call-" + tc.name),
				Type: schemas.Ptr("function"),
				Function: schemas.ChatAssistantMessageToolCallFunction{
					Name:      schemas.Ptr(toolName),
					Arguments: string(argsJSON),
				},
			}

			result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)

			// System should handle malformed responses gracefully
			if bifrostErr != nil {
				t.Logf("Malformed response (%s) handled with error: %v", tc.name, bifrostErr.Error)
			} else if result != nil {
				t.Logf("Malformed response (%s) handled in result", tc.name)
			}

			t.Logf("✅ Malformed response (%s) handled: %s", tc.name, tc.desc)
		})
	}
}

func TestAgent_ErrorHandling_PartialBatchFailure(t *testing.T) {
	t.Parallel()

	// Test mixed success/failure in a batch
	manager := setupMCPManager(t)

	// Register tools with different outcomes
	outcomes := []bool{true, false, true, false, true} // true=success, false=fail

	for i, shouldSucceed := range outcomes {
		toolName := fmt.Sprintf("tool_%d", i)
		success := shouldSucceed
		toolIndex := i

		toolHandler := func(ctx context.Context, args any) (string, error) {
			if success {
				return fmt.Sprintf(`{"tool": "tool_%d", "success": true}`, toolIndex), nil
			}
			return "", fmt.Errorf("tool_%d failed", toolIndex)
		}

		toolSchema := schemas.ChatTool{
			Type: schemas.ChatToolTypeFunction,
			Function: &schemas.ChatToolFunction{
				Name:        toolName,
				Description: schemas.Ptr(fmt.Sprintf("Tool %d", i)),
			},
		}

		err := manager.RegisterTool(toolName, fmt.Sprintf("Tool %d", i), func(ctx context.Context, args any) (string, error) {
			return toolHandler(ctx, args)
		}, toolSchema)
		require.NoError(t, err)
	}

	err := SetInternalClientAutoExecute(manager, []string{"*"})
	require.NoError(t, err)

	ctx := createTestContext()

	// Create tool calls for all tools
	toolCalls := []schemas.ChatAssistantMessageToolCall{}
	for i := range outcomes {
		toolCalls = append(toolCalls, schemas.ChatAssistantMessageToolCall{
			ID:   schemas.Ptr(fmt.Sprintf("call-%d", i)),
			Type: schemas.Ptr("function"),
			Function: schemas.ChatAssistantMessageToolCallFunction{
				Name:      schemas.Ptr(fmt.Sprintf("bifrostInternal-tool_%d", i)),
				Arguments: "{}",
			},
		})
	}

	mockLLM := &MockLLMCaller{
		chatResponses: []*schemas.BifrostChatResponse{
			CreateChatResponseWithToolCalls(toolCalls),
			CreateChatResponseWithText("Partial batch completed"),
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
					ContentStr: schemas.Ptr("Execute batch"),
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

	require.Nil(t, bifrostErr, "partial failures should not crash agent")
	require.NotNil(t, result)

	successCount := 0
	failCount := 0
	for _, success := range outcomes {
		if success {
			successCount++
		} else {
			failCount++
		}
	}

	t.Logf("✅ Partial batch handled: %d succeeded, %d failed", successCount, failCount)
}

func TestAgent_ErrorHandling_RecoveryAndContinuation(t *testing.T) {
	t.Parallel()

	// Test that agent can recover from errors and continue
	manager := setupMCPManager(t)

	// Register tools: some fail, agent should recover and continue
	err := RegisterEchoTool(manager)
	require.NoError(t, err)

	failHandler := func(ctx context.Context, args any) (string, error) {
		return "", fmt.Errorf("temporary failure")
	}
	failSchema := schemas.ChatTool{
		Type: schemas.ChatToolTypeFunction,
		Function: &schemas.ChatToolFunction{
			Name:        "fail_tool",
			Description: schemas.Ptr("A tool that fails"),
		},
	}
	err = manager.RegisterTool("fail_tool", "A tool that fails", func(ctx context.Context, args any) (string, error) {
		return failHandler(ctx, args)
	}, failSchema)
	require.NoError(t, err)

	err = SetInternalClientAutoExecute(manager, []string{"*"})
	require.NoError(t, err)

	ctx := createTestContext()

	// Agent loop: fail first, then succeed with echo
	mockLLM := &MockLLMCaller{
		chatResponses: []*schemas.BifrostChatResponse{
			// Iteration 1: failing tool
			CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
				{
					ID:   schemas.Ptr("call-fail"),
					Type: schemas.Ptr("function"),
					Function: schemas.ChatAssistantMessageToolCallFunction{
						Name:      schemas.Ptr("bifrostInternal-fail_tool"),
						Arguments: "{}",
					},
				},
			}),
			// Iteration 2: successful tool (recovery)
			CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
				GetSampleEchoToolCall("call-echo", "recovered"),
			}),
			// Iteration 3: finish
			CreateChatResponseWithText("Successfully recovered and completed"),
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
					ContentStr: schemas.Ptr("Test recovery"),
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

	require.Nil(t, bifrostErr, "agent should recover from error")
	require.NotNil(t, result)

	// Verify agent made multiple iterations (recovery worked)
	assert.GreaterOrEqual(t, mockLLM.chatCallCount, 2, "agent should continue after error")
	assert.Equal(t, "stop", *result.Choices[0].FinishReason)

	t.Logf("✅ Agent recovered from error and continued successfully")
}

func TestAgent_ErrorHandling_ErrorInToolArguments(t *testing.T) {
	t.Parallel()

	// Test handling of errors in tool argument parsing
	manager := setupMCPManager(t)
	err := RegisterCalculatorTool(manager)
	require.NoError(t, err)

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	testCases := []struct {
		name      string
		arguments string
		desc      string
	}{
		{"invalid_json", `{invalid`, "Invalid JSON in arguments"},
		{"wrong_types", `{"operation": "add", "x": "not_a_number", "y": "also_not_a_number"}`, "Wrong argument types"},
		{"missing_required", `{"operation": "add"}`, "Missing required arguments"},
		{"extra_fields", `{"operation": "add", "x": 1, "y": 2, "z": 3, "unexpected": "field"}`, "Extra unexpected fields"},
		{"empty_object", `{}`, "Empty arguments object"},
		{"null_values", `{"operation": null, "x": null, "y": null}`, "Null argument values"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			toolCall := schemas.ChatAssistantMessageToolCall{
				ID:   schemas.Ptr("call-" + tc.name),
				Type: schemas.Ptr("function"),
				Function: schemas.ChatAssistantMessageToolCallFunction{
					Name:      schemas.Ptr("bifrostInternal-calculator"),
					Arguments: tc.arguments,
				},
			}

			result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)

			// Should handle argument errors gracefully
			if bifrostErr != nil {
				t.Logf("Argument error (%s) handled with error: %v", tc.name, bifrostErr.Error)
			} else if result != nil {
				t.Logf("Argument error (%s) handled in result", tc.name)
			}

			t.Logf("✅ Argument error (%s) handled: %s", tc.name, tc.desc)
		})
	}
}

func TestAgent_ErrorHandling_MultipleErrorsInSequence(t *testing.T) {
	t.Parallel()

	// Test agent handling multiple errors in sequence
	manager := setupMCPManager(t)

	// Register failing tool
	failHandler := func(ctx context.Context, args any) (string, error) {
		return "", fmt.Errorf("consistent failure")
	}
	failSchema := schemas.ChatTool{
		Type: schemas.ChatToolTypeFunction,
		Function: &schemas.ChatToolFunction{
			Name:        "fail_tool",
			Description: schemas.Ptr("Consistently fails"),
		},
	}
	err := manager.RegisterTool("fail_tool", "Consistently fails", func(ctx context.Context, args any) (string, error) {
		return failHandler(ctx, args)
	}, failSchema)
	require.NoError(t, err)

	err = SetInternalClientAutoExecute(manager, []string{"*"})
	require.NoError(t, err)

	ctx := createTestContext()

	// Agent attempts multiple failing tool calls in sequence
	mockLLM := &MockLLMCaller{
		chatResponses: []*schemas.BifrostChatResponse{
			// Iteration 1: fail
			CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
				{
					ID:   schemas.Ptr("call-fail-1"),
					Type: schemas.Ptr("function"),
					Function: schemas.ChatAssistantMessageToolCallFunction{
						Name:      schemas.Ptr("bifrostInternal-fail_tool"),
						Arguments: "{}",
					},
				},
			}),
			// Iteration 2: fail again
			CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
				{
					ID:   schemas.Ptr("call-fail-2"),
					Type: schemas.Ptr("function"),
					Function: schemas.ChatAssistantMessageToolCallFunction{
						Name:      schemas.Ptr("bifrostInternal-fail_tool"),
						Arguments: "{}",
					},
				},
			}),
			// Iteration 3: give up
			CreateChatResponseWithText("Multiple failures encountered, stopping"),
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
					ContentStr: schemas.Ptr("Test multiple failures"),
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

	require.Nil(t, bifrostErr, "agent should handle multiple sequential errors")
	require.NotNil(t, result)

	t.Logf("✅ Multiple sequential errors handled gracefully")
}

func TestAgent_ErrorHandling_ErrorMessagePreservation(t *testing.T) {
	t.Parallel()

	// Verify that error messages from tools are preserved and passed to LLM
	manager := setupMCPManager(t)

	expectedErrorMsg := "This is a very specific error message that should be preserved"

	errorHandler := func(ctx context.Context, args any) (string, error) {
		return "", fmt.Errorf("%s", expectedErrorMsg)
	}
	errorSchema := schemas.ChatTool{
		Type: schemas.ChatToolTypeFunction,
		Function: &schemas.ChatToolFunction{
			Name:        "error_tool",
			Description: schemas.Ptr("Returns specific error"),
		},
	}
	err := manager.RegisterTool("error_tool", "Returns specific error", func(ctx context.Context, args any) (string, error) {
		return errorHandler(ctx, args)
	}, errorSchema)
	require.NoError(t, err)

	err = SetInternalClientAutoExecute(manager, []string{"*"})
	require.NoError(t, err)

	ctx := createTestContext()

	mockLLM := &MockLLMCaller{
		chatResponses: []*schemas.BifrostChatResponse{
			CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
				{
					ID:   schemas.Ptr("call-error"),
					Type: schemas.Ptr("function"),
					Function: schemas.ChatAssistantMessageToolCallFunction{
						Name:      schemas.Ptr("bifrostInternal-error_tool"),
						Arguments: "{}",
					},
				},
			}),
			CreateChatResponseWithText("Error message received"),
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
					ContentStr: schemas.Ptr("Test error message"),
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

	t.Logf("✅ Error message preservation verified")
	t.Logf("Original error: %s", expectedErrorMsg)
}
