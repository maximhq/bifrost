package utils

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func capsWithMax(v int) *schemas.ModelCapabilities {
	return &schemas.ModelCapabilities{MaxOutputTokens: new(v)}
}

// withResolver installs a capability resolver for the duration of a test.
func withResolver(t *testing.T, fn func(schemas.ModelProvider, string) *schemas.ModelCapabilities) {
	t.Helper()
	SetCapabilityResolver(fn)
	t.Cleanup(func() { SetCapabilityResolver(nil) })
}

// Core holds no capability state of its own: with nothing installed every
// lookup misses so callers fall back to their own detection. That is what keeps
// a catalog-less deployment working.
func TestCapabilitiesFor_NoResolverInstalled(t *testing.T) {
	SetCapabilityResolver(nil)
	if got := CapabilitiesFor(schemas.Anthropic, "claude-opus-4-7"); got != nil {
		t.Errorf("expected nil without a resolver, got %+v", got)
	}
}

func TestCapabilitiesFor_DelegatesProviderAndModel(t *testing.T) {
	type ask struct {
		provider schemas.ModelProvider
		model    string
	}
	var asked []ask
	withResolver(t, func(p schemas.ModelProvider, m string) *schemas.ModelCapabilities {
		asked = append(asked, ask{p, m})
		return capsWithMax(16384)
	})

	got := CapabilitiesFor(schemas.Azure, "gpt-5.1-chat")
	if got == nil || *got.MaxOutputTokens != 16384 {
		t.Fatalf("expected the resolver's record, got %+v", got)
	}
	if len(asked) != 1 || asked[0] != (ask{schemas.Azure, "gpt-5.1-chat"}) {
		t.Errorf("expected one (azure, gpt-5.1-chat) lookup, got %+v", asked)
	}
}

// An empty model short-circuits before the resolver: there is nothing to look up
// and the datasheet has no row for "".
func TestCapabilitiesFor_EmptyModelSkipsResolver(t *testing.T) {
	called := false
	withResolver(t, func(schemas.ModelProvider, string) *schemas.ModelCapabilities {
		called = true
		return capsWithMax(1)
	})

	if got := CapabilitiesFor(schemas.Anthropic, ""); got != nil {
		t.Errorf("expected nil for an empty model, got %+v", got)
	}
	if called {
		t.Error("resolver must not be consulted for an empty model")
	}
}

func TestGetMaxOutputTokensOrDefault(t *testing.T) {
	withResolver(t, func(p schemas.ModelProvider, m string) *schemas.ModelCapabilities {
		if p == schemas.Anthropic && m == "test-or-default" {
			return capsWithMax(16384)
		}
		return nil
	})

	if got := GetMaxOutputTokensOrDefault(schemas.Anthropic, "test-or-default", 4096); got != 16384 {
		t.Errorf("expected the record's ceiling 16384, got %d", got)
	}
	if got := GetMaxOutputTokensOrDefault(schemas.OpenAI, "missing-model-default", 4096); got != 4096 {
		t.Errorf("expected default 4096 for a model with no record, got %d", got)
	}
}

// Claude requires max_tokens on every request, so a model with no record still
// resolves through the static table rather than the caller's generic default.
func TestGetMaxOutputTokensOrDefault_ClaudeStaticFallback(t *testing.T) {
	SetCapabilityResolver(nil)
	if got := GetMaxOutputTokensOrDefault(schemas.Bedrock, "us.anthropic.claude-opus-4-6-v1:0", 4096); got != 128000 {
		t.Errorf("expected static 128000 for claude-opus-4-6, got %d", got)
	}
}

func TestIsVertexMultiRegionOnlyModel(t *testing.T) {
	yes := true
	withResolver(t, func(p schemas.ModelProvider, m string) *schemas.ModelCapabilities {
		if p == schemas.Vertex && m == "claude-opus-4-7" {
			return &schemas.ModelCapabilities{IsVertexMultiRegionOnly: &yes}
		}
		return nil
	})

	if !IsVertexMultiRegionOnlyModel("claude-opus-4-7") {
		t.Error("expected the flagged model to report multi-region-only")
	}
	if IsVertexMultiRegionOnlyModel("claude-sonnet-4-5") {
		t.Error("expected an unflagged model to report false")
	}
}

