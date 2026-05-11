package modelcatalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"sync"
	"time"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/tidwall/gjson"
	"gorm.io/gorm"
)

const (
	urlFetchMaxRetries = 3                // retries after the first attempt (4 attempts total)
	urlFetchMaxBackoff = 10 * time.Second // cap for exponential backoff (steps start at 1s)

	// syncLockTTL is the TTL for distributed locks that cover the full URL→DB sync
	// of pricing and model parameters. The sync UPSERTs ~9000 rows; over a slow
	// pooled Postgres connection this can take 60-120s. The heartbeat (see
	// withDistributedSyncLock) extends the lock periodically while the sync runs,
	// so this TTL only acts as the fallback if the holder process dies. Keep it
	// long enough that a brief pooler stall does not cause a second replica to
	// steal the lock and start a concurrent UPSERT batch (the original deadlock
	// trigger).
	syncLockTTL = 5 * time.Minute

	// syncLockHeartbeatInterval is how often the heartbeat goroutine extends the
	// lock's TTL. Set to TTL/3 so two consecutive missed heartbeats still leave
	// time before TTL expiry — gives operators a margin against transient DB
	// blips without making the lock a no-op.
	syncLockHeartbeatInterval = syncLockTTL / 3
)

// errLockHeldByPeer is a sentinel used by withDistributedLockSkipIfHeld to
// signal that the operation was skipped because another node already holds the
// lock. Callers should treat this as non-fatal: the leader is performing the
// sync, so this replica can rely on its in-memory cache (loaded earlier from
// the DB) and pick up fresh data on the next interval.
var errLockHeldByPeer = errors.New("distributed lock held by peer; skipping")

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
			params := providerUtils.ModelParams{
				MaxOutputTokens: entry.MaxOutputTokens,
			}
			modelParamsEntries[modelName] = params
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

// Background context for the locking model:
//
// A previous version used a 30-second TTL with no heartbeat, plus a blocking
// LockWithRetry that backed off exponentially up to ~3 minutes. The
// pricing/model-parameters startup sync UPSERTs ~9000 rows over a pooled
// Postgres connection and routinely takes 60–120s. With a 30s TTL the lock
// would expire mid-sync, a second replica would clean up the (now-expired)
// lock row, acquire its own lock, and start a concurrent UPSERT batch. Two
// transactions then took row-level locks on the same set of
// governance_model_parameters rows in different orders → Postgres deadlock
// (40P01) → fatal at startup → ACA restart loop.
//
// The new design uses (a) skip-if-held semantics so non-leader replicas never
// run a redundant sync, and (b) a heartbeat goroutine that extends the lock's
// TTL while fn runs, so even if a peer is doing the sync, its lock survives
// long pooler stalls.

// withDistributedLockSkipIfHeld attempts to acquire the lock once. If the lock
// is currently held by another node, fn is NOT executed and errLockHeldByPeer
// is returned. This is the preferred mode for startup syncs: a single leader
// replica performs the URL→DB sync while the others rely on the data already
// loaded into their in-memory cache from the DB. The next periodic sync (every
// syncInterval, default 24h) will eventually run on whichever node holds the
// lock at that moment.
func (mc *ModelCatalog) withDistributedLockSkipIfHeld(ctx context.Context, key string, fn func() error) error {
	if mc.distributedLockManager == nil {
		return fn()
	}
	lock, err := mc.distributedLockManager.NewLockWithTTL(key, syncLockTTL)
	if err != nil {
		return fmt.Errorf("failed to create lock %q: %w", key, err)
	}
	acquired, err := lock.TryLock(ctx)
	if err != nil {
		return fmt.Errorf("failed to try-acquire lock %q: %w", key, err)
	}
	if !acquired {
		mc.logger.Info("distributed lock %q held by another node; skipping sync on this replica", key)
		return errLockHeldByPeer
	}

	heartbeatCtx, stopHeartbeat := context.WithCancel(context.Background())
	defer stopHeartbeat()
	mc.startLockHeartbeat(heartbeatCtx, lock, key)

	defer func() {
		// Use a fresh context for unlock so that a cancelled or timed-out work
		// context does not prevent the lock row from being deleted.
		if err := lock.Unlock(context.Background()); err != nil {
			// Downgrade from Warn to Debug for the common race where TTL
			// expired and the lock was already cleaned up by another node;
			// the heartbeat should normally prevent this.
			if errors.Is(err, configstore.ErrLockNotHeld) {
				mc.logger.Debug("distributed lock %q already released (TTL expired or cleaned up): %v", key, err)
			} else {
				mc.logger.Warn("failed to release distributed lock %q: %v", key, err)
			}
		}
	}()
	return fn()
}

