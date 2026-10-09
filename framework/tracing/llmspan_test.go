package tracing

import (
	"reflect"
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

// Responses API responses carry a single top-level stop_reason rather than
// per-choice finish reasons. It must reach the span as
// gen_ai.response.finish_reasons exactly like the chat path, otherwise model
// refusals on /v1/responses are invisible to OTEL consumers.
func TestPopulateResponsesResponseAttributesEmitsFinishReasons(t *testing.T) {
	for _, reason := range []string{"refusal", "stop", "length"} {
		attrs := map[string]any{}

		PopulateResponsesResponseAttributes(&schemas.BifrostResponsesResponse{
			StopReason: schemas.Ptr(reason),
		}, attrs)

		got, ok := attrs[schemas.AttrFinishReasons].([]string)
		if !ok {
			t.Fatalf("stop_reason=%q: %s = %T(%v), want []string", reason, schemas.AttrFinishReasons, attrs[schemas.AttrFinishReasons], attrs[schemas.AttrFinishReasons])
		}
		if len(got) != 1 || got[0] != reason {
			t.Fatalf("stop_reason=%q: %s = %v, want [%s]", reason, schemas.AttrFinishReasons, got, reason)
		}
	}
}

// A Responses API response without a stop_reason must not emit either finish
// reason attribute: an empty or placeholder value would read as a real outcome
// to OTEL consumers.
func TestPopulateResponsesResponseAttributesOmitsFinishReasonsWhenStopReasonNil(t *testing.T) {
	attrs := map[string]any{}

	PopulateResponsesResponseAttributes(&schemas.BifrostResponsesResponse{
		ID:    schemas.Ptr("resp_123"),
		Model: "gpt-4o-mini",
	}, attrs)

	if got, ok := attrs[schemas.AttrFinishReasons]; ok {
		t.Fatalf("%s = %v, want absent when stop_reason is nil", schemas.AttrFinishReasons, got)
	}
	if got, ok := attrs[schemas.AttrFinishReason]; ok {
		t.Fatalf("%s = %v, want absent when stop_reason is nil", schemas.AttrFinishReason, got)
	}
}

// ---------------------------------------------------------------------------
// Typed span payload: equivalence with the legacy attribute map.
// ---------------------------------------------------------------------------

// legacyChatAttributes renders a request/response/error triple the way the
// Populate* functions do today, so the shim can be diffed against it.
func legacyChatAttributes(req *schemas.BifrostRequest, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) map[string]any {
	attrs := PopulateRequestAttributes(req)
	for k, v := range PopulateResponseAttributes(resp) {
		attrs[k] = v
	}
	for k, v := range PopulateErrorAttributes(bifrostErr) {
		attrs[k] = v
	}
	return attrs
}

func strPtr(s string) *string   { return &s }
func intPtr(i int) *int         { return &i }
func f64Ptr(f float64) *float64 { return &f }
func boolPtr(b bool) *bool      { return &b }

// chatFixture builds a chat request/response pair exercising params, content,
// tool calls, usage details and the response envelope.
func chatFixture() (*schemas.BifrostRequest, *schemas.BifrostResponse) {
	req := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4o",
			Input: []schemas.ChatMessage{
				{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: strPtr("hello")}},
			},
			Params: &schemas.ChatParameters{
				MaxCompletionTokens: intPtr(512),
				Temperature:         f64Ptr(0.7),
				TopP:                f64Ptr(0.95),
				Stop:                []string{"END", "STOP"},
				PresencePenalty:     f64Ptr(0.1),
				FrequencyPenalty:    f64Ptr(0.2),
				ParallelToolCalls:   boolPtr(true),
				User:                strPtr("user-1"),
			},
		},
	}

	tier := schemas.BifrostServiceTier("flex")
	resp := &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			ID:                "chatcmpl-1",
			Model:             "gpt-4o-2024-08-06",
			Object:            "chat.completion",
			SystemFingerprint: "fp_abc",
			Created:           1700000000,
			ServiceTier:       &tier,
			Choices: []schemas.BifrostResponseChoice{{
				FinishReason: strPtr("stop"),
				ChatNonStreamResponseChoice: &schemas.ChatNonStreamResponseChoice{
					Message: &schemas.ChatMessage{
						Role:    schemas.ChatMessageRoleAssistant,
						Content: &schemas.ChatMessageContent{ContentStr: strPtr("hi there")},
					},
				},
			}},
			Usage: &schemas.BifrostLLMUsage{
				PromptTokens:     10,
				CompletionTokens: 5,
				TotalTokens:      15,
				PromptTokensDetails: &schemas.ChatPromptTokensDetails{
					TextTokens:              8,
					AudioTokens:             2,
					CachedReadTokens:        4,
					CachedWriteTokens:       3,
					CachedWriteTokenDetails: &schemas.ChatCachedWriteTokenDetails{CachedWriteTokens5m: 1, CachedWriteTokens1h: 2},
				},
				CompletionTokensDetails: &schemas.ChatCompletionTokensDetails{
					TextTokens:               5,
					ReasoningTokens:          3,
					AcceptedPredictionTokens: 1,
					RejectedPredictionTokens: 2,
					CitationTokens:           intPtr(4),
					NumSearchQueries:         intPtr(6),
				},
			},
		},
	}
	return req, resp
}

