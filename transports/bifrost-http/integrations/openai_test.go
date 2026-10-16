package integrations

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fasthttp/router"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/providers/openai"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func BenchmarkParseOpenAIImageEditJSONRequest(b *testing.B) {
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetContentType("application/json; charset=utf-8")
	ctx.Request.SetBodyString(`{"model":"gpt-image-1.5","images":[{"image_url":"https://example.com/image.png"}],"prompt":"Improve clarity","quality":"low"}`)
	b.ReportAllocs()
	for b.Loop() {
		request := &openai.OpenAIImageEditRequest{}
		if err := parseOpenAIImageEditRequest(ctx, request); err != nil {
			b.Fatal(err)
		}
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

// runImageEditSDKRoute drives the registered SDK ingress and its real converter,
// then stops before provider execution so these tests need no API key or listener.
func runImageEditSDKRoute(t *testing.T, prefix, path string, ctx *fasthttp.RequestCtx) (*openai.OpenAIImageEditRequest, *schemas.BifrostImageEditRequest) {
	t.Helper()
	store := &mockHandlerStore{}
	logger := &testLogger{}
	var integration *GenericRouter
	switch prefix {
	case "/openai":
		integration = NewOpenAIRouter(nil, store, nil, logger).GenericRouter
	case "/litellm":
		integration = NewLiteLLMRouter(nil, store, nil, logger).GenericRouter
	case "/langchain":
		integration = NewLangChainRouter(nil, store, nil, logger).GenericRouter
	case "/pydanticai":
		integration = NewPydanticAIRouter(nil, store, nil, logger).GenericRouter
	default:
		t.Fatalf("unknown SDK prefix %q", prefix)
	}
	var route *RouteConfig
	for _, candidate := range integration.routes {
		deploymentRoute := prefix + "/openai/deployments/{deploymentPath:*}"
		matchesDeployment := strings.HasPrefix(path, "/openai/deployments/") && candidate.Path == deploymentRoute
		if (candidate.Path == prefix+path || matchesDeployment) && candidate.Method == fasthttp.MethodPost {
			route = &candidate
			break
		}
	}
	require.NotNil(t, route, "image edit route %s%s must be registered", prefix, path)
	require.NotNil(t, route.StreamConfig, "image edits must retain their streaming configuration")
	var parsed *openai.OpenAIImageEditRequest
	var converted *schemas.BifrostImageEditRequest
	converter := route.RequestConverter
	route.RequestConverter = func(bifrostCtx *schemas.BifrostContext, req interface{}) (*schemas.BifrostRequest, error) {
		result, err := converter(bifrostCtx, req)
		if err != nil {
			return nil, err
		}
		parsed = req.(*openai.OpenAIImageEditRequest)
		converted = result.ImageEditRequest
		return nil, errors.New("image edit captured before provider execution")
	}
	integration.routes = []RouteConfig{*route}
	r := router.New()
	integration.RegisterRoutes(r)
	ctx.Request.Header.SetMethod(fasthttp.MethodPost)
	ctx.Request.SetRequestURI(prefix + path)
	r.Handler(ctx)
	return parsed, converted
}

func captureImageEditSDKWire(t *testing.T, request *schemas.BifrostImageEditRequest, stream, passthrough bool) (string, []byte) {
	t.Helper()
	type wireRequest struct {
		contentType string
		body        []byte
	}
	requests := make(chan wireRequest, 1)
	client := &fasthttp.Client{
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
		Dial: func(_ string) (net.Conn, error) {
			clientConn, serverConn := net.Pipe()
			go func() {
				defer serverConn.Close()
				_ = fasthttp.ServeConn(serverConn, func(ctx *fasthttp.RequestCtx) {
					requests <- wireRequest{string(ctx.Request.Header.ContentType()), bytes.Clone(ctx.Request.Body())}
					ctx.SetConnectionClose()
					ctx.SetStatusCode(fasthttp.StatusBadRequest)
					ctx.SetBodyString(`{"error":{"message":"captured image edit wire"}}`)
				})
			}()
			return clientConn, nil
		},
	}
	t.Cleanup(client.CloseIdleConnections)
	ctx := schemas.NewBifrostContext(t.Context(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyPassthroughExtraParams, passthrough)
	var err *schemas.BifrostError
	if stream {
		_, err = openai.HandleOpenAIImageEditStreamRequest(ctx, client, "http://upstream.test/v1/images/edits", request,
			nil, nil, 5, false, false, schemas.OpenAI, nil, nil, nil, nil, nil, nil)
	} else {
		_, err = openai.HandleOpenAIImageEditRequest(ctx, client, "http://upstream.test/v1/images/edits", request,
			schemas.Key{}, nil, nil, false, false, schemas.OpenAI, nil)
	}
	require.NotNil(t, err)
	require.NotNil(t, err.Error)
	require.Equal(t, "captured image edit wire", err.Error.Message, "request must reach the mock upstream")
	wire := <-requests
	return wire.contentType, wire.body
}

func TestOpenAIImageEditSDKRoutesJSON(t *testing.T) {
	t.Parallel()
	for _, prefix := range []string{"/openai", "/litellm", "/langchain", "/pydanticai"} {
		for _, path := range []string{"/v1/images/edits", "/images/edits"} {
			for _, stream := range []bool{false, true} {
				for _, passthrough := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s%s/stream=%t/passthrough=%t", prefix, path, stream, passthrough), func(t *testing.T) {
						t.Parallel()
						var ctx fasthttp.RequestCtx
						contentType := "application/json; charset=utf-8"
						if passthrough {
							contentType = "application/json"
							ctx.Request.Header.Set("x-bf-passthrough-extra-params", "true")
						}
						ctx.Request.Header.SetContentType(contentType)
						ctx.Request.SetBodyString(fmt.Sprintf(`{
							"model":"openai/gpt-image-2", "prompt":"保持构图，提高画面清晰度。",
							"images":[{"image_url":"https://example.com/first.png"},{"image_url":"https://example.com/second.png"}],
							"background":"transparent", "input_fidelity":"high", "n":2,
							"output_compression":80, "output_format":"webp", "partial_images":1,
							"quality":"low", "size":"1024x1024", "user":"image-edit-test", "response_format":"b64_json", "mask":null,
							"stream":%t, "fallbacks":["openai/gpt-image-1"],
							"extra_params":{"future_option":{"mode":"strict"}}
						}`, stream))
						parsed, converted := runImageEditSDKRoute(t, prefix, path, &ctx)
						require.NotNil(t, parsed, "JSON must reach the SDK converter: %s", ctx.Response.Body())
						require.NotNil(t, converted)
						assert.Equal(t, stream, parsed.IsStreamingRequested())
						assert.Equal(t, schemas.OpenAI, converted.Provider)
						assert.Equal(t, "gpt-image-2", converted.Model)
						require.NotNil(t, converted.Input)
						assert.Equal(t, "保持构图，提高画面清晰度。", converted.Input.Prompt)
						assert.Equal(t, []schemas.ImageInput{{URL: "https://example.com/first.png"}, {URL: "https://example.com/second.png"}}, converted.Input.Images)
						assert.Equal(t, []schemas.Fallback{{Provider: schemas.OpenAI, Model: "gpt-image-1"}}, converted.Fallbacks)
						require.NotNil(t, converted.Params)
						assert.Equal(t, schemas.Ptr("transparent"), converted.Params.Background)
						assert.Equal(t, schemas.Ptr("high"), converted.Params.InputFidelity)
						assert.Equal(t, schemas.Ptr(2), converted.Params.N)
						assert.Equal(t, schemas.Ptr(80), converted.Params.OutputCompression)
						assert.Equal(t, schemas.Ptr("webp"), converted.Params.OutputFormat)
						assert.Equal(t, schemas.Ptr(1), converted.Params.PartialImages)
						assert.Equal(t, schemas.Ptr("low"), converted.Params.Quality)
						assert.Equal(t, schemas.Ptr("1024x1024"), converted.Params.Size)
						assert.Equal(t, schemas.Ptr("image-edit-test"), converted.Params.User)
						assert.Equal(t, schemas.Ptr("b64_json"), converted.Params.ResponseFormat)
						if passthrough {
							assert.Equal(t, map[string]interface{}{"future_option": map[string]interface{}{"mode": "strict"}}, converted.Params.ExtraParams)
						} else {
							assert.Empty(t, converted.Params.ExtraParams)
						}
						contentType, outgoing := captureImageEditSDKWire(t, converted, stream, passthrough)
						assert.Equal(t, "application/json", contentType)
						var wire map[string]json.RawMessage
						require.NoError(t, json.Unmarshal(outgoing, &wire))
						assert.Equal(t, `"gpt-image-2"`, string(wire["model"]))
						assert.Equal(t, `"保持构图，提高画面清晰度。"`, string(wire["prompt"]))
						assert.JSONEq(t, `[{"image_url":"https://example.com/first.png"},{"image_url":"https://example.com/second.png"}]`, string(wire["images"]))
						for _, field := range []string{"input", "url", "image", "fallbacks", "extra_params", "mask", "response_format"} {
							assert.NotContains(t, wire, field)
						}
						if stream {
							assert.Equal(t, "true", string(wire["stream"]))
						} else {
							assert.NotContains(t, wire, "stream")
						}
						if passthrough {
							assert.JSONEq(t, `{"mode":"strict"}`, string(wire["future_option"]))
						} else {
							assert.NotContains(t, wire, "future_option")
						}
					})
				}
			}
		}
	}
}

