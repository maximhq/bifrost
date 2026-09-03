package logstore

import (
	"context"
	"testing"
	"time"

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

// A filter dropdown offers the projects that actually appear in recent logs, by name, the way it
// does for teams and business units. Rows that carry an id but no name, or neither, are not a
// project anyone can pick.
func TestDistinctProjectsAreListedByName(t *testing.T) {
	s := newProjectDimensionStore(t)

	now := time.Now().UTC()
	for _, row := range []struct{ id, project, name string }{
		{"req-1", "proj-a", "Atlas"},
		{"req-2", "proj-a", "Atlas"},
		{"req-3", "proj-b", "Helios"},
		{"req-4", "proj-c", ""},
		{"req-5", "", ""},
	} {
		entry := &Log{ID: row.id, Status: "success", Timestamp: now}
		if row.project != "" {
			p := row.project
			entry.ProjectID = &p
		}
		if row.name != "" {
			n := row.name
			entry.ProjectName = &n
		}
		require.NoError(t, s.db.Create(entry).Error)
	}

	pairs, err := s.GetDistinctKeyPairs(context.Background(), "project_id", "project_name", 10, "")
	require.NoError(t, err)
	require.Len(t, pairs, 2, "one entry per named project, however many requests it served")
	assert.Equal(t, "proj-a", pairs[0].ID)
	assert.Equal(t, "Atlas", pairs[0].Name)
	assert.Equal(t, "proj-b", pairs[1].ID)
	assert.Equal(t, "Helios", pairs[1].Name)

	searched, err := s.GetDistinctKeyPairs(context.Background(), "project_id", "project_name", 10, "hel")
	require.NoError(t, err)
	require.Len(t, searched, 1)
	assert.Equal(t, "Helios", searched[0].Name)
}

// insertProjectLog writes one terminal, costed log row attributed to a project, or to none when
// projectID is empty. Raw SQL so the fixture is the row the migration creates, not the Go writer's
// idea of it.
func insertProjectLog(t *testing.T, db *gorm.DB, id string, ts time.Time, projectID, projectName string, cost float64) {
	t.Helper()
	nz := func(s string) any {
		if s == "" {
			return nil
		}
		return s
	}
	err := db.Exec(`
		INSERT INTO logs (id, timestamp, object_type, provider, model, status, project_id, project_name,
			created_at, latency, cost, prompt_tokens, completion_tokens, total_tokens)
		VALUES (?, ?, 'chat_completion', 'openai', 'gpt-4', 'success', ?, ?, ?, 100, ?, 10, 5, 15)
	`, id, ts, nz(projectID), nz(projectName), ts, cost).Error
	require.NoError(t, err, "failed to insert project test log")
}

// The rankings are the coverage report: every project beside what it spent, and the requests that
// named no project collected under Unassigned rather than dropped, so the rows reconcile to the
// real traffic. One project per request, so attributed equals actual: no fan-out.
func TestProjectRankingsBucketUnattributedTraffic(t *testing.T) {
	s := newProjectDimensionStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	insertProjectLog(t, s.db, "req-1", now, "proj-a", "Atlas", 0.50)
	insertProjectLog(t, s.db, "req-2", now, "proj-a", "Atlas", 0.25)
	insertProjectLog(t, s.db, "req-3", now, "proj-b", "Helios", 0.10)
	insertProjectLog(t, s.db, "req-4", now, "", "", 0.40)

	res, err := s.GetDimensionRankings(ctx, fanoutWindow(now), RankingDimensionProject)
	require.NoError(t, err)
	require.Equal(t, RankingDimensionProject, res.Dimension)

	requests := make(map[string]int64, len(res.Rankings))
	names := make(map[string]string, len(res.Rankings))
	cost := make(map[string]float64, len(res.Rankings))
	for _, r := range res.Rankings {
		requests[r.ID] = r.TotalRequests
		names[r.ID] = r.Name
		cost[r.ID] = r.TotalCost
	}
	assert.Equal(t, int64(2), requests["proj-a"])
	assert.Equal(t, "Atlas", names["proj-a"])
	assert.InDelta(t, 0.75, cost["proj-a"], 1e-9)
	assert.Equal(t, int64(1), requests["proj-b"])
	assert.Equal(t, int64(1), requests[unassignedDimensionID], "a request that named no project is reported, not hidden")
	assert.Equal(t, unassignedDimensionName, names[unassignedDimensionID])
	assert.InDelta(t, 0.40, cost[unassignedDimensionID], 1e-9)

	assert.Equal(t, int64(4), res.TotalActualRequests)
	assert.Equal(t, int64(4), res.TotalAttributedRequests, "one project per request: nothing is counted twice")
}

// The by-dimension histogram splits spend over time per project, with the unattributed remainder
// as its own series, so a chart can show coverage alongside the projects.
func TestProjectCostHistogramGroupsByProject(t *testing.T) {
	s := newProjectDimensionStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Hour).Add(30 * time.Minute)

	insertProjectLog(t, s.db, "req-1", now, "proj-a", "Atlas", 0.50)
	insertProjectLog(t, s.db, "req-2", now, "proj-b", "Helios", 0.10)
	insertProjectLog(t, s.db, "req-3", now, "", "", 0.40)

	res, err := s.GetDimensionCostHistogram(ctx, fanoutWindow(now), 3600, DimensionProject)
	require.NoError(t, err)
	require.Equal(t, DimensionProject, res.Dimension)
	assert.ElementsMatch(t, []string{"proj-a", "proj-b", unassignedDimensionID}, res.DimensionValues)

	var total float64
	byProject := make(map[string]float64)
	for _, b := range res.Buckets {
		total += b.TotalCost
		for k, v := range b.ByDimension {
			byProject[k] += v
		}
	}
	assert.InDelta(t, 1.0, total, 1e-9)
	assert.InDelta(t, 0.50, byProject["proj-a"], 1e-9)
	assert.InDelta(t, 0.10, byProject["proj-b"], 1e-9)
	assert.InDelta(t, 0.40, byProject[unassignedDimensionID], 1e-9)
}

