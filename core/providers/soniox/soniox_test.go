package soniox_test

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
	"unicode/utf8"

	ws "github.com/fasthttp/websocket"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/internal/llmtests"
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

	t.Run("SpeechSession", func(t *testing.T) {
		english := "en"
		llmtests.RunSpeechSession(t, newTestContext(t), provider, key, &schemas.BifrostSpeechRequest{
			Provider: schemas.Soniox,
			Model:    "tts-rt-v2",
			Input:    &schemas.SpeechInput{},
			Params: &schemas.SpeechParameters{
				VoiceConfig:    &schemas.SpeechVoiceInput{Voice: &voice},
				LanguageCode:   &english,
				ResponseFormat: "pcm",
			},
		})
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

// fakeTTSConn is the TTS WebSocket as the session sees it: frames the test sends as Soniox, and the
// frames the session wrote.
type fakeTTSConn struct {
	frames chan []byte
	writes chan []byte
	closed chan struct{}
	once   sync.Once
}

func newFakeTTSConn() *fakeTTSConn {
	return &fakeTTSConn{frames: make(chan []byte, 256), writes: make(chan []byte, 256), closed: make(chan struct{})}
}

func (c *fakeTTSConn) ReadMessage() (int, []byte, error) {
	select {
	case frame := <-c.frames:
		return ws.TextMessage, frame, nil
	case <-c.closed:
		return 0, nil, io.EOF
	}
}

func (c *fakeTTSConn) WriteMessage(_ int, data []byte) error {
	select {
	case <-c.closed:
		return io.ErrClosedPipe
	default:
	}
	c.writes <- append([]byte(nil), data...)
	return nil
}

func (c *fakeTTSConn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return nil
}

func (c *fakeTTSConn) serve(frame string) { c.frames <- []byte(frame) }

// brokenWriteConn is a TTS connection whose writes fail: at once, or, with hold, when the connection closes, as a
// write does that is in flight when the session is closed.
type brokenWriteConn struct {
	*fakeTTSConn
	hold    bool
	entered chan struct{}
}

func (c *brokenWriteConn) WriteMessage(int, []byte) error {
	if !c.hold {
		return errors.New("write failed")
	}
	select {
	case c.entered <- struct{}{}:
	default:
	}
	<-c.closed
	return io.ErrClosedPipe
}

func ttsAudio(stream, audio string) string {
	return fmt.Sprintf(`{"stream_id":%q,"audio":%q}`, stream, audio)
}

func ttsTerminated(stream string) string {
	return fmt.Sprintf(`{"stream_id":%q,"terminated":true}`, stream)
}

func ttsError(stream string, code int, errorType, message string) string {
	return fmt.Sprintf(`{"stream_id":%q,"error_code":%d,"error_type":%q,"error_message":%q,"request_id":"req-1"}`, stream, code, errorType, message)
}

func ttsSessionRequest(extra map[string]interface{}) *schemas.BifrostSpeechRequest {
	voice, language, timestamps := "Daniel", "en", true
	params := &schemas.SpeechParameters{
		VoiceConfig:    &schemas.SpeechVoiceInput{Voice: &voice},
		LanguageCode:   &language,
		ResponseFormat: "pcm",
		WithTimestamps: &timestamps,
		ExtraParams:    map[string]interface{}{"sample_rate": 24000},
	}
	for name, value := range extra {
		params.ExtraParams[name] = value
	}
	return &schemas.BifrostSpeechRequest{Provider: schemas.Soniox, Model: "tts-rt-v2", Params: params}
}

func openTTSSession(t *testing.T, ctx *schemas.BifrostContext, request *schemas.BifrostSpeechRequest) (schemas.SpeechSession, *fakeTTSConn) {
	t.Helper()
	conn := newFakeTTSConn()
	session, bifrostErr := newTestProvider(t, "https://api.soniox.com").OpenSpeechSession(ctx, testKey(), request,
		func(*schemas.BifrostContext, string, map[string]string) (schemas.RealtimeUpstream, *schemas.BifrostError) {
			return conn, nil
		})
	require.Nil(t, bifrostErr)
	return session, conn
}

// written reads the next frame the session wrote.
func (c *fakeTTSConn) written(t *testing.T) gjson.Result {
	t.Helper()
	select {
	case frame := <-c.writes:
		return gjson.ParseBytes(frame)
	case <-time.After(2 * time.Second):
		require.FailNow(t, "the session wrote nothing")
		return gjson.Result{}
	}
}

func nextSessionEvent(t *testing.T, session schemas.SpeechSession) *schemas.BifrostSpeechSessionEvent {
	t.Helper()
	select {
	case event, ok := <-session.Events():
		require.True(t, ok, "events closed")
		return event
	case <-time.After(2 * time.Second):
		require.FailNow(t, "the session sent no event")
		return nil
	}
}

func requireSessionEnded(t *testing.T, session schemas.SpeechSession) {
	t.Helper()
	select {
	case event, ok := <-session.Events():
		require.False(t, ok, "unexpected event %+v", event)
	case <-time.After(2 * time.Second):
		require.FailNow(t, "the session did not end")
	}
}

func TestSonioxSpeechSession(t *testing.T) {
	t.Run("opens the TTS WebSocket of the base URL's region with the key", func(t *testing.T) {
		var dialedURL string
		var dialedHeaders map[string]string
		session, bifrostErr := newTestProvider(t, "https://api.eu.soniox.com").OpenSpeechSession(newTestContext(t), testKey(), ttsSessionRequest(nil),
			func(_ *schemas.BifrostContext, url string, headers map[string]string) (schemas.RealtimeUpstream, *schemas.BifrostError) {
				dialedURL, dialedHeaders = url, headers
				return newFakeTTSConn(), nil
			})
		require.Nil(t, bifrostErr)
		defer session.Close()
		assert.Equal(t, "wss://tts-rt.eu.soniox.com/tts-websocket", dialedURL)
		assert.Equal(t, "Bearer test-key", dialedHeaders["Authorization"])
		format, rate := session.AudioFormat()
		assert.Equal(t, "pcm", format)
		assert.Equal(t, 24000, rate)
	})

	t.Run("raw audio without a sample rate gets Soniox's default, sent and reported", func(t *testing.T) {
		request := ttsSessionRequest(nil)
		delete(request.Params.ExtraParams, "sample_rate")
		session, conn := openTTSSession(t, newTestContext(t), request)
		defer session.Close()
		_, rate := session.AudioFormat()
		assert.Equal(t, 24000, rate)
		require.NoError(t, session.SendText("Hello"))
		assert.Equal(t, int64(24000), conn.written(t).Get("sample_rate").Int())

		request = ttsSessionRequest(nil)
		delete(request.Params.ExtraParams, "sample_rate")
		request.Params.ResponseFormat = "mp3"
		mp3Session, mp3Conn := openTTSSession(t, newTestContext(t), request)
		defer mp3Session.Close()
		_, rate = mp3Session.AudioFormat()
		assert.Zero(t, rate, "a container format carries its own rate")
		require.NoError(t, mp3Session.SendText("Hello"))
		assert.False(t, mp3Conn.written(t).Get("sample_rate").Exists())
	})

	t.Run("a failed dial is returned before the session exists", func(t *testing.T) {
		refused := &schemas.BifrostError{Error: &schemas.ErrorField{Message: "refused"}}
		session, bifrostErr := newTestProvider(t, "https://api.soniox.com").OpenSpeechSession(newTestContext(t), testKey(), ttsSessionRequest(nil),
			func(*schemas.BifrostContext, string, map[string]string) (schemas.RealtimeUpstream, *schemas.BifrostError) {
				return nil, refused
			})
		assert.Nil(t, session)
		assert.Same(t, refused, bifrostErr)
	})

	t.Run("opens nothing until the first text, then sends the config and the text", func(t *testing.T) {
		session, conn := openTTSSession(t, newTestContext(t), ttsSessionRequest(nil))
		defer session.Close()
		assert.Empty(t, conn.writes)

		require.NoError(t, session.SendText("Hello"))
		config := conn.written(t)
		assert.Equal(t, "s1", config.Get("stream_id").String())
		assert.Equal(t, "tts-rt-v2", config.Get("model").String())
		assert.Equal(t, "en", config.Get("language").String())
		assert.Equal(t, "Daniel", config.Get("voice").String())
		assert.Equal(t, "pcm_s16le", config.Get("audio_format").String())
		assert.Equal(t, int64(24000), config.Get("sample_rate").Int())
		assert.True(t, config.Get("return_timestamps").Bool())
		assert.False(t, config.Get("text").Exists(), "a config carries no text")

		text := conn.written(t)
		assert.Equal(t, "s1", text.Get("stream_id").String())
		assert.Equal(t, "Hello", text.Get("text").String())
		assert.False(t, text.Get("text_end").Bool())

		require.NoError(t, session.SendText(" again"))
		assert.Equal(t, " again", conn.written(t).Get("text").String(), "later text reuses the stream")
		assert.Empty(t, conn.writes)
	})

	t.Run("the config carries the fields of the unary request, minus the text", func(t *testing.T) {
		speed := 1.1
		request := ttsSessionRequest(map[string]interface{}{"bitrate": 64, "reduce_silence": true, "client_reference_id": "ref-1"})
		request.Params.Speed = &speed
		session, conn := openTTSSession(t, newTestContext(t), request)
		defer session.Close()
		require.NoError(t, session.SendText("Hello"))

		unary, err := json.Marshal(soniox.ToSonioxSpeechRequest(request))
		require.NoError(t, err)
		want := map[string]bool{"stream_id": true, "return_timestamps": true}
		gjson.ParseBytes(unary).ForEach(func(name, _ gjson.Result) bool {
			if name.String() != "text" {
				want[name.String()] = true
			}
			return true
		})
		got := map[string]bool{}
		conn.written(t).ForEach(func(name, _ gjson.Result) bool {
			got[name.String()] = true
			return true
		})
		assert.Equal(t, want, got)
	})

	t.Run("extra params reach the config only with passthrough", func(t *testing.T) {
		for _, passthrough := range []bool{false, true} {
			ctx := newTestContext(t)
			if passthrough {
				ctx.SetValue(schemas.BifrostContextKeyPassthroughExtraParams, true)
			}
			session, conn := openTTSSession(t, ctx, ttsSessionRequest(map[string]interface{}{"emotion": "calm"}))
			require.NoError(t, session.SendText("Hello"))
			assert.Equal(t, passthrough, conn.written(t).Get("emotion").Exists())
			session.Close()
		}
	})

	t.Run("flush ends the stream and the next text opens a new segment", func(t *testing.T) {
		session, conn := openTTSSession(t, newTestContext(t), ttsSessionRequest(nil))
		defer session.Close()
		require.NoError(t, session.SendText("Hello there."))
		conn.written(t)
		conn.written(t)

		require.NoError(t, session.Flush())
		end := conn.written(t)
		assert.Equal(t, "s1", end.Get("stream_id").String())
		assert.True(t, end.Get("text_end").Bool())
		assert.Equal(t, `{"stream_id":"s1","text":"","text_end":true}`, end.Raw, "an ending message must carry an empty text")

		require.NoError(t, session.Flush())
		assert.Empty(t, conn.writes, "a flush with nothing buffered writes nothing")

		require.NoError(t, session.SendText("More."))
		assert.Equal(t, "s2", conn.written(t).Get("stream_id").String())
		assert.Equal(t, "More.", conn.written(t).Get("text").String())
	})

	t.Run("audio of a later segment is held until the earlier one terminates", func(t *testing.T) {
		session, conn := openTTSSession(t, newTestContext(t), ttsSessionRequest(nil))
		defer session.Close()
		require.NoError(t, session.SendText("one"))
		require.NoError(t, session.Flush())
		require.NoError(t, session.SendText("two"))

		conn.serve(ttsAudio("s2", "B1"))
		conn.serve(ttsAudio("s1", "A1"))
		first := nextSessionEvent(t, session)
		assert.Equal(t, schemas.SpeechSessionEventAudioDelta, first.Type)
		assert.Equal(t, 1, first.Segment)
		assert.Equal(t, "A1", first.Audio)

		conn.serve(ttsTerminated("s1"))
		done := nextSessionEvent(t, session)
		assert.Equal(t, schemas.SpeechSessionEventSegmentDone, done.Type)
		assert.Equal(t, 1, done.Segment)
		released := nextSessionEvent(t, session)
		assert.Equal(t, schemas.SpeechSessionEventAudioDelta, released.Type)
		assert.Equal(t, 2, released.Segment)
		assert.Equal(t, "B1", released.Audio)

		conn.serve(ttsAudio("s2", "B2"))
		assert.Equal(t, "B2", nextSessionEvent(t, session).Audio, "the head segment's audio is not held")
		conn.serve(ttsTerminated("s2"))
		assert.Equal(t, 2, nextSessionEvent(t, session).Segment)
	})

	t.Run("audio held behind an earlier segment is bounded", func(t *testing.T) {
		session, conn := openTTSSession(t, newTestContext(t), ttsSessionRequest(nil))
		defer session.Close()
		require.NoError(t, session.SendText("one"))
		require.NoError(t, session.Flush())
		require.NoError(t, session.SendText("two"))

		megabyte := strings.Repeat("A", 1<<20)
		for range 9 {
			conn.serve(ttsAudio("s2", megabyte))
		}
		requireSessionEnded(t, session)
		bifrostErr := session.Err()
		require.NotNil(t, bifrostErr)
		assert.Contains(t, bifrostErr.Error.Message, "held too much audio")
	})

	t.Run("text over 5000 characters is split on rune boundaries", func(t *testing.T) {
		session, conn := openTTSSession(t, newTestContext(t), ttsSessionRequest(nil))
		defer session.Close()
		text := strings.Repeat("ह", 12001)
		require.NoError(t, session.SendText(text))
		conn.written(t)

		var joined strings.Builder
		var sizes []int
		for range 3 {
			chunk := conn.written(t).Get("text").String()
			require.True(t, utf8.ValidString(chunk))
			sizes = append(sizes, utf8.RuneCountInString(chunk))
			joined.WriteString(chunk)
		}
		assert.Equal(t, []int{5000, 5000, 2001}, sizes)
		assert.Equal(t, text, joined.String())
		assert.Empty(t, conn.writes)
	})

	t.Run("end waits for the last segment, then reports audio done and closes", func(t *testing.T) {
		session, conn := openTTSSession(t, newTestContext(t), ttsSessionRequest(nil))
		defer session.Close()
		require.NoError(t, session.SendText("Goodbye."))
		conn.written(t)
		conn.written(t)

		require.NoError(t, session.End())
		assert.True(t, conn.written(t).Get("text_end").Bool())
		assert.Error(t, session.SendText("late"), "no input is taken after the end")
		assert.Error(t, session.Flush())

		conn.serve(ttsAudio("s1", "A1"))
		assert.Equal(t, "A1", nextSessionEvent(t, session).Audio)
		conn.serve(ttsTerminated("s1"))
		assert.Equal(t, schemas.SpeechSessionEventSegmentDone, nextSessionEvent(t, session).Type)
		done := nextSessionEvent(t, session)
		assert.Equal(t, schemas.SpeechSessionEventAudioDone, done.Type)
		assert.Nil(t, done.Usage, "the transport fills in the usage")
		requireSessionEnded(t, session)
		assert.Nil(t, session.Err())
		select {
		case <-conn.closed:
		case <-time.After(2 * time.Second):
			require.FailNow(t, "the connection stayed open")
		}
	})

	t.Run("end with nothing pending finishes at once", func(t *testing.T) {
		session, _ := openTTSSession(t, newTestContext(t), ttsSessionRequest(nil))
		defer session.Close()
		require.NoError(t, session.End())
		assert.Equal(t, schemas.SpeechSessionEventAudioDone, nextSessionEvent(t, session).Type)
		requireSessionEnded(t, session)
		assert.Nil(t, session.Err())
	})

	t.Run("cancel drops every live stream, answers at once and starts a new segment", func(t *testing.T) {
		session, conn := openTTSSession(t, newTestContext(t), ttsSessionRequest(nil))
		defer session.Close()
		require.NoError(t, session.SendText("one"))
		require.NoError(t, session.Flush())
		require.NoError(t, session.SendText("two"))
		for range 5 {
			conn.written(t)
		}

		require.NoError(t, session.Cancel())
		for _, stream := range []string{"s1", "s2"} {
			cancel := conn.written(t)
			assert.Equal(t, stream, cancel.Get("stream_id").String())
			assert.True(t, cancel.Get("cancel").Bool())
		}
		cancelled := nextSessionEvent(t, session)
		assert.Equal(t, schemas.SpeechSessionEventCancelled, cancelled.Type)
		assert.Equal(t, 2, cancelled.Segment, "the last segment dropped")

		// Audio already in flight, and the terminations, are discarded.
		conn.serve(ttsAudio("s1", "late1"))
		conn.serve(ttsAudio("s2", "late2"))
		conn.serve(ttsTerminated("s1"))
		conn.serve(ttsTerminated("s2"))
		require.NoError(t, session.SendText("three"))
		assert.Equal(t, "s3", conn.written(t).Get("stream_id").String())
		conn.written(t)
		conn.serve(ttsAudio("s3", "fresh"))
		fresh := nextSessionEvent(t, session)
		assert.Equal(t, "fresh", fresh.Audio, "late audio of a cancelled stream is dropped")
		assert.Equal(t, 3, fresh.Segment)
	})

	t.Run("an error of a cancelled stream does not end the session", func(t *testing.T) {
		session, conn := openTTSSession(t, newTestContext(t), ttsSessionRequest(nil))
		defer session.Close()
		require.NoError(t, session.SendText("one"))
		require.NoError(t, session.Cancel())
		assert.Equal(t, schemas.SpeechSessionEventCancelled, nextSessionEvent(t, session).Type)

		conn.serve(ttsError("s1", 400, "invalid_stream_state", "stream was cancelled"))
		require.NoError(t, session.SendText("two"))
		conn.serve(ttsAudio("s2", "alive"))
		assert.Equal(t, "alive", nextSessionEvent(t, session).Audio)
		assert.Nil(t, session.Err())
	})

	t.Run("an error of a live stream ends the session with the provider's error", func(t *testing.T) {
		session, conn := openTTSSession(t, newTestContext(t), ttsSessionRequest(nil))
		defer session.Close()
		require.NoError(t, session.SendText("one"))
		conn.serve(ttsError("s1", 400, "invalid_request", "language is required"))
		requireSessionEnded(t, session)

		bifrostErr := session.Err()
		require.NotNil(t, bifrostErr)
		require.NotNil(t, bifrostErr.StatusCode)
		assert.Equal(t, 400, *bifrostErr.StatusCode)
		assert.Equal(t, "language is required", bifrostErr.Error.Message)
		assert.Equal(t, "invalid_request", *bifrostErr.Error.Type)
		assert.Equal(t, "req-1", *bifrostErr.EventID)
		assert.Error(t, session.SendText("more"))
	})

	t.Run("a connection that closes under the session is an upstream error", func(t *testing.T) {
		session, conn := openTTSSession(t, newTestContext(t), ttsSessionRequest(nil))
		defer session.Close()
		require.NoError(t, session.SendText("one"))
		conn.Close()
		requireSessionEnded(t, session)
		require.NotNil(t, session.Err())
		assert.Equal(t, 502, *session.Err().StatusCode)
	})

	t.Run("carries character timings in seconds", func(t *testing.T) {
		session, conn := openTTSSession(t, newTestContext(t), ttsSessionRequest(nil))
		defer session.Close()
		require.NoError(t, session.SendText("Hi"))
		conn.serve(`{"stream_id":"s1","audio":"QQ==","timestamps":{"characters":["H","i"],"character_start_times_seconds":[0,0.5],"character_end_times_seconds":[0.5,1.25]}}`)

		event := nextSessionEvent(t, session)
		require.NotNil(t, event.Alignment)
		assert.Equal(t, []string{"H", "i"}, event.Alignment.Characters)
		assert.Equal(t, []float64{0, 0.5}, event.Alignment.CharacterStartTimesSeconds)
		assert.Equal(t, []float64{0.5, 1.25}, event.Alignment.CharacterEndTimesSeconds)

		conn.serve(ttsAudio("s1", "Qg=="))
		assert.Nil(t, nextSessionEvent(t, session).Alignment, "a frame without timestamps has no alignment")
	})

	t.Run("text waits for a slot while Soniox holds five streams", func(t *testing.T) {
		session, conn := openTTSSession(t, newTestContext(t), ttsSessionRequest(nil))
		defer session.Close()
		for i := range 5 {
			require.NoError(t, session.SendText(fmt.Sprintf("sentence %d", i+1)))
			require.NoError(t, session.Flush())
			for range 3 {
				conn.written(t)
			}
		}
		require.NoError(t, session.SendText("sixth"))
		require.NoError(t, session.Flush())
		assert.Empty(t, conn.writes, "no sixth stream may be opened")

		conn.serve(ttsTerminated("s1"))
		assert.Equal(t, schemas.SpeechSessionEventSegmentDone, nextSessionEvent(t, session).Type)
		assert.Equal(t, "s6", conn.written(t).Get("stream_id").String())
		assert.Equal(t, "sixth", conn.written(t).Get("text").String())
		assert.True(t, conn.written(t).Get("text_end").Bool())
	})

	t.Run("flushes queued behind busy streams are merged, so the queue cannot be grown by flushing", func(t *testing.T) {
		session, conn := openTTSSession(t, newTestContext(t), ttsSessionRequest(nil))
		defer session.Close()
		for i := range 5 {
			require.NoError(t, session.SendText(fmt.Sprintf("sentence %d", i+1)))
			require.NoError(t, session.Flush())
			for range 3 {
				conn.written(t)
			}
		}
		require.NoError(t, session.SendText("waiting"))

		liveHeap := func() int64 {
			runtime.GC()
			var stats runtime.MemStats
			runtime.ReadMemStats(&stats)
			return int64(stats.HeapAlloc)
		}
		before := liveHeap()
		for range 200_000 {
			if err := session.Flush(); err != nil {
				t.Fatal(err)
			}
		}
		assert.Less(t, liveHeap()-before, int64(1<<20), "a flush right after a flush ends nothing more, so it is not queued again")

		// The merged flushes still end the waiting text once, when a slot frees.
		conn.serve(ttsTerminated("s1"))
		assert.Equal(t, "s6", conn.written(t).Get("stream_id").String())
		assert.Equal(t, "waiting", conn.written(t).Get("text").String())
		assert.True(t, conn.written(t).Get("text_end").Bool())
		time.Sleep(50 * time.Millisecond)
		assert.Empty(t, conn.writes, "no second end for the same stream")
	})

	t.Run("a cancel discards the text that was waiting for a slot", func(t *testing.T) {
		session, conn := openTTSSession(t, newTestContext(t), ttsSessionRequest(nil))
		defer session.Close()
		for i := range 5 {
			require.NoError(t, session.SendText(fmt.Sprintf("sentence %d", i+1)))
			require.NoError(t, session.Flush())
		}
		require.NoError(t, session.SendText("waiting"))
		require.NoError(t, session.Cancel())
		for range 15 + 5 {
			conn.written(t)
		}
		conn.serve(ttsTerminated("s1"))
		assert.Empty(t, conn.writes, "the waiting text is gone")
		assert.Equal(t, schemas.SpeechSessionEventCancelled, nextSessionEvent(t, session).Type)
	})

	t.Run("close is idempotent, releases a blocked sender and leaks no goroutine", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			defer ctx.Cancel()
			session, conn := openTTSSession(t, ctx, ttsSessionRequest(nil))
			require.NoError(t, session.SendText("one"))
			// Nobody reads the events, so the reader blocks once the buffer is full.
			for range 80 {
				conn.serve(ttsAudio("s1", "AAAA"))
			}
			synctest.Wait()

			require.NoError(t, session.Close())
			require.NoError(t, session.Close())
			drained := 0
			for range session.Events() {
				drained++
			}
			assert.LessOrEqual(t, drained, 80)
			assert.Nil(t, session.Err())
			assert.Error(t, session.SendText("after close"))
		})
	})

	t.Run("close does not wait behind a cancel that is blocked sending", func(t *testing.T) {
		session, conn := openTTSSession(t, newTestContext(t), ttsSessionRequest(nil))
		require.NoError(t, session.SendText("one"))
		// Nobody reads the events, so the reader blocks once the buffer is full.
		for range 80 {
			conn.serve(ttsAudio("s1", "AAAA"))
		}
		time.Sleep(200 * time.Millisecond)
		// The cancel holds the session lock while it waits behind the reader to send its event.
		cancelled := make(chan struct{})
		go func() { _ = session.Cancel(); close(cancelled) }()
		time.Sleep(200 * time.Millisecond)

		closed := make(chan struct{})
		go func() { _ = session.Close(); close(closed) }()
		select {
		case <-closed:
		case <-time.After(3 * time.Second):
			require.FailNow(t, "close is blocked behind a cancel that is waiting to send")
		}
		select {
		case <-cancelled:
		case <-time.After(3 * time.Second):
			require.FailNow(t, "the cancel was not released by the close")
		}
	})

	t.Run("a late termination does not take speech.audio.done from a call that is waiting to send it", func(t *testing.T) {
		for _, tc := range []struct {
			name   string
			free   int // room left in the event buffer when the call that finishes the session is made
			finish func(t *testing.T, session schemas.SpeechSession, finished chan<- struct{})
		}{
			{"a cancel that finishes an ended session", 0, func(t *testing.T, session schemas.SpeechSession, finished chan<- struct{}) {
				require.NoError(t, session.End())
				go func() { _ = session.Cancel(); close(finished) }()
			}},
			{"an end that finishes a cancelled session", 1, func(t *testing.T, session schemas.SpeechSession, finished chan<- struct{}) {
				require.NoError(t, session.Cancel())
				go func() { _ = session.End(); close(finished) }()
			}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				session, conn := openTTSSession(t, newTestContext(t), ttsSessionRequest(nil))
				defer session.Close()
				require.NoError(t, session.SendText("one"))
				queued := cap(session.Events()) - tc.free
				for range queued {
					conn.serve(ttsAudio("s1", "AAAA"))
				}
				require.Eventually(t, func() bool { return len(session.Events()) == queued }, 2*time.Second, 5*time.Millisecond)

				finished := make(chan struct{})
				tc.finish(t, session, finished)
				time.Sleep(200 * time.Millisecond) // the finishing call is now waiting for room in the buffer
				conn.serve(ttsTerminated("s1"))
				require.Eventually(t, func() bool { return len(conn.frames) == 0 }, 2*time.Second, time.Millisecond)
				time.Sleep(200 * time.Millisecond) // the reader has handled the termination before anything is read

				var types []schemas.SpeechSessionEventType
				timeout := time.After(3 * time.Second)
			drain:
				for {
					select {
					case event, ok := <-session.Events():
						if !ok {
							break drain
						}
						types = append(types, event.Type)
					case <-timeout:
						require.FailNow(t, "the session did not end")
					}
				}
				require.NotEmpty(t, types)
				assert.Equal(t, schemas.SpeechSessionEventAudioDone, types[len(types)-1], "the events ended without speech.audio.done")
				assert.Nil(t, session.Err())
				select {
				case <-finished:
				case <-time.After(2 * time.Second):
					require.FailNow(t, "the finishing call did not return")
				}
			})
		}
	})

	t.Run("a write that fails because the session was closed is not an upstream failure", func(t *testing.T) {
		conn := &brokenWriteConn{fakeTTSConn: newFakeTTSConn(), hold: true, entered: make(chan struct{}, 1)}
		session, bifrostErr := newTestProvider(t, "https://api.soniox.com").OpenSpeechSession(newTestContext(t), testKey(), ttsSessionRequest(nil),
			func(*schemas.BifrostContext, string, map[string]string) (schemas.RealtimeUpstream, *schemas.BifrostError) {
				return conn, nil
			})
		require.Nil(t, bifrostErr)
		sent := make(chan error, 1)
		go func() { sent <- session.SendText("one") }()
		select {
		case <-conn.entered: // the write that opens the stream is in flight
		case <-time.After(2 * time.Second):
			require.FailNow(t, "the session never wrote")
		}

		require.NoError(t, session.Close())

		assert.Error(t, <-sent)
		assert.Nil(t, session.Err(), "the client closed the session, so nothing failed upstream")
	})

	t.Run("a write that fails while the session is open is an upstream failure", func(t *testing.T) {
		conn := &brokenWriteConn{fakeTTSConn: newFakeTTSConn()}
		session, bifrostErr := newTestProvider(t, "https://api.soniox.com").OpenSpeechSession(newTestContext(t), testKey(), ttsSessionRequest(nil),
			func(*schemas.BifrostContext, string, map[string]string) (schemas.RealtimeUpstream, *schemas.BifrostError) {
				return conn, nil
			})
		require.Nil(t, bifrostErr)

		assert.Error(t, session.SendText("one"))

		require.NotNil(t, session.Err())
		assert.Contains(t, session.Err().Error.Message, "failed to write to the soniox text-to-speech websocket")
		require.NoError(t, session.Close())
	})

	t.Run("keeps a quiet connection alive", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			defer ctx.Cancel()
			session, conn := openTTSSession(t, ctx, ttsSessionRequest(nil))
			defer session.Close()

			time.Sleep(19 * time.Second)
			synctest.Wait()
			assert.Empty(t, conn.writes)
			time.Sleep(2 * time.Second)
			synctest.Wait()
			require.Len(t, conn.writes, 1)
			assert.Equal(t, `{"keep_alive":true}`, string(<-conn.writes))
			time.Sleep(20 * time.Second)
			synctest.Wait()
			require.Len(t, conn.writes, 1)
			assert.Equal(t, `{"keep_alive":true}`, string(<-conn.writes))
		})
	})

	t.Run("ends a stream that gets no text before Soniox fails it", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			defer ctx.Cancel()
			session, conn := openTTSSession(t, ctx, ttsSessionRequest(nil))
			defer session.Close()

			require.NoError(t, session.SendText("a"))
			time.Sleep(2 * time.Second)
			require.NoError(t, session.SendText("b"))
			time.Sleep(2 * time.Second)
			synctest.Wait()
			require.Len(t, conn.writes, 3, "the second text restarted the idle timer: config, a, b")
			<-conn.writes
			<-conn.writes
			<-conn.writes

			time.Sleep(1500 * time.Millisecond)
			synctest.Wait()
			require.Len(t, conn.writes, 1)
			assert.Equal(t, `{"stream_id":"s1","text":"","text_end":true}`, string(<-conn.writes))

			require.NoError(t, session.SendText("c"))
			assert.Equal(t, "s2", gjson.ParseBytes(<-conn.writes).Get("stream_id").String())
		})
	})
}

