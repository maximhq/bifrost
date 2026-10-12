package logstore

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/queryscope"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// TestDimensionFanoutFrom_PerDialect pins the dispatcher: Postgres must delegate
// to teamOrBUFanoutFrom byte-for-byte (the filter matviews are built from that
// exact text, and drift makes repairMatViewShapes drop them on boot), the other
// supported dialects must produce their own array-or-scalar subquery, and every
// unsupported (dialect, dimension) pair must fall back to the scalar path.
func TestDimensionFanoutFrom_PerDialect(t *testing.T) {
	for _, idCol := range []string{"team_id", "customer_id", "business_unit_id"} {
		pgWant, ok := teamOrBUFanoutFrom(idCol)
		require.True(t, ok)

		pgGot, ok := dimensionFanoutFrom("postgres", idCol)
		require.True(t, ok)
		assert.Equal(t, pgWant, pgGot, "postgres text is pinned by the filter matview DDL for %s", idCol)

		agentGot, ok := agentDimensionFanoutFrom("postgres", idCol)
		require.True(t, ok)
		assert.Equal(t, strings.ReplaceAll(pgWant, "FROM logs", "FROM agent_logs"), agentGot)
	}

	// Every fan-out dimension must be wired on every dialect: a column-mapping
	// slip (e.g. business unit reading the team arrays) is invisible unless each
	// dimension is asserted against its own columns.
	for _, idCol := range []string{"team_id", "customer_id", "business_unit_id"} {
		arrIDs, arrNames, scalarName, ok := fanoutDimensionColumns(idCol)
		require.True(t, ok)

		sqliteSQL, ok := dimensionFanoutFrom("sqlite", idCol)
		require.Truef(t, ok, "sqlite fan-out missing for %s", idCol)
		assert.Contains(t, sqliteSQL, "JOIN json_each(")
		assert.Contains(t, sqliteSQL, "json_extract(", "names are read positionally")
		assert.Contains(t, sqliteSQL, "json_valid(l."+arrIDs+")")
		assert.Contains(t, sqliteSQL, "json_valid(l."+arrNames+")")
		assert.Contains(t, sqliteSQL, "ELSE '[]' END", "non-array values must not reach json_each")
		assert.Contains(t, sqliteSQL, "UNION ALL")
		assert.Contains(t, sqliteSQL, "COALESCE(l."+idCol+", '') AS dim_id", "scalar fallback branch")
		assert.Contains(t, sqliteSQL, "COALESCE(l."+scalarName+", '') AS dim_name")
		assert.Contains(t, sqliteSQL, ") AS logs", "aliased AS logs so scope/filters resolve")

		chSQL, ok := dimensionFanoutFrom("clickhouse", idCol)
		require.Truef(t, ok, "clickhouse fan-out missing for %s", idCol)
		assert.Contains(t, chSQL, "ARRAY JOIN if(")
		assert.Contains(t, chSQL, "JSONExtract(ifNull(l."+arrIDs+", ''), 'Array(String)')")
		assert.Contains(t, chSQL, "JSONExtract(ifNull(l."+arrNames+", ''), 'Array(String)')")
		assert.Contains(t, chSQL, "arrayEnumerate(")
		assert.Contains(t, chSQL, "[(ifNull(l."+idCol+", ''), ifNull(l."+scalarName+", ''))]", "scalar fallback branch")
		assert.Contains(t, chSQL, ") AS logs")

		// No dimension may read another's columns.
		for _, other := range []string{"team_ids", "customer_ids", "business_unit_ids"} {
			if other == arrIDs {
				continue
			}
			assert.NotContainsf(t, sqliteSQL, "l."+other, "sqlite %s must not read %s", idCol, other)
			assert.NotContainsf(t, chSQL, "l."+other, "clickhouse %s must not read %s", idCol, other)
		}
	}

	// Dimensions with no array column keep plain scalar attribution.
	for _, idCol := range []string{"user_id", "virtual_key_id", "provider", "app"} {
		_, ok := dimensionFanoutFrom("postgres", idCol)
		assert.Falsef(t, ok, "%s has no array column", idCol)
		_, ok = dimensionFanoutFrom("sqlite", idCol)
		assert.Falsef(t, ok, "%s has no array column", idCol)
	}
	// Dialects without an implementation fall back rather than emitting bad SQL.
	_, mysqlOK := dimensionFanoutFrom("mysql", "team_id")
	assert.False(t, mysqlOK)
}

