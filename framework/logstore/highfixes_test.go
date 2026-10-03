package logstore

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// recordingRetentionManager is a retention stub for both tables that records
// the batch size each call asked for and returns one scripted count per call.
type recordingRetentionManager struct {
	logCounts  []int64
	mcpCounts  []int64
	logBatches []int
	mcpBatches []int
}

// DeleteLogsBatch records the batch size and returns the next scripted count.
func (m *recordingRetentionManager) DeleteLogsBatch(_ context.Context, _ time.Time, size int) (int64, error) {
	m.logBatches = append(m.logBatches, size)
	if len(m.logBatches) > len(m.logCounts) {
		return 0, nil
	}
	return m.logCounts[len(m.logBatches)-1], nil
}

// DeleteMCPToolLogsBatch records the batch size and returns the next scripted count.
func (m *recordingRetentionManager) DeleteMCPToolLogsBatch(_ context.Context, _ time.Time, size int) (int64, error) {
	m.mcpBatches = append(m.mcpBatches, size)
	if len(m.mcpBatches) > len(m.mcpCounts) {
		return 0, nil
	}
	return m.mcpCounts[len(m.mcpBatches)-1], nil
}

// TestHighFixesCleanerUsesLargeBatchesAndCleansMCP pins H2: the cleaner deletes
// in batches of 5000 (100 could not keep up with 5.6M expired rows per day) and
// runs the same retention over mcp_tool_logs, which previously had none.
func TestHighFixesCleanerUsesLargeBatchesAndCleansMCP(t *testing.T) {
	m := &recordingRetentionManager{
		logCounts: []int64{5000, 5000, 12},
		mcpCounts: []int64{5000, 3},
	}
	NewLogsCleaner(m, CleanerConfig{RetentionDays: 7}, testLogger{}).cleanupOldLogs(context.Background())

	require.Equal(t, []int{5000, 5000, 5000}, m.logBatches, "logs must be deleted 5000 rows at a time until a short batch")
	require.Equal(t, []int{5000, 5000}, m.mcpBatches, "mcp_tool_logs must get the same retention pass")
}

// backlogRetentionManager simulates a logs table that never finishes draining:
// every logs batch is full (or fails, when logErr is set). It counts the MCP
// batches the cleaner still gets to run.
type backlogRetentionManager struct {
	logErr     error
	mcpBatches atomic.Int32
}

// DeleteLogsBatch reports a full batch after a short pause, or logErr.
func (m *backlogRetentionManager) DeleteLogsBatch(ctx context.Context, _ time.Time, size int) (int64, error) {
	if m.logErr != nil {
		return 0, m.logErr
	}
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	case <-time.After(2 * time.Millisecond):
	}
	return int64(size), nil
}

// DeleteMCPToolLogsBatch counts the call and reports a short batch.
func (m *backlogRetentionManager) DeleteMCPToolLogsBatch(_ context.Context, _ time.Time, _ int) (int64, error) {
	m.mcpBatches.Add(1)
	return 3, nil
}

// TestHighFixesCleanerMCPNotStarvedByLogsBacklog pins that mcp_tool_logs get a
// share of every retention pass: a logs backlog that outlasts the pass deadline,
// or a logs delete that keeps failing, must not skip MCP retention forever. A
// pass that was stopped (parent ctx cancelled) still ends without touching MCP.
func TestHighFixesCleanerMCPNotStarvedByLogsBacklog(t *testing.T) {
	t.Run("logs backlog outlasts the deadline", func(t *testing.T) {
		m := &backlogRetentionManager{}
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()
		NewLogsCleaner(m, CleanerConfig{RetentionDays: 7}, testLogger{}).cleanupOldLogs(ctx)
		require.Positive(t, m.mcpBatches.Load(), "a logs backlog must not starve MCP tool-log retention")
	})
	t.Run("logs delete fails", func(t *testing.T) {
		m := &backlogRetentionManager{logErr: fmt.Errorf("lock timeout")}
		NewLogsCleaner(m, CleanerConfig{RetentionDays: 7}, testLogger{}).cleanupOldLogs(context.Background())
		require.Positive(t, m.mcpBatches.Load(), "a failing logs delete must not skip MCP tool-log retention")
	})
	t.Run("stopped pass", func(t *testing.T) {
		m := &backlogRetentionManager{}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		NewLogsCleaner(m, CleanerConfig{RetentionDays: 7}, testLogger{}).cleanupOldLogs(ctx)
		require.Zero(t, m.mcpBatches.Load(), "a stopped pass must not start MCP retention")
	})
}

