package schemas

import (
	"context"
	"slices"
	"testing"
)

// TestResolveModelCapsForRequest pins the request resolution order: rows keyed
// by the request's own provider outrank the base family's, and a custom
// provider with no row of its own falls back to its base type.
func TestResolveModelCapsForRequest(t *testing.T) {
	wireRow := &ModelCapabilities{ReasoningEffortRenames: map[string]string{"none": "off"}}
	baseRow := &ModelCapabilities{ReasoningEffortRenames: map[string]string{"none": "minimal"}}
	// The base row for the custom model's own name: a resolver checking the
	// base provider first must return this, not the wire row.
	competingBaseRow := &ModelCapabilities{ReasoningEffortRenames: map[string]string{"none": "low"}}
	SetCapabilityResolver(func(p ModelProvider, m string) *ModelCapabilities {
		switch {
		case p == "commandcode" && m == "deepseek-v4.1-flash":
			return wireRow
		case p == OpenAI && m == "gpt-5.1":
			return baseRow
		case p == OpenAI && m == "deepseek-v4.1-flash":
			return competingBaseRow
		}
		return nil
	})
	t.Cleanup(func() { SetCapabilityResolver(nil) })

	customCtx := NewBifrostContextWithValue(context.Background(), NoDeadline, BifrostContextKeyBaseProviderType, OpenAI)
	plainCtx := NewBifrostContext(context.Background(), NoDeadline)

	t.Run("custom provider row wins over base family", func(t *testing.T) {
		// Both rows set renames, so the layered record carries the wire row's.
		caps := ResolveModelCapsForRequest(customCtx, "commandcode", "deepseek-v4.1-flash")
		if renamed := caps.NormalizeReasoningEffort("none", nil); renamed != "off" {
			t.Fatalf("expected wire row renames to apply, got %q", renamed)
		}
	})

	t.Run("custom provider without a row falls back to base", func(t *testing.T) {
		caps := ResolveModelCapsForRequest(customCtx, "acme", "gpt-5.1")
		if got := caps.Record(); got != baseRow {
			t.Fatalf("expected the base provider's row, got %v", got)
		}
	})

	t.Run("prefixed model falls back to base on its bare name", func(t *testing.T) {
		// A wire provider whose models carry its own prefix, registered the way
		// bifrost registers every provider key (bifrost.go calls
		// RegisterKnownProvider for custom keys too, so ParseModelString strips
		// the prefix). The base fallback must see the stripped name rather than
		// re-look-up the prefixed form.
		RegisterKnownProvider("acme")
		t.Cleanup(func() { UnregisterKnownProvider("acme") })

		caps := ResolveModelCapsForRequest(customCtx, "acme", "acme/gpt-5.1")
		if got := caps.Record(); got != baseRow {
			t.Fatalf("expected the base provider's row via the stripped model, got %v", got)
		}
	})

	t.Run("standard provider resolves as ResolveModelCaps", func(t *testing.T) {
		a := ResolveModelCapsForRequest(plainCtx, OpenAI, "gpt-5.1")
		b := ResolveModelCaps(OpenAI, "gpt-5.1")
		if a.Record() != b.Record() {
			t.Fatalf("expected identical records for a standard provider")
		}
	})

	t.Run("no ctx keeps wire provider resolution", func(t *testing.T) {
		caps := ResolveModelCapsForRequest(nil, "commandcode", "deepseek-v4.1-flash")
		if got := caps.Record(); got != wireRow {
			t.Fatalf("expected the wire provider's row without a ctx, got %v", got)
		}
	})
}

// TestResolveModelCapsForRequest_CustomRowLayersOverBase pins that a custom
// provider's own row, which a params override builds from its patch alone,
// adds to the base family's row instead of replacing it: the custom row wins
// on the fields it sets, and every field it leaves out still answers from the
// base row.
func TestResolveModelCapsForRequest_CustomRowLayersOverBase(t *testing.T) {
	customRow := &ModelCapabilities{
		ReasoningEffortLevels:  []string{"off", "low", "medium", "high"},
		ReasoningEffortRenames: map[string]string{"none": "off"},
	}
	baseRow := &ModelCapabilities{
		ReasoningEffortLevels:          []string{"none", "low", "medium", "high", "xhigh"},
		MaxOutputTokens:                Ptr(128000),
		SupportsReasoning:              Ptr(true),
		ConditionallyUnsupportedFields: map[string]string{"temperature": "when_effort_not_none"},
	}
	SetCapabilityResolver(func(p ModelProvider, m string) *ModelCapabilities {
		switch {
		case p == "openai_effnone" && m == "gpt-5.4-mini":
			return customRow
		case p == OpenAI && m == "gpt-5.4-mini":
			return baseRow
		}
		return nil
	})
	t.Cleanup(func() { SetCapabilityResolver(nil) })

	ctx := NewBifrostContextWithValue(context.Background(), NoDeadline, BifrostContextKeyBaseProviderType, OpenAI)
	caps := ResolveModelCapsForRequest(ctx, "openai_effnone", "gpt-5.4-mini")

	if got := caps.NormalizeReasoningEffort("none", nil); got != "off" {
		t.Fatalf("custom renames must win, got %q", got)
	}
	if got := caps.ReasoningEffortLevels(nil); len(got) != 4 || got[0] != "off" {
		t.Fatalf("custom levels must win, got %v", got)
	}
	if got := caps.MaxOutputTokens(4096); got != 128000 {
		t.Fatalf("max output tokens must come from the base row, got %d", got)
	}
	if !caps.SupportsReasoning(false) {
		t.Fatalf("supports_reasoning must come from the base row")
	}
	if rec := caps.Record(); rec == nil || rec.ConditionallyUnsupportedFields["temperature"] != "when_effort_not_none" {
		t.Fatalf("conditional field restrictions must come from the base row, got %v", rec)
	}
	// Neither source record is mutated by the layering.
	if customRow.MaxOutputTokens != nil || baseRow.ReasoningEffortRenames != nil {
		t.Fatalf("layering must not write into the source records")
	}
	// The layered record is reused for the same pair of source records, so a
	// request pays a map read, not a rebuild.
	if again := ResolveModelCapsForRequest(ctx, "openai_effnone", "gpt-5.4-mini"); again.Record() != caps.Record() {
		t.Fatalf("expected the cached layered record to be reused")
	}
}

