package anthropic

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/providers/openai"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Regression coverage for output_config.effort on non-Anthropic
// Anthropic-compatible mounts, verified against vendor docs 2026-08-16:
//
//   - z.ai Coding Plan Anthropic mount (schemas.Zhipu): output_config.effort is a
//     documented extension for GLM-5.2+; out-of-scale values are mapped
//     server-side (GLM-5.3: none/minimal/low→low, medium/high→high,
//     xhigh/max→max). thinking.type:"disabled" 400s on GLM-5.3+ and is ignored
//     on forced-thinking GLM-4.7, so the field is omitted instead.
//     Source: https://docs.z.ai/guides/capabilities/thinking
//   - Alibaba Cloud Model Studio /apps/anthropic (schemas.Alibaba):
//     output_config.effort documented for qwen3.8-max, hosted glm-5.2 and
//     deepseek-v4-pro/flash. The mount does NOT map out-of-enum values
//     server-side: it validates the field against the per-model
//     reasoning_effort enum and 400s on anything else ("'reasoning_effort'
//     must be one of: ...", verified live 2026-08-23), so the gateway emits
//     only each family's officially valid values (see
//     clampAlibabaMountEffortForModel for the per-model matrix; vendor API
//     docs supplied 2026-08-23). The mount also rejects effort and
//     thinking_budget set together and engages thinking on its own from the
//     effort value, so a forwarded effort travels alone (no synthesized
//     thinking field).
//     Source: https://www.alibabacloud.com/help/en/model-studio/anthropic-api-messages
//   - Kimi /anthropic (schemas.Kimi): unofficial empirical contract
//     (MoonshotAI/Kimi-K2#129) — no effort equivalent documented, so effort
//     keeps being stripped rather than forwarded as an unknown field.
//
// The reported bug: an Anthropic-protocol client sending
// {"output_config":{"effort":"max"}} with no thinking parameter had the effort
// silently discarded on the zhipu mount because every gate was keyed on
// Anthropic's own model list.

func providerEffortTestCtx(t *testing.T) *schemas.BifrostContext {
	t.Helper()
	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	t.Cleanup(cancel)
	return ctx
}

// anthropicInbound builds the Messages API shape an Anthropic-protocol client
// (opencode / Claude Code style) sends, then normalizes it the way the inbound
// integration does.
func anthropicInbound(model string, effort *string, thinking *AnthropicThinking) *AnthropicMessageRequest {
	req := &AnthropicMessageRequest{
		Model:     model,
		MaxTokens: 128000,
		Messages: []AnthropicMessage{{
			Role:    AnthropicMessageRoleUser,
			Content: AnthropicContent{ContentStr: schemas.Ptr("What is 1+1?")},
		}},
		Thinking: thinking,
	}
	if effort != nil {
		req.OutputConfig = &AnthropicOutputConfig{Effort: effort}
	}
	return req
}

func TestSupportsProviderEffort(t *testing.T) {
	t.Parallel()

	cases := []struct {
		provider schemas.ModelProvider
		model    string
		want     bool
	}{
		// Zhipu: GLM-5.2+ only (z.ai documents reasoning_effort for "GLM-5.2 and above").
		{schemas.Zhipu, "glm-5.3", true},
		{schemas.Zhipu, "GLM-5.3", true},
		{schemas.Zhipu, "glm-5.2", true},
		{schemas.Zhipu, "glm-5.2[1m]", true},
		{schemas.Zhipu, "glm-5", false},
		{schemas.Zhipu, "glm-5.1", false},
		{schemas.Zhipu, "glm-4.7", false},
		{schemas.Zhipu, "glm-4.6v", false},
		// Alibaba: qwen3.8-max / hosted glm-5.2+ / deepseek-v4 only.
		{schemas.Alibaba, "qwen3.8-max", true},
		{schemas.Alibaba, "qwen3.8-max-preview", true},
		{schemas.Alibaba, "glm-5.2", true},
		{schemas.Alibaba, "deepseek-v4-pro", true},
		{schemas.Alibaba, "deepseek-v4-flash", true},
		{schemas.Alibaba, "deepseek-v4-flash-0731", true},
		{schemas.Alibaba, "qwen3.7-plus", false},
		{schemas.Alibaba, "qwen3-max", false},
		{schemas.Alibaba, "glm-5.1", false},
		{schemas.Alibaba, "MiniMax-M2.5", false},
		// Kimi: no effort equivalent on the /anthropic mount.
		{schemas.Kimi, "kimi-k3", false},
		{schemas.Kimi, "kimi-k2.6", false},
		// Anthropic falls back to the model gate unchanged.
		{schemas.Anthropic, "claude-opus-4-6", true},
		{schemas.Anthropic, "claude-sonnet-4-5", false},
		{schemas.Anthropic, "claude-haiku-4-5", false},
		// Unknown provider: model gate only (safe default, unchanged behavior).
		{schemas.ModelProvider("unknown-vendor"), "claude-opus-4-6", true},
		{schemas.ModelProvider("unknown-vendor"), "glm-5.3", false},
	}

	for _, tc := range cases {
		t.Run(string(tc.provider)+"/"+tc.model, func(t *testing.T) {
			assert.Equal(t, tc.want, SupportsProviderEffort(tc.provider, tc.model),
				"SupportsProviderEffort(%s, %s)", tc.provider, tc.model)
		})
	}
}

func TestZhipuForcedThinkingModel(t *testing.T) {
	t.Parallel()

	cases := []struct {
		model string
		want  bool
	}{
		{"glm-5.3", true},
		{"GLM-5.3", true},
		{"glm-5.3[1m]", true},
		{"glm-5.4", true},
		{"glm-4.7", true},
		{"glm-4.7-flash", true},
		{"glm-4.5v", true},
		{"glm-5.2", false},
		{"glm-5.2[1m]", false},
		{"glm-5", false},
		{"glm-4.6", false},
		{"qwen3.8-max", false},
	}
	for _, tc := range cases {
		t.Run(tc.model, func(t *testing.T) {
			assert.Equal(t, tc.want, ZhipuForcedThinkingModel(tc.model))
		})
	}
}