// hfInsertLogAt inserts a minimal logs row with explicit timestamp and created_at.
func hfInsertLogAt(t *testing.T, store *RDBLogStore, id string, ts time.Time) {
	t.Helper()
	require.NoError(t, store.Create(context.Background(), &Log{
		ID: id, Timestamp: ts, CreatedAt: ts, Object: "chat_completion",
		Provider: "openai", Model: "gpt-4o", Status: "success",
	}))
}

// hfInsertMCPAt inserts a minimal mcp_tool_logs row with explicit timestamps.
func hfInsertMCPAt(t *testing.T, store *RDBLogStore, id string, ts time.Time) {
	t.Helper()
	require.NoError(t, store.CreateMCPToolLog(context.Background(), &MCPToolLog{
		ID: id, RequestID: id, Timestamp: ts, CreatedAt: ts, ToolName: "search", Status: "success",
	}))
}

// hfLogIDs returns every id in logs, sorted.
func hfLogIDs(t *testing.T, db *gorm.DB, table string) []string {
	t.Helper()
	var ids []string
	require.NoError(t, db.Table(table).Order("id").Pluck("id", &ids).Error)
	return ids
}

// TestHighFixesDeleteLogsBatchOldestFirst pins that a retention batch takes the
// oldest expired rows first (ORDER BY on the indexed cutoff column), so each
// batch walks the index from its low end instead of an arbitrary heap order.
func TestHighFixesDeleteLogsBatchOldestFirst(t *testing.T) {
	store := newTestSQLiteStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	// Inserted newest-expired first so insertion order disagrees with age.
	hfInsertLogAt(t, store, "old-10d", now.AddDate(0, 0, -10))
	hfInsertLogAt(t, store, "old-20d", now.AddDate(0, 0, -20))
	hfInsertLogAt(t, store, "old-30d", now.AddDate(0, 0, -30))
	hfInsertLogAt(t, store, "fresh", now)

	deleted, err := store.DeleteLogsBatch(ctx, now.AddDate(0, 0, -5), 2)
	require.NoError(t, err)
	require.Equal(t, int64(2), deleted)
	require.Equal(t, []string{"fresh", "old-10d"}, hfLogIDs(t, store.db, "logs"), "the two oldest expired rows go first")

	hfInsertMCPAt(t, store, "m-old-10d", now.AddDate(0, 0, -10))
	hfInsertMCPAt(t, store, "m-old-20d", now.AddDate(0, 0, -20))
	hfInsertMCPAt(t, store, "m-old-30d", now.AddDate(0, 0, -30))
	hfInsertMCPAt(t, store, "m-fresh", now)
	deleted, err = store.DeleteMCPToolLogsBatch(ctx, now.AddDate(0, 0, -5), 2)
	require.NoError(t, err)
	require.Equal(t, int64(2), deleted)
	require.Equal(t, []string{"m-fresh", "m-old-10d"}, hfLogIDs(t, store.db, "mcp_tool_logs"))
}

