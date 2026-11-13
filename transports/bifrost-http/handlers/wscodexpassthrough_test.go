package handlers

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fasthttp/router"
	"github.com/fasthttp/websocket"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configtables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/grant"
	"github.com/maximhq/bifrost/plugins/governance"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/integrations"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"github.com/valyala/fasthttp"
)

// codexTestToken is a ChatGPT sign-in token as Codex sends it: a JWT that auth.openai.com issued.
var codexTestToken = "eyJhbGciOiJSUzI1NiJ9." + base64.RawURLEncoding.EncodeToString([]byte(`{"iss":"https://auth.openai.com"}`)) + ".sig"

// fakeChatGPT stands in for chatgpt.com's Codex responses socket. Each accepted connection is
// handed to the test, which plays the upstream side frame by frame.
type fakeChatGPT struct {
	refuseStatus   int
	refuseBody     string
	upgradeHeaders http.Header
	delay          time.Duration // held before answering the handshake

	mu        sync.Mutex
	handshake http.Header
	conns     chan *websocket.Conn
}

func newFakeChatGPT() *fakeChatGPT {
	return &fakeChatGPT{conns: make(chan *websocket.Conn, 4)}
}

func (f *fakeChatGPT) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.handshake = r.Header.Clone()
	f.mu.Unlock()
	time.Sleep(f.delay)
	if f.refuseStatus != 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(f.refuseStatus)
		_, _ = w.Write([]byte(f.refuseBody))
		return
	}
	upgrader := newWSTestUpgrader()
	conn, err := upgrader.Upgrade(w, r, f.upgradeHeaders)
	if err != nil {
		return
	}
	f.conns <- conn
}

func (f *fakeChatGPT) seenHandshake() http.Header {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.handshake
}

func (f *fakeChatGPT) accept(t *testing.T) *websocket.Conn {
	t.Helper()
	select {
	case conn := <-f.conns:
		t.Cleanup(func() { _ = conn.Close() })
		return conn
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the relay to reach the upstream")
		return nil
	}
}

// codexTurnRecord is what the pipeline saw of one turn.
type codexTurnRecord struct {
	requestID string
	model     string
	usage     *schemas.BifrostPassthroughUsage
	status    int
	errStatus int
}

// codexPipelineRecorder observes Codex turns through the plugin pipeline, the way governance and
// logging do, and can refuse a turn the way governance refuses a model the virtual key excludes.
type codexPipelineRecorder struct {
	mu        sync.Mutex
	refuse    *schemas.BifrostError
	answer    *schemas.BifrostResponse
	rewrite   func([]byte) []byte
	preTypes  []schemas.RequestType
	preModels []string
	preSkip   []bool
	finals    []codexTurnRecord
	chunks    int
	chunksBy  map[string]int // chunks seen per turn request id
}

func (*codexPipelineRecorder) GetName() string { return "codex-recorder" }
func (*codexPipelineRecorder) Cleanup() error  { return nil }
func (*codexPipelineRecorder) PreRequestHook(*schemas.BifrostContext, *schemas.BifrostRequest) error {
	return nil
}

func (r *codexPipelineRecorder) PreLLMHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.preTypes = append(r.preTypes, req.RequestType)
	if req.PassthroughRequest != nil {
		r.preModels = append(r.preModels, req.PassthroughRequest.Model)
	}
	skip, _ := ctx.Value(schemas.BifrostContextKeySkipKeySelection).(bool)
	r.preSkip = append(r.preSkip, skip)
	if r.refuse != nil {
		return req, &schemas.LLMPluginShortCircuit{Error: r.refuse}, nil
	}
	if r.answer != nil {
		return req, &schemas.LLMPluginShortCircuit{Response: r.answer}, nil
	}
	if r.rewrite != nil && req.PassthroughRequest != nil {
		rewritten := *req.PassthroughRequest
		rewritten.Body = r.rewrite(rewritten.Body)
		return &schemas.BifrostRequest{RequestType: req.RequestType, PassthroughRequest: &rewritten}, nil, nil
	}
	return req, nil, nil
}

func (r *codexPipelineRecorder) PostLLMHook(ctx *schemas.BifrostContext, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	final, _ := ctx.Value(schemas.BifrostContextKeyStreamEndIndicator).(bool)
	requestID, _ := ctx.Value(schemas.BifrostContextKeyRequestID).(string)
	if !final {
		r.chunks++
		if r.chunksBy == nil {
			r.chunksBy = map[string]int{}
		}
		r.chunksBy[requestID]++
		return resp, bifrostErr, nil
	}
	_, _, model, _ := bifrost.GetResponseFields(resp, bifrostErr) // originalModel is the third field
	record := codexTurnRecord{requestID: requestID, model: model}
	if resp != nil && resp.PassthroughResponse != nil {
		record.usage = resp.PassthroughResponse.PassthroughUsage
		record.status = resp.PassthroughResponse.StatusCode
	}
	if bifrostErr != nil && bifrostErr.StatusCode != nil {
		record.errStatus = *bifrostErr.StatusCode
	}
	r.finals = append(r.finals, record)
	return resp, bifrostErr, nil
}

func (r *codexPipelineRecorder) setRefuse(err *schemas.BifrostError) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.refuse = err
}

func (r *codexPipelineRecorder) setAnswer(answer *schemas.BifrostResponse) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.answer = answer
}

func (r *codexPipelineRecorder) chunksFor(requestID string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.chunksBy[requestID]
}

func (r *codexPipelineRecorder) setRewrite(rewrite func([]byte) []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rewrite = rewrite
}

func (r *codexPipelineRecorder) finalTurns() []codexTurnRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]codexTurnRecord(nil), r.finals...)
}

func (r *codexPipelineRecorder) awaitFinals(t *testing.T, n int) []codexTurnRecord {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if finals := r.finalTurns(); len(finals) >= n {
			return finals
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d finished turns, have %d", n, len(r.finalTurns()))
	return nil
}

// codexRelayFixture serves the relay on a real listener in front of a fake chatgpt.com.
type codexRelayFixture struct {
	handler  *WSCodexPassthroughHandler
	upstream *fakeChatGPT
	recorder *codexPipelineRecorder
	url      string
}

func newCodexRelayFixture(t *testing.T, config *lib.Config) *codexRelayFixture {
	return newCodexRelayFixtureWith(t, config, codexFixtureOptions{})
}

// codexFixtureOptions customizes a relay fixture: the handler store, plugins registered ahead of
// the recorder (the real governance plugin, for admission tests), and the gateway server.
type codexFixtureOptions struct {
	store   testWSHandlerStore
	plugins []schemas.LLMPlugin
	server  func(*fasthttp.Server)
}

func newCodexRelayFixtureWith(t *testing.T, config *lib.Config, opts codexFixtureOptions) *codexRelayFixture {
	t.Helper()
	plugins := opts.plugins
	SetLogger(bifrost.NewDefaultLogger(schemas.LogLevelError))
	recorder := &codexPipelineRecorder{}
	client, err := bifrost.Init(context.Background(), schemas.BifrostConfig{
		Account:    wsTwoKeyAccount{baseURL: "http://127.0.0.1:1"},
		LLMPlugins: append(plugins, recorder),
		Logger:     bifrost.NewDefaultLogger(schemas.LogLevelError),
	})
	require.NoError(t, err)
	t.Cleanup(client.Shutdown)

	upstream := newFakeChatGPT()
	upstreamSrv := httptest.NewServer(upstream)
	t.Cleanup(upstreamSrv.Close)

	h := newWSCodexPassthroughHandler(client, config, opts.store)
	h.upstreamURL = "ws" + strings.TrimPrefix(upstreamSrv.URL, "http") + "/backend-api/codex/responses"
	t.Cleanup(h.Close)

	r := router.New()
	h.RegisterRoutes(r)
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv := &fasthttp.Server{Handler: r.Handler, KeepHijackedConns: true}
	if opts.server != nil {
		opts.server(srv)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Shutdown() })

	return &codexRelayFixture{
		handler:  h,
		upstream: upstream,
		recorder: recorder,
		url:      "ws://" + ln.Addr().String() + integrations.ChatGPTCodexResponsesRoute,
	}
}

