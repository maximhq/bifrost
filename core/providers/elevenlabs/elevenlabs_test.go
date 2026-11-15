package elevenlabs_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/bytedance/sonic"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/internal/llmtests"
	"github.com/maximhq/bifrost/core/providers/elevenlabs"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

func TestElevenlabsListModelsWithoutCatalog(t *testing.T) {
	for _, tc := range []struct {
		status int
		model  string
	}{
		{http.StatusOK, "eleven_turbo_v2_5"},
		{http.StatusOK, "scribe_v2"},
		{http.StatusUnauthorized, ""},
	} {
		t.Run(http.StatusText(tc.status)+"/"+tc.model, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				if tc.status == http.StatusOK {
					_, _ = w.Write([]byte(`[{"model_id":"` + tc.model + `","name":"Upstream Model"}]`))
				} else {
					_, _ = w.Write([]byte(`{"detail":{"status":"invalid_api_key","message":"invalid api key"}}`))
				}
			}))
			defer server.Close()
			provider := elevenlabs.NewElevenlabsProvider(&schemas.ProviderConfig{
				NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL, AllowPrivateNetwork: true},
			}, bifrost.NewNoOpLogger())
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			defer ctx.Cancel()
			response, failure := provider.ListModels(ctx, []schemas.Key{{
				ID: "key-1", Models: schemas.WhiteList{"*"}, Value: *schemas.NewSecretVar("test-key"),
			}}, &schemas.BifrostListModelsRequest{Provider: schemas.Elevenlabs})
			if tc.status != http.StatusOK {
				if failure == nil || failure.StatusCode == nil || *failure.StatusCode != tc.status {
					t.Fatalf("upstream status must be preserved, got %+v", failure)
				}
				return
			}
			if failure != nil {
				t.Fatal(failure.GetErrorString())
			}
			if len(response.Data) != 1 || response.Data[0].ID != "elevenlabs/"+tc.model {
				t.Fatalf("SDK without a catalog must retain the upstream-only listing, got %+v", response.Data)
			}
		})
	}
}

// A text-to-speech body and the voice of its path convert to the typed request governance, logs and cost read.
func TestElevenlabsSpeechRequestToBifrost(t *testing.T) {
	for _, tc := range []struct {
		name, modelID, wantModel string
		wantProvider             schemas.ModelProvider
	}{
		{"a model with no prefix", "eleven_v4_turbo", "eleven_v4_turbo", ""},
		{"a model of ElevenLabs by prefix", "elevenlabs/eleven_v4_turbo", "eleven_v4_turbo", schemas.Elevenlabs},
		{"a model of another provider", "openai/tts-1", "tts-1", schemas.OpenAI},
		{"no model", "", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			language := "hi"
			req := (&elevenlabs.ElevenlabsSpeechRequest{Text: "hello", ModelID: tc.modelID, LanguageCode: &language}).ToBifrostSpeechRequest("voice123")

			if req.Provider != tc.wantProvider || req.Model != tc.wantModel {
				t.Fatalf("provider and model = %q %q, want %q %q", req.Provider, req.Model, tc.wantProvider, tc.wantModel)
			}
			if req.Input.Input != "hello" || req.Params.VoiceConfig.Voice == nil || *req.Params.VoiceConfig.Voice != "voice123" || *req.Params.LanguageCode != "hi" {
				t.Fatalf("request = %+v %+v", req.Input, req.Params)
			}
		})
	}
}

// The model list is the Bifrost list in ElevenLabs' schema, as on the other native routes: each model under the id a
// client sends, which is the key's alias for an aliased model, with only what Bifrost knows of it. What ElevenLabs
// itself sent is not echoed.
func TestElevenlabsListModelsResponseIsTheBifrostList(t *testing.T) {
	name := "Eleven v4 Turbo"
	upstream := "eleven_multilingual_v2"
	resp := &schemas.BifrostListModelsResponse{
		Data: []schemas.Model{
			{ID: "elevenlabs/eleven_v4_turbo", Name: &name},
			{ID: "elevenlabs/friendly", Alias: &upstream},
			{ID: "elevenlabs/eleven_flash_v2_5"},
		},
		ExtraFields: schemas.BifrostResponseExtraFields{RawResponse: []any{json.RawMessage(`[{"model_id":"eleven_v4_turbo","can_do_text_to_speech":true,"languages":[{"language_id":"en","name":"English"}]}]`)}},
	}

	encoded, err := json.Marshal(elevenlabs.ToElevenlabsListModelsResponse(resp))

	if err != nil {
		t.Fatal(err)
	}
	if want := `[{"model_id":"eleven_v4_turbo","name":"Eleven v4 Turbo"},{"model_id":"friendly"},{"model_id":"eleven_flash_v2_5"}]`; string(encoded) != want {
		t.Fatalf("list = %s, want %s", encoded, want)
	}
	if list := elevenlabs.ToElevenlabsListModelsResponse(nil); list != nil {
		t.Fatalf("list of no response = %+v", list)
	}
}

