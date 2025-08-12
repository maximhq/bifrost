package logstore

import (
	"fmt"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

const (
	LogStoreTypeSqlite = "sqlite"
)

var logger = bifrost.NewDefaultLogger(schemas.LogLevelInfo)

type LogStore interface {
	Insert(entry *Log) error
	Get(query any, fields ...string) (*Log, error)
	SearchLogs(filters SearchFilters, pagination PaginationOptions) (*SearchResult, error)
	Update(id string, entry any) error
	CleanupLogs(since time.Time) error
}

func NewLogStore(config *Config) (LogStore, error) {
	switch config.Type {
	case LogStoreTypeSqlite:
		if sqliteConfig, ok := config.Config.(SQLiteConfig); ok {
			return newSqliteLogStore(&sqliteConfig)
		}
		return nil, fmt.Errorf("invalid sqlite config: %T", config.Config)
	default:
		return nil, fmt.Errorf("unsupported log store type: %s", config.Type)
	}
}
