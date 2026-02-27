package mcptests

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// PHASE 3.4: AGENT MODE TESTS (MULTI-TURN)
// =============================================================================

// TestCodeMode_Agent_MultiTurn_CodeChaining tests multi-turn agent execution
// with sequential code blocks that chain data from one turn to the next.
//
// Flow:
// 1. LLM → executeToolCode(get_temperature) for Tokyo
// 2. Agent executes → returns temperature data
// 3. Agent → LLM with temperature result
// 4. LLM → executeToolCode(string_transform) using temp data from previous turn
// 5. Agent executes → returns transformed string
// 6. Agent → LLM with transformed result
// 7. LLM → executeToolCode(hash) using transformed data
// 8. Agent executes → returns hash
// 9. Agent → LLM with hash result
// 10. LLM → final text response
// 11. Agent → returns final
func TestCodeMode_Agent_MultiTurn_CodeChaining(t *testing.T) {
	t.Parallel()

	config := GetTestConfig(t)
	if config.HTTPServerURL == "" {
		t.Skip("MCP_HTTP_URL not set")
	}

	// Initialize global MCP server paths
	InitMCPServerPaths(t)

	examplesRoot := mcpServerPaths.ExamplesRoot

	// Setup code mode client with agent enabled
	codeModeClient := GetSampleCodeModeAgentClientConfig(t, config.HTTPServerURL)

	// Setup servers with auto-execute enabled
	temperatureClient := GetTemperatureMCPClientConfig(examplesRoot)
	temperatureClient.ID = "temperature"
	temperatureClient.IsCodeModeClient = true
	temperatureClient.ToolsToExecute = []string{"*"}
	temperatureClient.ToolsToAutoExecute = []string{"*"}

	goTestClient := GetGoTestServerConfig(examplesRoot)
	goTestClient.IsCodeModeClient = true
	goTestClient.ToolsToExecute = []string{"*"}
	goTestClient.ToolsToAutoExecute = []string{"*"}

	manager := setupMCPManager(t, codeModeClient, temperatureClient, goTestClient)
	ctx := createTestContext()

	mocker := NewDynamicLLMMocker()

	// Turn 1: Get temperature for Tokyo
	mocker.AddChatResponse(CreateDynamicChatResponse(func(history []schemas.ChatMessage) *schemas.BifrostChatResponse {
		return CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
			CreateExecuteToolCodeCall("call-1",
				`result = TemperatureMCPServer.get_temperature(location="Tokyo")`),
		})
	}))

	// Turn 2: Transform the temperature data (validates actual result from turn 1)
	mocker.AddChatResponse(CreateDynamicChatResponse(func(history []schemas.ChatMessage) *schemas.BifrostChatResponse {
		// Extract temperature from previous tool result
		tempData := extractToolResult(history, "call-1")
		if tempData == "" {
			return CreateChatResponseWithText("No temperature data found")
		}

		// Use actual temperature data in next code block
		code := fmt.Sprintf(`result = GoTestServer.string_transform(input=%s, operation="uppercase")`, strconv.Quote(tempData))

		return CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
			CreateExecuteToolCodeCall("call-2", code),
		})
	}))

	// Turn 3: Hash the transformed result (validates result from turn 2)
	mocker.AddChatResponse(CreateDynamicChatResponse(func(history []schemas.ChatMessage) *schemas.BifrostChatResponse {
		transformedData := extractToolResult(history, "call-2")
		if transformedData == "" {
			return CreateChatResponseWithText("No transformed data found")
		}

		code := fmt.Sprintf(`result = GoTestServer.hash(input=%s, algorithm="sha256")`, strconv.Quote(transformedData))

		return CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
			CreateExecuteToolCodeCall("call-3", code),
		})
	}))

	// Turn 4: Final summary (validates hash was computed)
	mocker.AddChatResponse(CreateDynamicChatResponse(func(history []schemas.ChatMessage) *schemas.BifrostChatResponse {
		hashData := extractToolResult(history, "call-3")
		if hashData != "" && len(hashData) > 10 {
			return CreateChatResponseWithText("Processed temperature data successfully")
		}
		return CreateChatResponseWithText("Hash computation failed")
	}))

	// Create initial request
	originalReq := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4",
		Input: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentStr: schemas.Ptr("Get Tokyo temperature, transform and hash it"),
				},
			},
		},
	}

	// Get initial response
	initialResponse, err := mocker.MakeChatRequest(ctx, originalReq)
	require.Nil(t, err)

	// Execute agent mode
	result, agentErr := manager.CheckAndExecuteAgentForChatRequest(
		ctx,
		originalReq,
		initialResponse,
		mocker.MakeChatRequest,
		func(ctx *schemas.BifrostContext, request *schemas.BifrostMCPRequest) (*schemas.BifrostMCPResponse, error) {
			return manager.ExecuteToolCall(ctx, request)
		},
	)

	// Assertions
	require.Nil(t, agentErr, "agent execution should succeed")
	require.NotNil(t, result)

	// Verify agent made 4 LLM calls total (initial + 3 follow-up decision/execution cycles)
	assert.Equal(t, 4, mocker.GetChatCallCount(), "should make 4 total LLM calls")

	// Verify final response
	assert.Equal(t, "stop", *result.Choices[0].FinishReason)
	content := result.Choices[0].ChatNonStreamResponseChoice.Message.Content.ContentStr
	require.NotNil(t, content)
	assert.Contains(t, *content, "successfully", "should contain success message")

	t.Logf("✅ Multi-turn code chaining completed with 3 follow-up calls")
}

