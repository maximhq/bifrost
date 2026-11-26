//go:build cgo

package logstore

import (
	"database/sql"
	"fmt"
	"sync"

	sqlite3 "github.com/mattn/go-sqlite3"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

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
