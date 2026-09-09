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

// newRootsOnlyStore returns a store over a seeded in-memory DB.
//
// Named shared-cache DSN: SearchLogs queries from concurrent goroutines, and a
// plain :memory: DSN would give each pooled connection its own empty DB. The
// name is process-global and the DB lives until its last connection closes, so
// close it on cleanup — otherwise a second run in the same process (-count=2)
// re-seeds into the surviving DB and fails on duplicate primary keys.
func newRootsOnlyStore(t *testing.T) (*RDBLogStore, time.Time) {
	t.Helper()

	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(&Log{}))

	now := time.Now()
	strPtr := func(v string) *string { return &v }
	floatPtr := func(v float64) *float64 { return &v }

	seed := []Log{
		// Fallback chain: root + two attempts pointing at the root's log ID. The
		// root errored and the last attempt succeeded — the shape that a status
		// filter must not make disappear.
		{ID: "root-a", Timestamp: now, Status: "error", Provider: "openai", FallbackIndex: 0},
		{ID: "child-a1", Timestamp: now.Add(time.Second), Status: "error", Provider: "openai", FallbackIndex: 1, ParentRequestID: strPtr("root-a"), Cost: floatPtr(0.5), TotalTokens: 100},
		{ID: "child-a2", Timestamp: now.Add(2 * time.Second), Status: "success", Provider: "anthropic", FallbackIndex: 2, ParentRequestID: strPtr("root-a"), Cost: floatPtr(0.25), TotalTokens: 40},
		// Plain root with no children.
		{ID: "root-b", Timestamp: now, Status: "success", Provider: "openai", FallbackIndex: 0},
		// Baggage-session member: parent is a client session string, not a log ID.
		{ID: "session-member", Timestamp: now, Status: "success", Provider: "openai", FallbackIndex: 0, ParentRequestID: strPtr("client-session-123")},
		// Baggage-session member that reuses a real log's ID as its session ID.
		// parent points at an actual log row, so it nests under root-a like a
		// fallback attempt — hidden from the roots list, counted in aggregates.
		{ID: "session-member-on-root", Timestamp: now.Add(3 * time.Second), Status: "success", Provider: "openai", FallbackIndex: 0, ParentRequestID: strPtr("root-a"), Cost: floatPtr(1.5), TotalTokens: 60},
		// parent_request_id is client-settable (baggage), so a row can point at
		// itself. It must stay listable rather than hide itself out of every view.
		{ID: "self-ref", Timestamp: now, Status: "success", Provider: "openai", FallbackIndex: 0, ParentRequestID: strPtr("self-ref")},
	}
	for i := range seed {
		require.NoError(t, db.Create(&seed[i]).Error)
	}

	return &RDBLogStore{db: db, logger: bifrost.NewDefaultLogger(schemas.LogLevelInfo)}, now
}

