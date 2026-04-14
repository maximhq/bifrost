package logstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"golang.org/x/sync/errgroup"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// validMetadataKeyRegex allows alphanumeric, hyphens, underscores, and dots in metadata keys.
var validMetadataKeyRegex = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

// isValidMetadataKey validates a metadata key to prevent SQL injection.
func isValidMetadataKey(key string) bool {
	return key != "" && len(key) <= 256 && validMetadataKeyRegex.MatchString(key)
}

const bulkUpdateCostChunkSize = 500
const sessionLogPageLimit = 50

const (
	// defaultMaxQueryLimit is a safety cap for unbounded queries (FindAll, FindAllDistinct).
	defaultMaxQueryLimit = 10000
	// defaultMaxSearchLimit is the maximum number of rows returned by SearchLogs / SearchMCPToolLogs.
	defaultMaxSearchLimit = 1000
	// defaultMaxRankingsLimit caps the number of model+provider groups returned by GetModelRankings.
	defaultMaxRankingsLimit = 100
	// defaultFilterDataCutoffDays limits GetDistinct* filter-data queries to recent data.
	defaultFilterDataCutoffDays = 30
	// defaultFilterDataLimit caps the number of distinct values returned by filter-data queries.
	defaultFilterDataLimit = 500
)

// RDBLogStore represents a log store that uses a SQLite database.
type RDBLogStore struct {
	db             *gorm.DB
	logger         schemas.Logger
	matViewsReady  atomic.Bool
}

// generateBucketTimestamps generates all bucket timestamps for a time range.
// It aligns the start time to bucket boundaries and generates timestamps up to (but not exceeding) the end time.
func generateBucketTimestamps(startTime, endTime *time.Time, bucketSizeSeconds int64) []int64 {
	if startTime == nil || endTime == nil || bucketSizeSeconds <= 0 {
		return nil
	}

	startUnix := startTime.Unix()
	endUnix := endTime.Unix()

	// Align start time to bucket boundary
	alignedStart := (startUnix / bucketSizeSeconds) * bucketSizeSeconds

	// Generate all bucket timestamps
	var timestamps []int64
	for ts := alignedStart; ts <= endUnix; ts += bucketSizeSeconds {
		timestamps = append(timestamps, ts)
	}

	return timestamps
}

// applyFilters applies search filters to a GORM query
func (s *RDBLogStore) applyFilters(baseQuery *gorm.DB, filters SearchFilters) *gorm.DB {
	if len(filters.Providers) > 0 {
		baseQuery = baseQuery.Where("provider IN ?", filters.Providers)
	}
	if len(filters.Models) > 0 {
		baseQuery = baseQuery.Where("model IN ?", filters.Models)
	}
	if len(filters.Aliases) > 0 {
		baseQuery = baseQuery.Where("alias IN ?", filters.Aliases)
	}
	if len(filters.Status) > 0 {
		baseQuery = baseQuery.Where("status IN ?", filters.Status)
	}
	if len(filters.Objects) > 0 {
		baseQuery = baseQuery.Where("object_type IN ?", filters.Objects)
	}
	if filters.ParentRequestID != "" {
		baseQuery = baseQuery.Where("parent_request_id = ?", filters.ParentRequestID)
	}
	if len(filters.SelectedKeyIDs) > 0 {
		baseQuery = baseQuery.Where("selected_key_id IN ?", filters.SelectedKeyIDs)
	}
	if len(filters.VirtualKeyIDs) > 0 {
		baseQuery = baseQuery.Where("virtual_key_id IN ?", filters.VirtualKeyIDs)
	}
	if len(filters.RoutingRuleIDs) > 0 {
		baseQuery = baseQuery.Where("routing_rule_id IN ?", filters.RoutingRuleIDs)
	}
	if len(filters.TeamIDs) > 0 {
		baseQuery = baseQuery.Where("team_id IN ?", filters.TeamIDs)
	}
	if len(filters.CustomerIDs) > 0 {
		baseQuery = baseQuery.Where("customer_id IN ?", filters.CustomerIDs)
	}
	if len(filters.UserIDs) > 0 {
		baseQuery = baseQuery.Where("user_id IN ?", filters.UserIDs)
	}
	if len(filters.BusinessUnitIDs) > 0 {
		baseQuery = baseQuery.Where("business_unit_id IN ?", filters.BusinessUnitIDs)
	}
	if len(filters.RoutingEngineUsed) > 0 {
		// Query routing engines (comma-separated values) - find logs containing ANY of the specified engines
		dialect := s.db.Dialector.Name()

		// Collect non-empty engine values
		var engines []string
		for _, engine := range filters.RoutingEngineUsed {
			engine = strings.TrimSpace(engine)
			if engine != "" {
				engines = append(engines, engine)
			}
		}

		if len(engines) > 0 {
			switch dialect {
			case "postgres":
				// Use array overlap operator which can leverage the GIN index on
				// string_to_array(routing_engines_used, ',').
				placeholders := make([]string, len(engines))
				args := make([]interface{}, len(engines))
				for i, e := range engines {
					placeholders[i] = "?"
					args[i] = e
				}
				baseQuery = baseQuery.Where(
					"string_to_array(routing_engines_used, ',') && ARRAY["+strings.Join(placeholders, ",")+"]::text[]",
					args...,
				)
			default:
				// SQLite and others: use delimiter-aware LIKE matching
				var engineConditions []string
				var engineArgs []interface{}
				var concatExpr string
				if dialect == "sqlite" {
					concatExpr = "',' || routing_engines_used || ','"
				} else {
					concatExpr = "CONCAT(',', routing_engines_used, ',')"
				}
				for _, engine := range engines {
					engineConditions = append(engineConditions, concatExpr+" LIKE ?")
					engineArgs = append(engineArgs, "%,"+engine+",%")
				}
				baseQuery = baseQuery.Where(strings.Join(engineConditions, " OR "), engineArgs...)
			}
		}
	}
	if filters.StartTime != nil {
		baseQuery = baseQuery.Where("timestamp >= ?", *filters.StartTime)
	}
	if filters.EndTime != nil {
		baseQuery = baseQuery.Where("timestamp <= ?", *filters.EndTime)
	}
	if filters.MinLatency != nil {
		baseQuery = baseQuery.Where("latency >= ?", *filters.MinLatency)
	}
	if filters.MaxLatency != nil {
		baseQuery = baseQuery.Where("latency <= ?", *filters.MaxLatency)
	}
	if filters.MinTokens != nil {
		baseQuery = baseQuery.Where("total_tokens >= ?", *filters.MinTokens)
	}
	if filters.MaxTokens != nil {
		baseQuery = baseQuery.Where("total_tokens <= ?", *filters.MaxTokens)
	}
	if filters.MinCost != nil {
		baseQuery = baseQuery.Where("cost >= ?", *filters.MinCost)
	}
	if filters.MaxCost != nil {
		baseQuery = baseQuery.Where("cost <= ?", *filters.MaxCost)
	}
	if filters.MissingCostOnly {
		// cost is null and status is not error
		baseQuery = baseQuery.Where("(cost IS NULL OR cost <= 0) AND status NOT IN ('error')")
	}
	if filters.ContentSearch != "" {
		dialect := s.db.Dialector.Name()
		if dialect == "postgres" {
			baseQuery = baseQuery.Where("to_tsvector('simple', content_summary) @@ plainto_tsquery('simple', ?)", filters.ContentSearch)
		} else {
			baseQuery = baseQuery.Where("content_summary LIKE ?", "%"+filters.ContentSearch+"%")
		}
	}
	if len(filters.MetadataFilters) > 0 {
		dialect := s.db.Dialector.Name()
		// Guard must match the partial-index predicate so the planner uses the GIN index.
		// SQLite does not support IS JSON OBJECT, so fall back to the equivalent json_type check.
		if dialect == "postgres" {
			baseQuery = baseQuery.Where("metadata IS NOT NULL AND metadata IS JSON OBJECT")
		} else {
			baseQuery = baseQuery.Where("metadata IS NOT NULL AND json_valid(metadata) AND json_type(metadata) = 'object'")
		}
		for key, value := range filters.MetadataFilters {
			if !isValidMetadataKey(key) {
				continue
			}
			switch dialect {
			case "postgres":
				// Use @> containment operator to leverage GIN index on metadata::jsonb
				// Preserve value type (number/boolean) for JSON containment
				var jsonFragment string
				if value == "true" || value == "false" {
					jsonFragment = fmt.Sprintf(`{%q: %s}`, key, value)
				} else if f, err := strconv.ParseFloat(value, 64); err == nil && !math.IsNaN(f) && !math.IsInf(f, 0) {
					// Reject NaN/Inf which would produce invalid JSON; normalize the number
					jsonFragment = fmt.Sprintf(`{%q: %s}`, key, strconv.FormatFloat(f, 'f', -1, 64))
				} else {
					jsonFragment = fmt.Sprintf(`{%q: %q}`, key, value)
				}
				baseQuery = baseQuery.Where("metadata::jsonb @> ?::jsonb", jsonFragment)
			default:
				// SQLite: quote the member name so dots/hyphens stay part of the key
				path := `$."` + key + `"`
				if value == "true" {
					// json_extract returns 1 for true, but json_type returns 'true'
					baseQuery = baseQuery.Where("json_type(metadata, ?) = 'true'", path)
				} else if value == "false" {
					baseQuery = baseQuery.Where("json_type(metadata, ?) = 'false'", path)
				} else {
					// Numeric and string values: compare both as-is and as text
					baseQuery = baseQuery.Where("json_extract(metadata, ?) = ? OR CAST(json_extract(metadata, ?) AS TEXT) = ?", path, value, path, value)
				}
			}
		}
	}
	return baseQuery
}

// Create inserts a new log entry into the database.
func (s *RDBLogStore) Create(ctx context.Context, entry *Log) error {
	return s.db.WithContext(ctx).Create(entry).Error
}

// CreateIfNotExists inserts a new log entry only if it doesn't already exist.
// Uses ON CONFLICT DO NOTHING to handle duplicate key errors gracefully.
func (s *RDBLogStore) CreateIfNotExists(ctx context.Context, entry *Log) error {
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "id"}},
		DoNothing: true,
	}).Create(entry).Error
}

// BatchCreateIfNotExists inserts multiple log entries in a single transaction.
// Uses ON CONFLICT DO NOTHING for idempotency.
func (s *RDBLogStore) BatchCreateIfNotExists(ctx context.Context, entries []*Log) error {
	if len(entries) == 0 {
		return nil
	}
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "id"}},
		DoNothing: true,
	}).Create(&entries).Error
}

// Ping checks if the database is reachable.
func (s *RDBLogStore) Ping(ctx context.Context) error {
	return s.db.WithContext(ctx).Exec("SELECT 1").Error
}

