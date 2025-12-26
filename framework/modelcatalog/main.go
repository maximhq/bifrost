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
	configStore configstore.ConfigStore
	logger      schemas.Logger

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
	InputCostPerImage          *float64 `json:"input_cost_per_image,omitempty"`
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
		pricingURL:            pricingURL,
		pricingSyncInterval:   pricingSyncInterval,
		configStore:           configStore,
		logger:                logger,
		pricingData:           make(map[string]configstoreTables.TableModelPricing),
		modelPool:             make(map[schemas.ModelProvider][]string),
		done:                  make(chan struct{}),
		shouldSyncPricingFunc: shouldSyncPricingFunc,
	}

	logger.Info("initializing pricing manager...")
	if configStore != nil {
		// Load initial pricing data
		if err := mc.loadPricingFromDatabase(ctx); err != nil {
			return nil, fmt.Errorf("failed to load initial pricing data: %w", err)
		}

		// For the boot-up we sync pricing data from file to database
		if err := mc.syncPricing(ctx); err != nil {
			return nil, fmt.Errorf("failed to sync pricing data: %w", err)
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

// RefineModelForProvider refines the model for a given provider.
// e.g. "gpt-oss-120b" for groq provider -> "openai/gpt-oss-120b"
func (mc *ModelCatalog) RefineModelForProvider(provider schemas.ModelProvider, model string) string {
	switch provider {
	case schemas.Groq:
		if model == "gpt-oss-120b" {
			return "openai/" + model
		}
	}
	return model
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
