// Package modelcatalog provides a pricing manager for the framework.
package modelcatalog

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
)

// Default sync interval and config key
const (
	TokenTierAbove272K = 272000
	TokenTierAbove200K = 200000
	TokenTierAbove128K = 128000
)

type ModelCatalog struct {
	configStore            configstore.ConfigStore
	distributedLockManager *configstore.DistributedLockManager

	logger schemas.Logger

	// Configuration fields (protected by syncMu)
	pricingURL   string
	syncInterval time.Duration
	lastSyncedAt time.Time
	syncMu       sync.RWMutex

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

	// Background sync worker
	syncTicker *time.Ticker
	done       chan struct{}
	wg         sync.WaitGroup
	syncCtx    context.Context
	syncCancel context.CancelFunc
}

// PricingEntry represents a single model's pricing information.
// Field names and JSON tags match the datasheet schema exactly.
type PricingEntry struct {
	BaseModel string `json:"base_model,omitempty"`
	Provider  string `json:"provider"`
	Mode      string `json:"mode"`

	ContextLength   *int                  `json:"context_length,omitempty"`
	MaxInputTokens  *int                  `json:"max_input_tokens,omitempty"`
	MaxOutputTokens *int                  `json:"max_output_tokens,omitempty"`
	Architecture    *schemas.Architecture `json:"architecture,omitempty"`

	// Costs - Text
	InputCostPerToken          float64  `json:"input_cost_per_token"`
	OutputCostPerToken         float64  `json:"output_cost_per_token"`
	InputCostPerTokenBatches   *float64 `json:"input_cost_per_token_batches,omitempty"`
	OutputCostPerTokenBatches  *float64 `json:"output_cost_per_token_batches,omitempty"`
	InputCostPerTokenPriority  *float64 `json:"input_cost_per_token_priority,omitempty"`
	OutputCostPerTokenPriority *float64 `json:"output_cost_per_token_priority,omitempty"`
	InputCostPerTokenFlex      *float64 `json:"input_cost_per_token_flex,omitempty"`
	OutputCostPerTokenFlex     *float64 `json:"output_cost_per_token_flex,omitempty"`
	InputCostPerCharacter      *float64 `json:"input_cost_per_character,omitempty"`
	// Costs - 128k Tier
	InputCostPerTokenAbove128kTokens          *float64 `json:"input_cost_per_token_above_128k_tokens,omitempty"`
	InputCostPerImageAbove128kTokens          *float64 `json:"input_cost_per_image_above_128k_tokens,omitempty"`
	InputCostPerVideoPerSecondAbove128kTokens *float64 `json:"input_cost_per_video_per_second_above_128k_tokens,omitempty"`
	InputCostPerAudioPerSecondAbove128kTokens *float64 `json:"input_cost_per_audio_per_second_above_128k_tokens,omitempty"`
	OutputCostPerTokenAbove128kTokens         *float64 `json:"output_cost_per_token_above_128k_tokens,omitempty"`
	// Costs - 200k Tier
	InputCostPerTokenAbove200kTokens          *float64 `json:"input_cost_per_token_above_200k_tokens,omitempty"`
	InputCostPerTokenAbove200kTokensPriority  *float64 `json:"input_cost_per_token_above_200k_tokens_priority,omitempty"`
	OutputCostPerTokenAbove200kTokens         *float64 `json:"output_cost_per_token_above_200k_tokens,omitempty"`
	OutputCostPerTokenAbove200kTokensPriority *float64 `json:"output_cost_per_token_above_200k_tokens_priority,omitempty"`
	// Costs - 272k Tier
	InputCostPerTokenAbove272kTokens          *float64 `json:"input_cost_per_token_above_272k_tokens,omitempty"`
	InputCostPerTokenAbove272kTokensPriority  *float64 `json:"input_cost_per_token_above_272k_tokens_priority,omitempty"`
	OutputCostPerTokenAbove272kTokens         *float64 `json:"output_cost_per_token_above_272k_tokens,omitempty"`
	OutputCostPerTokenAbove272kTokensPriority *float64 `json:"output_cost_per_token_above_272k_tokens_priority,omitempty"`

	// Costs - Cache
	CacheCreationInputTokenCost                        *float64 `json:"cache_creation_input_token_cost,omitempty"`
	CacheReadInputTokenCost                            *float64 `json:"cache_read_input_token_cost,omitempty"`
	CacheCreationInputTokenCostAbove200kTokens         *float64 `json:"cache_creation_input_token_cost_above_200k_tokens,omitempty"`
	CacheReadInputTokenCostAbove200kTokens             *float64 `json:"cache_read_input_token_cost_above_200k_tokens,omitempty"`
	CacheReadInputTokenCostAbove200kTokensPriority     *float64 `json:"cache_read_input_token_cost_above_200k_tokens_priority,omitempty"`
	CacheCreationInputTokenCostAbove1hr                *float64 `json:"cache_creation_input_token_cost_above_1hr,omitempty"`
	CacheCreationInputTokenCostAbove1hrAbove200kTokens *float64 `json:"cache_creation_input_token_cost_above_1hr_above_200k_tokens,omitempty"`
	CacheCreationInputAudioTokenCost                   *float64 `json:"cache_creation_input_audio_token_cost,omitempty"`
	CacheReadInputTokenCostPriority                    *float64 `json:"cache_read_input_token_cost_priority,omitempty"`
	CacheReadInputTokenCostFlex                        *float64 `json:"cache_read_input_token_cost_flex,omitempty"`
	CacheReadInputImageTokenCost                       *float64 `json:"cache_read_input_image_token_cost,omitempty"`
	CacheReadInputTokenCostAbove272kTokens             *float64 `json:"cache_read_input_token_cost_above_272k_tokens,omitempty"`
	CacheReadInputTokenCostAbove272kTokensPriority     *float64 `json:"cache_read_input_token_cost_above_272k_tokens_priority,omitempty"`

	// Costs - Image
	InputCostPerImage                             *float64 `json:"input_cost_per_image,omitempty"`
	InputCostPerPixel                             *float64 `json:"input_cost_per_pixel,omitempty"`
	OutputCostPerImage                            *float64 `json:"output_cost_per_image,omitempty"`
	OutputCostPerPixel                            *float64 `json:"output_cost_per_pixel,omitempty"`
	OutputCostPerImagePremiumImage                *float64 `json:"output_cost_per_image_premium_image,omitempty"`
	OutputCostPerImageAbove512x512Pixels          *float64 `json:"output_cost_per_image_above_512_and_512_pixels,omitempty"`
	OutputCostPerImageAbove512x512PixelsPremium   *float64 `json:"output_cost_per_image_above_512_and_512_pixels_and_premium_image,omitempty"`
	OutputCostPerImageAbove1024x1024Pixels        *float64 `json:"output_cost_per_image_above_1024_and_1024_pixels,omitempty"`
	OutputCostPerImageAbove1024x1024PixelsPremium *float64 `json:"output_cost_per_image_above_1024_and_1024_pixels_and_premium_image,omitempty"`
	OutputCostPerImageAbove2048x2048Pixels        *float64 `json:"output_cost_per_image_above_2048_and_2048_pixels,omitempty"`
	OutputCostPerImageAbove4096x4096Pixels        *float64 `json:"output_cost_per_image_above_4096_and_4096_pixels,omitempty"`
	OutputCostPerImageLowQuality                  *float64 `json:"output_cost_per_image_low_quality,omitempty"`
	OutputCostPerImageMediumQuality               *float64 `json:"output_cost_per_image_medium_quality,omitempty"`
	OutputCostPerImageHighQuality                 *float64 `json:"output_cost_per_image_high_quality,omitempty"`
	OutputCostPerImageAutoQuality                 *float64 `json:"output_cost_per_image_auto_quality,omitempty"`
	InputCostPerImageToken                        *float64 `json:"input_cost_per_image_token,omitempty"`
	OutputCostPerImageToken                       *float64 `json:"output_cost_per_image_token,omitempty"`

	// Costs - Audio/Video
	InputCostPerAudioToken      *float64 `json:"input_cost_per_audio_token,omitempty"`
	InputCostPerAudioPerSecond  *float64 `json:"input_cost_per_audio_per_second,omitempty"`
	InputCostPerSecond          *float64 `json:"input_cost_per_second,omitempty"`
	InputCostPerVideoPerSecond  *float64 `json:"input_cost_per_video_per_second,omitempty"`
	OutputCostPerAudioToken     *float64 `json:"output_cost_per_audio_token,omitempty"`
	OutputCostPerVideoPerSecond *float64 `json:"output_cost_per_video_per_second,omitempty"`
	OutputCostPerSecond         *float64 `json:"output_cost_per_second,omitempty"`

	// Costs - Other
	//
	// SearchContextCostPerQuery is stored as a single float64, but the pricing datasheet
	// represents it as a tiered object with three keys: search_context_size_low,
	// search_context_size_medium, and search_context_size_high.  For every provider except
	// Perplexity the three tier values are identical, so we collapse the object to its
	// medium tier value (falling back to low then high).  Perplexity always returns a
	// pre-computed total_cost in its usage response, so the per-query rate is never
	// consumed for that provider; the collapsed value is therefore correct in all cases.
	// See UnmarshalJSON below for the custom decoding logic.
	SearchContextCostPerQuery     *float64 `json:"search_context_cost_per_query,omitempty"`
	CodeInterpreterCostPerSession *float64 `json:"code_interpreter_cost_per_session,omitempty"`
}