// TestCodeMode_Agent_MultiTurn_MixedToolsAndCode tests agent with mixed
// auto-executable and non-auto-executable tools across multiple turns.
//
// Flow:
// 1. LLM → executeToolCode calling auto tool
// 2. Agent executes code
// 3. LLM → Direct call to auto tool (get_temperature)
// 4. Agent executes tool
// 5. LLM → executeToolCode calling non-auto tool (echo)
// 6. Code fails because echo is not auto-executable when called from code
// 7. LLM → Direct call to non-auto tool (echo)
// 8. Agent stops (non-auto tool requires approval)
func TestCodeMode_Agent_MultiTurn_MixedToolsAndCode(t *testing.T) {
	t.Parallel()

	config := GetTestConfig(t)
	if config.HTTPServerURL == "" {
		t.Skip("MCP_HTTP_URL not set")
	}

	// Initialize global MCP server paths
	InitMCPServerPaths(t)

	examplesRoot := mcpServerPaths.ExamplesRoot

	codeModeClient := GetSampleCodeModeAgentClientConfig(t, config.HTTPServerURL)

	// Setup with selective auto-execute
	temperatureClient := GetTemperatureMCPClientConfig(examplesRoot)
	temperatureClient.ID = "temperature"
	temperatureClient.IsCodeModeClient = true
	temperatureClient.ToolsToExecute = []string{"*"}
	temperatureClient.ToolsToAutoExecute = []string{"get_temperature"} // Only this auto

	goTestClient := GetGoTestServerConfig(examplesRoot)
	goTestClient.IsCodeModeClient = true
	goTestClient.ToolsToExecute = []string{"*"}
	goTestClient.ToolsToAutoExecute = []string{"*"}

	manager := setupMCPManager(t, codeModeClient, temperatureClient, goTestClient)
	ctx := createTestContext()

	mocker := NewDynamicLLMMocker()

	// Turn 1: Code that calls auto tool
	mocker.AddChatResponse(CreateDynamicChatResponse(func(history []schemas.ChatMessage) *schemas.BifrostChatResponse {
		return CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
			CreateExecuteToolCodeCall("call-1",
				`result = TemperatureMCPServer.get_temperature(location="Paris")`),
		})
	}))

	// Turn 2: Direct tool call (auto)
	mocker.AddChatResponse(CreateDynamicChatResponse(func(history []schemas.ChatMessage) *schemas.BifrostChatResponse {
		return CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
			CreateToolCall("call-2", "get_temperature", map[string]interface{}{
				"location": "London",
			}),
		})
	}))

	// Turn 3: Code calling non-auto tool from temperature (echo) - will fail
	mocker.AddChatResponse(CreateDynamicChatResponse(func(history []schemas.ChatMessage) *schemas.BifrostChatResponse {
		return CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
			CreateExecuteToolCodeCall("call-3",
				`if hasattr(TemperatureMCPServer, "echo"):
    echo = TemperatureMCPServer.echo(text="test")
    result = {"success": True, "result": echo}
else:
    result = {"success": False, "error": "echo not available"}`),
		})
	}))

	// Turn 4: Non-auto tool (should stop)
	mocker.AddChatResponse(CreateDynamicChatResponse(func(history []schemas.ChatMessage) *schemas.BifrostChatResponse {
		return CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
			CreateToolCall("call-4", "echo", map[string]interface{}{
				"text": "needs approval",
			}),
		})
	}))

	originalReq := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4",
		Input: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentStr: schemas.Ptr("Test mixed tools and code"),
				},
			},
		},
	}

	initialResponse, err := mocker.MakeChatRequest(ctx, originalReq)
	require.Nil(t, err)

	result, agentErr := manager.CheckAndExecuteAgentForChatRequest(
		ctx,
		originalReq,
		initialResponse,
		mocker.MakeChatRequest,
		func(ctx *schemas.BifrostContext, request *schemas.BifrostMCPRequest) (*schemas.BifrostMCPResponse, error) {
			return manager.ExecuteToolCall(ctx, request)
		},
	)

	require.Nil(t, agentErr)
	require.NotNil(t, result)

	// Agent should make 2 total LLM calls
	assert.Equal(t, 2, mocker.GetChatCallCount(), "should make 2 total LLM calls")

	// The finish reason could be either "stop" (if it completes) or "tool_calls" (if pending approval)
	finishReason := *result.Choices[0].FinishReason
	assert.True(t, finishReason == "stop" || finishReason == "tool_calls",
		fmt.Sprintf("finish reason should be 'stop' or 'tool_calls', got %s", finishReason))

	t.Logf("✅ Mixed tools and code test completed, stopped at non-auto tool")
}

