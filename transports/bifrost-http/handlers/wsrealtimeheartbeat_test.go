package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/providers/azure"
	openaiProvider "github.com/maximhq/bifrost/core/providers/openai"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/kvstore"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/tidwall/gjson"

	"github.com/fasthttp/router"
	ws "github.com/fasthttp/websocket"
	bfws "github.com/maximhq/bifrost/transports/v2/bifrost-http/websocket"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// TestRealtimeAudioCommitAllowsResponse checks the socket relay admits manual and automatic responses once.
func TestRealtimeAudioCommitAllowsResponse(t *testing.T) {
	for _, provider := range []schemas.RealtimeProvider{&openaiProvider.OpenAIProvider{}, &azure.AzureProvider{}} {
		for _, mode := range []string{"manual", "automatic", "transcription"} {
			t.Run(fmt.Sprintf("%T/%s", provider, mode), func(t *testing.T) {
				gateway := newLiveOwnerTestGateway(t, nil)
				server, client, cleanup := dialRealtimeTestConn(t)
				defer cleanup()
				upstream, providerConn := dialLiveTestUpstream(t)
				session := bfws.NewSession(nil)
				defer session.Close()
				ctx := liveCallerCtx(t, "")
				handler := &WSRealtimeHandler{client: gateway.client}
				conn := newRealtimeClientConn(server)
				transcription := mode == "transcription"
				done := make(chan error, 1)
				go func() {
					done <- handler.relayRealtimeProviderToClient(conn, session, upstream, provider, ctx, schemas.OpenAI, "gpt-realtime-mini", schemas.Key{}, transcription)
				}()
				defer func() {
					_ = upstream.Close()
					select {
					case <-done:
					case <-time.After(time.Second):
						t.Error("provider relay did not terminate")
					}
				}()
				require.NoError(t, client.SetReadDeadline(time.Now().Add(3*time.Second)))
				require.NoError(t, providerConn.WriteMessage(ws.TextMessage, []byte(`{"type":"input_audio_buffer.committed","item_id":"audio"}`)))
				_, raw, err := client.ReadMessage()
				require.NoError(t, err)
				require.Contains(t, string(raw), "input_audio_buffer.committed")
				if transcription {
					require.NotNil(t, session.PeekRealtimeTurnHooks())
					require.NoError(t, providerConn.WriteMessage(ws.TextMessage, []byte(`{"type":"conversation.item.input_audio_transcription.completed","item_id":"audio","transcript":"hello"}`)))
					_, raw, err = client.ReadMessage()
					require.NoError(t, err)
					require.Contains(t, string(raw), "input_audio_transcription.completed")
					require.Nil(t, session.PeekRealtimeTurnHooks())
					return
				}
				require.Nil(t, session.PeekRealtimeTurnHooks())
				if mode == "manual" {
					stop, err := handler.processRealtimeClientMessage(conn, session, upstream, provider, ctx, schemas.OpenAI, "gpt-realtime-mini", schemas.Key{}, false, ws.TextMessage, []byte(`{"type":"response.create"}`))
					require.NoError(t, err)
					require.False(t, stop)
					require.NotNil(t, session.PeekRealtimeTurnHooks())
					require.NoError(t, providerConn.SetReadDeadline(time.Now().Add(time.Second)))
					_, raw, err = providerConn.ReadMessage()
					require.NoError(t, err)
					require.Contains(t, string(raw), "response.create")
				}
				started := session.PeekRealtimeTurnHooks()
				require.NoError(t, providerConn.WriteMessage(ws.TextMessage, []byte(`{"type":"response.created","response":{"id":"response"}}`)))
				_, raw, err = client.ReadMessage()
				require.NoError(t, err)
				require.Contains(t, string(raw), "response.created")
				require.NotNil(t, session.PeekRealtimeTurnHooks())
				if started != nil {
					require.Same(t, started, session.PeekRealtimeTurnHooks())
				}
				require.NoError(t, providerConn.WriteMessage(ws.TextMessage, []byte(`{"type":"response.done","response":{"id":"response","status":"completed"}}`)))
				_, raw, err = client.ReadMessage()
				require.NoError(t, err)
				require.Contains(t, string(raw), "response.done")
				require.Nil(t, session.PeekRealtimeTurnHooks(), "completion must release the pipeline")
			})
		}
	}
}

// TestRealtimeProviderErrorsRemainRecoverable preserves native errors and lets the provider continue.
func TestRealtimeProviderErrorsRemainRecoverable(t *testing.T) {
	for _, code := range []string{"invalid_value", "response_cancel_not_active", "rate_limit_exceeded", "server_error"} {
		t.Run(code, func(t *testing.T) {
			server, client, cleanup := dialRealtimeTestConn(t)
			defer cleanup()
			upstream, providerConn := dialLiveTestUpstream(t)
			session := bfws.NewSession(nil)
			calls, cleanups := 0, 0
			session.SetRealtimeTurnHooks(&bfws.RealtimeTurnPluginState{
				RequestID: "turn", StartedAt: time.Now(),
				PostHookRunner: func(_ *schemas.BifrostContext, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
					calls++
					return result, err
				},
				Cleanup: func() { cleanups++ },
			})
			done := make(chan error, 1)
			go func() {
				done <- (&WSRealtimeHandler{}).relayRealtimeProviderToClient(newRealtimeClientConn(server), session, upstream, &openaiProvider.OpenAIProvider{}, nil, schemas.OpenAI, "realtime", schemas.Key{}, false)
			}()
			defer func() {
				_ = upstream.Close()
				select {
				case <-done:
				case <-time.After(time.Second):
					t.Error("provider relay did not terminate")
				}
				// Hook callbacks run on the relay goroutine.
				require.Equal(t, 1, calls)
				require.Equal(t, 1, cleanups)
			}()
			raw := []byte(fmt.Sprintf(`{"type":"error","event_id":"server_event","error":{"type":"invalid_request_error","code":%q,"message":"recoverable","param":"response.output_modalities","event_id":"client_event","extra":{"keep":true}}}`, code))
			require.NoError(t, providerConn.WriteMessage(ws.TextMessage, raw))
			require.NoError(t, client.SetReadDeadline(time.Now().Add(time.Second)))
			_, actual, err := client.ReadMessage()
			require.NoError(t, err)
			require.Equal(t, string(raw), string(actual))
			if code == "response_cancel_not_active" {
				// A rejected cancel must not consume the current or pending turn.
				require.NotNil(t, session.PeekRealtimeTurnHooks())
			}
			followup := []byte(`{"type":"response.done","response":{"id":"next","status":"completed"}}`)
			require.NoError(t, providerConn.WriteMessage(ws.TextMessage, followup))
			_, actual, err = client.ReadMessage()
			require.NoError(t, err, "recoverable provider error must not end the relay")
			require.Equal(t, string(followup), string(actual))
		})
	}
}

// TestRealtimeProviderClosePreservesCode forwards provider close frames without a synthetic error.
func TestRealtimeProviderClosePreservesCode(t *testing.T) {
	for _, code := range []int{ws.CloseNormalClosure, ws.CloseGoingAway, ws.ClosePolicyViolation, ws.CloseInternalServerErr, 4004} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			server, client, cleanup := dialRealtimeTestConn(t)
			defer cleanup()
			upstream, providerConn := dialLiveTestUpstream(t)
			done := make(chan error, 1)
			go func() {
				done <- (&WSRealtimeHandler{}).relayRealtimeProviderToClient(newRealtimeClientConn(server), bfws.NewSession(nil), upstream, &openaiProvider.OpenAIProvider{}, nil, schemas.OpenAI, "realtime", schemas.Key{}, false)
			}()
			reason := "invalid_request_error.model_not_found"
			require.NoError(t, providerConn.WriteControl(ws.CloseMessage, ws.FormatCloseMessage(code, reason), time.Now().Add(time.Second)))
			require.NoError(t, client.SetReadDeadline(time.Now().Add(time.Second)))
			_, data, err := client.ReadMessage()
			require.True(t, ws.IsCloseError(err, code), "got data %s and error %v", data, err)
			var closeErr *ws.CloseError
			require.ErrorAs(t, err, &closeErr)
			require.Equal(t, reason, closeErr.Text)
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("close must end the provider relay")
			}
		})
	}
}

