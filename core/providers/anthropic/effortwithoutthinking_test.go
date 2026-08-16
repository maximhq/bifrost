package anthropic

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// output_config.effort and thinking are independent controls on the Anthropic
// Messages API. From the effort docs
// (https://platform.claude.com/docs/en/build-with-claude/effort):
//
//	"This approach has two major advantages: 1. It doesn't require thinking to be
//	 enabled. 2. It can affect all token spend including tool calls."
//	"Effort works with or without thinking"
//
// and its canonical Basic usage example sends output_config.effort with no
// thinking parameter at all.
//
// Bifrost read output_config.effort only inside `if req.Thinking != nil`, so an
// effort-only request lost the effort entirely. Synthesizing a thinking parameter
// to compensate is not an option: per the per-model configuration table
// (https://platform.claude.com/docs/en/build-with-claude/thinking-troubleshooting),
// omitting `thinking` resolves to a per-model default that differs by model --
// Opus 5 and Sonnet 5 default thinking On, while Opus 4.6/4.7/4.8 and Sonnet 4.6
// default it Off -- so injecting thinking:{adaptive} would turn thinking on
// against the caller's request on half the family, billing thinking tokens they
// never asked for and breaking their cached prefix.

const (
	effortTestModelOpus5   = "claude-opus-5"
	effortTestModelOpus47  = "claude-opus-4-7"
	effortTestModelOpus45  = "claude-opus-4-5-20251101"
	effortTestUserQuestion = "What is 1+1? Answer in one word."
)

func effortTestCtx(t *testing.T) *schemas.BifrostContext {
	t.Helper()
	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	t.Cleanup(cancel)
	return ctx
}

// effortTestRequest builds the Messages API request from the effort docs' Basic
// usage example, optionally with a thinking parameter.
func effortTestRequest(model string, effort *string, thinking *AnthropicThinking) *AnthropicMessageRequest {
	req := &AnthropicMessageRequest{
		Model:     model,
		MaxTokens: 4096,
		Messages: []AnthropicMessage{{
			Role:    AnthropicMessageRoleUser,
			Content: AnthropicContent{ContentStr: schemas.Ptr(effortTestUserQuestion)},
		}},
		Thinking: thinking,
	}
	if effort != nil {
		req.OutputConfig = &AnthropicOutputConfig{Effort: effort}
	}
	return req
}

// TestAnthropicIngress_EffortSurvivesWithoutThinking is the direct regression for
// the report: output_config.effort alone must reach the neutral request.
func TestAnthropicIngress_EffortSurvivesWithoutThinking(t *testing.T) {
	t.Parallel()

	req := effortTestRequest(effortTestModelOpus5, schemas.Ptr("medium"), nil)
	bifrostReq := req.ToBifrostResponsesRequest(effortTestCtx(t))

	require.NotNil(t, bifrostReq)
	require.NotNil(t, bifrostReq.Params)
	require.NotNil(t, bifrostReq.Params.Reasoning, "effort was dropped because no thinking parameter was sent")
	require.NotNil(t, bifrostReq.Params.Reasoning.Effort)
	assert.Equal(t, "medium", *bifrostReq.Params.Reasoning.Effort)
}

// TestAnthropicRoundTrip_EffortWithoutThinkingDoesNotInventThinking covers the
// half of the fix that matters most: the effort must come out the other side
// without Bifrost adding a thinking parameter the caller never sent. Opus 4.7 is
// the sharp case -- it defaults thinking Off, so an injected thinking:{adaptive}
// silently turns thinking on.
func TestAnthropicRoundTrip_EffortWithoutThinkingDoesNotInventThinking(t *testing.T) {
	t.Parallel()

	for _, model := range []string{effortTestModelOpus5, effortTestModelOpus47, effortTestModelOpus45} {
		t.Run(model, func(t *testing.T) {
			ctx := effortTestCtx(t)

			bifrostReq := effortTestRequest(model, schemas.Ptr("medium"), nil).ToBifrostResponsesRequest(ctx)
			require.NotNil(t, bifrostReq)

			out, err := ToAnthropicResponsesRequest(ctx, bifrostReq)
			require.NoError(t, err)
			require.NotNil(t, out)

			assert.Nil(t, out.Thinking, "caller sent no thinking parameter; Bifrost must not invent one")
			require.NotNil(t, out.OutputConfig, "output_config was dropped")
			require.NotNil(t, out.OutputConfig.Effort)
			assert.Equal(t, "medium", *out.OutputConfig.Effort)
		})
	}
}