// codexHeaders is what Codex sends on the handshake when signed in with ChatGPT through Bifrost.
func codexHeaders() http.Header {
	h := http.Header{}
	h.Set("Authorization", "Bearer "+codexTestToken)
	h.Set("ChatGPT-Account-ID", "acct_1")
	h.Set("OpenAI-Beta", "responses_websockets=2026-02-06")
	h.Set("Originator", "codex_cli_rs")
	h.Set("x-bf-vk", "sk-bf-test")
	h.Set("Cookie", "auth-openid=idp-token")
	return h
}

func (f *codexRelayFixture) dial(t *testing.T, headers http.Header) (*websocket.Conn, *http.Response) {
	t.Helper()
	conn, resp, err := websocket.DefaultDialer.Dial(f.url, headers)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return conn, resp
}

func readCodexFrame(t *testing.T, conn *websocket.Conn) string {
	t.Helper()
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(5*time.Second)))
	_, data, err := conn.ReadMessage()
	require.NoError(t, err)
	return string(data)
}

func sendCodexFrame(t *testing.T, conn *websocket.Conn, frame string) {
	t.Helper()
	require.NoError(t, conn.WriteMessage(websocket.TextMessage, []byte(frame)))
}

const (
	codexCreateFrame    = `{"type":"response.create","model":"gpt-5.5","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}],"store":false,"stream":true}`
	codexCreatedEvent   = `{"type":"response.created","sequence_number":0,"response":{"id":"resp_1","object":"response","status":"in_progress","model":"gpt-5.5","output":[]}}`
	codexDeltaEvent     = `{"type":"response.output_text.delta","sequence_number":1,"item_id":"msg_1","output_index":0,"content_index":0,"delta":"hello"}`
	codexCompletedEvent = `{"type":"response.completed","sequence_number":2,"response":{"id":"resp_1","object":"response","status":"completed","model":"gpt-5.5","service_tier":"priority","output":[],"usage":{"input_tokens":11,"output_tokens":7,"total_tokens":18}}}`
)

// TestCodexWebSocketRefusesBeforeUpgrade: every refusal is a plain HTTP status on the handshake,
// because that is what Codex acts on. A caller without a ChatGPT token gets 426 so Codex moves to
// HTTP at once, and chatgpt.com's own refusal reaches Codex unchanged.
func TestCodexWebSocketRefusesBeforeUpgrade(t *testing.T) {
	f := newCodexRelayFixture(t, nil)

	plain, err := http.Get("http" + strings.TrimPrefix(f.url, "ws"))
	require.NoError(t, err)
	_ = plain.Body.Close()
	assert.Equal(t, http.StatusMethodNotAllowed, plain.StatusCode, "a GET that is not an upgrade keeps the route's 405")

	cases := []struct {
		name   string
		auth   string
		extra  map[string]string
		status int
	}{
		{name: "no credential", status: http.StatusUpgradeRequired},
		{name: "api key", auth: "Bearer sk-proj-abc", status: http.StatusUpgradeRequired},
		{name: "virtual key", auth: "Bearer sk-bf-abc", status: http.StatusUpgradeRequired},
		{name: "jwt another issuer signed", auth: "Bearer eyJhbGciOiJSUzI1NiJ9." + base64.RawURLEncoding.EncodeToString([]byte(`{"iss":"https://idp.example.com"}`)) + ".sig", status: http.StatusUpgradeRequired},
		{name: "another provider", auth: "Bearer " + codexTestToken, extra: map[string]string{"x-model-provider": "anthropic"}, status: http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			headers := http.Header{}
			if tc.auth != "" {
				headers.Set("Authorization", tc.auth)
			}
			for k, v := range tc.extra {
				headers.Set(k, v)
			}
			_, resp, err := websocket.DefaultDialer.Dial(f.url, headers)
			require.ErrorIs(t, err, websocket.ErrBadHandshake)
			require.NotNil(t, resp)
			assert.Equal(t, tc.status, resp.StatusCode)
		})
	}

	f.upstream.refuseStatus = http.StatusUnauthorized
	f.upstream.refuseBody = `{"detail":"token expired"}`
	_, resp, err := websocket.DefaultDialer.Dial(f.url, codexHeaders())
	require.ErrorIs(t, err, websocket.ErrBadHandshake)
	require.NotNil(t, resp)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, "chatgpt.com's 401 reaches Codex, which refreshes its token")
}

// TestCodexWebSocketRefusesAnonymousCallerWhenAuthIsEnforced: on a deployment that enforces auth on
// inference, the ChatGPT token alone is not a Bifrost credential, so the upgrade is refused before
// chatgpt.com is dialed.
func TestCodexWebSocketRefusesAnonymousCallerWhenAuthIsEnforced(t *testing.T) {
	f := newCodexRelayFixture(t, &lib.Config{ClientConfig: &configstore.ClientConfig{EnforceAuthOnInference: true}})
	headers := codexHeaders()
	headers.Del("x-bf-vk")
	_, resp, err := websocket.DefaultDialer.Dial(f.url, headers)
	require.ErrorIs(t, err, websocket.ErrBadHandshake)
	require.NotNil(t, resp)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	assert.Nil(t, f.upstream.seenHandshake(), "chatgpt.com is not dialed for a refused caller")
}