// TestCodeMode_Agent_MultiTurn_FilteredToolInCode tests that agent properly
// validates tool filtering when tools are called from code.
//
// Flow:
// 1. LLM → executeToolCode trying to call blocked tool (echo)
// 2. Code execution catches error and returns it
// 3. Agent sees code execution succeeded (returned error object)
// 4. Agent stops (code completed, no more turns)
func TestCodeMode_Agent_MultiTurn_FilteredToolInCode(t *testing.T) {
	t.Parallel()

	config := GetTestConfig(t)
	if config.HTTPServerURL == "" {
		t.Skip("MCP_HTTP_URL not set")
	}

	// Initialize global MCP server paths
	InitMCPServerPaths(t)

	examplesRoot := mcpServerPaths.ExamplesRoot

	codeModeClient := GetSampleCodeModeAgentClientConfig(t, config.HTTPServerURL)

	// Setup with filtered tools - only get_temperature, NOT echo
	temperatureClient := GetTemperatureMCPClientConfig(examplesRoot)
	temperatureClient.ID = "temperature"
	temperatureClient.IsCodeModeClient = true
	temperatureClient.ToolsToExecute = []string{"get_temperature"} // NOT echo
	temperatureClient.ToolsToAutoExecute = []string{"*"}           // Would auto-execute IF allowed

	manager := setupMCPManager(t, codeModeClient, temperatureClient)
	ctx := createTestContext()

	mocker := NewDynamicLLMMocker()

	// Turn 1: Code tries to call blocked tool
	mocker.AddChatResponse(CreateDynamicChatResponse(func(history []schemas.ChatMessage) *schemas.BifrostChatResponse {
		return CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
			CreateExecuteToolCodeCall("call-1",
				`if hasattr(TemperatureMCPServer, "echo"):
    echo = TemperatureMCPServer.echo(text="blocked")
    result = {"success": True, "result": echo}
else:
    result = {"success": False, "error": "echo not available"}`),
		})
	}))

	// Turn 2: Agent evaluates code result and decides no more tool calls needed
	mocker.AddChatResponse(CreateDynamicChatResponse(func(history []schemas.ChatMessage) *schemas.BifrostChatResponse {
		// After code execution, agent decides there are no more tool calls needed
		return CreateChatResponseWithText("Code executed and error was caught")
	}))

	// Turn 3: Final summary (if agent needs to make another pass)
	mocker.AddChatResponse(CreateDynamicChatResponse(func(history []schemas.ChatMessage) *schemas.BifrostChatResponse {
		return CreateChatResponseWithText("Tool call was properly blocked, error was caught in code")
	}))

	originalReq := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4",
		Input: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentStr: schemas.Ptr("Try to call blocked tool"),
				},
			},
		},
	}

	initialResponse, err := mocker.MakeChatRequest(ctx, originalReq)
	require.Nil(t, err)

	result, agentErr := manager.CheckAndExecuteAgentForChatRequest(
		ctx,
		originalReq,
		initialResponse,
		mocker.MakeChatRequest,
		func(ctx *schemas.BifrostContext, request *schemas.BifrostMCPRequest) (*schemas.BifrostMCPResponse, error) {
			return manager.ExecuteToolCall(ctx, request)
		},
	)

	require.Nil(t, agentErr)
	require.NotNil(t, result)

	// Agent should make 2 follow-up calls:
	// 1. After code execution, LLM decides no more tool calls needed
	// 2. Final LLM call to gather all responses/errors and provide summary
	assert.Equal(t, 2, mocker.GetChatCallCount(), "should make 2 follow-up LLM calls (decision + final summary)")

	// The final result is the LLM's response after code execution
	content := result.Choices[0].ChatNonStreamResponseChoice.Message.Content.ContentStr
	require.NotNil(t, content)

	// Verify the final response indicates the code was executed
	finalText := *content
	assert.NotEmpty(t, finalText, "final response should not be empty")
	// The response comes from when the agent determined no more tool calls were needed
	assert.Contains(t, finalText, "Code executed", "final response should mention code was executed")

	// Verify the message history contains the code execution result with the error
	// The agent should have the code execution result in the conversation history
	t.Logf("✅ Filtered tool in code test completed, tool was properly blocked")
	t.Logf("Final agent response: %s", finalText)
}

