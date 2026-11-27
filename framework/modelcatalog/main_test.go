package modelcatalog

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/livestore"
	"github.com/maximhq/bifrost/framework/modelcatalog/datasheet"
	"github.com/maximhq/bifrost/framework/modelcatalog/datasheet/datasheettest"
	"github.com/maximhq/bifrost/framework/modelcatalog/keyconfig"
	"github.com/maximhq/bifrost/framework/modelcatalog/live"
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
	ds := datasheetForTest(t, nil, logger, datasheet.Config{
		URL:                "file://" + pricingPath,
		ModelParametersURL: "file://" + paramsPath,
		SyncInterval:       time.Hour,
	})
	mc := catalogWithDatasheetForTest(t, ds)
	mc.logger = logger
	mc.distributedLockManager = configstore.NewDistributedLockManager(nil, logger)

	hookCalls := 0
	mc.SetShouldSyncGate(func(context.Context) bool { return gateOpen })
	mc.SetAfterSyncHook(func(context.Context) { hookCalls++ })
	return mc, &hookCalls
}

type pausedModelParamsStore struct {
	configstore.ConfigStore
	paused  chan struct{}
	release chan struct{}
	once    sync.Once
}

type observedLockStore struct {
	configstore.ConfigStore
	tried chan bool
	once  sync.Once
}

// TryAcquireLock records when the second gateway attempts to enter the shared critical section.
func (s *observedLockStore) TryAcquireLock(ctx context.Context, lock *tables.TableDistributedLock) (bool, error) {
	acquired, err := s.ConfigStore.TryAcquireLock(ctx, lock)
	s.once.Do(func() { s.tried <- acquired })
	return acquired, err
}

// GetModelParameters pauses after taking a database snapshot to expose overlapping syncs.
func (s *pausedModelParamsStore) GetModelParameters(ctx context.Context) ([]tables.TableModelParameters, error) {
	rows, err := s.ConfigStore.GetModelParameters(ctx)
	if err != nil {
		return nil, err
	}
	s.once.Do(func() { close(s.paused) })
	select {
	case <-s.release:
		return rows, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// TestRunParamsSyncSerializesSharedDatabaseSnapshot verifies gateways cannot publish overlapping snapshots.
func TestRunParamsSyncSerializesSharedDatabaseSnapshot(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	logger := bifrost.NewNoOpLogger()
	cfg := &configstore.Config{Enabled: true, Type: configstore.ConfigStoreTypeSQLite,
		Config: &configstore.SQLiteConfig{Path: filepath.Join(t.TempDir(), "shared.db")}}
	openStore := func() configstore.ConfigStore {
		cs, err := configstore.NewConfigStore(ctx, cfg, logger)
		require.NoError(t, err)
		t.Cleanup(func() { _ = cs.Close(context.Background()) })
		return cs
	}
	csA, csB := openStore(), openStore()
	paused := &pausedModelParamsStore{ConfigStore: csA, paused: make(chan struct{}), release: make(chan struct{})}

	newCatalog := func(cs configstore.ConfigStore, model string) (*ModelCatalog, *datasheet.Store) {
		path := filepath.Join(t.TempDir(), model+".json")
		require.NoError(t, os.WriteFile(path, []byte(`{"custom-anthropic/`+model+`":{"provider":"custom-anthropic","max_output_tokens":32000}}`), 0o600))
		ds := datasheetForTest(t, cs, logger, datasheet.Config{ModelParametersURL: "file://" + path})
		mc := catalogWithDatasheetForTest(t, ds)
		mc.configStore = cs
		mc.logger = logger
		mc.distributedLockManager = configstore.NewDistributedLockManager(cs, logger)
		return mc, ds
	}
	first, _ := newCatalog(paused, "first")
	second, secondDatasheet := newCatalog(csB, "second")
	secondLockStore := &observedLockStore{ConfigStore: csB, tried: make(chan bool, 1)}
	second.distributedLockManager = configstore.NewDistributedLockManager(secondLockStore, logger)
	firstDone, secondDone := make(chan error, 1), make(chan error, 1)
	go func() { _, err := first.runParamsSync(ctx); firstDone <- err }()
	select {
	case <-paused.paused:
	case <-ctx.Done():
		t.Fatal("first gateway did not pause after reading its snapshot")
	}
	go func() { _, err := second.runParamsSync(ctx); secondDone <- err }()
	select {
	case acquired := <-secondLockStore.tried:
		if acquired {
			t.Fatal("second gateway acquired the shared lock while the first still held it")
		}
	case <-ctx.Done():
		t.Fatal("second gateway did not try to acquire the shared lock")
	}
	select {
	case err := <-secondDone:
		t.Fatalf("second gateway bypassed the shared lock: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(paused.release)
	require.NoError(t, <-firstDone)
	require.NoError(t, <-secondDone)

	for _, model := range []string{"first", "second"} {
		caps, err := secondDatasheet.LoadModelCapabilities(ctx, "custom-anthropic", model)
		require.NoError(t, err)
		if caps == nil || caps.MaxOutputTokens == nil || *caps.MaxOutputTokens != 32000 {
			t.Fatalf("last published snapshot lost %s: %+v", model, caps)
		}
	}
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

// stateStoresForTest returns a live-models store and a key config store over a new live store
// closed when the test ends.
func stateStoresForTest(t testing.TB) (*live.Store, *keyconfig.Store) {
	t.Helper()
	store, err := livestore.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	liveModels, err := live.New(store, nil)
	if err != nil {
		t.Fatal(err)
	}
	keyconf, err := keyconfig.New(store, nil)
	if err != nil {
		t.Fatal(err)
	}
	return liveModels, keyconf
}

// catalogForTest returns a test model catalog that is cleaned up when the test
// ends, failing the test if it cannot be built.
func catalogForTest(t testing.TB, baseModelIndex map[string]string) *ModelCatalog {
	t.Helper()
	catalog, err := NewTestCatalog(baseModelIndex)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = catalog.Cleanup() })
	return catalog
}

// catalogWithDatasheetForTest returns a test model catalog over ds that is
// cleaned up when the test ends, failing the test if it cannot be built.
func catalogWithDatasheetForTest(t testing.TB, ds *datasheet.Store) *ModelCatalog {
	t.Helper()
	catalog, err := NewTestCatalogWithDatasheet(ds)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = catalog.Cleanup() })
	return catalog
}

// TestCleanupIsSafeToCallTwice pins that a second Cleanup is a no-op: test
// helpers register Cleanup at test end while tests and Config.Close may call it
// on the same catalog first.
func TestCleanupIsSafeToCallTwice(t *testing.T) {
	catalog, err := NewTestCatalog(nil)
	require.NoError(t, err)
	require.NoError(t, catalog.Cleanup())
	require.NotPanics(t, func() { require.NoError(t, catalog.Cleanup()) })
}

// datasheetForTest returns a datasheet over a new live store closed when the test ends.
var datasheetForTest = datasheettest.New

// testDatasheet returns a test datasheet over a live store of its own, closed when the test ends.
func testDatasheet(t testing.TB, baseModelIndex map[string]string) *datasheet.Store {
	t.Helper()
	ds, err := datasheet.NewTestStore(baseModelIndex)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ds.Close)
	return ds
}
