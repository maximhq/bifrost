package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

type imageEditWireRequest struct {
	contentType string
	body        []byte
}

var (
	benchmarkImageEditBody        []byte
	benchmarkImageEditContentType string
)

func BenchmarkEncodeOpenAIImageEditRequest(b *testing.B) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	for _, test := range []struct {
		name    string
		request *OpenAIImageEditRequest
	}{
		{
			name: "url_json",
			request: &OpenAIImageEditRequest{
				Model: "gpt-image-1.5",
				Input: &schemas.ImageEditInput{
					Prompt: "Improve clarity",
					Images: []schemas.ImageInput{{URL: "https://example.com/image.png"}},
				},
			},
		},
		{
			name: "bytes_multipart",
			request: &OpenAIImageEditRequest{
				Model: "gpt-image-1.5",
				Input: &schemas.ImageEditInput{
					Prompt: "Improve clarity",
					Images: []schemas.ImageInput{{Image: []byte("fake-png-bytes")}},
				},
			},
		},
	} {
		b.Run(test.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				body, contentType, err := encodeOpenAIImageEditRequest(ctx, test.request, schemas.OpenAI)
				if err != nil {
					b.Fatal(err)
				}
				benchmarkImageEditBody = body
				benchmarkImageEditContentType = contentType
			}
		})
	}
}

// A pipe captures the real fasthttp request without a listening socket or live API.
func imageEditWireClient(t *testing.T) (*fasthttp.Client, <-chan imageEditWireRequest) {
	t.Helper()
	requests := make(chan imageEditWireRequest, 1)
	client := &fasthttp.Client{
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
		Dial: func(_ string) (net.Conn, error) {
			clientConn, serverConn := net.Pipe()
			go func() {
				defer serverConn.Close()
				_ = fasthttp.ServeConn(serverConn, func(ctx *fasthttp.RequestCtx) {
					requests <- imageEditWireRequest{
						contentType: string(ctx.Request.Header.ContentType()),
						body:        bytes.Clone(ctx.Request.Body()),
					}
					ctx.SetConnectionClose()
					ctx.SetStatusCode(fasthttp.StatusBadRequest)
					ctx.SetBodyString(`{"error":{"message":"captured upstream request"}}`)
				})
			}()
			return clientConn, nil
		},
	}
	t.Cleanup(client.CloseIdleConnections)
	return client, requests
}

func callImageEditWire(
	ctx *schemas.BifrostContext,
	client *fasthttp.Client,
	request *schemas.BifrostImageEditRequest,
	provider schemas.ModelProvider,
	stream bool,
) *schemas.BifrostError {
	if stream {
		_, err := HandleOpenAIImageEditStreamRequest(
			ctx, client, "http://upstream.test/v1/images/edits", request,
			nil, nil, 5, false, false, provider, nil, nil, nil, nil, nil, nil,
		)
		return err
	}
	_, err := HandleOpenAIImageEditRequest(
		ctx, client, "http://upstream.test/v1/images/edits", request,
		schemas.Key{}, nil, nil, false, false, provider, nil,
	)
	return err
}

