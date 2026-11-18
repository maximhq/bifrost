package logstore

import (
	"context"
	"fmt"
	"os"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/storehooks"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// SQLiteConfig represents the configuration for a SQLite database.
type SQLiteConfig struct {
	Path string `json:"path"`
}

// sqliteBusyTimeoutMs is how long a connection waits for a lock before failing.
const sqliteBusyTimeoutMs = 60000

// sqliteDSN is the connection string every pooled connection opens with.
// Anything set here runs on each new connection, so it must be cheap and must
// not need a write lock; one-time database settings belong in
// initSQLiteAutoVacuum instead.
func sqliteDSN(path string, busyTimeoutMs int) string {
	return fmt.Sprintf("%s?_journal_mode=WAL&_synchronous=NORMAL&_cache_size=10000&_busy_timeout=%d&_wal_autocheckpoint=1000&_foreign_keys=1", path, busyTimeoutMs)
}

// initSQLiteAutoVacuum sets auto_vacuum=INCREMENTAL on a database that has no
// pages yet, so a new logs.db can later give freed pages back to the OS through
// ReclaimSpace. It runs once, on a dedicated connection, before migrations. A
// database that already has content keeps its mode (NONE or FULL); moving a
// NONE database to INCREMENTAL needs a one-off VACUUM.
func initSQLiteAutoVacuum(path string) error {
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{Logger: gormlogger.Discard})
	if err != nil {
		return err
	}
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	defer sqlDB.Close()
	sqlDB.SetMaxOpenConns(1)

	var pages int64
	if err := db.Raw("PRAGMA page_count").Scan(&pages).Error; err != nil {
		return err
	}
	if pages > 0 {
		return nil
	}
	// The pragma alone writes the header of the empty file, so the mode is
	// stored without creating anything that an interrupted start could leave
	// behind.
	return db.Exec("PRAGMA auto_vacuum = INCREMENTAL").Error
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
	if err := initSQLiteAutoVacuum(config.Path); err != nil {
		return nil, fmt.Errorf("failed to initialise sqlite auto_vacuum: %w", err)
	}
	dsn := sqliteDSN(config.Path, sqliteBusyTimeoutMs)
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
