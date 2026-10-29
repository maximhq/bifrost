package realtime

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// dial describes how a client connects to the gateway. The zero value is a server-side app on
// /v1/realtime presenting a virtual key minted for the test.
type dial struct {
	route        string      // /v1/realtime when empty
	query        string      // model=openai/<openaiModel> when empty (openSession only)
	vk           virtualKey  // the key the session is billed to; a fresh one when zero
	token        string      // a bearer to present instead of the virtual key's value
	anonymous    bool        // present no Authorization header
	subprotocols []string    // offered Sec-WebSocket-Protocol values, as a browser sends them
	headers      http.Header // anything else to send on the upgrade
}

func (d dial) url() string {
	route := d.route
	if route == "" {
		route = "/v1/realtime"
	}
	return "ws" + strings.TrimPrefix(gatewayURL, "http") + route + "?" + d.query
}

func (d dial) header() http.Header {
	header := http.Header{}
	for name, values := range d.headers {
		header[name] = values
	}
	bearer := d.token
	if bearer == "" {
		bearer = d.vk.Value
	}
	if !d.anonymous && bearer != "" && header.Get("Authorization") == "" {
		header.Set("Authorization", "Bearer "+bearer)
	}
	return header
}

// client is the app's end of a realtime session, connected to the gateway.
type client struct {
	t           *testing.T
	conn        *websocket.Conn
	vk          virtualKey        // the key this client's turns are logged under
	subprotocol string            // what the gateway answered the upgrade with
	frames      chan gjson.Result // closed when the gateway closes the connection
	closeCode   atomic.Int64      // the close code the gateway sent, once frames is closed
	writeMu     sync.Mutex
}

// connect upgrades a client connection through the gateway; the test fails if the gateway refuses
// the upgrade. A dial with no credential at all gets a virtual key minted for the test.
func connect(t *testing.T, d dial) *client {
	t.Helper()
	if !d.anonymous && d.token == "" && d.vk.Value == "" {
		d.vk = createVirtualKey(t, keySpec{})
	}
	dialer := websocket.Dialer{Proxy: http.ProxyFromEnvironment, HandshakeTimeout: frameTimeout, Subprotocols: d.subprotocols}
	conn, resp, err := dialer.Dial(d.url(), d.header())
	require.NoError(t, err, "upgrade %s refused: %s", d.url(), responseBody(resp))
	c := &client{t: t, conn: conn, vk: d.vk, subprotocol: conn.Subprotocol(), frames: make(chan gjson.Result, 1024)}
	go c.read()
	t.Cleanup(func() { _ = conn.Close() })
	return c
}

// refused asserts the gateway refuses the upgrade at the HTTP layer, and returns the status and body.
func refused(t *testing.T, d dial) (int, gjson.Result) {
	t.Helper()
	conn, resp, err := websocket.DefaultDialer.Dial(d.url(), d.header())
	if err == nil {
		_ = conn.Close()
		t.Fatalf("the gateway upgraded %s, want a refusal", d.url())
	}
	require.NotNil(t, resp, "dial %s: %v", d.url(), err)
	return resp.StatusCode, gjson.Parse(responseBody(resp))
}

func (c *client) read() {
	defer close(c.frames)
	for {
		_, raw, err := c.conn.ReadMessage()
		if err != nil {
			code := int64(websocket.CloseAbnormalClosure)
			if closeErr, ok := err.(*websocket.CloseError); ok {
				code = int64(closeErr.Code)
			}
			c.closeCode.Store(code)
			return
		}
		c.frames <- gjson.ParseBytes(raw)
	}
}

// send writes client events to the gateway, in order.
func (c *client) send(events ...event) {
	c.t.Helper()
	for _, e := range events {
		raw, err := json.Marshal(e)
		require.NoError(c.t, err)
		require.NoError(c.t, c.write(websocket.TextMessage, raw), "client send %s", e["type"])
	}
}

