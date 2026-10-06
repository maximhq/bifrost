package handlers

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/fasthttp/router"
	ws "github.com/fasthttp/websocket"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	bfws "github.com/maximhq/bifrost/transports/bifrost-http/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// dialLiveTestUpstream returns Bifrost's upstream connection and the fake OpenAI end of it.
func dialLiveTestUpstream(t *testing.T) (*bfws.UpstreamConn, *ws.Conn) {
	t.Helper()
	upgrader := ws.FastHTTPUpgrader{CheckOrigin: func(*fasthttp.RequestCtx) bool { return true }}
	serverConns := make(chan *ws.Conn, 1)
	release := make(chan struct{})
	r := router.New()
	r.GET("/v1/live/sessions", func(ctx *fasthttp.RequestCtx) {
		_ = upgrader.Upgrade(ctx, func(conn *ws.Conn) {
			serverConns <- conn
			<-release
		})
	})
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	// KeepHijackedConns lets the fake provider really drop its connection.
	srv := &fasthttp.Server{Handler: r.Handler, KeepHijackedConns: true}
	go func() { _ = srv.Serve(ln) }()

	upstream, err := bfws.DialUpstream("ws://"+ln.Addr().String()+"/v1/live/sessions", nil, schemas.OpenAI, "key-1", nil)
	require.NoError(t, err)
	var openai *ws.Conn
	select {
	case openai = <-serverConns:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the fake upstream connection")
	}
	t.Cleanup(func() {
		close(release)
		_ = upstream.Close()
		_ = srv.Shutdown()
	})
	return upstream, openai
}

type fakeLiveModels struct{ allowed bool }

func (f fakeLiveModels) KeySupportsModel(schemas.ModelProvider, schemas.Key, string) bool {
	return f.allowed
}

type liveRelayFixture struct {
	runner *fakeLiveRunner
	app    *ws.Conn // the caller's end
	openai *ws.Conn // the fake provider's end
	done   chan struct{}
}

// newTestLiveController wires a controller the way admission does, with test-sized timers.
func testLiveUpdatePolicy(models liveModelChecker) liveUpdatePolicy {
	return liveUpdatePolicy{
		models:         models,
		provider:       schemas.OpenAI,
		key:            schemas.Key{ID: "key-1", Aliases: schemas.KeyAliases{"terra": {ModelID: "gpt-5.6-terra"}}},
		checkKeyModels: true,
	}
}

func newTestLiveController(models liveModelChecker, meter *liveMeter, io liveSessionIO) *liveSessionController {
	return &liveSessionController{
		liveUpdatePolicy: testLiveUpdatePolicy(models),
		io:               io,
		meter:            meter,
		drainTimeout:     time.Second,
		staleCheckEvery:  time.Hour,
		upstreamDone:     make(chan struct{}),
	}
}

func startLiveRelay(t *testing.T, models liveModelChecker, backendModel string) *liveRelayFixture {
	t.Helper()
	bifrostSide, app, cleanup := dialRealtimeTestConn(t)
	t.Cleanup(cleanup)
	upstream, openai := dialLiveTestUpstream(t)

	runner := &fakeLiveRunner{}
	meter := newTestLiveMeter(runner)
	require.Nil(t, meter.admit("gpt-live-1", backendModel))
	relay := &liveWSRelay{clientConn: newRealtimeClientConn(bifrostSide), upstream: upstream}
	relay.liveSessionController = newTestLiveController(models, meter, relay)
	done := make(chan struct{})
	go func() {
		relay.run()
		close(done)
	}()
	// Cleanups run last-in first-out: end the relay before its sockets are released, as the
	// upgrade handler does by blocking on run.
	t.Cleanup(func() {
		_ = openai.UnderlyingConn().Close()
		<-done
	})
	return &liveRelayFixture{runner: runner, app: app, openai: openai, done: done}
}

func readFrame(t *testing.T, conn *ws.Conn) string {
	t.Helper()
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(5*time.Second)))
	_, message, err := conn.ReadMessage()
	require.NoError(t, err)
	return string(message)
}

