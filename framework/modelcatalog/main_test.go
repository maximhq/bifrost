package modelcatalog

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
)

// newTestCatalog creates a minimal ModelCatalog for testing within the package.
func newTestCatalog(modelPool map[schemas.ModelProvider][]string, baseModelIndex map[string]string) *ModelCatalog {
	if modelPool == nil {
		modelPool = make(map[schemas.ModelProvider][]string)
	}
	if baseModelIndex == nil {
		baseModelIndex = make(map[string]string)
	}
	return &ModelCatalog{
		modelPool:                modelPool,
		baseModelIndex:           baseModelIndex,
		pricingData:              make(map[string]configstoreTables.TableModelPricing),
		rawPricingOverrides:      make(map[string]configstoreTables.TablePricingOverride),
		compiledPricingOverrides: make(map[string][]compiledPricingOverride),
	}
}

// --- GetBaseModelName tests ---

func TestGetBaseModelName_Simple(t *testing.T) {
	mc := newTestCatalog(nil, nil)
	// No catalog data, no prefix — returns as-is (no date suffix to strip either)
	assert.Equal(t, "gpt-4o", mc.GetBaseModelName("gpt-4o"))
}

func TestGetBaseModelName_Prefixed(t *testing.T) {
	mc := newTestCatalog(nil, nil)
	// Provider prefix stripped, no catalog — algorithmic fallback returns base
	assert.Equal(t, "gpt-4o", mc.GetBaseModelName("openai/gpt-4o"))
}

func TestGetBaseModelName_PrefixedAnthropic(t *testing.T) {
	mc := newTestCatalog(nil, nil)
	assert.Equal(t, "claude-3-5-sonnet", mc.GetBaseModelName("anthropic/claude-3-5-sonnet"))
}

func TestGetBaseModelName_FromCatalog(t *testing.T) {
	// Model has a pre-computed base_model in the catalog
	mc := newTestCatalog(nil, map[string]string{
		"gpt-4o":            "gpt-4o",
		"gpt-4o-2024-08-06": "gpt-4o",
	})
	assert.Equal(t, "gpt-4o", mc.GetBaseModelName("gpt-4o"))
	assert.Equal(t, "gpt-4o", mc.GetBaseModelName("gpt-4o-2024-08-06"))
}

func TestGetBaseModelName_ProviderPrefixWithCatalog(t *testing.T) {
	// Model has provider prefix — strip prefix, then find in catalog
	mc := newTestCatalog(nil, map[string]string{
		"gpt-4o": "gpt-4o",
	})
	assert.Equal(t, "gpt-4o", mc.GetBaseModelName("openai/gpt-4o"))
}

func TestGetBaseModelName_FallbackAlgorithmic(t *testing.T) {
	// Model NOT in catalog — falls back to schemas.BaseModelName (date stripping)
	mc := newTestCatalog(nil, nil)
	// Anthropic-style date suffix
	assert.Equal(t, "claude-sonnet-4", mc.GetBaseModelName("claude-sonnet-4-20250514"))
	// OpenAI-style date suffix
	assert.Equal(t, "gpt-4o", mc.GetBaseModelName("gpt-4o-2024-08-06"))
}

func TestGetBaseModelName_FallbackAlgorithmicWithPrefix(t *testing.T) {
	// Provider prefix + not in catalog — strip prefix, then algorithmic fallback
	mc := newTestCatalog(nil, nil)
	assert.Equal(t, "claude-sonnet-4", mc.GetBaseModelName("anthropic/claude-sonnet-4-20250514"))
}

func TestGetBaseModelName_UnknownModel(t *testing.T) {
	mc := newTestCatalog(nil, nil)
	assert.Equal(t, "some-random-model", mc.GetBaseModelName("some-random-model"))
}

func TestGetBaseModelName_CatalogTakesPrecedence(t *testing.T) {
	// If catalog says the base_model is X, use it even if algorithmic would give Y
	mc := newTestCatalog(nil, map[string]string{
		"my-custom-model-20250101": "my-custom-model-20250101", // catalog says keep the date
	})
	assert.Equal(t, "my-custom-model-20250101", mc.GetBaseModelName("my-custom-model-20250101"))
}

// --- IsSameModel tests ---

func TestIsSameModel_DirectMatch(t *testing.T) {
	mc := newTestCatalog(nil, nil)
	assert.True(t, mc.IsSameModel("gpt-4o", "gpt-4o"))
}

func TestIsSameModel_ProviderPrefix(t *testing.T) {
	mc := newTestCatalog(nil, nil)
	assert.True(t, mc.IsSameModel("openai/gpt-4o", "gpt-4o"))
	assert.True(t, mc.IsSameModel("gpt-4o", "openai/gpt-4o"))
}

func TestIsSameModel_BothPrefixed(t *testing.T) {
	mc := newTestCatalog(nil, nil)
	assert.True(t, mc.IsSameModel("openai/gpt-4o", "openai/gpt-4o"))
}

func TestIsSameModel_DifferentProvidersSameBase(t *testing.T) {
	mc := newTestCatalog(nil, nil)
	// Both have the same base model after stripping different provider prefixes
	assert.True(t, mc.IsSameModel("openai/gpt-4o", "azure/gpt-4o"))
}

func TestIsSameModel_DifferentModels(t *testing.T) {
	mc := newTestCatalog(nil, nil)
	assert.False(t, mc.IsSameModel("gpt-4o", "claude-3-5-sonnet"))
}

func TestIsSameModel_DifferentModelsBothPrefixed(t *testing.T) {
	mc := newTestCatalog(nil, nil)
	assert.False(t, mc.IsSameModel("openai/gpt-4o", "anthropic/claude-3-5-sonnet"))
}

func TestIsSameModel_CatalogBacked(t *testing.T) {
	// Two model strings that look different but the catalog says they have the same base_model
	mc := newTestCatalog(nil, map[string]string{
		"claude-3-5-sonnet":          "claude-3-5-sonnet",
		"claude-3-5-sonnet-20241022": "claude-3-5-sonnet",
	})
	assert.True(t, mc.IsSameModel("claude-3-5-sonnet", "claude-3-5-sonnet-20241022"))
	assert.True(t, mc.IsSameModel("claude-3-5-sonnet-20241022", "claude-3-5-sonnet"))
}

func TestIsSameModel_AlgorithmicFallback(t *testing.T) {
	// Models not in catalog — use algorithmic date stripping
	mc := newTestCatalog(nil, nil)
	assert.True(t, mc.IsSameModel("custom-model-20250101", "custom-model"))
}

func TestIsSameModel_EmptyStrings(t *testing.T) {
	mc := newTestCatalog(nil, nil)
	assert.True(t, mc.IsSameModel("", ""))
	assert.False(t, mc.IsSameModel("gpt-4o", ""))
	assert.False(t, mc.IsSameModel("", "gpt-4o"))
}
