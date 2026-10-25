package integrations

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/fasthttp/router"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/providers/openai"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
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
// an unnamed question stays unnamed, a model without a prefix is left for
// routing to resolve, an unknown future field is marked to reach the provider
// wire, and fallbacks are kept for routing. The payload is synthetic.
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
	if decision.Provider != "" || decision.Model != "gpt-6-luna" {
		t.Errorf("got provider %q model %q, want unresolved provider and gpt-6-luna", decision.Provider, decision.Model)
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

// speechTestAccount configures OpenAI and Azure providers whose endpoints point at a local
// upstream, so speech routes run end to end without a live key. The Azure-style deployments
// route always resolves to Azure, the plain speech route to the provider in the model prefix.
type speechTestAccount struct {
	baseURL string
}

func (a speechTestAccount) GetConfiguredProviders() ([]schemas.ModelProvider, error) {
	return []schemas.ModelProvider{schemas.OpenAI, schemas.Azure}, nil
}

func (a speechTestAccount) GetKeysForProvider(_ context.Context, p schemas.ModelProvider) ([]schemas.Key, error) {
	key := schemas.Key{
		ID:     "speech-test-key-" + string(p),
		Value:  *schemas.NewSecretVar("sk-test-operator-key"),
		Models: schemas.WhiteList{"*"},
		Weight: 1.0,
	}
	switch p {
	case schemas.OpenAI:
	case schemas.Azure:
		key.AzureKeyConfig = &schemas.AzureKeyConfig{Endpoint: *schemas.NewSecretVar(a.baseURL)}
	default:
		return nil, nil
	}
	return []schemas.Key{key}, nil
}

func (a speechTestAccount) GetConfigForProvider(p schemas.ModelProvider) (*schemas.ProviderConfig, error) {
	if p != schemas.OpenAI && p != schemas.Azure {
		return nil, fmt.Errorf("unsupported provider %s", p)
	}
	nc := schemas.DefaultNetworkConfig
	if p == schemas.OpenAI {
		nc.BaseURL = a.baseURL
	}
	return &schemas.ProviderConfig{
		NetworkConfig:            nc,
		ConcurrencyAndBufferSize: schemas.DefaultConcurrencyAndBufferSize,
	}, nil
}

// TestOpenAISpeechRoutesReturnRawAudio pins that every OpenAI-integration speech route
// answers with the audio bytes, as the OpenAI and Azure OpenAI SDKs expect. The Azure-style
// /openai/deployments/{deployment}/audio/speech route used to serialize the whole
// BifrostSpeechResponse as JSON ({"audio":"<base64>"}) instead.
func TestOpenAISpeechRoutesReturnRawAudio(t *testing.T) {
	audio := []byte("ID3\x04\x00fake-mp3-frames")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write(audio)
	}))
	defer upstream.Close()

	client, err := bifrost.Init(context.Background(), schemas.BifrostConfig{
		Account: speechTestAccount{baseURL: upstream.URL},
		Logger:  bifrost.NewDefaultLogger(schemas.LogLevelError),
	})
	require.NoError(t, err)
	defer client.Shutdown()

	r := router.New()
	identity := func(next fasthttp.RequestHandler) fasthttp.RequestHandler { return next }
	NewOpenAIRouter(client, &mockHandlerStore{}, nil, &testLogger{}).RegisterRoutes(r, identity)

	for _, uri := range []string{
		"/openai/v1/audio/speech",
		"/openai/openai/deployments/gpt-4o-mini-tts/audio/speech?api-version=2025-04-01-preview",
	} {
		t.Run(uri, func(t *testing.T) {
			var ctx fasthttp.RequestCtx
			ctx.Request.Header.SetMethod(fasthttp.MethodPost)
			ctx.Request.SetRequestURI(uri)
			ctx.Request.Header.SetContentType("application/json")
			ctx.Request.SetBodyString(`{"model":"openai/gpt-4o-mini-tts","input":"Hello from the harness.","voice":"alloy"}`)

			r.Handler(&ctx)

			require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), "body: %s", ctx.Response.Body())
			require.Contains(t, string(ctx.Response.Header.ContentType()), "audio/", "body: %.200s", ctx.Response.Body())
			require.Equal(t, audio, ctx.Response.Body())
		})
	}
}

// azureDataSourcesJSON is an Azure "On Your Data" data_sources value, the
// extension the Azure OpenAI SDK sends at the top level of a chat body.
const azureDataSourcesJSON = `[{"type":"azure_search","parameters":{"endpoint":"https://unreachable.search.windows.net","index_name":"idx","authentication":{"type":"api_key","key":"k"}}}]`

// chatUpstream records the last chat body the provider sent and answers it,
// as SSE when the body asked to stream.
type chatUpstream struct {
	mu   sync.Mutex
	path string
	body []byte
}

