package logstore

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRelayLogsRoundTrip(t *testing.T) {
	ctx := context.Background()
	store, err := newSqliteLogStore(ctx, &SQLiteConfig{Path: filepath.Join(t.TempDir(), "pte.db")}, testLogger{})
	require.NoError(t, err)

	now := time.Now().UTC().Truncate(time.Second)
	vk := "vk-1"
	entries := []*RelayLog{
		{ID: "a", RequestID: "a", Timestamp: now.Add(-2 * time.Minute), RelayName: "tavily", Method: "POST", Path: "/search", RoutePath: "/search", Status: RelayLogStatusSuccess, StatusCode: 200, Billable: true, Cost: schemas.Ptr(0.033), BilledUnits: schemas.Ptr(4.0), Latency: schemas.Ptr(120.0), UpstreamLatency: schemas.Ptr(100.0), OverheadLatency: schemas.Ptr(20.0), OverheadBreakdownParsed: []OverheadBucket{{Name: "plugin.governance", DurationUs: 1500}}, UserAgent: schemas.Ptr("claude-code/1.0.0"), App: schemas.Ptr("claude-code"), VirtualKeyID: &vk, VirtualKeyName: schemas.Ptr("prod"), TeamID: schemas.Ptr("t1"), TeamName: schemas.Ptr("Team One"), TeamIDsParsed: []string{"t1", "t2"}, TeamNamesParsed: []string{"Team One", "Team Two"}, BudgetIDsParsed: []string{"b1"}, RequestHeadersParsed: map[string]string{"content-type": "application/json"}},
		{ID: "b", RequestID: "b", Timestamp: now.Add(-time.Minute), RelayName: "tavily", Method: "GET", Path: "/usage", Status: RelayLogStatusSuccess, StatusCode: 200, Latency: schemas.Ptr(40.0)},
		{ID: "c", RequestID: "c", Timestamp: now, RelayName: "textract", Method: "POST", Path: "/", Status: RelayLogStatusError, StatusCode: 500, Billable: true, ErrorDetailsParsed: &schemas.BifrostError{Error: &schemas.ErrorField{Message: "boom"}}},
		{ID: "c-job", RequestID: "c", Kind: RelayLogKindJob, Timestamp: now, RelayName: "textract", Status: RelayLogStatusSuccess, Billable: true, Cost: schemas.Ptr(0.15), JobID: schemas.Ptr("j-1"), ParentLogID: schemas.Ptr("c")},
	}
	inserted, err := store.BatchCreateRelayLogsIfNotExists(ctx, entries)
	require.NoError(t, err)
	assert.Len(t, inserted, 4)
	inserted, err = store.BatchCreateRelayLogsIfNotExists(ctx, entries[:1])
	require.NoError(t, err)
	assert.Empty(t, inserted, "an id already stored is not inserted twice")

	got, err := store.FindRelayLog(ctx, "a")
	require.NoError(t, err)
	assert.Equal(t, []string{"b1"}, got.BudgetIDsParsed)
	assert.Equal(t, "application/json", got.RequestHeadersParsed["content-type"])
	assert.Equal(t, 100.0, *got.UpstreamLatency)
	assert.Equal(t, 20.0, *got.OverheadLatency)
	assert.Equal(t, []OverheadBucket{{Name: "plugin.governance", DurationUs: 1500}}, got.OverheadBreakdownParsed, "the breakdown round-trips through its stored column")
	assert.Equal(t, "claude-code", *got.App)
	assert.Equal(t, []string{"t1", "t2"}, got.TeamIDsParsed, "every team the call settled under round-trips")
	assert.Equal(t, []string{"Team One", "Team Two"}, got.TeamNamesParsed)
	got, err = store.FindRelayLog(ctx, "c")
	require.NoError(t, err)
	require.NotNil(t, got.ErrorDetailsParsed)
	assert.Equal(t, "boom", got.ErrorDetailsParsed.Error.Message)
	_, err = store.FindRelayLog(ctx, "missing")
	require.ErrorIs(t, err, ErrNotFound)

	page, err := store.SearchRelayLogs(ctx, RelayLogFilters{EndpointNames: []string{"tavily"}}, PaginationOptions{Limit: 10})
	require.NoError(t, err)
	require.Len(t, page.Logs, 2)
	assert.Equal(t, "b", page.Logs[0].ID, "newest first")
	assert.EqualValues(t, 2, page.Pagination.TotalCount)

	billable := true
	page, err = store.SearchRelayLogs(ctx, RelayLogFilters{Billable: &billable, VirtualKeyIDs: []string{vk}}, PaginationOptions{Limit: 10})
	require.NoError(t, err)
	require.Len(t, page.Logs, 1)
	page, err = store.SearchRelayLogs(ctx, RelayLogFilters{Search: "usa"}, PaginationOptions{Limit: 10})
	require.NoError(t, err)
	require.Len(t, page.Logs, 1, "search matches the path")
	page, err = store.SearchRelayLogs(ctx, RelayLogFilters{TeamIDs: []string{"t2"}}, PaginationOptions{Limit: 10})
	require.NoError(t, err)
	require.Len(t, page.Logs, 1, "a team the call settled under beyond the first still matches")
	assert.Equal(t, "a", page.Logs[0].ID)
	page, err = store.SearchRelayLogs(ctx, RelayLogFilters{Apps: []string{"claude-code"}}, PaginationOptions{Limit: 10})
	require.NoError(t, err)
	require.Len(t, page.Logs, 1, "rows filter by the detected app")

	filterData, err := store.GetRelayFilterData(ctx, []string{"teams", "apps"}, 10, "two")
	require.NoError(t, err)
	assert.Nil(t, filterData.VirtualKeys, "a dimension not asked for is absent")
	assert.Equal(t, []RelayFilterOption{{ID: "t2", Name: "Team Two"}}, filterData.Teams, "a team the call settled under beyond the first is a choice, narrowed by name")
	filterData, err = store.GetRelayFilterData(ctx, []string{"teams"}, 10, "")
	require.NoError(t, err)
	assert.ElementsMatch(t, []RelayFilterOption{{ID: "t1", Name: "Team One"}, {ID: "t2", Name: "Team Two"}}, filterData.Teams)
	filterData, err = store.GetRelayFilterData(ctx, nil, 10, "")
	require.NoError(t, err)
	assert.Equal(t, []string{"claude-code"}, filterData.Apps)
	assert.Equal(t, []RelayFilterOption{{ID: vk, Name: "prod"}}, filterData.VirtualKeys)

	stats, err := store.GetRelayLogStats(ctx, RelayLogFilters{})
	require.NoError(t, err)
	assert.EqualValues(t, 4, stats.TotalRequests)
	assert.EqualValues(t, 1, stats.ErrorCount)
	assert.InDelta(t, 0.183, stats.TotalCost, 1e-9)
	assert.InDelta(t, 4, stats.TotalUnits, 1e-9)

	start, end := now.Add(-5*time.Minute), now.Add(time.Minute)
	hist, err := store.GetRelayHistogram(ctx, RelayLogFilters{StartTime: &start, EndTime: &end}, 60)
	require.NoError(t, err)
	var count int64
	var cost float64
	for _, b := range hist.Buckets {
		count += b.Count
		cost += b.Cost
	}
	assert.EqualValues(t, 4, count)
	assert.InDelta(t, 0.183, cost, 1e-9)

	top, err := store.GetRelayTopEndpoints(ctx, RelayLogFilters{}, 5)
	require.NoError(t, err)
	require.Len(t, top.Endpoints, 2)

	filterData, err = store.GetRelayFilterData(ctx, nil, 10, "")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"tavily", "textract"}, filterData.EndpointNames)
	assert.ElementsMatch(t, []string{"GET", "POST"}, filterData.Methods)
	require.Len(t, filterData.VirtualKeys, 1)
	assert.Equal(t, "prod", filterData.VirtualKeys[0].Name)

	require.NoError(t, store.DeleteRelayLogs(ctx, []string{"c"}))
	_, err = store.FindRelayLog(ctx, "c-job")
	require.ErrorIs(t, err, ErrNotFound, "deleting a start call deletes the job row that settles it")

	deleted, err := store.DeleteRelayLogsBatch(ctx, now.Add(-90*time.Second), 10)
	require.NoError(t, err)
	assert.EqualValues(t, 1, deleted, "retention removes rows older than the cutoff")
	has, err := store.HasRelayLogs(ctx)
	require.NoError(t, err)
	assert.True(t, has)
}

