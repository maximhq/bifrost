package schemas

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGuardrailDebugContextRoundTrip verifies typed guardrail debug context storage.
func TestGuardrailDebugContextRoundTrip(t *testing.T) {
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
	debug, ok := GuardrailDebugFromContext(ctx)
	require.True(t, ok)
	require.Len(t, debug.JudgeCalls, 1)
	assert.Equal(t, call, debug.JudgeCalls[0])
}

// TestGuardrailDebugContextReturnsOwnedSnapshot verifies callers cannot mutate context state.
func TestGuardrailDebugContextReturnsOwnedSnapshot(t *testing.T) {
	ctx := NewBifrostContext(nil, NoDeadline)
	require.True(t, AppendGuardrailJudgeCallOnContext(ctx, BifrostGuardrailJudgeCall{
		JudgeProvider: OpenAI,
		JudgeModel:    "gpt-4o-mini",
		TotalTokens:   10,
	}))

	first, ok := GuardrailDebugFromContext(ctx)
	require.True(t, ok)
	first.JudgeCalls[0].TotalTokens = 999

	second, ok := GuardrailDebugFromContext(ctx)
	require.True(t, ok)
	assert.Equal(t, 10, second.JudgeCalls[0].TotalTokens)
}

// TestAppendGuardrailJudgeCallRejectsEmptyUsage verifies non-billable calls are omitted.
func TestAppendGuardrailJudgeCallRejectsEmptyUsage(t *testing.T) {
	ctx := NewBifrostContext(nil, NoDeadline)
	assert.False(t, AppendGuardrailJudgeCallOnContext(ctx, BifrostGuardrailJudgeCall{}))
	_, ok := GuardrailDebugFromContext(ctx)
	assert.False(t, ok)
}