// TestRealtimeProviderErrorBeforeAbruptClose avoids replacing an already delivered provider error.
func TestRealtimeProviderErrorBeforeAbruptClose(t *testing.T) {
	server, client, cleanup := dialRealtimeTestConn(t)
	defer cleanup()
	upstream, providerConn := dialLiveTestUpstream(t)
	done := make(chan error, 1)
	go func() {
		done <- (&WSRealtimeHandler{}).relayRealtimeProviderToClient(newRealtimeClientConn(server), bfws.NewSession(nil), upstream, &openaiProvider.OpenAIProvider{}, nil, schemas.OpenAI, "realtime", schemas.Key{}, false)
	}()
	raw := []byte(`{"type":"error","error":{"type":"server_error","code":"upstream_detail","message":"original failure"}}`)
	require.NoError(t, providerConn.WriteMessage(ws.TextMessage, raw))
	require.NoError(t, client.SetReadDeadline(time.Now().Add(time.Second)))
	_, actual, err := client.ReadMessage()
	require.NoError(t, err)
	require.Equal(t, string(raw), string(actual))
	require.NoError(t, providerConn.Close())
	_, actual, err = client.ReadMessage()
	require.True(t, ws.IsCloseError(err, ws.CloseInternalServerErr), "unexpected replacement event %s, error %v", actual, err)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("abrupt provider closure did not terminate relay")
	}
}

// TestRealtimeTurnMetadataIsolation checks setup snapshots and unit contexts for
// concurrent connections, including the separate pre-request context used by Live.
func TestRealtimeTurnMetadataIsolation(t *testing.T) {
	type metadataKey uint8
	const userKey metadataKey = 0
	const requestOnlyKey metadataKey = 1
	for i := range 12 {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			t.Parallel()
			userID := fmt.Sprintf("user-%d", i)
			request, cancel := schemas.NewBifrostContextWithCancel(context.Background())
			transport := &fasthttp.RequestCtx{}
			transport.SetUserValue(lib.FastHTTPUserValueBifrostContext, request)
			name := "custom-plugin"
			scoped := request.WithPluginScope(&name)
			scoped.SetValueForRealtimeTurns(userKey, userID)
			scoped.SetValue(requestOnlyKey, "request-only")
			scoped.SetValueForRealtimeTurns(schemas.BifrostContextKeyTraceID, "dead-trace")
			scoped.SetValueForRealtimeTurns(schemas.BifrostContextKeyExportTraceID, "dead-export")
			scoped.ReleasePluginScope()
			values := snapshotRealtimeMiddlewareValues(transport)
			preReq := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			preReq.SetValueForRealtimeTurns(metadataKey(2), "pre-request")
			withContext := snapshotRealtimeMiddlewareValuesWithContext(transport, preReq)
			liveValues := liveMiddlewareValues(values, preReq)
			cancel()
			transport.ResetUserValues()
			request.SetValueForRealtimeTurns(userKey, "after-snapshot")
			for _, snapshot := range []map[any]any{values, withContext, liveValues} {
				session := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
				applyRealtimeMiddlewareValues(session, snapshot)
				for turn := range 2 {
					unit := newRealtimeTurnContext(session, fmt.Sprint(turn), "session", "", "", "", nil)
					live := newLiveMeter(nil, session, schemas.OpenAI, schemas.Key{}, "session")
					liveUnit := live.unitContext(fmt.Sprint(turn), false, "voice")
					backendUnit := live.unitContext(fmt.Sprint(turn), false, "backend")
					for _, ctx := range []*schemas.BifrostContext{unit, liveUnit, backendUnit} {
						if ctx.Value(userKey) != userID || ctx.Value(requestOnlyKey) != nil {
							t.Fatalf("unit metadata = %v, request-only = %v", ctx.Value(userKey), ctx.Value(requestOnlyKey))
						}
						if ctx.Value(schemas.BifrostContextKeyTraceID) != nil || ctx.Value(schemas.BifrostContextKeyExportTraceID) != nil {
							t.Fatal("unit inherited a dead upgrade trace")
						}
						ctx.SetValue(userKey, "unit-local")
					}
				}
				if session.Value(userKey) != userID {
					t.Fatal("unit mutated the session's metadata")
				}
			}
			if withContext[metadataKey(2)] != "pre-request" || liveValues[metadataKey(2)] != "pre-request" {
				t.Fatal("separate pre-request context lost its opted-in metadata")
			}
			request.DrainPluginLogs()
		})
	}
}

// TestRealtimeTurnMetadataPreservesAuth verifies setup values cannot replace resolved auth.
func TestRealtimeTurnMetadataPreservesAuth(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyVirtualKey, "resolved")
	applyRealtimeMiddlewareValues(ctx, map[any]any{schemas.BifrostContextKeyVirtualKey: "stale"})
	if ctx.Value(schemas.BifrostContextKeyVirtualKey) != "resolved" {
		t.Fatal("metadata replaced the resolved credential")
	}
}

// BenchmarkRealtimeMetadataHandoff measures the middleware and session snapshots
// together, excluding existing context allocation and per-turn processing.
func BenchmarkRealtimeMetadataHandoff(b *testing.B) {
	type metadataKey uint8
	for _, marked := range []bool{false, true} {
		b.Run(fmt.Sprint(marked), func(b *testing.B) {
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			if marked {
				ctx.SetValueForRealtimeTurns(metadataKey(0), "test-alice")
			}
			transport := &fasthttp.RequestCtx{}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				transport.ResetUserValues()
				retainRealtimeTurnValues(transport, ctx)
				_ = snapshotRealtimeMiddlewareValues(transport)
			}
		})
	}
}

// dialRealtimeTestConn returns the server side of a live websocket connection.
//
// The upgrade handler is held open until cleanup runs, so the connection stays
// valid for the duration of the test.
func dialRealtimeTestConn(t testing.TB) (*ws.Conn, *ws.Conn, func()) {
	t.Helper()

	upgrader := ws.FastHTTPUpgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
		CheckOrigin:     func(*fasthttp.RequestCtx) bool { return true },
	}

	serverConns := make(chan *ws.Conn, 1)
	release := make(chan struct{})
	released := make(chan struct{})

	r := router.New()
	r.GET("/realtime", func(ctx *fasthttp.RequestCtx) {
		_ = upgrader.Upgrade(ctx, func(conn *ws.Conn) {
			serverConns <- conn
			<-release
			close(released)
		})
	})

	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &fasthttp.Server{Handler: r.Handler}
	go func() { _ = srv.Serve(ln) }()

	client, _, err := ws.DefaultDialer.Dial("ws://"+ln.Addr().String()+"/realtime", nil)
	if err != nil {
		_ = srv.Shutdown()
		t.Fatalf("dial: %v", err)
	}

	var serverConn *ws.Conn
	select {
	case serverConn = <-serverConns:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the server side of the connection")
	}

	return serverConn, client, func() {
		close(release)
		<-released
		_ = client.Close()
		_ = srv.Shutdown()
	}
}

