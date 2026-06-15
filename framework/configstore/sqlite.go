package configstore

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

// newSqliteConfigStore creates a new SQLite config store.
func newSqliteConfigStore(ctx context.Context, config *SQLiteConfig, logger schemas.Logger) (ConfigStore, error) {
	if _, err := os.Stat(config.Path); os.IsNotExist(err) {
		// Create DB file
		f, err := os.Create(config.Path)
		if err != nil {
			return nil, err
		}
		_ = f.Close()
	}
	dsn := fmt.Sprintf("%s?_journal_mode=WAL&_synchronous=NORMAL&_cache_size=10000&_busy_timeout=60000&_wal_autocheckpoint=1000&_foreign_keys=1", config.Path)
	logger.Debug("opening DB with dsn: %s", dsn)
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: newGormLogger(logger),
	})

	if err != nil {
		return nil, err
	}
	logger.Debug("db opened for configstore")
	s := &RDBConfigStore{logger: logger}
	s.db.Store(db)
	// SQLite has no server-side prepared-plan cache, and opening a second
	// handle on the same file would contend for the single-writer lock —
	// so both hooks operate on the existing *gorm.DB.
	s.migrateOnFreshFn = func(ctx context.Context, fn func(context.Context, *gorm.DB) error) error {
		return fn(ctx, s.DB())
	}
	s.refreshPoolFn = func(ctx context.Context) error { return nil }

	logger.Debug("running migration to remove duplicate keys")
	// Run migration to remove duplicate keys before AutoMigrate
	if err := s.removeDuplicateKeysAndNullKeys(ctx); err != nil {
		return nil, fmt.Errorf("failed to remove duplicate keys: %w", err)
	}
	// Run migrations
	if err := triggerMigrations(ctx, db); err != nil {
		return nil, err
	}
	// Encrypt any plaintext rows if encryption is enabled
	if err := s.EncryptPlaintextRows(ctx); err != nil {
		return nil, fmt.Errorf("failed to encrypt plaintext rows: %w", err)
	}
	return s, nil
}