// TestCodexWebSocketRelaysATurn: the handshake carries the caller's ChatGPT token and Codex
// headers and none of Bifrost's, the 101 carries the headers Codex reads, frames cross unchanged
// both ways, and the turn reaches the pipeline as the passthrough stream the HTTP route makes,
// with the usage and served tier of its response.completed.
func TestCodexWebSocketRelaysATurn(t *testing.T) {
	f := newCodexRelayFixture(t, nil)
	f.upstream.upgradeHeaders = http.Header{
		"X-Codex-Turn-State":                  {"ts_1"},
		"Openai-Model":                        {"gpt-5.5"},
		"X-Codex-Websocket-Max-Message-Bytes": {"8388608"},
		"Set-Cookie":                          {"upstream=1"},
	}
	app, resp := f.dial(t, codexHeaders())
	chatgpt := f.upstream.accept(t)

	seen := f.upstream.seenHandshake()
	assert.Equal(t, "Bearer "+codexTestToken, seen.Get("Authorization"))
	assert.Equal(t, "acct_1", seen.Get("ChatGPT-Account-ID"))
	assert.Equal(t, "responses_websockets=2026-02-06", seen.Get("OpenAI-Beta"))
	assert.Empty(t, seen.Get("x-bf-vk"), "Bifrost's own headers stay in the gateway")
	assert.Empty(t, seen.Get("Cookie"), "the gateway cookie stays in the gateway")

	assert.Equal(t, "ts_1", resp.Header.Get("X-Codex-Turn-State"))
	assert.Equal(t, "gpt-5.5", resp.Header.Get("Openai-Model"))
	assert.Equal(t, "8388608", resp.Header.Get("X-Codex-Websocket-Max-Message-Bytes"))
	assert.Empty(t, resp.Header.Get("Set-Cookie"), "only the headers Codex reads ride the 101")

	sendCodexFrame(t, app, codexCreateFrame)
	assert.Equal(t, codexCreateFrame, readCodexFrame(t, chatgpt), "the request frame reaches chatgpt.com unchanged")
	for _, event := range []string{codexCreatedEvent, codexDeltaEvent, codexCompletedEvent} {
		sendCodexFrame(t, chatgpt, event)
		assert.Equal(t, event, readCodexFrame(t, app), "upstream events reach Codex unchanged")
	}

	finals := f.recorder.awaitFinals(t, 1)
	require.Len(t, finals, 1)
	assert.Equal(t, "gpt-5.5", finals[0].model)
	assert.Equal(t, http.StatusOK, finals[0].status)
	require.NotNil(t, finals[0].usage)
	require.NotNil(t, finals[0].usage.LLMUsage)
	assert.Equal(t, 11, finals[0].usage.LLMUsage.PromptTokens)
	assert.Equal(t, 7, finals[0].usage.LLMUsage.CompletionTokens)
	require.NotNil(t, finals[0].usage.ServiceTier)
	assert.Equal(t, "priority", string(*finals[0].usage.ServiceTier))

	f.recorder.mu.Lock()
	assert.Equal(t, []schemas.RequestType{schemas.PassthroughStreamRequest}, f.recorder.preTypes)
	assert.Equal(t, []string{"gpt-5.5"}, f.recorder.preModels)
	assert.Equal(t, []bool{true}, f.recorder.preSkip, "the caller's token is the credential, so no stored key is selected")
	assert.Equal(t, 3, f.recorder.chunks, "every upstream event of the turn reaches the post-hooks")
	f.recorder.mu.Unlock()

	// An interrupt starts no turn and crosses unchanged.
	interrupt := `{"type":"response.interrupt","response_id":"resp_1","mode":"discard_partial_items"}`
	sendCodexFrame(t, app, interrupt)
	assert.Equal(t, interrupt, readCodexFrame(t, chatgpt))
}

// TestCodexWebSocketForwardsTheBodyPluginsRewrote: a plugin that rewrites the turn in its
// pre-hook (a guardrail redacting the prompt) has its body sent to chatgpt.com, not the frame
// Codex sent.
func TestCodexWebSocketForwardsTheBodyPluginsRewrote(t *testing.T) {
	f := newCodexRelayFixture(t, nil)
	f.recorder.setRewrite(func(body []byte) []byte {
		return bytes.Replace(body, []byte(`"text":"hi"`), []byte(`"text":"[redacted]"`), 1)
	})
	app, _ := f.dial(t, codexHeaders())
	chatgpt := f.upstream.accept(t)

	sendCodexFrame(t, app, codexCreateFrame)
	forwarded := readCodexFrame(t, chatgpt)
	assert.Contains(t, forwarded, `"text":"[redacted]"`, "the rewritten body reaches chatgpt.com")
	assert.NotContains(t, forwarded, `"text":"hi"`)
}

// TestCodexWebSocketRefusesFramesThePipelineCannotSee: the relay forwards only the two events
// Codex sends, and refuses any frame chatgpt.com might read as a request the pipeline never saw:
// a repeated "type" key (the relay reads the first, a last-wins parser the second), a binary
// frame, an unknown event, a non-string type and invalid JSON. None reaches chatgpt.com and the
// connection stays open.
func TestCodexWebSocketRefusesFramesThePipelineCannotSee(t *testing.T) {
	f := newCodexRelayFixture(t, nil)
	app, _ := f.dial(t, codexHeaders())
	chatgpt := f.upstream.accept(t)

	refused := []struct {
		name        string
		messageType int
		frame       string
	}{
		{"repeated type key", websocket.TextMessage, `{"type":"response.interrupt","type":"response.create","model":"gpt-5.5","input":"hi"}`},
		{"repeated escaped type key", websocket.TextMessage, `{"type":"response.interrupt","typ\u0065":"response.create","model":"gpt-5.5","input":"hi"}`},
		{"binary frame", websocket.BinaryMessage, codexCreateFrame},
		{"unknown event", websocket.TextMessage, `{"type":"response.fork","model":"gpt-5.5"}`},
		{"type not a string", websocket.TextMessage, `{"type":["response.create"],"model":"gpt-5.5"}`},
		{"invalid json", websocket.TextMessage, `{"type":"response.create",`},
		{"not an object", websocket.TextMessage, `["response.create"]`},
	}
	for _, tc := range refused {
		require.NoError(t, app.WriteMessage(tc.messageType, []byte(tc.frame)), tc.name)
		refusal := readCodexFrame(t, app)
		assert.Contains(t, refusal, `"type":"error"`, tc.name)
		assert.Contains(t, refusal, `"status":400`, tc.name)
	}

	interrupt := `{"type":"response.interrupt","response_id":"resp_1","mode":"discard_partial_items"}`
	sendCodexFrame(t, app, interrupt)
	assert.Equal(t, interrupt, readCodexFrame(t, chatgpt), "nothing refused reached chatgpt.com, and the connection still relays")

	f.recorder.mu.Lock()
	assert.Empty(t, f.recorder.preTypes, "no refused frame started a turn")
	f.recorder.mu.Unlock()
}