// TestResolveModelCapsForRequest_InheritedRenameTargetsStayOnTheLadder pins that
// a custom row publishing its own effort ladder does not inherit a rename whose
// target that ladder omits: the rename would send an effort the row does not
// publish. The none entry survives because the disabled spelling is not a
// reasoning rung.
func TestResolveModelCapsForRequest_InheritedRenameTargetsStayOnTheLadder(t *testing.T) {
	ownRow := &ModelCapabilities{ReasoningEffortLevels: []string{"low", "high"}}
	baseRow := &ModelCapabilities{
		ReasoningEffortLevels:  []string{"low", "medium", "high", "xhigh"},
		ReasoningEffortRenames: map[string]string{"high": "xhigh", "max": "xhigh", "none": "off"},
	}
	SetCapabilityResolver(func(p ModelProvider, m string) *ModelCapabilities {
		switch {
		case p == "narrow_renames" && m == "gpt-5.4-mini":
			return ownRow
		case p == OpenAI && m == "gpt-5.4-mini":
			return baseRow
		}
		return nil
	})
	t.Cleanup(func() { SetCapabilityResolver(nil) })

	ctx := NewBifrostContextWithValue(context.Background(), NoDeadline, BifrostContextKeyBaseProviderType, OpenAI)
	caps := ResolveModelCapsForRequest(ctx, "narrow_renames", "gpt-5.4-mini")

	if got := caps.NormalizeReasoningEffort("high", nil); got != "high" {
		t.Fatalf("a rename onto a rung the row omits must not apply, got %q", got)
	}
	if got := caps.NormalizeReasoningEffort("max", nil); got != "high" {
		t.Fatalf("a rename onto a rung the row omits must clamp instead, got %q", got)
	}
	if got := caps.NormalizeReasoningEffort("none", nil); got != "off" {
		t.Fatalf("the disabled spelling stays renamed, got %q", got)
	}
	if len(baseRow.ReasoningEffortRenames) != 3 {
		t.Fatalf("the base row's rename table must not be mutated, got %v", baseRow.ReasoningEffortRenames)
	}
}

// TestResolveModelCapsForRequest_LevelsImplyEffortSupport pins the precedence
// between a custom row's ladder and an inherited support flag: publishing an
// effort ladder implies the effort surface, while a flag the row sets itself
// still wins.
func TestResolveModelCapsForRequest_LevelsImplyEffortSupport(t *testing.T) {
	levelsOnly := &ModelCapabilities{ReasoningEffortLevels: []string{"low", "high"}}
	explicitFalse := &ModelCapabilities{
		ReasoningEffortLevels:   []string{"low", "high"},
		SupportsReasoningEffort: Ptr(false),
	}
	baseRow := &ModelCapabilities{SupportsReasoningEffort: Ptr(false)}
	SetCapabilityResolver(func(p ModelProvider, m string) *ModelCapabilities {
		switch {
		case p == "levels_only" && m == "gpt-5.4-mini":
			return levelsOnly
		case p == "levels_explicit_false" && m == "gpt-5.4-mini":
			return explicitFalse
		case p == OpenAI && m == "gpt-5.4-mini":
			return baseRow
		}
		return nil
	})
	t.Cleanup(func() { SetCapabilityResolver(nil) })
	ctx := NewBifrostContextWithValue(context.Background(), NoDeadline, BifrostContextKeyBaseProviderType, OpenAI)

	t.Run("a levels-only custom row implies effort support over the inherited false", func(t *testing.T) {
		caps := ResolveModelCapsForRequest(ctx, "levels_only", "gpt-5.4-mini")
		if !caps.SupportsReasoningEffort(false) {
			t.Fatalf("a custom row that publishes an effort ladder must report effort support")
		}
		if got := caps.ReasoningEffortLevels(nil); len(got) != 2 || got[0] != "low" {
			t.Fatalf("custom levels must survive the layering, got %v", got)
		}
	})

	t.Run("an explicit flag on the custom row wins", func(t *testing.T) {
		caps := ResolveModelCapsForRequest(ctx, "levels_explicit_false", "gpt-5.4-mini")
		if caps.SupportsReasoningEffort(true) {
			t.Fatalf("an explicit supports_reasoning_effort=false on the custom row must win")
		}
	})
}

// TestResolveModelCapsForRequest_OwnRowUnderBasePrefixedName pins that the wire
// provider's own row is still found when the model arrives carrying the base
// family's routing prefix: the prefix names a provider, not a part of the own
// key. The row answers only under the stripped spelling, so the resolution is
// not memoized: the preferred spelling is re-read on the next request.
func TestResolveModelCapsForRequest_OwnRowUnderBasePrefixedName(t *testing.T) {
	ownRow := &ModelCapabilities{ReasoningEffortRenames: map[string]string{"high": "max"}}
	baseRow := &ModelCapabilities{ReasoningEffortLevels: []string{"low", "medium", "high"}}

	ownLookups := 0
	SetCapabilityResolver(func(p ModelProvider, m string) *ModelCapabilities {
		switch {
		case p == "acme" && m == "gpt-5.1":
			ownLookups++
			return ownRow
		case p == OpenAI && m == "openai/gpt-5.1":
			return baseRow
		}
		return nil
	})
	t.Cleanup(func() { SetCapabilityResolver(nil) })

	ctx := NewBifrostContextWithValue(context.Background(), NoDeadline, BifrostContextKeyBaseProviderType, OpenAI)
	caps := ResolveModelCapsForRequest(ctx, "acme", "openai/gpt-5.1")
	if got := caps.NormalizeReasoningEffort("high", nil); got != "max" {
		t.Fatalf("the own row's rename must apply under a base-prefixed model name, got %q", got)
	}

	ResolveModelCapsForRequest(ctx, "acme", "openai/gpt-5.1")
	if ownLookups != 2 {
		t.Fatalf("a resolution whose row answered under a stripped name must not be memoized, own lookups %d", ownLookups)
	}
}