// Update updates a log entry in the database.
func (s *RDBLogStore) Update(ctx context.Context, id string, entry any) error {
	serializedEntry, err := serializeLogUpdateEntry(entry)
	if err != nil {
		return err
	}

	tx := s.db.WithContext(ctx).Model(&Log{}).Where("id = ?", id).Updates(serializedEntry)
	if tx.Error != nil {
		return tx.Error
	}
	if tx.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// BulkUpdateCost updates log costs in bulk, using a PostgreSQL-specific batched
// VALUES update when available and per-row updates for other dialects.
func (s *RDBLogStore) BulkUpdateCost(ctx context.Context, updates map[string]float64) error {
	if len(updates) == 0 {
		return nil
	}

	if s.db.Dialector.Name() == "postgres" {
		return s.bulkUpdateCostPostgres(ctx, updates)
	}

	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for id, cost := range updates {
			costValue := cost
			if err := tx.Model(&Log{}).Where("id = ?", id).Update("cost", costValue).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// serializeLogUpdateEntry serializes parsed Log fields before passing the
// update payload to GORM. Non-Log payloads are returned unchanged.
func serializeLogUpdateEntry(entry any) (any, error) {
	switch v := entry.(type) {
	case *Log:
		if err := v.SerializeFields(); err != nil {
			return nil, err
		}
		return v, nil
	case Log:
		copyEntry := v
		if err := copyEntry.SerializeFields(); err != nil {
			return nil, err
		}
		return copyEntry, nil
	default:
		return entry, nil
	}
}

// buildBulkUpdateCostPostgresSQL builds a deterministic UPDATE ... FROM
// (VALUES ...) statement and argument list for a chunk of PostgreSQL log cost
// updates.
func buildBulkUpdateCostPostgresSQL(ids []string, updates map[string]float64) (string, []interface{}) {
	var sqlBuilder strings.Builder
	args := make([]interface{}, 0, len(ids)*2)

	sqlBuilder.WriteString("UPDATE logs SET cost = v.cost FROM (VALUES ")
	for i, id := range ids {
		if i > 0 {
			sqlBuilder.WriteString(",")
		}
		argOffset := i * 2
		sqlBuilder.WriteString("($")
		sqlBuilder.WriteString(strconv.Itoa(argOffset + 1))
		sqlBuilder.WriteString("::text,$")
		sqlBuilder.WriteString(strconv.Itoa(argOffset + 2))
		sqlBuilder.WriteString("::float8)")
		args = append(args, id, updates[id])
	}
	sqlBuilder.WriteString(") AS v(id, cost) WHERE logs.id = v.id")

	return sqlBuilder.String(), args
}

// bulkUpdateCostPostgres applies chunked PostgreSQL bulk cost updates to avoid
// issuing one UPDATE per log row.
func (s *RDBLogStore) bulkUpdateCostPostgres(ctx context.Context, updates map[string]float64) error {
	ids := make([]string, 0, len(updates))
	for id := range updates {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for start := 0; start < len(ids); start += bulkUpdateCostChunkSize {
			end := min(start+bulkUpdateCostChunkSize, len(ids))
			query, args := buildBulkUpdateCostPostgresSQL(ids[start:end], updates)
			if err := tx.Exec(query, args...).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// SearchLogs searches for logs in the database without calculating statistics.
func (s *RDBLogStore) SearchLogs(ctx context.Context, filters SearchFilters, pagination PaginationOptions) (*SearchResult, error) {
	// Build order clause up front (needed by the data goroutine).
	direction := "DESC"
	if pagination.Order == "asc" {
		direction = "ASC"
	}

	var orderClause string
	switch pagination.SortBy {
	case "timestamp":
		orderClause = "timestamp " + direction
	case "latency":
		orderClause = "latency " + direction
	case "tokens":
		orderClause = "total_tokens " + direction
	case "cost":
		orderClause = "cost " + direction
	default:
		orderClause = "timestamp " + direction
	}

	limit := pagination.Limit
	if limit <= 0 || limit > defaultMaxSearchLimit {
		limit = defaultMaxSearchLimit
	}
	pagination.Limit = limit

	// Run COUNT and data fetch concurrently — the COUNT on large tables is the
	// bottleneck, so overlapping it with the (fast) data query halves wall time.
	// Each goroutine builds its own *gorm.DB because Count() mutates the session.
	var totalCount int64
	var logs []Log

	g, gCtx := errgroup.WithContext(ctx)

	g.Go(func() error {
		if s.db.Dialector.Name() == "postgres" && s.canUseMatView(filters) {
			var err error
			totalCount, err = s.getCountFromMatView(gCtx, filters)
			return err
		}
		countQuery := s.db.WithContext(gCtx).Model(&Log{})
		countQuery = s.applyFilters(countQuery, filters)
		return countQuery.Count(&totalCount).Error
	})

	g.Go(func() error {
		dataQuery := s.db.WithContext(gCtx).Model(&Log{})
		dataQuery = s.applyFilters(dataQuery, filters)
		dataQuery = dataQuery.Order(orderClause).Select(s.listSelectColumns()).Limit(limit)
		if pagination.Offset > 0 {
			dataQuery = dataQuery.Offset(pagination.Offset)
		}
		err := dataQuery.Find(&logs).Error
		if err != nil && errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	})

	if err := g.Wait(); err != nil {
		return nil, err
	}

	hasLogs := len(logs) > 0
	if !hasLogs {
		var err error
		hasLogs, err = s.HasLogs(ctx)
		if err != nil {
			return nil, err
		}
	}

	return &SearchResult{
		Logs:       logs,
		Pagination: pagination,
		Stats: SearchStats{
			TotalRequests: totalCount,
		},
		HasLogs: hasLogs,
	}, nil
}

// GetSessionLogs returns paginated logs for a single parent_request_id session.
func (s *RDBLogStore) GetSessionLogs(ctx context.Context, sessionID string, pagination PaginationOptions) (*SessionDetailResult, error) {
	if strings.TrimSpace(sessionID) == "" {
		return nil, fmt.Errorf("sessionID cannot be empty")
	}

	limit := pagination.Limit
	if limit <= 0 || limit > sessionLogPageLimit {
		limit = sessionLogPageLimit
	}
	pagination.Limit = limit
	if pagination.Offset < 0 {
		pagination.Offset = 0
	}

	pagination.SortBy = "timestamp"
	orderDir := "ASC"
	if pagination.Order == "desc" {
		orderDir = "DESC"
	}
	orderClause := "timestamp " + orderDir + ", id " + orderDir

	baseQuery := s.db.WithContext(ctx).Model(&Log{}).Where("parent_request_id = ?", sessionID)

	var (
		totalCount int64
		logs       []Log
	)

	g, gCtx := errgroup.WithContext(ctx)

	g.Go(func() error {
		return s.db.WithContext(gCtx).Model(&Log{}).Where("parent_request_id = ?", sessionID).Count(&totalCount).Error
	})

	g.Go(func() error {
		dataQuery := baseQuery.Session(&gorm.Session{}).
			WithContext(gCtx).
			Order(orderClause).
			Select(s.listSelectColumns()).
			Limit(limit)
		if pagination.Offset > 0 {
			dataQuery = dataQuery.Offset(pagination.Offset)
		}
		err := dataQuery.Find(&logs).Error
		if err != nil && errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	})

	if err := g.Wait(); err != nil {
		return nil, err
	}

	pagination.TotalCount = totalCount
	returnedCount := len(logs)
	return &SessionDetailResult{
		SessionID:     sessionID,
		Logs:          logs,
		Pagination:    pagination,
		Count:         totalCount,
		ReturnedCount: returnedCount,
		HasMore:       int64(pagination.Offset+returnedCount) < totalCount,
	}, nil
}

// GetSessionSummary returns aggregate totals for a single parent_request_id session.
func (s *RDBLogStore) GetSessionSummary(ctx context.Context, sessionID string) (*SessionSummaryResult, error) {
	if strings.TrimSpace(sessionID) == "" {
		return nil, fmt.Errorf("sessionID cannot be empty")
	}

	var (
		count       int64
		totalCost   float64
		totalTokens int64
		startedAt   string
		latestAt    string
		startedRaw  any
		latestRaw   any
	)

	// Single aggregate select keeps Count/SUM/MIN/MAX consistent against the same row snapshot
	// and halves the round trips compared to running Count and the aggregate row in parallel.
	row := s.db.WithContext(ctx).
		Model(&Log{}).
		Where("parent_request_id = ?", sessionID).
		Select("COUNT(*) AS count, COALESCE(SUM(cost), 0) AS total_cost, COALESCE(SUM(total_tokens), 0) AS total_tokens, MIN(timestamp) AS started_at, MAX(timestamp) AS latest_at").
		Row()

	if err := row.Scan(&count, &totalCost, &totalTokens, &startedRaw, &latestRaw); err != nil {
		return nil, err
	}

	startedAt = normalizeAggregateTimestamp(startedRaw)
	latestAt = normalizeAggregateTimestamp(latestRaw)

	durationMs := int64(0)
	if startedAt != "" && latestAt != "" {
		if startedTime, err := time.Parse(time.RFC3339Nano, startedAt); err == nil {
			if latestTime, err := time.Parse(time.RFC3339Nano, latestAt); err == nil {
				durationMs = latestTime.Sub(startedTime).Milliseconds()
				if durationMs < 0 {
					durationMs = 0
				}
			}
		}
	}

	return &SessionSummaryResult{
		SessionID:   sessionID,
		Count:       count,
		TotalCost:   totalCost,
		TotalTokens: totalTokens,
		StartedAt:   startedAt,
		LatestAt:    latestAt,
		DurationMs:  durationMs,
	}, nil
}

func normalizeAggregateTimestamp(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case time.Time:
		return v.UTC().Format(time.RFC3339Nano)
	case []byte:
		return normalizeAggregateTimestamp(string(v))
	case string:
		raw := strings.TrimSpace(v)
		if raw == "" {
			return ""
		}
		layouts := []string{
			time.RFC3339Nano,
			time.RFC3339,
			"2006-01-02 15:04:05.999999999-07:00",
			"2006-01-02 15:04:05.999999999Z07:00",
			"2006-01-02 15:04:05.999999999",
			"2006-01-02 15:04:05",
			"2006-01-02T15:04:05.999999999",
			"2006-01-02T15:04:05",
		}
		for _, layout := range layouts {
			if parsed, err := time.Parse(layout, raw); err == nil {
				return parsed.UTC().Format(time.RFC3339Nano)
			}
		}
		return raw
	default:
		return fmt.Sprint(v)
	}
}

// listSelectColumns returns a SELECT clause for list queries that omits large
// output/detail TEXT columns and uses SQL JSON functions to extract only the
// last element from input_history and responses_input_history arrays.
//
// Realtime turn rows are kept intact because the logs table renders them as a
// combined Tool/User/Assistant summary and needs the full turn context.
func (s *RDBLogStore) listSelectColumns() string {
	baseCols := strings.Join([]string{
		"id", "parent_request_id", "timestamp", "object_type", "provider", "model", "alias",
		"number_of_retries", "fallback_index",
		"selected_key_id", "selected_key_name",
		"virtual_key_id", "virtual_key_name",
		"routing_engines_used", "routing_rule_id", "routing_rule_name",
		"user_id", "team_id", "team_name", "customer_id", "customer_name",
		"business_unit_id", "business_unit_name",
		"speech_input", "transcription_input", "image_generation_input", "video_generation_input",
		"latency", "token_usage", "cost", "status", "error_details", "stream",
		"content_summary", "metadata",
		"is_large_payload_request", "is_large_payload_response",
		"prompt_tokens", "completion_tokens", "total_tokens",
		"created_at",
	}, ", ")

	var inputHistoryExpr, responsesInputExpr, outputMessageExpr string
	switch s.db.Dialector.Name() {
	case "postgres":
		inputHistoryExpr = `CASE
			WHEN object_type = 'realtime.turn' THEN input_history
			WHEN input_history IS NOT NULL AND input_history != '' AND input_history != '[]'
			THEN jsonb_build_array(input_history::jsonb->-1)::text
			ELSE input_history END AS input_history`
		responsesInputExpr = `CASE
			WHEN object_type = 'realtime.turn' THEN responses_input_history
			WHEN responses_input_history IS NOT NULL AND responses_input_history != '' AND responses_input_history != '[]'
			THEN jsonb_build_array(responses_input_history::jsonb->-1)::text
			ELSE responses_input_history END AS responses_input_history`
		outputMessageExpr = `CASE WHEN object_type = 'realtime.turn' THEN output_message ELSE NULL END AS output_message`
	default: // sqlite
		inputHistoryExpr = `CASE
			WHEN object_type = 'realtime.turn' THEN input_history
			WHEN input_history IS NOT NULL AND input_history != '' AND input_history != '[]'
			THEN json_array(json_extract(input_history, '$[' || (json_array_length(input_history) - 1) || ']'))
			ELSE input_history END AS input_history`
		responsesInputExpr = `CASE
			WHEN object_type = 'realtime.turn' THEN responses_input_history
			WHEN responses_input_history IS NOT NULL AND responses_input_history != '' AND responses_input_history != '[]'
			THEN json_array(json_extract(responses_input_history, '$[' || (json_array_length(responses_input_history) - 1) || ']'))
			ELSE responses_input_history END AS responses_input_history`
		outputMessageExpr = `CASE WHEN object_type = 'realtime.turn' THEN output_message ELSE NULL END AS output_message`
	}

	return baseCols + ", " + inputHistoryExpr + ", " + responsesInputExpr + ", " + outputMessageExpr
}

// GetStats calculates statistics for logs matching the given filters.
func (s *RDBLogStore) GetStats(ctx context.Context, filters SearchFilters) (*SearchStats, error) {
	if s.db.Dialector.Name() == "postgres" && s.canUseMatView(filters) {
		return s.getStatsFromMatView(ctx, filters)
	}
	baseQuery := s.db.WithContext(ctx).Model(&Log{})
	baseQuery = s.applyFilters(baseQuery, filters)

	// Get total count (includes processing status)
	var totalCount int64
	if err := baseQuery.Count(&totalCount).Error; err != nil {
		return nil, err
	}

	stats := &SearchStats{
		TotalRequests: totalCount,
	}

	if totalCount > 0 {
		// Single query for all completed-request stats: counts, latency, tokens, cost
		var result struct {
			CompletedCount sql.NullInt64   `gorm:"column:completed_count"`
			SuccessCount   sql.NullInt64   `gorm:"column:success_count"`
			AvgLatency     sql.NullFloat64 `gorm:"column:avg_latency"`
			TotalTokens    sql.NullInt64   `gorm:"column:total_tokens"`
			TotalCost      sql.NullFloat64 `gorm:"column:total_cost"`
		}

		statsQuery := s.db.WithContext(ctx).Model(&Log{})
		statsQuery = s.applyFilters(statsQuery, filters)
		statsQuery = statsQuery.Where("status IN ?", []string{"success", "error"})

		if err := statsQuery.Select(`
			COUNT(*) as completed_count,
			SUM(CASE WHEN status = 'success' THEN 1 ELSE 0 END) as success_count,
			AVG(latency) as avg_latency,
			SUM(total_tokens) as total_tokens,
			SUM(cost) as total_cost
		`).Scan(&result).Error; err != nil {
			return nil, err
		}

		completedCount := result.CompletedCount.Int64
		if completedCount > 0 {
			stats.SuccessRate = float64(result.SuccessCount.Int64) / float64(completedCount) * 100
			if result.AvgLatency.Valid {
				stats.AverageLatency = result.AvgLatency.Float64
			}
			if result.TotalTokens.Valid {
				stats.TotalTokens = result.TotalTokens.Int64
			}
			if result.TotalCost.Valid {
				stats.TotalCost = result.TotalCost.Float64
			}
		}

		// User-facing success rate: count each fallback chain as one user request.
		// A chain is identified by the original request (fallback_index=0); any successful
		// attempt (original or fallback) makes the whole chain a success.
		// When scoped to a specific parent request, root rows are excluded by definition
		// (they have parent_request_id = NULL), so fall back to the per-attempt success rate.
		if filters.ParentRequestID != "" {
			stats.UserFacingSuccessRate = stats.SuccessRate
		} else {
			var userFacingResult struct {
				TotalUserRequests      sql.NullInt64 `gorm:"column:total_user_requests"`
				SuccessfulUserRequests sql.NullInt64 `gorm:"column:successful_user_requests"`
			}
			userFacingQuery := s.db.WithContext(ctx).Model(&Log{})
			userFacingQuery = s.applyFilters(userFacingQuery, filters)
			// Scope to root rows only so denominator and numerator are drawn from the same population.
			// A chain is successful if the root itself succeeded or any of its fallbacks succeeded.
			userFacingQuery = userFacingQuery.Where("fallback_index = ?", 0).Where("status IN ?", []string{"success", "error"})
			if err := userFacingQuery.Select(`
				COUNT(DISTINCT id) as total_user_requests,
				COUNT(DISTINCT CASE
					WHEN status = 'success' THEN id
					WHEN EXISTS (
						SELECT 1
						FROM logs fallback
						WHERE fallback.parent_request_id = logs.id
							AND fallback.status = 'success'
					) THEN id
					ELSE NULL
				END) as successful_user_requests
			`).Scan(&userFacingResult).Error; err != nil {
				return nil, err
			}
			if userFacingResult.TotalUserRequests.Int64 > 0 {
				stats.UserFacingSuccessRate = float64(userFacingResult.SuccessfulUserRequests.Int64) / float64(userFacingResult.TotalUserRequests.Int64) * 100
			}
		}
	}

	return stats, nil
}

// GetHistogram returns time-bucketed request counts for the given filters.
func (s *RDBLogStore) GetHistogram(ctx context.Context, filters SearchFilters, bucketSizeSeconds int64) (*HistogramResult, error) {
	if bucketSizeSeconds <= 0 {
		bucketSizeSeconds = 3600 // Default to 1 hour
	}
	if s.db.Dialector.Name() == "postgres" && s.canUseMatView(filters) && bucketSizeSeconds >= 3600 {
		return s.getHistogramFromMatView(ctx, filters, bucketSizeSeconds)
	}

	// Determine database type for SQL syntax
	dialect := s.db.Dialector.Name()

	// Build query with filters
	baseQuery := s.db.WithContext(ctx).Model(&Log{})
	baseQuery = s.applyFilters(baseQuery, filters)
	baseQuery = baseQuery.Where("status IN ?", []string{"success", "error"})

	// Query for histogram buckets - use int64 for bucket timestamp to avoid parsing issues
	var results []struct {
		BucketTimestamp int64 `gorm:"column:bucket_timestamp"`
		Total           int64 `gorm:"column:total"`
		Success         int64 `gorm:"column:success"`
		Error           int64 `gorm:"column:error_count"`
	}

	// Build select clause with database-specific unix timestamp calculation
	var selectClause string
	switch dialect {
	case "sqlite":
		// SQLite: use strftime to get unix timestamp, then bucket
		selectClause = fmt.Sprintf(`
			(CAST(strftime('%%s', timestamp) AS INTEGER) / %d) * %d as bucket_timestamp,
			COUNT(*) as total,
			SUM(CASE WHEN status = 'success' THEN 1 ELSE 0 END) as success,
			SUM(CASE WHEN status = 'error' THEN 1 ELSE 0 END) as error_count
		`, bucketSizeSeconds, bucketSizeSeconds)
	case "mysql":
		// MySQL: use UNIX_TIMESTAMP
		selectClause = fmt.Sprintf(`
			(FLOOR(UNIX_TIMESTAMP(timestamp) / %d) * %d) as bucket_timestamp,
			COUNT(*) as total,
			SUM(CASE WHEN status = 'success' THEN 1 ELSE 0 END) as success,
			SUM(CASE WHEN status = 'error' THEN 1 ELSE 0 END) as error_count
		`, bucketSizeSeconds, bucketSizeSeconds)
	default:
		// PostgreSQL (and others): use EXTRACT(EPOCH FROM timestamp)
		selectClause = fmt.Sprintf(`
			CAST(FLOOR(EXTRACT(EPOCH FROM timestamp) / %d) * %d AS BIGINT) as bucket_timestamp,
			COUNT(*) as total,
			SUM(CASE WHEN status = 'success' THEN 1 ELSE 0 END) as success,
			SUM(CASE WHEN status = 'error' THEN 1 ELSE 0 END) as error_count
		`, bucketSizeSeconds, bucketSizeSeconds)
	}

	if err := baseQuery.
		Select(selectClause).
		Group("bucket_timestamp").
		Order("bucket_timestamp ASC").
		Find(&results).Error; err != nil {
		return nil, fmt.Errorf("failed to get histogram: %w", err)
	}

	// Create a map of bucket timestamp -> result for quick lookup
	resultMap := make(map[int64]struct {
		Total   int64
		Success int64
		Error   int64
	})
	for _, r := range results {
		resultMap[r.BucketTimestamp] = struct {
			Total   int64
			Success int64
			Error   int64
		}{
			Total:   r.Total,
			Success: r.Success,
			Error:   r.Error,
		}
	}

	// Generate all bucket timestamps for the time range
	allTimestamps := generateBucketTimestamps(filters.StartTime, filters.EndTime, bucketSizeSeconds)

	// If no time range specified, just return what we have from the query
	if len(allTimestamps) == 0 {
		buckets := make([]HistogramBucket, len(results))
		for i, r := range results {
			buckets[i] = HistogramBucket{
				Timestamp: time.Unix(r.BucketTimestamp, 0).UTC(),
				Count:     r.Total,
				Success:   r.Success,
				Error:     r.Error,
			}
		}
		return &HistogramResult{
			Buckets:           buckets,
			BucketSizeSeconds: bucketSizeSeconds,
		}, nil
	}

	// Fill in all buckets, using zeros for missing timestamps
	buckets := make([]HistogramBucket, len(allTimestamps))
	for i, ts := range allTimestamps {
		if data, exists := resultMap[ts]; exists {
			buckets[i] = HistogramBucket{
				Timestamp: time.Unix(ts, 0).UTC(),
				Count:     data.Total,
				Success:   data.Success,
				Error:     data.Error,
			}
		} else {
			buckets[i] = HistogramBucket{
				Timestamp: time.Unix(ts, 0).UTC(),
				Count:     0,
				Success:   0,
				Error:     0,
			}
		}
	}

	return &HistogramResult{
		Buckets:           buckets,
		BucketSizeSeconds: bucketSizeSeconds,
	}, nil
}

// GetTokenHistogram returns time-bucketed token usage for the given filters.
func (s *RDBLogStore) GetTokenHistogram(ctx context.Context, filters SearchFilters, bucketSizeSeconds int64) (*TokenHistogramResult, error) {
	if bucketSizeSeconds <= 0 {
		bucketSizeSeconds = 3600 // Default to 1 hour
	}
	if s.db.Dialector.Name() == "postgres" && s.canUseMatView(filters) && bucketSizeSeconds >= 3600 {
		return s.getTokenHistogramFromMatView(ctx, filters, bucketSizeSeconds)
	}

	dialect := s.db.Dialector.Name()

	baseQuery := s.db.WithContext(ctx).Model(&Log{})
	baseQuery = s.applyFilters(baseQuery, filters)
	// Only count completed requests for token stats
	baseQuery = baseQuery.Where("status IN ?", []string{"success", "error"})

	var results []struct {
		BucketTimestamp  int64 `gorm:"column:bucket_timestamp"`
		PromptTokens     int64 `gorm:"column:prompt_tokens"`
		CompletionTokens int64 `gorm:"column:completion_tokens"`
		TotalTokens      int64 `gorm:"column:total_tokens"`
		CachedReadTokens int64 `gorm:"column:cached_read_tokens"`
	}

	var selectClause string
	switch dialect {
	case "sqlite":
		selectClause = fmt.Sprintf(`
			(CAST(strftime('%%s', timestamp) AS INTEGER) / %d) * %d as bucket_timestamp,
			COALESCE(SUM(prompt_tokens), 0) as prompt_tokens,
			COALESCE(SUM(completion_tokens), 0) as completion_tokens,
			COALESCE(SUM(total_tokens), 0) as total_tokens,
			COALESCE(SUM(cached_read_tokens), 0) as cached_read_tokens
		`, bucketSizeSeconds, bucketSizeSeconds)
	case "mysql":
		selectClause = fmt.Sprintf(`
			(FLOOR(UNIX_TIMESTAMP(timestamp) / %d) * %d) as bucket_timestamp,
			COALESCE(SUM(prompt_tokens), 0) as prompt_tokens,
			COALESCE(SUM(completion_tokens), 0) as completion_tokens,
			COALESCE(SUM(total_tokens), 0) as total_tokens,
			COALESCE(SUM(cached_read_tokens), 0) as cached_read_tokens
		`, bucketSizeSeconds, bucketSizeSeconds)
	default:
		selectClause = fmt.Sprintf(`
			CAST(FLOOR(EXTRACT(EPOCH FROM timestamp) / %d) * %d AS BIGINT) as bucket_timestamp,
			COALESCE(SUM(prompt_tokens), 0) as prompt_tokens,
			COALESCE(SUM(completion_tokens), 0) as completion_tokens,
			COALESCE(SUM(total_tokens), 0) as total_tokens,
			COALESCE(SUM(cached_read_tokens), 0) as cached_read_tokens
		`, bucketSizeSeconds, bucketSizeSeconds)
	}

	if err := baseQuery.
		Select(selectClause).
		Group("bucket_timestamp").
		Order("bucket_timestamp ASC").
		Find(&results).Error; err != nil {
		return nil, fmt.Errorf("failed to get token histogram: %w", err)
	}

	// Create a map of bucket timestamp -> result for quick lookup
	resultMap := make(map[int64]struct {
		PromptTokens     int64
		CompletionTokens int64
		TotalTokens      int64
		CachedReadTokens int64
	})
	for _, r := range results {
		resultMap[r.BucketTimestamp] = struct {
			PromptTokens     int64
			CompletionTokens int64
			TotalTokens      int64
			CachedReadTokens int64
		}{
			PromptTokens:     r.PromptTokens,
			CompletionTokens: r.CompletionTokens,
			TotalTokens:      r.TotalTokens,
			CachedReadTokens: r.CachedReadTokens,
		}
	}

	// Generate all bucket timestamps for the time range
	allTimestamps := generateBucketTimestamps(filters.StartTime, filters.EndTime, bucketSizeSeconds)

	// If no time range specified, just return what we have from the query
	if len(allTimestamps) == 0 {
		buckets := make([]TokenHistogramBucket, len(results))
		for i, r := range results {
			buckets[i] = TokenHistogramBucket{
				Timestamp:        time.Unix(r.BucketTimestamp, 0).UTC(),
				PromptTokens:     r.PromptTokens,
				CompletionTokens: r.CompletionTokens,
				TotalTokens:      r.TotalTokens,
				CachedReadTokens: r.CachedReadTokens,
			}
		}
		return &TokenHistogramResult{
			Buckets:           buckets,
			BucketSizeSeconds: bucketSizeSeconds,
		}, nil
	}

	// Fill in all buckets, using zeros for missing timestamps
	buckets := make([]TokenHistogramBucket, len(allTimestamps))
	for i, ts := range allTimestamps {
		if data, exists := resultMap[ts]; exists {
			buckets[i] = TokenHistogramBucket{
				Timestamp:        time.Unix(ts, 0).UTC(),
				PromptTokens:     data.PromptTokens,
				CompletionTokens: data.CompletionTokens,
				TotalTokens:      data.TotalTokens,
				CachedReadTokens: data.CachedReadTokens,
			}
		} else {
			buckets[i] = TokenHistogramBucket{
				Timestamp: time.Unix(ts, 0).UTC(),
			}
		}
	}

	return &TokenHistogramResult{
		Buckets:           buckets,
		BucketSizeSeconds: bucketSizeSeconds,
	}, nil
}

// GetCostHistogram returns time-bucketed cost data with model breakdown for the given filters.
func (s *RDBLogStore) GetCostHistogram(ctx context.Context, filters SearchFilters, bucketSizeSeconds int64) (*CostHistogramResult, error) {
	if bucketSizeSeconds <= 0 {
		bucketSizeSeconds = 3600 // Default to 1 hour
	}
	if s.db.Dialector.Name() == "postgres" && s.canUseMatView(filters) && bucketSizeSeconds >= 3600 {
		return s.getCostHistogramFromMatView(ctx, filters, bucketSizeSeconds)
	}

	dialect := s.db.Dialector.Name()

	baseQuery := s.db.WithContext(ctx).Model(&Log{})
	baseQuery = s.applyFilters(baseQuery, filters)
	// Only count completed requests with cost
	baseQuery = baseQuery.Where("status IN ?", []string{"success", "error"})
	baseQuery = baseQuery.Where("cost IS NOT NULL AND cost > 0")

	// Query grouped by bucket and model
	var results []struct {
		BucketTimestamp int64   `gorm:"column:bucket_timestamp"`
		Model           string  `gorm:"column:model"`
		TotalCost       float64 `gorm:"column:total_cost"`
	}

	var selectClause string
	switch dialect {
	case "sqlite":
		selectClause = fmt.Sprintf(`
			(CAST(strftime('%%s', timestamp) AS INTEGER) / %d) * %d as bucket_timestamp,
			model,
			COALESCE(SUM(cost), 0) as total_cost
		`, bucketSizeSeconds, bucketSizeSeconds)
	case "mysql":
		selectClause = fmt.Sprintf(`
			(FLOOR(UNIX_TIMESTAMP(timestamp) / %d) * %d) as bucket_timestamp,
			model,
			COALESCE(SUM(cost), 0) as total_cost
		`, bucketSizeSeconds, bucketSizeSeconds)
	default:
		selectClause = fmt.Sprintf(`
			CAST(FLOOR(EXTRACT(EPOCH FROM timestamp) / %d) * %d AS BIGINT) as bucket_timestamp,
			model,
			COALESCE(SUM(cost), 0) as total_cost
		`, bucketSizeSeconds, bucketSizeSeconds)
	}

	if err := baseQuery.
		Select(selectClause).
		Group("bucket_timestamp, model").
		Order("bucket_timestamp ASC").
		Find(&results).Error; err != nil {
		return nil, fmt.Errorf("failed to get cost histogram: %w", err)
	}

	// Aggregate results into buckets with model breakdown
	bucketMap := make(map[int64]*CostHistogramBucket)
	modelsSet := make(map[string]bool)

	for _, r := range results {
		modelsSet[r.Model] = true
		if bucket, exists := bucketMap[r.BucketTimestamp]; exists {
			bucket.TotalCost += r.TotalCost
			bucket.ByModel[r.Model] = r.TotalCost
		} else {
			bucketMap[r.BucketTimestamp] = &CostHistogramBucket{
				Timestamp: time.Unix(r.BucketTimestamp, 0).UTC(),
				TotalCost: r.TotalCost,
				ByModel:   map[string]float64{r.Model: r.TotalCost},
			}
		}
	}

	// Extract unique models
	models := make([]string, 0, len(modelsSet))
	for model := range modelsSet {
		models = append(models, model)
	}

	// Generate all bucket timestamps for the time range
	allTimestamps := generateBucketTimestamps(filters.StartTime, filters.EndTime, bucketSizeSeconds)

	// If no time range specified, just return what we have from the query
	if len(allTimestamps) == 0 {
		// Convert map to sorted slice
		buckets := make([]CostHistogramBucket, 0, len(bucketMap))
		for _, bucket := range bucketMap {
			buckets = append(buckets, *bucket)
		}

		// Sort by timestamp
		sort.Slice(buckets, func(i, j int) bool {
			return buckets[i].Timestamp.Before(buckets[j].Timestamp)
		})

		return &CostHistogramResult{
			Buckets:           buckets,
			BucketSizeSeconds: bucketSizeSeconds,
			Models:            models,
		}, nil
	}

	// Fill in all buckets, using zeros for missing timestamps
	buckets := make([]CostHistogramBucket, len(allTimestamps))
	for i, ts := range allTimestamps {
		if bucket, exists := bucketMap[ts]; exists {
			buckets[i] = *bucket
		} else {
			buckets[i] = CostHistogramBucket{
				Timestamp: time.Unix(ts, 0).UTC(),
				TotalCost: 0,
				ByModel:   make(map[string]float64),
			}
		}
	}

	return &CostHistogramResult{
		Buckets:           buckets,
		BucketSizeSeconds: bucketSizeSeconds,
		Models:            models,
	}, nil
}

// GetModelHistogram returns time-bucketed model usage with success/error breakdown for the given filters.
func (s *RDBLogStore) GetModelHistogram(ctx context.Context, filters SearchFilters, bucketSizeSeconds int64) (*ModelHistogramResult, error) {
	if bucketSizeSeconds <= 0 {
		bucketSizeSeconds = 3600 // Default to 1 hour
	}
	if s.db.Dialector.Name() == "postgres" && s.canUseMatView(filters) && bucketSizeSeconds >= 3600 {
		return s.getModelHistogramFromMatView(ctx, filters, bucketSizeSeconds)
	}

	dialect := s.db.Dialector.Name()

	baseQuery := s.db.WithContext(ctx).Model(&Log{})
	baseQuery = s.applyFilters(baseQuery, filters)
	baseQuery = baseQuery.Where("status IN ?", []string{"success", "error"})

	// Query grouped by bucket and model with status counts
	var results []struct {
		BucketTimestamp int64  `gorm:"column:bucket_timestamp"`
		Model           string `gorm:"column:model"`
		Total           int64  `gorm:"column:total"`
		Success         int64  `gorm:"column:success"`
		Error           int64  `gorm:"column:error_count"`
	}

	var selectClause string
	switch dialect {
	case "sqlite":
		selectClause = fmt.Sprintf(`
			(CAST(strftime('%%s', timestamp) AS INTEGER) / %d) * %d as bucket_timestamp,
			model,
			COUNT(*) as total,
			SUM(CASE WHEN status = 'success' THEN 1 ELSE 0 END) as success,
			SUM(CASE WHEN status = 'error' THEN 1 ELSE 0 END) as error_count
		`, bucketSizeSeconds, bucketSizeSeconds)
	case "mysql":
		selectClause = fmt.Sprintf(`
			(FLOOR(UNIX_TIMESTAMP(timestamp) / %d) * %d) as bucket_timestamp,
			model,
			COUNT(*) as total,
			SUM(CASE WHEN status = 'success' THEN 1 ELSE 0 END) as success,
			SUM(CASE WHEN status = 'error' THEN 1 ELSE 0 END) as error_count
		`, bucketSizeSeconds, bucketSizeSeconds)
	default:
		selectClause = fmt.Sprintf(`
			CAST(FLOOR(EXTRACT(EPOCH FROM timestamp) / %d) * %d AS BIGINT) as bucket_timestamp,
			model,
			COUNT(*) as total,
			SUM(CASE WHEN status = 'success' THEN 1 ELSE 0 END) as success,
			SUM(CASE WHEN status = 'error' THEN 1 ELSE 0 END) as error_count
		`, bucketSizeSeconds, bucketSizeSeconds)
	}

	if err := baseQuery.
		Select(selectClause).
		Group("bucket_timestamp, model").
		Order("bucket_timestamp ASC").
		Find(&results).Error; err != nil {
		return nil, fmt.Errorf("failed to get model histogram: %w", err)
	}

	// Aggregate results into buckets with model breakdown
	bucketMap := make(map[int64]*ModelHistogramBucket)
	modelsSet := make(map[string]bool)

	for _, r := range results {
		modelsSet[r.Model] = true
		if bucket, exists := bucketMap[r.BucketTimestamp]; exists {
			bucket.ByModel[r.Model] = ModelUsageStats{
				Total:   r.Total,
				Success: r.Success,
				Error:   r.Error,
			}
		} else {
			bucketMap[r.BucketTimestamp] = &ModelHistogramBucket{
				Timestamp: time.Unix(r.BucketTimestamp, 0).UTC(),
				ByModel: map[string]ModelUsageStats{
					r.Model: {
						Total:   r.Total,
						Success: r.Success,
						Error:   r.Error,
					},
				},
			}
		}
	}

	// Extract unique models
	models := make([]string, 0, len(modelsSet))
	for model := range modelsSet {
		models = append(models, model)
	}

	// Generate all bucket timestamps for the time range
	allTimestamps := generateBucketTimestamps(filters.StartTime, filters.EndTime, bucketSizeSeconds)

	// If no time range specified, just return what we have from the query
	if len(allTimestamps) == 0 {
		// Convert map to sorted slice
		buckets := make([]ModelHistogramBucket, 0, len(bucketMap))
		for _, bucket := range bucketMap {
			buckets = append(buckets, *bucket)
		}

		// Sort by timestamp
		sort.Slice(buckets, func(i, j int) bool {
			return buckets[i].Timestamp.Before(buckets[j].Timestamp)
		})

		return &ModelHistogramResult{
			Buckets:           buckets,
			BucketSizeSeconds: bucketSizeSeconds,
			Models:            models,
		}, nil
	}

	// Fill in all buckets, using empty maps for missing timestamps
	buckets := make([]ModelHistogramBucket, len(allTimestamps))
	for i, ts := range allTimestamps {
		if bucket, exists := bucketMap[ts]; exists {
			buckets[i] = *bucket
		} else {
			buckets[i] = ModelHistogramBucket{
				Timestamp: time.Unix(ts, 0).UTC(),
				ByModel:   make(map[string]ModelUsageStats),
			}
		}
	}

	return &ModelHistogramResult{
		Buckets:           buckets,
		BucketSizeSeconds: bucketSizeSeconds,
		Models:            models,
	}, nil
}

// computePercentile computes the p-th percentile (0–1) from a pre-sorted float64 slice using linear interpolation.
func computePercentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if len(sorted) == 1 {
		return sorted[0]
	}
	rank := p * float64(len(sorted)-1)
	lower := int(math.Floor(rank))
	upper := int(math.Ceil(rank))
	if lower == upper {
		return sorted[lower]
	}
	frac := rank - float64(lower)
	return sorted[lower]*(1-frac) + sorted[upper]*frac
}

// GetLatencyHistogram returns time-bucketed latency percentiles (avg, p90, p95, p99) for the given filters.
// PostgreSQL uses database-level percentile_cont aggregation (returns 1 row per bucket).
// MySQL and SQLite fall back to Go-based percentile computation (loads individual latency values).
func (s *RDBLogStore) GetLatencyHistogram(ctx context.Context, filters SearchFilters, bucketSizeSeconds int64) (*LatencyHistogramResult, error) {
	if bucketSizeSeconds <= 0 {
		bucketSizeSeconds = 3600
	}
	if s.db.Dialector.Name() == "postgres" && s.canUseMatView(filters) && bucketSizeSeconds >= 3600 {
		return s.getLatencyHistogramFromMatView(ctx, filters, bucketSizeSeconds)
	}

	dialect := s.db.Dialector.Name()

	baseQuery := s.db.WithContext(ctx).Model(&Log{})
	baseQuery = s.applyFilters(baseQuery, filters)
	baseQuery = baseQuery.Where("status IN ?", []string{"success", "error"})
	baseQuery = baseQuery.Where("latency IS NOT NULL")

	switch dialect {
	case "sqlite":
		return s.getLatencyHistogramSQLite(ctx, baseQuery, filters, bucketSizeSeconds)
	case "mysql":
		return s.getLatencyHistogramMySQL(ctx, baseQuery, filters, bucketSizeSeconds)
	default:
		return s.getLatencyHistogramPercentileCont(ctx, baseQuery, filters, bucketSizeSeconds)
	}
}

// getLatencyHistogramPercentileCont uses database-level percentile_cont for PostgreSQL.
// Returns 1 aggregated row per bucket instead of loading all individual latency values.
func (s *RDBLogStore) getLatencyHistogramPercentileCont(ctx context.Context, baseQuery *gorm.DB, filters SearchFilters, bucketSizeSeconds int64) (*LatencyHistogramResult, error) {
	var results []struct {
		BucketTimestamp int64           `gorm:"column:bucket_timestamp"`
		AvgLatency      sql.NullFloat64 `gorm:"column:avg_latency"`
		P90Latency      sql.NullFloat64 `gorm:"column:p90_latency"`
		P95Latency      sql.NullFloat64 `gorm:"column:p95_latency"`
		P99Latency      sql.NullFloat64 `gorm:"column:p99_latency"`
		TotalRequests   int64           `gorm:"column:total_requests"`
	}

	selectClause := fmt.Sprintf(`
		CAST(FLOOR(EXTRACT(EPOCH FROM timestamp) / %d) * %d AS BIGINT) as bucket_timestamp,
		AVG(latency) as avg_latency,
		percentile_cont(0.90) WITHIN GROUP (ORDER BY latency) as p90_latency,
		percentile_cont(0.95) WITHIN GROUP (ORDER BY latency) as p95_latency,
		percentile_cont(0.99) WITHIN GROUP (ORDER BY latency) as p99_latency,
		COUNT(*) as total_requests
	`, bucketSizeSeconds, bucketSizeSeconds)

	if err := baseQuery.
		Select(selectClause).
		Group("bucket_timestamp").
		Order("bucket_timestamp ASC").
		Find(&results).Error; err != nil {
		return nil, fmt.Errorf("failed to get latency histogram: %w", err)
	}

	computedBuckets := make(map[int64]LatencyHistogramBucket, len(results))
	var orderedKeys []int64
	for _, r := range results {
		orderedKeys = append(orderedKeys, r.BucketTimestamp)
		computedBuckets[r.BucketTimestamp] = LatencyHistogramBucket{
			Timestamp:     time.Unix(r.BucketTimestamp, 0).UTC(),
			AvgLatency:    r.AvgLatency.Float64,
			P90Latency:    r.P90Latency.Float64,
			P95Latency:    r.P95Latency.Float64,
			P99Latency:    r.P99Latency.Float64,
			TotalRequests: r.TotalRequests,
		}
	}

	return s.buildLatencyHistogramResult(computedBuckets, orderedKeys, filters, bucketSizeSeconds)
}

// getLatencyHistogramSQLite uses Go-based percentile computation for SQLite
// which lacks percentile_cont.
func (s *RDBLogStore) getLatencyHistogramSQLite(ctx context.Context, baseQuery *gorm.DB, filters SearchFilters, bucketSizeSeconds int64) (*LatencyHistogramResult, error) {
	var results []struct {
		BucketTimestamp int64   `gorm:"column:bucket_timestamp"`
		Latency         float64 `gorm:"column:latency"`
	}

	selectClause := fmt.Sprintf(
		`(CAST(strftime('%%s', timestamp) AS INTEGER) / %d) * %d as bucket_timestamp, latency`,
		bucketSizeSeconds, bucketSizeSeconds,
	)

	if err := baseQuery.
		Select(selectClause).
		Order("bucket_timestamp ASC, latency ASC").
		Find(&results).Error; err != nil {
		return nil, fmt.Errorf("failed to get latency histogram: %w", err)
	}

	type bucketData struct {
		latencies []float64
	}
	bucketMap := make(map[int64]*bucketData)
	var orderedKeys []int64

	for _, r := range results {
		bd, exists := bucketMap[r.BucketTimestamp]
		if !exists {
			bd = &bucketData{}
			bucketMap[r.BucketTimestamp] = bd
			orderedKeys = append(orderedKeys, r.BucketTimestamp)
		}
		bd.latencies = append(bd.latencies, r.Latency)
	}

	computedBuckets := make(map[int64]LatencyHistogramBucket, len(bucketMap))
	for ts, bd := range bucketMap {
		var sum float64
		for _, v := range bd.latencies {
			sum += v
		}
		computedBuckets[ts] = LatencyHistogramBucket{
			Timestamp:     time.Unix(ts, 0).UTC(),
			AvgLatency:    sum / float64(len(bd.latencies)),
			P90Latency:    computePercentile(bd.latencies, 0.90),
			P95Latency:    computePercentile(bd.latencies, 0.95),
			P99Latency:    computePercentile(bd.latencies, 0.99),
			TotalRequests: int64(len(bd.latencies)),
		}
	}

	return s.buildLatencyHistogramResult(computedBuckets, orderedKeys, filters, bucketSizeSeconds)
}

// getLatencyHistogramMySQL uses Go-based percentile computation for MySQL
// which lacks percentile_cont.
func (s *RDBLogStore) getLatencyHistogramMySQL(ctx context.Context, baseQuery *gorm.DB, filters SearchFilters, bucketSizeSeconds int64) (*LatencyHistogramResult, error) {
	var results []struct {
		BucketTimestamp int64   `gorm:"column:bucket_timestamp"`
		Latency         float64 `gorm:"column:latency"`
	}

	selectClause := fmt.Sprintf(
		`(FLOOR(UNIX_TIMESTAMP(timestamp) / %d) * %d) as bucket_timestamp, latency`,
		bucketSizeSeconds, bucketSizeSeconds,
	)

	if err := baseQuery.
		Select(selectClause).
		Order("bucket_timestamp ASC, latency ASC").
		Find(&results).Error; err != nil {
		return nil, fmt.Errorf("failed to get latency histogram: %w", err)
	}

	type bucketData struct {
		latencies []float64
	}
	bucketMap := make(map[int64]*bucketData)
	var orderedKeys []int64

	for _, r := range results {
		bd, exists := bucketMap[r.BucketTimestamp]
		if !exists {
			bd = &bucketData{}
			bucketMap[r.BucketTimestamp] = bd
			orderedKeys = append(orderedKeys, r.BucketTimestamp)
		}
		bd.latencies = append(bd.latencies, r.Latency)
	}

	computedBuckets := make(map[int64]LatencyHistogramBucket, len(bucketMap))
	for ts, bd := range bucketMap {
		var sum float64
		for _, v := range bd.latencies {
			sum += v
		}
		computedBuckets[ts] = LatencyHistogramBucket{
			Timestamp:     time.Unix(ts, 0).UTC(),
			AvgLatency:    sum / float64(len(bd.latencies)),
			P90Latency:    computePercentile(bd.latencies, 0.90),
			P95Latency:    computePercentile(bd.latencies, 0.95),
			P99Latency:    computePercentile(bd.latencies, 0.99),
			TotalRequests: int64(len(bd.latencies)),
		}
	}

	return s.buildLatencyHistogramResult(computedBuckets, orderedKeys, filters, bucketSizeSeconds)
}

// buildLatencyHistogramResult fills in bucket timestamps for the time range and returns the result.
func (s *RDBLogStore) buildLatencyHistogramResult(computedBuckets map[int64]LatencyHistogramBucket, orderedKeys []int64, filters SearchFilters, bucketSizeSeconds int64) (*LatencyHistogramResult, error) {
	allTimestamps := generateBucketTimestamps(filters.StartTime, filters.EndTime, bucketSizeSeconds)

	if len(allTimestamps) == 0 {
		buckets := make([]LatencyHistogramBucket, 0, len(computedBuckets))
		for _, ts := range orderedKeys {
			buckets = append(buckets, computedBuckets[ts])
		}
		return &LatencyHistogramResult{
			Buckets:           buckets,
			BucketSizeSeconds: bucketSizeSeconds,
		}, nil
	}

	buckets := make([]LatencyHistogramBucket, len(allTimestamps))
	for i, ts := range allTimestamps {
		if bucket, exists := computedBuckets[ts]; exists {
			buckets[i] = bucket
		} else {
			buckets[i] = LatencyHistogramBucket{
				Timestamp: time.Unix(ts, 0).UTC(),
			}
		}
	}

	return &LatencyHistogramResult{
		Buckets:           buckets,
		BucketSizeSeconds: bucketSizeSeconds,
	}, nil
}

// GetModelRankings returns models ranked by usage with trend comparison to the previous period.
func (s *RDBLogStore) GetModelRankings(ctx context.Context, filters SearchFilters) (*ModelRankingResult, error) {
	if s.db.Dialector.Name() == "postgres" && s.canUseMatView(filters) {
		return s.getModelRankingsFromMatView(ctx, filters)
	}
	selectClause := `
		model,
		provider,
		COUNT(*) as total_requests,
		SUM(CASE WHEN status = 'success' THEN 1 ELSE 0 END) as success_count,
		SUM(total_tokens) as total_tokens,
		COALESCE(SUM(cost), 0) as total_cost,
		AVG(latency) as avg_latency
	`

	// Query current period
	currentQuery := s.db.WithContext(ctx).Model(&Log{})
	currentQuery = s.applyFilters(currentQuery, filters)
	currentQuery = currentQuery.Where("status IN ?", []string{"success", "error"})
	currentQuery = currentQuery.Where("model IS NOT NULL AND model != ''")

	var currentResults []struct {
		Model         string          `gorm:"column:model"`
		Provider      string          `gorm:"column:provider"`
		TotalRequests int64           `gorm:"column:total_requests"`
		SuccessCount  int64           `gorm:"column:success_count"`
		TotalTokens   sql.NullInt64   `gorm:"column:total_tokens"`
		TotalCost     sql.NullFloat64 `gorm:"column:total_cost"`
		AvgLatency    sql.NullFloat64 `gorm:"column:avg_latency"`
	}

	if err := currentQuery.
		Select(selectClause).
		Group("model, provider").
		Order("total_requests DESC").
		Limit(defaultMaxRankingsLimit).
		Find(&currentResults).Error; err != nil {
		return nil, fmt.Errorf("failed to get model rankings: %w", err)
	}

	// Query previous period for trend comparison
	type modelProviderKey struct {
		provider string
		model    string
	}
	prevMap := make(map[modelProviderKey]ModelRankingEntry)
	if filters.StartTime != nil && filters.EndTime != nil {
		duration := filters.EndTime.Sub(*filters.StartTime)
		prevStart := filters.StartTime.Add(-duration)
		prevEnd := filters.StartTime.Add(-time.Nanosecond)

		prevFilters := filters
		prevFilters.StartTime = &prevStart
		prevFilters.EndTime = &prevEnd

		prevQuery := s.db.WithContext(ctx).Model(&Log{})
		prevQuery = s.applyFilters(prevQuery, prevFilters)
		prevQuery = prevQuery.Where("status IN ?", []string{"success", "error"})
		prevQuery = prevQuery.Where("model IS NOT NULL AND model != ''")

		// Only fetch previous-period data for (model, provider) pairs that
		// appear in the current ranking so trend computation is accurate even
		// when the previous period has more groups than the limit.
		if len(currentResults) > 0 {
			pairConditions := make([]string, len(currentResults))
			pairArgs := make([]interface{}, 0, len(currentResults)*2)
			for i, r := range currentResults {
				pairConditions[i] = "(model = ? AND provider = ?)"
				pairArgs = append(pairArgs, r.Model, r.Provider)
			}
			prevQuery = prevQuery.Where(strings.Join(pairConditions, " OR "), pairArgs...)
		}

		var prevResults []struct {
			Model         string          `gorm:"column:model"`
			Provider      string          `gorm:"column:provider"`
			TotalRequests int64           `gorm:"column:total_requests"`
			SuccessCount  int64           `gorm:"column:success_count"`
			TotalTokens   sql.NullInt64   `gorm:"column:total_tokens"`
			TotalCost     sql.NullFloat64 `gorm:"column:total_cost"`
			AvgLatency    sql.NullFloat64 `gorm:"column:avg_latency"`
		}

		if err := prevQuery.
			Select(selectClause).
			Group("model, provider").
			Find(&prevResults).Error; err != nil {
			return nil, fmt.Errorf("failed to get previous period model rankings: %w", err)
		}

		for _, r := range prevResults {
			key := modelProviderKey{provider: r.Provider, model: r.Model}
			prevMap[key] = ModelRankingEntry{
				Model:         r.Model,
				Provider:      r.Provider,
				TotalRequests: r.TotalRequests,
				TotalTokens:   r.TotalTokens.Int64,
				TotalCost:     r.TotalCost.Float64,
				AvgLatency:    r.AvgLatency.Float64,
			}
		}
	}

	// Build results with trends
	rankings := make([]ModelRankingWithTrend, len(currentResults))
	for i, r := range currentResults {
		entry := ModelRankingEntry{
			Model:         r.Model,
			Provider:      r.Provider,
			TotalRequests: r.TotalRequests,
			SuccessCount:  r.SuccessCount,
			TotalTokens:   r.TotalTokens.Int64,
			TotalCost:     r.TotalCost.Float64,
			AvgLatency:    r.AvgLatency.Float64,
		}
		if r.TotalRequests > 0 {
			entry.SuccessRate = float64(r.SuccessCount) / float64(r.TotalRequests) * 100
		}

		var trend ModelRankingTrend
		key := modelProviderKey{provider: r.Provider, model: r.Model}
		if prev, ok := prevMap[key]; ok && prev.TotalRequests > 0 {
			trend.HasPreviousPeriod = true
			trend.RequestsTrend = pctChange(float64(prev.TotalRequests), float64(r.TotalRequests))
			trend.TokensTrend = pctChange(float64(prev.TotalTokens), float64(r.TotalTokens.Int64))
			trend.CostTrend = pctChange(prev.TotalCost, r.TotalCost.Float64)
			if prev.AvgLatency > 0 {
				trend.LatencyTrend = pctChange(prev.AvgLatency, r.AvgLatency.Float64)
			}
		}

		rankings[i] = ModelRankingWithTrend{
			ModelRankingEntry: entry,
			Trend:             trend,
		}
	}

	return &ModelRankingResult{Rankings: rankings}, nil
}

// GetUserRankings returns users ranked by usage with trend comparison to the previous period.
func (s *RDBLogStore) GetUserRankings(ctx context.Context, filters SearchFilters) (*UserRankingResult, error) {
	if s.db.Dialector.Name() == "postgres" && s.canUseMatView(filters) {
		return s.getUserRankingsFromMatView(ctx, filters)
	}
	selectClause := `
		user_id,
		COUNT(*) as total_requests,
		SUM(total_tokens) as total_tokens,
		COALESCE(SUM(cost), 0) as total_cost
	`

	// Query current period
	currentQuery := s.db.WithContext(ctx).Model(&Log{})
	currentQuery = s.applyFilters(currentQuery, filters)
	currentQuery = currentQuery.Where("status IN ?", []string{"success", "error"})
	currentQuery = currentQuery.Where("user_id IS NOT NULL AND user_id != ''")

	var currentResults []struct {
		UserID        string          `gorm:"column:user_id"`
		TotalRequests int64           `gorm:"column:total_requests"`
		TotalTokens   sql.NullInt64   `gorm:"column:total_tokens"`
		TotalCost     sql.NullFloat64 `gorm:"column:total_cost"`
	}

	if err := currentQuery.
		Select(selectClause).
		Group("user_id").
		Order("total_requests DESC").
		Limit(defaultMaxRankingsLimit).
		Find(&currentResults).Error; err != nil {
		return nil, fmt.Errorf("failed to get user rankings: %w", err)
	}

	// Query previous period for trend comparison
	prevMap := make(map[string]UserRankingEntry)
	if filters.StartTime != nil && filters.EndTime != nil {
		duration := filters.EndTime.Sub(*filters.StartTime)
		prevStart := filters.StartTime.Add(-duration)
		prevEnd := filters.StartTime.Add(-time.Nanosecond)

		prevFilters := filters
		prevFilters.StartTime = &prevStart
		prevFilters.EndTime = &prevEnd

		prevQuery := s.db.WithContext(ctx).Model(&Log{})
		prevQuery = s.applyFilters(prevQuery, prevFilters)
		prevQuery = prevQuery.Where("status IN ?", []string{"success", "error"})
		prevQuery = prevQuery.Where("user_id IS NOT NULL AND user_id != ''")

		if len(currentResults) > 0 {
			userIDs := make([]string, len(currentResults))
			for i, r := range currentResults {
				userIDs[i] = r.UserID
			}
			prevQuery = prevQuery.Where("user_id IN ?", userIDs)
		}

		var prevResults []struct {
			UserID        string          `gorm:"column:user_id"`
			TotalRequests int64           `gorm:"column:total_requests"`
			TotalTokens   sql.NullInt64   `gorm:"column:total_tokens"`
			TotalCost     sql.NullFloat64 `gorm:"column:total_cost"`
		}

		if err := prevQuery.
			Select(selectClause).
			Group("user_id").
			Find(&prevResults).Error; err != nil {
			return nil, fmt.Errorf("failed to get previous period user rankings: %w", err)
		}

		for _, r := range prevResults {
			prevMap[r.UserID] = UserRankingEntry{
				UserID:        r.UserID,
				TotalRequests: r.TotalRequests,
				TotalTokens:   r.TotalTokens.Int64,
				TotalCost:     r.TotalCost.Float64,
			}
		}
	}

	// Build results with trends
	rankings := make([]UserRankingWithTrend, len(currentResults))
	for i, r := range currentResults {
		entry := UserRankingEntry{
			UserID:        r.UserID,
			TotalRequests: r.TotalRequests,
			TotalTokens:   r.TotalTokens.Int64,
			TotalCost:     r.TotalCost.Float64,
		}

		var trend UserRankingTrend
		if prev, ok := prevMap[r.UserID]; ok && prev.TotalRequests > 0 {
			trend.HasPreviousPeriod = true
			trend.RequestsTrend = pctChange(float64(prev.TotalRequests), float64(r.TotalRequests))
			trend.TokensTrend = pctChange(float64(prev.TotalTokens), float64(r.TotalTokens.Int64))
			trend.CostTrend = pctChange(prev.TotalCost, r.TotalCost.Float64)
		}

		rankings[i] = UserRankingWithTrend{
			UserRankingEntry: entry,
			Trend:            trend,
		}
	}

	return &UserRankingResult{Rankings: rankings}, nil
}

// pctChange computes the percentage change from old to new.
func pctChange(old, new float64) float64 {
	if old == 0 {
		return 0
	}
	return (new - old) / old * 100
}

// GetProviderCostHistogram returns time-bucketed cost data with provider breakdown for the given filters.
func (s *RDBLogStore) GetProviderCostHistogram(ctx context.Context, filters SearchFilters, bucketSizeSeconds int64) (*ProviderCostHistogramResult, error) {
	if bucketSizeSeconds <= 0 {
		bucketSizeSeconds = 3600
	}
	if s.db.Dialector.Name() == "postgres" && s.canUseMatView(filters) && bucketSizeSeconds >= 3600 {
		return s.getProviderCostHistogramFromMatView(ctx, filters, bucketSizeSeconds)
	}

	dialect := s.db.Dialector.Name()

	baseQuery := s.db.WithContext(ctx).Model(&Log{})
	baseQuery = s.applyFilters(baseQuery, filters)
	baseQuery = baseQuery.Where("status IN ?", []string{"success", "error"})
	baseQuery = baseQuery.Where("cost IS NOT NULL AND cost > 0")

	var results []struct {
		BucketTimestamp int64   `gorm:"column:bucket_timestamp"`
		Provider        string  `gorm:"column:provider"`
		TotalCost       float64 `gorm:"column:total_cost"`
	}

	var selectClause string
	switch dialect {
	case "sqlite":
		selectClause = fmt.Sprintf(`
			(CAST(strftime('%%s', timestamp) AS INTEGER) / %d) * %d as bucket_timestamp,
			provider,
			COALESCE(SUM(cost), 0) as total_cost
		`, bucketSizeSeconds, bucketSizeSeconds)
	case "mysql":
		selectClause = fmt.Sprintf(`
			(FLOOR(UNIX_TIMESTAMP(timestamp) / %d) * %d) as bucket_timestamp,
			provider,
			COALESCE(SUM(cost), 0) as total_cost
		`, bucketSizeSeconds, bucketSizeSeconds)
	default:
		selectClause = fmt.Sprintf(`
			CAST(FLOOR(EXTRACT(EPOCH FROM timestamp) / %d) * %d AS BIGINT) as bucket_timestamp,
			provider,
			COALESCE(SUM(cost), 0) as total_cost
		`, bucketSizeSeconds, bucketSizeSeconds)
	}

	if err := baseQuery.
		Select(selectClause).
		Group("bucket_timestamp, provider").
		Order("bucket_timestamp ASC").
		Find(&results).Error; err != nil {
		return nil, fmt.Errorf("failed to get provider cost histogram: %w", err)
	}

	bucketMap := make(map[int64]*ProviderCostHistogramBucket)
	providersSet := make(map[string]bool)

	for _, r := range results {
		providersSet[r.Provider] = true
		if bucket, exists := bucketMap[r.BucketTimestamp]; exists {
			bucket.TotalCost += r.TotalCost
			bucket.ByProvider[r.Provider] = r.TotalCost
		} else {
			bucketMap[r.BucketTimestamp] = &ProviderCostHistogramBucket{
				Timestamp:  time.Unix(r.BucketTimestamp, 0).UTC(),
				TotalCost:  r.TotalCost,
				ByProvider: map[string]float64{r.Provider: r.TotalCost},
			}
		}
	}

	providers := make([]string, 0, len(providersSet))
	for provider := range providersSet {
		providers = append(providers, provider)
	}

	allTimestamps := generateBucketTimestamps(filters.StartTime, filters.EndTime, bucketSizeSeconds)

	if len(allTimestamps) == 0 {
		buckets := make([]ProviderCostHistogramBucket, 0, len(bucketMap))
		for _, bucket := range bucketMap {
			buckets = append(buckets, *bucket)
		}
		sort.Slice(buckets, func(i, j int) bool {
			return buckets[i].Timestamp.Before(buckets[j].Timestamp)
		})
		return &ProviderCostHistogramResult{
			Buckets:           buckets,
			BucketSizeSeconds: bucketSizeSeconds,
			Providers:         providers,
		}, nil
	}

	buckets := make([]ProviderCostHistogramBucket, len(allTimestamps))
	for i, ts := range allTimestamps {
		if bucket, exists := bucketMap[ts]; exists {
			buckets[i] = *bucket
		} else {
			buckets[i] = ProviderCostHistogramBucket{
				Timestamp:  time.Unix(ts, 0).UTC(),
				TotalCost:  0,
				ByProvider: make(map[string]float64),
			}
		}
	}

	return &ProviderCostHistogramResult{
		Buckets:           buckets,
		BucketSizeSeconds: bucketSizeSeconds,
		Providers:         providers,
	}, nil
}

// GetProviderTokenHistogram returns time-bucketed token usage with provider breakdown for the given filters.
func (s *RDBLogStore) GetProviderTokenHistogram(ctx context.Context, filters SearchFilters, bucketSizeSeconds int64) (*ProviderTokenHistogramResult, error) {
	if bucketSizeSeconds <= 0 {
		bucketSizeSeconds = 3600
	}
	if s.db.Dialector.Name() == "postgres" && s.canUseMatView(filters) && bucketSizeSeconds >= 3600 {
		return s.getProviderTokenHistogramFromMatView(ctx, filters, bucketSizeSeconds)
	}

	dialect := s.db.Dialector.Name()

	baseQuery := s.db.WithContext(ctx).Model(&Log{})
	baseQuery = s.applyFilters(baseQuery, filters)
	baseQuery = baseQuery.Where("status IN ?", []string{"success", "error"})

	var results []struct {
		BucketTimestamp  int64  `gorm:"column:bucket_timestamp"`
		Provider         string `gorm:"column:provider"`
		PromptTokens     int64  `gorm:"column:prompt_tokens"`
		CompletionTokens int64  `gorm:"column:completion_tokens"`
		TotalTokens      int64  `gorm:"column:total_tokens"`
	}

	var selectClause string
	switch dialect {
	case "sqlite":
		selectClause = fmt.Sprintf(`
			(CAST(strftime('%%s', timestamp) AS INTEGER) / %d) * %d as bucket_timestamp,
			provider,
			COALESCE(SUM(prompt_tokens), 0) as prompt_tokens,
			COALESCE(SUM(completion_tokens), 0) as completion_tokens,
			COALESCE(SUM(total_tokens), 0) as total_tokens
		`, bucketSizeSeconds, bucketSizeSeconds)
	case "mysql":
		selectClause = fmt.Sprintf(`
			(FLOOR(UNIX_TIMESTAMP(timestamp) / %d) * %d) as bucket_timestamp,
			provider,
			COALESCE(SUM(prompt_tokens), 0) as prompt_tokens,
			COALESCE(SUM(completion_tokens), 0) as completion_tokens,
			COALESCE(SUM(total_tokens), 0) as total_tokens
		`, bucketSizeSeconds, bucketSizeSeconds)
	default:
		selectClause = fmt.Sprintf(`
			CAST(FLOOR(EXTRACT(EPOCH FROM timestamp) / %d) * %d AS BIGINT) as bucket_timestamp,
			provider,
			COALESCE(SUM(prompt_tokens), 0) as prompt_tokens,
			COALESCE(SUM(completion_tokens), 0) as completion_tokens,
			COALESCE(SUM(total_tokens), 0) as total_tokens
		`, bucketSizeSeconds, bucketSizeSeconds)
	}

	if err := baseQuery.
		Select(selectClause).
		Group("bucket_timestamp, provider").
		Order("bucket_timestamp ASC").
		Find(&results).Error; err != nil {
		return nil, fmt.Errorf("failed to get provider token histogram: %w", err)
	}

	bucketMap := make(map[int64]*ProviderTokenHistogramBucket)
	providersSet := make(map[string]bool)

	for _, r := range results {
		providersSet[r.Provider] = true
		if bucket, exists := bucketMap[r.BucketTimestamp]; exists {
			bucket.ByProvider[r.Provider] = ProviderTokenStats{
				PromptTokens:     r.PromptTokens,
				CompletionTokens: r.CompletionTokens,
				TotalTokens:      r.TotalTokens,
			}
		} else {
			bucketMap[r.BucketTimestamp] = &ProviderTokenHistogramBucket{
				Timestamp: time.Unix(r.BucketTimestamp, 0).UTC(),
				ByProvider: map[string]ProviderTokenStats{
					r.Provider: {
						PromptTokens:     r.PromptTokens,
						CompletionTokens: r.CompletionTokens,
						TotalTokens:      r.TotalTokens,
					},
				},
			}
		}
	}

	providers := make([]string, 0, len(providersSet))
	for provider := range providersSet {
		providers = append(providers, provider)
	}

	allTimestamps := generateBucketTimestamps(filters.StartTime, filters.EndTime, bucketSizeSeconds)

	if len(allTimestamps) == 0 {
		buckets := make([]ProviderTokenHistogramBucket, 0, len(bucketMap))
		for _, bucket := range bucketMap {
			buckets = append(buckets, *bucket)
		}
		sort.Slice(buckets, func(i, j int) bool {
			return buckets[i].Timestamp.Before(buckets[j].Timestamp)
		})
		return &ProviderTokenHistogramResult{
			Buckets:           buckets,
			BucketSizeSeconds: bucketSizeSeconds,
			Providers:         providers,
		}, nil
	}

	buckets := make([]ProviderTokenHistogramBucket, len(allTimestamps))
	for i, ts := range allTimestamps {
		if bucket, exists := bucketMap[ts]; exists {
			buckets[i] = *bucket
		} else {
			buckets[i] = ProviderTokenHistogramBucket{
				Timestamp:  time.Unix(ts, 0).UTC(),
				ByProvider: make(map[string]ProviderTokenStats),
			}
		}
	}

	return &ProviderTokenHistogramResult{
		Buckets:           buckets,
		BucketSizeSeconds: bucketSizeSeconds,
		Providers:         providers,
	}, nil
}

// GetProviderLatencyHistogram returns time-bucketed latency percentiles with provider breakdown for the given filters.
// PostgreSQL uses database-level percentile_cont aggregation.
// MySQL and SQLite fall back to Go-based percentile computation.
func (s *RDBLogStore) GetProviderLatencyHistogram(ctx context.Context, filters SearchFilters, bucketSizeSeconds int64) (*ProviderLatencyHistogramResult, error) {
	if bucketSizeSeconds <= 0 {
		bucketSizeSeconds = 3600
	}
	if s.db.Dialector.Name() == "postgres" && s.canUseMatView(filters) && bucketSizeSeconds >= 3600 {
		return s.getProviderLatencyHistogramFromMatView(ctx, filters, bucketSizeSeconds)
	}

	dialect := s.db.Dialector.Name()

	baseQuery := s.db.WithContext(ctx).Model(&Log{})
	baseQuery = s.applyFilters(baseQuery, filters)
	baseQuery = baseQuery.Where("status IN ?", []string{"success", "error"})
	baseQuery = baseQuery.Where("latency IS NOT NULL")

	switch dialect {
	case "sqlite":
		return s.getProviderLatencyHistogramSQLite(ctx, baseQuery, filters, bucketSizeSeconds)
	case "mysql":
		return s.getProviderLatencyHistogramMySQL(ctx, baseQuery, filters, bucketSizeSeconds)
	default:
		return s.getProviderLatencyHistogramPercentileCont(ctx, baseQuery, filters, bucketSizeSeconds)
	}
}

// getProviderLatencyHistogramPercentileCont uses database-level percentile_cont for PostgreSQL.
// Returns 1 aggregated row per (bucket, provider) instead of loading all individual latency values.
func (s *RDBLogStore) getProviderLatencyHistogramPercentileCont(ctx context.Context, baseQuery *gorm.DB, filters SearchFilters, bucketSizeSeconds int64) (*ProviderLatencyHistogramResult, error) {
	var results []struct {
		BucketTimestamp int64           `gorm:"column:bucket_timestamp"`
		Provider        string          `gorm:"column:provider"`
		AvgLatency      sql.NullFloat64 `gorm:"column:avg_latency"`
		P90Latency      sql.NullFloat64 `gorm:"column:p90_latency"`
		P95Latency      sql.NullFloat64 `gorm:"column:p95_latency"`
		P99Latency      sql.NullFloat64 `gorm:"column:p99_latency"`
		TotalRequests   int64           `gorm:"column:total_requests"`
	}

	selectClause := fmt.Sprintf(`
		CAST(FLOOR(EXTRACT(EPOCH FROM timestamp) / %d) * %d AS BIGINT) as bucket_timestamp,
		provider,
		AVG(latency) as avg_latency,
		percentile_cont(0.90) WITHIN GROUP (ORDER BY latency) as p90_latency,
		percentile_cont(0.95) WITHIN GROUP (ORDER BY latency) as p95_latency,
		percentile_cont(0.99) WITHIN GROUP (ORDER BY latency) as p99_latency,
		COUNT(*) as total_requests
	`, bucketSizeSeconds, bucketSizeSeconds)

	if err := baseQuery.
		Select(selectClause).
		Group("bucket_timestamp, provider").
		Order("bucket_timestamp ASC, provider ASC").
		Find(&results).Error; err != nil {
		return nil, fmt.Errorf("failed to get provider latency histogram: %w", err)
	}

	providersSet := make(map[string]bool)
	computedBuckets := make(map[int64]*ProviderLatencyHistogramBucket)
	var orderedBuckets []int64
	seenBuckets := make(map[int64]bool)

	for _, r := range results {
		providersSet[r.Provider] = true
		if !seenBuckets[r.BucketTimestamp] {
			seenBuckets[r.BucketTimestamp] = true
			orderedBuckets = append(orderedBuckets, r.BucketTimestamp)
		}
		stats := ProviderLatencyStats{
			AvgLatency:    r.AvgLatency.Float64,
			P90Latency:    r.P90Latency.Float64,
			P95Latency:    r.P95Latency.Float64,
			P99Latency:    r.P99Latency.Float64,
			TotalRequests: r.TotalRequests,
		}
		if bucket, exists := computedBuckets[r.BucketTimestamp]; exists {
			bucket.ByProvider[r.Provider] = stats
		} else {
			computedBuckets[r.BucketTimestamp] = &ProviderLatencyHistogramBucket{
				Timestamp:  time.Unix(r.BucketTimestamp, 0).UTC(),
				ByProvider: map[string]ProviderLatencyStats{r.Provider: stats},
			}
		}
	}

	providers := make([]string, 0, len(providersSet))
	for provider := range providersSet {
		providers = append(providers, provider)
	}

	return s.buildProviderLatencyHistogramResult(computedBuckets, orderedBuckets, providers, filters, bucketSizeSeconds)
}

// getProviderLatencyHistogramSQLite uses Go-based percentile computation for SQLite
// which lacks percentile_cont.
func (s *RDBLogStore) getProviderLatencyHistogramSQLite(ctx context.Context, baseQuery *gorm.DB, filters SearchFilters, bucketSizeSeconds int64) (*ProviderLatencyHistogramResult, error) {
	var results []struct {
		BucketTimestamp int64   `gorm:"column:bucket_timestamp"`
		Provider        string  `gorm:"column:provider"`
		Latency         float64 `gorm:"column:latency"`
	}

	selectClause := fmt.Sprintf(
		`(CAST(strftime('%%s', timestamp) AS INTEGER) / %d) * %d as bucket_timestamp, provider, latency`,
		bucketSizeSeconds, bucketSizeSeconds,
	)

	if err := baseQuery.
		Select(selectClause).
		Order("bucket_timestamp ASC, provider ASC, latency ASC").
		Find(&results).Error; err != nil {
		return nil, fmt.Errorf("failed to get provider latency histogram: %w", err)
	}

	type providerBucketKey struct {
		BucketTimestamp int64
		Provider        string
	}
	latencyMap := make(map[providerBucketKey][]float64)
	providersSet := make(map[string]bool)
	var orderedBuckets []int64
	seenBuckets := make(map[int64]bool)

	for _, r := range results {
		providersSet[r.Provider] = true
		key := providerBucketKey{BucketTimestamp: r.BucketTimestamp, Provider: r.Provider}
		latencyMap[key] = append(latencyMap[key], r.Latency)
		if !seenBuckets[r.BucketTimestamp] {
			seenBuckets[r.BucketTimestamp] = true
			orderedBuckets = append(orderedBuckets, r.BucketTimestamp)
		}
	}

	providers := make([]string, 0, len(providersSet))
	for provider := range providersSet {
		providers = append(providers, provider)
	}

	computedBuckets := make(map[int64]*ProviderLatencyHistogramBucket)
	for key, latencies := range latencyMap {
		var sum float64
		for _, v := range latencies {
			sum += v
		}
		stats := ProviderLatencyStats{
			AvgLatency:    sum / float64(len(latencies)),
			P90Latency:    computePercentile(latencies, 0.90),
			P95Latency:    computePercentile(latencies, 0.95),
			P99Latency:    computePercentile(latencies, 0.99),
			TotalRequests: int64(len(latencies)),
		}
		if bucket, exists := computedBuckets[key.BucketTimestamp]; exists {
			bucket.ByProvider[key.Provider] = stats
		} else {
			computedBuckets[key.BucketTimestamp] = &ProviderLatencyHistogramBucket{
				Timestamp:  time.Unix(key.BucketTimestamp, 0).UTC(),
				ByProvider: map[string]ProviderLatencyStats{key.Provider: stats},
			}
		}
	}

	return s.buildProviderLatencyHistogramResult(computedBuckets, orderedBuckets, providers, filters, bucketSizeSeconds)
}

// getProviderLatencyHistogramMySQL uses Go-based percentile computation for MySQL
// which lacks percentile_cont.
func (s *RDBLogStore) getProviderLatencyHistogramMySQL(ctx context.Context, baseQuery *gorm.DB, filters SearchFilters, bucketSizeSeconds int64) (*ProviderLatencyHistogramResult, error) {
	var results []struct {
		BucketTimestamp int64   `gorm:"column:bucket_timestamp"`
		Provider        string  `gorm:"column:provider"`
		Latency         float64 `gorm:"column:latency"`
	}

	selectClause := fmt.Sprintf(
		`(FLOOR(UNIX_TIMESTAMP(timestamp) / %d) * %d) as bucket_timestamp, provider, latency`,
		bucketSizeSeconds, bucketSizeSeconds,
	)

	if err := baseQuery.
		Select(selectClause).
		Order("bucket_timestamp ASC, provider ASC, latency ASC").
		Find(&results).Error; err != nil {
		return nil, fmt.Errorf("failed to get provider latency histogram: %w", err)
	}

	type bucketProviderKey struct {
		BucketTimestamp int64
		Provider        string
	}
	latencyMap := make(map[bucketProviderKey][]float64)
	providersSet := make(map[string]bool)
	var orderedBuckets []int64
	seenBuckets := make(map[int64]bool)

	for _, r := range results {
		key := bucketProviderKey{r.BucketTimestamp, r.Provider}
		latencyMap[key] = append(latencyMap[key], r.Latency)
		providersSet[r.Provider] = true
		if !seenBuckets[r.BucketTimestamp] {
			seenBuckets[r.BucketTimestamp] = true
			orderedBuckets = append(orderedBuckets, r.BucketTimestamp)
		}
	}

	providers := make([]string, 0, len(providersSet))
	for provider := range providersSet {
		providers = append(providers, provider)
	}

	computedBuckets := make(map[int64]*ProviderLatencyHistogramBucket)
	for key, latencies := range latencyMap {
		var sum float64
		for _, v := range latencies {
			sum += v
		}
		stats := ProviderLatencyStats{
			AvgLatency:    sum / float64(len(latencies)),
			P90Latency:    computePercentile(latencies, 0.90),
			P95Latency:    computePercentile(latencies, 0.95),
			P99Latency:    computePercentile(latencies, 0.99),
			TotalRequests: int64(len(latencies)),
		}
		if bucket, exists := computedBuckets[key.BucketTimestamp]; exists {
			bucket.ByProvider[key.Provider] = stats
		} else {
			computedBuckets[key.BucketTimestamp] = &ProviderLatencyHistogramBucket{
				Timestamp:  time.Unix(key.BucketTimestamp, 0).UTC(),
				ByProvider: map[string]ProviderLatencyStats{key.Provider: stats},
			}
		}
	}

	return s.buildProviderLatencyHistogramResult(computedBuckets, orderedBuckets, providers, filters, bucketSizeSeconds)
}

// buildProviderLatencyHistogramResult fills in bucket timestamps and returns the result.
func (s *RDBLogStore) buildProviderLatencyHistogramResult(computedBuckets map[int64]*ProviderLatencyHistogramBucket, orderedBuckets []int64, providers []string, filters SearchFilters, bucketSizeSeconds int64) (*ProviderLatencyHistogramResult, error) {
	allTimestamps := generateBucketTimestamps(filters.StartTime, filters.EndTime, bucketSizeSeconds)

	if len(allTimestamps) == 0 {
		buckets := make([]ProviderLatencyHistogramBucket, 0, len(computedBuckets))
		for _, ts := range orderedBuckets {
			if bucket, exists := computedBuckets[ts]; exists {
				buckets = append(buckets, *bucket)
			}
		}
		return &ProviderLatencyHistogramResult{
			Buckets:           buckets,
			BucketSizeSeconds: bucketSizeSeconds,
			Providers:         providers,
		}, nil
	}

	buckets := make([]ProviderLatencyHistogramBucket, len(allTimestamps))
	for i, ts := range allTimestamps {
		if bucket, exists := computedBuckets[ts]; exists {
			buckets[i] = *bucket
		} else {
			buckets[i] = ProviderLatencyHistogramBucket{
				Timestamp:  time.Unix(ts, 0).UTC(),
				ByProvider: make(map[string]ProviderLatencyStats),
			}
		}
	}

	return &ProviderLatencyHistogramResult{
		Buckets:           buckets,
		BucketSizeSeconds: bucketSizeSeconds,
		Providers:         providers,
	}, nil
}

// ---------------------------------------------------------------------------
// Generic dimension histogram methods
// ---------------------------------------------------------------------------

// GetDimensionCostHistogram returns time-bucketed cost data grouped by the specified dimension.
// Uses the mv_logs_hourly materialized view on PostgreSQL when eligible; falls back to raw queries otherwise.
func (s *RDBLogStore) GetDimensionCostHistogram(ctx context.Context, filters SearchFilters, bucketSizeSeconds int64, dimension HistogramDimension) (*DimensionCostHistogramResult, error) {
	if !ValidHistogramDimensions[dimension] {
		return nil, fmt.Errorf("invalid histogram dimension: %s", dimension)
	}
	if bucketSizeSeconds <= 0 {
		bucketSizeSeconds = 3600
	}
	if s.db.Dialector.Name() == "postgres" && s.canUseMatView(filters) && bucketSizeSeconds >= 3600 {
		return s.getDimensionCostHistogramFromMatView(ctx, filters, bucketSizeSeconds, dimension)
	}
	dimCol := string(dimension)
	dialect := s.db.Dialector.Name()
	baseQuery := s.db.WithContext(ctx).Model(&Log{})
	baseQuery = s.applyFilters(baseQuery, filters)
	baseQuery = baseQuery.Where("status IN ?", []string{"success", "error"})
	baseQuery = baseQuery.Where("cost IS NOT NULL AND cost > 0")

	var bucketExpr string
	switch dialect {
	case "sqlite":
		bucketExpr = fmt.Sprintf("CAST((CAST(strftime('%%s', timestamp) AS INTEGER) / %d) * %d AS INTEGER)", bucketSizeSeconds, bucketSizeSeconds)
	default:
		bucketExpr = fmt.Sprintf("CAST(FLOOR(EXTRACT(EPOCH FROM timestamp) / %d) * %d AS BIGINT)", bucketSizeSeconds, bucketSizeSeconds)
	}

	var results []struct {
		BucketTimestamp int64   `gorm:"column:bucket_timestamp"`
		DimValue        string  `gorm:"column:dim_value"`
		Cost            float64 `gorm:"column:cost"`
	}
	if err := baseQuery.Select(fmt.Sprintf(`
		%s AS bucket_timestamp,
		COALESCE(%s, '') AS dim_value,
		SUM(cost) AS cost
	`, bucketExpr, dimCol)).
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

	dimValues := sortedStringKeys(dimSet)
	allTimestamps := generateBucketTimestamps(filters.StartTime, filters.EndTime, bucketSizeSeconds)

	// If no time range specified, build buckets directly from query results
	if len(allTimestamps) == 0 {
		keys := make([]int64, 0, len(grouped))
		for ts := range grouped {
			keys = append(keys, ts)
		}
		sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
		buckets := make([]DimensionCostHistogramBucket, 0, len(keys))
		for _, ts := range keys {
			a := grouped[ts]
			buckets = append(buckets, DimensionCostHistogramBucket{
				Timestamp:   time.Unix(ts, 0).UTC(),
				TotalCost:   a.totalCost,
				ByDimension: a.byDimension,
			})
		}
		return &DimensionCostHistogramResult{Buckets: buckets, BucketSizeSeconds: bucketSizeSeconds, Dimension: dimension, DimensionValues: dimValues}, nil
	}

	buckets := make([]DimensionCostHistogramBucket, 0, len(allTimestamps))
	for _, ts := range allTimestamps {
		b := DimensionCostHistogramBucket{Timestamp: time.Unix(ts, 0).UTC(), ByDimension: make(map[string]float64)}
		if a, ok := grouped[ts]; ok {
			b.TotalCost = a.totalCost
			b.ByDimension = a.byDimension
		}
		buckets = append(buckets, b)
	}

	return &DimensionCostHistogramResult{Buckets: buckets, BucketSizeSeconds: bucketSizeSeconds, Dimension: dimension, DimensionValues: dimValues}, nil
}

// GetDimensionTokenHistogram returns time-bucketed token usage grouped by the specified dimension.
// Uses the mv_logs_hourly materialized view on PostgreSQL when eligible; falls back to raw queries otherwise.
func (s *RDBLogStore) GetDimensionTokenHistogram(ctx context.Context, filters SearchFilters, bucketSizeSeconds int64, dimension HistogramDimension) (*DimensionTokenHistogramResult, error) {
	if !ValidHistogramDimensions[dimension] {
		return nil, fmt.Errorf("invalid histogram dimension: %s", dimension)
	}
	if bucketSizeSeconds <= 0 {
		bucketSizeSeconds = 3600
	}
	if s.db.Dialector.Name() == "postgres" && s.canUseMatView(filters) && bucketSizeSeconds >= 3600 {
		return s.getDimensionTokenHistogramFromMatView(ctx, filters, bucketSizeSeconds, dimension)
	}
	dimCol := string(dimension)
	dialect := s.db.Dialector.Name()
	baseQuery := s.db.WithContext(ctx).Model(&Log{})
	baseQuery = s.applyFilters(baseQuery, filters)
	baseQuery = baseQuery.Where("status IN ?", []string{"success", "error"})

	var bucketExpr string
	switch dialect {
	case "sqlite":
		bucketExpr = fmt.Sprintf("CAST((CAST(strftime('%%s', timestamp) AS INTEGER) / %d) * %d AS INTEGER)", bucketSizeSeconds, bucketSizeSeconds)
	default:
		bucketExpr = fmt.Sprintf("CAST(FLOOR(EXTRACT(EPOCH FROM timestamp) / %d) * %d AS BIGINT)", bucketSizeSeconds, bucketSizeSeconds)
	}

	var results []struct {
		BucketTimestamp  int64  `gorm:"column:bucket_timestamp"`
		DimValue         string `gorm:"column:dim_value"`
		PromptTokens     int64  `gorm:"column:prompt_tokens"`
		CompletionTokens int64  `gorm:"column:completion_tokens"`
		TotalTokens      int64  `gorm:"column:total_tkns"`
	}
	if err := baseQuery.Select(fmt.Sprintf(`
		%s AS bucket_timestamp,
		COALESCE(%s, '') AS dim_value,
		COALESCE(SUM(prompt_tokens), 0) AS prompt_tokens,
		COALESCE(SUM(completion_tokens), 0) AS completion_tokens,
		COALESCE(SUM(total_tokens), 0) AS total_tkns
	`, bucketExpr, dimCol)).
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

	dimValues := sortedStringKeys(dimSet)
	allTimestamps := generateBucketTimestamps(filters.StartTime, filters.EndTime, bucketSizeSeconds)

	// If no time range specified, build buckets directly from query results
	if len(allTimestamps) == 0 {
		keys := make([]int64, 0, len(grouped))
		for ts := range grouped {
			keys = append(keys, ts)
		}
		sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
		buckets := make([]DimensionTokenHistogramBucket, 0, len(keys))
		for _, ts := range keys {
			a := grouped[ts]
			b := DimensionTokenHistogramBucket{Timestamp: time.Unix(ts, 0).UTC(), ByDimension: make(map[string]DimensionTokenStats)}
			for dim, da := range a.byDimension {
				b.ByDimension[dim] = DimensionTokenStats{
					PromptTokens:     da.prompt,
					CompletionTokens: da.completion,
					TotalTokens:      da.total,
				}
			}
			buckets = append(buckets, b)
		}
		return &DimensionTokenHistogramResult{Buckets: buckets, BucketSizeSeconds: bucketSizeSeconds, Dimension: dimension, DimensionValues: dimValues}, nil
	}

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

	return &DimensionTokenHistogramResult{Buckets: buckets, BucketSizeSeconds: bucketSizeSeconds, Dimension: dimension, DimensionValues: dimValues}, nil
}

// GetDimensionLatencyHistogram returns time-bucketed latency percentiles grouped by the specified dimension.
// Uses the mv_logs_hourly materialized view on PostgreSQL when eligible; falls back to raw queries otherwise.
// The fallback path computes AVG latency only (no percentiles) since percentile_cont is Postgres-specific.
func (s *RDBLogStore) GetDimensionLatencyHistogram(ctx context.Context, filters SearchFilters, bucketSizeSeconds int64, dimension HistogramDimension) (*DimensionLatencyHistogramResult, error) {
	if !ValidHistogramDimensions[dimension] {
		return nil, fmt.Errorf("invalid histogram dimension: %s", dimension)
	}
	if bucketSizeSeconds <= 0 {
		bucketSizeSeconds = 3600
	}
	if s.db.Dialector.Name() == "postgres" && s.canUseMatView(filters) && bucketSizeSeconds >= 3600 {
		return s.getDimensionLatencyHistogramFromMatView(ctx, filters, bucketSizeSeconds, dimension)
	}
	dimCol := string(dimension)
	dialect := s.db.Dialector.Name()
	baseQuery := s.db.WithContext(ctx).Model(&Log{})
	baseQuery = s.applyFilters(baseQuery, filters)
	baseQuery = baseQuery.Where("status IN ?", []string{"success", "error"})
	baseQuery = baseQuery.Where("latency IS NOT NULL")

	var bucketExpr string
	switch dialect {
	case "sqlite":
		bucketExpr = fmt.Sprintf("CAST((CAST(strftime('%%s', timestamp) AS INTEGER) / %d) * %d AS INTEGER)", bucketSizeSeconds, bucketSizeSeconds)
	default:
		bucketExpr = fmt.Sprintf("CAST(FLOOR(EXTRACT(EPOCH FROM timestamp) / %d) * %d AS BIGINT)", bucketSizeSeconds, bucketSizeSeconds)
	}

	var results []struct {
		BucketTimestamp int64   `gorm:"column:bucket_timestamp"`
		DimValue        string  `gorm:"column:dim_value"`
		AvgLatency      float64 `gorm:"column:avg_lat"`
		TotalRequests   int64   `gorm:"column:total_requests"`
	}
	if err := baseQuery.Select(fmt.Sprintf(`
		%s AS bucket_timestamp,
		COALESCE(%s, '') AS dim_value,
		COALESCE(AVG(latency), 0) AS avg_lat,
		COUNT(*) AS total_requests
	`, bucketExpr, dimCol)).
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
			TotalRequests: r.TotalRequests,
		}
		dimSet[r.DimValue] = struct{}{}
	}

	dimValues := sortedStringKeys(dimSet)
	allTimestamps := generateBucketTimestamps(filters.StartTime, filters.EndTime, bucketSizeSeconds)

	// If no time range specified, build buckets directly from query results
	if len(allTimestamps) == 0 {
		keys := make([]int64, 0, len(grouped))
		for ts := range grouped {
			keys = append(keys, ts)
		}
		sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
		buckets := make([]DimensionLatencyHistogramBucket, 0, len(keys))
		for _, ts := range keys {
			a := grouped[ts]
			buckets = append(buckets, DimensionLatencyHistogramBucket{
				Timestamp:   time.Unix(ts, 0).UTC(),
				ByDimension: a.byDimension,
			})
		}
		return &DimensionLatencyHistogramResult{Buckets: buckets, BucketSizeSeconds: bucketSizeSeconds, Dimension: dimension, DimensionValues: dimValues}, nil
	}

	buckets := make([]DimensionLatencyHistogramBucket, 0, len(allTimestamps))
	for _, ts := range allTimestamps {
		b := DimensionLatencyHistogramBucket{Timestamp: time.Unix(ts, 0).UTC(), ByDimension: make(map[string]DimensionLatencyStats)}
		if a, ok := grouped[ts]; ok {
			b.ByDimension = a.byDimension
		}
		buckets = append(buckets, b)
	}

	return &DimensionLatencyHistogramResult{Buckets: buckets, BucketSizeSeconds: bucketSizeSeconds, Dimension: dimension, DimensionValues: dimValues}, nil
}

// HasLogs checks if there are any logs in the database.
func (s *RDBLogStore) HasLogs(ctx context.Context) (bool, error) {
	var log Log
	err := s.db.WithContext(ctx).Select("id").Limit(1).Take(&log).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// FindByID gets a log entry from the database by its ID.
func (s *RDBLogStore) FindByID(ctx context.Context, id string) (*Log, error) {
	var log Log
	if err := s.db.WithContext(ctx).Where("id = ?", id).First(&log).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &log, nil
}

// IsLogEntryPresent checks if a log entry is present in the database.
// Here we dont load entire log entry in memory - just check if it exists.
func (s *RDBLogStore) IsLogEntryPresent(ctx context.Context, id string) (bool, error) {
	var log Log
	err := s.db.WithContext(ctx).Select("id").Where("id = ?", id).First(&log).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// FindFirst gets a log entry from the database.
func (s *RDBLogStore) FindFirst(ctx context.Context, query any, fields ...string) (*Log, error) {
	var log Log
	if err := s.db.WithContext(ctx).Select(fields).Where(query).First(&log).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &log, nil
}

// Flush deletes old log entries from the database.
func (s *RDBLogStore) Flush(ctx context.Context, since time.Time) error {
	result := s.db.WithContext(ctx).Where("status = ? AND created_at < ?", "processing", since).Delete(&Log{})
	if result.Error != nil {
		return fmt.Errorf("failed to cleanup old processing logs: %w", result.Error)
	}
	return nil
}

// GetDistinctModels returns all unique non-empty model values using SELECT DISTINCT.
// Scoped to recent data to avoid full table scans.
func (s *RDBLogStore) GetDistinctModels(ctx context.Context) ([]string, error) {
	if s.db.Dialector.Name() == "postgres" && s.matViewsReady.Load() {
		return s.getDistinctModelsFromMatView(ctx)
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -defaultFilterDataCutoffDays)
	var models []string
	err := s.db.WithContext(ctx).Model(&Log{}).
		Where("model IS NOT NULL AND model != '' AND timestamp >= ?", cutoff).
		Distinct("model").Limit(defaultFilterDataLimit).Pluck("model", &models).Error
	if err != nil {
		return nil, fmt.Errorf("failed to get distinct models: %w", err)
	}
	return models, nil
}

// GetDistinctAliases returns all unique non-empty alias values using SELECT DISTINCT.
// Scoped to recent data to avoid full table scans.
func (s *RDBLogStore) GetDistinctAliases(ctx context.Context) ([]string, error) {
	cutoff := time.Now().UTC().AddDate(0, 0, -defaultFilterDataCutoffDays)
	var aliases []string
	err := s.db.WithContext(ctx).Model(&Log{}).
		Where("alias IS NOT NULL AND alias != '' AND timestamp >= ?", cutoff).
		Distinct("alias").Limit(defaultFilterDataLimit).Pluck("alias", &aliases).Error
	if err != nil {
		return nil, fmt.Errorf("failed to get distinct aliases: %w", err)
	}
	return aliases, nil
}

// allowedKeyPairColumns is a whitelist of column names that can be used in GetDistinctKeyPairs
// to prevent SQL injection from interpolated column names.
var allowedKeyPairColumns = map[string]struct{}{
	"selected_key_id":    {},
	"selected_key_name":  {},
	"virtual_key_id":     {},
	"virtual_key_name":   {},
	"routing_rule_id":    {},
	"routing_rule_name":  {},
	"team_id":            {},
	"team_name":          {},
	"customer_id":        {},
	"customer_name":      {},
	"user_id":            {},
	"business_unit_id":   {},
	"business_unit_name": {},
}

// GetDistinctKeyPairs returns unique non-empty ID-Name pairs for the given columns using SELECT DISTINCT.
// idCol and nameCol must be valid column names (e.g., "selected_key_id", "selected_key_name").
func (s *RDBLogStore) GetDistinctKeyPairs(ctx context.Context, idCol, nameCol string) ([]KeyPairResult, error) {
	if s.db.Dialector.Name() == "postgres" && s.matViewsReady.Load() {
		return s.getDistinctKeyPairsFromMatView(ctx, idCol, nameCol)
	}
	if _, ok := allowedKeyPairColumns[idCol]; !ok {
		return nil, fmt.Errorf("invalid id column: %s", idCol)
	}
	if _, ok := allowedKeyPairColumns[nameCol]; !ok {
		return nil, fmt.Errorf("invalid name column: %s", nameCol)
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -defaultFilterDataCutoffDays)
	var results []KeyPairResult
	err := s.db.WithContext(ctx).Model(&Log{}).
		Select(fmt.Sprintf("DISTINCT %s as id, %s as name", idCol, nameCol)).
		Where(fmt.Sprintf("%s IS NOT NULL AND %s != '' AND %s IS NOT NULL AND %s != '' AND timestamp >= ?", idCol, idCol, nameCol, nameCol), cutoff).
		Limit(defaultFilterDataLimit).
		Find(&results).Error
	if err != nil {
		return nil, fmt.Errorf("failed to get distinct key pairs (%s, %s): %w", idCol, nameCol, err)
	}
	return results, nil
}

// GetDistinctRoutingEngines returns all unique routing engine values from the comma-separated column.
// Scoped to recent data to avoid full table scans.
func (s *RDBLogStore) GetDistinctRoutingEngines(ctx context.Context) ([]string, error) {
	if s.db.Dialector.Name() == "postgres" && s.matViewsReady.Load() {
		return s.getDistinctRoutingEnginesFromMatView(ctx)
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -defaultFilterDataCutoffDays)
	var rawValues []string
	err := s.db.WithContext(ctx).Model(&Log{}).
		Where("routing_engines_used IS NOT NULL AND routing_engines_used != '' AND timestamp >= ?", cutoff).
		Distinct("routing_engines_used").Limit(defaultFilterDataLimit).Pluck("routing_engines_used", &rawValues).Error
	if err != nil {
		return nil, fmt.Errorf("failed to get distinct routing engines: %w", err)
	}
	// Each row may contain comma-separated values; deduplicate across all rows
	uniqueEngines := make(map[string]struct{})
	for _, raw := range rawValues {
		for _, engine := range strings.Split(raw, ",") {
			engine = strings.TrimSpace(engine)
			if engine != "" {
				uniqueEngines[engine] = struct{}{}
			}
		}
	}
	engines := make([]string, 0, len(uniqueEngines))
	for engine := range uniqueEngines {
		engines = append(engines, engine)
	}
	return engines, nil
}

// metadataSystemKeys are metadata keys added by the system that should be excluded from filter data.
var metadataSystemKeys = map[string]struct{}{
	"isAsyncRequest": {},
}

const (
	// maxMetadataRows is the maximum number of recent rows to scan for metadata keys.
	maxMetadataRows = 1000
	// maxMetadataValuesPerKey caps the number of distinct values collected per metadata key.
	maxMetadataValuesPerKey = 100
)

// GetDistinctMetadataKeys returns unique metadata keys and their distinct values from recent logs.
// It scans a bounded number of recent rows to avoid memory bloat on large tables.
func (s *RDBLogStore) GetDistinctMetadataKeys(ctx context.Context) (map[string][]string, error) {
	cutoff := time.Now().UTC().AddDate(0, 0, -defaultFilterDataCutoffDays)
	var metadataStrings []string
	// Guard must match the partial-index predicate so the planner uses the GIN index.
	var metadataGuard string
	if s.db.Dialector.Name() == "postgres" {
		metadataGuard = "metadata IS NOT NULL AND metadata IS JSON OBJECT AND metadata != '{}' AND timestamp >= ?"
	} else {
		metadataGuard = "metadata IS NOT NULL AND json_valid(metadata) AND json_type(metadata) = 'object' AND metadata != '{}' AND timestamp >= ?"
	}
	err := s.db.WithContext(ctx).Model(&Log{}).
		Where(metadataGuard, cutoff).
		Order("timestamp DESC").
		Limit(maxMetadataRows).
		Pluck("metadata", &metadataStrings).Error
	if err != nil {
		return nil, fmt.Errorf("failed to get metadata: %w", err)
	}

	// Collect unique key-value pairs with bounded sizes
	keyValues := make(map[string]map[string]struct{})
	for _, raw := range metadataStrings {
		var parsed map[string]interface{}
		if err := sonic.UnmarshalString(raw, &parsed); err != nil {
			continue
		}
		for key, val := range parsed {
			if _, isSystem := metadataSystemKeys[key]; isSystem {
				continue
			}
			if !isValidMetadataKey(key) {
				continue
			}
			if _, ok := keyValues[key]; !ok {
				keyValues[key] = make(map[string]struct{})
			}
			if len(keyValues[key]) >= maxMetadataValuesPerKey {
				continue
			}
			var strVal string
			switch v := val.(type) {
			case string:
				strVal = v
			case float64:
				strVal = fmt.Sprint(v)
			case bool:
				strVal = fmt.Sprint(v)
			default:
				continue
			}
			if strVal != "" {
				keyValues[key][strVal] = struct{}{}
			}
		}
	}

	result := make(map[string][]string, len(keyValues))
	for key, vals := range keyValues {
		values := make([]string, 0, len(vals))
		for v := range vals {
			values = append(values, v)
		}
		sort.Strings(values)
		result[key] = values
	}
	return result, nil
}

// FindAll finds all log entries from the database.
func (s *RDBLogStore) FindAll(ctx context.Context, query any, fields ...string) ([]*Log, error) {
	var logs []*Log
	if err := s.db.WithContext(ctx).Select(fields).Where(query).Limit(defaultMaxQueryLimit).Find(&logs).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return []*Log{}, nil
		}
		return nil, err
	}
	return logs, nil
}

// allowedDistinctLogColumns is an allowlist of column names that can be passed to
// FindAllDistinct. GORM's Distinct() does not parameterize column identifiers,
// so we validate against this set to prevent SQL injection.
var allowedDistinctLogColumns = map[string]struct{}{
	"id": {}, "parent_request_id": {}, "timestamp": {}, "object_type": {},
	"provider": {}, "model": {}, "number_of_retries": {}, "fallback_index": {},
	"selected_key_id": {}, "selected_key_name": {},
	"virtual_key_id": {}, "virtual_key_name": {},
	"routing_engines_used": {}, "routing_rule_id": {}, "routing_rule_name": {},
	"status": {}, "stream": {},
}

// FindAllDistinct finds all distinct log entries for the given fields.
// Uses SQL DISTINCT to return only unique combinations, avoiding loading
// all rows when only unique values are needed (e.g., for filter dropdowns).
func (s *RDBLogStore) FindAllDistinct(ctx context.Context, query any, fields ...string) ([]*Log, error) {
	var logs []*Log
	db := s.db.WithContext(ctx).Where(query)
	if len(fields) > 0 {
		for _, f := range fields {
			if _, ok := allowedDistinctLogColumns[f]; !ok {
				return nil, fmt.Errorf("invalid distinct field: %s", f)
			}
		}
		args := make([]interface{}, len(fields))
		for i, f := range fields {
			args[i] = f
		}
		db = db.Distinct(args...)
	}
	if err := db.Limit(defaultMaxQueryLimit).Find(&logs).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return []*Log{}, nil
		}
		return nil, err
	}
	return logs, nil
}

