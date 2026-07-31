package migrator

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/maximhq/bifrost/core/schemas"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// addColumnLockTimeout bounds how long a single attempt waits to acquire the
// ACCESS EXCLUSIVE lock ADD COLUMN needs on the target table. Postgres queues
// lock requests FIFO per relation: once this request is queued, only
// transactions that were ALREADY open at that instant can make it wait - new
// writes queue behind it, they don't compete with it. On a table fed by
// short, autocommitted statements (e.g. single-row log inserts), that
// currently-open set drains in milliseconds, so a short timeout retried
// often finds a grant quickly. A long timeout would instead hold up every
// new query behind it for the full timeout on every failed attempt.
const addColumnLockTimeout = 500 * time.Millisecond

// addColumnLockBudget is the total time to keep retrying before giving up
// loudly. A wait that never resolves within this window means something is
// holding an open transaction on the table well beyond a normal statement
// (idle-in-transaction connection, long scan, stuck prior migration) - a
// real operational problem that should surface as a failed deploy with a
// blocker to go kill, not an indefinitely hanging boot.
const addColumnLockBudget = 3 * time.Minute

// addColumnLockBackoffMin and addColumnLockBackoffMax bound the jittered
// delay between attempts. Jitter keeps multiple pods retrying the same ALTER
// (rolling deploy) from re-synchronizing on the same cadence and repeatedly
// colliding on the same instant.
const (
	addColumnLockBackoffMin = 250 * time.Millisecond
	addColumnLockBackoffMax = 750 * time.Millisecond
)

// isLockNotAvailable reports whether err is Postgres SQLSTATE 55P03
// (lock_not_available), which is what lock_timeout produces when it expires.
func isLockNotAvailable(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return pgErr.Code == "55P03"
}

