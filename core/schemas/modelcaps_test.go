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
		caps := ResolveModelCapsForRequest(customCtx, "commandcode", "deepseek-v4.1-flash")
		if got := caps.Record(); got != wireRow {
			t.Fatalf("expected the wire provider's row, got %v", got)
		}
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
