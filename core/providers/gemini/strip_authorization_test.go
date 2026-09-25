package gemini

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

const stripAuthSingleModelPayload = `{"name":"models/gemini-2.5-pro","displayName":"Gemini 2.5 Pro","description":"test","inputTokenLimit":1048576,"outputTokenLimit":8192,"supportedGenerationMethods":["generateContent"]}`

// TestStripsForwardedAuthorizationWhenAPIKeySet verifies the Vertex Express fix:
// when the provider authenticates with x-goog-api-key, an Authorization header
// injected by SetExtraHeaders (the same path a forwarded x-bf-eh-authorization
// context header takes) must be stripped before the request reaches upstream —
// otherwise Vertex Express rejects the dual-credential request. When no API key
// is present, Authorization must be left intact, since it may be the only auth.
func TestStripsForwardedAuthorizationWhenAPIKeySet(t *testing.T) {
	cases := []struct {
		name           string
		apiKey         string
		wantGoogAPIKey string
		wantAuth       string // expected upstream Authorization ("" => stripped)
	}{
		{name: "api_key_set_strips_authorization", apiKey: "dummy-key", wantGoogAPIKey: "dummy-key", wantAuth: ""},
		{name: "no_api_key_keeps_authorization", apiKey: "", wantGoogAPIKey: "", wantAuth: "Bearer leaked-token"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotAuth, gotKey string
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotAuth = r.Header.Get("Authorization")
				gotKey = r.Header.Get("x-goog-api-key")
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(stripAuthSingleModelPayload))
			}))
			defer ts.Close()

			provider := NewGeminiProvider(&schemas.ProviderConfig{
				NetworkConfig: schemas.NetworkConfig{
					BaseURL:      ts.URL,
					ExtraHeaders: map[string]string{"Authorization": "Bearer leaked-token"},
				},
			}, testNoopLogger{})

			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			ctx.SetValue(schemas.BifrostContextKeyURLPath, "/models/gemini-2.5-pro")

			key := schemas.Key{Value: *schemas.NewSecretVar(tc.apiKey)}
			_, bifrostErr := provider.listModelsByKey(ctx, key, &schemas.BifrostListModelsRequest{
				Provider:   schemas.Gemini,
				Unfiltered: true,
			})
			require.Nil(t, bifrostErr)

			assert.Equal(t, tc.wantGoogAPIKey, gotKey, "x-goog-api-key header")
			assert.Equal(t, tc.wantAuth, gotAuth, "forwarded Authorization header")
		})
	}
}

// TestHandleGeminiChatCompletionStream_StripsForwardedAuthorization covers the
// streaming map path flagged by review (Greptile P2): the headers map only ever
// holds x-goog-api-key/Accept/Cache-Control, so deleting "Authorization" from
// that map was a no-op. SetExtraHeaders injects Authorization onto the real
// request, so the strip must happen on req *after* the headers are applied.
func TestHandleGeminiChatCompletionStream_StripsForwardedAuthorization(t *testing.T) {
	gotHeaders := make(chan http.Header, 1)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case gotHeaders <- r.Header.Clone():
		default:
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		_, _ = w.Write([]byte("data: {}\n\n"))
	}))
	defer ts.Close()

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	noopPostHook := func(_ *schemas.BifrostContext, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
		return result, err
	}

	stream, bifrostErr := HandleGeminiChatCompletionStream(
		ctx,
		&fasthttp.Client{},
		ts.URL+"/models/gemini-2.5-pro:streamGenerateContent?alt=sse",
		[]byte(`{}`),
		map[string]string{
			"x-goog-api-key": "dummy-key",
			"Accept":         "text/event-stream",
			"Cache-Control":  "no-cache",
		},
		map[string]string{"Authorization": "Bearer leaked-token"}, // injected via SetExtraHeaders
		30, // streamIdleTimeoutInSeconds
		false,
		false,
		schemas.Gemini,
		"gemini-2.5-pro",
		noopPostHook,
		nil,
		testNoopLogger{},
		func(context.Context) {},
	)
	require.Nil(t, bifrostErr)
	require.NotNil(t, stream)

	// Drain so the request completes and streaming resources are released.
	drained := make(chan struct{})
	go func() {
		for range stream {
		}
		close(drained)
	}()

	var headers http.Header
	select {
	case headers = <-gotHeaders:
	case <-time.After(5 * time.Second):
		t.Fatal("upstream request was never received")
	}
	select {
	case <-drained:
	case <-time.After(5 * time.Second):
		t.Fatal("stream did not close")
	}

	assert.Equal(t, "dummy-key", headers.Get("x-goog-api-key"), "x-goog-api-key should be set")
	assert.Empty(t, headers.Get("Authorization"), "forwarded Authorization must be stripped on the streaming path")
}