// TestRelayLogsMigrationIsIdempotent pins add_relay_logs_table: it creates the table and every index its
// tags declare, a rerun is a no-op, and its index names are its own, so a database still holding an
// earlier table's indexes does not refuse it.
func TestRelayLogsMigrationIsIdempotent(t *testing.T) {
	ctx := context.Background()
	store, err := newSqliteLogStore(ctx, &SQLiteConfig{Path: filepath.Join(t.TempDir(), "relay.db")}, testLogger{})
	require.NoError(t, err)
	db := store.db
	for _, index := range []string{"idx_relay_logs_request_id", "idx_relay_logs_relay_name", "idx_relay_logs_timestamp", "idx_relay_logs_virtual_key_id", "idx_relay_logs_user_agent", "idx_relay_logs_app"} {
		assert.True(t, db.Migrator().HasIndex(&RelayLog{}, index), "%s is created", index)
	}
	require.NoError(t, db.Exec("DELETE FROM migrations WHERE id = ?", "add_relay_logs_table").Error)
	require.NoError(t, migrationCreateRelayLogsTable(ctx, db, testLogger{}), "a rerun over an existing table and its indexes succeeds")
}

// TestRelayLogObjectColumnsMigration pins relay_logs_add_object_columns: a database that ran
// add_relay_logs_table before the object columns existed gets them, and a rerun is a no-op.
func TestRelayLogObjectColumnsMigration(t *testing.T) {
	ctx := context.Background()
	store, err := newSqliteLogStore(ctx, &SQLiteConfig{Path: filepath.Join(t.TempDir(), "relay-object.db")}, testLogger{})
	require.NoError(t, err)
	db := store.db
	for _, column := range []string{"payload_reference", "has_object"} {
		assert.True(t, db.Migrator().HasColumn(&RelayLog{}, column), "%s is created", column)
		require.NoError(t, db.Migrator().DropColumn(&RelayLog{}, column))
	}
	require.NoError(t, db.Exec("DELETE FROM migrations WHERE id = ?", "relay_logs_add_object_columns").Error)
	require.NoError(t, migrationAddRelayLogObjectColumns(ctx, db, testLogger{}))
	for _, column := range []string{"payload_reference", "has_object"} {
		assert.True(t, db.Migrator().HasColumn(&RelayLog{}, column), "%s is added to an existing table", column)
	}
	require.NoError(t, db.Exec("DELETE FROM migrations WHERE id = ?", "relay_logs_add_object_columns").Error)
	require.NoError(t, migrationAddRelayLogObjectColumns(ctx, db, testLogger{}), "a rerun over existing columns succeeds")
}

