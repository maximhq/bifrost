package logstore

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// hfPGIndexValid reports whether a named index exists on table and is valid.
func hfPGIndexValid(t *testing.T, db *gorm.DB, table, name string) bool {
	t.Helper()
	var valid bool
	require.NoError(t, db.Raw(`
		SELECT COALESCE(bool_and(pi.indisvalid), false)
		FROM pg_class pc
		JOIN pg_index pi ON pi.indrelid = pc.oid
		JOIN pg_class ic ON ic.oid = pi.indexrelid
		JOIN pg_namespace n ON n.oid = pc.relnamespace
		WHERE n.nspname = current_schema() AND pc.relname = ? AND ic.relname = ?
	`, table, name).Scan(&valid).Error)
	return valid
}

// hfPGIndexExists reports whether a named index exists in the test schema.
func hfPGIndexExists(t *testing.T, db *gorm.DB, name string) bool {
	t.Helper()
	var n int64
	require.NoError(t, db.Raw(`SELECT COUNT(*) FROM pg_indexes WHERE schemaname = current_schema() AND indexname = ?`, name).Scan(&n).Error)
	return n > 0
}

// TestHighFixesPGOwnerTimestampIndexesReplaceSingles simulates an upgraded
// database (single-column owner indexes, no composites) and runs the
// background builder: the composites are built valid and the replaced
// single-column indexes are dropped only afterwards.
func TestHighFixesPGOwnerTimestampIndexesReplaceSingles(t *testing.T) {
	_, db := setupPerfTestDB(t)
	ctx := context.Background()

	for table, names := range hfOwnerTimestampIndexes {
		for _, name := range names {
			require.NoError(t, db.Exec("DROP INDEX IF EXISTS "+name).Error)
		}
		for i, name := range hfReplacedSingleIndexes[table] {
			col := []string{"virtual_key_id", "user_id", "team_id"}[i]
			require.NoError(t, db.Exec("CREATE INDEX IF NOT EXISTS "+name+" ON "+table+"("+col+")").Error)
		}
	}

	conn := acquirePerfTestSQLConn(t, ctx, db)
	require.NoError(t, ensureOwnerTimestampIndexes(ctx, conn, testLogger{}))
	// Idempotent: a second pass with everything already in place is a no-op.
	require.NoError(t, ensureOwnerTimestampIndexes(ctx, conn, testLogger{}))

	for table, names := range hfOwnerTimestampIndexes {
		for _, name := range names {
			assert.True(t, hfPGIndexValid(t, db, table, name), "%s must be built and valid", name)
		}
		for _, name := range hfReplacedSingleIndexes[table] {
			assert.False(t, hfPGIndexExists(t, db, name), "%s must be dropped once its composite is valid", name)
		}
	}

	// The performance-index pass must not rebuild the dropped single-column indexes.
	require.NoError(t, ensurePerformanceIndexes(ctx, conn, testLogger{}))
	for _, names := range hfReplacedSingleIndexes {
		for _, name := range names {
			assert.False(t, hfPGIndexExists(t, db, name), "%s must stay dropped after ensurePerformanceIndexes", name)
		}
	}
}

// TestHighFixesPGRankingNameIndexesReplaceSingles simulates an upgraded
// database (single-column id indexes, no composites) and runs the background
// builder: the ranking-name composites are built valid, the replaced singles
// are dropped only afterwards, and the performance-index pass does not bring
// them back.
func TestHighFixesPGRankingNameIndexesReplaceSingles(t *testing.T) {
	_, db := setupPerfTestDB(t)
	ctx := context.Background()
	for _, idx := range hfRankingNameIndexes {
		require.NoError(t, db.Exec("DROP INDEX IF EXISTS "+idx.name).Error)
		require.NoError(t, db.Exec("CREATE INDEX IF NOT EXISTS "+idx.replaces+" ON logs("+idx.column+")").Error)
	}

	conn := acquirePerfTestSQLConn(t, ctx, db)
	require.NoError(t, ensureOwnerTimestampIndexes(ctx, conn, testLogger{}))
	require.NoError(t, ensureOwnerTimestampIndexes(ctx, conn, testLogger{}))
	require.NoError(t, ensurePerformanceIndexes(ctx, conn, testLogger{}))

	for _, idx := range hfRankingNameIndexes {
		assert.True(t, hfPGIndexValid(t, db, "logs", idx.name), "%s must be built and valid", idx.name)
		assert.False(t, hfPGIndexExists(t, db, idx.replaces), "%s must be dropped once its composite is valid", idx.replaces)
	}
}

