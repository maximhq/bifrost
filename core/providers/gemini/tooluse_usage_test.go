package gemini

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestConvertGeminiUsageMetadataToChatUsage_ToolUsePromptTokens validates that
// toolUsePromptTokenCount is added to PromptTokens in Chat completions usage.
func TestConvertGeminiUsageMetadataToChatUsage_ToolUsePromptTokens(t *testing.T) {
	tests := []struct {
		name                 string
		metadata             *GenerateContentResponseUsageMetadata
		expectedPromptTokens int
		expectedCompTokens   int
		expectedTotalTokens  int
		expectedCachedTokens int
		expectedReasoning    int
	}{
		{
			name:                 "nil metadata returns nil",
			metadata:             nil,
			expectedPromptTokens: 0,
		},
		{
			name: "zero tool-use tokens leaves prompt tokens unchanged",
			metadata: &GenerateContentResponseUsageMetadata{
				PromptTokenCount:        20,
				ToolUsePromptTokenCount: 0,
				CandidatesTokenCount:    30,
				TotalTokenCount:         50,
			},
			expectedPromptTokens: 20,
			expectedCompTokens:   30,
			expectedTotalTokens:  50,
		},
		{
			name: "documented URL-context example with thinking tokens",
			metadata: &GenerateContentResponseUsageMetadata{
				PromptTokenCount:        27,
				ToolUsePromptTokenCount: 10309,
				ThoughtsTokenCount:      31,
				CandidatesTokenCount:    45,
				TotalTokenCount:         10412,
			},
			expectedPromptTokens: 10336, // 27 + 10309
			expectedCompTokens:   76,    // 45 + 31
			expectedTotalTokens:  10412,
			expectedReasoning:    31,
		},
		{
			name: "tool-use tokens combined with cached content",
			metadata: &GenerateContentResponseUsageMetadata{
				PromptTokenCount:        100,
				ToolUsePromptTokenCount: 500,
				CachedContentTokenCount: 80,
				CandidatesTokenCount:    40,
				TotalTokenCount:         640,
			},
			expectedPromptTokens: 600, // 100 + 500
			expectedCompTokens:   40,
			expectedTotalTokens:  640,
			expectedCachedTokens: 80,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			usage := ConvertGeminiUsageMetadataToChatUsage(tt.metadata)
			if tt.metadata == nil {
				assert.Nil(t, usage)
				return
			}
			require.NotNil(t, usage)
			assert.Equal(t, tt.expectedPromptTokens, usage.PromptTokens)
			assert.Equal(t, tt.expectedCompTokens, usage.CompletionTokens)
			assert.Equal(t, tt.expectedTotalTokens, usage.TotalTokens)

			if tt.expectedReasoning > 0 {
				require.NotNil(t, usage.CompletionTokensDetails)
				assert.Equal(t, tt.expectedReasoning, usage.CompletionTokensDetails.ReasoningTokens)
			}
			if tt.expectedCachedTokens > 0 {
				require.NotNil(t, usage.PromptTokensDetails)
				assert.Equal(t, tt.expectedCachedTokens, usage.PromptTokensDetails.CachedReadTokens)
			}
		})
	}
}

