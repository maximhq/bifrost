package schemas

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// xAI reports cost as cost_in_usd_ticks (TICKS_IN_USD_CENT = 100_000_000), so the
// 200000000 ticks a grok-imagine call returns is $0.02. These tests pin both the
// factor and the rule that a provider-sent cost object always wins.

func TestNormalizeProviderCost_TicksBecomeCost(t *testing.T) {
	chat := &BifrostLLMUsage{CostInUsdTicks: new(int64(200000000))}
	chat.NormalizeProviderCost()
	require.NotNil(t, chat.Cost)
	assert.Equal(t, 0.02, chat.Cost.TotalCost)

	responses := &ResponsesResponseUsage{CostInUsdTicks: new(int64(200000000))}
	responses.NormalizeProviderCost()
	require.NotNil(t, responses.Cost)
	assert.Equal(t, 0.02, responses.Cost.TotalCost)

	image := &ImageUsage{CostInUsdTicks: new(int64(200000000))}
	image.NormalizeProviderCost()
	require.NotNil(t, image.Cost)
	assert.Equal(t, 0.02, image.Cost.TotalCost)
}

func TestNormalizeProviderCost_DoesNotOverwriteProviderCost(t *testing.T) {
	usage := &BifrostLLMUsage{
		Cost:           &BifrostCost{TotalCost: 0.99},
		CostInUsdTicks: new(int64(200000000)),
	}

	usage.NormalizeProviderCost()
	usage.NormalizeProviderCost() // idempotent

	assert.Equal(t, 0.99, usage.Cost.TotalCost)
}

// Nil, zero and negative tick counts must leave Cost unset so billing falls
// through to datasheet pricing instead of charging nothing.
func TestNormalizeProviderCost_NoCostWithoutUsableTicks(t *testing.T) {
	for name, usage := range map[string]*ImageUsage{
		"nil":      {},
		"zero":     {CostInUsdTicks: new(int64(0))},
		"negative": {CostInUsdTicks: new(int64(-1))},
	} {
		t.Run(name, func(t *testing.T) {
			usage.NormalizeProviderCost()
			assert.Nil(t, usage.Cost)
		})
	}
}

func TestNormalizeProviderCost_NilReceiver(t *testing.T) {
	var chat *BifrostLLMUsage
	var responses *ResponsesResponseUsage
	var image *ImageUsage

	assert.NotPanics(t, func() {
		chat.NormalizeProviderCost()
		responses.NormalizeProviderCost()
		image.NormalizeProviderCost()
	})
}

// The raw xAI field is surfaced alongside the normalized cost — this is what the
// grok-imagine response reported as an empty "usage":{} before the field existed.
func TestImageUsage_CostInUsdTicksRoundTrip(t *testing.T) {
	body := []byte(`{
		"data": [{"url": "https://imgen.x.ai/xai-imgen/xai-tmp-imgen-1.jpeg"}],
		"usage": {"cost_in_usd_ticks": 200000000}
	}`)

	var resp BifrostImageGenerationResponse
	require.NoError(t, json.Unmarshal(body, &resp))
	require.NotNil(t, resp.Usage)
	require.NotNil(t, resp.Usage.CostInUsdTicks)
	assert.Equal(t, int64(200000000), *resp.Usage.CostInUsdTicks)

	resp.Usage.NormalizeProviderCost()

	data, err := json.Marshal(resp.Usage)
	require.NoError(t, err)

	var decoded map[string]interface{}
	require.NoError(t, json.Unmarshal(data, &decoded))
	assert.Equal(t, float64(200000000), decoded["cost_in_usd_ticks"])
	assert.Equal(t, map[string]interface{}{"total_cost": 0.02}, decoded["cost"])
}

// Providers that report no cost keep emitting usage without either key.
func TestImageUsage_CostKeysOmittedWhenAbsent(t *testing.T) {
	data, err := json.Marshal(&ImageUsage{InputTokens: 12, OutputTokens: 4, TotalTokens: 16})
	require.NoError(t, err)

	var decoded map[string]interface{}
	require.NoError(t, json.Unmarshal(data, &decoded))
	assert.NotContains(t, decoded, "cost_in_usd_ticks")
	assert.NotContains(t, decoded, "cost")
}

// DeepCopy promises no shared pointer fields; cost calculation relies on it.
func TestImageUsage_DeepCopyCostFields(t *testing.T) {
	usage := &ImageUsage{CostInUsdTicks: new(int64(200000000))}
	usage.NormalizeProviderCost()

	copied := usage.DeepCopy()
	require.NotNil(t, copied.CostInUsdTicks)
	require.NotNil(t, copied.Cost)
	assert.NotSame(t, usage.CostInUsdTicks, copied.CostInUsdTicks)
	assert.NotSame(t, usage.Cost, copied.Cost)

	*copied.CostInUsdTicks = 1
	copied.Cost.TotalCost = 1

	assert.Equal(t, int64(200000000), *usage.CostInUsdTicks)
	assert.Equal(t, 0.02, usage.Cost.TotalCost)
}

// Both usage shapes must carry the pair across, or a responses-path cost is lost
// the moment it is converted for chat consumers.
func TestUsageConversions_CarryCostFields(t *testing.T) {
	chat := &BifrostLLMUsage{
		Cost:           &BifrostCost{TotalCost: 0.02},
		CostInUsdTicks: new(int64(200000000)),
	}
	converted := chat.ToResponsesResponseUsage()
	require.NotNil(t, converted.CostInUsdTicks)
	assert.Equal(t, int64(200000000), *converted.CostInUsdTicks)
	assert.Equal(t, 0.02, converted.Cost.TotalCost)

	back := converted.ToBifrostLLMUsage()
	require.NotNil(t, back.CostInUsdTicks)
	assert.Equal(t, int64(200000000), *back.CostInUsdTicks)
	assert.Equal(t, 0.02, back.Cost.TotalCost)
}
