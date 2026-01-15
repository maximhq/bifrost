package openai

import (
	"encoding/json"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestToOpenAIChatResponse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    *schemas.BifrostChatResponse
		validate func(t *testing.T, result *OpenAIChatResponse)
	}{
		{
			name:  "nil input returns nil",
			input: nil,
			validate: func(t *testing.T, result *OpenAIChatResponse) {
				if result != nil {
					t.Error("expected nil result for nil input")
				}
			},
		},
		{
			name: "converts non-streaming response correctly",
			input: &schemas.BifrostChatResponse{
				ID:      "chatcmpl-abc123",
				Object:  "chat.completion",
				Created: 1234567890,
				Model:   "gpt-4o-mini",
				Choices: []schemas.BifrostResponseChoice{
					{
						Index:        0,
						FinishReason: schemas.Ptr("stop"),
						ChatNonStreamResponseChoice: &schemas.ChatNonStreamResponseChoice{
							Message: &schemas.ChatMessage{
								Role:    schemas.ChatMessageRoleAssistant,
								Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("Hello!")},
							},
						},
					},
				},
				Usage: &schemas.BifrostLLMUsage{
					PromptTokens:     10,
					CompletionTokens: 5,
					TotalTokens:      15,
				},
				ExtraFields: &schemas.BifrostResponseExtraFields{
					Provider: schemas.OpenAI,
					Latency:  100,
				},
			},
			validate: func(t *testing.T, result *OpenAIChatResponse) {
				if result == nil {
					t.Fatal("expected non-nil result")
				}
				if result.ID != "chatcmpl-abc123" {
					t.Errorf("expected ID 'chatcmpl-abc123', got '%s'", result.ID)
				}
				if result.Usage == nil {
					t.Fatal("expected usage to be present")
				}
				if result.Usage.PromptTokens != 10 {
					t.Errorf("expected prompt_tokens 10, got %d", result.Usage.PromptTokens)
				}
				if result.Usage.CompletionTokens != 5 {
					t.Errorf("expected completion_tokens 5, got %d", result.Usage.CompletionTokens)
				}
				if result.Usage.TotalTokens != 15 {
					t.Errorf("expected total_tokens 15, got %d", result.Usage.TotalTokens)
				}
				if len(result.Choices) != 1 {
					t.Fatalf("expected 1 choice, got %d", len(result.Choices))
				}
				if result.Choices[0].Message == nil {
					t.Fatal("expected message to be present")
				}
				if result.Choices[0].Message.Role != "assistant" {
					t.Errorf("expected role 'assistant', got '%s'", result.Choices[0].Message.Role)
				}
			},
		},
		{
			name: "converts streaming response correctly",
			input: &schemas.BifrostChatResponse{
				ID:      "chatcmpl-stream123",
				Object:  "chat.completion.chunk",
				Created: 1234567890,
				Model:   "gpt-4o-mini",
				Choices: []schemas.BifrostResponseChoice{
					{
						Index: 0,
						ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{
							Delta: &schemas.ChatStreamResponseChoiceDelta{
								Content: schemas.Ptr("Hello"),
							},
						},
					},
				},
			},
			validate: func(t *testing.T, result *OpenAIChatResponse) {
				if result == nil {
					t.Fatal("expected non-nil result")
				}
				if len(result.Choices) != 1 {
					t.Fatalf("expected 1 choice, got %d", len(result.Choices))
				}
				if result.Choices[0].Delta == nil {
					t.Fatal("expected delta to be present")
				}
				if result.Choices[0].Delta.Content == nil || *result.Choices[0].Delta.Content != "Hello" {
					t.Error("expected delta content 'Hello'")
				}
			},
		},
		{
			name: "output does not contain extra_fields",
			input: &schemas.BifrostChatResponse{
				ID:      "test",
				Object:  "chat.completion",
				Created: 1234567890,
				Model:   "gpt-4",
				ExtraFields: &schemas.BifrostResponseExtraFields{
					Provider: schemas.OpenAI,
					Latency:  100,
				},
			},
			validate: func(t *testing.T, result *OpenAIChatResponse) {
				if result == nil {
					t.Fatal("expected non-nil result")
				}
				// Marshal to JSON and check no extra_fields
				jsonBytes, err := json.Marshal(result)
				if err != nil {
					t.Fatalf("failed to marshal result: %v", err)
				}
				jsonStr := string(jsonBytes)
				if contains(jsonStr, "extra_fields") {
					t.Error("output should not contain extra_fields")
				}
				if contains(jsonStr, "latency") {
					t.Error("output should not contain latency")
				}
				if contains(jsonStr, "model_requested") {
					t.Error("output should not contain model_requested")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ToOpenAIChatResponse(tt.input)
			tt.validate(t, result)
		})
	}
}

func TestToOpenAIChatStreamResponse(t *testing.T) {
	t.Parallel()

	input := &schemas.BifrostChatResponse{
		ID:     "stream-test",
		Object: "", // Empty, should default to chunk
		Choices: []schemas.BifrostResponseChoice{
			{
				Index: 0,
				ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{
					Delta: &schemas.ChatStreamResponseChoiceDelta{
						Content: schemas.Ptr("Hi"),
					},
				},
			},
		},
	}

	result := ToOpenAIChatStreamResponse(input)
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.Object != "chat.completion.chunk" {
		t.Errorf("expected object 'chat.completion.chunk', got '%s'", result.Object)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