func TestZhipuRequiresThinkingModel(t *testing.T) {
	t.Parallel()

	cases := []struct {
		model string
		want  bool
	}{
		{"glm-5.3", true},
		{"glm-5.3[1m]", true},
		{"glm-5.4", true},
		// GLM-4.7 tolerates an absent thinking field (Claude Code's default
		// traffic against the mount carries none), so it is NOT here.
		{"glm-4.7", false},
		{"glm-4.5v", false},
		{"glm-5.2", false},
		{"glm-5", false},
	}
	for _, tc := range cases {
		t.Run(tc.model, func(t *testing.T) {
			assert.Equal(t, tc.want, ZhipuRequiresThinkingModel(tc.model))
		})
	}
}

// toAnthropicResponsesBuilt mirrors the builder flow: convert, then run the
// strip pass the builder applies (ToAnthropicResponsesRequest does not strip
// internally; BuildAnthropicResponsesRequestBody does).
func toAnthropicResponsesBuilt(t *testing.T, ctx *schemas.BifrostContext, bifrostReq *schemas.BifrostResponsesRequest) *AnthropicMessageRequest {
	t.Helper()
	out, err := ToAnthropicResponsesRequest(ctx, bifrostReq)
	require.NoError(t, err)
	require.NotNil(t, out)
	stripUnsupportedAnthropicFields(out, bifrostReq.Provider, out.Model)
	return out
}

// TestZhipuAnthropicMount_EffortOnlyRoundTrip is the direct regression for the
// reported bug: an Anthropic-protocol client sending output_config.effort with
// no thinking parameter must see the effort reach the z.ai upstream verbatim.
// GLM-5.3 also requires the thinking field outright on this mount (absent =
// disabled = 1210 error, verified live 2026-08-16), so a thinking:{enabled} is
// synthesized with the budget derived from the caller's effort.
func TestZhipuAnthropicMount_EffortOnlyRoundTrip(t *testing.T) {
	t.Parallel()

	for _, effort := range []string{"max", "high", "low"} {
		t.Run(effort, func(t *testing.T) {
			ctx := providerEffortTestCtx(t)
			bifrostReq := anthropicInbound("glm-5.3", schemas.Ptr(effort), nil).ToBifrostResponsesRequest(ctx)
			require.NotNil(t, bifrostReq)
			bifrostReq.Provider = schemas.Zhipu

			out := toAnthropicResponsesBuilt(t, ctx, bifrostReq)

			require.NotNil(t, out.OutputConfig, "output_config was dropped on the zhipu mount")
			require.NotNil(t, out.OutputConfig.Effort)
			assert.Equal(t, effort, *out.OutputConfig.Effort, "effort must reach z.ai verbatim (Coding Plan maps server-side)")
			require.NotNil(t, out.Thinking, "GLM-5.3 rejects requests with no thinking field on this mount")
			assert.Equal(t, "enabled", out.Thinking.Type)
			require.NotNil(t, out.Thinking.BudgetTokens)
			assert.Positive(t, *out.Thinking.BudgetTokens)
		})
	}
}

// TestZhipuAnthropicMount_CoPresentBudgetAndEffortRoundTrip pins the ZCode-proven
// wire shape: thinking{budget_tokens} and output_config{effort} coexist.
func TestZhipuAnthropicMount_CoPresentBudgetAndEffortRoundTrip(t *testing.T) {
	t.Parallel()

	ctx := providerEffortTestCtx(t)
	budget := 32000
	bifrostReq := anthropicInbound("glm-5.3", schemas.Ptr("max"), &AnthropicThinking{
		Type:         "enabled",
		BudgetTokens: &budget,
	}).ToBifrostResponsesRequest(ctx)
	require.NotNil(t, bifrostReq)
	bifrostReq.Provider = schemas.Zhipu

	out := toAnthropicResponsesBuilt(t, ctx, bifrostReq)

	require.NotNil(t, out.Thinking)
	assert.Equal(t, "enabled", out.Thinking.Type)
	require.NotNil(t, out.Thinking.BudgetTokens)
	assert.Equal(t, 32000, *out.Thinking.BudgetTokens)
	require.NotNil(t, out.OutputConfig)
	require.NotNil(t, out.OutputConfig.Effort)
	assert.Equal(t, "max", *out.OutputConfig.Effort)
}

// TestZhipuAnthropicMount_NoReasoningSignalSynthesizesThinking covers a plain
// request (no effort, no thinking) against GLM-5.3: the mount treats an absent
// thinking field as disabled and 400s, so the strip pass synthesizes
// thinking:{enabled} with the minimum budget.
func TestZhipuAnthropicMount_NoReasoningSignalSynthesizesThinking(t *testing.T) {
	t.Parallel()

	ctx := providerEffortTestCtx(t)
	bifrostReq := anthropicInbound("glm-5.3", nil, nil).ToBifrostResponsesRequest(ctx)
	require.NotNil(t, bifrostReq)
	bifrostReq.Provider = schemas.Zhipu

	out := toAnthropicResponsesBuilt(t, ctx, bifrostReq)

	require.NotNil(t, out.Thinking, "GLM-5.3 requires an explicit thinking field on z.ai's mount")
	assert.Equal(t, "enabled", out.Thinking.Type)
	require.NotNil(t, out.Thinking.BudgetTokens)
	assert.Equal(t, MinimumReasoningMaxTokens, *out.Thinking.BudgetTokens)
}

// TestZhipuAnthropicMount_EffortDroppedBelowGLM52 pins the documented model
// scope: GLM-5/5.1/4.7 do not accept effort, so it is stripped (budget thinking
// still carries the intent) rather than risking an upstream 400.
func TestZhipuAnthropicMount_EffortDroppedBelowGLM52(t *testing.T) {
	t.Parallel()

	for _, model := range []string{"glm-5", "glm-5.1", "glm-4.7"} {
		t.Run(model, func(t *testing.T) {
			ctx := providerEffortTestCtx(t)
			bifrostReq := anthropicInbound(model, schemas.Ptr("max"), nil).ToBifrostResponsesRequest(ctx)
			bifrostReq.Provider = schemas.Zhipu

			out := toAnthropicResponsesBuilt(t, ctx, bifrostReq)

			if out.OutputConfig != nil {
				assert.Nil(t, out.OutputConfig.Effort, "%s does not accept output_config.effort", model)
			}
			// GLM-4.7 tolerates an absent thinking field, and glm-5/5.1 allow
			// disabled — none of them require a synthesized thinking field.
			assert.Nil(t, out.Thinking)
		})
	}
}

