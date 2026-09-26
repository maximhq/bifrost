package schemas

import (
	"encoding/json"
	"reflect"
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

// BifrostCost.DeepCopy is what ctx.CalculateCostBreakdown hands to plugins, so a
// plugin editing any category must never reach the cost shared with the
// client-facing response.
func TestBifrostCost_DeepCopy(t *testing.T) {
	var nilCost *BifrostCost
	assert.Nil(t, nilCost.DeepCopy())

	src := &BifrostCost{
		InputCost:             3,
		InputCostDetails:      &InputCostDetails{TextCost: 1, CachedReadCost: 1, CachedWriteCost: 1},
		OutputCost:            2,
		OutputCostDetails:     &OutputCostDetails{TextCost: 2},
		AdditionalCost:        1,
		AdditionalCostDetails: &AdditionalCostDetails{GuardrailCost: 1},
		TotalCost:             6,
	}
	copied := src.DeepCopy()
	require.NotNil(t, copied)
	assert.NotSame(t, src, copied)
	assert.NotSame(t, src.InputCostDetails, copied.InputCostDetails)
	assert.NotSame(t, src.OutputCostDetails, copied.OutputCostDetails)
	assert.NotSame(t, src.AdditionalCostDetails, copied.AdditionalCostDetails)
	assert.Equal(t, *src, BifrostCost{
		InputCost:             copied.InputCost,
		InputCostDetails:      src.InputCostDetails,
		OutputCost:            copied.OutputCost,
		OutputCostDetails:     src.OutputCostDetails,
		AdditionalCost:        copied.AdditionalCost,
		AdditionalCostDetails: src.AdditionalCostDetails,
		TotalCost:             copied.TotalCost,
	})
	assert.Equal(t, *src.InputCostDetails, *copied.InputCostDetails)
	assert.Equal(t, *src.OutputCostDetails, *copied.OutputCostDetails)
	assert.Equal(t, *src.AdditionalCostDetails, *copied.AdditionalCostDetails)

	copied.TotalCost = 99
	copied.InputCostDetails.CachedWriteCost = 99
	copied.OutputCostDetails.TextCost = 99
	copied.AdditionalCostDetails.GuardrailCost = 99
	assert.Equal(t, 6.0, src.TotalCost)
	assert.Equal(t, 1.0, src.InputCostDetails.CachedWriteCost)
	assert.Equal(t, 2.0, src.OutputCostDetails.TextCost)
	assert.Equal(t, 1.0, src.AdditionalCostDetails.GuardrailCost)

	// Nil detail pointers stay nil rather than becoming empty structs.
	bare := (&BifrostCost{TotalCost: 1}).DeepCopy()
	require.NotNil(t, bare)
	assert.Nil(t, bare.InputCostDetails)
	assert.Nil(t, bare.OutputCostDetails)
	assert.Nil(t, bare.AdditionalCostDetails)
}

// DeepCopy copies field by field, so a field added to BifrostCost or one of its
// detail structs without a matching line in DeepCopy would silently come back
// zero. This fills every field with a distinct non-zero value through reflection
// and checks each one survived the copy without aliasing, naming the field that
// did not.
func TestBifrostCost_DeepCopyCoversEveryField(t *testing.T) {
	src := &BifrostCost{}
	next := 1.0
	fillDistinct(t, reflect.ValueOf(src).Elem(), "BifrostCost", &next)

	copied := src.DeepCopy()
	require.NotNil(t, copied)
	assertDeepCopied(t, reflect.ValueOf(src).Elem(), reflect.ValueOf(copied).Elem(), "BifrostCost")
}

// fillDistinct sets every float64 to a fresh value and allocates every
// pointer-to-struct, recursing into it. Any other field kind fails the test:
// DeepCopy and this helper both need teaching about it.
func fillDistinct(t *testing.T, v reflect.Value, path string, next *float64) {
	t.Helper()
	for i := 0; i < v.NumField(); i++ {
		f := v.Field(i)
		name := path + "." + v.Type().Field(i).Name
		switch f.Kind() {
		case reflect.Float64:
			f.SetFloat(*next)
			*next++
		case reflect.Ptr:
			if f.Type().Elem().Kind() != reflect.Struct {
				t.Fatalf("%s is a pointer to %s: extend BifrostCost.DeepCopy and this test", name, f.Type().Elem().Kind())
			}
			f.Set(reflect.New(f.Type().Elem()))
			fillDistinct(t, f.Elem(), name, next)
		default:
			t.Fatalf("%s is a %s: extend BifrostCost.DeepCopy and this test", name, f.Kind())
		}
	}
}

// assertDeepCopied checks every field of dst equals src, and that every pointer
// field points at a different allocation, recursing into it.
func assertDeepCopied(t *testing.T, src, dst reflect.Value, path string) {
	t.Helper()
	for i := 0; i < src.NumField(); i++ {
		sf, df := src.Field(i), dst.Field(i)
		name := path + "." + src.Type().Field(i).Name
		switch sf.Kind() {
		case reflect.Float64:
			assert.Equal(t, sf.Float(), df.Float(), "%s was not copied by DeepCopy", name)
		case reflect.Ptr:
			if !assert.False(t, df.IsNil(), "%s came back nil from DeepCopy", name) {
				continue
			}
			assert.NotEqual(t, sf.Pointer(), df.Pointer(), "%s is shared with the source, not copied", name)
			assertDeepCopied(t, sf.Elem(), df.Elem(), name)
		default:
			t.Fatalf("%s is a %s: extend BifrostCost.DeepCopy and this test", name, sf.Kind())
		}
	}
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
