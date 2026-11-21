package xai_test

import (
	"context"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"testing/iotest"

	"github.com/bytedance/sonic"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/internal/llmtests"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/providers/xai"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestXAI(t *testing.T) {
	t.Parallel()
	if strings.TrimSpace(os.Getenv("XAI_API_KEY")) == "" {
		t.Skip("Skipping XAI tests because XAI_API_KEY is not set")
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	testConfig := llmtests.ComprehensiveTestConfig{
		Provider:                schemas.XAI,
		ChatModel:               "grok-4-0709",
		ReasoningModel:          "grok-3-mini",
		TextModel:               "", // xAI dropped raw sampling (/v1/completions); all current models are reasoning models
		VisionModel:             "grok-4-1-fast-reasoning",
		EmbeddingModel:          "", // XAI doesn't support embedding
		ImageGenerationModel:    "grok-imagine-image",
		ExternalCompactionModel: "grok-4.3",
		Scenarios: llmtests.TestScenarios{
			TextCompletion:             true,
			SimpleChat:                 true,
			CompletionStream:           true,
			MultiTurnConversation:      true,
			ToolCalls:                  true,
			ToolCallsStreaming:         true,
			MultipleToolCalls:          true,
			MultipleToolCallsStreaming: true,
			End2EndToolCalling:         true,
			AutomaticFunctionCall:      true,
			ImageURL:                   true,
			ImageBase64:                true,
			ImageGeneration:            true,
			ImageGenerationStream:      false,
			FileBase64:                 false,
			FileURL:                    false,
			MultipleImages:             true,
			CompleteEnd2End:            true,
			Reasoning:                  true,
			Embedding:                  false,
			ListModels:                 true,
			ExternalCompaction:         true,
		},
	}

	t.Run("XAITests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
}

// A retrieve must land on /v1/models/{model} with the key's bearer token, map the
// provider's model object onto the Bifrost shape, and refuse a path-shaping model id
// before anything is dispatched.
func TestXAIModelRetrieve(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var gotPath, gotMethod, gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotPath, gotMethod, gotAuth = r.URL.Path, r.Method, r.Header.Get("Authorization")
		mu.Unlock()
		_, _ = w.Write([]byte(`{"id":"grok-4","object":"model","created":1743724800,"owned_by":"xai"}`))
	}))
	defer server.Close()

	provider, err := xai.NewXAIProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL, DefaultRequestTimeoutInSeconds: 30},
	}, bifrost.NewNoOpLogger())
	require.NoError(t, err)

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	key := schemas.Key{Value: *schemas.NewSecretVar("xai-test")}

	response, bifrostErr := provider.ModelRetrieve(ctx, key, &schemas.BifrostModelRetrieveRequest{Model: "grok-4"})
	require.Nil(t, bifrostErr)

	mu.Lock()
	path, method, auth := gotPath, gotMethod, gotAuth
	mu.Unlock()
	require.Equal(t, "/v1/models/grok-4", path)
	require.Equal(t, http.MethodGet, method)
	require.Equal(t, "Bearer xai-test", auth)

	require.Equal(t, "xai/grok-4", response.ID)
	require.Equal(t, schemas.Ptr("xai"), response.OwnedBy)
	require.Equal(t, schemas.Ptr(int64(1743724800)), response.Created)

	_, bifrostErr = provider.ModelRetrieve(ctx, key, &schemas.BifrostModelRetrieveRequest{Model: "../models"})
	require.NotNil(t, bifrostErr)
	require.Equal(t, schemas.Ptr(http.StatusBadRequest), bifrostErr.StatusCode)
}

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
		body, err := providerUtils.MarshalSorted(got)
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
		body, err := providerUtils.MarshalSorted(got)
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

// stageLargePayload puts ctx into large-payload mode with body as the staged client body.
func stageLargePayload(ctx *schemas.BifrostContext, body, contentType string) {
	ctx.SetValue(schemas.BifrostContextKeyLargePayloadMode, true)
	ctx.SetValue(schemas.BifrostContextKeyLargePayloadReader, io.Reader(strings.NewReader(body)))
	ctx.SetValue(schemas.BifrostContextKeyLargePayloadContentLength, len(body))
	ctx.SetValue(schemas.BifrostContextKeyLargePayloadContentType, contentType)
}

// A staged large-payload body is the client's OpenAI-shaped speech JSON (input, voice),
// which /v1/tts does not accept. Speech refuses it instead of sending it, or an empty body.
func TestXAISpeechRefusesLargePayload(t *testing.T) {
	t.Parallel()

	var hits int
	var mu sync.Mutex
	provider := newXAITestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write([]byte("ID3fake"))
	})

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	stageLargePayload(ctx, `{"model":"xai/grok-tts","input":"Hello from a staged body.","voice":"eve"}`, "application/json")
	resp, bifrostErr := provider.Speech(ctx, schemas.Key{Value: *schemas.NewSecretVar("xai-test")}, &schemas.BifrostSpeechRequest{Model: "grok-tts"})
	require.Nil(t, resp)
	require.NotNil(t, bifrostErr)
	require.NotNil(t, bifrostErr.StatusCode)
	assert.Equal(t, http.StatusBadRequest, *bifrostErr.StatusCode)
	assert.Contains(t, bifrostErr.Error.Message, "large-payload")
	mu.Lock()
	defer mu.Unlock()
	assert.Zero(t, hits, "the staged body must not reach xAI")
}

