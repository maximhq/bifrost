package handlers

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/fasthttp/router"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/integrations"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/valyala/fasthttp"
)

// TestPrepareSpeechRequestVoiceValidation locks in the transport-layer voice
// requirement and its ElevenLabs carve-out. The transport cannot resolve
// virtual-key aliases, so it cannot tell a voice-less ElevenLabs sound-effect
// model (including an alias that resolves to one) from a text-to-speech model
// that omitted its voice. For ElevenLabs the voice check is therefore deferred
// to ElevenlabsProvider.Speech (which runs after alias resolution); every other
// provider keeps the handler-level "voice is required" 400.
func TestPrepareSpeechRequestVoiceValidation(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		wantError bool
	}{
		{
			// Regression: an alias like "my-sfx" -> "eleven_text_to_sound_v2"
			// carries no "eleven_text_to_sound" substring, so a name-based check
			// would wrongly 400 it before the provider could dispatch it.
			name:      "elevenlabs alias without voice is deferred to provider",
			body:      `{"model":"elevenlabs/my-sfx","input":"glass shattering on concrete"}`,
			wantError: false,
		},
		{
			name:      "elevenlabs literal sound model without voice is allowed",
			body:      `{"model":"elevenlabs/eleven_text_to_sound_v2","input":"rain on a tin roof"}`,
			wantError: false,
		},
		{
			name:      "elevenlabs tts without voice is deferred to provider (no transport 400)",
			body:      `{"model":"elevenlabs/eleven_multilingual_v2","input":"hello there"}`,
			wantError: false,
		},
		{
			name:      "non-elevenlabs provider without voice still errors at the transport",
			body:      `{"model":"openai/tts-1","input":"hello there"}`,
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := &fasthttp.RequestCtx{}
			ctx.Request.SetBodyString(tt.body)

			_, _, err := prepareSpeechRequest(ctx, nil)
			if tt.wantError && err == nil {
				t.Fatalf("expected voice-required error, got nil")
			}
			if !tt.wantError && err != nil {
				t.Fatalf("expected no error, got %v", err)
			}
			if tt.wantError && err != nil && err.Error() != "voice is required for speech completion" {
				t.Fatalf("error = %q, want %q", err.Error(), "voice is required for speech completion")
			}
		})
	}
}

type transcriptionTestAccount struct {
	baseURL string
	raw     bool
}

func (a transcriptionTestAccount) GetConfiguredProviders() ([]schemas.ModelProvider, error) {
	return []schemas.ModelProvider{schemas.Groq, schemas.OpenAI}, nil
}

func (a transcriptionTestAccount) GetKeysForProvider(_ context.Context, p schemas.ModelProvider) ([]schemas.Key, error) {
	return []schemas.Key{{ID: string(p) + "-test-key", Value: *schemas.NewSecretVar("test-key"), Models: schemas.WhiteList{"*"}, Weight: 1}}, nil
}

func (a transcriptionTestAccount) GetConfigForProvider(p schemas.ModelProvider) (*schemas.ProviderConfig, error) {
	if p != schemas.Groq && p != schemas.OpenAI {
		return nil, fmt.Errorf("unsupported provider %s", p)
	}
	nc := schemas.DefaultNetworkConfig
	nc.BaseURL = a.baseURL
	return &schemas.ProviderConfig{
		NetworkConfig: nc, ConcurrencyAndBufferSize: schemas.DefaultConcurrencyAndBufferSize,
		SendBackRawResponse: a.raw,
	}, nil
}

func transcriptionMultipartBody(tb testing.TB, model, format, prompt string) ([]byte, string) {
	tb.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	require.NoError(tb, w.WriteField("model", model))
	if format != "" {
		require.NoError(tb, w.WriteField("response_format", format))
	}
	if prompt != "" {
		require.NoError(tb, w.WriteField("prompt", prompt))
	}
	part, err := w.CreateFormFile("file", "sample.wav")
	require.NoError(tb, err)
	_, err = part.Write([]byte("fake-audio-bytes"))
	require.NoError(tb, err)
	require.NoError(tb, w.Close())
	return body.Bytes(), w.FormDataContentType()
}

