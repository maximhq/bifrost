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

func setOverride(t *testing.T, model string, ov schemas.BifrostOverrides) {
	t.Helper()
	// Helpers read overrides via GetBifrostOverridesForRequest(Anthropic, model),
	// which resolves the OverrideCacheKey composite; seed under the same key.
	key := providerUtils.OverrideCacheKey(model, schemas.Anthropic)
	providerUtils.SetModelParams(key, providerUtils.ModelParams{BifrostOverrides: &ov})
	t.Cleanup(func() { providerUtils.DeleteModelParams(key) })
}

func TestSupportsNativeEffort_OverrideHit(t *testing.T) {
	model := "non-claude-test-model-effort-yes"
	yes := true
	setOverride(t, model, schemas.BifrostOverrides{SupportsNativeEffort: &yes})

	// Substring fallback would return false (no "opus") — override wins.
	assert.True(t, SupportsNativeEffort(schemas.Anthropic, model))
}

func TestSupportsNativeEffort_OverrideMiss(t *testing.T) {
	// Explicit false override even though the name would fall back true.
	model := "claude-opus-4-6-effort-overridden"
	no := false
	setOverride(t, model, schemas.BifrostOverrides{SupportsNativeEffort: &no})
	assert.False(t, SupportsNativeEffort(schemas.Anthropic, model))
}

func TestSupportsNativeEffort_OverrideAbsent_FallbackTakesOver(t *testing.T) {
	// Derived = accepts effort AND not adaptive. Opus 4.5 accepts effort and
	// predates adaptive → true. Opus 4.6 accepts effort but is adaptive → false.
	// Haiku doesn't accept effort → false.
	assert.True(t, SupportsNativeEffort(schemas.Anthropic, "claude-opus-4-5-20251101"))
	assert.False(t, SupportsNativeEffort(schemas.Anthropic, "claude-opus-4-6-20250514"))
	assert.False(t, SupportsNativeEffort(schemas.Anthropic, "claude-haiku-4-5-20251001"))
}

func TestSupportsNativeEffort_DerivedFalseWhenAdaptive(t *testing.T) {
	// Accepts effort AND supports adaptive → adaptive wins the ladder, so the
	// derived native-effort helper is false even though effort is accepted.
	model := "effort-and-adaptive-model"
	yes := true
	setOverride(t, model, schemas.BifrostOverrides{
		SupportsNativeEffort:     &yes,
		SupportsAdaptiveThinking: &yes,
	})
	assert.True(t, SupportsEffortParameter(schemas.Anthropic, model))
	assert.False(t, SupportsNativeEffort(schemas.Anthropic, model))
}

func TestSupportsEffortParameter_OverrideHit(t *testing.T) {
	model := "non-claude-effort-param-yes"
	yes := true
	setOverride(t, model, schemas.BifrostOverrides{SupportsNativeEffort: &yes})
	// Substring fallback would return false — override wins.
	assert.True(t, SupportsEffortParameter(schemas.Anthropic, model))
}

func TestSupportsEffortParameter_OverrideExplicitFalse(t *testing.T) {
	// Opus 4.7 name would fall back true, but an explicit false override wins.
	model := "claude-opus-4-7-effort-overridden"
	no := false
	setOverride(t, model, schemas.BifrostOverrides{SupportsNativeEffort: &no})
	assert.False(t, SupportsEffortParameter(schemas.Anthropic, model))
}

func TestSupportsEffortParameter_FallbackTakesOver(t *testing.T) {
	// The effort-supported set per the effort docs; sonnet 4.5 / haiku reject it.
	assert.True(t, SupportsEffortParameter(schemas.Anthropic, "claude-opus-4-8-20260601"))
	assert.True(t, SupportsEffortParameter(schemas.Anthropic, "claude-opus-4-5-20251101"))
	assert.True(t, SupportsEffortParameter(schemas.Anthropic, "claude-sonnet-4-6-20250514"))
	assert.False(t, SupportsEffortParameter(schemas.Anthropic, "claude-sonnet-4-5-20250929"))
	assert.False(t, SupportsEffortParameter(schemas.Anthropic, "claude-haiku-4-5-20251001"))
}

func TestSupportsMidConversationSystem_OverrideHit(t *testing.T) {
	// Anthropic provider + a non-opus-4.8 name (fallback false) → override wins.
	model := "non-opus-midconv-yes"
	yes := true
	setOverride(t, model, schemas.BifrostOverrides{SupportsMidConversationSystem: &yes})
	assert.True(t, SupportsMidConversationSystem(schemas.Anthropic, model))
}

