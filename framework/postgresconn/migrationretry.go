package postgresconn

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// sqlStateLockNotAvailable is lock_not_available: a lock_timeout fired while the
// statement waited on a lock another session holds.
const sqlStateLockNotAvailable = "55P03"

const (
	// DefaultLockRetryBudget bounds how long a migration step keeps retrying after
	// lock timeouts, so a process that cannot get its table lock still exits
	// with a diagnosable error rather than hanging indefinitely.
	DefaultLockRetryBudget = 10 * time.Minute

	// lockRetryInitialBackoff and lockRetryMaxBackoff shape the wait between
	// attempts: short enough to slip in between two long readers, long enough
	// that a blocked step does not hammer the lock queue of a hot table.
	lockRetryInitialBackoff = 5 * time.Second
	lockRetryMaxBackoff     = 30 * time.Second
)

// sqlStateCarrier is the subset of *pgconn.PgError the classifier needs, so the
// check works without importing pgconn and for any driver exposing SQLSTATEs.
type sqlStateCarrier interface {
	SQLState() string
}

// IsLockNotAvailableError reports whether err is PostgreSQL lock_not_available
// (SQLSTATE 55P03), which is what lock_timeout raises. Many migrations wrap the
// driver error with %s rather than %w, which drops the typed error, so the
// rendered "(SQLSTATE 55P03)" suffix pgx appends is matched as a fallback.
func IsLockNotAvailableError(err error) bool {
	if err == nil {
		return false
	}
	var se sqlStateCarrier
	if errors.As(err, &se) && se.SQLState() == sqlStateLockNotAvailable {
		return true
	}
	return strings.Contains(err.Error(), "SQLSTATE "+sqlStateLockNotAvailable)
}

// MigrationLockRetryOptions returns the step-level retry options a store's
// config asks for: its migration_lock_retry_budget when set and valid, and
// otherwise the package default. An invalid or non-positive value is logged and
// ignored rather than failing boot. A nil config yields the default.
func MigrationLockRetryOptions(config *Config, logger schemas.Logger) LockRetryOptions {
	if config == nil || config.MigrationLockRetryBudget == "" {
		return LockRetryOptions{}
	}
	d, err := time.ParseDuration(config.MigrationLockRetryBudget)
	if err != nil || d <= 0 {
		if logger != nil {
			logger.Warn("postgres: invalid migration_lock_retry_budget %q; using default %s", config.MigrationLockRetryBudget, DefaultLockRetryBudget)
		}
		return LockRetryOptions{}
	}
	return LockRetryOptions{Budget: d}
}

// WithBudgetFallback returns opts with fallback's budget filled in where opts
// has none, so a test or caller override keeps precedence over configuration.
func (opts LockRetryOptions) WithBudgetFallback(fallback LockRetryOptions) LockRetryOptions {
	if opts.Budget <= 0 {
		opts.Budget = fallback.Budget
	}
	return opts
}

// LockRetryOptions tunes RunWithLockRetry. Zero values take the package defaults.
type LockRetryOptions struct {
	// Budget is the total time spent retrying; DefaultLockRetryBudget when zero.
	Budget time.Duration
	// InitialBackoff is the first wait between attempts; it doubles up to MaxBackoff.
	InitialBackoff time.Duration
	// MaxBackoff caps the wait between attempts.
	MaxBackoff time.Duration
	// Describe, when set, is called on the first lock timeout (logged with the
	// retry warning, so operators see what is blocking while there is still time
	// to act) and once the budget is exhausted to add context (typically the
	// sessions holding the contended lock) to the final error.
	Describe func(ctx context.Context) string
}

// RunWithLockRetry runs fn and, while it fails with a lock timeout, runs it again
// with exponential backoff until the budget is spent. Only lock timeouts are
// retried: they mean another session held the lock for a while and retrying later
// can succeed, whereas every other error is returned at once. fn must be
// idempotent; migration steps are, because the migrator records completed IDs and
// the DDL helpers use IF [NOT] EXISTS.
func RunWithLockRetry(ctx context.Context, logger schemas.Logger, label string, opts LockRetryOptions, fn func() error) error {
	budget := opts.Budget
	if budget <= 0 {
		budget = DefaultLockRetryBudget
	}
	backoff := opts.InitialBackoff
	if backoff <= 0 {
		backoff = lockRetryInitialBackoff
	}
	maxBackoff := opts.MaxBackoff
	if maxBackoff <= 0 {
		maxBackoff = lockRetryMaxBackoff
	}
	deadline := time.Now().Add(budget)
	for attempt := 1; ; attempt++ {
		err := fn()
		if err == nil || !IsLockNotAvailableError(err) {
			if err == nil && attempt > 1 && logger != nil {
				logger.Info("%s: succeeded after %d attempts", label, attempt)
			}
			return err
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			detail := ""
			if opts.Describe != nil {
				if d := opts.Describe(context.WithoutCancel(ctx)); d != "" {
					detail = "\n" + d
				}
			}
			return fmt.Errorf("%s: still blocked by lock timeouts after %d attempts over %s: %w%s", label, attempt, budget, err, detail)
		}
		wait := min(backoff, remaining)
		if logger != nil {
			logger.Warn("%s: lock timeout on attempt %d (%v); retrying in %s (%s of %s retry budget left)",
				label, attempt, err, wait, remaining.Round(time.Second), budget)
			if attempt == 1 && opts.Describe != nil {
				if d := opts.Describe(context.WithoutCancel(ctx)); d != "" {
					logger.Warn("%s: blocked by:\n%s", label, d)
				}
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%s: cancelled while retrying after lock timeout: %w", label, ctx.Err())
		case <-time.After(wait):
		}
		backoff = min(backoff*2, maxBackoff)
	}
}
