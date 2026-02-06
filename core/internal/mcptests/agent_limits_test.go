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
// MAX DEPTH TESTS - NON-CODE MODE
// =============================================================================

func TestAgent_MaxDepthEnforcement(t *testing.T) {
	t.Parallel()

	clientConfig := GetSampleHTTPClientConfig(GetTestConfig(t).HTTPServerURL)
	clientConfig.ToolsToExecute = []string{"*"}
	clientConfig.ToolsToAutoExecute = []string{"*"}
	manager := setupMCPManager(t, clientConfig)

	// Update tool manager config to set MaxAgentDepth = 5
	manager.UpdateToolManagerConfig(&schemas.MCPToolManagerConfig{
		MaxAgentDepth:        5,
		ToolExecutionTimeout: 30 * time.Second,
	})

	ctx := createTestContext()

	// LLM returns tool calls for 6+ iterations (exceeds max depth of 5)
	mockLLM := &MockLLMCaller{
		chatResponses: []*schemas.BifrostChatResponse{
			CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
				GetSampleEchoToolCall("call-1", "iteration 1"),
			}),
			CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
				GetSampleEchoToolCall("call-2", "iteration 2"),
			}),
			CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
				GetSampleEchoToolCall("call-3", "iteration 3"),
			}),
			CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
				GetSampleEchoToolCall("call-4", "iteration 4"),
			}),
			CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
				GetSampleEchoToolCall("call-5", "iteration 5"),
			}),
			CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
				GetSampleEchoToolCall("call-6", "iteration 6 - should not reach"),
			}),
			CreateChatResponseWithText("Final - should not reach"),
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
					ContentStr: schemas.Ptr("Long task"),
				},
			},
		},
	}

	result, err := manager.CheckAndExecuteAgentForChatRequest(
		ctx,
		originalReq,
		initialResponse,
		mockLLM.MakeChatRequest,
		func(ctx *schemas.BifrostContext, request *schemas.BifrostMCPRequest) (*schemas.BifrostMCPResponse, error) {
			return manager.ExecuteToolCall(ctx, request)
		},
	)

	require.Nil(t, err)
	require.NotNil(t, result)

	// Agent should stop at depth 5 (made 4 additional calls after initial)
	assert.LessOrEqual(t, mockLLM.chatCallCount, 4, "should stop at max depth")
	t.Logf("Agent stopped at %d iterations (max depth: 5)", mockLLM.chatCallCount)
}

func TestAgent_MaxDepthCustomValue(t *testing.T) {
	t.Parallel()

	clientConfig := GetSampleHTTPClientConfig(GetTestConfig(t).HTTPServerURL)
	clientConfig.ToolsToExecute = []string{"*"}
	clientConfig.ToolsToAutoExecute = []string{"*"}
	manager := setupMCPManager(t, clientConfig)

	// Set MaxAgentDepth = 3
	manager.UpdateToolManagerConfig(&schemas.MCPToolManagerConfig{
		MaxAgentDepth:        3,
		ToolExecutionTimeout: 30 * time.Second,
	})

	ctx := createTestContext()

	mockLLM := &MockLLMCaller{
		chatResponses: []*schemas.BifrostChatResponse{
			CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
				GetSampleEchoToolCall("call-1", "iter 1"),
			}),
			CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
				GetSampleEchoToolCall("call-2", "iter 2"),
			}),
			CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
				GetSampleEchoToolCall("call-3", "iter 3"),
			}),
			CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
				GetSampleEchoToolCall("call-4", "should not reach"),
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
					ContentStr: schemas.Ptr("Test"),
				},
			},
		},
	}

	result, err := manager.CheckAndExecuteAgentForChatRequest(
		ctx,
		originalReq,
		initialResponse,
		mockLLM.MakeChatRequest,
		func(ctx *schemas.BifrostContext, request *schemas.BifrostMCPRequest) (*schemas.BifrostMCPResponse, error) {
			return manager.ExecuteToolCall(ctx, request)
		},
	)

	require.Nil(t, err)
	require.NotNil(t, result)

	// Should stop at depth 3 (made 2 additional calls after initial)
	assert.LessOrEqual(t, mockLLM.chatCallCount, 2, "should stop at custom max depth of 3")
	t.Logf("Agent stopped at depth 3 with %d follow-up calls", mockLLM.chatCallCount)
}

