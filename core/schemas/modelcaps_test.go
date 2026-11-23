package schemas

import (
	"context"
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
