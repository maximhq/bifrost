package live

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// liveClient is what a customer's app is to Bifrost: a WebSocket or WebRTC live session, or a
// sideband on one. It records every server event so a test can wait for the one it needs.
type liveClient struct {
	t    *testing.T
	conn *websocket.Conn     // WebSocket sessions and sidebands
	dc   *webrtc.DataChannel // WebRTC sessions
	pc   *webrtc.PeerConnection

	writeMu sync.Mutex
	mu      sync.Mutex
	frames  []gjson.Result
	cursor  int
	gone    bool

	// ProviderSessionID is the id OpenAI (or the fake) gave the session, from session.started.
	ProviderSessionID string
	onFunctionCall    func(name string, arguments gjson.Result) any
	onClientTask      func(delegationID string)
	stopSilence       chan struct{}
	speech            chan [][]byte // WebRTC: Opus packets to play on the microphone track
	pcmSpeech         chan []byte   // WebSocket: PCM to send on the microphone
}

// clientOptions is what a test sets on a session it opens.
type clientOptions struct {
	path    string            // "/v1/live/sessions" or "/openai/v1/live/sessions"
	headers map[string]string // credentials; nil sends none
	session map[string]any    // the session object of session.start
	// onFunctionCall answers a backend function call: the return value is the result the app
	// sends back, after which the backend is asked to continue.
	onFunctionCall func(name string, arguments gjson.Result) any
	// onClientTask runs the app's backend under client delegation: the voice model hands the
	// app a task id, the app answers with session.commentary.append.
	onClientTask func(delegationID string)
	// microphone keeps 24 kHz PCM flowing on a WebSocket session, silence when nothing is
	// queued; OpenAI stalls a session whose audio stops.
	microphone bool
}

// sessionFor is a session.start session object: the two models, and the test's marker as instructions.
func sessionFor(t *testing.T, voice, backend string, extra map[string]any) map[string]any {
	session := map[string]any{"model": voice, "instructions": marker(t), "audio": map[string]any{"output": map[string]any{"voice": "marin"}}}
	if backend != "" {
		session["delegation"] = map[string]any{"type": "responses", "responses": map[string]any{"model": backend, "tools": []any{map[string]any{"type": "web_search"}}}}
	}
	for k, v := range extra {
		session[k] = v
	}
	return session
}

func vkHeaders(vk virtualKey) map[string]string { return map[string]string{"x-bf-vk": vk.Value} }

func wsURL(path string) string {
	return strings.Replace(strings.Replace(gatewayURL, "https://", "wss://", 1), "http://", "ws://", 1) + path
}

// dialLive opens the WebSocket to the gateway. A refusal before the upgrade comes back as the
// HTTP status; a refusal after it arrives as an error frame on the returned client.
func dialLive(t *testing.T, path string, headers map[string]string) (*liveClient, int, error) {
	t.Helper()
	h := http.Header{}
	for k, v := range headers {
		h.Set(k, v)
	}
	dialer := websocket.Dialer{HandshakeTimeout: frameTimeout}
	conn, resp, err := dialer.Dial(wsURL(path), h)
	status := 0
	if resp != nil {
		status = resp.StatusCode
		_ = resp.Body.Close()
	}
	if err != nil {
		return nil, status, err
	}
	c := &liveClient{t: t, conn: conn}
	go c.readWS()
	t.Cleanup(c.Close)
	return c, status, nil
}

// transport is how a session reaches the gateway.
type transport string

const (
	wsTransport     transport = "websocket"
	webrtcTransport transport = "webrtc"
)

var transports = []transport{wsTransport, webrtcTransport}

// forEachTransport runs a scenario on each transport as a parallel subtest. Admission, billing,
// governance and logging are shared, but each transport has its own relay, pump and close path,
// so a scenario proves itself on both.
func forEachTransport(t *testing.T, run func(t *testing.T, tr transport)) {
	t.Helper()
	for _, tr := range transports {
		t.Run(string(tr), func(t *testing.T) {
			t.Parallel()
			run(t, tr)
		})
	}
}

// openLive starts a session on a transport and waits for session.started.
func openLive(t *testing.T, tr transport, opts clientOptions) *liveClient {
	t.Helper()
	if tr == webrtcTransport {
		return createWebRTCSession(t, opts)
	}
	return startSession(t, opts)
}