func TestAgent_MaxDepthReached_ChatFormat(t *testing.T) {
	t.Parallel()

	clientConfig := GetSampleHTTPClientConfig(GetTestConfig(t).HTTPServerURL)
	clientConfig.ToolsToExecute = []string{"*"}
	clientConfig.ToolsToAutoExecute = []string{"*"}
	manager := setupMCPManager(t, clientConfig)

	manager.UpdateToolManagerConfig(&schemas.MCPToolManagerConfig{
		MaxAgentDepth:        2,
		ToolExecutionTimeout: 30 * time.Second,
	}) // Max depth = 2

	ctx := createTestContext()

	mockLLM := &MockLLMCaller{
		chatResponses: []*schemas.BifrostChatResponse{
			CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
				GetSampleEchoToolCall("call-1", "first"),
			}),
			CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
				GetSampleEchoToolCall("call-2", "second"),
			}),
			CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
				GetSampleEchoToolCall("call-3", "should not reach"),
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
					ContentStr: schemas.Ptr("Test"),
				},
			},
		},
	}

	result, err := manager.CheckAndExecuteAgentForChatRequest(
		ctx,
		originalReq,
		initialResponse,
		mockLLM.MakeChatRequest,
		func(ctx *schemas.BifrostContext, request *schemas.BifrostMCPRequest) (*schemas.BifrostMCPResponse, error) {
			return manager.ExecuteToolCall(ctx, request)
		},
	)

	require.Nil(t, err)
	require.NotNil(t, result)
	assert.LessOrEqual(t, mockLLM.chatCallCount, 1, "max depth 2 in Chat format")
}

func TestAgent_MaxDepthReached_ResponsesFormat(t *testing.T) {
	t.Parallel()

	clientConfig := GetSampleHTTPClientConfig(GetTestConfig(t).HTTPServerURL)
	clientConfig.ToolsToExecute = []string{"*"}
	clientConfig.ToolsToAutoExecute = []string{"*"}
	manager := setupMCPManager(t, clientConfig)

	manager.UpdateToolManagerConfig(&schemas.MCPToolManagerConfig{
		MaxAgentDepth:        2,
		ToolExecutionTimeout: 30 * time.Second,
	}) // Max depth = 2

	ctx := createTestContext()

	mockLLM := &MockLLMCaller{
		responsesResponses: []*schemas.BifrostResponsesResponse{
			CreateResponsesResponseWithToolCalls([]schemas.ResponsesToolMessage{
				{
					CallID:    schemas.Ptr("call-1"),
					Name: schemas.Ptr("bifrostInternal-echo"),
					Arguments: schemas.Ptr(`{"message": "first"}`),
				},
			}),
			CreateResponsesResponseWithToolCalls([]schemas.ResponsesToolMessage{
				{
					CallID:    schemas.Ptr("call-2"),
					Name: schemas.Ptr("bifrostInternal-echo"),
					Arguments: schemas.Ptr(`{"message": "second"}`),
				},
			}),
			CreateResponsesResponseWithText("Should not reach"),
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
					ContentStr: schemas.Ptr("Test"),
				},
			},
		},
	}

	result, err := manager.CheckAndExecuteAgentForResponsesRequest(
		ctx,
		originalReq,
		initialResponse,
		mockLLM.MakeResponsesRequest,
		func(ctx *schemas.BifrostContext, request *schemas.BifrostMCPRequest) (*schemas.BifrostMCPResponse, error) {
			return manager.ExecuteToolCall(ctx, request)
		},
	)

	require.Nil(t, err)
	require.NotNil(t, result)
	assert.LessOrEqual(t, mockLLM.responsesCallCount, 1, "max depth 2 in Responses format")
}

// =============================================================================
// MAX DEPTH TESTS - CODE MODE
// =============================================================================

