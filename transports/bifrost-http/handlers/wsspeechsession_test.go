package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	ws "github.com/fasthttp/websocket"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// fakeSpeechSession records the calls the relay makes and plays back the events a test queues.
type fakeSpeechSession struct {
	mu      sync.Mutex
	calls   []string
	textErr error // what SendText refuses with, once set
	events  chan *schemas.BifrostSpeechSessionEvent
	err     *schemas.BifrostError
	once    sync.Once
}

func newFakeSpeechSession() *fakeSpeechSession {
	return &fakeSpeechSession{events: make(chan *schemas.BifrostSpeechSessionEvent, 16)}
}

func (f *fakeSpeechSession) record(call string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call)
	return nil
}

func (f *fakeSpeechSession) recorded() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeSpeechSession) refuseText(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.textErr = err
}

func (f *fakeSpeechSession) SendText(text string) error {
	f.mu.Lock()
	err := f.textErr
	f.mu.Unlock()
	if err != nil {
		return err
	}
	return f.record("text:" + text)
}

func (f *fakeSpeechSession) AudioFormat() (string, int)                        { return "pcm", 24000 }
func (f *fakeSpeechSession) Flush() error                                      { return f.record("flush") }
func (f *fakeSpeechSession) End() error                                        { return f.record("end") }
func (f *fakeSpeechSession) Cancel() error                                     { return f.record("cancel") }
func (f *fakeSpeechSession) Events() <-chan *schemas.BifrostSpeechSessionEvent { return f.events }
func (f *fakeSpeechSession) Err() *schemas.BifrostError                        { return f.err }
func (f *fakeSpeechSession) Close() error {
	f.once.Do(func() { close(f.events) })
	return nil
}

// speechRelayFixture runs a session's relays against a real client connection, as serve does.
type speechRelayFixture struct {
	speech *fakeSpeechSession
	run    *speechSessionRun
	app    *ws.Conn
	done   chan error
}

func startSpeechRelay(t *testing.T, idleTimeout time.Duration) *speechRelayFixture {
	t.Helper()
	bifrostSide, app, cleanup := dialRealtimeTestConn(t)
	t.Cleanup(cleanup)
	speech := newFakeSpeechSession()
	clientConn := newRealtimeClientConn(bifrostSide)
	run := &speechSessionRun{clientConn: clientConn, speech: speech, idleTimeout: idleTimeout}
	done := make(chan error, 1)
	go func() {
		done <- runRealtimeRelays(bifrostSide, run.relayClient, run.relayEvents, func() {
			_ = clientConn.Close()
			_ = speech.Close()
		})
	}()
	return &speechRelayFixture{speech: speech, run: run, app: app, done: done}
}

func (f *speechRelayFixture) waitDone(t *testing.T) {
	t.Helper()
	select {
	case <-f.done:
	case <-time.After(5 * time.Second):
		t.Fatal("relay did not end")
	}
}

func readSpeechEvent(t *testing.T, conn *ws.Conn) map[string]any {
	t.Helper()
	var event map[string]any
	require.NoError(t, json.Unmarshal([]byte(readFrame(t, conn)), &event))
	return event
}

