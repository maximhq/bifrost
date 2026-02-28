package modelcatalog

import (
	"fmt"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type noOpLogger struct{}

func (noOpLogger) Debug(string, ...any)                   {}
func (noOpLogger) Info(string, ...any)                    {}
func (noOpLogger) Warn(string, ...any)                    {}
func (noOpLogger) Error(string, ...any)                   {}
func (noOpLogger) Fatal(string, ...any)                   {}
func (noOpLogger) SetLevel(schemas.LogLevel)              {}
func (noOpLogger) SetOutputType(schemas.LoggerOutputType) {}
func (noOpLogger) LogHTTPRequest(schemas.LogLevel, string) schemas.LogEventBuilder {
	return schemas.NoopLogEvent
}

type providerOverrideCompat struct {
	ModelPattern      string
	MatchType         schemas.PricingOverrideMatchType
	RequestTypes      []schemas.RequestType
	InputCostPerToken *float64
	ID                string
	UpdatedAt         time.Time
}

func setProviderScopedOverrides(t *testing.T, mc *ModelCatalog, provider schemas.ModelProvider, overrides []providerOverrideCompat) error {
	t.Helper()
	scopeID := string(provider)
	compiled := make([]schemas.PricingOverride, 0, len(overrides))
	for i, override := range overrides {
		id := override.ID
		if id == "" {
			id = fmt.Sprintf("%s-override-%d", scopeID, i)
		}
		compiled = append(compiled, schemas.PricingOverride{
			ID:           id,
			ScopeKind:    schemas.PricingOverrideScopeKindProvider,
			ProviderID:   &scopeID,
			MatchType:    override.MatchType,
			Pattern:      override.ModelPattern,
			RequestTypes: override.RequestTypes,
			UpdatedAt:    override.UpdatedAt,
			Patch: schemas.PricingOverridePatch{
				InputCostPerToken: override.InputCostPerToken,
			},
		})
	}
	return mc.SetPricingOverrides(compiled)
}

func TestGetPricing_OverridePrecedenceExactWildcard(t *testing.T) {
	mc := newTestCatalog(nil, nil)
	mc.logger = noOpLogger{}
	mc.pricingData[makeKey("gpt-4o", "openai", "chat")] = configstoreTables.TableModelPricing{
		Model:              "gpt-4o",
		Provider:           "openai",
		Mode:               "chat",
		InputCostPerToken:  1,
		OutputCostPerToken: 2,
	}

	exact := 20.0
	wildcard := 10.0
	require.NoError(t, setProviderScopedOverrides(t, mc, schemas.OpenAI, []providerOverrideCompat{
		{
			ModelPattern:      "gpt-*",
			MatchType:         schemas.PricingOverrideMatchWildcard,
			InputCostPerToken: &wildcard,
		},
		{
			ModelPattern:      "gpt-4o",
			MatchType:         schemas.PricingOverrideMatchExact,
			InputCostPerToken: &exact,
		},
	}))

	pricing, ok := mc.getPricing("gpt-4o", "openai", schemas.ChatCompletionRequest)
	require.True(t, ok)
	require.NotNil(t, pricing)
	assert.Equal(t, 20.0, pricing.InputCostPerToken)
	assert.Equal(t, 2.0, pricing.OutputCostPerToken)
}

func TestGetPricing_RequestTypeSpecificOverrideBeatsGeneric(t *testing.T) {
	t.Skip()
	mc := newTestCatalog(nil, nil)
	mc.logger = noOpLogger{}
	mc.pricingData[makeKey("gpt-4o", "openai", "responses")] = configstoreTables.TableModelPricing{
		Model:              "gpt-4o",
		Provider:           "openai",
		Mode:               "responses",
		InputCostPerToken:  1,
		OutputCostPerToken: 2,
	}

	specific := 15.0
	generic := 9.0
	require.NoError(t, setProviderScopedOverrides(t, mc, schemas.OpenAI, []providerOverrideCompat{
		{
			ModelPattern:      "gpt-4o",
			MatchType:         schemas.PricingOverrideMatchExact,
			InputCostPerToken: &generic,
		},
		{
			ModelPattern:      "gpt-4o",
			MatchType:         schemas.PricingOverrideMatchExact,
			RequestTypes:      []schemas.RequestType{schemas.ResponsesRequest},
			InputCostPerToken: &specific,
		},
	}))

	pricing, ok := mc.getPricing("gpt-4o", "openai", schemas.ResponsesRequest)
	require.True(t, ok)
	require.NotNil(t, pricing)
	assert.Equal(t, 15.0, pricing.InputCostPerToken)
}

func TestGetPricing_AppliesOverrideAfterFallbackResolution(t *testing.T) {
	t.Skip()
	mc := newTestCatalog(nil, nil)
	mc.logger = noOpLogger{}
	mc.pricingData[makeKey("gpt-4o", "vertex", "chat")] = configstoreTables.TableModelPricing{
		Model:              "gpt-4o",
		Provider:           "vertex",
		Mode:               "chat",
		InputCostPerToken:  1,
		OutputCostPerToken: 2,
	}

	override := 7.0
	require.NoError(t, setProviderScopedOverrides(t, mc, schemas.Gemini, []providerOverrideCompat{
		{
			ModelPattern:      "gpt-4o",
			MatchType:         schemas.PricingOverrideMatchExact,
			InputCostPerToken: &override,
		},
	}))

	pricing, ok := mc.getPricing("gpt-4o", "gemini", schemas.ChatCompletionRequest)
	require.True(t, ok)
	require.NotNil(t, pricing)
	assert.Equal(t, 7.0, pricing.InputCostPerToken)
}

func TestGetPricing_DeploymentLookupUsesRequestedModelForOverrideMatching(t *testing.T) {
	mc := newTestCatalog(nil, nil)
	mc.logger = noOpLogger{}
	mc.pricingData[makeKey("dep-gpt4o", "openai", "chat")] = configstoreTables.TableModelPricing{
		Model:              "dep-gpt4o",
		Provider:           "openai",
		Mode:               "chat",
		InputCostPerToken:  1,
		OutputCostPerToken: 2,
	}

	override := 7.0
	providerID := string(schemas.OpenAI)
	require.NoError(t, mc.SetPricingOverrides([]schemas.PricingOverride{
		{
			ID:         "requested-model-override",
			ScopeKind:  schemas.PricingOverrideScopeKindProvider,
			ProviderID: &providerID,
			MatchType:  schemas.PricingOverrideMatchExact,
			Pattern:    "gpt-4o",
			Patch: schemas.PricingOverridePatch{
				InputCostPerToken: &override,
			},
		},
	}))

	pricing, ok := mc.getPricingWithScopesAndMatchModel(
		"dep-gpt4o",
		"gpt-4o",
		"openai",
		schemas.ChatCompletionRequest,
		PricingLookupScopes{ProviderID: "openai"},
	)
	require.True(t, ok)
	require.NotNil(t, pricing)
	assert.Equal(t, 7.0, pricing.InputCostPerToken)
}

func TestGetPricing_FallbackUsesRequestedProviderForScopeMatching(t *testing.T) {
	mc := newTestCatalog(nil, nil)
	mc.logger = noOpLogger{}
	mc.pricingData[makeKey("gpt-4o", "vertex", "chat")] = configstoreTables.TableModelPricing{
		Model:              "gpt-4o",
		Provider:           "vertex",
		Mode:               "chat",
		InputCostPerToken:  1,
		OutputCostPerToken: 2,
	}

	geminiProviderID := string(schemas.Gemini)
	vertexProviderID := string(schemas.Vertex)
	geminiOverrideCost := 5.0
	vertexOverrideCost := 9.0
	require.NoError(t, mc.SetPricingOverrides([]schemas.PricingOverride{
		{
			ID:         "gemini-provider-override",
			ScopeKind:  schemas.PricingOverrideScopeKindProvider,
			ProviderID: &geminiProviderID,
			MatchType:  schemas.PricingOverrideMatchExact,
			Pattern:    "gpt-4o",
			Patch: schemas.PricingOverridePatch{
				InputCostPerToken: &geminiOverrideCost,
			},
		},
		{
			ID:         "vertex-provider-override",
			ScopeKind:  schemas.PricingOverrideScopeKindProvider,
			ProviderID: &vertexProviderID,
			MatchType:  schemas.PricingOverrideMatchExact,
			Pattern:    "gpt-4o",
			Patch: schemas.PricingOverridePatch{
				InputCostPerToken: &vertexOverrideCost,
			},
		},
	}))

	pricing, ok := mc.getPricing("gpt-4o", "gemini", schemas.ChatCompletionRequest)
	require.True(t, ok)
	require.NotNil(t, pricing)
	assert.Equal(t, 5.0, pricing.InputCostPerToken)
}

func TestGetPricing_ExactOverrideDoesNotMatchProviderPrefixedModel(t *testing.T) {
	t.Skip()
	mc := newTestCatalog(nil, nil)
	mc.logger = noOpLogger{}
	mc.pricingData[makeKey("openai/gpt-4o", "openai", "chat")] = configstoreTables.TableModelPricing{
		Model:              "openai/gpt-4o",
		Provider:           "openai",
		Mode:               "chat",
		InputCostPerToken:  1,
		OutputCostPerToken: 2,
	}

	override := 19.0
	require.NoError(t, setProviderScopedOverrides(t, mc, schemas.OpenAI, []providerOverrideCompat{
		{
			ModelPattern:      "gpt-4o",
			MatchType:         schemas.PricingOverrideMatchExact,
			InputCostPerToken: &override,
		},
	}))

	pricing, ok := mc.getPricing("openai/gpt-4o", "openai", schemas.ChatCompletionRequest)
	require.True(t, ok)
	require.NotNil(t, pricing)
	assert.Equal(t, 1.0, pricing.InputCostPerToken)
}

func TestGetPricing_NoMatchingOverrideLeavesPricingUnchanged(t *testing.T) {
	t.Skip()
	mc := newTestCatalog(nil, nil)
	mc.logger = noOpLogger{}
	baseCacheRead := 0.4
	mc.pricingData[makeKey("gpt-4o", "openai", "chat")] = configstoreTables.TableModelPricing{
		Model:                   "gpt-4o",
		Provider:                "openai",
		Mode:                    "chat",
		InputCostPerToken:       1,
		OutputCostPerToken:      2,
		CacheReadInputTokenCost: &baseCacheRead,
	}

	override := 9.0
	require.NoError(t, setProviderScopedOverrides(t, mc, schemas.OpenAI, []providerOverrideCompat{
		{
			ModelPattern:      "claude-*",
			MatchType:         schemas.PricingOverrideMatchWildcard,
			InputCostPerToken: &override,
		},
	}))

	pricing, ok := mc.getPricing("gpt-4o", "openai", schemas.ChatCompletionRequest)
	require.True(t, ok)
	require.NotNil(t, pricing)
	assert.Equal(t, 1.0, pricing.InputCostPerToken)
	assert.Equal(t, 2.0, pricing.OutputCostPerToken)
	require.NotNil(t, pricing.CacheReadInputTokenCost)
	assert.Equal(t, 0.4, *pricing.CacheReadInputTokenCost)
}

func TestDeleteProviderPricingOverrides_StopsApplying(t *testing.T) {
	t.Skip()
	mc := newTestCatalog(nil, nil)
	mc.logger = noOpLogger{}
	mc.pricingData[makeKey("gpt-4o", "openai", "chat")] = configstoreTables.TableModelPricing{
		Model:              "gpt-4o",
		Provider:           "openai",
		Mode:               "chat",
		InputCostPerToken:  1,
		OutputCostPerToken: 2,
	}

	override := 11.0
	require.NoError(t, setProviderScopedOverrides(t, mc, schemas.OpenAI, []providerOverrideCompat{
		{
			ModelPattern:      "gpt-4o",
			MatchType:         schemas.PricingOverrideMatchExact,
			InputCostPerToken: &override,
		},
	}))

	pricing, ok := mc.getPricing("gpt-4o", "openai", schemas.ChatCompletionRequest)
	require.True(t, ok)
	require.NotNil(t, pricing)
	assert.Equal(t, 11.0, pricing.InputCostPerToken)

	require.NoError(t, mc.SetPricingOverrides(nil))

	pricing, ok = mc.getPricing("gpt-4o", "openai", schemas.ChatCompletionRequest)
	require.True(t, ok)
	require.NotNil(t, pricing)
	assert.Equal(t, 1.0, pricing.InputCostPerToken)
}

func TestGetPricing_WildcardSpecificityLongerLiteralWins(t *testing.T) {
	t.Skip()
	mc := newTestCatalog(nil, nil)
	mc.logger = noOpLogger{}
	mc.pricingData[makeKey("gpt-4o-mini", "openai", "chat")] = configstoreTables.TableModelPricing{
		Model:              "gpt-4o-mini",
		Provider:           "openai",
		Mode:               "chat",
		InputCostPerToken:  1,
		OutputCostPerToken: 2,
	}

	generic := 5.0
	specific := 6.0
	require.NoError(t, setProviderScopedOverrides(t, mc, schemas.OpenAI, []providerOverrideCompat{
		{
			ModelPattern:      "gpt-*",
			MatchType:         schemas.PricingOverrideMatchWildcard,
			InputCostPerToken: &generic,
		},
		{
			ModelPattern:      "gpt-4o*",
			MatchType:         schemas.PricingOverrideMatchWildcard,
			InputCostPerToken: &specific,
		},
	}))

	pricing, ok := mc.getPricing("gpt-4o-mini", "openai", schemas.ChatCompletionRequest)
	require.True(t, ok)
	require.NotNil(t, pricing)
	assert.Equal(t, 6.0, pricing.InputCostPerToken)
}

func TestGetPricing_TieBreakLatestUpdatedAtWins(t *testing.T) {
	mc := newTestCatalog(nil, nil)
	mc.logger = noOpLogger{}
	mc.pricingData[makeKey("gpt-4o-mini", "openai", "chat")] = configstoreTables.TableModelPricing{
		Model:              "gpt-4o-mini",
		Provider:           "openai",
		Mode:               "chat",
		InputCostPerToken:  1,
		OutputCostPerToken: 2,
	}

	first := 8.0
	second := 9.0
	now := time.Now().UTC()
	require.NoError(t, setProviderScopedOverrides(t, mc, schemas.OpenAI, []providerOverrideCompat{
		{
			ModelPattern:      "gpt-4o*",
			MatchType:         schemas.PricingOverrideMatchWildcard,
			InputCostPerToken: &first,
			ID:                "older",
			UpdatedAt:         now.Add(-1 * time.Minute),
		},
		{
			ModelPattern:      "gpt-4o*",
			MatchType:         schemas.PricingOverrideMatchWildcard,
			InputCostPerToken: &second,
			ID:                "newer",
			UpdatedAt:         now,
		},
	}))

	pricing, ok := mc.getPricing("gpt-4o-mini", "openai", schemas.ChatCompletionRequest)
	require.True(t, ok)
	require.NotNil(t, pricing)
	assert.Equal(t, 9.0, pricing.InputCostPerToken)
}

func TestGetPricing_TieBreakIDWinsWhenUpdatedAtEqual(t *testing.T) {
	mc := newTestCatalog(nil, nil)
	mc.logger = noOpLogger{}
	mc.pricingData[makeKey("gpt-4o-mini", "openai", "chat")] = configstoreTables.TableModelPricing{
		Model:              "gpt-4o-mini",
		Provider:           "openai",
		Mode:               "chat",
		InputCostPerToken:  1,
		OutputCostPerToken: 2,
	}

	first := 8.0
	second := 9.0
	now := time.Now().UTC()
	require.NoError(t, setProviderScopedOverrides(t, mc, schemas.OpenAI, []providerOverrideCompat{
		{
			ModelPattern:      "gpt-4o*",
			MatchType:         schemas.PricingOverrideMatchWildcard,
			InputCostPerToken: &first,
			ID:                "a-override",
			UpdatedAt:         now,
		},
		{
			ModelPattern:      "gpt-4o*",
			MatchType:         schemas.PricingOverrideMatchWildcard,
			InputCostPerToken: &second,
			ID:                "b-override",
			UpdatedAt:         now,
		},
	}))

	pricing, ok := mc.getPricing("gpt-4o-mini", "openai", schemas.ChatCompletionRequest)
	require.True(t, ok)
	require.NotNil(t, pricing)
	assert.Equal(t, 8.0, pricing.InputCostPerToken)
}

func TestPatchPricing_PartialPatchOnlyChangesSpecifiedFields(t *testing.T) {
	t.Skip()
	baseCacheRead := 0.4
	baseInputImage := 0.7
	base := configstoreTables.TableModelPricing{
		Model:                   "gpt-4o",
		Provider:                "openai",
		Mode:                    "chat",
		InputCostPerToken:       1,
		OutputCostPerToken:      2,
		CacheReadInputTokenCost: &baseCacheRead,
		InputCostPerImage:       &baseInputImage,
	}

	patched := patchPricing(base, schemas.PricingOverridePatch{
		InputCostPerToken:       schemas.Ptr(3.0),
		CacheReadInputTokenCost: schemas.Ptr(0.9),
	})

	// Changed fields
	assert.Equal(t, 3.0, patched.InputCostPerToken)
	require.NotNil(t, patched.CacheReadInputTokenCost)
	assert.Equal(t, 0.9, *patched.CacheReadInputTokenCost)

	// Unchanged fields
	assert.Equal(t, 2.0, patched.OutputCostPerToken)
	require.NotNil(t, patched.InputCostPerImage)
	assert.Equal(t, 0.7, *patched.InputCostPerImage)
}

func TestApplyScopedPricingOverrides_ScopePrecedence(t *testing.T) {
	mc := newTestCatalog(nil, nil)
	mc.logger = noOpLogger{}

	providerScopeID := "openai"
	providerKeyScopeID := "provider-key-1"
	virtualKeyScopeID := "virtual-key-1"

	globalCost := 2.0
	providerCost := 3.0
	providerKeyCost := 4.0
	virtualKeyCost := 5.0

	require.NoError(t, mc.SetPricingOverrides([]schemas.PricingOverride{
		{
			ID:        "global",
			ScopeKind: schemas.PricingOverrideScopeKindGlobal,
			MatchType: schemas.PricingOverrideMatchExact,
			Pattern:   "gpt-5-nano",
			Patch: schemas.PricingOverridePatch{
				InputCostPerToken: &globalCost,
			},
		},
		{
			ID:         "provider",
			ScopeKind:  schemas.PricingOverrideScopeKindProvider,
			ProviderID: &providerScopeID,
			MatchType:  schemas.PricingOverrideMatchExact,
			Pattern:    "gpt-5-nano",
			Patch: schemas.PricingOverridePatch{
				InputCostPerToken: &providerCost,
			},
		},
		{
			ID:            "provider-key",
			ScopeKind:     schemas.PricingOverrideScopeKindProviderKey,
			ProviderKeyID: &providerKeyScopeID,
			MatchType:     schemas.PricingOverrideMatchExact,
			Pattern:       "gpt-5-nano",
			Patch: schemas.PricingOverridePatch{
				InputCostPerToken: &providerKeyCost,
			},
		},
		{
			ID:           "virtual-key",
			ScopeKind:    schemas.PricingOverrideScopeKindVirtualKey,
			VirtualKeyID: &virtualKeyScopeID,
			MatchType:    schemas.PricingOverrideMatchExact,
			Pattern:      "gpt-5-nano",
			Patch: schemas.PricingOverridePatch{
				InputCostPerToken: &virtualKeyCost,
			},
		},
	}))

	base := configstoreTables.TableModelPricing{
		Model:              "gpt-5-nano",
		Provider:           "openai",
		Mode:               "chat",
		InputCostPerToken:  1.0,
		OutputCostPerToken: 2.0,
	}

	tests := []struct {
		name     string
		scopes   PricingLookupScopes
		expected float64
	}{
		{
			name: "virtual key wins over provider key, provider and global",
			scopes: PricingLookupScopes{
				VirtualKeyID:  virtualKeyScopeID,
				ProviderKeyID: providerKeyScopeID,
				ProviderID:    providerScopeID,
			},
			expected: virtualKeyCost,
		},
		{
			name: "provider key wins over provider and global",
			scopes: PricingLookupScopes{
				ProviderKeyID: providerKeyScopeID,
				ProviderID:    providerScopeID,
			},
			expected: providerKeyCost,
		},
		{
			name: "provider wins over global",
			scopes: PricingLookupScopes{
				ProviderID: providerScopeID,
			},
			expected: providerCost,
		},
		{
			name:     "global applies when no narrower scope is provided",
			scopes:   PricingLookupScopes{},
			expected: globalCost,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			patched := mc.applyScopedPricingOverrides("gpt-5-nano", schemas.ChatCompletionRequest, base, tc.scopes)
			assert.Equal(t, tc.expected, patched.InputCostPerToken)
		})
	}
}
