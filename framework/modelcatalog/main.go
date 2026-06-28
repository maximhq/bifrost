// Package modelcatalog composes three subpackages — datasheet (pricing +
// model parameters + capabilities), live (per-(provider, keyID) list-models
// cache), and keyconfig (per-provider allow/block/aliases derived from
// keys) — into the ModelCatalog facade that consumers (governance,
// telemetry, logging, server, etc.) use.
//
// The composer owns I/O orchestration: the hourly pricing sync ticker, the
// distributed lock used during sync, and the gossip after-sync hook.
// Subpackages perform no I/O directly — they expose Load/Sync methods the
// composer calls.
package modelcatalog

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/modelcatalog/datasheet"
	"github.com/maximhq/bifrost/framework/modelcatalog/keyconfig"
	"github.com/maximhq/bifrost/framework/modelcatalog/live"
)

type ModelCatalog struct {
	configStore            configstore.ConfigStore
	distributedLockManager *configstore.DistributedLockManager
	logger                 schemas.Logger

	datasheet *datasheet.Store
	live      *live.Store
	keyconf   *keyconfig.Store

	// MCP library sync configuration (protected by syncMu)
	mcpLibraryURL          string
	mcpLibrarySyncInterval time.Duration
	lastMCPLibrarySyncedAt time.Time
	syncMu                 sync.RWMutex

	shouldSyncGate func(ctx context.Context) bool
	afterSyncHook  func(ctx context.Context)

	// Background sync orchestration. The ticker, distributed lock, and gossip
	// hook live at this level — datasheet.Store has no internal scheduler.
	syncTicker *time.Ticker
	syncCtx    context.Context
	syncCancel context.CancelFunc
	done       chan struct{}
	wg         sync.WaitGroup
}

