package gemini_test

import (
	"context"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/providers/gemini"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Gemini 3 accepts thinkingLevel, but the set of levels each model accepts is
// per-model, and Gemini 3 has no "off" switch. Bifrost used to branch only on
// whether the model name contained "pro", which emitted levels the model rejects
// (e.g. "minimal" to gemini-3.7-flash) and disabled thinking entirely via
// thinkingBudget:0 - and a Gemini 3 model with thinking disabled stops calling
// functions, which reads as "MCP tools exposed but never invoked".
//
// Levels per model: https://ai.google.dev/gemini-api/docs/thinking#thinking-levels
// ThinkingLevel enum:  https://ai.google.dev/api/generate-content#ThinkingLevel
// Thinking improves Gemini 3 function calling:
//
//	https://ai.google.dev/gemini-api/docs/function-calling#thinking
func chatReqWithReasoning(model string, reasoning *schemas.ChatReasoning) *schemas.BifrostChatRequest {
	props := schemas.NewOrderedMap()
	props.Set("path", map[string]interface{}{"type": "string"})
	return &schemas.BifrostChatRequest{
		Model: model,
		Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("read /tmp/x")}},
		},
		Params: &schemas.ChatParameters{
			Reasoning: reasoning,
			Tools: []schemas.ChatTool{
				{Type: "function", Function: &schemas.ChatToolFunction{
					Name:       "mcp_fs_read_file",
					Parameters: &schemas.ToolFunctionParameters{Type: "object", Properties: props, Required: []string{"path"}},
				}},
			},
		},
	}
}

// assertFunctionDeclarationsSurvived pins the other half of the bug: the reasoning
// controls are only worth clamping if the tools they affect actually reach Gemini.
// Asserting thinkingConfig alone would still pass if a later change dropped
// functionDeclarations, which is the exact failure this folder exists to prevent.
func assertFunctionDeclarationsSurvived(t *testing.T, out *gemini.GeminiGenerationRequest) {
	t.Helper()
	var names []string
	for _, tool := range out.Tools {
		for _, fd := range tool.FunctionDeclarations {
			names = append(names, fd.Name)
		}
	}
	require.NotEmpty(t, names, "function declarations must reach Gemini, otherwise the model cannot call the tool")
	assert.Contains(t, names, "mcp_fs_read_file")
}

func TestGeminiThinkingLevelClampedToModelSupport(t *testing.T) {
	cases := []struct {
		name   string
		model  string
		effort string
		want   string
	}{
		// gemini-3.7-flash supports low, medium, high - NOT minimal.
		{"3.7-flash minimal clamps up to low", "gemini-3.7-flash", "minimal", "low"},
		{"3.7-flash low", "gemini-3.7-flash", "low", "low"},
		{"3.7-flash medium", "gemini-3.7-flash", "medium", "medium"},
		{"3.7-flash high", "gemini-3.7-flash", "high", "high"},

		// gemini-3-pro-preview supports low and high only.
		{"3-pro minimal clamps to low", "gemini-3-pro-preview", "minimal", "low"},
		{"3-pro medium clamps to high", "gemini-3-pro-preview", "medium", "high"},

		// gemini-3.6-flash supports the full set including minimal.
		{"3.6-flash keeps minimal", "gemini-3.6-flash", "minimal", "minimal"},

		// gemini-3-flash-preview supports the full set including minimal.
		{"3-flash-preview keeps minimal", "gemini-3-flash-preview", "minimal", "minimal"},

		// Text Flash-Lite supports all four levels; its image variant does not.
		{"3.1-flash-lite keeps minimal", "gemini-3.1-flash-lite", "minimal", "minimal"},
		{"3.1-flash-lite low", "gemini-3.1-flash-lite", "low", "low"},
		{"3.1-flash-lite medium", "gemini-3.1-flash-lite", "medium", "medium"},
		{"3.1-flash-lite high", "gemini-3.1-flash-lite", "high", "high"},
		{"3.1-flash-lite-preview keeps minimal", "gemini-3.1-flash-lite-preview", "minimal", "minimal"},
		{"unknown model retains conservative floor", "gemini-3-unknown", "minimal", "low"},

		// gemini-3.1-flash-lite-image is the one documented Gemini 3 model with NO "low"
		// rung at all - Google lists exactly "minimal, high" for it. It is the reason the
		// defaultGemini3ThinkingLevels comment cannot claim every documented Gemini 3
		// model accepts "low". Source: https://ai.google.dev/gemini-api/docs/thinking
		{"3.1-flash-lite-image keeps minimal", "gemini-3.1-flash-lite-image", "minimal", "minimal"},
		{"3.1-flash-lite-image low clamps down to minimal", "gemini-3.1-flash-lite-image", "low", "minimal"},
		{"3.1-flash-lite-image medium clamps up to high", "gemini-3.1-flash-lite-image", "medium", "high"},
		{"3.1-flash-lite-image high", "gemini-3.1-flash-lite-image", "high", "high"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := gemini.ToGeminiChatCompletionRequest(nil, chatReqWithReasoning(tc.model,
				&schemas.ChatReasoning{Effort: schemas.Ptr(tc.effort)}))
			require.NoError(t, err)
			require.NotNil(t, out.GenerationConfig.ThinkingConfig, "thinkingConfig must be set")
			require.NotNil(t, out.GenerationConfig.ThinkingConfig.ThinkingLevel, "thinkingLevel must be set on Gemini 3")
			assert.Equal(t, tc.want, *out.GenerationConfig.ThinkingConfig.ThinkingLevel)
			assert.Nil(t, out.GenerationConfig.ThinkingConfig.ThinkingBudget,
				"Gemini 3 is controlled by thinkingLevel, thinkingBudget must not be sent alongside it")
			assertFunctionDeclarationsSurvived(t, out)
		})
	}
}

