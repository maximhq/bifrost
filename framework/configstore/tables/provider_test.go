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
