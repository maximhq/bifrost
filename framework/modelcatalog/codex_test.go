package modelcatalog

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/require"
)

func TestResolvePricing_CodexIncludedZero(t *testing.T) {
	mc := newTestCatalog(nil, nil)
	mc.codexPricingModes = map[schemas.ModelProvider]schemas.CodexPricingMode{
		schemas.Codex: schemas.CodexPricingModeIncludedZero,
	}
	mc.pricingData[makeKey("gpt-5", "openai", "responses")] = configstoreTables.TableModelPricing{
		Provider:           "openai",
		Model:              "gpt-5",
		Mode:               "responses",
		InputCostPerToken:  1,
		OutputCostPerToken: 2,
	}

	pricing := mc.resolvePricing("codex", "gpt-5", "", schemas.ResponsesRequest)
	require.NotNil(t, pricing)
	require.Equal(t, "codex", pricing.Provider)
	require.Zero(t, pricing.InputCostPerToken)
	require.Zero(t, pricing.OutputCostPerToken)
}

func TestResolvePricing_CodexOpenAIEquivalent(t *testing.T) {
	mc := newTestCatalog(nil, nil)
	mc.codexPricingModes = map[schemas.ModelProvider]schemas.CodexPricingMode{
		schemas.Codex: schemas.CodexPricingModeOpenAIEquivalent,
	}
	mc.pricingData[makeKey("gpt-5", "openai", "responses")] = configstoreTables.TableModelPricing{
		Provider:           "openai",
		Model:              "gpt-5",
		Mode:               "responses",
		InputCostPerToken:  0.25,
		OutputCostPerToken: 0.75,
	}

	pricing := mc.resolvePricing("codex", "gpt-5", "", schemas.ResponsesRequest)
	require.NotNil(t, pricing)
	require.Equal(t, "openai", pricing.Provider)
	require.Equal(t, 0.25, pricing.InputCostPerToken)
	require.Equal(t, 0.75, pricing.OutputCostPerToken)
}
