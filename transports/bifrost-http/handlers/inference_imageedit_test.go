package handlers

import (
	"bytes"
	"mime/multipart"
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

func jsonImageEditCtx(body string) *fasthttp.RequestCtx {
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod("POST")
	ctx.Request.SetRequestURI("/v1/images/edits")
	ctx.Request.Header.SetContentType("application/json")
	ctx.Request.SetBody([]byte(body))
	return ctx
}

func multipartImageEditCtx(t *testing.T, fields map[string]string, files map[string][]byte) *fasthttp.RequestCtx {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for k, v := range fields {
		if err := writer.WriteField(k, v); err != nil {
			t.Fatalf("write field %s: %v", k, err)
		}
	}
	for name, content := range files {
		part, err := writer.CreateFormFile(name, name+".png")
		if err != nil {
			t.Fatalf("create file part %s: %v", name, err)
		}
		if _, err := part.Write(content); err != nil {
			t.Fatalf("write file part %s: %v", name, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod("POST")
	ctx.Request.SetRequestURI("/v1/images/edits")
	ctx.Request.Header.SetContentType(writer.FormDataContentType())
	ctx.Request.SetBody(body.Bytes())
	return ctx
}

// The JSON shape: images carry their source under "images", the same nested form /v1/videos/edits
// uses for its source video.
func TestPrepareImageEditRequest_JSON(t *testing.T) {
	_, req, err := prepareImageEditRequest(jsonImageEditCtx(
		`{"model":"runware/topazlabs:wonder@3.5","images":[{"url":"https://example.com/teapot.jpg"}],"prompt":"sharpen","type":"upscale","upscale_factor":4}`), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.Provider != schemas.Runware || req.Model != "topazlabs:wonder@3.5" {
		t.Fatalf("provider/model = %q/%q", req.Provider, req.Model)
	}
	if len(req.Input.Images) != 1 || req.Input.Images[0].URL != "https://example.com/teapot.jpg" {
		t.Fatalf("images not carried: %+v", req.Input.Images)
	}
	if req.Input.Prompt != "sharpen" {
		t.Fatalf("prompt = %q", req.Input.Prompt)
	}
	if req.Params == nil || req.Params.Type == nil || *req.Params.Type != "upscale" {
		t.Fatalf("type not carried: %+v", req.Params)
	}
	if req.Params.UpscaleFactor == nil || *req.Params.UpscaleFactor != 4 {
		t.Fatalf("upscale_factor = %v, want 4", req.Params.UpscaleFactor)
	}
}

// The reason JSON exists on this route: multipart stringifies every value, so a model's nested
// tuning object (Topaz Wonder's settings.enhancementStrength / settings.grain) can only reach the
// provider with its real JSON types through a JSON body.
func TestPrepareImageEditRequest_JSONTypedExtraParams(t *testing.T) {
	_, req, err := prepareImageEditRequest(jsonImageEditCtx(
		`{"model":"runware/topazlabs:wonder@3.5","images":[{"url":"https://example.com/teapot.jpg"}],"type":"upscale",`+
			`"settings":{"upscaleFactor":4,"enhancementStrength":"high","grain":{"amount":2}},"outputQuality":90,"includeCost":true}`), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	settings, ok := req.Params.ExtraParams["settings"].(map[string]any)
	if !ok {
		t.Fatalf("settings = %#v, want a nested object", req.Params.ExtraParams["settings"])
	}
	if _, ok := settings["upscaleFactor"].(string); ok {
		t.Fatalf("settings.upscaleFactor arrived as a string: %#v", settings["upscaleFactor"])
	}
	if grain, ok := settings["grain"].(map[string]any); !ok || grain["amount"] == nil {
		t.Fatalf("settings.grain = %#v, want a nested object", settings["grain"])
	}
	if _, ok := req.Params.ExtraParams["outputQuality"].(string); ok {
		t.Fatalf("outputQuality arrived as a string: %#v", req.Params.ExtraParams["outputQuality"])
	}
	if req.Params.ExtraParams["includeCost"] != true {
		t.Fatalf("includeCost = %#v, want the boolean true", req.Params.ExtraParams["includeCost"])
	}
}

// Every field the JSON body consumes must be a known field, or passthrough re-sends it verbatim on
// top of the envelope the provider converter already built - Runware rejects the duplicate.
func TestPrepareImageEditRequest_JSONInputFieldsAreNotExtraParams(t *testing.T) {
	_, req, err := prepareImageEditRequest(jsonImageEditCtx(
		`{"model":"runware/topazlabs:wonder@3.5","images":["https://example.com/teapot.jpg"],"prompt":"sharpen","type":"upscale","upscale_factor":4,"settings":{"grain":true}}`), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, key := range []string{"model", "images", "prompt", "type", "upscale_factor", "fallbacks", "stream"} {
		if _, leaked := req.Params.ExtraParams[key]; leaked {
			t.Fatalf("%q leaked into extra params: %+v", key, req.Params.ExtraParams)
		}
	}
	if req.Params.ExtraParams["settings"] == nil {
		t.Fatalf("settings should still reach extra params: %+v", req.Params.ExtraParams)
	}
}

// A bare string is accepted alongside the object form, so the array reads the way input_images does
// on /v1/images/generations.
func TestPrepareImageEditRequest_JSONBareStringImages(t *testing.T) {
	_, req, err := prepareImageEditRequest(jsonImageEditCtx(
		`{"model":"runware/google:4@1","images":["https://example.com/a.jpg",{"url":"https://example.com/b.jpg"}],"prompt":"make it blue"}`), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(req.Input.Images) != 2 {
		t.Fatalf("images = %+v, want 2 entries", req.Input.Images)
	}
	if req.Input.Images[0].URL != "https://example.com/a.jpg" {
		t.Fatalf("string form not carried: %+v", req.Input.Images[0])
	}
	if req.Input.Images[1].URL != "https://example.com/b.jpg" {
		t.Fatalf("object form not carried: %+v", req.Input.Images[1])
	}
}

// Base64 bytes ride in the same "images" array, so a JSON caller never has to switch to multipart
// just to send an image it holds in memory.
func TestPrepareImageEditRequest_JSONBase64Image(t *testing.T) {
	_, req, err := prepareImageEditRequest(jsonImageEditCtx(
		`{"model":"runware/runware:110@1","images":[{"image":"aGVsbG8="}],"type":"background_removal"}`), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(req.Input.Images) != 1 || string(req.Input.Images[0].Image) != "hello" {
		t.Fatalf("image bytes not decoded: %+v", req.Input.Images)
	}
}

// The multipart contract is unchanged: image_url still stands in for an upload, scalars are still
// converted, and unrecognised values still pass through as the strings multipart delivers.
func TestPrepareImageEditRequest_MultipartUnchanged(t *testing.T) {
	_, req, err := prepareImageEditRequest(multipartImageEditCtx(t, map[string]string{
		"model":          "runware/topazlabs:wonder@3.5",
		"image_url":      "https://example.com/teapot.jpg",
		"type":           "upscale",
		"upscale_factor": "4",
		"settings":       `{"enhancementStrength":"high"}`,
	}, nil), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.Provider != schemas.Runware || req.Model != "topazlabs:wonder@3.5" {
		t.Fatalf("provider/model = %q/%q", req.Provider, req.Model)
	}
	if len(req.Input.Images) != 1 || req.Input.Images[0].URL != "https://example.com/teapot.jpg" {
		t.Fatalf("image_url not carried: %+v", req.Input.Images)
	}
	if req.Params.UpscaleFactor == nil || *req.Params.UpscaleFactor != 4 {
		t.Fatalf("upscale_factor = %v, want 4", req.Params.UpscaleFactor)
	}
	if req.Params.ExtraParams["settings"] != `{"enhancementStrength":"high"}` {
		t.Fatalf("settings = %#v, want the raw form string", req.Params.ExtraParams["settings"])
	}
}

// Uploads keep their leading position ahead of any image_url, and the mask still arrives as bytes.
func TestPrepareImageEditRequest_MultipartUpload(t *testing.T) {
	_, req, err := prepareImageEditRequest(multipartImageEditCtx(t,
		map[string]string{
			"model":     "runware/runware:102@1",
			"prompt":    "a bunch of yellow sunflowers",
			"image_url": "https://example.com/teapot.jpg",
		},
		map[string][]byte{"image": []byte("png-bytes"), "mask": []byte("mask-bytes")}), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(req.Input.Images) != 2 {
		t.Fatalf("images = %+v, want the upload plus the url", req.Input.Images)
	}
	if string(req.Input.Images[0].Image) != "png-bytes" {
		t.Fatalf("uploaded image must lead: %+v", req.Input.Images)
	}
	if req.Input.Images[1].URL != "https://example.com/teapot.jpg" {
		t.Fatalf("image_url must follow the upload: %+v", req.Input.Images)
	}
	if string(req.Params.Mask) != "mask-bytes" {
		t.Fatalf("mask = %q", req.Params.Mask)
	}
}

// Model and at least one image are required on both bodies, and a malformed JSON body is rejected
// rather than silently treated as an empty request.
func TestPrepareImageEditRequest_Validation(t *testing.T) {
	if _, _, err := prepareImageEditRequest(jsonImageEditCtx(
		`{"images":[{"url":"https://example.com/teapot.jpg"}]}`), nil); err == nil {
		t.Fatal("expected an error when model is missing from a JSON body")
	}
	if _, _, err := prepareImageEditRequest(jsonImageEditCtx(
		`{"model":"runware/runware:110@1"}`), nil); err == nil {
		t.Fatal("expected an error when a JSON body carries no images")
	}
	if _, _, err := prepareImageEditRequest(jsonImageEditCtx(`{"model":`), nil); err == nil {
		t.Fatal("expected an error for a malformed JSON body")
	}
	if _, _, err := prepareImageEditRequest(multipartImageEditCtx(t,
		map[string]string{"image_url": "https://example.com/teapot.jpg"}, nil), nil); err == nil {
		t.Fatal("expected an error when model is missing from a multipart form")
	}
	if _, _, err := prepareImageEditRequest(multipartImageEditCtx(t,
		map[string]string{"model": "runware/runware:110@1"}, nil), nil); err == nil {
		t.Fatal("expected an error when a multipart form carries no image")
	}
}

// JSON has several ways to spell an image entry that carries nothing. None of them may reach a
// provider, where they would become an empty input key and a confusing upstream 4xx.
func TestPrepareImageEditRequest_EmptyImageEntriesAreDropped(t *testing.T) {
	for _, entries := range []string{`[null]`, `[{}]`, `[{"image":null}]`, `[{"url":"  "}]`} {
		if _, _, err := prepareImageEditRequest(jsonImageEditCtx(
			`{"model":"runware/runware:110@1","images":`+entries+`}`), nil); err == nil {
			t.Fatalf("images: %s was accepted, want the same error as an empty array", entries)
		}
	}

	// A junk entry alongside a real one is dropped rather than failing the whole request.
	_, req, err := prepareImageEditRequest(jsonImageEditCtx(
		`{"model":"runware/runware:110@1","images":[null,{"url":"https://example.com/a.jpg"},{}],"type":"background_removal"}`), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(req.Input.Images) != 1 || req.Input.Images[0].URL != "https://example.com/a.jpg" {
		t.Fatalf("images = %+v, want only the populated entry", req.Input.Images)
	}
}

// Streaming is selected the same way on both bodies, so the handler's stream branch is reachable
// from JSON callers too.
func TestPrepareImageEditRequest_Stream(t *testing.T) {
	req, _, err := prepareImageEditRequest(jsonImageEditCtx(
		`{"model":"openai/gpt-image-1","images":[{"url":"https://example.com/teapot.jpg"}],"prompt":"blue","stream":true}`), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.Stream == nil || !*req.Stream {
		t.Fatalf("stream = %v, want true from the JSON body", req.Stream)
	}

	req, _, err = prepareImageEditRequest(multipartImageEditCtx(t, map[string]string{
		"model":     "openai/gpt-image-1",
		"image_url": "https://example.com/teapot.jpg",
		"prompt":    "blue",
		"stream":    "true",
	}, nil), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.Stream == nil || !*req.Stream {
		t.Fatalf("stream = %v, want true from the multipart form", req.Stream)
	}
}
