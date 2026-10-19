package logstore

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/require"
	gormsqlite "gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestIsTransientWriteError(t *testing.T) {
	transient := map[string]error{
		"context canceled":                    context.Canceled,
		"context deadline":                    fmt.Errorf("write logs: %w", context.DeadlineExceeded),
		"bad conn":                            driver.ErrBadConn,
		"eof":                                 io.EOF,
		"unexpected eof":                      io.ErrUnexpectedEOF,
		"net op error":                        &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED},
		"pgx connect error":                   &pgconn.ConnectError{},
		"connection exception":                &pgconn.PgError{Code: "08006"},
		"deadlock detected":                   &pgconn.PgError{Code: "40P01"},
		"too many connections":                &pgconn.PgError{Code: "53300"},
		"admin shutdown":                      &pgconn.PgError{Code: "57P01"},
		"statement timeout":                   &pgconn.PgError{Code: "57014"},
		"system io error":                     &pgconn.PgError{Code: "58030"},
		"wrapped pg error":                    fmt.Errorf("batch: %w", &pgconn.PgError{Code: "08003"}),
		"lock not available":                  &pgconn.PgError{Code: "55P03"},
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
		"nil":                       nil,
		"plain":                     errors.New("something odd"),
		"pgx parameter limit":       errors.New("extended protocol limited to 65535 parameters"),
		"invalid byte sequence":     &pgconn.PgError{Code: "22021"},
		"string too long":           &pgconn.PgError{Code: "22001"},
		"not null violation":        &pgconn.PgError{Code: "23502"},
		"unique violation":          &pgconn.PgError{Code: "23505"},
		"undefined column":          &pgconn.PgError{Code: "42703"},
		"short code":                &pgconn.PgError{Code: "5"},
		"object in use":             &pgconn.PgError{Code: "55006"},
		"cant change runtime param": &pgconn.PgError{Code: "55P02"},
		"sqlite constraint":         sqlite3.Error{Code: sqlite3.ErrConstraint},
		"sqlite too big":            sqlite3.Error{Code: sqlite3.ErrTooBig},
		"sqlite generic error":      sqlite3.Error{Code: sqlite3.ErrError},
		"sqlite constraint unique extended errno": sqlite3.ErrConstraintUnique,
	}
	for name, err := range rowLevel {
		require.False(t, IsTransientWriteError(err), name)
	}
}

func TestIsPostgresStatementTimeoutError(t *testing.T) {
	require.True(t, IsPostgresStatementTimeoutError(&pgconn.PgError{Code: "57014"}))
	require.True(t, IsPostgresStatementTimeoutError(fmt.Errorf("batch: %w", &pgconn.PgError{Code: "57014"})))
	// Still transient: callers that do not split must at least not fan out per row.
	require.True(t, IsTransientWriteError(&pgconn.PgError{Code: "57014"}))

	require.False(t, IsPostgresStatementTimeoutError(nil))
	require.False(t, IsPostgresStatementTimeoutError(&pgconn.PgError{Code: "57P01"}))
	require.False(t, IsPostgresStatementTimeoutError(&pgconn.PgError{Code: "55P03"}))
	require.False(t, IsPostgresStatementTimeoutError(context.DeadlineExceeded))
	require.False(t, IsPostgresStatementTimeoutError(errors.New("57014")))
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
