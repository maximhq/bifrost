package logstore

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/postgresconn"
	"gorm.io/gorm"
)

const (
	// DefaultMatViewPauseCancelAfter is how long a migration waits for an
	// in-flight materialized-view refresh pass to finish on its own before
	// cancelling it. A REFRESH ... CONCURRENTLY over a very large logs table can
	// run for many minutes, and while it runs it holds a snapshot that every
	// CREATE INDEX CONCURRENTLY in the database waits on and an ACCESS SHARE lock
	// that every ALTER TABLE logs times out behind.
	DefaultMatViewPauseCancelAfter = 60 * time.Second

	// matViewPausePollInterval is how often the pause retries the refresh lock.
	matViewPausePollInterval = 2 * time.Second

	// matViewPauseLogInterval throttles the log line describing the refresher.
	matViewPauseLogInterval = 30 * time.Second
)

// MatViewPauseOptions tunes PauseMatViewMaintenance.
type MatViewPauseOptions struct {
	// CancelAfter is how long to wait for a running refresh pass before
	// cancelling its REFRESH statements. Zero or negative never cancels: the
	// pause then waits for the pass to finish on its own.
	CancelAfter time.Duration
}

// cancelRefreshSQL cancels the given backend only while it is still running a
// REFRESH MATERIALIZED VIEW, re-checking state and query in the same statement
// so a refresher that has moved on to other work in the meantime is left alone.
const cancelRefreshSQL = `
SELECT pg_cancel_backend(pid)
FROM pg_stat_activity
WHERE pid = $1 AND state = 'active' AND ltrim(query) ILIKE 'REFRESH MATERIALIZED VIEW%'`

// PauseMatViewMaintenance stops every replica from starting a materialized-view
// refresh pass until release is called. Migrations call it so a long refresh
// cannot block their DDL.
//
// It works across versions because it holds the same advisory lock
// (matviewRefreshAdvisoryLockKey) that every refresher, including older releases,
// try-locks before a pass and skips the pass when it cannot get it. A pass that
// is already running is waited for; after opts.CancelAfter its REFRESH statements
// are cancelled with pg_cancel_backend. A cancelled REFRESH ... CONCURRENTLY rolls
// back and the view keeps its previous contents. The refresher moves on to its
// next view while still holding the lock, so the cancel repeats on each poll until
// the pass ends and the lock frees. Sessions are never terminated, and a holder
// that is not running a REFRESH is never cancelled.
//
// Non-Postgres stores have no materialized views, so this is a no-op there. The
// wait is bounded by ctx; callers should treat an error as "proceed without the
// pause" rather than fail their migrations.
func PauseMatViewMaintenance(ctx context.Context, db *gorm.DB, logger schemas.Logger, opts MatViewPauseOptions) (release func(), err error) {
	noop := func() {}
	if db == nil || db.Dialector.Name() != "postgres" {
		return noop, nil
	}
	sqlDB, err := db.DB()
	if err != nil {
		return noop, fmt.Errorf("matview pause: get sql.DB: %w", err)
	}
	conn, err := sqlDB.Conn(ctx)
	if err != nil {
		return noop, fmt.Errorf("matview pause: dedicated connection: %w", err)
	}
	// discard drops the physical connection so Postgres ends the session and
	// releases any advisory lock it may hold; sql.Conn.Close alone would return
	// the session, lock and all, to the pool.
	discard := func() {
		_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		_ = conn.Close()
	}

	started := time.Now()
	var lastLogged time.Time
	cancelFailedLogged := false
	for {
		// Bound the try-lock separately from ctx: if ctx ended while the reply was
		// in flight the server could hold the lock with no unlock registered.
		lockCtx, cancelLock := context.WithTimeout(context.WithoutCancel(ctx), matViewUnlockTimeout)
		var acquired bool
		lockErr := conn.QueryRowContext(lockCtx, "SELECT pg_try_advisory_lock($1)", matviewRefreshAdvisoryLockKey).Scan(&acquired)
		cancelLock()
		if lockErr != nil {
			discard()
			return noop, fmt.Errorf("matview pause: try advisory lock: %w", lockErr)
		}
		if acquired {
			if waited := time.Since(started); waited > matViewPausePollInterval && logger != nil {
				logger.Info("[logstore] materialized-view refreshes paused for migrations after waiting %s", waited.Round(time.Second))
			}
			var once sync.Once
			return func() { once.Do(func() { releaseMatViewPause(ctx, conn, discard, logger) }) }, nil
		}

		lookupCtx, cancelLookup := context.WithTimeout(ctx, 5*time.Second)
		holder, _ := postgresconn.FindAdvisoryLockHolder(lookupCtx, conn, matviewRefreshAdvisoryLockKey)
		cancelLookup()
		waited := time.Since(started)
		if logger != nil && time.Since(lastLogged) >= matViewPauseLogInterval {
			lastLogged = time.Now()
			logger.Info("[logstore] waiting %s for a materialized-view refresh pass to finish before migrating (held by %s)",
				waited.Round(time.Second), describeRefreshHolder(holder))
		}
		if holder != nil && opts.CancelAfter > 0 && waited >= opts.CancelAfter {
			cancelCtx, cancelStmt := context.WithTimeout(ctx, 5*time.Second)
			var cancelled bool
			cErr := conn.QueryRowContext(cancelCtx, cancelRefreshSQL, holder.PID).Scan(&cancelled)
			cancelStmt()
			switch {
			case cErr == nil && cancelled:
				if logger != nil {
					logger.Warn("[logstore] cancelled a materialized-view refresh (pid %d, %s) that blocked migrations for %s; the view keeps its previous contents and is refreshed again after migrations",
						holder.PID, holder.ClientAddr, waited.Round(time.Second))
				}
			case cErr != nil && !errors.Is(cErr, sql.ErrNoRows) && !cancelFailedLogged:
				// Typically a missing privilege (pg_cancel_backend needs the same role
				// or pg_signal_backend). Keep waiting for the pass to end on its own.
				cancelFailedLogged = true
				if logger != nil {
					logger.Warn("[logstore] could not cancel materialized-view refresh pid %d (%v); waiting for it to finish", holder.PID, cErr)
				}
			}
		}
		select {
		case <-ctx.Done():
			_ = conn.Close()
			return noop, fmt.Errorf("matview pause: gave up after %s waiting for refresh lock: %w", waited.Round(time.Second), ctx.Err())
		case <-time.After(matViewPausePollInterval):
		}
	}
}

