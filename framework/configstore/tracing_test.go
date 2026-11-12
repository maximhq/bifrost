package configstore

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupTracingTestStore extends the base test store with the tracing tables.
func setupTracingTestStore(t *testing.T) *RDBConfigStore {
	store := setupRDBTestStore(t)
	require.NoError(t, store.DB().AutoMigrate(&tables.TableTraceProject{}, &tables.TableTracingKey{}), "migrate tracing tables")
	return store
}

// TestTraceProjectCRUD pins tracing projects: names are unique, a project is found by id and by
// name, and its settings round-trip.
func TestTraceProjectCRUD(t *testing.T) {
	ctx := context.Background()
	store := setupTracingTestStore(t)

	project := &tables.TableTraceProject{Name: "checkout-agent", RetentionDays: new(30), MaskingRules: []tables.TraceMaskingRule{{Name: "emails", Pattern: `[\w.]+@[\w.]+`, Replacement: "[email]"}}}
	require.NoError(t, store.CreateTraceProject(ctx, project))
	require.NotEmpty(t, project.ID, "the store assigns the id")
	assert.ErrorIs(t, store.CreateTraceProject(ctx, &tables.TableTraceProject{Name: "checkout-agent"}), ErrTraceProjectExists)

	byName, err := store.GetTraceProjectByName(ctx, "checkout-agent")
	require.NoError(t, err)
	assert.Equal(t, project.ID, byName.ID)
	assert.Equal(t, 30, *byName.RetentionDays)
	require.Len(t, byName.MaskingRules, 1)
	assert.Equal(t, "[email]", byName.MaskingRules[0].Replacement)

	byName.Description = "prod"
	byName.RetentionDays = nil
	require.NoError(t, store.UpdateTraceProject(ctx, byName))
	byID, err := store.GetTraceProject(ctx, project.ID)
	require.NoError(t, err)
	assert.Equal(t, "prod", byID.Description)
	assert.Nil(t, byID.RetentionDays, "a cleared retention falls back to the global setting")

	projects, err := store.ListTraceProjects(ctx)
	require.NoError(t, err)
	assert.Len(t, projects, 1)

	require.NoError(t, store.DeleteTraceProject(ctx, project.ID))
	_, err = store.GetTraceProject(ctx, project.ID)
	assert.ErrorIs(t, err, ErrNotFound)
	assert.ErrorIs(t, store.DeleteTraceProject(ctx, project.ID), ErrNotFound)
}

// TestTracingKeyCRUD pins tracing keys: only the hash of the key is stored, a key is found by that
// hash, and its bindings round-trip.
func TestTracingKeyCRUD(t *testing.T) {
	ctx := context.Background()
	store := setupTracingTestStore(t)

	value, err := tables.NewTracingKeyValue()
	require.NoError(t, err)
	key := &tables.TableTracingKey{Name: "ci", ProjectIDs: []string{"p1", "p2"}, DefaultProjectID: "p1", VirtualKeyIDs: []string{"vk1"}, LogContent: true, Enabled: true}
	key.SetValue(value)
	require.NoError(t, store.CreateTracingKey(ctx, key))
	assert.NotContains(t, key.KeyHash, value)
	assert.Equal(t, tables.HashTracingKey(value), key.KeyHash)
	assert.True(t, len(key.KeyPrefix) > 0 && len(key.KeyPrefix) < len(value), "only a short prefix is kept for display")

	found, err := store.GetTracingKeyByHash(ctx, tables.HashTracingKey(value))
	require.NoError(t, err)
	assert.Equal(t, []string{"p1", "p2"}, found.ProjectIDs)
	assert.Equal(t, []string{"vk1"}, found.VirtualKeyIDs)
	assert.True(t, found.AllowsProject("p2"))
	assert.False(t, found.AllowsProject("p3"))
	_, err = store.GetTracingKeyByHash(ctx, tables.HashTracingKey("bf-tr-other"))
	assert.ErrorIs(t, err, ErrNotFound)

	found.Enabled = false
	found.ProjectIDs = []string{"p1"}
	require.NoError(t, store.UpdateTracingKey(ctx, found))
	keys, err := store.ListTracingKeys(ctx)
	require.NoError(t, err)
	require.Len(t, keys, 1)
	assert.False(t, keys[0].Enabled)
	assert.Equal(t, []string{"p1"}, keys[0].ProjectIDs)

	require.NoError(t, store.DeleteTracingKey(ctx, found.ID))
	assert.ErrorIs(t, store.DeleteTracingKey(ctx, found.ID), ErrNotFound)
}
