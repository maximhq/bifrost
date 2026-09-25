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

type testNoopLogger struct{}

func (testNoopLogger) Debug(string, ...any)                   {}
func (testNoopLogger) Info(string, ...any)                    {}
func (testNoopLogger) Warn(string, ...any)                    {}
func (testNoopLogger) Error(string, ...any)                   {}
func (testNoopLogger) Fatal(string, ...any)                   {}
func (testNoopLogger) SetLevel(schemas.LogLevel)              {}
func (testNoopLogger) SetOutputType(schemas.LoggerOutputType) {}
func (testNoopLogger) LogHTTPRequest(schemas.LogLevel, string) schemas.LogEventBuilder {
	return schemas.NoopLogEvent
}

func TestListModelsByKey_ParsesSingleModelPayload(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "unexpected method", http.StatusMethodNotAllowed)
			return
		}
		if r.URL.Path != "/models/gemini-2.5-pro" {
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"name":"models/gemini-2.5-pro","displayName":"Gemini 2.5 Pro","description":"test","inputTokenLimit":1048576,"outputTokenLimit":8192,"supportedGenerationMethods":["generateContent"]}`))
	}))
	defer ts.Close()

	provider := NewGeminiProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: ts.URL},
	}, testNoopLogger{})

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyURLPath, "/models/gemini-2.5-pro")

	key := schemas.Key{Value: *schemas.NewSecretVar("dummy-key")}
	// Unfiltered=true bypasses the allowed/alias/blacklist filter pipeline so
	// this test can focus on the single-model-payload parsing code path in
	// listModelsByKey (gemini.go:215-220).
	resp, err := provider.listModelsByKey(ctx, key, &schemas.BifrostListModelsRequest{Provider: schemas.Gemini, Unfiltered: true})
	require.Nil(t, err)
	require.NotNil(t, resp)
	require.Len(t, resp.Data, 1)
	assert.Equal(t, "gemini/gemini-2.5-pro", resp.Data[0].ID)
	require.NotNil(t, resp.Data[0].Name)
	assert.Equal(t, "Gemini 2.5 Pro", *resp.Data[0].Name)
}

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
			gotHeaders := make(chan http.Header, 1)
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				select {
				case gotHeaders <- r.Header.Clone():
				default:
				}
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

			var headers http.Header
			select {
			case headers = <-gotHeaders:
			case <-time.After(5 * time.Second):
				t.Fatal("upstream request was never received")
			}
			assert.Equal(t, tc.wantGoogAPIKey, headers.Get("x-goog-api-key"), "x-goog-api-key header")
			assert.Equal(t, tc.wantAuth, headers.Get("Authorization"), "forwarded Authorization header")
		})
	}
}

// TestHandleGeminiStreams_StripsForwardedAuthorization covers the streaming map
// paths (chat + responses) flagged by review (Greptile P2): the headers map only
// ever holds x-goog-api-key/Accept/Cache-Control, so deleting "Authorization"
// from that map was a no-op. SetExtraHeaders injects Authorization onto the real
// request, so the strip must happen on req *after* the headers are applied.
func TestHandleGeminiStreams_StripsForwardedAuthorization(t *testing.T) {
	noopPostHook := func(_ *schemas.BifrostContext, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
		return result, err
	}
	handlers := []struct {
		name  string
		start func(ctx *schemas.BifrostContext, url string, headers, extra map[string]string) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError)
	}{
		{name: "chat", start: func(ctx *schemas.BifrostContext, url string, headers, extra map[string]string) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
			return HandleGeminiChatCompletionStream(ctx, &fasthttp.Client{}, url, []byte(`{}`), headers, extra, 30, false, false, schemas.Gemini, "gemini-2.5-pro", noopPostHook, nil, testNoopLogger{}, func(context.Context) {})
		}},
		{name: "responses", start: func(ctx *schemas.BifrostContext, url string, headers, extra map[string]string) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
			return HandleGeminiResponsesStream(ctx, &fasthttp.Client{}, url, []byte(`{}`), headers, extra, 30, false, false, schemas.Gemini, "gemini-2.5-pro", noopPostHook, nil, testNoopLogger{}, func(context.Context) {})
		}},
	}
	cases := []struct {
		name      string
		apiKey    string // value in the headers map; omitted when omitKey is set
		omitKey   bool   // provider key empty: callers leave x-goog-api-key out of the map
		configKey string // x-goog-api-key supplied through network-config extra headers
		wantKey   string
		wantAuth  string
	}{
		{name: "api_key_set_strips_authorization", apiKey: "dummy-key", wantKey: "dummy-key", wantAuth: ""},
		// An empty x-goog-api-key must not cost the request its only credential.
		{name: "empty_api_key_keeps_authorization", apiKey: "", wantKey: "", wantAuth: "Bearer leaked-token"},
		// The strip keys off the header actually applied to the request, so an API key
		// arriving through extra headers still drops the forwarded Authorization.
		{name: "extra_header_api_key_strips_authorization", omitKey: true, configKey: "config-key", wantKey: "config-key", wantAuth: ""},
	}
	for _, hd := range handlers {
		for _, tc := range cases {
			t.Run(hd.name+"/"+tc.name, func(t *testing.T) {
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

				reqHeaders := map[string]string{
					"Accept":        "text/event-stream",
					"Cache-Control": "no-cache",
				}
				if !tc.omitKey {
					reqHeaders["x-goog-api-key"] = tc.apiKey
				}
				extra := map[string]string{"Authorization": "Bearer leaked-token"} // injected via SetExtraHeaders
				if tc.configKey != "" {
					extra["X-Goog-Api-Key"] = tc.configKey
				}
				stream, bifrostErr := hd.start(
					schemas.NewBifrostContext(context.Background(), schemas.NoDeadline),
					ts.URL+"/models/gemini-2.5-pro:streamGenerateContent?alt=sse",
					reqHeaders,
					extra,
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

				assert.Equal(t, tc.wantKey, headers.Get("x-goog-api-key"), "x-goog-api-key header")
				assert.Equal(t, tc.wantAuth, headers.Get("Authorization"), "Authorization header")
			})
		}
	}
}