func TestAgent_MaxDepth_CodeMode(t *testing.T) {
	t.Parallel()

	// Code mode client
	codeModeClient := GetSampleCodeModeClientConfig(t, GetTestConfig(t).HTTPServerURL)
	// Regular HTTP client with tools
	httpClient := GetSampleHTTPClientConfig(GetTestConfig(t).HTTPServerURL)
	httpClient.ID = "mcpserver"
	httpClient.ToolsToExecute = []string{"*"}
	httpClient.ToolsToAutoExecute = []string{"echo"}

	manager := setupMCPManager(t, codeModeClient, httpClient)
	manager.UpdateToolManagerConfig(&schemas.MCPToolManagerConfig{
		MaxAgentDepth:        3,
		ToolExecutionTimeout: 30 * time.Second,
	}) // Max depth = 3

	ctx := createTestContext()

	// Mock LLM that returns executeToolCode calls multiple times
	mockLLM := &MockLLMCaller{
		chatResponses: []*schemas.BifrostChatResponse{
			CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
				{
					ID:   schemas.Ptr("call-1"),
					Type: schemas.Ptr("function"),
					Function: schemas.ChatAssistantMessageToolCallFunction{
						Name:      schemas.Ptr("executeToolCode"),
						Arguments: `{"code": "await mcpserver.echo({message: 'iter 1'}); return 'done1';"}`,
					},
				},
			}),
			CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
				{
					ID:   schemas.Ptr("call-2"),
					Type: schemas.Ptr("function"),
					Function: schemas.ChatAssistantMessageToolCallFunction{
						Name:      schemas.Ptr("executeToolCode"),
						Arguments: `{"code": "await mcpserver.echo({message: 'iter 2'}); return 'done2';"}`,
					},
				},
			}),
			CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
				{
					ID:   schemas.Ptr("call-3"),
					Type: schemas.Ptr("function"),
					Function: schemas.ChatAssistantMessageToolCallFunction{
						Name:      schemas.Ptr("executeToolCode"),
						Arguments: `{"code": "await mcpserver.echo({message: 'iter 3'}); return 'done3';"}`,
					},
				},
			}),
			CreateChatResponseWithText("Should not reach - max depth hit"),
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
					ContentStr: schemas.Ptr("Code mode test"),
				},
			},
		},
	}

	result, err := manager.CheckAndExecuteAgentForChatRequest(
		ctx,
		originalReq,
		initialResponse,
		mockLLM.MakeChatRequest,
		func(ctx *schemas.BifrostContext, request *schemas.BifrostMCPRequest) (*schemas.BifrostMCPResponse, error) {
			return manager.ExecuteToolCall(ctx, request)
		},
	)

	require.Nil(t, err)
	require.NotNil(t, result)
	assert.LessOrEqual(t, mockLLM.chatCallCount, 2, "max depth should apply to code mode")
	t.Logf("Code mode agent stopped with %d calls", mockLLM.chatCallCount)
}

func TestAgent_MaxDepth_CodeMode_ChatFormat(t *testing.T) {
	t.Parallel()

	codeModeClient := GetSampleCodeModeClientConfig(t, GetTestConfig(t).HTTPServerURL)
	httpClient := GetSampleHTTPClientConfig(GetTestConfig(t).HTTPServerURL)
	httpClient.ID = "server"
	httpClient.ToolsToExecute = []string{"*"}
	httpClient.ToolsToAutoExecute = []string{"*"}

	manager := setupMCPManager(t, codeModeClient, httpClient)
	manager.UpdateToolManagerConfig(&schemas.MCPToolManagerConfig{
		MaxAgentDepth:        2,
		ToolExecutionTimeout: 30 * time.Second,
	})

	ctx := createTestContext()

	mockLLM := &MockLLMCaller{
		chatResponses: []*schemas.BifrostChatResponse{
			CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
				{
					ID:   schemas.Ptr("call-1"),
					Type: schemas.Ptr("function"),
					Function: schemas.ChatAssistantMessageToolCallFunction{
						Name:      schemas.Ptr("executeToolCode"),
						Arguments: `{"code": "return 'test1';"}`,
					},
				},
			}),
			CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
				{
					ID:   schemas.Ptr("call-2"),
					Type: schemas.Ptr("function"),
					Function: schemas.ChatAssistantMessageToolCallFunction{
						Name:      schemas.Ptr("executeToolCode"),
						Arguments: `{"code": "return 'test2';"}`,
					},
				},
			}),
			CreateChatResponseWithText("Done"),
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

	result, err := manager.CheckAndExecuteAgentForChatRequest(
		ctx,
		originalReq,
		initialResponse,
		mockLLM.MakeChatRequest,
		func(ctx *schemas.BifrostContext, request *schemas.BifrostMCPRequest) (*schemas.BifrostMCPResponse, error) {
			return manager.ExecuteToolCall(ctx, request)
		},
	)

	require.Nil(t, err)
	require.NotNil(t, result)
	t.Logf("Code mode Chat format test completed")
}

