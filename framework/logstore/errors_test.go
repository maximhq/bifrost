package logstore

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"net"
	"syscall"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

func TestIsTransientWriteError(t *testing.T) {
	transient := map[string]error{
		"context canceled":     context.Canceled,
		"context deadline":     fmt.Errorf("write logs: %w", context.DeadlineExceeded),
		"bad conn":             driver.ErrBadConn,
		"eof":                  io.EOF,
		"unexpected eof":       io.ErrUnexpectedEOF,
		"net op error":         &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED},
		"pgx connect error":    &pgconn.ConnectError{},
		"connection exception": &pgconn.PgError{Code: "08006"},
		"deadlock detected":    &pgconn.PgError{Code: "40P01"},
		"too many connections": &pgconn.PgError{Code: "53300"},
		"admin shutdown":       &pgconn.PgError{Code: "57P01"},
		"statement timeout":    &pgconn.PgError{Code: "57014"},
		"system io error":      &pgconn.PgError{Code: "58030"},
		"wrapped pg error":     fmt.Errorf("batch: %w", &pgconn.PgError{Code: "08003"}),
		"lock not available":   &pgconn.PgError{Code: "55P03"},
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