// The list is what the logs page renders, and it reads a narrower column set than the detail
// view; the project has to be in it or the column on the page is always empty.
func TestListRowsCarryTheProject(t *testing.T) {
	// A file-backed store: the list path reads through the pool, and sqlite gives every
	// connection to ":memory:" a database of its own.
	s := newProjectionTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	insertProjectLog(t, s.db, "req-1", now, "proj-a", "Atlas", 0.50)

	res, err := s.SearchLogs(ctx, fanoutWindow(now), PaginationOptions{Limit: 10})
	require.NoError(t, err)
	require.Len(t, res.Logs, 1)
	require.NotNil(t, res.Logs[0].ProjectID)
	require.NotNil(t, res.Logs[0].ProjectName)
	assert.Equal(t, "proj-a", *res.Logs[0].ProjectID)
	assert.Equal(t, "Atlas", *res.Logs[0].ProjectName)
}

// ── Postgres: the matview path ──
// These skip without a local Postgres, like every other matview test.

// The dropdown's project list is served from its own filter view once the views are populated,
// the way teams and business units are: no raw scan on a deployment that has the views.
func TestFilterProjectsMatView_ListsProjects(t *testing.T) {
	store, db := setupPerfTestDB(t)
	store.matViewsReady.Store(true)
	ctx := context.Background()
	now := time.Now().UTC()

	insertProjectLog(t, db, "req-1", now, "proj-a", "Atlas", 0.50)
	insertProjectLog(t, db, "req-2", now, "proj-a", "Atlas", 0.25)
	insertProjectLog(t, db, "req-3", now, "proj-b", "Helios", 0.10)
	insertProjectLog(t, db, "req-4", now, "", "", 0.40)
	refreshTestMatViews(t, db)

	pairs, served, err := store.getDistinctKeyPairsFromMatView(ctx, "project_id", "project_name", 1000, "")
	require.NoError(t, err)
	require.True(t, served, "mv_filter_projects must serve the projects dimension")
	require.Len(t, pairs, 2)
	assert.Equal(t, "proj-a", pairs[0].ID)
	assert.Equal(t, "Atlas", pairs[0].Name)
	assert.Equal(t, "proj-b", pairs[1].ID)
	assert.Equal(t, "Helios", pairs[1].Name)
}

// A project filter is eligible for the hourly view and narrows it: the stats a project page shows
// over a long window come off the pre-aggregated rows, and only the project's own rows.
func TestHourlyMatView_FiltersByProject(t *testing.T) {
	store, db := setupPerfTestDB(t)
	store.matViewsReady.Store(true)
	ctx := context.Background()
	now := time.Now().UTC().Add(-2 * time.Hour)

	insertProjectLog(t, db, "req-1", now, "proj-a", "Atlas", 0.50)
	insertProjectLog(t, db, "req-2", now, "proj-a", "Atlas", 0.25)
	insertProjectLog(t, db, "req-3", now, "proj-b", "Helios", 0.10)
	insertProjectLog(t, db, "req-4", now, "", "", 0.40)
	refreshTestMatViews(t, db)

	start := now.Add(-48 * time.Hour)
	end := now.Add(time.Hour)
	filters := SearchFilters{StartTime: &start, EndTime: &end, ProjectIDs: []string{"proj-a"}}
	require.True(t, store.canUseMatView(filters), "a project filter must not force the raw path")

	stats, err := store.getStatsFromMatView(ctx, filters)
	require.NoError(t, err)
	assert.EqualValues(t, 2, stats.TotalRequests)
	assert.InDelta(t, 0.75, stats.TotalCost, 1e-9)

	both := SearchFilters{StartTime: &start, EndTime: &end, ProjectIDs: []string{"proj-a", "proj-b"}}
	stats, err = store.getStatsFromMatView(ctx, both)
	require.NoError(t, err)
	assert.EqualValues(t, 3, stats.TotalRequests, "several projects union; the unattributed request stays out")
}
