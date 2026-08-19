package bedrock

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// Opus 4.7 rejects `thinking.type: "enabled"` with a 400, so convertChatParameters
// rewrites every reasoning request for these models to `thinking.type: "adaptive"`.
// See "Extended thinking" -> "Migrating to adaptive thinking":
// https://platform.claude.com/docs/en/build-with-claude/extended-thinking
//
//	"You are moving to Claude Opus 4.7, Claude Opus 4.8, Claude Opus 5, Claude
//	 Sonnet 5, Claude Fable 5, or Claude Mythos 5, where `type: "enabled"`
//	 returns a 400 error."
//
// The rewrite is correct, but it has two arms and only one of them sets
// thinking.display. Without display, an adaptive-only model produces no visible
// thinking blocks, so the caller's reasoning request is silently swallowed.
//
// Caught by harness folder "47.6.2 Reasoning streaming", cell
// 47.6.2.F /genai :generateContent -> bedrock/global.anthropic.claude-opus-4-7.
// A genai request carrying thinkingConfig.thinkingBudget maps to
// Reasoning.MaxTokens, lands on the budget arm, and streams back zero
// {"thought":true} parts. The same request expressed as thinkingLevel maps to
// Reasoning.Effort, lands on the effort arm, and streams thinking normally.
const adaptiveOnlyModel = "global.anthropic.claude-opus-4-7"

// thinkingFields returns the `thinking` object convertChatParameters built, or
// fails the test if reasoning never made it into AdditionalModelRequestFields.
func thinkingFields(t *testing.T, reasoning *schemas.ChatReasoning) map[string]any {
	t.Helper()

	bifrostReq := &schemas.BifrostChatRequest{
		Model:  adaptiveOnlyModel,
		Params: &schemas.ChatParameters{Reasoning: reasoning},
	}
	bedrockReq := &BedrockConverseRequest{}
	if err := convertChatParameters(nil, bifrostReq, bedrockReq); err != nil {
		t.Fatalf("convertChatParameters failed: %v", err)
	}
	if bedrockReq.AdditionalModelRequestFields == nil {
		t.Fatalf("expected AdditionalModelRequestFields to carry the thinking config, got nil")
	}
	raw, ok := bedrockReq.AdditionalModelRequestFields.Get("thinking")
	if !ok {
		t.Fatalf("expected a `thinking` field, got none")
	}
	thinking, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("expected `thinking` to be a map, got %T: %+v", raw, raw)
	}
	if got := thinking["type"]; got != "adaptive" {
		t.Fatalf("expected thinking.type \"adaptive\" on an adaptive-only model, got %v", got)
	}
	return thinking
}

// TestConvertChatParameters_AdaptiveBudgetSetsDisplay pins the bug: the budget
// arm must default display to "summarized" exactly as the effort arm does.
func TestConvertChatParameters_AdaptiveBudgetSetsDisplay(t *testing.T) {
	thinking := thinkingFields(t, &schemas.ChatReasoning{
		MaxTokens: schemas.Ptr(1024),
	})

	if got, ok := thinking["display"]; !ok || got != "summarized" {
		t.Errorf("budget arm dropped thinking.display: want \"summarized\", got %v (present=%v).\n"+
			"Without it the model returns no visible thinking blocks, so a caller-requested\n"+
			"reasoning budget is silently discarded. Full thinking config: %+v",
			got, ok, thinking)
	}
}

// TestConvertChatParameters_AdaptiveEffortSetsDisplay is the control. It passes
// today and documents the behaviour the budget arm is supposed to match; if this
// ever regresses, the two arms are wrong in the same way rather than differently.
func TestConvertChatParameters_AdaptiveEffortSetsDisplay(t *testing.T) {
	thinking := thinkingFields(t, &schemas.ChatReasoning{
		Effort: schemas.Ptr("medium"),
	})

	if got, ok := thinking["display"]; !ok || got != "summarized" {
		t.Errorf("effort arm dropped thinking.display: want \"summarized\", got %v (present=%v)", got, ok)
	}
}

