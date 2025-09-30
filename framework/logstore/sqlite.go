package logstore

import (
	"context"
	"fmt"

	"github.com/maximhq/bifrost/core/schemas"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// SQLiteConfig represents the configuration for a SQLite database.
type SQLiteConfig struct {
	Path string `json:"path"`
}

// newSqliteLogStore creates a new SQLite log store.
func newSqliteLogStore(ctx context.Context, config *SQLiteConfig, logger schemas.Logger) (*RDBLogStore, error) {
	// Configure SQLite with proper settings to handle concurrent access
	dsn := fmt.Sprintf("%s?_journal_mode=WAL&_synchronous=NORMAL&_cache_size=10000&_busy_timeout=60000&_wal_autocheckpoint=1000", config.Path)
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		return nil, err
	}
	if err := db.WithContext(ctx).AutoMigrate(&Log{}); err != nil {
		return nil, err
	}
	return &RDBLogStore{db: db, logger: logger}, nil
}
