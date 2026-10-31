package tables

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTableClientConfigAfterFindReplacesMetadata(t *testing.T) {
	config := &TableClientConfig{
		MetadataJSON: `{"theme":"light"}`,
		Metadata: map[string]any{
			"stale": "value",
			"theme": "dark",
		},
	}
	require.NoError(t, config.AfterFind(nil))
	assert.Equal(t, map[string]any{"theme": "light"}, config.Metadata)
}

func TestTableClientConfigAfterFindClearsMetadataWhenEmpty(t *testing.T) {
	config := &TableClientConfig{
		Metadata: map[string]any{
			"stale": "value",
		},
	}
	require.NoError(t, config.AfterFind(nil))
	assert.Nil(t, config.Metadata)
}

// TestA2APrivateAllowlistRoundTrip verifies persistence and clearing of permissions.
func TestA2APrivateAllowlistRoundTrip(t *testing.T) {
	config := &TableClientConfig{A2APrivateAllowlist: []string{"localhost", "10.0.0.0/8"}}
	require.NoError(t, config.BeforeSave(nil))
	stored := &TableClientConfig{A2APrivateAllowlistJSON: config.A2APrivateAllowlistJSON}
	require.NoError(t, stored.AfterFind(nil))
	assert.Equal(t, config.A2APrivateAllowlist, stored.A2APrivateAllowlist)
	stored.A2APrivateAllowlist = nil
	require.NoError(t, stored.BeforeSave(nil))
	assert.Empty(t, stored.A2APrivateAllowlistJSON)
	require.NoError(t, stored.AfterFind(nil))
	assert.Empty(t, stored.A2APrivateAllowlist)
}
