package modelcatalog

import (
	"context"
	"slices"

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
		return err
	}
	index := make(modelTagsIndex, len(stored))
	for provider, models := range stored {
		index[schemas.ModelProvider(provider)] = models
	}
	mc.modelTags.Store(&index)
	return nil
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
