package semanticcache

import (
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// TestExtractTextForEmbedding_NilContent verifies that extractTextForEmbedding
// does not panic when chat messages have nil Content (e.g., assistant tool-call messages).
func TestExtractTextForEmbedding_NilContent(t *testing.T) {
	plugin := &Plugin{
		config: &Config{},
	}

	tests := []struct {
		name    string
		request *schemas.BifrostRequest
	}{
		{
			name: "ChatRequest with nil Content in assistant tool-call message",
			request: &schemas.BifrostRequest{
				RequestType: schemas.ChatCompletionRequest,
				ChatRequest: &schemas.BifrostChatRequest{
					Provider: schemas.OpenAI,
					Model:    "gpt-4o-mini",
					Input: []schemas.ChatMessage{
						{
							Role: schemas.ChatMessageRoleUser,
							Content: &schemas.ChatMessageContent{
								ContentStr: bifrost.Ptr("Call the get_weather function"),
							},
						},
						{
							Role:    schemas.ChatMessageRoleAssistant,
							Content: nil, // tool-call message with no content
							ChatAssistantMessage: &schemas.ChatAssistantMessage{
								ToolCalls: []schemas.ChatAssistantMessageToolCall{
									{
										ID:   bifrost.Ptr("call_123"),
										Type: bifrost.Ptr("function"),
										Function: schemas.ChatAssistantMessageToolCallFunction{
											Name:      bifrost.Ptr("get_weather"),
											Arguments: `{"location": "San Francisco"}`,
										},
									},
								},
							},
						},
					},
					Params: &schemas.ChatParameters{
						Temperature:         bifrost.Ptr(0.7),
						MaxCompletionTokens: bifrost.Ptr(100),
					},
				},
			},
		},
		{
			name: "ChatRequest where all messages have nil Content",
			request: &schemas.BifrostRequest{
				RequestType: schemas.ChatCompletionRequest,
				ChatRequest: &schemas.BifrostChatRequest{
					Provider: schemas.OpenAI,
					Model:    "gpt-4o-mini",
					Input: []schemas.ChatMessage{
						{
							Role:    schemas.ChatMessageRoleAssistant,
							Content: nil,
						},
					},
					Params: &schemas.ChatParameters{
						Temperature:         bifrost.Ptr(0.7),
						MaxCompletionTokens: bifrost.Ptr(100),
					},
				},
			},
		},
		{
			name: "ResponsesRequest with nil Content",
			request: &schemas.BifrostRequest{
				RequestType:      schemas.ResponsesRequest,
				ResponsesRequest: createResponsesRequestWithNilContent(),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// This should not panic
			text, hash, err := plugin.extractTextForEmbedding(tt.request)
			// We don't care about the error — the important thing is no panic
			t.Logf("text=%q, hash=%q, err=%v", text, hash, err)
		})
	}
}

// TestGetNormalizedInputForCaching_NilContent verifies that getNormalizedInputForCaching
// does not panic when chat messages have nil Content.
func TestGetNormalizedInputForCaching_NilContent(t *testing.T) {
	plugin := &Plugin{
		config: &Config{},
	}

	request := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4o-mini",
			Input: []schemas.ChatMessage{
				{
					Role: schemas.ChatMessageRoleUser,
					Content: &schemas.ChatMessageContent{
						ContentStr: bifrost.Ptr("Call the get_weather function"),
					},
				},
				{
					Role:    schemas.ChatMessageRoleAssistant,
					Content: nil,
					ChatAssistantMessage: &schemas.ChatAssistantMessage{
						ToolCalls: []schemas.ChatAssistantMessageToolCall{
							{
								ID:   bifrost.Ptr("call_123"),
								Type: bifrost.Ptr("function"),
								Function: schemas.ChatAssistantMessageToolCallFunction{
									Name:      bifrost.Ptr("get_weather"),
									Arguments: `{"location": "San Francisco"}`,
								},
							},
						},
					},
				},
			},
			Params: &schemas.ChatParameters{
				Temperature:         bifrost.Ptr(0.7),
				MaxCompletionTokens: bifrost.Ptr(100),
			},
		},
	}

	// This should not panic
	result := plugin.getNormalizedInputForCaching(request)
	t.Logf("result type: %T", result)
}

// createResponsesRequestWithNilContent builds a BifrostResponsesRequest with a nil Content message for testing.
func createResponsesRequestWithNilContent() *schemas.BifrostResponsesRequest {
	return &schemas.BifrostResponsesRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o-mini",
		Input: []schemas.ResponsesMessage{
			{
				Role: bifrost.Ptr(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{
					ContentStr: bifrost.Ptr("Hello"),
				},
			},
			{
				Role:    bifrost.Ptr(schemas.ResponsesInputMessageRoleAssistant),
				Content: nil,
			},
		},
		Params: &schemas.ResponsesParameters{
			Temperature:     bifrost.Ptr(0.7),
			MaxOutputTokens: bifrost.Ptr(100),
		},
	}
}
