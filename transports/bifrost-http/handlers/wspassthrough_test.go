package handlers

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fasthttp/websocket"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/integrations"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// wsOneKeyAccount exposes OpenAI with one stored key served from baseURL.
type wsOneKeyAccount struct{ baseURL string }

func (wsOneKeyAccount) GetConfiguredProviders() ([]schemas.ModelProvider, error) {
	return []schemas.ModelProvider{schemas.OpenAI}, nil
}

func (wsOneKeyAccount) GetKeysForProvider(_ context.Context, p schemas.ModelProvider) ([]schemas.Key, error) {
	if p != schemas.OpenAI {
		return nil, nil
	}
	return []schemas.Key{{ID: "key-a", Name: "key-a", Value: *schemas.NewSecretVar("sk-stored"), Models: schemas.WhiteList{"*"}, Weight: 1.0}}, nil
}

func (a wsOneKeyAccount) GetConfigForProvider(p schemas.ModelProvider) (*schemas.ProviderConfig, error) {
	if p != schemas.OpenAI {
		return nil, fmt.Errorf("unsupported provider %s", p)
	}
	networkConfig := schemas.DefaultNetworkConfig
	networkConfig.BaseURL = a.baseURL
	return &schemas.ProviderConfig{NetworkConfig: networkConfig, ConcurrencyAndBufferSize: schemas.DefaultConcurrencyAndBufferSize}, nil
}

// wsPassthroughRecorder is a plugin that records what the relay hands the plugin pipeline and
// refuses any turn whose body contains "refuse-me".
type wsPassthroughRecorder struct {
	mu      sync.Mutex
	pre     []*schemas.BifrostPassthroughRequest
	preKeys []string
	finals  []*schemas.BifrostPassthroughResponse
	bodies  []string
	errs    []*schemas.BifrostError
	done    chan struct{}
}

func newWSPassthroughRecorder() *wsPassthroughRecorder {
	return &wsPassthroughRecorder{done: make(chan struct{}, 16)}
}

func (*wsPassthroughRecorder) GetName() string { return "ws-passthrough-recorder" }
func (*wsPassthroughRecorder) Cleanup() error  { return nil }
func (*wsPassthroughRecorder) PreRequestHook(*schemas.BifrostContext, *schemas.BifrostRequest) error {
	return nil
}

func (r *wsPassthroughRecorder) PreLLMHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pre = append(r.pre, req.PassthroughRequest)
	keyID, _ := ctx.Value(schemas.BifrostContextKeySelectedKeyID).(string)
	r.preKeys = append(r.preKeys, keyID)
	if req.PassthroughRequest != nil && strings.Contains(string(req.PassthroughRequest.Body), "refuse-me") {
		return req, &schemas.LLMPluginShortCircuit{Error: newBifrostError(403, "model_not_allowed", "model is not allowed for this virtual key")}, nil
	}
	return req, nil, nil
}

func (r *wsPassthroughRecorder) PostLLMHook(ctx *schemas.BifrostContext, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if bifrostErr != nil {
		r.errs = append(r.errs, bifrostErr)
		r.signal()
		return resp, bifrostErr, nil
	}
	if resp == nil || resp.PassthroughResponse == nil {
		return resp, bifrostErr, nil
	}
	if final, _ := ctx.Value(schemas.BifrostContextKeyStreamEndIndicator).(bool); final {
		r.finals = append(r.finals, resp.PassthroughResponse)
		r.signal()
	} else {
		r.bodies = append(r.bodies, string(resp.PassthroughResponse.Body))
	}
	return resp, bifrostErr, nil
}

func (r *wsPassthroughRecorder) signal() {
	select {
	case r.done <- struct{}{}:
	default:
	}
}

// awaitTurn waits until a turn has ended in the plugin pipeline, served or failed.
func (r *wsPassthroughRecorder) awaitTurn(t *testing.T) {
	t.Helper()
	select {
	case <-r.done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for a turn to reach the post-hooks")
	}
}

