package soniox_test

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	ws "github.com/fasthttp/websocket"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/providers/soniox"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func newTestProvider(t *testing.T, baseURL string) *soniox.SonioxProvider {
	t.Helper()
	provider, err := soniox.NewSonioxProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: baseURL, AllowPrivateNetwork: true},
	}, bifrost.NewNoOpLogger())
	require.NoError(t, err)
	return provider
}

func newTestContext(t *testing.T) *schemas.BifrostContext {
	t.Helper()
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	t.Cleanup(ctx.Cancel)
	return ctx
}

func testKey() schemas.Key {
	return schemas.Key{ID: "key-1", Models: schemas.WhiteList{"*"}, Value: *schemas.NewSecretVar("test-key")}
}

func passThroughPostHook(_ *schemas.BifrostContext, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
	return result, err
}

func speechRequest(params *schemas.SpeechParameters) *schemas.BifrostSpeechRequest {
	return &schemas.BifrostSpeechRequest{
		Provider: schemas.Soniox,
		Model:    "tts-rt-v2",
		Input:    &schemas.SpeechInput{Input: "Hello there."},
		Params:   params,
	}
}

func TestToSonioxSpeechRequest(t *testing.T) {
	t.Run("a bare request gets Soniox's required audio_format", func(t *testing.T) {
		req := soniox.ToSonioxSpeechRequest(speechRequest(nil))
		assert.Equal(t, "tts-rt-v2", req.Model)
		assert.Equal(t, "Hello there.", req.Text)
		assert.Equal(t, "mp3", req.AudioFormat)
		assert.Empty(t, req.Voice)
		assert.Empty(t, req.Language)
	})

	t.Run("OpenAI fields map onto Soniox fields", func(t *testing.T) {
		voice, language, speed := "Daniel", "hi", 1.2
		req := soniox.ToSonioxSpeechRequest(speechRequest(&schemas.SpeechParameters{
			VoiceConfig:    &schemas.SpeechVoiceInput{Voice: &voice},
			LanguageCode:   &language,
			ResponseFormat: "pcm",
			Speed:          &speed,
		}))
		assert.Equal(t, "Daniel", req.Voice)
		assert.Equal(t, "hi", req.Language)
		assert.Equal(t, "pcm_s16le", req.AudioFormat)
		require.NotNil(t, req.Speed)
		assert.Equal(t, 1.2, *req.Speed)
	})

	t.Run("a native Soniox format passes through", func(t *testing.T) {
		req := soniox.ToSonioxSpeechRequest(speechRequest(&schemas.SpeechParameters{ResponseFormat: "pcm_mulaw"}))
		assert.Equal(t, "pcm_mulaw", req.AudioFormat)
	})

	t.Run("multi-speaker voices are dropped", func(t *testing.T) {
		req := soniox.ToSonioxSpeechRequest(speechRequest(&schemas.SpeechParameters{
			VoiceConfig: &schemas.SpeechVoiceInput{MultiVoiceConfig: []schemas.VoiceConfig{{Speaker: "a", Voice: "Daniel"}}},
		}))
		assert.Empty(t, req.Voice)
	})

	t.Run("extra params become typed fields without mutating the caller's map", func(t *testing.T) {
		extra := map[string]interface{}{
			"sample_rate":         24000,
			"bitrate":             128000,
			"reduce_silence":      "true",
			"client_reference_id": "ref-1",
			"unmapped":            "kept",
		}
		req := soniox.ToSonioxSpeechRequest(speechRequest(&schemas.SpeechParameters{ExtraParams: extra}))
		require.NotNil(t, req.SampleRate)
		assert.Equal(t, 24000, *req.SampleRate)
		require.NotNil(t, req.Bitrate)
		assert.Equal(t, 128000, *req.Bitrate)
		require.NotNil(t, req.ReduceSilence)
		assert.True(t, *req.ReduceSilence)
		require.NotNil(t, req.ClientReferenceID)
		assert.Equal(t, "ref-1", *req.ClientReferenceID)
		assert.Equal(t, map[string]interface{}{"unmapped": "kept"}, req.ExtraParams)
		assert.Len(t, extra, 5, "a retry or fallback must still see the original params")
	})

	assert.Nil(t, soniox.ToSonioxSpeechRequest(nil))
}

func TestToSonioxTranscriptionRequest(t *testing.T) {
	language := "hi"
	extra := map[string]interface{}{
		"enable_speaker_diarization":     "true",
		"enable_language_identification": true,
		"client_reference_id":            "ref-2",
		"context":                        "kept",
	}
	req := soniox.ToSonioxTranscriptionRequest(&schemas.BifrostTranscriptionRequest{
		Provider: schemas.Soniox,
		Model:    "stt-async-v6",
		Input:    &schemas.TranscriptionInput{File: []byte("audio"), Filename: "call.wav"},
		Params:   &schemas.TranscriptionParameters{Language: &language, ExtraParams: extra},
	})
	require.NotNil(t, req)
	assert.Equal(t, "stt-async-v6", req.Model)
	assert.Equal(t, []byte("audio"), req.File)
	assert.Equal(t, "call.wav", req.Filename)
	assert.Equal(t, []string{"hi"}, req.LanguageHints)
	require.NotNil(t, req.EnableSpeakerDiarization)
	assert.True(t, *req.EnableSpeakerDiarization)
	require.NotNil(t, req.EnableLanguageIdentification)
	assert.True(t, *req.EnableLanguageIdentification)
	require.NotNil(t, req.ClientReferenceID)
	assert.Equal(t, "ref-2", *req.ClientReferenceID)
	assert.Equal(t, map[string]interface{}{"context": "kept"}, req.ExtraParams)
	assert.Len(t, extra, 4, "a retry or fallback must still see the original params")

	assert.Nil(t, soniox.ToSonioxTranscriptionRequest(nil))
}

