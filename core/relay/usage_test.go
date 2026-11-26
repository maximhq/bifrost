package relay

import (
	"math"
	"strings"
	"testing"
	"unicode/utf8"

	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// With nothing mapped, a generic usage reads no LLM-style field: no tokens, no usage.cost and no
// model. A non-LLM API's reply can carry those names with other meanings.
func TestExtractUsageReadsNothingUnmapped(t *testing.T) {
	u := resolveUsage(nil, nil)
	usage := extractUsage(u, "application/json", []byte(`{"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,"cost":0.25}}`), usageFields{})
	assert.Zero(t, usage.TotalTokens)
	assert.Zero(t, usage.Cost)
	assert.Equal(t, "", u.effectiveModelPath())
}

// Token and cost fields are read at the paths a mapping names, whichever shape the API uses.
func TestExtractUsageMappedShapes(t *testing.T) {
	u := resolveUsage(&schemas.RelayUsageConfig{InputTokensPath: "usage.prompt_tokens", OutputTokensPath: "usage.completion_tokens", TotalTokensPath: "usage.total_tokens", CostPath: "usage.cost"}, nil)
	anthropic := resolveUsage(&schemas.RelayUsageConfig{InputTokensPath: "usage.input_tokens", OutputTokensPath: "usage.output_tokens"}, nil)

	t.Run("openai shape", func(t *testing.T) {
		usage := extractUsage(u, "application/json", []byte(`{"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`), usageFields{})
		assert.Equal(t, 10, usage.InputTokens)
		assert.Equal(t, 5, usage.OutputTokens)
		assert.Equal(t, 15, usage.TotalTokens)
		assert.Zero(t, usage.Cost)
	})
	t.Run("anthropic shape sums input and output", func(t *testing.T) {
		usage := extractUsage(anthropic, "application/json; charset=utf-8", []byte(`{"usage":{"input_tokens":3,"output_tokens":4}}`), usageFields{})
		assert.Equal(t, 7, usage.TotalTokens)
	})
	t.Run("cost only from the usage block", func(t *testing.T) {
		usage := extractUsage(u, "application/json", []byte(`{"usage":{"cost":0.25}}`), usageFields{})
		assert.InDelta(t, 0.25, usage.Cost, 1e-9)
		usage = extractUsage(u, "application/json", []byte(`{"cost":1499.99}`), usageFields{})
		assert.Zero(t, usage.Cost, "a root cost is not read when the mapping names usage.cost")
	})
	t.Run("negative numbers are not measurements", func(t *testing.T) {
		usage := extractUsage(u, "application/json", []byte(`{"usage":{"prompt_tokens":-1000000,"completion_tokens":4,"cost":-5}}`), usageFields{})
		assert.Equal(t, 0, usage.InputTokens)
		assert.Equal(t, 4, usage.TotalTokens)
		assert.Zero(t, usage.Cost)
		fee := resolveUsage(&schemas.RelayUsageConfig{CostPath: "usage.cost", CostPerRequest: schemas.Ptr(0.01)}, nil)
		usage = extractUsage(fee, "application/json", []byte(`{"usage":{"cost":-5}}`), usageFields{})
		assert.InDelta(t, 0.01, usage.Cost, 1e-9, "a negative upstream cost cannot cancel the flat fee")
	})
	t.Run("aws json media type is json", func(t *testing.T) {
		usage := extractUsage(u, "application/x-amz-json-1.1", []byte(`{"usage":{"total_tokens":3}}`), usageFields{})
		assert.Equal(t, 3, usage.TotalTokens)
	})
	t.Run("nothing found is still a non-nil usage", func(t *testing.T) {
		usage := extractUsage(u, "application/json", []byte(`{"results":[]}`), usageFields{})
		require.NotNil(t, usage)
		assert.Zero(t, usage.TotalTokens)
		assert.Zero(t, usage.Cost)
	})
	t.Run("non-json content type is not scanned", func(t *testing.T) {
		usage := extractUsage(u, "text/plain", []byte(`{"usage":{"total_tokens":9}}`), usageFields{})
		assert.Zero(t, usage.TotalTokens)
	})
	t.Run("missing content type falls back to sniffing json", func(t *testing.T) {
		usage := extractUsage(u, "", []byte(`{"usage":{"total_tokens":9}}`), usageFields{})
		assert.Equal(t, 9, usage.TotalTokens)
	})
	t.Run("zero tokens is no usage", func(t *testing.T) {
		usage := extractUsage(u, "application/json", []byte(`{"usage":{"total_tokens":0}}`), usageFields{})
		assert.Zero(t, usage.TotalTokens)
	})
}

func TestExtractUsageConfiguredPaths(t *testing.T) {
	u := resolveUsage(&schemas.RelayUsageConfig{
		InputTokensPath:  "meta.in",
		OutputTokensPath: "meta.out",
		CostPath:         "data.#.cost",
		CostPerRequest:   schemas.Ptr(0.01),
	}, nil)
	usage := extractUsage(u, "application/json", []byte(`{"meta":{"in":2,"out":3},"data":[{"cost":0.1},{"cost":0.2},{"cost":"n/a"}],"usage":{"total_tokens":99}}`), usageFields{})
	assert.Equal(t, 5, usage.TotalTokens, "configured input/output paths win and total is derived")
	assert.InDelta(t, 0.31, usage.Cost, 1e-9, "array costs are summed and the per-request fee is added")

	t.Run("only the configured paths are read", func(t *testing.T) {
		usage := extractUsage(u, "application/json", []byte(`{"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`), usageFields{})
		assert.Zero(t, usage.TotalTokens, "a usage block the mapping does not name is not read")
	})
}

func TestExtractUsageFeeOnly(t *testing.T) {
	u := resolveUsage(&schemas.RelayUsageConfig{CostPerRequest: schemas.Ptr(0.004)}, nil)
	usage := extractUsage(u, "application/json", []byte(`{"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2,"cost":9},"cost":5}`), usageFields{})
	assert.Zero(t, usage.TotalTokens)
	assert.InDelta(t, 0.004, usage.Cost, 1e-9, "only the flat fee: nothing unmapped is read")
}

func TestResolveUsageRouteOverride(t *testing.T) {
	base := &schemas.RelayUsageConfig{ModelPath: "model", CostPath: "cost", CostPerRequest: schemas.Ptr(0.01)}
	override := &schemas.RelayUsageConfig{CostPerRequest: schemas.Ptr(0.05), TotalTokensPath: "tokens"}
	u := resolveUsage(base, override)
	assert.Equal(t, "model", u.modelPath, "untouched fields come from the provider")
	assert.Equal(t, "cost", u.costPath)
	assert.Equal(t, "tokens", u.totalTokensPath)
	assert.InDelta(t, 0.05, u.costPerRequest, 1e-9, "the route's fee replaces the provider's")
}

func TestStreamUsageObservers(t *testing.T) {
	u := resolveUsage(&schemas.RelayUsageConfig{InputTokensPath: "usage.input_tokens", OutputTokensPath: "usage.output_tokens", TotalTokensPath: "usage.total_tokens", CostPath: "usage.cost", CostPerRequest: schemas.Ptr(0.01)}, nil)
	assert.False(t, u.hasUsageEvent([]byte(`{"delta":"x"}`)))
	assert.True(t, u.hasUsageEvent([]byte(`{"usage":{"total_tokens":1}}`)))
	assert.True(t, u.hasUsageEvent([]byte(`{"usage":{"output_tokens":5}}`)), "an output-only event is worth parsing")
	assert.True(t, u.hasUsageEvent([]byte(`{"usage":{"cost":0.2}}`)))
	assert.False(t, u.hasUsageEvent([]byte(`{"cost":0.2}`)), "a root cost is not a usage field")

	observe := u.streamObserver(usageFields{})
	assert.Nil(t, observe([]byte(`{"delta":"x"}`)), "a content-only event reports nothing and clears nothing")

	// The Anthropic shape: input tokens on the first event, output tokens on the last.
	first := observe([]byte(`{"usage":{"input_tokens":12}}`))
	require.NotNil(t, first)
	assert.Equal(t, 12, first.TotalTokens)
	assert.Nil(t, observe([]byte(`{"delta":"more"}`)))
	last := observe([]byte(`{"usage":{"output_tokens":5}}`))
	require.NotNil(t, last)
	assert.Equal(t, 12, last.InputTokens)
	assert.Equal(t, 5, last.OutputTokens)
	assert.Equal(t, 17, last.TotalTokens, "fields merge across events")
	assert.InDelta(t, 0.01, last.Cost, 1e-9)

	// A later total wins over the derived sum.
	withTotal := observe([]byte(`{"usage":{"total_tokens":20}}`))
	require.NotNil(t, withTotal)
	assert.Equal(t, 20, withTotal.TotalTokens)

	fallback := u.defaultUsage(usageFields{})
	require.NotNil(t, fallback)
	assert.Zero(t, fallback.TotalTokens)
	assert.InDelta(t, 0.01, fallback.Cost, 1e-9)
	assert.Zero(t, resolveUsage(nil, nil).defaultUsage(usageFields{}).Cost, "no fee means an empty usage")
}

func TestEffectiveModelPath(t *testing.T) {
	assert.Equal(t, "", resolveUsage(nil, nil).effectiveModelPath(), "no model is read unless mapped")
	assert.Equal(t, "engine", resolveUsage(&schemas.RelayUsageConfig{ModelPath: "engine"}, nil).effectiveModelPath())
	assert.Equal(t, "name", resolveUsage(nil, &schemas.RelayUsageConfig{ModelPath: "name"}).effectiveModelPath())
}

// A quantity or a sum that overflows float64 is not a price: the row keeps the raw reading and
// bills what is representable, so the logging hooks never see +Inf.
func TestBuildKeepsUsageFinite(t *testing.T) {
	huge := resolveUsage(&schemas.RelayUsageConfig{CostPerUnit: schemas.Ptr(1.0), UnitsBlockSize: schemas.Ptr(0.1)}, nil)
	got := huge.build(usageFields{hasUnits: true, units: 1e308})
	require.NotNil(t, got.Units)
	assert.Nil(t, got.BilledUnits, "a block count that overflows is not stored")
	assert.False(t, math.IsInf(got.Cost, 0) || math.IsNaN(got.Cost), "cost stays finite")

	sum := resolveUsage(&schemas.RelayUsageConfig{CostPerRequest: schemas.Ptr(1e308), CostPerUnit: schemas.Ptr(1e308)}, nil)
	got = sum.build(usageFields{hasUnits: true, units: 1})
	assert.Equal(t, 1e308, got.Cost, "a unit charge whose sum with the fee overflows is dropped, the fee stands")
	assert.False(t, math.IsInf(got.Cost, 0))
}

// A batch body cannot dodge a request-side unit charge with a case-variant key in one element.
func TestRequestBodyErrorBatchCaseVariant(t *testing.T) {
	u := resolveUsage(&schemas.RelayUsageConfig{CostPerUnit: schemas.Ptr(0.001), UnitsPath: "#.Text", UnitsSource: schemas.RelayUnitsFromRequest, UnitsMeasure: schemas.RelayUnitsChars}, nil)
	assert.NoError(t, u.requestBodyError([]byte(`[{"Text":"a"},{"Text":"b"}]`)))
	assert.Error(t, u.requestBodyError([]byte(`[{"text":"hidden"}]`)), "a lone case-variant element is refused")
	assert.Error(t, u.requestBodyError([]byte(`[{"Text":"a"},{"text":"hidden"}]`)), "a mixed body is refused, since one element would go unbilled")
	assert.NoError(t, u.requestBodyError([]byte(`[{"Other":"x"}]`)), "a key that is simply absent is not a case variant")
	nested := resolveUsage(&schemas.RelayUsageConfig{CostPerUnit: schemas.Ptr(0.001), UnitsPath: "docs.#.Text", UnitsSource: schemas.RelayUnitsFromRequest, UnitsMeasure: schemas.RelayUnitsChars}, nil)
	assert.NoError(t, nested.requestBodyError([]byte(`{"docs":[{"Text":"a"}]}`)))
	assert.Error(t, nested.requestBodyError([]byte(`{"Docs":[{"Text":"hidden"}]}`)), "a case variant on the path to the array is refused too")
	assert.NoError(t, nested.requestBodyError([]byte(`{"other":[{"Text":"a"}]}`)), "an absent array is not a case variant")
}

func TestUnitsPricing(t *testing.T) {
	t.Run("response value: Textract pages", func(t *testing.T) {
		u := resolveUsage(&schemas.RelayUsageConfig{UnitsPath: "DocumentMetadata.Pages", CostPerUnit: schemas.Ptr(0.0015)}, nil)
		usage := extractUsage(u, "application/x-amz-json-1.1", []byte(`{"Blocks":[],"DocumentMetadata":{"Pages":7}}`), u.requestUnits([]byte(`{"Document":{}}`)))
		assert.InDelta(t, 7*0.0015, usage.Cost, 1e-9)
		assert.Zero(t, usage.TotalTokens)
	})
	t.Run("request chars: Translate text", func(t *testing.T) {
		u := resolveUsage(&schemas.RelayUsageConfig{UnitsPath: "Text", UnitsSource: schemas.RelayUnitsFromRequest, UnitsMeasure: schemas.RelayUnitsChars, CostPerUnit: schemas.Ptr(15.0 / 1_000_000)}, nil)
		initial := u.requestUnits([]byte(`{"Text":"héllo wörld","SourceLanguageCode":"en","TargetLanguageCode":"de"}`))
		assert.True(t, initial.hasUnits)
		assert.InDelta(t, 11, initial.units, 0, "characters, not bytes")
		usage := extractUsage(u, "application/x-amz-json-1.1", []byte(`{"TranslatedText":"hallo welt"}`), initial)
		assert.InDelta(t, 11*15.0/1_000_000, usage.Cost, 1e-12)
	})
	t.Run("request body bytes: TTS markup with an audio response", func(t *testing.T) {
		u := resolveUsage(&schemas.RelayUsageConfig{UnitsSource: schemas.RelayUnitsFromRequest, UnitsMeasure: schemas.RelayUnitsBodyBytes, CostPerUnit: schemas.Ptr(16.0 / 1_000_000)}, nil)
		ssml := []byte(`<speak version='1.0' xml:lang='en-US'><voice name='en-US-JennyNeural'>Hi</voice></speak>`)
		initial := u.requestUnits(ssml)
		assert.True(t, initial.hasUnits)
		assert.InDelta(t, float64(len(ssml)), initial.units, 0)
		usage := extractUsage(u, "audio/mpeg", []byte("ID3binary"), initial)
		assert.InDelta(t, float64(len(ssml))*16.0/1_000_000, usage.Cost, 1e-12)
		assert.Equal(t, usage.Cost, u.defaultUsage(initial).Cost, "the streaming fallback carries the same request-side quantity")
	})
	t.Run("rounding: per started block with a minimum (Comprehend Medical shape)", func(t *testing.T) {
		u := resolveUsage(&schemas.RelayUsageConfig{UnitsPath: "Text", UnitsSource: schemas.RelayUnitsFromRequest, UnitsMeasure: schemas.RelayUnitsChars, UnitsBlockSize: schemas.Ptr(100.0), UnitsMinimum: schemas.Ptr(1.0), CostPerUnit: schemas.Ptr(0.01)}, nil)
		assert.InDelta(t, 3, u.billedUnits(250), 0, "250 characters start three 100-character blocks")
		assert.InDelta(t, 1, u.billedUnits(100), 0, "an exact block is one")
		assert.InDelta(t, 1, u.billedUnits(0), 0, "the minimum applies once a quantity was read")
		initial := u.requestUnits([]byte(`{"Text":"` + strings.Repeat("a", 250) + `"}`))
		require.True(t, initial.hasUnits)
		usage := extractUsage(u, "application/x-amz-json-1.1", []byte(`{"Entities":[]}`), initial)
		assert.InDelta(t, 0.03, usage.Cost, 1e-9)
		exact := resolveUsage(&schemas.RelayUsageConfig{UnitsPath: "Text", UnitsSource: schemas.RelayUnitsFromRequest, UnitsMeasure: schemas.RelayUnitsChars, CostPerUnit: schemas.Ptr(0.01)}, nil)
		assert.InDelta(t, 250, exact.billedUnits(250), 0, "without a block size the raw quantity is billed exactly")
		assert.InDelta(t, 0, exact.billedUnits(0), 0, "and no minimum applies")
	})
	t.Run("request body chars: multi-byte SSML billed per character, not per byte", func(t *testing.T) {
		u := resolveUsage(&schemas.RelayUsageConfig{UnitsSource: schemas.RelayUnitsFromRequest, UnitsMeasure: schemas.RelayUnitsBodyChars, CostPerUnit: schemas.Ptr(16.0 / 1_000_000)}, nil)
		ssml := []byte(`<speak xml:lang='ja-JP'><voice name='ja-JP-NanamiNeural'>こんにちは、世界</voice></speak>`)
		require.Greater(t, len(ssml), utf8.RuneCount(ssml), "the fixture must contain multi-byte characters")
		initial := u.requestUnits(ssml)
		assert.True(t, initial.hasUnits)
		assert.InDelta(t, float64(utf8.RuneCount(ssml)), initial.units, 0, "characters, not bytes")
		usage := extractUsage(u, "audio/mpeg", []byte("ID3binary"), initial)
		assert.InDelta(t, float64(utf8.RuneCount(ssml))*16.0/1_000_000, usage.Cost, 1e-12)
		plain := resolveUsage(&schemas.RelayUsageConfig{UnitsSource: schemas.RelayUnitsFromRequest, UnitsMeasure: schemas.RelayUnitsBodyChars, CostPerUnit: schemas.Ptr(1.0)}, nil)
		assert.InDelta(t, 11, plain.requestUnits([]byte("héllo wörld")).units, 0, "any content type")
	})
	t.Run("units add to fee and extracted cost", func(t *testing.T) {
		u := resolveUsage(&schemas.RelayUsageConfig{UnitsPath: "usage.credits", CostPerUnit: schemas.Ptr(0.008), CostPerRequest: schemas.Ptr(0.001), CostPath: "usage.cost"}, nil)
		usage := extractUsage(u, "application/json", []byte(`{"usage":{"credits":2,"cost":0.5}}`), usageFields{})
		assert.InDelta(t, 2*0.008+0.001+0.5, usage.Cost, 1e-9)
	})
	t.Run("missing or negative quantity bills nothing extra", func(t *testing.T) {
		u := resolveUsage(&schemas.RelayUsageConfig{UnitsPath: "pages", CostPerUnit: schemas.Ptr(1.0), CostPerRequest: schemas.Ptr(0.01)}, nil)
		usage := extractUsage(u, "application/json", []byte(`{"pages":-3}`), usageFields{})
		assert.InDelta(t, 0.01, usage.Cost, 1e-9)
		usage = extractUsage(u, "application/json", []byte(`{"other":1}`), usageFields{})
		assert.InDelta(t, 0.01, usage.Cost, 1e-9)
		usage = extractUsage(u, "text/plain", []byte(`pages: 3`), usageFields{})
		assert.InDelta(t, 0.01, usage.Cost, 1e-9, "a non-JSON response has no value to read")
	})
	t.Run("no rate means units are ignored", func(t *testing.T) {
		u := resolveUsage(&schemas.RelayUsageConfig{UnitsPath: "pages"}, nil)
		assert.False(t, u.billsUnits())
		usage := extractUsage(u, "application/json", []byte(`{"pages":3}`), usageFields{})
		assert.Zero(t, usage.Cost)
	})
	t.Run("streams observe response units and keep request units", func(t *testing.T) {
		u := resolveUsage(&schemas.RelayUsageConfig{UnitsPath: "pages", CostPerUnit: schemas.Ptr(0.5)}, nil)
		assert.True(t, u.hasUsageEvent([]byte(`{"pages":2}`)))
		observe := u.streamObserver(usageFields{})
		assert.Nil(t, observe([]byte(`{"delta":"x"}`)))
		got := observe([]byte(`{"pages":2}`))
		require.NotNil(t, got)
		assert.InDelta(t, 1.0, got.Cost, 1e-9)

		req := resolveUsage(&schemas.RelayUsageConfig{TotalTokensPath: "usage.total_tokens", UnitsPath: "text", UnitsSource: schemas.RelayUnitsFromRequest, UnitsMeasure: schemas.RelayUnitsChars, CostPerUnit: schemas.Ptr(0.1)}, nil)
		initial := req.requestUnits([]byte(`{"text":"abcd"}`))
		observe = req.streamObserver(initial)
		got = observe([]byte(`{"usage":{"total_tokens":3}}`))
		require.NotNil(t, got)
		assert.InDelta(t, 0.4, got.Cost, 1e-9, "request units survive response events")
		assert.Equal(t, 3, got.TotalTokens)
	})
	t.Run("streams never measure a whole-body response quantity per event", func(t *testing.T) {
		u := resolveUsage(&schemas.RelayUsageConfig{TotalTokensPath: "usage.total_tokens", UnitsMeasure: schemas.RelayUnitsBodyChars, CostPerUnit: schemas.Ptr(1.0), CostPerRequest: schemas.Ptr(0.01)}, nil)
		observe := u.streamObserver(usageFields{})
		got := observe([]byte(`{"usage":{"total_tokens":3}}`))
		require.NotNil(t, got)
		assert.InDelta(t, 0.01, got.Cost, 1e-9, "the event's size is not the body's: only the fee is billed")
		assert.Equal(t, 3, got.TotalTokens)
		assert.Nil(t, observe([]byte(`{"delta":"x"}`)), "an event without usage fields still reports nothing")
	})
	t.Run("an array at the path is summed: a batch body bills every element", func(t *testing.T) {
		u := resolveUsage(&schemas.RelayUsageConfig{UnitsPath: "#.Text", UnitsSource: schemas.RelayUnitsFromRequest, UnitsMeasure: schemas.RelayUnitsChars, CostPerUnit: schemas.Ptr(1.0)}, nil)
		got := u.requestUnits([]byte(`[{"Text":"abc"},{"Text":"héllo"},{"Other":1}]`))
		require.True(t, got.hasUnits)
		assert.InDelta(t, 8, got.units, 0)
		pages := resolveUsage(&schemas.RelayUsageConfig{UnitsPath: "docs.#.pages", CostPerUnit: schemas.Ptr(1.0)}, nil)
		usage := extractUsage(pages, "application/json", []byte(`{"docs":[{"pages":2},{"pages":5},{"pages":-1}]}`), usageFields{})
		assert.InDelta(t, 7, usage.Cost, 1e-9, "negative elements are not measurements")
	})
	t.Run("an empty body is a measured zero, so the minimum applies", func(t *testing.T) {
		u := resolveUsage(&schemas.RelayUsageConfig{UnitsSource: schemas.RelayUnitsFromRequest, UnitsMeasure: schemas.RelayUnitsBodyChars, UnitsMinimum: schemas.Ptr(1.0), CostPerUnit: schemas.Ptr(0.5)}, nil)
		got := u.requestUnits(nil)
		require.True(t, got.hasUnits)
		usage := extractUsage(u, "audio/mpeg", nil, got)
		assert.InDelta(t, 0.5, usage.Cost, 1e-9)
		resp := resolveUsage(&schemas.RelayUsageConfig{UnitsMeasure: schemas.RelayUnitsBodyBytes, UnitsMinimum: schemas.Ptr(2.0), CostPerUnit: schemas.Ptr(1.0)}, nil)
		usage = extractUsage(resp, "application/octet-stream", nil, usageFields{})
		require.NotNil(t, usage.Cost, "an empty buffered response is measured too")
		assert.InDelta(t, 2, usage.Cost, 1e-9)
	})
	t.Run("an overflowing quantity is not a measurement", func(t *testing.T) {
		u := resolveUsage(&schemas.RelayUsageConfig{UnitsPath: "pages", CostPerUnit: schemas.Ptr(1.0), CostPerRequest: schemas.Ptr(0.01)}, nil)
		usage := extractUsage(u, "application/json", []byte(`{"pages":1e400}`), usageFields{})
		assert.InDelta(t, 0.01, usage.Cost, 1e-9)
		sum := resolveUsage(&schemas.RelayUsageConfig{UnitsPath: "#.n", CostPerUnit: schemas.Ptr(1.0)}, nil)
		usage = extractUsage(sum, "application/json", []byte(`[{"n":1e308},{"n":1e308}]`), usageFields{})
		assert.Zero(t, usage.Cost, "a sum that overflows bills nothing rather than +Inf")
		cost := resolveUsage(&schemas.RelayUsageConfig{CostPath: "usage.cost"}, nil)
		assert.Zero(t, extractUsage(cost, "application/json", []byte(`{"usage":{"cost":1e400}}`), usageFields{}).Cost)
		costs := resolveUsage(&schemas.RelayUsageConfig{CostPath: "costs"}, nil)
		usage = extractUsage(costs, "application/json", []byte(`{"costs":[1e308,1e308]}`), usageFields{})
		assert.Zero(t, usage.Cost, "a reported cost array that sums to +Inf is not a price")
		assert.Nil(t, usage.ExtractedCost)
	})
	t.Run("a charge that overflows is refused on the request side and dropped on the response side", func(t *testing.T) {
		req := resolveUsage(&schemas.RelayUsageConfig{UnitsPath: "n", UnitsSource: schemas.RelayUnitsFromRequest, CostPerUnit: schemas.Ptr(10.0)}, nil)
		assert.Error(t, req.requestBodyError([]byte(`{"n":1e308}`)), "the caller's quantity times the rate overflows")
		assert.NoError(t, req.requestBodyError([]byte(`{"n":5}`)))
		resp := resolveUsage(&schemas.RelayUsageConfig{UnitsPath: "n", CostPerUnit: schemas.Ptr(10.0), CostPerRequest: schemas.Ptr(0.01)}, nil)
		usage := extractUsage(resp, "application/json", []byte(`{"n":1e308}`), usageFields{})
		assert.InDelta(t, 0.01, usage.Cost, 1e-9, "the fee stands, the overflowing unit charge does not")
	})
	t.Run("route overrides units", func(t *testing.T) {
		base := &schemas.RelayUsageConfig{UnitsPath: "pages", CostPerUnit: schemas.Ptr(0.5)}
		u := resolveUsage(base, &schemas.RelayUsageConfig{UnitsPath: "usage.credits", CostPerUnit: schemas.Ptr(0.0)})
		assert.Equal(t, "usage.credits", u.unitsPath)
		assert.False(t, u.billsUnits(), "a route can switch unit pricing off with a zero rate")
	})
}

// BenchmarkHasUsageEvent is the per-event gate on a generic SSE stream: it must not allocate.
func BenchmarkHasUsageEvent(b *testing.B) {
	u := resolveUsage(&schemas.RelayUsageConfig{CostPerRequest: schemas.Ptr(0.01), UnitsPath: "pages", CostPerUnit: schemas.Ptr(0.1)}, nil)
	event := []byte(`{"id":"evt_1","type":"delta","delta":{"text":"hello world"},"index":3}`)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if u.hasUsageEvent(event) {
			b.Fatal("delta event must not look like usage")
		}
	}
}

