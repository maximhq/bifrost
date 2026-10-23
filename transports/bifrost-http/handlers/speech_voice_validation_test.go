package handlers

import (
	"bytes"
	"context"
	"encoding/json"
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

func transcriptionMultipartBody(tb testing.TB, model, format, prompt string, fallbacks ...string) ([]byte, string) {
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
	for _, fallback := range fallbacks {
		require.NoError(tb, w.WriteField("fallbacks", fallback))
	}
	part, err := w.CreateFormFile("file", "sample.wav")
	require.NoError(tb, err)
	_, err = part.Write([]byte("fake-audio-bytes"))
	require.NoError(tb, err)
	require.NoError(tb, w.Close())
	return body.Bytes(), w.FormDataContentType()
}

// Exercise the actual multipart ingress, core queue, native Groq/OpenAI provider,
// and both HTTP response writers. No live API key or audio decoding is needed.
func TestTranscriptionRoutesResponseFormats(t *testing.T) {
	cases := []struct {
		name, format, contentType, body, prompt string
		status                                  int
	}{
		{name: "text", format: "text", contentType: "text/plain; charset=utf-8", body: " .\n", status: 200},
		{name: "srt", format: "srt", contentType: "application/x-subrip; charset=utf-8", body: "1\r\n00:00:00,000 --> 00:00:01,000\r\n Héllo.\r\n\r\n", status: 200},
		{name: "vtt", format: "vtt", contentType: "text/vtt; charset=utf-8", body: "WEBVTT\n\n00:00.000 --> 00:01.000\nHello.\n", status: 200},
		{name: "default", contentType: "application/json", body: `{"text":"Hello."}`, status: 200},
		{name: "json", format: "json", contentType: "application/json", body: `{"text":"Hello."}`, status: 200},
		{name: "verbose_json", format: "verbose_json", contentType: "application/json", body: `{"text":"Hello.","duration":1.5,"segments":[{"id":0,"text":"Hello."}]}`, status: 200},
		{name: "text_error", format: "text", prompt: "reject", contentType: "application/json", body: `{"error":{"message":"invalid audio","type":"invalid_request_error"}}`, status: 400},
		{name: "srt_error", format: "srt", prompt: "reject", contentType: "application/json", body: `{"error":{"message":"invalid audio","type":"invalid_request_error"}}`, status: 400},
		{name: "vtt_error", format: "vtt", prompt: "reject", contentType: "application/json", body: `{"error":{"message":"invalid audio","type":"invalid_request_error"}}`, status: 400},
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
		file, header, err := r.FormFile("file")
		if err != nil {
			t.Error(err)
			http.Error(w, "file missing", http.StatusBadRequest)
			return
		}
		defer file.Close()
		audio, err := io.ReadAll(file)
		if err != nil || string(audio) != "fake-audio-bytes" || header.Filename != "sample.wav" {
			t.Errorf("unexpected upstream file: %q, %q, %v", header.Filename, audio, err)
		}
		for _, tc := range cases {
			if r.FormValue("response_format") == tc.format && r.FormValue("prompt") == tc.prompt {
				w.Header().Set("Content-Type", tc.contentType)
				w.Header().Set("X-Upstream-Test", "forwarded")
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
				return
			}
		}
		http.Error(w, "unexpected response_format", http.StatusBadRequest)
	}))
	t.Cleanup(upstream.Close)

	for _, mode := range []struct{ raw, headers bool }{{false, false}, {true, false}, {false, true}, {true, true}} {
		t.Run(fmt.Sprintf("raw=%t/headers=%t", mode.raw, mode.headers), func(t *testing.T) {
			client, err := bifrost.Init(context.Background(), schemas.BifrostConfig{
				Account: transcriptionTestAccount{baseURL: upstream.URL, raw: mode.raw},
				Logger:  bifrost.NewDefaultLogger(schemas.LogLevelError),
			})
			require.NoError(t, err)
			t.Cleanup(func() { client.Shutdown() })
			r := router.New()
			identity := func(next fasthttp.RequestHandler) fasthttp.RequestHandler { return next }
			config := &lib.Config{ClientConfig: new(lib.DefaultClientConfig)}
			if mode.headers {
				config.ClientConfig.ExtraFieldsPlacement = "headers"
			}
			integrations.NewOpenAIRouter(client, config, nil, bifrost.NewDefaultLogger(schemas.LogLevelError)).RegisterRoutes(r, identity)
			NewInferenceHandler(nil, client, config).RegisterRoutes(r, identity)
			for _, provider := range []string{"groq", "openai"} {
				model := "whisper-1"
				if provider == "groq" {
					model = "whisper-large-v3-turbo"
				}
				for _, route := range []string{"/v1/audio/transcriptions", "/openai/v1/audio/transcriptions", "/openai/audio/transcriptions"} {
					for _, tc := range cases {
						// Groq documents JSON/text; pin subtitle formats on OpenAI.
						if provider == "groq" && (tc.format == "srt" || tc.format == "vtt") {
							continue
						}
						t.Run(provider+route+"/"+tc.name, func(t *testing.T) {
							t.Parallel()
							body, contentType := transcriptionMultipartBody(t, provider+"/"+model, tc.format, tc.prompt)
							var ctx fasthttp.RequestCtx
							ctx.Request.SetRequestURI(route)
							ctx.Request.Header.SetMethod(fasthttp.MethodPost)
							ctx.Request.Header.SetContentType(contentType)
							ctx.Request.SetBody(body)
							defer ctx.Request.RemoveMultipartFormFiles()
							r.Handler(&ctx)
							require.Equal(t, tc.status, ctx.Response.StatusCode(), "body: %s", ctx.Response.Body())
							require.Equal(t, "forwarded", string(ctx.Response.Header.Peek("X-Upstream-Test")))
							if tc.contentType != "application/json" {
								require.Equal(t, tc.body, string(ctx.Response.Body()), "non-JSON body must be byte-for-byte preserved")
							} else if tc.status == 200 {
								require.Equal(t, "Hello.", gjson.GetBytes(ctx.Response.Body(), "text").String())
								if mode.headers {
									require.False(t, gjson.GetBytes(ctx.Response.Body(), "extra_fields").Exists())
								}
								if tc.format == "verbose_json" {
									require.Equal(t, 1.5, gjson.GetBytes(ctx.Response.Body(), "duration").Float())
								}
							} else {
								require.Contains(t, string(ctx.Response.Body()), "invalid audio")
							}
							require.Equal(t, tc.contentType, string(ctx.Response.Header.ContentType()))
							require.Equal(t, provider, string(ctx.Response.Header.Peek(lib.HeaderBifrostProvider)))
							if tc.status == 200 {
								if mode.headers {
									require.NotEmpty(t, ctx.Response.Header.Peek(lib.HeaderBifrostLatency))
								} else {
									require.Empty(t, ctx.Response.Header.Peek(lib.HeaderBifrostLatency))
								}
							}
						})
					}
				}
			}
		})
	}
}