// DeleteLogsBatch deletes logs older than the cutoff time in batches.
func (s *RDBLogStore) DeleteLogsBatch(ctx context.Context, cutoff time.Time, batchSize int) (deletedCount int64, err error) {
	// First, select the IDs of logs to delete with proper LIMIT
	var ids []string
	if err := s.db.WithContext(ctx).
		Model(&Log{}).
		Select("id").
		Where("created_at < ?", cutoff).
		Limit(batchSize).
		Pluck("id", &ids).Error; err != nil {
		return 0, err
	}

	// If no IDs found, return early
	if len(ids) == 0 {
		return 0, nil
	}

	// Delete the selected IDs
	result := s.db.WithContext(ctx).Where("id IN ?", ids).Delete(&Log{})
	if result.Error != nil {
		return 0, result.Error
	}
	return result.RowsAffected, nil
}

// Close closes the log store.
func (s *RDBLogStore) Close(ctx context.Context) error {
	sqlDB, err := s.db.WithContext(ctx).DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

// DeleteLog deletes a log entry from the database by its ID.
func (s *RDBLogStore) DeleteLog(ctx context.Context, id string) error {
	if err := s.db.WithContext(ctx).Where("id = ?", id).Delete(&Log{}).Error; err != nil {
		return err
	}
	return nil
}

// DeleteLogs deletes multiple log entries from the database by their IDs.
func (s *RDBLogStore) DeleteLogs(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	if err := s.db.WithContext(ctx).Where("id IN ?", ids).Delete(&Log{}).Error; err != nil {
		return err
	}
	return nil
}

// ============================================================================
// MCP Tool Log Methods
// ============================================================================

// applyMCPFilters applies search filters to a GORM query for MCP tool logs
func (s *RDBLogStore) applyMCPFilters(baseQuery *gorm.DB, filters MCPToolLogSearchFilters) *gorm.DB {
	if len(filters.ToolNames) > 0 {
		baseQuery = baseQuery.Where("tool_name IN ?", filters.ToolNames)
	}
	if len(filters.ServerLabels) > 0 {
		baseQuery = baseQuery.Where("server_label IN ?", filters.ServerLabels)
	}
	if len(filters.Status) > 0 {
		baseQuery = baseQuery.Where("status IN ?", filters.Status)
	}
	if len(filters.VirtualKeyIDs) > 0 {
		baseQuery = baseQuery.Where("virtual_key_id IN ?", filters.VirtualKeyIDs)
	}
	if len(filters.LLMRequestIDs) > 0 {
		baseQuery = baseQuery.Where("llm_request_id IN ?", filters.LLMRequestIDs)
	}
	if filters.StartTime != nil {
		baseQuery = baseQuery.Where("timestamp >= ?", *filters.StartTime)
	}
	if filters.EndTime != nil {
		baseQuery = baseQuery.Where("timestamp <= ?", *filters.EndTime)
	}
	if filters.MinLatency != nil {
		baseQuery = baseQuery.Where("latency >= ?", *filters.MinLatency)
	}
	if filters.MaxLatency != nil {
		baseQuery = baseQuery.Where("latency <= ?", *filters.MaxLatency)
	}
	if filters.ContentSearch != "" {
		// Search in both arguments and result fields
		dialect := s.db.Dialector.Name()
		if dialect == "postgres" {
			baseQuery = baseQuery.Where("(to_tsvector('simple', arguments) @@ plainto_tsquery('simple', ?) OR to_tsvector('simple', result) @@ plainto_tsquery('simple', ?))", filters.ContentSearch, filters.ContentSearch)
		} else {
			search := "%" + filters.ContentSearch + "%"
			baseQuery = baseQuery.Where("(arguments LIKE ? OR result LIKE ?)", search, search)
		}
	}
	return baseQuery
}

// CreateMCPToolLog inserts a new MCP tool log entry into the database.
func (s *RDBLogStore) CreateMCPToolLog(ctx context.Context, entry *MCPToolLog) error {
	return s.db.WithContext(ctx).Create(entry).Error
}

// FindMCPToolLog retrieves a single MCP tool log entry by its ID.
func (s *RDBLogStore) FindMCPToolLog(ctx context.Context, id string) (*MCPToolLog, error) {
	var log MCPToolLog
	if err := s.db.WithContext(ctx).Where("id = ?", id).First(&log).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &log, nil
}

// UpdateMCPToolLog updates an MCP tool log entry in the database.
func (s *RDBLogStore) UpdateMCPToolLog(ctx context.Context, id string, entry any) error {
	serializedEntry, err := serializeMCPToolLogUpdateEntry(entry)
	if err != nil {
		return err
	}

	tx := s.db.WithContext(ctx).Model(&MCPToolLog{}).Where("id = ?", id).Updates(serializedEntry)
	if tx.Error != nil {
		return tx.Error
	}
	if tx.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// serializeMCPToolLogUpdateEntry serializes parsed MCP tool log fields before
// passing the update payload to GORM. Non-MCPToolLog payloads are returned unchanged.
func serializeMCPToolLogUpdateEntry(entry any) (any, error) {
	switch v := entry.(type) {
	case *MCPToolLog:
		if err := v.SerializeFields(); err != nil {
			return nil, err
		}
		return v, nil
	case MCPToolLog:
		copyEntry := v
		if err := copyEntry.SerializeFields(); err != nil {
			return nil, err
		}
		return copyEntry, nil
	default:
		return entry, nil
	}
}

// SearchMCPToolLogs searches for MCP tool logs in the database.
func (s *RDBLogStore) SearchMCPToolLogs(ctx context.Context, filters MCPToolLogSearchFilters, pagination PaginationOptions) (*MCPToolLogSearchResult, error) {
	var err error
	baseQuery := s.db.WithContext(ctx).Model(&MCPToolLog{})

	// Apply filters
	baseQuery = s.applyMCPFilters(baseQuery, filters)

	// Get total count for pagination
	var totalCount int64
	if err := baseQuery.Count(&totalCount).Error; err != nil {
		return nil, err
	}

	// Build order clause
	direction := "DESC"
	if pagination.Order == "asc" {
		direction = "ASC"
	}

	var orderClause string
	switch pagination.SortBy {
	case "timestamp":
		orderClause = "timestamp " + direction
	case "latency":
		orderClause = "latency " + direction
	case "cost":
		orderClause = "cost " + direction
	default:
		orderClause = "timestamp " + direction
	}

	// Execute main query with sorting and pagination
	var logs []MCPToolLog
	mainQuery := baseQuery.Order(orderClause)

	limit := pagination.Limit
	if limit <= 0 || limit > defaultMaxSearchLimit {
		limit = defaultMaxSearchLimit
	}
	pagination.Limit = limit
	mainQuery = mainQuery.Limit(limit)
	if pagination.Offset > 0 {
		mainQuery = mainQuery.Offset(pagination.Offset)
	}

	if err = mainQuery.Find(&logs).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			pagination.TotalCount = totalCount
			return &MCPToolLogSearchResult{
				Logs:       logs,
				Pagination: pagination,
				Stats: MCPToolLogStats{
					TotalExecutions: totalCount,
				},
			}, nil
		}
		return nil, err
	}

	// Populate virtual key objects for logs that have virtual key information
	for i := range logs {
		if logs[i].VirtualKeyID != nil && logs[i].VirtualKeyName != nil {
			logs[i].VirtualKey = &tables.TableVirtualKey{
				ID:   *logs[i].VirtualKeyID,
				Name: *logs[i].VirtualKeyName,
			}
		}
	}

	hasLogs := len(logs) > 0
	if !hasLogs {
		hasLogs, err = s.HasMCPToolLogs(ctx)
		if err != nil {
			return nil, err
		}
	}

	pagination.TotalCount = totalCount
	return &MCPToolLogSearchResult{
		Logs:       logs,
		Pagination: pagination,
		Stats: MCPToolLogStats{
			TotalExecutions: totalCount,
		},
		HasLogs: hasLogs,
	}, nil
}

