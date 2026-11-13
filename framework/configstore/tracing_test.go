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
	require.NoError(t, store.DB().AutoMigrate(&tables.TableTraceProject{}), "migrate tracing tables")
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