// TestSpanDataAttributesMatchLegacy pins LLMSpanData.Attributes() to the output
// of the Populate* functions. It is what makes migrating connectors onto the
// typed record safe: any key or value the typed path would lose shows up here.
func TestSpanDataAttributesMatchLegacy(t *testing.T) {
	billedErr := &schemas.BifrostError{
		StatusCode: intPtr(429),
		Error: &schemas.ErrorField{
			Message: "rate limited",
			Type:    strPtr("rate_limit_error"),
			Code:    strPtr("429"),
		},
	}

	cases := []struct {
		name string
		req  *schemas.BifrostRequest
		resp *schemas.BifrostResponse
		err  *schemas.BifrostError
	}{
		{name: "chat request and response"},
		{name: "request only"},
		{name: "error only"},
	}

	req, resp := chatFixture()
	cases[0].req, cases[0].resp = req, resp
	cases[1].req = req
	cases[2].req, cases[2].err = req, billedErr

	for _, fx := range []func() (*schemas.BifrostRequest, *schemas.BifrostResponse){
		responsesFixture, textCompletionFixture, embeddingFixture, speechFixture, transcriptionFixture,
		chatMultimodalFixture, responsesMultimodalFixture,
	} {
		fReq, fResp := fx()
		cases = append(cases, struct {
			name string
			req  *schemas.BifrostRequest
			resp *schemas.BifrostResponse
			err  *schemas.BifrostError
		}{name: string(fReq.RequestType), req: fReq, resp: fResp})
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want := legacyChatAttributes(tc.req, tc.resp, tc.err)

			data := BuildLLMSpanData(tc.req, tc.resp, tc.err, SpanBuildOptions{WantContent: true})
			got := data.Attributes()

			for key, wantVal := range want {
				gotVal, ok := got[key]
				if !ok {
					t.Errorf("missing key %q (legacy had %#v)", key, wantVal)
					continue
				}
				if !equalAttrFor(key, wantVal, gotVal) {
					t.Errorf("key %q = %#v, want %#v", key, gotVal, wantVal)
				}
			}
			for key, gotVal := range got {
				if _, ok := want[key]; !ok {
					t.Errorf("extra key %q = %#v, legacy emitted nothing", key, gotVal)
				}
			}
		})
	}
}

// jsonValuedAttrs carry marshalled JSON. Key order is not part of the contract
// (every consumer unmarshals), so these compare structurally rather than byte-wise.
var jsonValuedAttrs = map[string]bool{
	schemas.AttrInputMessages:  true,
	schemas.AttrOutputMessages: true,
	schemas.AttrTools:          true,
	schemas.AttrRespTools:      true,
	schemas.AttrRespMetadata:   true,
	schemas.AttrLogitBias:      true,
}

