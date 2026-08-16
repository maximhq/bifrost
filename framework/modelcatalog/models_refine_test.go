package modelcatalog

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// TestRefineModelForProvider covers the refinement contract: bare names resolve to the
// provider's catalog slug, known-provider prefixes are idempotent, the target provider's
// own prefix is stripped and re-refined (round-tripping canonical own-prefixed names),
// and unknown namespace prefixes are left untouched.
func TestRefineModelForProvider(t *testing.T) {
	mc := NewTestCatalog(nil)
	mc.UpsertLive(schemas.Groq, "groq-key", false, []string{"openai/gpt-oss-120b", "meta-llama/llama-4-scout-17b-16e-instruct", "qwen/qwen3-32b"})
	mc.UpsertLive(schemas.OpenRouter, "or-key", false, []string{"openai/gpt-4o", "anthropic/claude-sonnet-4-5", "openrouter/auto"})
	mc.UpsertLive(schemas.Perplexity, "pplx-key", false, []string{"perplexity/sonar", "anthropic/claude-opus-4-6"})
	mc.UpsertLive(schemas.Replicate, "repl-key", false, []string{"anthropic/claude-4.5-sonnet", "meta/llama-3-8b", "black-forest-labs/flux-dev"})

	cases := []struct {
		name     string
		provider schemas.ModelProvider
		model    string
		want     string
	}{
		{"groq bare resolves to catalog slug", schemas.Groq, "gpt-oss-120b", "openai/gpt-oss-120b"},
		{"groq already-refined is unchanged", schemas.Groq, "openai/gpt-oss-120b", "openai/gpt-oss-120b"},
		{"groq unknown namespace prefix untouched", schemas.Groq, "meta-llama/llama-4-scout-17b-16e-instruct", "meta-llama/llama-4-scout-17b-16e-instruct"},
		{"groq bare with no catalog match unchanged", schemas.Groq, "llama-3.3-70b-versatile", "llama-3.3-70b-versatile"},
		{"openrouter bare resolves to catalog slug", schemas.OpenRouter, "gpt-4o", "openai/gpt-4o"},
		{"openrouter already-refined is unchanged", schemas.OpenRouter, "anthropic/claude-sonnet-4-5", "anthropic/claude-sonnet-4-5"},
		{"openrouter own-prefixed canonical round-trips", schemas.OpenRouter, "openrouter/auto", "openrouter/auto"},
		{"perplexity bare resolves to catalog slug", schemas.Perplexity, "claude-opus-4-6", "anthropic/claude-opus-4-6"},
		{"perplexity own-prefixed canonical round-trips", schemas.Perplexity, "perplexity/sonar", "perplexity/sonar"},
		{"replicate bare resolves to catalog slug", schemas.Replicate, "claude-4.5-sonnet", "anthropic/claude-4.5-sonnet"},
		{"replicate already-refined is unchanged", schemas.Replicate, "anthropic/claude-4.5-sonnet", "anthropic/claude-4.5-sonnet"},
		{"replicate owner/model identifier untouched", schemas.Replicate, "meta/llama-3-8b", "meta/llama-3-8b"},
		{"replicate bare owner-namespaced model unchanged", schemas.Replicate, "flux-dev", "flux-dev"},
		{"own prefix on non-nested provider is stripped", schemas.OpenAI, "openai/gpt-oss-120b", "gpt-oss-120b"},
		{"foreign prefix on non-nested provider unchanged", schemas.OpenAI, "anthropic/claude-opus-4-6", "anthropic/claude-opus-4-6"},
		{"non-nested provider bare unchanged", schemas.Anthropic, "claude-opus-4-6", "claude-opus-4-6"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := mc.RefineModelForProvider(tc.provider, tc.model)
			if err != nil {
				t.Fatalf("RefineModelForProvider(%s, %q) returned error: %v", tc.provider, tc.model, err)
			}
			if got != tc.want {
				t.Fatalf("RefineModelForProvider(%s, %q) = %q, want %q", tc.provider, tc.model, got, tc.want)
			}
		})
	}
}
