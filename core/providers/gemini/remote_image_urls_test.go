package gemini

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInlineRemoteImageURLsForGeminiChatConvertsHTTPToInlineData(t *testing.T) {
	remoteURL := "https://cdn.example.com/image.jpeg?sig=secret"
	imageBytes := []byte("\x89PNG\r\n\x1a\nfake png bytes")
	encoded := base64.StdEncoding.EncodeToString(imageBytes)
	stubGeminiImageFetch(t, func(_ context.Context, gotURL string) (string, string, error) {
		assert.Equal(t, remoteURL, gotURL)
		return "image/png", encoded, nil
	})

	req := geminiChatImageRequest(remoteURL)
	require.NoError(t, inlineRemoteImageURLsForGeminiChat(nil, req))
	assert.Equal(t, "data:image/png;base64,"+encoded, req.Input[0].Content.ContentBlocks[1].ImageURLStruct.URL)

	out, err := ToGeminiChatCompletionRequest(nil, req)
	require.NoError(t, err)
	require.Len(t, out.Contents, 1)
	require.Len(t, out.Contents[0].Parts, 2)
	assert.Nil(t, out.Contents[0].Parts[1].FileData)
	require.NotNil(t, out.Contents[0].Parts[1].InlineData)
	assert.Equal(t, "image/png", out.Contents[0].Parts[1].InlineData.MIMEType)
	assert.Equal(t, encoded, out.Contents[0].Parts[1].InlineData.Data)
}

func TestInlineRemoteImageURLsForGeminiResponsesConvertsHTTPToInlineData(t *testing.T) {
	remoteURL := "https://cdn.example.com/image.jpeg"
	encoded := base64.StdEncoding.EncodeToString([]byte("\xff\xd8\xff\xe0fake jpeg bytes"))
	stubGeminiImageFetch(t, func(_ context.Context, gotURL string) (string, string, error) {
		assert.Equal(t, remoteURL, gotURL)
		return "image/jpeg", encoded, nil
	})

	req := geminiResponsesImageRequest(remoteURL)
	require.NoError(t, inlineRemoteImageURLsForGeminiResponses(nil, req))
	require.Equal(t, "data:image/jpeg;base64,"+encoded, *req.Input[0].Content.ContentBlocks[1].ResponsesInputMessageContentBlockImage.ImageURL)

	out, err := ToGeminiResponsesRequest(nil, req)
	require.NoError(t, err)
	require.Len(t, out.Contents, 1)
	require.Len(t, out.Contents[0].Parts, 2)
	assert.Nil(t, out.Contents[0].Parts[1].FileData)
	require.NotNil(t, out.Contents[0].Parts[1].InlineData)
	assert.Equal(t, "image/jpeg", out.Contents[0].Parts[1].InlineData.MIMEType)
	assert.Equal(t, encoded, out.Contents[0].Parts[1].InlineData.Data)
}

func TestInlineRemoteImageURLsForGeminiLeavesDataURLInline(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\nfake png bytes"))
	dataURL := "data:image/png;base64," + encoded
	stubGeminiImageFetch(t, func(context.Context, string) (string, string, error) {
		t.Fatal("data URLs must not be fetched")
		return "", "", nil
	})

	req := geminiChatImageRequest(dataURL)
	require.NoError(t, inlineRemoteImageURLsForGeminiChat(nil, req))
	assert.Equal(t, dataURL, req.Input[0].Content.ContentBlocks[1].ImageURLStruct.URL)

	out, err := ToGeminiChatCompletionRequest(nil, req)
	require.NoError(t, err)
	require.NotNil(t, out.Contents[0].Parts[1].InlineData)
	assert.Equal(t, encoded, out.Contents[0].Parts[1].InlineData.Data)
}

