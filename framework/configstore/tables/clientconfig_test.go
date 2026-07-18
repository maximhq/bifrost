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