// openRefused starts a session the gateway should turn away and returns the refusal: the error
// frame after a WebSocket upgrade, or the HTTP error body of a WebRTC create.
func openRefused(t *testing.T, tr transport, opts clientOptions) gjson.Result {
	t.Helper()
	if tr == webrtcTransport {
		c, status, body := offerWebRTCSession(t, opts)
		if c != nil {
			t.Fatalf("WebRTC create was accepted (HTTP %d), want a refusal", status)
		}
		require.GreaterOrEqual(t, status, http.StatusBadRequest, "a refusal is an HTTP error: %s", body)
		return gjson.ParseBytes(body)
	}
	return openSession(t, opts).WaitFor("error")
}

// startSession opens a WebSocket session and waits for session.started.
func startSession(t *testing.T, opts clientOptions) *liveClient {
	t.Helper()
	c := openSession(t, opts)
	started := c.WaitFor("session.started")
	c.ProviderSessionID = started.Get("session.id").Str
	require.NotEmpty(t, c.ProviderSessionID, "session.started carries the provider's session id: %s", started.Raw)
	return c
}

// openSession opens a WebSocket session and sends session.start, without waiting for the answer.
func openSession(t *testing.T, opts clientOptions) *liveClient {
	t.Helper()
	path := opts.path
	if path == "" {
		path = "/v1/live/sessions"
	}
	c, status, err := dialLive(t, path, opts.headers)
	if err != nil {
		t.Fatalf("dial %s: %v (HTTP %d)", path, err, status)
	}
	c.onFunctionCall = opts.onFunctionCall
	c.onClientTask = opts.onClientTask
	frame, _ := json.Marshal(map[string]any{"type": "session.start", "event_id": "evt_start", "session": opts.session})
	c.Send(string(frame))
	if opts.microphone {
		c.pcmSpeech = make(chan []byte, 4)
		c.stopSilence = make(chan struct{})
		go c.runWSMicrophone()
	}
	return c
}

// runWSMicrophone sends one 20 ms PCM frame per tick: queued speech, else silence.
func (c *liveClient) runWSMicrophone() {
	const frameBytes = 24000 * 2 / 50
	silence := make([]byte, frameBytes)
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	var pending []byte
	for {
		select {
		case <-c.stopSilence:
			return
		case <-ticker.C:
		}
		if len(pending) == 0 {
			select {
			case pcm := <-c.pcmSpeech:
				pending = pcm
			default:
			}
		}
		frame := silence
		if len(pending) > 0 {
			n := min(frameBytes, len(pending))
			frame, pending = pending[:n], pending[n:]
		}
		c.mu.Lock()
		gone := c.gone
		c.mu.Unlock()
		if gone {
			return
		}
		c.Send(`{"type":"session.input_audio.append","audio":"` + base64.StdEncoding.EncodeToString(frame) + `"}`)
	}
}

func (c *liveClient) readWS() {
	for {
		_, frame, err := c.conn.ReadMessage()
		if err != nil {
			c.markGone()
			return
		}
		c.record(frame)
	}
}

func (c *liveClient) record(frame []byte) {
	parsed := gjson.ParseBytes(append([]byte(nil), frame...))
	c.mu.Lock()
	c.frames = append(c.frames, parsed)
	c.mu.Unlock()
	c.maybeAnswerFunctionCall(parsed)
	if c.onClientTask != nil && parsed.Get("type").Str == "session.delegation.created" && parsed.Get("delegation.target").Str == "client" {
		go c.onClientTask(parsed.Get("delegation.id").Str)
	}
}

// Commentary sends the app's result for a client-delegated task; the voice model speaks it.
func (c *liveClient) Commentary(delegationID, content string) {
	quoted, _ := json.Marshal(content)
	c.Send(fmt.Sprintf(`{"type":"session.commentary.append","event_id":"result_%s","delegation_id":%q,"content":%s}`, delegationID, delegationID, quoted))
}

// callResponses is the app's own backend call through the gateway, grouped with the live
// session by its session id, as a customer's app would do under client delegation. It runs on
// the app's goroutine, so it reports failure rather than asserting.
func callResponses(path string, headers map[string]string, providerSessionID, model string, input []map[string]any) (gjson.Result, error) {
	h := map[string]string{"x-bf-session-id": providerSessionID}
	for k, v := range headers {
		h[k] = v
	}
	body := map[string]any{"model": model, "instructions": "Answer the user's latest request in one short sentence.", "input": input, "tools": []any{map[string]any{"type": "web_search"}}}
	status, raw, err := apiCall(http.MethodPost, path, body, h)
	if err != nil {
		return gjson.Result{}, err
	}
	if status != http.StatusOK {
		return gjson.Result{}, fmt.Errorf("POST %s: HTTP %d %s", path, status, raw)
	}
	return gjson.ParseBytes(raw), nil
}

