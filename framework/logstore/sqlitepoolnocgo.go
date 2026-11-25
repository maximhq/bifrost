//go:build !cgo

package logstore

import (
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// openSQLitePool opens dsn with the plain driver: the go-sqlite3 connect hook
// exists only in its cgo build, and without cgo it cannot open a database.
func openSQLitePool(dsn string) gorm.Dialector {
	return sqlite.Open(dsn)
}
