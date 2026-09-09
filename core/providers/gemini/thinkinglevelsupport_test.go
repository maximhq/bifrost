package gemini_test

import (
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
