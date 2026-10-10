package logstore

import (
	"context"
	"fmt"
	"strings"
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

// GetStats reports input/output tokens alongside the total so the logs stats
// card can show the split. Only terminal requests contribute, matching the
// total/cost aggregates computed in the same query.
func TestGetStatsTokenSplit(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Log{}))

	s := &RDBLogStore{db: db, logger: bifrost.NewDefaultLogger(schemas.LogLevelInfo)}
	ctx := context.Background()
	now := time.Now()

	seed := []struct {
		id                        string
		prompt, completion, total int
		status                    string
	}{
		{"a", 100, 10, 110, "success"},
		{"b", 200, 20, 220, "success"},
		{"c", 400, 40, 440, "error"},       // terminal, must count
		{"d", 999, 99, 1098, "processing"}, // non-terminal, must NOT count
	}
	for _, sd := range seed {
		require.NoError(t, db.Create(&Log{
			ID:               sd.id,
			Timestamp:        now,
			Status:           sd.status,
			PromptTokens:     sd.prompt,
			CompletionTokens: sd.completion,
			TotalTokens:      sd.total,
		}).Error)
	}

	stats, err := s.GetStats(ctx, SearchFilters{})
	require.NoError(t, err)

	require.Equal(t, int64(770), stats.TotalTokens, "total excludes non-terminal")
	require.Equal(t, int64(700), stats.PromptTokens, "prompt = 100+200+400")
	require.Equal(t, int64(70), stats.CompletionTokens, "completion = 10+20+40")
	require.Equal(t, stats.TotalTokens, stats.PromptTokens+stats.CompletionTokens, "split sums to total")
}

func TestReconcileAgentCorrelationFillOnly(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&AgentLog{}))
	store := &RDBLogStore{db: db, logger: bifrost.NewDefaultLogger(schemas.LogLevelInfo)}
	ctx := context.Background()
	now := time.Now().UTC()
	taskID, contextID := "task-1", "context-1"
	otherTaskID, otherContextID := "task-existing", "context-existing"

	event := &AgentLog{ID: "event-first", Timestamp: now, RecordKind: "event", Status: "success", AgentName: "alpha", RequestID: "request-1", TaskID: &taskID, ContextID: &contextID}
	require.NoError(t, agentLogsCreateError(store.BatchCreateAgentLogsIfNotExists(ctx, []*AgentLog{event})))
	require.NoError(t, store.ReconcileAgentCorrelation(ctx, []*AgentLog{event}))
	request := &AgentLog{ID: "request-later", Timestamp: now.Add(time.Second), RecordKind: "request", Status: "success", AgentName: "alpha", RequestID: "request-1"}
	require.NoError(t, agentLogsCreateError(store.BatchCreateAgentLogsIfNotExists(ctx, []*AgentLog{request})))
	require.NoError(t, store.ReconcileAgentCorrelation(ctx, []*AgentLog{request}))

	found, err := store.FindAgentLog(ctx, request.ID)
	require.NoError(t, err)
	require.Equal(t, taskID, *found.TaskID)
	require.Equal(t, contextID, *found.ContextID)

	requestFirst := &AgentLog{ID: "request-first", Timestamp: now, RecordKind: "request", Status: "success", AgentName: "alpha", RequestID: "request-2"}
	require.NoError(t, agentLogsCreateError(store.BatchCreateAgentLogsIfNotExists(ctx, []*AgentLog{requestFirst})))
	require.NoError(t, store.ReconcileAgentCorrelation(ctx, []*AgentLog{requestFirst}))
	eventLater := &AgentLog{ID: "event-later", Timestamp: now.Add(time.Second), RecordKind: "event", Status: "success", AgentName: "alpha", RequestID: "request-2", TaskID: &taskID, ContextID: &contextID}
	require.NoError(t, agentLogsCreateError(store.BatchCreateAgentLogsIfNotExists(ctx, []*AgentLog{eventLater})))
	require.NoError(t, store.ReconcileAgentCorrelation(ctx, []*AgentLog{eventLater}))
	found, err = store.FindAgentLog(ctx, requestFirst.ID)
	require.NoError(t, err)
	require.Equal(t, taskID, *found.TaskID)
	require.Equal(t, contextID, *found.ContextID)

	laterTaskRow := &AgentLog{ID: "task-scoped-later", Timestamp: now.Add(2 * time.Second), RecordKind: "event", Status: "success", AgentName: "alpha", RequestID: "request-3", TaskID: &taskID}
	isolatedAgent := &AgentLog{ID: "other-agent", Timestamp: now.Add(2 * time.Second), RecordKind: "event", Status: "success", AgentName: "beta", RequestID: "request-1", TaskID: &taskID}
	preserved := &AgentLog{ID: "preserved", Timestamp: now.Add(2 * time.Second), RecordKind: "event", Status: "success", AgentName: "alpha", RequestID: "request-1", TaskID: &otherTaskID, ContextID: &otherContextID}
	require.NoError(t, agentLogsCreateError(store.BatchCreateAgentLogsIfNotExists(ctx, []*AgentLog{laterTaskRow, isolatedAgent, preserved})))
	require.NoError(t, store.ReconcileAgentCorrelation(ctx, []*AgentLog{laterTaskRow, isolatedAgent, preserved}))
	require.NoError(t, store.ReconcileAgentCorrelation(ctx, []*AgentLog{laterTaskRow, isolatedAgent, preserved}))

	found, err = store.FindAgentLog(ctx, laterTaskRow.ID)
	require.NoError(t, err)
	require.Equal(t, contextID, *found.ContextID)
	found, err = store.FindAgentLog(ctx, isolatedAgent.ID)
	require.NoError(t, err)
	require.Nil(t, found.ContextID)
	found, err = store.FindAgentLog(ctx, preserved.ID)
	require.NoError(t, err)
	require.Equal(t, otherTaskID, *found.TaskID)
	require.Equal(t, otherContextID, *found.ContextID)
}