func TestDiscoverRealtimeTranscriptionModel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		message string
		want    string
	}{
		{
			name:    "nested transcription model",
			message: `{"type":"session.update","session":{"audio":{"input":{"transcription":{"model":" openai/gpt-4o-transcribe "}}}}}`,
			want:    "openai/gpt-4o-transcribe",
		},
		{name: "wrong event type", message: `{"type":"input_audio_buffer.append","session":{"audio":{"input":{"transcription":{"model":"openai/gpt-4o-transcribe"}}}}}`},
		{name: "top-level model", message: `{"type":"session.update","model":"openai/gpt-4o-transcribe"}`},
		{name: "invalid JSON", message: `{`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := discoverRealtimeTranscriptionModel([]byte(tt.message)); got != tt.want {
				t.Fatalf("discoverRealtimeTranscriptionModel() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPinRealtimeTranscriptionModel(t *testing.T) {
	t.Parallel()

	provider := &openaiProvider.OpenAIProvider{}
	tests := []struct {
		name                 string
		transcriptionSession bool
		message              string
		wantModel            string
	}{
		{
			name:                 "transcription session uses pinned alias-resolved model",
			transcriptionSession: true,
			message:              `{"type":"session.update","session":{"type":"transcription","audio":{"input":{"transcription":{"model":"openai/transcription-alias","language":"en"}}}}}`,
			wantModel:            "gpt-4o-transcribe",
		},
		{
			name:                 "normal realtime keeps input transcription model",
			transcriptionSession: false,
			message:              `{"type":"session.update","session":{"type":"realtime","model":"gpt-realtime","audio":{"input":{"transcription":{"model":"openai/whisper-1","language":"en"}}}}}`,
			wantModel:            "whisper-1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			event, err := schemas.ParseRealtimeEvent([]byte(tt.message))
			if err != nil {
				t.Fatalf("ParseRealtimeEvent() error = %v", err)
			}
			if err := pinRealtimeTranscriptionModel(event, "gpt-4o-transcribe", tt.transcriptionSession); err != nil {
				t.Fatalf("pinRealtimeTranscriptionModel() error = %v", err)
			}
			sanitizeRealtimeSessionEventForProvider(event)
			serialized, err := provider.ToProviderRealtimeEvent(event)
			if err != nil {
				t.Fatalf("ToProviderRealtimeEvent() error = %v", err)
			}

			var payload struct {
				Session struct {
					Audio struct {
						Input struct {
							Transcription struct {
								Model    string `json:"model"`
								Language string `json:"language"`
							} `json:"transcription"`
						} `json:"input"`
					} `json:"audio"`
				} `json:"session"`
			}
			if err := json.Unmarshal(serialized, &payload); err != nil {
				t.Fatalf("json.Unmarshal() error = %v", err)
			}
			if payload.Session.Audio.Input.Transcription.Model != tt.wantModel {
				t.Fatalf("transcription model = %q, want %q", payload.Session.Audio.Input.Transcription.Model, tt.wantModel)
			}
			if payload.Session.Audio.Input.Transcription.Language != "en" {
				t.Fatalf("transcription language = %q, want en", payload.Session.Audio.Input.Transcription.Language)
			}
		})
	}
}

func TestSnapshotRealtimeMiddlewareValuesWithContext(t *testing.T) {
	t.Parallel()

	ctx := &fasthttp.RequestCtx{}
	ctx.SetUserValue(schemas.BifrostContextKeyGovernanceVirtualKeyID, "transport-virtual-key-id")
	ctx.SetUserValue(schemas.BifrostContextKeyTraceID, "dead-upgrade-trace")

	bifrostCtx := schemas.NewBifrostContext(t.Context(), schemas.NoDeadline)
	bifrostCtx.SetValue(schemas.BifrostContextKeySelectedKeyID, "selected-key")

	values := snapshotRealtimeMiddlewareValuesWithContext(ctx, bifrostCtx)
	if got := values[schemas.BifrostContextKeyGovernanceVirtualKeyID]; got != "transport-virtual-key-id" {
		t.Fatalf("governance virtual key ID = %v, want transport-virtual-key-id", got)
	}
	if got := values[schemas.BifrostContextKeySelectedKeyID]; got != "selected-key" {
		t.Fatalf("selected key ID = %v, want selected-key", got)
	}
	if _, ok := values[schemas.BifrostContextKeyTraceID]; ok {
		t.Fatal("upgrade trace ID must not be inherited by realtime turns")
	}
}

func TestBufferRealtimeTranscriptionBootstrapPreservesFrames(t *testing.T) {
	serverConn, peerConn, cleanup := dialRealtimeTestConn(t)
	defer cleanup()

	client := newRealtimeClientConn(serverConn)
	frames := []realtimeWebSocketFrame{
		{messageType: ws.BinaryMessage, data: []byte{0, 1, 2, 3}},
		{messageType: ws.TextMessage, data: []byte(`{"type":"input_audio_buffer.append","audio":"AQID"}`)},
		{messageType: ws.TextMessage, data: []byte(`{"type":"session.update","session":{"audio":{"input":{"transcription":{"model":"openai/gpt-4o-transcribe"}}}}}`)},
	}

	writeErr := make(chan error, 1)
	go func() {
		for _, frame := range frames {
			if err := peerConn.WriteMessage(frame.messageType, frame.data); err != nil {
				writeErr <- err
				return
			}
		}
		writeErr <- nil
	}()

	buffered, model, err := bufferRealtimeTranscriptionBootstrap(client)
	if err != nil {
		t.Fatalf("bufferRealtimeTranscriptionBootstrap() error = %v", err)
	}
	if err := <-writeErr; err != nil {
		t.Fatalf("write bootstrap frames: %v", err)
	}
	if model != "openai/gpt-4o-transcribe" {
		t.Fatalf("model = %q, want %q", model, "openai/gpt-4o-transcribe")
	}
	if len(buffered) != len(frames) {
		t.Fatalf("buffered frame count = %d, want %d", len(buffered), len(frames))
	}
	for i := range frames {
		if buffered[i].messageType != frames[i].messageType || string(buffered[i].data) != string(frames[i].data) {
			t.Fatalf("buffered[%d] = (%d, %q), want (%d, %q)", i, buffered[i].messageType, buffered[i].data, frames[i].messageType, frames[i].data)
		}
	}
}

// TestRealtimeStopHeartbeatWaitsForPingGoroutine pins the invariant that makes
// the realtime upgrade handler safe to return from: once stopHeartbeat returns,
// no goroutine can still be writing to the client connection.
//
// fasthttp nils out the hijacked connection's net.Conn the moment the handler
// returns, so a ping still in flight at that point dereferences nil. There is no
// recover on the heartbeat goroutine, so that panic is fatal to the process.
func TestRealtimeStopHeartbeatWaitsForPingGoroutine(t *testing.T) {
	serverConn, _, cleanup := dialRealtimeTestConn(t)
	defer cleanup()

	client := newRealtimeClientConn(serverConn)
	client.pingInterval = time.Millisecond
	client.startHeartbeat()

	// Let the heartbeat tick a few times so it is genuinely running.
	time.Sleep(50 * time.Millisecond)

	client.stopHeartbeat()

	select {
	case <-client.heartbeatDone:
	default:
		t.Fatal("stopHeartbeat returned while the ping goroutine was still running")
	}
}

// TestRealtimeStopHeartbeatWithoutStart covers the early-return paths that fail
// before the heartbeat is ever started.
func TestRealtimeStopHeartbeatWithoutStart(t *testing.T) {
	serverConn, _, cleanup := dialRealtimeTestConn(t)
	defer cleanup()

	client := newRealtimeClientConn(serverConn)

	done := make(chan struct{})
	go func() {
		defer close(done)
		client.stopHeartbeat()
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("stopHeartbeat blocked when the heartbeat had never been started")
	}
}

// TestRealtimeStopHeartbeatIsIdempotent guards the deferred-stop paths, which
// can run more than once as a session unwinds.
func TestRealtimeStopHeartbeatIsIdempotent(t *testing.T) {
	serverConn, _, cleanup := dialRealtimeTestConn(t)
	defer cleanup()

	client := newRealtimeClientConn(serverConn)
	client.pingInterval = time.Millisecond
	client.startHeartbeat()
	time.Sleep(20 * time.Millisecond)

	client.stopHeartbeat()
	client.stopHeartbeat()
}

// fasthttp ignores Close on a hijacked connection, so when the upstream ends the relay reading the
// client stays blocked; the teardown must release it and tell the client the session is over.
func TestRunRealtimeRelaysReleasesTheClientWhenTheUpstreamEnds(t *testing.T) {
	for _, tc := range []struct {
		name         string
		clientAnswer bool // the client reads, so its library answers the close frame
	}{
		{name: "client answers the close", clientAnswer: true},
		{name: "silent client", clientAnswer: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			serverConn, client, cleanup := dialRealtimeTestConn(t)
			defer cleanup()
			clientConn := newRealtimeClientConn(serverConn)

			toClient := func() error { return nil } // the upstream ended
			toProvider := func() error {
				_, _, err := clientConn.ReadMessage()
				return err
			}
			closeBoth := func() { _ = clientConn.Close() }

			clientClose := make(chan error, 1)
			if tc.clientAnswer {
				go func() {
					_, _, err := client.ReadMessage()
					clientClose <- err
				}()
			}
			result := make(chan error, 1)
			go func() { result <- runRealtimeRelays(serverConn, toProvider, toClient, closeBoth) }()

			select {
			case err := <-result:
				if err != nil {
					t.Fatalf("a clean shutdown reported a relay error: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("runRealtimeRelays stayed blocked on the client after the upstream ended")
			}
			if tc.clientAnswer {
				select {
				case err := <-clientClose:
					if !ws.IsCloseError(err, ws.CloseNormalClosure) {
						t.Fatalf("expected the client to receive a normal close frame, got %v", err)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("the client never received a close frame")
				}
			}
		})
	}
}

// After the upstream ends, a client relay that fails on its own before its read is forced to return
// (a panic in a hook, say) still ends the session with that error.
func TestRunRealtimeRelaysReportsAClientFailureAfterTheUpstreamEnds(t *testing.T) {
	serverConn, _, cleanup := dialRealtimeTestConn(t)
	defer cleanup()
	clientConn := newRealtimeClientConn(serverConn)

	tornDown := make(chan struct{})
	toClient := func() error { return nil } // the upstream ended
	toProvider := func() error {
		<-tornDown
		panic("hook exploded during teardown")
	}
	closeBoth := func() {
		_ = clientConn.Close()
		close(tornDown)
	}

	err := runRealtimeRelays(serverConn, toProvider, toClient, closeBoth)

	if err == nil || !strings.Contains(err.Error(), "panic") {
		t.Fatalf("expected the client relay's panic to be reported, got %v", err)
	}
}

// The forced read deadline only releases the relay reading the client; a timeout from the relay
// writing to it during teardown is a failed delivery and is reported.
func TestRunRealtimeRelaysReportsAClientWriteTimeoutDuringTeardown(t *testing.T) {
	serverConn, _, cleanup := dialRealtimeTestConn(t)
	defer cleanup()
	clientConn := newRealtimeClientConn(serverConn)

	tornDown := make(chan struct{})
	toProvider := func() error { return nil } // the client side ended first
	toClient := func() error {
		<-tornDown
		time.Sleep(realtimeCloseHandshakeGrace + 200*time.Millisecond) // still writing when the deadline is forced
		return &net.OpError{Op: "write", Net: "tcp", Err: os.ErrDeadlineExceeded}
	}
	closeBoth := func() {
		_ = clientConn.Close()
		close(tornDown)
	}

	err := runRealtimeRelays(serverConn, toProvider, toClient, closeBoth)

	var netErr net.Error
	if !errors.As(err, &netErr) || !netErr.Timeout() {
		t.Fatalf("expected the client write timeout to be reported, got %v", err)
	}
}

// Only the timeout the teardown sets itself is expected; a client read that timed out first still
// ended the session and is reported.
func TestRunRealtimeRelaysReportsAClientReadTimeoutBeforeTeardown(t *testing.T) {
	serverConn, _, cleanup := dialRealtimeTestConn(t)
	defer cleanup()
	clientConn := newRealtimeClientConn(serverConn)

	upstreamClosed := make(chan struct{})
	toProvider := func() error {
		_ = serverConn.SetReadDeadline(time.Now().Add(-time.Second))
		_, _, err := clientConn.ReadMessage()
		return err
	}
	toClient := func() error {
		<-upstreamClosed
		return nil
	}
	closeBoth := func() {
		_ = clientConn.Close()
		close(upstreamClosed)
	}

	err := runRealtimeRelays(serverConn, toProvider, toClient, closeBoth)

	var netErr net.Error
	if !errors.As(err, &netErr) || !netErr.Timeout() {
		t.Fatalf("expected the client read timeout to be reported, got %v", err)
	}
}

// A relay runs on its own goroutine, past the recover deferred by the upgrade callback, so a
// panic inside it (a plugin hook on the turn pipeline) must be caught by the relay runner
// itself: it ends the session like any other relay error instead of ending the process.
func TestRunRealtimeRelaysEndsSessionWhenARelayPanics(t *testing.T) {
	closed := make(chan struct{})
	var closes int
	toProvider := func() error {
		panic("plugin hook exploded")
	}
	toClient := func() error {
		<-closed // a blocking read, released by the teardown
		return net.ErrClosed
	}
	closeBoth := func() {
		closes++
		close(closed)
	}

	result := make(chan error, 1)
	go func() { result <- runRealtimeRelays(nil, toProvider, toClient, closeBoth) }()
	select {
	case err := <-result:
		if err == nil || !strings.Contains(err.Error(), "panic") {
			t.Fatalf("expected the panic to end the session as a relay error, got %v", err)
		}
		if strings.Contains(err.Error(), "exploded") {
			t.Fatalf("the panic value must not be echoed into the relay error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runRealtimeRelays did not return after a relay panicked")
	}
	if closes != 1 {
		t.Fatalf("expected both connections to be closed once, closeBoth ran %d times", closes)
	}
}

// scriptedRealtimeTranslator is a RealtimeTranslator whose steps are scripted by the test.
type scriptedRealtimeTranslator struct {
	open       schemas.RealtimeTranslation
	toProvider func(*schemas.BifrostRealtimeEvent) (schemas.RealtimeTranslation, error)
	toClient   func(json.RawMessage) (schemas.RealtimeTranslation, error)
	onClose    func(int, string) schemas.RealtimeTranslation
}

func (s *scriptedRealtimeTranslator) Open() schemas.RealtimeTranslation { return s.open }

func (s *scriptedRealtimeTranslator) Close(code int, reason string) schemas.RealtimeTranslation {
	if s.onClose == nil {
		return schemas.RealtimeTranslation{}
	}
	return s.onClose(code, reason)
}

func (s *scriptedRealtimeTranslator) ToProvider(event *schemas.BifrostRealtimeEvent) (schemas.RealtimeTranslation, error) {
	return s.toProvider(event)
}

func (s *scriptedRealtimeTranslator) ToClient(frame json.RawMessage) (schemas.RealtimeTranslation, error) {
	return s.toClient(frame)
}

// translatingRealtimeProvider is an OpenAI-shaped provider that also asks for a translator.
type translatingRealtimeProvider struct {
	*openaiProvider.OpenAIProvider
	translator schemas.RealtimeTranslator
	err        *schemas.BifrostError
}

func (p *translatingRealtimeProvider) NewRealtimeTranslator(schemas.Key, string) (schemas.RealtimeTranslator, *schemas.BifrostError) {
	return p.translator, p.err
}

func readRealtimeTestFrame(t *testing.T, conn *ws.Conn) string {
	t.Helper()
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(3*time.Second)))
	_, raw, err := conn.ReadMessage()
	require.NoError(t, err)
	return string(raw)
}

// TestRealtimeClientProtocolAndTranslatorFactory pins when a connection is translated: the client then
// speaks OpenAI Realtime whatever the provider is, and a provider that asks for a translator must give one.
func TestRealtimeClientProtocolAndTranslatorFactory(t *testing.T) {
	plain := &azure.AzureProvider{}
	require.Same(t, plain, realtimeClientProtocol(plain))
	translator, bifrostErr := newRealtimeTranslator(plain, schemas.Key{}, "model")
	require.Nil(t, translator)
	require.Nil(t, bifrostErr)

	scripted := &scriptedRealtimeTranslator{}
	translating := &translatingRealtimeProvider{OpenAIProvider: &openaiProvider.OpenAIProvider{}, translator: scripted}
	require.Same(t, openAIRealtimeWire, realtimeClientProtocol(translating))
	require.Equal(t, "realtime", realtimeClientProtocol(translating).RealtimeWebSocketSubprotocol())
	translator, bifrostErr = newRealtimeTranslator(translating, schemas.Key{}, "model")
	require.Nil(t, bifrostErr)
	require.Same(t, scripted, translator)

	translating.translator = nil
	_, bifrostErr = newRealtimeTranslator(translating, schemas.Key{}, "model")
	require.NotNil(t, bifrostErr, "a provider that asks for a translator and returns none must fail the connection, not fall back to relaying its native frames")

	translating.err = newRealtimeWireBifrostError(400, "invalid_request_error", "unsupported model")
	_, bifrostErr = newRealtimeTranslator(translating, schemas.Key{}, "model")
	require.Same(t, translating.err, bifrostErr)
}

// TestRealtimeTranslatorOpensBeforeEitherSideSpeaks checks Open's frames go out first: provider frames
// upstream, client events through the turn pipeline.
func TestRealtimeTranslatorOpensBeforeEitherSideSpeaks(t *testing.T) {
	gateway := newLiveOwnerTestGateway(t, nil)
	server, client, cleanup := dialRealtimeTestConn(t)
	defer cleanup()
	upstream, providerConn := dialLiveTestUpstream(t)
	session := bfws.NewSession(nil)
	defer session.Close()
	translator := &scriptedRealtimeTranslator{open: schemas.RealtimeTranslation{
		Provider: []json.RawMessage{json.RawMessage(`{"setup":true}`)},
		Client:   []json.RawMessage{json.RawMessage(`{"type":"session.created","session":{"id":"session"}}`)},
	}}
	handler := &WSRealtimeHandler{client: gateway.client, config: gateway.config}

	require.NoError(t, handler.openRealtimeTranslator(newRealtimeClientConn(server), session, upstream, translator, openAIRealtimeWire, liveCallerCtx(t, ""), schemas.OpenAI, "model", schemas.Key{}, false))

	require.Contains(t, readRealtimeTestFrame(t, client), "session.created")
	require.Equal(t, `{"setup":true}`, readRealtimeTestFrame(t, providerConn))
	require.Equal(t, "session", session.ProviderSessionID())
}

// TestRealtimeTranslatorRelaysProviderFrames checks a provider frame, text or binary, reaches the client as
// the OpenAI event the translator made of it, through the turn pipeline, and that the provider frames the
// translator asked for are written upstream.
func TestRealtimeTranslatorRelaysProviderFrames(t *testing.T) {
	gateway := newLiveOwnerTestGateway(t, nil)
	server, client, cleanup := dialRealtimeTestConn(t)
	defer cleanup()
	upstream, providerConn := dialLiveTestUpstream(t)
	session := bfws.NewSession(nil)
	defer session.Close()
	seen := make(chan string, 2)
	step := 0
	session.SetRealtimeTranslator(&scriptedRealtimeTranslator{
		toClient: func(frame json.RawMessage) (schemas.RealtimeTranslation, error) {
			seen <- string(frame)
			if step++; step == 1 {
				return schemas.RealtimeTranslation{
					Provider: []json.RawMessage{json.RawMessage(`{"ack":1}`)},
					Client:   []json.RawMessage{json.RawMessage(`{"type":"response.created","response":{"id":"response"}}`)},
				}, nil
			}
			return schemas.RealtimeTranslation{
				Client: []json.RawMessage{json.RawMessage(`{"type":"response.done","response":{"id":"response","status":"completed"}}`)},
			}, nil
		},
	})
	handler := &WSRealtimeHandler{client: gateway.client, config: gateway.config}
	done := make(chan error, 1)
	go func() {
		done <- handler.relayRealtimeProviderToClient(newRealtimeClientConn(server), session, upstream, openAIRealtimeWire, liveCallerCtx(t, ""), schemas.OpenAI, "gpt-realtime-mini", schemas.Key{}, false)
	}()
	defer func() {
		_ = upstream.Close()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("provider relay did not terminate")
		}
	}()

	require.NoError(t, providerConn.WriteMessage(ws.BinaryMessage, []byte("native-created")))
	require.Equal(t, "native-created", <-seen, "the translator must see the provider's frame as it arrived")
	require.Contains(t, readRealtimeTestFrame(t, client), "response.created")
	require.NotNil(t, session.PeekRealtimeTurnHooks(), "a translated response.created must open the turn like a provider's would")
	require.Equal(t, `{"ack":1}`, readRealtimeTestFrame(t, providerConn))

	require.NoError(t, providerConn.WriteMessage(ws.TextMessage, []byte("native-done")))
	require.Equal(t, "native-done", <-seen)
	require.Contains(t, readRealtimeTestFrame(t, client), "response.done")
	require.Nil(t, session.PeekRealtimeTurnHooks(), "a translated response.done must release the turn")
}

// TestRealtimeTranslatorRelaysClientEvents checks one client event can become several provider frames in
// order plus an immediate reply to the client, and that a translation error is reported to the client
// without writing anything upstream or ending the connection.
func TestRealtimeTranslatorRelaysClientEvents(t *testing.T) {
	gateway := newLiveOwnerTestGateway(t, nil)
	server, client, cleanup := dialRealtimeTestConn(t)
	defer cleanup()
	upstream, providerConn := dialLiveTestUpstream(t)
	session := bfws.NewSession(nil)
	defer session.Close()
	session.SetRealtimeTranslator(&scriptedRealtimeTranslator{
		toProvider: func(event *schemas.BifrostRealtimeEvent) (schemas.RealtimeTranslation, error) {
			if event.Type != schemas.RTEventConversationItemCreate {
				return schemas.RealtimeTranslation{}, errors.New("session.update is not supported once the session has started")
			}
			return schemas.RealtimeTranslation{
				Provider: []json.RawMessage{json.RawMessage(`{"first":1}`), json.RawMessage(`{"second":2}`)},
				Client:   []json.RawMessage{json.RawMessage(`{"type":"conversation.item.created","item":{"id":"item","type":"message","role":"user"}}`)},
			}, nil
		},
	})
	handler := &WSRealtimeHandler{client: gateway.client, config: gateway.config}
	conn := newRealtimeClientConn(server)
	ctx := liveCallerCtx(t, "")
	send := func(message string) (bool, error) {
		return handler.processRealtimeClientMessage(conn, session, upstream, openAIRealtimeWire, ctx, schemas.OpenAI, "model", schemas.Key{}, false, ws.TextMessage, []byte(message))
	}

	stop, err := send(`{"type":"conversation.item.create","item":{"id":"item","type":"message","role":"user"}}`)
	require.NoError(t, err)
	require.False(t, stop)
	require.Contains(t, readRealtimeTestFrame(t, client), "conversation.item.created")
	require.Equal(t, `{"first":1}`, readRealtimeTestFrame(t, providerConn))
	require.Equal(t, `{"second":2}`, readRealtimeTestFrame(t, providerConn))

	stop, err = send(`{"type":"session.update","session":{}}`)
	require.NoError(t, err)
	require.False(t, stop, "a rejected client event must leave the connection open")
	rejected := readRealtimeTestFrame(t, client)
	require.Contains(t, rejected, "invalid_request_error")
	require.Contains(t, rejected, "session.update is not supported once the session has started")
	require.NoError(t, providerConn.SetReadDeadline(time.Now().Add(100*time.Millisecond)))
	_, _, err = providerConn.ReadMessage()
	require.Error(t, err, "nothing may reach the provider for a rejected event")
}

// TestRealtimeTranslatorReportsProviderClose checks a provider that reports errors only through its close
// reason reaches the client as an error event first, then the close with the provider's code, and that the
// turn log records the translated error.
func TestRealtimeTranslatorReportsProviderClose(t *testing.T) {
	server, client, cleanup := dialRealtimeTestConn(t)
	defer cleanup()
	upstream, providerConn := dialLiveTestUpstream(t)
	session := bfws.NewSession(nil)
	defer session.Close()
	var recorded *schemas.BifrostError
	session.SetRealtimeTurnHooks(&bfws.RealtimeTurnPluginState{
		RequestID: "turn", StartedAt: time.Now(),
		PostHookRunner: func(_ *schemas.BifrostContext, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
			recorded = err
			return result, err
		},
		Cleanup: func() {},
	})
	closed := make(chan struct {
		code   int
		reason string
	}, 1)
	session.SetRealtimeTranslator(&scriptedRealtimeTranslator{
		onClose: func(code int, reason string) schemas.RealtimeTranslation {
			closed <- struct {
				code   int
				reason string
			}{code, reason}
			return schemas.RealtimeTranslation{Client: []json.RawMessage{json.RawMessage(`{"type":"error","error":{"type":"invalid_request_error","code":"invalid_api_key","message":"the API key was rejected"}}`)}}
		},
	})
	done := make(chan error, 1)
	go func() {
		done <- (&WSRealtimeHandler{}).relayRealtimeProviderToClient(newRealtimeClientConn(server), session, upstream, openAIRealtimeWire, nil, schemas.OpenAI, "realtime", schemas.Key{}, false)
	}()

	const reason = "API key not valid. Please pass a valid API key."
	require.NoError(t, providerConn.WriteControl(ws.CloseMessage, ws.FormatCloseMessage(ws.CloseInvalidFramePayloadData, reason), time.Now().Add(time.Second)))

	require.Contains(t, readRealtimeTestFrame(t, client), "the API key was rejected", "the error event must come before the close")
	require.NoError(t, client.SetReadDeadline(time.Now().Add(time.Second)))
	_, _, err := client.ReadMessage()
	var closeErr *ws.CloseError
	require.ErrorAs(t, err, &closeErr)
	require.Equal(t, ws.CloseInvalidFramePayloadData, closeErr.Code)
	require.Equal(t, reason, closeErr.Text)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("close must end the provider relay")
	}
	got := <-closed
	require.Equal(t, ws.CloseInvalidFramePayloadData, got.code, "the translator must see the provider's close code")
	require.Equal(t, reason, got.reason, "the translator must see the provider's close reason")
	require.NotNil(t, recorded, "the turn must end with the translated error")
	require.Equal(t, "the API key was rejected", recorded.Error.Message)
}

// setupGatedRealtimeTranslator holds client events until the provider has answered its setup frame, then
// releases them in order, like a protocol whose first frame must be a one-shot setup. Its state is plain
// fields: the relay's translation lock is what makes that safe, and -race fails the test without it.
type setupGatedRealtimeTranslator struct {
	ready  bool
	queued []json.RawMessage
}

func (g *setupGatedRealtimeTranslator) Open() schemas.RealtimeTranslation {
	return schemas.RealtimeTranslation{}
}

func (g *setupGatedRealtimeTranslator) ToProvider(event *schemas.BifrostRealtimeEvent) (schemas.RealtimeTranslation, error) {
	if event.Type == schemas.RTEventSessionUpdate {
		return schemas.RealtimeTranslation{Provider: []json.RawMessage{json.RawMessage(`{"setup":true}`)}}, nil
	}
	frame := json.RawMessage(fmt.Sprintf(`{"item":%q}`, event.Item.ID))
	if !g.ready {
		g.queued = append(g.queued, frame)
		return schemas.RealtimeTranslation{}, nil
	}
	return schemas.RealtimeTranslation{Provider: []json.RawMessage{frame}}, nil
}

func (g *setupGatedRealtimeTranslator) Close(int, string) schemas.RealtimeTranslation {
	return schemas.RealtimeTranslation{}
}

func (g *setupGatedRealtimeTranslator) ToClient(frame json.RawMessage) (schemas.RealtimeTranslation, error) {
	g.ready = true
	flushed := g.queued
	g.queued = nil
	return schemas.RealtimeTranslation{Provider: flushed}, nil
}

// TestRealtimeTranslatorKeepsProviderFrameOrderAcrossRelays checks frames reach the provider in the order
// the translator produced them even while both relay directions run: events the client sends while setup is
// still pending must not overtake the ones released when setup completes.
func TestRealtimeTranslatorKeepsProviderFrameOrderAcrossRelays(t *testing.T) {
	const events = 100
	gateway := newLiveOwnerTestGateway(t, nil)
	server, client, cleanup := dialRealtimeTestConn(t)
	defer cleanup()
	upstream, providerConn := dialLiveTestUpstream(t)
	session := bfws.NewSession(nil)
	defer session.Close()
	session.SetRealtimeTranslator(&setupGatedRealtimeTranslator{})
	handler := &WSRealtimeHandler{client: gateway.client, config: gateway.config}
	conn := newRealtimeClientConn(server)
	ctx := liveCallerCtx(t, "")
	relays := make(chan error, 2)
	go func() {
		relays <- handler.relayClientToRealtimeProvider(conn, session, upstream, openAIRealtimeWire, ctx, schemas.OpenAI, "model", schemas.Key{}, false, nil)
	}()
	go func() {
		relays <- handler.relayRealtimeProviderToClient(conn, session, upstream, openAIRealtimeWire, ctx, schemas.OpenAI, "model", schemas.Key{}, false)
	}()
	defer func() {
		_ = client.Close()
		_ = upstream.Close()
		for range 2 {
			select {
			case <-relays:
			case <-time.After(2 * time.Second):
				t.Error("relay did not terminate")
			}
		}
	}()

	go func() {
		_ = client.WriteMessage(ws.TextMessage, []byte(`{"type":"session.update","session":{}}`))
		for i := range events {
			_ = client.WriteMessage(ws.TextMessage, []byte(fmt.Sprintf(`{"type":"conversation.item.create","item":{"id":"item-%03d","type":"message","role":"user"}}`, i)))
		}
	}()

	require.Equal(t, `{"setup":true}`, readRealtimeTestFrame(t, providerConn))
	// Answer setup while the client is still sending, so the release races its later events.
	require.NoError(t, providerConn.WriteMessage(ws.TextMessage, []byte(`{"setupComplete":{}}`)))
	for i := range events {
		require.Equal(t, fmt.Sprintf(`{"item":"item-%03d"}`, i), readRealtimeTestFrame(t, providerConn), "event %d left out of order", i)
	}
}

// BenchmarkRealtimeRelayProviderToClient measures the per-frame cost of the provider-to-client relay for a
// provider whose frames are relayed as they are, the path every OpenAI, Azure and xAI connection takes.
func BenchmarkRealtimeRelayProviderToClient(b *testing.B) {
	gateway := newLiveOwnerTestGateway(b, nil)
	server, client, cleanup := dialRealtimeTestConn(b)
	defer cleanup()
	upstream, providerConn := dialLiveTestUpstream(b)
	session := bfws.NewSession(nil)
	defer session.Close()
	handler := &WSRealtimeHandler{client: gateway.client, config: gateway.config}
	ctx := liveCallerCtx(b, "")
	done := make(chan error, 1)
	go func() {
		done <- handler.relayRealtimeProviderToClient(newRealtimeClientConn(server), session, upstream, &openaiProvider.OpenAIProvider{}, ctx, schemas.OpenAI, "gpt-realtime-mini", schemas.Key{}, false)
	}()
	defer func() {
		_ = upstream.Close()
		<-done
	}()

	frame := []byte(`{"type":"response.output_audio.delta","response_id":"resp","item_id":"item","output_index":0,"content_index":0,"delta":"` + strings.Repeat("AQID", 700) + `"}`)
	received := make(chan struct{})
	go func() {
		for range b.N {
			if _, _, err := client.ReadMessage(); err != nil {
				break
			}
		}
		close(received)
	}()
	b.ReportAllocs()
	b.SetBytes(int64(len(frame)))
	b.ResetTimer()
	for range b.N {
		if err := providerConn.WriteMessage(ws.TextMessage, frame); err != nil {
			b.Fatal(err)
		}
	}
	<-received
}

// BenchmarkRealtimeRelayClientToProvider measures the per-event cost of the client-to-provider relay for a
// provider whose events map one to one, using the audio append that dominates a voice session.
func BenchmarkRealtimeRelayClientToProvider(b *testing.B) {
	gateway := newLiveOwnerTestGateway(b, nil)
	server, client, cleanup := dialRealtimeTestConn(b)
	defer cleanup()
	upstream, providerConn := dialLiveTestUpstream(b)
	session := bfws.NewSession(nil)
	defer session.Close()
	handler := &WSRealtimeHandler{client: gateway.client, config: gateway.config}
	conn := newRealtimeClientConn(server)
	ctx := liveCallerCtx(b, "")
	provider := &openaiProvider.OpenAIProvider{}

	event := []byte(`{"type":"input_audio_buffer.append","audio":"` + strings.Repeat("AQID", 700) + `"}`)
	received := make(chan struct{})
	go func() {
		for range b.N {
			if _, _, err := providerConn.ReadMessage(); err != nil {
				break
			}
		}
		close(received)
	}()
	_ = client
	b.ReportAllocs()
	b.SetBytes(int64(len(event)))
	b.ResetTimer()
	for range b.N {
		if stop, err := handler.processRealtimeClientMessage(conn, session, upstream, provider, ctx, schemas.OpenAI, "gpt-realtime-mini", schemas.Key{}, false, ws.TextMessage, event); stop || err != nil {
			b.Fatalf("relay stopped: %v", err)
		}
	}
	<-received
}

// The Live frames below are trimmed captures from a session against gemini-3.8-live.
const (
	liveTestAudioFrame = `{"serverContent":{"modelTurn":{"parts":[{"inlineData":{"mimeType":"audio/pcm;rate=24000","data":"AQADAAQAEgA="}}],"role":"model"},"outputTranscription":{"text":"Hello there!"}}}`
	liveTestTurnDone   = `{"serverContent":{"turnComplete":true},"usageMetadata":{"promptTokenCount":548,"responseTokenCount":30,"totalTokenCount":578,"promptTokensDetails":[{"modality":"TEXT","tokenCount":306},{"modality":"AUDIO","tokenCount":222}],"responseTokensDetails":[{"modality":"AUDIO","tokenCount":30}],"thoughtsTokenCount":40}}`
)

// geminiLiveAccount gives the gateway one Gemini key whose upstream is the test's fake Live server.
type geminiLiveAccount struct{ baseURL string }

func (geminiLiveAccount) GetConfiguredProviders() ([]schemas.ModelProvider, error) {
	return []schemas.ModelProvider{schemas.Gemini}, nil
}

func (geminiLiveAccount) GetKeysForProvider(context.Context, schemas.ModelProvider) ([]schemas.Key, error) {
	return []schemas.Key{{
		ID: "gemini-key", Name: "gemini", Value: *schemas.NewSecretVar("sk-gemini-test"), Models: schemas.WhiteList{"*"}, Weight: 1,
		Aliases: schemas.KeyAliases{"alias-live": {ModelID: "gemini-3.8-live"}},
	}}, nil
}

func (a geminiLiveAccount) GetConfigForProvider(schemas.ModelProvider) (*schemas.ProviderConfig, error) {
	network := schemas.DefaultNetworkConfig
	network.BaseURL = a.baseURL
	return &schemas.ProviderConfig{NetworkConfig: network, ConcurrencyAndBufferSize: schemas.DefaultConcurrencyAndBufferSize}, nil
}

// liveTurnRecorder records the turns the pipeline logs and bills.
type liveTurnRecorder struct {
	mu        sync.Mutex
	requests  []*schemas.BifrostRequest
	responses []*schemas.BifrostResponse
}

func (*liveTurnRecorder) GetName() string { return "live-turn-recorder" }
func (*liveTurnRecorder) Cleanup() error  { return nil }
func (*liveTurnRecorder) PreRequestHook(*schemas.BifrostContext, *schemas.BifrostRequest) error {
	return nil
}

func (r *liveTurnRecorder) PreLLMHook(_ *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests = append(r.requests, req)
	return req, nil, nil
}

func (r *liveTurnRecorder) PostLLMHook(_ *schemas.BifrostContext, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.responses = append(r.responses, resp)
	return resp, bifrostErr, nil
}

func (r *liveTurnRecorder) turns() (int, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.requests), len(r.responses)
}

