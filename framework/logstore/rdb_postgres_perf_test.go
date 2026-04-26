package logstore

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// setupPerfTestDB connects to Postgres, runs migrations, and returns the store.
func setupPerfTestDB(t *testing.T) (*RDBLogStore, *gorm.DB) {
	t.Helper()
	db := trySetupPostgresDB(t)
	if db == nil {
		t.Skip("Postgres not available, skipping test")
	}

	// Clean slate — drop test-owned tables but preserve the shared migrations
	// table so concurrent test packages (e.g. configstore) are not disrupted.
	db.Exec("DROP MATERIALIZED VIEW IF EXISTS mv_logs_hourly CASCADE")
	db.Exec("DROP MATERIALIZED VIEW IF EXISTS mv_logs_filterdata CASCADE")
	db.Exec("DROP TABLE IF EXISTS mcp_tool_logs CASCADE")
	db.Exec("DROP TABLE IF EXISTS async_jobs CASCADE")
	db.Exec("DROP TABLE IF EXISTS logs CASCADE")
	db.Exec("CREATE TABLE IF NOT EXISTS migrations (id VARCHAR(255) PRIMARY KEY)")
	db.Exec("DELETE FROM migrations")

	ctx := context.Background()
	err := triggerMigrations(ctx, db)
	require.NoError(t, err, "migrations should succeed")

	err = ensureMatViews(ctx, db)
	require.NoError(t, err, "matview creation should succeed")

	store := &RDBLogStore{db: db}

	t.Cleanup(func() {
		for _, idx := range performanceIndexes {
			db.Exec("DROP INDEX IF EXISTS " + idx.name)
		}
		db.Exec("DROP MATERIALIZED VIEW IF EXISTS mv_logs_hourly CASCADE")
		db.Exec("DROP MATERIALIZED VIEW IF EXISTS mv_logs_filterdata CASCADE")
		db.Exec("DROP TABLE IF EXISTS mcp_tool_logs CASCADE")
		db.Exec("DROP TABLE IF EXISTS async_jobs CASCADE")
		db.Exec("DROP TABLE IF EXISTS logs CASCADE")
		db.Exec("DELETE FROM migrations")
	})

	return store, db
}

