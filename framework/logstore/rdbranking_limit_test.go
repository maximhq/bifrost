package logstore

import (
	"context"
	"fmt"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// newRankingLimitTestStore seeds `teams` teams with one log each, so the ranking
// row count is exactly the number of teams unless a cap applies.
func newRankingLimitTestStore(t *testing.T, teams int) *RDBLogStore {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Log{}))

	now := time.Now()
	for i := range teams {
		teamID := fmt.Sprintf("team-%03d", i)
		require.NoError(t, db.Create(&Log{
			ID:          teamID,
			Timestamp:   now,
			Status:      "success",
			Provider:    "openai",
			Model:       "gpt-4",
			TeamID:      &teamID,
			TotalTokens: 10,
		}).Error)
	}

	return &RDBLogStore{db: db, logger: bifrost.NewDefaultLogger(schemas.LogLevelInfo)}
}

// The rankings endpoints cap rows at defaultMaxRankingsLimit unless the caller
// asks for a different cap (dashboard) or for everything (export).
func TestGetDimensionRankingsRespectsRankingLimit(t *testing.T) {
	const totalTeams = defaultMaxRankingsLimit + 15
	s := newRankingLimitTestStore(t, totalTeams)
	ctx := context.Background()

	limit := func(n int) *int { return &n }

	cases := []struct {
		name     string
		limit    *int
		expected int
	}{
		{"default caps at 100", nil, defaultMaxRankingsLimit},
		{"explicit limit", limit(10), 10},
		{"limit above the row count returns everything", limit(totalTeams + 50), totalTeams},
		{"all=true returns everything", limit(0), totalTeams},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := s.GetDimensionRankings(ctx, SearchFilters{RankingLimit: tc.limit}, RankingDimensionTeam)
			require.NoError(t, err)
			require.Len(t, res.Rankings, tc.expected)
		})
	}
}

func TestGetModelRankingsRespectsRankingLimit(t *testing.T) {
	s := newRankingLimitTestStore(t, defaultMaxRankingsLimit+15)
	ctx := context.Background()

	// Every seeded row shares one model+provider pair, so a cap is only
	// observable through the limit actually reaching the query.
	unlimited := 0
	res, err := s.GetModelRankings(ctx, SearchFilters{RankingLimit: &unlimited})
	require.NoError(t, err)
	require.Len(t, res.Rankings, 1)

	one := 1
	res, err = s.GetModelRankings(ctx, SearchFilters{RankingLimit: &one})
	require.NoError(t, err)
	require.Len(t, res.Rankings, 1)
}

// TestModelRankingsManyPairsSQLite pins that an unlimited model ranking over
// more (model, provider) pairs than SQLite's expression depth allows still
// answers, with trends. The previous-period query once filtered with one OR
// term per pair, which SQLite rejects past about 1,000 terms; above a modest
// count the filter is dropped, since previous rows are matched to the current
// ones by pair anyway. So each pair's trend must come from its own previous
// rows, and a pair seen only in the previous period must not be ranked.
func TestModelRankingsManyPairsSQLite(t *testing.T) {
	store := newTestSQLiteStore(t)
	defer store.Close(context.Background())
	now := time.Now().UTC().Truncate(time.Second)
	const models = 1200
	insert := `
		INSERT INTO logs (id, timestamp, object_type, provider, model, status, latency, total_tokens, cost, created_at)
		VALUES (?, ?, 'chat.completion', 'openai', ?, 'success', 100, 10, 0.01, ?)
	`
	// prevRequests gives the pairs distinct previous-period counts (1 to 4).
	prevRequests := func(i int) int { return 1 + i%4 }
	tx := store.db.Begin()
	for i := 0; i < models; i++ {
		model := fmt.Sprintf("model-%04d", i)
		cur := now.Add(-30 * time.Minute)
		require.NoError(t, tx.Exec(insert, fmt.Sprintf("rp-%d-cur", i), cur, model, cur).Error)
		for j := 0; j < prevRequests(i); j++ {
			prev := now.Add(-90 * time.Minute)
			require.NoError(t, tx.Exec(insert, fmt.Sprintf("rp-%d-prev-%d", i, j), prev, model, prev).Error)
		}
	}
	// A pair with traffic only in the previous period.
	for j := 0; j < 3; j++ {
		prev := now.Add(-90 * time.Minute)
		require.NoError(t, tx.Exec(insert, fmt.Sprintf("rp-ghost-%d", j), prev, "model-ghost", prev).Error)
	}
	require.NoError(t, tx.Commit().Error)

	start, end := now.Add(-time.Hour), now
	all := 0
	res, err := store.GetModelRankings(context.Background(), SearchFilters{StartTime: &start, EndTime: &end, RankingLimit: &all})
	require.NoError(t, err, "a ranking over many pairs still answers")
	require.Len(t, res.Rankings, models, "only pairs with current traffic are ranked")
	for _, r := range res.Rankings {
		require.NotEqual(t, "model-ghost", r.Model, "a previous-only pair is not ranked")
		require.Equal(t, int64(1), r.TotalRequests)
		var i int
		_, err := fmt.Sscanf(r.Model, "model-%04d", &i)
		require.NoError(t, err)
		require.True(t, r.Trend.HasPreviousPeriod, "%s has a previous period", r.Model)
		want := pctChange(float64(prevRequests(i)), 1)
		require.InDelta(t, want, r.Trend.RequestsTrend, 0.001, "%s trend comes from its own previous rows", r.Model)
	}
}
