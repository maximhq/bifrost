package modelcatalog

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/modelcatalog/datasheet"
	"github.com/stretchr/testify/require"
)

// newSyncTestCatalog builds a catalog whose pricing and model-parameters URLs
// point at local files, with no config store. A lock manager without a store
// grants every lock, so syncTick runs without a database.
func newSyncTestCatalog(t *testing.T, gateOpen bool) (*ModelCatalog, *int) {
	t.Helper()
	dir := t.TempDir()
	pricingPath := filepath.Join(dir, "pricing.json")
	paramsPath := filepath.Join(dir, "params.json")
	require.NoError(t, os.WriteFile(pricingPath, []byte(`{"gpt-4o":{"provider":"openai","mode":"chat","input_cost_per_token":0.0000025,"output_cost_per_token":0.00001}}`), 0o600))
	require.NoError(t, os.WriteFile(paramsPath, []byte(`{"gpt-4o":{"supports_reasoning":false}}`), 0o600))

	logger := bifrost.NewNoOpLogger()
	ds := datasheet.New(nil, logger, datasheet.Config{
		URL:                "file://" + pricingPath,
		ModelParametersURL: "file://" + paramsPath,
		SyncInterval:       time.Hour,
	})
	mc := NewTestCatalogWithDatasheet(ds)
	mc.logger = logger
	mc.distributedLockManager = configstore.NewDistributedLockManager(nil, logger)

	hookCalls := 0
	mc.SetShouldSyncGate(func(context.Context) bool { return gateOpen })
	mc.SetAfterSyncHook(func(context.Context) { hookCalls++ })
	return mc, &hookCalls
}

// In cluster mode the gate is "am I the leader". A pod whose gate rejects the
// sync wrote nothing, so it must not broadcast a reload: with every pod doing
// so, one hourly cycle cost N*(N-1) full catalog reads. It must not mark itself
// synced either, so a pod that becomes leader picks up the overdue sync on its
// next tick instead of waiting a full interval.
func TestSyncTickGateClosedSkipsHookAndMarkSynced(t *testing.T) {
	mc, hookCalls := newSyncTestCatalog(t, false)

	mc.syncTick(context.Background())

	require.Equal(t, 0, *hookCalls, "after-sync hook fired on a pod whose sync was gated off")
	require.True(t, mc.datasheet.LastSyncedAt().IsZero(), "gated-off pod marked itself synced")
}

// The leader path: a sync that ran broadcasts exactly once and marks the pod synced.
func TestSyncTickGateOpenFiresHookOnce(t *testing.T) {
	mc, hookCalls := newSyncTestCatalog(t, true)

	mc.syncTick(context.Background())

	require.Equal(t, 1, *hookCalls)
	require.False(t, mc.datasheet.LastSyncedAt().IsZero(), "leader did not mark itself synced")
}

// Leadership can move after syncTick's pre-lock gate check, so one of the two
// runs may sync while the other is skipped. Peers must still reload what was
// written, but the node must not count the cycle as synced: a half-skipped
// cycle stays overdue and the next tick on the leader retries it.
func TestSyncTickGateFlipMidSyncFiresHookButDoesNotMarkSynced(t *testing.T) {
	mc, hookCalls := newSyncTestCatalog(t, true)
	// Call 1 is the pre-lock check; of the two in-run checks, which run on
	// separate goroutines, exactly one is allowed through.
	var gateCalls atomic.Int32
	mc.SetShouldSyncGate(func(context.Context) bool { return gateCalls.Add(1) <= 2 })

	mc.syncTick(context.Background())

	require.Equal(t, int32(3), gateCalls.Load())
	require.Equal(t, 1, *hookCalls, "peers must reload the half that was written")
	require.True(t, mc.datasheet.LastSyncedAt().IsZero(), "half-skipped cycle marked the node synced")
}

func TestForceReloadPricingGateClosedSkipsHook(t *testing.T) {
	mc, hookCalls := newSyncTestCatalog(t, false)

	require.NoError(t, mc.ForceReloadPricing(context.Background()))

	require.Equal(t, 0, *hookCalls, "after-sync hook fired on a force reload whose sync was gated off")
}

func TestForceReloadPricingGateOpenFiresHookOnce(t *testing.T) {
	mc, hookCalls := newSyncTestCatalog(t, true)

	require.NoError(t, mc.ForceReloadPricing(context.Background()))

	require.Equal(t, 1, *hookCalls)
}
