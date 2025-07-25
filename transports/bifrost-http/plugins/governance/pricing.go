// Package governance provides pricing management for AI model cost calculations
package governance

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"gorm.io/gorm"
)

// Default sync interval and config key
const (
	DefaultPricingSyncInterval = 24 * time.Hour
	LastPricingSyncKey         = "LastModelPricingSync"
	PricingFileURL             = "https://getbifrost.ai/datasheet"
)

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

// PricingManager handles model pricing data synchronization and access
type PricingManager struct {
	db     *gorm.DB
	logger schemas.Logger

	// In-memory cache for fast access
	pricingCache []ModelPricing
	mu           sync.RWMutex

	// Background sync worker
	syncTicker *time.Ticker
	done       chan struct{}
	wg         sync.WaitGroup
}

// NewPricingManager creates a new pricing manager
func NewPricingManager(db *gorm.DB, logger schemas.Logger) (*PricingManager, error) {
	if db == nil {
		return nil, fmt.Errorf("database connection cannot be nil")
	}

	pm := &PricingManager{
		db:           db,
		logger:       logger,
		pricingCache: make([]ModelPricing, 0),
		done:         make(chan struct{}),
	}

	// Load initial pricing data
	if err := pm.loadPricingFromDatabase(); err != nil {
		return nil, fmt.Errorf("failed to load initial pricing data: %w", err)
	}

	// Sync pricing data from file to database
	if pm.shouldSync() {
		if err := pm.syncPricing(); err != nil {
			return nil, fmt.Errorf("failed to sync pricing data: %w", err)
		}
	}

	// Start background sync worker
	pm.startSyncWorker()

	return pm, nil
}