// Exercise multipart ingress, the provider queue, and both response writers.
func TestTranscriptionRoutesResponseFormats(t *testing.T) {
	cases := []struct {
		name, model, format, contentType, body, prompt string
		status                                         int
	}{
		{name: "text", model: "groq/whisper-large-v3-turbo", format: "text", contentType: "text/plain; charset=utf-8", body: " .\n", status: 200},
		{name: "srt", model: "openai/whisper-1", format: "srt", contentType: "application/x-subrip; charset=utf-8", body: "1\r\n00:00:00,000 --> 00:00:01,000\r\n Héllo.\r\n\r\n", status: 200},
		{name: "vtt", model: "openai/whisper-1", format: "vtt", contentType: "text/vtt; charset=utf-8", body: "WEBVTT\n\n00:00.000 --> 00:01.000\nHello.\n", status: 200},
		{name: "default", model: "groq/whisper-large-v3-turbo", contentType: "application/json", body: `{"text":"Hello."}`, status: 200},
		{name: "json", model: "groq/whisper-large-v3-turbo", format: "json", contentType: "application/json", body: `{"text":"Hello."}`, status: 200},
		{name: "text_error", model: "groq/whisper-large-v3-turbo", format: "text", prompt: "reject", contentType: "application/json", body: `{"error":{"message":"invalid audio","type":"invalid_request_error"}}`, status: 400},
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		defer r.MultipartForm.RemoveAll()
		model := r.FormValue("model")
		if r.URL.Path != "/v1/audio/transcriptions" || (model != "whisper-1" && model != "whisper-large-v3-turbo") {
			t.Errorf("unexpected upstream path/model: %s / %s", r.URL.Path, r.FormValue("model"))
		}
		for _, tc := range cases {
			if r.FormValue("response_format") == tc.format && r.FormValue("prompt") == tc.prompt {
				w.Header().Set("Content-Type", tc.contentType)
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
				return
			}
		}
		http.Error(w, "unexpected response_format", http.StatusBadRequest)
	}))
	t.Cleanup(upstream.Close)

	for _, raw := range []bool{false, true} {
		t.Run(fmt.Sprintf("raw=%t", raw), func(t *testing.T) {
			client, err := bifrost.Init(context.Background(), schemas.BifrostConfig{
				Account: transcriptionTestAccount{baseURL: upstream.URL, raw: raw},
				Logger:  bifrost.NewDefaultLogger(schemas.LogLevelError),
			})
			require.NoError(t, err)
			t.Cleanup(func() { client.Shutdown() })
			r := router.New()
			identity := func(next fasthttp.RequestHandler) fasthttp.RequestHandler { return next }
			config := &lib.Config{ClientConfig: new(lib.DefaultClientConfig)}
			integrations.NewOpenAIRouter(client, config, nil, bifrost.NewDefaultLogger(schemas.LogLevelError)).RegisterRoutes(r, identity)
			integrations.NewLiteLLMRouter(client, config, nil, bifrost.NewDefaultLogger(schemas.LogLevelError)).RegisterRoutes(r, identity)
			integrations.NewLangChainRouter(client, config, nil, bifrost.NewDefaultLogger(schemas.LogLevelError)).RegisterRoutes(r, identity)
			integrations.NewPydanticAIRouter(client, config, nil, bifrost.NewDefaultLogger(schemas.LogLevelError)).RegisterRoutes(r, identity)
			NewInferenceHandler(nil, client, config).RegisterRoutes(r, identity)
			for _, route := range []string{"/v1/audio/transcriptions", "/openai/v1/audio/transcriptions", "/openai/audio/transcriptions", "/litellm/v1/audio/transcriptions", "/langchain/v1/audio/transcriptions", "/pydanticai/v1/audio/transcriptions"} {
				for _, tc := range cases {
					t.Run(route+"/"+tc.name, func(t *testing.T) {
						t.Parallel()
						body, contentType := transcriptionMultipartBody(t, tc.model, tc.format, tc.prompt)
						var ctx fasthttp.RequestCtx
						ctx.Request.SetRequestURI(route)
						ctx.Request.Header.SetMethod(fasthttp.MethodPost)
						ctx.Request.Header.SetContentType(contentType)
						ctx.Request.SetBody(body)
						defer ctx.Request.RemoveMultipartFormFiles()
						r.Handler(&ctx)
						require.Equal(t, tc.status, ctx.Response.StatusCode(), "body: %s", ctx.Response.Body())
						if tc.contentType != "application/json" {
							require.Equal(t, tc.body, string(ctx.Response.Body()), "non-JSON body must be byte-for-byte preserved")
						} else if tc.status == 200 {
							require.Equal(t, "Hello.", gjson.GetBytes(ctx.Response.Body(), "text").String())
						} else {
							require.Contains(t, string(ctx.Response.Body()), "invalid audio")
						}
						require.Equal(t, tc.contentType, string(ctx.Response.Header.ContentType()))
					})
				}
			}
		})
	}
}

// Cache/plugin responses may omit ResponseFormat. Serialize the edited Text, not RawResponse.
type transcriptionTestPlugin struct {
	postFormat *string
}