// appCall is what the app's backend call under client delegation came back with.
type appCall struct {
	response gjson.Result
	err      error
}

// await receives from a channel within frameTimeout, failing the test when nothing arrives.
func await[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	return awaitWithin(t, ch, frameTimeout, what)
}

// awaitWithin is await with its own timeout, for the real upstream's pace.
func awaitWithin[T any](t *testing.T, ch <-chan T, timeout time.Duration, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(timeout):
		t.Fatalf("%s did not happen within %s", what, timeout)
		var zero T
		return zero
	}
}

func (c *liveClient) markGone() {
	c.mu.Lock()
	c.gone = true
	c.mu.Unlock()
}

// maybeAnswerFunctionCall plays the app's part of a function call: result back, then continue.
func (c *liveClient) maybeAnswerFunctionCall(frame gjson.Result) {
	if c.onFunctionCall == nil || frame.Get("type").Str != "response.event" || frame.Get("event.type").Str != "response.output_item.done" {
		return
	}
	item := frame.Get("event.item")
	if item.Get("type").Str != "function_call" {
		return
	}
	result := c.onFunctionCall(item.Get("name").Str, gjson.Parse(item.Get("arguments").Str))
	output, _ := json.Marshal(result)
	quoted, _ := json.Marshal(string(output))
	c.Send(fmt.Sprintf(`{"type":"response.item.create","event_id":"tool_result","item":{"type":"function_call_output","call_id":%q,"output":%s}}`, item.Get("call_id").Str, quoted))
	c.Send(`{"type":"response.create","event_id":"continue"}`)
}

// Send writes one client event.
func (c *liveClient) Send(frame string) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.dc != nil {
		_ = c.dc.SendText(frame)
		return
	}
	_ = c.conn.WriteMessage(websocket.TextMessage, []byte(frame))
}

// WaitFor returns the next unconsumed frame of a type, failing the test after the timeout.
func (c *liveClient) WaitFor(typ string) gjson.Result {
	c.t.Helper()
	return c.WaitForAny(typ)
}

// WaitForAny returns the next unconsumed frame whose type is one of types.
func (c *liveClient) WaitForAny(types ...string) gjson.Result {
	c.t.Helper()
	return c.WaitWithin(frameTimeout, types...)
}

