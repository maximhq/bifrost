package live

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pion/webrtc/v4"
	"github.com/tidwall/gjson"
)

// fakeOpenAI speaks the GPT Live protocol the way api.openai.com does, on the routes Bifrost
// dials: the primary WebSocket, the WebRTC create, attach, and the recording download. Tests
// drive what it emits; it only answers the client events that need an answer.
type fakeOpenAI struct {
	srv      *http.Server
	upgrader websocket.Upgrader

	refuseStatus atomic.Int32 // when set, every primary dial and WebRTC create is refused with it

	mu             sync.Mutex
	run            string // per-process nonce: ids stay unique across runs against the same gateway
	nextID         int
	responsesCalls int
	sessions       map[string]*fakeSession
	waiters        map[string]chan *fakeSession // marker → the session that carries it
}

// fakeSession is one live session on the fake: its connections (primary and sidebands) and
// every frame Bifrost sent it.
type fakeSession struct {
	fake      *fakeOpenAI
	id        string
	transport string
	marker    string
	model     string
	backend   string
	client    bool // client delegation: the app runs the backend itself
	store     bool
	auth      string
	createdAt time.Time

	mu      sync.Mutex
	conns   []fakeConn
	frames  []gjson.Result
	seconds float64

	recordingBytes int           // a recording of this size replaces the one-second default
	recordingPace  time.Duration // pause between 1 MiB chunks of a large recording
	recordingSent  int           // bytes of a large recording written so far
	closed         bool
}

// fakeConn is one connection of a session: a WebSocket, or a WebRTC data channel.
type fakeConn interface {
	send([]byte) error
	close()
}

type wsFakeConn struct {
	mu   sync.Mutex
	conn *websocket.Conn
}

func (c *wsFakeConn) send(frame []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn.WriteMessage(websocket.TextMessage, frame)
}

func (c *wsFakeConn) close() { _ = c.conn.Close() }

type dcFakeConn struct {
	dc *webrtc.DataChannel
	pc *webrtc.PeerConnection
}

func (c *dcFakeConn) send(frame []byte) error { return c.dc.SendText(string(frame)) }
func (c *dcFakeConn) close()                  { _ = c.pc.Close() }

func startFakeOpenAI(addr string) (*fakeOpenAI, error) {
	f := &fakeOpenAI{
		run:      fmt.Sprintf("%x", time.Now().UnixNano()%0xffffff),
		upgrader: websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }},
		sessions: map[string]*fakeSession{},
		waiters:  map[string]chan *fakeSession{},
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	f.srv = &http.Server{Handler: http.HandlerFunc(f.route)}
	// Each test process starts its own fake on the same port; a connection the gateway kept
	// alive to the previous one would fail its first request with 502, so none are kept.
	f.srv.SetKeepAlivesEnabled(false)
	go func() { _ = f.srv.Serve(listener) }()
	return f, nil
}

func (f *fakeOpenAI) Stop() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = f.srv.Shutdown(ctx)
}

// route dispatches the four live paths.
func (f *fakeOpenAI) route(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/v1/live/sessions")
	switch {
	case path == "" && r.Method == http.MethodGet:
		f.servePrimary(w, r)
	case path == "" && r.Method == http.MethodPost:
		f.serveWebRTCCreate(w, r)
	case strings.HasSuffix(path, "/attach") && r.Method == http.MethodGet:
		f.serveAttach(w, r, strings.TrimSuffix(strings.TrimPrefix(path, "/"), "/attach"))
	case strings.HasSuffix(path, "/content") && r.Method == http.MethodGet:
		f.serveContent(w, r, strings.TrimSuffix(strings.TrimPrefix(path, "/"), "/content"))
	case r.URL.Path == "/v1/responses" && r.Method == http.MethodPost:
		f.serveResponses(w, r)
	default:
		writeOpenAIError(w, http.StatusNotFound, "not_found", "unknown live route "+r.URL.Path)
	}
}