// fakeGeminiLive is a Live server. It records what the gateway sends and the key it authenticates with,
// and answers each frame with what script returns, as binary frames the way Live sends them.
type fakeGeminiLive struct {
	url    string
	mu     sync.Mutex
	frames []string
	keys   []string
}

func newFakeGeminiLive(t *testing.T, script func(frame []byte, frames int) []string) *fakeGeminiLive {
	t.Helper()
	fake := &fakeGeminiLive{}
	upgrader := ws.FastHTTPUpgrader{CheckOrigin: func(*fasthttp.RequestCtx) bool { return true }}
	r := router.New()
	r.GET("/ws/google.ai.generativelanguage.v1beta.GenerativeService.BidiGenerateContent", func(ctx *fasthttp.RequestCtx) {
		key := string(ctx.Request.Header.Peek("x-goog-api-key"))
		_ = upgrader.Upgrade(ctx, func(conn *ws.Conn) {
			defer conn.Close()
			fake.mu.Lock()
			fake.keys = append(fake.keys, key)
			fake.mu.Unlock()
			for {
				_, frame, err := conn.ReadMessage()
				if err != nil {
					return
				}
				fake.mu.Lock()
				fake.frames = append(fake.frames, string(frame))
				count := len(fake.frames)
				fake.mu.Unlock()
				for _, reply := range script(frame, count) {
					if conn.WriteMessage(ws.BinaryMessage, []byte(reply)) != nil {
						return
					}
				}
			}
		})
	})
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv := &fasthttp.Server{Handler: r.Handler, KeepHijackedConns: true}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Shutdown() })
	fake.url = "http://" + ln.Addr().String()
	return fake
}

