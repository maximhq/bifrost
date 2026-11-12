package logstore

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"gorm.io/gorm"
)

// TraceFilters narrows the trace list, the span list and the Tracing overview. ProjectIDs is the
// caller's visible projects; an empty list means every project.
type TraceFilters struct {
	ProjectIDs   []string `json:"project_ids,omitempty"`
	Sources      []string `json:"sources,omitempty"`
	Kinds        []string `json:"kinds,omitempty"`  // span list only
	Models       []string `json:"models,omitempty"` // span list only
	Environments []string `json:"environments,omitempty"`
	Tags         []string `json:"tags,omitempty"`
	SessionID    string   `json:"session_id,omitempty"`
	UserID       string   `json:"user_id,omitempty"`
	ExperimentID string   `json:"experiment_id,omitempty"`
	TraceID      string   `json:"trace_id,omitempty"`
	Name         string   `json:"name,omitempty"`           // exact name
	ParentSpanID string   `json:"parent_span_id,omitempty"` // span list only
	// ExcludeTraceRows leaves out the span that stands for a Langfuse trace object: its span id is the trace id.
	ExcludeTraceRows bool       `json:"-"`
	Status           string     `json:"status,omitempty"` // "error" = with errors, "ok" = without, "in_progress"
	Search           string     `json:"search,omitempty"` // name, input and output
	StartTime        *time.Time `json:"start_time,omitempty"`
	EndTime          *time.Time `json:"end_time,omitempty"`
	MinCost          *float64   `json:"min_cost,omitempty"`
	MinTokens        *int       `json:"min_tokens,omitempty"`
	MinDuration      *float64   `json:"min_duration_ms,omitempty"`
}

// TraceSearchResult is one page of traces.
type TraceSearchResult struct {
	Traces     []*Trace          `json:"traces"`
	Pagination PaginationOptions `json:"pagination"`
}

// TraceSpanSearchResult is one page of spans across traces.
type TraceSpanSearchResult struct {
	Spans      []*TraceSpan      `json:"spans"`
	Pagination PaginationOptions `json:"pagination"`
}

// TraceStats is the Tracing overview. Every figure counts a model call once: linked spans with the
// gateway's numbers, the rest with the SDK's.
type TraceStats struct {
	TraceCount        int64   `json:"trace_count"`
	SpanCount         int64   `json:"span_count"`
	LLMSpanCount      int64   `json:"llm_span_count"`
	LinkedSpanCount   int64   `json:"linked_span_count"` // model calls served by this gateway
	ErrorTraceCount   int64   `json:"error_trace_count"`
	InProgressCount   int64   `json:"in_progress_count"`
	TotalTokens       int64   `json:"total_tokens"`
	PromptTokens      int64   `json:"prompt_tokens"`
	CompletionTokens  int64   `json:"completion_tokens"`
	CachedReadTokens  int64   `json:"cached_read_tokens"`
	TotalCost         float64 `json:"total_cost"`
	EstimatedCost     float64 `json:"estimated_cost"`      // the share of TotalCost priced by Bifrost
	UnpricedSpanCount int64   `json:"unpriced_span_count"` // model calls with tokens and no price
	AvgDurationMs     float64 `json:"avg_duration_ms"`
}

// TraceHistogramBucket is one time bucket of the trace histogram.
type TraceHistogramBucket struct {
	Timestamp   time.Time `json:"timestamp"`
	Count       int64     `json:"count"`
	Errors      int64     `json:"errors"`
	TotalTokens int64     `json:"total_tokens"`
	TotalCost   float64   `json:"total_cost"`
}

// TraceHistogramResult is the trace histogram.
type TraceHistogramResult struct {
	Buckets           []TraceHistogramBucket `json:"buckets"`
	BucketSizeSeconds int64                  `json:"bucket_size_seconds"`
}