// TestAlibabaAnthropicMount_EffortRoundTrip covers the Model Studio families
// with documented effort support. The mount validates the value against its
// per-model reasoning_effort enum instead of mapping it server-side (verified
// live 2026-08-23: a forwarded "max" drew "'reasoning_effort' must be one of:
// ..."), so the gateway emits only each family's officially valid values
// (vendor API docs supplied 2026-08-23; see clampAlibabaMountEffortForModel):
//
//   - qwen3.8-max: valid xhigh/medium/low; max clamps to xhigh.
//   - glm-5.2/5.1/5 and non-dated deepseek-v4-pro/flash: valid high/max;
//     xhigh→max, low/medium→high, minimal/none→low.
//   - glm-5.3+: valid max/high/low; xhigh→max, medium→high, minimal/none→low.
//   - deepseek-v4-pro-0813 / deepseek-v4-flash-0731: valid max/high/low;
//     xhigh/medium→high, minimal/none→low.
func TestAlibabaAnthropicMount_EffortRoundTrip(t *testing.T) {
	t.Parallel()

	supported := []struct{ model, effort, want string }{
		{"qwen3.8-max", "xhigh", "xhigh"},
		{"qwen3.8-max", "high", "high"},
		{"qwen3.8-max", "medium", "medium"},
		{"qwen3.8-max", "low", "low"},
		{"qwen3.8-max", "max", "xhigh"}, // out of the mount enum; gateway clamps to the top tier
		{"glm-5.2", "max", "max"},
		{"glm-5.2", "xhigh", "max"},
		{"glm-5.2", "high", "high"},
		{"glm-5.2", "low", "high"},
		{"glm-5.2", "medium", "high"},
		// "minimal" is covered in TestClampAlibabaMountEffortForModel: the
		// conversion pre-maps minimal→low (MapBifrostEffortToAnthropic) before
		// the clamp runs, so on this wire path it lands on "high" — the
		// clamp's own minimal→low rule applies to raw/native bodies.
		{"glm-5.3", "max", "max"},
		{"glm-5.3", "high", "high"},
		{"glm-5.3", "low", "low"},
		{"glm-5.3", "xhigh", "max"},
		{"glm-5.3", "medium", "high"},
		{"glm-5.3", "none", "low"},
		{"deepseek-v4-pro", "max", "max"},
		{"deepseek-v4-pro", "xhigh", "max"},
		{"deepseek-v4-pro-0813", "max", "max"},
		{"deepseek-v4-pro-0813", "xhigh", "high"},
		{"deepseek-v4-flash-0731", "medium", "high"},
		// Case variants: provider-prefixed / mixed-case model strings.
		{"ZHIPU/GLM-5.3", "xhigh", "max"},
		{"alibaba/glm-5.2", "max", "max"},
	}
	for _, tc := range supported {
		t.Run(tc.model+"/"+tc.effort, func(t *testing.T) {
			ctx := providerEffortTestCtx(t)
			bifrostReq := anthropicInbound(tc.model, schemas.Ptr(tc.effort), nil).ToBifrostResponsesRequest(ctx)
			bifrostReq.Provider = schemas.Alibaba

			out := toAnthropicResponsesBuilt(t, ctx, bifrostReq)

			require.NotNil(t, out.OutputConfig, "output_config was dropped on the alibaba mount")
			require.NotNil(t, out.OutputConfig.Effort)
			assert.Equal(t, tc.want, *out.OutputConfig.Effort)
			// Alibaba treats an absent thinking field as the model default and
			// rejects effort + thinking_budget together, so nothing is
			// synthesized alongside a forwarded effort.
			assert.Nil(t, out.Thinking)
		})
	}

	// Families without documented effort support keep the strip behavior.
	for _, model := range []string{"qwen3.7-plus", "qwen3-max", "glm-5.1"} {
		t.Run(model+"-stripped", func(t *testing.T) {
			ctx := providerEffortTestCtx(t)
			bifrostReq := anthropicInbound(model, schemas.Ptr("max"), nil).ToBifrostResponsesRequest(ctx)
			bifrostReq.Provider = schemas.Alibaba

			out := toAnthropicResponsesBuilt(t, ctx, bifrostReq)

			if out.OutputConfig != nil {
				assert.Nil(t, out.OutputConfig.Effort, "%s has no documented effort support on Model Studio", model)
			}
		})
	}
}

