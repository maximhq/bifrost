// Package modelcatalog provides a pricing manager for the framework.
package modelcatalog

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sync"
	"time"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
)

type ModelCatalog struct {
	configStore            configstore.ConfigStore
	distributedLockManager *configstore.DistributedLockManager

	logger schemas.Logger

	// Configuration fields (protected by syncMu)
	pricingURL         string
	modelParametersURL string
	syncInterval       time.Duration
	lastSyncedAt       time.Time
	syncMu             sync.RWMutex

	shouldSyncGate func(ctx context.Context) bool
	afterSyncHook  func(ctx context.Context)

	// In-memory cache for fast access - direct map for O(1) lookups
	pricingData map[string]configstoreTables.TableModelPricing
	mu          sync.RWMutex

	// rawOverrides is the canonical list of all active overrides. It exists solely
	// to support incremental mutations: UpsertPricingOverrides and DeletePricingOverride
	// iterate over it to rebuild the list, then derive customPricing from it.
	// customPricing is the actual lookup structure used at query time.
	rawOverrides  []PricingOverride
	customPricing *customPricingData
	overridesMu   sync.RWMutex

	modelPool           map[schemas.ModelProvider][]string
	unfilteredModelPool map[schemas.ModelProvider][]string // model pool without allowed models filtering
	baseModelIndex      map[string]string                  // model string → canonical base model name

	// Pre-parsed supported response types index (keyed by model name)
	// Values are normalized response types: "chat_completion", "responses", "text_completion"
	supportedResponseTypes map[string][]string

	// Pre-parsed supported parameters index (keyed by model name, populated from model parameters supported_parameters)
	// Values are parameter names the model accepts (e.g., "temperature", "top_p", "tools")
	supportedParams map[string][]string

	// modelCatalogData holds per-(model, provider) attribute blobs surfaced
	// from governance_model_catalog. The key is "<model>|<provider>" using the
	// raw datasheet provider string (not the normalized one) — matches the
	// upsert key. catalogMu guards reads and writes of the map.
	modelCatalogData map[string]*configstoreTables.TableModelCatalogEntry
	catalogMu        sync.RWMutex

	// Background sync worker
	syncTicker *time.Ticker
	done       chan struct{}
	wg         sync.WaitGroup
	syncCtx    context.Context
	syncCancel context.CancelFunc
}