// TestCodexWebSocketBillsAnInterruptedTurn: interrupting a turn does not take it off the books.
// It stays open past the interrupt's acknowledgement and is billed with the usage of the terminal
// event that follows, so interrupting cannot be used to skip the budget.
func TestCodexWebSocketBillsAnInterruptedTurn(t *testing.T) {
	f := newCodexRelayFixture(t, nil)
	app, _ := f.dial(t, codexHeaders())
	chatgpt := f.upstream.accept(t)

	sendCodexFrame(t, app, codexCreateFrame)
	readCodexFrame(t, chatgpt)
	for _, event := range []string{
		codexCreatedEvent,
		`{"type":"response.interrupt.accepted","response_id":"resp_1"}`,
		`{"type":"response.incomplete","response":{"id":"resp_1","status":"incomplete","output":[],"usage":{"input_tokens":40,"output_tokens":30,"total_tokens":70}}}`,
	} {
		sendCodexFrame(t, chatgpt, event)
		readCodexFrame(t, app)
	}
	finals := f.recorder.awaitFinals(t, 1)
	require.NotNil(t, finals[0].usage, "the interrupted turn is billed")
	assert.Equal(t, 70, finals[0].usage.LLMUsage.TotalTokens)

	// An interrupted turn whose terminal event never comes ends when its lane starts the next response.
	sendCodexFrame(t, app, codexCreateFrame)
	readCodexFrame(t, chatgpt)
	sendCodexFrame(t, chatgpt, `{"type":"response.created","response":{"id":"resp_2","status":"in_progress","output":[]}}`)
	readCodexFrame(t, app)
	sendCodexFrame(t, chatgpt, `{"type":"response.interrupt.accepted","response_id":"resp_2"}`)
	readCodexFrame(t, app)
	sendCodexFrame(t, app, codexCreateFrame)
	readCodexFrame(t, chatgpt)
	sendCodexFrame(t, chatgpt, `{"type":"response.created","response":{"id":"resp_3","status":"in_progress","output":[]}}`)
	readCodexFrame(t, app)
	finals = f.recorder.awaitFinals(t, 2)
	assert.Equal(t, 499, finals[1].errStatus, "the abandoned interrupted turn is closed as cancelled")
}

// TestCodexWebSocketIgnoresLateEventsOfAFinishedResponse: an event for a response that already
// finished is not attributed to the next turn on the lane, so it cannot take over that turn and
// leave its own response unbilled.
func TestCodexWebSocketIgnoresLateEventsOfAFinishedResponse(t *testing.T) {
	f := newCodexRelayFixture(t, nil)
	app, _ := f.dial(t, codexHeaders())
	chatgpt := f.upstream.accept(t)

	sendCodexFrame(t, app, codexCreateFrame)
	readCodexFrame(t, chatgpt)
	for _, event := range []string{codexCreatedEvent, codexCompletedEvent} {
		sendCodexFrame(t, chatgpt, event)
		readCodexFrame(t, app)
	}
	f.recorder.awaitFinals(t, 1)

	sendCodexFrame(t, app, codexCreateFrame)
	readCodexFrame(t, chatgpt)
	for _, event := range []string{
		`{"type":"response.in_progress","response":{"id":"resp_1","status":"in_progress","output":[]}}`,
		`{"type":"response.created","response":{"id":"resp_2","status":"in_progress","output":[]}}`,
		`{"type":"response.completed","response":{"id":"resp_2","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}}`,
	} {
		sendCodexFrame(t, chatgpt, event)
		readCodexFrame(t, app)
	}
	finals := f.recorder.awaitFinals(t, 2)
	require.NotNil(t, finals[1].usage, "the second turn is billed for its own response")
	assert.Equal(t, 3, finals[1].usage.LLMUsage.TotalTokens)
}

// TestCodexWebSocketRequestErrorDoesNotEndAStreamingTurn: an error event that names no response
// rejects a response.create that never started, so it ends the turn still waiting for a
// response, not the one already streaming. A delta naming no response still belongs to the
// streaming one. The streaming turn stays open and is billed with the usage of its own
// response.completed.
func TestCodexWebSocketRequestErrorDoesNotEndAStreamingTurn(t *testing.T) {
	f := newCodexRelayFixture(t, nil)
	app, _ := f.dial(t, codexHeaders())
	chatgpt := f.upstream.accept(t)

	sendCodexFrame(t, app, codexCreateFrame)
	readCodexFrame(t, chatgpt)
	sendCodexFrame(t, chatgpt, codexCreatedEvent)
	readCodexFrame(t, app)

	second := `{"type":"response.create","model":"gpt-5.5","input":"second"}`
	sendCodexFrame(t, app, second)
	readCodexFrame(t, chatgpt)
	for _, event := range []string{
		codexDeltaEvent,
		`{"type":"error","status":429,"error":{"type":"rate_limit_error","message":"a response is already in progress"}}`,
		codexCompletedEvent,
	} {
		sendCodexFrame(t, chatgpt, event)
		readCodexFrame(t, app)
	}

	finals := f.recorder.awaitFinals(t, 2)
	require.Len(t, finals, 2)
	assert.Equal(t, http.StatusTooManyRequests, finals[0].status, "the error ends the turn that was still waiting for a response")
	assert.Nil(t, finals[0].usage)
	assert.Equal(t, http.StatusOK, finals[1].status)
	require.NotNil(t, finals[1].usage, "the streaming turn is billed for its own response")
	assert.Equal(t, 18, finals[1].usage.LLMUsage.TotalTokens)
	assert.Equal(t, 3, f.recorder.chunksFor(finals[1].requestID), "the delta naming no response belongs to the response streaming on the lane (created, delta, completed)")
	assert.Equal(t, 1, f.recorder.chunksFor(finals[0].requestID), "the waiting turn sees only its error")
}

// TestCodexWebSocketBoundsRequestDataInFlight: a connection may hold only so many bytes of
// request frames for turns that have not finished; past that a turn is refused, not forwarded.
func TestCodexWebSocketBoundsRequestDataInFlight(t *testing.T) {
	f := newCodexRelayFixture(t, nil)
	f.handler.maxRetained = len(codexCreateFrame) + 10
	app, _ := f.dial(t, codexHeaders())
	chatgpt := f.upstream.accept(t)

	sendCodexFrame(t, app, codexCreateFrame)
	readCodexFrame(t, chatgpt)
	sendCodexFrame(t, app, codexCreateFrame)
	refusal := readCodexFrame(t, app)
	assert.Contains(t, refusal, "too_many_in_flight_responses")

	// Once the first turn finishes its bytes are released and the next turn is accepted.
	for _, event := range []string{codexCreatedEvent, codexCompletedEvent} {
		sendCodexFrame(t, chatgpt, event)
		readCodexFrame(t, app)
	}
	f.recorder.awaitFinals(t, 1)
	next := `{"type":"response.create","model":"gpt-5.5","input":"again"}`
	sendCodexFrame(t, app, next)
	assert.Equal(t, next, readCodexFrame(t, chatgpt))
}

// TestCodexWebSocketLimitsSocketsPerCaller: one caller cannot take every slot of the shared
// connection limit; past its own share the upgrade is refused with 429, and a socket it closes
// frees its share.
func TestCodexWebSocketLimitsSocketsPerCaller(t *testing.T) {
	f := newCodexRelayFixture(t, nil)
	f.handler.maxPerCaller = 1
	first, _ := f.dial(t, codexHeaders())
	f.upstream.accept(t)

	_, resp, err := websocket.DefaultDialer.Dial(f.url, codexHeaders())
	require.ErrorIs(t, err, websocket.ErrBadHandshake)
	require.NotNil(t, resp)
	assert.Equal(t, http.StatusTooManyRequests, resp.StatusCode)

	require.NoError(t, first.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second)))
	_ = first.Close()
	require.Eventually(t, func() bool {
		conn, _, err := websocket.DefaultDialer.Dial(f.url, codexHeaders())
		if err != nil {
			return false
		}
		_ = conn.Close()
		return true
	}, 5*time.Second, 50*time.Millisecond, "the caller's share is freed when its socket closes")
}