// TestConvertGeminiUsageMetadataToResponsesUsage_ToolUsePromptTokens validates that
// toolUsePromptTokenCount is added to InputTokens in Responses API usage.
func TestConvertGeminiUsageMetadataToResponsesUsage_ToolUsePromptTokens(t *testing.T) {
	tests := []struct {
		name                string
		metadata            *GenerateContentResponseUsageMetadata
		expectedInputTokens int
		expectedOutTokens   int
		expectedTotalTokens int
		expectedCachedRead  int
		expectedReasoning   int
	}{
		{
			name:                "nil metadata returns nil",
			metadata:            nil,
			expectedInputTokens: 0,
		},
		{
			name: "zero tool-use tokens leaves input tokens unchanged",
			metadata: &GenerateContentResponseUsageMetadata{
				PromptTokenCount:        50,
				ToolUsePromptTokenCount: 0,
				CandidatesTokenCount:    25,
				TotalTokenCount:         75,
			},
			expectedInputTokens: 50,
			expectedOutTokens:   25,
			expectedTotalTokens: 75,
		},
		{
			name: "documented URL-context example with thinking tokens",
			metadata: &GenerateContentResponseUsageMetadata{
				PromptTokenCount:        27,
				ToolUsePromptTokenCount: 10309,
				ThoughtsTokenCount:      31,
				CandidatesTokenCount:    45,
				TotalTokenCount:         10412,
			},
			expectedInputTokens: 10336, // 27 + 10309
			expectedOutTokens:   76,    // 45 + 31
			expectedTotalTokens: 10412,
			expectedReasoning:   31,
		},
		{
			name: "tool-use tokens combined with cached content",
			metadata: &GenerateContentResponseUsageMetadata{
				PromptTokenCount:        150,
				ToolUsePromptTokenCount: 350,
				CachedContentTokenCount: 100,
				CandidatesTokenCount:    60,
				TotalTokenCount:         560,
			},
			expectedInputTokens: 500, // 150 + 350
			expectedOutTokens:   60,
			expectedTotalTokens: 560,
			expectedCachedRead:  100,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			usage := ConvertGeminiUsageMetadataToResponsesUsage(tt.metadata)
			if tt.metadata == nil {
				assert.Nil(t, usage)
				return
			}
			require.NotNil(t, usage)
			assert.Equal(t, tt.expectedInputTokens, usage.InputTokens)
			assert.Equal(t, tt.expectedOutTokens, usage.OutputTokens)
			assert.Equal(t, tt.expectedTotalTokens, usage.TotalTokens)

			if tt.expectedReasoning > 0 {
				require.NotNil(t, usage.OutputTokensDetails)
				assert.Equal(t, tt.expectedReasoning, usage.OutputTokensDetails.ReasoningTokens)
			}
			if tt.expectedCachedRead > 0 {
				require.NotNil(t, usage.InputTokensDetails)
				assert.Equal(t, tt.expectedCachedRead, usage.InputTokensDetails.CachedReadTokens)
			}
		})
	}
}

// TestConvertResponsesUsageToGeminiUsageMetadata_NoDoubleCounting verifies that
// reverse conversion correctly maps InputTokens back to PromptTokenCount without double-counting.
func TestConvertResponsesUsageToGeminiUsageMetadata_NoDoubleCounting(t *testing.T) {
	origMetadata := &GenerateContentResponseUsageMetadata{
		PromptTokenCount:        27,
		ToolUsePromptTokenCount: 10309,
		ThoughtsTokenCount:      31,
		CandidatesTokenCount:    45,
		TotalTokenCount:         10412,
	}

	bifrostUsage := ConvertGeminiUsageMetadataToResponsesUsage(origMetadata)
	require.NotNil(t, bifrostUsage)
	assert.Equal(t, 10336, bifrostUsage.InputTokens)
	assert.Equal(t, 76, bifrostUsage.OutputTokens)

	reverseMetadata := ConvertBifrostResponsesUsageToGeminiUsageMetadata(bifrostUsage)
	require.NotNil(t, reverseMetadata)
	assert.Equal(t, int32(10336), reverseMetadata.PromptTokenCount)
	assert.Equal(t, int32(0), reverseMetadata.ToolUsePromptTokenCount)
	assert.Equal(t, int32(45), reverseMetadata.CandidatesTokenCount)
	assert.Equal(t, int32(31), reverseMetadata.ThoughtsTokenCount)
	assert.Equal(t, int32(10412), reverseMetadata.TotalTokenCount)
}

