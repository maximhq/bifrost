package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// MockLLMCaller implements schemas.BifrostLLMCaller for testing
type MockLLMCaller struct {
	chatResponses      []*schemas.BifrostChatResponse
	responsesResponses []*schemas.BifrostResponsesResponse
	chatCallCount      int
	responsesCallCount int
}

func (m *MockLLMCaller) ChatCompletionRequest(ctx context.Context, req *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
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

func (m *MockLLMCaller) ResponsesRequest(ctx context.Context, req *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
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

// MockLogger implements schemas.Logger for testing
type MockLogger struct{}

func (m *MockLogger) Debug(msg string, args ...any)                     {}
func (m *MockLogger) Info(msg string, args ...any)                      {}
func (m *MockLogger) Warn(msg string, args ...any)                      {}
func (m *MockLogger) Error(msg string, args ...any)                     {}
func (m *MockLogger) Fatal(msg string, args ...any)                     {}
func (m *MockLogger) SetLevel(level schemas.LogLevel)                   {}
func (m *MockLogger) SetOutputType(outputType schemas.LoggerOutputType) {}

// MockClientManager implements ClientManager for testing
type MockClientManager struct{}

func (m *MockClientManager) GetClientForTool(toolName string) *schemas.MCPClientState {
	return nil // Return nil to simulate no client found
}

func (m *MockClientManager) GetClientByName(clientName string) *schemas.MCPClientState {
	return nil
}

func (m *MockClientManager) GetToolPerClient(ctx context.Context) map[string][]schemas.ChatTool {
	return make(map[string][]schemas.ChatTool)
}

func TestHasToolCallsForChatResponse(t *testing.T) {
	// Test nil response
	if hasToolCallsForChatResponse(nil) {
		t.Error("Should return false for nil response")
	}

	// Test empty choices
	emptyResponse := &schemas.BifrostChatResponse{
		Choices: []schemas.BifrostResponseChoice{},
	}
	if hasToolCallsForChatResponse(emptyResponse) {
		t.Error("Should return false for response with empty choices")
	}

	// Test response with tool_calls finish reason
	toolCallsResponse := &schemas.BifrostChatResponse{
		Choices: []schemas.BifrostResponseChoice{
			{
				FinishReason: schemas.Ptr("tool_calls"),
			},
		},
	}
	if !hasToolCallsForChatResponse(toolCallsResponse) {
		t.Error("Should return true for response with tool_calls finish reason")
	}

	// Test response with actual tool calls
	responseWithToolCalls := &schemas.BifrostChatResponse{
		Choices: []schemas.BifrostResponseChoice{
			{
				ChatNonStreamResponseChoice: &schemas.ChatNonStreamResponseChoice{
					Message: &schemas.ChatMessage{
						ChatAssistantMessage: &schemas.ChatAssistantMessage{
							ToolCalls: []schemas.ChatAssistantMessageToolCall{
								{
									Function: schemas.ChatAssistantMessageToolCallFunction{
										Name: schemas.Ptr("test_tool"),
									},
								},
							},
						},
					},
				},
			},
		},
	}
	if !hasToolCallsForChatResponse(responseWithToolCalls) {
		t.Error("Should return true for response with tool calls in message")
	}

	// Test response with stop finish reason (should return false even with tool calls)
	responseWithStopReason := &schemas.BifrostChatResponse{
		Choices: []schemas.BifrostResponseChoice{
			{
				FinishReason: schemas.Ptr("stop"),
				ChatNonStreamResponseChoice: &schemas.ChatNonStreamResponseChoice{
					Message: &schemas.ChatMessage{
						ChatAssistantMessage: &schemas.ChatAssistantMessage{
							ToolCalls: []schemas.ChatAssistantMessageToolCall{
								{
									Function: schemas.ChatAssistantMessageToolCallFunction{
										Name: schemas.Ptr("test_tool"),
									},
								},
							},
						},
					},
				},
			},
		},
	}
	if hasToolCallsForChatResponse(responseWithStopReason) {
		t.Error("Should return false for response with stop finish reason even with tool calls")
	}
}

func TestExtractToolCalls(t *testing.T) {
	// Test response without tool calls
	responseNoTools := &schemas.BifrostChatResponse{
		Choices: []schemas.BifrostResponseChoice{
			{
				FinishReason: schemas.Ptr("stop"),
			},
		},
	}

	toolCalls := extractToolCalls(responseNoTools)
	if len(toolCalls) != 0 {
		t.Error("Should return empty slice for response without tool calls")
	}

	// Test response with tool calls
	expectedToolCalls := []schemas.ChatAssistantMessageToolCall{
		{
			ID: schemas.Ptr("call_123"),
			Function: schemas.ChatAssistantMessageToolCallFunction{
				Name:      schemas.Ptr("test_tool"),
				Arguments: `{"param": "value"}`,
			},
		},
	}

	responseWithTools := &schemas.BifrostChatResponse{
		Choices: []schemas.BifrostResponseChoice{
			{
				ChatNonStreamResponseChoice: &schemas.ChatNonStreamResponseChoice{
					Message: &schemas.ChatMessage{
						ChatAssistantMessage: &schemas.ChatAssistantMessage{
							ToolCalls: expectedToolCalls,
						},
					},
				},
			},
		},
	}

	actualToolCalls := extractToolCalls(responseWithTools)
	if len(actualToolCalls) != 1 {
		t.Errorf("Expected 1 tool call, got %d", len(actualToolCalls))
	}

	if actualToolCalls[0].Function.Name == nil || *actualToolCalls[0].Function.Name != "test_tool" {
		t.Error("Tool call name mismatch")
	}
}

func TestExecuteAgentForChatRequest(t *testing.T) {
	// Test with response that has no tool calls - should return immediately
	responseNoTools := &schemas.BifrostChatResponse{
		Choices: []schemas.BifrostResponseChoice{
			{
				FinishReason: schemas.Ptr("stop"),
				ChatNonStreamResponseChoice: &schemas.ChatNonStreamResponseChoice{
					Message: &schemas.ChatMessage{
						Role: schemas.ChatMessageRoleAssistant,
						Content: &schemas.ChatMessageContent{
							ContentStr: schemas.Ptr("Hello, how can I help you?"),
						},
					},
				},
			},
		},
	}

	llmCaller := &MockLLMCaller{}
	makeReq := func(ctx *schemas.BifrostContext, req *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
		return llmCaller.ChatCompletionRequest(ctx, req)
	}
	originalReq := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4",
		Input: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentStr: schemas.Ptr("Hello"),
				},
			},
		},
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	agentModeExecutor := &AgentModeExecutor{
		logger: &MockLogger{},
	}
	result, err := agentModeExecutor.ExecuteAgentForChatRequest(ctx, 10, originalReq, responseNoTools, makeReq, nil, nil, &MockClientManager{})
	if err != nil {
		t.Errorf("Expected no error for response without tool calls, got: %v", err)
	}
	if result != responseNoTools {
		t.Error("Expected same response to be returned for response without tool calls")
	}
}

