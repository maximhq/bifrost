package mcp_headers

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// sweepCountingStore counts the per-user headers sweep's deletes.
type sweepCountingStore struct {
	*testConfigStore
	flowSweeps   atomic.Int32
	orphanSweeps atomic.Int32
}

func (s *sweepCountingStore) DeleteExpiredMCPPerUserHeaderFlows(context.Context) (int64, error) {
	s.flowSweeps.Add(1)
	return 0, nil
}

func (s *sweepCountingStore) DeleteOrphanedMCPPerUserHeaderCredentials(context.Context, time.Duration) (int64, error) {
	s.orphanSweeps.Add(1)
	return 0, nil
}

// TestCredentialSweepWorker_GateDecidesEverySweep pins that the sweep gate is
// consulted before both sweeps, the one run on start included: a closed gate
// deletes nothing, an open one deletes, and clearing it sweeps unconditionally.
func TestCredentialSweepWorker_GateDecidesEverySweep(t *testing.T) {
	newWorker := func(t *testing.T) (*CredentialSweepWorker, *sweepCountingStore) {
		t.Helper()
		store := &sweepCountingStore{testConfigStore: newTestConfigStore()}
		w := NewCredentialSweepWorker(NewProvider(store, bifrost.NewDefaultLogger(schemas.LogLevelError)), time.Hour, nil)
		require.NotNil(t, w)
		return w, store
	}

	t.Run("run on start", func(t *testing.T) {
		w, store := newWorker(t)
		consulted := make(chan struct{}, 2)
		w.SetShouldSweepGate(func(context.Context) bool {
			consulted <- struct{}{}
			return false
		})
		w.Start(context.Background())
		defer w.Stop()

		<-consulted
		<-consulted
		assert.Zero(t, store.flowSweeps.Load())
		assert.Zero(t, store.orphanSweeps.Load())
	})

	t.Run("each sweep", func(t *testing.T) {
		w, store := newWorker(t)
		ctx := context.Background()
		sweep := func() {
			w.sweepOrphanedCredentials(ctx)
			w.sweepExpiredFlows(ctx)
		}
		var open atomic.Bool
		w.SetShouldSweepGate(func(context.Context) bool { return open.Load() })

		sweep()
		assert.Zero(t, store.flowSweeps.Load(), "closed gate")
		assert.Zero(t, store.orphanSweeps.Load(), "closed gate")

		open.Store(true)
		sweep()
		assert.EqualValues(t, 1, store.flowSweeps.Load(), "open gate")
		assert.EqualValues(t, 1, store.orphanSweeps.Load(), "open gate")

		w.SetShouldSweepGate(nil)
		sweep()
		assert.EqualValues(t, 2, store.flowSweeps.Load(), "no gate")
		assert.EqualValues(t, 2, store.orphanSweeps.Load(), "no gate")
	})
}
