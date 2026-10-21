package cohere

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func cohereUsageWithCache() *CohereUsage {
	return &CohereUsage{
		Tokens: &CohereTokenUsage{
			InputTokens:  schemas.Ptr(120),
			OutputTokens: schemas.Ptr(30),
		},
		CachedTokens: schemas.Ptr(96),
	}
}

func TestCohereChatStreamMessageEndCarriesCachedTokens(t *testing.T) {
	event := CohereStreamEvent{
		Type: StreamEventMessageEnd,
		Delta: &CohereStreamDelta{
			FinishReason: schemas.Ptr(FinishReasonComplete),
			Usage:        cohereUsageWithCache(),
		},
	}

	streamResp, bifrostErr, done := event.ToBifrostChatCompletionStream()
	require.Nil(t, bifrostErr)
	require.True(t, done)
	require.NotNil(t, streamResp)
	require.NotNil(t, streamResp.Usage)

	assert.Equal(t, 120, streamResp.Usage.PromptTokens)
	assert.Equal(t, 30, streamResp.Usage.CompletionTokens)
	assert.Equal(t, 150, streamResp.Usage.TotalTokens)
	require.NotNil(t, streamResp.Usage.PromptTokensDetails)
	assert.Equal(t, 96, streamResp.Usage.PromptTokensDetails.CachedReadTokens)

	// Same usage as the non-streaming conversion of the same Cohere usage.
	nonStream := (&CohereChatResponse{
		FinishReason: schemas.Ptr(FinishReasonComplete),
		Usage:        cohereUsageWithCache(),
	}).ToBifrostChatResponse("command-a-03-2025")
	assert.Equal(t, nonStream.Usage, streamResp.Usage)
}
