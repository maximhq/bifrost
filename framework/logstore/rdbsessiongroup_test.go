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

// newSessionGroupStore seeds a DB covering the shapes session grouping has to
// get right: a session whose first row is a fallback child, a session whose
// first row is filtered out by a status filter, a single-row session, and rows
// with no session at all.
//
// Named shared-cache DSN for the same reason as newRootsOnlyStore: SearchLogs
// queries from concurrent goroutines, and a plain :memory: DSN gives each
// pooled connection its own empty DB.
func newSessionGroupStore(t *testing.T) (*RDBLogStore, time.Time) {
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
		// Session s1, three turns. The second turn fell back once, and that
		// attempt is timestamped before the third turn — it must not be mistaken
		// for a session root.
		{ID: "s1-turn1", Timestamp: now, Status: "success", Provider: "openai", SessionID: strPtr("s1"), Cost: floatPtr(1), TotalTokens: 10},
		{ID: "s1-turn2", Timestamp: now.Add(time.Second), Status: "error", Provider: "openai", SessionID: strPtr("s1"), Cost: floatPtr(2), TotalTokens: 20},
		{ID: "s1-turn2-retry", Timestamp: now.Add(2 * time.Second), Status: "success", Provider: "anthropic", FallbackIndex: 1, ParentRequestID: strPtr("s1-turn2"), SessionID: strPtr("s1"), Cost: floatPtr(4), TotalTokens: 40},
		{ID: "s1-turn3", Timestamp: now.Add(3 * time.Second), Status: "success", Provider: "openai", SessionID: strPtr("s1"), Cost: floatPtr(8), TotalTokens: 80},

		// Session s2 whose earliest turn is a fallback child of a row in the same
		// session: the chain root is later in time. The session must still list,
		// rooted at the chain root rather than vanishing behind the child.
		{ID: "s2-child", Timestamp: now, Status: "error", Provider: "openai", FallbackIndex: 1, ParentRequestID: strPtr("s2-root"), SessionID: strPtr("s2"), Cost: floatPtr(1), TotalTokens: 5},
		{ID: "s2-root", Timestamp: now.Add(time.Second), Status: "success", Provider: "openai", SessionID: strPtr("s2"), Cost: floatPtr(3), TotalTokens: 15},

		// Single-row session: no group, so it must carry no session aggregates.
		{ID: "s3-only", Timestamp: now, Status: "success", Provider: "openai", SessionID: strPtr("s3"), Cost: floatPtr(1), TotalTokens: 7},

		// No session at all.
		{ID: "loose", Timestamp: now.Add(4 * time.Second), Status: "success", Provider: "openai", Cost: floatPtr(1), TotalTokens: 3},
	}
	for i := range seed {
		require.NoError(t, db.Create(&seed[i]).Error)
	}

	return &RDBLogStore{db: db, logger: bifrost.NewDefaultLogger(schemas.LogLevelInfo)}, now
}

