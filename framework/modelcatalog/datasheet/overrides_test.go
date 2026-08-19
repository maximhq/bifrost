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
		deprecatedByProvider:   map[schemas.ModelProvider][]string{},
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
	s := newTestStore()
	s.pricingData[makeKey("gpt-4o", "openai", "responses")] = configstoreTables.TableModelPricing{
		Model:              "gpt-4o",
		Provider:           "openai",
		Mode:               "responses",
		InputCostPerToken:  bifrost.Ptr(1.0),
		OutputCostPerToken: bifrost.Ptr(2.0),
	}

	providerID := "openai"
	// "specific" is inserted first so it wins the first-insertion-wins tie-break
	// (see TestGetPricing_FirstInsertionWinsOnTie) once "generic" is also made
	// eligible to match the Responses mode below — otherwise "generic" would win
	// merely by being listed first, not because it's actually more specific.
	require.NoError(t, s.SetOverrides([]configstoreTables.TablePricingOverride{
		{
			ID:               "openai-specific",
			ScopeKind:        string(ScopeKindProvider),
			ProviderID:       &providerID,
			MatchType:        string(MatchTypeExact),
			Pattern:          "gpt-4o",
			RequestTypes:     []schemas.RequestType{schemas.ResponsesRequest},
			PricingPatchJSON: `{"input_cost_per_token":15}`,
		},
		{
			ID:               "openai-generic",
			ScopeKind:        string(ScopeKindProvider),
			ProviderID:       &providerID,
			MatchType:        string(MatchTypeExact),
			Pattern:          "gpt-4o",
			RequestTypes:     []schemas.RequestType{schemas.ResponsesRequest},
			PricingPatchJSON: `{"input_cost_per_token":9}`,
		},
	}))

	pricing := s.resolvePricing(schemas.RoutingInfo{Provider: "openai", Model: "gpt-4o"}, schemas.ResponsesRequest, LookupScopes{Provider: "openai"})
	require.NotNil(t, pricing)
	require.NotNil(t, pricing.InputCostPerToken)
	assert.Equal(t, 15.0, *pricing.InputCostPerToken)
}

func TestGetPricing_AppliesOverrideAfterFallbackResolution(t *testing.T) {
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
			RequestTypes:     []schemas.RequestType{schemas.ChatCompletionRequest},
			PricingPatchJSON: `{"input_cost_per_token":7}`,
		},
	}))

	pricing := s.resolvePricing(schemas.RoutingInfo{Provider: "gemini", Model: "gpt-4o"}, schemas.ChatCompletionRequest, LookupScopes{Provider: "gemini"})
	require.NotNil(t, pricing)
	require.NotNil(t, pricing.InputCostPerToken)
	assert.Equal(t, 7.0, *pricing.InputCostPerToken)
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
			RequestTypes:     []schemas.RequestType{schemas.ChatCompletionRequest},
			PricingPatchJSON: `{"input_cost_per_token":19}`,
		},
	}))

	pricing := s.resolvePricing(schemas.RoutingInfo{Provider: "openai", Model: "openai/gpt-4o"}, schemas.ChatCompletionRequest, LookupScopes{Provider: "openai"})
	require.NotNil(t, pricing)
	require.NotNil(t, pricing.InputCostPerToken)
	assert.Equal(t, 1.0, *pricing.InputCostPerToken)
}

func TestGetPricing_NoMatchingOverrideLeavesPricingUnchanged(t *testing.T) {
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
			RequestTypes:     []schemas.RequestType{schemas.ChatCompletionRequest},
			PricingPatchJSON: `{"input_cost_per_token":9}`,
		},
	}))

	pricing := s.resolvePricing(schemas.RoutingInfo{Provider: "openai", Model: "gpt-4o"}, schemas.ChatCompletionRequest, LookupScopes{Provider: "openai"})
	require.NotNil(t, pricing)
	require.NotNil(t, pricing.InputCostPerToken)
	require.NotNil(t, pricing.OutputCostPerToken)
	assert.Equal(t, 1.0, *pricing.InputCostPerToken)
	assert.Equal(t, 2.0, *pricing.OutputCostPerToken)
	require.NotNil(t, pricing.CacheReadInputTokenCost)
	assert.Equal(t, 0.4, *pricing.CacheReadInputTokenCost)
}

