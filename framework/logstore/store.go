package logstore

import (
	"context"
	"fmt"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/objectstore"
)

// LogStoreType represents the type of log store.
type LogStoreType string

// LogStoreTypeSQLite is the type of log store for SQLite.
const (
	LogStoreTypeSQLite     LogStoreType = "sqlite"
	LogStoreTypePostgres   LogStoreType = "postgres"
	LogStoreTypeClickHouse LogStoreType = "clickhouse"
)

// LogStore is the interface for the log store.
type LogStore interface {
	Ping(ctx context.Context) error
	Create(ctx context.Context, entry *Log) error
	CreateIfNotExists(ctx context.Context, entry *Log) error
	BatchCreateIfNotExists(ctx context.Context, entries []*Log) error
	FindByID(ctx context.Context, id string) (*Log, error)
	IsLogEntryPresent(ctx context.Context, id string) (bool, error)
	FindFirst(ctx context.Context, query any, fields ...string) (*Log, error)
	FindAll(ctx context.Context, query any, fields ...string) ([]*Log, error)
	FindAllDistinct(ctx context.Context, query any, fields ...string) ([]*Log, error)
	HasLogs(ctx context.Context) (bool, error)
	SearchLogs(ctx context.Context, filters SearchFilters, pagination PaginationOptions) (*SearchResult, error)
	GetSessionLogs(ctx context.Context, sessionID string, pagination PaginationOptions) (*SessionDetailResult, error)
	GetSessionSummary(ctx context.Context, sessionID string) (*SessionSummaryResult, error)
	GetStats(ctx context.Context, filters SearchFilters) (*SearchStats, error)
	GetHistogram(ctx context.Context, filters SearchFilters, bucketSizeSeconds int64) (*HistogramResult, error)
	GetTokenHistogram(ctx context.Context, filters SearchFilters, bucketSizeSeconds int64) (*TokenHistogramResult, error)
	GetCostHistogram(ctx context.Context, filters SearchFilters, bucketSizeSeconds int64) (*CostHistogramResult, error)
	GetModelHistogram(ctx context.Context, filters SearchFilters, bucketSizeSeconds int64) (*ModelHistogramResult, error)
	GetLatencyHistogram(ctx context.Context, filters SearchFilters, bucketSizeSeconds int64) (*LatencyHistogramResult, error)
	GetProviderCostHistogram(ctx context.Context, filters SearchFilters, bucketSizeSeconds int64) (*ProviderCostHistogramResult, error)
	GetProviderTokenHistogram(ctx context.Context, filters SearchFilters, bucketSizeSeconds int64) (*ProviderTokenHistogramResult, error)
	GetProviderLatencyHistogram(ctx context.Context, filters SearchFilters, bucketSizeSeconds int64) (*ProviderLatencyHistogramResult, error)
	// GetThroughputHistogram returns time-bucketed token-generation throughput (tokens/sec).
	GetThroughputHistogram(ctx context.Context, filters SearchFilters, bucketSizeSeconds int64) (*ThroughputHistogramResult, error)
	// GetProviderThroughputHistogram returns time-bucketed tokens/sec with provider breakdown.
	GetProviderThroughputHistogram(ctx context.Context, filters SearchFilters, bucketSizeSeconds int64) (*ProviderThroughputHistogramResult, error)
	GetModelRankings(ctx context.Context, filters SearchFilters) (*ModelRankingResult, error)
	GetUserRankings(ctx context.Context, filters SearchFilters) (*UserRankingResult, error)
	GetDimensionRankings(ctx context.Context, filters SearchFilters, dimension RankingDimension) (*DimensionRankingResult, error)
	// GetDimensionCostHistogram returns time-bucketed cost data grouped by the specified dimension (e.g., team_id, customer_id).
	GetDimensionCostHistogram(ctx context.Context, filters SearchFilters, bucketSizeSeconds int64, dimension HistogramDimension) (*DimensionCostHistogramResult, error)
	// GetDimensionTokenHistogram returns time-bucketed token usage grouped by the specified dimension.
	GetDimensionTokenHistogram(ctx context.Context, filters SearchFilters, bucketSizeSeconds int64, dimension HistogramDimension) (*DimensionTokenHistogramResult, error)
	// GetDimensionLatencyHistogram returns time-bucketed latency percentiles grouped by the specified dimension.
	GetDimensionLatencyHistogram(ctx context.Context, filters SearchFilters, bucketSizeSeconds int64, dimension HistogramDimension) (*DimensionLatencyHistogramResult, error)
	// GetNodeUsageAfter returns cumulative usage for rows after the provided stable
	// cursor. When the cursor has both timestamp and log ID, rows with the same
	// timestamp but greater log ID are included to avoid skipping same-timestamp rows.
	GetNodeUsageAfter(ctx context.Context, nodeID string, cursor NodeUsageCursor) (*NodeUsageAggregate, error)
	Update(ctx context.Context, id string, entry any) error
	BulkUpdateCost(ctx context.Context, updates map[string]float64) error
	Flush(ctx context.Context, since time.Time) error
	Close(ctx context.Context) error
	DeleteLog(ctx context.Context, id string) error
	DeleteLogs(ctx context.Context, ids []string) error
	DeleteLogsBatch(ctx context.Context, cutoff time.Time, batchSize int) (deletedCount int64, err error)

	CreateUserAgentMapping(ctx context.Context, mapping *UserAgentMapping) error
	UpdateUserAgentMapping(ctx context.Context, id string, mapping *UserAgentMapping) error
	DeleteUserAgentMapping(ctx context.Context, id string) error
	ListUserAgentMappings(ctx context.Context, activeOnly bool) ([]UserAgentMapping, error)

	// Distinct value methods for filter data
	GetDistinctModels(ctx context.Context, limit int, query string) ([]string, error)
	GetDistinctAliases(ctx context.Context, limit int, query string) ([]string, error)
	GetDistinctKeyPairs(ctx context.Context, idCol, nameCol string, limit int, query string) ([]KeyPairResult, error)
	GetDistinctRoutingEngines(ctx context.Context, limit int, query string) ([]string, error)
	GetDistinctStopReasons(ctx context.Context, limit int, query string) ([]string, error)
	// GetDistinctUserAgents returns distinct raw User-Agent strings from logs for the "App" filter.
	GetDistinctUserAgents(ctx context.Context, limit int, query string) ([]string, error)
	// GetDistinctApps returns distinct backend-detected app labels from logs.
	GetDistinctApps(ctx context.Context, limit int, query string) ([]string, error)
	GetDistinctMetadataKeys(ctx context.Context, limit int, query string) (map[string][]string, error)

	// MCP Tool Log histogram methods
	GetMCPHistogram(ctx context.Context, filters MCPToolLogSearchFilters, bucketSizeSeconds int64) (*MCPHistogramResult, error)
	GetMCPCostHistogram(ctx context.Context, filters MCPToolLogSearchFilters, bucketSizeSeconds int64) (*MCPCostHistogramResult, error)
	GetMCPTopTools(ctx context.Context, filters MCPToolLogSearchFilters, limit int) (*MCPTopToolsResult, error)

	// MCP Tool Log methods
	CreateMCPToolLog(ctx context.Context, entry *MCPToolLog) error
	BatchCreateMCPToolLogsIfNotExists(ctx context.Context, entries []*MCPToolLog) error
	FindMCPToolLog(ctx context.Context, id string) (*MCPToolLog, error)
	UpdateMCPToolLog(ctx context.Context, id string, entry any) error
	SearchMCPToolLogs(ctx context.Context, filters MCPToolLogSearchFilters, pagination PaginationOptions) (*MCPToolLogSearchResult, error)
	GetMCPToolLogStats(ctx context.Context, filters MCPToolLogSearchFilters) (*MCPToolLogStats, error)
	HasMCPToolLogs(ctx context.Context) (bool, error)
	DeleteMCPToolLogs(ctx context.Context, ids []string) error
	FlushMCPToolLogs(ctx context.Context, since time.Time) error
	GetAvailableToolNames(ctx context.Context, limit int, query string) ([]string, error)
	GetAvailableServerLabels(ctx context.Context, limit int, query string) ([]string, error)
	// GetAvailableMCPUserAgents returns distinct raw User-Agent strings from MCP tool logs for the "App" filter.
	GetAvailableMCPUserAgents(ctx context.Context, limit int, query string) ([]string, error)
	// GetAvailableMCPApps returns distinct backend-detected app labels from MCP tool logs.
	GetAvailableMCPApps(ctx context.Context, limit int, query string) ([]string, error)
	GetAvailableMCPVirtualKeys(ctx context.Context, limit int, query string) ([]MCPToolLog, error)

	// Async Job methods
	CreateAsyncJob(ctx context.Context, job *AsyncJob) error
	FindAsyncJobByID(ctx context.Context, id string) (*AsyncJob, error)
	UpdateAsyncJob(ctx context.Context, id string, updates map[string]interface{}) error
	DeleteExpiredAsyncJobs(ctx context.Context) (int64, error)
	DeleteStaleAsyncJobs(ctx context.Context, staleSince time.Time) (int64, error)

	// Webhook Delivery methods
	CreateWebhookDelivery(ctx context.Context, delivery *WebhookDelivery) error
	FindWebhookDeliveryByID(ctx context.Context, id string) (*WebhookDelivery, error)
	SearchWebhookDeliveries(ctx context.Context, endpointID string, pagination PaginationOptions) (*WebhookDeliverySearchResult, error)
	DeleteExpiredWebhookDeliveries(ctx context.Context) (int64, error)
}