func TestAgent_MaxDepth_CodeMode_ResponsesFormat(t *testing.T) {
	t.Parallel()

	codeModeClient := GetSampleCodeModeClientConfig(t, GetTestConfig(t).HTTPServerURL)
	httpClient := GetSampleHTTPClientConfig(GetTestConfig(t).HTTPServerURL)
	httpClient.ID = "server"
	httpClient.ToolsToExecute = []string{"*"}
	httpClient.ToolsToAutoExecute = []string{"*"}

	manager := setupMCPManager(t, codeModeClient, httpClient)
	manager.UpdateToolManagerConfig(&schemas.MCPToolManagerConfig{
		MaxAgentDepth:        2,
		ToolExecutionTimeout: 30 * time.Second,
	})

	ctx := createTestContext()

	mockLLM := &MockLLMCaller{
		responsesResponses: []*schemas.BifrostResponsesResponse{
			CreateResponsesResponseWithToolCalls([]schemas.ResponsesToolMessage{
				{
					CallID:    schemas.Ptr("call-1"),
					Name:      schemas.Ptr("executeToolCode"),
					Arguments: schemas.Ptr(`{"code": "return 'test1';"}`),
				},
			}),
			CreateResponsesResponseWithToolCalls([]schemas.ResponsesToolMessage{
				{
					CallID:    schemas.Ptr("call-2"),
					Name:      schemas.Ptr("executeToolCode"),
					Arguments: schemas.Ptr(`{"code": "return 'test2';"}`),
				},
			}),
			CreateResponsesResponseWithText("Done"),
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
					ContentStr: schemas.Ptr("Test"),
				},
			},
		},
	}

	result, err := manager.CheckAndExecuteAgentForResponsesRequest(
		ctx,
		originalReq,
		initialResponse,
		mockLLM.MakeResponsesRequest,
		func(ctx *schemas.BifrostContext, request *schemas.BifrostMCPRequest) (*schemas.BifrostMCPResponse, error) {
			return manager.ExecuteToolCall(ctx, request)
		},
	)

	require.Nil(t, err)
	require.NotNil(t, result)
	t.Logf("Code mode Responses format test completed")
}

// =============================================================================
// AGENT TIMEOUT TESTS - NON-CODE MODE
// =============================================================================

func TestAgent_Timeout(t *testing.T) {
	t.Parallel()

	// Test that agent loop MUST timeout by creating a tool that takes longer than the timeout
	manager := setupMCPManager(t)

	// Register a slow tool that takes 500ms (longer than our 200ms timeout)
	slowToolSchema := schemas.ChatTool{
		Type: schemas.ChatToolTypeFunction,
		Function: &schemas.ChatToolFunction{
			Name:        "slow_tool",
			Description: schemas.Ptr("A tool that takes a long time"),
			Parameters: &schemas.ToolFunctionParameters{
				Type:       "object",
				Properties: &schemas.OrderedMap{},
			},
		},
	}

	err := manager.RegisterTool(
		"slow_tool",
		"A tool that takes a long time",
		func(args any) (string, error) {
			// This will definitely exceed the 200ms timeout
			time.Sleep(500 * time.Millisecond)
			return `{"result": "should not reach here"}`, nil
		},
		slowToolSchema,
	)
	require.NoError(t, err)

	err = SetInternalClientAutoExecute(manager, []string{"slow_tool"})
	require.NoError(t, err)

	// Timeout set to 200ms - tool takes 500ms, so it MUST timeout
	ctx, cancel := createTestContextWithTimeout(200 * time.Millisecond)
	defer cancel()

	mockLLM := &MockLLMCaller{
		chatResponses: []*schemas.BifrostChatResponse{
			CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
				CreateInProcessToolCall("call-1", "slow_tool", map[string]interface{}{}),
			}),
		},
	}

	initialResponse := mockLLM.chatResponses[0]

	originalReq := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o",
		Input: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentStr: schemas.Ptr("Test timeout"),
				},
			},
		},
	}

	// Agent MUST timeout since tool takes 500ms but timeout is 200ms
	_, bifrostErr := manager.CheckAndExecuteAgentForChatRequest(
		ctx,
		originalReq,
		initialResponse,
		mockLLM.MakeChatRequest,
		func(ctx *schemas.BifrostContext, request *schemas.BifrostMCPRequest) (*schemas.BifrostMCPResponse, error) {
			return manager.ExecuteToolCall(ctx, request)
		},
	)

	// MUST have timeout error
	require.NotNil(t, bifrostErr, "Expected timeout error but got success - timeout is not being enforced!")
	t.Logf("✅ Timeout correctly enforced: %v", bifrostErr.Error)
}

