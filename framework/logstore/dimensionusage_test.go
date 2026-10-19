package logstore

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// usageParityStore is one backend the usage readers are checked against: the
// store to read through and how to write a row into it.
type usageParityStore struct {
	store     *RDBLogStore
	create    func(t *testing.T, entry *Log)
	createMCP func(t *testing.T, entry *MCPToolLog)
}

// usageParityStores runs fn against SQLite, Postgres and ClickHouse, skipping a
// server backend that is not reachable, since each dialect fans out the array
// columns and binds the window boundary with different SQL.
func usageParityStores(t *testing.T, fn func(t *testing.T, s usageParityStore)) {
	writer := func(store LogStore) usageParityStore {
		return usageParityStore{
			create: func(t *testing.T, entry *Log) {
				t.Helper()
				require.NoError(t, store.Create(context.Background(), entry))
			},
			createMCP: func(t *testing.T, entry *MCPToolLog) {
				t.Helper()
				require.NoError(t, store.CreateMCPToolLog(context.Background(), entry))
			},
		}
	}
	t.Run("sqlite", func(t *testing.T) {
		store := newTestSQLiteStore(t)
		s := writer(store)
		s.store = store
		fn(t, s)
	})
	t.Run("postgres", func(t *testing.T) {
		store, _ := setupPerfTestDB(t)
		s := writer(store)
		s.store = store
		fn(t, s)
	})
	t.Run("clickhouse", func(t *testing.T) {
		ch := trySetupClickHouseStore(t)
		s := writer(ch)
		s.store = ch.RDBLogStore
		fn(t, s)
	})
}

// usageLog builds a terminal log row with the given model, cost and tokens.
func usageLog(id string, ts time.Time, model string, cost float64, tokens int) *Log {
	return &Log{
		ID: id, Timestamp: ts, CreatedAt: ts, Object: "chat_completion",
		Provider: fakeUsageProvider(model), Model: model, Status: "success",
		Cost: &cost, TotalTokens: tokens,
	}
}

// fakeUsageProvider names a test model's provider so (model, provider) groups differ.
func fakeUsageProvider(model string) string {
	if model == "claude" {
		return "anthropic"
	}
	return "openai"
}

// seedUsageLogs writes a spread of rows that exercises every attribution path
// the rankings take: array-only rows credited to several units, scalar-only
// rows, owner-less rows, rows in the previous window, a unit active only in
// the previous window, a zero-cost row, a non-terminal row, and rows outside
// both windows. It returns the current window.
func seedUsageLogs(t *testing.T, s usageParityStore) SearchFilters {
	now := time.Now().UTC().Truncate(time.Second)
	start, end := now.Add(-time.Hour), now.Add(time.Hour)
	cur, prev := now.Add(-10*time.Minute), now.Add(-90*time.Minute)
	n := 0
	add := func(ts time.Time, model string, cost float64, tokens int, edit func(*Log)) {
		n++
		entry := usageLog(fmt.Sprintf("usage-%d", n), ts, model, cost, tokens)
		edit(entry)
		s.create(t, entry)
	}
	units := func(ids ...string) func(*Log) {
		return func(l *Log) {
			l.BusinessUnitIDsParsed = ids
			l.BusinessUnitNamesParsed = ids
			l.TeamIDsParsed = []string{"team-" + ids[0]}
			l.TeamNamesParsed = []string{"team-" + ids[0]}
			user := "user-" + ids[0]
			l.UserID = &user
		}
	}
	scalar := func(id string) func(*Log) {
		return func(l *Log) { l.BusinessUnitID = &id }
	}

	add(cur, "gpt-4o", 1.5, 100, units("bu-a", "bu-b"))
	add(cur, "claude", 2.25, 40, units("bu-a"))
	add(cur, "gpt-4o", 0, 10, units("bu-b"))
	add(cur, "gpt-4o", 0.75, 30, scalar("bu-c"))
	add(cur, "gpt-4o", 0.5, 5, func(*Log) {})
	add(prev, "gpt-4o", 3, 300, units("bu-a"))
	add(prev, "claude", 1, 10, units("bu-a", "bu-c"))
	add(prev, "gpt-4o", 9, 90, units("bu-gone"))
	add(prev.Add(-2*time.Hour), "gpt-4o", 100, 1000, units("bu-a"))
	add(now.Add(2*time.Hour), "gpt-4o", 100, 1000, units("bu-b"))
	add(cur, "gpt-4o", 100, 1000, func(l *Log) {
		units("bu-a")(l)
		l.Status = "processing"
	})
	return SearchFilters{StartTime: &start, EndTime: &end}
}