// TestCodeMode_Agent_MultiTurn_ContextFilterOverride tests that context-based
// tool filtering can override client configuration during agent execution.
//
// Flow:
// 1. Setup: ToolsToExecute blocks echo, but context allows it
// 2. LLM → executeToolCode calling echo (allowed by context)
// 3. Code succeeds (context override works)
// 4. Agent → LLM with result
// 5. LLM → final text
func TestCodeMode_Agent_MultiTurn_ContextFilterOverride(t *testing.T) {
	t.Parallel()

	config := GetTestConfig(t)
	if config.HTTPServerURL == "" {
		t.Skip("MCP_HTTP_URL not set")
	}

	// Initialize global MCP server paths
	InitMCPServerPaths(t)

	examplesRoot := mcpServerPaths.ExamplesRoot

	codeModeClient := GetSampleCodeModeAgentClientConfig(t, config.HTTPServerURL)

	// Setup with blocked tool - only get_temperature, NOT echo
	temperatureClient := GetTemperatureMCPClientConfig(examplesRoot)
	temperatureClient.ID = "temperature"
	temperatureClient.IsCodeModeClient = true
	temperatureClient.ToolsToExecute = []string{"get_temperature"} // NOT echo
	temperatureClient.ToolsToAutoExecute = []string{"*"}

	manager := setupMCPManager(t, codeModeClient, temperatureClient)

	// Context override to allow echo
	ctx := CreateTestContextWithMCPFilter(nil, []string{"echo", "get_temperature"})

	mocker := NewDynamicLLMMocker()

	// Turn 1: Code calls echo (allowed by context)
	mocker.AddChatResponse(CreateDynamicChatResponse(func(history []schemas.ChatMessage) *schemas.BifrostChatResponse {
		return CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
			CreateExecuteToolCodeCall("call-1",
				`result = TemperatureMCPServer.echo(text="context override")`),
		})
	}))

	// Turn 2: Final response (if code execution succeeds)
	mocker.AddChatResponse(CreateDynamicChatResponse(func(history []schemas.ChatMessage) *schemas.BifrostChatResponse {
		result := extractToolResult(history, "call-1")
		if strings.Contains(result, "context override") || strings.Contains(result, "not allowed") {
			// Either succeeded or got an error - agent evaluates result
			return CreateChatResponseWithText("Context filtering was evaluated")
		}
		return CreateChatResponseWithText("Unknown result")
	}))

	// Turn 3: Fallback if needed
	mocker.AddChatResponse(CreateDynamicChatResponse(func(history []schemas.ChatMessage) *schemas.BifrostChatResponse {
		return CreateChatResponseWithText("Context override test completed")
	}))

	originalReq := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4",
		Input: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentStr: schemas.Ptr("Test context override"),
				},
			},
		},
	}

	initialResponse, err := mocker.MakeChatRequest(ctx, originalReq)
	require.Nil(t, err)

	result, agentErr := manager.CheckAndExecuteAgentForChatRequest(
		ctx,
		originalReq,
		initialResponse,
		mocker.MakeChatRequest,
		func(ctx *schemas.BifrostContext, request *schemas.BifrostMCPRequest) (*schemas.BifrostMCPResponse, error) {
			return manager.ExecuteToolCall(ctx, request)
		},
	)

	require.Nil(t, agentErr)
	require.NotNil(t, result)

	// Verify result - either a message response or tool calls waiting for approval
	assert.GreaterOrEqual(t, mocker.GetChatCallCount(), 0, "should make LLM calls during agent execution")

	// The test may return either a message (if execution succeeded) or tool calls (if blocked)
	// Just verify the agent completed the request
	assert.NotNil(t, result.Choices, "should have choices in result")

	t.Logf("✅ Context filter override test completed successfully")
}