// TestHighFixesPGRankingNameFallbackWalksIndexPerID pins that the raw-table
// name fallback reads one row per id through the (id, timestamp) composite,
// newest first, instead of every row the id ever logged: on a planner-realistic
// table its plan has a per-id Limit over idx_logs_selected_key_ts. It still
// returns the latest name across all history. 2,000 keys over 50k rows give the
// planner the per-key selectivity of a real fleet of keys or rules.
func TestHighFixesPGRankingNameFallbackWalksIndexPerID(t *testing.T) {
	store, db := setupPerfTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	require.NoError(t, db.Exec(`
		INSERT INTO logs (id, timestamp, object_type, provider, model, status, created_at, selected_key_id, selected_key_name)
		SELECT 'rn-' || g, ?::timestamp - (g || ' seconds')::interval, 'chat_completion', 'openai', 'gpt-4o', 'success',
		       ?::timestamp, 'key-' || (g % 2000), 'Key old ' || (g % 2000)
		FROM generate_series(1, 50000) AS g
	`, now, now).Error)
	hfInsertNamedLogWithKey(t, db, "rn-latest", now.Add(time.Minute), "key-3", "Key three")
	require.NoError(t, db.Exec("ANALYZE logs").Error)

	capture := hfCaptureQueries(t, db)
	names := store.resolveRankingNames(ctx, "selected_key_id", "selected_key_name", []string{"key-3", "key-missing"})
	assert.Equal(t, map[string]string{"key-3": "Key three"}, names, "the fallback returns the latest name across all history")

	var lookup string
	for _, st := range capture.snapshot() {
		if strings.Contains(st.sql, "selected_key_name") {
			lookup = st.rendered
		}
	}
	require.NotEmpty(t, lookup, "the fallback query was not captured")
	plan := hfExplain(t, db, lookup)
	assert.Contains(t, plan, "Limit", "each id must stop at its newest named row; plan:\n%s", plan)
	assert.Contains(t, plan, "idx_logs_selected_key_ts", "the lookup must walk the (selected_key_id, timestamp) composite; plan:\n%s", plan)
}

// hfInsertNamedLogWithKey writes one logs row carrying a provider key id and name.
func hfInsertNamedLogWithKey(t *testing.T, db *gorm.DB, id string, ts time.Time, keyID, keyName string) {
	t.Helper()
	require.NoError(t, db.Exec(`
		INSERT INTO logs (id, timestamp, object_type, provider, model, status, created_at, selected_key_id, selected_key_name)
		VALUES (?, ?, 'chat_completion', 'openai', 'gpt-4o', 'success', ?, ?, ?)
	`, id, ts, ts, keyID, keyName).Error)
}

// hfExplain returns the text plan of query.
func hfExplain(t *testing.T, db *gorm.DB, query string, args ...any) string {
	t.Helper()
	var lines []string
	require.NoError(t, db.Raw("EXPLAIN "+query, args...).Scan(&lines).Error)
	return strings.Join(lines, "\n")
}

