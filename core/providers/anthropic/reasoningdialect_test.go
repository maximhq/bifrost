package anthropic

import (
	"context"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Regression tests for reasoning dropped on non-OpenAI inbound dialects.
//
// The harness runs one reasoning request through seven inbound dialects against the same backend.
// Every OpenAI-shaped dialect reached the model as a thinking request; the others did not:
//
//	47.6.2.A native /v1/chat        reasoning_effort            pass
//	47.6.2.B native /v1/responses   reasoning.effort            pass
//	47.6.2.C /openai/v1/chat        reasoning_effort            pass
//	47.6.2.D /openai/v1/responses   reasoning.effort            pass
//	47.6.2.E /anthropic/v1/messages thinking.budget_tokens      FAIL against anthropic-family
//	47.6.2.F /genai                 thinkingConfig              FAIL
//	47.6.2.G /bedrock converse      reasoning_config            FAIL
//
// The streams proved it rather than implied it: the first content block came back
// {"type":"text"} with no thinking block at all, so the model was never asked to reason.
//
// These tests exercise ToAnthropicChatRequest directly with the unified shapes those dialects
// produce. A budget-only request is the interesting one: the OpenAI dialects send an effort, while
// the GenAI and Bedrock dialects arrive carrying max_tokens.

const opus47 = "claude-opus-4-7"

func testCtx() *schemas.BifrostContext {
	return schemas.NewBifrostContext(context.Background(), time.Time{})
}

func chatReq(model string, reasoning *schemas.ChatReasoning, maxTokens *int) *schemas.BifrostChatRequest {
	return &schemas.BifrostChatRequest{
		Provider: schemas.Anthropic,
		Model:    model,
		Input: []schemas.ChatMessage{{
			Role:    schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("What is 17 * 23?")},
		}},
		Params: &schemas.ChatParameters{
			Reasoning:           reasoning,
			MaxCompletionTokens: maxTokens,
		},
	}
}

// The OpenAI-shaped dialects. These already worked; they are here so a fix to the budget path
// cannot silently regress the path that was fine.
func TestToAnthropicChatRequest_EffortEnablesThinking(t *testing.T) {
	req, err := ToAnthropicChatRequest(testCtx(), chatReq(opus47,
		&schemas.ChatReasoning{Effort: schemas.Ptr("low")}, schemas.Ptr(2048)))
	require.NoError(t, err)
	require.NotNil(t, req)
	require.NotNil(t, req.Thinking, "effort must reach the model as a thinking request")
	// NotEmpty would accept "disabled", which is precisely the regression these tests exist to
	// catch: the converter can return a Thinking block that turns thinking OFF and still pass.
	// opus47 has adaptive as its only thinking-on mode, so name it.
	assert.Equal(t, "adaptive", req.Thinking.Type)
	require.NotNil(t, req.Thinking.Display, "an enabled thinking block must carry a display mode")
	assert.Equal(t, "summarized", *req.Thinking.Display)
}

// The GenAI and Bedrock dialects land here: a budget, no effort.
func TestToAnthropicChatRequest_BudgetOnlyEnablesThinking(t *testing.T) {
	req, err := ToAnthropicChatRequest(testCtx(), chatReq(opus47,
		&schemas.ChatReasoning{MaxTokens: schemas.Ptr(1024)}, schemas.Ptr(2048)))
	require.NoError(t, err)
	require.NotNil(t, req)
	require.NotNil(t, req.Thinking,
		"a budget-only reasoning request must still enable thinking - this is the /genai and /bedrock path")
	// NotEmpty would accept "disabled", which is precisely the regression these tests exist to
	// catch: the converter can return a Thinking block that turns thinking OFF and still pass.
	// opus47 has adaptive as its only thinking-on mode, so name it.
	assert.Equal(t, "adaptive", req.Thinking.Type)
	require.NotNil(t, req.Thinking.Display, "an enabled thinking block must carry a display mode")
	assert.Equal(t, "summarized", *req.Thinking.Display)
}

// Both supplied together, which is what the GenAI inbound conversion actually emits: it derives an
// effort alongside the budget for compatibility (gemini/utils.go).
func TestToAnthropicChatRequest_BudgetAndEffortEnablesThinking(t *testing.T) {
	req, err := ToAnthropicChatRequest(testCtx(), chatReq(opus47,
		&schemas.ChatReasoning{MaxTokens: schemas.Ptr(1024), Effort: schemas.Ptr("low")}, schemas.Ptr(2048)))
	require.NoError(t, err)
	require.NotNil(t, req)
	require.NotNil(t, req.Thinking)
	// NotEmpty would accept "disabled", which is precisely the regression these tests exist to
	// catch: the converter can return a Thinking block that turns thinking OFF and still pass.
	// opus47 has adaptive as its only thinking-on mode, so name it.
	assert.Equal(t, "adaptive", req.Thinking.Type)
	require.NotNil(t, req.Thinking.Display, "an enabled thinking block must carry a display mode")
	assert.Equal(t, "summarized", *req.Thinking.Display)
}

// No reasoning asked for means no thinking sent - the fix must not turn every request into a
// thinking request.
func TestToAnthropicChatRequest_NoReasoningLeavesThinkingUnset(t *testing.T) {
	req, err := ToAnthropicChatRequest(testCtx(), chatReq(opus47, nil, schemas.Ptr(2048)))
	require.NoError(t, err)
	require.NotNil(t, req)
	assert.Nil(t, req.Thinking)
}

// Explicitly disabled is sent as an explicit disable, not as an absent field. That distinction
// matters on models where thinking is otherwise on by default: omitting the field would let the
// model reason anyway, so "none" has to be stated rather than implied.
func TestToAnthropicChatRequest_EffortNoneDisablesThinkingExplicitly(t *testing.T) {
	req, err := ToAnthropicChatRequest(testCtx(), chatReq(opus47,
		&schemas.ChatReasoning{Effort: schemas.Ptr("none")}, schemas.Ptr(2048)))
	require.NoError(t, err)
	require.NotNil(t, req)
	require.NotNil(t, req.Thinking)
	assert.Equal(t, "disabled", req.Thinking.Type)
}
