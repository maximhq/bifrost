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
	BatchCreateIfNotExists(ctx context.Context, entries []*Log) error
	FindByID(ctx context.Context, id string) (*Log, error)
	FindFirst(ctx context.Context, query any, fields ...string) (*Log, error)
	FindAll(ctx context.Context, query any, fields ...string) ([]*Log, error)
	FindAllDistinct(ctx context.Context, query any, fields ...string) ([]*Log, error)
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

	// Distinct value methods for filter data
	GetDistinctModels(ctx context.Context) ([]string, error)
	GetDistinctKeyPairs(ctx context.Context, idCol, nameCol string) ([]KeyPairResult, error)
	GetDistinctRoutingEngines(ctx context.Context) ([]string, error)

	// MCP Tool Log methods
	CreateMCPToolLog(ctx context.Context, entry *MCPToolLog) error
	FindMCPToolLog(ctx context.Context, id string) (*MCPToolLog, error)
	UpdateMCPToolLog(ctx context.Context, id string, entry any) error
	SearchMCPToolLogs(ctx context.Context, filters MCPToolLogSearchFilters, pagination PaginationOptions) (*MCPToolLogSearchResult, error)
	GetMCPToolLogStats(ctx context.Context, filters MCPToolLogSearchFilters) (*MCPToolLogStats, error)
	HasMCPToolLogs(ctx context.Context) (bool, error)
	DeleteMCPToolLogs(ctx context.Context, ids []string) error
	FlushMCPToolLogs(ctx context.Context, since time.Time) error
	GetAvailableToolNames(ctx context.Context) ([]string, error)
	GetAvailableServerLabels(ctx context.Context) ([]string, error)
	GetAvailableMCPVirtualKeys(ctx context.Context) ([]MCPToolLog, error)

	// Async Job methods
	CreateAsyncJob(ctx context.Context, job *AsyncJob) error
	FindAsyncJobByID(ctx context.Context, id string) (*AsyncJob, error)
	UpdateAsyncJob(ctx context.Context, id string, updates map[string]interface{}) error
	DeleteExpiredAsyncJobs(ctx context.Context) (int64, error)
	DeleteStaleAsyncJobs(ctx context.Context, staleSince time.Time) (int64, error)
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
