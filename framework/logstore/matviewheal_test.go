package logstore

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestIsMatViewShapeError(t *testing.T) {
	shape := []error{
		&pgconn.PgError{Code: "42P01"},                                 // undefined_table
		&pgconn.PgError{Code: "42703"},                                 // undefined_column
		&pgconn.PgError{Code: "55000"},                                 // object_not_in_prerequisite_state
		fmt.Errorf("query failed: %w", &pgconn.PgError{Code: "42703"}), // wrapped
	}
	for _, err := range shape {
		assert.True(t, isMatViewShapeError(err), "expected shape error: %v", err)
	}

	notShape := []error{
		nil,
		context.Canceled,
		gorm.ErrRecordNotFound,
		errors.New("column does not exist"),         // text without a PgError is not classified
		&pgconn.PgError{Code: "0A000"},              // cached-plan drift must stay loud
		&pgconn.PgError{Code: "42883"},              // undefined_function is a code bug
		fmt.Errorf("wrap: %w", errors.New("42703")), // code in message only
	}
	for _, err := range notShape {
		assert.False(t, isMatViewShapeError(err), "expected non-shape error: %v", err)
	}
}

// TestMatViewShapeErrorFallsBackAndSelfHeals drops mv_logs_hourly mid-run and
// verifies that (a) matview-gated reads keep returning correct results from
// the raw table with no error, (b) the matview read path is disabled
// immediately, and (c) the background self-heal recreates the view and
// re-enables the matview path without a restart.
func TestMatViewShapeErrorFallsBackAndSelfHeals(t *testing.T) {
	store, db := setupPerfTestDB(t)
	ctx := context.Background()

	day := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
	insertCountTestLog(t, db, day.Add(10*time.Hour), "success")
	insertCountTestLog(t, db, day.Add(11*time.Hour), "error")
	refreshTestMatViews(t, db)
	store.matViewsReady.Store(true)
	store.resetMatViewHeal()

	require.NoError(t, db.Exec("DROP MATERIALIZED VIEW mv_logs_hourly CASCADE").Error)

	// Unbounded window keeps the fresh-aggregate gate satisfied, so both
	// calls attempt the matview first and must fall through on 42P01.
	stats, err := store.GetStats(ctx, SearchFilters{})
	require.NoError(t, err, "GetStats must serve from raw tables when the view is missing")
	assert.Equal(t, int64(2), stats.TotalRequests)

	result, err := store.SearchLogs(ctx, SearchFilters{}, PaginationOptions{Limit: 10})
	require.NoError(t, err, "SearchLogs must serve from raw tables when the view is missing")
	assert.Equal(t, int64(2), result.Pagination.TotalCount)

	assert.False(t, store.matViewsReady.Load(),
		"the matview read path must be disabled after a shape error")

	require.Eventually(t, func() bool {
		if !store.matViewsReady.Load() {
			return false
		}
		var exists bool
		if err := db.Raw(
			"SELECT EXISTS (SELECT 1 FROM pg_class WHERE relname = 'mv_logs_hourly' AND relkind = 'm')",
		).Scan(&exists).Error; err != nil {
			return false
		}
		return exists
	}, 90*time.Second, 500*time.Millisecond,
		"self-heal must recreate mv_logs_hourly and re-enable the matview path")

	stats, err = store.GetStats(ctx, SearchFilters{})
	require.NoError(t, err)
	assert.Equal(t, int64(2), stats.TotalRequests, "healed matview path must agree with raw")
}

// TestMatViewStaleShapeFallsBackAndSelfHeals replaces mv_logs_hourly with an
// old-shape view missing required columns (the #5384 rolling-deploy state:
// REFRESH succeeds on it, only reads fail) and verifies the 42703 path: raw
// fallback with no error, then background repair via repairMatViewShapes.
func TestMatViewStaleShapeFallsBackAndSelfHeals(t *testing.T) {
	store, db := setupPerfTestDB(t)
	ctx := context.Background()

	day := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
	insertCountTestLog(t, db, day.Add(10*time.Hour), "success")

	require.NoError(t, db.Exec("DROP MATERIALIZED VIEW mv_logs_hourly CASCADE").Error)
	require.NoError(t, db.Exec(`
		CREATE MATERIALIZED VIEW mv_logs_hourly AS
		SELECT date_trunc('hour', timestamp) AS hour, provider, model, status, COUNT(*) AS count
		FROM logs WHERE status IN ('success', 'error', 'cancelled')
		GROUP BY 1, 2, 3, 4
	`).Error)
	// REFRESH succeeds on the stale shape - readiness alone cannot detect it.
	require.NoError(t, db.Exec("REFRESH MATERIALIZED VIEW mv_logs_hourly").Error)
	store.matViewsReady.Store(true)
	store.resetMatViewHeal()

	hist, err := store.GetHistogram(ctx, SearchFilters{}, 3600)
	require.NoError(t, err, "GetHistogram must serve from raw tables when the view shape is stale")
	var total int64
	for _, b := range hist.Buckets {
		total += b.Count
	}
	assert.Equal(t, int64(1), total)
	assert.False(t, store.matViewsReady.Load())

	require.Eventually(t, func() bool {
		if !store.matViewsReady.Load() {
			return false
		}
		var hasColumn bool
		if err := db.Raw(`
			SELECT EXISTS (
				SELECT 1 FROM pg_attribute a
				JOIN pg_class c ON c.oid = a.attrelid
				WHERE c.relname = 'mv_logs_hourly' AND c.relkind = 'm'
				  AND a.attname = 'cancelled_count' AND a.attnum > 0 AND NOT a.attisdropped
			)`).Scan(&hasColumn).Error; err != nil {
			return false
		}
		return hasColumn
	}, 90*time.Second, 500*time.Millisecond,
		"self-heal must rebuild the stale-shaped view with the current columns")
}

// TestFilterMatViewShapeErrorFallsBack covers the mv_filter_* views: dropping
// one must not break its GetDistinct* endpoint.
func TestFilterMatViewShapeErrorFallsBack(t *testing.T) {
	store, db := setupPerfTestDB(t)
	ctx := context.Background()

	day := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
	insertCountTestLog(t, db, day.Add(10*time.Hour), "success") // model gpt-4
	refreshTestMatViews(t, db)
	store.matViewsReady.Store(true)
	store.resetMatViewHeal()

	require.NoError(t, db.Exec("DROP MATERIALIZED VIEW mv_filter_models CASCADE").Error)

	models, err := store.GetDistinctModels(ctx, 10, "")
	require.NoError(t, err, "GetDistinctModels must serve from raw tables when the filter view is missing")
	assert.Contains(t, models, "gpt-4")
	assert.False(t, store.matViewsReady.Load())
}