// TestClampReasoningEffort_SinglePublishedLevel pins the snap for a row that
// publishes exactly one level: nothing on the ladder can rank against it, so any
// ranked effort must resolve to that one label instead of passing through.
func TestClampReasoningEffort_SinglePublishedLevel(t *testing.T) {
	single := ModelCaps{record: &ModelCapabilities{ReasoningEffortLevels: []string{"deep"}}}
	if got := single.ClampReasoningEffort("medium", nil); got != "deep" {
		t.Fatalf("a one-level row accepts only its own label, got %q", got)
	}

	ranked := ModelCaps{record: &ModelCapabilities{ReasoningEffortLevels: []string{"low", "high"}}}
	if got := ranked.ClampReasoningEffort("medium", nil); got != "high" {
		t.Fatalf("a ranked ladder keeps the nearest rung, ties upward, got %q", got)
	}

	t.Run("a disabled singleton never swallows an active effort", func(t *testing.T) {
		disabled := ModelCaps{record: &ModelCapabilities{ReasoningEffortLevels: []string{"none"}}}
		if got := disabled.ClampReasoningEffort("medium", nil); got != "medium" {
			t.Fatalf("snapping an active effort onto a disabled label turns reasoning off, got %q", got)
		}
	})

	t.Run("a rename-mapped disabled label is not an active rung", func(t *testing.T) {
		// The row spells "off" as "low": clamping an active effort onto it would
		// silently turn reasoning off.
		disableable := ModelCaps{record: &ModelCapabilities{
			SupportsReasoningDisable: Ptr(true),
			ReasoningEffortRenames:   map[string]string{"none": "low"},
			ReasoningEffortLevels:    []string{"low", "high"},
		}}
		if got := disableable.ClampReasoningEffort("minimal", nil); got != "high" {
			t.Fatalf("a row that permits disabling must not clamp an active effort onto the disabled label, got %q", got)
		}

		alwaysReasons := ModelCaps{record: &ModelCapabilities{
			SupportsReasoningDisable: Ptr(false),
			ReasoningEffortRenames:   map[string]string{"none": "low"},
			ReasoningEffortLevels:    []string{"low", "high"},
		}}
		if got := alwaysReasons.ClampReasoningEffort("minimal", nil); got != "low" {
			t.Fatalf("a row that cannot disable reasoning keeps the label as an active rung, got %q", got)
		}
	})

	t.Run("an unrankable ladder falls back to a published label", func(t *testing.T) {
		custom := ModelCaps{record: &ModelCapabilities{ReasoningEffortLevels: []string{"shallow", "deep"}}}
		got := custom.ClampReasoningEffort("medium", nil)
		if !slices.Contains([]string{"shallow", "deep"}, got) {
			t.Fatalf("the result must be one of the row's published levels, got %q", got)
		}
		if got != "shallow" {
			t.Fatalf("the row's first published level answers, got %q", got)
		}
	})
}

// TestResolveModelCapsForRequest_BaseRowSentSpellingWins pins that the base row is
// resolved under the model name as sent before any stripped spelling: a row the
// base family keys by the fully prefixed name must not lose to the bare row.
func TestResolveModelCapsForRequest_BaseRowSentSpellingWins(t *testing.T) {
	t.Cleanup(func() { SetCapabilityResolver(nil) })
	RegisterKnownProvider("acme")
	t.Cleanup(func() { UnregisterKnownProvider("acme") })

	sentSpellingRow := &ModelCapabilities{MaxOutputTokens: Ptr(8192)}
	bareRow := &ModelCapabilities{MaxOutputTokens: Ptr(4096)}
	SetCapabilityResolver(func(p ModelProvider, m string) *ModelCapabilities {
		switch {
		case p == OpenAI && m == "openai/vendor-model":
			return sentSpellingRow
		case p == OpenAI && m == "vendor-model":
			return bareRow
		}
		return nil
	})
	ctx := NewBifrostContextWithValue(context.Background(), NoDeadline, BifrostContextKeyBaseProviderType, OpenAI)

	caps := ResolveModelCapsForRequest(ctx, "acme", "openai/vendor-model")
	if ceiling := caps.MaxOutputTokens(0); ceiling != 8192 {
		t.Fatalf("the base row keyed by the spelling as sent must win, got %d", ceiling)
	}
}

// TestResolveModelCapsForRequest_BaseRowKeepsModelNamespace pins the base
// lookup's model name: a prefix that names the wire provider is a routing
// prefix for the own row, but it can be a model namespace the base family's
// sheet keys a row by ("meta-llama/Llama-3.1-8B"), so the base lookup must
// still find that namespaced row rather than only the bare name it strips to.
func TestResolveModelCapsForRequest_BaseRowKeepsModelNamespace(t *testing.T) {
	RegisterKnownProvider("meta-llama")
	t.Cleanup(func() { UnregisterKnownProvider("meta-llama") })
	t.Cleanup(func() { SetCapabilityResolver(nil) })

	ownRow := &ModelCapabilities{ReasoningEffortRenames: map[string]string{"none": "off"}}
	namespacedBaseRow := &ModelCapabilities{MaxOutputTokens: Ptr(8192)}
	bareBaseRow := &ModelCapabilities{MaxOutputTokens: Ptr(4096)}
	SetCapabilityResolver(func(p ModelProvider, m string) *ModelCapabilities {
		switch {
		case p == "meta-llama" && m == "Llama-3.1-8B-Instruct":
			return ownRow
		case p == OpenAI && m == "meta-llama/Llama-3.1-8B-Instruct":
			return namespacedBaseRow
		case p == OpenAI && m == "Llama-3.1-8B-Instruct":
			return bareBaseRow
		}
		return nil
	})
	ctx := NewBifrostContextWithValue(context.Background(), NoDeadline, BifrostContextKeyBaseProviderType, OpenAI)

	caps := ResolveModelCapsForRequest(ctx, "meta-llama", "meta-llama/Llama-3.1-8B-Instruct")
	if ceiling := caps.MaxOutputTokens(0); ceiling != 8192 {
		t.Fatalf("the base row keyed by the namespaced id must win over the bare name, got %d", ceiling)
	}
	if effort := caps.NormalizeReasoningEffort("none", nil); effort != "off" {
		t.Fatalf("the custom row's renames must still apply, got %q", effort)
	}
}

