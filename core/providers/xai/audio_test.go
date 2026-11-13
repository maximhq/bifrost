package xai_test

import (
	"context"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bytedance/sonic"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/providers/xai"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newXAITestProvider(t *testing.T, handler http.HandlerFunc) *xai.XAIProvider {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	provider, err := xai.NewXAIProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL, DefaultRequestTimeoutInSeconds: 30},
	}, bifrost.NewNoOpLogger())
	require.NoError(t, err)
	return provider
}

func TestToXAISpeechRequest(t *testing.T) {
	t.Parallel()

	t.Run("defaults", func(t *testing.T) {
		got := xai.ToXAISpeechRequest(&schemas.BifrostSpeechRequest{
			Model: "grok-tts",
			Input: &schemas.SpeechInput{Input: "Hello."},
		})
		body, err := sonic.Marshal(got)
		require.NoError(t, err)
		assert.JSONEq(t, `{"text":"Hello.","language":"auto","voice_id":"eve"}`, string(body))
	})

	t.Run("params", func(t *testing.T) {
		extra := map[string]interface{}{"sample_rate": 16000}
		got := xai.ToXAISpeechRequest(&schemas.BifrostSpeechRequest{
			Model: "grok-tts",
			Input: &schemas.SpeechInput{Input: "Hej."},
			Params: &schemas.SpeechParameters{
				VoiceConfig:    &schemas.SpeechVoiceInput{Voice: schemas.Ptr("ara")},
				ResponseFormat: "wav",
				Speed:          schemas.Ptr(1.2),
				LanguageCode:   schemas.Ptr("sv"),
				ExtraParams:    extra,
			},
		})
		body, err := sonic.Marshal(got)
		require.NoError(t, err)
		assert.JSONEq(t, `{"text":"Hej.","language":"sv","voice_id":"ara","speed":1.2,"output_format":{"codec":"wav","sample_rate":16000}}`, string(body))
		assert.Empty(t, got.ExtraParams, "sample_rate is consumed")
		assert.Contains(t, extra, "sample_rate", "caller's map is not mutated")
	})
}

func TestXAISpeechReturnsRawAudio(t *testing.T) {
	t.Parallel()

	audio := []byte("ID3\x04\x00fake-mp3-bytes")
	var gotPath, gotAuth string
	var gotBody map[string]interface{}
	provider := newXAITestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		_ = sonic.Unmarshal(raw, &gotBody)
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write(audio)
	})

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	resp, bifrostErr := provider.Speech(ctx, schemas.Key{Value: *schemas.NewSecretVar("xai-test")}, &schemas.BifrostSpeechRequest{
		Model:  "grok-tts",
		Input:  &schemas.SpeechInput{Input: "Hello from the gateway test."},
		Params: &schemas.SpeechParameters{VoiceConfig: &schemas.SpeechVoiceInput{Voice: schemas.Ptr("eve")}, ResponseFormat: "mp3"},
	})
	require.Nil(t, bifrostErr)
	assert.Equal(t, "/v1/tts", gotPath)
	assert.Equal(t, "Bearer xai-test", gotAuth)
	assert.Equal(t, map[string]interface{}{
		"text":          "Hello from the gateway test.",
		"language":      "auto",
		"voice_id":      "eve",
		"output_format": map[string]interface{}{"codec": "mp3"},
	}, gotBody)
	assert.Equal(t, audio, resp.Audio)
}

func TestXAISpeechError(t *testing.T) {
	t.Parallel()

	provider := newXAITestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"TTS synthesis failed: Voice 'nope' not found"}`))
	})

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	_, bifrostErr := provider.Speech(ctx, schemas.Key{Value: *schemas.NewSecretVar("xai-test")}, &schemas.BifrostSpeechRequest{
		Model:  "grok-tts",
		Input:  &schemas.SpeechInput{Input: "x"},
		Params: &schemas.SpeechParameters{VoiceConfig: &schemas.SpeechVoiceInput{Voice: schemas.Ptr("nope")}},
	})
	require.NotNil(t, bifrostErr)
	require.NotNil(t, bifrostErr.StatusCode)
	assert.Equal(t, http.StatusNotFound, *bifrostErr.StatusCode)
	assert.Equal(t, "TTS synthesis failed: Voice 'nope' not found", bifrostErr.Error.Message)
}

// xaiSTTResponse is a verbatim POST /v1/stt response for a 16 kHz WAV clip.
const xaiSTTResponse = `{"text":"The quick brown fox jumps over the lazy dog.","language":"en","duration":2.846,"words":[{"text":"The","start":0.06,"end":0.16},{"text":"quick","start":0.18,"end":0.401},{"text":"brown","start":0.461,"end":0.782},{"text":"fox","start":0.822,"end":1.022},{"text":"jumps","start":1.142,"end":1.443},{"text":"over","start":1.563,"end":1.824},{"text":"the","start":1.864,"end":1.944},{"text":"lazy","start":2.024,"end":2.325},{"text":"dog.","start":2.385,"end":2.585}]}`

func TestXAITranscription(t *testing.T) {
	t.Parallel()

	var gotPath string
	var fieldOrder []string
	var gotModel, gotLanguage string
	provider := newXAITestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if assert.NoError(t, err) {
			mr := multipart.NewReader(r.Body, params["boundary"])
			for {
				part, err := mr.NextPart()
				if err != nil {
					break
				}
				value, _ := io.ReadAll(part)
				fieldOrder = append(fieldOrder, part.FormName())
				switch part.FormName() {
				case "model":
					gotModel = string(value)
				case "language":
					gotLanguage = string(value)
				}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(xaiSTTResponse))
	})

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	resp, bifrostErr := provider.Transcription(ctx, schemas.Key{Value: *schemas.NewSecretVar("xai-test")}, &schemas.BifrostTranscriptionRequest{
		Model:  "grok-voice-transcribe-2.0",
		Input:  &schemas.TranscriptionInput{File: []byte("RIFF\x00\x00\x00\x00WAVEfake"), Filename: "a.wav"},
		Params: &schemas.TranscriptionParameters{Language: schemas.Ptr("en")},
	})
	require.Nil(t, bifrostErr)

	assert.Equal(t, "/v1/stt", gotPath)
	require.NotEmpty(t, fieldOrder)
	assert.Equal(t, "file", fieldOrder[len(fieldOrder)-1], "xAI ignores fields after the file")
	assert.Equal(t, "grok-voice-transcribe-2.0", gotModel)
	assert.Equal(t, "en", gotLanguage)

	assert.Equal(t, "The quick brown fox jumps over the lazy dog.", resp.Text)
	require.NotNil(t, resp.Language)
	assert.Equal(t, "en", *resp.Language)
	require.NotNil(t, resp.Duration)
	assert.Equal(t, 2.846, *resp.Duration)
	require.Len(t, resp.Words, 9)
	assert.Equal(t, schemas.TranscriptionWord{Word: "The", Start: 0.06, End: 0.16}, resp.Words[0])
	assert.Equal(t, "dog.", resp.Words[8].Word)
	require.NotNil(t, resp.Usage)
	assert.Equal(t, "duration", resp.Usage.Type)
	require.NotNil(t, resp.Usage.Seconds)
	assert.Equal(t, 2.846, *resp.Usage.Seconds)
}
