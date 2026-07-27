package logstore

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"gorm.io/gorm"
)

// ---------------------------------------------------------------------------
// Materialized view definitions
// ---------------------------------------------------------------------------

// mvLogsHourlyDDL creates a materialized view that pre-aggregates logs into
// hourly buckets grouped by provider, model, status, object_type, and key IDs.
// Includes exact percentiles (p90/p95/p99) computed per hour so they can be
// re-aggregated via weighted averages across wider time ranges.
// canonical_model_name is a dimension despite being effectively functionally
// dependent on model: a bucket only splits transiently when a model's rows mix
// empty and populated canonical values (e.g. an alias gains model_name), and
// readers must re-aggregate per model via SUM / MAX(NULLIF(...)).
const mvLogsHourlyDDL = `
CREATE MATERIALIZED VIEW IF NOT EXISTS mv_logs_hourly AS
SELECT
    date_trunc('hour', timestamp) AS hour,
    provider,
    model,
    status,
    object_type,
    selected_key_id,
    COALESCE(virtual_key_id, '') AS virtual_key_id,
    COALESCE(routing_rule_id, '') AS routing_rule_id,
    COALESCE(user_id, '') AS user_id,
    COALESCE(team_id, '') AS team_id,
    COALESCE(customer_id, '') AS customer_id,
    COALESCE(business_unit_id, '') AS business_unit_id,
    COALESCE(alias, '') AS alias,
    COALESCE(canonical_model_name, '') AS canonical_model_name,
    COUNT(*) AS count,
    SUM(CASE WHEN status = 'success' THEN 1 ELSE 0 END) AS success_count,
    SUM(CASE WHEN status = 'error' THEN 1 ELSE 0 END) AS error_count,
    SUM(CASE WHEN status = 'cancelled' THEN 1 ELSE 0 END) AS cancelled_count,
    COALESCE(AVG(latency), 0) AS avg_latency,
    COALESCE(percentile_cont(0.90) WITHIN GROUP (ORDER BY latency), 0) AS p90_latency,
    COALESCE(percentile_cont(0.95) WITHIN GROUP (ORDER BY latency), 0) AS p95_latency,
    COALESCE(percentile_cont(0.99) WITHIN GROUP (ORDER BY latency), 0) AS p99_latency,
    COALESCE(SUM(prompt_tokens), 0) AS total_prompt_tokens,
    COALESCE(SUM(completion_tokens), 0) AS total_completion_tokens,
    -- Throughput measures restricted to rows with a positive measured latency,
    -- so the tokens/sec matview path matches the raw path exactly (which filters
    -- latency > 0). Without this, a latency=0 success row would add completion
    -- tokens to the numerator with nothing in the denominator and inflate the rate.
    COALESCE(SUM(completion_tokens) FILTER (WHERE latency > 0), 0) AS throughput_completion_tokens,
    COALESCE(SUM(latency) FILTER (WHERE latency > 0), 0) AS throughput_latency_ms,
    COALESCE(COUNT(*) FILTER (WHERE latency > 0), 0) AS throughput_request_count,
    COALESCE(SUM(total_tokens), 0) AS total_tokens,
    COALESCE(SUM(cached_read_tokens), 0) AS total_cached_read_tokens,
    COALESCE(SUM(cost), 0) AS total_cost
FROM logs
WHERE status IN ('success', 'error', 'cancelled')
GROUP BY 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14
`

// mvLogsHourlyUniqueIdx is required for REFRESH MATERIALIZED VIEW CONCURRENTLY.
// CONCURRENTLY avoids the AccessExclusiveLock that the plain form would take
// during startup ensure / repair paths.
const mvLogsHourlyUniqueIdx = `
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS mv_logs_hourly_uniq
ON mv_logs_hourly (hour, provider, model, status, object_type, selected_key_id, virtual_key_id, routing_rule_id, user_id, team_id, customer_id, business_unit_id, alias, canonical_model_name)
`

// mvLogsHourlyRequiredColumns is the canonical column set used by
// repairMatViewShapes to detect old-shape mv_logs_hourly views from prior
// schema versions and drop them so they get rebuilt on startup, and by
// matViewShapesReady to gate the matview read path.
//
// Must mirror every output column of mvLogsHourlyDDL. A partial list would let
// a view missing an unlisted column pass both checks, and readers selecting it
// would fail with "column does not exist" — see TestMvLogsHourlyRequiredColumnsMatchDDL.
var mvLogsHourlyRequiredColumns = []string{
	"hour",
	"provider",
	"model",
	"status",
	"object_type",
	"selected_key_id",
	"virtual_key_id",
	"routing_rule_id",
	"user_id",
	"team_id",
	"customer_id",
	"business_unit_id",
	"alias",
	"canonical_model_name",
	"count",
	"success_count",
	"error_count",
	"cancelled_count",
	"avg_latency",
	"p90_latency",
	"p95_latency",
	"p99_latency",
	"total_prompt_tokens",
	"total_completion_tokens",
	"throughput_completion_tokens",
	"throughput_latency_ms",
	"throughput_request_count",
	"total_tokens",
	"total_cached_read_tokens",
	"total_cost",
}

// legacyMatViewNames are matviews from previous schema versions that no longer
// exist in this branch. Dropped on startup so a deploy from an older release
// doesn't leave orphaned objects (and so REFRESH never picks them up).
//
// Two-phase retirement: a matview is only added here AFTER a release has shipped
// that stops reading from it. Adding it in the same PR that switches readers is
// unsafe — during a rolling deploy the still-old replicas would query a view
// that the new replicas have already dropped, returning "relation does not
// exist". See migrationSplitFilterDataMatView for the canonical example: the
// new per-dimension matviews ship in one release while mv_logs_filterdata stays
// in place; a follow-up release adds it here.
var legacyMatViewNames = []string{}

// Per-dimension filter matviews. The previous single 16-column DISTINCT view
// (mv_logs_filterdata) had a row count proportional to the Cartesian-ish product
// of all dimension cardinalities — for tenants with many users/customers it grew
// nearly as large as the source `logs` table, which made REFRESH MATERIALIZED
// VIEW CONCURRENTLY both memory- and disk-intensive (it builds a full second
// copy and diffs). Splitting per-dimension keeps each view bounded by the
// cardinality of one column (or one pair), so refreshes are cheap and reads are
// constant-time.
//
// Window matches defaultFilterDataCutoffDays (30 days) so the matview path and
// raw-table fallback agree on what "recent" means.
const filterDataMatViewWindow = "30 days"

// filterMatViewDef describes a single per-dimension materialized view.
//
//   - name:            view name
//   - selectExpr:      comma-separated list of result columns (already COALESCEd if needed)
//   - whereExpr:       predicate that filters out empty rows for this dimension
//   - uniqueIdx:       comma-separated columns for the unique index (required for
//     REFRESH ... CONCURRENTLY)
//   - requiredColumns: resolved column aliases used by repairMatViewShapes
//     to detect drifted matviews. Declared explicitly (not parsed from
//     selectExpr) so SQL fragments like COALESCE(...) cannot poison the check.
//   - bodyOverride:    when set, replaces the whole `SELECT DISTINCT ... FROM
//     logs WHERE ...` body (selectExpr/whereExpr are ignored). Used by the
//     multi-valued team / business-unit views, which must union the scalar
//     column with the JSON-array column rather than read a single column.
type filterMatViewDef struct {
	name            string
	selectExpr      string
	whereExpr       string
	uniqueIdx       string
	requiredColumns []string
	bodyOverride    string
}

// multiValueFilterMatViewBody builds the SELECT body for a filter matview whose
// dimension is single-valued on the scalar column (old / pre-migration rows and
// the VK-team path) and multi-valued on the JSON-array column (the enterprise
// user/AP path). It reuses teamOrBUFanoutFrom — the same fan-out the ranking /
// histogram readers use — so a team / business unit that only ever appears in
// the JSON array still surfaces in the filter dropdown. The fanned-out
// dim_id/dim_name become the dropdown id/name; the visibility columns
// (user_id, team_id, virtual_key_id) come from the original log row (exposed via
// l.* by the fan-out subquery) so DAC scope still applies. idCol is the scalar
// id column ("team_id" / "business_unit_id").
func multiValueFilterMatViewBody(idCol string) string {
	from, _ := teamOrBUFanoutFrom(idCol)
	return fmt.Sprintf(
		"SELECT DISTINCT dim_id AS id, dim_name AS name, "+
			"COALESCE(user_id, '') AS user_id, COALESCE(team_id, '') AS team_id, "+
			"COALESCE(virtual_key_id, '') AS virtual_key_id "+
			"FROM %s WHERE timestamp >= NOW() - INTERVAL '%s' AND dim_id != '' AND dim_name != ''",
		from, filterDataMatViewWindow,
	)
}

// scopeProjection is the per-row visibility columns appended to every
// filter matview's SELECT so DAC scope WHERE clauses can be applied at
// the matview level instead of falling back to the raw `logs` table.
// COALESCE keeps NULL values from causing the DISTINCT to multiply (and
// the unique index from rejecting NULLs on REFRESH CONCURRENTLY).
const scopeProjection = "COALESCE(user_id, '') AS user_id, " +
	"COALESCE(team_id, '') AS team_id, " +
	"COALESCE(virtual_key_id, '') AS virtual_key_id"

// scopeIdxColumns is the unique-index suffix that pairs with scopeProjection.
const scopeIdxColumns = "user_id, team_id, virtual_key_id"

// scopeRequiredColumns lists the resolved column aliases produced by
// scopeProjection. Appended to each matview's requiredColumns so
// repairMatViewShapes can verify them against pg_attribute.
var scopeRequiredColumns = []string{"user_id", "team_id", "virtual_key_id"}

// filterMatViews enumerates every per-dimension materialized view used to
// populate filter dropdowns on the logs page. Each view carries the
// dropdown's dimension columns plus the visibility columns
// (user_id, team_id, virtual_key_id) so DAC scope applies in-matview.
// Order matters only for deterministic startup logs.
var filterMatViews = []filterMatViewDef{
	{
		name:            "mv_filter_models",
		selectExpr:      "model, provider, " + scopeProjection,
		whereExpr:       "model IS NOT NULL AND model != ''",
		uniqueIdx:       "model, provider, " + scopeIdxColumns,
		requiredColumns: append([]string{"model", "provider"}, scopeRequiredColumns...),
	},
	{
		name:            "mv_filter_aliases",
		selectExpr:      "alias, " + scopeProjection,
		whereExpr:       "alias IS NOT NULL AND alias != ''",
		uniqueIdx:       "alias, " + scopeIdxColumns,
		requiredColumns: append([]string{"alias"}, scopeRequiredColumns...),
	},
	{
		name:            "mv_filter_stop_reasons",
		selectExpr:      "stop_reason, " + scopeProjection,
		whereExpr:       "stop_reason IS NOT NULL AND stop_reason != ''",
		uniqueIdx:       "stop_reason, " + scopeIdxColumns,
		requiredColumns: append([]string{"stop_reason"}, scopeRequiredColumns...),
	},
	{
		name:            "mv_filter_routing_engines",
		selectExpr:      "routing_engines_used, " + scopeProjection,
		whereExpr:       "routing_engines_used IS NOT NULL AND routing_engines_used != ''",
		uniqueIdx:       "routing_engines_used, " + scopeIdxColumns,
		requiredColumns: append([]string{"routing_engines_used"}, scopeRequiredColumns...),
	},
	{
		name:            "mv_filter_selected_keys",
		selectExpr:      "selected_key_id AS id, selected_key_name AS name, " + scopeProjection,
		whereExpr:       "selected_key_id IS NOT NULL AND selected_key_id != '' AND selected_key_name IS NOT NULL AND selected_key_name != ''",
		uniqueIdx:       "id, name, " + scopeIdxColumns,
		requiredColumns: append([]string{"id", "name"}, scopeRequiredColumns...),
	},
	{
		name: "mv_filter_virtual_keys",
		// virtual_key_id is exposed as "id" for the dropdown and also as the
		// scope column so DAC predicates use a stable name across matviews.
		selectExpr: "virtual_key_id AS id, virtual_key_name AS name, " +
			"COALESCE(user_id, '') AS user_id, COALESCE(team_id, '') AS team_id, " +
			"COALESCE(virtual_key_id, '') AS virtual_key_id",
		whereExpr:       "virtual_key_id IS NOT NULL AND virtual_key_id != '' AND virtual_key_name IS NOT NULL AND virtual_key_name != ''",
		uniqueIdx:       "id, name, " + scopeIdxColumns,
		requiredColumns: append([]string{"id", "name"}, scopeRequiredColumns...),
	},
	{
		name:            "mv_filter_routing_rules",
		selectExpr:      "routing_rule_id AS id, routing_rule_name AS name, " + scopeProjection,
		whereExpr:       "routing_rule_id IS NOT NULL AND routing_rule_id != '' AND routing_rule_name IS NOT NULL AND routing_rule_name != ''",
		uniqueIdx:       "id, name, " + scopeIdxColumns,
		requiredColumns: append([]string{"id", "name"}, scopeRequiredColumns...),
	},
	{
		name: "mv_filter_teams",
		// A request can belong to one team (scalar team_id) or many (JSON-array
		// team_ids, enterprise user/AP path). bodyOverride unions both so every
		// team shows in the dropdown, not just the scalar primary. team_id is
		// exposed as the dropdown "id" and the original row's team_id is kept as
		// the scope column for uniform DAC predicates.
		bodyOverride:    multiValueFilterMatViewBody("team_id"),
		uniqueIdx:       "id, name, " + scopeIdxColumns,
		requiredColumns: append([]string{"id", "name"}, scopeRequiredColumns...),
	},
	{
		name: "mv_filter_customers",
		// A request can carry one customer (scalar customer_id) or many (JSON-array
		// customer_ids, enterprise team↔customer M2M). bodyOverride unions both so
		// every customer shows in the dropdown, not just the scalar primary.
		bodyOverride:    multiValueFilterMatViewBody("customer_id"),
		uniqueIdx:       "id, name, " + scopeIdxColumns,
		requiredColumns: append([]string{"id", "name"}, scopeRequiredColumns...),
	},
	{
		name:            "mv_filter_users",
		selectExpr:      "user_id AS id, user_name AS name, " + scopeProjection,
		whereExpr:       "user_id IS NOT NULL AND user_id != '' AND user_name IS NOT NULL AND user_name != ''",
		uniqueIdx:       "id, name, " + scopeIdxColumns,
		requiredColumns: append([]string{"id", "name"}, scopeRequiredColumns...),
	},
	{
		name: "mv_filter_business_units",
		// Same scalar-or-JSON-array union as mv_filter_teams: a request can carry
		// one business unit (scalar) or many (JSON-array business_unit_ids).
		bodyOverride:    multiValueFilterMatViewBody("business_unit_id"),
		uniqueIdx:       "id, name, " + scopeIdxColumns,
		requiredColumns: append([]string{"id", "name"}, scopeRequiredColumns...),
	},
}

