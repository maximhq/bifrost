package handlers

import (
	"bytes"
	"mime/multipart"
	"testing"

	"github.com/valyala/fasthttp"
)

// buildImageEditForm builds a minimal multipart image-edit request body.
func buildImageEditForm(t *testing.T, fields map[string]string) (*fasthttp.RequestCtx, error) {
	t.Helper()

	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	for key, value := range fields {
		if err := w.WriteField(key, value); err != nil {
			return nil, err
		}
	}
	fw, err := w.CreateFormFile("image", "input.png")
	if err != nil {
		return nil, err
	}
	if _, err := fw.Write([]byte{0x89, 0x50, 0x4e, 0x47}); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod(fasthttp.MethodPost)
	ctx.Request.Header.SetContentType(w.FormDataContentType())
	ctx.Request.SetBody(body.Bytes())
	return ctx, nil
}

// TestPrepareImageEditRequest_AspectRatio verifies the multipart image-edit
// parser maps the aspect_ratio form field into the typed parameter instead of
// letting it fall through to ExtraParams (issue #5324 follow-up).
func TestPrepareImageEditRequest_AspectRatio(t *testing.T) {
	ctx, err := buildImageEditForm(t, map[string]string{
		"model":        "gemini/gemini-3.1-flash-image",
		"prompt":       "extend into a wide banner",
		"aspect_ratio": "21:9",
	})
	if err != nil {
		t.Fatalf("build form: %v", err)
	}

	_, bifrostReq, err := prepareImageEditRequest(ctx, nil)
	if err != nil {
		t.Fatalf("prepareImageEditRequest: %v", err)
	}
	if bifrostReq.Params == nil || bifrostReq.Params.AspectRatio == nil {
		t.Fatal("aspect_ratio form field not mapped to typed parameter")
	}
	if *bifrostReq.Params.AspectRatio != "21:9" {
		t.Errorf("aspect_ratio: got %q, want %q", *bifrostReq.Params.AspectRatio, "21:9")
	}
	if _, ok := bifrostReq.Params.ExtraParams["aspect_ratio"]; ok {
		t.Error("aspect_ratio leaked into ExtraParams despite being a known field")
	}
}

// TestPrepareImageEditRequest_NoAspectRatio verifies the field stays nil when absent.
func TestPrepareImageEditRequest_NoAspectRatio(t *testing.T) {
	ctx, err := buildImageEditForm(t, map[string]string{
		"model":  "gemini/gemini-3.1-flash-image",
		"prompt": "extend into a wide banner",
	})
	if err != nil {
		t.Fatalf("build form: %v", err)
	}

	_, bifrostReq, err := prepareImageEditRequest(ctx, nil)
	if err != nil {
		t.Fatalf("prepareImageEditRequest: %v", err)
	}
	if bifrostReq.Params != nil && bifrostReq.Params.AspectRatio != nil {
		t.Errorf("aspect_ratio should be nil when not sent, got %q", *bifrostReq.Params.AspectRatio)
	}
}