// TestRelayHistogramCountsCancelled pins the cancelled count the Relay Logs chart draws, as the LLM
// logs histogram does.
func TestRelayHistogramCountsCancelled(t *testing.T) {
	ctx := context.Background()
	store, err := newSqliteLogStore(ctx, &SQLiteConfig{Path: filepath.Join(t.TempDir(), "relay-hist.db")}, testLogger{})
	require.NoError(t, err)
	now := time.Now().UTC().Truncate(time.Second)
	_, err = store.BatchCreateRelayLogsIfNotExists(ctx, []*RelayLog{
		{ID: "ok", RequestID: "ok", Timestamp: now, RelayName: "docs", Status: RelayLogStatusSuccess, Latency: schemas.Ptr(100.0), BilledUnits: schemas.Ptr(4.0), BytesOut: 300},
		{ID: "gone", RequestID: "gone", Timestamp: now, RelayName: "docs", Status: RelayLogStatusCancelled, Latency: schemas.Ptr(300.0), BytesOut: 100},
	})
	require.NoError(t, err)
	start, end := now.Add(-time.Minute), now.Add(time.Minute)
	hist, err := store.GetRelayHistogram(ctx, RelayLogFilters{StartTime: &start, EndTime: &end}, 60)
	require.NoError(t, err)
	var success, cancelled int64
	for _, b := range hist.Buckets {
		success += b.Success
		cancelled += b.Cancelled
	}
	assert.EqualValues(t, 1, success)
	assert.EqualValues(t, 1, cancelled, "cancelled calls are counted for the chart")
	var latency, units float64
	var bytesOut int64
	for _, b := range hist.Buckets {
		if b.Count > 0 {
			latency, units, bytesOut = b.AvgLatency, b.Units, b.BytesOut
		}
	}
	assert.InDelta(t, 200, latency, 1e-9, "the bucket carries the average latency for the sparkline")
	assert.InDelta(t, 4, units, 1e-9)
	assert.EqualValues(t, 400, bytesOut)
	stats, err := store.GetRelayLogStats(ctx, RelayLogFilters{StartTime: &start, EndTime: &end})
	require.NoError(t, err)
	assert.EqualValues(t, 400, stats.TotalBytesOut)
}
