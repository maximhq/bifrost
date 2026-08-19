package logstore

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