// UnmarshalJSON implements json.Unmarshaler for PricingEntry.
// It handles the special case where search_context_cost_per_query may arrive as either
// a plain float64 or a tiered object {"search_context_size_low":…,
// "search_context_size_medium":…, "search_context_size_high":…}.
func (p *PricingEntry) UnmarshalJSON(data []byte) error {
	// Type alias breaks the UnmarshalJSON recursion while keeping all other fields.
	type PricingEntryAlias PricingEntry
	var raw struct {
		PricingEntryAlias
		SearchContextCostPerQuery *struct {
			Low    *float64 `json:"search_context_size_low"`
			Medium *float64 `json:"search_context_size_medium"`
			High   *float64 `json:"search_context_size_high"`
		} `json:"search_context_cost_per_query,omitempty"`
	}
	if err := sonic.Unmarshal(data, &raw); err != nil {
		return err
	}
	*p = PricingEntry(raw.PricingEntryAlias)

	// search_context_cost_per_query arrives as a tiered object – all three values are
	// equal for non-Perplexity providers; we prefer medium, then low, then high.
	// Perplexity always returns a pre-computed total_cost so the per-query rate is
	// never consumed for that provider.
	if q := raw.SearchContextCostPerQuery; q != nil {
		switch {
		case q.Medium != nil:
			p.SearchContextCostPerQuery = q.Medium
		case q.Low != nil:
			p.SearchContextCostPerQuery = q.Low
		case q.High != nil:
			p.SearchContextCostPerQuery = q.High
		}
	}
	return nil
}