func TestPostgresReconcileAgentCorrelation(t *testing.T) {
	db := trySetupPostgresDB(t)
	if db == nil {
		t.Skip("Postgres not available")
	}
	require.NoError(t, db.AutoMigrate(&AgentLog{}))
	store := &RDBLogStore{db: db, logger: bifrost.NewDefaultLogger(schemas.LogLevelInfo)}
	ctx := context.Background()
	now := time.Now().UTC()
	taskID, contextID := "task-postgres", "context-postgres"
	request := &AgentLog{ID: "pg-a2a-request", Timestamp: now, RecordKind: "request", Status: "success", AgentName: "fixture", RequestID: "pg-request-id"}
	event := &AgentLog{ID: "pg-a2a-event", Timestamp: now.Add(time.Millisecond), RecordKind: "event", Status: "success", AgentName: "fixture", RequestID: "pg-request-id", TaskID: &taskID, ContextID: &contextID}
	t.Cleanup(func() { _ = db.Where("id IN ?", []string{request.ID, event.ID}).Delete(&AgentLog{}).Error })

	require.NoError(t, agentLogsCreateError(store.BatchCreateAgentLogsIfNotExists(ctx, []*AgentLog{request, event})))
	require.NoError(t, store.ReconcileAgentCorrelation(ctx, []*AgentLog{request, event}))
	found, err := store.FindAgentLog(ctx, request.ID)
	require.NoError(t, err)
	require.Equal(t, taskID, *found.TaskID)
	require.Equal(t, contextID, *found.ContextID)
}

func TestDeleteAgentLogsCascadesToSharedRequestIDs(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&AgentLog{}))
	store := &RDBLogStore{db: db, logger: bifrost.NewDefaultLogger(schemas.LogLevelInfo)}
	ctx := context.Background()
	now := time.Now().UTC()
	rows := []*AgentLog{
		{ID: "request", Timestamp: now, RecordKind: "request", Status: "success", AgentName: "fixture", RequestID: "request-1"},
		{ID: "event-1", Timestamp: now, RecordKind: "event", Status: "success", AgentName: "fixture", RequestID: "request-1"},
		{ID: "event-2", Timestamp: now, RecordKind: "event", Status: "success", AgentName: "fixture", RequestID: "request-1"},
		{ID: "unrelated", Timestamp: now, RecordKind: "request", Status: "success", AgentName: "fixture", RequestID: "request-2"},
	}
	require.NoError(t, agentLogsCreateError(store.BatchCreateAgentLogsIfNotExists(ctx, rows)))

	require.NoError(t, store.DeleteAgentLogs(ctx, nil))
	require.NoError(t, store.DeleteAgentLogs(ctx, []string{"request"}))

	var remaining []string
	require.NoError(t, db.Model(&AgentLog{}).Order("id").Pluck("id", &remaining).Error)
	require.Equal(t, []string{"unrelated"}, remaining)
}