func (f *fakeGeminiLive) received() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.frames...)
}

// newGeminiLiveGateway serves the gateway's Live route over a fake Gemini, and returns the URL a Google
// GenAI client dials.
func newGeminiLiveGateway(t *testing.T, upstream *fakeGeminiLive, enforceAuth bool, recorder *liveTurnRecorder) string {
	t.Helper()
	client, err := bifrost.Init(context.Background(), schemas.BifrostConfig{
		Account:    geminiLiveAccount{baseURL: upstream.url},
		LLMPlugins: []schemas.LLMPlugin{recorder},
		Logger:     bifrost.NewDefaultLogger(schemas.LogLevelError),
	})
	require.NoError(t, err)
	t.Cleanup(client.Shutdown)
	kv, err := kvstore.New(kvstore.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = kv.Close() })
	config := &lib.Config{ClientConfig: &configstore.ClientConfig{EnforceAuthOnInference: enforceAuth}, KVStore: kv}
	pool := bfws.NewPool(nil)
	t.Cleanup(pool.Close)
	handler := &WSRealtimeHandler{client: client, config: config, handlerStore: config, pool: pool, sessions: bfws.NewSessionManager(10)}
	r := router.New()
	handler.RegisterRoutes(r)
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv := &fasthttp.Server{Handler: r.Handler, KeepHijackedConns: true}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Shutdown() })
	return "ws://" + ln.Addr().String() + "/genai/ws/google.ai.generativelanguage.v1beta.GenerativeService.BidiGenerateContent"
}

