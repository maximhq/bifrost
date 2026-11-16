package logstore

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// A log row's primary key is the request ID, so a pasted ID is an exact PK
// lookup rather than a content-summary text scan. It deliberately ignores the
// selected time window — an ID is unique, and hiding it behind the window is
// the usual reason a pasted ID appears to match nothing.
func TestSearchLogsRequestID(t *testing.T) {
	s, now := newRootsOnlyStore(t)
	ctx := context.Background()

	t.Run("returns only the matching row", func(t *testing.T) {
		result, err := s.SearchLogs(ctx, SearchFilters{RequestID: "root-b"}, PaginationOptions{Limit: 50})
		require.NoError(t, err)
		require.Equal(t, []string{"root-b"}, logIDs(result.Logs))
		require.EqualValues(t, 1, result.Pagination.TotalCount)
	})

	t.Run("ignores the time window", func(t *testing.T) {
		// A window that excludes every seeded row.
		past := now.Add(-48 * time.Hour)
		filters := SearchFilters{
			RequestID: "root-b",
			StartTime: &past,
			EndTime:   timePtr(past.Add(time.Hour)),
		}
		result, err := s.SearchLogs(ctx, filters, PaginationOptions{Limit: 50})
		require.NoError(t, err)
		require.Equal(t, []string{"root-b"}, logIDs(result.Logs))
	})

	t.Run("surfaces a fallback child that roots_only would hide", func(t *testing.T) {
		result, err := s.SearchLogs(ctx, SearchFilters{RequestID: "child-a1", RootsOnly: true}, PaginationOptions{Limit: 50})
		require.NoError(t, err)
		require.Equal(t, []string{"child-a1"}, logIDs(result.Logs))
	})

	t.Run("still honors the other filters", func(t *testing.T) {
		result, err := s.SearchLogs(ctx, SearchFilters{RequestID: "root-b", Providers: []string{"anthropic"}}, PaginationOptions{Limit: 50})
		require.NoError(t, err)
		require.Empty(t, result.Logs)
	})

	t.Run("unknown id matches nothing", func(t *testing.T) {
		result, err := s.SearchLogs(ctx, SearchFilters{RequestID: "no-such-id"}, PaginationOptions{Limit: 50})
		require.NoError(t, err)
		require.Empty(t, result.Logs)
		require.EqualValues(t, 0, result.Pagination.TotalCount)
	})
}

// mv_logs_hourly is a per-hour rollup with no id column, so an ID lookup must
// fall back to the raw logs table.
func TestCanUseMatViewFiltersRejectsRequestID(t *testing.T) {
	require.True(t, canUseMatViewFilters(SearchFilters{}))
	require.False(t, canUseMatViewFilters(SearchFilters{RequestID: "root-b"}))
}

func timePtr(v time.Time) *time.Time { return &v }