// TraceBreakdownRow is the overview's figures for one project or one source.
type TraceBreakdownRow struct {
	Key             string  `gorm:"column:key_value" json:"key"`
	TraceCount      int64   `json:"trace_count"`
	TotalTokens     int64   `json:"total_tokens"`
	TotalCost       float64 `json:"total_cost"`
	ErrorTraceCount int64   `json:"error_trace_count"`
	LLMSpanCount    int64   `json:"llm_span_count"`
	LinkedSpanCount int64   `json:"linked_span_count"`
}

// TraceSession is the traces that share one session id, as one conversation.
type TraceSession struct {
	ProjectID   string    `json:"project_id"`
	SessionID   string    `json:"session_id"`
	TraceCount  int64     `json:"trace_count"`
	FirstSeen   time.Time `json:"first_seen"`
	LastSeen    time.Time `json:"last_seen"`
	TotalTokens int64     `json:"total_tokens"`
	TotalCost   float64   `json:"total_cost"`
	ErrorCount  int64     `json:"error_count"`
	UserID      string    `json:"user_id,omitempty"`
}

// TraceSessionSearchResult is one page of trace sessions.
type TraceSessionSearchResult struct {
	Sessions   []*TraceSession   `json:"sessions"`
	Pagination PaginationOptions `json:"pagination"`
}

// TraceFilterData is the distinct values the filter bar offers.
type TraceFilterData struct {
	Sources      []string `json:"sources"`
	Environments []string `json:"environments"`
	Models       []string `json:"models"`
	Kinds        []string `json:"kinds"`
}

// TraceReadStore is the read side of the trace store, used by the dashboard API.
type TraceReadStore interface {
	SearchTraces(ctx context.Context, filters TraceFilters, pagination PaginationOptions) (*TraceSearchResult, error)
	SearchTraceSpans(ctx context.Context, filters TraceFilters, pagination PaginationOptions) (*TraceSpanSearchResult, error)
	GetTraceStats(ctx context.Context, filters TraceFilters) (*TraceStats, error)
	GetTraceHistogram(ctx context.Context, filters TraceFilters, bucketSizeSeconds int64) (*TraceHistogramResult, error)
	GetTraceBreakdown(ctx context.Context, filters TraceFilters, dimension string) ([]TraceBreakdownRow, error)
	SearchTraceSessions(ctx context.Context, filters TraceFilters, pagination PaginationOptions) (*TraceSessionSearchResult, error)
	GetTraceFilterData(ctx context.Context, projectIDs []string) (*TraceFilterData, error)
	ListTraceUploads(ctx context.Context, projectID string, ids []string) ([]*TraceUpload, error)
	ListProjectTraceKeys(ctx context.Context, projectID string, limit int) ([]TraceKey, error)
	SearchTraceScores(ctx context.Context, filters TraceScoreFilters, limit, offset int) ([]*TraceScore, error)
}

// TraceScoreFilters narrows a listing of scores across the traces of one project.
type TraceScoreFilters struct {
	ProjectID string
	IDs       []string
	Names     []string
	Sources   []string
	TraceIDs  []string
	SpanIDs   []string
	From      *time.Time
	To        *time.Time
}

// traceLike adds a case-insensitive substring match on the given columns, OR-ed together.
func (s *RDBLogStore) traceLike(query *gorm.DB, term string, columns ...string) *gorm.DB {
	pattern := "%" + strings.ToLower(escapeLike(term)) + "%"
	clauses := make([]string, len(columns))
	args := make([]any, len(columns))
	for i, column := range columns {
		switch s.db.Dialector.Name() {
		case "postgres":
			clauses[i] = column + " ILIKE ?"
		case "clickhouse":
			clauses[i] = "lowerUTF8(" + column + ") LIKE ?"
		case "sqlite":
			clauses[i] = "LOWER(" + column + ") LIKE ? ESCAPE '\\'"
		default:
			clauses[i] = "LOWER(" + column + ") LIKE ?"
		}
		args[i] = pattern
	}
	return query.Where("("+strings.Join(clauses, " OR ")+")", args...)
}