func TestExecuteAgentForChatRequest_WithNonAutoExecutableTools(t *testing.T) {

	// Create a response with tool calls that will NOT be auto-executed
	responseWithNonAutoTools := &schemas.BifrostChatResponse{
		Choices: []schemas.BifrostResponseChoice{
			{
				FinishReason: schemas.Ptr("tool_calls"),
				ChatNonStreamResponseChoice: &schemas.ChatNonStreamResponseChoice{
					Message: &schemas.ChatMessage{
						Role: schemas.ChatMessageRoleAssistant,
						Content: &schemas.ChatMessageContent{
							ContentStr: schemas.Ptr("I need to call a tool"),
						},
						ChatAssistantMessage: &schemas.ChatAssistantMessage{
							ToolCalls: []schemas.ChatAssistantMessageToolCall{
								{
									ID: schemas.Ptr("call_123"),
									Function: schemas.ChatAssistantMessageToolCallFunction{
										Name:      schemas.Ptr("non_auto_executable_tool"),
										Arguments: `{"param": "value"}`,
									},
								},
							},
						},
					},
				},
			},
		},
	}

	llmCaller := &MockLLMCaller{}
	makeReq := func(ctx *schemas.BifrostContext, req *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
		return llmCaller.ChatCompletionRequest(ctx, req)
	}
	originalReq := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4",
		Input: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentStr: schemas.Ptr("Test message"),
				},
			},
		},
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	agentModeExecutor := &AgentModeExecutor{
		logger: &MockLogger{},
	}
	// Execute agent mode - should return immediately with non-auto-executable tools
	result, err := agentModeExecutor.ExecuteAgentForChatRequest(ctx, 10, originalReq, responseWithNonAutoTools, makeReq, nil, nil, &MockClientManager{})

	// Should not return error for non-auto-executable tools
	if err != nil {
		t.Errorf("Expected no error for non-auto-executable tools, got: %v", err)
	}

	// Should return a response with the non-auto-executable tool calls
	if result == nil {
		t.Error("Expected result to be returned for non-auto-executable tools")
	}

	// Verify that no LLM calls were made (since tools are non-auto-executable)
	if llmCaller.chatCallCount != 0 {
		t.Errorf("Expected 0 LLM calls for non-auto-executable tools, got %d", llmCaller.chatCallCount)
	}
}