// Init initializes the model catalog
func Init(ctx context.Context, config *Config, configStore configstore.ConfigStore, logger schemas.Logger) (*ModelCatalog, error) {
	// Initialize pricing URL and sync interval
	pricingURL := DefaultPricingURL
	if config.PricingURL != nil {
		pricingURL = *config.PricingURL
	}
	modelParametersURL := DefaultModelParametersURL
	if config.ModelParametersURL != nil && *config.ModelParametersURL != "" {
		modelParametersURL = *config.ModelParametersURL
	}
	syncInterval := DefaultSyncInterval
	if config.PricingSyncInterval != nil {
		syncInterval = time.Duration(*config.PricingSyncInterval) * time.Second
	}

	// Log the active interval and the scheduler's actual check frequency so operators
	// are not surprised that setting interval=1h does not mean checks happen every second.
	// Actual syncs occur when: (1) the 1-hour ticker fires AND (2) time.Since(lastSync) >= pricingSyncInterval.
	logger.Info("pricing sync interval set to %v (scheduler checks every %v)", syncInterval, syncWorkerTickerPeriod)

	mc := &ModelCatalog{
		pricingURL:             pricingURL,
		modelParametersURL:     modelParametersURL,
		syncInterval:           syncInterval,
		configStore:            configStore,
		logger:                 logger,
		pricingData:            make(map[string]configstoreTables.TableModelPricing),
		modelPool:              make(map[schemas.ModelProvider][]string),
		unfilteredModelPool:    make(map[schemas.ModelProvider][]string),
		baseModelIndex:         make(map[string]string),
		supportedResponseTypes: make(map[string][]string),
		supportedParams:        make(map[string][]string),
		modelCatalogData:       make(map[string]*configstoreTables.TableModelCatalogEntry),
		done:                   make(chan struct{}),
		distributedLockManager: configstore.NewDistributedLockManager(configStore, logger, configstore.WithDefaultTTL(30*time.Second)),
	}

	// Initialize syncCtx early so background startup goroutines can use it and
	// Cleanup() can cancel them. startSyncWorker is still called at the end after
	// cold-start paths have completed.
	mc.syncCtx, mc.syncCancel = context.WithCancel(ctx)

	// If Init returns an error the caller never owns mc and will never call
	// Cleanup(), so cancel syncCtx to stop any background goroutines that were
	// already spawned before the failure.
	initSucceeded := false
	defer func() {
		if !initSucceeded {
			mc.syncCancel()
		}
	}()

	logger.Info("initializing model catalog...")
	if configStore != nil {
		// Per-model lazy load when the in-memory cache misses (eviction, new models, or if
		// startup bulk load was skipped). loadModelParametersFromDatabase still bulk-warms
		// the cache on init and on ReloadFromDB so common paths avoid a DB read per model.
		providerUtils.SetCacheMissHandler(func(model string) *providerUtils.ModelParams {
			missCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			params, err := configStore.GetModelParametersByModel(missCtx, model)
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
			if err := mc.loadPricingFromDatabase(ctx); err != nil {
				pricingErr = fmt.Errorf("failed to load initial pricing data: %w", err)
				return
			}
			mc.mu.RLock()
			hasPricingData := len(mc.pricingData) > 0
			mc.mu.RUnlock()
			if hasPricingData {
				mc.logger.Info("existing pricing data found in database, syncing from URL in background")
				mc.wg.Add(1)
				go func() {
					defer mc.wg.Done()
					if err := mc.withDistributedLock(mc.syncCtx, "model_catalog_pricing_startup_sync", 10, func() error {
						return mc.syncPricing(mc.syncCtx)
					}); err != nil {
						mc.logger.Warn("background startup pricing sync failed: %v", err)
					} else {
						mc.logger.Info("background startup pricing sync completed successfully")
					}
				}()
			} else {
				if err := mc.withDistributedLock(ctx, "model_catalog_pricing_startup_sync", 10, func() error {
					return mc.syncPricing(ctx)
				}); err != nil {
					pricingErr = fmt.Errorf("failed to sync pricing data: %w", err)
				}
			}
		}()
		go func() {
			defer wg.Done()
			n, err := mc.loadModelParametersFromDatabase(ctx)
			if err != nil {
				paramsErr = fmt.Errorf("failed to load initial model parameters: %w", err)
				return
			}
			if n > 0 {
				mc.logger.Info("existing model parameters found in database (%d records), syncing from URL in background", n)
				mc.wg.Add(1)
				go func() {
					defer mc.wg.Done()
					if err := mc.withDistributedLock(mc.syncCtx, "model_catalog_params_startup_sync", 10, func() error {
						return mc.syncModelParameters(mc.syncCtx)
					}); err != nil {
						mc.logger.Warn("background startup model parameters sync failed: %v", err)
					} else {
						mc.logger.Info("background startup model parameters sync completed successfully")
					}
				}()
			} else {
				if err := mc.withDistributedLock(ctx, "model_catalog_params_startup_sync", 10, func() error {
					return mc.syncModelParameters(ctx)
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

		// Catalog load is sequenced after pricing/params: pricing sync may
		// upsert catalog rows from PricingEntry.Description, and we want the
		// in-memory map to observe those before the catalog is queried.
		if err := mc.loadCatalogFromDatabase(ctx); err != nil {
			return nil, fmt.Errorf("failed to load model catalog: %w", err)
		}
	} else {
		// Load pricing and model parameters from URL into memory (no config store)
		if err := mc.loadPricingIntoMemoryFromURL(ctx); err != nil {
			return nil, fmt.Errorf("failed to load pricing data from config memory: %w", err)
		}
		if err := mc.loadModelParametersIntoMemoryFromURL(ctx); err != nil {
			return nil, fmt.Errorf("failed to load model parameters from URL: %w", err)
		}
	}

	mc.syncMu.Lock()
	mc.lastSyncedAt = time.Now()
	mc.syncMu.Unlock()

	// Populate model pool with normalized providers from pricing data
	mc.populateModelPoolFromPricingData()

	if err := mc.loadPricingOverridesFromStore(ctx); err != nil {
		return nil, fmt.Errorf("failed to load pricing overrides: %w", err)
	}

	// Start background sync worker
	mc.startSyncWorker(mc.syncCtx)
	initSucceeded = true
	return mc, nil
}

func (mc *ModelCatalog) SetShouldSyncGate(shouldSyncGate func(ctx context.Context) bool) {
	mc.shouldSyncGate = shouldSyncGate
}

// SetAfterSyncHook registers a callback invoked after every successful URL → DB pricing sync.
// In enterprise this is used to broadcast a gossip message so other pods reload from DB.
func (mc *ModelCatalog) SetAfterSyncHook(fn func(ctx context.Context)) {
	mc.afterSyncHook = fn
}

// ReloadFromDB reloads the in-memory pricing cache and model-parameters provider cache from the database.
// In enterprise this is called on non-leader pods when they receive a gossip sync notification.
func (mc *ModelCatalog) ReloadFromDB(ctx context.Context) error {
	if err := mc.loadPricingFromDatabase(ctx); err != nil {
		return err
	}
	mc.populateModelPoolFromPricingData()
	if _, err := mc.loadModelParametersFromDatabase(ctx); err != nil {
		return err
	}
	return mc.loadCatalogFromDatabase(ctx)
}

// UpdateSyncConfig updates the pricing URL and sync interval, restarts the background sync worker,
// then delegates to ForceReloadPricing for a full sync cycle.
func (mc *ModelCatalog) UpdateSyncConfig(ctx context.Context, config *Config) error {
	// Acquire pricing mutex to update configuration atomically
	mc.syncMu.Lock()

	// Stop existing sync worker before updating configuration
	if mc.syncCancel != nil {
		mc.syncCancel()
	}
	if mc.syncTicker != nil {
		mc.syncTicker.Stop()
	}

	// Update pricing configuration
	mc.pricingURL = DefaultPricingURL
	if config.PricingURL != nil {
		mc.pricingURL = *config.PricingURL
	}

	mc.modelParametersURL = DefaultModelParametersURL
	if config.ModelParametersURL != nil && *config.ModelParametersURL != "" {
		mc.modelParametersURL = *config.ModelParametersURL
	}

	mc.syncInterval = DefaultSyncInterval
	if config.PricingSyncInterval != nil {
		mc.syncInterval = time.Duration(*config.PricingSyncInterval) * time.Second
	}

	// Create new sync worker with updated configuration
	mc.syncCtx, mc.syncCancel = context.WithCancel(ctx)
	mc.startSyncWorker(mc.syncCtx)

	mc.syncMu.Unlock()

	// Delegate to ForceReloadPricing for a complete sync cycle
	return mc.ForceReloadPricing(ctx)
}

func (mc *ModelCatalog) ForceReloadPricing(ctx context.Context) error {
	timeout := DefaultPricingTimeout
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	// Run pricing sync and model parameters sync in parallel
	var wg sync.WaitGroup
	var pricingErr, paramsErr error

	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := mc.syncPricing(ctx); err != nil {
			pricingErr = fmt.Errorf("failed to sync pricing data: %w", err)
			return
		}

		// Rebuild model pool from updated pricing data
		mc.populateModelPoolFromPricingData()

		if err := mc.loadPricingOverridesFromStore(ctx); err != nil {
			pricingErr = fmt.Errorf("failed to load pricing overrides: %w", err)
			return
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := mc.syncModelParameters(ctx); err != nil {
			paramsErr = fmt.Errorf("failed to sync model parameters: %w", err)
			return
		}
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

	mc.syncMu.Lock()
	// Reset the ticker so the next scheduled sync waits a full interval from now
	if mc.syncTicker != nil {
		mc.syncTicker.Reset(mc.syncInterval)
	}
	mc.syncMu.Unlock()

	return nil
}

// getPricingURL returns a copy of the pricing URL under mutex protection
func (mc *ModelCatalog) getPricingURL() string {
	mc.syncMu.RLock()
	defer mc.syncMu.RUnlock()
	return mc.pricingURL
}

func (mc *ModelCatalog) getModelParametersURL() string {
	mc.syncMu.RLock()
	defer mc.syncMu.RUnlock()
	return mc.modelParametersURL
}

// IsRequestTypeSupported checks if a model supports chat completion.
// It checks the supportedResponseTypes index.
func (mc *ModelCatalog) IsRequestTypeSupported(model string, provider schemas.ModelProvider, requestType schemas.RequestType) bool {
	mc.mu.RLock()
	defer mc.mu.RUnlock()
	outputs, ok := mc.supportedResponseTypes[model]
	return ok && slices.Contains(outputs, string(requestType))
}

// GetSupportedParameters returns the list of supported parameter names for a model.
// Returns nil if the model is not found in the catalog.
func (mc *ModelCatalog) GetSupportedParameters(model string) []string {
	mc.mu.RLock()
	params, ok := mc.supportedParams[model]
	mc.mu.RUnlock()
	if !ok {
		return nil
	}
	// Return a copy to prevent external modification
	result := make([]string, len(params))
	copy(result, params)
	return result
}

// populateModelPool populates the model pool with all available models per provider (thread-safe)
func (mc *ModelCatalog) populateModelPoolFromPricingData() {
	// Acquire write lock for the entire rebuild operation
	mc.mu.Lock()
	defer mc.mu.Unlock()

	// Clear existing model pool and base model index
	mc.modelPool = make(map[schemas.ModelProvider][]string)
	mc.unfilteredModelPool = make(map[schemas.ModelProvider][]string)
	mc.baseModelIndex = make(map[string]string)

	// Map to track unique models per provider
	providerModels := make(map[schemas.ModelProvider]map[string]bool)

	// Iterate through all pricing data to collect models per provider
	for _, pricing := range mc.pricingData {
		// Normalize provider before adding to model pool
		normalizedProvider := schemas.ModelProvider(normalizeProvider(pricing.Provider))

		// Initialize map for this provider if not exists
		if providerModels[normalizedProvider] == nil {
			providerModels[normalizedProvider] = make(map[string]bool)
		}

		// Add model to the provider's model set (using map for deduplication)
		providerModels[normalizedProvider][pricing.Model] = true

		// Build base model index from pre-computed base_model field
		if pricing.BaseModel != "" {
			mc.baseModelIndex[pricing.Model] = pricing.BaseModel
		}
	}

	// Convert sets to slices and assign to modelPool
	for provider, modelSet := range providerModels {
		models := make([]string, 0, len(modelSet))
		for model := range modelSet {
			models = append(models, model)
		}
		mc.modelPool[provider] = models
		mc.unfilteredModelPool[provider] = models
	}

	// Log the populated model pool for debugging
	totalModels := 0
	for provider, models := range mc.modelPool {
		totalModels += len(models)
		mc.logger.Debug("populated %d models for provider %s", len(models), string(provider))
	}
	mc.logger.Info("populated model pool with %d models across %d providers", totalModels, len(mc.modelPool))
}

// Cleanup cleans up the model catalog
func (mc *ModelCatalog) Cleanup() error {
	if mc.syncCancel != nil {
		mc.syncCancel()
	}

	mc.syncMu.Lock()
	if mc.syncTicker != nil {
		mc.syncTicker.Stop()
	}
	mc.syncMu.Unlock()

	close(mc.done)
	mc.wg.Wait()

	return nil
}

// GetModelCatalogEntry returns the cached catalog entry for (model, provider),
// or nil when no entry exists. The provider is matched against the raw upsert
// key used when the entry was written (matches schemas.ModelProvider casing).
func (mc *ModelCatalog) GetModelCatalogEntry(model string, provider schemas.ModelProvider) *configstoreTables.TableModelCatalogEntry {
	mc.catalogMu.RLock()
	defer mc.catalogMu.RUnlock()
	return mc.modelCatalogData[catalogKey(model, string(provider))]
}

// UpsertModelCatalogEntry persists an entry and refreshes the in-memory cache.
// Safe to call from both the management CRUD API and the pricing-sync path.
func (mc *ModelCatalog) UpsertModelCatalogEntry(ctx context.Context, entry *configstoreTables.TableModelCatalogEntry) error {
	if mc.configStore == nil {
		// In-memory-only deployment: still maintain the cache so reads return
		// the value the caller just wrote.
		mc.catalogMu.Lock()
		mc.modelCatalogData[catalogKey(entry.Model, entry.Provider)] = entry
		mc.catalogMu.Unlock()
		return nil
	}
	if err := mc.configStore.UpsertModelCatalogEntry(ctx, entry); err != nil {
		return err
	}
	return mc.loadCatalogFromDatabase(ctx)
}

// DeleteModelCatalogEntry removes an entry by primary key and reloads the cache.
func (mc *ModelCatalog) DeleteModelCatalogEntry(ctx context.Context, id uint) error {
	if mc.configStore == nil {
		return nil
	}
	if err := mc.configStore.DeleteModelCatalogEntry(ctx, id); err != nil {
		return err
	}
	return mc.loadCatalogFromDatabase(ctx)
}

// ReloadCatalog re-reads the catalog table into the in-memory cache
func (mc *ModelCatalog) ReloadCatalog(ctx context.Context) error {
	return mc.loadCatalogFromDatabase(ctx)
}

// GetAllModelCatalogEntries returns every catalog row. Reads through the
// configstore so callers always see committed state.
func (mc *ModelCatalog) GetAllModelCatalogEntries(ctx context.Context) ([]configstoreTables.TableModelCatalogEntry, error) {
	if mc.configStore == nil {
		mc.catalogMu.RLock()
		defer mc.catalogMu.RUnlock()
		out := make([]configstoreTables.TableModelCatalogEntry, 0, len(mc.modelCatalogData))
		for _, e := range mc.modelCatalogData {
			out = append(out, *e)
		}
		return out, nil
	}
	return mc.configStore.GetAllModelCatalogEntries(ctx)
}

// catalogKey returns the lookup key used by modelCatalogData. Provider is the
// raw datasheet/store value (not normalized) so upserts and reads agree on the
// same key.
func catalogKey(model, provider string) string {
	return model + "|" + provider
}

// NewTestCatalog creates a minimal ModelCatalog for testing purposes.
// It does not start background sync workers or connect to external services.
func NewTestCatalog(baseModelIndex map[string]string) *ModelCatalog {
	if baseModelIndex == nil {
		baseModelIndex = make(map[string]string)
	}
	return &ModelCatalog{
		modelPool:              make(map[schemas.ModelProvider][]string),
		unfilteredModelPool:    make(map[schemas.ModelProvider][]string),
		baseModelIndex:         baseModelIndex,
		pricingData:            make(map[string]configstoreTables.TableModelPricing),
		supportedResponseTypes: make(map[string][]string),
		supportedParams:        make(map[string][]string),
		modelCatalogData:       make(map[string]*configstoreTables.TableModelCatalogEntry),
		done:                   make(chan struct{}),
	}
}
