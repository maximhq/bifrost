//go:build cgo

package logstore

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/require"
	gormsqlite "gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestIsTransientWriteErrorSQLite(t *testing.T) {
	transient := map[string]error{
		"sqlite busy":                         sqlite3.Error{Code: sqlite3.ErrBusy},
		"sqlite busy snapshot":                sqlite3.Error{Code: sqlite3.ErrBusy, ExtendedCode: sqlite3.ErrBusySnapshot},
		"sqlite locked":                       sqlite3.Error{Code: sqlite3.ErrLocked},
		"wrapped sqlite busy":                 fmt.Errorf("batch: %w", sqlite3.Error{Code: sqlite3.ErrBusy}),
		"sqlite busy errno":                   sqlite3.ErrBusy,
		"sqlite busy snapshot extended errno": sqlite3.ErrBusySnapshot,
		"wrapped sqlite locked shared cache":  fmt.Errorf("batch: %w", sqlite3.ErrLockedSharedCache),
	}
	for name, err := range transient {
		require.True(t, IsTransientWriteError(err), name)
	}

	rowLevel := map[string]error{
		"sqlite constraint":                       sqlite3.Error{Code: sqlite3.ErrConstraint},
		"sqlite too big":                          sqlite3.Error{Code: sqlite3.ErrTooBig},
		"sqlite generic error":                    sqlite3.Error{Code: sqlite3.ErrError},
		"sqlite constraint unique extended errno": sqlite3.ErrConstraintUnique,
	}
	for name, err := range rowLevel {
		require.False(t, IsTransientWriteError(err), name)
	}
}

// TestIsTransientWriteErrorRealSQLiteBusy drives a real SQLITE_BUSY through
// GORM: one connection holds the write lock with BEGIN IMMEDIATE while a second
// (busy_timeout=0, so it fails at once) tries to insert. The error the logs
// writer sees for a contended SQLite writer lock must classify as transient, so
// the batch is retried whole instead of fanning out into per-row inserts that
// each wait out the busy timeout again.
func TestIsTransientWriteErrorRealSQLiteBusy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "busy.db")
	dsn := path + "?_journal_mode=WAL&_busy_timeout=0"
	holder, err := gorm.Open(gormsqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, holder.Exec("CREATE TABLE t (id TEXT PRIMARY KEY)").Error)
	holderSQL, err := holder.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = holderSQL.Close() })

	conn, err := holderSQL.Conn(context.Background())
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	_, err = conn.ExecContext(context.Background(), "BEGIN IMMEDIATE")
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = conn.ExecContext(context.Background(), "ROLLBACK") })

	writer, err := gorm.Open(gormsqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	writerSQL, err := writer.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = writerSQL.Close() })

	err = writer.Exec("INSERT INTO t (id) VALUES ('a')").Error
	require.Error(t, err)
	var sqliteErr sqlite3.Error
	require.True(t, errors.As(err, &sqliteErr), "expected a sqlite3.Error, got %T: %v", err, err)
	require.Equal(t, sqlite3.ErrBusy, sqliteErr.Code, "expected SQLITE_BUSY, got %v", err)
	require.True(t, IsTransientWriteError(err), "real SQLITE_BUSY must be transient: %v", err)
	require.False(t, IsPostgresStatementTimeoutError(err))
}
