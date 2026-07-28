package logstore

// Retention coverage for the LogsCleaner. The MCP tool log sweep is driven
// through the optional MCPToolLogRetentionManager interface, so most of these
// tests script a fake manager to pin down the loop's termination, cancellation
// and error behaviour without needing a database. The tail of the file uses a
// real SQLite store to prove the RDB implementation prunes the right table.

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/objectstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Every backend the cleaner can be handed must prune MCP tool logs by age, or
// mcp_tool_logs silently grows without bound on that backend. Compile-time
// assertions keep a new store (or a dropped override) from regressing that.
var (
	_ MCPToolLogRetentionManager = (*RDBLogStore)(nil)
	_ MCPToolLogRetentionManager = (*ClickHouseLogStore)(nil)
	_ MCPToolLogRetentionManager = (*HybridLogStore)(nil)
)

// --- Fakes ---

// retentionCall records the arguments one batch delete was invoked with.
type retentionCall struct {
	cutoff    time.Time
	batchSize int
}

// retentionResult is one scripted reply. Calls past the end of a script get
// (0, nil), i.e. "nothing left to delete".
type retentionResult struct {
	deleted int64
	err     error
}

func scriptedResult(results []retentionResult, callIndex int) (int64, error) {
	if callIndex < len(results) {
		return results[callIndex].deleted, results[callIndex].err
	}
	return 0, nil
}

// logsOnlyManager implements LogRetentionManager and deliberately nothing
// else, standing in for a store that cannot prune MCP tool logs by age.
// onLogCall runs before the scripted reply is returned so a test can cancel
// the context from inside the logs loop.
type logsOnlyManager struct {
	logCalls   []retentionCall
	logResults []retentionResult
	onLogCall  func(callIndex int)
}

func (m *logsOnlyManager) DeleteLogsBatch(_ context.Context, cutoff time.Time, batchSize int) (int64, error) {
	m.logCalls = append(m.logCalls, retentionCall{cutoff: cutoff, batchSize: batchSize})
	if m.onLogCall != nil {
		m.onLogCall(len(m.logCalls) - 1)
	}
	return scriptedResult(m.logResults, len(m.logCalls)-1)
}

// logsAndMCPManager additionally implements MCPToolLogRetentionManager.
// onMCPCall runs before the scripted reply is returned so a test can cancel
// the context from inside the loop.
type logsAndMCPManager struct {
	logsOnlyManager
	mcpCalls   []retentionCall
	mcpResults []retentionResult
	onMCPCall  func(callIndex int)
}

func (m *logsAndMCPManager) DeleteMCPToolLogsBatch(_ context.Context, cutoff time.Time, batchSize int) (int64, error) {
	m.mcpCalls = append(m.mcpCalls, retentionCall{cutoff: cutoff, batchSize: batchSize})
	if m.onMCPCall != nil {
		m.onMCPCall(len(m.mcpCalls) - 1)
	}
	return scriptedResult(m.mcpResults, len(m.mcpCalls)-1)
}

// recordingLogger captures formatted Error and Warn lines so tests can assert
// the cleaner reported a failure instead of swallowing it, and can tell the
// logs sweep's messages apart from the MCP sweep's.
type recordingLogger struct {
	mu     sync.Mutex
	errors []string
	warns  []string
}

func (l *recordingLogger) Debug(string, ...any) {}
func (l *recordingLogger) Info(string, ...any)  {}
func (l *recordingLogger) Warn(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.warns = append(l.warns, fmt.Sprintf(format, args...))
}
func (l *recordingLogger) Error(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.errors = append(l.errors, fmt.Sprintf(format, args...))
}
func (l *recordingLogger) Fatal(string, ...any)                   {}
func (l *recordingLogger) SetLevel(schemas.LogLevel)              {}
func (l *recordingLogger) SetOutputType(schemas.LoggerOutputType) {}
func (l *recordingLogger) LogHTTPRequest(schemas.LogLevel, string) schemas.LogEventBuilder {
	return schemas.NoopLogEvent
}

func newTestCleaner(manager LogRetentionManager, logger schemas.Logger) *LogsCleaner {
	return NewLogsCleaner(manager, CleanerConfig{RetentionDays: 30}, logger)
}

// --- Failure and edge cases ---

