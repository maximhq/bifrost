package modelcatalog

import (
	"fmt"
	"testing"
	"time"

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

func makeScopedOverride(
	id string,
	createdAt time.Time,
	scope configstoreTables.PricingOverrideScope,
	scopeID string,
	override schemas.ProviderPricingOverride,
) configstoreTables.TablePricingOverride {
	var scopeIDPtr *string
	if scope != configstoreTables.PricingOverrideScopeGlobal {
		scopeIDPtr = bifrost.Ptr(scopeID)
	}

	return configstoreTables.TablePricingOverride{
		ID:                id,
		Name:              id,
		Enabled:           true,
		Scope:             scope,
		ScopeID:           scopeIDPtr,
		ModelPattern:      override.ModelPattern,
		MatchType:         override.MatchType,
		RequestTypes:      override.RequestTypes,
		InputCostPerToken: override.InputCostPerToken,
		CreatedAt:         createdAt,
	}
}

func setProviderScopedOverrides(mc *ModelCatalog, provider schemas.ModelProvider, overrides []schemas.ProviderPricingOverride) ([]string, error) {
	records := make([]configstoreTables.TablePricingOverride, 0, len(overrides))
	ids := make([]string, 0, len(overrides))
	baseTime := time.Unix(1704067200, 0) // 2024-01-01T00:00:00Z

	for i := range overrides {
		id := fmt.Sprintf("%s-%d", provider, i)
		ids = append(ids, id)
		records = append(records, makeScopedOverride(
			id,
			baseTime.Add(time.Duration(i)*time.Millisecond),
			configstoreTables.PricingOverrideScopeProvider,
			string(provider),
			overrides[i],
		))
	}

	return ids, mc.SetPricingOverrides(records)
}

func TestSetPricingOverrides_InvalidRegex(t *testing.T) {
	mc := newTestCatalog(nil, nil)
	err := mc.SetPricingOverrides([]configstoreTables.TablePricingOverride{
		makeScopedOverride(
			"bad-regex",
			time.Now(),
			configstoreTables.PricingOverrideScopeProvider,
			string(schemas.OpenAI),
			schemas.ProviderPricingOverride{
				ModelPattern: "[",
				MatchType:    schemas.PricingOverrideMatchRegex,
			},
		),
	})
	require.Error(t, err)
}

func TestGetPricing_OverridePrecedenceExactWildcardRegex(t *testing.T) {
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
	regex := 30.0
	_, err := setProviderScopedOverrides(mc, schemas.OpenAI, []schemas.ProviderPricingOverride{
		{
			ModelPattern:      "gpt-*",
			MatchType:         schemas.PricingOverrideMatchWildcard,
			InputCostPerToken: &wildcard,
		},
		{
			ModelPattern:      "^gpt-.*$",
			MatchType:         schemas.PricingOverrideMatchRegex,
			InputCostPerToken: &regex,
		},
		{
			ModelPattern:      "gpt-4o",
			MatchType:         schemas.PricingOverrideMatchExact,
			InputCostPerToken: &exact,
		},
	})
	require.NoError(t, err)

	pricing, ok := mc.getPricing("gpt-4o", "openai", schemas.ChatCompletionRequest, "", "")
	require.True(t, ok)
	require.NotNil(t, pricing)
	assert.Equal(t, 20.0, pricing.InputCostPerToken)
	assert.Equal(t, 2.0, pricing.OutputCostPerToken)
}

func TestGetPricing_WildcardBeatsRegex(t *testing.T) {
	mc := newTestCatalog(nil, nil)
	mc.logger = noOpLogger{}
	mc.pricingData[makeKey("gpt-4o-mini", "openai", "chat")] = configstoreTables.TableModelPricing{
		Model:              "gpt-4o-mini",
		Provider:           "openai",
		Mode:               "chat",
		InputCostPerToken:  1,
		OutputCostPerToken: 2,
	}

	wildcard := 11.0
	regex := 12.0
	_, err := setProviderScopedOverrides(mc, schemas.OpenAI, []schemas.ProviderPricingOverride{
		{
			ModelPattern:      "^gpt-4o.*$",
			MatchType:         schemas.PricingOverrideMatchRegex,
			InputCostPerToken: &regex,
		},
		{
			ModelPattern:      "gpt-4o*",
			MatchType:         schemas.PricingOverrideMatchWildcard,
			InputCostPerToken: &wildcard,
		},
	})
	require.NoError(t, err)

	pricing, ok := mc.getPricing("gpt-4o-mini", "openai", schemas.ChatCompletionRequest, "", "")
	require.True(t, ok)
	require.NotNil(t, pricing)
	assert.Equal(t, 11.0, pricing.InputCostPerToken)
}

func TestGetPricing_RequestTypeSpecificOverrideBeatsGeneric(t *testing.T) {
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
	_, err := setProviderScopedOverrides(mc, schemas.OpenAI, []schemas.ProviderPricingOverride{
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
	})
	require.NoError(t, err)

	pricing, ok := mc.getPricing("gpt-4o", "openai", schemas.ResponsesRequest, "", "")
	require.True(t, ok)
	require.NotNil(t, pricing)
	assert.Equal(t, 15.0, pricing.InputCostPerToken)
}

func TestGetPricing_AppliesOverrideAfterFallbackResolution(t *testing.T) {
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
	_, err := setProviderScopedOverrides(mc, schemas.Gemini, []schemas.ProviderPricingOverride{
		{
			ModelPattern:      "gpt-4o",
			MatchType:         schemas.PricingOverrideMatchExact,
			InputCostPerToken: &override,
		},
	})
	require.NoError(t, err)

	pricing, ok := mc.getPricing("gpt-4o", "gemini", schemas.ChatCompletionRequest, "", "")
	require.True(t, ok)
	require.NotNil(t, pricing)
	assert.Equal(t, 7.0, pricing.InputCostPerToken)
}

func TestGetPricing_ExactOverrideDoesNotMatchProviderPrefixedModel(t *testing.T) {
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
	_, err := setProviderScopedOverrides(mc, schemas.OpenAI, []schemas.ProviderPricingOverride{
		{
			ModelPattern:      "gpt-4o",
			MatchType:         schemas.PricingOverrideMatchExact,
			InputCostPerToken: &override,
		},
	})
	require.NoError(t, err)

	pricing, ok := mc.getPricing("openai/gpt-4o", "openai", schemas.ChatCompletionRequest, "", "")
	require.True(t, ok)
	require.NotNil(t, pricing)
	assert.Equal(t, 1.0, pricing.InputCostPerToken)
}

func TestGetPricing_NoMatchingOverrideLeavesPricingUnchanged(t *testing.T) {
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
	_, err := setProviderScopedOverrides(mc, schemas.OpenAI, []schemas.ProviderPricingOverride{
		{
			ModelPattern:      "claude-*",
			MatchType:         schemas.PricingOverrideMatchWildcard,
			InputCostPerToken: &override,
		},
	})
	require.NoError(t, err)

	pricing, ok := mc.getPricing("gpt-4o", "openai", schemas.ChatCompletionRequest, "", "")
	require.True(t, ok)
	require.NotNil(t, pricing)
	assert.Equal(t, 1.0, pricing.InputCostPerToken)
	assert.Equal(t, 2.0, pricing.OutputCostPerToken)
	require.NotNil(t, pricing.CacheReadInputTokenCost)
	assert.Equal(t, 0.4, *pricing.CacheReadInputTokenCost)
}

func TestDeletePricingOverride_StopsApplying(t *testing.T) {
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
	ids, err := setProviderScopedOverrides(mc, schemas.OpenAI, []schemas.ProviderPricingOverride{
		{
			ModelPattern:      "gpt-4o",
			MatchType:         schemas.PricingOverrideMatchExact,
			InputCostPerToken: &override,
		},
	})
	require.NoError(t, err)

	pricing, ok := mc.getPricing("gpt-4o", "openai", schemas.ChatCompletionRequest, "", "")
	require.True(t, ok)
	require.NotNil(t, pricing)
	assert.Equal(t, 11.0, pricing.InputCostPerToken)

	mc.DeletePricingOverride(ids[0])

	pricing, ok = mc.getPricing("gpt-4o", "openai", schemas.ChatCompletionRequest, "", "")
	require.True(t, ok)
	require.NotNil(t, pricing)
	assert.Equal(t, 1.0, pricing.InputCostPerToken)
}

func TestGetPricing_WildcardSpecificityLongerLiteralWins(t *testing.T) {
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
	_, err := setProviderScopedOverrides(mc, schemas.OpenAI, []schemas.ProviderPricingOverride{
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
	})
	require.NoError(t, err)

	pricing, ok := mc.getPricing("gpt-4o-mini", "openai", schemas.ChatCompletionRequest, "", "")
	require.True(t, ok)
	require.NotNil(t, pricing)
	assert.Equal(t, 6.0, pricing.InputCostPerToken)
}

func TestGetPricing_ConfigOrderTiebreakFirstWinsWhenEqual(t *testing.T) {
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
	_, err := setProviderScopedOverrides(mc, schemas.OpenAI, []schemas.ProviderPricingOverride{
		{
			ModelPattern:      "gpt-4o*",
			MatchType:         schemas.PricingOverrideMatchWildcard,
			InputCostPerToken: &first,
		},
		{
			ModelPattern:      "gpt-4o*",
			MatchType:         schemas.PricingOverrideMatchWildcard,
			InputCostPerToken: &second,
		},
	})
	require.NoError(t, err)

	pricing, ok := mc.getPricing("gpt-4o-mini", "openai", schemas.ChatCompletionRequest, "", "")
	require.True(t, ok)
	require.NotNil(t, pricing)
	assert.Equal(t, 8.0, pricing.InputCostPerToken)
}

func TestGetPricing_ScopePriority(t *testing.T) {
	mc := newTestCatalog(nil, nil)
	mc.logger = noOpLogger{}
	mc.pricingData[makeKey("gpt-4o", "openai", "chat")] = configstoreTables.TableModelPricing{
		Model:              "gpt-4o",
		Provider:           "openai",
		Mode:               "chat",
		InputCostPerToken:  1,
		OutputCostPerToken: 2,
	}

	globalValue := 2.0
	providerValue := 3.0
	providerKeyValue := 4.0
	virtualKeyValue := 5.0
	now := time.Unix(1704067200, 0)

	providerScopeID := "openai"
	providerKeyScopeID := "pk_123"
	virtualKeyScopeID := "vk_123"
	require.NoError(t, mc.SetPricingOverrides([]configstoreTables.TablePricingOverride{
		makeScopedOverride("global", now, configstoreTables.PricingOverrideScopeGlobal, "", schemas.ProviderPricingOverride{
			ModelPattern:      "gpt-4o",
			MatchType:         schemas.PricingOverrideMatchExact,
			InputCostPerToken: &globalValue,
		}),
		makeScopedOverride("provider", now.Add(time.Millisecond), configstoreTables.PricingOverrideScopeProvider, providerScopeID, schemas.ProviderPricingOverride{
			ModelPattern:      "gpt-4o",
			MatchType:         schemas.PricingOverrideMatchExact,
			InputCostPerToken: &providerValue,
		}),
		makeScopedOverride("provider-key", now.Add(2*time.Millisecond), configstoreTables.PricingOverrideScopeProviderKey, providerKeyScopeID, schemas.ProviderPricingOverride{
			ModelPattern:      "gpt-4o",
			MatchType:         schemas.PricingOverrideMatchExact,
			InputCostPerToken: &providerKeyValue,
		}),
		makeScopedOverride("virtual-key", now.Add(3*time.Millisecond), configstoreTables.PricingOverrideScopeVirtualKey, virtualKeyScopeID, schemas.ProviderPricingOverride{
			ModelPattern:      "gpt-4o",
			MatchType:         schemas.PricingOverrideMatchExact,
			InputCostPerToken: &virtualKeyValue,
		}),
	}))

	pricing, ok := mc.getPricing("gpt-4o", "openai", schemas.ChatCompletionRequest, providerKeyScopeID, virtualKeyScopeID)
	require.True(t, ok)
	require.NotNil(t, pricing)
	assert.Equal(t, 5.0, pricing.InputCostPerToken)

	pricing, ok = mc.getPricing("gpt-4o", "openai", schemas.ChatCompletionRequest, providerKeyScopeID, "")
	require.True(t, ok)
	require.NotNil(t, pricing)
	assert.Equal(t, 4.0, pricing.InputCostPerToken)

	pricing, ok = mc.getPricing("gpt-4o", "openai", schemas.ChatCompletionRequest, "", "")
	require.True(t, ok)
	require.NotNil(t, pricing)
	assert.Equal(t, 3.0, pricing.InputCostPerToken)
}

func TestGetPricing_GlobalAppliesToAllProviders(t *testing.T) {
	mc := newTestCatalog(nil, nil)
	mc.logger = noOpLogger{}
	mc.pricingData[makeKey("gpt-4o", "openai", "chat")] = configstoreTables.TableModelPricing{
		Model:              "gpt-4o",
		Provider:           "openai",
		Mode:               "chat",
		InputCostPerToken:  1,
		OutputCostPerToken: 2,
	}

	globalValue := 99.0
	now := time.Unix(1704067200, 0)
	require.NoError(t, mc.SetPricingOverrides([]configstoreTables.TablePricingOverride{
		makeScopedOverride("global-1", now, configstoreTables.PricingOverrideScopeGlobal, "", schemas.ProviderPricingOverride{
			ModelPattern:      "gpt-4o",
			MatchType:         schemas.PricingOverrideMatchExact,
			InputCostPerToken: &globalValue,
		}),
	}))

	// Global override applies when no key/VK IDs are provided
	pricing, ok := mc.getPricing("gpt-4o", "openai", schemas.ChatCompletionRequest, "", "")
	require.True(t, ok)
	require.NotNil(t, pricing)
	assert.Equal(t, 99.0, pricing.InputCostPerToken)
	assert.Equal(t, 2.0, pricing.OutputCostPerToken)
}

func TestGetPricing_ProviderScopeOverridesGlobal(t *testing.T) {
	mc := newTestCatalog(nil, nil)
	mc.logger = noOpLogger{}
	mc.pricingData[makeKey("gpt-4o", "openai", "chat")] = configstoreTables.TableModelPricing{
		Model:              "gpt-4o",
		Provider:           "openai",
		Mode:               "chat",
		InputCostPerToken:  1,
		OutputCostPerToken: 2,
	}

	globalValue := 10.0
	providerValue := 20.0
	now := time.Unix(1704067200, 0)
	require.NoError(t, mc.SetPricingOverrides([]configstoreTables.TablePricingOverride{
		makeScopedOverride("global-1", now, configstoreTables.PricingOverrideScopeGlobal, "", schemas.ProviderPricingOverride{
			ModelPattern:      "gpt-4o",
			MatchType:         schemas.PricingOverrideMatchExact,
			InputCostPerToken: &globalValue,
		}),
		makeScopedOverride("provider-1", now.Add(time.Millisecond), configstoreTables.PricingOverrideScopeProvider, "openai", schemas.ProviderPricingOverride{
			ModelPattern:      "gpt-4o",
			MatchType:         schemas.PricingOverrideMatchExact,
			InputCostPerToken: &providerValue,
		}),
	}))

	// Provider scope beats global
	pricing, ok := mc.getPricing("gpt-4o", "openai", schemas.ChatCompletionRequest, "", "")
	require.True(t, ok)
	require.NotNil(t, pricing)
	assert.Equal(t, 20.0, pricing.InputCostPerToken)
}

func TestGetPricing_WithinScopePrecedenceHoldsAcrossScopes(t *testing.T) {
	mc := newTestCatalog(nil, nil)
	mc.logger = noOpLogger{}
	mc.pricingData[makeKey("gpt-4o", "openai", "chat")] = configstoreTables.TableModelPricing{
		Model:              "gpt-4o",
		Provider:           "openai",
		Mode:               "chat",
		InputCostPerToken:  1,
		OutputCostPerToken: 2,
	}

	// Global has an exact match, provider has only a wildcard.
	// The provider scope should win (scope precedence beats match-type precedence)
	// because scope is evaluated first: provider > global.
	globalExact := 10.0
	providerWildcard := 20.0
	now := time.Unix(1704067200, 0)
	require.NoError(t, mc.SetPricingOverrides([]configstoreTables.TablePricingOverride{
		makeScopedOverride("global-exact", now, configstoreTables.PricingOverrideScopeGlobal, "", schemas.ProviderPricingOverride{
			ModelPattern:      "gpt-4o",
			MatchType:         schemas.PricingOverrideMatchExact,
			InputCostPerToken: &globalExact,
		}),
		makeScopedOverride("provider-wildcard", now.Add(time.Millisecond), configstoreTables.PricingOverrideScopeProvider, "openai", schemas.ProviderPricingOverride{
			ModelPattern:      "gpt-*",
			MatchType:         schemas.PricingOverrideMatchWildcard,
			InputCostPerToken: &providerWildcard,
		}),
	}))

	pricing, ok := mc.getPricing("gpt-4o", "openai", schemas.ChatCompletionRequest, "", "")
	require.True(t, ok)
	require.NotNil(t, pricing)
	assert.Equal(t, 20.0, pricing.InputCostPerToken, "provider wildcard should beat global exact due to scope precedence")
}

func TestGetPricing_DisabledOverrideIsSkipped(t *testing.T) {
	mc := newTestCatalog(nil, nil)
	mc.logger = noOpLogger{}
	mc.pricingData[makeKey("gpt-4o", "openai", "chat")] = configstoreTables.TableModelPricing{
		Model:              "gpt-4o",
		Provider:           "openai",
		Mode:               "chat",
		InputCostPerToken:  1,
		OutputCostPerToken: 2,
	}

	disabledValue := 50.0
	enabledValue := 30.0
	now := time.Unix(1704067200, 0)

	disabledScopeID := "openai"
	disabledOverride := makeScopedOverride("disabled-provider", now, configstoreTables.PricingOverrideScopeProvider, disabledScopeID, schemas.ProviderPricingOverride{
		ModelPattern:      "gpt-4o",
		MatchType:         schemas.PricingOverrideMatchExact,
		InputCostPerToken: &disabledValue,
	})
	disabledOverride.Enabled = false

	require.NoError(t, mc.SetPricingOverrides([]configstoreTables.TablePricingOverride{
		disabledOverride,
		makeScopedOverride("enabled-global", now.Add(time.Millisecond), configstoreTables.PricingOverrideScopeGlobal, "", schemas.ProviderPricingOverride{
			ModelPattern:      "gpt-4o",
			MatchType:         schemas.PricingOverrideMatchExact,
			InputCostPerToken: &enabledValue,
		}),
	}))

	// The disabled provider override should be skipped; global should apply
	pricing, ok := mc.getPricing("gpt-4o", "openai", schemas.ChatCompletionRequest, "", "")
	require.True(t, ok)
	require.NotNil(t, pricing)
	assert.Equal(t, 30.0, pricing.InputCostPerToken, "disabled override should be skipped, global should apply")
}

func TestGetPricing_ProviderKeyOverridesProviderAndGlobal(t *testing.T) {
	mc := newTestCatalog(nil, nil)
	mc.logger = noOpLogger{}
	mc.pricingData[makeKey("gpt-4o", "openai", "chat")] = configstoreTables.TableModelPricing{
		Model:              "gpt-4o",
		Provider:           "openai",
		Mode:               "chat",
		InputCostPerToken:  1,
		OutputCostPerToken: 2,
	}

	globalValue := 10.0
	providerValue := 20.0
	keyValue := 30.0
	now := time.Unix(1704067200, 0)

	require.NoError(t, mc.SetPricingOverrides([]configstoreTables.TablePricingOverride{
		makeScopedOverride("global-1", now, configstoreTables.PricingOverrideScopeGlobal, "", schemas.ProviderPricingOverride{
			ModelPattern:      "gpt-4o",
			MatchType:         schemas.PricingOverrideMatchExact,
			InputCostPerToken: &globalValue,
		}),
		makeScopedOverride("provider-1", now.Add(time.Millisecond), configstoreTables.PricingOverrideScopeProvider, "openai", schemas.ProviderPricingOverride{
			ModelPattern:      "gpt-4o",
			MatchType:         schemas.PricingOverrideMatchExact,
			InputCostPerToken: &providerValue,
		}),
		makeScopedOverride("key-1", now.Add(2*time.Millisecond), configstoreTables.PricingOverrideScopeProviderKey, "pk_123", schemas.ProviderPricingOverride{
			ModelPattern:      "gpt-4o",
			MatchType:         schemas.PricingOverrideMatchExact,
			InputCostPerToken: &keyValue,
		}),
	}))

	// With selectedKeyID, provider_key wins over provider and global
	pricing, ok := mc.getPricing("gpt-4o", "openai", schemas.ChatCompletionRequest, "pk_123", "")
	require.True(t, ok)
	require.NotNil(t, pricing)
	assert.Equal(t, 30.0, pricing.InputCostPerToken)

	// Without selectedKeyID, provider wins over global
	pricing, ok = mc.getPricing("gpt-4o", "openai", schemas.ChatCompletionRequest, "", "")
	require.True(t, ok)
	require.NotNil(t, pricing)
	assert.Equal(t, 20.0, pricing.InputCostPerToken)
}

func TestPatchPricing_PartialPatchOnlyChangesSpecifiedFields(t *testing.T) {
	baseCacheRead := 0.4
	baseImageInput := 0.7
	baseImageOutput := 0.8
	base := configstoreTables.TableModelPricing{
		Model:                        "gpt-4o",
		Provider:                     "openai",
		Mode:                         "chat",
		InputCostPerToken:            1,
		OutputCostPerToken:           2,
		CacheReadInputTokenCost:      &baseCacheRead,
		InputCostPerImageToken:       &baseImageInput,
		OutputCostPerImageToken:      &baseImageOutput,
		CacheReadInputImageTokenCost: schemas.Ptr(0.2),
	}

	patched := patchPricing(base, schemas.ProviderPricingOverride{
		ModelPattern:            "gpt-4o",
		MatchType:               schemas.PricingOverrideMatchExact,
		InputCostPerToken:       schemas.Ptr(3.0),
		CacheReadInputTokenCost: schemas.Ptr(0.9),
		OutputCostPerImageToken: schemas.Ptr(1.2),
	})

	// Changed fields
	assert.Equal(t, 3.0, patched.InputCostPerToken)
	require.NotNil(t, patched.CacheReadInputTokenCost)
	assert.Equal(t, 0.9, *patched.CacheReadInputTokenCost)
	require.NotNil(t, patched.OutputCostPerImageToken)
	assert.Equal(t, 1.2, *patched.OutputCostPerImageToken)

	// Unchanged fields
	assert.Equal(t, 2.0, patched.OutputCostPerToken)
	require.NotNil(t, patched.InputCostPerImageToken)
	assert.Equal(t, 0.7, *patched.InputCostPerImageToken)
	require.NotNil(t, patched.CacheReadInputImageTokenCost)
	assert.Equal(t, 0.2, *patched.CacheReadInputImageTokenCost)
}
