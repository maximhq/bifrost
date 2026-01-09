package bedrock

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/maximhq/bifrost/core/schemas"
)

func TestToBifrostChatResponse_CacheTokens(t *testing.T) {
	tests := []struct {
		name      string
		response  *BedrockConverseResponse
		wantUsage *schemas.BifrostLLMUsage
	}{
		{
			name: "cache read and write tokens",
			response: &BedrockConverseResponse{
				Output: &BedrockConverseOutput{
					Message: &BedrockMessage{
						Role:    "assistant",
						Content: []BedrockContentBlock{{Text: strPtr("Hello")}},
					},
				},
				StopReason: "end_turn",
				Usage: &BedrockTokenUsage{
					InputTokens:           1000,
					OutputTokens:          500,
					TotalTokens:           1500,
					CacheReadInputTokens:  100,
					CacheWriteInputTokens: 200,
				},
			},
			wantUsage: &schemas.BifrostLLMUsage{
				PromptTokens: 1000,
				PromptTokensDetails: &schemas.ChatPromptTokensDetails{
					CachedTokens:        100,
					CacheReadTokens:     100,
					CacheCreationTokens: 200,
				},
				CompletionTokens: 500,
				CompletionTokensDetails: &schemas.ChatCompletionTokensDetails{
					CachedTokens: 200,
				},
				TotalTokens: 1500,
			},
		},
		{
			name: "only cache read tokens",
			response: &BedrockConverseResponse{
				Output: &BedrockConverseOutput{
					Message: &BedrockMessage{
						Role:    "assistant",
						Content: []BedrockContentBlock{{Text: strPtr("Hello")}},
					},
				},
				StopReason: "end_turn",
				Usage: &BedrockTokenUsage{
					InputTokens:          500,
					OutputTokens:         200,
					TotalTokens:          700,
					CacheReadInputTokens: 100,
				},
			},
			wantUsage: &schemas.BifrostLLMUsage{
				PromptTokens: 500,
				PromptTokensDetails: &schemas.ChatPromptTokensDetails{
					CachedTokens:    100,
					CacheReadTokens: 100,
				},
				CompletionTokens: 200,
				TotalTokens:      700,
			},
		},
		{
			name: "no cache tokens",
			response: &BedrockConverseResponse{
				Output: &BedrockConverseOutput{
					Message: &BedrockMessage{
						Role:    "assistant",
						Content: []BedrockContentBlock{{Text: strPtr("Hello")}},
					},
				},
				StopReason: "end_turn",
				Usage: &BedrockTokenUsage{
					InputTokens:  100,
					OutputTokens: 50,
					TotalTokens:  150,
				},
			},
			wantUsage: &schemas.BifrostLLMUsage{
				PromptTokens:     100,
				CompletionTokens: 50,
				TotalTokens:      150,
			},
		},
	}

	ctx := context.Background()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.response.ToBifrostChatResponse(ctx, "test-model")
			if err != nil {
				t.Fatalf("ToBifrostChatResponse failed: %v", err)
			}
			if diff := cmp.Diff(tt.wantUsage, got.Usage); diff != "" {
				t.Errorf("Usage mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestToBifrostResponsesResponse_CacheTokens(t *testing.T) {
	tests := []struct {
		name                   string
		response               *BedrockConverseResponse
		wantInputTokensDetails *schemas.ResponsesResponseInputTokens
	}{
		{
			name: "cache read and write tokens",
			response: &BedrockConverseResponse{
				Output: &BedrockConverseOutput{
					Message: &BedrockMessage{
						Role:    "assistant",
						Content: []BedrockContentBlock{{Text: strPtr("Hello")}},
					},
				},
				StopReason: "end_turn",
				Usage: &BedrockTokenUsage{
					InputTokens:           1000,
					OutputTokens:          500,
					TotalTokens:           1500,
					CacheReadInputTokens:  100,
					CacheWriteInputTokens: 200,
				},
			},
			wantInputTokensDetails: &schemas.ResponsesResponseInputTokens{
				CachedTokens:        100,
				CacheReadTokens:     100,
				CacheCreationTokens: 200,
			},
		},
	}

	ctx := context.Background()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.response.ToBifrostResponsesResponse(&ctx)
			if err != nil {
				t.Fatalf("ToBifrostResponsesResponse failed: %v", err)
			}
			if got.Usage == nil {
				t.Fatal("Usage is nil")
			}
			if diff := cmp.Diff(tt.wantInputTokensDetails, got.Usage.InputTokensDetails); diff != "" {
				t.Errorf("InputTokensDetails mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// Helper function
func strPtr(s string) *string { return &s }
