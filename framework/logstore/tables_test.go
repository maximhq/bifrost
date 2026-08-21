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
				InputCost:  0.0021,
				OutputCost: 0.0075,
				InputCostDetails: &schemas.InputCostDetails{
					TextCost:       0.00165,
					CachedReadCost: 0.00045,
				},
				AdditionalCost: 0.0003,
				AdditionalCostDetails: &schemas.AdditionalCostDetails{
					GuardrailCost: 0.0003,
				},
				TotalCost: 0.0099,
			},
		},
	}
	require.NoError(t, log.SerializeFields())

	// Token denorm still works.
	assert.Equal(t, 1000, log.PromptTokens)
	assert.Equal(t, 400, log.CachedReadTokens)
	// Input/output/additional cost split is denormalized for SQL aggregation.
	assert.InDelta(t, 0.0021, log.InputCost, 1e-12)
	assert.InDelta(t, 0.0075, log.OutputCost, 1e-12)
	assert.InDelta(t, 0.0003, log.AdditionalCost, 1e-12)
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
	assert.Zero(t, log.AdditionalCost)
}

// TestSerializeFieldsAttributesOpaqueTotalToInput covers providers that report
// only a total with no per-category split (xAI usd-ticks, Runware): the
// denormalized columns must still reconcile to the cost column by attributing
// the total to the input side.
func TestSerializeFieldsAttributesOpaqueTotalToInput(t *testing.T) {
	total := 0.0042
	log := &Log{
		Cost: &total,
		TokenUsageParsed: &schemas.BifrostLLMUsage{
			PromptTokens: 100,
			// Opaque total only, no input/output/additional split.
			Cost: &schemas.BifrostCost{TotalCost: total},
		},
	}
	require.NoError(t, log.SerializeFields())
	assert.InDelta(t, total, log.InputCost, 1e-12)
	assert.Zero(t, log.OutputCost)
	assert.Zero(t, log.AdditionalCost)
	assert.InDelta(t, total, log.InputCost+log.OutputCost+log.AdditionalCost, 1e-12)
}

// TestSerializeFieldsOpaqueTotalNoUsageCarrier covers the same opaque-total
// reconciliation when there is no usage carrier at all (e.g. OCR rows before the
// plugin denormalizes): the total on the cost column still lands on input.
func TestSerializeFieldsOpaqueTotalNoUsageCarrier(t *testing.T) {
	total := 0.01
	log := &Log{Cost: &total}
	require.NoError(t, log.SerializeFields())
	assert.InDelta(t, total, log.InputCost, 1e-12)
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