func TestNormalizeClaudeModelName(t *testing.T) {
	tests := []struct {
		input    string
		expected string
		desc     string
	}{
		// Anthropic direct (bare model names)
		{"claude-sonnet-4-5", "claude-sonnet-4-5", "Anthropic: no version suffix"},
		{"claude-sonnet-4-20250514", "claude-sonnet-4", "Anthropic: date suffix"},
		{"claude-opus-4-5", "claude-opus-4-5", "Anthropic: no version suffix"},
		{"claude-opus-4-6-20250514", "claude-opus-4-6", "Anthropic: date suffix"},
		{"claude-sonnet-4-6", "claude-sonnet-4-6", "Anthropic: no version suffix"},
		{"claude-3-5-sonnet-20241022", "claude-3-5-sonnet", "Anthropic: legacy date suffix"},
		{"claude-3-7-sonnet-20250219", "claude-3-7-sonnet", "Anthropic: legacy date suffix"},

		// Bedrock (anthropic. prefix + -v1:0 suffix)
		{"anthropic.claude-3-sonnet-20240229-v1:0", "claude-3-sonnet", "Bedrock: prefix + v1:0"},
		{"anthropic.claude-opus-4-6-v1", "claude-opus-4-6", "Bedrock: prefix + v1 no colon"},
		{"anthropic.claude-3-7-sonnet-v1", "claude-3-7-sonnet", "Bedrock: prefix + v1 no colon"},
		{"anthropic.claude-sonnet-4-20250514-v1:0", "claude-sonnet-4", "Bedrock: prefix + date + v1:0"},
		{"anthropic.claude-3-5-sonnet-20241022-v1:0", "claude-3-5-sonnet", "Bedrock: prefix + legacy date + v1:0"},

		// Bedrock with region prefix
		{"us.anthropic.claude-sonnet-4-6", "claude-sonnet-4-6", "Bedrock regional: us prefix"},
		{"us.anthropic.claude-3-sonnet-20240229-v1:0", "claude-3-sonnet", "Bedrock regional: us + v1:0"},
		{"global.anthropic.claude-opus-4-6-20260301-v1:0", "claude-opus-4-6", "Bedrock regional: global + date + v1:0"},
		{"eu.anthropic.claude-sonnet-4-5-20250929-v1:0", "claude-sonnet-4-5", "Bedrock regional: eu + date + v1:0"},

		// Vertex (same as Anthropic direct — deployment is bare model name)
		{"claude-sonnet-4-5", "claude-sonnet-4-5", "Vertex: bare model"},
		{"claude-sonnet-4-20250514", "claude-sonnet-4", "Vertex: date suffix"},

		// Azure (deployment names — typically bare model names)
		{"claude-opus-4-5", "claude-opus-4-5", "Azure: deployment name"},
	}

	for _, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			got := normalizeClaudeModelName(tt.input)
			if got != tt.expected {
				t.Errorf("normalizeClaudeModelName(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestGetMaxOutputTokensOrDefaultStaticFallback(t *testing.T) {
	// Use a fresh cache with no entries to test static fallback only
	// We test via the normalizeClaudeModelName + map lookup directly
	// since the global cache may have entries from other tests
	tests := []struct {
		model    string
		expected int
		desc     string
	}{
		// Anthropic direct
		{"claude-sonnet-4-20250514", 64000, "Anthropic: claude-sonnet-4"},
		{"claude-opus-4-6-20250514", 128000, "Anthropic: claude-opus-4-6"},
		{"claude-3-5-sonnet-20241022", 8192, "Anthropic: claude-3-5-sonnet"},

		// Bedrock
		{"anthropic.claude-sonnet-4-20250514-v1:0", 64000, "Bedrock: claude-sonnet-4"},
		{"anthropic.claude-opus-4-6-v1", 128000, "Bedrock: claude-opus-4-6"},
		{"anthropic.claude-3-5-sonnet-20241022-v1:0", 8192, "Bedrock: claude-3-5-sonnet"},

		// Bedrock with region prefix
		{"us.anthropic.claude-opus-4-6-v1:0", 128000, "Bedrock regional: claude-opus-4-6"},
		{"global.anthropic.claude-sonnet-4-5-20250929-v1:0", 64000, "Bedrock regional: claude-sonnet-4-5"},
		{"eu.anthropic.claude-3-haiku-20240307-v1:0", 4096, "Bedrock regional: claude-3-haiku"},

		// Vertex
		{"claude-opus-4-5", 64000, "Vertex: claude-opus-4-5"},
		{"claude-haiku-4-5", 64000, "Vertex: claude-haiku-4-5"},

		// Azure
		{"claude-3-5-sonnet-20241022", 8192, "Azure: claude-3-5-sonnet"},
		{"claude-sonnet-4-6", 64000, "Azure: claude-sonnet-4-6"},

		// Non-Claude models should return the default
		{"gpt-4o", 4096, "Non-Claude: gpt-4o"},
		{"gemini-2.0-flash", 4096, "Non-Claude: gemini-2.0-flash"},
		{"command-r-plus", 4096, "Non-Claude: command-r-plus"},
	}

	for _, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			// Test the static fallback logic directly
			got := staticAnthropicFallback(tt.model, 4096)
			if got != tt.expected {
				t.Errorf("staticAnthropicFallback(%q, 4096) = %d, want %d", tt.model, got, tt.expected)
			}
		})
	}
}

// staticAnthropicFallback is a test helper that mimics the fallback logic
// in GetMaxOutputTokensOrDefault without going through the global cache.
func staticAnthropicFallback(model string, defaultValue int) int {
	if !contains(model, "claude") {
		return defaultValue
	}
	base := normalizeClaudeModelName(model)
	if m, ok := knownAnthropicMaxOutputTokens[base]; ok {
		return m
	}
	return defaultValue
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 || indexSubstring(s, substr) >= 0)
}

func indexSubstring(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}