func dialGeminiLive(t *testing.T, url string) *ws.Conn {
	t.Helper()
	conn, _, err := ws.DefaultDialer.Dial(url, map[string][]string{"x-goog-api-key": {"client-key"}})
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// readGeminiLiveFrame reads the next frame the client receives and says whether Live's binary framing survived.
func readGeminiLiveFrame(t *testing.T, conn *ws.Conn) (string, int) {
	t.Helper()
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(5*time.Second)))
	messageType, frame, err := conn.ReadMessage()
	require.NoError(t, err)
	return string(frame), messageType
}

// TestGeminiLiveRouteRelaysANativeSession drives the route a Google GenAI client dials through the real
// handler: the model comes from the setup frame, the gateway's key replaces the client's, every frame
// reaches Live and the client as it was written, and each turn is logged and billed from Live's own usage.
func TestGeminiLiveRouteRelaysANativeSession(t *testing.T) {
	recorder := &liveTurnRecorder{}
	upstream := newFakeGeminiLive(t, func(frame []byte, _ int) []string {
		switch text := gjson.GetBytes(frame, "realtimeInput.text").String(); {
		case gjson.GetBytes(frame, "setup").Exists():
			return []string{`{"setupComplete":{}}`}
		case text == "Say hi":
			return []string{liveTestAudioFrame, liveTestTurnDone}
		case text == "Count slowly":
			return []string{liveTestAudioFrame}
		case text == "Stop":
			return []string{liveTestTurnDone}
		}
		return nil
	})
	conn := dialGeminiLive(t, newGeminiLiveGateway(t, upstream, false, recorder))

	setup := `{"setup":{"model":"models/alias-live","generationConfig":{"responseModalities":["AUDIO"]},"outputAudioTranscription":{}}}`
	require.NoError(t, conn.WriteMessage(ws.TextMessage, []byte(setup)))
	frame, messageType := readGeminiLiveFrame(t, conn)
	require.Equal(t, `{"setupComplete":{}}`, frame)
	require.Equal(t, ws.BinaryMessage, messageType, "Live's binary framing reaches the client")
	require.JSONEq(t, `{"setup":{"model":"models/gemini-3.8-live","generationConfig":{"responseModalities":["AUDIO"]},"outputAudioTranscription":{}}}`, upstream.received()[0],
		"Live gets the model the key's alias resolves to and every other setting as the client wrote it")
	require.Equal(t, []string{"sk-gemini-test"}, upstream.keys, "Live is called with the gateway's key, never the client's")

	// A turn: the client's text, then Live's audio and the frame that ends the turn, all untouched.
	require.NoError(t, conn.WriteMessage(ws.TextMessage, []byte(`{"realtimeInput":{"text":"Say hi"}}`)))
	for _, want := range []string{liveTestAudioFrame, liveTestTurnDone} {
		frame, _ = readGeminiLiveFrame(t, conn)
		require.Equal(t, want, frame)
	}
	require.Equal(t, `{"realtimeInput":{"text":"Say hi"}}`, upstream.received()[1])
	require.Eventually(t, func() bool { _, responses := recorder.turns(); return responses == 1 }, 2*time.Second, 10*time.Millisecond)

	recorder.mu.Lock()
	request, response := recorder.requests[0].ResponsesRequest, recorder.responses[0].ResponsesResponse
	recorder.mu.Unlock()
	require.Equal(t, schemas.Gemini, request.Provider)
	require.Contains(t, string(mustMarshal(t, request.Input)), "Say hi", "the turn is logged with what the user asked")
	require.Equal(t, 548, response.Usage.InputTokens)
	require.Equal(t, 70, response.Usage.OutputTokens, "thinking tokens bill as output, as on the OpenAI-protocol route")
	require.Equal(t, 618, response.Usage.TotalTokens)
	require.Contains(t, string(mustMarshal(t, response.Output)), "Hello there!", "and with what the model said")

	// Speaking over the model is allowed: the response in progress ends and the session carries on.
	require.NoError(t, conn.WriteMessage(ws.TextMessage, []byte(`{"realtimeInput":{"text":"Count slowly"}}`)))
	frame, _ = readGeminiLiveFrame(t, conn)
	require.Equal(t, liveTestAudioFrame, frame)
	require.NoError(t, conn.WriteMessage(ws.BinaryMessage, []byte(`{"realtimeInput":{"text":"Stop"}}`)))
	frame, _ = readGeminiLiveFrame(t, conn)
	require.Equal(t, liveTestTurnDone, frame, "a second input while a turn is open is relayed, not refused, and the connection stays up")
	require.Eventually(t, func() bool { requests, responses := recorder.turns(); return requests == 2 && responses == 2 }, 2*time.Second, 10*time.Millisecond,
		"both inputs belong to the one turn that Live ended")
}