// filterMatViewKeyPairColumns maps the (idCol, nameCol) pair callers pass into
// GetDistinctKeyPairs to the per-dimension matview that pre-aggregates it.
var filterMatViewKeyPairColumns = map[[2]string]string{
	{"selected_key_id", "selected_key_name"}:   "mv_filter_selected_keys",
	{"virtual_key_id", "virtual_key_name"}:     "mv_filter_virtual_keys",
	{"routing_rule_id", "routing_rule_name"}:   "mv_filter_routing_rules",
	{"team_id", "team_name"}:                   "mv_filter_teams",
	{"customer_id", "customer_name"}:           "mv_filter_customers",
	{"user_id", "user_name"}:                   "mv_filter_users",
	{"business_unit_id", "business_unit_name"}: "mv_filter_business_units",
}

func filterMatViewDDL(v filterMatViewDef) string {
	if v.bodyOverride != "" {
		return fmt.Sprintf("CREATE MATERIALIZED VIEW IF NOT EXISTS %s AS %s", v.name, v.bodyOverride)
	}
	return fmt.Sprintf(
		"CREATE MATERIALIZED VIEW IF NOT EXISTS %s AS SELECT DISTINCT %s FROM logs WHERE timestamp >= NOW() - INTERVAL '%s' AND (%s)",
		v.name, v.selectExpr, filterDataMatViewWindow, v.whereExpr,
	)
}

// filterMatViewUniqueIdx returns a CONCURRENTLY-built unique index DDL for the
// view. CONCURRENTLY is required so the index can be created outside any
// transaction (matches the dedicated-conn ensureMatViews path) and avoids
// AccessExclusiveLock during repair.
func filterMatViewUniqueIdx(v filterMatViewDef) string {
	return fmt.Sprintf("CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS %s_uniq ON %s (%s)", v.name, v.name, v.uniqueIdx)
}

// filterMatViewUniqueIdxName is the canonical index name for v's unique index.
func filterMatViewUniqueIdxName(v filterMatViewDef) string {
	return v.name + "_uniq"
}

// filterMatViewScopeIdx returns a CONCURRENTLY-built BTREE index DDL that
// makes DAC-scoped queries index-only. Leading columns are the visibility
// dimensions so a query of the form
// `SELECT DISTINCT <dim> FROM <view> WHERE user_id IN (?)` can satisfy the
// predicate from the index. The unique index already covers the
// unscoped `SELECT DISTINCT <dim>` path via its leading-prefix scan, so
// this index is purely for the scoped path.
//
// CONCURRENTLY makes the build non-blocking; IF NOT EXISTS keeps repeated
// boots idempotent.
func filterMatViewScopeIdx(v filterMatViewDef) string {
	return fmt.Sprintf(
		"CREATE INDEX CONCURRENTLY IF NOT EXISTS %s_scope ON %s (%s)",
		v.name, v.name, scopeIdxColumns,
	)
}

// filterMatViewScopeIdxName is the canonical index name for v's scope index.
func filterMatViewScopeIdxName(v filterMatViewDef) string {
	return v.name + "_scope"
}

// filterMatViewRequiredColumns returns a defensive copy of the resolved
// column aliases used by repairMatViewShapes to detect drifted matviews.
// The list is declared explicitly on each filterMatViewDef so SQL fragments
// in selectExpr (e.g. COALESCE expressions containing commas) cannot poison
// the comparison against pg_attribute.
func filterMatViewRequiredColumns(v filterMatViewDef) []string {
	return append([]string(nil), v.requiredColumns...)
}

type matviewIndexDef struct {
	view   string
	name   string
	sql    string
	unique bool // true for the index that REFRESH MATERIALIZED VIEW CONCURRENTLY requires
}

// matviewUniqueIndexes enumerates every (matview, unique-index) pair this
// package manages. Required for REFRESH ... CONCURRENTLY. Built lazily so
// it always reflects the current filterMatViews list.
var matviewUniqueIndexes = func() []matviewIndexDef {
	defs := []matviewIndexDef{{
		view:   "mv_logs_hourly",
		name:   "mv_logs_hourly_uniq",
		sql:    mvLogsHourlyUniqueIdx,
		unique: true,
	}}
	for _, v := range filterMatViews {
		defs = append(defs, matviewIndexDef{
			view:   v.name,
			name:   filterMatViewUniqueIdxName(v),
			sql:    filterMatViewUniqueIdx(v),
			unique: true,
		})
	}
	return defs
}()

// matviewScopeIndexes enumerates the secondary BTREE indexes that make
// DAC-scoped reads on the per-dimension filter matviews cheap. The
// composite (user_id, team_id, virtual_key_id) is a covering index for
// the only column subset every filter-dropdown query selects, so
// Postgres can serve scoped DISTINCT queries via an index-only scan
// even when only the trailing columns are in the predicate. Filter
// matviews are small (one row per (dim, scope)) and dropdown queries
// are LIMIT-bounded, so the index-only fallback is acceptable for
// non-user-leading scope shapes without paying for per-column indexes
// on every REFRESH MATERIALIZED VIEW CONCURRENTLY. mv_logs_hourly is
// excluded: it is queried via applyMatViewFilters with many WHERE
// shapes (provider/model/status/object_type/key/team/etc.) where the
// planner is better served by bitmap-AND'ing the existing per-shape
// indexes than by an extra 3-column scope index.
var matviewScopeIndexes = func() []matviewIndexDef {
	defs := make([]matviewIndexDef, 0, len(filterMatViews))
	for _, v := range filterMatViews {
		defs = append(defs, matviewIndexDef{
			view: v.name,
			name: filterMatViewScopeIdxName(v),
			sql:  filterMatViewScopeIdx(v),
		})
	}
	return defs
}()

// matviewRequiredColumns drives repairMatViewShapes — any matview present in
// pg_catalog whose column set is missing one of these is treated as
// drifted-from-old-schema and dropped so it gets recreated below. Built
// lazily for the same reason as matviewUniqueIndexes.
var matviewRequiredColumns = func() map[string][]string {
	out := map[string][]string{
		"mv_logs_hourly": mvLogsHourlyRequiredColumns,
	}
	for _, v := range filterMatViews {
		out[v.name] = filterMatViewRequiredColumns(v)
	}
	return out
}()

// ---------------------------------------------------------------------------
// View lifecycle
// ---------------------------------------------------------------------------

// ensureMatViews creates materialized views and their unique indexes if they
// don't already exist. Called once on startup.
//
// Postgres-only — runs on a dedicated connection so the advisory lock + the
// CONCURRENTLY DDL all share one session and live outside any migration
// transaction. Multi-replica deployments serialize on the advisory lock so
// only one instance does the work. It shares the same advisory lock as
// refreshMatViews so startup create/repair cannot overlap a periodic refresh.
//
// Returns false (with a nil error) when another replica holds the lock: nothing
// about the view shape is established then, so callers must verify with
// matViewShapesReady before enabling the matview read path.
func ensureMatViews(ctx context.Context, db *gorm.DB) (bool, error) {
	if db.Dialector.Name() != "postgres" {
		return false, nil
	}

	sqlDB, err := db.DB()
	if err != nil {
		return false, fmt.Errorf("failed to get sql.DB for matview creation: %w", err)
	}

	conn, err := sqlDB.Conn(ctx)
	if err != nil {
		return false, fmt.Errorf("failed to get dedicated connection for matview creation: %w", err)
	}
	defer conn.Close()

	var acquired bool
	if err := conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1)", matviewRefreshAdvisoryLockKey).Scan(&acquired); err != nil {
		return false, fmt.Errorf("failed to try advisory lock for matview creation: %w", err)
	}
	if !acquired {
		// Another replica is doing the work — nothing to do here.
		return false, nil
	}
	defer func() {
		_, _ = conn.ExecContext(ctx, "SELECT pg_advisory_unlock($1)", matviewRefreshAdvisoryLockKey)
	}()

	if err := dropLegacyMatViews(ctx, conn); err != nil {
		return false, err
	}
	if err := repairMatViewShapes(ctx, conn); err != nil {
		return false, err
	}

	ddls := []string{mvLogsHourlyDDL}
	for _, v := range filterMatViews {
		ddls = append(ddls, filterMatViewDDL(v))
	}
	for _, ddl := range ddls {
		if _, err := conn.ExecContext(ctx, ddl); err != nil {
			return false, fmt.Errorf("failed to create materialized view: %w", err)
		}
	}

	if err := ensureMatViewUniqueIndexes(ctx, conn); err != nil {
		return false, err
	}

	return true, nil
}

// matViewShapesReady reports whether every managed materialized view exists and
// carries the full column set this build reads. One read-only pg_catalog query,
// no lock, no DDL — safe to repeat on refresher ticks.
//
// Gates matViewsReady during a rolling deploy: without it a replica that skipped
// the repair reads an old-shape view and gets "column does not exist".
func matViewShapesReady(ctx context.Context, db *gorm.DB) (bool, error) {
	if db.Dialector.Name() != "postgres" {
		return false, nil
	}

	sqlDB, err := db.DB()
	if err != nil {
		return false, fmt.Errorf("failed to get sql.DB for matview shape check: %w", err)
	}

	conn, err := sqlDB.Conn(ctx)
	if err != nil {
		return false, fmt.Errorf("failed to get connection for matview shape check: %w", err)
	}
	defer conn.Close()

	// Flattened into parallel arrays so the check is one round trip. A pair is
	// unsatisfied when the view is absent, shadowed in the search path, or
	// missing the column — all three mean "not ready".
	views := make([]string, 0, len(matviewRequiredColumns)*len(mvLogsHourlyRequiredColumns))
	columns := make([]string, 0, cap(views))
	for view, required := range matviewRequiredColumns {
		for _, column := range required {
			views = append(views, view)
			columns = append(columns, column)
		}
	}

	var missing int
	if err := conn.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM unnest($1::text[], $2::text[]) AS required(view_name, column_name)
		WHERE NOT EXISTS (
			SELECT 1
			FROM pg_class c
			JOIN pg_attribute a ON a.attrelid = c.oid
			WHERE c.relkind = 'm'
			  AND c.relname = required.view_name
			  AND pg_catalog.pg_table_is_visible(c.oid)
			  AND a.attnum > 0
			  AND NOT a.attisdropped
			  AND a.attname = required.column_name
		)
	`, pqTextArray(views), pqTextArray(columns)).Scan(&missing); err != nil {
		return false, fmt.Errorf("failed to inspect matview shapes: %w", err)
	}
	return missing == 0, nil
}

// pqTextArray renders a []string as a Postgres text[] literal, avoiding a
// driver-specific array type for the one place that needs it.
func pqTextArray(values []string) string {
	quoted := make([]string, 0, len(values))
	for _, v := range values {
		quoted = append(quoted, `"`+strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(v)+`"`)
	}
	return "{" + strings.Join(quoted, ",") + "}"
}

// dropLegacyMatViews removes matviews from prior schema versions that no
// longer exist in this branch. CASCADE catches any lingering indexes.
func dropLegacyMatViews(ctx context.Context, conn *sql.Conn) error {
	for _, view := range legacyMatViewNames {
		if _, err := conn.ExecContext(ctx, "DROP MATERIALIZED VIEW IF EXISTS "+view+" CASCADE"); err != nil {
			return fmt.Errorf("failed to drop legacy matview %s: %w", view, err)
		}
	}
	return nil
}

func repairMatViewShapes(ctx context.Context, conn *sql.Conn) error {
	for view, columns := range matviewRequiredColumns {
		needsRebuild, err := matViewNeedsRebuild(ctx, conn, view, columns)
		if err != nil {
			return err
		}
		if !needsRebuild {
			continue
		}
		if _, err := conn.ExecContext(ctx, "DROP MATERIALIZED VIEW IF EXISTS "+view+" CASCADE"); err != nil {
			return fmt.Errorf("failed to drop old-shape matview %s: %w", view, err)
		}
	}
	return nil
}

func matViewNeedsRebuild(ctx context.Context, conn *sql.Conn, view string, requiredColumns []string) (bool, error) {
	var exists bool
	if err := conn.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM pg_class c
			WHERE c.relkind = 'm'
			  AND c.relname = $1
			  AND pg_catalog.pg_table_is_visible(c.oid)
		)
	`, view).Scan(&exists); err != nil {
		return false, fmt.Errorf("failed to check matview %s existence: %w", view, err)
	}
	if !exists {
		return false, nil
	}

	rows, err := conn.QueryContext(ctx, `
		SELECT a.attname
		FROM pg_class c
		JOIN pg_attribute a ON a.attrelid = c.oid
		WHERE c.relkind = 'm'
		  AND c.relname = $1
		  AND pg_catalog.pg_table_is_visible(c.oid)
		  AND a.attnum > 0
		  AND NOT a.attisdropped
	`, view)
	if err != nil {
		return false, fmt.Errorf("failed to inspect matview %s columns: %w", view, err)
	}
	defer rows.Close()

	actual := make(map[string]struct{}, len(requiredColumns))
	for rows.Next() {
		var column string
		if err := rows.Scan(&column); err != nil {
			return false, fmt.Errorf("failed to scan matview %s column: %w", view, err)
		}
		actual[column] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("failed to inspect matview %s columns: %w", view, err)
	}

	for _, column := range requiredColumns {
		if _, ok := actual[column]; !ok {
			return true, nil
		}
	}
	return false, nil
}

