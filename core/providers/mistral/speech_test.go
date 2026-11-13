package mistral

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestToMistralSpeechRequest(t *testing.T) {
	t.Parallel()

	got := ToMistralSpeechRequest(&schemas.BifrostSpeechRequest{
		Model: "voxtral-mini-tts-latest",
		Input: &schemas.SpeechInput{Input: "Hello."},
		Params: &schemas.SpeechParameters{
			VoiceConfig:    &schemas.SpeechVoiceInput{Voice: schemas.Ptr("gb_jane_neutral")},
			ResponseFormat: "wav",
		},
	})
	body, err := providerUtils.MarshalSorted(got)
	require.NoError(t, err)
	assert.JSONEq(t, `{"model":"voxtral-mini-tts-latest","input":"Hello.","voice_id":"gb_jane_neutral","response_format":"wav"}`, string(body))

	bare := ToMistralSpeechRequest(&schemas.BifrostSpeechRequest{Model: "voxtral-mini-tts-latest", Input: &schemas.SpeechInput{Input: "Hi"}})
	body, err = providerUtils.MarshalSorted(bare)
	require.NoError(t, err)
	assert.JSONEq(t, `{"model":"voxtral-mini-tts-latest","input":"Hi"}`, string(body))
}

func TestSpeechWithMockServer(t *testing.T) {
	t.Parallel()

	audio := []byte("ID3\x04\x00fake-mp3-bytes")
	var gotPath, gotAuth string
	var gotBody map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		_ = sonic.Unmarshal(raw, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"audio_data":"` + base64.StdEncoding.EncodeToString(audio) + `"}`))
	}))
	defer server.Close()

	provider := NewMistralProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL, DefaultRequestTimeoutInSeconds: 30},
	}, &testLogger{})

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	resp, bifrostErr := provider.Speech(ctx, schemas.Key{Value: *schemas.NewSecretVar("test-api-key")}, &schemas.BifrostSpeechRequest{
		Model:  "voxtral-mini-tts-latest",
		Input:  &schemas.SpeechInput{Input: "Hello from the gateway test."},
		Params: &schemas.SpeechParameters{VoiceConfig: &schemas.SpeechVoiceInput{Voice: schemas.Ptr("gb_jane_neutral")}, ResponseFormat: "mp3"},
	})
	require.Nil(t, bifrostErr)
	assert.Equal(t, "/v1/audio/speech", gotPath)
	assert.Equal(t, "Bearer test-api-key", gotAuth)
	assert.Equal(t, map[string]interface{}{
		"model":           "voxtral-mini-tts-latest",
		"input":           "Hello from the gateway test.",
		"voice_id":        "gb_jane_neutral",
		"response_format": "mp3",
	}, gotBody)
	assert.Equal(t, audio, resp.Audio)
}

func TestSpeechInvalidVoice(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"object":"error","message":"Voice 'nope' not found.","type":"invalid_voice","param":null,"code":"1902","raw_status_code":404}`))
	}))
	defer server.Close()

	provider := NewMistralProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL, DefaultRequestTimeoutInSeconds: 30},
	}, &testLogger{})

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	_, bifrostErr := provider.Speech(ctx, schemas.Key{Value: *schemas.NewSecretVar("test-api-key")}, &schemas.BifrostSpeechRequest{
		Model:  "voxtral-mini-tts-latest",
		Input:  &schemas.SpeechInput{Input: "Hi"},
		Params: &schemas.SpeechParameters{VoiceConfig: &schemas.SpeechVoiceInput{Voice: schemas.Ptr("nope")}},
	})
	require.NotNil(t, bifrostErr)
	assert.Contains(t, bifrostErr.Error.Message, "Voice 'nope' not found.")
}

// With raw-response capture on, the decoded Mistral JSON body is returned alongside the audio.
func TestSpeechReturnsRawResponse(t *testing.T) {
	t.Parallel()

	upstream := `{"audio_data":"` + base64.StdEncoding.EncodeToString([]byte("ID3fake")) + `"}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(upstream))
	}))
	defer server.Close()

	provider := NewMistralProvider(&schemas.ProviderConfig{
		NetworkConfig:       schemas.NetworkConfig{BaseURL: server.URL, DefaultRequestTimeoutInSeconds: 30},
		SendBackRawResponse: true,
	}, &testLogger{})

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	resp, bifrostErr := provider.Speech(ctx, schemas.Key{Value: *schemas.NewSecretVar("test-api-key")}, &schemas.BifrostSpeechRequest{
		Model: "voxtral-mini-tts-latest",
		Input: &schemas.SpeechInput{Input: "Hi"},
	})
	require.Nil(t, bifrostErr)
	assert.Equal(t, []byte("ID3fake"), resp.Audio)
	raw, ok := resp.ExtraFields.RawResponse.(json.RawMessage)
	require.True(t, ok, "raw response should be the upstream JSON, got %T", resp.ExtraFields.RawResponse)
	assert.JSONEq(t, upstream, string(raw))
}

// A staged large-payload body is the client's OpenAI-shaped speech JSON (voice), which
// Mistral does not accept (voice_id). Speech refuses it with a 400, before the input
// check (a staged request carries no parsed input) and without calling Mistral.
func TestSpeechRefusesLargePayload(t *testing.T) {
	t.Parallel()

	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"audio_data":"` + base64.StdEncoding.EncodeToString([]byte("ID3fake")) + `"}`))
	}))
	defer server.Close()

	provider := NewMistralProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL, DefaultRequestTimeoutInSeconds: 30},
	}, &testLogger{})

	staged := `{"model":"mistral/voxtral-mini-tts-latest","input":"Hello from a staged body.","voice":"gb_jane_neutral"}`
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyLargePayloadMode, true)
	ctx.SetValue(schemas.BifrostContextKeyLargePayloadReader, io.Reader(strings.NewReader(staged)))
	ctx.SetValue(schemas.BifrostContextKeyLargePayloadContentLength, len(staged))
	ctx.SetValue(schemas.BifrostContextKeyLargePayloadContentType, "application/json")
	resp, bifrostErr := provider.Speech(ctx, schemas.Key{Value: *schemas.NewSecretVar("test-api-key")}, &schemas.BifrostSpeechRequest{Model: "voxtral-mini-tts-latest"})
	require.Nil(t, resp)
	require.NotNil(t, bifrostErr)
	require.NotNil(t, bifrostErr.StatusCode)
	assert.Equal(t, http.StatusBadRequest, *bifrostErr.StatusCode)
	assert.Contains(t, bifrostErr.Error.Message, "large-payload")
	assert.Zero(t, hits.Load(), "the staged body must not reach Mistral")
}