func writeOpenAIError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"type": "invalid_request_error", "code": code, "message": message}})
}

// newSession registers a session from a session object and hands it to the test waiting on its marker.
func (f *fakeOpenAI) newSession(transport, auth string, session gjson.Result) *fakeSession {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	s := &fakeSession{
		fake:      f,
		id:        fmt.Sprintf("live_fake_%s_%d", f.run, f.nextID),
		transport: transport,
		marker:    session.Get("instructions").Str,
		model:     session.Get("model").Str,
		backend:   session.Get("delegation.responses.model").Str,
		client:    session.Get("delegation.type").Str == "client",
		store:     session.Get("store").Bool(),
		auth:      auth,
		createdAt: time.Now(),
	}
	f.sessions[s.id] = s
	if ch, ok := f.waiters[s.marker]; ok {
		ch <- s
		delete(f.waiters, s.marker)
	}
	return s
}

// WaitSession returns the session whose session.start carried marker.
func (f *fakeOpenAI) WaitSession(t *testing.T, marker string) *fakeSession {
	t.Helper()
	f.mu.Lock()
	for _, s := range f.sessions {
		if s.marker == marker {
			f.mu.Unlock()
			return s
		}
	}
	ch := make(chan *fakeSession, 1)
	f.waiters[marker] = ch
	f.mu.Unlock()
	select {
	case s := <-ch:
		return s
	case <-time.After(frameTimeout):
		t.Fatalf("no session reached the fake upstream for %q", marker)
		return nil
	}
}

// SessionCount is how many sessions the fake has seen carrying marker.
func (f *fakeOpenAI) SessionCount(marker string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, s := range f.sessions {
		if s.marker == marker {
			n++
		}
	}
	return n
}

func (f *fakeOpenAI) lookup(id string) *fakeSession {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sessions[id]
}

// servePrimary is the WebSocket a session lives on: session.start first, then the conversation.
func (f *fakeOpenAI) servePrimary(w http.ResponseWriter, r *http.Request) {
	if status := int(f.refuseStatus.Load()); status != 0 {
		writeOpenAIError(w, status, "server_error", "the provider refused the session")
		return
	}
	auth := r.Header.Get("Authorization")
	conn, err := f.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	_ = conn.SetReadDeadline(time.Now().Add(frameTimeout))
	_, first, err := conn.ReadMessage()
	if err != nil || gjson.GetBytes(first, "type").Str != "session.start" {
		_ = conn.Close()
		return
	}
	_ = conn.SetReadDeadline(time.Time{})
	s := f.newSession("websocket", auth, gjson.GetBytes(first, "session"))
	s.recordFrame(first)
	c := &wsFakeConn{conn: conn}
	s.addConn(c)
	_ = c.send(s.startedFrame())
	s.readLoop(conn, c)
}

// SetRefuse makes the fake refuse every primary dial and WebRTC create with status until it is
// called with 0. It is process-wide, so only a test that does not run in parallel may use it.
func (f *fakeOpenAI) SetRefuse(status int) { f.refuseStatus.Store(int32(status)) }

// serveAttach is a sideband: only a WebRTC (or SIP) primary accepts one, as OpenAI does.
func (f *fakeOpenAI) serveAttach(w http.ResponseWriter, r *http.Request, id string) {
	s := f.lookup(id)
	if s == nil || s.transport == "websocket" {
		writeOpenAIError(w, http.StatusNotFound, "session_not_found", "no attachable session "+id)
		return
	}
	conn, err := f.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	c := &wsFakeConn{conn: conn}
	s.addConn(c)
	_ = c.send(s.startedFrame())
	s.readLoop(conn, c)
}