// equalAttrFor compares one attribute, structurally for JSON-valued keys.
func equalAttrFor(key string, want, got any) bool {
	if !jsonValuedAttrs[key] {
		return equalAttr(want, got)
	}
	ws, wok := want.(string)
	gs, gok := got.(string)
	if !wok || !gok {
		return equalAttr(want, got)
	}
	var wv, gv any
	if err := schemas.Unmarshal([]byte(ws), &wv); err != nil {
		return ws == gs
	}
	if err := schemas.Unmarshal([]byte(gs), &gv); err != nil {
		return ws == gs
	}
	return reflect.DeepEqual(wv, gv)
}

// equalAttr compares two attribute values, treating []string slices element-wise.
func equalAttr(a, b any) bool {
	as, aok := a.([]string)
	bs, bok := b.([]string)
	if aok || bok {
		if !aok || !bok || len(as) != len(bs) {
			return false
		}
		for i := range as {
			if as[i] != bs[i] {
				return false
			}
		}
		return true
	}
	return a == b
}

func responsesFixture() (*schemas.BifrostRequest, *schemas.BifrostResponse) {
	tier := schemas.BifrostServiceTier("priority")
	role := schemas.ResponsesInputMessageRoleUser
	msgType := schemas.ResponsesMessageTypeMessage
	req := &schemas.BifrostRequest{
		RequestType: schemas.ResponsesRequest,
		ResponsesRequest: &schemas.BifrostResponsesRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-5",
			Input: []schemas.ResponsesMessage{{
				Type:    &msgType,
				Role:    &role,
				Content: &schemas.ResponsesMessageContent{ContentStr: strPtr("summarize this")},
			}},
			Params: &schemas.ResponsesParameters{
				ParallelToolCalls: boolPtr(true),
				PromptCacheKey:    strPtr("cache-1"),
				SafetyIdentifier:  strPtr("safety-1"),
				ServiceTier:       &tier,
				Store:             boolPtr(false),
				Temperature:       f64Ptr(0.3),
				TopLogProbs:       intPtr(3),
				TopP:              f64Ptr(0.8),
				Truncation:        strPtr("auto"),
			},
		},
	}

	respID := "resp-1"
	resp := &schemas.BifrostResponse{
		ResponsesResponse: &schemas.BifrostResponsesResponse{
			ID:              &respID,
			Model:           "gpt-5-2026-01-01",
			ServiceTier:     &tier,
			MaxOutputTokens: intPtr(2048),
			MaxToolCalls:    intPtr(4),
			Store:           boolPtr(false),
			Temperature:     f64Ptr(0.3),
			TopP:            f64Ptr(0.8),
			Truncation:      strPtr("auto"),
			Usage: &schemas.ResponsesResponseUsage{
				InputTokens:  20,
				OutputTokens: 9,
				TotalTokens:  29,
				InputTokensDetails: &schemas.ResponsesResponseInputTokens{
					TextTokens: 18, CachedReadTokens: 6,
				},
				OutputTokensDetails: &schemas.ResponsesResponseOutputTokens{
					TextTokens: 9, ReasoningTokens: 4,
				},
			},
		},
	}
	return req, resp
}

func textCompletionFixture() (*schemas.BifrostRequest, *schemas.BifrostResponse) {
	req := &schemas.BifrostRequest{
		RequestType: schemas.TextCompletionRequest,
		TextCompletionRequest: &schemas.BifrostTextCompletionRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-3.5-turbo-instruct",
			Input:    &schemas.TextCompletionInput{PromptStr: strPtr("once upon a time")},
			Params: &schemas.TextCompletionParameters{
				MaxTokens:   intPtr(128),
				Temperature: f64Ptr(0.5),
				Stop:        []string{"\n"},
				BestOf:      intPtr(2),
				Echo:        boolPtr(true),
				LogProbs:    intPtr(3),
				N:           intPtr(1),
				Seed:        intPtr(42),
				Suffix:      strPtr(" the end"),
				User:        strPtr("user-2"),
			},
		},
	}
	resp := &schemas.BifrostResponse{
		TextCompletionResponse: &schemas.BifrostTextCompletionResponse{
			ID:     "cmpl-1",
			Model:  "gpt-3.5-turbo-instruct",
			Object: "text_completion",
			Choices: []schemas.BifrostResponseChoice{{
				FinishReason:                 strPtr("length"),
				TextCompletionResponseChoice: &schemas.TextCompletionResponseChoice{Text: strPtr("there was a gateway")},
			}},
			Usage: &schemas.BifrostLLMUsage{PromptTokens: 4, CompletionTokens: 5, TotalTokens: 9},
		},
	}
	return req, resp
}