func TestDeleteAgentLogsAppliesScopeToRequestedAndCorrelatedRows(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&AgentLog{}))
	store := &RDBLogStore{db: db, logger: bifrost.NewDefaultLogger(schemas.LogLevelInfo)}
	now := time.Now().UTC()
	alice, bob := "alice", "bob"
	rows := []*AgentLog{
		{ID: "alice-request", Timestamp: now, RecordKind: "request", Status: "success", AgentName: "fixture", RequestID: "shared-request", UserID: &alice},
		{ID: "alice-event", Timestamp: now, RecordKind: "event", Status: "success", AgentName: "fixture", RequestID: "shared-request", UserID: &alice},
		{ID: "bob-shared-event", Timestamp: now, RecordKind: "event", Status: "success", AgentName: "fixture", RequestID: "shared-request", UserID: &bob},
		{ID: "bob-request", Timestamp: now, RecordKind: "request", Status: "success", AgentName: "fixture", RequestID: "bob-request", UserID: &bob},
	}
	require.NoError(t, agentLogsCreateError(store.BatchCreateAgentLogsIfNotExists(context.Background(), rows)))
	ctx := queryscope.WithQueryScope(context.Background(), func(db *gorm.DB) *gorm.DB {
		return db.Where("user_id = ?", alice)
	})

	require.NoError(t, store.DeleteAgentLogs(ctx, []string{"alice-request", "bob-request"}))

	var remaining []string
	require.NoError(t, db.Model(&AgentLog{}).Order("id").Pluck("id", &remaining).Error)
	require.Equal(t, []string{"bob-request", "bob-shared-event"}, remaining)
}

func TestListAgentLogHistoryOrdersNullLatencyLast(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&AgentLog{}))
	store := &RDBLogStore{db: db, logger: bifrost.NewDefaultLogger(schemas.LogLevelInfo)}
	ctx := context.Background()
	now := time.Now().UTC()
	fast, slow := 10.0, 20.0
	require.NoError(t, agentLogsCreateError(store.BatchCreateAgentLogsIfNotExists(ctx, []*AgentLog{
		{ID: "no-latency", Timestamp: now, RecordKind: "request", Status: "error", AgentName: "fixture", RequestID: "no-latency"},
		{ID: "fast", Timestamp: now, RecordKind: "request", Status: "success", AgentName: "fixture", RequestID: "fast", Latency: &fast},
		{ID: "slow", Timestamp: now, RecordKind: "request", Status: "success", AgentName: "fixture", RequestID: "slow", Latency: &slow},
	})))

	for _, test := range []struct {
		order string
		want  []string
	}{
		{order: "asc", want: []string{"fast", "slow", "no-latency"}},
		{order: "desc", want: []string{"slow", "fast", "no-latency"}},
	} {
		t.Run(test.order, func(t *testing.T) {
			result, err := store.ListAgentLogHistory(ctx, AgentLogHistoryFilter{}, PaginationOptions{Limit: 10, SortBy: "latency", Order: test.order})
			require.NoError(t, err)
			require.Equal(t, test.want, []string{result.Logs[0].ID, result.Logs[1].ID, result.Logs[2].ID})
		})
	}
}