// TestResolveModelCapsForRequest_OwnRowKeepsModelNamespace pins the own lookup's
// model name: a custom provider can be named like a model namespace its own
// models carry, so the own row must be reachable under the name as sent rather
// than only under the provider-stripped form.
func TestResolveModelCapsForRequest_OwnRowKeepsModelNamespace(t *testing.T) {
	RegisterKnownProvider("meta-llama")
	t.Cleanup(func() { UnregisterKnownProvider("meta-llama") })
	t.Cleanup(func() { SetCapabilityResolver(nil) })

	ownRow := &ModelCapabilities{MaxOutputTokens: Ptr(8192)}
	baseRow := &ModelCapabilities{MaxOutputTokens: Ptr(4096)}
	SetCapabilityResolver(func(p ModelProvider, m string) *ModelCapabilities {
		switch {
		case p == "meta-llama" && m == "meta-llama/Llama-3.1-8B-Instruct":
			return ownRow
		case p == OpenAI && m == "meta-llama/Llama-3.1-8B-Instruct":
			return baseRow
		}
		return nil
	})
	ctx := NewBifrostContextWithValue(context.Background(), NoDeadline, BifrostContextKeyBaseProviderType, OpenAI)

	caps := ResolveModelCapsForRequest(ctx, "meta-llama", "meta-llama/Llama-3.1-8B-Instruct")
	if ceiling := caps.MaxOutputTokens(0); ceiling != 8192 {
		t.Fatalf("the own row keyed by the configured namespaced id must be reached, got %d", ceiling)
	}
}

// TestResolveModelCapsForRequest_FallbackOwnRowIsNotMemoized pins that an own
// row reached only through the provider-stripped fallback is not memoized: that
// hit may hide a failed load of the preferred spelling, so the next request
// retries it and a row registered under the name as sent is picked up.
func TestResolveModelCapsForRequest_FallbackOwnRowIsNotMemoized(t *testing.T) {
	t.Cleanup(func() { SetCapabilityResolver(nil) })
	RegisterKnownProvider("acme")
	t.Cleanup(func() { UnregisterKnownProvider("acme") })

	fallbackOwnRow := &ModelCapabilities{ReasoningEffortRenames: map[string]string{"none": "off"}}
	sentOwnRow := &ModelCapabilities{ReasoningEffortRenames: map[string]string{"none": "low"}}
	baseRow := &ModelCapabilities{MaxOutputTokens: Ptr(4096)}
	useSentOwn := false
	var calls int
	SetCapabilityResolver(func(p ModelProvider, m string) *ModelCapabilities {
		calls++
		switch {
		case p == "acme" && m == "acme/acme/gpt-5.1":
			if useSentOwn {
				return sentOwnRow
			}
			return nil
		case p == "acme" && m == "gpt-5.1":
			return fallbackOwnRow
		case p == OpenAI && m == "acme/acme/gpt-5.1":
			return baseRow
		}
		return nil
	})
	ctx := NewBifrostContextWithValue(context.Background(), NoDeadline, BifrostContextKeyBaseProviderType, OpenAI)

	first := ResolveModelCapsForRequest(ctx, "acme", "acme/acme/gpt-5.1")
	if got := first.NormalizeReasoningEffort("none", nil); got != "off" {
		t.Fatalf("expected the own row's rename, got %q", got)
	}
	calls = 0
	ResolveModelCapsForRequest(ctx, "acme", "acme/acme/gpt-5.1")
	if calls <= 1 {
		t.Fatalf("an own-row fallback must not be memoized, got %d lookups", calls)
	}

	useSentOwn = true
	got := ResolveModelCapsForRequest(ctx, "acme", "acme/acme/gpt-5.1")
	if renamed := got.NormalizeReasoningEffort("none", nil); renamed != "low" {
		t.Fatalf("a row registered under the name as sent must answer, got %q", renamed)
	}
}

// TestResolveModelCapsForRequest_BareFallbackStripsRepeatedPrefix pins the base
// fallback's progressive strip: a repeated routing prefix must not hide a
// namespaced or bare row, and a fallback-hit base row is never memoized.
func TestResolveModelCapsForRequest_BareFallbackStripsRepeatedPrefix(t *testing.T) {
	t.Cleanup(func() { SetCapabilityResolver(nil) })
	RegisterKnownProvider("acme")
	t.Cleanup(func() { UnregisterKnownProvider("acme") })

	ownRow := &ModelCapabilities{ReasoningEffortRenames: map[string]string{"none": "off"}}
	namespacedBaseRow := &ModelCapabilities{MaxOutputTokens: Ptr(8192)}
	bareBaseRow := &ModelCapabilities{MaxOutputTokens: Ptr(4096)}
	hideNamespaced := false
	var calls int
	SetCapabilityResolver(func(p ModelProvider, m string) *ModelCapabilities {
		calls++
		switch {
		case p == "acme" && m == "acme/acme/gpt-5.1":
			return ownRow
		case p == OpenAI && m == "acme/gpt-5.1":
			if !hideNamespaced {
				return namespacedBaseRow
			}
		case p == OpenAI && m == "gpt-5.1":
			return bareBaseRow
		}
		return nil
	})
	ctx := NewBifrostContextWithValue(context.Background(), NoDeadline, BifrostContextKeyBaseProviderType, OpenAI)

	t.Run("an intermediate namespaced row beats the bare row", func(t *testing.T) {
		if ceiling := ResolveModelCapsForRequest(ctx, "acme", "acme/acme/gpt-5.1").MaxOutputTokens(0); ceiling != 8192 {
			t.Fatalf("the first stripped spelling that answers must win, got %d", ceiling)
		}
	})

	t.Run("a repeated prefix still reaches the bare row, and is not memoized", func(t *testing.T) {
		hideNamespaced = true
		if ceiling := ResolveModelCapsForRequest(ctx, "acme", "acme/acme/gpt-5.1").MaxOutputTokens(0); ceiling != 4096 {
			t.Fatalf("a repeated routing prefix must still reach the bare base row, got %d", ceiling)
		}
		calls = 0
		ResolveModelCapsForRequest(ctx, "acme", "acme/acme/gpt-5.1")
		if calls <= 1 {
			t.Fatalf("a bare-name base fallback must not be memoized, got %d lookups", calls)
		}
	})
}