func embeddingFixture() (*schemas.BifrostRequest, *schemas.BifrostResponse) {
	req := &schemas.BifrostRequest{
		RequestType: schemas.EmbeddingRequest,
		EmbeddingRequest: &schemas.BifrostEmbeddingRequest{
			Provider: schemas.OpenAI,
			Model:    "text-embedding-3-small",
			Input: schemas.EmbeddingInput{
				{Content: schemas.EmbeddingContent{{Type: schemas.EmbeddingContentPartTypeText, Text: strPtr("alpha")}}},
				{Content: schemas.EmbeddingContent{{Type: schemas.EmbeddingContentPartTypeText, Text: strPtr("beta")}}},
			},
			Params: &schemas.EmbeddingParameters{
				Dimensions:     intPtr(256),
				EncodingFormat: strPtr("float"),
			},
		},
	}
	resp := &schemas.BifrostResponse{
		EmbeddingResponse: &schemas.BifrostEmbeddingResponse{
			Usage: &schemas.BifrostLLMUsage{PromptTokens: 3, CompletionTokens: 0, TotalTokens: 3},
		},
	}
	return req, resp
}

func speechFixture() (*schemas.BifrostRequest, *schemas.BifrostResponse) {
	req := &schemas.BifrostRequest{
		RequestType: schemas.SpeechRequest,
		SpeechRequest: &schemas.BifrostSpeechRequest{
			Provider: schemas.OpenAI,
			Model:    "tts-1",
			Input:    &schemas.SpeechInput{Input: "read this aloud"},
			Params: &schemas.SpeechParameters{
				VoiceConfig:    &schemas.SpeechVoiceInput{Voice: strPtr("alloy")},
				Instructions:   "cheerful",
				ResponseFormat: "mp3",
				Speed:          f64Ptr(1.25),
			},
		},
	}
	resp := &schemas.BifrostResponse{
		SpeechResponse: &schemas.BifrostSpeechResponse{
			Usage: &schemas.SpeechUsage{InputTokens: 7, OutputTokens: 0, TotalTokens: 7},
		},
	}
	return req, resp
}

func transcriptionFixture() (*schemas.BifrostRequest, *schemas.BifrostResponse) {
	req := &schemas.BifrostRequest{
		RequestType: schemas.TranscriptionRequest,
		TranscriptionRequest: &schemas.BifrostTranscriptionRequest{
			Provider: schemas.OpenAI,
			Model:    "whisper-1",
			Params: &schemas.TranscriptionParameters{
				Language:       strPtr("en"),
				Prompt:         strPtr("technical audio"),
				ResponseFormat: strPtr("json"),
				Format:         strPtr("wav"),
			},
		},
	}
	total := 12
	in, out := 12, 0
	resp := &schemas.BifrostResponse{
		TranscriptionResponse: &schemas.BifrostTranscriptionResponse{
			Text: "hello from the gateway",
			Usage: &schemas.TranscriptionUsage{
				InputTokens:       &in,
				OutputTokens:      &out,
				TotalTokens:       &total,
				InputTokenDetails: &schemas.TranscriptionUsageInputTokenDetails{TextTokens: 4, AudioTokens: 8},
			},
		},
	}
	return req, resp
}

// chatMultimodalFixture exercises the attachment path: a data: URL reduces to
// media type and size, an https URL is carried as a reference.
func chatMultimodalFixture() (*schemas.BifrostRequest, *schemas.BifrostResponse) {
	req, resp := chatFixture()
	req.ChatRequest.Input = []schemas.ChatMessage{{
		Role: schemas.ChatMessageRoleUser,
		Content: &schemas.ChatMessageContent{ContentBlocks: []schemas.ChatContentBlock{
			{Type: schemas.ChatContentBlockTypeText, Text: strPtr("what is in these")},
			{Type: schemas.ChatContentBlockTypeImage, ImageURLStruct: &schemas.ChatInputImage{
				URL: "data:image/png;base64,aGVsbG93b3JsZA==", Detail: strPtr("high")}},
			{Type: schemas.ChatContentBlockTypeImage, ImageURLStruct: &schemas.ChatInputImage{
				URL: "https://example.com/cat.png"}},
		}},
	}}
	return req, resp
}