// fakeCodexUpstream is a Responses WebSocket backend. It answers each response.create with a
// created and a completed event, except a message containing "hang", which it leaves unanswered
// after the created event, and "fail", which it answers with an error event.
type fakeCodexUpstream struct {
	refuseStatus int // non-zero refuses the handshake with this status

	mu          sync.Mutex
	connections int
	handshakes  []http.Header
	paths       []string
	received    []string
}

const (
	fakeCodexCreated   = `{"type":"response.created","sequence_number":0,"response":{"id":"resp_1","status":"in_progress"}}`
	fakeCodexCompleted = `{"type":"response.completed","sequence_number":1,"response":{"id":"resp_1","object":"response","model":"gpt-5.5","status":"completed","output":[],"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}}`
	fakeCodexError     = `{"type":"error","status":429,"error":{"type":"usage_limit_reached","code":"usage_limit_reached","message":"The usage limit has been reached"}}`
)

func (f *fakeCodexUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if f.refuseStatus != 0 {
		w.WriteHeader(f.refuseStatus)
		_, _ = w.Write([]byte(`{"error":{"message":"token expired"}}`))
		return
	}
	f.mu.Lock()
	f.connections++
	f.handshakes = append(f.handshakes, r.Header.Clone())
	f.paths = append(f.paths, r.URL.RequestURI())
	f.mu.Unlock()

	up := newWSTestUpgrader()
	c, err := up.Upgrade(w, r, http.Header{
		"X-Codex-Turn-State":                  {"sticky-1"},
		"X-Models-Etag":                       {"etag-9"},
		"X-Codex-Websocket-Max-Message-Bytes": {"999999999999"},
		"X-Bifrost-Request-Id":                {"upstream-hop"},
		"Set-Cookie":                          {"session=upstream"},
	})
	if err != nil {
		return
	}
	defer c.Close()
	for {
		_, data, err := c.ReadMessage()
		if err != nil {
			return
		}
		f.mu.Lock()
		f.received = append(f.received, string(data))
		f.mu.Unlock()
		if !strings.Contains(string(data), `"response.create"`) {
			continue
		}
		switch {
		case strings.Contains(string(data), "fail"):
			_ = c.WriteMessage(websocket.TextMessage, []byte(fakeCodexError))
		case strings.Contains(string(data), "hang"):
			_ = c.WriteMessage(websocket.TextMessage, []byte(fakeCodexCreated))
		default:
			_ = c.WriteMessage(websocket.TextMessage, []byte(fakeCodexCreated))
			_ = c.WriteMessage(websocket.TextMessage, []byte(fakeCodexCompleted))
		}
	}
}

func (f *fakeCodexUpstream) snapshot() (connections int, handshakes []http.Header, paths, received []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.connections, append([]http.Header(nil), f.handshakes...), append([]string(nil), f.paths...), append([]string(nil), f.received...)
}

// wsPassthroughHarness is a gateway serving one passthrough WebSocket target over a fake upstream.
type wsPassthroughHarness struct {
	upstream *fakeCodexUpstream
	recorder *wsPassthroughRecorder
	url      string
}

