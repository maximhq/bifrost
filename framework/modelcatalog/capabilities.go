package modelcatalog

import (
	"context"
	"time"

	"github.com/maximhq/bifrost/framework/livestore"

	"github.com/maximhq/bifrost/core/schemas"
)

// datasheetCapabilityLoader reads one capability record out of the datasheet.
// Installed at Init so the cache has a single load path and tests can substitute
// one without a config store.
func (mc *ModelCatalog) datasheetCapabilityLoader(provider schemas.ModelProvider, model string) (*schemas.ModelCapabilities, error) {
	ctx, cancel := context.WithTimeout(context.Background(), capabilityLoadTimeout)
	defer cancel()
	return mc.datasheet.LoadModelCapabilities(ctx, provider, model)
}

// capabilityLoadTimeout caps a single datasheet read. Reached only on a cold
// key, and single-flighted, so at most one caller pays it per model.
const capabilityLoadTimeout = 3 * time.Second

// capabilityMemo is one cached capability answer.
type capabilityMemo struct {
	caps   *schemas.ModelCapabilities // the record, nil when the datasheet has none
	listed bool                       // the provider lists the requested name as written
}

// ttl keeps a record found for a listed name until a sheet apply flushes it, and every other
// answer, no record or a record reached through another spelling, for catalogNegativeTTL.
func (m capabilityMemo) ttl() time.Duration {
	if m.caps != nil && m.listed {
		return 0
	}
	return catalogNegativeTTL
}

// GetModelCapabilities returns the datasheet's capability record for a
// (provider, model) pair, or nil when the catalog has none.
//
// The cache is keyed by exactly what the caller asked for; translating that to
// whatever the datasheet calls the row is LoadModelCapabilities' job. A miss
// loads once, concurrent callers sharing one load, and the result is cached
// either way, so a model with no record is looked up once per
// catalogNegativeTTL rather than on every capability check a request makes. A
// record is kept until the next sheet apply only for a name the provider lists
// (see listedFor), and for catalogNegativeTTL under any other spelling.
//
// Records are dropped wholesale when a sync applies a new sheet, so a record
// can outlive its source by at most one sync.
func (mc *ModelCatalog) GetModelCapabilities(provider schemas.ModelProvider, model string) *schemas.ModelCapabilities {
	if mc == nil || model == "" {
		return nil
	}
	key := livestore.EncodeKey(string(provider), model)
	if memo, ok := mc.capabilities.Get(key); ok {
		return memo.caps
	}
	if mc.loadCapabilities == nil {
		return nil
	}
	// Background is enough: the loader bounds itself with capabilityLoadTimeout,
	// so a follower waiting on the in-flight load can never outlast it.
	memo, err := mc.capabilities.Fill(context.Background(), key, func() (capabilityMemo, string, error) {
		record, err := mc.loadCapabilities(provider, model)
		if err != nil {
			return capabilityMemo{}, "", err
		}
		return capabilityMemo{caps: record, listed: record != nil && mc.listedFor(provider, model)}, "", nil
	})
	if err != nil {
		// Fill does not cache a failed load, so the next request retries rather
		// than serving "no capabilities" until the following sheet apply.
		if mc.logger != nil {
			mc.logger.Debug("capability load failed for %s/%s, falling back to name detection: %v", provider, model, err)
		}
		return nil
	}
	return memo.caps
}