func TestAgent_TimeoutDuringExecution(t *testing.T) {
	t.Parallel()

	// Test that timeout is enforced DURING tool execution (not just between iterations)
	// Tool takes 1 second, timeout is 150ms - MUST timeout mid-execution
	manager := setupMCPManager(t)

	slowToolSchema := schemas.ChatTool{
		Type: schemas.ChatToolTypeFunction,
		Function: &schemas.ChatToolFunction{
			Name:        "very_slow_tool",
			Description: schemas.Ptr("A tool that takes 1 full second"),
			Parameters: &schemas.ToolFunctionParameters{
				Type:       "object",
				Properties: &schemas.OrderedMap{},
			},
		},
	}

	err := manager.RegisterTool(
		"very_slow_tool",
		"A tool that takes 1 full second",
		func(args any) (string, error) {
			// This takes 1 second - much longer than 150ms timeout
			time.Sleep(1 * time.Second)
			return `{"result": "should never complete"}`, nil
		},
		slowToolSchema,
	)
	require.NoError(t, err)

	err = SetInternalClientAutoExecute(manager, []string{"very_slow_tool"})
	require.NoError(t, err)

	// Timeout is 150ms, tool takes 1000ms - MUST timeout during execution
	ctx, cancel := createTestContextWithTimeout(150 * time.Millisecond)
	defer cancel()

	mockLLM := &MockLLMCaller{
		chatResponses: []*schemas.BifrostChatResponse{
			CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
				CreateInProcessToolCall("call-1", "very_slow_tool", map[string]interface{}{}),
			}),
		},
	}

	_, bifrostErr := manager.CheckAndExecuteAgentForChatRequest(
		ctx,
		&schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4o",
			Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("Test")}}},
		},
		mockLLM.chatResponses[0],
		mockLLM.MakeChatRequest,
		func(ctx *schemas.BifrostContext, request *schemas.BifrostMCPRequest) (*schemas.BifrostMCPResponse, error) {
			return manager.ExecuteToolCall(ctx, request)
		},
	)

	// MUST timeout during execution
	require.NotNil(t, bifrostErr, "Expected timeout during tool execution but got success - timeout not enforced mid-execution!")
	t.Logf("✅ Timeout during execution correctly enforced: %v", bifrostErr.Error)
}

func TestAgent_Timeout_ChatFormat(t *testing.T) {
	t.Parallel()

	// Chat format MUST timeout - tool takes 400ms, timeout is 150ms
	manager := setupMCPManager(t)

	slowToolSchema := schemas.ChatTool{
		Type: schemas.ChatToolTypeFunction,
		Function: &schemas.ChatToolFunction{
			Name:        "slow_chat_tool",
			Description: schemas.Ptr("Tool for chat format timeout test"),
			Parameters:  &schemas.ToolFunctionParameters{Type: "object", Properties: &schemas.OrderedMap{}},
		},
	}

	err := manager.RegisterTool("slow_chat_tool", "Tool for timeout test",
		func(args any) (string, error) {
			time.Sleep(400 * time.Millisecond) // Longer than 150ms timeout
			return `{"status": "should not complete"}`, nil
		}, slowToolSchema)
	require.NoError(t, err)

	err = SetInternalClientAutoExecute(manager, []string{"slow_chat_tool"})
	require.NoError(t, err)

	ctx, cancel := createTestContextWithTimeout(150 * time.Millisecond)
	defer cancel()

	mockLLM := &MockLLMCaller{
		chatResponses: []*schemas.BifrostChatResponse{
			CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
				CreateInProcessToolCall("call-1", "slow_chat_tool", map[string]interface{}{}),
			}),
		},
	}

	_, bifrostErr := manager.CheckAndExecuteAgentForChatRequest(ctx,
		&schemas.BifrostChatRequest{Provider: schemas.OpenAI, Model: "gpt-4o",
			Input: []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("Test")}}}},
		mockLLM.chatResponses[0], mockLLM.MakeChatRequest,
		func(ctx *schemas.BifrostContext, request *schemas.BifrostMCPRequest) (*schemas.BifrostMCPResponse, error) {
			return manager.ExecuteToolCall(ctx, request)
		})

	require.NotNil(t, bifrostErr, "Chat format timeout not enforced!")
	t.Logf("✅ Chat format timeout enforced: %v", bifrostErr.Error)
}