// TestGeminiBatchResult_ToolUsePromptTokens verifies that Gemini batch body construction
// includes toolUsePromptTokenCount in prompt_tokens.
func TestGeminiBatchResult_ToolUsePromptTokens(t *testing.T) {
	resp := &GenerateContentResponse{
		Candidates: []*Candidate{
			{
				FinishReason: FinishReasonStop,
				Content:      &Content{Parts: []*Part{{Text: "Batch answer"}}},
			},
		},
		UsageMetadata: &GenerateContentResponseUsageMetadata{
			PromptTokenCount:        27,
			ToolUsePromptTokenCount: 10309,
			ThoughtsTokenCount:      31,
			CandidatesTokenCount:    45,
			TotalTokenCount:         10412,
		},
	}

	body := geminiGenerateContentToBatchResultBody(resp)
	require.NotNil(t, body["usage"])
	usage, ok := body["usage"].(map[string]interface{})
	require.True(t, ok)

	assert.EqualValues(t, 10336, usage["prompt_tokens"])
	assert.EqualValues(t, 76, usage["completion_tokens"])
	assert.EqualValues(t, 10412, usage["total_tokens"])
}

// TestExtractGeminiPassthroughUsage_ToolUsePromptTokens verifies that passthrough usage extraction
// includes toolUsePromptTokenCount on both generateContent and interactions endpoints.
func TestExtractGeminiPassthroughUsage_ToolUsePromptTokens(t *testing.T) {
	t.Run("generateContent passthrough", func(t *testing.T) {
		body := []byte(`{
			"candidates": [{"content": {"parts": [{"text": "Hello"}]}, "finishReason": "STOP"}],
			"usageMetadata": {
				"promptTokenCount": 27,
				"toolUsePromptTokenCount": 10309,
				"thoughtsTokenCount": 31,
				"candidatesTokenCount": 45,
				"totalTokenCount": 10412
			}
		}`)

		pu := extractGeminiGenerateContentUsage(body)
		require.NotNil(t, pu)
		require.NotNil(t, pu.LLMUsage)
		assert.Equal(t, 10336, pu.LLMUsage.PromptTokens)
		assert.Equal(t, 76, pu.LLMUsage.CompletionTokens)
		assert.Equal(t, 10412, pu.LLMUsage.TotalTokens)
	})

	t.Run("interactions passthrough", func(t *testing.T) {
		body := []byte(`{
			"usage": {
				"total_input_tokens": 27,
				"tool_use_input_tokens": 10309,
				"total_thought_tokens": 31,
				"total_output_tokens": 45,
				"total_tokens": 10412
			}
		}`)

		iu := extractGeminiInteractionsUsage(body)
		require.NotNil(t, iu)
		require.NotNil(t, iu.LLMUsage)
		assert.Equal(t, 10336, iu.LLMUsage.PromptTokens)
		assert.Equal(t, 76, iu.LLMUsage.CompletionTokens)
		assert.Equal(t, 10412, iu.LLMUsage.TotalTokens)
	})
}

// BenchmarkConvertGeminiUsageMetadataToChatUsage measures the overhead of chat usage conversion
// including toolUsePromptTokenCount and reasoning tokens.
func BenchmarkConvertGeminiUsageMetadataToChatUsage(b *testing.B) {
	meta := &GenerateContentResponseUsageMetadata{
		PromptTokenCount:        27,
		ToolUsePromptTokenCount: 10309,
		ThoughtsTokenCount:      31,
		CandidatesTokenCount:    45,
		TotalTokenCount:         10412,
	}
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = ConvertGeminiUsageMetadataToChatUsage(meta)
	}
}

// BenchmarkConvertGeminiUsageMetadataToResponsesUsage measures the overhead of Responses API
// usage conversion including toolUsePromptTokenCount.
func BenchmarkConvertGeminiUsageMetadataToResponsesUsage(b *testing.B) {
	meta := &GenerateContentResponseUsageMetadata{
		PromptTokenCount:        27,
		ToolUsePromptTokenCount: 10309,
		ThoughtsTokenCount:      31,
		CandidatesTokenCount:    45,
		TotalTokenCount:         10412,
	}
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = ConvertGeminiUsageMetadataToResponsesUsage(meta)
	}
}
