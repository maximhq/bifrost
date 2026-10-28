package integrations

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/fasthttp/router"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/modelcatalog"
	"github.com/maximhq/bifrost/plugins/modelcatalogresolver"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// Live Azure AI Speech answers from an eastus2 resource, and an Azure OpenAI Whisper answer.
const (
	azureRouteFastResponse       = `{"durationMilliseconds":4262,"combinedPhrases":[{"text":"Hello from Bifrost."}],"phrases":[{"offsetMilliseconds":110,"durationMilliseconds":1280,"text":"Hello from Bifrost.","words":[{"text":"Hello","offsetMilliseconds":110,"durationMilliseconds":400}],"locale":"en-US","confidence":0.69548494}]}`
	azureRouteShortAudioResponse = `{"RecognitionStatus":"Success","Offset":700000,"Duration":28900000,"DisplayText":"The quick brown fox jumps over the lazy dog.","NBest":[{"Confidence":0.7915801,"Lexical":"the quick brown fox jumps over the lazy dog","ITN":"the quick brown fox jumps over the lazy dog","MaskedITN":"the quick brown fox jumps over the lazy dog","Display":"The quick brown fox jumps over the lazy dog."}]}`
	azureRouteWhisperResponse    = `{"text":"Hello from Whisper.","language":"english","duration":2.5,"segments":[{"id":0,"start":0.0,"end":2.5,"text":"Hello from Whisper."}]}`
)

// azureRouteUpstream is a fake Azure AI Services resource that records the last request.
type azureRouteUpstream struct {
	mu      sync.Mutex
	path    string
	query   string
	headers http.Header
	body    []byte
}