func ensureMatViewUniqueIndexes(ctx context.Context, conn *sql.Conn) error {
	_, _ = conn.ExecContext(ctx, "SET maintenance_work_mem = '512MB'")
	_, _ = conn.ExecContext(ctx, "SET max_parallel_maintenance_workers = 4")

	if err := ensureIndexes(ctx, conn, matviewUniqueIndexes); err != nil {
		return err
	}
	return ensureIndexes(ctx, conn, matviewScopeIndexes)
}

// ensureIndexes idempotently brings each index up: skips when the index
// already exists and is valid, otherwise drops any prior invalid version
// (CONCURRENTLY, no blocking) and rebuilds via the supplied DDL. The
// unique flag tightens the validity check for indexes used by REFRESH
// MATERIALIZED VIEW CONCURRENTLY, which requires `indisunique`.
func ensureIndexes(ctx context.Context, conn *sql.Conn, defs []matviewIndexDef) error {
	for _, idx := range defs {
		validityExpr := "pi.indisvalid"
		if idx.unique {
			validityExpr = "pi.indisvalid AND pi.indisunique"
		}
		var indexReady bool
		if err := conn.QueryRowContext(ctx, fmt.Sprintf(`
			SELECT COALESCE(bool_and(%s), false)
			FROM pg_class pc
			JOIN pg_index pi ON pi.indrelid = pc.oid
			JOIN pg_class ic ON ic.oid = pi.indexrelid
			WHERE pc.relname = $1
			  AND ic.relname = $2
		`, validityExpr), idx.view, idx.name).Scan(&indexReady); err != nil {
			return fmt.Errorf("failed to check matview index %s validity: %w", idx.name, err)
		}
		if indexReady {
			continue
		}

		if _, err := conn.ExecContext(ctx, "DROP INDEX CONCURRENTLY IF EXISTS "+idx.name); err != nil {
			return fmt.Errorf("failed to drop invalid matview index %s: %w", idx.name, err)
		}
		if _, err := conn.ExecContext(ctx, idx.sql); err != nil {
			return fmt.Errorf("failed to create matview index %s: %w", idx.name, err)
		}
	}
	return nil
}

// allMatViewNames returns the names of every materialized view this package
// manages, in refresh order (hourly first so dashboards prioritize over
// filter-dropdown freshness).
func allMatViewNames() []string {
	names := []string{"mv_logs_hourly"}
	for _, v := range filterMatViews {
		names = append(names, v.name)
	}
	return names
}

// matViewRefreshSafetyInterval forces a periodic refresh even when
// pg_stat_user_tables shows no DML on `logs`. This guards against two
// edge cases:
//  1. The 30-day window in mv_filter_* — old rows aging past the cutoff need
//     to be evicted from the matview even on write-quiet clusters.
//  2. Any drift if the stat collector lags or undercounts.
//
// 10 minutes is short enough that aged-out filter values disappear within an
// acceptable window, long enough that idle clusters do ~6 refreshes/hour
// instead of 120.
const matViewRefreshSafetyInterval = 10 * time.Minute

// matViewRefreshGate tracks the last-seen activity counter on `logs` from
// pg_stat_user_tables so refreshMatViews can short-circuit when nothing has
// changed. Per-process state — multi-replica deployments still serialize via
// the advisory lock.
type matViewRefreshGate struct {
	mu           sync.Mutex
	lastActivity int64
	lastForcedAt time.Time
	initialized  bool
}

var refreshGate matViewRefreshGate

// logsActivityCounter returns the cumulative INSERT+UPDATE+DELETE count for
// the `logs` table from pg_stat_user_tables. The stat collector is eventually
// consistent (lags a few seconds under load) which is fine for the periodic
// refresh tick.
//
// Returns (0, false) if the row is missing (fresh DB before any writes) or the
// query fails — callers treat that as "fall back to always-refresh."
func logsActivityCounter(ctx context.Context, conn *sql.Conn) (int64, bool) {
	var activity sql.NullInt64
	err := conn.QueryRowContext(ctx, `
		SELECT COALESCE(n_tup_ins, 0) + COALESCE(n_tup_upd, 0) + COALESCE(n_tup_del, 0)
		FROM pg_stat_user_tables
		WHERE relname = 'logs' AND schemaname = current_schema()
	`).Scan(&activity)
	if err != nil || !activity.Valid {
		return 0, false
	}
	return activity.Int64, true
}

// shouldSkip reports whether refreshMatViews can no-op for this tick.
// Returns true only when (a) we have a baseline from a prior refresh,
// (b) the activity counter is unchanged, AND (c) we're inside the safety
// interval. Any uncertainty falls through to "do the refresh."
func (g *matViewRefreshGate) shouldSkip(currentActivity int64, currentActivityOK bool) bool {
	if !currentActivityOK {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.initialized {
		return false
	}
	if time.Since(g.lastForcedAt) >= matViewRefreshSafetyInterval {
		return false
	}
	return currentActivity == g.lastActivity
}

// markRefreshed records a successful refresh. activityAtStart is the counter
// captured BEFORE the refresh ran — using it (rather than the post-refresh
// counter) ensures writes that landed during the refresh are picked up on the
// next tick.
func (g *matViewRefreshGate) markRefreshed(activityAtStart int64, activityOK bool) {
	if !activityOK {
		// We refreshed but couldn't read the counter — keep state uninitialized so
		// the next tick will refresh again rather than silently skipping forever.
		return
	}
	g.mu.Lock()
	g.lastActivity = activityAtStart
	g.lastForcedAt = time.Now()
	g.initialized = true
	g.mu.Unlock()
}

// refreshMatViews refreshes all materialized views concurrently (non-blocking
// for readers). Uses a PostgreSQL advisory try-lock so that in multi-replica
// deployments only one instance refreshes at a time — others skip silently and
// try again on their next scheduled tick.
//
// Also short-circuits when pg_stat_user_tables reports no INSERT/UPDATE/DELETE
// on `logs` since the last refresh. A periodic safety-interval refresh runs
// regardless so the rolling 30-day filter window evicts aged-out rows.
func refreshMatViews(ctx context.Context, db *gorm.DB) error {
	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("failed to get sql.DB for matview refresh: %w", err)
	}

	// Use a dedicated connection so lock/unlock/refresh all run on the same session.
	conn, err := sqlDB.Conn(ctx)
	if err != nil {
		return fmt.Errorf("failed to get dedicated connection for matview refresh: %w", err)
	}
	defer conn.Close()

	// Activity check happens before the advisory lock so write-quiet replicas
	// don't even contend for it. Capture the counter BEFORE refreshing — any
	// writes that land during refresh will bump it again and trigger the next
	// tick.
	activityAtStart, activityOK := logsActivityCounter(ctx, conn)
	if refreshGate.shouldSkip(activityAtStart, activityOK) {
		return nil
	}

	// Try to acquire advisory lock; skip refresh if another replica holds it.
	var acquired bool
	if err := conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1)", matviewRefreshAdvisoryLockKey).Scan(&acquired); err != nil {
		return fmt.Errorf("failed to try advisory lock for matview refresh: %w", err)
	}
	if !acquired {
		return nil // another replica is refreshing
	}
	defer func() {
		// Release lock explicitly; connection close would also release session-scoped locks.
		_, _ = conn.ExecContext(ctx, "SELECT pg_advisory_unlock($1)", matviewRefreshAdvisoryLockKey)
	}()

	for _, view := range allMatViewNames() {
		if _, err := conn.ExecContext(ctx, "REFRESH MATERIALIZED VIEW CONCURRENTLY "+view); err != nil {
			return fmt.Errorf("failed to refresh %s: %w", view, err)
		}
	}
	refreshGate.markRefreshed(activityAtStart, activityOK)
	return nil
}

// startMatViewRefresher launches a background goroutine that periodically
// refreshes materialized views. If readyFlag is provided and not yet true,
// it will be set to true on the first successful refresh (recovery path when
// the initial refresh failed). Returns a stop function for graceful shutdown.
func startMatViewRefresher(ctx context.Context, db *gorm.DB, interval time.Duration, logger schemas.Logger, readyFlag *atomic.Bool) func() {
	stopCh := make(chan struct{})
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := refreshMatViews(ctx, db); err != nil {
					logger.Warn(fmt.Sprintf("logstore: matview refresh failed: %s", err))
				} else if readyFlag != nil && !readyFlag.Load() {
					// A successful refresh is not evidence of the right shape:
					// REFRESH works fine on an old-shape view, and refreshMatViews
					// also returns nil when it skipped. Check the catalog.
					shapesOK, err := matViewShapesReady(ctx, db)
					if err != nil {
						logger.Warn(fmt.Sprintf("logstore: matview shape check failed: %s (dashboard queries will use raw tables)", err))
					} else if shapesOK {
						logger.Info("logstore: materialized views are ready (recovered)")
						readyFlag.Store(true)
					}
				}
			case <-ctx.Done():
				return
			case <-stopCh:
				return
			}
		}
	}()
	return func() { close(stopCh) }
}

// canUseMatViewFilters returns true if the given filters can be served from
// mv_logs_hourly. Per-row filters (content search, metadata, numeric ranges)
// require the raw logs table.
func canUseMatViewFilters(f SearchFilters) bool {
	return f.ContentSearch == "" &&
		len(f.MetadataFilters) == 0 &&
		canUseMatViewStatusFilter(f.Status) &&
		len(f.RoutingEngineUsed) == 0 &&
		len(f.StopReasons) == 0 &&
		f.MinLatency == nil && f.MaxLatency == nil &&
		f.MinTokens == nil && f.MaxTokens == nil &&
		f.MinCost == nil && f.MaxCost == nil &&
		!f.MissingCostOnly &&
		len(f.CacheHitTypes) == 0 &&
		len(f.TeamIDs) == 0 &&
		len(f.BusinessUnitIDs) == 0 &&
		len(f.CustomerIDs) == 0
}

func canUseMatViewStatusFilter(statuses []string) bool {
	for _, status := range statuses {
		if !isTerminalLogStatus(status) {
			return false
		}
	}
	return true
}

func isTerminalLogStatus(status string) bool {
	for _, terminalStatus := range terminalLogStatuses {
		if status == terminalStatus {
			return true
		}
	}
	return false
}

// canUseMatView checks both that materialized views are ready (created and
// populated) and that the given filters are eligible for the matview path.
// This prevents queries from hitting non-existent views during the startup
// window between migration (which drops old views) and ensureMatViews (which
// recreates them asynchronously).
func (s *RDBLogStore) canUseMatView(f SearchFilters) bool {
	return s.matViewsReady.Load() && canUseMatViewFilters(f)
}

// freshAggregateMatViewMinWindow is the minimum time-range size that justifies
// serving user-visible aggregates (e.g. /api/logs/stats totals, /api/logs
// pagination counts) from the materialized view. Below this, the raw `logs`
// table is fast enough and avoids the refresh-interval freshness lag that would
// otherwise cause counts to disagree with the row-list view — a visible
// inconsistency on short, low-traffic windows.
const freshAggregateMatViewMinWindow = 24 * time.Hour

