package tables

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTableProviderBeforeSave_ClearsRemovedCustomProviderConfig pins that saving a
// provider loaded with a custom config, after that config was removed, clears the
// stored JSON. A custom provider whose name became a standard provider is converted
// in place this way; keeping the JSON would revive the custom provider on restart.
func TestTableProviderBeforeSave_ClearsRemovedCustomProviderConfig(t *testing.T) {
	p := &TableProvider{
		Name:                 "cloudflare",
		CustomProviderConfig: &schemas.CustomProviderConfig{BaseProviderType: schemas.OpenAI},
	}
	require.NoError(t, p.BeforeSave(nil))
	require.NotEmpty(t, p.CustomProviderConfigJSON)

	p.CustomProviderConfig = nil
	require.NoError(t, p.BeforeSave(nil))
	assert.Empty(t, p.CustomProviderConfigJSON)
}

// model_residency lives inside network_config, which this table stores as a JSON blob, so it is
// written and restored by the same hooks that carry base_url. This pins the second boot: read back
// from the column alone, the adapter must still be custom. If the field were dropped on the way in
// or out, the probe would silently fall back to auto-detection after every restart and a custom
// endpoint would stop parsing with nothing in the operator's config to explain it.
func TestTableProviderRoundTrip_KeepsModelResidency(t *testing.T) {
	p := &TableProvider{
		Name: "local-omlx",
		NetworkConfig: &schemas.NetworkConfig{
			BaseURL: "http://192.0.2.10:8090",
			ModelResidency: &schemas.ModelResidencyConfig{
				Adapter:    schemas.ResidencyAdapterCustom,
				Path:       "/internal/serving",
				TimeoutMS:  1500,
				CacheTTLMS: 250,
				Fields:     &schemas.ResidencyFieldMap{Items: "data.models", ID: "name", Loaded: "in_memory", Loading: "warming"},
			},
		},
	}
	require.NoError(t, p.BeforeSave(nil))
	require.Contains(t, p.NetworkConfigJSON, "model_residency", "residency must be stored with the rest of network_config")

	restored := &TableProvider{Name: "local-omlx", NetworkConfigJSON: p.NetworkConfigJSON}
	require.NoError(t, restored.AfterFind(nil))
	require.NotNil(t, restored.NetworkConfig)
	require.NotNil(t, restored.NetworkConfig.ModelResidency, "model_residency was lost between save and read")

	residency := restored.NetworkConfig.ModelResidency
	assert.Equal(t, schemas.ResidencyAdapterCustom, residency.Adapter)
	assert.Equal(t, "/internal/serving", residency.Path)
	assert.Equal(t, 1500, residency.TimeoutMS)
	assert.Equal(t, 250, residency.CacheTTLMS)
	require.NotNil(t, residency.Fields)
	assert.Equal(t, "data.models", residency.Fields.Items)
	assert.Equal(t, "warming", residency.Fields.Loading)
}