// newWSPassthroughHarness starts the gateway. hostBacked serves the target the way
// /chatgpt_passthrough does, a fixed upstream host that takes the caller's bearer; otherwise the
// provider's own endpoint is dialed with its stored key, as on /openai_passthrough.
func newWSPassthroughHarness(t *testing.T, upstream *fakeCodexUpstream, hostBacked bool, maxBodyMB int) *wsPassthroughHarness {
	t.Helper()
	SetLogger(bifrost.NewDefaultLogger(schemas.LogLevelError))
	upstreamSrv := httptest.NewServer(upstream)
	t.Cleanup(upstreamSrv.Close)

	recorder := newWSPassthroughRecorder()
	client, err := bifrost.Init(context.Background(), schemas.BifrostConfig{
		Account:    wsOneKeyAccount{baseURL: upstreamSrv.URL},
		LLMPlugins: []schemas.LLMPlugin{recorder},
		Logger:     bifrost.NewDefaultLogger(schemas.LogLevelError),
	})
	require.NoError(t, err)
	t.Cleanup(client.Shutdown)

	h := NewWSPassthroughHandler(client, &lib.Config{
		Providers:       map[schemas.ModelProvider]configstore.ProviderConfig{schemas.OpenAI: {}},
		ClientConfig:    &configstore.ClientConfig{MaxRequestBodySizeMB: maxBodyMB},
		WebSocketConfig: &schemas.WebSocketConfig{MaxConnections: 10},
	})
	h.handlerStore = testWSHandlerStore{}
	t.Cleanup(h.Close)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	server := &fasthttp.Server{Handler: func(ctx *fasthttp.RequestCtx) {
		target := &integrations.PassthroughWebSocketTarget{
			Provider:     schemas.OpenAI,
			BaseProvider: schemas.OpenAI,
			Path:         "/v1/responses",
			RawQuery:     string(ctx.URI().QueryString()),
			Headers:      map[string]string{"openai-beta": string(ctx.Request.Header.Peek("OpenAI-Beta"))},
		}
		if hostBacked {
			target.Path = "/backend-api/codex/responses"
			target.UpstreamURL = upstreamSrv.URL
			target.ForwardCallerAuth = true
			target.Headers["authorization"] = string(ctx.Request.Header.Peek("Authorization"))
		}
		h.HandleUpgrade(ctx, target)
	}}
	go func() { _ = server.Serve(ln) }()
	t.Cleanup(func() { _ = server.Shutdown() })

	return &wsPassthroughHarness{upstream: upstream, recorder: recorder, url: "ws://" + ln.Addr().String()}
}

func (h *wsPassthroughHarness) dial(t *testing.T) (*websocket.Conn, *http.Response) {
	t.Helper()
	conn, resp, err := websocket.DefaultDialer.Dial(h.url+"/relay", http.Header{
		"Authorization": {"Bearer caller-token"},
		"OpenAI-Beta":   {"responses_websockets=2026-02-06"},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return conn, resp
}

func readWSText(t *testing.T, conn *websocket.Conn) string {
	t.Helper()
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(5*time.Second)))
	_, data, err := conn.ReadMessage()
	require.NoError(t, err)
	return string(data)
}

// A Codex session's turns all run on the one upstream connection its client connection got, the
// frames reach each side as the other sent them, and each response.create is one logged request.
func TestWSPassthroughRelaysTurnsOnOneUpstreamConnection(t *testing.T) {
	h := newWSPassthroughHarness(t, &fakeCodexUpstream{}, true, 100)
	conn, resp := h.dial(t)

	assert.Equal(t, http.StatusSwitchingProtocols, resp.StatusCode)
	assert.Equal(t, "sticky-1", resp.Header.Get("X-Codex-Turn-State"), "per-connection state the client reads from the handshake")
	assert.Equal(t, "etag-9", resp.Header.Get("X-Models-Etag"))
	assert.Equal(t, "104857600", resp.Header.Get("X-Codex-Websocket-Max-Message-Bytes"), "the gateway's own limit when it is the lower one")
	assert.NotEqual(t, "upstream-hop", resp.Header.Get("X-Bifrost-Request-Id"), "the upstream hop's gateway headers are not forwarded")
	assert.Empty(t, resp.Header.Get("Set-Cookie"))

	first := `{"type":"response.create","model":"gpt-5.5","input":[{"role":"user","content":"one"}]}`
	second := `{"type":"response.create","model":"gpt-5.5","previous_response_id":"resp_1","input":[{"role":"user","content":"two"}]}`
	for _, message := range []string{first, second} {
		require.NoError(t, conn.WriteMessage(websocket.TextMessage, []byte(message)))
		assert.Equal(t, fakeCodexCreated, readWSText(t, conn))
		assert.Equal(t, fakeCodexCompleted, readWSText(t, conn))
		h.recorder.awaitTurn(t)
	}
	interrupt := `{"type":"response.interrupt"}`
	require.NoError(t, conn.WriteMessage(websocket.TextMessage, []byte(interrupt)))
	require.Eventually(t, func() bool { _, _, _, received := h.upstream.snapshot(); return len(received) == 3 }, 5*time.Second, 10*time.Millisecond)

	connections, handshakes, paths, received := h.upstream.snapshot()
	assert.Equal(t, 1, connections, "history lives on the upstream connection, so both turns share it")
	assert.Equal(t, []string{first, second, interrupt}, received)
	assert.Equal(t, "/backend-api/codex/responses", paths[0])
	assert.Equal(t, "Bearer caller-token", handshakes[0].Get("Authorization"), "the caller's token is the upstream credential on a host-backed route")
	assert.Equal(t, "responses_websockets=2026-02-06", handshakes[0].Get("OpenAI-Beta"))

	h.recorder.mu.Lock()
	defer h.recorder.mu.Unlock()
	require.Len(t, h.recorder.pre, 2, "only response.create opens a request")
	assert.Equal(t, fasthttp.MethodPost, h.recorder.pre[0].Method)
	assert.Equal(t, "/backend-api/codex/responses", h.recorder.pre[0].Path)
	assert.Equal(t, "gpt-5.5", h.recorder.pre[0].Model)
	assert.Equal(t, second, string(h.recorder.pre[1].Body))
	require.Len(t, h.recorder.finals, 2)
	for i, final := range h.recorder.finals {
		require.NotNil(t, final.PassthroughUsage, "turn %d", i)
		require.NotNil(t, final.PassthroughUsage.LLMUsage, "turn %d", i)
		assert.Equal(t, 5, final.PassthroughUsage.LLMUsage.TotalTokens, "turn %d", i)
		assert.Equal(t, schemas.PassthroughStreamRequest, final.ExtraFields.RequestType)
	}
	assert.Equal(t, first, string(h.recorder.finals[0].ExtraFields.RawRequest.([]byte)))
	assert.Equal(t, "data: "+fakeCodexCreated+"\n\n", h.recorder.bodies[0], "hooks see the event framed as the SSE data event the HTTP route streams")
	assert.Empty(t, h.recorder.errs)
}

