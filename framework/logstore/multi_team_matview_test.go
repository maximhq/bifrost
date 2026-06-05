package logstore

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/maximhq/bifrost/framework/queryscope"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// insertTeamBULog inserts a single log row carrying the scalar and/or JSON-array
// team / business-unit columns. Empty-string args are stored as NULL so the
// matview's scalar-vs-array branch selection (team_ids IS JSON ARRAY) is
// exercised faithfully — a row with no array column must fall back to the scalar.
func insertTeamBULog(t *testing.T, db *gorm.DB, ts time.Time,
	userID, teamID, teamName, teamIDs, teamNames, buID, buName, buIDs, buNames string) {
	t.Helper()
	nz := func(s string) any {
		if s == "" {
			return nil
		}
		return s
	}
	err := db.Exec(`
		INSERT INTO logs (id, timestamp, object_type, provider, model, status,
			user_id, team_id, team_name, team_ids, team_names,
			business_unit_id, business_unit_name, business_unit_ids, business_unit_names,
			created_at, latency, cost, prompt_tokens, completion_tokens, total_tokens)
		VALUES (?, ?, 'chat_completion', 'openai', 'gpt-4', 'success',
			?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 100, 0.01, 10, 5, 15)
	`, uuid.New().String(), ts, nz(userID), nz(teamID), nz(teamName), nz(teamIDs), nz(teamNames),
		nz(buID), nz(buName), nz(buIDs), nz(buNames), ts).Error
	require.NoError(t, err, "failed to insert team/BU test log")
}

func keyPairsByID(pairs []KeyPairResult) map[string]string {
	byID := make(map[string]string, len(pairs))
	for _, p := range pairs {
		byID[p.ID] = p.Name
	}
	return byID
}

// TestFilterTeamMatView_CollectsScalarAndArray is the regression for the reported
// bug: the team filter dropdown read mv_filter_teams, which only projected the
// scalar team_id/team_name. Teams that exist only in the JSON-array team_ids
// (enterprise user/AP path) were missing. The recreated view must surface both.
func TestFilterTeamMatView_CollectsScalarAndArray(t *testing.T) {
	store, db := setupPerfTestDB(t)
	store.matViewsReady.Store(true) // force the matview read path
	ctx := context.Background()
	now := time.Now().UTC()

	// Old / VK-team log: scalar team only, no JSON array.
	insertTeamBULog(t, db, now, "u-1", "t-scalar", "Scalar Team", "", "", "", "", "", "")
	// Enterprise log: teams only in the JSON array, scalar team NULL.
	insertTeamBULog(t, db, now, "u-1", "", "", `["t-arr1","t-arr2"]`, `["Array One","Array Two"]`, "", "", "", "")
	refreshTestMatViews(t, db)

	pairs, err := store.GetDistinctKeyPairs(ctx, "team_id", "team_name", 1000, "")
	require.NoError(t, err)
	byID := keyPairsByID(pairs)

	assert.Equal(t, "Scalar Team", byID["t-scalar"], "scalar-only team must still appear (backward compatibility)")
	assert.Equal(t, "Array One", byID["t-arr1"], "array-only team must now appear")
	assert.Equal(t, "Array Two", byID["t-arr2"], "second array team must appear, name aligned by ordinality")
}

// TestFilterBusinessUnitMatView_CollectsScalarAndArray mirrors the team test for
// the business-unit dropdown (mv_filter_business_units), which had the identical
// scalar-only bug.
func TestFilterBusinessUnitMatView_CollectsScalarAndArray(t *testing.T) {
	store, db := setupPerfTestDB(t)
	store.matViewsReady.Store(true)
	ctx := context.Background()
	now := time.Now().UTC()

	insertTeamBULog(t, db, now, "u-1", "", "", "", "", "bu-scalar", "Scalar BU", "", "")
	insertTeamBULog(t, db, now, "u-1", "", "", "", "", "", "", `["bu-arr1"]`, `["BU One"]`)
	refreshTestMatViews(t, db)

	pairs, err := store.GetDistinctKeyPairs(ctx, "business_unit_id", "business_unit_name", 1000, "")
	require.NoError(t, err)
	byID := keyPairsByID(pairs)

	assert.Equal(t, "Scalar BU", byID["bu-scalar"], "scalar-only business unit must still appear")
	assert.Equal(t, "BU One", byID["bu-arr1"], "array-only business unit must now appear")
}

// TestDimensionRankings_ActualVsAttributedTotals pins the semantics of the
// server-side totals: a request attributed to two teams counts once in
// TotalActualRequests (COUNT(DISTINCT id)) and twice in
// TotalAttributedRequests (COUNT(*) over the fan-out), while the per-team
// rows stay accurate per team.
func TestDimensionRankings_ActualVsAttributedTotals(t *testing.T) {
	store, db := setupPerfTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()

	// One request attributed to two teams + one scalar single-team request:
	// 2 actual requests, 3 attributed team-credits.
	insertTeamBULog(t, db, now, "u-1", "", "", `["t-a","t-b"]`, `["Team A","Team B"]`, "", "", "", "")
	insertTeamBULog(t, db, now, "u-1", "t-a", "Team A", "", "", "", "", "", "")

	start := now.Add(-time.Hour)
	end := now.Add(time.Hour)
	res, err := store.GetDimensionRankings(ctx, SearchFilters{StartTime: &start, EndTime: &end}, RankingDimensionTeam)
	require.NoError(t, err)

	assert.Equal(t, int64(2), res.TotalActualRequests, "actual must count distinct requests, not fan-out rows")
	assert.Equal(t, int64(3), res.TotalAttributedRequests, "attributed must credit a request once per team it touches")

	requestsByID := make(map[string]int64, len(res.Rankings))
	for _, r := range res.Rankings {
		requestsByID[r.ID] = r.TotalRequests
	}
	assert.Equal(t, int64(2), requestsByID["t-a"], "t-a is touched by both requests (array + scalar fallback)")
	assert.Equal(t, int64(1), requestsByID["t-b"], "t-b is touched only by the multi-team request")
}

// TestFilterTeamMatView_DACScopeAppliesAfterFanout proves the visibility columns
// (user_id) survive the fan-out: each fanned-out team row carries its source
// log's user_id, so a QueryScope still both resolves (no "column does not exist")
// and filters — an array team owned by another user must not leak.
func TestFilterTeamMatView_DACScopeAppliesAfterFanout(t *testing.T) {
	store, db := setupPerfTestDB(t)
	store.matViewsReady.Store(true)
	now := time.Now().UTC()

	// Array team owned by u-secret; scalar team visible to u-visible.
	insertTeamBULog(t, db, now, "u-secret", "", "", `["t-secret"]`, `["Secret Team"]`, "", "", "", "")
	insertTeamBULog(t, db, now, "u-visible", "t-public", "Public Team", "", "", "", "", "", "")
	refreshTestMatViews(t, db)

	scope := queryscope.QueryScope(func(db *gorm.DB) *gorm.DB {
		return db.Where("user_id = ?", "u-visible")
	})
	ctx := queryscope.WithQueryScope(context.Background(), scope)

	pairs, err := store.GetDistinctKeyPairs(ctx, "team_id", "team_name", 1000, "")
	require.NoError(t, err, "scope WHERE must resolve against the fanned-out matview columns")
	byID := keyPairsByID(pairs)

	assert.Equal(t, "Public Team", byID["t-public"], "team visible to the scoped user must appear")
	_, leaked := byID["t-secret"]
	assert.False(t, leaked, "array team owned by another user must be filtered out by DAC scope")
}
