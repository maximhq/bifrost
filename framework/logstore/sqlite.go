package logstore

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/storehooks"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// SQLiteConfig represents the configuration for a SQLite database.
type SQLiteConfig struct {
	Path string `json:"path"`
}

// newSqliteLogStore creates a new SQLite log store.
func newSqliteLogStore(ctx context.Context, config *SQLiteConfig, logger schemas.Logger) (*RDBLogStore, error) {
	// The driver reads everything after a "?" in the path as connection options, so a path
	// containing one would open a different file from the one named here.
	if strings.Contains(config.Path, "?") {
		return nil, fmt.Errorf("sqlite path %q must not contain \"?\"", config.Path)
	}
	// A path written as a SQLite URI ("file:name.db") is two different files: the one created
	// below would be literally "file:name.db", while the driver opens "name.db" and creates it
	// with its own default mode. The database must be the file made owner-only here.
	if len(config.Path) >= 5 && strings.EqualFold(config.Path[:5], "file:") {
		return nil, fmt.Errorf("sqlite path %q must be a plain filesystem path, not a file: URI", config.Path)
	}
	if _, err := os.Stat(config.Path); os.IsNotExist(err) {
		// Create the DB file readable and writable by the process owner only: it
		// holds credentials and request data. SQLite gives its -wal and -shm files
		// the mode of the database file. An existing file keeps its mode.
		f, err := os.OpenFile(config.Path, os.O_RDWR|os.O_CREATE, 0o600)
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
	storehooks.PoolOpened(storehooks.RoleLogs, db)

	s := &RDBLogStore{db: db, logger: logger}
	// Run migrations
	if err := triggerMigrations(ctx, db, logger); err != nil {
		return nil, err
	}

	return s, nil
}
