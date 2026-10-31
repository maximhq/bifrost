package modelcatalog

import (
	"context"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/lrucache"
)

// datasheetCapabilityLoader reads one capability record out of the datasheet.
// Installed at Init so the cache has a single load path and tests can substitute
// one without a config store.
func (mc *ModelCatalog) datasheetCapabilityLoader(provider schemas.ModelProvider, model string) (*schemas.ModelCapabilities, error) {
	ctx, cancel := context.WithTimeout(context.Background(), capabilityLoadTimeout)
	defer cancel()
	return mc.datasheet.LoadModelCapabilities(ctx, provider, model)
}

// capabilityCacheSize bounds the resident capability set. A deployment's
// working set is a few dozen models, so the bound is generous; anything
// evicted is reloaded on next use.
const capabilityCacheSize = 2048

// capabilityLoadTimeout caps a single datasheet read. Reached only on a cold
// key, and single-flighted, so at most one caller pays it per model.
const capabilityLoadTimeout = 3 * time.Second

// GetModelCapabilities returns the datasheet's capability record for a
// (provider, model) pair, or nil when the catalog has none.
//
// The cache is keyed by exactly what the caller asked for; translating that to
// whatever the datasheet calls the row is LoadModelCapabilities' job. A miss
// loads once — concurrent callers share one load — and the result is cached
// either way, so a model with no record is looked up once rather than on every
// capability check a request makes.
//
// Records are dropped wholesale when a sync applies a new sheet, so a record
// can outlive its source by at most one sync.
func (mc *ModelCatalog) GetModelCapabilities(provider schemas.ModelProvider, model string) *schemas.ModelCapabilities {
	if mc == nil || model == "" {
		return nil
	}
	key := lrucache.EncodeKey(string(provider), model)
	if caps, ok := mc.capabilities.Get(key); ok {
		return caps
	}
	if mc.loadCapabilities == nil {
		return nil
	}
	// Background is enough: the loader bounds itself with capabilityLoadTimeout,
	// so a follower waiting on the in-flight load can never outlast it.
	caps, err := mc.capabilities.Fill(context.Background(), key, func() (*schemas.ModelCapabilities, string, error) {
		record, err := mc.loadCapabilities(provider, model)
		return record, "", err
	})
	if err != nil {
		// Fill does not cache a failed load, so the next request retries rather
		// than serving "no capabilities" until the following sheet apply.
		if mc.logger != nil {
			mc.logger.Debug("capability load failed for %s/%s, falling back to name detection: %v", provider, model, err)
		}
		return nil
	}
	return caps
}