func TestInlineRemoteImageURLsForGeminiReportsInvalidRequest(t *testing.T) {
	tests := []struct {
		name string
		url  string
		err  error
		want string
	}{
		{
			name: "unsupported scheme",
			url:  "gs://bucket/image.png",
			err:  nil,
			want: `URL scheme "gs" is not allowed`,
		},
		{
			name: "non-image fetch result",
			url:  "https://cdn.example.com/not-image",
			err:  errors.New(`fetched resource "https://cdn.example.com/not-image" is not an image: content-type "text/html"`),
			want: "not an image",
		},
		{
			name: "too large fetch result",
			url:  "https://cdn.example.com/huge.png",
			err:  errors.New(`resource at "https://cdn.example.com/huge.png" exceeds 26214400-byte limit`),
			want: "exceeds 26214400-byte limit",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.err != nil {
				stubGeminiImageFetch(t, func(context.Context, string) (string, string, error) {
					return "", "", tt.err
				})
			}

			err := inlineRemoteImageURLsForGeminiChat(nil, geminiChatImageRequest(tt.url))
			require.Error(t, err)
			assert.Contains(t, err.Error(), "messages[0].content[1].image_url")
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}

func TestInlineRemoteImageURLsForGeminiBlocksPrivateURL(t *testing.T) {
	err := inlineRemoteImageURLsForGeminiChat(nil, geminiChatImageRequest("https://127.0.0.1/private.png?X-Amz-Signature=deadbeefcafe"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "messages[0].content[1].image_url")
	assert.NotContains(t, err.Error(), "deadbeefcafe")
	assert.NotContains(t, err.Error(), "X-Amz-Signature")
}

func TestGeminiProviderChatCompletionInlinesRemoteImageURL(t *testing.T) {
	captured := make(chan string, 1)
	provider := newGeminiCaptureProvider(t, captured, func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],"modelVersion":"gemini-3.5-flash","usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`))
	})
	encoded := base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\nfake png bytes"))
	stubGeminiImageFetch(t, func(context.Context, string) (string, string, error) {
		return "image/png", encoded, nil
	})

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	resp, bifrostErr := provider.ChatCompletion(ctx, schemas.Key{Value: *schemas.NewSecretVar("dummy-key")}, geminiChatImageRequest("https://cdn.example.com/image.png"))
	require.Nil(t, bifrostErr)
	require.NotNil(t, resp)

	body := <-captured
	assert.Contains(t, body, `"inlineData"`)
	assert.Contains(t, body, `"mimeType":"image/png"`)
	assert.NotContains(t, body, `"fileData"`)
	assert.NotContains(t, body, "https://cdn.example.com/image.png")
}

func TestGeminiProviderChatCompletionStreamInlinesRemoteImageURL(t *testing.T) {
	captured := make(chan string, 1)
	provider := newGeminiCaptureProvider(t, captured, func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"ok\"}]},\"finishReason\":\"STOP\"}],\"modelVersion\":\"gemini-3.5-flash\",\"usageMetadata\":{\"promptTokenCount\":1,\"candidatesTokenCount\":1,\"totalTokenCount\":2}}\n\n"))
	})
	encoded := base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\nfake png bytes"))
	stubGeminiImageFetch(t, func(context.Context, string) (string, string, error) {
		return "image/png", encoded, nil
	})

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	stream, bifrostErr := provider.ChatCompletionStream(
		ctx,
		func(ctx *schemas.BifrostContext, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
			return result, err
		},
		nil,
		schemas.Key{Value: *schemas.NewSecretVar("dummy-key")},
		geminiChatImageRequest("https://cdn.example.com/image.png"),
	)
	require.Nil(t, bifrostErr)

	for range stream {
	}

	body := <-captured
	assert.Contains(t, body, `"inlineData"`)
	assert.Contains(t, body, `"mimeType":"image/png"`)
	assert.NotContains(t, body, `"fileData"`)
	assert.NotContains(t, body, "https://cdn.example.com/image.png")
}

func TestGeminiProviderResponsesInlinesRemoteImageURL(t *testing.T) {
	captured := make(chan string, 1)
	provider := newGeminiCaptureProvider(t, captured, func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],"modelVersion":"gemini-3.5-flash","usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`))
	})
	encoded := base64.StdEncoding.EncodeToString([]byte("\xff\xd8\xff\xe0fake jpeg bytes"))
	stubGeminiImageFetch(t, func(context.Context, string) (string, string, error) {
		return "image/jpeg", encoded, nil
	})

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	resp, bifrostErr := provider.Responses(ctx, schemas.Key{Value: *schemas.NewSecretVar("dummy-key")}, geminiResponsesImageRequest("https://cdn.example.com/image.jpeg"))
	require.Nil(t, bifrostErr)
	require.NotNil(t, resp)

	body := <-captured
	assert.Contains(t, body, `"inlineData"`)
	assert.Contains(t, body, `"mimeType":"image/jpeg"`)
	assert.NotContains(t, body, `"fileData"`)
	assert.NotContains(t, body, "https://cdn.example.com/image.jpeg")
}