// Use the supported routing hook for integration fallbacks: its multipart parser
// currently forwards a "fallbacks" field as ExtraParams, not routing metadata.
type transcriptionFallbackRoutingPlugin struct{}

func (transcriptionFallbackRoutingPlugin) GetName() string { return "transcription-fallback-routing" }
func (transcriptionFallbackRoutingPlugin) Cleanup() error  { return nil }
func (transcriptionFallbackRoutingPlugin) PreRequestHook(_ *schemas.BifrostContext, req *schemas.BifrostRequest) error {
	if req.TranscriptionRequest != nil {
		req.TranscriptionRequest.Fallbacks = []schemas.Fallback{{Provider: schemas.OpenAI, Model: "whisper-1"}}
	}
	return nil
}
func (transcriptionFallbackRoutingPlugin) PreLLMHook(_ *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	return req, nil, nil
}
func (transcriptionFallbackRoutingPlugin) PostLLMHook(_ *schemas.BifrostContext, resp *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	return resp, err, nil
}

func TestTranscriptionFallbackPreservesResponseFormat(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		defer r.MultipartForm.RemoveAll()
		if r.FormValue("model") == "unavailable-transcriber" {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Mock-Stage", "primary")
			w.Header().Set("X-Mock-Primary-Only", "present")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"error":{"message":"primary unavailable","type":"server_error"}}`)
			return
		}
		if r.FormValue("model") != "whisper-1" {
			t.Errorf("unexpected fallback model: %q", r.FormValue("model"))
		}
		w.Header().Set("X-Mock-Stage", "fallback")
		switch r.FormValue("response_format") {
		case "text":
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		case "srt":
			w.Header().Set("Content-Type", "application/x-subrip; charset=utf-8")
		case "vtt":
			w.Header().Set("Content-Type", "text/vtt; charset=utf-8")
		default:
			t.Errorf("fallback lost response_format: %q", r.FormValue("response_format"))
		}
		_, _ = io.WriteString(w, "fallback body\r\n")
	}))
	defer upstream.Close()
	client, err := bifrost.Init(context.Background(), schemas.BifrostConfig{
		Account: transcriptionTestAccount{baseURL: upstream.URL}, Logger: bifrost.NewDefaultLogger(schemas.LogLevelError),
		LLMPlugins: []schemas.LLMPlugin{transcriptionFallbackRoutingPlugin{}},
	})
	require.NoError(t, err)
	defer client.Shutdown()
	r := router.New()
	config := &lib.Config{ClientConfig: new(lib.DefaultClientConfig)}
	identity := func(next fasthttp.RequestHandler) fasthttp.RequestHandler { return next }
	integrations.NewOpenAIRouter(client, config, nil, bifrost.NewDefaultLogger(schemas.LogLevelError)).RegisterRoutes(r, identity)
	NewInferenceHandler(nil, client, config).RegisterRoutes(r, identity)
	for _, format := range []string{"text", "srt", "vtt"} {
		for _, route := range []string{"/v1/audio/transcriptions", "/openai/v1/audio/transcriptions"} {
			t.Run(format+route, func(t *testing.T) {
				body, ct := transcriptionMultipartBody(t, "openai/unavailable-transcriber", format, "", "openai/whisper-1")
				var ctx fasthttp.RequestCtx
				ctx.Request.SetRequestURI(route)
				ctx.Request.Header.SetMethod(fasthttp.MethodPost)
				ctx.Request.Header.SetContentType(ct)
				ctx.Request.SetBody(body)
				defer ctx.Request.RemoveMultipartFormFiles()
				r.Handler(&ctx)
				require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), "body: %s", ctx.Response.Body())
				require.Equal(t, "fallback body\r\n", string(ctx.Response.Body()))
				wantType := map[string]string{"text": "text/plain; charset=utf-8", "srt": "application/x-subrip; charset=utf-8", "vtt": "text/vtt; charset=utf-8"}[format]
				require.Equal(t, wantType, string(ctx.Response.Header.ContentType()))
				require.Equal(t, "fallback", string(ctx.Response.Header.Peek("X-Mock-Stage")))
				require.Empty(t, ctx.Response.Header.Peek("X-Mock-Primary-Only"))
				require.Equal(t, "whisper-1", string(ctx.Response.Header.Peek(lib.HeaderBifrostResolvedModel)))
				require.Equal(t, "1", string(ctx.Response.Header.Peek(lib.HeaderBifrostFallbackIndex)))
			})
		}
	}
}

// A cached or plugin-produced response still goes through the post-hook before
// serialization. RawResponse must not override edits made to the normalized Text.
type transcriptionTestPlugin struct {
	roundTrip  bool
	postFormat *string
}

func (transcriptionTestPlugin) GetName() string { return "transcription-test" }
func (transcriptionTestPlugin) Cleanup() error  { return nil }
func (transcriptionTestPlugin) PreRequestHook(*schemas.BifrostContext, *schemas.BifrostRequest) error {
	return nil
}
func (p transcriptionTestPlugin) PreLLMHook(_ *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	response := &schemas.BifrostResponse{
		TranscriptionResponse: &schemas.BifrostTranscriptionResponse{
			Text: "original\n", ResponseFormat: req.TranscriptionRequest.Params.ResponseFormat,
			ExtraFields: schemas.BifrostResponseExtraFields{RawResponse: "original\n"},
		},
	}
	if p.roundTrip {
		// Semantic-cache hits deserialize a BifrostResponse from JSON. The
		// response's internal ResponseFormat (json:"-") does not survive.
		body, err := schemas.MarshalSorted(response)
		if err != nil {
			return req, nil, err
		}
		var cached schemas.BifrostResponse
		if err := json.Unmarshal(body, &cached); err != nil {
			return req, nil, err
		}
		response = &cached
	}
	return req, &schemas.LLMPluginShortCircuit{Response: response}, nil
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

func TestTranscriptionResponsePreservesPostHookEdits(t *testing.T) {
	t.Run("in-memory", func(t *testing.T) { testTranscriptionPostHookEdits(t, transcriptionTestPlugin{}, "text", false) })
	t.Run("JSON-cache-round-trip", func(t *testing.T) {
		testTranscriptionPostHookEdits(t, transcriptionTestPlugin{roundTrip: true}, "text", false)
	})
	for _, format := range []string{"srt", "vtt"} {
		t.Run("JSON-cache-round-trip/"+format, func(t *testing.T) {
			testTranscriptionPostHookEdits(t, transcriptionTestPlugin{roundTrip: true}, format, false)
		})
	}
	t.Run("explicit-post-hook-format", func(t *testing.T) {
		testTranscriptionPostHookEdits(t, transcriptionTestPlugin{postFormat: schemas.Ptr("json")}, "text", true)
	})
}

func testTranscriptionPostHookEdits(t *testing.T, plugin transcriptionTestPlugin, format string, wantJSON bool) {
	t.Helper()
	client, err := bifrost.Init(context.Background(), schemas.BifrostConfig{
		Account:    transcriptionTestAccount{baseURL: "http://127.0.0.1:1"},
		LLMPlugins: []schemas.LLMPlugin{plugin}, Logger: bifrost.NewDefaultLogger(schemas.LogLevelError),
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
			// This is a serialized plugin response, not a Groq subtitle API call.
			body, ct := transcriptionMultipartBody(t, "groq/whisper-large-v3-turbo", format, "")
			var ctx fasthttp.RequestCtx
			ctx.Request.SetRequestURI(route)
			ctx.Request.Header.SetMethod(fasthttp.MethodPost)
			ctx.Request.Header.SetContentType(ct)
			ctx.Request.SetBody(body)
			defer ctx.Request.RemoveMultipartFormFiles()
			r.Handler(&ctx)
			require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode())
			if wantJSON {
				require.Equal(t, "application/json", string(ctx.Response.Header.ContentType()))
				require.Equal(t, "edited\n", gjson.GetBytes(ctx.Response.Body(), "text").String())
			} else {
				require.Equal(t, "edited\n", string(ctx.Response.Body()))
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
