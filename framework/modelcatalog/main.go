// Package modelcatalog provides a pricing manager for the framework.
package modelcatalog

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
)

// Default sync interval and config key
const (
	TokenTierAbove128K = 128000
	TokenTierAbove200K = 200000
)

type ModelCatalog struct {
	configStore            configstore.ConfigStore
	distributedLockManager *configstore.DistributedLockManager

	logger schemas.Logger

	// Pricing configuration fields (protected by pricingMu)
	pricingURL          string
	pricingSyncInterval time.Duration
	pricingMu           sync.RWMutex

	shouldSyncPricingFunc ShouldSyncPricingFunc

	// In-memory cache for fast access - direct map for O(1) lookups
	pricingData map[string]configstoreTables.TableModelPricing
	mu          sync.RWMutex

	modelPool map[schemas.ModelProvider][]string

	// Background sync worker
	syncTicker *time.Ticker
	done       chan struct{}
	wg         sync.WaitGroup
	syncCtx    context.Context
	syncCancel context.CancelFunc
}

// PricingEntry represents a single model's pricing information
type PricingEntry struct {
	// Basic pricing
	InputCostPerToken  float64 `json:"input_cost_per_token"`
	OutputCostPerToken float64 `json:"output_cost_per_token"`
	Provider           string  `json:"provider"`
	Mode               string  `json:"mode"`
	// Additional pricing for media
	InputCostPerVideoPerSecond *float64 `json:"input_cost_per_video_per_second,omitempty"`
	InputCostPerAudioPerSecond *float64 `json:"input_cost_per_audio_per_second,omitempty"`
	// Character-based pricing
	InputCostPerCharacter  *float64 `json:"input_cost_per_character,omitempty"`
	OutputCostPerCharacter *float64 `json:"output_cost_per_character,omitempty"`
	// Pricing above 128k tokens
	InputCostPerTokenAbove128kTokens          *float64 `json:"input_cost_per_token_above_128k_tokens,omitempty"`
	InputCostPerCharacterAbove128kTokens      *float64 `json:"input_cost_per_character_above_128k_tokens,omitempty"`
	InputCostPerImageAbove128kTokens          *float64 `json:"input_cost_per_image_above_128k_tokens,omitempty"`
	InputCostPerVideoPerSecondAbove128kTokens *float64 `json:"input_cost_per_video_per_second_above_128k_tokens,omitempty"`
	InputCostPerAudioPerSecondAbove128kTokens *float64 `json:"input_cost_per_audio_per_second_above_128k_tokens,omitempty"`
	OutputCostPerTokenAbove128kTokens         *float64 `json:"output_cost_per_token_above_128k_tokens,omitempty"`
	OutputCostPerCharacterAbove128kTokens     *float64 `json:"output_cost_per_character_above_128k_tokens,omitempty"`
	//Pricing above 200k tokens
	InputCostPerTokenAbove200kTokens           *float64 `json:"input_cost_per_token_above_200k_tokens,omitempty"`
	OutputCostPerTokenAbove200kTokens          *float64 `json:"output_cost_per_token_above_200k_tokens,omitempty"`
	CacheCreationInputTokenCostAbove200kTokens *float64 `json:"cache_creation_input_token_cost_above_200k_tokens,omitempty"`
	CacheReadInputTokenCostAbove200kTokens     *float64 `json:"cache_read_input_token_cost_above_200k_tokens,omitempty"`
	// Cache and batch pricing
	CacheReadInputTokenCost   *float64 `json:"cache_read_input_token_cost,omitempty"`
	InputCostPerTokenBatches  *float64 `json:"input_cost_per_token_batches,omitempty"`
	OutputCostPerTokenBatches *float64 `json:"output_cost_per_token_batches,omitempty"`
	// Image generation pricing
	InputCostPerImageToken       *float64 `json:"input_cost_per_image_token,omitempty"`
	OutputCostPerImageToken      *float64 `json:"output_cost_per_image_token,omitempty"`
	InputCostPerImage            *float64 `json:"input_cost_per_image,omitempty"`
	OutputCostPerImage           *float64 `json:"output_cost_per_image,omitempty"`
	CacheReadInputImageTokenCost *float64 `json:"cache_read_input_image_token_cost,omitempty"`	
}

// ShouldSyncPricingFunc is a function that determines if pricing data should be synced
// It returns a boolean indicating if syncing is needed
// It is completely optional and can be nil if not needed
// syncPricing function will be called if this function returns true
type ShouldSyncPricingFunc func(ctx context.Context) bool