func sendFrame(t *testing.T, conn *ws.Conn, frame string) {
	t.Helper()
	require.NoError(t, conn.WriteMessage(ws.TextMessage, []byte(frame)))
}

func (f *liveRelayFixture) waitDone(t *testing.T) {
	t.Helper()
	select {
	case <-f.done:
	case <-time.After(5 * time.Second):
		t.Fatal("relay did not end")
	}
}

func (f *liveRelayFixture) voiceSeconds(t *testing.T) []float64 {
	t.Helper()
	_, posts, _ := f.runner.snapshot()
	var seconds []float64
	for _, post := range posts {
		if post.model == "gpt-live-1" && post.resp != nil {
			seconds = append(seconds, postSeconds(t, post))
		}
	}
	return seconds
}

func TestLiveRelayForwardsFramesAndBillsUntilSessionClosed(t *testing.T) {
	t.Parallel()
	f := startLiveRelay(t, fakeLiveModels{allowed: true}, "gpt-5.6-luna")

	// Audio passes through byte for byte in both directions.
	appendFrame := `{"type":"session.input_audio.append","audio":"AAAA"}`
	sendFrame(t, f.app, appendFrame)
	assert.Equal(t, appendFrame, readFrame(t, f.openai))
	deltaFrame := `{"type":"session.output_audio.delta","delta":"BBBB"}`
	sendFrame(t, f.openai, deltaFrame)
	assert.Equal(t, deltaFrame, readFrame(t, f.app))

	for _, frame := range []string{
		`{"type":"session.started","session":{"id":"live_1","model":"gpt-live-1"}}`,
		`{"type":"session.usage.updated","usage":{"seconds":14}}`,
		`{"type":"session.usage.updated","usage":{"seconds":31}}`,
		`{"type":"response.event","delegation_id":"item_1","event":{"type":"response.completed","response":{"id":"resp_1","model":"gpt-5.6-luna","service_tier":"default","output":[],"usage":{"input_tokens":120,"output_tokens":30,"total_tokens":150,"input_tokens_details":{"cached_tokens":64}}}}}`,
	} {
		sendFrame(t, f.openai, frame)
		assert.Equal(t, frame, readFrame(t, f.app), "control events are forwarded unchanged")
	}

	sendFrame(t, f.app, `{"type":"session.close"}`)
	assert.Equal(t, `{"type":"session.close"}`, readFrame(t, f.openai))
	closed := `{"type":"session.closed","reason":"close_requested","usage":{"seconds":37}}`
	sendFrame(t, f.openai, closed)
	assert.Equal(t, closed, readFrame(t, f.app))
	f.waitDone(t)

	assert.Equal(t, []float64{31, 6}, f.voiceSeconds(t), "a full window, then the rest at session.closed")
	_, posts, _ := f.runner.snapshot()
	var backend *schemas.BifrostResponsesResponse
	for _, post := range posts {
		// Units opened before and after session.started share one group.
		assert.Equal(t, "bf-session-1", post.parentID)
		if post.model == "gpt-5.6-luna" && post.resp != nil && post.resp.Usage.TotalTokens > 0 {
			backend = post.resp
		}
	}
	require.NotNil(t, backend, "the backend response is billed")
	assert.Equal(t, 150, backend.Usage.TotalTokens)
	assert.Equal(t, 64, backend.Usage.InputTokensDetails.CachedReadTokens, "OpenAI's cached_tokens are read")
}

func TestLiveRelayClosesUpstreamWhenClientLeaves(t *testing.T) {
	t.Parallel()
	f := startLiveRelay(t, fakeLiveModels{allowed: true}, "")

	sendFrame(t, f.openai, `{"type":"session.usage.updated","usage":{"seconds":9}}`)
	readFrame(t, f.app)
	require.NoError(t, f.app.Close())

	assert.Equal(t, `{"type":"session.close"}`, readFrame(t, f.openai), "Bifrost ends the session itself")
	sendFrame(t, f.openai, `{"type":"session.closed","reason":"close_requested","usage":{"seconds":12}}`)
	f.waitDone(t)
	assert.Equal(t, []float64{12}, f.voiceSeconds(t), "the final usage is still billed")
}

