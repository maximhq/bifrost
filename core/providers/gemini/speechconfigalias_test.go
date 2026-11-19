package gemini

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSpeechConfigAcceptsCamelCaseVoice(t *testing.T) {
	var cfg SpeechConfig
	require.NoError(t, json.Unmarshal([]byte(`{
		"voiceConfig":{"prebuiltVoiceConfig":{"voiceName":"alloy"}},
		"languageCode":"en-US"
	}`), &cfg))

	require.NotNil(t, cfg.VoiceConfig)
	require.NotNil(t, cfg.VoiceConfig.PrebuiltVoiceConfig)
	assert.Equal(t, "alloy", cfg.VoiceConfig.PrebuiltVoiceConfig.VoiceName)
	assert.Equal(t, "en-US", cfg.LanguageCode)
}

func TestSpeechConfigAcceptsSnakeCaseVoice(t *testing.T) {
	var cfg SpeechConfig
	require.NoError(t, json.Unmarshal([]byte(`{
		"voice_config":{"prebuilt_voice_config":{"voice_name":"Kore"}},
		"language_code":"en-GB"
	}`), &cfg))

	require.NotNil(t, cfg.VoiceConfig)
	require.NotNil(t, cfg.VoiceConfig.PrebuiltVoiceConfig)
	assert.Equal(t, "Kore", cfg.VoiceConfig.PrebuiltVoiceConfig.VoiceName)
	assert.Equal(t, "en-GB", cfg.LanguageCode)
}

func TestSpeechConfigCamelCaseWins(t *testing.T) {
	var cfg SpeechConfig
	require.NoError(t, json.Unmarshal([]byte(`{
		"voiceConfig":{"prebuiltVoiceConfig":{"voiceName":"alloy"}},
		"voice_config":{"prebuilt_voice_config":{"voice_name":"Kore"}}
	}`), &cfg))

	require.NotNil(t, cfg.VoiceConfig)
	require.NotNil(t, cfg.VoiceConfig.PrebuiltVoiceConfig)
	assert.Equal(t, "alloy", cfg.VoiceConfig.PrebuiltVoiceConfig.VoiceName)
}

func TestSpeechStreamSendsTheAPIKeyHeaderAndEmitsDeltasThenDoneWithTokens(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1beta/models/gemini-2.5-flash-preview-tts:streamGenerateContent", r.URL.Path)
		assert.Equal(t, "sse", r.URL.Query().Get("alt"))
		assert.Equal(t, "dummy-key", r.Header.Get("x-goog-api-key"))
		assert.Equal(t, "text/event-stream", r.Header.Get("Accept"))
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"inlineData\":{\"mimeType\":\"audio/L16;rate=24000\",\"data\":\"cGNtLW9uZQ==\"}}]}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":9,\"candidatesTokenCount\":73,\"totalTokenCount\":82}}\n\n"))
	}))
	defer ts.Close()

	provider := NewGeminiProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: ts.URL + "/v1beta"},
	}, testNoopLogger{})
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	passThrough := func(_ *schemas.BifrostContext, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
		return result, err
	}
	voice := "Kore"
	stream, bifrostErr := provider.SpeechStream(ctx, passThrough, func(context.Context) {}, schemas.Key{Value: *schemas.NewSecretVar("dummy-key")}, &schemas.BifrostSpeechRequest{
		Provider: schemas.Gemini,
		Model:    "gemini-2.5-flash-preview-tts",
		Input:    &schemas.SpeechInput{Input: "hello"},
		Params:   &schemas.SpeechParameters{VoiceConfig: &schemas.SpeechVoiceInput{Voice: &voice}},
	})
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

	assert.Equal(t, [][]byte{[]byte("pcm-one")}, audio)
	require.NotNil(t, done, "the stream must end with a done chunk")
	require.NotNil(t, done.Usage)
	assert.Equal(t, 9, done.Usage.InputTokens)
	assert.Equal(t, 73, done.Usage.OutputTokens)
	assert.Equal(t, 82, done.Usage.TotalTokens)
}

