package server

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// readFailures fails reads of a database: the next few, or every one while it is set to fail
// them all.
type readFailures struct {
	left atomic.Int32 // reads still to fail; below zero, every read fails
}

// install registers the failures on db's query callbacks.
func (f *readFailures) install(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.NoError(t, db.Callback().Query().Before("gorm:query").Register("test:read_failures", func(tx *gorm.DB) {
		for {
			left := f.left.Load()
			if left == 0 {
				return
			}
			if left < 0 || f.left.CompareAndSwap(left, left-1) {
				break
			}
		}
		_ = tx.AddError(errors.New("sql: database is closed"))
	}))
}

// TestVirtualMCPDetachRetriesTheKeyRead checks that the key refresh after a Virtual MCP detach
// retries a failed read, so a read failing once still drops the detached Virtual MCP from the
// key, and that when every retry fails the refresh returns the error, which the route answers
// with a 500, and the key keeps its previous record.
func TestVirtualMCPDetachRetriesTheKeyRead(t *testing.T) {
	for _, tc := range []struct {
		name     string
		failures int32
	}{
		{name: "a read failing once is retried", failures: 1},
		{name: "reads failing past the retries keep the previous record", failures: -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, gs := governedServer(t)
			ctx := context.Background()
			store := server.Config.ConfigStore
			storeTestVirtualKey(t, server, "vk-1", "app", "sk-bf-app")
			def := &configstoreTables.TableVirtualMCP{Name: "research", Enabled: true}
			require.NoError(t, store.CreateVirtualMCP(ctx, def))
			_, err := server.ReloadVirtualMCP(ctx, def.ID)
			require.NoError(t, err)
			require.NoError(t, store.AttachVirtualMCPToVirtualKey(ctx, def.ID, "vk-1"))
			_, err = server.ReloadVirtualKey(ctx, "vk-1")
			require.NoError(t, err)
			require.Equal(t, []uint{def.ID}, assignedVirtualMCPs(t, server, gs, "vk-1"))

			require.NoError(t, store.DetachVirtualMCPFromVirtualKey(ctx, def.ID, "vk-1"))
			reads := &readFailures{}
			reads.install(t, store.DB())
			reads.left.Store(tc.failures)
			err = server.ReloadVirtualMCPAssignment(ctx, "vk-1", def.ID, false)
			reads.left.Store(0)
			if tc.failures > 0 {
				require.NoError(t, err, "the retry reads the key")
				require.Empty(t, assignedVirtualMCPs(t, server, gs, "vk-1"), "the detached Virtual MCP is no longer granted")
				return
			}
			require.Error(t, err, "the failed refresh is returned for the route to answer")
			require.Equal(t, []uint{def.ID}, assignedVirtualMCPs(t, server, gs, "vk-1"), "the key keeps its previous record")
		})
	}
}