func TestLiveRelayEndsSessionWhenBudgetRunsOut(t *testing.T) {
	t.Parallel()
	f := startLiveRelay(t, fakeLiveModels{allowed: true}, "")

	f.runner.setRefuse(true)
	sendFrame(t, f.openai, `{"type":"session.usage.updated","usage":{"seconds":31}}`)
	refusal := readFrame(t, f.app)
	assert.Contains(t, refusal, `"type":"error"`)
	assert.Contains(t, refusal, `"insufficient_quota"`)
	assert.Contains(t, readFrame(t, f.app), `"session.usage.updated"`)

	assert.Equal(t, `{"type":"session.close"}`, readFrame(t, f.openai))
	sendFrame(t, f.openai, `{"type":"session.closed","reason":"close_requested","usage":{"seconds":33}}`)
	readFrame(t, f.app)
	f.waitDone(t)
	assert.Equal(t, []float64{33}, f.voiceSeconds(t), "seconds past the budget are billed on the admitted unit")
}

func TestLiveRelaySessionUpdateChecksBackendModel(t *testing.T) {
	t.Parallel()

	refused := startLiveRelay(t, fakeLiveModels{allowed: false}, "gpt-5.6-luna")
	sendFrame(t, refused.app, `{"type":"session.update","session":{"delegation":{"type":"responses","responses":{"model":"gpt-5.6-sol"}}}}`)
	errFrame := readFrame(t, refused.app)
	assert.Contains(t, errFrame, "does not support model gpt-5.6-sol")
	marker := `{"type":"session.input_audio.append","audio":"CCCC"}`
	sendFrame(t, refused.app, marker)
	assert.Equal(t, marker, readFrame(t, refused.openai), "the refused update never reached the provider")

	allowed := startLiveRelay(t, fakeLiveModels{allowed: true}, "gpt-5.6-luna")
	sendFrame(t, allowed.app, `{"type":"session.update","session":{"delegation":{"type":"responses","responses":{"model":"openai/terra","reasoning":{"effort":"low"}}}}}`)
	forwarded := readFrame(t, allowed.openai)
	assert.Contains(t, forwarded, `"model":"gpt-5.6-terra"`, "provider prefix and key alias are resolved")
	assert.Contains(t, forwarded, `"reasoning":{"effort":"low"}`, "the rest of the frame is untouched")
	opens, _, _ := allowed.runner.snapshot()
	assert.Equal(t, fakeLiveOpen{model: "terra", kind: liveUnitBackend, continuation: true}, opens[len(opens)-1], "governance admitted the new model")

	allowed.runner.setRefuse(true)
	sendFrame(t, allowed.app, `{"type":"session.update","session":{"delegation":{"type":"responses","responses":{"model":"gpt-5.6-sol"}}}}`)
	assert.Contains(t, readFrame(t, allowed.app), `"insufficient_quota"`)

	wrongProvider := startLiveRelay(t, fakeLiveModels{allowed: true}, "gpt-5.6-luna")
	sendFrame(t, wrongProvider.app, `{"type":"session.update","session":{"delegation":{"responses":{"model":"anthropic/claude-opus-5"}}}}`)
	assert.Contains(t, readFrame(t, wrongProvider.app), "must be served by openai")
}

func TestLiveRelayBillsLastReportedUsageWhenUpstreamNeverCloses(t *testing.T) {
	t.Parallel()
	f := startLiveRelay(t, fakeLiveModels{allowed: true}, "")

	sendFrame(t, f.openai, `{"type":"session.usage.updated","usage":{"seconds":20}}`)
	readFrame(t, f.app)
	require.NoError(t, f.app.Close())
	assert.Equal(t, `{"type":"session.close"}`, readFrame(t, f.openai))
	// The provider never answers; the drain timeout ends the session.
	f.waitDone(t)
	assert.Equal(t, []float64{20}, f.voiceSeconds(t))
}