// TestCodexWebSocketCallerLimitIgnoresUnresolvedVirtualKeys: a virtual key nothing has resolved
// does not name a caller, so sending a different one on each socket does not buy a fresh share;
// such sockets count against the peer address.
func TestCodexWebSocketCallerLimitIgnoresUnresolvedVirtualKeys(t *testing.T) {
	f := newCodexRelayFixture(t, nil)
	f.handler.maxPerCaller = 1
	first := codexHeaders()
	first.Set("x-bf-vk", "sk-bf-forged-1")
	f.dial(t, first)
	f.upstream.accept(t)

	second := codexHeaders()
	second.Set("x-bf-vk", "sk-bf-forged-2")
	_, resp, err := websocket.DefaultDialer.Dial(f.url, second)
	require.ErrorIs(t, err, websocket.ErrBadHandshake)
	require.NotNil(t, resp)
	assert.Equal(t, http.StatusTooManyRequests, resp.StatusCode)
}

// TestCodexWebSocketRefusesAnUnresolvedVirtualKeyWhenAuthIsEnforced: with auth enforced, a
// virtual key that does not resolve is refused at the upgrade, before chatgpt.com is dialed, as
// realtime refuses it.
func TestCodexWebSocketRefusesAnUnresolvedVirtualKeyWhenAuthIsEnforced(t *testing.T) {
	f := newCodexRelayFixture(t, &lib.Config{ClientConfig: &configstore.ClientConfig{EnforceAuthOnInference: true}})
	_, resp, err := websocket.DefaultDialer.Dial(f.url, codexHeaders())
	require.ErrorIs(t, err, websocket.ErrBadHandshake)
	require.NotNil(t, resp)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	assert.Nil(t, f.upstream.seenHandshake(), "chatgpt.com is not dialed for an unresolved key")
}

// TestCodexCallerKey: a virtual key names the caller only once governance resolved it; otherwise
// the socket counts against the peer address.
func TestCodexCallerKey(t *testing.T) {
	reqCtx := &fasthttp.RequestCtx{}
	reqCtx.SetRemoteAddr(&net.TCPAddr{IP: net.IPv4(10, 0, 0, 7), Port: 4000})

	unresolved := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	unresolved.SetValue(schemas.BifrostContextKeyVirtualKey, "sk-bf-forged")
	assert.Equal(t, "ip:10.0.0.7", codexCallerKey(reqCtx, unresolved))

	resolved := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	resolved.SetValue(schemas.BifrostContextKeyVirtualKey, "sk-bf-vk-a")
	resolved.SetGrant(grant.New())
	resolved.Grant().SetIdentity(grant.NewIdentity(grant.NewCredential(grant.CredentialVirtualKey, "sk-bf-vk-a"), nil, &schemas.EntityRef{ID: "vk-a", Name: "vk-a"}, nil, nil, nil, nil))
	resolved.Grant().SetAccess(grant.NewAccess(nil, nil, grant.Union, nil))
	assert.Equal(t, "vk:vk-a", codexCallerKey(reqCtx, resolved), "the resolved key row names the caller, not the header")

	// A direct key resolves the request, but says nothing about a virtual key sent beside it.
	direct := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	direct.SetValue(schemas.BifrostContextKeyVirtualKey, "sk-bf-forged")
	direct.SetValue(schemas.BifrostContextKeyDirectKey, schemas.Key{ID: "direct"})
	assert.Equal(t, "ip:10.0.0.7", codexCallerKey(reqCtx, direct))

	// A user and a key both name the request: the user holds the socket, so one user cannot
	// multiply its share across the keys it owns.
	both := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	both.SetGrant(grant.New())
	both.Grant().SetIdentity(grant.NewIdentity(grant.NewCredential(grant.CredentialVirtualKey, "sk-bf-vk-a"), &schemas.UserRef{ID: "user-1"}, &schemas.EntityRef{ID: "vk-a", Name: "vk-a"}, nil, nil, nil, nil))
	both.Grant().SetAccess(grant.NewAccess(nil, nil, grant.Union, nil))
	assert.Equal(t, "user:user-1", codexCallerKey(reqCtx, both))
}

// codexGovernedFixture serves the relay behind the real governance plugin, with two virtual keys
// that may use OpenAI.
func codexGovernedFixture(t *testing.T, enforceAuth, allowDirectKeys bool) *codexRelayFixture {
	t.Helper()
	config := &lib.Config{
		ClientConfig: &configstore.ClientConfig{EnforceAuthOnInference: enforceAuth},
		Providers: map[schemas.ModelProvider]configstore.ProviderConfig{
			schemas.OpenAI: {Keys: []schemas.Key{{ID: "openai-key", Models: schemas.WhiteList{"*"}, Value: *schemas.NewSecretVar("sk-openai"), Weight: 1}}},
		},
	}
	virtualKey := func(name string) configtables.TableVirtualKey {
		return configtables.TableVirtualKey{
			ID: name, Name: name, Value: *schemas.NewSecretVar("sk-bf-" + name), IsActive: new(true),
			ProviderConfigs: []configtables.TableVirtualKeyProviderConfig{{Provider: "openai", AllowedModels: schemas.WhiteList{"*"}, AllowAllKeys: true}},
		}
	}
	inactive := virtualKey("codex-inactive")
	inactive.IsActive = new(false)
	expired := virtualKey("codex-expired")
	expiredAt := time.Now().Add(-time.Hour)
	expired.ExpiresAt = &expiredAt
	plugin, err := governance.Init(t.Context(), nil, bifrost.NewNoOpLogger(), nil,
		&configstore.GovernanceConfig{VirtualKeys: []configtables.TableVirtualKey{virtualKey("codex-a"), virtualKey("codex-b"), inactive, expired}}, nil, nil, scribeInMemoryStore{config})
	require.NoError(t, err)
	return newCodexRelayFixtureWith(t, config, codexFixtureOptions{store: testWSHandlerStore{allowDirectKeys: allowDirectKeys}, plugins: []schemas.LLMPlugin{plugin}})
}

