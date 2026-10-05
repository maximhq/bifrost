package logstore

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"net"

	"github.com/jackc/pgx/v5/pgconn"
)

var (
	ErrNotFound    = fmt.Errorf("log not found")
	ErrJobInternal = fmt.Errorf("internal job store error")
	// ErrInvalidWebhookReference marks a submit whose webhook endpoint
	// reference is unusable — a caller mistake, not a server failure.
	ErrInvalidWebhookReference = fmt.Errorf("invalid webhook endpoint reference")
)

// postgresSQLStateError is the subset of *pgconn.PgError (and any other
// Postgres driver error carrying a SQLSTATE) the classifiers below need.
// Matching on the interface rather than the concrete pgx type lets callers in
// other modules build such an error without importing pgx.
type postgresSQLStateError interface {
	SQLState() string
}

// postgresSQLStateOf returns the five-character SQLSTATE carried by err, or ""
// for errors from other drivers (SQLite and ClickHouse errors carry numeric
// codes, not SQLSTATEs).
func postgresSQLStateOf(err error) string {
	var se postgresSQLStateError
	if errors.As(err, &se) {
		return se.SQLState()
	}
	return ""
}

// postgresSQLStateStatementTimeout is query_canceled: statement_timeout fired
// or the query was cancelled server-side. A smaller statement can fit the same
// budget.
const postgresSQLStateStatementTimeout = "57014"

// postgresSQLStateLockNotAvailable is lock_not_available: lock_timeout fired
// while waiting on a lock another session holds. Retrying later, once the lock
// is released, can succeed; retrying each row now only waits on the same lock.
const postgresSQLStateLockNotAvailable = "55P03"

// IsPostgresStatementTimeoutError reports whether err is PostgreSQL
// query_canceled (SQLSTATE 57014), which is what statement_timeout raises. It
// is Postgres-specific: SQLite has no statement timeout of this kind and
// ClickHouse signals one with its own exception code, so it reports false for
// those stores. It is also a transient error (IsTransientWriteError reports
// true), but it deserves its own recovery: the same statement will usually time
// out again, while a smaller write of the same rows may finish within the
// budget, so callers split the batch instead of retrying or dropping it whole.
func IsPostgresStatementTimeoutError(err error) bool {
	return postgresSQLStateOf(err) == postgresSQLStateStatementTimeout
}

// isPostgresTransientError reports whether err is a Postgres driver or server
// error that describes the connection or the server's state rather than the
// rows being written: a failed connect, a pgx-level timeout, lock_not_available,
// or a SQLSTATE in the connection, rollback, resource, operator-intervention or
// system-error classes. Errors from other drivers report false here.
func isPostgresTransientError(err error) bool {
	var connErr *pgconn.ConnectError
	if errors.As(err, &connErr) {
		return true
	}
	if pgconn.Timeout(err) {
		return true
	}
	code := postgresSQLStateOf(err)
	if code == postgresSQLStateLockNotAvailable {
		// Only this member of class 55 (object_not_in_prerequisite_state) is
		// transient; the rest describe objects that will not change on retry.
		return true
	}
	if len(code) >= 2 {
		switch code[:2] {
		case "08", // connection exception
			"40", // transaction rollback (serialization failure, deadlock detected)
			"53", // insufficient resources (too many connections, out of memory, disk full)
			"57", // operator intervention (admin shutdown, cannot connect now, query cancelled)
			"58": // system error (I/O error)
			return true
		}
	}
	return false
}

// IsTransientWriteError reports whether a failed batch write failed for a
// reason that has nothing to do with the rows in it: the connection dropped,
// the server was unreachable or shutting down, the statement timed out or
// waited too long for a lock, or the context was cancelled. Retrying each row
// of the batch on its own cannot help with any of these; it only multiplies the
// failed statements by the batch size. Callers use it to decide between
// retrying the batch whole and the per-row fallback that isolates a single bad
// row.
//
// Coverage by store: the context, net.Error, driver.ErrBadConn and EOF checks
// apply to every SQL driver. The server-side classification (SQLSTATE classes,
// lock_timeout, pgx connect and timeout errors) is Postgres only, via
// isPostgresTransientError; SQLite's busy/locked codes and ClickHouse exception
// codes are not recognised, so on those stores only the driver-agnostic checks
// fire and everything else takes the row-isolating fallback.
//
// Anything not recognised here (including data errors such as SQLSTATE class
// 22 and constraint violations in class 23, serialization failures from the
// JSON column types, and the pgx parameter-limit error) reports false, so the
// row-isolating fallback remains the default.
func IsTransientWriteError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, driver.ErrBadConn) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	return isPostgresTransientError(err)
}
