package modelcatalog

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// persistentLiveCatalog creates the real SQLite store without starting unrelated datasheet workers.
func persistentLiveCatalog(t *testing.T) (*ModelCatalog, configstore.ConfigStore, *configstore.ProviderConfig) {
	t.Helper()
	logger := bifrost.NewNoOpLogger()
	store, err := configstore.NewConfigStore(t.Context(), &configstore.Config{Enabled: true,
		Type: configstore.ConfigStoreTypeSQLite, Config: &configstore.SQLiteConfig{Path: filepath.Join(t.TempDir(), "config.db")}}, logger)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close(context.Background()) })
	keys := []schemas.Key{
		{ID: "one", Name: "one", Value: *schemas.NewSecretVar("credential-one"), Models: schemas.WhiteList{"*"}},
		{ID: "two", Name: "two", Value: *schemas.NewSecretVar("credential-two"), Models: schemas.WhiteList{"second-model"}},
	}
	require.NoError(t, store.AddProvider(t.Context(), schemas.OpenAI, configstore.ProviderConfig{Keys: keys}))
	config, err := store.GetProviderConfig(t.Context(), schemas.OpenAI)
	require.NoError(t, err)
	return liveCatalogFromStore(store, config), store, config
}

// liveCatalogFromStore represents another process with independent caches over the same database.
func liveCatalogFromStore(store configstore.ConfigStore, config *configstore.ProviderConfig) *ModelCatalog {
	mc := NewTestCatalog(nil)
	mc.configStore, mc.logger = store, bifrost.NewNoOpLogger()
	mc.distributedLockManager = configstore.NewDistributedLockManager(store, mc.logger)
	mc.ReplaceKeyConfig(map[schemas.ModelProvider][]schemas.Key{schemas.OpenAI: config.Keys})
	return mc
}

// liveFetch returns fresh independent response objects like a successful provider request.
func liveFetch(model string) func(context.Context, bool) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	return func(context.Context, bool) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
		return &schemas.BifrostListModelsResponse{Data: []schemas.Model{{ID: "openai/" + model, Created: schemas.Ptr(int64(1700000000))}}}, nil
	}
}

// syncLiveFixture seeds via the production sync path and requires usable persistent data.
func syncLiveFixture(t *testing.T, mc *ModelCatalog, source *configstore.LiveModelSource, model string) {
	t.Helper()
	_, available, err := mc.SyncLiveModelsForKey(t.Context(), *source, liveFetch(model))
	require.NoError(t, err)
	require.True(t, available)
}

// TestLiveModelsPersistReload verifies independent modes and keys survive process-local cache replacement.
func TestLiveModelsPersistReload(t *testing.T) {
	mc, store, config := persistentLiveCatalog(t)
	// A provider update keeps this pointer in memory, while SQL reload omits the empty URL.
	config.ProxyConfig = &schemas.ProxyConfig{Type: schemas.EnvProxy, URL: schemas.NewSecretVar("")}
	require.NoError(t, store.UpdateProvider(t.Context(), schemas.OpenAI, *config))
	repo := store.(configstore.LiveModelListingStore)
	one, err := repo.GetLiveModelSource(t.Context(), schemas.OpenAI, "one")
	require.NoError(t, err)
	one.ProviderHash, err = configstore.LiveModelProviderHash(config)
	require.NoError(t, err)
	two, err := repo.GetLiveModelSource(t.Context(), schemas.OpenAI, "two")
	require.NoError(t, err)
	var calls atomic.Int32
	_, available, err := mc.SyncLiveModelsForKey(t.Context(), *one,
		func(ctx context.Context, unfiltered bool) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
			calls.Add(1)
			resp, _ := liveFetch("first-model")(ctx, unfiltered)
			if unfiltered {
				resp.Data = append(resp.Data, schemas.Model{ID: "openai/raw-only"})
			}
			return resp, nil
		})
	require.NoError(t, err)
	require.Equal(t, int32(2), calls.Load(), "equivalent proxy representations must not skip discovery")
	require.True(t, available)
	syncLiveFixture(t, mc, two, "second-model")
	follower := liveCatalogFromStore(store, config)
	require.NoError(t, follower.ReloadLiveModelsFromDB(t.Context(), schemas.OpenAI, config, follower.LiveGeneration(schemas.OpenAI)))
	require.ElementsMatch(t, []string{"first-model", "second-model"}, follower.GetModelsForProvider(schemas.OpenAI))
	require.ElementsMatch(t, []string{"first-model", "second-model", "raw-only"}, follower.GetUnfilteredModelsForProvider(schemas.OpenAI))
	require.Equal(t, int64(1700000000), follower.GetModelCreatedTimes(schemas.OpenAI, false)["first-model"])
	require.True(t, follower.IsModelAllowedForProvider(schemas.OpenAI, "first-model", config, schemas.WhiteList{"*"}))
	require.False(t, follower.IsModelAllowedForProvider(schemas.OpenAI, "first-model", config, schemas.WhiteList{}))
	syncLiveFixture(t, mc, one, "changed-model")
	require.NoError(t, follower.ReloadLiveModelsFromDB(t.Context(), schemas.OpenAI, config, follower.LiveGeneration(schemas.OpenAI), "one"))
	require.ElementsMatch(t, []string{"changed-model", "second-model"}, follower.GetModelsForProvider(schemas.OpenAI))
	rows, err := repo.GetLiveModelListings(t.Context(), schemas.OpenAI, &two.KeyID)
	require.NoError(t, err)
	require.Len(t, rows, 2, "another key's refresh preserves both entries")
}

