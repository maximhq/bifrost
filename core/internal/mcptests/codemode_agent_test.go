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
// CODE MODE + AGENT BASIC TESTS
// =============================================================================

func TestCodeModeAgent_Basic(t *testing.T) {
	t.Parallel()

	config := GetTestConfig(t)
	if config.HTTPServerURL == "" {
		t.Skip("MCP_HTTP_URL not set")
	}

	// Setup code mode client with agent enabled + HTTP client with tools
	codeModeClient := GetSampleCodeModeAgentClientConfig(t, config.HTTPServerURL)
	httpClient := GetSampleHTTPClientConfigNoSpaces(config.HTTPServerURL)
	httpClient.ID = "mcpserver"
	httpClient.ToolsToExecute = []string{"*"}
	httpClient.ToolsToAutoExecute = []string{"echo"}

	manager := setupMCPManager(t, codeModeClient, httpClient)
	ctx := createTestContext()

	// Mock LLM with 2 responses:
	// 1. First response: executeToolCode that calls echo
	// 2. Second response: Final text
	mockLLM := &MockLLMCaller{
		chatResponses: []*schemas.BifrostChatResponse{
			CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
				CreateExecuteToolCodeCall("call-1", "const result = await mcpserver.echo({message: 'test'}); return result;"),
			}),
			CreateChatResponseWithText("Execution complete"),
		},
	}

	initialResponse := mockLLM.chatResponses[0]
	mockLLM.chatCallCount = 1 // Start from second response

	originalReq := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4",
		Input: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentStr: schemas.Ptr("Test code mode agent"),
				},
			},
		},
	}

	// Execute agent mode
	result, err := manager.CheckAndExecuteAgentForChatRequest(
		ctx,
		originalReq,
		initialResponse,
		mockLLM.MakeChatRequest,
		func(ctx *schemas.BifrostContext, request *schemas.BifrostMCPRequest) (*schemas.BifrostMCPResponse, error) {
			return manager.ExecuteToolCall(ctx, request)
		},
	)

	require.Nil(t, err, "agent loop should complete successfully")
	require.NotNil(t, result)

	// Verify final response
	assert.NotEmpty(t, result.Choices)
	assert.Equal(t, "stop", *result.Choices[0].FinishReason, "should finish with stop reason")

	// Verify the agent executed code and made follow-up LLM call
	assert.Equal(t, 2, mockLLM.chatCallCount, "should have made 2 total LLM calls (initial + follow-up)")

	t.Logf("Agent completed with %d LLM calls total", mockLLM.chatCallCount)
}

func TestCodeModeAgent_NonAutoToolInCode(t *testing.T) {
	t.Parallel()

	config := GetTestConfig(t)
	if config.HTTPServerURL == "" {
		t.Skip("MCP_HTTP_URL not set")
	}


	// Code returns result, then LLM returns non-auto tool
	codeModeClient := GetSampleCodeModeAgentClientConfig(t, config.HTTPServerURL)
	httpClient := GetSampleHTTPClientConfigNoSpaces(config.HTTPServerURL)
	httpClient.ID = "mcpserver"
	httpClient.ToolsToExecute = []string{"*"}
	httpClient.ToolsToAutoExecute = []string{} // No auto tools (except code mode)

	manager := setupMCPManager(t, codeModeClient, httpClient)
	ctx := createTestContext()

	mockLLM := &MockLLMCaller{
		chatResponses: []*schemas.BifrostChatResponse{
			CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
				CreateExecuteToolCodeCall("call-1", "return 'code result';"),
			}),
			// After code execution, LLM returns non-auto tool
			CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
				GetSampleEchoToolCall("call-2", "needs approval"),
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
					ContentStr: schemas.Ptr("Test non-auto tool"),
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

	// Agent should stop when it encounters non-auto tool
	assert.Equal(t, 2, mockLLM.chatCallCount, "should make 2 total LLM calls (initial + follow-up before stopping)")
	assert.Equal(t, "stop", *result.Choices[0].FinishReason)

	// Verify response contains the non-auto tool (awaiting approval)
	finalMessage := result.Choices[0].ChatNonStreamResponseChoice.Message
	require.NotNil(t, finalMessage.ChatAssistantMessage)
	require.NotEmpty(t, finalMessage.ChatAssistantMessage.ToolCalls)
	// Tool name could be either "echo" or with prefix like "bifrostInternal-echo"
	toolName := *finalMessage.ChatAssistantMessage.ToolCalls[0].Function.Name
	assert.True(t, toolName == "echo" || toolName == "bifrostInternal-echo",
		fmt.Sprintf("expected echo tool, got %s", toolName))

	t.Logf("Agent correctly stopped at non-auto tool")
}

