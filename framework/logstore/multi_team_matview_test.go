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

// TestDimensionRankings_SingleOwnerAdditive pins the additive attribution
// semantics for the org-rollup dimensions: each request is credited to exactly
// one owner — the scalar team_id — and requests with no scalar owner (e.g. the
// enterprise user/AP path that only populates the JSON array) fall into a
// synthetic "Unassigned" bucket rather than being fanned out to every team they
// touch. This keeps the per-team spend additive so the Teams tab reconciles to
// the org total, and TotalActualRequests == TotalAttributedRequests.
func TestDimensionRankings_SingleOwnerAdditive(t *testing.T) {
	store, db := setupPerfTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()

	// Row 1: array-only multi-team request with no scalar owner -> Unassigned.
	// Row 2: single-team request with a scalar owner -> t-a.
	// Additive: 2 requests total, each counted once.
	insertTeamBULog(t, db, now, "u-1", "", "", `["t-a","t-b"]`, `["Team A","Team B"]`, "", "", "", "")
	insertTeamBULog(t, db, now, "u-1", "t-a", "Team A", "", "", "", "", "", "")

	start := now.Add(-time.Hour)
	end := now.Add(time.Hour)
	res, err := store.GetDimensionRankings(ctx, SearchFilters{StartTime: &start, EndTime: &end}, RankingDimensionTeam)
	require.NoError(t, err)

	assert.Equal(t, int64(2), res.TotalActualRequests, "actual counts every terminal request, including Unassigned")
	assert.Equal(t, int64(2), res.TotalAttributedRequests, "single-owner attribution is additive: attributed == actual")

	requestsByID := make(map[string]int64, len(res.Rankings))
	namesByID := make(map[string]string, len(res.Rankings))
	var summedRequests int64
	for _, r := range res.Rankings {
		requestsByID[r.ID] = r.TotalRequests
		namesByID[r.ID] = r.Name
		summedRequests += r.TotalRequests
	}
	assert.Equal(t, int64(1), requestsByID["t-a"], "t-a owns exactly its one scalar-attributed request")
	assert.Equal(t, int64(1), requestsByID[unassignedDimensionID], "the array-only request with no scalar owner is Unassigned")
	assert.Equal(t, unassignedDimensionName, namesByID[unassignedDimensionID], "the unassigned bucket gets a stable display name")
	_, hasTB := requestsByID["t-b"]
	assert.False(t, hasTB, "no fan-out: t-b is never credited a request it doesn't scalar-own")
	assert.Equal(t, res.TotalActualRequests, summedRequests, "rows must sum to the org total (additive rollup)")
}