func TestDeleteProviderOverrides_StopsApplying(t *testing.T) {
	s := newTestStore()
	s.pricingData[makeKey("gpt-4o", "openai", "chat")] = configstoreTables.TableModelPricing{
		Model:              "gpt-4o",
		Provider:           "openai",
		Mode:               "chat",
		InputCostPerToken:  bifrost.Ptr(1.0),
		OutputCostPerToken: bifrost.Ptr(2.0),
	}
	s.pricingData[makeKey("gpt-4o-mini", "openai", "chat")] = configstoreTables.TableModelPricing{
		Model:              "gpt-4o-mini",
		Provider:           "openai",
		Mode:               "chat",
		InputCostPerToken:  bifrost.Ptr(3.0),
		OutputCostPerToken: bifrost.Ptr(4.0),
	}

	providerID := "openai"
	require.NoError(t, s.SetOverrides([]configstoreTables.TablePricingOverride{
		{
			ID:               "openai-override-0",
			ScopeKind:        string(ScopeKindProvider),
			ProviderID:       &providerID,
			MatchType:        string(MatchTypeExact),
			Pattern:          "gpt-4o",
			RequestTypes:     []schemas.RequestType{schemas.ChatCompletionRequest},
			PricingPatchJSON: `{"input_cost_per_token":11}`,
		},
		{
			ID:               "openai-override-1",
			ScopeKind:        string(ScopeKindProvider),
			ProviderID:       &providerID,
			MatchType:        string(MatchTypeExact),
			Pattern:          "gpt-4o-mini",
			RequestTypes:     []schemas.RequestType{schemas.ChatCompletionRequest},
			PricingPatchJSON: `{"input_cost_per_token":22}`,
		},
	}))

	pricing := s.resolvePricing(schemas.RoutingInfo{Provider: "openai", Model: "gpt-4o"}, schemas.ChatCompletionRequest, LookupScopes{Provider: "openai"})
	require.NotNil(t, pricing)
	require.NotNil(t, pricing.InputCostPerToken)
	assert.Equal(t, 11.0, *pricing.InputCostPerToken)

	s.DeleteOverride("openai-override-0")

	pricing = s.resolvePricing(schemas.RoutingInfo{Provider: "openai", Model: "gpt-4o"}, schemas.ChatCompletionRequest, LookupScopes{Provider: "openai"})
	require.NotNil(t, pricing)
	require.NotNil(t, pricing.InputCostPerToken)
	assert.Equal(t, 1.0, *pricing.InputCostPerToken)

	// The untouched override must still be applying its patch.
	pricing = s.resolvePricing(schemas.RoutingInfo{Provider: "openai", Model: "gpt-4o-mini"}, schemas.ChatCompletionRequest, LookupScopes{Provider: "openai"})
	require.NotNil(t, pricing)
	require.NotNil(t, pricing.InputCostPerToken)
	assert.Equal(t, 22.0, *pricing.InputCostPerToken)
}

func TestGetPricing_WildcardSpecificityLongerLiteralWins(t *testing.T) {
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
			RequestTypes:     []schemas.RequestType{schemas.ChatCompletionRequest},
			PricingPatchJSON: `{"input_cost_per_token":5}`,
		},
		{
			ID:               "openai-override-1",
			ScopeKind:        string(ScopeKindProvider),
			ProviderID:       &providerID,
			MatchType:        string(MatchTypeWildcard),
			Pattern:          "gpt-4o*",
			RequestTypes:     []schemas.RequestType{schemas.ChatCompletionRequest},
			PricingPatchJSON: `{"input_cost_per_token":6}`,
		},
	}))

	pricing := s.resolvePricing(schemas.RoutingInfo{Provider: "openai", Model: "gpt-4o-mini"}, schemas.ChatCompletionRequest, LookupScopes{Provider: "openai"})
	require.NotNil(t, pricing)
	require.NotNil(t, pricing.InputCostPerToken)
	assert.Equal(t, 6.0, *pricing.InputCostPerToken)
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

	require.NotNil(t, patched.InputCostPerToken)
	assert.Equal(t, 3.0, *patched.InputCostPerToken)
	require.NotNil(t, patched.CacheReadInputTokenCost)
	assert.Equal(t, 0.9, *patched.CacheReadInputTokenCost)

	require.NotNil(t, patched.OutputCostPerToken)
	assert.Equal(t, 2.0, *patched.OutputCostPerToken)
	require.NotNil(t, patched.InputCostPerImage)
	assert.Equal(t, 0.7, *patched.InputCostPerImage)
}