// WaitWithin is WaitForAny with its own timeout, for the real upstream's pace.
func (c *liveClient) WaitWithin(timeout time.Duration, types ...string) gjson.Result {
	c.t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		c.mu.Lock()
		for i := c.cursor; i < len(c.frames); i++ {
			for _, typ := range types {
				if c.frames[i].Get("type").Str == typ {
					c.cursor = i + 1
					frame := c.frames[i]
					c.mu.Unlock()
					return frame
				}
			}
		}
		gone := c.gone
		c.mu.Unlock()
		if time.Now().After(deadline) || gone && time.Now().After(deadline.Add(-timeout+time.Second)) {
			c.t.Fatalf("no %v frame arrived (connection gone: %v); frames seen: %s; errors: %s", types, gone, c.Types(), c.errorMessages())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// WaitSeen waits until at least one frame of a type has arrived, wherever it sits in the
// buffer, without moving the cursor.
func (c *liveClient) WaitSeen(timeout time.Duration, typ string) {
	c.t.Helper()
	deadline := time.Now().Add(timeout)
	for len(c.Frames(typ)) == 0 {
		if time.Now().After(deadline) {
			c.t.Fatalf("no %s frame arrived within %s; frames seen: %s; errors: %s", typ, timeout, c.Types(), c.errorMessages())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// WaitQuiet waits until frames of a type have stopped arriving for a quiet period, after at
// least one more than already seen. It is how a test knows the assistant has finished speaking.
func (c *liveClient) WaitQuiet(timeout, quiet time.Duration, typ string) {
	c.t.Helper()
	c.WaitQuietSince(timeout, quiet, typ, len(c.Frames(typ)))
}

// WaitQuietSince is WaitQuiet counting from an earlier frame count, for a turn whose answer may
// have finished before the wait began.
func (c *liveClient) WaitQuietSince(timeout, quiet time.Duration, typ string, seen int) {
	c.t.Helper()
	deadline := time.Now().Add(timeout)
	for len(c.Frames(typ)) == seen {
		if time.Now().After(deadline) {
			c.t.Fatalf("no new %s frame arrived within %s; frames seen: %s; errors: %s", typ, timeout, c.Types(), c.errorMessages())
		}
		time.Sleep(50 * time.Millisecond)
	}
	for {
		count := len(c.Frames(typ))
		time.Sleep(quiet)
		if len(c.Frames(typ)) == count {
			return
		}
		if time.Now().After(deadline) {
			return
		}
	}
}

// Frames is every frame received so far, of one type or all when typ is empty.
func (c *liveClient) Frames(typ string) []gjson.Result {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []gjson.Result
	for _, frame := range c.frames {
		if typ == "" || frame.Get("type").Str == typ {
			out = append(out, frame)
		}
	}
	return out
}

// errorMessages joins the messages of every error frame received, for failure messages.
func (c *liveClient) errorMessages() string {
	var messages []string
	for _, frame := range c.Frames("error") {
		messages = append(messages, errorMessage(frame))
	}
	return strings.Join(messages, " | ")
}

// Types lists the types received, in order, for failure messages.
func (c *liveClient) Types() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var types []string
	for _, frame := range c.frames {
		types = append(types, frame.Get("type").Str)
	}
	return strings.Join(types, ",")
}

// WaitGone waits for the gateway to close the connection.
func (c *liveClient) WaitGone() {
	c.t.Helper()
	deadline := time.Now().Add(liveDrainTimeout + frameTimeout)
	for {
		c.mu.Lock()
		gone := c.gone
		c.mu.Unlock()
		if gone {
			return
		}
		if time.Now().After(deadline) {
			c.t.Fatalf("the gateway never closed the connection; frames seen: %s", c.Types())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// liveDrainTimeout mirrors the gateway's wait for session.closed after session.close.
const liveDrainTimeout = 15 * time.Second

// Close ends the client side: the gateway then closes the session upstream.
func (c *liveClient) Close() {
	if c.stopSilence != nil {
		select {
		case <-c.stopSilence:
		default:
			close(c.stopSilence)
		}
	}
	if c.pc != nil {
		_ = c.pc.Close()
		return
	}
	if c.conn != nil {
		_ = c.conn.Close()
	}
}

// CloseSession asks the session to end and waits for session.closed.
func (c *liveClient) CloseSession() gjson.Result {
	c.t.Helper()
	c.Send(`{"type":"session.close"}`)
	return c.WaitFor("session.closed")
}

// ---- WebRTC ----

// opusSilence is one 20 ms Opus frame of silence: the microphone of an app that says nothing.
var opusSilence = []byte{0xf8, 0xff, 0xfe}

// createWebRTCSession does what a browser does: POST the session with an SDP offer, then talk
// on the oai-events data channel. It waits for session.started.
func createWebRTCSession(t *testing.T, opts clientOptions) *liveClient {
	t.Helper()
	c, status, body := offerWebRTCSession(t, opts)
	if c == nil {
		t.Fatalf("WebRTC create refused: HTTP %d %s", status, body)
	}
	started := c.WaitFor("session.started")
	c.ProviderSessionID = started.Get("session.id").Str
	require.NotEmpty(t, c.ProviderSessionID, "session.started carries the provider's session id: %s", started.Raw)
	return c
}

// offerWebRTCSession posts the offer and returns the client with the create status and body; a
// refusal comes back with a nil client.
func offerWebRTCSession(t *testing.T, opts clientOptions) (*liveClient, int, []byte) {
	t.Helper()
	path := opts.path
	if path == "" {
		path = "/v1/live/sessions"
	}
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	require.NoError(t, err)
	microphone, err := webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2}, "audio", "live-e2e")
	require.NoError(t, err)
	_, err = pc.AddTrack(microphone)
	require.NoError(t, err)
	pc.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		for {
			if _, _, err := track.ReadRTP(); err != nil {
				return
			}
		}
	})
	c := &liveClient{t: t, pc: pc, onFunctionCall: opts.onFunctionCall, onClientTask: opts.onClientTask, stopSilence: make(chan struct{}), speech: make(chan [][]byte, 4)}
	dc, err := pc.CreateDataChannel("oai-events", nil)
	require.NoError(t, err)
	c.dc = dc
	opened := make(chan struct{})
	dc.OnOpen(func() { close(opened) })
	dc.OnMessage(func(msg webrtc.DataChannelMessage) { c.record(msg.Data) })
	dc.OnClose(func() { c.markGone() })
	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		if state == webrtc.PeerConnectionStateClosed || state == webrtc.PeerConnectionStateFailed || state == webrtc.PeerConnectionStateDisconnected {
			c.markGone()
		}
	})
	t.Cleanup(c.Close)

	offer, err := pc.CreateOffer(nil)
	require.NoError(t, err)
	gathered := webrtc.GatheringCompletePromise(pc)
	require.NoError(t, pc.SetLocalDescription(offer))
	<-gathered

	body, _ := json.Marshal(map[string]any{"session": opts.session, "transport": map[string]any{"type": "webrtc", "sdp": pc.LocalDescription().SDP}})
	status, raw, err := apiCall(http.MethodPost, path, json.RawMessage(body), opts.headers)
	require.NoError(t, err)
	if status != http.StatusCreated {
		return nil, status, raw
	}
	answer := gjson.GetBytes(raw, "transport.sdp").Str
	require.NotEmpty(t, answer, "create response carries the SDP answer: %s", raw)
	require.NoError(t, pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answer}))
	select {
	case <-opened:
	case <-time.After(frameTimeout):
		t.Fatalf("the oai-events data channel did not open")
	}
	go c.sendSilence(microphone)
	return c, status, raw
}

