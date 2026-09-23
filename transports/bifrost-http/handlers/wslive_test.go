package handlers

import (
	"net"
	"strings"
	"testing"
	"time"

	"github.com/fasthttp/router"
	ws "github.com/fasthttp/websocket"
	"github.com/maximhq/bifrost/core/schemas"
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

func startLiveRelay(t *testing.T, models liveModelChecker, backendModel string) *liveRelayFixture {
	t.Helper()
	bifrostSide, app, cleanup := dialRealtimeTestConn(t)
	t.Cleanup(cleanup)
	upstream, openai := dialLiveTestUpstream(t)

	runner := &fakeLiveRunner{}
	meter := newTestLiveMeter(runner)
	require.Nil(t, meter.admit("gpt-live-1", backendModel))
	relay := &liveRelay{
		models:          models,
		clientConn:      newRealtimeClientConn(bifrostSide),
		upstream:        upstream,
		meter:           meter,
		provider:        schemas.OpenAI,
		key:             schemas.Key{ID: "key-1", Aliases: schemas.KeyAliases{"terra": {ModelID: "gpt-5.6-terra"}}},
		checkKeyModels:  true,
		upstreamDone:    make(chan struct{}),
		drainTimeout:    time.Second,
		staleCheckEvery: time.Hour,
	}
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
	assert.Equal(t, fakeLiveOpen{model: "terra", continuation: true}, opens[len(opens)-1], "governance admitted the new model")

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

	bifrostSide, app, cleanup := dialRealtimeTestConn(t)
	defer cleanup()
	start := `{"type":"session.start","session":{"model":"openai/gpt-live-1","delegation":{"type":"responses","responses":{"model":"gpt-5.6-luna"}}}}`
	sendFrame(t, app, start)
	frame, session, err := readLiveSessionStart(newRealtimeClientConn(bifrostSide))
	require.NoError(t, err)
	assert.Equal(t, start, string(frame))
	assert.Equal(t, "openai/gpt-live-1", session.Model)
}

func TestLiveBackendModelAndRewrite(t *testing.T) {
	t.Parallel()

	start := &schemas.LiveSession{
		Model: "openai/gpt-live-1",
		Delegation: &schemas.LiveDelegationConfig{
			Type:      schemas.LiveDelegationResponses,
			Responses: &schemas.LiveResponsesDelegation{Model: "openai/luna"},
		},
	}
	model, err := liveBackendModel(start, schemas.OpenAI)
	require.NoError(t, err)
	assert.Equal(t, "luna", model)

	key := schemas.Key{Aliases: schemas.KeyAliases{"luna": {ModelID: "gpt-5.6-luna"}}}
	frame := []byte(`{"type":"session.start","session":{"model":"openai/gpt-live-1","delegation":{"type":"responses","responses":{"model":"openai/luna","tools":[{"type":"web_search"}]}}}}`)
	rewritten, err := rewriteLiveModels(frame, start, key, "gpt-live-1", model)
	require.NoError(t, err)
	assert.Equal(t, `{"type":"session.start","session":{"model":"gpt-live-1","delegation":{"type":"responses","responses":{"model":"gpt-5.6-luna","tools":[{"type":"web_search"}]}}}}`, string(rewritten))

	// A bare, unaliased frame is sent exactly as received.
	bare := []byte(`{"type":"session.start","session":{"model":"gpt-live-1"}}`)
	unchanged, err := rewriteLiveModels(bare, &schemas.LiveSession{Model: "gpt-live-1"}, schemas.Key{}, "gpt-live-1", "")
	require.NoError(t, err)
	assert.Equal(t, string(bare), string(unchanged))

	// Client delegation has no backend on the socket.
	model, err = liveBackendModel(&schemas.LiveSession{Delegation: &schemas.LiveDelegationConfig{Type: schemas.LiveDelegationClient}}, schemas.OpenAI)
	require.NoError(t, err)
	assert.Empty(t, model)

	_, err = liveBackendModel(&schemas.LiveSession{Delegation: &schemas.LiveDelegationConfig{Type: schemas.LiveDelegationResponses, Responses: &schemas.LiveResponsesDelegation{Model: "anthropic/claude-opus-5"}}}, schemas.OpenAI)
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "must be served by openai"))
}