// A large-payload transcription streams the staged multipart body to /v1/stt and must
// still decode xAI's response shape: words named "text" and usage in seconds.
func TestXAITranscriptionLargePayloadDecodesXAIResponse(t *testing.T) {
	t.Parallel()

	var gotPath string
	provider := newXAITestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(xaiSTTResponse))
	})

	staged := "--b\r\nContent-Disposition: form-data; name=\"model\"\r\n\r\ngrok-voice-transcribe-2.0\r\n" +
		"--b\r\nContent-Disposition: form-data; name=\"file\"; filename=\"a.wav\"\r\nContent-Type: audio/wav\r\n\r\nRIFF\x00\x00\x00\x00WAVEfake\r\n--b--\r\n"
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	stageLargePayload(ctx, staged, "multipart/form-data; boundary=b")
	ctx.SetValue(schemas.BifrostContextKeyCaptureRawResponse, true)
	resp, bifrostErr := provider.Transcription(ctx, schemas.Key{Value: *schemas.NewSecretVar("xai-test")}, &schemas.BifrostTranscriptionRequest{Model: "grok-voice-transcribe-2.0"})
	require.Nil(t, bifrostErr)
	assert.Equal(t, "/v1/stt", gotPath)
	assert.Equal(t, "The quick brown fox jumps over the lazy dog.", resp.Text)
	require.Len(t, resp.Words, 9)
	assert.Equal(t, schemas.TranscriptionWord{Word: "The", Start: 0.06, End: 0.16}, resp.Words[0])
	require.NotNil(t, resp.Usage)
	assert.Equal(t, "duration", resp.Usage.Type)
	require.NotNil(t, resp.Usage.Seconds)
	assert.Equal(t, 2.846, *resp.Usage.Seconds)
	// Raw-response capture applies on the staged path as on the normal one.
	raw, ok := resp.ExtraFields.RawResponse.(map[string]interface{})
	require.True(t, ok, "raw response should be the decoded xAI body, got %T", resp.ExtraFields.RawResponse)
	assert.Equal(t, "en", raw["language"])
}

// xAI ignores every multipart field after the file, and a large-payload body streams to
// /v1/stt as the client sent it. A field after the file fails the upload with a 400
// instead of being dropped, including when the delimiter straddles two reads.
func TestXAITranscriptionLargePayloadFileMustBeLast(t *testing.T) {
	t.Parallel()

	audio := "RIFF\x00\x00\x00\x00WAVE" + strings.Repeat("-b\r\n--", 20000)
	modelPart := "--b\r\nContent-Disposition: form-data; name=\"model\"\r\n\r\ngrok-voice-transcribe-2.0\r\n"
	languagePart := "--b\r\nContent-Disposition: form-data; name=\"language\"\r\n\r\nsv\r\n"
	filePart := "--b\r\nContent-Disposition: form-data; name=\"file\"; filename=\"a.wav\"\r\nContent-Type: audio/wav\r\n\r\n" + audio + "\r\n"
	fileLast := modelPart + languagePart + filePart + "--b--\r\n"
	fileFirst := modelPart + filePart + languagePart + "--b--\r\n"

	readers := map[string]func(string) io.Reader{
		"whole":    func(s string) io.Reader { return strings.NewReader(s) },
		"one byte": func(s string) io.Reader { return iotest.OneByteReader(strings.NewReader(s)) },
	}
	for name, newReader := range readers {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			provider := newXAITestProvider(t, func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.ReadAll(r.Body)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(xaiSTTResponse))
			})
			key := schemas.Key{Value: *schemas.NewSecretVar("xai-test")}
			stage := func(body string) *schemas.BifrostContext {
				ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
				stageLargePayload(ctx, body, "multipart/form-data; boundary=b")
				ctx.SetValue(schemas.BifrostContextKeyLargePayloadReader, newReader(body))
				return ctx
			}

			resp, bifrostErr := provider.Transcription(stage(fileLast), key, &schemas.BifrostTranscriptionRequest{Model: "grok-voice-transcribe-2.0"})
			require.Nil(t, bifrostErr)
			assert.Equal(t, "The quick brown fox jumps over the lazy dog.", resp.Text)

			resp, bifrostErr = provider.Transcription(stage(fileFirst), key, &schemas.BifrostTranscriptionRequest{Model: "grok-voice-transcribe-2.0"})
			require.Nil(t, resp)
			require.NotNil(t, bifrostErr)
			require.NotNil(t, bifrostErr.StatusCode)
			assert.Equal(t, http.StatusBadRequest, *bifrostErr.StatusCode)
			assert.Contains(t, bifrostErr.Error.Message, `"language"`)
		})
	}
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