// sendSilence keeps the microphone track flowing: queued speech when there is some, silence otherwise.
func (c *liveClient) sendSilence(track *webrtc.TrackLocalStaticSample) {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	var pending [][]byte
	for {
		select {
		case <-c.stopSilence:
			return
		case <-ticker.C:
		}
		select {
		case packets := <-c.speech:
			pending = append(pending, packets...)
		default:
		}
		frame := opusSilence
		if len(pending) > 0 {
			frame, pending = pending[0], pending[1:]
		}
		if err := track.WriteSample(media.Sample{Data: frame, Duration: 20 * time.Millisecond}); err != nil {
			return
		}
	}
}

// SpeakOpus plays 20 ms Opus packets on the WebRTC microphone and returns once they are sent.
func (c *liveClient) SpeakOpus(packets [][]byte) {
	c.speech <- packets
	time.Sleep(time.Duration(len(packets)) * 20 * time.Millisecond)
}

// SpeakPCM queues 24 kHz PCM16 on the WebSocket microphone and returns once it has played.
func (c *liveClient) SpeakPCM(pcm []byte) {
	if c.pcmSpeech == nil {
		c.t.Fatalf("SpeakPCM needs a session opened with microphone: true")
	}
	c.pcmSpeech <- pcm
	time.Sleep(time.Duration(len(pcm)/(24000*2)) * time.Second)
}

// ---- sidebands and recordings ----

// attachSideband opens a sideband on an existing session. A refusal comes back as the HTTP status.
func attachSideband(t *testing.T, providerSessionID string, headers map[string]string) (*liveClient, int, error) {
	t.Helper()
	return dialLive(t, "/v1/live/sessions/"+providerSessionID+"/attach", headers)
}

// downloadContent fetches a session's recording through the gateway.
func downloadContent(t *testing.T, providerSessionID string, headers map[string]string) (int, []byte, string) {
	t.Helper()
	return downloadContentWithin(t, providerSessionID, headers, 30*time.Second)
}

// downloadContentWithin is downloadContent with its own timeout: the gateway buffers the whole
// recording before answering, and a long call's recording takes a while to arrive.
func downloadContentWithin(t *testing.T, providerSessionID string, headers map[string]string, timeout time.Duration) (int, []byte, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, gatewayURL+"/v1/live/sessions/"+providerSessionID+"/content", nil)
	require.NoError(t, err)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := (&http.Client{Timeout: timeout}).Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, raw, resp.Header.Get("Content-Type")
}

// errorMessage reads the message of an error frame or an HTTP error body.
func errorMessage(frame gjson.Result) string {
	if m := frame.Get("error.message").Str; m != "" {
		return m
	}
	return frame.Get("message").Str
}
