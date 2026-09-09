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