// newFanoutTestStore returns an empty in-memory SQLite store. Fan-out rows are
// inserted with insertFanoutLog so the raw JSON-array columns can hold values
// (including malformed ones) that the Go writer would never produce.
func newFanoutTestStore(t *testing.T) (*RDBLogStore, *gorm.DB) {
	t.Helper()
	// A named shared-cache database rather than ":memory:", which gives every
	// pooled connection its own empty database - so a call that uses two at once
	// (SearchLogs counts and pages concurrently) found no logs table on the
	// second. Named per test so parallel tests do not share rows.
	dsn := "file:" + url.QueryEscape(t.Name()) + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Log{}))
	return &RDBLogStore{db: db, logger: bifrost.NewDefaultLogger(schemas.LogLevelInfo)}, db
}

// insertDimensionLog writes one log row with the scalar and/or JSON-array
// columns of the given dimension set. Raw SQL (not the Go writer) so the array
// columns can hold values the writer would never produce, e.g. malformed JSON.
// Empty-string args are stored as NULL so the array-vs-scalar branch selection
// is exercised faithfully.
func insertDimensionLog(t *testing.T, db *gorm.DB, idCol, id string, ts time.Time, scalarID, scalarName, arrayIDs, arrayNames string) {
	t.Helper()
	arrIDsCol, arrNamesCol, scalarNameCol, ok := fanoutDimensionColumns(idCol)
	require.Truef(t, ok, "%s is not a fan-out dimension", idCol)
	nz := func(s string) any {
		if s == "" {
			return nil
		}
		return s
	}
	err := db.Exec(fmt.Sprintf(`
		INSERT INTO logs (id, timestamp, object_type, provider, model, status,
			%s, %s, %s, %s,
			created_at, latency, cost, prompt_tokens, completion_tokens, total_tokens)
		VALUES (?, ?, 'chat_completion', 'openai', 'gpt-4', 'success',
			?, ?, ?, ?, ?, 100, 0.01, 10, 5, 15)
	`, idCol, scalarNameCol, arrIDsCol, arrNamesCol),
		id, ts, nz(scalarID), nz(scalarName), nz(arrayIDs), nz(arrayNames), ts).Error
	require.NoError(t, err, "failed to insert fan-out test log")
}

// insertFanoutLog is the customer-dimension shorthand for insertDimensionLog.
func insertFanoutLog(t *testing.T, db *gorm.DB, id string, ts time.Time, customerID, customerName, customerIDs, customerNames string) {
	t.Helper()
	insertDimensionLog(t, db, "customer_id", id, ts, customerID, customerName, customerIDs, customerNames)
}

func fanoutWindow(now time.Time) SearchFilters {
	start := now.Add(-time.Hour)
	end := now.Add(time.Hour)
	return SearchFilters{StartTime: &start, EndTime: &end}
}

// TestDimensionRankings_SQLiteArrayFanout is the regression for the reported
// bug: the enterprise user/AP path records a request's customers only in the
// JSON-array columns, leaving the scalar customer_id NULL, so scalar-only
// rankings reported every request as "Unassigned". Each id on the row must now
// be credited in full, with the scalar column still covering rows that have no
// array.
func TestDimensionRankings_SQLiteArrayFanout(t *testing.T) {
	s, db := newFanoutTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	// Row 1: array-only multi-customer request, scalar NULL (enterprise path).
	insertFanoutLog(t, db, "log-1", now, "", "", `["c-a","c-b"]`, `["Customer A","Customer B"]`)
	// Row 2: scalar-only request (VK path / pre-migration row).
	insertFanoutLog(t, db, "log-2", now, "c-a", "Customer A", "", "")
	// Row 3: no owner at all -> Unassigned.
	insertFanoutLog(t, db, "log-3", now, "", "", "", "")

	res, err := s.GetDimensionRankings(ctx, fanoutWindow(now), RankingDimensionCustomer)
	require.NoError(t, err)

	requests := make(map[string]int64, len(res.Rankings))
	names := make(map[string]string, len(res.Rankings))
	cost := make(map[string]float64, len(res.Rankings))
	var summed int64
	for _, r := range res.Rankings {
		requests[r.ID] = r.TotalRequests
		names[r.ID] = r.Name
		cost[r.ID] = r.TotalCost
		summed += r.TotalRequests
	}

	assert.Equal(t, int64(2), requests["c-a"], "credited its own scalar row plus the array row it appears in")
	assert.Equal(t, int64(1), requests["c-b"], "array-only customer is now credited instead of being lost")
	assert.Equal(t, int64(1), requests[unassignedDimensionID], "rows with neither array nor scalar owner")
	assert.Equal(t, "Customer B", names["c-b"], "name read positionally from customer_names")
	assert.Equal(t, unassignedDimensionName, names[unassignedDimensionID])
	assert.InDelta(t, 0.01, cost["c-b"], 1e-9, "fan-out credits the full cost to each customer, it does not split it")

	assert.Equal(t, int64(3), res.TotalActualRequests, "actual counts each request once, including Unassigned")
	assert.Equal(t, int64(4), res.TotalAttributedRequests, "the two-customer request is attributed twice")
	assert.Equal(t, res.TotalAttributedRequests, summed, "rows sum to attributed, not actual")
}

