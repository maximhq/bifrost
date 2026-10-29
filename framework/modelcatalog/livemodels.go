package modelcatalog

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/modelcatalog/live"
)

// SetAfterLiveModelsSyncHook supplies a scoped notification for usable database-backed listings.
// A retained DB snapshot may trigger it too; notification is not proof of a fresh upstream fetch.
func (mc *ModelCatalog) SetAfterLiveModelsSyncHook(fn func(context.Context, schemas.ModelProvider, string)) {
	mc.syncMu.Lock()
	mc.afterLiveSyncHook = fn
	mc.syncMu.Unlock()
}

// NotifyLiveModelsSynced runs the notification after the caller has applied discovery statuses.
// Call only after a commit or a usable DB fallback, never for a memory-only result or a skipped pass.
func (mc *ModelCatalog) NotifyLiveModelsSynced(ctx context.Context, provider schemas.ModelProvider, keyID string) {
	mc.syncMu.RLock()
	hook := mc.afterLiveSyncHook
	mc.syncMu.RUnlock()
	if hook != nil {
		hook(ctx, provider, keyID)
	}
}

// HasLiveModelPersistence reports whether this catalog can share durable listings.
func (mc *ModelCatalog) HasLiveModelPersistence() bool {
	_, ok := mc.configStore.(configstore.LiveModelListingStore)
	return ok
}

// PruneLiveModels removes retired persisted scopes; a replay cannot delete a newer compatible snapshot.
func (mc *ModelCatalog) PruneLiveModels(ctx context.Context, provider schemas.ModelProvider) error {
	if store, ok := mc.configStore.(configstore.LiveModelListingStore); ok {
		return store.PruneLiveModelListings(ctx, provider)
	}
	return nil
}

// ReloadLiveModelsFromDB applies a provider or key snapshot belonging to configured, enabled keys.
// Capture generation before the configuration snapshot. Data writes retry, but a changed
// configuration discards the reload; failed reads/decodes also keep the existing cache.
func (mc *ModelCatalog) ReloadLiveModelsFromDB(ctx context.Context, provider schemas.ModelProvider, config *configstore.ProviderConfig, generation uint64, keyIDs ...string) error {
	store, ok := mc.configStore.(configstore.LiveModelListingStore)
	if !ok || config == nil {
		return nil
	}
	var keyID *string
	if len(keyIDs) > 0 {
		keyID = &keyIDs[0]
	}
	hashes := make(map[string]string)
	keyless := config.CustomProviderConfig != nil && config.CustomProviderConfig.IsKeyLess
	if keyless {
		hash, err := configstore.LiveModelSourceHash(provider, config, schemas.Key{})
		if err != nil {
			return err
		}
		hashes[""] = hash
	} else {
		for _, key := range config.Keys {
			if keyID != nil && key.ID != *keyID {
				continue
			}
			if key.Enabled != nil && !*key.Enabled {
				continue
			}
			hash, err := configstore.LiveModelSourceHash(provider, config, key)
			if err != nil {
				return err
			}
			hashes[key.ID] = hash
		}
	}
	for attempt := 0; attempt < 3; attempt++ {
		version := mc.live.Version(provider)
		listings, err := store.GetLiveModelListings(ctx, provider, keyID)
		if err != nil {
			return err
		}
		if mc.live.Generation(provider) != generation {
			return nil // The supplied configuration is stale; keep the newer local cache.
		}
		entries := make(map[live.Key]live.Entry, len(listings))
		for _, listing := range listings {
			if hash, configured := hashes[listing.KeyID]; !configured || hash != listing.SourceHash {
				continue
			}
			var entry live.Entry
			if err := sonic.UnmarshalString(listing.Data, &entry); err != nil {
				return fmt.Errorf("failed to decode live models for %s key %s: %w", provider, listing.KeyID, err)
			}
			if entry.Models == nil {
				return fmt.Errorf("missing live models for %s key %s", provider, listing.KeyID)
			}
			entries[live.Key{Provider: provider, KeyID: listing.KeyID, Unfiltered: listing.Unfiltered}] = entry
		}
		if mc.live.ReplaceIfCurrent(provider, keyID, entries, version) {
			return nil
		}
	}
	return nil // A newer local write wins; the next notification or scheduled reload retries.
}