// A body forwarded as the client wrote it still names the model ElevenLabs knows, which the client may have written
// with the provider prefix or as a key's alias; the rest of the body goes as it was.
func TestElevenlabsSpeechRawBodyNamesTheResolvedModel(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{
		{"provider prefix", `{"text":"hi","model_id":"elevenlabs/eleven_v4_turbo","seed":3}`, `{"text":"hi","model_id":"eleven_v4_turbo","seed":3}`},
		{"key alias", `{"text":"hi","model_id":"friendly","seed":3}`, `{"text":"hi","model_id":"eleven_v4_turbo","seed":3}`},
		{"already named", `{"text":"hi","model_id":"eleven_v4_turbo","seed":3}`, `{"text":"hi","model_id":"eleven_v4_turbo","seed":3}`},
		{"no model", `{"text":"hi","seed":3}`, `{"text":"hi","seed":3}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				got = string(body)
				w.Header().Set("Content-Type", "audio/mpeg")
				_, _ = w.Write([]byte("audio-bytes"))
			}))
			defer server.Close()
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			ctx.SetValue(schemas.BifrostContextKeyUseRawRequestBody, true)
			voice := "voice123"
			request := &schemas.BifrostSpeechRequest{
				Provider:       schemas.Elevenlabs,
				Model:          "eleven_v4_turbo",
				Input:          &schemas.SpeechInput{Input: "hi"},
				Params:         &schemas.SpeechParameters{VoiceConfig: &schemas.SpeechVoiceInput{Voice: &voice}},
				RawRequestBody: []byte(tc.body),
			}

			_, bifrostErr := passthroughProvider(t, server.URL).Speech(ctx, schemas.Key{Value: *schemas.NewSecretVar("provider-key")}, request)

			if bifrostErr != nil {
				t.Fatalf("speech failed: %+v", bifrostErr.Error)
			}
			if got != tc.want {
				t.Fatalf("upstream body = %s, want %s", got, tc.want)
			}
			if string(request.RawRequestBody) != tc.body {
				t.Fatalf("the client's body was changed in place: %s", request.RawRequestBody)
			}
		})
	}
}

func TestElevenlabs(t *testing.T) {
	t.Parallel()
	if strings.TrimSpace(os.Getenv("ELEVENLABS_API_KEY")) == "" {
		t.Skip("Skipping Elevenlabs tests because ELEVENLABS_API_KEY is not set")
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	realtimeAgentID := strings.TrimSpace(os.Getenv("ELEVENLABS_AGENT_ID"))
	hasRealtimeAgent := false

	testConfig := llmtests.ComprehensiveTestConfig{
		Provider:             schemas.Elevenlabs,
		SpeechSynthesisModel: "eleven_turbo_v2_5",
		TranscriptionModel:   "scribe_v1",
		RealtimeModel:        realtimeAgentID,
		SpeechSessionModel:   "eleven_v4_turbo",
		Scenarios: llmtests.TestScenarios{
			TextCompletion:        false,
			TextCompletionStream:  false,
			SimpleChat:            false,
			CompletionStream:      false,
			MultiTurnConversation: false,
			ToolCalls:             false,
			MultipleToolCalls:     false,
			End2EndToolCalling:    false,
			AutomaticFunctionCall: false,
			ImageURL:              false,
			ImageBase64:           false,
			MultipleImages:        false,
			CompleteEnd2End:       false,
			SpeechSynthesis:       true,
			SpeechSynthesisStream: true,
			Transcription:         true,
			TranscriptionStream:   false,
			Embedding:             false,
			Reasoning:             false,
			ListModels:            false,
			Realtime:              hasRealtimeAgent,
			SpeechSession:         true,
		},
	}

	t.Run("ElevenlabsTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
}

// The with-timestamps endpoint reports character timing in seconds as
// character_start_times_seconds / character_end_times_seconds. Speech must pass both
// alignments through to the client under the same names and units.
func TestElevenlabsSpeechWithTimestampsAlignment(t *testing.T) {
	t.Parallel()

	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"audio_base64": "AAAA",
			"alignment": {"characters": ["H", "i"], "character_start_times_seconds": [0.0, 0.18], "character_end_times_seconds": [0.18, 0.8]},
			"normalized_alignment": {"characters": ["H", "i"], "character_start_times_seconds": [0.0, 0.18], "character_end_times_seconds": [0.18, 0.8]}
		}`))
	}))
	defer server.Close()

	provider := elevenlabs.NewElevenlabsProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL, DefaultRequestTimeoutInSeconds: 30},
	}, bifrost.NewNoOpLogger())
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	key := schemas.Key{Value: *schemas.NewSecretVar("xi-test")}

	response, bifrostErr := provider.Speech(ctx, key, &schemas.BifrostSpeechRequest{
		Provider: schemas.Elevenlabs,
		Model:    "eleven_v4",
		Input:    &schemas.SpeechInput{Input: "Hi"},
		Params: &schemas.SpeechParameters{
			VoiceConfig:    &schemas.SpeechVoiceInput{Voice: schemas.Ptr("voice-id")},
			WithTimestamps: schemas.Ptr(true),
		},
	})
	require.Nil(t, bifrostErr)
	require.Equal(t, "/v1/text-to-speech/voice-id/with-timestamps", gotPath)
	require.NotNil(t, response.AudioBase64)
	require.Equal(t, "AAAA", *response.AudioBase64)

	// Assert on the JSON a client receives, field names included.
	body, err := sonic.Marshal(response)
	require.NoError(t, err)
	var got map[string]interface{}
	require.NoError(t, sonic.Unmarshal(body, &got))

	want := map[string]interface{}{
		"characters":                    []interface{}{"H", "i"},
		"character_start_times_seconds": []interface{}{0.0, 0.18},
		"character_end_times_seconds":   []interface{}{0.18, 0.8},
	}
	for _, field := range []string{"alignment", "normalized_alignment"} {
		t.Run(field, func(t *testing.T) {
			require.Equal(t, want, got[field])
		})
	}
}

