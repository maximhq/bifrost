package pricing

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/maximhq/bifrost/framework/configstore"
	"gorm.io/gorm"
)

// checkAndSyncPricing determines if pricing data needs to be synced and performs the sync if needed.
// It syncs pricing data in the following scenarios:
//   - No config store available (returns early with no error)
//   - No previous sync record exists
//   - Previous sync timestamp is invalid/corrupted
//   - Sync interval has elapsed since last successful sync
func (pm *PricingManager) checkAndSyncPricing(ctx context.Context) error {
	// Skip sync if no config store is available
	if pm.configStore == nil {
		return nil
	}

	// Determine if sync is needed and perform it
	needsSync, reason := pm.shouldSyncPricing(ctx)
	if needsSync {
		pm.logger.Debug("pricing sync needed: %s", reason)
		return pm.syncPricing(ctx)
	}

	return nil
}

// shouldSyncPricing determines if pricing data should be synced and returns the reason
func (pm *PricingManager) shouldSyncPricing(ctx context.Context) (bool, string) {
	config, err := pm.configStore.GetConfig(ctx, LastPricingSyncKey)
	if err != nil {
		return true, "no previous sync record found"
	}

	lastSync, err := time.Parse(time.RFC3339, config.Value)
	if err != nil {
		pm.logger.Warn("invalid last sync timestamp: %v", err)
		return true, "corrupted sync timestamp"
	}

	if time.Since(lastSync) >= DefaultPricingSyncInterval {
		return true, "sync interval elapsed"
	}

	return false, "sync not needed"
}

// syncPricing syncs pricing data from URL to database and updates cache
func (pm *PricingManager) syncPricing(ctx context.Context) error {
	pm.logger.Debug("starting pricing data synchronization for governance")

	// Load pricing data from URL
	pricingData, err := pm.loadPricingFromURL(ctx)
	if err != nil {
		// Check if we have existing data in database
		pricingRecords, pricingErr := pm.configStore.GetModelPrices(ctx)
		if pricingErr != nil {
			return fmt.Errorf("failed to get pricing records: %w", pricingErr)
		}
		if len(pricingRecords) > 0 {
			pm.logger.Error("failed to load pricing data from URL, but existing data found in database: %v", err)
			return nil
		} else {
			return fmt.Errorf("failed to load pricing data from URL and no existing data in database: %w", err)
		}
	}

	// Update database in transaction
	err = pm.configStore.ExecuteTransaction(ctx, func(tx *gorm.DB) error {
		// Clear existing pricing data
		if err := pm.configStore.DeleteModelPrices(ctx, tx); err != nil {
			return fmt.Errorf("failed to clear existing pricing data: %v", err)
		}

		// Deduplicate and insert new pricing data
		seen := make(map[string]bool)
		for modelKey, entry := range pricingData {
			pricing := convertPricingDataToTableModelPricing(modelKey, entry)

			// Create composite key for deduplication
			key := makeKey(pricing.Model, pricing.Provider, pricing.Mode)

			// Skip if already seen
			if exists, ok := seen[key]; ok && exists {
				continue
			}

			// Mark as seen
			seen[key] = true

			if err := pm.configStore.CreateModelPrices(ctx, &pricing, tx); err != nil {
				return fmt.Errorf("failed to create pricing record for model %s: %w", pricing.Model, err)
			}
		}

		// Clear seen map
		seen = nil

		return nil
	})

	if err != nil {
		return fmt.Errorf("failed to sync pricing data to database: %w", err)
	}

	config := &configstore.TableConfig{
		Key:   LastPricingSyncKey,
		Value: time.Now().Format(time.RFC3339),
	}

	// Update last sync time
	if err := pm.configStore.UpdateConfig(ctx, config); err != nil {
		pm.logger.Warn("Failed to update last sync time: %v", err)
	}

	// Reload cache from database
	if err := pm.loadPricingFromDatabase(ctx); err != nil {
		return fmt.Errorf("failed to reload pricing cache: %w", err)
	}

	pm.logger.Info("successfully synced %d pricing records", len(pricingData))
	return nil
}

// loadPricingFromURL loads pricing data from the remote URL
func (pm *PricingManager) loadPricingFromURL(ctx context.Context) (PricingData, error) {
	// Create HTTP client with timeout
	client := &http.Client{
		Timeout: 30 * time.Second,
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, PricingFileURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create HTTP request: %w", err)
	}
	// Make HTTP request
	resp, err := client.Do(req)
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

	pm.logger.Debug("successfully downloaded and parsed %d pricing records", len(pricingData))
	return pricingData, nil
}

// loadPricingIntoMemory loads pricing data from URL into memory cache
func (pm *PricingManager) loadPricingIntoMemory(ctx context.Context) error {
	pricingData, err := pm.loadPricingFromURL(ctx)
	if err != nil {
		return fmt.Errorf("failed to load pricing data from URL: %w", err)
	}

	pm.mu.Lock()
	defer pm.mu.Unlock()

	// Clear and rebuild the pricing map
	pm.pricingData = make(map[string]configstore.TableModelPricing, len(pricingData))
	for modelKey, entry := range pricingData {
		pricing := convertPricingDataToTableModelPricing(modelKey, entry)
		key := makeKey(pricing.Model, pricing.Provider, pricing.Mode)
		pm.pricingData[key] = pricing
	}

	return nil
}

// loadPricingFromDatabase loads pricing data from database into memory cache
func (pm *PricingManager) loadPricingFromDatabase(ctx context.Context) error {
	if pm.configStore == nil {
		return nil
	}

	pricingRecords, err := pm.configStore.GetModelPrices(ctx)
	if err != nil {
		return fmt.Errorf("failed to load pricing from database: %w", err)
	}

	pm.mu.Lock()
	defer pm.mu.Unlock()

	// Clear and rebuild the pricing map
	pm.pricingData = make(map[string]configstore.TableModelPricing, len(pricingRecords))
	for _, pricing := range pricingRecords {
		key := makeKey(pricing.Model, pricing.Provider, pricing.Mode)
		pm.pricingData[key] = pricing
	}

	pm.logger.Debug("loaded %d pricing records into cache", len(pricingRecords))
	return nil
}

// startSyncWorker starts the background sync worker
func (pm *PricingManager) startSyncWorker(ctx context.Context) {
	// Use a ticker that checks every hour, but only sync when needed
	pm.syncTicker = time.NewTicker(1 * time.Hour)
	pm.wg.Add(1)
	go pm.syncWorker(ctx)
}

// syncWorker runs the background sync check
func (pm *PricingManager) syncWorker(ctx context.Context) {
	defer pm.wg.Done()
	defer pm.syncTicker.Stop()

	for {
		select {
		case <-pm.syncTicker.C:
			// Check and sync pricing data - this handles the sync internally
			if err := pm.checkAndSyncPricing(ctx); err != nil {
				pm.logger.Error("background pricing sync failed: %v", err)
			}

		case <-pm.done:
			return
		}
	}
}