// TestDimensionRankings_VirtualKeyUnassigned pins that the Unassigned bucket now
// also applies to the non-org rollup dimensions (here virtual_key; user is
// identical). Requests with no scalar virtual_key_id collapse into "Unassigned"
// instead of being dropped, so the per-key breakdown stays additive and
// reconciles to the org total.
func TestDimensionRankings_VirtualKeyUnassigned(t *testing.T) {
	store, db := setupPerfTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()

	// Row 1: request through a virtual key -> vk-1. Row 2: direct API-key
	// request with no virtual key -> Unassigned.
	err := db.Exec(`
		INSERT INTO logs (id, timestamp, object_type, provider, model, status,
			virtual_key_id, virtual_key_name, created_at, latency, cost,
			prompt_tokens, completion_tokens, total_tokens)
		VALUES (?, ?, 'chat_completion', 'openai', 'gpt-4', 'success',
			'vk-1', 'Prod Key', ?, 100, 0.01, 10, 5, 15)
	`, uuid.New().String(), now, now).Error
	require.NoError(t, err)
	err = db.Exec(`
		INSERT INTO logs (id, timestamp, object_type, provider, model, status,
			created_at, latency, cost, prompt_tokens, completion_tokens, total_tokens)
		VALUES (?, ?, 'chat_completion', 'openai', 'gpt-4', 'success',
			?, 100, 0.01, 10, 5, 15)
	`, uuid.New().String(), now, now).Error
	require.NoError(t, err)

	start := now.Add(-time.Hour)
	end := now.Add(time.Hour)
	res, err := store.GetDimensionRankings(ctx, SearchFilters{StartTime: &start, EndTime: &end}, RankingDimensionVirtualKey)
	require.NoError(t, err)

	assert.Equal(t, int64(2), res.TotalActualRequests, "additive total includes the Unassigned bucket")
	assert.Equal(t, res.TotalActualRequests, res.TotalAttributedRequests, "single-owner attribution: attributed == actual")

	requestsByID := make(map[string]int64, len(res.Rankings))
	namesByID := make(map[string]string, len(res.Rankings))
	var summed int64
	for _, r := range res.Rankings {
		requestsByID[r.ID] = r.TotalRequests
		namesByID[r.ID] = r.Name
		summed += r.TotalRequests
	}
	assert.Equal(t, int64(1), requestsByID["vk-1"], "the keyed request is credited to its virtual key")
	assert.Equal(t, int64(1), requestsByID[unassignedDimensionID], "the keyless request falls into Unassigned")
	assert.Equal(t, unassignedDimensionName, namesByID[unassignedDimensionID], "Unassigned gets a stable display name")
	assert.Equal(t, res.TotalActualRequests, summed, "rows sum to the org total")
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

// insertOrgScopeLog inserts a log row carrying the full set of ownership
// columns a DAC scope can predicate on, so the org dimensions
// (customer_id / business_unit_id) can be exercised independently of the
// user / team ones.
func insertOrgScopeLog(t *testing.T, db *gorm.DB, ts time.Time,
	userID, userName, teamID, customerID, buID string) {
	t.Helper()
	nz := func(s string) any {
		if s == "" {
			return nil
		}
		return s
	}
	err := db.Exec(`
		INSERT INTO logs (id, timestamp, object_type, provider, model, status,
			user_id, user_name, team_id, customer_id, business_unit_id,
			created_at, latency, cost, prompt_tokens, completion_tokens, total_tokens)
		VALUES (?, ?, 'chat_completion', 'openai', 'gpt-4', 'success',
			?, ?, ?, ?, ?, ?, 100, 0.01, 10, 5, 15)
	`, uuid.New().String(), ts, nz(userID), nz(userName), nz(teamID),
		nz(customerID), nz(buID), ts).Error
	require.NoError(t, err, "failed to insert org-scope test log")
}

// teamDataScope mirrors the predicate the enterprise DAC resolver builds for a
// team-data principal: a single OR over every ownership dimension, including
// the org ones. Kept faithful to that shape because the failure mode it guards
// is a column the matview does not project, which errors the whole query rather
// than under-filtering it.
func teamDataScope(userIDs, teamIDs, customerIDs, buIDs []string) queryscope.QueryScope {
	return func(db *gorm.DB) *gorm.DB {
		return db.Where(
			"(user_id IN ? OR team_id IN ? OR customer_id IN ? OR business_unit_id IN ?)",
			userIDs, teamIDs, customerIDs, buIDs,
		)
	}
}

// TestFilterUsersMatView_TeamDataScopeIncludesOrgDimensions is the regression for
// the org-dimension gap: mv_filter_* projected only (user_id, team_id,
// virtual_key_id), while a team-data DAC scope also ORs customer_id and
// business_unit_id. The matview query then failed with 42703 undefined_column,
// which fallBackToRaw classifies as a shape error — so every team-data
// filterdata request silently disabled the matview read path process-wide,
// kicked a self-heal that rebuilt the same too-narrow views, and served a raw
// full-window scan instead. The widened views must resolve the predicate AND
// honour it: a user reachable only through the shared customer is visible, an
// unrelated one is not.
//
// Asserted against getDistinctKeyPairsFromMatView rather than the public
// GetDistinctKeyPairs precisely because the raw fallback hides the failure —
// through the public method the old shape returns correct rows, just from the
// wrong (slow) path.
func TestFilterUsersMatView_TeamDataScopeIncludesOrgDimensions(t *testing.T) {
	store, db := setupPerfTestDB(t)
	store.matViewsReady.Store(true)
	now := time.Now().UTC()

	// Visible only via customer_id, and only via business_unit_id respectively;
	// neither shares the principal's user or team, so the predicate cannot be
	// satisfied without those two columns being present on the view.
	insertOrgScopeLog(t, db, now, "u-cust", "Customer Peer", "t-other", "c-mine", "")
	insertOrgScopeLog(t, db, now, "u-bu", "BU Peer", "t-other", "", "bu-mine")
	insertOrgScopeLog(t, db, now, "u-outsider", "Outsider", "t-other", "c-other", "bu-other")
	refreshTestMatViews(t, db)

	ctx := queryscope.WithQueryScope(context.Background(),
		teamDataScope([]string{"u-me"}, []string{"t-mine"}, []string{"c-mine"}, []string{"bu-mine"}))

	pairs, served, err := store.getDistinctKeyPairsFromMatView(ctx, "user_id", "user_name", 1000, "")
	require.True(t, served, "mv_filter_users must serve the users dimension")
	require.NoError(t, err, "team-data scope must resolve against mv_filter_users' visibility columns")
	byID := keyPairsByID(pairs)

	assert.Equal(t, "Customer Peer", byID["u-cust"], "user sharing the principal's customer must appear")
	assert.Equal(t, "BU Peer", byID["u-bu"], "user sharing the principal's business unit must appear")
	_, leaked := byID["u-outsider"]
	assert.False(t, leaked, "user in another customer and BU must stay hidden")

	// The public path must agree, and must not have tripped the shape-error
	// fallback on the way (that flag flip is the production symptom).
	public, err := store.GetDistinctKeyPairs(ctx, "user_id", "user_name", 1000, "")
	require.NoError(t, err)
	assert.Equal(t, byID, keyPairsByID(public), "matview and public paths must agree")
	assert.True(t, store.matViewsReady.Load(),
		"a scoped filterdata read must not disable the matview read path")
}

// TestFilterMultiValueMatViews_TeamDataScopeResolves covers the bodyOverride
// views (teams / business units), whose visibility columns are projected by
// multiValueFilterMatViewBody rather than scopeProjection — the two are easy to
// let drift apart, and drift there means the same silent demotion to raw scans.
func TestFilterMultiValueMatViews_TeamDataScopeResolves(t *testing.T) {
	store, db := setupPerfTestDB(t)
	store.matViewsReady.Store(true)
	now := time.Now().UTC()

	insertOrgScopeLog(t, db, now, "u-cust", "Customer Peer", "t-visible", "c-mine", "bu-mine")
	insertOrgScopeLog(t, db, now, "u-outsider", "Outsider", "t-hidden", "c-other", "bu-other")
	// Name the teams/BUs so they qualify for the dropdown (the views drop rows
	// with an empty name).
	require.NoError(t, db.Exec(`UPDATE logs SET team_name = 'Team ' || team_id,
		business_unit_name = 'BU ' || business_unit_id`).Error)
	refreshTestMatViews(t, db)

	ctx := queryscope.WithQueryScope(context.Background(),
		teamDataScope([]string{"u-me"}, []string{"t-mine"}, []string{"c-mine"}, []string{"bu-mine"}))

	teams, served, err := store.getDistinctKeyPairsFromMatView(ctx, "team_id", "team_name", 1000, "")
	require.True(t, served)
	require.NoError(t, err, "team-data scope must resolve against mv_filter_teams")
	teamsByID := keyPairsByID(teams)
	assert.Contains(t, teamsByID, "t-visible", "team on a row inside the principal's customer must appear")
	assert.NotContains(t, teamsByID, "t-hidden", "team on an out-of-scope row must not leak")

	bus, served, err := store.getDistinctKeyPairsFromMatView(ctx, "business_unit_id", "business_unit_name", 1000, "")
	require.True(t, served)
	require.NoError(t, err, "team-data scope must resolve against mv_filter_business_units")
	busByID := keyPairsByID(bus)
	assert.Contains(t, busByID, "bu-mine", "the principal's own business unit must appear")
	assert.NotContains(t, busByID, "bu-other", "an out-of-scope business unit must not leak")
}
