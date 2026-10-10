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

// findOpenAIRoute returns the POST route registered at path under the given
// prefix, or nil when none is.
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
// route is served at both SDK paths, under /openai and under every SDK prefix
// that reuses the OpenAI routes, and that it attaches no large-payload hook,
// which would leave the questions unparsed.
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
			if route.PreCallback != nil {
				t.Errorf("%s%s must not register a large-payload hook", prefix, path)
			}
		}
	}
}

// TestOpenAIDecisionsRouteConvertsRequest pins the route's request handling:
// an SDK body becomes the normalized request, questions keep their order and
// an unnamed question stays unnamed, a model without a prefix is OpenAI's, an
// unknown future field is marked to reach the provider wire, and fallbacks are
// kept for routing. The payload is synthetic.
func TestOpenAIDecisionsRouteConvertsRequest(t *testing.T) {
	route := findOpenAIRoute("/openai", "/openai/v1/decisions")
	if route == nil {
		t.Fatal("decisions route not registered")
	}

	incoming := route.GetRequestTypeInstance(context.Background())
	body := []byte(`{
		"model": "gpt-6-luna",
		"input": [{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "Inspect the product in this photo."}, {"type": "input_image", "image_url": "https://example.com/product.png", "detail": "high"}]}],
		"questions": [
			{"type": "predicate", "name": "visible_damage", "instructions": "Is the product damaged?"},
			{"type": "score", "instructions": "How severe is the damage?", "levels": [{"label": "Cosmetic"}, {"label": "Blocked"}]}
		],
		"fallbacks": ["typesafe/jev-1.13.0"],
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
	decision := converted.DecisionRequest
	if decision.Provider != schemas.OpenAI || decision.Model != "gpt-6-luna" {
		t.Errorf("got provider %q model %q, want openai gpt-6-luna", decision.Provider, decision.Model)
	}
	if decision.Input.NonTextPartType() != schemas.DecisionInputPartTypeImage {
		t.Error("input messages, with the image, were not kept")
	}
	if image := decision.Input.Messages[0].Content.Parts[1]; image.Detail == nil || *image.Detail != "high" {
		t.Errorf("the image detail level was not kept: %+v", image)
	}
	if len(decision.Questions) != 2 || *decision.Questions[0].Name != "visible_damage" || decision.Questions[1].Name != nil {
		t.Errorf("question order or the unnamed question was not kept: %+v", decision.Questions)
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
	want := schemas.Fallback{Provider: schemas.Typesafe, Model: "jev-1.13.0"}
	if len(decision.Fallbacks) != 1 || decision.Fallbacks[0] != want {
		t.Errorf("fallbacks = %+v, want [%+v]", decision.Fallbacks, want)
	}
}

// TestOpenAIDecisionsRouteRejectsOtherShapes pins that the route speaks only
// OpenAI's body: a Typesafe-shaped body (questions keyed by name) does not
// decode, and a body without input or questions, with Typesafe's criteria on a
// question, or with a stray state next to input is a 400, so a stray state can
// never reach a Typesafe model as its state.
func TestOpenAIDecisionsRouteRejectsOtherShapes(t *testing.T) {
	route := findOpenAIRoute("/openai", "/openai/v1/decisions")
	if route == nil {
		t.Fatal("decisions route not registered")
	}
	typesafeShaped := route.GetRequestTypeInstance(context.Background())
	if err := parseJSONRequestBody([]byte(`{"model":"typesafe/jev-1.13.0","state":"s","questions":{"q":{"type":"noul"}}}`), typesafeShaped); err == nil {
		t.Error("a Typesafe-shaped body must not decode on the OpenAI route")
	}
	for _, body := range []string{
		`{"model":"gpt-6-luna","questions":[{"type":"predicate","instructions":"Q?"}]}`,
		`{"model":"gpt-6-luna","input":"text","questions":[]}`,
		`{"model":"typesafe/jev-1.13.0","input":"text","questions":[{"type":"predicate","instructions":"Q?","criteria":{"true":"yes"}}]}`,
		`{"model":"typesafe/jev-1.13.0","state":"stale","input":"text","questions":[{"type":"predicate","instructions":"Q?"}]}`,
	} {
		incoming := route.GetRequestTypeInstance(context.Background())
		if err := parseJSONRequestBody([]byte(body), incoming); err != nil {
			t.Fatalf("parse %s: %v", body, err)
		}
		if _, err := route.RequestConverter(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), incoming); err == nil {
			t.Errorf("expected rejection for %s", body)
		}
	}
}

// TestOpenAIDecisionsRouteResponses pins the route's response handling: the
// normalized response is rendered in OpenAI's shape whichever provider served
// it, so a change a plugin made is what the client receives, and the upstream
// body is relayed only when raw responses were asked for, including for a
// custom OpenAI-based provider. Rendered answers carry name null when their
// question had none, usage keeps OpenAI's names and token details, Bifrost's
// extra_fields come along as on the other OpenAI routes, and Laya's fields are
// kept. Errors pass through as on the other OpenAI routes. The
// payloads are synthetic.
func TestOpenAIDecisionsRouteResponses(t *testing.T) {
	route := findOpenAIRoute("/openai", "/openai/v1/decisions")
	if route == nil {
		t.Fatal("decisions route not registered")
	}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	render := func(ctx *schemas.BifrostContext, resp *schemas.BifrostDecisionResponse) map[string]any {
		t.Helper()
		out, err := route.DecisionResponseConverter(ctx, resp)
		if err != nil {
			t.Fatalf("convert: %v", err)
		}
		encoded, err := json.Marshal(out)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var wire map[string]any
		if err := json.Unmarshal(encoded, &wire); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return wire
	}

	// OpenAI answered 0.99; a plugin changed the normalized answer to 0.01.
	served := &schemas.BifrostDecisionResponse{
		ID:      "dec_1",
		Model:   "gpt-6-luna",
		Answers: []schemas.DecisionAnswer{{Type: schemas.DecisionTypePredicate, Name: schemas.Ptr("q"), Probability: schemas.Ptr(0.01)}},
		Usage: &schemas.BifrostLLMUsage{PromptTokens: 120, TotalTokens: 120,
			PromptTokensDetails: &schemas.ChatPromptTokensDetails{CachedReadTokens: 30}},
	}
	served.ExtraFields.Provider = schemas.OpenAI
	wire := render(ctx, served)
	if _, hasID := wire["id"]; hasID {
		t.Errorf("OpenAI's Decision object has no id, got %v", wire["id"])
	}
	if extra, _ := wire["extra_fields"].(map[string]any); extra["provider"] != "openai" {
		t.Errorf("extra_fields must come along as on the other OpenAI routes, got %v", wire["extra_fields"])
	}
	if got := wire["answers"].([]any)[0].(map[string]any)["probability"]; got != 0.01 {
		t.Errorf("the normalized answer must reach the client, got probability %v", got)
	}
	details, _ := wire["usage"].(map[string]any)["input_tokens_details"].(map[string]any)
	if details["cached_tokens"] != 30.0 {
		t.Errorf("cached input tokens must survive: %v", wire["usage"])
	}

	raw := json.RawMessage(`{"id":"dec_1","model":"gpt-6-luna","answers":[{"type":"predicate","name":"q","probability":0.99}],"future_field":true}`)
	served.ExtraFields.RawResponse = raw
	if out, err := route.DecisionResponseConverter(ctx, served); err != nil || string(out.(json.RawMessage)) != string(raw) {
		t.Errorf("raw response not relayed when asked for: %#v (%v)", out, err)
	}
	customCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	customCtx.SetValue(schemas.BifrostContextKeyBaseProviderType, schemas.OpenAI)
	served.ExtraFields.Provider = "my-openai"
	if out, err := route.DecisionResponseConverter(customCtx, served); err != nil || string(out.(json.RawMessage)) != string(raw) {
		t.Errorf("custom OpenAI provider's raw response not relayed when asked for: %#v (%v)", out, err)
	}

	stateTokens, answerConfidence := 13, 0.97
	laya := &schemas.BifrostDecisionResponse{
		Model: "laya-rl-agent",
		Answers: []schemas.DecisionAnswer{
			{Type: schemas.DecisionTypeChoice, Name: schemas.Ptr("category"), Choice: &schemas.DecisionScalar{Str: schemas.Ptr("billing")}, AnswerConfidence: &answerConfidence},
			{Type: schemas.DecisionTypePredicate, Probability: schemas.Ptr(0.2)},
		},
		Usage:   &schemas.BifrostLLMUsage{PromptTokens: 39, TotalTokens: 39, StateTokens: &stateTokens},
		Routing: json.RawMessage(`{"model":"english"}`),
	}
	laya.ExtraFields.Provider = "laya"
	wire = render(ctx, laya)
	wantUsage := map[string]any{"input_tokens": 39.0, "output_tokens": 0.0, "total_tokens": 39.0, "state_tokens": 13.0}
	if got := wire["usage"]; !jsonEqual(got, wantUsage) {
		t.Errorf("usage = %v, want %v", got, wantUsage)
	}
	answers := wire["answers"].([]any)
	if len(answers) != 2 || answers[0].(map[string]any)["answer_confidence"] != 0.97 {
		t.Errorf("answers must keep their order and Laya's fields: %v", answers)
	}
	if name, named := answers[1].(map[string]any)["name"]; !named || name != nil {
		t.Errorf("an unnamed question's answer carries name null, as OpenAI's do: %v", answers[1])
	}
	if !jsonEqual(wire["routing"], map[string]any{"model": "english"}) {
		t.Errorf("routing = %v", wire["routing"])
	}

	bifrostErr := &schemas.BifrostError{Error: &schemas.ErrorField{Message: "Invalid value for 'questions': expected an array."}}
	if got := route.ErrorConverter(ctx, bifrostErr); got != bifrostErr {
		t.Errorf("error must pass through as on the other OpenAI routes, got %#v", got)
	}
}

// jsonEqual reports whether two decoded JSON values are equal.
func jsonEqual(a, b any) bool {
	left, _ := json.Marshal(a)
	right, _ := json.Marshal(b)
	return bytes.Equal(left, right)
}