func TestImageEditURLWireFormat(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		model  string
		stream bool
	}{
		{name: "unary", model: "gpt-image-2.5-flare"},
		{name: "streaming", model: "gpt-image-2.5-flare", stream: true},
		{name: "unlisted snapshot/unary", model: "gpt-image-2.5-flare-2099-01-01"},
		{name: "unlisted snapshot/streaming", model: "gpt-image-2.5-flare-2099-01-01", stream: true},
		{name: "model alias/unary", model: "my-image-model"},
		{name: "model alias/streaming", model: "my-image-model", stream: true},
		{name: "upstream model rejection/unary", model: "dall-e-2"},
		{name: "upstream model rejection/streaming", model: "dall-e-2", stream: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			client, requests := imageEditWireClient(t)
			ctx := schemas.NewBifrostContext(t.Context(), schemas.NoDeadline)
			ctx.SetValue(schemas.BifrostContextKeyPassthroughExtraParams, true)
			request := &schemas.BifrostImageEditRequest{
				Provider: schemas.OpenAI,
				Model:    test.model,
				Input: &schemas.ImageEditInput{
					Prompt: "Improve clarity",
					Images: []schemas.ImageInput{
						{URL: "https://example.com/image.png"},
						{URL: "data:image/png;base64,iVBORw0KGgo="},
					},
				},
				Params: &schemas.ImageEditParameters{
					Quality:        schemas.Ptr("low"),
					N:              schemas.Ptr(1),
					Type:           schemas.Ptr("upscale"),
					Seed:           schemas.Ptr(123),
					ResponseFormat: schemas.Ptr("b64_json"),
					ExtraParams:    map[string]interface{}{"test_extra": true},
				},
			}
			err := callImageEditWire(ctx, client, request, schemas.OpenAI, test.stream)
			if err == nil || err.IsBifrostError || err.StatusCode == nil || *err.StatusCode != 400 ||
				err.Error == nil || err.Error.Message != "captured upstream request" {
				t.Fatalf("expected upstream 400 capture error, got %+v", err)
			}
			wire := <-requests
			if wire.contentType != "application/json" {
				t.Fatalf("Content-Type = %q, want application/json", wire.contentType)
			}
			var body map[string]json.RawMessage
			if err := json.Unmarshal(wire.body, &body); err != nil {
				t.Fatal(err)
			}
			for _, field := range []string{"input", "url", "image", "type", "seed", "fallbacks", "extra_params", "response_format"} {
				if _, ok := body[field]; ok {
					t.Errorf("internal field %q leaked into JSON: %s", field, wire.body)
				}
			}
			if string(body["model"]) != `"`+test.model+`"` || string(body["prompt"]) != `"Improve clarity"` {
				t.Errorf("wrong model or prompt: %s", wire.body)
			}
			if string(body["quality"]) != `"low"` || string(body["n"]) != "1" {
				t.Errorf("optional parameters missing: %s", wire.body)
			}
			if string(body["test_extra"]) != "true" {
				t.Errorf("extra parameter missing: %s", wire.body)
			}
			var images []map[string]string
			if err := json.Unmarshal(body["images"], &images); err != nil {
				t.Fatal(err)
			}
			if len(images) != 2 {
				t.Fatalf("wrong images: %+v", images)
			}
			for i, image := range images {
				if len(image) != 1 || image["image_url"] != request.Input.Images[i].URL {
					t.Errorf("image %d = %+v, want only image_url", i, image)
				}
			}
			if test.stream && string(body["stream"]) != "true" {
				t.Errorf("stream flag missing: %s", wire.body)
			}
			if !test.stream && body["stream"] != nil {
				t.Errorf("unexpected stream flag: %s", wire.body)
			}
		})
	}
}

func TestImageEditBytesKeepMultipart(t *testing.T) {
	t.Parallel()
	png := []byte("\x89PNG\r\n\x1a\nimage bytes")
	for _, test := range []struct {
		name   string
		url    string
		stream bool
	}{
		{name: "unary"},
		{name: "streaming", stream: true},
		{name: "blank URL/unary", url: "   "},
		{name: "blank URL/streaming", url: " \t\r\n ", stream: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			client, requests := imageEditWireClient(t)
			request := &schemas.BifrostImageEditRequest{
				Provider: schemas.Azure,
				Model:    "custom-deployment",
				Input: &schemas.ImageEditInput{
					Prompt: "Improve clarity",
					Images: []schemas.ImageInput{{URL: test.url, Image: png}},
				},
				Params: &schemas.ImageEditParameters{Mask: png, Quality: schemas.Ptr("low")},
			}
			err := callImageEditWire(
				schemas.NewBifrostContext(t.Context(), schemas.NoDeadline), client, request, schemas.Azure, test.stream,
			)
			if err == nil || err.Error.Message != "captured upstream request" {
				t.Fatalf("expected upstream capture error, got %+v", err)
			}
			wire := <-requests
			mediaType, params, parseErr := mime.ParseMediaType(wire.contentType)
			if parseErr != nil || mediaType != "multipart/form-data" {
				t.Fatalf("Content-Type = %q: %v", wire.contentType, parseErr)
			}
			form, parseErr := multipart.NewReader(bytes.NewReader(wire.body), params["boundary"]).ReadForm(1024)
			if parseErr != nil {
				t.Fatal(parseErr)
			}
			defer form.RemoveAll()
			if form.Value["quality"][0] != "low" || form.Value["prompt"][0] != request.Input.Prompt {
				t.Errorf("multipart fields changed: %+v", form.Value)
			}
			for _, field := range []string{"image[]", "mask"} {
				files := form.File[field]
				if len(files) != 1 || files[0].Header.Get("Content-Type") != "image/png" {
					t.Fatalf("wrong %s file parts: %+v", field, files)
				}
				file, err := files[0].Open()
				if err != nil {
					t.Fatal(err)
				}
				got, err := io.ReadAll(file)
				file.Close()
				if err != nil || !bytes.Equal(got, png) {
					t.Errorf("%s bytes changed: %q, %v", field, got, err)
				}
			}
			if test.stream && form.Value["stream"][0] != "true" {
				t.Error("multipart stream flag missing")
			}
		})
	}
}