// A provider route dials the provider's own Responses WebSocket endpoint with the stored key, and
// the caller's Authorization never reaches the upstream.
func TestWSPassthroughProviderRouteUsesTheStoredKey(t *testing.T) {
	h := newWSPassthroughHarness(t, &fakeCodexUpstream{}, false, 100)
	conn, _ := h.dial(t)

	require.NoError(t, conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","model":"gpt-5.5","input":[]}`)))
	assert.Equal(t, fakeCodexCreated, readWSText(t, conn))
	assert.Equal(t, fakeCodexCompleted, readWSText(t, conn))
	h.recorder.awaitTurn(t)

	_, handshakes, paths, _ := h.upstream.snapshot()
	assert.Equal(t, "/v1/responses", paths[0])
	assert.Equal(t, "Bearer sk-stored", handshakes[0].Get("Authorization"))
	h.recorder.mu.Lock()
	defer h.recorder.mu.Unlock()
	assert.Equal(t, []string{"key-a"}, h.recorder.preKeys, "the turn is attributed to the key the connection was dialed with")
}

// An upstream that refuses the handshake is answered with its HTTP status. A 200 or a 101 followed
// by a closed socket leaves the client retrying with nothing to diagnose.
func TestWSPassthroughUpstreamHandshakeRefusalIsAnHTTPStatus(t *testing.T) {
	h := newWSPassthroughHarness(t, &fakeCodexUpstream{refuseStatus: http.StatusUnauthorized}, true, 100)
	_, resp, err := websocket.DefaultDialer.Dial(h.url+"/relay", http.Header{"Authorization": {"Bearer caller-token"}})
	require.Error(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

// A turn a plugin refuses is answered with an error event and never reaches the upstream; the
// connection, and the history the upstream holds for it, stay usable.
func TestWSPassthroughRefusedTurnNeverReachesUpstream(t *testing.T) {
	h := newWSPassthroughHarness(t, &fakeCodexUpstream{}, true, 100)
	conn, _ := h.dial(t)

	require.NoError(t, conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","model":"refuse-me"}`)))
	refusal := readWSText(t, conn)
	assert.Contains(t, refusal, `"type":"error"`)
	assert.Contains(t, refusal, `"status":403`)
	assert.Contains(t, refusal, "model is not allowed for this virtual key")

	allowed := `{"type":"response.create","model":"gpt-5.5","input":[]}`
	require.NoError(t, conn.WriteMessage(websocket.TextMessage, []byte(allowed)))
	assert.Equal(t, fakeCodexCreated, readWSText(t, conn))
	assert.Equal(t, fakeCodexCompleted, readWSText(t, conn))

	_, _, _, received := h.upstream.snapshot()
	assert.Equal(t, []string{allowed}, received)
}

// An error event is the WebSocket form of an HTTP error response: the client gets it as sent and
// the turn is recorded as failed with the upstream's status.
func TestWSPassthroughUpstreamErrorEventFailsTheTurn(t *testing.T) {
	h := newWSPassthroughHarness(t, &fakeCodexUpstream{}, true, 100)
	conn, _ := h.dial(t)

	require.NoError(t, conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","model":"gpt-5.5","input":"fail"}`)))
	assert.Equal(t, fakeCodexError, readWSText(t, conn))
	h.recorder.awaitTurn(t)

	h.recorder.mu.Lock()
	defer h.recorder.mu.Unlock()
	require.Len(t, h.recorder.errs, 1)
	require.NotNil(t, h.recorder.errs[0].StatusCode)
	assert.Equal(t, 429, *h.recorder.errs[0].StatusCode)
	assert.Equal(t, "The usage limit has been reached", h.recorder.errs[0].Error.Message)
	assert.Empty(t, h.recorder.finals)
}

// A client that hangs up while a turn is open ends that turn once, as a client disconnect.
func TestWSPassthroughClientDisconnectFailsTheOpenTurnOnce(t *testing.T) {
	h := newWSPassthroughHarness(t, &fakeCodexUpstream{}, true, 100)
	conn, _ := h.dial(t)

	require.NoError(t, conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","model":"gpt-5.5","input":"hang"}`)))
	assert.Equal(t, fakeCodexCreated, readWSText(t, conn))
	require.NoError(t, conn.Close())
	h.recorder.awaitTurn(t)
	time.Sleep(100 * time.Millisecond)

	h.recorder.mu.Lock()
	defer h.recorder.mu.Unlock()
	require.Len(t, h.recorder.errs, 1)
	require.NotNil(t, h.recorder.errs[0].StatusCode)
	assert.Equal(t, 499, *h.recorder.errs[0].StatusCode)
}

// One response.create message is the WebSocket form of one request body, so it takes the HTTP
// request body limit: the handshake advertises it and a larger message closes with 1009.
func TestWSPassthroughClientMessageOverTheBodyLimitClosesWith1009(t *testing.T) {
	h := newWSPassthroughHarness(t, &fakeCodexUpstream{}, true, 1)
	conn, resp := h.dial(t)
	assert.Equal(t, "1048576", resp.Header.Get("X-Codex-Websocket-Max-Message-Bytes"))

	oversize := `{"type":"response.create","model":"gpt-5.5","input":"` + strings.Repeat("a", 2<<20) + `"}`
	require.NoError(t, conn.WriteMessage(websocket.TextMessage, []byte(oversize)))
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(5*time.Second)))
	_, _, err := conn.ReadMessage()
	require.Error(t, err)
	assert.True(t, websocket.IsCloseError(err, websocket.CloseMessageTooBig), "got %v", err)

	_, _, _, received := h.upstream.snapshot()
	assert.Empty(t, received, "an oversize message is never forwarded")
}

// The relay's per-event cost on a content delta: one type lookup to find the turn's end, one
// usage presence check, and the SSE frame handed to post-hooks.
func BenchmarkWSPassthroughTurnChunk(b *testing.B) {
	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()
	turn := &wsPassthroughTurn{ctx: ctx, cancel: cancel, hooks: &bifrost.WSStreamHooks{
		PostHookRunner: func(_ *schemas.BifrostContext, resp *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
			return resp, err
		},
		Cleanup: func() {},
	}}
	event := []byte(`{"type":"response.output_text.delta","sequence_number":7,"item_id":"msg_1","output_index":0,"content_index":0,"delta":"Hello there, this is a streamed token."}`)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		turn.chunk("/backend-api/codex/responses", event)
	}
}
