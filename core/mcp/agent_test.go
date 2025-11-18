package mcp

import (
	"context"
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
	// Set up logger for the test
	SetLogger(&MockLogger{})

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
	makeReq := func(ctx context.Context, req *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
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

	ctx := context.Background()

	result, err := ExecuteAgentForChatRequest(&ctx, 10, originalReq, responseNoTools, makeReq, nil, nil, &MockClientManager{})
	if err != nil {
		t.Errorf("Expected no error for response without tool calls, got: %v", err)
	}
	if result != responseNoTools {
		t.Error("Expected same response to be returned for response without tool calls")
	}
}

func TestExecuteAgentForChatRequest_WithNonAutoExecutableTools(t *testing.T) {
	// Set up logger for the test
	SetLogger(&MockLogger{})

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
	makeReq := func(ctx context.Context, req *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
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

	ctx := context.Background()

	// Execute agent mode - should return immediately with non-auto-executable tools
	result, err := ExecuteAgentForChatRequest(&ctx, 10, originalReq, responseWithNonAutoTools, makeReq, nil, nil, &MockClientManager{})

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
	// Set up logger for the test
	SetLogger(&MockLogger{})

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
	makeReq := func(ctx context.Context, req *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
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

	ctx := context.Background()

	result, err := ExecuteAgentForResponsesRequest(&ctx, 10, originalReq, responseNoTools, makeReq, nil, nil, &MockClientManager{})
	if err != nil {
		t.Errorf("Expected no error for response without tool calls, got: %v", err)
	}
	if result != responseNoTools {
		t.Error("Expected same response to be returned for response without tool calls")
	}
}

func TestExecuteAgentForResponsesRequest_WithNonAutoExecutableTools(t *testing.T) {
	// Set up logger for the test
	SetLogger(&MockLogger{})

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
	makeReq := func(ctx context.Context, req *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
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

	ctx := context.Background()

	// Execute agent mode - should return immediately with non-auto-executable tools
	result, err := ExecuteAgentForResponsesRequest(&ctx, 10, originalReq, responseWithNonAutoTools, makeReq, nil, nil, &MockClientManager{})

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
