package schemas

import "testing"

func TestSplitOpenRouterVariant(t *testing.T) {
	tests := []struct {
		name        string
		model       string
		wantBase    string
		wantVariant string
	}{
		{"nitro", "openai/gpt-5.2:nitro", "openai/gpt-5.2", "nitro"},
		{"floor", "openai/gpt-5.2:floor", "openai/gpt-5.2", "floor"},
		{"online", "anthropic/claude-3.7:online", "anthropic/claude-3.7", "online"},
		{"thinking", "deepseek/deepseek-r1:thinking", "deepseek/deepseek-r1", "thinking"},
		{"exacto", "openai/gpt-5.2:exacto", "openai/gpt-5.2", "exacto"},
		{"extended", "anthropic/claude-sonnet-4.5:extended", "anthropic/claude-sonnet-4.5", "extended"},
		{"free", "meta-llama/llama-3.2-3b-instruct:free", "meta-llama/llama-3.2-3b-instruct", "free"},
		{"case insensitive", "openai/gpt-5.2:NITRO", "openai/gpt-5.2", "nitro"},
		{"no variant", "openai/gpt-5.2", "openai/gpt-5.2", ""},
		{"unknown suffix preserved", "openai/gpt-5.2:foobar", "openai/gpt-5.2:foobar", ""},
		{"colon only", "openai/gpt-5.2:", "openai/gpt-5.2:", ""},
		{"empty", "", "", ""},
		{"bedrock-like id untouched", "anthropic.claude-sonnet-4-v1:0", "anthropic.claude-sonnet-4-v1:0", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotBase, gotVariant := SplitOpenRouterVariant(tc.model)
			if gotBase != tc.wantBase || gotVariant != tc.wantVariant {
				t.Fatalf("SplitOpenRouterVariant(%q) = (%q, %q), want (%q, %q)",
					tc.model, gotBase, gotVariant, tc.wantBase, tc.wantVariant)
			}
		})
	}
}

func TestBaseOpenRouterModel(t *testing.T) {
	if got := BaseOpenRouterModel("openai/gpt-5.2:nitro"); got != "openai/gpt-5.2" {
		t.Fatalf("expected base openai/gpt-5.2, got %q", got)
	}
	if got := BaseOpenRouterModel("openai/gpt-5.2"); got != "openai/gpt-5.2" {
		t.Fatalf("expected passthrough, got %q", got)
	}
}

func TestWhiteListAllowsBaseViaOpenRouterVariant(t *testing.T) {
	wl := WhiteList{"openai/gpt-5.2", "anthropic/claude-3.7-sonnet"}

	// Exact match still works.
	if !wl.IsAllowed("openai/gpt-5.2") {
		t.Fatal("base model should be allowed")
	}

	// Variant must resolve via SplitOpenRouterVariant at the dispatch site,
	// not inside WhiteList itself (which remains provider-agnostic).
	if wl.IsAllowed("openai/gpt-5.2:nitro") {
		t.Fatal("WhiteList.IsAllowed should NOT match variant directly; variant-awareness is the caller's responsibility")
	}

	// But the helper lets the caller fall back to base.
	base := BaseOpenRouterModel("openai/gpt-5.2:nitro")
	if !wl.IsAllowed(base) {
		t.Fatalf("variant-stripped base %q should be allowed", base)
	}
}

func TestKeyAliasesResolveOpenRouterVariant(t *testing.T) {
	base := KeyAliases{"openai/gpt-5.2": "openai/gpt-5.2-provider-id"}

	tests := []struct {
		name    string
		aliases KeyAliases
		input   string
		want    string
	}{
		{"direct base match", base, "openai/gpt-5.2", "openai/gpt-5.2-provider-id"},
		{"variant via base fallback", base, "openai/gpt-5.2:nitro", "openai/gpt-5.2-provider-id:nitro"},
		{"variant case insensitive", base, "openai/gpt-5.2:NITRO", "openai/gpt-5.2-provider-id:nitro"},
		{"case-insensitive base match", base, "OpenAI/GPT-5.2:free", "openai/gpt-5.2-provider-id:free"},
		{"unknown variant suffix passes through", base, "openai/gpt-5.2:foobar", "openai/gpt-5.2:foobar"},
		{"empty trailing colon passes through", base, "openai/gpt-5.2:", "openai/gpt-5.2:"},
		{"unknown model returns input", base, "unknown/model:nitro", "unknown/model:nitro"},
		{"nil aliases returns input", nil, "openai/gpt-5.2:nitro", "openai/gpt-5.2:nitro"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.aliases.Resolve(tc.input); got != tc.want {
				t.Fatalf("Resolve(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// TestKeyAliasesResolveExactVariantWins pins down that an alias configured for
// the exact variant slug takes precedence over the base-model fallback.
func TestKeyAliasesResolveExactVariantWins(t *testing.T) {
	aliases := KeyAliases{
		"openai/gpt-5.2":       "base-target",
		"openai/gpt-5.2:nitro": "custom-target",
	}
	if got := aliases.Resolve("openai/gpt-5.2:nitro"); got != "custom-target" {
		t.Fatalf("exact variant alias should win, got %q", got)
	}
	if got := aliases.Resolve("openai/gpt-5.2:free"); got != "base-target:free" {
		t.Fatalf("variant without exact alias must fall back to base, got %q", got)
	}
}

// TestKeyAliasesResolveAvoidsDoubleVariant pins down that if the alias target
// already carries a variant suffix, Resolve strips it before re-appending the
// request variant, so we never produce "foo:nitro:nitro".
func TestKeyAliasesResolveAvoidsDoubleVariant(t *testing.T) {
	aliases := KeyAliases{"openai/gpt-5.2": "provider-id:free"}
	if got := aliases.Resolve("openai/gpt-5.2:nitro"); got != "provider-id:nitro" {
		t.Fatalf("double-variant should be stripped, got %q", got)
	}
}
