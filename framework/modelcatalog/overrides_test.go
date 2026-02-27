package modelcatalog

import (
	"fmt"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/pricingoverrides"
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
	MatchType         pricingoverrides.MatchType
	RequestTypes      []schemas.RequestType
	InputCostPerToken *float64
	ID                string
	UpdatedAt         time.Time
}

func setProviderScopedOverrides(t *testing.T, mc *ModelCatalog, provider schemas.ModelProvider, overrides []providerOverrideCompat) error {
	t.Helper()
	scopeID := string(provider)
	compiled := make([]pricingoverrides.Override, 0, len(overrides))
	for i, override := range overrides {
		id := override.ID
		if id == "" {
			id = fmt.Sprintf("%s-override-%d", scopeID, i)
		}
		compiled = append(compiled, pricingoverrides.Override{
			ID:           id,
			ScopeKind:    pricingoverrides.ScopeKindProvider,
			ProviderID:   &scopeID,
			MatchType:    override.MatchType,
			Pattern:      override.ModelPattern,
			RequestTypes: override.RequestTypes,
			UpdatedAt:    override.UpdatedAt,
			Patch: pricingoverrides.Patch{
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
			MatchType:         pricingoverrides.MatchTypeWildcard,
			InputCostPerToken: &wildcard,
		},
		{
			ModelPattern:      "gpt-4o",
			MatchType:         pricingoverrides.MatchTypeExact,
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
			MatchType:         pricingoverrides.MatchTypeExact,
			InputCostPerToken: &generic,
		},
		{
			ModelPattern:      "gpt-4o",
			MatchType:         pricingoverrides.MatchTypeExact,
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
			MatchType:         pricingoverrides.MatchTypeExact,
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
	require.NoError(t, mc.SetPricingOverrides([]pricingoverrides.Override{
		{
			ID:         "requested-model-override",
			ScopeKind:  pricingoverrides.ScopeKindProvider,
			ProviderID: &providerID,
			MatchType:  pricingoverrides.MatchTypeExact,
			Pattern:    "gpt-4o",
			Patch: pricingoverrides.Patch{
				InputCostPerToken: &override,
			},
		},
	}))

	pricing, ok := mc.getPricingLocked(
		"dep-gpt4o",
		"gpt-4o",
		"openai",
		schemas.ChatCompletionRequest,
		PricingLookupScopes{Provider: "openai"},
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
	require.NoError(t, mc.SetPricingOverrides([]pricingoverrides.Override{
		{
			ID:         "gemini-provider-override",
			ScopeKind:  pricingoverrides.ScopeKindProvider,
			ProviderID: &geminiProviderID,
			MatchType:  pricingoverrides.MatchTypeExact,
			Pattern:    "gpt-4o",
			Patch: pricingoverrides.Patch{
				InputCostPerToken: &geminiOverrideCost,
			},
		},
		{
			ID:         "vertex-provider-override",
			ScopeKind:  pricingoverrides.ScopeKindProvider,
			ProviderID: &vertexProviderID,
			MatchType:  pricingoverrides.MatchTypeExact,
			Pattern:    "gpt-4o",
			Patch: pricingoverrides.Patch{
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
			MatchType:         pricingoverrides.MatchTypeExact,
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
			MatchType:         pricingoverrides.MatchTypeWildcard,
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
			MatchType:         pricingoverrides.MatchTypeExact,
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
			MatchType:         pricingoverrides.MatchTypeWildcard,
			InputCostPerToken: &generic,
		},
		{
			ModelPattern:      "gpt-4o*",
			MatchType:         pricingoverrides.MatchTypeWildcard,
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
			MatchType:         pricingoverrides.MatchTypeWildcard,
			InputCostPerToken: &first,
			ID:                "older",
			UpdatedAt:         now.Add(-1 * time.Minute),
		},
		{
			ModelPattern:      "gpt-4o*",
			MatchType:         pricingoverrides.MatchTypeWildcard,
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
			MatchType:         pricingoverrides.MatchTypeWildcard,
			InputCostPerToken: &first,
			ID:                "a-override",
			UpdatedAt:         now,
		},
		{
			ModelPattern:      "gpt-4o*",
			MatchType:         pricingoverrides.MatchTypeWildcard,
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

	patched := patchPricing(base, pricingoverrides.Patch{
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

	require.NoError(t, mc.SetPricingOverrides([]pricingoverrides.Override{
		{
			ID:        "global",
			ScopeKind: pricingoverrides.ScopeKindGlobal,
			MatchType: pricingoverrides.MatchTypeExact,
			Pattern:   "gpt-5-nano",
			Patch: pricingoverrides.Patch{
				InputCostPerToken: &globalCost,
			},
		},
		{
			ID:         "provider",
			ScopeKind:  pricingoverrides.ScopeKindProvider,
			ProviderID: &providerScopeID,
			MatchType:  pricingoverrides.MatchTypeExact,
			Pattern:    "gpt-5-nano",
			Patch: pricingoverrides.Patch{
				InputCostPerToken: &providerCost,
			},
		},
		{
			ID:            "provider-key",
			ScopeKind:     pricingoverrides.ScopeKindProviderKey,
			ProviderKeyID: &providerKeyScopeID,
			MatchType:     pricingoverrides.MatchTypeExact,
			Pattern:       "gpt-5-nano",
			Patch: pricingoverrides.Patch{
				InputCostPerToken: &providerKeyCost,
			},
		},
		{
			ID:           "virtual-key",
			ScopeKind:    pricingoverrides.ScopeKindVirtualKey,
			VirtualKeyID: &virtualKeyScopeID,
			MatchType:    pricingoverrides.MatchTypeExact,
			Pattern:      "gpt-5-nano",
			Patch: pricingoverrides.Patch{
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
				SelectedKeyID: providerKeyScopeID,
				Provider:      providerScopeID,
			},
			expected: virtualKeyCost,
		},
		{
			name: "provider key wins over provider and global",
			scopes: PricingLookupScopes{
				SelectedKeyID: providerKeyScopeID,
				Provider:      providerScopeID,
			},
			expected: providerKeyCost,
		},
		{
			name: "provider wins over global",
			scopes: PricingLookupScopes{
				Provider: providerScopeID,
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
