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
	LogStoreTypeSQLite LogStoreType = "sqlite"
	LogStoreTypePostgres LogStoreType = "postgres"
)

// LogStore is the interface for the log store.
type LogStore interface {
	Create(ctx context.Context, entry *Log) error
	FindFirst(ctx context.Context, query any, fields ...string) (*Log, error)
	FindAll(ctx context.Context, query any, fields ...string) ([]*Log, error)
	SearchLogs(ctx context.Context, filters SearchFilters, pagination PaginationOptions) (*SearchResult, error)
	Update(ctx context.Context, id string, entry any) error
	Flush(ctx context.Context, since time.Time) error	
	Close(ctx context.Context) error
}

// NewLogStore creates a new log store based on the configuration.
func NewLogStore(ctx context.Context,config *Config, logger schemas.Logger) (LogStore, error) {
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