// canUseMatViewForFreshAggregate narrows canUseMatView with a time-window check.
// The matview wins on long ranges (multi-day aggregations would otherwise
// require a full-table scan) but loses on short ranges, where raw aggregation
// is cheap and the staleness becomes visible. Only fully unbounded ranges
// (both StartTime and EndTime nil) are treated as matview-safe — a half-bounded
// range has no measurable width and could still be a short window.
//
// Used by /api/logs/stats (full metric payload), /api/logs (pagination total
// count), and the model/user/dimension ranking readers so all those surfaces
// stay consistent on the same window.
func (s *RDBLogStore) canUseMatViewForFreshAggregate(f SearchFilters) bool {
	if !s.canUseMatView(f) {
		return false
	}
	if f.StartTime == nil && f.EndTime == nil {
		return true
	}
	if f.StartTime == nil || f.EndTime == nil {
		return false
	}
	return f.EndTime.Sub(*f.StartTime) >= freshAggregateMatViewMinWindow
}

// ---------------------------------------------------------------------------
// Mat-view filter helpers
// ---------------------------------------------------------------------------

// applyMatViewFilters builds WHERE clauses for queries against mv_logs_hourly.
// Callers are responsible for starting from ScopedDB(ctx) when row
// visibility should be respected; this helper only adds the matview
// filter predicates.
func (s *RDBLogStore) applyMatViewFilters(q *gorm.DB, f SearchFilters) *gorm.DB {
	return applyMatViewFiltersOnly(q, f)
}

// applyMatViewFiltersOnly builds WHERE clauses for queries against
// mv_logs_hourly without applying visibility. Kept separate so the visibility
// pass happens exactly once (at the entry point) and unit tests of the filter
// translation don't need a context.
func applyMatViewFiltersOnly(q *gorm.DB, f SearchFilters) *gorm.DB {
	if f.StartTime != nil {
		q = q.Where("hour >= date_trunc('hour', ?::timestamptz)", *f.StartTime)
	}
	if f.EndTime != nil {
		q = q.Where("hour <= ?", *f.EndTime)
	}
	if len(f.Providers) > 0 {
		q = q.Where("provider IN ?", f.Providers)
	}
	if len(f.Models) > 0 {
		q = q.Where("model IN ?", f.Models)
	}
	if len(f.Aliases) > 0 {
		q = q.Where("alias IN ?", f.Aliases)
	}
	if len(f.Status) > 0 {
		q = q.Where("status IN ?", f.Status)
	}
	if len(f.Objects) > 0 {
		q = q.Where("object_type IN ?", f.Objects)
	}
	if len(f.SelectedKeyIDs) > 0 {
		q = q.Where("selected_key_id IN ?", f.SelectedKeyIDs)
	}
	if len(f.VirtualKeyIDs) > 0 {
		q = q.Where("virtual_key_id IN ?", f.VirtualKeyIDs)
	}
	if len(f.RoutingRuleIDs) > 0 {
		q = q.Where("routing_rule_id IN ?", f.RoutingRuleIDs)
	}
	if len(f.TeamIDs) > 0 {
		q = q.Where("team_id IN ?", f.TeamIDs)
	}
	if len(f.CustomerIDs) > 0 {
		q = q.Where("customer_id IN ?", f.CustomerIDs)
	}
	if len(f.UserIDs) > 0 {
		q = q.Where("user_id IN ?", f.UserIDs)
	}
	if len(f.BusinessUnitIDs) > 0 {
		q = q.Where("business_unit_id IN ?", f.BusinessUnitIDs)
	}
	return q
}

// ---------------------------------------------------------------------------
// Mat-view query methods (called from rdb.go when dialect == "postgres")
// ---------------------------------------------------------------------------

// getCountFromMatView returns the total number of logs matching the filters
// by summing pre-aggregated counts from mv_logs_hourly.
func (s *RDBLogStore) getCountFromMatView(ctx context.Context, filters SearchFilters) (int64, error) {
	var total int64
	q := s.ScopedDB(ctx).Table("mv_logs_hourly")
	q = s.applyMatViewFilters(q, filters)
	if err := q.Select("COALESCE(SUM(count), 0)").Row().Scan(&total); err != nil {
		return 0, err
	}
	return total, nil
}

// getStatsFromMatView computes dashboard statistics (total requests, success
// rate, average latency, total tokens, total cost) from mv_logs_hourly.
// Latency is a weighted average across hourly buckets.
func (s *RDBLogStore) getStatsFromMatView(ctx context.Context, filters SearchFilters) (*SearchStats, error) {
	var result struct {
		TotalCount       int64   `gorm:"column:total_count"`
		SuccessCount     int64   `gorm:"column:success_count"`
		AvgLatency       float64 `gorm:"column:avg_latency"`
		TotalTokens      int64   `gorm:"column:total_tokens"`
		PromptTokens     int64   `gorm:"column:prompt_tokens"`
		CompletionTokens int64   `gorm:"column:completion_tokens"`
		TotalCost        float64 `gorm:"column:total_cost"`
	}
	q := s.ScopedDB(ctx).Table("mv_logs_hourly")
	q = s.applyMatViewFilters(q, filters)
	if err := q.Select(`
		COALESCE(SUM(count), 0) AS total_count,
		COALESCE(SUM(success_count), 0) AS success_count,
		CASE WHEN SUM(count) > 0 THEN SUM(avg_latency * count) / SUM(count) ELSE 0 END AS avg_latency,
		COALESCE(SUM(total_tokens), 0) AS total_tokens,
		COALESCE(SUM(total_prompt_tokens), 0) AS prompt_tokens,
		COALESCE(SUM(total_completion_tokens), 0) AS completion_tokens,
		COALESCE(SUM(total_cost), 0) AS total_cost
	`).Scan(&result).Error; err != nil {
		return nil, err
	}

	var successRate float64
	if result.TotalCount > 0 {
		successRate = float64(result.SuccessCount) / float64(result.TotalCount) * 100
	}

	// User-facing success rate requires per-request fallback chain data which is not
	// available in the materialized view. Scanning the raw logs table on large datasets
	// (>100 GB) can take minutes, so the matview path uses the per-attempt success rate
	// as a fast approximation. Accurate chain-level computation runs in the raw-table path.

	cacheHitRateTotalRequests := result.TotalCount
	stats := &SearchStats{
		TotalRequests:             result.TotalCount,
		SuccessRate:               successRate,
		UserFacingSuccessRate:     successRate,
		UserFacingTotalRequests:   result.TotalCount, // matview approximation; no per-chain data available
		AverageLatency:            result.AvgLatency,
		TotalTokens:               result.TotalTokens,
		PromptTokens:              result.PromptTokens,
		CompletionTokens:          result.CompletionTokens,
		TotalCost:                 result.TotalCost,
		CacheHitRateTotalRequests: &cacheHitRateTotalRequests,
	}

	// cache_debug is not stored in the matview — query the raw logs table directly.
	// Align the time window to hour boundaries to match the matview denominator (TotalRequests).
	alignedFilters := filters
	if filters.StartTime != nil {
		aligned := filters.StartTime.Truncate(time.Hour)
		alignedFilters.StartTime = &aligned
	}
	if filters.EndTime != nil {
		alignedEnd := filters.EndTime.Truncate(time.Hour).Add(time.Hour - time.Nanosecond)
		alignedFilters.EndTime = &alignedEnd
	}
	cacheBase := s.ScopedDB(ctx).Model(&Log{}).Where("status IN ?", terminalLogStatuses)
	direct, semantic, err := s.aggregateCacheHits(ctx, cacheBase, alignedFilters)
	if err != nil {
		s.logger.Warn(fmt.Sprintf("logstore: failed to aggregate cache-hit stats, skipping: %s", err))
	} else if direct != nil || semantic != nil {
		stats.DirectCacheHits = direct
		stats.SemanticCacheHits = semantic
	}

	return stats, nil
}

// getHistogramFromMatView returns time-bucketed request counts (total,
// success, error, cancelled) by re-aggregating hourly buckets from mv_logs_hourly.
func (s *RDBLogStore) getHistogramFromMatView(ctx context.Context, filters SearchFilters, bucketSizeSeconds int64) (*HistogramResult, error) {
	var results []struct {
		BucketTimestamp int64 `gorm:"column:bucket_timestamp"`
		Total           int64 `gorm:"column:total"`
		Success         int64 `gorm:"column:success"`
		ErrorCount      int64 `gorm:"column:error_count"`
		CancelledCount  int64 `gorm:"column:cancelled_count"`
	}
	q := s.ScopedDB(ctx).Table("mv_logs_hourly")
	q = s.applyMatViewFilters(q, filters)
	if err := q.Select(fmt.Sprintf(`
		CAST(FLOOR(EXTRACT(EPOCH FROM hour) / %d) * %d AS BIGINT) AS bucket_timestamp,
		SUM(count) AS total,
		SUM(success_count) AS success,
		SUM(error_count) AS error_count,
		SUM(cancelled_count) AS cancelled_count
	`, bucketSizeSeconds, bucketSizeSeconds)).
		Group("bucket_timestamp").
		Order("bucket_timestamp ASC").
		Find(&results).Error; err != nil {
		return nil, err
	}

	resultMap := make(map[int64]*struct{ total, success, errCount, cancelledCount int64 }, len(results))
	for _, r := range results {
		resultMap[r.BucketTimestamp] = &struct{ total, success, errCount, cancelledCount int64 }{r.Total, r.Success, r.ErrorCount, r.CancelledCount}
	}

	allTimestamps := generateBucketTimestamps(filters.StartTime, filters.EndTime, bucketSizeSeconds)
	buckets := make([]HistogramBucket, 0, len(allTimestamps))
	for _, ts := range allTimestamps {
		b := HistogramBucket{Timestamp: time.Unix(ts, 0).UTC()}
		if a, ok := resultMap[ts]; ok {
			b.Count = a.total
			b.Success = a.success
			b.Error = a.errCount
			b.Cancelled = a.cancelledCount
		}
		buckets = append(buckets, b)
	}
	return &HistogramResult{Buckets: buckets, BucketSizeSeconds: bucketSizeSeconds}, nil
}

// getTokenHistogramFromMatView returns time-bucketed token usage (prompt,
// completion, total, cached) from mv_logs_hourly.
func (s *RDBLogStore) getTokenHistogramFromMatView(ctx context.Context, filters SearchFilters, bucketSizeSeconds int64) (*TokenHistogramResult, error) {
	var results []struct {
		BucketTimestamp  int64 `gorm:"column:bucket_timestamp"`
		PromptTokens     int64 `gorm:"column:prompt_tokens"`
		CompletionTokens int64 `gorm:"column:completion_tokens"`
		TotalTokens      int64 `gorm:"column:total_tkns"`
		CachedReadTokens int64 `gorm:"column:cached_read_tokens"`
	}
	q := s.ScopedDB(ctx).Table("mv_logs_hourly")
	q = s.applyMatViewFilters(q, filters)
	if err := q.Select(fmt.Sprintf(`
		CAST(FLOOR(EXTRACT(EPOCH FROM hour) / %d) * %d AS BIGINT) AS bucket_timestamp,
		SUM(total_prompt_tokens) AS prompt_tokens,
		SUM(total_completion_tokens) AS completion_tokens,
		SUM(total_tokens) AS total_tkns,
		SUM(total_cached_read_tokens) AS cached_read_tokens
	`, bucketSizeSeconds, bucketSizeSeconds)).
		Group("bucket_timestamp").
		Order("bucket_timestamp ASC").
		Find(&results).Error; err != nil {
		return nil, err
	}

	resultMap := make(map[int64]int, len(results))
	for i, r := range results {
		resultMap[r.BucketTimestamp] = i
	}

	allTimestamps := generateBucketTimestamps(filters.StartTime, filters.EndTime, bucketSizeSeconds)
	buckets := make([]TokenHistogramBucket, 0, len(allTimestamps))
	for _, ts := range allTimestamps {
		b := TokenHistogramBucket{Timestamp: time.Unix(ts, 0).UTC()}
		if idx, ok := resultMap[ts]; ok {
			r := results[idx]
			b.PromptTokens = r.PromptTokens
			b.CompletionTokens = r.CompletionTokens
			b.TotalTokens = r.TotalTokens
			b.CachedReadTokens = r.CachedReadTokens
		}
		buckets = append(buckets, b)
	}
	return &TokenHistogramResult{Buckets: buckets, BucketSizeSeconds: bucketSizeSeconds}, nil
}