// Gemini 3 cannot turn thinking off. Sending thinkingBudget:0 both uses the wrong
// control surface and kills function calling. "none" must land on the model's
// lowest supported level instead.
func TestGeminiEffortNoneDoesNotDisableThinkingOnGemini3(t *testing.T) {
	cases := []struct {
		model string
		want  string
	}{
		{"gemini-3.7-flash", "low"},     // floor is low
		{"gemini-3.6-flash", "minimal"}, // floor is minimal
		{"gemini-3-pro-preview", "low"}, // floor is low
		{"gemini-3-flash-preview", "minimal"},
		{"gemini-3.1-flash-lite", "minimal"},
	}

	for _, tc := range cases {
		t.Run(tc.model, func(t *testing.T) {
			out, err := gemini.ToGeminiChatCompletionRequest(nil, chatReqWithReasoning(tc.model,
				&schemas.ChatReasoning{Effort: schemas.Ptr("none")}))
			require.NoError(t, err)
			require.NotNil(t, out.GenerationConfig.ThinkingConfig)
			require.NotNil(t, out.GenerationConfig.ThinkingConfig.ThinkingLevel)
			assert.Equal(t, tc.want, *out.GenerationConfig.ThinkingConfig.ThinkingLevel)
			assert.Nil(t, out.GenerationConfig.ThinkingConfig.ThinkingBudget,
				"thinkingBudget:0 disables thinking and breaks Gemini 3 function calling")
			assertFunctionDeclarationsSurvived(t, out)
		})
	}
}

// Same for an explicit zero budget: on Gemini 3 it must not disable thinking.
func TestGeminiZeroBudgetDoesNotDisableThinkingOnGemini3(t *testing.T) {
	out, err := gemini.ToGeminiChatCompletionRequest(nil, chatReqWithReasoning("gemini-3.7-flash",
		&schemas.ChatReasoning{MaxTokens: schemas.Ptr(0)}))
	require.NoError(t, err)
	require.NotNil(t, out.GenerationConfig.ThinkingConfig)
	require.NotNil(t, out.GenerationConfig.ThinkingConfig.ThinkingLevel)
	assert.Equal(t, "low", *out.GenerationConfig.ThinkingConfig.ThinkingLevel)
	assert.Nil(t, out.GenerationConfig.ThinkingConfig.ThinkingBudget)
	assertFunctionDeclarationsSurvived(t, out)
}