// releaseMatViewPause unlocks the refresh lock on the pause connection. When the
// unlock cannot be confirmed the connection is discarded instead, so a leaked
// session lock cannot stop every replica's refreshes for good.
func releaseMatViewPause(ctx context.Context, conn *sql.Conn, discard func(), logger schemas.Logger) {
	unlockCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), matViewUnlockTimeout)
	defer cancel()
	if _, err := conn.ExecContext(unlockCtx, "SELECT pg_advisory_unlock($1)", matviewRefreshAdvisoryLockKey); err != nil {
		if logger != nil {
			logger.Warn("[logstore] could not confirm matview refresh unlock (%v); discarding the connection to release it", err)
		}
		discard()
		return
	}
	_ = conn.Close()
}

// describeRefreshHolder renders the refresh-lock holder for the wait log.
func describeRefreshHolder(h *postgresconn.AdvisoryLockHolder) string {
	if h == nil {
		return "a session that has since released it"
	}
	who := fmt.Sprintf("pid %d", h.PID)
	if h.ClientAddr != "" {
		who += " from " + h.ClientAddr
	}
	if h.State == "active" {
		return fmt.Sprintf("%s running %q", who, truncateRunes(strings.Join(strings.Fields(h.Query), " "), 120))
	}
	return who + " (" + h.State + ")"
}

// matViewPauseWaitBudget bounds how long migrations wait to pause refreshes. If a
// pass can be neither waited out nor cancelled within it (for example the role
// lacks the privilege to cancel), migrations proceed without the pause, which is
// exactly how they behaved before the pause existed.
const matViewPauseWaitBudget = 10 * time.Minute

// PauseMatViewsForMigrations is PauseMatViewMaintenance for migration runners: it
// bounds the wait and never fails the caller. On any error it logs a warning and
// returns a no-op release so migrations carry on unpaused.
func PauseMatViewsForMigrations(ctx context.Context, db *gorm.DB, logger schemas.Logger, opts MatViewPauseOptions) (release func()) {
	waitCtx, cancel := context.WithTimeout(ctx, matViewPauseWaitBudget)
	defer cancel()
	release, err := PauseMatViewMaintenance(waitCtx, db, logger, opts)
	if err != nil {
		if logger != nil {
			logger.Warn("[logstore] running migrations without pausing materialized-view refreshes: %v", err)
		}
		return func() {}
	}
	return release
}

// resolveMigrationMatViewCancelAfter parses migration_matview_cancel_after: empty
// takes DefaultMatViewPauseCancelAfter, "off" or a non-positive duration disables
// cancelling (the pause then only waits), and an invalid value logs a warning and
// falls back to the default.
func resolveMigrationMatViewCancelAfter(raw string, logger schemas.Logger) time.Duration {
	if raw == "" {
		return DefaultMatViewPauseCancelAfter
	}
	if raw == "off" {
		return 0
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		if logger != nil {
			logger.Warn("logstore: invalid migration_matview_cancel_after %q (%s); using default %s", raw, err, DefaultMatViewPauseCancelAfter)
		}
		return DefaultMatViewPauseCancelAfter
	}
	if d < 0 {
		return 0
	}
	return d
}

// MigrationLockRetryOptions returns the lock-timeout retry options a log store
// config asks for: its migration_lock_retry_budget when cfg is a Postgres log
// store, and the package default otherwise. Downstream migrations that run
// against the log store (bifrost-enterprise) call it so one setting governs them.
func MigrationLockRetryOptions(cfg *Config, logger schemas.Logger) postgresconn.LockRetryOptions {
	if cfg != nil {
		if pg, ok := cfg.Config.(*PostgresConfig); ok && pg != nil {
			return postgresconn.MigrationLockRetryOptions(&pg.Config, logger)
		}
	}
	return postgresconn.LockRetryOptions{}
}

// MigrationMatViewPauseOptions returns the matview pause options a log store
// config asks for: its migration_matview_cancel_after when cfg is a Postgres log
// store, and the defaults otherwise. Other stores' migrations (the config store,
// enterprise tables) share the database with the log store and call this so the
// one setting governs every migration's pause.
func MigrationMatViewPauseOptions(cfg *Config, logger schemas.Logger) MatViewPauseOptions {
	if cfg != nil {
		if pg, ok := cfg.Config.(*PostgresConfig); ok && pg != nil {
			return MatViewPauseOptions{CancelAfter: resolveMigrationMatViewCancelAfter(pg.MigrationMatViewCancelAfter, logger)}
		}
	}
	return MatViewPauseOptions{CancelAfter: DefaultMatViewPauseCancelAfter}
}
