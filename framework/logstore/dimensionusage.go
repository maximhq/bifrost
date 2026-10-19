package logstore

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/maximhq/bifrost/framework/queryscope"
	"gorm.io/gorm"
)

// DimensionUsageEntry is one entity's usage on a rollup dimension inside the
// filter window. Trend compares it with the equally long window before, and is
// filled only when GetDimensionUsage was asked to compare.
type DimensionUsageEntry struct {
	ID            string                `json:"id"`
	TotalRequests int64                 `json:"total_requests"`
	TotalTokens   int64                 `json:"total_tokens"`
	TotalCost     float64               `json:"total_cost"`
	Trend         DimensionRankingTrend `json:"trend"`
}

// DimensionModelUsageEntry is one (entity, model, provider) group's usage on a
// rollup dimension inside the filter window.
type DimensionModelUsageEntry struct {
	ID            string  `json:"id"`
	Model         string  `json:"model"`
	Provider      string  `json:"provider"`
	TotalRequests int64   `json:"total_requests"`
	TotalCost     float64 `json:"total_cost"`
}

// usageRead is how a usage reader reads one dimension: the relation and id
// expression to group on, and the filters to apply.
type usageRead struct {
	src     dimensionReadSource
	idCol   string
	filters SearchFilters
	// ownIDs holds the filter on the dimension's own ids when it is applied to
	// the fanned-out id rather than to each row's id columns.
	ownIDs []string
}

// usageReadFor resolves how the usage readers read a dimension. It attributes a
// request to the same entities GetDimensionRankings does, with two differences
// that only change the cost of the read:
//
//   - On Postgres a fanned-out dimension reads ids only, through one
//     set-returning call over the array-or-scalar choice. The shared fan-out
//     relation also parses the names column and branches through a correlated
//     UNION ALL, which keeps the planner from running the fan-out in parallel
//     workers; on a large window that relation is most of the read.
//   - A filter on the dimension's own ids is matched against the fanned-out id
//     instead of each row's id columns, so a row is not parsed once per filtered
//     id. Each filtered entity is credited the same rows either way, but only
//     the filtered entities are returned: the other entities those rows carry,
//     and Unassigned, are left out.
//
// The JSON-field and comma-list dimensions have no single id column and are not
// supported.
func (s *RDBLogStore) usageReadFor(dimension RankingDimension, filters SearchFilters) (usageRead, error) {
	if _, isJSONField := jsonFieldDimensions[dimension]; isJSONField {
		return usageRead{}, fmt.Errorf("dimension %s has no usage reader", dimension)
	}
	if _, isCommaList := commaListDimensions[dimension]; isCommaList {
		return usageRead{}, fmt.Errorf("dimension %s has no usage reader", dimension)
	}
	idCol, nameCol, ok := DimensionColumnDef(dimension)
	if !ok {
		return usageRead{}, fmt.Errorf("invalid ranking dimension: %s", dimension)
	}
	read := usageRead{src: s.dimensionReadSourceFor(idCol, nameCol), idCol: idCol, filters: filters}
	if !read.src.FannedOut {
		return read, nil
	}
	if s.db.Dialector.Name() == "postgres" {
		if from, ok := postgresUsageFanoutFrom(idCol); ok {
			read.src.FromSQL = from
			read.src.NameCol = ""
		}
	}
	switch idCol {
	case "team_id":
		read.ownIDs, read.filters.TeamIDs = filters.TeamIDs, nil
	case "customer_id":
		read.ownIDs, read.filters.CustomerIDs = filters.CustomerIDs, nil
	case "business_unit_id":
		read.ownIDs, read.filters.BusinessUnitIDs = filters.BusinessUnitIDs, nil
	}
	return read, nil
}

// postgresUsageFanoutFrom is the ids-only Postgres fan-out the usage readers read
// a team, customer or business unit through: one row per id in the JSON-array
// column, or the scalar column for a row whose array is missing or malformed,
// the same choice teamOrBUFanoutFrom makes. It stays separate from that text,
// which the filter matviews are built from.
func postgresUsageFanoutFrom(idCol string) (string, bool) {
	arrIDs, _, _, ok := fanoutDimensionColumns(idCol)
	if !ok {
		return "", false
	}
	return fmt.Sprintf(`(
	SELECT l.*, fan.dim_id AS dim_id
	FROM logs l
	CROSS JOIN LATERAL jsonb_array_elements_text(
		CASE WHEN l.%[1]s IS JSON ARRAY THEN l.%[1]s::jsonb ELSE jsonb_build_array(l.%[2]s) END
	) AS fan(dim_id)
) AS logs`, arrIDs, idCol), true
}