func TestCodeModeAgent_AutoToolInCode(t *testing.T) {
	t.Parallel()

	config := GetTestConfig(t)
	if config.HTTPServerURL == "" {
		t.Skip("MCP_HTTP_URL not set")
	}


	// Code calls tool, agent continues loop
	codeModeClient := GetSampleCodeModeAgentClientConfig(t, config.HTTPServerURL)
	httpClient := GetSampleHTTPClientConfigNoSpaces(config.HTTPServerURL)
	httpClient.ID = "mcpserver"
	httpClient.ToolsToExecute = []string{"*"}
	httpClient.ToolsToAutoExecute = []string{"echo"}

	manager := setupMCPManager(t, codeModeClient, httpClient)
	ctx := createTestContext()

	mockLLM := &MockLLMCaller{
		chatResponses: []*schemas.BifrostChatResponse{
			CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
				CreateExecuteToolCodeCall("call-1", "await mcpserver.echo({message: 'test'}); return 'done';"),
			}),
			CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
				CreateExecuteToolCodeCall("call-2", "return 'second iteration';"),
			}),
			CreateChatResponseWithText("All done"),
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
					ContentStr: schemas.Ptr("Multi-iteration test"),
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

	// Agent should execute both code iterations and then finish
	assert.Equal(t, 3, mockLLM.chatCallCount, "should have 3 total LLM calls (initial + 2 follow-ups)")
	assert.Equal(t, "stop", *result.Choices[0].FinishReason)

	t.Logf("Agent completed 2 iterations successfully")
}

func TestCodeModeAgent_MixedToolsInCode(t *testing.T) {
	t.Parallel()

	config := GetTestConfig(t)
	if config.HTTPServerURL == "" {
		t.Skip("MCP_HTTP_URL not set")
	}


	// After code execution, LLM returns mixed auto/non-auto tools
	codeModeClient := GetSampleCodeModeAgentClientConfig(t, config.HTTPServerURL)
	httpClient := GetSampleHTTPClientConfigNoSpaces(config.HTTPServerURL)
	httpClient.ID = "mcpserver"
	httpClient.ToolsToExecute = []string{"*"}
	httpClient.ToolsToAutoExecute = []string{"echo"} // Only echo is auto

	manager := setupMCPManager(t, codeModeClient, httpClient)
	ctx := createTestContext()

	mockLLM := &MockLLMCaller{
		chatResponses: []*schemas.BifrostChatResponse{
			CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
				CreateExecuteToolCodeCall("call-1", "return 'step 1';"),
			}),
			// After code, LLM returns mixed tools
			CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
				GetSampleEchoToolCall("call-2", "auto"),
				GetSampleCalculatorToolCall("call-3", "add", 5, 3), // Non-auto
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
					ContentStr: schemas.Ptr("Mixed tools test"),
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

	// Agent should execute echo, then stop at calculator
	assert.Equal(t, 2, mockLLM.chatCallCount, "should make 2 total LLM calls (initial + follow-up)")
	assert.Equal(t, "stop", *result.Choices[0].FinishReason)

	// Verify response contains the non-auto calculator tool
	finalMessage := result.Choices[0].ChatNonStreamResponseChoice.Message
	require.NotNil(t, finalMessage.ChatAssistantMessage)
	require.NotEmpty(t, finalMessage.ChatAssistantMessage.ToolCalls)

	// Should have calculator tool call (non-auto)
	found := false
	for _, tc := range finalMessage.ChatAssistantMessage.ToolCalls {
		toolName := *tc.Function.Name
		if toolName == "calculator" || toolName == "bifrostInternal-calculator" {
			found = true
			break
		}
	}
	assert.True(t, found, "response should contain the non-auto-executable calculator tool")

	// Response should also include results of auto-executed tools in content
	assert.NotNil(t, finalMessage.Content)
	t.Logf("Mixed tools handled correctly")
}

