package server

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTracePartIdentityKeyIsCreatedOnceAndShared pins the trace part identity key: the first load
// creates a random 32-byte key in the config store, and every load after it, on any node, reads the
// same one. Loads that race on an empty store still agree: only one insert wins.
func TestTracePartIdentityKeyIsCreatedOnceAndShared(t *testing.T) {
	ctx := context.Background()
	store, err := configstore.NewConfigStore(ctx, &configstore.Config{
		Enabled: true, Type: configstore.ConfigStoreTypeSQLite,
		Config: &configstore.SQLiteConfig{Path: filepath.Join(t.TempDir(), "config.db")},
	}, noopTestLogger{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close(ctx) })

	const loaders = 8
	keys := make([][]byte, loaders)
	var wg sync.WaitGroup
	for i := range loaders {
		wg.Go(func() {
			key, err := loadTracePartIdentityKey(ctx, store)
			assert.NoError(t, err)
			keys[i] = key
		})
	}
	wg.Wait()
	require.Len(t, keys[0], 32, "a 32-byte key")
	for i := range loaders {
		assert.Equal(t, keys[0], keys[i], "every load reads the same key")
	}
	again, err := loadTracePartIdentityKey(ctx, store)
	require.NoError(t, err)
	assert.Equal(t, keys[0], again, "a later load reads the stored key")

	row, err := store.GetConfig(ctx, tracePartIdentityConfigKey)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(row.Value, "hex:"), "without an encryption key the key is stored as hex")
}
