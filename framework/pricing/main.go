package pricing

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
)

// Default sync interval and config key
const (
	DefaultPricingSyncInterval = 24 * time.Hour
	LastPricingSyncKey         = "LastModelPricingSync"
	PricingFileURL             = "https://getbifrost.ai/datasheet"
	TokenTierAbove128K         = 128000
)

type PricingManager struct {
	configStore configstore.ConfigStore
	logger      schemas.Logger

	// In-memory cache for fast access - direct map for O(1) lookups
	pricingData map[string]configstore.TableModelPricing
	mu          sync.RWMutex

	modelPool map[schemas.ModelProvider][]string

	// Background sync worker
	syncTicker *time.Ticker
	done       chan struct{}
	wg         sync.WaitGroup
	syncCtx    context.Context
	syncCancel context.CancelFunc
}

// PricingData represents the structure of the pricing.json file
type PricingData map[string]PricingEntry

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

	// Cache and batch pricing
	CacheReadInputTokenCost   *float64 `json:"cache_read_input_token_cost,omitempty"`
	InputCostPerTokenBatches  *float64 `json:"input_cost_per_token_batches,omitempty"`
	OutputCostPerTokenBatches *float64 `json:"output_cost_per_token_batches,omitempty"`
}

// Init initializes the pricing manager
func Init(ctx context.Context, configStore configstore.ConfigStore, logger schemas.Logger) (*PricingManager, error) {
	pm := &PricingManager{
		configStore: configStore,
		logger:      logger,
		pricingData: make(map[string]configstore.TableModelPricing),
		modelPool:   make(map[schemas.ModelProvider][]string),
		done:        make(chan struct{}),
	}

	logger.Info("initializing pricing manager...")

	if configStore != nil {
		// Load initial pricing data
		if err := pm.loadPricingFromDatabase(ctx); err != nil {
			return nil, fmt.Errorf("failed to load initial pricing data: %w", err)
		}

		// For the bootup we sync pricing data from file to database
		if err := pm.syncPricing(ctx); err != nil {
			return nil, fmt.Errorf("failed to sync pricing data: %w", err)
		}
	} else {
		// Load pricing data from config memory
		if err := pm.loadPricingIntoMemory(ctx); err != nil {
			return nil, fmt.Errorf("failed to load pricing data from config memory: %w", err)
		}
	}

	// Populate model pool with normalized providers
	pm.populateModelPool()

	// Start background sync worker
	pm.syncCtx, pm.syncCancel = context.WithCancel(ctx)
	pm.startSyncWorker(pm.syncCtx)
	pm.configStore = configStore
	pm.logger = logger

	return pm, nil
}

func (pm *PricingManager) CalculateCost(result *schemas.BifrostResponse) float64 {
	if result == nil {
		return 0.0
	}

	var usage *schemas.LLMUsage
	var audioSeconds *int
	var audioTokenDetails *schemas.AudioTokenDetails

	//TODO: Detect cache and batch operations
	isCacheRead := false
	isBatch := false

	// Check main usage field
	if result.Usage != nil {
		usage = result.Usage
	} else if result.Speech != nil && result.Speech.Usage != nil {
		// For speech synthesis, create LLMUsage from AudioLLMUsage
		usage = &schemas.LLMUsage{
			PromptTokens:     result.Speech.Usage.InputTokens,
			CompletionTokens: 0, // Speech doesn't have completion tokens
			TotalTokens:      result.Speech.Usage.TotalTokens,
		}

		// Extract audio token details if available
		if result.Speech.Usage.InputTokensDetails != nil {
			audioTokenDetails = result.Speech.Usage.InputTokensDetails
		}
	} else if result.Transcribe != nil && result.Transcribe.Usage != nil && result.Transcribe.Usage.TotalTokens != nil {
		// For transcription, create LLMUsage from TranscriptionUsage
		inputTokens := 0
		outputTokens := 0
		if result.Transcribe.Usage.InputTokens != nil {
			inputTokens = *result.Transcribe.Usage.InputTokens
		}
		if result.Transcribe.Usage.OutputTokens != nil {
			outputTokens = *result.Transcribe.Usage.OutputTokens
		}
		usage = &schemas.LLMUsage{
			PromptTokens:     inputTokens,
			CompletionTokens: outputTokens,
			TotalTokens:      int(*result.Transcribe.Usage.TotalTokens),
		}

		// Extract audio duration if available (for duration-based pricing)
		if result.Transcribe.Usage.Seconds != nil {
			audioSeconds = result.Transcribe.Usage.Seconds
		}

		// Extract audio token details if available
		if result.Transcribe.Usage.InputTokenDetails != nil {
			audioTokenDetails = result.Transcribe.Usage.InputTokenDetails
		}
	}

	cost := 0.0
	if usage != nil || audioSeconds != nil || audioTokenDetails != nil {
		cost = pm.CalculateCostFromUsage(string(result.ExtraFields.Provider), result.ExtraFields.ModelRequested, usage, result.ExtraFields.RequestType, isCacheRead, isBatch, audioSeconds, audioTokenDetails)
	}

	return cost
}

