//go:build !cgo

package logstore

// isSQLiteTransientError reports false without cgo: the SQLite driver's error
// types exist only in its cgo build, and without cgo it cannot open a database.
func isSQLiteTransientError(error) bool { return false }