func (transcriptionTestPlugin) GetName() string { return "transcription-test" }
func (transcriptionTestPlugin) Cleanup() error  { return nil }
func (transcriptionTestPlugin) PreRequestHook(*schemas.BifrostContext, *schemas.BifrostRequest) error {
	return nil
}
func (transcriptionTestPlugin) PreLLMHook(_ *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	return req, &schemas.LLMPluginShortCircuit{Response: &schemas.BifrostResponse{
		TranscriptionResponse: &schemas.BifrostTranscriptionResponse{
			Text:        "original\n",
			ExtraFields: schemas.BifrostResponseExtraFields{RawResponse: "original\n"},
		},
	}}, nil
}
func (p transcriptionTestPlugin) PostLLMHook(_ *schemas.BifrostContext, resp *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	if resp != nil && resp.TranscriptionResponse != nil {
		resp.TranscriptionResponse.Text = "edited\n"
		if p.postFormat != nil {
			resp.TranscriptionResponse.ResponseFormat = p.postFormat
		}
	}
	return resp, err, nil
}

func TestTranscriptionPluginResponseFormats(t *testing.T) {
	for _, tc := range []struct {
		name, format string
		postFormat   *string
	}{
		{name: "default"},
		{name: "text", format: "text"},
		{name: "explicit post-hook JSON", format: "text", postFormat: schemas.Ptr("json")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, err := bifrost.Init(context.Background(), schemas.BifrostConfig{
				Account:    transcriptionTestAccount{baseURL: "http://127.0.0.1:1"},
				LLMPlugins: []schemas.LLMPlugin{transcriptionTestPlugin{postFormat: tc.postFormat}},
				Logger:     bifrost.NewDefaultLogger(schemas.LogLevelError),
			})
			require.NoError(t, err)
			defer client.Shutdown()
			r := router.New()
			config := &lib.Config{ClientConfig: new(lib.DefaultClientConfig)}
			identity := func(next fasthttp.RequestHandler) fasthttp.RequestHandler { return next }
			integrations.NewOpenAIRouter(client, config, nil, bifrost.NewDefaultLogger(schemas.LogLevelError)).RegisterRoutes(r, identity)
			NewInferenceHandler(nil, client, config).RegisterRoutes(r, identity)
			for _, route := range []string{"/v1/audio/transcriptions", "/openai/v1/audio/transcriptions"} {
				t.Run(route, func(t *testing.T) {
					body, ct := transcriptionMultipartBody(t, "groq/whisper-large-v3-turbo", tc.format, "")
					var ctx fasthttp.RequestCtx
					ctx.Request.SetRequestURI(route)
					ctx.Request.Header.SetMethod(fasthttp.MethodPost)
					ctx.Request.Header.SetContentType(ct)
					ctx.Request.SetBody(body)
					defer ctx.Request.RemoveMultipartFormFiles()
					r.Handler(&ctx)
					require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode())
					if tc.format == "" || tc.postFormat != nil {
						require.Equal(t, "application/json", string(ctx.Response.Header.ContentType()))
						require.Equal(t, "edited\n", gjson.GetBytes(ctx.Response.Body(), "text").String())
					} else {
						require.Equal(t, "edited\n", string(ctx.Response.Body()))
					}
				})
			}
		})
	}
}

// Measures native multipart ingress, core/plugin handling and HTTP serialization
// without upstream socket latency. The same plugin fixture runs before and after.
func BenchmarkTranscriptionResponseFormats(b *testing.B) {
	client, err := bifrost.Init(context.Background(), schemas.BifrostConfig{
		Account:    transcriptionTestAccount{baseURL: "http://127.0.0.1:1"},
		LLMPlugins: []schemas.LLMPlugin{transcriptionTestPlugin{}}, Logger: bifrost.NewDefaultLogger(schemas.LogLevelError),
	})
	require.NoError(b, err)
	defer client.Shutdown()
	h := NewInferenceHandler(nil, client, &lib.Config{ClientConfig: new(lib.DefaultClientConfig)})
	for _, format := range []string{"", "json", "text"} {
		name := format
		if name == "" {
			name = "default"
		}
		b.Run(name, func(b *testing.B) {
			body, ct := transcriptionMultipartBody(b, "groq/whisper-large-v3-turbo", format, "")
			var ctx fasthttp.RequestCtx
			ctx.Request.SetRequestURI("/v1/audio/transcriptions")
			ctx.Request.Header.SetMethod(fasthttp.MethodPost)
			ctx.Request.Header.SetContentType(ct)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				ctx.Request.SetBody(body)
				h.transcription(&ctx)
				ctx.Request.RemoveMultipartFormFiles()
				ctx.Response.Reset()
				ctx.ResetUserValues()
			}
		})
	}
}