func TestParseSpeechSessionConfig(t *testing.T) {
	t.Parallel()
	frame := []byte(`{"type":"speech.config","model":"elevenlabs/eleven_v4_turbo","voice":"voice-1","response_format":"pcm","speed":1.1,"language_code":"hi","with_timestamps":true,"enable_logging":false,"stability":0.5,"input":"ignored"}`)
	req, err := parseSpeechSessionConfig(frame)
	require.NoError(t, err)
	assert.Equal(t, schemas.Elevenlabs, req.Provider)
	assert.Equal(t, "eleven_v4_turbo", req.Model)
	require.NotNil(t, req.Input)
	assert.Empty(t, req.Input.Input, "a session's text arrives in speech.text events, never in the config")
	require.NotNil(t, req.Params)
	assert.Equal(t, "voice-1", *req.Params.VoiceConfig.Voice)
	assert.Equal(t, "pcm", req.Params.ResponseFormat)
	assert.InDelta(t, 1.1, *req.Params.Speed, 0)
	assert.Equal(t, "hi", *req.Params.LanguageCode)
	assert.True(t, *req.Params.WithTimestamps)
	require.NotNil(t, req.Params.EnableLogging, "a client's zero-retention opt-out is a typed parameter, which the provider forwards")
	assert.False(t, *req.Params.EnableLogging)
	assert.Equal(t, 0.5, req.Params.ExtraParams["stability"], "fields no type models reach the provider as extra params")
	assert.NotContains(t, req.Params.ExtraParams, "type")
	assert.NotContains(t, req.Params.ExtraParams, "model")
	assert.NotContains(t, req.Params.ExtraParams, "input")

	req, err = parseSpeechSessionConfig([]byte(`{"type":"speech.config","model":"eleven_v4_turbo"}`))
	require.NoError(t, err)
	assert.Empty(t, req.Provider, "a model without a prefix is left for routing to resolve")
	assert.Equal(t, "eleven_v4_turbo", req.Model)
	assert.NotNil(t, req.Params)

	_, err = parseSpeechSessionConfig([]byte(`{"type":`))
	assert.Error(t, err)
}

func TestReadSpeechSessionConfig(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		send    func(t *testing.T, conn *ws.Conn)
		wantErr string
	}{
		"config": {
			send: func(t *testing.T, conn *ws.Conn) {
				sendFrame(t, conn, `{"type":"speech.config","model":"elevenlabs/eleven_v4_turbo"}`)
			},
		},
		"another event first": {
			send:    func(t *testing.T, conn *ws.Conn) { sendFrame(t, conn, `{"type":"speech.text","text":"hi"}`) },
			wantErr: "must be speech.config",
		},
		"not JSON": {
			send:    func(t *testing.T, conn *ws.Conn) { sendFrame(t, conn, `{`) },
			wantErr: "must be speech.config",
		},
		"binary": {
			send: func(t *testing.T, conn *ws.Conn) {
				require.NoError(t, conn.WriteMessage(ws.BinaryMessage, []byte{1, 2, 3}))
			},
			wantErr: "only accept text messages",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			bifrostSide, app, cleanup := dialRealtimeTestConn(t)
			t.Cleanup(cleanup)
			tc.send(t, app)
			frame, err := readSpeechSessionConfig(newRealtimeClientConn(bifrostSide))
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Contains(t, string(frame), "speech.config")
		})
	}
}