// fakeSpeechUpstream is a provider socket a test drives: it feeds frames in and records what the session writes.
type fakeSpeechUpstream struct {
	reads     chan []byte
	writes    chan string
	closed    chan struct{}
	linger    chan struct{} // when set, reads keep delivering after Close until it is closed
	closeOnce sync.Once
}

func newFakeSpeechUpstream() *fakeSpeechUpstream {
	return &fakeSpeechUpstream{reads: make(chan []byte, 256), writes: make(chan string, 256), closed: make(chan struct{})}
}

func (f *fakeSpeechUpstream) ReadMessage() (int, []byte, error) {
	select {
	case frame := <-f.reads:
		return 1, frame, nil
	case <-f.closed:
		if f.linger == nil {
			return 0, nil, io.EOF
		}
		select {
		case frame := <-f.reads:
			return 1, frame, nil
		case <-f.linger:
			return 0, nil, io.EOF
		}
	}
}

func (f *fakeSpeechUpstream) WriteMessage(_ int, data []byte) error {
	select {
	case <-f.closed:
		return errors.New("closed")
	default:
	}
	f.writes <- string(data)
	return nil
}

func (f *fakeSpeechUpstream) Close() error {
	f.closeOnce.Do(func() { close(f.closed) })
	return nil
}

func (f *fakeSpeechUpstream) send(frame string) { f.reads <- []byte(frame) }

// fakeSpeechDialer hands out a fresh fake socket for every dial and remembers how it was dialed.
type fakeSpeechDialer struct {
	mu      sync.Mutex
	sockets []*fakeSpeechUpstream
	urls    []string
	headers []map[string]string
	err     *schemas.BifrostError
}

func (d *fakeSpeechDialer) dial(_ *schemas.BifrostContext, url string, headers map[string]string) (schemas.RealtimeUpstream, *schemas.BifrostError) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.err != nil {
		return nil, d.err
	}
	socket := newFakeSpeechUpstream()
	d.sockets = append(d.sockets, socket)
	d.urls = append(d.urls, url)
	d.headers = append(d.headers, headers)
	return socket, nil
}

func (d *fakeSpeechDialer) socket(i int) *fakeSpeechUpstream {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.sockets[i]
}

func (d *fakeSpeechDialer) dials() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.sockets)
}

func speechSessionProvider() *elevenlabs.ElevenlabsProvider {
	return elevenlabs.NewElevenlabsProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL:      "https://eleven.example.test",
			ExtraHeaders: map[string]schemas.SecretVar{"x-residency": *schemas.NewSecretVar("in")},
		},
	}, bifrost.NewNoOpLogger())
}

func speechSessionRequest(params *schemas.SpeechParameters) *schemas.BifrostSpeechRequest {
	return &schemas.BifrostSpeechRequest{Provider: schemas.Elevenlabs, Model: "eleven_v4_turbo", Params: params}
}

func speechSessionVoice(voice string) *schemas.SpeechParameters {
	return &schemas.SpeechParameters{VoiceConfig: &schemas.SpeechVoiceInput{Voice: &voice}, ResponseFormat: "pcm_24000"}
}

// openSpeechSession opens a session on a fake dialer; call it inside a synctest bubble.
func openSpeechSession(t *testing.T, params *schemas.SpeechParameters) (schemas.SpeechSession, *fakeSpeechDialer) {
	t.Helper()
	dialer := &fakeSpeechDialer{}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	t.Cleanup(ctx.Cancel)
	session, bifrostErr := speechSessionProvider().OpenSpeechSession(ctx, schemas.Key{Value: *schemas.NewSecretVar("test-key")}, speechSessionRequest(params), dialer.dial)
	if bifrostErr != nil {
		t.Fatal(bifrostErr.GetErrorString())
	}
	return session, dialer
}

