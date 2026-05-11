package handlers

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
)

func TestResolveModelCatalogProvider_RejectsAmbiguousModel(t *testing.T) {
	provider, providers, err := resolveModelCatalogProvider("gemini-2.5-flash", func(string) []schemas.ModelProvider {
		return []schemas.ModelProvider{schemas.OpenRouter, "custom-provider", schemas.OpenRouter}
	})

	assert.Empty(t, provider)
	assert.Equal(t, []schemas.ModelProvider{"custom-provider", schemas.OpenRouter}, providers)
	assert.ErrorContains(t, err, `model "gemini-2.5-flash" is ambiguous across multiple providers: custom-provider, openrouter`)
}

func TestResolveModelCatalogProvider_ResolvesSingleProvider(t *testing.T) {
	provider, providers, err := resolveModelCatalogProvider("gemini-2.5-flash", func(string) []schemas.ModelProvider {
		return []schemas.ModelProvider{schemas.OpenRouter}
	})

	assert.NoError(t, err)
	assert.Equal(t, schemas.OpenRouter, provider)
	assert.Equal(t, []schemas.ModelProvider{schemas.OpenRouter}, providers)
}