// TestSetCapabilityResolver_InvalidatesMemo pins that installing a new resolver
// drops the resolution memo: the entry was built from the old base record, and
// the own pointer alone cannot tell the two apart.
func TestSetCapabilityResolver_InvalidatesMemo(t *testing.T) {
	t.Cleanup(func() { SetCapabilityResolver(nil) })

	ownRow := &ModelCapabilities{ReasoningEffortRenames: map[string]string{"none": "off"}}
	baseRowA := &ModelCapabilities{MaxOutputTokens: Ptr(4096)}
	baseRowB := &ModelCapabilities{MaxOutputTokens: Ptr(8192)}
	install := func(base *ModelCapabilities) {
		SetCapabilityResolver(func(p ModelProvider, m string) *ModelCapabilities {
			switch {
			case p == "swap-proxy" && m == "swap-model":
				return ownRow
			case p == OpenAI && m == "swap-model":
				return base
			}
			return nil
		})
	}
	ctx := NewBifrostContextWithValue(context.Background(), NoDeadline, BifrostContextKeyBaseProviderType, OpenAI)

	install(baseRowA)
	if ceiling := ResolveModelCapsForRequest(ctx, "swap-proxy", "swap-model").MaxOutputTokens(0); ceiling != 4096 {
		t.Fatalf("expected the first resolver's base row, got %d", ceiling)
	}
	install(baseRowB)
	if ceiling := ResolveModelCapsForRequest(ctx, "swap-proxy", "swap-model").MaxOutputTokens(0); ceiling != 8192 {
		t.Fatalf("a new resolver must invalidate the memo, got %d", ceiling)
	}
}

// TestResolveModelCapsForRequest_FieldRestrictionsMergeByKey pins that a sparse
// custom row adds to the base family's field restrictions instead of replacing
// them wholesale: a row that clears top_p must not un-reject the base row's
// temperature, an explicit false still wins, and the base maps are never
// written into.
func TestResolveModelCapsForRequest_FieldRestrictionsMergeByKey(t *testing.T) {
	t.Cleanup(func() { SetCapabilityResolver(nil) })

	ownRow := &ModelCapabilities{
		UnsupportedFields:              map[string]bool{FieldTopP: false},
		ConditionallyUnsupportedFields: map[string]string{FieldStop: ConditionWhenEffortNone},
	}
	baseRow := &ModelCapabilities{
		UnsupportedFields:              map[string]bool{FieldTemperature: true, FieldTopP: true},
		ConditionallyUnsupportedFields: map[string]string{FieldLogprobs: ConditionWhenEffortNone},
	}
	SetCapabilityResolver(func(p ModelProvider, m string) *ModelCapabilities {
		switch {
		case p == "openai_sparse" && m == "gpt-5.4-mini":
			return ownRow
		case p == OpenAI && m == "gpt-5.4-mini":
			return baseRow
		}
		return nil
	})
	ctx := NewBifrostContextWithValue(context.Background(), NoDeadline, BifrostContextKeyBaseProviderType, OpenAI)

	caps := ResolveModelCapsForRequest(ctx, "openai_sparse", "gpt-5.4-mini")
	if !caps.FieldUnsupported(FieldTemperature, false) {
		t.Fatalf("the base row's temperature restriction must survive a sparse custom row")
	}
	if caps.FieldUnsupported(FieldTopP, true) {
		t.Fatalf("an explicit false on the custom row must override the inherited restriction")
	}
	rec := caps.Record()
	if rec == nil {
		t.Fatalf("expected a layered record")
	}
	if got := rec.ConditionallyUnsupportedFields[FieldLogprobs]; got != ConditionWhenEffortNone {
		t.Fatalf("the base row's conditional restriction must survive, got %q", got)
	}
	if got := rec.ConditionallyUnsupportedFields[FieldStop]; got != ConditionWhenEffortNone {
		t.Fatalf("the custom row's conditional restriction must apply, got %q", got)
	}
	if !baseRow.UnsupportedFields[FieldTopP] || len(baseRow.ConditionallyUnsupportedFields) != 1 {
		t.Fatalf("layering must not mutate the base row's maps")
	}
}

// TestResolveModelCapsForRequest_CarriesBaseProviderIdentity pins that the
// resolved caps report the BASE provider, not the custom key, so provider-family
// gates downstream (service tier on Bedrock, ProviderFeatures lookups) see the
// family the request actually targets. Returning the wire provider made a custom
// provider fronting Bedrock look like no Bedrock at all.
func TestResolveModelCapsForRequest_CarriesBaseProviderIdentity(t *testing.T) {
	ownRow := &ModelCapabilities{ReasoningEffortRenames: map[string]string{"none": "off"}}
	bedrockRow := &ModelCapabilities{ServiceTiers: []string{"priority"}}
	SetCapabilityResolver(func(p ModelProvider, m string) *ModelCapabilities {
		switch {
		case p == "bedrock-proxy" && m == "anthropic.claude-sonnet-4-5-v1:0":
			return ownRow
		case p == Bedrock && m == "anthropic.claude-sonnet-4-5-v1:0":
			return bedrockRow
		}
		return nil
	})
	t.Cleanup(func() { SetCapabilityResolver(nil) })

	ctx := NewBifrostContextWithValue(context.Background(), NoDeadline, BifrostContextKeyBaseProviderType, Bedrock)

	t.Run("layered custom row reports the base provider", func(t *testing.T) {
		caps := ResolveModelCapsForRequest(ctx, "bedrock-proxy", "anthropic.claude-sonnet-4-5-v1:0")
		if got := caps.Provider(); got != Bedrock {
			t.Fatalf("provider identity must be the base family, got %q", got)
		}
		// The layering still happened: the custom row's renames are present and
		// the base row's tiers survive.
		if got := caps.NormalizeReasoningEffort("none", nil); got != "off" {
			t.Fatalf("custom renames must win, got %q", got)
		}
		if !caps.ServiceTierSupported(BifrostServiceTierPriority, false) {
			t.Fatalf("base row's tiers must survive the layering")
		}
	})

	t.Run("base-row-only fallback reports the base provider", func(t *testing.T) {
		caps := ResolveModelCapsForRequest(ctx, "some-other-custom", "anthropic.claude-sonnet-4-5-v1:0")
		if got := caps.Provider(); got != Bedrock {
			t.Fatalf("provider identity must be the base family, got %q", got)
		}
		if caps.Record() != bedrockRow {
			t.Fatalf("expected the base row, got %v", caps.Record())
		}
	})
}

