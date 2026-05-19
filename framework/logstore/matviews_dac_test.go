package logstore

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFilterMatViews_AllCarryVisibilityColumns asserts the widening
// from Fix 4 — every per-dimension matview projects user_id, team_id,
// and virtual_key_id (or has them as part of its dimension columns).
// Without these columns the DAC scope WHERE applied via ScopedDB
// would error at the SQL layer with "no such column".
func TestFilterMatViews_AllCarryVisibilityColumns(t *testing.T) {
	required := []string{"user_id", "team_id", "virtual_key_id"}
	for _, v := range filterMatViews {
		cols := filterMatViewRequiredColumns(v)
		colSet := make(map[string]struct{}, len(cols))
		for _, c := range cols {
			colSet[c] = struct{}{}
		}
		for _, want := range required {
			_, ok := colSet[want]
			assert.Truef(t, ok,
				"matview %s must project %s as a visibility column (selectExpr=%q)",
				v.name, want, v.selectExpr)
		}
	}
}

// TestFilterMatViews_UniqueIdxIncludesVisibilityColumns asserts the
// widened unique index covers (user_id, team_id, virtual_key_id) so
// the new row shape can be uniquely keyed for REFRESH ... CONCURRENTLY.
func TestFilterMatViews_UniqueIdxIncludesVisibilityColumns(t *testing.T) {
	for _, v := range filterMatViews {
		idx := strings.ToLower(v.uniqueIdx)
		assert.Contains(t, idx, "user_id",
			"matview %s unique index must include user_id", v.name)
		assert.Contains(t, idx, "team_id",
			"matview %s unique index must include team_id", v.name)
		assert.Contains(t, idx, "virtual_key_id",
			"matview %s unique index must include virtual_key_id", v.name)
	}
}

// TestFilterMatViewScopeIdx_IsConcurrentAndIdempotent asserts the DDL
// builder emits CREATE INDEX CONCURRENTLY IF NOT EXISTS with the
// visibility columns as the leading key, so scoped reads are
// index-only and the boot path never blocks on the build.
func TestFilterMatViewScopeIdx_IsConcurrentAndIdempotent(t *testing.T) {
	for _, v := range filterMatViews {
		ddl := filterMatViewScopeIdx(v)
		assert.Contains(t, ddl, "CREATE INDEX CONCURRENTLY",
			"scope index for %s must be CONCURRENTLY", v.name)
		assert.Contains(t, ddl, "IF NOT EXISTS",
			"scope index for %s must be idempotent", v.name)
		assert.Contains(t, ddl, filterMatViewScopeIdxName(v),
			"scope index DDL must reference the canonical index name")
		assert.Contains(t, ddl, scopeIdxColumns,
			"scope index must lead with (user_id, team_id, virtual_key_id)")
	}
}

// TestMatviewScopeIndexes_OnePerFilterMatView asserts the registered
// scope-index list mirrors filterMatViews exactly. ensureMatViews
// iterates this list to bring up the indexes; a missing entry would
// silently leave a matview without its DAC index.
func TestMatviewScopeIndexes_OnePerFilterMatView(t *testing.T) {
	assert.Equal(t, len(filterMatViews), len(matviewScopeIndexes),
		"every per-dimension matview must have a scope-index entry")
	for i, v := range filterMatViews {
		assert.Equal(t, v.name, matviewScopeIndexes[i].view,
			"scope-index ordering must mirror filterMatViews")
		assert.False(t, matviewScopeIndexes[i].unique,
			"scope indexes must NOT be marked unique (the unique-index list owns that role)")
	}
}

// TestFilterMatViewUniqueIdx_StillConcurrent ensures we didn't lose
// the CONCURRENTLY clause when widening the unique index — REFRESH
// MATERIALIZED VIEW CONCURRENTLY depends on the unique index existing.
func TestFilterMatViewUniqueIdx_StillConcurrent(t *testing.T) {
	for _, v := range filterMatViews {
		ddl := filterMatViewUniqueIdx(v)
		assert.Contains(t, ddl, "CREATE UNIQUE INDEX CONCURRENTLY",
			"unique index for %s must remain CONCURRENTLY", v.name)
	}
}

// TestPerDimensionReaders_TargetTheirOwnMatview pins each Get*FromMatView
// reader to its per-dimension view. A prior refactor left two readers
// pointing at the retired mv_logs_filterdata view; this test catches
// that drift on every change.
func TestPerDimensionReaders_TargetTheirOwnMatview(t *testing.T) {
	// Map of expected matview to a substring that must appear in the
	// reader function's body. The bodies are short and the substring
	// is just `Table("<name>")` — Go test reflection can't reach into
	// closures, so we read the source file instead.
	src, err := os.ReadFile("matviews.go")
	require.NoError(t, err)

	cases := []struct {
		funcName string
		view     string
	}{
		{"getDistinctAliasesFromMatView", "mv_filter_aliases"},
		{"getDistinctRoutingEnginesFromMatView", "mv_filter_routing_engines"},
		{"getDistinctStopReasonsFromMatView", "mv_filter_stop_reasons"},
		{"getDistinctModelsFromMatView", "mv_filter_models"},
	}

	for _, c := range cases {
		// Find the function declaration and check the next ~20 lines
		// for the expected Table("...") reference. Anchoring on the
		// declaration keeps the assertion local to the reader.
		idx := strings.Index(string(src), "func (s *RDBLogStore) "+c.funcName+"(")
		if !assert.NotEqualf(t, -1, idx, "reader %s not found", c.funcName) {
			continue
		}
		// Look at the next 1.5KB of the file — enough to span the
		// short reader body without bleeding into siblings.
		end := idx + 1500
		if end > len(src) {
			end = len(src)
		}
		body := string(src[idx:end])
		assert.Containsf(t, body, `Table("`+c.view+`")`,
			"reader %s must target %s, not the legacy mv_logs_filterdata or another view",
			c.funcName, c.view)
		assert.NotContainsf(t, body, `Table("mv_logs_filterdata")`,
			"reader %s still points at the retired mv_logs_filterdata view",
			c.funcName)
	}
}