// TestLiveModelsRefreshFailures keeps only failed modes' last-known-good data and accepts genuine empty success.
func TestLiveModelsRefreshFailures(t *testing.T) {
	cases := []struct{ name, filtered, unfiltered string }{
		{"both fail", "error", "error"}, {"filtered fails", "error", ""}, {"unfiltered fails", "", "error"},
		{"nil response", "nil", "nil"}, {"next page", "next", "next"}, {"has more", "more", "more"},
		{"failed key despite response", "status", "status"}, {"empty filtered success", "empty", ""},
		{"partial discovery", "partial", "partial"}, {"partial filtered", "partial", ""}, {"partial unfiltered", "", "partial"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mc, store, config := persistentLiveCatalog(t)
			repo := store.(configstore.LiveModelListingStore)
			source, err := repo.GetLiveModelSource(t.Context(), schemas.OpenAI, "one")
			require.NoError(t, err)
			syncLiveFixture(t, mc, source, "last-good")
			mc = liveCatalogFromStore(store, config) // Force fallback to come from SQL, not an already warm cache.
			statuses, available, err := mc.SyncLiveModelsForKey(t.Context(), *source,
				func(ctx context.Context, unfiltered bool) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
					bad := tc.filtered
					if unfiltered {
						bad = tc.unfiltered
					}
					if bad == "nil" {
						return nil, nil
					}
					if bad == "error" {
						return nil, &schemas.BifrostError{Error: &schemas.ErrorField{Message: "synthetic timeout"},
							ExtraFields: schemas.BifrostErrorExtraFields{KeyStatuses: []schemas.KeyStatus{{Provider: schemas.OpenAI, KeyID: "one", Status: schemas.KeyStatusListModelsFailed}}}}
					}
					resp, _ := liveFetch("new-model")(ctx, unfiltered)
					switch bad {
					case "next":
						resp.NextPageToken = "more"
					case "more":
						resp.HasMore = schemas.Ptr(true)
					case "partial":
						resp.IsPartial = true
					case "status":
						resp.KeyStatuses = []schemas.KeyStatus{{KeyID: "one", Status: schemas.KeyStatusListModelsFailed}}
					case "empty":
						resp.Data = []schemas.Model{}
					}
					return resp, nil
				})
			require.NoError(t, err)
			require.Equal(t, tc.filtered != "nil" || tc.unfiltered != "nil", available)
			if tc.filtered != "" && tc.filtered != "nil" && tc.filtered != "empty" {
				require.Len(t, statuses, 1)
				require.Equal(t, schemas.KeyStatusListModelsFailed, statuses[0].Status)
			}
			rows, err := repo.GetLiveModelListings(t.Context(), schemas.OpenAI, &source.KeyID)
			require.NoError(t, err)
			for _, row := range rows {
				bad := tc.filtered
				if row.Unfiltered {
					bad = tc.unfiltered
				}
				want := `{"Models":["last-good"],"Created":{"last-good":1700000000}}`
				if bad == "" {
					want = `{"Models":["new-model"],"Created":{"new-model":1700000000}}`
				}
				if bad == "empty" {
					want = `{"Models":[],"Created":null}`
				}
				require.JSONEq(t, want, row.Data)
				models := mc.GetModelsForProvider(schemas.OpenAI)
				if row.Unfiltered {
					models = mc.GetUnfilteredModelsForProvider(schemas.OpenAI)
				}
				switch bad {
				case "":
					require.Contains(t, models, "new-model")
				case "nil", "empty":
					require.NotContains(t, models, "last-good")
				default:
					require.Contains(t, models, "last-good", "a cold cache must recover from the persisted snapshot")
				}
			}
			if tc.filtered == "empty" {
				config, err := store.GetProviderConfig(t.Context(), schemas.OpenAI)
				require.NoError(t, err)
				follower := liveCatalogFromStore(store, config)
				require.NoError(t, follower.ReloadLiveModelsFromDB(t.Context(), schemas.OpenAI, config, follower.LiveGeneration(schemas.OpenAI)))
				require.NotContains(t, follower.GetModelsForProvider(schemas.OpenAI), "last-good", "a successful empty list is valid, not corrupt data")
			}
		})
	}
}