func TestSupportsMidConversationSystem_OverrideExplicitFalse(t *testing.T) {
	// Opus 4.8 name would fall back true, but an explicit false override wins.
	model := "claude-opus-4-8-midconv-off"
	no := false
	setOverride(t, model, schemas.BifrostOverrides{SupportsMidConversationSystem: &no})
	assert.False(t, SupportsMidConversationSystem(schemas.Anthropic, model))
}

func TestSupportsMidConversationSystem_ProviderGateWins(t *testing.T) {
	// The hardcoded Anthropic-only provider gate runs before the override, so a
	// non-Anthropic provider stays false even with an explicit true override.
	model := "claude-opus-4-8-gatecheck"
	yes := true
	setOverride(t, model, schemas.BifrostOverrides{SupportsMidConversationSystem: &yes})
	assert.True(t, SupportsMidConversationSystem(schemas.Anthropic, model))
	assert.False(t, SupportsMidConversationSystem(schemas.Vertex, model))
	assert.False(t, SupportsMidConversationSystem(schemas.Bedrock, model))
	assert.False(t, SupportsMidConversationSystem(schemas.Azure, model))
}

func TestSupportsMidConversationSystem_FallbackTakesOver(t *testing.T) {
	// No override → provider gate + substring model gate.
	assert.True(t, SupportsMidConversationSystem(schemas.Anthropic, "claude-opus-4-8-20260601"))
	assert.True(t, SupportsMidConversationSystem(schemas.Anthropic, "claude-fable-5"))
	assert.False(t, SupportsMidConversationSystem(schemas.Anthropic, "claude-opus-4-7-20260401"))
	assert.False(t, SupportsMidConversationSystem(schemas.Vertex, "claude-opus-4-8-20260601"))
}

func TestIsAdaptiveOnlyThinkingModel_OverrideHit(t *testing.T) {
	// supports_sampling_params=false ⇒ adaptive-only. Non-opus name so the
	// substring fallback would return false — override wins.
	model := "non-opus-adaptive-only-yes"
	no := false
	setOverride(t, model, schemas.BifrostOverrides{SupportsSamplingParams: &no})
	assert.True(t, IsAdaptiveOnlyThinkingModel(schemas.Anthropic, model))
}

func TestIsAdaptiveOnlyThinkingModel_OverrideExplicitTrue(t *testing.T) {
	// Opus 4.7 name would fall back true, but supports_sampling_params=true
	// (sampling accepted) ⇒ not adaptive-only.
	model := "claude-opus-4-7-sampling-ok"
	yes := true
	setOverride(t, model, schemas.BifrostOverrides{SupportsSamplingParams: &yes})
	assert.False(t, IsAdaptiveOnlyThinkingModel(schemas.Anthropic, model))
}

func TestIsAdaptiveOnlyThinkingModel_FallbackTakesOver(t *testing.T) {
	// No override → substring: Opus 4.7+/Sonnet 5+/Fable are adaptive-only;
	// Opus 4.6 / Opus 4.5 / Sonnet 4.6 are not.
	assert.True(t, IsAdaptiveOnlyThinkingModel(schemas.Anthropic, "claude-opus-4-8-20260601"))
	assert.True(t, IsAdaptiveOnlyThinkingModel(schemas.Anthropic, "claude-sonnet-5"))
	assert.True(t, IsAdaptiveOnlyThinkingModel(schemas.Anthropic, "claude-fable-5"))
	assert.False(t, IsAdaptiveOnlyThinkingModel(schemas.Anthropic, "claude-opus-4-6-20250514"))
	assert.False(t, IsAdaptiveOnlyThinkingModel(schemas.Anthropic, "claude-opus-4-5-20251101"))
	assert.False(t, IsAdaptiveOnlyThinkingModel(schemas.Anthropic, "claude-sonnet-4-6-20250514"))
}

func TestSupportsFastMode_OverrideHit(t *testing.T) {
	model := "non-opus-test-model-fast-yes"
	yes := true
	setOverride(t, model, schemas.BifrostOverrides{SupportsFastMode: &yes})
	assert.True(t, SupportsFastMode(schemas.Anthropic, model))
}

func TestSupportsFastMode_OverrideExplicitFalse(t *testing.T) {
	// Even on a name that LOOKS like opus 4.6, an explicit false overrides
	// the substring fallback.
	model := "claude-opus-4-6-test-overridden"
	no := false
	setOverride(t, model, schemas.BifrostOverrides{SupportsFastMode: &no})
	assert.False(t, SupportsFastMode(schemas.Anthropic, model))
}

