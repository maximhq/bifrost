package logstore

import (
	"context"
	"fmt"
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
	searchEventType := "artifact_update"
	require.NoError(t, agentLogsCreateError(store.BatchCreateAgentLogsIfNotExists(context.Background(), []*AgentLog{
		{ID: "a-1", Timestamp: now, RecordKind: "request", Operation: "SendMessage", Status: "success", AgentName: "alpha", RequestID: "req-1", TaskID: &taskA, Latency: latency(100)},
		{ID: "a-2", Timestamp: now.Add(time.Minute), RecordKind: "request", Operation: "SendMessage", Status: "error", AgentName: "alpha", RequestID: "req-2", TaskID: &taskA, Latency: latency(300)},
		{ID: "a-3", Timestamp: now.Add(2 * time.Minute), RecordKind: "request", Operation: "GetTask", Status: "processing", AgentName: "alpha", RequestID: "req-3", TaskID: &taskA},
		{ID: "b-1", Timestamp: now.Add(3 * time.Minute), RecordKind: "request", Operation: "SendMessage", Status: "success", AgentName: "beta", RequestID: "req-4", TaskID: &taskB, Latency: latency(500)},
		{ID: "search-parent", Timestamp: now.Add(4 * time.Minute), RecordKind: "request", Operation: "SendStreamingMessage", Status: "success", AgentName: "alpha", RequestID: "req-search", TaskID: &requestTask},
		{ID: "search-event", Timestamp: now.Add(5 * time.Minute), RecordKind: "event", Operation: "SendStreamingMessage", Status: "success", AgentName: "alpha", RequestID: "req-search", TaskID: &requestTask, ArtifactID: &eventArtifact, EventType: &searchEventType},
	})))
	return store, now
}

func agentWindow(now time.Time) AgentLogHistoryFilter {
	start, end := now.Add(-time.Minute), now.Add(time.Hour)
	return AgentLogHistoryFilter{StartTime: &start, EndTime: &end}
}