// applyTraceFilters narrows a query on the traces table.
func (s *RDBLogStore) applyTraceFilters(query *gorm.DB, filters TraceFilters) *gorm.DB {
	if len(filters.ProjectIDs) > 0 {
		query = query.Where("project_id IN ?", filters.ProjectIDs)
	}
	if len(filters.Sources) > 0 {
		query = query.Where("source IN ?", filters.Sources)
	}
	if len(filters.Environments) > 0 {
		query = query.Where("environment IN ?", filters.Environments)
	}
	for _, tag := range filters.Tags {
		// Tags are stored as a JSON list of strings, so a tag is matched with its quotes.
		query = s.traceLike(query, `"`+tag+`"`, "tags")
	}
	if filters.SessionID != "" {
		query = query.Where("session_id = ?", filters.SessionID)
	}
	if filters.UserID != "" {
		query = query.Where("user_id = ?", filters.UserID)
	}
	if filters.ExperimentID != "" {
		query = query.Where("experiment_id = ?", filters.ExperimentID)
	}
	if filters.TraceID != "" {
		query = query.Where("trace_id = ?", filters.TraceID)
	}
	if filters.Name != "" {
		query = query.Where("name = ?", filters.Name)
	}
	switch filters.Status {
	case TraceSpanStatusError:
		query = query.Where("error_count > 0")
	case TraceSpanStatusOK:
		query = query.Where("error_count = 0")
	case "in_progress":
		query = query.Where("in_progress = ?", true)
	}
	if filters.Search != "" {
		query = s.traceLike(query, filters.Search, "name", "input", "output")
	}
	if filters.StartTime != nil {
		query = query.Where("start_time >= ?", *filters.StartTime)
	}
	if filters.EndTime != nil {
		query = query.Where("start_time <= ?", *filters.EndTime)
	}
	if filters.MinCost != nil {
		query = query.Where("cost >= ?", *filters.MinCost)
	}
	if filters.MinTokens != nil {
		query = query.Where("total_tokens >= ?", *filters.MinTokens)
	}
	if filters.MinDuration != nil {
		query = query.Where("duration_ms >= ?", *filters.MinDuration)
	}
	return query
}

// traceSortColumns maps the API's sort keys onto columns. Anything else sorts by start time.
var traceSortColumns = map[string]string{"timestamp": "start_time", "duration": "duration_ms", "latency": "duration_ms", "tokens": "total_tokens", "cost": "cost", "name": "name"}

func traceOrder(pagination PaginationOptions, tiebreak string) string {
	column, ok := traceSortColumns[pagination.SortBy]
	if !ok {
		column = "start_time"
	}
	direction := "DESC"
	if strings.EqualFold(pagination.Order, "asc") {
		direction = "ASC"
	}
	return column + " " + direction + ", " + tiebreak + " " + direction
}

func tracePage(pagination PaginationOptions) (limit, offset int) {
	limit = pagination.Limit
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	return limit, max(pagination.Offset, 0)
}

// SearchTraces returns one page of trace rollups, newest first by default.
func (s *RDBLogStore) SearchTraces(ctx context.Context, filters TraceFilters, pagination PaginationOptions) (*TraceSearchResult, error) {
	base := func() *gorm.DB { return s.applyTraceFilters(s.db.WithContext(ctx).Model(&Trace{}), filters) }
	if err := base().Count(&pagination.TotalCount).Error; err != nil {
		return nil, fmt.Errorf("failed to count traces: %w", err)
	}
	limit, offset := tracePage(pagination)
	var traces []*Trace
	if err := base().Order(traceOrder(pagination, "trace_id")).Limit(limit).Offset(offset).Find(&traces).Error; err != nil {
		return nil, fmt.Errorf("failed to search traces: %w", err)
	}
	pagination.Limit, pagination.Offset = limit, offset
	return &TraceSearchResult{Traces: traces, Pagination: pagination}, nil
}