// responsesMultimodalFixture is the Responses-API counterpart. Before
// ExtractResponsesAttachments existed this content was dropped entirely.
func responsesMultimodalFixture() (*schemas.BifrostRequest, *schemas.BifrostResponse) {
	req, resp := responsesFixture()
	role := schemas.ResponsesInputMessageRoleUser
	msgType := schemas.ResponsesMessageTypeMessage
	req.ResponsesRequest.Input = []schemas.ResponsesMessage{{
		Type: &msgType,
		Role: &role,
		Content: &schemas.ResponsesMessageContent{ContentBlocks: []schemas.ResponsesMessageContentBlock{
			{Type: schemas.ResponsesInputMessageContentBlockTypeText, Text: strPtr("summarize the attachment")},
			{Type: schemas.ResponsesInputMessageContentBlockTypeFile,
				ResponsesInputMessageContentBlockFile: &schemas.ResponsesInputMessageContentBlockFile{
					FileURL:  strPtr("https://example.com/report.pdf"),
					Filename: strPtr("report.pdf"),
					FileType: strPtr("application/pdf"),
				}},
		}},
	}}
	return req, resp
}

// TestSpanDataCarriesAttachments pins the one behavioural change the typed path
// introduces: span content now describes non-text blocks, which the previous
// text-only extraction dropped. Bounded by design - an inline payload is
// reduced to media type and size, never copied.
func TestSpanDataCarriesAttachments(t *testing.T) {
	for _, tc := range []struct {
		name string
		fx   func() (*schemas.BifrostRequest, *schemas.BifrostResponse)
		want schemas.AttachmentSummary
	}{
		{"chat inline image", chatMultimodalFixture, schemas.AttachmentSummary{
			Kind: schemas.AttachmentImage, MediaType: "image/png", Detail: "high",
			Inline: true, ByteSize: 10}},
		{"responses file reference", responsesMultimodalFixture, schemas.AttachmentSummary{
			Kind: schemas.AttachmentFile, MediaType: "application/pdf",
			URL: "https://example.com/report.pdf", Filename: "report.pdf"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, _ := tc.fx()
			data := BuildLLMSpanData(req, nil, nil, SpanBuildOptions{WantContent: true})
			if len(data.InputMessages) == 0 {
				t.Fatal("no input messages")
			}
			got := data.InputMessages[0].Attachments
			if len(got) == 0 {
				t.Fatal("no attachments extracted")
			}
			if got[0] != tc.want {
				t.Errorf("attachment = %#v, want %#v", got[0], tc.want)
			}
			for _, a := range got {
				if a.Data != "" {
					t.Errorf("payload bytes copied by default: %#v", a)
				}
			}
		})
	}
}

// Raw is built only when a connector declared RawPayloadConsumer.
func TestRawPayloadsOnlyBuiltUnderDemand(t *testing.T) {
	resp := &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{ID: "c1", Model: "gpt-4o-mini"},
	}
	resp.ChatResponse.ExtraFields.RawRequest = map[string]any{"prompt": "secret-request"}
	resp.ChatResponse.ExtraFields.RawResponse = map[string]any{"text": "secret-response"}

	off := BuildLLMSpanData(nil, resp, nil, SpanBuildOptions{})
	if off.RawRequest != "" || off.RawResponse != "" {
		t.Errorf("raw built without demand: req=%q resp=%q", off.RawRequest, off.RawResponse)
	}
	if attrs := off.Attributes(); attrs[schemas.AttrBifrostRawRequest] != nil || attrs[schemas.AttrBifrostRawResponse] != nil {
		t.Error("raw attributes emitted without demand")
	}

	on := BuildLLMSpanData(nil, resp, nil, SpanBuildOptions{WantRawPayloads: true})
	if !strings.Contains(on.RawRequest, "secret-request") {
		t.Errorf("raw request not captured under demand: %q", on.RawRequest)
	}
	if !strings.Contains(on.RawResponse, "secret-response") {
		t.Errorf("raw response not captured under demand: %q", on.RawResponse)
	}
	// Even under demand, raw stays off span.Attributes.
	attrs := on.Attributes()
	if attrs[schemas.AttrBifrostRawRequest] != nil || attrs[schemas.AttrBifrostRawResponse] != nil {
		t.Error("raw leaked into span attributes under demand")
	}
}

