package postgresconn

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

// TestIsLockNotAvailableError covers the typed pgx error, the same error after a
// migration flattened it with %s (which drops the type), and unrelated errors.
func TestIsLockNotAvailableError(t *testing.T) {
	pgErr := &pgconn.PgError{Code: "55P03", Message: "canceling statement due to lock timeout"}
	require.True(t, IsLockNotAvailableError(pgErr))
	require.True(t, IsLockNotAvailableError(fmt.Errorf("wrapped: %w", pgErr)))
	// The 2.3.0 failure: migrationAddEmbeddingInputColumn wraps with %s.
	flattened := fmt.Errorf("error while adding embedding_input column: %s", pgErr.Error())
	require.True(t, IsLockNotAvailableError(flattened))

	require.False(t, IsLockNotAvailableError(nil))
	require.False(t, IsLockNotAvailableError(errors.New("boom")))
	require.False(t, IsLockNotAvailableError(&pgconn.PgError{Code: "57014"}))
}

// fastRetry keeps the retry tests quick.
var fastRetry = LockRetryOptions{Budget: 500 * time.Millisecond, InitialBackoff: 10 * time.Millisecond, MaxBackoff: 20 * time.Millisecond}

// TestRunWithLockRetryRetriesUntilSuccess pins that lock timeouts are retried.
func TestRunWithLockRetryRetriesUntilSuccess(t *testing.T) {
	calls := 0
	err := RunWithLockRetry(context.Background(), nil, "step", fastRetry, func() error {
		calls++
		if calls < 3 {
			return &pgconn.PgError{Code: "55P03"}
		}
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, 3, calls)
}

// TestRunWithLockRetryReturnsOtherErrorsImmediately pins that only lock
// timeouts are retried; any other failure surfaces on the first attempt.
func TestRunWithLockRetryReturnsOtherErrorsImmediately(t *testing.T) {
	calls := 0
	want := errors.New("syntax error")
	err := RunWithLockRetry(context.Background(), nil, "step", fastRetry, func() error {
		calls++
		return want
	})
	require.ErrorIs(t, err, want)
	require.Equal(t, 1, calls)
}

// TestRunWithLockRetryGivesUpAfterBudget pins that the retry is bounded and that
// the final error keeps the lock-timeout cause and the caller's context detail.
func TestRunWithLockRetryGivesUpAfterBudget(t *testing.T) {
	opts := fastRetry
	opts.Budget = 100 * time.Millisecond
	opts.Describe = func(context.Context) string { return "blocked by pid 42" }
	start := time.Now()
	err := RunWithLockRetry(context.Background(), nil, "step", opts, func() error {
		return &pgconn.PgError{Code: "55P03"}
	})
	require.Error(t, err)
	require.True(t, IsLockNotAvailableError(err))
	require.Contains(t, err.Error(), "blocked by pid 42")
	require.Less(t, time.Since(start), 2*time.Second)
}

// TestRunWithLockRetryStopsOnContextCancel pins that a cancelled context ends
// the backoff wait promptly.
func TestRunWithLockRetryStopsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	opts := LockRetryOptions{Budget: time.Minute, InitialBackoff: time.Minute}
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	err := RunWithLockRetry(ctx, nil, "step", opts, func() error { return &pgconn.PgError{Code: "55P03"} })
	require.ErrorIs(t, err, context.Canceled)
}

// warnLogger records Warn messages for TestRunWithLockRetryDescribesFirstTimeout.
type warnLogger struct {
	schemas.Logger
	warns []string
}

// Info drops the message; only warnings matter to these tests.
func (w *warnLogger) Info(string, ...any) {}

// Warn records the formatted message.
func (w *warnLogger) Warn(msg string, args ...any) {
	w.warns = append(w.warns, fmt.Sprintf(msg, args...))
}

// TestRunWithLockRetryDescribesFirstTimeout pins that the blockers are logged on
// the first lock timeout, while the operator can still act, and only then.
func TestRunWithLockRetryDescribesFirstTimeout(t *testing.T) {
	log := &warnLogger{}
	opts := fastRetry
	described := 0
	opts.Describe = func(context.Context) string { described++; return "pid=42 rel=logs" }
	calls := 0
	require.NoError(t, RunWithLockRetry(context.Background(), log, "step", opts, func() error {
		calls++
		if calls < 4 {
			return &pgconn.PgError{Code: "55P03"}
		}
		return nil
	}))
	require.Equal(t, 1, described, "Describe runs once, on the first timeout")
	found := false
	for _, w := range log.warns {
		if strings.Contains(w, "blocked by:") && strings.Contains(w, "pid=42") {
			found = true
		}
	}
	require.True(t, found, "the first timeout must log the blockers: %v", log.warns)
}

// TestMigrationLockRetryOptions pins how migration_lock_retry_budget is read: a
// valid duration becomes the budget, and empty, invalid or non-positive values
// fall back to the package default without failing.
func TestMigrationLockRetryOptions(t *testing.T) {
	require.Equal(t, LockRetryOptions{}, MigrationLockRetryOptions(nil, nil))
	require.Equal(t, LockRetryOptions{}, MigrationLockRetryOptions(&Config{}, nil))
	require.Equal(t, 45*time.Minute, MigrationLockRetryOptions(&Config{MigrationLockRetryBudget: "45m"}, nil).Budget)
	log := &warnLogger{}
	require.Equal(t, LockRetryOptions{}, MigrationLockRetryOptions(&Config{MigrationLockRetryBudget: "soon"}, log))
	require.Equal(t, LockRetryOptions{}, MigrationLockRetryOptions(&Config{MigrationLockRetryBudget: "-1m"}, log))
	require.Len(t, log.warns, 2)
}

// TestLockRetryOptionsWithBudgetFallback pins that an explicit budget (a test
// override) keeps precedence over the configured one, and a zero budget takes it.
func TestLockRetryOptionsWithBudgetFallback(t *testing.T) {
	configured := LockRetryOptions{Budget: 45 * time.Minute}
	require.Equal(t, 45*time.Minute, LockRetryOptions{}.WithBudgetFallback(configured).Budget)
	require.Equal(t, time.Second, LockRetryOptions{Budget: time.Second}.WithBudgetFallback(configured).Budget)
	require.Equal(t, time.Duration(0), LockRetryOptions{}.WithBudgetFallback(LockRetryOptions{}).Budget, "no budget anywhere leaves the package default to RunWithLockRetry")
}

// TestStatementKindNeverEchoesQueryText pins that the lock-holder diagnostic
// classifies statements through a fixed allowlist: newlines, tabs, literals and
// leading comments never leak, and only allowlisted constants come back.
func TestStatementKindNeverEchoesQueryText(t *testing.T) {
	cases := map[string]string{
		"SELECT count(*) FROM logs":                 "SELECT",
		"\n\tselect\n'secret'\nFROM logs":           "SELECT",
		"INSERT INTO logs VALUES ('secret')":        "INSERT",
		"REFRESH MATERIALIZED VIEW CONCURRENTLY mv": "REFRESH",
		"/* secret */ SELECT 1":                     "UNKNOWN",
		"-- secret\nSELECT 1":                       "UNKNOWN",
		"(SELECT 'secret')":                         "UNKNOWN",
		"EXPLAIN SELECT 'secret'":                   "UNKNOWN",
		"":                                          "UNKNOWN",
		"SELECT'secret'":                            "SELECT",
	}
	for query, want := range cases {
		require.Equal(t, want, statementKind(query), "query %q", query)
		require.NotContains(t, statementKind(query), "secret")
	}
}
