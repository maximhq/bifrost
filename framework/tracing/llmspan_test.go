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

func TestPopulateErrorAttributesEmitsBilledUsage(t *testing.T) {
	msg := "stream cancelled by client"
	bifrostErr := &schemas.BifrostError{
		Error: &schemas.ErrorField{Message: msg},
	}
	bifrostErr.ExtraFields.RequestType = schemas.ChatCompletionStreamRequest
	bifrostErr.ExtraFields.BilledUsage = &schemas.BifrostLLMUsage{
		PromptTokens:     1200,
		CompletionTokens: 34,
		TotalTokens:      1234,
		PromptTokensDetails: &schemas.ChatPromptTokensDetails{
			CachedReadTokens:  1000,
			CachedWriteTokens: 200,
			CachedWriteTokenDetails: &schemas.ChatCachedWriteTokenDetails{
				CachedWriteTokens5m: 120,
				CachedWriteTokens1h: 80,
			},
		},
	}

	attrs := PopulateErrorAttributes(bifrostErr)

	for key, want := range map[string]any{
		schemas.AttrInputTokens:                     1200,
		schemas.AttrOutputTokens:                    34,
		schemas.AttrTotalTokens:                     1234,
		schemas.AttrUsageCacheReadInputTokens:       1000,
		schemas.AttrUsageCacheCreationInputTokens:   200,
		schemas.AttrPromptTokenDetailsCachedWrite5m: 120,
		schemas.AttrPromptTokenDetailsCachedWrite1h: 80,
	} {
		if got := attrs[key]; got != want {
			t.Errorf("attribute %s = %v, want %v", key, got, want)
		}
	}
	// A failed chat span must not carry the Responses namespace: the otel
	// plugin treats the two 5m/1h families as mutually exclusive per request.
	for _, key := range []string{
		schemas.AttrInputTokenDetailsCachedWrite5m,
		schemas.AttrInputTokenDetailsCachedWrite1h,
	} {
		if _, ok := attrs[key]; ok {
			t.Errorf("Responses-namespace attribute %s present on a chat span", key)
		}
	}
}

func TestPopulateErrorAttributesUsesResponsesNamespace(t *testing.T) {
	bifrostErr := &schemas.BifrostError{Error: &schemas.ErrorField{Message: "responses stream cancelled"}}
	bifrostErr.ExtraFields.RequestType = schemas.ResponsesStreamRequest
	bifrostErr.ExtraFields.BilledUsage = &schemas.BifrostLLMUsage{
		PromptTokensDetails: &schemas.ChatPromptTokensDetails{
			CachedWriteTokenDetails: &schemas.ChatCachedWriteTokenDetails{
				CachedWriteTokens5m: 120,
				CachedWriteTokens1h: 80,
			},
		},
	}

	attrs := PopulateErrorAttributes(bifrostErr)

	for key, want := range map[string]any{
		schemas.AttrInputTokenDetailsCachedWrite5m: 120,
		schemas.AttrInputTokenDetailsCachedWrite1h: 80,
	} {
		if got := attrs[key]; got != want {
			t.Errorf("attribute %s = %v, want %v", key, got, want)
		}
	}
	for _, key := range []string{
		schemas.AttrPromptTokenDetailsCachedWrite5m,
		schemas.AttrPromptTokenDetailsCachedWrite1h,
	} {
		if _, ok := attrs[key]; ok {
			t.Errorf("chat-namespace attribute %s present on a Responses span", key)
		}
	}
}

func TestPopulateErrorAttributesWithoutBilledUsageEmitsNoTokens(t *testing.T) {
	msg := "401 before the model ran"
	bifrostErr := &schemas.BifrostError{Error: &schemas.ErrorField{Message: msg}}

	attrs := PopulateErrorAttributes(bifrostErr)

	for _, key := range []string{schemas.AttrInputTokens, schemas.AttrOutputTokens, schemas.AttrTotalTokens} {
		if _, ok := attrs[key]; ok {
			t.Errorf("attribute %s present for a request that consumed no tokens", key)
		}
	}
}