// getCostHistogramFromMatView returns time-bucketed cost data with per-model
// breakdown from mv_logs_hourly.
func (s *RDBLogStore) getCostHistogramFromMatView(ctx context.Context, filters SearchFilters, bucketSizeSeconds int64) (*CostHistogramResult, error) {
	var results []struct {
		BucketTimestamp int64   `gorm:"column:bucket_timestamp"`
		Model           string  `gorm:"column:model"`
		Cost            float64 `gorm:"column:cost"`
	}
	q := s.ScopedDB(ctx).Table("mv_logs_hourly")
	q = s.applyMatViewFilters(q, filters)
	if err := q.Select(fmt.Sprintf(`
		CAST(FLOOR(EXTRACT(EPOCH FROM hour) / %d) * %d AS BIGINT) AS bucket_timestamp,
		model,
		SUM(total_cost) AS cost
	`, bucketSizeSeconds, bucketSizeSeconds)).
		Group("bucket_timestamp, model").
		Order("bucket_timestamp ASC").
		Find(&results).Error; err != nil {
		return nil, err
	}

	type bucketAgg struct {
		totalCost float64
		byModel   map[string]float64
	}
	grouped := make(map[int64]*bucketAgg)
	modelsSet := make(map[string]struct{})
	for _, r := range results {
		a, ok := grouped[r.BucketTimestamp]
		if !ok {
			a = &bucketAgg{byModel: make(map[string]float64)}
			grouped[r.BucketTimestamp] = a
		}
		a.totalCost += r.Cost
		a.byModel[r.Model] += r.Cost
		modelsSet[r.Model] = struct{}{}
	}

	allTimestamps := generateBucketTimestamps(filters.StartTime, filters.EndTime, bucketSizeSeconds)
	buckets := make([]CostHistogramBucket, 0, len(allTimestamps))
	for _, ts := range allTimestamps {
		b := CostHistogramBucket{Timestamp: time.Unix(ts, 0).UTC(), ByModel: make(map[string]float64)}
		if a, ok := grouped[ts]; ok {
			b.TotalCost = a.totalCost
			b.ByModel = a.byModel
		}
		buckets = append(buckets, b)
	}

	models := sortedStringKeys(modelsSet)
	return &CostHistogramResult{Buckets: buckets, BucketSizeSeconds: bucketSizeSeconds, Models: models}, nil
}

// getModelHistogramFromMatView returns time-bucketed model usage with
// success/error/cancelled breakdown per model from mv_logs_hourly.
func (s *RDBLogStore) getModelHistogramFromMatView(ctx context.Context, filters SearchFilters, bucketSizeSeconds int64) (*ModelHistogramResult, error) {
	var results []struct {
		BucketTimestamp int64  `gorm:"column:bucket_timestamp"`
		Model           string `gorm:"column:model"`
		Total           int64  `gorm:"column:total"`
		Success         int64  `gorm:"column:success"`
		ErrorCount      int64  `gorm:"column:error_count"`
		CancelledCount  int64  `gorm:"column:cancelled_count"`
	}
	q := s.ScopedDB(ctx).Table("mv_logs_hourly")
	q = s.applyMatViewFilters(q, filters)
	if err := q.Select(fmt.Sprintf(`
		CAST(FLOOR(EXTRACT(EPOCH FROM hour) / %d) * %d AS BIGINT) AS bucket_timestamp,
		model,
		SUM(count) AS total,
		SUM(success_count) AS success,
		SUM(error_count) AS error_count,
		SUM(cancelled_count) AS cancelled_count
	`, bucketSizeSeconds, bucketSizeSeconds)).
		Group("bucket_timestamp, model").
		Order("bucket_timestamp ASC").
		Find(&results).Error; err != nil {
		return nil, err
	}

	type bucketAgg struct {
		byModel map[string]ModelUsageStats
	}
	grouped := make(map[int64]*bucketAgg)
	modelsSet := make(map[string]struct{})
	for _, r := range results {
		a, ok := grouped[r.BucketTimestamp]
		if !ok {
			a = &bucketAgg{byModel: make(map[string]ModelUsageStats)}
			grouped[r.BucketTimestamp] = a
		}
		existing := a.byModel[r.Model]
		existing.Total += r.Total
		existing.Success += r.Success
		existing.Error += r.ErrorCount
		existing.Cancelled += r.CancelledCount
		a.byModel[r.Model] = existing
		modelsSet[r.Model] = struct{}{}
	}

	allTimestamps := generateBucketTimestamps(filters.StartTime, filters.EndTime, bucketSizeSeconds)
	buckets := make([]ModelHistogramBucket, 0, len(allTimestamps))
	for _, ts := range allTimestamps {
		b := ModelHistogramBucket{Timestamp: time.Unix(ts, 0).UTC(), ByModel: make(map[string]ModelUsageStats)}
		if a, ok := grouped[ts]; ok {
			b.ByModel = a.byModel
		}
		buckets = append(buckets, b)
	}

	models := sortedStringKeys(modelsSet)
	return &ModelHistogramResult{Buckets: buckets, BucketSizeSeconds: bucketSizeSeconds, Models: models}, nil
}

// getLatencyHistogramFromMatView returns time-bucketed latency percentiles
// (avg, p90, p95, p99) from mv_logs_hourly. Percentiles are re-aggregated
// across hourly buckets using weighted averages (weighted by request count).
func (s *RDBLogStore) getLatencyHistogramFromMatView(ctx context.Context, filters SearchFilters, bucketSizeSeconds int64) (*LatencyHistogramResult, error) {
	var results []struct {
		BucketTimestamp int64   `gorm:"column:bucket_timestamp"`
		AvgLatency      float64 `gorm:"column:avg_lat"`
		P90Latency      float64 `gorm:"column:p90_lat"`
		P95Latency      float64 `gorm:"column:p95_lat"`
		P99Latency      float64 `gorm:"column:p99_lat"`
		TotalRequests   int64   `gorm:"column:total_requests"`
	}
	// Weighted average of percentiles across hourly buckets
	q := s.ScopedDB(ctx).Table("mv_logs_hourly")
	q = s.applyMatViewFilters(q, filters)
	if err := q.Select(fmt.Sprintf(`
		CAST(FLOOR(EXTRACT(EPOCH FROM hour) / %d) * %d AS BIGINT) AS bucket_timestamp,
		CASE WHEN SUM(count) > 0 THEN SUM(avg_latency * count) / SUM(count) ELSE 0 END AS avg_lat,
		CASE WHEN SUM(count) > 0 THEN SUM(p90_latency * count) / SUM(count) ELSE 0 END AS p90_lat,
		CASE WHEN SUM(count) > 0 THEN SUM(p95_latency * count) / SUM(count) ELSE 0 END AS p95_lat,
		CASE WHEN SUM(count) > 0 THEN SUM(p99_latency * count) / SUM(count) ELSE 0 END AS p99_lat,
		SUM(count) AS total_requests
	`, bucketSizeSeconds, bucketSizeSeconds)).
		Group("bucket_timestamp").
		Order("bucket_timestamp ASC").
		Find(&results).Error; err != nil {
		return nil, err
	}

	resultMap := make(map[int64]int, len(results))
	for i, r := range results {
		resultMap[r.BucketTimestamp] = i
	}

	allTimestamps := generateBucketTimestamps(filters.StartTime, filters.EndTime, bucketSizeSeconds)
	buckets := make([]LatencyHistogramBucket, 0, len(allTimestamps))
	for _, ts := range allTimestamps {
		b := LatencyHistogramBucket{Timestamp: time.Unix(ts, 0).UTC()}
		if idx, ok := resultMap[ts]; ok {
			r := results[idx]
			b.AvgLatency = r.AvgLatency
			b.P90Latency = r.P90Latency
			b.P95Latency = r.P95Latency
			b.P99Latency = r.P99Latency
			b.TotalRequests = r.TotalRequests
		}
		buckets = append(buckets, b)
	}
	return &LatencyHistogramResult{Buckets: buckets, BucketSizeSeconds: bucketSizeSeconds}, nil
}

// getThroughputHistogramFromMatView returns time-bucketed token-generation
// throughput (tokens/sec) from mv_logs_hourly. It sums the precomputed
// positive-latency measures (throughput_completion_tokens / throughput_latency_ms
// / throughput_request_count), which the matview restricts to rows with
// latency > 0 exactly as the raw path does — so both paths return the same value.
func (s *RDBLogStore) getThroughputHistogramFromMatView(ctx context.Context, filters SearchFilters, bucketSizeSeconds int64) (*ThroughputHistogramResult, error) {
	var results []struct {
		BucketTimestamp  int64   `gorm:"column:bucket_timestamp"`
		CompletionTokens int64   `gorm:"column:completion_tokens"`
		SumLatency       float64 `gorm:"column:sum_latency"`
		TotalRequests    int64   `gorm:"column:total_requests"`
	}
	q := s.ScopedDB(ctx).Table("mv_logs_hourly")
	q = s.applyMatViewFilters(q, filters)
	// Successful requests only: status is a matview dimension, so this restricts
	// the summed tokens/latency/count to success rows (see GetThroughputHistogram).
	q = q.Where("status = ?", "success")
	if err := q.Select(fmt.Sprintf(`
		CAST(FLOOR(EXTRACT(EPOCH FROM hour) / %d) * %d AS BIGINT) AS bucket_timestamp,
		COALESCE(SUM(throughput_completion_tokens), 0) AS completion_tokens,
		COALESCE(SUM(throughput_latency_ms), 0) AS sum_latency,
		COALESCE(SUM(throughput_request_count), 0) AS total_requests
	`, bucketSizeSeconds, bucketSizeSeconds)).
		Group("bucket_timestamp").
		Order("bucket_timestamp ASC").
		Find(&results).Error; err != nil {
		return nil, err
	}

	resultMap := make(map[int64]int, len(results))
	for i, r := range results {
		resultMap[r.BucketTimestamp] = i
	}

	allTimestamps := generateBucketTimestamps(filters.StartTime, filters.EndTime, bucketSizeSeconds)
	buckets := make([]ThroughputHistogramBucket, 0, len(allTimestamps))
	for _, ts := range allTimestamps {
		b := ThroughputHistogramBucket{Timestamp: time.Unix(ts, 0).UTC()}
		if idx, ok := resultMap[ts]; ok {
			r := results[idx]
			b.TokensPerSecond = tokensPerSecond(r.CompletionTokens, r.SumLatency)
			b.TotalCompletionTokens = r.CompletionTokens
			b.TotalRequests = r.TotalRequests
		}
		buckets = append(buckets, b)
	}
	return &ThroughputHistogramResult{Buckets: buckets, BucketSizeSeconds: bucketSizeSeconds}, nil
}

// getProviderThroughputHistogramFromMatView returns time-bucketed tokens/sec
// with per-provider breakdown from mv_logs_hourly.
func (s *RDBLogStore) getProviderThroughputHistogramFromMatView(ctx context.Context, filters SearchFilters, bucketSizeSeconds int64) (*ProviderThroughputHistogramResult, error) {
	var results []struct {
		BucketTimestamp  int64   `gorm:"column:bucket_timestamp"`
		Provider         string  `gorm:"column:provider"`
		CompletionTokens int64   `gorm:"column:completion_tokens"`
		SumLatency       float64 `gorm:"column:sum_latency"`
		TotalRequests    int64   `gorm:"column:total_requests"`
	}
	q := s.ScopedDB(ctx).Table("mv_logs_hourly")
	q = s.applyMatViewFilters(q, filters)
	// Successful requests only — see getThroughputHistogramFromMatView.
	q = q.Where("status = ?", "success")
	if err := q.Select(fmt.Sprintf(`
		CAST(FLOOR(EXTRACT(EPOCH FROM hour) / %d) * %d AS BIGINT) AS bucket_timestamp,
		provider,
		COALESCE(SUM(throughput_completion_tokens), 0) AS completion_tokens,
		COALESCE(SUM(throughput_latency_ms), 0) AS sum_latency,
		COALESCE(SUM(throughput_request_count), 0) AS total_requests
	`, bucketSizeSeconds, bucketSizeSeconds)).
		Group("bucket_timestamp, provider").
		Order("bucket_timestamp ASC").
		Find(&results).Error; err != nil {
		return nil, err
	}

	type bucketAgg struct {
		byProvider map[string]ProviderThroughputStats
	}
	grouped := make(map[int64]*bucketAgg)
	providersSet := make(map[string]struct{})
	for _, r := range results {
		a, ok := grouped[r.BucketTimestamp]
		if !ok {
			a = &bucketAgg{byProvider: make(map[string]ProviderThroughputStats)}
			grouped[r.BucketTimestamp] = a
		}
		a.byProvider[r.Provider] = ProviderThroughputStats{
			TokensPerSecond:       tokensPerSecond(r.CompletionTokens, r.SumLatency),
			TotalCompletionTokens: r.CompletionTokens,
			TotalRequests:         r.TotalRequests,
		}
		providersSet[r.Provider] = struct{}{}
	}

	allTimestamps := generateBucketTimestamps(filters.StartTime, filters.EndTime, bucketSizeSeconds)
	buckets := make([]ProviderThroughputHistogramBucket, 0, len(allTimestamps))
	for _, ts := range allTimestamps {
		b := ProviderThroughputHistogramBucket{Timestamp: time.Unix(ts, 0).UTC(), ByProvider: make(map[string]ProviderThroughputStats)}
		if a, ok := grouped[ts]; ok {
			b.ByProvider = a.byProvider
		}
		buckets = append(buckets, b)
	}

	providers := sortedStringKeys(providersSet)
	return &ProviderThroughputHistogramResult{Buckets: buckets, BucketSizeSeconds: bucketSizeSeconds, Providers: providers}, nil
}

