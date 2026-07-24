package logstore

import (
	"context"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// RootsOnly hides every row whose parent_request_id references an actual log
// row (fallback attempts and session members keyed on a prior request's ID), so
// each chain lists as its root request. Rows whose parent is a client session
// string stay top-level — there is no root row to nest under. Roots carry child
// aggregates for the expandable chain UI.
func TestSearchLogsRootsOnly(t *testing.T) {
	// Named shared-cache DSN: SearchLogs queries from concurrent goroutines, and
	// a plain :memory: DSN would give each pooled connection its own empty DB.
	db, err := gorm.Open(sqlite.Open("file:rootsonly_test?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Log{}))

	s := &RDBLogStore{db: db, logger: bifrost.NewDefaultLogger(schemas.LogLevelInfo)}
	ctx := context.Background()
	now := time.Now()

	strPtr := func(v string) *string { return &v }
	floatPtr := func(v float64) *float64 { return &v }

	seed := []Log{
		// Fallback chain: root + two attempts pointing at the root's log ID.
		{ID: "root-a", Timestamp: now, Status: "error", FallbackIndex: 0},
		{ID: "child-a1", Timestamp: now.Add(time.Second), Status: "error", FallbackIndex: 1, ParentRequestID: strPtr("root-a"), Cost: floatPtr(0.5), TotalTokens: 100},
		{ID: "child-a2", Timestamp: now.Add(2 * time.Second), Status: "success", FallbackIndex: 2, ParentRequestID: strPtr("root-a"), Cost: floatPtr(0.25), TotalTokens: 40},
		// Plain root with no children.
		{ID: "root-b", Timestamp: now, Status: "success", FallbackIndex: 0},
		// Baggage-session member: parent is a client session string, not a log ID.
		{ID: "session-member", Timestamp: now, Status: "success", FallbackIndex: 0, ParentRequestID: strPtr("client-session-123")},
		// Baggage-session member that reuses a real log's ID as its session ID.
		// parent points at an actual log row, so it nests under root-a like a
		// fallback attempt — hidden from the roots list, counted in aggregates.
		{ID: "session-member-on-root", Timestamp: now.Add(3 * time.Second), Status: "success", FallbackIndex: 0, ParentRequestID: strPtr("root-a"), Cost: floatPtr(1.5), TotalTokens: 60},
	}
	for i := range seed {
		require.NoError(t, db.Create(&seed[i]).Error)
	}

	t.Run("flat view returns every row", func(t *testing.T) {
		result, err := s.SearchLogs(ctx, SearchFilters{}, PaginationOptions{Limit: 50})
		require.NoError(t, err)
		require.Len(t, result.Logs, 6)
		require.EqualValues(t, 6, result.Pagination.TotalCount)
	})

	t.Run("roots_only hides fallback children and attaches child aggregates", func(t *testing.T) {
		result, err := s.SearchLogs(ctx, SearchFilters{RootsOnly: true}, PaginationOptions{Limit: 50})
		require.NoError(t, err)
		require.EqualValues(t, 3, result.Pagination.TotalCount)

		byID := map[string]Log{}
		for _, log := range result.Logs {
			byID[log.ID] = log
		}
		require.Len(t, byID, 3)
		require.NotContains(t, byID, "child-a1")
		require.NotContains(t, byID, "child-a2")
		require.NotContains(t, byID, "session-member-on-root")

		rootA := byID["root-a"]
		require.EqualValues(t, 3, rootA.ChildCount)
		require.InDelta(t, 2.25, rootA.ChildrenCost, 1e-9)
		require.EqualValues(t, 200, rootA.ChildrenTokens)

		require.Zero(t, byID["root-b"].ChildCount)
		require.Zero(t, byID["session-member"].ChildCount)
	})

	t.Run("parent filter overrides roots_only so children stay reachable", func(t *testing.T) {
		result, err := s.SearchLogs(ctx, SearchFilters{RootsOnly: true, ParentRequestID: "root-a"}, PaginationOptions{Limit: 50})
		require.NoError(t, err)
		require.Len(t, result.Logs, 3)
		for _, log := range result.Logs {
			require.Equal(t, "root-a", *log.ParentRequestID)
		}
	})

	t.Run("GetSessionLogs serves a chain's children by root ID", func(t *testing.T) {
		result, err := s.GetSessionLogs(ctx, "root-a", PaginationOptions{Limit: 50, Order: "asc"})
		require.NoError(t, err)
		require.Len(t, result.Logs, 3)
		require.Equal(t, "child-a1", result.Logs[0].ID)
		require.Equal(t, "child-a2", result.Logs[1].ID)
		require.Equal(t, "session-member-on-root", result.Logs[2].ID)
	})
}

// RootsOnly needs a per-row predicate the hourly matview cannot express, so the
// matview count path must fall back to raw.
func TestCanUseMatViewFiltersExcludesRootsOnly(t *testing.T) {
	require.True(t, canUseMatViewFilters(SearchFilters{}))
	require.False(t, canUseMatViewFilters(SearchFilters{RootsOnly: true}))
}
