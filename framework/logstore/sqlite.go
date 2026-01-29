package logstore

import (
	"context"
	"fmt"
	"os"

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
	if _, err := os.Stat(config.Path); os.IsNotExist(err) {
		// Create DB file
		f, err := os.Create(config.Path)
		if err != nil {
			return nil, err
		}
		_ = f.Close()
	}
	// Configure SQLite with proper settings to handle concurrent access
	dsn := fmt.Sprintf("%s?_journal_mode=WAL&_synchronous=NORMAL&_cache_size=10000&_busy_timeout=60000&_wal_autocheckpoint=1000&_foreign_keys=1", config.Path)
	logger.Debug("opening DB with dsn: %s", dsn)
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: newGormLogger(logger),
	})
	if err != nil {
		return nil, err
	}
	logger.Debug("db opened for logstore")
	s := &RDBLogStore{db: db, logger: logger}
	// Run migrations
	if err := triggerMigrations(ctx, db); err != nil {
		return nil, err
	}
	return s, nil
}
