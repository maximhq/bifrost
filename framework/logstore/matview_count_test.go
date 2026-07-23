package logstore

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// insertCountTestLog inserts a minimal log row at the given timestamp for the
// hybrid-count boundary tests.
func insertCountTestLog(t *testing.T, db *gorm.DB, ts time.Time, status string) {
	t.Helper()
	insertCountTestModelLog(t, db, ts, status, "gpt-4", nil)
}

// insertCountTestModelLog is insertCountTestLog with an explicit wire model and
// optional canonical model name, for the canonical-model filter tests.
func insertCountTestModelLog(t *testing.T, db *gorm.DB, ts time.Time, status, model string, canonicalModelName *string) {
	t.Helper()
	err := db.Exec(`
		INSERT INTO logs (id, timestamp, object_type, provider, model, canonical_model_name, status,
			created_at, latency, cost, prompt_tokens, completion_tokens, total_tokens)
		VALUES (?, ?, 'chat_completion', 'openai', ?, ?, ?, ?, 100, 0.01, 10, 5, 15)
	`, uuid.New().String(), ts, model, canonicalModelName, status, ts).Error
	require.NoError(t, err, "failed to insert count test log")
}

// TestSearchLogsMatViewCountMatchesRawRange is the regression test for
// https://github.com/maximhq/bifrost/issues/5329: for matview-eligible windows
// (>= 24h), pagination total_count came from mv_logs_hourly with predicates
// that rounded both boundaries out to full hour buckets, counting logs the
// exact-range row list can never page to. The hybrid count must match the
// number of logs strictly within [StartTime, EndTime] that the row list can
// page to - terminal logs plus in-flight ("processing") logs, which the row
// list shows but mv_logs_hourly cannot see.
//
// The expectations hold regardless of the server's session TimeZone (which
// shifts the date_trunc('hour') bucket grid — e.g. Asia/Kolkata puts buckets
// on :30 UTC boundaries): the hybrid count does all grid arithmetic in SQL.
func TestSearchLogsMatViewCountMatchesRawRange(t *testing.T) {
	store, db := setupPerfTestDB(t)
	ctx := context.Background()

	day := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
	start := day.Add(10*time.Hour + 30*time.Minute) // 10:30, mid-hour
	end := day.Add(35*time.Hour + 45*time.Minute)   // next day 11:45 (25h15m window)

	insertCountTestLog(t, db, day.Add(10*time.Hour+15*time.Minute), "success")   // before start -> excluded
	insertCountTestLog(t, db, day.Add(10*time.Hour+45*time.Minute), "success")   // just after start -> counted
	insertCountTestLog(t, db, day.Add(18*time.Hour), "success")                  // interior -> counted
	insertCountTestLog(t, db, day.Add(35*time.Hour), "success")                  // near end, in range -> counted
	insertCountTestLog(t, db, day.Add(35*time.Hour+30*time.Minute), "error")     // just before end -> counted
	insertCountTestLog(t, db, day.Add(35*time.Hour+50*time.Minute), "success")   // after end -> excluded
	insertCountTestLog(t, db, day.Add(18*time.Hour+5*time.Minute), "processing") // in-flight -> counted (visible in the row list)

	refreshTestMatViews(t, db)
	store.matViewsReady.Store(true) // force the matview count path

	filters := SearchFilters{StartTime: &start, EndTime: &end}
	require.True(t, store.canUseMatViewForFreshAggregate(filters), "window must take the matview count path")

	result, err := store.SearchLogs(ctx, filters, PaginationOptions{Limit: 50})
	require.NoError(t, err)
	assert.Equal(t, int64(5), result.Pagination.TotalCount,
		"total_count must include every log inside [start, end] the row list can page to, in-flight included")
	assert.Len(t, result.Logs, 5, "row list and total_count must agree on the same population")

	// Hour-aligned end: nothing after end may be counted (the old `hour <= end`
	// predicate included the entire bucket starting exactly at end), while a
	// log exactly at end stays included.
	alignedEnd := day.Add(35 * time.Hour) // next day 11:00
	filters = SearchFilters{StartTime: &start, EndTime: &alignedEnd}
	require.True(t, store.canUseMatViewForFreshAggregate(filters))

	result, err = store.SearchLogs(ctx, filters, PaginationOptions{Limit: 50})
	require.NoError(t, err)
	assert.Equal(t, int64(4), result.Pagination.TotalCount,
		"hour-aligned end must include a log exactly at end but nothing after it")

	// Hour-aligned start: everything at-or-after start is counted, including
	// the 10:15 log that the mid-hour start above excluded.
	alignedStart := day.Add(10 * time.Hour) // 10:00
	filters = SearchFilters{StartTime: &alignedStart, EndTime: &end}
	require.True(t, store.canUseMatViewForFreshAggregate(filters))

	result, err = store.SearchLogs(ctx, filters, PaginationOptions{Limit: 50})
	require.NoError(t, err)
	assert.Equal(t, int64(6), result.Pagination.TotalCount,
		"hour-aligned start must count everything from start onward")
}