// TestHighFixesCleanerRetentionEndToEnd runs the real cleaner over a SQLite
// store: rows older than the retention cutoff go from both tables, newer rows
// stay, and the cutoff column for logs is still created_at.
func TestHighFixesCleanerRetentionEndToEnd(t *testing.T) {
	store := newTestSQLiteStore(t)
	now := time.Now().UTC()

	for i := range 7 {
		hfInsertLogAt(t, store, fmt.Sprintf("log-old-%d", i), now.AddDate(0, 0, -30-i))
		hfInsertMCPAt(t, store, fmt.Sprintf("mcp-old-%d", i), now.AddDate(0, 0, -30-i))
	}
	hfInsertLogAt(t, store, "log-new", now.AddDate(0, 0, -1))
	hfInsertMCPAt(t, store, "mcp-new", now.AddDate(0, 0, -1))
	// timestamp inside retention but created_at outside it: logs retention is
	// keyed on created_at, exactly as before.
	require.NoError(t, store.Create(context.Background(), &Log{
		ID: "log-created-old", Timestamp: now, CreatedAt: now.AddDate(0, 0, -30), Object: "chat_completion",
		Provider: "openai", Model: "gpt-4o", Status: "success",
	}))

	NewLogsCleaner(store, CleanerConfig{RetentionDays: 7}, testLogger{}).cleanupOldLogs(context.Background())

	assert.Equal(t, []string{"log-new"}, hfLogIDs(t, store.db, "logs"))
	assert.Equal(t, []string{"mcp-new"}, hfLogIDs(t, store.db, "mcp_tool_logs"))
}

// hfOwnerTimestampIndexes are the H4 composite indexes, per table.
var hfOwnerTimestampIndexes = map[string][]string{
	"logs":          {"idx_logs_vk_ts", "idx_logs_user_ts", "idx_logs_team_ts"},
	"mcp_tool_logs": {"idx_mcp_logs_vk_ts", "idx_mcp_logs_user_ts", "idx_mcp_logs_team_ts"},
}

// hfReplacedSingleIndexes are the single-column indexes the composites replace.
var hfReplacedSingleIndexes = map[string][]string{
	"logs":          {"idx_logs_virtual_key_id", "idx_logs_user_id", "idx_logs_team_id"},
	"mcp_tool_logs": {"idx_mcp_logs_virtual_key_id", "idx_mcp_logs_user_id", "idx_mcp_logs_team_id"},
}

// TestHighFixesCreateTableBuildsOwnerTimestampIndexes pins that a fresh install
// (CreateTable from the struct tags) builds the (owner, timestamp) composites,
// and that a migrated SQLite store ends with the composites and without the
// single-column indexes they replace.
func TestHighFixesCreateTableBuildsOwnerTimestampIndexes(t *testing.T) {
	dsn := "file:" + url.QueryEscape(t.Name()) + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.Migrator().CreateTable(&Log{}, &MCPToolLog{}))
	for _, name := range hfOwnerTimestampIndexes["logs"] {
		assert.True(t, db.Migrator().HasIndex(&Log{}, name), "fresh logs table must have %s", name)
	}
	for _, name := range hfOwnerTimestampIndexes["mcp_tool_logs"] {
		assert.True(t, db.Migrator().HasIndex(&MCPToolLog{}, name), "fresh mcp_tool_logs table must have %s", name)
	}

	store := newTestSQLiteStore(t)
	for table, names := range hfOwnerTimestampIndexes {
		for _, name := range names {
			assert.True(t, store.db.Migrator().HasIndex(table, name), "migrated %s must have %s", table, name)
		}
	}
	for table, names := range hfReplacedSingleIndexes {
		for _, name := range names {
			assert.False(t, store.db.Migrator().HasIndex(table, name), "migrated %s must not keep %s", table, name)
		}
	}
}

// hfRankingNameIndexes are the (id, timestamp) composites behind the ranking
// name fallback, each with the single-column index it replaces.
var hfRankingNameIndexes = []struct{ name, column, replaces string }{
	{"idx_logs_selected_key_ts", "selected_key_id", "idx_logs_selected_key_id"},
	{"idx_logs_routing_rule_ts", "routing_rule_id", "idx_logs_routing_rule_id"},
	{"idx_logs_customer_ts", "customer_id", "idx_logs_customer_id"},
	{"idx_logs_business_unit_ts", "business_unit_id", "idx_logs_business_unit_id"},
	{"idx_logs_project_ts", "project_id", "idx_logs_project_id"},
}