// TestClampAlibabaMountEffortForModel pins the full per-model clamp matrix on
// the alibaba Anthropic mount directly (vendor API docs supplied 2026-08-23).
// Every output is an officially valid value for the model family — the mount
// validates against its per-model enum instead of mapping server-side.
func TestClampAlibabaMountEffortForModel(t *testing.T) {
	t.Parallel()

	cases := []struct {
		model  string
		effort string
		want   string
	}{
		// qwen3.8-max: max→xhigh, everything else verbatim (unchanged).
		{"qwen3.8-max", "max", "xhigh"},
		{"qwen3.8-max", "xhigh", "xhigh"},
		{"qwen3.8-max", "high", "high"},
		{"qwen3.8-max", "low", "low"},
		// glm-5.2/5.1: valid high/max; xhigh→max, low/medium→high, minimal/none→low.
		{"glm-5.2", "max", "max"},
		{"glm-5.2", "xhigh", "max"},
		{"glm-5.2", "high", "high"},
		{"glm-5.2", "low", "high"},
		{"glm-5.2", "medium", "high"},
		{"glm-5.2", "minimal", "low"},
		{"glm-5.2", "none", "low"},
		{"glm-5.1", "max", "max"},
		{"glm-5.1", "xhigh", "max"},
		// glm-5.3+: valid max/high/low; xhigh→max, medium→high, minimal/none→low.
		{"glm-5.3", "max", "max"},
		{"glm-5.3", "high", "high"},
		{"glm-5.3", "low", "low"},
		{"glm-5.3", "xhigh", "max"},
		{"glm-5.3", "medium", "high"},
		{"glm-5.3", "minimal", "low"},
		{"glm-5.3", "none", "low"},
		// Non-dated deepseek-v4: same family as glm-5.2 (valid high/max).
		{"deepseek-v4-pro", "max", "max"},
		{"deepseek-v4-pro", "xhigh", "max"},
		{"deepseek-v4-pro", "low", "high"},
		{"deepseek-v4-flash", "medium", "high"},
		// Dated snapshots: valid max/high/low; xhigh/medium→high, minimal/none→low.
		{"deepseek-v4-pro-0813", "max", "max"},
		{"deepseek-v4-pro-0813", "high", "high"},
		{"deepseek-v4-pro-0813", "low", "low"},
		{"deepseek-v4-pro-0813", "xhigh", "high"},
		{"deepseek-v4-pro-0813", "medium", "high"},
		{"deepseek-v4-pro-0813", "minimal", "low"},
		{"deepseek-v4-flash-0731", "max", "max"},
		{"deepseek-v4-flash-0731", "xhigh", "high"},
		{"deepseek-v4-flash-0731", "medium", "high"},
		{"deepseek-v4-flash-0731", "none", "low"},
		// Case variants: provider-prefixed / mixed-case model strings.
		{"ZHIPU/GLM-5.3", "xhigh", "max"},
		{"ZHIPU/GLM-5.3", "medium", "high"},
		{"alibaba/glm-5.2", "max", "max"},
		{"alibaba/glm-5.2", "low", "high"},
		// Anything else verbatim — kimi-k3 never reaches the clamp (its effort
		// is stripped upstream by the provider effort gate).
		{"kimi-k3", "max", "max"},
		{"qwen3.7-plus", "max", "max"},
		{"MiniMax-M2.5", "xhigh", "xhigh"},
	}
	for _, tc := range cases {
		t.Run(tc.model+"/"+tc.effort, func(t *testing.T) {
			assert.Equal(t, tc.want, clampAlibabaMountEffortForModel(tc.model, tc.effort),
				"clampAlibabaMountEffortForModel(%s, %s)", tc.model, tc.effort)
		})
	}

	// The provider gate: only the alibaba profile clamps; zhipu forwards
	// verbatim (Coding Plan maps out-of-scale values server-side).
	assert.Equal(t, "max", clampAlibabaMountEffortFor(schemas.Alibaba, "glm-5.3", "xhigh"))
	for _, provider := range []schemas.ModelProvider{schemas.Zhipu, schemas.Kimi, schemas.Anthropic} {
		assert.Equal(t, "xhigh", clampAlibabaMountEffortFor(provider, "glm-5.3", "xhigh"),
			"provider %s must not clamp (verbatim rule preserved)", provider)
	}
}

// TestAlibabaAnthropicMount_OpenAIDialectResponsesEffort covers the other
// half of the alibaba mount: when an Anthropic-protocol request is served
// through the OpenAI-compatible surface, the effort flows into a native
// /responses call whose reasoning.effort runs through the shared OpenAI-dialect
// ladder. qwen3.8-max tops out at xhigh there (vendor enum: none/minimal/low/
// medium/high/xhigh), so xhigh forwards verbatim and max clamps down to xhigh —
// unlike the Anthropic mount above, the gateway, not the vendor, does the
// mapping.
func TestAlibabaAnthropicMount_OpenAIDialectResponsesEffort(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ effort, want string }{
		{"xhigh", "xhigh"},
		{"max", "xhigh"},
		{"high", "high"},
	} {
		t.Run(tc.effort, func(t *testing.T) {
			ctx := providerEffortTestCtx(t)
			bifrostReq := anthropicInbound("qwen3.8-max", schemas.Ptr(tc.effort), nil).ToBifrostResponsesRequest(ctx)
			require.NotNil(t, bifrostReq)
			bifrostReq.Provider = schemas.Alibaba

			out := openai.ToOpenAIResponsesRequest(ctx, bifrostReq)
			require.NotNil(t, out)
			require.NotNil(t, out.ResponsesParameters.Reasoning, "reasoning was dropped on the alibaba responses path")
			require.NotNil(t, out.ResponsesParameters.Reasoning.Effort)
			assert.Equal(t, tc.want, *out.ResponsesParameters.Reasoning.Effort)
		})
	}
}

// TestAlibabaChatPath_EffortAloneNoSynthesizedThinking is the direct chat-with-
// toggle regression (verified live 2026-08-23): a chat request carrying
// reasoning_effort routed through use_anthropic_endpoints used to emit BOTH
// thinking:{enabled,budget_tokens} (budget derived from the effort) and
// output_config.effort, and the mount answered 400 ("'reasoning_effort' and
// 'thinking_budget' cannot be set simultaneously"). The effort-only shape works
// — the vendor engages thinking itself from the effort value — so the effort
// travels alone. Covers the effort-only input and the co-present
// reasoning.max_tokens + effort input (the mount's constraint is field-level,
// not input-path-level), plus the max→xhigh clamp on the same path.
func TestAlibabaChatPath_EffortAloneNoSynthesizedThinking(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		effort  string
		budget  *int
		wantEff string
	}{
		{name: "effort only, high", effort: "high", wantEff: "high"},
		{name: "effort only, xhigh", effort: "xhigh", wantEff: "xhigh"},
		{name: "effort only, max clamps to xhigh", effort: "max", wantEff: "xhigh"},
		{name: "co-present reasoning.max_tokens, high", effort: "high", budget: schemas.Ptr(32000), wantEff: "high"},
		{name: "co-present reasoning.max_tokens, max clamps to xhigh", effort: "max", budget: schemas.Ptr(32000), wantEff: "xhigh"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bifrostReq := &schemas.BifrostChatRequest{
				Provider: schemas.Alibaba,
				Model:    "qwen3.8-max",
				Input: []schemas.ChatMessage{
					{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("What is 1+1?")}},
				},
				Params: &schemas.ChatParameters{
					MaxCompletionTokens: schemas.Ptr(128000),
					Reasoning:           &schemas.ChatReasoning{Effort: &tc.effort, MaxTokens: tc.budget},
				},
			}

			out, err := ToAnthropicChatRequest(providerEffortTestCtx(t), bifrostReq)
			require.NoError(t, err)

			require.NotNil(t, out.OutputConfig, "chat-path effort was dropped on the alibaba mount")
			require.NotNil(t, out.OutputConfig.Effort)
			assert.Equal(t, tc.wantEff, *out.OutputConfig.Effort)
			assert.Nil(t, out.Thinking, "the alibaba mount rejects effort + thinking_budget together; the effort must travel alone")
		})
	}
}

