package cohere

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Regression tests for Cohere's thinking parameter producing an invalid reasoning effort.
//
// ToBifrostChatRequest mapped Cohere's {"thinking":{"type":"enabled"}} onto
// ChatReasoning{Effort: "auto"}. "auto" is not a value that field accepts - the schema documents
// "none" | "minimal" | "low" | "medium" | "high" | "xhigh" (schemas/chatcompletions.go) - so it
// travelled to the provider verbatim and was rejected:
//
//	openai:  Invalid value: 'auto'. Supported values are: 'low', 'medium', 'high', and 'xhigh'.
//	bedrock: unknown variant `auto`, expected one of `low`, `medium`, `high`, `xhigh`, `max`
//
// Every Cohere request that asked for thinking hit this. It stayed hidden because the harness
// cells sent OpenAI's reasoning_effort to the Cohere endpoint instead of Cohere's own parameter,
// so this path was never exercised (48.x.C reasoning cells).
//
// "thinking on" is expressed by Enabled, and the budget by MaxTokens; effort is left for the
// downstream model-aware mapping to derive rather than invented here.

func bifrostFromCohereThinking(t *testing.T, thinking *CohereThinking) *schemas.ChatReasoning {
	t.Helper()
	req := &CohereChatRequest{
		Model:    "openai/o3-mini",
		Messages: []CohereMessage{{Role: "user", Content: &CohereMessageContent{StringContent: schemas.Ptr("hi")}}},
		Thinking: thinking,
	}
	out := req.ToBifrostChatRequest(nil)
	require.NotNil(t, out)
	require.NotNil(t, out.Params)
	return out.Params.Reasoning
}

func TestToBifrostChatRequest_ThinkingEnabledDoesNotInventAutoEffort(t *testing.T) {
	r := bifrostFromCohereThinking(t, &CohereThinking{
		Type: ThinkingTypeEnabled, TokenBudget: schemas.Ptr(1024),
	})
	require.NotNil(t, r, "thinking must reach Bifrost as a reasoning request")

	if r.Effort != nil {
		assert.NotEqual(t, "auto", *r.Effort,
			"'auto' is not an accepted effort; providers reject it with a 400")
		assert.Contains(t, []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}, *r.Effort)
	}
	require.NotNil(t, r.Enabled, "reasoning must be marked enabled")
	assert.True(t, *r.Enabled)
	require.NotNil(t, r.MaxTokens, "Cohere's token_budget must survive as the reasoning budget")
	assert.Equal(t, 1024, *r.MaxTokens)
}

// Enabled with no budget is still a request to think; it just carries no budget.
func TestToBifrostChatRequest_ThinkingEnabledWithoutBudget(t *testing.T) {
	r := bifrostFromCohereThinking(t, &CohereThinking{Type: ThinkingTypeEnabled})
	require.NotNil(t, r)
	require.NotNil(t, r.Enabled)
	assert.True(t, *r.Enabled)
	assert.Nil(t, r.MaxTokens)
	if r.Effort != nil {
		assert.NotEqual(t, "auto", *r.Effort)
	}
}

// Disabled must stay an explicit disable - that mapping was already correct.
func TestToBifrostChatRequest_ThinkingDisabledStaysNone(t *testing.T) {
	r := bifrostFromCohereThinking(t, &CohereThinking{Type: ThinkingTypeDisabled})
	require.NotNil(t, r)
	require.NotNil(t, r.Effort)
	assert.Equal(t, "none", *r.Effort)
}

// No thinking at all means no reasoning request.
func TestToBifrostChatRequest_NoThinkingLeavesReasoningUnset(t *testing.T) {
	assert.Nil(t, bifrostFromCohereThinking(t, nil))
}
