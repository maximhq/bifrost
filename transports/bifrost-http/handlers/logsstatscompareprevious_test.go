package handlers

import (
	"encoding/json"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/valyala/fasthttp"
)

// callStats runs GET /api/logs/stats with the given query string against a mock
// log manager and returns the manager, the decoded body and the status code.
func callStats(t *testing.T, mgr *dashboardLogManager, query string) (map[string]json.RawMessage, int) {
	t.Helper()
	SetLogger(&mockLogger{})

	h := &LoggingHandler{logManager: mgr}
	var req fasthttp.Request
	uri := "/api/logs/stats"
	if query != "" {
		uri += "?" + query
	}
	req.SetRequestURI(uri)

	ctx := &fasthttp.RequestCtx{}
	ctx.Init(&req, &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 12345}, nil)

	h.getLogsStats(ctx)

	body := ctx.Response.Body()
	payload := map[string]json.RawMessage{}
	if json.Valid(body) {
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("decode stats response: %v (body %s)", err, string(body))
		}
	}
	return payload, ctx.Response.StatusCode()
}

// TestLogsStatsWithoutCompareIsUnchanged pins the backward-compatible shape: a
// request that does not ask for a comparison must still return the current
// period's fields at the top level and must issue exactly one stats query.
func TestLogsStatsWithoutCompareIsUnchanged(t *testing.T) {
	mgr := &dashboardLogManager{
		statsFunc: func(filters *logstore.SearchFilters) (*logstore.SearchStats, error) {
			return &logstore.SearchStats{TotalRequests: 42, SuccessRate: 68.27}, nil
		},
	}

	payload, status := callStats(t, mgr, "period=24h")

	if status != fasthttp.StatusOK {
		t.Fatalf("expected 200, got %d", status)
	}
	if len(mgr.statsCalls) != 1 {
		t.Fatalf("expected exactly one stats query, got %d", len(mgr.statsCalls))
	}
	if _, ok := payload["previous"]; ok {
		t.Fatal("previous must be omitted when compare_to_previous is not requested")
	}
	// has_previous_period is the one comparison field that is always present, so
	// callers can read it without probing for the key - the same shape the three
	// ranking-trend structs in framework/logstore/tables.go publish. The schema
	// marks it required; this is what stops it drifting to omitempty.
	raw, ok := payload["has_previous_period"]
	if !ok {
		t.Fatal("has_previous_period must be present even without compare_to_previous")
	}
	var hasPreviousUnrequested bool
	if err := json.Unmarshal(raw, &hasPreviousUnrequested); err != nil {
		t.Fatalf("decode has_previous_period: %v", err)
	}
	if hasPreviousUnrequested {
		t.Fatal("has_previous_period must be false when no comparison was requested")
	}
	var total int64
	if err := json.Unmarshal(payload["total_requests"], &total); err != nil {
		t.Fatalf("total_requests must stay at the top level: %v", err)
	}
	if total != 42 {
		t.Fatalf("expected top-level total_requests 42, got %d", total)
	}
}

// TestLogsStatsComparePreviousWindow is the core case: the previous window must
// be the same length as the current one and must end just before it starts.
func TestLogsStatsComparePreviousWindow(t *testing.T) {
	mgr := &dashboardLogManager{}
	// statsCalls has already been appended to when statsFunc runs, so call 1 is
	// the current period (100) and call 2 is the previous one (200).
	mgr.statsFunc = func(filters *logstore.SearchFilters) (*logstore.SearchStats, error) {
		return &logstore.SearchStats{TotalRequests: int64(100 * len(mgr.statsCalls))}, nil
	}

	payload, status := callStats(t, mgr, "period=24h&compare_to_previous=true")

	if status != fasthttp.StatusOK {
		t.Fatalf("expected 200, got %d", status)
	}
	if len(mgr.statsCalls) != 2 {
		t.Fatalf("expected current + previous stats queries, got %d", len(mgr.statsCalls))
	}

	current, previous := mgr.statsCalls[0], mgr.statsCalls[1]
	if current.StartTime == nil || current.EndTime == nil || previous.StartTime == nil || previous.EndTime == nil {
		t.Fatal("both windows must be bounded")
	}

	currentLen := current.EndTime.Sub(*current.StartTime)
	previousLen := previous.EndTime.Sub(*previous.StartTime)
	// The previous window is one nanosecond shorter by construction: it ends at
	// currentStart-1ns so the two periods never overlap on the same row.
	if got := currentLen - previousLen; got != time.Nanosecond {
		t.Fatalf("previous window must match the current length (minus the 1ns gap), got a %v difference", got)
	}
	if !previous.EndTime.Equal(current.StartTime.Add(-time.Nanosecond)) {
		t.Fatalf("previous window must end 1ns before the current one starts: prevEnd=%v currentStart=%v", previous.EndTime, current.StartTime)
	}

	var hasPrevious bool
	if err := json.Unmarshal(payload["has_previous_period"], &hasPrevious); err != nil || !hasPrevious {
		t.Fatalf("expected has_previous_period true, got %v (err %v)", hasPrevious, err)
	}
	if _, ok := payload["previous"]; !ok {
		t.Fatal("expected previous stats in the payload")
	}
}

