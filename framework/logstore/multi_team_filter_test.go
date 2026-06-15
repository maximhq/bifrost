package logstore

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMultiValueDimensionFilterSQL verifies the predicate that matches logs by a
// dimension that is scalar on the primary column and multi-valued on the JSON
// array column: it must OR the scalar IN with guarded array containment per id,
// and bind args in order (the id slice first, then one JSON fragment per id).
func TestMultiValueDimensionFilterSQL(t *testing.T) {
	sql, args := multiValueDimensionFilterSQL("team_id", "team_ids", []string{"t1", "t2"})

	// Outer parens so it ANDs as one group with other filters.
	assert.True(t, strings.HasPrefix(sql, "(") && strings.HasSuffix(sql, ")"), "must be parenthesised: %s", sql)
	// Scalar primary path (covers VK-team / pre-migration / NULL-array rows).
	assert.Contains(t, sql, "team_id IN ?")
	// Partial-index guard so the planner uses the jsonb_path_ops GIN.
	assert.Contains(t, sql, "team_ids IS NOT NULL AND team_ids IS JSON ARRAY")
	// One containment test per requested id.
	assert.Equal(t, 2, strings.Count(sql, "team_ids::jsonb @> ?::jsonb"))

	// Args: the id slice first (for `IN ?`), then a JSON array fragment per id.
	require.Len(t, args, 3)
	assert.Equal(t, []string{"t1", "t2"}, args[0])
	assert.Equal(t, `["t1"]`, args[1])
	assert.Equal(t, `["t2"]`, args[2])

	// Works for the BU columns too.
	buSQL, buArgs := multiValueDimensionFilterSQL("business_unit_id", "business_unit_ids", []string{"bu1"})
	assert.Contains(t, buSQL, "business_unit_id IN ?")
	assert.Contains(t, buSQL, "business_unit_ids::jsonb @> ?::jsonb")
	require.Len(t, buArgs, 2)
	assert.Equal(t, `["bu1"]`, buArgs[1])
}

// TestTeamOrBUFanoutFrom verifies the fan-out FROM subquery: it must unnest the
// array columns (id+name aligned by ordinality) for array rows and fall back to
// the scalar id/name for non-array rows, with mutually exclusive branches, and
// be aliased AS logs so DAC scope + filters still resolve.
func TestTeamOrBUFanoutFrom(t *testing.T) {
	teamSQL, ok := teamOrBUFanoutFrom("team_id")
	require.True(t, ok)
	assert.Contains(t, teamSQL, "jsonb_array_elements_text(l.team_ids::jsonb) WITH ORDINALITY")
	assert.Contains(t, teamSQL, "jsonb_array_elements_text(l.team_names::jsonb) WITH ORDINALITY")
	assert.Contains(t, teamSQL, "ON n.ord = t.ord", "names aligned with ids by ordinality")
	// array branch guard + mutually-exclusive scalar fallback branch
	assert.Contains(t, teamSQL, "WHERE l.team_ids IS NOT NULL AND l.team_ids IS JSON ARRAY")
	assert.Contains(t, teamSQL, "SELECT l.team_id, COALESCE(l.team_name, '')")
	assert.Contains(t, teamSQL, "WHERE l.team_ids IS NULL OR l.team_ids IS NOT JSON ARRAY")
	assert.Contains(t, teamSQL, "UNION ALL")
	assert.Contains(t, teamSQL, ") AS logs", "aliased AS logs so scope/filters resolve")
	assert.Contains(t, teamSQL, "fan.dim_id AS dim_id")
	assert.Contains(t, teamSQL, "fan.dim_name AS dim_name")

	buSQL, ok := teamOrBUFanoutFrom("business_unit_id")
	require.True(t, ok)
	assert.Contains(t, buSQL, "business_unit_ids::jsonb")
	assert.Contains(t, buSQL, "SELECT l.business_unit_id, COALESCE(l.business_unit_name, '')")

	// Customers fan out too (enterprise team↔customer M2M).
	custSQL, ok := teamOrBUFanoutFrom("customer_id")
	require.True(t, ok)
	assert.Contains(t, custSQL, "jsonb_array_elements_text(l.customer_ids::jsonb) WITH ORDINALITY")
	assert.Contains(t, custSQL, "SELECT l.customer_id, COALESCE(l.customer_name, '')")

	// Non-fan-out dimensions return false (caller uses the normal scalar path).
	_, ok = teamOrBUFanoutFrom("user_id")
	assert.False(t, ok)
	_, ok = teamOrBUFanoutFrom("provider")
	assert.False(t, ok)
}

// TestCanUseMatViewFilters_ExcludesTeamBU verifies that a team or business-unit
// filter disqualifies the matview path: mv_logs_hourly only has the scalar
// primary, so these must fall through to the raw (array-or-scalar) path to stay
// complete. Other filters (e.g. provider) remain matview-eligible.
func TestCanUseMatViewFilters_ExcludesTeamBU(t *testing.T) {
	assert.True(t, canUseMatViewFilters(SearchFilters{}), "empty filters → matview eligible")
	assert.True(t, canUseMatViewFilters(SearchFilters{Providers: []string{"openai"}}), "provider filter stays matview-eligible")

	assert.False(t, canUseMatViewFilters(SearchFilters{TeamIDs: []string{"t1"}}), "team filter must force the raw path")
	assert.False(t, canUseMatViewFilters(SearchFilters{BusinessUnitIDs: []string{"bu1"}}), "BU filter must force the raw path")
	assert.False(t, canUseMatViewFilters(SearchFilters{CustomerIDs: []string{"c1"}}), "customer filter must force the raw path")
}
