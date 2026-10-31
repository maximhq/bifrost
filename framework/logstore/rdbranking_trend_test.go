package logstore

import (
	"context"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestMetricTrend(t *testing.T) {
	assert.Nil(t, metricTrend(0, 5), "zero to nonzero has no percentage")
	require.NotNil(t, metricTrend(0, 0))
	assert.Zero(t, *metricTrend(0, 0), "zero to zero is genuinely unchanged")
	require.NotNil(t, metricTrend(10, 15))
	assert.InDelta(t, 50.0, *metricTrend(10, 15), 0.001)
	require.NotNil(t, metricTrend(10, 0))
	assert.InDelta(t, -100.0, *metricTrend(10, 0), 0.001)
}

// trendRow is one entity's traffic in the previous and current hour. Each
// entity gets its own team, user and model so every ranking sees it alone.
type trendRow struct {
	id                    string
	prevTokens, curTokens int
	prevCost, curCost     float64
}

// newRankingTrendTestStore seeds one request per entity in each of the previous
// and current hour, and returns the filters for the current hour.
func newRankingTrendTestStore(t *testing.T, rows []trendRow) (*RDBLogStore, SearchFilters) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Log{}))

	now := time.Now().UTC()
	insert := func(logID, entity string, ts time.Time, tokens int, cost float64) {
		id := entity
		require.NoError(t, db.Create(&Log{
			ID:          logID,
			Timestamp:   ts,
			Status:      "success",
			Provider:    "openai",
			Model:       entity,
			TeamID:      &id,
			UserID:      &id,
			TotalTokens: tokens,
			Cost:        &cost,
		}).Error)
	}
	for _, r := range rows {
		insert(r.id+"-prev", r.id, now.Add(-90*time.Minute), r.prevTokens, r.prevCost)
		insert(r.id+"-cur", r.id, now.Add(-5*time.Minute), r.curTokens, r.curCost)
	}

	start := now.Add(-time.Hour)
	return &RDBLogStore{db: db, logger: bifrost.NewDefaultLogger(schemas.LogLevelInfo)}, SearchFilters{StartTime: &start, EndTime: &now}
}

// A previous period that had requests but no tokens or cost - cache hits, a
// free model - gives the current period nothing to be a percentage of. Reporting
// 0% there would claim the metric held steady when it went from nothing to
// something; the trend must be null instead, while has_previous_period stays
// true because the request history is real.
func TestRankingTrendsZeroBaseline(t *testing.T) {
	rows := []trendRow{
		{id: "from-zero", prevTokens: 0, curTokens: 100, prevCost: 0, curCost: 0.5},
		{id: "flat", prevTokens: 100, curTokens: 100, prevCost: 0.5, curCost: 0.5},
		{id: "still-zero", prevTokens: 0, curTokens: 0, prevCost: 0, curCost: 0},
	}
	s, filters := newRankingTrendTestStore(t, rows)
	ctx := context.Background()

	check := func(t *testing.T, id string, has bool, requests float64, tokens, cost *float64) {
		t.Helper()
		assert.True(t, has, "%s: request history is present in both periods", id)
		assert.Zero(t, requests, "%s: one request in each period", id)
		switch id {
		case "from-zero":
			assert.Nil(t, tokens, "zero-to-nonzero tokens must not read as 0%")
			assert.Nil(t, cost, "zero-to-nonzero cost must not read as 0%")
		case "flat", "still-zero":
			require.NotNil(t, tokens, "%s: tokens trend is a real 0%%", id)
			require.NotNil(t, cost, "%s: cost trend is a real 0%%", id)
			assert.Zero(t, *tokens)
			assert.Zero(t, *cost)
		}
	}

	t.Run("dimension", func(t *testing.T) {
		res, err := s.GetDimensionRankings(ctx, filters, RankingDimensionTeam)
		require.NoError(t, err)
		require.Len(t, res.Rankings, len(rows))
		for _, r := range res.Rankings {
			check(t, r.ID, r.Trend.HasPreviousPeriod, r.Trend.RequestsTrend, r.Trend.TokensTrend, r.Trend.CostTrend)
		}
	})
	t.Run("user", func(t *testing.T) {
		res, err := s.GetUserRankings(ctx, filters)
		require.NoError(t, err)
		require.Len(t, res.Rankings, len(rows))
		for _, r := range res.Rankings {
			check(t, r.UserID, r.Trend.HasPreviousPeriod, r.Trend.RequestsTrend, r.Trend.TokensTrend, r.Trend.CostTrend)
		}
	})
	t.Run("model", func(t *testing.T) {
		res, err := s.GetModelRankings(ctx, filters)
		require.NoError(t, err)
		require.Len(t, res.Rankings, len(rows))
		for _, r := range res.Rankings {
			check(t, r.Model, r.Trend.HasPreviousPeriod, r.Trend.RequestsTrend, r.Trend.TokensTrend, r.Trend.CostTrend)
		}
	})
}