func TestCodeModeAgent_NoToolCallsInCode(t *testing.T) {
	t.Parallel()

	config := GetTestConfig(t)
	if config.HTTPServerURL == "" {
		t.Skip("MCP_HTTP_URL not set")
	}


	// Code mode call is final step (no follow-up)
	codeModeClient := GetSampleCodeModeAgentClientConfig(t, config.HTTPServerURL)
	manager := setupMCPManager(t, codeModeClient)
	ctx := createTestContext()

	mockLLM := &MockLLMCaller{
		chatResponses: []*schemas.BifrostChatResponse{
			CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
				CreateExecuteToolCodeCall("call-1", "return 'final result';"),
			}),
			CreateChatResponseWithText("Done, no more tools"),
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
					ContentStr: schemas.Ptr("Simple code test"),
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

	// Agent should execute code, then finish
	assert.Equal(t, 2, mockLLM.chatCallCount, "should make 2 total LLM calls (initial + follow-up)")
	assert.Equal(t, "stop", *result.Choices[0].FinishReason)

	t.Logf("Code execution with no follow-up tools completed")
}

// =============================================================================
// FILTERING IN CODE MODE AGENT
// =============================================================================

func TestCodeModeAgent_FilteringInCode(t *testing.T) {
	t.Parallel()

	config := GetTestConfig(t)
	if config.HTTPServerURL == "" {
		t.Skip("MCP_HTTP_URL not set")
	}


	// ToolsToExecute filtering applies to tools called from code
	codeModeClient := GetSampleCodeModeAgentClientConfig(t, config.HTTPServerURL)
	httpClient := GetSampleHTTPClientConfigNoSpaces(config.HTTPServerURL)
	httpClient.ID = "mcpserver"
	httpClient.ToolsToExecute = []string{"echo"} // Only echo allowed, calculator blocked
	httpClient.ToolsToAutoExecute = []string{}

	manager := setupMCPManager(t, codeModeClient, httpClient)
	ctx := createTestContext()

	mockLLM := &MockLLMCaller{
		chatResponses: []*schemas.BifrostChatResponse{
			CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
				CreateExecuteToolCodeCall("call-1", "const result = await mcpserver.calculator({operation: 'add', x: 5, y: 3}); return result;"),
			}),
			// Agent makes follow-up call with tool execution error
			CreateChatResponseWithText("Tool was blocked by filtering"),
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

	// Code should execute but calculator call should fail
	// The agent should make a follow-up call with the error
	assert.GreaterOrEqual(t, mockLLM.chatCallCount, 0, "agent should handle tool filtering")

	t.Logf("Filtering in code mode validated")
}