// A proxy in front of xAI can answer 200 with a JSON error. Speech returns that error
// instead of handing the JSON to the client as audio.
func TestXAISpeechJSONErrorOn200(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name, body, message string
		status              int
	}{
		{"openai envelope", `{"error":{"message":"rate limited","type":"rate_limit_error"}}`, "rate limited", http.StatusBadGateway},
		{"xai shape", `{"code":"Some resource has been exhausted","error":"quota used up"}`, "quota used up", http.StatusBadGateway},
		{"numeric code", `{"code":429,"error":"slow down"}`, "slow down", http.StatusTooManyRequests},
		{"no error field", `{"status":"ok"}`, "xAI text-to-speech returned JSON instead of audio", http.StatusBadGateway},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			provider := newXAITestProvider(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				_, _ = w.Write([]byte(tc.body))
			})
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			resp, bifrostErr := provider.Speech(ctx, schemas.Key{Value: *schemas.NewSecretVar("xai-test")}, &schemas.BifrostSpeechRequest{
				Model: "grok-tts",
				Input: &schemas.SpeechInput{Input: "x"},
			})
			require.Nil(t, resp)
			require.NotNil(t, bifrostErr)
			assert.False(t, bifrostErr.IsBifrostError)
			require.NotNil(t, bifrostErr.StatusCode)
			assert.Equal(t, tc.status, *bifrostErr.StatusCode)
			require.NotNil(t, bifrostErr.Error)
			assert.Equal(t, tc.message, bifrostErr.Error.Message)
		})
	}
}

func TestXAISpeechEmptyInputIsBadRequest(t *testing.T) {
	t.Parallel()

	provider := newXAITestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("an empty input must not reach xAI")
	})
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	_, bifrostErr := provider.Speech(ctx, schemas.Key{Value: *schemas.NewSecretVar("xai-test")}, &schemas.BifrostSpeechRequest{
		Model: "grok-tts",
		Input: &schemas.SpeechInput{},
	})
	require.NotNil(t, bifrostErr)
	require.NotNil(t, bifrostErr.StatusCode)
	assert.Equal(t, http.StatusBadRequest, *bifrostErr.StatusCode)
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

// A 200 body carrying an error instead of a transcript is returned as that error, not
// decoded into an empty transcript.
func TestXAITranscriptionJSONErrorOn200(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name, body, message string
	}{
		{"openai envelope", `{"error":{"message":"rate limited","type":"rate_limit_error"}}`, "rate limited"},
		{"xai shape", `{"code":"Some resource has been exhausted","error":"quota used up"}`, "quota used up"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			provider := newXAITestProvider(t, func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.body))
			})
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			resp, bifrostErr := provider.Transcription(ctx, schemas.Key{Value: *schemas.NewSecretVar("xai-test")}, &schemas.BifrostTranscriptionRequest{
				Model: "grok-voice-transcribe-2.0",
				Input: &schemas.TranscriptionInput{File: []byte("RIFF\x00\x00\x00\x00WAVEfake"), Filename: "a.wav"},
			})
			require.Nil(t, resp)
			require.NotNil(t, bifrostErr)
			assert.False(t, bifrostErr.IsBifrostError)
			require.NotNil(t, bifrostErr.StatusCode)
			assert.Equal(t, http.StatusBadGateway, *bifrostErr.StatusCode)
			require.NotNil(t, bifrostErr.Error)
			assert.Equal(t, tc.message, bifrostErr.Error.Message)
		})
	}
}

// /v1/stt only answers xAI's JSON, so a plain-text format would put that body in the
// transcript and diarized_json would decode it as OpenAI's diarized shape, dropping
// words, language and duration usage. Those formats are refused before anything is sent.
func TestXAITranscriptionRejectsPlainTextFormats(t *testing.T) {
	t.Parallel()

	for _, format := range []string{"text", "srt", "vtt", "diarized_json"} {
		t.Run(format, func(t *testing.T) {
			t.Parallel()
			var hits int
			var mu sync.Mutex
			provider := newXAITestProvider(t, func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				hits++
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(xaiSTTResponse))
			})

			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			resp, bifrostErr := provider.Transcription(ctx, schemas.Key{Value: *schemas.NewSecretVar("xai-test")}, &schemas.BifrostTranscriptionRequest{
				Model:  "grok-voice-transcribe-2.0",
				Input:  &schemas.TranscriptionInput{File: []byte("RIFF\x00\x00\x00\x00WAVEfake"), Filename: "a.wav"},
				Params: &schemas.TranscriptionParameters{ResponseFormat: schemas.Ptr(format)},
			})
			require.Nil(t, resp)
			require.NotNil(t, bifrostErr)
			require.NotNil(t, bifrostErr.StatusCode)
			assert.Equal(t, http.StatusBadRequest, *bifrostErr.StatusCode)
			assert.Contains(t, bifrostErr.Error.Message, format)
			mu.Lock()
			defer mu.Unlock()
			assert.Zero(t, hits, "the request must not reach xAI")
		})
	}
}