// The client's events reach the provider session in order, its characters are counted by runes, and
// the event that finishes the session carries them.
func TestSpeechSessionRelayAppliesEventsAndReportsUsage(t *testing.T) {
	t.Parallel()
	f := startSpeechRelay(t, time.Minute)
	for _, frame := range []string{
		`{"type":"speech.text","text":"héllo "}`,
		`{"type":"speech.flush"}`,
		`{"type":"speech.text","text":"wörld"}`,
		`{"type":"speech.cancel"}`,
		`{"type":"speech.end"}`,
	} {
		sendFrame(t, f.app, frame)
	}
	require.Eventually(t, func() bool { return len(f.speech.recorded()) == 5 }, 5*time.Second, 5*time.Millisecond)
	assert.Equal(t, []string{"text:héllo ", "flush", "text:wörld", "cancel", "end"}, f.speech.recorded())

	f.speech.events <- &schemas.BifrostSpeechSessionEvent{Type: schemas.SpeechSessionEventAudioDelta, Segment: 1, Audio: "QUJD"}
	f.speech.events <- &schemas.BifrostSpeechSessionEvent{Type: schemas.SpeechSessionEventSegmentDone, Segment: 1}
	f.speech.events <- &schemas.BifrostSpeechSessionEvent{Type: schemas.SpeechSessionEventCancelled, Segment: 2}
	f.speech.events <- &schemas.BifrostSpeechSessionEvent{Type: schemas.SpeechSessionEventAudioDone}

	delta := readSpeechEvent(t, f.app)
	assert.Equal(t, "speech.audio.delta", delta["type"])
	assert.EqualValues(t, 1, delta["segment"])
	assert.Equal(t, "QUJD", delta["audio"])
	assert.Equal(t, "speech.segment.done", readSpeechEvent(t, f.app)["type"])
	cancelled := readSpeechEvent(t, f.app)
	assert.Equal(t, "speech.cancelled", cancelled["type"])
	assert.EqualValues(t, 2, cancelled["segment"])
	done := readSpeechEvent(t, f.app)
	assert.Equal(t, "speech.audio.done", done["type"])
	assert.EqualValues(t, 11, done["usage"].(map[string]any)["input_chars"], "characters are counted as runes, not bytes")

	// The finished session is closed with a normal close frame.
	require.NoError(t, f.app.SetReadDeadline(time.Now().Add(5*time.Second)))
	_, _, err := f.app.ReadMessage()
	require.Error(t, err)
	assert.True(t, ws.IsCloseError(err, ws.CloseNormalClosure), "got %v", err)
	f.waitDone(t)
	assert.Nil(t, f.run.failure)
	assert.EqualValues(t, 11, f.run.chars.Load())
}

// A bad event is answered with an error event and the session carries on.
func TestSpeechSessionRelayAnswersInvalidEvents(t *testing.T) {
	t.Parallel()
	f := startSpeechRelay(t, time.Minute)
	require.NoError(t, f.app.WriteMessage(ws.BinaryMessage, []byte{1}))
	sendFrame(t, f.app, `{`)
	sendFrame(t, f.app, `{"type":"speech.config"}`)
	sendFrame(t, f.app, `{"type":"speech.text","text":"still here"}`)
	for range 3 {
		event := readSpeechEvent(t, f.app)
		assert.Equal(t, "error", event["type"])
	}
	require.Eventually(t, func() bool { return len(f.speech.recorded()) == 1 }, 5*time.Second, 5*time.Millisecond)
	assert.Equal(t, []string{"text:still here"}, f.speech.recorded())
	_ = f.app.Close()
	f.waitDone(t)
}

// Text the provider session refuses was never sent, so it is not counted or billed.
func TestSpeechSessionRelayDoesNotCountRefusedText(t *testing.T) {
	t.Parallel()
	f := startSpeechRelay(t, time.Minute)
	sendFrame(t, f.app, `{"type":"speech.text","text":"héllo"}`)
	require.Eventually(t, func() bool { return len(f.speech.recorded()) == 1 }, 5*time.Second, 5*time.Millisecond)
	f.speech.refuseText(errors.New("the session has ended its input"))
	sendFrame(t, f.app, `{"type":"speech.text","text":"too late"}`)
	refused := readSpeechEvent(t, f.app)
	assert.Equal(t, "error", refused["type"])

	f.speech.events <- &schemas.BifrostSpeechSessionEvent{Type: schemas.SpeechSessionEventAudioDone}
	done := readSpeechEvent(t, f.app)
	assert.Equal(t, "speech.audio.done", done["type"])
	assert.EqualValues(t, 5, done["usage"].(map[string]any)["input_chars"], "only the accepted text is counted")
	f.waitDone(t)
	assert.EqualValues(t, 5, f.run.chars.Load())
}

// A provider that fails the session is reported to the client and to the settlement.
func TestSpeechSessionRelayReportsProviderFailure(t *testing.T) {
	t.Parallel()
	f := startSpeechRelay(t, time.Minute)
	f.speech.err = newRealtimeWireBifrostError(502, "server_error", "provider hung up")
	f.speech.events <- &schemas.BifrostSpeechSessionEvent{Type: schemas.SpeechSessionEventAudioDelta, Segment: 1, Audio: "QUJD"}
	_ = f.speech.Close()
	assert.Equal(t, "speech.audio.delta", readSpeechEvent(t, f.app)["type"])
	failure := readFrame(t, f.app)
	assert.Contains(t, failure, `"type":"error"`)
	assert.Contains(t, failure, "provider hung up")
	f.waitDone(t)
	require.NotNil(t, f.run.failure)
	assert.Equal(t, 502, *f.run.failure.StatusCode)
}