// A store without the optional interface must keep working: the logs sweep
// still runs and nothing is reported as an error. This is the regression the
// separate interface exists to prevent.
func TestLogsCleaner_StoreWithoutMCPSupportStillPrunesLogs(t *testing.T) {
	_, implements := any(&logsOnlyManager{}).(MCPToolLogRetentionManager)
	require.False(t, implements, "fixture must not implement the optional MCP interface")

	manager := &logsOnlyManager{logResults: []retentionResult{{deleted: 7}}}
	logger := &recordingLogger{}

	newTestCleaner(manager, logger).cleanupOldLogs(context.Background())

	assert.Len(t, manager.logCalls, 1, "a short batch drains the logs table in one pass")
	assert.Empty(t, logger.errors, "an MCP-incapable store is not an error")
	assert.Empty(t, logger.warns)
}

// An MCP batch failure must be logged and must stop only the MCP loop - the
// logs sweep already completed and its work must not be re-attempted or lost.
func TestLogsCleaner_MCPBatchErrorStopsOnlyMCPLoop(t *testing.T) {
	manager := &logsAndMCPManager{
		logsOnlyManager: logsOnlyManager{logResults: []retentionResult{{deleted: 5}}},
		mcpResults:      []retentionResult{{err: errors.New("mcp delete exploded")}},
	}
	logger := &recordingLogger{}

	newTestCleaner(manager, logger).cleanupOldLogs(context.Background())

	assert.Len(t, manager.logCalls, 1, "logs sweep must have run to completion")
	assert.Len(t, manager.mcpCalls, 1, "MCP loop must stop on the failing batch")
	require.Len(t, logger.errors, 1, "the MCP failure must be logged, not swallowed")
	assert.Contains(t, logger.errors[0], "mcp tool logs", "the MCP sweep must be the reported failure")
}

// A failing logs sweep must NOT starve MCP pruning. The two tables are
// independent, so a persistently broken logs delete would otherwise leave
// mcp_tool_logs growing without bound - the very bug this sweep fixes.
func TestLogsCleaner_LogsBatchErrorStillPrunesMCPToolLogs(t *testing.T) {
	manager := &logsAndMCPManager{
		logsOnlyManager: logsOnlyManager{logResults: []retentionResult{{err: errors.New("logs delete exploded")}}},
		mcpResults:      []retentionResult{{deleted: 4}},
	}
	logger := &recordingLogger{}

	newTestCleaner(manager, logger).cleanupOldLogs(context.Background())

	assert.Len(t, manager.logCalls, 1)
	assert.Len(t, manager.mcpCalls, 1, "MCP sweep must run even though the logs sweep failed")
	require.Len(t, logger.errors, 1)
	assert.Contains(t, logger.errors[0], "logs delete exploded")
}

// A logs sweep cancelled mid-run leaves a dead context, so the MCP sweep must
// bail immediately rather than issuing deletes that cannot succeed.
func TestLogsCleaner_LogsSweepCancelledSkipsMCPDeletes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	manager := &logsAndMCPManager{
		logsOnlyManager: logsOnlyManager{logResults: []retentionResult{{deleted: int64(batchSize)}}},
	}
	manager.onLogCall = func(int) { cancel() }
	logger := &recordingLogger{}

	newTestCleaner(manager, logger).cleanupOldLogs(ctx)

	assert.Len(t, manager.logCalls, 1)
	assert.Empty(t, manager.mcpCalls, "a dead context must not drive MCP deletes")
	assert.Len(t, logger.warns, 2, "both sweeps report the cancellation")
}

// Zero matching rows must terminate after a single probe - no second call and
// no delete issued behind it.
func TestLogsCleaner_MCPZeroRowsIssuesSingleCall(t *testing.T) {
	manager := &logsAndMCPManager{}

	newTestCleaner(manager, testLogger{}).cleanupOldLogs(context.Background())

	assert.Len(t, manager.mcpCalls, 1)
}

// A short batch means the table is drained, so the loop must not probe again.
func TestLogsCleaner_MCPFewerRowsThanBatchSizeStopsAfterOnePass(t *testing.T) {
	manager := &logsAndMCPManager{mcpResults: []retentionResult{{deleted: int64(batchSize) - 1}}}

	newTestCleaner(manager, testLogger{}).cleanupOldLogs(context.Background())

	assert.Len(t, manager.mcpCalls, 1)
	assert.Equal(t, batchSize, manager.mcpCalls[0].batchSize)
}