func TestLiveRelayReportsUpstreamDrop(t *testing.T) {
	t.Parallel()
	f := startLiveRelay(t, fakeLiveModels{allowed: true}, "")

	sendFrame(t, f.openai, `{"type":"session.usage.updated","usage":{"seconds":16}}`)
	readFrame(t, f.app)
	require.NoError(t, f.openai.UnderlyingConn().Close())
	assert.Contains(t, readFrame(t, f.app), "ended before session.closed")
	f.waitDone(t)
	assert.Equal(t, []float64{16}, f.voiceSeconds(t))
}

func TestReadLiveSessionStart(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		frame   string
		wantErr string
	}{
		{"not session.start", `{"type":"session.update","session":{"model":"gpt-live-1"}}`, "must be session.start"},
		{"missing model", `{"type":"session.start","session":{}}`, "requires session.model"},
		{"malformed", `{"type":"session.start","session":`, "failed to parse"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bifrostSide, app, cleanup := dialRealtimeTestConn(t)
			defer cleanup()
			sendFrame(t, app, tc.frame)
			_, _, err := readLiveSessionStart(newRealtimeClientConn(bifrostSide))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}

	// The cap is the connection's read limit: an oversized frame fails the read at the limit
	// instead of being buffered whole and then measured. The sender's write outlives that read
	// (nothing drains the rest), so it runs aside and is released by closing its socket.
	t.Run("oversized", func(t *testing.T) {
		bifrostSide, app, cleanup := dialRealtimeTestConn(t)
		defer cleanup()
		oversized := `{"type":"session.start","session":{"model":"gpt-live-1","instructions":"` + strings.Repeat("a", liveMaxFrameBytes) + `"}}`
		written := make(chan error, 1)
		go func() { written <- app.WriteMessage(ws.TextMessage, []byte(oversized)) }()
		_, _, err := readLiveSessionStart(newRealtimeClientConn(bifrostSide))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "read limit")
		_ = app.Close()
		<-written
	})

	bifrostSide, app, cleanup := dialRealtimeTestConn(t)
	defer cleanup()
	start := `{"type":"session.start","session":{"model":"openai/gpt-live-1","delegation":{"type":"responses","responses":{"model":"gpt-5.6-luna"}}}}`
	sendFrame(t, app, start)
	frame, session, err := readLiveSessionStart(newRealtimeClientConn(bifrostSide))
	require.NoError(t, err)
	assert.Equal(t, start, string(frame))
	assert.Equal(t, "openai/gpt-live-1", session.Model)
}

// fakeLiveDialProvider dials whatever URL the test names.
type fakeLiveDialProvider struct {
	schemas.LiveProvider
	url string
}

func (f fakeLiveDialProvider) LiveWebSocketURL(schemas.Key, schemas.LiveConnectionKind, string) (string, *schemas.BifrostError) {
	return f.url, nil
}

func (f fakeLiveDialProvider) LiveHeaders(*schemas.BifrostContext, schemas.Key) (map[string]string, *schemas.BifrostError) {
	return nil, nil
}