func TestListAgentLogHistoryFiltersWithoutSelectingPayloads(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&AgentLog{}))
	store := &RDBLogStore{db: db, logger: bifrost.NewDefaultLogger(schemas.LogLevelInfo)}
	ctx := context.Background()
	now := time.Now().UTC()
	taskID, otherTaskID, contextID := "task-1", "task-2", "context-1"
	body := `{"large":"payload"}`
	requestBody := `{"message":{"parts":[{"text":"` + strings.Repeat("x", maxA2APayloadPreviewRunes+50) + `"}]}}`
	rows := []*AgentLog{
		{ID: "request", Timestamp: now.Add(3 * time.Second), RecordKind: "request", Operation: "message/send", Status: "success", AgentName: "fixture", RequestID: "request-3", TaskID: &taskID, ContextID: &contextID, RequestBody: &requestBody},
		{ID: "event-2", Timestamp: now.Add(time.Second), RecordKind: "event", Operation: "tasks/subscribe", Status: "success", AgentName: "fixture", RequestID: "request-1", TaskID: &taskID, ContextID: &contextID, EventBody: &body},
		{ID: "event-1", Timestamp: now, RecordKind: "event", Operation: "message/stream", Status: "success", AgentName: "fixture", RequestID: "request-1", TaskID: &taskID, ContextID: &contextID, EventBody: &body},
		{ID: "other", Timestamp: now.Add(2 * time.Second), RecordKind: "event", Operation: "message/stream", Status: "success", AgentName: "fixture", RequestID: "request-2", TaskID: &otherTaskID, EventBody: &body},
	}
	require.NoError(t, agentLogsCreateError(store.BatchCreateAgentLogsIfNotExists(ctx, rows)))

	result, err := store.ListAgentLogHistory(ctx, AgentLogHistoryFilter{TaskID: taskID}, PaginationOptions{Limit: 1, Order: "desc"})
	require.NoError(t, err)
	require.EqualValues(t, 3, result.Pagination.TotalCount)
	require.Len(t, result.Logs, 1)
	require.Equal(t, "request", result.Logs[0].ID)
	require.NotNil(t, result.Logs[0].Input)
	require.Equal(t, string([]rune(requestBody)[:maxA2APayloadPreviewRunes]), *result.Logs[0].Input)

	detail, err := store.FindAgentLog(ctx, "request")
	require.NoError(t, err)
	require.NotNil(t, detail.RequestBody)
	require.Equal(t, requestBody, *detail.RequestBody)

	result, err = store.ListAgentLogHistory(ctx, AgentLogHistoryFilter{AgentName: []string{"fixture"}, Operation: []string{"tasks/subscribe"}, RequestID: "request-1", TaskID: taskID, ContextID: contextID, StartTime: &now, EndTime: ptrTime(now.Add(time.Second))}, PaginationOptions{Limit: 10, Order: "desc"})
	require.NoError(t, err)
	require.Len(t, result.Logs, 1)
	require.Equal(t, "event-2", result.Logs[0].ID)

	tie := now.Add(3 * time.Second)
	require.NoError(t, agentLogsCreateError(store.BatchCreateAgentLogsIfNotExists(ctx, []*AgentLog{
		{ID: "tie-a", Timestamp: tie, RecordKind: "event", Status: "success", AgentName: "fixture", RequestID: "tie-a", TaskID: &taskID},
		{ID: "tie-b", Timestamp: tie, RecordKind: "event", Status: "success", AgentName: "fixture", RequestID: "tie-b", TaskID: &taskID},
	})))
	result, err = store.ListAgentLogHistory(ctx, AgentLogHistoryFilter{TaskID: taskID}, PaginationOptions{Limit: 2, Order: "desc"})
	require.NoError(t, err)
	require.Equal(t, []string{"tie-b", "tie-a"}, []string{result.Logs[0].ID, result.Logs[1].ID})
	_, err = store.ListAgentLogHistory(ctx, AgentLogHistoryFilter{TaskID: taskID}, PaginationOptions{})
	require.Error(t, err)
	_, err = store.ListAgentLogHistory(ctx, AgentLogHistoryFilter{TaskID: taskID}, PaginationOptions{Limit: AgentLogHistoryMaxLimit + 1})
	require.Error(t, err)
}

func ptrTime(value time.Time) *time.Time { return &value }

// TestAgentLogLatencyBreakdownRoundTrip verifies that the upstream/overhead
// latency columns and the JSON overhead breakdown survive persistence
// (BeforeCreate/AfterFind) and appear in both the list projection and the
// detail contract, mirroring the logs table.
func TestAgentLogLatencyBreakdownRoundTrip(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&AgentLog{}))
	store := &RDBLogStore{db: db, logger: bifrost.NewDefaultLogger(schemas.LogLevelInfo)}
	ctx := context.Background()
	now := time.Now().UTC()
	upstream, overheadTotal, latency := 10.0, 5.0, 15.0
	breakdown := []OverheadBucket{
		{Name: "plugin.logging", DurationUs: 2000},
		{Name: "scheduling", DurationUs: 3000},
	}
	require.NoError(t, agentLogsCreateError(store.BatchCreateAgentLogsIfNotExists(ctx, []*AgentLog{{
		ID: "latency-row", Timestamp: now, RecordKind: "request", Operation: "message/send", Status: "success",
		AgentName: "fixture", RequestID: "request-latency",
		Latency: &latency, UpstreamLatency: &upstream, OverheadLatency: &overheadTotal,
		OverheadBreakdownParsed: breakdown,
	}})))

	found, err := store.FindAgentLog(ctx, "latency-row")
	require.NoError(t, err)
	require.Equal(t, upstream, *found.UpstreamLatency)
	require.Equal(t, overheadTotal, *found.OverheadLatency)
	require.Equal(t, breakdown, found.OverheadBreakdownParsed)

	detail := NewAgentLogDetail(found)
	require.Equal(t, upstream, *detail.UpstreamLatency)
	require.Equal(t, overheadTotal, *detail.OverheadLatency)
	require.Equal(t, breakdown, detail.OverheadBreakdown)

	result, err := store.ListAgentLogHistory(ctx, AgentLogHistoryFilter{RequestID: "request-latency"}, PaginationOptions{Limit: 10, Order: "desc"})
	require.NoError(t, err)
	require.Len(t, result.Logs, 1)
	require.Equal(t, upstream, *result.Logs[0].UpstreamLatency)
	require.Equal(t, overheadTotal, *result.Logs[0].OverheadLatency)
	require.Equal(t, breakdown, result.Logs[0].OverheadBreakdown)
}