// A client that goes silent is told so and the session ends, whatever the heartbeat says.
func TestSpeechSessionRelayEndsWhenClientIsIdle(t *testing.T) {
	t.Parallel()
	f := startSpeechRelay(t, 50*time.Millisecond)
	event := readFrame(t, f.app)
	assert.Contains(t, event, `"type":"error"`)
	assert.Contains(t, event, "idle")
	f.waitDone(t)
}

// speechHookRecorder observes the session's request through the plugin pipeline.
type speechHookRecorder struct {
	mu          sync.Mutex
	preType     schemas.RequestType
	preProvider schemas.ModelProvider
	preModel    string
	preInput    string
	preStream   bool
	postType    schemas.RequestType
	postUsage   *schemas.SpeechUsage
	postFinal   bool
	postKeyName string
	postErr     *schemas.BifrostError
	posts       int
}

func (*speechHookRecorder) GetName() string { return "speech-session-recorder" }
func (*speechHookRecorder) Cleanup() error  { return nil }
func (*speechHookRecorder) PreRequestHook(*schemas.BifrostContext, *schemas.BifrostRequest) error {
	return nil
}
func (r *speechHookRecorder) PreLLMHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.preType = req.RequestType
	r.preProvider, r.preModel, _ = req.GetRequestFields()
	r.preInput = req.SpeechRequest.Input.Input
	r.preStream = bifrost.IsFinalChunk(ctx)
	return req, nil, nil
}
func (r *speechHookRecorder) PostLLMHook(ctx *schemas.BifrostContext, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.posts++
	r.postType, _, _, _ = bifrost.GetResponseFields(resp, bifrostErr)
	r.postFinal = bifrost.IsFinalChunk(ctx)
	r.postKeyName, _ = ctx.Value(schemas.BifrostContextKeySelectedKeyName).(string)
	r.postErr = bifrostErr
	if resp != nil && resp.SpeechStreamResponse != nil {
		r.postUsage = resp.SpeechStreamResponse.Usage
	}
	return resp, bifrostErr, nil
}