// TestAnthropicRoundTrip_ThinkingDisabledKeepsEffort covers the sibling case the
// same conflation caused. Per the docs effort "affects all tokens in the
// response", including text and tool calls, so it remains meaningful with
// thinking off -- and Opus 5 accepts thinking:{disabled} at effort high or below.
// Bifrost collapsed disabled thinking to effort "none", overwriting the caller's
// effort.
func TestAnthropicRoundTrip_ThinkingDisabledKeepsEffort(t *testing.T) {
	t.Parallel()

	ctx := effortTestCtx(t)
	req := effortTestRequest(effortTestModelOpus5, schemas.Ptr("low"), &AnthropicThinking{Type: "disabled"})

	bifrostReq := req.ToBifrostResponsesRequest(ctx)
	require.NotNil(t, bifrostReq)

	out, err := ToAnthropicResponsesRequest(ctx, bifrostReq)
	require.NoError(t, err)
	require.NotNil(t, out)

	require.NotNil(t, out.Thinking, "caller explicitly disabled thinking; that must be preserved")
	assert.Equal(t, "disabled", out.Thinking.Type)
	require.NotNil(t, out.OutputConfig, "effort was discarded when thinking was disabled")
	require.NotNil(t, out.OutputConfig.Effort)
	assert.Equal(t, "low", *out.OutputConfig.Effort)
}

// TestAnthropicRoundTrip_AdaptiveThinkingWithEffortUnchanged pins the path that
// already worked, so the fix above cannot regress it.
func TestAnthropicRoundTrip_AdaptiveThinkingWithEffortUnchanged(t *testing.T) {
	t.Parallel()

	ctx := effortTestCtx(t)
	req := effortTestRequest(effortTestModelOpus5, schemas.Ptr("medium"), &AnthropicThinking{Type: "adaptive"})

	bifrostReq := req.ToBifrostResponsesRequest(ctx)
	require.NotNil(t, bifrostReq)

	out, err := ToAnthropicResponsesRequest(ctx, bifrostReq)
	require.NoError(t, err)
	require.NotNil(t, out)

	require.NotNil(t, out.Thinking)
	assert.Equal(t, "adaptive", out.Thinking.Type)
	require.NotNil(t, out.OutputConfig)
	require.NotNil(t, out.OutputConfig.Effort)
	assert.Equal(t, "medium", *out.OutputConfig.Effort)
}

// TestMapBifrostEffortToAnthropicNeverEmitsAdaptive enforces the docs' explicit
// warning: "Don't pass `adaptive` as an `effort` value: `adaptive` is a thinking
// mode, not an effort level." A caller sending reasoning.effort:"adaptive" on an
// OpenAI-shaped inbound route must not have it forwarded into
// output_config.effort.
func TestMapBifrostEffortToAnthropicNeverEmitsAdaptive(t *testing.T) {
	t.Parallel()

	for _, effort := range []string{"adaptive", "minimal", "low", "medium", "high", "xhigh", "max"} {
		t.Run(effort, func(t *testing.T) {
			assert.NotEqual(t, "adaptive", MapBifrostEffortToAnthropic(effort),
				"adaptive is a thinking mode, not an effort level; it must never reach output_config.effort")
		})
	}
}