func nextSpeechSessionWrite(t *testing.T, socket *fakeSpeechUpstream) string {
	t.Helper()
	synctest.Wait()
	select {
	case frame := <-socket.writes:
		return frame
	default:
		t.Fatal("the session wrote nothing")
		return ""
	}
}

func assertNoSpeechSessionWrite(t *testing.T, socket *fakeSpeechUpstream) {
	t.Helper()
	synctest.Wait()
	select {
	case frame := <-socket.writes:
		t.Fatalf("unexpected write %s", frame)
	default:
	}
}

// nextSpeechSessionEvent returns the next event as "type:segment", with the audio when there is any.
func nextSpeechSessionEvent(t *testing.T, session schemas.SpeechSession) *schemas.BifrostSpeechSessionEvent {
	t.Helper()
	synctest.Wait()
	select {
	case event, ok := <-session.Events():
		if !ok {
			t.Fatal("events are closed")
		}
		return event
	default:
		t.Fatal("the session sent no event")
		return nil
	}
}

func assertSpeechSessionEvent(t *testing.T, session schemas.SpeechSession, eventType schemas.SpeechSessionEventType, segment int) *schemas.BifrostSpeechSessionEvent {
	t.Helper()
	event := nextSpeechSessionEvent(t, session)
	if event.Type != eventType || event.Segment != segment {
		t.Fatalf("got %s segment %d, want %s segment %d", event.Type, event.Segment, eventType, segment)
	}
	return event
}

func assertSpeechSessionEventsClosed(t *testing.T, session schemas.SpeechSession) {
	t.Helper()
	synctest.Wait()
	select {
	case event, ok := <-session.Events():
		if ok {
			t.Fatalf("unexpected event %+v", event)
		}
	default:
		t.Fatal("events are still open")
	}
}

func assertNoSpeechSessionEvent(t *testing.T, session schemas.SpeechSession) {
	t.Helper()
	synctest.Wait()
	select {
	case event, ok := <-session.Events():
		t.Fatalf("unexpected event %+v (open %v)", event, ok)
	default:
	}
}

func TestElevenlabsSpeechSessionOpen(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		params := speechSessionVoice("voice-1")
		params.LanguageCode = new("hi")
		params.WithTimestamps = new(true)
		params.EnableLogging = new(false) // an enterprise client's zero-retention opt-out reaches ElevenLabs
		params.ExtraParams = map[string]interface{}{"stability": 0.4}
		params.PronunciationDictionaryLocators = []schemas.SpeechPronunciationDictionaryLocator{{PronunciationDictionaryID: "dict-1", VersionID: new("v1")}}
		session, dialer := openSpeechSession(t, params)
		defer session.Close()

		if dialer.dials() != 1 {
			t.Fatalf("expected one dial, got %d", dialer.dials())
		}
		wantURL := "wss://eleven.example.test/v1/text-to-dialogue/stream-input?enable_logging=false&language_code=hi&model_id=eleven_v4_turbo&output_format=pcm_24000&sync_alignment=true"
		if dialer.urls[0] != wantURL {
			t.Fatalf("url = %s, want %s", dialer.urls[0], wantURL)
		}
		if dialer.headers[0]["xi-api-key"] != "test-key" || dialer.headers[0]["x-residency"] != "in" {
			t.Fatalf("headers = %v", dialer.headers[0])
		}
		wantConfig := `{"voices":["voice-1"],"voice_settings":{"stability":0.4},"pronunciation_dictionary_locators":[{"pronunciation_dictionary_id":"dict-1","version_id":"v1"}]}`
		if got := nextSpeechSessionWrite(t, dialer.socket(0)); got != wantConfig {
			t.Fatalf("registration = %s, want %s", got, wantConfig)
		}
		if format, rate := session.AudioFormat(); format != "pcm" || rate != 24000 {
			t.Fatalf("audio format = %s %d", format, rate)
		}
	})
}

