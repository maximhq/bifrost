package postgresconn

import (
	"errors"
	"strings"
)

// transientLockSQLStates are the PostgreSQL errors a write transaction can hit
// purely because another session held the rows it needed. Re-running the same
// transaction once that session finishes succeeds, so callers retry them rather
// than treating them as configuration failures.
var transientLockSQLStates = []string{
	"40P01", // deadlock_detected: Postgres aborted this side of a lock cycle.
	"40001", // serialization_failure: a concurrent write invalidated this transaction.
	"55P03", // lock_not_available: lock_timeout fired while waiting on a row or table lock.
	"57014", // query_canceled: statement_timeout fired, typically while queued behind a lock.
}

// IsTransientLockError reports whether err is a lock-contention failure that is
// expected to succeed on retry: a deadlock, a serialization failure, a lock
// timeout or a statement timeout. Like IsLockNotAvailableError it checks the
// typed SQLSTATE first and falls back to the "(SQLSTATE xxxxx)" suffix pgx
// renders, because many call sites wrap driver errors with %s and drop the type.
func IsTransientLockError(err error) bool {
	if err == nil {
		return false
	}
	var se sqlStateCarrier
	if errors.As(err, &se) {
		state := se.SQLState()
		for _, transient := range transientLockSQLStates {
			if state == transient {
				return true
			}
		}
	}
	msg := err.Error()
	for _, transient := range transientLockSQLStates {
		if strings.Contains(msg, "SQLSTATE "+transient) {
			return true
		}
	}
	return false
}