// TestAlibabaAnthropicMount_NoEffortNoOutputConfig pins the existing no-effort
// behavior: without an effort knob nothing is forwarded and nothing synthesized.
func TestAlibabaAnthropicMount_NoEffortNoOutputConfig(t *testing.T) {
	t.Parallel()

	ctx := providerEffortTestCtx(t)
	bifrostReq := anthropicInbound("qwen3.8-max", nil, nil).ToBifrostResponsesRequest(ctx)
	bifrostReq.Provider = schemas.Alibaba

	out := toAnthropicResponsesBuilt(t, ctx, bifrostReq)

	if out.OutputConfig != nil {
		assert.Nil(t, out.OutputConfig.Effort, "no effort was requested; none may be forwarded")
	}
	assert.Nil(t, out.Thinking, "no reasoning signal was given; the model default applies")
}

// TestKimiAnthropicMount_EffortStripped pins the fail-closed behavior: Kimi's
// /anthropic mount documents no effort equivalent (MoonshotAI/Kimi-K2#129), so
// the field is stripped rather than forwarded as an unknown field. Thinking is
// NOT synthesized to compensate — an Anthropic-protocol caller who omitted
// thinking gets the model's own default (k2.6 defaults thinking on), matching
// the don't-invent-thinking rule the Anthropic provider follows.
func TestKimiAnthropicMount_EffortStripped(t *testing.T) {
	t.Parallel()

	ctx := providerEffortTestCtx(t)
	bifrostReq := anthropicInbound("kimi-k2.6", schemas.Ptr("max"), nil).ToBifrostResponsesRequest(ctx)
	bifrostReq.Provider = schemas.Kimi

	out := toAnthropicResponsesBuilt(t, ctx, bifrostReq)

	if out.OutputConfig != nil {
		assert.Nil(t, out.OutputConfig.Effort, "kimi's /anthropic mount has no effort equivalent; the field must not be forwarded")
	}
	assert.Nil(t, out.Thinking, "caller sent no thinking parameter; Bifrost must not invent one")
}

// TestZhipuAnthropicMount_DisabledThinkingRewritten covers the GLM-5.3 quirk:
// z.ai 400s on thinking.type:"disabled" for forced-thinking models ("This model
// always engages in thinking and cannot be disabled"), so it is rewritten to
// enabled with the minimum budget — the closest legal shape to the caller's
// "think as little as possible" intent.
func TestZhipuAnthropicMount_DisabledThinkingRewritten(t *testing.T) {
	t.Parallel()

	ctx := providerEffortTestCtx(t)
	bifrostReq := anthropicInbound("glm-5.3", nil, &AnthropicThinking{Type: "disabled"}).ToBifrostResponsesRequest(ctx)
	bifrostReq.Provider = schemas.Zhipu

	out := toAnthropicResponsesBuilt(t, ctx, bifrostReq)

	require.NotNil(t, out.Thinking, "GLM-5.3 rejects thinking.type:\"disabled\"; it must be rewritten, not forwarded")
	assert.Equal(t, "enabled", out.Thinking.Type)
	require.NotNil(t, out.Thinking.BudgetTokens)
	assert.Equal(t, MinimumReasoningMaxTokens, *out.Thinking.BudgetTokens)
}

// TestZhipuAnthropicMount_DisabledThinkingPreservedOnGLM52 is the control:
// GLM-5.2 supports disabling thinking, so the field passes through.
func TestZhipuAnthropicMount_DisabledThinkingPreservedOnGLM52(t *testing.T) {
	t.Parallel()

	ctx := providerEffortTestCtx(t)
	bifrostReq := anthropicInbound("glm-5.2", nil, &AnthropicThinking{Type: "disabled"}).ToBifrostResponsesRequest(ctx)
	bifrostReq.Provider = schemas.Zhipu

	out := toAnthropicResponsesBuilt(t, ctx, bifrostReq)

	require.NotNil(t, out.Thinking)
	assert.Equal(t, "disabled", out.Thinking.Type)
}

// TestZhipuChatPath_EffortEmitsOutputConfig covers the OpenAI-dialect inbound
// (reasoning_effort) on the zhipu Anthropic mount: effort lands on
// output_config.effort and thinking is synthesized from it (the vendor requires
// thinking enabled for effort to take effect).
func TestZhipuChatPath_EffortEmitsOutputConfig(t *testing.T) {
	t.Parallel()

	effort := "max"
	bifrostReq := &schemas.BifrostChatRequest{
		Provider: schemas.Zhipu,
		Model:    "glm-5.3",
		Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("What is 1+1?")}},
		},
		Params: &schemas.ChatParameters{
			MaxCompletionTokens: schemas.Ptr(128000),
			Reasoning:           &schemas.ChatReasoning{Effort: &effort},
		},
	}

	out, err := ToAnthropicChatRequest(providerEffortTestCtx(t), bifrostReq)
	require.NoError(t, err)

	require.NotNil(t, out.OutputConfig, "chat-path effort was dropped on the zhipu mount")
	require.NotNil(t, out.OutputConfig.Effort)
	assert.Equal(t, "max", *out.OutputConfig.Effort)
	require.NotNil(t, out.Thinking)
	assert.Equal(t, "enabled", out.Thinking.Type)
	require.NotNil(t, out.Thinking.BudgetTokens)
}