// An oversized body is dropped, not truncated.
func TestRawPayloadsOverCapAreDropped(t *testing.T) {
	huge := strings.Repeat("x", schemas.RawPayloadCap+1)
	resp := &schemas.BifrostResponse{ChatResponse: &schemas.BifrostChatResponse{ID: "c1"}}
	resp.ChatResponse.ExtraFields.RawResponse = huge
	resp.ChatResponse.ExtraFields.RawRequest = "small"

	d := BuildLLMSpanData(nil, resp, nil, SpanBuildOptions{WantRawPayloads: true})
	if d.RawResponse != "" {
		t.Errorf("over-cap payload kept (%d bytes)", len(d.RawResponse))
	}
	if d.RawRequest != "small" {
		t.Errorf("under-cap payload dropped: %q", d.RawRequest)
	}
}

// Raw is content, so disable_content_logging strips it.
func TestRawPayloadsAreContentAttributes(t *testing.T) {
	for _, k := range []string{schemas.AttrBifrostRawRequest, schemas.AttrBifrostRawResponse} {
		if !schemas.IsContentAttribute(k) {
			t.Errorf("%s is not classified as content; disable_content_logging would not strip it", k)
		}
	}
}

// ---------------------------------------------------------------------------
// Client tool call items whose input does not live on `arguments`.
// ---------------------------------------------------------------------------

// customToolCallItem builds the item Codex emits for a custom tool.
func customToolCallItem(name, input string) schemas.ResponsesMessage {
	return schemas.ResponsesMessage{
		Type: schemas.Ptr(schemas.ResponsesMessageTypeCustomToolCall),
		ID:   schemas.Ptr("ctc_1"),
		ResponsesToolMessage: &schemas.ResponsesToolMessage{
			CallID:                  schemas.Ptr("call_1"),
			Name:                    schemas.Ptr(name),
			ResponsesCustomToolCall: &schemas.ResponsesCustomToolCall{Input: input},
		},
	}
}

// A custom_tool_call keeps its input on `input`; the old type tag dropped it.
func TestResponsesSummariesCarryCustomToolCallInput(t *testing.T) {
	const input = `{"cmd":"whoami","max_output_tokens":1000}`
	item := customToolCallItem("exec_command", input)

	for _, tc := range []struct {
		side string
		got  []ResponsesMessageSummary
	}{
		{"output", extractResponsesOutputMessages(&schemas.BifrostResponsesResponse{Output: []schemas.ResponsesMessage{item}})},
		{"input", extractResponsesInputMessages([]schemas.ResponsesMessage{item})},
	} {
		if len(tc.got) != 1 {
			t.Fatalf("%s: len = %d, want 1: %+v", tc.side, len(tc.got), tc.got)
		}
		summary := tc.got[0]
		if summary.Content != "" {
			t.Fatalf("%s: content = %q, want the call on tool_calls", tc.side, summary.Content)
		}
		if len(summary.ToolCalls) != 1 {
			t.Fatalf("%s: tool_calls = %+v, want 1", tc.side, summary.ToolCalls)
		}
		call := summary.ToolCalls[0]
		if call.Type != "custom" || call.Name != "exec_command" {
			t.Fatalf("%s: type/name = %q/%q, want custom/exec_command", tc.side, call.Type, call.Name)
		}
		if call.Args != input {
			t.Fatalf("%s: args = %q, want %q", tc.side, call.Args, input)
		}
		if call.ID != "ctc_1" {
			t.Fatalf("%s: id = %q, want ctc_1", tc.side, call.ID)
		}
	}
}