func TestLiveDialMapsProviderRefusals(t *testing.T) {
	if logger == nil {
		SetLogger(&mockLogger{})
	}
	r := router.New()
	r.GET("/v1/live/sessions/{id}/attach", func(ctx *fasthttp.RequestCtx) { ctx.SetStatusCode(fasthttp.StatusNotFound) })
	r.GET("/v1/live/sessions", func(ctx *fasthttp.RequestCtx) { ctx.SetStatusCode(fasthttp.StatusServiceUnavailable) })
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv := &fasthttp.Server{Handler: r.Handler}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Shutdown() })
	base := "ws://" + ln.Addr().String() + "/v1/live/sessions"

	pool := bfws.NewPool(nil)
	t.Cleanup(pool.Close)
	h := &WSLiveHandler{config: &lib.Config{}, pool: pool}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	key := schemas.Key{ID: "key-1"}

	_, bifrostErr := h.dial(ctx, fakeLiveDialProvider{url: base + "/ls_secret/attach"}, schemas.OpenAI, key, schemas.LiveConnectionSideband, "ls_secret")
	require.NotNil(t, bifrostErr)
	assert.Equal(t, fasthttp.StatusNotFound, *bifrostErr.StatusCode, "the provider's 404 is the client's 404")
	assert.Contains(t, bifrostErr.Error.Message, "sideband needs a WebRTC or SIP session")
	assert.NotContains(t, bifrostErr.Error.Message, "ls_secret", "the provider's session id stays behind the handle")

	_, bifrostErr = h.dial(ctx, fakeLiveDialProvider{url: base}, schemas.OpenAI, key, schemas.LiveConnectionPrimary, "")
	require.NotNil(t, bifrostErr)
	assert.Equal(t, fasthttp.StatusBadGateway, *bifrostErr.StatusCode)
	assert.NotContains(t, bifrostErr.Error.Message, ln.Addr().String())
}

func TestLiveRelayBillsSidebandBackendSwitch(t *testing.T) {
	t.Parallel()
	f := startLiveRelay(t, fakeLiveModels{allowed: true}, "gpt-5.6-luna")

	// A sideband switched the backend: OpenAI's acknowledgement moves billing to the new model.
	updated := `{"type":"session.updated","session":{"id":"live_1","delegation":{"type":"responses","responses":{"model":"gpt-5.6-sol"}}}}`
	sendFrame(t, f.openai, updated)
	assert.Equal(t, updated, readFrame(t, f.app))
	sendFrame(t, f.openai, `{"type":"response.event","delegation_id":"item_1","event":{"type":"response.completed","response":{"id":"resp_1","model":"gpt-5.6-sol","output":[],"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}}}`)
	readFrame(t, f.app)

	sendFrame(t, f.app, `{"type":"session.close"}`)
	readFrame(t, f.openai)
	sendFrame(t, f.openai, `{"type":"session.closed","reason":"close_requested","usage":{"seconds":5}}`)
	readFrame(t, f.app)
	f.waitDone(t)

	_, posts, _ := f.runner.snapshot()
	var billed []string
	for _, post := range posts {
		if post.resp != nil && post.resp.Usage != nil && post.resp.Usage.TotalTokens > 0 {
			billed = append(billed, post.model)
		}
	}
	assert.Equal(t, []string{"gpt-5.6-sol"}, billed, "backend tokens land on the model the session now runs")
}

func TestLiveSidebandRelayChecksUpdatesAndEndsAlone(t *testing.T) {
	t.Parallel()
	bifrostSide, app, cleanup := dialRealtimeTestConn(t)
	t.Cleanup(cleanup)
	upstream, openai := dialLiveTestUpstream(t)

	relay := &liveSidebandRelay{
		liveUpdatePolicy: testLiveUpdatePolicy(fakeLiveModels{allowed: false}),
		clientConn:       newRealtimeClientConn(bifrostSide),
		upstream:         upstream,
	}
	done := make(chan struct{})
	go func() {
		relay.run()
		close(done)
	}()
	t.Cleanup(func() {
		_ = openai.UnderlyingConn().Close()
		<-done
	})

	// Session events and transcripts reach the sideband unchanged.
	for _, frame := range []string{
		`{"type":"session.updated","session":{"id":"live_1","model":"gpt-live-1"}}`,
		`{"type":"session.input_transcript.delta","delta":"hello","start_ms":0,"end_ms":400}`,
	} {
		sendFrame(t, openai, frame)
		assert.Equal(t, frame, readFrame(t, app))
	}

	// A backend switch the key cannot serve is refused here, before it reaches OpenAI.
	sendFrame(t, app, `{"type":"session.update","session":{"delegation":{"type":"responses","responses":{"model":"gpt-5.6-sol"}}}}`)
	assert.Contains(t, readFrame(t, app), "does not support model gpt-5.6-sol")
	command := `{"type":"session.instructions.append","delegation_id":null,"content":"be brief"}`
	sendFrame(t, app, command)
	assert.Equal(t, command, readFrame(t, openai))

	// The sideband leaving ends its own connection only: no session.close is sent for the primary.
	require.NoError(t, app.Close())
	require.NoError(t, openai.SetReadDeadline(time.Now().Add(5*time.Second)))
	_, frame, err := openai.ReadMessage()
	assert.Error(t, err, "got %q instead of the sideband's socket closing", frame)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("sideband relay did not end")
	}
}