func TestGeminiProviderResponsesStreamInlinesRemoteImageURL(t *testing.T) {
	captured := make(chan string, 1)
	provider := newGeminiCaptureProvider(t, captured, func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"ok\"}]},\"finishReason\":\"STOP\"}],\"modelVersion\":\"gemini-3.5-flash\",\"usageMetadata\":{\"promptTokenCount\":1,\"candidatesTokenCount\":1,\"totalTokenCount\":2}}\n\n"))
	})
	encoded := base64.StdEncoding.EncodeToString([]byte("\xff\xd8\xff\xe0fake jpeg bytes"))
	stubGeminiImageFetch(t, func(context.Context, string) (string, string, error) {
		return "image/jpeg", encoded, nil
	})

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	stream, bifrostErr := provider.ResponsesStream(
		ctx,
		func(ctx *schemas.BifrostContext, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
			return result, err
		},
		nil,
		schemas.Key{Value: *schemas.NewSecretVar("dummy-key")},
		geminiResponsesImageRequest("https://cdn.example.com/image.jpeg"),
	)
	require.Nil(t, bifrostErr)

	for range stream {
	}

	body := <-captured
	assert.Contains(t, body, `"inlineData"`)
	assert.Contains(t, body, `"mimeType":"image/jpeg"`)
	assert.NotContains(t, body, `"fileData"`)
	assert.NotContains(t, body, "https://cdn.example.com/image.jpeg")
}

func TestGeminiProviderCountTokensInlinesRemoteImageURL(t *testing.T) {
	captured := make(chan string, 1)
	provider := newGeminiCaptureProvider(t, captured, func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"totalTokens":7,"promptTokensDetails":[{"modality":"TEXT","tokenCount":7}]}`))
	})
	encoded := base64.StdEncoding.EncodeToString([]byte("\xff\xd8\xff\xe0fake jpeg bytes"))
	stubGeminiImageFetch(t, func(context.Context, string) (string, string, error) {
		return "image/jpeg", encoded, nil
	})

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	resp, bifrostErr := provider.CountTokens(ctx, schemas.Key{Value: *schemas.NewSecretVar("dummy-key")}, geminiResponsesImageRequest("https://cdn.example.com/image.jpeg"))
	require.Nil(t, bifrostErr)
	require.NotNil(t, resp)

	body := <-captured
	assert.Contains(t, body, `"generateContentRequest"`)
	assert.Contains(t, body, `"inlineData"`)
	assert.Contains(t, body, `"mimeType":"image/jpeg"`)
	assert.NotContains(t, body, `"fileData"`)
	assert.NotContains(t, body, "https://cdn.example.com/image.jpeg")
}

func stubGeminiImageFetch(t *testing.T, fn func(context.Context, string) (string, string, error)) {
	t.Helper()
	previous := fetchAndEncodeGeminiImageURL
	fetchAndEncodeGeminiImageURL = fn
	t.Cleanup(func() {
		fetchAndEncodeGeminiImageURL = previous
	})
}

func geminiChatImageRequest(imageURL string) *schemas.BifrostChatRequest {
	return &schemas.BifrostChatRequest{
		Model: "gemini-3.5-flash",
		Input: []schemas.ChatMessage{{
			Role: schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{
				ContentBlocks: []schemas.ChatContentBlock{
					{Type: schemas.ChatContentBlockTypeText, Text: schemas.Ptr("Describe this image.")},
					{Type: schemas.ChatContentBlockTypeImage, ImageURLStruct: &schemas.ChatInputImage{URL: imageURL}},
				},
			},
		}},
	}
}

func geminiResponsesImageRequest(imageURL string) *schemas.BifrostResponsesRequest {
	return &schemas.BifrostResponsesRequest{
		Model: "gemini-3.5-flash",
		Input: []schemas.ResponsesMessage{{
			Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
			Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
			Content: &schemas.ResponsesMessageContent{
				ContentBlocks: []schemas.ResponsesMessageContentBlock{
					{Type: schemas.ResponsesInputMessageContentBlockTypeText, Text: schemas.Ptr("Describe this image.")},
					{
						Type: schemas.ResponsesInputMessageContentBlockTypeImage,
						ResponsesInputMessageContentBlockImage: &schemas.ResponsesInputMessageContentBlockImage{
							ImageURL: schemas.Ptr(imageURL),
						},
					},
				},
			},
		}},
	}
}

func newGeminiCaptureProvider(t *testing.T, captured chan<- string, writeResponse func(http.ResponseWriter)) *GeminiProvider {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		captured <- string(body)
		writeResponse(w)
	}))
	t.Cleanup(server.Close)

	return NewGeminiProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL:             server.URL + "/v1beta",
			AllowPrivateNetwork: true,
		},
	}, testNoopLogger{})
}
