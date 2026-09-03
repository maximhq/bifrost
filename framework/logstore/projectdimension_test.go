package logstore

import (
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// newProjectDimensionStore builds an in-memory store over the real log table, so the project
// columns are exercised as the migration will create them rather than as a struct literal.
func newProjectDimensionStore(t *testing.T) *RDBLogStore {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Log{}))
	return &RDBLogStore{db: db, logger: bifrost.NewDefaultLogger(schemas.LogLevelInfo)}
}

// A project filter has to actually narrow the rows. A filter the caller sets and the query drops
// returns more than was asked for, and nothing about that surfaces as an error; the caller simply
// sees another project's traffic in their own report.
func TestProjectFilterNarrowsTheRawQuery(t *testing.T) {
	s := newProjectDimensionStore(t)

	for _, row := range []struct{ id, project string }{
		{"req-1", "proj-a"},
		{"req-2", "proj-a"},
		{"req-3", "proj-b"},
		{"req-4", ""}, // no project: the shape every request has today
	} {
		entry := &Log{ID: row.id, Status: "success"}
		if row.project != "" {
			p := row.project
			entry.ProjectID = &p
		}
		require.NoError(t, s.db.Create(entry).Error)
	}

	countWith := func(f SearchFilters) int64 {
		var n int64
		require.NoError(t, s.applyFilters(s.db.Model(&Log{}), f).Count(&n).Error)
		return n
	}

	assert.EqualValues(t, 2, countWith(SearchFilters{ProjectIDs: []string{"proj-a"}}))
	assert.EqualValues(t, 1, countWith(SearchFilters{ProjectIDs: []string{"proj-b"}}))
	assert.EqualValues(t, 3, countWith(SearchFilters{ProjectIDs: []string{"proj-a", "proj-b"}}),
		"several projects union, they do not intersect")
	assert.EqualValues(t, 4, countWith(SearchFilters{}),
		"no project filter must not hide the requests that carry no project")
	assert.EqualValues(t, 0, countWith(SearchFilters{ProjectIDs: []string{"proj-missing"}}))
}

// The project is stored as the resolved id plus its display name, so a row still reads correctly
// after the project is renamed or deleted, the same reason the team and business-unit dimensions
// keep a name column of their own.
func TestProjectColumnsRoundTrip(t *testing.T) {
	s := newProjectDimensionStore(t)

	id, name := "proj-a", "gpt-migration"
	require.NoError(t, s.db.Create(&Log{ID: "req-1", Status: "success", ProjectID: &id, ProjectName: &name}).Error)

	var stored Log
	require.NoError(t, s.db.First(&stored, "id = ?", "req-1").Error)
	require.NotNil(t, stored.ProjectID)
	require.NotNil(t, stored.ProjectName)
	assert.Equal(t, "proj-a", *stored.ProjectID)
	assert.Equal(t, "gpt-migration", *stored.ProjectName)

	// A request with no project stores neither, so "no project" and "a project named empty" stay
	// distinguishable.
	require.NoError(t, s.db.Create(&Log{ID: "req-2", Status: "success"}).Error)
	var bare Log
	require.NoError(t, s.db.First(&bare, "id = ?", "req-2").Error)
	assert.Nil(t, bare.ProjectID)
	assert.Nil(t, bare.ProjectName)
}
