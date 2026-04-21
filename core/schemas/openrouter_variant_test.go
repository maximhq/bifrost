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
	aliases := KeyAliases{"openai/gpt-5.2": "openai/gpt-5.2-provider-id"}

	if got := aliases.Resolve("openai/gpt-5.2"); got != "openai/gpt-5.2-provider-id" {
		t.Fatalf("direct alias resolution failed: got %q", got)
	}
	if got := aliases.Resolve("openai/gpt-5.2:nitro"); got != "openai/gpt-5.2-provider-id:nitro" {
		t.Fatalf("variant alias resolution failed: got %q", got)
	}
	if got := aliases.Resolve("unknown/model:nitro"); got != "unknown/model:nitro" {
		t.Fatalf("unknown model should return input: got %q", got)
	}
}