func TestHasToolCallsForResponsesResponse(t *testing.T) {
	// Test nil response
	if hasToolCallsForResponsesResponse(nil) {
		t.Error("Should return false for nil response")
	}

	// Test empty output
	emptyResponse := &schemas.BifrostResponsesResponse{
		Output: []schemas.ResponsesMessage{},
	}
	if hasToolCallsForResponsesResponse(emptyResponse) {
		t.Error("Should return false for response with empty output")
	}

	// Test response with function call
	responseWithFunctionCall := &schemas.BifrostResponsesResponse{
		Output: []schemas.ResponsesMessage{
			{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					CallID: schemas.Ptr("call_123"),
					Name:   schemas.Ptr("test_tool"),
				},
			},
		},
	}
	if !hasToolCallsForResponsesResponse(responseWithFunctionCall) {
		t.Error("Should return true for response with function call")
	}

	// Test response with function call but no ResponsesToolMessage
	responseWithoutToolMessage := &schemas.BifrostResponsesResponse{
		Output: []schemas.ResponsesMessage{
			{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
				// No ResponsesToolMessage
			},
		},
	}
	if hasToolCallsForResponsesResponse(responseWithoutToolMessage) {
		t.Error("Should return false for response with function call type but no ResponsesToolMessage")
	}

	// Test response with regular message
	responseWithRegularMessage := &schemas.BifrostResponsesResponse{
		Output: []schemas.ResponsesMessage{
			{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
				Content: &schemas.ResponsesMessageContent{
					ContentStr: schemas.Ptr("Hello"),
				},
			},
		},
	}
	if hasToolCallsForResponsesResponse(responseWithRegularMessage) {
		t.Error("Should return false for response with regular message")
	}
}

func TestExecuteAgentForResponsesRequest(t *testing.T) {

	// Test with response that has no tool calls - should return immediately
	responseNoTools := &schemas.BifrostResponsesResponse{
		Output: []schemas.ResponsesMessage{
			{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
				Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
				Content: &schemas.ResponsesMessageContent{
					ContentStr: schemas.Ptr("Hello, how can I help you?"),
				},
			},
		},
	}

	llmCaller := &MockLLMCaller{}
	makeReq := func(ctx *schemas.BifrostContext, req *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
		return llmCaller.ResponsesRequest(ctx, req)
	}
	originalReq := &schemas.BifrostResponsesRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4",
		Input: []schemas.ResponsesMessage{
			{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
				Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{
					ContentStr: schemas.Ptr("Hello"),
				},
			},
		},
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	agentModeExecutor := &AgentModeExecutor{
		logger: &MockLogger{},
	}
	result, err := agentModeExecutor.ExecuteAgentForResponsesRequest(ctx, 10, originalReq, responseNoTools, makeReq, nil, nil, &MockClientManager{})
	if err != nil {
		t.Errorf("Expected no error for response without tool calls, got: %v", err)
	}
	if result != responseNoTools {
		t.Error("Expected same response to be returned for response without tool calls")
	}
}