// local_shell_call keeps its command on `action`, code_interpreter_call on `code`.
func TestResponsesSummariesCarryShellAndInterpreterInput(t *testing.T) {
	output := []schemas.ResponsesMessage{
		{
			Type: schemas.Ptr(schemas.ResponsesMessageTypeLocalShellCall),
			ResponsesToolMessage: &schemas.ResponsesToolMessage{
				Action: &schemas.ResponsesToolMessageActionStruct{
					ResponsesLocalShellToolCallAction: &schemas.ResponsesLocalShellToolCallAction{
						Type:    "exec",
						Command: []string{"bash", "-lc", "whoami"},
					},
				},
			},
		},
		{
			Type: schemas.Ptr(schemas.ResponsesMessageTypeCodeInterpreterCall),
			ResponsesToolMessage: &schemas.ResponsesToolMessage{
				ResponsesCodeInterpreterToolCall: &schemas.ResponsesCodeInterpreterToolCall{
					Code: schemas.Ptr("print(1)"),
				},
			},
		},
	}

	got := extractResponsesOutputMessages(&schemas.BifrostResponsesResponse{Output: output})
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2: %+v", len(got), got)
	}

	shell := got[0].ToolCalls
	if len(shell) != 1 || shell[0].Type != "local_shell" || shell[0].Name != "local_shell" {
		t.Fatalf("shell tool call = %+v", shell)
	}
	if !strings.Contains(shell[0].Args, `"whoami"`) {
		t.Fatalf("shell args = %q, want the command", shell[0].Args)
	}

	interp := got[1].ToolCalls
	if len(interp) != 1 || interp[0].Type != "code_interpreter" || interp[0].Args != "print(1)" {
		t.Fatalf("interpreter tool call = %+v", interp)
	}
}

// Items with no model-generated input keep the placeholder tag.
func TestResponsesSummariesTagItemsWithoutInput(t *testing.T) {
	got := extractResponsesOutputMessages(&schemas.BifrostResponsesResponse{
		Output: []schemas.ResponsesMessage{{
			Type:                 schemas.Ptr(schemas.ResponsesMessageTypeFileSearchCall),
			ResponsesToolMessage: &schemas.ResponsesToolMessage{Name: schemas.Ptr("docs")},
		}},
	})
	if len(got) != 1 || got[0].Content != "[file_search_call] docs" {
		t.Fatalf("got = %+v, want the [file_search_call] docs tag", got)
	}
}

// End-to-end: a custom tool call must survive spanbuild and attribute marshaling,
// which is what a downstream sink (BigQuery, Datadog) actually reads.
func TestSpanAttributesCarryCustomToolCallInput(t *testing.T) {
	const input = `{"cmd":"whoami","max_output_tokens":1000}`
	resp := &schemas.BifrostResponse{ResponsesResponse: &schemas.BifrostResponsesResponse{
		ID:     schemas.Ptr("resp_1"),
		Model:  "gpt-5-codex",
		Output: []schemas.ResponsesMessage{customToolCallItem("exec_command", input)},
	}}
	req := &schemas.BifrostRequest{RequestType: schemas.ResponsesRequest}

	attrs := BuildLLMSpanData(req, resp, nil, SpanBuildOptions{WantContent: true}).Attributes()
	raw, ok := attrs[schemas.AttrOutputMessages].(string)
	if !ok {
		t.Fatalf("%s = %T, want JSON string", schemas.AttrOutputMessages, attrs[schemas.AttrOutputMessages])
	}
	if !strings.Contains(raw, `whoami`) || !strings.Contains(raw, `"name":"exec_command"`) {
		t.Fatalf("%s = %s, want the tool name and its input", schemas.AttrOutputMessages, raw)
	}

	// Tool args are content, so they must vanish when no connector reads content.
	noContent := BuildLLMSpanData(req, resp, nil, SpanBuildOptions{}).Attributes()
	if got, ok := noContent[schemas.AttrOutputMessages]; ok {
		t.Fatalf("%s = %v, want absent without WantContent", schemas.AttrOutputMessages, got)
	}
}

