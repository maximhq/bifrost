package anthropic

import (
	"testing"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
)

// Override-aware variants of SupportsNativeEffort / SupportsFastMode /
// SupportsAdaptiveThinking. The existing TestSupportsAdaptiveThinking
// (utils_test.go:1996) already covers the substring fallback path.
//
// Each helper looks up its bare model name via providerUtils.GetBifrostOverrides.
// We seed the cache directly under the test model key, then assert the
// helper returns the override value (and not the substring fallback).
//
// Models are picked so the substring fallback would return the OPPOSITE
// answer — that way the test only passes when the override path is taken.

func setOverride(t *testing.T, key string, ov schemas.BifrostOverrides) {
	t.Helper()
	providerUtils.SetModelParams(key, providerUtils.ModelParams{BifrostOverrides: &ov})
	t.Cleanup(func() { providerUtils.DeleteModelParams(key) })
}

func TestSupportsNativeEffort_OverrideHit(t *testing.T) {
	model := "non-claude-test-model-effort-yes"
	field := "output_config.effort"
	setOverride(t, model, schemas.BifrostOverrides{
		Reasoning: &schemas.BifrostReasoningConfig{Field: &field},
	})

	// Substring fallback would return false (no "opus") — override wins.
	assert.True(t, SupportsNativeEffort(model))
}

func TestSupportsNativeEffort_OverrideMiss(t *testing.T) {
	model := "non-claude-test-model-effort-no"
	field := "thinking.budget_tokens"
	setOverride(t, model, schemas.BifrostOverrides{
		Reasoning: &schemas.BifrostReasoningConfig{Field: &field},
	})
	assert.False(t, SupportsNativeEffort(model))
}

func TestSupportsNativeEffort_OverrideAbsent_FallbackTakesOver(t *testing.T) {
	// No override registered → existing substring logic runs.
	assert.True(t, SupportsNativeEffort("claude-opus-4-6-20250514"))
	assert.False(t, SupportsNativeEffort("claude-haiku-4-5-20251001"))
}

func TestSupportsFastMode_OverrideHit(t *testing.T) {
	model := "non-opus-test-model-fast-yes"
	yes := true
	setOverride(t, model, schemas.BifrostOverrides{SupportsFastMode: &yes})
	assert.True(t, SupportsFastMode(model))
}

func TestSupportsFastMode_OverrideExplicitFalse(t *testing.T) {
	// Even on a name that LOOKS like opus 4.6, an explicit false overrides
	// the substring fallback.
	model := "claude-opus-4-6-test-overridden"
	no := false
	setOverride(t, model, schemas.BifrostOverrides{SupportsFastMode: &no})
	assert.False(t, SupportsFastMode(model))
}

func TestSupportsFastMode_OverrideAbsent_FallbackTakesOver(t *testing.T) {
	assert.True(t, SupportsFastMode("claude-opus-4-6-20250514"))
	assert.False(t, SupportsFastMode("claude-opus-4-7-20260401"))
}

func TestSupportsAdaptiveThinking_OverrideHit(t *testing.T) {
	model := "non-opus-test-model-adaptive-yes"
	style := "adaptive"
	setOverride(t, model, schemas.BifrostOverrides{
		Reasoning: &schemas.BifrostReasoningConfig{Style: &style},
	})
	assert.True(t, SupportsAdaptiveThinking(model))
}

func TestSupportsAdaptiveThinking_OverrideMiss(t *testing.T) {
	// Opus 4.6 model name (substring fallback would be true) but override
	// says style is budget_tokens → override wins.
	model := "claude-opus-4-6-overridden"
	style := "budget_tokens"
	setOverride(t, model, schemas.BifrostOverrides{
		Reasoning: &schemas.BifrostReasoningConfig{Style: &style},
	})
	assert.False(t, SupportsAdaptiveThinking(model))
}

func TestSupportsAdaptiveThinking_OverrideAbsent_FallbackTakesOver(t *testing.T) {
	// Existing TestSupportsAdaptiveThinking already covers this; one
	// targeted assertion here as a smoke check.
	assert.True(t, SupportsAdaptiveThinking("claude-opus-4-7-20260401"))
}

// ---- Stage 2C: tool-generation helpers ----

func TestComputerUseGeneration_OverrideNewGen(t *testing.T) {
	model := "non-opus-test-model-computer-new"
	setOverride(t, model, schemas.BifrostOverrides{
		ServerTools: map[string]string{"computer_use": string(AnthropicToolTypeComputer20251124)},
	})
	// Substring fallback would return ComputerUseGen20250124 (no opus/sonnet 4.x match).
	assert.Equal(t, ComputerUseGen20251124, ComputerUseGeneration(model))
}

func TestComputerUseGeneration_OverrideOldGen(t *testing.T) {
	// Substring fallback for opus-4-6 would say new gen, but override
	// pinning it to the old computer_20250124 wins.
	model := "claude-opus-4-6-overridden-old-computer"
	setOverride(t, model, schemas.BifrostOverrides{
		ServerTools: map[string]string{"computer_use": string(AnthropicToolTypeComputer20250124)},
	})
	assert.Equal(t, ComputerUseGen20250124, ComputerUseGeneration(model))
}

func TestComputerUseGeneration_OverrideAbsent_FallbackTakesOver(t *testing.T) {
	assert.Equal(t, ComputerUseGen20251124, ComputerUseGeneration("claude-opus-4-7-20260401"))
	assert.Equal(t, ComputerUseGen20251124, ComputerUseGeneration("claude-sonnet-4-6-20250514"))
	assert.Equal(t, ComputerUseGen20250124, ComputerUseGeneration("claude-haiku-4-5-20251001"))
}

func TestTextEditorGeneration_OverrideNewGen(t *testing.T) {
	model := "non-opus-test-model-text-editor-new"
	setOverride(t, model, schemas.BifrostOverrides{
		ServerTools: map[string]string{"text_editor": string(AnthropicToolTypeTextEditor20250728)},
	})
	// Substring fallback would return ComputerUseGen20250124.
	assert.Equal(t, ComputerUseGen20251124, TextEditorGeneration(model))
}

func TestTextEditorGeneration_OverrideOldGen(t *testing.T) {
	// Substring fallback for opus-4-6 would say new gen; override pinning
	// to text_editor_20250124 wins.
	model := "claude-opus-4-6-overridden-old-text-editor"
	setOverride(t, model, schemas.BifrostOverrides{
		ServerTools: map[string]string{"text_editor": string(AnthropicToolTypeTextEditor20250124)},
	})
	assert.Equal(t, ComputerUseGen20250124, TextEditorGeneration(model))
}

func TestTextEditorGeneration_OverrideAbsent_FallbackTakesOver(t *testing.T) {
	assert.Equal(t, ComputerUseGen20251124, TextEditorGeneration("claude-opus-4-7-20260401"))
	// sonnet-4-5 deliberately uses new-gen text_editor per docstring.
	assert.Equal(t, ComputerUseGen20251124, TextEditorGeneration("claude-sonnet-4-5-20241022"))
	assert.Equal(t, ComputerUseGen20250124, TextEditorGeneration("claude-3-5-sonnet-20241022"))
}