func TestOpenAIImageEditSDKDeploymentRouteJSON(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, modelField string }{
		{"omitted body model", ""},
		{"empty body model", `"model":"",`},
		{"path overrides body model", `"model":"ignored-model",`},
	}
	for _, prefix := range []string{"/openai", "/litellm", "/langchain", "/pydanticai"} {
		for _, tc := range tests {
			t.Run(prefix+"/"+tc.name, func(t *testing.T) {
				t.Parallel()
				var ctx fasthttp.RequestCtx
				ctx.Request.Header.SetContentType("application/json")
				ctx.Request.SetBodyString(`{` + tc.modelField + `"prompt":"Edit the image.","images":[{"image_url":"https://example.com/image.png"}]}`)
				parsed, converted := runImageEditSDKRoute(t, prefix, "/openai/deployments/openai/gpt-image-2/images/edits", &ctx)
				require.NotNil(t, parsed, "deployment JSON must reach the SDK converter: %s", ctx.Response.Body())
				require.NotNil(t, converted)
				assert.Equal(t, schemas.OpenAI, converted.Provider)
				assert.Equal(t, "gpt-image-2", converted.Model, "deployment path must supply the model and take precedence over the body")
				contentType, body := captureImageEditSDKWire(t, converted, false, false)
				assert.Equal(t, "application/json", contentType)
				assert.JSONEq(t, `{"model":"gpt-image-2","prompt":"Edit the image.","images":[{"image_url":"https://example.com/image.png"}]}`, string(body))
			})
		}
	}
}