func TestExecuteAgentForResponsesRequest_WithNonAutoExecutableTools(t *testing.T) {

	// Create a response with tool calls that will NOT be auto-executed
	responseWithNonAutoTools := &schemas.BifrostResponsesResponse{
		Output: []schemas.ResponsesMessage{
			{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
				Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					CallID:    schemas.Ptr("call_123"),
					Name:      schemas.Ptr("non_auto_executable_tool"),
					Arguments: schemas.Ptr(`{"param": "value"}`),
				},
			},
		},
	}

	llmCaller := &MockLLMCaller{}
	makeReq := func(ctx *schemas.BifrostContext, req *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
		return llmCaller.ResponsesRequest(ctx, req)
	}
	originalReq := &schemas.BifrostResponsesRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4",
		Input: []schemas.ResponsesMessage{
			{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
				Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{
					ContentStr: schemas.Ptr("Test message"),
				},
			},
		},
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	agentModeExecutor := &AgentModeExecutor{
		logger: &MockLogger{},
	}
	// Execute agent mode - should return immediately with non-auto-executable tools
	result, err := agentModeExecutor.ExecuteAgentForResponsesRequest(ctx, 10, originalReq, responseWithNonAutoTools, makeReq, nil, nil, &MockClientManager{})

	// Should not return error for non-auto-executable tools
	if err != nil {
		t.Errorf("Expected no error for non-auto-executable tools, got: %v", err)
	}

	// Should return a response with the non-auto-executable tool calls
	if result == nil {
		t.Error("Expected result to be returned for non-auto-executable tools")
	}

	// Verify that no LLM calls were made (since tools are non-auto-executable)
	if llmCaller.responsesCallCount != 0 {
		t.Errorf("Expected 0 LLM calls for non-auto-executable tools, got %d", llmCaller.responsesCallCount)
	}
}

// ============================================================================
// CONVERTER TESTS (Phase 2)
// ============================================================================

// TestResponsesToolMessageToChatAssistantMessageToolCall tests conversion of Responses tool message to Chat tool call
func TestResponsesToolMessageToChatAssistantMessageToolCall(t *testing.T) {
	// Test with valid tool message
	responsesToolMsg := &schemas.ResponsesToolMessage{
		CallID:    schemas.Ptr("call-123"),
		Name:      schemas.Ptr("calculate"),
		Arguments: schemas.Ptr("{\"x\": 10, \"y\": 20}"),
	}

	chatToolCall := responsesToolMsg.ToChatAssistantMessageToolCall()

	if chatToolCall == nil {
		t.Fatal("Expected non-nil ChatAssistantMessageToolCall")
	}

	if chatToolCall.Type == nil || *chatToolCall.Type != "function" {
		t.Errorf("Expected Type 'function', got %v", chatToolCall.Type)
	}

	if chatToolCall.Function.Name == nil || *chatToolCall.Function.Name != "calculate" {
		t.Errorf("Expected Name 'calculate', got %v", chatToolCall.Function.Name)
	}

	if chatToolCall.Function.Arguments != `{"x": 10, "y": 20}` {
		t.Errorf("Expected Arguments '{\"x\": 10, \"y\": 20}', got %s", chatToolCall.Function.Arguments)
	}
}

// TestResponsesToolMessageToChatAssistantMessageToolCall_Nil tests nil handling
func TestResponsesToolMessageToChatAssistantMessageToolCall_Nil(t *testing.T) {
	responsesToolMsg := &schemas.ResponsesToolMessage{
		CallID:    schemas.Ptr("call-123"),
		Name:      schemas.Ptr("calculate"),
		Arguments: nil, // Test nil Arguments case
	}

	chatToolCall := responsesToolMsg.ToChatAssistantMessageToolCall()
	if chatToolCall == nil {
		t.Fatal("Expected non-nil ChatAssistantMessageToolCall")
	}

	// Assert that nil Arguments produces a valid empty JSON object
	if chatToolCall.Function.Arguments != "{}" {
		t.Errorf("Expected Arguments '{}' for nil input, got %q", chatToolCall.Function.Arguments)
	}

	// Verify it's valid JSON by attempting to unmarshal
	var args map[string]interface{}
	if err := json.Unmarshal([]byte(chatToolCall.Function.Arguments), &args); err != nil {
		t.Errorf("Expected valid JSON, but unmarshaling failed: %v", err)
	}
}