func TestListAgentLogOperationsGroupsScopedEventsOutsideRequestWindow(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&AgentLog{}))
	store := &RDBLogStore{db: db, logger: bifrost.NewDefaultLogger(schemas.LogLevelInfo)}
	now := time.Now().UTC()
	alice, bob := "alice", "bob"
	requestBody := `{"message":{"parts":[{"text":"question"}]}}`
	earlyEventBody := `{"artifactUpdate":{"artifact":{"artifactId":"answer","parts":[{"text":"first"}]}}}`
	lateEventBody := `{"artifactUpdate":{"artifact":{"artifactId":"answer","parts":[{"text":"second"}]},"append":true}}`
	foreignEventBody := `{"artifactUpdate":{"artifact":{"artifactId":"answer","parts":[{"text":"secret"}]}}}`
	require.NoError(t, agentLogsCreateError(store.BatchCreateAgentLogsIfNotExists(context.Background(), []*AgentLog{
		{ID: "alice-request", Timestamp: now, RecordKind: "request", Operation: "SendStreamingMessage", Status: "success", AgentName: "fixture", RequestID: "request-1", UserID: &alice, RequestBody: &requestBody},
		{ID: "alice-early", Timestamp: now.Add(-time.Minute), RecordKind: "event", Operation: "SendStreamingMessage", Status: "success", AgentName: "fixture", RequestID: "request-1", UserID: &alice, EventBody: &earlyEventBody},
		{ID: "alice-late", Timestamp: now.Add(time.Minute), RecordKind: "event", Operation: "SendStreamingMessage", Status: "success", AgentName: "fixture", RequestID: "request-1", UserID: &alice, EventBody: &lateEventBody},
		{ID: "bob-event", Timestamp: now.Add(time.Second), RecordKind: "event", Operation: "SendStreamingMessage", Status: "success", AgentName: "fixture", RequestID: "request-1", UserID: &bob, EventBody: &foreignEventBody},
		{ID: "unselected-request", Timestamp: now.Add(-time.Hour), RecordKind: "request", Operation: "SendMessage", Status: "success", AgentName: "fixture", RequestID: "request-2", UserID: &alice},
		{ID: "unselected-event", Timestamp: now.Add(time.Second), RecordKind: "event", Operation: "SendMessage", Status: "success", AgentName: "fixture", RequestID: "request-2", UserID: &alice, EventBody: &lateEventBody},
	})))
	ctx := queryscope.WithQueryScope(context.Background(), func(db *gorm.DB) *gorm.DB {
		return db.Where("user_id = ?", alice)
	})
	start, end := now.Add(-time.Second), now.Add(time.Second)

	result, err := store.ListAgentLogOperations(ctx, AgentLogHistoryFilter{StartTime: &start, EndTime: &end}, PaginationOptions{Limit: 1, Order: "desc"})
	require.NoError(t, err)
	require.EqualValues(t, 1, result.Pagination.TotalCount)
	require.Len(t, result.Logs, 1)
	require.Equal(t, "alice-request", result.Logs[0].ID)
	require.Equal(t, []string{"alice-early", "alice-late"}, []string{result.Logs[0].Events[0].ID, result.Logs[0].Events[1].ID})
	require.Equal(t, earlyEventBody, *result.Logs[0].Events[0].EventBody)
}

