package handlers

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/fasthttp/router"
	ws "github.com/fasthttp/websocket"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	bfws "github.com/maximhq/bifrost/transports/v2/bifrost-http/websocket"
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

type fakeLiveModels struct {
	allowed bool
	blocked string
}

func (f fakeLiveModels) KeySupportsModel(_ schemas.ModelProvider, _ schemas.Key, model string) bool {
	return f.allowed && model != f.blocked
}

type liveRelayFixture struct {
	runner *fakeLiveRunner
	relay  *liveWSRelay
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
	return &liveRelayFixture{runner: runner, relay: relay, app: app, openai: openai, done: done}
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

// TestLiveRelayWaitsForTheClientsCloseReply: a client that reads session.closed and the close
// frame together may fail its close reply if the socket is already gone, and some libraries then
// discard the frames read with it. The relay keeps the socket until the client has answered.
func TestLiveRelayWaitsForTheClientsCloseReply(t *testing.T) {
	t.Parallel()
	f := startLiveRelay(t, fakeLiveModels{allowed: true}, "")
	// Hold the close reply instead of answering as soon as the frame arrives.
	f.app.SetCloseHandler(func(int, string) error { return nil })

	closed := `{"type":"session.closed","reason":"close_requested","usage":{"seconds":3}}`
	sendFrame(t, f.openai, closed)
	assert.Equal(t, closed, readFrame(t, f.app))
	require.NoError(t, f.app.SetReadDeadline(time.Now().Add(5*time.Second)))
	_, _, err := f.app.ReadMessage()
	var closeErr *ws.CloseError
	require.ErrorAs(t, err, &closeErr)
	assert.Equal(t, ws.CloseNormalClosure, closeErr.Code)

	select {
	case <-f.done:
		t.Fatal("the relay dropped the connection before the client answered the close frame")
	case <-time.After(300 * time.Millisecond):
	}
	require.NoError(t, f.app.WriteControl(ws.CloseMessage, ws.FormatCloseMessage(ws.CloseNormalClosure, ""), time.Now().Add(time.Second)))
	f.waitDone(t)
}

func TestLiveRelayReleasesAClientThatNeverAnswersClose(t *testing.T) {
	t.Parallel()
	f := startLiveRelay(t, fakeLiveModels{allowed: true}, "")
	f.app.SetCloseHandler(func(int, string) error { return nil })

	sendFrame(t, f.openai, `{"type":"session.closed","reason":"close_requested","usage":{"seconds":3}}`)
	readFrame(t, f.app)
	require.NoError(t, f.app.SetReadDeadline(time.Now().Add(5*time.Second)))
	_, _, err := f.app.ReadMessage()
	var closeErr *ws.CloseError
	require.ErrorAs(t, err, &closeErr)

	started := time.Now()
	f.waitDone(t)
	assert.GreaterOrEqual(t, time.Since(started), liveCloseHandshakeGrace/2, "the handshake is given its grace before the socket is dropped")
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

// TestLiveRelayStartupErrorFailsTheSession: an error before session.started means the provider
// never created the session, so it ends failed with the provider's error and no second frame.
func TestLiveRelayStartupErrorFailsTheSession(t *testing.T) {
	t.Parallel()
	f := startLiveRelay(t, fakeLiveModels{allowed: true}, "gpt-5.6-luna")

	refused := `{"type":"error","error":{"type":"invalid_request_error","code":"unsupported_feature","message":"Stored Live sessions are not available yet.","param":"session.store"}}`
	sendFrame(t, f.openai, refused)
	assert.Equal(t, refused, readFrame(t, f.app), "the provider's error reaches the client unchanged")
	require.NoError(t, f.openai.WriteControl(ws.CloseMessage, ws.FormatCloseMessage(ws.CloseNormalClosure, ""), time.Now().Add(time.Second)))
	require.NoError(t, f.openai.UnderlyingConn().Close())
	f.waitDone(t)

	require.NoError(t, f.app.SetReadDeadline(time.Now().Add(time.Second)))
	_, extra, err := f.app.ReadMessage()
	assert.Error(t, err, "no second error frame after the provider's own: got %s", extra)

	_, posts, _ := f.runner.snapshot()
	require.Len(t, posts, 2, "the voice and backend units both close")
	for _, post := range posts {
		require.NotNil(t, post.err, "%s closes with the startup error", post.model)
		assert.Equal(t, "Stored Live sessions are not available yet.", post.err.Error.Message)
		assert.Nil(t, post.resp, "nothing ran")
	}
	assert.Equal(t, "gpt-live-1", posts[1].model, "the voice unit closes last and carries the session end")
	assert.True(t, posts[1].end)
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

// TestLiveSidebandRelayReportsHowItEnded: the sideband's row records an upstream that dropped
// while the client was attached, and nothing when the client left or the upstream closed normally.
func TestLiveSidebandRelayReportsHowItEnded(t *testing.T) {
	t.Parallel()
	start := func(t *testing.T) (*ws.Conn, *ws.Conn, chan *schemas.BifrostError) {
		t.Helper()
		bifrostSide, app, cleanup := dialRealtimeTestConn(t)
		t.Cleanup(cleanup)
		upstream, openai := dialLiveTestUpstream(t)
		relay := &liveSidebandRelay{liveUpdatePolicy: testLiveUpdatePolicy(fakeLiveModels{allowed: true}), clientConn: newRealtimeClientConn(bifrostSide), upstream: upstream}
		ended := make(chan *schemas.BifrostError, 1)
		go func() { ended <- relay.run() }()
		t.Cleanup(func() { _ = openai.UnderlyingConn().Close() })
		return app, openai, ended
	}
	wait := func(t *testing.T, ended chan *schemas.BifrostError) *schemas.BifrostError {
		t.Helper()
		select {
		case err := <-ended:
			return err
		case <-time.After(5 * time.Second):
			t.Fatal("sideband relay did not end")
			return nil
		}
	}

	t.Run("upstream drops while the client is attached", func(t *testing.T) {
		app, openai, ended := start(t)
		require.NoError(t, openai.UnderlyingConn().Close())
		assert.Contains(t, readFrame(t, app), "upstream connection ended", "the client is told")
		err := wait(t, ended)
		require.NotNil(t, err, "the row records the failure the client was told about")
		assert.Equal(t, 502, *err.StatusCode)
	})
	t.Run("client leaves", func(t *testing.T) {
		app, _, ended := start(t)
		require.NoError(t, app.Close())
		assert.Nil(t, wait(t, ended))
	})
	t.Run("upstream closes normally", func(t *testing.T) {
		_, openai, ended := start(t)
		require.NoError(t, openai.WriteControl(ws.CloseMessage, ws.FormatCloseMessage(ws.CloseNormalClosure, "session over"), time.Now().Add(time.Second)))
		assert.Nil(t, wait(t, ended))
	})
}

func TestLiveSidebandUpgradeRefusalKeepsTheHandshakeStatus(t *testing.T) {
	t.Parallel()
	assert.Equal(t, 403, *sidebandUpgradeRefusal(403).StatusCode, "a disallowed origin is refused with 403 by the upgrader")
	assert.Equal(t, 400, *sidebandUpgradeRefusal(400).StatusCode)
	assert.Equal(t, 400, *sidebandUpgradeRefusal(0).StatusCode, "an upgrade that wrote no status is a bad handshake")
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

func TestLiveRelayBackendSwitchEnforcesPinnedKeyBeforeForwarding(t *testing.T) {
	t.Parallel()
	f := startLiveRelay(t, fakeLiveModels{allowed: true}, "gpt-5.6-luna")
	f.runner.restrictKeys("gpt-5.6-terra", []string{"key-2"})
	update := `{"type":"session.update","session":{"delegation":{"type":"responses","responses":{"model":"gpt-5.6-terra"}}}}`
	sendFrame(t, f.app, update)
	assert.Contains(t, readFrame(t, f.app), "the key serving this session is not allowed for model gpt-5.6-terra")
	marker := `{"type":"session.thinking.append","delegation_id":null,"content":"still here"}`
	sendFrame(t, f.app, marker)
	assert.Equal(t, marker, readFrame(t, f.openai), "the refused update never reaches the provider and the session remains usable")

	f.runner.restrictKeys("gpt-5.6-terra", []string{"key-1"})
	sendFrame(t, f.app, update)
	assert.Equal(t, update, readFrame(t, f.openai), "the permitted update is forwarded")
	sendFrame(t, f.app, `{"type":"session.close"}`)
	assert.Equal(t, `{"type":"session.close"}`, readFrame(t, f.openai))
	sendFrame(t, f.openai, `{"type":"session.closed","usage":{"seconds":0}}`)
	readFrame(t, f.app)
	f.waitDone(t)
	opens, posts, cleanups := f.runner.snapshot()
	assert.Len(t, posts, len(opens))
	assert.Equal(t, len(opens), cleanups, "rejected and admitted units are cleaned exactly once")
}

// sidebandHookRecorder observes a sideband's request through the plugin pipeline.
type sidebandHookRecorder struct {
	refuse       *schemas.BifrostError
	preType      schemas.RequestType
	preProvider  schemas.ModelProvider
	preSession   string
	preSkipSpend bool
	postType     schemas.RequestType
	postKeyName  string
	postLatency  int64
	postErr      bool
}

func (*sidebandHookRecorder) GetName() string { return "sideband-recorder" }
func (*sidebandHookRecorder) Cleanup() error  { return nil }
func (*sidebandHookRecorder) PreRequestHook(*schemas.BifrostContext, *schemas.BifrostRequest) error {
	return nil
}
func (r *sidebandHookRecorder) PreLLMHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	r.preType = req.RequestType
	r.preProvider, _, _ = req.GetRequestFields()
	r.preSession, _ = ctx.Value(schemas.BifrostContextKeyRealtimeProviderSessionID).(string)
	r.preSkipSpend, _ = ctx.Value(schemas.BifrostContextKeySkipBudgetAndRateLimits).(bool)
	if r.refuse != nil {
		return req, &schemas.LLMPluginShortCircuit{Error: r.refuse}, nil
	}
	return req, nil, nil
}
func (r *sidebandHookRecorder) PostLLMHook(ctx *schemas.BifrostContext, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	r.postType, _, _, _ = bifrost.GetResponseFields(resp, bifrostErr)
	r.postKeyName, _ = ctx.Value(schemas.BifrostContextKeySelectedKeyName).(string)
	r.postErr = bifrostErr != nil
	if resp != nil {
		r.postLatency = resp.GetExtraFields().Latency
	}
	return resp, bifrostErr, nil
}

// TestLiveSidebandIsARequestOfItsOwn: a sideband names only a session id, yet it acts on the
// operator's key for the provider, so it runs through the plugin pipeline as a request of its own:
// governance holds it to the caller's virtual key without charging it, and its row names the
// provider session and how long it stayed attached.
func TestLiveSidebandIsARequestOfItsOwn(t *testing.T) {
	t.Parallel()
	recorder := &sidebandHookRecorder{}
	client, err := bifrost.Init(context.Background(), schemas.BifrostConfig{Account: splitKeysAccount{}, LLMPlugins: []schemas.LLMPlugin{recorder}, Logger: bifrost.NewDefaultLogger(schemas.LogLevelError)})
	require.NoError(t, err)
	defer client.Shutdown()
	g := &liveGateway{client: client}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	hooks, bifrostErr := g.admitSideband(ctx, schemas.OpenAI, "live_1")
	require.Nil(t, bifrostErr)
	require.NotNil(t, hooks)
	assert.Equal(t, schemas.LiveSidebandRequest, recorder.preType)
	assert.Equal(t, schemas.OpenAI, recorder.preProvider)
	assert.Equal(t, "live_1", recorder.preSession, "the row names the provider session it joined")
	assert.True(t, recorder.preSkipSpend, "a sideband spends nothing: budgets and request limits govern the session")
	closeSideband(ctx, hooks, schemas.OpenAI, schemas.Key{ID: "every-model", Name: "every-model"}, time.Now().Add(-1500*time.Millisecond), nil)
	assert.Equal(t, schemas.LiveSidebandRequest, recorder.postType)
	assert.Equal(t, "every-model", recorder.postKeyName, "the row names the key the sideband dialed with")
	assert.False(t, recorder.postErr)
	assert.GreaterOrEqual(t, recorder.postLatency, int64(1500), "the row carries how long the sideband stayed attached")

	// A refusal from the pipeline is the caller's answer, and the row closes with it.
	recorder.refuse = &schemas.BifrostError{StatusCode: schemas.Ptr(403), Error: &schemas.ErrorField{Message: "Provider 'openai' is not allowed"}}
	hooks, bifrostErr = g.admitSideband(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), schemas.OpenAI, "live_2")
	require.NotNil(t, bifrostErr)
	assert.Nil(t, hooks)
	assert.Equal(t, 403, *bifrostErr.StatusCode)
	assert.True(t, recorder.postErr)

	// A dial that fails after admission closes the row as an error without touching the caller's copy.
	recorder.refuse, recorder.postErr = nil, false
	ctx = schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	hooks, bifrostErr = g.admitSideband(ctx, schemas.OpenAI, "live_3")
	require.Nil(t, bifrostErr)
	dialErr := newRealtimeWireBifrostError(502, "upstream_connection_error", "dial failed")
	closeSideband(ctx, hooks, schemas.OpenAI, schemas.Key{}, time.Now(), dialErr)
	assert.True(t, recorder.postErr)
	assert.Equal(t, schemas.RequestType(""), dialErr.ExtraFields.RequestType, "the error sent to the caller is not rewritten")
}

// panickingLiveModels stands in for a model check that panics, the way a plugin hook on the
// client pump might.
type panickingLiveModels struct{}

func (panickingLiveModels) KeySupportsModel(schemas.ModelProvider, schemas.Key, string) bool {
	panic("model check exploded")
}

// The client pump runs on its own goroutine, past the recover deferred by the upgrade callback.
// A panic there ends this session, with the client told and the usage still billed, instead of
// ending the process.
func TestLiveRelayEndsSessionWhenClientPumpPanics(t *testing.T) {
	t.Parallel()
	f := startLiveRelay(t, panickingLiveModels{}, "gpt-5.6-luna")

	sendFrame(t, f.app, `{"type":"session.update","session":{"delegation":{"type":"responses","responses":{"model":"gpt-5.6-sol"}}}}`)
	errFrame := readFrame(t, f.app)
	assert.Contains(t, errFrame, `"type":"error"`)
	assert.Contains(t, errFrame, "internal error")
	assert.NotContains(t, errFrame, "exploded", "the panic value is not echoed to the client")

	assert.Equal(t, `{"type":"session.close"}`, readFrame(t, f.openai), "Bifrost ends the session itself")
	sendFrame(t, f.openai, `{"type":"session.closed","reason":"close_requested","usage":{"seconds":4}}`)
	f.waitDone(t)
	assert.Equal(t, []float64{4}, f.voiceSeconds(t), "the usage so far is still billed")
}

// The upstream pump runs on the upgrade callback's goroutine, whose recover keeps the process
// alive, but a panic there (a billing post-hook) must still end the session properly: the
// stale watcher must be released and the client told, instead of leaking a watcher per failure.
func TestLiveRelayEndsSessionWhenUpstreamPumpPanics(t *testing.T) {
	t.Parallel()
	f := startLiveRelay(t, fakeLiveModels{allowed: true}, "")

	f.runner.setPanicOnPost(true)
	sendFrame(t, f.openai, `{"type":"session.usage.updated","usage":{"seconds":31}}`)
	errFrame := readFrame(t, f.app)
	assert.Contains(t, errFrame, `"type":"error"`)
	assert.NotContains(t, errFrame, "exploded", "the panic value is not echoed to the client")
	f.waitDone(t)
	select {
	case <-f.relay.upstreamDone:
	default:
		t.Fatal("upstreamDone was not closed after the upstream pump panicked; the stale watcher is leaked")
	}

	// The panic hit the post hook of a window rotation, after the next unit was admitted. That unit
	// is still the lane's: ending the session bills and cleans it, and the session end is recorded.
	opens, posts, cleanups := f.runner.snapshot()
	assert.Equal(t, len(opens), cleanups, "every admitted unit is cleaned up, including the one opened by the rotation that panicked")
	ended := false
	for _, post := range posts {
		if post.end {
			ended = true
		}
	}
	assert.True(t, ended, "the session end is recorded on the unit the rotation admitted")
}

// Session close bills every lane, backends first and the voice lane last, since the voice unit
// carries the session's end. A backend post hook that panics there must not cost the lanes after
// it: each admitted unit is still billed and cleaned, and the session end is still recorded.
func TestLiveRelayClosesEveryLaneWhenABackendPostHookPanicsAtSessionEnd(t *testing.T) {
	t.Parallel()
	f := startLiveRelay(t, fakeLiveModels{allowed: true}, "gpt-5.6-luna")

	for _, frame := range []string{
		`{"type":"session.started","session":{"id":"live_close_panic","model":"gpt-live-1"}}`,
		`{"type":"session.usage.updated","usage":{"seconds":9}}`,
	} {
		sendFrame(t, f.openai, frame)
		readFrame(t, f.app)
	}
	f.runner.setPanicOnPost(true)
	sendFrame(t, f.openai, `{"type":"session.closed","reason":"close_requested","usage":{"seconds":12}}`)
	f.waitDone(t)

	opens, posts, cleanups := f.runner.snapshot()
	require.GreaterOrEqual(t, len(opens), 2, "the session admitted a voice and a backend unit")
	assert.Equal(t, len(opens), cleanups, "every admitted unit is cleaned up even though a backend post hook panicked")
	ended := false
	for _, post := range posts {
		if post.end {
			ended = true
		}
	}
	assert.True(t, ended, "the voice unit still records the session end")
}