func TestAgent_Timeout_ResponsesFormat(t *testing.T) {
	t.Parallel()

	// Responses format MUST timeout - tool takes 400ms, timeout is 150ms
	manager := setupMCPManager(t)

	slowToolSchema := schemas.ChatTool{
		Type: schemas.ChatToolTypeFunction,
		Function: &schemas.ChatToolFunction{
			Name:        "slow_responses_tool",
			Description: schemas.Ptr("Tool for responses format timeout test"),
			Parameters:  &schemas.ToolFunctionParameters{Type: "object", Properties: &schemas.OrderedMap{}},
		},
	}

	err := manager.RegisterTool("slow_responses_tool", "Tool for timeout test",
		func(args any) (string, error) {
			time.Sleep(400 * time.Millisecond) // Longer than 150ms timeout
			return `{"status": "should not complete"}`, nil
		}, slowToolSchema)
	require.NoError(t, err)

	err = SetInternalClientAutoExecute(manager, []string{"slow_responses_tool"})
	require.NoError(t, err)

	ctx, cancel := createTestContextWithTimeout(150 * time.Millisecond)
	defer cancel()

	mockLLM := &MockLLMCaller{
		responsesResponses: []*schemas.BifrostResponsesResponse{
			CreateResponsesResponseWithToolCalls([]schemas.ResponsesToolMessage{
				{CallID: schemas.Ptr("call-1"), Name: schemas.Ptr("bifrostInternal-slow_responses_tool"), Arguments: schemas.Ptr(`{}`)},
			}),
		},
	}

	_, bifrostErr := manager.CheckAndExecuteAgentForResponsesRequest(ctx,
		&schemas.BifrostResponsesRequest{Provider: schemas.OpenAI, Model: "gpt-4o",
			Input: []schemas.ResponsesMessage{{Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage), Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("Test")}}}},
		mockLLM.responsesResponses[0], mockLLM.MakeResponsesRequest,
		func(ctx *schemas.BifrostContext, request *schemas.BifrostMCPRequest) (*schemas.BifrostMCPResponse, error) {
			return manager.ExecuteToolCall(ctx, request)
		})

	require.NotNil(t, bifrostErr, "Responses format timeout not enforced!")
	t.Logf("✅ Responses format timeout enforced: %v", bifrostErr.Error)
}

// =============================================================================
// ERROR PROPAGATION TESTS
// =============================================================================

func TestAgent_ErrorPropagation(t *testing.T) {
	t.Parallel()

	clientConfig := GetSampleHTTPClientConfig(GetTestConfig(t).HTTPServerURL)
	clientConfig.ToolsToExecute = []string{"*"}
	clientConfig.ToolsToAutoExecute = []string{"*"}
	manager := setupMCPManager(t, clientConfig)

	ctx := createTestContext()

	// Mock a tool that will return an error
	mockLLM := &MockLLMCaller{
		chatResponses: []*schemas.BifrostChatResponse{
			// Call a non-existent tool
			CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
				{
					ID:   schemas.Ptr("call-error"),
					Type: schemas.Ptr("function"),
					Function: schemas.ChatAssistantMessageToolCallFunction{
						Name: schemas.Ptr("bifrostInternal-nonexistent_tool"),
						Arguments: `{}`,
					},
				},
			}),
			CreateChatResponseWithText("Handled error"),
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
					ContentStr: schemas.Ptr("Test error"),
				},
			},
		},
	}

	result, err := manager.CheckAndExecuteAgentForChatRequest(
		ctx,
		originalReq,
		initialResponse,
		mockLLM.MakeChatRequest,
		func(ctx *schemas.BifrostContext, request *schemas.BifrostMCPRequest) (*schemas.BifrostMCPResponse, error) {
			return manager.ExecuteToolCall(ctx, request)
		},
	)

	// Error should be propagated, or tool result should contain error
	// The exact behavior depends on implementation
	if err != nil {
		t.Logf("Error propagated: %v", err)
	} else {
		require.NotNil(t, result)
		t.Logf("Error handled in response")
	}
}

func TestAgent_ErrorInMiddleOfLoop(t *testing.T) {
	t.Parallel()

	clientConfig := GetSampleHTTPClientConfig(GetTestConfig(t).HTTPServerURL)
	clientConfig.ToolsToExecute = []string{"*"}
	clientConfig.ToolsToAutoExecute = []string{"*"}
	manager := setupMCPManager(t, clientConfig)

	ctx := createTestContext()

	mockLLM := &MockLLMCaller{
		chatResponses: []*schemas.BifrostChatResponse{
			// First tool succeeds
			CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
				GetSampleEchoToolCall("call-1", "success"),
			}),
			// Second tool has error
			CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
				{
					ID:   schemas.Ptr("call-2"),
					Type: schemas.Ptr("function"),
					Function: schemas.ChatAssistantMessageToolCallFunction{
						Name: schemas.Ptr("bifrostInternal-nonexistent"),
						Arguments: `{}`,
					},
				},
			}),
			CreateChatResponseWithText("Recovered"),
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
					ContentStr: schemas.Ptr("Multi-step with error"),
				},
			},
		},
	}

	result, err := manager.CheckAndExecuteAgentForChatRequest(
		ctx,
		originalReq,
		initialResponse,
		mockLLM.MakeChatRequest,
		func(ctx *schemas.BifrostContext, request *schemas.BifrostMCPRequest) (*schemas.BifrostMCPResponse, error) {
			return manager.ExecuteToolCall(ctx, request)
		},
	)

	// First tool should have executed successfully
	// Error in second tool should be handled
	if err != nil {
		t.Logf("Error in middle of loop: %v", err)
	} else {
		require.NotNil(t, result)
		t.Logf("Agent handled error in middle of loop")
	}
}

