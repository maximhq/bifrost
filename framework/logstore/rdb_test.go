package logstore

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// TestModelRoutingColumnsFilter pins that a log records the model router that
// took it, and that the router filters the log list from the raw table rather
// than the hourly view, which does not carry it.
func TestModelRoutingColumnsFilter(t *testing.T) {
	store, _ := newFanoutTestStore(t)
	ctx := context.Background()
	base := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	row := func(id, router, routerName, tier string) {
		opt := func(v string) *string {
			if v == "" {
				return nil
			}
			return &v
		}
		cost := 1.0
		require.NoError(t, store.Create(ctx, &Log{
			ID: id, Timestamp: base, CreatedAt: base, Object: "chat_completion", Provider: "openai", Model: "m",
			Status: "success", Cost: &cost, TotalTokens: 10,
			ModelRouterID: opt(router), ModelRouterName: opt(routerName), ComplexityTier: opt(tier),
		}))
	}
	row("a", "support", "Support", "SIMPLE")
	row("b", "support", "Support", "COMPLEX")
	row("c", "general", "General", "SIMPLE")
	row("d", "", "", "")

	start, end := base.Add(-time.Hour), base.Add(time.Hour)
	filters := SearchFilters{StartTime: &start, EndTime: &end}
	search := func(f SearchFilters) []string {
		res, err := store.SearchLogs(ctx, f, PaginationOptions{Limit: 10})
		require.NoError(t, err)
		ids := make([]string, 0, len(res.Logs))
		for _, l := range res.Logs {
			ids = append(ids, l.ID)
		}
		return ids
	}

	byRouter := filters
	byRouter.ModelRouterIDs = []string{"support"}
	assert.ElementsMatch(t, []string{"a", "b"}, search(byRouter))

	res, err := store.SearchLogs(ctx, byRouter, PaginationOptions{Limit: 1})
	require.NoError(t, err)
	require.Len(t, res.Logs, 1)
	got := res.Logs[0]
	require.NotNil(t, got.ModelRouterName)
	assert.Equal(t, "Support", *got.ModelRouterName)

	assert.False(t, canUseMatViewFilters(byRouter), "a router filter must not be served from the hourly view")

	// The Model Routers page ranks requests by router.
	assert.True(t, ValidRankingDimensions[RankingDimensionModelRouter], "the rankings endpoint accepts model_router")
	rankings, err := store.GetDimensionRankings(ctx, filters, RankingDimensionModelRouter)
	require.NoError(t, err)
	counts := make(map[string]int64, len(rankings.Rankings))
	names := make(map[string]string, len(rankings.Rankings))
	for _, entry := range rankings.Rankings {
		counts[entry.ID], names[entry.ID] = entry.TotalRequests, entry.Name
	}
	assert.Equal(t, map[string]int64{"support": 2, "general": 1}, counts, "a request no router took is not ranked")
	assert.Equal(t, "Support", names["support"])

	// A router's page splits its requests by tier: only that router's rows count.
	assert.True(t, ValidRankingDimensions[RankingDimensionComplexityTier], "the rankings endpoint accepts complexity_tier")
	tiers, err := store.GetDimensionRankings(ctx, byRouter, RankingDimensionComplexityTier)
	require.NoError(t, err)
	tierCounts := make(map[string]int64, len(tiers.Rankings))
	for _, entry := range tiers.Rankings {
		tierCounts[entry.ID] = entry.TotalRequests
	}
	assert.Equal(t, map[string]int64{"SIMPLE": 1, "COMPLEX": 1}, tierCounts, "another router's tiers are not counted")
}

// seedExpiredLogs inserts total rows where every keepEvery-th is fresh and the
// rest are 60 days old, each with a payload large enough to fill pages.
func seedExpiredLogs(t *testing.T, store *RDBLogStore, total, keepEvery int) {
	t.Helper()
	payload := strings.Repeat("x", 4000)
	old := time.Now().UTC().Add(-60 * 24 * time.Hour)
	fresh := time.Now().UTC()
	for i := 0; i < total; i += 500 {
		entries := make([]*Log, 500)
		for j := range entries {
			ts := old
			if (i+j)%keepEvery == 0 {
				ts = fresh
			}
			entries[j] = &Log{ID: fmt.Sprintf("id-%07d", i+j), Timestamp: ts, CreatedAt: ts, Provider: "openai", Model: "gpt-4o", Status: "success", Object: "chat.completion", InputHistory: payload}
		}
		require.NoError(t, store.BatchCreateIfNotExists(context.Background(), entries))
	}
}

