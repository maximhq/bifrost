package logstore

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestHourlyRetentionPreservesPartialHour verifies created_at retention cannot
// overwrite a complete archived hour with its remaining subset of raw rows.
func TestHourlyRetentionPreservesPartialHour(t *testing.T) {
	db, conn := hourlyArchiveTestDB(t)
	ctx := context.Background()
	hour := time.Now().UTC().Truncate(time.Hour).Add(-72 * time.Hour)
	insertHourlyLog(t, db, "expire", hour, 3)
	insertHourlyLog(t, db, "retain", hour.Add(20*time.Minute), 7)
	require.NoError(t, db.Exec(`UPDATE logs SET created_at=now() WHERE id='retain'`).Error)
	store := &RDBLogStore{db: db}
	deleted, err := store.DeleteLogsBatch(ctx, time.Now().Add(-24*time.Hour), 100)
	require.NoError(t, err)
	require.EqualValues(t, 1, deleted)
	assertHourlyTotals(t, db, 2, 10)
	require.NoError(t, refreshHourlyArchive(ctx, conn))
	assertHourlyTotals(t, db, 2, 10)
	// Once frozen, future edits to surviving rows do not destroy the archive.
	require.NoError(t, db.Exec(`UPDATE logs SET cost=100 WHERE id='retain'`).Error)
	require.NoError(t, refreshHourlyArchive(ctx, conn))
	assertHourlyTotals(t, db, 2, 10)
	deleted, err = store.DeleteLogsBatch(ctx, time.Now().Add(time.Hour), 100)
	require.NoError(t, err)
	require.EqualValues(t, 1, deleted)
	require.NoError(t, db.Exec(`DROP MATERIALIZED VIEW mv_logs_hourly`).Error)
	require.NoError(t, ensureMatViews(ctx, db))
	assertHourlyTotals(t, db, 2, 10)
}

// TestHourlyRetentionDefersIncompleteHours keeps active and unfinished inputs.
func TestHourlyRetentionDefersIncompleteHours(t *testing.T) {
	db, _ := hourlyArchiveTestDB(t)
	ctx := context.Background()
	old := time.Now().Add(-72 * time.Hour)
	insertHourlyLog(t, db, "processing", old, 3)
	insertHourlyLog(t, db, "recent", time.Now(), 7)
	require.NoError(t, db.Exec(`UPDATE logs SET status='processing' WHERE id='processing'`).Error)
	require.NoError(t, db.Exec(`UPDATE logs SET created_at=?`, old).Error)
	store := &RDBLogStore{db: db}
	deleted, err := store.DeleteLogsBatch(ctx, time.Now().Add(-24*time.Hour), 100)
	require.NoError(t, err)
	require.Zero(t, deleted)
	require.NoError(t, db.Exec(`UPDATE logs SET status='success' WHERE id='processing'`).Error)
	deleted, err = store.DeleteLogsBatch(ctx, time.Now().Add(-24*time.Hour), 100)
	require.NoError(t, err)
	require.EqualValues(t, 1, deleted)
	assertHourlyTotals(t, db, 2, 10)
}

// TestHourlyRetentionDisabledMaintenanceProtectsMutableData prevents expiry while paused.
func TestHourlyRetentionDisabledMaintenanceProtectsMutableData(t *testing.T) {
	db, _ := hourlyArchiveTestDB(t)
	insertHourlyLog(t, db, "old", time.Now().Add(-72*time.Hour), 3)
	store := &RDBLogStore{db: db, matViewMaintenanceDisabled: true}
	deleted, err := store.DeleteLogsBatch(context.Background(), time.Now().Add(-24*time.Hour), 100)
	require.NoError(t, err)
	require.Zero(t, deleted)
}

// TestHourlyRetentionWaitsForInitialization protects logs during asynchronous startup.
func TestHourlyRetentionWaitsForInitialization(t *testing.T) {
	db, _ := hourlyTestDB(t)
	insertHourlyLog(t, db, "old", time.Now().Add(-72*time.Hour), 3)
	store := &RDBLogStore{db: db, hourlyArchiveRequested: true}
	deleted, err := store.DeleteLogsBatch(context.Background(), time.Now(), 100)
	require.ErrorContains(t, err, "initialization")
	require.Zero(t, deleted)
}
