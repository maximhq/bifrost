package openai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pngBytes is enough of a PNG header for http.DetectContentType to report image/png.
var pngBytes = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")

type capturedImageEdit struct {
	contentType string
	body        []byte
}

func newImageEditCaptureServer(t *testing.T, status int, respBody string) (*httptest.Server, *capturedImageEdit) {
	t.Helper()
	got := &capturedImageEdit{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.contentType = r.Header.Get("Content-Type")
		got.body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(respBody))
	}))
	t.Cleanup(server.Close)
	return server, got
}

type imageEditJSONBody struct {
	Model   string `json:"model"`
	Prompt  string `json:"prompt"`
	Stream  *bool  `json:"stream"`
	Size    string `json:"size"`
	Quality string `json:"quality"`
	Images  []struct {
		ImageURL string `json:"image_url"`
	} `json:"images"`
	Mask *struct {
		ImageURL string `json:"image_url"`
	} `json:"mask"`
}

// Multipart has no field for a URL, so a URL reference must switch the request to OpenAI's JSON
// form (images[].image_url) rather than upload an empty image[] part.
func TestImageEditURLReferenceSendsJSONBody(t *testing.T) {
	server, got := newImageEditCaptureServer(t, http.StatusOK, `{"created":1,"data":[{"b64_json":"aGk="}]}`)
	provider := NewOpenAIProvider(&schemas.ProviderConfig{NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL}}, testNoopLogger{})
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	resp, bifrostErr := provider.ImageEdit(ctx, schemas.Key{Value: *schemas.NewSecretVar("test-key")}, &schemas.BifrostImageEditRequest{
		Model: "gpt-image-2",
		Input: &schemas.ImageEditInput{
			Prompt: "make it frosted",
			Images: []schemas.ImageInput{{URL: "https://example.com/scones.jpg"}, {Image: pngBytes}},
		},
		Params: &schemas.ImageEditParameters{Size: new("1024x1024"), Quality: new("low"), Mask: pngBytes},
	})
	require.Nil(t, bifrostErr)
	require.NotNil(t, resp)

	assert.True(t, strings.HasPrefix(got.contentType, "application/json"), "content-type = %q", got.contentType)
	var body imageEditJSONBody
	require.NoError(t, json.Unmarshal(got.body, &body), string(got.body))
	assert.Equal(t, "gpt-image-2", body.Model)
	assert.Equal(t, "make it frosted", body.Prompt)
	assert.Equal(t, "1024x1024", body.Size)
	assert.Equal(t, "low", body.Quality)
	require.Len(t, body.Images, 2)
	assert.Equal(t, "https://example.com/scones.jpg", body.Images[0].ImageURL)
	assert.True(t, strings.HasPrefix(body.Images[1].ImageURL, "data:image/png;base64,"), "bytes image = %q", body.Images[1].ImageURL)
	require.NotNil(t, body.Mask)
	assert.True(t, strings.HasPrefix(body.Mask.ImageURL, "data:image/png;base64,"), "mask = %q", body.Mask.ImageURL)
	assert.Nil(t, body.Stream)
}

// Byte-only edits keep the multipart upload they have always used.
func TestImageEditBytesOnlyStaysMultipart(t *testing.T) {
	server, got := newImageEditCaptureServer(t, http.StatusOK, `{"created":1,"data":[{"b64_json":"aGk="}]}`)
	provider := NewOpenAIProvider(&schemas.ProviderConfig{NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL}}, testNoopLogger{})
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	_, bifrostErr := provider.ImageEdit(ctx, schemas.Key{Value: *schemas.NewSecretVar("test-key")}, &schemas.BifrostImageEditRequest{
		Model: "gpt-image-2",
		Input: &schemas.ImageEditInput{Prompt: "make it blue", Images: []schemas.ImageInput{{Image: pngBytes}}},
	})
	require.Nil(t, bifrostErr)
	assert.True(t, strings.HasPrefix(got.contentType, "multipart/form-data"), "content-type = %q", got.contentType)
}

// The streaming edit shares the same switch and carries stream:true in the JSON body.
func TestImageEditStreamURLReferenceSendsJSONBody(t *testing.T) {
	server, got := newImageEditCaptureServer(t, http.StatusBadRequest, `{"error":{"message":"stop here","type":"invalid_request_error"}}`)
	provider := NewOpenAIProvider(&schemas.ProviderConfig{NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL}}, testNoopLogger{})
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	_, _ = provider.ImageEditStream(ctx, nil, nil, schemas.Key{Value: *schemas.NewSecretVar("test-key")}, &schemas.BifrostImageEditRequest{
		Model: "gpt-image-2",
		Input: &schemas.ImageEditInput{Prompt: "make it frosted", Images: []schemas.ImageInput{{URL: "https://example.com/scones.jpg"}}},
	})

	assert.True(t, strings.HasPrefix(got.contentType, "application/json"), "content-type = %q", got.contentType)
	var body imageEditJSONBody
	require.NoError(t, json.Unmarshal(got.body, &body), string(got.body))
	require.Len(t, body.Images, 1)
	assert.Equal(t, "https://example.com/scones.jpg", body.Images[0].ImageURL)
	require.NotNil(t, body.Stream)
	assert.True(t, *body.Stream)
}