// ShouldSyncPricingFunc is a function that determines if pricing data should be synced
// It returns a boolean indicating if syncing is needed
// It is completely optional and can be nil if not needed
// syncPricing function will be called if this function returns true
type ShouldSyncPricingFunc func(ctx context.Context) bool

// Init initializes the model catalog
func Init(ctx context.Context, config *Config, configStore configstore.ConfigStore, logger schemas.Logger) (*ModelCatalog, error) {
	// Initialize pricing URL and sync interval
	pricingURL := DefaultPricingURL
	if config.PricingURL != nil {
		pricingURL = *config.PricingURL
	}
	syncInterval := DefaultSyncInterval
	if config.PricingSyncInterval != nil {
		pricingSyncInterval = time.Duration(*config.PricingSyncInterval) * time.Second
	}
	// Log the active interval and the scheduler's actual check frequency so operators
	// are not surprised that setting interval=1h does not mean checks happen every second.
	// Actual syncs occur when: (1) the 1-hour ticker fires AND (2) time.Since(lastSync) >= pricingSyncInterval.
	logger.Info("pricing sync interval set to %v (scheduler checks every %v)", pricingSyncInterval, syncWorkerTickerPeriod)

	mc := &ModelCatalog{
		pricingURL:             pricingURL,
		syncInterval:           syncInterval,
		configStore:            configStore,
		logger:                 logger,
		pricingData:            make(map[string]configstoreTables.TableModelPricing),
		modelPool:              make(map[schemas.ModelProvider][]string),
		unfilteredModelPool:    make(map[schemas.ModelProvider][]string),
		baseModelIndex:         make(map[string]string),
		supportedResponseTypes: make(map[string][]string),
		supportedParams:        make(map[string][]string),
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
				MaxOutputTokens *int `json:"max_output_tokens"`
			}
			if err := json.Unmarshal([]byte(params.Data), &p); err != nil || p.MaxOutputTokens == nil {
				return nil
			}
			return &providerUtils.ModelParams{MaxOutputTokens: p.MaxOutputTokens}
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
	_, err := mc.loadModelParametersFromDatabase(ctx)
	return err
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

	mc.syncInterval = DefaultSyncInterval
	if config.PricingSyncInterval != nil {
		mc.pricingSyncInterval = time.Duration(*config.PricingSyncInterval) * time.Second
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
		done:                   make(chan struct{}),
	}
}