// GetMCPToolLogStats calculates statistics for MCP tool logs matching the given filters.
func (s *RDBLogStore) GetMCPToolLogStats(ctx context.Context, filters MCPToolLogSearchFilters) (*MCPToolLogStats, error) {
	baseQuery := s.db.WithContext(ctx).Model(&MCPToolLog{})
	baseQuery = s.applyMCPFilters(baseQuery, filters)

	// Get total count (includes processing status)
	var totalCount int64
	if err := baseQuery.Count(&totalCount).Error; err != nil {
		return nil, err
	}

	stats := &MCPToolLogStats{
		TotalExecutions: totalCount,
	}

	if totalCount > 0 {
		// Single query for all completed-execution stats
		var result struct {
			CompletedCount sql.NullInt64   `gorm:"column:completed_count"`
			SuccessCount   sql.NullInt64   `gorm:"column:success_count"`
			AvgLatency     sql.NullFloat64 `gorm:"column:avg_latency"`
			TotalCost      sql.NullFloat64 `gorm:"column:total_cost"`
		}

		statsQuery := s.db.WithContext(ctx).Model(&MCPToolLog{})
		statsQuery = s.applyMCPFilters(statsQuery, filters)
		statsQuery = statsQuery.Where("status IN ?", []string{"success", "error"})

		if err := statsQuery.Select(`
			COUNT(*) as completed_count,
			SUM(CASE WHEN status = 'success' THEN 1 ELSE 0 END) as success_count,
			AVG(latency) as avg_latency,
			SUM(cost) as total_cost
		`).Scan(&result).Error; err != nil {
			return nil, err
		}

		completedCount := result.CompletedCount.Int64
		if completedCount > 0 {
			stats.SuccessRate = float64(result.SuccessCount.Int64) / float64(completedCount) * 100
			if result.AvgLatency.Valid {
				stats.AverageLatency = result.AvgLatency.Float64
			}
			if result.TotalCost.Valid {
				stats.TotalCost = result.TotalCost.Float64
			}
		}
	}

	return stats, nil
}