// TestChatMessageToResponsesToolMessage tests conversion of Chat tool result to Responses tool message
func TestChatMessageToResponsesToolMessage(t *testing.T) {
	// Test with valid chat tool message
	chatMsg := &schemas.ChatMessage{
		Role: schemas.ChatMessageRoleTool,
		ChatToolMessage: &schemas.ChatToolMessage{
			ToolCallID: schemas.Ptr("call-123"),
		},
		Content: &schemas.ChatMessageContent{
			ContentStr: schemas.Ptr("Result: 30"),
		},
	}

	responsesMsg := chatMsg.ToResponsesToolMessage()

	if responsesMsg == nil {
		t.Fatal("Expected non-nil ResponsesMessage")
	}

	if responsesMsg.Type == nil || *responsesMsg.Type != schemas.ResponsesMessageTypeFunctionCallOutput {
		t.Errorf("Expected Type 'function_call_output', got %v", responsesMsg.Type)
	}

	if responsesMsg.ResponsesToolMessage == nil {
		t.Fatal("Expected non-nil ResponsesToolMessage")
	}

	if responsesMsg.ResponsesToolMessage.CallID == nil || *responsesMsg.ResponsesToolMessage.CallID != "call-123" {
		t.Errorf("Expected CallID 'call-123', got %v", responsesMsg.ResponsesToolMessage.CallID)
	}

	if responsesMsg.ResponsesToolMessage.Output == nil {
		t.Fatal("Expected non-nil Output")
	}

	if responsesMsg.ResponsesToolMessage.Output.ResponsesToolCallOutputStr == nil {
		t.Fatal("Expected non-nil ResponsesToolCallOutputStr")
	}

	if *responsesMsg.ResponsesToolMessage.Output.ResponsesToolCallOutputStr != "Result: 30" {
		t.Errorf("Expected Output 'Result: 30', got %s", *responsesMsg.ResponsesToolMessage.Output.ResponsesToolCallOutputStr)
	}
}

// TestChatMessageToResponsesToolMessage_Nil tests nil handling
func TestChatMessageToResponsesToolMessage_Nil(t *testing.T) {
	var chatMsg *schemas.ChatMessage

	responsesMsg := chatMsg.ToResponsesToolMessage()

	if responsesMsg != nil {
		t.Errorf("Expected nil for nil input, got %v", responsesMsg)
	}
}

// TestChatMessageToResponsesToolMessage_NoToolMessage tests with non-tool message
func TestChatMessageToResponsesToolMessage_NoToolMessage(t *testing.T) {
	// Chat message without ChatToolMessage
	chatMsg := &schemas.ChatMessage{
		Role: schemas.ChatMessageRoleAssistant,
	}

	responsesMsg := chatMsg.ToResponsesToolMessage()

	if responsesMsg != nil {
		t.Errorf("Expected nil for non-tool message, got %v", responsesMsg)
	}
}

// ============================================================================
// RESPONSES API TOOL CONVERSION TESTS (Phase 3)
// ============================================================================