// applyTraceSpanFilters narrows a query on the trace_spans table.
func (s *RDBLogStore) applyTraceSpanFilters(query *gorm.DB, filters TraceFilters) *gorm.DB {
	if len(filters.ProjectIDs) > 0 {
		query = query.Where("project_id IN ?", filters.ProjectIDs)
	}
	if len(filters.Sources) > 0 {
		query = query.Where("source IN ?", filters.Sources)
	}
	if len(filters.Kinds) > 0 {
		query = query.Where("kind IN ?", filters.Kinds)
	}
	if len(filters.Models) > 0 {
		query = query.Where("model IN ?", filters.Models)
	}
	if len(filters.Environments) > 0 {
		query = query.Where("environment IN ?", filters.Environments)
	}
	for _, tag := range filters.Tags {
		query = s.traceLike(query, `"`+tag+`"`, "tags")
	}
	if filters.SessionID != "" {
		query = query.Where("session_id = ?", filters.SessionID)
	}
	if filters.UserID != "" {
		query = query.Where("user_id = ?", filters.UserID)
	}
	if filters.ExperimentID != "" {
		query = query.Where("experiment_id = ?", filters.ExperimentID)
	}
	if filters.TraceID != "" {
		query = query.Where("trace_id = ?", filters.TraceID)
	}
	if filters.Name != "" {
		query = query.Where("name = ?", filters.Name)
	}
	if filters.ParentSpanID != "" {
		query = query.Where("parent_span_id = ?", filters.ParentSpanID)
	}
	if filters.ExcludeTraceRows {
		query = query.Where("span_id <> trace_id")
	}
	switch filters.Status {
	case TraceSpanStatusError, TraceSpanStatusOK:
		query = query.Where("status = ?", filters.Status)
	case "in_progress":
		query = query.Where("partial = ?", true)
	}
	if filters.Search != "" {
		query = s.traceLike(query, filters.Search, "name", "input", "output")
	}
	if filters.StartTime != nil {
		query = query.Where("start_time >= ?", *filters.StartTime)
	}
	if filters.EndTime != nil {
		query = query.Where("start_time <= ?", *filters.EndTime)
	}
	if filters.MinCost != nil {
		query = query.Where("cost >= ?", *filters.MinCost)
	}
	if filters.MinTokens != nil {
		query = query.Where("total_tokens >= ?", *filters.MinTokens)
	}
	if filters.MinDuration != nil {
		query = query.Where("duration_ms >= ?", *filters.MinDuration)
	}
	return query
}

// SearchTraceSpans returns one page of spans across traces, newest first by default.
func (s *RDBLogStore) SearchTraceSpans(ctx context.Context, filters TraceFilters, pagination PaginationOptions) (*TraceSpanSearchResult, error) {
	base := func() *gorm.DB { return s.applyTraceSpanFilters(s.db.WithContext(ctx).Model(&TraceSpan{}), filters) }
	if err := base().Count(&pagination.TotalCount).Error; err != nil {
		return nil, fmt.Errorf("failed to count trace spans: %w", err)
	}
	limit, offset := tracePage(pagination)
	var spans []*TraceSpan
	if err := base().Order(traceOrder(pagination, "span_id")).Limit(limit).Offset(offset).Find(&spans).Error; err != nil {
		return nil, fmt.Errorf("failed to search trace spans: %w", err)
	}
	pagination.Limit, pagination.Offset = limit, offset
	return &TraceSpanSearchResult{Spans: spans, Pagination: pagination}, nil
}

