package logstore

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// hourlyArchiveTestDB creates fresh archive storage without legacy frozen hours.
func hourlyArchiveTestDB(t *testing.T) (*gorm.DB, *sql.Conn) {
	t.Helper()
	db, sqlDB := hourlyTestDB(t)
	require.NoError(t, db.Exec(mvLogsHourlyDDL).Error)
	conn, err := sqlDB.Conn(context.Background())
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	require.NoError(t, initializeHourlyArchive(context.Background(), conn, false))
	return db, conn
}

// insertHourlyLog creates a complete terminal log with explicit aggregate inputs.
func insertHourlyLog(t *testing.T, db *gorm.DB, id string, stamp time.Time, cost float64) {
	t.Helper()
	require.NoError(t, db.Exec(`INSERT INTO logs
	 (id,timestamp,created_at,status,provider,model,object_type,selected_key_id,latency,cost)
	 VALUES (?,?,?,'success','openai','test','chat_completion','',100,?)`, id, stamp, stamp, cost).Error)
}

// assertHourlyTotals compares the published request count and cost with expectations.
func assertHourlyTotals(t *testing.T, db *gorm.DB, count int64, cost float64) {
	t.Helper()
	var got struct {
		Count int64
		Cost  float64
	}
	require.NoError(t, db.Raw(`SELECT COALESCE(SUM(count),0) AS count,COALESCE(SUM(total_cost),0) AS cost FROM mv_logs_hourly`).Scan(&got).Error)
	require.Equal(t, count, got.Count)
	require.InDelta(t, cost, got.Cost, 1e-9)
}

// TestHourlyArchiveReplacesChangedHours verifies corrections, deletes and idempotency.
func TestHourlyArchiveReplacesChangedHours(t *testing.T) {
	db, conn := hourlyArchiveTestDB(t)
	ctx := context.Background()
	old := time.Now().Add(-72 * time.Hour)
	insertHourlyLog(t, db, "old", old, 2)
	insertHourlyLog(t, db, "recent", time.Now(), 3)
	require.NoError(t, refreshHourlyArchive(ctx, conn))
	assertHourlyTotals(t, db, 2, 5)
	require.NoError(t, db.Exec(`UPDATE logs SET cost=7, model='changed' WHERE id='old'`).Error)
	require.NoError(t, refreshHourlyArchive(ctx, conn))
	assertHourlyTotals(t, db, 2, 10)
	require.NoError(t, refreshHourlyArchive(ctx, conn))
	assertHourlyTotals(t, db, 2, 10)
	require.NoError(t, db.Exec(`DELETE FROM logs WHERE id='old'`).Error)
	require.NoError(t, refreshHourlyArchive(ctx, conn))
	assertHourlyTotals(t, db, 1, 3)
	var obsolete int64
	require.NoError(t, db.Table("mv_logs_hourly").Where("model = ?", "changed").Count(&obsolete).Error)
	require.Zero(t, obsolete)
}

// TestHourlyArchivePreservesLegacyBaseline keeps historical values after raw expiry.
func TestHourlyArchivePreservesLegacyBaseline(t *testing.T) {
	db, sqlDB := hourlyTestDB(t)
	insertHourlyLog(t, db, "old", time.Now().Add(-72*time.Hour), 11)
	require.NoError(t, db.Exec(mvLogsHourlyDDL).Error)
	require.NoError(t, db.Exec(`DELETE FROM logs`).Error)
	conn, err := sqlDB.Conn(context.Background())
	require.NoError(t, err)
	defer conn.Close()
	require.NoError(t, initializeHourlyArchive(context.Background(), conn, true))
	require.NoError(t, refreshHourlyArchive(context.Background(), conn))
	assertHourlyTotals(t, db, 1, 11)
	// Re-running startup must never recreate the archive from the empty logs table.
	require.NoError(t, ensureMatViews(context.Background(), db))
	assertHourlyTotals(t, db, 1, 11)
}

// TestHourlyArchiveCanceledRefreshPreservesPublication proves retries retain dirty work.
func TestHourlyArchiveCanceledRefreshPreservesPublication(t *testing.T) {
	db, conn := hourlyArchiveTestDB(t)
	insertHourlyLog(t, db, "a", time.Now(), 4)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.Error(t, refreshHourlyArchive(ctx, conn))
	assertHourlyTotals(t, db, 0, 0)
	require.NoError(t, refreshHourlyArchive(context.Background(), conn))
	assertHourlyTotals(t, db, 1, 4)
}

// TestHourlyArchiveRejectsDestructiveRepair prevents archived history being rebuilt raw.
func TestHourlyArchiveRejectsDestructiveRepair(t *testing.T) {
	db, conn := hourlyArchiveTestDB(t)
	insertHourlyLog(t, db, "a", time.Now(), 4)
	require.NoError(t, refreshHourlyArchive(context.Background(), conn))
	require.NoError(t, db.Exec(`ALTER MATERIALIZED VIEW mv_logs_hourly RENAME COLUMN total_cost TO incompatible_cost`).Error)
	require.Error(t, ensureMatViews(context.Background(), db))
	var snapshotExists bool
	require.NoError(t, db.Raw(`SELECT to_regclass('mv_logs_hourly_snapshot') IS NOT NULL`).Scan(&snapshotExists).Error)
	require.True(t, snapshotExists)
}

// TestHourlyArchiveRestoresPublishedCopy rebuilds mutable hours from available logs.
func TestHourlyArchiveRestoresPublishedCopy(t *testing.T) {
	db, conn := hourlyArchiveTestDB(t)
	insertHourlyLog(t, db, "a", time.Now(), 4)
	require.NoError(t, refreshHourlyArchive(context.Background(), conn))
	require.NoError(t, db.Exec(`DROP MATERIALIZED VIEW mv_logs_hourly`).Error)
	require.NoError(t, ensureMatViews(context.Background(), db))
	assertHourlyTotals(t, db, 1, 4)
}