// TestLiveModelsDatabaseFailuresPreserveCache distinguishes storage faults from legitimate catalog absence.
func TestLiveModelsDatabaseFailuresPreserveCache(t *testing.T) {
	for _, operation := range []string{"write", "read"} {
		t.Run(operation, func(t *testing.T) {
			mc, store, config := persistentLiveCatalog(t)
			source, err := store.(configstore.LiveModelListingStore).GetLiveModelSource(t.Context(), schemas.OpenAI, "one")
			require.NoError(t, err)
			syncLiveFixture(t, mc, source, "last-good")
			failure := errors.New("synthetic database failure")
			callback := func(db *gorm.DB) {
				if db.Statement.Table == (tables.TableLiveModelListing{}).TableName() {
					db.AddError(failure)
				}
			}
			if operation == "write" {
				require.NoError(t, store.DB().Callback().Create().Before("gorm:create").Register("test:live-write", callback))
				_, available, err := mc.SyncLiveModelsForKey(t.Context(), *source, liveFetch("uncommitted"))
				require.ErrorIs(t, err, failure)
				require.False(t, available)
			} else {
				require.NoError(t, store.DB().Callback().Query().Before("gorm:query").Register("test:live-read", callback))
				require.ErrorIs(t, mc.ReloadLiveModelsFromDB(t.Context(), schemas.OpenAI, config, mc.LiveGeneration(schemas.OpenAI)), failure)
			}
			require.ElementsMatch(t, []string{"last-good", "second-model"}, mc.GetModelsForProvider(schemas.OpenAI))
		})
	}
}

// TestLiveModelsMalformedSnapshot preserves a valid cache when persisted JSON is not a listing.
func TestLiveModelsMalformedSnapshot(t *testing.T) {
	for _, data := range []string{"{", "null", "{}"} {
		t.Run(data, func(t *testing.T) {
			mc, store, config := persistentLiveCatalog(t)
			source, err := store.(configstore.LiveModelListingStore).GetLiveModelSource(t.Context(), schemas.OpenAI, "one")
			require.NoError(t, err)
			syncLiveFixture(t, mc, source, "last-good")
			require.NoError(t, store.DB().Model(&tables.TableLiveModelListing{}).Where("key_id = ?", "one").Update("data", data).Error)
			require.Error(t, mc.ReloadLiveModelsFromDB(t.Context(), schemas.OpenAI, config, mc.LiveGeneration(schemas.OpenAI)))
			require.ElementsMatch(t, []string{"last-good", "second-model"}, mc.GetModelsForProvider(schemas.OpenAI))
			_, available, err := mc.SyncLiveModelsForKey(t.Context(), *source,
				func(context.Context, bool) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
					return nil, &schemas.BifrostError{Error: &schemas.ErrorField{Message: "synthetic failure"}}
				})
			require.Error(t, err)
			require.False(t, available)
			require.ElementsMatch(t, []string{"last-good", "second-model"}, mc.GetModelsForProvider(schemas.OpenAI))
		})
	}
}