func TestToBifrostTranscriptionResponse(t *testing.T) {
	speaker, language := "1", "hi"

	t.Run("sub-word tokens regroup into words with timing and speaker", func(t *testing.T) {
		durationMs := 4040
		response := soniox.ToBifrostTranscriptionResponse(&soniox.SonioxTranscript{
			Text: "Hello, world",
			Tokens: []soniox.SonioxToken{
				{Text: "Hel", StartMs: 210, EndMs: 270, Speaker: &speaker, Language: &language},
				{Text: "lo", StartMs: 270, EndMs: 330},
				{Text: ",", StartMs: 330, EndMs: 360},
				{Text: " wor", StartMs: 450, EndMs: 510, Speaker: &speaker},
				{Text: "ld", StartMs: 510, EndMs: 600},
			},
		}, &durationMs)
		assert.Equal(t, "Hello, world", response.Text)
		require.Len(t, response.Words, 2)
		assert.Equal(t, "Hello,", response.Words[0].Word)
		assert.Equal(t, 0.21, response.Words[0].Start)
		assert.Equal(t, 0.36, response.Words[0].End)
		assert.Equal(t, &speaker, response.Words[0].Speaker)
		assert.Equal(t, "world", response.Words[1].Word)
		assert.Equal(t, 0.45, response.Words[1].Start)
		assert.Equal(t, 0.6, response.Words[1].End)
		require.NotNil(t, response.Language)
		assert.Equal(t, "hi", *response.Language)
		require.NotNil(t, response.Duration)
		assert.Equal(t, 4.04, *response.Duration)
		require.NotNil(t, response.Usage)
		assert.Equal(t, "duration", response.Usage.Type)
		require.NotNil(t, response.Usage.Seconds)
		assert.Equal(t, 4.04, *response.Usage.Seconds)
	})

	t.Run("a space-only token does not produce an empty word", func(t *testing.T) {
		response := soniox.ToBifrostTranscriptionResponse(&soniox.SonioxTranscript{
			Text:   "abc",
			Tokens: []soniox.SonioxToken{{Text: " ", StartMs: 0, EndMs: 10}, {Text: "abc", StartMs: 10, EndMs: 90}},
		}, nil)
		require.Len(t, response.Words, 1)
		assert.Equal(t, "abc", response.Words[0].Word)
		assert.Equal(t, 0.01, response.Words[0].Start)
		require.NotNil(t, response.Duration)
		assert.Equal(t, 0.09, *response.Duration, "without a job duration the last token end is used")
		assert.Nil(t, response.Usage, "duration usage needs the job's audio_duration_ms")
	})

	t.Run("an empty transcript has no words", func(t *testing.T) {
		response := soniox.ToBifrostTranscriptionResponse(&soniox.SonioxTranscript{}, nil)
		assert.Empty(t, response.Text)
		assert.Nil(t, response.Words)
		assert.Nil(t, response.Duration)
	})

	assert.Nil(t, soniox.ToBifrostTranscriptionResponse(nil, nil))
}

func TestSonioxSpeech(t *testing.T) {
	t.Run("posts the mapped body and returns the audio", func(t *testing.T) {
		var gotBody map[string]interface{}
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, http.MethodPost, r.Method)
			assert.Equal(t, "/tts", r.URL.Path)
			assert.Equal(t, "Bearer test-key", r.Header.Get("Authorization"))
			assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&gotBody))
			w.Header().Set("Content-Type", "audio/mpeg")
			_, _ = w.Write([]byte("ID3-audio-bytes"))
		}))
		defer server.Close()

		voice, language := "Daniel", "en"
		response, bifrostErr := newTestProvider(t, server.URL).Speech(newTestContext(t), testKey(), speechRequest(&schemas.SpeechParameters{
			VoiceConfig:  &schemas.SpeechVoiceInput{Voice: &voice},
			LanguageCode: &language,
		}))
		require.Nil(t, bifrostErr)
		assert.Equal(t, []byte("ID3-audio-bytes"), response.Audio)
		assert.Equal(t, map[string]interface{}{
			"model":        "tts-rt-v2",
			"voice":        "Daniel",
			"language":     "en",
			"audio_format": "mp3",
			"text":         "Hello there.",
		}, gotBody)
	})

	t.Run("a Soniox error keeps its status and message", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error_code":400,"error_message":"Missing required field: language","error_type":"invalid_request"}`))
		}))
		defer server.Close()

		_, bifrostErr := newTestProvider(t, server.URL).Speech(newTestContext(t), testKey(), speechRequest(nil))
		require.NotNil(t, bifrostErr)
		require.NotNil(t, bifrostErr.StatusCode)
		assert.Equal(t, http.StatusBadRequest, *bifrostErr.StatusCode)
		assert.Equal(t, "Missing required field: language", bifrostErr.Error.Message)
	})
}

func TestSonioxSpeechStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/tts", r.URL.Path)
		assert.Equal(t, "Bearer test-key", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "audio/pcm")
		_, _ = w.Write([]byte("pcm-one"))
		w.(http.Flusher).Flush()
		time.Sleep(50 * time.Millisecond)
		_, _ = w.Write([]byte("pcm-two"))
	}))
	defer server.Close()

	stream, bifrostErr := newTestProvider(t, server.URL).SpeechStream(newTestContext(t), passThroughPostHook, func(context.Context) {}, testKey(), speechRequest(&schemas.SpeechParameters{ResponseFormat: "pcm"}))
	require.Nil(t, bifrostErr)

	var audio []byte
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
				audio = append(audio, chunk.BifrostSpeechStreamResponse.Audio...)
			case schemas.SpeechStreamResponseTypeDone:
				done = chunk.BifrostSpeechStreamResponse
			}
		case <-timeout:
			t.Fatal("speech stream did not finish")
		}
	}
	assert.Equal(t, "pcm-onepcm-two", string(audio))
	require.NotNil(t, done, "the stream must end with a done chunk")
	// Soniox reports no usage, so the done chunk carries the billable characters of the input.
	require.NotNil(t, done.Usage, "the done chunk must carry usage")
	assert.Equal(t, len("Hello there."), done.Usage.InputChars)
}

// transcriptionServer is a fake Soniox REST API that records every call it receives.
type transcriptionServer struct {
	*httptest.Server
	mu             sync.Mutex
	calls          []string
	uploadFilename string
	uploadedBytes  []byte
	createBody     map[string]interface{}
	polls          int
	deleteGate     chan struct{} // held DELETEs wait on it; nil when ungated
}

func (s *transcriptionServer) callList() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}

func (s *transcriptionServer) called(call string) bool {
	return slices.Contains(s.callList(), call)
}

// gateDeletes holds every DELETE until the returned release is called.
func (s *transcriptionServer) gateDeletes() (release func()) {
	gate := make(chan struct{})
	s.mu.Lock()
	s.deleteGate = gate
	s.mu.Unlock()
	var once sync.Once
	return func() { once.Do(func() { close(gate) }) }
}

func (s *transcriptionServer) waitForDeleteGate() {
	s.mu.Lock()
	gate := s.deleteGate
	s.mu.Unlock()
	if gate != nil {
		<-gate
	}
}

// newTranscriptionServer serves one job. The job reports "processing" for pollsBeforeDone
// polls and then jobStatus; "processing" forever when jobStatus is "processing".
func newTranscriptionServer(t *testing.T, pollsBeforeDone int, jobStatus string) *transcriptionServer {
	t.Helper()
	s := &transcriptionServer{}
	mux := http.NewServeMux()
	record := func(r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.calls = append(s.calls, r.Method+" "+r.URL.Path)
		assert.Equal(t, "Bearer test-key", r.Header.Get("Authorization"))
	}
	writeJSON := func(w http.ResponseWriter, status int, body string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}

	mux.HandleFunc("POST /v1/files", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		file, header, err := r.FormFile("file")
		if !assert.NoError(t, err) {
			writeJSON(w, http.StatusBadRequest, `{}`)
			return
		}
		defer file.Close()
		data, _ := io.ReadAll(file)
		s.mu.Lock()
		s.uploadFilename, s.uploadedBytes = header.Filename, data
		s.mu.Unlock()
		writeJSON(w, http.StatusCreated, `{"id":"file-1","filename":"`+header.Filename+`","size":5}`)
	})
	mux.HandleFunc("POST /v1/transcriptions", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		var body map[string]interface{}
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		s.mu.Lock()
		s.createBody = body
		s.mu.Unlock()
		writeJSON(w, http.StatusCreated, `{"id":"job-1","status":"queued","model":"stt-async-v6","audio_duration_ms":null}`)
	})
	mux.HandleFunc("GET /v1/transcriptions/job-1", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		s.mu.Lock()
		s.polls++
		polls := s.polls
		s.mu.Unlock()
		switch {
		case jobStatus == "processing" || polls <= pollsBeforeDone:
			writeJSON(w, http.StatusOK, `{"id":"job-1","status":"processing"}`)
		case jobStatus == "error":
			writeJSON(w, http.StatusOK, `{"id":"job-1","status":"error","error_type":"invalid_audio_file","error_message":"Invalid audio file: Error analyzing the file."}`)
		default:
			writeJSON(w, http.StatusOK, `{"id":"job-1","status":"completed","audio_duration_ms":4040}`)
		}
	})
	mux.HandleFunc("GET /v1/transcriptions/job-1/transcript", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		writeJSON(w, http.StatusOK, `{"id":"job-1","text":"Hello, world","tokens":[
			{"text":"Hel","start_ms":210,"end_ms":270,"confidence":0.99,"speaker":"1","language":"en"},
			{"text":"lo,","start_ms":270,"end_ms":360,"confidence":0.98,"speaker":"1","language":"en"},
			{"text":" world","start_ms":450,"end_ms":600,"confidence":0.97,"speaker":"2","language":"en"}]}`)
	})
	mux.HandleFunc("DELETE /v1/transcriptions/job-1", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		s.waitForDeleteGate()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("DELETE /v1/files/file-1", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		s.waitForDeleteGate()
		w.WriteHeader(http.StatusNoContent)
	})

	s.Server = httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

func transcriptionRequest() *schemas.BifrostTranscriptionRequest {
	language := "en"
	return &schemas.BifrostTranscriptionRequest{
		Provider: schemas.Soniox,
		Model:    "stt-async-v6",
		Input:    &schemas.TranscriptionInput{File: []byte("audio"), Filename: "call.wav"},
		Params: &schemas.TranscriptionParameters{
			Language:    &language,
			ExtraParams: map[string]interface{}{"enable_speaker_diarization": "true"},
		},
	}
}