// The session is one speech stream request: admitted by the pre-hooks when it opens, and settled by
// post-hooks on the final chunk with the characters the client sent, whatever ended it.
func TestSpeechSessionUnitIsOneSpeechStreamRequest(t *testing.T) {
	t.Parallel()
	recorder := &speechHookRecorder{}
	client, err := bifrost.Init(context.Background(), schemas.BifrostConfig{Account: splitKeysAccount{}, LLMPlugins: []schemas.LLMPlugin{recorder}, Logger: bifrost.NewDefaultLogger(schemas.LogLevelError)})
	require.NoError(t, err)
	defer client.Shutdown()
	req := &schemas.BifrostSpeechRequest{Provider: schemas.OpenAI, Model: "gpt-4o-mini-tts", Input: &schemas.SpeechInput{}, Params: &schemas.SpeechParameters{}}
	key := schemas.Key{ID: "every-model", Name: "every-model"}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	unit, bifrostErr := openSpeechSessionUnit(client, ctx, "speech_1", req)
	require.Nil(t, bifrostErr)
	assert.Equal(t, schemas.SpeechStreamRequest, recorder.preType)
	assert.Equal(t, schemas.OpenAI, recorder.preProvider)
	assert.Equal(t, "gpt-4o-mini-tts", recorder.preModel)
	assert.Empty(t, recorder.preInput, "the session's text is not in the admitted request")
	assert.False(t, recorder.preStream, "the pre-hooks run before the stream ends")
	assert.Equal(t, 0, recorder.posts)

	unit.settle(req, key, 42, nil)
	assert.Equal(t, 1, recorder.posts)
	assert.Equal(t, schemas.SpeechStreamRequest, recorder.postType)
	assert.True(t, recorder.postFinal, "the session settles as the final chunk of its stream")
	assert.Equal(t, "every-model", recorder.postKeyName)
	require.NotNil(t, recorder.postUsage)
	assert.Equal(t, 42, recorder.postUsage.InputChars)
	assert.Nil(t, recorder.postErr)

	// A failure settles as an error, and the post-hooks still run exactly once.
	unit, bifrostErr = openSpeechSessionUnit(client, schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), "speech_2", req)
	require.Nil(t, bifrostErr)
	failure := newRealtimeWireBifrostError(502, "server_error", "provider hung up")
	recorder.postUsage = nil
	unit.settle(req, key, 7, failure)
	assert.Equal(t, 2, recorder.posts)
	require.NotNil(t, recorder.postErr)
	assert.Equal(t, 502, *recorder.postErr.StatusCode)
	assert.Equal(t, schemas.RequestType(""), failure.ExtraFields.RequestType, "the error sent to the client is not rewritten")
	require.NotNil(t, recorder.postUsage, "the characters the client sent are billed when the provider fails the session")
	assert.Equal(t, 7, recorder.postUsage.InputChars)

	// A session that failed before any text was sent has nothing to bill.
	unit, bifrostErr = openSpeechSessionUnit(client, schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), "speech_3", req)
	require.Nil(t, bifrostErr)
	recorder.postUsage = nil
	unit.settle(req, key, 0, failure)
	assert.Equal(t, 3, recorder.posts)
	require.NotNil(t, recorder.postErr)
	assert.Nil(t, recorder.postUsage)
}

// BenchmarkSpeechSessionWriteEvent is the gateway's own cost of one audio event, a 16 KiB frame of
// base64 as Soniox sends them, from the provider session's event to the client's socket. The client end
// drains in the same process, so its reads count in allocs/op too.
func BenchmarkSpeechSessionWriteEvent(b *testing.B) {
	bifrostSide, app, cleanup := dialRealtimeTestConn(b)
	defer cleanup()
	go func() {
		for {
			if _, _, err := app.ReadMessage(); err != nil {
				return
			}
		}
	}()
	run := &speechSessionRun{clientConn: newRealtimeClientConn(bifrostSide), speech: newFakeSpeechSession()}
	event := &schemas.BifrostSpeechSessionEvent{Type: schemas.SpeechSessionEventAudioDelta, Segment: 1, Audio: strings.Repeat("QUJD", 4096)}
	b.ReportAllocs()
	for b.Loop() {
		if err := run.write(event); err != nil {
			b.Fatal(err)
		}
	}
}

// The characters of a text-to-dialogue frame are the text of its inputs, counted in runes; frames that carry
// no text, such as the registration and the flush, are free.
func TestElevenlabsFrameChars(t *testing.T) {
	for _, tc := range []struct {
		name, frame string
		want        int
	}{
		{"one input", `{"inputs":[{"text":"Hello there.","voice_id":"v1"}]}`, 12},
		{"several inputs", `{"inputs":[{"text":"Hello","voice_id":"v1"},{"text":" wörld","voice_id":"v2","new_turn":true}]}`, 11},
		{"runes, not bytes", `{"inputs":[{"text":"नमस्ते","voice_id":"v1"}]}`, 6},
		{"a single-voice frame", `{"text":"Hello","flush":true}`, 5},
		{"the registration", `{"voices":["v1"],"voice_settings":{"stability":0.5}}`, 0},
		{"a flush", `{"flush":true}`, 0},
		{"a keep-alive", `{"keep_alive":true}`, 0},
		{"a close", `{"close_socket":true}`, 0},
		{"text that is not a string", `{"inputs":[{"text":7}],"text":null}`, 0},
		{"not JSON", `{"inputs":`, 0},
	} {
		if got := elevenlabsFrameChars([]byte(tc.frame)); got != tc.want {
			t.Errorf("%s: %d characters, want %d", tc.name, got, tc.want)
		}
	}
}