func TestLiveAttachRefusesBeforeUpgrade(t *testing.T) {
	t.Parallel()

	attach := func(enforceAuth bool, id string) *fasthttp.RequestCtx {
		config := &lib.Config{ClientConfig: &configstore.ClientConfig{EnforceAuthOnInference: enforceAuth}}
		h := &WSLiveHandler{gateway: &liveGateway{config: config}, config: config}
		ctx := &fasthttp.RequestCtx{}
		ctx.Request.SetRequestURI("/v1/live/sessions/" + id + "/attach")
		ctx.SetUserValue("session_id", id)
		h.handleAttach(ctx)
		return ctx
	}

	ctx := attach(true, "live_1")
	assert.Equal(t, fasthttp.StatusUnauthorized, ctx.Response.StatusCode(), "anonymous callers are refused first")

	ctx = attach(false, " ")
	assert.Equal(t, fasthttp.StatusBadRequest, ctx.Response.StatusCode())
	assert.Contains(t, string(ctx.Response.Body()), "session id is required")
}

func TestLiveRelayCarriesTranscriptOnSessionEnd(t *testing.T) {
	t.Parallel()
	f := startLiveRelay(t, fakeLiveModels{allowed: true}, "")

	for _, frame := range []string{
		`{"type":"session.input_transcript.delta","delta":"Hi ","start_ms":0,"end_ms":200}`,
		`{"type":"session.input_transcript.delta","delta":"there.","start_ms":200,"end_ms":400}`,
		`{"type":"session.output_transcript.delta","delta":"Hello!","start_ms":900,"end_ms":1200}`,
	} {
		sendFrame(t, f.openai, frame)
		assert.Equal(t, frame, readFrame(t, f.app), "transcripts are relayed as sent")
	}
	sendFrame(t, f.app, `{"type":"session.close"}`)
	readFrame(t, f.openai)
	sendFrame(t, f.openai, `{"type":"session.closed","reason":"close_requested","usage":{"seconds":3}}`)
	readFrame(t, f.app)
	f.waitDone(t)

	_, posts, _ := f.runner.snapshot()
	last := posts[len(posts)-1]
	require.True(t, last.end)
	require.NotNil(t, last.live)
	require.Len(t, last.live.Transcript, 2, "the closing unit carries the conversation")
	assert.Equal(t, schemas.LiveTranscriptLine{Role: "user", Text: "Hi there.", StartMs: 0, EndMs: 400}, last.live.Transcript[0])
	assert.Equal(t, schemas.LiveTranscriptLine{Role: "assistant", Text: "Hello!", StartMs: 900, EndMs: 1200}, last.live.Transcript[1])
}