func TestReadModel(t *testing.T) {
	model := func(body, contentType, path string) string {
		t.Helper()
		m, err := ReadModel([]byte(body), contentType, path)
		require.NoError(t, err)
		return m
	}
	assert.Equal(t, "gpt-4o", model(`{"model":"gpt-4o"}`, "application/json", "model"))
	assert.Equal(t, "fast", model(`{"engine":"fast"}`, "application/json", "engine"))
	assert.Equal(t, "", model(`{"model":7}`, "application/json", "model"), "a non-string model is no model, not an error")
	assert.Equal(t, "", model(`{"model":"x"}`, "text/plain", "model"), "a declared non-JSON body is not read")
	assert.Equal(t, "x", model(`{"model":"x"}`, "", "model"), "no content type sniffs JSON")
	assert.Equal(t, "x", model(`{"model":"x"}`, "application/x-amz-json-1.1", "model"), "AWS JSON is JSON")
	assert.Equal(t, "", model(`{"model":"x"}`, "application/json", ""), "an empty path reads no model")
	assert.Equal(t, "", model(``, "application/json", "model"))
	assert.Equal(t, "", model(`{"Model":"large"}`, "application/json", "model"), "a lone case variant is no model, not an error")
	assert.Equal(t, "b", model(`{"cfg":{"name":"b"},"name":"a","Name":"z"}`, "application/json", "cfg.name"), "only the objects along the path are checked")

	for name, tc := range map[string]struct{ body, path string }{
		"duplicate":                  {`{"model":"small","model":"large"}`, "model"},
		"case variant":               {`{"model":"small","Model":"large"}`, "model"},
		"nested duplicate":           {`{"cfg":{"name":"small","NAME":"large"}}`, "cfg.name"},
		"duplicate parent object":    {`{"cfg":{"name":"small"},"cfg":{"name":"large"}}`, "cfg.name"},
		"case-variant parent object": {`{"cfg":{"name":"small"},"Cfg":{"name":"large"}}`, "cfg.name"},
	} {
		_, err := ReadModel([]byte(tc.body), "application/json", tc.path)
		assert.Errorf(t, err, "%s must be refused", name)
	}
}

