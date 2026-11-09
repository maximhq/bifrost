package vertex

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/valyala/fasthttp"
	"golang.org/x/oauth2"
)

const chirpVoice = "hi-IN-Chirp3-HD-Erinome"

func speechRequest(input string, params *schemas.SpeechParameters) *schemas.BifrostSpeechRequest {
	return &schemas.BifrostSpeechRequest{Provider: schemas.Vertex, Model: "chirp-3-hd", Input: &schemas.SpeechInput{Input: input}, Params: params}
}

func speechParams(voice string) *schemas.SpeechParameters {
	return &schemas.SpeechParameters{VoiceConfig: &schemas.SpeechVoiceInput{Voice: &voice}}
}

func marshalSpeech(t *testing.T, request *VertexSpeechRequest) string {
	t.Helper()
	body, err := json.Marshal(request)
	require.NoError(t, err)
	return string(body)
}

func TestToVertexSpeechRequest_ChirpVoiceDerivesLanguage(t *testing.T) {
	request, err := ToVertexSpeechRequest(speechRequest("नमस्ते", speechParams(chirpVoice)))
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"input":{"text":"नमस्ते"},
		"voice":{"languageCode":"hi-IN","name":"hi-IN-Chirp3-HD-Erinome"},
		"audioConfig":{"audioEncoding":"MP3"}
	}`, marshalSpeech(t, request))
}

func TestToVertexSpeechRequest_LanguageCodeWinsOverVoiceLocale(t *testing.T) {
	params := speechParams(chirpVoice)
	language := "en-IN"
	params.LanguageCode = &language
	request, err := ToVertexSpeechRequest(speechRequest("hello", params))
	require.NoError(t, err)
	assert.Equal(t, "en-IN", request.Voice.LanguageCode)
}

func TestToVertexSpeechRequest_VoiceIsOptionalWithLanguageCode(t *testing.T) {
	language := "hi-IN"
	request, err := ToVertexSpeechRequest(speechRequest("hello", &schemas.SpeechParameters{LanguageCode: &language}))
	require.NoError(t, err)
	assert.JSONEq(t, `{"input":{"text":"hello"},"voice":{"languageCode":"hi-IN"},"audioConfig":{"audioEncoding":"MP3"}}`, marshalSpeech(t, request))
}

func TestToVertexSpeechRequest_SSMLInputIsSentAsSSML(t *testing.T) {
	request, err := ToVertexSpeechRequest(speechRequest("  <speak>नमस्ते <break time=\"300ms\"/> दुनिया</speak>", speechParams(chirpVoice)))
	require.NoError(t, err)
	assert.Empty(t, request.Input.Text)
	assert.Contains(t, request.Input.SSML, "<speak>")
}

func TestToVertexSpeechRequest_ResponseFormats(t *testing.T) {
	for format, encoding := range map[string]string{"": "MP3", "mp3": "MP3", "wav": "LINEAR16", "WAV": "LINEAR16", "opus": "OGG_OPUS"} {
		params := speechParams(chirpVoice)
		params.ResponseFormat = format
		request, err := ToVertexSpeechRequest(speechRequest("hello", params))
		require.NoError(t, err, format)
		assert.Equal(t, encoding, request.AudioConfig.AudioEncoding, "response_format %q", format)
	}
}

func TestToVertexSpeechRequest_UnsupportedFormatsAreRefused(t *testing.T) {
	for _, format := range []string{"flac", "aac", "pcm"} {
		params := speechParams(chirpVoice)
		params.ResponseFormat = format
		_, err := ToVertexSpeechRequest(speechRequest("hello", params))
		require.Error(t, err, format)
		assert.True(t, providerUtils.IsInvalidRequestError(err), "response_format %q must be a caller error", format)
	}
}

func TestToVertexSpeechRequest_SpeedAndExtraParams(t *testing.T) {
	params := speechParams(chirpVoice)
	speed := 1.25
	params.Speed = &speed
	params.ExtraParams = map[string]interface{}{
		"pitch":              2.0,
		"volume_gain_db":     -3.0,
		"sample_rate_hertz":  16000.0,
		"effects_profile_id": []interface{}{"headphone-class-device"},
		"unrelated":          "kept",
	}
	request, err := ToVertexSpeechRequest(speechRequest("hello", params))
	require.NoError(t, err)

	audio := request.AudioConfig
	require.NotNil(t, audio.SpeakingRate)
	assert.Equal(t, 1.25, *audio.SpeakingRate)
	require.NotNil(t, audio.Pitch)
	assert.Equal(t, 2.0, *audio.Pitch)
	require.NotNil(t, audio.VolumeGainDb)
	assert.Equal(t, -3.0, *audio.VolumeGainDb)
	require.NotNil(t, audio.SampleRateHertz)
	assert.Equal(t, 16000, *audio.SampleRateHertz)
	assert.Equal(t, []string{"headphone-class-device"}, audio.EffectsProfileID)

	// Only what is not mapped stays in the extra params, and the caller's map is left whole for a fallback provider.
	assert.Equal(t, map[string]interface{}{"unrelated": "kept"}, request.ExtraParams)
	assert.Len(t, params.ExtraParams, 5)
}

func TestToVertexSpeechRequest_RefusesWrongTypedExtraParams(t *testing.T) {
	for key, value := range map[string]interface{}{
		"pitch":              "high",
		"volume_gain_db":     "loud",
		"sample_rate_hertz":  "16k",
		"effects_profile_id": "headphone-class-device",
	} {
		params := speechParams(chirpVoice)
		params.ExtraParams = map[string]interface{}{key: value}
		_, err := ToVertexSpeechRequest(speechRequest("hello", params))
		require.Error(t, err, key)
		assert.True(t, providerUtils.IsInvalidRequestError(err), "%s must be a caller error", key)
	}
}

func TestToVertexSpeechRequest_RefusesRequestsItCannotServe(t *testing.T) {
	multi := &schemas.SpeechParameters{VoiceConfig: &schemas.SpeechVoiceInput{MultiVoiceConfig: []schemas.VoiceConfig{{Speaker: "a", Voice: chirpVoice}}}}
	cases := map[string]*schemas.BifrostSpeechRequest{
		"a voice with no locale and no language_code": speechRequest("hello", speechParams("alloy")),
		"no voice and no language_code":               speechRequest("hello", nil),
		"a multi-speaker voice":                       speechRequest("hello", multi),
		"empty input":                                 speechRequest("", speechParams(chirpVoice)),
	}
	for name, request := range cases {
		_, err := ToVertexSpeechRequest(request)
		require.Error(t, err, name)
		assert.True(t, providerUtils.IsInvalidRequestError(err), "%s must be a caller error", name)
	}
}

// newSpeechTestProvider returns a provider whose texttospeech.googleapis.com requests reach handler, and a key
// whose token source is a fixed token, so the call runs without Google.
func newSpeechTestProvider(t *testing.T, handler http.HandlerFunc) (*VertexProvider, schemas.Key) {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)

	config := &schemas.ProviderConfig{NetworkConfig: schemas.NetworkConfig{DefaultRequestTimeoutInSeconds: 10}}
	config.CheckAndSetDefaults()
	provider, err := NewVertexProvider(config, nil)
	require.NoError(t, err)
	dial := func(string) (net.Conn, error) { return net.Dial("tcp", server.Listener.Addr().String()) }
	provider.client = &fasthttp.Client{Dial: dial, TLSConfig: &tls.Config{InsecureSkipVerify: true}}
	provider.streamingClient = &fasthttp.Client{Dial: dial, TLSConfig: &tls.Config{InsecureSkipVerify: true}}

	key := schemas.Key{VertexKeyConfig: &schemas.VertexKeyConfig{
		ProjectID:       *schemas.NewSecretVar("test-project"),
		Region:          *schemas.NewSecretVar("us-central1"),
		AuthCredentials: *schemas.NewSecretVar(`{"type":"service_account"}`),
	}}
	provider.tokenSources.Store(getClientKey(vertexCredentialIdentity(key)), oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "test-token"}))
	return provider, key
}

func TestVertexSpeechSynthesizesThroughTextToSpeech(t *testing.T) {
	var body map[string]interface{}
	provider, key := newSpeechTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/v1/text:synthesize", r.URL.Path)
		assert.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))
		assert.Equal(t, "test-project", r.Header.Get("X-Goog-User-Project"))
		raw, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(raw, &body))
		_, _ = w.Write([]byte(`{"audioContent":"` + base64.StdEncoding.EncodeToString([]byte("ID3-audio")) + `"}`))
	})

	params := speechParams(chirpVoice)
	speed := 1.25
	params.Speed = &speed
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	response, bifrostErr := provider.Speech(ctx, key, speechRequest("नमस्ते", params))
	require.Nil(t, bifrostErr)

	assert.Equal(t, []byte("ID3-audio"), response.Audio)
	assert.JSONEq(t, `{
		"input":{"text":"नमस्ते"},
		"voice":{"languageCode":"hi-IN","name":"hi-IN-Chirp3-HD-Erinome"},
		"audioConfig":{"audioEncoding":"MP3","speakingRate":1.25}
	}`, mustJSON(t, body))
}

func TestVertexSpeechSurfacesGoogleErrors(t *testing.T) {
	provider, key := newSpeechTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":400,"message":"This voice does not support pitch parameters at this time.","status":"INVALID_ARGUMENT"}}`))
	})
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	response, bifrostErr := provider.Speech(ctx, key, speechRequest("hello", speechParams(chirpVoice)))
	require.Nil(t, response)
	require.NotNil(t, bifrostErr)
	require.NotNil(t, bifrostErr.StatusCode)
	assert.Equal(t, http.StatusBadRequest, *bifrostErr.StatusCode)
	assert.Contains(t, bifrostErr.GetErrorString(), "does not support pitch parameters")
}

