package integrations

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/fasthttp/router"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

const (
	elevenlabsClientVirtualKey = "sk-bf-elevenlabs-client"
	elevenlabsProviderKey      = "elevenlabs-provider-key"

	// elevenlabsTimestampsAnswer is a with-timestamps answer with a field the Bifrost types do not have.
	elevenlabsTimestampsAnswer = `{"audio_base64":"YXVkaW8=","alignment":{"characters":["h","i"],"character_start_times_seconds":[0,0.1],"character_end_times_seconds":[0.1,0.2]},"normalized_alignment":null,"a_field_added_later":{"x":1}}`
	elevenlabsModelTurbo       = `{"model_id":"eleven_v4_turbo","name":"Eleven v4 Turbo","description":"Fast","serves_pro_voices":false,"token_cost_factor":0.5,"can_be_finetuned":false,"can_do_text_to_speech":true,"can_do_voice_conversion":false,"can_use_style":false,"can_use_speaker_boost":false,"languages":[{"language_id":"en","name":"English"}],"requires_alpha_access":false,"max_characters_request_free_user":10000,"max_characters_request_subscribed_user":10000,"maximum_text_length_per_request":10000,"model_rates":{"character_cost_multiplier":0.5},"concurrency_group":"turbo"}`
	elevenlabsModelV3          = `{"model_id":"eleven_v3","name":"Eleven v3","description":"Expressive","serves_pro_voices":false,"token_cost_factor":1,"can_be_finetuned":false,"can_do_text_to_speech":true,"can_do_voice_conversion":false,"can_use_style":false,"can_use_speaker_boost":false,"languages":[{"language_id":"en","name":"English"}],"requires_alpha_access":false,"max_characters_request_free_user":5000,"max_characters_request_subscribed_user":5000,"maximum_text_length_per_request":5000,"model_rates":{"character_cost_multiplier":1},"concurrency_group":"standard"}`
	elevenlabsModelsAnswer     = "[" + elevenlabsModelTurbo + "," + elevenlabsModelV3 + "]"
)

// elevenlabsAccount configures one ElevenLabs key whose base URL is a fake upstream.
type elevenlabsAccount struct {
	baseURL string
	models  schemas.WhiteList
}

func (elevenlabsAccount) GetConfiguredProviders() ([]schemas.ModelProvider, error) {
	return []schemas.ModelProvider{schemas.Elevenlabs}, nil
}

func (a elevenlabsAccount) GetKeysForProvider(context.Context, schemas.ModelProvider) ([]schemas.Key, error) {
	return []schemas.Key{{ID: "el-key", Value: *schemas.NewSecretVar(elevenlabsProviderKey), Models: a.models, Weight: 1}}, nil
}

func (a elevenlabsAccount) GetConfigForProvider(schemas.ModelProvider) (*schemas.ProviderConfig, error) {
	return &schemas.ProviderConfig{NetworkConfig: schemas.NetworkConfig{BaseURL: a.baseURL, AllowPrivateNetwork: true}}, nil
}

// elevenlabsUpstreamRequest is what reached the fake ElevenLabs.
type elevenlabsUpstreamRequest struct {
	method, path, query, body string
	apiKeys                   []string
}

// elevenlabsObserver records what core saw of a request.
type elevenlabsObserver struct {
	mu          sync.Mutex
	types       []schemas.RequestType
	speech      []*schemas.BifrostSpeechRequest
	passthrough []*schemas.BifrostPassthroughRequest
	usage       *schemas.BifrostPassthroughUsage
	denyWith    *schemas.BifrostError
}

func (*elevenlabsObserver) GetName() string { return "elevenlabs-observer" }
func (*elevenlabsObserver) Cleanup() error  { return nil }
func (*elevenlabsObserver) PreRequestHook(*schemas.BifrostContext, *schemas.BifrostRequest) error {
	return nil
}