func (u *azureRouteUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	u.mu.Lock()
	u.path, u.query, u.headers, u.body = r.URL.Path, r.URL.RawQuery, r.Header.Clone(), body
	u.mu.Unlock()
	switch {
	case r.URL.Path == "/tts/cognitiveservices/v1":
		w.Header().Set("Content-Type", "audio/x-wav")
		_, _ = w.Write([]byte("RIFF-wav-audio"))
	case r.URL.Path == "/speechtotext/transcriptions:transcribe":
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, azureRouteFastResponse)
	case r.URL.Path == "/stt/speech/recognition/dictation/cognitiveservices/v1":
		if r.URL.Query().Get("language") == "xx-YY" {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, "Invalid 'language' query parameter or unknown custom deployment.")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, azureRouteShortAudioResponse)
	case strings.HasSuffix(r.URL.Path, "/audio/transcriptions"):
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, azureRouteWhisperResponse)
	case strings.HasSuffix(r.URL.Path, "/audio/speech"):
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write([]byte("ID3-openai-audio"))
	case r.URL.Path == "/openai/v1/files":
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"object":"list","data":[],"has_more":false}`)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (u *azureRouteUpstream) last() (string, string, http.Header, []byte) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.path, u.query, u.headers, u.body
}

// azureRouteTestAccount serves one Azure key at the fake resource; speech sets use_speech_endpoints.
type azureRouteTestAccount struct {
	baseURL string
	speech  bool
}

func (a azureRouteTestAccount) GetConfiguredProviders() ([]schemas.ModelProvider, error) {
	return []schemas.ModelProvider{schemas.Azure}, nil
}

func (a azureRouteTestAccount) GetKeysForProvider(_ context.Context, p schemas.ModelProvider) ([]schemas.Key, error) {
	if p != schemas.Azure {
		return nil, nil
	}
	return []schemas.Key{{
		ID:             "azure-route-test-key",
		Value:          *schemas.NewSecretVar("azure-resource-key"),
		Models:         schemas.WhiteList{"*"},
		Weight:         1.0,
		AzureKeyConfig: &schemas.AzureKeyConfig{Endpoint: *schemas.NewSecretVar(a.baseURL), UseSpeechEndpoints: a.speech},
	}}, nil
}

func (a azureRouteTestAccount) GetConfigForProvider(p schemas.ModelProvider) (*schemas.ProviderConfig, error) {
	if p != schemas.Azure {
		return nil, fmt.Errorf("unsupported provider %s", p)
	}
	return &schemas.ProviderConfig{
		NetworkConfig:            schemas.DefaultNetworkConfig,
		ConcurrencyAndBufferSize: schemas.DefaultConcurrencyAndBufferSize,
	}, nil
}

func newAzureRouteTestRouter(t *testing.T, account azureRouteTestAccount, plugins ...schemas.LLMPlugin) *router.Router {
	t.Helper()
	client, err := bifrost.Init(context.Background(), schemas.BifrostConfig{
		Account:    account,
		Logger:     bifrost.NewDefaultLogger(schemas.LogLevelError),
		LLMPlugins: plugins,
	})
	require.NoError(t, err)
	t.Cleanup(client.Shutdown)
	r := router.New()
	identity := func(next fasthttp.RequestHandler) fasthttp.RequestHandler { return next }
	NewAzureRouter(client, &mockHandlerStore{}, nil, &testLogger{}).RegisterRoutes(r, identity)
	return r
}

// azureRouteResolver is the model catalog resolver with the Azure model IDs the /azure routes ask for in
// Azure's list, as aliases named after them on an Azure key put them there.
func azureRouteResolver(t *testing.T) schemas.LLMPlugin {
	t.Helper()
	catalog := modelcatalog.NewTestCatalog(nil)
	catalog.UpsertLive(schemas.Azure, "azure-route-test-key", false, []string{"neural", "DragonHDLatestNeural", "fast-transcription", "short-audio"})
	resolver, err := modelcatalogresolver.Init(catalog, bifrost.NewNoOpLogger())
	require.NoError(t, err)
	return resolver
}

// azureFastTranscriptionBody builds the multipart body an Azure fast transcription client sends.
func azureFastTranscriptionBody(t *testing.T, definition string) (string, []byte) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("audio", "call.wav")
	require.NoError(t, err)
	_, _ = part.Write([]byte("RIFF-call-audio"))
	require.NoError(t, writer.WriteField("definition", definition))
	require.NoError(t, writer.Close())
	return writer.FormDataContentType(), body.Bytes()
}

// TestAzureSpeechRoutes pins the /azure Azure AI Speech REST routes end to end on a key with
// use_speech_endpoints: the request reaches Azure in its own shape with the resource key in place of
// the virtual key, and Azure's answer comes back unchanged.
func TestAzureSpeechRoutes(t *testing.T) {
	upstream := &azureRouteUpstream{}
	server := httptest.NewServer(upstream)
	defer server.Close()
	r := newAzureRouteTestRouter(t, azureRouteTestAccount{baseURL: server.URL, speech: true}, azureRouteResolver(t))

	for _, uri := range []string{"/azure/cognitiveservices/v1", "/azure/tts/cognitiveservices/v1"} {
		t.Run("text to speech "+uri, func(t *testing.T) {
			ssml := `<speak version="1.0" xml:lang="en-US"><voice name="en-US-Ava:DragonHDLatestNeural">Hello &amp; welcome.</voice></speak>`
			var ctx fasthttp.RequestCtx
			ctx.Request.Header.SetMethod(fasthttp.MethodPost)
			ctx.Request.SetRequestURI(uri)
			ctx.Request.Header.SetContentType("application/ssml+xml")
			ctx.Request.Header.Set("X-Microsoft-OutputFormat", "riff-24khz-16bit-mono-pcm")
			ctx.Request.Header.Set("Ocp-Apim-Subscription-Key", "sk-bf-route-test")
			ctx.Request.SetBodyString(ssml)

			r.Handler(&ctx)

			require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), "body: %s", ctx.Response.Body())
			require.Equal(t, "audio/x-wav", string(ctx.Response.Header.ContentType()))
			require.Equal(t, "RIFF-wav-audio", string(ctx.Response.Body()))
			path, _, headers, body := upstream.last()
			require.Equal(t, "/tts/cognitiveservices/v1", path)
			require.Equal(t, ssml, string(body))
			require.Equal(t, "riff-24khz-16bit-mono-pcm", headers.Get("X-Microsoft-OutputFormat"))
			require.Equal(t, "azure-resource-key", headers.Get("Ocp-Apim-Subscription-Key"))
		})
	}

	t.Run("fast transcription", func(t *testing.T) {
		definition := `{"locales":["en-US","de-DE"],"diarization":{"enabled":true,"maxSpeakers":2},"phraseList":{"phrases":["Bifrost"]}}`
		contentType, body := azureFastTranscriptionBody(t, definition)
		var ctx fasthttp.RequestCtx
		ctx.Request.Header.SetMethod(fasthttp.MethodPost)
		ctx.Request.SetRequestURI("/azure/speechtotext/transcriptions:transcribe?api-version=2024-11-15")
		ctx.Request.Header.SetContentType(contentType)
		ctx.Request.SetBody(body)

		r.Handler(&ctx)

		require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), "body: %s", ctx.Response.Body())
		require.JSONEq(t, azureRouteFastResponse, string(ctx.Response.Body()))
		path, _, headers, sent := upstream.last()
		require.Equal(t, "/speechtotext/transcriptions:transcribe", path)
		_, params, err := mime.ParseMediaType(headers.Get("Content-Type"))
		require.NoError(t, err)
		form, err := multipart.NewReader(bytes.NewReader(sent), params["boundary"]).ReadForm(1 << 20)
		require.NoError(t, err)
		require.JSONEq(t, definition, form.Value["definition"][0])
	})

	t.Run("short audio", func(t *testing.T) {
		var ctx fasthttp.RequestCtx
		ctx.Request.Header.SetMethod(fasthttp.MethodPost)
		ctx.Request.SetRequestURI("/azure/stt/speech/recognition/dictation/cognitiveservices/v1?language=en-US&format=detailed")
		ctx.Request.Header.SetContentType("audio/wav; codecs=audio/pcm; samplerate=16000")
		ctx.Request.SetBodyString("RIFF-short-audio")

		r.Handler(&ctx)

		require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), "body: %s", ctx.Response.Body())
		require.JSONEq(t, azureRouteShortAudioResponse, string(ctx.Response.Body()))
		path, query, _, sent := upstream.last()
		require.Equal(t, "/stt/speech/recognition/dictation/cognitiveservices/v1", path)
		require.Equal(t, "format=detailed&language=en-US", query)
		require.Equal(t, "RIFF-short-audio", string(sent))
	})

	t.Run("short audio error", func(t *testing.T) {
		var ctx fasthttp.RequestCtx
		ctx.Request.Header.SetMethod(fasthttp.MethodPost)
		ctx.Request.SetRequestURI("/azure/speech/recognition/dictation/cognitiveservices/v1?language=xx-YY")
		ctx.Request.SetBodyString("RIFF-short-audio")

		r.Handler(&ctx)

		require.Equal(t, fasthttp.StatusBadRequest, ctx.Response.StatusCode())
		var got map[string]interface{}
		require.NoError(t, json.Unmarshal(ctx.Response.Body(), &got), "body: %s", ctx.Response.Body())
		require.Contains(t, got["message"], "Invalid 'language' query parameter")
	})
}

// TestAzureSpeechRoutesFollowTheKey pins that the key, not the route, picks the Azure surface: without
// use_speech_endpoints a fast transcription call goes to the Azure OpenAI deployment of that name, and
// its answer is rendered in the fast transcription shape.
func TestAzureSpeechRoutesFollowTheKey(t *testing.T) {
	upstream := &azureRouteUpstream{}
	server := httptest.NewServer(upstream)
	defer server.Close()
	r := newAzureRouteTestRouter(t, azureRouteTestAccount{baseURL: server.URL}, azureRouteResolver(t))

	contentType, body := azureFastTranscriptionBody(t, `{"locales":["en-US"]}`)
	var ctx fasthttp.RequestCtx
	ctx.Request.Header.SetMethod(fasthttp.MethodPost)
	ctx.Request.SetRequestURI("/azure/speechtotext/transcriptions:transcribe")
	ctx.Request.Header.SetContentType(contentType)
	ctx.Request.SetBody(body)

	r.Handler(&ctx)

	require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), "body: %s", ctx.Response.Body())
	path, _, _, _ := upstream.last()
	require.Equal(t, "/openai/deployments/fast-transcription/audio/transcriptions", path)
	var got struct {
		DurationMilliseconds int64 `json:"durationMilliseconds"`
		CombinedPhrases      []struct {
			Text string `json:"text"`
		} `json:"combinedPhrases"`
		Phrases []struct {
			Text string `json:"text"`
		} `json:"phrases"`
	}
	require.NoError(t, json.Unmarshal(ctx.Response.Body(), &got), "body: %s", ctx.Response.Body())
	require.Equal(t, int64(2500), got.DurationMilliseconds)
	require.Equal(t, "Hello from Whisper.", got.CombinedPhrases[0].Text)
	require.Len(t, got.Phrases, 1)
}

// TestAzureSpeechRoutesLeaveTheProviderToRouting pins that the /azure routes name a model but no provider:
// with no routing plugin to resolve one, the request is refused instead of reaching Azure.
func TestAzureSpeechRoutesLeaveTheProviderToRouting(t *testing.T) {
	upstream := &azureRouteUpstream{}
	server := httptest.NewServer(upstream)
	defer server.Close()
	r := newAzureRouteTestRouter(t, azureRouteTestAccount{baseURL: server.URL, speech: true})

	var ctx fasthttp.RequestCtx
	ctx.Request.Header.SetMethod(fasthttp.MethodPost)
	ctx.Request.SetRequestURI("/azure/cognitiveservices/v1")
	ctx.Request.Header.SetContentType("application/ssml+xml")
	ctx.Request.SetBodyString(`<speak version="1.0" xml:lang="en-US"><voice name="en-US-AvaMultilingualNeural">Hi.</voice></speak>`)

	r.Handler(&ctx)

	require.NotEqual(t, fasthttp.StatusOK, ctx.Response.StatusCode(), "a request with no provider was served: %s", ctx.Response.Body())
	path, _, _, _ := upstream.last()
	require.Empty(t, path, "the request reached Azure without routing picking a provider")
}

// TestAzureOpenAIRoutesOnResourceBaseURL pins that /azure also serves the Azure OpenAI paths the
// AzureOpenAI SDK builds from a resource endpoint, and only those.
func TestAzureOpenAIRoutesOnResourceBaseURL(t *testing.T) {
	paths := map[string]bool{}
	for _, route := range createAzureOpenAIRouteConfigs("/azure", &mockHandlerStore{}) {
		require.True(t, strings.HasPrefix(route.Path, "/azure/openai/"), "route %s is not an Azure resource path", route.Path)
		paths[route.Path] = true
	}
	for _, want := range []string{"/azure/openai/deployments/{deploymentPath:*}", "/azure/openai/files", "/azure/openai/batches", "/azure/openai/models", "/azure/openai/responses"} {
		require.True(t, paths[want], "missing %s", want)
	}

	upstream := &azureRouteUpstream{}
	server := httptest.NewServer(upstream)
	defer server.Close()
	r := newAzureRouteTestRouter(t, azureRouteTestAccount{baseURL: server.URL}, azureRouteResolver(t))

	var ctx fasthttp.RequestCtx
	ctx.Request.Header.SetMethod(fasthttp.MethodPost)
	ctx.Request.SetRequestURI("/azure/openai/deployments/gpt-4o-mini-tts/audio/speech?api-version=2025-04-01-preview")
	ctx.Request.Header.SetContentType("application/json")
	ctx.Request.SetBodyString(`{"model":"gpt-4o-mini-tts","input":"Hello.","voice":"alloy"}`)

	r.Handler(&ctx)

	require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), "body: %s", ctx.Response.Body())
	require.Equal(t, "ID3-openai-audio", string(ctx.Response.Body()))
	path, _, _, _ := upstream.last()
	require.Equal(t, "/openai/v1/audio/speech", path)
}

// TestAzureOpenAIMountIsAnAzureRequest pins that a request on /azure/openai/ counts as an Azure client
// whatever its User-Agent: a files call with no provider reaches Azure, the responses route hints Azure
// to the model catalog, and the same call on the OpenAI integration still defaults to OpenAI.
func TestAzureOpenAIMountIsAnAzureRequest(t *testing.T) {
	upstream := &azureRouteUpstream{}
	server := httptest.NewServer(upstream)
	defer server.Close()
	r := newAzureRouteTestRouter(t, azureRouteTestAccount{baseURL: server.URL})

	var ctx fasthttp.RequestCtx
	ctx.Request.Header.SetMethod(fasthttp.MethodGet)
	ctx.Request.SetRequestURI("/azure/openai/files?api-version=2025-04-01-preview")

	r.Handler(&ctx)

	require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), "body: %s", ctx.Response.Body())
	require.Equal(t, "azure", string(ctx.Response.Header.Peek("x-bifrost-provider")))
	path, _, _, _ := upstream.last()
	require.Equal(t, "/openai/v1/files", path)

	hinted := false
	for _, route := range createAzureOpenAIRouteConfigs("/azure", &mockHandlerStore{}) {
		if route.Path != "/azure/openai/responses" || route.Method != fasthttp.MethodPost {
			continue
		}
		var responsesCtx fasthttp.RequestCtx
		responsesCtx.Request.SetRequestURI(route.Path)
		bifrostCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		require.NoError(t, route.PreCallback(&responsesCtx, bifrostCtx, nil))
		require.Equal(t, true, bifrostCtx.Value(schemas.BifrostContextKeyIsAzureUserAgent))
		hinted = true
	}
	require.True(t, hinted, "no /azure/openai/responses route")

	var openAICtx fasthttp.RequestCtx
	openAICtx.Request.SetRequestURI("/openai/v1/files")
	require.False(t, isAzureSDKRequest(&openAICtx))
}

// TestAzureSpeechRawResponsePreCallbackKeepsRawRequestGated pins that forcing the raw response on the
// Azure Speech routes does not also honor a client's x-bf-send-back-raw-request the admin has not allowed.
func TestAzureSpeechRawResponsePreCallbackKeepsRawRequestGated(t *testing.T) {
	bifrostCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	bifrostCtx.SetValue(schemas.BifrostContextKeyAllowPerRequestRawOverride, false)
	bifrostCtx.SetValue(schemas.BifrostContextKeySendBackRawRequest, true)

	require.NoError(t, azureSpeechRawResponsePreCallback(nil, bifrostCtx, nil))

	require.Nil(t, bifrostCtx.Value(schemas.BifrostContextKeySendBackRawRequest), "the client's raw request override survived")
	require.Equal(t, true, bifrostCtx.Value(schemas.BifrostContextKeySendBackRawResponse))
}
