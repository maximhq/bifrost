package schemas

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGuardrailMetadataContextRoundTrip verifies typed guardrail metadata context storage.
func TestGuardrailMetadataContextRoundTrip(t *testing.T) {
	ctx := NewBifrostContext(nil, NoDeadline)
	call := BifrostGuardrailJudgeCall{
		Phase:         "input",
		RuleName:      "pii",
		JudgeProvider: OpenAI,
		JudgeModel:    "gpt-4o-mini",
		PromptTokens:  12,
		TotalTokens:   12,
	}

	require.True(t, AppendGuardrailJudgeCallOnContext(ctx, call))
	metadata, ok := GuardrailMetadataFromContext(ctx)
	require.True(t, ok)
	require.Len(t, metadata.JudgeCalls, 1)
	assert.Equal(t, call, metadata.JudgeCalls[0])
}

// TestGuardrailMetadataContextReturnsOwnedSnapshot verifies callers cannot mutate context state.
func TestGuardrailMetadataContextReturnsOwnedSnapshot(t *testing.T) {
	ctx := NewBifrostContext(nil, NoDeadline)
	require.True(t, AppendGuardrailJudgeCallOnContext(ctx, BifrostGuardrailJudgeCall{
		JudgeProvider: OpenAI,
		JudgeModel:    "gpt-4o-mini",
		TotalTokens:   10,
	}))

	first, ok := GuardrailMetadataFromContext(ctx)
	require.True(t, ok)
	first.JudgeCalls[0].TotalTokens = 999

	second, ok := GuardrailMetadataFromContext(ctx)
	require.True(t, ok)
	assert.Equal(t, 10, second.JudgeCalls[0].TotalTokens)
}

// TestGuardrailMetadataContextClonesUsageDetails verifies nested pricing details cannot alias context state.
func TestGuardrailMetadataContextClonesUsageDetails(t *testing.T) {
	ctx := NewBifrostContext(nil, NoDeadline)
	citationTokens := 3
	require.True(t, AppendGuardrailJudgeCallOnContext(ctx, BifrostGuardrailJudgeCall{
		JudgeProvider: OpenAI,
		JudgeModel:    "gpt-4o-mini",
		PromptTokens:  10,
		PromptTokensDetails: &ChatPromptTokensDetails{
			CachedWriteTokenDetails: &ChatCachedWriteTokenDetails{CachedWriteTokens5m: 4},
		},
		CompletionTokens: 5,
		CompletionTokensDetails: &ChatCompletionTokensDetails{
			CitationTokens: &citationTokens,
		},
		TotalTokens: 15,
	}))

	first, ok := GuardrailMetadataFromContext(ctx)
	require.True(t, ok)
	first.JudgeCalls[0].PromptTokensDetails.CachedWriteTokenDetails.CachedWriteTokens5m = 999
	*first.JudgeCalls[0].CompletionTokensDetails.CitationTokens = 999

	second, ok := GuardrailMetadataFromContext(ctx)
	require.True(t, ok)
	assert.Equal(t, 4, second.JudgeCalls[0].PromptTokensDetails.CachedWriteTokenDetails.CachedWriteTokens5m)
	assert.Equal(t, 3, *second.JudgeCalls[0].CompletionTokensDetails.CitationTokens)
}

// TestAppendGuardrailJudgeCallRejectsEmptyUsage verifies non-billable calls are omitted.
func TestAppendGuardrailJudgeCallRejectsEmptyUsage(t *testing.T) {
	ctx := NewBifrostContext(nil, NoDeadline)
	assert.False(t, AppendGuardrailJudgeCallOnContext(ctx, BifrostGuardrailJudgeCall{}))
	_, ok := GuardrailMetadataFromContext(ctx)
	assert.False(t, ok)
}

func TestLegacyGuardrailDebugAPIsRemainCompatible(t *testing.T) {
	ctx := NewBifrostContext(nil, NoDeadline)
	metadata := &BifrostGuardrailDebug{JudgeCalls: []BifrostGuardrailJudgeCall{{TotalTokens: 1}}}
	require.True(t, SetGuardrailDebugOnContext(ctx, metadata))

	stored, ok := GuardrailDebugFromContext(ctx)
	require.True(t, ok)
	assert.Len(t, stored.JudgeCalls, 1)
}