// TestConvertChatParameters_AdaptiveBudgetHonoursExplicitDisplay guards the fix
// against overreach: defaulting display must not clobber a caller who explicitly
// asked for "omitted".
func TestConvertChatParameters_AdaptiveBudgetHonoursExplicitDisplay(t *testing.T) {
	thinking := thinkingFields(t, &schemas.ChatReasoning{
		MaxTokens: schemas.Ptr(1024),
		Display:   schemas.Ptr("omitted"),
	})

	if got := thinking["display"]; got != "omitted" {
		t.Errorf("explicit thinking.display was not honoured: want \"omitted\", got %v", got)
	}
}

// The Responses converter carries its own copy of the same translation, and the
// /genai drop-in routes through it rather than through convertChatParameters —
// GeminiGenerationRequest.convertGenerationConfigToResponsesParameters produces
// ResponsesParameters. That is the path harness cell 47.6.2.F actually exercises,
// so the display default has to hold on both converters or the fix misses the
// live traffic entirely.
//
// ResponsesParametersReasoning has no Display field; this surface expresses the
// same intent through Summary, which is why these cases drive Summary instead.
func responsesThinkingFields(t *testing.T, reasoning *schemas.ResponsesParametersReasoning) map[string]any {
	t.Helper()

	bifrostReq := &schemas.BifrostResponsesRequest{
		Model:  adaptiveOnlyModel,
		Params: &schemas.ResponsesParameters{Reasoning: reasoning},
	}
	bedrockReq, err := ToBedrockResponsesRequest(nil, bifrostReq)
	if err != nil {
		t.Fatalf("ToBedrockResponsesRequest failed: %v", err)
	}
	if bedrockReq.AdditionalModelRequestFields == nil {
		t.Fatalf("expected AdditionalModelRequestFields to carry the thinking config, got nil")
	}
	raw, ok := bedrockReq.AdditionalModelRequestFields.Get("thinking")
	if !ok {
		t.Fatalf("expected a `thinking` field, got none")
	}
	thinking, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("expected `thinking` to be a map, got %T: %+v", raw, raw)
	}
	if got := thinking["type"]; got != "adaptive" {
		t.Fatalf("expected thinking.type \"adaptive\" on an adaptive-only model, got %v", got)
	}
	return thinking
}

// TestToBedrockResponsesRequest_AdaptiveBudgetSetsDisplay is the Responses-side
// twin of TestConvertChatParameters_AdaptiveBudgetSetsDisplay, and the one that
// covers the path harness cell 47.6.2.F takes.
func TestToBedrockResponsesRequest_AdaptiveBudgetSetsDisplay(t *testing.T) {
	thinking := responsesThinkingFields(t, &schemas.ResponsesParametersReasoning{
		MaxTokens: schemas.Ptr(1024),
	})

	if got, ok := thinking["display"]; !ok || got != "summarized" {
		t.Errorf("budget arm dropped thinking.display: want \"summarized\", got %v (present=%v).\n"+
			"This is the converter /genai routes through, so without it cell 47.6.2.F\n"+
			"streams zero {\"thought\":true} parts. Full thinking config: %+v", got, ok, thinking)
	}
}

// TestToBedrockResponsesRequest_AdaptiveEffortSetsDisplay is the control: the
// effort arm of the same converter already defaulted display correctly.
func TestToBedrockResponsesRequest_AdaptiveEffortSetsDisplay(t *testing.T) {
	thinking := responsesThinkingFields(t, &schemas.ResponsesParametersReasoning{
		Effort: schemas.Ptr("medium"),
	})

	if got, ok := thinking["display"]; !ok || got != "summarized" {
		t.Errorf("effort arm dropped thinking.display: want \"summarized\", got %v (present=%v)", got, ok)
	}
}

