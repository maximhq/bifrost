package schemas

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestModelCapabilities_RoundTrip verifies marshal/unmarshal preserves every
// field. This is the primary safety net against silent field drift between
// the datasheet wire shape and the Go struct.
func TestModelCapabilities_RoundTrip(t *testing.T) {
	truePtr := true
	intPtr := 4096
	str := "converse"
	condStr := "when_effort_none"

	original := ModelCapabilities{
		SupportsCachePoint:             &truePtr,
		SupportsContext1M:              &truePtr,
		ServerTools:                    map[string]string{"web_search": "web_search_20260209"},
		BetaHeaders:                    map[string]string{"compaction": "compact-2026-01-12"},
		FieldNames:                     map[string]string{"max_tokens": "max_completion_tokens"},
		ServerToolAutoInjects:          map[string][]string{"web_search_20260209": {"code_execution_20260120"}},
		ServerToolImplicitBetas:        map[string]string{"memory_20250818": "context-management-2025-06-27"},
		ExtraHeaders:                   map[string]string{"OpenAI-Beta": "realtime=v1"},
		UnsupportedFields:              map[string]bool{"top_p": true, "temperature": true},
		ConditionallyUnsupportedFields: map[string]string{"top_p": condStr},
		SupportsReasoningEffort:        &truePtr,
		ReasoningEffortLevels:          []string{"low", "medium", "high"},
		ReasoningEffortRenames:         map[string]string{"minimal": "low"},
		ReasoningBudget:                &BudgetControl{Min: new(128), Max: new(32768)},
		SupportsReasoningDisable:       &truePtr,
		SupportsDynamicReasoningBudget: &truePtr,
		DefaultMaxTokens:               &intPtr,
		RequestPath:                    &str,
		IsReasoningModel:               &truePtr,
	}

	bytes, err := json.Marshal(original)
	require.NoError(t, err)

	var roundTripped ModelCapabilities
	err = json.Unmarshal(bytes, &roundTripped)
	require.NoError(t, err)

	assert.Equal(t, *original.SupportsCachePoint, *roundTripped.SupportsCachePoint)
	assert.Equal(t, *original.SupportsContext1M, *roundTripped.SupportsContext1M)
	assert.Equal(t, original.ServerTools, roundTripped.ServerTools)
	assert.Equal(t, original.BetaHeaders, roundTripped.BetaHeaders)
	assert.Equal(t, original.FieldNames, roundTripped.FieldNames)
	assert.Equal(t, original.ServerToolAutoInjects, roundTripped.ServerToolAutoInjects)
	assert.Equal(t, original.ServerToolImplicitBetas, roundTripped.ServerToolImplicitBetas)
	assert.Equal(t, original.ExtraHeaders, roundTripped.ExtraHeaders)
	assert.Equal(t, original.UnsupportedFields, roundTripped.UnsupportedFields)
	assert.Equal(t, original.ConditionallyUnsupportedFields, roundTripped.ConditionallyUnsupportedFields)
	assert.Equal(t, *original.SupportsReasoningEffort, *roundTripped.SupportsReasoningEffort)
	assert.Equal(t, original.ReasoningEffortLevels, roundTripped.ReasoningEffortLevels)
	assert.Equal(t, original.ReasoningEffortRenames, roundTripped.ReasoningEffortRenames)
	require.NotNil(t, roundTripped.ReasoningBudget)
	assert.Equal(t, *original.ReasoningBudget.Min, *roundTripped.ReasoningBudget.Min)
	assert.Equal(t, *original.ReasoningBudget.Max, *roundTripped.ReasoningBudget.Max)
	assert.Equal(t, *original.SupportsReasoningDisable, *roundTripped.SupportsReasoningDisable)
	assert.Equal(t, *original.SupportsDynamicReasoningBudget, *roundTripped.SupportsDynamicReasoningBudget)
	assert.Equal(t, *original.DefaultMaxTokens, *roundTripped.DefaultMaxTokens)
	assert.Equal(t, *original.RequestPath, *roundTripped.RequestPath)
	assert.Equal(t, *original.IsReasoningModel, *roundTripped.IsReasoningModel)
}

// TestModelCapabilities_Empty verifies an empty struct serializes to "{}"
// rather than carrying noise into the wire.
func TestModelCapabilities_Empty(t *testing.T) {
	var empty ModelCapabilities
	bytes, err := json.Marshal(empty)
	require.NoError(t, err)
	assert.Equal(t, "{}", string(bytes))
}

