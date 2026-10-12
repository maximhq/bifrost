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

// TestGuardrailMetadataKeepsDetectionOnlyMetadata verifies detections persist without any judge calls.
func TestGuardrailMetadataKeepsDetectionOnlyMetadata(t *testing.T) {
	ctx := NewBifrostContext(nil, NoDeadline)
	ruleID := uint(7)
	detection := BifrostGuardrailDetection{
		Phase:             "input",
		RuleID:            &ruleID,
		RuleName:          "pii-audit",
		GuardrailName:     "bedrock-detect",
		GuardrailProvider: "bedrock",
		Reason:            "Guardrail detected a policy match",
		Assessments:       []string{"content_policy VIOLENCE"},
	}

	require.True(t, AppendGuardrailDetectionOnContext(ctx, detection))
	metadata, ok := GuardrailMetadataFromContext(ctx)
	require.True(t, ok)
	assert.Empty(t, metadata.JudgeCalls)
	require.Len(t, metadata.Detections, 1)
	assert.Equal(t, detection, metadata.Detections[0])
}

// TestGuardrailMetadataDetectionSnapshotIsOwned verifies nested detection fields cannot alias context state.
func TestGuardrailMetadataDetectionSnapshotIsOwned(t *testing.T) {
	ctx := NewBifrostContext(nil, NoDeadline)
	ruleID := uint(3)
	require.True(t, AppendGuardrailDetectionOnContext(ctx, BifrostGuardrailDetection{
		RuleID:      &ruleID,
		Assessments: []string{"topic_policy weapons"},
	}))

	first, ok := GuardrailMetadataFromContext(ctx)
	require.True(t, ok)
	*first.Detections[0].RuleID = 999
	first.Detections[0].Assessments[0] = "mutated"

	second, ok := GuardrailMetadataFromContext(ctx)
	require.True(t, ok)
	assert.Equal(t, uint(3), *second.Detections[0].RuleID)
	assert.Equal(t, "topic_policy weapons", second.Detections[0].Assessments[0])
}

// TestGuardrailMetadataKeepsJudgeCallsAndDetectionsTogether verifies each append preserves the other list.
func TestGuardrailMetadataKeepsJudgeCallsAndDetectionsTogether(t *testing.T) {
	ctx := NewBifrostContext(nil, NoDeadline)
	require.True(t, AppendGuardrailDetectionOnContext(ctx, BifrostGuardrailDetection{RuleName: "first"}))
	require.True(t, AppendGuardrailJudgeCallOnContext(ctx, BifrostGuardrailJudgeCall{TotalTokens: 5}))
	require.True(t, AppendGuardrailDetectionOnContext(ctx, BifrostGuardrailDetection{RuleName: "second"}))

	metadata, ok := GuardrailMetadataFromContext(ctx)
	require.True(t, ok)
	require.Len(t, metadata.JudgeCalls, 1)
	require.Len(t, metadata.Detections, 2)
	assert.Equal(t, "first", metadata.Detections[0].RuleName)
	assert.Equal(t, "second", metadata.Detections[1].RuleName)
}

// TestGuardrailMetadataEmptyCloneIsNil verifies metadata with neither list stays omitted.
func TestGuardrailMetadataEmptyCloneIsNil(t *testing.T) {
	assert.Nil(t, (&BifrostGuardrailMetadata{}).Clone())
	assert.False(t, SetGuardrailMetadataOnContext(NewBifrostContext(nil, NoDeadline), &BifrostGuardrailMetadata{}))
}
