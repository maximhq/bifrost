package logstore

import (
	"context"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/queryscope"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// newAgentAggregateFixture seeds a store with two agents' worth of terminal and
// in-flight entries inside a one-hour window.
func newAgentAggregateFixture(t *testing.T) (*RDBLogStore, time.Time) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&AgentLog{}))
	store := &RDBLogStore{db: db, logger: bifrost.NewDefaultLogger(schemas.LogLevelInfo)}

	now := time.Now().UTC().Truncate(time.Hour)
	taskA, taskB := "task-alpha", "task-beta"
	latency := func(v float64) *float64 { return &v }
	requestTask := "task-search-parent"
	eventArtifact := "artifact-only-on-event"
	require.NoError(t, agentLogsCreateError(store.BatchCreateAgentLogsIfNotExists(context.Background(), []*AgentLog{
		{ID: "a-1", Timestamp: now, RecordKind: "request", Operation: "SendMessage", Status: "success", AgentName: "alpha", RequestID: "req-1", TaskID: &taskA, Latency: latency(100)},
		{ID: "a-2", Timestamp: now.Add(time.Minute), RecordKind: "request", Operation: "SendMessage", Status: "error", AgentName: "alpha", RequestID: "req-2", TaskID: &taskA, Latency: latency(300)},
		{ID: "a-3", Timestamp: now.Add(2 * time.Minute), RecordKind: "request", Operation: "GetTask", Status: "processing", AgentName: "alpha", RequestID: "req-3", TaskID: &taskA},
		{ID: "b-1", Timestamp: now.Add(3 * time.Minute), RecordKind: "request", Operation: "SendMessage", Status: "success", AgentName: "beta", RequestID: "req-4", TaskID: &taskB, Latency: latency(500)},
		{ID: "search-parent", Timestamp: now.Add(4 * time.Minute), RecordKind: "request", Operation: "SendStreamingMessage", Status: "success", AgentName: "alpha", RequestID: "req-search", TaskID: &requestTask},
		{ID: "search-event", Timestamp: now.Add(5 * time.Minute), RecordKind: "event", Operation: "SendStreamingMessage", Status: "success", AgentName: "alpha", RequestID: "req-search", TaskID: &requestTask, ArtifactID: &eventArtifact},
	})))
	return store, now
}

func agentWindow(now time.Time) AgentLogHistoryFilter {
	start, end := now.Add(-time.Minute), now.Add(time.Hour)
	return AgentLogHistoryFilter{StartTime: &start, EndTime: &end}
}

// Multi-value filters must narrow the aggregates exactly as they narrow the
// list, otherwise the cards would report totals over rows the table excludes.
func TestGetAgentLogStatsAppliesFilters(t *testing.T) {
	store, now := newAgentAggregateFixture(t)
	ctx := context.Background()

	all, err := store.GetAgentLogStats(ctx, agentWindow(now))
	require.NoError(t, err)
	require.EqualValues(t, 6, all.TotalEntries)
	require.EqualValues(t, 4, all.SuccessCount)
	require.EqualValues(t, 1, all.ErrorCount)
	require.InDelta(t, 80, all.SuccessRate, 0.01)
	require.InDelta(t, 300, all.AverageLatency, 0.01)

	filter := agentWindow(now)
	filter.AgentName = []string{"alpha"}
	filter.Operation = []string{"SendMessage"}
	scoped, err := store.GetAgentLogStats(ctx, filter)
	require.NoError(t, err)
	require.EqualValues(t, 2, scoped.TotalEntries)
	require.EqualValues(t, 1, scoped.SuccessCount)
	require.EqualValues(t, 1, scoped.ErrorCount)
	require.InDelta(t, 50, scoped.SuccessRate, 0.01)

	// A filter that matches nothing must still return zeroed stats, not an error.
	empty := agentWindow(now)
	empty.AgentName = []string{"nobody"}
	none, err := store.GetAgentLogStats(ctx, empty)
	require.NoError(t, err)
	require.EqualValues(t, 0, none.TotalEntries)
	require.Zero(t, none.SuccessRate)
}

// The free-text search must match DB-resident metadata columns, case-insensitively.
func TestGetAgentLogStatsSearchMatchesMetadataColumn(t *testing.T) {
	store, now := newAgentAggregateFixture(t)
	ctx := context.Background()

	byAgent := agentWindow(now)
	byAgent.Search = "BETA"
	stats, err := store.GetAgentLogStats(ctx, byAgent)
	require.NoError(t, err)
	require.EqualValues(t, 1, stats.TotalEntries)

	byTask := agentWindow(now)
	byTask.Search = "task-alpha"
	stats, err = store.GetAgentLogStats(ctx, byTask)
	require.NoError(t, err)
	require.EqualValues(t, 3, stats.TotalEntries)

	byOperation := agentWindow(now)
	byOperation.Search = "GetTask"
	list, err := store.ListAgentLogHistory(ctx, byOperation, PaginationOptions{Limit: 10, Order: "desc"})
	require.NoError(t, err)
	require.Len(t, list.Logs, 1)
	require.Equal(t, "a-3", list.Logs[0].ID)

	byEventArtifact := agentWindow(now)
	byEventArtifact.RecordKind = []string{"request"}
	byEventArtifact.Search = "artifact-only-on-event"
	list, err = store.ListAgentLogHistory(ctx, byEventArtifact, PaginationOptions{Limit: 10, Order: "desc"})
	require.NoError(t, err)
	require.Len(t, list.Logs, 1)
	require.Equal(t, "search-parent", list.Logs[0].ID)

	miss := agentWindow(now)
	miss.Search = "not-present-anywhere"
	stats, err = store.GetAgentLogStats(ctx, miss)
	require.NoError(t, err)
	require.EqualValues(t, 0, stats.TotalEntries)
}