func Init(ctx context.Context, config *Config, configStore configstore.ConfigStore, logger schemas.Logger) (*ModelCatalog, error) {
	pricingURL := DefaultPricingURL
	if config != nil && config.PricingURL != nil {
		pricingURL = *config.PricingURL
	}
	modelParametersURL := DefaultModelParametersURL
	if config != nil && config.ModelParametersURL != nil && *config.ModelParametersURL != "" {
		modelParametersURL = *config.ModelParametersURL
	}
	mcpLibraryURL := DefaultMCPLibraryURL
	if config != nil && config.MCPLibraryURL != nil && *config.MCPLibraryURL != "" {
		mcpLibraryURL = *config.MCPLibraryURL
	}
	mcpLibrarySyncInterval := DefaultSyncInterval
	if config != nil && config.MCPLibrarySyncInterval != nil && *config.MCPLibrarySyncInterval > 0 {
		mcpLibrarySyncInterval = time.Duration(*config.MCPLibrarySyncInterval) * time.Second
	}
	syncInterval := DefaultSyncInterval
	if config != nil && config.PricingSyncInterval != nil {
		syncInterval = time.Duration(*config.PricingSyncInterval) * time.Second
	}

	// Log the active interval and the scheduler's actual check frequency so operators
	// are not surprised that setting interval=1h does not mean checks happen every second.
	logger.Info("pricing sync interval set to %v (scheduler checks every %v)", syncInterval, syncWorkerTickerPeriod)

	mc := &ModelCatalog{
		mcpLibraryURL:          mcpLibraryURL,
		mcpLibrarySyncInterval: mcpLibrarySyncInterval,
		configStore:            configStore,
		logger:                 logger,
		distributedLockManager: configstore.NewDistributedLockManager(configStore, logger, configstore.WithDefaultTTL(30*time.Second)),
		datasheet: datasheet.New(configStore, logger, datasheet.Config{
			URL:                pricingURL,
			ModelParametersURL: modelParametersURL,
			SyncInterval:       syncInterval,
		}),
		live:    live.New(logger),
		keyconf: keyconfig.New(logger),
		done:    make(chan struct{}),
	}
	mc.syncCtx, mc.syncCancel = context.WithCancel(ctx)

	// If Init returns an error the caller never owns mc and will never call
	// Cleanup(), so cancel syncCtx to stop any background goroutines that
	// were already spawned before the failure.
	initSucceeded := false
	defer func() {
		if !initSucceeded {
			mc.syncCancel()
		}
	}()

	logger.Info("initializing model catalog...")
	if configStore != nil {
		// Lazy load on cache miss: providers may need params for models not
		// covered by the startup bulk load (e.g. just-uploaded models). The
		// bulk load still warms the common case so this only fires on misses.
		providerUtils.SetCacheMissHandler(func(model string) *providerUtils.ModelParams {
			missCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			params, err := mc.datasheet.GetModelParametersByModel(missCtx, model)
			if err != nil || params == nil {
				return nil
			}
			var p struct {
				MaxOutputTokens       *int  `json:"max_output_tokens"`
				VertexMultiRegionOnly *bool `json:"vertex_multi_region_only"`
			}
			if err := json.Unmarshal([]byte(params.Data), &p); err != nil {
				return nil
			}
			if p.MaxOutputTokens == nil && p.VertexMultiRegionOnly == nil {
				return nil
			}
			return &providerUtils.ModelParams{
				MaxOutputTokens:         p.MaxOutputTokens,
				IsVertexMultiRegionOnly: p.VertexMultiRegionOnly,
			}
		})

		var wg sync.WaitGroup
		var pricingErr, paramsErr error
		wg.Add(2)
		go func() {
			defer wg.Done()
			if err := mc.datasheet.LoadFromDB(ctx); err != nil {
				pricingErr = fmt.Errorf("failed to load initial pricing data: %w", err)
				return
			}
			if mc.hasPricingData() {
				logger.Info("existing pricing data found in database, syncing from URL in background")
				mc.wg.Add(1)
				go func() {
					defer mc.wg.Done()
					if err := mc.withDistributedLock(mc.syncCtx, "model_catalog_pricing_startup_sync", 10, func() error {
						return mc.runPricingSync(mc.syncCtx)
					}); err != nil {
						logger.Warn("background startup pricing sync failed: %v", err)
					} else {
						logger.Info("background startup pricing sync completed successfully")
					}
				}()
			} else {
				if err := mc.withDistributedLock(ctx, "model_catalog_pricing_startup_sync", 10, func() error {
					return mc.runPricingSync(ctx)
				}); err != nil {
					pricingErr = fmt.Errorf("failed to sync pricing data: %w", err)
				}
			}
		}()
		go func() {
			defer wg.Done()
			n, err := mc.datasheet.LoadModelParamsFromDB(ctx)
			if err != nil {
				paramsErr = fmt.Errorf("failed to load initial model parameters: %w", err)
				return
			}
			if n > 0 {
				logger.Info("existing model parameters found in database (%d records), syncing from URL in background", n)
				mc.wg.Add(1)
				go func() {
					defer mc.wg.Done()
					if err := mc.withDistributedLock(mc.syncCtx, "model_catalog_params_startup_sync", 10, func() error {
						return mc.runParamsSync(mc.syncCtx)
					}); err != nil {
						logger.Warn("background startup model parameters sync failed: %v", err)
					} else {
						logger.Info("background startup model parameters sync completed successfully")
					}
				}()
			} else {
				if err := mc.withDistributedLock(ctx, "model_catalog_params_startup_sync", 10, func() error {
					return mc.runParamsSync(ctx)
				}); err != nil {
					paramsErr = fmt.Errorf("failed to sync model parameters data: %w", err)
				}
			}
		}()
		wg.Wait()
		if pricingErr != nil {
			return nil, pricingErr
		}
		if paramsErr != nil {
			return nil, paramsErr
		}

		// MCP library catalog follows the datasheet bootstrap pattern: if the DB
		// already has catalog rows, refresh from URL in the background; if it is
		// empty, block startup until the first remote sync lands so the library page
		// is populated immediately after boot.
		hasMCPLibraryData, err := mc.hasMCPLibraryData(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to load initial MCP library data: %w", err)
		}
		if hasMCPLibraryData {
			logger.Info("existing MCP library data found in database, syncing from URL in background")
			mc.wg.Add(1)
			go func() {
				defer mc.wg.Done()
				if err := mc.withDistributedLock(mc.syncCtx, "model_catalog_mcp_library_startup_sync", 10, func() error {
					return mc.syncMCPLibrary(mc.syncCtx)
				}); err != nil {
					mc.logger.Warn("background startup MCP library sync failed: %v", err)
				} else {
					mc.syncMu.Lock()
					mc.lastMCPLibrarySyncedAt = time.Now()
					mc.syncMu.Unlock()
				}
			}()
		} else {
			// Empty DB: attempt a blocking sync so the library page is populated
			// immediately after boot. Unlike pricing, a failure here is non-fatal
			// — the background worker will retry on the next tick.
			if err := mc.withDistributedLock(ctx, "model_catalog_mcp_library_startup_sync", 10, func() error {
				return mc.syncMCPLibrary(ctx)
			}); err != nil {
				logger.Warn("initial MCP library sync failed (will retry in background): %v", err)
			} else {
				mc.syncMu.Lock()
				mc.lastMCPLibrarySyncedAt = time.Now()
				mc.syncMu.Unlock()
			}
		}
	} else {
		if err := mc.datasheet.LoadFromURLIntoMemory(ctx); err != nil {
			return nil, fmt.Errorf("failed to load pricing data into memory: %w", err)
		}
		if err := mc.datasheet.LoadModelParamsFromURLIntoMemory(ctx); err != nil {
			return nil, fmt.Errorf("failed to load model parameters from URL: %w", err)
		}
	}

	mc.datasheet.MarkSynced(time.Now())

	if err := mc.datasheet.LoadOverridesFromStore(ctx); err != nil {
		return nil, fmt.Errorf("failed to load pricing overrides: %w", err)
	}

	mc.startSyncWorker(mc.syncCtx)
	initSucceeded = true
	return mc, nil
}