func (pm *PricingManager) CalculateCostWithCacheDebug(result *schemas.BifrostResponse) float64 {
	if result == nil {
		return 0.0
	}
	cacheDebug := result.ExtraFields.CacheDebug
	if cacheDebug != nil {
		if cacheDebug.CacheHit {
			if cacheDebug.HitType != nil && *cacheDebug.HitType == "direct" {
				return 0
			} else if cacheDebug.ProviderUsed != nil && cacheDebug.ModelUsed != nil && cacheDebug.InputTokens != nil {
				return pm.CalculateCostFromUsage(*cacheDebug.ProviderUsed, *cacheDebug.ModelUsed, &schemas.LLMUsage{
					PromptTokens:     *cacheDebug.InputTokens,
					CompletionTokens: 0,
					TotalTokens:      *cacheDebug.InputTokens,
				}, schemas.EmbeddingRequest, false, false, nil, nil)
			}

			// Don't over-bill cache hits if fields are missing.
			return 0
		} else {
			baseCost := pm.CalculateCost(result)
			var semanticCacheCost float64
			if cacheDebug.ProviderUsed != nil && cacheDebug.ModelUsed != nil && cacheDebug.InputTokens != nil {
				semanticCacheCost = pm.CalculateCostFromUsage(*cacheDebug.ProviderUsed, *cacheDebug.ModelUsed, &schemas.LLMUsage{
					PromptTokens:     *cacheDebug.InputTokens,
					CompletionTokens: 0,
					TotalTokens:      *cacheDebug.InputTokens,
				}, schemas.EmbeddingRequest, false, false, nil, nil)
			}

			return baseCost + semanticCacheCost
		}
	}

	return pm.CalculateCost(result)
}

func (pm *PricingManager) Cleanup() error {
	if pm.syncCancel != nil {
		pm.syncCancel()
	}
	if pm.syncTicker != nil {
		pm.syncTicker.Stop()
	}

	close(pm.done)
	pm.wg.Wait()

	return nil
}

// CalculateCostFromUsage calculates cost in dollars using pricing manager and usage data with conditional pricing
func (pm *PricingManager) CalculateCostFromUsage(provider string, model string, usage *schemas.LLMUsage, requestType schemas.RequestType, isCacheRead bool, isBatch bool, audioSeconds *int, audioTokenDetails *schemas.AudioTokenDetails) float64 {
	// Allow audio-only flows by only returning early if we have no usage data at all
	if usage == nil && audioSeconds == nil && audioTokenDetails == nil {
		return 0.0
	}

	// Get pricing for the model
	pricing, exists := pm.getPricing(model, provider, requestType)
	if !exists {
		pm.logger.Debug("pricing not found for model %s and provider %s of request type %s, skipping cost calculation", model, provider, normalizeRequestType(requestType))
		return 0.0
	}

	var inputCost, outputCost float64

	// Helper function to safely get token counts with zero defaults
	safeTokenCount := func(usage *schemas.LLMUsage, getter func(*schemas.LLMUsage) int) int {
		if usage == nil {
			return 0
		}
		return getter(usage)
	}

	totalTokens := safeTokenCount(usage, func(u *schemas.LLMUsage) int { return u.TotalTokens })
	promptTokens := safeTokenCount(usage, func(u *schemas.LLMUsage) int {
		if u.ResponsesExtendedResponseUsage != nil {
			return u.ResponsesExtendedResponseUsage.InputTokens
		}
		return u.PromptTokens
	})
	completionTokens := safeTokenCount(usage, func(u *schemas.LLMUsage) int {
		if u.ResponsesExtendedResponseUsage != nil {
			return u.ResponsesExtendedResponseUsage.OutputTokens
		}
		return u.CompletionTokens
	})

	// Special handling for audio operations with duration-based pricing
	if (requestType == schemas.SpeechRequest || requestType == schemas.TranscriptionRequest) && audioSeconds != nil && *audioSeconds > 0 {
		// Determine if this is above TokenTierAbove128K for pricing tier selection
		isAbove128k := totalTokens > TokenTierAbove128K

		// Use duration-based pricing for audio when available
		var audioPerSecondRate *float64
		if isAbove128k && pricing.InputCostPerAudioPerSecondAbove128kTokens != nil {
			audioPerSecondRate = pricing.InputCostPerAudioPerSecondAbove128kTokens
		} else if pricing.InputCostPerAudioPerSecond != nil {
			audioPerSecondRate = pricing.InputCostPerAudioPerSecond
		}

		if audioPerSecondRate != nil {
			inputCost = float64(*audioSeconds) * *audioPerSecondRate
		} else {
			// Fall back to token-based pricing
			inputCost = float64(promptTokens) * pricing.InputCostPerToken
		}

		// For audio operations, output cost is typically based on tokens (if any)
		outputCost = float64(completionTokens) * pricing.OutputCostPerToken

		return inputCost + outputCost
	}

	// Handle audio token details if available (for token-based audio pricing)
	if audioTokenDetails != nil && (requestType == schemas.SpeechRequest || requestType == schemas.TranscriptionRequest) {
		// Use audio-specific token pricing if available
		audioTokens := float64(audioTokenDetails.AudioTokens)
		textTokens := float64(audioTokenDetails.TextTokens)
		isAbove128k := totalTokens > TokenTierAbove128K

		// Determine the appropriate token pricing rates
		var inputTokenRate, outputTokenRate float64

		if isAbove128k {
			inputTokenRate = getSafeFloat64(pricing.InputCostPerTokenAbove128kTokens, pricing.InputCostPerToken)
			outputTokenRate = getSafeFloat64(pricing.OutputCostPerTokenAbove128kTokens, pricing.OutputCostPerToken)
		} else {
			inputTokenRate = pricing.InputCostPerToken
			outputTokenRate = pricing.OutputCostPerToken
		}

		// Calculate costs using token-based pricing with audio/text breakdown
		inputCost = audioTokens*inputTokenRate + textTokens*inputTokenRate
		outputCost = float64(completionTokens) * outputTokenRate

		return inputCost + outputCost
	}

	// Use conditional pricing based on request characteristics
	if isBatch {
		// Use batch pricing if available, otherwise fall back to regular pricing
		if pricing.InputCostPerTokenBatches != nil {
			inputCost = float64(promptTokens) * *pricing.InputCostPerTokenBatches
		} else {
			inputCost = float64(promptTokens) * pricing.InputCostPerToken
		}

		if pricing.OutputCostPerTokenBatches != nil {
			outputCost = float64(completionTokens) * *pricing.OutputCostPerTokenBatches
		} else {
			outputCost = float64(completionTokens) * pricing.OutputCostPerToken
		}
	} else if isCacheRead {
		// Use cache read pricing for input tokens if available, regular pricing for output
		if pricing.CacheReadInputTokenCost != nil {
			inputCost = float64(promptTokens) * *pricing.CacheReadInputTokenCost
		} else {
			inputCost = float64(promptTokens) * pricing.InputCostPerToken
		}

		// Output tokens always use regular pricing for cache reads
		outputCost = float64(completionTokens) * pricing.OutputCostPerToken
	} else {
		// Use regular pricing
		inputCost = float64(promptTokens) * pricing.InputCostPerToken
		outputCost = float64(completionTokens) * pricing.OutputCostPerToken
	}

	totalCost := inputCost + outputCost

	return totalCost
}