// rankingsByID indexes a full ranking by id, leaving out the Other bucket the
// usage reader has no counterpart for.
func rankingsByID(t *testing.T, s *RDBLogStore, ctx context.Context, filters SearchFilters, dimension RankingDimension) map[string]DimensionRankingWithTrend {
	t.Helper()
	unlimited := 0
	filters.RankingLimit = &unlimited
	res, err := s.GetDimensionRankings(ctx, filters, dimension)
	require.NoError(t, err)
	byID := make(map[string]DimensionRankingWithTrend, len(res.Rankings))
	for _, r := range res.Rankings {
		if r.ID != otherDimensionID {
			byID[r.ID] = r
		}
	}
	return byID
}

// TestDimensionUsageMatchesRankings checks the one-read usage reader returns
// exactly the per-entity totals and trends GetDimensionRankings returns, on
// every rollup dimension the org chart prices, with and without a ceiling.
func TestDimensionUsageMatchesRankings(t *testing.T) {
	usageParityStores(t, func(t *testing.T, s usageParityStore) {
		window := seedUsageLogs(t, s)
		contexts := map[string]context.Context{
			"unbounded":    context.Background(),
			"with ceiling": withCeiling(context.Background(), "business_unit_id", "bu-a", "bu-c"),
		}
		for _, dimension := range []RankingDimension{RankingDimensionBusinessUnit, RankingDimensionTeam, RankingDimensionUser, RankingDimensionCustomer} {
			for name, ctx := range contexts {
				want := rankingsByID(t, s.store, ctx, window, dimension)
				got, err := s.store.GetDimensionUsage(ctx, window, dimension, true)
				require.NoError(t, err)
				require.Lenf(t, got, len(want), "%s %s: same entities", dimension, name)
				for _, entry := range got {
					w, ok := want[entry.ID]
					require.Truef(t, ok, "%s %s: unexpected entity %s", dimension, name, entry.ID)
					assert.Equalf(t, w.TotalRequests, entry.TotalRequests, "%s %s %s requests", dimension, name, entry.ID)
					assert.Equalf(t, w.TotalTokens, entry.TotalTokens, "%s %s %s tokens", dimension, name, entry.ID)
					assert.InDeltaf(t, w.TotalCost, entry.TotalCost, 1e-9, "%s %s %s cost", dimension, name, entry.ID)
					assert.Equalf(t, w.Trend.HasPreviousPeriod, entry.Trend.HasPreviousPeriod, "%s %s %s has previous", dimension, name, entry.ID)
					assert.InDeltaf(t, w.Trend.RequestsTrend, entry.Trend.RequestsTrend, 1e-9, "%s %s %s requests trend", dimension, name, entry.ID)
					assertTrendPtr(t, w.Trend.CostTrend, entry.Trend.CostTrend, "%s %s %s cost trend", dimension, name, entry.ID)
					assertTrendPtr(t, w.Trend.TokensTrend, entry.Trend.TokensTrend, "%s %s %s tokens trend", dimension, name, entry.ID)
				}
			}
		}

		// The seed must actually reach the interesting branches, or parity proves little.
		got, err := s.store.GetDimensionUsage(context.Background(), window, RankingDimensionBusinessUnit, true)
		require.NoError(t, err)
		byID := make(map[string]DimensionUsageEntry, len(got))
		for _, entry := range got {
			byID[entry.ID] = entry
		}
		assert.Equal(t, int64(2), byID["bu-a"].TotalRequests, "array rows credit every unit they carry")
		assert.InDelta(t, 3.75, byID["bu-a"].TotalCost, 1e-9)
		assert.True(t, byID["bu-a"].Trend.HasPreviousPeriod)
		require.NotNil(t, byID["bu-a"].Trend.CostTrend)
		assert.InDelta(t, -6.25, *byID["bu-a"].Trend.CostTrend, 1e-9, "3.75 now against 4 before")
		assert.Equal(t, int64(1), byID["bu-c"].TotalRequests, "scalar-only rows still count")
		assert.Contains(t, byID, unassignedDimensionID, "owner-less rows bucket under Unassigned")
		assert.NotContains(t, byID, "bu-gone", "a unit active only in the previous window is left out")
		assert.False(t, byID["bu-b"].Trend.HasPreviousPeriod)
	})
}