func TestListAgentLogHistoryAppliesScopeBeforeCountAndPagination(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&AgentLog{}))
	store := &RDBLogStore{db: db, logger: bifrost.NewDefaultLogger(schemas.LogLevelInfo)}
	now := time.Now().UTC()
	taskID := "shared-task"
	alice, bob := "alice", "bob"
	require.NoError(t, agentLogsCreateError(store.BatchCreateAgentLogsIfNotExists(context.Background(), []*AgentLog{
		{ID: "alice-1", Timestamp: now, RecordKind: "event", Status: "success", AgentName: "fixture", RequestID: "request-1", TaskID: &taskID, UserID: &alice},
		{ID: "bob-1", Timestamp: now.Add(time.Second), RecordKind: "event", Status: "success", AgentName: "fixture", RequestID: "request-2", TaskID: &taskID, UserID: &bob},
		{ID: "alice-2", Timestamp: now.Add(2 * time.Second), RecordKind: "event", Status: "success", AgentName: "fixture", RequestID: "request-3", TaskID: &taskID, UserID: &alice},
	})))
	ctx := queryscope.WithQueryScope(context.Background(), func(db *gorm.DB) *gorm.DB {
		return db.Where("user_id = ?", alice)
	})

	result, err := store.ListAgentLogHistory(ctx, AgentLogHistoryFilter{TaskID: taskID}, PaginationOptions{Limit: 1, Offset: 1, Order: "desc"})
	require.NoError(t, err)
	require.EqualValues(t, 2, result.Pagination.TotalCount)
	require.Len(t, result.Logs, 1)
	require.Equal(t, "alice-1", result.Logs[0].ID)

	_, err = store.FindAgentLog(ctx, "bob-1")
	require.ErrorIs(t, err, ErrNotFound)
	entry, err := store.FindAgentLog(ctx, "alice-1")
	require.NoError(t, err)
	require.Equal(t, "alice-1", entry.ID)
}

func TestGetAgentFilterDataUsesMembershipFanout(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&AgentLog{}))
	store := &RDBLogStore{db: db, logger: bifrost.NewDefaultLogger(schemas.LogLevelInfo)}
	userID, userName := "user-a", "Agent User"
	entry := &AgentLog{
		ID: "filter-data", Timestamp: time.Now().UTC(), RecordKind: "request", Status: "success", AgentName: "fixture", RequestID: "request-filter-data",
		UserID: &userID, UserName: &userName,
		TeamIDsParsed: []string{"team-b", "team-a"}, TeamNamesParsed: []string{"Beta", "Alpha"},
		CustomerIDsParsed: []string{"customer-b", "customer-a"}, CustomerNamesParsed: []string{"Bravo", "Acme"},
		BusinessUnitIDsParsed: []string{"bu-b", "bu-a"}, BusinessUnitNamesParsed: []string{"Build", "Advisory"},
	}
	require.NoError(t, entry.SerializeFields())
	require.NoError(t, agentLogsCreateError(store.BatchCreateAgentLogsIfNotExists(context.Background(), []*AgentLog{entry})))

	data, err := store.GetAgentFilterData(context.Background(), nil, 100, "")
	require.NoError(t, err)
	require.Equal(t, []AgentFilterKeyPair{{ID: userID, Name: userName}}, data.Users)
	require.Equal(t, []AgentFilterKeyPair{{ID: "team-a", Name: "Alpha"}, {ID: "team-b", Name: "Beta"}}, data.Teams)
	require.Equal(t, []AgentFilterKeyPair{{ID: "customer-a", Name: "Acme"}, {ID: "customer-b", Name: "Bravo"}}, data.Customers)
	require.Equal(t, []AgentFilterKeyPair{{ID: "bu-a", Name: "Advisory"}, {ID: "bu-b", Name: "Build"}}, data.BusinessUnits)

	data, err = store.GetAgentFilterData(context.Background(), []string{"teams"}, 1, "et")
	require.NoError(t, err)
	require.Equal(t, []AgentFilterKeyPair{{ID: "team-b", Name: "Beta"}}, data.Teams)

	hiddenUserID := "user-b"
	hidden := &AgentLog{
		ID: "hidden-filter-data", Timestamp: time.Now().UTC(), RecordKind: "request", Status: "success", AgentName: "fixture", RequestID: "hidden-request-filter-data",
		UserID: &hiddenUserID, TeamIDsParsed: []string{"team-hidden"}, TeamNamesParsed: []string{"Hidden"},
	}
	require.NoError(t, hidden.SerializeFields())
	require.NoError(t, agentLogsCreateError(store.BatchCreateAgentLogsIfNotExists(context.Background(), []*AgentLog{hidden})))
	scoped := queryscope.WithQueryScope(context.Background(), func(db *gorm.DB) *gorm.DB {
		return db.Where("user_id = ?", userID)
	})
	data, err = store.GetAgentFilterData(scoped, []string{"teams"}, 100, "")
	require.NoError(t, err)
	require.Equal(t, []AgentFilterKeyPair{{ID: "team-a", Name: "Alpha"}, {ID: "team-b", Name: "Beta"}}, data.Teams)
}

