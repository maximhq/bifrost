package integrations

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"testing"

	"github.com/maximhq/bifrost/core/providers/openai"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// buildFileUploadCtx builds a fasthttp request context carrying a multipart
// file upload body with the given extra form fields.
func buildFileUploadCtx(t *testing.T, fields map[string]string) *fasthttp.RequestCtx {
	t.Helper()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "test.png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte("fake-png-bytes")); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteField("purpose", "vision"); err != nil {
		t.Fatal(err)
	}
	for k, v := range fields {
		if err := writer.WriteField(k, v); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetContentType(writer.FormDataContentType())
	ctx.Request.SetBody(body.Bytes())
	return ctx
}

func TestParseOpenAIFileUploadMultipartRequest_ExpiresAfter(t *testing.T) {
	tests := []struct {
		name    string
		fields  map[string]string
		want    *schemas.FileExpiresAfter
		wantErr bool
	}{
		{
			name: "both fields present",
			fields: map[string]string{
				"expires_after[anchor]":  "created_at",
				"expires_after[seconds]": "3600",
			},
			want: &schemas.FileExpiresAfter{Anchor: "created_at", Seconds: 3600},
		},
		{
			name:   "neither field present",
			fields: nil,
			want:   nil,
		},
		{
			// Partial input is passed through; the upstream provider rejects it.
			name: "anchor only",
			fields: map[string]string{
				"expires_after[anchor]": "created_at",
			},
			want: &schemas.FileExpiresAfter{Anchor: "created_at", Seconds: 0},
		},
		{
			name: "seconds only",
			fields: map[string]string{
				"expires_after[seconds]": "3600",
			},
			want: &schemas.FileExpiresAfter{Anchor: "", Seconds: 3600},
		},
		{
			// Out-of-range values are passed through; the upstream provider rejects them.
			name: "out-of-range seconds",
			fields: map[string]string{
				"expires_after[anchor]":  "created_at",
				"expires_after[seconds]": "100",
			},
			want: &schemas.FileExpiresAfter{Anchor: "created_at", Seconds: 100},
		},
		{
			name: "non-numeric seconds",
			fields: map[string]string{
				"expires_after[anchor]":  "created_at",
				"expires_after[seconds]": "not-a-number",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := buildFileUploadCtx(t, tt.fields)
			uploadReq := &schemas.BifrostFileUploadRequest{}
			err := parseOpenAIFileUploadMultipartRequest(ctx, uploadReq)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if uploadReq.Purpose != schemas.FilePurpose("vision") {
				t.Errorf("purpose = %q, want %q", uploadReq.Purpose, "vision")
			}
			if tt.want == nil {
				if uploadReq.ExpiresAfter != nil {
					t.Errorf("ExpiresAfter = %+v, want nil", uploadReq.ExpiresAfter)
				}
				return
			}
			if uploadReq.ExpiresAfter == nil {
				t.Fatal("ExpiresAfter is nil, expected it to be populated")
			}
			if *uploadReq.ExpiresAfter != *tt.want {
				t.Errorf("ExpiresAfter = %+v, want %+v", *uploadReq.ExpiresAfter, *tt.want)
			}
		})
	}
}