// Routing rule, provider key, alias and complexity tier are all filters on the
// Logs page, so "how much went through this rule" could be checked one rule at a
// time and "which rule takes the most traffic" could not be asked at all. Each
// ranks like any other single-owner dimension: rows that never had one are left
// out rather than bucketed, and the tool's own filters still apply.
func TestRoutingDimensionsRank(t *testing.T) {
	store, _ := newFanoutTestStore(t)
	ctx := context.Background()
	base := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	row := func(id, provider, rule, ruleName, key, keyName, alias, tier, mechanism string, cost float64) {
		opt := func(v string) *string {
			if v == "" {
				return nil
			}
			return &v
		}
		require.NoError(t, store.Create(ctx, &Log{
			ID: id, Timestamp: base, CreatedAt: base, Object: "chat_completion", Provider: provider, Model: "m",
			Status: "success", Cost: &cost, TotalTokens: 10,
			RoutingRuleID: opt(rule), RoutingRuleName: opt(ruleName), SelectedKeyID: key, SelectedKeyName: keyName,
			Alias: opt(alias), ComplexityTier: opt(tier), ComplexityMechanism: opt(mechanism),
		}))
	}
	row("a", "anthropic", "rule-premium", "Premium tier", "key-1", "anthropic-primary", "smart", "COMPLEX", "semantic", 3)
	row("b", "anthropic", "rule-premium", "Premium tier", "key-1", "anthropic-primary", "smart", "COMPLEX", "semantic", 2)
	row("c", "openai", "rule-budget", "Budget tier", "key-2", "openai-primary", "fast", "SIMPLE", "llm", 1)
	row("d", "openai", "", "", "key-2", "openai-primary", "", "", "", 1)

	start, end := base.Add(-time.Hour), base.Add(time.Hour)
	filters := SearchFilters{StartTime: &start, EndTime: &end}
	ranked := func(dimension RankingDimension, f SearchFilters) map[string]DimensionRankingWithTrend {
		res, err := store.GetDimensionRankings(ctx, f, dimension)
		require.NoError(t, err, "dimension %s", dimension)
		return rankingByID(res)
	}

	rules := ranked(RankingDimensionRoutingRule, filters)
	require.Len(t, rules, 2, "the request no rule handled names no rule")
	assert.Equal(t, int64(2), rules["rule-premium"].TotalRequests)
	assert.Equal(t, "Premium tier", rules["rule-premium"].Name, "a rule is reported by its name, not only its id")
	assert.InDelta(t, 5.0, rules["rule-premium"].TotalCost, 1e-9)

	keys := ranked(RankingDimensionSelectedKey, filters)
	assert.Equal(t, int64(2), keys["key-2"].TotalRequests)
	assert.Equal(t, "openai-primary", keys["key-2"].Name)

	assert.Equal(t, int64(2), ranked(RankingDimensionAlias, filters)["smart"].TotalRequests)
	assert.Equal(t, int64(1), ranked(RankingDimensionComplexityTier, filters)["SIMPLE"].TotalRequests)
	assert.Equal(t, int64(2), ranked(RankingDimensionComplexityMechanism, filters)["semantic"].TotalRequests)

	// Composes with filters: which rules send traffic to Anthropic.
	toAnthropic := filters
	toAnthropic.Providers = []string{"anthropic"}
	assert.Len(t, ranked(RankingDimensionRoutingRule, toAnthropic), 1)

	// The complexity columns are not in the hourly matview. Read from it, they
	// would raise a shape error and trip the matview self-heal for a query that
	// was never the matview's to serve.
	for _, dimension := range []RankingDimension{RankingDimensionComplexityTier, RankingDimensionComplexityMechanism} {
		assert.True(t, dimensionColumns[dimension].RawOnly, "%s must not use the matview path", dimension)
	}
	// Ranked for in-process callers without widening the HTTP rankings endpoint.
	assert.False(t, ValidRankingDimensions[RankingDimensionRoutingRule])
}