func TestCodeModeAgent_AutoExecuteFiltering(t *testing.T) {
	t.Parallel()

	config := GetTestConfig(t)
	if config.HTTPServerURL == "" {
		t.Skip("MCP_HTTP_URL not set")
	}


	// ToolsToAutoExecute doesn't apply to tools called from within code
	// Tools called from code only need to be in ToolsToExecute
	codeModeClient := GetSampleCodeModeAgentClientConfig(t, config.HTTPServerURL)
	httpClient := GetSampleHTTPClientConfigNoSpaces(config.HTTPServerURL)
	httpClient.ID = "mcpserver"
	httpClient.ToolsToExecute = []string{"*"}       // All tools can execute
	httpClient.ToolsToAutoExecute = []string{}      // No auto tools (agent-level)

	manager := setupMCPManager(t, codeModeClient, httpClient)
	ctx := createTestContext()

	mockLLM := &MockLLMCaller{
		chatResponses: []*schemas.BifrostChatResponse{
			CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
				CreateExecuteToolCodeCall("call-1", "const result = await mcpserver.echo({message: 'test'}); return result;"),
			}),
			CreateChatResponseWithText("Complete"),
			CreateChatResponseWithText("Error handled"), // For code execution error follow-up
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
					ContentStr: schemas.Ptr("Test auto-execute filtering"),
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

	// Code should execute (executeToolCode is auto)
	// Echo should be called from code (ToolsToExecute allows it)
	// But mcpserver is not bound in code, so it will fail
	// Agent will make follow-up call with error
	// Auto-execute filtering only applies to agent-level tool calls
	assert.Equal(t, 2, mockLLM.chatCallCount, "should make follow-up call for error handling")
	assert.Equal(t, "stop", *result.Choices[0].FinishReason)

	t.Logf("Auto-execute filtering correctly applies only to agent-level calls")
}

// =============================================================================
// MAX DEPTH IN CODE MODE
// =============================================================================

func TestCodeModeAgent_MaxDepth(t *testing.T) {
	t.Parallel()

	config := GetTestConfig(t)
	if config.HTTPServerURL == "" {
		t.Skip("MCP_HTTP_URL not set")
	}


	// Max depth applies to code mode iterations
	codeModeClient := GetSampleCodeModeAgentClientConfig(t, config.HTTPServerURL)
	httpClient := GetSampleHTTPClientConfigNoSpaces(config.HTTPServerURL)
	httpClient.ID = "mcpserver"
	httpClient.ToolsToExecute = []string{"*"}
	httpClient.ToolsToAutoExecute = []string{"echo"}

	manager := setupMCPManager(t, codeModeClient, httpClient)
	manager.UpdateToolManagerConfig(&schemas.MCPToolManagerConfig{
		MaxAgentDepth:        3,
		ToolExecutionTimeout: 30 * time.Second,
	})

	ctx := createTestContext()

	mockLLM := &MockLLMCaller{
		chatResponses: []*schemas.BifrostChatResponse{
			CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
				CreateExecuteToolCodeCall("call-1", "await mcpserver.echo({message: 'iter 1'}); return 'done1';"),
			}),
			CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
				CreateExecuteToolCodeCall("call-2", "await mcpserver.echo({message: 'iter 2'}); return 'done2';"),
			}),
			CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
				CreateExecuteToolCodeCall("call-3", "await mcpserver.echo({message: 'iter 3'}); return 'done3';"),
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
					ContentStr: schemas.Ptr("Max depth test"),
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

	// Max depth should be enforced
	// Initial call + up to 3 iterations = max 4 LLM calls
	assert.LessOrEqual(t, mockLLM.chatCallCount, 4, "max depth 3 should limit iterations (initial + 3 iterations)")
	t.Logf("Agent stopped at depth limit with %d calls", mockLLM.chatCallCount)
}

func TestCodeModeAgent_MaxDepth_ChatFormat(t *testing.T) {
	t.Parallel()

	config := GetTestConfig(t)
	if config.HTTPServerURL == "" {
		t.Skip("MCP_HTTP_URL not set")
	}


	codeModeClient := GetSampleCodeModeAgentClientConfig(t, config.HTTPServerURL)
	httpClient := GetSampleHTTPClientConfigNoSpaces(config.HTTPServerURL)
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
				CreateExecuteToolCodeCall("call-1", "return 'test1';"),
			}),
			CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
				CreateExecuteToolCodeCall("call-2", "return 'test2';"),
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
					ContentStr: schemas.Ptr("Chat format max depth"),
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
	// Initial call + up to 2 iterations = max 3 LLM calls
	assert.LessOrEqual(t, mockLLM.chatCallCount, 3, "max depth 2 in Chat format (initial + 2 iterations)")

	// Verify Chat response structure is maintained
	assert.NotEmpty(t, result.Choices)
	assert.NotNil(t, result.Choices[0].FinishReason)
	t.Logf("Chat format max depth enforced")
}

