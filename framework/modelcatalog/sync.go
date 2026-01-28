package modelcatalog

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"gorm.io/gorm"
)

// checkAndSyncPricing determines if pricing data needs to be synced and performs the sync if needed.
// It syncs pricing data in the following scenarios:
//   - No config store available (returns early with no error)
//   - No previous sync record exists
//   - Previous sync timestamp is invalid/corrupted
//   - Sync interval has elapsed since last successful sync
func (mc *ModelCatalog) checkAndSyncPricing(ctx context.Context) error {
	// Skip sync if no config store is available
	if mc.configStore == nil {
		return nil
	}

	// Determine if sync is needed and perform it
	needsSync, reason := mc.shouldSyncPricing(ctx)
	if needsSync {
		mc.logger.Debug("pricing sync needed: %s", reason)
		return mc.syncPricing(ctx)
	}

	return nil
}

// shouldSyncPricing determines if pricing data should be synced and returns the reason
func (mc *ModelCatalog) shouldSyncPricing(ctx context.Context) (bool, string) {
	config, err := mc.configStore.GetConfig(ctx, ConfigLastPricingSyncKey)
	if err != nil {
		return true, "no previous sync record found"
	}

	lastSync, err := time.Parse(time.RFC3339, config.Value)
	if err != nil {
		mc.logger.Warn("invalid last sync timestamp: %v", err)
		return true, "corrupted sync timestamp"
	}

	if time.Since(lastSync) >= mc.getPricingSyncInterval() {
		return true, "sync interval elapsed"
	}

	return false, "sync not needed"
}

// syncPricing syncs pricing data from URL to database and updates cache
func (mc *ModelCatalog) syncPricing(ctx context.Context) error {
	mc.logger.Debug("starting pricing data synchronization for governance")
	if mc.shouldSyncPricingFunc != nil {
		if !mc.shouldSyncPricingFunc(ctx) {
			mc.logger.Debug("pricing sync cancelled by custom function")
			return nil
		}
	}
	// Load pricing data from URL
	pricingData, err := mc.loadPricingFromURL(ctx)
	if err != nil {
		// Check if we have existing data in database
		pricingRecords, pricingErr := mc.configStore.GetModelPrices(ctx)
		if pricingErr != nil {
			return fmt.Errorf("failed to get pricing records: %w", pricingErr)
		}
		if len(pricingRecords) > 0 {
			mc.logger.Error("failed to load pricing data from URL, but existing data found in database: %v", err)
			return nil
		} else {
			return fmt.Errorf("failed to load pricing data from URL and no existing data in database: %w", err)
		}
	}

	// Update database in transaction
	err = mc.configStore.ExecuteTransaction(ctx, func(tx *gorm.DB) error {
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
			if err := mc.configStore.UpsertModelPrices(ctx, &pricing, tx); err != nil {
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

	config := &configstoreTables.TableGovernanceConfig{
		Key:   ConfigLastPricingSyncKey,
		Value: time.Now().Format(time.RFC3339),
	}

	// Update last sync time
	if err := mc.configStore.UpdateConfig(ctx, config); err != nil {
		mc.logger.Warn("Failed to update last sync time: %v", err)
	}

	// Reload cache from database
	if err := mc.loadPricingFromDatabase(ctx); err != nil {
		return fmt.Errorf("failed to reload pricing cache: %w", err)
	}

	mc.logger.Info("successfully synced %d pricing records", len(pricingData))
	return nil
}

// loadPricingFromURL loads pricing data from the remote URL
func (mc *ModelCatalog) loadPricingFromURL(ctx context.Context) (map[string]PricingEntry, error) {
	// Create HTTP client with timeout
	client := &http.Client{}
	client.Timeout = DefaultPricingTimeout
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, mc.getPricingURL(), nil)
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
	var pricingData map[string]PricingEntry
	if err := json.Unmarshal(data, &pricingData); err != nil {
		return nil, fmt.Errorf("failed to unmarshal pricing data: %w", err)
	}

	mc.logger.Debug("successfully downloaded and parsed %d pricing records", len(pricingData))
	return pricingData, nil
}

// loadPricingIntoMemory loads pricing data from URL into memory cache
func (mc *ModelCatalog) loadPricingIntoMemory(ctx context.Context) error {
	pricingData, err := mc.loadPricingFromURL(ctx)
	if err != nil {
		return fmt.Errorf("failed to load pricing data from URL: %w", err)
	}

	mc.mu.Lock()
	defer mc.mu.Unlock()

	// Clear and rebuild the pricing map
	mc.pricingData = make(map[string]configstoreTables.TableModelPricing, len(pricingData))
	for modelKey, entry := range pricingData {
		pricing := convertPricingDataToTableModelPricing(modelKey, entry)
		key := makeKey(pricing.Model, pricing.Provider, pricing.Mode)
		mc.pricingData[key] = pricing
	}

	return nil
}

// loadPricingFromDatabase loads pricing data from database into memory cache
func (mc *ModelCatalog) loadPricingFromDatabase(ctx context.Context) error {
	if mc.configStore == nil {
		return nil
	}

	pricingRecords, err := mc.configStore.GetModelPrices(ctx)
	if err != nil {
		return fmt.Errorf("failed to load pricing from database: %w", err)
	}

	mc.mu.Lock()
	defer mc.mu.Unlock()

	// Clear and rebuild the pricing map
	mc.pricingData = make(map[string]configstoreTables.TableModelPricing, len(pricingRecords))
	for _, pricing := range pricingRecords {
		key := makeKey(pricing.Model, pricing.Provider, pricing.Mode)
		mc.pricingData[key] = pricing
	}

	mc.logger.Debug("loaded %d pricing records into cache", len(pricingRecords))
	return nil
}

// startSyncWorker starts the background sync worker
func (mc *ModelCatalog) startSyncWorker(ctx context.Context) {
	// Use a ticker that checks every hour, but only sync when needed
	mc.syncTicker = time.NewTicker(1 * time.Hour)
	mc.wg.Add(1)
	go mc.syncWorker(ctx)
}

// syncTick performs a single sync tick with proper lock management
func (mc *ModelCatalog) syncTick(ctx context.Context) {
	if mc.distributedLockManager == nil {
		if err := mc.checkAndSyncPricing(ctx); err != nil {
			mc.logger.Error("background pricing sync failed: %v", err)
		}
		return
	}
	lock, err := mc.distributedLockManager.NewLock("model_catalog_pricing_sync")
	if err != nil {
		mc.logger.Error("failed to create model catalog pricing sync lock: %v", err)
		return
	}
	if err := lock.Lock(ctx); err != nil {
		mc.logger.Error("failed to acquire model catalog pricing sync lock: %v", err)
		return
	}
	defer lock.Unlock(ctx)
	if err := mc.checkAndSyncPricing(ctx); err != nil {
		mc.logger.Error("background pricing sync failed: %v", err)
	}
}

// syncWorker runs the background sync check
func (mc *ModelCatalog) syncWorker(ctx context.Context) {
	defer mc.wg.Done()
	defer mc.syncTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-mc.syncTicker.C:
			mc.syncTick(ctx)
		case <-mc.done:
			return
		}
	}
}