// TestKimiChatPath_EffortStaysBudgetOnly is the chat-path control: kimi keeps
// the budget-only shape.
func TestKimiChatPath_EffortStaysBudgetOnly(t *testing.T) {
	t.Parallel()

	effort := "high"
	bifrostReq := &schemas.BifrostChatRequest{
		Provider: schemas.Kimi,
		Model:    "kimi-k2.6",
		Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("What is 1+1?")}},
		},
		Params: &schemas.ChatParameters{
			MaxCompletionTokens: schemas.Ptr(8192),
			Reasoning:           &schemas.ChatReasoning{Effort: &effort},
		},
	}

	out, err := ToAnthropicChatRequest(providerEffortTestCtx(t), bifrostReq)
	require.NoError(t, err)

	if out.OutputConfig != nil {
		assert.Nil(t, out.OutputConfig.Effort)
	}
	require.NotNil(t, out.Thinking)
	assert.Equal(t, "enabled", out.Thinking.Type)
}

// TestZhipuChatPath_DisabledThinkingRewrittenOnGLM53 pins the chat-path half
// of the forced-thinking rewrite (the chat converter strips internally).
func TestZhipuChatPath_DisabledThinkingRewrittenOnGLM53(t *testing.T) {
	t.Parallel()

	none := "none"
	bifrostReq := &schemas.BifrostChatRequest{
		Provider: schemas.Zhipu,
		Model:    "glm-5.3",
		Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("What is 1+1?")}},
		},
		Params: &schemas.ChatParameters{
			MaxCompletionTokens: schemas.Ptr(8192),
			Reasoning:           &schemas.ChatReasoning{Effort: &none},
		},
	}

	out, err := ToAnthropicChatRequest(providerEffortTestCtx(t), bifrostReq)
	require.NoError(t, err)

	require.NotNil(t, out.Thinking, "GLM-5.3 cannot disable thinking; it must be rewritten to enabled")
	assert.Equal(t, "enabled", out.Thinking.Type)
	require.NotNil(t, out.Thinking.BudgetTokens)
	assert.Equal(t, MinimumReasoningMaxTokens, *out.Thinking.BudgetTokens)
}

// TestStripUnsupportedAnthropicFields_ProviderEffort covers the typed strip
// gate: effort survives for the documented vendor families and is removed
// everywhere else, with Anthropic's own model gate unchanged.
func TestStripUnsupportedAnthropicFields_ProviderEffort(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		provider   schemas.ModelProvider
		model      string
		kept       bool
		wantEffort string
	}{
		{"zhipu glm-5.3 keeps", schemas.Zhipu, "glm-5.3", true, "max"},
		{"zhipu glm-5.2[1m] keeps", schemas.Zhipu, "glm-5.2[1m]", true, "max"},
		{"zhipu glm-4.7 strips", schemas.Zhipu, "glm-4.7", false, ""},
		{"alibaba qwen3.8 keeps", schemas.Alibaba, "qwen3.8-max", true, "xhigh"},
		{"alibaba glm-5.2 keeps", schemas.Alibaba, "glm-5.2", true, "max"},
		{"alibaba deepseek keeps", schemas.Alibaba, "deepseek-v4-pro", true, "max"},
		{"alibaba qwen3.7 strips", schemas.Alibaba, "qwen3.7-plus", false, ""},
		{"kimi strips", schemas.Kimi, "kimi-k3", false, ""},
		{"anthropic haiku strips", schemas.Anthropic, "claude-haiku-4-5", false, ""},
		{"anthropic opus keeps", schemas.Anthropic, "claude-opus-4-6", true, "max"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := &AnthropicMessageRequest{
				Model:        tc.model,
				MaxTokens:    4096,
				OutputConfig: &AnthropicOutputConfig{Effort: schemas.Ptr("max")},
			}
			stripUnsupportedAnthropicFields(req, tc.provider, tc.model)
			if tc.kept {
				require.NotNil(t, req.OutputConfig, "output_config was stripped for %s/%s", tc.provider, tc.model)
				require.NotNil(t, req.OutputConfig.Effort)
				// z.ai maps out-of-scale values server-side, so max stays
				// verbatim there; Model Studio rejects out-of-enum values, so
				// the strip pass clamps to each family's officially valid
				// values for the alibaba profile.
				assert.Equal(t, tc.wantEffort, *req.OutputConfig.Effort)
			} else if req.OutputConfig != nil {
				assert.Nil(t, req.OutputConfig.Effort)
			}
		})
	}
}