func (mc *ModelCatalog) SetShouldSyncGate(fn func(ctx context.Context) bool) {
	mc.shouldSyncGate = fn
}

// SetAfterSyncHook registers a callback invoked after every successful
// URL → DB pricing sync. In enterprise this broadcasts a gossip message so
// other pods reload from DB.
func (mc *ModelCatalog) SetAfterSyncHook(fn func(ctx context.Context)) {
	mc.afterSyncHook = fn
}

// ReloadFromDB reloads pricing + model-parameters caches from the database.
// Gossip handler on non-leader pods.
func (mc *ModelCatalog) ReloadFromDB(ctx context.Context) error {
	if err := mc.datasheet.LoadFromDB(ctx); err != nil {
		return err
	}
	_, err := mc.datasheet.LoadModelParamsFromDB(ctx)
	return err
}

// ReloadPricing re-reads the pricing table into the in-memory cache. The
// management API uses this after a batched write so the new attributes are
// observable immediately. The 24-hour ticker still owns refreshing pricing
// fields from the upstream datasheet; this just refreshes the cache.
func (mc *ModelCatalog) ReloadPricing(ctx context.Context) error {
	return mc.datasheet.LoadFromDB(ctx)
}

// UpdateSyncConfig updates the pricing/params URLs and sync interval,
// restarts the background sync worker, then runs a full sync cycle.
func (mc *ModelCatalog) UpdateSyncConfig(ctx context.Context, config *Config) error {
	if mc.syncCancel != nil {
		mc.syncCancel()
	}
	if mc.syncTicker != nil {
		mc.syncTicker.Stop()
	}

	pricingURL := DefaultPricingURL
	if config != nil && config.PricingURL != nil {
		pricingURL = *config.PricingURL
	}
	modelParametersURL := DefaultModelParametersURL
	if config != nil && config.ModelParametersURL != nil && *config.ModelParametersURL != "" {
		modelParametersURL = *config.ModelParametersURL
	}
	mcpLibraryURL := DefaultMCPLibraryURL
	if config != nil && config.MCPLibraryURL != nil && *config.MCPLibraryURL != "" {
		mcpLibraryURL = *config.MCPLibraryURL
	}
	mcpLibrarySyncInterval := DefaultSyncInterval
	if config != nil && config.MCPLibrarySyncInterval != nil && *config.MCPLibrarySyncInterval > 0 {
		mcpLibrarySyncInterval = time.Duration(*config.MCPLibrarySyncInterval) * time.Second
	}
	mc.syncMu.Lock()
	mc.mcpLibraryURL = mcpLibraryURL
	mc.mcpLibrarySyncInterval = mcpLibrarySyncInterval
	mc.syncMu.Unlock()

	syncInterval := DefaultSyncInterval
	if config != nil && config.PricingSyncInterval != nil {
		syncInterval = time.Duration(*config.PricingSyncInterval) * time.Second
	}
	mc.datasheet.UpdateSyncConfig(datasheet.Config{
		URL:                pricingURL,
		ModelParametersURL: modelParametersURL,
		SyncInterval:       syncInterval,
	})

	mc.syncCtx, mc.syncCancel = context.WithCancel(ctx)
	mc.startSyncWorker(mc.syncCtx)

	return mc.ForceReloadPricing(ctx)
}

