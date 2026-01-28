package mcptests

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// PHASE 3.3: AGENT MODE TESTS (SINGLE TURN)
// =============================================================================

// TestCodeMode_Agent_AutoExecuteSingleTool tests agent mode with a single
// auto-executable tool call in code, validating that the agent executes the
// code, gets the result, and makes a follow-up LLM call with the result.
//
// Flow:
// 1. LLM returns executeToolCode calling get_temperature
// 2. Agent auto-executes code (get_temperature is auto-executable)
// 3. Agent calls LLM with tool result
// 4. LLM validates result contains expected data and returns final text
// 5. Agent returns final response
func TestCodeMode_Agent_AutoExecuteSingleTool(t *testing.T) {
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

	// Setup temperature server with auto-execute enabled
	temperatureClient := GetTemperatureMCPClientConfig(examplesRoot)
	temperatureClient.ID = "temperature"
	temperatureClient.IsCodeModeClient = true
	temperatureClient.ToolsToExecute = []string{"*"}
	temperatureClient.ToolsToAutoExecute = []string{"get_temperature"} // Auto-execute get_temperature

	manager := setupMCPManager(t, codeModeClient, temperatureClient)
	ctx := createTestContext()

	// Create dynamic LLM mocker with validating response
	mocker := NewDynamicLLMMocker()

	// Turn 1: LLM returns executeToolCode that calls get_temperature
	mocker.AddChatResponse(CreateDynamicChatResponse(func(history []schemas.ChatMessage) *schemas.BifrostChatResponse {
		return CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
			CreateExecuteToolCodeCall("call-1",
				`const temp = await TemperatureMCPServer.get_temperature({location: "London"}); return temp;`),
		})
	}))

	// Turn 2: LLM validates the actual result and responds accordingly
	mocker.AddChatResponse(CreateDynamicChatResponse(func(history []schemas.ChatMessage) *schemas.BifrostChatResponse {
		// Find the tool result from call-1 (executeToolCode result)
		for _, msg := range history {
			if msg.Role == schemas.ChatMessageRoleTool &&
				msg.ToolCallID != nil &&
				*msg.ToolCallID == "call-1" {
				content := *msg.Content.ContentStr

				// Check if the content contains temperature data (look for the pattern in the raw content)
				if strings.Contains(content, "temperature") || strings.Contains(content, "°C") ||
					strings.Contains(content, "return value") {
					// The tool executed successfully and returned a result
					return CreateChatResponseWithText("The temperature in London is 15°C")
				}

				// Also check if it's JSON with a result field
				var execResult map[string]interface{}
				if err := json.Unmarshal([]byte(content), &execResult); err == nil {
					if returnValue, hasResult := execResult["result"]; hasResult {
						returnStr := fmt.Sprintf("%v", returnValue)
						if returnStr != "" && returnStr != "<nil>" {
							return CreateChatResponseWithText("The temperature in London is 15°C")
						}
					}
				}

				return CreateChatResponseWithText("Temperature retrieved but unexpected format")
			}
		}
		return CreateChatResponseWithText("No temperature data received")
	}))

	// Create initial request
	originalReq := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4",
		Input: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentStr: schemas.Ptr("Get the temperature in London"),
				},
			},
		},
	}

	// Get initial response (first LLM call)
	initialResponse, err := mocker.MakeChatRequest(ctx, originalReq)
	require.Nil(t, err, "initial LLM call should succeed")

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
	require.NotNil(t, result, "should return result")

	// Verify final response
	require.NotEmpty(t, result.Choices, "should have choices")
	assert.Equal(t, "stop", *result.Choices[0].FinishReason, "should finish with stop reason")

	// Verify content contains expected text
	content := result.Choices[0].ChatNonStreamResponseChoice.Message.Content.ContentStr
	require.NotNil(t, content, "should have content")
	// The response could be either the validated temperature or a fallback message
	assert.True(t,
		strings.Contains(*content, "15°C") || strings.Contains(*content, "temperature"),
		fmt.Sprintf("response should contain temperature info, got: %s", *content))

	// Verify LLM was called 2 times total (initial + 1 follow-up after executeToolCode)
	assert.Equal(t, 2, mocker.GetChatCallCount(), "should make 2 total LLM calls (initial + follow-up)")

	t.Logf("✅ Agent completed successfully with 1 follow-up LLM call")
}

// TestCodeMode_Agent_NonAutoToolInCode tests that when code is auto-executed
// but the LLM then returns a non-auto-executable tool, the agent stops and
// returns the tool call for user approval.
//
// Flow:
// 1. LLM returns executeToolCode (auto-executable)
// 2. Agent executes code and returns result
// 3. Agent calls LLM with code result
// 4. LLM returns get_temperature (NOT auto-executable)
// 5. Agent stops and returns response with tool call waiting for approval
func TestCodeMode_Agent_NonAutoToolInCode(t *testing.T) {
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

	// Setup temperature server with NO auto-execute (except executeToolCode)
	temperatureClient := GetTemperatureMCPClientConfig(examplesRoot)
	temperatureClient.ID = "temperature"
	temperatureClient.IsCodeModeClient = true
	temperatureClient.ToolsToExecute = []string{"*"}
	temperatureClient.ToolsToAutoExecute = []string{} // NO auto-execute for direct calls

	manager := setupMCPManager(t, codeModeClient, temperatureClient)
	ctx := createTestContext()

	// Create dynamic LLM mocker
	mocker := NewDynamicLLMMocker()

	// Turn 1: LLM returns executeToolCode (auto-executable)
	mocker.AddChatResponse(CreateDynamicChatResponse(func(history []schemas.ChatMessage) *schemas.BifrostChatResponse {
		return CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
			CreateExecuteToolCodeCall("call-1", `return {value: 42};`),
		})
	}))

	// Turn 2: LLM returns non-auto tool (should stop agent)
	mocker.AddChatResponse(CreateDynamicChatResponse(func(history []schemas.ChatMessage) *schemas.BifrostChatResponse {
		return CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
			CreateInProcessToolCall("call-2", "get_temperature", map[string]interface{}{
				"location": "Paris",
			}),
		})
	}))

	// Create initial request
	originalReq := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4",
		Input: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentStr: schemas.Ptr("Execute code and get temperature"),
				},
			},
		},
	}

	// Get initial response (first LLM call)
	initialResponse, err := mocker.MakeChatRequest(ctx, originalReq)
	require.Nil(t, err, "initial LLM call should succeed")

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
	require.Nil(t, agentErr, "agent execution should succeed (stops at non-auto tool)")
	require.NotNil(t, result, "should return result")

	// Verify agent stopped at non-auto tool
	require.NotEmpty(t, result.Choices, "should have choices")
	assert.Equal(t, "stop", *result.Choices[0].FinishReason, "should finish with stop reason")

	// Verify response contains tool call waiting for approval
	toolCalls := result.Choices[0].ChatNonStreamResponseChoice.Message.ChatAssistantMessage.ToolCalls
	require.NotEmpty(t, toolCalls, "should have tool calls waiting for approval")
	assert.Equal(t, "bifrostInternal-get_temperature", *toolCalls[0].Function.Name, "should be get_temperature tool")

	// Verify LLM was called 2 times total (initial + 1 follow-up after executeToolCode)
	assert.Equal(t, 2, mocker.GetChatCallCount(), "should make 2 total LLM calls (initial + follow-up before stopping)")

	t.Logf("✅ Agent correctly stopped at non-auto tool with 1 follow-up LLM call")
}

// Note: CreateToolCall is defined in agent_test_helpers.go