// SyncLiveModelsForKey preserves independent filtered/unfiltered success and last-known-good data.
// fetch owns provider-specific request construction; all DB work uses the parent, not its fetch deadline.
// The returned bool means peers can reload usable DB data, not necessarily that upstream discovery succeeded.
func (mc *ModelCatalog) SyncLiveModelsForKey(ctx context.Context, expected configstore.LiveModelSource,
	fetch func(context.Context, bool) (*schemas.BifrostListModelsResponse, *schemas.BifrostError),
) ([]schemas.KeyStatus, bool, error) {
	if !mc.ShouldSync(ctx) {
		return nil, false, nil
	}
	gen := mc.live.Generation(expected.Provider)
	store, persistent := mc.configStore.(configstore.LiveModelListingStore)
	if persistent {
		// One lease covers both modes. Prefix the provider length so scope names cannot collide.
		held, release, err := mc.distributedLockManager.Acquire(ctx,
			fmt.Sprintf("live_models:%d:%s:%s", len(expected.Provider), expected.Provider, expected.KeyID))
		if err != nil {
			return nil, false, err
		}
		defer release()
		ctx = held
		if !mc.ShouldSync(ctx) {
			return nil, false, nil
		}
		source, err := store.GetLiveModelSource(ctx, expected.Provider, expected.KeyID)
		if errors.Is(err, configstore.ErrNotFound) {
			return nil, false, nil
		}
		if err != nil {
			return nil, false, err
		}
		if source.Hash != expected.Hash || source.ProviderHash != expected.ProviderHash {
			return nil, false, nil
		}
		expected = *source
	}
	type result struct {
		statuses    []schemas.KeyStatus
		dbAvailable bool
		failed      bool
		err         error
	}
	var results [2]result
	var wg sync.WaitGroup
	for mode := range results {
		wg.Add(1)
		go func(mode int) {
			defer wg.Done()
			unfiltered := mode == 1
			resp, bfErr := fetch(ctx, unfiltered)
			if ctx.Err() != nil {
				results[mode].err = ctx.Err()
				return
			}
			if mc.live.Generation(expected.Provider) != gen || !mc.ShouldSync(ctx) {
				return
			}
			if bfErr != nil {
				results[mode].failed = true
				if !unfiltered {
					results[mode].statuses = bfErr.ExtraFields.KeyStatuses
				}
				return
			}
			if resp == nil {
				return
			}
			if resp.NextPageToken != "" || (resp.HasMore != nil && *resp.HasMore) {
				results[mode].failed = true
				if mc.logger != nil {
					mc.logger.Warn("incomplete list-models for provider %s key %s (unfiltered=%t); retaining last-known-good data", expected.Provider, expected.KeyID, unfiltered)
				}
				if !unfiltered {
					results[mode].statuses = []schemas.KeyStatus{{Provider: expected.Provider, KeyID: expected.KeyID,
						Status: schemas.KeyStatusListModelsFailed, Error: &schemas.BifrostError{Error: &schemas.ErrorField{Message: "provider returned an incomplete model listing"}}}}
				}
				return
			}
			for _, status := range resp.KeyStatuses {
				if status.Status != schemas.KeyStatusSuccess {
					results[mode].failed = true
					if !unfiltered {
						results[mode].statuses = resp.KeyStatuses
					}
					return
				}
			}
			models, created := extractModelIDs(resp, expected.Provider)
			if persistent {
				data, err := providerUtils.MarshalSorted(live.Entry{Models: models, Created: created})
				if err != nil {
					results[mode].err = err
					return
				}
				if !mc.ShouldSync(ctx) {
					return
				}
				stored, err := store.UpsertLiveModelListing(ctx, expected, unfiltered, string(data))
				if err != nil {
					results[mode].err = err
					return
				}
				if !stored {
					return
				}
				results[mode].dbAvailable = true
			}
			if mc.live.UpsertIfCurrent(expected.Provider, expected.KeyID, unfiltered, models, created, gen) && !unfiltered {
				results[mode].statuses = resp.KeyStatuses
			}
		}(mode)
	}
	wg.Wait()
	dbAvailable := results[0].dbAvailable || results[1].dbAvailable
	err := errors.Join(results[0].err, results[1].err)
	if persistent && (results[0].failed || results[1].failed) && mc.live.Generation(expected.Provider) == gen {
		listings, loadErr := store.GetLiveModelListings(ctx, expected.Provider, &expected.KeyID)
		if loadErr != nil {
			return results[0].statuses, dbAvailable, errors.Join(err, loadErr)
		}
		for _, listing := range listings {
			if listing.SourceHash != expected.Hash {
				continue
			}
			var entry live.Entry
			if decodeErr := sonic.UnmarshalString(listing.Data, &entry); decodeErr != nil {
				err = errors.Join(err, decodeErr)
				continue
			}
			if entry.Models == nil {
				err = errors.Join(err, fmt.Errorf("missing live models for %s key %s", expected.Provider, expected.KeyID))
				continue
			}
			if mc.live.UpsertIfCurrent(expected.Provider, expected.KeyID, listing.Unfiltered, entry.Models, entry.Created, gen) {
				dbAvailable = true
			}
		}
	}
	return results[0].statuses, dbAvailable, err
}
