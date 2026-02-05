package ollama

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestExtractBase64Image(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "data URL with JPEG",
			input:    "data:image/jpeg;base64,/9j/4AAQSkZJRg==",
			expected: "/9j/4AAQSkZJRg==",
		},
		{
			name:     "data URL with PNG",
			input:    "data:image/png;base64,iVBORw0KGgoAAAANSUhEUg==",
			expected: "iVBORw0KGgoAAAANSUhEUg==",
		},
		{
			name:     "raw base64",
			input:    "iVBORw0KGgoAAAANSUhEUg==",
			expected: "iVBORw0KGgoAAAANSUhEUg==",
		},
		{
			name:     "HTTP URL",
			input:    "https://example.com/image.jpg",
			expected: "",
		},
		{
			name:     "HTTPS URL",
			input:    "https://example.com/image.png",
			expected: "",
		},
		{
			name:     "empty string",
			input:    "",
			expected: "",
		},
		{
			name:     "malformed data URL - no comma",
			input:    "data:image/jpeg;base64",
			expected: "",
		},
		{
			name:     "malformed data URL - empty after comma",
			input:    "data:image/jpeg;base64,",
			expected: "",
		},
		{
			name:     "raw string passed through",
			input:    "not-valid-base64!@#$%",
			expected: "not-valid-base64!@#$%",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractBase64Image(tt.input)
			if result != tt.expected {
				t.Errorf("extractBase64Image(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestConvertMessagesToOllama_ToolCalls(t *testing.T) {
	t.Run("assistant message with tool calls", func(t *testing.T) {
		functionName := "getWeather"
		messages := []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleAssistant,
				Content: &schemas.ChatMessageContent{
					ContentStr: schemas.Ptr("I'll check the weather for you."),
				},
				ChatAssistantMessage: &schemas.ChatAssistantMessage{
					ToolCalls: []schemas.ChatAssistantMessageToolCall{
						{
							Index: 0,
							Type:  schemas.Ptr("function"),
							ID:    schemas.Ptr("call_123"),
							Function: schemas.ChatAssistantMessageToolCallFunction{
								Name:      &functionName,
								Arguments: `{"location":"San Francisco"}`,
							},
						},
					},
				},
			},
		}

		result := convertMessagesToOllama(messages)

		if len(result) != 1 {
			t.Fatalf("Expected 1 message, got %d", len(result))
		}

		msg := result[0]
		if msg.Role != "assistant" {
			t.Errorf("Expected role 'assistant', got %q", msg.Role)
		}

		if len(msg.ToolCalls) != 1 {
			t.Fatalf("Expected 1 tool call, got %d", len(msg.ToolCalls))
		}

		if msg.ToolCalls[0].Function.Name != "getWeather" {
			t.Errorf("Expected function name 'getWeather', got %q", msg.ToolCalls[0].Function.Name)
		}

		if msg.ToolName != nil {
			t.Errorf("ToolName should be nil for assistant messages, got %q", *msg.ToolName)
		}
	})

	t.Run("tool response message with correct mapping", func(t *testing.T) {
		functionName := "getWeather"
		// First: assistant makes a tool call
		// Second: tool response references that call by tool_call_id
		messages := []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleAssistant,
				Content: &schemas.ChatMessageContent{
					ContentStr: schemas.Ptr("I'll check the weather."),
				},
				ChatAssistantMessage: &schemas.ChatAssistantMessage{
					ToolCalls: []schemas.ChatAssistantMessageToolCall{
						{
							Index: 0,
							Type:  schemas.Ptr("function"),
							ID:    schemas.Ptr("call_abc123"),
							Function: schemas.ChatAssistantMessageToolCallFunction{
								Name:      &functionName,
								Arguments: `{"location":"Tokyo"}`,
							},
						},
					},
				},
			},
			{
				Role: schemas.ChatMessageRoleTool,
				Content: &schemas.ChatMessageContent{
					ContentStr: schemas.Ptr(`{"temperature": 72, "condition": "sunny"}`),
				},
				ChatToolMessage: &schemas.ChatToolMessage{
					ToolCallID: schemas.Ptr("call_abc123"), // References the tool call
				},
			},
		}

		result := convertMessagesToOllama(messages)

		if len(result) != 2 {
			t.Fatalf("Expected 2 messages, got %d", len(result))
		}

		// Verify assistant message
		assistantMsg := result[0]
		if assistantMsg.Role != "assistant" {
			t.Errorf("Expected role 'assistant', got %q", assistantMsg.Role)
		}

		// Verify tool response message
		toolMsg := result[1]
		if toolMsg.Role != "tool" {
			t.Errorf("Expected role 'tool', got %q", toolMsg.Role)
		}

		if toolMsg.ToolName == nil {
			t.Fatal("ToolName should be set for tool messages")
		}

		// CRITICAL: tool_name should be "getWeather" (from the mapping), NOT "call_abc123"
		if *toolMsg.ToolName != "getWeather" {
			t.Errorf("Expected tool_name 'getWeather', got %q", *toolMsg.ToolName)
		}

		if len(toolMsg.ToolCalls) != 0 {
			t.Errorf("Tool response messages should not have tool_calls")
		}
	})

	t.Run("tool response without prior assistant message", func(t *testing.T) {
		// Edge case: tool response arrives without a prior tool call in the conversation
		messages := []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleTool,
				Name: schemas.Ptr("getWeather"), // Fallback to Name field
				Content: &schemas.ChatMessageContent{
					ContentStr: schemas.Ptr(`{"temperature": 72}`),
				},
				ChatToolMessage: &schemas.ChatToolMessage{
					ToolCallID: schemas.Ptr("call_unknown"),
				},
			},
		}

		result := convertMessagesToOllama(messages)

		if len(result) != 1 {
			t.Fatalf("Expected 1 message, got %d", len(result))
		}

		msg := result[0]
		if msg.ToolName == nil {
			t.Fatal("ToolName should be set using Name field as fallback")
		}

		if *msg.ToolName != "getWeather" {
			t.Errorf("Expected tool_name 'getWeather' from Name field, got %q", *msg.ToolName)
		}
	})

	t.Run("multiple tool calls and responses", func(t *testing.T) {
		weatherFunc := "getWeather"
		timeFunc := "getTime"
		messages := []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleAssistant,
				Content: &schemas.ChatMessageContent{
					ContentStr: schemas.Ptr("I'll check both."),
				},
				ChatAssistantMessage: &schemas.ChatAssistantMessage{
					ToolCalls: []schemas.ChatAssistantMessageToolCall{
						{
							ID: schemas.Ptr("call_weather"),
							Function: schemas.ChatAssistantMessageToolCallFunction{
								Name:      &weatherFunc,
								Arguments: `{"location":"NYC"}`,
							},
						},
						{
							ID: schemas.Ptr("call_time"),
							Function: schemas.ChatAssistantMessageToolCallFunction{
								Name:      &timeFunc,
								Arguments: `{"timezone":"EST"}`,
							},
						},
					},
				},
			},
			{
				Role: schemas.ChatMessageRoleTool,
				Content: &schemas.ChatMessageContent{
					ContentStr: schemas.Ptr(`{"temp": 65}`),
				},
				ChatToolMessage: &schemas.ChatToolMessage{
					ToolCallID: schemas.Ptr("call_weather"),
				},
			},
			{
				Role: schemas.ChatMessageRoleTool,
				Content: &schemas.ChatMessageContent{
					ContentStr: schemas.Ptr(`{"time": "3pm"}`),
				},
				ChatToolMessage: &schemas.ChatToolMessage{
					ToolCallID: schemas.Ptr("call_time"),
				},
			},
		}

		result := convertMessagesToOllama(messages)

		if len(result) != 3 {
			t.Fatalf("Expected 3 messages, got %d", len(result))
		}

		// Check first tool response
		if result[1].ToolName == nil || *result[1].ToolName != "getWeather" {
			t.Errorf("Expected first tool response to have tool_name 'getWeather'")
		}

		// Check second tool response
		if result[2].ToolName == nil || *result[2].ToolName != "getTime" {
			t.Errorf("Expected second tool response to have tool_name 'getTime'")
		}
	})

	t.Run("tool calls on non-assistant message should be ignored", func(t *testing.T) {
		functionName := "someFunction"
		messages := []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentStr: schemas.Ptr("Hello"),
				},
				ChatAssistantMessage: &schemas.ChatAssistantMessage{
					ToolCalls: []schemas.ChatAssistantMessageToolCall{
						{
							Function: schemas.ChatAssistantMessageToolCallFunction{
								Name: &functionName,
							},
						},
					},
				},
			},
		}

		result := convertMessagesToOllama(messages)

		if len(result) != 1 {
			t.Fatalf("Expected 1 message, got %d", len(result))
		}

		// Tool calls should not be present for non-assistant messages
		if len(result[0].ToolCalls) != 0 {
			t.Errorf("User messages should not have tool_calls in Ollama format")
		}
	})
}

