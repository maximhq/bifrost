package modelcatalog

import (
	"context"
	"slices"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
)

// modelTagsIndex maps provider -> model name -> tags. It is replaced as a whole on every reload
// and never mutated after it is published, so readers need no lock.
type modelTagsIndex map[schemas.ModelProvider]map[string][]string

// ReloadModelTags re-reads operator-assigned model tags from the config store into memory.
// Without a config store there are no model tags and this is a no-op.
//
// The catalog owns the overlay because it is already the per-model enrichment layer for both
// the management model listings and /v1/models, and because ReloadFromDB, which enterprise
// peers run when the model catalog is reloaded cluster-wide, refreshes it too.
//
// Reloads are serialized from the store read through the publish: without that, two reloads
// racing after two tag writes could publish in reverse order, leaving the older snapshot live
// until the next reload. A reload that starts after a write commits therefore always publishes
// a snapshot at least as new as that write.
func (mc *ModelCatalog) ReloadModelTags(ctx context.Context) error {
	if mc == nil || mc.configStore == nil {
		return nil
	}
	mc.modelTagsReloadMu.Lock()
	defer mc.modelTagsReloadMu.Unlock()
	stored, err := mc.configStore.GetModelTags(ctx)
	if err != nil {
		// Recorded under the reload lock, so a concurrent successful reload cannot be undone by
		// a stale mark from an older failure.
		mc.modelTagsStale.Store(true)
		return err
	}
	index := make(modelTagsIndex, len(stored))
	for provider, models := range stored {
		index[schemas.ModelProvider(provider)] = models
	}
	mc.modelTags.Store(&index)
	mc.modelTagsStale.Store(false)
	return nil
}

// ModelTagsStale reports whether the last overlay reload failed, so the overlay may not reflect
// every committed tag write yet. syncTick keeps retrying until a reload succeeds.
func (mc *ModelCatalog) ModelTagsStale() bool {
	return mc != nil && mc.modelTagsStale.Load()
}

// retryModelTagsIfNeeded reloads the tag overlay when it never loaded (the startup load and its
// retries all failed) or when the last reload failed (for example after a tag write whose reload
// and background retries all failed). Called on every sync tick, so a stale overlay is retried
// until the config store answers instead of being given up after a fixed number of attempts.
func (mc *ModelCatalog) retryModelTagsIfNeeded(ctx context.Context) {
	if mc == nil || mc.configStore == nil {
		return
	}
	if mc.modelTags.Load() != nil && !mc.modelTagsStale.Load() {
		return
	}
	if err := mc.ReloadModelTags(ctx); err != nil && mc.logger != nil {
		mc.logger.Warn("model tags overlay is still not up to date: %v", err)
	}
}

// GetModelTags returns the operator-assigned tags of a model, or nil when it has none. The
// returned slice is a copy owned by the caller.
func (mc *ModelCatalog) GetModelTags(provider schemas.ModelProvider, model string) []string {
	if mc == nil {
		return nil
	}
	index := mc.modelTags.Load()
	if index == nil {
		return nil
	}
	return slices.Clone((*index)[provider][model])
}

// ApplyModelTags sets model.Tags from the overlay, looking the model up by the name in its
// "provider/model" ID and then by its alias. Tags are owned by the gateway: whatever the field
// held before (for example something decoded from an upstream list-models body) is replaced.
func (mc *ModelCatalog) ApplyModelTags(model *schemas.Model) {
	if model == nil {
		return
	}
	provider, name := schemas.ParseModelString(model.ID, "")
	tags := mc.GetModelTags(provider, name)
	if tags == nil && model.Alias != nil {
		tags = mc.GetModelTags(provider, *model.Alias)
	}
	model.Tags = tags
}

// NewTestCatalogWithConfigStore is NewTestCatalog backed by a config store, so tests in other
// packages can exercise ReloadModelTags without a full Init (which syncs pricing over the
// network).
func NewTestCatalogWithConfigStore(store configstore.ConfigStore) *ModelCatalog {
	mc := NewTestCatalog(nil)
	mc.configStore = store
	return mc
}

// modelTagsStartupRetryDelays spaces the background retries of a failed startup load of the tag
// overlay. After the last one, syncTick keeps retrying on its hourly tick while the overlay is
// still unloaded.
var modelTagsStartupRetryDelays = []time.Duration{5 * time.Second, 15 * time.Second, 30 * time.Second, time.Minute, 2 * time.Minute}

// loadModelTagsAtStartup loads the tag overlay. Tags are labels, not pricing, so a failed load
// does not fail startup; it is retried in the background after each of delays instead, so a
// config store that is briefly unavailable at startup does not leave every listing without tags
// (and tags= filters matching nothing) until the next tag write or cluster reload. The retries
// stop on success, once another path (a tag write or a cluster reload) has published an overlay,
// or when the catalog shuts down.
func (mc *ModelCatalog) loadModelTagsAtStartup(ctx context.Context, delays []time.Duration) {
	err := mc.ReloadModelTags(ctx)
	if err == nil {
		return
	}
	if mc.logger != nil {
		mc.logger.Warn("failed to load model tags (retrying in background): %v", err)
	}
	mc.wg.Add(1)
	go func() {
		defer mc.wg.Done()
		for _, delay := range delays {
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-mc.done:
				timer.Stop()
				return
			case <-timer.C:
			}
			if mc.modelTags.Load() != nil {
				return
			}
			if err := mc.ReloadModelTags(ctx); err == nil {
				return
			} else if mc.logger != nil {
				mc.logger.Warn("retrying model tags load failed: %v", err)
			}
		}
	}()
}