// TestFindAgentLogIgnoresMalformedErrorDetails verifies that a corrupt supplementary field does not make the entire log unreadable.
func TestFindAgentLogIgnoresMalformedErrorDetails(t *testing.T) {
	store, _ := newAgentAggregateFixture(t)
	require.NoError(t, store.db.Model(&AgentLog{}).Where("id = ?", "a-2").UpdateColumn("error_details", "not-json").Error)

	entry, err := store.FindAgentLog(context.Background(), "a-2")
	require.NoError(t, err)
	require.Equal(t, "a-2", entry.ID)
	require.Nil(t, entry.ErrorDetailsParsed)
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

// TestGetAgentTopAgentsRanksRequestRows verifies per-Agent counts, errors,
// latency, ordering, and that filters narrow the ranking like the operation list.
func TestGetAgentTopAgentsRanksRequestRows(t *testing.T) {
	store, now := newAgentAggregateFixture(t)
	ctx := context.Background()

	ranked, err := store.GetAgentTopAgents(ctx, agentWindow(now), 10)
	require.NoError(t, err)
	require.Len(t, ranked.Agents, 2)
	// The alpha stream event must not inflate its request count.
	require.Equal(t, "alpha", ranked.Agents[0].AgentName)
	require.EqualValues(t, 4, ranked.Agents[0].Count)
	require.EqualValues(t, 1, ranked.Agents[0].ErrorCount)
	require.InDelta(t, 200, ranked.Agents[0].AverageLatency, 0.01)
	require.Equal(t, "beta", ranked.Agents[1].AgentName)
	require.EqualValues(t, 1, ranked.Agents[1].Count)
	require.InDelta(t, 500, ranked.Agents[1].AverageLatency, 0.01)

	limited, err := store.GetAgentTopAgents(ctx, agentWindow(now), 1)
	require.NoError(t, err)
	require.Len(t, limited.Agents, 1)

	filter := agentWindow(now)
	filter.AgentName = []string{"beta"}
	scoped, err := store.GetAgentTopAgents(ctx, filter, 10)
	require.NoError(t, err)
	require.Len(t, scoped.Agents, 1)
	require.Equal(t, "beta", scoped.Agents[0].AgentName)

	// Agents with equal counts rank by name so the top-N cut is deterministic.
	// "aaa" is inserted after "alpha", so only the tiebreak can put it first.
	var tiedRows []*AgentLog
	for i := 0; i < 4; i++ {
		id := fmt.Sprintf("tie-%d", i)
		tiedRows = append(tiedRows, &AgentLog{ID: id, Timestamp: now.Add(10 * time.Minute), RecordKind: "request", Operation: "GetTask", Status: "success", AgentName: "aaa", RequestID: "req-" + id})
	}
	require.NoError(t, agentLogsCreateError(store.BatchCreateAgentLogsIfNotExists(ctx, tiedRows)))
	tiedRank, err := store.GetAgentTopAgents(ctx, agentWindow(now), 1)
	require.NoError(t, err)
	require.Len(t, tiedRank.Agents, 1)
	require.Equal(t, "aaa", tiedRank.Agents[0].AgentName)
	require.EqualValues(t, 4, tiedRank.Agents[0].Count)

	// A search matching only a child event still ranks its parent operation.
	search := agentWindow(now)
	search.Search = "artifact-only-on-event"
	matched, err := store.GetAgentTopAgents(ctx, search, 10)
	require.NoError(t, err)
	require.Len(t, matched.Agents, 1)
	require.Equal(t, "alpha", matched.Agents[0].AgentName)
	require.EqualValues(t, 1, matched.Agents[0].Count)

	// An event type only present on a child event still ranks its parent operation.
	byEventType := agentWindow(now)
	byEventType.EventType = []string{"artifact_update"}
	typed, err := store.GetAgentTopAgents(ctx, byEventType, 10)
	require.NoError(t, err)
	require.Len(t, typed.Agents, 1)
	require.Equal(t, "alpha", typed.Agents[0].AgentName)
	require.EqualValues(t, 1, typed.Agents[0].Count)
}

// TestListAgentLogOperationsMatchesEventTypeOnChildEvents verifies that the
// operation list resolves an event type filter through child events.
func TestListAgentLogOperationsMatchesEventTypeOnChildEvents(t *testing.T) {
	store, now := newAgentAggregateFixture(t)
	filter := agentWindow(now)
	filter.EventType = []string{"artifact_update"}
	result, err := store.ListAgentLogOperations(context.Background(), filter, PaginationOptions{Limit: 10, Order: "desc"})
	require.NoError(t, err)
	require.Len(t, result.Logs, 1)
	require.Equal(t, "search-parent", result.Logs[0].ID)
	require.Len(t, result.Logs[0].Events, 1)

	filter.EventType = []string{"no_such_event_type"}
	none, err := store.ListAgentLogOperations(context.Background(), filter, PaginationOptions{Limit: 10, Order: "desc"})
	require.NoError(t, err)
	require.Empty(t, none.Logs)
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

func TestAgentParentFiltersKeepCorrelatedEventsOutsideHistoryWindow(t *testing.T) {
	store, now := newAgentAggregateFixture(t)
	ctx := context.Background()
	start, end := now.Add(30*time.Minute), now.Add(31*time.Minute)
	working, completed := "working", "completed"
	sequence1, sequence2 := int64(1), int64(2)
	taskID := "windowed-task"
	artifactID := "before-window-artifact"

	require.NoError(t, agentLogsCreateError(store.BatchCreateAgentLogsIfNotExists(ctx, []*AgentLog{
		{ID: "windowed-parent", Timestamp: now.Add(30*time.Minute + 30*time.Second), RecordKind: "request", Operation: "SendStreamingMessage", Status: "success", AgentName: "alpha", RequestID: "windowed-request", TaskID: &taskID},
		{ID: "windowed-before", Timestamp: start.Add(-time.Second), RecordKind: "event", Operation: "SendStreamingMessage", Status: "success", AgentName: "alpha", RequestID: "windowed-request", TaskID: &taskID, ArtifactID: &artifactID, EventSequence: &sequence1, TaskState: &working},
		{ID: "windowed-after", Timestamp: end.Add(time.Second), RecordKind: "event", Operation: "SendStreamingMessage", Status: "success", AgentName: "alpha", RequestID: "windowed-request", TaskID: &taskID, EventSequence: &sequence2, TaskState: &completed},
	})))

	filter := AgentLogHistoryFilter{StartTime: &start, EndTime: &end, RecordKind: []string{"request"}, TaskState: []string{"completed"}}
	list, err := store.ListAgentLogHistory(ctx, filter, PaginationOptions{Limit: 10, Order: "desc"})
	require.NoError(t, err)
	require.Len(t, list.Logs, 1)
	require.Equal(t, "windowed-parent", list.Logs[0].ID)

	filter.TaskState = nil
	filter.Search = artifactID
	list, err = store.ListAgentLogHistory(ctx, filter, PaginationOptions{Limit: 10, Order: "desc"})
	require.NoError(t, err)
	require.Len(t, list.Logs, 1)
	require.Equal(t, "windowed-parent", list.Logs[0].ID)
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

// TestListAgentLogHistoryEventTypeKeepsOnlyMatchingEvents verifies that listing
// request and event rows together does not return events of other types.
func TestListAgentLogHistoryEventTypeKeepsOnlyMatchingEvents(t *testing.T) {
	store, now := newAgentAggregateFixture(t)
	statusType := "status_update"
	task := "task-search-parent"
	require.NoError(t, agentLogsCreateError(store.BatchCreateAgentLogsIfNotExists(context.Background(), []*AgentLog{
		{ID: "search-status-event", Timestamp: now.Add(6 * time.Minute), RecordKind: "event", Operation: "SendStreamingMessage", Status: "success", AgentName: "alpha", RequestID: "req-search", TaskID: &task, EventType: &statusType},
	})))

	filter := agentWindow(now)
	filter.RecordKind = []string{"request", "event"}
	filter.EventType = []string{"artifact_update"}
	result, err := store.ListAgentLogHistory(context.Background(), filter, PaginationOptions{Limit: 20, Order: "desc"})
	require.NoError(t, err)

	ids := make([]string, 0, len(result.Logs))
	for _, log := range result.Logs {
		ids = append(ids, log.ID)
	}
	require.ElementsMatch(t, []string{"search-parent", "search-event"}, ids)
}
