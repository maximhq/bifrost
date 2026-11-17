package server

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib/libtest"
	"github.com/stretchr/testify/require"
)

// TestSeedModelCatalogCarriesKeylessProvidersAndIgnoredCosts checks what boot pushes into the
// catalog: each provider's keys, a keyless custom provider that stays routable with no keys, and
// the providers whose self-reported cost is ignored.
func TestSeedModelCatalogCarriesKeylessProvidersAndIgnoredCosts(t *testing.T) {
	enabled := true
	config := libtest.Config(t, map[schemas.ModelProvider]configstore.ProviderConfig{
		"local-llm": {CustomProviderConfig: &schemas.CustomProviderConfig{BaseProviderType: schemas.OpenAI, IsKeyLess: true}},
		schemas.OpenAI: {
			Keys:               []schemas.Key{{ID: "k1", Name: "k1", Value: *schemas.NewSecretVar("sk-1"), Models: schemas.WhiteList{"*"}, Enabled: &enabled, Weight: 1}},
			IgnoreProviderCost: true,
		},
	})
	catalog := catalogForTest(t, nil)

	snapshot := SeedModelCatalog(catalog, config.LiveStore)
	require.Len(t, snapshot[schemas.OpenAI], 1)
	require.Empty(t, snapshot["local-llm"])
	require.Equal(t, schemas.WhiteList{"*"}, catalog.AllowedModelsForProvider("local-llm"), "a keyless provider with no keys serves any model")
	require.Equal(t, schemas.WhiteList{"*"}, catalog.AllowedModelsForProvider(schemas.OpenAI))
	require.True(t, catalog.IsProviderCostIgnored(schemas.OpenAI))
	require.False(t, catalog.IsProviderCostIgnored("local-llm"))
}
