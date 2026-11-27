package configstore

import (
	"context"
	"testing"

	"gorm.io/gorm"

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
// name, and its name, description and retention round-trip.
func TestTraceProjectCRUD(t *testing.T) {
	ctx := context.Background()
	store := setupTracingTestStore(t)

	project := &tables.TableTraceProject{Name: "checkout-agent", Description: "agent", RetentionDays: new(30)}
	require.NoError(t, store.CreateTraceProject(ctx, project))
	require.NotEmpty(t, project.ID, "the store assigns the id")
	assert.ErrorIs(t, store.CreateTraceProject(ctx, &tables.TableTraceProject{Name: "checkout-agent"}), ErrTraceProjectExists)

	byName, err := store.GetTraceProjectByName(ctx, "checkout-agent")
	require.NoError(t, err)
	assert.Equal(t, project.ID, byName.ID)
	assert.Equal(t, 30, *byName.RetentionDays)
	assert.Equal(t, "agent", byName.Description)

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

// An update that loses a race with a delete reports ErrNotFound and does not bring the
// project back. GORM's Save inserts the row when its update matches nothing, which would
// resurrect a project deleted between the read and the write.
func TestUpdateTraceProjectDoesNotResurrectADeletedProject(t *testing.T) {
	ctx := context.Background()
	store := setupTracingTestStore(t)
	project := &tables.TableTraceProject{Name: "deleted-meanwhile"}
	require.NoError(t, store.CreateTraceProject(ctx, project))

	// The row disappears right before GORM updates it, as if a delete committed between
	// the read and the write. Any insert after that is the resurrection.
	table := (tables.TableTraceProject{}).TableName()
	inserted := false
	update, create := "test:delete-before-update", "test:watch-insert"
	require.NoError(t, store.DB().Callback().Update().Before("gorm:update").Register(update, func(db *gorm.DB) {
		if db.Statement.Table == table {
			db.Session(&gorm.Session{NewDB: true}).Exec("DELETE FROM trace_projects WHERE id = ?", project.ID)
		}
	}))
	require.NoError(t, store.DB().Callback().Create().Before("gorm:create").Register(create, func(db *gorm.DB) {
		if db.Statement.Table == table {
			inserted = true
		}
	}))
	t.Cleanup(func() {
		_ = store.DB().Callback().Update().Remove(update)
		_ = store.DB().Callback().Create().Remove(create)
	})

	project.Description = "late edit"
	assert.ErrorIs(t, store.UpdateTraceProject(ctx, project), ErrNotFound)
	assert.False(t, inserted, "the update must not insert the deleted project again")
}
