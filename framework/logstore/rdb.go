package logstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// RDBLogStore represents a log store that uses a SQLite database.
type RDBLogStore struct {
	db     *gorm.DB
	logger schemas.Logger
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
