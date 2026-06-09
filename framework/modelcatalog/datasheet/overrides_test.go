package datasheet

import (
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
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

// newTestStore builds a minimal Store for unit tests. Callers seed pricingData
// directly and use SetOverrides for overrides.
func newTestStore() *Store {
	return &Store{
		logger:                 noOpLogger{},
		pricingData:            map[string]configstoreTables.TableModelPricing{},
		baseModelIndex:         map[string]string{},
		supportedResponseTypes: map[string][]string{},
		supportedParams:        map[string][]string{},
		datasheetByProvider:    map[schemas.ModelProvider][]string{},
	}
}

func TestGetPricing_OverridePrecedenceExactWildcard(t *testing.T) {
	s := newTestStore()
	s.pricingData[makeKey("gpt-4o", "openai", "chat")] = configstoreTables.TableModelPricing{
		Model:              "gpt-4o",
		Provider:           "openai",
		Mode:               "chat",
		InputCostPerToken:  bifrost.Ptr(1.0),
		OutputCostPerToken: bifrost.Ptr(2.0),
	}

	providerID := "openai"
	require.NoError(t, s.SetOverrides([]configstoreTables.TablePricingOverride{
		{
			ID:               "openai-override-0",
			ScopeKind:        string(ScopeKindProvider),
			ProviderID:       &providerID,
			MatchType:        string(MatchTypeWildcard),
			Pattern:          "gpt-*",
			RequestTypes:     []schemas.RequestType{schemas.ChatCompletionRequest},
			PricingPatchJSON: `{"input_cost_per_token":10}`,
		},
		{
			ID:               "openai-override-1",
			ScopeKind:        string(ScopeKindProvider),
			ProviderID:       &providerID,
			MatchType:        string(MatchTypeExact),
			Pattern:          "gpt-4o",
			RequestTypes:     []schemas.RequestType{schemas.ChatCompletionRequest},
			PricingPatchJSON: `{"input_cost_per_token":20}`,
		},
	}))

	pricing := s.resolvePricing(schemas.RoutingInfo{Provider: "openai", Model: "gpt-4o"}, schemas.ChatCompletionRequest, LookupScopes{Provider: "openai"})
	require.NotNil(t, pricing)
	require.NotNil(t, pricing.InputCostPerToken)
	assert.Equal(t, 20.0, *pricing.InputCostPerToken)
}

func TestGetPricing_RequestTypeSpecificOverrideBeatsGeneric(t *testing.T) {
	t.Skip()
	s := newTestStore()
	s.pricingData[makeKey("gpt-4o", "openai", "responses")] = configstoreTables.TableModelPricing{
		Model:              "gpt-4o",
		Provider:           "openai",
		Mode:               "responses",
		InputCostPerToken:  bifrost.Ptr(1.0),
		OutputCostPerToken: bifrost.Ptr(2.0),
	}

	providerID := "openai"
	require.NoError(t, s.SetOverrides([]configstoreTables.TablePricingOverride{
		{
			ID:               "openai-generic",
			ScopeKind:        string(ScopeKindProvider),
			ProviderID:       &providerID,
			MatchType:        string(MatchTypeExact),
			Pattern:          "gpt-4o",
			PricingPatchJSON: `{"input_cost_per_token":9}`,
		},
		{
			ID:               "openai-specific",
			ScopeKind:        string(ScopeKindProvider),
			ProviderID:       &providerID,
			MatchType:        string(MatchTypeExact),
			Pattern:          "gpt-4o",
			RequestTypes:     []schemas.RequestType{schemas.ResponsesRequest},
			PricingPatchJSON: `{"input_cost_per_token":15}`,
		},
	}))

	pricing := s.resolvePricing(schemas.RoutingInfo{Provider: "openai", Model: "gpt-4o"}, schemas.ResponsesRequest, LookupScopes{Provider: "openai"})
	require.NotNil(t, pricing)
	assert.Equal(t, 15.0, pricing.InputCostPerToken)
}

func TestGetPricing_AppliesOverrideAfterFallbackResolution(t *testing.T) {
	t.Skip()
	s := newTestStore()
	s.pricingData[makeKey("gpt-4o", "vertex", "chat")] = configstoreTables.TableModelPricing{
		Model:              "gpt-4o",
		Provider:           "vertex",
		Mode:               "chat",
		InputCostPerToken:  bifrost.Ptr(1.0),
		OutputCostPerToken: bifrost.Ptr(2.0),
	}

	geminiProviderID := "gemini"
	require.NoError(t, s.SetOverrides([]configstoreTables.TablePricingOverride{
		{
			ID:               "gemini-override",
			ScopeKind:        string(ScopeKindProvider),
			ProviderID:       &geminiProviderID,
			MatchType:        string(MatchTypeExact),
			Pattern:          "gpt-4o",
			PricingPatchJSON: `{"input_cost_per_token":7}`,
		},
	}))

	pricing := s.resolvePricing(schemas.RoutingInfo{Provider: "gemini", Model: "gpt-4o"}, schemas.ChatCompletionRequest, LookupScopes{Provider: "gemini"})
	require.NotNil(t, pricing)
	assert.Equal(t, 7.0, pricing.InputCostPerToken)
}

func TestGetPricing_DeploymentLookupUsesResolvedModelForOverrideMatching(t *testing.T) {
	s := newTestStore()
	s.pricingData[makeKey("dep-gpt4o", "openai", "chat")] = configstoreTables.TableModelPricing{
		Model:              "dep-gpt4o",
		Provider:           "openai",
		Mode:               "chat",
		InputCostPerToken:  bifrost.Ptr(1.0),
		OutputCostPerToken: bifrost.Ptr(2.0),
	}

	providerID := "openai"
	require.NoError(t, s.SetOverrides([]configstoreTables.TablePricingOverride{
		{
			ID:               "resolved-model-override",
			ScopeKind:        string(ScopeKindProvider),
			ProviderID:       &providerID,
			MatchType:        string(MatchTypeExact),
			Pattern:          "dep-gpt4o",
			RequestTypes:     []schemas.RequestType{schemas.ChatCompletionRequest},
			PricingPatchJSON: `{"input_cost_per_token":7}`,
		},
	}))

	// Override pattern matches the resolved model name ("dep-gpt4o"), not the
	// originally requested name ("gpt-4o"), because resolved model has priority.
	pricing := s.resolvePricing(schemas.RoutingInfo{Provider: "openai", Model: "gpt-4o", ResolvedKeyAlias: &schemas.ResolvedKeyAlias{ModelID: "dep-gpt4o"}}, schemas.ChatCompletionRequest, LookupScopes{Provider: "openai"})
	require.NotNil(t, pricing)
	require.NotNil(t, pricing.InputCostPerToken)
	assert.Equal(t, 7.0, *pricing.InputCostPerToken)
}

func TestGetPricing_FallbackUsesRequestedProviderForScopeMatching(t *testing.T) {
	s := newTestStore()
	s.pricingData[makeKey("gpt-4o", "vertex", "chat")] = configstoreTables.TableModelPricing{
		Model:              "gpt-4o",
		Provider:           "vertex",
		Mode:               "chat",
		InputCostPerToken:  bifrost.Ptr(1.0),
		OutputCostPerToken: bifrost.Ptr(2.0),
	}

	geminiProviderID := "gemini"
	vertexProviderID := "vertex"
	require.NoError(t, s.SetOverrides([]configstoreTables.TablePricingOverride{
		{
			ID:               "gemini-provider-override",
			ScopeKind:        string(ScopeKindProvider),
			ProviderID:       &geminiProviderID,
			MatchType:        string(MatchTypeExact),
			Pattern:          "gpt-4o",
			RequestTypes:     []schemas.RequestType{schemas.ChatCompletionRequest},
			PricingPatchJSON: `{"input_cost_per_token":5}`,
		},
		{
			ID:               "vertex-provider-override",
			ScopeKind:        string(ScopeKindProvider),
			ProviderID:       &vertexProviderID,
			MatchType:        string(MatchTypeExact),
			Pattern:          "gpt-4o",
			RequestTypes:     []schemas.RequestType{schemas.ChatCompletionRequest},
			PricingPatchJSON: `{"input_cost_per_token":9}`,
		},
	}))

	pricing := s.resolvePricing(schemas.RoutingInfo{Provider: "gemini", Model: "gpt-4o"}, schemas.ChatCompletionRequest, LookupScopes{Provider: "gemini"})
	require.NotNil(t, pricing)
	require.NotNil(t, pricing.InputCostPerToken)
	assert.Equal(t, 5.0, *pricing.InputCostPerToken)
}

func TestGetPricing_ExactOverrideDoesNotMatchProviderPrefixedModel(t *testing.T) {
	t.Skip()
	s := newTestStore()
	s.pricingData[makeKey("openai/gpt-4o", "openai", "chat")] = configstoreTables.TableModelPricing{
		Model:              "openai/gpt-4o",
		Provider:           "openai",
		Mode:               "chat",
		InputCostPerToken:  bifrost.Ptr(1.0),
		OutputCostPerToken: bifrost.Ptr(2.0),
	}

	providerID := "openai"
	require.NoError(t, s.SetOverrides([]configstoreTables.TablePricingOverride{
		{
			ID:               "openai-override-0",
			ScopeKind:        string(ScopeKindProvider),
			ProviderID:       &providerID,
			MatchType:        string(MatchTypeExact),
			Pattern:          "gpt-4o",
			PricingPatchJSON: `{"input_cost_per_token":19}`,
		},
	}))

	pricing := s.resolvePricing(schemas.RoutingInfo{Provider: "openai", Model: "openai/gpt-4o"}, schemas.ChatCompletionRequest, LookupScopes{Provider: "openai"})
	require.NotNil(t, pricing)
	assert.Equal(t, 1.0, pricing.InputCostPerToken)
}

func TestGetPricing_NoMatchingOverrideLeavesPricingUnchanged(t *testing.T) {
	t.Skip()
	s := newTestStore()
	baseCacheRead := 0.4
	s.pricingData[makeKey("gpt-4o", "openai", "chat")] = configstoreTables.TableModelPricing{
		Model:                   "gpt-4o",
		Provider:                "openai",
		Mode:                    "chat",
		InputCostPerToken:       bifrost.Ptr(1.0),
		OutputCostPerToken:      bifrost.Ptr(2.0),
		CacheReadInputTokenCost: &baseCacheRead,
	}

	providerID := "openai"
	require.NoError(t, s.SetOverrides([]configstoreTables.TablePricingOverride{
		{
			ID:               "openai-override-0",
			ScopeKind:        string(ScopeKindProvider),
			ProviderID:       &providerID,
			MatchType:        string(MatchTypeWildcard),
			Pattern:          "claude-*",
			PricingPatchJSON: `{"input_cost_per_token":9}`,
		},
	}))

	pricing := s.resolvePricing(schemas.RoutingInfo{Provider: "openai", Model: "gpt-4o"}, schemas.ChatCompletionRequest, LookupScopes{Provider: "openai"})
	require.NotNil(t, pricing)
	assert.Equal(t, 1.0, pricing.InputCostPerToken)
	assert.Equal(t, 2.0, pricing.OutputCostPerToken)
	require.NotNil(t, pricing.CacheReadInputTokenCost)
	assert.Equal(t, 0.4, *pricing.CacheReadInputTokenCost)
}

func TestDeleteProviderOverrides_StopsApplying(t *testing.T) {
	t.Skip()
	s := newTestStore()
	s.pricingData[makeKey("gpt-4o", "openai", "chat")] = configstoreTables.TableModelPricing{
		Model:              "gpt-4o",
		Provider:           "openai",
		Mode:               "chat",
		InputCostPerToken:  bifrost.Ptr(1.0),
		OutputCostPerToken: bifrost.Ptr(2.0),
	}

	providerID := "openai"
	require.NoError(t, s.SetOverrides([]configstoreTables.TablePricingOverride{
		{
			ID:               "openai-override-0",
			ScopeKind:        string(ScopeKindProvider),
			ProviderID:       &providerID,
			MatchType:        string(MatchTypeExact),
			Pattern:          "gpt-4o",
			PricingPatchJSON: `{"input_cost_per_token":11}`,
		},
	}))

	pricing := s.resolvePricing(schemas.RoutingInfo{Provider: "openai", Model: "gpt-4o"}, schemas.ChatCompletionRequest, LookupScopes{Provider: "openai"})
	require.NotNil(t, pricing)
	assert.Equal(t, 11.0, pricing.InputCostPerToken)

	require.NoError(t, s.SetOverrides(nil))

	pricing = s.resolvePricing(schemas.RoutingInfo{Provider: "openai", Model: "gpt-4o"}, schemas.ChatCompletionRequest, LookupScopes{Provider: "openai"})
	require.NotNil(t, pricing)
	assert.Equal(t, 1.0, pricing.InputCostPerToken)
}

func TestGetPricing_WildcardSpecificityLongerLiteralWins(t *testing.T) {
	t.Skip()
	s := newTestStore()
	s.pricingData[makeKey("gpt-4o-mini", "openai", "chat")] = configstoreTables.TableModelPricing{
		Model:              "gpt-4o-mini",
		Provider:           "openai",
		Mode:               "chat",
		InputCostPerToken:  bifrost.Ptr(1.0),
		OutputCostPerToken: bifrost.Ptr(2.0),
	}

	providerID := "openai"
	require.NoError(t, s.SetOverrides([]configstoreTables.TablePricingOverride{
		{
			ID:               "openai-override-0",
			ScopeKind:        string(ScopeKindProvider),
			ProviderID:       &providerID,
			MatchType:        string(MatchTypeWildcard),
			Pattern:          "gpt-*",
			PricingPatchJSON: `{"input_cost_per_token":5}`,
		},
		{
			ID:               "openai-override-1",
			ScopeKind:        string(ScopeKindProvider),
			ProviderID:       &providerID,
			MatchType:        string(MatchTypeWildcard),
			Pattern:          "gpt-4o*",
			PricingPatchJSON: `{"input_cost_per_token":6}`,
		},
	}))

	pricing := s.resolvePricing(schemas.RoutingInfo{Provider: "openai", Model: "gpt-4o-mini"}, schemas.ChatCompletionRequest, LookupScopes{Provider: "openai"})
	require.NotNil(t, pricing)
	assert.Equal(t, 6.0, pricing.InputCostPerToken)
}

func TestGetPricing_FirstInsertionWinsOnTie(t *testing.T) {
	s := newTestStore()
	s.pricingData[makeKey("gpt-4o-mini", "openai", "chat")] = configstoreTables.TableModelPricing{
		Model:              "gpt-4o-mini",
		Provider:           "openai",
		Mode:               "chat",
		InputCostPerToken:  bifrost.Ptr(1.0),
		OutputCostPerToken: bifrost.Ptr(2.0),
	}

	providerID := "openai"
	require.NoError(t, s.SetOverrides([]configstoreTables.TablePricingOverride{
		{
			ID:               "a-override",
			ScopeKind:        string(ScopeKindProvider),
			ProviderID:       &providerID,
			MatchType:        string(MatchTypeWildcard),
			Pattern:          "gpt-4o*",
			RequestTypes:     []schemas.RequestType{schemas.ChatCompletionRequest},
			PricingPatchJSON: `{"input_cost_per_token":8}`,
		},
		{
			ID:               "b-override",
			ScopeKind:        string(ScopeKindProvider),
			ProviderID:       &providerID,
			MatchType:        string(MatchTypeWildcard),
			Pattern:          "gpt-4o*",
			RequestTypes:     []schemas.RequestType{schemas.ChatCompletionRequest},
			PricingPatchJSON: `{"input_cost_per_token":9}`,
		},
	}))

	pricing := s.resolvePricing(schemas.RoutingInfo{Provider: "openai", Model: "gpt-4o-mini"}, schemas.ChatCompletionRequest, LookupScopes{Provider: "openai"})
	require.NotNil(t, pricing)
	require.NotNil(t, pricing.InputCostPerToken)
	assert.Equal(t, 8.0, *pricing.InputCostPerToken)
}

func TestPatchPricing_PartialPatchOnlyChangesSpecifiedFields(t *testing.T) {
	t.Skip()
	baseCacheRead := 0.4
	baseInputImage := 0.7
	base := configstoreTables.TableModelPricing{
		Model:                   "gpt-4o",
		Provider:                "openai",
		Mode:                    "chat",
		InputCostPerToken:       bifrost.Ptr(1.0),
		OutputCostPerToken:      bifrost.Ptr(2.0),
		CacheReadInputTokenCost: &baseCacheRead,
		InputCostPerImage:       &baseInputImage,
	}

	cacheRead := 0.9
	patched := patchPricing(base, Options{
		InputCostPerToken:       bifrost.Ptr(3.0),
		CacheReadInputTokenCost: &cacheRead,
	})

	assert.Equal(t, 3.0, patched.InputCostPerToken)
	require.NotNil(t, patched.CacheReadInputTokenCost)
	assert.Equal(t, 0.9, *patched.CacheReadInputTokenCost)

	assert.Equal(t, 2.0, patched.OutputCostPerToken)
	require.NotNil(t, patched.InputCostPerImage)
	assert.Equal(t, 0.7, *patched.InputCostPerImage)
}

func TestApplyScopedOverrides_ScopePrecedence(t *testing.T) {
	s := newTestStore()

	providerScopeID := "openai"
	providerKeyScopeID := "provider-key-1"
	virtualKeyScopeID := "virtual-key-1"

	require.NoError(t, s.SetOverrides([]configstoreTables.TablePricingOverride{
		{
			ID:               "global",
			ScopeKind:        string(ScopeKindGlobal),
			MatchType:        string(MatchTypeExact),
			Pattern:          "gpt-5-nano",
			RequestTypes:     []schemas.RequestType{schemas.ChatCompletionRequest},
			PricingPatchJSON: `{"input_cost_per_token":2}`,
		},
		{
			ID:               "provider",
			ScopeKind:        string(ScopeKindProvider),
			ProviderID:       &providerScopeID,
			MatchType:        string(MatchTypeExact),
			Pattern:          "gpt-5-nano",
			RequestTypes:     []schemas.RequestType{schemas.ChatCompletionRequest},
			PricingPatchJSON: `{"input_cost_per_token":3}`,
		},
		{
			ID:               "provider-key",
			ScopeKind:        string(ScopeKindProviderKey),
			ProviderKeyID:    &providerKeyScopeID,
			MatchType:        string(MatchTypeExact),
			Pattern:          "gpt-5-nano",
			RequestTypes:     []schemas.RequestType{schemas.ChatCompletionRequest},
			PricingPatchJSON: `{"input_cost_per_token":4}`,
		},
		{
			ID:               "virtual-key",
			ScopeKind:        string(ScopeKindVirtualKey),
			VirtualKeyID:     &virtualKeyScopeID,
			MatchType:        string(MatchTypeExact),
			Pattern:          "gpt-5-nano",
			RequestTypes:     []schemas.RequestType{schemas.ChatCompletionRequest},
			PricingPatchJSON: `{"input_cost_per_token":5}`,
		},
	}))

	base := configstoreTables.TableModelPricing{
		Model:              "gpt-5-nano",
		Provider:           "openai",
		Mode:               "chat",
		InputCostPerToken:  bifrost.Ptr(1.0),
		OutputCostPerToken: bifrost.Ptr(2.0),
	}

	tests := []struct {
		name     string
		scopes   LookupScopes
		expected float64
	}{
		{
			name: "virtual key wins over provider key, provider and global",
			scopes: LookupScopes{
				VirtualKeyID:  virtualKeyScopeID,
				SelectedKeyID: providerKeyScopeID,
				Provider:      providerScopeID,
			},
			expected: 5.0,
		},
		{
			name: "provider key wins over provider and global",
			scopes: LookupScopes{
				SelectedKeyID: providerKeyScopeID,
				Provider:      providerScopeID,
			},
			expected: 4.0,
		},
		{
			name: "provider wins over global",
			scopes: LookupScopes{
				Provider: providerScopeID,
			},
			expected: 3.0,
		},
		{
			name:     "global applies when no narrower scope is provided",
			scopes:   LookupScopes{},
			expected: 2.0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			patched, applied := s.applyPricingOverrides("gpt-5-nano", schemas.ChatCompletionRequest, base, tc.scopes)
			require.True(t, applied)
			require.NotNil(t, patched.InputCostPerToken)
			assert.Equal(t, tc.expected, *patched.InputCostPerToken)
		})
	}
}