// serveWebRTCCreate answers Bifrost's SDP offer with a pion peer and waits for its data channel.
func (f *fakeOpenAI) serveWebRTCCreate(w http.ResponseWriter, r *http.Request) {
	if status := int(f.refuseStatus.Load()); status != 0 {
		writeOpenAIError(w, status, "server_error", "the provider refused the session")
		return
	}
	var body struct {
		Session   json.RawMessage `json:"session"`
		Transport struct {
			Type string `json:"type"`
			SDP  string `json:"sdp"`
		} `json:"transport"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Transport.Type != "webrtc" || body.Transport.SDP == "" {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request", "transport.sdp is required")
		return
	}
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "server_error", err.Error())
		return
	}
	s := f.newSession("webrtc", r.Header.Get("Authorization"), gjson.ParseBytes(body.Session))
	s.recordFrame([]byte(`{"type":"session.start","session":` + string(body.Session) + `}`))
	pc.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		for {
			if _, _, err := track.ReadRTP(); err != nil {
				return
			}
		}
	})
	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		c := &dcFakeConn{dc: dc, pc: pc}
		dc.OnOpen(func() {
			s.addConn(c)
			_ = c.send(s.startedFrame())
		})
		dc.OnMessage(func(msg webrtc.DataChannelMessage) { s.handleInbound(msg.Data) })
		dc.OnClose(func() { s.removeConn(c) })
	})
	if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: body.Transport.SDP}); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_sdp", err.Error())
		return
	}
	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "server_error", err.Error())
		return
	}
	gathered := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(answer); err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "server_error", err.Error())
		return
	}
	<-gathered
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"session":   map[string]any{"id": s.id, "model": s.model},
		"transport": map[string]any{"type": "webrtc", "sdp": pc.LocalDescription().SDP},
	})
}

// serveResponses is the ordinary Responses API, for an app that runs the backend itself under
// client delegation: it answers any request with one message and fixed usage.
func (f *fakeOpenAI) serveResponses(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Model string `json:"model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Model == "" {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request", "model is required")
		return
	}
	f.mu.Lock()
	f.responsesCalls++
	n := f.responsesCalls
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id": fmt.Sprintf("resp_client_%d", n), "object": "response", "model": body.Model, "status": "completed",
		"output": []any{map[string]any{"id": "msg_client", "type": "message", "role": "assistant", "status": "completed",
			"content": []any{map[string]any{"type": "output_text", "text": clientBackendAnswer, "annotations": []any{}}}}},
		"usage": map[string]any{"input_tokens": clientBackendInput, "output_tokens": clientBackendOutput, "total_tokens": clientBackendInput + clientBackendOutput},
	})
}

// What the fake backend says and bills when the app calls it.
const (
	clientBackendAnswer = "It is sunny in Paris."
	clientBackendInput  = 200
	clientBackendOutput = 20
)

// serveContent serves a stored session's recording: a one-second silent WAV.
func (f *fakeOpenAI) serveContent(w http.ResponseWriter, r *http.Request, id string) {
	s := f.lookup(id)
	if s == nil || !s.store {
		writeOpenAIError(w, http.StatusNotFound, "session_not_found", "no recording for session "+id)
		return
	}
	w.Header().Set("Content-Type", "audio/wav")
	s.mu.Lock()
	size, pace := s.recordingBytes, s.recordingPace
	s.mu.Unlock()
	if size == 0 {
		_, _ = w.Write(fakeRecording())
		return
	}
	// A long call's recording: the header, then silence in paced chunks so a gateway that waits
	// for the whole body is told apart from one that relays it as it arrives.
	w.Header().Set("Content-Length", strconv.Itoa(size))
	flusher, _ := w.(http.Flusher)
	header := fakeRecording()[:44]
	binary.LittleEndian.PutUint32(header[4:], uint32(size-8))
	binary.LittleEndian.PutUint32(header[40:], uint32(size-44))
	if _, err := w.Write(header); err != nil {
		return
	}
	chunk := make([]byte, 1<<20)
	for written := len(header); written < size; {
		n := min(len(chunk), size-written)
		if _, err := w.Write(chunk[:n]); err != nil {
			return
		}
		written += n
		if flusher != nil {
			flusher.Flush()
		}
		s.mu.Lock()
		s.recordingSent = written
		s.mu.Unlock()
		time.Sleep(pace)
	}
}