// TestDimensionRankings_SQLiteFanoutAllDimensions runs the same array /
// scalar-fallback / Unassigned contract over every fan-out dimension, so a
// column-mapping slip in one of them (business unit reading the team arrays,
// say) cannot hide behind the customer coverage above.
func TestDimensionRankings_SQLiteFanoutAllDimensions(t *testing.T) {
	cases := []struct {
		dimension RankingDimension
		idCol     string
	}{
		{RankingDimensionTeam, "team_id"},
		{RankingDimensionCustomer, "customer_id"},
		{RankingDimensionBusinessUnit, "business_unit_id"},
	}
	for _, tc := range cases {
		t.Run(string(tc.dimension), func(t *testing.T) {
			s, db := newFanoutTestStore(t)
			ctx := context.Background()
			now := time.Now().UTC()

			// Array-only multi-entity row, scalar-only row, and an owner-less row.
			insertDimensionLog(t, db, tc.idCol, "log-1", now, "", "", `["e-a","e-b"]`, `["Entity A","Entity B"]`)
			insertDimensionLog(t, db, tc.idCol, "log-2", now, "e-a", "Entity A", "", "")
			insertDimensionLog(t, db, tc.idCol, "log-3", now, "", "", "", "")

			res, err := s.GetDimensionRankings(ctx, fanoutWindow(now), tc.dimension)
			require.NoError(t, err)

			requests := make(map[string]int64, len(res.Rankings))
			names := make(map[string]string, len(res.Rankings))
			for _, r := range res.Rankings {
				requests[r.ID] = r.TotalRequests
				names[r.ID] = r.Name
			}
			assert.Equal(t, int64(2), requests["e-a"], "scalar row plus the array row it appears in")
			assert.Equal(t, int64(1), requests["e-b"], "array-only entity is credited, not lost")
			assert.Equal(t, "Entity B", names["e-b"], "name aligned positionally with the id")
			assert.Equal(t, int64(1), requests[unassignedDimensionID])
			assert.Equal(t, unassignedDimensionName, names[unassignedDimensionID])
			assert.Equal(t, int64(3), res.TotalActualRequests)
			assert.Equal(t, int64(4), res.TotalAttributedRequests)
		})
	}
}

// TestDimensionRankings_FanoutDimensionsAreIndependent pins that each fan-out
// dimension reads only its own columns: a row carrying every hierarchy array
// must produce exactly that dimension's entities, with no bleed between them.
func TestDimensionRankings_FanoutDimensionsAreIndependent(t *testing.T) {
	s, db := newFanoutTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	require.NoError(t, db.Exec(`
		INSERT INTO logs (id, timestamp, object_type, provider, model, status,
			team_ids, team_names, customer_ids, customer_names, business_unit_ids, business_unit_names,
			created_at, latency, cost, prompt_tokens, completion_tokens, total_tokens)
		VALUES ('log-1', ?, 'chat_completion', 'openai', 'gpt-4', 'success',
			'["t-1"]', '["Team One"]', '["c-1","c-2"]', '["Cust One","Cust Two"]', '["b-1"]', '["BU One"]',
			?, 100, 0.01, 10, 5, 15)
	`, now, now).Error)

	expected := map[RankingDimension][]string{
		RankingDimensionTeam:         {"t-1"},
		RankingDimensionCustomer:     {"c-1", "c-2"},
		RankingDimensionBusinessUnit: {"b-1"},
	}
	for dim, wantIDs := range expected {
		res, err := s.GetDimensionRankings(ctx, fanoutWindow(now), dim)
		require.NoError(t, err)
		gotIDs := make([]string, 0, len(res.Rankings))
		for _, r := range res.Rankings {
			gotIDs = append(gotIDs, r.ID)
		}
		sort.Strings(gotIDs)
		assert.Equalf(t, wantIDs, gotIDs, "%s must read only its own array columns", dim)
	}
}