// Gemini 2.5 must keep its existing budget-based behaviour untouched.
func TestGeminiThinkingUnchangedForGemini25(t *testing.T) {
	t.Run("effort maps to budget", func(t *testing.T) {
		out, err := gemini.ToGeminiChatCompletionRequest(nil, chatReqWithReasoning("gemini-2.5-pro",
			&schemas.ChatReasoning{Effort: schemas.Ptr("medium")}))
		require.NoError(t, err)
		require.NotNil(t, out.GenerationConfig.ThinkingConfig)
		assert.Nil(t, out.GenerationConfig.ThinkingConfig.ThinkingLevel,
			"thinkingLevel on a pre-3.0 model is a hard API error")
		require.NotNil(t, out.GenerationConfig.ThinkingConfig.ThinkingBudget)
	})

	t.Run("gemini-2.5-pro cannot disable thinking", func(t *testing.T) {
		out, err := gemini.ToGeminiChatCompletionRequest(nil, chatReqWithReasoning("gemini-2.5-pro",
			&schemas.ChatReasoning{Effort: schemas.Ptr("none")}))
		require.NoError(t, err)
		assert.Nil(t, out.GenerationConfig.ThinkingConfig)
	})

	t.Run("gemini-2.5-flash can disable thinking with budget 0", func(t *testing.T) {
		out, err := gemini.ToGeminiChatCompletionRequest(nil, chatReqWithReasoning("gemini-2.5-flash",
			&schemas.ChatReasoning{Effort: schemas.Ptr("none")}))
		require.NoError(t, err)
		require.NotNil(t, out.GenerationConfig.ThinkingConfig)
		require.NotNil(t, out.GenerationConfig.ThinkingConfig.ThinkingBudget)
		assert.Equal(t, int32(0), *out.GenerationConfig.ThinkingConfig.ThinkingBudget)
	})
}

// The Responses path shares the same gating and must behave identically.
func TestGeminiThinkingLevelClampedOnResponsesPath(t *testing.T) {
	req := &schemas.BifrostResponsesRequest{
		Model: "gemini-3.7-flash",
		Input: []schemas.ResponsesMessage{
			{Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("read /tmp/x")}},
		},
		Params: &schemas.ResponsesParameters{
			Reasoning: &schemas.ResponsesParametersReasoning{Effort: schemas.Ptr("minimal")},
		},
	}
	out, err := gemini.ToGeminiResponsesRequest(nil, req)
	require.NoError(t, err)
	require.NotNil(t, out.GenerationConfig.ThinkingConfig)
	require.NotNil(t, out.GenerationConfig.ThinkingConfig.ThinkingLevel)
	assert.Equal(t, "low", *out.GenerationConfig.ThinkingConfig.ThinkingLevel)
}

// Pins the claim defaultGemini3ThinkingLevels' comment makes about its own fallback.
//
// The comment used to justify the fallback with "low is the only rung every documented
// Gemini 3 model accepts", which the support table itself contradicts: its first entry,
// gemini-3.1-flash-lite-image, accepts only "minimal" and "high". This asserts that
// counterexample directly, so the justification cannot silently drift back to a claim the
// table disproves. Source: https://ai.google.dev/gemini-api/docs/thinking
func TestNotEveryDocumentedGemini3ModelAcceptsLow(t *testing.T) {
	out, err := gemini.ToGeminiChatCompletionRequest(nil, chatReqWithReasoning(
		"gemini-3.1-flash-lite-image", &schemas.ChatReasoning{Effort: schemas.Ptr("low")}))
	require.NoError(t, err)
	require.NotNil(t, out.GenerationConfig.ThinkingConfig)
	require.NotNil(t, out.GenerationConfig.ThinkingConfig.ThinkingLevel)

	assert.NotEqual(t, "low", *out.GenerationConfig.ThinkingConfig.ThinkingLevel,
		`gemini-3.1-flash-lite-image has no "low" rung, so "low" must never reach the wire for it`)
	assert.Equal(t, "minimal", *out.GenerationConfig.ThinkingConfig.ThinkingLevel,
		`"minimal" is the nearest rung below "low" that this model implements`)
}