// TestToBedrockResponsesRequest_AdaptiveBudgetHonoursSummaryNone guards against
// overreach on this surface too: Summary "none" is how a Responses caller asks
// for thinking to stay hidden, and defaulting must not override it.
func TestToBedrockResponsesRequest_AdaptiveBudgetHonoursSummaryNone(t *testing.T) {
	thinking := responsesThinkingFields(t, &schemas.ResponsesParametersReasoning{
		MaxTokens: schemas.Ptr(1024),
		Summary:   schemas.Ptr("none"),
	})

	if got := thinking["display"]; got != "omitted" {
		t.Errorf("summary \"none\" was not honoured: want display \"omitted\", got %v", got)
	}
}

// bedrockReasoningRequest builds the inbound side of the /bedrock drop-in: a
// Converse request carrying reasoning_config, as harness cell 47.6.2.G sends it.
func bedrockReasoningRequest(model string) *BedrockConverseRequest {
	fields := schemas.NewOrderedMap()
	fields.Set("reasoning_config", map[string]any{
		"type":          "enabled",
		"budget_tokens": 1024,
	})
	return &BedrockConverseRequest{
		ModelID:                      model,
		AdditionalModelRequestFields: fields,
		Messages: []BedrockMessage{{
			Role:    "user",
			Content: []BedrockContentBlock{{Text: schemas.Ptr("hi")}},
		}},
	}
}

// Converse has no reasoning-summary field and no reasoning-token field in usage,
// so an OpenAI reasoning model reached through /bedrock is never asked for
// summaries and returns nothing observable — a 200 with the caller's
// reasoning_config silently producing no reasoning at all. Cell
// 47.6.2.G /bedrock converse -> openai/o3-mini caught exactly that: every other
// provider emitted reasoningContent and o3-mini emitted none.
//
// Defaulting Summary mirrors the gemini drop-in, which does the same for the same
// reason in convertGenerationConfigToResponsesParameters.
func TestToBifrostResponsesRequest_DefaultsSummaryForOpenAI(t *testing.T) {
	req := bedrockReasoningRequest("openai/o3-mini")
	bifrostReq, err := req.ToBifrostResponsesRequest(nil)
	if err != nil {
		t.Fatalf("ToBifrostResponsesRequest failed: %v", err)
	}
	if bifrostReq.Params == nil || bifrostReq.Params.Reasoning == nil {
		t.Fatalf("expected reasoning params to survive the conversion, got %+v", bifrostReq.Params)
	}
	summary := bifrostReq.Params.Reasoning.Summary
	if summary == nil {
		t.Fatalf("reasoning.summary was not defaulted for an OpenAI model; without it " +
			"o3-mini returns no reasoning text and Converse has no reasoning-token field " +
			"to fall back on, so the reasoning request vanishes silently")
	}
	if *summary != "auto" {
		t.Errorf("expected reasoning.summary \"auto\", got %q", *summary)
	}
}

// The default must not fire for providers that already return reasoning content
// on their own — those all passed cell 47.6.2.G before this change, and asking
// for summaries there would be a behaviour change with no upside.
func TestToBifrostResponsesRequest_LeavesSummaryUnsetForAnthropic(t *testing.T) {
	req := bedrockReasoningRequest(adaptiveOnlyModel)
	bifrostReq, err := req.ToBifrostResponsesRequest(nil)
	if err != nil {
		t.Fatalf("ToBifrostResponsesRequest failed: %v", err)
	}
	if bifrostReq.Params == nil || bifrostReq.Params.Reasoning == nil {
		t.Fatalf("expected reasoning params to survive the conversion, got %+v", bifrostReq.Params)
	}
	if s := bifrostReq.Params.Reasoning.Summary; s != nil {
		t.Errorf("reasoning.summary should stay unset for a non-OpenAI model, got %q", *s)
	}
}