func TestOpenAIImageEditSDKRoutesRequireBodyModel(t *testing.T) {
	t.Parallel()
	for _, prefix := range []string{"/openai", "/litellm", "/langchain", "/pydanticai"} {
		for _, path := range []string{"/v1/images/edits", "/images/edits"} {
			t.Run(prefix+path, func(t *testing.T) {
				t.Parallel()
				var ctx fasthttp.RequestCtx
				ctx.Request.Header.SetContentType("application/json")
				ctx.Request.SetBodyString(`{"prompt":"Edit the image.","images":[{"image_url":"https://example.com/image.png"}]}`)
				parsed, converted := runImageEditSDKRoute(t, prefix, path, &ctx)
				assert.Nil(t, parsed)
				assert.Nil(t, converted)
				assert.Equal(t, fasthttp.StatusBadRequest, ctx.Response.StatusCode())
				assert.Contains(t, string(ctx.Response.Body()), "model field is required")
			})
		}
	}
}

func TestOpenAIImageEditSDKRoutesMultipart(t *testing.T) {
	t.Parallel()
	for _, prefix := range []string{"/openai", "/litellm", "/langchain", "/pydanticai"} {
		for _, path := range []string{"/v1/images/edits", "/images/edits"} {
			t.Run(prefix+path, func(t *testing.T) {
				t.Parallel()
				var body bytes.Buffer
				writer := multipart.NewWriter(&body)
				for _, field := range []struct{ name, value string }{
					{"model", "openai/gpt-image-1"}, {"prompt", "Keep the composition."},
					{"quality", "low"}, {"n", "2"}, {"stream", "true"},
					{"fallbacks", "openai/gpt-image-2"},
				} {
					require.NoError(t, writer.WriteField(field.name, field.value))
				}
				for _, file := range []struct{ name, filename, data string }{
					{"image[]", "first.png", "first-image-bytes"},
					{"image[]", "second.png", "second-image-bytes"},
					{"mask", "mask.png", "mask-bytes"},
				} {
					part, err := writer.CreateFormFile(file.name, file.filename)
					require.NoError(t, err)
					_, err = part.Write([]byte(file.data))
					require.NoError(t, err)
				}
				require.NoError(t, writer.Close())
				var ctx fasthttp.RequestCtx
				ctx.Request.Header.SetContentType(writer.FormDataContentType())
				ctx.Request.SetBody(body.Bytes())
				t.Cleanup(ctx.Request.RemoveMultipartFormFiles)
				parsed, converted := runImageEditSDKRoute(t, prefix, path, &ctx)
				require.NotNil(t, parsed, "multipart must reach the SDK converter: %s", ctx.Response.Body())
				require.NotNil(t, converted)
				assert.True(t, parsed.IsStreamingRequested())
				assert.Equal(t, schemas.OpenAI, converted.Provider)
				assert.Equal(t, "gpt-image-1", converted.Model)
				assert.Equal(t, "Keep the composition.", converted.Input.Prompt)
				assert.Equal(t, []schemas.ImageInput{{Image: []byte("first-image-bytes")}, {Image: []byte("second-image-bytes")}}, converted.Input.Images)
				assert.Equal(t, []byte("mask-bytes"), converted.Params.Mask)
				assert.Equal(t, schemas.Ptr("low"), converted.Params.Quality)
				assert.Equal(t, schemas.Ptr(2), converted.Params.N)
				assert.Equal(t, []schemas.Fallback{{Provider: schemas.OpenAI, Model: "gpt-image-2"}}, converted.Fallbacks)
			})
		}
	}
}