func TestVertexSpeechDropsTheCachedTokenSourceOnAuthFailure(t *testing.T) {
	provider, key := newSpeechTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"code":403,"message":"caller does not have permission","status":"PERMISSION_DENIED"}}`))
	})
	clientKey := getClientKey(vertexCredentialIdentity(key))
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	_, bifrostErr := provider.Speech(ctx, key, speechRequest("hello", speechParams(chirpVoice)))
	require.NotNil(t, bifrostErr)
	_, cached := provider.tokenSources.Load(clientKey)
	assert.False(t, cached, "a 403 must evict the token source so the next request re-reads the credentials")
}

const geminiTTSModel = "gemini-2.5-flash-tts"

func geminiSpeechRequest(input string, params *schemas.SpeechParameters) *schemas.BifrostSpeechRequest {
	request := speechRequest(input, params)
	request.Model = geminiTTSModel
	return request
}

// geminiAudioEvent is one generateContent body (or SSE event) carrying PCM audio.
func geminiAudioEvent(pcm []byte, tail string) string {
	return `{"candidates":[{"content":{"role":"model","parts":[{"inlineData":{"mimeType":"audio/L16;codec=pcm;rate=24000","data":"` +
		base64.StdEncoding.EncodeToString(pcm) + `"}}]}}]` + tail + `}`
}

const geminiTTSUsage = `,"usageMetadata":{"promptTokenCount":9,"candidatesTokenCount":73,"totalTokenCount":82,` +
	`"promptTokensDetails":[{"modality":"TEXT","tokenCount":9}],"candidatesTokensDetails":[{"modality":"AUDIO","tokenCount":73}]}`

func TestVertexGeminiSpeechUsesGenerateContentAndReportsTokens(t *testing.T) {
	pcm := []byte("0123456789abcdef")
	var body []byte
	provider, key := newSpeechTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/v1/projects/test-project/locations/us-central1/publishers/google/models/"+geminiTTSModel+":generateContent", r.URL.Path)
		assert.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))
		var err error
		body, err = io.ReadAll(r.Body)
		require.NoError(t, err)
		_, _ = w.Write([]byte(geminiAudioEvent(pcm, geminiTTSUsage)))
	})

	params := speechParams("Kore")
	params.ResponseFormat = "wav"
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	response, bifrostErr := provider.Speech(ctx, key, geminiSpeechRequest("hello", params))
	require.Nil(t, bifrostErr)

	assert.Equal(t, "hello", gjson.GetBytes(body, "contents.0.parts.0.text").String())
	assert.Equal(t, "user", gjson.GetBytes(body, "contents.0.role").String(), "Vertex rejects contents without a role")
	assert.Equal(t, "AUDIO", gjson.GetBytes(body, "generationConfig.responseModalities.0").String())
	assert.Equal(t, "Kore", gjson.GetBytes(body, "generationConfig.speechConfig.voice_config.prebuiltVoiceConfig.voiceName").String())
	assert.Equal(t, "RIFF", string(response.Audio[:4]), "wav is the PCM wrapped in a header")
	assert.Equal(t, 44+len(pcm), len(response.Audio))
	require.NotNil(t, response.Usage)
	assert.Equal(t, 9, response.Usage.InputTokens)
	assert.Equal(t, 73, response.Usage.OutputTokens)
	assert.Equal(t, 82, response.Usage.TotalTokens)
}

func TestVertexGeminiSpeechReturnsRawPCMWithoutAFormat(t *testing.T) {
	pcm := []byte("0123456789abcdef")
	provider, key := newSpeechTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(geminiAudioEvent(pcm, "")))
	})
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	response, bifrostErr := provider.Speech(ctx, key, geminiSpeechRequest("hello", nil))
	require.Nil(t, bifrostErr)
	assert.Equal(t, pcm, response.Audio)
}