// TestGeminiLiveRouteRefusesWhatLiveWouldRefuse checks a bad connection fails the way Live fails one: with a
// close frame whose code and reason say why, before anything is sent upstream.
func TestGeminiLiveRouteRefusesWhatLiveWouldRefuse(t *testing.T) {
	upstream := newFakeGeminiLive(t, func([]byte, int) []string { return nil })
	url := newGeminiLiveGateway(t, upstream, false, &liveTurnRecorder{})

	for name, tc := range map[string]struct {
		first  string
		reason string
	}{
		"the first frame is not a setup": {`{"realtimeInput":{"text":"hi"}}`, "must be a setup message"},
		"the setup names no model":       {`{"setup":{}}`, "setup.model is required"},
		"the provider is not Gemini":     {`{"setup":{"model":"models/azure/gpt-realtime"}}`, "does not speak the gemini live protocol"},
	} {
		conn := dialGeminiLive(t, url)
		require.NoError(t, conn.WriteMessage(ws.TextMessage, []byte(tc.first)), name)
		require.NoError(t, conn.SetReadDeadline(time.Now().Add(5*time.Second)))
		_, _, err := conn.ReadMessage()
		var closed *ws.CloseError
		require.ErrorAs(t, err, &closed, name)
		require.Equal(t, ws.CloseInvalidFramePayloadData, closed.Code, name)
		require.Contains(t, closed.Text, tc.reason, name)
	}
	require.Empty(t, upstream.received(), "a refused session never reaches Live")

	// With authentication enforced, a caller that presents nothing is turned away before the upgrade.
	enforced := newGeminiLiveGateway(t, upstream, true, &liveTurnRecorder{})
	_, response, err := ws.DefaultDialer.Dial(enforced, nil)
	require.ErrorIs(t, err, ws.ErrBadHandshake)
	require.Equal(t, fasthttp.StatusUnauthorized, response.StatusCode)
}