func TestSonioxTranscription(t *testing.T) {
	t.Run("uploads, creates, polls, reads the transcript, then deletes both resources", func(t *testing.T) {
		server := newTranscriptionServer(t, 1, "completed")
		response, bifrostErr := newTestProvider(t, server.URL).Transcription(newTestContext(t), testKey(), transcriptionRequest())
		require.Nil(t, bifrostErr)

		// The deletes run in the background after the response.
		require.Eventually(t, func() bool { return len(server.callList()) == 7 }, 2*time.Second, 10*time.Millisecond)
		assert.Equal(t, []string{
			"POST /v1/files",
			"POST /v1/transcriptions",
			"GET /v1/transcriptions/job-1",
			"GET /v1/transcriptions/job-1",
			"GET /v1/transcriptions/job-1/transcript",
			"DELETE /v1/transcriptions/job-1",
			"DELETE /v1/files/file-1",
		}, server.callList())
		assert.Equal(t, "call.wav", server.uploadFilename)
		assert.Equal(t, []byte("audio"), server.uploadedBytes)
		assert.Equal(t, map[string]interface{}{
			"model":                      "stt-async-v6",
			"file_id":                    "file-1",
			"language_hints":             []interface{}{"en"},
			"enable_speaker_diarization": true,
		}, server.createBody)

		assert.Equal(t, "Hello, world", response.Text)
		require.Len(t, response.Words, 2)
		assert.Equal(t, "Hello,", response.Words[0].Word)
		assert.Equal(t, "world", response.Words[1].Word)
		require.NotNil(t, response.Words[1].Speaker)
		assert.Equal(t, "2", *response.Words[1].Speaker)
		require.NotNil(t, response.Language)
		assert.Equal(t, "en", *response.Language)
		require.NotNil(t, response.Usage)
		require.NotNil(t, response.Usage.Seconds)
		assert.Equal(t, 4.04, *response.Usage.Seconds)
	})

	t.Run("a failed job becomes an error and the resources are still deleted", func(t *testing.T) {
		server := newTranscriptionServer(t, 0, "error")
		_, bifrostErr := newTestProvider(t, server.URL).Transcription(newTestContext(t), testKey(), transcriptionRequest())
		require.NotNil(t, bifrostErr)
		require.NotNil(t, bifrostErr.StatusCode)
		assert.Equal(t, http.StatusBadRequest, *bifrostErr.StatusCode)
		assert.Contains(t, bifrostErr.Error.Message, "Invalid audio file")

		require.Eventually(t, func() bool {
			return server.called("DELETE /v1/transcriptions/job-1") && server.called("DELETE /v1/files/file-1")
		}, 2*time.Second, 10*time.Millisecond)
		assert.NotContains(t, server.callList(), "GET /v1/transcriptions/job-1/transcript")
	})

	t.Run("the response does not wait for the deletes", func(t *testing.T) {
		server := newTranscriptionServer(t, 0, "completed")
		release := server.gateDeletes()
		t.Cleanup(release)
		provider := newTestProvider(t, server.URL)
		ctx := newTestContext(t)

		done := make(chan *schemas.BifrostError, 1)
		go func() {
			_, bifrostErr := provider.Transcription(ctx, testKey(), transcriptionRequest())
			done <- bifrostErr
		}()
		select {
		case bifrostErr := <-done:
			require.Nil(t, bifrostErr)
		case <-time.After(5 * time.Second):
			t.Fatal("Transcription waited for the Soniox deletes before returning")
		}

		release()
		require.Eventually(t, func() bool {
			return server.called("DELETE /v1/transcriptions/job-1") && server.called("DELETE /v1/files/file-1")
		}, 2*time.Second, 10*time.Millisecond)
	})

	t.Run("a rejected job creation still deletes the uploaded file", func(t *testing.T) {
		var mu sync.Mutex
		var calls []string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			calls = append(calls, r.Method+" "+r.URL.Path)
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			switch r.Method + " " + r.URL.Path {
			case "POST /v1/files":
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte(`{"id":"file-1","filename":"call.wav","size":5}`))
			case "DELETE /v1/files/file-1":
				w.WriteHeader(http.StatusNoContent)
			default:
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"status_code":400,"error_type":"invalid_request","message":"Invalid request.","validation_errors":[]}`))
			}
		}))
		defer server.Close()

		_, bifrostErr := newTestProvider(t, server.URL).Transcription(newTestContext(t), testKey(), transcriptionRequest())
		require.NotNil(t, bifrostErr)

		require.Eventually(t, func() bool {
			mu.Lock()
			defer mu.Unlock()
			return slices.Contains(calls, "DELETE /v1/files/file-1")
		}, 2*time.Second, 10*time.Millisecond)
		mu.Lock()
		defer mu.Unlock()
		assert.Equal(t, []string{"POST /v1/files", "POST /v1/transcriptions", "DELETE /v1/files/file-1"}, calls)
	})

	t.Run("a job creation Soniox never answered keeps the uploaded file, since the job may exist", func(t *testing.T) {
		for _, tc := range []struct {
			name     string
			create   http.HandlerFunc
			deadline time.Duration // a request deadline, when the case is a timeout
		}{
			{name: "the connection drops", create: func(w http.ResponseWriter, r *http.Request) {
				if conn, _, err := w.(http.Hijacker).Hijack(); err == nil {
					_ = conn.Close()
				}
			}},
			{name: "the request times out", deadline: 200 * time.Millisecond, create: func(w http.ResponseWriter, r *http.Request) {
				time.Sleep(600 * time.Millisecond)
			}},
			{name: "the answer cannot be read", create: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte("not json"))
			}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				var mu sync.Mutex
				var calls []string
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					mu.Lock()
					calls = append(calls, r.Method+" "+r.URL.Path)
					mu.Unlock()
					switch r.Method + " " + r.URL.Path {
					case "POST /v1/files":
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(http.StatusCreated)
						_, _ = w.Write([]byte(`{"id":"file-1","filename":"call.wav","size":5}`))
					case "POST /v1/transcriptions":
						tc.create(w, r)
					default:
						w.WriteHeader(http.StatusNoContent)
					}
				}))
				defer server.Close()
				ctx := newTestContext(t)
				if tc.deadline > 0 {
					ctx = schemas.NewBifrostContext(context.Background(), time.Now().Add(tc.deadline))
					defer ctx.Cancel()
				}

				_, bifrostErr := newTestProvider(t, server.URL).Transcription(ctx, testKey(), transcriptionRequest())
				require.NotNil(t, bifrostErr)

				// A job Soniox accepted may still be processing, and it would be left without its audio.
				assert.Never(t, func() bool {
					mu.Lock()
					defer mu.Unlock()
					return slices.Contains(calls, "DELETE /v1/files/file-1")
				}, 400*time.Millisecond, 20*time.Millisecond)
			})
		}
	})

	t.Run("a rejected upload stops before any job is created", func(t *testing.T) {
		var calls []string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls = append(calls, r.Method+" "+r.URL.Path)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"status_code":401,"error_type":"unauthenticated","message":"Incorrect API key provided.","validation_errors":[]}`))
		}))
		defer server.Close()

		_, bifrostErr := newTestProvider(t, server.URL).Transcription(newTestContext(t), testKey(), transcriptionRequest())
		require.NotNil(t, bifrostErr)
		require.NotNil(t, bifrostErr.StatusCode)
		assert.Equal(t, http.StatusUnauthorized, *bifrostErr.StatusCode)
		assert.Equal(t, []string{"POST /v1/files"}, calls)
	})

	t.Run("a request deadline ends the wait and leaves the unfinished job and its file alone", func(t *testing.T) {
		server := newTranscriptionServer(t, 0, "processing")
		ctx := schemas.NewBifrostContext(context.Background(), time.Now().Add(800*time.Millisecond))
		defer ctx.Cancel()

		_, bifrostErr := newTestProvider(t, server.URL).Transcription(ctx, testKey(), transcriptionRequest())
		require.NotNil(t, bifrostErr)

		// Soniox answers 409 to deleting a job it is still processing, so nothing may be deleted.
		assert.Never(t, func() bool {
			return server.called("DELETE /v1/transcriptions/job-1") || server.called("DELETE /v1/files/file-1")
		}, 300*time.Millisecond, 20*time.Millisecond)
	})

	t.Run("the wait for an unfinished job is bounded by the provider's request timeout", func(t *testing.T) {
		// Polls land at about 0.5s, 1.25s, 2.4s and 4.1s, so the job only finishes after the 1s timeout.
		server := newTranscriptionServer(t, 3, "completed")
		provider, err := soniox.NewSonioxProvider(&schemas.ProviderConfig{
			NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL, AllowPrivateNetwork: true, DefaultRequestTimeoutInSeconds: 1},
		}, bifrost.NewNoOpLogger())
		require.NoError(t, err)

		_, bifrostErr := provider.Transcription(newTestContext(t), testKey(), transcriptionRequest())
		require.NotNil(t, bifrostErr, "the job finished instead of hitting the 1s provider timeout")
		assert.Contains(t, bifrostErr.Error.Message, "did not finish within 1s")
	})

	t.Run("the last sleep ends at the request timeout, not after it", func(t *testing.T) {
		// Polls land at about 0.5s, 1.25s and 2.4s, so an unbounded sleep carries the wait past the 2s timeout.
		server := newTranscriptionServer(t, 0, "processing")
		provider, err := soniox.NewSonioxProvider(&schemas.ProviderConfig{
			NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL, AllowPrivateNetwork: true, DefaultRequestTimeoutInSeconds: 2},
		}, bifrost.NewNoOpLogger())
		require.NoError(t, err)

		started := time.Now()
		_, bifrostErr := provider.Transcription(newTestContext(t), testKey(), transcriptionRequest())

		require.NotNil(t, bifrostErr)
		assert.Contains(t, bifrostErr.Error.Message, "did not finish within 2s")
		assert.Less(t, time.Since(started), 2200*time.Millisecond, "the wait outlasted the request timeout")
	})

	t.Run("a job that finishes at the last poll is returned, not discarded", func(t *testing.T) {
		// With a 1s timeout the polls land at 0.5s and 1s, and the job is done at the second.
		server := newTranscriptionServer(t, 1, "completed")
		provider, err := soniox.NewSonioxProvider(&schemas.ProviderConfig{
			NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL, AllowPrivateNetwork: true, DefaultRequestTimeoutInSeconds: 1},
		}, bifrost.NewNoOpLogger())
		require.NoError(t, err)

		response, bifrostErr := provider.Transcription(newTestContext(t), testKey(), transcriptionRequest())

		require.Nil(t, bifrostErr)
		assert.Equal(t, "Hello, world", response.Text)
	})

	t.Run("a request without audio is refused before any call", func(t *testing.T) {
		server := newTranscriptionServer(t, 0, "completed")
		request := transcriptionRequest()
		request.Input.File = nil
		_, bifrostErr := newTestProvider(t, server.URL).Transcription(newTestContext(t), testKey(), request)
		require.NotNil(t, bifrostErr)
		assert.Empty(t, server.callList())
	})
}