// NewLogStore creates a new log store based on the configuration.
// When ObjectStorage is configured, the returned store is wrapped with a
// HybridLogStore that offloads payloads to S3-compatible object storage.
func NewLogStore(ctx context.Context, config *Config, logger schemas.Logger) (LogStore, error) {
	if config == nil {
		return nil, fmt.Errorf("logstore: config is nil")
	}

	var inner LogStore
	var err error

	switch config.Type {
	case LogStoreTypeSQLite:
		if sqliteConfig, ok := config.Config.(*SQLiteConfig); ok {
			inner, err = newSqliteLogStore(ctx, sqliteConfig, logger)
		} else {
			return nil, fmt.Errorf("invalid sqlite config: %T", config.Config)
		}
	case LogStoreTypePostgres:
		if postgresConfig, ok := config.Config.(*PostgresConfig); ok {
			inner, err = newPostgresLogStore(ctx, postgresConfig, logger)
		} else {
			return nil, fmt.Errorf("invalid postgres config: %T", config.Config)
		}
	case LogStoreTypeClickHouse:
		if clickhouseConfig, ok := config.Config.(*ClickHouseConfig); ok {
			inner, err = newClickHouseLogStore(ctx, clickhouseConfig, config.RetentionDays, logger)
		} else {
			return nil, fmt.Errorf("invalid clickhouse config: %T", config.Config)
		}
	default:
		return nil, fmt.Errorf("unsupported log store type: %s", config.Type)
	}
	if err != nil {
		return nil, err
	}

	// Optionally wrap with hybrid decorator for object storage offloading.
	if config.ObjectStorage != nil {
		objStore, objErr := objectstore.NewObjectStore(ctx, config.ObjectStorage, logger)
		if objErr != nil {
			_ = inner.Close(ctx)
			return nil, fmt.Errorf("failed to create object store: %w", objErr)
		}
		if err := objStore.Ping(ctx); err != nil {
			_ = objStore.Close()
			_ = inner.Close(ctx)
			return nil, fmt.Errorf("failed to ping object store: %w", err)
		}
		return newHybridLogStore(inner, objStore, config.ObjectStorage.GetPrefix(), logger, config.ObjectStorageExcludeFields), nil
	}
	return inner, nil
}
