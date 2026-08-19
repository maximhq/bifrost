package modelcatalog

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

func TestFetchMCPLibraryFromFileURL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "servers.json")
	payload := `{"servers":[{"name":"Filesystem","connection_type":"stdio","auth_type":"none"}]}`
	require.NoError(t, os.WriteFile(path, []byte(payload), 0o600))

	entries, err := fetchMCPLibrary(context.Background(), "file://"+path)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "Filesystem", entries[0].Name)
	require.Equal(t, schemas.MCPConnectionTypeSTDIO, entries[0].ConnectionType)
}

func TestWithRetries_TableDriven(t *testing.T) {
	t.Parallel()

	errTransient := errors.New("transient")

	tests := []struct {
		name         string
		maxRetries   int
		maxBackoff   time.Duration
		// failCount is how many times op fails before succeeding.
		// Set to -1 to always fail.
		failCount    int
		ctxTimeout   time.Duration // 0 means no timeout
		wantAttempts int
		wantVal      string
		wantErr      error
	}{
		{
			name:         "succeeds after N retries",
			maxRetries:   3,
			maxBackoff:   time.Millisecond,
			failCount:    2,
			wantAttempts: 3,
			wantVal:      "ok",
		},
		{
			name:         "succeeds on first attempt",
			maxRetries:   3,
			maxBackoff:   time.Millisecond,
			failCount:    0,
			wantAttempts: 1,
			wantVal:      "ok",
		},
		{
			name:         "exhausts all retries",
			maxRetries:   2,
			maxBackoff:   time.Millisecond,
			failCount:    -1,
			wantAttempts: 3, // 1 initial + 2 retries
			wantErr:      errTransient,
		},
		{
			// maxBackoff left high so the first retry's backoff wait (1s,
			// from retryBackoffMin) outlasts the 20ms ctx timeout, forcing
			// cancellation during the wait rather than after exhausting
			// retries.
			name:         "context cancelled before success",
			maxRetries:   5,
			maxBackoff:   time.Second,
			failCount:    -1,
			ctxTimeout:   20 * time.Millisecond,
			wantErr:      context.DeadlineExceeded,
		},
		{
			name:         "backoff does not exceed cap",
			maxRetries:   6,
			maxBackoff:   2 * time.Millisecond,
			failCount:    -1,
			wantAttempts: 7, // 1 initial + 6 retries
			wantErr:      errTransient,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			var cancel context.CancelFunc
			if tt.ctxTimeout > 0 {
				ctx, cancel = context.WithTimeout(ctx, tt.ctxTimeout)
				defer cancel()
			}

			attempts := 0
			start := time.Now()

			op := func() (string, error) {
				attempts++
				if tt.failCount < 0 || attempts <= tt.failCount {
					return "", errTransient
				}
				return "ok", nil
			}

			val, err := withRetries(ctx, tt.maxRetries, tt.maxBackoff, op)

			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				require.Empty(t, val)
			} else {
				require.NoError(t, err)
				require.Equal(t, tt.wantVal, val)
			}

			if tt.wantAttempts > 0 {
				require.Equal(t, tt.wantAttempts, attempts)
			}

			// For the context-cancellation case, verify that attempts stopped
			// early (well before maxRetries+1).
			if tt.ctxTimeout > 0 {
				require.Less(t, attempts, tt.maxRetries+1,
					"expected context cancellation to stop retries early")
			}

			// For the backoff-cap case, verify total elapsed time stays
			// bounded. With maxBackoff=2ms and 6 retries, uncapped
			// exponential would be 1+2+4+8+16+32 = 63ms. Capped at 2ms
			// it's at most 6*2 = 12ms. We allow a generous margin.
			if tt.name == "backoff does not exceed cap" {
				elapsed := time.Since(start)
				require.Less(t, elapsed, 200*time.Millisecond,
					"total elapsed time suggests backoff was not capped")
			}
		})
	}
}