func (o *elevenlabsObserver) PreLLMHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.types = append(o.types, req.RequestType)
	if req.SpeechRequest != nil {
		o.speech = append(o.speech, req.SpeechRequest)
	}
	if req.PassthroughRequest != nil {
		copied := *req.PassthroughRequest
		o.passthrough = append(o.passthrough, &copied)
	}
	if o.denyWith != nil {
		return req, &schemas.LLMPluginShortCircuit{Error: o.denyWith}, nil
	}
	return req, nil, nil
}

func (o *elevenlabsObserver) PostLLMHook(_ *schemas.BifrostContext, resp *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	if resp != nil && resp.PassthroughResponse != nil && resp.PassthroughResponse.PassthroughUsage != nil {
		o.mu.Lock()
		o.usage = resp.PassthroughResponse.PassthroughUsage
		o.mu.Unlock()
	}
	return resp, err, nil
}

// elevenlabsGateway serves the ElevenLabs routers in front of a fake ElevenLabs.
type elevenlabsGateway struct {
	router   *router.Router
	observer *elevenlabsObserver
	received *[]elevenlabsUpstreamRequest
	status   *int
	failure  *string // the body of a failing answer
}

func newElevenlabsGateway(t *testing.T, models ...string) *elevenlabsGateway {
	t.Helper()
	var mu sync.Mutex
	received := []elevenlabsUpstreamRequest{}
	status := http.StatusOK
	failure := `{"detail":{"status":"invalid_api_key","message":"Invalid API key"}}`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		received = append(received, elevenlabsUpstreamRequest{r.Method, r.URL.Path, r.URL.RawQuery, string(body), r.Header.Values("xi-api-key")})
		code, failing := status, failure
		mu.Unlock()
		if code != http.StatusOK {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(code)
			_, _ = w.Write([]byte(failing))
			return
		}
		w.Header().Set("request-id", "req-123")
		w.Header().Set("history-item-id", "hist-123")
		w.Header().Set("character-cost", "22")
		switch {
		case r.URL.Path == "/v1/models":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(elevenlabsModelsAnswer))
		case r.URL.Path == "/v1/voices":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"voices":[]}`))
		case strings.HasSuffix(r.URL.Path, "/with-timestamps") && !strings.Contains(r.URL.Path, "/stream"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(elevenlabsTimestampsAnswer))
		default:
			w.Header().Set("Content-Type", "audio/mpeg")
			flusher := w.(http.Flusher)
			for _, chunk := range []string{"audio-", "bytes"} {
				_, _ = w.Write([]byte(chunk))
				flusher.Flush()
			}
		}
	}))
	t.Cleanup(upstream.Close)

	allowed := schemas.WhiteList{"*"}
	if len(models) > 0 {
		allowed = schemas.WhiteList(models)
	}
	observer := &elevenlabsObserver{}
	client, err := bifrost.Init(context.Background(), schemas.BifrostConfig{Account: elevenlabsAccount{upstream.URL, allowed}, LLMPlugins: []schemas.LLMPlugin{observer}, Logger: bifrost.NewNoOpLogger()})
	require.NoError(t, err)
	t.Cleanup(client.Shutdown)
	r := router.New()
	NewElevenlabsRouter(client, &mockHandlerStore{}, nil, &testLogger{}).RegisterRoutes(r)
	NewElevenlabsPassthroughRouter(client, &mockHandlerStore{}, nil, &testLogger{}).RegisterRoutes(r)
	return &elevenlabsGateway{router: r, observer: observer, received: &received, status: &status, failure: &failure}
}

func (g *elevenlabsGateway) do(method, uri, body string) *fasthttp.RequestCtx {
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod(method)
	ctx.Request.SetRequestURI(uri)
	ctx.Request.Header.Set("xi-api-key", elevenlabsClientVirtualKey)
	ctx.Request.Header.Set("User-Agent", "elevenlabs/2.78.0")
	if body != "" {
		ctx.Request.Header.SetContentType("application/json")
		ctx.Request.SetBodyString(body)
	}
	g.router.Handler(ctx)
	return ctx
}

func (g *elevenlabsGateway) lastUpstream(t *testing.T) elevenlabsUpstreamRequest {
	t.Helper()
	require.NotEmpty(t, *g.received)
	return (*g.received)[len(*g.received)-1]
}

// Text to speech reaches ElevenLabs as the client sent it, under the key's credential, and its answer comes back as
// ElevenLabs wrote it, while Bifrost reads the call as a typed speech request.
func TestElevenlabsRouterServesTextToSpeech(t *testing.T) {
	const body = `{"text":"hello world","model_id":"elevenlabs/eleven_v4_turbo","voice_settings":{"stability":0.4},"seed":3,"apply_text_normalization":"on"}`
	for _, tc := range []struct {
		name, path, wantBody, wantType string
		withTimestamps                 bool
	}{
		{"audio", "/v1/text-to-speech/voice123", "audio-bytes", "audio/mpeg", false},
		{"with timestamps", "/v1/text-to-speech/voice123/with-timestamps", elevenlabsTimestampsAnswer, "application/json", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gateway := newElevenlabsGateway(t)
			ctx := gateway.do(fasthttp.MethodPost, "/elevenlabs"+tc.path+"?output_format=mp3_22050_32&enable_logging=false", body)

			require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), string(ctx.Response.Body()))
			if tc.withTimestamps {
				assert.JSONEq(t, tc.wantBody, string(ctx.Response.Body()), "the answer is ElevenLabs' own, with the field the Bifrost types lack")
			} else {
				assert.Equal(t, tc.wantBody, string(ctx.Response.Body()))
			}
			assert.Contains(t, string(ctx.Response.Header.ContentType()), tc.wantType)
			assert.Equal(t, "req-123", string(ctx.Response.Header.Peek("request-id")), "ElevenLabs' own headers come back")
			assert.Equal(t, "22", string(ctx.Response.Header.Peek("character-cost")))

			upstream := gateway.lastUpstream(t)
			assert.Equal(t, tc.path, upstream.path)
			assert.Equal(t, "enable_logging=false&output_format=mp3_22050_32", upstream.query)
			assert.Equal(t, strings.Replace(body, "elevenlabs/eleven_v4_turbo", "eleven_v4_turbo", 1), upstream.body, "the body goes upstream as the client wrote it, with the model ElevenLabs names")
			assert.Equal(t, []string{elevenlabsProviderKey}, upstream.apiKeys, "the client's credential must not reach ElevenLabs")

			require.Len(t, gateway.observer.speech, 1)
			speech := gateway.observer.speech[0]
			assert.Equal(t, schemas.SpeechRequest, gateway.observer.types[0])
			assert.Equal(t, schemas.Elevenlabs, speech.Provider)
			assert.Equal(t, "eleven_v4_turbo", speech.Model)
			assert.Equal(t, "hello world", speech.Input.Input)
			require.NotNil(t, speech.Params.VoiceConfig.Voice)
			assert.Equal(t, "voice123", *speech.Params.VoiceConfig.Voice)
			assert.Equal(t, "mp3_22050_32", speech.Params.ResponseFormat)
			require.NotNil(t, speech.Params.EnableLogging)
			assert.False(t, *speech.Params.EnableLogging)
			if tc.withTimestamps {
				require.NotNil(t, speech.Params.WithTimestamps)
				assert.True(t, *speech.Params.WithTimestamps)
			} else {
				assert.Nil(t, speech.Params.WithTimestamps)
			}
		})
	}
}

// The model list is Bifrost's, less the models the key does not allow, in ElevenLabs' schema: a bare array of models
// with the id and name Bifrost has of each, not the records ElevenLabs sent.
func TestElevenlabsRouterServesTheModelList(t *testing.T) {
	t.Run("every model", func(t *testing.T) {
		gateway := newElevenlabsGateway(t)
		ctx := gateway.do(fasthttp.MethodGet, "/elevenlabs/v1/models", "")

		require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), string(ctx.Response.Body()))
		assert.JSONEq(t, `[{"model_id":"eleven_v3","name":"Eleven v3"},{"model_id":"eleven_v4_turbo","name":"Eleven v4 Turbo"}]`, string(ctx.Response.Body()))
		assert.Equal(t, "/v1/models", gateway.lastUpstream(t).path)
		assert.Equal(t, []string{elevenlabsProviderKey}, gateway.lastUpstream(t).apiKeys)
	})
	t.Run("a key that allows one", func(t *testing.T) {
		gateway := newElevenlabsGateway(t, "eleven_v3")
		ctx := gateway.do(fasthttp.MethodGet, "/elevenlabs/v1/models", "")

		require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), string(ctx.Response.Body()))
		assert.JSONEq(t, `[{"model_id":"eleven_v3","name":"Eleven v3"}]`, string(ctx.Response.Body()))
	})
}

// Only the endpoints Bifrost serves natively are on /elevenlabs, and the base URL prefix is required.
func TestElevenlabsRouterServesOnlyItsEndpoints(t *testing.T) {
	gateway := newElevenlabsGateway(t)
	for _, tc := range []struct {
		method, uri string
		want        int
	}{
		{fasthttp.MethodPost, "/elevenlabs/v1/text-to-speech/voice123", fasthttp.StatusOK},
		{fasthttp.MethodPost, "/elevenlabs/v1/text-to-speech/voice123/with-timestamps", fasthttp.StatusOK},
		{fasthttp.MethodGet, "/elevenlabs/v1/models", fasthttp.StatusOK},
		{fasthttp.MethodGet, "/elevenlabs/v1/text-to-speech/voice123", fasthttp.StatusMethodNotAllowed},
		{fasthttp.MethodPost, "/elevenlabs/v1/models", fasthttp.StatusMethodNotAllowed},
		{fasthttp.MethodGet, "/elevenlabs/v1/voices", fasthttp.StatusNotFound},
		{fasthttp.MethodPost, "/elevenlabs/v1/text-to-speech/voice123/stream", fasthttp.StatusNotFound},
		{fasthttp.MethodPost, "/elevenlabs/v1/text-to-dialogue", fasthttp.StatusNotFound},
		{fasthttp.MethodPost, "/v1/text-to-speech/voice123", fasthttp.StatusNotFound},
	} {
		t.Run(tc.method+" "+tc.uri, func(t *testing.T) {
			ctx := gateway.do(tc.method, tc.uri, `{"text":"hi","model_id":"elevenlabs/eleven_v4_turbo"}`)
			assert.Equal(t, tc.want, ctx.Response.StatusCode(), string(ctx.Response.Body()))
		})
	}
}

// A voice that tries to leave its path segment never reaches ElevenLabs.
func TestElevenlabsRouterVoiceCannotLeaveItsSegment(t *testing.T) {
	gateway := newElevenlabsGateway(t)
	for _, uri := range []string{
		"/elevenlabs/v1/text-to-speech/..%2Fmodels",
		"/elevenlabs/v1/text-to-speech/%2e%2e/models",
		"/elevenlabs/v1/text-to-speech/..%5Cmodels",
		"/elevenlabs/v1/text-to-speech/voice%3Fx=1",
	} {
		ctx := gateway.do(fasthttp.MethodPost, uri, `{"text":"hi","model_id":"elevenlabs/eleven_v4_turbo"}`)
		assert.GreaterOrEqual(t, ctx.Response.StatusCode(), 400, uri)
	}
	assert.Empty(t, *gateway.received)
}

// A failure is written the way ElevenLabs writes its own errors, so an SDK parses it.
func TestElevenlabsRouterWritesElevenlabsErrors(t *testing.T) {
	t.Run("a body that is not JSON", func(t *testing.T) {
		gateway := newElevenlabsGateway(t)
		ctx := gateway.do(fasthttp.MethodPost, "/elevenlabs/v1/text-to-speech/voice123", `{"text":`)
		require.Equal(t, fasthttp.StatusBadRequest, ctx.Response.StatusCode())
		assert.Contains(t, string(ctx.Response.Body()), `"detail":{`)
		assert.Empty(t, *gateway.received)
	})
	t.Run("a request that names no model", func(t *testing.T) {
		gateway := newElevenlabsGateway(t)
		ctx := gateway.do(fasthttp.MethodPost, "/elevenlabs/v1/text-to-speech/voice123", `{"text":"hi"}`)
		assert.GreaterOrEqual(t, ctx.Response.StatusCode(), 400)
		assert.Contains(t, string(ctx.Response.Body()), `"detail":{`)
		assert.Empty(t, *gateway.received)
	})
	t.Run("a refusal by a plugin", func(t *testing.T) {
		gateway := newElevenlabsGateway(t)
		gateway.observer.denyWith = &schemas.BifrostError{Type: schemas.Ptr("model_blocked"), StatusCode: schemas.Ptr(403), Error: &schemas.ErrorField{Message: "model is not allowed"}}
		ctx := gateway.do(fasthttp.MethodPost, "/elevenlabs/v1/text-to-speech/voice123", `{"text":"hi","model_id":"elevenlabs/eleven_v4_turbo"}`)
		require.Equal(t, fasthttp.StatusForbidden, ctx.Response.StatusCode())
		assert.JSONEq(t, `{"detail":{"status":"model_blocked","message":"model is not allowed"}}`, string(ctx.Response.Body()))
		assert.Empty(t, *gateway.received)
	})
	t.Run("a validation error of ElevenLabs names the field", func(t *testing.T) {
		gateway := newElevenlabsGateway(t)
		*gateway.status = http.StatusUnprocessableEntity
		*gateway.failure = `{"detail":[{"type":"less_than_equal","loc":["body","seed"],"msg":"Input should be less than or equal to 4294967295","input":99999999999}]}`
		ctx := gateway.do(fasthttp.MethodPost, "/elevenlabs/v1/text-to-speech/voice123", `{"text":"hi","model_id":"elevenlabs/eleven_v4_turbo","seed":99999999999}`)
		require.Equal(t, fasthttp.StatusUnprocessableEntity, ctx.Response.StatusCode())
		assert.Contains(t, string(ctx.Response.Body()), `"detail":{`)
		assert.Contains(t, string(ctx.Response.Body()), "body.seed")
	})
	t.Run("an error of ElevenLabs", func(t *testing.T) {
		gateway := newElevenlabsGateway(t)
		*gateway.status = http.StatusUnauthorized
		ctx := gateway.do(fasthttp.MethodPost, "/elevenlabs/v1/text-to-speech/voice123", `{"text":"hi","model_id":"elevenlabs/eleven_v4_turbo"}`)
		require.Equal(t, fasthttp.StatusUnauthorized, ctx.Response.StatusCode())
		assert.Contains(t, string(ctx.Response.Body()), `"detail":{`)
		assert.Contains(t, string(ctx.Response.Body()), "Invalid API key")
	})
}

// The rest of the ElevenLabs API is forwarded as sent on /elevenlabs_passthrough, under the key's credential, and
// ElevenLabs names its model model_id.
func TestElevenlabsPassthroughRouterForwardsAsSent(t *testing.T) {
	t.Run("a call that names no model", func(t *testing.T) {
		gateway := newElevenlabsGateway(t)
		ctx := gateway.do(fasthttp.MethodGet, "/elevenlabs_passthrough/v1/voices?show_legacy=true", "")

		require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), string(ctx.Response.Body()))
		assert.JSONEq(t, `{"voices":[]}`, string(ctx.Response.Body()))
		upstream := gateway.lastUpstream(t)
		assert.Equal(t, "/v1/voices", upstream.path)
		assert.Equal(t, "show_legacy=true", upstream.query)
		assert.Equal(t, []string{elevenlabsProviderKey}, upstream.apiKeys, "the client's credential must not reach ElevenLabs")
	})
	t.Run("a stream of speech", func(t *testing.T) {
		gateway := newElevenlabsGateway(t)
		ctx := gateway.do(fasthttp.MethodPost, "/elevenlabs_passthrough/v1/text-to-speech/voice123/stream/with-timestamps", `{"text":"hello world","model_id":"eleven_v4_turbo"}`)

		require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode())
		require.True(t, ctx.Response.IsBodyStream())
		audio, _ := io.ReadAll(ctx.Response.BodyStream())
		assert.Equal(t, "audio-bytes", string(audio))
		assert.Equal(t, "/v1/text-to-speech/voice123/stream/with-timestamps", gateway.lastUpstream(t).path)
		require.Len(t, gateway.observer.passthrough, 1)
		assert.Equal(t, "eleven_v4_turbo", gateway.observer.passthrough[0].Model, "the model is model_id")
		assert.Equal(t, schemas.PassthroughStreamRequest, gateway.observer.types[0])
		require.NotNil(t, gateway.observer.usage)
		assert.Equal(t, 11, gateway.observer.usage.AudioInputChars)
	})
	// ElevenLabs runs its own default for a call that sends no model_id, whatever a model field says, so the plugins
	// that check the model must not be shown that field.
	t.Run("a body model ElevenLabs does not read", func(t *testing.T) {
		for _, tc := range []struct{ name, body, wantModel string }{
			{"model only", `{"text":"hello","model":"eleven_v4_turbo"}`, ""},
			{"model_id wins", `{"text":"hello","model":"eleven_v4_turbo","model_id":"eleven_multilingual_v2"}`, "eleven_multilingual_v2"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				gateway := newElevenlabsGateway(t)
				ctx := gateway.do(fasthttp.MethodPost, "/elevenlabs_passthrough/v1/text-to-speech/voice123", tc.body)

				require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), string(ctx.Response.Body()))
				assert.Equal(t, tc.body, gateway.lastUpstream(t).body, "the body goes upstream as sent")
				require.Len(t, gateway.observer.passthrough, 1)
				assert.Equal(t, tc.wantModel, gateway.observer.passthrough[0].Model)
			})
		}
	})
}

// A call goes to ElevenLabs when its model says so, by prefix or, with none, by an ElevenLabs model name; any other
// provider's model gets the typed path, without the ElevenLabs body.
func TestIsElevenlabsCall(t *testing.T) {
	for _, tc := range []struct {
		model string
		want  bool
	}{
		{"elevenlabs/eleven_v4_turbo", true},
		{"eleven_v4_turbo", true},
		{"eleven_text_to_sound_v2", true},
		{"openai/tts-1", false},
		{"tts-1", false},
		{"", false},
	} {
		provider, model := schemas.ParseModelString(tc.model, "")
		assert.Equal(t, tc.want, isElevenlabsCall(provider, model), tc.model)
	}
}

// BenchmarkElevenlabsSpeechRequestConversion is the cost a text-to-speech call adds on the way in: parsing the body
// and converting it to the typed request.
func BenchmarkElevenlabsSpeechRequestConversion(b *testing.B) {
	body := []byte(`{"text":"Namaste, this is a sentence of the kind a voice agent speaks.","model_id":"eleven_v4_turbo","voice_settings":{"stability":0.4,"similarity_boost":0.8},"seed":3}`)
	route := elevenlabsSpeechRoute("/elevenlabs", "", false)
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	for b.Loop() {
		call := route.GetRequestTypeInstance(context.Background()).(*elevenlabsSpeechCall)
		if err := sonic.Unmarshal(body, call); err != nil {
			b.Fatal(err)
		}
		call.voice, call.outputFormat = "voice123", "mp3_44100_128"
		if _, err := route.RequestConverter(nil, call); err != nil {
			b.Fatal(err)
		}
	}
}