// BenchmarkElevenlabsFrameChars is the gateway's cost of counting one text frame of a text-to-dialogue session,
// the only per-frame work its relay adds on the client side.
func BenchmarkElevenlabsFrameChars(b *testing.B) {
	frame := []byte(`{"inputs":[{"text":"Namaste, this is a sentence of the kind a voice agent speaks.","voice_id":"21m00Tcm4TlvDq8ikWAM","new_turn":false}]}`)
	b.SetBytes(int64(len(frame)))
	b.ReportAllocs()
	for b.Loop() {
		if elevenlabsFrameChars(frame) == 0 {
			b.Fatal("no characters counted")
		}
	}
}

// runElevenlabsDialogueRelay relays a client and a provider socket pair through the session's relays, and returns
// the app's end of each, the run and a channel that closes when the relays end.
func runElevenlabsDialogueRelay(t *testing.T) (app, provider *ws.Conn, run *elevenlabsDialogueRun, ended chan struct{}) {
	t.Helper()
	clientSide, app, cleanupClient := dialRealtimeTestConn(t)
	t.Cleanup(cleanupClient)
	providerSide, provider, cleanupProvider := dialRealtimeTestConn(t)
	t.Cleanup(cleanupProvider)
	run = &elevenlabsDialogueRun{clientConn: newRealtimeClientConn(clientSide), upstream: providerSide}
	ended = make(chan struct{})
	go func() {
		defer close(ended)
		_ = runRealtimeRelays(clientSide, run.relayClient, run.relayUpstream, func() {
			_ = run.clientConn.Close()
			_ = providerSide.Close()
		})
	}()
	return app, provider, run, ended
}

// The relay passes frames both ways as they are, and bills the text the provider accepted.
func TestElevenlabsDialogueRelaysFramesAsTheyAreAndCountsTheText(t *testing.T) {
	app, provider, run, ended := runElevenlabsDialogueRelay(t)

	frames := []string{
		`{"voices":["v1"],"voice_settings":{"stability":0.4}}`,
		`{"inputs":[{"text":"Hello ","voice_id":"v1"}]}`,
		`{"inputs":[{"text":"world.","voice_id":"v1","new_turn":true}],"flush":true}`,
	}
	for _, frame := range frames {
		if err := app.WriteMessage(ws.TextMessage, []byte(frame)); err != nil {
			t.Fatal(err)
		}
		_, got, err := provider.ReadMessage()
		if err != nil || string(got) != frame {
			t.Fatalf("the provider got %q (%v), want the frame as sent: %q", got, err, frame)
		}
	}
	for _, frame := range []string{`{"audio":"QUJD","is_final_audio_for_turn":false}`, `{"audio":"","is_final_audio_for_turn":true}`, `{"is_final":true}`} {
		if err := provider.WriteMessage(ws.TextMessage, []byte(frame)); err != nil {
			t.Fatal(err)
		}
		_, got, err := app.ReadMessage()
		if err != nil || string(got) != frame {
			t.Fatalf("the client got %q (%v), want the frame as sent: %q", got, err, frame)
		}
	}
	if got := run.chars.Load(); got != 12 {
		t.Errorf("characters = %d, want 12", got)
	}

	_ = app.WriteControl(ws.CloseMessage, ws.FormatCloseMessage(ws.CloseNormalClosure, ""), time.Now().Add(time.Second))
	// Closing a hijacked test connection does not interrupt its pending read, so the provider's end of the pair goes too.
	time.AfterFunc(200*time.Millisecond, func() { _ = provider.Close() })
	select {
	case <-ended:
	case <-time.After(5 * time.Second):
		t.Fatal("the relays did not end when the client left")
	}
	if failure := run.failure.Load(); failure != nil {
		t.Errorf("a client that leaves is not a failure: %v", failure)
	}
}

