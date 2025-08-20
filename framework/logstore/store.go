package logstore

import (
	"fmt"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// LogStoreType represents the type of log store.
type LogStoreType string

// LogStoreTypeSQLite is the type of log store for SQLite.
const (
	LogStoreTypeSQLite LogStoreType = "sqlite"
)

// LogStore is the interface for the log store.
type LogStore interface {
	Create(entry *Log) error
	FindFirst(query any, fields ...string) (*Log, error)
	FindAll(query any, fields ...string) ([]*Log, error)
	SearchLogs(filters SearchFilters, pagination PaginationOptions) (*SearchResult, error)
	Update(id string, entry any) error
	CleanupLogs(since time.Time) error
}

// NewLogStore creates a new log store based on the configuration.
func NewLogStore(config *Config, logger schemas.Logger) (LogStore, error) {
	switch config.Type {
	case LogStoreTypeSQLite:
		if sqliteConfig, ok := config.Config.(*SQLiteConfig); ok {
			return newSqliteLogStore(sqliteConfig, logger)
		}
		return nil, fmt.Errorf("invalid sqlite config: %T", config.Config)
	default:
		return nil, fmt.Errorf("unsupported log store type: %s", config.Type)
	}
}
