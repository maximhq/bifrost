package logstore

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestModelRoutingColumnsFilter pins that a log records the model router that
// took it, and that the router filters the log list from the raw table rather
// than the hourly view, which does not carry it.
func TestModelRoutingColumnsFilter(t *testing.T) {
	store, _ := newFanoutTestStore(t)
	ctx := context.Background()
	base := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	row := func(id, router, routerName, tier string) {
		opt := func(v string) *string {
			if v == "" {
				return nil
			}
			return &v
		}
		cost := 1.0
		require.NoError(t, store.Create(ctx, &Log{
			ID: id, Timestamp: base, CreatedAt: base, Object: "chat_completion", Provider: "openai", Model: "m",
			Status: "success", Cost: &cost, TotalTokens: 10,
			ModelRouterID: opt(router), ModelRouterName: opt(routerName), ComplexityTier: opt(tier),
		}))
	}
	row("a", "support", "Support", "SIMPLE")
	row("b", "support", "Support", "COMPLEX")
	row("c", "general", "General", "SIMPLE")
	row("d", "", "", "")

	start, end := base.Add(-time.Hour), base.Add(time.Hour)
	filters := SearchFilters{StartTime: &start, EndTime: &end}
	search := func(f SearchFilters) []string {
		res, err := store.SearchLogs(ctx, f, PaginationOptions{Limit: 10})
		require.NoError(t, err)
		ids := make([]string, 0, len(res.Logs))
		for _, l := range res.Logs {
			ids = append(ids, l.ID)
		}
		return ids
	}

	byRouter := filters
	byRouter.ModelRouterIDs = []string{"support"}
	assert.ElementsMatch(t, []string{"a", "b"}, search(byRouter))

	res, err := store.SearchLogs(ctx, byRouter, PaginationOptions{Limit: 1})
	require.NoError(t, err)
	require.Len(t, res.Logs, 1)
	got := res.Logs[0]
	require.NotNil(t, got.ModelRouterName)
	assert.Equal(t, "Support", *got.ModelRouterName)

	assert.False(t, canUseMatViewFilters(byRouter), "a router filter must not be served from the hourly view")

	// The Model Routers page ranks requests by router.
	assert.True(t, ValidRankingDimensions[RankingDimensionModelRouter], "the rankings endpoint accepts model_router")
	rankings, err := store.GetDimensionRankings(ctx, filters, RankingDimensionModelRouter)
	require.NoError(t, err)
	counts := make(map[string]int64, len(rankings.Rankings))
	names := make(map[string]string, len(rankings.Rankings))
	for _, entry := range rankings.Rankings {
		counts[entry.ID], names[entry.ID] = entry.TotalRequests, entry.Name
	}
	assert.Equal(t, map[string]int64{"support": 2, "general": 1}, counts, "a request no router took is not ranked")
	assert.Equal(t, "Support", names["support"])

	// A router's page splits its requests by tier: only that router's rows count.
	assert.True(t, ValidRankingDimensions[RankingDimensionComplexityTier], "the rankings endpoint accepts complexity_tier")
	tiers, err := store.GetDimensionRankings(ctx, byRouter, RankingDimensionComplexityTier)
	require.NoError(t, err)
	tierCounts := make(map[string]int64, len(tiers.Rankings))
	for _, entry := range tiers.Rankings {
		tierCounts[entry.ID] = entry.TotalRequests
	}
	assert.Equal(t, map[string]int64{"SIMPLE": 1, "COMPLEX": 1}, tierCounts, "another router's tiers are not counted")
}
