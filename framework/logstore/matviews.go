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
    COUNT(*) AS count,
    SUM(CASE WHEN status = 'success' THEN 1 ELSE 0 END) AS success_count,
    SUM(CASE WHEN status = 'error' THEN 1 ELSE 0 END) AS error_count,
    COALESCE(AVG(latency), 0) AS avg_latency,
    COALESCE(percentile_cont(0.90) WITHIN GROUP (ORDER BY latency), 0) AS p90_latency,
    COALESCE(percentile_cont(0.95) WITHIN GROUP (ORDER BY latency), 0) AS p95_latency,
    COALESCE(percentile_cont(0.99) WITHIN GROUP (ORDER BY latency), 0) AS p99_latency,
    COALESCE(SUM(prompt_tokens), 0) AS total_prompt_tokens,
    COALESCE(SUM(completion_tokens), 0) AS total_completion_tokens,
    COALESCE(SUM(total_tokens), 0) AS total_tokens,
    COALESCE(SUM(cached_read_tokens), 0) AS total_cached_read_tokens,
    COALESCE(SUM(cost), 0) AS total_cost
FROM logs
WHERE status IN ('success', 'error')
GROUP BY 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12
`

// mvLogsHourlyUniqueIdx is required for REFRESH MATERIALIZED VIEW CONCURRENTLY.
const mvLogsHourlyUniqueIdx = `
CREATE UNIQUE INDEX IF NOT EXISTS mv_logs_hourly_uniq
ON mv_logs_hourly (hour, provider, model, status, object_type, selected_key_id, virtual_key_id, routing_rule_id, user_id, team_id, customer_id, business_unit_id)
`

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
//   - name:       view name
//   - selectExpr: comma-separated list of result columns (already COALESCEd if needed)
//   - whereExpr:  predicate that filters out empty rows for this dimension
//   - uniqueIdx:  comma-separated columns for the unique index (required for
//     REFRESH ... CONCURRENTLY)
type filterMatViewDef struct {
	name       string
	selectExpr string
	whereExpr  string
	uniqueIdx  string
}

// filterMatViews enumerates every per-dimension materialized view used to
// populate filter dropdowns on the logs page. Order matters only for
// deterministic startup logs.
var filterMatViews = []filterMatViewDef{
	{
		name:       "mv_filter_models",
		selectExpr: "model, provider",
		whereExpr:  "model IS NOT NULL AND model != ''",
		uniqueIdx:  "model, provider",
	},
	{
		name:       "mv_filter_aliases",
		selectExpr: "alias",
		whereExpr:  "alias IS NOT NULL AND alias != ''",
		uniqueIdx:  "alias",
	},
	{
		name:       "mv_filter_stop_reasons",
		selectExpr: "stop_reason",
		whereExpr:  "stop_reason IS NOT NULL AND stop_reason != ''",
		uniqueIdx:  "stop_reason",
	},
	{
		name:       "mv_filter_routing_engines",
		selectExpr: "routing_engines_used",
		whereExpr:  "routing_engines_used IS NOT NULL AND routing_engines_used != ''",
		uniqueIdx:  "routing_engines_used",
	},
	{
		name:       "mv_filter_selected_keys",
		selectExpr: "selected_key_id AS id, selected_key_name AS name",
		whereExpr:  "selected_key_id IS NOT NULL AND selected_key_id != '' AND selected_key_name IS NOT NULL AND selected_key_name != ''",
		uniqueIdx:  "id, name",
	},
	{
		name:       "mv_filter_virtual_keys",
		selectExpr: "virtual_key_id AS id, virtual_key_name AS name",
		whereExpr:  "virtual_key_id IS NOT NULL AND virtual_key_id != '' AND virtual_key_name IS NOT NULL AND virtual_key_name != ''",
		uniqueIdx:  "id, name",
	},
	{
		name:       "mv_filter_routing_rules",
		selectExpr: "routing_rule_id AS id, routing_rule_name AS name",
		whereExpr:  "routing_rule_id IS NOT NULL AND routing_rule_id != '' AND routing_rule_name IS NOT NULL AND routing_rule_name != ''",
		uniqueIdx:  "id, name",
	},
	{
		name:       "mv_filter_teams",
		selectExpr: "team_id AS id, team_name AS name",
		whereExpr:  "team_id IS NOT NULL AND team_id != '' AND team_name IS NOT NULL AND team_name != ''",
		uniqueIdx:  "id, name",
	},
	{
		name:       "mv_filter_customers",
		selectExpr: "customer_id AS id, customer_name AS name",
		whereExpr:  "customer_id IS NOT NULL AND customer_id != '' AND customer_name IS NOT NULL AND customer_name != ''",
		uniqueIdx:  "id, name",
	},
	{
		name:       "mv_filter_users",
		selectExpr: "user_id AS id, user_id AS name",
		whereExpr:  "user_id IS NOT NULL AND user_id != ''",
		uniqueIdx:  "id",
	},
	{
		name:       "mv_filter_business_units",
		selectExpr: "business_unit_id AS id, business_unit_name AS name",
		whereExpr:  "business_unit_id IS NOT NULL AND business_unit_id != '' AND business_unit_name IS NOT NULL AND business_unit_name != ''",
		uniqueIdx:  "id, name",
	},
}

// filterMatViewKeyPairColumns maps the (idCol, nameCol) pair callers pass into
// GetDistinctKeyPairs to the per-dimension matview that pre-aggregates it.
var filterMatViewKeyPairColumns = map[[2]string]string{
	{"selected_key_id", "selected_key_name"}:     "mv_filter_selected_keys",
	{"virtual_key_id", "virtual_key_name"}:       "mv_filter_virtual_keys",
	{"routing_rule_id", "routing_rule_name"}:     "mv_filter_routing_rules",
	{"team_id", "team_name"}:                     "mv_filter_teams",
	{"customer_id", "customer_name"}:             "mv_filter_customers",
	{"user_id", "user_id"}:                       "mv_filter_users",
	{"business_unit_id", "business_unit_name"}:   "mv_filter_business_units",
}

func filterMatViewDDL(v filterMatViewDef) string {
	return fmt.Sprintf(
		"CREATE MATERIALIZED VIEW IF NOT EXISTS %s AS SELECT DISTINCT %s FROM logs WHERE timestamp >= NOW() - INTERVAL '%s' AND (%s)",
		v.name, v.selectExpr, filterDataMatViewWindow, v.whereExpr,
	)
}

func filterMatViewUniqueIdx(v filterMatViewDef) string {
	return fmt.Sprintf("CREATE UNIQUE INDEX IF NOT EXISTS %s_uniq ON %s (%s)", v.name, v.name, v.uniqueIdx)
}

// ---------------------------------------------------------------------------
// View lifecycle
// ---------------------------------------------------------------------------

// ensureMatViews creates materialized views and their unique indexes if they
// don't already exist. Called once on startup.
func ensureMatViews(ctx context.Context, db *gorm.DB) error {
	stmts := []string{mvLogsHourlyDDL, mvLogsHourlyUniqueIdx}
	for _, v := range filterMatViews {
		stmts = append(stmts, filterMatViewDDL(v), filterMatViewUniqueIdx(v))
	}
	for _, ddl := range stmts {
		if err := db.WithContext(ctx).Exec(ddl).Error; err != nil {
			return fmt.Errorf("failed to create materialized view: %w", err)
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
	mu               sync.Mutex
	lastActivity     int64
	lastForcedAt     time.Time
	initialized      bool
}

var refreshGate matViewRefreshGate

// logsActivityCounter returns the cumulative INSERT+UPDATE+DELETE count for
// the `logs` table from pg_stat_user_tables. The stat collector is eventually
// consistent (lags a few seconds under load) which is fine for a 30s tick.
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
// deployments only one instance refreshes at a time — others skip silently.
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
					logger.Info("logstore: materialized views are ready (recovered)")
					readyFlag.Store(true)
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
		len(f.RoutingEngineUsed) == 0 &&
		len(f.StopReasons) == 0 &&
		f.MinLatency == nil && f.MaxLatency == nil &&
		f.MinTokens == nil && f.MaxTokens == nil &&
		f.MinCost == nil && f.MaxCost == nil &&
		!f.MissingCostOnly
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
// table is fast enough and avoids the up-to-30s freshness lag that would
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
// Used by both /api/logs/stats (full metric payload) and /api/logs (pagination
// total count) so those two surfaces stay consistent on the same window.
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
func applyMatViewFilters(q *gorm.DB, f SearchFilters) *gorm.DB {
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
	q := s.db.WithContext(ctx).Table("mv_logs_hourly")
	q = applyMatViewFilters(q, filters)
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
		TotalCount   int64   `gorm:"column:total_count"`
		SuccessCount int64   `gorm:"column:success_count"`
		AvgLatency   float64 `gorm:"column:avg_latency"`
		TotalTokens  int64   `gorm:"column:total_tokens"`
		TotalCost    float64 `gorm:"column:total_cost"`
	}
	q := s.db.WithContext(ctx).Table("mv_logs_hourly")
	q = applyMatViewFilters(q, filters)
	if err := q.Select(`
		COALESCE(SUM(count), 0) AS total_count,
		COALESCE(SUM(success_count), 0) AS success_count,
		CASE WHEN SUM(count) > 0 THEN SUM(avg_latency * count) / SUM(count) ELSE 0 END AS avg_latency,
		COALESCE(SUM(total_tokens), 0) AS total_tokens,
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
	cacheBase := s.db.WithContext(ctx).Model(&Log{}).Where("status IN ?", []string{"success", "error"})
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
// success, error) by re-aggregating hourly buckets from mv_logs_hourly.
func (s *RDBLogStore) getHistogramFromMatView(ctx context.Context, filters SearchFilters, bucketSizeSeconds int64) (*HistogramResult, error) {
	var results []struct {
		BucketTimestamp int64 `gorm:"column:bucket_timestamp"`
		Total           int64 `gorm:"column:total"`
		Success         int64 `gorm:"column:success"`
		ErrorCount      int64 `gorm:"column:error_count"`
	}
	q := s.db.WithContext(ctx).Table("mv_logs_hourly")
	q = applyMatViewFilters(q, filters)
	if err := q.Select(fmt.Sprintf(`
		CAST(FLOOR(EXTRACT(EPOCH FROM hour) / %d) * %d AS BIGINT) AS bucket_timestamp,
		SUM(count) AS total,
		SUM(success_count) AS success,
		SUM(error_count) AS error_count
	`, bucketSizeSeconds, bucketSizeSeconds)).
		Group("bucket_timestamp").
		Order("bucket_timestamp ASC").
		Find(&results).Error; err != nil {
		return nil, err
	}

	resultMap := make(map[int64]*struct{ total, success, errCount int64 }, len(results))
	for _, r := range results {
		resultMap[r.BucketTimestamp] = &struct{ total, success, errCount int64 }{r.Total, r.Success, r.ErrorCount}
	}

	allTimestamps := generateBucketTimestamps(filters.StartTime, filters.EndTime, bucketSizeSeconds)
	buckets := make([]HistogramBucket, 0, len(allTimestamps))
	for _, ts := range allTimestamps {
		b := HistogramBucket{Timestamp: time.Unix(ts, 0).UTC()}
		if a, ok := resultMap[ts]; ok {
			b.Count = a.total
			b.Success = a.success
			b.Error = a.errCount
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
	q := s.db.WithContext(ctx).Table("mv_logs_hourly")
	q = applyMatViewFilters(q, filters)
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
	q := s.db.WithContext(ctx).Table("mv_logs_hourly")
	q = applyMatViewFilters(q, filters)
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
// success/error breakdown per model from mv_logs_hourly.
func (s *RDBLogStore) getModelHistogramFromMatView(ctx context.Context, filters SearchFilters, bucketSizeSeconds int64) (*ModelHistogramResult, error) {
	var results []struct {
		BucketTimestamp int64  `gorm:"column:bucket_timestamp"`
		Model           string `gorm:"column:model"`
		Total           int64  `gorm:"column:total"`
		Success         int64  `gorm:"column:success"`
		ErrorCount      int64  `gorm:"column:error_count"`
	}
	q := s.db.WithContext(ctx).Table("mv_logs_hourly")
	q = applyMatViewFilters(q, filters)
	if err := q.Select(fmt.Sprintf(`
		CAST(FLOOR(EXTRACT(EPOCH FROM hour) / %d) * %d AS BIGINT) AS bucket_timestamp,
		model,
		SUM(count) AS total,
		SUM(success_count) AS success,
		SUM(error_count) AS error_count
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
	q := s.db.WithContext(ctx).Table("mv_logs_hourly")
	q = applyMatViewFilters(q, filters)
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

// getProviderCostHistogramFromMatView returns time-bucketed cost data with
// per-provider breakdown from mv_logs_hourly.
func (s *RDBLogStore) getProviderCostHistogramFromMatView(ctx context.Context, filters SearchFilters, bucketSizeSeconds int64) (*ProviderCostHistogramResult, error) {
	var results []struct {
		BucketTimestamp int64   `gorm:"column:bucket_timestamp"`
		Provider        string  `gorm:"column:provider"`
		Cost            float64 `gorm:"column:cost"`
	}
	q := s.db.WithContext(ctx).Table("mv_logs_hourly")
	q = applyMatViewFilters(q, filters)
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
	q := s.db.WithContext(ctx).Table("mv_logs_hourly")
	q = applyMatViewFilters(q, filters)
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
	q := s.db.WithContext(ctx).Table("mv_logs_hourly")
	q = applyMatViewFilters(q, filters)
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
	dimCol := string(dimension)
	var results []struct {
		BucketTimestamp int64   `gorm:"column:bucket_timestamp"`
		DimValue        string  `gorm:"column:dim_value"`
		Cost            float64 `gorm:"column:cost"`
	}
	q := s.db.WithContext(ctx).Table("mv_logs_hourly")
	q = applyMatViewFilters(q, filters)
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
	dimCol := string(dimension)
	var results []struct {
		BucketTimestamp  int64  `gorm:"column:bucket_timestamp"`
		DimValue         string `gorm:"column:dim_value"`
		PromptTokens     int64  `gorm:"column:prompt_tokens"`
		CompletionTokens int64  `gorm:"column:completion_tokens"`
		TotalTokens      int64  `gorm:"column:total_tkns"`
	}
	q := s.db.WithContext(ctx).Table("mv_logs_hourly")
	q = applyMatViewFilters(q, filters)
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
	dimCol := string(dimension)
	var results []struct {
		BucketTimestamp int64   `gorm:"column:bucket_timestamp"`
		DimValue        string  `gorm:"column:dim_value"`
		AvgLatency      float64 `gorm:"column:avg_lat"`
		P90Latency      float64 `gorm:"column:p90_lat"`
		P95Latency      float64 `gorm:"column:p95_lat"`
		P99Latency      float64 `gorm:"column:p99_lat"`
		TotalRequests   int64   `gorm:"column:total_requests"`
	}
	q := s.db.WithContext(ctx).Table("mv_logs_hourly")
	q = applyMatViewFilters(q, filters)
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
		Model        string  `gorm:"column:model"`
		Provider     string  `gorm:"column:provider"`
		Total        int64   `gorm:"column:total"`
		SuccessCount int64   `gorm:"column:success_count"`
		AvgLatency   float64 `gorm:"column:avg_lat"`
		TotalTokens  int64   `gorm:"column:total_tkns"`
		TotalCost    float64 `gorm:"column:total_cost"`
	}
	q := s.db.WithContext(ctx).Table("mv_logs_hourly")
	q = applyMatViewFilters(q, filters)
	if err := q.Select(`
		model, provider,
		SUM(count) AS total,
		SUM(success_count) AS success_count,
		CASE WHEN SUM(count) > 0 THEN SUM(avg_latency * count) / SUM(count) ELSE 0 END AS avg_lat,
		SUM(total_tokens) AS total_tkns,
		SUM(total_cost) AS total_cost
	`).Group("model, provider").
		Order("total DESC").
		Find(&results).Error; err != nil {
		return nil, err
	}

	// Previous period for trend (same duration, ending just before current start)
	type prevRow struct {
		Model       string  `gorm:"column:model"`
		Provider    string  `gorm:"column:provider"`
		Total       int64   `gorm:"column:total"`
		AvgLatency  float64 `gorm:"column:avg_lat"`
		TotalTokens int64   `gorm:"column:total_tkns"`
		TotalCost   float64 `gorm:"column:total_cost"`
	}
	var prevResults []prevRow
	if filters.StartTime != nil && filters.EndTime != nil {
		duration := filters.EndTime.Sub(*filters.StartTime)
		prevStart := filters.StartTime.Add(-duration)
		prevEnd := filters.StartTime.Add(-time.Nanosecond)
		prevFilters := filters
		prevFilters.StartTime = &prevStart
		prevFilters.EndTime = &prevEnd
		pq := s.db.WithContext(ctx).Table("mv_logs_hourly")
		pq = applyMatViewFilters(pq, prevFilters)
		if err := pq.Select(`
			model, provider,
			SUM(count) AS total,
			CASE WHEN SUM(count) > 0 THEN SUM(avg_latency * count) / SUM(count) ELSE 0 END AS avg_lat,
			SUM(total_tokens) AS total_tkns,
			SUM(total_cost) AS total_cost
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
	q := s.db.WithContext(ctx).Table("mv_logs_hourly")
	q = applyMatViewFilters(q, filters)
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
		duration := filters.EndTime.Sub(*filters.StartTime)
		prevStart := filters.StartTime.Add(-duration)
		prevEnd := filters.StartTime.Add(-time.Nanosecond)
		prevFilters := filters
		prevFilters.StartTime = &prevStart
		prevFilters.EndTime = &prevEnd
		pq := s.db.WithContext(ctx).Table("mv_logs_hourly")
		pq = applyMatViewFilters(pq, prevFilters)
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

// ---------------------------------------------------------------------------
// Filterdata from mat view
// ---------------------------------------------------------------------------

// getDistinctModelsFromMatView returns unique model names from mv_filter_models.
// Limit matches the raw-table fallback so callers see the same row cap regardless
// of which path served the request.
func (s *RDBLogStore) getDistinctModelsFromMatView(ctx context.Context) ([]string, error) {
	var models []string
	if err := s.db.WithContext(ctx).Table("mv_filter_models").
		Distinct("model").
		Where("model != ''").
		Limit(defaultFilterDataLimit).
		Pluck("model", &models).Error; err != nil {
		return nil, err
	}
	return models, nil
}

// getDistinctAliasesFromMatView returns unique alias values from mv_filter_aliases.
func (s *RDBLogStore) getDistinctAliasesFromMatView(ctx context.Context) ([]string, error) {
	var aliases []string
	if err := s.db.WithContext(ctx).Table("mv_filter_aliases").
		Distinct("alias").
		Where("alias != ''").
		Limit(defaultFilterDataLimit).
		Pluck("alias", &aliases).Error; err != nil {
		return nil, err
	}
	return aliases, nil
}

// getDistinctStopReasonsFromMatView returns unique stop reasons from mv_filter_stop_reasons.
func (s *RDBLogStore) getDistinctStopReasonsFromMatView(ctx context.Context) ([]string, error) {
	var stopReasons []string
	if err := s.db.WithContext(ctx).Table("mv_filter_stop_reasons").
		Distinct("stop_reason").
		Where("stop_reason != ''").
		Limit(defaultFilterDataLimit).
		Pluck("stop_reason", &stopReasons).Error; err != nil {
		return nil, err
	}
	return stopReasons, nil
}

// getDistinctKeyPairsFromMatView returns unique ID-Name pairs for the given
// (idCol, nameCol) by selecting from the per-dimension matview pre-aggregated
// for that pair. Returns (nil, false) if no matview is registered for the pair —
// callers fall back to the raw-table path.
func (s *RDBLogStore) getDistinctKeyPairsFromMatView(ctx context.Context, idCol, nameCol string) ([]KeyPairResult, bool, error) {
	view, ok := filterMatViewKeyPairColumns[[2]string{idCol, nameCol}]
	if !ok {
		return nil, false, nil
	}
	var results []KeyPairResult
	q := s.db.WithContext(ctx).Table(view).Where("id != ''")
	// User matview stores name = id and the view-level WHERE already filters
	// empty ids; other matviews include name and we additionally guard against
	// stragglers with empty names.
	if !(idCol == "user_id" && nameCol == "user_id") {
		q = q.Where("name != ''")
	}
	if err := q.
		Select("DISTINCT id, name").
		Limit(defaultFilterDataLimit).
		Find(&results).Error; err != nil {
		return nil, true, err
	}
	return results, true, nil
}

// getDistinctRoutingEnginesFromMatView returns unique routing engine names by
// parsing the comma-separated routing_engines_used values from
// mv_filter_routing_engines.
func (s *RDBLogStore) getDistinctRoutingEnginesFromMatView(ctx context.Context) ([]string, error) {
	var rawValues []string
	if err := s.db.WithContext(ctx).Table("mv_filter_routing_engines").
		Distinct("routing_engines_used").
		Where("routing_engines_used != ''").
		Limit(defaultFilterDataLimit).
		Pluck("routing_engines_used", &rawValues).Error; err != nil {
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
	return sortedStringKeys(seen), nil
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