// TestSearchLogsMatViewCountCanonicalModelFilter verifies that a Models filter
// counts aliased traffic the same way the raw row list does: applyFilters
// matches (model IN ? OR canonical_model_name IN ?), so the matview predicates
// must too. Before the fix the matview side matched only the wire model, so a
// row with model "prod-alias" / canonical_model_name "gpt-4o-mini" landing in
// an interior bucket was pageable but missing from total_count.
func TestSearchLogsMatViewCountCanonicalModelFilter(t *testing.T) {
	store, db := setupPerfTestDB(t)
	ctx := context.Background()

	day := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
	start := day.Add(10*time.Hour + 30*time.Minute)
	end := day.Add(35*time.Hour + 45*time.Minute) // 25h15m window, matview-eligible

	canonical := "gpt-4o-mini"
	interior := day.Add(18 * time.Hour) // deep inside the window, never a boundary sliver
	insertCountTestModelLog(t, db, interior, "success", canonical, nil)                              // wire-model match
	insertCountTestModelLog(t, db, interior.Add(time.Minute), "success", "prod-alias", &canonical)   // canonical-only match
	insertCountTestModelLog(t, db, interior.Add(2*time.Minute), "success", "other-model", nil)       // no match
	insertCountTestModelLog(t, db, start.Add(5*time.Minute), "success", "prod-alias", &canonical)    // canonical match in the head boundary sliver
	insertCountTestModelLog(t, db, day.Add(9*time.Hour), "success", "prod-alias", &canonical)        // before start -> excluded

	refreshTestMatViews(t, db)
	store.matViewsReady.Store(true)

	filters := SearchFilters{StartTime: &start, EndTime: &end, Models: []string{canonical}}
	require.True(t, store.canUseMatViewForFreshAggregate(filters), "Models filter must stay matview-eligible")

	result, err := store.SearchLogs(ctx, filters, PaginationOptions{Limit: 50})
	require.NoError(t, err)
	assert.Equal(t, int64(3), result.Pagination.TotalCount,
		"total_count must include canonical-model matches in interior buckets and boundary slivers alike")
	assert.Len(t, result.Logs, 3, "row list and total_count must agree on the same population")
}

// TestSearchLogsParentRequestIDForcesRawPath verifies the matview count path is
// never taken for a fallback-chain search: mv_logs_hourly has no
// parent_request_id dimension, so a matview interior sum would count every
// terminal log in the window instead of just the chain's rows.
func TestSearchLogsParentRequestIDForcesRawPath(t *testing.T) {
	store, db := setupPerfTestDB(t)
	ctx := context.Background()

	day := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
	start := day.Add(10*time.Hour + 30*time.Minute)
	end := day.Add(35*time.Hour + 45*time.Minute) // >=24h: matview-eligible if the filter gate regresses

	parentID := uuid.New().String()
	interior := day.Add(18 * time.Hour)
	insertCountTestLog(t, db, interior, "success") // unrelated chain -> must not be counted
	insertCountTestLog(t, db, interior.Add(time.Minute), "success")
	err := db.Exec(`
		INSERT INTO logs (id, timestamp, object_type, provider, model, status, parent_request_id,
			created_at, latency, cost, prompt_tokens, completion_tokens, total_tokens)
		VALUES (?, ?, 'chat_completion', 'openai', 'gpt-4', 'success', ?, ?, 100, 0.01, 10, 5, 15)
	`, uuid.New().String(), interior.Add(2*time.Minute), parentID, interior).Error
	require.NoError(t, err)

	refreshTestMatViews(t, db)
	store.matViewsReady.Store(true)

	filters := SearchFilters{StartTime: &start, EndTime: &end, ParentRequestID: parentID}
	require.False(t, store.canUseMatViewForFreshAggregate(filters),
		"parent_request_id has no matview dimension; the count must take the raw path")

	result, err := store.SearchLogs(ctx, filters, PaginationOptions{Limit: 50})
	require.NoError(t, err)
	assert.Equal(t, int64(1), result.Pagination.TotalCount,
		"total_count must cover only the fallback chain's rows, not every log in the window")
}