// Routing engines and tool-call names are comma-separated on the row, because a
// request can pass through several engines and call several tools. A request
// that called two tools counts under both, so the attributed total can exceed
// the requests there actually were - the same relationship fail_reason has.
func TestCommaListDimensionsRank(t *testing.T) {
	store, _ := newFanoutTestStore(t)
	ctx := context.Background()
	base := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	// Set through the slice fields: the write hook joins them into the columns,
	// which is the only way they are ever populated.
	row := func(id string, ts time.Time, tools, engines []string, cost float64) {
		require.NoError(t, store.Create(ctx, &Log{
			ID: id, Timestamp: ts, CreatedAt: ts, Object: "chat_completion", Provider: "openai", Model: "m",
			Status: "success", Cost: &cost, TotalTokens: 10,
			ToolCallNames: tools, RoutingEnginesUsed: engines,
		}))
	}
	row("a", base, []string{"get_weather", "search_docs"}, []string{"routing-rule", "loadbalancing"}, 2)
	row("b", base, []string{"get_weather"}, []string{"governance"}, 1)
	row("c", base, []string{" search_docs ", ""}, []string{"routing-rule"}, 1)
	row("d", base, nil, nil, 5)
	// The period before, for the trend: one get_weather call.
	row("p", base.Add(-2*time.Hour), []string{"get_weather"}, []string{"governance"}, 1)

	start, end := base.Add(-time.Hour), base.Add(time.Hour)
	filters := SearchFilters{StartTime: &start, EndTime: &end}

	res, err := store.GetDimensionRankings(ctx, filters, RankingDimensionToolCallName)
	require.NoError(t, err)
	tools := rankingByID(res)
	require.Len(t, tools, 2, "the row with no tool call, and the stray empty element, name nothing")
	assert.Equal(t, int64(2), tools["get_weather"].TotalRequests)
	assert.Equal(t, int64(2), tools["search_docs"].TotalRequests)
	assert.InDelta(t, 3.0, tools["get_weather"].TotalCost, 1e-9, "a request's cost counts under each tool it called")
	assert.Equal(t, int64(4), res.TotalActualRequests)
	assert.Equal(t, int64(4), res.TotalAttributedRequests, "a and b and c, with a counted twice")
	assert.True(t, tools["get_weather"].Trend.HasPreviousPeriod)
	assert.InDelta(t, 100.0, tools["get_weather"].Trend.RequestsTrend, 1e-9)
	assert.False(t, tools["search_docs"].Trend.HasPreviousPeriod)

	engines, err := store.GetDimensionRankings(ctx, filters, RankingDimensionRoutingEngine)
	require.NoError(t, err)
	require.NotEmpty(t, engines.Rankings)
	assert.Equal(t, "routing-rule", engines.Rankings[0].ID, "most requests first")
	assert.Equal(t, int64(2), engines.Rankings[0].TotalRequests)

	// The cap applies to the names, after splitting - not to the raw combinations.
	one := 1
	capped := filters
	capped.RankingLimit = &one
	res, err = store.GetDimensionRankings(ctx, capped, RankingDimensionRoutingEngine)
	require.NoError(t, err)
	assert.Len(t, res.Rankings, 1)
}