// Init initializes the pricing manager
func Init(ctx context.Context, config *Config, configStore configstore.ConfigStore, shouldSyncPricingFunc ShouldSyncPricingFunc, logger schemas.Logger) (*ModelCatalog, error) {
	// Initialize pricing URL and sync interval
	pricingURL := DefaultPricingURL
	if config.PricingURL != nil {
		pricingURL = *config.PricingURL
	}
	pricingSyncInterval := DefaultPricingSyncInterval
	if config.PricingSyncInterval != nil {
		pricingSyncInterval = *config.PricingSyncInterval
	}

	mc := &ModelCatalog{
		pricingURL:             pricingURL,
		pricingSyncInterval:    pricingSyncInterval,
		configStore:            configStore,
		logger:                 logger,
		pricingData:            make(map[string]configstoreTables.TableModelPricing),
		modelPool:              make(map[schemas.ModelProvider][]string),
		done:                   make(chan struct{}),
		shouldSyncPricingFunc:  shouldSyncPricingFunc,
		distributedLockManager: configstore.NewDistributedLockManager(configStore, logger, configstore.WithDefaultTTL(30*time.Second)),
	}

	logger.Info("initializing pricing manager...")
	if configStore != nil {
		if mc.distributedLockManager == nil {
			if err := mc.loadPricingFromDatabase(ctx); err != nil {
				return nil, fmt.Errorf("failed to load initial pricing data: %w", err)
			}
			if err := mc.syncPricing(ctx); err != nil {
				return nil, fmt.Errorf("failed to sync pricing data: %w", err)
			}
		} else {
			lock, err := mc.distributedLockManager.NewLock("model_catalog_pricing_sync")
			if err != nil {
				return nil, fmt.Errorf("failed to create model catalog pricing sync lock: %w", err)
			}
			if err := lock.Lock(ctx); err != nil {
				return nil, fmt.Errorf("failed to acquire model catalog pricing sync lock: %w", err)
			}
			defer lock.Unlock(ctx)
			// Load initial pricing data
			if err := mc.loadPricingFromDatabase(ctx); err != nil {
				return nil, fmt.Errorf("failed to load initial pricing data: %w", err)
			}
			if err := mc.syncPricing(ctx); err != nil {
				return nil, fmt.Errorf("failed to sync pricing data: %w", err)
			}
		}
	} else {
		// Load pricing data from config memory
		if err := mc.loadPricingIntoMemory(ctx); err != nil {
			return nil, fmt.Errorf("failed to load pricing data from config memory: %w", err)
		}
	}

	// Populate model pool with normalized providers from pricing data
	mc.populateModelPoolFromPricingData()

	// Start background sync worker
	mc.syncCtx, mc.syncCancel = context.WithCancel(ctx)
	mc.startSyncWorker(mc.syncCtx)
	mc.configStore = configStore
	mc.logger = logger

	return mc, nil
}

// ReloadPricing reloads the pricing manager from config
func (mc *ModelCatalog) ReloadPricing(ctx context.Context, config *Config) error {
	// Acquire pricing mutex to update configuration atomically
	mc.pricingMu.Lock()

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
	mc.pricingSyncInterval = DefaultPricingSyncInterval
	if config.PricingSyncInterval != nil {
		mc.pricingSyncInterval = *config.PricingSyncInterval
	}

	// Create new sync worker with updated configuration
	mc.syncCtx, mc.syncCancel = context.WithCancel(ctx)
	mc.startSyncWorker(mc.syncCtx)

	mc.pricingMu.Unlock()

	// Perform immediate sync with new configuration
	if err := mc.syncPricing(ctx); err != nil {
		return fmt.Errorf("failed to sync pricing data: %w", err)
	}

	return nil
}

func (mc *ModelCatalog) ForceReloadPricing(ctx context.Context) error {
	mc.pricingMu.Lock()
	// Reset the ticker so the next scheduled sync waits a full interval from now
	if mc.syncTicker != nil {
		mc.syncTicker.Reset(mc.pricingSyncInterval)
	}
	mc.pricingMu.Unlock()

	timeout := DefaultPricingTimeout
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	if err := mc.syncPricing(ctx); err != nil {
		return fmt.Errorf("failed to sync pricing data: %w", err)
	}

	return nil
}

