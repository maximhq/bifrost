package logstore

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSerializeFieldsDenormalizesCostBreakdown(t *testing.T) {
	log := &Log{
		TokenUsageParsed: &schemas.BifrostLLMUsage{
			PromptTokens:     1000,
			CompletionTokens: 500,
			TotalTokens:      1500,
			PromptTokensDetails: &schemas.ChatPromptTokensDetails{
				CachedReadTokens: 400,
			},
			Cost: &schemas.BifrostCost{
				InputTokensCost:     0.0021,
				OutputTokensCost:    0.0075,
				CacheReadTokensCost: 0.00045,
				TotalCost:           0.0096,
			},
		},
	}
	require.NoError(t, log.SerializeFields())

	// Token denorm still works.
	assert.Equal(t, 1000, log.PromptTokens)
	assert.Equal(t, 400, log.CachedReadTokens)
	// Cost split is denormalized for SQL aggregation.
	assert.InDelta(t, 0.0021, log.InputCost, 1e-12)
	assert.InDelta(t, 0.0075, log.OutputCost, 1e-12)
	assert.InDelta(t, 0.00045, log.CacheReadCost, 1e-12)
}

func TestSerializeFieldsLeavesCostBreakdownZeroWhenNoCost(t *testing.T) {
	log := &Log{
		TokenUsageParsed: &schemas.BifrostLLMUsage{
			PromptTokens:     10,
			CompletionTokens: 5,
			TotalTokens:      15,
		},
	}
	require.NoError(t, log.SerializeFields())
	assert.Zero(t, log.InputCost)
	assert.Zero(t, log.OutputCost)
	assert.Zero(t, log.CacheReadCost)
}

func TestDeserializeFieldsReconstructsTokenUsageFromDenormalizedColumns(t *testing.T) {
	log := &Log{
		PromptTokens:     10,
		CompletionTokens: 5,
		TotalTokens:      15,
	}
	require.NoError(t, log.DeserializeFields())
	require.NotNil(t, log.TokenUsageParsed)
	assert.Equal(t, 10, log.TokenUsageParsed.PromptTokens)
	assert.Equal(t, 5, log.TokenUsageParsed.CompletionTokens)
	assert.Equal(t, 15, log.TokenUsageParsed.TotalTokens)
	assert.Nil(t, log.TokenUsageParsed.PromptTokensDetails)
}

func TestDeserializeFieldsPrefersSerializedTokenUsage(t *testing.T) {
	log := &Log{
		TokenUsage:       `{"prompt_tokens":99,"completion_tokens":1,"total_tokens":100}`,
		PromptTokens:     10,
		CompletionTokens: 5,
		TotalTokens:      15,
	}
	require.NoError(t, log.DeserializeFields())
	require.NotNil(t, log.TokenUsageParsed)
	assert.Equal(t, 99, log.TokenUsageParsed.PromptTokens)
	assert.Equal(t, 1, log.TokenUsageParsed.CompletionTokens)
	assert.Equal(t, 100, log.TokenUsageParsed.TotalTokens)
}

func TestDeserializeFieldsDoesNotReconstructTokenUsageWhenSerializedValueIsMalformed(t *testing.T) {
	log := &Log{
		TokenUsage:       `{"prompt_tokens":`,
		PromptTokens:     10,
		CompletionTokens: 5,
		TotalTokens:      15,
	}
	require.NoError(t, log.DeserializeFields())
	assert.Nil(t, log.TokenUsageParsed)
}

func TestDeserializeFieldsSkipsTokenUsageReconstructionWhenAllZero(t *testing.T) {
	log := &Log{}
	require.NoError(t, log.DeserializeFields())
	assert.Nil(t, log.TokenUsageParsed)
}
