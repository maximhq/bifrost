package gemini

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGeminiInteractionTranscriptionPreservesRawRequestBody(t *testing.T) {
	rawBody := `{"model":"gemini-3.5-transcribe","input":[{"type":"audio","data":"raw-audio","mime_type":"audio/wav"}],"provider_only":"keep-me"}`
	var receivedBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"output_text":"transcribed"}`))
	}))
	defer server.Close()

	provider := NewGeminiProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL},
	}, testNoopLogger{})
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyUseRawRequestBody, true)
	request := &schemas.BifrostTranscriptionRequest{
		Provider:       schemas.Gemini,
		Model:          "gemini-3.5-transcribe",
		Input:          &schemas.TranscriptionInput{File: []byte("typed-audio")},
		RawRequestBody: []byte(rawBody),
	}

	_, bifrostErr := provider.Transcription(ctx, schemas.Key{Value: *schemas.NewSecretVar("test-key")}, request)
	require.Nil(t, bifrostErr)
	assert.Equal(t, rawBody, string(receivedBody))
}

func TestGeminiInteractionTranscriptionStreamPreservesRawRequestBody(t *testing.T) {
	rawBody := `{"model":"gemini-3.5-transcribe","stream":true,"input":[{"type":"audio","data":"raw-audio","mime_type":"audio/wav"}],"provider_only":"keep-me"}`
	var receivedBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"delta\":{\"type\":\"text\",\"text\":\"transcribed\"}}\n\n" +
			"data: {\"event_type\":\"interaction.completed\",\"interaction\":{\"status\":\"completed\"}}\n\n"))
	}))
	defer server.Close()

	provider := NewGeminiProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL},
	}, testNoopLogger{})
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyUseRawRequestBody, true)
	request := &schemas.BifrostTranscriptionRequest{
		Provider:       schemas.Gemini,
		Model:          "gemini-3.5-transcribe",
		Input:          &schemas.TranscriptionInput{File: []byte("typed-audio")},
		RawRequestBody: []byte(rawBody),
	}
	postHookRunner := func(_ *schemas.BifrostContext, response *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
		return response, err
	}

	stream, bifrostErr := provider.TranscriptionStream(ctx, postHookRunner, nil, schemas.Key{Value: *schemas.NewSecretVar("test-key")}, request)
	require.Nil(t, bifrostErr)
	for range stream {
	}
	assert.Equal(t, rawBody, string(receivedBody))
}

// interactionTranscriptionTestAudio starts with a WAV header so MIME detection is deterministic.
var interactionTranscriptionTestAudio = []byte("RIFF\x24\x00\x00\x00WAVEfmt \x10\x00\x00\x00")

type capturedInteractionRequest struct {
	method string
	path   string
	accept string
	body   map[string]interface{}
}

// newInteractionTranscriptionServer records the outgoing request and answers with
// the given content type and body.
func newInteractionTranscriptionServer(t *testing.T, contentType, responseBody string) (*httptest.Server, *capturedInteractionRequest) {
	t.Helper()
	captured := &capturedInteractionRequest{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		captured.method = r.Method
		captured.path = r.URL.Path
		captured.accept = r.Header.Get("Accept")
		_ = json.Unmarshal(raw, &captured.body)
		w.Header().Set("Content-Type", contentType)
		_, _ = w.Write([]byte(responseBody))
	}))
	t.Cleanup(server.Close)
	return server, captured
}

func newInteractionTranscriptionTestProvider(baseURL string) *GeminiProvider {
	return NewGeminiProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: baseURL},
	}, testNoopLogger{})
}

func passthroughPostHookRunner(_ *schemas.BifrostContext, response *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
	return response, err
}

func collectTranscriptionStream(stream chan *schemas.BifrostStreamChunk) []*schemas.BifrostStreamChunk {
	var chunks []*schemas.BifrostStreamChunk
	for chunk := range stream {
		chunks = append(chunks, chunk)
	}
	return chunks
}

func TestGeminiInteractionTranscriptionRoutesToInteractionsEndpoint(t *testing.T) {
	server, captured := newInteractionTranscriptionServer(t, "application/json",
		`{"steps":[{"type":"model_output","content":[{"type":"text","text":"hello "},{"type":"text","text":"world"}]}],`+
			`"usage":{"total_input_tokens":10,"total_output_tokens":5,"total_tokens":15}}`)
	provider := newInteractionTranscriptionTestProvider(server.URL)
	request := &schemas.BifrostTranscriptionRequest{
		Provider: schemas.Gemini,
		Model:    geminiInteractionTranscriptionModel,
		Input:    &schemas.TranscriptionInput{File: interactionTranscriptionTestAudio},
		Params: &schemas.TranscriptionParameters{
			Prompt:      schemas.Ptr("Transcribe the call."),
			Language:    schemas.Ptr("en"),
			ExtraParams: map[string]interface{}{"diarization_mode": "speaker_labels"},
		},
	}

	response, bifrostErr := provider.Transcription(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), schemas.Key{Value: *schemas.NewSecretVar("test-key")}, request)
	require.Nil(t, bifrostErr)

	// Outgoing path and body.
	assert.Equal(t, http.MethodPost, captured.method)
	assert.Equal(t, "/interactions", captured.path)
	assert.Equal(t, geminiInteractionTranscriptionModel, captured.body["model"])
	assert.Nil(t, captured.body["stream"], "unary request must not set stream")
	input, ok := captured.body["input"].([]interface{})
	require.True(t, ok, "input must be an array: %v", captured.body)
	require.Len(t, input, 2)
	assert.Equal(t, map[string]interface{}{"type": "text", "text": "Transcribe the call."}, input[0])
	assert.Equal(t, map[string]interface{}{
		"type":      "audio",
		"data":      encodeBytesToBase64String(interactionTranscriptionTestAudio),
		"mime_type": providerUtils.DetectAudioMimeType(interactionTranscriptionTestAudio),
	}, input[1])
	assert.Equal(t, map[string]interface{}{
		"transcription_config": map[string]interface{}{
			"language_codes": []interface{}{"en"},
			"mode":           map[string]interface{}{"type": "verbatim", "diarization_mode": "speaker_labels"},
		},
	}, captured.body["generation_config"])

	// Text falls back to steps when output_text is absent; usage maps to tokens.
	assert.Equal(t, "hello world", response.Text)
	require.NotNil(t, response.Usage)
	assert.Equal(t, "tokens", response.Usage.Type)
	assert.Equal(t, 10, *response.Usage.InputTokens)
	assert.Equal(t, 5, *response.Usage.OutputTokens)
	assert.Equal(t, 15, *response.Usage.TotalTokens)
}

// Word timestamps requested with timestamp_granularities come back as word_info
// annotations (offsets as duration strings) and must map to Words in seconds,
// alongside the unchanged transcript text.
func TestGeminiInteractionTranscriptionMapsWordAnnotations(t *testing.T) {
	server, captured := newInteractionTranscriptionServer(t, "application/json",
		`{"status":"completed","output_text":"Hello world","steps":[{"type":"model_output","content":[{"type":"text","text":"Hello world","annotations":[`+
			`{"type":"word_info","text":"Hello","speaker":"spk_1","start_offset":"0.100s","end_offset":"0.400s"},`+
			`{"type":"word_info","text":"world","speaker":"spk_1","start_offset":"0.500s","end_offset":"0.800s"},`+
			`{"type":"word_info","text":"bad","start_offset":"not-a-duration","end_offset":"1s"},`+
			`{"type":"other","text":"ignored","start_offset":"1s","end_offset":"2s"}]}]}]}`)
	provider := newInteractionTranscriptionTestProvider(server.URL)

	response, bifrostErr := provider.Transcription(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), schemas.Key{Value: *schemas.NewSecretVar("test-key")}, &schemas.BifrostTranscriptionRequest{
		Provider: schemas.Gemini,
		Model:    geminiInteractionTranscriptionModel,
		Input:    &schemas.TranscriptionInput{File: interactionTranscriptionTestAudio},
		Params:   &schemas.TranscriptionParameters{TimestampGranularities: []string{"word"}},
	})
	require.Nil(t, bifrostErr)

	assert.Equal(t, map[string]interface{}{
		"transcription_config": map[string]interface{}{
			"mode": map[string]interface{}{"type": "verbatim", "timestamp_granularities": []interface{}{"word"}},
		},
	}, captured.body["generation_config"])
	assert.Equal(t, "Hello world", response.Text)
	require.Len(t, response.Words, 2, "only parseable word_info annotations become words")
	assert.Equal(t, "Hello", response.Words[0].Word)
	assert.InDelta(t, 0.1, response.Words[0].Start, 1e-9)
	assert.InDelta(t, 0.4, response.Words[0].End, 1e-9)
	require.NotNil(t, response.Words[0].Speaker)
	assert.Equal(t, "spk_1", *response.Words[0].Speaker)
	assert.Equal(t, "world", response.Words[1].Word)
	assert.InDelta(t, 0.5, response.Words[1].Start, 1e-9)
	assert.InDelta(t, 0.8, response.Words[1].End, 1e-9)
}

func TestGeminiInteractionTranscriptionPrefersOutputText(t *testing.T) {
	server, _ := newInteractionTranscriptionServer(t, "application/json",
		`{"output_text":"  from output text  ","steps":[{"content":[{"type":"text","text":"from steps"}]}]}`)
	provider := newInteractionTranscriptionTestProvider(server.URL)

	response, bifrostErr := provider.Transcription(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), schemas.Key{Value: *schemas.NewSecretVar("test-key")}, &schemas.BifrostTranscriptionRequest{
		Provider: schemas.Gemini,
		Model:    geminiInteractionTranscriptionModel,
		Input:    &schemas.TranscriptionInput{File: interactionTranscriptionTestAudio},
	})
	require.Nil(t, bifrostErr)
	assert.Equal(t, "from output text", response.Text)
	assert.Nil(t, response.Usage)
}

func TestGeminiTranscriptionOtherModelsKeepGenerateContent(t *testing.T) {
	server, captured := newInteractionTranscriptionServer(t, "application/json",
		`{"candidates":[{"content":{"parts":[{"text":"legacy"}]}}]}`)
	provider := newInteractionTranscriptionTestProvider(server.URL)

	_, bifrostErr := provider.Transcription(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), schemas.Key{Value: *schemas.NewSecretVar("test-key")}, &schemas.BifrostTranscriptionRequest{
		Provider: schemas.Gemini,
		Model:    "gemini-2.5-flash",
		Input:    &schemas.TranscriptionInput{File: interactionTranscriptionTestAudio},
	})
	require.Nil(t, bifrostErr)
	assert.Equal(t, "/models/gemini-2.5-flash:generateContent", captured.path)
}

func TestGeminiInteractionTranscriptionStreamCompletes(t *testing.T) {
	server, captured := newInteractionTranscriptionServer(t, "text/event-stream",
		"data: {\"event_type\":\"content.delta\",\"delta\":{\"type\":\"text\",\"text\":\"hel\"}}\n\n"+
			"data: {\"event_type\":\"content.delta\",\"delta\":{\"type\":\"text\",\"text\":\"lo\"}}\n\n"+
			"data: {\"event_type\":\"interaction.completed\",\"interaction\":{\"status\":\"completed\",\"usage\":{\"total_input_tokens\":7,\"total_output_tokens\":2,\"total_tokens\":9}}}\n\n")
	provider := newInteractionTranscriptionTestProvider(server.URL)

	stream, bifrostErr := provider.TranscriptionStream(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), passthroughPostHookRunner, nil, schemas.Key{Value: *schemas.NewSecretVar("test-key")}, &schemas.BifrostTranscriptionRequest{
		Provider: schemas.Gemini,
		Model:    geminiInteractionTranscriptionModel,
		Input:    &schemas.TranscriptionInput{File: interactionTranscriptionTestAudio},
	})
	require.Nil(t, bifrostErr)
	chunks := collectTranscriptionStream(stream)

	assert.Equal(t, "/interactions", captured.path)
	assert.Equal(t, "text/event-stream", captured.accept)
	assert.Equal(t, true, captured.body["stream"])

	require.Len(t, chunks, 3)
	for i, want := range []string{"hel", "lo"} {
		require.Nil(t, chunks[i].BifrostError)
		require.NotNil(t, chunks[i].BifrostTranscriptionStreamResponse)
		assert.Equal(t, schemas.TranscriptionStreamResponseTypeDelta, chunks[i].BifrostTranscriptionStreamResponse.Type)
		assert.Equal(t, want, *chunks[i].BifrostTranscriptionStreamResponse.Delta)
	}
	done := chunks[2].BifrostTranscriptionStreamResponse
	require.NotNil(t, done)
	assert.Equal(t, schemas.TranscriptionStreamResponseTypeDone, done.Type)
	assert.Equal(t, "hello", done.Text)
	require.NotNil(t, done.Usage)
	assert.Equal(t, 7, *done.Usage.InputTokens)
	assert.Equal(t, 2, *done.Usage.OutputTokens)
	assert.Equal(t, 9, *done.Usage.TotalTokens)
}

func TestGeminiInteractionTranscriptionStreamErrorEvent(t *testing.T) {
	server, _ := newInteractionTranscriptionServer(t, "text/event-stream",
		"data: {\"event_type\":\"content.delta\",\"delta\":{\"type\":\"text\",\"text\":\"par\"}}\n\n"+
			"data: {\"event_type\":\"error\",\"error\":{\"code\":\"internal\",\"message\":\"audio decode failed\"}}\n\n")
	provider := newInteractionTranscriptionTestProvider(server.URL)

	stream, bifrostErr := provider.TranscriptionStream(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), passthroughPostHookRunner, nil, schemas.Key{Value: *schemas.NewSecretVar("test-key")}, &schemas.BifrostTranscriptionRequest{
		Provider: schemas.Gemini,
		Model:    geminiInteractionTranscriptionModel,
		Input:    &schemas.TranscriptionInput{File: interactionTranscriptionTestAudio},
	})
	require.Nil(t, bifrostErr)
	chunks := collectTranscriptionStream(stream)

	require.NotEmpty(t, chunks)
	last := chunks[len(chunks)-1]
	require.NotNil(t, last.BifrostError, "stream must end with the upstream error")
	assert.Equal(t, "audio decode failed", last.BifrostError.Error.Message)
	for _, chunk := range chunks {
		if chunk.BifrostTranscriptionStreamResponse != nil {
			assert.NotEqual(t, schemas.TranscriptionStreamResponseTypeDone, chunk.BifrostTranscriptionStreamResponse.Type)
		}
	}
}

// A body that closes before interaction.completed must surface as an error, not a
// partial transcript marked done.
func TestGeminiInteractionTranscriptionStreamEOFBeforeCompletedIsError(t *testing.T) {
	server, _ := newInteractionTranscriptionServer(t, "text/event-stream",
		"data: {\"event_type\":\"content.delta\",\"delta\":{\"type\":\"text\",\"text\":\"partial\"}}\n\n")
	provider := newInteractionTranscriptionTestProvider(server.URL)

	stream, bifrostErr := provider.TranscriptionStream(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), passthroughPostHookRunner, nil, schemas.Key{Value: *schemas.NewSecretVar("test-key")}, &schemas.BifrostTranscriptionRequest{
		Provider: schemas.Gemini,
		Model:    geminiInteractionTranscriptionModel,
		Input:    &schemas.TranscriptionInput{File: interactionTranscriptionTestAudio},
	})
	require.Nil(t, bifrostErr)
	chunks := collectTranscriptionStream(stream)

	require.NotEmpty(t, chunks)
	last := chunks[len(chunks)-1]
	require.NotNil(t, last.BifrostError, "truncated stream must end with an error")
	assert.Contains(t, last.BifrostError.Error.Message, "ended before interaction.completed")
	for _, chunk := range chunks {
		if chunk.BifrostTranscriptionStreamResponse != nil {
			assert.NotEqual(t, schemas.TranscriptionStreamResponseTypeDone, chunk.BifrostTranscriptionStreamResponse.Type)
		}
	}
}

func TestIsGeminiInteractionStreamCompleted(t *testing.T) {
	assert.False(t, isGeminiInteractionStreamCompleted(nil))
	assert.False(t, isGeminiInteractionStreamCompleted(&geminiInteractionStreamEvent{EventType: "content.delta"}))
	assert.True(t, isGeminiInteractionStreamCompleted(&geminiInteractionStreamEvent{EventType: "interaction.completed"}))
	assert.True(t, isGeminiInteractionStreamCompleted(&geminiInteractionStreamEvent{Interaction: &geminiInteractionStreamInteraction{Status: "completed"}}))
	assert.False(t, isGeminiInteractionStreamCompleted(&geminiInteractionStreamEvent{Interaction: &geminiInteractionStreamInteraction{Status: "in_progress"}}))
}

// An upstream that keeps the body open after interaction.completed must still end the
// stream with the done chunk, not wait for the idle timeout and report an error.
func TestGeminiInteractionTranscriptionStreamStopsAtCompletedWhileBodyOpen(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"event_type\":\"content.delta\",\"delta\":{\"type\":\"text\",\"text\":\"full \"}}\n\n" +
			"data: {\"event_type\":\"content.delta\",\"delta\":{\"type\":\"text\",\"text\":\"transcript\"}}\n\n" +
			"data: {\"event_type\":\"interaction.completed\",\"interaction\":{\"status\":\"completed\",\"usage\":{\"total_input_tokens\":3,\"total_output_tokens\":2,\"total_tokens\":5}}}\n\n"))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		select { // hold the body open past the idle timeout
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	defer close(release)

	provider := NewGeminiProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL, StreamIdleTimeoutInSeconds: 1},
	}, testNoopLogger{})
	stream, bifrostErr := provider.TranscriptionStream(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), passthroughPostHookRunner, nil, schemas.Key{Value: *schemas.NewSecretVar("test-key")}, &schemas.BifrostTranscriptionRequest{
		Provider: schemas.Gemini,
		Model:    geminiInteractionTranscriptionModel,
		Input:    &schemas.TranscriptionInput{File: interactionTranscriptionTestAudio},
	})
	require.Nil(t, bifrostErr)
	chunks := collectTranscriptionStream(stream)

	require.NotEmpty(t, chunks)
	last := chunks[len(chunks)-1]
	require.Nil(t, last.BifrostError, "finished stream must not end in an error")
	require.NotNil(t, last.BifrostTranscriptionStreamResponse)
	assert.Equal(t, schemas.TranscriptionStreamResponseTypeDone, last.BifrostTranscriptionStreamResponse.Type)
	assert.Equal(t, "full transcript", last.BifrostTranscriptionStreamResponse.Text)
	require.NotNil(t, last.BifrostTranscriptionStreamResponse.Usage)
	assert.Equal(t, 5, *last.BifrostTranscriptionStreamResponse.Usage.TotalTokens)
}

// A panic in a post-hook on the stream goroutine must be recovered into a terminal
// 500 error chunk instead of crashing the process.
func TestGeminiInteractionTranscriptionStreamRecoversPostHookPanic(t *testing.T) {
	server, _ := newInteractionTranscriptionServer(t, "text/event-stream",
		"data: {\"event_type\":\"content.delta\",\"delta\":{\"type\":\"text\",\"text\":\"boom\"}}\n\n"+
			"data: {\"event_type\":\"interaction.completed\",\"interaction\":{\"status\":\"completed\"}}\n\n")
	provider := newInteractionTranscriptionTestProvider(server.URL)
	panickingPostHook := func(_ *schemas.BifrostContext, response *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
		if response != nil {
			panic("post-hook failure")
		}
		return response, err
	}

	stream, bifrostErr := provider.TranscriptionStream(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), panickingPostHook, nil, schemas.Key{Value: *schemas.NewSecretVar("test-key")}, &schemas.BifrostTranscriptionRequest{
		Provider: schemas.Gemini,
		Model:    geminiInteractionTranscriptionModel,
		Input:    &schemas.TranscriptionInput{File: interactionTranscriptionTestAudio},
	})
	require.Nil(t, bifrostErr)
	chunks := collectTranscriptionStream(stream)

	require.NotEmpty(t, chunks)
	last := chunks[len(chunks)-1]
	require.NotNil(t, last.BifrostError, "panic must surface as a terminal error chunk")
	assert.Equal(t, providerUtils.ErrStreamPanicMessage, last.BifrostError.Error.Message)
	require.NotNil(t, last.BifrostError.StatusCode)
	assert.Equal(t, 500, *last.BifrostError.StatusCode)
}

// store is a top-level Interactions field: an explicit false (bool or the "false"
// string a multipart form sends) must be serialized, and an absent store omitted.
func TestToGeminiInteractionTranscriptionRequestStore(t *testing.T) {
	for _, test := range []struct {
		name  string
		extra map[string]interface{}
		want  string
	}{
		{name: "bool false", extra: map[string]interface{}{"store": false}, want: `false`},
		{name: "form string false", extra: map[string]interface{}{"store": "false"}, want: `false`},
		{name: "bool true", extra: map[string]interface{}{"store": true}, want: `true`},
		{name: "absent", extra: nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			converted := ToGeminiInteractionTranscriptionRequest(&schemas.BifrostTranscriptionRequest{
				Model:  geminiInteractionTranscriptionModel,
				Input:  &schemas.TranscriptionInput{File: interactionTranscriptionTestAudio},
				Params: &schemas.TranscriptionParameters{ExtraParams: test.extra},
			}, false)
			body, err := providerUtils.MarshalProviderRequest(converted)
			require.NoError(t, err)

			var decoded map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(body, &decoded))
			store, present := decoded["store"]
			if test.want == "" {
				assert.False(t, present, "store must be omitted when not set: %s", body)
				return
			}
			require.True(t, present, "store must be sent: %s", body)
			assert.Equal(t, test.want, string(store))
			_, inConfig := decoded["generation_config"]
			assert.False(t, inConfig, "store must not go into transcription_config: %s", body)
		})
	}
}

// diarization_mode is merged into a copy of the caller's mode map, so the request's
// ExtraParams stay unchanged across retries and fallbacks.
func TestToGeminiInteractionTranscriptionRequestDoesNotMutateCallerMode(t *testing.T) {
	callerMode := map[string]interface{}{"type": "verbatim"}
	request := &schemas.BifrostTranscriptionRequest{
		Model: geminiInteractionTranscriptionModel,
		Input: &schemas.TranscriptionInput{File: interactionTranscriptionTestAudio},
		Params: &schemas.TranscriptionParameters{
			ExtraParams: map[string]interface{}{
				"mode":             callerMode,
				"diarization_mode": "speaker_labels",
			},
		},
	}

	for attempt := 0; attempt < 2; attempt++ {
		converted := ToGeminiInteractionTranscriptionRequest(request, false)
		require.NotNil(t, converted.GenerationConfig)
		assert.Equal(t,
			map[string]interface{}{"type": "verbatim", "diarization_mode": "speaker_labels"},
			converted.GenerationConfig.TranscriptionConfig["mode"], "attempt %d", attempt)
	}
	assert.Equal(t, map[string]interface{}{"type": "verbatim"}, callerMode, "caller's mode map must not be mutated")
}

func BenchmarkBuildGeminiInteractionTranscriptionRequestBody(b *testing.B) {
	for _, test := range []struct {
		name    string
		useRaw  bool
		request *schemas.BifrostTranscriptionRequest
	}{
		{
			name:    "typed",
			request: &schemas.BifrostTranscriptionRequest{Model: geminiInteractionTranscriptionModel, Input: &schemas.TranscriptionInput{File: []byte("audio")}},
		},
		{
			name:   "raw",
			useRaw: true,
			request: &schemas.BifrostTranscriptionRequest{
				Model:          geminiInteractionTranscriptionModel,
				RawRequestBody: []byte(`{"model":"gemini-3.5-transcribe","input":[{"type":"audio","data":"raw-audio","mime_type":"audio/wav"}]}`),
			},
		},
	} {
		b.Run(test.name, func(b *testing.B) {
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			ctx.SetValue(schemas.BifrostContextKeyUseRawRequestBody, test.useRaw)
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if _, bifrostErr := buildGeminiInteractionTranscriptionRequestBody(ctx, test.request, false); bifrostErr != nil {
					b.Fatal(bifrostErr)
				}
			}
		})
	}
}

// BenchmarkToGeminiInteractionTranscriptionRequest measures building the
// Interactions body (convert + marshal) against the legacy :generateContent body
// for the same audio, which is what gemini-3.5-transcribe was sent before.
func BenchmarkToGeminiInteractionTranscriptionRequest(b *testing.B) {
	audio := make([]byte, 64*1024)
	copy(audio, "RIFF\x00\x00\x00\x00WAVEfmt ")
	minimal := &schemas.BifrostTranscriptionRequest{
		Provider: schemas.Gemini,
		Model:    geminiInteractionTranscriptionModel,
		Input:    &schemas.TranscriptionInput{File: audio},
	}
	full := &schemas.BifrostTranscriptionRequest{
		Provider: schemas.Gemini,
		Model:    geminiInteractionTranscriptionModel,
		Input:    &schemas.TranscriptionInput{File: audio},
		Params: &schemas.TranscriptionParameters{
			Prompt:                 schemas.Ptr("Transcribe the meeting."),
			Language:               schemas.Ptr("en"),
			TimestampGranularities: []string{"word", "segment"},
			ExtraParams: map[string]interface{}{
				"diarization_mode":  "speaker_labels",
				"custom_vocabulary": []string{"Bifrost", "Gemini"},
			},
		},
	}

	for _, test := range []struct {
		name    string
		convert func(*schemas.BifrostTranscriptionRequest) interface{}
		request *schemas.BifrostTranscriptionRequest
	}{
		{"legacy_generate_content/minimal", func(r *schemas.BifrostTranscriptionRequest) interface{} { return ToGeminiTranscriptionRequest(r) }, minimal},
		{"interactions/minimal", func(r *schemas.BifrostTranscriptionRequest) interface{} {
			return ToGeminiInteractionTranscriptionRequest(r, false)
		}, minimal},
		{"legacy_generate_content/full_params", func(r *schemas.BifrostTranscriptionRequest) interface{} { return ToGeminiTranscriptionRequest(r) }, full},
		{"interactions/full_params", func(r *schemas.BifrostTranscriptionRequest) interface{} {
			return ToGeminiInteractionTranscriptionRequest(r, false)
		}, full},
	} {
		b.Run(test.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if _, err := providerUtils.MarshalProviderRequest(test.convert(test.request)); err != nil {
					b.Fatal(err)
				}
			}
		})
	}

	// Routing check every Gemini transcription now pays, including non-3.5 models.
	b.Run("route_check/other_model", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if isGeminiInteractionTranscriptionModel("gemini-2.5-flash") {
				b.Fatal("unexpected interactions route")
			}
		}
	})
}

// Gemini bills the prompt even when it returns no transcript (for example a
// speechless clip), so usage must survive an empty-text response.
func TestToBifrostTranscriptionResponseKeepsUsageWhenTextEmpty(t *testing.T) {
	resp := &GenerateContentResponse{
		Candidates: []*Candidate{{Content: &Content{Role: string(RoleModel), Parts: []*Part{{Text: ""}}}}},
		UsageMetadata: &GenerateContentResponseUsageMetadata{
			PromptTokenCount: 12,
			TotalTokenCount:  12,
		},
	}

	got := resp.ToBifrostTranscriptionResponse()
	if got.Text != "" {
		t.Fatalf("text = %q, want empty", got.Text)
	}
	if got.Usage == nil {
		t.Fatal("usage dropped for an empty transcript")
	}
	if got.Usage.InputTokens == nil || *got.Usage.InputTokens != 12 {
		t.Errorf("input tokens = %v, want 12", got.Usage.InputTokens)
	}

	genai := ToGeminiTranscriptionResponse(got)
	if genai.UsageMetadata == nil || genai.UsageMetadata.PromptTokenCount != 12 {
		t.Errorf("genai usageMetadata = %+v, want promptTokenCount 12", genai.UsageMetadata)
	}
}

// The transcription converter runs once per retry/fallback attempt on the same
// Bifrost request and removes safety_settings, cached_content and labels from the
// outbound ExtraParams. It used to alias the source map, so the second attempt was
// sent without any of them. Regression for issue #7826.
func TestToGeminiTranscriptionRequest_ExtraParamsSurviveRetries(t *testing.T) {
	bifrostReq := &schemas.BifrostTranscriptionRequest{
		Provider: schemas.Gemini,
		Model:    "gemini-2.5-flash",
		Input:    &schemas.TranscriptionInput{File: []byte("not-really-audio"), Filename: "sample.mp3"},
		Params: &schemas.TranscriptionParameters{
			ExtraParams: map[string]interface{}{
				"safety_settings": []interface{}{
					map[string]interface{}{
						"category":  "HARM_CATEGORY_HARASSMENT",
						"threshold": "BLOCK_NONE",
					},
				},
				"cached_content":     "cachedContents/abc123",
				"labels":             map[string]interface{}{"team": "platform"},
				"custom_passthrough": "keep-me",
			},
		},
	}

	for attempt := 1; attempt <= 3; attempt++ {
		geminiReq := ToGeminiTranscriptionRequest(bifrostReq)
		require.NotNil(t, geminiReq, "attempt %d", attempt)

		require.Len(t, geminiReq.SafetySettings, 1, "attempt %d: safetySettings", attempt)
		assert.Equal(t, "HARM_CATEGORY_HARASSMENT", geminiReq.SafetySettings[0].Category, "attempt %d", attempt)
		assert.Equal(t, "BLOCK_NONE", geminiReq.SafetySettings[0].Threshold, "attempt %d", attempt)
		assert.Equal(t, "cachedContents/abc123", geminiReq.CachedContent, "attempt %d: cachedContent", attempt)
		assert.Equal(t, map[string]string{"team": "platform"}, geminiReq.Labels, "attempt %d: labels", attempt)
		assert.Equal(t, map[string]interface{}{"custom_passthrough": "keep-me"}, geminiReq.GetExtraParams(), "attempt %d: wire extra params", attempt)
	}

	// The Bifrost request itself must be left intact for the next attempt.
	assert.Contains(t, bifrostReq.Params.ExtraParams, "safety_settings")
	assert.Contains(t, bifrostReq.Params.ExtraParams, "cached_content")
	assert.Contains(t, bifrostReq.Params.ExtraParams, "labels")
	assert.Len(t, bifrostReq.Params.ExtraParams, 4)
}
