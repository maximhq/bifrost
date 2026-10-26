package generic

import (
	"strings"
	"testing"
	"unicode/utf8"

	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExtractUsageOptimisticDefaults(t *testing.T) {
	u := resolveUsage(nil, nil)

	t.Run("openai shape", func(t *testing.T) {
		usage := extractUsage(u, "application/json", []byte(`{"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`), usageFields{})
		require.NotNil(t, usage.LLMUsage)
		assert.Equal(t, 10, usage.LLMUsage.PromptTokens)
		assert.Equal(t, 5, usage.LLMUsage.CompletionTokens)
		assert.Equal(t, 15, usage.LLMUsage.TotalTokens)
		assert.Nil(t, usage.Cost)
	})
	t.Run("anthropic shape sums input and output", func(t *testing.T) {
		usage := extractUsage(u, "application/json; charset=utf-8", []byte(`{"usage":{"input_tokens":3,"output_tokens":4}}`), usageFields{})
		require.NotNil(t, usage.LLMUsage)
		assert.Equal(t, 7, usage.LLMUsage.TotalTokens)
	})
	t.Run("cost only from the usage block", func(t *testing.T) {
		usage := extractUsage(u, "application/json", []byte(`{"usage":{"cost":0.25}}`), usageFields{})
		require.NotNil(t, usage.Cost)
		assert.InDelta(t, 0.25, usage.Cost.TotalCost, 1e-9)
		usage = extractUsage(u, "application/json", []byte(`{"cost":1499.99}`), usageFields{})
		assert.Nil(t, usage.Cost, "a root cost is as likely a product price as a bill; it is never read by default")
	})
	t.Run("negative numbers are not measurements", func(t *testing.T) {
		usage := extractUsage(u, "application/json", []byte(`{"usage":{"prompt_tokens":-1000000,"completion_tokens":4,"cost":-5}}`), usageFields{})
		require.NotNil(t, usage.LLMUsage)
		assert.Equal(t, 0, usage.LLMUsage.PromptTokens)
		assert.Equal(t, 4, usage.LLMUsage.TotalTokens)
		assert.Nil(t, usage.Cost)
		fee := resolveUsage(&schemas.GenericUsageConfig{CostPerRequest: schemas.Ptr(0.01)}, nil)
		usage = extractUsage(fee, "application/json", []byte(`{"usage":{"cost":-5}}`), usageFields{})
		require.NotNil(t, usage.Cost)
		assert.InDelta(t, 0.01, usage.Cost.TotalCost, 1e-9, "a negative upstream cost cannot cancel the flat fee")
	})
	t.Run("aws json media type is json", func(t *testing.T) {
		usage := extractUsage(u, "application/x-amz-json-1.1", []byte(`{"usage":{"total_tokens":3}}`), usageFields{})
		require.NotNil(t, usage.LLMUsage)
		assert.Equal(t, 3, usage.LLMUsage.TotalTokens)
	})
	t.Run("nothing found is still a non-nil usage", func(t *testing.T) {
		usage := extractUsage(u, "application/json", []byte(`{"results":[]}`), usageFields{})
		require.NotNil(t, usage)
		assert.Nil(t, usage.LLMUsage)
		assert.Nil(t, usage.Cost)
	})
	t.Run("non-json content type is not scanned", func(t *testing.T) {
		usage := extractUsage(u, "text/plain", []byte(`{"usage":{"total_tokens":9}}`), usageFields{})
		assert.Nil(t, usage.LLMUsage)
	})
	t.Run("missing content type falls back to sniffing json", func(t *testing.T) {
		usage := extractUsage(u, "", []byte(`{"usage":{"total_tokens":9}}`), usageFields{})
		require.NotNil(t, usage.LLMUsage)
		assert.Equal(t, 9, usage.LLMUsage.TotalTokens)
	})
	t.Run("zero tokens is no usage", func(t *testing.T) {
		usage := extractUsage(u, "application/json", []byte(`{"usage":{"total_tokens":0}}`), usageFields{})
		assert.Nil(t, usage.LLMUsage)
	})
}

func TestExtractUsageConfiguredPaths(t *testing.T) {
	u := resolveUsage(&schemas.GenericUsageConfig{
		InputTokensPath:  "meta.in",
		OutputTokensPath: "meta.out",
		CostPath:         "data.#.cost",
		CostPerRequest:   schemas.Ptr(0.01),
	}, nil)
	usage := extractUsage(u, "application/json", []byte(`{"meta":{"in":2,"out":3},"data":[{"cost":0.1},{"cost":0.2},{"cost":"n/a"}],"usage":{"total_tokens":99}}`), usageFields{})
	require.NotNil(t, usage.LLMUsage)
	assert.Equal(t, 5, usage.LLMUsage.TotalTokens, "configured input/output paths win and total is derived")
	require.NotNil(t, usage.Cost)
	assert.InDelta(t, 0.31, usage.Cost.TotalCost, 1e-9, "array costs are summed and the per-request fee is added")

	t.Run("configured path overrides optimistic even when absent", func(t *testing.T) {
		usage := extractUsage(u, "application/json", []byte(`{"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`), usageFields{})
		assert.Nil(t, usage.LLMUsage, "the optimistic usage block is not read for a field with a configured path")
	})
}

func TestExtractUsageDisableOptimistic(t *testing.T) {
	u := resolveUsage(&schemas.GenericUsageConfig{DisableOptimisticParsing: true, CostPerRequest: schemas.Ptr(0.004)}, nil)
	usage := extractUsage(u, "application/json", []byte(`{"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2},"cost":5}`), usageFields{})
	assert.Nil(t, usage.LLMUsage)
	require.NotNil(t, usage.Cost)
	assert.InDelta(t, 0.004, usage.Cost.TotalCost, 1e-9, "only the flat fee remains")
}

func TestResolveUsageRouteOverride(t *testing.T) {
	base := &schemas.GenericUsageConfig{ModelPath: "model", CostPath: "cost", CostPerRequest: schemas.Ptr(0.01)}
	override := &schemas.GenericUsageConfig{CostPerRequest: schemas.Ptr(0.05), TotalTokensPath: "tokens"}
	u := resolveUsage(base, override)
	assert.Equal(t, "model", u.modelPath, "untouched fields come from the provider")
	assert.Equal(t, "cost", u.costPath)
	assert.Equal(t, "tokens", u.totalTokensPath)
	assert.InDelta(t, 0.05, u.costPerRequest, 1e-9, "the route's fee replaces the provider's")
	assert.True(t, u.optimistic)
}

func TestStreamUsageObservers(t *testing.T) {
	u := resolveUsage(&schemas.GenericUsageConfig{CostPerRequest: schemas.Ptr(0.01)}, nil)
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
	assert.Equal(t, 12, first.LLMUsage.TotalTokens)
	assert.Nil(t, observe([]byte(`{"delta":"more"}`)))
	last := observe([]byte(`{"usage":{"output_tokens":5}}`))
	require.NotNil(t, last)
	assert.Equal(t, 12, last.LLMUsage.PromptTokens)
	assert.Equal(t, 5, last.LLMUsage.CompletionTokens)
	assert.Equal(t, 17, last.LLMUsage.TotalTokens, "fields merge across events")
	assert.InDelta(t, 0.01, last.Cost.TotalCost, 1e-9)

	// A later total wins over the derived sum.
	withTotal := observe([]byte(`{"usage":{"total_tokens":20}}`))
	require.NotNil(t, withTotal)
	assert.Equal(t, 20, withTotal.LLMUsage.TotalTokens)

	fallback := u.defaultUsage(usageFields{})
	require.NotNil(t, fallback)
	assert.Nil(t, fallback.LLMUsage)
	assert.InDelta(t, 0.01, fallback.Cost.TotalCost, 1e-9)
	assert.Nil(t, resolveUsage(nil, nil).defaultUsage(usageFields{}).Cost, "no fee means an empty usage")
}

func TestEffectiveModelPath(t *testing.T) {
	assert.Equal(t, "model", resolveUsage(nil, nil).effectiveModelPath())
	assert.Equal(t, "engine", resolveUsage(&schemas.GenericUsageConfig{ModelPath: "engine"}, nil).effectiveModelPath())
	assert.Equal(t, "", resolveUsage(&schemas.GenericUsageConfig{DisableOptimisticParsing: true}, nil).effectiveModelPath(), "no optimistic parsing means no default model lookup")
	assert.Equal(t, "name", resolveUsage(&schemas.GenericUsageConfig{DisableOptimisticParsing: true}, &schemas.GenericUsageConfig{ModelPath: "name"}).effectiveModelPath())
}

func TestUnitsPricing(t *testing.T) {
	t.Run("response value: Textract pages", func(t *testing.T) {
		u := resolveUsage(&schemas.GenericUsageConfig{UnitsPath: "DocumentMetadata.Pages", CostPerUnit: schemas.Ptr(0.0015)}, nil)
		usage := extractUsage(u, "application/x-amz-json-1.1", []byte(`{"Blocks":[],"DocumentMetadata":{"Pages":7}}`), u.requestUnits([]byte(`{"Document":{}}`)))
		require.NotNil(t, usage.Cost)
		assert.InDelta(t, 7*0.0015, usage.Cost.TotalCost, 1e-9)
		assert.Nil(t, usage.LLMUsage)
	})
	t.Run("request chars: Translate text", func(t *testing.T) {
		u := resolveUsage(&schemas.GenericUsageConfig{UnitsPath: "Text", UnitsSource: schemas.GenericUnitsFromRequest, UnitsMeasure: schemas.GenericUnitsChars, CostPerUnit: schemas.Ptr(15.0 / 1_000_000)}, nil)
		initial := u.requestUnits([]byte(`{"Text":"héllo wörld","SourceLanguageCode":"en","TargetLanguageCode":"de"}`))
		assert.True(t, initial.hasUnits)
		assert.InDelta(t, 11, initial.units, 0, "characters, not bytes")
		usage := extractUsage(u, "application/x-amz-json-1.1", []byte(`{"TranslatedText":"hallo welt"}`), initial)
		require.NotNil(t, usage.Cost)
		assert.InDelta(t, 11*15.0/1_000_000, usage.Cost.TotalCost, 1e-12)
	})
	t.Run("request body bytes: TTS markup with an audio response", func(t *testing.T) {
		u := resolveUsage(&schemas.GenericUsageConfig{UnitsSource: schemas.GenericUnitsFromRequest, UnitsMeasure: schemas.GenericUnitsBodyBytes, CostPerUnit: schemas.Ptr(16.0 / 1_000_000)}, nil)
		ssml := []byte(`<speak version='1.0' xml:lang='en-US'><voice name='en-US-JennyNeural'>Hi</voice></speak>`)
		initial := u.requestUnits(ssml)
		assert.True(t, initial.hasUnits)
		assert.InDelta(t, float64(len(ssml)), initial.units, 0)
		usage := extractUsage(u, "audio/mpeg", []byte("ID3binary"), initial)
		require.NotNil(t, usage.Cost)
		assert.InDelta(t, float64(len(ssml))*16.0/1_000_000, usage.Cost.TotalCost, 1e-12)
		assert.Equal(t, usage.Cost.TotalCost, u.defaultUsage(initial).Cost.TotalCost, "the streaming fallback carries the same request-side quantity")
	})
	t.Run("rounding: per started block with a minimum (Comprehend Medical shape)", func(t *testing.T) {
		u := resolveUsage(&schemas.GenericUsageConfig{UnitsPath: "Text", UnitsSource: schemas.GenericUnitsFromRequest, UnitsMeasure: schemas.GenericUnitsChars, UnitsBlockSize: schemas.Ptr(100.0), UnitsMinimum: schemas.Ptr(1.0), CostPerUnit: schemas.Ptr(0.01)}, nil)
		assert.InDelta(t, 3, u.billedUnits(250), 0, "250 characters start three 100-character blocks")
		assert.InDelta(t, 1, u.billedUnits(100), 0, "an exact block is one")
		assert.InDelta(t, 1, u.billedUnits(0), 0, "the minimum applies once a quantity was read")
		initial := u.requestUnits([]byte(`{"Text":"` + strings.Repeat("a", 250) + `"}`))
		require.True(t, initial.hasUnits)
		usage := extractUsage(u, "application/x-amz-json-1.1", []byte(`{"Entities":[]}`), initial)
		require.NotNil(t, usage.Cost)
		assert.InDelta(t, 0.03, usage.Cost.TotalCost, 1e-9)
		exact := resolveUsage(&schemas.GenericUsageConfig{UnitsPath: "Text", UnitsSource: schemas.GenericUnitsFromRequest, UnitsMeasure: schemas.GenericUnitsChars, CostPerUnit: schemas.Ptr(0.01)}, nil)
		assert.InDelta(t, 250, exact.billedUnits(250), 0, "without a block size the raw quantity is billed exactly")
		assert.InDelta(t, 0, exact.billedUnits(0), 0, "and no minimum applies")
	})
	t.Run("request body chars: multi-byte SSML billed per character, not per byte", func(t *testing.T) {
		u := resolveUsage(&schemas.GenericUsageConfig{UnitsSource: schemas.GenericUnitsFromRequest, UnitsMeasure: schemas.GenericUnitsBodyChars, CostPerUnit: schemas.Ptr(16.0 / 1_000_000)}, nil)
		ssml := []byte(`<speak xml:lang='ja-JP'><voice name='ja-JP-NanamiNeural'>こんにちは、世界</voice></speak>`)
		require.Greater(t, len(ssml), utf8.RuneCount(ssml), "the fixture must contain multi-byte characters")
		initial := u.requestUnits(ssml)
		assert.True(t, initial.hasUnits)
		assert.InDelta(t, float64(utf8.RuneCount(ssml)), initial.units, 0, "characters, not bytes")
		usage := extractUsage(u, "audio/mpeg", []byte("ID3binary"), initial)
		require.NotNil(t, usage.Cost)
		assert.InDelta(t, float64(utf8.RuneCount(ssml))*16.0/1_000_000, usage.Cost.TotalCost, 1e-12)
		plain := resolveUsage(&schemas.GenericUsageConfig{UnitsSource: schemas.GenericUnitsFromRequest, UnitsMeasure: schemas.GenericUnitsBodyChars, CostPerUnit: schemas.Ptr(1.0)}, nil)
		assert.InDelta(t, 11, plain.requestUnits([]byte("héllo wörld")).units, 0, "any content type")
	})
	t.Run("units add to fee and extracted cost", func(t *testing.T) {
		u := resolveUsage(&schemas.GenericUsageConfig{UnitsPath: "usage.credits", CostPerUnit: schemas.Ptr(0.008), CostPerRequest: schemas.Ptr(0.001), CostPath: "usage.cost"}, nil)
		usage := extractUsage(u, "application/json", []byte(`{"usage":{"credits":2,"cost":0.5}}`), usageFields{})
		require.NotNil(t, usage.Cost)
		assert.InDelta(t, 2*0.008+0.001+0.5, usage.Cost.TotalCost, 1e-9)
	})
	t.Run("missing or negative quantity bills nothing extra", func(t *testing.T) {
		u := resolveUsage(&schemas.GenericUsageConfig{UnitsPath: "pages", CostPerUnit: schemas.Ptr(1.0), CostPerRequest: schemas.Ptr(0.01)}, nil)
		usage := extractUsage(u, "application/json", []byte(`{"pages":-3}`), usageFields{})
		require.NotNil(t, usage.Cost)
		assert.InDelta(t, 0.01, usage.Cost.TotalCost, 1e-9)
		usage = extractUsage(u, "application/json", []byte(`{"other":1}`), usageFields{})
		assert.InDelta(t, 0.01, usage.Cost.TotalCost, 1e-9)
		usage = extractUsage(u, "text/plain", []byte(`pages: 3`), usageFields{})
		assert.InDelta(t, 0.01, usage.Cost.TotalCost, 1e-9, "a non-JSON response has no value to read")
	})
	t.Run("no rate means units are ignored", func(t *testing.T) {
		u := resolveUsage(&schemas.GenericUsageConfig{UnitsPath: "pages"}, nil)
		assert.False(t, u.billsUnits())
		usage := extractUsage(u, "application/json", []byte(`{"pages":3}`), usageFields{})
		assert.Nil(t, usage.Cost)
	})
	t.Run("streams observe response units and keep request units", func(t *testing.T) {
		u := resolveUsage(&schemas.GenericUsageConfig{UnitsPath: "pages", CostPerUnit: schemas.Ptr(0.5)}, nil)
		assert.True(t, u.hasUsageEvent([]byte(`{"pages":2}`)))
		observe := u.streamObserver(usageFields{})
		assert.Nil(t, observe([]byte(`{"delta":"x"}`)))
		got := observe([]byte(`{"pages":2}`))
		require.NotNil(t, got)
		assert.InDelta(t, 1.0, got.Cost.TotalCost, 1e-9)

		req := resolveUsage(&schemas.GenericUsageConfig{UnitsPath: "text", UnitsSource: schemas.GenericUnitsFromRequest, UnitsMeasure: schemas.GenericUnitsChars, CostPerUnit: schemas.Ptr(0.1)}, nil)
		initial := req.requestUnits([]byte(`{"text":"abcd"}`))
		observe = req.streamObserver(initial)
		got = observe([]byte(`{"usage":{"total_tokens":3}}`))
		require.NotNil(t, got)
		assert.InDelta(t, 0.4, got.Cost.TotalCost, 1e-9, "request units survive response events")
		assert.Equal(t, 3, got.LLMUsage.TotalTokens)
	})
	t.Run("streams never measure a whole-body response quantity per event", func(t *testing.T) {
		u := resolveUsage(&schemas.GenericUsageConfig{UnitsMeasure: schemas.GenericUnitsBodyChars, CostPerUnit: schemas.Ptr(1.0), CostPerRequest: schemas.Ptr(0.01)}, nil)
		observe := u.streamObserver(usageFields{})
		got := observe([]byte(`{"usage":{"total_tokens":3}}`))
		require.NotNil(t, got)
		assert.InDelta(t, 0.01, got.Cost.TotalCost, 1e-9, "the event's size is not the body's: only the fee is billed")
		assert.Equal(t, 3, got.LLMUsage.TotalTokens)
		assert.Nil(t, observe([]byte(`{"delta":"x"}`)), "an event without usage fields still reports nothing")
	})
	t.Run("an array at the path is summed: a batch body bills every element", func(t *testing.T) {
		u := resolveUsage(&schemas.GenericUsageConfig{UnitsPath: "#.Text", UnitsSource: schemas.GenericUnitsFromRequest, UnitsMeasure: schemas.GenericUnitsChars, CostPerUnit: schemas.Ptr(1.0)}, nil)
		got := u.requestUnits([]byte(`[{"Text":"abc"},{"Text":"héllo"},{"Other":1}]`))
		require.True(t, got.hasUnits)
		assert.InDelta(t, 8, got.units, 0)
		pages := resolveUsage(&schemas.GenericUsageConfig{UnitsPath: "docs.#.pages", CostPerUnit: schemas.Ptr(1.0)}, nil)
		usage := extractUsage(pages, "application/json", []byte(`{"docs":[{"pages":2},{"pages":5},{"pages":-1}]}`), usageFields{})
		require.NotNil(t, usage.Cost)
		assert.InDelta(t, 7, usage.Cost.TotalCost, 1e-9, "negative elements are not measurements")
	})
	t.Run("an empty body is a measured zero, so the minimum applies", func(t *testing.T) {
		u := resolveUsage(&schemas.GenericUsageConfig{UnitsSource: schemas.GenericUnitsFromRequest, UnitsMeasure: schemas.GenericUnitsBodyChars, UnitsMinimum: schemas.Ptr(1.0), CostPerUnit: schemas.Ptr(0.5)}, nil)
		got := u.requestUnits(nil)
		require.True(t, got.hasUnits)
		usage := extractUsage(u, "audio/mpeg", nil, got)
		require.NotNil(t, usage.Cost)
		assert.InDelta(t, 0.5, usage.Cost.TotalCost, 1e-9)
		resp := resolveUsage(&schemas.GenericUsageConfig{UnitsMeasure: schemas.GenericUnitsBodyBytes, UnitsMinimum: schemas.Ptr(2.0), CostPerUnit: schemas.Ptr(1.0)}, nil)
		usage = extractUsage(resp, "application/octet-stream", nil, usageFields{})
		require.NotNil(t, usage.Cost, "an empty buffered response is measured too")
		assert.InDelta(t, 2, usage.Cost.TotalCost, 1e-9)
	})
	t.Run("an overflowing quantity is not a measurement", func(t *testing.T) {
		u := resolveUsage(&schemas.GenericUsageConfig{UnitsPath: "pages", CostPerUnit: schemas.Ptr(1.0), CostPerRequest: schemas.Ptr(0.01)}, nil)
		usage := extractUsage(u, "application/json", []byte(`{"pages":1e400}`), usageFields{})
		require.NotNil(t, usage.Cost)
		assert.InDelta(t, 0.01, usage.Cost.TotalCost, 1e-9)
		sum := resolveUsage(&schemas.GenericUsageConfig{UnitsPath: "#.n", CostPerUnit: schemas.Ptr(1.0)}, nil)
		usage = extractUsage(sum, "application/json", []byte(`[{"n":1e308},{"n":1e308}]`), usageFields{})
		assert.Nil(t, usage.Cost, "a sum that overflows bills nothing rather than +Inf")
		cost := resolveUsage(&schemas.GenericUsageConfig{CostPath: "usage.cost"}, nil)
		assert.Nil(t, extractUsage(cost, "application/json", []byte(`{"usage":{"cost":1e400}}`), usageFields{}).Cost)
	})
	t.Run("a charge that overflows is refused on the request side and dropped on the response side", func(t *testing.T) {
		req := resolveUsage(&schemas.GenericUsageConfig{UnitsPath: "n", UnitsSource: schemas.GenericUnitsFromRequest, CostPerUnit: schemas.Ptr(10.0)}, nil)
		assert.Error(t, req.requestBodyError([]byte(`{"n":1e308}`)), "the caller's quantity times the rate overflows")
		assert.NoError(t, req.requestBodyError([]byte(`{"n":5}`)))
		resp := resolveUsage(&schemas.GenericUsageConfig{UnitsPath: "n", CostPerUnit: schemas.Ptr(10.0), CostPerRequest: schemas.Ptr(0.01)}, nil)
		usage := extractUsage(resp, "application/json", []byte(`{"n":1e308}`), usageFields{})
		require.NotNil(t, usage.Cost)
		assert.InDelta(t, 0.01, usage.Cost.TotalCost, 1e-9, "the fee stands, the overflowing unit charge does not")
	})
	t.Run("route overrides units", func(t *testing.T) {
		base := &schemas.GenericUsageConfig{UnitsPath: "pages", CostPerUnit: schemas.Ptr(0.5)}
		u := resolveUsage(base, &schemas.GenericUsageConfig{UnitsPath: "usage.credits", CostPerUnit: schemas.Ptr(0.0)})
		assert.Equal(t, "usage.credits", u.unitsPath)
		assert.False(t, u.billsUnits(), "a route can switch unit pricing off with a zero rate")
	})
}

// BenchmarkHasUsageEvent is the per-event gate on a generic SSE stream: it must not allocate.
func BenchmarkHasUsageEvent(b *testing.B) {
	u := resolveUsage(&schemas.GenericUsageConfig{CostPerRequest: schemas.Ptr(0.01), UnitsPath: "pages", CostPerUnit: schemas.Ptr(0.1)}, nil)
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
	translate := resolveUsage(&schemas.GenericUsageConfig{UnitsPath: "Text", UnitsSource: schemas.GenericUnitsFromRequest, UnitsMeasure: schemas.GenericUnitsChars, CostPerUnit: schemas.Ptr(0.001)}, nil)
	assert.NoError(t, translate.requestBodyError([]byte(`{"Text":"hello","Lang":"de"}`)))
	got := translate.requestUnits([]byte(`{"Text":"hello"}`))
	require.True(t, got.hasUnits, "the body is measured as JSON even when the caller labels it text/plain")
	assert.InDelta(t, 5, got.units, 0)

	for name, body := range map[string]string{
		"not JSON":                     `Text=hello`,
		"empty":                        ``,
		"duplicate key":                `{"Text":"a","Text":"` + strings.Repeat("b", 100) + `"}`,
		"case variant":                 `{"Text":"a","text":"` + strings.Repeat("b", 100) + `"}`,
		"duplicate inside an array":    `[{"Text":"a"},{"Text":"a","TEXT":"b"}]`,
		"duplicate in a nested object": `{"Options":{"x":1,"X":2},"Text":"a"}`,
		"too deep":                     strings.Repeat(`{"a":`, 70) + `1` + strings.Repeat(`}`, 70),
	} {
		assert.Errorf(t, translate.requestBodyError([]byte(body)), "%s must be refused", name)
	}

	tts := resolveUsage(&schemas.GenericUsageConfig{UnitsSource: schemas.GenericUnitsFromRequest, UnitsMeasure: schemas.GenericUnitsBodyChars, CostPerUnit: schemas.Ptr(0.001)}, nil)
	assert.NoError(t, tts.requestBodyError([]byte(`<speak>hi</speak>`)), "a whole-body measure reads any body")
	pages := resolveUsage(&schemas.GenericUsageConfig{UnitsPath: "Pages", CostPerUnit: schemas.Ptr(0.001)}, nil)
	assert.NoError(t, pages.requestBodyError([]byte(`not json`)), "response-side units do not constrain the request")
	assert.NoError(t, resolveUsage(nil, nil).requestBodyError([]byte(`{"a":1,"a":2}`)), "no unit pricing, no check")
}

// A generic usage always reports its mapping charge (fee, units and any upstream-reported cost), which
// marks the row as a generic relay's and is what cost recalculation keeps.
func TestBuildReportsMappingCharge(t *testing.T) {
	u := resolveUsage(&schemas.GenericUsageConfig{CostPerRequest: schemas.Ptr(0.01)}, nil)
	usage := u.build(usageFields{input: 10, output: 5, hasInput: true, hasOutput: true})
	require.NotNil(t, usage.Cost)
	assert.InDelta(t, 0.01, usage.Cost.TotalCost, 1e-12)
	assert.InDelta(t, 0.01, usage.Cost.AdditionalCost, 1e-12, "filed as additional cost so the cost columns add up")
	require.NotNil(t, usage.MappingCharge)
	assert.InDelta(t, 0.01, *usage.MappingCharge, 1e-12)

	free := resolveUsage(&schemas.GenericUsageConfig{}, nil).build(usageFields{})
	require.NotNil(t, free.MappingCharge, "a zero charge is still reported")
	assert.Zero(t, *free.MappingCharge)
}