// TestStripUnsupportedAnthropicFields_ZhipuForcedThinking covers the typed
// thinking rewrite/synthesis for forced-thinking GLM models.
func TestStripUnsupportedAnthropicFields_ZhipuForcedThinking(t *testing.T) {
	t.Parallel()

	// disabled → rewritten to enabled + minimum budget on GLM-5.3.
	req := &AnthropicMessageRequest{
		Model:     "glm-5.3",
		MaxTokens: 4096,
		Thinking:  &AnthropicThinking{Type: "disabled"},
	}
	stripUnsupportedAnthropicFields(req, schemas.Zhipu, "glm-5.3")
	require.NotNil(t, req.Thinking, "thinking.type:\"disabled\" must be rewritten for GLM-5.3, not forwarded")
	assert.Equal(t, "enabled", req.Thinking.Type)
	require.NotNil(t, req.Thinking.BudgetTokens)
	assert.Equal(t, MinimumReasoningMaxTokens, *req.Thinking.BudgetTokens)

	// Same rewrite on GLM-4.7 (forced-thinking but tolerant of an absent field).
	req47 := &AnthropicMessageRequest{
		Model:     "glm-4.7",
		MaxTokens: 4096,
		Thinking:  &AnthropicThinking{Type: "disabled"},
	}
	stripUnsupportedAnthropicFields(req47, schemas.Zhipu, "glm-4.7")
	require.NotNil(t, req47.Thinking)
	assert.Equal(t, "enabled", req47.Thinking.Type)

	// GLM-5.2 keeps disabled (control).
	req52 := &AnthropicMessageRequest{
		Model:     "glm-5.2",
		MaxTokens: 4096,
		Thinking:  &AnthropicThinking{Type: "disabled"},
	}
	stripUnsupportedAnthropicFields(req52, schemas.Zhipu, "glm-5.2")
	require.NotNil(t, req52.Thinking, "GLM-5.2 accepts thinking.type:\"disabled\"")
	assert.Equal(t, "disabled", req52.Thinking.Type)

	// Absent thinking → synthesized on GLM-5.3, budget derived from effort.
	reqSynth := &AnthropicMessageRequest{
		Model:        "glm-5.3",
		MaxTokens:    128000,
		OutputConfig: &AnthropicOutputConfig{Effort: schemas.Ptr("max")},
	}
	stripUnsupportedAnthropicFields(reqSynth, schemas.Zhipu, "glm-5.3")
	require.NotNil(t, reqSynth.Thinking, "GLM-5.3 requires an explicit thinking field")
	assert.Equal(t, "enabled", reqSynth.Thinking.Type)
	require.NotNil(t, reqSynth.Thinking.BudgetTokens)
	assert.Greater(t, *reqSynth.Thinking.BudgetTokens, MinimumReasoningMaxTokens, "effort=max should derive a large budget")

	// Absent thinking stays absent on GLM-5.2 (control).
	req52Absent := &AnthropicMessageRequest{Model: "glm-5.2", MaxTokens: 4096}
	stripUnsupportedAnthropicFields(req52Absent, schemas.Zhipu, "glm-5.2")
	assert.Nil(t, req52Absent.Thinking)
}

// TestStripUnsupportedFieldsFromRawBody_ProviderEffort is the raw-passthrough
// counterpart: same provider/model matrix against the JSON body bytes.
func TestStripUnsupportedFieldsFromRawBody_ProviderEffort(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		provider   schemas.ModelProvider
		model      string
		kept       bool
		wantEffort string
	}{
		{"zhipu glm-5.3 keeps", schemas.Zhipu, "glm-5.3", true, "max"},
		{"alibaba qwen3.8 keeps", schemas.Alibaba, "qwen3.8-max", true, "xhigh"},
		{"alibaba glm-5.2 keeps max", schemas.Alibaba, "glm-5.2", true, "max"},
		{"alibaba prefixed glm-5.3 keeps max", schemas.Alibaba, "ZHIPU/GLM-5.3", true, "max"},
		{"alibaba qwen3-max strips", schemas.Alibaba, "qwen3-max", false, ""},
		{"kimi strips", schemas.Kimi, "kimi-k2.6", false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(`{"model":"` + tc.model + `","max_tokens":4096,"messages":[{"role":"user","content":"hi"}],"output_config":{"effort":"max"}}`)
			out, err := StripUnsupportedFieldsFromRawBody(body, tc.provider, tc.model)
			require.NoError(t, err)
			got := providerUtils.GetJSONField(out, "output_config.effort")
			if tc.kept {
				require.True(t, got.Exists(), "raw output_config.effort was stripped for %s/%s", tc.provider, tc.model)
				assert.Equal(t, tc.wantEffort, got.String())
			} else {
				assert.False(t, got.Exists(), "raw output_config.effort must be stripped for %s/%s", tc.provider, tc.model)
			}
		})
	}
}

// TestResolveAnthropicMountProfile pins the host→profile mapping used for
// custom providers (base_provider_type: anthropic). The reported bug's live
// configuration was exactly this: a custom provider named "zai-anthropic"
// pointed at https://api.z.ai/api/anthropic, which must get Zhipu's capability
// profile, not Anthropic's model gates.
func TestResolveAnthropicMountProfile(t *testing.T) {
	t.Parallel()

	cases := []struct {
		baseURL string
		want    schemas.ModelProvider
	}{
		{"https://api.z.ai/api/anthropic", schemas.Zhipu},
		{"https://api.z.ai/api/coding/paas/v4", schemas.Zhipu},
		{"https://open.bigmodel.cn/api/anthropic", schemas.Zhipu},
		{"https://dashscope-intl.aliyuncs.com/apps/anthropic", schemas.Alibaba},
		{"https://ws-abc123.ap-southeast-1.maas.aliyuncs.com/apps/anthropic", schemas.Alibaba},
		{"https://token-plan.ap-southeast-1.maas.aliyuncs.com/apps/anthropic", schemas.Alibaba},
		{"https://api.moonshot.ai/anthropic", schemas.Kimi},
		{"https://api.moonshot.cn/anthropic", schemas.Kimi},
		{"https://api.kimi.com/coding", schemas.Kimi},
		{"https://api.anthropic.com", schemas.Anthropic},
		{"https://my-proxy.internal/anthropic", schemas.Anthropic},
		{"", schemas.Anthropic},
	}
	for _, tc := range cases {
		t.Run(tc.baseURL, func(t *testing.T) {
			assert.Equal(t, tc.want, ResolveAnthropicMountProfile(tc.baseURL))
		})
	}
}

