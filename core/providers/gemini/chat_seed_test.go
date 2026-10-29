package gemini_test

import (
	"math"
	"testing"

	"github.com/maximhq/bifrost/core/providers/gemini"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func chatRequestWithSeed(seed *int) *schemas.BifrostChatRequest {
	return &schemas.BifrostChatRequest{
		Provider: schemas.Gemini,
		Model:    "gemini-2.5-flash",
		Input: []schemas.ChatMessage{
			{
				Role:    schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("Hello")},
			},
		},
		Params: &schemas.ChatParameters{Seed: seed},
	}
}

func TestToGeminiChatCompletionRequest_ForwardsSeed(t *testing.T) {
	geminiReq, err := gemini.ToGeminiChatCompletionRequest(nil, chatRequestWithSeed(schemas.Ptr(42)))
	require.NoError(t, err)
	require.NotNil(t, geminiReq)
	require.NotNil(t, geminiReq.GenerationConfig.Seed)
	assert.Equal(t, int32(42), *geminiReq.GenerationConfig.Seed)
}

func TestToGeminiChatCompletionRequest_ForwardsZeroSeed(t *testing.T) {
	geminiReq, err := gemini.ToGeminiChatCompletionRequest(nil, chatRequestWithSeed(schemas.Ptr(0)))
	require.NoError(t, err)
	require.NotNil(t, geminiReq.GenerationConfig.Seed)
	assert.Equal(t, int32(0), *geminiReq.GenerationConfig.Seed)
}

func TestToGeminiChatCompletionRequest_OmitsSeedWhenUnset(t *testing.T) {
	geminiReq, err := gemini.ToGeminiChatCompletionRequest(nil, chatRequestWithSeed(nil))
	require.NoError(t, err)
	assert.Nil(t, geminiReq.GenerationConfig.Seed)
}

func TestToGeminiChatCompletionRequest_RejectsSeedOutsideInt32(t *testing.T) {
	for _, seed := range []int{math.MaxInt32 + 1, math.MinInt32 - 1} {
		_, err := gemini.ToGeminiChatCompletionRequest(nil, chatRequestWithSeed(schemas.Ptr(seed)))
		require.Error(t, err, "seed %d", seed)
		assert.Contains(t, err.Error(), "seed must be between")
	}
}

func BenchmarkToGeminiChatCompletionRequest_Seed(b *testing.B) {
	req := chatRequestWithSeed(schemas.Ptr(42))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := gemini.ToGeminiChatCompletionRequest(nil, req); err != nil {
			b.Fatal(err)
		}
	}
}
