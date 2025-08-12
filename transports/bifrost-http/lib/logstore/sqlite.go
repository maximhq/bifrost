package logstore

import (
	"database/sql"
	"fmt"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// SQLiteConfig represents the configuration for a SQLite database.
type SQLiteConfig struct {
	Path string `json:"path"`
}

// SQLiteLogStore represents a logs store that uses a SQLite database.
type SQLiteLogStore struct {
	db *gorm.DB
}

// Insert inserts a new log entry into the database.
func (s *SQLiteLogStore) Insert(entry *Log) error {
	return s.db.Create(entry).Error
}

// Update updates a log entry in the database.
func (s *SQLiteLogStore) Update(id string, entry any) error {
	return s.db.Model(&Log{}).Where("id = ?", id).Updates(entry).Error
}

// SearchLogs searches for logs in the database.
func (s *SQLiteLogStore) SearchLogs(filters SearchFilters, pagination PaginationOptions) (*SearchResult, error) {
	baseQuery := s.db.Model(&Log{})

	// Apply filters efficiently
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
	if filters.ContentSearch != "" {
		baseQuery = baseQuery.Where("content_summary LIKE ?", "%"+filters.ContentSearch+"%")
	}

	// Get total count
	var totalCount int64
	if err := baseQuery.Count(&totalCount).Error; err != nil {
		return nil, err
	}

	// Initialize stats
	stats := SearchStats{}

	// Calculate statistics efficiently if we have data
	if totalCount > 0 {
		// Total requests should include all requests (processing, success, error)
		stats.TotalRequests = totalCount

		// Get completed requests count (success + error, excluding processing) for success rate calculation
		var completedCount int64
		completedQuery := baseQuery.Session(&gorm.Session{})
		if err := completedQuery.Where("status IN ?", []string{"success", "error"}).Count(&completedCount).Error; err != nil {
			return nil, err
		}

		if completedCount > 0 {
			// Calculate success rate based on completed requests only
			var successCount int64
			successQuery := baseQuery.Session(&gorm.Session{})
			if err := successQuery.Where("status = ?", "success").Count(&successCount).Error; err != nil {
				return nil, err
			}
			stats.SuccessRate = float64(successCount) / float64(completedCount) * 100

			// Calculate average latency and total tokens in a single query for better performance
			var result struct {
				AvgLatency  sql.NullFloat64 `json:"avg_latency"`
				TotalTokens sql.NullInt64   `json:"total_tokens"`
			}

			statsQuery := baseQuery.Session(&gorm.Session{})
			if err := statsQuery.Select("AVG(latency) as avg_latency, SUM(total_tokens) as total_tokens").Scan(&result).Error; err != nil {
				return nil, err
			}

			if result.AvgLatency.Valid {
				stats.AverageLatency = result.AvgLatency.Float64
			}
			if result.TotalTokens.Valid {
				stats.TotalTokens = result.TotalTokens.Int64
			}
		}
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

	if err := mainQuery.Find(&logs).Error; err != nil {
		return nil, err
	}

	return &SearchResult{
		Logs:       logs,
		Pagination: pagination,
		Stats:      stats,
	}, nil
}

// Get gets a log entry from the database.
func (s *SQLiteLogStore) Get(query any, fields ...string) (*Log, error) {
	var log Log
	if err := s.db.Select(fields).Where(query).First(&log).Error; err != nil {
		return nil, err
	}
	return &log, nil
}

// CleanupLogs deletes old log entries from the database.
func (s *SQLiteLogStore) CleanupLogs(since time.Time) error {
	result := s.db.Where("status = ? AND created_at < ?", "processing", since).Delete(&Log{})
	if result.Error != nil {
		return fmt.Errorf("failed to cleanup old processing logs: %w", result.Error)
	}

	if result.RowsAffected > 0 {
		logger.Info(fmt.Sprintf("Cleaned up %d old processing logs", result.RowsAffected))
	}
	return nil
}

func newSqliteLogStore(config *SQLiteConfig) (*SQLiteLogStore, error) {
	db, err := gorm.Open(sqlite.Open(config.Path), &gorm.Config{})
	if err != nil {
		return nil, err
	}
	if err := db.AutoMigrate(&Log{}); err != nil {
		return nil, err
	}
	return &SQLiteLogStore{db: db}, nil
}