func TestSonioxListModels(t *testing.T) {
	newModelsServer := func(ttsStatus int) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "Bearer test-key", r.Header.Get("Authorization"))
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Path {
			case "/v1/models":
				_, _ = w.Write([]byte(`{"models":[{"id":"stt-rt-v6","aliased_model_id":null,"name":"Speech-to-Text Real-time v6"},{"id":"stt-rt-v5","aliased_model_id":null,"name":"Speech-to-Text Real-time v5"}]}`))
			case "/v1/tts-models":
				w.WriteHeader(ttsStatus)
				if ttsStatus == http.StatusOK {
					_, _ = w.Write([]byte(`{"models":[{"id":"tts-rt-v2","aliased_model_id":null,"name":"TTS RT v2","voices":[{"id":"Daniel"}]}]}`))
				} else {
					_, _ = w.Write([]byte(`{"status_code":500,"error_type":"internal_error","message":"boom","validation_errors":[]}`))
				}
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
	}

	t.Run("merges speech-to-text and text-to-speech models", func(t *testing.T) {
		server := newModelsServer(http.StatusOK)
		defer server.Close()

		response, bifrostErr := newTestProvider(t, server.URL).ListModels(newTestContext(t), []schemas.Key{testKey()}, &schemas.BifrostListModelsRequest{Provider: schemas.Soniox})
		require.Nil(t, bifrostErr)

		var ids []string
		for _, model := range response.Data {
			ids = append(ids, model.ID)
		}
		assert.ElementsMatch(t, []string{"soniox/stt-rt-v6", "soniox/stt-rt-v5", "soniox/tts-rt-v2"}, ids)
	})

	t.Run("a failing half fails the listing with its status", func(t *testing.T) {
		server := newModelsServer(http.StatusInternalServerError)
		defer server.Close()

		_, bifrostErr := newTestProvider(t, server.URL).ListModels(newTestContext(t), []schemas.Key{testKey()}, &schemas.BifrostListModelsRequest{Provider: schemas.Soniox})
		require.NotNil(t, bifrostErr)
		require.NotNil(t, bifrostErr.StatusCode)
		assert.Equal(t, http.StatusInternalServerError, *bifrostErr.StatusCode)
	})
}