// calculateCostForUsage calculates cost in dollars using pricing manager and usage data with conditional pricing
func (pm *PricingManager) calculateCostForUsage(provider string, model string, usage *schemas.LLMUsage, requestType string, isCacheRead bool, isBatch bool, audioSeconds *int, audioTokenDetails *schemas.AudioTokenDetails) float64 {
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
	pricing, exists := pm.getPricing(model, provider, requestType)
	if !exists {
		pm.logger.Warn(fmt.Sprintf("Pricing not found for model %s and provider %s of request type %s, skipping cost calculation", model, provider, requestType))
		return 0.0
	}

	var inputCost, outputCost float64

	// Special handling for audio operations with duration-based pricing
	if (requestType == "audio_speech" || requestType == "audio_transcription") && audioSeconds != nil && *audioSeconds > 0 {
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
	if audioTokenDetails != nil && (requestType == "audio_speech" || requestType == "audio_transcription") {
		// Use audio-specific token pricing if available
		audioTokens := float64(audioTokenDetails.AudioTokens)
		textTokens := float64(audioTokenDetails.TextTokens)
		isAbove128k := usage.TotalTokens > 128000

		// Determine the appropriate token pricing rates
		var inputTokenRate, outputTokenRate float64

		if isAbove128k {
			inputTokenRate = pm.getSafeFloat64(pricing.InputCostPerTokenAbove128kTokens, pricing.InputCostPerToken)
			outputTokenRate = pm.getSafeFloat64(pricing.OutputCostPerTokenAbove128kTokens, pricing.OutputCostPerToken)
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

// getSafeFloat64 returns the value of a float64 pointer or fallback if nil
func (pm *PricingManager) getSafeFloat64(ptr *float64, fallback float64) float64 {
	if ptr != nil {
		return *ptr
	}
	return fallback
}

// getPricing returns pricing information for a model (thread-safe)
func (pm *PricingManager) getPricing(model, provider, requestType string) (*ModelPricing, bool) {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	for _, pricing := range pm.pricingCache {
		if pricing.Model == model && pricing.Provider == provider && pricing.Mode == requestType {
			return &pricing, true
		}
	}
	return nil, false
}

// shouldSync checks if pricing data should be synced based on last sync time
func (pm *PricingManager) shouldSync() bool {
	var config Config
	err := pm.db.First(&config, "key = ?", LastPricingSyncKey).Error
	if err != nil {
		// No sync record found, should sync
		return true
	}

	lastSync, err := time.Parse(time.RFC3339, config.Value)
	if err != nil {
		pm.logger.Warn(fmt.Sprintf("Failed to parse last sync time: %v", err))
		return true
	}

	return time.Since(lastSync) >= DefaultPricingSyncInterval
}

// syncPricing syncs pricing data from URL to database and updates cache
func (pm *PricingManager) syncPricing() error {
	pm.logger.Debug("Starting pricing data synchronization for governance")

	// Load pricing data from URL
	pricingData, err := pm.loadPricingFromURL()
	if err != nil {
		// Check if we have existing data in database
		var count int64
		pm.db.Model(&ModelPricing{}).Count(&count)
		if count > 0 {
			pm.logger.Error(fmt.Errorf("failed to load pricing data from URL, but existing data found in database: %w", err))
			return nil
		} else {
			return fmt.Errorf("failed to load pricing data from URL and no existing data in database: %w", err)
		}
	}

	// Update database in transaction
	err = pm.db.Transaction(func(tx *gorm.DB) error {
		// Clear existing pricing data
		if err := tx.Delete(&ModelPricing{}, "1=1").Error; err != nil {
			return fmt.Errorf("failed to clear existing pricing data: %w", err)
		}

		// Insert new pricing data
		for modelKey, entry := range pricingData {
			provider := entry.Provider

			if provider == "vertex_ai-language-models" {
				provider = "vertex"
			}

			// Handle provider/model format - extract just the model name
			modelName := modelKey
			if strings.Contains(modelKey, "/") {
				parts := strings.Split(modelKey, "/")
				if len(parts) > 1 {
					if parts[0] != provider {
						continue
					}
					modelName = parts[1]
				}
			}

			// Check if entry already exists
			var existingCount int64
			tx.Model(&ModelPricing{}).Where("model = ? AND provider = ? AND mode = ?",
				modelName, provider, entry.Mode).Count(&existingCount)

			if existingCount > 0 {
				continue
			}

			pricing := &ModelPricing{
				Model:              modelName,
				Provider:           provider,
				InputCostPerToken:  entry.InputCostPerToken,
				OutputCostPerToken: entry.OutputCostPerToken,
				Mode:               entry.Mode,

				// Additional pricing for media
				InputCostPerImage:          entry.InputCostPerImage,
				InputCostPerVideoPerSecond: entry.InputCostPerVideoPerSecond,
				InputCostPerAudioPerSecond: entry.InputCostPerAudioPerSecond,

				// Character-based pricing
				InputCostPerCharacter:  entry.InputCostPerCharacter,
				OutputCostPerCharacter: entry.OutputCostPerCharacter,

				// Pricing above 128k tokens
				InputCostPerTokenAbove128kTokens:          entry.InputCostPerTokenAbove128kTokens,
				InputCostPerCharacterAbove128kTokens:      entry.InputCostPerCharacterAbove128kTokens,
				InputCostPerImageAbove128kTokens:          entry.InputCostPerImageAbove128kTokens,
				InputCostPerVideoPerSecondAbove128kTokens: entry.InputCostPerVideoPerSecondAbove128kTokens,
				InputCostPerAudioPerSecondAbove128kTokens: entry.InputCostPerAudioPerSecondAbove128kTokens,
				OutputCostPerTokenAbove128kTokens:         entry.OutputCostPerTokenAbove128kTokens,
				OutputCostPerCharacterAbove128kTokens:     entry.OutputCostPerCharacterAbove128kTokens,

				// Cache and batch pricing
				CacheReadInputTokenCost:   entry.CacheReadInputTokenCost,
				InputCostPerTokenBatches:  entry.InputCostPerTokenBatches,
				OutputCostPerTokenBatches: entry.OutputCostPerTokenBatches,
			}

			if err := tx.Create(pricing).Error; err != nil {
				return fmt.Errorf("failed to create pricing record for model %s: %w", modelName, err)
			}
		}

		return nil
	})

	if err != nil {
		return fmt.Errorf("failed to sync pricing data to database: %w", err)
	}

	config := &Config{
		Key:   LastPricingSyncKey,
		Value: time.Now().Format(time.RFC3339),
	}

	// Update last sync time
	if err := pm.db.Save(config).Error; err != nil {
		pm.logger.Warn(fmt.Sprintf("Failed to update last sync time: %v", err))
	}

	// Reload cache from database
	if err := pm.loadPricingFromDatabase(); err != nil {
		return fmt.Errorf("failed to reload pricing cache: %w", err)
	}

	pm.logger.Info(fmt.Sprintf("Successfully synced %d pricing records", len(pricingData)))
	return nil
}

// loadPricingFromURL loads pricing data from the remote URL
func (pm *PricingManager) loadPricingFromURL() (PricingData, error) {
	// Create HTTP client with timeout
	client := &http.Client{
		Timeout: 30 * time.Second,
	}

	// Make HTTP request
	resp, err := client.Get(PricingFileURL)
	if err != nil {
		return nil, fmt.Errorf("failed to download pricing data: %w", err)
	}
	defer resp.Body.Close()

	// Check HTTP status
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to download pricing data: HTTP %d", resp.StatusCode)
	}

	// Read response body
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read pricing data response: %w", err)
	}

	// Unmarshal JSON data
	var pricingData PricingData
	if err := json.Unmarshal(data, &pricingData); err != nil {
		return nil, fmt.Errorf("failed to unmarshal pricing data: %w", err)
	}

	pm.logger.Debug(fmt.Sprintf("Successfully downloaded and parsed %d pricing records", len(pricingData)))
	return pricingData, nil
}

// loadPricingFromDatabase loads pricing data from database into memory cache
func (pm *PricingManager) loadPricingFromDatabase() error {
	var pricingRecords []ModelPricing
	if err := pm.db.Find(&pricingRecords).Error; err != nil {
		return fmt.Errorf("failed to load pricing from database: %w", err)
	}

	pm.mu.Lock()
	defer pm.mu.Unlock()

	// Clear and rebuild cache
	pm.pricingCache = pricingRecords

	pm.logger.Debug(fmt.Sprintf("Loaded %d pricing records into cache", len(pricingRecords)))
	return nil
}

// startSyncWorker starts the background sync worker
func (pm *PricingManager) startSyncWorker() {
	// Use a ticker that checks every hour, but only sync when needed
	pm.syncTicker = time.NewTicker(1 * time.Hour)
	pm.wg.Add(1)
	go pm.syncWorker()
}

// syncWorker runs the background sync check
func (pm *PricingManager) syncWorker() {
	defer pm.wg.Done()

	for {
		select {
		case <-pm.syncTicker.C:
			if pm.shouldSync() {
				if err := pm.syncPricing(); err != nil {
					pm.logger.Error(fmt.Errorf("background pricing sync failed: %w", err))
				}
			}

		case <-pm.done:
			return
		}
	}
}

// Cleanup stops the background sync worker and waits for it to finish
func (pm *PricingManager) Cleanup() {
	if pm.syncTicker != nil {
		pm.syncTicker.Stop()
	}

	close(pm.done)
	pm.wg.Wait()
}