// getProviderCostHistogramFromMatView returns time-bucketed cost data with
// per-provider breakdown from mv_logs_hourly.
func (s *RDBLogStore) getProviderCostHistogramFromMatView(ctx context.Context, filters SearchFilters, bucketSizeSeconds int64) (*ProviderCostHistogramResult, error) {
	var results []struct {
		BucketTimestamp int64   `gorm:"column:bucket_timestamp"`
		Provider        string  `gorm:"column:provider"`
		Cost            float64 `gorm:"column:cost"`
	}
	q := s.ScopedDB(ctx).Table("mv_logs_hourly")
	q = s.applyMatViewFilters(q, filters)
	if err := q.Select(fmt.Sprintf(`
		CAST(FLOOR(EXTRACT(EPOCH FROM hour) / %d) * %d AS BIGINT) AS bucket_timestamp,
		provider,
		SUM(total_cost) AS cost
	`, bucketSizeSeconds, bucketSizeSeconds)).
		Group("bucket_timestamp, provider").
		Order("bucket_timestamp ASC").
		Find(&results).Error; err != nil {
		return nil, err
	}

	type bucketAgg struct {
		totalCost  float64
		byProvider map[string]float64
	}
	grouped := make(map[int64]*bucketAgg)
	providersSet := make(map[string]struct{})
	for _, r := range results {
		a, ok := grouped[r.BucketTimestamp]
		if !ok {
			a = &bucketAgg{byProvider: make(map[string]float64)}
			grouped[r.BucketTimestamp] = a
		}
		a.totalCost += r.Cost
		a.byProvider[r.Provider] += r.Cost
		providersSet[r.Provider] = struct{}{}
	}

	allTimestamps := generateBucketTimestamps(filters.StartTime, filters.EndTime, bucketSizeSeconds)
	buckets := make([]ProviderCostHistogramBucket, 0, len(allTimestamps))
	for _, ts := range allTimestamps {
		b := ProviderCostHistogramBucket{Timestamp: time.Unix(ts, 0).UTC(), ByProvider: make(map[string]float64)}
		if a, ok := grouped[ts]; ok {
			b.TotalCost = a.totalCost
			b.ByProvider = a.byProvider
		}
		buckets = append(buckets, b)
	}

	providers := sortedStringKeys(providersSet)
	return &ProviderCostHistogramResult{Buckets: buckets, BucketSizeSeconds: bucketSizeSeconds, Providers: providers}, nil
}

// getProviderTokenHistogramFromMatView returns time-bucketed token usage with
// per-provider breakdown from mv_logs_hourly.
func (s *RDBLogStore) getProviderTokenHistogramFromMatView(ctx context.Context, filters SearchFilters, bucketSizeSeconds int64) (*ProviderTokenHistogramResult, error) {
	var results []struct {
		BucketTimestamp  int64  `gorm:"column:bucket_timestamp"`
		Provider         string `gorm:"column:provider"`
		PromptTokens     int64  `gorm:"column:prompt_tokens"`
		CompletionTokens int64  `gorm:"column:completion_tokens"`
		TotalTokens      int64  `gorm:"column:total_tkns"`
	}
	q := s.ScopedDB(ctx).Table("mv_logs_hourly")
	q = s.applyMatViewFilters(q, filters)
	if err := q.Select(fmt.Sprintf(`
		CAST(FLOOR(EXTRACT(EPOCH FROM hour) / %d) * %d AS BIGINT) AS bucket_timestamp,
		provider,
		SUM(total_prompt_tokens) AS prompt_tokens,
		SUM(total_completion_tokens) AS completion_tokens,
		SUM(total_tokens) AS total_tkns,
		SUM(total_cached_read_tokens) AS cached_read_tokens
	`, bucketSizeSeconds, bucketSizeSeconds)).
		Group("bucket_timestamp, provider").
		Order("bucket_timestamp ASC").
		Find(&results).Error; err != nil {
		return nil, err
	}

	type provAgg struct {
		prompt, completion, total int64
	}
	type bucketAgg struct {
		byProvider map[string]*provAgg
	}
	grouped := make(map[int64]*bucketAgg)
	providersSet := make(map[string]struct{})
	for _, r := range results {
		a, ok := grouped[r.BucketTimestamp]
		if !ok {
			a = &bucketAgg{byProvider: make(map[string]*provAgg)}
			grouped[r.BucketTimestamp] = a
		}
		pa, ok := a.byProvider[r.Provider]
		if !ok {
			pa = &provAgg{}
			a.byProvider[r.Provider] = pa
		}
		pa.prompt += r.PromptTokens
		pa.completion += r.CompletionTokens
		pa.total += r.TotalTokens
		providersSet[r.Provider] = struct{}{}
	}

	allTimestamps := generateBucketTimestamps(filters.StartTime, filters.EndTime, bucketSizeSeconds)
	buckets := make([]ProviderTokenHistogramBucket, 0, len(allTimestamps))
	for _, ts := range allTimestamps {
		b := ProviderTokenHistogramBucket{Timestamp: time.Unix(ts, 0).UTC(), ByProvider: make(map[string]ProviderTokenStats)}
		if a, ok := grouped[ts]; ok {
			for prov, pa := range a.byProvider {
				b.ByProvider[prov] = ProviderTokenStats{
					PromptTokens:     pa.prompt,
					CompletionTokens: pa.completion,
					TotalTokens:      pa.total,
				}
			}
		}
		buckets = append(buckets, b)
	}

	providers := sortedStringKeys(providersSet)
	return &ProviderTokenHistogramResult{Buckets: buckets, BucketSizeSeconds: bucketSizeSeconds, Providers: providers}, nil
}

// getProviderLatencyHistogramFromMatView returns time-bucketed latency
// percentiles with per-provider breakdown from mv_logs_hourly. Percentiles
// are re-aggregated using weighted averages.
func (s *RDBLogStore) getProviderLatencyHistogramFromMatView(ctx context.Context, filters SearchFilters, bucketSizeSeconds int64) (*ProviderLatencyHistogramResult, error) {
	var results []struct {
		BucketTimestamp int64   `gorm:"column:bucket_timestamp"`
		Provider        string  `gorm:"column:provider"`
		AvgLatency      float64 `gorm:"column:avg_lat"`
		P90Latency      float64 `gorm:"column:p90_lat"`
		P95Latency      float64 `gorm:"column:p95_lat"`
		P99Latency      float64 `gorm:"column:p99_lat"`
		TotalRequests   int64   `gorm:"column:total_requests"`
	}
	q := s.ScopedDB(ctx).Table("mv_logs_hourly")
	q = s.applyMatViewFilters(q, filters)
	if err := q.Select(fmt.Sprintf(`
		CAST(FLOOR(EXTRACT(EPOCH FROM hour) / %d) * %d AS BIGINT) AS bucket_timestamp,
		provider,
		CASE WHEN SUM(count) > 0 THEN SUM(avg_latency * count) / SUM(count) ELSE 0 END AS avg_lat,
		CASE WHEN SUM(count) > 0 THEN SUM(p90_latency * count) / SUM(count) ELSE 0 END AS p90_lat,
		CASE WHEN SUM(count) > 0 THEN SUM(p95_latency * count) / SUM(count) ELSE 0 END AS p95_lat,
		CASE WHEN SUM(count) > 0 THEN SUM(p99_latency * count) / SUM(count) ELSE 0 END AS p99_lat,
		SUM(count) AS total_requests
	`, bucketSizeSeconds, bucketSizeSeconds)).
		Group("bucket_timestamp, provider").
		Order("bucket_timestamp ASC").
		Find(&results).Error; err != nil {
		return nil, err
	}

	type bucketAgg struct {
		byProvider map[string]ProviderLatencyStats
	}
	grouped := make(map[int64]*bucketAgg)
	providersSet := make(map[string]struct{})
	for _, r := range results {
		a, ok := grouped[r.BucketTimestamp]
		if !ok {
			a = &bucketAgg{byProvider: make(map[string]ProviderLatencyStats)}
			grouped[r.BucketTimestamp] = a
		}
		a.byProvider[r.Provider] = ProviderLatencyStats{
			AvgLatency:    r.AvgLatency,
			P90Latency:    r.P90Latency,
			P95Latency:    r.P95Latency,
			P99Latency:    r.P99Latency,
			TotalRequests: r.TotalRequests,
		}
		providersSet[r.Provider] = struct{}{}
	}

	allTimestamps := generateBucketTimestamps(filters.StartTime, filters.EndTime, bucketSizeSeconds)
	buckets := make([]ProviderLatencyHistogramBucket, 0, len(allTimestamps))
	for _, ts := range allTimestamps {
		b := ProviderLatencyHistogramBucket{Timestamp: time.Unix(ts, 0).UTC(), ByProvider: make(map[string]ProviderLatencyStats)}
		if a, ok := grouped[ts]; ok {
			b.ByProvider = a.byProvider
		}
		buckets = append(buckets, b)
	}

	providers := sortedStringKeys(providersSet)
	return &ProviderLatencyHistogramResult{Buckets: buckets, BucketSizeSeconds: bucketSizeSeconds, Providers: providers}, nil
}

// ---------------------------------------------------------------------------
// Generic dimension histogram queries (cost, tokens, latency grouped by any dimension)
// ---------------------------------------------------------------------------

// getDimensionCostHistogramFromMatView returns time-bucketed cost data grouped by
// the specified dimension column from mv_logs_hourly.
func (s *RDBLogStore) getDimensionCostHistogramFromMatView(ctx context.Context, filters SearchFilters, bucketSizeSeconds int64, dimension HistogramDimension) (*DimensionCostHistogramResult, error) {
	dimCol, ok := histogramDimensionColumn(dimension)
	if !ok {
		return nil, fmt.Errorf("invalid histogram dimension: %s", dimension)
	}
	var results []struct {
		BucketTimestamp int64   `gorm:"column:bucket_timestamp"`
		DimValue        string  `gorm:"column:dim_value"`
		Cost            float64 `gorm:"column:cost"`
	}
	q := s.ScopedDB(ctx).Table("mv_logs_hourly")
	q = s.applyMatViewFilters(q, filters)
	if err := q.Select(fmt.Sprintf(`
		CAST(FLOOR(EXTRACT(EPOCH FROM hour) / %d) * %d AS BIGINT) AS bucket_timestamp,
		%s AS dim_value,
		SUM(total_cost) AS cost
	`, bucketSizeSeconds, bucketSizeSeconds, dimCol)).
		Group(fmt.Sprintf("bucket_timestamp, %s", dimCol)).
		Order("bucket_timestamp ASC").
		Find(&results).Error; err != nil {
		return nil, err
	}

	type bucketAgg struct {
		totalCost   float64
		byDimension map[string]float64
	}
	grouped := make(map[int64]*bucketAgg)
	dimSet := make(map[string]struct{})
	for _, r := range results {
		a, ok := grouped[r.BucketTimestamp]
		if !ok {
			a = &bucketAgg{byDimension: make(map[string]float64)}
			grouped[r.BucketTimestamp] = a
		}
		a.totalCost += r.Cost
		a.byDimension[r.DimValue] += r.Cost
		dimSet[r.DimValue] = struct{}{}
	}

	allTimestamps := generateBucketTimestamps(filters.StartTime, filters.EndTime, bucketSizeSeconds)
	buckets := make([]DimensionCostHistogramBucket, 0, len(allTimestamps))
	for _, ts := range allTimestamps {
		b := DimensionCostHistogramBucket{Timestamp: time.Unix(ts, 0).UTC(), ByDimension: make(map[string]float64)}
		if a, ok := grouped[ts]; ok {
			b.TotalCost = a.totalCost
			b.ByDimension = a.byDimension
		}
		buckets = append(buckets, b)
	}

	dimValues := sortedStringKeys(dimSet)
	return &DimensionCostHistogramResult{Buckets: buckets, BucketSizeSeconds: bucketSizeSeconds, Dimension: dimension, DimensionValues: dimValues}, nil
}

