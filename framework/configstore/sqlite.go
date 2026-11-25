package configstore

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

// newSqliteConfigStore creates a new SQLite config store.
func newSqliteConfigStore(ctx context.Context, config *SQLiteConfig, logger schemas.Logger) (ConfigStore, error) {
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
	// _txlock=immediate begins every transaction holding the write lock. A deferred transaction
	// that reads and then writes has to upgrade its lock, and SQLite refuses that upgrade at once
	// ("database is locked") when another connection is writing, without waiting out the busy
	// timeout; taken at BEGIN, the lock is waited for like any other.
	dsn := fmt.Sprintf("%s?_journal_mode=WAL&_synchronous=NORMAL&_cache_size=10000&_busy_timeout=60000&_wal_autocheckpoint=1000&_foreign_keys=1&_txlock=immediate", config.Path)
	logger.Debug("opening DB with dsn: %s", dsn)
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: newGormLogger(logger),
	})

	if err != nil {
		return nil, err
	}
	logger.Debug("db opened for configstore")
	// Install the global vault store/remove callbacks so plaintext SecretVar
	// fields are rewritten to vault refs before persistence and owned vault
	// secrets are cleaned up on delete.
	RegisterVaultCallbacks(db)
	storehooks.PoolOpened(storehooks.RoleConfig, db)
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
	if err := triggerMigrations(ctx, db, logger); err != nil {
		return nil, err
	}
	// Encrypt any plaintext rows if encryption is enabled
	if err := s.EncryptPlaintextRows(ctx); err != nil {
		return nil, fmt.Errorf("failed to encrypt plaintext rows: %w", err)
	}
	return s, nil
}
