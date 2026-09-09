package handlers

import (
	"bytes"
	"mime/multipart"
	"testing"

	"github.com/fasthttp/router"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

func jsonVideoEditCtx(body string) *fasthttp.RequestCtx {
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod("POST")
	ctx.Request.SetRequestURI("/v1/videos/edits")
	ctx.Request.Header.SetContentType("application/json")
	ctx.Request.SetBody([]byte(body))
	return ctx
}

func multipartVideoEditCtx(t *testing.T, fields map[string]string, file []byte) *fasthttp.RequestCtx {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for k, v := range fields {
		if err := writer.WriteField(k, v); err != nil {
			t.Fatalf("write field %s: %v", k, err)
		}
	}
	if file != nil {
		part, err := writer.CreateFormFile("video", "source.mp4")
		if err != nil {
			t.Fatalf("create file part: %v", err)
		}
		if _, err := part.Write(file); err != nil {
			t.Fatalf("write file part: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod("POST")
	ctx.Request.SetRequestURI("/v1/videos/edits")
	ctx.Request.Header.SetContentType(writer.FormDataContentType())
	ctx.Request.SetBody(body.Bytes())
	return ctx
}

// The documented JSON shape: prompt plus a source reference, with model naming the provider.
func TestPrepareVideoEditRequest_JSON(t *testing.T) {
	req, err := prepareVideoEditRequest(jsonVideoEditCtx(
		`{"model":"runware/runway:aleph@2.0","prompt":"teal palette","video":{"url":"https://assets.runware.ai/clip.mp4"},"type":"upscale","upscale_factor":2}`), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.Provider != schemas.Runware || req.Model != "runway:aleph@2.0" {
		t.Fatalf("provider/model = %q/%q", req.Provider, req.Model)
	}
	if req.Input.Prompt != "teal palette" || req.Input.Video.URL != "https://assets.runware.ai/clip.mp4" {
		t.Fatalf("input not carried: %+v", req.Input)
	}
	if req.Params == nil || req.Params.Type == nil || *req.Params.Type != "upscale" {
		t.Fatalf("type not carried: %+v", req.Params)
	}
	if req.Params.UpscaleFactor == nil || *req.Params.UpscaleFactor != 2 {
		t.Fatalf("upscale_factor = %v, want 2", req.Params.UpscaleFactor)
	}
}

// Model is optional when the source video ID already names its provider — the same id:provider
// convention the retrieve, download, delete and remix routes use.
func TestPrepareVideoEditRequest_ProviderFromVideoID(t *testing.T) {
	req, err := prepareVideoEditRequest(jsonVideoEditCtx(
		`{"prompt":"teal palette","video":{"id":"video_abc123:openai"}}`), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.Provider != schemas.OpenAI {
		t.Fatalf("provider = %q, want openai", req.Provider)
	}
	if req.Model != "" {
		t.Fatalf("model = %q, want empty so the provider infers it from the source", req.Model)
	}
	if req.Input.Video.ID != "video_abc123:openai" {
		t.Fatalf("video id = %q, want the suffixed id preserved for the provider to strip", req.Input.Video.ID)
	}
}

// With no model and a bare video ID, the provider falls back to the query param / header, then errors.
func TestPrepareVideoEditRequest_ProviderFallbacks(t *testing.T) {
	ctx := jsonVideoEditCtx(`{"prompt":"teal palette","video":{"id":"video_abc123"}}`)
	ctx.Request.SetRequestURI("/v1/videos/edits?provider=openai")
	req, err := prepareVideoEditRequest(ctx, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.Provider != schemas.OpenAI {
		t.Fatalf("provider = %q, want openai from the query param", req.Provider)
	}

	ctx = jsonVideoEditCtx(`{"prompt":"teal palette","video":{"id":"video_abc123"}}`)
	ctx.Request.Header.Set("x-model-provider", "openai")
	if req, err = prepareVideoEditRequest(ctx, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.Provider != schemas.OpenAI {
		t.Fatalf("provider = %q, want openai from the header", req.Provider)
	}

	if _, err = prepareVideoEditRequest(jsonVideoEditCtx(
		`{"prompt":"teal palette","video":{"id":"video_abc123"}}`), nil); err == nil {
		t.Fatal("expected an error when no model, id suffix, query param or header is present")
	}
}

// A model with no provider prefix must not short-circuit provider resolution: Runware AIR ids like
// "runway:aleph@2.0" carry no "/" so ParseModelString yields an empty provider, and the query
// param, header and video-id suffix all still have to be consulted.
func TestPrepareVideoEditRequest_BareModelStillResolvesProvider(t *testing.T) {
	ctx := jsonVideoEditCtx(`{"model":"runway:aleph@2.0","prompt":"teal","video":{"url":"https://x/clip.mp4"}}`)
	ctx.Request.SetRequestURI("/v1/videos/edits?provider=runware")
	req, err := prepareVideoEditRequest(ctx, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.Provider != schemas.Runware {
		t.Fatalf("provider = %q, want runware from the query param", req.Provider)
	}
	if req.Model != "runway:aleph@2.0" {
		t.Fatalf("model = %q, want the bare AIR id preserved", req.Model)
	}

	ctx = jsonVideoEditCtx(`{"model":"runway:aleph@2.0","prompt":"teal","video":{"id":"task-uuid:runware"}}`)
	if req, err = prepareVideoEditRequest(ctx, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.Provider != schemas.Runware || req.Model != "runway:aleph@2.0" {
		t.Fatalf("provider/model = %q/%q, want runware/runway:aleph@2.0 from the id suffix", req.Provider, req.Model)
	}
}

// A bare model with no provider hint anywhere is not an error — the model catalogue and routing
// plugins resolve it downstream, exactly as on chat, images and /v1/videos.
func TestPrepareVideoEditRequest_BareModelDefersToRouting(t *testing.T) {
	req, err := prepareVideoEditRequest(jsonVideoEditCtx(
		`{"model":"runway:aleph@2.0","prompt":"teal","video":{"url":"https://x/clip.mp4"}}`), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.Provider != "" {
		t.Fatalf("provider = %q, want empty so routing can resolve it", req.Provider)
	}
	if req.Model != "runway:aleph@2.0" {
		t.Fatalf("model = %q", req.Model)
	}
}

// Only a suffix that names a real provider counts, so an id that merely contains a colon is not
// mistaken for one.
func TestVideoIDProviderSuffix(t *testing.T) {
	cases := map[string]string{
		"video_abc123:openai":  "openai",
		"task-uuid:runware":    "runware",
		"video_abc123":         "",
		"weird:id:with:colons": "",
		"video_abc123:":        "",
		":openai":              "",
		"":                     "",
	}
	for id, want := range cases {
		if got := videoIDProviderSuffix(id); got != want {
			t.Fatalf("videoIDProviderSuffix(%q) = %q, want %q", id, got, want)
		}
	}
}

func TestPrepareVideoEditRequest_RequiresSource(t *testing.T) {
	if _, err := prepareVideoEditRequest(jsonVideoEditCtx(`{"model":"openai/sora-2-pro","prompt":"teal"}`), nil); err == nil {
		t.Fatal("expected an error when the source video is missing")
	}
}

// Unrecognised body fields flow through as extra params rather than being dropped.
func TestPrepareVideoEditRequest_ExtraParams(t *testing.T) {
	req, err := prepareVideoEditRequest(jsonVideoEditCtx(
		`{"model":"runware/bria:2@1","prompt":"cut it","video":{"url":"https://x/clip.mp4"},"providerSettings":{"bria":{"preserveAlpha":true}}}`), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.Params == nil || req.Params.ExtraParams["providerSettings"] == nil {
		t.Fatalf("providerSettings should reach extra params, got %+v", req.Params)
	}
}

// An uploaded source arrives as multipart, which is also what the official OpenAI SDKs always send.
func TestPrepareVideoEditRequest_MultipartUpload(t *testing.T) {
	ctx := multipartVideoEditCtx(t,
		map[string]string{"model": "openai/sora-2-pro", "prompt": "warm backlight"},
		[]byte("\x00\x00\x00\x18ftypmp42\x00\x00\x00\x00mp42isom"))

	req, err := prepareVideoEditRequest(ctx, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.Provider != schemas.OpenAI || req.Model != "sora-2-pro" {
		t.Fatalf("provider/model = %q/%q", req.Provider, req.Model)
	}
	if len(req.Input.Video.Video) == 0 {
		t.Fatal("uploaded video bytes were not read")
	}
	if req.Input.Prompt != "warm backlight" {
		t.Fatalf("prompt = %q", req.Input.Prompt)
	}
}

// The OpenAI SDKs flatten a reference source into a bracketed multipart field.
func TestPrepareVideoEditRequest_MultipartBracketedVideoID(t *testing.T) {
	ctx := multipartVideoEditCtx(t,
		map[string]string{"prompt": "warm backlight", "video[id]": "video_abc123:openai"}, nil)

	req, err := prepareVideoEditRequest(ctx, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.Input.Video.ID != "video_abc123:openai" {
		t.Fatalf("video id = %q", req.Input.Video.ID)
	}
	if req.Provider != schemas.OpenAI {
		t.Fatalf("provider = %q, want openai resolved from the id suffix", req.Provider)
	}
}

// The video routes mix a static path with parameterised siblings under the same prefix, which the
// router rejects if the shapes conflict. Registration panics on conflict, so this asserts the set
// registers and that each path still dispatches to its own handler.
func TestVideoRouteShapesDoNotConflict(t *testing.T) {
	hit := ""
	mark := func(name string) fasthttp.RequestHandler {
		return func(ctx *fasthttp.RequestCtx) { hit = name }
	}

	r := router.New()
	r.POST("/v1/videos", mark("generation"))
	r.POST("/v1/videos/edits", mark("edit"))
	r.GET("/v1/videos", mark("list"))
	r.GET("/v1/videos/{video_id}", mark("retrieve"))
	r.GET("/v1/videos/{video_id}/content", mark("download"))
	r.DELETE("/v1/videos/{video_id}", mark("delete"))
	r.POST("/v1/videos/{video_id}/remix", mark("remix"))

	cases := []struct {
		method, path, want string
	}{
		{"POST", "/v1/videos/edits", "edit"},
		{"POST", "/v1/videos", "generation"},
		{"POST", "/v1/videos/video_abc123:openai/remix", "remix"},
		{"GET", "/v1/videos/video_abc123:openai", "retrieve"},
		{"GET", "/v1/videos/video_abc123:openai/content", "download"},
		{"DELETE", "/v1/videos/video_abc123:openai", "delete"},
		{"GET", "/v1/videos", "list"},
	}
	for _, tc := range cases {
		hit = ""
		ctx := &fasthttp.RequestCtx{}
		ctx.Request.Header.SetMethod(tc.method)
		ctx.Request.SetRequestURI(tc.path)
		r.Handler(ctx)
		if hit != tc.want {
			t.Fatalf("%s %s dispatched to %q (status %d), want %q", tc.method, tc.path, hit, ctx.Response.StatusCode(), tc.want)
		}
	}
}