func BenchmarkToSonioxSpeechRequest(b *testing.B) {
	voice, language, speed := "Daniel", "hi", 1.1
	request := speechRequest(&schemas.SpeechParameters{
		VoiceConfig:    &schemas.SpeechVoiceInput{Voice: &voice},
		LanguageCode:   &language,
		ResponseFormat: "pcm",
		Speed:          &speed,
		ExtraParams:    map[string]interface{}{"sample_rate": 24000, "reduce_silence": true},
	})
	b.ReportAllocs()
	for b.Loop() {
		_ = soniox.ToSonioxSpeechRequest(request)
	}
}

// BenchmarkToBifrostTranscriptionResponse regroups a transcript of about 2,000 tokens, roughly
// two minutes of speech, into words.
func BenchmarkToBifrostTranscriptionResponse(b *testing.B) {
	speaker := "1"
	tokens := make([]soniox.SonioxToken, 0, 2000)
	for i := 0; i < 1000; i++ {
		tokens = append(tokens,
			soniox.SonioxToken{Text: " wor", StartMs: i * 120, EndMs: i*120 + 60, Speaker: &speaker},
			soniox.SonioxToken{Text: "d", StartMs: i*120 + 60, EndMs: i*120 + 120, Speaker: &speaker})
	}
	transcript := &soniox.SonioxTranscript{Text: "word", Tokens: tokens}
	durationMs := 120000
	b.ReportAllocs()
	for b.Loop() {
		_ = soniox.ToBifrostTranscriptionResponse(transcript, &durationMs)
	}
}