func TestConvertMessagesFromOllama_ToolCalls(t *testing.T) {
	t.Run("assistant message with tool calls", func(t *testing.T) {
		messages := []OllamaMessage{
			{
				Role:    "assistant",
				Content: "I'll check the weather for you.",
				ToolCalls: []OllamaToolCall{
					{
						Function: OllamaToolCallFunction{
							Name: "getWeather",
							Arguments: map[string]interface{}{
								"location": "San Francisco",
							},
						},
					},
				},
			},
		}

		result := convertMessagesFromOllama(messages)

		if len(result) != 1 {
			t.Fatalf("Expected 1 message, got %d", len(result))
		}

		msg := result[0]
		if msg.Role != schemas.ChatMessageRoleAssistant {
			t.Errorf("Expected role 'assistant', got %q", msg.Role)
		}

		if msg.ChatAssistantMessage == nil {
			t.Fatal("ChatAssistantMessage should not be nil")
		}

		if len(msg.ChatAssistantMessage.ToolCalls) != 1 {
			t.Fatalf("Expected 1 tool call, got %d", len(msg.ChatAssistantMessage.ToolCalls))
		}

		toolCall := msg.ChatAssistantMessage.ToolCalls[0]
		if toolCall.Function.Name == nil || *toolCall.Function.Name != "getWeather" {
			t.Errorf("Expected function name 'getWeather'")
		}
	})

	t.Run("tool response message", func(t *testing.T) {
		toolName := "getWeather"
		messages := []OllamaMessage{
			{
				Role:     "tool",
				Content:  `{"temperature": 72, "condition": "sunny"}`,
				ToolName: &toolName,
			},
		}

		result := convertMessagesFromOllama(messages)

		if len(result) != 1 {
			t.Fatalf("Expected 1 message, got %d", len(result))
		}

		msg := result[0]
		if msg.Role != schemas.ChatMessageRoleTool {
			t.Errorf("Expected role 'tool', got %q", msg.Role)
		}

		if msg.ChatToolMessage == nil {
			t.Fatal("ChatToolMessage should not be nil")
		}

		if msg.ChatToolMessage.ToolCallID == nil {
			t.Fatal("ToolCallID should be set")
		}

		if *msg.ChatToolMessage.ToolCallID != "getWeather" {
			t.Errorf("Expected tool_call_id 'getWeather', got %q", *msg.ChatToolMessage.ToolCallID)
		}

		if msg.Name == nil || *msg.Name != "getWeather" {
			t.Errorf("Expected Name 'getWeather'")
		}
	})
}
