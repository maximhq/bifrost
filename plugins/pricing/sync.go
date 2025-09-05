package pricing

import (
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
func (p *PricingPlugin) checkAndSyncPricing() error {
	// Skip sync if no config store is available
	if p.configStore == nil {
		return nil
	}

	// Determine if sync is needed and perform it
	needsSync, reason := p.shouldSyncPricing()
	if needsSync {
		p.logger.Debug("pricing sync needed: %s", reason)
		return p.syncPricing()
	}

	return nil
}

// shouldSyncPricing determines if pricing data should be synced and returns the reason
func (p *PricingPlugin) shouldSyncPricing() (bool, string) {
	config, err := p.configStore.GetConfig(LastPricingSyncKey)
	if err != nil {
		return true, "no previous sync record found"
	}

	lastSync, err := time.Parse(time.RFC3339, config.Value)
	if err != nil {
		p.logger.Warn("invalid last sync timestamp: %v", err)
		return true, "corrupted sync timestamp"
	}

	if time.Since(lastSync) >= DefaultPricingSyncInterval {
		return true, "sync interval elapsed"
	}

	return false, "sync not needed"
}

// syncPricing syncs pricing data from URL to database and updates cache
func (p *PricingPlugin) syncPricing() error {
	p.logger.Debug("Starting pricing data synchronization for governance")

	// Load pricing data from URL
	pricingData, err := p.loadPricingFromURL()
	if err != nil {
		// Check if we have existing data in database
		pricingRecords, err := p.configStore.GetModelPrices()
		if err != nil {
			return fmt.Errorf("failed to get pricing records: %w", err)
		}
		if len(pricingRecords) > 0 {
			p.logger.Error("failed to load pricing data from URL, but existing data found in database: %v", err)
			return nil
		} else {
			return fmt.Errorf("failed to load pricing data from URL and no existing data in database: %w", err)
		}
	}

	// Update database in transaction
	err = p.configStore.ExecuteTransaction(func(tx *gorm.DB) error {
		// Clear existing pricing data
		if err := p.configStore.DeleteModelPrices(tx); err != nil {
			return fmt.Errorf("failed to clear existing pricing data: %v", err)
		}

		// Insert new pricing data
		for modelKey, entry := range pricingData {
			pricing := convertPricingDataToTableModelPricing(modelKey, entry)

			// Check if entry already exists
			var existingCount int64
			tx.Model(&configstore.TableModelPricing{}).Where("model = ? AND provider = ? AND mode = ?",
				pricing.Model, pricing.Provider, pricing.Mode).Count(&existingCount)

			if existingCount > 0 {
				continue
			}

			if err := p.configStore.CreateModelPrices(&pricing, tx); err != nil {
				return fmt.Errorf("failed to create pricing record for model %s: %w", pricing.Model, err)
			}
		}

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
	if err := p.configStore.UpdateConfig(config); err != nil {
		p.logger.Warn("Failed to update last sync time: %v", err)
	}

	// Reload cache from database
	if err := p.loadPricingFromDatabase(); err != nil {
		return fmt.Errorf("failed to reload pricing cache: %w", err)
	}

	p.logger.Info("successfully synced %d pricing records", len(pricingData))
	return nil
}

// loadPricingFromURL loads pricing data from the remote URL
func (p *PricingPlugin) loadPricingFromURL() (PricingData, error) {
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

	p.logger.Debug("successfully downloaded and parsed %d pricing records", len(pricingData))
	return pricingData, nil
}

// loadPricingIntoMemory loads pricing data from URL into memory cache
func (p *PricingPlugin) loadPricingIntoMemory() error {
	pricingData, err := p.loadPricingFromURL()
	if err != nil {
		return fmt.Errorf("failed to load pricing data from URL: %w", err)
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	// Clear and rebuild the pricing map
	p.pricingData = make(map[string]configstore.TableModelPricing, len(pricingData))
	for modelKey, entry := range pricingData {
		pricing := convertPricingDataToTableModelPricing(modelKey, entry)
		key := makeKey(pricing.Model, pricing.Provider, pricing.Mode)
		p.pricingData[key] = pricing
	}

	return nil
}

// loadPricingFromDatabase loads pricing data from database into memory cache
func (p *PricingPlugin) loadPricingFromDatabase() error {
	if p.configStore == nil {
		return nil
	}

	pricingRecords, err := p.configStore.GetModelPrices()
	if err != nil {
		return fmt.Errorf("failed to load pricing from database: %w", err)
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	// Clear and rebuild the pricing map
	p.pricingData = make(map[string]configstore.TableModelPricing, len(pricingRecords))
	for _, pricing := range pricingRecords {
		key := makeKey(pricing.Model, pricing.Provider, pricing.Mode)
		p.pricingData[key] = pricing
	}

	p.logger.Debug("loaded %d pricing records into cache", len(pricingRecords))
	return nil
}

// startSyncWorker starts the background sync worker
func (p *PricingPlugin) startSyncWorker() {
	// Use a ticker that checks every hour, but only sync when needed
	p.syncTicker = time.NewTicker(1 * time.Hour)
	p.wg.Add(1)
	go p.syncWorker()
}

// syncWorker runs the background sync check
func (p *PricingPlugin) syncWorker() {
	defer p.wg.Done()

	for {
		select {
		case <-p.syncTicker.C:
			// Check and sync pricing data - this handles the sync internally
			if err := p.checkAndSyncPricing(); err != nil {
				p.logger.Error("background pricing sync failed: %v", err)
			}

		case <-p.done:
			return
		}
	}
}
