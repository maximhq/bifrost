package modelcatalog

import (
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/modelcatalog/datasheet"
	"github.com/maximhq/bifrost/framework/modelcatalog/keyconfig"
	"github.com/maximhq/bifrost/framework/modelcatalog/live"
)

// emptyCatalog builds a ModelCatalog with empty (but valid) backing stores, so
// GetProvidersForModel resolves to "no provider serves this model" — enough to
// exercise the wildcard allow/deny logic without seeding a datasheet.
func emptyCatalog() *ModelCatalog {
	logger := bifrost.NewNoOpLogger()
	return &ModelCatalog{
		logger:    logger,
		datasheet: datasheet.New(nil, logger, datasheet.Config{}),
		live:      live.New(logger),
		keyconf:   keyconfig.New(logger),
	}
}

func customProviderConfig() *configstore.ProviderConfig {
	return &configstore.ProviderConfig{
		CustomProviderConfig: &schemas.CustomProviderConfig{},
	}
}

// TestIsModelAllowedForProvider_WildcardCustomProvider pins the patched behavior: a ["*"]
// allow-list permits ANY model on a custom provider, including one the catalog has never heard
// of (an empty catalog here) — fixing the spurious model_blocked 403 on internal models.
func TestIsModelAllowedForProvider_WildcardCustomProvider(t *testing.T) {
	mc := emptyCatalog()
	allowed := mc.IsModelAllowedForProvider(
		schemas.ModelProvider("my-custom"), "glm-4.6", customProviderConfig(), schemas.WhiteList{"*"})
	if !allowed {
		t.Fatal("wildcard on a custom provider must allow an uncatalogued model")
	}
}

// TestIsModelAllowedForProvider_WildcardNativeProviderUnserved pins the asymmetry: a wildcard on
// a NATIVE provider is still catalog-cross-checked, so a model no provider serves is denied (a
// wildcard must not spray a model to a provider that doesn't serve it).
func TestIsModelAllowedForProvider_WildcardNativeProviderUnserved(t *testing.T) {
	mc := emptyCatalog()
	allowed := mc.IsModelAllowedForProvider(
		schemas.OpenAI, "model-no-one-serves", nil, schemas.WhiteList{"*"})
	if allowed {
		t.Fatal("wildcard on a native provider must still be catalog-cross-checked (deny when unserved)")
	}
}

// TestIsModelAllowedForProvider_WildcardNativeProviderServed is the positive native case: a
// wildcard on a native provider that DOES serve the model (per the catalog) is allowed. Guards
// against a regression that denied all native wildcard requests.
func TestIsModelAllowedForProvider_WildcardNativeProviderServed(t *testing.T) {
	mc := emptyCatalog()
	mc.live.Upsert(schemas.OpenAI, "test-key", false, []string{"gpt-4o"})
	allowed := mc.IsModelAllowedForProvider(
		schemas.OpenAI, "gpt-4o", nil, schemas.WhiteList{"*"})
	if !allowed {
		t.Fatal("wildcard on a native provider that serves the model must be allowed")
	}
}

// TestIsModelAllowedForProvider_EmptyDenies pins deny-by-default: an empty allow-list denies
// regardless of provider type.
func TestIsModelAllowedForProvider_EmptyDenies(t *testing.T) {
	mc := emptyCatalog()
	if mc.IsModelAllowedForProvider(schemas.ModelProvider("my-custom"), "glm-4.6", customProviderConfig(), schemas.WhiteList{}) {
		t.Fatal("empty allow-list must deny on a custom provider")
	}
	if mc.IsModelAllowedForProvider(schemas.OpenAI, "gpt-4o", nil, schemas.WhiteList{}) {
		t.Fatal("empty allow-list must deny on a native provider")
	}
}

// TestIsModelAllowedForProvider_ExplicitMatch pins explicit allow-listing: a model named in the
// list is allowed (direct match) without needing the catalog.
func TestIsModelAllowedForProvider_ExplicitMatch(t *testing.T) {
	mc := emptyCatalog()
	if !mc.IsModelAllowedForProvider(schemas.ModelProvider("my-custom"), "glm-4.6", customProviderConfig(), schemas.WhiteList{"glm-4.6"}) {
		t.Fatal("explicitly allow-listed model must be allowed")
	}
	if mc.IsModelAllowedForProvider(schemas.ModelProvider("my-custom"), "other", customProviderConfig(), schemas.WhiteList{"glm-4.6"}) {
		t.Fatal("a model not in the explicit allow-list must be denied")
	}
}