// RecordingSent is how much of a large recording the provider has written so far.
func (s *fakeSession) RecordingSent() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.recordingSent
}

// SetRecording makes the session's recording size bytes long, written in paced 1 MiB chunks.
func (s *fakeSession) SetRecording(size int, pace time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recordingBytes, s.recordingPace = size, pace
}

// fakeRecording is one second of 24 kHz mono silence as a WAV file.
func fakeRecording() []byte {
	const samples = 24000
	buf := make([]byte, 44+samples*2)
	copy(buf[0:], "RIFF")
	binary.LittleEndian.PutUint32(buf[4:], uint32(36+samples*2))
	copy(buf[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(buf[16:], 16)
	binary.LittleEndian.PutUint16(buf[20:], 1)
	binary.LittleEndian.PutUint16(buf[22:], 1)
	binary.LittleEndian.PutUint32(buf[24:], 24000)
	binary.LittleEndian.PutUint32(buf[28:], 24000*2)
	binary.LittleEndian.PutUint16(buf[32:], 2)
	binary.LittleEndian.PutUint16(buf[34:], 16)
	copy(buf[36:], "data")
	binary.LittleEndian.PutUint32(buf[40:], uint32(samples*2))
	return buf
}

// ---- session behaviour ----

func (s *fakeSession) ID() string         { return s.id }
func (s *fakeSession) Auth() string       { return s.auth }
func (s *fakeSession) Transport() string  { return s.transport }
func (s *fakeSession) StartModel() string { return s.model }
func (s *fakeSession) StartBackend() string {
	return s.backend
}

func (s *fakeSession) addConn(c fakeConn) {
	s.mu.Lock()
	s.conns = append(s.conns, c)
	s.mu.Unlock()
}

func (s *fakeSession) removeConn(c fakeConn) {
	s.mu.Lock()
	for i, existing := range s.conns {
		if existing == c {
			s.conns = append(s.conns[:i], s.conns[i+1:]...)
			break
		}
	}
	s.mu.Unlock()
}

func (s *fakeSession) startedFrame() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessionFrameLocked("session.started")
}

func (s *fakeSession) sessionFrameLocked(typ string) []byte {
	frame, _ := json.Marshal(map[string]any{"type": typ, "event_id": fmt.Sprintf("evt_%d", time.Now().UnixNano()), "session": s.sessionObjectLocked()})
	return frame
}

// sessionObjectLocked is the resolved session as OpenAI echoes it on started, updated and closed.
func (s *fakeSession) sessionObjectLocked() map[string]any {
	session := map[string]any{"id": s.id, "model": s.model, "expires_at": s.createdAt.Add(time.Hour).Unix(), "instructions": s.marker, "store": s.store}
	if s.client {
		session["delegation"] = map[string]any{"type": "client"}
	} else if s.backend != "" {
		session["delegation"] = map[string]any{"type": "responses", "responses": map[string]any{"model": s.backend}}
	}
	return session
}

func (s *fakeSession) readLoop(conn *websocket.Conn, c fakeConn) {
	defer s.removeConn(c)
	for {
		_, frame, err := conn.ReadMessage()
		if err != nil {
			return
		}
		s.handleInbound(frame)
	}
}

func (s *fakeSession) recordFrame(frame []byte) {
	s.mu.Lock()
	s.frames = append(s.frames, gjson.ParseBytes(append([]byte(nil), frame...)))
	s.mu.Unlock()
}

// handleInbound answers the client events that need an answer and records every one.
func (s *fakeSession) handleInbound(frame []byte) {
	s.recordFrame(frame)
	switch gjson.GetBytes(frame, "type").Str {
	case "session.update":
		s.mu.Lock()
		if model := gjson.GetBytes(frame, "session.delegation.responses.model").Str; model != "" {
			s.backend = model
		}
		if instructions := gjson.GetBytes(frame, "session.instructions").Str; instructions != "" {
			s.marker = instructions
		}
		updated := s.sessionFrameLocked("session.updated")
		s.mu.Unlock()
		s.broadcast(updated)
	case "session.commentary.append":
		s.broadcast([]byte(`{"type":"session.commentary.appended","event_id":"evt_commentary","client_event_id":"` + gjson.GetBytes(frame, "event_id").Str + `"}`))
	case "session.instructions.append":
		s.broadcast([]byte(`{"type":"session.instructions.appended","event_id":"evt_appended","client_event_id":"` + gjson.GetBytes(frame, "event_id").Str + `"}`))
	case "session.thinking.append":
		s.broadcast([]byte(`{"type":"session.thinking.appended","event_id":"evt_thinking","client_event_id":"` + gjson.GetBytes(frame, "event_id").Str + `"}`))
	case "session.close":
		s.mu.Lock()
		seconds := s.seconds
		s.mu.Unlock()
		s.Close("close_requested", seconds)
	}
}

func (s *fakeSession) broadcast(frame []byte) {
	s.mu.Lock()
	conns := append([]fakeConn(nil), s.conns...)
	s.mu.Unlock()
	for _, c := range conns {
		_ = c.send(frame)
	}
}

// Emit sends one server event on every connection of the session.
func (s *fakeSession) Emit(frame string) { s.broadcast([]byte(frame)) }

// EmitUsage reports the session's cumulative voice seconds, as session.usage.updated does.
func (s *fakeSession) EmitUsage(seconds float64) {
	s.mu.Lock()
	s.seconds = seconds
	s.mu.Unlock()
	s.Emit(fmt.Sprintf(`{"type":"session.usage.updated","usage":{"seconds":%g,"context_window":0.01}}`, seconds))
}

// Close ends the session the way OpenAI does: session.closed with the final usage, then the
// connections close.
func (s *fakeSession) Close(reason string, seconds float64) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.seconds = seconds
	conns := append([]fakeConn(nil), s.conns...)
	session := s.sessionObjectLocked()
	s.mu.Unlock()
	// session.closed carries the final session snapshot with the usage, as OpenAI's does.
	frame, _ := json.Marshal(map[string]any{"type": "session.closed", "event_id": fmt.Sprintf("evt_%d", time.Now().UnixNano()), "reason": reason, "session": session, "usage": map[string]any{"seconds": seconds}})
	for _, c := range conns {
		_ = c.send(frame)
	}
	time.Sleep(50 * time.Millisecond)
	for _, c := range conns {
		c.close()
	}
}