// TestGetAgentFilterDataAppliesDimensionCeiling verifies the agent filter
// dropdowns list only the organisations the caller may be shown, like the LLM
// log dropdowns: a readable row still never names a team, customer, business
// unit or user outside the ceiling.
func TestGetAgentFilterDataAppliesDimensionCeiling(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&AgentLog{}))
	store := &RDBLogStore{db: db, logger: bifrost.NewDefaultLogger(schemas.LogLevelInfo)}
	userID, userName := "user-a", "Agent User"
	otherID, otherName := "user-b", "Other User"
	entries := []*AgentLog{
		{
			ID: "ceiling-a", Timestamp: time.Now().UTC(), RecordKind: "request", Status: "success", AgentName: "fixture", RequestID: "request-ceiling-a",
			UserID: &userID, UserName: &userName,
			TeamIDsParsed: []string{"team-a", "team-hidden"}, TeamNamesParsed: []string{"Alpha", "Hidden"},
			CustomerIDsParsed: []string{"customer-a", "customer-hidden"}, CustomerNamesParsed: []string{"Acme", "Hidden Co"},
			BusinessUnitIDsParsed: []string{"bu-hidden"}, BusinessUnitNamesParsed: []string{"Hidden BU"},
		},
		{
			ID: "ceiling-b", Timestamp: time.Now().UTC(), RecordKind: "request", Status: "success", AgentName: "fixture", RequestID: "request-ceiling-b",
			UserID: &otherID, UserName: &otherName,
		},
	}
	for _, entry := range entries {
		require.NoError(t, entry.SerializeFields())
	}
	require.NoError(t, agentLogsCreateError(store.BatchCreateAgentLogsIfNotExists(context.Background(), entries)))

	allowed := map[string][]string{"team_id": {"team-a"}, "customer_id": {"customer-a"}, "business_unit_id": {}, "user_id": {userID}}
	ctx := queryscope.WithDimensionScope(context.Background(), func(col string) ([]string, bool) {
		ids, ok := allowed[col]
		return ids, ok
	})
	data, err := store.GetAgentFilterData(ctx, nil, 100, "")
	require.NoError(t, err)
	require.Equal(t, []AgentFilterKeyPair{{ID: "team-a", Name: "Alpha"}}, data.Teams)
	require.Equal(t, []AgentFilterKeyPair{{ID: "customer-a", Name: "Acme"}}, data.Customers)
	require.Empty(t, data.BusinessUnits)
	require.Equal(t, []AgentFilterKeyPair{{ID: userID, Name: userName}}, data.Users)
}

// TestGetAgentFilterDataBindsLargeCeilingAsOneParameter verifies a ceiling
// longer than SQLite's 32,766-parameter limit still loads the dropdowns: the
// allowed ids must bind as one parameter, not one per id.
func TestGetAgentFilterDataBindsLargeCeilingAsOneParameter(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&AgentLog{}))
	store := &RDBLogStore{db: db, logger: bifrost.NewDefaultLogger(schemas.LogLevelInfo)}
	userID, userName := "user-a", "Agent User"
	entry := &AgentLog{
		ID: "large-ceiling", Timestamp: time.Now().UTC(), RecordKind: "request", Status: "success", AgentName: "fixture", RequestID: "request-large-ceiling",
		UserID: &userID, UserName: &userName,
		TeamIDsParsed: []string{"team-a"}, TeamNamesParsed: []string{"Alpha"},
	}
	require.NoError(t, entry.SerializeFields())
	require.NoError(t, agentLogsCreateError(store.BatchCreateAgentLogsIfNotExists(context.Background(), []*AgentLog{entry})))

	allowed := make([]string, 0, 40000)
	for i := 0; i < 40000; i++ {
		allowed = append(allowed, fmt.Sprintf("user-%d", i))
	}
	allowed = append(allowed, userID)
	ctx := queryscope.WithDimensionScope(context.Background(), func(col string) ([]string, bool) {
		if col == "user_id" {
			return allowed, true
		}
		return nil, false
	})
	data, err := store.GetAgentFilterData(ctx, []string{"users"}, 100, "")
	require.NoError(t, err)
	require.Equal(t, []AgentFilterKeyPair{{ID: userID, Name: userName}}, data.Users)
}

func TestA2AAttributionFiltersApplyToRowsStatsAndHistogram(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&AgentLog{}))
	store := &RDBLogStore{db: db, logger: bifrost.NewDefaultLogger(schemas.LogLevelInfo)}
	assertA2AAttributionFilters(t, store, store.BatchCreateAgentLogsIfNotExists)
}