func TestVertexGeminiSpeechAuthenticatesWithAnAPIKeyWhenTheKeyHasOne(t *testing.T) {
	provider, key := newSpeechTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "api-key", r.URL.Query().Get("key"))
		assert.Empty(t, r.Header.Get("Authorization"))
		_, _ = w.Write([]byte(geminiAudioEvent([]byte("pcm"), "")))
	})
	key.Value = *schemas.NewSecretVar("api-key")
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	_, bifrostErr := provider.Speech(ctx, key, geminiSpeechRequest("hello", speechParams("Kore")))
	require.Nil(t, bifrostErr)
}

func TestVertexGeminiSpeechSurfacesGoogleErrors(t *testing.T) {
	provider, key := newSpeechTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"code":429,"message":"Quota exceeded for this model.","status":"RESOURCE_EXHAUSTED"}}`))
	})
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	response, bifrostErr := provider.Speech(ctx, key, geminiSpeechRequest("hello", speechParams("Kore")))
	require.Nil(t, response)
	require.NotNil(t, bifrostErr)
	require.NotNil(t, bifrostErr.StatusCode)
	assert.Equal(t, http.StatusTooManyRequests, *bifrostErr.StatusCode)
	assert.Contains(t, bifrostErr.GetErrorString(), "Quota exceeded")
}

func TestVertexSpeechStreamsGeminiTTSAndReportsTokensOnDone(t *testing.T) {
	first, second := []byte("first-chunk-pcm."), []byte("second-chunk-pcm")
	provider, key := newSpeechTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/projects/test-project/locations/us-central1/publishers/google/models/"+geminiTTSModel+":streamGenerateContent", r.URL.Path)
		assert.Equal(t, "sse", r.URL.Query().Get("alt"))
		assert.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "text/event-stream")
		// Vertex repeats an empty usageMetadata on audio events and sends the counts with the finishing event.
		_, _ = w.Write([]byte("data: " + geminiAudioEvent(first, `,"usageMetadata":{"trafficType":"ON_DEMAND"}`) + "\n\n"))
		_, _ = w.Write([]byte("data: " + geminiAudioEvent(second, `,"usageMetadata":{"trafficType":"ON_DEMAND"}`) + "\n\n"))
		_, _ = w.Write([]byte(`data: {"candidates":[{"content":{"role":"model","parts":[{"text":""}]},"finishReason":"STOP"}]` + geminiTTSUsage + "}\n\n"))
	})

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	passThrough := func(_ *schemas.BifrostContext, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
		return result, err
	}
	stream, bifrostErr := provider.SpeechStream(ctx, passThrough, func(context.Context) {}, key, geminiSpeechRequest("hello", speechParams("Kore")))
	require.Nil(t, bifrostErr)

	var audio [][]byte
	var done *schemas.BifrostSpeechStreamResponse
	timeout := time.After(10 * time.Second)
	for open := true; open; {
		select {
		case chunk, ok := <-stream:
			if !ok {
				open = false
				break
			}
			require.NotNil(t, chunk.BifrostSpeechStreamResponse, "unexpected chunk: %+v", chunk)
			switch chunk.BifrostSpeechStreamResponse.Type {
			case schemas.SpeechStreamResponseTypeDelta:
				audio = append(audio, chunk.BifrostSpeechStreamResponse.Audio)
			case schemas.SpeechStreamResponseTypeDone:
				done = chunk.BifrostSpeechStreamResponse
			}
		case <-timeout:
			t.Fatal("speech stream did not finish")
		}
	}

	assert.Equal(t, [][]byte{first, second}, audio)
	require.NotNil(t, done, "the stream must end with a done chunk")
	require.NotNil(t, done.Usage)
	assert.Equal(t, 9, done.Usage.InputTokens)
	assert.Equal(t, 73, done.Usage.OutputTokens)
	assert.Equal(t, 82, done.Usage.TotalTokens)
}

func TestVertexSpeechStreamIsUnsupportedForCloudTextToSpeech(t *testing.T) {
	provider, key := newSpeechTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("Cloud Text-to-Speech streams over gRPC only, so the request must not be sent")
	})
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	stream, bifrostErr := provider.SpeechStream(ctx, nil, func(context.Context) {}, key, speechRequest("hello", speechParams(chirpVoice)))
	require.Nil(t, stream)
	require.NotNil(t, bifrostErr)
	assert.Contains(t, bifrostErr.GetErrorString(), "not supported")
}

func TestVertexSpeechNeedsGoogleCredentials(t *testing.T) {
	provider, _ := newSpeechTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("a key without Google credentials must not reach Cloud Text-to-Speech")
	})
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	_, bifrostErr := provider.Speech(ctx, schemas.Key{Value: *schemas.NewSecretVar("api-key")}, speechRequest("hello", speechParams(chirpVoice)))
	require.NotNil(t, bifrostErr)
	assert.Contains(t, bifrostErr.GetErrorString(), "does not accept API keys")
}

func mustJSON(t *testing.T, value interface{}) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	require.NoError(t, err)
	return string(encoded)
}

func BenchmarkToVertexSpeechRequest(b *testing.B) {
	params := speechParams(chirpVoice)
	speed := 1.25
	params.Speed = &speed
	params.ResponseFormat = "wav"
	params.ExtraParams = map[string]interface{}{"volume_gain_db": -3.0, "unrelated": "kept"}
	request := speechRequest("नमस्ते, आप कैसे हैं?", params)

	b.ReportAllocs()
	for b.Loop() {
		if _, err := ToVertexSpeechRequest(request); err != nil {
			b.Fatal(err)
		}
	}
}