func TestA2ATaskStateFilterUsesLatestStateEventForParentOperations(t *testing.T) {
	store, now := newAgentAggregateFixture(t)
	ctx := context.Background()
	working, completed, failed := "working", "completed", "failed"
	sequence1, sequence2, sequence3 := int64(1), int64(2), int64(3)
	taskA, taskB := "state-task-a", "state-task-b"

	require.NoError(t, agentLogsCreateError(store.BatchCreateAgentLogsIfNotExists(ctx, []*AgentLog{
		{ID: "state-parent-a", Timestamp: now.Add(10 * time.Minute), RecordKind: "request", Operation: "SendStreamingMessage", Status: "success", AgentName: "alpha", RequestID: "state-req-a", TaskID: &taskA},
		{ID: "state-a-working", Timestamp: now.Add(11 * time.Minute), RecordKind: "event", Operation: "SendStreamingMessage", Status: "success", AgentName: "alpha", RequestID: "state-req-a", TaskID: &taskA, EventSequence: &sequence1, TaskState: &working},
		{ID: "state-a-completed", Timestamp: now.Add(12 * time.Minute), RecordKind: "event", Operation: "SendStreamingMessage", Status: "success", AgentName: "alpha", RequestID: "state-req-a", TaskID: &taskA, EventSequence: &sequence2, TaskState: &completed},
		// A later non-state event must not hide the latest task-state event.
		{ID: "state-a-artifact", Timestamp: now.Add(13 * time.Minute), RecordKind: "event", Operation: "SendStreamingMessage", Status: "success", AgentName: "alpha", RequestID: "state-req-a", TaskID: &taskA, EventSequence: &sequence3},
		{ID: "state-parent-b", Timestamp: now.Add(20 * time.Minute), RecordKind: "request", Operation: "SendStreamingMessage", Status: "error", AgentName: "beta", RequestID: "state-req-b", TaskID: &taskB},
		{ID: "state-b-failed", Timestamp: now.Add(21 * time.Minute), RecordKind: "event", Operation: "SendStreamingMessage", Status: "success", AgentName: "beta", RequestID: "state-req-b", TaskID: &taskB, EventSequence: &sequence1, TaskState: &failed},
	})))

	filter := agentWindow(now)
	filter.RecordKind = []string{"request"}
	filter.TaskState = []string{"completed"}

	list, err := store.ListAgentLogHistory(ctx, filter, PaginationOptions{Limit: 10, Order: "desc"})
	require.NoError(t, err)
	require.Len(t, list.Logs, 1)
	require.Equal(t, "state-parent-a", list.Logs[0].ID)

	stats, err := store.GetAgentLogStats(ctx, filter)
	require.NoError(t, err)
	require.EqualValues(t, 1, stats.TotalEntries)
	require.EqualValues(t, 1, stats.SuccessCount)

	histogram, err := store.GetAgentHistogram(ctx, filter, 3600)
	require.NoError(t, err)
	var count int64
	for _, bucket := range histogram.Buckets {
		count += bucket.Count
	}
	require.EqualValues(t, 1, count)

	filter.TaskState = []string{"working"}
	list, err = store.ListAgentLogHistory(ctx, filter, PaginationOptions{Limit: 10, Order: "desc"})
	require.NoError(t, err)
	require.Empty(t, list.Logs)
}

func TestGetAgentHistogramBucketsAndScope(t *testing.T) {
	store, now := newAgentAggregateFixture(t)

	result, err := store.GetAgentHistogram(context.Background(), agentWindow(now), 3600)
	require.NoError(t, err)
	require.EqualValues(t, 3600, result.BucketSizeSeconds)
	var count, success, errors int64
	for _, bucket := range result.Buckets {
		count += bucket.Count
		success += bucket.Success
		errors += bucket.Error
	}
	require.EqualValues(t, 6, count)
	require.EqualValues(t, 4, success)
	require.EqualValues(t, 1, errors)

	// The DAC query scope must narrow the aggregates too.
	scoped := queryscope.WithQueryScope(context.Background(), func(db *gorm.DB) *gorm.DB {
		return db.Where("agent_name = ?", "beta")
	})
	result, err = store.GetAgentHistogram(scoped, agentWindow(now), 3600)
	require.NoError(t, err)
	count = 0
	for _, bucket := range result.Buckets {
		count += bucket.Count
	}
	require.EqualValues(t, 1, count)

	stats, err := store.GetAgentLogStats(scoped, agentWindow(now))
	require.NoError(t, err)
	require.EqualValues(t, 1, stats.TotalEntries)

	// An unbounded search is allowed, matching the LLM and MCP aggregates.
	stats, err = store.GetAgentLogStats(context.Background(), AgentLogHistoryFilter{})
	require.NoError(t, err)
	require.EqualValues(t, 6, stats.TotalEntries)
	_, err = store.GetAgentHistogram(context.Background(), AgentLogHistoryFilter{}, 3600)
	require.NoError(t, err)
}