// buildTranscriptionMultipartCtx builds a fasthttp request context carrying
// a multipart transcription upload body with the given extra form fields.
// Fields with multiple values (e.g. repeated "timestamp_granularities[]")
// are written once per slice element, preserving order.
func buildTranscriptionMultipartCtx(t *testing.T, fields map[string][]string) *fasthttp.RequestCtx {
	t.Helper()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("model", "gpt-4o-transcribe-diarize"); err != nil {
		t.Fatal(err)
	}
	part, err := writer.CreateFormFile("file", "sample.mp3")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte("fake-audio-bytes")); err != nil {
		t.Fatal(err)
	}
	for k, values := range fields {
		for _, v := range values {
			if err := writer.WriteField(k, v); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetContentType(writer.FormDataContentType())
	ctx.Request.SetBody(body.Bytes())
	return ctx
}

func TestParseTranscriptionMultipartRequest_ExtraParamsPassthrough(t *testing.T) {
	ctx := buildTranscriptionMultipartCtx(t, map[string][]string{
		"diarize":            {"true"},      // ElevenLabs-specific: JSON-decodable (bool)
		"num_speakers":       {"2"},         // ElevenLabs-specific: JSON-decodable (number)
		"unstructured_extra": {"not-json{"}, // falls back to raw string on decode failure
		"chunking_strategy":  {"auto"},      // has its own dedicated handling; must not also appear via the generic passthrough
	})

	req := &openai.OpenAITranscriptionRequest{}
	if err := parseTranscriptionMultipartRequest(ctx, req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if req.ExtraParams["diarize"] != true {
		t.Errorf("ExtraParams[diarize] = %#v, want true (bool)", req.ExtraParams["diarize"])
	}
	if req.ExtraParams["num_speakers"] != float64(2) {
		t.Errorf("ExtraParams[num_speakers] = %#v, want float64(2)", req.ExtraParams["num_speakers"])
	}
	if req.ExtraParams["unstructured_extra"] != "not-json{" {
		t.Errorf("ExtraParams[unstructured_extra] = %#v, want raw string fallback", req.ExtraParams["unstructured_extra"])
	}

	// chunking_strategy must be set exactly once, via its own dedicated
	// handling above the generic passthrough loop, not duplicated.
	if req.ExtraParams["chunking_strategy"] != "auto" {
		t.Errorf("ExtraParams[chunking_strategy] = %#v, want %q", req.ExtraParams["chunking_strategy"], "auto")
	}
}

func TestParseTranscriptionMultipartRequest_TypedFieldsNotShadowedByExtraParams(t *testing.T) {
	ctx := buildTranscriptionMultipartCtx(t, map[string][]string{
		"temperature":               {"0.3"},
		"timestamp_granularities[]": {"word", "segment"},
	})

	req := &openai.OpenAITranscriptionRequest{}
	if err := parseTranscriptionMultipartRequest(ctx, req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if req.Temperature == nil || *req.Temperature != 0.3 {
		t.Fatalf("Temperature = %v, want *0.3", req.Temperature)
	}
	if len(req.TimestampGranularities) != 2 || req.TimestampGranularities[0] != "word" || req.TimestampGranularities[1] != "segment" {
		t.Fatalf("TimestampGranularities = %v, want [word segment]", req.TimestampGranularities)
	}
	// These are known/typed fields now, so they must not also leak into
	// ExtraParams via the generic passthrough loop.
	if _, ok := req.ExtraParams["temperature"]; ok {
		t.Errorf("temperature leaked into ExtraParams: %#v", req.ExtraParams["temperature"])
	}
	if _, ok := req.ExtraParams["timestamp_granularities[]"]; ok {
		t.Errorf("timestamp_granularities[] leaked into ExtraParams: %#v", req.ExtraParams["timestamp_granularities[]"])
	}
}

// TestParseTranscriptionMultipartRequest_PreservesFilename covers issue #5670:
// the multipart part's filename must reach the parsed request, otherwise the
// provider re-derives it from magic bytes and mislabels non-sniffable
// containers (m4a/mp4/webm) as audio.mp3, which OpenAI rejects.
func TestParseTranscriptionMultipartRequest_PreservesFilename(t *testing.T) {
	ctx := buildTranscriptionMultipartCtx(t, nil)

	req := &openai.OpenAITranscriptionRequest{}
	if err := parseTranscriptionMultipartRequest(ctx, req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if req.Filename != "sample.mp3" {
		t.Errorf("Filename = %q, want %q (multipart part filename dropped)", req.Filename, "sample.mp3")
	}
}

// findOpenAIRoute returns the route registered at path under the given prefix,
// or nil when none is.
func findOpenAIRoute(prefix, path string) *RouteConfig {
	for _, route := range CreateOpenAIRouteConfigs(prefix, &mockHandlerStore{}) {
		if route.Path == path && route.Method == "POST" {
			rc := route
			return &rc
		}
	}
	return nil
}

// TestOpenAIDecisionsRouteRegisteredOnEveryPath pins that OpenAI's decisions
// shape is served at both SDK paths, under /openai and under every SDK prefix
// that reuses the OpenAI routes.
func TestOpenAIDecisionsRouteRegisteredOnEveryPath(t *testing.T) {
	for _, prefix := range []string{"/openai", "/langchain", "/litellm", "/pydanticai"} {
		for _, path := range []string{"/v1/decisions", "/decisions"} {
			route := findOpenAIRoute(prefix, prefix+path)
			if route == nil {
				t.Errorf("POST %s%s not registered", prefix, path)
				continue
			}
			if got := route.GetHTTPRequestType(nil); got != schemas.DecisionRequest {
				t.Errorf("%s%s request type = %s, want %s", prefix, path, got, schemas.DecisionRequest)
			}
		}
	}
}

// TestOpenAIDecisionsRouteConvertsRequest pins the route's request handling: an
// SDK body becomes an ordered request for OpenAI, an unknown future field is
// marked to reach the provider wire, and fallbacks are kept for gateway
// routing. The payload is synthetic.
func TestOpenAIDecisionsRouteConvertsRequest(t *testing.T) {
	route := findOpenAIRoute("/openai", "/openai/v1/decisions")
	if route == nil {
		t.Fatal("decisions route not registered")
	}

	incoming := route.GetRequestTypeInstance(context.Background())
	body := []byte(`{
		"model": "gpt-6-luna",
		"input": [{"role": "user", "content": [{"type": "input_text", "text": "Inspect the product in this photo."}, {"type": "input_image", "image_url": "data:image/png;base64,AAAA"}]}],
		"questions": [
			{"type": "predicate", "name": "visible_damage", "instructions": "Is the product damaged?"},
			{"type": "score", "instructions": "How severe is the damage?", "levels": [{"label": "Cosmetic"}, {"label": "Blocked"}]}
		],
		"fallbacks": ["gpt-6-luna-2026-09-01"],
		"future_option": {"mode": "strict"}
	}`)
	if err := parseJSONRequestBody(body, incoming); err != nil {
		t.Fatalf("parse: %v", err)
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	converted, err := route.RequestConverter(ctx, incoming)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if converted == nil || converted.DecisionRequest == nil {
		t.Fatal("decision request not produced")
	}
	decision := converted.DecisionRequest
	if decision.Provider != schemas.OpenAI || decision.Model != "gpt-6-luna" {
		t.Errorf("got provider %q model %q, want openai gpt-6-luna", decision.Provider, decision.Model)
	}
	if !decision.UsesOrderedForm() || decision.UsesMapForm() {
		t.Error("route request must be in the ordered form only")
	}
	if len(decision.OrderedQuestions) != 2 || decision.OrderedQuestions[1].Name != nil {
		t.Errorf("question order or the unnamed question was not kept: %+v", decision.OrderedQuestions)
	}
	if _, ok := decision.ExtraParams["future_option"]; !ok {
		t.Error("unknown field was dropped instead of carried to the provider")
	}
	if passthrough, _ := ctx.Value(schemas.BifrostContextKeyPassthroughExtraParams).(bool); !passthrough {
		t.Error("unknown fields present but passthrough not requested; they would be dropped on the wire")
	}

	router := &GenericRouter{}
	if err := router.extractAndParseFallbacks(ctx, incoming, converted); err != nil {
		t.Fatalf("fallbacks: %v", err)
	}
	want := schemas.Fallback{Provider: schemas.OpenAI, Model: "gpt-6-luna-2026-09-01"}
	if len(decision.Fallbacks) != 1 || decision.Fallbacks[0] != want {
		t.Errorf("fallbacks = %+v, want [%+v]", decision.Fallbacks, want)
	}
}

// TestOpenAIDecisionsRouteResponses pins the route's response handling: the
// native body is relayed when an OpenAI-based provider served the request,
// including a custom provider, and the wire shape is rebuilt from the typed
// answers otherwise. Errors use the OpenAI error envelope like the other
// OpenAI routes.
func TestOpenAIDecisionsRouteResponses(t *testing.T) {
	route := findOpenAIRoute("/openai", "/openai/v1/decisions")
	if route == nil {
		t.Fatal("decisions route not registered")
	}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	native := `{"id":"dec_1","model":"gpt-6-luna","answers":[{"type":"predicate","name":"q","probability":0.4}],"future_field":true}`
	resp := &schemas.BifrostDecisionResponse{
		ID:    "dec_1",
		Model: "gpt-6-luna",
		OrderedAnswers: []schemas.DecisionOrderedAnswer{
			{Type: schemas.DecisionOrderedKindPredicate, Name: schemas.Ptr("q"), Probability: schemas.Ptr(0.4)},
			{Type: schemas.DecisionOrderedKindRefusal, Name: schemas.Ptr("r")},
		},
		NativeResponse: json.RawMessage(native),
	}
	resp.ExtraFields.Provider = schemas.OpenAI
	out, err := route.DecisionResponseConverter(ctx, resp)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if got, ok := out.(json.RawMessage); !ok || string(got) != native {
		t.Errorf("native body not relayed verbatim: %#v", out)
	}

	customCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	customCtx.SetValue(schemas.BifrostContextKeyBaseProviderType, schemas.OpenAI)
	resp.ExtraFields.Provider = "my-openai"
	out, err = route.DecisionResponseConverter(customCtx, resp)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if got, ok := out.(json.RawMessage); !ok || string(got) != native {
		t.Errorf("custom OpenAI provider's native body not relayed verbatim: %#v", out)
	}

	resp.NativeResponse = nil
	resp.ExtraFields.Provider = schemas.OpenAI
	out, err = route.DecisionResponseConverter(ctx, resp)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	built, ok := out.(*openai.OpenAIDecisionResponse)
	if !ok {
		t.Fatalf("expected the typed wire response without a native body, got %#v", out)
	}
	if len(built.Answers) != 2 || built.Answers[1].Type != schemas.DecisionOrderedKindRefusal {
		t.Errorf("typed response must keep answer order and the refusal: %+v", built.Answers)
	}

	bifrostErr := &schemas.BifrostError{Error: &schemas.ErrorField{Message: "Invalid value for 'questions': expected an array."}}
	if got := route.ErrorConverter(ctx, bifrostErr); got != bifrostErr {
		t.Errorf("error must be returned in the OpenAI envelope, got %#v", got)
	}
}