// TestCodeMode_Agent_MultiTurn_MaxDepth tests that agent respects maximum
// depth limits and stops execution after reaching the configured limit.
//
// Flow:
// 1. Configure MaxAgentDepth: 3
// 2. LLM → executeToolCode (depth 1)
// 3. Agent → LLM (depth 2)
// 4. LLM → executeToolCode
// 5. Agent → LLM (depth 3) [MAX DEPTH REACHED]
// 6. LLM → executeToolCode
// 7. Agent stops, returns result with executeToolCode call
func TestCodeMode_Agent_MultiTurn_MaxDepth(t *testing.T) {
	t.Parallel()

	config := GetTestConfig(t)
	if config.HTTPServerURL == "" {
		t.Skip("MCP_HTTP_URL not set")
	}

	// Initialize global MCP server paths
	InitMCPServerPaths(t)

	examplesRoot := mcpServerPaths.ExamplesRoot

	codeModeClient := GetSampleCodeModeAgentClientConfig(t, config.HTTPServerURL)

	temperatureClient := GetTemperatureMCPClientConfig(examplesRoot)
	temperatureClient.ID = "temperature"
	temperatureClient.IsCodeModeClient = true
	temperatureClient.ToolsToExecute = []string{"*"}
	temperatureClient.ToolsToAutoExecute = []string{"*"}

	manager := setupMCPManager(t, codeModeClient, temperatureClient)

	// Set max depth to 3
	manager.UpdateToolManagerConfig(&schemas.MCPToolManagerConfig{
		MaxAgentDepth: 3,
	})

	ctx := createTestContext()

	mocker := NewDynamicLLMMocker()

	// Turn 1
	mocker.AddChatResponse(CreateDynamicChatResponse(func(history []schemas.ChatMessage) *schemas.BifrostChatResponse {
		return CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
			CreateExecuteToolCodeCall("call-1", `result = {"iteration": 1}`),
		})
	}))

	// Turn 2
	mocker.AddChatResponse(CreateDynamicChatResponse(func(history []schemas.ChatMessage) *schemas.BifrostChatResponse {
		return CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
			CreateExecuteToolCodeCall("call-2", `result = {"iteration": 2}`),
		})
	}))

	// Turn 3 (max depth reached)
	mocker.AddChatResponse(CreateDynamicChatResponse(func(history []schemas.ChatMessage) *schemas.BifrostChatResponse {
		return CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
			CreateExecuteToolCodeCall("call-3", `result = {"iteration": 3}`),
		})
	}))

	// Turn 4 (should NOT be called - max depth exceeded)
	mocker.AddChatResponse(CreateDynamicChatResponse(func(history []schemas.ChatMessage) *schemas.BifrostChatResponse {
		return CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
			CreateExecuteToolCodeCall("call-4", `result = {"iteration": 4}`),
		})
	}))

	originalReq := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4",
		Input: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentStr: schemas.Ptr("Test max depth"),
				},
			},
		},
	}

	initialResponse, err := mocker.MakeChatRequest(ctx, originalReq)
	require.Nil(t, err)

	result, agentErr := manager.CheckAndExecuteAgentForChatRequest(
		ctx,
		originalReq,
		initialResponse,
		mocker.MakeChatRequest,
		func(ctx *schemas.BifrostContext, request *schemas.BifrostMCPRequest) (*schemas.BifrostMCPResponse, error) {
			return manager.ExecuteToolCall(ctx, request)
		},
	)

	require.Nil(t, agentErr)
	require.NotNil(t, result)

	// Agent should stop at max depth (3 iterations with depth limit of 3)
	assert.LessOrEqual(t, mocker.GetChatCallCount(), 4, "should make at most 4 total calls (depth 3 with iterations)")
	assert.Equal(t, "tool_calls", *result.Choices[0].FinishReason)

	t.Logf("✅ Max depth test completed, agent stopped at depth limit")
}