// TestDimensionUsageFilteredOnItsOwnIDsMatchesRankings checks that filtering on
// the dimension's own ids, which the usage reader matches against the
// fanned-out id instead of each row's columns, credits every filtered entity
// exactly what the rankings credit it, and returns no other entity.
func TestDimensionUsageFilteredOnItsOwnIDsMatchesRankings(t *testing.T) {
	usageParityStores(t, func(t *testing.T, s usageParityStore) {
		window := seedUsageLogs(t, s)
		ctx := context.Background()
		for _, tc := range []struct {
			dimension RankingDimension
			ids       []string
			set       func(*SearchFilters, []string)
		}{
			{RankingDimensionBusinessUnit, []string{"bu-a", "bu-c", "bu-none"}, func(f *SearchFilters, ids []string) { f.BusinessUnitIDs = ids }},
			{RankingDimensionTeam, []string{"team-bu-b"}, func(f *SearchFilters, ids []string) { f.TeamIDs = ids }},
		} {
			filtered := window
			tc.set(&filtered, tc.ids)
			want := rankingsByID(t, s.store, ctx, filtered, tc.dimension)
			got, err := s.store.GetDimensionUsage(ctx, filtered, tc.dimension, true)
			require.NoError(t, err)
			returned := map[string]bool{}
			for _, entry := range got {
				returned[entry.ID] = true
				assert.Containsf(t, tc.ids, entry.ID, "%s: only filtered entities are returned", tc.dimension)
				w := want[entry.ID]
				assert.Equalf(t, w.TotalRequests, entry.TotalRequests, "%s %s requests", tc.dimension, entry.ID)
				assert.Equalf(t, w.TotalTokens, entry.TotalTokens, "%s %s tokens", tc.dimension, entry.ID)
				assert.InDeltaf(t, w.TotalCost, entry.TotalCost, 1e-9, "%s %s cost", tc.dimension, entry.ID)
				assert.Equalf(t, w.Trend.HasPreviousPeriod, entry.Trend.HasPreviousPeriod, "%s %s has previous", tc.dimension, entry.ID)
				assertTrendPtr(t, w.Trend.CostTrend, entry.Trend.CostTrend, "%s %s cost trend", tc.dimension, entry.ID)
			}
			for _, id := range tc.ids {
				_, ranked := want[id]
				assert.Equalf(t, ranked, returned[id], "%s %s: returned exactly when ranked", tc.dimension, id)
			}
		}
	})
}

// TestDimensionUsageWithoutCompareReadsOnlyTheWindow checks compare=false keeps
// the totals and leaves every trend empty.
func TestDimensionUsageWithoutCompareReadsOnlyTheWindow(t *testing.T) {
	usageParityStores(t, func(t *testing.T, s usageParityStore) {
		window := seedUsageLogs(t, s)
		ctx := context.Background()
		want := rankingsByID(t, s.store, ctx, window, RankingDimensionBusinessUnit)
		got, err := s.store.GetDimensionUsage(ctx, window, RankingDimensionBusinessUnit, false)
		require.NoError(t, err)
		require.Len(t, got, len(want))
		for _, entry := range got {
			assert.Equal(t, want[entry.ID].TotalRequests, entry.TotalRequests, entry.ID)
			assert.InDelta(t, want[entry.ID].TotalCost, entry.TotalCost, 1e-9, entry.ID)
			assert.Equal(t, DimensionRankingTrend{}, entry.Trend, entry.ID)
		}
	})
}

