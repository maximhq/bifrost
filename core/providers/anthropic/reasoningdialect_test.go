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

// thinking:{type:"between_tools"} is a thinking type, not an effort level: the
// caller's effort must reach output_config unchanged alongside it. Sonnet 5.5
// accepts it and rejects "disabled"; other models get the nearest setting so a
// fallback off Sonnet 5.5 does not 400.
func TestToAnthropicChatRequest_BetweenToolsThinking(t *testing.T) {
	cases := []struct {
		name       string
		model      string
		reasoning  *schemas.ChatReasoning
		wantType   string // "" means thinking omitted
		wantEffort string // "" means no output_config.effort
	}{
		{"sonnet55_with_effort", "claude-sonnet-5-5", &schemas.ChatReasoning{Type: schemas.Ptr("between_tools"), Effort: schemas.Ptr("medium")}, "between_tools", "medium"},
		{"sonnet55_without_effort", "claude-sonnet-5-5", &schemas.ChatReasoning{Type: schemas.Ptr("between_tools")}, "between_tools", ""},
		// No gateway-side validation: upstream owns the xhigh/max rejection.
		{"sonnet55_xhigh_forwarded", "claude-sonnet-5-5", &schemas.ChatReasoning{Type: schemas.Ptr("between_tools"), Effort: schemas.Ptr("xhigh")}, "between_tools", "xhigh"},
		{"sonnet55_type_wins_over_budget", "claude-sonnet-5-5", &schemas.ChatReasoning{Type: schemas.Ptr("between_tools"), MaxTokens: schemas.Ptr(2048)}, "between_tools", ""},
		{"sonnet5_falls_back_to_disabled", "claude-sonnet-5", &schemas.ChatReasoning{Type: schemas.Ptr("between_tools"), Effort: schemas.Ptr("medium")}, "disabled", "medium"},
		{"opus55_always_on_omits_thinking", "claude-opus-5-5", &schemas.ChatReasoning{Type: schemas.Ptr("between_tools"), Effort: schemas.Ptr("medium")}, "", "medium"},
		// Opus 5 accepts "disabled" only at effort high or below.
		{"opus5_disabled_at_medium", "claude-opus-5", &schemas.ChatReasoning{Type: schemas.Ptr("between_tools"), Effort: schemas.Ptr("medium")}, "disabled", "medium"},
		{"opus5_omits_thinking_at_xhigh", "claude-opus-5", &schemas.ChatReasoning{Type: schemas.Ptr("between_tools"), Effort: schemas.Ptr("xhigh")}, "", "xhigh"},
		{"haiku45_disabled_without_effort", "claude-haiku-4-5", &schemas.ChatReasoning{Type: schemas.Ptr("between_tools"), Effort: schemas.Ptr("medium")}, "disabled", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, err := ToAnthropicChatRequest(testCtx(), chatReq(tc.model, tc.reasoning, schemas.Ptr(2048)))
			require.NoError(t, err)
			require.NotNil(t, req)
			if tc.wantType == "" {
				assert.Nil(t, req.Thinking)
			} else {
				require.NotNil(t, req.Thinking)
				assert.Equal(t, tc.wantType, req.Thinking.Type)
				assert.Nil(t, req.Thinking.BudgetTokens)
				assert.Nil(t, req.Thinking.Display, "between_tools and disabled reject display")
			}
			if tc.wantEffort == "" {
				assert.True(t, req.OutputConfig == nil || req.OutputConfig.Effort == nil, "unexpected effort %+v", req.OutputConfig)
			} else {
				require.NotNil(t, req.OutputConfig)
				require.NotNil(t, req.OutputConfig.Effort)
				assert.Equal(t, tc.wantEffort, *req.OutputConfig.Effort)
			}
		})
	}
}

// A display the caller set must not ride along: between_tools returns a 400 with it.
func TestToAnthropicChatRequest_BetweenToolsDropsDisplay(t *testing.T) {
	req, err := ToAnthropicChatRequest(testCtx(), chatReq("claude-sonnet-5-5",
		&schemas.ChatReasoning{Type: schemas.Ptr("between_tools"), Display: schemas.Ptr("summarized")}, schemas.Ptr(2048)))
	require.NoError(t, err)
	require.NotNil(t, req.Thinking)
	assert.Equal(t, "between_tools", req.Thinking.Type)
	assert.Nil(t, req.Thinking.Display)
}