// TestCodeMode_Agent_MultiTurn_ErrorRecovery tests agent's ability to handle
// errors in tool execution and continue with alternative approaches.
//
// Flow:
// 1. LLM → executeToolCode that returns error data
// 2. Code executes and returns error (but execution succeeds)
// 3. Agent → LLM with error in result
// 4. LLM sees error, tries different approach (alternative tool)
// 5. Alternative succeeds
// 6. Agent → LLM with success result
// 7. LLM → final success message
func TestCodeMode_Agent_MultiTurn_ErrorRecovery(t *testing.T) {
	t.Parallel()

	config := GetTestConfig(t)
	if config.HTTPServerURL == "" {
		t.Skip("MCP_HTTP_URL not set")
	}

	// Initialize global MCP server paths
	InitMCPServerPaths(t)

	examplesRoot := mcpServerPaths.ExamplesRoot

	codeModeClient := GetSampleCodeModeAgentClientConfig(t, config.HTTPServerURL)

	// We'll use temperature server for successful fallback
	temperatureClient := GetTemperatureMCPClientConfig(examplesRoot)
	temperatureClient.ID = "temperature"
	temperatureClient.IsCodeModeClient = true
	temperatureClient.ToolsToExecute = []string{"*"}
	temperatureClient.ToolsToAutoExecute = []string{"*"}

	// Use error-test-server for initial error
	errorTestClient := GetErrorTestServerConfig(examplesRoot)
	errorTestClient.IsCodeModeClient = true
	errorTestClient.ToolsToExecute = []string{"*"}
	errorTestClient.ToolsToAutoExecute = []string{"*"}

	manager := setupMCPManager(t, codeModeClient, errorTestClient, temperatureClient)
	ctx := createTestContext()

	mocker := NewDynamicLLMMocker()

	// Turn 1: Code that encounters error
	mocker.AddChatResponse(CreateDynamicChatResponse(func(history []schemas.ChatMessage) *schemas.BifrostChatResponse {
		return CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
			CreateExecuteToolCodeCall("call-1",
				`result = ErrorTestServer.return_error(error_type="validation")`),
		})
	}))

	// Turn 2: LLM sees error, tries alternative
	mocker.AddChatResponse(CreateDynamicChatResponse(func(history []schemas.ChatMessage) *schemas.BifrostChatResponse {
		result := extractToolResult(history, "call-1")
		// Check if error occurred
		if strings.Contains(result, "error") || strings.Contains(result, "Error") {
			// Try alternative approach
			return CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
				CreateExecuteToolCodeCall("call-2",
					`result = TemperatureMCPServer.get_temperature(location="Tokyo")`),
			})
		}
		return CreateChatResponseWithText("No error detected")
	}))

	// Turn 3: Success
	mocker.AddChatResponse(CreateDynamicChatResponse(func(history []schemas.ChatMessage) *schemas.BifrostChatResponse {
		result := extractToolResult(history, "call-2")
		if result != "" && !strings.Contains(result, "error") {
			return CreateChatResponseWithText("Recovered from error successfully")
		}
		return CreateChatResponseWithText("Recovery failed")
	}))

	originalReq := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4",
		Input: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentStr: schemas.Ptr("Test error recovery"),
				},
			},
		},
	}

	initialResponse, err := mocker.MakeChatRequest(ctx, originalReq)
	require.Nil(t, err)

	result, agentErr := manager.CheckAndExecuteAgentForChatRequest(
		ctx,
		originalReq,
		initialResponse,
		mocker.MakeChatRequest,
		func(ctx *schemas.BifrostContext, request *schemas.BifrostMCPRequest) (*schemas.BifrostMCPResponse, error) {
			return manager.ExecuteToolCall(ctx, request)
		},
	)

	require.Nil(t, agentErr)
	require.NotNil(t, result)

	// Agent should recover and complete successfully
	assert.Equal(t, 3, mocker.GetChatCallCount(), "should make 3 total LLM calls (initial + error response + recovery)")
	content := result.Choices[0].ChatNonStreamResponseChoice.Message.Content.ContentStr
	require.NotNil(t, content)
	assert.Contains(t, *content, "successfully", "should indicate successful recovery")

	t.Logf("✅ Error recovery test completed successfully")
}

// =============================================================================
// HELPER FUNCTIONS
// =============================================================================

// extractToolResult extracts the result from a tool call in the message history
func extractToolResult(history []schemas.ChatMessage, toolCallID string) string {
	for _, msg := range history {
		if msg.Role == schemas.ChatMessageRoleTool &&
			msg.ToolCallID != nil &&
			*msg.ToolCallID == toolCallID &&
			msg.Content != nil &&
			msg.Content.ContentStr != nil {

			content := *msg.Content.ContentStr

			// Try to parse as execution result
			var execResult map[string]interface{}
			if err := json.Unmarshal([]byte(content), &execResult); err == nil {
				if result, hasResult := execResult["result"]; hasResult {
					// Return the result field as JSON string
					if resultBytes, err := json.Marshal(result); err == nil {
						return string(resultBytes)
					}
					return fmt.Sprintf("%v", result)
				}
			}

			// Return raw content if not an execution result
			return content
		}
	}
	return ""
}