// TestModelCapabilities_UnsupportedFieldsParsing verifies the
// `unsupported_fields` map round-trips with explicit boolean values. It is the
// only representation Go reads: the legacy per-field `accepts_*` booleans are
// still emitted on the wire for older clients, and are ignored on decode.
func TestModelCapabilities_UnsupportedFieldsParsing(t *testing.T) {
	wire := `{"unsupported_fields":{"top_p":true,"top_k":true,"temperature":true},"accepts_top_k":false,"accepts_temperature":false}`
	var ov ModelCapabilities
	require.NoError(t, json.Unmarshal([]byte(wire), &ov))
	assert.Equal(t, map[string]bool{"top_p": true, "top_k": true, "temperature": true}, ov.UnsupportedFields)
}

// TestModelCapabilities_AcceptsTopPStringIgnored verifies that the special
// string value `accepts_top_p: "conditional_when_effort_none"` does NOT
// fail unmarshalling. The Go struct has no AcceptsTopP field (the JSON
// decoder ignores unknown fields by default), so the value is silently
// dropped on the Go side; callers should read
// ConditionallyUnsupportedFields["top_p"] for that semantic.
func TestModelCapabilities_AcceptsTopPStringIgnored(t *testing.T) {
	wire := `{"accepts_top_p":"conditional_when_effort_none","conditionally_unsupported_fields":{"top_p":"when_effort_none"}}`
	var ov ModelCapabilities
	require.NoError(t, json.Unmarshal([]byte(wire), &ov))
	assert.Equal(t, "when_effort_none", ov.ConditionallyUnsupportedFields["top_p"])
}

// setCapabilityOverride installs a resolver answering for one (Anthropic, model)
// pair, so the method under test takes its datasheet branch rather than the
// name-based fallback.
func setCapabilityOverride(t *testing.T, model string, ov ModelCapabilities) {
	t.Helper()
	SetCapabilityResolver(func(_ ModelProvider, m string) *ModelCapabilities {
		if m == model {
			return &ov
		}
		return nil
	})
	t.Cleanup(func() { SetCapabilityResolver(nil) })
}

// ModelCaps.SupportsFastMode has to answer identically on every branch —
// datasheet hit, explicit false, and the name-based fallback. Models are picked
// so the fallback would return the OPPOSITE answer to the override.
func TestModelCaps_SupportsFastMode(t *testing.T) {
	t.Run("OverrideHit", func(t *testing.T) {
		model := "non-opus-test-model-fast-yes"
		yes := true
		setCapabilityOverride(t, model, ModelCapabilities{SupportsFastMode: &yes})
		assert.True(t, ResolveModelCaps(Anthropic, model).SupportsFastMode(false))
	})

	t.Run("OverrideExplicitFalse", func(t *testing.T) {
		model := "claude-opus-4-6-test-overridden"
		no := false
		setCapabilityOverride(t, model, ModelCapabilities{SupportsFastMode: &no})
		assert.False(t, ResolveModelCaps(Anthropic, model).SupportsFastMode(false))
	})

	// Name detection lives with the Anthropic package now (TestSupportsFastMode
	// there covers the model list); schemas only decides record-vs-fallback.
	t.Run("OverrideAbsent_CallerFallbackTakesOver", func(t *testing.T) {
		assert.True(t, ResolveModelCaps(Anthropic, "claude-opus-4-6-20250514").SupportsFastMode(true))
		assert.False(t, ResolveModelCaps(Anthropic, "claude-opus-4-6-20250514").SupportsFastMode(false))
	})

	t.Run("ZeroValue", func(t *testing.T) {
		// Reached from response-direction converters, which have no model.
		var caps ModelCaps
		assert.False(t, caps.SupportsFastMode(false))
		assert.Equal(t, "", caps.Model())
		assert.Nil(t, caps.Record())
	})
}