func BenchmarkSonioxSpeechSessionFrame(b *testing.B) {
	provider, err := soniox.NewSonioxProvider(&schemas.ProviderConfig{}, bifrost.NewNoOpLogger())
	require.NoError(b, err)
	audio := strings.Repeat("A", 16384) // about 0.25 s of 24 kHz PCM
	for name, frame := range map[string]string{
		"audio":      ttsAudio("s1", audio),
		"timestamps": fmt.Sprintf(`{"stream_id":"s1","audio":%q,"timestamps":{"characters":["H","e","l","l","o"],"character_start_times_seconds":[0,0.1,0.2,0.3,0.4],"character_end_times_seconds":[0.1,0.2,0.3,0.4,0.5]}}`, audio),
	} {
		b.Run(name, func(b *testing.B) {
			conn := newFakeTTSConn()
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			defer ctx.Cancel()
			session, bifrostErr := provider.OpenSpeechSession(ctx, testKey(), ttsSessionRequest(nil),
				func(*schemas.BifrostContext, string, map[string]string) (schemas.RealtimeUpstream, *schemas.BifrostError) {
					return conn, nil
				})
			require.Nil(b, bifrostErr)
			defer session.Close()
			require.NoError(b, session.SendText("Hello"))
			<-conn.writes
			<-conn.writes

			payload := []byte(frame)
			b.ReportAllocs()
			b.SetBytes(int64(len(payload)))
			for b.Loop() {
				conn.frames <- payload
				<-session.Events()
			}
		})
	}
}