// TestDimensionRankings_SQLiteMalformedArray pins the guards around json_each:
// it raises on non-JSON input, so a row whose array column holds garbage (or a
// JSON object rather than an array) must fall to the scalar branch instead of
// failing the whole query.
func TestDimensionRankings_SQLiteMalformedArray(t *testing.T) {
	s, db := newFanoutTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	insertFanoutLog(t, db, "log-1", now, "c-a", "Customer A", "not json", "")
	insertFanoutLog(t, db, "log-2", now, "c-b", "Customer B", `{"a":1}`, `{"a":1}`)
	insertFanoutLog(t, db, "log-3", now, "", "", `["c-c"]`, `["Customer C"]`)

	res, err := s.GetDimensionRankings(ctx, fanoutWindow(now), RankingDimensionCustomer)
	require.NoError(t, err, "malformed JSON must not fail the query")

	requests := make(map[string]int64, len(res.Rankings))
	for _, r := range res.Rankings {
		requests[r.ID] = r.TotalRequests
	}
	assert.Equal(t, int64(1), requests["c-a"], "garbage array falls back to the scalar owner")
	assert.Equal(t, int64(1), requests["c-b"], "a JSON object is not an array; scalar owner wins")
	assert.Equal(t, int64(1), requests["c-c"])
	assert.Equal(t, int64(3), res.TotalActualRequests)
	assert.Equal(t, int64(3), res.TotalAttributedRequests, "no row fanned out to more than one customer")
}

// TestDimensionRankings_SingleOwnerDimensionsUnchanged pins that dimensions with
// no array column keep exactly their previous behaviour: one owner per request,
// owner-less rows bucketed, and attributed == actual.
func TestDimensionRankings_SingleOwnerDimensionsUnchanged(t *testing.T) {
	s, db := newFanoutTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	require.NoError(t, db.Exec(`
		INSERT INTO logs (id, timestamp, object_type, provider, model, status,
			virtual_key_id, virtual_key_name, customer_ids, created_at, latency, cost,
			prompt_tokens, completion_tokens, total_tokens)
		VALUES ('log-1', ?, 'chat_completion', 'openai', 'gpt-4', 'success',
			'vk-1', 'Prod Key', '["c-a","c-b"]', ?, 100, 0.01, 10, 5, 15)
	`, now, now).Error)
	insertFanoutLog(t, db, "log-2", now, "", "", "", "")

	res, err := s.GetDimensionRankings(ctx, fanoutWindow(now), RankingDimensionVirtualKey)
	require.NoError(t, err)

	requests := make(map[string]int64, len(res.Rankings))
	for _, r := range res.Rankings {
		requests[r.ID] = r.TotalRequests
	}
	assert.Equal(t, int64(1), requests["vk-1"], "the customer array must not fan out the virtual-key rollup")
	assert.Equal(t, int64(1), requests[unassignedDimensionID])
	assert.Equal(t, int64(2), res.TotalActualRequests)
	assert.Equal(t, res.TotalActualRequests, res.TotalAttributedRequests, "single-owner dimension stays additive")
}

// TestDimensionHistograms_SQLiteArrayFanout pins that the cost / token / latency
// by-dimension histograms use the same attribution as the rankings shown beside
// them: each customer on the row gets the full value, and owner-less rows land
// in the Unassigned bucket (which the latency histogram never had before).
func TestDimensionHistograms_SQLiteArrayFanout(t *testing.T) {
	s, db := newFanoutTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	insertFanoutLog(t, db, "log-1", now, "", "", `["c-a","c-b"]`, `["Customer A","Customer B"]`)
	insertFanoutLog(t, db, "log-2", now, "", "", "", "")
	filters := fanoutWindow(now)

	costRes, err := s.GetDimensionCostHistogram(ctx, filters, 3600, DimensionCustomer)
	require.NoError(t, err)
	costByDim := map[string]float64{}
	for _, b := range costRes.Buckets {
		for dim, v := range b.ByDimension {
			costByDim[dim] += v
		}
	}
	assert.InDelta(t, 0.01, costByDim["c-a"], 1e-9)
	assert.InDelta(t, 0.01, costByDim["c-b"], 1e-9, "full cost credited to each customer")
	assert.InDelta(t, 0.01, costByDim[unassignedDimensionID], 1e-9)

	tokenRes, err := s.GetDimensionTokenHistogram(ctx, filters, 3600, DimensionCustomer)
	require.NoError(t, err)
	tokensByDim := map[string]int64{}
	for _, b := range tokenRes.Buckets {
		for dim, v := range b.ByDimension {
			tokensByDim[dim] += v.TotalTokens
		}
	}
	assert.Equal(t, int64(15), tokensByDim["c-a"])
	assert.Equal(t, int64(15), tokensByDim["c-b"])
	assert.Equal(t, int64(15), tokensByDim[unassignedDimensionID])

	latencyRes, err := s.GetDimensionLatencyHistogram(ctx, filters, 3600, DimensionCustomer)
	require.NoError(t, err)
	latencyDims := map[string]bool{}
	for _, b := range latencyRes.Buckets {
		for dim := range b.ByDimension {
			latencyDims[dim] = true
		}
	}
	assert.True(t, latencyDims["c-a"])
	assert.True(t, latencyDims["c-b"], "latency must fan out like cost and tokens")
	assert.True(t, latencyDims[unassignedDimensionID], "latency must bucket owner-less rows, not key them on ''")
	assert.False(t, latencyDims[""], "no empty-string dimension key")
}