// rollupUsageQuery starts a terminal-status read of the dimension's relation
// under the caller's row scope and dimension ceiling, with filters applied.
func (s *RDBLogStore) rollupUsageQuery(ctx context.Context, read usageRead) *gorm.DB {
	q := read.src.base(s.scopedLogsDB(ctx))
	q = s.applyFilters(q, read.filters)
	q = q.Where("status IN ?", terminalLogStatuses)
	q = applyDimensionCeiling(ctx, q, read.src, read.idCol)
	if !read.src.Bucketed {
		q = q.Where(fmt.Sprintf("%s IS NOT NULL AND %s != ''", read.idCol, read.idCol))
	}
	if len(read.ownIDs) > 0 {
		// One bind for the whole list, which can exceed the per-statement
		// parameter limit as an "IN ?" list.
		inClause, inArg := queryscope.InStrings(q, "dim_id", read.ownIDs)
		q = q.Where(inClause, inArg)
	}
	return q
}

// GetDimensionUsage returns each entity's requests, tokens and cost in one
// grouped read, read the way usageReadFor describes. With compare set and a bounded window, the same read also
// covers the equally long window before and fills each entry's Trend the way
// GetDimensionRankings does, instead of a second read over the previous
// window. Entities active only in the previous window are left out.
func (s *RDBLogStore) GetDimensionUsage(ctx context.Context, filters SearchFilters, dimension RankingDimension, compare bool) ([]DimensionUsageEntry, error) {
	compare = compare && filters.StartTime != nil && filters.EndTime != nil
	window := filters
	current := "1 = 1"
	var args []any
	if compare {
		prevStart := prevWindowStart(*filters.StartTime, *filters.EndTime)
		window.StartTime = &prevStart
		placeholder, arg := timestampBound(s.db.Dialector.Name(), *filters.StartTime, lowerTimeBound)
		current = "timestamp >= " + placeholder
		// Every sum below splits on the boundary, so each one binds it once.
		args = []any{arg, arg, arg, arg, arg, arg}
	}

	read, err := s.usageReadFor(dimension, window)
	if err != nil {
		return nil, err
	}

	// Without a comparison every row is current and only the current sums are read.
	columns := []string{
		read.src.IDExpr + " AS id",
		fmt.Sprintf("SUM(CASE WHEN %s THEN 1 ELSE 0 END) AS total_requests", current),
		fmt.Sprintf("COALESCE(SUM(CASE WHEN %s THEN total_tokens ELSE 0 END), 0) AS total_tokens", current),
		fmt.Sprintf("COALESCE(SUM(CASE WHEN %s THEN cost ELSE 0 END), 0) AS total_cost", current),
	}
	if compare {
		columns = append(columns,
			fmt.Sprintf("SUM(CASE WHEN %s THEN 0 ELSE 1 END) AS prev_requests", current),
			fmt.Sprintf("COALESCE(SUM(CASE WHEN %s THEN 0 ELSE total_tokens END), 0) AS prev_tokens", current),
			fmt.Sprintf("COALESCE(SUM(CASE WHEN %s THEN 0 ELSE cost END), 0) AS prev_cost", current),
		)
	}

	var rows []struct {
		ID            string          `gorm:"column:id"`
		TotalRequests int64           `gorm:"column:total_requests"`
		TotalTokens   sql.NullInt64   `gorm:"column:total_tokens"`
		TotalCost     sql.NullFloat64 `gorm:"column:total_cost"`
		PrevRequests  int64           `gorm:"column:prev_requests"`
		PrevTokens    sql.NullInt64   `gorm:"column:prev_tokens"`
		PrevCost      sql.NullFloat64 `gorm:"column:prev_cost"`
	}
	q := s.rollupUsageQuery(ctx, read)
	if err := q.Select(strings.Join(columns, ", "), args...).Group(read.src.IDExpr).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("failed to get dimension usage for %s: %w", dimension, err)
	}

	entries := make([]DimensionUsageEntry, 0, len(rows))
	for _, r := range rows {
		if r.TotalRequests == 0 {
			continue
		}
		entry := DimensionUsageEntry{
			ID:            r.ID,
			TotalRequests: r.TotalRequests,
			TotalTokens:   r.TotalTokens.Int64,
			TotalCost:     r.TotalCost.Float64,
		}
		if r.PrevRequests > 0 {
			entry.Trend = DimensionRankingTrend{
				HasPreviousPeriod: true,
				RequestsTrend:     pctChange(float64(r.PrevRequests), float64(r.TotalRequests)),
				TokensTrend:       metricTrend(float64(r.PrevTokens.Int64), float64(r.TotalTokens.Int64)),
				CostTrend:         metricTrend(r.PrevCost.Float64, r.TotalCost.Float64),
			}
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

// GetDimensionModelUsage returns requests and cost per (entity, model, provider)
// in one grouped read, read like GetDimensionUsage. It replaces one
// GetModelRankings call per entity for callers that only need each entity's
// model mix: no previous window, no ordering and no row limit.
func (s *RDBLogStore) GetDimensionModelUsage(ctx context.Context, filters SearchFilters, dimension RankingDimension) ([]DimensionModelUsageEntry, error) {
	read, err := s.usageReadFor(dimension, filters)
	if err != nil {
		return nil, err
	}
	var rows []struct {
		ID            string          `gorm:"column:id"`
		Model         string          `gorm:"column:model"`
		Provider      string          `gorm:"column:provider"`
		TotalRequests int64           `gorm:"column:total_requests"`
		TotalCost     sql.NullFloat64 `gorm:"column:total_cost"`
	}
	q := s.rollupUsageQuery(ctx, read)
	q = q.Where("model IS NOT NULL AND model != ''")
	if err := q.Select(read.src.IDExpr + " AS id, model, provider, COUNT(*) AS total_requests, COALESCE(SUM(cost), 0) AS total_cost").
		Group(read.src.IDExpr + ", model, provider").
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("failed to get dimension model usage for %s: %w", dimension, err)
	}
	entries := make([]DimensionModelUsageEntry, len(rows))
	for i, r := range rows {
		entries[i] = DimensionModelUsageEntry{
			ID:            r.ID,
			Model:         r.Model,
			Provider:      r.Provider,
			TotalRequests: r.TotalRequests,
			TotalCost:     r.TotalCost.Float64,
		}
	}
	return entries, nil
}

// mcpDimensionColumn names the MCP tool log column that holds a dimension's id.
// MCP tool logs carry scalar ownership columns only.
func mcpDimensionColumn(dimension RankingDimension) (string, bool) {
	switch dimension {
	case RankingDimensionUser:
		return "user_id", true
	case RankingDimensionTeam:
		return "team_id", true
	case RankingDimensionCustomer:
		return "customer_id", true
	case RankingDimensionBusinessUnit:
		return "business_unit_id", true
	}
	return "", false
}

// GetMCPToolCountsByDimension returns how many distinct tools each entity
// called inside the filter window, in one grouped read over the tool calls
// GetMCPTopTools counts. It replaces one GetMCPTopTools call per entity. An
// entity the caller's dimension ceiling hides is left out.
func (s *RDBLogStore) GetMCPToolCountsByDimension(ctx context.Context, filters MCPToolLogSearchFilters, dimension RankingDimension) (map[string]int, error) {
	col, ok := mcpDimensionColumn(dimension)
	if !ok {
		return nil, fmt.Errorf("dimension %s has no MCP tool count reader", dimension)
	}
	q := s.ScopedDB(ctx).Model(&MCPToolLog{})
	q = s.applyMCPFilters(q, filters)
	q = q.Where("status IN ?", []string{"success", "error"})
	q = q.Where(fmt.Sprintf("%s IS NOT NULL AND %s != ''", col, col))
	// Grouping by the owner column names the owners, so the caller's dimension
	// ceiling applies here as it does to the LLM usage reads.
	q = applyDimensionCeiling(ctx, q, dimensionReadSource{}, col)
	var rows []struct {
		ID        string `gorm:"column:id"`
		ToolCount int64  `gorm:"column:tool_count"`
	}
	if err := q.Select(col + " AS id, COUNT(DISTINCT tool_name) AS tool_count").
		Group(col).
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("failed to get mcp tool counts for %s: %w", dimension, err)
	}
	counts := make(map[string]int, len(rows))
	for _, r := range rows {
		counts[r.ID] = int(r.ToolCount)
	}
	return counts, nil
}

// prevWindowStart returns the start of the equally long window before
// [start, end], the window GetDimensionRankings compares against.
func prevWindowStart(start, end time.Time) time.Time {
	return start.Add(-end.Sub(start))
}