// GetTraceStats sums the rollups of the matching traces.
func (s *RDBLogStore) GetTraceStats(ctx context.Context, filters TraceFilters) (*TraceStats, error) {
	var stats TraceStats
	err := s.applyTraceFilters(s.db.WithContext(ctx).Model(&Trace{}), filters).Select(`
		COUNT(*) AS trace_count,
		COALESCE(SUM(span_count), 0) AS span_count,
		COALESCE(SUM(llm_span_count), 0) AS llm_span_count,
		COALESCE(SUM(linked_span_count), 0) AS linked_span_count,
		COALESCE(SUM(CASE WHEN error_count > 0 THEN 1 ELSE 0 END), 0) AS error_trace_count,
		COALESCE(SUM(CASE WHEN in_progress THEN 1 ELSE 0 END), 0) AS in_progress_count,
		COALESCE(SUM(total_tokens), 0) AS total_tokens,
		COALESCE(SUM(prompt_tokens), 0) AS prompt_tokens,
		COALESCE(SUM(completion_tokens), 0) AS completion_tokens,
		COALESCE(SUM(cached_read_tokens), 0) AS cached_read_tokens,
		COALESCE(SUM(cost), 0) AS total_cost,
		COALESCE(SUM(estimated_cost), 0) AS estimated_cost,
		COALESCE(SUM(unpriced_span_count), 0) AS unpriced_span_count,
		COALESCE(AVG(duration_ms), 0) AS avg_duration_ms`).Scan(&stats).Error
	if err != nil {
		return nil, fmt.Errorf("failed to get trace stats: %w", err)
	}
	return &stats, nil
}

// traceBucketExpr buckets start_time into bucketSizeSeconds-wide unix buckets.
func traceBucketExpr(dialect string, bucketSizeSeconds int64) string {
	switch dialect {
	case "sqlite":
		return fmt.Sprintf("(CAST(strftime('%%s', start_time) AS INTEGER) / %d) * %d", bucketSizeSeconds, bucketSizeSeconds)
	case "clickhouse":
		return fmt.Sprintf("toInt64(intDiv(toUnixTimestamp(start_time), %d) * %d)", bucketSizeSeconds, bucketSizeSeconds)
	default:
		return fmt.Sprintf("CAST(FLOOR(EXTRACT(EPOCH FROM start_time) / %d) * %d AS BIGINT)", bucketSizeSeconds, bucketSizeSeconds)
	}
}

// GetTraceHistogram counts traces per time bucket. Empty buckets inside the filter's time range
// are returned as zeros, so the chart has no gaps.
func (s *RDBLogStore) GetTraceHistogram(ctx context.Context, filters TraceFilters, bucketSizeSeconds int64) (*TraceHistogramResult, error) {
	if bucketSizeSeconds <= 0 {
		bucketSizeSeconds = 3600
	}
	var rows []struct {
		BucketTimestamp int64   `gorm:"column:bucket_timestamp"`
		Count           int64   `gorm:"column:count"`
		Errors          int64   `gorm:"column:errors"`
		TotalTokens     int64   `gorm:"column:total_tokens"`
		TotalCost       float64 `gorm:"column:total_cost"`
	}
	selectClause := fmt.Sprintf(`%s AS bucket_timestamp, COUNT(*) AS count,
		COALESCE(SUM(CASE WHEN error_count > 0 THEN 1 ELSE 0 END), 0) AS errors,
		COALESCE(SUM(total_tokens), 0) AS total_tokens, COALESCE(SUM(cost), 0) AS total_cost`, traceBucketExpr(s.db.Dialector.Name(), bucketSizeSeconds))
	err := s.applyTraceFilters(s.db.WithContext(ctx).Model(&Trace{}), filters).Select(selectClause).Group("bucket_timestamp").Order("bucket_timestamp ASC").Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("failed to get trace histogram: %w", err)
	}
	byTimestamp := make(map[int64]TraceHistogramBucket, len(rows))
	for _, row := range rows {
		byTimestamp[row.BucketTimestamp] = TraceHistogramBucket{Timestamp: time.Unix(row.BucketTimestamp, 0).UTC(), Count: row.Count, Errors: row.Errors, TotalTokens: row.TotalTokens, TotalCost: row.TotalCost}
	}
	result := &TraceHistogramResult{BucketSizeSeconds: bucketSizeSeconds}
	if timestamps := generateBucketTimestamps(filters.StartTime, filters.EndTime, bucketSizeSeconds); len(timestamps) > 0 {
		for _, ts := range timestamps {
			bucket, ok := byTimestamp[ts]
			if !ok {
				bucket = TraceHistogramBucket{Timestamp: time.Unix(ts, 0).UTC()}
			}
			result.Buckets = append(result.Buckets, bucket)
		}
		return result, nil
	}
	for _, row := range rows {
		result.Buckets = append(result.Buckets, byTimestamp[row.BucketTimestamp])
	}
	return result, nil
}