// sendRaw writes a text frame that need not be valid JSON.
func (c *client) sendRaw(text string) {
	c.t.Helper()
	require.NoError(c.t, c.write(websocket.TextMessage, []byte(text)))
}

// sendBinary writes a binary frame, which the Realtime API does not accept.
func (c *client) sendBinary(data []byte) {
	c.t.Helper()
	require.NoError(c.t, c.write(websocket.BinaryMessage, data))
}

func (c *client) write(messageType int, data []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.conn.WriteMessage(messageType, data)
}

// expect asserts the next frames from the gateway have these types, in this order, and returns
// them. Use it for frames whose order the gateway controls; provider streams go through until.
func (c *client) expect(types ...string) []gjson.Result {
	c.t.Helper()
	got := make([]gjson.Result, 0, len(types))
	for _, want := range types {
		select {
		case frame, open := <-c.frames:
			require.True(c.t, open, "connection closed while waiting for %s (after %s)", want, typesOf(got))
			require.Equal(c.t, want, frame.Get("type").Str, "unexpected frame after %s: %s", typesOf(got), frame.Raw)
			got = append(got, frame)
		case <-time.After(frameTimeout):
			c.t.Fatalf("no %s within %s (after %s)", want, frameTimeout, typesOf(got))
		}
	}
	return got
}

// until reads frames up to and including the first of this type and returns them all. Any error
// event on the way fails the test.
func (c *client) until(eventType string) []gjson.Result {
	c.t.Helper()
	frames := c.collect(eventType)
	for _, frame := range frames {
		if frame.Get("type").Str == "error" && eventType != "error" {
			c.t.Fatalf("error event while waiting for %s: %s", eventType, frame.Raw)
		}
	}
	return frames
}

// untilAll reads frames until each of these types has arrived, in any order, and returns them all.
// Any error event on the way fails the test.
func (c *client) untilAll(types ...string) []gjson.Result {
	c.t.Helper()
	var got []gjson.Result
	for _, eventType := range types {
		if findType(got, eventType).Exists() {
			continue
		}
		got = append(got, c.until(eventType)...)
	}
	return got
}

// collect is until without the error check, for tests that expect errors on the way.
func (c *client) collect(eventType string) []gjson.Result {
	c.t.Helper()
	var got []gjson.Result
	deadline := time.After(turnTimeout)
	for {
		select {
		case frame, open := <-c.frames:
			require.True(c.t, open, "connection closed while waiting for %s (after %s)", eventType, typesOf(got))
			got = append(got, frame)
			if frame.Get("type").Str == eventType {
				return got
			}
		case <-deadline:
			c.t.Fatalf("no %s within %s (after %s)", eventType, turnTimeout, typesOf(got))
		}
	}
}

// anyType makes expectError accept an error of any type.
const anyType = ""

// expectError asserts the next frame is an error event of this type whose message contains a
// fragment, and returns its error object.
func (c *client) expectError(errorType, messageFragment string) gjson.Result {
	c.t.Helper()
	return assertError(c.t, c.expect("error")[0], errorType, messageFragment)
}

func assertError(t *testing.T, frame gjson.Result, errorType, messageFragment string) gjson.Result {
	t.Helper()
	errObj := frame.Get("error")
	if errorType != anyType {
		assert.Equal(t, errorType, errObj.Get("type").Str, "error type: %s", errObj.Raw)
	}
	assert.Contains(t, errObj.Get("message").Str, messageFragment, "error message: %s", errObj.Raw)
	return errObj
}

// expectClosed asserts the gateway closes the connection without sending anything else.
func (c *client) expectClosed() {
	c.t.Helper()
	select {
	case frame, open := <-c.frames:
		require.False(c.t, open, "expected the connection to close, got %s", frame.Raw)
	case <-time.After(frameTimeout):
		c.t.Fatalf("connection still open after %s", frameTimeout)
	}
}