// TestHighFixesPGOwnerListUsesComposite pins H4 on a planner-realistic table:
// with 50k rows spread over 50 virtual keys and users, the logs list query
// (owner filter, time window, ORDER BY timestamp DESC, id DESC LIMIT) walks
// the (owner, timestamp) composite instead of a single-column index plus sort.
func TestHighFixesPGOwnerListUsesComposite(t *testing.T) {
	_, db := setupPerfTestDB(t)
	now := time.Now().UTC()
	require.NoError(t, db.Exec(`
		INSERT INTO logs (id, timestamp, object_type, provider, model, status, created_at, virtual_key_id, user_id, team_id)
		SELECT 'hf-' || g, ?::timestamp - (g || ' seconds')::interval, 'chat_completion', 'openai', 'gpt-4o', 'success',
		       ?::timestamp, 'vk-' || (g % 50), 'user-' || (g % 50), 'team-' || (g % 50)
		FROM generate_series(1, 50000) AS g
	`, now, now).Error)
	require.NoError(t, db.Exec("ANALYZE logs").Error)

	start := now.Add(-30 * 24 * time.Hour)
	for _, c := range []struct{ col, value, idx string }{
		{"virtual_key_id", "vk-7", "idx_logs_vk_ts"},
		{"user_id", "user-7", "idx_logs_user_ts"},
	} {
		plan := hfExplain(t, db,
			"SELECT id FROM logs WHERE "+c.col+" IN (?) AND timestamp >= ? AND timestamp <= ? ORDER BY timestamp DESC, id DESC LIMIT 50",
			c.value, start, now)
		assert.Contains(t, plan, c.idx, "list filtered by %s must use %s; plan:\n%s", c.col, c.idx, plan)
	}

	require.NoError(t, db.Exec(`
		INSERT INTO mcp_tool_logs (id, request_id, timestamp, tool_name, status, created_at, virtual_key_id, user_id)
		SELECT 'hfm-' || g, 'hfm-' || g, ?::timestamp - (g || ' seconds')::interval, 'search', 'success', ?::timestamp,
		       'vk-' || (g % 50), 'user-' || (g % 50)
		FROM generate_series(1, 50000) AS g
	`, now, now).Error)
	require.NoError(t, db.Exec("ANALYZE mcp_tool_logs").Error)
	plan := hfExplain(t, db,
		"SELECT id FROM mcp_tool_logs WHERE virtual_key_id IN (?) AND timestamp >= ? AND timestamp <= ? ORDER BY timestamp DESC LIMIT 50",
		"vk-7", start, now)
	assert.Contains(t, plan, "idx_mcp_logs_vk_ts", "MCP list filtered by virtual key must use the composite; plan:\n%s", plan)
}

// hfInsertNamedLog writes one logs row with owner ids and names for ranking tests.
func hfInsertNamedLog(t *testing.T, db *gorm.DB, id string, ts time.Time, vkID, vkName, app string) {
	t.Helper()
	require.NoError(t, db.Exec(`
		INSERT INTO logs (id, timestamp, object_type, provider, model, status, created_at,
			virtual_key_id, virtual_key_name, app, cost, prompt_tokens, completion_tokens, total_tokens)
		VALUES (?, ?, 'chat_completion', 'openai', 'gpt-4o', 'success', ?, ?, ?, ?, 0.01, 10, 5, 15)
	`, id, ts, ts, vkID, vkName, app).Error)
}

// TestHighFixesPGRankingNamesAvoidFullHistoryScan pins H7: dimension rankings
// served from mv_logs_hourly resolve display names from the dimension's
// mv_filter_* view (or skip the lookup when the id is the name) instead of a
// DISTINCT ON over the whole logs history, and return the same names.
func TestHighFixesPGRankingNamesAvoidFullHistoryScan(t *testing.T) {
	store, db := setupPerfTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	hfInsertNamedLog(t, db, "r1", now.Add(-3*time.Hour), "vk-a", "Alpha", "claude-code")
	hfInsertNamedLog(t, db, "r2", now.Add(-2*time.Hour), "vk-a", "Alpha", "claude-code")
	hfInsertNamedLog(t, db, "r3", now.Add(-2*time.Hour), "vk-b", "Beta", "codex")
	// vk-c was renamed: the latest name wins, as before.
	hfInsertNamedLog(t, db, "r4", now.Add(-5*time.Hour), "vk-c", "Gamma old", "codex")
	hfInsertNamedLog(t, db, "r5", now.Add(-1*time.Hour), "vk-c", "Gamma", "codex")

	refreshTestMatViews(t, db)
	store.matViewsReady.Store(true)

	start := now.Add(-24 * time.Hour)
	end := now
	filters := SearchFilters{StartTime: &start, EndTime: &end}

	capture := hfCaptureQueries(t, db)
	res, err := store.getDimensionRankingsFromMatView(ctx, filters, RankingDimensionVirtualKey)
	require.NoError(t, err)
	names := map[string]string{}
	for _, r := range res.Rankings {
		names[r.ID] = r.Name
	}
	assert.Equal(t, map[string]string{"vk-a": "Alpha", "vk-b": "Beta", "vk-c": "Gamma"}, names)

	// Only the id the view could not name (vk-c, renamed inside the view's
	// window) reaches the raw-table lookup, and it runs exactly once as the
	// unnest ... CROSS JOIN LATERAL walk; the ids the view resolved never do.
	var fallbacks int
	for _, s := range capture.snapshot() {
		if !strings.Contains(s.sql, "CROSS JOIN LATERAL") {
			continue
		}
		fallbacks++
		assert.Contains(t, s.rendered, "vk-c", "the raw lookup is for the id the view could not name: %s", s.rendered)
		assert.NotContains(t, s.rendered, "vk-a", "an id the view named must not reach the raw lookup: %s", s.rendered)
		assert.NotContains(t, s.rendered, "vk-b", "an id the view named must not reach the raw lookup: %s", s.rendered)
	}
	assert.Equal(t, 1, fallbacks, "the raw-table fallback must run exactly once, for vk-c")
	var viewLookups int
	for _, s := range capture.snapshot() {
		if strings.Contains(s.sql, "mv_filter_virtual_keys") {
			viewLookups++
		}
	}
	assert.Equal(t, 1, viewLookups, "names must be read from mv_filter_virtual_keys")

	// app: the id is the name, so there is no name lookup at all.
	capture = hfCaptureQueries(t, db)
	res, err = store.getDimensionRankingsFromMatView(ctx, filters, RankingDimensionApp)
	require.NoError(t, err)
	for _, r := range res.Rankings {
		assert.Equal(t, r.ID, r.Name)
	}
	require.Len(t, res.Rankings, 2)
	for _, s := range capture.snapshot() {
		assert.NotContains(t, s.sql, "CROSS JOIN LATERAL", "app rankings must not look names up: %s", s.sql)
	}
}