// GenAI requests for Gemini and Vertex share the native schema and normalize to
// Responses before routing. Changing the provider must preserve supported levels,
// including on the second conversion when a request is retried or falls back.
func TestFlashLiteThinkingRoundTripAcrossProviders(t *testing.T) {
	cases := []struct {
		name       string
		config     *gemini.GenerationConfigThinkingConfig
		wantLevel  string
		wantBudget *int32
	}{
		{"minimal", &gemini.GenerationConfigThinkingConfig{ThinkingLevel: schemas.Ptr("MINIMAL")}, "minimal", nil},
		{"low", &gemini.GenerationConfigThinkingConfig{ThinkingLevel: schemas.Ptr("LOW")}, "low", nil},
		{"medium", &gemini.GenerationConfigThinkingConfig{ThinkingLevel: schemas.Ptr("MEDIUM")}, "medium", nil},
		{"high", &gemini.GenerationConfigThinkingConfig{ThinkingLevel: schemas.Ptr("HIGH")}, "high", nil},
		{"zero budget uses floor", &gemini.GenerationConfigThinkingConfig{ThinkingBudget: schemas.Ptr(int32(0))}, "minimal", nil},
		{"dynamic budget", &gemini.GenerationConfigThinkingConfig{ThinkingBudget: schemas.Ptr(int32(-1))}, "", schemas.Ptr(int32(-1))},
		{"explicit budget wins over level", &gemini.GenerationConfigThinkingConfig{ThinkingBudget: schemas.Ptr(int32(1024)), ThinkingLevel: schemas.Ptr("HIGH")}, "", schemas.Ptr(int32(1024))},
		{"unspecified", nil, "", nil},
	}
	for _, source := range []schemas.ModelProvider{schemas.Gemini, schemas.Vertex} {
		for _, target := range []schemas.ModelProvider{schemas.Gemini, schemas.Vertex} {
			for _, tc := range cases {
				t.Run(string(source)+" to "+string(target)+"/"+tc.name, func(t *testing.T) {
					ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
					native := &gemini.GeminiGenerationRequest{
						Model:            string(source) + "/gemini-3.1-flash-lite",
						Contents:         []gemini.Content{{Role: "user", Parts: []*gemini.Part{{Text: "Say OK."}}}},
						GenerationConfig: gemini.GenerationConfig{ThinkingConfig: tc.config},
					}
					normalized := native.ToBifrostResponsesRequest(ctx)
					require.Equal(t, source, normalized.Provider)
					require.Equal(t, "gemini-3.1-flash-lite", normalized.Model)
					normalized.Provider = target
					for range 2 {
						var out *gemini.GeminiGenerationRequest
						var err error
						if target == schemas.Vertex {
							out, err = gemini.ToGeminiResponsesRequestWithImageURLSchemes(ctx, normalized, "http", "https", "gs")
						} else {
							out, err = gemini.ToGeminiResponsesRequest(ctx, normalized)
						}
						require.NoError(t, err)
						got := out.GenerationConfig.ThinkingConfig
						if tc.config == nil {
							require.Nil(t, got)
							continue
						}
						require.NotNil(t, got)
						if tc.wantLevel == "" {
							assert.Nil(t, got.ThinkingLevel)
						} else {
							require.NotNil(t, got.ThinkingLevel)
							assert.Equal(t, tc.wantLevel, *got.ThinkingLevel)
						}
						assert.Equal(t, tc.wantBudget, got.ThinkingBudget)
					}
					if tc.config != nil && tc.config.ThinkingBudget == nil {
						assert.Equal(t, strings.ToLower(*tc.config.ThinkingLevel), *normalized.Params.Reasoning.Effort)
						assert.Nil(t, normalized.Params.Reasoning.MaxTokens)
					}
				})
			}
		}
	}
}

// Gemini 4 removed thinkingBudget: the API rejects it with "Thinking budget is not
// supported for this model. Use thinking level instead." and accepts only
// low/medium/high, with no way to turn thinking off. A budget from any ingress has to
// reach the wire as the nearest thinkingLevel.
// Source: https://ai.google.dev/gemini-api/docs/thinking
var gemini4BudgetToLevelCases = []struct {
	name   string
	budget int
	want   string
}{
	{"small budget is low", 512, "low"},
	{"1024 is low", 1024, "low"},
	{"just above 1024 is medium", 1025, "medium"},
	{"agy default 4096 is medium", 4096, "medium"},
	{"8192 is medium", 8192, "medium"},
	{"just above 8192 is high", 8193, "high"},
	{"large budget is high", 32768, "high"},
	{"zero budget lands on the floor", 0, "low"},
}