// TestResolveModelCapsForRequest_MemoInvalidation pins the resolution memo's
// contract against a resolver whose "sheet apply" mints NEW record pointers,
// the way the catalog replaces records wholesale. Only a resolution that found
// both source rows is memoized, and a memoized answer must never outlive the
// rows it was built from — content change or row removed on either side — while
// a steady state pays one lookup rather than the two a cold resolution needs. A
// missing row keeps both lookups, since a nil can be a transient load failure
// the catalog retries on the next request.
func TestResolveModelCapsForRequest_MemoInvalidation(t *testing.T) {
	t.Cleanup(func() { SetCapabilityResolver(nil) })

	// The memo is package state, so a repeated run (-count=2) must start cold
	// or the first case would see a hit from the previous run.
	clearMemos := func() {
		layeredRequestMu.Lock()
		clear(layeredRequestMemos)
		layeredRequestMu.Unlock()
	}
	clearMemos()
	t.Cleanup(clearMemos)

	type sheet struct {
		own  *ModelCapabilities
		base *ModelCapabilities
	}
	var current sheet
	var calls int
	// baseRows answers per-model base rows for the fallback cases; a missing
	// entry falls through to current.base.
	baseRows := map[string]*ModelCapabilities{}
	// failOwn and failBase simulate a transient catalog load failure: the row
	// exists but the first read of it answers nil, the way GetModelCapabilities
	// does when Fill errors (it does not cache the failure).
	failOwn := map[string]bool{}
	failBase := map[string]bool{}
	// onBaseRead runs once inside the base read, which is how a sheet apply
	// lands between the two reads of one resolution.
	var onBaseRead func()
	SetCapabilityResolver(func(p ModelProvider, m string) *ModelCapabilities {
		calls++
		switch p {
		case "memo-proxy":
			if failOwn[m] {
				delete(failOwn, m)
				return nil
			}
			return current.own
		case OpenAI:
			if onBaseRead != nil {
				hook := onBaseRead
				onBaseRead = nil
				hook()
			}
			if failBase[m] {
				delete(failBase, m)
				return nil
			}
			if row, ok := baseRows[m]; ok {
				return row
			}
			return current.base
		}
		return nil
	})

	ctx := NewBifrostContextWithValue(context.Background(), NoDeadline, BifrostContextKeyBaseProviderType, OpenAI)
	// apply replaces the sheet the way the catalog does: every record is
	// re-minted, so no pointer survives. A case that wants to keep one row
	// unchanged passes a shallow copy of its fields rather than the old pointer.
	apply := func(own, base *ModelCapabilities) {
		current = sheet{own: own, base: base}
		baseRows = map[string]*ModelCapabilities{}
		calls = 0
	}
	// lookup resolves a model unique to the case, so cases never share a memo
	// entry and each starts from a cold path.
	lookup := func(model string) ModelCaps {
		return ResolveModelCapsForRequest(ctx, "memo-proxy", model)
	}

	t.Run("steady state pays one lookup", func(t *testing.T) {
		apply(&ModelCapabilities{ReasoningEffortRenames: map[string]string{"none": "off"}},
			&ModelCapabilities{MaxOutputTokens: Ptr(128000)})
		first := lookup("steady")
		if calls != 2 {
			t.Fatalf("cold resolution must pay both lookups, got %d", calls)
		}
		calls = 0
		for range 5 {
			if again := lookup("steady"); again.Record() != first.Record() {
				t.Fatalf("expected the memoized layered record")
			}
		}
		if calls != 5 {
			t.Fatalf("a hit must validate with exactly one lookup each, got %d for 5", calls)
		}
	})

	t.Run("own row content change invalidates", func(t *testing.T) {
		apply(&ModelCapabilities{ReasoningEffortRenames: map[string]string{"none": "off"}},
			&ModelCapabilities{MaxOutputTokens: Ptr(128000)})
		lookup("own-content")
		// New sheet: the own row's content changed, the base row was re-minted.
		apply(&ModelCapabilities{ReasoningEffortRenames: map[string]string{"max": "low"}},
			&ModelCapabilities{MaxOutputTokens: Ptr(128000)})
		calls = 0
		got := lookup("own-content")
		if calls != 3 {
			t.Fatalf("a stale memo pays one validation lookup plus the full two-lookup path, got %d", calls)
		}
		if effort := got.NormalizeReasoningEffort("max", nil); effort != "low" {
			t.Fatalf("expected the new own row's rename, got %q", effort)
		}
	})

	t.Run("base row change invalidates a memoized entry", func(t *testing.T) {
		ownFields := ModelCapabilities{ReasoningEffortRenames: map[string]string{"none": "off"}}
		apply(&ownFields, &ModelCapabilities{MaxOutputTokens: Ptr(128000)})
		lookup("base-change")
		// The own row is content-identical but re-minted, as every sheet apply
		// does; only the base ceiling actually moved.
		ownFieldsCopy := ownFields
		apply(&ownFieldsCopy, &ModelCapabilities{MaxOutputTokens: Ptr(4096)})
		calls = 0
		got := lookup("base-change")
		if calls != 3 {
			t.Fatalf("a stale memo pays one validation lookup plus the full two-lookup path, got %d", calls)
		}
		if ceiling := got.MaxOutputTokens(0); ceiling != 4096 {
			t.Fatalf("expected the new base ceiling, got %d", ceiling)
		}
	})

	t.Run("own row added is picked up — base-only stays uncached", func(t *testing.T) {
		// A base-only answer is never memoized (a nil own row may be a failed
		// load), so the next request resolves in full and sees the own row.
		apply(nil, &ModelCapabilities{MaxOutputTokens: Ptr(128000)})
		lookup("own-added")
		apply(&ModelCapabilities{ReasoningEffortRenames: map[string]string{"none": "off"}},
			&ModelCapabilities{MaxOutputTokens: Ptr(128000)})
		calls = 0
		got := lookup("own-added")
		if calls != 2 {
			t.Fatalf("an uncached base-only answer resolves in full, got %d lookups", calls)
		}
		if effort := got.NormalizeReasoningEffort("none", nil); effort != "off" {
			t.Fatalf("expected the newly-added own row's rename, got %q", effort)
		}
	})

	t.Run("both-absent result is not cached", func(t *testing.T) {
		// Both rows absent leaves no pointer to validate a hit against, so the
		// answer is never memoized: the next request still pays two lookups and
		// sees an own row that appeared in between.
		apply(nil, nil)
		lookup("both-absent")
		calls = 0
		lookup("both-absent")
		if calls != 2 {
			t.Fatalf("both-absent must stay at two lookups, got %d", calls)
		}
		apply(&ModelCapabilities{ReasoningEffortRenames: map[string]string{"none": "off"}}, nil)
		calls = 0
		got := lookup("both-absent")
		if effort := got.NormalizeReasoningEffort("none", nil); effort != "off" {
			t.Fatalf("expected the added own row to apply, got %q", effort)
		}
		if calls != 2 {
			t.Fatalf("the freshly added own row resolves in full, got %d lookups", calls)
		}
	})

	t.Run("own row removed invalidates", func(t *testing.T) {
		apply(&ModelCapabilities{ReasoningEffortRenames: map[string]string{"none": "off"}},
			&ModelCapabilities{MaxOutputTokens: Ptr(128000)})
		lookup("own-removed")
		apply(nil, &ModelCapabilities{MaxOutputTokens: Ptr(128000)})
		calls = 0
		got := lookup("own-removed")
		if calls != 3 {
			t.Fatalf("a stale memo pays one validation lookup plus the full two-lookup path, got %d", calls)
		}
		if effort := got.NormalizeReasoningEffort("none", nil); effort != "none" {
			t.Fatalf("the own row's rename must be gone, got %q", effort)
		}
	})

	t.Run("base row added is picked up — own-only stays uncached", func(t *testing.T) {
		// An own-only answer is never memoized either: the base row may have
		// been a failed load, so the next request retries it.
		ownFields := ModelCapabilities{ReasoningEffortRenames: map[string]string{"none": "off"}}
		apply(&ownFields, nil)
		lookup("base-added")
		ownFieldsCopy := ownFields
		apply(&ownFieldsCopy, &ModelCapabilities{MaxOutputTokens: Ptr(128000)})
		calls = 0
		got := lookup("base-added")
		if calls != 2 {
			t.Fatalf("an uncached own-only answer resolves in full, got %d lookups", calls)
		}
		if ceiling := got.MaxOutputTokens(0); ceiling != 128000 {
			t.Fatalf("expected the new base row's ceiling, got %d", ceiling)
		}
	})

	t.Run("base row removed invalidates", func(t *testing.T) {
		ownFields := ModelCapabilities{ReasoningEffortRenames: map[string]string{"none": "off"}}
		apply(&ownFields, &ModelCapabilities{MaxOutputTokens: Ptr(128000)})
		lookup("base-removed")
		ownFieldsCopy := ownFields
		apply(&ownFieldsCopy, nil)
		calls = 0
		got := lookup("base-removed")
		if calls != 3 {
			t.Fatalf("a stale memo pays one validation lookup plus the full two-lookup path, got %d", calls)
		}
		if ceiling := got.MaxOutputTokens(0); ceiling == 128000 {
			t.Fatalf("the removed base row's ceiling must be gone")
		}
	})

	t.Run("failed own-row load is retried, not memoized", func(t *testing.T) {
		// A nil own row is ambiguous — absent, or a load that failed and will be
		// retried — so the base-only answer is never memoized. Both rows are
		// installed BEFORE the failure, so the retry re-reads the same pointers
		// and only the failed load can explain the recovered own row.
		ownFields := ModelCapabilities{ReasoningEffortRenames: map[string]string{"none": "off"}}
		apply(&ownFields, &ModelCapabilities{MaxOutputTokens: Ptr(128000)})
		failOwn["own-load-fail"] = true
		first := lookup("own-load-fail")
		if ceiling := first.MaxOutputTokens(0); ceiling != 128000 {
			t.Fatalf("the first answer must fall back to the base row, got %d", ceiling)
		}
		if effort := first.NormalizeReasoningEffort("none", nil); effort != "none" {
			t.Fatalf("the failed own row must not rename, got %q", effort)
		}
		calls = 0
		got := lookup("own-load-fail")
		if calls != 2 {
			t.Fatalf("the retried resolution must pay both lookups, got %d", calls)
		}
		if effort := got.NormalizeReasoningEffort("none", nil); effort != "off" {
			t.Fatalf("expected the own row the retry found, got %q", effort)
		}
		calls = 0
		for range 3 {
			if again := lookup("own-load-fail"); again.Record() != got.Record() {
				t.Fatalf("expected the memoized layered record")
			}
		}
		if calls != 3 {
			t.Fatalf("a hit must validate with exactly one lookup each, got %d for 3", calls)
		}
	})

	t.Run("failed base-row load is retried, not memoized", func(t *testing.T) {
		// The mirror case: the own row resolved, so the answer is own-only, but
		// the missing base row may be a failed load the catalog retries. Both
		// rows are installed before the failure, so the retry re-reads the same
		// pointers.
		ownFields := ModelCapabilities{ReasoningEffortRenames: map[string]string{"none": "off"}}
		apply(&ownFields, &ModelCapabilities{MaxOutputTokens: Ptr(128000)})
		failBase["base-load-fail"] = true
		first := lookup("base-load-fail")
		if ceiling := first.MaxOutputTokens(0); ceiling == 128000 {
			t.Fatalf("the first answer must not see the base row that failed to load")
		}
		if effort := first.NormalizeReasoningEffort("none", nil); effort != "off" {
			t.Fatalf("expected the own row's rename, got %q", effort)
		}
		calls = 0
		got := lookup("base-load-fail")
		if calls != 2 {
			t.Fatalf("the retried resolution must pay both lookups, got %d", calls)
		}
		if ceiling := got.MaxOutputTokens(0); ceiling != 128000 {
			t.Fatalf("expected the base row the retry found, got %d", ceiling)
		}
		calls = 0
		for range 3 {
			if again := lookup("base-load-fail"); again.Record() != got.Record() {
				t.Fatalf("expected the memoized layered record")
			}
		}
		if calls != 3 {
			t.Fatalf("a hit must validate with exactly one lookup each, got %d for 3", calls)
		}
	})

	t.Run("reads straddling a sheet apply do not poison the memo", func(t *testing.T) {
		// The apply lands between the own read and the base read, the one window
		// the memo cannot see. Neither answer may be pinned.
		t.Run("own row appearing mid-read is seen next request", func(t *testing.T) {
			apply(nil, &ModelCapabilities{MaxOutputTokens: Ptr(128000)})
			onBaseRead = func() {
				current = sheet{
					own:  &ModelCapabilities{ReasoningEffortRenames: map[string]string{"none": "off"}},
					base: &ModelCapabilities{MaxOutputTokens: Ptr(128000)},
				}
			}
			straddling := lookup("straddle-appear")
			if effort := straddling.NormalizeReasoningEffort("none", nil); effort != "none" {
				t.Fatalf("the straddling request may answer base-only, got %q", effort)
			}
			calls = 0
			got := lookup("straddle-appear")
			if calls != 2 {
				t.Fatalf("a base-only answer is never memoized, got %d lookups", calls)
			}
			if effort := got.NormalizeReasoningEffort("none", nil); effort != "off" {
				t.Fatalf("expected the own row the next request resolves, got %q", effort)
			}
		})

		t.Run("own row re-minted mid-read re-resolves fully", func(t *testing.T) {
			ownFields := ModelCapabilities{ReasoningEffortRenames: map[string]string{"none": "off"}}
			apply(&ownFields, &ModelCapabilities{MaxOutputTokens: Ptr(128000)})
			onBaseRead = func() {
				current = sheet{
					own:  &ModelCapabilities{ReasoningEffortRenames: map[string]string{"max": "low"}},
					base: &ModelCapabilities{MaxOutputTokens: Ptr(4096)},
				}
			}
			lookup("straddle-remint")
			calls = 0
			got := lookup("straddle-remint")
			if calls != 3 {
				t.Fatalf("the own record was re-minted, so the memo must miss: one validation plus two reads, got %d", calls)
			}
			if effort := got.NormalizeReasoningEffort("max", nil); effort != "low" {
				t.Fatalf("expected the post-apply own row, got %q", effort)
			}
			if ceiling := got.MaxOutputTokens(0); ceiling != 4096 {
				t.Fatalf("expected the post-apply base row, got %d", ceiling)
			}
		})
	})

	t.Run("bare-name fallback is not memoized", func(t *testing.T) {
		// The preferred base spelling fails to load and the bare name answers. That
		// hit may be hiding a transient failure, so it must not be memoized: the
		// next request retries the preferred spelling and finds its row.
		own := &ModelCapabilities{ReasoningEffortRenames: map[string]string{"none": "off"}}
		apply(own, nil)
		// The provider prefix must be known for the base lookup to strip it and
		// retry under the bare name, which is the namespace the base sheet keys.
		RegisterKnownProvider("memo-proxy")
		t.Cleanup(func() { UnregisterKnownProvider("memo-proxy") })
		baseRows["meta-llama/Llama-3.1-8B"] = &ModelCapabilities{MaxOutputTokens: Ptr(4096)}
		baseRows["memo-proxy/meta-llama/Llama-3.1-8B"] = &ModelCapabilities{MaxOutputTokens: Ptr(8192)}
		failBase["memo-proxy/meta-llama/Llama-3.1-8B"] = true
		calls = 0
		first := lookup("memo-proxy/meta-llama/Llama-3.1-8B")
		if ceiling := first.MaxOutputTokens(0); ceiling != 4096 {
			t.Fatalf("the fallback must answer while the preferred read fails, got %d", ceiling)
		}
		calls = 0
		got := lookup("memo-proxy/meta-llama/Llama-3.1-8B")
		if calls != 2 {
			t.Fatalf("a fallback-hit answer is not memoized, so the retry pays both lookups, got %d", calls)
		}
		if ceiling := got.MaxOutputTokens(0); ceiling != 8192 {
			t.Fatalf("expected the recovered preferred base row, got %d", ceiling)
		}
	})

	t.Run("standard provider path is untouched", func(t *testing.T) {
		apply(&ModelCapabilities{ReasoningEffortRenames: map[string]string{"none": "off"}}, nil)
		plain := NewBifrostContext(context.Background(), NoDeadline)
		calls = 0
		for range 3 {
			_ = ResolveModelCapsForRequest(plain, OpenAI, "standard")
		}
		if calls != 3 {
			t.Fatalf("a standard provider must pay exactly one lookup per request, got %d for 3", calls)
		}
	})
}