func (u *chatUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	u.mu.Lock()
	u.path, u.body = r.URL.Path, body
	u.mu.Unlock()
	var probe struct {
		Stream bool `json:"stream"`
	}
	_ = json.Unmarshal(body, &probe)
	if probe.Stream {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"id":"c1","object":"chat.completion","created":1,"model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
}

func (u *chatUpstream) last() (string, []byte) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.path, u.body
}

// TestAzureDeploymentChatForwardsUnknownTopLevelFields pins that the Azure
// OpenAI SDK route /openai/deployments/{deployment}/chat/completions forwards
// top-level fields Bifrost does not model, such as On Your Data data_sources,
// to the Azure deployment, unary and streaming. They used to be dropped at
// decode, so Azure answered 200 for a body it should have rejected. The plain
// chat routes and a provider-prefixed deployment path keep dropping them.
func TestAzureDeploymentChatForwardsUnknownTopLevelFields(t *testing.T) {
	upstream := &chatUpstream{}
	server := httptest.NewServer(upstream)
	defer server.Close()

	client, err := bifrost.Init(context.Background(), schemas.BifrostConfig{
		Account: speechTestAccount{baseURL: server.URL},
		Logger:  bifrost.NewDefaultLogger(schemas.LogLevelError),
	})
	require.NoError(t, err)
	defer client.Shutdown()

	r := router.New()
	identity := func(next fasthttp.RequestHandler) fasthttp.RequestHandler { return next }
	NewOpenAIRouter(client, &mockHandlerStore{}, nil, &testLogger{}).RegisterRoutes(r, identity)

	const deploymentURI = "/openai/openai/deployments/gpt-4o/chat/completions?api-version=2024-10-21"
	cases := []struct {
		name        string
		uri         string
		body        string
		headers     map[string]string
		wantPath    string
		wantFields  map[string]string
		wantMissing []string
	}{
		{
			name:       "azure deployment unary",
			uri:        deploymentURI,
			body:       `{"messages":[{"role":"user","content":"hi"}],"temperature":0.2,"data_sources":` + azureDataSourcesJSON + `,"x_future_field":{"a":1}}`,
			wantPath:   "/openai/v1/chat/completions",
			wantFields: map[string]string{"data_sources": azureDataSourcesJSON, "x_future_field": `{"a":1}`, "temperature": "0.2"},
		},
		{
			name:       "azure deployment stream",
			uri:        deploymentURI,
			body:       `{"messages":[{"role":"user","content":"hi"}],"stream":true,"data_sources":` + azureDataSourcesJSON + `}`,
			wantPath:   "/openai/v1/chat/completions",
			wantFields: map[string]string{"data_sources": azureDataSourcesJSON, "stream": "true"},
		},
		{
			name:        "azure deployment nested extra_params wins on conflict",
			uri:         deploymentURI,
			body:        `{"messages":[{"role":"user","content":"hi"}],"data_sources":` + azureDataSourcesJSON + `,"x_dup":"top","extra_params":{"x_dup":"nested"}}`,
			headers:     map[string]string{"x-bf-passthrough-extra-params": "true"},
			wantPath:    "/openai/v1/chat/completions",
			wantFields:  map[string]string{"data_sources": azureDataSourcesJSON, "x_dup": `"nested"`},
			wantMissing: []string{"extra_params"},
		},
		{
			name:        "azure deployment without unknown fields",
			uri:         deploymentURI,
			body:        `{"messages":[{"role":"user","content":"hi"}],"reasoning_effort":"low","fallbacks":["openai/gpt-4o"]}`,
			wantPath:    "/openai/v1/chat/completions",
			wantMissing: []string{"fallbacks", "extra_params"},
		},
		{
			name:        "plain chat route keeps dropping unknown fields",
			uri:         "/openai/v1/chat/completions",
			body:        `{"model":"azure/gpt-4o","messages":[{"role":"user","content":"hi"}],"data_sources":` + azureDataSourcesJSON + `}`,
			wantPath:    "/openai/v1/chat/completions",
			wantMissing: []string{"data_sources"},
		},
		{
			name:        "provider-prefixed deployment path does not forward Azure fields",
			uri:         "/openai/openai/deployments/openai/gpt-4o/chat/completions?api-version=2024-10-21",
			body:        `{"messages":[{"role":"user","content":"hi"}],"data_sources":` + azureDataSourcesJSON + `}`,
			wantPath:    "/v1/chat/completions",
			wantMissing: []string{"data_sources"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var ctx fasthttp.RequestCtx
			ctx.Request.Header.SetMethod(fasthttp.MethodPost)
			ctx.Request.SetRequestURI(tc.uri)
			ctx.Request.Header.SetContentType("application/json")
			for k, v := range tc.headers {
				ctx.Request.Header.Set(k, v)
			}
			ctx.Request.SetBodyString(tc.body)

			r.Handler(&ctx)
			respBody := string(ctx.Response.Body())
			require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), "body: %s", respBody)

			path, sent := upstream.last()
			require.Equal(t, tc.wantPath, path, "upstream body: %s", sent)
			for field, want := range tc.wantFields {
				got := gjson.GetBytes(sent, field)
				require.True(t, got.Exists(), "%s missing from the provider body: %s", field, sent)
				require.JSONEq(t, want, got.Raw, "%s in provider body: %s", field, sent)
			}
			for _, field := range tc.wantMissing {
				require.False(t, gjson.GetBytes(sent, field).Exists(), "%s must not reach the provider: %s", field, sent)
			}
		})
	}
}