// TestCodexWebSocketAdmitsAValidVirtualKeyWhenAuthIsEnforced: with auth enforced, a virtual key
// governance resolves is admitted and chatgpt.com is dialed, while a key it does not know is
// refused before the dial.
//
// The fixture also holds an inactive and an expired key, which resolve to a row but grant nothing.
func TestCodexWebSocketAdmitsAValidVirtualKeyWhenAuthIsEnforced(t *testing.T) {
	f := codexGovernedFixture(t, true, false)
	valid := codexHeaders()
	valid.Set("x-bf-vk", "sk-bf-codex-a")
	f.dial(t, valid)
	f.upstream.accept(t)

	forged := codexHeaders()
	forged.Set("x-bf-vk", "sk-bf-forged")
	_, resp, err := websocket.DefaultDialer.Dial(f.url, forged)
	require.ErrorIs(t, err, websocket.ErrBadHandshake)
	require.NotNil(t, resp)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

// TestCodexWebSocketCallerLimitKeysOnTheResolvedVirtualKey: each virtual key governance resolves
// has its own share, while sockets whose virtual key resolved to nothing share the peer address's
// one, even when a direct key admitted them.
func TestCodexWebSocketCallerLimitKeysOnTheResolvedVirtualKey(t *testing.T) {
	f := codexGovernedFixture(t, false, true)
	f.handler.maxPerCaller = 1
	for _, vk := range []string{"sk-bf-codex-a", "sk-bf-codex-b"} {
		headers := codexHeaders()
		headers.Set("x-bf-vk", vk)
		f.dial(t, headers)
		f.upstream.accept(t)
	}

	direct := func(vk string) http.Header {
		headers := codexHeaders()
		headers.Set("x-bf-direct-key", "true")
		headers.Set("x-bf-vk", vk)
		return headers
	}
	f.dial(t, direct("sk-bf-forged-1"))
	f.upstream.accept(t)
	_, resp, err := websocket.DefaultDialer.Dial(f.url, direct("sk-bf-forged-2"))
	require.ErrorIs(t, err, websocket.ErrBadHandshake)
	require.NotNil(t, resp)
	assert.Equal(t, http.StatusTooManyRequests, resp.StatusCode, "a new made-up key beside a direct key buys no new share")
}

// TestCodexWebSocketCallerLimitIgnoresUnusableVirtualKeys: an inactive or expired key resolves
// to a row but grants nothing, so sending one beside a direct key buys no share of its own.
func TestCodexWebSocketCallerLimitIgnoresUnusableVirtualKeys(t *testing.T) {
	f := codexGovernedFixture(t, false, true)
	f.handler.maxPerCaller = 1
	direct := func(vk string) http.Header {
		headers := codexHeaders()
		headers.Set("x-bf-direct-key", "true")
		headers.Set("x-bf-vk", vk)
		return headers
	}
	f.dial(t, direct("sk-bf-codex-inactive"))
	f.upstream.accept(t)
	_, resp, err := websocket.DefaultDialer.Dial(f.url, direct("sk-bf-codex-expired"))
	require.ErrorIs(t, err, websocket.ErrBadHandshake)
	require.NotNil(t, resp)
	assert.Equal(t, http.StatusTooManyRequests, resp.StatusCode)
}

// TestCodexWebSocketReleasesASocketWhoseUpgradeNeverRan: when the server never hands the
// connection over (the 101 could not be written, or the connection was marked to close), the
// upstream socket is closed and the caller's share is given back.
func TestCodexWebSocketReleasesASocketWhoseUpgradeNeverRan(t *testing.T) {
	f := newCodexRelayFixtureWith(t, nil, codexFixtureOptions{server: func(srv *fasthttp.Server) { srv.MaxRequestsPerConn = 1 }})
	f.handler.hijackWait = 100 * time.Millisecond
	conn, _, err := websocket.DefaultDialer.Dial(f.url, codexHeaders())
	if err == nil {
		t.Cleanup(func() { _ = conn.Close() })
	}
	chatgpt := f.upstream.accept(t)

	require.NoError(t, chatgpt.SetReadDeadline(time.Now().Add(5*time.Second)))
	_, _, readErr := chatgpt.ReadMessage()
	var netErr net.Error
	require.Error(t, readErr)
	assert.False(t, errors.As(readErr, &netErr) && netErr.Timeout(), "the relay closes the upstream socket, not a read timeout: %v", readErr)
	assert.Eventually(t, func() bool {
		f.handler.callersMu.Lock()
		defer f.handler.callersMu.Unlock()
		return len(f.handler.callerSockets) == 0
	}, 5*time.Second, 20*time.Millisecond, "the caller's share is released")
}

// TestCodexWebSocketConnectionCapRefusesBeforeUpgrade: handshakes racing for the last slot of the
// connection limit are refused with 429 before the upgrade, never upgraded and then closed.
func TestCodexWebSocketConnectionCapRefusesBeforeUpgrade(t *testing.T) {
	f := newCodexRelayFixture(t, nil)
	f.handler.maxSessions = 1
	f.handler.maxPerCaller = 10
	f.upstream.delay = 200 * time.Millisecond

	const dials = 4
	var wg sync.WaitGroup
	statuses := make(chan int, dials)
	for range dials {
		wg.Add(1)
		go func() {
			defer wg.Done()
			conn, resp, err := websocket.DefaultDialer.Dial(f.url, codexHeaders())
			if err == nil {
				t.Cleanup(func() { _ = conn.Close() })
				statuses <- http.StatusSwitchingProtocols
				return
			}
			if resp != nil {
				statuses <- resp.StatusCode
				return
			}
			statuses <- 0
		}()
	}
	wg.Wait()
	close(statuses)
	counts := map[int]int{}
	for status := range statuses {
		counts[status]++
	}
	assert.Equal(t, map[int]int{http.StatusSwitchingProtocols: 1, http.StatusTooManyRequests: dials - 1}, counts)
}

// TestCodexWebSocketRefusesARewriteBeyondTheInFlightBound: the bound on request data in flight
// applies to the body that would be sent, so a plugin that grows a turn past it gets the turn
// refused, and the turn's row is closed.
func TestCodexWebSocketRefusesARewriteBeyondTheInFlightBound(t *testing.T) {
	f := newCodexRelayFixture(t, nil)
	f.handler.maxRetained = len(codexCreateFrame) + 10
	f.recorder.setRewrite(func(body []byte) []byte {
		return bytes.Replace(body, []byte(`"text":"hi"`), []byte(`"text":"`+strings.Repeat("x", 200)+`"`), 1)
	})
	app, _ := f.dial(t, codexHeaders())
	chatgpt := f.upstream.accept(t)

	sendCodexFrame(t, app, codexCreateFrame)
	refusal := readCodexFrame(t, app)
	assert.Contains(t, refusal, "too_many_in_flight_responses")
	finals := f.recorder.awaitFinals(t, 1)
	assert.Equal(t, http.StatusBadRequest, finals[0].errStatus, "the refused turn's row is closed")

	f.recorder.setRewrite(nil)
	next := `{"type":"response.create","model":"gpt-5.5","input":"next"}`
	sendCodexFrame(t, app, next)
	assert.Equal(t, next, readCodexFrame(t, chatgpt), "the oversized turn never reached chatgpt.com")
}

// TestCodexWebSocketTakesTheLaneFromTheForwardedBody: a plugin that moves a turn to another
// stream_id lane has its turn found by the events chatgpt.com sends back on that lane.
func TestCodexWebSocketTakesTheLaneFromTheForwardedBody(t *testing.T) {
	f := newCodexRelayFixture(t, nil)
	f.recorder.setRewrite(func(body []byte) []byte {
		out, _ := sjson.SetBytes(body, "stream_id", "b")
		return out
	})
	app, _ := f.dial(t, codexHeaders())
	chatgpt := f.upstream.accept(t)

	sendCodexFrame(t, app, codexCreateFrame)
	readCodexFrame(t, chatgpt)
	for _, event := range []string{
		`{"type":"response.created","stream_id":"b","response":{"id":"resp_1","status":"in_progress","output":[]}}`,
		`{"type":"response.completed","stream_id":"b","response":{"id":"resp_1","status":"completed","output":[],"usage":{"input_tokens":11,"output_tokens":7,"total_tokens":18}}}`,
	} {
		sendCodexFrame(t, chatgpt, event)
		readCodexFrame(t, app)
	}
	finals := f.recorder.awaitFinals(t, 1)
	require.NotNil(t, finals[0].usage, "the turn is billed for the response on the lane it was sent on")
	assert.Equal(t, 18, finals[0].usage.LLMUsage.TotalTokens)
}

// TestCodexWebSocketRefusalsNameTheirLane: an error answering a turn carries the turn's stream_id,
// so Codex matches it to the request on that lane; a turn on the default lane gets none.
func TestCodexWebSocketRefusalsNameTheirLane(t *testing.T) {
	f := newCodexRelayFixture(t, nil)
	app, _ := f.dial(t, codexHeaders())
	f.upstream.accept(t)

	f.recorder.setRefuse(&schemas.BifrostError{StatusCode: schemas.Ptr(http.StatusForbidden), Error: &schemas.ErrorField{Message: "model not allowed"}})
	sendCodexFrame(t, app, `{"type":"response.create","stream_id":"b","model":"gpt-5.5","input":"hi"}`)
	refusal := readCodexFrame(t, app)
	assert.Contains(t, refusal, `"status":403`)
	assert.Equal(t, "b", gjson.Get(refusal, "stream_id").String())

	sendCodexFrame(t, app, codexCreateFrame)
	refusal = readCodexFrame(t, app)
	assert.Contains(t, refusal, `"status":403`)
	assert.False(t, gjson.Get(refusal, "stream_id").Exists(), "a turn on the default lane names none")
}

// TestCodexWebSocketBillsAResponseTheClientCouldNotReceive: a response.completed that arrives
// from chatgpt.com after Codex's socket stopped taking writes is still billed, since the usage
// was consumed whether or not Codex saw it.
func TestCodexWebSocketBillsAResponseTheClientCouldNotReceive(t *testing.T) {
	f := newCodexRelayFixture(t, nil)
	app, _ := f.dial(t, codexHeaders())
	chatgpt := f.upstream.accept(t)

	sendCodexFrame(t, app, codexCreateFrame)
	readCodexFrame(t, chatgpt)
	sendCodexFrame(t, chatgpt, codexCreatedEvent)
	readCodexFrame(t, app)

	sessions := f.handler.sessions.Snapshot()
	require.Len(t, sessions, 1)
	// fasthttp hands the handler its hijacked connection wrapped; the TCP socket is the embedded net.Conn.
	underlying := sessions[0].ClientConn().UnderlyingConn()
	if embedded := reflect.ValueOf(underlying).Elem().FieldByName("Conn"); embedded.IsValid() {
		underlying, _ = embedded.Interface().(net.Conn)
	}
	tcp, ok := underlying.(*net.TCPConn)
	require.True(t, ok, "client conn is %T", underlying)
	require.NoError(t, tcp.CloseWrite())

	sendCodexFrame(t, chatgpt, codexCompletedEvent)
	finals := f.recorder.awaitFinals(t, 1)
	require.NotNil(t, finals[0].usage, "the completed response is billed")
	assert.Equal(t, 18, finals[0].usage.LLMUsage.TotalTokens)
}

// TestCodexWebSocketClosesAnIdleSession: a socket with no open turn and no traffic is closed
// after the idle timeout, so an abandoned connection does not hold a slot forever. A socket with
// a turn in flight is left alone.
func TestCodexWebSocketClosesAnIdleSession(t *testing.T) {
	f := newCodexRelayFixture(t, nil)
	f.handler.idleTimeout = 150 * time.Millisecond
	f.handler.idleCheck = 20 * time.Millisecond

	busy, _ := f.dial(t, codexHeaders())
	busyUpstream := f.upstream.accept(t)
	sendCodexFrame(t, busy, codexCreateFrame)
	readCodexFrame(t, busyUpstream)

	idle, _ := f.dial(t, codexHeaders())
	f.upstream.accept(t)
	require.NoError(t, idle.SetReadDeadline(time.Now().Add(5*time.Second)))
	_, _, err := idle.ReadMessage()
	var closeErr *websocket.CloseError
	require.ErrorAs(t, err, &closeErr)
	assert.Equal(t, websocket.CloseNormalClosure, closeErr.Code)
	assert.Equal(t, "idle", closeErr.Text)

	sendCodexFrame(t, busyUpstream, codexCreatedEvent)
	assert.Equal(t, codexCreatedEvent, readCodexFrame(t, busy), "a socket with a turn in flight stays open")
}

// TestCodexWebSocketRefusedTurnKeepsTheConnection: a turn the pipeline refuses (a model the
// virtual key excludes) is answered with an error event and never reaches chatgpt.com, and the
// connection serves the next turn, as an HTTP refusal leaves Codex free to send its next request.
func TestCodexWebSocketRefusedTurnKeepsTheConnection(t *testing.T) {
	f := newCodexRelayFixture(t, nil)
	app, _ := f.dial(t, codexHeaders())
	chatgpt := f.upstream.accept(t)

	f.recorder.setRefuse(&schemas.BifrostError{StatusCode: schemas.Ptr(http.StatusForbidden), Error: &schemas.ErrorField{Message: "model gpt-5.5 is not allowed for this virtual key"}})
	sendCodexFrame(t, app, codexCreateFrame)
	refusal := readCodexFrame(t, app)
	assert.Contains(t, refusal, `"type":"error"`)
	assert.Contains(t, refusal, `"status":403`)
	assert.Contains(t, refusal, "not allowed for this virtual key")

	f.recorder.setRefuse(nil)
	next := `{"type":"response.create","model":"gpt-5.5","input":"next turn"}`
	sendCodexFrame(t, app, next)
	assert.Equal(t, next, readCodexFrame(t, chatgpt), "the refused turn never reached chatgpt.com, and the next one does")
}

// TestCodexWebSocketAnswersAnEmptyShortCircuit: a plugin that answers a turn without a body
// still leaves Codex an answer to its response.create: an error event, never silence.
func TestCodexWebSocketAnswersAnEmptyShortCircuit(t *testing.T) {
	f := newCodexRelayFixture(t, nil)
	app, _ := f.dial(t, codexHeaders())
	chatgpt := f.upstream.accept(t)

	f.recorder.setAnswer(&schemas.BifrostResponse{PassthroughResponse: &schemas.BifrostPassthroughResponse{StatusCode: http.StatusOK}})
	sendCodexFrame(t, app, codexCreateFrame)
	answer := readCodexFrame(t, app)
	assert.Contains(t, answer, `"type":"error"`)
	assert.Contains(t, answer, `"status":500`)

	f.recorder.setAnswer(nil)
	next := `{"type":"response.create","model":"gpt-5.5","input":"next turn"}`
	sendCodexFrame(t, app, next)
	assert.Equal(t, next, readCodexFrame(t, chatgpt), "the answered turn never reached chatgpt.com, and the next one does")
}

// TestCodexWebSocketAttributesLanesToTheirTurns: responses on different stream_id lanes may finish
// in any order, and each is billed to the turn that started it.
func TestCodexWebSocketAttributesLanesToTheirTurns(t *testing.T) {
	f := newCodexRelayFixture(t, nil)
	app, _ := f.dial(t, codexHeaders())
	chatgpt := f.upstream.accept(t)

	sendCodexFrame(t, app, `{"type":"response.create","stream_id":"a","model":"gpt-5.5","input":"one"}`)
	readCodexFrame(t, chatgpt)
	sendCodexFrame(t, app, `{"type":"response.create","stream_id":"b","model":"gpt-5.4-mini","input":"two"}`)
	readCodexFrame(t, chatgpt)

	events := []string{
		`{"type":"response.created","stream_id":"a","response":{"id":"resp_a","status":"in_progress","output":[]}}`,
		`{"type":"response.created","stream_id":"b","response":{"id":"resp_b","status":"in_progress","output":[]}}`,
		`{"type":"response.completed","stream_id":"b","response":{"id":"resp_b","status":"completed","output":[],"usage":{"input_tokens":2,"output_tokens":2,"total_tokens":4}}}`,
		`{"type":"response.completed","stream_id":"a","response":{"id":"resp_a","status":"completed","output":[],"usage":{"input_tokens":5,"output_tokens":5,"total_tokens":10}}}`,
	}
	for _, event := range events {
		sendCodexFrame(t, chatgpt, event)
		readCodexFrame(t, app)
	}

	finals := f.recorder.awaitFinals(t, 2)
	byModel := map[string]int{}
	for _, final := range finals {
		require.NotNil(t, final.usage)
		byModel[final.model] = final.usage.LLMUsage.TotalTokens
	}
	assert.Equal(t, map[string]int{"gpt-5.4-mini": 4, "gpt-5.5": 10}, byModel)
}

// TestCodexWebSocketUpstreamDropFailsTheOpenTurn: chatgpt.com vanishing mid-turn closes Codex's
// socket with 1011 and closes the turn's row as an error rather than leaving it pending.
func TestCodexWebSocketUpstreamDropFailsTheOpenTurn(t *testing.T) {
	f := newCodexRelayFixture(t, nil)
	app, _ := f.dial(t, codexHeaders())
	chatgpt := f.upstream.accept(t)

	sendCodexFrame(t, app, codexCreateFrame)
	readCodexFrame(t, chatgpt)
	require.NoError(t, chatgpt.UnderlyingConn().Close())

	require.NoError(t, app.SetReadDeadline(time.Now().Add(5*time.Second)))
	_, _, err := app.ReadMessage()
	var closeErr *websocket.CloseError
	require.ErrorAs(t, err, &closeErr)
	assert.Equal(t, websocket.CloseInternalServerErr, closeErr.Code)

	finals := f.recorder.awaitFinals(t, 1)
	assert.Equal(t, http.StatusBadGateway, finals[0].errStatus)
}

// TestCodexWebSocketClientDropEndsTheSession: Codex lets a socket go by dropping the connection
// without a close frame. That ends the session normally: the upstream is closed, and a turn still
// open is recorded as the client leaving (499), not as an upstream failure.
func TestCodexWebSocketClientDropEndsTheSession(t *testing.T) {
	f := newCodexRelayFixture(t, nil)
	app, _ := f.dial(t, codexHeaders())
	chatgpt := f.upstream.accept(t)

	sendCodexFrame(t, app, codexCreateFrame)
	readCodexFrame(t, chatgpt)
	require.NoError(t, app.UnderlyingConn().Close())

	require.NoError(t, chatgpt.SetReadDeadline(time.Now().Add(5*time.Second)))
	_, _, err := chatgpt.ReadMessage()
	require.Error(t, err, "the relay closes the upstream when the client leaves")

	finals := f.recorder.awaitFinals(t, 1)
	assert.Equal(t, 499, finals[0].errStatus)
}

// TestCodexWebSocketMirrorsUpstreamClose: chatgpt.com's close code reaches Codex as sent, which is
// how Codex learns a connection reached its lifetime limit or a frame was too large.
func TestCodexWebSocketMirrorsUpstreamClose(t *testing.T) {
	f := newCodexRelayFixture(t, nil)
	app, _ := f.dial(t, codexHeaders())
	chatgpt := f.upstream.accept(t)

	require.NoError(t, chatgpt.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseMessageTooBig, "too big"), time.Now().Add(time.Second)))
	require.NoError(t, app.SetReadDeadline(time.Now().Add(5*time.Second)))
	_, _, err := app.ReadMessage()
	var closeErr *websocket.CloseError
	require.ErrorAs(t, err, &closeErr)
	assert.Equal(t, websocket.CloseMessageTooBig, closeErr.Code)
	assert.Equal(t, "too big", closeErr.Text)
}