func TestOpenAIImageEditSDKRoutesRejectInvalidJSON(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, body, wantError string }{
		{"empty body", ``, "failed to parse request"},
		{"malformed", `{"model":"gpt-image-2",`, "failed to parse request"},
		{"missing model", `{"prompt":"edit","images":[{"image_url":"https://example.com/image.png"}]}`, "model"},
		{"missing prompt", `{"model":"gpt-image-2","images":[{"image_url":"https://example.com/image.png"}]}`, "prompt"},
		{"empty images", `{"model":"gpt-image-2","prompt":"edit","images":[]}`, "image"},
		{"missing images", `{"model":"gpt-image-2","prompt":"edit"}`, "image"},
		{"null images", `{"model":"gpt-image-2","prompt":"edit","images":null}`, "image"},
		{"missing image_url", `{"model":"gpt-image-2","prompt":"edit","images":[{}]}`, "image_url"},
		{"blank image_url", `{"model":"gpt-image-2","prompt":"edit","images":[{"image_url":"  "}]}`, "image_url"},
		{"wrong image_url type", `{"model":"gpt-image-2","prompt":"edit","images":[{"image_url":42}]}`, "failed to parse request"},
		{"JSON mask", `{"model":"gpt-image-2","prompt":"edit","images":[{"image_url":"https://example.com/image.png"}],"mask":{"image_url":"https://example.com/mask.png"}}`, "mask"},
		{"null body", `null`, "model"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var ctx fasthttp.RequestCtx
			ctx.Request.Header.SetContentType("application/json")
			ctx.Request.SetBodyString(tc.body)
			parsed, converted := runImageEditSDKRoute(t, "/openai", "/v1/images/edits", &ctx)
			assert.Nil(t, parsed, "invalid JSON must stop before the SDK converter")
			assert.Nil(t, converted)
			assert.Equal(t, fasthttp.StatusBadRequest, ctx.Response.StatusCode())
			assert.Contains(t, string(ctx.Response.Body()), tc.wantError)
		})
	}
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