func TestPopulateErrorAttributesEmitsCacheWriteDetailsWithoutAggregate(t *testing.T) {
	bifrostErr := &schemas.BifrostError{Error: &schemas.ErrorField{Message: "stream failed during cache creation"}}
	bifrostErr.ExtraFields.RequestType = schemas.ChatCompletionStreamRequest
	bifrostErr.ExtraFields.BilledUsage = &schemas.BifrostLLMUsage{
		PromptTokensDetails: &schemas.ChatPromptTokensDetails{
			CachedWriteTokenDetails: &schemas.ChatCachedWriteTokenDetails{
				CachedWriteTokens5m: 120,
				CachedWriteTokens1h: 80,
			},
		},
	}

	attrs := PopulateErrorAttributes(bifrostErr)

	for key, want := range map[string]any{
		schemas.AttrPromptTokenDetailsCachedWrite5m: 120,
		schemas.AttrPromptTokenDetailsCachedWrite1h: 80,
	} {
		if got := attrs[key]; got != want {
			t.Errorf("attribute %s = %v, want %v", key, got, want)
		}
	}
	// Zero-valued aggregates and totals stay absent: this BilledUsage carries
	// only cache-write details, so emitting the totals would stamp explicit
	// zeros on the span.
	for _, key := range []string{
		schemas.AttrUsageCacheCreationInputTokens,
		schemas.AttrInputTokens,
		schemas.AttrOutputTokens,
		schemas.AttrTotalTokens,
	} {
		if _, ok := attrs[key]; ok {
			t.Errorf("zero-valued attribute %s is present", key)
		}
	}
}

// A cancelled stream reaches PopulateLLMResponseAttributes with BOTH a non-nil
// accumulated response and a non-nil error (see core/providers/utils). The
// accumulated response is missing the final usage chunk, so the error's
// BilledUsage must win. This mirrors the merge order in Tracer.
func TestErrorAttributesOverrideAccumulatedResponseTokens(t *testing.T) {
	partial := &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			Usage: &schemas.BifrostLLMUsage{PromptTokens: 0, CompletionTokens: 0, TotalTokens: 0},
		},
	}
	bifrostErr := &schemas.BifrostError{Error: &schemas.ErrorField{Message: "client cancelled the stream"}}
	bifrostErr.ExtraFields.BilledUsage = &schemas.BifrostLLMUsage{
		PromptTokens:     4096,
		CompletionTokens: 128,
		TotalTokens:      4224,
	}

	attrs := PopulateResponseAttributes(partial)
	for k, v := range PopulateErrorAttributes(bifrostErr) {
		attrs[k] = v
	}

	if got := attrs[schemas.AttrInputTokens]; got != 4096 {
		t.Errorf("%s = %v, want 4096 from BilledUsage", schemas.AttrInputTokens, got)
	}
	if got := attrs[schemas.AttrTotalTokens]; got != 4224 {
		t.Errorf("%s = %v, want 4224 from BilledUsage", schemas.AttrTotalTokens, got)
	}
}

func TestPopulateResponsesRequestAttributesExportsInstructions(t *testing.T) {
	instructions := "Code every brand mention in the answer."
	attrs := map[string]any{}

	PopulateResponsesRequestAttributes(&schemas.BifrostResponsesRequest{
		Params: &schemas.ResponsesParameters{Instructions: &instructions},
	}, attrs)

	if got := attrs[schemas.AttrInstructions]; got != instructions {
		t.Fatalf("instructions attribute = %v, want %q", got, instructions)
	}

	// Absent or empty instructions must not produce the attribute at all.
	for _, in := range []*string{nil, new(string)} {
		attrs = map[string]any{}
		PopulateResponsesRequestAttributes(&schemas.BifrostResponsesRequest{
			Params: &schemas.ResponsesParameters{Instructions: in},
		}, attrs)
		if _, ok := attrs[schemas.AttrInstructions]; ok {
			t.Fatalf("instructions attribute set for %v, want absent", in)
		}
	}
}
