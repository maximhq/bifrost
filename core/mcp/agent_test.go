package mcp

import (
	"context"
	"fmt"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// MockLLMCaller implements schemas.BifrostLLMCaller for testing
type MockLLMCaller struct {
	responses []*schemas.BifrostChatResponse
	callCount int
}

func (m *MockLLMCaller) ChatCompletionRequest(ctx context.Context, req *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	if m.callCount >= len(m.responses) {
		return nil, &schemas.BifrostError{
			IsBifrostError: false,
			Error: &schemas.ErrorField{
				Message: "no more mock responses available",
			},
		}
	}

	response := m.responses[m.callCount]
	m.callCount++
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

func TestHasToolCalls(t *testing.T) {
	// Test nil response
	if hasToolCalls(nil) {
		t.Error("Should return false for nil response")
	}

	// Test empty choices
	emptyResponse := &schemas.BifrostChatResponse{
		Choices: []schemas.BifrostResponseChoice{},
	}
	if hasToolCalls(emptyResponse) {
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
	if !hasToolCalls(toolCallsResponse) {
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
	if !hasToolCalls(responseWithToolCalls) {
		t.Error("Should return true for response with tool calls in message")
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

func TestCheckAndExecuteAgentMode(t *testing.T) {
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

	result, err := ExecuteAgent(&ctx, 10, originalReq, responseNoTools, llmCaller, nil, nil, nil)
	if err != nil {
		t.Errorf("Expected no error for response without tool calls, got: %v", err)
	}
	if result != responseNoTools {
		t.Error("Expected same response to be returned for response without tool calls")
	}
}

func TestCheckAndExecuteAgentMode_MaxDepth(t *testing.T) {
	// Set up logger for the test
	SetLogger(&MockLogger{})

	maxDepth := 2

	// Create a response with tool calls that will trigger agent mode
	createToolCallResponse := func(toolID string) *schemas.BifrostChatResponse {
		return &schemas.BifrostChatResponse{
			Choices: []schemas.BifrostResponseChoice{
				{
					FinishReason: schemas.Ptr("tool_calls"),
					ChatNonStreamResponseChoice: &schemas.ChatNonStreamResponseChoice{
						Message: &schemas.ChatMessage{
							Role: schemas.ChatMessageRoleAssistant,
							ChatAssistantMessage: &schemas.ChatAssistantMessage{
								ToolCalls: []schemas.ChatAssistantMessageToolCall{
									{
										ID: schemas.Ptr(toolID),
										Function: schemas.ChatAssistantMessageToolCallFunction{
											Name:      schemas.Ptr("test_tool"),
											Arguments: `{}`,
										},
									},
								},
							},
						},
					},
				},
			},
		}
	}

	// Create mock LLM caller that always returns responses with tool calls
	// This will cause the agent mode to loop until max depth is reached
	initialResponse := createToolCallResponse("call_1")
	response1 := createToolCallResponse("call_2")
	response2 := createToolCallResponse("call_3") // This will be called but max depth will be exceeded

	llmCaller := &MockLLMCaller{
		responses: []*schemas.BifrostChatResponse{
			response1, // First iteration after initial
			response2, // Second iteration (will hit max depth)
		},
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

	// Execute agent mode - should hit max depth and return error
	result, err := ExecuteAgent(&ctx, maxDepth, originalReq, initialResponse, llmCaller, nil, nil, nil)

	// Should return error when max depth is exceeded
	if err == nil {
		t.Error("Expected error when max depth is exceeded, got nil")
	}

	if result != nil {
		t.Error("Expected nil result when max depth is exceeded")
	}

	// Verify error message contains max depth information
	if err != nil && (err.Error == nil || err.Error.Message == "") {
		t.Error("Expected error message when max depth is exceeded")
	}

	expectedErrorMsg := fmt.Sprintf("Agent mode exceeded maximum depth of %d", maxDepth)
	if err.Error.Message != expectedErrorMsg {
		t.Errorf("Expected error message '%s', got '%s'", expectedErrorMsg, err.Error.Message)
	}

	// Verify that the LLM was called the expected number of times
	// Initial response triggers agent mode, then we make maxDepth calls before hitting the limit
	expectedCalls := maxDepth
	if llmCaller.callCount != expectedCalls {
		t.Errorf("Expected %d LLM calls before hitting max depth, got %d", expectedCalls, llmCaller.callCount)
	}
}
