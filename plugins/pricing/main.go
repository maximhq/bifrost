package pricing

import (
	"context"
	"fmt"
	"strings"
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
)

// PluginName is the name of the governance plugin
const PluginName = "pricing"

const (
	pricingCostContextKey schemas.BifrostContextKey = "bf-pricing-cost"
)

type PricingPlugin struct {
	configStore configstore.ConfigStore
	logger      schemas.Logger

	// In-memory cache for fast access - direct map for O(1) lookups
	pricingData map[string]configstore.TableModelPricing
	mu          sync.RWMutex

	// Background sync worker
	syncTicker *time.Ticker
	done       chan struct{}
	wg         sync.WaitGroup
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

func Init(configStore configstore.ConfigStore, logger schemas.Logger) (schemas.Plugin, error) {
	plugin := &PricingPlugin{
		configStore: configStore,
		logger:      logger,
		pricingData: make(map[string]configstore.TableModelPricing),
		done:        make(chan struct{}),
	}

	if configStore != nil {
		// Load initial pricing data
		if err := plugin.loadPricingFromDatabase(); err != nil {
			return nil, fmt.Errorf("failed to load initial pricing data: %w", err)
		}

		// Sync pricing data from file to database
		if err := plugin.checkAndSyncPricing(); err != nil {
			return nil, fmt.Errorf("failed to sync pricing data: %w", err)
		}
	} else {
		// Load pricing data from config memory
		if err := plugin.loadPricingIntoMemory(); err != nil {
			return nil, fmt.Errorf("failed to load pricing data from config memory: %w", err)
		}
	}

	// Start background sync worker
	plugin.startSyncWorker()
	plugin.configStore = configStore
	plugin.logger = logger

	return plugin, nil
}

func (p *PricingPlugin) GetName() string {
	return PluginName
}

func (p *PricingPlugin) PreHook(ctx *context.Context, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.PluginShortCircuit, error) {
	return req, nil, nil
}

func (p *PricingPlugin) PostHook(ctx *context.Context, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	if result == nil || err != nil {
		return result, err, nil
	}

	// Extract provider and model from stored context values (set in PreHook)
	var provider schemas.ModelProvider
	var model string
	var requestType schemas.RequestType

	if providerValue := (*ctx).Value(schemas.BifrostContextKeyRequestProvider); providerValue != nil {
		if p, ok := providerValue.(schemas.ModelProvider); ok {
			provider = p
		}
	}
	if modelValue := (*ctx).Value(schemas.BifrostContextKeyRequestModel); modelValue != nil {
		if m, ok := modelValue.(string); ok {
			model = m
		}
	}
	if requestTypeValue := (*ctx).Value(schemas.BifrostContextKeyRequestType); requestTypeValue != nil {
		if r, ok := requestTypeValue.(schemas.RequestType); ok {
			requestType = r
		}
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

	if usage != nil || audioSeconds != nil || audioTokenDetails != nil {
		cost := p.calculateCostForUsage(string(provider), model, usage, requestType, isCacheRead, isBatch, audioSeconds, audioTokenDetails)
		*ctx = context.WithValue(*ctx, pricingCostContextKey, cost)
	}

	return result, err, nil
}

func (p *PricingPlugin) Cleanup() error {
	if p.syncTicker != nil {
		p.syncTicker.Stop()
	}

	close(p.done)
	p.wg.Wait()

	return nil
}

// calculateCostForUsage calculates cost in dollars using pricing manager and usage data with conditional pricing
func (p *PricingPlugin) calculateCostForUsage(provider string, model string, usage *schemas.LLMUsage, requestType schemas.RequestType, isCacheRead bool, isBatch bool, audioSeconds *int, audioTokenDetails *schemas.AudioTokenDetails) float64 {
	if usage == nil {
		return 0.0
	}

	if strings.Contains(model, "/") {
		parts := strings.Split(model, "/")
		if len(parts) > 1 {
			model = parts[1]
		}
	}

	// Get pricing for the model
	pricing, exists := p.getPricing(model, provider, requestType)
	if !exists {
		p.logger.Warn("pricing not found for model %s and provider %s of request type %s, skipping cost calculation", model, provider, requestType)
		return 0.0
	}

	var inputCost, outputCost float64

	// Special handling for audio operations with duration-based pricing
	if (requestType == schemas.SpeechRequest || requestType == schemas.TranscriptionRequest) && audioSeconds != nil && *audioSeconds > 0 {
		// Determine if this is above 128k tokens for pricing tier selection
		isAbove128k := usage.TotalTokens > 128000

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
			inputCost = float64(usage.PromptTokens) * pricing.InputCostPerToken
		}

		// For audio operations, output cost is typically based on tokens (if any)
		outputCost = float64(usage.CompletionTokens) * pricing.OutputCostPerToken

		return inputCost + outputCost
	}

	// Handle audio token details if available (for token-based audio pricing)
	if audioTokenDetails != nil && (requestType == schemas.SpeechRequest || requestType == schemas.TranscriptionRequest) {
		// Use audio-specific token pricing if available
		audioTokens := float64(audioTokenDetails.AudioTokens)
		textTokens := float64(audioTokenDetails.TextTokens)
		isAbove128k := usage.TotalTokens > 128000

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
		outputCost = float64(usage.CompletionTokens) * outputTokenRate

		return inputCost + outputCost
	}

	// Use conditional pricing based on request characteristics
	if isBatch {
		// Use batch pricing if available, otherwise fall back to regular pricing
		if pricing.InputCostPerTokenBatches != nil {
			inputCost = float64(usage.PromptTokens) * *pricing.InputCostPerTokenBatches
		} else {
			inputCost = float64(usage.PromptTokens) * pricing.InputCostPerToken
		}

		if pricing.OutputCostPerTokenBatches != nil {
			outputCost = float64(usage.CompletionTokens) * *pricing.OutputCostPerTokenBatches
		} else {
			outputCost = float64(usage.CompletionTokens) * pricing.OutputCostPerToken
		}
	} else if isCacheRead {
		// Use cache read pricing for input tokens if available, regular pricing for output
		if pricing.CacheReadInputTokenCost != nil {
			inputCost = float64(usage.PromptTokens) * *pricing.CacheReadInputTokenCost
		} else {
			inputCost = float64(usage.PromptTokens) * pricing.InputCostPerToken
		}

		// Output tokens always use regular pricing for cache reads
		outputCost = float64(usage.CompletionTokens) * pricing.OutputCostPerToken
	} else {
		// Use regular pricing
		inputCost = float64(usage.PromptTokens) * pricing.InputCostPerToken
		outputCost = float64(usage.CompletionTokens) * pricing.OutputCostPerToken
	}

	totalCost := inputCost + outputCost

	return totalCost
}

// getPricing returns pricing information for a model (thread-safe)
func (p *PricingPlugin) getPricing(model, provider string, requestType schemas.RequestType) (*configstore.TableModelPricing, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	pricing, ok := p.pricingData[makeKey(model, provider, normalizeRequestType(requestType))]
	if !ok {
		return nil, false
	}
	return &pricing, true
}