// TestLiveModelsGateAndNoSnapshot preserves the existing datasheet fallback and caller-supplied manual override.
func TestLiveModelsGateAndNoSnapshot(t *testing.T) {
	mc, store, _ := persistentLiveCatalog(t)
	mc.datasheet.SetPricingRowsForTest([]tables.TableModelPricing{{Provider: "openai", Model: "sheet-model", Mode: "chat"}})
	source, err := store.(configstore.LiveModelListingStore).GetLiveModelSource(t.Context(), schemas.OpenAI, "one")
	require.NoError(t, err)
	failure := func(context.Context, bool) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
		return nil, &schemas.BifrostError{Error: &schemas.ErrorField{Message: "synthetic provider failure"}}
	}
	_, available, err := mc.SyncLiveModelsForKey(t.Context(), *source, failure)
	require.NoError(t, err)
	require.False(t, available)
	require.Contains(t, mc.GetModelsForProvider(schemas.OpenAI), "sheet-model")
	type overrideKey struct{}
	mc.SetShouldSyncGate(func(ctx context.Context) bool { return ctx.Value(overrideKey{}) == true })
	var calls atomic.Int32
	fetch := func(ctx context.Context, mode bool) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
		calls.Add(1)
		return liveFetch("manual-model")(ctx, mode)
	}
	_, available, err = mc.SyncLiveModelsForKey(t.Context(), *source, fetch)
	require.NoError(t, err)
	require.False(t, available)
	require.Zero(t, calls.Load())
	_, available, err = mc.SyncLiveModelsForKey(context.WithValue(t.Context(), overrideKey{}, true), *source, fetch)
	require.NoError(t, err)
	require.True(t, available)
	require.EqualValues(t, 2, calls.Load())
}

// TestLiveModelsInFlightChanges discards both modes when eligibility or the captured source changes mid-fetch.
func TestLiveModelsInFlightChanges(t *testing.T) {
	for _, change := range []string{"generation", "credentials", "gate", "cancellation"} {
		t.Run(change, func(t *testing.T) {
			mc, store, _ := persistentLiveCatalog(t)
			repo := store.(configstore.LiveModelListingStore)
			source, err := repo.GetLiveModelSource(t.Context(), schemas.OpenAI, "one")
			require.NoError(t, err)
			syncLiveFixture(t, mc, source, "last-good")
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			var gate atomic.Bool
			gate.Store(true)
			mc.SetShouldSyncGate(func(context.Context) bool { return gate.Load() })
			started, release := make(chan struct{}, 2), make(chan struct{})
			done := make(chan error, 1)
			go func() {
				_, available, err := mc.SyncLiveModelsForKey(ctx, *source,
					func(ctx context.Context, mode bool) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
						started <- struct{}{}
						select {
						case <-release:
						case <-ctx.Done():
						}
						return liveFetch("obsolete")(ctx, mode)
					})
				if available {
					err = errors.New("obsolete data became available")
				}
				done <- err
			}()
			for i := 0; i < 2; i++ {
				select {
				case <-started:
				case <-ctx.Done():
					t.Fatal("fetch did not start")
				}
			}
			switch change {
			case "generation":
				mc.InvalidateLive(schemas.OpenAI, "one")
			case "credentials":
				key, err := store.GetProviderKey(t.Context(), schemas.OpenAI, "one")
				require.NoError(t, err)
				key.Value = *schemas.NewSecretVar("replacement")
				require.NoError(t, store.UpdateProviderKey(t.Context(), schemas.OpenAI, key.ID, *key))
			case "gate":
				gate.Store(false)
			case "cancellation":
				cancel()
			}
			close(release)
			err = <-done
			if change == "cancellation" {
				require.ErrorIs(t, err, context.Canceled)
			} else {
				require.NoError(t, err)
			}
			var rows []tables.TableLiveModelListing
			require.NoError(t, store.DB().Find(&rows).Error)
			for _, row := range rows {
				require.NotContains(t, row.Data, "obsolete")
			}
		})
	}
}