// getDimensionTokenHistogramFromMatView returns time-bucketed token usage grouped by
// the specified dimension column from mv_logs_hourly.
func (s *RDBLogStore) getDimensionTokenHistogramFromMatView(ctx context.Context, filters SearchFilters, bucketSizeSeconds int64, dimension HistogramDimension) (*DimensionTokenHistogramResult, error) {
	dimCol, ok := histogramDimensionColumn(dimension)
	if !ok {
		return nil, fmt.Errorf("invalid histogram dimension: %s", dimension)
	}
	var results []struct {
		BucketTimestamp  int64  `gorm:"column:bucket_timestamp"`
		DimValue         string `gorm:"column:dim_value"`
		PromptTokens     int64  `gorm:"column:prompt_tokens"`
		CompletionTokens int64  `gorm:"column:completion_tokens"`
		TotalTokens      int64  `gorm:"column:total_tkns"`
	}
	q := s.ScopedDB(ctx).Table("mv_logs_hourly")
	q = s.applyMatViewFilters(q, filters)
	if err := q.Select(fmt.Sprintf(`
		CAST(FLOOR(EXTRACT(EPOCH FROM hour) / %d) * %d AS BIGINT) AS bucket_timestamp,
		%s AS dim_value,
		SUM(total_prompt_tokens) AS prompt_tokens,
		SUM(total_completion_tokens) AS completion_tokens,
		SUM(total_tokens) AS total_tkns
	`, bucketSizeSeconds, bucketSizeSeconds, dimCol)).
		Group(fmt.Sprintf("bucket_timestamp, %s", dimCol)).
		Order("bucket_timestamp ASC").
		Find(&results).Error; err != nil {
		return nil, err
	}

	type dimAgg struct {
		prompt, completion, total int64
	}
	type bucketAgg struct {
		byDimension map[string]*dimAgg
	}
	grouped := make(map[int64]*bucketAgg)
	dimSet := make(map[string]struct{})
	for _, r := range results {
		a, ok := grouped[r.BucketTimestamp]
		if !ok {
			a = &bucketAgg{byDimension: make(map[string]*dimAgg)}
			grouped[r.BucketTimestamp] = a
		}
		da, ok := a.byDimension[r.DimValue]
		if !ok {
			da = &dimAgg{}
			a.byDimension[r.DimValue] = da
		}
		da.prompt += r.PromptTokens
		da.completion += r.CompletionTokens
		da.total += r.TotalTokens
		dimSet[r.DimValue] = struct{}{}
	}

	allTimestamps := generateBucketTimestamps(filters.StartTime, filters.EndTime, bucketSizeSeconds)
	buckets := make([]DimensionTokenHistogramBucket, 0, len(allTimestamps))
	for _, ts := range allTimestamps {
		b := DimensionTokenHistogramBucket{Timestamp: time.Unix(ts, 0).UTC(), ByDimension: make(map[string]DimensionTokenStats)}
		if a, ok := grouped[ts]; ok {
			for dim, da := range a.byDimension {
				b.ByDimension[dim] = DimensionTokenStats{
					PromptTokens:     da.prompt,
					CompletionTokens: da.completion,
					TotalTokens:      da.total,
				}
			}
		}
		buckets = append(buckets, b)
	}

	dimValues := sortedStringKeys(dimSet)
	return &DimensionTokenHistogramResult{Buckets: buckets, BucketSizeSeconds: bucketSizeSeconds, Dimension: dimension, DimensionValues: dimValues}, nil
}

// getDimensionLatencyHistogramFromMatView returns time-bucketed latency percentiles
// grouped by the specified dimension column from mv_logs_hourly.
func (s *RDBLogStore) getDimensionLatencyHistogramFromMatView(ctx context.Context, filters SearchFilters, bucketSizeSeconds int64, dimension HistogramDimension) (*DimensionLatencyHistogramResult, error) {
	dimCol, ok := histogramDimensionColumn(dimension)
	if !ok {
		return nil, fmt.Errorf("invalid histogram dimension: %s", dimension)
	}
	var results []struct {
		BucketTimestamp int64   `gorm:"column:bucket_timestamp"`
		DimValue        string  `gorm:"column:dim_value"`
		AvgLatency      float64 `gorm:"column:avg_lat"`
		P90Latency      float64 `gorm:"column:p90_lat"`
		P95Latency      float64 `gorm:"column:p95_lat"`
		P99Latency      float64 `gorm:"column:p99_lat"`
		TotalRequests   int64   `gorm:"column:total_requests"`
	}
	q := s.ScopedDB(ctx).Table("mv_logs_hourly")
	q = s.applyMatViewFilters(q, filters)
	if err := q.Select(fmt.Sprintf(`
		CAST(FLOOR(EXTRACT(EPOCH FROM hour) / %d) * %d AS BIGINT) AS bucket_timestamp,
		%s AS dim_value,
		CASE WHEN SUM(count) > 0 THEN SUM(avg_latency * count) / SUM(count) ELSE 0 END AS avg_lat,
		CASE WHEN SUM(count) > 0 THEN SUM(p90_latency * count) / SUM(count) ELSE 0 END AS p90_lat,
		CASE WHEN SUM(count) > 0 THEN SUM(p95_latency * count) / SUM(count) ELSE 0 END AS p95_lat,
		CASE WHEN SUM(count) > 0 THEN SUM(p99_latency * count) / SUM(count) ELSE 0 END AS p99_lat,
		SUM(count) AS total_requests
	`, bucketSizeSeconds, bucketSizeSeconds, dimCol)).
		Group(fmt.Sprintf("bucket_timestamp, %s", dimCol)).
		Order("bucket_timestamp ASC").
		Find(&results).Error; err != nil {
		return nil, err
	}

	type bucketAgg struct {
		byDimension map[string]DimensionLatencyStats
	}
	grouped := make(map[int64]*bucketAgg)
	dimSet := make(map[string]struct{})
	for _, r := range results {
		a, ok := grouped[r.BucketTimestamp]
		if !ok {
			a = &bucketAgg{byDimension: make(map[string]DimensionLatencyStats)}
			grouped[r.BucketTimestamp] = a
		}
		a.byDimension[r.DimValue] = DimensionLatencyStats{
			AvgLatency:    r.AvgLatency,
			P90Latency:    r.P90Latency,
			P95Latency:    r.P95Latency,
			P99Latency:    r.P99Latency,
			TotalRequests: r.TotalRequests,
		}
		dimSet[r.DimValue] = struct{}{}
	}

	allTimestamps := generateBucketTimestamps(filters.StartTime, filters.EndTime, bucketSizeSeconds)
	buckets := make([]DimensionLatencyHistogramBucket, 0, len(allTimestamps))
	for _, ts := range allTimestamps {
		b := DimensionLatencyHistogramBucket{Timestamp: time.Unix(ts, 0).UTC(), ByDimension: make(map[string]DimensionLatencyStats)}
		if a, ok := grouped[ts]; ok {
			b.ByDimension = a.byDimension
		}
		buckets = append(buckets, b)
	}

	dimValues := sortedStringKeys(dimSet)
	return &DimensionLatencyHistogramResult{Buckets: buckets, BucketSizeSeconds: bucketSizeSeconds, Dimension: dimension, DimensionValues: dimValues}, nil
}

// getModelRankingsFromMatView returns models ranked by usage with trend
// comparison to the previous period of equal duration from mv_logs_hourly.
func (s *RDBLogStore) getModelRankingsFromMatView(ctx context.Context, filters SearchFilters) (*ModelRankingResult, error) {
	var results []struct {
		Model              string         `gorm:"column:model"`
		CanonicalName      sql.NullString `gorm:"column:canonical_name"`
		Provider           string         `gorm:"column:provider"`
		Total              int64          `gorm:"column:total"`
		SuccessCount       int64          `gorm:"column:success_count"`
		AvgLatency         float64        `gorm:"column:avg_lat"`
		TotalTokens        int64          `gorm:"column:total_tkns"`
		TotalCost          float64        `gorm:"column:total_cost"`
		TPCompletionTokens int64          `gorm:"column:tp_completion_tokens"`
		TPLatencyMs        float64        `gorm:"column:tp_latency_ms"`
	}
	q := s.ScopedDB(ctx).Table("mv_logs_hourly")
	q = s.applyMatViewFilters(q, filters)
	q = q.Where("model IS NOT NULL AND model != ''")
	if err := q.Select(`
		model, provider,
		MAX(NULLIF(canonical_model_name, '')) AS canonical_name,
		SUM(count) AS total,
		SUM(success_count) AS success_count,
		CASE WHEN SUM(count) > 0 THEN SUM(avg_latency * count) / SUM(count) ELSE 0 END AS avg_lat,
		SUM(total_tokens) AS total_tkns,
		SUM(total_cost) AS total_cost,
		COALESCE(SUM(CASE WHEN status = 'success' THEN throughput_completion_tokens ELSE 0 END), 0) AS tp_completion_tokens,
		COALESCE(SUM(CASE WHEN status = 'success' THEN throughput_latency_ms ELSE 0 END), 0) AS tp_latency_ms
	`).Group("model, provider").
		Order("total DESC").
		Find(&results).Error; err != nil {
		return nil, err
	}

	// Previous period for trend (same duration, ending just before current start)
	type prevRow struct {
		Model              string  `gorm:"column:model"`
		Provider           string  `gorm:"column:provider"`
		Total              int64   `gorm:"column:total"`
		AvgLatency         float64 `gorm:"column:avg_lat"`
		TotalTokens        int64   `gorm:"column:total_tkns"`
		TotalCost          float64 `gorm:"column:total_cost"`
		TPCompletionTokens int64   `gorm:"column:tp_completion_tokens"`
		TPLatencyMs        float64 `gorm:"column:tp_latency_ms"`
	}
	var prevResults []prevRow
	if filters.StartTime != nil && filters.EndTime != nil {
		// Anchor the previous period to the hour grid: the current period's
		// hour >= date_trunc('hour', StartTime) predicate claims the bucket
		// containing StartTime, so its effective span is [hourStart, EndTime].
		// Derive the comparison duration from hourStart (not StartTime) so the
		// previous window covers exactly the same effective interval; otherwise
		// a sub-hour StartTime makes the current window longer by
		// StartTime-hourStart and skews the trend. The previous period must
		// also end strictly before that bucket: ending at StartTime-1ns would
		// match the same bucket via hour <= prevEnd and double-count the
		// boundary hour in both periods.
		hourStart := filters.StartTime.Truncate(time.Hour)
		duration := filters.EndTime.Sub(hourStart)
		prevStart := hourStart.Add(-duration)
		prevEnd := hourStart.Add(-time.Nanosecond)
		prevFilters := filters
		prevFilters.StartTime = &prevStart
		prevFilters.EndTime = &prevEnd
		pq := s.ScopedDB(ctx).Table("mv_logs_hourly")
		pq = s.applyMatViewFilters(pq, prevFilters)
		pq = pq.Where("model IS NOT NULL AND model != ''")
		if err := pq.Select(`
			model, provider,
			SUM(count) AS total,
			CASE WHEN SUM(count) > 0 THEN SUM(avg_latency * count) / SUM(count) ELSE 0 END AS avg_lat,
			SUM(total_tokens) AS total_tkns,
			SUM(total_cost) AS total_cost,
			COALESCE(SUM(CASE WHEN status = 'success' THEN throughput_completion_tokens ELSE 0 END), 0) AS tp_completion_tokens,
			COALESCE(SUM(CASE WHEN status = 'success' THEN throughput_latency_ms ELSE 0 END), 0) AS tp_latency_ms
		`).Group("model, provider").Find(&prevResults).Error; err != nil {
			return nil, fmt.Errorf("failed to get previous period rankings: %w", err)
		}
	}
	// Key by model+provider to match current period granularity
	type rankingKey struct{ model, provider string }
	prevMap := make(map[rankingKey]int, len(prevResults))
	for i, r := range prevResults {
		prevMap[rankingKey{r.Model, r.Provider}] = i
	}

	rankings := make([]ModelRankingWithTrend, 0, len(results))
	for _, r := range results {
		var successRate float64
		if r.Total > 0 {
			successRate = float64(r.SuccessCount) / float64(r.Total) * 100
		}
		entry := ModelRankingEntry{
			Model:         r.Model,
			Provider:      r.Provider,
			TotalRequests: r.Total,
			SuccessCount:  r.SuccessCount,
			SuccessRate:   successRate,
			TotalTokens:   r.TotalTokens,
			TotalCost:     r.TotalCost,
			AvgLatency:    r.AvgLatency,
			Throughput:    tokensPerSecond(r.TPCompletionTokens, r.TPLatencyMs),
		}
		if r.CanonicalName.Valid {
			entry.CanonicalModelName = &r.CanonicalName.String
		}
		mrt := ModelRankingWithTrend{ModelRankingEntry: entry}
		if idx, ok := prevMap[rankingKey{r.Model, r.Provider}]; ok {
			prev := prevResults[idx]
			mrt.Trend = ModelRankingTrend{
				HasPreviousPeriod: true,
				RequestsTrend:     trendPct(float64(r.Total), float64(prev.Total)),
				TokensTrend:       trendPct(float64(r.TotalTokens), float64(prev.TotalTokens)),
				CostTrend:         trendPct(r.TotalCost, prev.TotalCost),
				LatencyTrend:      trendPct(r.AvgLatency, prev.AvgLatency),
				ThroughputTrend:   trendPct(entry.Throughput, tokensPerSecond(prev.TPCompletionTokens, prev.TPLatencyMs)),
			}
		}
		rankings = append(rankings, mrt)
	}
	return &ModelRankingResult{Rankings: rankings}, nil
}