// ForceReloadPricing triggers an immediate URL→DB→memory sync for pricing
// and model parameters in parallel, fires the gossip hook, and resets the
// ticker so the next scheduled sync waits a full interval from now.
//
// Behavior change from pre-refactor: this no longer touches the live
// list-models cache. List-models refresh is now driven by key/provider
// edits, not by pricing reloads.
func (mc *ModelCatalog) ForceReloadPricing(ctx context.Context) error {
	timeout := datasheet.DefaultPricingTimeout
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	var wg sync.WaitGroup
	var pricingErr, paramsErr error
	wg.Add(2)
	go func() {
		defer wg.Done()
		if err := mc.runPricingSync(ctx); err != nil {
			pricingErr = fmt.Errorf("failed to sync pricing data: %w", err)
			return
		}
		if err := mc.datasheet.LoadOverridesFromStore(ctx); err != nil {
			pricingErr = fmt.Errorf("failed to load pricing overrides: %w", err)
		}
	}()
	go func() {
		defer wg.Done()
		if err := mc.runParamsSync(ctx); err != nil {
			paramsErr = fmt.Errorf("failed to sync model parameters: %w", err)
		}
	}()

	// MCP library sync runs alongside but is non-fatal: a failure here must not
	// block a pricing/params force-reload. It is logged and the last-sync
	// timestamp is only advanced on success.
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := mc.syncMCPLibrary(ctx); err != nil {
			mc.logger.Warn("MCP library sync during force-reload failed: %v", err)
			return
		}
		mc.syncMu.Lock()
		mc.lastMCPLibrarySyncedAt = time.Now()
		mc.syncMu.Unlock()
	}()

	wg.Wait()
	if pricingErr != nil {
		return pricingErr
	}
	if paramsErr != nil {
		return paramsErr
	}

	if mc.afterSyncHook != nil {
		mc.afterSyncHook(ctx)
	}

	if mc.syncTicker != nil {
		mc.syncTicker.Reset(mc.datasheet.SyncInterval())
	}
	return nil
}

func (mc *ModelCatalog) Cleanup() error {
	if mc.syncCancel != nil {
		mc.syncCancel()
	}
	if mc.syncTicker != nil {
		mc.syncTicker.Stop()
	}
	close(mc.done)
	mc.wg.Wait()
	return nil
}

// --- Sync ticker (orchestrates datasheet.Store sync methods) ---