func TestPatchPricing_CostPerRequest(t *testing.T) {
	base := configstoreTables.TableModelPricing{
		Model:    "gpt-4o",
		Provider: "openai",
		Mode:     "chat",
	}

	patched := patchPricing(base, Options{
		CostPerRequest: bifrost.Ptr(0.02),
	})

	require.NotNil(t, patched.CostPerRequest)
	assert.Equal(t, 0.02, *patched.CostPerRequest)
}

func TestPatchPricing_CostPerRequestZero(t *testing.T) {
	base := configstoreTables.TableModelPricing{
		CostPerRequest: bifrost.Ptr(0.02),
	}
	patched := patchPricing(base, Options{
		CostPerRequest: bifrost.Ptr(0.0),
	})

	require.NotNil(t, patched.CostPerRequest)
	assert.Equal(t, 0.0, *patched.CostPerRequest)
}

func TestApplyScopedOverrides_ScopePrecedence(t *testing.T) {
	s := newTestStore()

	providerScopeID := "openai"
	providerKeyScopeID := "provider-key-1"
	virtualKeyScopeID := "virtual-key-1"
	userScopeID := "user-1"

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
		{
			ID:               "user",
			ScopeKind:        string(ScopeKindUser),
			UserID:           &userScopeID,
			MatchType:        string(MatchTypeExact),
			Pattern:          "gpt-5-nano",
			RequestTypes:     []schemas.RequestType{schemas.ChatCompletionRequest},
			PricingPatchJSON: `{"input_cost_per_token":6}`,
		},
		{
			ID:               "user-provider",
			ScopeKind:        string(ScopeKindUserProvider),
			UserID:           &userScopeID,
			ProviderID:       &providerScopeID,
			MatchType:        string(MatchTypeExact),
			Pattern:          "gpt-5-nano",
			RequestTypes:     []schemas.RequestType{schemas.ChatCompletionRequest},
			PricingPatchJSON: `{"input_cost_per_token":7}`,
		},
		{
			ID:               "user-provider-key",
			ScopeKind:        string(ScopeKindUserProviderKey),
			UserID:           &userScopeID,
			ProviderID:       &providerScopeID,
			ProviderKeyID:    &providerKeyScopeID,
			MatchType:        string(MatchTypeExact),
			Pattern:          "gpt-5-nano",
			RequestTypes:     []schemas.RequestType{schemas.ChatCompletionRequest},
			PricingPatchJSON: `{"input_cost_per_token":8}`,
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
			name: "virtual key wins over the whole user family",
			scopes: LookupScopes{
				UserID:        userScopeID,
				VirtualKeyID:  virtualKeyScopeID,
				SelectedKeyID: providerKeyScopeID,
				Provider:      providerScopeID,
			},
			expected: 5.0,
		},
		{
			name: "user provider key wins when the virtual key does not match",
			scopes: LookupScopes{
				UserID:        userScopeID,
				VirtualKeyID:  "some-other-vk",
				SelectedKeyID: providerKeyScopeID,
				Provider:      providerScopeID,
			},
			expected: 8.0,
		},
		{
			name: "user provider wins when no provider key is selected",
			scopes: LookupScopes{
				UserID:   userScopeID,
				Provider: providerScopeID,
			},
			expected: 7.0,
		},
		{
			name: "user wins when only the user matches",
			scopes: LookupScopes{
				UserID: userScopeID,
			},
			expected: 6.0,
		},
		{
			name: "non-matching user falls through to provider key",
			scopes: LookupScopes{
				UserID:        "someone-else",
				SelectedKeyID: providerKeyScopeID,
				Provider:      providerScopeID,
			},
			expected: 4.0,
		},
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

// TestOverrideIsValid_UserScopeKind covers the user scope kind contract:
// user_id is required, and no other scope identifier may accompany it, in
// either direction.
func TestOverrideIsValid_UserScopeKind(t *testing.T) {
	userID := "user-1"
	vkID := "virtual-key-1"

	valid := Override{
		ScopeKind:    ScopeKindUser,
		UserID:       &userID,
		MatchType:    MatchTypeExact,
		Pattern:      "gpt-5-nano",
		RequestTypes: []schemas.RequestType{schemas.ChatCompletionRequest},
	}
	require.NoError(t, valid.IsValid())

	missingUser := valid
	missingUser.UserID = nil
	require.ErrorContains(t, missingUser.IsValid(), "user_id is required")

	withVK := valid
	withVK.VirtualKeyID = &vkID
	require.ErrorContains(t, withVK.IsValid(), "only supports user_id")

	vkWithUser := Override{
		ScopeKind:    ScopeKindVirtualKey,
		VirtualKeyID: &vkID,
		UserID:       &userID,
		MatchType:    MatchTypeExact,
		Pattern:      "gpt-5-nano",
		RequestTypes: []schemas.RequestType{schemas.ChatCompletionRequest},
	}
	require.ErrorContains(t, vkWithUser.IsValid(), "only supports virtual_key_id")

	globalWithUser := Override{
		ScopeKind:    ScopeKindGlobal,
		UserID:       &userID,
		MatchType:    MatchTypeExact,
		Pattern:      "gpt-5-nano",
		RequestTypes: []schemas.RequestType{schemas.ChatCompletionRequest},
	}
	require.ErrorContains(t, globalWithUser.IsValid(), "must not include scope identifiers")

	providerID := "openai"
	providerKeyID := "provider-key-1"

	userProvider := Override{
		ScopeKind:    ScopeKindUserProvider,
		UserID:       &userID,
		ProviderID:   &providerID,
		MatchType:    MatchTypeExact,
		Pattern:      "gpt-5-nano",
		RequestTypes: []schemas.RequestType{schemas.ChatCompletionRequest},
	}
	require.NoError(t, userProvider.IsValid())

	userProviderMissingProvider := userProvider
	userProviderMissingProvider.ProviderID = nil
	require.ErrorContains(t, userProviderMissingProvider.IsValid(), "user_id and provider_id are required")

	userProviderWithVK := userProvider
	userProviderWithVK.VirtualKeyID = &vkID
	require.ErrorContains(t, userProviderWithVK.IsValid(), "does not support virtual_key_id or provider_key_id")

	userProviderKey := Override{
		ScopeKind:     ScopeKindUserProviderKey,
		UserID:        &userID,
		ProviderID:    &providerID,
		ProviderKeyID: &providerKeyID,
		MatchType:     MatchTypeExact,
		Pattern:       "gpt-5-nano",
		RequestTypes:  []schemas.RequestType{schemas.ChatCompletionRequest},
	}
	require.NoError(t, userProviderKey.IsValid())

	userProviderKeyMissingKey := userProviderKey
	userProviderKeyMissingKey.ProviderKeyID = nil
	require.ErrorContains(t, userProviderKeyMissingKey.IsValid(), "user_id, provider_id, and provider_key_id are required")

	userProviderKeyWithVK := userProviderKey
	userProviderKeyWithVK.VirtualKeyID = &vkID
	require.ErrorContains(t, userProviderKeyWithVK.IsValid(), "does not support virtual_key_id")
}

func TestCatalogPricingOverrides_ProviderBeatsGlobal(t *testing.T) {
	s := newTestStore()
	providerID := "openai"

	require.NoError(t, s.SetOverrides([]configstoreTables.TablePricingOverride{
		{
			ID:               "global",
			Name:             "Global",
			ScopeKind:        string(ScopeKindGlobal),
			MatchType:        string(MatchTypeExact),
			Pattern:          "gpt-4o",
			RequestTypes:     []schemas.RequestType{schemas.ChatCompletionRequest},
			PricingPatchJSON: `{"input_cost_per_token":2}`,
		},
		{
			ID:               "provider",
			Name:             "Provider",
			ScopeKind:        string(ScopeKindProvider),
			ProviderID:       &providerID,
			MatchType:        string(MatchTypeExact),
			Pattern:          "gpt-4o",
			RequestTypes:     []schemas.RequestType{schemas.ChatCompletionRequest},
			PricingPatchJSON: `{"input_cost_per_token":3}`,
		},
	}))

	result := s.CatalogPricingOverrides("gpt-4o", schemas.OpenAI, "chat")
	assert.Equal(t, "provider", result.AppliedID)
	require.NotNil(t, result.AppliedPatch)
	require.NotNil(t, result.AppliedPatch.InputCostPerToken)
	assert.Equal(t, 3.0, *result.AppliedPatch.InputCostPerToken)

	require.Len(t, result.Matching, 2)
	assert.Equal(t, "provider", result.Matching[0].ID, "provider scope sorts before global")
	assert.Equal(t, "global", result.Matching[1].ID)
}

func TestCatalogPricingOverrides_IgnoresMismatchedProvider(t *testing.T) {
	s := newTestStore()
	anthropicID := "anthropic"

	require.NoError(t, s.SetOverrides([]configstoreTables.TablePricingOverride{
		{
			ID:               "anthropic-only",
			ScopeKind:        string(ScopeKindProvider),
			ProviderID:       &anthropicID,
			MatchType:        string(MatchTypeExact),
			Pattern:          "gpt-4o",
			RequestTypes:     []schemas.RequestType{schemas.ChatCompletionRequest},
			PricingPatchJSON: `{"input_cost_per_token":3}`,
		},
	}))

	result := s.CatalogPricingOverrides("gpt-4o", schemas.OpenAI, "chat")
	assert.Empty(t, result.AppliedID)
	assert.Nil(t, result.AppliedPatch)
	assert.Empty(t, result.Matching)
}

func TestCatalogPricingOverrides_NonGlobalScopesAreInformationalOnly(t *testing.T) {
	s := newTestStore()
	providerID := "openai"
	providerKeyID := "provider-key-1"
	virtualKeyID := "virtual-key-1"
	userID := "user-1"

	require.NoError(t, s.SetOverrides([]configstoreTables.TablePricingOverride{
		{
			ID:               "virtual-key",
			ScopeKind:        string(ScopeKindVirtualKey),
			VirtualKeyID:     &virtualKeyID,
			MatchType:        string(MatchTypeExact),
			Pattern:          "gpt-4o",
			RequestTypes:     []schemas.RequestType{schemas.ChatCompletionRequest},
			PricingPatchJSON: `{"input_cost_per_token":5}`,
		},
		{
			ID:               "user-provider",
			ScopeKind:        string(ScopeKindUserProvider),
			UserID:           &userID,
			ProviderID:       &providerID,
			MatchType:        string(MatchTypeExact),
			Pattern:          "gpt-4o",
			RequestTypes:     []schemas.RequestType{schemas.ChatCompletionRequest},
			PricingPatchJSON: `{"input_cost_per_token":7}`,
		},
		{
			// provider_key scope carries no provider_id, so it can't be
			// provider-filtered — it must still surface informationally.
			ID:               "provider-key",
			ScopeKind:        string(ScopeKindProviderKey),
			ProviderKeyID:    &providerKeyID,
			MatchType:        string(MatchTypeExact),
			Pattern:          "gpt-4o",
			RequestTypes:     []schemas.RequestType{schemas.ChatCompletionRequest},
			PricingPatchJSON: `{"input_cost_per_token":4}`,
		},
	}))

	result := s.CatalogPricingOverrides("gpt-4o", schemas.OpenAI, "chat")
	assert.Empty(t, result.AppliedID, "no global/provider scoped override exists")
	assert.Nil(t, result.AppliedPatch)

	ids := make([]string, 0, len(result.Matching))
	for _, o := range result.Matching {
		ids = append(ids, o.ID)
	}
	assert.Equal(t, []string{"virtual-key", "user-provider", "provider-key"}, ids)
}

func TestCatalogPricingOverrides_WildcardLongestPrefixWins(t *testing.T) {
	s := newTestStore()

	require.NoError(t, s.SetOverrides([]configstoreTables.TablePricingOverride{
		{
			ID:               "broad",
			ScopeKind:        string(ScopeKindGlobal),
			MatchType:        string(MatchTypeWildcard),
			Pattern:          "gpt-*",
			RequestTypes:     []schemas.RequestType{schemas.ChatCompletionRequest},
			PricingPatchJSON: `{"input_cost_per_token":1}`,
		},
		{
			ID:               "narrow",
			ScopeKind:        string(ScopeKindGlobal),
			MatchType:        string(MatchTypeWildcard),
			Pattern:          "gpt-4*",
			RequestTypes:     []schemas.RequestType{schemas.ChatCompletionRequest},
			PricingPatchJSON: `{"input_cost_per_token":2}`,
		},
	}))

	result := s.CatalogPricingOverrides("gpt-4o", schemas.OpenAI, "chat")
	assert.Equal(t, "narrow", result.AppliedID)
	require.Len(t, result.Matching, 2)
	assert.Equal(t, "narrow", result.Matching[0].ID)
	assert.Equal(t, "gpt-4*", result.Matching[0].Pattern, "un-stripped pattern is reported")

	// A model only the broad pattern covers.
	other := s.CatalogPricingOverrides("gpt-5-nano", schemas.OpenAI, "chat")
	assert.Equal(t, "broad", other.AppliedID)
	require.Len(t, other.Matching, 1)
}

func TestCatalogPricingOverrides_ModeFilteringAppliesToWinnerOnly(t *testing.T) {
	s := newTestStore()

	require.NoError(t, s.SetOverrides([]configstoreTables.TablePricingOverride{
		{
			ID:               "embedding-only",
			ScopeKind:        string(ScopeKindGlobal),
			MatchType:        string(MatchTypeExact),
			Pattern:          "gpt-4o",
			RequestTypes:     []schemas.RequestType{schemas.EmbeddingRequest},
			PricingPatchJSON: `{"input_cost_per_token":9}`,
		},
	}))

	chat := s.CatalogPricingOverrides("gpt-4o", schemas.OpenAI, "chat")
	assert.Empty(t, chat.AppliedID)
	assert.Nil(t, chat.AppliedPatch)
	require.Len(t, chat.Matching, 1, "listed informationally even though the mode differs")
	assert.Equal(t, "embedding-only", chat.Matching[0].ID)

	embedding := s.CatalogPricingOverrides("gpt-4o", schemas.OpenAI, "embedding")
	assert.Equal(t, "embedding-only", embedding.AppliedID)
	require.NotNil(t, embedding.AppliedPatch)
}

func TestCatalogPricingOverrides_EmptyStore(t *testing.T) {
	s := newTestStore()
	assert.Equal(t, CatalogPricingOverrides{}, s.CatalogPricingOverrides("gpt-4o", schemas.OpenAI, "chat"))

	require.NoError(t, s.SetOverrides(nil))
	assert.Equal(t, CatalogPricingOverrides{}, s.CatalogPricingOverrides("gpt-4o", schemas.OpenAI, "chat"))
}

// Callers must not be able to reach through the returned result into the
// pointers the runtime lookup structure prices requests with.
func TestCatalogPricingOverrides_ReturnsDeepCopies(t *testing.T) {
	s := newTestStore()
	providerID := "openai"

	require.NoError(t, s.SetOverrides([]configstoreTables.TablePricingOverride{
		{
			ID:               "provider",
			Name:             "Provider",
			ScopeKind:        string(ScopeKindProvider),
			ProviderID:       &providerID,
			MatchType:        string(MatchTypeExact),
			Pattern:          "gpt-4o",
			RequestTypes:     []schemas.RequestType{schemas.ChatCompletionRequest},
			PricingPatchJSON: `{"input_cost_per_token":3}`,
		},
	}))

	result := s.CatalogPricingOverrides("gpt-4o", schemas.OpenAI, "chat")
	require.NotNil(t, result.AppliedPatch)
	require.NotNil(t, result.AppliedPatch.InputCostPerToken)
	require.Len(t, result.Matching, 1)
	require.NotNil(t, result.Matching[0].ProviderID)
	require.Len(t, result.Matching[0].RequestTypes, 1)
	require.NotNil(t, result.Matching[0].Options.InputCostPerToken)

	// A caller mutating what it was handed.
	*result.AppliedPatch.InputCostPerToken = 999
	*result.Matching[0].Options.InputCostPerToken = 999
	*result.Matching[0].ProviderID = "mutated"
	result.Matching[0].RequestTypes[0] = schemas.EmbeddingRequest

	fresh := s.CatalogPricingOverrides("gpt-4o", schemas.OpenAI, "chat")
	require.NotNil(t, fresh.AppliedPatch)
	require.NotNil(t, fresh.AppliedPatch.InputCostPerToken)
	assert.Equal(t, 3.0, *fresh.AppliedPatch.InputCostPerToken, "applied patch must survive a caller mutating a prior result")
	require.Len(t, fresh.Matching, 1)
	require.NotNil(t, fresh.Matching[0].Options.InputCostPerToken)
	assert.Equal(t, 3.0, *fresh.Matching[0].Options.InputCostPerToken)
	require.NotNil(t, fresh.Matching[0].ProviderID)
	assert.Equal(t, "openai", *fresh.Matching[0].ProviderID)
	assert.Equal(t, []schemas.RequestType{schemas.ChatCompletionRequest}, fresh.Matching[0].RequestTypes)

	// The request-pricing path reads the same entries the catalog view exposed.
	// Scope with a literal, not providerID — the override holds &providerID, so
	// the mutation above would otherwise rewrite this lookup's own input.
	priced, ok := s.applyPricingOverrides("gpt-4o", schemas.ChatCompletionRequest,
		configstoreTables.TableModelPricing{}, LookupScopes{Provider: "openai"})
	require.True(t, ok)
	require.NotNil(t, priced.InputCostPerToken)
	assert.Equal(t, 3.0, *priced.InputCostPerToken, "runtime pricing must survive a caller mutating a catalog result")
}