func TestElevenlabsSpeechSessionOpenDefaultsAndRefusals(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		voice := "voice-2"
		session, dialer := openSpeechSession(t, &schemas.SpeechParameters{VoiceConfig: &schemas.SpeechVoiceInput{MultiVoiceConfig: []schemas.VoiceConfig{{Speaker: "a", Voice: voice}, {Speaker: "b", Voice: "voice-3"}}}})
		defer session.Close()
		if want := "wss://eleven.example.test/v1/text-to-dialogue/stream-input?model_id=eleven_v4_turbo&output_format=mp3_44100_128"; dialer.urls[0] != want {
			t.Fatalf("url = %s, want %s", dialer.urls[0], want)
		}
		if got := nextSpeechSessionWrite(t, dialer.socket(0)); got != `{"voices":["voice-2"]}` {
			t.Fatalf("registration = %s", got)
		}
		if format, rate := session.AudioFormat(); format != "mp3" || rate != 0 {
			t.Fatalf("audio format = %s %d", format, rate)
		}

		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		defer ctx.Cancel()
		provider := speechSessionProvider()
		if _, bifrostErr := provider.OpenSpeechSession(ctx, schemas.Key{}, speechSessionRequest(nil), dialer.dial); bifrostErr == nil || bifrostErr.StatusCode == nil || *bifrostErr.StatusCode != 400 {
			t.Fatalf("a session without a voice must be a 400, got %+v", bifrostErr)
		}
		sound := speechSessionRequest(speechSessionVoice("voice-1"))
		sound.Model = "eleven_text_to_sound_v2"
		if _, bifrostErr := provider.OpenSpeechSession(ctx, schemas.Key{}, sound, dialer.dial); bifrostErr == nil || bifrostErr.Error == nil || bifrostErr.Error.Code == nil || *bifrostErr.Error.Code != "unsupported_operation" {
			t.Fatalf("a sound model is not a session, got %+v", bifrostErr)
		}
		failing := &fakeSpeechDialer{err: &schemas.BifrostError{Error: &schemas.ErrorField{Message: "dial refused"}}}
		if _, bifrostErr := provider.OpenSpeechSession(ctx, schemas.Key{}, speechSessionRequest(speechSessionVoice("voice-1")), failing.dial); bifrostErr == nil || bifrostErr.Error.Message != "dial refused" {
			t.Fatalf("a dial failure must reach the caller, got %+v", bifrostErr)
		}
	})
}

func TestElevenlabsSpeechSessionTextFlushAndNewTurn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		session, dialer := openSpeechSession(t, speechSessionVoice("voice-1"))
		defer session.Close()
		socket := dialer.socket(0)
		nextSpeechSessionWrite(t, socket) // registration

		if err := session.Flush(); err != nil {
			t.Fatal(err)
		}
		if err := session.SendText(""); err != nil {
			t.Fatal(err)
		}
		assertNoSpeechSessionWrite(t, socket)

		if err := session.SendText("Hello there, "); err != nil {
			t.Fatal(err)
		}
		if got := nextSpeechSessionWrite(t, socket); got != `{"inputs":[{"text":"Hello there, ","voice_id":"voice-1","new_turn":false}]}` {
			t.Fatalf("text frame = %s", got)
		}
		if err := session.Flush(); err != nil {
			t.Fatal(err)
		}
		if got := nextSpeechSessionWrite(t, socket); got != `{"flush":true}` {
			t.Fatalf("flush frame = %s", got)
		}
		if err := session.Flush(); err != nil {
			t.Fatal(err)
		}
		assertNoSpeechSessionWrite(t, socket) // nothing was sent since the flush

		if err := session.SendText("and then more."); err != nil {
			t.Fatal(err)
		}
		if got := nextSpeechSessionWrite(t, socket); got != `{"inputs":[{"text":"and then more.","voice_id":"voice-1","new_turn":true}]}` {
			t.Fatalf("text after a flush must start a turn, got %s", got)
		}
		if err := session.SendText("Same turn."); err != nil {
			t.Fatal(err)
		}
		if got := nextSpeechSessionWrite(t, socket); got != `{"inputs":[{"text":"Same turn.","voice_id":"voice-1","new_turn":false}]}` {
			t.Fatalf("only the first text after a flush starts a turn, got %s", got)
		}
	})
}

func drainSpeechSessionWrites(t *testing.T, socket *fakeSpeechUpstream) []string {
	t.Helper()
	synctest.Wait()
	var frames []string
	for {
		select {
		case frame := <-socket.writes:
			frames = append(frames, frame)
		default:
			return frames
		}
	}
}