// HasMCPToolLogs checks if there are any MCP tool logs in the database.
func (s *RDBLogStore) HasMCPToolLogs(ctx context.Context) (bool, error) {
	var log MCPToolLog
	err := s.db.WithContext(ctx).Select("id").Limit(1).Take(&log).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// DeleteMCPToolLogs deletes multiple MCP tool log entries from the database by their IDs.
func (s *RDBLogStore) DeleteMCPToolLogs(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	if err := s.db.WithContext(ctx).Where("id IN ?", ids).Delete(&MCPToolLog{}).Error; err != nil {
		return err
	}
	return nil
}

// FlushMCPToolLogs deletes old processing MCP tool log entries from the database.
func (s *RDBLogStore) FlushMCPToolLogs(ctx context.Context, since time.Time) error {
	result := s.db.WithContext(ctx).Where("status = ? AND created_at < ?", "processing", since).Delete(&MCPToolLog{})
	if result.Error != nil {
		return fmt.Errorf("failed to cleanup old processing MCP tool logs: %w", result.Error)
	}
	return nil
}

// GetAvailableToolNames returns all unique tool names from the MCP tool logs.
// Scoped to recent data to avoid full table scans.
func (s *RDBLogStore) GetAvailableToolNames(ctx context.Context) ([]string, error) {
	cutoff := time.Now().UTC().AddDate(0, 0, -defaultFilterDataCutoffDays)
	var toolNames []string
	result := s.db.WithContext(ctx).Model(&MCPToolLog{}).
		Where("tool_name IS NOT NULL AND tool_name != '' AND timestamp >= ?", cutoff).
		Distinct("tool_name").Limit(defaultFilterDataLimit).Pluck("tool_name", &toolNames)
	if result.Error != nil {
		return nil, fmt.Errorf("failed to get available tool names: %w", result.Error)
	}
	return toolNames, nil
}

// GetAvailableServerLabels returns all unique server labels from the MCP tool logs.
// Scoped to recent data to avoid full table scans.
func (s *RDBLogStore) GetAvailableServerLabels(ctx context.Context) ([]string, error) {
	cutoff := time.Now().UTC().AddDate(0, 0, -defaultFilterDataCutoffDays)
	var serverLabels []string
	result := s.db.WithContext(ctx).Model(&MCPToolLog{}).
		Where("server_label IS NOT NULL AND server_label != '' AND timestamp >= ?", cutoff).
		Distinct("server_label").Limit(defaultFilterDataLimit).Pluck("server_label", &serverLabels)
	if result.Error != nil {
		return nil, fmt.Errorf("failed to get available server labels: %w", result.Error)
	}
	return serverLabels, nil
}

// GetAvailableMCPVirtualKeys returns all unique virtual key ID-Name pairs from MCP tool logs.
// Scoped to recent data to avoid full table scans.
func (s *RDBLogStore) GetAvailableMCPVirtualKeys(ctx context.Context) ([]MCPToolLog, error) {
	cutoff := time.Now().UTC().AddDate(0, 0, -defaultFilterDataCutoffDays)
	var logs []MCPToolLog
	result := s.db.WithContext(ctx).
		Model(&MCPToolLog{}).
		Select("DISTINCT virtual_key_id, virtual_key_name").
		Where("virtual_key_id IS NOT NULL AND virtual_key_id != '' AND virtual_key_name IS NOT NULL AND virtual_key_name != '' AND timestamp >= ?", cutoff).
		Limit(defaultFilterDataLimit).
		Find(&logs)
	if result.Error != nil {
		return nil, fmt.Errorf("failed to get available virtual keys from MCP logs: %w", result.Error)
	}
	return logs, nil
}

// GetMCPHistogram returns time-bucketed MCP tool call volume for the given filters.
func (s *RDBLogStore) GetMCPHistogram(ctx context.Context, filters MCPToolLogSearchFilters, bucketSizeSeconds int64) (*MCPHistogramResult, error) {
	if bucketSizeSeconds <= 0 {
		bucketSizeSeconds = 3600
	}

	dialect := s.db.Dialector.Name()

	baseQuery := s.db.WithContext(ctx).Model(&MCPToolLog{})
	baseQuery = s.applyMCPFilters(baseQuery, filters)
	baseQuery = baseQuery.Where("status IN ?", []string{"success", "error"})

	var results []struct {
		BucketTimestamp int64 `gorm:"column:bucket_timestamp"`
		Count           int64 `gorm:"column:count"`
		Success         int64 `gorm:"column:success"`
		Error           int64 `gorm:"column:error"`
	}

	var selectClause string
	switch dialect {
	case "sqlite":
		selectClause = fmt.Sprintf(`
			(CAST(strftime('%%s', timestamp) AS INTEGER) / %d) * %d as bucket_timestamp,
			COUNT(*) as count,
			SUM(CASE WHEN status = 'success' THEN 1 ELSE 0 END) as success,
			SUM(CASE WHEN status = 'error' THEN 1 ELSE 0 END) as error
		`, bucketSizeSeconds, bucketSizeSeconds)
	case "mysql":
		selectClause = fmt.Sprintf(`
			(FLOOR(UNIX_TIMESTAMP(timestamp) / %d) * %d) as bucket_timestamp,
			COUNT(*) as count,
			SUM(CASE WHEN status = 'success' THEN 1 ELSE 0 END) as success,
			SUM(CASE WHEN status = 'error' THEN 1 ELSE 0 END) as error
		`, bucketSizeSeconds, bucketSizeSeconds)
	default:
		selectClause = fmt.Sprintf(`
			CAST(FLOOR(EXTRACT(EPOCH FROM timestamp) / %d) * %d AS BIGINT) as bucket_timestamp,
			COUNT(*) as count,
			SUM(CASE WHEN status = 'success' THEN 1 ELSE 0 END) as success,
			SUM(CASE WHEN status = 'error' THEN 1 ELSE 0 END) as error
		`, bucketSizeSeconds, bucketSizeSeconds)
	}

	if err := baseQuery.
		Select(selectClause).
		Group("bucket_timestamp").
		Order("bucket_timestamp ASC").
		Find(&results).Error; err != nil {
		return nil, fmt.Errorf("failed to get mcp histogram: %w", err)
	}

	resultMap := make(map[int64]struct {
		Count   int64
		Success int64
		Error   int64
	})
	for _, r := range results {
		resultMap[r.BucketTimestamp] = struct {
			Count   int64
			Success int64
			Error   int64
		}{Count: r.Count, Success: r.Success, Error: r.Error}
	}

	allTimestamps := generateBucketTimestamps(filters.StartTime, filters.EndTime, bucketSizeSeconds)

	if len(allTimestamps) == 0 {
		buckets := make([]MCPHistogramBucket, len(results))
		for i, r := range results {
			buckets[i] = MCPHistogramBucket{
				Timestamp: time.Unix(r.BucketTimestamp, 0).UTC(),
				Count:     r.Count,
				Success:   r.Success,
				Error:     r.Error,
			}
		}
		return &MCPHistogramResult{Buckets: buckets, BucketSizeSeconds: bucketSizeSeconds}, nil
	}

	buckets := make([]MCPHistogramBucket, len(allTimestamps))
	for i, ts := range allTimestamps {
		if data, exists := resultMap[ts]; exists {
			buckets[i] = MCPHistogramBucket{
				Timestamp: time.Unix(ts, 0).UTC(),
				Count:     data.Count,
				Success:   data.Success,
				Error:     data.Error,
			}
		} else {
			buckets[i] = MCPHistogramBucket{Timestamp: time.Unix(ts, 0).UTC()}
		}
	}

	return &MCPHistogramResult{Buckets: buckets, BucketSizeSeconds: bucketSizeSeconds}, nil
}

// GetMCPCostHistogram returns time-bucketed MCP cost data for the given filters.
func (s *RDBLogStore) GetMCPCostHistogram(ctx context.Context, filters MCPToolLogSearchFilters, bucketSizeSeconds int64) (*MCPCostHistogramResult, error) {
	if bucketSizeSeconds <= 0 {
		bucketSizeSeconds = 3600
	}

	dialect := s.db.Dialector.Name()

	baseQuery := s.db.WithContext(ctx).Model(&MCPToolLog{})
	baseQuery = s.applyMCPFilters(baseQuery, filters)
	baseQuery = baseQuery.Where("status IN ?", []string{"success", "error"})

	var results []struct {
		BucketTimestamp int64   `gorm:"column:bucket_timestamp"`
		TotalCost       float64 `gorm:"column:total_cost"`
	}

	var selectClause string
	switch dialect {
	case "sqlite":
		selectClause = fmt.Sprintf(`
			(CAST(strftime('%%s', timestamp) AS INTEGER) / %d) * %d as bucket_timestamp,
			COALESCE(SUM(cost), 0) as total_cost
		`, bucketSizeSeconds, bucketSizeSeconds)
	case "mysql":
		selectClause = fmt.Sprintf(`
			(FLOOR(UNIX_TIMESTAMP(timestamp) / %d) * %d) as bucket_timestamp,
			COALESCE(SUM(cost), 0) as total_cost
		`, bucketSizeSeconds, bucketSizeSeconds)
	default:
		selectClause = fmt.Sprintf(`
			CAST(FLOOR(EXTRACT(EPOCH FROM timestamp) / %d) * %d AS BIGINT) as bucket_timestamp,
			COALESCE(SUM(cost), 0) as total_cost
		`, bucketSizeSeconds, bucketSizeSeconds)
	}

	if err := baseQuery.
		Select(selectClause).
		Group("bucket_timestamp").
		Order("bucket_timestamp ASC").
		Find(&results).Error; err != nil {
		return nil, fmt.Errorf("failed to get mcp cost histogram: %w", err)
	}

	resultMap := make(map[int64]float64)
	for _, r := range results {
		resultMap[r.BucketTimestamp] = r.TotalCost
	}

	allTimestamps := generateBucketTimestamps(filters.StartTime, filters.EndTime, bucketSizeSeconds)

	if len(allTimestamps) == 0 {
		buckets := make([]MCPCostHistogramBucket, len(results))
		for i, r := range results {
			buckets[i] = MCPCostHistogramBucket{
				Timestamp: time.Unix(r.BucketTimestamp, 0).UTC(),
				TotalCost: r.TotalCost,
			}
		}
		return &MCPCostHistogramResult{Buckets: buckets, BucketSizeSeconds: bucketSizeSeconds}, nil
	}

	buckets := make([]MCPCostHistogramBucket, len(allTimestamps))
	for i, ts := range allTimestamps {
		if cost, exists := resultMap[ts]; exists {
			buckets[i] = MCPCostHistogramBucket{
				Timestamp: time.Unix(ts, 0).UTC(),
				TotalCost: cost,
			}
		} else {
			buckets[i] = MCPCostHistogramBucket{Timestamp: time.Unix(ts, 0).UTC()}
		}
	}

	return &MCPCostHistogramResult{Buckets: buckets, BucketSizeSeconds: bucketSizeSeconds}, nil
}

// GetMCPTopTools returns the top N MCP tools by call count for the given filters.
func (s *RDBLogStore) GetMCPTopTools(ctx context.Context, filters MCPToolLogSearchFilters, limit int) (*MCPTopToolsResult, error) {
	if limit <= 0 {
		limit = 10
	}

	baseQuery := s.db.WithContext(ctx).Model(&MCPToolLog{})
	baseQuery = s.applyMCPFilters(baseQuery, filters)
	baseQuery = baseQuery.Where("status IN ?", []string{"success", "error"})

	var results []struct {
		ToolName string  `gorm:"column:tool_name"`
		Count    int64   `gorm:"column:count"`
		Cost     float64 `gorm:"column:cost"`
	}

	if err := baseQuery.
		Select("tool_name, COUNT(*) as count, COALESCE(SUM(cost), 0) as cost").
		Group("tool_name").
		Order("count DESC").
		Limit(limit).
		Find(&results).Error; err != nil {
		return nil, fmt.Errorf("failed to get mcp top tools: %w", err)
	}

	tools := make([]MCPTopToolResult, len(results))
	for i, r := range results {
		tools[i] = MCPTopToolResult{
			ToolName: r.ToolName,
			Count:    r.Count,
			Cost:     r.Cost,
		}
	}

	return &MCPTopToolsResult{Tools: tools}, nil
}

// CreateAsyncJob creates a new async job record in the database.
func (s *RDBLogStore) CreateAsyncJob(ctx context.Context, job *AsyncJob) error {
	return s.db.WithContext(ctx).Create(job).Error
}

// FindAsyncJobByID retrieves an async job by its ID.
func (s *RDBLogStore) FindAsyncJobByID(ctx context.Context, id string) (*AsyncJob, error) {
	var job AsyncJob
	result := s.db.WithContext(ctx).Where("id = ? AND (expires_at IS NULL OR expires_at > ?)", id, time.Now().UTC()).First(&job)
	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, result.Error
	}
	return &job, nil
}