// populateModelPool populates the model pool with all available models per provider (thread-safe)
func (pm *PricingManager) populateModelPool() {
	// Acquire write lock for the entire rebuild operation
	pm.mu.Lock()
	defer pm.mu.Unlock()

	// Clear existing model pool
	pm.modelPool = make(map[schemas.ModelProvider][]string)

	// Map to track unique models per provider
	providerModels := make(map[schemas.ModelProvider]map[string]bool)

	// Iterate through all pricing data to collect models per provider
	for _, pricing := range pm.pricingData {
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
		pm.modelPool[provider] = models
	}

	// Log the populated model pool for debugging
	totalModels := 0
	for provider, models := range pm.modelPool {
		totalModels += len(models)
		pm.logger.Debug("populated %d models for provider %s", len(models), string(provider))
	}
	pm.logger.Info("populated model pool with %d models across %d providers", totalModels, len(pm.modelPool))
}

// GetModelsForProvider returns all available models for a given provider (thread-safe)
func (pm *PricingManager) GetModelsForProvider(provider schemas.ModelProvider) []string {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	models, exists := pm.modelPool[provider]
	if !exists {
		return []string{}
	}

	// Return a copy to prevent external modification
	result := make([]string, len(models))
	copy(result, models)
	return result
}

// GetProvidersForModel returns all providers for a given model (thread-safe)
func (pm *PricingManager) GetProvidersForModel(model string) []schemas.ModelProvider {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	providers := make([]schemas.ModelProvider, 0)
	for provider, models := range pm.modelPool {
		if slices.Contains(models, model) {
			providers = append(providers, provider)
		}
	}
	return providers
}

// getPricing returns pricing information for a model (thread-safe)
func (pm *PricingManager) getPricing(model, provider string, requestType schemas.RequestType) (*configstore.TableModelPricing, bool) {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	pricing, ok := pm.pricingData[makeKey(model, provider, normalizeRequestType(requestType))]
	if !ok {
		// Lookup in vertex if gemini not found
		if provider == string(schemas.Gemini) {
			pricing, ok = pm.pricingData[makeKey(model, "vertex", normalizeRequestType(requestType))]
			if ok {
				return &pricing, true
			}
		}

		// Lookup in chat if responses not found
		if requestType == schemas.ResponsesRequest || requestType == schemas.ResponsesStreamRequest {
			pricing, ok = pm.pricingData[makeKey(model, provider, normalizeRequestType(schemas.ChatCompletionRequest))]
			if ok {
				return &pricing, true
			}
		}

		return nil, false
	}
	return &pricing, true
}
