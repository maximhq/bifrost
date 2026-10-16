package realtime

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// fakeUpstream plays OpenAI's Realtime API on the routes Bifrost dials. It does only what OpenAI
// always does on its own, greeting a new connection with session.created; every other frame it
// sends comes from a test, so a test reads as the whole conversation.
//
// Sessions are told apart by model: each test connects with a model name of its own (newModel),
// which the gateway passes upstream in the dial URL, so tests run in parallel without sharing.
type fakeUpstream struct {
	srv      *http.Server
	upgrader websocket.Upgrader
	ids      atomic.Int64

	mu       sync.Mutex
	sessions map[string]chan *upstreamSession // model → the connection the gateway opened for it
	secrets  map[string]chan gjson.Result     // model → the client_secrets request the gateway sent
	refusals map[string]int                   // model → the HTTP status its dial is refused with
	dials    map[string]int                   // model → how many times the gateway dialed for it
}

func startFakeUpstream(addr string) (*fakeUpstream, error) {
	f := &fakeUpstream{
		upgrader: websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }},
		sessions: map[string]chan *upstreamSession{},
		secrets:  map[string]chan gjson.Result{},
		refusals: map[string]int{},
		dials:    map[string]int{},
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/realtime", f.handleDial)
	mux.HandleFunc("POST /v1/realtime/client_secrets", f.handleClientSecret)
	f.srv = &http.Server{Handler: mux}
	// A connection the gateway kept alive to a previous run's fake would fail its first request.
	f.srv.SetKeepAlivesEnabled(false)
	go func() { _ = f.srv.Serve(listener) }()
	return f, nil
}

func (f *fakeUpstream) stop() { _ = f.srv.Close() }

// session waits for the connection the gateway opens upstream for a model.
func (f *fakeUpstream) session(t *testing.T, model string) *upstreamSession {
	t.Helper()
	select {
	case s := <-f.sessionSlot(model):
		s.t = t
		t.Cleanup(s.discard)
		return s
	case <-time.After(frameTimeout):
		t.Fatalf("the gateway did not open an upstream session for model %s", model)
		return nil
	}
}

// dialed reports whether the gateway ever tried to open an upstream session for a model.
func (f *fakeUpstream) dialed(model string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.dials[model] > 0
}

// refuse makes every dial for a model fail with an HTTP status, as OpenAI does when it is down.
func (f *fakeUpstream) refuse(model string, status int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refusals[model] = status
}

// clientSecretRequest waits for the client_secrets request the gateway forwards for a model.
func (f *fakeUpstream) clientSecretRequest(t *testing.T, model string) gjson.Result {
	t.Helper()
	select {
	case body := <-f.secretSlot(model):
		return body
	case <-time.After(frameTimeout):
		t.Fatalf("the gateway did not forward a client_secrets request for model %s", model)
		return gjson.Result{}
	}
}

func (f *fakeUpstream) sessionSlot(model string) chan *upstreamSession {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sessions[model] == nil {
		f.sessions[model] = make(chan *upstreamSession, 1)
	}
	return f.sessions[model]
}

func (f *fakeUpstream) secretSlot(model string) chan gjson.Result {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.secrets[model] == nil {
		f.secrets[model] = make(chan gjson.Result, 1)
	}
	return f.secrets[model]
}

func (f *fakeUpstream) nextID(prefix string) string {
	return fmt.Sprintf("%s_fake%d", prefix, f.ids.Add(1))
}

// handleDial accepts the gateway's WebSocket. A transcription session (intent=transcription) names
// no model in the URL, so it is registered once its first frame, a session.update, names one.
func (f *fakeUpstream) handleDial(w http.ResponseWriter, r *http.Request) {
	model := r.URL.Query().Get("model")
	f.mu.Lock()
	f.dials[model]++
	refusal := f.refusals[model]
	f.mu.Unlock()
	if refusal != 0 {
		http.Error(w, `{"error":{"type":"server_error","message":"fake upstream refused the dial"}}`, refusal)
		return
	}

	conn, err := f.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	s := &upstreamSession{
		ID:            f.nextID("sess"),
		Model:         model,
		Intent:        r.URL.Query().Get("intent"),
		Authorization: r.Header.Get("Authorization"),
		conn:          conn,
		frames:        make(chan gjson.Result, 256),
	}
	s.write(sessionCreated(s.ID, model))
	go s.read(func() {
		if model == "" {
			f.mu.Lock()
			f.dials[s.Model]++
			f.mu.Unlock()
		}
		f.sessionSlot(s.Model) <- s
	})
}