func assertGemini4ThinkingLevel(t *testing.T, got *gemini.GenerationConfigThinkingConfig, want string) {
	t.Helper()
	require.NotNil(t, got, "thinkingConfig must be set")
	assert.Nil(t, got.ThinkingBudget, "gemini-4 rejects thinkingBudget with a 400")
	require.NotNil(t, got.ThinkingLevel, "a budget must be translated to thinkingLevel on gemini-4")
	assert.Equal(t, want, *got.ThinkingLevel)
}

func TestGemini4ThinkingBudgetMapsToLevelOnChatPath(t *testing.T) {
	for _, tc := range gemini4BudgetToLevelCases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := gemini.ToGeminiChatCompletionRequest(nil, chatReqWithReasoning("gemini-4-argon",
				&schemas.ChatReasoning{MaxTokens: schemas.Ptr(tc.budget)}))
			require.NoError(t, err)
			assertGemini4ThinkingLevel(t, out.GenerationConfig.ThinkingConfig, tc.want)
			assertFunctionDeclarationsSurvived(t, out)
		})
	}

	t.Run("dynamic budget leaves the level to the model default", func(t *testing.T) {
		out, err := gemini.ToGeminiChatCompletionRequest(nil, chatReqWithReasoning("gemini-4-argon",
			&schemas.ChatReasoning{MaxTokens: schemas.Ptr(-1)}))
		require.NoError(t, err)
		require.NotNil(t, out.GenerationConfig.ThinkingConfig)
		assert.Nil(t, out.GenerationConfig.ThinkingConfig.ThinkingBudget)
		assert.Nil(t, out.GenerationConfig.ThinkingConfig.ThinkingLevel)
	})
}

func TestGemini4ThinkingBudgetMapsToLevelOnResponsesPath(t *testing.T) {
	for _, provider := range []schemas.ModelProvider{schemas.Gemini, schemas.Vertex} {
		for _, tc := range gemini4BudgetToLevelCases {
			t.Run(string(provider)+"/"+tc.name, func(t *testing.T) {
				req := &schemas.BifrostResponsesRequest{
					Provider: provider,
					Model:    "gemini-4-argon",
					Input: []schemas.ResponsesMessage{
						{Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
							Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("hi")}},
					},
					Params: &schemas.ResponsesParameters{
						Reasoning: &schemas.ResponsesParametersReasoning{MaxTokens: schemas.Ptr(tc.budget)},
					},
				}
				out, err := gemini.ToGeminiResponsesRequest(nil, req)
				require.NoError(t, err)
				assertGemini4ThinkingLevel(t, out.GenerationConfig.ThinkingConfig, tc.want)
			})
		}
	}
}

// Gemini 3 still accepts a budget, so it must keep reaching the wire unchanged.
func TestGemini3ThinkingBudgetStillSentAsBudget(t *testing.T) {
	out, err := gemini.ToGeminiChatCompletionRequest(nil, chatReqWithReasoning("gemini-3.8-flash",
		&schemas.ChatReasoning{MaxTokens: schemas.Ptr(4096)}))
	require.NoError(t, err)
	require.NotNil(t, out.GenerationConfig.ThinkingConfig)
	require.NotNil(t, out.GenerationConfig.ThinkingConfig.ThinkingBudget)
	assert.Equal(t, int32(4096), *out.GenerationConfig.ThinkingConfig.ThinkingBudget)
	assert.Nil(t, out.GenerationConfig.ThinkingConfig.ThinkingLevel)
}