func drainRetention(t *testing.T, store *RDBLogStore) {
	t.Helper()
	cutoff := time.Now().UTC().AddDate(0, 0, -30)
	for {
		n, err := store.DeleteLogsBatch(context.Background(), cutoff, 1000)
		require.NoError(t, err)
		if n == 0 {
			return
		}
	}
}

func dbFileSize(t *testing.T, store *RDBLogStore, path string) int64 {
	t.Helper()
	require.NoError(t, store.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)").Error)
	st, err := os.Stat(path)
	require.NoError(t, err)
	return st.Size()
}

// TestSQLiteReclaimSpace_ShrinksFileAfterRetention pins issue #9738: on a new
// logs.db, retention deletes must be followed by a file that shrinks.
func TestSQLiteReclaimSpace_ShrinksFileAfterRetention(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "logs.db")
	store, err := newSqliteLogStore(ctx, &SQLiteConfig{Path: path}, testLogger{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close(ctx) })

	var mode int
	require.NoError(t, store.db.Raw("PRAGMA auto_vacuum").Scan(&mode).Error)
	require.Equal(t, 2, mode, "new databases must use auto_vacuum=INCREMENTAL")

	seedExpiredLogs(t, store, 20000, 10)
	peak := dbFileSize(t, store, path)
	drainRetention(t, store)
	require.Equal(t, peak, dbFileSize(t, store, path), "deletes alone must not shrink the file")

	var freeBefore int64
	require.NoError(t, store.db.Raw("PRAGMA freelist_count").Scan(&freeBefore).Error)
	released, err := store.ReclaimSpace(ctx, 500)
	require.NoError(t, err)
	require.Equal(t, freeBefore, released, "released pages are counted from incremental_vacuum itself")

	after := dbFileSize(t, store, path)
	require.Less(t, after, peak/5, "file should shrink to roughly the live data (peak=%d after=%d)", peak, after)

	var left, free int64
	require.NoError(t, store.db.Model(&Log{}).Count(&left).Error)
	require.EqualValues(t, 2000, left)
	require.NoError(t, store.db.Raw("PRAGMA freelist_count").Scan(&free).Error)
	require.Zero(t, free)
}

// TestSQLiteAutoVacuum_LegacyDatabaseIsMigrated pins that a file created before
// the change (auto_vacuum=NONE) is converted on open with a one-off VACUUM, keeps
// its data, and then shrinks after retention like a new file.
func TestSQLiteAutoVacuum_LegacyDatabaseIsMigrated(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "logs.db")
	legacy, err := gorm.Open(sqlite.Open(path))
	require.NoError(t, err)
	require.NoError(t, legacy.Exec("CREATE TABLE legacy_marker (id INTEGER)").Error)
	require.NoError(t, legacy.Exec("INSERT INTO legacy_marker VALUES (42)").Error)
	sqlDB, err := legacy.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	store, err := newSqliteLogStore(ctx, &SQLiteConfig{Path: path}, testLogger{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close(ctx) })

	var mode int
	require.NoError(t, store.db.Raw("PRAGMA auto_vacuum").Scan(&mode).Error)
	require.Equal(t, 2, mode, "an existing NONE database must be migrated to INCREMENTAL")
	var marker int
	require.NoError(t, store.db.Raw("SELECT id FROM legacy_marker").Scan(&marker).Error)
	require.Equal(t, 42, marker, "VACUUM must keep existing data")

	seedExpiredLogs(t, store, 5000, 10)
	peak := dbFileSize(t, store, path)
	drainRetention(t, store)
	released, err := store.ReclaimSpace(ctx, 500)
	require.NoError(t, err)
	require.Positive(t, released)
	require.Less(t, dbFileSize(t, store, path), peak/5)
}

// TestSQLiteAutoVacuum_CancelledContextStopsMigration pins that the one-off
// VACUUM of a legacy file follows the constructor context: a cancelled start
// returns the context error and leaves the file in NONE mode for the next start.
func TestSQLiteAutoVacuum_CancelledContextStopsMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs.db")
	legacy, err := gorm.Open(sqlite.Open(path))
	require.NoError(t, err)
	require.NoError(t, legacy.Exec("CREATE TABLE legacy_marker (id INTEGER)").Error)
	sqlDB, err := legacy.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = newSqliteLogStore(ctx, &SQLiteConfig{Path: path}, testLogger{})
	require.ErrorIs(t, err, context.Canceled)

	check, err := gorm.Open(sqlite.Open(path))
	require.NoError(t, err)
	checkDB, err := check.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = checkDB.Close() })
	var mode int
	require.NoError(t, check.Raw("PRAGMA auto_vacuum").Scan(&mode).Error)
	require.Zero(t, mode, "a cancelled start must not run the VACUUM")
}