// UpdateAsyncJob updates an async job record with the provided fields.
func (s *RDBLogStore) UpdateAsyncJob(ctx context.Context, id string, updates map[string]interface{}) error {
	return s.db.WithContext(ctx).Model(&AsyncJob{}).Where("id = ?", id).Updates(updates).Error
}

// DeleteExpiredAsyncJobs deletes async jobs whose expires_at has passed.
// Only deletes jobs that have a non-null expires_at (i.e., completed or failed jobs).
// Deletes in batches to avoid long-running transactions that hold row locks.
func (s *RDBLogStore) DeleteExpiredAsyncJobs(ctx context.Context) (int64, error) {
	now := time.Now().UTC()
	const batchLimit = 100
	var totalDeleted int64
	for {
		result := s.db.WithContext(ctx).
			Where("id IN (?)",
				s.db.Model(&AsyncJob{}).Select("id").
					Where("expires_at IS NOT NULL AND expires_at < ?", now).
					Limit(batchLimit),
			).Delete(&AsyncJob{})
		if result.Error != nil {
			return totalDeleted, result.Error
		}
		totalDeleted += result.RowsAffected
		if result.RowsAffected < batchLimit {
			break
		}
	}
	return totalDeleted, nil
}

// DeleteStaleAsyncJobs deletes async jobs stuck in "processing" status since before the given time.
// This handles edge cases like marshal failures or server crashes that leave jobs permanently stuck.
func (s *RDBLogStore) DeleteStaleAsyncJobs(ctx context.Context, staleSince time.Time) (int64, error) {
	result := s.db.WithContext(ctx).
		Where("status = ? AND created_at < ?", "processing", staleSince).
		Delete(&AsyncJob{})
	return result.RowsAffected, result.Error
}
