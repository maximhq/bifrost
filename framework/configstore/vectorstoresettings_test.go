package configstore

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A store's settings are created by name, replaced in place, read back one at a
// time or all together, and a name with no row is ErrNotFound.
func TestVectorStoreSettingsRoundTrip(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()

	_, err := store.GetVectorStoreSettings(ctx, "weaviate")
	require.ErrorIs(t, err, ErrNotFound)

	require.NoError(t, store.UpsertVectorStoreSettings(ctx, &tables.TableVectorStoreSettings{Name: "weaviate", RetentionDays: 7, MaxSizeMB: 2048, ConfigHash: "h1"}))
	require.NoError(t, store.UpsertVectorStoreSettings(ctx, &tables.TableVectorStoreSettings{Name: "archive", RetentionDays: 30}))
	require.NoError(t, store.UpsertVectorStoreSettings(ctx, &tables.TableVectorStoreSettings{Name: "weaviate", RetentionDays: 14, MaxSizeMB: 0, ConfigHash: "h2"}))

	got, err := store.GetVectorStoreSettings(ctx, "weaviate")
	require.NoError(t, err)
	assert.Equal(t, 14, got.RetentionDays)
	assert.Zero(t, got.MaxSizeMB, "an upsert replaces every value, 0 included")
	assert.Equal(t, "h2", got.ConfigHash)

	all, err := store.ListVectorStoreSettings(ctx)
	require.NoError(t, err)
	require.Len(t, all, 2)
	assert.Equal(t, []string{"archive", "weaviate"}, []string{all[0].Name, all[1].Name}, "listed by name")
}

// The bounds every entry point enforces, and a hash that tells config.json values apart.
func TestValidateVectorStoreSettings(t *testing.T) {
	require.NoError(t, ValidateVectorStoreSettings(0, 0))
	require.NoError(t, ValidateVectorStoreSettings(MaxVectorRetentionDays, MaxVectorMaxSizeMB))
	assert.ErrorContains(t, ValidateVectorStoreSettings(-1, 0), "retention_days")
	assert.ErrorContains(t, ValidateVectorStoreSettings(MaxVectorRetentionDays+1, 0), "retention_days")
	assert.ErrorContains(t, ValidateVectorStoreSettings(0, -1), "max_size_mb")
	assert.ErrorContains(t, ValidateVectorStoreSettings(0, MaxVectorMaxSizeMB+1), "max_size_mb")

	assert.Equal(t, GenerateVectorStoreSettingsHash(7, 2048), GenerateVectorStoreSettingsHash(7, 2048))
	assert.NotEqual(t, GenerateVectorStoreSettingsHash(7, 2048), GenerateVectorStoreSettingsHash(2048, 7))
	assert.NotEqual(t, GenerateVectorStoreSettingsHash(0, 0), GenerateVectorStoreSettingsHash(1, 0))
}

// An update writes only the settings it is given, so it cannot undo an
// overlapping update of the other one, and keeps the config hash; with no row
// it creates one, the other setting at 0. It returns the row before and after
// the write, as stored: the other update shows in both.
func TestUpdateVectorStoreSettingsWritesOnlyWhatItIsGiven(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()
	require.NoError(t, store.UpsertVectorStoreSettings(ctx, &tables.TableVectorStoreSettings{Name: "weaviate", RetentionDays: 7, MaxSizeMB: 100, ConfigHash: "from-file"}))

	retention, size := 30, 200
	_, _, err := store.UpdateVectorStoreSettings(ctx, "weaviate", &retention, nil)
	require.NoError(t, err)
	before, after, err := store.UpdateVectorStoreSettings(ctx, "weaviate", nil, &size)
	require.NoError(t, err)
	require.NotNil(t, before)
	assert.Equal(t, [2]int{30, 100}, [2]int{before.RetentionDays, before.MaxSizeMB}, "the row the write replaced")
	require.NotNil(t, after)
	assert.Equal(t, [2]int{30, 200}, [2]int{after.RetentionDays, after.MaxSizeMB}, "the row as the write left it")
	got, err := store.GetVectorStoreSettings(ctx, "weaviate")
	require.NoError(t, err)
	assert.Equal(t, 30, got.RetentionDays, "the size update left the retention alone")
	assert.Equal(t, 200, got.MaxSizeMB)
	assert.Equal(t, "from-file", got.ConfigHash)

	before, after, err = store.UpdateVectorStoreSettings(ctx, "new-store", nil, &size)
	require.NoError(t, err)
	assert.Nil(t, before, "there was no row")
	require.NotNil(t, after)
	assert.Equal(t, "new-store", after.Name)
	got, err = store.GetVectorStoreSettings(ctx, "new-store")
	require.NoError(t, err)
	assert.Zero(t, got.RetentionDays)
	assert.Equal(t, 200, got.MaxSizeMB)
}