// A response_format Gemini TTS cannot produce is the caller's mistake, so it answers 400 on the unary and
// the streaming path, and the request never reaches Google.
func TestSpeechRefusesAnUnsupportedResponseFormatAsACallerError(t *testing.T) {
	var upstreamCalls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { upstreamCalls.Add(1) }))
	defer ts.Close()
	provider := NewGeminiProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: ts.URL + "/v1beta"},
	}, testNoopLogger{})
	key := schemas.Key{Value: *schemas.NewSecretVar("dummy-key")}
	passThrough := func(_ *schemas.BifrostContext, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
		return result, err
	}
	voice := "Kore"

	for _, format := range []string{"mp3", "opus", "flac", "aac", "pcm"} {
		t.Run(format, func(t *testing.T) {
			request := &schemas.BifrostSpeechRequest{
				Provider: schemas.Gemini,
				Model:    "gemini-2.5-flash-preview-tts",
				Input:    &schemas.SpeechInput{Input: "hello"},
				Params:   &schemas.SpeechParameters{VoiceConfig: &schemas.SpeechVoiceInput{Voice: &voice}, ResponseFormat: format},
			}
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

			_, bifrostErr := provider.Speech(ctx, key, request)
			require.NotNil(t, bifrostErr)
			assert.Equal(t, http.StatusBadRequest, bifrostErr.EffectiveHTTPStatus(), "unary: %+v", bifrostErr.Error)
			assert.Contains(t, bifrostErr.Error.Message, format)

			_, bifrostErr = provider.SpeechStream(ctx, passThrough, func(context.Context) {}, key, request)
			require.NotNil(t, bifrostErr)
			assert.Equal(t, http.StatusBadRequest, bifrostErr.EffectiveHTTPStatus(), "stream: %+v", bifrostErr.Error)
		})
	}
	assert.Zero(t, upstreamCalls.Load(), "a refused request must not reach Google")
}

// wav and an empty format stay accepted: the refusal is only for formats Gemini TTS cannot produce.
func TestToGeminiSpeechRequestAcceptsWavAndEmptyFormat(t *testing.T) {
	voice := "Kore"
	for _, format := range []string{"", "wav"} {
		_, err := ToGeminiSpeechRequest(&schemas.BifrostSpeechRequest{
			Model:  "gemini-2.5-flash-preview-tts",
			Input:  &schemas.SpeechInput{Input: "hello"},
			Params: &schemas.SpeechParameters{VoiceConfig: &schemas.SpeechVoiceInput{Voice: &voice}, ResponseFormat: format},
		})
		require.NoError(t, err, "response_format %q", format)
	}
	_, err := ToGeminiSpeechRequest(&schemas.BifrostSpeechRequest{
		Model:  "gemini-2.5-flash-preview-tts",
		Input:  &schemas.SpeechInput{Input: "hello"},
		Params: &schemas.SpeechParameters{ResponseFormat: "mp3"},
	})
	require.Error(t, err)
	assert.True(t, providerUtils.IsInvalidRequestError(err), "an unsupported response_format must be a caller error")
}

// Gemini TTS has no instructions field; a style direction in front of the text is what steers it.
func TestToGeminiSpeechRequestPutsInstructionsInFrontOfTheText(t *testing.T) {
	voice := "Kore"
	build := func(instructions string) string {
		request, err := ToGeminiSpeechRequest(&schemas.BifrostSpeechRequest{
			Model:  "gemini-2.5-flash-preview-tts",
			Input:  &schemas.SpeechInput{Input: "The report is ready."},
			Params: &schemas.SpeechParameters{VoiceConfig: &schemas.SpeechVoiceInput{Voice: &voice}, Instructions: instructions},
		})
		require.NoError(t, err)
		require.Len(t, request.Contents, 1)
		require.Len(t, request.Contents[0].Parts, 1)
		return request.Contents[0].Parts[0].Text
	}

	assert.Equal(t, "Speak slowly and warmly: The report is ready.", build("Speak slowly and warmly"))
	assert.Equal(t, "Speak slowly and warmly: The report is ready.", build("  Speak slowly and warmly  "))
	assert.Equal(t, "The report is ready.", build(""), "no instructions leaves the text alone")
	assert.Equal(t, "The report is ready.", build("   "), "blank instructions leave the text alone")
}
