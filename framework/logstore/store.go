package logstore

import (
	"context"
	"fmt"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// LogStoreType represents the type of log store.
type LogStoreType string

// LogStoreTypeSQLite is the type of log store for SQLite.
const (
	LogStoreTypeSQLite   LogStoreType = "sqlite"
	LogStoreTypePostgres LogStoreType = "postgres"
)

// LogStore is the interface for the log store.
type LogStore interface {
	Ping(ctx context.Context) error
	Create(ctx context.Context, entry *Log) error
	CreateIfNotExists(ctx context.Context, entry *Log) error
	FindByID(ctx context.Context, id string) (*Log, error)
	FindFirst(ctx context.Context, query any, fields ...string) (*Log, error)
	FindAll(ctx context.Context, query any, fields ...string) ([]*Log, error)
	HasLogs(ctx context.Context) (bool, error)
	SearchLogs(ctx context.Context, filters SearchFilters, pagination PaginationOptions) (*SearchResult, error)
	GetStats(ctx context.Context, filters SearchFilters) (*SearchStats, error)
	GetHistogram(ctx context.Context, filters SearchFilters, bucketSizeSeconds int64) (*HistogramResult, error)
	GetTokenHistogram(ctx context.Context, filters SearchFilters, bucketSizeSeconds int64) (*TokenHistogramResult, error)
	GetCostHistogram(ctx context.Context, filters SearchFilters, bucketSizeSeconds int64) (*CostHistogramResult, error)
	GetModelHistogram(ctx context.Context, filters SearchFilters, bucketSizeSeconds int64) (*ModelHistogramResult, error)
	Update(ctx context.Context, id string, entry any) error
	BulkUpdateCost(ctx context.Context, updates map[string]float64) error
	Flush(ctx context.Context, since time.Time) error
	Close(ctx context.Context) error
	DeleteLog(ctx context.Context, id string) error
	DeleteLogs(ctx context.Context, ids []string) error
	DeleteLogsBatch(ctx context.Context, cutoff time.Time, batchSize int) (deletedCount int64, err error)
}

// NewLogStore creates a new log store based on the configuration.
func NewLogStore(ctx context.Context, config *Config, logger schemas.Logger) (LogStore, error) {
	switch config.Type {
	case LogStoreTypeSQLite:
		if sqliteConfig, ok := config.Config.(*SQLiteConfig); ok {
			return newSqliteLogStore(ctx, sqliteConfig, logger)
		}
		return nil, fmt.Errorf("invalid sqlite config: %T", config.Config)
	case LogStoreTypePostgres:
		if postgresConfig, ok := config.Config.(*PostgresConfig); ok {
			return newPostgresLogStore(ctx, postgresConfig, logger)
		}
		return nil, fmt.Errorf("invalid postgres config: %T", config.Config)
	default:
		return nil, fmt.Errorf("unsupported log store type: %s", config.Type)
	}
}