// TestLiveModelsConcurrentCatalogs serializes one key across independent caches without globally locking other keys.
func TestLiveModelsConcurrentCatalogs(t *testing.T) {
	first, store, config := persistentLiveCatalog(t)
	second := liveCatalogFromStore(store, config)
	repo := store.(configstore.LiveModelListingStore)
	one, err := repo.GetLiveModelSource(t.Context(), schemas.OpenAI, "one")
	require.NoError(t, err)
	two, err := repo.GetLiveModelSource(t.Context(), schemas.OpenAI, "two")
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	started, release := make(chan struct{}, 2), make(chan struct{})
	firstDone, secondDone := make(chan error, 1), make(chan error, 1)
	go func() {
		_, _, err := first.SyncLiveModelsForKey(ctx, *one, func(ctx context.Context, mode bool) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
			started <- struct{}{}
			select {
			case <-release:
			case <-ctx.Done():
			}
			return liveFetch("first")(ctx, mode)
		})
		firstDone <- err
	}()
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-ctx.Done():
			t.Fatal("first fetch did not start")
		}
	}
	type callerKey struct{}
	contended := make(chan int64, 1)
	require.NoError(t, store.DB().Callback().Create().After("gorm:create").Register("test:live-contention", func(db *gorm.DB) {
		if db.Statement.Table == (tables.TableDistributedLock{}).TableName() && db.Statement.Context.Value(callerKey{}) == true {
			select {
			case contended <- db.RowsAffected:
			default:
			}
		}
	}))
	var secondCalls atomic.Int32
	go func() {
		_, _, err := second.SyncLiveModelsForKey(context.WithValue(ctx, callerKey{}, true), *one,
			func(ctx context.Context, mode bool) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
				secondCalls.Add(1)
				return liveFetch("second")(ctx, mode)
			})
		secondDone <- err
	}()
	select {
	case rows := <-contended:
		require.Zero(t, rows, "first sync owns the database lease")
	case <-ctx.Done():
		t.Fatal("second sync did not attempt the lease")
	}
	require.Zero(t, secondCalls.Load())
	_, available, err := second.SyncLiveModelsForKey(ctx, *two, liveFetch("independent-key"))
	require.NoError(t, err)
	require.True(t, available, "a different key does not wait for the held lease")
	close(release)
	require.NoError(t, <-firstDone)
	require.NoError(t, <-secondDone)
	require.EqualValues(t, 2, secondCalls.Load())
	require.NoError(t, first.ReloadLiveModelsFromDB(t.Context(), schemas.OpenAI, config, first.LiveGeneration(schemas.OpenAI)))
	require.ElementsMatch(t, []string{"second", "independent-key", "second-model"}, first.GetModelsForProvider(schemas.OpenAI))
}

// delayedLiveListingStore pauses one completed database read without holding a SQL transaction.
type delayedLiveListingStore struct {
	configstore.ConfigStore
	configstore.LiveModelListingStore
	loaded chan struct{}
	resume chan struct{}
	once   sync.Once
}

// GetLiveModelListings exposes a stale read while a configuration update and fresh sync finish.
func (s *delayedLiveListingStore) GetLiveModelListings(ctx context.Context, provider schemas.ModelProvider, keyID *string) ([]tables.TableLiveModelListing, error) {
	rows, err := s.LiveModelListingStore.GetLiveModelListings(ctx, provider, keyID)
	s.once.Do(func() {
		close(s.loaded)
		select {
		case <-s.resume:
		case <-ctx.Done():
		}
	})
	return rows, err
}

// TestLiveModelsReloadConfigurationRace prevents a stale reload retry from clearing a newly refreshed cache.
func TestLiveModelsReloadConfigurationRace(t *testing.T) {
	mc, store, config := persistentLiveCatalog(t)
	generation := mc.LiveGeneration(schemas.OpenAI)
	repo := store.(configstore.LiveModelListingStore)
	source, err := repo.GetLiveModelSource(t.Context(), schemas.OpenAI, "one")
	require.NoError(t, err)
	syncLiveFixture(t, mc, source, "old-model")
	delayed := &delayedLiveListingStore{ConfigStore: store, LiveModelListingStore: repo, loaded: make(chan struct{}), resume: make(chan struct{})}
	mc.configStore = delayed
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- mc.ReloadLiveModelsFromDB(ctx, schemas.OpenAI, config, generation, "one") }()
	select {
	case <-delayed.loaded:
	case <-ctx.Done():
		t.Fatal("reload did not read its initial snapshot")
	}
	key, err := store.GetProviderKey(ctx, schemas.OpenAI, "one")
	require.NoError(t, err)
	key.Value = *schemas.NewSecretVar("replacement-credential")
	require.NoError(t, store.UpdateProviderKey(ctx, schemas.OpenAI, "one", *key))
	mc.InvalidateLive(schemas.OpenAI, "one")
	current, err := repo.GetLiveModelSource(ctx, schemas.OpenAI, "one")
	require.NoError(t, err)
	syncLiveFixture(t, mc, current, "fresh-model")
	close(delayed.resume)
	require.NoError(t, <-done)
	require.Contains(t, mc.GetModelsForProvider(schemas.OpenAI), "fresh-model")
	require.NotContains(t, mc.GetModelsForProvider(schemas.OpenAI), "old-model")
	require.NoError(t, mc.ReloadLiveModelsFromDB(ctx, schemas.OpenAI, config, generation, "one"))
	require.Contains(t, mc.GetModelsForProvider(schemas.OpenAI), "fresh-model", "a caller snapshot that became stale before the call must not erase newer data")
}