// TestRequestBodyError pins what a route billing on a path in the caller's body accepts: JSON
// whatever Content-Type the caller declares, and nothing ambiguous.
func TestRequestBodyError(t *testing.T) {
	translate := resolveUsage(&schemas.RelayUsageConfig{UnitsPath: "Text", UnitsSource: schemas.RelayUnitsFromRequest, UnitsMeasure: schemas.RelayUnitsChars, CostPerUnit: schemas.Ptr(0.001)}, nil)
	assert.NoError(t, translate.requestBodyError([]byte(`{"Text":"hello","Lang":"de"}`)))
	got := translate.requestUnits([]byte(`{"Text":"hello"}`))
	require.True(t, got.hasUnits, "the body is measured as JSON even when the caller labels it text/plain")
	assert.InDelta(t, 5, got.units, 0)

	for name, body := range map[string]string{
		"not JSON":                     `Text=hello`,
		"empty":                        ``,
		"duplicate key":                `{"Text":"a","Text":"` + strings.Repeat("b", 100) + `"}`,
		"case variant":                 `{"Text":"a","text":"` + strings.Repeat("b", 100) + `"}`,
		"lone case variant":            `{"text":"` + strings.Repeat("b", 100) + `"}`,
		"duplicate inside an array":    `[{"Text":"a"},{"Text":"a","TEXT":"b"}]`,
		"duplicate in a nested object": `{"Options":{"x":1,"X":2},"Text":"a"}`,
		"too deep":                     strings.Repeat(`{"a":`, 70) + `1` + strings.Repeat(`}`, 70),
	} {
		assert.Errorf(t, translate.requestBodyError([]byte(body)), "%s must be refused", name)
	}

	tts := resolveUsage(&schemas.RelayUsageConfig{UnitsSource: schemas.RelayUnitsFromRequest, UnitsMeasure: schemas.RelayUnitsBodyChars, CostPerUnit: schemas.Ptr(0.001)}, nil)
	assert.NoError(t, tts.requestBodyError([]byte(`<speak>hi</speak>`)), "a whole-body measure reads any body")
	pages := resolveUsage(&schemas.RelayUsageConfig{UnitsPath: "Pages", CostPerUnit: schemas.Ptr(0.001)}, nil)
	assert.NoError(t, pages.requestBodyError([]byte(`not json`)), "response-side units do not constrain the request")
	assert.NoError(t, resolveUsage(nil, nil).requestBodyError([]byte(`{"a":1,"a":2}`)), "no unit pricing, no check")
}

