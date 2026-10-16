package realtime

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// dial describes how a client connects to the gateway. The zero value is a server-side app on
// /v1/realtime presenting the suite's virtual key.
type dial struct {
	route        string      // /v1/realtime when empty
	query        string      // as the client writes it; openSession puts its model in for {model}
	key          string      // the bearer credential; the suite's virtual key when empty
	anonymous    bool        // present no credential at all
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
	key := d.key
	if key == "" {
		key = suiteKey.Value
	}
	if !d.anonymous && header.Get("Authorization") == "" {
		header.Set("Authorization", "Bearer "+key)
	}
	return header
}

// client is the app's end of a realtime session, connected to the gateway.
type client struct {
	t           *testing.T
	conn        *websocket.Conn
	subprotocol string            // what the gateway answered the upgrade with
	frames      chan gjson.Result // closed when the gateway closes the connection
	closeCode   atomic.Int64      // the close code the gateway sent, once frames is closed
}

// connect upgrades a client connection through the gateway; the test fails if the gateway refuses
// the upgrade.
func connect(t *testing.T, d dial) *client {
	t.Helper()
	dialer := websocket.Dialer{HandshakeTimeout: frameTimeout, Subprotocols: d.subprotocols}
	conn, resp, err := dialer.Dial(d.url(), d.header())
	require.NoError(t, err, "upgrade %s refused: %s", d.url(), responseBody(resp))
	c := &client{t: t, conn: conn, subprotocol: conn.Subprotocol(), frames: make(chan gjson.Result, 256)}
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
		require.NoError(c.t, c.conn.WriteMessage(websocket.TextMessage, raw), "client send %s", e["type"])
	}
}

// sendRaw writes a text frame that need not be valid JSON.
func (c *client) sendRaw(text string) {
	c.t.Helper()
	require.NoError(c.t, c.conn.WriteMessage(websocket.TextMessage, []byte(text)))
}

// sendBinary writes a binary frame, which the Realtime API does not accept.
func (c *client) sendBinary(data []byte) {
	c.t.Helper()
	require.NoError(c.t, c.conn.WriteMessage(websocket.BinaryMessage, data))
}

// expect asserts the next frames from the gateway have these types, in this order, and returns them.
func (c *client) expect(types ...string) []gjson.Result {
	c.t.Helper()
	return expectFrames(c.t, "client", c.frames, types)
}

// expectRelayed asserts the client receives exactly these provider events, unchanged and in order.
func (c *client) expectRelayed(events ...event) {
	c.t.Helper()
	types := make([]string, len(events))
	for i, e := range events {
		types[i] = fmt.Sprint(e["type"])
	}
	for i, frame := range c.expect(types...) {
		want, err := json.Marshal(events[i])
		require.NoError(c.t, err)
		assert.JSONEq(c.t, string(want), frame.Raw, "event %d (%s) was changed on the way", i, types[i])
	}
}

// anyType makes expectError accept an error of any type.
const anyType = ""

// expectError asserts the next frame is an error event of this type whose message contains a
// fragment, and returns its error object.
func (c *client) expectError(errorType, messageFragment string) gjson.Result {
	c.t.Helper()
	frame := c.expect("error")[0].Get("error")
	if errorType != anyType {
		assert.Equal(c.t, errorType, frame.Get("type").Str, "error type: %s", frame.Raw)
	}
	assert.Contains(c.t, frame.Get("message").Str, messageFragment, "error message: %s", frame.Raw)
	return frame
}

// expectClosed asserts the gateway closes the connection without sending anything else.
func (c *client) expectClosed() {
	c.t.Helper()
	expectFramesEnd(c.t, "client", c.frames)
}

// leave closes the client connection the orderly way, as an app that hangs up does.
func (c *client) leave() {
	_ = c.conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
	_ = c.conn.Close()
}

// session is one realtime session through the gateway, seen from both ends.
type session struct {
	t        *testing.T
	model    string // the bare model, unique to this session; it names the session's log rows
	client   *client
	upstream *upstreamSession
}

// openSession connects a client on a model of its own and waits until the gateway has opened the
// upstream session and relayed its session.created. The query defaults to model=openai/{model}.
func openSession(t *testing.T, opts ...dial) *session {
	t.Helper()
	var d dial
	if len(opts) > 0 {
		d = opts[0]
	}
	if d.query == "" {
		d.query = "model=openai/{model}"
	}
	model := newModel(t)
	d.query = strings.ReplaceAll(d.query, "{model}", model)
	c := connect(t, d)
	up := upstream.session(t, model)
	c.expect("session.created")
	return &session{t: t, model: model, client: c, upstream: up}
}

var (
	modelRun = time.Now().Unix() % 100000 // keeps names unique across runs against one gateway
	modelSeq atomic.Int64
)

var nonModelChars = regexp.MustCompile(`[^a-z0-9]+`)

// newModel returns a model name no other test uses, readable in the logs: rt-e2e-<test>-<run>-<n>.
// The gateway's openai key serves every model, and the suite prices rt-e2e-*.
func newModel(t *testing.T) string {
	name := nonModelChars.ReplaceAllString(strings.ToLower(strings.TrimPrefix(t.Name(), "Test")), "-")
	return fmt.Sprintf("rt-e2e-%s-%d-%d", strings.Trim(name, "-"), modelRun, modelSeq.Add(1))
}

func responseBody(resp *http.Response) string {
	if resp == nil || resp.Body == nil {
		return ""
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return string(raw)
}
