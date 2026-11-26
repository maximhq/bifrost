package handlers

import (
	"context"
	"errors"
	"testing"

	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// flakyVirtualMCPReloader fails the first failures reloads, then succeeds.
type flakyVirtualMCPReloader struct {
	GovernanceManager
	failures int
	calls    int
}

// ReloadVirtualMCP fails until the configured number of failures has been returned.
func (f *flakyVirtualMCPReloader) ReloadVirtualMCP(_ context.Context, id uint) (*configstoreTables.TableVirtualMCP, error) {
	f.calls++
	if f.calls <= f.failures {
		return nil, errors.New("synthetic reload failure")
	}
	return &configstoreTables.TableVirtualMCP{ID: id}, nil
}

// TestReloadVirtualMCPWithRetry verifies a failed cache reload after a re-link is retried rather
// than left stale, and that the attempts are bounded.
func TestReloadVirtualMCPWithRetry(t *testing.T) {
	recovering := &flakyVirtualMCPReloader{failures: relinkReloadAttempts - 1}
	h := &MCPHandler{governanceManager: recovering}
	require.NoError(t, h.reloadVirtualMCPWithRetry(context.Background(), 7))
	assert.Equal(t, relinkReloadAttempts, recovering.calls)

	broken := &flakyVirtualMCPReloader{failures: relinkReloadAttempts + 5}
	h = &MCPHandler{governanceManager: broken}
	require.Error(t, h.reloadVirtualMCPWithRetry(context.Background(), 7))
	assert.Equal(t, relinkReloadAttempts, broken.calls, "attempts are bounded")
}