// Session grouping collapses every row sharing a session_id onto that session's
// earliest chain root, and rolls the whole session up onto it.
func TestSearchLogsSessionGrouping(t *testing.T) {
	s, _ := newSessionGroupStore(t)
	ctx := context.Background()

	t.Run("collapses each session to its earliest chain root", func(t *testing.T) {
		result, err := s.SearchLogs(ctx, SearchFilters{RootsOnly: true, GroupSessions: true}, PaginationOptions{Limit: 50})
		require.NoError(t, err)
		require.ElementsMatch(t, []string{"s1-turn1", "s2-root", "s3-only", "loose"}, logIDs(result.Logs))
		require.EqualValues(t, 4, result.Pagination.TotalCount)
	})

	// The case that breaks if the anti-join forgets to require the peer to be a
	// chain root: s2-child is earlier than s2-root, but it is already hidden by
	// roots_only, so letting it suppress s2-root would drop the session entirely.
	t.Run("session whose earliest row is a fallback child still lists", func(t *testing.T) {
		result, err := s.SearchLogs(ctx, SearchFilters{RootsOnly: true, GroupSessions: true}, PaginationOptions{Limit: 50})
		require.NoError(t, err)
		require.Contains(t, logIDs(result.Logs), "s2-root")
	})

	t.Run("session root carries the whole session's rollup", func(t *testing.T) {
		result, err := s.SearchLogs(ctx, SearchFilters{RootsOnly: true, GroupSessions: true}, PaginationOptions{Limit: 50})
		require.NoError(t, err)

		byID := map[string]Log{}
		for _, log := range result.Logs {
			byID[log.ID] = log
		}

		s1 := byID["s1-turn1"]
		require.EqualValues(t, 3, s1.SessionChildCount)
		require.InDelta(t, 15, s1.SessionTotalCost, 1e-9)
		require.EqualValues(t, 150, s1.SessionTotalTokens)

		s2 := byID["s2-root"]
		require.EqualValues(t, 1, s2.SessionChildCount)
		require.InDelta(t, 4, s2.SessionTotalCost, 1e-9)
		require.EqualValues(t, 20, s2.SessionTotalTokens)
	})

	t.Run("single-row session and session-less rows stay ordinary", func(t *testing.T) {
		result, err := s.SearchLogs(ctx, SearchFilters{RootsOnly: true, GroupSessions: true}, PaginationOptions{Limit: 50})
		require.NoError(t, err)
		for _, log := range result.Logs {
			if log.ID == "s3-only" || log.ID == "loose" {
				require.Zero(t, log.SessionChildCount)
				require.Zero(t, log.SessionTotalCost)
				require.Zero(t, log.SessionTotalTokens)
			}
		}
	})

	t.Run("filtering out the earliest turn promotes the next one", func(t *testing.T) {
		result, err := s.SearchLogs(ctx, SearchFilters{RootsOnly: true, GroupSessions: true, Providers: []string{"anthropic"}}, PaginationOptions{Limit: 50})
		require.NoError(t, err)
		// Only s1-turn2-retry is anthropic. Its parent is filtered out, so it is
		// promoted to chain root, and it is then the session's only row.
		require.Equal(t, []string{"s1-turn2-retry"}, logIDs(result.Logs))
	})

	t.Run("ignored for queries already scoped to one group or row", func(t *testing.T) {
		bySession, err := s.SearchLogs(ctx, SearchFilters{RootsOnly: true, GroupSessions: true, SessionID: "s1"}, PaginationOptions{Limit: 50})
		require.NoError(t, err)
		require.ElementsMatch(t, []string{"s1-turn1", "s1-turn2", "s1-turn3"}, logIDs(bySession.Logs))

		byParent, err := s.SearchLogs(ctx, SearchFilters{RootsOnly: true, GroupSessions: true, ParentRequestID: "s1-turn2"}, PaginationOptions{Limit: 50})
		require.NoError(t, err)
		require.Equal(t, []string{"s1-turn2-retry"}, logIDs(byParent.Logs))

		byID, err := s.SearchLogs(ctx, SearchFilters{RootsOnly: true, GroupSessions: true, RequestID: "s1-turn3"}, PaginationOptions{Limit: 50})
		require.NoError(t, err)
		require.Equal(t, []string{"s1-turn3"}, logIDs(byID.Logs))
	})

	t.Run("roots_only alone is unchanged by the new flag", func(t *testing.T) {
		result, err := s.SearchLogs(ctx, SearchFilters{RootsOnly: true}, PaginationOptions{Limit: 50})
		require.NoError(t, err)
		require.ElementsMatch(t, []string{"s1-turn1", "s1-turn2", "s1-turn3", "s2-root", "s3-only", "loose"}, logIDs(result.Logs))
		for _, log := range result.Logs {
			require.Zero(t, log.SessionChildCount, "session aggregates leaked into a plain roots_only query for %s", log.ID)
		}
	})

	// The invariant the two-level view rests on: every row the flat view lists is
	// reachable from the grouped view exactly once, through the session root's
	// own chain plus the session's other chain roots and their chains.
	t.Run("grouped view reaches every filtered row exactly once", func(t *testing.T) {
		for _, f := range []SearchFilters{
			{},
			{Status: []string{"success"}},
			{Providers: []string{"openai"}},
		} {
			flat, err := s.SearchLogs(ctx, f, PaginationOptions{Limit: 50})
			require.NoError(t, err)

			groupFilters := f
			groupFilters.RootsOnly = true
			groupFilters.GroupSessions = true
			roots, err := s.SearchLogs(ctx, groupFilters, PaginationOptions{Limit: 50})
			require.NoError(t, err)

			var seen []string
			expand := func(parentID string) {
				childFilters := f
				childFilters.ParentRequestID = parentID
				children, err := s.SearchLogs(ctx, childFilters, PaginationOptions{Limit: 50})
				require.NoError(t, err)
				seen = append(seen, logIDs(children.Logs)...)
			}
			for _, root := range roots.Logs {
				seen = append(seen, root.ID)
				expand(root.ID)
				if root.SessionID == nil {
					continue
				}
				memberFilters := f
				memberFilters.RootsOnly = true
				memberFilters.SessionID = *root.SessionID
				members, err := s.SearchLogs(ctx, memberFilters, PaginationOptions{Limit: 50})
				require.NoError(t, err)
				for _, member := range members.Logs {
					if member.ID == root.ID {
						continue
					}
					seen = append(seen, member.ID)
					expand(member.ID)
				}
			}

			require.ElementsMatch(t, logIDs(flat.Logs), seen, "grouped view lost or duplicated rows under %+v", f)
		}
	})
}

