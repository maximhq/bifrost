package logstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/storehooks"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// SQLiteConfig represents the configuration for a SQLite database.
type SQLiteConfig struct {
	Path string `json:"path"`
}

// incrementalVacuumPages is how many free pages one PRAGMA incremental_vacuum
// statement hands back. Each statement is its own write transaction, so the log
// writer waits behind one chunk (about 4 MB at the default 4 KiB page size) at
// a time, never behind the whole freelist.
const incrementalVacuumPages = 1000

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
	if info, err := os.Stat(config.Path); err == nil && info.Size() == 0 {
		if err := requestIncrementalAutoVacuum(dsn); err != nil {
			return nil, err
		}
	}
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

// requestIncrementalAutoVacuum opens one connection that asks for incremental
// auto-vacuum before the pool opens the file. SQLite settles the mode when it
// writes a database's first page, so a new logs.db is created in it and keeps
// it for every connection. newSqliteLogStore asks only for an empty file, so an
// existing database keeps the mode it has; asking on every open would switch a
// FULL database to INCREMENTAL. The pool's DSN leaves the parameter out: there
// it would make each new pooled connection open a write transaction to restate
// the mode.
func requestIncrementalAutoVacuum(dsn string) error {
	db, err := sql.Open(sqlite.DriverName, dsn+"&_auto_vacuum=incremental")
	if err != nil {
		return err
	}
	err = db.Ping()
	return errors.Join(err, db.Close())
}

// ReclaimFreeSpace hands the pages deletes left on a SQLite database's freelist
// back to the filesystem, so logs.db shrinks after retention instead of keeping
// the largest size it reached (#9738). It drains the freelist it finds in
// chunks of incrementalVacuumPages until that is done or ctx ends, and reports
// the bytes given back. Other dialects report 0, as do SQLite databases created
// before incremental auto-vacuum, which need a one-time VACUUM to switch.
func (s *RDBLogStore) ReclaimFreeSpace(ctx context.Context) (int64, error) {
	if s.db.Dialector.Name() != "sqlite" {
		return 0, nil
	}
	db := s.db.WithContext(ctx)
	var mode, free, pageSize int64
	if err := db.Raw("PRAGMA auto_vacuum").Row().Scan(&mode); err != nil {
		return 0, err
	}
	if mode != 2 { // 0 is NONE, 1 is FULL (which keeps no freelist), 2 is INCREMENTAL
		return 0, nil
	}
	if err := db.Raw("PRAGMA freelist_count").Row().Scan(&free); err != nil {
		return 0, err
	}
	if err := db.Raw("PRAGMA page_size").Row().Scan(&pageSize); err != nil {
		return 0, err
	}
	var freed int64
	for ; free > 0 && ctx.Err() == nil; free -= incrementalVacuumPages {
		pages, err := incrementalVacuum(db, incrementalVacuumPages)
		if err != nil {
			return freed * pageSize, err
		}
		freed += pages
	}
	if freed > 0 && ctx.Err() == nil {
		// In WAL mode the vacuum truncates the logical database. logs.db shrinks only
		// when a checkpoint writes that truncation back. PASSIVE does not block writers
		// or wait for readers, so busy readers can leave part of the WAL for later.
		var busy, walFrames, checkpointed int64
		if err := db.Raw("PRAGMA wal_checkpoint(PASSIVE)").Row().Scan(&busy, &walFrames, &checkpointed); err != nil {
			return freed * pageSize, err
		}
		s.logger.Debug("passive checkpoint after free-space reclaim: busy=%d wal_frames=%d checkpointed=%d", busy, walFrames, checkpointed)
	}
	return freed * pageSize, ctx.Err()
}

// incrementalVacuum runs one PRAGMA incremental_vacuum and returns the pages it
// freed. The pragma yields a row per freed page and the driver's Exec steps a
// statement once, so Exec would free a single page; the rows are read instead.
func incrementalVacuum(db *gorm.DB, pages int) (int64, error) {
	rows, err := db.Raw(fmt.Sprintf("PRAGMA incremental_vacuum(%d)", pages)).Rows()
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var freed int64
	for rows.Next() {
		freed++
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	return freed, nil
}