// TestHighFixesRankingNameIndexes pins that a fresh install builds the
// (id, timestamp) composites the ranking name fallback walks, and that a
// migrated SQLite store ends with them and without the single-column indexes
// they replace.
func TestHighFixesRankingNameIndexes(t *testing.T) {
	dsn := "file:" + url.QueryEscape(t.Name()) + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.Migrator().CreateTable(&Log{}))
	for _, idx := range hfRankingNameIndexes {
		assert.True(t, db.Migrator().HasIndex(&Log{}, idx.name), "fresh logs table must have %s", idx.name)
	}

	store := newTestSQLiteStore(t)
	for _, idx := range hfRankingNameIndexes {
		assert.True(t, store.db.Migrator().HasIndex("logs", idx.name), "migrated logs must have %s", idx.name)
		assert.False(t, store.db.Migrator().HasIndex("logs", idx.replaces), "migrated logs must not keep %s", idx.replaces)
	}
}

// hfBulkInsertLogs inserts n logs rows in one statement through a recursive CTE.
func hfBulkInsertLogs(t *testing.T, db *gorm.DB, prefix string, n int) {
	t.Helper()
	require.NoError(t, db.Exec(`
		WITH RECURSIVE seq(x) AS (SELECT 1 UNION ALL SELECT x + 1 FROM seq WHERE x < ?)
		INSERT INTO logs (id, timestamp, object_type, provider, model, status, created_at)
		SELECT ? || x, CURRENT_TIMESTAMP, 'chat_completion', 'openai', 'gpt-4o', 'success', CURRENT_TIMESTAMP FROM seq
	`, n, prefix).Error)
}

// hfBulkInsertMCP inserts n mcp_tool_logs rows in one statement.
func hfBulkInsertMCP(t *testing.T, db *gorm.DB, prefix string, n int) {
	t.Helper()
	require.NoError(t, db.Exec(`
		WITH RECURSIVE seq(x) AS (SELECT 1 UNION ALL SELECT x + 1 FROM seq WHERE x < ?)
		INSERT INTO mcp_tool_logs (id, request_id, timestamp, tool_name, status, created_at)
		SELECT ? || x, ? || x, CURRENT_TIMESTAMP, 'search', 'success', CURRENT_TIMESTAMP FROM seq
	`, n, prefix, prefix).Error)
}

// TestHighFixesSearchCountIsExact pins that the logs and MCP logs list counts
// stay exact above 10,000 matching rows, as they were before the scale work:
// the pagination footer, page count and navigation all depend on the real
// total, so the count must never stop at a cap. SkipCount still skips it.
func TestHighFixesSearchCountIsExact(t *testing.T) {
	const many = 10045
	store := newTestSQLiteStore(t)
	ctx := context.Background()

	hfBulkInsertLogs(t, store.db, "few-", 5)
	res, err := store.SearchLogs(ctx, SearchFilters{}, PaginationOptions{Limit: 10})
	require.NoError(t, err)
	assert.Equal(t, int64(5), res.Pagination.TotalCount)

	hfBulkInsertLogs(t, store.db, "many-", many)
	res, err = store.SearchLogs(ctx, SearchFilters{}, PaginationOptions{Limit: 10})
	require.NoError(t, err)
	assert.Equal(t, int64(many+5), res.Pagination.TotalCount, "the logs count must be exact above 10,000")
	assert.Len(t, res.Logs, 10)

	res, err = store.SearchLogs(ctx, SearchFilters{}, PaginationOptions{Limit: 10, SkipCount: true})
	require.NoError(t, err)
	assert.Equal(t, int64(0), res.Pagination.TotalCount, "SkipCount runs no count")
	assert.Len(t, res.Logs, 10)

	hfBulkInsertMCP(t, store.db, "mcp-few-", 3)
	mres, err := store.SearchMCPToolLogs(ctx, MCPToolLogSearchFilters{}, PaginationOptions{Limit: 10})
	require.NoError(t, err)
	assert.Equal(t, int64(3), mres.Pagination.TotalCount)

	hfBulkInsertMCP(t, store.db, "mcp-many-", many)
	mres, err = store.SearchMCPToolLogs(ctx, MCPToolLogSearchFilters{}, PaginationOptions{Limit: 10})
	require.NoError(t, err)
	assert.Equal(t, int64(many+3), mres.Pagination.TotalCount, "the MCP logs count must be exact above 10,000")
}