func TestSupportsFastMode_OverrideAbsent_FallbackTakesOver(t *testing.T) {
	assert.True(t, SupportsFastMode(schemas.Anthropic, "claude-opus-4-6-20250514"))
	assert.False(t, SupportsFastMode(schemas.Anthropic, "claude-opus-4-7-20260401"))
}

func TestSupportsAdaptiveThinking_OverrideHit(t *testing.T) {
	model := "non-opus-test-model-adaptive-yes"
	yes := true
	setOverride(t, model, schemas.BifrostOverrides{SupportsAdaptiveThinking: &yes})
	assert.True(t, SupportsAdaptiveThinking(schemas.Anthropic, model))
}

func TestSupportsAdaptiveThinking_OverrideMiss(t *testing.T) {
	// Opus 4.6 name (substring fallback would be true) but an explicit false
	// override wins.
	model := "claude-opus-4-6-overridden"
	no := false
	setOverride(t, model, schemas.BifrostOverrides{SupportsAdaptiveThinking: &no})
	assert.False(t, SupportsAdaptiveThinking(schemas.Anthropic, model))
}

func TestSupportsAdaptiveThinking_OverrideAbsent_FallbackTakesOver(t *testing.T) {
	// Existing TestSupportsAdaptiveThinking already covers this; one
	// targeted assertion here as a smoke check.
	assert.True(t, SupportsAdaptiveThinking(schemas.Anthropic, "claude-opus-4-7-20260401"))
}

// ---- Stage 2C: tool-generation helpers ----

func TestComputerUseGeneration_OverrideNewGen(t *testing.T) {
	model := "non-opus-test-model-computer-new"
	setOverride(t, model, schemas.BifrostOverrides{
		ServerTools: map[string]string{"computer_use": string(AnthropicToolTypeComputer20251124)},
	})
	// Substring fallback would return ComputerUseGen20250124 (no opus/sonnet 4.x match).
	assert.Equal(t, ComputerUseGen20251124, ComputerUseGeneration(schemas.Anthropic, model))
}

func TestComputerUseGeneration_OverrideOldGen(t *testing.T) {
	// Substring fallback for opus-4-6 would say new gen, but override
	// pinning it to the old computer_20250124 wins.
	model := "claude-opus-4-6-overridden-old-computer"
	setOverride(t, model, schemas.BifrostOverrides{
		ServerTools: map[string]string{"computer_use": string(AnthropicToolTypeComputer20250124)},
	})
	assert.Equal(t, ComputerUseGen20250124, ComputerUseGeneration(schemas.Anthropic, model))
}

func TestComputerUseGeneration_OverrideAbsent_FallbackTakesOver(t *testing.T) {
	assert.Equal(t, ComputerUseGen20251124, ComputerUseGeneration(schemas.Anthropic, "claude-opus-4-7-20260401"))
	assert.Equal(t, ComputerUseGen20251124, ComputerUseGeneration(schemas.Anthropic, "claude-sonnet-4-6-20250514"))
	assert.Equal(t, ComputerUseGen20250124, ComputerUseGeneration(schemas.Anthropic, "claude-haiku-4-5-20251001"))
}

func TestTextEditorGeneration_OverrideNewGen(t *testing.T) {
	model := "non-opus-test-model-text-editor-new"
	setOverride(t, model, schemas.BifrostOverrides{
		ServerTools: map[string]string{"text_editor": string(AnthropicToolTypeTextEditor20250728)},
	})
	// Substring fallback would return ComputerUseGen20250124.
	assert.Equal(t, ComputerUseGen20251124, TextEditorGeneration(schemas.Anthropic, model))
}

func TestTextEditorGeneration_OverrideOldGen(t *testing.T) {
	// Substring fallback for opus-4-6 would say new gen; override pinning
	// to text_editor_20250124 wins.
	model := "claude-opus-4-6-overridden-old-text-editor"
	setOverride(t, model, schemas.BifrostOverrides{
		ServerTools: map[string]string{"text_editor": string(AnthropicToolTypeTextEditor20250124)},
	})
	assert.Equal(t, ComputerUseGen20250124, TextEditorGeneration(schemas.Anthropic, model))
}

func TestTextEditorGeneration_OverrideAbsent_FallbackTakesOver(t *testing.T) {
	assert.Equal(t, ComputerUseGen20251124, TextEditorGeneration(schemas.Anthropic, "claude-opus-4-7-20260401"))
	// sonnet-4-5 deliberately uses new-gen text_editor per docstring.
	assert.Equal(t, ComputerUseGen20251124, TextEditorGeneration(schemas.Anthropic, "claude-sonnet-4-5-20241022"))
	assert.Equal(t, ComputerUseGen20250124, TextEditorGeneration(schemas.Anthropic, "claude-3-5-sonnet-20241022"))
}