// getPricingURL returns a copy of the pricing URL under mutex protection
func (mc *ModelCatalog) getPricingURL() string {
	mc.pricingMu.RLock()
	defer mc.pricingMu.RUnlock()
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
		schemas.SpeechRequest,
		schemas.TranscriptionRequest,
	} {
		key := makeKey(model, string(provider), normalizeRequestType(mode))
		pricing, ok := mc.pricingData[key]
		if ok {
			return convertTableModelPricingToPricingData(&pricing)
		}
	}
	return nil
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

// GetProvidersForModel returns all providers for a given model (thread-safe)
func (mc *ModelCatalog) GetProvidersForModel(model string) []schemas.ModelProvider {
	mc.mu.RLock()
	defer mc.mu.RUnlock()

	providers := make([]schemas.ModelProvider, 0)
	for provider, models := range mc.modelPool {
		if slices.Contains(models, model) {
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
//   - If allowedModels is empty: Uses model catalog to check if provider supports the model
//     (delegates to GetProvidersForModel which handles all cross-provider logic)
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
//	// Empty allowedModels - uses catalog
//	mc.IsModelAllowedForProvider("openrouter", "claude-3-5-sonnet", []string{})
//	// Returns: true (catalog knows openrouter has "anthropic/claude-3-5-sonnet")
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
func (mc *ModelCatalog) IsModelAllowedForProvider(provider schemas.ModelProvider, model string, allowedModels []string) bool {
	// Case 1: Empty allowedModels = use catalog to determine support
	// This leverages GetProvidersForModel which already handles all cross-provider logic
	if len(allowedModels) == 0 {
		supportedProviders := mc.GetProvidersForModel(model)
		return slices.Contains(supportedProviders, provider)
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

// AddModelDataToPool adds model data to the model pool.
func (mc *ModelCatalog) AddModelDataToPool(modelData *schemas.BifrostListModelsResponse) {
	if modelData == nil {
		return
	}
	mc.mu.Lock()
	defer mc.mu.Unlock()

	for _, model := range modelData.Data {
		provider, model := schemas.ParseModelString(model.ID, "")
		if provider == "" {
			continue
		}
		provider = schemas.ModelProvider(provider)
		mc.modelPool[provider] = append(mc.modelPool[provider], model)
	}
}

// DeleteModelDataForProvider deletes all model data from the pool for a given provider
func (mc *ModelCatalog) DeleteModelDataForProvider(provider schemas.ModelProvider) {
	mc.mu.Lock()
	defer mc.mu.Unlock()

	delete(mc.modelPool, provider)
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
	// These providers have {provider}/{model} format for models
	case schemas.Groq:
		if strings.Contains(model, "gpt-") {
			return "openai/" + model, nil
		}
		// Check if the model without provider prefix is present in the provider's catalog
		// Guard concurrent access to mc.modelPool with read lock
		mc.mu.RLock()
		models, ok := mc.modelPool[provider]
		mc.mu.RUnlock()

		if ok {
			var candidateModels []string
			for _, poolModel := range models {
				providerPart, modelPart := schemas.ParseModelString(poolModel, "")
				if model == modelPart {
					candidateModels = append(candidateModels, string(providerPart)+"/"+modelPart)
				}
			}
			// Handle candidateModels based on count
			if len(candidateModels) == 1 {
				return candidateModels[0], nil
			} else if len(candidateModels) == 0 {
				// No matches found, return original model to allow fallback
				return model, nil
			} else {
				// Multiple matches found, return error
				return "", fmt.Errorf("multiple compatible models found for model %s: %v", model, candidateModels)
			}
		}
	}
	return model, nil
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

// populateModelPool populates the model pool with all available models per provider (thread-safe)
func (mc *ModelCatalog) populateModelPoolFromPricingData() {
	// Acquire write lock for the entire rebuild operation
	mc.mu.Lock()
	defer mc.mu.Unlock()

	// Clear existing model pool
	mc.modelPool = make(map[schemas.ModelProvider][]string)

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
	}

	// Convert sets to slices and assign to modelPool
	for provider, modelSet := range providerModels {
		models := make([]string, 0, len(modelSet))
		for model := range modelSet {
			models = append(models, model)
		}
		mc.modelPool[provider] = models
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

	mc.pricingMu.Lock()
	if mc.syncTicker != nil {
		mc.syncTicker.Stop()
	}
	mc.pricingMu.Unlock()

	close(mc.done)
	mc.wg.Wait()

	return nil
}