// TestDimensionUsageRejectsDimensionsWithoutAnIDColumn checks the JSON-field
// and comma-list dimensions are refused instead of grouped wrongly.
func TestDimensionUsageRejectsDimensionsWithoutAnIDColumn(t *testing.T) {
	store := newTestSQLiteStore(t)
	for _, dimension := range []RankingDimension{RankingDimensionRoutingEngine, RankingDimensionToolCallName, "not_a_dimension"} {
		_, err := store.GetDimensionUsage(context.Background(), SearchFilters{}, dimension, true)
		assert.Error(t, err, dimension)
		_, err = store.GetDimensionModelUsage(context.Background(), SearchFilters{}, dimension)
		assert.Error(t, err, dimension)
	}
}

// TestDimensionModelUsageMatchesPerEntityModelRankings checks one grouped read
// gives every entity the same (model, provider) totals that one
// GetModelRankings call per entity gives it.
func TestDimensionModelUsageMatchesPerEntityModelRankings(t *testing.T) {
	usageParityStores(t, func(t *testing.T, s usageParityStore) {
		window := seedUsageLogs(t, s)
		ctx := context.Background()
		ids := []string{"bu-a", "bu-b", "bu-c"}
		batched := window
		batched.BusinessUnitIDs = ids
		got, err := s.store.GetDimensionModelUsage(ctx, batched, RankingDimensionBusinessUnit)
		require.NoError(t, err)

		type key struct{ id, model, provider string }
		gotByKey := map[key]DimensionModelUsageEntry{}
		for _, entry := range got {
			gotByKey[key{entry.ID, entry.Model, entry.Provider}] = entry
		}
		wantKeys := 0
		for _, id := range ids {
			single := window
			single.BusinessUnitIDs = []string{id}
			models, err := s.store.GetModelRankings(ctx, single)
			require.NoError(t, err)
			for _, row := range models.Rankings {
				wantKeys++
				entry, ok := gotByKey[key{id, row.Model, row.Provider}]
				require.Truef(t, ok, "%s: missing %s/%s", id, row.Provider, row.Model)
				assert.Equalf(t, row.TotalRequests, entry.TotalRequests, "%s %s requests", id, row.Model)
				assert.InDeltaf(t, row.TotalCost, entry.TotalCost, 1e-9, "%s %s cost", id, row.Model)
			}
		}
		requested := map[string]bool{"bu-a": true, "bu-b": true, "bu-c": true}
		inPage := 0
		for k := range gotByKey {
			if requested[k.id] {
				inPage++
			}
		}
		assert.Equal(t, wantKeys, inPage, "no extra groups for the requested units")
		assert.Equal(t, inPage, len(gotByKey), "only the requested units are returned")
		assert.Contains(t, gotByKey, key{"bu-a", "claude", "anthropic"})
	})
}