func TestLiveRelayAssemblesDelegationOutputFromItemEvents(t *testing.T) {
	t.Parallel()
	f := startLiveRelay(t, fakeLiveModels{allowed: true}, "gpt-5.6-luna")

	// OpenAI delivers a delegation's output items one by one and the terminal event with none.
	for _, frame := range []string{
		`{"type":"response.event","delegation_id":"item_1","event":{"type":"response.output_item.done","output_index":0,"item":{"id":"rs_1","type":"reasoning","summary":[],"encrypted_content":"xxx"}}}`,
		`{"type":"response.event","delegation_id":"item_1","event":{"type":"response.output_item.done","output_index":1,"item":{"id":"ws_1","type":"web_search_call","status":"completed","action":{"type":"search","query":"weather paris"}}}}`,
		`{"type":"response.event","delegation_id":"item_1","event":{"type":"response.output_item.done","output_index":2,"item":{"id":"msg_1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"It's sunny in Paris."}]}}}`,
		`{"type":"response.event","delegation_id":"item_1","event":{"type":"response.completed","response":{"id":"resp_1","model":"gpt-5.6-luna","output":[],"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}}}`,
	} {
		sendFrame(t, f.openai, frame)
		assert.Equal(t, frame, readFrame(t, f.app), "events reach the client unchanged")
	}
	// A function call: the backend's first response ends on the call, the app answers on the
	// socket, and a second response of the same delegation carries the message.
	for _, frame := range []string{
		`{"type":"session.delegation.created","delegation":{"id":"item_2","target":"responses"}}`,
		`{"type":"response.event","delegation_id":"item_2","event":{"type":"response.output_item.done","output_index":0,"item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"get_calendar","arguments":"{\"date\":\"today\"}"}}}`,
		`{"type":"response.event","delegation_id":"item_2","event":{"type":"response.completed","response":{"id":"resp_2","model":"gpt-5.6-luna","output":[],"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}}}`,
	} {
		sendFrame(t, f.openai, frame)
		readFrame(t, f.app)
	}
	sendFrame(t, f.app, `{"type":"response.item.create","event_id":"tool_result_1","item":{"type":"function_call_output","call_id":"call_1","output":"{\"events\":[]}"}}`)
	readFrame(t, f.openai)
	for _, frame := range []string{
		`{"type":"response.event","delegation_id":"item_2","event":{"type":"response.output_item.done","output_index":0,"item":{"id":"msg_2","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Nothing today."}]}}}`,
		`{"type":"response.event","delegation_id":"item_2","event":{"type":"response.completed","response":{"id":"resp_3","model":"gpt-5.6-luna","output":[],"usage":{"input_tokens":12,"output_tokens":4,"total_tokens":16}}}}`,
	} {
		sendFrame(t, f.openai, frame)
		readFrame(t, f.app)
	}
	sendFrame(t, f.app, `{"type":"session.close"}`)
	readFrame(t, f.openai)
	sendFrame(t, f.openai, `{"type":"session.closed","reason":"close_requested","usage":{"seconds":5}}`)
	readFrame(t, f.app)
	f.waitDone(t)

	_, posts, _ := f.runner.snapshot()
	var billed []fakeLivePost
	for _, post := range posts {
		if post.resp != nil && post.resp.Usage != nil && post.resp.Usage.TotalTokens > 0 {
			billed = append(billed, post)
		}
	}
	require.Len(t, billed, 3, "each backend response is billed")
	assert.Equal(t, "resp_1", *billed[0].resp.ID)
	assert.Equal(t, "item_1", billed[0].delegationID)
	require.Len(t, billed[0].resp.Output, 2, "the tool call and the message are attached; reasoning is not")
	assert.Equal(t, schemas.ResponsesMessageTypeWebSearchCall, *billed[0].resp.Output[0].Type)
	assert.Equal(t, schemas.ResponsesMessageTypeMessage, *billed[0].resp.Output[1].Type)

	assert.Equal(t, "resp_2", *billed[1].resp.ID)
	assert.Equal(t, "item_2", billed[1].delegationID)
	require.Len(t, billed[1].resp.Output, 1)
	assert.Equal(t, schemas.ResponsesMessageTypeFunctionCall, *billed[1].resp.Output[0].Type)

	assert.Equal(t, "resp_3", *billed[2].resp.ID)
	assert.Equal(t, "item_2", billed[2].delegationID, "the continuation belongs to the same delegation")
	require.Len(t, billed[2].resp.Output, 2, "the app's result precedes the answer")
	assert.Equal(t, schemas.ResponsesMessageTypeFunctionCallOutput, *billed[2].resp.Output[0].Type)
	assert.Equal(t, "call_1", *billed[2].resp.Output[0].ResponsesToolMessage.CallID)
	assert.Equal(t, schemas.ResponsesMessageTypeMessage, *billed[2].resp.Output[1].Type)
}
