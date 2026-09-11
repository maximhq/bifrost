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