// Drop cuts every connection without session.closed, as an upstream outage would.
func (s *fakeSession) Drop() {
	s.mu.Lock()
	s.closed = true
	conns := append([]fakeConn(nil), s.conns...)
	s.mu.Unlock()
	for _, c := range conns {
		c.close()
	}
}

// Inbound returns every frame of a type Bifrost sent the session so far.
func (s *fakeSession) Inbound(typ string) []gjson.Result {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []gjson.Result
	for _, frame := range s.frames {
		if frame.Get("type").Str == typ {
			out = append(out, frame)
		}
	}
	return out
}

// WaitInbound waits until Bifrost has sent at least n frames of a type and returns the n-th.
func (s *fakeSession) WaitInbound(t *testing.T, typ string, n int) gjson.Result {
	t.Helper()
	deadline := time.Now().Add(frameTimeout)
	for {
		frames := s.Inbound(typ)
		if len(frames) >= n {
			return frames[n-1]
		}
		if time.Now().After(deadline) {
			t.Fatalf("fake upstream: frame %d of type %s never arrived (saw %d)", n, typ, len(frames))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Connections is how many connections the session has right now.
func (s *fakeSession) Connections() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.conns)
}

// WaitConnections waits until the session has n connections.
func (s *fakeSession) WaitConnections(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(frameTimeout)
	for s.Connections() != n {
		if time.Now().After(deadline) {
			t.Fatalf("fake upstream: session %s has %d connections, want %d", s.id, s.Connections(), n)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// ---- delegation scripts ----

// delegation is one backend Responses call the fake plays for the session.
type delegation struct {
	ID         string   // delegation id; reuse it for a function call's continuation
	ResponseID string   // response id; each response of a delegation has its own
	Model      string   // backend model the response names
	Items      []string // raw output items, in order (JSON objects)
	Input      int
	Output     int
	Reasoning  int
	Cached     int
	Created    bool // send session.delegation.created first
}

// item builders, in OpenAI's wire shapes.
func reasoningItem(id string) string {
	return fmt.Sprintf(`{"id":%q,"type":"reasoning","summary":[],"encrypted_content":"opaque"}`, id)
}

func webSearchItem(id, query string) string {
	return fmt.Sprintf(`{"id":%q,"type":"web_search_call","status":"completed","action":{"type":"search","query":%q}}`, id, query)
}

func functionCallItem(id, callID, name, arguments string) string {
	args, _ := json.Marshal(arguments)
	return fmt.Sprintf(`{"id":%q,"type":"function_call","status":"completed","call_id":%q,"name":%q,"arguments":%s}`, id, callID, name, args)
}

func messageItem(id, text string) string {
	quoted, _ := json.Marshal(text)
	return fmt.Sprintf(`{"id":%q,"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":%s,"annotations":[]}]}`, id, quoted)
}

// Play emits a delegation: created, each finished item, then the terminal event with usage and
// an empty output, exactly as the live socket delivers them.
func (s *fakeSession) Play(d delegation) {
	if d.Created {
		s.Emit(fmt.Sprintf(`{"type":"session.delegation.created","delegation":{"id":%q,"target":"responses","response_id":%q}}`, d.ID, d.ResponseID))
	}
	for i, item := range d.Items {
		s.Emit(fmt.Sprintf(`{"type":"response.event","delegation_id":%q,"event":{"type":"response.output_item.done","output_index":%d,"item":%s}}`, d.ID, i, item))
	}
	s.Emit(fmt.Sprintf(`{"type":"response.event","delegation_id":%q,"event":{"type":"response.completed","response":{"id":%q,"object":"response","model":%q,"status":"completed","output":[],"usage":{"input_tokens":%d,"input_tokens_details":{"cached_tokens":%d},"output_tokens":%d,"output_tokens_details":{"reasoning_tokens":%d},"total_tokens":%d}}}}`,
		d.ID, d.ResponseID, d.Model, d.Input, d.Cached, d.Output, d.Reasoning, d.Input+d.Output))
}

// DelegateToClient hands the app a task, as OpenAI does under client delegation: metadata only.
func (s *fakeSession) DelegateToClient(id string) {
	s.Emit(fmt.Sprintf(`{"type":"session.delegation.created","delegation":{"id":%q,"target":"client"}}`, id))
}

// IsClientDelegation reports whether the session started under client delegation.
func (s *fakeSession) IsClientDelegation() bool { return s.client }

// Transcript emits one transcript fragment on the session timeline.
func (s *fakeSession) Transcript(role, delta string, startMs, endMs int) {
	typ := "session.input_transcript.delta"
	if role == "assistant" {
		typ = "session.output_transcript.delta"
	}
	quoted, _ := json.Marshal(delta)
	s.Emit(fmt.Sprintf(`{"type":%q,"delta":%s,"start_ms":%d,"end_ms":%d}`, typ, quoted, startMs, endMs))
}