// The GenAI ingress (agy in gateway mode) on the typed path: the exact thinkingConfig
// agy 1.4.9 sends must leave Bifrost as a level for gemini-4.
func TestGemini4GenAIThinkingBudgetRoundTripsAsLevel(t *testing.T) {
	for _, provider := range []schemas.ModelProvider{schemas.Gemini, schemas.Vertex} {
		t.Run(string(provider), func(t *testing.T) {
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			native := &gemini.GeminiGenerationRequest{
				Model:    string(provider) + "/gemini-4-argon",
				Contents: []gemini.Content{{Role: "user", Parts: []*gemini.Part{{Text: "Say OK."}}}},
				GenerationConfig: gemini.GenerationConfig{ThinkingConfig: &gemini.GenerationConfigThinkingConfig{
					IncludeThoughts: true,
					ThinkingBudget:  schemas.Ptr(int32(4096)),
				}},
			}
			normalized := native.ToBifrostResponsesRequest(ctx)
			out, err := gemini.ToGeminiResponsesRequest(ctx, normalized)
			require.NoError(t, err)
			assertGemini4ThinkingLevel(t, out.GenerationConfig.ThinkingConfig, "medium")
			assert.True(t, out.GenerationConfig.ThinkingConfig.IncludeThoughts)
		})
	}
}

// The GenAI ingress on the raw passthrough path forwards the body as sent, so the
// byte-level normalizer has to make the same translation without reordering keys.
func TestGemini4RawThinkingBudgetRewrittenToLevel(t *testing.T) {
	cases := []struct {
		name  string
		model string
		in    string
		want  string
	}{
		{
			"agy request",
			"gemini-4-argon",
			`{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"temperature":1,"thinkingConfig":{"includeThoughts":true,"thinkingBudget":4096},"maxOutputTokens":8192}}`,
			`{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"temperature":1,"thinkingConfig":{"includeThoughts":true,"thinkingLevel":"medium"},"maxOutputTokens":8192}}`,
		},
		{
			"snake_case keeps snake_case",
			"gemini-4-argon",
			`{"generation_config":{"thinking_config":{"include_thoughts":true,"thinking_budget":16384}}}`,
			`{"generation_config":{"thinking_config":{"include_thoughts":true,"thinking_level":"high"}}}`,
		},
		{
			"explicit level wins over budget",
			"gemini-4-argon",
			`{"generationConfig":{"thinkingConfig":{"thinkingBudget":512,"thinkingLevel":"high"}}}`,
			`{"generationConfig":{"thinkingConfig":{"thinkingLevel":"high"}}}`,
		},
		{
			"zero budget lands on the floor",
			"gemini-4-argon",
			`{"generationConfig":{"thinkingConfig":{"thinkingBudget":0}}}`,
			`{"generationConfig":{"thinkingConfig":{"thinkingLevel":"low"}}}`,
		},
		{
			"dynamic budget is dropped",
			"gemini-4-argon",
			`{"generationConfig":{"thinkingConfig":{"includeThoughts":true,"thinkingBudget":-1}}}`,
			`{"generationConfig":{"thinkingConfig":{"includeThoughts":true}}}`,
		},
		{
			"prefixed vertex model name",
			"google/gemini-4-argon",
			`{"generationConfig":{"thinkingConfig":{"thinkingBudget":1024}}}`,
			`{"generationConfig":{"thinkingConfig":{"thinkingLevel":"low"}}}`,
		},
		{
			"camelCase config with snake_case budget",
			"gemini-4-argon",
			`{"generationConfig":{"thinkingConfig":{"includeThoughts":true,"thinking_budget":4096}}}`,
			`{"generationConfig":{"thinkingConfig":{"includeThoughts":true,"thinking_level":"medium"}}}`,
		},
		{
			"snake_case config with camelCase budget",
			"gemini-4-argon",
			`{"generation_config":{"thinking_config":{"thinkingBudget":16384}}}`,
			`{"generation_config":{"thinking_config":{"thinkingLevel":"high"}}}`,
		},
		{
			"explicit level in the other spelling wins over budget",
			"gemini-4-argon",
			`{"generationConfig":{"thinkingConfig":{"thinkingBudget":512,"thinking_level":"high"}}}`,
			`{"generationConfig":{"thinkingConfig":{"thinking_level":"high"}}}`,
		},
		{
			"invalid negative budget is left for Google to reject",
			"gemini-4-argon",
			`{"generationConfig":{"thinkingConfig":{"thinkingBudget":-2}}}`,
			`{"generationConfig":{"thinkingConfig":{"thinkingBudget":-2}}}`,
		},
		{
			"null budget means unset and is left alone",
			"gemini-4-argon",
			`{"generationConfig":{"thinkingConfig":{"thinkingBudget":null}}}`,
			`{"generationConfig":{"thinkingConfig":{"thinkingBudget":null}}}`,
		},
		{
			"boolean budget is left for Google to reject",
			"gemini-4-argon",
			`{"generationConfig":{"thinkingConfig":{"thinkingBudget":true}}}`,
			`{"generationConfig":{"thinkingConfig":{"thinkingBudget":true}}}`,
		},
		{
			"object budget is left for Google to reject",
			"gemini-4-argon",
			`{"generationConfig":{"thinkingConfig":{"thinkingBudget":{}}}}`,
			`{"generationConfig":{"thinkingConfig":{"thinkingBudget":{}}}}`,
		},
		{
			"non-numeric string budget is left for Google to reject",
			"gemini-4-argon",
			`{"generationConfig":{"thinkingConfig":{"thinkingBudget":"abc"}}}`,
			`{"generationConfig":{"thinkingConfig":{"thinkingBudget":"abc"}}}`,
		},
		{
			"fractional budget is left for Google to reject",
			"gemini-4-argon",
			`{"generationConfig":{"thinkingConfig":{"thinkingBudget":4096.5}}}`,
			`{"generationConfig":{"thinkingConfig":{"thinkingBudget":4096.5}}}`,
		},
		{
			"quoted int32 budget is rewritten like a number",
			"gemini-4-argon",
			`{"generationConfig":{"thinkingConfig":{"thinkingBudget":"1024"}}}`,
			`{"generationConfig":{"thinkingConfig":{"thinkingLevel":"low"}}}`,
		},
		{
			"gemini 3 keeps its budget",
			"gemini-3.8-flash",
			`{"generationConfig":{"thinkingConfig":{"includeThoughts":true,"thinkingBudget":4096}}}`,
			`{"generationConfig":{"thinkingConfig":{"includeThoughts":true,"thinkingBudget":4096}}}`,
		},
	}
	for _, provider := range []schemas.ModelProvider{schemas.Gemini, schemas.Vertex} {
		for _, tc := range cases {
			t.Run(string(provider)+"/"+tc.name, func(t *testing.T) {
				got := gemini.NormalizeRawGenerateContentRequestForCompatibility(nil, []byte(tc.in), provider, tc.model)
				assert.Equal(t, tc.want, string(got))
			})
		}
	}
}

