package tracing

import (
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func assertJSONAttr(t *testing.T, attrs map[string]any, key string) map[string]any {
	t.Helper()

	raw, ok := attrs[key].(string)
	if !ok {
		t.Fatalf("attribute %s = %T(%v), want JSON string", key, attrs[key], attrs[key])
	}
	if strings.Contains(raw, "map[") || strings.Contains(raw, "&map") {
		t.Fatalf("attribute %s used Go map formatting: %q", key, raw)
	}

	var parsed map[string]any
	if err := schemas.Unmarshal([]byte(raw), &parsed); err != nil {
		t.Fatalf("attribute %s = %q, want valid JSON object: %v", key, raw, err)
	}
	return parsed
}

func TestPopulateResponsesResponseAttributesSerializesMetadataAsJSON(t *testing.T) {
	emptyMetadata := map[string]any{}
	attrs := map[string]any{}

	PopulateResponsesResponseAttributes(&schemas.BifrostResponsesResponse{
		Metadata: &emptyMetadata,
	}, attrs)

	if got := attrs[schemas.AttrRespMetadata]; got != "{}" {
		t.Fatalf("empty metadata = %v, want {}", got)
	}

	metadata := map[string]any{
		"tenant": "acme",
		"flags":  []any{"beta", "trace"},
		"nested": map[string]any{"enabled": true},
	}
	attrs = map[string]any{}

	PopulateResponsesResponseAttributes(&schemas.BifrostResponsesResponse{
		Metadata: &metadata,
	}, attrs)

	parsed := assertJSONAttr(t, attrs, schemas.AttrRespMetadata)
	if parsed["tenant"] != "acme" {
		t.Fatalf("metadata tenant = %v, want acme", parsed["tenant"])
	}
	if _, ok := parsed["nested"].(map[string]any); !ok {
		t.Fatalf("metadata nested = %T(%v), want object", parsed["nested"], parsed["nested"])
	}
}

func TestPopulateTextCompletionRequestAttributesSerializesLogitBiasAsJSON(t *testing.T) {
	logitBias := map[string]float64{"50256": -100}
	attrs := map[string]any{}

	PopulateTextCompletionRequestAttributes(&schemas.BifrostTextCompletionRequest{
		Params: &schemas.TextCompletionParameters{
			LogitBias: &logitBias,
		},
	}, attrs)

	parsed := assertJSONAttr(t, attrs, schemas.AttrLogitBias)
	if parsed["50256"] != float64(-100) {
		t.Fatalf("logit bias = %v, want -100", parsed["50256"])
	}
}

func TestPopulateBatchCreateRequestAttributesSerializesMetadataAsJSON(t *testing.T) {
	attrs := map[string]any{}

	PopulateBatchCreateRequestAttributes(&schemas.BifrostBatchCreateRequest{
		Metadata: map[string]string{"job": "nightly"},
	}, attrs)

	parsed := assertJSONAttr(t, attrs, schemas.AttrBatchMetadata)
	if parsed["job"] != "nightly" {
		t.Fatalf("batch metadata job = %v, want nightly", parsed["job"])
	}
}

func TestPopulateRequestExtraParamsSerializesStructuredValues(t *testing.T) {
	tests := []struct {
		name     string
		populate func(map[string]any)
	}{
		{
			name: "chat",
			populate: func(attrs map[string]any) {
				PopulateChatRequestAttributes(&schemas.BifrostChatRequest{
					Params: &schemas.ChatParameters{
						ExtraParams: map[string]any{
							"structured": map[string]any{"mode": "json"},
							"scalar":     7,
						},
					},
				}, attrs)
			},
		},
		{
			name: "text",
			populate: func(attrs map[string]any) {
				PopulateTextCompletionRequestAttributes(&schemas.BifrostTextCompletionRequest{
					Params: &schemas.TextCompletionParameters{
						ExtraParams: map[string]any{
							"structured": []any{"a", "b"},
							"scalar":     true,
						},
					},
				}, attrs)
			},
		},
		{
			name: "embedding",
			populate: func(attrs map[string]any) {
				PopulateEmbeddingRequestAttributes(&schemas.BifrostEmbeddingRequest{
					Params: &schemas.EmbeddingParameters{
						ExtraParams: map[string]any{
							"structured": map[string]any{"dimensions": 1536},
							"scalar":     "text",
						},
					},
				}, attrs)
			},
		},
		{
			name: "batch",
			populate: func(attrs map[string]any) {
				PopulateBatchListRequestAttributes(&schemas.BifrostBatchListRequest{
					ExtraParams: map[string]any{
						"structured": map[string]any{"cursor": "next"},
						"scalar":     3,
					},
				}, attrs)
			},
		},
		{
			name: "file",
			populate: func(attrs map[string]any) {
				PopulateFileListRequestAttributes(&schemas.BifrostFileListRequest{
					ExtraParams: map[string]any{
						"structured": map[string]any{"storage": "s3"},
						"scalar":     "raw",
					},
				}, attrs)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			attrs := map[string]any{}
			tc.populate(attrs)

			raw, ok := attrs["structured"].(string)
			if !ok {
				t.Fatalf("structured extra param = %T(%v), want string", attrs["structured"], attrs["structured"])
			}
			if strings.Contains(raw, "map[") || strings.Contains(raw, "&map") {
				t.Fatalf("structured extra param used Go formatting: %q", raw)
			}
			var parsed any
			if err := schemas.Unmarshal([]byte(raw), &parsed); err != nil {
				t.Fatalf("structured extra param = %q, want valid JSON: %v", raw, err)
			}
			if attrs["scalar"] == "" || attrs["scalar"] == nil {
				t.Fatalf("scalar extra param was not preserved: %v", attrs["scalar"])
			}
		})
	}
}
