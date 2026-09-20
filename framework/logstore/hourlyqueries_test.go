package logstore

import (
	"context"
	"testing"
	"time"

	"github.com/maximhq/bifrost/framework/queryscope"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// TestHourlyQueriesArchivedBoundariesPreserveCounts exercises a one-hour historical
// query after partial raw expiry, including response metadata and list pagination.
func TestHourlyQueriesArchivedBoundariesPreserveCounts(t *testing.T) {
	db, _ := hourlyArchiveTestDB(t)
	ctx := context.Background()
	hour := time.Now().UTC().Truncate(time.Hour).Add(-72 * time.Hour)
	insertHourlyLog(t, db, "expired", hour.Add(10*time.Minute), 3)
	insertHourlyLog(t, db, "retained", hour.Add(20*time.Minute), 7)
	require.NoError(t, db.Exec(`UPDATE logs SET created_at=now() WHERE id='retained'`).Error)
	store := &RDBLogStore{db: db, hourlyArchiveRequested: true}
	deleted, err := store.DeleteLogsBatch(ctx, time.Now().Add(-24*time.Hour), 100)
	require.NoError(t, err)
	require.EqualValues(t, 1, deleted)
	start, end := hour.Add(15*time.Minute), hour.Add(25*time.Minute)
	filters := SearchFilters{StartTime: &start, EndTime: &end}
	stats, err := store.GetStats(ctx, filters)
	require.NoError(t, err)
	require.EqualValues(t, 2, stats.TotalRequests)
	require.InDelta(t, 10, stats.TotalCost, 1e-9)
	require.NotNil(t, stats.AggregationInfo)
	require.Equal(t, "complete", stats.AggregationInfo.Coverage)
	require.True(t, hour.Equal(*stats.AggregationInfo.EffectiveStartTime))
	hist, err := store.GetHistogram(ctx, filters, 60)
	require.NoError(t, err)
	require.EqualValues(t, 3600, hist.BucketSizeSeconds)
	require.Len(t, hist.Buckets, 1)
	require.EqualValues(t, 2, hist.Buckets[0].Count)
	cost, err := store.GetCostHistogram(ctx, filters, 60)
	require.NoError(t, err)
	require.Equal(t, "complete", cost.AggregationInfo.Coverage)
	rankings, err := store.GetModelRankings(ctx, filters)
	require.NoError(t, err)
	require.Len(t, rankings.Rankings, 1)
	// The raw list still has one surviving row, not two archived requests.
	require.NoError(t, migrationAddSafeJsonbFunction(ctx, db, testLogger{}))
	list, err := store.SearchLogs(ctx, filters, PaginationOptions{Limit: 10})
	require.NoError(t, err)
	require.EqualValues(t, 1, list.Pagination.TotalCount)
	require.Len(t, list.Logs, 1)
}

// TestHourlyQueriesUnsupportedFiltersReportRetainedOnly avoids presenting partial
// raw data as complete archived history when per-request fields are required.
func TestHourlyQueriesUnsupportedFiltersReportRetainedOnly(t *testing.T) {
	db, _ := hourlyArchiveTestDB(t)
	ctx := context.Background()
	insertHourlyLog(t, db, "expired", time.Now().Add(-72*time.Hour), 3)
	store := &RDBLogStore{db: db, hourlyArchiveRequested: true}
	_, err := store.DeleteLogsBatch(ctx, time.Now().Add(-24*time.Hour), 100)
	require.NoError(t, err)
	stats, err := store.GetStats(ctx, SearchFilters{RequestID: "expired"})
	require.NoError(t, err)
	require.Zero(t, stats.TotalRequests)
	require.Equal(t, "retained_only", stats.AggregationInfo.Coverage)
}

// TestHourlyQueriesMixArchiveAndLiveBoundary avoids counting a frozen raw subset
// twice while preserving exact boundary filtering for current requests.
func TestHourlyQueriesMixArchiveAndLiveBoundary(t *testing.T) {
	db, _ := hourlyArchiveTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Hour)
	old := now.Add(-72 * time.Hour)
	insertHourlyLog(t, db, "old", old, 3)
	insertHourlyLog(t, db, "inside", now.Add(5*time.Minute), 7)
	insertHourlyLog(t, db, "outside", now.Add(25*time.Minute), 11)
	store := &RDBLogStore{db: db, hourlyArchiveRequested: true}
	_, err := store.DeleteLogsBatch(ctx, now.Add(-24*time.Hour), 100)
	require.NoError(t, err)
	end := now.Add(10 * time.Minute)
	stats, err := store.GetStats(ctx, SearchFilters{StartTime: &old, EndTime: &end})
	require.NoError(t, err)
	require.EqualValues(t, 2, stats.TotalRequests)
	require.InDelta(t, 10, stats.TotalCost, 1e-9)
	costs, err := store.GetCostHistogram(ctx, SearchFilters{StartTime: &old, EndTime: &end}, 3600)
	require.NoError(t, err)
	var total float64
	for _, bucket := range costs.Buckets {
		total += bucket.TotalCost
	}
	require.InDelta(t, 10, total, 1e-9)
}

// TestHourlyQueriesKeepAccessScope applies visibility to both archived aggregates
// and the derived raw-boundary relation used for costs and rankings.
func TestHourlyQueriesKeepAccessScope(t *testing.T) {
	db, _ := hourlyArchiveTestDB(t)
	stamp := time.Now().UTC().Truncate(time.Hour).Add(-72 * time.Hour)
	insertHourlyLog(t, db, "allowed", stamp, 3)
	insertHourlyLog(t, db, "hidden", stamp, 100)
	require.NoError(t, db.Exec(`UPDATE logs SET user_id=id`).Error)
	store := &RDBLogStore{db: db, hourlyArchiveRequested: true}
	_, err := store.DeleteLogsBatch(context.Background(), time.Now().Add(-24*time.Hour), 100)
	require.NoError(t, err)
	ctx := queryscope.WithQueryScope(context.Background(), func(db *gorm.DB) *gorm.DB {
		return db.Where("user_id = ?", "allowed")
	})
	start, end := stamp.Add(15*time.Minute), stamp.Add(30*time.Minute)
	filters := SearchFilters{StartTime: &start, EndTime: &end}
	stats, err := store.GetStats(ctx, filters)
	require.NoError(t, err)
	require.EqualValues(t, 1, stats.TotalRequests)
	require.InDelta(t, 3, stats.TotalCost, 1e-9)
	costs, err := store.GetCostHistogram(ctx, filters, 3600)
	require.NoError(t, err)
	require.Len(t, costs.Buckets, 1)
	require.InDelta(t, 3, costs.Buckets[0].TotalCost, 1e-9)
}
