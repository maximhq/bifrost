package mcp

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// TestDispatcherRecoversFromHookPanic verifies runToolsUpdatedDispatcher survives a
// panicking onToolsUpdated hook. The hook is caller-supplied (the transport's
// SyncAllMCPServers wiring) and runs on the package's single, long-lived dispatcher
// goroutine — without recovery, one bad run would crash the process, and since the
// goroutine never restarts, silently stop all future gateway resyncs for the rest of
// the process lifetime.
func TestDispatcherRecoversFromHookPanic(t *testing.T) {
	manager := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, nil, nil)
	t.Cleanup(func() { _ = manager.Cleanup() })

	var calls int32
	manager.SetOnToolsUpdated(func() {
		n := atomic.AddInt32(&calls, 1)
		if n == 1 {
			panic("simulated hook panic")
		}
	})

	manager.notifyToolsUpdated()
	waitForNotifyCount(t, &calls, 1, time.Second)

	// If the dispatcher goroutine died with the panic above, this second signal would
	// never be processed and the test would time out.
	manager.notifyToolsUpdated()
	waitForNotifyCount(t, &calls, 2, time.Second)
}
