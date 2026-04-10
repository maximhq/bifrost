// Package modelcatalog provides a pricing manager for the framework.
package modelcatalog

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"encoding/json"

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

	// Pricing configuration fields (protected by pricingMu)
	pricingURL          string
	pricingSyncInterval time.Duration
	pricingMu           sync.RWMutex

	shouldSyncPricingFunc ShouldSyncPricingFunc

	// In-memory cache for fast access - direct map for O(1) lookups
	pricingData map[string]configstoreTables.TableModelPricing
	mu          sync.RWMutex

	// Provider-level pricing overrides are maintained separately to avoid contention
	// with pricing cache rebuilds.
	compiledOverrides map[schemas.ModelProvider][]compiledProviderPricingOverride
	overridesMu       sync.RWMutex

	modelPool           map[schemas.ModelProvider][]string
	unfilteredModelPool map[schemas.ModelProvider][]string // model pool without allowed models filtering
	baseModelIndex      map[string]string                  // model string → canonical base model name

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
	InputCostPerCharacter      *float64 `json:"input_cost_per_character,omitempty"`
	// Costs - 128k Tier
	InputCostPerTokenAbove128kTokens          *float64 `json:"input_cost_per_token_above_128k_tokens,omitempty"`
	InputCostPerImageAbove128kTokens          *float64 `json:"input_cost_per_image_above_128k_tokens,omitempty"`
	InputCostPerVideoPerSecondAbove128kTokens *float64 `json:"input_cost_per_video_per_second_above_128k_tokens,omitempty"`
	InputCostPerAudioPerSecondAbove128kTokens *float64 `json:"input_cost_per_audio_per_second_above_128k_tokens,omitempty"`
	OutputCostPerTokenAbove128kTokens         *float64 `json:"output_cost_per_token_above_128k_tokens,omitempty"`
	// Costs - 200k Tier
	InputCostPerTokenAbove200kTokens         *float64 `json:"input_cost_per_token_above_200k_tokens,omitempty"`
	InputCostPerTokenAbove200kTokensPriority *float64 `json:"input_cost_per_token_above_200k_tokens_priority,omitempty"`
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
		compiledOverrides:      make(map[schemas.ModelProvider][]compiledProviderPricingOverride),
		modelPool:              make(map[schemas.ModelProvider][]string),
		unfilteredModelPool:    make(map[schemas.ModelProvider][]string),
		baseModelIndex:         make(map[string]string),
		done:                   make(chan struct{}),
		shouldSyncPricingFunc:  shouldSyncPricingFunc,
		distributedLockManager: configstore.NewDistributedLockManager(configStore, logger, configstore.WithDefaultTTL(30*time.Second)),
	}

	logger.Info("initializing model catalog...")
	if configStore != nil {
		// Register a cache miss handler so that on first request for a model,
		// the cache lazily loads its parameters from the database.
		providerUtils.SetCacheMissHandler(func(model string) *providerUtils.ModelParams {
			missCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			params, err := configStore.GetModelParameters(missCtx, model)
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
		if mc.distributedLockManager == nil {
			if err := mc.loadPricingFromDatabase(ctx); err != nil {
				return nil, fmt.Errorf("failed to load initial pricing data: %w", err)
			}
			if err := mc.syncPricing(ctx); err != nil {
				return nil, fmt.Errorf("failed to sync pricing data: %w", err)
			}
			// Sync model parameters asynchronously - not needed for startup
			go func() {
				if err := mc.syncModelParameters(ctx); err != nil {
					mc.logger.Warn("failed to sync model parameters data: %v", err)
				}
			}()
		} else {
			lock, err := mc.distributedLockManager.NewLock("model_catalog_pricing_sync")
			if err != nil {
				return nil, fmt.Errorf("failed to create model catalog pricing sync lock: %w", err)
			}
			if err := lock.LockWithRetry(ctx, 10); err != nil {
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
			// Sync model parameters asynchronously - not needed for startup
			go func() {
				if err := mc.syncModelParameters(ctx); err != nil {
					mc.logger.Warn("failed to sync model parameters data: %v", err)
				}
			}()
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
	return mc, nil
}

// ReloadPricing reloads the model catalog from config
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

	// Also sync model parameters
	if err := mc.syncModelParameters(ctx); err != nil {
		mc.logger.Warn("failed to sync model parameters during reload: %v", err)
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

	// Rebuild model pool from updated pricing data
	mc.populateModelPoolFromPricingData()

	// Also sync model parameters
	if err := mc.syncModelParameters(ctx); err != nil {
		mc.logger.Warn("failed to sync model parameters during force reload: %v", err)
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
func (mc *ModelCatalog) UpsertModelDataForProvider(provider schemas.ModelProvider, modelData *schemas.BifrostListModelsResponse, allowedModels []schemas.Model, deniedModels []schemas.Model) {
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
	if len(modelData.Data) == 0 && len(allowedModels) == 0 && len(deniedModels) == 0 {
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
		deniedSet := make(map[string]struct{}, len(deniedModels))
		for _, d := range deniedModels {
			_, modelName := schemas.ParseModelString(d.ID, "")
			deniedSet[modelName] = struct{}{}
		}
		for _, model := range providerModels {
			if _, denied := deniedSet[model]; denied {
				continue
			}
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

	mc.pricingMu.Lock()
	if mc.syncTicker != nil {
		mc.syncTicker.Stop()
	}
	mc.pricingMu.Unlock()

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
		compiledOverrides:   make(map[schemas.ModelProvider][]compiledProviderPricingOverride),
		done:                make(chan struct{}),
	}
}