// leave closes the client connection the orderly way, as an app that hangs up does.
func (c *client) leave() {
	c.writeMu.Lock()
	_ = c.conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
	c.writeMu.Unlock()
	_ = c.conn.Close()
}

// Audio: 24 kHz, 16-bit mono PCM, the default format of both OpenAI and Nova Sonic.
const (
	audioChunk    = 32 * time.Millisecond
	audioChunkLen = 24000 * 2 * 32 / 1000
)

// streamAudio plays a WAV recording into the session at real-time pace, then keeps sending silence
// like an open microphone, until the test ends. Nova Sonic needs audio flowing for the whole
// session, and server VAD needs the silence to notice the user has finished.
func (c *client) streamAudio(wavPath string) {
	c.t.Helper()
	var pcm []byte
	if wavPath != "" {
		wav, err := os.ReadFile(wavPath)
		require.NoError(c.t, err)
		dataAt := strings.Index(string(wav), "data")
		require.Positive(c.t, dataAt, "%s has no data chunk", wavPath)
		pcm = wav[dataAt+8:]
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(audioChunk)
		defer ticker.Stop()
		silence := make([]byte, audioChunkLen)
		for offset := 0; ; offset += audioChunkLen {
			chunk := silence
			if offset < len(pcm) {
				chunk = pcm[offset:min(offset+audioChunkLen, len(pcm))]
			}
			raw, _ := json.Marshal(inputAudioAppend(base64.StdEncoding.EncodeToString(chunk)))
			if c.write(websocket.TextMessage, raw) != nil {
				return
			}
			select {
			case <-stop:
				return
			case <-ticker.C:
			}
		}
	}()
	c.t.Cleanup(func() {
		close(stop)
		<-done
	})
}

// session is one realtime session through the gateway.
type session struct {
	t       *testing.T
	client  *client
	created gjson.Result // the session.created the session opened with
}

// openSession connects a client and waits for the session.created the provider greets it with. The
// query defaults to the suite's OpenAI realtime model.
func openSession(t *testing.T, opts ...dial) *session {
	t.Helper()
	var d dial
	if len(opts) > 0 {
		d = opts[0]
	}
	if d.query == "" {
		d.query = "model=openai/" + openaiModel
	}
	c := connect(t, d)
	created := c.until("session.created")
	return &session{t: t, client: c, created: created[len(created)-1]}
}

// configure sends a session.update and waits for the provider to confirm it.
func (s *session) configure(config map[string]any) gjson.Result {
	s.t.Helper()
	s.client.send(sessionUpdate(config))
	updated := s.client.until("session.updated")
	return updated[len(updated)-1]
}

// ask sends a user message, asks for a response and returns its response.done.
func (s *session) ask(text string) gjson.Result {
	s.t.Helper()
	s.client.send(userMessage(text), responseCreate())
	return s.responseDone()
}

// responseDone waits for the running response to finish and returns its response.done.
func (s *session) responseDone() gjson.Result {
	s.t.Helper()
	frames := s.client.until("response.done")
	return frames[len(frames)-1]
}

func typesOf(frames []gjson.Result) string {
	types := make([]string, len(frames))
	for i, frame := range frames {
		types[i] = frame.Get("type").Str
	}
	return "[" + strings.Join(types, " ") + "]"
}

// findType returns the first frame of one type, or an empty result.
func findType(frames []gjson.Result, eventType string) gjson.Result {
	for _, frame := range frames {
		if frame.Get("type").Str == eventType {
			return frame
		}
	}
	return gjson.Result{}
}

// countType counts the frames of one type.
func countType(frames []gjson.Result, eventType string) int {
	n := 0
	for _, frame := range frames {
		if frame.Get("type").Str == eventType {
			n++
		}
	}
	return n
}

func responseBody(resp *http.Response) string {
	if resp == nil || resp.Body == nil {
		return ""
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return string(raw)
}