func (mc *ModelCatalog) startSyncWorker(ctx context.Context) {
	// IMPORTANT scheduling model:
	//
	// The sync worker wakes on a fixed ticker (syncWorkerTickerPeriod = 1h).
	// On each wake it checks time.Since(LastSyncedAt) >= SyncInterval.
	// This means SyncInterval defines the *minimum elapsed time* between syncs,
	// and the actual frequency = max(syncWorkerTickerPeriod, SyncInterval).
	// Setting SyncInterval below the ticker period has no effect — the hourly
	// ticker is the hard lower bound on check granularity.
	mc.syncTicker = time.NewTicker(syncWorkerTickerPeriod)
	mc.wg.Add(1)
	go mc.syncWorker(ctx)
}

func (mc *ModelCatalog) syncWorker(ctx context.Context) {
	// Capture the ticker once so the select loop doesn't race with
	// UpdateSyncConfig overwriting mc.syncTicker while this goroutine
	// is still draining after mc.syncCancel().
	ticker := mc.syncTicker
	defer mc.wg.Done()
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			mc.syncTick(ctx)
		case <-mc.done:
			return
		}
	}
}

func (mc *ModelCatalog) syncTick(ctx context.Context) {
	pricingDue := time.Since(mc.datasheet.LastSyncedAt()) >= mc.datasheet.SyncInterval()
	mcpLibraryDue := mc.isMCPLibrarySyncDue()
	if !pricingDue && !mcpLibraryDue {
		return
	}
	mc.logger.Debug("starting model catalog background sync")

	// Pricing and MCP library use separate distributed locks so their
	// independent cadences don't block each other.
	var outerWg sync.WaitGroup
	if pricingDue {
		outerWg.Add(1)
		go func() {
			defer outerWg.Done()
			if err := mc.withDistributedLock(ctx, "model_catalog_pricing_sync", 10, func() error {
				var wg sync.WaitGroup
				var pricingErr, paramsErr error
				wg.Add(2)
				go func() {
					defer wg.Done()
					if err := mc.runPricingSync(ctx); err != nil {
						mc.logger.Error("background pricing sync failed: %v", err)
						pricingErr = err
					}
				}()
				go func() {
					defer wg.Done()
					if err := mc.runParamsSync(ctx); err != nil {
						mc.logger.Error("background model parameters sync failed: %v", err)
						paramsErr = err
					}
				}()
				wg.Wait()
				if pricingErr == nil && paramsErr == nil {
					if mc.afterSyncHook != nil {
						mc.afterSyncHook(ctx)
					}
					mc.datasheet.MarkSynced(time.Now())
				}
				if pricingErr != nil {
					return pricingErr
				}
				return paramsErr
			}); err != nil {
				mc.logger.Error("failed to run pricing sync: %v", err)
			}
		}()
	}
	if mcpLibraryDue {
		outerWg.Add(1)
		go func() {
			defer outerWg.Done()
			if err := mc.withDistributedLock(ctx, "model_catalog_mcp_library_sync", 10, func() error {
				if err := mc.syncMCPLibrary(ctx); err != nil {
					mc.logger.Error("background MCP library sync failed: %v", err)
					return err
				}
				mc.syncMu.Lock()
				mc.lastMCPLibrarySyncedAt = time.Now()
				mc.syncMu.Unlock()
				return nil
			}); err != nil {
				mc.logger.Error("failed to run MCP library sync: %v", err)
			}
		}()
	}
	outerWg.Wait()
	mc.logger.Debug("model catalog background sync completed")
}

// runPricingSync wraps the datasheet pricing sync with the gate check.
func (mc *ModelCatalog) runPricingSync(ctx context.Context) error {
	if mc.shouldSyncGate != nil && !mc.shouldSyncGate(ctx) {
		return nil
	}
	return mc.datasheet.SyncFromURL(ctx)
}

// runParamsSync wraps the datasheet params sync with the gate check.
func (mc *ModelCatalog) runParamsSync(ctx context.Context) error {
	if mc.shouldSyncGate != nil && !mc.shouldSyncGate(ctx) {
		mc.logger.Debug("model parameters sync cancelled by custom gate")
		return nil
	}
	return mc.datasheet.SyncModelParamsFromURL(ctx)
}