// TestSoniox runs against the live Soniox API: a Hindi sentence is synthesized and then
// transcribed back, so one pass exercises text-to-speech, streaming, async speech-to-text
// and model listing. It is skipped without SONIOX_API_KEY.
func TestSoniox(t *testing.T) {
	apiKey := strings.TrimSpace(os.Getenv("SONIOX_API_KEY"))
	if apiKey == "" {
		t.Skip("Skipping Soniox tests because SONIOX_API_KEY is not set")
	}

	provider, err := soniox.NewSonioxProvider(&schemas.ProviderConfig{}, bifrost.NewNoOpLogger())
	require.NoError(t, err)
	key := schemas.Key{ID: "live", Models: schemas.WhiteList{"*"}, Value: *schemas.NewSecretVar(apiKey)}

	voice, language := "Daniel", "hi"
	text := "नमस्ते, मैं जियो से बोल रहा हूँ।"
	liveSpeech := func(format string) *schemas.BifrostSpeechRequest {
		return &schemas.BifrostSpeechRequest{
			Provider: schemas.Soniox,
			Model:    "tts-rt-v2",
			Input:    &schemas.SpeechInput{Input: text},
			Params: &schemas.SpeechParameters{
				VoiceConfig:    &schemas.SpeechVoiceInput{Voice: &voice},
				LanguageCode:   &language,
				ResponseFormat: format,
			},
		}
	}

	t.Run("ListModels", func(t *testing.T) {
		response, bifrostErr := provider.ListModels(newTestContext(t), []schemas.Key{key}, &schemas.BifrostListModelsRequest{Provider: schemas.Soniox})
		require.Nil(t, bifrostErr, "%v", bifrostErr)
		var ids []string
		for _, model := range response.Data {
			ids = append(ids, model.ID)
		}
		assert.Contains(t, ids, "soniox/stt-async-v6")
		assert.Contains(t, ids, "soniox/tts-rt-v2")
	})

	t.Run("Speech without a language surfaces Soniox's error", func(t *testing.T) {
		request := liveSpeech("mp3")
		request.Params.LanguageCode = nil
		_, bifrostErr := provider.Speech(newTestContext(t), key, request)
		require.NotNil(t, bifrostErr)
		require.NotNil(t, bifrostErr.StatusCode)
		assert.Equal(t, http.StatusBadRequest, *bifrostErr.StatusCode)
		assert.Contains(t, strings.ToLower(bifrostErr.Error.Message), "language")
	})

	var mp3 []byte
	t.Run("Speech", func(t *testing.T) {
		response, bifrostErr := provider.Speech(newTestContext(t), key, liveSpeech("mp3"))
		require.Nil(t, bifrostErr, "%v", bifrostErr)
		require.Greater(t, len(response.Audio), 1000)
		mp3 = response.Audio
	})

	t.Run("SpeechStream", func(t *testing.T) {
		stream, bifrostErr := provider.SpeechStream(newTestContext(t), passThroughPostHook, func(context.Context) {}, key, liveSpeech("pcm"))
		require.Nil(t, bifrostErr, "%v", bifrostErr)
		total, deltas, sawDone := 0, 0, false
		timeout := time.After(60 * time.Second)
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
					deltas++
					total += len(chunk.BifrostSpeechStreamResponse.Audio)
				case schemas.SpeechStreamResponseTypeDone:
					sawDone = true
				}
			case <-timeout:
				t.Fatal("speech stream did not finish")
			}
		}
		assert.Greater(t, deltas, 0)
		assert.Greater(t, total, 1000)
		assert.True(t, sawDone)
	})

	t.Run("Transcription round trip", func(t *testing.T) {
		require.NotEmpty(t, mp3, "the Speech subtest must have produced audio")
		response, bifrostErr := provider.Transcription(newTestContext(t), key, &schemas.BifrostTranscriptionRequest{
			Provider: schemas.Soniox,
			Model:    "stt-async-v6",
			Input:    &schemas.TranscriptionInput{File: mp3, Filename: "speech.mp3"},
			Params:   &schemas.TranscriptionParameters{Language: &language},
		})
		require.Nil(t, bifrostErr, "%v", bifrostErr)
		assert.Contains(t, response.Text, "जियो")
		assert.NotEmpty(t, response.Words)
		require.NotNil(t, response.Usage)
		require.NotNil(t, response.Usage.Seconds)
		assert.Greater(t, *response.Usage.Seconds, 0.0)
	})

	t.Run("Transcription of a corrupt file is a client error", func(t *testing.T) {
		_, bifrostErr := provider.Transcription(newTestContext(t), key, &schemas.BifrostTranscriptionRequest{
			Provider: schemas.Soniox,
			Model:    "stt-async-v6",
			Input:    &schemas.TranscriptionInput{File: []byte("not audio at all, just text padding"), Filename: "junk.wav"},
		})
		require.NotNil(t, bifrostErr)
		require.NotNil(t, bifrostErr.StatusCode)
		assert.Equal(t, http.StatusBadRequest, *bifrostErr.StatusCode)
	})
}

// realtimeClient drives the provider's translator against a live Soniox socket the way the gateway's relay
// does: one step at a time, under one lock, writing each provider frame as the type the translator marks.
type realtimeClient struct {
	t          *testing.T
	conn       *ws.Conn
	translator schemas.RealtimeTranslator
	mu         sync.Mutex
	events     chan gjson.Result
	done       chan struct{}
}

func dialRealtime(t *testing.T, provider *soniox.SonioxProvider, key schemas.Key) *realtimeClient {
	t.Helper()
	ctx := newTestContext(t)
	url, bifrostErr := provider.RealtimeWebSocketURL(ctx, key, "stt-rt-v5", "transcription")
	require.Nil(t, bifrostErr, "%v", bifrostErr)
	headers, bifrostErr := provider.RealtimeHeaders(ctx, key)
	require.Nil(t, bifrostErr, "%v", bifrostErr)
	header := http.Header{}
	for name, value := range headers {
		header.Set(name, value)
	}
	conn, _, err := ws.DefaultDialer.Dial(url, header)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	translator, bifrostErr := provider.NewRealtimeTranslator(ctx, key, "stt-rt-v5")
	require.Nil(t, bifrostErr, "%v", bifrostErr)

	c := &realtimeClient{t: t, conn: conn, translator: translator, events: make(chan gjson.Result, 512), done: make(chan struct{})}
	c.write(translator.Open())
	go c.read()
	return c
}

func (c *realtimeClient) write(translation schemas.RealtimeTranslation) {
	for i, frame := range translation.Provider {
		messageType := ws.TextMessage
		if translation.IsProviderBinary(i) {
			messageType = ws.BinaryMessage
		}
		require.NoError(c.t, c.conn.WriteMessage(messageType, frame))
	}
	for _, frame := range translation.Client {
		c.events <- gjson.ParseBytes(frame)
	}
}

func (c *realtimeClient) send(raw string) {
	c.t.Helper()
	event, err := schemas.ParseRealtimeEvent([]byte(raw))
	require.NoError(c.t, err)
	c.mu.Lock()
	defer c.mu.Unlock()
	translation, err := c.translator.ToProvider(event)
	require.NoError(c.t, err)
	c.write(translation)
}

func (c *realtimeClient) read() {
	defer close(c.done)
	for {
		_, data, err := c.conn.ReadMessage()
		if err != nil {
			return
		}
		c.mu.Lock()
		translation, err := c.translator.ToClient(data)
		if err == nil {
			for _, frame := range translation.Client {
				c.events <- gjson.ParseBytes(frame)
			}
		}
		c.mu.Unlock()
		if err != nil {
			return
		}
	}
}

// stream sends audio as 100 ms append events, pausing between them.
func (c *realtimeClient) stream(pcm []byte, rate int, pause time.Duration) {
	c.t.Helper()
	chunk := rate * 2 / 10
	for start := 0; start < len(pcm); start += chunk {
		c.send(`{"type":"input_audio_buffer.append","audio":"` + base64.StdEncoding.EncodeToString(pcm[start:min(start+chunk, len(pcm))]) + `"}`)
		time.Sleep(pause)
	}
}

