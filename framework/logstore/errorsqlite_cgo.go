//go:build cgo

package logstore

import (
	"errors"

	"github.com/mattn/go-sqlite3"
)

// isSQLiteTransientError reports whether err is SQLITE_BUSY or SQLITE_LOCKED
// (any extended variant, e.g. SQLITE_BUSY_SNAPSHOT) from the SQLite driver.
// Both mean another connection holds the lock the write needs; they say nothing
// about the rows, so the batch can be retried whole once the holder commits.
// Falling back to per-row inserts instead makes every row wait out the
// connection's busy_timeout again.
func isSQLiteTransientError(err error) bool {
	var sqliteErr sqlite3.Error
	if errors.As(err, &sqliteErr) {
		return sqliteErr.Code == sqlite3.ErrBusy || sqliteErr.Code == sqlite3.ErrLocked
	}
	var code sqlite3.ErrNo
	if errors.As(err, &code) {
		return code == sqlite3.ErrBusy || code == sqlite3.ErrLocked
	}
	// An extended code carries its primary code in the low byte (ErrNo.Extend).
	var extended sqlite3.ErrNoExtended
	if errors.As(err, &extended) {
		base := sqlite3.ErrNo(int(extended) & 0xff)
		return base == sqlite3.ErrBusy || base == sqlite3.ErrLocked
	}
	return false
}