func mustMarshal(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := schemas.Marshal(value)
	require.NoError(t, err)
	return encoded
}

// TestNativeCloseFrame pins how a failure is told to a client that speaks Live: the close code Live uses
// for that kind of failure, and a reason that always fits a close frame.
func TestNativeCloseFrame(t *testing.T) {
	for status, want := range map[int]int{
		400: ws.CloseInvalidFramePayloadData,
		401: ws.ClosePolicyViolation,
		403: ws.ClosePolicyViolation,
		404: ws.ClosePolicyViolation,
		429: ws.CloseTryAgainLater,
		500: ws.CloseInternalServerErr,
		502: ws.CloseInternalServerErr,
	} {
		code, reason := nativeCloseFrame(newRealtimeWireBifrostError(status, "server_error", "why"))
		require.Equal(t, want, code, status)
		require.Equal(t, "why", reason)
	}

	_, reason := nativeCloseFrame(newRealtimeWireBifrostError(400, "invalid_request_error", strings.Repeat("é", 100)))
	require.LessOrEqual(t, len(reason), maxCloseReasonBytes)
	require.True(t, strings.HasSuffix(reason, "é"), "the reason is cut between characters, never inside one")
	code, reason := nativeCloseFrame(nil)
	require.Equal(t, ws.CloseInternalServerErr, code)
	require.NotEmpty(t, reason)

	// A native client has no error event to receive, so an error written to it closes the connection.
	server, client, cleanup := dialRealtimeTestConn(t)
	defer cleanup()
	conn := newRealtimeClientConn(server)
	conn.native = true
	conn.writeRealtimeError(newRealtimeWireBifrostError(429, "rate_limit_exceeded", "slow down"))
	require.NoError(t, client.SetReadDeadline(time.Now().Add(3*time.Second)))
	_, _, err := client.ReadMessage()
	var closed *ws.CloseError
	require.ErrorAs(t, err, &closed)
	require.Equal(t, ws.CloseTryAgainLater, closed.Code)
	require.Equal(t, "slow down", closed.Text)
}