func TestElevenlabsSpeechSessionSegmentsAndEnd(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		session, dialer := openSpeechSession(t, speechSessionVoice("voice-1"))
		socket := dialer.socket(0)

		if err := session.SendText("First sentence is here. "); err != nil {
			t.Fatal(err)
		}
		socket.send(`{"audio":"QUFB"}`)
		socket.send(`{"audio":""}`) // empty audio frames are dropped
		socket.send(`{"audio":"QkJC"}`)
		if event := assertSpeechSessionEvent(t, session, schemas.SpeechSessionEventAudioDelta, 1); event.Audio != "QUFB" {
			t.Fatalf("audio = %s", event.Audio)
		}
		assertSpeechSessionEvent(t, session, schemas.SpeechSessionEventAudioDelta, 1)
		assertNoSpeechSessionEvent(t, session)

		if err := session.Flush(); err != nil {
			t.Fatal(err)
		}
		socket.send(`{"is_final_audio_for_turn":true}`)
		assertSpeechSessionEvent(t, session, schemas.SpeechSessionEventSegmentDone, 1)

		if err := session.SendText("Second sentence follows. "); err != nil {
			t.Fatal(err)
		}
		socket.send(`{"audio":"Q0ND"}`)
		assertSpeechSessionEvent(t, session, schemas.SpeechSessionEventAudioDelta, 2)

		if err := session.End(); err != nil {
			t.Fatal(err)
		}
		writes := drainSpeechSessionWrites(t, socket)
		if len(writes) != 5 || writes[3] != `{"inputs":[{"text":"Second sentence follows. ","voice_id":"voice-1","new_turn":true}]}` || writes[4] != `{"close_socket":true}` {
			t.Fatalf("writes = %v", writes)
		}
		if err := session.SendText("too late"); err == nil {
			t.Fatal("text after End must be refused")
		}
		socket.send(`{"audio":"RERE"}`)
		socket.send(`{"is_final":true}`)
		assertSpeechSessionEvent(t, session, schemas.SpeechSessionEventAudioDelta, 2)
		assertSpeechSessionEvent(t, session, schemas.SpeechSessionEventSegmentDone, 2)
		assertSpeechSessionEvent(t, session, schemas.SpeechSessionEventAudioDone, 0)
		assertSpeechSessionEventsClosed(t, session)
		if session.Err() != nil {
			t.Fatalf("a finished session has no error, got %+v", session.Err())
		}
	})
}

func TestElevenlabsSpeechSessionEndAfterFlushClosesNoExtraSegment(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		session, dialer := openSpeechSession(t, speechSessionVoice("voice-1"))
		socket := dialer.socket(0)
		if err := session.SendText("Only sentence. "); err != nil {
			t.Fatal(err)
		}
		if err := session.Flush(); err != nil {
			t.Fatal(err)
		}
		if err := session.End(); err != nil {
			t.Fatal(err)
		}
		socket.send(`{"audio":"QUFB"}`)
		socket.send(`{"is_final_audio_for_turn":true}`)
		socket.send(`{"is_final":true}`)
		assertSpeechSessionEvent(t, session, schemas.SpeechSessionEventAudioDelta, 1)
		assertSpeechSessionEvent(t, session, schemas.SpeechSessionEventSegmentDone, 1)
		assertSpeechSessionEvent(t, session, schemas.SpeechSessionEventAudioDone, 0)
		assertSpeechSessionEventsClosed(t, session)
	})
}

func TestElevenlabsSpeechSessionEndWithoutSocket(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		session, _ := openSpeechSession(t, speechSessionVoice("voice-1"))
		if err := session.Cancel(); err != nil {
			t.Fatal(err)
		}
		assertSpeechSessionEvent(t, session, schemas.SpeechSessionEventCancelled, 0)
		if err := session.End(); err != nil {
			t.Fatal(err)
		}
		assertSpeechSessionEvent(t, session, schemas.SpeechSessionEventAudioDone, 0)
		assertSpeechSessionEventsClosed(t, session)
	})
}

func TestElevenlabsSpeechSessionCancelRedialsOnNextText(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		session, dialer := openSpeechSession(t, speechSessionVoice("voice-1"))
		defer session.Close()
		first := dialer.socket(0)
		first.linger = make(chan struct{})
		if err := session.SendText("Something the user interrupts. "); err != nil {
			t.Fatal(err)
		}
		first.send(`{"audio":"QUFB"}`)
		assertSpeechSessionEvent(t, session, schemas.SpeechSessionEventAudioDelta, 1)

		if err := session.Cancel(); err != nil {
			t.Fatal(err)
		}
		assertSpeechSessionEvent(t, session, schemas.SpeechSessionEventCancelled, 1)
		<-first.closed // the socket is closed, not flushed
		for _, frame := range drainSpeechSessionWrites(t, first) {
			if strings.Contains(frame, "flush") || strings.Contains(frame, "close_socket") {
				t.Fatalf("a cancel must not flush the socket, wrote %s", frame)
			}
		}
		first.send(`{"audio":"TEFURQ=="}`) // late audio the cancelled socket still delivers
		first.send(`{"is_final_audio_for_turn":true}`)
		synctest.Wait()
		close(first.linger)
		assertNoSpeechSessionEvent(t, session)
		if dialer.dials() != 1 {
			t.Fatalf("the session must not dial until there is text, dialed %d times", dialer.dials())
		}

		if err := session.Flush(); err != nil {
			t.Fatal(err)
		}
		if err := session.SendText("A fresh sentence for the new socket. "); err != nil {
			t.Fatal(err)
		}
		if dialer.dials() != 2 || dialer.urls[1] != dialer.urls[0] {
			t.Fatalf("expected a second dial of the same url, got %v", dialer.urls)
		}
		second := dialer.socket(1)
		writes := drainSpeechSessionWrites(t, second)
		if len(writes) != 2 || writes[0] != `{"voices":["voice-1"]}` || writes[1] != `{"inputs":[{"text":"A fresh sentence for the new socket. ","voice_id":"voice-1","new_turn":false}]}` {
			t.Fatalf("the new socket must register the voice and start a fresh turn, wrote %v", writes)
		}
		second.send(`{"audio":"QkJC"}`)
		assertSpeechSessionEvent(t, session, schemas.SpeechSessionEventAudioDelta, 2)

		if err := session.Cancel(); err != nil {
			t.Fatal(err)
		}
		assertSpeechSessionEvent(t, session, schemas.SpeechSessionEventCancelled, 2)
	})
}

