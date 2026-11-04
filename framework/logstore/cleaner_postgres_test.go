package logstore

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// setupRetentionPostgres migrates a fresh logs table holding expired old rows and fresh new ones.
func setupRetentionPostgres(t *testing.T, expired, fresh int) (*RDBLogStore, *gorm.DB) {
	t.Helper()
	db := trySetupPostgresDB(t)
	if db == nil {
		t.Skip("Postgres not available")
	}
	ctx := context.Background()
	dropAllManagedMatViews(db)
	require.NoError(t, db.Exec("DROP TABLE IF EXISTS mcp_tool_logs CASCADE").Error)
	require.NoError(t, db.Exec("DROP TABLE IF EXISTS async_jobs CASCADE").Error)
	require.NoError(t, db.Exec("DROP TABLE IF EXISTS webhook_deliveries CASCADE").Error)
	require.NoError(t, db.Exec("DROP TABLE IF EXISTS logs CASCADE").Error)
	require.NoError(t, db.Exec("CREATE TABLE IF NOT EXISTS migrations (id VARCHAR(255) PRIMARY KEY)").Error)
	require.NoError(t, db.Exec("DELETE FROM migrations").Error)
	require.NoError(t, triggerMigrations(ctx, db, testLogger{}))
	t.Cleanup(func() { _ = db.Exec("TRUNCATE logs").Error })

	seed := `INSERT INTO logs (id, timestamp, created_at, provider, model, status, object_type)
		SELECT ? || g, NOW() - ?::interval + g * INTERVAL '1 millisecond', NOW() - ?::interval + g * INTERVAL '1 millisecond',
			'openai', 'gpt-4o', 'success', 'chat.completion'
		FROM generate_series(1, ?) AS g`
	require.NoError(t, db.Exec(seed, "old-", "40 days", "40 days", expired).Error)
	require.NoError(t, db.Exec(seed, "new-", "1 day", "1 day", fresh).Error)
	return &RDBLogStore{db: db, logger: testLogger{}}, db
}

func countLogs(t *testing.T, db *gorm.DB, prefix string) int64 {
	t.Helper()
	var n int64
	require.NoError(t, db.Raw("SELECT count(*) FROM logs WHERE id LIKE ?", prefix+"%").Scan(&n).Error)
	return n
}

// Replicas yielding to each other on the advisory lock must still drain every expired row between them.
func TestPostgresConcurrentCleanersDrainEverything(t *testing.T) {
	store, db := setupRetentionPostgres(t, 3*batchSize+123, 50)
	cutoff := time.Now().UTC().AddDate(0, 0, -30)

	const replicas = 8
	var wg sync.WaitGroup
	results := make([]bool, replicas)
	for i := range replicas {
		wg.Go(func() {
			cleaner := NewLogsCleaner(store, CleanerConfig{RetentionDays: 30}, testLogger{})
			results[i] = cleaner.drainExpired(context.Background(), "logs", cutoff, store.DeleteLogsBatch)
		})
	}
	wg.Wait()

	for i, ok := range results {
		assert.True(t, ok, "replica %d must finish or yield, not fail", i)
	}
	assert.Zero(t, countLogs(t, db, "old-"), "every expired row must be deleted")
	assert.Equal(t, int64(50), countLogs(t, db, "new-"), "rows inside retention must survive")
}

// A replica that finds the table's retention lock taken skips the batch at
// once instead of queueing behind the holder.
func TestPostgresRetentionBatchYieldsWhileLockHeld(t *testing.T) {
	store, db := setupRetentionPostgres(t, 20, 0)
	cutoff := time.Now().UTC().AddDate(0, 0, -30)

	holder := db.Begin()
	require.NoError(t, holder.Error)
	var acquired bool
	require.NoError(t, holder.Raw("SELECT pg_try_advisory_xact_lock(?)", retentionAdvisoryLockKeys["logs"]).Row().Scan(&acquired))
	require.True(t, acquired)

	_, err := store.DeleteLogsBatch(context.Background(), cutoff, batchSize)
	assert.ErrorIs(t, err, ErrRetentionBusy)
	assert.Equal(t, int64(20), countLogs(t, db, "old-"))

	other, err := store.DeleteMCPToolLogsBatch(context.Background(), cutoff, batchSize)
	assert.NoError(t, err, "each table has its own lock")
	assert.Zero(t, other)

	require.NoError(t, holder.Commit().Error)
	deleted, err := store.DeleteLogsBatch(context.Background(), cutoff, batchSize)
	require.NoError(t, err)
	assert.Equal(t, int64(20), deleted, "the lock goes with the holder's transaction")
}

// An operator's own pruner holding row locks must not stall the cleaner: the
// batch skips those rows and deletes the rest.
func TestPostgresRetentionBatchSkipsLockedRows(t *testing.T) {
	store, db := setupRetentionPostgres(t, 30, 0)
	cutoff := time.Now().UTC().AddDate(0, 0, -30)

	pruner := db.Begin()
	require.NoError(t, pruner.Error)
	defer pruner.Rollback()
	require.NoError(t, pruner.Exec("SELECT id FROM logs WHERE id LIKE 'old-%' ORDER BY created_at LIMIT 10 FOR UPDATE").Error)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	deleted, err := store.DeleteLogsBatch(ctx, cutoff, batchSize)
	require.NoError(t, err, "the batch must not wait on the pruner's row locks")
	assert.Equal(t, int64(20), deleted)

	require.NoError(t, pruner.Rollback().Error)
	deleted, err = store.DeleteLogsBatch(context.Background(), cutoff, batchSize)
	require.NoError(t, err)
	assert.Equal(t, int64(10), deleted, "the skipped rows go on a later batch")
}