// TestHighFixesKeysetPagination pins the (timestamp, id) keyset cursor: pages
// taken after the last row of the previous page cover every row exactly once,
// including rows that share one timestamp.
func TestHighFixesKeysetPagination(t *testing.T) {
	store := newTestSQLiteStore(t)
	ctx := context.Background()
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	var want []string
	for i := range 11 {
		id := fmt.Sprintf("k%02d", i)
		// Pairs of rows share a timestamp.
		hfInsertLogAt(t, store, id, base.Add(time.Duration(i/2)*time.Second))
		want = append(want, id)
	}
	start := base.Add(-time.Minute)
	end := base.Add(time.Hour)
	filters := SearchFilters{StartTime: &start, EndTime: &end}
	var got []string
	pagination := PaginationOptions{Limit: 3, SortBy: "timestamp", Order: "asc", SkipCount: true}
	for range 10 {
		res, err := store.SearchLogs(ctx, filters, pagination)
		require.NoError(t, err)
		for _, l := range res.Logs {
			got = append(got, l.ID)
		}
		if len(res.Logs) < 3 {
			break
		}
		last := res.Logs[len(res.Logs)-1]
		ts := last.Timestamp
		pagination.AfterTimestamp = &ts
		pagination.AfterID = last.ID
	}
	require.Equal(t, want, got)
}

// hfQueryCapture records the SQL and bind variables of every query a gorm DB runs.
type hfQueryCapture struct {
	mu    sync.Mutex
	stmts []hfCapturedStmt
}

// hfCapturedStmt is one captured query: its SQL and the same SQL with the bind
// variables inlined by the dialect.
type hfCapturedStmt struct {
	sql      string
	rendered string
}

// hfCaptureQueries registers a query callback on db for the rest of the test.
func hfCaptureQueries(t *testing.T, db *gorm.DB) *hfQueryCapture {
	t.Helper()
	c := &hfQueryCapture{}
	name := "hf:capture:" + t.Name()
	record := func(tx *gorm.DB) {
		c.mu.Lock()
		defer c.mu.Unlock()
		sqlText := tx.Statement.SQL.String()
		c.stmts = append(c.stmts, hfCapturedStmt{sql: sqlText, rendered: tx.Dialector.Explain(sqlText, tx.Statement.Vars...)})
	}
	require.NoError(t, db.Callback().Query().After("gorm:query").Register(name, record))
	require.NoError(t, db.Callback().Row().After("gorm:row").Register(name, record))
	t.Cleanup(func() {
		_ = db.Callback().Query().Remove(name)
		_ = db.Callback().Row().Remove(name)
	})
	return c
}

// snapshot returns a copy of the captured statements.
func (c *hfQueryCapture) snapshot() []hfCapturedStmt {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]hfCapturedStmt(nil), c.stmts...)
}

// hfHasStatusList reports whether the rendered statement filters status on
// exactly the given list, whatever quote style the dialect renders.
func hfHasStatusList(s hfCapturedStmt, want []string) bool {
	plain := strings.NewReplacer(`"`, "", "'", "", " ", "").Replace(s.rendered)
	return strings.Contains(plain, "statusIN("+strings.Join(want, ",")+")")
}

