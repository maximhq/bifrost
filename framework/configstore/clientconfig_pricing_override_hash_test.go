package configstore

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGeneratePricingOverrideHash_DifferentFieldsDoNotCollide(t *testing.T) {
	base := tables.TablePricingOverride{
		ID:           "override-1",
		Name:         "pricing-rule",
		Enabled:      true,
		Scope:        tables.PricingOverrideScopeGlobal,
		ModelPattern: "gpt-4.1",
		MatchType:    schemas.PricingOverrideMatchExact,
	}

	a := base
	a.InputCostPerToken = floatPtrForPricingHashTest(1.25)

	b := base
	b.OutputCostPerToken = floatPtrForPricingHashTest(1.25)

	hashA, err := GeneratePricingOverrideHash(a)
	require.NoError(t, err)
	hashB, err := GeneratePricingOverrideHash(b)
	require.NoError(t, err)

	assert.NotEqual(t, hashA, hashB)
}

func TestGeneratePricingOverrideHash_RequestTypeOrderInsensitiveAndDeterministic(t *testing.T) {
	base := tables.TablePricingOverride{
		ID:                "override-2",
		Name:              "pricing-rule-req-types",
		Enabled:           true,
		Scope:             tables.PricingOverrideScopeGlobal,
		ModelPattern:      "gpt-4.1",
		MatchType:         schemas.PricingOverrideMatchExact,
		InputCostPerToken: floatPtrForPricingHashTest(0.000001),
	}

	a := base
	a.RequestTypes = []schemas.RequestType{
		schemas.ChatCompletionRequest,
		schemas.ResponsesRequest,
	}

	b := base
	b.RequestTypes = []schemas.RequestType{
		schemas.ResponsesRequest,
		schemas.ChatCompletionRequest,
	}

	hashA, err := GeneratePricingOverrideHash(a)
	require.NoError(t, err)
	hashB, err := GeneratePricingOverrideHash(b)
	require.NoError(t, err)
	hashA2, err := GeneratePricingOverrideHash(a)
	require.NoError(t, err)

	assert.Equal(t, hashA, hashB)
	assert.Equal(t, hashA, hashA2)
}

func floatPtrForPricingHashTest(v float64) *float64 {
	return &v
}