// Plain effort "none" on Sonnet 5.5 keeps the always-on behaviour (thinking
// omitted): between_tools is only sent when the caller asks for the type.
func TestToAnthropicChatRequest_EffortNoneOnSonnet55OmitsThinking(t *testing.T) {
	req, err := ToAnthropicChatRequest(testCtx(), chatReq("claude-sonnet-5-5",
		&schemas.ChatReasoning{Effort: schemas.Ptr("none")}, schemas.Ptr(2048)))
	require.NoError(t, err)
	assert.Nil(t, req.Thinking, "Sonnet 5.5 rejects thinking:{type:\"disabled\"}")
}

// Anthropic's spelling on the unified route (thinking in extra params) was
// silently dropped for between_tools, letting the model run full adaptive thinking.
func TestToAnthropicChatRequest_BetweenToolsPromotedFromExtraParams(t *testing.T) {
	bifrostReq := chatReq("claude-sonnet-5-5", nil, schemas.Ptr(2048))
	bifrostReq.Params.ExtraParams = map[string]interface{}{
		"thinking": map[string]interface{}{"type": "between_tools"},
	}
	req, err := ToAnthropicChatRequest(testCtx(), bifrostReq)
	require.NoError(t, err)
	require.NotNil(t, req.Thinking)
	assert.Equal(t, "between_tools", req.Thinking.Type)
}

// /anthropic/v1/messages: between_tools collapsed into effort "none", which on
// Sonnet 5.5 omitted thinking (full adaptive) and discarded the caller's effort.
func TestAnthropicRoundTrip_BetweenToolsKeepsTypeAndEffort(t *testing.T) {
	ctx := testCtx()
	in := effortTestRequest("claude-sonnet-5-5", schemas.Ptr("medium"), &AnthropicThinking{Type: "between_tools"})

	bifrostReq := in.ToBifrostResponsesRequest(ctx)
	require.NotNil(t, bifrostReq)
	bifrostReq.Provider = schemas.Anthropic

	out, err := ToAnthropicResponsesRequest(ctx, bifrostReq)
	require.NoError(t, err)
	require.NotNil(t, out.Thinking, "between_tools was dropped; the model runs full adaptive thinking")
	assert.Equal(t, "between_tools", out.Thinking.Type)
	assert.Nil(t, out.Thinking.Display)
	require.NotNil(t, out.OutputConfig)
	require.NotNil(t, out.OutputConfig.Effort)
	assert.Equal(t, "medium", *out.OutputConfig.Effort)
}

// Same inbound request falling back to Sonnet 5, which predates between_tools.
func TestAnthropicRoundTrip_BetweenToolsFallsBackToDisabled(t *testing.T) {
	ctx := testCtx()
	in := effortTestRequest("claude-sonnet-5", schemas.Ptr("medium"), &AnthropicThinking{Type: "between_tools"})

	bifrostReq := in.ToBifrostResponsesRequest(ctx)
	require.NotNil(t, bifrostReq)
	bifrostReq.Provider = schemas.Anthropic

	out, err := ToAnthropicResponsesRequest(ctx, bifrostReq)
	require.NoError(t, err)
	require.NotNil(t, out.Thinking)
	assert.Equal(t, "disabled", out.Thinking.Type)
	require.NotNil(t, out.OutputConfig)
	require.NotNil(t, out.OutputConfig.Effort)
	assert.Equal(t, "medium", *out.OutputConfig.Effort)
}

// Non-Claude models behind the Anthropic-shaped converter (DeepSeek, Fireworks,
// vLLM, SGL) ignore reasoning.type: the caller's effort is honoured as usual.
func TestToAnthropicChatRequest_BetweenToolsIgnoredForNonClaudeModels(t *testing.T) {
	bifrostReq := chatReq("deepseek-v4-pro", &schemas.ChatReasoning{Type: schemas.Ptr("between_tools"), Effort: schemas.Ptr("medium")}, schemas.Ptr(4096))
	bifrostReq.Provider = schemas.DeepSeek
	req, err := ToAnthropicChatRequest(testCtx(), bifrostReq)
	require.NoError(t, err)
	require.NotNil(t, req.Thinking, "effort medium must still turn thinking on")
	assert.NotEqual(t, "between_tools", req.Thinking.Type)
	assert.NotEqual(t, "disabled", req.Thinking.Type)
}