// TestHighFixesPGRankingNamesCoverWholeStartHour pins that the raw-table name
// fallback covers the same hour buckets the matview counts. The count includes
// the whole bucket holding StartTime, so an id whose only in-window rows fall
// in [date_trunc('hour', StartTime), StartTime) is ranked and must still get a
// name. vk-d was renamed within the filter view's window, so its name cannot be
// read from mv_filter_virtual_keys and comes from the fallback.
func TestHighFixesPGRankingNamesCoverWholeStartHour(t *testing.T) {
	store, db := setupPerfTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	hourStart := now.Truncate(time.Hour).Add(-3 * time.Hour)

	hfInsertNamedLog(t, db, "d-old", now.Add(-48*time.Hour), "vk-d", "Delta old", "codex")
	hfInsertNamedLog(t, db, "d-new", hourStart.Add(5*time.Minute), "vk-d", "Delta", "codex")

	refreshTestMatViews(t, db)
	store.matViewsReady.Store(true)

	start := hourStart.Add(30 * time.Minute)
	end := now
	res, err := store.getDimensionRankingsFromMatView(ctx, SearchFilters{StartTime: &start, EndTime: &end}, RankingDimensionVirtualKey)
	require.NoError(t, err)
	names := map[string]string{}
	for _, r := range res.Rankings {
		names[r.ID] = r.Name
	}
	require.Contains(t, names, "vk-d", "vk-d is counted through the bucket holding StartTime")
	assert.Equal(t, "Delta", names["vk-d"], "the name fallback must cover the whole StartTime hour the count covers")
}

// TestHighFixesPGRankingNamesFallbackUsesAllHistory pins that when the filter
// view cannot name an id (renamed within its window, so it holds two names), the
// raw-table fallback returns the latest name across all history, as dev did,
// rather than the name inside the requested window. This is the name a deleted
// entity keeps; the handler shows a live entity's current config name instead.
func TestHighFixesPGRankingNamesFallbackUsesAllHistory(t *testing.T) {
	store, db := setupPerfTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	hfInsertNamedLog(t, db, "e-old", now.Add(-10*24*time.Hour), "vk-e", "Echo old", "codex")
	hfInsertNamedLog(t, db, "e-new", now.Add(-1*time.Hour), "vk-e", "Echo", "codex")

	refreshTestMatViews(t, db)
	store.matViewsReady.Store(true)

	start := now.Add(-12 * 24 * time.Hour)
	end := now.Add(-5 * 24 * time.Hour)
	res, err := store.getDimensionRankingsFromMatView(ctx, SearchFilters{StartTime: &start, EndTime: &end}, RankingDimensionVirtualKey)
	require.NoError(t, err)
	names := map[string]string{}
	for _, r := range res.Rankings {
		names[r.ID] = r.Name
	}
	require.Contains(t, names, "vk-e")
	assert.Equal(t, "Echo", names["vk-e"], "the fallback must return the latest name across all history")
}

// hfInsertMetadataLog writes a logs row with raw metadata text at ts.
func hfInsertMetadataLog(t *testing.T, db *gorm.DB, id string, ts time.Time, metadata string) {
	t.Helper()
	require.NoError(t, db.Exec(`
		INSERT INTO logs (id, timestamp, object_type, provider, model, status, metadata, created_at)
		VALUES (?, ?, 'chat_completion', 'openai', 'gpt-4', 'success', ?, ?)
	`, id, ts, metadata, ts).Error)
}

