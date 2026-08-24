package anthropic

import (
	"testing"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Override-aware variants of SupportsNativeEffort / SupportsFastMode /
// SupportsAdaptiveThinking. The existing TestSupportsAdaptiveThinking
// (utils_test.go:1996) already covers the substring fallback path.
//
// Each helper looks up its bare model name via providerUtils.GetModelCapabilities.
// We seed the cache directly under the test model key, then assert the
// helper returns the override value (and not the substring fallback).
//
// Models are picked so the substring fallback would return the OPPOSITE
// answer — that way the test only passes when the override path is taken.

// setOverride installs a resolver answering for one (Anthropic, model) pair, so
// the helper under test takes its datasheet branch rather than the substring
// fallback.
func setOverride(t *testing.T, model string, ov schemas.ModelCapabilities) {
	t.Helper()
	providerUtils.SetCapabilityResolver(func(provider schemas.ModelProvider, m string) *schemas.ModelCapabilities {
		if provider == schemas.Anthropic && m == model {
			return &ov
		}
		return nil
	})
	t.Cleanup(func() { providerUtils.SetCapabilityResolver(nil) })
}

func TestSupportsNativeEffort_OverrideHit(t *testing.T) {
	model := "non-claude-test-model-effort-yes"
	yes := true
	setOverride(t, model, schemas.ModelCapabilities{SupportsNativeEffort: &yes})

	// Substring fallback would return false (no "opus") — override wins.
	assert.True(t, SupportsNativeEffort(schemas.ResolveModelCaps(schemas.Anthropic, model)))
}

func TestSupportsNativeEffort_OverrideMiss(t *testing.T) {
	// Explicit false override even though the name would fall back true.
	model := "claude-opus-4-6-effort-overridden"
	no := false
	setOverride(t, model, schemas.ModelCapabilities{SupportsNativeEffort: &no})
	assert.False(t, SupportsNativeEffort(schemas.ResolveModelCaps(schemas.Anthropic, model)))
}

func TestSupportsNativeEffort_OverrideAbsent_FallbackTakesOver(t *testing.T) {
	// Derived = accepts effort AND not adaptive. Opus 4.5 accepts effort and
	// predates adaptive → true. Opus 4.6 accepts effort but is adaptive → false.
	// Haiku doesn't accept effort → false.
	assert.True(t, SupportsNativeEffort(schemas.ResolveModelCaps(schemas.Anthropic, "claude-opus-4-5-20251101")))
	assert.False(t, SupportsNativeEffort(schemas.ResolveModelCaps(schemas.Anthropic, "claude-opus-4-6-20250514")))
	assert.False(t, SupportsNativeEffort(schemas.ResolveModelCaps(schemas.Anthropic, "claude-haiku-4-5-20251001")))
}

func TestSupportsNativeEffort_DerivedFalseWhenAdaptive(t *testing.T) {
	// Accepts effort AND supports adaptive → adaptive wins the ladder, so the
	// derived native-effort helper is false even though effort is accepted.
	model := "effort-and-adaptive-model"
	yes := true
	setOverride(t, model, schemas.ModelCapabilities{
		SupportsNativeEffort:     &yes,
		SupportsAdaptiveThinking: &yes,
	})
	assert.True(t, schemas.ResolveModelCaps(schemas.Anthropic, model).SupportsNativeEffort(DefaultSupportsNativeEffort(model)))
	assert.False(t, SupportsNativeEffort(schemas.ResolveModelCaps(schemas.Anthropic, model)))
}

func TestSupportsEffortParameter_OverrideHit(t *testing.T) {
	model := "non-claude-effort-param-yes"
	yes := true
	setOverride(t, model, schemas.ModelCapabilities{SupportsNativeEffort: &yes})
	// Substring fallback would return false — override wins.
	assert.True(t, schemas.ResolveModelCaps(schemas.Anthropic, model).SupportsNativeEffort(DefaultSupportsNativeEffort(model)))
}

func TestSupportsEffortParameter_OverrideExplicitFalse(t *testing.T) {
	// Opus 4.7 name would fall back true, but an explicit false override wins.
	model := "claude-opus-4-7-effort-overridden"
	no := false
	setOverride(t, model, schemas.ModelCapabilities{SupportsNativeEffort: &no})
	assert.False(t, schemas.ResolveModelCaps(schemas.Anthropic, model).SupportsNativeEffort(DefaultSupportsNativeEffort(model)))
}

func TestSupportsEffortParameter_FallbackTakesOver(t *testing.T) {
	// The effort-supported set per the effort docs; sonnet 4.5 / haiku reject it.
	assert.True(t, schemas.ResolveModelCaps(schemas.Anthropic, "claude-opus-4-8-20260601").SupportsNativeEffort(DefaultSupportsNativeEffort("claude-opus-4-8-20260601")))
	assert.True(t, schemas.ResolveModelCaps(schemas.Anthropic, "claude-opus-4-5-20251101").SupportsNativeEffort(DefaultSupportsNativeEffort("claude-opus-4-5-20251101")))
	assert.True(t, schemas.ResolveModelCaps(schemas.Anthropic, "claude-sonnet-4-6-20250514").SupportsNativeEffort(DefaultSupportsNativeEffort("claude-sonnet-4-6-20250514")))
	assert.False(t, schemas.ResolveModelCaps(schemas.Anthropic, "claude-sonnet-4-5-20250929").SupportsNativeEffort(DefaultSupportsNativeEffort("claude-sonnet-4-5-20250929")))
	assert.False(t, schemas.ResolveModelCaps(schemas.Anthropic, "claude-haiku-4-5-20251001").SupportsNativeEffort(DefaultSupportsNativeEffort("claude-haiku-4-5-20251001")))
}

func TestSupportsMidConversationSystem_OverrideHit(t *testing.T) {
	// Anthropic provider + a non-opus-4.8 name (fallback false) → override wins.
	model := "non-opus-midconv-yes"
	yes := true
	setOverride(t, model, schemas.ModelCapabilities{SupportsMidConversationSystem: &yes})
	assert.True(t, schemas.ResolveModelCaps(schemas.Anthropic, model).SupportsMidConversationSystem(DefaultSupportsMidConversationSystem(schemas.Anthropic, model)))
}

func TestSupportsMidConversationSystem_OverrideExplicitFalse(t *testing.T) {
	// Opus 4.8 name would fall back true, but an explicit false override wins.
	model := "claude-opus-4-8-midconv-off"
	no := false
	setOverride(t, model, schemas.ModelCapabilities{SupportsMidConversationSystem: &no})
	assert.False(t, schemas.ResolveModelCaps(schemas.Anthropic, model).SupportsMidConversationSystem(DefaultSupportsMidConversationSystem(schemas.Anthropic, model)))
}

func TestSupportsMidConversationSystem_ProviderGateWins(t *testing.T) {
	// The hardcoded Anthropic-only provider gate runs before the override, so a
	// non-Anthropic provider stays false even with an explicit true override.
	model := "claude-opus-4-8-gatecheck"
	yes := true
	setOverride(t, model, schemas.ModelCapabilities{SupportsMidConversationSystem: &yes})
	assert.True(t, schemas.ResolveModelCaps(schemas.Anthropic, model).SupportsMidConversationSystem(DefaultSupportsMidConversationSystem(schemas.Anthropic, model)))
	assert.False(t, schemas.ResolveModelCaps(schemas.Vertex, model).SupportsMidConversationSystem(DefaultSupportsMidConversationSystem(schemas.Vertex, model)))
	assert.False(t, schemas.ResolveModelCaps(schemas.Bedrock, model).SupportsMidConversationSystem(DefaultSupportsMidConversationSystem(schemas.Bedrock, model)))
	assert.False(t, schemas.ResolveModelCaps(schemas.Azure, model).SupportsMidConversationSystem(DefaultSupportsMidConversationSystem(schemas.Azure, model)))
}

func TestSupportsMidConversationSystem_FallbackTakesOver(t *testing.T) {
	// No override → provider gate + substring model gate.
	assert.True(t, schemas.ResolveModelCaps(schemas.Anthropic, "claude-opus-4-8-20260601").SupportsMidConversationSystem(DefaultSupportsMidConversationSystem(schemas.Anthropic, "claude-opus-4-8-20260601")))
	assert.True(t, schemas.ResolveModelCaps(schemas.Anthropic, "claude-fable-5").SupportsMidConversationSystem(DefaultSupportsMidConversationSystem(schemas.Anthropic, "claude-fable-5")))
	assert.False(t, schemas.ResolveModelCaps(schemas.Anthropic, "claude-opus-4-7-20260401").SupportsMidConversationSystem(DefaultSupportsMidConversationSystem(schemas.Anthropic, "claude-opus-4-7-20260401")))
	assert.False(t, schemas.ResolveModelCaps(schemas.Vertex, "claude-opus-4-8-20260601").SupportsMidConversationSystem(DefaultSupportsMidConversationSystem(schemas.Vertex, "claude-opus-4-8-20260601")))
}

func TestIsAdaptiveOnlyThinkingModel_OverrideHit(t *testing.T) {
	// supports_sampling_params=false ⇒ adaptive-only. Non-opus name so the
	// substring fallback would return false — override wins.
	model := "non-opus-adaptive-only-yes"
	no := false
	setOverride(t, model, schemas.ModelCapabilities{SupportsSamplingParams: &no})
	assert.True(t, schemas.ResolveModelCaps(schemas.Anthropic, model).AdaptiveOnlyThinking(DefaultAdaptiveOnlyThinking(model)))
}

func TestIsAdaptiveOnlyThinkingModel_OverrideExplicitTrue(t *testing.T) {
	// Opus 4.7 name would fall back true, but supports_sampling_params=true
	// (sampling accepted) ⇒ not adaptive-only.
	model := "claude-opus-4-7-sampling-ok"
	yes := true
	setOverride(t, model, schemas.ModelCapabilities{SupportsSamplingParams: &yes})
	assert.False(t, schemas.ResolveModelCaps(schemas.Anthropic, model).AdaptiveOnlyThinking(DefaultAdaptiveOnlyThinking(model)))
}

func TestIsAdaptiveOnlyThinkingModel_FallbackTakesOver(t *testing.T) {
	// No override → substring: Opus 4.7+/Sonnet 5+/Fable are adaptive-only;
	// Opus 4.6 / Opus 4.5 / Sonnet 4.6 are not.
	assert.True(t, schemas.ResolveModelCaps(schemas.Anthropic, "claude-opus-4-8-20260601").AdaptiveOnlyThinking(DefaultAdaptiveOnlyThinking("claude-opus-4-8-20260601")))
	assert.True(t, schemas.ResolveModelCaps(schemas.Anthropic, "claude-sonnet-5").AdaptiveOnlyThinking(DefaultAdaptiveOnlyThinking("claude-sonnet-5")))
	assert.True(t, schemas.ResolveModelCaps(schemas.Anthropic, "claude-fable-5").AdaptiveOnlyThinking(DefaultAdaptiveOnlyThinking("claude-fable-5")))
	assert.False(t, schemas.ResolveModelCaps(schemas.Anthropic, "claude-opus-4-6-20250514").AdaptiveOnlyThinking(DefaultAdaptiveOnlyThinking("claude-opus-4-6-20250514")))
	assert.False(t, schemas.ResolveModelCaps(schemas.Anthropic, "claude-opus-4-5-20251101").AdaptiveOnlyThinking(DefaultAdaptiveOnlyThinking("claude-opus-4-5-20251101")))
	assert.False(t, schemas.ResolveModelCaps(schemas.Anthropic, "claude-sonnet-4-6-20250514").AdaptiveOnlyThinking(DefaultAdaptiveOnlyThinking("claude-sonnet-4-6-20250514")))
}

func TestSupportsFastMode_OverrideHit(t *testing.T) {
	model := "non-opus-test-model-fast-yes"
	yes := true
	setOverride(t, model, schemas.ModelCapabilities{SupportsFastMode: &yes})
	assert.True(t, schemas.ResolveModelCaps(schemas.Anthropic, model).SupportsFastMode(DefaultSupportsFastMode(model)))
}

func TestSupportsFastMode_OverrideExplicitFalse(t *testing.T) {
	// Even on a name that LOOKS like opus 4.6, an explicit false overrides
	// the substring fallback.
	model := "claude-opus-4-6-test-overridden"
	no := false
	setOverride(t, model, schemas.ModelCapabilities{SupportsFastMode: &no})
	assert.False(t, schemas.ResolveModelCaps(schemas.Anthropic, model).SupportsFastMode(DefaultSupportsFastMode(model)))
}

func TestSupportsFastMode_OverrideAbsent_FallbackTakesOver(t *testing.T) {
	// Fast mode is supported on Opus 4.6 and every Opus 4.7+ generation.
	assert.True(t, schemas.ResolveModelCaps(schemas.Anthropic, "claude-opus-4-6-20250514").SupportsFastMode(DefaultSupportsFastMode("claude-opus-4-6-20250514")))
	assert.True(t, schemas.ResolveModelCaps(schemas.Anthropic, "claude-opus-4-7-20260401").SupportsFastMode(DefaultSupportsFastMode("claude-opus-4-7-20260401")))
	assert.False(t, schemas.ResolveModelCaps(schemas.Anthropic, "claude-sonnet-4-5-20250929").SupportsFastMode(DefaultSupportsFastMode("claude-sonnet-4-5-20250929")))
}

func TestSupportsAdaptiveThinking_OverrideHit(t *testing.T) {
	model := "non-opus-test-model-adaptive-yes"
	yes := true
	setOverride(t, model, schemas.ModelCapabilities{SupportsAdaptiveThinking: &yes})
	assert.True(t, schemas.ResolveModelCaps(schemas.Anthropic, model).SupportsAdaptiveThinking(DefaultSupportsAdaptiveThinking(model)))
}

func TestSupportsAdaptiveThinking_OverrideMiss(t *testing.T) {
	// Opus 4.6 name (substring fallback would be true) but an explicit false
	// override wins.
	model := "claude-opus-4-6-overridden"
	no := false
	setOverride(t, model, schemas.ModelCapabilities{SupportsAdaptiveThinking: &no})
	assert.False(t, schemas.ResolveModelCaps(schemas.Anthropic, model).SupportsAdaptiveThinking(DefaultSupportsAdaptiveThinking(model)))
}

func TestSupportsAdaptiveThinking_OverrideAbsent_FallbackTakesOver(t *testing.T) {
	// Existing TestSupportsAdaptiveThinking already covers this; one
	// targeted assertion here as a smoke check.
	assert.True(t, schemas.ResolveModelCaps(schemas.Anthropic, "claude-opus-4-7-20260401").SupportsAdaptiveThinking(DefaultSupportsAdaptiveThinking("claude-opus-4-7-20260401")))
}

// ---- Stage 2C: tool-generation helpers ----

func TestComputerUseGeneration_OverrideNewGen(t *testing.T) {
	model := "non-opus-test-model-computer-new"
	setOverride(t, model, schemas.ModelCapabilities{
		ServerTools: map[string]string{"computer_use": string(AnthropicToolTypeComputer20251124)},
	})
	// Substring fallback would return ComputerUseGen20250124 (no opus/sonnet 4.x match).
	assert.Equal(t, ComputerUseGen20251124, ComputerUseGeneration(schemas.ResolveModelCaps(schemas.Anthropic, model)))
}

func TestComputerUseGeneration_OverrideOldGen(t *testing.T) {
	// Substring fallback for opus-4-6 would say new gen, but override
	// pinning it to the old computer_20250124 wins.
	model := "claude-opus-4-6-overridden-old-computer"
	setOverride(t, model, schemas.ModelCapabilities{
		ServerTools: map[string]string{"computer_use": string(AnthropicToolTypeComputer20250124)},
	})
	assert.Equal(t, ComputerUseGen20250124, ComputerUseGeneration(schemas.ResolveModelCaps(schemas.Anthropic, model)))
}

func TestComputerUseGeneration_OverrideAbsent_FallbackTakesOver(t *testing.T) {
	assert.Equal(t, ComputerUseGen20251124, ComputerUseGeneration(schemas.ResolveModelCaps(schemas.Anthropic, "claude-opus-4-7-20260401")))
	assert.Equal(t, ComputerUseGen20251124, ComputerUseGeneration(schemas.ResolveModelCaps(schemas.Anthropic, "claude-sonnet-4-6-20250514")))
	assert.Equal(t, ComputerUseGen20250124, ComputerUseGeneration(schemas.ResolveModelCaps(schemas.Anthropic, "claude-haiku-4-5-20251001")))
}

func TestTextEditorGeneration_OverrideNewGen(t *testing.T) {
	model := "non-opus-test-model-text-editor-new"
	setOverride(t, model, schemas.ModelCapabilities{
		ServerTools: map[string]string{"text_editor": string(AnthropicToolTypeTextEditor20250728)},
	})
	// Substring fallback would return ComputerUseGen20250124.
	assert.Equal(t, ComputerUseGen20251124, TextEditorGeneration(schemas.ResolveModelCaps(schemas.Anthropic, model)))
}

func TestTextEditorGeneration_OverrideOldGen(t *testing.T) {
	// Substring fallback for opus-4-6 would say new gen; override pinning
	// to text_editor_20250124 wins.
	model := "claude-opus-4-6-overridden-old-text-editor"
	setOverride(t, model, schemas.ModelCapabilities{
		ServerTools: map[string]string{"text_editor": string(AnthropicToolTypeTextEditor20250124)},
	})
	assert.Equal(t, ComputerUseGen20250124, TextEditorGeneration(schemas.ResolveModelCaps(schemas.Anthropic, model)))
}

func TestTextEditorGeneration_OverrideAbsent_FallbackTakesOver(t *testing.T) {
	assert.Equal(t, ComputerUseGen20251124, TextEditorGeneration(schemas.ResolveModelCaps(schemas.Anthropic, "claude-opus-4-7-20260401")))
	// sonnet-4-5 deliberately uses new-gen text_editor per docstring.
	assert.Equal(t, ComputerUseGen20251124, TextEditorGeneration(schemas.ResolveModelCaps(schemas.Anthropic, "claude-sonnet-4-5-20241022")))
	assert.Equal(t, ComputerUseGen20250124, TextEditorGeneration(schemas.ResolveModelCaps(schemas.Anthropic, "claude-3-5-sonnet-20241022")))
}

// ---- server_tools now drives web_search / web_fetch too ----

// webSearchTypeFor converts a single web_search tool for the model and returns
// the dated Anthropic tool type Bifrost picked.
func webSearchTypeFor(t *testing.T, model string) AnthropicToolType {
	t.Helper()
	caps := schemas.ResolveModelCaps(schemas.Anthropic, model)
	tool := &schemas.ResponsesTool{Type: schemas.ResponsesToolTypeWebSearch}
	out := convertBifrostToolToAnthropic(caps, tool, schemas.Anthropic, false)
	require.NotNil(t, out)
	require.NotNil(t, out.Type)
	return *out.Type
}

func webFetchTypeFor(t *testing.T, model string) AnthropicToolType {
	t.Helper()
	caps := schemas.ResolveModelCaps(schemas.Anthropic, model)
	tool := &schemas.ResponsesTool{Type: schemas.ResponsesToolTypeWebFetch}
	out := convertBifrostToolToAnthropic(caps, tool, schemas.Anthropic, false)
	require.NotNil(t, out)
	require.NotNil(t, out.Type)
	return *out.Type
}

func TestWebSearchVersion_OverrideBeatsNameUpgrade(t *testing.T) {
	// The name fallback would upgrade a 4.6 model to web_search_20260209;
	// an explicit server_tools pin to the GA version must win.
	model := "claude-opus-4-6-pinned-web-search"
	setOverride(t, model, schemas.ModelCapabilities{
		ServerTools: map[string]string{ServerToolWebSearch: string(AnthropicToolTypeWebSearch20250305)},
	})
	assert.Equal(t, AnthropicToolTypeWebSearch20250305, webSearchTypeFor(t, model))
}

func TestWebSearchVersion_OverrideBeatsNameDowngrade(t *testing.T) {
	// The name fallback would leave this model on the GA version; the pin
	// opts it into dynamic filtering.
	model := "some-unrecognised-anthropic-model"
	setOverride(t, model, schemas.ModelCapabilities{
		ServerTools: map[string]string{ServerToolWebSearch: string(AnthropicToolTypeWebSearch20260209)},
	})
	assert.Equal(t, AnthropicToolTypeWebSearch20260209, webSearchTypeFor(t, model))
}

func TestWebSearchVersion_OverrideAbsent_FallbackTakesOver(t *testing.T) {
	assert.Equal(t, AnthropicToolTypeWebSearch20260209, webSearchTypeFor(t, "claude-opus-4-6-20250514"))
	assert.Equal(t, AnthropicToolTypeWebSearch20250305, webSearchTypeFor(t, "claude-haiku-4-5-20251001"))
}

func TestWebFetchVersion_OverrideBeatsNameUpgrade(t *testing.T) {
	model := "claude-opus-4-6-pinned-web-fetch"
	setOverride(t, model, schemas.ModelCapabilities{
		ServerTools: map[string]string{ServerToolWebFetch: string(AnthropicToolTypeWebFetch20250910)},
	})
	assert.Equal(t, AnthropicToolTypeWebFetch20250910, webFetchTypeFor(t, model))
}

func TestWebFetchVersion_OverrideAbsent_FallbackTakesOver(t *testing.T) {
	assert.Equal(t, AnthropicToolTypeWebFetch20260309, webFetchTypeFor(t, "claude-opus-4-6-20250514"))
	assert.Equal(t, AnthropicToolTypeWebFetch20250910, webFetchTypeFor(t, "claude-haiku-4-5-20251001"))
}