// A provider that ends the session with a code other than a normal one has failed it, and the client reads the
// provider's own close, its code and reason.
func TestElevenlabsDialogueRecordsAProviderCloseAsAFailure(t *testing.T) {
	app, provider, run, ended := runElevenlabsDialogueRelay(t)

	_ = provider.WriteControl(ws.CloseMessage, ws.FormatCloseMessage(ws.ClosePolicyViolation, "Invalid voice"), time.Now().Add(time.Second))

	_ = app.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, _, err := app.ReadMessage()
	var closeErr *ws.CloseError
	if !errors.As(err, &closeErr) || closeErr.Code != ws.ClosePolicyViolation || closeErr.Text != "Invalid voice" {
		t.Fatalf("the client read %v, want the provider's close 1008 \"Invalid voice\"", err)
	}
	select {
	case <-ended:
	case <-time.After(5 * time.Second):
		t.Fatal("the relays did not end when the provider closed")
	}
	if failure := run.failure.Load(); failure == nil || failure.StatusCode == nil || *failure.StatusCode != 502 {
		t.Errorf("failure = %+v, want a 502", failure)
	}
}

// A provider that drops with no close frame sent no code: 1006 is only what the read reports, and a close frame may
// not carry it, so the client reads the gateway's own failure and a valid close instead of a frame it cannot parse.
func TestElevenlabsDialogueTurnsAnAbruptProviderDropIntoAGatewayFailure(t *testing.T) {
	app, provider, run, ended := runElevenlabsDialogueRelay(t)

	_ = provider.UnderlyingConn().Close()

	_ = app.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, frame, err := app.ReadMessage()
	require.NoError(t, err, "the client reads the gateway's error frame first")
	var failureFrame struct {
		Error string `json:"error"`
		Code  int    `json:"code"`
	}
	require.NoError(t, json.Unmarshal(frame, &failureFrame), string(frame))
	assert.Equal(t, ws.CloseInternalServerErr, failureFrame.Code)
	assert.NotEmpty(t, failureFrame.Error)

	_, _, err = app.ReadMessage()
	var closeErr *ws.CloseError
	require.True(t, errors.As(err, &closeErr), "the client then reads a close, got %v", err)
	assert.Equal(t, ws.CloseInternalServerErr, closeErr.Code, "a close with a code a frame may carry")
	select {
	case <-ended:
	case <-time.After(5 * time.Second):
		t.Fatal("the relays did not end when the provider dropped")
	}
	if failure := run.failure.Load(); failure == nil || failure.StatusCode == nil || *failure.StatusCode != 502 {
		t.Errorf("failure = %+v, want a 502", failure)
	}
}

// gatedFailingUpstream fails both of its directions once the test lets it.
type gatedFailingUpstream struct{ release chan struct{} }

func (u *gatedFailingUpstream) ReadMessage() (int, []byte, error) {
	<-u.release
	return 0, nil, errors.New("connection reset")
}

func (u *gatedFailingUpstream) WriteMessage(int, []byte) error {
	<-u.release
	return errors.New("broken pipe")
}

func (u *gatedFailingUpstream) Close() error { return nil }

// A provider that drops while the client is still sending fails both relays at once, the read of the provider and the
// write to it. The settlement records one failure, and the relays do not race to set it.
func TestElevenlabsDialogueRelaysFailTogetherWithoutRacing(t *testing.T) {
	clientSide, app, cleanupClient := dialRealtimeTestConn(t)
	t.Cleanup(cleanupClient)
	upstream := &gatedFailingUpstream{release: make(chan struct{})}
	run := &elevenlabsDialogueRun{clientConn: newRealtimeClientConn(clientSide), upstream: upstream}
	if err := app.WriteMessage(ws.TextMessage, []byte(`{"text":"hi"}`)); err != nil {
		t.Fatal(err)
	}

	var relays sync.WaitGroup
	for _, relay := range []func() error{run.relayClient, run.relayUpstream} {
		relays.Add(1)
		go func() {
			defer relays.Done()
			_ = relay()
		}()
	}
	close(upstream.release)
	relays.Wait()

	if failure := run.failure.Load(); failure == nil || failure.StatusCode == nil || *failure.StatusCode != 502 {
		t.Fatalf("failure = %+v, want a 502", failure)
	}
}