// BenchmarkResolveModelCapsForRequest measures the per-request resolution cost
// for a standard provider, a custom provider inheriting the base row, and a
// custom provider whose own row is layered over the base row.
func BenchmarkResolveModelCapsForRequest(b *testing.B) {
	customRow := &ModelCapabilities{ReasoningEffortRenames: map[string]string{"none": "off"}}
	baseRow := &ModelCapabilities{MaxOutputTokens: Ptr(128000)}
	SetCapabilityResolver(func(p ModelProvider, m string) *ModelCapabilities {
		switch {
		case p == "withrow":
			return customRow
		case p == OpenAI:
			return baseRow
		}
		return nil
	})
	b.Cleanup(func() { SetCapabilityResolver(nil) })
	plain := NewBifrostContext(context.Background(), NoDeadline)
	custom := NewBifrostContextWithValue(context.Background(), NoDeadline, BifrostContextKeyBaseProviderType, OpenAI)
	cases := []struct {
		name     string
		ctx      *BifrostContext
		provider ModelProvider
	}{
		{"standard", plain, OpenAI},
		{"custom_inherits", custom, "norow"},
		{"custom_layered", custom, "withrow"},
	}
	for _, c := range cases {
		b.Run(c.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_ = ResolveModelCapsForRequest(c.ctx, c.provider, "gpt-5.4-mini")
			}
		})
	}
}