// TestExecuteAgentForResponsesRequest_ConversionRoundTrip tests that tool calls survive format conversion
// This is a unit test of the conversion logic only, not full agent execution
func TestExecuteAgentForResponsesRequest_ConversionRoundTrip(t *testing.T) {
	// Create a tool message in Responses format
	responsesToolMsg := &schemas.ResponsesToolMessage{
		CallID:    schemas.Ptr("call-456"),
		Name:      schemas.Ptr("readToolFile"),
		Arguments: schemas.Ptr("{\"file\": \"test.txt\"}"),
	}

	// Step 1: Convert Responses format to Chat format
	chatToolCall := responsesToolMsg.ToChatAssistantMessageToolCall()

	if chatToolCall == nil {
		t.Fatal("Failed to convert Responses to Chat format")
	}

	if *chatToolCall.ID != "call-456" {
		t.Errorf("ID lost in conversion: expected 'call-456', got %s", *chatToolCall.ID)
	}

	if *chatToolCall.Function.Name != "readToolFile" {
		t.Errorf("Name lost in conversion: expected 'readToolFile', got %s", *chatToolCall.Function.Name)
	}

	if chatToolCall.Function.Arguments != "{\"file\": \"test.txt\"}" {
		t.Errorf("Arguments lost in conversion: expected '%s', got %s",
			"{\"file\": \"test.txt\"}", chatToolCall.Function.Arguments)
	}

	// Step 2: Simulate tool execution by creating a result message
	chatResultMsg := &schemas.ChatMessage{
		Role: schemas.ChatMessageRoleTool,
		ChatToolMessage: &schemas.ChatToolMessage{
			ToolCallID: chatToolCall.ID,
		},
		Content: &schemas.ChatMessageContent{
			ContentStr: schemas.Ptr("File contents here"),
		},
	}

	// Step 3: Convert tool result back to Responses format
	responsesResultMsg := chatResultMsg.ToResponsesToolMessage()

	if responsesResultMsg == nil {
		t.Fatal("Failed to convert Chat result to Responses format")
	}

	if responsesResultMsg.ResponsesToolMessage.CallID == nil {
		t.Error("CallID lost in round-trip conversion")
	} else if *responsesResultMsg.ResponsesToolMessage.CallID != "call-456" {
		t.Errorf("CallID changed in round-trip: expected 'call-456', got %s", *responsesResultMsg.ResponsesToolMessage.CallID)
	}

	// Verify output is preserved
	if responsesResultMsg.ResponsesToolMessage.Output == nil {
		t.Error("Output lost in conversion")
	} else if responsesResultMsg.ResponsesToolMessage.Output.ResponsesToolCallOutputStr == nil {
		t.Error("Output content lost in conversion")
	} else if *responsesResultMsg.ResponsesToolMessage.Output.ResponsesToolCallOutputStr != "File contents here" {
		t.Errorf("Output content changed: expected 'File contents here', got %s",
			*responsesResultMsg.ResponsesToolMessage.Output.ResponsesToolCallOutputStr)
	}

	// Verify message type is correct
	if responsesResultMsg.Type == nil || *responsesResultMsg.Type != schemas.ResponsesMessageTypeFunctionCallOutput {
		t.Errorf("Expected message type 'function_call_output', got %v", responsesResultMsg.Type)
	}
}

// TestExecuteAgentForResponsesRequest_OutputStructured tests conversion with structured output blocks
func TestExecuteAgentForResponsesRequest_OutputStructured(t *testing.T) {
	chatResultMsg := &schemas.ChatMessage{
		Role: schemas.ChatMessageRoleTool,
		ChatToolMessage: &schemas.ChatToolMessage{
			ToolCallID: schemas.Ptr("call-789"),
		},
		Content: &schemas.ChatMessageContent{
			ContentBlocks: []schemas.ChatContentBlock{
				{
					Type: schemas.ChatContentBlockTypeText,
					Text: schemas.Ptr("Block 1"),
				},
				{
					Type: schemas.ChatContentBlockTypeText,
					Text: schemas.Ptr("Block 2"),
				},
			},
		},
	}

	responsesMsg := chatResultMsg.ToResponsesToolMessage()

	if responsesMsg == nil {
		t.Fatal("Expected non-nil ResponsesMessage for structured output")
	}

	if responsesMsg.ResponsesToolMessage.Output == nil {
		t.Fatal("Expected non-nil Output for structured content")
	}

	if responsesMsg.ResponsesToolMessage.Output.ResponsesFunctionToolCallOutputBlocks == nil {
		t.Error("Expected output blocks for structured content")
	} else if len(responsesMsg.ResponsesToolMessage.Output.ResponsesFunctionToolCallOutputBlocks) != 2 {
		t.Errorf("Expected 2 output blocks, got %d", len(responsesMsg.ResponsesToolMessage.Output.ResponsesFunctionToolCallOutputBlocks))
	}
}