// A generic usage always reports its mapping charge (fee, units and any upstream-reported cost), which
// marks the row as a generic relay's and is what cost recalculation keeps.
func TestBuildReportsCostBreakdown(t *testing.T) {
	u := resolveUsage(&schemas.RelayUsageConfig{CostPerRequest: schemas.Ptr(0.01), UnitsPath: "n", CostPerUnit: schemas.Ptr(0.002), UnitsBlockSize: schemas.Ptr(10.0), CostPath: "c"}, nil)
	usage := u.build(usageFields{input: 10, output: 5, hasInput: true, hasOutput: true, units: 25, hasUnits: true, cost: 0.1, hasCost: true})
	assert.InDelta(t, 0.01+0.1+3*0.002, usage.Cost, 1e-12, "fee + reported cost + ceil(25/10) units * rate")
	assert.InDelta(t, 0.01, usage.CostPerRequest, 1e-12)
	assert.InDelta(t, 0.002, usage.CostPerUnit, 1e-12)
	require.NotNil(t, usage.Units)
	assert.InDelta(t, 25, *usage.Units, 1e-12)
	require.NotNil(t, usage.BilledUnits)
	assert.InDelta(t, 3, *usage.BilledUnits, 1e-12)
	require.NotNil(t, usage.ExtractedCost)
	assert.InDelta(t, 0.1, *usage.ExtractedCost, 1e-12)
	assert.Equal(t, 15, usage.TotalTokens, "tokens are carried, never priced")

	free := resolveUsage(&schemas.RelayUsageConfig{}, nil).build(usageFields{})
	assert.Zero(t, free.Cost, "a zero charge is still a usage: the call counts one request")
	assert.Nil(t, free.Units)
}