// describeLockHolders reports sessions currently holding a lock on table, for
// diagnostics on final failure. It is best-effort: if the query itself fails
// (e.g. insufficient privilege), it returns a short note rather than an error,
// since the caller is already returning the real failure.
func describeLockHolders(ctx context.Context, tx *gorm.DB, table string) string {
	type lockHolder struct {
		PID             int64 `gorm:"column:pid"`
		Usename         string
		ApplicationName string
		ClientAddr      string
		State           string
	}
	var holders []lockHolder
	err := tx.WithContext(ctx).Raw(`
		SELECT l.pid,
		       COALESCE(a.usename, '') AS usename,
		       COALESCE(a.application_name, '') AS application_name,
		       COALESCE(host(a.client_addr), '') AS client_addr,
		       COALESCE(a.state, '') AS state
		FROM pg_locks l
		JOIN pg_class c ON c.oid = l.relation
		LEFT JOIN pg_stat_activity a ON a.pid = l.pid
		WHERE c.relname = ? AND l.locktype = 'relation' AND l.pid <> pg_backend_pid() AND l.granted
	`, table).Scan(&holders).Error
	if err != nil {
		return fmt.Sprintf("\ncould not inspect lock holders on %s: %s", table, err.Error())
	}
	if len(holders) == 0 {
		return fmt.Sprintf("\nno other session currently holds a lock on %s (contention was transient)", table)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\nsessions currently holding a lock on %s (SELECT pg_terminate_backend(pid) to clear a stuck one):", table)
	for _, h := range holders {
		fmt.Fprintf(&b, "\n  pid=%d user=%s app=%s client=%s state=%s",
			h.PID, h.Usename, h.ApplicationName, h.ClientAddr, h.State)
	}
	return b.String()
}

// execWithLockTimeout runs sql inside a short-lived transaction with a
// per-attempt lock_timeout, retrying with jittered backoff whenever the wait
// for the lock times out. SET LOCAL only applies for the lifetime of the
// enclosing transaction, so it and the DDL statement must share one.
//
// Known limitation: when tx is already inside an active transaction (e.g. a
// migration run with UseTransaction: true), GORM's Transaction() opens a
// SAVEPOINT rather than an independent transaction, so the ACCESS EXCLUSIVE
// lock this statement takes is held until the OUTER migration transaction
// commits, not released after this one statement. The retry/backoff loop
// still works correctly in that case; it just no longer bounds how long
// other queries on the table are blocked to a single addColumnLockTimeout
// window per attempt.
func execWithLockTimeout(tx *gorm.DB, logger schemas.Logger, table string, sql string, args ...interface{}) error {
	ctx := context.Background()
	if tx.Statement != nil && tx.Statement.Context != nil {
		ctx = tx.Statement.Context
	}

	lockTimeout := fmt.Sprintf("%dms", addColumnLockTimeout.Milliseconds())
	deadline := time.Now().Add(addColumnLockBudget)
	var lastErr error
	attempt := 0
	for {
		attempt++
		lastErr = tx.Transaction(func(txn *gorm.DB) error {
			if err := txn.Exec("SELECT set_config('lock_timeout', ?, true)", lockTimeout).Error; err != nil {
				return err
			}
			return txn.Exec(sql, args...).Error
		})
		if lastErr == nil {
			if attempt > 1 && logger != nil {
				logger.Info("[migrator] acquired lock on table %s after %d attempts", table, attempt)
			}
			return nil
		}
		if !isLockNotAvailable(lastErr) {
			return lastErr
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("could not acquire lock on table %s after %d attempts over %s: %w%s",
				table, attempt, addColumnLockBudget, lastErr, describeLockHolders(ctx, tx, table))
		}
		if logger != nil {
			logger.Warn("[migrator] could not acquire lock on table %s within %s (attempt %d); retrying", table, addColumnLockTimeout, attempt)
		}
		backoff := addColumnLockBackoffMin + time.Duration(rand.Int63n(int64(addColumnLockBackoffMax-addColumnLockBackoffMin)))
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
	}
}

// AddColumnIfNotExists adds the column backing struct field `field` of `model`
// in a way that is idempotent at the database statement level, so concurrent
// migration runners (rolling deploys, version skew, or a duplicate side path)
// and re-runs cannot fail with a duplicate-column error (PostgreSQL SQLSTATE
// 42701).
//
// GORM's Migrator.AddColumn emits a bare `ALTER TABLE ... ADD COLUMN`, and the
// usual `if !HasColumn { AddColumn }` guard is a non-atomic check-then-act: two
// sessions can both observe the column as absent and then both issue the ADD,
// and the loser fails when its DDL finally executes. On PostgreSQL we instead
// emit `ALTER TABLE ... ADD COLUMN IF NOT EXISTS`, which is a true no-op when
// the column already exists and therefore never aborts the surrounding
// migration transaction.
//
// Other dialects (e.g. SQLite) do not support `ADD COLUMN IF NOT EXISTS`, so we
// keep the HasColumn guard and fall back to GORM's AddColumn there.
//
// The function owns the existence check, so call sites should NOT wrap it in
// their own `if !HasColumn { ... }` guard — that just adds a second, redundant
// round-trip. The single HasColumn here also lets us emit the "adding column"
// log line only when a column is actually added: pass a non-nil logger to get
// that line, or nil to stay silent.
func AddColumnIfNotExists(tx *gorm.DB, logger schemas.Logger, model interface{}, field string) error {
	mig := tx.Migrator()
	if mig.HasColumn(model, field) {
		return nil
	}

	// Replicate GORM's Migrator.AddColumn DDL with an IF NOT EXISTS guard.
	stmt := &gorm.Statement{DB: tx}
	if err := stmt.Parse(model); err != nil {
		return fmt.Errorf("failed to parse schema for %T: %w", model, err)
	}
	f := stmt.Schema.LookUpField(field)
	if f == nil {
		return fmt.Errorf("failed to look up field with name: %s", field)
	}
	if f.IgnoreMigration {
		return nil
	}

	if logger != nil {
		logger.Info("[migrator] adding column %s to table %s", f.DBName, stmt.Table)
	}

	if tx.Dialector.Name() != "postgres" {
		return mig.AddColumn(model, field)
	}
	return execWithLockTimeout(tx, logger, stmt.Table,
		"ALTER TABLE ? ADD COLUMN IF NOT EXISTS ? ?",
		clause.Table{Name: stmt.Table}, clause.Column{Name: f.DBName}, mig.FullDataTypeOf(f),
	)
}