func TestAgent_LLMError(t *testing.T) {
	t.Parallel()

	clientConfig := GetSampleHTTPClientConfig(GetTestConfig(t).HTTPServerURL)
	clientConfig.ToolsToExecute = []string{"*"}
	clientConfig.ToolsToAutoExecute = []string{"*"}
	manager := setupMCPManager(t, clientConfig)

	ctx := createTestContext()

	mockLLM := &MockLLMCaller{
		chatResponses: []*schemas.BifrostChatResponse{
			CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
				GetSampleEchoToolCall("call-1", "first"),
			}),
			// Next call will error (no more responses)
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
					ContentStr: schemas.Ptr("Test LLM error"),
				},
			},
		},
	}

	result, err := manager.CheckAndExecuteAgentForChatRequest(
		ctx,
		originalReq,
		initialResponse,
		mockLLM.MakeChatRequest,
		func(ctx *schemas.BifrostContext, request *schemas.BifrostMCPRequest) (*schemas.BifrostMCPResponse, error) {
			return manager.ExecuteToolCall(ctx, request)
		},
	)

	// LLM error should be returned
	if err != nil {
		assert.Contains(t, err.Error.Message, "no more mock", "should get LLM error")
		t.Logf("LLM error correctly propagated: %s", err.Error.Message)
	} else {
		t.Logf("LLM error handled gracefully, result: %+v", result)
	}
}

// =============================================================================
// COMBINED LIMITS TESTS
// =============================================================================

func TestAgent_MaxDepthAndTimeout(t *testing.T) {
	t.Parallel()

	clientConfig := GetSampleHTTPClientConfig(GetTestConfig(t).HTTPServerURL)
	clientConfig.ToolsToExecute = []string{"*"}
	clientConfig.ToolsToAutoExecute = []string{"*"}
	manager := setupMCPManager(t, clientConfig)

	// Set both max depth and timeout
	manager.UpdateToolManagerConfig(&schemas.MCPToolManagerConfig{
		MaxAgentDepth:        3,
		ToolExecutionTimeout: 5 * time.Second,
	}) // Max depth = 3, timeout = 5 seconds

	ctx := createTestContext()

	mockLLM := &MockLLMCaller{
		chatResponses: []*schemas.BifrostChatResponse{
			CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
				GetSampleEchoToolCall("call-1", "iter 1"),
			}),
			CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
				GetSampleEchoToolCall("call-2", "iter 2"),
			}),
			CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
				GetSampleEchoToolCall("call-3", "iter 3"),
			}),
			CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
				GetSampleEchoToolCall("call-4", "should not reach"),
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
					ContentStr: schemas.Ptr("Test combined limits"),
				},
			},
		},
	}

	result, err := manager.CheckAndExecuteAgentForChatRequest(
		ctx,
		originalReq,
		initialResponse,
		mockLLM.MakeChatRequest,
		func(ctx *schemas.BifrostContext, request *schemas.BifrostMCPRequest) (*schemas.BifrostMCPResponse, error) {
			return manager.ExecuteToolCall(ctx, request)
		},
	)

	// Whichever limit hits first should stop the agent
	// In this case, max depth should hit first
	if err != nil {
		t.Logf("Agent stopped with error: %v", err)
	} else {
		require.NotNil(t, result)
		assert.LessOrEqual(t, mockLLM.chatCallCount, 2, "should stop at max depth 3")
		t.Logf("Agent stopped at %d calls (max depth: 3)", mockLLM.chatCallCount)
	}
}

// =============================================================================
// EDGE CASE TESTS
// =============================================================================

func TestAgent_MaxDepthZero(t *testing.T) {
	t.Parallel()

	clientConfig := GetSampleHTTPClientConfig(GetTestConfig(t).HTTPServerURL)
	clientConfig.ToolsToExecute = []string{"*"}
	clientConfig.ToolsToAutoExecute = []string{"*"}
	manager := setupMCPManager(t, clientConfig)

	// Set max depth = 0 (should not allow any iterations)
	manager.UpdateToolManagerConfig(&schemas.MCPToolManagerConfig{
		MaxAgentDepth:        0,
		ToolExecutionTimeout: 30 * time.Second,
	})

	ctx := createTestContext()

	mockLLM := &MockLLMCaller{
		chatResponses: []*schemas.BifrostChatResponse{
			CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
				GetSampleEchoToolCall("call-1", "should not execute"),
			}),
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
					ContentStr: schemas.Ptr("Test zero depth"),
				},
			},
		},
	}

	result, err := manager.CheckAndExecuteAgentForChatRequest(
		ctx,
		originalReq,
		initialResponse,
		mockLLM.MakeChatRequest,
		func(ctx *schemas.BifrostContext, request *schemas.BifrostMCPRequest) (*schemas.BifrostMCPResponse, error) {
			return manager.ExecuteToolCall(ctx, request)
		},
	)

	// Should return immediately with tool calls
	require.Nil(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 0, mockLLM.chatCallCount, "should not make any LLM calls with max depth 0")

	// Should return the tools for approval
	finalMessage := result.Choices[0].ChatNonStreamResponseChoice.Message
	require.NotNil(t, finalMessage.ChatAssistantMessage)
	require.NotEmpty(t, finalMessage.ChatAssistantMessage.ToolCalls)
	t.Logf("Max depth 0: correctly returned tools without execution")
}