// getUserRankingsFromMatView returns users ranked by usage with trend
// comparison to the previous period of equal duration from mv_logs_hourly.
func (s *RDBLogStore) getUserRankingsFromMatView(ctx context.Context, filters SearchFilters) (*UserRankingResult, error) {
	var results []struct {
		UserID      string  `gorm:"column:user_id"`
		Total       int64   `gorm:"column:total"`
		TotalTokens int64   `gorm:"column:total_tkns"`
		TotalCost   float64 `gorm:"column:total_cost"`
	}
	q := s.ScopedDB(ctx).Table("mv_logs_hourly")
	q = s.applyMatViewFilters(q, filters)
	q = q.Where("user_id != ''")
	if err := q.Select(`
		user_id,
		SUM(count) AS total,
		SUM(total_tokens) AS total_tkns,
		SUM(total_cost) AS total_cost
	`).Group("user_id").
		Order("total DESC").
		Find(&results).Error; err != nil {
		return nil, err
	}

	// Previous period for trend (same duration, ending just before current start)
	type prevRow struct {
		UserID      string  `gorm:"column:user_id"`
		Total       int64   `gorm:"column:total"`
		TotalTokens int64   `gorm:"column:total_tkns"`
		TotalCost   float64 `gorm:"column:total_cost"`
	}
	var prevResults []prevRow
	if filters.StartTime != nil && filters.EndTime != nil {
		// Anchor the previous period to the hour grid: the current period's
		// hour >= date_trunc('hour', StartTime) predicate claims the bucket
		// containing StartTime, so its effective span is [hourStart, EndTime].
		// Derive the comparison duration from hourStart (not StartTime) so the
		// previous window covers exactly the same effective interval; otherwise
		// a sub-hour StartTime makes the current window longer by
		// StartTime-hourStart and skews the trend. The previous period must
		// also end strictly before that bucket: ending at StartTime-1ns would
		// match the same bucket via hour <= prevEnd and double-count the
		// boundary hour in both periods.
		hourStart := filters.StartTime.Truncate(time.Hour)
		duration := filters.EndTime.Sub(hourStart)
		prevStart := hourStart.Add(-duration)
		prevEnd := hourStart.Add(-time.Nanosecond)
		prevFilters := filters
		prevFilters.StartTime = &prevStart
		prevFilters.EndTime = &prevEnd
		pq := s.ScopedDB(ctx).Table("mv_logs_hourly")
		pq = s.applyMatViewFilters(pq, prevFilters)
		pq = pq.Where("user_id != ''")
		if err := pq.Select(`
			user_id,
			SUM(count) AS total,
			SUM(total_tokens) AS total_tkns,
			SUM(total_cost) AS total_cost
		`).Group("user_id").Find(&prevResults).Error; err != nil {
			return nil, fmt.Errorf("failed to get previous period user rankings: %w", err)
		}
	}

	prevMap := make(map[string]int, len(prevResults))
	for i, r := range prevResults {
		prevMap[r.UserID] = i
	}

	rankings := make([]UserRankingWithTrend, 0, len(results))
	for _, r := range results {
		entry := UserRankingEntry{
			UserID:        r.UserID,
			TotalRequests: r.Total,
			TotalTokens:   r.TotalTokens,
			TotalCost:     r.TotalCost,
		}
		urt := UserRankingWithTrend{UserRankingEntry: entry}
		if idx, ok := prevMap[r.UserID]; ok {
			prev := prevResults[idx]
			urt.Trend = UserRankingTrend{
				HasPreviousPeriod: true,
				RequestsTrend:     trendPct(float64(r.Total), float64(prev.Total)),
				TokensTrend:       trendPct(float64(r.TotalTokens), float64(prev.TotalTokens)),
				CostTrend:         trendPct(r.TotalCost, prev.TotalCost),
			}
		}
		rankings = append(rankings, urt)
	}
	return &UserRankingResult{Rankings: rankings}, nil
}

// getDimensionRankingsFromMatView returns entities ranked by usage from mv_logs_hourly.
func (s *RDBLogStore) getDimensionRankingsFromMatView(ctx context.Context, filters SearchFilters, dimension RankingDimension) (*DimensionRankingResult, error) {
	idCol, nameCol, ok := DimensionColumnDef(dimension)
	if !ok {
		return nil, fmt.Errorf("invalid ranking dimension: %s", dimension)
	}

	type row struct {
		ID        string  `gorm:"column:id"`
		Total     int64   `gorm:"column:total"`
		TotalTkns int64   `gorm:"column:total_tkns"`
		TotalCost float64 `gorm:"column:total_cost"`
	}

	var results []row
	q := s.ScopedDB(ctx).Table("mv_logs_hourly")
	q = s.applyMatViewFilters(q, filters)
	q = q.Where(fmt.Sprintf("%s != ''", idCol))
	if err := q.Select(fmt.Sprintf(`
		%s AS id,
		SUM(count) AS total,
		SUM(total_tokens) AS total_tkns,
		SUM(total_cost) AS total_cost
	`, idCol)).Group(idCol).
		Order("total DESC").
		Find(&results).Error; err != nil {
		return nil, err
	}

	// Resolve names from the logs table
	nameMap := make(map[string]string)
	if nameCol != "" && len(results) > 0 {
		ids := make([]string, len(results))
		for i, r := range results {
			ids[i] = r.ID
		}
		var nameRows []struct {
			ID   string `gorm:"column:id"`
			Name string `gorm:"column:name"`
		}
		if err := s.ScopedDB(ctx).Model(&Log{}).
			Select(fmt.Sprintf("DISTINCT ON (%s) %s AS id, %s AS name", idCol, idCol, nameCol)).
			Where(fmt.Sprintf("%s IN ?", idCol), ids).
			Where(fmt.Sprintf("%s IS NOT NULL AND %s != ''", nameCol, nameCol)).
			Order(fmt.Sprintf("%s, timestamp DESC", idCol)).
			Find(&nameRows).Error; err == nil {
			for _, nr := range nameRows {
				nameMap[nr.ID] = nr.Name
			}
		}
	}

	// Previous period
	var prevResults []row
	if filters.StartTime != nil && filters.EndTime != nil {
		// Anchor the previous period to the hour grid: the current period's
		// hour >= date_trunc('hour', StartTime) predicate claims the bucket
		// containing StartTime, so its effective span is [hourStart, EndTime].
		// Derive the comparison duration from hourStart (not StartTime) so the
		// previous window covers exactly the same effective interval; otherwise
		// a sub-hour StartTime makes the current window longer by
		// StartTime-hourStart and skews the trend. The previous period must
		// also end strictly before that bucket: ending at StartTime-1ns would
		// match the same bucket via hour <= prevEnd and double-count the
		// boundary hour in both periods.
		hourStart := filters.StartTime.Truncate(time.Hour)
		duration := filters.EndTime.Sub(hourStart)
		prevStart := hourStart.Add(-duration)
		prevEnd := hourStart.Add(-time.Nanosecond)
		prevFilters := filters
		prevFilters.StartTime = &prevStart
		prevFilters.EndTime = &prevEnd
		pq := s.ScopedDB(ctx).Table("mv_logs_hourly")
		pq = s.applyMatViewFilters(pq, prevFilters)
		pq = pq.Where(fmt.Sprintf("%s != ''", idCol))
		if err := pq.Select(fmt.Sprintf(`
			%s AS id,
			SUM(count) AS total,
			SUM(total_tokens) AS total_tkns,
			SUM(total_cost) AS total_cost
		`, idCol)).Group(idCol).Find(&prevResults).Error; err != nil {
			return nil, fmt.Errorf("failed to get previous period dimension rankings: %w", err)
		}
	}

	prevMap := make(map[string]int, len(prevResults))
	for i, r := range prevResults {
		prevMap[r.ID] = i
	}

	rankings := make([]DimensionRankingWithTrend, 0, len(results))
	for _, r := range results {
		entry := DimensionRankingEntry{
			ID:            r.ID,
			Name:          nameMap[r.ID],
			TotalRequests: r.Total,
			TotalTokens:   r.TotalTkns,
			TotalCost:     r.TotalCost,
		}
		drt := DimensionRankingWithTrend{DimensionRankingEntry: entry}
		if idx, exists := prevMap[r.ID]; exists {
			prev := prevResults[idx]
			drt.Trend = DimensionRankingTrend{
				HasPreviousPeriod: true,
				RequestsTrend:     trendPct(float64(r.Total), float64(prev.Total)),
				TokensTrend:       trendPct(float64(r.TotalTkns), float64(prev.TotalTkns)),
				CostTrend:         trendPct(r.TotalCost, prev.TotalCost),
			}
		}
		rankings = append(rankings, drt)
	}
	return &DimensionRankingResult{Rankings: rankings, Dimension: dimension}, nil
}

// ---------------------------------------------------------------------------
// Filterdata from mat view
// ---------------------------------------------------------------------------

// getDistinctModelsFromMatView returns unique model names from mv_filter_models.
// Limit matches the raw-table fallback so callers see the same row cap regardless
// of which path served the request.
func (s *RDBLogStore) getDistinctModelsFromMatView(ctx context.Context, limit int, query string) ([]string, error) {
	var models []string
	q := s.ScopedDB(ctx).Table("mv_filter_models").
		Distinct("model").
		Where("model != ''")
	if query != "" {
		q = q.Where("model ILIKE ?", "%"+query+"%")
	}
	if err := q.Order("model ASC").Limit(limit).Pluck("model", &models).Error; err != nil {
		return nil, err
	}
	return models, nil
}

// getDistinctAliasesFromMatView returns unique alias values from mv_filter_aliases.
func (s *RDBLogStore) getDistinctAliasesFromMatView(ctx context.Context, limit int, query string) ([]string, error) {
	var aliases []string
	q := s.ScopedDB(ctx).Table("mv_filter_aliases").
		Distinct("alias").
		Where("alias != ''")
	if query != "" {
		q = q.Where("alias ILIKE ?", "%"+query+"%")
	}
	if err := q.Order("alias ASC").Limit(limit).Pluck("alias", &aliases).Error; err != nil {
		return nil, err
	}
	return aliases, nil
}

// getDistinctStopReasonsFromMatView returns unique stop reasons from mv_filter_stop_reasons.
func (s *RDBLogStore) getDistinctStopReasonsFromMatView(ctx context.Context, limit int, query string) ([]string, error) {
	var stopReasons []string
	q := s.ScopedDB(ctx).Table("mv_filter_stop_reasons").
		Distinct("stop_reason").
		Where("stop_reason != ''")
	if query != "" {
		q = q.Where("stop_reason ILIKE ?", "%"+query+"%")
	}
	if err := q.Order("stop_reason ASC").Limit(limit).Pluck("stop_reason", &stopReasons).Error; err != nil {
		return nil, err
	}
	return stopReasons, nil
}

// getDistinctKeyPairsFromMatView returns unique ID-Name pairs for the given
// (idCol, nameCol) by selecting from the per-dimension matview pre-aggregated
// for that pair. Returns (nil, false) if no matview is registered for the pair —
// callers fall back to the raw-table path.
func (s *RDBLogStore) getDistinctKeyPairsFromMatView(ctx context.Context, idCol, nameCol string, limit int, query string) ([]KeyPairResult, bool, error) {
	view, ok := filterMatViewKeyPairColumns[[2]string{idCol, nameCol}]
	if !ok {
		return nil, false, nil
	}
	var results []KeyPairResult
	q := s.ScopedDB(ctx).Table(view).Where("id != ''")
	// User matview stores name = id and the view-level WHERE already filters
	// empty ids; other matviews include name and we additionally guard against
	// stragglers with empty names.
	if !(idCol == "user_id" && nameCol == "user_id") {
		q = q.Where("name != ''")
	}
	if query != "" {
		q = q.Where("name ILIKE ?", "%"+query+"%")
	}
	if err := q.
		Select("DISTINCT id, name").
		Order("name ASC").
		Limit(limit).
		Find(&results).Error; err != nil {
		return nil, true, err
	}
	return results, true, nil
}

// getDistinctRoutingEnginesFromMatView returns unique routing engine names by
// parsing the comma-separated routing_engines_used values from
// mv_filter_routing_engines.
func (s *RDBLogStore) getDistinctRoutingEnginesFromMatView(ctx context.Context, limit int, query string) ([]string, error) {
	var rawValues []string
	q := s.ScopedDB(ctx).Table("mv_filter_routing_engines").
		Distinct("routing_engines_used").
		Where("routing_engines_used != ''")
	if query != "" {
		q = q.Where("routing_engines_used ILIKE ?", "%"+query+"%")
	}
	if err := q.Pluck("routing_engines_used", &rawValues).Error; err != nil {
		return nil, err
	}
	seen := make(map[string]struct{})
	for _, raw := range rawValues {
		for _, eng := range strings.Split(raw, ",") {
			eng = strings.TrimSpace(eng)
			if eng != "" {
				seen[eng] = struct{}{}
			}
		}
	}
	result := sortedStringKeys(seen)
	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// sortedStringKeys returns the keys of a set map in sorted order.
func sortedStringKeys(m map[string]struct{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// trendPct computes the percentage change from previous to current.
// Returns 0 when the previous value is zero (no basis for comparison).
func trendPct(current, previous float64) float64 {
	if previous == 0 {
		return 0
	}
	return ((current - previous) / previous) * 100
}