// The model is read where the mapping says: a body field by default, or a query parameter
// (Deepgram's ?model=nova-3). A parameter named twice is refused like a key named twice.
func TestReadRequestModel(t *testing.T) {
	body := []byte(`{"model_id":"eleven_multilingual_v2"}`)
	got, err := ReadRequestModel("", body, "application/json", "model=nova-3", "model_id")
	require.NoError(t, err)
	assert.Equal(t, "eleven_multilingual_v2", got, "body is the default source")

	got, err = ReadRequestModel(schemas.RelayModelFromQuery, body, "application/json", "model=nova-3&language=en", "model")
	require.NoError(t, err)
	assert.Equal(t, "nova-3", got)

	got, err = ReadRequestModel(schemas.RelayModelFromQuery, nil, "", "model=nova%2D3", "model")
	require.NoError(t, err)
	assert.Equal(t, "nova-3", got, "the value is unescaped")

	got, err = ReadRequestModel(schemas.RelayModelFromQuery, nil, "", "language=en", "model")
	require.NoError(t, err)
	assert.Empty(t, got)

	_, err = ReadRequestModel(schemas.RelayModelFromQuery, nil, "", "model=a&model=b", "model")
	require.Error(t, err, "a model named twice in the query is refused")

	_, err = ReadRequestModel(schemas.RelayModelFromQuery, nil, "", "model=a&model=b&x=%zz", "model")
	require.Error(t, err, "a malformed query is refused, not read as naming no model")

	got, err = ReadRequestModel(schemas.RelayModelFromQuery, nil, "", "model=nova-3", "")
	require.NoError(t, err)
	assert.Empty(t, got, "no path reads nothing")
}