// handleClientSecret mints an upstream ephemeral token the way OpenAI does.
func (f *fakeUpstream) handleClientSecret(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	body := gjson.ParseBytes(raw)
	model := body.Get("session.model").Str
	if model == "" {
		model = body.Get("session.audio.input.transcription.model").Str
	}
	f.secretSlot(model) <- body

	reply, _ := json.Marshal(map[string]any{
		"value":      f.nextID("ek_upstream"),
		"expires_at": time.Now().Add(10 * time.Minute).Unix(),
		"session":    json.RawMessage(body.Get("session").Raw),
	})
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(reply)
}

// upstreamSession is the provider's end of one realtime session: what the gateway sent it, and
// the frames a test makes it send back.
type upstreamSession struct {
	ID            string // the provider session id, announced in session.created
	Model         string
	Intent        string
	Authorization string // the credential the gateway dialed with

	t       *testing.T
	conn    *websocket.Conn
	writeMu sync.Mutex
	frames  chan gjson.Result // closed when the gateway closes the connection
}

// read queues every frame the gateway sends. register hands the session to its test once its model
// is known: at once, or from the first frame of a transcription session.
func (s *upstreamSession) read(register func()) {
	defer close(s.frames)
	registered := s.Model != ""
	if registered {
		register()
	}
	for {
		_, raw, err := s.conn.ReadMessage()
		if err != nil {
			return
		}
		frame := gjson.ParseBytes(raw)
		if !registered {
			s.Model = frame.Get("session.audio.input.transcription.model").Str
			registered = true
			register()
		}
		s.frames <- frame
	}
}

// send writes provider events to the gateway, in order.
func (s *upstreamSession) send(events ...event) {
	s.t.Helper()
	for _, e := range events {
		require.NoError(s.t, s.write(e), "upstream send %s", e["type"])
	}
}

// sendRaw writes a text frame that need not be valid JSON.
func (s *upstreamSession) sendRaw(text string) {
	s.t.Helper()
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	require.NoError(s.t, s.conn.WriteMessage(websocket.TextMessage, []byte(text)))
}

func (s *upstreamSession) write(e event) error {
	raw, err := json.Marshal(e)
	if err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.conn.WriteMessage(websocket.TextMessage, raw)
}

// expect asserts the next frames the gateway sent upstream have these types, in this order, and
// returns them.
func (s *upstreamSession) expect(types ...string) []gjson.Result {
	s.t.Helper()
	return expectFrames(s.t, "upstream", s.frames, types)
}

// expectClosed asserts the gateway closes the upstream connection without sending anything else.
func (s *upstreamSession) expectClosed() {
	s.t.Helper()
	expectFramesEnd(s.t, "upstream", s.frames)
}

// drop cuts the connection without a close frame, as a network failure or a crashed provider does.
func (s *upstreamSession) drop() { _ = s.conn.NetConn().Close() }

// hangUp ends the connection the orderly way, with a normal close frame.
func (s *upstreamSession) hangUp() {
	s.writeMu.Lock()
	_ = s.conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
	s.writeMu.Unlock()
	_ = s.conn.Close()
}

// discard closes what a finished test left open.
func (s *upstreamSession) discard() { _ = s.conn.Close() }

// expectFrames reads frames off a connection's queue and checks their types in order.
func expectFrames(t *testing.T, side string, frames <-chan gjson.Result, types []string) []gjson.Result {
	t.Helper()
	got := make([]gjson.Result, 0, len(types))
	for _, want := range types {
		select {
		case frame, open := <-frames:
			require.True(t, open, "%s: connection closed while waiting for %s (after %s)", side, want, typesOf(got))
			require.Equal(t, want, frame.Get("type").Str, "%s: unexpected frame after %s: %s", side, typesOf(got), frame.Raw)
			got = append(got, frame)
		case <-time.After(frameTimeout):
			t.Fatalf("%s: no %s within %s (after %s)", side, want, frameTimeout, typesOf(got))
		}
	}
	return got
}

// expectFramesEnd waits for a connection to close, failing on any frame that arrives first.
func expectFramesEnd(t *testing.T, side string, frames <-chan gjson.Result) {
	t.Helper()
	select {
	case frame, open := <-frames:
		require.False(t, open, "%s: expected the connection to close, got %s", side, frame.Raw)
	case <-time.After(frameTimeout):
		t.Fatalf("%s: connection still open after %s", side, frameTimeout)
	}
}

func typesOf(frames []gjson.Result) string {
	types := make([]string, len(frames))
	for i, frame := range frames {
		types[i] = frame.Get("type").Str
	}
	return "[" + strings.Join(types, " ") + "]"
}