// TestLogsStatsCompareSkippedWhenUnbounded covers the all-time filter: there is
// no preceding window, so no second query is issued.
func TestLogsStatsCompareSkippedWhenUnbounded(t *testing.T) {
	mgr := &dashboardLogManager{}

	payload, status := callStats(t, mgr, "compare_to_previous=true")

	if status != fasthttp.StatusOK {
		t.Fatalf("expected 200, got %d", status)
	}
	if len(mgr.statsCalls) != 1 {
		t.Fatalf("an unbounded filter has no previous period, expected one query, got %d", len(mgr.statsCalls))
	}
	var hasPrevious bool
	if err := json.Unmarshal(payload["has_previous_period"], &hasPrevious); err != nil {
		t.Fatalf("decode has_previous_period: %v", err)
	}
	if hasPrevious {
		t.Fatal("has_previous_period must be false for an unbounded window")
	}
}

// TestLogsStatsCompareDegradesOnPreviousError asserts the comparison is
// best-effort: a failing previous-period query must not fail the whole request,
// because the current period is still useful on its own.
func TestLogsStatsCompareDegradesOnPreviousError(t *testing.T) {
	mgr := &dashboardLogManager{}
	mgr.statsFunc = func(filters *logstore.SearchFilters) (*logstore.SearchStats, error) {
		if len(mgr.statsCalls) > 1 {
			return nil, errors.New("previous period query failed")
		}
		return &logstore.SearchStats{TotalRequests: 7}, nil
	}

	payload, status := callStats(t, mgr, "period=7d&compare_to_previous=true")

	if status != fasthttp.StatusOK {
		t.Fatalf("a failing comparison must not fail the request, got %d", status)
	}
	var total int64
	if err := json.Unmarshal(payload["total_requests"], &total); err != nil || total != 7 {
		t.Fatalf("expected the current period to survive, got %d (err %v)", total, err)
	}
	var hasPrevious bool
	if err := json.Unmarshal(payload["has_previous_period"], &hasPrevious); err != nil {
		t.Fatalf("decode has_previous_period: %v", err)
	}
	if hasPrevious {
		t.Fatal("has_previous_period must be false when the previous query failed")
	}
	if _, ok := payload["previous"]; ok {
		t.Fatal("previous must be omitted when the previous query failed")
	}
}

// TestLogsStatsCompareSkippedForRequestIDLookup covers a window that is present
// but not in effect. An explicit request_id is an exact primary-key lookup, and
// the store deliberately drops the time-range clauses for it (see the RequestID
// branch in framework/logstore/rdb.go), so shifting the window back changes
// nothing about which row is read: without a guard the "previous period" is the
// very same request, reported as a real comparison.
func TestLogsStatsCompareSkippedForRequestIDLookup(t *testing.T) {
	mgr := &dashboardLogManager{
		statsFunc: func(filters *logstore.SearchFilters) (*logstore.SearchStats, error) {
			return &logstore.SearchStats{TotalRequests: 1}, nil
		},
	}

	payload, status := callStats(t, mgr, "request_id=req-abc&period=24h&compare_to_previous=true")

	if status != fasthttp.StatusOK {
		t.Fatalf("expected 200, got %d", status)
	}
	if len(mgr.statsCalls) != 1 {
		t.Fatalf("an id lookup ignores the window, so there is no previous period to query: got %d queries", len(mgr.statsCalls))
	}
	if _, ok := payload["previous"]; ok {
		t.Fatal("previous must be omitted for an id lookup: it would be the same request")
	}
	var hasPrevious bool
	if err := json.Unmarshal(payload["has_previous_period"], &hasPrevious); err != nil {
		t.Fatalf("decode has_previous_period: %v", err)
	}
	if hasPrevious {
		t.Fatal("has_previous_period must be false for an id lookup")
	}
}

// TestLogsStatsCompareSkippedForEmptyWindow covers start == end and a reversed
// window. Neither has a positive duration to shift back by, so the preceding
// range previousPeriodFilters would build ends before it starts.
func TestLogsStatsCompareSkippedForEmptyWindow(t *testing.T) {
	instant := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	earlier := time.Date(2026, 9, 1, 11, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)

	for name, query := range map[string]string{
		"zero length": "start_time=" + instant + "&end_time=" + instant + "&compare_to_previous=true",
		"reversed":    "start_time=" + instant + "&end_time=" + earlier + "&compare_to_previous=true",
	} {
		t.Run(name, func(t *testing.T) {
			mgr := &dashboardLogManager{}

			payload, status := callStats(t, mgr, query)

			if status != fasthttp.StatusOK {
				t.Fatalf("expected 200, got %d", status)
			}
			if len(mgr.statsCalls) != 1 {
				t.Fatalf("expected no previous-period query for a %s window, got %d queries", name, len(mgr.statsCalls))
			}
			var hasPrevious bool
			if err := json.Unmarshal(payload["has_previous_period"], &hasPrevious); err != nil {
				t.Fatalf("decode has_previous_period: %v", err)
			}
			if hasPrevious {
				t.Fatalf("has_previous_period must be false for a %s window", name)
			}
		})
	}
}