// TestHighFixesPGMetadataCleanupRunsOnce pins H8 for the invalid-metadata
// cleanup: one run clears invalid rows across many hours, records completion
// in the migrations ledger, and later boots skip the scan entirely.
func TestHighFixesPGMetadataCleanupRunsOnce(t *testing.T) {
	db := trySetupPostgresDB(t)
	if db == nil {
		t.Skip("Postgres not available, skipping test")
	}
	setupLogsTableForGINIndexTest(t, db)
	ctx := context.Background()
	now := time.Now().UTC()

	hfInsertMetadataLog(t, db, "bad-old", now.Add(-50*time.Hour), `not json`)
	hfInsertMetadataLog(t, db, "bad-new", now, `[1,2]`)
	hfInsertMetadataLog(t, db, "good", now.Add(-3*time.Hour), `{"k":"v"}`)

	sqlDB, err := db.DB()
	require.NoError(t, err)
	conn, err := sqlDB.Conn(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	require.NoError(t, ensureMetadataGINIndex(ctx, conn))
	assert.Nil(t, getMetadataValue(t, db, "bad-old"))
	assert.Nil(t, getMetadataValue(t, db, "bad-new"))
	require.NotNil(t, getMetadataValue(t, db, "good"))

	var marked int64
	require.NoError(t, db.Raw("SELECT COUNT(*) FROM migrations WHERE id = ?", invalidMetadataCleanupJobID).Scan(&marked).Error)
	assert.Equal(t, int64(1), marked, "completion must be recorded in the migrations ledger")

	// A row that would have been cleaned is left alone: the second boot does no scan.
	hfInsertMetadataLog(t, db, "bad-after", now, `nope`)
	require.NoError(t, ensureMetadataGINIndex(ctx, conn))
	got := getMetadataValue(t, db, "bad-after")
	require.NotNil(t, got, "a recorded cleanup must not scan logs again")
}

// TestHighFixesPGCachedReadTokensBackfillRunsOnce pins H8 for the
// cached_read_tokens backfill: keyset hour windows cover old and new rows in
// one run, completion is recorded, and a second run does not scan.
func TestHighFixesPGCachedReadTokensBackfillRunsOnce(t *testing.T) {
	_, db := setupPerfTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	insert := func(id string, ts time.Time) {
		require.NoError(t, db.Exec(`
			INSERT INTO logs (id, timestamp, object_type, provider, model, status, created_at, token_usage, cached_read_tokens)
			VALUES (?, ?, 'chat_completion', 'openai', 'gpt-4', 'success', ?, '{"prompt_tokens_details":{"cached_read_tokens":7}}', 0)
		`, id, ts, ts).Error)
	}
	insert("c-old", now.Add(-72*time.Hour))
	insert("c-new", now)

	conn := acquirePerfTestSQLConn(t, ctx, db)
	require.NoError(t, backfillCachedReadTokens(ctx, conn))
	read := func(id string) int {
		var v int
		require.NoError(t, db.Raw("SELECT cached_read_tokens FROM logs WHERE id = ?", id).Scan(&v).Error)
		return v
	}
	assert.Equal(t, 7, read("c-old"))
	assert.Equal(t, 7, read("c-new"))

	insert("c-after", now)
	require.NoError(t, backfillCachedReadTokens(ctx, conn))
	assert.Equal(t, 0, read("c-after"), "a recorded backfill must not scan logs again")
}

// TestHighFixesPGLongJobsLiftPoolStatementTimeout pins that the long-running
// jobs on dedicated connections (matview creation and refresh, the background
// index builds and backfills) are not cut by the runtime pool's
// statement_timeout, while the pool itself keeps the timeout: every connection
// handed back afterwards still reports it.
func TestHighFixesPGLongJobsLiftPoolStatementTimeout(t *testing.T) {
	_, db := setupPerfTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	require.NoError(t, db.Exec(`
		INSERT INTO logs (id, timestamp, object_type, provider, model, status, created_at, virtual_key_id, virtual_key_name, cost, total_tokens)
		SELECT 'hft-' || g, ?::timestamp - ((g % 86400) || ' seconds')::interval, 'chat_completion', 'openai', 'gpt-4o-' || (g % 7), 'success',
		       ?::timestamp, 'vk-' || (g % 97), 'VK ' || (g % 97), 0.01, 15
		FROM generate_series(1, 300000) AS g
	`, now, now).Error)
	require.NoError(t, db.Exec("ANALYZE logs").Error)

	// pgx passes unknown DSN keys as run-time parameters, so every connection of
	// this pool starts with a 40ms statement_timeout, like a production runtime pool.
	tight, err := gorm.Open(postgres.Open(postgresDSN+" statement_timeout=40"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	tightSQL, err := tight.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = tightSQL.Close() })

	require.Error(t, tight.Exec("SELECT pg_sleep(0.2)").Error, "the pool timeout must be live")
	require.Error(t, tight.Exec("REFRESH MATERIALIZED VIEW mv_logs_hourly").Error,
		"a refresh over 300k rows must exceed the pool timeout, or this test proves nothing")

	resetTestMatViewRefreshGate()
	require.NoError(t, refreshMatViews(ctx, tight), "refreshMatViews must lift the pool timeout on its connection")

	require.NoError(t, db.Exec("DROP MATERIALIZED VIEW IF EXISTS mv_logs_hourly CASCADE").Error)
	require.NoError(t, ensureMatViews(ctx, tight), "ensureMatViews must lift the pool timeout on its connection")

	require.NoError(t, db.Exec("DROP INDEX IF EXISTS idx_logs_vk_ts").Error)
	buildBackgroundIndexes(ctx, tight, testLogger{})
	assert.True(t, hfPGIndexValid(t, db, "logs", "idx_logs_vk_ts"), "the background builder must finish its index builds")

	// Connections returned to the pool keep the pool's timeout.
	conns := make([]interface{ Close() error }, 0, 3)
	for range 3 {
		c, err := tightSQL.Conn(ctx)
		require.NoError(t, err)
		conns = append(conns, c)
		var setting string
		require.NoError(t, c.QueryRowContext(ctx, "SHOW statement_timeout").Scan(&setting))
		assert.Equal(t, "40ms", setting, "a pooled connection must keep the runtime statement_timeout")
	}
	for _, c := range conns {
		_ = c.Close()
	}
}

// TestHighFixesPGOwnerTimestampIndexIgnoresOtherSchemas pins that the builder
// judges an index by the current schema only: a same-named index on a same-named
// table in another schema must not make it skip the build, and the replaced
// single-column index must still be dropped only after the current schema's
// composite is valid.
func TestHighFixesPGOwnerTimestampIndexIgnoresOtherSchemas(t *testing.T) {
	_, db := setupPerfTestDB(t)
	ctx := context.Background()

	const otherSchema = "hf_other_schema"
	require.NoError(t, db.Exec("DROP SCHEMA IF EXISTS "+otherSchema+" CASCADE").Error)
	require.NoError(t, db.Exec("CREATE SCHEMA "+otherSchema).Error)
	t.Cleanup(func() { db.Exec("DROP SCHEMA IF EXISTS " + otherSchema + " CASCADE") })
	require.NoError(t, db.Exec("CREATE TABLE "+otherSchema+".logs (virtual_key_id varchar(255), timestamp timestamp)").Error)
	require.NoError(t, db.Exec("CREATE INDEX idx_logs_vk_ts ON "+otherSchema+".logs(virtual_key_id, timestamp)").Error)

	require.NoError(t, db.Exec("DROP INDEX IF EXISTS idx_logs_vk_ts").Error)
	require.NoError(t, db.Exec("CREATE INDEX IF NOT EXISTS idx_logs_virtual_key_id ON logs(virtual_key_id)").Error)

	conn := acquirePerfTestSQLConn(t, ctx, db)
	require.NoError(t, ensureOwnerTimestampIndexes(ctx, conn, testLogger{}))

	assert.True(t, hfPGIndexValid(t, db, "logs", "idx_logs_vk_ts"), "the composite must be built in the current schema")
	assert.False(t, hfPGIndexExists(t, db, "idx_logs_virtual_key_id"), "the single-column index is dropped once the composite is valid")
	var other int64
	require.NoError(t, db.Raw(`SELECT COUNT(*) FROM pg_indexes WHERE schemaname = ? AND indexname = 'idx_logs_vk_ts'`, otherSchema).Scan(&other).Error)
	assert.EqualValues(t, 1, other, "the other schema's index must be left alone")
}