// TestGetStatsMatViewMatchesPaginationTotal verifies the matview stats path is
// exact-range like the pagination count: the Logs page renders both numbers
// for the same filters, so GetStats' TotalRequests must equal SearchLogs'
// total_count (terminal plus in-flight rows inside [start, end]), with rate
// denominators terminal-only, mirroring the raw GetStats path.
func TestGetStatsMatViewMatchesPaginationTotal(t *testing.T) {
	store, db := setupPerfTestDB(t)
	ctx := context.Background()

	day := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
	start := day.Add(10*time.Hour + 30*time.Minute)
	end := day.Add(35*time.Hour + 45*time.Minute) // 25h15m window, matview-eligible

	insertCountTestLog(t, db, day.Add(10*time.Hour+15*time.Minute), "success")   // before start -> excluded
	insertCountTestLog(t, db, day.Add(10*time.Hour+45*time.Minute), "success")   // head sliver -> counted
	insertCountTestLog(t, db, day.Add(18*time.Hour), "success")                  // interior -> counted
	insertCountTestLog(t, db, day.Add(35*time.Hour), "success")                  // tail sliver -> counted
	insertCountTestLog(t, db, day.Add(35*time.Hour+30*time.Minute), "error")     // tail sliver -> counted
	insertCountTestLog(t, db, day.Add(35*time.Hour+50*time.Minute), "success")   // after end -> excluded
	insertCountTestLog(t, db, day.Add(18*time.Hour+5*time.Minute), "processing") // in-flight -> in totals, not in rates

	refreshTestMatViews(t, db)
	store.matViewsReady.Store(true)

	filters := SearchFilters{StartTime: &start, EndTime: &end}
	require.True(t, store.canUseMatViewForFreshAggregate(filters), "window must take the matview stats path")

	stats, err := store.GetStats(ctx, filters)
	require.NoError(t, err)
	searchResult, err := store.SearchLogs(ctx, filters, PaginationOptions{Limit: 50})
	require.NoError(t, err)

	assert.Equal(t, searchResult.Pagination.TotalCount, stats.TotalRequests,
		"stat card total and pagination total render on the same page for the same filters and must agree")
	assert.Equal(t, int64(5), stats.TotalRequests,
		"4 terminal + 1 in-flight log inside [start, end]")
	assert.InDelta(t, 75.0, stats.SuccessRate, 0.001,
		"success rate is terminal-only: 3 successes out of 4 completed")
	require.NotNil(t, stats.CacheHitRateTotalRequests)
	assert.Equal(t, int64(4), *stats.CacheHitRateTotalRequests,
		"cache-hit denominator is the exact-range completed count")
}

// TestGetHistogramMatViewTrimsBoundaryBuckets verifies the matview histogram
// path only counts in-window rows in the first and last display buckets,
// matching the raw histogram path's exact timestamp predicates - previously
// the boundary buckets included every row of their full hour, so the chart
// disagreed with the exact-range stats total for the same filters.
func TestGetHistogramMatViewTrimsBoundaryBuckets(t *testing.T) {
	store, db := setupPerfTestDB(t)
	ctx := context.Background()

	day := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
	start := day.Add(10*time.Hour + 30*time.Minute)
	end := day.Add(35*time.Hour + 45*time.Minute)

	insertCountTestLog(t, db, day.Add(10*time.Hour+15*time.Minute), "success")   // before start -> must not appear
	insertCountTestLog(t, db, day.Add(10*time.Hour+45*time.Minute), "success")   // head sliver -> first bar
	insertCountTestLog(t, db, day.Add(18*time.Hour), "error")                    // interior
	insertCountTestLog(t, db, day.Add(35*time.Hour+30*time.Minute), "success")   // tail sliver -> last bar
	insertCountTestLog(t, db, day.Add(35*time.Hour+50*time.Minute), "success")   // after end -> must not appear
	insertCountTestLog(t, db, day.Add(18*time.Hour+5*time.Minute), "processing") // in-flight -> histograms are terminal-only on both paths

	refreshTestMatViews(t, db)
	store.matViewsReady.Store(true)

	filters := SearchFilters{StartTime: &start, EndTime: &end}
	require.True(t, store.canUseMatView(filters), "filters must take the matview histogram path")

	hist, err := store.GetHistogram(ctx, filters, 3600)
	require.NoError(t, err)

	var total, success, errCount int64
	byBucket := make(map[int64]int64, len(hist.Buckets))
	for _, b := range hist.Buckets {
		total += b.Count
		success += b.Success
		errCount += b.Error
		byBucket[b.Timestamp.Unix()] = b.Count
	}
	assert.Equal(t, int64(3), total, "bars must sum to the terminal logs inside [start, end] only")
	assert.Equal(t, int64(2), success)
	assert.Equal(t, int64(1), errCount)
	assert.Equal(t, int64(1), byBucket[day.Add(10*time.Hour).Unix()],
		"first bar holds only the in-window sliver row, not the pre-start row from the same hour")
	assert.Equal(t, int64(1), byBucket[day.Add(35*time.Hour).Unix()],
		"last bar holds only the in-window sliver row, not the post-end row from the same hour")
}