// acquirePerfTestSQLConn returns a dedicated connection for ensurePerformanceIndexes (CONCURRENTLY + session SET).
func acquirePerfTestSQLConn(t *testing.T, ctx context.Context, db *gorm.DB) *sql.Conn {
	t.Helper()
	sqlDB, err := db.DB()
	require.NoError(t, err)
	conn, err := sqlDB.Conn(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

type logOpts struct {
	Model              string
	Provider           string
	Status             string
	Timestamp          time.Time
	RoutingEnginesUsed string
	Metadata           string
	ContentSummary     string
	VirtualKeyID       string
	VirtualKeyName     string
	SelectedKeyID      string
	SelectedKeyName    string
	RoutingRuleID      string
	RoutingRuleName    string
	StopReason         string
}

func insertPerfLog(t *testing.T, db *gorm.DB, opts logOpts) {
	t.Helper()
	if opts.Provider == "" {
		opts.Provider = "openai"
	}
	if opts.Status == "" {
		opts.Status = "success"
	}
	if opts.Model == "" {
		opts.Model = "gpt-4"
	}
	id := uuid.New().String()
	err := db.Exec(`
		INSERT INTO logs (id, timestamp, object_type, provider, model, status,
			routing_engines_used, metadata, content_summary,
			virtual_key_id, virtual_key_name, selected_key_id, selected_key_name,
			routing_rule_id, routing_rule_name, stop_reason, created_at, latency, cost,
			prompt_tokens, completion_tokens, total_tokens)
		VALUES (?, ?, 'chat_completion', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 100, 0.01, 10, 5, 15)
	`, id, opts.Timestamp, opts.Provider, opts.Model, opts.Status,
		opts.RoutingEnginesUsed, opts.Metadata, opts.ContentSummary,
		opts.VirtualKeyID, opts.VirtualKeyName, opts.SelectedKeyID, opts.SelectedKeyName,
		opts.RoutingRuleID, opts.RoutingRuleName, opts.StopReason, opts.Timestamp).Error
	require.NoError(t, err, "Failed to insert test log")
}

type mcpLogOpts struct {
	ToolName       string
	ServerLabel    string
	Timestamp      time.Time
	VirtualKeyID   string
	VirtualKeyName string
	Arguments      string
	Result         string
}

func insertPerfMCPLog(t *testing.T, db *gorm.DB, opts mcpLogOpts) {
	t.Helper()
	id := uuid.New().String()
	err := db.Exec(`
		INSERT INTO mcp_tool_logs (id, llm_request_id, tool_name, server_label,
			timestamp, status, latency, cost,
			virtual_key_id, virtual_key_name, arguments, result, created_at)
		VALUES (?, ?, ?, ?, ?, 'success', 50, 0.001, ?, ?, ?, ?, ?)
	`, id, uuid.New().String(), opts.ToolName, opts.ServerLabel,
		opts.Timestamp, opts.VirtualKeyID, opts.VirtualKeyName,
		opts.Arguments, opts.Result, opts.Timestamp).Error
	require.NoError(t, err, "Failed to insert MCP test log")
}

// refreshTestMatViews refreshes materialized views after inserting test data.
// This is needed because matviews are populated at creation time and don't
// automatically reflect new inserts until explicitly refreshed.
func refreshTestMatViews(t *testing.T, db *gorm.DB) {
	t.Helper()
	ctx := context.Background()
	err := refreshMatViews(ctx, db)
	require.NoError(t, err, "Failed to refresh materialized views")
}

// ---------- Phase 1: Defensive Limits ----------

func TestSearchLogs_LimitClamping(t *testing.T) {
	store, db := setupPerfTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()

	for i := 0; i < 5; i++ {
		insertPerfLog(t, db, logOpts{Timestamp: now})
	}
	refreshTestMatViews(t, db)

	// Limit=0 should be clamped (not return 0 results)
	result, err := store.SearchLogs(ctx, SearchFilters{}, PaginationOptions{Limit: 0})
	require.NoError(t, err)
	assert.Equal(t, 5, len(result.Logs), "Limit=0 should be clamped")

	// Limit=2 should return 2
	result, err = store.SearchLogs(ctx, SearchFilters{}, PaginationOptions{Limit: 2})
	require.NoError(t, err)
	assert.Equal(t, 2, len(result.Logs))

	// Limit=-1 should be clamped
	result, err = store.SearchLogs(ctx, SearchFilters{}, PaginationOptions{Limit: -1})
	require.NoError(t, err)
	assert.Equal(t, 5, len(result.Logs), "Limit=-1 should be clamped")

	// Limit=2000 should be clamped to 1000
	result, err = store.SearchLogs(ctx, SearchFilters{}, PaginationOptions{Limit: 2000})
	require.NoError(t, err)
	assert.Equal(t, 5, len(result.Logs))
}

func TestSearchMCPToolLogs_LimitClamping(t *testing.T) {
	store, db := setupPerfTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()

	for i := 0; i < 5; i++ {
		insertPerfMCPLog(t, db, mcpLogOpts{
			ToolName: "search", ServerLabel: "s1", Timestamp: now,
			VirtualKeyID: "vk-1", VirtualKeyName: "key-1",
		})
	}

	result, err := store.SearchMCPToolLogs(ctx, MCPToolLogSearchFilters{}, PaginationOptions{Limit: 0})
	require.NoError(t, err)
	assert.Equal(t, 5, len(result.Logs), "Limit=0 should be clamped")

	result, err = store.SearchMCPToolLogs(ctx, MCPToolLogSearchFilters{}, PaginationOptions{Limit: 3})
	require.NoError(t, err)
	assert.Equal(t, 3, len(result.Logs))
}

func TestGetModelRankings_HasLimit(t *testing.T) {
	store, db := setupPerfTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	start := now.Add(-1 * time.Hour)

	for i := 0; i < 5; i++ {
		insertPerfLog(t, db, logOpts{
			Model: fmt.Sprintf("model-%d", i), Timestamp: now,
		})
	}
	refreshTestMatViews(t, db)

	result, err := store.GetModelRankings(ctx, SearchFilters{StartTime: &start, EndTime: &now})
	require.NoError(t, err)
	assert.LessOrEqual(t, len(result.Rankings), defaultMaxRankingsLimit)
	assert.Equal(t, 5, len(result.Rankings))
}

func TestDeleteExpiredAsyncJobs_BatchDeletes(t *testing.T) {
	store, db := setupPerfTestDB(t)
	ctx := context.Background()
	past := time.Now().UTC().Add(-1 * time.Hour)

	for i := 0; i < 5; i++ {
		err := db.Exec(`
			INSERT INTO async_jobs (id, status, request_type, virtual_key_id, expires_at, created_at)
			VALUES (?, 'completed', 'chat_completion', 'vk-1', ?, ?)
		`, uuid.New().String(), past, past).Error
		require.NoError(t, err)
	}

	deleted, err := store.DeleteExpiredAsyncJobs(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(5), deleted)

	var count int64
	db.Model(&AsyncJob{}).Count(&count)
	assert.Equal(t, int64(0), count)
}

// ---------- Phase 2: Time-scoped filter data ----------

func TestGetDistinctModels_TimeCutoff(t *testing.T) {
	store, db := setupPerfTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	recent := now.Add(-7 * 24 * time.Hour)
	old := now.Add(-60 * 24 * time.Hour)

	insertPerfLog(t, db, logOpts{Model: "recent-model", Timestamp: recent})
	insertPerfLog(t, db, logOpts{Model: "old-model", Timestamp: old})
	refreshTestMatViews(t, db)

	models, err := store.GetDistinctModels(ctx)
	require.NoError(t, err)
	assert.Contains(t, models, "recent-model")
	assert.NotContains(t, models, "old-model")
}

func TestGetDistinctKeyPairs_TimeCutoff(t *testing.T) {
	store, db := setupPerfTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	recent := now.Add(-7 * 24 * time.Hour)
	old := now.Add(-60 * 24 * time.Hour)

	insertPerfLog(t, db, logOpts{
		Timestamp: recent, VirtualKeyID: "vk-recent", VirtualKeyName: "Recent Key",
	})
	insertPerfLog(t, db, logOpts{
		Timestamp: old, VirtualKeyID: "vk-old", VirtualKeyName: "Old Key",
	})
	refreshTestMatViews(t, db)

	pairs, err := store.GetDistinctKeyPairs(ctx, "virtual_key_id", "virtual_key_name")
	require.NoError(t, err)

	var ids []string
	for _, p := range pairs {
		ids = append(ids, p.ID)
	}
	assert.Contains(t, ids, "vk-recent")
	assert.NotContains(t, ids, "vk-old")
}

func TestGetDistinctRoutingEngines_TimeCutoff(t *testing.T) {
	store, db := setupPerfTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	recent := now.Add(-7 * 24 * time.Hour)
	old := now.Add(-60 * 24 * time.Hour)

	insertPerfLog(t, db, logOpts{
		Timestamp: recent, RoutingEnginesUsed: "loadbalancing,governance",
	})
	insertPerfLog(t, db, logOpts{
		Timestamp: old, RoutingEnginesUsed: "routing-rule",
	})
	refreshTestMatViews(t, db)

	engines, err := store.GetDistinctRoutingEngines(ctx)
	require.NoError(t, err)
	assert.Contains(t, engines, "loadbalancing")
	assert.Contains(t, engines, "governance")
	assert.NotContains(t, engines, "routing-rule")
}

func TestGetDistinctMetadataKeys_TimeCutoff(t *testing.T) {
	store, db := setupPerfTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	recent := now.Add(-7 * 24 * time.Hour)
	old := now.Add(-60 * 24 * time.Hour)

	insertPerfLog(t, db, logOpts{
		Timestamp: recent, Metadata: `{"env": "production"}`,
	})
	insertPerfLog(t, db, logOpts{
		Timestamp: old, Metadata: `{"old_key": "old_value"}`,
	})

	keys, err := store.GetDistinctMetadataKeys(ctx)
	require.NoError(t, err)
	assert.Contains(t, keys, "env")
	assert.NotContains(t, keys, "old_key")
}

func TestGetDistinctStopReasons_TimeCutoff(t *testing.T) {
	store, db := setupPerfTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	recent := now.Add(-7 * 24 * time.Hour)
	old := now.Add(-60 * 24 * time.Hour)

	insertPerfLog(t, db, logOpts{Timestamp: recent, StopReason: "refusal"})
	insertPerfLog(t, db, logOpts{Timestamp: recent, StopReason: "content_filter"})
	insertPerfLog(t, db, logOpts{Timestamp: old, StopReason: "length"})

	stopReasons, err := store.GetDistinctStopReasons(ctx)
	require.NoError(t, err)
	assert.Contains(t, stopReasons, "refusal")
	assert.Contains(t, stopReasons, "content_filter")
	assert.NotContains(t, stopReasons, "length")
}

func TestGetAvailableToolNames_TimeCutoff(t *testing.T) {
	store, db := setupPerfTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	recent := now.Add(-7 * 24 * time.Hour)
	old := now.Add(-60 * 24 * time.Hour)

	insertPerfMCPLog(t, db, mcpLogOpts{
		ToolName: "recent-tool", ServerLabel: "s1", Timestamp: recent,
		VirtualKeyID: "vk-1", VirtualKeyName: "k1",
	})
	insertPerfMCPLog(t, db, mcpLogOpts{
		ToolName: "old-tool", ServerLabel: "s1", Timestamp: old,
		VirtualKeyID: "vk-1", VirtualKeyName: "k1",
	})

	tools, err := store.GetAvailableToolNames(ctx)
	require.NoError(t, err)
	assert.Contains(t, tools, "recent-tool")
	assert.NotContains(t, tools, "old-tool")
}

func TestGetAvailableServerLabels_TimeCutoff(t *testing.T) {
	store, db := setupPerfTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	recent := now.Add(-7 * 24 * time.Hour)
	old := now.Add(-60 * 24 * time.Hour)

	insertPerfMCPLog(t, db, mcpLogOpts{
		ToolName: "t1", ServerLabel: "recent-server", Timestamp: recent,
		VirtualKeyID: "vk-1", VirtualKeyName: "k1",
	})
	insertPerfMCPLog(t, db, mcpLogOpts{
		ToolName: "t2", ServerLabel: "old-server", Timestamp: old,
		VirtualKeyID: "vk-1", VirtualKeyName: "k1",
	})

	labels, err := store.GetAvailableServerLabels(ctx)
	require.NoError(t, err)
	assert.Contains(t, labels, "recent-server")
	assert.NotContains(t, labels, "old-server")
}

func TestGetAvailableMCPVirtualKeys_TimeCutoff(t *testing.T) {
	store, db := setupPerfTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	recent := now.Add(-7 * 24 * time.Hour)
	old := now.Add(-60 * 24 * time.Hour)

	insertPerfMCPLog(t, db, mcpLogOpts{
		ToolName: "t1", ServerLabel: "s1", Timestamp: recent,
		VirtualKeyID: "vk-recent", VirtualKeyName: "Recent VK",
	})
	insertPerfMCPLog(t, db, mcpLogOpts{
		ToolName: "t2", ServerLabel: "s1", Timestamp: old,
		VirtualKeyID: "vk-old", VirtualKeyName: "Old VK",
	})

	keys, err := store.GetAvailableMCPVirtualKeys(ctx)
	require.NoError(t, err)

	var ids []string
	for _, k := range keys {
		if k.VirtualKeyID != nil {
			ids = append(ids, *k.VirtualKeyID)
		}
	}
	assert.Contains(t, ids, "vk-recent")
	assert.NotContains(t, ids, "vk-old")
}

// ---------- Phase 3: Routing engine filter + indexes ----------

func TestRoutingEngineFilter_Postgres(t *testing.T) {
	store, db := setupPerfTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	start := now.Add(-1 * time.Hour)

	insertPerfLog(t, db, logOpts{
		Model: "m1", Timestamp: now, RoutingEnginesUsed: "loadbalancing,governance",
	})
	insertPerfLog(t, db, logOpts{
		Model: "m2", Timestamp: now, RoutingEnginesUsed: "routing-rule",
	})
	insertPerfLog(t, db, logOpts{
		Model: "m3", Timestamp: now, RoutingEnginesUsed: "loadbalancing",
	})

	// Single engine filter
	result, err := store.SearchLogs(ctx, SearchFilters{
		RoutingEngineUsed: []string{"loadbalancing"},
		StartTime:         &start, EndTime: &now,
	}, PaginationOptions{Limit: 100})
	require.NoError(t, err)
	assert.Equal(t, 2, len(result.Logs), "Should find 2 logs with loadbalancing")

	result, err = store.SearchLogs(ctx, SearchFilters{
		RoutingEngineUsed: []string{"governance"},
		StartTime:         &start, EndTime: &now,
	}, PaginationOptions{Limit: 100})
	require.NoError(t, err)
	assert.Equal(t, 1, len(result.Logs), "Should find 1 log with governance")

	result, err = store.SearchLogs(ctx, SearchFilters{
		RoutingEngineUsed: []string{"routing-rule"},
		StartTime:         &start, EndTime: &now,
	}, PaginationOptions{Limit: 100})
	require.NoError(t, err)
	assert.Equal(t, 1, len(result.Logs), "Should find 1 log with routing-rule")

	// Multiple engines (OR)
	result, err = store.SearchLogs(ctx, SearchFilters{
		RoutingEngineUsed: []string{"loadbalancing", "routing-rule"},
		StartTime:         &start, EndTime: &now,
	}, PaginationOptions{Limit: 100})
	require.NoError(t, err)
	assert.Equal(t, 3, len(result.Logs), "Should find all 3 with loadbalancing OR routing-rule")

	// Non-existent engine
	result, err = store.SearchLogs(ctx, SearchFilters{
		RoutingEngineUsed: []string{"nonexistent"},
		StartTime:         &start, EndTime: &now,
	}, PaginationOptions{Limit: 100})
	require.NoError(t, err)
	assert.Equal(t, 0, len(result.Logs))
}

func TestEnsurePerformanceIndexes(t *testing.T) {
	db := trySetupPostgresDB(t)
	if db == nil {
		t.Skip("Postgres not available, skipping test")
	}

	db.Exec("DROP TABLE IF EXISTS mcp_tool_logs CASCADE")
	db.Exec("DROP TABLE IF EXISTS async_jobs CASCADE")
	db.Exec("DROP TABLE IF EXISTS logs CASCADE")
	db.Exec("CREATE TABLE IF NOT EXISTS migrations (id VARCHAR(255) PRIMARY KEY)")
	db.Exec("DELETE FROM migrations")

	ctx := context.Background()
	err := triggerMigrations(ctx, db)
	require.NoError(t, err)

	t.Cleanup(func() {
		for _, idx := range performanceIndexes {
			db.Exec("DROP INDEX IF EXISTS " + idx.name)
		}
		db.Exec("DROP TABLE IF EXISTS mcp_tool_logs CASCADE")
		db.Exec("DROP TABLE IF EXISTS async_jobs CASCADE")
		db.Exec("DROP TABLE IF EXISTS logs CASCADE")
		db.Exec("DELETE FROM migrations")
	})

	conn := acquirePerfTestSQLConn(t, ctx, db)
	// First run
	err = ensurePerformanceIndexes(ctx, conn)
	require.NoError(t, err, "ensurePerformanceIndexes should succeed")

	// Verify all indexes exist and are valid
	for _, idx := range performanceIndexes {
		var indexValid bool
		err := db.Raw(`
			SELECT COALESCE(bool_and(pi.indisvalid), false)
			FROM pg_class pc
			JOIN pg_index pi ON pi.indrelid = pc.oid
			JOIN pg_class ic ON ic.oid = pi.indexrelid
			WHERE pc.relname = ?
			  AND ic.relname = ?
		`, idx.table, idx.name).Scan(&indexValid).Error
		require.NoError(t, err)
		assert.True(t, indexValid, "Index %s should be valid", idx.name)
	}

	// Idempotent — second run should be a no-op
	err = ensurePerformanceIndexes(ctx, conn)
	require.NoError(t, err, "ensurePerformanceIndexes should be idempotent")
}

func TestContentSearch_Postgres(t *testing.T) {
	store, db := setupPerfTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	start := now.Add(-1 * time.Hour)

	// Build indexes
	conn := acquirePerfTestSQLConn(t, ctx, db)

	err := ensurePerformanceIndexes(ctx, conn)
	require.NoError(t, err)

	insertPerfLog(t, db, logOpts{
		Timestamp:      now,
		ContentSummary: "The quick brown fox jumps over the lazy dog",
	})
	insertPerfLog(t, db, logOpts{
		Timestamp:      now,
		ContentSummary: "Hello world this is a test message",
	})

	result, err := store.SearchLogs(ctx, SearchFilters{
		ContentSearch: "brown fox",
		StartTime:     &start, EndTime: &now,
	}, PaginationOptions{Limit: 100})
	require.NoError(t, err)
	assert.Equal(t, 1, len(result.Logs), "Should find 1 log matching 'brown fox'")

	result, err = store.SearchLogs(ctx, SearchFilters{
		ContentSearch: "test message",
		StartTime:     &start, EndTime: &now,
	}, PaginationOptions{Limit: 100})
	require.NoError(t, err)
	assert.Equal(t, 1, len(result.Logs), "Should find 1 log matching 'test message'")

	result, err = store.SearchLogs(ctx, SearchFilters{
		ContentSearch: "nonexistent phrase",
		StartTime:     &start, EndTime: &now,
	}, PaginationOptions{Limit: 100})
	require.NoError(t, err)
	assert.Equal(t, 0, len(result.Logs))
}

func TestMCPContentSearch_Postgres(t *testing.T) {
	store, db := setupPerfTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()

	// Build indexes
	conn := acquirePerfTestSQLConn(t, ctx, db)
	err := ensurePerformanceIndexes(ctx, conn)
	require.NoError(t, err)

	insertPerfMCPLog(t, db, mcpLogOpts{
		ToolName: "search", ServerLabel: "s1", Timestamp: now,
		VirtualKeyID: "vk-1", VirtualKeyName: "k1",
		Arguments: `{"query": "weather in london"}`,
		Result:    `{"temperature": 15}`,
	})
	insertPerfMCPLog(t, db, mcpLogOpts{
		ToolName: "calc", ServerLabel: "s1", Timestamp: now,
		VirtualKeyID: "vk-1", VirtualKeyName: "k1",
		Arguments: `{"expression": "2+2"}`,
		Result:    `{"answer": 4}`,
	})

	result, err := store.SearchMCPToolLogs(ctx, MCPToolLogSearchFilters{
		ContentSearch: "london",
	}, PaginationOptions{Limit: 100})
	require.NoError(t, err)
	assert.Equal(t, 1, len(result.Logs), "Should find 1 MCP log matching 'london'")

	result, err = store.SearchMCPToolLogs(ctx, MCPToolLogSearchFilters{
		ContentSearch: "temperature",
	}, PaginationOptions{Limit: 100})
	require.NoError(t, err)
	assert.Equal(t, 1, len(result.Logs), "Should find 1 MCP log matching 'temperature' in result")
}