// GetTraceBreakdown splits the overview by "project" or "source".
func (s *RDBLogStore) GetTraceBreakdown(ctx context.Context, filters TraceFilters, dimension string) ([]TraceBreakdownRow, error) {
	column := map[string]string{"project": "project_id", "source": "source", "environment": "environment"}[dimension]
	if column == "" {
		return nil, fmt.Errorf("unknown trace breakdown dimension %q", dimension)
	}
	var rows []TraceBreakdownRow
	err := s.applyTraceFilters(s.db.WithContext(ctx).Model(&Trace{}), filters).Select(column + ` AS key_value, COUNT(*) AS trace_count,
		COALESCE(SUM(total_tokens), 0) AS total_tokens, COALESCE(SUM(cost), 0) AS total_cost,
		COALESCE(SUM(CASE WHEN error_count > 0 THEN 1 ELSE 0 END), 0) AS error_trace_count,
		COALESCE(SUM(llm_span_count), 0) AS llm_span_count, COALESCE(SUM(linked_span_count), 0) AS linked_span_count`).
		Group(column).Order("trace_count DESC").Limit(100).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("failed to get trace breakdown: %w", err)
	}
	return rows, nil
}

// SearchTraceSessions groups the matching traces that carry a session id into sessions, most
// recently active first.
func (s *RDBLogStore) SearchTraceSessions(ctx context.Context, filters TraceFilters, pagination PaginationOptions) (*TraceSessionSearchResult, error) {
	base := func() *gorm.DB {
		return s.applyTraceFilters(s.db.WithContext(ctx).Model(&Trace{}), filters).Where("session_id <> ''")
	}
	var total []struct{ ProjectID, SessionID string }
	if err := base().Select("project_id, session_id").Group("project_id, session_id").Limit(10000).Scan(&total).Error; err != nil {
		return nil, fmt.Errorf("failed to count trace sessions: %w", err)
	}
	pagination.TotalCount = int64(len(total))
	limit, offset := tracePage(pagination)
	var rows []struct {
		ProjectID, SessionID, UserID string
		TraceCount, TotalTokens      int64
		ErrorCount                   int64
		TotalCost                    float64
		FirstSeen, LastSeen          aggregatedTime
	}
	err := base().Select(`project_id, session_id, COUNT(*) AS trace_count, MIN(start_time) AS first_seen, MAX(start_time) AS last_seen,
		COALESCE(SUM(total_tokens), 0) AS total_tokens, COALESCE(SUM(cost), 0) AS total_cost,
		COALESCE(SUM(CASE WHEN error_count > 0 THEN 1 ELSE 0 END), 0) AS error_count, MAX(user_id) AS user_id`).
		Group("project_id, session_id").Order("last_seen DESC, session_id DESC").Limit(limit).Offset(offset).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("failed to search trace sessions: %w", err)
	}
	sessions := make([]*TraceSession, len(rows))
	for i, row := range rows {
		sessions[i] = &TraceSession{
			ProjectID: row.ProjectID, SessionID: row.SessionID, UserID: row.UserID, TraceCount: row.TraceCount, TotalTokens: row.TotalTokens,
			ErrorCount: row.ErrorCount, TotalCost: row.TotalCost, FirstSeen: time.Time(row.FirstSeen), LastSeen: time.Time(row.LastSeen),
		}
	}
	pagination.Limit, pagination.Offset = limit, offset
	return &TraceSessionSearchResult{Sessions: sessions, Pagination: pagination}, nil
}

// aggregatedTime scans the result of MIN or MAX over a time column. SQLite returns such an
// aggregate as text, because the column's declared type does not carry through the aggregate.
type aggregatedTime time.Time