func TestImageEditInvalidInputsNeverReachUpstream(t *testing.T) {
	t.Parallel()
	urlImage := schemas.ImageInput{URL: "https://example.com/image.png"}
	tests := []struct {
		name     string
		model    string
		provider schemas.ModelProvider
		images   []schemas.ImageInput
		mask     []byte
		format   *string
		message  string
	}{
		{name: "Azure deployment", model: "gpt-image-1", provider: schemas.Azure, images: []schemas.ImageInput{urlImage}, message: "not enabled"},
		{name: "custom provider", model: "gpt-image-1", provider: schemas.ModelProvider("custom-openai"), images: []schemas.ImageInput{urlImage}, message: "not enabled"},
		{name: "mixed inputs", model: "gpt-image-1", provider: schemas.OpenAI, images: []schemas.ImageInput{urlImage, {Image: []byte("png")}}, message: "mix"},
		{name: "ambiguous input", model: "gpt-image-1", provider: schemas.OpenAI, images: []schemas.ImageInput{{URL: urlImage.URL, Image: []byte("png")}}, message: "both"},
		{name: "byte mask with URL", model: "gpt-image-1", provider: schemas.OpenAI, images: []schemas.ImageInput{urlImage}, mask: []byte("png"), message: "mask"},
		{name: "empty image", model: "gpt-image-1", provider: schemas.OpenAI, images: []schemas.ImageInput{{}}, message: "empty"},
		{name: "empty image list", model: "gpt-image-1", provider: schemas.OpenAI, images: []schemas.ImageInput{}, message: "image"},
		{name: "nil image list", model: "gpt-image-1", provider: schemas.OpenAI, message: "image"},
		{name: "blank URL", model: "gpt-image-1", provider: schemas.OpenAI, images: []schemas.ImageInput{{URL: "   "}}, message: "empty"},
		{name: "URL response format", model: "gpt-image-1", provider: schemas.OpenAI, images: []schemas.ImageInput{urlImage}, format: schemas.Ptr("url"), message: "response_format"},
	}
	for _, test := range tests {
		for _, stream := range []bool{false, true} {
			name := test.name + "/unary"
			if stream {
				name = test.name + "/streaming"
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				client, requests := imageEditWireClient(t)
				request := &schemas.BifrostImageEditRequest{
					Provider: test.provider,
					Model:    test.model,
					Input:    &schemas.ImageEditInput{Prompt: "Improve clarity", Images: test.images},
					Params:   &schemas.ImageEditParameters{Mask: test.mask, ResponseFormat: test.format},
				}
				err := callImageEditWire(
					schemas.NewBifrostContext(t.Context(), schemas.NoDeadline), client, request, test.provider, stream,
				)
				select {
				case <-requests:
					t.Error("invalid image input reached the upstream")
				default:
				}
				if err == nil || err.StatusCode == nil || *err.StatusCode != 400 || !strings.Contains(err.Error.Message, test.message) {
					t.Errorf("want 400 mentioning %q, got %+v", test.message, err)
				}
			})
		}
	}
}