// TestAnthropicProvider_ConversionProviderAndNormalization covers the custom
// provider wiring: the build config and the request's Provider both resolve to
// the mount profile, while the stock provider and custom-anthropic providers
// are untouched.
func TestAnthropicProvider_ConversionProviderAndNormalization(t *testing.T) {
	t.Parallel()

	custom := &AnthropicProvider{
		customProviderConfig: &schemas.CustomProviderConfig{BaseProviderType: schemas.Anthropic},
		networkConfig:        schemas.NetworkConfig{BaseURL: "https://api.z.ai/api/anthropic"},
	}
	assert.Equal(t, schemas.Zhipu, custom.conversionProvider())

	// Request carrying the custom provider's own name is re-tagged to the
	// resolved profile (shallow copy — the caller's request is never mutated).
	req := &schemas.BifrostChatRequest{Provider: schemas.ModelProvider("zai-anthropic"), Model: "glm-5.3"}
	normalized := custom.normalizeChatRequestForConversion(req)
	assert.Equal(t, schemas.Zhipu, normalized.Provider)
	assert.Equal(t, schemas.ModelProvider("zai-anthropic"), req.Provider, "caller's request must not be mutated")

	respReq := &schemas.BifrostResponsesRequest{Provider: schemas.ModelProvider("zai-anthropic"), Model: "glm-5.3"}
	normalizedResp := custom.normalizeResponsesRequestForConversion(respReq)
	assert.Equal(t, schemas.Zhipu, normalizedResp.Provider)

	// Stock provider: no normalization, profile stays Anthropic.
	stock := &AnthropicProvider{}
	assert.Equal(t, schemas.Anthropic, stock.conversionProvider())
	assert.Same(t, req, stock.normalizeChatRequestForConversion(req))

	// Custom provider pointed at Anthropic itself: no normalization (existing
	// behavior preserved bit-for-bit).
	customAnthropic := &AnthropicProvider{
		customProviderConfig: &schemas.CustomProviderConfig{BaseProviderType: schemas.Anthropic},
		networkConfig:        schemas.NetworkConfig{BaseURL: "https://api.anthropic.com"},
	}
	assert.Equal(t, schemas.Anthropic, customAnthropic.conversionProvider())
	assert.Same(t, req, customAnthropic.normalizeChatRequestForConversion(req))

	// Kimi-host custom provider resolves to the Kimi profile.
	customKimi := &AnthropicProvider{
		customProviderConfig: &schemas.CustomProviderConfig{BaseProviderType: schemas.Anthropic},
		networkConfig:        schemas.NetworkConfig{BaseURL: "https://api.kimi.com/coding"},
	}
	assert.Equal(t, schemas.Kimi, customKimi.conversionProvider())
}

// TestCustomProviderZaiMount_EffortAndThinkingRoundTrip is the end-to-end pin
// for the reported configuration: a request normalized through a
// "zai-anthropic"-style custom provider profile emits effort + thinking exactly
// like the built-in zhipu mount.
func TestCustomProviderZaiMount_EffortAndThinkingRoundTrip(t *testing.T) {
	t.Parallel()

	custom := &AnthropicProvider{
		customProviderConfig: &schemas.CustomProviderConfig{BaseProviderType: schemas.Anthropic},
		networkConfig:        schemas.NetworkConfig{BaseURL: "https://api.z.ai/api/anthropic"},
	}

	ctx := providerEffortTestCtx(t)
	bifrostReq := anthropicInbound("glm-5.3", schemas.Ptr("max"), nil).ToBifrostResponsesRequest(ctx)
	require.NotNil(t, bifrostReq)
	bifrostReq.Provider = schemas.ModelProvider("zai-anthropic") // what routing assigns

	normalized := custom.normalizeResponsesRequestForConversion(bifrostReq)
	out := toAnthropicResponsesBuilt(t, ctx, normalized)

	require.NotNil(t, out.OutputConfig, "output_config was dropped on the custom z.ai mount")
	require.NotNil(t, out.OutputConfig.Effort)
	assert.Equal(t, "max", *out.OutputConfig.Effort)
	require.NotNil(t, out.Thinking, "GLM-5.3 requires an explicit thinking field on z.ai's mount")
	assert.Equal(t, "enabled", out.Thinking.Type)
}

// TestStripUnsupportedFieldsFromRawBody_ZhipuForcedThinking covers the raw-path
// thinking rewrite/synthesis for GLM-5.3, and its GLM-5.2 control.
func TestStripUnsupportedFieldsFromRawBody_ZhipuForcedThinking(t *testing.T) {
	t.Parallel()

	// disabled → rewritten to enabled + minimum budget.
	body53 := []byte(`{"model":"glm-5.3","max_tokens":4096,"messages":[{"role":"user","content":"hi"}],"thinking":{"type":"disabled"}}`)
	out, err := StripUnsupportedFieldsFromRawBody(body53, schemas.Zhipu, "glm-5.3")
	require.NoError(t, err)
	assert.Equal(t, "enabled", providerUtils.GetJSONField(out, "thinking.type").String(), "thinking must be rewritten for GLM-5.3 on the raw path")
	assert.Equal(t, int64(MinimumReasoningMaxTokens), providerUtils.GetJSONField(out, "thinking.budget_tokens").Int())

	// Absent thinking → synthesized; budget derived from output_config.effort.
	bodySynth := []byte(`{"model":"glm-5.3","max_tokens":128000,"messages":[{"role":"user","content":"hi"}],"output_config":{"effort":"max"}}`)
	outSynth, err := StripUnsupportedFieldsFromRawBody(bodySynth, schemas.Zhipu, "glm-5.3")
	require.NoError(t, err)
	assert.Equal(t, "enabled", providerUtils.GetJSONField(outSynth, "thinking.type").String(), "GLM-5.3 requires an explicit thinking field")
	assert.Greater(t, providerUtils.GetJSONField(outSynth, "thinking.budget_tokens").Int(), int64(MinimumReasoningMaxTokens), "effort=max should derive a large budget")
	assert.Equal(t, "max", providerUtils.GetJSONField(outSynth, "output_config.effort").String(), "effort survives alongside the synthesized thinking")

	// GLM-5.2 keeps disabled (control).
	body52 := []byte(`{"model":"glm-5.2","max_tokens":4096,"messages":[{"role":"user","content":"hi"}],"thinking":{"type":"disabled"}}`)
	out52, err := StripUnsupportedFieldsFromRawBody(body52, schemas.Zhipu, "glm-5.2")
	require.NoError(t, err)
	assert.Equal(t, "disabled", providerUtils.GetJSONField(out52, "thinking.type").String(), "GLM-5.2 accepts thinking.type:\"disabled\"")

	// GLM-5.2 absent thinking stays absent (control).
	body52Absent := []byte(`{"model":"glm-5.2","max_tokens":4096,"messages":[{"role":"user","content":"hi"}]}`)
	out52Absent, err := StripUnsupportedFieldsFromRawBody(body52Absent, schemas.Zhipu, "glm-5.2")
	require.NoError(t, err)
	assert.False(t, providerUtils.JSONFieldExists(out52Absent, "thinking"))
}