func (t *aggregatedTime) Scan(value any) error {
	switch v := value.(type) {
	case nil:
		*t = aggregatedTime{}
	case time.Time:
		*t = aggregatedTime(v)
	case []byte:
		return t.parse(string(v))
	case string:
		return t.parse(v)
	default:
		return fmt.Errorf("cannot scan %T into a time", value)
	}
	return nil
}

func (t *aggregatedTime) parse(text string) error {
	for _, layout := range []string{"2006-01-02 15:04:05.999999999-07:00", "2006-01-02 15:04:05.999999999Z07:00", time.RFC3339Nano, "2006-01-02 15:04:05.999999999", "2006-01-02 15:04:05"} {
		if parsed, err := time.Parse(layout, text); err == nil {
			*t = aggregatedTime(parsed.UTC())
			return nil
		}
	}
	return fmt.Errorf("cannot parse %q as a time", text)
}

// GetTraceFilterData returns the distinct values the filter bar offers for the given projects.
func (s *RDBLogStore) GetTraceFilterData(ctx context.Context, projectIDs []string) (*TraceFilterData, error) {
	distinct := func(model any, column string) ([]string, error) {
		query := s.db.WithContext(ctx).Model(model).Where(column + " <> ''")
		if len(projectIDs) > 0 {
			query = query.Where("project_id IN ?", projectIDs)
		}
		var values []string
		if err := query.Distinct(column).Limit(200).Pluck(column, &values).Error; err != nil {
			return nil, fmt.Errorf("failed to list trace %s values: %w", column, err)
		}
		slices.Sort(values)
		return values, nil
	}
	var data TraceFilterData
	var err error
	if data.Sources, err = distinct(&Trace{}, "source"); err != nil {
		return nil, err
	}
	if data.Environments, err = distinct(&Trace{}, "environment"); err != nil {
		return nil, err
	}
	if data.Models, err = distinct(&TraceSpan{}, "model"); err != nil {
		return nil, err
	}
	if data.Kinds, err = distinct(&TraceSpan{}, "kind"); err != nil {
		return nil, err
	}
	return &data, nil
}

// ListTraceUploads returns the named uploads of a project, without their bytes.
func (s *RDBLogStore) ListTraceUploads(ctx context.Context, projectID string, ids []string) ([]*TraceUpload, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var uploads []*TraceUpload
	err := s.db.WithContext(ctx).Omit("data").Where("project_id = ? AND id IN ?", projectID, ids).Find(&uploads).Error
	return uploads, err
}

// ListProjectTraceKeys returns up to limit traces of a project, for deleting a project's data in
// batches.
func (s *RDBLogStore) ListProjectTraceKeys(ctx context.Context, projectID string, limit int) ([]TraceKey, error) {
	var keys []TraceKey
	err := s.db.WithContext(ctx).Model(&Trace{}).Select("project_id", "trace_id").Where("project_id = ?", projectID).Limit(limit).Find(&keys).Error
	return keys, err
}

// SearchTraceScores lists a project's scores, newest first.
func (s *RDBLogStore) SearchTraceScores(ctx context.Context, filters TraceScoreFilters, limit, offset int) ([]*TraceScore, error) {
	query := s.db.WithContext(ctx).Where("project_id = ?", filters.ProjectID)
	for column, values := range map[string][]string{"id": filters.IDs, "name": filters.Names, "source": filters.Sources, "trace_id": filters.TraceIDs, "span_id": filters.SpanIDs} {
		if len(values) > 0 {
			query = query.Where(column+" IN ?", values)
		}
	}
	if filters.From != nil {
		query = query.Where("created_at >= ?", *filters.From)
	}
	if filters.To != nil {
		query = query.Where("created_at <= ?", *filters.To)
	}
	var scores []*TraceScore
	if err := query.Order("created_at DESC, id DESC").Limit(limit).Offset(offset).Find(&scores).Error; err != nil {
		return nil, fmt.Errorf("failed to search trace scores: %w", err)
	}
	return scores, nil
}