func TestElevenlabsSpeechSessionCancelWhileEnding(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		session, dialer := openSpeechSession(t, speechSessionVoice("voice-1"))
		if err := session.SendText("The reply is done being written. "); err != nil {
			t.Fatal(err)
		}
		if err := session.End(); err != nil {
			t.Fatal(err)
		}
		if err := session.Cancel(); err != nil {
			t.Fatal(err)
		}
		assertSpeechSessionEvent(t, session, schemas.SpeechSessionEventCancelled, 1)
		assertSpeechSessionEvent(t, session, schemas.SpeechSessionEventAudioDone, 0)
		assertSpeechSessionEventsClosed(t, session)
		<-dialer.socket(0).closed
	})
}

func TestElevenlabsSpeechSessionKeepAlive(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		session, dialer := openSpeechSession(t, speechSessionVoice("voice-1"))
		socket := dialer.socket(0)
		nextSpeechSessionWrite(t, socket) // registration

		time.Sleep(9 * time.Second)
		assertNoSpeechSessionWrite(t, socket)
		time.Sleep(2 * time.Second)
		if got := nextSpeechSessionWrite(t, socket); got != `{"keep_alive":true}` {
			t.Fatalf("expected a keep alive after 10 s of silence, got %s", got)
		}
		// A write counts as activity.
		time.Sleep(6 * time.Second)
		if err := session.SendText("Still talking. "); err != nil {
			t.Fatal(err)
		}
		nextSpeechSessionWrite(t, socket)
		time.Sleep(6 * time.Second)
		assertNoSpeechSessionWrite(t, socket)

		// No keep alive follows the end of input.
		time.Sleep(6 * time.Second)
		if err := session.End(); err != nil {
			t.Fatal(err)
		}
		if got := nextSpeechSessionWrite(t, socket); got != `{"close_socket":true}` {
			t.Fatalf("expected close_socket, got %s", got)
		}
		time.Sleep(time.Minute)
		assertNoSpeechSessionWrite(t, socket)
		session.Close()
	})
}

func TestElevenlabsSpeechSessionErrorFrame(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		session, dialer := openSpeechSession(t, speechSessionVoice("voice-1"))
		socket := dialer.socket(0)
		if err := session.SendText("Text for a voice nobody registered. "); err != nil {
			t.Fatal(err)
		}
		socket.send(`{"message":"voice is not registered","error":"unregistered_voice","code":1008,"param":"voice_id"}`)
		assertSpeechSessionEventsClosed(t, session)
		bifrostErr := session.Err()
		if bifrostErr == nil || bifrostErr.Error == nil {
			t.Fatal("an error frame must end the session with an error")
		}
		if bifrostErr.Error.Message != "voice is not registered" || bifrostErr.Error.Type == nil || *bifrostErr.Error.Type != "unregistered_voice" || bifrostErr.Error.Param != "voice_id" {
			t.Fatalf("error = %+v", bifrostErr.Error)
		}
		if bifrostErr.StatusCode == nil || *bifrostErr.StatusCode != 400 {
			t.Fatalf("a refused request is a 400, got %v", bifrostErr.StatusCode)
		}
		if bifrostErr.ExtraFields.Provider != schemas.Elevenlabs || bifrostErr.ExtraFields.RequestType != schemas.SpeechStreamRequest {
			t.Fatalf("extra fields = %+v", bifrostErr.ExtraFields)
		}
		if err := session.SendText("more"); err == nil {
			t.Fatal("a failed session must refuse text")
		}
		<-socket.closed
	})
}

func TestElevenlabsSpeechSessionUnexpectedClose(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		session, dialer := openSpeechSession(t, speechSessionVoice("voice-1"))
		if err := session.SendText("Text before the provider drops. "); err != nil {
			t.Fatal(err)
		}
		_ = dialer.socket(0).Close()
		assertSpeechSessionEventsClosed(t, session)
		if bifrostErr := session.Err(); bifrostErr == nil || bifrostErr.Error == nil || bifrostErr.Error.Message != schemas.ErrProviderStreamTruncated {
			t.Fatalf("a socket that drops before is_final is a truncated stream, got %+v", bifrostErr)
		}
	})
}