// TestSQLiteReclaimSpace_ShrinksWithoutManualCheckpoint pins that ReclaimSpace
// itself checkpoints the WAL: the main file shrinks while the store stays open
// and no one else checkpoints.
func TestSQLiteReclaimSpace_ShrinksWithoutManualCheckpoint(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "logs.db")
	store, err := newSqliteLogStore(ctx, &SQLiteConfig{Path: path}, testLogger{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close(ctx) })

	seedExpiredLogs(t, store, 4000, 1000)
	drainRetention(t, store)
	peak := dbFileSize(t, store, path) // checkpoints, so the file holds the full peak

	released, err := store.ReclaimSpace(ctx, 500)
	require.NoError(t, err)
	require.Positive(t, released)

	st, err := os.Stat(path) // no checkpoint here
	require.NoError(t, err)
	require.Less(t, st.Size(), peak/2, "ReclaimSpace must leave the file shrunk (peak=%d after=%d)", peak, st.Size())
}

// TestSQLiteAutoVacuum_FullDatabaseKeepsFull pins that reopening a database in
// auto_vacuum=FULL does not switch it to INCREMENTAL, which would stop it
// shrinking on every commit.
func TestSQLiteAutoVacuum_FullDatabaseKeepsFull(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "logs.db")
	full, err := gorm.Open(sqlite.Open(path))
	require.NoError(t, err)
	require.NoError(t, full.Exec("PRAGMA auto_vacuum = FULL").Error)
	require.NoError(t, full.Exec("CREATE TABLE legacy_marker (id INTEGER)").Error)
	sqlDB, err := full.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	store, err := newSqliteLogStore(ctx, &SQLiteConfig{Path: path}, testLogger{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close(ctx) })

	var mode int
	require.NoError(t, store.db.Raw("PRAGMA auto_vacuum").Scan(&mode).Error)
	require.Equal(t, 1, mode)
}

// TestSQLiteDSN_NewConnectionReadsWhileWriterIsOpen pins that opening a new
// connection with the pool DSN needs no write lock: a reader must not wait on a
// transaction that is writing.
func TestSQLiteDSN_NewConnectionReadsWhileWriterIsOpen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "logs.db")
	store, err := newSqliteLogStore(ctx, &SQLiteConfig{Path: path}, testLogger{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close(ctx) })

	tx := store.db.Begin()
	require.NoError(t, tx.Error)
	t.Cleanup(func() { tx.Rollback() })
	now := time.Now().UTC()
	require.NoError(t, tx.Create(&Log{ID: "writer-open", Timestamp: now, CreatedAt: now, Provider: "openai", Model: "gpt-4o", Status: "success", Object: "chat.completion"}).Error)

	reader, err := gorm.Open(sqlite.Open(sqliteDSN(path, 100)))
	require.NoError(t, err)
	readerDB, err := reader.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = readerDB.Close() })

	var n int64
	require.NoError(t, reader.Model(&Log{}).Count(&n).Error)
	require.Zero(t, n)
}

// TestSQLiteReclaimSpace_EmptyFreelistDoesNotWaitForWriter pins that a reclaim
// with nothing to free returns at once instead of waiting for the write lock
// that incremental_vacuum would take while a writer holds a transaction open.
func TestSQLiteReclaimSpace_EmptyFreelistDoesNotWaitForWriter(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "logs.db")
	store, err := newSqliteLogStore(ctx, &SQLiteConfig{Path: path}, testLogger{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close(ctx) })

	tx := store.db.Begin()
	require.NoError(t, tx.Error)
	t.Cleanup(func() { tx.Rollback() })
	now := time.Now().UTC()
	require.NoError(t, tx.Create(&Log{ID: "writer-open", Timestamp: now, CreatedAt: now, Provider: "openai", Model: "gpt-4o", Status: "success", Object: "chat.completion"}).Error)

	limited, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	start := time.Now()
	released, err := store.ReclaimSpace(limited, 500)
	require.NoError(t, err)
	require.Zero(t, released)
	require.Less(t, time.Since(start), time.Second)
}