// TestDimensionRankings_FanoutTrendUsesFanoutRelation pins that the previous
// period is measured on the same relation as the current one. Reading the
// previous period from the scalar column would leave an array-only entity with
// no baseline and report it as brand-new traffic.
func TestDimensionRankings_FanoutTrendUsesFanoutRelation(t *testing.T) {
	s, db := newFanoutTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	// One request this hour, one in the previous hour, both array-only.
	insertFanoutLog(t, db, "log-now", now.Add(-5*time.Minute), "", "", `["c-a"]`, `["Customer A"]`)
	insertFanoutLog(t, db, "log-prev", now.Add(-90*time.Minute), "", "", `["c-a"]`, `["Customer A"]`)

	start := now.Add(-time.Hour)
	res, err := s.GetDimensionRankings(ctx, SearchFilters{StartTime: &start, EndTime: &now}, RankingDimensionCustomer)
	require.NoError(t, err)

	var found bool
	for _, r := range res.Rankings {
		if r.ID != "c-a" {
			continue
		}
		found = true
		assert.True(t, r.Trend.HasPreviousPeriod, "the previous period must be read from the fan-out relation too")
		assert.Zero(t, r.Trend.RequestsTrend, "one request in each period is flat, not a spike")
	}
	assert.True(t, found, "expected a ranking row for c-a")
}

// withCeiling returns ctx bounding idCol to allowed on charts, the way the
// enterprise DAC scope does for a team / business-unit / customer-data caller.
func withCeiling(ctx context.Context, idCol string, allowed ...string) context.Context {
	return queryscope.WithDimensionScope(ctx, func(col string) ([]string, bool) {
		if col != idCol {
			return nil, false
		}
		return allowed, true
	})
}

// rankingRequestsByID indexes a ranking's request counts and names by id and
// sums its rows.
func rankingRequestsByID(res *DimensionRankingResult) (map[string]int64, map[string]string, int64) {
	requests := make(map[string]int64, len(res.Rankings))
	names := make(map[string]string, len(res.Rankings))
	var sum int64
	for _, r := range res.Rankings {
		requests[r.ID] = r.TotalRequests
		names[r.ID] = r.Name
		sum += r.TotalRequests
	}
	return requests, names, sum
}

// otherRankingStores runs fn against SQLite, and against Postgres when one is
// reachable, since the two fan out the team arrays with different SQL.
func otherRankingStores(t *testing.T, fn func(t *testing.T, store *RDBLogStore, db *gorm.DB)) {
	t.Run("sqlite", func(t *testing.T) {
		store := newTestSQLiteStore(t)
		fn(t, store, store.db)
	})
	t.Run("postgres", func(t *testing.T) {
		store, db := setupPerfTestDB(t)
		fn(t, store, db)
	})
}