// A failure the gateway reports is a frame in the shape each ElevenLabs socket uses, and then a close.
func TestElevenlabsSocketErrorFrames(t *testing.T) {
	for _, tc := range []struct {
		name   string
		write  func(*realtimeClientConn, *schemas.BifrostError)
		status int
		want   string
		code   int
	}{
		{"scribe budget", writeElevenlabsScribeError, 402, `{"message_type":"quota_exceeded","error":"budget spent"}`, ws.CloseInternalServerErr},
		{"scribe rate limit", writeElevenlabsScribeError, 429, `{"message_type":"rate_limited","error":"slow down"}`, ws.CloseTryAgainLater},
		{"scribe model blocked", writeElevenlabsScribeError, 403, `{"message_type":"auth_error","error":"model is not allowed"}`, ws.ClosePolicyViolation},
		{"scribe bad request", writeElevenlabsScribeError, 400, `{"message_type":"invalid_request","error":"bad query"}`, ws.CloseInvalidFramePayloadData},
		{"scribe server fault", writeElevenlabsScribeError, 500, `{"message_type":"error","error":"oops"}`, ws.CloseInternalServerErr},
		{"dialogue refusal", writeElevenlabsDialogueError, 403, `{"error":"model is not allowed","code":1008}`, ws.ClosePolicyViolation},
		{"dialogue rate limit", writeElevenlabsDialogueError, 429, `{"error":"slow down","code":1013}`, ws.CloseTryAgainLater},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bifrostSide, app, cleanup := dialRealtimeTestConn(t)
			defer cleanup()
			messages := map[string]string{
				`{"message_type":"quota_exceeded","error":"budget spent"}`:     "budget spent",
				`{"message_type":"rate_limited","error":"slow down"}`:          "slow down",
				`{"message_type":"auth_error","error":"model is not allowed"}`: "model is not allowed",
				`{"message_type":"invalid_request","error":"bad query"}`:       "bad query",
				`{"message_type":"error","error":"oops"}`:                      "oops",
				`{"error":"model is not allowed","code":1008}`:                 "model is not allowed",
				`{"error":"slow down","code":1013}`:                            "slow down",
			}

			tc.write(newRealtimeClientConn(bifrostSide), newRealtimeWireBifrostError(tc.status, "decision", messages[tc.want]))

			_ = app.SetReadDeadline(time.Now().Add(5 * time.Second))
			_, got, err := app.ReadMessage()
			if err != nil || string(got) != tc.want {
				t.Fatalf("frame = %q (%v), want %q", got, err, tc.want)
			}
			_, _, err = app.ReadMessage()
			var closeErr *ws.CloseError
			if !errors.As(err, &closeErr) || closeErr.Code != tc.code {
				t.Fatalf("close = %v, want code %d", err, tc.code)
			}
		})
	}
}

// A refused handshake is written in ElevenLabs' error shape.
func TestSendElevenlabsHandshakeError(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}

	sendElevenlabsHandshakeError(ctx, newRealtimeWireBifrostError(401, "invalid_api_key", "a virtual key is required"))

	if ctx.Response.StatusCode() != 401 {
		t.Errorf("status = %d, want 401", ctx.Response.StatusCode())
	}
	if got, want := string(ctx.Response.Body()), `{"detail":{"status":"invalid_api_key","message":"a virtual key is required"}}`; got != want {
		t.Errorf("body = %s, want %s", got, want)
	}
}