// TestHighFixesErrorDimensionsBoundByFailedStatus pins H9: error_type,
// error_code and status_code rankings only read failed rows (status error or
// cancelled), so idx_logs_status_timestamp bounds the scan, and the results
// are unchanged because only failed rows carry error_details.
func TestHighFixesErrorDimensionsBoundByFailedStatus(t *testing.T) {
	store := newTestSQLiteStore(t)
	ctx := context.Background()
	base := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)

	create := func(id, status string, ts time.Time, errType, errCode string, code int) {
		l := &Log{ID: id, Timestamp: ts, CreatedAt: ts, Object: "chat_completion", Provider: "openai", Model: "gpt-4o", Status: status, TotalTokens: 10, Cost: new(0.01)}
		if errType != "" {
			l.ErrorDetailsParsed = &schemas.BifrostError{StatusCode: new(code), Error: &schemas.ErrorField{Type: new(errType), Code: new(errCode), Message: "m"}}
		}
		require.NoError(t, store.Create(ctx, l))
	}
	for i := range 5 {
		create(fmt.Sprintf("ok-%d", i), "success", base.Add(time.Duration(i)*time.Minute), "", "", 0)
	}
	create("e1", "error", base.Add(10*time.Minute), "rate_limit_error", "rate_limited", 429)
	create("e2", "error", base.Add(11*time.Minute), "rate_limit_error", "rate_limited", 429)
	create("e3", "error", base.Add(12*time.Minute), "authentication_error", "invalid_api_key", 401)
	create("c1", "cancelled", base.Add(13*time.Minute), "request_cancelled", "client_closed", 499)
	// Previous period: one error so the trend is populated.
	create("prev-e1", "error", base.Add(-30*time.Minute), "rate_limit_error", "rate_limited", 429)

	start := base.Add(-time.Minute)
	end := base.Add(time.Hour)
	filters := SearchFilters{StartTime: &start, EndTime: &end}

	capture := hfCaptureQueries(t, store.db)

	type row struct {
		id    string
		total int64
		trend bool
	}
	expect := map[RankingDimension][]row{
		RankingDimensionErrorType:  {{"rate_limit_error", 2, true}, {"authentication_error", 1, false}, {"request_cancelled", 1, false}},
		RankingDimensionErrorCode:  {{"rate_limited", 2, true}, {"client_closed", 1, false}, {"invalid_api_key", 1, false}},
		RankingDimensionStatusCode: {{"429", 2, true}, {"401", 1, false}, {"499", 1, false}},
	}
	for dim, want := range expect {
		res, err := store.GetJSONFieldDimensionRankings(ctx, filters, dim)
		require.NoError(t, err, dim)
		var got []row
		for _, r := range res.Rankings {
			got = append(got, row{r.ID, r.TotalRequests, r.Trend.HasPreviousPeriod})
		}
		assert.Equal(t, want, got, dim)
		assert.Equal(t, int64(9), res.TotalActualRequests, "actual requests still count every terminal row (%s)", dim)
		assert.Equal(t, int64(4), res.TotalAttributedRequests, dim)
	}

	bounded := 0
	for _, s := range capture.snapshot() {
		if hfHasStatusList(s, failedLogStatuses) {
			bounded++
		}
	}
	// Per dimension: current rankings, attributed count, previous period.
	assert.GreaterOrEqual(t, bounded, 9, "every error-dimension scan must carry the failed-status predicate")
}

// TestHighFixesMCPSearchIgnoresKeysetCursor pins what PaginationOptions documents:
// the keyset cursor is honoured by the LLM log searches only, and
// SearchMCPToolLogs pages by Offset exactly as before, so a cursor set on its
// options changes nothing.
func TestHighFixesMCPSearchIgnoresKeysetCursor(t *testing.T) {
	store := newTestSQLiteStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	hfInsertMCPAt(t, store, "mcp-1", now.Add(-3*time.Minute))
	hfInsertMCPAt(t, store, "mcp-2", now.Add(-2*time.Minute))
	hfInsertMCPAt(t, store, "mcp-3", now.Add(-1*time.Minute))

	plain, err := store.SearchMCPToolLogs(ctx, MCPToolLogSearchFilters{}, PaginationOptions{Limit: 10})
	require.NoError(t, err)
	newest := now.Add(-1 * time.Minute)
	withCursor, err := store.SearchMCPToolLogs(ctx, MCPToolLogSearchFilters{}, PaginationOptions{Limit: 10, AfterTimestamp: &newest, AfterID: "mcp-3"})
	require.NoError(t, err)
	require.Len(t, withCursor.Logs, 3, "the MCP search must ignore the keyset cursor")
	for i := range plain.Logs {
		require.Equal(t, plain.Logs[i].ID, withCursor.Logs[i].ID)
	}
}