// TestDimensionRankings_HiddenTeamsGoToOther verifies a fanned-out ranking
// under a ceiling puts the attributions the caller may not be shown into one
// Other row instead of dropping them, without naming the hidden team, so the
// rows still sum to TotalAttributedRequests and actual stays the full count.
func TestDimensionRankings_HiddenTeamsGoToOther(t *testing.T) {
	otherRankingStores(t, func(t *testing.T, store *RDBLogStore, db *gorm.DB) {
		now := time.Now().UTC()
		insertTeamBULog(t, db, now, "u-1", "", "", `["t-a","t-b"]`, `["Team A","Team B"]`, "", "", "", "")
		insertTeamBULog(t, db, now, "u-1", "t-a", "Team A", "", "", "", "", "", "")
		insertTeamBULog(t, db, now, "u-2", "", "", `["t-c"]`, `["Team C"]`, "", "", "", "")
		insertTeamBULog(t, db, now, "u-3", "", "", "", "", "", "", "", "")

		start, end := now.Add(-time.Hour), now.Add(time.Hour)
		ctx := withCeiling(context.Background(), "team_id", "t-a")
		res, err := store.GetDimensionRankings(ctx, SearchFilters{StartTime: &start, EndTime: &end}, RankingDimensionTeam)
		require.NoError(t, err)

		requests, names, sum := rankingRequestsByID(res)
		assert.Equal(t, int64(2), requests["t-a"])
		assert.Equal(t, int64(2), requests[otherDimensionID], "t-b's attribution and the t-c request")
		assert.Equal(t, int64(1), requests[unassignedDimensionID])
		assert.NotContains(t, requests, "t-b")
		assert.NotContains(t, requests, "t-c")
		assert.Equal(t, otherDimensionName, names[otherDimensionID], "the Other row never carries a hidden team's name")
		assert.Equal(t, int64(4), res.TotalActualRequests, "actual counts every request the caller can read")
		assert.Equal(t, int64(5), res.TotalAttributedRequests)
		assert.Equal(t, res.TotalAttributedRequests, sum, "rows sum to attributed")
	})
}

// TestDimensionRankings_HiddenUsersGoToOther verifies a single-owner ranking
// under a ceiling reconciles: shown + Other + Unassigned equals the request
// count, so the tab's total matches the rows.
func TestDimensionRankings_HiddenUsersGoToOther(t *testing.T) {
	otherRankingStores(t, func(t *testing.T, store *RDBLogStore, db *gorm.DB) {
		now := time.Now().UTC()
		insertTeamBULog(t, db, now, "u-1", "", "", "", "", "", "", "", "")
		insertTeamBULog(t, db, now, "u-2", "", "", "", "", "", "", "", "")
		insertTeamBULog(t, db, now, "u-2", "", "", "", "", "", "", "", "")
		insertTeamBULog(t, db, now, "", "", "", "", "", "", "", "", "")

		start, end := now.Add(-time.Hour), now.Add(time.Hour)
		ctx := withCeiling(context.Background(), "user_id", "u-1")
		res, err := store.GetDimensionRankings(ctx, SearchFilters{StartTime: &start, EndTime: &end}, RankingDimensionUser)
		require.NoError(t, err)

		requests, _, sum := rankingRequestsByID(res)
		assert.Equal(t, int64(1), requests["u-1"])
		assert.Equal(t, int64(2), requests[otherDimensionID])
		assert.Equal(t, int64(1), requests[unassignedDimensionID])
		assert.NotContains(t, requests, "u-2")
		assert.Equal(t, res.Rankings[0].ID, otherDimensionID, "Other sorts by its request count like any row")
		assert.Equal(t, int64(4), res.TotalActualRequests)
		assert.Equal(t, int64(4), res.TotalAttributedRequests)
		assert.Equal(t, res.TotalActualRequests, sum)
	})
}

// TestDimensionRankings_EmptyCeilingPutsAllOwnedInOther verifies a bounded
// ceiling with nothing allowed still returns the caller's rows, all in Other
// and Unassigned.
func TestDimensionRankings_EmptyCeilingPutsAllOwnedInOther(t *testing.T) {
	otherRankingStores(t, func(t *testing.T, store *RDBLogStore, db *gorm.DB) {
		now := time.Now().UTC()
		insertTeamBULog(t, db, now, "u-1", "", "", "", "", "", "", "", "")
		insertTeamBULog(t, db, now, "", "", "", "", "", "", "", "", "")

		start, end := now.Add(-time.Hour), now.Add(time.Hour)
		res, err := store.GetDimensionRankings(withCeiling(context.Background(), "user_id"), SearchFilters{StartTime: &start, EndTime: &end}, RankingDimensionUser)
		require.NoError(t, err)

		requests, _, sum := rankingRequestsByID(res)
		assert.Equal(t, map[string]int64{otherDimensionID: 1, unassignedDimensionID: 1}, requests)
		assert.Equal(t, res.TotalActualRequests, sum)
	})
}

