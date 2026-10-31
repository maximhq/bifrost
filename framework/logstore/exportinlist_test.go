package logstore

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Each count is one more than the database's per-statement bind parameter limit
// (SQLite 32,766, Postgres 65,535), so a previous-period query that binds one
// parameter per ranked id fails on that database.
const (
	exportInListUsersSQLite   = 32_767
	exportInListUsersPostgres = 65_536
)

// seedExportInListLogs inserts one current-period and one previous-period log for
// each of n distinct users and virtual keys, and returns filters covering the
// current period with the ranking limit disabled (export mode).
func seedExportInListLogs(t *testing.T, ctx context.Context, store *RDBLogStore, n int) SearchFilters {
	t.Helper()
	end := time.Now().UTC().Truncate(time.Second)
	start := end.Add(-time.Hour)
	entries := make([]*Log, 0, 2*n)
	for i := 0; i < n; i++ {
		userID := fmt.Sprintf("user-%05d", i)
		vkID := fmt.Sprintf("vk-%05d", i)
		for period, ts := range []time.Time{start.Add(30 * time.Minute), start.Add(-30 * time.Minute)} {
			entries = append(entries, &Log{
				ID:           fmt.Sprintf("log-%05d-%d", i, period),
				Timestamp:    ts,
				Provider:     "openai",
				Model:        "gpt-4o",
				Status:       "success",
				Object:       "chat.completion",
				UserID:       &userID,
				VirtualKeyID: &vkID,
			})
		}
	}
	require.NoError(t, store.BatchCreateIfNotExists(ctx, entries))
	unlimited := 0
	return SearchFilters{StartTime: &start, EndTime: &end, RankingLimit: &unlimited}
}

// assertExportRankingsCoverAllIDs runs the export-mode ranking queries against a
// store seeded by seedExportInListLogs and checks every one of the n ids comes
// back with its previous-period trend.
func assertExportRankingsCoverAllIDs(t *testing.T, ctx context.Context, store *RDBLogStore, filters SearchFilters, n int) {
	t.Helper()
	t.Run("GetUserRankings", func(t *testing.T) {
		res, err := store.GetUserRankings(ctx, filters)
		require.NoError(t, err)
		require.Len(t, res.Rankings, n)
		for _, r := range res.Rankings {
			require.True(t, r.Trend.HasPreviousPeriod, "user %s missing previous period", r.UserID)
		}
	})

	for _, dim := range []RankingDimension{RankingDimensionUser, RankingDimensionVirtualKey} {
		t.Run("GetDimensionRankings/"+string(dim), func(t *testing.T) {
			res, err := store.GetDimensionRankings(ctx, filters, dim)
			require.NoError(t, err)
			require.Len(t, res.Rankings, n)
			for _, r := range res.Rankings {
				require.True(t, r.Trend.HasPreviousPeriod, "%s %s missing previous period", dim, r.ID)
			}
		})
	}
}

// TestRankingsExport_PreviousPeriodInListOverParamLimit pins that the export
// (RankingLimit <= 0) ranking queries still work on SQLite when the
// previous-period id filter holds more ids than the database accepts as bind
// parameters. Before the fix each id was bound as its own parameter and SQLite
// rejected the statement with "too many SQL variables".
func TestRankingsExport_PreviousPeriodInListOverParamLimit(t *testing.T) {
	if testing.Short() {
		t.Skip("inserts 65k rows")
	}
	ctx := context.Background()
	store, err := newSqliteLogStore(ctx, &SQLiteConfig{Path: filepath.Join(t.TempDir(), "export.db")}, testLogger{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close(ctx) })
	filters := seedExportInListLogs(t, ctx, store, exportInListUsersSQLite)
	assertExportRankingsCoverAllIDs(t, ctx, store, filters, exportInListUsersSQLite)
}

// TestRankingsExport_PreviousPeriodInListOverParamLimitPostgres is the Postgres
// counterpart: above 65,535 ids the old per-id binding failed with "extended
// protocol limited to 65535 parameters". Skips when Postgres is unavailable.
func TestRankingsExport_PreviousPeriodInListOverParamLimitPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("inserts 131k rows")
	}
	db := trySetupPostgresDB(t)
	if db == nil {
		t.Skip("Postgres not available")
	}
	ctx := context.Background()
	// Clean slate with no matviews, so every ranking takes the raw-table path
	// that carries the previous-period id filter.
	dropAllManagedMatViews(db)
	require.NoError(t, db.Exec("DROP TABLE IF EXISTS mcp_tool_logs CASCADE").Error)
	require.NoError(t, db.Exec("DROP TABLE IF EXISTS async_jobs CASCADE").Error)
	require.NoError(t, db.Exec("DROP TABLE IF EXISTS webhook_deliveries CASCADE").Error)
	require.NoError(t, db.Exec("DROP TABLE IF EXISTS logs CASCADE").Error)
	require.NoError(t, db.Exec("CREATE TABLE IF NOT EXISTS migrations (id VARCHAR(255) PRIMARY KEY)").Error)
	require.NoError(t, db.Exec("DELETE FROM migrations").Error)
	require.NoError(t, triggerMigrations(ctx, db, testLogger{}))
	t.Cleanup(func() { _ = db.Exec("TRUNCATE logs").Error })
	store := &RDBLogStore{db: db, logger: testLogger{}}

	filters := seedExportInListLogs(t, ctx, store, exportInListUsersPostgres)
	assertExportRankingsCoverAllIDs(t, ctx, store, filters, exportInListUsersPostgres)
}
