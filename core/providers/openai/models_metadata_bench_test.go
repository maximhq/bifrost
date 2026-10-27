package openai

import (
	"encoding/json"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// benchmarkListModelsEnvelope is a representative OpenAI-envelope list-models
// payload: a mix of bare entries (the OpenAI catalog today) and entries
// carrying the extended metadata emitted by OpenAI-compatible upstreams.
var benchmarkListModelsEnvelope = mustJSON(map[string]any{
	"object": "list",
	"data": []map[string]any{
		{"id": "gpt-5.2", "object": "model", "owned_by": "openai", "created": 1761264000},
		{"id": "gpt-5.2-mini", "object": "model", "owned_by": "openai", "created": 1761264000},
		{
			"id":                "kimi/k3",
			"object":            "model",
			"created":           1761264000,
			"display_name":      "K3",
			"context_length":    1048576,
			"max_input_tokens":  1040500,
			"max_output_tokens": 131072,
			"reasoning": map[string]any{
				"mandatory": false, "default_enabled": true,
				"supported_efforts": []string{"low", "high"}, "default_effort": "high",
			},
			"modalities": map[string]any{"input": []string{"text", "image"}, "output": []string{"text"}},
		},
	},
})

func mustJSON(v any) []byte {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return data
}

// BenchmarkOpenAIListModelsParseAndConvert measures decode of an upstream
// list-models payload plus conversion to the Bifrost catalog shape — the full
// per-call cost of GET /v1/models on the provider side.
func BenchmarkOpenAIListModelsParseAndConvert(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		var response OpenAIListModelsResponse
		if err := json.Unmarshal(benchmarkListModelsEnvelope, &response); err != nil {
			b.Fatal(err)
		}
		response.ToBifrostListModelsResponse(
			schemas.ModelProvider("bench"),
			schemas.WhiteList{"*"},
			nil,
			nil,
			false,
		)
	}
}

// BenchmarkOpenAIListModelsParse isolates the unmarshal cost.
func BenchmarkOpenAIListModelsParse(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		var response OpenAIListModelsResponse
		if err := json.Unmarshal(benchmarkListModelsEnvelope, &response); err != nil {
			b.Fatal(err)
		}
	}
}

// benchmarkListModelsBareEnvelope is a catalog whose upstream emits no
// extended metadata (plain OpenAI shape) — the common case that must not
// regress when the decoder learns new fields.
var benchmarkListModelsBareEnvelope = mustJSON(map[string]any{
	"object": "list",
	"data": []map[string]any{
		{"id": "gpt-5.2", "object": "model", "owned_by": "openai", "created": 1761264000},
		{"id": "gpt-5.2-mini", "object": "model", "owned_by": "openai", "created": 1761264000},
	},
})

// BenchmarkOpenAIListModelsBareParseAndConvert measures the no-metadata path.
func BenchmarkOpenAIListModelsBareParseAndConvert(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		var response OpenAIListModelsResponse
		if err := json.Unmarshal(benchmarkListModelsBareEnvelope, &response); err != nil {
			b.Fatal(err)
		}
		response.ToBifrostListModelsResponse(
			schemas.ModelProvider("bench"),
			schemas.WhiteList{"*"},
			nil,
			nil,
			false,
		)
	}
}