// A row's effort ladder is taken verbatim over the caller's name-based fallback
// and the base set — narrowing included, which is what the per-level booleans
// this replaced could not express.
func TestModelCaps_ReasoningEffortLevels(t *testing.T) {
	t.Run("RowLadderWins", func(t *testing.T) {
		model := "some-vendor-reasoner"
		setCapabilityOverride(t, model, ModelCapabilities{
			ReasoningEffortLevels: []string{"minimal", "low", "medium", "high", "xhigh"},
		})
		levels := ResolveModelCaps(Anthropic, model).ReasoningEffortLevels(nil)
		assert.Equal(t, []string{"minimal", "low", "medium", "high", "xhigh"}, levels)
	})

	t.Run("RowCanNarrowBelowBaseSet", func(t *testing.T) {
		// Gemini 3 Pro accepts low/high only; base low/medium/high must not leak back in.
		model := "gemini-pro-like"
		setCapabilityOverride(t, model, ModelCapabilities{
			ReasoningEffortLevels: []string{"low", "high"},
		})
		assert.Equal(t, []string{"low", "high"}, ResolveModelCaps(Gemini, model).ReasoningEffortLevels(nil))
	})

	t.Run("FeedBeatsNameDetection", func(t *testing.T) {
		// gpt-5.1's name would yield no xhigh; an explicit row wins.
		model := "gpt-5.1"
		setCapabilityOverride(t, model, ModelCapabilities{
			ReasoningEffortLevels: []string{"low", "medium", "high", "xhigh"},
		})
		caps := ResolveModelCaps(OpenAI, model)
		assert.Contains(t, caps.ReasoningEffortLevels(nil), ReasoningEffortXHigh)
		assert.Equal(t, ReasoningEffortXHigh, caps.NormalizeReasoningEffort(ReasoningEffortXHigh, nil))
	})

	t.Run("LadderGrantsMaxWithoutDowngrade", func(t *testing.T) {
		// Listing max is what grants it; a requested "max" must survive intact
		// rather than fall to xhigh.
		model := "gpt-5.6-like"
		setCapabilityOverride(t, model, ModelCapabilities{
			ReasoningEffortLevels: []string{"low", "medium", "high", "xhigh", "max"},
		})
		caps := ResolveModelCaps(OpenAI, model)
		assert.Equal(t, ReasoningEffortMax, caps.NormalizeReasoningEffort(ReasoningEffortMax, nil))
	})

	t.Run("RenamesWinOverLadder", func(t *testing.T) {
		model := "gemini-pro-renames"
		setCapabilityOverride(t, model, ModelCapabilities{
			ReasoningEffortLevels:  []string{"low", "high"},
			ReasoningEffortRenames: map[string]string{"medium": "high", "minimal": "low"},
		})
		caps := ResolveModelCaps(Gemini, model)
		assert.Equal(t, ReasoningEffortHigh, caps.NormalizeReasoningEffort(ReasoningEffortMedium, nil))
		assert.Equal(t, ReasoningEffortLow, caps.NormalizeReasoningEffort(ReasoningEffortMinimal, nil))
	})
}

// Every reasoning key answers on its own. A row seeding one must not implicitly
// assert anything about the others — that independence is what makes a
// partially-populated sheet safe.
func TestModelCaps_ReasoningKeysAreIndependent(t *testing.T) {
	no := false

	t.Run("DisableKeyAloneLeavesEffortToFallback", func(t *testing.T) {
		model := "partially-seeded-model"
		setCapabilityOverride(t, model, ModelCapabilities{SupportsReasoningDisable: &no})
		caps := ResolveModelCaps(Anthropic, model)
		assert.False(t, caps.CanDisableReasoning(true))
		assert.True(t, caps.SupportsReasoningEffort(true))
		assert.False(t, caps.SupportsReasoningEffort(false))
	})

	t.Run("LadderImpliesEffortSurface", func(t *testing.T) {
		model := "ladder-only-model"
		setCapabilityOverride(t, model, ModelCapabilities{
			ReasoningEffortLevels: []string{"low", "high"},
		})
		assert.True(t, ResolveModelCaps(Gemini, model).SupportsReasoningEffort(false))
	})

	t.Run("ExplicitFalseDeniesEffortSurface", func(t *testing.T) {
		// A budget-only model (Gemini 2.5) takes no effort label at all.
		model := "budget-only-model"
		setCapabilityOverride(t, model, ModelCapabilities{SupportsReasoningEffort: &no})
		assert.False(t, ResolveModelCaps(Gemini, model).SupportsReasoningEffort(true))
	})

	t.Run("DynamicBudgetDefaultsToFallback", func(t *testing.T) {
		model := "dynamic-budget-model"
		setCapabilityOverride(t, model, ModelCapabilities{SupportsDynamicReasoningBudget: &no})
		caps := ResolveModelCaps(Gemini, model)
		assert.False(t, caps.SupportsDynamicReasoningBudget(true))
		assert.True(t, ResolveModelCaps(Gemini, "unseeded-model").SupportsDynamicReasoningBudget(true))
	})
}