// TestMCPToolCountsByDimensionMatchesTopTools checks one grouped read gives every
// entity the distinct tool count that one GetMCPTopTools call per entity gives.
func TestMCPToolCountsByDimensionMatchesTopTools(t *testing.T) {
	usageParityStores(t, func(t *testing.T, s usageParityStore) {
		now := time.Now().UTC().Truncate(time.Second)
		start, end := now.Add(-time.Hour), now.Add(time.Hour)
		n := 0
		add := func(unit, tool, status string, ts time.Time) {
			n++
			id := fmt.Sprintf("mcp-%d", n)
			s.createMCP(t, &MCPToolLog{ID: id, RequestID: id, Timestamp: ts, CreatedAt: ts, ToolName: tool, Status: status, BusinessUnitID: &unit})
		}
		add("bu-a", "search", "success", now)
		add("bu-a", "search", "success", now)
		add("bu-a", "fetch", "error", now)
		add("bu-a", "write", "processing", now)
		add("bu-a", "old", "success", now.Add(-3*time.Hour))
		add("bu-b", "search", "success", now)
		add("bu-c", "search", "success", now)

		ctx := context.Background()
		ids := []string{"bu-a", "bu-b"}
		filters := MCPToolLogSearchFilters{StartTime: &start, EndTime: &end, BusinessUnitIDs: ids}
		got, err := s.store.GetMCPToolCountsByDimension(ctx, filters, RankingDimensionBusinessUnit)
		require.NoError(t, err)
		for _, id := range ids {
			single := filters
			single.BusinessUnitIDs = []string{id}
			top, err := s.store.GetMCPTopTools(ctx, single, 100)
			require.NoError(t, err)
			assert.Equal(t, len(top.Tools), got[id], id)
		}
		assert.Equal(t, 2, got["bu-a"], "processing and out-of-window calls are not counted")
		assert.NotContains(t, got, "bu-c", "units outside the filter are not read")

		_, err = s.store.GetMCPToolCountsByDimension(ctx, filters, RankingDimensionVirtualKey)
		assert.Error(t, err, "MCP tool logs carry no virtual key dimension reader")
	})
}

// assertTrendPtr compares two nil-able trends: both nil, or both set and equal.
func assertTrendPtr(t *testing.T, want, got *float64, msg string, args ...any) {
	t.Helper()
	if want == nil {
		assert.Nilf(t, got, msg, args...)
		return
	}
	if assert.NotNilf(t, got, msg, args...) {
		assert.InDeltaf(t, *want, *got, 1e-9, msg, args...)
	}
}

// TestDimensionUsageMalformedArraysMatchRankings checks the ids-only Postgres
// fan-out makes the same array-or-scalar choice as the shared fan-out for values
// the Go writer never produces: malformed JSON, an empty array, a JSON object,
// and an array next to a scalar.
func TestDimensionUsageMalformedArraysMatchRankings(t *testing.T) {
	stores := map[string]func(t *testing.T) (*RDBLogStore, *gorm.DB){
		"sqlite": newFanoutTestStore,
		"postgres": func(t *testing.T) (*RDBLogStore, *gorm.DB) {
			return setupPerfTestDB(t)
		},
	}
	for name, open := range stores {
		t.Run(name, func(t *testing.T) {
			store, db := open(t)
			now := time.Now().UTC()
			for _, row := range []struct{ id, scalar, array string }{
				{"bad-1", "bu-x", `not json`},
				{"bad-2", "bu-x", `[]`},
				{"bad-3", "bu-y", `{"bu-z": 1}`},
				{"bad-4", "bu-y", `["bu-z","bu-x"]`},
				{"bad-5", "", `["bu-z",""]`},
				{"bad-6", "", ""},
			} {
				insertDimensionLog(t, db, "business_unit_id", row.id, now, row.scalar, "", row.array, row.array)
			}
			window := fanoutWindow(now)
			for _, ids := range [][]string{nil, {"bu-x", "bu-z"}} {
				filtered := window
				filtered.BusinessUnitIDs = ids
				want := rankingsByID(t, store, context.Background(), filtered, RankingDimensionBusinessUnit)
				got, err := store.GetDimensionUsage(context.Background(), filtered, RankingDimensionBusinessUnit, false)
				require.NoError(t, err)
				require.NotEmpty(t, got)
				for _, entry := range got {
					assert.Equalf(t, want[entry.ID].TotalRequests, entry.TotalRequests, "ids %v: %s", ids, entry.ID)
				}
				if ids == nil {
					assert.Len(t, got, len(want), "every ranked entity is returned without an id filter")
				}
			}
		})
	}
}
