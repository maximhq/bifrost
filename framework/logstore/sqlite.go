package logstore

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"sync"
	"time"

	sqlite3 "github.com/mattn/go-sqlite3"
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

// sqliteJournalSizeLimit is the size logs.db-wal is cut back to after a checkpoint resets it.
const sqliteJournalSizeLimit = 64 << 20

// sqliteDriverName is the driver the log store pool opens with; it is
// go-sqlite3 plus sqliteConnectHook.
const sqliteDriverName = "sqlite3_bifrost_logstore"

var registerSQLiteDriver sync.Once

// sqliteConnectHook sets what the go-sqlite3 DSN cannot carry. The DSN rule
// applies here too: it runs on every new pooled connection, so it must be cheap
// and must not need a write lock.
//
// journal_size_limit is per connection, and SQLite applies it in the
// connection that resets the WAL. Without it the WAL never shrinks: a reclaim
// that runs next to readers and writers can grow logs.db-wal to hundreds of
// MB, and SQLite keeps reusing the file at that size until the last
// connection closes. With it, the first writer after a completed checkpoint
// cuts the file back to the limit. Setting it only stores a number on the
// connection; it reads and writes nothing.
func sqliteConnectHook(conn *sqlite3.SQLiteConn) error {
	_, err := conn.Exec(fmt.Sprintf("PRAGMA journal_size_limit = %d", sqliteJournalSizeLimit), nil)
	return err
}

// openSQLitePool opens dsn through the go-sqlite3 driver with sqliteConnectHook.
func openSQLitePool(dsn string) gorm.Dialector {
	registerSQLiteDriver.Do(func() {
		sql.Register(sqliteDriverName, &sqlite3.SQLiteDriver{ConnectHook: sqliteConnectHook})
	})
	return sqlite.New(sqlite.Config{DriverName: sqliteDriverName, DSN: dsn})
}

// sqliteDSN is the connection string every pooled connection opens with.
// Anything set here runs on each new connection, so it must be cheap and must
// not need a write lock; one-time database settings belong in
// initSQLiteAutoVacuum instead. go-sqlite3 ignores DSN keys it does not know,
// so a pragma it has no key for goes in sqliteConnectHook.
func sqliteDSN(path string, busyTimeoutMs int) string {
	return fmt.Sprintf("%s?_journal_mode=WAL&_synchronous=NORMAL&_cache_size=10000&_busy_timeout=%d&_wal_autocheckpoint=1000&_foreign_keys=1", path, busyTimeoutMs)
}

// initSQLiteAutoVacuum makes sure logs.db runs with auto_vacuum=INCREMENTAL so
// freed pages can go back to the OS through ReclaimSpace. It runs once, on a
// dedicated connection, before migrations. An empty file only needs the pragma.
// An existing file in the default NONE mode is converted with a one-off VACUUM,
// which rewrites the file and needs free disk space about the size of the
// database; if that fails the file keeps its mode and the next start retries. A
// file already in FULL or INCREMENTAL mode is left alone. Cancelling ctx stops
// the VACUUM and returns the context error.
func initSQLiteAutoVacuum(ctx context.Context, path string, logger schemas.Logger) error {
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{Logger: gormlogger.Discard})
	if err != nil {
		return err
	}
	db = db.WithContext(ctx)
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
	if pages == 0 {
		// The pragma alone writes the header of the empty file, so the mode is
		// stored without creating anything that an interrupted start could
		// leave behind.
		return db.Exec("PRAGMA auto_vacuum = INCREMENTAL").Error
	}
	var mode int
	if err := db.Raw("PRAGMA auto_vacuum").Scan(&mode).Error; err != nil {
		return err
	}
	if mode != 0 {
		return nil
	}
	logger.Info("migrating logs.db to auto_vacuum=incremental with a one-off VACUUM (%d pages); this can take a while on a large database and needs free disk space about its size", pages)
	start := time.Now()
	if err := db.Exec("PRAGMA auto_vacuum = INCREMENTAL").Error; err != nil {
		return err
	}
	if err := db.Exec("VACUUM").Error; err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		logger.Warn("logs.db auto_vacuum migration failed, will retry on next start: %v", err)
		return nil
	}
	logger.Info("logs.db auto_vacuum migration finished in %s", time.Since(start).Round(time.Millisecond))
	return nil
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
	if err := initSQLiteAutoVacuum(ctx, config.Path, logger); err != nil {
		return nil, fmt.Errorf("failed to initialise sqlite auto_vacuum: %w", err)
	}
	dsn := sqliteDSN(config.Path, sqliteBusyTimeoutMs)
	logger.Debug("opening DB with dsn: %s", dsn)
	db, err := gorm.Open(openSQLitePool(dsn), &gorm.Config{
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