// withDistributedLock acquires a named distributed lock and runs fn under
// it. retries=0 blocks until acquired; retries>0 uses LockWithRetry. The
// unlock uses a fresh context so cancelled work contexts don't leak the
// lock until TTL expiry.
func (mc *ModelCatalog) withDistributedLock(ctx context.Context, key string, retries int, fn func() error) error {
	lock, err := mc.distributedLockManager.NewLock(key)
	if err != nil {
		return fmt.Errorf("failed to create lock %q: %w", key, err)
	}
	if retries > 0 {
		if err := lock.LockWithRetry(ctx, retries); err != nil {
			return fmt.Errorf("failed to acquire lock %q: %w", key, err)
		}
	} else {
		if err := lock.Lock(ctx); err != nil {
			return fmt.Errorf("failed to acquire lock %q: %w", key, err)
		}
	}
	defer func() {
		if err := lock.Unlock(context.Background()); err != nil {
			mc.logger.Warn("failed to release distributed lock %q: %v", key, err)
		}
	}()
	return fn()
}

// hasPricingData reports whether the datasheet store currently has any
// pricing rows in memory. Used during Init to decide between blocking sync
// and background sync.
func (mc *ModelCatalog) hasPricingData() bool {
	return len(mc.datasheet.DatasheetProviders()) > 0
}

func (mc *ModelCatalog) hasMCPLibraryData(ctx context.Context) (bool, error) {
	if mc.configStore == nil {
		return false, nil
	}
	_, totalCount, err := mc.configStore.GetMCPLibraryPaginated(ctx, configstore.MCPLibraryQueryParams{Limit: 1})
	if err != nil {
		return false, err
	}
	return totalCount > 0, nil
}

func (mc *ModelCatalog) isMCPLibrarySyncDue() bool {
	if mc.configStore == nil {
		return false
	}
	mc.syncMu.RLock()
	lastSyncedAt := mc.lastMCPLibrarySyncedAt
	syncInterval := mc.mcpLibrarySyncInterval
	mc.syncMu.RUnlock()
	if syncInterval <= 0 {
		syncInterval = DefaultSyncInterval
	}
	return lastSyncedAt.IsZero() || time.Since(lastSyncedAt) >= syncInterval
}

// knownProviders returns the union of providers seen by any store. Used by
// GetProvidersForModel (models.go) to enumerate candidates.
func (mc *ModelCatalog) knownProviders() []schemas.ModelProvider {
	seen := make(map[schemas.ModelProvider]struct{})
	out := make([]schemas.ModelProvider, 0)
	for _, p := range mc.datasheet.DatasheetProviders() {
		if _, ok := seen[p]; !ok {
			seen[p] = struct{}{}
			out = append(out, p)
		}
	}
	for k := range mc.live.Snapshot() {
		if _, ok := seen[k.Provider]; !ok {
			seen[k.Provider] = struct{}{}
			out = append(out, k.Provider)
		}
	}
	for _, p := range mc.keyconf.Providers() {
		if _, ok := seen[p]; !ok {
			seen[p] = struct{}{}
			out = append(out, p)
		}
	}
	return out
}

// NewTestCatalog constructs a minimal ModelCatalog for unit tests. Does not
// start background workers or hit external services.
func NewTestCatalog(baseModelIndex map[string]string) *ModelCatalog {
	return &ModelCatalog{
		datasheet: datasheet.NewTestStore(baseModelIndex),
		live:      live.New(nil),
		keyconf:   keyconfig.New(nil),
		done:      make(chan struct{}),
	}
}

// NewTestCatalogWithDatasheet wraps a caller-provided datasheet.Store (e.g. one
// loaded from a local testdata pricing file via datasheet.New(...) +
// LoadFromURLIntoMemory) in a ModelCatalog, so tests in other packages can
// exercise real pricing/cost computation without reaching the network.
func NewTestCatalogWithDatasheet(ds *datasheet.Store) *ModelCatalog {
	return &ModelCatalog{
		datasheet: ds,
		live:      live.New(nil),
		keyconf:   keyconfig.New(nil),
		done:      make(chan struct{}),
	}
}