// until returns every event received up to and including the first of the given type.
func (c *realtimeClient) until(eventType string) []gjson.Result {
	c.t.Helper()
	var seen []gjson.Result
	timeout := time.After(60 * time.Second)
	for {
		select {
		case event := <-c.events:
			seen = append(seen, event)
			if event.Get("type").String() == eventType {
				return seen
			}
			require.NotEqual(c.t, "error", event.Get("type").String(), "%s", event.Raw)
		case <-c.done:
			require.Failf(c.t, "socket closed", "before %s; saw %d events", eventType, len(seen))
		case <-timeout:
			require.Failf(c.t, "timeout", "waiting for %s; saw %d events", eventType, len(seen))
		}
	}
}

// pcmOf returns the samples of a 16-bit mono WAV file and their rate.
func pcmOf(t *testing.T, path string) ([]byte, int) {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	rate := int(binary.LittleEndian.Uint32(data[24:28]))
	for offset := 12; offset+8 <= len(data); {
		size := int(binary.LittleEndian.Uint32(data[offset+4 : offset+8]))
		if string(data[offset:offset+4]) == "data" {
			return data[offset+8 : min(offset+8+size, len(data))], rate
		}
		offset += 8 + size + size%2
	}
	require.Fail(t, "no data chunk in "+path)
	return nil, 0
}

// TestSonioxRealtime runs a spoken question through the translator against live Soniox, both with
// Soniox's endpoint detection ending the segment and with the client committing it. It is skipped without
// SONIOX_API_KEY.
func TestSonioxRealtime(t *testing.T) {
	apiKey := strings.TrimSpace(os.Getenv("SONIOX_API_KEY"))
	if apiKey == "" {
		t.Skip("Skipping Soniox realtime test because SONIOX_API_KEY is not set")
	}
	provider, err := soniox.NewSonioxProvider(&schemas.ProviderConfig{}, bifrost.NewNoOpLogger())
	require.NoError(t, err)
	key := schemas.Key{ID: "live", Models: schemas.WhiteList{"*"}, Value: *schemas.NewSecretVar(apiKey)}
	question, rate := pcmOf(t, "../../../tests/realtime/testdata/ask-weather.wav")

	check := func(t *testing.T, frames []gjson.Result) {
		t.Helper()
		var deltas, items []string
		var completed, committed gjson.Result
		for _, frame := range frames {
			switch frame.Get("type").String() {
			case "conversation.item.input_audio_transcription.delta":
				deltas = append(deltas, frame.Get("delta").String())
				items = append(items, frame.Get("item_id").String())
				assert.True(t, frame.Get("transcript").Exists(), "a delta event carries the whole partial: %s", frame.Raw)
			case "conversation.item.input_audio_transcription.completed":
				completed = frame
			case "input_audio_buffer.committed":
				committed = frame
			}
		}
		assert.Contains(t, strings.ToLower(completed.Get("transcript").String()), "weather")
		assert.Equal(t, "duration", completed.Get("usage.type").String())
		assert.Positive(t, completed.Get("usage.seconds").Float(), "Soniox reports the audio it processed")
		item := completed.Get("item_id").String()
		assert.Equal(t, item, committed.Get("item_id").String())
		require.NotEmpty(t, deltas, "the partial text reached the client before the segment ended")
		for _, delta := range items {
			assert.Equal(t, item, delta)
		}
		assert.True(t, strings.HasPrefix(completed.Get("transcript").String(), strings.Join(deltas, "")), "the deltas are the start of the final text")
	}

	t.Run("endpoint detection ends the segment", func(t *testing.T) {
		c := dialRealtime(t, provider, key)
		c.send(`{"type":"session.update","session":{"audio":{"input":{"transcription":{"model":"stt-rt-v5","language":"en"}}}}}`)
		c.until("session.updated")
		c.stream(question, rate, 40*time.Millisecond)
		c.stream(make([]byte, rate*2*3), rate, 40*time.Millisecond) // three seconds of silence
		check(t, c.until("conversation.item.input_audio_transcription.completed"))
	})

	t.Run("a commit ends the segment", func(t *testing.T) {
		c := dialRealtime(t, provider, key)
		c.send(`{"type":"session.update","session":{"audio":{"input":{"transcription":{"model":"stt-rt-v5","language":"en"},"turn_detection":null}}}}`)
		c.until("session.updated")
		c.stream(question, rate, 40*time.Millisecond)
		c.send(`{"type":"input_audio_buffer.commit"}`)
		check(t, c.until("conversation.item.input_audio_transcription.completed"))
	})

	t.Run("a refused start reaches the client as an error event", func(t *testing.T) {
		bad := schemas.Key{ID: "bad", Models: schemas.WhiteList{"*"}, Value: *schemas.NewSecretVar("not-a-key")}
		c := dialRealtime(t, provider, bad)
		c.send(`{"type":"session.update","session":{"audio":{"input":{"transcription":{"model":"stt-rt-v5"}}}}}`)
		select {
		case <-time.After(30 * time.Second):
			t.Fatal("no answer to a bad key")
		case event := <-c.events:
			for event.Get("type").String() != "error" {
				select {
				case event = <-c.events:
				case <-time.After(30 * time.Second):
					t.Fatal("no error event for a bad key")
				}
			}
			assert.Equal(t, "authentication_error", event.Get("error.type").String())
			assert.Equal(t, "unauthenticated", event.Get("error.code").String())
		}
	})
}