// A status-only error must keep its status; Error is optional.
func TestPopulateErrorAttributes_StatusOnlyError(t *testing.T) {
	attrs := PopulateErrorAttributes(&schemas.BifrostError{StatusCode: schemas.Ptr(429)})

	if got := schemas.GetIntAttr(attrs, schemas.AttrHTTPResponseStatusCode); got != 429 {
		t.Errorf("%s = %d, want 429", schemas.AttrHTTPResponseStatusCode, got)
	}
	for _, absent := range []string{schemas.AttrError, schemas.AttrErrorTypeSpec, schemas.AttrErrorCode} {
		if _, ok := attrs[absent]; ok {
			t.Errorf("%s should be absent when Error is nil, got %v", absent, attrs[absent])
		}
	}
}

func TestPopulateErrorAttributes_NilError(t *testing.T) {
	if attrs := PopulateErrorAttributes(nil); len(attrs) != 0 {
		t.Errorf("attrs = %v, want empty for a nil error", attrs)
	}
}

// A failed request carries its own raw bodies on BifrostError.ExtraFields. They used to
// be dropped entirely: ApplyResponse only reached buildResponseSide when resp != nil, and
// the raw assignment lived only there, so the provider's error body — the most useful
// thing to see for a failure — never reached a connector.
func TestRawPayloadsCapturedOnFailedRequest(t *testing.T) {
	newErr := func() *schemas.BifrostError {
		e := &schemas.BifrostError{Error: &schemas.ErrorField{Message: "model_not_found"}}
		e.ExtraFields.RawRequest = map[string]any{"prompt": "failed-request-body"}
		e.ExtraFields.RawResponse = map[string]any{"error": "failed-response-body"}
		return e
	}

	t.Run("captured under demand with no response", func(t *testing.T) {
		d := BuildLLMSpanData(nil, nil, newErr(), SpanBuildOptions{WantRawPayloads: true})
		if !strings.Contains(d.RawRequest, "failed-request-body") {
			t.Errorf("raw request not captured on failure: %q", d.RawRequest)
		}
		if !strings.Contains(d.RawResponse, "failed-response-body") {
			t.Errorf("raw response not captured on failure: %q", d.RawResponse)
		}
	})

	t.Run("still gated on demand", func(t *testing.T) {
		d := BuildLLMSpanData(nil, nil, newErr(), SpanBuildOptions{})
		if d.RawRequest != "" || d.RawResponse != "" {
			t.Errorf("raw built without demand: req=%q resp=%q", d.RawRequest, d.RawResponse)
		}
	})

	t.Run("via ApplyResponse, the live two-pass path", func(t *testing.T) {
		d := BuildLLMSpanData(nil, nil, nil, SpanBuildOptions{WantRawPayloads: true})
		ApplyResponse(d, nil, newErr(), SpanBuildOptions{WantRawPayloads: true})
		if !strings.Contains(d.RawRequest, "failed-request-body") {
			t.Errorf("raw request not captured via ApplyResponse: %q", d.RawRequest)
		}
		if !strings.Contains(d.RawResponse, "failed-response-body") {
			t.Errorf("raw response not captured via ApplyResponse: %q", d.RawResponse)
		}
	})

	// A cancelled stream arrives with BOTH an accumulated response and an error. The
	// response side runs first and has the richer bodies; the error side must not
	// overwrite them with its own.
	t.Run("does not clobber payloads the response side already set", func(t *testing.T) {
		resp := &schemas.BifrostResponse{
			ChatResponse: &schemas.BifrostChatResponse{ID: "c1", Model: "gpt-4o-mini"},
		}
		resp.ChatResponse.ExtraFields.RawRequest = map[string]any{"prompt": "accumulated-request"}
		resp.ChatResponse.ExtraFields.RawResponse = map[string]any{"text": "accumulated-response"}

		d := BuildLLMSpanData(nil, resp, newErr(), SpanBuildOptions{WantRawPayloads: true})
		if !strings.Contains(d.RawRequest, "accumulated-request") {
			t.Errorf("response-side raw request was overwritten: %q", d.RawRequest)
		}
		if !strings.Contains(d.RawResponse, "accumulated-response") {
			t.Errorf("response-side raw response was overwritten: %q", d.RawResponse)
		}
	})
}