// TestCodexWebSocketClosesOversizedFrameWith1009: a client frame over the limit chatgpt.com
// advertised closes the socket with 1009, which Codex answers by retrying the turn over HTTP.
func TestCodexWebSocketClosesOversizedFrameWith1009(t *testing.T) {
	f := newCodexRelayFixture(t, nil)
	f.upstream.upgradeHeaders = http.Header{"X-Codex-Websocket-Max-Message-Bytes": {"1024"}}
	app, _ := f.dial(t, codexHeaders())
	f.upstream.accept(t)

	sendCodexFrame(t, app, `{"type":"response.create","model":"gpt-5.5","input":"`+strings.Repeat("x", 2048)+`"}`)
	require.NoError(t, app.SetReadDeadline(time.Now().Add(5*time.Second)))
	_, _, err := app.ReadMessage()
	var closeErr *websocket.CloseError
	require.ErrorAs(t, err, &closeErr)
	assert.Equal(t, websocket.CloseMessageTooBig, closeErr.Code)
}

// TestCodexWebSocketHonorsTheConnectionLimit: past the configured WebSocket limit the upgrade is
// refused with 429 before chatgpt.com is dialed.
func TestCodexWebSocketHonorsTheConnectionLimit(t *testing.T) {
	f := newCodexRelayFixture(t, &lib.Config{WebSocketConfig: &schemas.WebSocketConfig{MaxConnections: 1}})
	f.dial(t, codexHeaders())
	f.upstream.accept(t)

	_, resp, err := websocket.DefaultDialer.Dial(f.url, codexHeaders())
	require.ErrorIs(t, err, websocket.ErrBadHandshake)
	require.NotNil(t, resp)
	assert.Equal(t, http.StatusTooManyRequests, resp.StatusCode)
}

func TestCodexClientReadLimit(t *testing.T) {
	assert.Equal(t, int64(1024), codexClientReadLimit("1024"))
	assert.Equal(t, int64(codexMaxClientFrameBytes), codexClientReadLimit(""))
	assert.Equal(t, int64(codexMaxClientFrameBytes), codexClientReadLimit("not-a-number"))
	assert.Equal(t, int64(codexMaxClientFrameBytes), codexClientReadLimit("999999999999"), "an upstream limit above the cap is capped")
}

// BenchmarkPeekCodexEvent measures the per-frame read the relay does on every upstream event.
func BenchmarkPeekCodexEvent(b *testing.B) {
	frames := [][]byte{[]byte(codexDeltaEvent), []byte(codexCompletedEvent)}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = peekCodexEvent(frames[i%len(frames)])
	}
}