// RootsOnly hides every row whose parent_request_id references another row in
// the same filtered result set (fallback attempts and session members keyed on
// a prior request's ID), so each chain lists as its root request. Rows whose
// parent is a client session string stay top-level — there is no root row to
// nest under. Roots carry child aggregates for the expandable chain UI.
func TestSearchLogsRootsOnly(t *testing.T) {
	s, _ := newRootsOnlyStore(t)
	ctx := context.Background()

	t.Run("flat view returns every row", func(t *testing.T) {
		result, err := s.SearchLogs(ctx, SearchFilters{}, PaginationOptions{Limit: 50})
		require.NoError(t, err)
		require.Len(t, result.Logs, 7)
		require.EqualValues(t, 7, result.Pagination.TotalCount)
	})

	t.Run("roots_only hides fallback children and attaches child aggregates", func(t *testing.T) {
		result, err := s.SearchLogs(ctx, SearchFilters{RootsOnly: true}, PaginationOptions{Limit: 50})
		require.NoError(t, err)
		require.EqualValues(t, 4, result.Pagination.TotalCount)

		byID := map[string]Log{}
		for _, log := range result.Logs {
			byID[log.ID] = log
		}
		require.Len(t, byID, 4)
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

	t.Run("self-referencing row stays listable", func(t *testing.T) {
		result, err := s.SearchLogs(ctx, SearchFilters{RootsOnly: true}, PaginationOptions{Limit: 50})
		require.NoError(t, err)
		require.Contains(t, logIDs(result.Logs), "self-ref")
	})

	t.Run("parent filter overrides roots_only so children stay reachable", func(t *testing.T) {
		result, err := s.SearchLogs(ctx, SearchFilters{RootsOnly: true, ParentRequestID: "root-a"}, PaginationOptions{Limit: 50})
		require.NoError(t, err)
		require.Len(t, result.Logs, 3)
		for _, log := range result.Logs {
			require.Equal(t, "root-a", *log.ParentRequestID)
		}
	})

	// The invariant the grouped view rests on: under any filter set, every row
	// the flat view would list appears exactly once in the grouped view — either
	// as a root, or under the root it expands from — and ChildCount agrees with
	// what that expansion returns.
	t.Run("grouped view partitions the filtered rows exactly once each", func(t *testing.T) {
		for _, f := range []SearchFilters{
			{},
			{Status: []string{"success"}},
			{Status: []string{"error"}},
			{Providers: []string{"openai"}},
			{Providers: []string{"anthropic"}},
		} {
			flat, err := s.SearchLogs(ctx, f, PaginationOptions{Limit: 50})
			require.NoError(t, err)

			rootFilters := f
			rootFilters.RootsOnly = true
			roots, err := s.SearchLogs(ctx, rootFilters, PaginationOptions{Limit: 50})
			require.NoError(t, err)

			seen := logIDs(roots.Logs)
			for _, root := range roots.Logs {
				childFilters := f
				childFilters.ParentRequestID = root.ID
				children, err := s.SearchLogs(ctx, childFilters, PaginationOptions{Limit: 50})
				require.NoError(t, err)
				require.EqualValues(t, root.ChildCount, len(children.Logs), "child_count disagrees with expansion for %s under %+v", root.ID, f)
				seen = append(seen, logIDs(children.Logs)...)
			}

			require.ElementsMatch(t, logIDs(flat.Logs), seen, "grouped view lost or duplicated rows under %+v", f)
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

// A child is only hidden when the row it points at is itself listable under the
// same filters. Otherwise filtering on a column that varies across a fallback
// chain (status, provider, model, key) would drop the whole chain: the child is
// hidden because its parent exists, and the parent is dropped by the filter.
func TestSearchLogsRootsOnlyPromotesChildrenWhenParentIsFilteredOut(t *testing.T) {
	s, now := newRootsOnlyStore(t)
	ctx := context.Background()

	t.Run("status filter promotes the successful attempt of a failed chain", func(t *testing.T) {
		result, err := s.SearchLogs(ctx, SearchFilters{RootsOnly: true, Status: []string{"success"}}, PaginationOptions{Limit: 50})
		require.NoError(t, err)

		ids := logIDs(result.Logs)
		// root-a errored, so it is not a listable parent — its successful members
		// surface in their own right instead of vanishing with it.
		require.Contains(t, ids, "child-a2")
		require.Contains(t, ids, "session-member-on-root")
		require.NotContains(t, ids, "child-a1") // still an error row
		require.Len(t, ids, 5)
		require.EqualValues(t, 5, result.Pagination.TotalCount)
	})

	t.Run("provider filter promotes the attempt that ran on that provider", func(t *testing.T) {
		result, err := s.SearchLogs(ctx, SearchFilters{RootsOnly: true, Providers: []string{"anthropic"}}, PaginationOptions{Limit: 50})
		require.NoError(t, err)
		require.Equal(t, []string{"child-a2"}, logIDs(result.Logs))
	})

	t.Run("child aggregates count only children matching the filters", func(t *testing.T) {
		// root-a's three children are two openai rows and one anthropic row.
		result, err := s.SearchLogs(ctx, SearchFilters{RootsOnly: true, Providers: []string{"openai"}}, PaginationOptions{Limit: 50})
		require.NoError(t, err)

		byID := map[string]Log{}
		for _, log := range result.Logs {
			byID[log.ID] = log
		}
		rootA := byID["root-a"]
		require.EqualValues(t, 2, rootA.ChildCount) // child-a2 (anthropic) excluded
		require.InDelta(t, 2.0, rootA.ChildrenCost, 1e-9)
		require.EqualValues(t, 160, rootA.ChildrenTokens)
	})

	t.Run("time window promotes children whose parent falls outside it", func(t *testing.T) {
		start := now.Add(1500 * time.Millisecond)
		result, err := s.SearchLogs(ctx, SearchFilters{RootsOnly: true, StartTime: &start}, PaginationOptions{Limit: 50})
		require.NoError(t, err)
		require.ElementsMatch(t, []string{"child-a2", "session-member-on-root"}, logIDs(result.Logs))
	})
}

// The parent-existence subquery runs under the caller's QueryScope, like the
// outer query. A child whose parent is invisible to this caller is promoted to
// a root rather than hidden behind a row they can never see or expand.
func TestSearchLogsRootsOnlyRespectsQueryScope(t *testing.T) {
	s, _ := newRootsOnlyStore(t)
	ctx := queryscope.WithQueryScope(context.Background(), func(db *gorm.DB) *gorm.DB {
		return db.Where("id <> ?", "root-a")
	})

	result, err := s.SearchLogs(ctx, SearchFilters{RootsOnly: true}, PaginationOptions{Limit: 50})
	require.NoError(t, err)

	ids := logIDs(result.Logs)
	require.NotContains(t, ids, "root-a")
	require.Contains(t, ids, "child-a1")
	require.Contains(t, ids, "child-a2")
	require.Contains(t, ids, "session-member-on-root")
	require.EqualValues(t, len(ids), result.Pagination.TotalCount)
}

// RootsOnly needs a per-row predicate the hourly matview cannot express, so the
// matview count path must fall back to raw.
func TestCanUseMatViewFiltersExcludesRootsOnly(t *testing.T) {
	require.True(t, canUseMatViewFilters(SearchFilters{}))
	require.False(t, canUseMatViewFilters(SearchFilters{RootsOnly: true}))
}

func logIDs(logs []Log) []string {
	ids := make([]string, 0, len(logs))
	for _, log := range logs {
		ids = append(ids, log.ID)
	}
	return ids
}