// TestDimensionRankings_OtherCarriesTrend verifies the Other row compares
// against the previous period's hidden traffic like any other row.
func TestDimensionRankings_OtherCarriesTrend(t *testing.T) {
	otherRankingStores(t, func(t *testing.T, store *RDBLogStore, db *gorm.DB) {
		now := time.Now().UTC()
		insertTeamBULog(t, db, now.Add(-90*time.Minute), "u-2", "", "", "", "", "", "", "", "")
		insertTeamBULog(t, db, now, "u-2", "", "", "", "", "", "", "", "")
		insertTeamBULog(t, db, now, "u-3", "", "", "", "", "", "", "", "")

		start, end := now.Add(-time.Hour), now.Add(time.Hour)
		ctx := withCeiling(context.Background(), "user_id", "u-1")
		res, err := store.GetDimensionRankings(ctx, SearchFilters{StartTime: &start, EndTime: &end}, RankingDimensionUser)
		require.NoError(t, err)

		require.Len(t, res.Rankings, 1)
		other := res.Rankings[0]
		assert.Equal(t, otherDimensionID, other.ID)
		assert.Equal(t, int64(2), other.TotalRequests)
		assert.True(t, other.Trend.HasPreviousPeriod)
		assert.InDelta(t, 100.0, other.Trend.RequestsTrend, 0.001)
	})
}

// TestDimensionRankings_NoCeilingHasNoOther verifies callers without a
// ceiling (all-data, OSS) never see an Other row.
func TestDimensionRankings_NoCeilingHasNoOther(t *testing.T) {
	store := newTestSQLiteStore(t)
	now := time.Now().UTC()
	insertTeamBULog(t, store.db, now, "u-1", "", "", "", "", "", "", "", "")

	start, end := now.Add(-time.Hour), now.Add(time.Hour)
	res, err := store.GetDimensionRankings(context.Background(), SearchFilters{StartTime: &start, EndTime: &end}, RankingDimensionUser)
	require.NoError(t, err)
	requests, _, _ := rankingRequestsByID(res)
	assert.NotContains(t, requests, otherDimensionID)
}

// TestDimensionRankings_OtherStaysWithinRankingLimit verifies adding the Other
// row never returns more rows than the ranking limit: Other competes for the
// top-N like any row.
func TestDimensionRankings_OtherStaysWithinRankingLimit(t *testing.T) {
	store := newTestSQLiteStore(t)
	now := time.Now().UTC()
	for _, user := range []string{"u-1", "u-1", "u-2", "u-3", "u-3", "u-3"} {
		insertTeamBULog(t, store.db, now, user, "", "", "", "", "", "", "", "")
	}

	start, end := now.Add(-time.Hour), now.Add(time.Hour)
	limit := 2
	ctx := withCeiling(context.Background(), "user_id", "u-1", "u-2")
	res, err := store.GetDimensionRankings(ctx, SearchFilters{StartTime: &start, EndTime: &end, RankingLimit: &limit}, RankingDimensionUser)
	require.NoError(t, err)

	require.Len(t, res.Rankings, limit)
	assert.Equal(t, otherDimensionID, res.Rankings[0].ID)
	assert.Equal(t, int64(3), res.Rankings[0].TotalRequests)
	assert.Equal(t, "u-1", res.Rankings[1].ID)
}

// TestDimensionRankings_NoVisibleRowsSkipsPreviousGroupBy verifies a ranking
// whose every owned row is hidden runs no previous-period group-by: with no
// visible ids to narrow it, that query would group every value in the window
// only to discard the result. Other's previous total is read separately.
func TestDimensionRankings_NoVisibleRowsSkipsPreviousGroupBy(t *testing.T) {
	store := newTestSQLiteStore(t)
	now := time.Now().UTC()
	insertTeamBULog(t, store.db, now.Add(-90*time.Minute), "u-1", "", "", "", "", "", "", "", "")
	insertTeamBULog(t, store.db, now, "u-1", "", "", "", "", "", "", "", "")

	var grouped []string
	require.NoError(t, store.db.Callback().Query().After("gorm:query").Register("test:capture_group_by", func(tx *gorm.DB) {
		if sql := tx.Statement.SQL.String(); strings.Contains(sql, "GROUP BY") {
			grouped = append(grouped, sql)
		}
	}))

	start, end := now.Add(-time.Hour), now.Add(time.Hour)
	res, err := store.GetDimensionRankings(withCeiling(context.Background(), "user_id"), SearchFilters{StartTime: &start, EndTime: &end}, RankingDimensionUser)
	require.NoError(t, err)

	require.Len(t, res.Rankings, 1)
	assert.Equal(t, otherDimensionID, res.Rankings[0].ID)
	assert.True(t, res.Rankings[0].Trend.HasPreviousPeriod, "Other still compares against its own previous total")
	assert.Len(t, grouped, 1, "only the current-period ranking groups; got %v", grouped)
}