// peerSyncWaitTimeout caps how long a non-leader replica will wait for the
// leader's startup sync to populate the DB before giving up. It must be
// generously larger than syncLockTTL because the actual sync is what blocks
// us, not the lock TTL itself.
const peerSyncWaitTimeout = syncLockTTL + 2*time.Minute

// peerSyncPollInterval is how often we re-check the DB for data while waiting
// for the peer to finish its sync.
const peerSyncPollInterval = 3 * time.Second

// waitForPeerSyncAndReloadPricing polls the DB until pricing rows appear (the
// peer finished its sync) or the timeout elapses. On success it loads pricing
// into the in-memory cache and returns nil. Used only on the cold-start path
// where this replica has no DB data and lost the leader-election race.
func (mc *ModelCatalog) waitForPeerSyncAndReloadPricing(ctx context.Context) error {
	if mc.configStore == nil {
		return errors.New("no configStore available")
	}
	deadline := time.Now().Add(peerSyncWaitTimeout)
	for {
		// Reload from DB; if it now has rows, we're done.
		if err := mc.loadPricingFromDatabase(ctx); err != nil {
			return fmt.Errorf("failed to reload pricing from DB while waiting for peer: %w", err)
		}
		mc.mu.RLock()
		n := len(mc.pricingData)
		mc.mu.RUnlock()
		if n > 0 {
			mc.logger.Info("peer-driven startup pricing sync visible in DB (%d records); continuing", n)
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("timed out waiting for peer to populate pricing data")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(peerSyncPollInterval):
		}
	}
}

// waitForPeerSyncAndReloadParams is the model-parameters analogue of
// waitForPeerSyncAndReloadPricing.
func (mc *ModelCatalog) waitForPeerSyncAndReloadParams(ctx context.Context) error {
	if mc.configStore == nil {
		return errors.New("no configStore available")
	}
	deadline := time.Now().Add(peerSyncWaitTimeout)
	for {
		n, err := mc.loadModelParametersFromDatabase(ctx)
		if err != nil {
			return fmt.Errorf("failed to reload model parameters from DB while waiting for peer: %w", err)
		}
		if n > 0 {
			mc.logger.Info("peer-driven startup model parameters sync visible in DB (%d records); continuing", n)
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("timed out waiting for peer to populate model parameters")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(peerSyncPollInterval):
		}
	}
}

// startLockHeartbeat launches a goroutine that periodically calls Extend on the
// lock to push its expires_at forward. The goroutine exits when ctx is cancelled
// (which happens when the calling helper returns). If Extend reports
// ErrLockNotHeld, the lock has been lost (e.g., TTL expired earlier and another
// node grabbed it) — we log loudly because this is the failure mode that
// previously caused the deadlock.
func (mc *ModelCatalog) startLockHeartbeat(ctx context.Context, lock *configstore.DistributedLock, key string) {
	mc.wg.Add(1)
	go func() {
		defer mc.wg.Done()
		ticker := time.NewTicker(syncLockHeartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				// Extend uses the lock's own holderID, so the holder identity
				// stays stable across the entire sync — no more "lock not held
				// by this holder" warnings caused by holder mismatch.
				extendCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				err := lock.Extend(extendCtx)
				cancel()
				if err == nil {
					continue
				}
				if errors.Is(err, configstore.ErrLockNotHeld) {
					mc.logger.Error("distributed lock %q lost mid-sync (heartbeat Extend reported not-held); another node may now be running the same sync — investigate sync duration vs syncLockTTL=%v", key, syncLockTTL)
					return
				}
				mc.logger.Warn("distributed lock %q heartbeat extend failed (transient, will retry): %v", key, err)
			}
		}
	}()
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
		// Periodic sync uses skip-if-held: if another replica is already
		// running this sync, the work is redundant — every node would write
		// the same UPSERT batch. Skipping prevents two replicas from racing
		// concurrent UPSERTs (the same race that produced the original
		// startup deadlock). Whichever node acquires the lock first does the
		// work; others reload from the DB on the next tick (via
		// loadPricingFromDatabase / loadModelParametersFromDatabase elsewhere).
		if err := mc.withDistributedLockSkipIfHeld(ctx, "model_catalog_pricing_sync", func() error {
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
			if errors.Is(err, errLockHeldByPeer) {
				mc.logger.Debug("background sync skipped: peer is currently running it")
			} else {
				mc.logger.Error("failed to run model catalog sync: %v", err)
			}
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
		if err := json.Unmarshal(rawData, &p); err == nil && (p.MaxOutputTokens != nil || parsed.VertexMultiRegionOnly != nil) {
			modelParamsEntries[model] = providerUtils.ModelParams{
				MaxOutputTokens:        p.MaxOutputTokens,
				IsVertexMultiRegionOnly: parsed.VertexMultiRegionOnly,
			}
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