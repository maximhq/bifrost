package bifrost

import (
	"slices"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// TestSynthesizedIDsMatchProviderConvention checks synthesized identifiers
// against the shapes real providers return, captured from a running gateway.
//
// The convention is provider/model, where model may itself contain slashes,
// dots or colons. A resold model keeps its upstream name inside the serving
// provider's namespace: Perplexity returns "perplexity/anthropic/claude-haiku-4-5"
// for a Claude model it fronts. Synthesizing the bare "anthropic/claude-haiku-4-5"
// instead produced the same model twice under two IDs, and left Anthropic
// appearing in listings after its keys had been disabled.
func TestSynthesizedIDsMatchProviderConvention(t *testing.T) {
	tests := []struct {
		name     string
		provider schemas.ModelProvider
		routable []string
		want     []string
	}{
		{
			name:     "openai plain names",
			provider: schemas.OpenAI,
			routable: []string{"gpt-4-0125-preview", "babbage-002"},
			want:     []string{"openai/gpt-4-0125-preview", "openai/babbage-002"},
		},
		{
			name:     "openai names already carrying their own prefix",
			provider: schemas.OpenAI,
			routable: []string{"openai/gpt-4o"},
			want:     []string{"openai/gpt-4o"},
		},
		{
			name:     "openrouter resells an upstream model with a slash in its name",
			provider: schemas.OpenRouter,
			routable: []string{"anthropic/claude-fable-5", "ai21/jamba-large-1.7"},
			want:     []string{"openrouter/anthropic/claude-fable-5", "openrouter/ai21/jamba-large-1.7"},
		},
		{
			name:     "perplexity fronts claude, matching its own perplexity/anthropic/... form",
			provider: schemas.Perplexity,
			routable: []string{"anthropic/claude-haiku-4-5"},
			want:     []string{"perplexity/anthropic/claude-haiku-4-5"},
		},
		{
			name:     "bedrock names contain dots and colons",
			provider: schemas.Bedrock,
			routable: []string{"amazon.titan-image-generator-v2:0", "ai21.jamba-1-5-large-v1:0"},
			want:     []string{"bedrock/amazon.titan-image-generator-v2:0", "bedrock/ai21.jamba-1-5-large-v1:0"},
		},
		{
			name:     "vertex plain names",
			provider: schemas.Vertex,
			routable: []string{"alphagenome-request", "claude-fable-5"},
			want:     []string{"vertex/alphagenome-request", "vertex/claude-fable-5"},
		},
		{
			name:     "azure deployment-style names",
			provider: schemas.Azure,
			routable: []string{"AI21-Jamba-1.5-Large", "claude-sonnet-4-5"},
			want:     []string{"azure/AI21-Jamba-1.5-Large", "azure/claude-sonnet-4-5"},
		},
		{
			name:     "groq upstream name containing a slash",
			provider: schemas.Groq,
			routable: []string{"canopylabs/orpheus-arabic-saudi", "allam-2-7b"},
			want:     []string{"groq/canopylabs/orpheus-arabic-saudi", "groq/allam-2-7b"},
		},
		{
			name:     "gemini, cohere, xai, cerebras plain names",
			provider: schemas.Gemini,
			routable: []string{"aqa", "antigravity-preview-05-2026"},
			want:     []string{"gemini/aqa", "gemini/antigravity-preview-05-2026"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := modelIDs(synthesizeModels(tt.provider, tt.routable, nil))
			if !slices.Equal(got, tt.want) {
				t.Errorf("synthesizeModels(%s) = %v, want %v", tt.provider, got, tt.want)
			}
		})
	}
}

// The duplicate this fixes: the provider lists the model under its namespaced
// form while the routable set names it without that prefix. Both refer to the
// same model, so only one entry may survive.
func TestSynthesizedIDsDoNotDuplicateProviderEntries(t *testing.T) {
	tests := []struct {
		name       string
		provider   schemas.ModelProvider
		providerID string
		routable   string
	}{
		{"perplexity fronting claude", schemas.Perplexity, "perplexity/anthropic/claude-haiku-4-5", "anthropic/claude-haiku-4-5"},
		{"openrouter reselling", schemas.OpenRouter, "openrouter/ai21/jamba-large-1.7", "ai21/jamba-large-1.7"},
		{"openai prefixed vs bare", schemas.OpenAI, "openai/gpt-4o", "gpt-4o"},
		{"bedrock colon name", schemas.Bedrock, "bedrock/ai21.jamba-1-5-large-v1:0", "ai21.jamba-1-5-large-v1:0"},
		{"groq slashed upstream name", schemas.Groq, "groq/canopylabs/orpheus-arabic-saudi", "canopylabs/orpheus-arabic-saudi"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := &Bifrost{}
			b.SetModelDirectory(&fakeModelDirectory{routable: []string{tt.routable}}, true)

			resp := &schemas.BifrostListModelsResponse{Data: modelsFixture(tt.providerID)}
			got := modelIDs(b.reconcileListModelsWithRoutable(resp, tt.provider, nil, &schemas.BifrostListModelsRequest{}).Data)

			if !slices.Equal(got, []string{tt.providerID}) {
				t.Errorf("reconciled = %v, want only the provider's own entry %q", got, tt.providerID)
			}
		})
	}
}

// localModelName drops only the serving provider's own prefix. A prefix that
// names a different provider is part of the upstream model name, so removing it
// would lose the identifier that provider expects.
func TestLocalModelName(t *testing.T) {
	tests := []struct {
		name     string
		id       string
		provider schemas.ModelProvider
		want     string
	}{
		{"own prefix stripped", "openai/gpt-4o", schemas.OpenAI, "gpt-4o"},
		{"no prefix untouched", "gpt-4o", schemas.OpenAI, "gpt-4o"},
		{"foreign prefix is part of the name", "anthropic/claude-haiku-4-5", schemas.Perplexity, "anthropic/claude-haiku-4-5"},
		{"own prefix stripped, upstream slash kept", "openrouter/ai21/jamba-large-1.7", schemas.OpenRouter, "ai21/jamba-large-1.7"},
		{"unknown prefix is part of the name", "canopylabs/orpheus-arabic-saudi", schemas.Groq, "canopylabs/orpheus-arabic-saudi"},
		{"colon and dot names untouched", "ai21.jamba-1-5-large-v1:0", schemas.Bedrock, "ai21.jamba-1-5-large-v1:0"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := localModelName(tt.id, tt.provider); got != tt.want {
				t.Errorf("localModelName(%q, %s) = %q, want %q", tt.id, tt.provider, got, tt.want)
			}
		})
	}
}