// assertFilterMatchesFanoutRankings pins that a team, customer or business-unit
// ranking row and the logs its id filters to agree. The rankings fan out over
// the JSON-array columns on every backend that has them, but the filter checked
// only the scalar column outside Postgres, so a row credited to an array-only
// owner linked to an empty Logs page - Warp showed 23 requests for a team and
// its link returned none.
func assertFilterMatchesFanoutRankings(t *testing.T, s *RDBLogStore, insert func(idCol, id, scalarID, scalarName, arrayIDs, arrayNames string), now time.Time) {
	t.Helper()
	ctx := context.Background()
	cases := []struct {
		dimension RankingDimension
		idCol     string
		filter    func(*SearchFilters, []string)
	}{
		{RankingDimensionTeam, "team_id", func(f *SearchFilters, ids []string) { f.TeamIDs = ids }},
		{RankingDimensionCustomer, "customer_id", func(f *SearchFilters, ids []string) { f.CustomerIDs = ids }},
		{RankingDimensionBusinessUnit, "business_unit_id", func(f *SearchFilters, ids []string) { f.BusinessUnitIDs = ids }},
	}
	for _, c := range cases {
		prefix := c.idCol + "-"
		insert(c.idCol, prefix+"array", "", "", `["x-a","x-b"]`, `["A","B"]`)
		insert(c.idCol, prefix+"scalar", "x-a", "A", "", "")
		insert(c.idCol, prefix+"malformed", "x-c", "C", "not json", "")
		insert(c.idCol, prefix+"none", "", "", "", "")
		// Both columns set: the fan-out reads the valid array and ignores the
		// scalar, so the scalar id must not match a filter either.
		insert(c.idCol, prefix+"both", "x-d", "D", `["x-b"]`, `["B"]`)

		res, err := s.GetDimensionRankings(ctx, fanoutWindow(now), c.dimension)
		require.NoError(t, err, c.idCol)
		checked := 0
		for _, row := range res.Rankings {
			if row.ID == unassignedDimensionID {
				continue
			}
			filters := fanoutWindow(now)
			c.filter(&filters, []string{row.ID})
			found, err := s.SearchLogs(ctx, filters, PaginationOptions{Limit: 50})
			require.NoError(t, err, "%s %s", c.idCol, row.ID)
			assert.Equal(t, row.TotalRequests, int64(len(found.Logs)), "%s %s: requests the ranking counts vs logs its filter returns", c.idCol, row.ID)
			checked++
		}
		// Rows of the other dimensions leave this column empty and rank as
		// Unassigned, so the cases share one table without clearing it.
		require.Equal(t, 3, checked, "%s: x-a, x-b and x-c each ranked, x-d never", c.idCol)

		shadowed := fanoutWindow(now)
		c.filter(&shadowed, []string{"x-d"})
		found, err := s.SearchLogs(ctx, shadowed, PaginationOptions{Limit: 50})
		require.NoError(t, err, "%s x-d", c.idCol)
		assert.Empty(t, found.Logs, "%s: a scalar beside a valid array is not ranked, so its filter matches nothing", c.idCol)

		// Several ids at once: x-b only in the array column, x-c only on the
		// scalar. ClickHouse bound the list inside hasAny's brackets as one
		// tuple, so any filter with two ids failed there.
		filters := fanoutWindow(now)
		c.filter(&filters, []string{"x-b", "x-c"})
		found, err = s.SearchLogs(ctx, filters, PaginationOptions{Limit: 50})
		require.NoError(t, err, "%s two ids", c.idCol)
		var got []string
		for _, entry := range found.Logs {
			got = append(got, entry.ID)
		}
		assert.ElementsMatch(t, []string{prefix + "array", prefix + "malformed", prefix + "both"}, got, "%s: two ids match the array rows and the scalar row", c.idCol)
	}
}

func TestDimensionFilters_PostgresMatchFanoutRankings(t *testing.T) {
	s, db := setupPerfTestDB(t)
	now := time.Now().UTC()
	assertFilterMatchesFanoutRankings(t, s, func(idCol, id, scalarID, scalarName, arrayIDs, arrayNames string) {
		insertDimensionLog(t, db, idCol, id, now, scalarID, scalarName, arrayIDs, arrayNames)
	}, now)
}

func TestDimensionFilters_SQLiteMatchFanoutRankings(t *testing.T) {
	s, db := newFanoutTestStore(t)
	now := time.Now().UTC()
	assertFilterMatchesFanoutRankings(t, s, func(idCol, id, scalarID, scalarName, arrayIDs, arrayNames string) {
		insertDimensionLog(t, db, idCol, id, now, scalarID, scalarName, arrayIDs, arrayNames)
	}, now)
}