// Full batches must keep the loop going until a short batch drains the table.
func TestLogsCleaner_MCPMoreRowsThanBatchSizeIteratesUntilDrained(t *testing.T) {
	manager := &logsAndMCPManager{mcpResults: []retentionResult{
		{deleted: int64(batchSize)},
		{deleted: int64(batchSize)},
		{deleted: 3},
	}}

	newTestCleaner(manager, testLogger{}).cleanupOldLogs(context.Background())

	assert.Len(t, manager.mcpCalls, 3)
}

// An already-cancelled context must be noticed before any delete is issued.
func TestLogsCleaner_MCPAlreadyCancelledContextIssuesNoCalls(t *testing.T) {
	manager := &logsAndMCPManager{mcpResults: []retentionResult{{deleted: int64(batchSize)}}}
	logger := &recordingLogger{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	newTestCleaner(manager, logger).cleanupOldMCPToolLogs(ctx, time.Now().UTC())

	assert.Empty(t, manager.mcpCalls)
	assert.Len(t, logger.warns, 1, "cancellation must be reported")
}

// Cancelling mid-loop must break out at the next iteration rather than
// draining the whole table on a dead context.
func TestLogsCleaner_MCPContextCancelledMidLoopReturnsPromptly(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	full := retentionResult{deleted: int64(batchSize)}
	manager := &logsAndMCPManager{mcpResults: []retentionResult{full, full, full, full}}
	manager.onMCPCall = func(callIndex int) {
		if callIndex == 1 {
			cancel()
		}
	}
	logger := &recordingLogger{}

	newTestCleaner(manager, logger).cleanupOldMCPToolLogs(ctx, time.Now().UTC())

	assert.Len(t, manager.mcpCalls, 2, "loop must stop at the iteration after cancellation")
	assert.Len(t, logger.warns, 1)
}

// --- Happy paths ---

// Both tables are pruned against one cutoff derived from RetentionDays, so an
// operator cannot end up with logs and mcp_tool_logs on different horizons.
func TestLogsCleaner_MCPSweepUsesSameCutoffAsLogsSweep(t *testing.T) {
	manager := &logsAndMCPManager{
		logsOnlyManager: logsOnlyManager{logResults: []retentionResult{{deleted: 1}}},
		mcpResults:      []retentionResult{{deleted: 1}},
	}
	before := time.Now().UTC()

	NewLogsCleaner(manager, CleanerConfig{RetentionDays: 30}, testLogger{}).cleanupOldLogs(context.Background())

	require.NotEmpty(t, manager.logCalls)
	require.NotEmpty(t, manager.mcpCalls)
	cutoff := manager.logCalls[0].cutoff
	assert.Equal(t, cutoff, manager.mcpCalls[0].cutoff)
	assert.WithinDuration(t, before.AddDate(0, 0, -30), cutoff, time.Minute)
}

// A non-positive retention falls back to the default for both tables alike.
func TestLogsCleaner_MCPSweepUsesDefaultRetentionWhenUnset(t *testing.T) {
	manager := &logsAndMCPManager{}
	before := time.Now().UTC()

	NewLogsCleaner(manager, CleanerConfig{RetentionDays: 0}, testLogger{}).cleanupOldLogs(context.Background())

	require.NotEmpty(t, manager.mcpCalls)
	assert.WithinDuration(t, before.AddDate(0, 0, -defaultRetentionDays), manager.mcpCalls[0].cutoff, time.Minute)
}

// --- RDB implementation against a real SQLite store ---

func seedMCPToolLog(t *testing.T, store *RDBLogStore, id string, createdAt time.Time) {
	t.Helper()
	require.NoError(t, store.CreateMCPToolLog(context.Background(), &MCPToolLog{
		ID: id, Timestamp: createdAt, CreatedAt: createdAt,
		ToolName: "search_web", Status: "success",
	}))
}

func mcpToolLogIDs(t *testing.T, store *RDBLogStore) []string {
	t.Helper()
	var ids []string
	require.NoError(t, store.db.Model(&MCPToolLog{}).Order("id ASC").Pluck("id", &ids).Error)
	return ids
}

// Rows at or after the cutoff must survive; only strictly older rows go. The
// row seeded exactly at the cutoff pins the comparison as `<`, not `<=`.
func TestRDBDeleteMCPToolLogsBatch_KeepsRowsNewerThanCutoff(t *testing.T) {
	store := newTestSQLiteStore(t)
	now := time.Now().UTC().Truncate(time.Second)
	cutoff := now.Add(-24 * time.Hour)
	seedMCPToolLog(t, store, "old", now.Add(-48*time.Hour))
	seedMCPToolLog(t, store, "at-cutoff", cutoff)
	seedMCPToolLog(t, store, "new", now.Add(-1*time.Hour))

	deleted, err := store.DeleteMCPToolLogsBatch(context.Background(), cutoff, 100)

	require.NoError(t, err)
	assert.Equal(t, int64(1), deleted)
	assert.Equal(t, []string{"at-cutoff", "new"}, mcpToolLogIDs(t, store))
}

// No matching rows must report zero without erroring, so the cleaner's loop
// terminates on the first pass.
func TestRDBDeleteMCPToolLogsBatch_NoMatchingRowsReturnsZero(t *testing.T) {
	store := newTestSQLiteStore(t)
	now := time.Now().UTC().Truncate(time.Second)
	seedMCPToolLog(t, store, "new", now.Add(-1*time.Hour))

	deleted, err := store.DeleteMCPToolLogsBatch(context.Background(), now.Add(-24*time.Hour), 100)

	require.NoError(t, err)
	assert.Zero(t, deleted)
	assert.Equal(t, []string{"new"}, mcpToolLogIDs(t, store))
}

// An empty table is the degenerate case of the above and must not error.
func TestRDBDeleteMCPToolLogsBatch_EmptyTableReturnsZero(t *testing.T) {
	store := newTestSQLiteStore(t)

	deleted, err := store.DeleteMCPToolLogsBatch(context.Background(), time.Now().UTC(), 100)

	require.NoError(t, err)
	assert.Zero(t, deleted)
}

// The limit must be honoured so a large backlog is drained in bounded chunks
// rather than one table-locking DELETE.
func TestRDBDeleteMCPToolLogsBatch_RespectsBatchSize(t *testing.T) {
	store := newTestSQLiteStore(t)
	now := time.Now().UTC().Truncate(time.Second)
	for _, id := range []string{"a", "b", "c", "d", "e"} {
		seedMCPToolLog(t, store, id, now.Add(-48*time.Hour))
	}

	deleted, err := store.DeleteMCPToolLogsBatch(context.Background(), now.Add(-24*time.Hour), 2)

	require.NoError(t, err)
	assert.Equal(t, int64(2), deleted)
	assert.Len(t, mcpToolLogIDs(t, store), 3)
}

// A cancelled context must surface as an error rather than a silent no-op that
// the cleaner would read as "table drained".
func TestRDBDeleteMCPToolLogsBatch_CancelledContextErrors(t *testing.T) {
	store := newTestSQLiteStore(t)
	seedMCPToolLog(t, store, "old", time.Now().UTC().Add(-48*time.Hour))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := store.DeleteMCPToolLogsBatch(ctx, time.Now().UTC(), 100)

	require.Error(t, err)
	assert.Len(t, mcpToolLogIDs(t, store), 1)
}

// The two sweeps must stay on their own tables: pruning MCP tool logs must not
// touch logs, and vice versa.
func TestRDBDeleteMCPToolLogsBatch_LeavesLogsTableAlone(t *testing.T) {
	store := newTestSQLiteStore(t)
	ctx := context.Background()
	old := time.Now().UTC().Truncate(time.Second).Add(-48 * time.Hour)
	seedMCPToolLog(t, store, "mcp-old", old)
	require.NoError(t, store.Create(ctx, &Log{ID: "log-old", Timestamp: old, CreatedAt: old, Status: "success"}))

	deleted, err := store.DeleteMCPToolLogsBatch(ctx, time.Now().UTC(), 100)

	require.NoError(t, err)
	assert.Equal(t, int64(1), deleted)
	assert.Empty(t, mcpToolLogIDs(t, store))
	_, err = store.FindByID(ctx, "log-old")
	assert.NoError(t, err, "the logs table must be untouched by the MCP sweep")
}

// End to end: the cleaner drains both tables of a real store in one run.
func TestLogsCleaner_PrunesBothTablesOnSQLiteStore(t *testing.T) {
	store := newTestSQLiteStore(t)
	ctx := context.Background()
	old := time.Now().UTC().Truncate(time.Second).AddDate(0, 0, -60)
	seedMCPToolLog(t, store, "mcp-old", old)
	require.NoError(t, store.Create(ctx, &Log{ID: "log-old", Timestamp: old, CreatedAt: old, Status: "success"}))

	newTestCleaner(store, testLogger{}).cleanupOldLogs(ctx)

	assert.Empty(t, mcpToolLogIDs(t, store))
	_, err := store.FindByID(ctx, "log-old")
	assert.ErrorIs(t, err, ErrNotFound)
}

// --- Hybrid delegation ---

// The hybrid store must forward age-based MCP pruning to its inner store,
// otherwise object-storage deployments lose MCP retention entirely.
func TestHybrid_DeleteMCPToolLogsBatchDelegatesToInner(t *testing.T) {
	hybrid, inner, _ := newTestHybrid(t)
	defer hybrid.Close(context.Background())

	ctx := context.Background()
	old := time.Now().UTC().Truncate(time.Second).Add(-48 * time.Hour)
	require.NoError(t, inner.CreateMCPToolLog(ctx, &MCPToolLog{
		ID: "mcp-old", Timestamp: old, CreatedAt: old, ToolName: "search_web", Status: "success",
	}))

	deleted, err := hybrid.DeleteMCPToolLogsBatch(ctx, time.Now().UTC(), 100)

	require.NoError(t, err)
	assert.Equal(t, int64(1), deleted)
	_, err = inner.FindMCPToolLog(ctx, "mcp-old")
	assert.ErrorIs(t, err, ErrNotFound)
}

// An inner store that cannot prune MCP tool logs by age must degrade to a
// no-op rather than failing the type assertion.
func TestHybrid_DeleteMCPToolLogsBatchUnsupportedInnerReturnsZero(t *testing.T) {
	inner, err := newSqliteLogStore(context.Background(), &SQLiteConfig{
		Path: filepath.Join(t.TempDir(), "hybrid-unsupported.db"),
	}, hybridTestLogger{})
	require.NoError(t, err)

	wrapped := mcpRetentionUnawareStore{LogStore: inner}
	_, implements := any(wrapped).(MCPToolLogRetentionManager)
	require.False(t, implements, "fixture must not implement the optional MCP interface")

	hybrid := newHybridLogStore(wrapped, objectstore.NewInMemoryObjectStore(), "test", hybridTestLogger{}, nil)
	defer hybrid.Close(context.Background())

	deleted, err := hybrid.DeleteMCPToolLogsBatch(context.Background(), time.Now().UTC(), 100)

	require.NoError(t, err)
	assert.Zero(t, deleted)
}

// An inner failure must reach the cleaner unchanged, otherwise a broken store
// reads as a drained table and retention silently stops.
func TestHybrid_DeleteMCPToolLogsBatchPropagatesInnerError(t *testing.T) {
	inner, err := newSqliteLogStore(context.Background(), &SQLiteConfig{
		Path: filepath.Join(t.TempDir(), "hybrid-closed.db"),
	}, hybridTestLogger{})
	require.NoError(t, err)
	hybrid := newHybridLogStore(inner, objectstore.NewInMemoryObjectStore(), "test", hybridTestLogger{}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = hybrid.DeleteMCPToolLogsBatch(ctx, time.Now().UTC(), 100)

	require.Error(t, err)
}

// mcpRetentionUnawareStore is a complete LogStore that does not satisfy
// MCPToolLogRetentionManager, standing in for a decorator that predates
// age-based MCP pruning. Embedding the LogStore interface promotes only the
// LogStore method set, which excludes DeleteMCPToolLogsBatch; the wrong-shaped
// method below keeps the fixture unsupported even if DeleteMCPToolLogsBatch is
// later promoted into LogStore.
type mcpRetentionUnawareStore struct {
	LogStore
}

func (mcpRetentionUnawareStore) DeleteMCPToolLogsBatch() {}
