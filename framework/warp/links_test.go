package warp

import (
	"testing"
	"time"

	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/stretchr/testify/require"
)

// Every row and every aggregate Warp reports can be opened in the Logs view.
// The links are built here, server-side, so the model never has to guess the
// dashboard's URL scheme - it only has to repeat what it was given.
func TestWarpLogDetailLink(t *testing.T) {
	require.Equal(t, "/workspace/logs?selected_log=req-1", logDetailLink("req-1"))
	require.Equal(t, "/workspace/logs?selected_log=a%2Fb", logDetailLink("a/b"), "ids are escaped")
	require.Empty(t, logDetailLink(""), "no id, no link")
}

func TestWarpLogsViewLinkEncodesFilters(t *testing.T) {
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	filters := &logstore.SearchFilters{
		Providers: []string{"gemini", "openai"},
		Models:    []string{"gemini-3.1-flash-lite"},
		Status:    []string{"success"},
		UserIDs:   []string{"u-1"},
		StartTime: &start,
		EndTime:   &end,
	}
	link := logsViewLink(filters)
	require.True(t, len(link) > len("/workspace/logs?"))
	require.Contains(t, link, "providers=gemini%2Copenai")
	require.Contains(t, link, "models=gemini-3.1-flash-lite")
	require.Contains(t, link, "status=success")
	require.Contains(t, link, "user_ids=u-1")
	// The Logs page keys its window on unix seconds, and only honours a window
	// when both ends are present.
	require.Contains(t, link, "start_time=1788220800")
	require.Contains(t, link, "end_time=1788307200")
}

func TestWarpLogsViewLinkOmitsEmptyFilters(t *testing.T) {
	require.Equal(t, "/workspace/logs", logsViewLink(&logstore.SearchFilters{}))
	require.Equal(t, "/workspace/logs", logsViewLink(nil))
	// A half-open window is dropped rather than sent as one side only, which the
	// Logs page would ignore in favour of its default hour.
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	require.Equal(t, "/workspace/logs", logsViewLink(&logstore.SearchFilters{StartTime: &start}))
}
