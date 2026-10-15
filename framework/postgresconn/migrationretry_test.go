package postgresconn

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
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
