package modelcatalog

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"sync"
	"time"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/tidwall/gjson"
	"gorm.io/gorm"
)

const (
	urlFetchMaxRetries = 3                // retries after the first attempt (4 attempts total)
	urlFetchMaxBackoff = 10 * time.Second // cap for exponential backoff (steps start at 1s)
)

// syncPricing syncs pricing data from URL to database and updates cache
func (mc *ModelCatalog) syncPricing(ctx context.Context) error {
	if mc.shouldSyncGate != nil {
		if !mc.shouldSyncGate(ctx) {
			return nil
		}
	}
	// Load pricing data from URL
	pricingData, err := WithRetries(ctx, urlFetchMaxRetries, urlFetchMaxBackoff, func() (map[string]PricingEntry, error) {
		return mc.loadPricingFromURL(ctx)
	})
	if err != nil {
		// Check if we have existing data in database
		pricingRecords, pricingErr := mc.configStore.GetModelPrices(ctx)
		if pricingErr != nil {
			return fmt.Errorf("failed to get pricing records: %w", pricingErr)
		}
		if len(pricingRecords) > 0 {
			mc.logger.Warn("failed to fetch pricing from URL, falling back to existing database records: %v", err)
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

	// Reload cache from database
	if err := mc.loadPricingFromDatabase(ctx); err != nil {
		return fmt.Errorf("failed to reload pricing cache: %w", err)
	}

	// Populate model params cache from pricing datasheet max_output_tokens
	mc.populateModelParamsFromPricing(pricingData)

	mc.logger.Debug("successfully synced %d pricing records", len(pricingData))
	return nil
}

// populateModelParamsFromPricing extracts max_output_tokens from pricing entries
// and populates the model params cache so that providers can look up max output
// tokens without a separate model-parameters sync.
func (mc *ModelCatalog) populateModelParamsFromPricing(pricingData map[string]PricingEntry) {
	modelParamsEntries := make(map[string]providerUtils.ModelParams)
	for modelKey, entry := range pricingData {
		if entry.MaxOutputTokens != nil {
			modelName := extractModelName(modelKey)
			modelParamsEntries[modelName] = providerUtils.ModelParams{MaxOutputTokens: entry.MaxOutputTokens}
		}
	}
	if len(modelParamsEntries) > 0 {
		providerUtils.BulkSetModelParams(modelParamsEntries)
		mc.logger.Debug("populated %d model params entries from pricing datasheet", len(modelParamsEntries))
	}
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

// loadPricingIntoMemoryFromURL loads pricing data from URL into memory cache (when config store is not available)
func (mc *ModelCatalog) loadPricingIntoMemoryFromURL(ctx context.Context) error {
	pricingData, err := WithRetries(ctx, urlFetchMaxRetries, urlFetchMaxBackoff, func() (map[string]PricingEntry, error) {
		return mc.loadPricingFromURL(ctx)
	})
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

	// Populate model params cache from pricing datasheet max_output_tokens
	mc.populateModelParamsFromPricing(pricingData)

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

	mc.logger.Debug("loaded %d pricing records from database into memory", len(mc.pricingData))
	return nil
}

// loadModelParametersFromDatabase bulk-loads model parameters from the DB into the provider
// utils cache (startup / ReloadFromDB). The SetCacheMissHandler path still loads one row at
// a time on cache miss; both use the same table JSON shape.
// Returns the number of rows loaded so callers can decide whether to background-sync from URL.
func (mc *ModelCatalog) loadModelParametersFromDatabase(ctx context.Context) (int, error) {
	if mc.configStore == nil {
		return 0, nil
	}

	rows, err := mc.configStore.GetModelParameters(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to load model parameters from database: %w", err)
	}
	if len(rows) == 0 {
		mc.logger.Debug("no model parameters rows in database")
		return 0, nil
	}

	paramsData := make(map[string]json.RawMessage, len(rows))
	for _, row := range rows {
		paramsData[row.Model] = json.RawMessage(row.Data)
	}
	mc.applyModelParameters(paramsData)
	mc.logger.Debug("loaded %d model parameters records from database into cache", len(rows))
	return len(rows), nil
}

// startSyncWorker starts the background sync worker
func (mc *ModelCatalog) startSyncWorker(ctx context.Context) {
	// IMPORTANT: scheduling model
	//
	// The sync worker wakes on a fixed ticker (syncWorkerTickerPeriod = 1h).
	// On each wake it calls checkAndSyncPricing, which checks:
	//
	//   time.Since(lastSyncTimestamp) >= pricingSyncInterval
	//
	// This means:
	//   • pricingSyncInterval defines the *minimum elapsed time* between syncs.
	//   • The actual sync frequency = max(syncWorkerTickerPeriod, pricingSyncInterval).
	//   • Setting pricingSyncInterval < 1h does NOT increase sync frequency —
	//     the hourly ticker is the hard lower bound on check granularity.
	//
	// Design rationale: avoids high-frequency polling while allowing operators to
	// tune how stale pricing data can get (e.g., 1h vs 24h vs 7d).
	mc.syncTicker = time.NewTicker(syncWorkerTickerPeriod)
	mc.wg.Add(1)
	go mc.syncWorker(ctx)
}

// withDistributedLock acquires a named distributed lock and executes fn under it.
// Pass retries=0 to block until acquired (Lock); pass retries>0 to use LockWithRetry.
func (mc *ModelCatalog) withDistributedLock(ctx context.Context, key string, retries int, fn func() error) error {
	lock, err := mc.distributedLockManager.NewLock(key)
	if err != nil {
		return fmt.Errorf("failed to create lock %q: %w", key, err)
	}
	if retries > 0 {
		if err := lock.LockWithRetry(ctx, retries); err != nil {
			return fmt.Errorf("failed to acquire lock %q: %w", key, err)
		}
	} else {
		if err := lock.Lock(ctx); err != nil {
			return fmt.Errorf("failed to acquire lock %q: %w", key, err)
		}
	}
	// Use a fresh context for unlock so that a cancelled or timed-out work context
	// does not prevent the lock row from being deleted. If we reused ctx and it was
	// already cancelled when the defer fires, ReleaseLock's DB call would fail
	// silently and the lock would stay in the database until TTL expiry (30s),
	// blocking every other node from acquiring it during that window.
	defer func() {
		if err := lock.Unlock(context.Background()); err != nil {
			mc.logger.Warn("failed to release distributed lock %q: %v", key, err)
		}
	}()
	return fn()
}

// syncTick performs a single sync tick with proper lock management
// if the last sync was more than the sync interval ago, sync pricing and model parameters in parallel
func (mc *ModelCatalog) syncTick(ctx context.Context) {
	mc.syncMu.RLock()
	lastSync := mc.lastSyncedAt
	interval := mc.syncInterval
	mc.syncMu.RUnlock()

	if time.Since(lastSync) >= interval {
		mc.logger.Debug("starting model catalog background sync")
		if err := mc.withDistributedLock(ctx, "model_catalog_pricing_sync", 10, func() error {
			// Sync pricing and model parameters in parallel
			var wg sync.WaitGroup
			var pricingErr, paramsErr error
			wg.Add(2)
			go func() {
				defer wg.Done()
				if err := mc.syncPricing(ctx); err != nil {
					mc.logger.Error("background pricing sync failed: %v", err)
					pricingErr = err
				}
			}()
			go func() {
				defer wg.Done()
				if err := mc.syncModelParameters(ctx); err != nil {
					mc.logger.Error("background model parameters sync failed: %v", err)
					paramsErr = err
				}
			}()
			wg.Wait()

			if pricingErr == nil && paramsErr == nil {
				if mc.afterSyncHook != nil {
					mc.afterSyncHook(ctx)
				}
				mc.syncMu.Lock()
				mc.lastSyncedAt = time.Now()
				mc.syncMu.Unlock()
			}
			if pricingErr != nil {
				return pricingErr
			}
			return paramsErr
		}); err != nil {
			mc.logger.Error("failed to run model catalog sync: %v", err)
		}
		mc.logger.Debug("model catalog background sync completed")
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

// --- Model Parameters sync ---

func (mc *ModelCatalog) applyModelParameters(paramsData map[string]json.RawMessage) {
	modelParamsEntries := make(map[string]providerUtils.ModelParams, len(paramsData))
	newResponseTypes := make(map[string][]string, len(paramsData))
	newParamsIndex := make(map[string][]string, len(paramsData))

	for model, rawData := range paramsData {
		var parsed modelParametersParseResult
		if err := json.Unmarshal(rawData, &parsed); err != nil {
			mc.logger.Warn("model-parameters-sync: skipping malformed parameters for model %s: %v", model, err)
			continue
		}

		outputs := make([]string, 0, len(parsed.SupportedEndpoints))
		for _, endpoint := range parsed.SupportedEndpoints {
			if normalized := normalizeEndpointToOutputType(endpoint); normalized != "" && !slices.Contains(outputs, normalized) {
				outputs = append(outputs, normalized)
			}
		}

		if parsed.Mode != nil {
			if normalized := normalizeModeToOutputType(*parsed.Mode); normalized != "" && !slices.Contains(outputs, normalized) {
				outputs = append(outputs, normalized)
			}
		}

		if !slices.Contains(outputs, "text_completion") {
			provider := gjson.GetBytes(rawData, "provider")
			if provider.Exists() {
				key := makeKey(model, normalizeProvider(provider.String()), normalizeRequestType(schemas.TextCompletionRequest))

				mc.mu.RLock()
				_, ok := mc.pricingData[key]
				mc.mu.RUnlock()
				if ok {
					outputs = append(outputs, "text_completion")
				}
			}
		}

		if len(outputs) > 0 {
			newResponseTypes[model] = outputs
		}

		supported := extractSupportedParams(&parsed)
		if len(supported) > 0 {
			newParamsIndex[model] = supported
		}

		var p struct {
			MaxOutputTokens *int `json:"max_output_tokens"`
		}
		if p.MaxOutputTokens == nil {
			if err := json.Unmarshal(rawData, &p); err == nil && p.MaxOutputTokens != nil {
				modelParamsEntries[model] = providerUtils.ModelParams{MaxOutputTokens: p.MaxOutputTokens}
			}
		} else {
			modelParamsEntries[model] = providerUtils.ModelParams{MaxOutputTokens: p.MaxOutputTokens}
		}
	}

	mc.mu.Lock()
	mc.supportedResponseTypes = newResponseTypes
	mc.supportedParams = newParamsIndex
	mc.mu.Unlock()

	if len(modelParamsEntries) > 0 {
		providerUtils.BulkSetModelParams(modelParamsEntries)
	}
}

// loadModelParametersIntoMemoryFromURL loads model parameters from the remote URL into the
// provider utils cache (when config store is not available).
func (mc *ModelCatalog) loadModelParametersIntoMemoryFromURL(ctx context.Context) error {
	paramsData, err := WithRetries(ctx, urlFetchMaxRetries, urlFetchMaxBackoff, func() (map[string]json.RawMessage, error) {
		return mc.loadModelParametersFromURL(ctx)
	})
	if err != nil {
		return fmt.Errorf("failed to load model parameters from URL: %w", err)
	}
	mc.applyModelParameters(paramsData)
	return nil
}

// syncModelParameters syncs model parameters data from URL into memory cache
func (mc *ModelCatalog) syncModelParameters(ctx context.Context) error {
	if mc.shouldSyncGate != nil {
		if !mc.shouldSyncGate(ctx) {
			mc.logger.Debug("model parameters sync cancelled by custom gate")
			return nil
		}
	}
	mc.logger.Debug("starting model parameters synchronization")

	paramsData, err := WithRetries(ctx, urlFetchMaxRetries, urlFetchMaxBackoff, func() (map[string]json.RawMessage, error) {
		return mc.loadModelParametersFromURL(ctx)
	})
	if err != nil {
		if mc.configStore != nil {
			rows, dbErr := mc.configStore.GetModelParameters(ctx)
			if dbErr == nil && len(rows) > 0 {
				mc.logger.Error("failed to load model parameters from URL, falling back to existing database records: %v", err)
				return nil
			}
		}
		return fmt.Errorf("failed to load model parameters from URL and no existing data in database: %w", err)
	}

	// Persist to database if config store is available
	if mc.configStore != nil {
		err = mc.configStore.ExecuteTransaction(ctx, func(tx *gorm.DB) error {
			for model, data := range paramsData {
				params := &configstoreTables.TableModelParameters{
					Model: model,
					Data:  string(data),
				}
				if err := mc.configStore.UpsertModelParameters(ctx, params, tx); err != nil {
					return fmt.Errorf("failed to upsert model parameters for model %s: %w", model, err)
				}
			}
			return nil
		})
		if err != nil {
			return fmt.Errorf("failed to sync model parameters to database: %w", err)
		}
	}

	mc.applyModelParameters(paramsData)

	mc.logger.Info("successfully synced %d model parameters records", len(paramsData))
	return nil
}

// loadModelParametersFromURL loads model parameters data from the remote URL
func (mc *ModelCatalog) loadModelParametersFromURL(ctx context.Context) (map[string]json.RawMessage, error) {
	client := &http.Client{}
	client.Timeout = DefaultModelParametersTimeout
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, DefaultModelParametersURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create HTTP request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to download model parameters data: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to download model parameters data: HTTP %d", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read model parameters response: %w", err)
	}

	var paramsData map[string]json.RawMessage
	if err := json.Unmarshal(data, &paramsData); err != nil {
		return nil, fmt.Errorf("failed to unmarshal model parameters data: %w", err)
	}

	mc.logger.Debug("successfully downloaded and parsed %d model parameters records", len(paramsData))
	return paramsData, nil
}