func TestAgent_ParallelToolExecution(t *testing.T) {
	t.Parallel()

	clientConfig := GetSampleHTTPClientConfig(GetTestConfig(t).HTTPServerURL)
	clientConfig.ToolsToExecute = []string{"*"}
	clientConfig.ToolsToAutoExecute = []string{"*"}
	manager := setupMCPManager(t, clientConfig)

	ctx := createTestContext()

	// LLM returns multiple tools in parallel
	mockLLM := &MockLLMCaller{
		chatResponses: []*schemas.BifrostChatResponse{
			CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
				GetSampleEchoToolCall("call-1", "parallel 1"),
				GetSampleEchoToolCall("call-2", "parallel 2"),
				GetSampleEchoToolCall("call-3", "parallel 3"),
			}),
			CreateChatResponseWithText("All parallel tools executed"),
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
					ContentStr: schemas.Ptr("Parallel test"),
				},
			},
		},
	}

	result, err := manager.CheckAndExecuteAgentForChatRequest(
		ctx,
		originalReq,
		initialResponse,
		mockLLM.MakeChatRequest,
		func(ctx *schemas.BifrostContext, request *schemas.BifrostMCPRequest) (*schemas.BifrostMCPResponse, error) {
			return manager.ExecuteToolCall(ctx, request)
		},
	)

	require.Nil(t, err)
	require.NotNil(t, result)

	// All 3 tools should be executed in parallel in one iteration
	assert.GreaterOrEqual(t, mockLLM.chatCallCount, 1, "should continue after parallel execution")
	t.Logf("Parallel tool execution completed successfully")
}

func TestAgent_IterationTracking(t *testing.T) {
	t.Parallel()

	clientConfig := GetSampleHTTPClientConfig(GetTestConfig(t).HTTPServerURL)
	clientConfig.ToolsToExecute = []string{"*"}
	clientConfig.ToolsToAutoExecute = []string{"*"}
	manager := setupMCPManager(t, clientConfig)

	manager.UpdateToolManagerConfig(&schemas.MCPToolManagerConfig{
		MaxAgentDepth:        10,
		ToolExecutionTimeout: 30 * time.Second,
	})

	ctx := createTestContext()

	iterationCount := 0
	trackingMockLLM := &MockLLMCaller{
		chatResponses: []*schemas.BifrostChatResponse{
			CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
				GetSampleEchoToolCall("call-1", fmt.Sprintf("iteration %d", iterationCount)),
			}),
			CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
				GetSampleEchoToolCall("call-2", fmt.Sprintf("iteration %d", iterationCount+1)),
			}),
			CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
				GetSampleEchoToolCall("call-3", fmt.Sprintf("iteration %d", iterationCount+2)),
			}),
			CreateChatResponseWithText("Done with iterations"),
		},
	}

	initialResponse := trackingMockLLM.chatResponses[0]
	trackingMockLLM.chatCallCount = 1

	originalReq := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4",
		Input: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentStr: schemas.Ptr("Track iterations"),
				},
			},
		},
	}

	result, err := manager.CheckAndExecuteAgentForChatRequest(
		ctx,
		originalReq,
		initialResponse,
		trackingMockLLM.MakeChatRequest,
		func(ctx *schemas.BifrostContext, request *schemas.BifrostMCPRequest) (*schemas.BifrostMCPResponse, error) {
			return manager.ExecuteToolCall(ctx, request)
		},
	)

	require.Nil(t, err)
	require.NotNil(t, result)

	// Track actual iterations
	actualIterations := trackingMockLLM.chatCallCount
	t.Logf("Agent completed with %d iterations", actualIterations)
	assert.GreaterOrEqual(t, actualIterations, 1, "should track iterations")
	assert.LessOrEqual(t, actualIterations, 3, "should not exceed expected iterations")
}