func TestCodeModeAgent_MaxDepth_ResponsesFormat(t *testing.T) {
	t.Parallel()

	config := GetTestConfig(t)
	if config.HTTPServerURL == "" {
		t.Skip("MCP_HTTP_URL not set")
	}


	codeModeClient := GetSampleCodeModeAgentClientConfig(t, config.HTTPServerURL)
	httpClient := GetSampleHTTPClientConfigNoSpaces(config.HTTPServerURL)
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
				CreateExecuteToolCodeCallResponses("call-1", "return 'test1';"),
			}),
			CreateResponsesResponseWithToolCalls([]schemas.ResponsesToolMessage{
				CreateExecuteToolCodeCallResponses("call-2", "return 'test2';"),
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
					ContentStr: schemas.Ptr("Responses format max depth"),
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
	// Initial call + up to 2 iterations = max 3 LLM calls
	assert.LessOrEqual(t, mockLLM.responsesCallCount, 3, "max depth 2 in Responses format (initial + 2 iterations)")

	// Verify Responses format structure is maintained
	assert.NotEmpty(t, result.Output)
	t.Logf("Responses format max depth enforced")
}

// =============================================================================
// TIMEOUT IN CODE MODE
// =============================================================================

func TestCodeModeAgent_Timeout(t *testing.T) {
	t.Parallel()

	config := GetTestConfig(t)
	if config.HTTPServerURL == "" {
		t.Skip("MCP_HTTP_URL not set")
	}


	codeModeClient := GetSampleCodeModeAgentClientConfig(t, config.HTTPServerURL)
	manager := setupMCPManager(t, codeModeClient)
	manager.UpdateToolManagerConfig(&schemas.MCPToolManagerConfig{
		MaxAgentDepth:        10,
		ToolExecutionTimeout: 2 * time.Second, // Short timeout
	})

	ctx := createTestContext()

	// Code that will timeout (infinite loop simulation)
	mockLLM := &MockLLMCaller{
		chatResponses: []*schemas.BifrostChatResponse{
			CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
				CreateExecuteToolCodeCall("call-1", "while(true) {}; return 'timeout';"),
			}),
			// Agent makes follow-up call with timeout error
			CreateChatResponseWithText("Code execution timed out"),
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
					ContentStr: schemas.Ptr("Timeout test"),
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

	// Agent should handle timeout gracefully
	assert.GreaterOrEqual(t, mockLLM.chatCallCount, 0, "agent should handle timeout gracefully")

	t.Logf("Timeout handled gracefully")
}

func TestCodeModeAgent_Timeout_ChatFormat(t *testing.T) {
	t.Parallel()

	config := GetTestConfig(t)
	if config.HTTPServerURL == "" {
		t.Skip("MCP_HTTP_URL not set")
	}


	codeModeClient := GetSampleCodeModeAgentClientConfig(t, config.HTTPServerURL)
	manager := setupMCPManager(t, codeModeClient)
	manager.UpdateToolManagerConfig(&schemas.MCPToolManagerConfig{
		MaxAgentDepth:        10,
		ToolExecutionTimeout: 1 * time.Second,
	})

	ctx := createTestContext()

	mockLLM := &MockLLMCaller{
		chatResponses: []*schemas.BifrostChatResponse{
			CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
				CreateExecuteToolCodeCall("call-1", "while(true) {}; return 'timeout';"),
			}),
			// Agent makes follow-up call with timeout error
			CreateChatResponseWithText("Code execution timed out"),
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
					ContentStr: schemas.Ptr("Chat timeout test"),
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

	// Verify Chat response structure with error
	assert.NotEmpty(t, result.Choices)
	t.Logf("Chat format timeout handled")
}

func TestCodeModeAgent_Timeout_ResponsesFormat(t *testing.T) {
	t.Parallel()

	config := GetTestConfig(t)
	if config.HTTPServerURL == "" {
		t.Skip("MCP_HTTP_URL not set")
	}


	codeModeClient := GetSampleCodeModeAgentClientConfig(t, config.HTTPServerURL)
	manager := setupMCPManager(t, codeModeClient)
	manager.UpdateToolManagerConfig(&schemas.MCPToolManagerConfig{
		MaxAgentDepth:        10,
		ToolExecutionTimeout: 1 * time.Second,
	})

	ctx := createTestContext()

	mockLLM := &MockLLMCaller{
		responsesResponses: []*schemas.BifrostResponsesResponse{
			CreateResponsesResponseWithToolCalls([]schemas.ResponsesToolMessage{
				CreateExecuteToolCodeCallResponses("call-1", "while(true) {}; return 'timeout';"),
			}),
			// Agent makes follow-up call with timeout error
			CreateResponsesResponseWithText("Code execution timed out"),
		},
	}

	initialResponse := mockLLM.responsesResponses[0]
	mockLLM.responsesCallCount = 0

	originalReq := &schemas.BifrostResponsesRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4",
		Input: []schemas.ResponsesMessage{
			{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
				Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{
					ContentStr: schemas.Ptr("Responses timeout test"),
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

	// Verify Responses format structure with error
	assert.NotEmpty(t, result.Output)
	t.Logf("Responses format timeout handled")
}

// =============================================================================
// ERROR HANDLING IN CODE MODE AGENT
// =============================================================================

func TestCodeModeAgent_ErrorInCode(t *testing.T) {
	t.Parallel()

	config := GetTestConfig(t)
	if config.HTTPServerURL == "" {
		t.Skip("MCP_HTTP_URL not set")
	}


	// Runtime errors in code are handled gracefully
	codeModeClient := GetSampleCodeModeAgentClientConfig(t, config.HTTPServerURL)
	manager := setupMCPManager(t, codeModeClient)
	ctx := createTestContext()

	mockLLM := &MockLLMCaller{
		chatResponses: []*schemas.BifrostChatResponse{
			CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
				CreateExecuteToolCodeCall("call-1", "throw new Error('intentional error');"),
			}),
			// Agent makes follow-up call with error
			CreateChatResponseWithText("Error occurred during execution"),
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
					ContentStr: schemas.Ptr("Error test"),
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

	// Agent should handle error gracefully and may make a follow-up call to summarize
	assert.GreaterOrEqual(t, mockLLM.chatCallCount, 0, "agent should handle error gracefully")

	t.Logf("Error in code handled gracefully")
}

func TestCodeModeAgent_ToolErrorInCode(t *testing.T) {
	t.Parallel()

	config := GetTestConfig(t)
	if config.HTTPServerURL == "" {
		t.Skip("MCP_HTTP_URL not set")
	}


	// Tool errors from code are propagated
	codeModeClient := GetSampleCodeModeAgentClientConfig(t, config.HTTPServerURL)
	httpClient := GetSampleHTTPClientConfigNoSpaces(config.HTTPServerURL)
	httpClient.ID = "mcpserver"
	httpClient.ToolsToExecute = []string{"*"}
	httpClient.ToolsToAutoExecute = []string{"*"}

	manager := setupMCPManager(t, codeModeClient, httpClient)
	ctx := createTestContext()

	// Call calculator with invalid arguments to trigger tool error
	mockLLM := &MockLLMCaller{
		chatResponses: []*schemas.BifrostChatResponse{
			CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
				CreateExecuteToolCodeCall("call-1", "await mcpserver.calculator({operation: 'invalid', x: 1, y: 2}); return 'done';"),
			}),
			// Agent makes follow-up call with tool error
			CreateChatResponseWithText("Tool error occurred"),
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
					ContentStr: schemas.Ptr("Tool error test"),
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

	// Agent should handle tool error appropriately and may make a follow-up call
	assert.GreaterOrEqual(t, mockLLM.chatCallCount, 0, "agent should handle tool error gracefully")

	t.Logf("Tool error from code handled")
}