// A Vertex alias can send an opaque endpoint id upstream while naming gemini-4-argon
// as its canonical model. The typed paths resolve that canonical model; the raw path
// has to as well, or the budget reaches Gemini 4 and fails.
func TestGemini4RawThinkingBudgetRewrittenForAliasedModel(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyResolvedAlias, &schemas.ResolvedAlias{
		Key:    "argon",
		Config: &schemas.AliasConfig{ModelID: "1234567890", ModelName: schemas.Ptr("gemini-4-argon")},
	})
	got := gemini.NormalizeRawGenerateContentRequestForCompatibility(ctx,
		[]byte(`{"generationConfig":{"thinkingConfig":{"thinkingBudget":4096}}}`), schemas.Vertex, "1234567890")
	assert.Equal(t, `{"generationConfig":{"thinkingConfig":{"thinkingLevel":"medium"}}}`, string(got))
}

// Only 0 and -1 are special budget values. Mapping a budget to a level on Gemini 4
// must not turn an invalid negative budget into a valid "low".
func TestGemini4InvalidNegativeThinkingBudgetRejected(t *testing.T) {
	t.Run("chat", func(t *testing.T) {
		_, err := gemini.ToGeminiChatCompletionRequest(nil, chatReqWithReasoning("gemini-4-argon",
			&schemas.ChatReasoning{MaxTokens: schemas.Ptr(-2)}))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "only 0 and -1 are supported")
	})
	t.Run("responses", func(t *testing.T) {
		_, err := gemini.ToGeminiResponsesRequest(nil, &schemas.BifrostResponsesRequest{
			Model: "gemini-4-argon",
			Input: []schemas.ResponsesMessage{
				{Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
					Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("hi")}},
			},
			Params: &schemas.ResponsesParameters{
				Reasoning: &schemas.ResponsesParametersReasoning{MaxTokens: schemas.Ptr(-2)},
			},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "only 0 and -1 are supported")
	})
}