func TestElevenlabsSpeechSessionRedialFailureEndsSession(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		session, dialer := openSpeechSession(t, speechSessionVoice("voice-1"))
		if err := session.Cancel(); err != nil {
			t.Fatal(err)
		}
		assertSpeechSessionEvent(t, session, schemas.SpeechSessionEventCancelled, 0)
		dialer.mu.Lock()
		dialer.err = &schemas.BifrostError{Error: &schemas.ErrorField{Message: "dial refused"}}
		dialer.mu.Unlock()
		if err := session.SendText("Text that needs a new socket. "); err == nil || err.Error() != "dial refused" {
			t.Fatalf("expected the dial failure, got %v", err)
		}
		assertSpeechSessionEventsClosed(t, session)
		if bifrostErr := session.Err(); bifrostErr == nil || bifrostErr.Error.Message != "dial refused" {
			t.Fatalf("the dial failure must end the session, got %+v", bifrostErr)
		}
	})
}

func TestElevenlabsSpeechSessionCloseIsIdempotent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		session, dialer := openSpeechSession(t, speechSessionVoice("voice-1"))
		// Fill the event buffer so the reader is blocked sending when the session closes.
		if err := session.SendText("Text whose audio nobody reads. "); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 80; i++ {
			dialer.socket(0).send(`{"audio":"QUFB"}`)
		}
		synctest.Wait()
		if err := session.Close(); err != nil {
			t.Fatal(err)
		}
		if err := session.Close(); err != nil {
			t.Fatal(err)
		}
		for range session.Events() { // drains what was queued, then ends
		}
		if session.Err() != nil {
			t.Fatalf("a closed session has no error, got %+v", session.Err())
		}
		for name, call := range map[string]func() error{"text": func() error { return session.SendText("x") }, "flush": session.Flush, "end": session.End, "cancel": session.Cancel} {
			if call() == nil {
				t.Fatalf("%s on a closed session must fail", name)
			}
		}
		<-dialer.socket(0).closed
		// The bubble ends only when the reader and keep alive goroutines have exited.
	})
}

func TestElevenlabsSpeechSessionAlignment(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		session, dialer := openSpeechSession(t, speechSessionVoice("voice-1"))
		defer session.Close()
		socket := dialer.socket(0)
		socket.send(`{"audio":"QUFB","alignment":{"chars":["H","i"],"char_start_times_ms":[0,50],"char_durations_ms":[50,70]},"normalized_alignment":{"chars":["h"],"char_start_times_ms":[0],"char_durations_ms":[1]}}`)
		socket.send(`{"audio":"","alignment":{"chars":["!"],"char_start_times_ms":[120],"char_durations_ms":[30]}}`)
		first := assertSpeechSessionEvent(t, session, schemas.SpeechSessionEventAudioDelta, 1)
		if first.Alignment == nil || strings.Join(first.Alignment.Characters, "") != "Hi" ||
			first.Alignment.CharacterStartTimesSeconds[1] != 0.05 || first.Alignment.CharacterEndTimesSeconds[0] != 0.05 || first.Alignment.CharacterEndTimesSeconds[1] != 0.12 {
			t.Fatalf("alignment = %+v", first.Alignment)
		}
		second := assertSpeechSessionEvent(t, session, schemas.SpeechSessionEventAudioDelta, 1)
		if second.Audio != "" || second.Alignment == nil || second.Alignment.CharacterEndTimesSeconds[0] != 0.15 {
			t.Fatalf("an alignment without audio must still be delivered, got %+v", second)
		}
	})
}

func BenchmarkElevenlabsSpeechSessionFrames(b *testing.B) {
	audio := strings.Repeat("QUFB", 4096) // a 12 KB audio chunk, as base64
	for name, frame := range map[string]string{
		"audio":     `{"audio":"` + audio + `"}`,
		"alignment": `{"audio":"` + audio + `","alignment":{"chars":["H","e","l","l","o"," ","t","h","e","r","e"],"char_start_times_ms":[0,40,80,120,160,200,240,280,320,360,400],"char_durations_ms":[40,40,40,40,40,40,40,40,40,40,40]}}`,
	} {
		b.Run(name, func(b *testing.B) {
			dialer := &fakeSpeechDialer{}
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			defer ctx.Cancel()
			session, bifrostErr := speechSessionProvider().OpenSpeechSession(ctx, schemas.Key{}, speechSessionRequest(speechSessionVoice("voice-1")), dialer.dial)
			if bifrostErr != nil {
				b.Fatal(bifrostErr.GetErrorString())
			}
			defer session.Close()
			socket := dialer.socket(0)
			payload := []byte(frame)
			b.ReportAllocs()
			b.SetBytes(int64(len(payload)))
			for b.Loop() {
				socket.reads <- payload
				<-session.Events()
			}
		})
	}
}
