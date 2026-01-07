package logstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// RDBLogStore represents a log store that uses a SQLite database.
type RDBLogStore struct {
	db     *gorm.DB
	logger schemas.Logger
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
	if len(filters.Status) > 0 {
		baseQuery = baseQuery.Where("status IN ?", filters.Status)
	}
	if len(filters.Objects) > 0 {
		baseQuery = baseQuery.Where("object_type IN ?", filters.Objects)
	}
	if len(filters.SelectedKeyIDs) > 0 {
		baseQuery = baseQuery.Where("selected_key_id IN ?", filters.SelectedKeyIDs)
	}
	if len(filters.VirtualKeyIDs) > 0 {
		baseQuery = baseQuery.Where("virtual_key_id IN ?", filters.VirtualKeyIDs)
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
		baseQuery = baseQuery.Where("cost IS NULL OR cost <= 0")
	}
	if filters.ContentSearch != "" {
		baseQuery = baseQuery.Where("content_summary LIKE ?", "%"+filters.ContentSearch+"%")
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

// Ping checks if the database is reachable.
func (s *RDBLogStore) Ping(ctx context.Context) error {
	return s.db.WithContext(ctx).Exec("SELECT 1").Error
}

// Update updates a log entry in the database.
func (s *RDBLogStore) Update(ctx context.Context, id string, entry any) error {
	tx := s.db.WithContext(ctx).Model(&Log{}).Where("id = ?", id).Updates(entry)
	if tx.Error != nil {
		return tx.Error
	}
	if tx.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *RDBLogStore) BulkUpdateCost(ctx context.Context, updates map[string]float64) error {
	if len(updates) == 0 {
		return nil
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

// SearchLogs searches for logs in the database without calculating statistics.
func (s *RDBLogStore) SearchLogs(ctx context.Context, filters SearchFilters, pagination PaginationOptions) (*SearchResult, error) {
	var err error
	baseQuery := s.db.WithContext(ctx).Model(&Log{})

	// Apply filters efficiently
	baseQuery = s.applyFilters(baseQuery, filters)

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
	case "tokens":
		orderClause = "total_tokens " + direction
	case "cost":
		orderClause = "cost " + direction
	default:
		orderClause = "timestamp " + direction
	}

	// Execute main query with sorting and pagination
	var logs []Log
	mainQuery := baseQuery.Order(orderClause)

	if pagination.Limit > 0 {
		mainQuery = mainQuery.Limit(pagination.Limit)
	}
	if pagination.Offset > 0 {
		mainQuery = mainQuery.Offset(pagination.Offset)
	}

	if err = mainQuery.Find(&logs).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return &SearchResult{
				Logs:       logs,
				Pagination: pagination,
				Stats: SearchStats{
					TotalRequests: totalCount,
				},
			}, nil
		}
		return nil, err
	}

	hasLogs := len(logs) > 0
	if !hasLogs {
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

// GetStats calculates statistics for logs matching the given filters.
func (s *RDBLogStore) GetStats(ctx context.Context, filters SearchFilters) (*SearchStats, error) {
	baseQuery := s.db.WithContext(ctx).Model(&Log{})

	// Apply filters
	baseQuery = s.applyFilters(baseQuery, filters)

	// Get total count
	var totalCount int64
	if err := baseQuery.Count(&totalCount).Error; err != nil {
		return nil, err
	}

	// Initialize stats
	stats := &SearchStats{
		TotalRequests: totalCount,
	}

	// Calculate statistics only if we have data
	if totalCount > 0 {
		// Build a completed query (success + error, excluding processing)
		completedQuery := s.db.WithContext(ctx).Model(&Log{})
		completedQuery = s.applyFilters(completedQuery, filters)
		completedQuery = completedQuery.Where("status IN ?", []string{"success", "error"})

		// Get completed requests count
		var completedCount int64
		if err := completedQuery.Count(&completedCount).Error; err != nil {
			return nil, err
		}

		if completedCount > 0 {
			// Calculate success rate based on completed requests only
			successQuery := s.db.WithContext(ctx).Model(&Log{})
			successQuery = s.applyFilters(successQuery, filters)
			successQuery = successQuery.Where("status = ?", "success")

			var successCount int64
			if err := successQuery.Count(&successCount).Error; err != nil {
				return nil, err
			}
			stats.SuccessRate = float64(successCount) / float64(completedCount) * 100

			// Calculate average latency and total tokens in a single query for better performance
			var result struct {
				AvgLatency  sql.NullFloat64 `json:"avg_latency"`
				TotalTokens sql.NullInt64   `json:"total_tokens"`
				TotalCost   sql.NullFloat64 `json:"total_cost"`
			}

			statsQuery := s.db.WithContext(ctx).Model(&Log{})
			statsQuery = s.applyFilters(statsQuery, filters)
			statsQuery = statsQuery.Where("status IN ?", []string{"success", "error"})

			if err := statsQuery.Select("AVG(latency) as avg_latency, SUM(total_tokens) as total_tokens, SUM(cost) as total_cost").Scan(&result).Error; err != nil {
				return nil, err
			}

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
	}

	return stats, nil
}

// GetHistogram returns time-bucketed request counts for the given filters.
func (s *RDBLogStore) GetHistogram(ctx context.Context, filters SearchFilters, bucketSizeSeconds int64) (*HistogramResult, error) {
	if bucketSizeSeconds <= 0 {
		bucketSizeSeconds = 3600 // Default to 1 hour
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
	}

	var selectClause string
	switch dialect {
	case "sqlite":
		selectClause = fmt.Sprintf(`
			(CAST(strftime('%%s', timestamp) AS INTEGER) / %d) * %d as bucket_timestamp,
			COALESCE(SUM(prompt_tokens), 0) as prompt_tokens,
			COALESCE(SUM(completion_tokens), 0) as completion_tokens,
			COALESCE(SUM(total_tokens), 0) as total_tokens
		`, bucketSizeSeconds, bucketSizeSeconds)
	case "mysql":
		selectClause = fmt.Sprintf(`
			(FLOOR(UNIX_TIMESTAMP(timestamp) / %d) * %d) as bucket_timestamp,
			COALESCE(SUM(prompt_tokens), 0) as prompt_tokens,
			COALESCE(SUM(completion_tokens), 0) as completion_tokens,
			COALESCE(SUM(total_tokens), 0) as total_tokens
		`, bucketSizeSeconds, bucketSizeSeconds)
	default:
		selectClause = fmt.Sprintf(`
			CAST(FLOOR(EXTRACT(EPOCH FROM timestamp) / %d) * %d AS BIGINT) as bucket_timestamp,
			COALESCE(SUM(prompt_tokens), 0) as prompt_tokens,
			COALESCE(SUM(completion_tokens), 0) as completion_tokens,
			COALESCE(SUM(total_tokens), 0) as total_tokens
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
	})
	for _, r := range results {
		resultMap[r.BucketTimestamp] = struct {
			PromptTokens     int64
			CompletionTokens int64
			TotalTokens      int64
		}{
			PromptTokens:     r.PromptTokens,
			CompletionTokens: r.CompletionTokens,
			TotalTokens:      r.TotalTokens,
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
			}
		} else {
			buckets[i] = TokenHistogramBucket{
				Timestamp:        time.Unix(ts, 0).UTC(),
				PromptTokens:     0,
				CompletionTokens: 0,
				TotalTokens:      0,
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
		for i := 0; i < len(buckets)-1; i++ {
			for j := i + 1; j < len(buckets); j++ {
				if buckets[i].Timestamp.After(buckets[j].Timestamp) {
					buckets[i], buckets[j] = buckets[j], buckets[i]
				}
			}
		}

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
		for i := 0; i < len(buckets)-1; i++ {
			for j := i + 1; j < len(buckets); j++ {
				if buckets[i].Timestamp.After(buckets[j].Timestamp) {
					buckets[i], buckets[j] = buckets[j], buckets[i]
				}
			}
		}

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

// FindAll finds all log entries from the database.
func (s *RDBLogStore) FindAll(ctx context.Context, query any, fields ...string) ([]*Log, error) {
	var logs []*Log
	if err := s.db.WithContext(ctx).Select(fields).Where(query).Find(&logs).Error; err != nil {
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
		baseQuery = baseQuery.Where("(arguments LIKE ? OR result LIKE ?)", "%"+filters.ContentSearch+"%", "%"+filters.ContentSearch+"%")
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
	tx := s.db.WithContext(ctx).Model(&MCPToolLog{}).Where("id = ?", id).Updates(entry)
	if tx.Error != nil {
		return tx.Error
	}
	if tx.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
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

	if pagination.Limit > 0 {
		mainQuery = mainQuery.Limit(pagination.Limit)
	}
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

	// Apply filters
	baseQuery = s.applyMCPFilters(baseQuery, filters)

	// Get total count
	var totalCount int64
	if err := baseQuery.Count(&totalCount).Error; err != nil {
		return nil, err
	}

	// Initialize stats
	stats := &MCPToolLogStats{
		TotalExecutions: totalCount,
	}

	// Calculate statistics only if we have data
	if totalCount > 0 {
		// Build a completed query (success + error, excluding processing)
		completedQuery := s.db.WithContext(ctx).Model(&MCPToolLog{})
		completedQuery = s.applyMCPFilters(completedQuery, filters)
		completedQuery = completedQuery.Where("status IN ?", []string{"success", "error"})

		// Get completed executions count
		var completedCount int64
		if err := completedQuery.Count(&completedCount).Error; err != nil {
			return nil, err
		}

		if completedCount > 0 {
			// Calculate success rate based on completed executions only
			successQuery := s.db.WithContext(ctx).Model(&MCPToolLog{})
			successQuery = s.applyMCPFilters(successQuery, filters)
			successQuery = successQuery.Where("status = ?", "success")

			var successCount int64
			if err := successQuery.Count(&successCount).Error; err != nil {
				return nil, err
			}
			stats.SuccessRate = float64(successCount) / float64(completedCount) * 100

			// Calculate average latency and total cost
			var result struct {
				AvgLatency sql.NullFloat64 `json:"avg_latency"`
				TotalCost  sql.NullFloat64 `json:"total_cost"`
			}

			statsQuery := s.db.WithContext(ctx).Model(&MCPToolLog{})
			statsQuery = s.applyMCPFilters(statsQuery, filters)
			statsQuery = statsQuery.Where("status IN ?", []string{"success", "error"})

			if err := statsQuery.Select("AVG(latency) as avg_latency, SUM(cost) as total_cost").Scan(&result).Error; err != nil {
				return nil, err
			}

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
func (s *RDBLogStore) GetAvailableToolNames(ctx context.Context) ([]string, error) {
	var toolNames []string
	result := s.db.WithContext(ctx).Model(&MCPToolLog{}).Distinct("tool_name").Pluck("tool_name", &toolNames)
	if result.Error != nil {
		return nil, fmt.Errorf("failed to get available tool names: %w", result.Error)
	}
	return toolNames, nil
}

// GetAvailableServerLabels returns all unique server labels from the MCP tool logs.
func (s *RDBLogStore) GetAvailableServerLabels(ctx context.Context) ([]string, error) {
	var serverLabels []string
	result := s.db.WithContext(ctx).Model(&MCPToolLog{}).Distinct("server_label").Where("server_label != ''").Pluck("server_label", &serverLabels)
	if result.Error != nil {
		return nil, fmt.Errorf("failed to get available server labels: %w", result.Error)
	}
	return serverLabels, nil
}

// GetAvailableMCPVirtualKeys returns all unique virtual key ID-Name pairs from MCP tool logs.
func (s *RDBLogStore) GetAvailableMCPVirtualKeys(ctx context.Context) ([]MCPToolLog, error) {
	var logs []MCPToolLog
	result := s.db.WithContext(ctx).
		Model(&MCPToolLog{}).
		Select("DISTINCT virtual_key_id, virtual_key_name").
		Where("virtual_key_id IS NOT NULL AND virtual_key_id != '' AND virtual_key_name IS NOT NULL AND virtual_key_name != ''").
		Find(&logs)
	if result.Error != nil {
		return nil, fmt.Errorf("failed to get available virtual keys from MCP logs: %w", result.Error)
	}
	return logs, nil
}