// BenchmarkAzureEndpointPreHookChat measures the Azure deployments pre-hook on
// a chat body with no unknown fields (the common case) and with data_sources.
func BenchmarkAzureEndpointPreHookChat(b *testing.B) {
	hook := AzureEndpointPreHook(&mockHandlerStore{})
	bodies := map[string]string{
		"known-only":   `{"messages":[{"role":"system","content":"be brief"},{"role":"user","content":"hi"}],"temperature":0.2,"max_tokens":64,"stream":true,"stream_options":{"include_usage":true}}`,
		"data-sources": `{"messages":[{"role":"system","content":"be brief"},{"role":"user","content":"hi"}],"temperature":0.2,"max_tokens":64,"data_sources":` + azureDataSourcesJSON + `}`,
	}
	for _, name := range []string{"known-only", "data-sources"} {
		body := []byte(bodies[name])
		b.Run(name, func(b *testing.B) {
			var ctx fasthttp.RequestCtx
			ctx.Request.SetBody(body)
			ctx.SetUserValue("deploymentPath", "gpt-4o/chat/completions")
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				bctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
				req := &openai.OpenAIChatRequest{}
				if err := hook(&ctx, bctx, req); err != nil {
					b.Fatal(err)
				}
				bctx.Cancel()
			}
		})
	}
}

// A request Bifrost converted to another API (chat served through Responses, text
// through chat, Responses through chat) carries that API's raw upstream bytes. The
// OpenAI raw passthrough must not echo them: a chat client reading
// {"type":"response.created",...} on a chat stream fails its schema check. Every
// chat, text and Responses converter on every OpenAI route is checked, unary and
// streaming, plus the unconverted control that must still pass raw bytes through.
func TestOpenAIRawPassthroughSkipsConvertedRequests(t *testing.T) {
	foreignRaw := `{"type":"response.created","response":{"id":"resp_1","object":"response"}}`
	extra := func(converted schemas.RequestType) schemas.BifrostResponseExtraFields {
		return schemas.BifrostResponseExtraFields{Provider: schemas.OpenAI, RawResponse: foreignRaw, ConvertedRequestType: converted}
	}
	checked := 0
	for _, prefix := range []string{"", "/openai"} {
		for _, route := range CreateOpenAIRouteConfigs(prefix, &mockHandlerStore{}) {
			for _, converted := range []schemas.RequestType{schemas.ResponsesRequest, ""} {
				name := route.Path + "/converted=" + string(converted)
				wantRaw := converted == ""
				check := func(kind string, got interface{}) {
					t.Helper()
					checked++
					if wantRaw {
						require.Equal(t, foreignRaw, got, "%s %s: unconverted response must pass raw bytes through", name, kind)
					} else {
						require.NotEqual(t, foreignRaw, got, "%s %s: converted response echoed the other API's raw bytes", name, kind)
					}
				}
				if route.ChatResponseConverter != nil {
					got, err := route.ChatResponseConverter(nil, &schemas.BifrostChatResponse{ExtraFields: extra(converted)})
					require.NoError(t, err)
					check("chat", got)
				}
				if route.TextResponseConverter != nil {
					got, err := route.TextResponseConverter(nil, &schemas.BifrostTextCompletionResponse{ExtraFields: extra(converted)})
					require.NoError(t, err)
					check("text", got)
				}
				if route.ResponsesResponseConverter != nil {
					got, err := route.ResponsesResponseConverter(nil, &schemas.BifrostResponsesResponse{ExtraFields: extra(converted)})
					require.NoError(t, err)
					check("responses", got)
				}
				if sc := route.StreamConfig; sc != nil {
					if sc.ChatStreamResponseConverter != nil {
						_, got, err := sc.ChatStreamResponseConverter(nil, &schemas.BifrostChatResponse{ExtraFields: extra(converted)})
						require.NoError(t, err)
						check("chat stream", got)
					}
					if sc.TextStreamResponseConverter != nil {
						_, got, err := sc.TextStreamResponseConverter(nil, &schemas.BifrostTextCompletionResponse{ExtraFields: extra(converted)})
						require.NoError(t, err)
						check("text stream", got)
					}
					if sc.ResponsesStreamResponseConverter != nil {
						_, got, err := sc.ResponsesStreamResponseConverter(nil, &schemas.BifrostResponsesStreamResponse{Type: schemas.ResponsesStreamResponseTypeCreated, ExtraFields: extra(converted)})
						require.NoError(t, err)
						check("responses stream", got)
					}
				}
			}
		}
	}
	require.Positive(t, checked, "no OpenAI chat/text/responses converters found")
}