// Budget ranges drive Gemini's thinkingBudget validation; ok=false means the
// caller skips validation entirely rather than enforcing a default.
func TestModelCaps_ReasoningBudgetRange(t *testing.T) {
	// The published per-model ranges now live with their provider; the Gemini
	// package covers them end-to-end. Here we only pin the precedence.
	t.Run("CallerFallbackUsedWhenRowIsSilent", func(t *testing.T) {
		lo, hi := 512, 24576
		min, max, ok := ResolveModelCaps(Gemini, "gemini-2.5-flash-lite").
			ReasoningBudgetRange(8192, &BudgetControl{Min: &lo, Max: &hi})
		assert.True(t, ok)
		assert.Equal(t, 512, min)
		assert.Equal(t, 24576, max)
	})

	t.Run("NoRowAndNoFallbackSkipsValidation", func(t *testing.T) {
		_, _, ok := ResolveModelCaps(Gemini, "gemini-9.9-flash").ReasoningBudgetRange(8192, nil)
		assert.False(t, ok)
	})

	t.Run("DatasheetWins", func(t *testing.T) {
		model := "gemini-2.5-pro"
		lo, hi := 64, 4096
		setCapabilityOverride(t, model, ModelCapabilities{
			ReasoningBudget: &BudgetControl{Min: &lo, Max: &hi},
		})
		min, max, ok := ResolveModelCaps(Anthropic, model).ReasoningBudgetRange(8192, nil)
		assert.True(t, ok)
		assert.Equal(t, 64, min)
		assert.Equal(t, 4096, max)
	})
}

func TestModelCaps_SupportsReasoning(t *testing.T) {
	// Name detection belongs to each provider package now — schemas holds no
	// model vocabulary, so it only decides record-vs-fallback precedence.
	t.Run("CallerFallbackUsedWhenRowIsSilent", func(t *testing.T) {
		assert.True(t, ResolveModelCaps(OpenAI, "some-model").SupportsReasoning(true))
		assert.False(t, ResolveModelCaps(OpenAI, "some-model").SupportsReasoning(false))
	})

	t.Run("DatasheetWins", func(t *testing.T) {
		model := "gpt-4o"
		yes := true
		setCapabilityOverride(t, model, ModelCapabilities{SupportsReasoning: &yes})
		assert.True(t, ResolveModelCaps(OpenAI, model).SupportsReasoning(false))
	})
}

// A caller reaching capability resolution with the wire model may still carry
// the provider prefix. Left in, it keys a second cache entry for the same model
// and hands the name-based fallbacks a string they cannot match on.
func TestResolveModelCaps_NormalizesRepeatedProviderPrefix(t *testing.T) {
	tests := []struct {
		name     string
		provider ModelProvider
		model    string
		want     string
	}{
		{"bare model untouched", XAI, "grok-4-0709", "grok-4-0709"},
		{"repeated prefix stripped", XAI, "xai/grok-4-0709", "grok-4-0709"},
		{"repeated prefix stripped (gemini)", Gemini, "gemini/gemini-2.5-flash", "gemini-2.5-flash"},
		{"repeated prefix stripped (openai)", OpenAI, "openai/gpt-5.2", "gpt-5.2"},
		// A different provider's prefix is a real routing distinction.
		{"cross-provider prefix kept", OpenRouter, "openai/gpt-5.1", "openai/gpt-5.1"},
		// Namespaces that merely look like a prefix must survive.
		{"model namespace kept", Bedrock, "meta-llama/Llama-3.1-8B", "meta-llama/Llama-3.1-8B"},
		// bedrock_mantle is its own provider, so "bedrock/" is not its prefix.
		{"sibling provider prefix kept", BedrockMantle, "bedrock/anthropic.claude-opus-5", "bedrock/anthropic.claude-opus-5"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ResolveModelCaps(tt.provider, tt.model).Model(); got != tt.want {
				t.Errorf("ResolveModelCaps(%q, %q).Model() = %q, want %q", tt.provider, tt.model, got, tt.want)
			}
		})
	}
}