func TestCanUseMatViewFiltersRejectsSessionGrouping(t *testing.T) {
	// Active grouping collapses rows, so the matview's pre-aggregated counts do
	// not describe the result set.
	require.False(t, canUseMatViewFilters(SearchFilters{GroupSessions: true, RootsOnly: true}))

	// Inactive grouping changes nothing about which rows match, so the matview
	// still answers correctly and there is no reason to pay for the raw scan.
	require.True(t, canUseMatViewFilters(SearchFilters{GroupSessions: true}))
}

// canUseMatViewFilters leans on RootsOnly alone to exclude every query that
// actually groups. That is only sound while grouping implies RootsOnly, so pin
// the implication here rather than leaving it as an unwritten assumption.
func TestSessionGroupingActiveImpliesRootsOnly(t *testing.T) {
	for _, f := range []SearchFilters{
		{GroupSessions: true},
		{GroupSessions: true, RootsOnly: true},
		{GroupSessions: true, RootsOnly: true, SessionID: "s1"},
		{GroupSessions: true, RootsOnly: true, ParentRequestID: "p1"},
		{GroupSessions: true, RootsOnly: true, RequestID: "r1"},
		{GroupSessions: false, RootsOnly: true},
	} {
		if f.SessionGroupingActive() {
			require.True(t, f.RootsOnly, "grouping active without RootsOnly under %+v: canUseMatViewFilters would wrongly allow the matview", f)
		}
	}
}

// An empty session_id is not a session. The collapse query has to agree with
// attachSessionAggregates, which already treats "" like NULL, or several
// sessionless rows fold onto a single row that then carries no rollup at all.
func TestSearchLogsSessionGroupingTreatsEmptySessionIDAsNoSession(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(&Log{}))

	now := time.Now()
	empty := ""
	seed := []Log{
		{ID: "empty-a", Timestamp: now, Status: "success", Provider: "openai", SessionID: &empty},
		{ID: "empty-b", Timestamp: now.Add(time.Second), Status: "success", Provider: "openai", SessionID: &empty},
		{ID: "null-c", Timestamp: now.Add(2 * time.Second), Status: "success", Provider: "openai"},
	}
	for i := range seed {
		require.NoError(t, db.Create(&seed[i]).Error)
	}
	s := &RDBLogStore{db: db, logger: bifrost.NewDefaultLogger(schemas.LogLevelInfo)}

	result, err := s.SearchLogs(context.Background(), SearchFilters{RootsOnly: true, GroupSessions: true}, PaginationOptions{Limit: 50})
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"empty-a", "empty-b", "null-c"}, logIDs(result.Logs),
		"rows with an empty session_id were collapsed as though they shared a session")
	require.EqualValues(t, 3, result.Pagination.TotalCount)
}