// assertA2AAttributionFilters pins every attribution filter on agent logs,
// including ids held only in the team, customer and business-unit arrays.
// create is the store's own writer, since ClickHouse writes agent logs its own
// way.
func assertA2AAttributionFilters(t *testing.T, store *RDBLogStore, create func(context.Context, []*AgentLog) ([]string, error)) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	a, b := "a", "b"
	require.NoError(t, agentLogsCreateError(create(ctx, []*AgentLog{
		{
			ID: "a", Timestamp: now, RecordKind: "request", Operation: "message/send", Status: "success", AgentName: "fixture", RequestID: "request-a",
			UserID: &a, VirtualKeyID: &a, TeamID: &a, CustomerID: &a, BusinessUnitID: &a, ProjectID: &a,
			TeamIDsParsed: []string{"team-shared", a}, CustomerIDsParsed: []string{"customer-shared", a}, BusinessUnitIDsParsed: []string{"bu-shared", a},
		},
		{
			ID: "b", Timestamp: now.Add(time.Second), RecordKind: "request", Operation: "message/send", Status: "error", AgentName: "fixture", RequestID: "request-b",
			UserID: &b, VirtualKeyID: &b, TeamID: &b, CustomerID: &b, BusinessUnitID: &b, ProjectID: &b,
		},
	})))
	end := now.Add(time.Minute)

	filters := []AgentLogHistoryFilter{
		{UserID: []string{a}},
		{VirtualKeyID: []string{a}},
		{TeamID: []string{a}},
		{TeamID: []string{"team-shared"}},
		{CustomerID: []string{a}},
		{CustomerID: []string{"customer-shared"}},
		{BusinessUnitID: []string{a}},
		{BusinessUnitID: []string{"bu-shared"}},
		// Two ids, one matching: ClickHouse bound a list inside hasAny's
		// brackets as one tuple, so these failed there.
		{TeamID: []string{"team-shared", "team-none"}},
		{CustomerID: []string{"customer-shared", "customer-none"}},
		{BusinessUnitID: []string{"bu-shared", "bu-none"}},
		{ProjectID: []string{a}},
	}
	for _, filter := range filters {
		filter.StartTime, filter.EndTime = &now, &end
		result, err := store.ListAgentLogHistory(ctx, filter, PaginationOptions{Limit: 10, SortBy: "timestamp", Order: "desc"})
		require.NoError(t, err)
		require.Len(t, result.Logs, 1)
		require.Equal(t, "a", result.Logs[0].ID)

		stats, err := store.GetAgentLogStats(ctx, filter)
		require.NoError(t, err)
		require.EqualValues(t, 1, stats.TotalEntries)
		require.EqualValues(t, 1, stats.SuccessCount)

		histogram, err := store.GetAgentHistogram(ctx, filter, 60)
		require.NoError(t, err)
		var histogramCount int64
		for _, bucket := range histogram.Buckets {
			histogramCount += bucket.Count
		}
		require.EqualValues(t, 1, histogramCount)
	}
}

// TestMCPAttributionFiltersApplyToRowsAndStats checks each stored scope filters both records and aggregates.
func TestMCPAttributionFiltersApplyToRowsAndStats(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&MCPToolLog{}))
	store := &RDBLogStore{db: db, logger: bifrost.NewDefaultLogger(schemas.LogLevelInfo)}
	ctx := context.Background()
	a, b := "a", "b"
	now := time.Now()
	require.NoError(t, db.Create(&MCPToolLog{ID: a, ToolName: "Read", Timestamp: now, Status: "success", UserID: &a, TeamID: &a, CustomerID: &a, BusinessUnitID: &a, ProjectID: &a, DeviceID: &a}).Error)
	require.NoError(t, db.Create(&MCPToolLog{ID: b, ToolName: "Read", Timestamp: now, Status: "error", UserID: &b, TeamID: &b, CustomerID: &b, BusinessUnitID: &b, ProjectID: &b, DeviceID: &b}).Error)
	for _, filters := range []MCPToolLogSearchFilters{{UserIDs: []string{a}}, {TeamIDs: []string{a}}, {CustomerIDs: []string{a}}, {BusinessUnitIDs: []string{a}}, {ProjectIDs: []string{a}}, {DeviceIDs: []string{a}}} {
		result, err := store.SearchMCPToolLogs(ctx, filters, PaginationOptions{Limit: 10, SortBy: "timestamp", Order: "desc"})
		require.NoError(t, err)
		require.Len(t, result.Logs, 1)
		require.Equal(t, a, result.Logs[0].ID)
		stats, err := store.GetMCPToolLogStats(ctx, filters)
		require.NoError(t, err)
		require.EqualValues(t, 1, stats.TotalExecutions)
		require.EqualValues(t, 100, stats.SuccessRate)
	}
}
