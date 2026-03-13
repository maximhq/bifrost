// Package modelcatalog provides a pricing manager for the framework.
package modelcatalog

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
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

	// Pre-parsed supported outputs index (keyed by model name, populated from model parameters supported_endpoints)
	// Values are normalized output types: "chat_completion", "responses", "text_completion"
	supportedOutputs map[string][]string

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
	syncInterval := DefaultSyncInterval
	if config.PricingSyncInterval != nil {
		syncInterval = *config.PricingSyncInterval
	}

	mc := &ModelCatalog{
		pricingURL:             pricingURL,
		syncInterval:           syncInterval,
		configStore:            configStore,
		logger:                 logger,
		pricingData:            make(map[string]configstoreTables.TableModelPricing),
		modelPool:              make(map[schemas.ModelProvider][]string),
		unfilteredModelPool:    make(map[schemas.ModelProvider][]string),
		baseModelIndex:         make(map[string]string),
		supportedOutputs:       make(map[string][]string),
		done:                   make(chan struct{}),
		distributedLockManager: configstore.NewDistributedLockManager(configStore, logger, configstore.WithDefaultTTL(30*time.Second)),
	}

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
		lock, err := mc.distributedLockManager.NewLock("model_catalog_pricing_sync")
		if err != nil {
			return nil, fmt.Errorf("failed to create model catalog pricing sync lock: %w", err)
		}
		if err := lock.LockWithRetry(ctx, 10); err != nil {
			return nil, fmt.Errorf("failed to acquire model catalog pricing sync lock: %w", err)
		}
		defer lock.Unlock(ctx)
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
				go func() {
					if err := mc.syncPricing(context.Background()); err != nil {
						mc.logger.Warn("background startup pricing sync failed: %v", err)
					} else {
						mc.logger.Info("background startup pricing sync completed successfully")
					}
				}()
			} else {
				if err := mc.syncPricing(ctx); err != nil {
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
				go func() {
					if err := mc.syncModelParameters(context.Background()); err != nil {
						mc.logger.Warn("background startup model parameters sync failed: %v", err)
					} else {
						mc.logger.Info("background startup model parameters sync completed successfully")
					}
				}()
			} else {
				if err := mc.syncModelParameters(ctx); err != nil {
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
	mc.syncCtx, mc.syncCancel = context.WithCancel(ctx)
	mc.startSyncWorker(mc.syncCtx)
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
		mc.syncInterval = *config.PricingSyncInterval
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

// getPricingSyncInterval returns a copy of the pricing sync interval under mutex protection
func (mc *ModelCatalog) getPricingSyncInterval() time.Duration {
	mc.pricingMu.RLock()
	defer mc.pricingMu.RUnlock()
	return mc.pricingSyncInterval
}

// GetPricingEntryForModel returns the pricing data
func (mc *ModelCatalog) GetPricingEntryForModel(model string, provider schemas.ModelProvider) *PricingEntry {
	mc.mu.RLock()
	defer mc.mu.RUnlock()
	// Check all modes
	for _, mode := range []schemas.RequestType{
		schemas.TextCompletionRequest,
		schemas.ChatCompletionRequest,
		schemas.ResponsesRequest,
		schemas.EmbeddingRequest,
		schemas.RerankRequest,
		schemas.SpeechRequest,
		schemas.TranscriptionRequest,
		schemas.ImageGenerationRequest,
		schemas.ImageEditRequest,
		schemas.ImageVariationRequest,
		schemas.VideoGenerationRequest,
	} {
		key := makeKey(model, string(provider), normalizeRequestType(mode))
		pricing, ok := mc.pricingData[key]
		if ok {
			return convertTableModelPricingToPricingData(&pricing)
		}
	}
	return nil
}

// GetModelCapabilityEntryForModel returns capability metadata for a model/provider pair.
// It prefers chat, then responses, then text-completion entries; if none exist,
// it falls back to the lexicographically first available mode for deterministic behavior.
func (mc *ModelCatalog) GetModelCapabilityEntryForModel(model string, provider schemas.ModelProvider) *PricingEntry {
	mc.mu.RLock()
	defer mc.mu.RUnlock()

	if entry := mc.getCapabilityEntryForExactModelUnsafe(model, provider); entry != nil {
		return entry
	}

	baseModel := mc.getBaseModelNameUnsafe(model)
	if baseModel != model {
		if entry := mc.getCapabilityEntryForExactModelUnsafe(baseModel, provider); entry != nil {
			return entry
		}
	}

	if entry := mc.getCapabilityEntryForModelFamilyUnsafe(baseModel, provider); entry != nil {
		return entry
	}

	return nil
}

func (mc *ModelCatalog) getCapabilityEntryForExactModelUnsafe(model string, provider schemas.ModelProvider) *PricingEntry {
	preferredModes := []schemas.RequestType{
		schemas.ChatCompletionRequest,
		schemas.ResponsesRequest,
		schemas.TextCompletionRequest,
	}

	for _, mode := range preferredModes {
		key := makeKey(model, string(provider), normalizeRequestType(mode))
		pricing, ok := mc.pricingData[key]
		if ok {
			return convertTableModelPricingToPricingData(&pricing)
		}
	}

	prefix := model + "|" + string(provider) + "|"
	matchingKeys := make([]string, 0)
	for key := range mc.pricingData {
		if strings.HasPrefix(key, prefix) {
			matchingKeys = append(matchingKeys, key)
		}
	}
	return mc.selectCapabilityEntryFromKeysUnsafe(matchingKeys)
}

func (mc *ModelCatalog) getCapabilityEntryForModelFamilyUnsafe(baseModel string, provider schemas.ModelProvider) *PricingEntry {
	if baseModel == "" {
		return nil
	}

	matchingKeys := make([]string, 0)
	for key, pricing := range mc.pricingData {
		if normalizeProvider(pricing.Provider) != string(provider) {
			continue
		}
		if mc.getBaseModelNameUnsafe(pricing.Model) != baseModel {
			continue
		}
		matchingKeys = append(matchingKeys, key)
	}
	return mc.selectCapabilityEntryFromKeysUnsafe(matchingKeys)
}

func (mc *ModelCatalog) selectCapabilityEntryFromKeysUnsafe(matchingKeys []string) *PricingEntry {
	if len(matchingKeys) == 0 {
		return nil
	}

	preferredModes := []string{
		normalizeRequestType(schemas.ChatCompletionRequest),
		normalizeRequestType(schemas.ResponsesRequest),
		normalizeRequestType(schemas.TextCompletionRequest),
	}

	for _, mode := range preferredModes {
		modeMatches := make([]string, 0)
		for _, key := range matchingKeys {
			parts := strings.SplitN(key, "|", 3)
			if len(parts) != 3 || parts[2] != mode {
				continue
			}
			modeMatches = append(modeMatches, key)
		}
		if len(modeMatches) == 0 {
			continue
		}
		slices.Sort(modeMatches)
		pricing := mc.pricingData[modeMatches[0]]
		return convertTableModelPricingToPricingData(&pricing)
	}

	slices.Sort(matchingKeys)
	pricing := mc.pricingData[matchingKeys[0]]
	return convertTableModelPricingToPricingData(&pricing)
}

// GetModelsForProvider returns all available models for a given provider (thread-safe)
func (mc *ModelCatalog) GetModelsForProvider(provider schemas.ModelProvider) []string {
	mc.mu.RLock()
	defer mc.mu.RUnlock()

	models, exists := mc.modelPool[provider]
	if !exists {
		return []string{}
	}

	// Return a copy to prevent external modification
	result := make([]string, len(models))
	copy(result, models)
	return result
}

// GetUnfilteredModelsForProvider returns all available models for a given provider (thread-safe)
func (mc *ModelCatalog) GetUnfilteredModelsForProvider(provider schemas.ModelProvider) []string {
	mc.mu.RLock()
	defer mc.mu.RUnlock()

	models, exists := mc.unfilteredModelPool[provider]
	if !exists {
		return []string{}
	}

	// Return a copy to prevent external modification
	result := make([]string, len(models))
	copy(result, models)
	return result
}

// GetDistinctBaseModelNames returns all unique base model names from the catalog (thread-safe).
// This is used for governance model selection when no specific provider is chosen.
func (mc *ModelCatalog) GetDistinctBaseModelNames() []string {
	mc.mu.RLock()
	defer mc.mu.RUnlock()

	seen := make(map[string]bool)
	for _, baseName := range mc.baseModelIndex {
		seen[baseName] = true
	}

	result := make([]string, 0, len(seen))
	for name := range seen {
		result = append(result, name)
	}
	return result
}

// GetProvidersForModel returns all providers for a given model (thread-safe)
func (mc *ModelCatalog) GetProvidersForModel(model string) []schemas.ModelProvider {
	mc.mu.RLock()
	defer mc.mu.RUnlock()

	providers := make([]schemas.ModelProvider, 0)
	for provider, models := range mc.modelPool {
		isModelMatch := false
		for _, m := range models {
			if m == model || mc.getBaseModelNameUnsafe(m) == mc.getBaseModelNameUnsafe(model) {
				isModelMatch = true
				break
			}
		}
		if isModelMatch {
			providers = append(providers, provider)
		}
	}

	// Handler special provider cases
	// 1. Handler openrouter models
	if !slices.Contains(providers, schemas.OpenRouter) {
		for _, provider := range providers {
			if openRouterModels, ok := mc.modelPool[schemas.OpenRouter]; ok {
				if slices.Contains(openRouterModels, string(provider)+"/"+model) {
					providers = append(providers, schemas.OpenRouter)
				}
			}
		}
	}

	// 2. Handle vertex models
	if !slices.Contains(providers, schemas.Vertex) {
		for _, provider := range providers {
			if vertexModels, ok := mc.modelPool[schemas.Vertex]; ok {
				if slices.Contains(vertexModels, string(provider)+"/"+model) {
					providers = append(providers, schemas.Vertex)
				}
			}
		}
	}

	// 3. Handle openai models for groq
	if !slices.Contains(providers, schemas.Groq) && strings.Contains(model, "gpt-") {
		if groqModels, ok := mc.modelPool[schemas.Groq]; ok {
			if slices.Contains(groqModels, "openai/"+model) {
				providers = append(providers, schemas.Groq)
			}
		}
	}

	// 4. Handle anthropic models for bedrock
	if !slices.Contains(providers, schemas.Bedrock) && strings.Contains(model, "claude") {
		if bedrockModels, ok := mc.modelPool[schemas.Bedrock]; ok {
			for _, bedrockModel := range bedrockModels {
				if strings.Contains(bedrockModel, model) {
					providers = append(providers, schemas.Bedrock)
					break
				}
			}
		}
	}

	return providers
}

// IsModelAllowedForProvider checks if a model is allowed for a specific provider
// based on the allowed models list and catalog data. It handles all cross-provider
// logic including provider-prefixed models and special routing rules.
//
// Parameters:
//   - provider: The provider to check against
//   - model: The model name (without provider prefix, e.g., "gpt-4o" or "claude-3-5-sonnet")
//   - allowedModels: List of allowed model names (can be empty, can include provider prefixes)
//
// Behavior:
//   - If allowedModels is ["*"]: Uses model catalog to check if provider supports the model
//     (delegates to GetProvidersForModel which handles all cross-provider logic)
//   - If allowedModels is empty ([]): Deny-by-default — returns false for any provider/model pair
//   - If allowedModels is not empty: Checks if model matches any entry in the list
//     Provider-specific validation:
//   - Direct matches: "gpt-4o" in allowedModels for any provider
//   - Prefixed matches: Only if the prefixed model exists in provider's catalog
//     (e.g., "openai/gpt-4o" in allowedModels only matches if openrouter's catalog
//     contains "openai/gpt-4o" AND the model part matches the request)
//
// Returns:
//   - bool: true if the model is allowed for the provider, false otherwise
//
// Examples:
//
//	// Wildcard allowedModels - uses catalog to check provider support
//	mc.IsModelAllowedForProvider("openrouter", "claude-3-5-sonnet", []string{"*"})
//	// Returns: true (catalog knows openrouter has "anthropic/claude-3-5-sonnet")
//
//	// Empty allowedModels - deny all (deny-by-default)
//	mc.IsModelAllowedForProvider("openrouter", "claude-3-5-sonnet", []string{})
//	// Returns: false (no models are permitted)
//
//	// Explicit allowedModels with prefix - validates against catalog
//	mc.IsModelAllowedForProvider("openrouter", "gpt-4o", []string{"openai/gpt-4o"})
//	// Returns: true (openrouter's catalog contains "openai/gpt-4o" AND model part is "gpt-4o")
//
//	// Explicit allowedModels with prefix - wrong model
//	mc.IsModelAllowedForProvider("openrouter", "claude-3-5-sonnet", []string{"openai/gpt-4o"})
//	// Returns: false (model part "gpt-4o" doesn't match request "claude-3-5-sonnet")
//
//	// Explicit allowedModels without prefix
//	mc.IsModelAllowedForProvider("openai", "gpt-4o", []string{"gpt-4o"})
//	// Returns: true (direct match)
func (mc *ModelCatalog) IsModelAllowedForProvider(provider schemas.ModelProvider, model string, allowedModels schemas.WhiteList) bool {
	// Case 1: ["*"] = allow all models; use catalog to determine support
	// Empty allowedModels = deny all (fail-safe deny-by-default)
	if allowedModels.IsUnrestricted() {
		supportedProviders := mc.GetProvidersForModel(model)
		return slices.Contains(supportedProviders, provider)
	}
	if allowedModels.IsEmpty() {
		return false
	}

	// Case 2: Explicit allowedModels = check if model matches any entry
	// Get provider's catalog models for validation of prefixed entries
	providerCatalogModels := mc.GetModelsForProvider(provider)

	for _, allowedModel := range allowedModels {
		// Direct match: "gpt-4o" == "gpt-4o"
		if allowedModel == model {
			return true
		}

		// Provider-prefixed match: verify it exists in provider's catalog first
		// This ensures we only allow provider-specific model combinations that are actually supported
		if strings.Contains(allowedModel, "/") {
			// Check if this exact prefixed model exists in the provider's catalog
			// e.g., for openrouter, check if "openai/gpt-4o" is in its catalog
			if slices.Contains(providerCatalogModels, allowedModel) {
				// Extract the model part and compare with request
				_, modelPart := schemas.ParseModelString(allowedModel, "")
				if modelPart == model {
					return true
				}
			}
		}
	}

	return false
}

// GetBaseModelName returns the canonical base model name for a given model string.
// It uses the pre-computed base_model from the pricing catalog when available,
// falling back to algorithmic date/version stripping for models not in the catalog.
//
// Examples:
//
//	mc.GetBaseModelName("gpt-4o")                    // Returns: "gpt-4o"
//	mc.GetBaseModelName("openai/gpt-4o")             // Returns: "gpt-4o"
//	mc.GetBaseModelName("gpt-4o-2024-08-06")         // Returns: "gpt-4o" (algorithmic fallback)
func (mc *ModelCatalog) GetBaseModelName(model string) string {
	mc.mu.RLock()
	defer mc.mu.RUnlock()
	return mc.getBaseModelNameUnsafe(model)
}

// getBaseModelNameUnsafe returns the canonical base model name for a given model string without locking.
// This is used to avoid locking overhead when getting the base model name for many models.
// Make sure the caller function is holding the read lock before calling this function.
// It is not safe to use this function when the model pool is being updated.
func (mc *ModelCatalog) getBaseModelNameUnsafe(model string) string {
	// Step 1: Direct lookup in base model index
	if base, ok := mc.baseModelIndex[model]; ok {
		return base
	}

	// Step 2: Strip provider prefix and try again
	_, baseName := schemas.ParseModelString(model, "")
	if baseName != model {
		if base, ok := mc.baseModelIndex[baseName]; ok {
			return base
		}
	}

	// Step 3: Fallback to algorithmic date/version stripping
	// (for models not in the catalog, e.g., user-configured custom models)
	return schemas.BaseModelName(baseName)
}

// IsSameModel checks if two model strings refer to the same underlying model.
// It compares the canonical base model names derived from the pricing catalog
// (or algorithmic fallback for models not in the catalog).
//
// Examples:
//
//	mc.IsSameModel("gpt-4o", "gpt-4o")                            // true (direct match)
//	mc.IsSameModel("openai/gpt-4o", "gpt-4o")                     // true (same base model)
//	mc.IsSameModel("gpt-4o", "claude-3-5-sonnet")                  // false (different models)
//	mc.IsSameModel("openai/gpt-4o", "anthropic/claude-3-5-sonnet") // false
func (mc *ModelCatalog) IsSameModel(model1, model2 string) bool {
	if model1 == model2 {
		return true
	}
	return mc.GetBaseModelName(model1) == mc.GetBaseModelName(model2)
}

// DeleteModelDataForProvider deletes all model data from the pool for a given provider
func (mc *ModelCatalog) DeleteModelDataForProvider(provider schemas.ModelProvider) {
	mc.mu.Lock()
	defer mc.mu.Unlock()

	delete(mc.modelPool, provider)
	delete(mc.unfilteredModelPool, provider)
}

// UpsertModelDataForProvider upserts model data for a given provider
func (mc *ModelCatalog) UpsertModelDataForProvider(provider schemas.ModelProvider, modelData *schemas.BifrostListModelsResponse, allowedModels []schemas.Model) {
	if modelData == nil {
		return
	}
	mc.mu.Lock()
	defer mc.mu.Unlock()

	// Populating models from pricing data for the given provider
	// Provider models map
	providerModels := []string{}
	// Iterate through all pricing data to collect models per provider
	for _, pricing := range mc.pricingData {
		// Normalize provider before adding to model pool
		normalizedProvider := schemas.ModelProvider(normalizeProvider(pricing.Provider))
		// We will only add models for the given provider
		if normalizedProvider != provider {
			continue
		}
		// Add model to the provider's model set (using map for deduplication)
		if slices.Contains(providerModels, pricing.Model) {
			continue
		}
		providerModels = append(providerModels, pricing.Model)
		// Build base model index from pre-computed base_model field
		if pricing.BaseModel != "" {
			mc.baseModelIndex[pricing.Model] = pricing.BaseModel
		}
	}
	// If modelData is empty, then we allow all models
	if len(modelData.Data) == 0 && len(allowedModels) == 0 {
		mc.modelPool[provider] = providerModels
		return
	}
	// Here we make sure that we still keep the backup for model catalog intact
	// So we start with a existing model pool and add the new models from incoming data
	finalModelList := make([]string, 0)
	seenModels := make(map[string]bool)
	// Case where list models failed but we have allowed models from keys
	if len(modelData.Data) == 0 && len(allowedModels) > 0 {
		for _, allowedModel := range allowedModels {
			parsedProvider, parsedModel := schemas.ParseModelString(allowedModel.ID, "")
			if parsedProvider != provider {
				continue
			}
			if !seenModels[parsedModel] {
				seenModels[parsedModel] = true
				finalModelList = append(finalModelList, parsedModel)
			}
		}
	}
	for _, model := range modelData.Data {
		parsedProvider, parsedModel := schemas.ParseModelString(model.ID, "")
		if parsedProvider != provider {
			continue
		}
		if !seenModels[parsedModel] {
			seenModels[parsedModel] = true
			finalModelList = append(finalModelList, parsedModel)
		}
	}

	if len(allowedModels) == 0 {
		for _, model := range providerModels {
			if !seenModels[model] {
				seenModels[model] = true
				finalModelList = append(finalModelList, model)
			}
		}
	}
	mc.modelPool[provider] = finalModelList
}

// UpsertUnfilteredModelDataForProvider upserts unfiltered model data for a given provider
func (mc *ModelCatalog) UpsertUnfilteredModelDataForProvider(provider schemas.ModelProvider, modelData *schemas.BifrostListModelsResponse) {
	if modelData == nil {
		return
	}
	mc.mu.Lock()
	defer mc.mu.Unlock()

	// Populating models from pricing data for the given provider
	providerModels := []string{}
	seenModels := make(map[string]bool)
	for _, pricing := range mc.pricingData {
		normalizedProvider := schemas.ModelProvider(normalizeProvider(pricing.Provider))
		if normalizedProvider != provider {
			continue
		}
		if !seenModels[pricing.Model] {
			seenModels[pricing.Model] = true
			providerModels = append(providerModels, pricing.Model)
		}
	}
	for _, model := range modelData.Data {
		parsedProvider, parsedModel := schemas.ParseModelString(model.ID, "")
		if parsedProvider != provider {
			continue
		}
		if !seenModels[parsedModel] {
			seenModels[parsedModel] = true
			providerModels = append(providerModels, parsedModel)
		}
	}
	mc.unfilteredModelPool[provider] = providerModels
}

// RefineModelForProvider refines the model for a given provider by performing a lookup
// in mc.modelPool and using schemas.ParseModelString to extract provider and model parts.
// e.g. "gpt-oss-120b" for groq provider -> "openai/gpt-oss-120b"
//
// Behavior:
// - When the provider's catalog (mc.modelPool) yields multiple matching models, returns an error
// - When exactly one match is found, returns the fully-qualified model (provider/model format)
// - When the provider is not handled or no refinement is needed, returns the original model unchanged
func (mc *ModelCatalog) RefineModelForProvider(provider schemas.ModelProvider, model string) (string, error) {
	switch provider {
	case schemas.Groq:
		if strings.Contains(model, "gpt-") {
			return "openai/" + model, nil
		}
		return mc.refineNestedProviderModel(provider, model)
	case schemas.Replicate:
		return mc.refineNestedProviderModel(provider, model)
	}
	return model, nil
}

// refineNestedProviderModel resolves provider-native model slugs such as
// "openai/gpt-5-nano" from a base model request like "gpt-5-nano".
// It only considers catalog entries whose leading segment is a known Bifrost provider,
// so Replicate owner/model identifiers like "meta/llama-3-8b" are left untouched.
func (mc *ModelCatalog) refineNestedProviderModel(provider schemas.ModelProvider, model string) (string, error) {
	mc.mu.RLock()
	models, ok := mc.modelPool[provider]
	mc.mu.RUnlock()
	if !ok {
		return model, nil
	}

	candidateModels := make([]string, 0)
	seenCandidates := make(map[string]struct{})
	for _, poolModel := range models {
		providerPart, modelPart := schemas.ParseModelString(poolModel, "")
		if providerPart == "" || model != modelPart {
			continue
		}

		candidate := string(providerPart) + "/" + modelPart
		if _, seen := seenCandidates[candidate]; seen {
			continue
		}
		seenCandidates[candidate] = struct{}{}
		candidateModels = append(candidateModels, candidate)
	}

	switch len(candidateModels) {
	case 0:
		return model, nil
	case 1:
		return candidateModels[0], nil
	default:
		return "", fmt.Errorf("multiple compatible models found for model %s: %v", model, candidateModels)
	}
}

// SetPricingOverrides replaces the full in-memory pricing override set.
func (mc *ModelCatalog) SetPricingOverrides(rows []configstoreTables.TablePricingOverride) error {
	seen := make(map[string]int, len(rows))
	overrides := make([]PricingOverride, 0, len(rows))
	for i := range rows {
		o, err := convertTablePricingOverrideToPricingOverride(&rows[i])
		if err != nil {
			return err
		}
		if idx, exists := seen[o.ID]; exists {
			overrides[idx] = o // last entry wins for duplicate IDs
		} else {
			seen[o.ID] = len(overrides)
			overrides = append(overrides, o)
		}
	}
	mc.overridesMu.Lock()
	mc.rawOverrides = overrides
	mc.customPricing = buildCustomPricingData(overrides)
	mc.overridesMu.Unlock()
	return nil
}

// UpsertPricingOverrides inserts or replaces one or more pricing overrides in a single
// operation, rebuilding the lookup map only once at the end.
func (mc *ModelCatalog) UpsertPricingOverrides(rows ...*configstoreTables.TablePricingOverride) error {
	// Deduplicate the input batch by ID (last entry wins) and build the
	// incoming set for O(1) lookup when filtering existing rawOverrides.
	seenIncoming := make(map[string]int, len(rows))
	overrides := make([]PricingOverride, 0, len(rows))
	for _, row := range rows {
		o, err := convertTablePricingOverrideToPricingOverride(row)
		if err != nil {
			return err
		}
		if idx, exists := seenIncoming[o.ID]; exists {
			overrides[idx] = o // last entry wins for duplicate IDs
		} else {
			seenIncoming[o.ID] = len(overrides)
			overrides = append(overrides, o)
		}
	}

	mc.overridesMu.Lock()
	defer mc.overridesMu.Unlock()

	updated := make([]PricingOverride, 0, len(mc.rawOverrides)+len(overrides))
	for _, o := range mc.rawOverrides {
		if _, replacing := seenIncoming[o.ID]; !replacing {
			updated = append(updated, o)
		}
	}
	updated = append(updated, overrides...)
	mc.rawOverrides = updated
	mc.customPricing = buildCustomPricingData(updated)
	return nil
}

// DeletePricingOverride removes a pricing override by ID.
func (mc *ModelCatalog) DeletePricingOverride(id string) {
	mc.overridesMu.Lock()
	defer mc.overridesMu.Unlock()

	updated := make([]PricingOverride, 0, len(mc.rawOverrides))
	for _, o := range mc.rawOverrides {
		if o.ID != id {
			updated = append(updated, o)
		}
	}
	mc.rawOverrides = updated
	mc.customPricing = buildCustomPricingData(updated)
}

// IsTextCompletionSupported checks if a model supports text completion for the given provider.
// Returns true if the model has pricing data for text completion ("text_completion"),
// false otherwise. This is used by the litellmcompat plugin to determine whether to
// convert text completion requests to chat completion requests.
func (mc *ModelCatalog) IsTextCompletionSupported(model string, provider schemas.ModelProvider) bool {
	mc.mu.RLock()
	defer mc.mu.RUnlock()
	// Check for text completion mode in pricing data
	key := makeKey(model, normalizeProvider(string(provider)), normalizeRequestType(schemas.TextCompletionRequest))
	_, ok := mc.pricingData[key]
	return ok
}

// IsChatCompletionSupported checks if a model supports chat completion.
// It checks the supportedOutputs index (derived from supported_endpoints in the datasheet).
func (mc *ModelCatalog) IsChatCompletionSupported(model string, provider schemas.ModelProvider) bool {
	mc.mu.RLock()
	outputs, ok := mc.supportedOutputs[model]
	mc.mu.RUnlock()
	return ok && slices.Contains(outputs, "chat_completion")
}

// IsResponsesSupported checks if a model supports the responses endpoint.
// It checks the supportedOutputs index (derived from supported_endpoints in the datasheet).
func (mc *ModelCatalog) IsResponsesSupported(model string, provider schemas.ModelProvider) bool {
	mc.mu.RLock()
	outputs, ok := mc.supportedOutputs[model]
	mc.mu.RUnlock()
	return ok && slices.Contains(outputs, "responses")
}

// buildSupportedOutputsIndex parses supported_endpoints from model parameters data
// and rebuilds the supportedOutputs index with normalized output type names.
func (mc *ModelCatalog) buildSupportedOutputsIndex(paramsData map[string]json.RawMessage) {
	newIndex := make(map[string][]string, len(paramsData))

	for model, data := range paramsData {
		var params struct {
			SupportedEndpoints []string `json:"supported_endpoints"`
		}
		if err := json.Unmarshal(data, &params); err != nil || len(params.SupportedEndpoints) == 0 {
			continue
		}
		outputs := make([]string, 0, len(params.SupportedEndpoints))
		for _, endpoint := range params.SupportedEndpoints {
			if normalized := normalizeEndpointToOutputType(endpoint); normalized != "" {
				if !slices.Contains(outputs, normalized) {
					outputs = append(outputs, normalized)
				}
			}
		}
		if len(outputs) > 0 {
			newIndex[model] = outputs
		}
	}

	mc.mu.Lock()
	mc.supportedOutputs = newIndex
	mc.mu.Unlock()
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
		modelPool:           make(map[schemas.ModelProvider][]string),
		unfilteredModelPool: make(map[schemas.ModelProvider][]string),
		baseModelIndex:      baseModelIndex,
		pricingData:         make(map[string]configstoreTables.TableModelPricing),
		supportedOutputs:    make(map[string][]string),
		done:                make(chan struct{}),
	}
}